// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"errors"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/client-go/tools/cache"
)

type dynamicHistorySink struct{ events []graph.ResourceEvent }

func (s *dynamicHistorySink) Enqueue(e graph.ResourceEvent) { s.events = append(s.events, e) }

type dynamicHistoryStore struct {
	*memory.Store
	fail bool
}

func (s *dynamicHistoryStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	if s.fail {
		return errors.New("injected dynamic write failure")
	}
	return s.Store.UpsertResource(ctx, r)
}

type historyRules struct{ calls int }

func (r *historyRules) EvaluateForResource(context.Context, graph.Resource) ([]graph.Edge, error) {
	r.calls++
	return nil, errors.New("injected extraction failure")
}

func TestDynamicHistoryReceiptsReplayExtractionAndTombstones(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		name := cluster
		if name == "" {
			name = "standalone"
		}
		t.Run(name, func(t *testing.T) { runDynamicHistoryReceipts(t, cluster) })
	}
}

func runDynamicHistoryReceipts(t *testing.T, cluster string) {
	ctx := context.Background()
	store := &dynamicHistoryStore{Store: memory.New(), fail: true}
	sink := &dynamicHistorySink{}
	history := discovery.NewResourceHistory(store, cluster, sink)
	if cluster == "" {
		history = discovery.NewStandaloneResourceHistory(store, sink)
	}
	repair := discovery.NewMemberRepair(nil, store, cluster)
	_, obj := coverageObjects(t)
	obj.SetUID("history-uid")
	rules := &historyRules{}
	d := New(nil, store, WithClusterID(cluster), WithMemberRepair(repair), WithResourceHistory(history), WithRegoEvaluator(rules))
	deliver := func(obj any, eventType graph.EventType) {
		d.handleUpsertEvent(ctx, coverageFooGVR, "Foo", obj, nil, eventType)
	}
	remove := func(obj any) { d.handleDelete(ctx, coverageFooGVR, obj, "Foo", nil) }

	deliver(obj, graph.EventTypeAdd)
	if len(sink.events) != 0 || rules.calls != 0 {
		t.Fatal("failed resource write produced history/extraction")
	}
	store.fail = false
	deliver(obj, graph.EventTypeAdd)
	deliver(obj.DeepCopy(), graph.EventTypeUpdate)
	if len(sink.events) != 1 || rules.calls != 2 {
		t.Fatal("replay duplicated history or skipped extraction")
	}
	changed := obj.DeepCopy()
	changed.SetResourceVersion("2")
	deliver(changed, graph.EventTypeUpdate)
	if len(sink.events) != 2 || sink.events[1].EventType != graph.EventTypeUpdate {
		t.Fatal("actual update missing")
	}
	replacement := changed.DeepCopy()
	replacement.SetUID("replacement")
	replacement.SetResourceVersion("3")
	deliver(replacement, graph.EventTypeAdd)
	remove(&cache.DeletedFinalStateUnknown{Obj: obj})
	if len(sink.events) != 3 {
		t.Fatal("stale tombstone recorded a delete")
	}
	r := discovery.UnstructuredToResource(replacement, "Foo")
	r.ClusterID = cluster
	if saved, err := store.GetResource(ctx, r.ID()); err != nil || saved.UID != r.UID {
		t.Fatal("stale tombstone removed new incarnation")
	}
	remove(&cache.DeletedFinalStateUnknown{Obj: replacement})
	remove(cache.DeletedFinalStateUnknown{Obj: replacement})
	if len(sink.events) != 4 || sink.events[3].EventType != graph.EventTypeDelete {
		t.Fatal("delete history missing/duplicated")
	}
	for _, e := range sink.events {
		if e.Data != nil || e.ClusterID != cluster {
			t.Fatal("dynamic history retained payload or wrong scope")
		}
	}
}
