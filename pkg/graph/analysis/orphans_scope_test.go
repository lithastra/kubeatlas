// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type orphanScopeStore struct {
	graph.GraphStore
	filter  graph.Filter
	listErr error
}

func (s *orphanScopeStore) Snapshot(context.Context) (*graph.Graph, error) {
	return nil, errors.New("orphan detection must not materialize a full snapshot")
}

func (s *orphanScopeStore) ListResources(ctx context.Context, filter graph.Filter) ([]graph.Resource, error) {
	s.filter = filter
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.GraphStore.ListResources(ctx, filter)
}

func TestOrphansPushdownPreservesCrossNamespaceIncomingEdges(t *testing.T) {
	ctx := context.Background()
	store := &orphanScopeStore{GraphStore: memory.New()}
	kept := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "referenced"}
	orphan := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "orphan"}
	outside := graph.Resource{Kind: "ReplicaSet", Namespace: "other", Name: "outside"}
	for _, r := range []graph.Resource{kept, orphan, outside} {
		_ = store.UpsertResource(ctx, r)
	}
	_ = store.UpsertEdge(ctx, graph.Edge{From: outside.ID(), To: kept.ID(), Type: graph.EdgeTypeOwns})
	got, err := analysis.DetectOrphans(ctx, store, analysis.OrphanOptions{Namespace: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if store.filter.Namespace != "demo" || len(got) != 1 || got[0].Resource.ID() != orphan.ID() {
		t.Fatalf("filter=%+v results=%+v", store.filter, got)
	}
	all, err := analysis.DetectOrphans(ctx, store, analysis.OrphanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if store.filter.Namespace != "" || len(all) != 2 {
		t.Fatalf("unscoped results=%+v", all)
	}
}

func TestOrphansPropagatesScopedListError(t *testing.T) {
	want := errors.New("list failed")
	store := &orphanScopeStore{GraphStore: memory.New(), listErr: want}
	_, err := analysis.DetectOrphans(context.Background(), store, analysis.OrphanOptions{Namespace: "demo"})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
