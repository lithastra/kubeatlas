// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// seedNetworkPolicyFixture builds a NetworkPolicy plus the Pods /
// Namespaces it references, and the SELECTS_NP / ALLOWS_FROM /
// ALLOWS_TO edges the F-109 extractor would have persisted. The
// handler reads these edges back — the extractor itself is unit-
// tested separately in pkg/extractor.
func seedNetworkPolicyFixture(s graph.GraphStore) {
	ctx := context.Background()
	np := graph.Resource{Kind: "NetworkPolicy", Namespace: "petclinic", Name: "api-policy"}
	webPod := graph.Resource{Kind: "Pod", Namespace: "petclinic", Name: "web-1"}
	apiPod := graph.Resource{Kind: "Pod", Namespace: "petclinic", Name: "api-1"}
	frontendPod := graph.Resource{Kind: "Pod", Namespace: "petclinic", Name: "frontend-1"}
	dbPod := graph.Resource{Kind: "Pod", Namespace: "petclinic", Name: "db-1"}
	trustedNS := graph.Resource{Kind: "Namespace", Namespace: "", Name: "monitoring"}
	for _, r := range []graph.Resource{np, webPod, apiPod, frontendPod, dbPod, trustedNS} {
		_ = s.UpsertResource(ctx, r)
	}
	npID := np.ID()
	// SELECTS_NP: the policy governs web-1 and api-1.
	_ = s.UpsertEdge(ctx, graph.Edge{From: npID, To: webPod.ID(), Type: graph.EdgeTypeSelectsNP})
	_ = s.UpsertEdge(ctx, graph.Edge{From: npID, To: apiPod.ID(), Type: graph.EdgeTypeSelectsNP})
	// ALLOWS_FROM: ingress permitted from frontend-1 and the
	// monitoring namespace.
	_ = s.UpsertEdge(ctx, graph.Edge{From: npID, To: frontendPod.ID(), Type: graph.EdgeTypeAllowsFrom})
	_ = s.UpsertEdge(ctx, graph.Edge{From: npID, To: trustedNS.ID(), Type: graph.EdgeTypeAllowsFrom})
	// ALLOWS_TO: egress permitted to db-1.
	_ = s.UpsertEdge(ctx, graph.Edge{From: npID, To: dbPod.ID(), Type: graph.EdgeTypeAllowsTo})
	// A dangling SELECTS_NP edge — target Pod was deleted after the
	// policy was observed. The handler must skip it silently.
	_ = s.UpsertEdge(ctx, graph.Edge{From: npID, To: "petclinic/Pod/ghost", Type: graph.EdgeTypeSelectsNP})
}

