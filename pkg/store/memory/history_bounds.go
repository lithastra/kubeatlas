// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

var _ graph.HistoryBoundsReader = (*Store)(nil)

func (s *Store) ReadHistoryBounds(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	if err := q.Validate(); err != nil {
		return graph.HistoryBounds{}, err
	}
	if err := s.lockProjectionRead(ctx); err != nil {
		return graph.HistoryBounds{}, err
	}
	defer s.mu.RUnlock()
	out := graph.HistoryBounds{ClusterID: q.ClusterID, From: q.From, To: q.To}
	for _, e := range s.events {
		if err := ctx.Err(); err != nil {
			return graph.HistoryBounds{}, err
		}
		if e.ClusterID != q.ClusterID || e.Kind == "Secret" || e.Timestamp.Before(q.From) || e.Timestamp.After(q.To) {
			continue
		}
		if out.FirstEventAt.IsZero() || e.Timestamp.Before(out.FirstEventAt) {
			out.FirstEventAt = e.Timestamp
		}
		if e.Timestamp.After(out.LastEventAt) {
			out.LastEventAt = e.Timestamp
		}
	}
	for _, m := range s.snapshotMeta {
		if err := ctx.Err(); err != nil {
			return graph.HistoryBounds{}, err
		}
		if m.ClusterID == q.ClusterID && !m.Timestamp.Before(q.From) && !m.Timestamp.After(q.To) && m.Timestamp.After(out.LatestMarkerAt) {
			out.LatestMarkerAt = m.Timestamp
		}
	}
	if err := ctx.Err(); err != nil {
		return graph.HistoryBounds{}, err
	}
	return out, nil
}
