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

// storageBlob wraps a Resource for JSONB persistence. The embedded
// graph.Resource provides the canonical wire fields (kind / name /
// ...). RawSpec is a sibling key — needed because Resource.Raw
// carries `json:"-"`, which is correct for the API wire format but
// would otherwise drop the unstructured spec on a Tier 2 round-trip.
//
// The RBAC handlers (P2-T14) read role.Raw["rules"] off resources
// returned by GetResource; without RawSpec, that read sees nil on
// any Tier 2 install and the API silently returns empty rules.
type storageBlob struct {
	graph.Resource
	RawSpec map[string]any `json:"raw,omitempty"`
}

// UpsertResource inserts or replaces the resource at r.ID(). The
// Resource is serialized as a storageBlob so both the public wire
// fields and the unstructured Raw map land in the JSONB column.
//
// Tier 2 keeps PG and AGE in sync via a single transaction: the
// JSONB row is the source of truth, the AGE vertex mirrors it for
// Cypher reads (ListIncoming/ListOutgoing/TraverseOutgoing). Unknown
// kinds (CRDs not in migrate/001_initial.sql's allowlist) fall
// through to PG-only — P2-T10 will register CRD labels at runtime.
func (s *Store) UpsertResource(ctx context.Context, r graph.Resource) error {
	body, err := json.Marshal(storageBlob{Resource: r, RawSpec: r.Raw})
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
	// DO UPDATE (not DO NOTHING) so a re-upsert refreshes the
	// attributes bag — a Constraint's violation status changes over
	// its lifetime, and the memory store already replaces on upsert,
	// so this keeps the two backends consistent.
	const sql = `
		INSERT INTO edges (from_id, to_id, type, attributes) VALUES ($1, $2, $3, $4)
		ON CONFLICT (from_id, to_id, type) DO UPDATE SET attributes = EXCLUDED.attributes
	`
	attrs, err := edgeAttributesJSON(e.Attributes)
	if err != nil {
		return fmt.Errorf("postgres.UpsertEdge: marshal attributes: %w", err)
	}
	return s.withAGETx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql, e.From, e.To, string(e.Type), attrs); err != nil {
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
	return unmarshalStorageBlob(body, id)
}

