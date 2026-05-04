package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestServiceAccount_ExplicitNameEmitsEdge(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"serviceAccountName": "api-sa",
					},
				},
			},
		},
	}
	got := (ServiceAccountExtractor{}).Extract(dep, nil)
	if len(got) != 1 || got[0].To != "demo/ServiceAccount/api-sa" {
		t.Errorf("expected single edge to demo/ServiceAccount/api-sa, got %v", got)
	}
}

func TestServiceAccount_MissingFieldImpliesDefault(t *testing.T) {
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "p",
		Raw: map[string]any{"spec": map[string]any{}},
	}
	got := (ServiceAccountExtractor{}).Extract(pod, nil)
	if len(got) != 1 || got[0].To != "demo/ServiceAccount/default" {
		t.Errorf("expected implicit default SA edge, got %v", got)
	}
	if got[0].Type != graph.EdgeTypeUsesServiceAccount {
		t.Errorf("wrong type: %v", got[0].Type)
	}
}
