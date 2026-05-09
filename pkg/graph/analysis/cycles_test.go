// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestDetectCycles_TriangleIsReported(t *testing.T) {
	// A -> B -> C -> A is a 3-cycle. Tarjan must return it as a
	// single SCC of size 3.
	s := memory.New()
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

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatalf("DetectCycles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d: %+v", len(got), got)
	}
	if len(got[0].Members) != 3 {
		t.Errorf("expected 3 members, got %d", len(got[0].Members))
	}
	names := map[string]bool{}
	for _, r := range got[0].Members {
		names[r.Name] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !names[want] {
			t.Errorf("cycle missing %q (got %v)", want, names)
		}
	}
}

func TestDetectCycles_AcyclicReturnsEmpty(t *testing.T) {
	// A -> B -> C is a DAG. No SCCs of size > 1 → empty result.
	s := memory.New()
	ctx := context.Background()
	a := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"}
	b := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "b"}
	c := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "c"}
	for _, r := range []graph.Resource{a, b, c} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: c.ID(), Type: graph.EdgeTypeOwns})

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("acyclic graph: got %d cycles, want 0 (%+v)", len(got), got)
	}
}

func TestDetectCycles_SelfLoopIsNotReported(t *testing.T) {
	// A -> A is a single-vertex SCC. Playbook says skip these —
	// they're either extractor bugs or legitimate self-references
	// (e.g. ConfigMap referring to its own name) and we don't want
	// to spam dashboards.
	s := memory.New()
	ctx := context.Background()
	a := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "a"}
	_ = s.UpsertResource(ctx, a)
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: a.ID(), Type: graph.EdgeTypeUsesConfigMap})

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("self-loop reported as cycle: %+v", got)
	}
}

func TestDetectCycles_TwoSeparateCycles(t *testing.T) {
	// Two disjoint cycles: A↔B and C↔D. Tarjan finds both
	// independently.
	s := memory.New()
	ctx := context.Background()
	a := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "a"}
	b := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "b"}
	c := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "c"}
	d := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "d"}
	for _, r := range []graph.Resource{a, b, c, d} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeUsesConfigMap})
	_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: a.ID(), Type: graph.EdgeTypeUsesConfigMap})
	_ = s.UpsertEdge(ctx, graph.Edge{From: c.ID(), To: d.ID(), Type: graph.EdgeTypeUsesConfigMap})
	_ = s.UpsertEdge(ctx, graph.Edge{From: d.ID(), To: c.ID(), Type: graph.EdgeTypeUsesConfigMap})

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 cycles, got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if len(c.Members) != 2 {
			t.Errorf("each cycle should have 2 members, got %d", len(c.Members))
		}
	}
}

func TestDetectCycles_DanglingEdgesIgnored(t *testing.T) {
	// Edge to a non-existent target must not crash Tarjan; the
	// snapshot loop drops dangling edges silently.
	s := memory.New()
	ctx := context.Background()
	a := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "a"}
	_ = s.UpsertResource(ctx, a)
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: "demo/ConfigMap/missing", Type: graph.EdgeTypeUsesConfigMap})

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("dangling-edge graph: got %d cycles, want 0 (%+v)", len(got), got)
	}
}

func TestDetectCycles_LargeGraphFinishesUnderBudget(t *testing.T) {
	// Performance gate from the playbook: 5K-resource graph with
	// 5K edges must finish DetectCycles under 200ms. We seed a
	// linear DAG (no cycles) since worst-case Tarjan is the same
	// O(V+E) regardless of cycle count.
	if testing.Short() {
		t.Skip("skipping perf gate under -short")
	}
	s := memory.New()
	ctx := context.Background()
	const n = 5000
	for i := 0; i < n; i++ {
		_ = s.UpsertResource(ctx, graph.Resource{
			Kind:      "ConfigMap",
			Namespace: "demo",
			Name:      fmt.Sprintf("cm-%05d", i),
		})
	}
	for i := 0; i < n-1; i++ {
		_ = s.UpsertEdge(ctx, graph.Edge{
			From: fmt.Sprintf("demo/ConfigMap/cm-%05d", i),
			To:   fmt.Sprintf("demo/ConfigMap/cm-%05d", i+1),
			Type: graph.EdgeTypeUsesConfigMap,
		})
	}
	start := time.Now()
	got, err := analysis.DetectCycles(ctx, s)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("DAG must yield no cycles, got %d", len(got))
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("DetectCycles 5K/5K took %s, want < 200ms", elapsed)
	}
}