// unmarshalStorageBlob parses a JSONB row into a Resource, lifting
// the storageBlob's RawSpec into Resource.Raw. Tolerant of legacy
// rows written before the wrapper landed: a missing "raw" key
// simply leaves Resource.Raw nil.
func unmarshalStorageBlob(body []byte, id string) (graph.Resource, error) {
	var sb storageBlob
	if err := json.Unmarshal(body, &sb); err != nil {
		return graph.Resource{}, fmt.Errorf("postgres.GetResource: unmarshal %s: %w", id, err)
	}
	r := sb.Resource
	r.Raw = sb.RawSpec
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
		  AND ($4::text = '' OR cluster_id = $4)
	`
	rows, err := s.pool.Query(ctx, sql, filter.Kind, filter.Namespace, labelsJSON, filter.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("postgres.ListResources: query: %w", err)
	}
	defer rows.Close()
	return scanResources(rows)
}

// ListResourcesInCluster is the cluster-tagged variant of
// ListResources (P3-T20). It filters on the generated cluster_id
// column added by migration 007 — every pre-P3-T20 row reads back as
// cluster_id=” so passing the empty clusterID reproduces the
// pre-multicluster behaviour exactly.
func (s *Store) ListResourcesInCluster(ctx context.Context, clusterID string, filter graph.Filter) ([]graph.Resource, error) {
	var labelsJSON []byte
	if len(filter.Labels) > 0 {
		var err error
		labelsJSON, err = json.Marshal(filter.Labels)
		if err != nil {
			return nil, fmt.Errorf("postgres.ListResourcesInCluster: marshal labels: %w", err)
		}
	}

	const sql = `
		SELECT data FROM resources
		WHERE cluster_id = $1
		  AND ($2::text = '' OR data->>'kind' = $2)
		  AND ($3::text = '' OR data->>'namespace' = $3)
		  AND (
		    $4::jsonb IS NULL
		    OR (data->'labels') @> $4::jsonb
		  )
	`
	rows, err := s.pool.Query(ctx, sql, clusterID, filter.Kind, filter.Namespace, labelsJSON)
	if err != nil {
		return nil, fmt.Errorf("postgres.ListResourcesInCluster: query: %w", err)
	}
	defer rows.Close()
	return scanResources(rows)
}

// GetEdgesAcrossClusters returns every edge whose endpoints are both
// resources in the given cluster set (P3-T20). Endpoints that are
// dangling (no resource row) or whose cluster_id is outside the set
// drop the edge — the visible-set rule the aggregators use.
//
// An empty clusterIDs slice returns no edges; passing []string{""}
// returns edges entirely within the single-cluster subgraph so v1.2
// callers keep their existing behaviour.
func (s *Store) GetEdgesAcrossClusters(ctx context.Context, clusterIDs []string) ([]graph.Edge, error) {
	if len(clusterIDs) == 0 {
		return nil, nil
	}
	const sql = `
		SELECT e.from_id, e.to_id, e.type, e.attributes
		FROM edges e
		JOIN resources rf ON rf.id = e.from_id
		JOIN resources rt ON rt.id = e.to_id
		WHERE rf.cluster_id = ANY($1::text[])
		  AND rt.cluster_id = ANY($1::text[])
	`
	rows, err := s.pool.Query(ctx, sql, clusterIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres.GetEdgesAcrossClusters: query: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
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
	const sql = `SELECT from_id, to_id, type, attributes FROM edges WHERE to_id = $1`
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
	const sql = `SELECT from_id, to_id, type, attributes FROM edges WHERE from_id = $1`
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

	eRows, err := s.pool.Query(ctx, `SELECT from_id, to_id, type, attributes FROM edges`)
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

// KindCountsByNamespace executes one GROUP BY query against the
// resources table, returning the per-(namespace, kind) counts the
// cluster-level aggregator needs.
//
// This replaces Snapshot+Go-side counting for cluster_view. On a 6K-
// resource cluster the old path allocated 50-200 MB per request (a
// full Resource struct including Raw payload per row) and OOM-killed
// the API pod under modest concurrent load. This path returns ~100
// rows of (text, text, bigint) — single-digit MB of allocation, even
// on real clusters with 10K+ resources.
//
// Cluster-scoped resources (Resource.Namespace == "") are bucketed
// under the empty-string key, matching the in-memory implementation
// and the contract test.
// marshalLabelFilter turns an F-114 label filter into the jsonb
// parameter the @> containment operator takes. An empty/nil filter
// marshals to a nil []byte, which binds as SQL NULL — every query
// below pairs that with a "$N IS NULL OR ..." guard so an unfiltered
// call behaves exactly as it did before F-114.
func marshalLabelFilter(labels map[string]string) ([]byte, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("marshal label filter: %w", err)
	}
	return b, nil
}

func (s *Store) KindCountsByNamespace(ctx context.Context, labels map[string]string) (map[string]map[string]int, error) {
	labelsJSON, err := marshalLabelFilter(labels)
	if err != nil {
		return nil, fmt.Errorf("postgres.KindCountsByNamespace: %w", err)
	}
	const sql = `
		SELECT
			COALESCE(data->>'namespace', '') AS ns,
			COALESCE(data->>'kind', '')      AS kind,
			COUNT(*)                         AS cnt
		FROM resources
		WHERE $1::jsonb IS NULL OR (data->'labels') @> $1::jsonb
		GROUP BY data->>'namespace', data->>'kind'
	`
	rows, err := s.pool.Query(ctx, sql, labelsJSON)
	if err != nil {
		return nil, fmt.Errorf("postgres.KindCountsByNamespace: query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]map[string]int)
	for rows.Next() {
		var ns, kind string
		var cnt int64
		if err := rows.Scan(&ns, &kind, &cnt); err != nil {
			return nil, fmt.Errorf("postgres.KindCountsByNamespace: scan: %w", err)
		}
		bucket := out[ns]
		if bucket == nil {
			bucket = make(map[string]int)
			out[ns] = bucket
		}
		bucket[kind] = int(cnt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres.KindCountsByNamespace: rows: %w", err)
	}
	return out, nil
}

// CrossNamespaceEdgeCounts executes one GROUP BY query that joins
// edges to both endpoints to bucket every edge into (from-ns, to-ns).
// Edges whose endpoint is missing from resources are dropped by the
// inner joins — matching the in-memory implementation's "skip
// dangling" rule and the contract test.
//
// Cluster_view does not differentiate edge types, so type is folded
// into the count rather than appearing in the key.
func (s *Store) CrossNamespaceEdgeCounts(ctx context.Context, labels map[string]string) (map[graph.NamespacePair]int, error) {
	labelsJSON, err := marshalLabelFilter(labels)
	if err != nil {
		return nil, fmt.Errorf("postgres.CrossNamespaceEdgeCounts: %w", err)
	}
	const sql = `
		SELECT
			COALESCE(r1.data->>'namespace', '') AS from_ns,
			COALESCE(r2.data->>'namespace', '') AS to_ns,
			COUNT(*)                            AS cnt
		FROM edges e
		JOIN resources r1 ON r1.id = e.from_id
		JOIN resources r2 ON r2.id = e.to_id
		WHERE $1::jsonb IS NULL
		   OR ((r1.data->'labels') @> $1::jsonb AND (r2.data->'labels') @> $1::jsonb)
		GROUP BY r1.data->>'namespace', r2.data->>'namespace'
	`
	rows, err := s.pool.Query(ctx, sql, labelsJSON)
	if err != nil {
		return nil, fmt.Errorf("postgres.CrossNamespaceEdgeCounts: query: %w", err)
	}
	defer rows.Close()
	out := make(map[graph.NamespacePair]int)
	for rows.Next() {
		var fromNs, toNs string
		var cnt int64
		if err := rows.Scan(&fromNs, &toNs, &cnt); err != nil {
			return nil, fmt.Errorf("postgres.CrossNamespaceEdgeCounts: scan: %w", err)
		}
		out[graph.NamespacePair{From: fromNs, To: toNs}] = int(cnt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres.CrossNamespaceEdgeCounts: rows: %w", err)
	}
	return out, nil
}

// NamespaceSubgraph returns the resources in namespace ns plus the
// edges whose endpoints are both in that namespace.
//
// Two SQL queries (not in a transaction; same isolation contract as
// Snapshot). The resource fetch uses idx_resources_namespace; the
// edge fetch joins to the resources table on both endpoints so PG
// can filter by namespace before deserialising the JSONB blob — the
// crucial bit, because the old code path's Snapshot deserialised
// every resource row regardless.
//
// stress-test-5k contains ~6K resources in a single namespace, so
// this query is still O(R-in-ns) and the namespace_view response is
// still large for that fixture. That is intrinsic to "return every
// resource in this namespace"; the OOM fix is that we no longer
// also fetch the 1K resources outside the namespace.
func (s *Store) NamespaceSubgraph(ctx context.Context, ns string, labels map[string]string) (*graph.Graph, error) {
	labelsJSON, err := marshalLabelFilter(labels)
	if err != nil {
		return nil, fmt.Errorf("postgres.NamespaceSubgraph: %w", err)
	}
	const resourcesSQL = `
		SELECT data FROM resources
		WHERE data->>'namespace' = $1
		  AND ($2::jsonb IS NULL OR (data->'labels') @> $2::jsonb)
	`
	rRows, err := s.pool.Query(ctx, resourcesSQL, ns, labelsJSON)
	if err != nil {
		return nil, fmt.Errorf("postgres.NamespaceSubgraph: resources query: %w", err)
	}
	resources, err := scanResources(rRows)
	rRows.Close()
	if err != nil {
		return nil, fmt.Errorf("postgres.NamespaceSubgraph: resources scan: %w", err)
	}
	const edgesSQL = `
		SELECT e.from_id, e.to_id, e.type, e.attributes
		FROM edges e
		JOIN resources r1 ON r1.id = e.from_id AND r1.data->>'namespace' = $1
		JOIN resources r2 ON r2.id = e.to_id   AND r2.data->>'namespace' = $1
		WHERE $2::jsonb IS NULL
		   OR ((r1.data->'labels') @> $2::jsonb AND (r2.data->'labels') @> $2::jsonb)
	`
	eRows, err := s.pool.Query(ctx, edgesSQL, ns, labelsJSON)
	if err != nil {
		return nil, fmt.Errorf("postgres.NamespaceSubgraph: edges query: %w", err)
	}
	edges, err := scanEdges(eRows)
	eRows.Close()
	if err != nil {
		return nil, fmt.Errorf("postgres.NamespaceSubgraph: edges scan: %w", err)
	}
	return &graph.Graph{Resources: resources, Edges: edges}, nil
}

// truncateAll wipes resources, edges, the snapshot-history tables,
// and the AGE graph contents without dropping the schema. Test-only
// helper used by the contract suite to give each sub-test a clean
// store while sharing a single container.
//
// resource_events / snapshot_meta are included so P3-T2 snapshot
// contract sub-tests don't see events leaked from earlier sub-tests.
// RESTART IDENTITY resets the BIGSERIAL counters so event IDs are
// deterministic per sub-test.
func (s *Store) truncateAll(ctx context.Context) error {
	return s.withAGETx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`TRUNCATE public.resources, public.edges,
			          public.resource_events, public.snapshot_meta
			 RESTART IDENTITY`); err != nil {
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
		r, err := unmarshalStorageBlob(body, "")
		if err != nil {
			return nil, err
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
		var attrsRaw []byte
		if err := rows.Scan(&e.From, &e.To, &typ, &attrsRaw); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		e.Type = graph.EdgeType(typ)
		// Leave Attributes nil for the common empty case so the result
		// matches an edge written without attributes (and the JSON
		// omitempty shape stays identical across both stores).
		if len(attrsRaw) > 0 {
			var m map[string]string
			if err := json.Unmarshal(attrsRaw, &m); err != nil {
				return nil, fmt.Errorf("scan edge attributes: %w", err)
			}
			if len(m) > 0 {
				e.Attributes = m
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows.Err: %w", err)
	}
	return out, nil
}

// edgeAttributesJSON marshals an edge's attribute bag for storage. A
// nil or empty map becomes the empty JSON object so the column stays
// non-null and consistent with the migration default.
func edgeAttributesJSON(attrs map[string]string) ([]byte, error) {
	if len(attrs) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(attrs)
}
