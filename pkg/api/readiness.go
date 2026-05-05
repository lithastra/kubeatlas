package api

import "sync/atomic"

// ReadinessGate is a flag the informer flips once its initial cache
// sync completes. /readyz returns 503 until then so a Kubernetes
// liveness/readiness probe can keep traffic off the Pod during the
// "informer is still listing" window.
type ReadinessGate struct {
	ready atomic.Bool
}

// NewReadinessGate returns a gate that starts in the not-ready state.
func NewReadinessGate() *ReadinessGate {
	return &ReadinessGate{}
}

// MarkReady is called by whoever owns "ready" semantics — typically
// the informer manager once WaitForCacheSync returns successfully.
func (g *ReadinessGate) MarkReady() { g.ready.Store(true) }

// IsReady reports the current readiness state.
func (g *ReadinessGate) IsReady() bool { return g.ready.Load() }
