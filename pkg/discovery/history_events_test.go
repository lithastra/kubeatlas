// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/client-go/tools/cache"
)

func TestInformerHistoryClusterAttribution(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	writer := snapshot.New(store, snapshot.Config{QueueSize: 128, Workers: 1}, nil)
	writer.Start(ctx)
	t.Cleanup(writer.Stop)
	from := time.Now().Add(-time.Second)
	var wg sync.WaitGroup
	// Deliberately identical namespace/name/UID/RVs across independently managed
	// clusters. Object annotations cannot override the trusted manager identity.
	for _, cluster := range []string{"", "east", "west"} {
		wg.Go(func() {
			mgr := NewInformerManager(nil, store, WithClusterID(cluster), WithSnapshotSink(writer))
			u := resyncObject("ConfigMap", "same", "same-uid", "1")
			u.SetAnnotations(map[string]string{"clusterId": "spoofed"})
			mgr.handleUpsert(ctx, configMapGVR, u, graph.EventTypeAdd)
			mgr.handleUpsert(ctx, configMapGVR, u.DeepCopy(), graph.EventTypeUpdate)
			u.SetResourceVersion("2")
			mgr.handleUpsert(ctx, configMapGVR, u, graph.EventTypeUpdate)
			// Exercise both ordinary deletes and tombstones with a missing Kind.
			u.SetKind("")
			if cluster == "west" {
				mgr.handleDelete(ctx, configMapGVR, cache.DeletedFinalStateUnknown{Obj: u})
			} else {
				mgr.handleDelete(ctx, configMapGVR, u)
			}
		})
	}
	wg.Wait()
	writer.Stop()
	if got := writer.Metrics().Snapshot(); got.QueueDropped != 0 || got.WriteFailed != 0 {
		t.Fatalf("fixture lost events: %+v", got)
	}
	for _, cluster := range []string{"", "east", "west"} {
		page, err := store.ReadHistoryEvents(ctx, graph.HistoryEventQuery{
			ClusterID: cluster, From: from, To: time.Now().Add(time.Second),
		})
		if err != nil || page.Truncated || len(page.Events) != 3 {
			t.Fatalf("cluster %q: events=%d truncated=%v err=%v", cluster, len(page.Events), page.Truncated, err)
		}
		for i, typ := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate, graph.EventTypeDelete} {
			e := page.Events[i]
			version := "2"
			if i == 0 {
				version = "1"
			}
			if e.ClusterID != cluster || e.Namespace != "test" || e.Kind != "ConfigMap" || e.Name != "same" ||
				e.UID != "same-uid" || e.ResourceVersion != version || e.EventType != typ {
				t.Fatalf("event lost trusted identity/version: %+v", e)
			}
		}
	}
}

func TestInformerHistoryFailedWriteDoesNotLoseClusterOnRetry(t *testing.T) {
	ctx := context.Background()
	store := &resyncStore{GraphStore: memory.New()}
	sink := &resyncSink{}
	mgr := NewInformerManager(nil, store, WithClusterID("east"), WithSnapshotSink(sink))
	u := resyncObject("ConfigMap", "same", "same-uid", "1")
	store.failResource.Store(true)
	mgr.handleUpsert(ctx, configMapGVR, u, graph.EventTypeAdd)
	if len(sink.recorded()) != 0 {
		t.Fatal("failed resource write produced a history event")
	}
	store.failResource.Store(false)
	mgr.handleUpsert(ctx, configMapGVR, u, graph.EventTypeUpdate)
	got := sink.recorded()
	if len(got) != 1 || got[0].ClusterID != "east" || got[0].ResourceVersion != "1" || got[0].Data != nil {
		t.Fatal("successful retry must retain cluster/version with metadata only")
	}
}
