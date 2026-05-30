// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// errBoom and errSnapshotStore drive the store-error path of
// GenerateReport. Embedding graph.GraphStore (nil) satisfies the
// interface; only Snapshot is exercised because it is the first call
// and fails immediately.
var errBoom = errors.New("boom")

type errSnapshotStore struct{ graph.GraphStore }

func (errSnapshotStore) Snapshot(context.Context) (*graph.Graph, error) { return nil, errBoom }

func TestGenerateReport_StoreError(t *testing.T) {
	_, err := analysis.GenerateReport(context.Background(), errSnapshotStore{},
		analysis.DiagnoseScope{AllNamespaces: true}, "v")
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
}

// TestGenerateReport_NamespaceCycleRetained covers the keep path of
// filterCyclesToNamespace: a cycle whose members live in the scoped
// namespace must survive the filter.
func TestGenerateReport_NamespaceCycleRetained(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	a := graph.Resource{Kind: "ConfigMap", Namespace: "petclinic", Name: "a"}
	b := graph.Resource{Kind: "ConfigMap", Namespace: "petclinic", Name: "b"}
	for _, r := range []graph.Resource{a, b} {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeUsesConfigMap})
	_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: a.ID(), Type: graph.EdgeTypeUsesConfigMap})

	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{Namespace: "petclinic"}, "v")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if len(rep.Cycles) != 1 {
		t.Errorf("Cycles len = %d, want 1 (in-namespace cycle retained)", len(rep.Cycles))
	}
}

// TestTopBlastRadius_Truncation covers the top-N cap: 12 resources each
// with a dependent must yield exactly 10 ranked entries.
func TestTopBlastRadius_Truncation(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	for i := 0; i < 12; i++ {
		cm := graph.Resource{Kind: "ConfigMap", Namespace: "ns", Name: fmt.Sprintf("cm-%02d", i)}
		dep := graph.Resource{Kind: "Deployment", Namespace: "ns", Name: fmt.Sprintf("dep-%02d", i)}
		if err := s.UpsertResource(ctx, cm); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertResource(ctx, dep); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{AllNamespaces: true}, "v")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if len(rep.TopBlastRadius) != 10 {
		t.Errorf("TopBlastRadius len = %d, want 10 (capped)", len(rep.TopBlastRadius))
	}
}

// TestRenderHTML_NoGraphviz covers the graceful-degradation branch:
// with no 'dot' on PATH, the report still renders, substituting a
// notice for the graph image.
func TestRenderHTML_NoGraphviz(t *testing.T) {
	t.Setenv("PATH", "")
	ctx := context.Background()
	s := diagnoseSeed(t)
	rep, err := analysis.GenerateReport(ctx, s, analysis.DiagnoseScope{AllNamespaces: true}, "v")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	html, err := analysis.RenderHTML(ctx, rep)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if !strings.Contains(string(html), "Graph image unavailable") {
		t.Error("HTML missing the graphviz-unavailable notice")
	}
}
