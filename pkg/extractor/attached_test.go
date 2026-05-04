package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestAttached_HTTPRouteToGateway(t *testing.T) {
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "demo", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"parentRefs": []any{
					map[string]any{"name": "gw"},
				},
			},
		},
	}
	got := (AttachedExtractor{}).Extract(rt, nil)
	if len(got) != 1 || got[0].To != "demo/Gateway/gw" {
		t.Errorf("expected edge to demo/Gateway/gw, got %v", got)
	}
	if got[0].Type != graph.EdgeTypeAttachedTo {
		t.Errorf("wrong type: %v", got[0].Type)
	}
}

func TestAttached_NonGatewayParentSkipped(t *testing.T) {
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "demo", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"parentRefs": []any{
					map[string]any{"name": "svc", "kind": "Service"},
				},
			},
		},
	}
	if got := (AttachedExtractor{}).Extract(rt, nil); got != nil {
		t.Errorf("non-Gateway parent should be skipped, got %v", got)
	}
}
