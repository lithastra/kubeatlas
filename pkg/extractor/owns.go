package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// OwnsExtractor emits OWNS edges from a resource to each of its
// metadata.ownerReferences. Direction is owned -> owner: this matches
// the PoC's JSON output and lets the chain Pod -> ReplicaSet ->
// Deployment fall out naturally as a sequence of edges that share an
// EdgeType. The conventional reading "X OWNS Y" is inverted; the
// project guides treat the resulting edge as "X carries an owner
// reference to Y".
type OwnsExtractor struct{}

func (OwnsExtractor) Type() graph.EdgeType { return graph.EdgeTypeOwns }

func (OwnsExtractor) Extract(r graph.Resource, _ []graph.Resource) []graph.Edge {
	if len(r.OwnerReferences) == 0 {
		return nil
	}
	edges := make([]graph.Edge, 0, len(r.OwnerReferences))
	for _, o := range r.OwnerReferences {
		edges = append(edges, graph.Edge{
			From: r.ID(),
			To:   graph.Resource{Kind: o.Kind, Name: o.Name, Namespace: r.Namespace}.ID(),
			Type: graph.EdgeTypeOwns,
		})
	}
	return edges
}
