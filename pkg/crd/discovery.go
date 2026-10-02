// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

// crdGVR is the GVR for CustomResourceDefinition objects themselves;
// the Discovery struct watches THIS to know when to (de)register
// per-CRD informers.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// defaultResyncPeriod controls the dynamic informer factory's resync
// cadence. Long enough that informer churn stays low, short enough
// that an event we somehow missed gets re-delivered within the
// budget operators expect (~5 minutes is the K8s ecosystem norm).
const defaultResyncPeriod = 5 * time.Minute

// RegoEvaluator is the slice of *rego.Engine that pkg/crd needs.
// Defining it here as an interface keeps the package independent of
// extractor/rego (no import cycle) and lets tests inject a mock that
// records calls without spinning up a real OPA evaluator.
type RegoEvaluator interface {
	EvaluateForResource(ctx context.Context, r graph.Resource) ([]graph.Edge, error)
}

// Discovery watches the cluster's CRD list and runs one dynamic
// informer per CRD with a selected served version. Both namespace and cluster
// scopes are supported. Synchronization is internal; callers treat it as a
// goroutine-safe service started once via Start.
type Discovery struct {
	dyn             dynamic.Interface
	factory         dynamicinformer.DynamicSharedInformerFactory
	store           graph.GraphStore
	rego            RegoEvaluator
	logger          *slog.Logger
	coverage        *operations.CoverageTracker
	coverageSession *operations.CoverageSession
	metaCoverage    *operations.CoverageSession

	mu        sync.Mutex
	informers map[string]*informerEntry // CRD name, not the selected version
	families  map[string]*callbackFamily
}

// registrationIdentity changes only when the observation endpoint or CRD
// incarnation changes, not for schema/status updates or metadata resync.
type registrationIdentity struct {
	name  string
	uid   types.UID
	gvr   schema.GroupVersionResource
	kind  string
	scope apiextensionsv1.ResourceScope
}

// callbackFamily serializes deliveries across overlapping informer lifetimes
// for one CRD, including delete/recreate. Cancellation cannot undo an in-flight
// store operation that ignores context; its successor must wait for that
// delivery, then replace its data. Other CRDs do not share this lock.
type callbackFamily struct {
	mu   sync.Mutex
	refs int // live informer runs; protected by Discovery.mu
}

type informerEntry struct {
	identity registrationIdentity
	stop     context.CancelFunc
	synced   cache.InformerSynced
	coverage *operations.CoverageSession
}

// Option configures Discovery at construction. Same functional-
// option pattern as pkg/discovery.InformerManager.
type Option func(*Discovery)

// WithCoverage supplies standalone CRD-source evidence, independent of core
// informers. Federation does not run this collector or infer member coverage.
func WithCoverage(tracker *operations.CoverageTracker) Option {
	return func(d *Discovery) { d.coverage = tracker }
}

// WithRegoEvaluator wires an evaluator. Without it, CRD events still
// land in the store but no rego-derived edges are produced. P2-T11
// supplies the real *rego.Engine.
func WithRegoEvaluator(r RegoEvaluator) Option {
	return func(d *Discovery) { d.rego = r }
}

// WithLogger swaps the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Discovery) {
		if l != nil {
			d.logger = l
		}
	}
}

