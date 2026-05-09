// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// buildResourceDetailV1 enriches the v1alpha1 detail bundle with
// the GA-only fields the v1 surface advertises. Each enrichment
// is a separate analysis-package call against the same store,
// so the work is bounded by the existing query-path budgets:
//
//   - blastRadiusCount: Traverse(Direction=Incoming, MaxDepth=5)
//   - isOrphan:         a single ListIncoming + the same top-level
//                       whitelist DetectOrphans uses
//   - inCycle:          DetectCycles full sweep, then membership
//                       lookup. O(V+E); acceptable here because
//                       the resource-detail endpoint is interactive
//                       (one Pod / Deployment at a time, not a list
//                       view sweep).
//
// Failures in any single enrichment fall back to a zero value
// rather than failing the response — the v1alpha1 fields the user
// already had a path to are non-negotiable; the v1 enrichments
// are best-effort by design.
func (s *Server) buildResourceDetailV1(r *http.Request, res graph.Resource, in, out []graph.Edge) ResourceDetailResponseV1 {
	ctx := r.Context()
	id := res.ID()

	var blastRadiusCount int
	if affected, err := analysis.BlastRadius(ctx, s.store, id, analysis.Options{}); err == nil {
		blastRadiusCount = len(affected)
	}

	isOrphan := isOrphanResource(res, in)

	var inCycle bool
	if cycles, err := analysis.DetectCycles(ctx, s.store); err == nil {
		for _, c := range cycles {
			for _, m := range c.Members {
				if m.ID() == id {
					inCycle = true
					break
				}
			}
			if inCycle {
				break
			}
		}
	}

	return ResourceDetailResponseV1{
		Resource:         res,
		Incoming:         in,
		Outgoing:         out,
		BlastRadiusCount: blastRadiusCount,
		IsOrphan:         isOrphan,
		InCycle:          inCycle,
	}
}

// isOrphanResource decides per-resource whether DetectOrphans
// would flag this exact resource. Mirrors the rules in
// pkg/graph/analysis/orphans.go so a single resource detail
// request never disagrees with /api/v1alpha1/orphans.
func isOrphanResource(res graph.Resource, in []graph.Edge) bool {
	if res.Kind == "Pod" {
		return len(res.OwnerReferences) == 0
	}
	if v1TopLevelKinds[res.Kind] {
		return false
	}
	return len(in) == 0
}

// v1TopLevelKinds duplicates pkg/graph/analysis.topLevelKinds.
// Kept as a sibling here rather than re-exported because the
// analysis package's whitelist is intentionally unexported (it's
// the canonical answer for one specific question and we don't
// want callers stitching their own variants).
var v1TopLevelKinds = map[string]bool{
	"Namespace":                true,
	"Node":                     true,
	"PersistentVolume":         true,
	"StorageClass":             true,
	"ClusterRole":              true,
	"ClusterRoleBinding":       true,
	"CustomResourceDefinition": true,
	"Deployment":               true,
	"StatefulSet":              true,
	"DaemonSet":                true,
	"Service":                  true,
	"Ingress":                  true,
	"Gateway":                  true,
	"HTTPRoute":                true,
	"ConfigMap":                true,
	"Secret":                   true,
	"ServiceAccount":           true,
	"Role":                     true,
	"RoleBinding":              true,
	"Job":                      true,
	"CronJob":                  true,
	"PersistentVolumeClaim":    true,
	"NetworkPolicy":            true,
}
