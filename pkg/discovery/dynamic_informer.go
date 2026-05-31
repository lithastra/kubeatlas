// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// ErrManagerNotStarted is returned by Add when the manager's base
// context has not been bound yet (Start has not run). Callers should
// only Add after Start has begun.
var ErrManagerNotStarted = errors.New("dynamic informer manager not started")

// DynamicInformerManager runs informers for GVRs that are not known at
// startup — the "informer-of-informers" pattern. Unlike crd.Discovery,
// which derives its GVRs by watching CustomResourceDefinitions, this
// manager exposes explicit Add/Remove so a caller can wire its own
// trigger (e.g. a Gatekeeper ConstraintTemplate handler that registers
// an informer for the Constraint kind the template generates).
//
// Each Add spins up one shared informer in its own goroutine with its
// own cancel func, mirroring crd.Discovery.registerCRD. Remove cancels
// it. Start binds the base context and blocks until it is cancelled,
// then tears every informer down. The whole surface is goroutine-safe.
type DynamicInformerManager struct {
	dyn     dynamic.Interface
	logger  *slog.Logger
	metrics *DynamicMetrics

	mu        sync.RWMutex
	factory   dynamicinformer.DynamicSharedInformerFactory
	baseCtx   context.Context
	started   bool
	informers map[schema.GroupVersionResource]*dynamicHandle
}

// dynamicHandle pairs an informer's cancel func with its sync check.
type dynamicHandle struct {
	cancel context.CancelFunc
	synced cache.InformerSynced
}

// DynamicOption configures a DynamicInformerManager at construction.
type DynamicOption func(*DynamicInformerManager)

// WithDynamicLogger swaps the structured logger.
func WithDynamicLogger(l *slog.Logger) DynamicOption {
	return func(m *DynamicInformerManager) {
		if l != nil {
			m.logger = l
		}
	}
}

// WithDynamicMetrics injects a shared metrics sink so /metrics can
// surface the active-informer gauge and error counter.
func WithDynamicMetrics(m *DynamicMetrics) DynamicOption {
	return func(d *DynamicInformerManager) {
		if m != nil {
			d.metrics = m
		}
	}
}

// NewDynamicInformerManager builds a manager against the given dynamic
// client. The shared informer factory is created in Start so the
// caller's context bounds every informer's lifetime.
func NewDynamicInformerManager(dyn dynamic.Interface, opts ...DynamicOption) *DynamicInformerManager {
	m := &DynamicInformerManager{
		dyn:       dyn,
		logger:    slog.Default(),
		metrics:   NewDynamicMetrics(),
		informers: make(map[schema.GroupVersionResource]*dynamicHandle),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Start binds the base context every spawned informer inherits, then
// blocks until ctx is cancelled and tears every informer down. It
// satisfies the same componentStarter shape as crd.Discovery, so it
// drops straight into the runWatch result loop.
func (m *DynamicInformerManager) Start(ctx context.Context) error {
	if m.dyn == nil {
		return errors.New("DynamicInformerManager.Start: nil dynamic client")
	}

	m.mu.Lock()
	m.factory = dynamicinformer.NewDynamicSharedInformerFactory(m.dyn, DefaultResyncPeriod)
	m.baseCtx = ctx
	m.started = true
	m.mu.Unlock()

	m.logger.Info("dynamic informer manager started", "resync_period", DefaultResyncPeriod)
	<-ctx.Done()
	m.removeAll()
	return ctx.Err()
}

// Add starts an informer for gvr and registers handler against it.
// Idempotent: a second Add for a GVR already running is a no-op (the
// existing informer and handler stay in place). An AddEventHandler
// failure increments the error counter and is returned — it never
// leaves a half-registered informer behind.
func (m *DynamicInformerManager) Add(gvr schema.GroupVersionResource, handler cache.ResourceEventHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return ErrManagerNotStarted
	}
	if _, exists := m.informers[gvr]; exists {
		return nil
	}

	informer := m.factory.ForResource(gvr).Informer()
	if _, err := informer.AddEventHandler(handler); err != nil {
		m.metrics.incErrors()
		return fmt.Errorf("dynamic informer add %s: %w", gvr.String(), err)
	}

	informerCtx, cancel := context.WithCancel(m.baseCtx)
	go informer.Run(informerCtx.Done())

	m.informers[gvr] = &dynamicHandle{cancel: cancel, synced: informer.HasSynced}
	m.metrics.setActive(len(m.informers))
	m.logger.Info("dynamic informer registered",
		"gvr", fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource))
	return nil
}

// Remove stops the informer for gvr. Idempotent: removing a GVR that
// is not registered is a no-op.
func (m *DynamicInformerManager) Remove(gvr schema.GroupVersionResource) {
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.informers[gvr]
	if !ok {
		return
	}
	delete(m.informers, gvr)
	h.cancel()
	m.metrics.setActive(len(m.informers))
	m.logger.Info("dynamic informer removed",
		"gvr", fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource))
}

// Has reports whether an informer is currently registered for gvr.
func (m *DynamicInformerManager) Has(gvr schema.GroupVersionResource) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.informers[gvr]
	return ok
}

// ActiveGVRs returns a snapshot of every GVR with a running informer.
func (m *DynamicInformerManager) ActiveGVRs() []schema.GroupVersionResource {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]schema.GroupVersionResource, 0, len(m.informers))
	for gvr := range m.informers {
		out = append(out, gvr)
	}
	return out
}

// removeAll cancels every informer. Called on Start's ctx cancellation.
func (m *DynamicInformerManager) removeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for gvr, h := range m.informers {
		h.cancel()
		delete(m.informers, gvr)
	}
	m.metrics.setActive(0)
}

// DynamicMetrics holds the dynamic-informer counters surfaced on
// /metrics: the live active-informer gauge and a cumulative error
// count. Safe for concurrent use.
type DynamicMetrics struct {
	active atomic.Int64
	errors atomic.Uint64
}

// NewDynamicMetrics returns a zeroed metrics sink.
func NewDynamicMetrics() *DynamicMetrics { return &DynamicMetrics{} }

func (m *DynamicMetrics) setActive(n int) { m.active.Store(int64(n)) }
func (m *DynamicMetrics) incErrors()      { m.errors.Add(1) }

// DynamicMetricsSnapshot is an immutable read of the counters.
type DynamicMetricsSnapshot struct {
	Active int64
	Errors uint64
}

// Snapshot reads the current counter values.
func (m *DynamicMetrics) Snapshot() DynamicMetricsSnapshot {
	return DynamicMetricsSnapshot{
		Active: m.active.Load(),
		Errors: m.errors.Load(),
	}
}
