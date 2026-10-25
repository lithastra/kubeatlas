// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/client-go/tools/cache"
)

func TestCoreDeleteReceiptParity(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/history=%t", cluster, history), func(t *testing.T) {
				ctx := context.Background()
				s := memory.New()
				sink := &resyncSink{}
				deletes := 0
				m := NewInformerManager(nil, s, WithClusterID(cluster), WithMemberRepair(NewMemberRepair(nil, s, cluster)), WithSnapshotSink(sink), WithBroadcaster(func(event, _, _, _ string) {
					if event == "delete" {
						deletes++
					}
				}))
				if history {
					m.resourceHistory = NewResourceHistory(s, cluster, sink)
					if cluster == "" {
						m.resourceHistory = NewStandaloneResourceHistory(s, sink)
					}
				}
				old := resyncObject("ConfigMap", "same", "old-object", "first-version")
				m.handleUpsert(ctx, configMapGVR, old, graph.EventTypeAdd)
				current := old.DeepCopy()
				current.SetUID("new-object")
				current.SetResourceVersion("new-version")
				m.handleUpsert(ctx, configMapGVR, current, graph.EventTypeAdd)
				r := UnstructuredToResource(current, "ConfigMap")
				r.ClusterID = cluster
				if err := s.UpsertEdge(ctx, graph.Edge{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeOwns}); err != nil {
					t.Fatal(err)
				}
				old.SetKind("") // Resolve the cached kind even for late tombstones.
				m.handleDelete(ctx, configMapGVR, &cache.DeletedFinalStateUnknown{Obj: old})
				if got, err := s.GetResource(ctx, r.ID()); err != nil || got.UID != r.UID || deletes != 0 || len(sink.recorded()) != 2 {
					t.Fatal("late tombstone removed/notified replacement", got, deletes, sink.recorded(), err)
				}
				if !history && m.persisted[resourceWriteKey{gvr: configMapGVR, namespace: r.Namespace, name: r.Name}].uid != "new-object" {
					t.Fatal("stale deletion invalidated the replacement's successful-write marker")
				}
				if edges, err := s.ListEdges(ctx, r.ID(), graph.DirectionOutgoing); err != nil || len(edges) != 1 {
					t.Fatal("stale deletion removed replacement edges", edges, err)
				}
				deleted := current.DeepCopy()
				deleted.SetResourceVersion("older-watch-version")
				m.handleDelete(ctx, configMapGVR, cache.DeletedFinalStateUnknown{Obj: deleted})
				m.handleDelete(ctx, configMapGVR, deleted)
				if _, err := s.GetResource(ctx, r.ID()); !errors.As(err, new(graph.ErrNotFound)) {
					t.Fatal("matching UID deletion did not converge", err)
				}
				events := sink.recorded()
				if len(events) != 3 || deletes != 1 || events[2].ResourceVersion != "new-version" || events[2].EventType != graph.EventTypeDelete {
					t.Fatal("delete receipt/event/notification was duplicated or incorrect", events, deletes)
				}
			})
		}
	}
}

type observedDeleteStore struct {
	*memory.Store
	readFail, atomicFail  bool
	onRead, beforeDelete  func()
	afterDelete           func()
	conditional, fallback int
}

func (s *observedDeleteStore) GetResource(ctx context.Context, id string) (graph.Resource, error) {
	if s.readFail {
		return graph.Resource{}, errors.New("injected receipt read failure")
	}
	if s.onRead != nil {
		s.onRead()
		ctx = context.Background()
	}
	return s.Store.GetResource(ctx, id)
}

func (s *observedDeleteStore) DeleteResource(context.Context, string) error {
	s.fallback++
	return errors.New("unconditional delete must not be used")
}

func (s *observedDeleteStore) DeleteResourceVersion(ctx context.Context, r graph.Resource) (bool, error) {
	s.conditional++
	if s.atomicFail {
		return false, errors.New("injected conditional delete failure")
	}
	if s.beforeDelete != nil {
		s.beforeDelete()
	}
	deleted, err := s.Store.DeleteResourceVersion(ctx, r)
	if s.afterDelete != nil {
		s.afterDelete()
	}
	return deleted, err
}

