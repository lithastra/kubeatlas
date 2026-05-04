package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// DefaultResyncPeriod is the SharedInformerFactory resync interval.
// Ten minutes is the client-go default: short enough to recover from
// missed deletes, long enough to avoid hammering the apiserver.
const DefaultResyncPeriod = 10 * time.Minute

// MinimalCoreGVRs is a small bootstrap set used by the informer when
// no explicit GVR list is configured. The full Phase 0 registry of
// 15 core resources lives in resources.go (added in P0-T12).
var MinimalCoreGVRs = []schema.GroupVersionResource{
	{Group: "", Version: "v1", Resource: "pods"},
	{Group: "", Version: "v1", Resource: "configmaps"},
	{Group: "", Version: "v1", Resource: "secrets"},
	{Group: "apps", Version: "v1", Resource: "deployments"},
}

// ExtractorRegistry is the seam between the informer pipeline and the
// edge-extractor pipeline (pkg/extractor, populated in P0-T15). The
// informer asks the registry to derive edges from a single resource
// against the current snapshot of all resources.
//
// Defined here as an interface to avoid an import cycle: pkg/extractor
// imports pkg/graph, and pkg/discovery would otherwise need to import
// pkg/extractor too.
type ExtractorRegistry interface {
	ExtractAll(r graph.Resource, all []graph.Resource) []graph.Edge
}

// noopRegistry returns no edges. Used as the default when no extractor
// is configured (W2 informer wiring exists before W4 extractors land).
type noopRegistry struct{}

func (noopRegistry) ExtractAll(_ graph.Resource, _ []graph.Resource) []graph.Edge {
	return nil
}

// InformerManager runs a dynamic SharedInformerFactory for the
// configured GVRs and forwards K8s add/update/delete events into a
// GraphStore via the configured ExtractorRegistry.
type InformerManager struct {
	factory   dynamicinformer.DynamicSharedInformerFactory
	store     graph.GraphStore
	extractor ExtractorRegistry
	gvrs      []schema.GroupVersionResource
	kindCache map[schema.GroupVersionResource]string
}

// InformerOption configures an InformerManager.
type InformerOption func(*InformerManager)

// WithGVRs overrides the default MinimalCoreGVRs list.
func WithGVRs(gvrs []schema.GroupVersionResource) InformerOption {
	return func(m *InformerManager) { m.gvrs = gvrs }
}

// WithExtractor wires an extractor registry. Without this option the
// informer still updates resources but emits no edges.
func WithExtractor(r ExtractorRegistry) InformerOption {
	return func(m *InformerManager) { m.extractor = r }
}

// WithResync overrides the default resync period.
func WithResync(d time.Duration) InformerOption {
	return func(m *InformerManager) {
		m.factory = dynamicinformer.NewDynamicSharedInformerFactory(
			factoryClient(m.factory), d,
		)
	}
}

// factoryClient pulls the dynamic client back out of an existing
// factory; used only when WithResync rebuilds the factory after
// construction.
func factoryClient(f dynamicinformer.DynamicSharedInformerFactory) dynamic.Interface {
	type clienter interface {
		Client() dynamic.Interface
	}
	if c, ok := f.(clienter); ok {
		return c.Client()
	}
	// Older controller-runtime versions don't expose Client(); the
	// constructor path that uses WithResync passes a dynamic.Interface
	// directly via the factory we just built, so this branch is only
	// hit on a programming error.
	return nil
}

// NewInformerManager constructs an InformerManager using the dynamic
// client from c. Pass options to override defaults.
func NewInformerManager(dyn dynamic.Interface, store graph.GraphStore, opts ...InformerOption) *InformerManager {
	m := &InformerManager{
		factory:   dynamicinformer.NewDynamicSharedInformerFactory(dyn, DefaultResyncPeriod),
		store:     store,
		extractor: noopRegistry{},
		gvrs:      MinimalCoreGVRs,
		kindCache: make(map[schema.GroupVersionResource]string),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// NewClientForConfig builds a Client from a pre-built rest.Config. Used
// by tests (envtest) that supply their own config; production callers
// use NewClient.
func NewClientForConfig(cfg *rest.Config) (*Client, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	return &Client{dynamic: dyn}, nil
}

// Dynamic returns the underlying dynamic client. Exposed so callers can
// hand it to NewInformerManager without re-loading kubeconfig.
func (c *Client) Dynamic() dynamic.Interface {
	return c.dynamic
}

// Start registers event handlers for every configured GVR, starts the
// SharedInformerFactory, waits for the initial cache sync, and blocks
// until ctx is done. Returns ctx.Err() on shutdown.
func (m *InformerManager) Start(ctx context.Context) error {
	for _, gvr := range m.gvrs {
		if isSkipped(gvr) {
			slog.Warn("skipping watch on blacklisted GVR", "gvr", gvr)
			continue
		}
		gvr := gvr // capture for closure
		informer := m.factory.ForResource(gvr).Informer()
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { m.handleUpsert(ctx, gvr, obj) },
			UpdateFunc: func(_, obj any) { m.handleUpsert(ctx, gvr, obj) },
			DeleteFunc: func(obj any) { m.handleDelete(ctx, obj) },
		})
		if err != nil {
			return fmt.Errorf("register handler for %s: %w", gvr, err)
		}
	}

	m.factory.Start(ctx.Done())
	syncs := m.factory.WaitForCacheSync(ctx.Done())
	for gvr, ok := range syncs {
		if !ok {
			return fmt.Errorf("informer for %s failed to sync", gvr)
		}
	}
	slog.Info("all informers synced", "count", len(syncs))
	<-ctx.Done()
	return ctx.Err()
}

