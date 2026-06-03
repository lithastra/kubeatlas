// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// kyvernoSeed stages a Kyverno ClusterPolicy enforcing one Deployment,
// with a failing PolicyReport result stamped on the edge.
func kyvernoSeed(s graph.GraphStore) {
	ctx := context.Background()
	policy := graph.Resource{
		Kind:         "ClusterPolicy",
		Name:         "require-labels",
		GroupVersion: "kyverno.io/v1",
	}
	web := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"}
	for _, r := range []graph.Resource{policy, web} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{
		From: policy.ID(), To: web.ID(), Type: graph.EdgeTypeEnforces,
		Attributes: map[string]string{"result": "fail"},
	})
}

func TestHandlePolicyConstraints_Kyverno(t *testing.T) {
	base, _, cleanup := seedAndServe(t, kyvernoSeed)
	defer cleanup()

	var got []struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Engine     string `json:"engine"`
		Violations int    `json:"violations"`
	}
	resp, body := getJSON(t, base+"/api/v1/policy/constraints?engine=kyverno", &got)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (%s)", resp.StatusCode, body)
	}
	if len(got) != 1 {
		t.Fatalf("kyverno constraints len = %d, want 1 (%s)", len(got), body)
	}
	c := got[0]
	if c.Name != "require-labels" || c.Engine != "kyverno" || c.Kind != "ClusterPolicy" || c.Violations != 1 {
		t.Errorf("kyverno constraint = %+v, want require-labels/kyverno/ClusterPolicy/1", c)
	}
}

func TestHandlePolicyConstraintAffected_Kyverno(t *testing.T) {
	base, _, cleanup := seedAndServe(t, kyvernoSeed)
	defer cleanup()

	var got struct {
		Constraint string `json:"constraint"`
		Count      int    `json:"count"`
		Resources  []struct {
			Resource graph.Resource `json:"resource"`
			Violated bool           `json:"violated"`
		} `json:"resources"`
	}
	resp, body := getJSON(t, base+"/api/v1/policy/constraints/require-labels/affected", &got)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (%s)", resp.StatusCode, body)
	}
	if got.Count != 1 || len(got.Resources) != 1 {
		t.Fatalf("affected count = %d, want 1 (%s)", got.Count, body)
	}
	if !got.Resources[0].Violated || got.Resources[0].Resource.Name != "web" {
		t.Errorf("affected = %+v, want web violated", got.Resources[0])
	}
}
