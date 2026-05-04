package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// SelectsExtractor emits SELECTS edges from a Service to every Pod
// (and pod-templated workload) in the same namespace whose labels
// match Service.spec.selector. An empty selector matches nothing
// (consistent with K8s semantics for headless / selectorless
// Services).
type SelectsExtractor struct{}

func (SelectsExtractor) Type() graph.EdgeType { return graph.EdgeTypeSelects }

func (SelectsExtractor) Extract(r graph.Resource, all []graph.Resource) []graph.Edge {
	if r.Kind != "Service" {
		return nil
	}
	selector := nestedStringMap(r.Raw, "spec", "selector")
	if len(selector) == 0 {
		return nil
	}
	from := r.ID()
	var edges []graph.Edge
	for _, t := range all {
		if t.Namespace != r.Namespace {
			continue
		}
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
	return edges
}
