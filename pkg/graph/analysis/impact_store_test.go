// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type impactRestrictedStore struct {
	graph.GraphStore // Deliberately exposes only the original method set.
}

func TestAnalyzeStoredImpact_NoFallbackThroughVisibilityWrapper(t *testing.T) {
	s := &impactRestrictedStore{GraphStore: memory.New()}
	result, err := analysis.AnalyzeStoredImpact(context.Background(), s, "demo/Pod/root", analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{})
	if result != nil || !errors.Is(err, analysis.ErrImpactSnapshotUnsupported) {
		t.Fatalf("restricted wrapper bypassed: %+v %v", result, err)
	}
}

type impactSnapshotProbe struct {
	graph.GraphStore
	read func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error)
}

func (s *impactSnapshotProbe) SnapshotImpact(ctx context.Context, opts graph.ImpactSnapshotOptions) (*graph.Graph, error) {
	return s.read(ctx, opts)
}

func TestAnalyzeStoredImpact_InvalidOptionsBeforeRead(t *testing.T) {
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		t.Fatal("invalid request reached store")
		return nil, nil
	}}
	for _, tc := range []struct {
		root   string
		opts   analysis.ImpactOptions
		limits graph.ImpactSnapshotOptions
		want   error
	}{
		{"", analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ErrImpactInvalidOptions},
		{"demo/Pod/root", analysis.ImpactOptions{MaxDepth: -1}, graph.ImpactSnapshotOptions{}, analysis.ErrImpactInvalidOptions},
		{"demo/Pod/root", analysis.ImpactOptions{ClusterID: "prod"}, graph.ImpactSnapshotOptions{ClusterID: "other"}, analysis.ErrImpactInvalidOptions},
		{"demo/Pod/root", analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{MaxBytes: -1}, graph.ErrImpactSnapshotOptions},
	} {
		result, err := analysis.AnalyzeStoredImpact(context.Background(), probe, tc.root, tc.opts, tc.limits)
		if result != nil || !errors.Is(err, tc.want) {
			t.Fatalf("invalid request: %+v %v, want %v", result, err, tc.want)
		}
	}
}

func TestAnalyzeStoredImpact_SharedBudgetAndCancellation(t *testing.T) {
	root := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "root"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe := &impactSnapshotProbe{read: func(readCtx context.Context, _ graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		deadline, ok := readCtx.Deadline()
		if !ok || time.Until(deadline) > graph.ImpactReadTimeout {
			t.Fatal("projection read has no five-second total budget")
		}
		cancel() // Cancellation after the read must still prevent analysis.
		return &graph.Graph{Resources: []graph.Resource{root}}, nil
	}}
	result, err := analysis.AnalyzeStoredImpact(ctx, probe, root.ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel after projection became success: %+v %v", result, err)
	}
	parent, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	probe.read = func(readCtx context.Context, _ graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		parentDeadline, _ := parent.Deadline()
		readDeadline, _ := readCtx.Deadline()
		if !readDeadline.Equal(parentDeadline) {
			t.Fatal("adapter extended caller's shorter deadline")
		}
		<-readCtx.Done()
		return nil, readCtx.Err()
	}
	result, err = analysis.AnalyzeStoredImpact(parent, probe, root.ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{})
	if result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline became successful empty result: %+v %v", result, err)
	}
}
