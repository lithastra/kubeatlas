// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

var _ graph.HistoryEventReader = (*Store)(nil)

func (s *Store) ReadHistoryEvents(ctx context.Context, query graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.HistoryReadTimeout)
	defer cancel()
	q, err := query.Normalize()
	if err != nil {
		return graph.HistoryEventPage{}, err
	}
	b, err := graph.NewHistoryEventPageBuilder(q)
	if err != nil {
		return graph.HistoryEventPage{}, err
	}
	if err := s.lockProjectionRead(ctx); err != nil {
		return graph.HistoryEventPage{}, err
	}
	defer s.mu.RUnlock()
	// The source ring has at most maxMemoryEvents entries. Copy only scalar
	// metadata (never Data), then sort because append order need not be ts order.
	selected := make([]graph.HistoryEvent, 0)
	for _, e := range s.events {
		if err := ctx.Err(); err != nil {
			return graph.HistoryEventPage{}, err
		}
		if e.ClusterID != q.ClusterID || (q.Namespace != "" && e.Namespace != q.Namespace) ||
			e.Timestamp.Before(q.From) || e.Timestamp.After(q.To) || e.Kind == "Secret" {
			continue
		}
		selected = append(selected, graph.ProjectHistoryEvent(e))
	}
	sort.Slice(selected, func(i, j int) bool {
		if !selected[i].Timestamp.Equal(selected[j].Timestamp) {
			return selected[i].Timestamp.Before(selected[j].Timestamp)
		}
		return selected[i].ID < selected[j].ID
	})
	for _, e := range selected {
		if err := b.Add(ctx, e); err != nil {
			return graph.HistoryEventPage{}, err
		}
		if b.Page.Truncated {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return graph.HistoryEventPage{}, err
	}
	return b.Page, nil
}
