// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package analysis hosts graph-level queries that build on the
// GraphStore primitives — blast radius (P2-T15), orphan and cycle
// detection (P2-T17/T18), etc.
package analysis

import (
	"context"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Options controls a BlastRadius call.
//
// MaxDepth defaults to 5 when unset; the underlying store clamps at
// 10. EdgeTypes restricts traversal to a subset of edge labels.
// IncludeSource controls whether the start resource itself appears
// in the result set.
type Options struct {
	MaxDepth      int
	EdgeTypes     []graph.EdgeType
	IncludeSource bool
}

// BlastRadius returns every resource that depends on the resource at
// startID — the transitive set reachable by walking incoming edges.
//
// "What breaks if I delete X?" is the operational question. We
// answer it by following BINDS_SUBJECT, USES_CONFIGMAP, OWNS, ...
// in reverse: anyone with an edge pointing at X.
//
// The starting resource is included in the result only when
// opts.IncludeSource is true. Callers that want a renderable
// subgraph usually want the source in; callers that want a count
// of *affected* resources usually want it out.
func BlastRadius(ctx context.Context, store graph.GraphStore, startID string, opts Options) ([]graph.Resource, error) {
	if startID == "" {
		return nil, fmt.Errorf("BlastRadius: startID is required")
	}
	out, err := store.Traverse(ctx, startID, graph.TraverseOptions{
		Direction: graph.DirectionIncoming,
		MaxDepth:  opts.MaxDepth,
		EdgeTypes: opts.EdgeTypes,
	})
	if err != nil {
		return nil, err
	}
	if opts.IncludeSource {
		src, err := store.GetResource(ctx, startID)
		if err != nil {
			return nil, err
		}
		out = append([]graph.Resource{src}, out...)
	}
	return out, nil
}
