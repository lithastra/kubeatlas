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

// OTLP trace spans (F-204). otel_spans is written by the receiver's
// batch workers, queried by the overlay API, and pruned by the
// retention worker. Like resource_events, spans are a plain SQL
// concern — no AGE / Cypher, so none of these run inside withAGETx.
//
// These methods are deliberately NOT on the graph.GraphStore
// interface: span storage is Tier 2-only, reached through the narrow
// SpanSink / SpanDeleter seams the otel package defines, so the
// in-memory backend stays span-free.

// spanPruneBatchSize bounds how many otel_spans rows a single
// retention DELETE removes. Span volume dwarfs resource_events, so an
// unbounded DELETE would hold a table lock for far too long; batching
// keeps each statement short while still draining a large backlog in
// a handful of iterations. Mirrors pruneBatchSize for resource_events.
const spanPruneBatchSize = 10000

// WriteSpans inserts a batch of spans. span_id is the primary key, so
// a re-sent span (OTLP retransmits on the exporter's retry path)
// upserts rather than erroring or duplicating. received_at defaults
// to now() server-side.
//
// The whole batch goes in one pgx.Batch round-trip; a single failing
// statement fails the call (the receiver's worker logs and moves on —
// dropped-on-write is acceptable for an opt-in observability overlay,
// and is counted separately from the channel-full drop).
func (s *Store) WriteSpans(ctx context.Context, spans []graph.Span) error {
	if len(spans) == 0 {
		return nil
	}
	const sql = `
		INSERT INTO otel_spans
			(trace_id, span_id, parent_span_id, service_name,
			 k8s_namespace, k8s_pod, k8s_deployment,
			 start_time, duration_ns, attributes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)
		ON CONFLICT (span_id) DO UPDATE SET
			trace_id       = EXCLUDED.trace_id,
			parent_span_id = EXCLUDED.parent_span_id,
			service_name   = EXCLUDED.service_name,
			k8s_namespace  = EXCLUDED.k8s_namespace,
			k8s_pod        = EXCLUDED.k8s_pod,
			k8s_deployment = EXCLUDED.k8s_deployment,
			start_time     = EXCLUDED.start_time,
			duration_ns    = EXCLUDED.duration_ns,
			attributes     = EXCLUDED.attributes
	`
	batch := &pgx.Batch{}
	for _, sp := range spans {
		var attrs []byte
		if sp.Attributes != nil {
			b, err := json.Marshal(sp.Attributes)
			if err != nil {
				return fmt.Errorf("postgres.WriteSpans: marshal attributes for span %s: %w", sp.SpanID, err)
			}
			attrs = b
		}
		batch.Queue(sql,
			sp.TraceID, sp.SpanID, sp.ParentSpanID, sp.ServiceName,
			sp.K8sNamespace, sp.K8sPod, sp.K8sDeployment,
			sp.StartTime, sp.DurationNS, attrs,
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for i := 0; i < len(spans); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres.WriteSpans: insert span %s: %w", spans[i].SpanID, err)
		}
	}
	return nil
}

// QuerySpans returns spans whose start_time is at or after since,
// ordered newest-first. An empty serviceName matches every service;
// a non-empty one filters to it (served from idx_otel_spans_service_time).
// limit caps the result; a non-positive limit applies a sane default.
func (s *Store) QuerySpans(ctx context.Context, serviceName string, since time.Time, limit int) ([]graph.Span, error) {
	if limit <= 0 {
		limit = 1000
	}
	const sql = `
		SELECT trace_id, span_id, parent_span_id, service_name,
		       k8s_namespace, k8s_pod, k8s_deployment,
		       start_time, duration_ns, attributes
		FROM otel_spans
		WHERE start_time >= $1
		  AND ($2::text = '' OR service_name = $2)
		ORDER BY start_time DESC
		LIMIT $3
	`
	rows, err := s.pool.Query(ctx, sql, since, serviceName, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres.QuerySpans: query: %w", err)
	}
	defer rows.Close()
	out := make([]graph.Span, 0)
	for rows.Next() {
		var sp graph.Span
		var attrs []byte
		if err := rows.Scan(
			&sp.TraceID, &sp.SpanID, &sp.ParentSpanID, &sp.ServiceName,
			&sp.K8sNamespace, &sp.K8sPod, &sp.K8sDeployment,
			&sp.StartTime, &sp.DurationNS, &attrs,
		); err != nil {
			return nil, fmt.Errorf("postgres.QuerySpans: scan: %w", err)
		}
		if len(attrs) > 0 {
			if err := json.Unmarshal(attrs, &sp.Attributes); err != nil {
				return nil, fmt.Errorf("postgres.QuerySpans: unmarshal attributes for span %s: %w", sp.SpanID, err)
			}
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres.QuerySpans: rows: %w", err)
	}
	return out, nil
}

// DeleteOldSpans deletes otel_spans rows received before cutoff, in
// spanPruneBatchSize-row batches, looping until none remain. Returns
// the total deleted. Retention keys on received_at (when KubeAtlas
// collected the span), not start_time, so a late-arriving span gets
// its full retention window. idx_otel_spans_received_at makes the
// `received_at < cutoff` scan an index range scan.
func (s *Store) DeleteOldSpans(ctx context.Context, cutoff time.Time) (int64, error) {
	const sql = `
		DELETE FROM otel_spans
		WHERE ctid IN (
			SELECT ctid FROM otel_spans
			WHERE received_at < $1
			LIMIT $2
		)
	`
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		tag, err := s.pool.Exec(ctx, sql, cutoff, spanPruneBatchSize)
		if err != nil {
			return total, fmt.Errorf("postgres.DeleteOldSpans: delete batch: %w", err)
		}
		n := tag.RowsAffected()
		total += n
		if n < spanPruneBatchSize {
			return total, nil
		}
	}
}

