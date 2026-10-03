// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// The Web characterization tests consume this same synthetic graph and oracle.
// These tests preserve observed legacy behavior, not v1.7 impact acceptance.
type blastReferenceFixture struct {
	SchemaVersion int    `json:"schemaVersion"`
	Purpose       string `json:"purpose"`
	Resources     []struct {
		ID string `json:"id"`
		graph.Resource
	} `json:"resources"`
	Edges   []graph.Edge `json:"edges"`
	Queries []struct {
		Name     string   `json:"name"`
		Root     string   `json:"root"`
		Depth    int      `json:"depth"`
		Incoming []string `json:"incoming"`
		Outgoing []string `json:"outgoing"`
	} `json:"queries"`
}

func loadBlastReference(t *testing.T) blastReferenceFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "test", "fixtures", "impact-analysis", "reference-graph.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture blastReferenceFixture
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || len(fixture.Queries) != 8 {
		t.Fatal("unexpected legacy reference fixture version or query count")
	}
	ids := make(map[string]bool)
	for _, r := range fixture.Resources {
		if r.ID != r.Resource.ID() || ids[r.ID] {
			t.Fatalf("invalid or duplicate fixture identity %q", r.ID)
		}
		ids[r.ID] = true
		if r.Kind == "Secret" && (r.UID != "" || r.Raw != nil || len(r.Annotations) != 0 || len(r.Labels) != 0) {
			t.Fatal("reference fixture must not contain a Secret object payload")
		}
	}
	for _, edge := range fixture.Edges {
		if !ids[edge.From] || !ids[edge.To] || !slices.Contains(graph.AllEdgeTypes, edge.Type) {
			t.Fatalf("invalid fixture edge: %v", edge)
		}
	}
	names := make(map[string]bool)
	for _, query := range fixture.Queries {
		if names[query.Name] || !ids[query.Root] || query.Depth < 1 || query.Depth > 10 {
			t.Fatalf("invalid query %q", query.Name)
		}
		names[query.Name] = true
		for _, expected := range [][]string{query.Incoming, query.Outgoing} {
			seen := make(map[string]bool)
			for _, id := range expected {
				if !ids[id] || id == query.Root || seen[id] {
					t.Fatalf("invalid expected result %q in %s", id, query.Name)
				}
				seen[id] = true
			}
		}
	}
	return fixture
}

func TestBlastRadius_SharedReferenceGraph(t *testing.T) {
	fixture := loadBlastReference(t)
	ctx := context.Background()
	store := memory.New()
	for _, resource := range fixture.Resources {
		if err := store.UpsertResource(ctx, resource.Resource); err != nil {
			t.Fatal(err)
		}
	}
	for _, edge := range fixture.Edges {
		if err := store.UpsertEdge(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range fixture.Queries {
		t.Run(query.Name, func(t *testing.T) {
			got, err := analysis.BlastRadius(ctx, store, query.Root, analysis.Options{MaxDepth: query.Depth})
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(got))
			for _, resource := range got {
				ids = append(ids, resource.ID())
			}
			slices.Sort(ids)
			want := slices.Clone(query.Incoming)
			slices.Sort(want)
			if !slices.Equal(ids, want) {
				t.Fatalf("legacy incoming traversal: got %v, want %v", ids, want)
			}
		})
	}
}

func TestBlastRadius_ReferenceOwnershipUsesExtractorDirection(t *testing.T) {
	fixture := loadBlastReference(t)
	for _, resource := range fixture.Resources {
		t.Run(resource.ID, func(t *testing.T) {
			got, err := (extractor.OwnsExtractor{}).Extract(context.Background(), resource.Resource, nil)
			if err != nil {
				t.Fatal(err)
			}
			var want []graph.Edge
			for _, edge := range fixture.Edges {
				if edge.From == resource.ID && edge.Type == graph.EdgeTypeOwns {
					want = append(want, edge)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("extractor produced %d owner edges, fixture has %d", len(got), len(want))
			}
			for i := range got {
				if got[i].From != want[i].From || got[i].To != want[i].To || got[i].Type != want[i].Type {
					t.Fatalf("fixture ownership differs from real extractor: got %v, want %v", got, want)
				}
			}
		})
	}
}
