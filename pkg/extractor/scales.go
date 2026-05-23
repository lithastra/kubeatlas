package extractor

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ScalesExtractor emits SCALES edges from a HorizontalPodAutoscaler
// to the workload its spec.scaleTargetRef names. The target's kind is
// taken from scaleTargetRef.kind (typically Deployment, but the field
// also accepts StatefulSet, ReplicaSet, and any /scale-bearing CRD)
// and stays in the HPA's namespace — HPAs cannot cross namespaces.
//
// The future VerticalPodAutoscaler resource type follows the same
// spec shape (spec.targetRef with kind + name); when its informer
// lands, this extractor can grow a second Kind switch and reuse the
// emit logic. Keeping it under one extractor matches the design's
// edge-domain grouping (control-loop relationships → workload
// domain) rather than splitting it into HPA / VPA siblings.
//
// Direction is autoscaler → target. The reading "X SCALES Y" matches
// the field name and falls in line with the rest of the extractor
// family (X carries a reference to Y).
type ScalesExtractor struct{}

func (ScalesExtractor) Type() graph.EdgeType { return graph.EdgeTypeScales }

func (ScalesExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "HorizontalPodAutoscaler" {
		return nil, nil
	}
	name := nestedString(r.Raw, "spec", "scaleTargetRef", "name")
	if name == "" {
		return nil, nil
	}
	kind := nestedString(r.Raw, "spec", "scaleTargetRef", "kind")
	if kind == "" {
		// The /scale subresource is the contract; without a kind we
		// can't materialise a real target id. Skip rather than guess.
		return nil, nil
	}
	to := graph.Resource{Kind: kind, Name: name, Namespace: r.Namespace, ClusterID: r.ClusterID}.ID()
	return []graph.Edge{{From: r.ID(), To: to, Type: graph.EdgeTypeScales}}, nil
}
