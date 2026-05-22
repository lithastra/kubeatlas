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
	// Level is the zoom of the view:
	//   "resource" — one Node per resource across the named clusters
	//                (the original v1.3.0 default behaviour).
	//   "cluster"  — one Node per cluster with a kind summary; the
	//                small-payload form intended for cluster-switcher
	//                landing pages.
	Level string `json:"level"`

	// Clusters are the attached clusters this view spans, sorted for
	// stable JSON. Callers compare against /api/v1/federation/clusters.
	Clusters []string `json:"clusters"`

	Nodes []FederatedNode `json:"nodes"`
	Edges []AEdge         `json:"edges"`
}

// FederatedNode is the federation-view node. The union of fields
// supports two shapes:
//   - Type="resource": carries Kind / Namespace / Name (the raw
//     graph resource form). The default level=resource view emits
//     these.
//   - Type="cluster" : carries Label / ResourceCount /
//     NamespaceCount / KindSummary (the cluster summary form). The
//     level=cluster view emits these.
//
// Both shapes always carry ID and ClusterID. Fields not relevant to
// a node's Type are omitted from the JSON via omitempty.
type FederatedNode struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // "resource" | "cluster"
	ClusterID string `json:"clusterId"`

	// Raw-resource fields (Type="resource").
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`

	// Cluster-summary fields (Type="cluster"). KindSummary is folded
	// to summaryKindLimit entries with the long tail collapsed under
	// "Other", matching the single-cluster ClusterAggregator's
	// children_summary semantics.
	Label          string         `json:"label,omitempty"`
	ResourceCount  int            `json:"resourceCount,omitempty"`
	NamespaceCount int            `json:"namespaceCount,omitempty"`
	KindSummary    map[string]int `json:"kindSummary,omitempty"`
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

	view := &FederatedView{Level: "resource", Clusters: sorted}
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

// MergeClustersAtClusterLevel is the cluster-level zoom of the
// federation view: one Node per attached cluster, each carrying a
// resource count and a top-N kind summary. It is the small-payload
// counterpart to MergeClusters — at ~7K resources per cluster the
// resource-level view JSON-marshals ~15K nodes (~2.4s p95 on a 2-
// cluster fixture); this returns just N nodes and stays in the
// low-millisecond range regardless of cluster size.
//
// Edges are intentionally empty: v1.3 only knows intra-cluster
// edges, which collapse to noise at the cluster zoom (every cluster
// would be one self-loop). The federation surface adds cross-cluster
// edges in a follow-up release; this view will then carry them.
func MergeClustersAtClusterLevel(ctx context.Context, store graph.GraphStore, clusterIDs []string) (*FederatedView, error) {
	if len(clusterIDs) == 0 {
		return nil, fmt.Errorf("federation: no clusters selected")
	}
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

	view := &FederatedView{Level: "cluster", Clusters: sorted}
	for _, cID := range sorted {
		resources, err := store.ListResourcesInCluster(ctx, cID, graph.Filter{})
		if err != nil {
			return nil, fmt.Errorf("federation: list cluster %q: %w", cID, err)
		}
		namespaces := make(map[string]struct{})
		kinds := make(map[string]int, 32)
		for _, r := range resources {
			kinds[r.Kind]++
			if r.Namespace != "" {
				namespaces[r.Namespace] = struct{}{}
			}
		}
		view.Nodes = append(view.Nodes, FederatedNode{
			ID:             cID,
			Type:           "cluster",
			ClusterID:      cID,
			Label:          cID,
			ResourceCount:  len(resources),
			NamespaceCount: len(namespaces),
			KindSummary:    foldKindSummary(kinds, summaryKindLimit),
		})
	}
	return view, nil
}
