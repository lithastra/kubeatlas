// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Snapshot history (F-111 / P3-T2). resource_events is an append-
// only stream; snapshot_meta records periodic full-sync markers.
// Both tables come from migrate/005_snapshots.sql.
//
// The write path is plain SQL INSERT — no AGE / Cypher (anti-
// pattern guard: the event stream is a relational concern; AGE is
// query-time only). INSERTs do not run inside withAGETx because
// they touch neither the resources table's AGE mirror nor any
// vertex/edge label.

// AppendEvent inserts one row into resource_events. The store
// assigns id (BIGSERIAL) and, when the caller leaves Timestamp
// zero, ts (DEFAULT now()). A non-zero caller Timestamp is honoured
// — the snapshot writer backfilling a burst may want to preserve
// the informer's observation time rather than the insert time.
func (s *Store) AppendEvent(ctx context.Context, e graph.ResourceEvent) error {
	var data []byte
	if e.Data != nil {
		var err error
		data, err = json.Marshal(e.Data)
		if err != nil {
			return fmt.Errorf("postgres.AppendEvent: marshal data for %s/%s: %w", e.Namespace, e.Name, err)
		}
	}
	// Two INSERT shapes: one lets ts default to now(), the other
	// pins the caller's Timestamp. Branching keeps the common
	// (zero-Timestamp) path on the table's own DEFAULT.
	if e.Timestamp.IsZero() {
		const sql = `
			INSERT INTO resource_events
				(cluster_id, namespace, kind, uid, name, event_type, resource_version, data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
		`
		if _, err := s.pool.Exec(ctx, sql,
			e.ClusterID, e.Namespace, e.Kind, e.UID, e.Name,
			string(e.EventType), e.ResourceVersion, data,
		); err != nil {
			return fmt.Errorf("postgres.AppendEvent: insert %s/%s: %w", e.Namespace, e.Name, err)
		}
		return nil
	}
	const sql = `
		INSERT INTO resource_events
			(ts, cluster_id, namespace, kind, uid, name, event_type, resource_version, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
	`
	if _, err := s.pool.Exec(ctx, sql,
		e.Timestamp, e.ClusterID, e.Namespace, e.Kind, e.UID, e.Name,
		string(e.EventType), e.ResourceVersion, data,
	); err != nil {
		return fmt.Errorf("postgres.AppendEvent: insert %s/%s: %w", e.Namespace, e.Name, err)
	}
	return nil
}

// WriteSnapshotMeta inserts one row into snapshot_meta.
func (s *Store) WriteSnapshotMeta(ctx context.Context, m graph.SnapshotMeta) error {
	if m.Timestamp.IsZero() {
		const sql = `
			INSERT INTO snapshot_meta
				(cluster_id, resource_count, edge_count, duration_ms, trigger)
			VALUES ($1, $2, $3, $4, $5)
		`
		if _, err := s.pool.Exec(ctx, sql,
			m.ClusterID, m.ResourceCount, m.EdgeCount, m.DurationMS, string(m.Trigger),
		); err != nil {
			return fmt.Errorf("postgres.WriteSnapshotMeta: insert: %w", err)
		}
		return nil
	}
	const sql = `
		INSERT INTO snapshot_meta
			(ts, cluster_id, resource_count, edge_count, duration_ms, trigger)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	if _, err := s.pool.Exec(ctx, sql,
		m.Timestamp, m.ClusterID, m.ResourceCount, m.EdgeCount, m.DurationMS, string(m.Trigger),
	); err != nil {
		return fmt.Errorf("postgres.WriteSnapshotMeta: insert: %w", err)
	}
	return nil
}

// pruneBatchSize bounds how many resource_events rows a single
// DELETE statement removes. Retention prunes can span millions of
// rows; an unbounded DELETE would hold a lock on the whole table
// for the duration. 10K keeps each statement short while still
// draining a large backlog in a handful of iterations.
const pruneBatchSize = 10000

// PruneEventsBefore deletes resource_events rows older than cutoff
// in pruneBatchSize-row batches, looping until none remain. Returns
// the total deleted.
//
// The batch DELETE uses a ctid sub-select (Postgres DELETE has no
// LIMIT clause): each statement removes at most pruneBatchSize of
// the matching rows. idx_events_ts from migrate/005 makes the
// `ts < cutoff` scan an index range scan.
func (s *Store) PruneEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	const sql = `
		DELETE FROM resource_events
		WHERE ctid IN (
			SELECT ctid FROM resource_events
			WHERE ts < $1
			LIMIT $2
		)
	`
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		tag, err := s.pool.Exec(ctx, sql, cutoff, pruneBatchSize)
		if err != nil {
			return total, fmt.Errorf("postgres.PruneEventsBefore: delete batch: %w", err)
		}
		n := tag.RowsAffected()
		total += n
		if n < pruneBatchSize {
			// Last batch — fewer rows than the cap means the
			// matching set is exhausted.
			return total, nil
		}
	}
}

// QueryEvents returns resource_events rows in [from, to], oldest
// first. An empty namespace matches every namespace. The
// idx_events_ns_ts / idx_events_ts indexes from migrate/005 cover
// both the filtered and unfiltered shapes.
func (s *Store) QueryEvents(ctx context.Context, namespace string, from, to time.Time) ([]graph.ResourceEvent, error) {
	const sql = `
		SELECT id, ts, cluster_id, namespace, kind, uid, name,
		       event_type, resource_version, data
		FROM resource_events
		WHERE ts >= $1 AND ts <= $2
		  AND ($3::text = '' OR namespace = $3)
		ORDER BY ts ASC, id ASC
	`
	rows, err := s.pool.Query(ctx, sql, from, to, namespace)
	if err != nil {
		return nil, fmt.Errorf("postgres.QueryEvents: query: %w", err)
	}
	defer rows.Close()

	out := make([]graph.ResourceEvent, 0)
	for rows.Next() {
		var (
			e       graph.ResourceEvent
			evType  string
			rawData []byte
		)
		if err := rows.Scan(
			&e.ID, &e.Timestamp, &e.ClusterID, &e.Namespace, &e.Kind,
			&e.UID, &e.Name, &evType, &e.ResourceVersion, &rawData,
		); err != nil {
			return nil, fmt.Errorf("postgres.QueryEvents: scan: %w", err)
		}
		e.EventType = graph.EventType(evType)
		if len(rawData) > 0 {
			if err := json.Unmarshal(rawData, &e.Data); err != nil {
				return nil, fmt.Errorf("postgres.QueryEvents: unmarshal data for event %d: %w", e.ID, err)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres.QueryEvents: rows: %w", err)
	}
	return out, nil
}
