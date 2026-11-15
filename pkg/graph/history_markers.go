// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"strings"
)

// HistoryMarkerReader is an explicit optional capability; GraphStore v2 is
// unchanged. Callers authorize before reading. Empty cluster selects only
// unlabelled rows with unknown legacy provenance, never all members. Wrappers
// must opt in; no fallback to ListSnapshotMeta is allowed. Results are newest
// first (Timestamp, ID), capped at MaxHistoryEvents and MaxHistoryBytes. An
// overflow/error returns no partial list. Markers do not prove recovery points.
type HistoryMarkerReader interface {
	ReadHistoryMarkers(context.Context, string) ([]SnapshotMeta, error)
}

func ValidHistoryCluster(cluster string) bool {
	return len(cluster) <= 253 && !strings.ContainsAny(cluster, "/:\x00")
}

// HistoryMarkerBuilder validates scoped scalar metadata using conservative
// JSON charges shared by both stores. Discard the builder after any error.
type HistoryMarkerBuilder struct {
	Markers   []SnapshotMeta
	cluster   string
	remaining int
}

func NewHistoryMarkerBuilder(cluster string) (*HistoryMarkerBuilder, error) {
	if !ValidHistoryCluster(cluster) {
		return nil, ErrHistoryEventQuery
	}
	return &HistoryMarkerBuilder{Markers: []SnapshotMeta{}, cluster: cluster, remaining: MaxHistoryBytes}, nil
}
func (b *HistoryMarkerBuilder) Add(ctx context.Context, m SnapshotMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.ClusterID != b.cluster {
		return ErrHistoryEventScope
	}
	if len(b.Markers) >= MaxHistoryEvents || b.remaining < 512 {
		return ErrHistoryEventLimit
	}
	b.remaining -= 512
	for _, s := range []string{m.ClusterID, string(m.Trigger)} {
		if len(s) > b.remaining/6 {
			return ErrHistoryEventLimit
		}
		b.remaining -= 6 * len(s)
	}
	b.Markers = append(b.Markers, m)
	return nil
}
