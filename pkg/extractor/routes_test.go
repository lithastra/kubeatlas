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

func TestRoutes_IngressMultiPathDedupOnSameBackend(t *testing.T) {
	// An Ingress with multiple rules and paths pointing at the same
	// Service must collapse to a single edge (dedup happens inside the
	// extractor; the graph store also dedups, but doing it here keeps
	// the per-resource snapshot honest).
	ing := graph.Resource{
		Kind: "Ingress", Namespace: "demo", Name: "web",
		Raw: map[string]any{
			"spec": map[string]any{
				"rules": []any{
					map[string]any{
						"host": "a.local",
						"http": map[string]any{
							"paths": []any{
								map[string]any{"backend": map[string]any{"service": map[string]any{"name": "web-svc"}}},
								map[string]any{"backend": map[string]any{"service": map[string]any{"name": "api-svc"}}},
							},
						},
					},
					map[string]any{
						"host": "b.local",
						"http": map[string]any{
							"paths": []any{
								// Repeated reference to web-svc — must dedup.
								map[string]any{"backend": map[string]any{"service": map[string]any{"name": "web-svc"}}},
							},
						},
					},
				},
			},
		},
	}
	got := (RoutesExtractor{}).Extract(ing, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 edges (web-svc + api-svc, deduped), got %d: %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.To] = true
	}
	for _, want := range []string{"demo/Service/web-svc", "demo/Service/api-svc"} {
		if !seen[want] {
			t.Errorf("missing edge to %q", want)
		}
	}
}

func TestRoutes_HTTPRouteSameNamespaceFallback(t *testing.T) {
	// backendRef without explicit namespace falls back to the route's
	// own namespace.
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "demo", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"rules": []any{
					map[string]any{
						"backendRefs": []any{
							map[string]any{"name": "web-svc"},
						},
					},
				},
			},
		},
	}
	got := (RoutesExtractor{}).Extract(rt, nil)
	if len(got) != 1 || got[0].To != "demo/Service/web-svc" {
		t.Errorf("expected fallback to route's namespace, got %v", got)
	}
}

func TestRoutes_HTTPRouteBackendKindOverride(t *testing.T) {
	// backendRef.kind is honoured when present (e.g. ServiceImport for
	// MCS); only when omitted does it default to Service.
	rt := graph.Resource{
		Kind: "HTTPRoute", Namespace: "demo", Name: "rt",
		Raw: map[string]any{
			"spec": map[string]any{
				"rules": []any{
					map[string]any{
						"backendRefs": []any{
							map[string]any{"name": "imported", "kind": "ServiceImport"},
						},
					},
				},
			},
		},
	}
	got := (RoutesExtractor{}).Extract(rt, nil)
	if len(got) != 1 || got[0].To != "demo/ServiceImport/imported" {
		t.Errorf("expected edge to ServiceImport, got %v", got)
	}
}

func TestRoutes_NonRoutingKindEmitsNothing(t *testing.T) {
	// Pod / Service / etc. do not produce ROUTES_TO edges.
	for _, kind := range []string{"Pod", "Service", "Deployment", "ConfigMap"} {
		r := graph.Resource{Kind: kind, Namespace: "demo", Name: "x"}
		if got := (RoutesExtractor{}).Extract(r, nil); got != nil {
			t.Errorf("kind=%s: expected nil edges, got %v", kind, got)
		}
	}
}