// handleUpsert converts the K8s object into a graph.Resource and
// upserts it into the store. If an extractor is configured, it derives
// edges from the snapshot and upserts those too.
func (m *InformerManager) handleUpsert(ctx context.Context, gvr schema.GroupVersionResource, obj any) {
	u, ok := toUnstructured(obj)
	if !ok {
		slog.Warn("informer received non-unstructured object", "type", fmt.Sprintf("%T", obj))
		return
	}
	r := unstructuredToResource(u, m.kindFor(gvr, u))
	if err := m.store.UpsertResource(ctx, r); err != nil {
		slog.Warn("upsert resource failed", "id", r.ID(), "err", err)
		return
	}

	// Edge re-derivation: ask the extractor for edges out of this
	// resource against the current snapshot. Edges are upserted; we do
	// not delete stale edges here, that requires per-resource diffing
	// the extractor will handle in P0-T15.
	snap, err := m.store.Snapshot(ctx)
	if err != nil {
		return
	}
	for _, e := range m.extractor.ExtractAll(r, snap.Resources) {
		if err := m.store.UpsertEdge(ctx, e); err != nil {
			slog.Warn("upsert edge failed", "from", e.From, "to", e.To, "err", err)
		}
	}
}

// handleDelete removes the resource from the store. The store cascades
// to incident edges so we do not need to walk them here.
func (m *InformerManager) handleDelete(ctx context.Context, obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	id := u.GetNamespace() + "/" + u.GetKind() + "/" + u.GetName()
	if err := m.store.DeleteResource(ctx, id); err != nil {
		slog.Warn("delete resource failed", "id", id, "err", err)
	}
}

func toUnstructured(obj any) (*unstructured.Unstructured, bool) {
	switch v := obj.(type) {
	case *unstructured.Unstructured:
		return v, true
	case unstructured.Unstructured:
		return &v, true
	default:
		return nil, false
	}
}

// kindFor returns the Kind for a GVR. The dynamic informer hands us
// objects whose GetKind() may be empty, so we cache the resolved Kind
// from the first object we see for that GVR.
func (m *InformerManager) kindFor(gvr schema.GroupVersionResource, u *unstructured.Unstructured) string {
	if k := u.GetKind(); k != "" {
		m.kindCache[gvr] = k
		return k
	}
	if k, ok := m.kindCache[gvr]; ok {
		return k
	}
	// Fallback: derive from the resource name. Strip plural "s" or "es".
	// Good enough for the common cases that hit this branch (rare).
	return defaultKindFromResource(gvr.Resource)
}

func defaultKindFromResource(r string) string {
	switch {
	case strings.HasSuffix(r, "ies"):
		return strings.ToUpper(r[:1]) + r[1:len(r)-3] + "y"
	case strings.HasSuffix(r, "ses"):
		return strings.ToUpper(r[:1]) + r[1:len(r)-2]
	case strings.HasSuffix(r, "s"):
		return strings.ToUpper(r[:1]) + r[1:len(r)-1]
	default:
		return strings.ToUpper(r[:1]) + r[1:]
	}
}

// unstructuredToResource builds a graph.Resource from a K8s object,
// populating all the W2-introduced metadata fields plus the Raw
// unstructured object that extractors use to read spec-level fields.
func unstructuredToResource(u *unstructured.Unstructured, kind string) graph.Resource {
	r := graph.Resource{
		Kind:            kind,
		Name:            u.GetName(),
		Namespace:       u.GetNamespace(),
		Labels:          u.GetLabels(),
		GroupVersion:    u.GetAPIVersion(),
		UID:             u.GetUID(),
		Annotations:     u.GetAnnotations(),
		ResourceVersion: u.GetResourceVersion(),
		Raw:             u.DeepCopy().Object,
	}
	if owners := u.GetOwnerReferences(); len(owners) > 0 {
		r.OwnerReferences = make([]graph.OwnerRef, 0, len(owners))
		for _, o := range owners {
			r.OwnerReferences = append(r.OwnerReferences, graph.OwnerRef{
				Kind: o.Kind,
				Name: o.Name,
				UID:  o.UID,
			})
		}
	}
	return r
}

// isSkipped reports whether the given GVR is on the PoC-era blacklist
// of resources that have no architectural intent. The check is
// preserved from the PoC even though the informer is GVR-driven — it
// exists so a future caller cannot accidentally start watching events
// or leases by adding them to the registry.
func isSkipped(gvr schema.GroupVersionResource) bool {
	key := strings.TrimPrefix(gvr.Group+"/"+gvr.Version+"/"+gvr.Resource, "/")
	return skippedGVRs[key]
}
