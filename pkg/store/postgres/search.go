// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// searchDefaultLimit caps a Search call whose SearchQuery.Limit is
// non-positive. The API handler clamps too; this is the store's own
// guard so a direct caller cannot ask for an unbounded result set.
const searchDefaultLimit = 50

// Search answers /api/v1/search from the GIN-indexed search_tsv
// column added by migration 006 (F-113). The whole query — match,
// rank, and limit — runs in PostgreSQL; no resource leaves the
// database that is not in the returned page.
//
// websearch_to_tsquery is deliberate: it parses arbitrary user
// input (unbalanced quotes, bare boolean operators) into a tsquery
// without ever raising a syntax error, so SearchQuery.Text is safe
// to pass straight through. When Text is empty the request is a
// pure field-filter query ("kind:Pod") — the @@ match is skipped
// and the kind / namespace predicates carry it.
//
// count(*) OVER() returns the pre-LIMIT match count on every row, so
// one query yields both the page and SearchResult.Total.
func (s *Store) Search(ctx context.Context, q graph.SearchQuery) (graph.SearchResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}

	const sql = `
		SELECT data, count(*) OVER() AS total
		FROM public.resources
		WHERE ($1::text = '' OR data->>'kind' = $1)
		  AND ($2::text = '' OR data->>'namespace' = $2)
		  AND ($3::text = '' OR search_tsv @@ websearch_to_tsquery('simple', $3))
		ORDER BY
		  ts_rank(search_tsv, websearch_to_tsquery('simple', $3)) DESC,
		  id
		LIMIT $4
	`
	rows, err := s.pool.Query(ctx, sql, q.Kind, q.Namespace, q.Text, limit)
	if err != nil {
		return graph.SearchResult{}, fmt.Errorf("postgres.Search: query: %w", err)
	}
	defer rows.Close()

	matches := make([]graph.Resource, 0, limit)
	total := 0
	for rows.Next() {
		var (
			body     []byte
			rowTotal int
		)
		if err := rows.Scan(&body, &rowTotal); err != nil {
			return graph.SearchResult{}, fmt.Errorf("postgres.Search: scan: %w", err)
		}
		total = rowTotal
		r, err := unmarshalStorageBlob(body, "search-result")
		if err != nil {
			return graph.SearchResult{}, err
		}
		matches = append(matches, r)
	}
	if err := rows.Err(); err != nil {
		return graph.SearchResult{}, fmt.Errorf("postgres.Search: rows: %w", err)
	}

	// Tier 2 is the indexed path — LinearScan stays false.
	return graph.SearchResult{Matches: matches, Total: total}, nil
}
