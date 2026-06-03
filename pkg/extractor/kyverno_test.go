// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func kyvernoPolicy(kind, name, namespace string, rules []any) graph.Resource {
	return graph.Resource{
		Kind:         kind,
		Name:         name,
		Namespace:    namespace,
		GroupVersion: "kyverno.io/v1",
		Raw:          map[string]any{"spec": map[string]any{"rules": rules}},
	}
}

func validateRule(kinds, namespaces []any) map[string]any {
	return map[string]any{
		"name":     "require-labels",
		"validate": map[string]any{"message": "label app required"},
		"match": map[string]any{
			"any": []any{
				map[string]any{"resources": map[string]any{"kinds": kinds, "namespaces": namespaces}},
			},
		},
	}
}

func policyReport(namespace string, results []any) graph.Resource {
	return graph.Resource{
		Kind:         "PolicyReport",
		Name:         "polr-1",
		Namespace:    namespace,
		GroupVersion: "wgpolicyk8s.io/v1alpha2",
		Raw:          map[string]any{"results": results},
	}
}

func reportResult(policy, result string, resources ...map[string]any) map[string]any {
	refs := make([]any, len(resources))
	for i, r := range resources {
		refs[i] = r
	}
	return map[string]any{"policy": policy, "result": result, "resources": refs}
}

func TestKyverno_MatchesAndOverlaysReport(t *testing.T) {
	pol := kyvernoPolicy("ClusterPolicy", "require-labels", "",
		[]any{validateRule([]any{"Deployment"}, []any{"demo"})})
	report := policyReport("demo", []any{
		reportResult("require-labels", "fail", map[string]any{"kind": "Deployment", "namespace": "demo", "name": "web"}),
		reportResult("require-labels", "pass", map[string]any{"kind": "Deployment", "namespace": "demo", "name": "api"}),
	})
	all := []graph.Resource{
		{Kind: "Deployment", Namespace: "demo", Name: "web"},
		{Kind: "Deployment", Namespace: "demo", Name: "api"},
		report,
	}

	got := extractEdges(t, KyvernoExtractor{}, pol, all)
	if len(got) != 2 {
		t.Fatalf("got %d edges, want 2 (%+v)", len(got), got)
	}
	by := edgesByTo(got)

	web := by["demo/Deployment/web"]
	if web.From != "/ClusterPolicy/require-labels" || web.Type != graph.EdgeTypeEnforces {
		t.Errorf("web edge = %+v, want From=/ClusterPolicy/require-labels type=ENFORCES", web)
	}
	if web.Attributes["result"] != "fail" {
		t.Errorf("web result = %q, want fail", web.Attributes["result"])
	}
	if by["demo/Deployment/api"].Attributes["result"] != "pass" {
		t.Errorf("api result = %q, want pass", by["demo/Deployment/api"].Attributes["result"])
	}
}

func TestKyverno_NoReportLeavesEdgesPlain(t *testing.T) {
	pol := kyvernoPolicy("ClusterPolicy", "require-labels", "",
		[]any{validateRule([]any{"Deployment"}, []any{"demo"})})
	all := []graph.Resource{{Kind: "Deployment", Namespace: "demo", Name: "web"}}

	got := extractEdges(t, KyvernoExtractor{}, pol, all)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if len(got[0].Attributes) != 0 {
		t.Errorf("edge should be plain without a report, got %v", got[0].Attributes)
	}
}

func TestKyverno_SkipsNonValidateRules(t *testing.T) {
	mutateRule := map[string]any{
		"name":   "add-label",
		"mutate": map[string]any{"patchStrategicMerge": map[string]any{}},
		"match":  map[string]any{"any": []any{map[string]any{"resources": map[string]any{"kinds": []any{"Deployment"}}}}},
	}
	pol := kyvernoPolicy("ClusterPolicy", "mutator", "", []any{mutateRule})
	all := []graph.Resource{{Kind: "Deployment", Namespace: "demo", Name: "web"}}
	if got := extractEdges(t, KyvernoExtractor{}, pol, all); len(got) != 0 {
		t.Errorf("mutate-only policy produced %d edges, want 0", len(got))
	}
}

func TestKyverno_NamespacedPolicyScopedToOwnNamespace(t *testing.T) {
	pol := kyvernoPolicy("Policy", "ns-rule", "demo",
		[]any{validateRule([]any{"Deployment"}, []any{"other"})}) // descriptor says other; must be ignored
	all := []graph.Resource{
		{Kind: "Deployment", Namespace: "demo", Name: "in"},
		{Kind: "Deployment", Namespace: "other", Name: "out"},
	}
	got := extractEdges(t, KyvernoExtractor{}, pol, all)
	if len(got) != 1 || got[0].To != "demo/Deployment/in" {
		t.Errorf("namespaced policy match = %+v, want exactly demo/Deployment/in", got)
	}
}

func TestKyverno_IgnoresNonPolicy(t *testing.T) {
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web", GroupVersion: "apps/v1"}
	if got := extractEdges(t, KyvernoExtractor{}, dep, nil); len(got) != 0 {
		t.Errorf("non-policy produced %d edges, want 0", len(got))
	}
	// A Kyverno CRD that is not a policy kind is ignored too.
	other := graph.Resource{Kind: "PolicyException", GroupVersion: "kyverno.io/v1"}
	if got := extractEdges(t, KyvernoExtractor{}, other, nil); len(got) != 0 {
		t.Errorf("non-policy kyverno kind produced %d edges, want 0", len(got))
	}
}
