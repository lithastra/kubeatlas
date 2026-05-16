package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// maxMemoryEvents bounds the snapshot-history ring buffer. Snapshots
// are a Tier 2 feature (invariant 2.2); the memory store keeps only
// this small window so upper-layer code (the P3-T3 snapshot writer)
// can be unit-tested against the memory backend without standing up
// PostgreSQL. It is deliberately NOT a Tier 1 snapshot feature — the
// /api/v1/snapshots endpoints return 503 on Tier 1, and a 1000-event
// lossy buffer is useless as real history.
const maxMemoryEvents = 1000

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

	// Snapshot-history ring buffers (test-support stub — see
	// maxMemoryEvents). events keeps the most recent maxMemoryEvents
	// ResourceEvents; snapshotMeta keeps markers under the same cap.
	// eventSeq is the monotonic ID source; it keeps climbing even as
	// the oldest events are dropped.
	events       []graph.ResourceEvent
	snapshotMeta []graph.SnapshotMeta
	eventSeq     int64
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

// Traverse walks the in-memory adjacency maps in BFS order and
// returns every distinct resource reachable from startID within
// opts.MaxDepth hops in the requested direction. The starting node
// is not included, and unresolved IDs (edges whose endpoint never
// got an UpsertResource) are silently skipped.
func (s *Store) Traverse(_ context.Context, startID string, opts graph.TraverseOptions) ([]graph.Resource, error) {
	depth := opts.MaxDepth
	if depth <= 0 {
		depth = 5
	}
	if depth > 10 {
		depth = 10
	}
	if opts.Direction != graph.DirectionIncoming && opts.Direction != graph.DirectionOutgoing {
		return nil, fmt.Errorf("Traverse: invalid direction %q", opts.Direction)
	}

	allowed := edgeTypeSet(opts.EdgeTypes)

	s.mu.RLock()
	defer s.mu.RUnlock()

	visited := map[string]bool{startID: true}
	frontier := []string{startID}
	var out []graph.Resource

	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		var next []string
		for _, cur := range frontier {
			var peers map[edgeKey]graph.Edge
			if opts.Direction == graph.DirectionIncoming {
				peers = s.incoming[cur]
			} else {
				peers = s.outgoing[cur]
			}
			for k := range peers {
				if allowed != nil && !allowed[k.typ] {
					continue
				}
				if visited[k.other] {
					continue
				}
				visited[k.other] = true
				if r, ok := s.resources[k.other]; ok {
					out = append(out, r)
				}
				next = append(next, k.other)
			}
		}
		frontier = next
	}
	return out, nil
}

// KindCountsByNamespace walks the resource map once and tallies
// counts by (namespace, kind). Cluster-scoped resources land in the
// empty-string namespace bucket.
//
// This is the in-process equivalent of the postgres GROUP BY query
// added in the same change. It allocates only the result maps, not
// the per-resource structs Snapshot would clone — that is the whole
// point of the pushdown.
func (s *Store) KindCountsByNamespace(_ context.Context) (map[string]map[string]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]map[string]int)
	for _, r := range s.resources {
		bucket := out[r.Namespace]
		if bucket == nil {
			bucket = make(map[string]int)
			out[r.Namespace] = bucket
		}
		bucket[r.Kind]++
	}
	return out, nil
}

// CrossNamespaceEdgeCounts walks every outgoing adjacency list once
// and tallies counts by (from-ns, to-ns). Edges whose endpoint is not
// present as a resource row are skipped — they cannot be assigned a
// namespace and would otherwise crash the aggregator with empty
// strings on both sides.
func (s *Store) CrossNamespaceEdgeCounts(_ context.Context) (map[graph.NamespacePair]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[graph.NamespacePair]int)
	for fromID, peers := range s.outgoing {
		fromRes, fromOK := s.resources[fromID]
		if !fromOK {
			continue
		}
		for k := range peers {
			toRes, toOK := s.resources[k.other]
			if !toOK {
				continue
			}
			out[graph.NamespacePair{From: fromRes.Namespace, To: toRes.Namespace}]++
		}
	}
	return out, nil
}