func TestNetworkPolicy_Selected_HappyPath(t *testing.T) {
	base, _, stop := seedAndServe(t, seedNetworkPolicyFixture)
	defer stop()

	var resp api.NetworkPolicySelectedResponse
	r, _ := getJSON(t, base+"/api/v1/networkpolicy/petclinic/api-policy/selected", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if resp.NetworkPolicy.Name != "api-policy" {
		t.Errorf("networkPolicy = %q, want api-policy", resp.NetworkPolicy.Name)
	}
	// Two live SELECTS_NP targets; the dangling "ghost" edge is
	// skipped, so Count is 2 not 3.
	if resp.Count != 2 || len(resp.Selected) != 2 {
		t.Fatalf("Count=%d len=%d, want 2 (dangling edge must be skipped): %+v",
			resp.Count, len(resp.Selected), resp.Selected)
	}
	names := map[string]bool{}
	for _, p := range resp.Selected {
		names[p.Name] = true
	}
	if !names["web-1"] || !names["api-1"] {
		t.Errorf("selected = %v, want web-1 + api-1", names)
	}
}

func TestNetworkPolicy_Selected_NotFound(t *testing.T) {
	base, _, stop := seedAndServe(t, seedNetworkPolicyFixture)
	defer stop()

	r, _ := getJSON(t, base+"/api/v1/networkpolicy/petclinic/does-not-exist/selected", nil)
	if r.StatusCode != 404 {
		t.Errorf("status = %d, want 404 for missing NetworkPolicy", r.StatusCode)
	}
}

func TestNetworkPolicy_AllowGraph_HappyPath(t *testing.T) {
	base, _, stop := seedAndServe(t, seedNetworkPolicyFixture)
	defer stop()

	var resp api.NetworkPolicyAllowGraphResponse
	r, _ := getJSON(t, base+"/api/v1/networkpolicy/petclinic/api-policy/allow-graph", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	// ALLOWS_FROM: frontend-1 Pod + monitoring Namespace.
	if len(resp.AllowFrom) != 2 {
		t.Fatalf("allowFrom len = %d, want 2: %+v", len(resp.AllowFrom), resp.AllowFrom)
	}
	fromKinds := map[string]bool{}
	for _, res := range resp.AllowFrom {
		fromKinds[res.Kind] = true
	}
	if !fromKinds["Pod"] || !fromKinds["Namespace"] {
		t.Errorf("allowFrom kinds = %v, want Pod + Namespace", fromKinds)
	}
	// ALLOWS_TO: db-1 Pod only.
	if len(resp.AllowTo) != 1 || resp.AllowTo[0].Name != "db-1" {
		t.Errorf("allowTo = %+v, want [db-1]", resp.AllowTo)
	}
}

func TestNetworkPolicy_Selected_IgnoresOtherEdgeTypes(t *testing.T) {
	// A NetworkPolicy that also has a non-NetworkPolicy edge type
	// hanging off it (e.g. an OWNS edge from a contrived fixture)
	// must not have that edge leak into /selected.
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		np := graph.Resource{Kind: "NetworkPolicy", Namespace: "demo", Name: "np"}
		pod := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "real"}
		other := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cm"}
		for _, r := range []graph.Resource{np, pod, other} {
			_ = s.UpsertResource(ctx, r)
		}
		_ = s.UpsertEdge(ctx, graph.Edge{From: np.ID(), To: pod.ID(), Type: graph.EdgeTypeSelectsNP})
		// Wrong edge type — must be filtered out by the handler.
		_ = s.UpsertEdge(ctx, graph.Edge{From: np.ID(), To: other.ID(), Type: graph.EdgeTypeUsesConfigMap})
	})
	defer stop()

	var resp api.NetworkPolicySelectedResponse
	getJSON(t, base+"/api/v1/networkpolicy/demo/np/selected", &resp)
	if resp.Count != 1 || resp.Selected[0].Name != "real" {
		t.Errorf("selected = %+v, want only the SELECTS_NP target 'real'", resp.Selected)
	}
}

func TestNetworkPolicy_Selected_ServedOnV1Alpha1Too(t *testing.T) {
	// The route is declared v1alpha1-canonical and auto-mirrored to
	// /api/v1/ — the same dual-serving every Phase 2 endpoint uses.
	// Both paths must reach the handler.
	base, _, stop := seedAndServe(t, seedNetworkPolicyFixture)
	defer stop()

	var resp api.NetworkPolicySelectedResponse
	r, _ := getJSON(t, base+"/api/v1alpha1/networkpolicy/petclinic/api-policy/selected", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("v1alpha1 path status = %d, want 200", r.StatusCode)
	}
	if resp.Count != 2 {
		t.Errorf("v1alpha1 path Count = %d, want 2", resp.Count)
	}
}

func TestNetworkPolicy_AllowGraph_EmptyWhenNoEdges(t *testing.T) {
	// A NetworkPolicy with no ALLOWS_* edges yields empty (non-nil)
	// arrays so the JSON encodes [] not null.
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		_ = s.UpsertResource(context.Background(),
			graph.Resource{Kind: "NetworkPolicy", Namespace: "demo", Name: "isolated"})
	})
	defer stop()

	var resp api.NetworkPolicyAllowGraphResponse
	_, body := getJSON(t, base+"/api/v1/networkpolicy/demo/isolated/allow-graph", &resp)
	if resp.AllowFrom == nil || resp.AllowTo == nil {
		t.Errorf("allowFrom/allowTo must be non-nil empty arrays, body: %s", body)
	}
	if len(resp.AllowFrom) != 0 || len(resp.AllowTo) != 0 {
		t.Errorf("expected empty arrays, got %+v", resp)
	}
}
