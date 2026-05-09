// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestCycles_Handler_TriangleReported(t *testing.T) {
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		a := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "a"}
		b := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "b"}
		c := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "c"}
		for _, r := range []graph.Resource{a, b, c} {
			_ = s.UpsertResource(ctx, r)
		}
		_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeUsesConfigMap})
		_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: c.ID(), Type: graph.EdgeTypeUsesConfigMap})
		_ = s.UpsertEdge(ctx, graph.Edge{From: c.ID(), To: a.ID(), Type: graph.EdgeTypeUsesConfigMap})
	})
	defer stop()

	var resp api.CyclesResponse
	r, _ := getJSON(t, base+"/api/v1alpha1/cycles", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if resp.Count != 1 || len(resp.Cycles) != 1 {
		t.Fatalf("expected 1 cycle, got %+v", resp)
	}
	if len(resp.Cycles[0].Members) != 3 {
		t.Errorf("expected 3 members, got %d", len(resp.Cycles[0].Members))
	}
}

func TestCycles_Handler_HealthyClusterIsEmpty(t *testing.T) {
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "api"})
	})
	defer stop()

	var resp api.CyclesResponse
	getJSON(t, base+"/api/v1alpha1/cycles", &resp)
	if resp.Count != 0 {
		t.Errorf("healthy cluster: got %d cycles, want 0 (%+v)", resp.Count, resp.Cycles)
	}
	if resp.Cycles == nil {
		t.Error("Cycles must be non-nil empty slice for client convenience")
	}
}
