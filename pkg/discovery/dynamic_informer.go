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
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"

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
	baseCtx   context.Context
	started   bool
	informers map[schema.GroupVersionResource]*dynamicHandle
}

// dynamicHandle pairs an informer's cancel func with its sync check.
type dynamicHandle struct {
	cancel   context.CancelFunc
	synced   cache.InformerSynced
	coverage *operations.CoverageSession
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
// client. Start binds the parent context; each registration owns a fresh
// informer and a child context, so removed registrations can be added again.
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
	if m.started {
		m.mu.Unlock()
		return errors.New("dynamic informer manager already started")
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
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
	return m.AddObserved(gvr, nil, func(context.Context, *operations.CoverageSession) cache.ResourceEventHandler { return handler })
}

// AddObserved creates a handler and list/watch adapter using the SAME
// registration token and lifetime. Idempotent Add never replaces callbacks or
// resets evidence. Evidence capacity does not disable collection; a nil token
// is handed to build and the source keeps its limited-inventory signal.
// build runs under the registration lock and must not reenter the manager.
func (m *DynamicInformerManager) AddObserved(gvr schema.GroupVersionResource, source *operations.CoverageSession, build func(context.Context, *operations.CoverageSession) cache.ResourceEventHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started || m.baseCtx.Err() != nil {
		return ErrManagerNotStarted
	}
	if isCoreSecretGVR(gvr) || gvr.Version == "" || gvr.Resource == "" || build == nil {
		return errors.New("invalid dynamic informer registration")
	}
	if _, exists := m.informers[gvr]; exists {
		return nil
	}

	var coverage *operations.CoverageSession
	if source != nil {
		var err error
		coverage, err = source.BeginType(gvr, time.Now())
		if err != nil {
			m.metrics.incErrors()
		}
	}
	informerCtx, cancel := context.WithCancel(m.baseCtx)
	handler := build(informerCtx, coverage)
	if handler == nil {
		cancel()
		coverage.Stop(time.Now())
		return errors.New("dynamic informer handler is nil")
	}
	// A cached stopped informer cannot restart. Each registration owns a fresh
	// factory/informer and captured token, including after Remove + Add.
	factory := dynamicinformer.NewDynamicSharedInformerFactory(ObserveCoverageClient(m.dyn, coverage), DefaultResyncPeriod)
	informer := factory.ForResource(gvr).Informer()
	registration, err := informer.AddEventHandler(handler)
	if err != nil {
		cancel()
		coverage.Stop(time.Now())
		m.metrics.incErrors()
		return fmt.Errorf("dynamic informer add %s: %w", gvr.String(), err)
	}

	go informer.Run(informerCtx.Done())
	if coverage != nil {
		go func() {
			if cache.WaitForCacheSync(informerCtx.Done(), registration.HasSynced) {
				coverage.InitialDeliveryComplete(gvr)
			}
		}()
	}

	m.informers[gvr] = &dynamicHandle{cancel: cancel, synced: registration.HasSynced, coverage: coverage}
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
	h.coverage.Stop(time.Now())
	m.metrics.setActive(len(m.informers))
	m.logger.Info("dynamic informer removed",
		"gvr", fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource))
}

// Started reports whether Start has bound the base context, i.e.
// whether Add will be accepted. Callers that register informers from a
// separate goroutine use it to wait out the brief startup window.
func (m *DynamicInformerManager) Started() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.started
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
		h.coverage.Stop(time.Now())
		delete(m.informers, gvr)
	}
	m.started = false
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
