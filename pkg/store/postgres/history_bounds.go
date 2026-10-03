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

var _ graph.HistoryBoundsReader = (*Store)(nil)

// Both aggregates share one statement snapshot. Scope/time/Secret filters
// apply in SQL, before any result crosses the wire. Output size is constant;
// scanned work is bounded by the deadline, not by a claimed row-scan limit.
const historyBoundsSQL = `
SELECT events.first_at, events.last_at, markers.latest_at
FROM (
  SELECT MIN(ts) AS first_at, MAX(ts) AS last_at
  FROM public.resource_events
  WHERE cluster_id = $1 AND ts >= $2 AND ts <= $3 AND kind <> 'Secret'
) AS events
CROSS JOIN (
  SELECT MAX(ts) AS latest_at
  FROM public.snapshot_meta
  WHERE cluster_id = $1 AND ts >= $2 AND ts <= $3
) AS markers`

func (s *Store) ReadHistoryBounds(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return graph.HistoryBounds{}, err
	}
	if err := q.Validate(); err != nil {
		return graph.HistoryBounds{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		if ctx.Err() != nil {
			return graph.HistoryBounds{}, ctx.Err()
		}
		return graph.HistoryBounds{}, fmt.Errorf("postgres.ReadHistoryBounds: begin: %w", err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		if ctx.Err() != nil {
			return graph.HistoryBounds{}, ctx.Err()
		}
		return graph.HistoryBounds{}, fmt.Errorf("postgres.ReadHistoryBounds: timeout: %w", err)
	}
	var first, last, marker *time.Time
	// PostgreSQL timestamps have microsecond precision. Round the lower bound
	// inward so pgx truncation cannot include a row just outside the window.
	from := q.From.Truncate(time.Microsecond)
	if from.Before(q.From) {
		from = from.Add(time.Microsecond)
	}
	if err := tx.QueryRow(ctx, historyBoundsSQL, q.ClusterID, from, q.To.Truncate(time.Microsecond)).Scan(&first, &last, &marker); err != nil {
		if ctx.Err() != nil {
			return graph.HistoryBounds{}, ctx.Err()
		}
		return graph.HistoryBounds{}, fmt.Errorf("postgres.ReadHistoryBounds: query: %w", err)
	}
	out := graph.HistoryBounds{ClusterID: q.ClusterID, From: q.From, To: q.To}
	if first != nil {
		out.FirstEventAt = *first
	}
	if last != nil {
		out.LastEventAt = *last
	}
	if marker != nil {
		out.LatestMarkerAt = *marker
	}
	if !out.ValidFor(q) {
		return graph.HistoryBounds{}, graph.ErrHistoryEventScope
	}
	if err := tx.Commit(ctx); err != nil {
		if ctx.Err() != nil {
			return graph.HistoryBounds{}, ctx.Err()
		}
		return graph.HistoryBounds{}, fmt.Errorf("postgres.ReadHistoryBounds: finish: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return graph.HistoryBounds{}, err
	}
	return out, nil
}
