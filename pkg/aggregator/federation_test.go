// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestMergeClusters_NoClustersIsError(t *testing.T) {
	_, err := MergeClusters(context.Background(), memory.New(), nil)
	if err == nil {
		t.Fatal("MergeClusters(empty): want error, got nil")
	}
}

func TestMergeClusters_UnionsResourcesAndScopesEdges(t *testing.T) {
	ctx := context.Background()
	s := memory.New()

	// prod: api → cfg. staging: api → cfg. The two clusters share
	// (ns, kind, name) triples; P3-T21's ID prefix keeps them apart.
	prodAPI := graph.Resource{Kind: "Pod", Name: "api", Namespace: "ns1", ClusterID: "prod"}
	prodCfg := graph.Resource{Kind: "ConfigMap", Name: "cfg", Namespace: "ns1", ClusterID: "prod"}
	stagingAPI := graph.Resource{Kind: "Pod", Name: "api", Namespace: "ns1", ClusterID: "staging"}
	stagingCfg := graph.Resource{Kind: "ConfigMap", Name: "cfg", Namespace: "ns1", ClusterID: "staging"}
	// An extra "dev" cluster the federation request will exclude.
	dev := graph.Resource{Kind: "Pod", Name: "api", Namespace: "ns1", ClusterID: "dev"}

	for _, r := range []graph.Resource{prodAPI, prodCfg, stagingAPI, stagingCfg, dev} {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatalf("UpsertResource %s: %v", r.ID(), err)
		}
	}
	for _, e := range []graph.Edge{
		{From: prodAPI.ID(), To: prodCfg.ID(), Type: graph.EdgeTypeUsesConfigMap},
		{From: stagingAPI.ID(), To: stagingCfg.ID(), Type: graph.EdgeTypeUsesConfigMap},
		// An edge to dev — out of the selected set; must be dropped.
		{From: prodAPI.ID(), To: dev.ID(), Type: graph.EdgeTypeRoutesTo},
	} {
		if err := s.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
	}

	view, err := MergeClusters(ctx, s, []string{"staging", "prod"}) // unsorted on purpose
	if err != nil {
		t.Fatalf("MergeClusters: %v", err)
	}
	// Clusters list comes back sorted.
	if want := []string{"prod", "staging"}; !equalStrings(view.Clusters, want) {
		t.Errorf("Clusters = %v, want %v", view.Clusters, want)
	}
	// 4 resources from prod+staging; 'dev' filtered out.
	if len(view.Nodes) != 4 {
		t.Errorf("len(Nodes) = %d, want 4", len(view.Nodes))
	}
	// Every node must carry its ClusterID for the UI cluster switcher.
	for _, n := range view.Nodes {
		if n.ClusterID == "" {
			t.Errorf("node %q has empty ClusterID", n.ID)
		}
	}
	// Only the two intra-cluster edges survive — the dev endpoint
	// dropped because dev wasn't in the selected set.
	if len(view.Edges) != 2 {
		t.Errorf("len(Edges) = %d, want 2 (cross-cluster-to-dev must drop)", len(view.Edges))
	}
}

func TestMergeClusters_DedupesClusterIDsAndSortsDeterministically(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "Pod", Name: "a", Namespace: "ns1", ClusterID: "prod"})

	view, err := MergeClusters(ctx, s, []string{"prod", "prod", "prod"})
	if err != nil {
		t.Fatalf("MergeClusters: %v", err)
	}
	if want := []string{"prod"}; !equalStrings(view.Clusters, want) {
		t.Errorf("Clusters = %v, want %v (duplicates must collapse)", view.Clusters, want)
	}
	if len(view.Nodes) != 1 {
		t.Errorf("len(Nodes) = %d, want 1", len(view.Nodes))
	}
}

