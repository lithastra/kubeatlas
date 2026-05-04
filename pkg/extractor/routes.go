package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// RoutesExtractor emits ROUTES_TO edges from Ingress and HTTPRoute
// resources to the backend Services they target.
//
//   - Ingress: spec.rules[].http.paths[].backend.service.name
//   - HTTPRoute: spec.rules[].backendRefs[].name (kind defaults to
//     Service; backendRefs[].namespace overrides the route's ns).
type RoutesExtractor struct{}

func (RoutesExtractor) Type() graph.EdgeType { return graph.EdgeTypeRoutesTo }

func (RoutesExtractor) Extract(r graph.Resource, _ []graph.Resource) []graph.Edge {
	switch r.Kind {
	case "Ingress":
		return ingressEdges(r)
	case "HTTPRoute":
		return httpRouteBackendEdges(r)
	}
	return nil
}

func ingressEdges(r graph.Resource) []graph.Edge {
	from := r.ID()
	ns := r.Namespace
	var edges []graph.Edge
	seen := make(map[string]struct{})
	for _, rule := range nestedSlice(r.Raw, "spec", "rules") {
		rmap, _ := rule.(map[string]any)
		for _, p := range nestedSlice(rmap, "http", "paths") {
			pmap, _ := p.(map[string]any)
			name := nestedString(pmap, "backend", "service", "name")
			if name == "" {
				continue
			}
			to := graph.Resource{Kind: "Service", Name: name, Namespace: ns}.ID()
			if _, ok := seen[to]; ok {
				continue
			}
			seen[to] = struct{}{}
			edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeRoutesTo})
		}
	}
	return edges
}

func httpRouteBackendEdges(r graph.Resource) []graph.Edge {
	from := r.ID()
	defaultNS := r.Namespace
	var edges []graph.Edge
	seen := make(map[string]struct{})
	for _, rule := range nestedSlice(r.Raw, "spec", "rules") {
		rmap, _ := rule.(map[string]any)
		for _, b := range nestedSlice(rmap, "backendRefs") {
			bmap, _ := b.(map[string]any)
			name := nestedString(bmap, "name")
			if name == "" {
				continue
			}
			kind := nestedString(bmap, "kind")
			if kind == "" {
				kind = "Service"
			}
			ns := nestedString(bmap, "namespace")
			if ns == "" {
				ns = defaultNS
			}
			to := graph.Resource{Kind: kind, Name: name, Namespace: ns}.ID()
			if _, ok := seen[to]; ok {
				continue
			}
			seen[to] = struct{}{}
			edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeRoutesTo})
		}
	}
	return edges
}
