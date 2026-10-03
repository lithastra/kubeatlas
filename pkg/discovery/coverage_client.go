// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/watchlist"
)

// This adapter observes the existing list/watch only: no probes, extra requests,
// persistent object retention, or unbounded buffering. The underlying client's streaming
// list capability and options are preserved, including fake-client behavior.
type coverageClient struct {
	dynamic.Interface
	session *operations.CoverageSession
}

// ObserveCoverageClient observes only the existing client's list/watch traffic
// for this registration token. It neither probes nor changes resource options.
func ObserveCoverageClient(client dynamic.Interface, session *operations.CoverageSession) dynamic.Interface {
	if session == nil {
		return client
	}
	return &coverageClient{Interface: client, session: session}
}

func (c *coverageClient) IsWatchListSemanticsUnSupported() bool {
	return watchlist.DoesClientNotSupportWatchListSemantics(c.Interface)
}

func (c *coverageClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	r := c.Interface.Resource(gvr)
	return &coverageNamespaceable{NamespaceableResourceInterface: r, coverageResourceClient: coverageResourceClient{ResourceInterface: r, session: c.session, gvr: gvr}}
}

type coverageNamespaceable struct {
	dynamic.NamespaceableResourceInterface
	coverageResourceClient
}

func (c *coverageNamespaceable) Namespace(namespace string) dynamic.ResourceInterface {
	return &coverageResourceClient{ResourceInterface: c.NamespaceableResourceInterface.Namespace(namespace), session: c.session, gvr: c.gvr}
}

// Resolve the two embedded interfaces explicitly; only these methods observe.
func (c *coverageNamespaceable) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return c.coverageResourceClient.List(ctx, opts)
}

func (c *coverageNamespaceable) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.coverageResourceClient.Watch(ctx, opts)
}

type coverageResourceClient struct {
	dynamic.ResourceInterface
	session *operations.CoverageSession
	gvr     schema.GroupVersionResource
}

func (c *coverageResourceClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	list, err := c.ResourceInterface.List(ctx, opts)
	c.session.ListResult(c.gvr, err == nil, coverageDenied(err), time.Now())
	return list, err
}

func (c *coverageResourceClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	stream, err := c.ResourceInterface.Watch(ctx, opts)
	if err == nil && stream == nil {
		err = errors.New("discovery: watch returned no stream")
	}
	if err != nil {
		c.session.WatchEnded(c.gvr, 0, coverageWatchReason(err), time.Now())
		return stream, err
	}
	w := &coverageWatch{source: stream, session: c.session, gvr: c.gvr, epoch: c.session.WatchStarted(c.gvr), done: make(chan struct{}), events: make(chan watch.Event)}
	go w.forward(ctx)
	return w, nil
}

func coverageDenied(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
}

func coverageWatchReason(err error) operations.CoverageReason {
	if coverageDenied(err) {
		return operations.CoverageWatchDenied
	}
	return operations.CoverageWatchFailed
}

type coverageWatch struct {
	source  watch.Interface
	session *operations.CoverageSession
	gvr     schema.GroupVersionResource
	epoch   uint64
	once    sync.Once
	done    chan struct{}
	events  chan watch.Event
}

func (w *coverageWatch) Stop() {
	w.once.Do(func() {
		close(w.done)
		w.session.WatchEnded(w.gvr, w.epoch, operations.CoverageWatchClosed, time.Now())
		w.source.Stop()
	})
}

func (w *coverageWatch) ResultChan() <-chan watch.Event { return w.events }

func (w *coverageWatch) forward(ctx context.Context) {
	defer close(w.events)
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case event, ok := <-w.source.ResultChan():
			if !ok {
				return
			}
			if event.Type == watch.Error {
				w.session.WatchEnded(w.gvr, w.epoch, coverageWatchReason(apierrors.FromObject(event.Object)), time.Now())
			}
			// Pass every event (including streaming-list bookmarks) unchanged.
			// A stopped consumer cannot strand this goroutine on a send.
			select {
			case <-ctx.Done():
				return
			case <-w.done:
				return
			case w.events <- event:
			}
		}
	}
}
