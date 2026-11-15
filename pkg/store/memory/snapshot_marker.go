// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

var _ graph.SnapshotMarkerRecorder = (*Store)(nil)

func (s *Store) RecordSnapshotMarker(ctx context.Context, cluster string, trigger graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	if !graph.ValidSnapshotMarker(cluster, trigger) {
		return graph.SnapshotMeta{}, graph.ErrHistoryEventQuery
	}
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return graph.SnapshotMeta{}, err
	}
	for !s.mu.TryLock() {
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return graph.SnapshotMeta{}, ctx.Err()
		case <-timer.C:
		}
	}
	defer s.mu.Unlock()
	marker := graph.SnapshotMeta{ClusterID: cluster, Trigger: trigger}
	for _, r := range s.resources {
		if err := ctx.Err(); err != nil {
			return graph.SnapshotMeta{}, err
		}
		if r.ClusterID == cluster {
			marker.ResourceCount++
		}
	}
	for from, peers := range s.outgoing {
		if err := ctx.Err(); err != nil {
			return graph.SnapshotMeta{}, err
		}
		f, ok := s.resources[from]
		if !ok || f.ClusterID != cluster {
			continue
		}
		for _, e := range peers {
			if err := ctx.Err(); err != nil {
				return graph.SnapshotMeta{}, err
			}
			if to, ok := s.resources[e.To]; ok && to.ClusterID == cluster {
				marker.EdgeCount++
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return graph.SnapshotMeta{}, err
	}
	marker.DurationMS = time.Since(start).Milliseconds()
	return s.appendSnapshotMetaLocked(marker), nil
}

// Caller holds the write lock. Legacy append retains its existing semantics.
func (s *Store) appendSnapshotMetaLocked(m graph.SnapshotMeta) graph.SnapshotMeta {
	s.eventSeq++
	m.ID = s.eventSeq
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	s.snapshotMeta = append(s.snapshotMeta, m)
	if len(s.snapshotMeta) > maxMemoryEvents {
		s.snapshotMeta = append([]graph.SnapshotMeta(nil), s.snapshotMeta[len(s.snapshotMeta)-maxMemoryEvents:]...)
	}
	return m
}
