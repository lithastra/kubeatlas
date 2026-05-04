package extractor

import "github.com/lithastra/kubeatlas/pkg/graph"

// Extractor derives edges of a single type from a Resource against a
// snapshot of every other resource the store currently knows.
//
// Implementations must be:
//
//   - Stateless. The same input must yield the same output.
//   - Concurrency-safe. The informer may invoke ExtractAll from
//     multiple goroutines.
//   - Pure. Extractors must not call back into the GraphStore — the
//     informer is responsible for writing the edges they return.
//
// Returned edges set From to r.ID() and reference targets by ID.
// Dangling references (the target hasn't been observed yet, or was
// deleted) are valid: extractors emit the edge anyway and the store
// is happy to hold an edge to a missing node.
type Extractor interface {
	// Type reports the EdgeType this extractor produces.
	Type() graph.EdgeType
	// Extract returns every edge this extractor finds rooted at r.
	Extract(r graph.Resource, all []graph.Resource) []graph.Edge
}

// Registry aggregates a set of Extractors and exposes a single entry
// point that the informer pipeline calls.
type Registry struct {
	extractors []Extractor
}

// New returns an empty Registry. Use Default for the canonical Phase 0
// set of eight built-in extractors.
func New() *Registry {
	return &Registry{}
}

// Default returns a Registry pre-populated with the eight Phase 0
// built-in extractors, in the order they appear in graph.AllEdgeTypes.
func Default() *Registry {
	r := New()
	r.Register(&OwnsExtractor{})
	r.Register(&ConfigMapExtractor{})
	r.Register(&SecretExtractor{})
	r.Register(&VolumeExtractor{})
	r.Register(&SelectsExtractor{})
	r.Register(&ServiceAccountExtractor{})
	r.Register(&RoutesExtractor{})
	r.Register(&AttachedExtractor{})
	return r
}

// Register adds an Extractor. Order is preserved; later registrations
// run after earlier ones inside ExtractAll.
func (r *Registry) Register(e Extractor) {
	r.extractors = append(r.extractors, e)
}

// ExtractAll runs every registered extractor against res and
// concatenates the results. Implements
// pkg/discovery.ExtractorRegistry.
func (r *Registry) ExtractAll(res graph.Resource, all []graph.Resource) []graph.Edge {
	var edges []graph.Edge
	for _, e := range r.extractors {
		edges = append(edges, e.Extract(res, all)...)
	}
	return edges
}
