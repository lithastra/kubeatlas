package aggregator

// Registry maps Level constants to their Aggregator implementation so
// the API layer can dispatch a request to the correct aggregator
// without knowing about the concrete types. New aggregators wire in by
// being added to NewRegistry().
type Registry struct {
	aggs map[Level]Aggregator
}

// NewRegistry returns a Registry populated with every Phase 1 level.
func NewRegistry() *Registry {
	return &Registry{
		aggs: map[Level]Aggregator{
			LevelCluster:   ClusterAggregator{},
			LevelNamespace: NamespaceAggregator{},
			LevelWorkload:  WorkloadAggregator{},
			LevelResource:  ResourceAggregator{},
		},
	}
}

// Get returns the aggregator for a level. The boolean is false when
// the level isn't registered (e.g. a future level the server hasn't
// learned about yet).
func (r *Registry) Get(level Level) (Aggregator, bool) {
	a, ok := r.aggs[level]
	return a, ok
}

// Levels returns the set of registered levels in a stable order
// (insertion order via the canonical Phase 1 list).
func (r *Registry) Levels() []Level {
	return []Level{LevelCluster, LevelNamespace, LevelWorkload, LevelResource}
}
