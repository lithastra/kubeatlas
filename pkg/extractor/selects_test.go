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
