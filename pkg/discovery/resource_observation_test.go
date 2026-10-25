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
)

func TestDynamicResourceReceiptParity(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, enabled := range []bool{false, true} {
			for _, scenario := range []string{"exact", "overlap", "missing-origin", "invalid-origin", "fresh-registration", "fresh-owner", "legacy", "invalid-stored-origin", "resource-version", "object-uid", "api-version", "missing-row", "unversioned", "other-member"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, enabled, scenario), func(t *testing.T) {
					ctx := context.Background()
					store := memory.New()
					previous := storetest.ResourceOriginFixture(cluster)
					next, wantWritten, wantEvents := previous, false, 0
					switch scenario {
					case "overlap":
						next.Origin.Source = "gatekeeper"
					case "missing-origin":
						next.Origin = graph.ResourceOrigin{}
					case "invalid-origin":
						next.Origin.ObjectUID = "another-object"
					case "fresh-registration":
						next.Origin.Registration = strings.Repeat("b", 32)
						wantWritten = true
					case "fresh-owner":
						next.Origin.OwnerUID = "replacement-owner"
						wantWritten = true
					case "legacy":
						previous.Origin = graph.ResourceOrigin{}
						wantWritten = true
					case "invalid-stored-origin":
						previous.Origin.ObjectVersion = "another-version"
						wantWritten = true
					case "resource-version":
						next.ResourceVersion, next.Origin.ObjectVersion = "opaque-next", "opaque-next"
						next.Origin.Source = "gatekeeper"
						wantWritten, wantEvents = true, 1
					case "object-uid":
						next.UID, next.Origin.ObjectUID = "replacement-object", "replacement-object"
						next.Origin.Source = "gatekeeper"
						wantWritten, wantEvents = true, 1
					case "api-version":
						next.GroupVersion, next.Origin.Version = "example.com/v2", "v2"
						wantWritten = true // Representation changes do not invent object history.
					case "missing-row":
						wantWritten, wantEvents = true, 1
					case "unversioned":
						next.ResourceVersion = ""
						wantWritten = true
					case "other-member":
						next.ClusterID, next.Origin.ClusterID = "west", "west"
						wantWritten, wantEvents = true, 1
					}
					if scenario != "missing-row" {
						if err := store.UpsertResource(ctx, previous); err != nil {
							t.Fatal(err)
						}
					}
					sink := &resyncSink{}
					upsert := func(r graph.Resource) (bool, error) { return UpsertObservedResource(ctx, store, r) }
					if enabled {
						h := NewResourceHistory(store, next.ClusterID, sink)
						if next.ClusterID == "" {
							h = NewStandaloneResourceHistory(store, sink)
						}
						upsert = func(r graph.Resource) (bool, error) { return h.Upsert(ctx, r, graph.EventTypeAdd) }
					} else {
						wantEvents = 0
					}
					written, err := upsert(next)
					if err != nil || written != wantWritten {
						t.Fatalf("receipt commit: %t want %t: %v", written, wantWritten, err)
					}
					want := previous
					if wantWritten {
						want = next
					}
					want = graph.SanitizeResource(want)
					got, err := store.GetResource(ctx, next.ID())
					if err != nil || !graph.SameResourceVersion(got, want) {
						t.Fatalf("wrong stored receipt: %+v want %+v: %v", got, want, err)
					}
					if scenario == "other-member" {
						got, err := store.GetResource(ctx, previous.ID())
						if err != nil || !graph.SameResourceVersion(got, previous) {
							t.Fatal("source delivery changed another member", err)
						}
					}
					events := sink.recorded()
					if len(events) != wantEvents {
						t.Fatalf("receipt-only refresh fabricated history: %+v", events)
					}
					for _, event := range events {
						wantType := graph.EventTypeAdd
						if scenario == "resource-version" {
							wantType = graph.EventTypeUpdate
						}
						if event.EventType != wantType || event.ClusterID != next.ClusterID || event.Data != nil {
							t.Fatal("wrong committed metadata event", event)
						}
					}
				})
			}
		}
	}
}

type observationReadStore struct {
	*historyFaultStore
	onRead func()
	writes int
}

func (s *observationReadStore) GetResource(ctx context.Context, id string) (graph.Resource, error) {
	if s.onRead != nil {
		s.onRead()
		ctx = context.Background() // Exercise a dependency ignoring cancellation.
	}
	return s.historyFaultStore.GetResource(ctx, id)
}

func (s *observationReadStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	s.writes++
	return s.historyFaultStore.UpsertResource(ctx, r)
}

func TestDynamicResourceReceiptFailuresDoNotFallback(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, failure := range []string{"read", "write", "canceled-read"} {
			t.Run(fmt.Sprintf("history=%t/%s", enabled, failure), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &observationReadStore{historyFaultStore: &historyFaultStore{Store: memory.New()}}
				previous := storetest.ResourceOriginFixture("east")
				if err := store.Store.UpsertResource(ctx, previous); err != nil {
					t.Fatal(err)
				}
				next := previous
				next.ResourceVersion, next.Origin.ObjectVersion = "next", "next"
				store.readFail, store.writeFail = failure == "read", failure == "write"
				if failure == "canceled-read" {
					store.onRead = cancel
				}
				sink := &resyncSink{}
				upsert := func() (bool, error) { return UpsertObservedResource(ctx, store, next) }
				if enabled {
					h := NewResourceHistory(store, "east", sink)
					upsert = func() (bool, error) { return h.Upsert(ctx, next, graph.EventTypeUpdate) }
				}
				if written, err := upsert(); err == nil || written || (failure == "canceled-read" && !errors.Is(err, context.Canceled)) {
					t.Fatalf("failed receipt was accepted: %t %v", written, err)
				}
				if failure != "write" && store.writes != 0 {
					t.Fatal("receipt lookup failure fell back to unconditional write")
				}
				got, err := store.Store.GetResource(context.Background(), previous.ID())
				if err != nil || !graph.SameResourceVersion(got, previous) || len(sink.recorded()) != 0 {
					t.Fatal("failed delivery changed data/history", got, err)
				}
				if failure != "canceled-read" {
					store.readFail, store.writeFail = false, false
					if written, err := upsert(); err != nil || !written {
						t.Fatal("failed receipt suppressed later successful observation", written, err)
					}
				}
			})
		}
	}
}
