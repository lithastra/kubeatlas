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

var _ graph.SnapshotMarkerRecorder = (*Store)(nil)

// One statement observes a single MVCC snapshot, counts only scalar metadata
// in the selected graph, and persists the marker. No graph payload crosses the
// wire and no global count is relabelled as a member. LIMIT would miscount;
// scans remain deadline-bound, with no claim of indexed edge-count performance.
const recordSnapshotMarkerSQL = `WITH counts AS MATERIALIZED (
 SELECT (SELECT count(*) FROM public.resources WHERE cluster_id=$1) AS resources,
 (SELECT count(*) FROM public.edges e
  JOIN public.resources f ON f.id=e.from_id
  JOIN public.resources t ON t.id=e.to_id
  WHERE f.cluster_id=$1 AND t.cluster_id=$1) AS edges
)
INSERT INTO public.snapshot_meta(ts,cluster_id,resource_count,edge_count,duration_ms,trigger)
SELECT statement_timestamp(),$1,resources,edges,
 GREATEST(0,FLOOR(EXTRACT(EPOCH FROM (clock_timestamp()-statement_timestamp()))*1000))::bigint,$2
FROM counts
RETURNING id,ts,cluster_id,resource_count,edge_count,duration_ms,trigger`

func (s *Store) RecordSnapshotMarker(ctx context.Context, cluster string, trigger graph.SnapshotTrigger) (marker graph.SnapshotMeta, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	defer func() {
		if err := ctx.Err(); err != nil {
			marker = graph.SnapshotMeta{}
			resultErr = err
		}
	}()
	if !graph.ValidSnapshotMarker(cluster, trigger) {
		return graph.SnapshotMeta{}, graph.ErrHistoryEventQuery
	}
	if err := ctx.Err(); err != nil {
		return graph.SnapshotMeta{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return graph.SnapshotMeta{}, fmt.Errorf("postgres.RecordSnapshotMarker: begin: %w", err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return graph.SnapshotMeta{}, fmt.Errorf("postgres.RecordSnapshotMarker: timeout: %w", err)
	}
	var recordedTrigger string
	if err := tx.QueryRow(ctx, recordSnapshotMarkerSQL, cluster, string(trigger)).Scan(&marker.ID, &marker.Timestamp, &marker.ClusterID, &marker.ResourceCount, &marker.EdgeCount, &marker.DurationMS, &recordedTrigger); err != nil {
		return graph.SnapshotMeta{}, fmt.Errorf("postgres.RecordSnapshotMarker: record: %w", err)
	}
	marker.Trigger = graph.SnapshotTrigger(recordedTrigger)
	if err := ctx.Err(); err != nil {
		return graph.SnapshotMeta{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return graph.SnapshotMeta{}, fmt.Errorf("postgres.RecordSnapshotMarker: commit outcome may be unknown: %w", err)
	}
	return marker, nil
}
