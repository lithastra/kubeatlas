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

func TestAttached_ExplicitParentNamespace(t *testing.T) {
	// parentRef.namespace overrides the route's own namespace, allowing
	// a route to attach to a Gateway in a shared infra namespace.
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "team-a", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"parentRefs": []any{
					map[string]any{"name": "shared-gw", "namespace": "infra"},
				},
			},
		},
	}
	got := (AttachedExtractor{}).Extract(rt, nil)
	if len(got) != 1 || got[0].To != "infra/Gateway/shared-gw" {
		t.Errorf("expected edge to infra/Gateway/shared-gw, got %v", got)
	}
}

func TestAttached_MultipleParentsEmitMultipleEdges(t *testing.T) {
	// A route can attach to several gateways; each yields one edge.
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "demo", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"parentRefs": []any{
					map[string]any{"name": "gw-a"},
					map[string]any{"name": "gw-b"},
				},
			},
		},
	}
	got := (AttachedExtractor{}).Extract(rt, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 edges, got %d: %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.To] = true
	}
	for _, want := range []string{"demo/Gateway/gw-a", "demo/Gateway/gw-b"} {
		if !seen[want] {
			t.Errorf("missing edge to %q", want)
		}
	}
}

func TestAttached_DuplicateParentRefDedup(t *testing.T) {
	// The same parent listed twice must collapse to a single edge.
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "demo", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"parentRefs": []any{
					map[string]any{"name": "gw"},
					map[string]any{"name": "gw"},
				},
			},
		},
	}
	got := (AttachedExtractor{}).Extract(rt, nil)
	if len(got) != 1 {
		t.Errorf("expected 1 deduplicated edge, got %d: %v", len(got), got)
	}
}

func TestAttached_NotHTTPRouteEmitsNothing(t *testing.T) {
	// Only HTTPRoute drives ATTACHED_TO; Ingress/Service must be ignored
	// even if they happen to carry a parentRefs-shaped field.
	for _, kind := range []string{"Ingress", "Service", "Gateway"} {
		r := graph.Resource{
			Kind: kind, Namespace: "demo", Name: "x",
			Raw: map[string]any{"spec": map[string]any{
				"parentRefs": []any{map[string]any{"name": "gw"}},
			}},
		}
		if got := (AttachedExtractor{}).Extract(r, nil); got != nil {
			t.Errorf("kind=%s: expected nil edges, got %v", kind, got)
		}
	}
}