func TestObservedDeletionRetainsUnprovenAndChangedReceipts(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, scenario := range []string{"matching", "old-api-version", "old-uid", "no-uid", "no-version", "no-api-version", "wrong-api-group", "invalid-api-version", "empty-api-version", "unversioned-row", "missing-row", "other-member", "read-failure", "atomic-failure", "no-capability", "canceled-read", "canceled-delete", "canceled-after-commit", "racing-uid", "racing-version", "racing-origin"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, scenario), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					s := &observedDeleteStore{Store: memory.New()}
					previous := storetest.ResourceOriginFixture(cluster)
					tombstone := previous
					tombstone.ResourceVersion = "older-watch-version"
					tombstone.Origin = graph.ResourceOrigin{}
					wantDeleted, wantError := false, false
					switch scenario {
					case "matching":
						wantDeleted = true
					case "old-api-version":
						tombstone.GroupVersion = "example.com/v0"
						wantDeleted = true
					case "old-uid":
						tombstone.UID = "old-incarnation"
					case "no-uid":
						tombstone.UID = ""
					case "no-version":
						tombstone.ResourceVersion = ""
					case "no-api-version":
						tombstone.GroupVersion = ""
					case "wrong-api-group":
						tombstone.GroupVersion = "other.example.com/v1"
					case "invalid-api-version":
						tombstone.GroupVersion = "invalid/group/v1"
					case "empty-api-version":
						tombstone.GroupVersion = "example.com/"
					case "unversioned-row":
						previous.ResourceVersion = ""
					case "other-member":
						tombstone.ClusterID = "west"
					case "read-failure":
						s.readFail, wantError = true, true
					case "atomic-failure":
						s.atomicFail, wantError = true, true
					case "no-capability":
						wantError = true
					case "canceled-read":
						s.onRead, wantError = cancel, true
					case "canceled-delete":
						s.beforeDelete, wantError = cancel, true
					case "canceled-after-commit":
						s.afterDelete, wantDeleted = cancel, true
					case "racing-uid", "racing-version", "racing-origin":
						s.beforeDelete = func() {
							fresh := previous
							switch scenario {
							case "racing-uid":
								fresh.UID, fresh.Origin.ObjectUID = "new-incarnation", "new-incarnation"
							case "racing-version":
								fresh.ResourceVersion, fresh.Origin.ObjectVersion = "new-version", "new-version"
							case "racing-origin":
								fresh.Origin.Registration = strings.Repeat("b", 32)
							}
							if err := s.UpsertResource(context.Background(), fresh); err != nil {
								t.Fatal(err)
							}
						}
					}
					if scenario != "missing-row" {
						if err := s.UpsertResource(ctx, previous); err != nil {
							t.Fatal(err)
						}
					}
					foreign := previous
					foreign.ClusterID = "foreign"
					if err := s.UpsertResource(ctx, foreign); err != nil {
						t.Fatal(err)
					}
					edge := graph.Edge{From: previous.ID(), To: foreign.ID(), Type: graph.EdgeTypeOwns}
					if scenario != "missing-row" {
						if err := s.UpsertEdge(ctx, edge); err != nil {
							t.Fatal(err)
						}
					}
					var store graph.GraphStore = s
					if scenario == "no-capability" {
						store = struct{ graph.GraphStore }{s}
					}
					sink := &resyncSink{}
					remove := func() (bool, error) { return DeleteObservedResource(ctx, store, tombstone) }
					if history {
						h := NewResourceHistory(store, tombstone.ClusterID, sink)
						if tombstone.ClusterID == "" {
							h = NewStandaloneResourceHistory(store, sink)
						}
						remove = func() (bool, error) { return h.Delete(ctx, tombstone) }
					}
					deleted, err := remove()
					if deleted != wantDeleted || (err != nil) != wantError || s.fallback != 0 || s.conditional > 1 {
						t.Fatalf("unsafe/incorrect deletion: %t want %t error=%v fallback=%d", deleted, wantDeleted, err, s.fallback)
					}
					_, readErr := s.Store.GetResource(context.Background(), previous.ID())
					if wantDeleted || scenario == "missing-row" {
						if !errors.As(readErr, new(graph.ErrNotFound)) {
							t.Fatal("committed deletion did not remove row", readErr)
						}
					} else if readErr != nil {
						t.Fatal("unproven deletion lost retained data", readErr)
					}
					if _, err := s.Store.GetResource(context.Background(), foreign.ID()); err != nil {
						t.Fatal("deletion crossed member scope", err)
					}
					edges, edgeErr := s.ListEdges(context.Background(), previous.ID(), graph.DirectionOutgoing)
					wantEdges := 1
					if wantDeleted || scenario == "missing-row" {
						wantEdges = 0
					}
					if edgeErr != nil || len(edges) != wantEdges {
						t.Fatal("uncommitted deletion changed incident edges", edges, edgeErr)
					}
					wantEvents := 0
					if history && wantDeleted {
						wantEvents = 1
					}
					if len(sink.recorded()) != wantEvents {
						t.Fatal("uncommitted deletion fabricated history", sink.recorded())
					}
					for _, event := range sink.recorded() {
						if event.ClusterID != cluster || event.UID != string(previous.UID) || event.ResourceVersion != previous.ResourceVersion || event.Data != nil || event.EventType != graph.EventTypeDelete {
							t.Fatal("delete event did not use committed metadata-only receipt", event)
						}
					}
				})
			}
		}
	}
}

