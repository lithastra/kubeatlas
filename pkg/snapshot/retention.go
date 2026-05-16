// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// DefaultRetention is how long resource_events rows are kept when
// snapshots.retention is unset. Matches the Helm chart default.
const DefaultRetention = 7 * 24 * time.Hour

// defaultPruneInterval is how often the Retainer sweeps expired
// events. Hourly is frequent enough to keep the table from
// ballooning between runs, cheap enough to be invisible.
const defaultPruneInterval = time.Hour

// Pruner is the store-side seam the Retainer needs.
// graph.GraphStore satisfies it.
type Pruner interface {
	PruneEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// Retainer periodically deletes resource_events rows older than the
// retention window, so the F-111 event stream does not grow without
// bound. It is the in-process half of retention; the periodic
// full-sync snapshot is driven separately by the CronJob trigger.
type Retainer struct {
	pruner    Pruner
	retention time.Duration
	interval  time.Duration
}

// NewRetainer builds a Retainer. A non-positive retention falls back
// to DefaultRetention.
func NewRetainer(p Pruner, retention time.Duration) *Retainer {
	if retention <= 0 {
		retention = DefaultRetention
	}
	return &Retainer{
		pruner:    p,
		retention: retention,
		interval:  defaultPruneInterval,
	}
}

// Start runs the prune loop until ctx is cancelled. It blocks — run
// it in a goroutine.
//
// A prune fires immediately on Start (clearing any backlog that
// accumulated while the process was down), then once per interval.
func (r *Retainer) Start(ctx context.Context) {
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

// pruneOnce computes the cutoff and asks the store to delete every
// event older than it. A failure is logged, not fatal — the next
// tick retries (a transient store outage must not crash the
// process).
func (r *Retainer) pruneOnce(ctx context.Context) {
	cutoff := time.Now().Add(-r.retention)
	n, err := r.pruner.PruneEventsBefore(ctx, cutoff)
	if err != nil {
		slog.Warn("snapshot retention: prune failed", "cutoff", cutoff, "err", err)
		return
	}
	if n > 0 {
		slog.Info("snapshot retention: pruned expired events",
			"deleted", n, "cutoff", cutoff, "retention", r.retention)
	}
}

// ParseRetention converts a Helm-style retention string into a
// Duration. It accepts a plain day suffix ("7d", "30d") on top of
// everything time.ParseDuration handles ("24h", "90m") — Go's
// parser has no day unit, but the operator-facing value is "7d".
//
// An empty string returns DefaultRetention. A malformed string is
// an error so a typo'd Helm value fails loudly rather than
// silently defaulting.
func ParseRetention(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultRetention, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid retention %q: day count must be a non-negative integer", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid retention %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid retention %q: must not be negative", s)
	}
	return d, nil
}
