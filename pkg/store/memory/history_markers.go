// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

var _ graph.HistoryMarkerReader = (*Store)(nil)

func (s *Store) ReadHistoryMarkers(ctx context.Context, cluster string) ([]graph.SnapshotMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	b, err := graph.NewHistoryMarkerBuilder(cluster)
	if err != nil {
		return nil, err
	}
	if err := s.lockProjectionRead(ctx); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	for _, m := range s.snapshotMeta {
		if m.ClusterID != cluster {
			continue
		}
		if err := b.Add(ctx, m); err != nil {
			return nil, err
		}
	}
	sort.Slice(b.Markers, func(i, j int) bool {
		if !b.Markers[i].Timestamp.Equal(b.Markers[j].Timestamp) {
			return b.Markers[i].Timestamp.After(b.Markers[j].Timestamp)
		}
		return b.Markers[i].ID > b.Markers[j].ID
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b.Markers, nil
}
