// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"reflect"
	"slices"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/operations"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ImpactAPIPresence string

const (
	ImpactAPIUnknown       ImpactAPIPresence = "unknown"
	ImpactAPIAdvertised    ImpactAPIPresence = "advertised"
	ImpactAPINotAdvertised ImpactAPIPresence = "not_advertised"
	ImpactAPIOtherVersion  ImpactAPIPresence = "other_version_advertised"
	ImpactAPIUnsupported   ImpactAPIPresence = "unsupported_shape"
)

type ImpactOptionalAPI struct {
	Group    string            `json:"group"`
	Version  string            `json:"version"`
	Resource string            `json:"resource"`
	Before   ImpactAPIPresence `json:"before"`
	After    ImpactAPIPresence `json:"after"`
}

func optionalImpactAPI(gvr schema.GroupVersionResource) bool {
	switch gvr.Group {
	case "gateway.networking.k8s.io", "kyverno.io", "wgpolicyk8s.io", "route.openshift.io":
		return true
	default:
		return false
	}
}

func validateImpactInventory(snapshot *operations.CoverageSnapshot) error {
	if snapshot.APIInventory == nil {
		return nil
	}
	e := snapshot.APIInventory
	if e.ClusterID != snapshot.ClusterID || e.Generation == 0 || !operations.ValidAPIInventoryState(e.State) || e.CheckedAt.After(snapshot.CapturedAt) || e.Stale != (!e.CheckedAt.IsZero() && snapshot.CapturedAt.Sub(e.CheckedAt) >= operations.APIInventoryMaxAge) || len(e.Resources) > operations.MaxInventoryResources || (e.State == operations.APIInventoryComplete && e.Limited) {
		return ErrImpactObservation
	}
	if e.State == operations.APIInventoryUnknown {
		if e.Revision != 0 || !e.CheckedAt.IsZero() || len(e.Resources) != 0 || e.Limited {
			return ErrImpactObservation
		}
	} else if e.Revision == 0 || e.CheckedAt.IsZero() {
		return ErrImpactObservation
	}
	if (e.State == operations.APIInventoryDenied || e.State == operations.APIInventoryFailed) && len(e.Resources) != 0 {
		return ErrImpactObservation
	}
	seen := map[schema.GroupVersionResource]bool{}
	versions := map[schema.GroupVersion]bool{}
	for _, r := range e.Resources {
		if !operations.ValidAPIResourceDescriptor(r) || seen[r.GVR()] {
			return ErrImpactObservation
		}
		seen[r.GVR()] = true
		versions[r.GVR().GroupVersion()] = true
	}
	if len(versions) > operations.MaxInventoryGroupVersions {
		return ErrImpactObservation
	}
	snapshot.APIInventory = operations.CloneAPIInventory(e)
	slices.SortFunc(snapshot.APIInventory.Resources, func(a, b operations.APIResourceDescriptor) int {
		return strings.Compare(a.GVR().String(), b.GVR().String())
	})
	return nil
}

func qualifiedImpactInventory(e *operations.APIInventoryEvidence) bool {
	return e != nil && e.State == operations.APIInventoryComplete && !e.Stale && !e.Stopped && !e.Limited
}

func impactAPIPresence(e *operations.APIInventoryEvidence, required impactObservedType) ImpactAPIPresence {
	if !qualifiedImpactInventory(e) {
		return ImpactAPIUnknown
	}
	other := false
	for _, r := range e.Resources {
		if r.Group != required.gvr.Group || r.Resource != required.gvr.Resource {
			continue
		}
		if r.Version != required.gvr.Version {
			other = true
			continue
		}
		wantNamespaced := required.kind != "ClusterPolicy" && required.kind != "ClusterPolicyReport"
		if r.Kind != required.kind || !r.List || !r.Watch || r.Namespaced != wantNamespaced {
			return ImpactAPIUnsupported
		}
		return ImpactAPIAdvertised
	}
	if other {
		return ImpactAPIOtherVersion
	}
	return ImpactAPINotAdvertised
}

func attachImpactInventory(out *ImpactCoverageFacet, before, after operations.CoverageSnapshot) {
	pre, post := before.APIInventory, after.APIInventory
	out.Before.APIInventory, out.After.APIInventory = pre, post
	if pre == nil || post == nil {
		out.Reasons = append(out.Reasons, "api_inventory_unavailable")
	} else {
		if !reflect.DeepEqual(pre, post) {
			out.StableWindow = false
			out.Reasons = append(out.Reasons, "api_inventory_window_unstable")
		}
		if pre.Stale || post.Stale {
			out.Reasons = append(out.Reasons, "api_inventory_stale")
		}
		if pre.Stopped || post.Stopped {
			out.Reasons = append(out.Reasons, "api_inventory_stopped")
		}
		if pre.Limited || post.Limited {
			out.Reasons = append(out.Reasons, "api_inventory_limited")
		}
		if pre.State != operations.APIInventoryComplete || post.State != operations.APIInventoryComplete {
			out.Reasons = append(out.Reasons, "api_inventory_incomplete")
		}
	}
	for _, required := range impactObservedTypes {
		if optionalImpactAPI(required.gvr) {
			out.OptionalAPIs = append(out.OptionalAPIs, ImpactOptionalAPI{Group: required.gvr.Group, Version: required.gvr.Version, Resource: required.gvr.Resource, Before: impactAPIPresence(pre, required), After: impactAPIPresence(post, required)})
		}
	}
	if qualifiedImpactInventory(post) {
		for _, r := range post.Resources {
			if !r.List || !r.Watch || (r.Group == "" && r.Resource == "secrets") {
				continue
			}
			registered := false
			for _, row := range after.Resources {
				if row.Group == r.Group && row.Version == r.Version && row.Resource == r.Resource && row.ContinuityRevision != 0 && !slices.Contains(row.Reasons, operations.CoverageCollectorStopped) {
					registered = true
					break
				}
			}
			for _, source := range after.Sources {
				for _, row := range source.Resources {
					if row.Group == r.Group && row.Version == r.Version && row.Resource == r.Resource && !slices.Contains(row.Reasons, operations.CoverageCollectorStopped) {
						registered = true
						break
					}
				}
			}
			if !registered {
				out.UnobservedAPIs = append(out.UnobservedAPIs, r)
			}
		}
		if len(out.UnobservedAPIs) != 0 {
			out.Reasons = append(out.Reasons, "advertised_resource_types_unobserved")
		}
	}
}