// Runtime overlay edges (F-204 part 2, P5-T5). otel_runtime_edges is
// written by the correlator, queried by the overlay API, and pruned by
// the correlator's own retention step. Like otel_spans it is a plain
// SQL, Tier 2-only concern reached through the otel package's narrow
// RuntimeEdgeSink seam — never on graph.GraphStore, so runtime edges
// stay off the declarative graph (invariant 2.2).

// UpsertRuntimeEdges inserts or folds a batch of inferred runtime edges.
// (from_id, to_id) is the primary key, so re-observing a call updates
// the existing row: first_seen keeps the earliest observation,
// last_seen the latest, and call_count the peak per-window count
// (GREATEST, not a running sum — see graph.RuntimeEdge). The whole
// batch goes in one round-trip; a single failing statement fails the
// call (the correlator logs and retries next pass).
func (s *Store) UpsertRuntimeEdges(ctx context.Context, edges []graph.RuntimeEdge) error {
	if len(edges) == 0 {
		return nil
	}
	const sql = `
		INSERT INTO otel_runtime_edges
			(from_id, to_id, from_service, to_service, namespace,
			 first_seen, last_seen, call_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (from_id, to_id) DO UPDATE SET
			from_service = EXCLUDED.from_service,
			to_service   = EXCLUDED.to_service,
			namespace    = EXCLUDED.namespace,
			first_seen   = LEAST(otel_runtime_edges.first_seen, EXCLUDED.first_seen),
			last_seen    = GREATEST(otel_runtime_edges.last_seen, EXCLUDED.last_seen),
			call_count   = GREATEST(otel_runtime_edges.call_count, EXCLUDED.call_count)
	`
	batch := &pgx.Batch{}
	for _, e := range edges {
		batch.Queue(sql,
			e.FromID, e.ToID, e.FromService, e.ToService, e.Namespace,
			e.FirstSeen, e.LastSeen, e.CallCount,
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for i := 0; i < len(edges); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres.UpsertRuntimeEdges: upsert %s->%s: %w", edges[i].FromID, edges[i].ToID, err)
		}
	}
	return nil
}

// QueryRuntimeEdges returns runtime edges observed at or after since,
// ordered by (from_id, to_id). An empty namespace matches every
// namespace; a non-empty one filters to it (served from
// idx_otel_runtime_edges_ns).
func (s *Store) QueryRuntimeEdges(ctx context.Context, namespace string, since time.Time) ([]graph.RuntimeEdge, error) {
	const sql = `
		SELECT from_id, to_id, from_service, to_service, namespace,
		       first_seen, last_seen, call_count
		FROM otel_runtime_edges
		WHERE last_seen >= $1
		  AND ($2::text = '' OR namespace = $2)
		ORDER BY from_id, to_id
	`
	rows, err := s.pool.Query(ctx, sql, since, namespace)
	if err != nil {
		return nil, fmt.Errorf("postgres.QueryRuntimeEdges: query: %w", err)
	}
	defer rows.Close()
	out := make([]graph.RuntimeEdge, 0)
	for rows.Next() {
		var e graph.RuntimeEdge
		if err := rows.Scan(
			&e.FromID, &e.ToID, &e.FromService, &e.ToService, &e.Namespace,
			&e.FirstSeen, &e.LastSeen, &e.CallCount,
		); err != nil {
			return nil, fmt.Errorf("postgres.QueryRuntimeEdges: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres.QueryRuntimeEdges: rows: %w", err)
	}
	return out, nil
}

// DeleteOldRuntimeEdges deletes runtime edges not re-observed since
// cutoff, so a decommissioned call path drops out of the overlay.
// Returns the number deleted. The table holds one row per resource
// pair, so a single unbounded DELETE is safe here (unlike the batched
// otel_spans sweep).
func (s *Store) DeleteOldRuntimeEdges(ctx context.Context, cutoff time.Time) (int64, error) {
	const sql = `DELETE FROM otel_runtime_edges WHERE last_seen < $1`
	tag, err := s.pool.Exec(ctx, sql, cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres.DeleteOldRuntimeEdges: %w", err)
	}
	return tag.RowsAffected(), nil
}
