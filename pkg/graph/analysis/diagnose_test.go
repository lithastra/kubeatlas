// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// diagnoseSeed builds a small fixture exercising every report section:
//
//   - petclinic/Deployment api  -> petclinic/ConfigMap app-config   (a dependency)
//   - petclinic/ReplicaSet lonely-rs                                 (an orphan)
//   - other/ConfigMap a <-> other/ConfigMap b                        (a 2-cycle)
func diagnoseSeed(t *testing.T) *memory.Store {
	t.Helper()
	s := memory.New()
	ctx := context.Background()
	add := func(r graph.Resource) {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatalf("seed resource: %v", err)
		}
	}
	edge := func(from, to string, et graph.EdgeType) {
		if err := s.UpsertEdge(ctx, graph.Edge{From: from, To: to, Type: et}); err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}

	dep := graph.Resource{Kind: "Deployment", Namespace: "petclinic", Name: "api"}
	cm := graph.Resource{Kind: "ConfigMap", Namespace: "petclinic", Name: "app-config"}
	rs := graph.Resource{Kind: "ReplicaSet", Namespace: "petclinic", Name: "lonely-rs"}
	cmA := graph.Resource{Kind: "ConfigMap", Namespace: "other", Name: "a"}
	cmB := graph.Resource{Kind: "ConfigMap", Namespace: "other", Name: "b"}
	for _, r := range []graph.Resource{dep, cm, rs, cmA, cmB} {
		add(r)
	}
	edge(dep.ID(), cm.ID(), graph.EdgeTypeUsesConfigMap)
	edge(cmA.ID(), cmB.ID(), graph.EdgeTypeUsesConfigMap)
	edge(cmB.ID(), cmA.ID(), graph.EdgeTypeUsesConfigMap)
	return s
}

func TestGenerateReport_ClusterScope(t *testing.T) {
	ctx := context.Background()
	s := diagnoseSeed(t)

	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{AllNamespaces: true}, "v1.4.0-test")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	if rep.ResourceCount != 5 {
		t.Errorf("ResourceCount = %d, want 5", rep.ResourceCount)
	}
	if rep.EdgeCount != 3 {
		t.Errorf("EdgeCount = %d, want 3", rep.EdgeCount)
	}
	if rep.KubeAtlasVersion != "v1.4.0-test" {
		t.Errorf("KubeAtlasVersion = %q, want v1.4.0-test", rep.KubeAtlasVersion)
	}

	if len(rep.Orphans) != 1 || rep.Orphans[0].Resource.Name != "lonely-rs" {
		t.Errorf("Orphans = %+v, want one entry for lonely-rs", rep.Orphans)
	}
	if len(rep.Cycles) != 1 || len(rep.Cycles[0].Members) != 2 {
		t.Errorf("Cycles = %+v, want one 2-member cycle", rep.Cycles)
	}
	// cm, a, b each have exactly one dependent; dep and lonely-rs have
	// none and are dropped.
	if len(rep.TopBlastRadius) != 3 {
		t.Fatalf("TopBlastRadius len = %d, want 3 (got %+v)", len(rep.TopBlastRadius), rep.TopBlastRadius)
	}
	for _, e := range rep.TopBlastRadius {
		if e.Affected != 1 {
			t.Errorf("blast entry %s/%s affected = %d, want 1", e.Resource.Kind, e.Resource.Name, e.Affected)
		}
	}
}

func TestGenerateReport_PolicyViolations(t *testing.T) {
	ctx := context.Background()
	s := memory.New()

	add := func(r graph.Resource) {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatalf("seed resource: %v", err)
		}
	}
	enforces := func(from, to string, attrs map[string]string) {
		if err := s.UpsertEdge(ctx, graph.Edge{
			From: from, To: to, Type: graph.EdgeTypeEnforces, Attributes: attrs,
		}); err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}

	gk := graph.Resource{Kind: "K8sRequiredLabels", Namespace: "", Name: "must-have-owner"}
	kv := graph.Resource{Kind: "ClusterPolicy", Namespace: "", Name: "disallow-latest"}
	bad := graph.Resource{Kind: "Deployment", Namespace: "petclinic", Name: "api"}
	good := graph.Resource{Kind: "Deployment", Namespace: "petclinic", Name: "web"}
	for _, r := range []graph.Resource{gk, kv, bad, good} {
		add(r)
	}
	// Gatekeeper violation, Kyverno failure, and a compliant Kyverno edge.
	enforces(gk.ID(), bad.ID(), map[string]string{"violated": "true", "violation_message": "missing owner label"})
	enforces(kv.ID(), bad.ID(), map[string]string{"result": "fail"})
	enforces(kv.ID(), good.ID(), map[string]string{"result": "pass"})

	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{AllNamespaces: true}, "v")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	if len(rep.PolicyViolations) != 2 {
		t.Fatalf("PolicyViolations len = %d, want 2 (got %+v)", len(rep.PolicyViolations), rep.PolicyViolations)
	}
	// Sorted by policy id then resource: the Kyverno ClusterPolicy id
	// ("/ClusterPolicy/...") sorts before the Gatekeeper Constraint id
	// ("/K8sRequiredLabels/..."), so [0] is the Kyverno failure.
	if got := rep.PolicyViolations[0]; got.Policy != kv.ID() || got.Resource != bad.ID() || got.Message != "" {
		t.Errorf("PolicyViolations[0] = %+v, want kyverno failure of %s", got, bad.ID())
	}
	if got := rep.PolicyViolations[1]; got.Policy != gk.ID() || got.Resource != bad.ID() || got.Message != "missing owner label" {
		t.Errorf("PolicyViolations[1] = %+v, want gatekeeper violation of %s", got, bad.ID())
	}
}

