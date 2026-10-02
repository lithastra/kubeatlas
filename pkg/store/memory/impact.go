// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

var _ graph.ImpactSnapshotter = (*Store)(nil)

func (s *Store) SnapshotImpact(ctx context.Context, opts graph.ImpactSnapshotOptions) (*graph.Graph, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.ImpactReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	builder, err := graph.NewImpactProjectionBuilder(opts)
	if err != nil {
		return nil, err
	}
	if err := s.lockProjectionRead(ctx); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	for _, resource := range s.resources {
		if err := builder.AddResource(ctx, resource); err != nil {
			return nil, err
		}
	}
	for _, peers := range s.outgoing {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, edge := range peers {
			if err := builder.AddEdge(ctx, edge); err != nil {
				return nil, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return builder.Graph, nil
}

// RWMutex.RLock cannot observe cancellation while waiting for a writer. Polling
// TryRLock avoids an abandoned lock-acquisition goroutine after a timeout.
func (s *Store) lockProjectionRead(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.mu.TryRLock() {
		return nil
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.mu.TryRLock() {
				return nil
			}
		}
	}
}
