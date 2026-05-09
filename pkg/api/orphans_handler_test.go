// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

func seedOrphansFixture(s graph.GraphStore) {
	ctx := context.Background()
	// Healthy chain: Deployment -> ReplicaSet -> Pod with edges.
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "api"}
	rs := graph.Resource{
		Kind: "ReplicaSet", Namespace: "demo", Name: "api-rs",
		UID:             types.UID("rs-uid"),
		OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "api"}},
	}
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "api-1",
		OwnerReferences: []graph.OwnerRef{{Kind: "ReplicaSet", Name: "api-rs", UID: types.UID("rs-uid")}},
	}
	// Orphan ReplicaSet: lost its Deployment, no incoming edges.
	orphanRS := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "ghost-rs"}
	// Standalone Pod: no OwnerReference.
	standalone := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "lonely"}

	for _, r := range []graph.Resource{dep, rs, pod, orphanRS, standalone} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: rs.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns})
	_ = s.UpsertEdge(ctx, graph.Edge{From: pod.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns})
}

func TestOrphans_Handler_FlagsBothCategories(t *testing.T) {
	base, _, stop := seedAndServe(t, seedOrphansFixture)
	defer stop()

	var resp api.OrphansResponse
	r, _ := getJSON(t, base+"/api/v1alpha1/orphans", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if resp.Count != len(resp.Reports) {
		t.Errorf("count %d != len(reports) %d", resp.Count, len(resp.Reports))
	}
	gotOrphan := false
	gotStandalone := false
	for _, rep := range resp.Reports {
		switch rep.Resource.Name {
		case "ghost-rs":
			if rep.Reason != "orphan" {
				t.Errorf("ghost-rs reason = %q, want orphan", rep.Reason)
			}
			gotOrphan = true
		case "lonely":
			if rep.Reason != "standalone_pod" {
				t.Errorf("lonely reason = %q, want standalone_pod", rep.Reason)
			}
			gotStandalone = true
		case "api-1":
			t.Errorf("owned Pod api-1 must not appear, got %+v", rep)
		}
	}
	if !gotOrphan {
		t.Errorf("expected orphan ghost-rs in reports, got %+v", resp.Reports)
	}
	if !gotStandalone {
		t.Errorf("expected standalone_pod lonely in reports, got %+v", resp.Reports)
	}
}

func TestOrphans_Handler_NamespaceFilter(t *testing.T) {
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "rs-demo"})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "ReplicaSet", Namespace: "other", Name: "rs-other"})
	})
	defer stop()

	var resp api.OrphansResponse
	getJSON(t, base+"/api/v1alpha1/orphans?namespace=demo", &resp)
	if resp.Count != 1 || resp.Reports[0].Resource.Name != "rs-demo" {
		t.Errorf("namespace filter not honoured, got %+v", resp.Reports)
	}
}

func TestOrphans_Handler_EmptyStore(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()

	var resp api.OrphansResponse
	getJSON(t, base+"/api/v1alpha1/orphans", &resp)
	if resp.Count != 0 {
		t.Errorf("empty store: got %d reports, want 0", resp.Count)
	}
	// Reports must serialize as [], not null, for client convenience.
	if resp.Reports == nil {
		t.Error("Reports must be non-nil empty slice on empty store")
	}
}
