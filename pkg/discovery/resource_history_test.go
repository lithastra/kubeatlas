// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
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
	for _, cluster := range []string{"east", ""} {
		name := cluster
		if name == "" {
			name = "standalone"
		}
		t.Run(name, func(t *testing.T) { runResourceHistoryReceipts(t, cluster) })
	}
}

func runResourceHistoryReceipts(t *testing.T, cluster string) {
	ctx := context.Background()
	store := &historyFaultStore{Store: memory.New()}
	sink := &resyncSink{}
	history := NewResourceHistory(store, cluster, sink)
	if cluster == "" {
		history = NewStandaloneResourceHistory(store, sink)
	}
	r := repairRow("history")
	r.ClusterID = cluster
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
		if e.EventType != want[i] || e.ClusterID != cluster || e.Data != nil {
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
	for _, cluster := range []string{"east", ""} {
		name := cluster
		if name == "" {
			name = "standalone"
		}
		t.Run(name, func(t *testing.T) { runResourceHistoryOverlap(t, cluster) })
	}
}

func runResourceHistoryOverlap(t *testing.T, cluster string) {
	ctx := context.Background()
	store := memory.New()
	sink := &resyncSink{}
	history := NewResourceHistory(store, cluster, sink)
	if cluster == "" {
		history = NewStandaloneResourceHistory(store, sink)
	}
	repair := NewMemberRepair(nil, store, cluster)
	r := repairRow("overlap")
	r.ClusterID = cluster
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

// Each watch preserves its own update/delete order, but independent watches may
// deliver the same update after another watch has already committed deletion.
// Current-row receipts are deliberately not a historical tombstone ledger.
func TestResourceHistoryLateWatchUpdateAfterDeleteIsAnotherObservation(t *testing.T) {
	for _, lateSources := range []int{1, 2} {
		t.Run(fmt.Sprintf("late_sources_%d", lateSources), func(t *testing.T) {
			ctx := context.Background()
			store := memory.New()
			sink := &resyncSink{}
			history := NewResourceHistory(store, "east", sink)
			repair := NewMemberRepair(nil, store, "east")
			r := repairRow("same")
			r.Kind, r.Namespace, r.GroupVersion = "K8sRequiredLabels", "", "constraints.gatekeeper.sh/v1beta1"
			upsert := func(eventType graph.EventType) {
				t.Helper()
				unlock, err := repair.LockResource(ctx, r.ID())
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
				if _, err := history.Upsert(ctx, r, eventType); err != nil {
					t.Fatal(err)
				}
			}
			remove := func() {
				t.Helper()
				unlock, err := repair.LockResource(ctx, r.ID())
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
				if deleted, err := history.Delete(ctx, r); err != nil || !deleted {
					t.Fatalf("delete: %v %v", deleted, err)
				}
			}
			// Initial deliveries deduplicate against the same still-present receipt.
			for range lateSources + 1 {
				upsert(graph.EventTypeAdd)
			}
			r.ResourceVersion = "opaque-next"
			upsert(graph.EventTypeUpdate)
			remove()
			for range lateSources {
				upsert(graph.EventTypeUpdate) // A delayed update from another watch can restore the row.
				got, err := store.GetResource(ctx, r.ID())
				if err != nil || !graph.SameResourceVersion(got, r) {
					t.Fatal("late observation was not stored", err)
				}
				remove() // That watch's later delete removes it again.
			}
			if _, err := store.GetResource(ctx, r.ID()); !errors.As(err, new(graph.ErrNotFound)) {
				t.Fatal("final delete did not converge", err)
			}
			events := sink.recorded()
			if len(events) != 3+2*lateSources {
				t.Fatalf("unexpected observation count: %+v", events)
			}
			for i, e := range events {
				want := graph.EventTypeUpdate
				if i == 0 {
					want = graph.EventTypeAdd
				} else if i%2 == 0 {
					want = graph.EventTypeDelete
				}
				if e.EventType != want || e.Data != nil || e.ClusterID != "east" {
					t.Fatalf("invalid metadata observation: %+v", e)
				}
			}
		})
	}
}

func TestStandaloneHistoryRequiresExplicitScopeAndRejectsNamedRows(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	sink := &resyncSink{}
	row := repairRow("explicit")
	row.ClusterID = ""
	if _, err := NewResourceHistory(store, "", sink).Upsert(ctx, row, graph.EventTypeAdd); err == nil {
		t.Fatal("empty member name silently became standalone")
	}
	h := NewStandaloneResourceHistory(store, sink)
	named := row
	named.ClusterID = "east"
	if err := store.UpsertResource(ctx, named); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Upsert(ctx, named, graph.EventTypeAdd); err == nil {
		t.Fatal("standalone history accepted a member row")
	}
	h.RecordDelete(named)
	if len(sink.recorded()) != 0 {
		t.Fatal("rejected scope produced an event")
	}
	if _, err := h.Upsert(ctx, row, graph.EventTypeAdd); err != nil {
		t.Fatal(err)
	}
	if removed, err := h.Delete(ctx, row); err != nil || !removed {
		t.Fatal("standalone watch deletion was not recorded", removed, err)
	}
	if got, err := store.GetResource(ctx, named.ID()); err != nil || !graph.SameResourceVersion(got, named) {
		t.Fatal("standalone deletion changed the same-UID member row", err)
	}
	events := sink.recorded()
	if len(events) != 2 || events[0].EventType != graph.EventTypeAdd || events[1].EventType != graph.EventTypeDelete {
		t.Fatal("wrong standalone receipt lifecycle", events)
	}
	for _, e := range events {
		if e.ClusterID != "" || e.Data != nil {
			t.Fatal("standalone history fabricated member provenance or retained a payload")
		}
	}
}

func TestStandaloneHistoryDoesNotFallbackToUnconditionalDelete(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	legacy := struct{ graph.GraphStore }{store}
	sink := &resyncSink{}
	h := NewStandaloneResourceHistory(legacy, sink)
	row := repairRow("no-fallback")
	row.ClusterID = ""
	if _, err := h.Upsert(ctx, row, graph.EventTypeAdd); err != nil {
		t.Fatal(err)
	}
	if deleted, err := h.Delete(ctx, row); err == nil || deleted {
		t.Fatal("missing optional delete capability was bypassed")
	}
	if _, err := store.GetResource(ctx, row.ID()); err != nil || len(sink.recorded()) != 1 {
		t.Fatal("unsupported delete removed a row or manufactured history", err)
	}
}
