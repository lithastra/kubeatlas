// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

const HistoryRetentionMaxAge = 10 * time.Minute

type HistoryRetentionState string

const (
	RetentionNotQueried  HistoryRetentionState = "not_queried"
	RetentionObserved    HistoryRetentionState = "observed"
	RetentionFailed      HistoryRetentionState = "failed"
	RetentionUnsupported HistoryRetentionState = "unsupported"
)

// Retention evidence is an expiring background observation of stored metadata.
// Even observed, fresh bounds establish no continuity or recovery guarantee.
type HistoryRetentionEvidence struct {
	State     HistoryRetentionState `json:"state"`
	CheckedAt time.Time             `json:"checkedAt"`
	Stale     bool                  `json:"stale"`
	Stopped   bool                  `json:"stopped"`
	Bounds    graph.HistoryBounds   `json:"bounds"`
}

// Validate also protects callers of injectable evidence providers. Failed
// checks must not carry previous success timestamps as current evidence.
func (e HistoryRetentionEvidence) ValidFor(cluster string, retention time.Duration, captured time.Time) bool {
	if e.Bounds.ClusterID != cluster || retention <= 0 || e.CheckedAt.After(captured) ||
		e.Stale != (!e.CheckedAt.IsZero() && captured.Sub(e.CheckedAt) >= HistoryRetentionMaxAge) {
		return false
	}
	noRecords := e.Bounds.FirstEventAt.IsZero() && e.Bounds.LastEventAt.IsZero() && e.Bounds.LatestMarkerAt.IsZero()
	switch e.State {
	case RetentionNotQueried, RetentionUnsupported:
		return e.CheckedAt.IsZero() && e.Bounds.From.IsZero() && e.Bounds.To.IsZero() && noRecords
	case RetentionObserved, RetentionFailed:
		q := graph.HistoryBoundsQuery{ClusterID: cluster, From: e.Bounds.From, To: e.Bounds.To}
		return !e.CheckedAt.IsZero() && !q.To.After(e.CheckedAt) && q.To.Sub(q.From) == retention &&
			e.Bounds.ValidFor(q) && (e.State == RetentionObserved || noRecords) &&
			(e.State != RetentionObserved || e.CheckedAt.Sub(q.To) <= graph.HistoryReadTimeout)
	default:
		return false
	}
}
