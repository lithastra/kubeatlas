// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apidiscovery "k8s.io/api/apidiscovery/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

type aggregatedInventoryStub struct {
	inventoryStub
	read func(context.Context) (*metav1.APIGroupList, map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error, error)
}

func (s aggregatedInventoryStub) GroupsAndMaybeResourcesWithContext(ctx context.Context) (*metav1.APIGroupList, map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error, error) {
	return s.read(ctx)
}

func TestAPIInventoryAggregatedRetainsOnlyHealthyAdvertisedMetadata(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, mode := range []string{"healthy", "stale-only-group", "stale-row", "missing-row", "nil-row", "nil-map", "nil-groups", "nil-failure", "wrong-version", "malformed-row", "unadvertised", "top-error", "cancel", "descriptor-cap", "version-cap", "stale-cap", "combined-cap", "invalid-failure", "unexplained-empty-group"} {
			t.Run(fmt.Sprintf("cluster=%s/%s", cluster, mode), func(t *testing.T) {
				testAggregatedInventory(t, cluster, mode)
			})
		}
	}
}

func testAggregatedInventory(t *testing.T, cluster, mode string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	groups := partialInventoryGroups()
	gateway, _ := schema.ParseGroupVersion(partialInventoryVersions[0])
	route, _ := schema.ParseGroupVersion(partialInventoryVersions[1])
	core := schema.GroupVersion{Version: "v1"}
	rows := map[schema.GroupVersion]*metav1.APIResourceList{
		gateway: partialInventoryResources(gateway.String()),
		route:   partialInventoryResources(route.String()),
		core:    partialInventoryResources(core.String()),
	}
	rows[gateway].APIResources = append(rows[gateway].APIResources, metav1.APIResource{Name: "gateways/status", Kind: "Gateway"})
	failed := map[schema.GroupVersion]error{route: errors.New("aggregated-stale-canary")}
	want, count, limited := operations.APIInventoryPartial, 2, false
	var topErr error
	switch mode {
	case "stale-only-group":
		groups.Groups[1].Versions = nil // client-go omits stale versions from its group list.
	case "stale-row":
		rows[route].APIResources[0].Kind = "UntrustedStaleCanary"
	case "missing-row":
		delete(rows, gateway)
		count = 1
	case "nil-row":
		rows[gateway] = nil
		count = 1
	case "nil-map":
		rows, count = nil, 0
	case "nil-groups":
		groups, count = nil, 0
	case "nil-failure":
		failed[route] = nil // Membership, not the error text/value, rejects a version.
	case "wrong-version":
		rows[gateway].GroupVersion = route.String()
		count = 1
	case "malformed-row":
		rows[gateway].APIResources[0].Name = "invalid resource"
		count = 1
	case "unadvertised":
		rows[schema.GroupVersion{Group: "foreign.example", Version: "v1"}] = &metav1.APIResourceList{GroupVersion: "foreign.example/v1", APIResources: []metav1.APIResource{{Name: "foreign", Kind: "ForeignCanary"}}}
	case "top-error":
		topErr = errors.New("aggregated-top-error-canary")
		want, count = operations.APIInventoryFailed, 0
	case "cancel":
		want, count = operations.APIInventoryFailed, 0
	case "descriptor-cap":
		rows[gateway].APIResources = nil
		for i := range operations.MaxInventoryResources + 1 {
			rows[gateway].APIResources = append(rows[gateway].APIResources, metav1.APIResource{Name: fmt.Sprintf("resource%d", i), Kind: "Widget"})
		}
		count, limited = operations.MaxInventoryResources, true
	case "version-cap":
		for i := range operations.MaxInventoryGroupVersions {
			name := fmt.Sprintf("group%d.example", i)
			groups.Groups = append(groups.Groups, metav1.APIGroup{Name: name, Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: name + "/v1", Version: "v1"}}})
		}
		count, limited = 0, true
	case "stale-cap":
		for i := range operations.MaxInventoryGroupVersions {
			failed[schema.GroupVersion{Group: fmt.Sprintf("stale%d.example", i), Version: "v1"}] = errors.New("stale")
		}
		count, limited = 0, true
	case "combined-cap":
		for i := range operations.MaxInventoryGroupVersions - 2 {
			failed[schema.GroupVersion{Group: fmt.Sprintf("stale%d.example", i), Version: "v1"}] = errors.New("stale")
		}
		count, limited = 0, true
	case "invalid-failure":
		failed[schema.GroupVersion{Group: "invalid group", Version: "v1"}] = errors.New("stale")
		want, count = operations.APIInventoryFailed, 0
	case "unexplained-empty-group":
		groups.Groups = append(groups.Groups, metav1.APIGroup{Name: "unexplained.example"})
		want, count = operations.APIInventoryFailed, 0
	}
	reads := 0
	client := aggregatedInventoryStub{inventoryStub: inventoryStub{
		groups: func(context.Context) (*metav1.APIGroupList, error) {
			t.Fatal("aggregated result fell back to group discovery")
			return nil, nil
		},
		resources: func(context.Context, string) (*metav1.APIResourceList, error) {
			t.Fatal("partial aggregated result fetched a version")
			return nil, nil
		},
	}, read: func(context.Context) (*metav1.APIGroupList, map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error, error) {
		reads++
		if mode == "cancel" {
			cancel()
		}
		return groups, rows, failed, topErr
	}}
	state, resources, gotLimited := discoverAPIInventory(ctx, client)
	if state != want || len(resources) != count || gotLimited != limited || reads != 1 {
		t.Fatal("aggregated discovery discarded healthy metadata or exceeded bounds", state, resources, gotLimited, reads)
	}
	for _, r := range resources {
		if r.Group == route.Group || r.Group == "foreign.example" || !operations.ValidAPIResourceDescriptor(r) {
			t.Fatal("failed or unadvertised metadata was retained", r)
		}
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	session, err := tracker.BeginAPIInventory(cluster)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Record(state, resources, gotLimited, time.Now()); err != nil {
		t.Fatal("aggregated result could not be recorded", err)
	}
	foreign, err := tracker.BeginAPIInventory("west")
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.Record(operations.APIInventoryComplete, []operations.APIResourceDescriptor{{Group: "foreign.example", Version: "v1", Resource: "foreign", Kind: "ForeignCanary"}}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Analyse through an existing graph fixture; metadata retention adds no graph writes.
	store := memory.New()
	root := graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Pod", Name: "root"}
	if err := store.UpsertResource(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	result, err := analysis.AnalyzeObservedImpact(context.Background(), store, root.ID(), analysis.ImpactOptions{ClusterID: cluster}, graph.ImpactSnapshotOptions{ClusterID: cluster}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	o := result.Observation.Ordinary
	if o.Before.APIInventory.State != want || len(o.Before.APIInventory.Resources) != count || o.After.APIInventory.ClusterID != cluster || len(o.UnobservedAPIs) != 0 || !slices.Contains(o.Reasons, "api_inventory_incomplete") || o.TypeScopeClosed {
		t.Fatal("aggregated partial evidence was lost or overstated", o)
	}
	for _, api := range o.OptionalAPIs {
		if api.Before != analysis.ImpactAPIUnknown || api.After != analysis.ImpactAPIUnknown {
			t.Fatal("aggregated partial result qualified API presence/absence", api)
		}
	}
	body, err := json.Marshal(result)
	if err != nil || strings.Contains(string(body), "Canary") || strings.Contains(string(body), "aggregated-stale-canary") || strings.Contains(string(body), "aggregated-top-error-canary") {
		t.Fatal("private, failed or foreign aggregated evidence escaped", string(body), err)
	}
}

func TestAPIInventoryAggregatedProductionClientKeepsCurrentVersions(t *testing.T) {
	for _, mode := range []string{"current", "core-stale", "mixed"} {
		t.Run(mode, func(t *testing.T) { testAggregatedProductionClient(t, mode) })
	}
}

func testAggregatedProductionClient(t *testing.T, mode string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json;g=apidiscovery.k8s.io;v=v2;as=APIGroupDiscoveryList")
		group := ""
		freshness := apidiscovery.DiscoveryFreshnessCurrent
		resource, kind := "pods", "Pod"
		switch r.URL.Path {
		case "/api":
			if mode == "mixed" {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(&metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions", APIVersion: "v1"}, Versions: []string{"v1"}}); err != nil {
					t.Error(err)
				}
				return
			}
			if mode == "core-stale" {
				freshness = apidiscovery.DiscoveryFreshnessStale
			}
		case "/apis":
			group, resource, kind = "gateway.networking.k8s.io", "gateways", "Gateway"
		default:
			t.Error("partial aggregated discovery fetched another endpoint", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		item := apidiscovery.APIGroupDiscovery{ObjectMeta: metav1.ObjectMeta{Name: group}, Versions: []apidiscovery.APIVersionDiscovery{{Version: "v1", Freshness: freshness, Resources: []apidiscovery.APIResourceDiscovery{{Resource: resource, ResponseKind: &metav1.GroupVersionKind{Group: group, Version: "v1", Kind: kind}, Scope: apidiscovery.ScopeNamespace, Verbs: []string{"list", "watch"}}}}}}
		response := apidiscovery.APIGroupDiscoveryList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupDiscoveryList", APIVersion: "apidiscovery.k8s.io/v2"}, Items: []apidiscovery.APIGroupDiscovery{item}}
		if group != "" {
			stale := item.DeepCopy()
			stale.Name = "route.openshift.io"
			stale.Versions[0].Freshness = apidiscovery.DiscoveryFreshnessStale
			response.Items = append(response.Items, *stale)
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := k8sdiscovery.NewDiscoveryClientForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	state, resources, limited := discoverAPIInventory(context.Background(), client)
	want := map[string]int{"current": 2, "core-stale": 1, "mixed": 0}[mode]
	if state != operations.APIInventoryPartial || limited || len(resources) != want {
		t.Fatal("real aggregated parser lost current versions", state, resources)
	}
	for _, r := range resources {
		if r.Group == "route.openshift.io" || (mode == "core-stale" && r.Group == "") {
			t.Fatal("real aggregated parser accepted stale metadata", r)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(paths, []string{"/api", "/apis"}) {
		t.Fatal("aggregated discovery retried or fell back", paths)
	}
}