// NamespaceSubgraph returns the resources in namespace ns plus the
// edges whose endpoints are both in that namespace. The owner-chain
// rule in K8s keeps OwnerReferences in-namespace (cluster-scoped
// owners excepted), so the namespace aggregator's owner-walk runs
// correctly against the subgraph without ever touching the rest of
// the store.
func (s *Store) NamespaceSubgraph(_ context.Context, ns string) (*graph.Graph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := &graph.Graph{Resources: make([]graph.Resource, 0), Edges: make([]graph.Edge, 0)}
	inNS := make(map[string]struct{})
	for _, r := range s.resources {
		if r.Namespace != ns {
			continue
		}
		g.Resources = append(g.Resources, r)
		inNS[r.ID()] = struct{}{}
	}
	// Edges where both endpoints are in the requested namespace.
	// Iterating outgoing avoids double-counting (incoming mirrors it).
	for fromID, peers := range s.outgoing {
		if _, ok := inNS[fromID]; !ok {
			continue
		}
		for k, e := range peers {
			if _, ok := inNS[k.other]; !ok {
				continue
			}
			g.Edges = append(g.Edges, e)
		}
	}
	return g, nil
}

// AppendEvent records one ResourceEvent in the bounded ring buffer.
// The store assigns a monotonic ID and, when the caller leaves it
// zero, a now() Timestamp. When the buffer is full the oldest event
// is dropped — lossy by design (see maxMemoryEvents).
func (s *Store) AppendEvent(_ context.Context, e graph.ResourceEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventSeq++
	e.ID = s.eventSeq
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	s.events = append(s.events, e)
	if len(s.events) > maxMemoryEvents {
		// Drop the oldest. Re-slice onto a fresh backing array so the
		// dropped event isn't pinned in memory by the slice header.
		s.events = append([]graph.ResourceEvent(nil), s.events[len(s.events)-maxMemoryEvents:]...)
	}
	return nil
}

// WriteSnapshotMeta records one SnapshotMeta marker, bounded under
// the same cap as the event ring buffer.
func (s *Store) WriteSnapshotMeta(_ context.Context, m graph.SnapshotMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventSeq++
	m.ID = s.eventSeq
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	s.snapshotMeta = append(s.snapshotMeta, m)
	if len(s.snapshotMeta) > maxMemoryEvents {
		s.snapshotMeta = append([]graph.SnapshotMeta(nil), s.snapshotMeta[len(s.snapshotMeta)-maxMemoryEvents:]...)
	}
	return nil
}

// QueryEvents returns the buffered events in [from, to], oldest
// first. An empty namespace matches every namespace. Results are
// copied so callers cannot mutate the buffer.
//
// The ring buffer is in append order, which is usually but not
// always chronological (a caller may AppendEvent with an explicit
// out-of-order Timestamp). The result is sorted by (Timestamp, ID)
// so the oldest-first contract holds regardless — matching the
// postgres backend's ORDER BY ts ASC, id ASC.
func (s *Store) QueryEvents(_ context.Context, namespace string, from, to time.Time) ([]graph.ResourceEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]graph.ResourceEvent, 0)
	for _, e := range s.events {
		if namespace != "" && e.Namespace != namespace {
			continue
		}
		if e.Timestamp.Before(from) || e.Timestamp.After(to) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// PruneEventsBefore drops ring-buffer events older than cutoff and
// returns the count removed. No batching is needed — the buffer is
// capped at maxMemoryEvents, so this is always a small in-memory
// filter. (Postgres batches because its table is unbounded; the
// memory store is bounded by construction.)
func (s *Store) PruneEventsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.events[:0:0]
	var deleted int64
	for _, e := range s.events {
		if e.Timestamp.Before(cutoff) {
			deleted++
			continue
		}
		kept = append(kept, e)
	}
	s.events = kept
	return deleted, nil
}

func edgeTypeSet(ts []graph.EdgeType) map[graph.EdgeType]bool {
	if len(ts) == 0 {
		return nil
	}
	m := make(map[graph.EdgeType]bool, len(ts))
	for _, t := range ts {
		m[t] = true
	}
	return m
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
