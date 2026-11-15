// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

var _ graph.HistoryMarkerReader = (*Store)(nil)

// Equality filtering precedes the bounded result; no global marker read or
// data column is needed. Existing timestamp indexes do not bound scanned rows.
const historyMarkersSQL = `SELECT id, ts, cluster_id, resource_count, edge_count, duration_ms, trigger
 FROM public.snapshot_meta WHERE cluster_id=$1 ORDER BY ts DESC,id DESC LIMIT $2`

func (s *Store) ReadHistoryMarkers(ctx context.Context, cluster string) (markers []graph.SnapshotMeta, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	defer func() {
		if err := ctx.Err(); err != nil {
			markers = nil
			resultErr = err
		}
	}()
	b, err := graph.NewHistoryMarkerBuilder(cluster)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("postgres.ReadHistoryMarkers: begin: %w", err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return nil, fmt.Errorf("postgres.ReadHistoryMarkers: timeout: %w", err)
	}
	rows, err := tx.Query(ctx, historyMarkersSQL, cluster, graph.MaxHistoryEvents+1)
	if err != nil {
		return nil, fmt.Errorf("postgres.ReadHistoryMarkers: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m graph.SnapshotMeta
		var trigger string
		if err := rows.Scan(&m.ID, &m.Timestamp, &m.ClusterID, &m.ResourceCount, &m.EdgeCount, &m.DurationMS, &trigger); err != nil {
			return nil, fmt.Errorf("postgres.ReadHistoryMarkers: scan: %w", err)
		}
		m.Trigger = graph.SnapshotTrigger(trigger)
		if err := b.Add(ctx, m); err != nil {
			return nil, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres.ReadHistoryMarkers: rows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres.ReadHistoryMarkers: finish: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b.Markers, nil
}
