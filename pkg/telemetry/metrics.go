// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "sync/atomic"

// Metrics tracks telemetry send outcomes for /metrics. Send failures
// are surfaced (not swallowed) so operators can see that the
// fire-and-forget sender is failing without it affecting the main path.
type Metrics struct {
	sent   atomic.Uint64
	errors atomic.Uint64
}

func NewMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) incSent()   { m.sent.Add(1) }
func (m *Metrics) incErrors() { m.errors.Add(1) }

// MetricsSnapshot is an immutable read of the counters.
type MetricsSnapshot struct {
	Sent   uint64
	Errors uint64
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{Sent: m.sent.Load(), Errors: m.errors.Load()}
}