// New returns a Discovery wired against the given dynamic client and
// store. The factory is constructed lazily inside Start so the
// caller's context bounds informer lifetimes; tests that need to
// inspect state before Start can still call RegisteredGVRs (returns
// the empty slice).
func New(dyn dynamic.Interface, store graph.GraphStore, opts ...Option) *Discovery {
	d := &Discovery{
		dyn:       dyn,
		store:     store,
		logger:    slog.Default(),
		informers: make(map[string]*informerEntry),
		families:  make(map[string]*callbackFamily),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Start runs the discovery loop until ctx is cancelled.
//
// Two informers cooperate:
//
//  1. The "meta" informer over CustomResourceDefinitions itself.
//     Add/Update events reconcile the selected registration; delete stops
//     only the matching CRD incarnation.
//  2. The dynamic informer factory hosts one informer per registered
//     CRD. Each forwards Add/Update events to handleUpsert and
//     Delete events to handleDelete.
//
// Returns a non-nil error only if the meta informer cannot start;
// per-CRD failures are logged at warn and do not halt the loop
// (anti-pattern #35: a single bad CRD must not stall discovery).
func (d *Discovery) Start(ctx context.Context) error {
	if d.dyn == nil {
		return errors.New("crd.Discovery.Start: nil dynamic client")
	}
	if d.store == nil {
		return errors.New("crd.Discovery.Start: nil store")
	}

	if d.coverage != nil {
		var err error
		d.coverageSession, err = d.coverage.BeginSource("", operations.CoverageSourceCRD, time.Now())
		if err != nil {
			return err
		}
		d.metaCoverage = d.beginCoverageType(crdGVR)
	}
	defer func() { d.shutdown(); d.coverageSession.Stop(time.Now()) }()
	d.factory = dynamicinformer.NewDynamicSharedInformerFactory(discovery.ObserveCoverageClient(d.dyn, d.metaCoverage), defaultResyncPeriod)

	metaInformer := d.factory.ForResource(crdGVR).Informer()
	registration, err := metaInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { d.onCRDAdd(ctx, obj) },
		UpdateFunc: func(_, obj any) { d.onCRDAdd(ctx, obj) },
		DeleteFunc: func(obj any) { d.onCRDDelete(ctx, obj) },
	})
	if err != nil {
		return fmt.Errorf("crd.Discovery.Start: meta handler: %w", err)
	}

	d.factory.Start(ctx.Done())
	if d.metaCoverage != nil {
		go func() {
			if cache.WaitForCacheSync(ctx.Done(), registration.HasSynced) {
				d.metaCoverage.InitialDeliveryComplete(crdGVR)
			}
		}()
	}
	d.factory.WaitForCacheSync(ctx.Done())

	d.logger.Info("crd discovery started", "resync_period", defaultResyncPeriod)
	<-ctx.Done()
	return ctx.Err()
}

func (d *Discovery) beginCoverageType(gvr schema.GroupVersionResource) *operations.CoverageSession {
	if d.coverageSession == nil {
		return nil
	}
	session, err := d.coverageSession.BeginType(gvr, time.Now())
	if err != nil {
		// Evidence capacity must not silently disable an existing collector.
		// The source keeps a sticky limited-inventory signal instead.
		d.metaCoverage.RecordGap(crdGVR, operations.CoverageProcessingFailed, time.Now())
		return nil
	}
	return session
}

// shutdown stops every per-CRD informer. The factory itself is
// driven by the parent ctx and will exit on its own.
func (d *Discovery) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for name := range d.informers {
		d.stopLocked(name)
	}
}

// onCRDAdd reconciles the selection from the ordered metadata informer stream.
// A version/shape/incarnation change retires the previous registration first.
func (d *Discovery) onCRDAdd(ctx context.Context, obj any) {
	crd, ok := toCRD(obj)
	if !ok {
		d.metaCoverage.RecordGap(crdGVR, operations.CoverageProcessingFailed, time.Now())
		d.logger.Warn("crd.onCRDAdd: object is not a CRD", "obj_type", fmt.Sprintf("%T", obj))
		return
	}
	gvr, kind, served := pickServedGVR(crd)
	identity := registrationIdentity{name: crd.Name, uid: crd.UID, gvr: gvr, kind: kind, scope: crd.Spec.Scope}
	if err := d.reconcileCRD(ctx, identity, served); err != nil {
		d.metaCoverage.RecordGap(crdGVR, operations.CoverageProcessingFailed, time.Now())
		d.logger.Warn("crd register failed",
			"crd_name", crd.Name, "err", err)
	}
}

// onCRDDelete tears down the informer the CRD owned. Resources
// already in the store are NOT cascaded — guide P2-T10 ❌ note: a
// CRD delete should not yank the downstream graph immediately.
func (d *Discovery) onCRDDelete(_ context.Context, obj any) {
	obj = unwrapTombstone(obj)
	crd, ok := toCRD(obj)
	if !ok {
		d.metaCoverage.RecordGap(crdGVR, operations.CoverageProcessingFailed, time.Now())
		return
	}
	if crd.Name == "" || crd.UID == "" {
		d.metaCoverage.RecordGap(crdGVR, operations.CoverageProcessingFailed, time.Now())
		return
	}
	d.deregisterCRD(crd.Name, crd.UID)
}

