package extractor

import (
	"context"
	"errors"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
// Provided spec/reference objects and required identity fields are checked;
// optional apiVersion values use the Kubernetes controller's GroupVersion parser.
// Parsing does not resolve API groups, versions, targets or scale support.
type ScalesExtractor struct{}

func (ScalesExtractor) Type() graph.EdgeType { return graph.EdgeTypeScales }

func (ScalesExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "HorizontalPodAutoscaler" {
		return nil, nil
	}
	value := r.Raw["spec"]
	if value == nil {
		return nil, nil
	}
	spec, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("ScalesExtractor: spec must be an object")
	}
	value = spec["scaleTargetRef"]
	if value == nil {
		return nil, nil
	}
	ref, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("ScalesExtractor: scaleTargetRef must be an object")
	}
	if ref == nil {
		return nil, nil
	}
	name, _ := ref["name"].(string)
	kind, _ := ref["kind"].(string)
	if name == "" || kind == "" {
		return nil, errors.New("ScalesExtractor: target kind and name must be nonempty strings")
	}
	if value := ref["apiVersion"]; value != nil {
		version, ok := value.(string)
		if !ok {
			return nil, errors.New("ScalesExtractor: target apiVersion must be a string")
		}
		if _, err := schema.ParseGroupVersion(version); err != nil {
			return nil, errors.New("ScalesExtractor: malformed target apiVersion")
		}
	}
	to := graph.Resource{Kind: kind, Name: name, Namespace: r.Namespace, ClusterID: r.ClusterID}.ID()
	return []graph.Edge{{From: r.ID(), To: to, Type: graph.EdgeTypeScales}}, nil
}
