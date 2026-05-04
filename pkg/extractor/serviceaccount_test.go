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

func TestServiceAccount_LegacyServiceAccountField(t *testing.T) {
	// Older manifests use spec.serviceAccount instead of
	// spec.serviceAccountName. The extractor must honour both so we
	// don't silently fall back to "default" on a perfectly valid Pod.
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "p",
		Raw: map[string]any{
			"spec": map[string]any{
				"serviceAccount": "legacy-sa",
			},
		},
	}
	got := (ServiceAccountExtractor{}).Extract(pod, nil)
	if len(got) != 1 || got[0].To != "demo/ServiceAccount/legacy-sa" {
		t.Errorf("expected edge to legacy-sa, got %v", got)
	}
}

func TestServiceAccount_DeploymentTemplateNesting(t *testing.T) {
	// Workloads carry the SA name under spec.template.spec.
	// serviceAccountName, not spec.serviceAccountName. The extractor
	// must walk the pod-template path for workload kinds.
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
	if len(got) != 1 || got[0].From != "demo/Deployment/api" || got[0].To != "demo/ServiceAccount/api-sa" {
		t.Errorf("expected Deployment/api -> SA/api-sa, got %v", got)
	}
}

func TestServiceAccount_CronJobJobTemplateNesting(t *testing.T) {
	// CronJob nests one extra level: spec.jobTemplate.spec.template.spec.
	// The shared spec helpers unwrap this; this test pins that behaviour.
	cj := graph.Resource{
		Kind: "CronJob", Namespace: "demo", Name: "backup",
		Raw: map[string]any{
			"spec": map[string]any{
				"jobTemplate": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"serviceAccountName": "backup-sa",
							},
						},
					},
				},
			},
		},
	}
	got := (ServiceAccountExtractor{}).Extract(cj, nil)
	if len(got) != 1 || got[0].To != "demo/ServiceAccount/backup-sa" {
		t.Errorf("expected CronJob to resolve nested SA, got %v", got)
	}
}

func TestServiceAccount_NonWorkloadEmitsNothing(t *testing.T) {
	// Service / ConfigMap / etc. don't run as a SA.
	for _, kind := range []string{"Service", "ConfigMap", "Secret", "Ingress", "Namespace"} {
		r := graph.Resource{
			Kind: kind, Namespace: "demo", Name: "x",
			Raw: map[string]any{"spec": map[string]any{"serviceAccountName": "ignored"}},
		}
		if got := (ServiceAccountExtractor{}).Extract(r, nil); got != nil {
			t.Errorf("kind=%s: expected nil edges, got %v", kind, got)
		}
	}
}
