// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Config carries the connection parameters for the Tier 2 backend.
// MaxConns defaults to 10 when zero — matching the guide's default
// pool size; raise it via Helm values for hot APIs (P2-T6).
type Config struct {
	DSN      string
	MaxConns int32
}

// Store implements graph.GraphStore on top of plain PostgreSQL tables.
// AGE-backed traversal queries are layered on in P2-T3; this skeleton
// is the SQL-only correctness baseline that the contract test pins.
type Store struct {
	pool *pgxpool.Pool
}

// Compile-time check: this skeleton satisfies the GraphStore contract.
var _ graph.GraphStore = (*Store)(nil)

// New opens a connection pool, runs Init (idempotent CREATE TABLE IF
// NOT EXISTS), and returns a ready Store.
//
// On any error during construction the pool is closed before
// returning, so callers do not need a defer-close before checking err.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, errors.New("postgres.New: empty DSN")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres.New: parse DSN: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	} else {
		poolCfg.MaxConns = 10
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres.New: connect: %w", err)
	}

	s := &Store{pool: pool}
	if err := s.Init(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the connection pool. After Close the Store is unusable.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Init brings the database schema up to currentSchemaVersion by
// running every migration under migrate/ that has not been applied
// yet. It is idempotent and safe to call on every startup.
//
// As of P2-T3 this delegates to the versioned migration framework
// in schema.go; the inline DDL the P2-T2 skeleton shipped now lives
// in migrate/001_initial.sql alongside the AGE bootstrap.
func (s *Store) Init(ctx context.Context) error {
	return s.migrate(ctx)
}

// UpsertResource inserts or replaces the resource at r.ID(). The full
// Resource is serialized to JSONB; Resource.Raw is dropped per its
// json:"-" tag, matching the wire contract.
//
// Tier 2 keeps PG and AGE in sync via a single transaction: the
// JSONB row is the source of truth, the AGE vertex mirrors it for
// Cypher reads (ListIncoming/ListOutgoing/TraverseOutgoing). Unknown
// kinds (CRDs not in migrate/001_initial.sql's allowlist) fall
// through to PG-only — P2-T10 will register CRD labels at runtime.
func (s *Store) UpsertResource(ctx context.Context, r graph.Resource) error {
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("postgres.UpsertResource: marshal %s: %w", r.ID(), err)
	}
	const sql = `
		INSERT INTO resources (id, data) VALUES ($1, $2::jsonb)
		ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data
	`
	return s.withAGETx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql, r.ID(), body); err != nil {
			return fmt.Errorf("postgres.UpsertResource: exec %s: %w", r.ID(), err)
		}
		return upsertVertex(ctx, tx, r)
	})
}

// DeleteResource removes the resource at id and cascades to every edge
// incident on it (incoming and outgoing). Missing ids are a no-op.
//
// PG cascade is via SQL DELETE; AGE cascade is via DETACH DELETE on
// the matching vertex. Both happen in the same transaction so a
// partial failure cannot leak orphan edges in either backend.
func (s *Store) DeleteResource(ctx context.Context, id string) error {
	return s.withAGETx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM public.edges WHERE from_id = $1 OR to_id = $1`, id); err != nil {
			return fmt.Errorf("postgres.DeleteResource: cascade edges %s: %w", id, err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM public.resources WHERE id = $1`, id); err != nil {
			return fmt.Errorf("postgres.DeleteResource: row %s: %w", id, err)
		}
		return deleteVertex(ctx, tx, id)
	})
}

// UpsertEdge inserts or replaces the edge identified by
// (e.From, e.To, e.Type). Idempotent on the natural key.
//
// Endpoints must already exist as PG rows AND AGE vertices for the
// AGE MERGE to attach the edge. The contract test always Upserts
// resources before edges, so this is the documented contract; if a
// caller ever upserts an edge before its endpoints, the AGE side
// silently no-ops while PG still records the row, and the cross-
// store consistency check in cypher_test.go will catch it.
func (s *Store) UpsertEdge(ctx context.Context, e graph.Edge) error {
	const sql = `
		INSERT INTO edges (from_id, to_id, type) VALUES ($1, $2, $3)
		ON CONFLICT (from_id, to_id, type) DO NOTHING
	`
	return s.withAGETx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql, e.From, e.To, string(e.Type)); err != nil {
			return fmt.Errorf("postgres.UpsertEdge: %s -[%s]-> %s: %w", e.From, e.Type, e.To, err)
		}
		return upsertEdge(ctx, tx, e)
	})
}

// DeleteEdge removes the edge identified by (from, to, t). Missing
// edges are a no-op.
func (s *Store) DeleteEdge(ctx context.Context, from, to string, t graph.EdgeType) error {
	const sql = `DELETE FROM edges WHERE from_id = $1 AND to_id = $2 AND type = $3`
	return s.withAGETx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql, from, to, string(t)); err != nil {
			return fmt.Errorf("postgres.DeleteEdge: %s -[%s]-> %s: %w", from, t, to, err)
		}
		return deleteEdge(ctx, tx, from, to, t)
	})
}

// GetResource returns the resource at id or graph.ErrNotFound.
func (s *Store) GetResource(ctx context.Context, id string) (graph.Resource, error) {
	const sql = `SELECT data FROM resources WHERE id = $1`
	var body []byte
	err := s.pool.QueryRow(ctx, sql, id).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return graph.Resource{}, graph.ErrNotFound{ID: id}
	}
	if err != nil {
		return graph.Resource{}, fmt.Errorf("postgres.GetResource: %s: %w", id, err)
	}
	var r graph.Resource
	if err := json.Unmarshal(body, &r); err != nil {
		return graph.Resource{}, fmt.Errorf("postgres.GetResource: unmarshal %s: %w", id, err)
	}
	return r, nil
}

