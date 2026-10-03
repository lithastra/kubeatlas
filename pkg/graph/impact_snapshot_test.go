// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestImpactProjection_OptionsAndClusterIdentity(t *testing.T) {
	opts, err := (graph.ImpactSnapshotOptions{}).Normalize()
	if err != nil || opts.MaxResources != 20_000 || opts.MaxEdges != 100_000 || opts.MaxBytes != 16<<20 {
		t.Fatalf("default budgets=%+v error=%v", opts, err)
	}
	for _, invalid := range []graph.ImpactSnapshotOptions{
		{MaxResources: -1}, {MaxResources: graph.MaxImpactResources + 1},
		{MaxEdges: -1}, {MaxEdges: graph.MaxImpactEdges + 1},
		{MaxBytes: -1}, {MaxBytes: graph.MaxImpactBytes + 1}, {ClusterID: "a:b"}, {ClusterID: "a/b"},
	} {
		if _, err := invalid.Normalize(); !errors.Is(err, graph.ErrImpactSnapshotOptions) {
			t.Fatalf("invalid budgets accepted: %+v", invalid)
		}
	}
	for _, tc := range []struct {
		id, cluster string
		want        bool
	}{
		{"demo/Pod/a", "", true}, {"/Namespace/demo", "", true}, {"prod:demo/Pod/a", "prod", true},
		{"prod:demo/Pod/a", "", false}, {"demo/Pod/a", "prod", false}, {":demo/Pod/a", "", false},
		{"team_%:demo/Pod/a", "team_%", true}, {"other:demo/Pod/a", "prod", false},
		{"/ClusterRole/system:node", "", true}, {"prod:/ClusterRole/system:node", "prod", true},
		{"/ClusterRole/system:node", "system", false}, {"prod:/ClusterRole/system:node", "", false},
		{"/User/system:serviceaccount:demo:worker", "", true}, {"not-an-id", "", false},
	} {
		if graph.ImpactIDInCluster(tc.id, tc.cluster) != tc.want {
			t.Fatalf("identity %q cluster %q scope mismatch", tc.id, tc.cluster)
		}
	}
}

func TestImpactProjection_ExactByteBudgetAndEscapeExpansion(t *testing.T) {
	ctx := context.Background()
	r := graph.Resource{Kind: "Pod", Name: "x", Namespace: "ns"}
	budget := 512 + 6*(3+1+2)
	builder, err := graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{MaxBytes: budget})
	if err != nil || builder.AddResource(ctx, r) != nil {
		t.Fatal("exact conservative budget rejected")
	}
	encoded, err := json.Marshal(builder.Graph.Resources[0])
	if err != nil || len(encoded) > budget {
		t.Fatalf("record encoding exceeded charged budget: %d %v", len(encoded), err)
	}
	builder, _ = graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{MaxBytes: budget - 1})
	if err := builder.AddResource(ctx, r); !errors.Is(err, graph.ErrImpactSnapshotLimit) || len(builder.Graph.Resources) != 0 {
		t.Fatal("byte budget admitted a partial resource")
	}
	r.ResourceVersion = strings.Repeat("\x00", 1000)
	builder, _ = graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{MaxBytes: 2048})
	if err := builder.AddResource(ctx, r); !errors.Is(err, graph.ErrImpactSnapshotLimit) {
		t.Fatal("escape expansion bypassed budget")
	}
	owner := graph.OwnerRef{Kind: "Deployment", Name: "api", UID: "owner"}
	r.ResourceVersion = ""
	r.OwnerReferences = []graph.OwnerRef{owner}
	builder, _ = graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{MaxBytes: budget})
	if err := builder.AddResource(ctx, r); !errors.Is(err, graph.ErrImpactSnapshotLimit) {
		t.Fatal("owner evidence was not charged")
	}
	edge := graph.Edge{From: "demo/Pod/a", To: "demo/Pod/b", Type: graph.EdgeTypeOwns}
	budget = 96 + 6*(len(edge.From)+len(edge.To)+len(edge.Type))
	builder, _ = graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{MaxBytes: budget})
	if err := builder.AddEdge(ctx, edge); err != nil {
		t.Fatal("exact edge budget rejected")
	}
	builder, _ = graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{MaxBytes: budget - 1})
	if err := builder.AddEdge(ctx, edge); !errors.Is(err, graph.ErrImpactSnapshotLimit) {
		t.Fatal("edge metadata bypassed budget")
	}
}

func TestImpactProjection_IgnoresPayloadAndHonorsCancellation(t *testing.T) {
	ctx := context.Background()
	builder, _ := graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{MaxBytes: 1024})
	r := graph.Resource{Kind: "Secret", Name: "test", Namespace: "demo", UID: "synthetic-canary", ResourceVersion: strings.Repeat("x", 8192), Raw: map[string]any{"synthetic": strings.Repeat("x", 8192)}}
	if err := builder.AddResource(ctx, r); err != nil {
		t.Fatal("Secret extras were charged instead of excluded")
	}
	if builder.Graph.Resources[0].UID != "" || builder.Graph.Resources[0].Raw != nil {
		t.Fatal("Secret extras retained")
	}
	r.ClusterID = "other"
	if err := builder.AddResource(ctx, r); err != nil || len(builder.Graph.Resources) != 1 {
		t.Fatal("foreign resource consumed selected cluster budget")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := builder.AddResource(cancelled, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("resource cancellation=%v", err)
	}
	if err := builder.AddEdge(cancelled, graph.Edge{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("edge cancellation=%v", err)
	}
}
