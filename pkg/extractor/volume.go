package extractor

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// VolumeExtractor emits MOUNTS_VOLUME edges from workloads (and raw
// Pods) to every PersistentVolumeClaim they mount via
// volumes[].persistentVolumeClaim.claimName.
type VolumeExtractor struct{}

func (VolumeExtractor) Type() graph.EdgeType { return graph.EdgeTypeMountsVolume }

func (VolumeExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "Pod" && !hasPodTemplate(r.Kind) {
		return nil, nil
	}
	from := r.ID()
	ns := r.Namespace
	seen := make(map[string]struct{})
	var edges []graph.Edge
	for _, v := range podTemplateVolumes(r) {
		vmap, _ := v.(map[string]any)
		name := nestedString(vmap, "persistentVolumeClaim", "claimName")
		if name == "" {
			continue
		}
		to := graph.Resource{Kind: "PersistentVolumeClaim", Name: name, Namespace: ns, ClusterID: r.ClusterID}.ID()
		if _, ok := seen[to]; ok {
			continue
		}
		seen[to] = struct{}{}
		edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeMountsVolume})
	}
	return edges, nil
}
