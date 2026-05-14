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

// TestDetectCycles_TriangleHasUnknownCategory verifies that a
// "plain" cycle (no Secret member, no opt-in annotation) gets
// Category=unknown — the default bucket that verifiers / the
// future GitHub Action treat as actionable noise.
func TestDetectCycles_TriangleHasUnknownCategory(t *testing.T) {
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
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(got))
	}
	if got[0].Category != analysis.CycleCategoryUnknown {
		t.Errorf("expected category=unknown, got %q", got[0].Category)
	}
}

// TestDetectCycles_BootstrapCertCategorized verifies the
// "controller owns its own cert Secret AND consumes it" 2-cycle
// is recognised — this is the shape the Phase 2 verifier (and
// every cert-manager / CNPG / kyverno install) emits.
func TestDetectCycles_BootstrapCertCategorized(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	// Webhook Deployment owns a TLS Secret (via OwnerReferences)
	// AND consumes the same Secret (USES_SECRET edge). Tarjan sees
	// a 2-cycle; the classifier should see "bootstrap-cert".
	dep := graph.Resource{Kind: "Deployment", Namespace: "cnpg-system", Name: "cnpg-controller"}
	sec := graph.Resource{
		Kind: "Secret", Namespace: "cnpg-system", Name: "cnpg-webhook-cert",
		OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "cnpg-controller"}},
	}
	for _, r := range []graph.Resource{dep, sec} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: sec.ID(), Type: graph.EdgeTypeUsesSecret})
	_ = s.UpsertEdge(ctx, graph.Edge{From: sec.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns})

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d: %+v", len(got), got)
	}
	if got[0].Category != analysis.CycleCategoryBootstrapCert {
		t.Errorf("expected category=bootstrap-cert, got %q", got[0].Category)
	}
}

// TestDetectCycles_BootstrapCertRequiresSecret verifies the
// classifier is strict: a 2-cycle that happens to have an
// ownerRef between non-Secret members must NOT be tagged as
// bootstrap-cert (it could be a real ownership-loop bug).
func TestDetectCycles_BootstrapCertRequiresSecret(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	// Two Deployments referencing each other; one names the other
	// in OwnerReferences. No Secret involved.
	a := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "a",
		OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "b"}},
	}
	b := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "b"}
	for _, r := range []graph.Resource{a, b} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: a.ID(), Type: graph.EdgeTypeOwns})

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(got))
	}
	if got[0].Category == analysis.CycleCategoryBootstrapCert {
		t.Errorf("non-Secret cycle wrongly tagged bootstrap-cert: %+v", got[0])
	}
}

// TestDetectCycles_IntentionalCategorized verifies that a single
// annotated member is enough to flip the whole cycle out of the
// "unknown" bucket. The contract is intentionally lenient: in
// multi-team setups, only one owner of the cycle needs to opt in.
func TestDetectCycles_IntentionalCategorized(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	// 3-cycle A→B→C→A; only A carries the opt-in annotation.
	a := graph.Resource{
		Kind: "ConfigMap", Namespace: "demo", Name: "a",
		Annotations: map[string]string{"kubeatlas.io/intentional-cycle": "true"},
	}
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
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(got))
	}
	if got[0].Category != analysis.CycleCategoryIntentional {
		t.Errorf("expected category=intentional, got %q", got[0].Category)
	}
}

// TestDetectCycles_BootstrapCertWinsOverIntentional verifies
// classifier precedence: bootstrap-cert takes priority over
// intentional even if the Secret happens to carry the annotation.
// Rationale: bootstrap-cert is structurally certain (it can only
// be what the name implies); intentional is a more generic opt-out.
// Reporting the structural category gives the operator more useful
// information.
func TestDetectCycles_BootstrapCertWinsOverIntentional(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "ctrl"}
	sec := graph.Resource{
		Kind: "Secret", Namespace: "demo", Name: "ctrl-cert",
		OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "ctrl"}},
		Annotations:     map[string]string{"kubeatlas.io/intentional-cycle": "true"},
	}
	for _, r := range []graph.Resource{dep, sec} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: sec.ID(), Type: graph.EdgeTypeUsesSecret})
	_ = s.UpsertEdge(ctx, graph.Edge{From: sec.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns})

	got, err := analysis.DetectCycles(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Category != analysis.CycleCategoryBootstrapCert {
		t.Errorf("precedence: expected bootstrap-cert (structural), got %q", got[0].Category)
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
