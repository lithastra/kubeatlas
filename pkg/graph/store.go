package graph

import (
	"context"
	"time"
)

// ResourceLister is the narrow read-only slice of GraphStore an
// edge extractor needs: a single filtered list query. Extractors take
// this rather than GraphStore so they cannot mutate the graph, and
// rather than a materialised []Resource so the informer no longer has
// to Snapshot the whole graph on every event (the O(N²) cold-start
// the pushdown work removed from the view aggregators).
//
// Every GraphStore satisfies ResourceLister.
type ResourceLister interface {
	ListResources(ctx context.Context, filter Filter) ([]Resource, error)
}

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

	// KindCountsByNamespace returns counts of resources grouped by
	// (namespace, kind). Cluster-scoped resources are bucketed under
	// the empty-string namespace key. Used by the cluster-level
	// aggregator to avoid materialising every Resource just to count
	// them — the alternative (Snapshot + Go-side counting) allocates
	// O(R) full Resource structs per request and OOM-kills the API
	// pod on real clusters around 5K-7K resources.
	//
	// The returned map is owned by the caller and safe to mutate.
	// Implementations must return non-nil maps; an empty store
	// returns an empty (non-nil) outer map.
	//
	// labels is the F-114 label filter: when non-empty, only
	// resources carrying every key=value pair are counted. A nil or
	// empty map counts every resource (the pre-F-114 behaviour).
	KindCountsByNamespace(ctx context.Context, labels map[string]string) (map[string]map[string]int, error)

	// CrossNamespaceEdgeCounts returns counts of edges grouped by
	// (from-namespace, to-namespace). Same-namespace pairs are
	// included; callers that only want cross-namespace edges should
	// filter where key.From != key.To. Edge type is intentionally not
	// part of the key because the cluster-level view collapses all
	// edge types into a single "from-ns → to-ns" arrow.
	//
	// Endpoints whose resource row is missing (dangling edges) are
	// dropped — they cannot be assigned a namespace bucket.
	//
	// labels is the F-114 label filter: when non-empty, an edge is
	// counted only if BOTH endpoint resources carry every key=value
	// pair. A nil or empty map counts every edge.
	//
	// The returned map is owned by the caller and safe to mutate.
	CrossNamespaceEdgeCounts(ctx context.Context, labels map[string]string) (map[NamespacePair]int, error)

	// NamespaceSubgraph returns every resource in namespace ns plus
	// every edge whose endpoints are both in that namespace. Cross-
	// namespace edges and resources in other namespaces are not
	// included, matching the namespace-level aggregator's existing
	// visible-set rule (an edge is emitted only if both endpoints
	// are visible in the namespace view).
	//
	// labels is the F-114 label filter: when non-empty, only
	// resources carrying every key=value pair are included, and an
	// edge survives only if both its (surviving) endpoints do.
	//
	// Used by the namespace-level aggregator to avoid materialising
	// the full Snapshot just to filter it down to one namespace.
	NamespaceSubgraph(ctx context.Context, ns string, labels map[string]string) (*Graph, error)

	// Snapshot history (F-111 / P3-T2). The Tier 2 (postgres)
	// backend persists these durably; the Tier 1 (memory) backend
	// keeps only a small bounded ring buffer for test support —
	// snapshots are a Tier 2 feature (invariant 2.2) and the
	// /api/v1/snapshots endpoints return 503 on Tier 1.

	// AppendEvent records one observed add/update/delete in the
	// append-only event stream. The store assigns ResourceEvent.ID
	// and (when zero) ResourceEvent.Timestamp; callers leave both
	// zero. Never updates or deletes an existing row — a correction
	// is a compensating event.
	AppendEvent(ctx context.Context, e ResourceEvent) error

	// WriteSnapshotMeta records one periodic full-sync marker. The
	// store assigns SnapshotMeta.ID and (when zero) Timestamp.
	WriteSnapshotMeta(ctx context.Context, m SnapshotMeta) error

	// ListSnapshotMeta returns the recorded full-sync markers,
	// most-recent first. Powers GET /api/v1/snapshots.
	ListSnapshotMeta(ctx context.Context) ([]SnapshotMeta, error)

	// QueryEvents returns every ResourceEvent whose Timestamp falls
	// in [from, to], ordered oldest-first. An empty namespace
	// matches every namespace; a non-empty namespace filters to it.
	QueryEvents(ctx context.Context, namespace string, from, to time.Time) ([]ResourceEvent, error)

	// PruneEventsBefore deletes every resource_events row older than
	// cutoff and returns the number deleted. The F-111 retention
	// worker calls it on a fixed cadence so the event stream does
	// not grow without bound.
	//
	// Implementations MUST delete in bounded batches — a single
	// unbounded DELETE on a multi-million-row table locks it for
	// the duration. The call returns only when every expired row
	// is gone (or ctx is cancelled).
	PruneEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// LabelStats returns, for every label key present on any
	// resource, how many resources carry it and its most common
	// values (F-114). It powers GET /api/v1/labels — the data the
	// UI's "group by label" picker is built from.
	//
	// Implementations cap each key's Values slice (a high-cardinality
	// key such as pod-template-hash would otherwise return thousands
	// of values); LabelStat.ValueCount reports the true distinct-value
	// total so a caller knows the list is a truncated top-N.
	LabelStats(ctx context.Context) ([]LabelStat, error)

	// ListResourcesInCluster returns every resource whose ClusterID
	// equals clusterID, intersected with filter. The empty clusterID
	// matches resources with no ClusterID set — the single-cluster
	// path through v1.2, where the multicluster manager (P3-T21) was
	// not active. Federation aggregation (P3-T22) calls it once per
	// member cluster to build a per-cluster view.
	//
	// Implementations must apply the same Filter semantics as
	// ListResources — namespace, kind, and label matching are
	// identical; only the ClusterID gate is added on top.
	ListResourcesInCluster(ctx context.Context, clusterID string, filter Filter) ([]Resource, error)

	// GetEdgesAcrossClusters returns every edge whose endpoints are
	// resources in the given cluster set. Edges with at least one
	// endpoint outside the set, or one endpoint that is dangling
	// (no resource row), are dropped — matching the visible-set rule
	// the namespace and cluster aggregators use.
	//
	// An empty clusterIDs slice returns no edges. A single-element
	// slice with the empty string returns edges entirely within the
	// single-cluster (ClusterID="") subgraph, so the v1.2 single-
	// cluster path keeps its existing behaviour when called this way.
	//
	// Federation aggregation (P3-T22) uses this to recover edges
	// that span clusters once it has merged the per-cluster views.
	GetEdgesAcrossClusters(ctx context.Context, clusterIDs []string) ([]Edge, error)

	// Search runs a full-text query across resources and returns a
	// ranked page of matches (F-113).
	//
	// Pushing this into the store is mandatory, not an optimisation:
	// the Phase 1 search read store.Snapshot and scanned in Go,
	// which is the O(R) full-Resource allocation P3-T0a removed from
	// the cluster/namespace views for OOM-ing the API pod past ~5K
	// resources.
	//
	// Tier 2 answers from a GIN-indexed tsvector column. Tier 1 has
	// no index and falls back to a linear scan, flagging
	// SearchResult.LinearScan so the API can warn the caller. Both
	// honour SearchQuery.Limit and report SearchResult.Total.
	Search(ctx context.Context, q SearchQuery) (SearchResult, error)
}

