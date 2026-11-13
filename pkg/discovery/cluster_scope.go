// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ScopeEdgesToCluster copies rule/extractor edges into the owning member's
// identity space. Already-qualified endpoints must name that same member.
// Colons inside external identity names (after the first slash) are preserved.
// All endpoints are checked before any caller can persist a partial edge set.
func ScopeEdgesToCluster(edges []graph.Edge, cluster string) ([]graph.Edge, error) {
	if cluster == "" {
		return edges, nil
	}
	return ScopeCollectorEdges(edges, cluster)
}

// ScopeCollectorEdges explicitly binds a collector's output. Empty scope means
// unlabelled standalone identities, never all clusters. Validate the whole
// batch before callers materialize any Secret references or persist any edges.
// ScopeEdgesToCluster retains its legacy empty-scope pass-through contract.
func ScopeCollectorEdges(edges []graph.Edge, cluster string) ([]graph.Edge, error) {
	qualify := func(id string) (string, error) {
		if id == "" {
			return "", errors.New("empty collector edge endpoint")
		}
		colon, slash := strings.IndexByte(id, ':'), strings.IndexByte(id, '/')
		if colon >= 0 && (slash < 0 || colon < slash) {
			if cluster == "" || id[:colon] != cluster {
				return "", errors.New("edge endpoint outside collector scope")
			}
			return id, nil
		}
		if cluster == "" {
			return id, nil
		}
		return cluster + ":" + id, nil
	}
	out := make([]graph.Edge, len(edges))
	for i, e := range edges {
		var err error
		if e.From, err = qualify(e.From); err != nil {
			return nil, err
		}
		if e.To, err = qualify(e.To); err != nil {
			return nil, err
		}
		out[i] = e
	}
	return out, nil
}

// NewScopedResourceLister exposes only filtered reads in exactly one scope.
// Empty cluster explicitly selects unlabelled standalone rows. Extractor filters
// cannot broaden this scope; all other filters retain their existing semantics.
func NewScopedResourceLister(store graph.GraphStore, cluster string) graph.ResourceLister {
	return scopedResourceLister{store: store, cluster: cluster}
}

type scopedResourceLister struct {
	store   graph.GraphStore
	cluster string
}

func (l scopedResourceLister) ListResources(ctx context.Context, filter graph.Filter) ([]graph.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.store == nil {
		return nil, errors.New("missing collector store")
	}
	filter.ClusterID = l.cluster
	rows, err := l.store.ListResourcesInCluster(ctx, l.cluster, filter)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.ClusterID != l.cluster {
			return nil, errors.New("resource outside collector scope")
		}
	}
	return rows, nil
}