// ListResources returns every resource matching the filter. Empty
// filter fields mean "any". Labels match exactly via JSONB
// containment: every key/value in filter.Labels must be present on
// the resource (extra labels on the resource are allowed).
func (s *Store) ListResources(ctx context.Context, filter graph.Filter) ([]graph.Resource, error) {
	var labelsJSON []byte
	if len(filter.Labels) > 0 {
		var err error
		labelsJSON, err = json.Marshal(filter.Labels)
		if err != nil {
			return nil, fmt.Errorf("postgres.ListResources: marshal labels: %w", err)
		}
	}

	const sql = `
		SELECT data FROM resources
		WHERE ($1::text = '' OR data->>'kind' = $1)
		  AND ($2::text = '' OR data->>'namespace' = $2)
		  AND (
		    $3::jsonb IS NULL
		    OR (data->'labels') @> $3::jsonb
		  )
	`
	rows, err := s.pool.Query(ctx, sql, filter.Kind, filter.Namespace, labelsJSON)
	if err != nil {
		return nil, fmt.Errorf("postgres.ListResources: query: %w", err)
	}
	defer rows.Close()
	return scanResources(rows)
}

// ListIncoming returns every edge whose To equals id.
//
// P2-T4 originally directed switching this to AGE Cypher, on the
// premise that AGE would outperform indexed SQL by ~2x on a
// 100-vertex / 500-edge graph. The actual benchmark
// (BenchmarkListOutgoing_AGE_vs_SQL) shows AGE ~5x SLOWER on this
// workload because per-call Cypher overhead (LOAD, tx, parser,
// agtype serialization) dwarfs the inner work, and the edges
// table's btree index on (from_id) makes the SQL path effectively
// free. We therefore keep ListIncoming/ListOutgoing on SQL and
// reserve AGE for TraverseOutgoing (variable-length paths), where
// it has no SQL equivalent.
//
// listIncomingFromAGE / listOutgoingFromAGE in cypher.go are
// preserved so the benchmark can keep measuring both paths; if a
// future workload (e.g. very high-fanout vertices) flips the
// equation, callers can opt in.
func (s *Store) ListIncoming(ctx context.Context, id string) ([]graph.Edge, error) {
	const sql = `SELECT from_id, to_id, type FROM edges WHERE to_id = $1`
	rows, err := s.pool.Query(ctx, sql, id)
	if err != nil {
		return nil, fmt.Errorf("postgres.ListIncoming: %s: %w", id, err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// ListOutgoing is the mirror of ListIncoming. See the godoc on
// ListIncoming for why this stays on SQL despite the P2-T4 sketch.
func (s *Store) ListOutgoing(ctx context.Context, id string) ([]graph.Edge, error) {
	const sql = `SELECT from_id, to_id, type FROM edges WHERE from_id = $1`
	rows, err := s.pool.Query(ctx, sql, id)
	if err != nil {
		return nil, fmt.Errorf("postgres.ListOutgoing: %s: %w", id, err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// Snapshot returns every resource and every edge in a single graph.
// The two queries are not wrapped in a transaction; under concurrent
// writes the snapshot may include an edge whose endpoint is missing.
// Phase 2 traffic does not need stricter isolation; if it does, lift
// to a repeatable-read tx in a follow-up.
func (s *Store) Snapshot(ctx context.Context) (*graph.Graph, error) {
	rRows, err := s.pool.Query(ctx, `SELECT data FROM resources`)
	if err != nil {
		return nil, fmt.Errorf("postgres.Snapshot: resources: %w", err)
	}
	resources, err := scanResources(rRows)
	rRows.Close()
	if err != nil {
		return nil, err
	}

	eRows, err := s.pool.Query(ctx, `SELECT from_id, to_id, type FROM edges`)
	if err != nil {
		return nil, fmt.Errorf("postgres.Snapshot: edges: %w", err)
	}
	edges, err := scanEdges(eRows)
	eRows.Close()
	if err != nil {
		return nil, err
	}

	return &graph.Graph{Resources: resources, Edges: edges}, nil
}

// truncateAll wipes resources, edges, and the AGE graph contents
// without dropping the schema. Test-only helper used by the
// contract suite to give each sub-test a clean store while sharing
// a single container.
func (s *Store) truncateAll(ctx context.Context) error {
	return s.withAGETx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `TRUNCATE public.resources, public.edges`); err != nil {
			return fmt.Errorf("postgres.truncateAll: pg: %w", err)
		}
		const cypherClear = `
			SELECT * FROM cypher('` + graphName + `'::name, $$
				MATCH (n) DETACH DELETE n
			$$::cstring) AS (v agtype)
		`
		if _, err := tx.Exec(ctx, cypherClear); err != nil {
			return fmt.Errorf("postgres.truncateAll: age: %w", err)
		}
		return nil
	})
}

func scanResources(rows pgx.Rows) ([]graph.Resource, error) {
	out := make([]graph.Resource, 0)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan resource: %w", err)
		}
		var r graph.Resource
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("unmarshal resource: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows.Err: %w", err)
	}
	return out, nil
}

func scanEdges(rows pgx.Rows) ([]graph.Edge, error) {
	out := make([]graph.Edge, 0)
	for rows.Next() {
		var e graph.Edge
		var typ string
		if err := rows.Scan(&e.From, &e.To, &typ); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		e.Type = graph.EdgeType(typ)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows.Err: %w", err)
	}
	return out, nil
}
