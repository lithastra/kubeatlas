// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type memberHistoryBounds struct {
	reader           graph.HistoryBoundsReader
	calls            atomic.Int32
	blockEast        atomic.Bool
	entered, release chan struct{}
}

func (r *memberHistoryBounds) ReadHistoryBounds(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
	r.calls.Add(1)
	if q.ClusterID == "east" && r.blockEast.Load() {
		close(r.entered)
		<-r.release
	}
	return r.reader.ReadHistoryBounds(ctx, q)
}

func TestMemberFactoryOwnsHistoryRetentionAcrossRemovalAndReattachment(t *testing.T) {
	store := memory.New()
	base := time.Now().Truncate(time.Microsecond)
	for i, cluster := range []string{"east", "west"} {
		if err := store.AppendEvent(context.Background(), graph.ResourceEvent{ClusterID: cluster, Kind: "Pod", Name: "history-object", EventType: graph.EventTypeUpdate, Timestamp: base.Add(-time.Duration(i+1) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	reader := &memberHistoryBounds{reader: store, entered: make(chan struct{}), release: make(chan struct{})}
	retained := snapshot.NewMemberHistoryRetention(reader, time.Hour)
	tracker := operations.NewCoverageTracker(time.Minute)
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods})}, tracker, nil, extractor.Default(), discovery.NewDynamicMetrics(), retained, nil)
	start := func(cluster string) func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		client := memberDynamicClient(t)
		go func() { done <- factory(cluster, client, memberMetadata{}, store).Start(ctx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Errorf("member stop: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("member history worker not joined")
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	stopEast := start("east")
	start("west")
	awaitMember(t, "both member history caches", func() bool {
		for _, cluster := range []string{"east", "west"} {
			e, err := retained.SnapshotRetention(context.Background(), cluster)
			if err != nil || e.State != operations.RetentionObserved {
				return false
			}
		}
		return true
	})
	stopEast()
	east, _ := retained.SnapshotRetention(context.Background(), "east")
	west, _ := retained.SnapshotRetention(context.Background(), "west")
	if !east.Stopped || west.Stopped || !east.Bounds.FirstEventAt.Equal(base.Add(-time.Minute)) || !west.Bounds.FirstEventAt.Equal(base.Add(-2*time.Minute)) {
		t.Fatal("member removal crossed cache/lifetime boundaries")
	}
	reader.blockEast.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	client := memberDynamicClient(t)
	var released sync.Once
	unblock := func() { released.Do(func() { close(reader.release) }) }
	t.Cleanup(func() { cancel(); unblock() })
	go func() { done <- factory("east", client, memberMetadata{}, store).Start(ctx) }()
	select {
	case <-reader.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("reattached observer did not start")
	}
	east, _ = retained.SnapshotRetention(context.Background(), "east")
	if east.State != operations.RetentionNotQueried || east.Stopped || !east.CheckedAt.IsZero() {
		t.Fatal("reattachment inherited stale member history")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("member bundle returned before its history query finished")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("member failed to join cancelled history query")
	}
	east, _ = retained.SnapshotRetention(context.Background(), "east")
	if !east.Stopped || east.State != operations.RetentionFailed || !east.Bounds.FirstEventAt.IsZero() {
		t.Fatal("cancelled query became retained-history success")
	}
	if reader.calls.Load() != 3 {
		t.Fatal("member requests refreshed another scope or used standalone history")
	}
}
