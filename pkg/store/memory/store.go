package memory

import (
	"context"
	"sync"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Store is the in-memory implementation of graph.GraphStore.
//
// Concurrency-safe via a single RWMutex; suitable for clusters up to
// roughly 5K resources. Beyond that scale, switch to the PostgreSQL +
// Apache AGE backend in pkg/store/postgres (available from v1.0).
type Store struct {
	mu        sync.RWMutex
	resources map[string]graph.Resource         // id -> Resource
	outgoing  map[string]map[edgeKey]graph.Edge // from -> (to, type) -> Edge
	incoming  map[string]map[edgeKey]graph.Edge // to   -> (from, type) -> Edge
}

// edgeKey identifies an edge within an adjacency map. The (other-end,
// type) pair makes (from, to, type) the natural uniqueness key, so two
// edges of different types between the same pair coexist.
type edgeKey struct {
	other string
	typ   graph.EdgeType
}

// New returns a fresh, empty Store.
func New() *Store {
	return &Store{
		resources: make(map[string]graph.Resource),
		outgoing:  make(map[string]map[edgeKey]graph.Edge),
		incoming:  make(map[string]map[edgeKey]graph.Edge),
	}
}

// UpsertResource inserts or replaces the resource at r.ID().
func (s *Store) UpsertResource(_ context.Context, r graph.Resource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[r.ID()] = r
	return nil
}

// DeleteResource removes the resource at id and cascades to every edge
// incident on it. Missing ids are a no-op.
func (s *Store) DeleteResource(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resources, id)
	for k := range s.outgoing[id] {
		if peers := s.incoming[k.other]; peers != nil {
			delete(peers, edgeKey{other: id, typ: k.typ})
		}
	}
	delete(s.outgoing, id)
	for k := range s.incoming[id] {
		if peers := s.outgoing[k.other]; peers != nil {
			delete(peers, edgeKey{other: id, typ: k.typ})
		}
	}
	delete(s.incoming, id)
	return nil
}

// UpsertEdge inserts or replaces the edge identified by
// (e.From, e.To, e.Type).
func (s *Store) UpsertEdge(_ context.Context, e graph.Edge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outgoing[e.From] == nil {
		s.outgoing[e.From] = make(map[edgeKey]graph.Edge)
	}
	if s.incoming[e.To] == nil {
		s.incoming[e.To] = make(map[edgeKey]graph.Edge)
	}
	s.outgoing[e.From][edgeKey{other: e.To, typ: e.Type}] = e
	s.incoming[e.To][edgeKey{other: e.From, typ: e.Type}] = e
	return nil
}

// DeleteEdge removes the edge identified by (from, to, t). Missing
// edges are a no-op.
func (s *Store) DeleteEdge(_ context.Context, from, to string, t graph.EdgeType) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if peers := s.outgoing[from]; peers != nil {
		delete(peers, edgeKey{other: to, typ: t})
	}
	if peers := s.incoming[to]; peers != nil {
		delete(peers, edgeKey{other: from, typ: t})
	}
	return nil
}

// GetResource returns the resource at id or graph.ErrNotFound.
func (s *Store) GetResource(_ context.Context, id string) (graph.Resource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.resources[id]
	if !ok {
		return graph.Resource{}, graph.ErrNotFound{ID: id}
	}
	return r, nil
}

// ListResources returns every resource matching the filter. Empty
// filter fields mean "any". Labels match exactly: every key/value in
// filter.Labels must be present on the resource.
func (s *Store) ListResources(_ context.Context, filter graph.Filter) ([]graph.Resource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]graph.Resource, 0, len(s.resources))
	for _, r := range s.resources {
		if filter.Kind != "" && r.Kind != filter.Kind {
			continue
		}
		if filter.Namespace != "" && r.Namespace != filter.Namespace {
			continue
		}
		if !labelsMatch(r.Labels, filter.Labels) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// ListIncoming returns every edge whose To equals id.
func (s *Store) ListIncoming(_ context.Context, id string) ([]graph.Edge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return collect(s.incoming[id]), nil
}

// ListOutgoing returns every edge whose From equals id.
func (s *Store) ListOutgoing(_ context.Context, id string) ([]graph.Edge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return collect(s.outgoing[id]), nil
}

// Snapshot returns a consistent point-in-time copy of the entire
// graph. Resources and edges are copied; callers are free to mutate
// the returned slices.
func (s *Store) Snapshot(_ context.Context) (*graph.Graph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := &graph.Graph{
		Resources: make([]graph.Resource, 0, len(s.resources)),
		Edges:     make([]graph.Edge, 0),
	}
	for _, r := range s.resources {
		g.Resources = append(g.Resources, r)
	}
	for _, peers := range s.outgoing {
		for _, e := range peers {
			g.Edges = append(g.Edges, e)
		}
	}
	return g, nil
}

func collect(peers map[edgeKey]graph.Edge) []graph.Edge {
	out := make([]graph.Edge, 0, len(peers))
	for _, e := range peers {
		out = append(out, e)
	}
	return out
}

// labelsMatch reports whether every key/value pair in want is present
// in have. An empty want matches any have.
func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}
