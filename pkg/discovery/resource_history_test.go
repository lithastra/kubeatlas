// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/client-go/tools/cache"
)

type historyFaultStore struct {
	*memory.Store
	readFail, writeFail, deleteFail bool
}

func (s *historyFaultStore) GetResource(ctx context.Context, id string) (graph.Resource, error) {
	if s.readFail {
		return graph.Resource{}, errors.New("injected history receipt read failure")
	}
	return s.Store.GetResource(ctx, id)
}
func (s *historyFaultStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	if s.writeFail {
		return errors.New("injected history resource failure")
	}
	return s.Store.UpsertResource(ctx, r)
}
func (s *historyFaultStore) DeleteResourceVersion(ctx context.Context, r graph.Resource) (bool, error) {
	if s.deleteFail {
		return false, errors.New("injected history delete failure")
	}
	return s.Store.DeleteResourceVersion(ctx, r)
}

func TestResourceHistorySuccessfulReceiptsFailuresAndIncarnations(t *testing.T) {
	ctx := context.Background()
	store := &historyFaultStore{Store: memory.New()}
	sink := &resyncSink{}
	history := NewResourceHistory(store, "east", sink)
	r := repairRow("history")
	r.Raw = map[string]any{"spec": map[string]any{"private": "payload-canary"}}
	for _, failure := range []string{"read", "write"} {
		store.readFail, store.writeFail = failure == "read", failure == "write"
		if _, err := history.Upsert(ctx, r, graph.EventTypeAdd); err == nil {
			t.Fatal("failed receipt accepted")
		}
		if len(sink.recorded()) != 0 {
			t.Fatal("failed write produced history")
		}
	}
	store.readFail, store.writeFail = false, false
	if changed, err := history.Upsert(ctx, r, graph.EventTypeAdd); err != nil || !changed {
		t.Fatalf("recovery: %v %v", changed, err)
	}
	if changed, err := history.Upsert(ctx, r, graph.EventTypeUpdate); err != nil || changed {
		t.Fatal("replay changed resource")
	}
	// Endpoint conversion updates the graph representation, not object history.
	converted := r
	converted.GroupVersion = "example.com/v2"
	if changed, err := history.Upsert(ctx, converted, graph.EventTypeAdd); err != nil || !changed {
		t.Fatal("served version not persisted")
	}
	if len(sink.recorded()) != 1 {
		t.Fatal("replay/served-version switch produced false history")
	}
	converted.ResourceVersion = "opaque-next"
	if _, err := history.Upsert(ctx, converted, graph.EventTypeAdd); err != nil {
		t.Fatal(err)
	}
	replaced := converted
	replaced.UID = "new-incarnation"
	replaced.ResourceVersion = "new-opaque"
	if _, err := history.Upsert(ctx, replaced, graph.EventTypeUpdate); err != nil {
		t.Fatal(err)
	}
	if deleted, err := history.Delete(ctx, r); err != nil || deleted {
		t.Fatal("old UID deleted replacement")
	}
	if stored, err := store.GetResource(ctx, r.ID()); err != nil || stored.UID != replaced.UID {
		t.Fatal("replacement not preserved")
	}
	store.deleteFail = true
	if _, err := history.Delete(ctx, replaced); err == nil {
		t.Fatal("delete failure not propagated")
	}
	if len(sink.recorded()) != 3 {
		t.Fatal("failed delete recorded")
	}
	store.deleteFail = false
	// The tombstone can carry an older RV for the same incarnation.
	tombstone := replaced
	tombstone.ResourceVersion = "old-watch-rv"
	if deleted, err := history.Delete(ctx, tombstone); err != nil || !deleted {
		t.Fatalf("delete recovery: %v %v", deleted, err)
	}
	if deleted, err := history.Delete(ctx, tombstone); err != nil || deleted {
		t.Fatal("duplicate delete committed")
	}
	events := sink.recorded()
	want := []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate, graph.EventTypeAdd, graph.EventTypeDelete}
	if len(events) != len(want) {
		t.Fatalf("history count: %d", len(events))
	}
	for i, e := range events {
		if e.EventType != want[i] || e.ClusterID != "east" || e.Data != nil {
			t.Fatalf("wrong metadata event: %+v", e)
		}
	}
	if events[3].ResourceVersion != replaced.ResourceVersion {
		t.Fatal("delete lost stored receipt")
	}
	foreign := r
	foreign.ClusterID = "west"
	if _, err := history.Upsert(ctx, foreign, graph.EventTypeAdd); err == nil {
		t.Fatal("foreign scope accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := history.Upsert(cancelled, r, graph.EventTypeAdd); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled upsert accepted")
	}
	unversioned := r
	unversioned.UID = ""
	if _, err := history.Upsert(ctx, unversioned, graph.EventTypeAdd); err != nil {
		t.Fatal(err)
	}
	if len(sink.recorded()) != 4 {
		t.Fatal("unverifiable identity generated history")
	}
}

func TestResourceHistoryOverlappingCollectorsUseSharedMemberLock(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	sink := &resyncSink{}
	history := NewResourceHistory(store, "east", sink)
	repair := NewMemberRepair(nil, store, "east")
	r := repairRow("overlap")
	run := func(remove bool) {
		var wg sync.WaitGroup
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				unlock, err := repair.LockResource(ctx, r.ID())
				if err != nil {
					t.Error(err)
					return
				}
				defer unlock()
				if remove {
					_, err = history.Delete(ctx, r)
				} else {
					_, err = history.Upsert(ctx, r, graph.EventTypeAdd)
				}
				if err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	run(false)
	run(true)
	events := sink.recorded()
	if len(events) != 2 || events[0].EventType != graph.EventTypeAdd || events[1].EventType != graph.EventTypeDelete {
		t.Fatalf("overlapping observations recorded %d changes", len(events))
	}
}

func TestMemberCoreHistoryUsesSharedReceiptAndReplaysExtraction(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	sink := &resyncSink{}
	history := NewResourceHistory(store, "east", sink)
	repair := NewMemberRepair(nil, store, "east")
	calls := 0
	mgr := NewInformerManager(nil, store, WithClusterID("east"), WithMemberRepair(repair), WithResourceHistory(history), WithSnapshotSink(sink), WithExtractor(resyncExtractorFunc(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
		calls++
		return nil, nil
	})))
	obj := resyncObject("ConfigMap", "shared", "uid", "1")
	r := UnstructuredToResource(obj, "ConfigMap")
	r.ClusterID = "east"
	unlock, _ := repair.LockResource(ctx, r.ID())
	_, err := history.Upsert(ctx, r, graph.EventTypeAdd)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeAdd)
	mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeUpdate)
	if calls != 2 || len(sink.recorded()) != 1 {
		t.Fatal("core replay duplicated history or skipped extraction")
	}
	mgr.handleDelete(ctx, configMapGVR, cache.DeletedFinalStateUnknown{Obj: obj})
	mgr.handleDelete(ctx, configMapGVR, obj)
	if len(sink.recorded()) != 2 {
		t.Fatal("core delete was not deduplicated")
	}
}