func TestCoreCommittedDeletionSurvivesLateCancellation(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/history=%t", cluster, history), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := &observedDeleteStore{Store: memory.New(), afterDelete: cancel}
				sink := &resyncSink{}
				deletes := 0
				m := NewInformerManager(nil, s, WithClusterID(cluster), WithSnapshotSink(sink), WithBroadcaster(func(event, _, _, _ string) {
					if event == "delete" {
						deletes++
					}
				}))
				if history {
					m.resourceHistory = NewResourceHistory(s, cluster, sink)
					if cluster == "" {
						m.resourceHistory = NewStandaloneResourceHistory(s, sink)
					}
				}
				object := resyncObject("ConfigMap", "committed", "object-uid", "stored-version")
				m.handleUpsert(ctx, configMapGVR, object, graph.EventTypeAdd)
				m.handleDelete(ctx, configMapGVR, object)
				if ctx.Err() == nil || deletes != 1 || len(sink.recorded()) != 2 || len(m.persisted) != 0 {
					t.Fatal("late cancellation hid committed deletion", deletes, sink.recorded())
				}
			})
		}
	}
}

func TestCoreDuplicateDeletionForgetsOnlyMatchingWriteMarker(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	sink := &resyncSink{}
	deletes := 0
	m := NewInformerManager(nil, s, WithSnapshotSink(sink), WithBroadcaster(func(event, _, _, _ string) {
		if event == "delete" {
			deletes++
		}
	}))
	object := resyncObject("ConfigMap", "overlap", "same-uid", "same-version")
	m.handleUpsert(ctx, configMapGVR, object, graph.EventTypeAdd)
	r := UnstructuredToResource(object, "ConfigMap")
	// Model another collector committing the deletion first.
	if deleted, err := s.DeleteResourceVersion(ctx, r); !deleted || err != nil {
		t.Fatal("other collector did not remove fixture", deleted, err)
	}
	m.handleDelete(ctx, configMapGVR, object)
	if deletes != 0 || len(sink.recorded()) != 1 || len(m.persisted) != 0 {
		t.Fatal("duplicate delete notified clients or retained a stale write marker", deletes, sink.recorded())
	}
	m.handleUpsert(ctx, configMapGVR, object, graph.EventTypeUpdate)
	if _, err := s.GetResource(ctx, r.ID()); err != nil || len(sink.recorded()) != 2 {
		t.Fatal("later same-version observation was suppressed by deleted marker", err)
	}
}
