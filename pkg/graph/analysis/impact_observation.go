// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var ErrImpactObservation = errors.New("impact: invalid observation evidence")

// ImpactCoverageProvider returns sanitized evidence for exactly the requested
// cluster/types, honoring cancellation. Cluster visibility must be authorized by
// the caller BEFORE invoking the observed analysis, including these reads.
type ImpactCoverageProvider interface {
	SnapshotCoverage(context.Context, string, []schema.GroupVersionResource) (operations.CoverageSnapshot, error)
}

type ImpactCoverageFacet struct {
	Profile               string                             `json:"profile"`
	State                 operations.CoverageState           `json:"state"`
	TypeScopeClosed       bool                               `json:"typeScopeClosed"`
	StableWindow          bool                               `json:"stableWindow"`
	Reasons               []string                           `json:"reasons"`
	Before                operations.CoverageSnapshot        `json:"before"`
	After                 operations.CoverageSnapshot        `json:"after"`
	EmptyResultAssessment string                             `json:"emptyResultAssessment"`
	OptionalAPIs          []ImpactOptionalAPI                `json:"optionalApis,omitempty"`
	UnobservedAPIs        []operations.APIResourceDescriptor `json:"unobservedApis,omitempty"`
}

// The window brackets one immutable graph read. It is not a transaction across
// Kubernetes, the recorder, and the store. It cannot establish effective RBAC,
// historical completeness, or safety of a change. Snapshot/history availability
// and live storage probes remain separate from current graph observations.
type ImpactObservation struct {
	Ordinary      ImpactCoverageFacet `json:"ordinary"`
	Authorization ImpactCoverageFacet `json:"authorization"`
}

type impactObservedType struct {
	kind          string
	gvr           schema.GroupVersionResource
	authorization bool
}

// A static requirement, NOT a list inferred from successful query matches. An
// empty/incomplete graph cannot erase its own missing watchers. OWNS accepts
// arbitrary kinds and dynamic collectors also write the graph, so the ordinary
// profile is a conservative minimum, never proof of a closed type universe.
// Optional APIs stay explicit unknown until their absence is independently
// established; lack of stored objects is not proof that an API is absent.
var impactObservedTypes = [...]impactObservedType{
	{"Namespace", schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, false},
	{"Pod", schema.GroupVersionResource{Version: "v1", Resource: "pods"}, true},
	{"Service", schema.GroupVersionResource{Version: "v1", Resource: "services"}, false},
	{"ConfigMap", schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, false},
	{"PersistentVolumeClaim", schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, false},
	{"ServiceAccount", schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, true},
	{"Deployment", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, true},
	{"ReplicaSet", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, true},
	{"StatefulSet", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, true},
	{"DaemonSet", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, true},
	{"Job", schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, true},
	{"CronJob", schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, true},
	{"HorizontalPodAutoscaler", schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, false},
	{"Ingress", schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, false},
	{"NetworkPolicy", schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, false},
	{"Gateway", schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, false},
	{"HTTPRoute", schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}, false},
	{"Role", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, true},
	{"RoleBinding", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, true},
	{"ClusterRole", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, true},
	{"ClusterRoleBinding", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, true},
	{"ClusterPolicy", schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}, false},
	{"Policy", schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "policies"}, false},
	{"PolicyReport", schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"}, false},
	{"ClusterPolicyReport", schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"}, false},
	{"Route", schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}, false},
}

func impactCoverageRequirements(authorization bool) []schema.GroupVersionResource {
	out := make([]schema.GroupVersionResource, 0, len(impactObservedTypes))
	for _, resource := range impactObservedTypes {
		if !authorization || resource.authorization {
			out = append(out, resource.gvr)
		}
	}
	return out
}