func TestGenerateReport_NamespaceScope(t *testing.T) {
	ctx := context.Background()
	s := diagnoseSeed(t)

	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{Namespace: "petclinic"}, "v")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	// Subgraph is the three petclinic resources plus the single intra-
	// namespace edge (api -> app-config).
	if rep.ResourceCount != 3 {
		t.Errorf("ResourceCount = %d, want 3", rep.ResourceCount)
	}
	if rep.EdgeCount != 1 {
		t.Errorf("EdgeCount = %d, want 1", rep.EdgeCount)
	}
	if rep.Scope.Namespace != "petclinic" || rep.Scope.AllNamespaces {
		t.Errorf("Scope = %+v, want namespace=petclinic", rep.Scope)
	}
	if len(rep.Orphans) != 1 {
		t.Errorf("Orphans len = %d, want 1", len(rep.Orphans))
	}
	// The a<->b cycle lives in namespace "other" and must be filtered out.
	if len(rep.Cycles) != 0 {
		t.Errorf("Cycles = %+v, want none (cross-namespace cycle filtered)", rep.Cycles)
	}
	if len(rep.TopBlastRadius) != 1 || rep.TopBlastRadius[0].Resource.Name != "app-config" {
		t.Errorf("TopBlastRadius = %+v, want one entry for app-config", rep.TopBlastRadius)
	}
}

func TestGenerateReport_EmptyNamespace(t *testing.T) {
	ctx := context.Background()
	s := diagnoseSeed(t)

	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{Namespace: "ghost"}, "v")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if rep.ResourceCount != 0 || rep.EdgeCount != 0 {
		t.Errorf("counts = (%d,%d), want (0,0)", rep.ResourceCount, rep.EdgeCount)
	}
	if len(rep.Orphans) != 0 || len(rep.Cycles) != 0 || len(rep.TopBlastRadius) != 0 {
		t.Errorf("expected all sections empty, got orphans=%d cycles=%d blast=%d",
			len(rep.Orphans), len(rep.Cycles), len(rep.TopBlastRadius))
	}
}

func TestGenerateReport_MultipleCycles(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	add := func(r graph.Resource) {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	edge := func(from, to string) {
		if err := s.UpsertEdge(ctx, graph.Edge{From: from, To: to, Type: graph.EdgeTypeUsesConfigMap}); err != nil {
			t.Fatal(err)
		}
	}
	x := graph.Resource{Kind: "ConfigMap", Namespace: "ns", Name: "x"}
	y := graph.Resource{Kind: "ConfigMap", Namespace: "ns", Name: "y"}
	p := graph.Resource{Kind: "ConfigMap", Namespace: "ns", Name: "p"}
	q := graph.Resource{Kind: "ConfigMap", Namespace: "ns", Name: "q"}
	for _, r := range []graph.Resource{x, y, p, q} {
		add(r)
	}
	edge(x.ID(), y.ID())
	edge(y.ID(), x.ID())
	edge(p.ID(), q.ID())
	edge(q.ID(), p.ID())

	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{AllNamespaces: true}, "v")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if len(rep.Cycles) != 2 {
		t.Errorf("Cycles len = %d, want 2", len(rep.Cycles))
	}
}

func TestRenderHTML(t *testing.T) {
	ctx := context.Background()
	s := diagnoseSeed(t)

	// Namespace scope: the report header reads "namespace petclinic"
	// and the orphan table lists lonely-rs.
	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{Namespace: "petclinic"}, "v9.9.9")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	html, err := analysis.RenderHTML(ctx, rep)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	got := string(html)
	for _, want := range []string{
		"KubeAtlas Diagnostic Report",
		"namespace petclinic",
		"lonely-rs",
		"v9.9.9",
		"<!DOCTYPE html>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
	}

	// Cluster scope renders the "all namespaces" header.
	repAll, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{AllNamespaces: true}, "v")
	if err != nil {
		t.Fatalf("GenerateReport (cluster): %v", err)
	}
	htmlAll, err := analysis.RenderHTML(ctx, repAll)
	if err != nil {
		t.Fatalf("RenderHTML (cluster): %v", err)
	}
	if !strings.Contains(string(htmlAll), "all namespaces") {
		t.Error("cluster-scope HTML missing 'all namespaces' header")
	}
}
