// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package gatekeeper wires KubeAtlas to OPA Gatekeeper. It watches
// ConstraintTemplates — whose generated CRDs are not known at startup —
// and, for each, registers a dynamic informer over the Constraint kind
// that template produces. Constraint events flow into the store and
// through the extractor pipeline, which emits the ENFORCES edges.
//
// This is strictly read-only observation: KubeAtlas reads the status
// the Gatekeeper controller computes and never sits in the admission
// path or re-evaluates a policy.
package gatekeeper

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// constraintTemplateGVR is the meta resource this component watches to
// learn which Constraint kinds exist.
var constraintTemplateGVR = schema.GroupVersionResource{
	Group:    "templates.gatekeeper.sh",
	Version:  "v1",
	Resource: "constrainttemplates",
}

const (
	constraintGroup   = "constraints.gatekeeper.sh"
	constraintVersion = "v1beta1"
	resyncPeriod      = 5 * time.Minute
)

// ExtractorRegistry is the slice of the extractor registry this
// component needs — defined locally to avoid importing pkg/extractor
// (and the import cycle that would create through discovery).
type ExtractorRegistry interface {
	ExtractAll(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error)
}

// Discovery watches ConstraintTemplates and drives a
// DynamicInformerManager to register one informer per Constraint kind.
type Discovery struct {
	dyn       dynamic.Interface
	store     graph.GraphStore
	extractor ExtractorRegistry
	dynMgr    *discovery.DynamicInformerManager
	logger    *slog.Logger
	factory   dynamicinformer.DynamicSharedInformerFactory
}

// Option configures Discovery.
type Option func(*Discovery)

// WithLogger swaps the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Discovery) {
		if l != nil {
			d.logger = l
		}
	}
}

// New builds a gatekeeper Discovery. The DynamicInformerManager is the
// shared informer-of-informers; its metrics are surfaced on /metrics by
// the caller.
func New(dyn dynamic.Interface, store graph.GraphStore, ext ExtractorRegistry, dynMgr *discovery.DynamicInformerManager, opts ...Option) *Discovery {
	d := &Discovery{
		dyn:       dyn,
		store:     store,
		extractor: ext,
		dynMgr:    dynMgr,
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Start runs until ctx is cancelled. It starts the DynamicInformer
// manager, then a meta-informer over ConstraintTemplates that
// registers/deregisters per-Constraint informers as templates come and
// go. A failure to register one Constraint informer is logged and
// skipped — a single bad template must not stall the rest (the same
// graceful-degradation rule crd.Discovery follows).
func (d *Discovery) Start(ctx context.Context) error {
	if d.dyn == nil {
		return errors.New("gatekeeper.Discovery.Start: nil dynamic client")
	}

	// The manager binds its base context on Start; run it alongside.
	go func() { _ = d.dynMgr.Start(ctx) }()

	d.factory = dynamicinformer.NewDynamicSharedInformerFactory(d.dyn, resyncPeriod)
	informer := d.factory.ForResource(constraintTemplateGVR).Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { d.onTemplate(ctx, obj) },
		UpdateFunc: func(_, obj any) { d.onTemplate(ctx, obj) },
		DeleteFunc: func(obj any) { d.onTemplateDelete(obj) },
	}); err != nil {
		return err
	}

	d.factory.Start(ctx.Done())
	d.factory.WaitForCacheSync(ctx.Done())
	d.logger.Info("gatekeeper discovery started")

	<-ctx.Done()
	return ctx.Err()
}

// onTemplate registers a Constraint informer for the kind the template
// generates. Idempotent (the manager dedups by GVR).
func (d *Discovery) onTemplate(ctx context.Context, obj any) {
	gvr, kind, ok := constraintGVRFromTemplate(obj)
	if !ok {
		return
	}

	// The manager binds its context asynchronously; wait out the brief
	// startup window so an at-boot template is not dropped.
	for !d.dynMgr.Started() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}

	if err := d.dynMgr.Add(gvr, d.constraintHandler(ctx, kind)); err != nil {
		d.logger.Warn("gatekeeper: register constraint informer",
			"gvr", gvr.String(), "err", err)
		return
	}
	d.logger.Info("gatekeeper: tracking constraint kind", "kind", kind, "gvr", gvr.String())
}

// onTemplateDelete stops the Constraint informer the template owned.
func (d *Discovery) onTemplateDelete(obj any) {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = t.Obj
	}
	gvr, kind, ok := constraintGVRFromTemplate(obj)
	if !ok {
		return
	}
	d.dynMgr.Remove(gvr)
	d.logger.Info("gatekeeper: stopped tracking constraint kind", "kind", kind, "gvr", gvr.String())
}

// constraintHandler returns the event handler for one Constraint kind:
// each Constraint flows into the store and through the extractor
// pipeline (which emits the ENFORCES edges); deletes cascade.
func (d *Discovery) constraintHandler(ctx context.Context, kind string) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { d.handleConstraint(ctx, kind, obj) },
		UpdateFunc: func(_, obj any) { d.handleConstraint(ctx, kind, obj) },
		DeleteFunc: func(obj any) { d.handleConstraintDelete(ctx, kind, obj) },
	}
}

func (d *Discovery) handleConstraint(ctx context.Context, kind string, obj any) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	r := discovery.UnstructuredToResource(u, kind)
	if err := d.store.UpsertResource(ctx, r); err != nil {
		d.logger.Warn("gatekeeper: upsert constraint", "id", r.ID(), "err", err)
		return
	}
	edges, err := d.extractor.ExtractAll(ctx, r, d.store)
	if err != nil {
		d.logger.Warn("gatekeeper: extract constraint edges", "id", r.ID(), "err", err)
		return
	}
	for _, e := range edges {
		if err := d.store.UpsertEdge(ctx, e); err != nil {
			d.logger.Warn("gatekeeper: upsert enforce edge",
				"from", e.From, "to", e.To, "err", err)
		}
	}
}

func (d *Discovery) handleConstraintDelete(ctx context.Context, kind string, obj any) {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = t.Obj
	}
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	r := discovery.UnstructuredToResource(u, kind)
	if err := d.store.DeleteResource(ctx, r.ID()); err != nil {
		d.logger.Warn("gatekeeper: delete constraint", "id", r.ID(), "err", err)
	}
}

// constraintGVRFromTemplate derives the Constraint GVR + Kind from a
// ConstraintTemplate's spec.crd.spec.names.kind. Gatekeeper names the
// generated CRD's plural as the lowercased kind, so the resource is
// strings.ToLower(kind).
func constraintGVRFromTemplate(obj any) (schema.GroupVersionResource, string, bool) {
	u, ok := toUnstructured(obj)
	if !ok {
		return schema.GroupVersionResource{}, "", false
	}
	kind, found, err := unstructured.NestedString(u.Object, "spec", "crd", "spec", "names", "kind")
	if err != nil || !found || kind == "" {
		return schema.GroupVersionResource{}, "", false
	}
	return schema.GroupVersionResource{
		Group:    constraintGroup,
		Version:  constraintVersion,
		Resource: strings.ToLower(kind),
	}, kind, true
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
