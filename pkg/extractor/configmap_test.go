package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestConfigMap_NotAWorkloadEmitsNothing(t *testing.T) {
	cm := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cm"}
	if got := (ConfigMapExtractor{}).Extract(cm, nil); got != nil {
		t.Errorf("expected nil edges for ConfigMap input, got %v", got)
	}
}

func TestConfigMap_DeploymentEnvFromAndValueFromAndVolumeDedup(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"envFrom": []any{
									map[string]any{
										"configMapRef": map[string]any{"name": "shared"},
									},
								},
								"env": []any{
									map[string]any{
										"valueFrom": map[string]any{
											"configMapKeyRef": map[string]any{"name": "shared"},
										},
									},
									map[string]any{
										"valueFrom": map[string]any{
											"configMapKeyRef": map[string]any{"name": "feature-flags"},
										},
									},
								},
							},
						},
						"volumes": []any{
							map[string]any{"configMap": map[string]any{"name": "shared"}},
						},
					},
				},
			},
		},
	}
	got := (ConfigMapExtractor{}).Extract(dep, nil)
	// "shared" appears 3 times (envFrom + valueFrom + volume) but
	// dedup should collapse to one edge. "feature-flags" gets its own.
	if len(got) != 2 {
		t.Fatalf("got %d edges, want 2 (dedup of 'shared' + 'feature-flags'); edges=%v", len(got), got)
	}
	want := map[string]bool{
		"demo/ConfigMap/shared":        false,
		"demo/ConfigMap/feature-flags": false,
	}
	for _, e := range got {
		if _, ok := want[e.To]; ok {
			want[e.To] = true
		}
		if e.Type != graph.EdgeTypeUsesConfigMap {
			t.Errorf("wrong type: %v", e.Type)
		}
	}
	for to, seen := range want {
		if !seen {
			t.Errorf("missing edge to %q", to)
		}
	}
}

func TestConfigMap_DanglingRefStillEmits(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"envFrom": []any{
									map[string]any{"configMapRef": map[string]any{"name": "missing-cm"}},
								},
							},
						},
					},
				},
			},
		},
	}
	got := (ConfigMapExtractor{}).Extract(dep, nil)
	if len(got) != 1 || got[0].To != "demo/ConfigMap/missing-cm" {
		t.Errorf("expected dangling edge to missing-cm, got %v", got)
	}
}

func TestConfigMap_RawPodSpecAlsoMatched(t *testing.T) {
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "raw",
		Raw: map[string]any{
			"spec": map[string]any{
				"containers": []any{
					map[string]any{
						"envFrom": []any{
							map[string]any{"configMapRef": map[string]any{"name": "raw-cm"}},
						},
					},
				},
			},
		},
	}
	got := (ConfigMapExtractor{}).Extract(pod, nil)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if got[0].From != "demo/Pod/raw" {
		t.Errorf("From = %q, want demo/Pod/raw", got[0].From)
	}
}

func TestConfigMap_MissingFieldsAreSilent(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "skel",
		Raw: map[string]any{}, // no spec at all
	}
	if got := (ConfigMapExtractor{}).Extract(dep, nil); got != nil {
		t.Errorf("expected nil edges for empty spec, got %v", got)
	}
}

func TestConfigMap_EnvWithoutConfigMapRefIsIgnored(t *testing.T) {
	// A literal env var (not from a ConfigMap) must not produce an edge.
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"env": []any{
									map[string]any{"name": "FOO", "value": "bar"},
								},
							},
						},
					},
				},
			},
		},
	}
	if got := (ConfigMapExtractor{}).Extract(dep, nil); got != nil {
		t.Errorf("expected nil edges, got %v", got)
	}
}
