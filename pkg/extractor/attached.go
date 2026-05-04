package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// AttachedExtractor emits ATTACHED_TO edges from HTTPRoute to its
// parent Gateway(s) via spec.parentRefs[]. Other parent kinds are
// skipped — the Gateway API allows non-Gateway parents in principle
// but Phase 0 only models the Gateway flow.
type AttachedExtractor struct{}

func (AttachedExtractor) Type() graph.EdgeType { return graph.EdgeTypeAttachedTo }

func (AttachedExtractor) Extract(r graph.Resource, _ []graph.Resource) []graph.Edge {
	if r.Kind != "HTTPRoute" {
		return nil
	}
	from := r.ID()
	defaultNS := r.Namespace
	var edges []graph.Edge
	seen := make(map[string]struct{})
	for _, p := range nestedSlice(r.Raw, "spec", "parentRefs") {
		pmap, _ := p.(map[string]any)
		name := nestedString(pmap, "name")
		if name == "" {
			continue
		}
		kind := nestedString(pmap, "kind")
		if kind == "" {
			kind = "Gateway"
		}
		if kind != "Gateway" {
			// Non-Gateway parents are out of scope for Phase 0.
			continue
		}
		ns := nestedString(pmap, "namespace")
		if ns == "" {
			ns = defaultNS
		}
		to := graph.Resource{Kind: kind, Name: name, Namespace: ns}.ID()
		if _, ok := seen[to]; ok {
			continue
		}
		seen[to] = struct{}{}
		edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeAttachedTo})
	}
	return edges
}
