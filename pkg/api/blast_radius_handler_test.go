// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

func seedBlastRadiusFixture(s graph.GraphStore) {
	ctx := context.Background()
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "api"}
	rs := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "api-rs"}
	pod := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "api-1"}
	cm := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "app-config"}
	other := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "lonely"}
	for _, r := range []graph.Resource{dep, rs, pod, cm, other} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: rs.ID(), To: pod.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: pod.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap})
}

func TestBlastRadius_Handler_HappyPath(t *testing.T) {
	base, _, stop := seedAndServe(t, seedBlastRadiusFixture)
	defer stop()

	var resp api.BlastRadiusResponse
	r, _ := getJSON(t, base+"/api/v1alpha1/blast-radius/demo/ConfigMap/app-config", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if resp.Source.Name != "app-config" {
		t.Errorf("source.name = %q, want app-config", resp.Source.Name)
	}
	if resp.Count != len(resp.Affected) {
		t.Errorf("count %d != len(affected) %d", resp.Count, len(resp.Affected))
	}
	names := map[string]bool{}
	for _, r := range resp.Affected {
		names[r.Name] = true
	}
	for _, want := range []string{"api", "api-rs", "api-1"} {
		if !names[want] {
			t.Errorf("missing %q in affected: %v", want, names)
		}
	}
	if names["lonely"] {
		t.Errorf("unrelated resource leaked into blast radius: %v", names)
	}
	if names["app-config"] {
		t.Errorf("source must be excluded by default, got %v", names)
	}
}

func TestBlastRadius_Handler_IncludeSource(t *testing.T) {
	base, _, stop := seedAndServe(t, seedBlastRadiusFixture)
	defer stop()

	var resp api.BlastRadiusResponse
	getJSON(t, base+"/api/v1alpha1/blast-radius/demo/ConfigMap/app-config?include_source=true", &resp)
	names := map[string]bool{}
	for _, r := range resp.Affected {
		names[r.Name] = true
	}
	if !names["app-config"] {
		t.Errorf("include_source=true should include source, got %v", names)
	}
}

func TestBlastRadius_Handler_NotFound(t *testing.T) {
	base, _, stop := seedAndServe(t, seedBlastRadiusFixture)
	defer stop()

	r, _ := getJSON(t, base+"/api/v1alpha1/blast-radius/demo/ConfigMap/missing", nil)
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", r.StatusCode)
	}
}

func TestBlastRadius_Handler_BadMaxDepth(t *testing.T) {
	base, _, stop := seedAndServe(t, seedBlastRadiusFixture)
	defer stop()

	r, _ := getJSON(t, base+"/api/v1alpha1/blast-radius/demo/ConfigMap/app-config?max_depth=abc", nil)
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", r.StatusCode)
	}
}

func TestBlastRadius_Handler_EdgeTypeFilter(t *testing.T) {
	base, _, stop := seedAndServe(t, seedBlastRadiusFixture)
	defer stop()

	// Filtering to OWNS only should still reach api-rs and api-1 from
	// app-config? No — app-config has only USES_CONFIGMAP incoming.
	// With OWNS-only, blast radius is empty.
	var resp api.BlastRadiusResponse
	getJSON(t, base+"/api/v1alpha1/blast-radius/demo/ConfigMap/app-config?edge_types=OWNS", &resp)
	if resp.Count != 0 {
		t.Errorf("OWNS-only filter from CM should yield 0 affected, got %d (%v)", resp.Count, resp.Affected)
	}
}
