// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// OrphanReason classifies *why* a resource showed up in the orphan
// list. Distinct codes let the UI render different copy and let
// operators filter the list for the cases they care about.
type OrphanReason string

const (
	// ReasonOrphan: a resource that is not a top-level kind and has
	// zero incoming edges. Almost always a leftover from a delete
	// that K8s GC didn't catch.
	ReasonOrphan OrphanReason = "orphan"
	// ReasonStandalonePod: a Pod with no OwnerReference. Not strictly
	// an orphan — many users `kubectl run` ad-hoc Pods on purpose —
	// but worth surfacing because it's a common source of "why is
	// this Pod outliving the cluster's intent".
	ReasonStandalonePod OrphanReason = "standalone_pod"
)

// OrphanReport pairs a resource with the reason it was flagged.
// Returned by DetectOrphans in stable order so consumers can diff
// successive snapshots without sorting.
type OrphanReport struct {
	Resource graph.Resource `json:"resource"`
	Reason   OrphanReason   `json:"reason"`
}

// OrphanOptions narrows the detection scope. The zero value is
// fine for whole-cluster sweeps; Namespace is the common filter
// for per-tenant dashboards.
type OrphanOptions struct {
	Namespace string
}

// topLevelKinds names every Kind we treat as a legitimate root in
// the dependency graph. These never count as orphans even when
// they have no incoming edges.
//
// The list mixes two categories:
//
//   - Cluster-scoped resources users create directly (Namespace,
//     Node, PersistentVolume, StorageClass, ClusterRole,
//     ClusterRoleBinding, CustomResourceDefinition).
//   - Namespaced resources that are conventionally created by
//     humans / GitOps (Deployment, StatefulSet, DaemonSet, Service,
//     Ingress, ConfigMap, Secret, ServiceAccount, Role,
//     RoleBinding, Job, CronJob, PersistentVolumeClaim, NetworkPolicy).
//
// The intent is "if this kind has no incoming edges, that is the
// expected state, not a leak" — anything else with no incoming
// edges is suspicious.
var topLevelKinds = map[string]bool{
	// Cluster-scoped roots.
	"Namespace":                true,
	"Node":                     true,
	"PersistentVolume":         true,
	"StorageClass":             true,
	"ClusterRole":              true,
	"ClusterRoleBinding":       true,
	"CustomResourceDefinition": true,
	// Namespaced kinds users / GitOps systems author directly.
	"Deployment":            true,
	"StatefulSet":           true,
	"DaemonSet":             true,
	"Service":               true,
	"Ingress":               true,
	"Gateway":               true,
	"HTTPRoute":             true,
	"ConfigMap":             true,
	"Secret":                true,
	"ServiceAccount":        true,
	"Role":                  true,
	"RoleBinding":           true,
	"Job":                   true,
	"CronJob":               true,
	"PersistentVolumeClaim": true,
	"NetworkPolicy":         true,
}

// DetectOrphans walks the graph and returns every resource that is
// either a non-top-level kind with zero incoming edges or a Pod
// without an OwnerReference. Result order matches Snapshot's
// resource order so reports stay diffable across runs.
//
// Tier 1 cost is O(R) where R is the resource count; the in-memory
// store's ListIncoming is O(1) per call. On Tier 2 the same
// interface methods compile to short Cypher reads. Caching is
// intentionally out of scope here — the Phase 2 budget for full-
// cluster sweeps is measured in single-digit milliseconds even on
// a 5K-resource graph, well below the API request budget.
func DetectOrphans(ctx context.Context, store graph.GraphStore, opts OrphanOptions) ([]OrphanReport, error) {
	snap, err := store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OrphanReport, 0)
	for _, r := range snap.Resources {
		if opts.Namespace != "" && r.Namespace != opts.Namespace {
			continue
		}

		// Pods are leaves — they are the *targets* of OWNS edges
		// (their owner emits the edge), so a healthy Pod always
		// has zero incoming OWNS edges. A no-owner Pod is its
		// own category — not strictly an orphan, but worth a
		// flag — and a Pod with an owner is normal regardless of
		// incoming-edge count, so we skip the generic check.
		if r.Kind == "Pod" {
			if len(r.OwnerReferences) == 0 {
				out = append(out, OrphanReport{Resource: r, Reason: ReasonStandalonePod})
			}
			continue
		}

		if topLevelKinds[r.Kind] {
			continue
		}

		incoming, err := store.ListIncoming(ctx, r.ID())
		if err != nil {
			return nil, err
		}
		if len(incoming) == 0 {
			out = append(out, OrphanReport{Resource: r, Reason: ReasonOrphan})
		}
	}
	return out, nil
}
