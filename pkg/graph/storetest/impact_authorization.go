// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

func runImpactAuthorization(t *testing.T, factory Factory) {
	ctx := context.Background()
	for _, cluster := range []string{"", "prod", "team_%"} {
		t.Run("scope="+cluster, func(t *testing.T) {
			s := factory(t)
			var selected *graph.Graph
			for _, scope := range []string{"", "prod", "team_%", "team_other"} {
				g := storeAuthorizationFixture(scope)
				for _, resource := range g.Resources {
					resource.Raw = map[string]any{"rules": "raw-canary", "roleRef": "raw-canary"}
					if err := s.UpsertResource(ctx, resource); err != nil {
						t.Fatal(err)
					}
				}
				for _, edge := range g.Edges {
					edge.Attributes = map[string]string{"test": "attribute-canary"}
					if err := s.UpsertEdge(ctx, edge); err != nil {
						t.Fatal(err)
					}
				}
				if scope == cluster {
					selected = g
				}
			}
			// Foreign dangling endpoints and their identities must not cross the
			// snapshot boundary. SQL equality must not treat _/% as wildcards.
			for _, edge := range []graph.Edge{
				{From: "foreign:/ClusterRoleBinding/hidden", To: selected.Resources[0].ID(), Type: graph.EdgeTypeBindsRole},
				{From: selected.Resources[1].ID(), To: "foreign:app/ServiceAccount/hidden", Type: graph.EdgeTypeBindsSubject},
			} {
				if err := s.UpsertEdge(ctx, edge); err != nil {
					t.Fatal(err)
				}
			}
			limits := graph.ImpactSnapshotOptions{ClusterID: cluster, MaxResources: 4, MaxEdges: 3}
			projection, err := requireImpactReader(t, s).SnapshotImpact(ctx, limits)
			if err != nil {
				t.Fatal(err)
			}
			sortImpactProjection(projection)
			wantProjection := &graph.Graph{Resources: slices.Clone(selected.Resources), Edges: slices.Clone(selected.Edges)}
			sortImpactProjection(wantProjection)
			if !reflect.DeepEqual(projection, wantProjection) {
				t.Fatalf("system: names or cluster boundary changed: %+v", projection)
			}
			for root := range selected.Resources {
				opts := analysis.ImpactOptions{ClusterID: cluster}
				id := selected.Resources[root].ID()
				pure, err := analysis.AnalyzeImpact(ctx, selected, id, opts)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := analysis.AnalyzeStoredImpact(ctx, s, id, opts, limits)
				if err != nil || !reflect.DeepEqual(actual, pure) || actual.Authorization.Counts.Total != 3 || !actual.Authorization.ModeledTraversalComplete {
					t.Fatalf("stored association mismatch: %+v error=%v", actual, err)
				}
				if root == 0 {
					want := analysis.ImpactAuthorizationCounts{Total: 3, Direct: 1, Indirect: 2, Bindings: 1, ServiceAccounts: 1, Pods: 1}
					if actual.Authorization.Counts != want || actual.Counts.Total != 0 || actual.Authorization.Resources[2].Depth != 3 || actual.Authorization.Resources[2].Path[0].BindingScope != "cluster" {
						t.Fatalf("hand-authored role oracle mismatch: %+v", actual)
					}
				}
				body, err := json.Marshal(actual)
				if err != nil || strings.Contains(string(body), "hidden") || strings.Contains(string(body), "canary") {
					t.Fatal("excluded payload/identity escaped")
				}
				actual.Authorization.Resources[0].Path[0].From = "modified"
				again, err := analysis.AnalyzeStoredImpact(ctx, s, id, opts, limits)
				if err != nil || !reflect.DeepEqual(again, pure) {
					t.Fatal("returned authorization path aliases stored evidence")
				}
			}
			// Budget and dangling/cardinality semantics must also survive storage.
			root := selected.Resources[0].ID()
			limited, err := analysis.AnalyzeStoredImpact(ctx, s, root, analysis.ImpactOptions{ClusterID: cluster, Limit: 2}, limits)
			if err != nil || limited.Authorization.Counts.Total != 2 || !limited.Authorization.Truncated || limited.Truncated {
				t.Fatalf("facet limit affected ordinary traversal: %+v %v", limited, err)
			}
			missing := graph.Resource{ClusterID: cluster, Kind: "ClusterRole", Name: "system:missing"}.ID()
			conflict := graph.Edge{From: selected.Resources[1].ID(), To: missing, Type: graph.EdgeTypeBindsRole}
			if err := s.UpsertEdge(ctx, conflict); err != nil {
				t.Fatal(err)
			}
			limits.MaxEdges = 4
			selected.Edges = append(selected.Edges, conflict)
			opts := analysis.ImpactOptions{ClusterID: cluster}
			pure, err := analysis.AnalyzeImpact(ctx, selected, root, opts)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := analysis.AnalyzeStoredImpact(ctx, s, root, opts, limits)
			if err != nil || !reflect.DeepEqual(actual, pure) || !actual.Authorization.Counts.LowerBound || actual.Authorization.Counts.Total != 0 {
				t.Fatalf("dangling ambiguity lost: %+v %v", actual, err)
			}
		})
	}
}

func storeAuthorizationFixture(cluster string) *graph.Graph {
	r := []graph.Resource{
		{Kind: "ClusterRole", Name: "system:reader", ClusterID: cluster, GroupVersion: "rbac.authorization.k8s.io/v1"},
		{Kind: "ClusterRoleBinding", Name: "system:reader", ClusterID: cluster, GroupVersion: "rbac.authorization.k8s.io/v1"},
		{Kind: "ServiceAccount", Name: "worker", Namespace: "app", ClusterID: cluster, GroupVersion: "v1"},
		{Kind: "Pod", Name: "worker", Namespace: "app", ClusterID: cluster, GroupVersion: "v1"},
	}
	return &graph.Graph{Resources: r, Edges: []graph.Edge{
		{From: r[1].ID(), To: r[0].ID(), Type: graph.EdgeTypeBindsRole},
		{From: r[1].ID(), To: r[2].ID(), Type: graph.EdgeTypeBindsSubject},
		{From: r[3].ID(), To: r[2].ID(), Type: graph.EdgeTypeUsesServiceAccount},
	}}
}
