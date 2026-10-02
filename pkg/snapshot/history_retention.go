// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

const historyRetentionInterval = 5 * time.Minute

var ErrHistoryRetentionStarted = errors.New("snapshot: history retention observer already started")

// HistoryRetentionObserver owns one immutable scope/configuration for one
// process lifetime. Only Start queries storage; request-side reads load a fixed
// size, owned cache value. Replacing the observer starts with unknown evidence.
type HistoryRetentionObserver struct {
	reader    graph.HistoryBoundsReader
	cluster   string
	retention time.Duration
	started   atomic.Bool
	stopped   atomic.Bool
	evidence  atomic.Pointer[operations.HistoryRetentionEvidence]
}

func NewHistoryRetentionObserver(reader graph.HistoryBoundsReader, cluster string, retention time.Duration) *HistoryRetentionObserver {
	if retention == 0 {
		retention = DefaultRetention
	}
	return &HistoryRetentionObserver{reader: reader, cluster: cluster, retention: retention}
}

func (o *HistoryRetentionObserver) Start(ctx context.Context) error {
	now := time.Now()
	if o.retention <= 0 || (graph.HistoryBoundsQuery{ClusterID: o.cluster, From: now.Add(-o.retention), To: now}).Validate() != nil {
		return ErrAnalysisAvailabilityConfig
	}
	if !o.started.CompareAndSwap(false, true) {
		return ErrHistoryRetentionStarted
	}
	defer o.stopped.Store(true)
	ticker := time.NewTicker(historyRetentionInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		o.observe(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (o *HistoryRetentionObserver) observe(ctx context.Context) {
	e := operations.HistoryRetentionEvidence{State: operations.RetentionUnsupported, Bounds: graph.HistoryBounds{ClusterID: o.cluster}}
	if o.reader != nil {
		ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
		defer cancel()
		to := time.Now()
		q := graph.HistoryBoundsQuery{ClusterID: o.cluster, From: to.Add(-o.retention), To: to}
		bounds, err := o.reader.ReadHistoryBounds(ctx, q)
		e.State = operations.RetentionFailed
		e.CheckedAt = time.Now()
		e.Bounds.From, e.Bounds.To = q.From, q.To
		if err == nil && ctx.Err() == nil && bounds.ValidFor(q) {
			e.State, e.Bounds = operations.RetentionObserved, bounds
		}
	}
	o.evidence.Store(&e)
}

func (o *HistoryRetentionObserver) SnapshotRetention(ctx context.Context, cluster string) (operations.HistoryRetentionEvidence, error) {
	if err := ctx.Err(); err != nil {
		return operations.HistoryRetentionEvidence{}, err
	}
	if cluster != o.cluster {
		return operations.HistoryRetentionEvidence{}, ErrAnalysisAvailabilityScope
	}
	e := operations.HistoryRetentionEvidence{State: operations.RetentionNotQueried, Bounds: graph.HistoryBounds{ClusterID: cluster}}
	if cached := o.evidence.Load(); cached != nil {
		e = *cached
	}
	e.Stopped = o.stopped.Load()
	e.Stale = !e.CheckedAt.IsZero() && time.Since(e.CheckedAt) >= operations.HistoryRetentionMaxAge
	return e, nil
}
