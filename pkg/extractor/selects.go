package extractor

import (
	"context"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// SelectsExtractor emits SELECTS edges from a Service to every Pod
// (and pod-templated workload) in the same namespace whose labels
// match Service.spec.selector. An empty selector matches nothing
// (consistent with K8s semantics for headless / selectorless
// Services).
type SelectsExtractor struct{}

func (SelectsExtractor) Type() graph.EdgeType { return graph.EdgeTypeSelects }

func (SelectsExtractor) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "Service" {
		return nil, nil
	}
	selector := nestedStringMap(r.Raw, "spec", "selector")
	if len(selector) == 0 {
		return nil, nil
	}
	// A Service only selects Pods in its own namespace, so the query
	// is scoped to that namespace rather than the whole graph.
	candidates, err := q.ListResources(ctx, graph.Filter{Namespace: r.Namespace})
	if err != nil {
		return nil, fmt.Errorf("SelectsExtractor: list namespace %q: %w", r.Namespace, err)
	}
	from := r.ID()
	var edges []graph.Edge
	for _, t := range candidates {
		switch {
		case t.Kind == "Pod":
			if labelsMatch(t.Labels, selector) {
				edges = append(edges, graph.Edge{From: from, To: t.ID(), Type: graph.EdgeTypeSelects})
			}
		case hasPodTemplate(t.Kind):
			meta := podTemplateMeta(t)
			labels := nestedStringMap(meta, "labels")
			if labelsMatch(labels, selector) {
				edges = append(edges, graph.Edge{From: from, To: t.ID(), Type: graph.EdgeTypeSelects})
			}
		}
	}
	return edges, nil
}