func readImpactCoverage(ctx context.Context, provider ImpactCoverageProvider, cluster string) (operations.CoverageSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return operations.CoverageSnapshot{}, err
	}
	required := impactCoverageRequirements(false)
	start := time.Now()
	if provider == nil {
		return operations.CoverageSnapshot{ClusterID: cluster, CapturedAt: start, State: operations.CoverageUnknown}, nil
	}
	snapshot, err := provider.SnapshotCoverage(ctx, cluster, slices.Clone(required))
	if err != nil {
		// Do not wrap potentially sensitive provider error text.
		if ctx.Err() != nil {
			return operations.CoverageSnapshot{}, ctx.Err()
		}
		return operations.CoverageSnapshot{}, ErrImpactObservation
	}
	if err := ctx.Err(); err != nil {
		return operations.CoverageSnapshot{}, err
	}
	if snapshot.ClusterID != cluster || snapshot.CapturedAt.Before(start) || snapshot.CapturedAt.After(time.Now()) || len(snapshot.Resources) != len(required) {
		return operations.CoverageSnapshot{}, ErrImpactObservation
	}
	snapshot.Resources = slices.Clone(snapshot.Resources)
	seen := make(map[schema.GroupVersionResource]bool)
	for i, row := range snapshot.Resources {
		gvr := schema.GroupVersionResource{Group: row.Group, Version: row.Version, Resource: row.Resource}
		if !slices.Contains(required, gvr) || seen[gvr] || !validImpactCoverageRow(row, snapshot.CapturedAt) {
			return operations.CoverageSnapshot{}, ErrImpactObservation
		}
		seen[gvr] = true
		snapshot.Resources[i].Reasons = slices.Clone(row.Reasons)
	}
	if err := validateImpactSources(&snapshot); err != nil {
		return operations.CoverageSnapshot{}, err
	}
	if err := validateImpactInventory(&snapshot); err != nil {
		return operations.CoverageSnapshot{}, err
	}
	return snapshot, nil
}

func validImpactCoverageRow(row operations.ResourceCoverage, captured time.Time) bool {
	switch row.State {
	case operations.CoverageUnknown, operations.CoverageInitializing, operations.CoverageObserved, operations.CoveragePartial, operations.CoverageStale:
	default:
		return false
	}
	known := []operations.CoverageReason{operations.CoverageNotWatched, operations.CoverageInitialPending,
		operations.CoverageListFailed, operations.CoverageListDenied, operations.CoverageWatchPending,
		operations.CoverageWatchClosed, operations.CoverageWatchFailed, operations.CoverageWatchDenied,
		operations.CoverageCollectorStopped, operations.CoverageProcessingFailed,
		operations.CoverageExtractionFailed, operations.CoveragePersistenceFailed}
	if len(row.Reasons) > len(known) {
		return false
	}
	for i, reason := range row.Reasons {
		if !slices.Contains(known, reason) || slices.Contains(row.Reasons[:i], reason) {
			return false
		}
	}
	for _, at := range []time.Time{row.Since, row.LastListSuccess, row.WatchGapSince, row.ProcessingGapSince, row.ExtractionGapSince, row.PersistenceGapSince} {
		if at.After(captured) {
			return false
		}
	}
	if row.State == operations.CoverageObserved && (!row.InitialDeliveryDone || !row.WatchEstablished || len(row.Reasons) > 0 || row.Since.IsZero() || row.ContinuityRevision == 0 || !row.WatchGapSince.IsZero() || !row.ProcessingGapSince.IsZero() || !row.ExtractionGapSince.IsZero() || !row.PersistenceGapSince.IsZero()) {
		return false
	}
	return true
}

func attachImpactObservation(result *ImpactResult, before, after operations.CoverageSnapshot, available bool) {
	ordinary := evaluateImpactCoverage(before, after, false, available)
	ordinary.TypeScopeClosed = false
	ordinary.Reasons = append(ordinary.Reasons, "dynamic_source_scope_unverified")
	if !knownImpactIdentitySchema(result.Root, false) && !result.Root.ReferenceOnly {
		ordinary.Reasons = append(ordinary.Reasons, "root_schema_unverified")
	}
	if ordinary.State == operations.CoverageObserved {
		ordinary.State = operations.CoveragePartial
	}
	ordinary.EmptyResultAssessment = impactEmptyAssessment(result.Counts.Total, result.ModeledTraversalComplete, ordinary)
	result.ObservationCoverage = string(ordinary.State)
	authorization := evaluateImpactCoverage(before, after, true, available)
	authorization.TypeScopeClosed = result.Authorization.Applicable
	if !result.Authorization.Applicable {
		authorization.State = operations.CoverageUnknown
		authorization.Reasons = append(authorization.Reasons, "authorization_not_applicable")
		authorization.EmptyResultAssessment = "not_applicable"
	} else {
		known := knownImpactIdentitySchema(result.Root, true)
		for _, match := range result.Authorization.Resources {
			known = known && knownImpactIdentitySchema(match.Resource, true)
		}
		if !known {
			authorization.TypeScopeClosed = false
			authorization.Reasons = append(authorization.Reasons, "resource_schema_unverified")
			if authorization.State == operations.CoverageObserved {
				authorization.State = operations.CoveragePartial
			}
		}
		authorization.EmptyResultAssessment = impactEmptyAssessment(result.Authorization.Counts.Total, result.Authorization.ModeledTraversalComplete, authorization)
	}
	result.Authorization.ObservationCoverage = string(authorization.State)
	result.Observation = &ImpactObservation{Ordinary: ordinary, Authorization: authorization}
}

