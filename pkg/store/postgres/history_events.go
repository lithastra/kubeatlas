// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

var _ graph.HistoryEventReader = (*Store)(nil)

// Cluster/namespace/time/Secret filtering happens before LIMIT or any returned
// count. Neither legacy Data nor other clusters' metadata crosses the wire.
// The JSON guard bounds each returned row before decoding; the builder also
// bounds aggregate metadata. Existing time indexes help narrow the window, but
// LIMIT does not bound scanned rows. No indexed cluster-scan performance claim
// is made; a new index/migration needs a separate plan and benchmark review.
const historyEventsSQL = `
SELECT CASE WHEN octet_length(projected::text) <= $6 THEN projected ELSE NULL END
FROM (
  SELECT jsonb_build_object(
    'id', id, 'ts', ts, 'clusterId', cluster_id, 'namespace', namespace,
    'kind', kind, 'uid', uid, 'name', name, 'eventType', event_type,
    'resourceVersion', resource_version) AS projected, ts, id
  FROM public.resource_events
  WHERE cluster_id = $1 AND ($2::text = '' OR namespace = $2)
    AND ts >= $3 AND ts <= $4 AND kind <> 'Secret'
  ORDER BY ts ASC, id ASC LIMIT $5
) AS scoped
ORDER BY ts ASC, id ASC`

func (s *Store) ReadHistoryEvents(ctx context.Context, query graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return graph.HistoryEventPage{}, err
	}
	q, err := query.Normalize()
	if err != nil {
		return graph.HistoryEventPage{}, err
	}
	b, err := graph.NewHistoryEventPageBuilder(q)
	if err != nil {
		return graph.HistoryEventPage{}, err
	}
	// One SELECT has a statement snapshot. Read-only mode and a local timeout
	// provide database-side safeguards in addition to the caller's deadline.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return graph.HistoryEventPage{}, fmt.Errorf("postgres.ReadHistoryEvents: begin: %w", err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return graph.HistoryEventPage{}, fmt.Errorf("postgres.ReadHistoryEvents: timeout: %w", err)
	}
	rows, err := tx.Query(ctx, historyEventsSQL, q.ClusterID, q.Namespace, q.From, q.To, q.Limit+1, q.MaxBytes)
	if err != nil {
		return graph.HistoryEventPage{}, fmt.Errorf("postgres.ReadHistoryEvents: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return graph.HistoryEventPage{}, err
		}
		if len(b.Page.Events) == q.Limit {
			b.Page.Truncated = true
			break
		}
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return graph.HistoryEventPage{}, fmt.Errorf("postgres.ReadHistoryEvents: scan: %w", err)
		}
		if body == nil {
			return graph.HistoryEventPage{}, graph.ErrHistoryEventLimit
		}
		var e graph.HistoryEvent
		if err := json.Unmarshal(body, &e); err != nil {
			return graph.HistoryEventPage{}, fmt.Errorf("postgres.ReadHistoryEvents: invalid metadata: %w", err)
		}
		if err := b.Add(ctx, e); err != nil {
			return graph.HistoryEventPage{}, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return graph.HistoryEventPage{}, fmt.Errorf("postgres.ReadHistoryEvents: rows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return graph.HistoryEventPage{}, fmt.Errorf("postgres.ReadHistoryEvents: finish: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return graph.HistoryEventPage{}, err
	}
	return b.Page, nil
}
