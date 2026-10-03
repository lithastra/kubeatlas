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

var _ graph.ImpactSnapshotter = (*Store)(nil)

// Only allowlisted metadata leaves PostgreSQL. The Secret CASE deliberately
// never selects UID, versions, owners, or object data, including for legacy rows.
// The indexed generated cluster_id column scopes the read before decoding.
var impactResourcesSQL = `
SELECT CASE WHEN octet_length(projected::text) <= $3 THEN projected ELSE NULL END
FROM (
  SELECT CASE WHEN data->>'kind' = 'Secret' THEN
    jsonb_build_object('kind', 'Secret', 'name', data->>'name',
      'namespace', data->>'namespace', 'clusterId', cluster_id)
  ELSE jsonb_strip_nulls(jsonb_build_object(
    'kind', data->>'kind', 'name', data->>'name', 'namespace', data->>'namespace',
    'clusterId', cluster_id, 'uid', data->>'uid',
    'groupVersion', data->>'groupVersion', 'resourceVersion', data->>'resourceVersion',
    'ownerReferences', ` + impactOwnerReferencesSQL + `)) ||
    jsonb_build_object('referenceFields', ` + impactReferenceSQL() + `) END AS projected
  FROM public.resources WHERE cluster_id = $1 LIMIT $2
) AS scoped`

// Keep same-cluster dangling tuples: an inner join would silently erase the
// missing-endpoint evidence. Prefix checks also exclude cross-cluster identities
// even when their resource rows are missing. Large scans remain deadline-bound;
// LIMIT bounds returned rows, not the number of foreign rows inspected by PG.
const impactEdgesSQL = `
SELECT CASE WHEN octet_length(projected::text) <= $3 THEN projected ELSE NULL END
FROM (
  SELECT jsonb_build_object('from', from_id, 'to', to_id, 'type', type) AS projected
  FROM public.edges
  WHERE strpos(from_id, '/') > 0 AND strpos(to_id, '/') > 0
    AND (($1 = '' AND strpos(split_part(from_id, '/', 1), ':') = 0)
      OR ($1 <> '' AND strpos(split_part(from_id, '/', 1), ':') > 0
        AND split_part(split_part(from_id, '/', 1), ':', 1) = $1))
    AND (($1 = '' AND strpos(split_part(to_id, '/', 1), ':') = 0)
      OR ($1 <> '' AND strpos(split_part(to_id, '/', 1), ':') > 0
        AND split_part(split_part(to_id, '/', 1), ':', 1) = $1))
  LIMIT $2
) AS scoped`

func (s *Store) SnapshotImpact(ctx context.Context, opts graph.ImpactSnapshotOptions) (*graph.Graph, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.ImpactReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts, err := opts.Normalize()
	if err != nil {
		return nil, err
	}
	builder, err := graph.NewImpactProjectionBuilder(opts)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("postgres.SnapshotImpact: begin: %w", err)
	}
	defer func() {
		// Cancellation must not leave an idle transaction or acquired pool slot.
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("postgres.SnapshotImpact: timeout: %w", err)
	}
	if err := readImpactRows(ctx, tx, impactResourcesSQL, opts.ClusterID, opts.MaxResources+1, opts.MaxBytes, func(body []byte) error {
		var row struct {
			graph.Resource
			ReferenceFields map[string]any `json:"referenceFields"`
		}
		if err := json.Unmarshal(body, &row); err != nil {
			return fmt.Errorf("postgres.SnapshotImpact: invalid resource metadata: %w", err)
		}
		row.Raw = row.ReferenceFields
		return builder.AddResource(ctx, row.Resource)
	}); err != nil {
		return nil, err
	}
	if err := readImpactRows(ctx, tx, impactEdgesSQL, opts.ClusterID, opts.MaxEdges+1, opts.MaxBytes, func(body []byte) error {
		var e graph.Edge
		if err := json.Unmarshal(body, &e); err != nil {
			return fmt.Errorf("postgres.SnapshotImpact: invalid edge tuple: %w", err)
		}
		return builder.AddEdge(ctx, e)
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("postgres.SnapshotImpact: finish: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return builder.Graph, nil
}

func readImpactRows(ctx context.Context, tx pgx.Tx, query, cluster string, limit, maxBytes int, add func([]byte) error) error {
	rows, err := tx.Query(ctx, query, cluster, limit, maxBytes)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("postgres.SnapshotImpact: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var body []byte
		if err := rows.Scan(&body); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("postgres.SnapshotImpact: scan: %w", err)
		}
		if body == nil {
			return graph.ErrImpactSnapshotLimit
		}
		if err := add(body); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("postgres.SnapshotImpact: rows: %w", err)
	}
	return nil
}

// Invalid owner identities fail decoding without transferring arbitrary payloads.
const impactOwnerReferencesSQL = `CASE
WHEN data->'ownerReferences' IS NULL OR data->'ownerReferences' = 'null'::jsonb THEN NULL
WHEN jsonb_typeof(data->'ownerReferences') <> 'array' THEN '"invalid owner references"'::jsonb
WHEN EXISTS (SELECT 1 FROM jsonb_array_elements(data->'ownerReferences') AS owner(value)
  WHERE jsonb_typeof(value) <> 'object'
    OR EXISTS (SELECT 1 FROM jsonb_each(CASE WHEN jsonb_typeof(value) = 'object' THEN value ELSE '{}'::jsonb END) AS field(key, value)
      WHERE key IN ('kind', 'name', 'uid') AND jsonb_typeof(field.value) NOT IN ('string', 'null')))
THEN '"invalid owner references"'::jsonb
ELSE COALESCE((SELECT jsonb_agg(jsonb_build_object(
  'kind', value->'kind', 'name', value->'name', 'uid', value->'uid') ORDER BY ordinality)
  FROM jsonb_array_elements(data->'ownerReferences') WITH ORDINALITY AS owner(value, ordinality)), '[]'::jsonb)
END`