// reconcileCRD is atomic with respect to metadata updates/deletes, but never
// waits for a blocked graph callback. A CRD with no served version stops its
// stored selection even though the incoming object has no selected GVR.
func (d *Discovery) reconcileCRD(ctx context.Context, identity registrationIdentity, served bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	old := d.informers[identity.name]
	if served && old != nil && old.identity == identity {
		return nil
	}
	// A malformed replacement must not leave an obsolete registration healthy.
	d.stopLocked(identity.name)
	if identity.name == "" || identity.uid == "" {
		return errors.New("invalid CRD observation identity")
	}
	if !served {
		return nil
	}
	gvr, kind := identity.gvr, identity.kind
	if gvr.Group == "" || kind == "Secret" || identity.name != gvr.Resource+"."+gvr.Group ||
		(identity.scope != apiextensionsv1.NamespaceScoped && identity.scope != apiextensionsv1.ClusterScoped) ||
		!operations.ValidAPIResourceDescriptor(operations.APIResourceDescriptor{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: kind}) {
		return errors.New("invalid CRD observation identity")
	}
	informerCtx, cancel := context.WithCancel(ctx)
	coverage := d.beginCoverageType(gvr)
	// Shared factories cache stopped informers, which cannot be restarted.
	// Every registration owns a fresh informer and its own coverage token.
	factory := dynamicinformer.NewDynamicSharedInformerFactory(discovery.ObserveCoverageClient(d.dyn, coverage), defaultResyncPeriod)
	informer := factory.ForResource(gvr).Informer()
	family := d.families[identity.name]
	if family == nil {
		family = &callbackFamily{}
	}
	upsert := func(obj any) {
		family.mu.Lock()
		defer family.mu.Unlock()
		d.handleUpsert(informerCtx, gvr, kind, obj, coverage)
	}
	registration, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    upsert,
		UpdateFunc: func(_, obj any) { upsert(obj) },
		DeleteFunc: func(obj any) {
			family.mu.Lock()
			defer family.mu.Unlock()
			d.handleDelete(informerCtx, gvr, obj, kind, coverage)
		},
	})
	if err != nil {
		cancel()
		coverage.RecordGap(gvr, operations.CoverageProcessingFailed, time.Now())
		coverage.Stop(time.Now())
		return fmt.Errorf("add handler: %w", err)
	}

	d.families[identity.name] = family
	family.refs++
	go func() {
		// Run waits for the handler processor, including in-flight callbacks.
		informer.Run(informerCtx.Done())
		d.mu.Lock()
		defer d.mu.Unlock()
		family.refs--
		if family.refs == 0 {
			delete(d.families, identity.name)
		}
	}()
	if coverage != nil {
		go func() {
			if cache.WaitForCacheSync(informerCtx.Done(), registration.HasSynced) {
				coverage.InitialDeliveryComplete(gvr)
			}
		}()
	}

	d.informers[identity.name] = &informerEntry{
		identity: identity,
		stop:     cancel,
		synced:   registration.HasSynced,
		coverage: coverage,
	}

	d.logger.Info("Discovered CRD, registered informer",
		"gvr", fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource),
		"kind", kind, "scope", identity.scope)
	return nil
}

// deregisterCRD resolves the stored endpoint, not the deleted object's version
// selection. A delayed tombstone for an old UID cannot stop its replacement.
func (d *Discovery) deregisterCRD(name string, uid types.UID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := d.informers[name]; e != nil && e.identity.uid == uid {
		d.stopLocked(name)
	}
}

// Caller holds d.mu. Graph nodes/edges are deliberately retained.
func (d *Discovery) stopLocked(name string) {
	e := d.informers[name]
	if e == nil {
		return
	}
	delete(d.informers, name)
	e.stop()
	e.coverage.Stop(time.Now())
	d.logger.Info("Deregistered CRD informer",
		"gvr", e.identity.gvr.String(), "kind", e.identity.kind)
}

// handleUpsert converts the K8s object into a graph.Resource, runs
// it through the optional Rego evaluator, and persists the resource
// + any derived edges. Mirrors pkg/discovery.InformerManager's
// handleUpsert; we duplicate intentionally because the two pipelines
// have different add-on hooks (built-in extractors vs Rego only).
func (d *Discovery) handleUpsert(ctx context.Context, gvr schema.GroupVersionResource, kind string, obj any, coverage *operations.CoverageSession) {
	if ctx.Err() != nil {
		return
	}
	u, ok := toUnstructured(obj)
	if !ok {
		coverage.RecordGap(gvr, operations.CoverageProcessingFailed, time.Now())
		d.logger.Warn("informer received non-unstructured object",
			"gvr", gvr.String(), "obj_type", fmt.Sprintf("%T", obj))
		return
	}
	r := discovery.UnstructuredToResource(u, kind)
	if err := d.store.UpsertResource(ctx, r); err != nil {
		if ctx.Err() != nil {
			return
		}
		coverage.RecordGap(gvr, operations.CoveragePersistenceFailed, time.Now())
		d.logger.Warn("upsert resource failed",
			"id", r.ID(), "err", err)
		return
	}

	if ctx.Err() != nil || d.rego == nil {
		return
	}
	edges, err := d.rego.EvaluateForResource(ctx, r)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		coverage.RecordGap(gvr, operations.CoverageExtractionFailed, time.Now())
		d.logger.Warn("rego eval failed; resource still stored, skipping rego-derived edges",
			"id", r.ID(), "err", err)
		return
	}
	for _, e := range edges {
		if ctx.Err() != nil {
			return
		}
		if err := d.store.UpsertEdge(ctx, e); err != nil {
			if ctx.Err() != nil {
				return
			}
			coverage.RecordGap(gvr, operations.CoveragePersistenceFailed, time.Now())
			d.logger.Warn("upsert rego edge failed",
				"from", e.From, "to", e.To, "type", e.Type, "err", err)
		}
	}
}

