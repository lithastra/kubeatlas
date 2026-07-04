// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestOtelRuntimeEdges_RoundTrip(t *testing.T) {
	s, ctx := newSpanStore(t)
	t0 := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	edges := []graph.RuntimeEdge{
		{
			FromID: "petclinic/Deployment/frontend", ToID: "petclinic/Deployment/api",
			FromService: "frontend", ToService: "api", Namespace: "petclinic",
			FirstSeen: t0, LastSeen: t0, CallCount: 3,
		},
		{
			FromID: "other/Deployment/a", ToID: "other/Deployment/b",
			FromService: "a", ToService: "b", Namespace: "other",
			FirstSeen: t0, LastSeen: t0, CallCount: 1,
		},
	}
	if err := s.UpsertRuntimeEdges(ctx, edges); err != nil {
		t.Fatalf("UpsertRuntimeEdges: %v", err)
	}

	// Namespace filter.
	got, err := s.QueryRuntimeEdges(ctx, "petclinic", t0.Add(-time.Hour))
	if err != nil {
		t.Fatalf("QueryRuntimeEdges(petclinic): %v", err)
	}
	if len(got) != 1 || got[0].ToID != "petclinic/Deployment/api" || got[0].CallCount != 3 {
		t.Fatalf("QueryRuntimeEdges(petclinic) = %+v, want one frontend->api count 3", got)
	}

	// Empty namespace returns all.
	all, err := s.QueryRuntimeEdges(ctx, "", t0.Add(-time.Hour))
	if err != nil {
		t.Fatalf("QueryRuntimeEdges(all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("QueryRuntimeEdges(all) = %d, want 2", len(all))
	}

	// Upsert folds a re-observation: later last_seen, earlier first_seen,
	// peak (GREATEST) call_count.
	later := t0.Add(30 * time.Minute)
	if err := s.UpsertRuntimeEdges(ctx, []graph.RuntimeEdge{{
		FromID: "petclinic/Deployment/frontend", ToID: "petclinic/Deployment/api",
		FromService: "frontend", ToService: "api", Namespace: "petclinic",
		FirstSeen: t0.Add(-time.Minute), LastSeen: later, CallCount: 2,
	}}); err != nil {
		t.Fatalf("UpsertRuntimeEdges (fold): %v", err)
	}
	got, err = s.QueryRuntimeEdges(ctx, "petclinic", t0.Add(-time.Hour))
	if err != nil {
		t.Fatalf("QueryRuntimeEdges after fold: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after fold: %d rows, want 1 (upsert, not insert)", len(got))
	}
	if !got[0].LastSeen.Equal(later) {
		t.Errorf("last_seen = %v, want %v (GREATEST)", got[0].LastSeen, later)
	}
	if !got[0].FirstSeen.Equal(t0.Add(-time.Minute)) {
		t.Errorf("first_seen = %v, want %v (LEAST)", got[0].FirstSeen, t0.Add(-time.Minute))
	}
	if got[0].CallCount != 3 {
		t.Errorf("call_count = %d, want 3 (GREATEST of 3 and 2)", got[0].CallCount)
	}

	// Recency floor: a since after last_seen hides the edge.
	fresh, err := s.QueryRuntimeEdges(ctx, "", later.Add(time.Hour))
	if err != nil {
		t.Fatalf("QueryRuntimeEdges(recency): %v", err)
	}
	if len(fresh) != 0 {
		t.Errorf("recency floor: got %d, want 0", len(fresh))
	}

	// Prune: delete edges not seen since a cutoff after both edges.
	n, err := s.DeleteOldRuntimeEdges(ctx, later.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteOldRuntimeEdges: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned = %d, want 2", n)
	}
	remaining, err := s.QueryRuntimeEdges(ctx, "", t0.Add(-time.Hour))
	if err != nil {
		t.Fatalf("QueryRuntimeEdges after prune: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("after prune: %d rows, want 0", len(remaining))
	}
}
