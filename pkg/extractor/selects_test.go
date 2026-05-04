package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestSelects_HappyPath(t *testing.T) {
	svc := graph.Resource{
		Kind: "Service", Namespace: "demo", Name: "web",
		Raw: map[string]any{
			"spec": map[string]any{
				"selector": map[string]any{"app": "web"},
			},
		},
	}
	pods := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "web-1", Labels: map[string]string{"app": "web"}},
		{Kind: "Pod", Namespace: "demo", Name: "api-1", Labels: map[string]string{"app": "api"}},
		{Kind: "Pod", Namespace: "other", Name: "web-x", Labels: map[string]string{"app": "web"}}, // wrong ns
	}
	got := (SelectsExtractor{}).Extract(svc, pods)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if got[0].To != "demo/Pod/web-1" {
		t.Errorf("To = %q, want demo/Pod/web-1", got[0].To)
	}
}

func TestSelects_EmptySelectorMatchesNothing(t *testing.T) {
	svc := graph.Resource{
		Kind: "Service", Namespace: "demo", Name: "headless",
		Raw: map[string]any{"spec": map[string]any{}},
	}
	pods := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "any", Labels: map[string]string{"app": "x"}},
	}
	if got := (SelectsExtractor{}).Extract(svc, pods); got != nil {
		t.Errorf("empty selector should match nothing, got %v", got)
	}
}

func TestSelects_NamespaceIsolation(t *testing.T) {
	// A Service in namespace A must not select a Pod in namespace B,
	// even if labels match. This is core K8s semantics — services are
	// namespace-scoped.
	svc := graph.Resource{
		Kind: "Service", Namespace: "team-a", Name: "web",
		Raw: map[string]any{"spec": map[string]any{"selector": map[string]any{"app": "web"}}},
	}
	pods := []graph.Resource{
		{Kind: "Pod", Namespace: "team-a", Name: "in-ns", Labels: map[string]string{"app": "web"}},
		{Kind: "Pod", Namespace: "team-b", Name: "out-ns", Labels: map[string]string{"app": "web"}},
	}
	got := (SelectsExtractor{}).Extract(svc, pods)
	if len(got) != 1 || got[0].To != "team-a/Pod/in-ns" {
		t.Errorf("expected single edge to in-namespace pod, got %v", got)
	}
}

func TestSelects_MultiLabelRequiresAllToMatch(t *testing.T) {
	// Selector with two labels matches only pods that carry BOTH; a pod
	// missing one of them is not selected.
	svc := graph.Resource{
		Kind: "Service", Namespace: "demo", Name: "tier-fe",
		Raw: map[string]any{
			"spec": map[string]any{
				"selector": map[string]any{"app": "web", "tier": "frontend"},
			},
		},
	}
	pods := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "full", Labels: map[string]string{"app": "web", "tier": "frontend"}},
		{Kind: "Pod", Namespace: "demo", Name: "partial", Labels: map[string]string{"app": "web"}},
	}
	got := (SelectsExtractor{}).Extract(svc, pods)
	if len(got) != 1 || got[0].To != "demo/Pod/full" {
		t.Errorf("expected single edge to fully-matching pod, got %v", got)
	}
}

func TestSelects_WorkloadTemplateLabelsAreMatched(t *testing.T) {
	// A Service can target a Deployment via the Deployment's pod-template
	// labels — the resulting edge points at the Deployment, not a Pod.
	svc := graph.Resource{
		Kind: "Service", Namespace: "demo", Name: "api-svc",
		Raw: map[string]any{"spec": map[string]any{"selector": map[string]any{"app": "api"}}},
	}
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{"app": "api"},
					},
				},
			},
		},
	}
	got := (SelectsExtractor{}).Extract(svc, []graph.Resource{dep})
	if len(got) != 1 || got[0].To != "demo/Deployment/api" {
		t.Errorf("expected edge to Deployment via template labels, got %v", got)
	}
}

func TestSelects_NonServiceEmitsNothing(t *testing.T) {
	// Only Service.spec.selector drives SELECTS edges. A Pod or
	// Deployment with a selector-shaped field must not produce edges.
	for _, kind := range []string{"Pod", "Deployment", "ConfigMap"} {
		r := graph.Resource{
			Kind: kind, Namespace: "demo", Name: "x",
			Raw: map[string]any{"spec": map[string]any{"selector": map[string]any{"app": "x"}}},
		}
		if got := (SelectsExtractor{}).Extract(r, nil); got != nil {
			t.Errorf("kind=%s: expected nil edges, got %v", kind, got)
		}
	}
}
