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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

func inventoryIdentityRows(mode string, reverse bool) *metav1.APIResourceList {
	list := partialInventoryResources(partialInventoryVersions[0])
	target := list.APIResources[0]
	sibling := metav1.APIResource{Name: "gatewayclasses", Kind: "GatewayClass", Verbs: metav1.Verbs{"list", "watch"}}
	duplicate := target
	switch mode {
	case "kind":
		duplicate.Kind = "AmbiguousCanary"
	case "scope":
		duplicate.Namespaced = false
	case "verbs":
		duplicate.Verbs = metav1.Verbs{"get"}
	case "group":
		duplicate.Group = "wrong.example"
	case "version":
		duplicate.Version = "v2"
	case "malformed":
		duplicate.Kind = "invalid kind"
	case "unique":
		duplicate.Name, duplicate.Kind = "additional", "Additional"
	case "subresource":
		duplicate.Name = "gateways/status"
	case "unrelated-malformed":
		duplicate.Name, duplicate.Kind = "unrelated", "invalid kind"
	}
	list.APIResources = []metav1.APIResource{target, sibling, duplicate}
	if mode == "many-duplicates" {
		// Exactly 257 occurrences must not wrap to a unique byte count.
		for range 255 {
			list.APIResources = append(list.APIResources, target)
		}
	}
	if reverse {
		slices.Reverse(list.APIResources)
	}
	return list
}

func TestAPIInventoryExcludesAmbiguousEndpointMetadata(t *testing.T) {
	for _, transport := range []string{"sequential", "aggregated"} {
		for _, cluster := range []string{"east", ""} {
			for _, mode := range []string{"identical", "many-duplicates", "kind", "scope", "verbs", "group", "version", "malformed", "unique", "subresource", "unrelated-malformed"} {
				for _, reverse := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/cluster=%s/%s/reverse=%t", transport, cluster, mode, reverse), func(t *testing.T) {
						testInventoryIdentity(t, transport, cluster, mode, reverse)
					})
				}
			}
		}
	}
}

