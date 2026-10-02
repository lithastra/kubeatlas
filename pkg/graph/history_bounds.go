// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"time"
)

// HistoryBoundsReader is an optional metadata-only capability. Callers must
// authorize its exact cluster before reading; wrappers must explicitly opt in.
// Empty ClusterID selects only unlabelled records, whose legacy provenance is
// unknown. No fallback to global ListEvents/ListSnapshotMeta is permitted.
// One consistent read returns three timestamps, never payloads or counts.
type HistoryBoundsReader interface {
	ReadHistoryBounds(context.Context, HistoryBoundsQuery) (HistoryBounds, error)
}

type HistoryBoundsQuery struct {
	ClusterID string
	From      time.Time
	To        time.Time
}

func (q HistoryBoundsQuery) Validate() error {
	_, err := (HistoryEventQuery{ClusterID: q.ClusterID, From: q.From, To: q.To}).Normalize()
	return err
}

// Bounds describe only retained non-Secret events and snapshot-marker records
// in the inclusive query window. Zero times mean no matching record, not no
// past changes. First/last times do not prove a continuously retained interval;
// a marker does not establish a restorable snapshot, backup, or drained writer.
type HistoryBounds struct {
	ClusterID      string    `json:"clusterId"`
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
	FirstEventAt   time.Time `json:"firstEventAt"`
	LastEventAt    time.Time `json:"lastEventAt"`
	LatestMarkerAt time.Time `json:"latestMarkerAt"`
}

func (b HistoryBounds) ValidFor(q HistoryBoundsQuery) bool {
	if q.Validate() != nil || b.ClusterID != q.ClusterID || !b.From.Equal(q.From) || !b.To.Equal(q.To) ||
		b.FirstEventAt.IsZero() != b.LastEventAt.IsZero() || b.FirstEventAt.After(b.LastEventAt) {
		return false
	}
	for _, at := range []time.Time{b.FirstEventAt, b.LastEventAt, b.LatestMarkerAt} {
		if !at.IsZero() && (at.Before(q.From) || at.After(q.To)) {
			return false
		}
	}
	return true
}
