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
