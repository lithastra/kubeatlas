// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// policySeed stages one Gatekeeper Constraint that enforces two
// Namespaces — one violating, one compliant.
func policySeed(s graph.GraphStore) {
	ctx := context.Background()
	constraint := graph.Resource{
		Kind:         "K8sRequiredLabels",
		Name:         "all",
		GroupVersion: "constraints.gatekeeper.sh/v1beta1",
		Raw: map[string]any{
			"status": map[string]any{"violations": []any{
				map[string]any{"kind": "Namespace", "name": "foo", "message": "missing label app"},
			}},
		},
	}
	foo := graph.Resource{Kind: "Namespace", Name: "foo"}
	bar := graph.Resource{Kind: "Namespace", Name: "bar"}
	for _, r := range []graph.Resource{constraint, foo, bar} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{
		From: constraint.ID(), To: foo.ID(), Type: graph.EdgeTypeEnforces,
		Attributes: map[string]string{"violated": "true", "violation_message": "missing label app"},
	})
	_ = s.UpsertEdge(ctx, graph.Edge{From: constraint.ID(), To: bar.ID(), Type: graph.EdgeTypeEnforces})
}

func TestHandlePolicyConstraints(t *testing.T) {
	base, _, cleanup := seedAndServe(t, policySeed)
	defer cleanup()

	var got []struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Engine     string `json:"engine"`
		Violations int    `json:"violations"`
	}
	resp, body := getJSON(t, base+"/api/v1/policy/constraints", &got)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if len(got) != 1 {
		t.Fatalf("constraints len = %d, want 1 (%s)", len(got), body)
	}
	c := got[0]
	if c.Name != "all" || c.Kind != "K8sRequiredLabels" || c.Engine != "gatekeeper" || c.Violations != 1 {
		t.Errorf("constraint = %+v, want all/K8sRequiredLabels/gatekeeper/1", c)
	}
}

func TestHandlePolicyConstraints_UnknownEngine(t *testing.T) {
	base, _, cleanup := seedAndServe(t, policySeed)
	defer cleanup()
	resp, _ := getJSON(t, base+"/api/v1/policy/constraints?engine=bogus", nil)
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for unknown engine", resp.StatusCode)
	}
}

func TestHandlePolicyConstraintAffected(t *testing.T) {
	base, _, cleanup := seedAndServe(t, policySeed)
	defer cleanup()

	var got struct {
		Constraint string `json:"constraint"`
		Count      int    `json:"count"`
		Resources  []struct {
			Resource graph.Resource `json:"resource"`
			Violated bool           `json:"violated"`
			Message  string         `json:"message"`
		} `json:"resources"`
	}
	resp, body := getJSON(t, base+"/api/v1/policy/constraints/all/affected", &got)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if got.Constraint != "all" || got.Count != 2 {
		t.Fatalf("affected = constraint=%q count=%d, want all/2 (%s)", got.Constraint, got.Count, body)
	}
	var violated int
	for _, r := range got.Resources {
		if r.Violated {
			violated++
			if r.Resource.Name != "foo" || r.Message == "" {
				t.Errorf("violating resource = %+v, want foo with a message", r)
			}
		}
	}
	if violated != 1 {
		t.Errorf("violated count = %d, want 1", violated)
	}
}

func TestHandlePolicyConstraintAffected_NotFound(t *testing.T) {
	base, _, cleanup := seedAndServe(t, policySeed)
	defer cleanup()
	resp, _ := getJSON(t, base+"/api/v1/policy/constraints/nope/affected", nil)
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
