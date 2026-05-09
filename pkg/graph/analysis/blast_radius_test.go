// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestBlastRadius_LineGraph(t *testing.T) {
	// A -> B -> C -> D, plus A -> E. BlastRadius(D) reaches C, B, A
	// (but not E, which is on the other branch from A).
	s := memory.New()
	ctx := context.Background()
	a := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"}
	b := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "b"}
	c := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "c"}
	d := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "d"}
	e := graph.Resource{Kind: "Service", Namespace: "demo", Name: "e"}
	for _, r := range []graph.Resource{a, b, c, d, e} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: c.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: c.ID(), To: d.ID(), Type: graph.EdgeTypeUsesConfigMap})
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: e.ID(), Type: graph.EdgeTypeSelects})

	got, err := analysis.BlastRadius(ctx, s, d.ID(), analysis.Options{MaxDepth: 5})
	if err != nil {
		t.Fatalf("BlastRadius: %v", err)
	}
	names := nameSet(got)
	for _, want := range []string{"a", "b", "c"} {
		if !names[want] {
			t.Errorf("BlastRadius(d) missing %q (got %v)", want, names)
		}
	}
	if names["e"] {
		t.Errorf("BlastRadius(d) should not include e (sibling branch), got %v", names)
	}
	if names["d"] {
		t.Errorf("BlastRadius without IncludeSource should omit the source, got %v", names)
	}
}

func TestBlastRadius_IncludeSource(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	a := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"}
	b := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "b"}
	_ = s.UpsertResource(ctx, a)
	_ = s.UpsertResource(ctx, b)
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})

	got, err := analysis.BlastRadius(ctx, s, b.ID(), analysis.Options{IncludeSource: true})
	if err != nil {
		t.Fatal(err)
	}
	names := nameSet(got)
	if !names["b"] {
		t.Errorf("IncludeSource=true should include b, got %v", names)
	}
	if !names["a"] {
		t.Errorf("expected a in result, got %v", names)
	}
}

func TestBlastRadius_RejectsEmptyID(t *testing.T) {
	s := memory.New()
	_, err := analysis.BlastRadius(context.Background(), s, "", analysis.Options{})
	if err == nil {
		t.Fatal("expected error on empty startID")
	}
}

func TestBlastRadius_DepthCap(t *testing.T) {
	// 3-hop chain; MaxDepth=2 reaches only 2 hops back.
	s := memory.New()
	ctx := context.Background()
	a := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"}
	b := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "b"}
	c := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "c"}
	d := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "d"}
	for _, r := range []graph.Resource{a, b, c, d} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: c.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: c.ID(), To: d.ID(), Type: graph.EdgeTypeUsesConfigMap})

	got, err := analysis.BlastRadius(ctx, s, d.ID(), analysis.Options{MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	names := nameSet(got)
	if names["a"] {
		t.Errorf("MaxDepth=2 should not reach a (3 hops away), got %v", names)
	}
	if !names["c"] || !names["b"] {
		t.Errorf("MaxDepth=2 should reach b and c, got %v", names)
	}
}

func nameSet(rs []graph.Resource) map[string]bool {
	m := make(map[string]bool, len(rs))
	for _, r := range rs {
		m[r.Name] = true
	}
	return m
}
