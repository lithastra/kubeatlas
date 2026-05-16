// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// LabelStats tallies every label key/value across the cluster for
// GET /api/v1/labels (F-114).
//
// jsonb_each_text expands each resource's labels object into (key,
// value) rows; the GROUP BY then counts resources per (key, value)
// pair entirely in PostgreSQL — no resource row is materialised in
// the API process. A resource with no labels (data->'labels' is
// JSON null or absent) contributes no rows, since jsonb_each_text of
// a non-object yields the empty set.
//
// The (key, value, count) rows are folded into the sorted, capped
// []LabelStat shape by graph.FoldLabelStats, shared with the Tier 1
// store so both tiers cap and order identically.
func (s *Store) LabelStats(ctx context.Context) ([]graph.LabelStat, error) {
	const sql = `
		SELECT l.key, l.value, COUNT(*) AS cnt
		FROM resources, jsonb_each_text(resources.data->'labels') AS l(key, value)
		GROUP BY l.key, l.value
	`
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("postgres.LabelStats: query: %w", err)
	}
	defer rows.Close()

	byKey := make(map[string][]graph.LabelValue)
	for rows.Next() {
		var (
			key, value string
			cnt        int64
		)
		if err := rows.Scan(&key, &value, &cnt); err != nil {
			return nil, fmt.Errorf("postgres.LabelStats: scan: %w", err)
		}
		byKey[key] = append(byKey[key], graph.LabelValue{Value: value, Count: int(cnt)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres.LabelStats: rows: %w", err)
	}
	return graph.FoldLabelStats(byKey), nil
}
