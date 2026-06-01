package extractor

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Extractor derives edges of a single type rooted at a Resource.
//
// Implementations must be:
//
//   - Stateless. The same input must yield the same output.
//   - Concurrency-safe. The informer may invoke ExtractAll from
//     multiple goroutines.
//
// Most extractors compute target IDs directly from the resource's own
// fields and ignore the ResourceLister entirely. The selector-based
// ones (Service / NetworkPolicy) need to enumerate candidate targets;
// they take a graph.ResourceLister and issue a *scoped* list query
// (usually one namespace) rather than receiving the whole graph. That
// is what keeps the informer's per-event cost bounded — it no longer
// Snapshots every resource on every event.
//
// Returned edges set From to r.ID() and reference targets by ID.
// Dangling references (the target hasn't been observed yet, or was
// deleted) are valid: extractors emit the edge anyway and the store
// is happy to hold an edge to a missing node.
type Extractor interface {
	// Type reports the EdgeType this extractor produces.
	Type() graph.EdgeType
	// Extract returns every edge this extractor finds rooted at r.
	// q is the read-only store handle the selector extractors query;
	// extractors that resolve targets purely from r ignore it.
	Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error)
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

// Default returns a Registry pre-populated with every built-in
// extractor, in the order they appear in graph.AllEdgeTypes.
//
// Phase 0 contributed the first eight (OWNS through ATTACHED_TO);
// Phase 2 P2-T14 added the two RBAC extractors so SA -> RoleBinding
// -> Role chains land in the graph the same way OwnerReference
// chains do; Phase 3 P3-T1 added the three NetworkPolicy
// extractors so a NetworkPolicy's podSelector + ingress/egress
// declarations appear as graph edges (F-109).
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
	r.Register(&BindsSubjectExtractor{})
	r.Register(&BindsRoleExtractor{})
	r.Register(&SelectsNPExtractor{})
	r.Register(&AllowsFromExtractor{})
	r.Register(&AllowsToExtractor{})
	// F-209 platform-identity extractors. Safe to register on every
	// cluster — each no-ops on the metadata it doesn't recognise, so
	// the cost on non-cloud installs is a few map lookups per SA
	// event. Edges share BINDS_PLATFORM_IDENTITY but the synthetic
	// endpoint id encodes the platform so the UI can distinguish.
	r.Register(&EKSIdentityExtractor{}) // F-209.1, P3-T23
	r.Register(&AKSIdentityExtractor{}) // F-209.2, P3-T24
	r.Register(&GKEIdentityExtractor{}) // F-209.3, P3-T25
	// HorizontalPodAutoscaler → workload (Deployment / StatefulSet /
	// ReplicaSet / any /scale-bearing kind). Cheap — kind-gated on
	// the first line so non-HPA events return immediately.
	r.Register(&ScalesExtractor{})
	// Gatekeeper Constraint -> matched resource (ENFORCES). Kind-gated
	// on the constraints.gatekeeper.sh API group; non-Constraint events
	// return immediately. Constraints reach the pipeline through the
	// dynamic-informer path.
	r.Register(&GatekeeperExtractor{})
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
//
// One extractor's failure never suppresses the others: ExtractAll
// collects every extractor's edges and returns the first error
// alongside them, leaving the caller to log-and-continue.
func (r *Registry) ExtractAll(ctx context.Context, res graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	var (
		edges    []graph.Edge
		firstErr error
	)
	for _, e := range r.extractors {
		got, err := e.Extract(ctx, res, q)
		edges = append(edges, got...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return edges, firstErr
}
