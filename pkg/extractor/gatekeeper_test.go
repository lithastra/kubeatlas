// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// gkConstraint builds a Gatekeeper Constraint resource with the given
// match block and status.violations.
func gkConstraint(name string, match map[string]any, violations []any) graph.Resource {
	return graph.Resource{
		Kind:         "K8sRequiredLabels",
		Name:         name,
		GroupVersion: "constraints.gatekeeper.sh/v1beta1",
		Raw: map[string]any{
			"spec":   map[string]any{"match": match},
			"status": map[string]any{"violations": violations},
		},
	}
}

func edgesByTo(edges []graph.Edge) map[string]graph.Edge {
	m := make(map[string]graph.Edge, len(edges))
	for _, e := range edges {
		m[e.To] = e
	}
	return m
}

func TestGatekeeper_MatchesKindAndTagsViolations(t *testing.T) {
	c := gkConstraint("all",
		map[string]any{
			"kinds": []any{
				map[string]any{"apiGroups": []any{""}, "kinds": []any{"Namespace"}},
			},
		},
		[]any{
			map[string]any{"kind": "Namespace", "name": "foo", "namespace": "", "message": "missing label app"},
		},
	)
	all := []graph.Resource{
		{Kind: "Namespace", Name: "foo"},
		{Kind: "Namespace", Name: "bar"},
	}

	got := extractEdges(t, GatekeeperExtractor{}, c, all)
	if len(got) != 2 {
		t.Fatalf("got %d edges, want 2 (%+v)", len(got), got)
	}
	by := edgesByTo(got)

	foo, ok := by["/Namespace/foo"]
	if !ok {
		t.Fatal("missing ENFORCES edge to /Namespace/foo")
	}
	if foo.From != "/K8sRequiredLabels/all" || foo.Type != graph.EdgeTypeEnforces {
		t.Errorf("foo edge = %+v, want From=/K8sRequiredLabels/all type=ENFORCES", foo)
	}
	if foo.Attributes["violated"] != "true" || foo.Attributes["violation_message"] != "missing label app" {
		t.Errorf("foo edge attributes = %v, want violated+message", foo.Attributes)
	}

	bar, ok := by["/Namespace/bar"]
	if !ok {
		t.Fatal("missing ENFORCES edge to /Namespace/bar")
	}
	if len(bar.Attributes) != 0 {
		t.Errorf("bar (compliant) edge should carry no attributes, got %v", bar.Attributes)
	}
}

func TestGatekeeper_IgnoresNonConstraint(t *testing.T) {
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web", GroupVersion: "apps/v1"}
	if got := extractEdges(t, GatekeeperExtractor{}, dep, nil); len(got) != 0 {
		t.Errorf("non-constraint produced %d edges, want 0", len(got))
	}
}

func TestGatekeeper_NoMatchKinds(t *testing.T) {
	c := gkConstraint("empty", map[string]any{}, nil)
	if got := extractEdges(t, GatekeeperExtractor{}, c, []graph.Resource{{Kind: "Pod", Namespace: "demo", Name: "p"}}); len(got) != 0 {
		t.Errorf("constraint with no match.kinds produced %d edges, want 0", len(got))
	}
}

func TestGatekeeper_LabelSelector(t *testing.T) {
	c := gkConstraint("labeled",
		map[string]any{
			"kinds":         []any{map[string]any{"kinds": []any{"Pod"}}},
			"labelSelector": map[string]any{"matchLabels": map[string]any{"team": "a"}},
		}, nil)
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "p1", Labels: map[string]string{"team": "a"}},
		{Kind: "Pod", Namespace: "demo", Name: "p2", Labels: map[string]string{"team": "b"}},
	}
	got := extractEdges(t, GatekeeperExtractor{}, c, all)
	if len(got) != 1 || got[0].To != "demo/Pod/p1" {
		t.Errorf("label selector match = %+v, want exactly demo/Pod/p1", got)
	}
}

func TestGatekeeper_NamespaceScoped(t *testing.T) {
	c := gkConstraint("ns-scoped",
		map[string]any{
			"kinds":      []any{map[string]any{"kinds": []any{"Pod"}}},
			"namespaces": []any{"default"},
		}, nil)
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "default", Name: "in"},
		{Kind: "Pod", Namespace: "other", Name: "out"},
	}
	got := extractEdges(t, GatekeeperExtractor{}, c, all)
	if len(got) != 1 || got[0].To != "default/Pod/in" {
		t.Errorf("namespace-scoped match = %+v, want exactly default/Pod/in", got)
	}
}