// handleDelete drops the resource (and incident edges, via store
// cascade) when the upstream API server tells us it's gone.
// Tombstones (DeletedFinalStateUnknown) are flattened by the cache
// helper.
func (d *Discovery) handleDelete(ctx context.Context, gvr schema.GroupVersionResource, obj any, kind string, coverage *operations.CoverageSession) {
	if ctx.Err() != nil {
		return
	}
	obj = unwrapTombstone(obj)
	u, ok := toUnstructured(obj)
	if !ok {
		coverage.RecordGap(gvr, operations.CoverageProcessingFailed, time.Now())
		return
	}
	r := discovery.UnstructuredToResource(u, kind)
	if err := d.store.DeleteResource(ctx, r.ID()); err != nil {
		if ctx.Err() != nil {
			return
		}
		coverage.RecordGap(gvr, operations.CoveragePersistenceFailed, time.Now())
		d.logger.Warn("delete resource failed",
			"id", r.ID(), "err", err)
	}
}

// RegisteredGVRs returns a snapshot of every GVR with an active
// informer. Used by tests and the /healthz endpoint to verify that
// an expected CRD has actually been picked up.
func (d *Discovery) RegisteredGVRs() []schema.GroupVersionResource {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]schema.GroupVersionResource, 0, len(d.informers))
	for _, entry := range d.informers {
		out = append(out, entry.identity.gvr)
	}
	return out
}

// toCRD coerces any to a typed CustomResourceDefinition. Dynamic
// informers deliver *unstructured.Unstructured for the meta GVR;
// FromUnstructured lifts those into the typed struct so we can read
// spec.names + spec.versions without manual map walking.
func toCRD(obj any) (*apiextensionsv1.CustomResourceDefinition, bool) {
	u, ok := toUnstructured(obj)
	if !ok {
		return nil, false
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &crd); err != nil {
		return nil, false
	}
	return &crd, true
}

// pickServedGVR picks the storage version (or the first served
// version) and returns the GVR + Kind. Returns ok=false when no
// version is served yet — that happens during CRD bootstrap
// transitions; we'll see another Update event when it stabilises.
func pickServedGVR(crd *apiextensionsv1.CustomResourceDefinition) (schema.GroupVersionResource, string, bool) {
	if crd == nil {
		return schema.GroupVersionResource{}, "", false
	}
	chosen := ""
	for _, v := range crd.Spec.Versions {
		if !v.Served {
			continue
		}
		if v.Storage {
			chosen = v.Name
			break
		}
		if chosen == "" {
			chosen = v.Name
		}
	}
	if chosen == "" {
		return schema.GroupVersionResource{}, "", false
	}
	return schema.GroupVersionResource{
		Group:    crd.Spec.Group,
		Version:  chosen,
		Resource: crd.Spec.Names.Plural,
	}, crd.Spec.Names.Kind, true
}

// toUnstructured normalises both *Unstructured and Unstructured
// payloads the dynamic informer can deliver.
func toUnstructured(obj any) (*unstructured.Unstructured, bool) {
	switch v := obj.(type) {
	case *unstructured.Unstructured:
		return v, v != nil
	case unstructured.Unstructured:
		return &v, true
	default:
		return nil, false
	}
}

func unwrapTombstone(obj any) any {
	switch tombstone := obj.(type) {
	case cache.DeletedFinalStateUnknown:
		return tombstone.Obj
	case *cache.DeletedFinalStateUnknown:
		if tombstone != nil {
			return tombstone.Obj
		}
	}
	return obj
}
