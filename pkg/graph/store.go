package graph

import "context"

// GraphStore is the persistence-agnostic interface for storing and
// querying the dependency graph.
//
// Tier 1 (in-memory) lives in pkg/store/memory and is the default
// backend through Phase 1. Tier 2 (PostgreSQL + Apache AGE) lives in
// pkg/store/postgres and is enabled in v1.0. Both implementations
// must satisfy the contract test suite in pkg/graph/storetest.
type GraphStore interface {
	// Mutations.
	UpsertResource(ctx context.Context, r Resource) error
	DeleteResource(ctx context.Context, id string) error
	UpsertEdge(ctx context.Context, e Edge) error
	DeleteEdge(ctx context.Context, from, to string, t EdgeType) error

	// Queries.
	GetResource(ctx context.Context, id string) (Resource, error)
	ListResources(ctx context.Context, filter Filter) ([]Resource, error)
	ListIncoming(ctx context.Context, id string) ([]Edge, error)
	ListOutgoing(ctx context.Context, id string) ([]Edge, error)

	// Snapshot returns a consistent point-in-time view of the entire
	// graph. Used by the CLI -once mode and by the REST API.
	Snapshot(ctx context.Context) (*Graph, error)

	// Traverse walks the graph from startID in the given direction
	// and returns every distinct resource reachable within
	// opts.MaxDepth hops. The starting node itself is not included.
	// Direction is mandatory — callers express "blast radius"
	// (DirectionIncoming) and "what does this depend on"
	// (DirectionOutgoing) explicitly rather than relying on a
	// per-method default.
	Traverse(ctx context.Context, startID string, opts TraverseOptions) ([]Resource, error)
}

// Direction names a graph traversal direction. Anti-pattern from
// P2-T15: "do not encode reverse semantics in a comment". Each call
// site must pick one.
type Direction string

const (
	// DirectionIncoming follows incoming edges (sources -> startID).
	// BlastRadius uses this — "what depends on me?".
	DirectionIncoming Direction = "incoming"
	// DirectionOutgoing follows outgoing edges (startID -> targets).
	// "What do I depend on?".
	DirectionOutgoing Direction = "outgoing"
)

// TraverseOptions configures GraphStore.Traverse.
//
// MaxDepth caps path length; values <= 0 default to 5 (covers ~99%
// of K8s dependency chains; deeper graphs are almost always cyclic
// and should be flagged separately). Implementations clamp at 10
// to keep query plans tractable.
//
// EdgeTypes is an optional allowlist of edge labels. Empty = any.
type TraverseOptions struct {
	Direction Direction
	MaxDepth  int
	EdgeTypes []EdgeType
}

// Filter narrows down ListResources results. Empty fields mean "any".
// Labels match exactly; selector-style matching belongs to the
// extractor layer, not the store.
type Filter struct {
	Kind      string
	Namespace string
	Labels    map[string]string
}

// ErrNotFound is returned by GetResource when the requested ID does
// not exist. Implementations should return this typed error so callers
// can use errors.As to distinguish "missing" from infrastructure
// errors.
type ErrNotFound struct{ ID string }

func (e ErrNotFound) Error() string { return "resource not found: " + e.ID }