func knownImpactIdentitySchema(identity ImpactIdentity, authorization bool) bool {
	for _, resource := range impactObservedTypes {
		if identity.Kind == resource.kind && (!authorization || resource.authorization) {
			return identity.GroupVersion == resource.gvr.GroupVersion().String()
		}
	}
	return false
}

func evaluateImpactCoverage(before, after operations.CoverageSnapshot, authorization, available bool) ImpactCoverageFacet {
	required := impactCoverageRequirements(authorization)
	out := ImpactCoverageFacet{Profile: "ordinary_minimum_v1", State: operations.CoverageUnknown, Reasons: []string{}}
	if authorization {
		out.Profile = "builtin_authorization_v1"
	}
	out.Before, out.After = subsetImpactCoverage(before, required), subsetImpactCoverage(after, required)
	if !available {
		out.Reasons = append(out.Reasons, "coverage_provider_unavailable")
		return out
	}
	stable := before.Generation != 0 && before.Generation == after.Generation
	if !stable {
		out.Reasons = append(out.Reasons, "collector_generation_unverified_or_changed")
	}
	for i, pre := range out.Before.Resources {
		post := out.After.Resources[i]
		if pre.ContinuityRevision == 0 || !reflect.DeepEqual(pre, post) {
			stable = false
		}
	}
	out.StableWindow = stable
	if !stable {
		out.Reasons = append(out.Reasons, "observation_window_unstable")
	}
	out.State = worseImpactCoverage(out.Before.State, out.After.State)
	if out.Before.State != operations.CoverageObserved || out.After.State != operations.CoverageObserved {
		out.Reasons = append(out.Reasons, "required_type_coverage_incomplete")
	}
	if !stable && out.State == operations.CoverageObserved {
		out.State = operations.CoveragePartial
	}
	if !authorization {
		attachImpactSourceCoverage(&out, before.Sources, after.Sources)
		attachImpactInventory(&out, before, after)
	}
	return out
}

func subsetImpactCoverage(snapshot operations.CoverageSnapshot, required []schema.GroupVersionResource) operations.CoverageSnapshot {
	out := operations.CoverageSnapshot{ClusterID: snapshot.ClusterID, Generation: snapshot.Generation, CapturedAt: snapshot.CapturedAt, State: operations.CoverageObserved, Resources: []operations.ResourceCoverage{}}
	unknown := 0
	for _, gvr := range required {
		row := operations.ResourceCoverage{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, State: operations.CoverageUnknown, Reasons: []operations.CoverageReason{operations.CoverageNotWatched}}
		for _, candidate := range snapshot.Resources {
			if candidate.Group == gvr.Group && candidate.Version == gvr.Version && candidate.Resource == gvr.Resource {
				row = candidate
				row.Reasons = slices.Clone(candidate.Reasons)
				break
			}
		}
		out.Resources = append(out.Resources, row)
		if row.State == operations.CoverageUnknown {
			unknown++
		}
		out.State = worseImpactCoverage(out.State, row.State)
	}
	if out.State == operations.CoverageUnknown && unknown < len(required) {
		out.State = operations.CoveragePartial
	}
	return out
}

func worseImpactCoverage(a, b operations.CoverageState) operations.CoverageState {
	order := []operations.CoverageState{operations.CoverageObserved, operations.CoverageUnknown, operations.CoverageInitializing, operations.CoveragePartial, operations.CoverageStale}
	if slices.Index(order, b) > slices.Index(order, a) {
		return b
	}
	return a
}

func impactEmptyAssessment(count int, traversalComplete bool, coverage ImpactCoverageFacet) string {
	if count != 0 {
		return "not_empty"
	}
	if traversalComplete && coverage.TypeScopeClosed && coverage.StableWindow && coverage.State == operations.CoverageObserved {
		return "none_found_in_analyzed_scope"
	}
	return "none_observed_incomplete"
}
