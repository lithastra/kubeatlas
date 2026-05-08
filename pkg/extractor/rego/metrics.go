// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import "sync/atomic"

// Metrics is the lock-free counter set the engine and cache update
// during normal evaluation. Snapshots are read by the api package's
// /metrics handler and turned into Prometheus exposition lines —
// matching the Phase 1 pattern of hand-rolled metrics rather than
// pulling in the prometheus/client_golang registry just for four
// counters.
//
// Prometheus names (when exposed by api):
//
//	kubeatlas_rego_cache_hits_total
//	kubeatlas_rego_cache_misses_total
//	kubeatlas_rego_eval_timeout_total
//	kubeatlas_rego_eval_panic_total
type Metrics struct {
	cacheHits    atomic.Uint64
	cacheMisses  atomic.Uint64
	evalTimeouts atomic.Uint64
	evalPanics   atomic.Uint64
}

// NewMetrics returns a zero-valued counter set.
func NewMetrics() *Metrics { return &Metrics{} }

// MetricsSnapshot is the read-side shape returned by Snapshot. Plain
// uint64 fields keep the api-side serialization free of atomic
// reads, which would compound in a tight loop over kinds/labels.
type MetricsSnapshot struct {
	CacheHits    uint64
	CacheMisses  uint64
	EvalTimeouts uint64
	EvalPanics   uint64
}

// Snapshot copies the current values without locking. Each Add /
// Snapshot pair is atomic individually; tearing across counters in
// the same snapshot is fine for /metrics where the scraper sees a
// consistent-enough view (Prometheus model is per-counter).
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		CacheHits:    m.cacheHits.Load(),
		CacheMisses:  m.cacheMisses.Load(),
		EvalTimeouts: m.evalTimeouts.Load(),
		EvalPanics:   m.evalPanics.Load(),
	}
}

// IncCacheHit / IncCacheMiss / IncEvalTimeout / IncEvalPanic are the
// hot-path mutators. Each is one atomic add — cheap enough to call
// from a per-resource evaluation loop without measurable overhead.

func (m *Metrics) IncCacheHit()    { m.cacheHits.Add(1) }
func (m *Metrics) IncCacheMiss()   { m.cacheMisses.Add(1) }
func (m *Metrics) IncEvalTimeout() { m.evalTimeouts.Add(1) }
func (m *Metrics) IncEvalPanic()   { m.evalPanics.Add(1) }
