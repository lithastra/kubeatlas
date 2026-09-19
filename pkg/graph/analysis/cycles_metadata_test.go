// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type cycleMetadataStore struct {
	graph.GraphStore
	err error
}

func (s cycleMetadataStore) Snapshot(context.Context) (*graph.Graph, error) {
	return nil, errors.New("full payload snapshot must not be used")
}

func (s cycleMetadataStore) SnapshotMetadata(ctx context.Context) (*graph.Graph, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.GraphStore.(graph.MetadataSnapshotter).SnapshotMetadata(ctx)
}

type hiddenCycleStore struct{ graph.GraphStore }

func (s hiddenCycleStore) Snapshot(context.Context) (*graph.Graph, error) {
	return &graph.Graph{Resources: []graph.Resource{}, Edges: []graph.Edge{}}, nil
}

func TestCycleMetadataPreservesCrossNamespaceReport(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	a := graph.Resource{Kind: "ConfigMap", Namespace: "a", Name: "source", ClusterID: "east",
		Annotations: map[string]string{"kubeatlas.io/intentional-cycle": "true"},
		Raw:         map[string]any{"synthetic": "not serialized"}}
	b := graph.Resource{Kind: "ConfigMap", Namespace: "b", Name: "target", ClusterID: "east"}
	for _, r := range []graph.Resource{a, b} {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []graph.Edge{
		{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeUsesConfigMap},
		{From: b.ID(), To: a.ID(), Type: graph.EdgeTypeUsesConfigMap},
	} {
		if err := s.UpsertEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	optimized, err := analysis.DetectCycles(ctx, cycleMetadataStore{GraphStore: s})
	if err != nil {
		t.Fatal(err)
	}
	// Embedding only GraphStore hides the optional capability and exercises
	// the compatibility path used by legacy or visibility-filtering wrappers.
	legacy, err := analysis.DetectCycles(ctx, struct{ graph.GraphStore }{s})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(optimized)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) || len(optimized) != 1 || optimized[0].Category != analysis.CycleCategoryIntentional {
		t.Fatalf("optimized=%s legacy=%s", got, want)
	}
	for _, member := range optimized[0].Members {
		if member.Raw != nil {
			t.Fatal("cycle snapshot retained Raw")
		}
	}
	if legacy[0].Members[0].Raw == nil {
		t.Fatal("legacy snapshot should retain its private Raw")
	}
	hidden, err := analysis.DetectCycles(ctx, hiddenCycleStore{GraphStore: s})
	if err != nil || len(hidden) != 0 {
		t.Fatalf("wrapper visibility bypassed: result=%+v error=%v", hidden, err)
	}
	wantErr := errors.New("metadata read denied")
	_, err = analysis.DetectCycles(ctx, cycleMetadataStore{GraphStore: s, err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("metadata error changed or bypassed: %v", err)
	}
}
