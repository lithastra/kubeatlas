package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestScales_DeploymentTarget(t *testing.T) {
	hpa := graph.Resource{
		Kind: "HorizontalPodAutoscaler", Namespace: "demo", Name: "podinfo",
		Raw: map[string]any{
			"spec": map[string]any{
				"scaleTargetRef": map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"name":       "podinfo",
				},
			},
		},
	}
	got := extractEdges(t, ScalesExtractor{}, hpa, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(got), got)
	}
	e := got[0]
	if e.From != "demo/HorizontalPodAutoscaler/podinfo" {
		t.Errorf("From = %q, want demo/HorizontalPodAutoscaler/podinfo", e.From)
	}
	if e.To != "demo/Deployment/podinfo" {
		t.Errorf("To = %q, want demo/Deployment/podinfo", e.To)
	}
	if e.Type != graph.EdgeTypeScales {
		t.Errorf("Type = %q, want SCALES", e.Type)
	}
}

func TestScales_StatefulSetTarget(t *testing.T) {
	// HPAs can target any /scale-bearing kind. The extractor honours
	// scaleTargetRef.kind verbatim so an HPA on a StatefulSet still
	// produces a well-formed edge to the right node id.
	hpa := graph.Resource{
		Kind: "HorizontalPodAutoscaler", Namespace: "data", Name: "ingester",
		Raw: map[string]any{
			"spec": map[string]any{
				"scaleTargetRef": map[string]any{
					"kind": "StatefulSet",
					"name": "ingester",
				},
			},
		},
	}
	got := extractEdges(t, ScalesExtractor{}, hpa, nil)
	if len(got) != 1 || got[0].To != "data/StatefulSet/ingester" {
		t.Errorf("expected edge to data/StatefulSet/ingester, got %v", got)
	}
}

func TestScales_IgnoresNonHPA(t *testing.T) {
	// Every other resource passes the extractor unchanged so the
	// registry can dispatch every resource to every extractor
	// without an outer kind dispatch.
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "podinfo",
		Raw: map[string]any{},
	}
	got := extractEdges(t, ScalesExtractor{}, dep, nil)
	if len(got) != 0 {
		t.Errorf("expected 0 edges for non-HPA resource, got %v", got)
	}
}

func TestScales_MissingFieldsSkipped(t *testing.T) {
	// Malformed HPAs (no kind, no name) should drop on the floor
	// rather than emit dangling edges to "" ids.
	cases := []struct {
		name string
		spec map[string]any
	}{
		{"no scaleTargetRef", map[string]any{}},
		{"no kind", map[string]any{"scaleTargetRef": map[string]any{"name": "x"}}},
		{"no name", map[string]any{"scaleTargetRef": map[string]any{"kind": "Deployment"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hpa := graph.Resource{
				Kind: "HorizontalPodAutoscaler", Namespace: "demo", Name: "broken",
				Raw:  map[string]any{"spec": tc.spec},
			}
			got := extractEdges(t, ScalesExtractor{}, hpa, nil)
			if len(got) != 0 {
				t.Errorf("expected 0 edges, got %v", got)
			}
		})
	}
}

func TestScales_ClusterIDPropagatesToTarget(t *testing.T) {
	// Multi-cluster mode prefixes resource ids with the cluster id.
	// Edge endpoints must carry the same prefix or they won't match
	// the target node's id in the store.
	hpa := graph.Resource{
		Kind: "HorizontalPodAutoscaler", Namespace: "demo", Name: "podinfo",
		ClusterID: "prod",
		Raw: map[string]any{
			"spec": map[string]any{
				"scaleTargetRef": map[string]any{
					"kind": "Deployment",
					"name": "podinfo",
				},
			},
		},
	}
	got := extractEdges(t, ScalesExtractor{}, hpa, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(got))
	}
	if got[0].To != "prod:demo/Deployment/podinfo" {
		t.Errorf("To = %q, want prod:demo/Deployment/podinfo", got[0].To)
	}
}
