package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestRoutes_IngressBackend(t *testing.T) {
	ing := graph.Resource{
		Kind: "Ingress", Namespace: "demo", Name: "web",
		Raw: map[string]any{
			"spec": map[string]any{
				"rules": []any{
					map[string]any{
						"http": map[string]any{
							"paths": []any{
								map[string]any{
									"backend": map[string]any{
										"service": map[string]any{"name": "web-svc"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	got := (RoutesExtractor{}).Extract(ing, nil)
	if len(got) != 1 || got[0].To != "demo/Service/web-svc" {
		t.Errorf("expected edge to demo/Service/web-svc, got %v", got)
	}
}

func TestRoutes_HTTPRouteBackendInExplicitNamespace(t *testing.T) {
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "demo", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"rules": []any{
					map[string]any{
						"backendRefs": []any{
							map[string]any{
								"name":      "web-svc",
								"namespace": "other",
							},
						},
					},
				},
			},
		},
	}
	got := (RoutesExtractor{}).Extract(rt, nil)
	if len(got) != 1 || got[0].To != "other/Service/web-svc" {
		t.Errorf("expected edge to other/Service/web-svc, got %v", got)
	}
}
