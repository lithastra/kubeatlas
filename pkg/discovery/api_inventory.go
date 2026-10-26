// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
)

// Narrow contextual seam avoids uncancellable requests in background workers.
type inventoryDiscoveryClient interface {
	ServerGroupsWithContext(context.Context) (*metav1.APIGroupList, error)
	ServerResourcesForGroupVersionWithContext(context.Context, string) (*metav1.APIResourceList, error)
}

func runAPIInventory(ctx context.Context, dc k8sdiscovery.DiscoveryInterface, tracker *operations.CoverageTracker, cluster string) {
	session, err := tracker.BeginAPIInventory(cluster)
	if err != nil {
		return
	}
	defer session.Stop()
	client, ok := dc.(inventoryDiscoveryClient)
	if !ok {
		return
	} // Unknown; no invented check or detached request.
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		state, resources, limited := discoverAPIInventory(probeCtx, client)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err := session.Record(state, resources, limited, time.Now()); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func discoverAPIInventory(ctx context.Context, dc inventoryDiscoveryClient) (operations.APIInventoryState, []operations.APIResourceDescriptor, bool) {
	if ctx.Err() != nil {
		return operations.APIInventoryFailed, nil, false
	}
	var groups *metav1.APIGroupList
	var err error
	var aggregatedIncomplete bool
	var aggregatedResources map[schema.GroupVersion]*metav1.APIResourceList
	var aggregatedFailures map[schema.GroupVersion]error
	if aggregated, ok := dc.(interface {
		GroupsAndMaybeResourcesWithContext(context.Context) (*metav1.APIGroupList, map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error, error)
	}); ok {
		groups, aggregatedResources, aggregatedFailures, err = aggregated.GroupsAndMaybeResourcesWithContext(ctx)
		// ServerGroupsWithContext drops aggregated stale/failure details. Their
		// missing resources must not become a successful absence assertion.
		aggregatedIncomplete = len(aggregatedFailures) != 0
	} else {
		groups, err = dc.ServerGroupsWithContext(ctx)
	}
	if ctx.Err() != nil {
		return operations.APIInventoryFailed, nil, false
	}
	if err != nil {
		return inventoryErrorState(err), nil, false
	}
	if groups == nil {
		if aggregatedIncomplete {
			return operations.APIInventoryPartial, nil, false
		}
		return operations.APIInventoryFailed, nil, false
	}
	versions := []string{"v1"} // Core discovery is queried independently of /apis.
	versionBudget := map[string]bool{"v1": true}
	failedGroups := map[string]bool{}
	if aggregatedIncomplete {
		if len(aggregatedFailures) > operations.MaxInventoryGroupVersions {
			return operations.APIInventoryPartial, nil, true
		}
		for gv := range aggregatedFailures {
			if ctx.Err() != nil {
				return operations.APIInventoryFailed, nil, false
			}
			if !operations.ValidAPIResourceDescriptor(operations.APIResourceDescriptor{Group: gv.Group, Version: gv.Version, Resource: "test", Kind: "Test"}) {
				return operations.APIInventoryFailed, nil, false
			}
			if !versionBudget[gv.String()] && len(versionBudget) >= operations.MaxInventoryGroupVersions {
				return operations.APIInventoryPartial, nil, true
			}
			versionBudget[gv.String()] = true
			failedGroups[gv.Group] = true
		}
	}
	seenGroups := map[string]bool{}
	for _, group := range groups.Groups {
		if ctx.Err() != nil {
			return operations.APIInventoryFailed, nil, false
		}
		// client-go includes the core group on some discovery paths.
		if seenGroups[group.Name] {
			return operations.APIInventoryFailed, nil, false
		}
		seenGroups[group.Name] = true
		if len(group.Versions) == 0 {
			if aggregatedIncomplete && failedGroups[group.Name] {
				continue // client-go removes stale versions, leaving empty groups.
			}
			return operations.APIInventoryFailed, nil, false
		}
		seenVersions := map[string]bool{}
		for _, version := range group.Versions {
			gv, err := schema.ParseGroupVersion(version.GroupVersion)
			if err != nil || gv.Group != group.Name || gv.Version != version.Version || !operations.ValidAPIResourceDescriptor(operations.APIResourceDescriptor{Group: gv.Group, Version: gv.Version, Resource: "test", Kind: "Test"}) {
				return operations.APIInventoryFailed, nil, false
			}
			if seenVersions[version.GroupVersion] {
				return operations.APIInventoryFailed, nil, false
			}
			seenVersions[version.GroupVersion] = true
			if group.Name == "" && version.GroupVersion == "v1" {
				continue
			}
			if slices.Contains(versions, version.GroupVersion) {
				return operations.APIInventoryFailed, nil, false
			}
			if !versionBudget[version.GroupVersion] && len(versionBudget) >= operations.MaxInventoryGroupVersions {
				return operations.APIInventoryPartial, nil, true
			}
			versionBudget[version.GroupVersion] = true
			versions = append(versions, version.GroupVersion)
		}
	}
	slices.Sort(versions)
	resources := []operations.APIResourceDescriptor{}
	state := operations.APIInventoryComplete
	if aggregatedIncomplete {
		state = operations.APIInventoryPartial
	}
	for _, version := range versions {
		if ctx.Err() != nil {
			return operations.APIInventoryFailed, nil, false
		}
		var list *metav1.APIResourceList
		var err error
		if aggregatedIncomplete {
			// Consume only healthy metadata already returned in this response.
			// Stale/missing versions never trigger a fallback request.
			gv, _ := schema.ParseGroupVersion(version)
			if _, failed := aggregatedFailures[gv]; failed {
				continue
			}
			list = aggregatedResources[gv]
		} else {
			list, err = dc.ServerResourcesForGroupVersionWithContext(ctx, version)
		}
		if ctx.Err() != nil {
			return operations.APIInventoryFailed, nil, false
		}
		if err != nil {
			if !apierrors.IsNotFound(err) {
				// Keep only this round's completed metadata. Rows accompanying the
				// failed response are untrusted, and no later request is started.
				if len(resources) != 0 {
					return operations.APIInventoryPartial, resources, false
				}
				return inventoryErrorState(err), nil, false
			}
			state = operations.APIInventoryPartial
			continue
		}
		// client-go tolerates core/v1 404 as an empty successful list.
		if list == nil || list.GroupVersion != version || (version == "v1" && len(list.APIResources) == 0) {
			state = operations.APIInventoryPartial
			continue
		}
		gv, _ := schema.ParseGroupVersion(version)
		seen := map[string]bool{}
		for _, resource := range list.APIResources {
			if ctx.Err() != nil {
				return operations.APIInventoryFailed, nil, false
			}
			if strings.Contains(resource.Name, "/") {
				continue
			} // Subresources are not independent graph objects.
			r := operations.APIResourceDescriptor{Group: gv.Group, Version: gv.Version, Resource: resource.Name, Kind: resource.Kind, Namespaced: resource.Namespaced, List: slices.Contains(resource.Verbs, "list"), Watch: slices.Contains(resource.Verbs, "watch")}
			if !operations.ValidAPIResourceDescriptor(r) || seen[r.Resource] || (resource.Group != "" && resource.Group != gv.Group) || (resource.Version != "" && resource.Version != gv.Version) {
				state = operations.APIInventoryPartial
				continue
			}
			seen[r.Resource] = true
			if len(resources) >= operations.MaxInventoryResources {
				return operations.APIInventoryPartial, resources, true
			}
			resources = append(resources, r)
		}
	}
	if ctx.Err() != nil {
		return operations.APIInventoryFailed, nil, false
	}
	return state, resources, false
}

func inventoryErrorState(err error) operations.APIInventoryState {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return operations.APIInventoryDenied
	}
	return operations.APIInventoryFailed
}
