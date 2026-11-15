// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func runEmptyLabelFilters(t *testing.T, factory Factory) {
	t.Helper()
	for _, cluster := range []string{"", "east"} {
		t.Run("cluster="+cluster, func(t *testing.T) {
			ctx, s := context.Background(), factory(t)
			rows := []graph.Resource{
				{ClusterID: cluster, Namespace: "demo", Kind: "Pod", Name: "a", Labels: map[string]string{"flag": "", "app": "selected"}},
				{ClusterID: cluster, Namespace: "demo", Kind: "Pod", Name: "b", Labels: map[string]string{"app": "selected"}},
				{ClusterID: cluster, Namespace: "other", Kind: "Deployment", Name: "c", Labels: map[string]string{"flag": ""}},
				{ClusterID: cluster, Namespace: "other", Kind: "Deployment", Name: "d", Labels: map[string]string{"other": ""}},
				{ClusterID: cluster, Namespace: "demo", Kind: "Pod", Name: "e", Labels: map[string]string{"flag": "value"}},
				{ClusterID: cluster, Namespace: "demo", Kind: "Pod", Name: "f"},
				{ClusterID: "west", Namespace: "hidden", Kind: "Pod", Name: "foreign", Labels: map[string]string{"flag": ""}},
			}
			for _, row := range rows {
				if err := s.UpsertResource(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			for _, pair := range [][2]int{{0, 2}, {1, 2}, {0, 3}, {0, 1}, {0, 0}} {
				if err := s.UpsertEdge(ctx, graph.Edge{From: rows[pair[0]].ID(), To: rows[pair[1]].ID(), Type: graph.EdgeTypeSelects}); err != nil {
					t.Fatal(err)
				}
			}
			filter := map[string]string{"flag": ""}
			assertIDs := func(t *testing.T, got []graph.Resource, err error, want ...string) {
				t.Helper()
				ids := make([]string, len(got))
				for i, r := range got {
					ids[i] = r.ID()
				}
				slices.Sort(ids)
				slices.Sort(want)
				if err != nil || !slices.Equal(ids, want) {
					t.Fatal("label filtering confused missing keys with empty values", ids, want, err)
				}
			}
			t.Run("global", func(t *testing.T) {
				got, err := s.ListResources(ctx, graph.Filter{Labels: filter})
				assertIDs(t, got, err, rows[0].ID(), rows[2].ID(), rows[6].ID())
			})
			t.Run("scoped", func(t *testing.T) {
				got, err := s.ListResourcesInCluster(ctx, cluster, graph.Filter{Labels: filter})
				assertIDs(t, got, err, rows[0].ID(), rows[2].ID())
			})
			t.Run("namespace", func(t *testing.T) {
				got, err := s.ListResources(ctx, graph.Filter{Namespace: "demo", Labels: filter})
				assertIDs(t, got, err, rows[0].ID())
			})
			t.Run("kind", func(t *testing.T) {
				got, err := s.ListResources(ctx, graph.Filter{Kind: "Deployment", Labels: filter})
				assertIDs(t, got, err, rows[2].ID())
			})
			t.Run("conjunction", func(t *testing.T) {
				got, err := s.ListResourcesInCluster(ctx, cluster, graph.Filter{Labels: map[string]string{"flag": "", "app": "selected"}})
				assertIDs(t, got, err, rows[0].ID())
			})
			t.Run("empty-filter", func(t *testing.T) {
				got, err := s.ListResourcesInCluster(ctx, cluster, graph.Filter{Labels: map[string]string{}})
				assertIDs(t, got, err, rows[0].ID(), rows[1].ID(), rows[2].ID(), rows[3].ID(), rows[4].ID(), rows[5].ID())
			})
			t.Run("kind-counts", func(t *testing.T) {
				got, err := s.CountKindsByNamespace(ctx, filter)
				want := map[string]map[string]int{"demo": {"Pod": 1}, "other": {"Deployment": 1}, "hidden": {"Pod": 1}}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("kind counts included missing label keys", got, want, err)
				}
			})
			t.Run("edge-counts", func(t *testing.T) {
				got, err := s.CountCrossNamespaceEdges(ctx, filter)
				want := map[graph.NamespacePair]int{{From: "demo", To: "other"}: 1, {From: "demo", To: "demo"}: 1}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("edge counts must require both endpoint keys", got, want, err)
				}
			})
			t.Run("subgraph", func(t *testing.T) {
				got, err := s.GetNamespaceSubgraph(ctx, "demo", filter)
				if err != nil || got == nil {
					t.Fatal("namespace subgraph failed", err)
				}
				assertIDs(t, got.Resources, nil, rows[0].ID())
				if len(got.Edges) != 1 || got.Edges[0].From != rows[0].ID() || got.Edges[0].To != rows[0].ID() || got.Edges[0].Type != graph.EdgeTypeSelects {
					t.Fatal("subgraph included endpoints without the required key", got.Edges)
				}
			})
		})
	}
}
