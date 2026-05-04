package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestSecret_NotAWorkloadEmitsNothing(t *testing.T) {
	cm := graph.Resource{Kind: "Service", Namespace: "demo", Name: "svc"}
	if got := (SecretExtractor{}).Extract(cm, nil); got != nil {
		t.Errorf("expected nil edges for Service input, got %v", got)
	}
}

func TestSecret_AllFourReferenceStylesDedup(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"imagePullSecrets": []any{
							map[string]any{"name": "regcred"},
						},
						"containers": []any{
							map[string]any{
								"envFrom": []any{
									map[string]any{"secretRef": map[string]any{"name": "db-creds"}},
								},
								"env": []any{
									map[string]any{
										"valueFrom": map[string]any{
											"secretKeyRef": map[string]any{"name": "db-creds"},
										},
									},
								},
							},
						},
						"volumes": []any{
							map[string]any{"secret": map[string]any{"secretName": "tls-cert"}},
						},
					},
				},
			},
		},
	}
	got := (SecretExtractor{}).Extract(dep, nil)
	// regcred + db-creds (env+envFrom dedup) + tls-cert = 3 edges.
	if len(got) != 3 {
		t.Fatalf("got %d edges, want 3 (regcred + db-creds + tls-cert); edges=%v", len(got), got)
	}
	want := map[string]bool{
		"demo/Secret/regcred":  false,
		"demo/Secret/db-creds": false,
		"demo/Secret/tls-cert": false,
	}
	for _, e := range got {
		if e.Type != graph.EdgeTypeUsesSecret {
			t.Errorf("wrong type: %v", e.Type)
		}
		want[e.To] = true
	}
	for to, seen := range want {
		if !seen {
			t.Errorf("missing edge to %q", to)
		}
	}
}

func TestSecret_DanglingRefStillEmits(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"envFrom": []any{
									map[string]any{"secretRef": map[string]any{"name": "missing-secret"}},
								},
							},
						},
					},
				},
			},
		},
	}
	got := (SecretExtractor{}).Extract(dep, nil)
	if len(got) != 1 || got[0].To != "demo/Secret/missing-secret" {
		t.Errorf("expected dangling edge to missing-secret, got %v", got)
	}
}

func TestSecret_ImagePullSecretsOnRawPod(t *testing.T) {
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "p",
		Raw: map[string]any{
			"spec": map[string]any{
				"imagePullSecrets": []any{
					map[string]any{"name": "regcred"},
				},
				"containers": []any{},
			},
		},
	}
	got := (SecretExtractor{}).Extract(pod, nil)
	if len(got) != 1 || got[0].To != "demo/Secret/regcred" {
		t.Errorf("expected single edge to regcred, got %v", got)
	}
}

func TestSecret_TypeReturnsConstant(t *testing.T) {
	if got := (SecretExtractor{}).Type(); got != graph.EdgeTypeUsesSecret {
		t.Errorf("Type() = %q, want USES_SECRET", got)
	}
}

func TestSecret_LiteralEnvVarIgnored(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"env": []any{
									map[string]any{"name": "PORT", "value": "8080"},
								},
							},
						},
					},
				},
			},
		},
	}
	if got := (SecretExtractor{}).Extract(dep, nil); got != nil {
		t.Errorf("expected nil edges, got %v", got)
	}
}