// SearchQuery is the parsed form of a /api/v1/search request — the
// deliberately small query model F-113 ships in v1.1: free-text
// terms plus optional kind / namespace field filters. A richer
// query DSL is deferred until there is user feedback (ADR 0011).
type SearchQuery struct {
	// Text is the free-text portion. Tier 2 hands it to PostgreSQL's
	// websearch_to_tsquery; Tier 1 matches it as a case-insensitive
	// substring. May be empty for a pure field-filter query such as
	// "kind:Pod".
	Text string

	// Kind and Namespace are exact-match filters. Empty means "any".
	Kind      string
	Namespace string

	// Limit caps the number of Matches returned. Implementations
	// clamp a non-positive value to a sane default.
	Limit int
}

// SearchResult is what GraphStore.Search returns.
type SearchResult struct {
	// Matches is the ranked page of resources, at most Limit long.
	Matches []Resource

	// Total is the count of resources matching the query before
	// Limit was applied, so the API can report "showing N of Total"
	// and set a truncation flag.
	Total int

	// LinearScan reports that the result came from an unindexed O(N)
	// scan — the Tier 1 memory store. The API surfaces it as a
	// warning so a large-cluster operator knows the query was slow
	// by construction, and that Tier 2 fixes it.
	LinearScan bool
}

// MaxLabelValuesPerKey caps the number of values LabelStats reports
// per key. A high-cardinality key (pod-template-hash, controller-uid)
// has one distinct value per resource; returning them all would make
// GET /api/v1/labels both huge and useless.
const MaxLabelValuesPerKey = 100

// LabelStat is the per-key summary in a LabelStats result: the label
// key, how many resources carry it, and its most common values.
type LabelStat struct {
	Key string `json:"key"`

	// ResourceCount is the number of resources carrying this key
	// (with any value).
	ResourceCount int `json:"resourceCount"`

	// ValueCount is the number of DISTINCT values seen for this key
	// across the cluster. Values below is capped at
	// MaxLabelValuesPerKey, so ValueCount > len(Values) signals the
	// list is a truncated top-N.
	ValueCount int `json:"valueCount"`

	// Values are the key's most common values, most-frequent first,
	// at most MaxLabelValuesPerKey entries.
	Values []LabelValue `json:"values"`
}

// LabelValue is one (value, frequency) pair within a LabelStat.
type LabelValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// NamespacePair keys the result of CrossNamespaceEdgeCounts. From and
// To are namespace names; the empty string represents cluster-scoped
// resources (matching the convention in KindCountsByNamespace).
type NamespacePair struct {
	From string
	To   string
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
//
// ClusterID is the federation gate (P3-T26 follow-up to P3-T20).
// Empty matches every cluster (the single-cluster v1.2 baseline);
// non-empty matches exactly that ClusterID. Selector extractors set
// it from their source resource's ClusterID so a multi-cluster
// Service does not match Pods in a sibling cluster.
type Filter struct {
	Kind      string
	Namespace string
	Labels    map[string]string
	ClusterID string
}

// ErrNotFound is returned by GetResource when the requested ID does
// not exist. Implementations should return this typed error so callers
// can use errors.As to distinguish "missing" from infrastructure
// errors.
type ErrNotFound struct{ ID string }

func (e ErrNotFound) Error() string { return "resource not found: " + e.ID }