func TestMergeClustersAtClusterLevel_OneNodePerCluster(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	// prod gets 2 Pods + 1 ConfigMap across two namespaces.
	for _, r := range []graph.Resource{
		{Kind: "Pod", Name: "a", Namespace: "ns1", ClusterID: "prod"},
		{Kind: "Pod", Name: "b", Namespace: "ns2", ClusterID: "prod"},
		{Kind: "ConfigMap", Name: "c", Namespace: "ns1", ClusterID: "prod"},
		// staging gets one Pod, one namespace.
		{Kind: "Pod", Name: "x", Namespace: "ns1", ClusterID: "staging"},
	} {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatalf("UpsertResource: %v", err)
		}
	}
	view, err := MergeClustersAtClusterLevel(ctx, s, []string{"prod", "staging"})
	if err != nil {
		t.Fatalf("MergeClustersAtClusterLevel: %v", err)
	}
	if view.Level != "cluster" {
		t.Errorf("Level = %q, want 'cluster'", view.Level)
	}
	if len(view.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2 (one per cluster)", len(view.Nodes))
	}
	if len(view.Edges) != 0 {
		t.Errorf("len(Edges) = %d, want 0 (v1.3 federation has no cross-cluster edges)", len(view.Edges))
	}

	byID := map[string]FederatedNode{}
	for _, n := range view.Nodes {
		byID[n.ID] = n
	}
	prod := byID["prod"]
	if prod.Type != "cluster" || prod.ClusterID != "prod" || prod.Label != "prod" {
		t.Errorf("prod node = %+v", prod)
	}
	if prod.ResourceCount != 3 {
		t.Errorf("prod.ResourceCount = %d, want 3", prod.ResourceCount)
	}
	if prod.NamespaceCount != 2 {
		t.Errorf("prod.NamespaceCount = %d, want 2", prod.NamespaceCount)
	}
	// KindSummary folds via foldKindSummary; prod has Pod=2,
	// ConfigMap=1 — both fit under summaryKindLimit (5).
	if prod.KindSummary["Pod"] != 2 || prod.KindSummary["ConfigMap"] != 1 {
		t.Errorf("prod.KindSummary = %v", prod.KindSummary)
	}
	staging := byID["staging"]
	if staging.ResourceCount != 1 || staging.NamespaceCount != 1 {
		t.Errorf("staging summary wrong: %+v", staging)
	}
}

func TestMergeClustersAtClusterLevel_EmptyClusterStillGetsANode(t *testing.T) {
	// A federation member that's attached but holds no resources
	// should still appear in the cluster-level view — the UI needs
	// to render it as a cluster card with zero resources, not omit
	// it. Confirms zero-count clusters survive the aggregation.
	ctx := context.Background()
	s := memory.New()
	view, err := MergeClustersAtClusterLevel(ctx, s, []string{"prod", "staging"})
	if err != nil {
		t.Fatalf("MergeClustersAtClusterLevel: %v", err)
	}
	if len(view.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(view.Nodes))
	}
	for _, n := range view.Nodes {
		if n.ResourceCount != 0 || n.NamespaceCount != 0 {
			t.Errorf("empty cluster node has non-zero counts: %+v", n)
		}
	}
}

func TestMergeClustersAtClusterLevel_NoClustersIsError(t *testing.T) {
	_, err := MergeClustersAtClusterLevel(context.Background(), memory.New(), nil)
	if err == nil {
		t.Fatal("want error for empty cluster list")
	}
}

func TestMergeClusters_LevelSetToResource(t *testing.T) {
	// MergeClusters sets Level="resource" so federation/graph
	// responses self-identify their zoom.
	ctx := context.Background()
	s := memory.New()
	_ = s.UpsertResource(ctx, graph.Resource{Kind: "Pod", Name: "a", Namespace: "ns1", ClusterID: "prod"})
	v, err := MergeClusters(ctx, s, []string{"prod"})
	if err != nil {
		t.Fatalf("MergeClusters: %v", err)
	}
	if v.Level != "resource" {
		t.Errorf("Level = %q, want 'resource'", v.Level)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
