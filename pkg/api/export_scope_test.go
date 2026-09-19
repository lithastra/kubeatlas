// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type exportScopeStore struct {
	graph.GraphStore
	snapshotCalls int
	subgraphCalls int
	namespace     string
	err           error
}

func (s *exportScopeStore) Snapshot(ctx context.Context) (*graph.Graph, error) {
	s.snapshotCalls++
	return s.GraphStore.Snapshot(ctx)
}

func (s *exportScopeStore) GetNamespaceSubgraph(ctx context.Context, ns string, labels map[string]string) (*graph.Graph, error) {
	s.subgraphCalls++
	s.namespace = ns
	if s.err != nil {
		return nil, s.err
	}
	return s.GraphStore.GetNamespaceSubgraph(ctx, ns, labels)
}

func TestExportScopePushdown(t *testing.T) {
	// Missing dot makes the small-view outcome deterministic on all hosts.
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	store := memory.New()
	_ = store.UpsertResource(ctx, graph.Resource{Kind: "ConfigMap", Namespace: "small", Name: "visible"})
	for i := range 1001 {
		_ = store.UpsertResource(ctx, graph.Resource{Kind: "ConfigMap", Namespace: "unrelated", Name: fmt.Sprintf("cm-%d", i)})
	}
	for _, tt := range []struct {
		name, query, namespace       string
		status, snapshots, subgraphs int
		err                          error
	}{
		{name: "namespace excludes large unrelated data", query: "?namespace=small", namespace: "small", status: http.StatusServiceUnavailable, subgraphs: 1},
		{name: "cluster retains node cap", status: http.StatusRequestEntityTooLarge, snapshots: 1},
		{name: "namespace retains node cap", query: "?namespace=unrelated", namespace: "unrelated", status: http.StatusRequestEntityTooLarge, subgraphs: 1},
		{name: "scoped read error is not retried as snapshot", query: "?namespace=small", namespace: "small", status: http.StatusInternalServerError, subgraphs: 1, err: errors.New("scoped read failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spy := &exportScopeStore{GraphStore: store, err: tt.err}
			server := New("", spy, aggregator.NewRegistry())
			response := httptest.NewRecorder()
			server.handleExport(response, httptest.NewRequest(http.MethodGet, "/api/v1alpha1/export"+tt.query, nil))
			if response.Code != tt.status || spy.snapshotCalls != tt.snapshots || spy.subgraphCalls != tt.subgraphs || spy.namespace != tt.namespace {
				t.Fatalf("status=%d snapshot=%d subgraph=%d namespace=%q", response.Code, spy.snapshotCalls, spy.subgraphCalls, spy.namespace)
			}
		})
	}
}

func TestExportScopedDOTPreservesVisibleGraph(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	a := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "a"}
	b := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "b"}
	c := graph.Resource{Kind: "ConfigMap", Namespace: "other", Name: "c"}
	for _, r := range []graph.Resource{a, b, c} {
		_ = store.UpsertResource(ctx, r)
	}
	for _, e := range []graph.Edge{
		{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeUsesConfigMap},
		{From: a.ID(), To: c.ID(), Type: graph.EdgeTypeUsesConfigMap},
		{From: c.ID(), To: b.ID(), Type: graph.EdgeTypeUsesConfigMap},
		{From: a.ID(), To: "demo/ConfigMap/missing", Type: graph.EdgeTypeUsesConfigMap},
	} {
		_ = store.UpsertEdge(ctx, e)
	}
	full, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := store.GetNamespaceSubgraph(ctx, "demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	options := graph.DOTOptions{Namespace: "demo"}
	if graph.ToDOTOptions(scoped, options) != graph.ToDOTOptions(full, options) {
		t.Fatal("namespace pushdown changed visible DOT nodes or edges")
	}
}
