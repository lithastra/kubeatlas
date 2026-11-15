package extractor

import (
	"context"
	"errors"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// AttachedExtractor emits ATTACHED_TO edges from HTTPRoute to its
// parent Gateway(s) via spec.parentRefs[]. Only the Gateway API group
// is modeled; an omitted group defaults to gateway.networking.k8s.io,
// while an explicit empty group refers to core. Other groups/kinds are skipped.
type AttachedExtractor struct{}

func (AttachedExtractor) Type() graph.EdgeType { return graph.EdgeTypeAttachedTo }

func (AttachedExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "HTTPRoute" {
		return nil, nil
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
		if value, present := pmap["group"]; present {
			group, ok := value.(string)
			if !ok {
				return edges, errors.New("AttachedExtractor: parent group must be a string")
			}
			if group != "gateway.networking.k8s.io" {
				continue
			}
		}
		ns := nestedString(pmap, "namespace")
		if ns == "" {
			ns = defaultNS
		}
		to := graph.Resource{Kind: kind, Name: name, Namespace: ns, ClusterID: r.ClusterID}.ID()
		if _, ok := seen[to]; ok {
			continue
		}
		seen[to] = struct{}{}
		edges = append(edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeAttachedTo})
	}
	return edges, nil
}
