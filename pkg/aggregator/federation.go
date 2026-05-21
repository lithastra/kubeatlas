// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"fmt"
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// FederatedView is the response served by /api/v1/federation/graph
// (P3-T22). It is a flat union of every resource in the selected
// member clusters plus every edge whose endpoints are both in the
// set. The UI groups nodes by ClusterID for the cluster switcher;
// nothing about the federation surface re-aggregates per-cluster
// namespaces (that would obscure exactly the cross-cluster comparison
// the federation view exists for).
//
// Schema rules:
//   - Every Node carries its source ClusterID so the UI can colour /
//     group by cluster.
//   - Every edge endpoint references a graph.Resource.ID() exactly as
//     the store stored it, including the ClusterID prefix added by
//     P3-T21. Dangling endpoints (resource missing) are dropped.
//   - v1.3 does NOT infer cross-cluster edges. Explicit annotation-
//     declared references (kubeatlas.io/external-ref) are deferred to
//     a follow-up; until then, "cross-cluster" means "explicit by
//     metadata", not "name-matched".
type FederatedView struct {
	// Clusters are the attached clusters this view spans, sorted for
	// stable JSON. Callers compare against /api/v1/federation/clusters.
	Clusters []string `json:"clusters"`

	Nodes []FederatedNode `json:"nodes"`
	Edges []AEdge         `json:"edges"`
}

// FederatedNode is the federation-view node. It mirrors the
// raw-resource shape from Node but always carries ClusterID so the
// UI can colour / group by it.
type FederatedNode struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // always "resource" in v1.3
	ClusterID string `json:"clusterId"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// MergeClusters returns a FederatedView covering the named clusters.
// An empty clusterIDs slice is an error — the caller must name at
// least one cluster (the federation handler enforces it on the
// query-string side).
//
// MergeClusters uses the cluster-scoped store methods landed in
// P3-T20 (ListResourcesInCluster + GetEdgesAcrossClusters) so it does
// not materialise the whole store — each member contributes only its
// own resources.
func MergeClusters(ctx context.Context, store graph.GraphStore, clusterIDs []string) (*FederatedView, error) {
	if len(clusterIDs) == 0 {
		return nil, fmt.Errorf("federation: no clusters selected")
	}
	// Sort + dedupe for deterministic output and so two requests with
	// the cluster list in different orders share a cache key.
	sorted := make([]string, 0, len(clusterIDs))
	seen := make(map[string]struct{}, len(clusterIDs))
	for _, c := range clusterIDs {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		sorted = append(sorted, c)
	}
	sort.Strings(sorted)

	view := &FederatedView{Clusters: sorted}
	for _, cID := range sorted {
		resources, err := store.ListResourcesInCluster(ctx, cID, graph.Filter{})
		if err != nil {
			return nil, fmt.Errorf("federation: list cluster %q: %w", cID, err)
		}
		for _, r := range resources {
			view.Nodes = append(view.Nodes, FederatedNode{
				ID:        r.ID(),
				Type:      "resource",
				ClusterID: r.ClusterID,
				Kind:      r.Kind,
				Namespace: r.Namespace,
				Name:      r.Name,
			})
		}
	}

	edges, err := store.GetEdgesAcrossClusters(ctx, sorted)
	if err != nil {
		return nil, fmt.Errorf("federation: edges: %w", err)
	}
	view.Edges = sortEdges(edges)

	// Deterministic node order so /federation/graph is cache-friendly.
	sort.Slice(view.Nodes, func(i, j int) bool { return view.Nodes[i].ID < view.Nodes[j].ID })
	return view, nil
}
