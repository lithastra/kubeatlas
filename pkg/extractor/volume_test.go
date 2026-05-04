package extractor

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestVolume_HappyPath(t *testing.T) {
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "db",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"volumes": []any{
							map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "data"}},
							map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "logs"}},
						},
					},
				},
			},
		},
	}
	got := (VolumeExtractor{}).Extract(dep, nil)
	if len(got) != 2 {
		t.Errorf("got %d edges, want 2", len(got))
	}
	for _, e := range got {
		if e.Type != graph.EdgeTypeMountsVolume {
			t.Errorf("wrong type: %v", e.Type)
		}
	}
}

func TestVolume_EmptyDirIsIgnored(t *testing.T) {
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "p",
		Raw: map[string]any{
			"spec": map[string]any{
				"volumes": []any{
					map[string]any{"emptyDir": map[string]any{}},
				},
			},
		},
	}
	if got := (VolumeExtractor{}).Extract(pod, nil); got != nil {
		t.Errorf("expected nil edges for emptyDir-only volumes, got %v", got)
	}
}

func TestVolume_DanglingPVCRefStillEmits(t *testing.T) {
	// A workload references a PVC that doesn't exist (yet, or anymore).
	// Per the extractor contract, dangling refs are valid: emit the edge,
	// the store may hold an edge to a missing node.
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "lonely",
		Raw: map[string]any{
			"spec": map[string]any{
				"volumes": []any{
					map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "deleted-pvc"}},
				},
			},
		},
	}
	got := (VolumeExtractor{}).Extract(pod, nil)
	if len(got) != 1 || got[0].To != "demo/PersistentVolumeClaim/deleted-pvc" {
		t.Errorf("expected dangling edge to deleted-pvc, got %v", got)
	}
}

func TestVolume_NonWorkloadEmitsNothing(t *testing.T) {
	// Service / ConfigMap / etc. don't carry pod templates. The extractor
	// must short-circuit and not pretend to walk a non-existent spec.
	for _, kind := range []string{"Service", "ConfigMap", "Secret", "Ingress"} {
		r := graph.Resource{Kind: kind, Namespace: "demo", Name: "x"}
		if got := (VolumeExtractor{}).Extract(r, nil); got != nil {
			t.Errorf("kind=%s: expected nil edges, got %v", kind, got)
		}
	}
}

func TestVolume_DuplicateClaimDedup(t *testing.T) {
	// Two volumes referencing the same PVC must collapse to one edge.
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"volumes": []any{
							map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "shared"}},
							map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "shared"}},
						},
					},
				},
			},
		},
	}
	got := (VolumeExtractor{}).Extract(dep, nil)
	if len(got) != 1 {
		t.Errorf("expected 1 deduplicated edge, got %d: %v", len(got), got)
	}
}

func TestVolume_TypeReturnsConstant(t *testing.T) {
	if got := (VolumeExtractor{}).Type(); got != graph.EdgeTypeMountsVolume {
		t.Errorf("Type() = %q, want MOUNTS_VOLUME", got)
	}
}
