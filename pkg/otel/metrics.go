// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package otel

import "sync/atomic"

// Metrics is the lock-free counter set the receiver and retention
// worker update. It mirrors snapshot.Metrics — the hand-rolled
// pattern KubeAtlas uses instead of prometheus/client_golang for a
// handful of counters.
//
// Prometheus names (when exposed by the api package's /metrics
// handler):
//
//	kubeatlas_otel_received_total          spans received over OTLP gRPC
//	kubeatlas_otel_dropped_total           spans dropped because the queue was full
//	kubeatlas_otel_written_total           spans durably written to PostgreSQL
//	kubeatlas_otel_retention_deleted_total spans deleted by the hourly retention sweep
type Metrics struct {
	received         atomic.Uint64
	dropped          atomic.Uint64
	written          atomic.Uint64
	retentionDeleted atomic.Uint64
}

// NewMetrics returns a zero-valued counter set.
func NewMetrics() *Metrics { return &Metrics{} }

// MetricsSnapshot is the read-side shape returned by Snapshot — plain
// uint64s so the /metrics serializer does no atomic reads.
type MetricsSnapshot struct {
	// Received counts every span seen on the Export path, whether or
	// not it was later enqueued (so Received == Dropped + Written in
	// steady state, modulo in-flight batches).
	Received uint64
	// Dropped counts spans shed because the queue was full — the
	// backpressure valve that protects the core graph path.
	Dropped uint64
	// Written counts spans durably persisted to PostgreSQL.
	Written uint64
	// RetentionDeleted counts spans removed by the hourly sweep.
	RetentionDeleted uint64
}

// Snapshot copies the current values without locking.
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		Received:         m.received.Load(),
		Dropped:          m.dropped.Load(),
		Written:          m.written.Load(),
		RetentionDeleted: m.retentionDeleted.Load(),
	}
}

func (m *Metrics) addReceived(n uint64)         { m.received.Add(n) }
func (m *Metrics) addDropped(n uint64)          { m.dropped.Add(n) }
func (m *Metrics) addWritten(n uint64)          { m.written.Add(n) }
func (m *Metrics) addRetentionDeleted(n uint64) { m.retentionDeleted.Add(n) }
