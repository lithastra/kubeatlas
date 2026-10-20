// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

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

type historyExtractor struct{ calls int }

func (r *historyExtractor) ExtractAll(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
	r.calls++
	return nil, errors.New("injected extraction failure")
}

func TestDynamicHistoryReceiptsReplayExtractionAndTombstones(t *testing.T) {
	ctx := context.Background()
	store := &dynamicHistoryStore{Store: memory.New(), fail: true}
	sink := &dynamicHistorySink{}
	history := discovery.NewResourceHistory(store, "east", sink)
	repair := discovery.NewMemberRepair(nil, store, "east")
	_, obj := coverageObjects()
	obj.SetUID("history-uid")
	rules := &historyExtractor{}
	d := New(nil, store, rules, nil, WithClusterID("east"), WithMemberRepair(repair), WithResourceHistory(history))
	handler := d.constraintHandler(ctx, "K8sRequiredLabels", nil)
	deliver := func(obj any, eventType graph.EventType) {
		if eventType == graph.EventTypeAdd {
			handler.OnAdd(obj, false)
		} else {
			handler.OnUpdate(obj, obj)
		}
	}
	remove := handler.OnDelete

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
	r := discovery.UnstructuredToResource(replacement, "K8sRequiredLabels")
	r.ClusterID = "east"
	if saved, err := store.GetResource(ctx, r.ID()); err != nil || saved.UID != r.UID {
		t.Fatal("stale tombstone removed new incarnation")
	}
	remove(&cache.DeletedFinalStateUnknown{Obj: replacement})
	remove(cache.DeletedFinalStateUnknown{Obj: replacement})
	if len(sink.events) != 4 || sink.events[3].EventType != graph.EventTypeDelete {
		t.Fatal("delete history missing/duplicated")
	}
	for _, e := range sink.events {
		if e.Data != nil || e.ClusterID != "east" {
			t.Fatal("dynamic history retained payload or wrong scope")
		}
	}
}
