package extractor

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// RoutesExtractor emits ROUTES_TO edges from Ingress, HTTPRoute, and
// OpenShift Route resources to the backend Services they target.
//
//   - Ingress: spec.rules[].http.paths[].backend.service.name
//   - HTTPRoute: spec.rules[].backendRefs[].name (kind defaults to
//     Service; backendRefs[].namespace overrides the route's ns).
//   - Route (route.openshift.io/v1): spec.to.{kind,name} and
//     spec.alternateBackends[].{kind,name}. OpenShift Route was
//     handled by the openshift rule pack through v1.1; from v1.3
//     (P3-T26) it is a built-in edge to put Route on the same
//     footing as Ingress / HTTPRoute. The rule pack stays for
//     extra-depth rules (TLS termination linkage, weighted backends).
type RoutesExtractor struct{}

func (RoutesExtractor) Type() graph.EdgeType { return graph.EdgeTypeRoutesTo }

func (RoutesExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	switch r.Kind {
	case "Ingress":
		return ingressEdges(r), nil
	case "HTTPRoute":
		return httpRouteBackendEdges(r), nil
	case "Route":
		return openshiftRouteEdges(r), nil
	}
	return nil, nil
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
			to := graph.Resource{Kind: "Service", Name: name, Namespace: ns, ClusterID: r.ClusterID}.ID()
			if _, ok := seen[to]; ok {
				continue
			}
			seen[to] = struct{}{}
			edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeRoutesTo})
		}
	}
	return edges
}

// openshiftRouteEdges turns one OpenShift Route into edges pointing
// at every Service it routes traffic to. spec.to is the primary
// target; spec.alternateBackends[] are weighted secondaries (the
// route splitter feature). Both default to kind=Service when
// omitted; we honour an explicit kind too so a future Route ->
// non-Service backend (an unusual configuration) still produces a
// well-formed edge.
//
// The synthetic target Resource carries the route's ClusterID so
// edge endpoints match the store key in multi-cluster mode (P3-T21).
func openshiftRouteEdges(r graph.Resource) []graph.Edge {
	from := r.ID()
	ns := r.Namespace
	var edges []graph.Edge
	seen := make(map[string]struct{})

	emit := func(name, kind string) {
		if name == "" {
			return
		}
		if kind == "" {
			kind = "Service"
		}
		to := graph.Resource{Kind: kind, Name: name, Namespace: ns, ClusterID: r.ClusterID}.ID()
		if _, ok := seen[to]; ok {
			return
		}
		seen[to] = struct{}{}
		edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeRoutesTo})
	}

	emit(nestedString(r.Raw, "spec", "to", "name"), nestedString(r.Raw, "spec", "to", "kind"))
	for _, b := range nestedSlice(r.Raw, "spec", "alternateBackends") {
		bmap, _ := b.(map[string]any)
		emit(nestedString(bmap, "name"), nestedString(bmap, "kind"))
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
			to := graph.Resource{Kind: kind, Name: name, Namespace: ns, ClusterID: r.ClusterID}.ID()
			if _, ok := seen[to]; ok {
				continue
			}
			seen[to] = struct{}{}
			edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeRoutesTo})
		}
	}
	return edges
}
