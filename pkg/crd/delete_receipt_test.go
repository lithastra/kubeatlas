// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/client-go/tools/cache"
)

func TestCRDDeleteReceiptParity(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/history=%t", cluster, history), func(t *testing.T) {
				ctx := context.Background()
				s := memory.New()
				sink := &dynamicHistorySink{}
				d := New(nil, s, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, s, cluster)))
				if history {
					d.resourceHistory = discovery.NewResourceHistory(s, cluster, sink)
					if cluster == "" {
						d.resourceHistory = discovery.NewStandaloneResourceHistory(s, sink)
					}
				}
				_, old := coverageObjects(t)
				old.SetUID("old-object")
				d.handleUpsert(ctx, coverageFooGVR, "Foo", old, nil)
				current := old.DeepCopy()
				current.SetUID("new-object")
				current.SetResourceVersion("new-version")
				d.handleUpsert(ctx, coverageFooGVR, "Foo", current, nil)
				r := discovery.UnstructuredToResource(current, "Foo")
				r.ClusterID = cluster
				if err := s.UpsertEdge(ctx, graph.Edge{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeOwns}); err != nil {
					t.Fatal(err)
				}
				d.handleDelete(ctx, coverageFooGVR, &cache.DeletedFinalStateUnknown{Obj: old}, "Foo", nil)
				if got, err := s.GetResource(ctx, r.ID()); err != nil || got.UID != r.UID {
					t.Fatal("late old-UID tombstone removed replacement", got, err)
				}
				if edges, err := s.ListEdges(ctx, r.ID(), graph.DirectionOutgoing); err != nil || len(edges) != 1 {
					t.Fatal("stale deletion removed replacement edges", edges, err)
				}
				deleted := current.DeepCopy()
				deleted.SetResourceVersion("older-watch-version")
				d.handleDelete(ctx, coverageFooGVR, cache.DeletedFinalStateUnknown{Obj: deleted}, "Foo", nil)
				d.handleDelete(ctx, coverageFooGVR, deleted, "Foo", nil)
				if _, err := s.GetResource(ctx, r.ID()); !errors.As(err, new(graph.ErrNotFound)) {
					t.Fatal("matching UID deletion did not converge", err)
				}
				if edges, err := s.ListEdges(ctx, r.ID(), graph.DirectionOutgoing); err != nil || len(edges) != 0 {
					t.Fatal("committed deletion did not cascade", edges, err)
				}
				want := 0
				if history {
					want = 3
				}
				if len(sink.events) != want || (history && (sink.events[2].EventType != graph.EventTypeDelete || sink.events[2].ResourceVersion != "new-version")) {
					t.Fatal("delete history lost committed receipt or was duplicated", sink.events)
				}
			})
		}
	}
}
