// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package snapshot holds the F-111 snapshot writer: the async
// pipeline that drains informer events into the Tier 2
// resource_events stream without ever blocking the informer's hot
// path.
package snapshot

import "sync/atomic"

// Metrics is the lock-free counter set the Writer updates as it
// processes events. It mirrors pkg/extractor/rego.Metrics — the
// hand-rolled pattern KubeAtlas uses instead of pulling in
// prometheus/client_golang for a handful of counters.
//
// Prometheus names (when exposed by the api package's /metrics
// handler):
//
//	kubeatlas_snapshot_events_processed_total
//	kubeatlas_snapshot_write_failed_total
//	kubeatlas_snapshot_queue_drop_total
//
// Queue depth is a live gauge read from the channel length at
// scrape time (Writer.QueueDepth), not a counter, so it is not a
// field here.
type Metrics struct {
	eventsProcessed atomic.Uint64
	writeFailed     atomic.Uint64
	queueDropped    atomic.Uint64
}

// NewMetrics returns a zero-valued counter set.
func NewMetrics() *Metrics { return &Metrics{} }

// MetricsSnapshot is the read-side shape returned by Snapshot —
// plain uint64s so the /metrics serializer does no atomic reads.
type MetricsSnapshot struct {
	// EventsProcessed counts events durably written to the store.
	EventsProcessed uint64
	// WriteFailed counts events dropped after the per-event retry
	// budget was exhausted (the store stayed unavailable).
	WriteFailed uint64
	// QueueDropped counts events dropped at Enqueue because the
	// queue was full — the informer-protection backpressure valve.
	QueueDropped uint64
}

// Snapshot copies the current values without locking.
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		EventsProcessed: m.eventsProcessed.Load(),
		WriteFailed:     m.writeFailed.Load(),
		QueueDropped:    m.queueDropped.Load(),
	}
}

func (m *Metrics) incProcessed()    { m.eventsProcessed.Add(1) }
func (m *Metrics) incWriteFailed()  { m.writeFailed.Add(1) }
func (m *Metrics) incQueueDropped() { m.queueDropped.Add(1) }
