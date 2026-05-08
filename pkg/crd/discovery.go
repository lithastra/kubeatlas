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
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
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
// informer per CRD whose objects survive the served+namespacedness
// filter. Synchronization is internal; callers treat it as a
// goroutine-safe service started once via Start.
type Discovery struct {
	dyn     dynamic.Interface
	factory dynamicinformer.DynamicSharedInformerFactory
	store   graph.GraphStore
	rego    RegoEvaluator
	logger  *slog.Logger

	mu        sync.Mutex
	informers map[schema.GroupVersionResource]*informerEntry
}

// informerEntry pairs a per-CRD informer with the cancel func that
// stops it. Stopping the cancel triggers the underlying go-routines
// to wind down at the next select-tick.
type informerEntry struct {
	gvr    schema.GroupVersionResource
	stop   context.CancelFunc
	kind   string
	synced cache.InformerSynced
}

// Option configures Discovery at construction. Same functional-
// option pattern as pkg/discovery.InformerManager.
type Option func(*Discovery)

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
		informers: make(map[schema.GroupVersionResource]*informerEntry),
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
//     Add/Update events trigger registerCRD; delete triggers
//     deregisterCRD.
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

	d.factory = dynamicinformer.NewDynamicSharedInformerFactory(d.dyn, defaultResyncPeriod)

	metaInformer := d.factory.ForResource(crdGVR).Informer()
	if _, err := metaInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { d.onCRDAdd(ctx, obj) },
		UpdateFunc: func(_, obj any) { d.onCRDAdd(ctx, obj) },
		DeleteFunc: func(obj any) { d.onCRDDelete(ctx, obj) },
	}); err != nil {
		return fmt.Errorf("crd.Discovery.Start: meta handler: %w", err)
	}

	d.factory.Start(ctx.Done())
	d.factory.WaitForCacheSync(ctx.Done())

	d.logger.Info("crd discovery started", "resync_period", defaultResyncPeriod)
	<-ctx.Done()
	d.shutdown()
	return ctx.Err()
}

// shutdown stops every per-CRD informer. The factory itself is
// driven by the parent ctx and will exit on its own.
func (d *Discovery) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.informers {
		e.stop()
	}
}

// onCRDAdd registers (or refreshes) an informer for the GVR derived
// from the CRD's served + storage version. Refresh is a no-op if the
// GVR is already registered.
func (d *Discovery) onCRDAdd(ctx context.Context, obj any) {
	crd, ok := toCRD(obj)
	if !ok {
		d.logger.Warn("crd.onCRDAdd: object is not a CRD", "obj_type", fmt.Sprintf("%T", obj))
		return
	}
	gvr, kind, ok := pickServedGVR(crd)
	if !ok {
		d.logger.Debug("crd has no served version yet; skipping",
			"crd_name", crd.GetName())
		return
	}

	d.mu.Lock()
	if _, exists := d.informers[gvr]; exists {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	if err := d.registerCRD(ctx, gvr, kind); err != nil {
		d.logger.Warn("crd register failed",
			"gvr", gvr.String(), "err", err)
	}
}

// onCRDDelete tears down the informer the CRD owned. Resources
// already in the store are NOT cascaded — guide P2-T10 ❌ note: a
// CRD delete should not yank the downstream graph immediately.
func (d *Discovery) onCRDDelete(_ context.Context, obj any) {
	crd, ok := toCRD(obj)
	if !ok {
		return
	}
	gvr, _, ok := pickServedGVR(crd)
	if !ok {
		return
	}
	d.deregisterCRD(gvr)
}

// registerCRD starts a per-CRD informer and threads its events into
// the shared upsert / delete handlers. The cancel func owns the
// informer's lifetime; stop is wired from deregisterCRD AND from the
// parent ctx (Discovery.Start's <-ctx.Done()).
func (d *Discovery) registerCRD(ctx context.Context, gvr schema.GroupVersionResource, kind string) error {
	informerCtx, cancel := context.WithCancel(ctx)

	informer := d.factory.ForResource(gvr).Informer()
	registration, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { d.handleUpsert(informerCtx, gvr, kind, obj) },
		UpdateFunc: func(_, obj any) { d.handleUpsert(informerCtx, gvr, kind, obj) },
		DeleteFunc: func(obj any) { d.handleDelete(informerCtx, obj, kind) },
	})
	if err != nil {
		cancel()
		return fmt.Errorf("add handler: %w", err)
	}

	go informer.Run(informerCtx.Done())

	d.mu.Lock()
	d.informers[gvr] = &informerEntry{
		gvr:    gvr,
		stop:   cancel,
		kind:   kind,
		synced: informer.HasSynced,
	}
	d.mu.Unlock()

	d.logger.Info("Discovered CRD, registered informer",
		"gvr", fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource),
		"kind", kind)
	_ = registration
	return nil
}

// deregisterCRD stops the per-CRD informer and removes it from the
// registry. Idempotent: a stale delete event for a GVR already
// removed is a no-op + debug log.
func (d *Discovery) deregisterCRD(gvr schema.GroupVersionResource) {
	d.mu.Lock()
	e, ok := d.informers[gvr]
	if ok {
		delete(d.informers, gvr)
	}
	d.mu.Unlock()

	if !ok {
		d.logger.Debug("crd deregister: gvr already gone", "gvr", gvr.String())
		return
	}
	e.stop()
	d.logger.Info("Deregistered CRD informer",
		"gvr", fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource),
		"kind", e.kind)
}

// handleUpsert converts the K8s object into a graph.Resource, runs
// it through the optional Rego evaluator, and persists the resource
// + any derived edges. Mirrors pkg/discovery.InformerManager's
// handleUpsert; we duplicate intentionally because the two pipelines
// have different add-on hooks (built-in extractors vs Rego only).
func (d *Discovery) handleUpsert(ctx context.Context, gvr schema.GroupVersionResource, kind string, obj any) {
	u, ok := toUnstructured(obj)
	if !ok {
		d.logger.Warn("informer received non-unstructured object",
			"gvr", gvr.String(), "obj_type", fmt.Sprintf("%T", obj))
		return
	}
	r := discovery.UnstructuredToResource(u, kind)
	if err := d.store.UpsertResource(ctx, r); err != nil {
		d.logger.Warn("upsert resource failed",
			"id", r.ID(), "err", err)
		return
	}

	if d.rego == nil {
		return
	}
	edges, err := d.rego.EvaluateForResource(ctx, r)
	if err != nil {
		d.logger.Warn("rego eval failed; resource still stored, skipping rego-derived edges",
			"id", r.ID(), "err", err)
		return
	}
	for _, e := range edges {
		if err := d.store.UpsertEdge(ctx, e); err != nil {
			d.logger.Warn("upsert rego edge failed",
				"from", e.From, "to", e.To, "type", e.Type, "err", err)
		}
	}
}

// handleDelete drops the resource (and incident edges, via store
// cascade) when the upstream API server tells us it's gone.
// Tombstones (DeletedFinalStateUnknown) are flattened by the cache
// helper.
func (d *Discovery) handleDelete(ctx context.Context, obj any, kind string) {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = t.Obj
	}
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	r := discovery.UnstructuredToResource(u, kind)
	if err := d.store.DeleteResource(ctx, r.ID()); err != nil {
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
	for gvr := range d.informers {
		out = append(out, gvr)
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
		return v, true
	case unstructured.Unstructured:
		return &v, true
	default:
		return nil, false
	}
}
