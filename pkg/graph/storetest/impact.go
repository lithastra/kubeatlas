// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// RunImpactSnapshot is the same projection and analysis oracle for both tiers.
// Each subtest gets an empty store. All resource contents are synthetic.
func RunImpactSnapshot(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("reference fields", func(t *testing.T) { runImpactReferenceProjection(t, factory) })
	t.Run("authorization associations", func(t *testing.T) { runImpactAuthorization(t, factory) })
	t.Run("empty and cancelled", func(t *testing.T) {
		s := factory(t)
		reader := requireImpactReader(t, s)
		got, err := reader.SnapshotImpact(context.Background(), graph.ImpactSnapshotOptions{})
		if err != nil || got == nil || got.Resources == nil || got.Edges == nil || len(got.Resources)+len(got.Edges) != 0 {
			t.Fatalf("empty projection=%+v error=%v", got, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err = reader.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{})
		if got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled projection=%+v error=%v", got, err)
		}
	})
	t.Run("scope allowlist dangling references and paths", func(t *testing.T) {
		s := factory(t)
		want := seedImpactProjection(t, s)
		reader := requireImpactReader(t, s)
		opts := graph.ImpactSnapshotOptions{ClusterID: "prod"}
		got, err := reader.SnapshotImpact(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		sortImpactProjection(want)
		sortImpactProjection(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("projection changed allowlist/scope: got=%+v want=%+v", got, want)
		}
		body, err := json.Marshal(got)
		if err != nil || strings.Contains(string(body), "canary") || strings.Contains(string(body), "hidden") {
			t.Fatalf("excluded fields escaped projection; marshal error: %v", err)
		}
		rootID := "prod:config/ConfigMap/settings"
		result, err := analysis.AnalyzeStoredImpact(context.Background(), s, rootID, analysis.ImpactOptions{ClusterID: "prod"}, opts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Counts != (analysis.ImpactCounts{Total: 2, Direct: 1, Indirect: 1, Workloads: 1, Pods: 1, LowerBound: true}) || result.ModeledTraversalComplete {
			t.Fatalf("counts or unresolved evidence changed: %+v", result)
		}
		wantPath := []analysis.ImpactStep{
			{From: "prod:app/Deployment/api", To: rootID, Type: graph.EdgeTypeUsesConfigMap, TraversalFrom: rootID, TraversalTo: "prod:app/Deployment/api"},
			{From: "prod:app/Pod/api-pod", To: "prod:app/Deployment/api", Type: graph.EdgeTypeOwns, TraversalFrom: "prod:app/Deployment/api", TraversalTo: "prod:app/Pod/api-pod", OwnerUIDStatus: "matched"},
		}
		if !reflect.DeepEqual(result.Resources[1].Path, wantPath) || !reflect.DeepEqual(result.Notices, []analysis.ImpactNotice{{Reason: analysis.ImpactMissingResource, Count: 1}}) {
			t.Fatalf("store-backed representative path/notices changed: %+v", result)
		}
		if len(result.ReferenceEvidence) != 1 || result.ReferenceEvidence[0].Status != "unknown" || result.ReferenceEvidence[0].Reason != "source_fields_unavailable" || len(result.ReferenceEvidence[0].Uses) != 0 {
			t.Fatalf("identity-only projection invented source field evidence: %+v", result.ReferenceEvidence)
		}
		// Mutating the projection must not mutate stored owner slices/markers.
		for i := range got.Resources {
			if len(got.Resources[i].OwnerReferences) != 0 {
				got.Resources[i].OwnerReferences[0].UID = "modified"
			}
			if got.Resources[i].Annotations != nil {
				got.Resources[i].Annotations[graph.ReferenceOnlyAnnotation] = "modified"
			}
		}
		got.Edges[0].From = "modified"
		again, err := reader.SnapshotImpact(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		sortImpactProjection(again)
		if !reflect.DeepEqual(again, want) {
			t.Fatal("returned projection aliases stored evidence")
		}
		stored, err := s.GetResource(context.Background(), "prod:app/Deployment/api")
		if err != nil || stored.Raw["synthetic"] != "raw-canary" || stored.Labels["test"] != "label-canary" {
			t.Fatal("projection altered the non-Secret source object")
		}
		local, err := reader.SnapshotImpact(context.Background(), graph.ImpactSnapshotOptions{MaxResources: 1, MaxEdges: 1})
		if err != nil || len(local.Resources) != 1 || len(local.Edges) != 0 || local.Resources[0].ClusterID != "" {
			t.Fatalf("standalone scope accidentally selected all clusters: %+v %v", local, err)
		}
	})
	for _, tc := range []struct {
		name string
		opts graph.ImpactSnapshotOptions
		want error
	}{
		{"resource limit", graph.ImpactSnapshotOptions{MaxResources: 3}, graph.ErrImpactSnapshotLimit},
		{"edge limit", graph.ImpactSnapshotOptions{MaxEdges: 3}, graph.ErrImpactSnapshotLimit},
		{"byte limit", graph.ImpactSnapshotOptions{MaxBytes: 1}, graph.ErrImpactSnapshotLimit},
		{"negative limit", graph.ImpactSnapshotOptions{MaxResources: -1}, graph.ErrImpactSnapshotOptions},
		{"excessive limit", graph.ImpactSnapshotOptions{MaxEdges: graph.MaxImpactEdges + 1}, graph.ErrImpactSnapshotOptions},
		{"exact row limits", graph.ImpactSnapshotOptions{MaxResources: 4, MaxEdges: 4}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := factory(t)
			seedImpactProjection(t, s)
			tc.opts.ClusterID = "prod"
			got, err := requireImpactReader(t, s).SnapshotImpact(context.Background(), tc.opts)
			if !errors.Is(err, tc.want) || tc.want != nil && got != nil || tc.want == nil && got == nil {
				t.Fatalf("projection=%+v error=%v, want %v", got, err, tc.want)
			}
			// A failed read cannot poison a lock, transaction, or pooled connection.
			_, err = requireImpactReader(t, s).SnapshotImpact(context.Background(), graph.ImpactSnapshotOptions{ClusterID: "prod"})
			if err != nil {
				t.Fatalf("read after bounded failure: %v", err)
			}
		})
	}
}

func seedImpactProjection(t *testing.T, s graph.GraphStore) *graph.Graph {
	t.Helper()
	ctx := context.Background()
	owner := graph.Resource{Kind: "Deployment", Name: "api", Namespace: "app", ClusterID: "prod", UID: "owner", GroupVersion: "apps/v1", ResourceVersion: "1"}
	child := graph.Resource{Kind: "Pod", Name: "api-pod", Namespace: "app", ClusterID: "prod", UID: "child", OwnerReferences: []graph.OwnerRef{{Kind: owner.Kind, Name: owner.Name, UID: owner.UID}}}
	config := graph.Resource{Kind: "ConfigMap", Name: "settings", Namespace: "config", ClusterID: "prod", UID: "config"}
	secret := graph.SecretReferenceResource("app", "credential", "prod")
	want := &graph.Graph{Resources: []graph.Resource{owner, child, config, secret}}
	want.Edges = []graph.Edge{
		{From: owner.ID(), To: config.ID(), Type: graph.EdgeTypeUsesConfigMap},
		{From: child.ID(), To: owner.ID(), Type: graph.EdgeTypeOwns},
		{From: owner.ID(), To: secret.ID(), Type: graph.EdgeTypeUsesSecret},
		{From: "prod:app/Pod/missing", To: config.ID(), Type: graph.EdgeTypeUsesConfigMap},
	}
	resources := slices.Clone(want.Resources)
	// JSON omitempty normalizes empty owners on Tier 2; Tier 1 must agree.
	resources[0].OwnerReferences = []graph.OwnerRef{}
	resources[0].Raw = map[string]any{"synthetic": "raw-canary"}
	resources[0].Labels = map[string]string{"test": "label-canary"}
	resources[0].Annotations = map[string]string{"test": "annotation-canary"}
	resources = append(resources, graph.Resource{Kind: "Pod", Name: "hidden", Namespace: "app", ClusterID: "other"}, graph.Resource{Kind: "Pod", Name: "local", Namespace: "app"})
	for _, resource := range resources {
		if err := s.UpsertResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
	}
	edges := append(slices.Clone(want.Edges), graph.Edge{From: "other:app/Pod/hidden", To: config.ID(), Type: graph.EdgeTypeSelects})
	for _, edge := range edges {
		edge.Attributes = map[string]string{"test": "attribute-canary"}
		if err := s.UpsertEdge(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
	return want
}

func requireImpactReader(t *testing.T, s graph.GraphStore) graph.ImpactSnapshotter {
	t.Helper()
	reader, ok := s.(graph.ImpactSnapshotter)
	if !ok {
		t.Fatal("built-in store lacks consistent impact projection")
	}
	return reader
}

func sortImpactProjection(g *graph.Graph) {
	slices.SortFunc(g.Resources, func(a, b graph.Resource) int { return strings.Compare(a.ID(), b.ID()) })
	slices.SortFunc(g.Edges, func(a, b graph.Edge) int {
		return strings.Compare(a.From+"\x00"+a.To+"\x00"+string(a.Type), b.From+"\x00"+b.To+"\x00"+string(b.Type))
	})
}