func testInventoryIdentity(t *testing.T, transport, cluster, mode string, reverse bool) {
	t.Helper()
	ctx := context.Background()
	gateway, _ := schema.ParseGroupVersion(partialInventoryVersions[0])
	route, _ := schema.ParseGroupVersion(partialInventoryVersions[1])
	core := schema.GroupVersion{Version: "v1"}
	recovering := false
	var calls []string
	read := func(version string) *metav1.APIResourceList {
		if version == gateway.String() && !recovering {
			return inventoryIdentityRows(mode, reverse)
		}
		list := partialInventoryResources(version)
		if version == route.String() {
			// Equal resource names in different groups must remain independent.
			list.APIResources = append(list.APIResources, metav1.APIResource{Name: "gateways", Kind: "OtherGateway", Verbs: metav1.Verbs{"list", "watch"}})
		}
		return list
	}
	sequential := inventoryStub{groups: func(context.Context) (*metav1.APIGroupList, error) { return partialInventoryGroups(), nil }, resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
		calls = append(calls, version)
		return read(version), nil
	}}
	var client inventoryDiscoveryClient = sequential
	if transport == "aggregated" {
		client = aggregatedInventoryStub{inventoryStub: sequential, read: func(context.Context) (*metav1.APIGroupList, map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error, error) {
			calls = append(calls, "aggregated")
			groups := partialInventoryGroups()
			stale := schema.GroupVersion{Group: "stale.example", Version: "v1"}
			groups.Groups = append(groups.Groups, metav1.APIGroup{Name: stale.Group})
			return groups, map[schema.GroupVersion]*metav1.APIResourceList{gateway: read(gateway.String()), route: read(route.String()), core: read(core.String())}, map[schema.GroupVersion]error{stale: errors.New("stale-identity-canary")}, nil
		}}
	}
	state, rows, limited := discoverAPIInventory(ctx, client)
	ambiguous := mode != "unique" && mode != "subresource" && mode != "unrelated-malformed"
	want := operations.APIInventoryPartial
	if transport == "sequential" && (mode == "unique" || mode == "subresource") {
		want = operations.APIInventoryComplete
	}
	wantCount := 4 // Healthy sibling, two route rows and core Pod.
	if !ambiguous {
		wantCount++
	}
	if mode == "unique" {
		wantCount++
	}
	if state != want || limited || len(rows) != wantCount {
		t.Fatal("inventory retained ambiguous rows or lost independent metadata", state, rows, limited)
	}
	for _, r := range rows {
		if r.GVR() == gateway.WithResource("gateways") && (ambiguous || r.Kind != "Gateway" || !r.Namespaced || !r.List || !r.Watch) {
			t.Fatal("ambiguous metadata depends on row order", r)
		}
	}
	wantCalls := partialInventoryVersions
	if transport == "aggregated" {
		wantCalls = []string{"aggregated"}
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatal("identity validation changed discovery requests", calls)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	session, err := tracker.BeginAPIInventory(cluster)
	if err != nil {
		t.Fatal(err)
	}
	old := operations.APIResourceDescriptor{Group: "old.example", Version: "v1", Resource: "old", Kind: "OldCanary"}
	if err := session.Record(operations.APIInventoryComplete, []operations.APIResourceDescriptor{old}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	foreign, err := tracker.BeginAPIInventory("west")
	if err != nil {
		t.Fatal(err)
	}
	old.Kind = "ForeignCanary"
	if err := foreign.Record(operations.APIInventoryComplete, []operations.APIResourceDescriptor{old}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := session.Record(state, rows, limited, time.Now()); err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	root := graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Pod", Name: "root", GroupVersion: "v1", UID: "uid", ResourceVersion: "version"}
	if err := store.UpsertResource(ctx, root); err != nil {
		t.Fatal(err)
	}
	result, err := analysis.AnalyzeObservedImpact(ctx, store, root.ID(), analysis.ImpactOptions{ClusterID: cluster}, graph.ImpactSnapshotOptions{ClusterID: cluster}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	o := result.Observation.Ordinary
	for _, capture := range []*operations.APIInventoryEvidence{o.Before.APIInventory, o.After.APIInventory} {
		if capture == nil || capture.ClusterID != cluster || capture.State != want || capture.Revision != 2 || len(capture.Resources) != wantCount || capture.Stale || capture.Stopped || capture.Limited {
			t.Fatal("analysis lost scoped sanitized inventory", capture)
		}
	}
	if want == operations.APIInventoryPartial {
		if !slices.Contains(o.Reasons, "api_inventory_incomplete") || o.TypeScopeClosed || len(o.UnobservedAPIs) != 0 {
			t.Fatal("ambiguous inventory established completeness", o)
		}
		for _, api := range o.OptionalAPIs {
			if api.Before != analysis.ImpactAPIUnknown || api.After != analysis.ImpactAPIUnknown {
				t.Fatal("partial inventory established optional API presence or absence", api)
			}
		}
	}
	body, err := json.Marshal(result)
	if err != nil || strings.Contains(string(body), "Canary") || strings.Contains(string(body), "stale-identity-canary") {
		t.Fatal("ambiguous, prior or foreign metadata escaped", string(body), err)
	}
	// A fresh complete response replaces this round; excluded descriptors are
	// restored only by that response, never from an old capture.
	recovering, calls = true, nil
	state, rows, limited = discoverAPIInventory(ctx, sequential)
	if state != operations.APIInventoryComplete || limited || len(rows) != 4 || !slices.Equal(calls, partialInventoryVersions) {
		t.Fatal("fresh complete response did not recover", state, rows, calls)
	}
	if err := session.Record(state, rows, limited, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := tracker.SnapshotCoverage(ctx, cluster, nil)
	if err != nil || snapshot.APIInventory.Revision != 3 || snapshot.APIInventory.State != operations.APIInventoryComplete || len(snapshot.APIInventory.Resources) != 4 {
		t.Fatal("recovery merged prior ambiguous metadata", snapshot, err)
	}
}

func TestAPIInventoryLateDuplicateAfterDescriptorCap(t *testing.T) {
	for _, transport := range []string{"sequential", "aggregated"} {
		t.Run(transport, func(t *testing.T) {
			list := &metav1.APIResourceList{GroupVersion: "v1"}
			for i := range operations.MaxInventoryResources + 1 {
				list.APIResources = append(list.APIResources, metav1.APIResource{Name: fmt.Sprintf("resource%d", i), Kind: "Resource"})
			}
			list.APIResources = append(list.APIResources, metav1.APIResource{Name: "resource0", Kind: "LateCanary"})
			calls := 0
			// Core sorts first for this set. No later version may be fetched.
			base := inventoryStub{groups: func(context.Context) (*metav1.APIGroupList, error) {
				return &metav1.APIGroupList{Groups: []metav1.APIGroup{{Name: "z.example", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "z.example/v1", Version: "v1"}}}}}, nil
			}, resources: func(context.Context, string) (*metav1.APIResourceList, error) {
				calls++
				return list, nil
			}}
			var client inventoryDiscoveryClient = base
			if transport == "aggregated" {
				client = aggregatedInventoryStub{inventoryStub: base, read: func(ctx context.Context) (*metav1.APIGroupList, map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error, error) {
					calls++
					groups, err := base.groups(ctx)
					return groups, map[schema.GroupVersion]*metav1.APIResourceList{{Version: "v1"}: list}, map[schema.GroupVersion]error{{Group: "stale.example", Version: "v1"}: errors.New("stale")}, err
				}}
			}
			state, rows, limited := discoverAPIInventory(context.Background(), client)
			if state != operations.APIInventoryPartial || !limited || len(rows) != operations.MaxInventoryResources-1 || calls != 1 {
				t.Fatal("cap bypassed duplicate validation or started another request", state, len(rows), limited, calls)
			}
			for _, r := range rows {
				if r.Resource == "resource0" || r.Resource == "resource256" {
					t.Fatal("ambiguous or over-budget descriptor retained", r)
				}
			}
		})
	}
}

func TestAPIInventoryProductionClientExcludesDuplicateDescriptors(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				var response any
				switch r.URL.Path {
				case "/api":
					response = &metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions", APIVersion: "v1"}, Versions: []string{"v1"}}
				case "/apis":
					groups := partialInventoryGroups()
					groups.TypeMeta = metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}
					response = groups
				case "/apis/gateway.networking.k8s.io/v1", "/apis/route.openshift.io/v1", "/api/v1":
					version := strings.TrimPrefix(r.URL.Path, "/apis/")
					if r.URL.Path == "/api/v1" {
						version = "v1"
					}
					list := partialInventoryResources(version)
					if version == partialInventoryVersions[0] {
						list = inventoryIdentityRows("malformed", reverse)
					}
					list.TypeMeta = metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}
					response = list
				default:
					t.Error("unexpected discovery or object request", r.URL.Path)
					http.NotFound(w, r)
					return
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
			state, rows, limited := discoverAPIInventory(context.Background(), client)
			if state != operations.APIInventoryPartial || limited || len(rows) != 3 {
				t.Fatal("real discovery parser retained conflicting endpoint", state, rows)
			}
			for _, row := range rows {
				if row.Resource == "gateways" {
					t.Fatal("real discovery retained an ambiguous descriptor", row)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(paths, []string{"/api", "/apis", "/apis/gateway.networking.k8s.io/v1", "/apis/route.openshift.io/v1", "/api/v1"}) {
				t.Fatal("identity validation added requests", paths)
			}
		})
	}
}
