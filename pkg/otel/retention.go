// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"log/slog"
	"time"
)

// SpanDeleter is the store-side seam the SpanRetainer needs.
// *postgres.Store satisfies it.
type SpanDeleter interface {
	DeleteOldSpans(ctx context.Context, cutoff time.Time) (int64, error)
}

// SpanRetainer periodically deletes otel_spans rows older than the
// retention window so the span table does not grow without bound. It
// mirrors snapshot.Retainer: an immediate sweep on Start, then once
// per interval, terminated only by ctx cancellation (no Stop method).
type SpanRetainer struct {
	deleter   SpanDeleter
	retention time.Duration
	interval  time.Duration
	metrics   *Metrics
}

// NewSpanRetainer builds a SpanRetainer. A non-positive retention
// falls back to DefaultRetention; a nil Metrics gets a fresh set.
func NewSpanRetainer(d SpanDeleter, retention time.Duration, m *Metrics) *SpanRetainer {
	if retention <= 0 {
		retention = DefaultRetention
	}
	if m == nil {
		m = NewMetrics()
	}
	return &SpanRetainer{
		deleter:   d,
		retention: retention,
		interval:  defaultPruneInterval,
		metrics:   m,
	}
}

// Start runs the prune loop until ctx is cancelled. It blocks — run
// it in a goroutine. A prune fires immediately on Start (clearing any
// backlog from while the process was down), then once per interval.
func (r *SpanRetainer) Start(ctx context.Context) {
	r.pruneOnce(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.pruneOnce(ctx)
		}
	}
}

// pruneOnce deletes every span received before now-retention. A
// failure is logged, not fatal — the next tick retries, so a
// transient store outage never crashes the process.
func (r *SpanRetainer) pruneOnce(ctx context.Context) {
	cutoff := time.Now().Add(-r.retention)
	n, err := r.deleter.DeleteOldSpans(ctx, cutoff)
	if err != nil {
		slog.Warn("otel retention: prune failed", "cutoff", cutoff, "err", err)
		return
	}
	if n > 0 {
		r.metrics.addRetentionDeleted(uint64(n))
		slog.Info("otel retention: pruned expired spans",
			"deleted", n, "cutoff", cutoff, "retention", r.retention)
	}
}
