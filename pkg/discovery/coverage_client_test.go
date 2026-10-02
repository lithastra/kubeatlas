// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func coverageView(t *testing.T, tracker *operations.CoverageTracker, cluster string, gvr schema.GroupVersionResource) operations.ResourceCoverage {
	t.Helper()
	got, err := tracker.Snapshot(cluster, []schema.GroupVersionResource{gvr}, time.Now())
	if err != nil || len(got.Resources) != 1 {
		t.Fatalf("coverage snapshot: %+v %v", got, err)
	}
	return got.Resources[0]
}

func awaitCoverage(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for synthetic observation")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type coverageStubClient struct {
	dynamic.Interface
	resource *coverageStubResource
}

func (c *coverageStubClient) Resource(schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return c.resource
}

type coverageStubResource struct {
	dynamic.NamespaceableResourceInterface
	list  func(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error)
	watch func(context.Context, metav1.ListOptions) (watch.Interface, error)
}

func (r *coverageStubResource) Namespace(string) dynamic.ResourceInterface { return r }
func (r *coverageStubResource) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return r.list(ctx, opts)
}
func (r *coverageStubResource) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return r.watch(ctx, opts)
}

func TestCoverageClientPreservesRequestsEventsAndWatchListCapability(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	session, err := tracker.Begin("prod", []schema.GroupVersionResource{configMapGVR}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stream := watch.NewRaceFreeFake()
	initial := true
	opts := metav1.ListOptions{ResourceVersion: "17", AllowWatchBookmarks: true, SendInitialEvents: &initial, ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan}
	list := &unstructured.UnstructuredList{}
	var listCalls, watchCalls int
	stub := &coverageStubClient{resource: &coverageStubResource{
		list: func(_ context.Context, actual metav1.ListOptions) (*unstructured.UnstructuredList, error) {
			listCalls++
			if !reflect.DeepEqual(actual, opts) {
				t.Error("list options changed")
			}
			return list, nil
		},
		watch: func(_ context.Context, actual metav1.ListOptions) (watch.Interface, error) {
			watchCalls++
			if !reflect.DeepEqual(actual, opts) {
				t.Error("watch/streaming-list options changed")
			}
			return stream, nil
		},
	}}
	client := &coverageClient{Interface: stub, session: session}
	if client.IsWatchListSemanticsUnSupported() {
		t.Fatal("adapter disabled streaming list")
	}
	client.Interface = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if !client.IsWatchListSemanticsUnSupported() {
		t.Fatal("adapter hid the fake client's streaming-list exclusion")
	}
	client.Interface = stub
	resource := client.Resource(configMapGVR)
	returned, err := resource.List(context.Background(), opts)
	if err != nil || returned != list || listCalls != 1 {
		t.Fatal("list result changed or extra request made")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := resource.Namespace("").Watch(ctx, opts)
	if err != nil || watchCalls != 1 {
		t.Fatal("watch changed or extra request made")
	}
	defer w.Stop()
	object := resyncObject("ConfigMap", "synthetic-object-canary", "uid", "1")
	for _, eventType := range []watch.EventType{watch.Added, watch.Modified, watch.Bookmark, watch.Deleted} {
		stream.Action(eventType, object)
		select {
		case event := <-w.ResultChan():
			if event.Type != eventType || event.Object != object {
				t.Fatal("adapter changed event or streaming-list bookmark")
			}
		case <-time.After(time.Second):
			t.Fatal("event was dropped")
		}
	}
	session.InitialDeliveryComplete(configMapGVR)
	if got := coverageView(t, tracker, "prod", configMapGVR); got.State != operations.CoverageObserved {
		t.Fatalf("healthy observation = %+v", got)
	}
	stream.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: 403, Message: "synthetic-sensitive-error"})
	select {
	case event := <-w.ResultChan():
		if event.Type != watch.Error {
			t.Fatal("error event suppressed")
		}
	case <-time.After(time.Second):
		t.Fatal("error event lost")
	}
	w.Stop()
	got := coverageView(t, tracker, "prod", configMapGVR)
	if !slices.Contains(got.Reasons, operations.CoverageWatchDenied) || got.State != operations.CoveragePartial {
		t.Fatalf("stream permission error lost: %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil || strings.Contains(string(body), "canary") || strings.Contains(string(body), "sensitive-error") {
		t.Fatal("coverage retained object/error content")
	}
}

func TestCoverageClientDeniedRequestsAndBlockedConsumerShutdown(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	session, err := tracker.Begin("", []schema.GroupVersionResource{configMapGVR}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	denied := apierrors.NewForbidden(configMapGVR.GroupResource(), "synthetic-name", errors.New("synthetic-sensitive-error"))
	resource := &coverageResourceClient{session: session, gvr: configMapGVR, ResourceInterface: &coverageStubResource{
		list:  func(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error) { return nil, denied },
		watch: func(context.Context, metav1.ListOptions) (watch.Interface, error) { return nil, denied },
	}}
	if _, err := resource.List(context.Background(), metav1.ListOptions{}); err != denied {
		t.Fatal("list error changed")
	}
	if _, err := resource.Watch(context.Background(), metav1.ListOptions{}); err != denied {
		t.Fatal("watch error changed")
	}
	got := coverageView(t, tracker, "", configMapGVR)
	if !slices.Contains(got.Reasons, operations.CoverageListDenied) || !slices.Contains(got.Reasons, operations.CoverageWatchDenied) {
		t.Fatalf("request denial not recorded: %+v", got)
	}
	for _, stopByContext := range []bool{false, true} {
		stream := watch.NewRaceFreeFake()
		resource.ResourceInterface = &coverageStubResource{watch: func(context.Context, metav1.ListOptions) (watch.Interface, error) { return stream, nil }}
		ctx, cancel := context.WithCancel(context.Background())
		w, err := resource.Watch(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		stream.Add(resyncObject("ConfigMap", "blocked-consumer", "uid", "1"))
		if stopByContext {
			cancel()
		} else {
			w.Stop()
		}
		select {
		case <-w.ResultChan():
			// A racing send may deliver one already-received event; drain below.
		case <-time.After(time.Second):
			t.Fatal("blocked watch did not terminate")
		}
		cancel()
		w.Stop()
		select {
		case _, open := <-w.ResultChan():
			if open {
				t.Fatal("forwarder retained unexpected buffered events")
			}
		case <-time.After(time.Second):
			t.Fatal("watch goroutine/channel leaked after stop")
		}
		if !stream.IsStopped() {
			t.Fatal("underlying watch was not stopped")
		}
	}
	stream := watch.NewRaceFreeFake()
	resource.ResourceInterface = &coverageStubResource{watch: func(context.Context, metav1.ListOptions) (watch.Interface, error) { return stream, nil }}
	w, err := resource.Watch(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	stream.Stop() // Server-side close, not a caller's Stop or context cancellation.
	select {
	case _, open := <-w.ResultChan():
		if open {
			t.Fatal("closed upstream produced an event")
		}
	case <-time.After(time.Second):
		t.Fatal("upstream close was not propagated")
	}
	if got := coverageView(t, tracker, "", configMapGVR); got.WatchEstablished || !slices.Contains(got.Reasons, operations.CoverageWatchClosed) {
		t.Fatal("disconnected upstream still reported established")
	}
	resource.ResourceInterface = &coverageStubResource{watch: func(context.Context, metav1.ListOptions) (watch.Interface, error) { return nil, nil }}
	if w, err := resource.Watch(context.Background(), metav1.ListOptions{}); err == nil || w != nil {
		t.Fatal("nil watch stream became successful observation")
	}
}

func TestInformerCoverageInitialDeliveryDenialAndSecretExclusion(t *testing.T) {
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	secret := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	object := resyncObject("ConfigMap", "initial", "uid", "1")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{configMapGVR: "ConfigMapList", pods: "PodList"}, object)
	var deniedLists atomic.Int64
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deniedLists.Add(1)
		return true, nil, apierrors.NewForbidden(pods.GroupResource(), "", errors.New("synthetic denial"))
	})
	started, release := make(chan struct{}), make(chan struct{})
	var blockOnce atomic.Bool
	store := &coverageBlockingStore{GraphStore: memory.New(), before: func() {
		if blockOnce.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
	}}
	tracker := operations.NewCoverageTracker(time.Minute)
	mgr := NewInformerManager(client, store, WithCoverage(tracker), WithClusterID("prod"), WithGVRs([]schema.GroupVersionResource{configMapGVR, pods, secret}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := mgr.Start(ctx)
		mgr.factory.Shutdown()
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("informer did not stop")
		}
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("initial handler did not start")
	}
	awaitCoverage(t, func() bool {
		return coverageView(t, tracker, "prod", configMapGVR).WatchEstablished && deniedLists.Load() > 0
	})
	if got := coverageView(t, tracker, "prod", configMapGVR); got.InitialDeliveryDone || got.State != operations.CoverageInitializing {
		t.Fatalf("list/cache completion passed a blocked initial write: %+v", got)
	}
	close(release)
	awaitCoverage(t, func() bool {
		return coverageView(t, tracker, "prod", configMapGVR).State == operations.CoverageObserved
	})
	if got := coverageView(t, tracker, "prod", pods); got.State != operations.CoveragePartial || !slices.Contains(got.Reasons, operations.CoverageListDenied) {
		t.Fatalf("healthy type concealed denied watcher: %+v", got)
	}
	if got := coverageView(t, tracker, "prod", secret); got.State != operations.CoverageUnknown {
		t.Fatal("Secret watch was registered")
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "secrets" || action.GetVerb() != "list" && action.GetVerb() != "watch" {
			t.Fatalf("observation introduced an extra/mutating/Secret request: %s %s", action.GetVerb(), action.GetResource())
		}
	}
}

type coverageBlockingStore struct {
	graph.GraphStore
	before func()
}

func (s *coverageBlockingStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	s.before()
	return s.GraphStore.UpsertResource(ctx, r)
}

type coverageDeleteFailure struct{ graph.GraphStore }

func (s *coverageDeleteFailure) DeleteResource(context.Context, string) error {
	return errors.New("synthetic delete failure")
}

func TestInformerCoverageProcessingAndPersistenceGaps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason operations.CoverageReason
	}{
		{"resource", operations.CoveragePersistenceFailed}, {"edge", operations.CoveragePersistenceFailed},
		{"reference", operations.CoveragePersistenceFailed}, {"delete", operations.CoveragePersistenceFailed},
		{"extract", operations.CoverageExtractionFailed}, {"convert", operations.CoverageProcessingFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			session, err := tracker.Begin("", []schema.GroupVersionResource{configMapGVR}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			session.InitialDeliveryComplete(configMapGVR)
			session.WatchStarted(configMapGVR)
			store := &resyncStore{GraphStore: memory.New()}
			mgr := NewInformerManager(nil, store, WithExtractor(secretEdgeExtractor{}))
			mgr.coverageSession = session
			object := resyncObject("ConfigMap", "synthetic-object-canary", "uid", "1")
			switch tc.name {
			case "resource":
				store.failResource.Store(true)
			case "edge":
				store.failEdge.Store(true)
			case "reference":
				store.failSecret.Store(true)
			case "extract":
				mgr.extractor = resyncExtractorFunc(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
					return nil, errors.New("synthetic extract failure")
				})
			case "delete":
				mgr.store = &coverageDeleteFailure{GraphStore: store}
			}
			switch tc.name {
			case "convert":
				mgr.handleUpsert(context.Background(), configMapGVR, "malformed", graph.EventTypeAdd)
			case "delete":
				mgr.handleDelete(context.Background(), configMapGVR, object)
			default:
				mgr.handleUpsert(context.Background(), configMapGVR, object, graph.EventTypeAdd)
			}
			if got := coverageView(t, tracker, "", configMapGVR); got.State != operations.CoveragePartial || !slices.Contains(got.Reasons, tc.reason) {
				t.Fatalf("failed processing was presented as observed: %+v", got)
			}
			store.failResource.Store(false)
			store.failEdge.Store(false)
			store.failSecret.Store(false)
			mgr.store, mgr.extractor = store, noopRegistry{}
			mgr.handleUpsert(context.Background(), configMapGVR, object, graph.EventTypeUpdate)
			session.ListResult(configMapGVR, true, false, time.Now())
			session.WatchStarted(configMapGVR)
			if got := coverageView(t, tracker, "", configMapGVR); got.State != operations.CoveragePartial || !slices.Contains(got.Reasons, tc.reason) {
				t.Fatal("one successful event or relist erased an unproven data gap")
			}
		})
	}
}
