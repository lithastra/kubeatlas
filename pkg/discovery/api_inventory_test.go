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
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

type inventoryStub struct {
	k8sdiscovery.DiscoveryInterface
	groups    func(context.Context) (*metav1.APIGroupList, error)
	resources func(context.Context, string) (*metav1.APIResourceList, error)
}

type incompleteAggregatedInventory struct{ inventoryStub }

func (incompleteAggregatedInventory) GroupsAndMaybeResourcesWithContext(context.Context) (*metav1.APIGroupList, map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error, error) {
	return &metav1.APIGroupList{}, nil, map[schema.GroupVersion]error{{Group: "example.com", Version: "v1"}: errors.New("synthetic-stale-canary")}, nil
}

func TestAPIInventoryAggregatedFailureDoesNotAssertAbsence(t *testing.T) {
	client := incompleteAggregatedInventory{inventoryStub: inventoryStub{groups: func(context.Context) (*metav1.APIGroupList, error) {
		t.Error("fallback dropped aggregated failures")
		return &metav1.APIGroupList{}, nil
	}, resources: func(context.Context, string) (*metav1.APIResourceList, error) {
		t.Error("incomplete discovery continued")
		return nil, nil
	}}}
	state, resources, limited := discoverAPIInventory(context.Background(), client)
	if state != operations.APIInventoryPartial || len(resources) != 0 || limited {
		t.Fatal("stale aggregated discovery qualified absence")
	}
}

func TestAPIInventoryProductionDiscoveryClientAndCancellation(t *testing.T) {
	for _, name := range []string{"complete", "core_404", "cancel"} {
		t.Run(name, func(t *testing.T) {
			var objectRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var response any
				switch r.URL.Path {
				case "/api":
					response = &metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions", APIVersion: "v1"}, Versions: []string{"v1"}}
				case "/apis":
					response = &metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
				case "/api/v1":
					if name == "cancel" {
						<-r.Context().Done()
						return
					}
					if name == "core_404" {
						http.NotFound(w, r)
						return
					}
					response = inventoryResources("v1")
					response.(*metav1.APIResourceList).TypeMeta = metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}
				default:
					objectRequests.Add(1)
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
			client.UseLegacyDiscovery = true
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if name == "cancel" {
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
				defer cancel()
			}
			state, resources, _ := discoverAPIInventory(ctx, client)
			want := operations.APIInventoryComplete
			if name == "cancel" {
				want = operations.APIInventoryFailed
			}
			if name == "core_404" {
				want = operations.APIInventoryPartial
			}
			if state != want || objectRequests.Load() != 0 {
				t.Fatalf("production discovery state=%s object requests=%d", state, objectRequests.Load())
			}
			if name == "complete" && (len(resources) != 1 || resources[0].Resource != "pods") {
				t.Fatal("production discovery metadata lost")
			}
		})
	}
}

func (s inventoryStub) ServerGroupsWithContext(ctx context.Context) (*metav1.APIGroupList, error) {
	return s.groups(ctx)
}
func (s inventoryStub) ServerResourcesForGroupVersionWithContext(ctx context.Context, gv string) (*metav1.APIResourceList, error) {
	return s.resources(ctx, gv)
}
func inventoryGroups() *metav1.APIGroupList {
	return &metav1.APIGroupList{Groups: []metav1.APIGroup{{Name: "example.com", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "example.com/v1", Version: "v1"}, {GroupVersion: "example.com/v1beta1", Version: "v1beta1"}}}}}
}
func inventoryResources(gv string) *metav1.APIResourceList {
	name, kind := "widgets", "Widget"
	if gv == "v1" {
		name, kind = "pods", "Pod"
	}
	return &metav1.APIResourceList{GroupVersion: gv, APIResources: []metav1.APIResource{{Name: name, Kind: kind, Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}, {Name: name + "/status", Kind: kind}}}
}

func TestAPIInventoryDiscoveryCompletenessErrorsAndCaps(t *testing.T) {
	for _, name := range []string{"complete", "group_denied", "resource_denied", "group_failed", "resource_failed", "group_nil", "resource_nil", "wrong_gv", "not_found", "bad_resource", "duplicate", "group_mismatch", "duplicate_group", "duplicate_version", "version_cap", "resource_cap", "canceled"} {
		t.Run(name, func(t *testing.T) {
			want, limited := operations.APIInventoryComplete, false
			groups := inventoryGroups()
			var groupErr error
			var calls atomic.Int32
			switch name {
			case "group_denied":
				groupErr = apierrors.NewForbidden(schema.GroupResource{Resource: "apis"}, "", errors.New("denial-canary"))
				want = operations.APIInventoryDenied
			case "resource_denied":
				want = operations.APIInventoryDenied
			case "group_failed":
				groupErr = errors.New("failure-canary")
				want = operations.APIInventoryFailed
			case "resource_failed", "group_nil", "duplicate_group", "duplicate_version", "canceled":
				want = operations.APIInventoryFailed
			case "resource_nil", "wrong_gv", "not_found", "bad_resource", "duplicate", "group_mismatch":
				want = operations.APIInventoryPartial
			case "version_cap":
				groups.Groups = nil
				for i := range operations.MaxInventoryGroupVersions {
					g := fmt.Sprintf("group%d.example.com", i)
					groups.Groups = append(groups.Groups, metav1.APIGroup{Name: g, Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: g + "/v1", Version: "v1"}}})
				}
				want, limited = operations.APIInventoryPartial, true
			case "resource_cap":
				want, limited = operations.APIInventoryPartial, true
			}
			if name == "group_nil" {
				groups = nil
			}
			if name == "duplicate_group" {
				groups.Groups = append(groups.Groups, groups.Groups[0])
			}
			if name == "duplicate_version" {
				groups.Groups[0].Versions = append(groups.Groups[0].Versions, groups.Groups[0].Versions[0])
			}
			client := inventoryStub{groups: func(context.Context) (*metav1.APIGroupList, error) { return groups, groupErr }, resources: func(ctx context.Context, gv string) (*metav1.APIResourceList, error) {
				calls.Add(1)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				list := inventoryResources(gv)
				switch name {
				case "resource_denied":
					return nil, apierrors.NewUnauthorized("denial-canary")
				case "resource_failed":
					return nil, errors.New("failure-canary")
				case "resource_nil":
					return nil, nil
				case "wrong_gv":
					list.GroupVersion = "wrong.example.com/v1"
				case "not_found":
					return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "groupversions"}, gv)
				case "bad_resource":
					list.APIResources[0].Name = "bad name"
				case "duplicate":
					list.APIResources = append(list.APIResources, list.APIResources[0])
				case "group_mismatch":
					list.APIResources[0].Group = "wrong.example.com"
				case "resource_cap":
					list.APIResources = nil
					for i := range operations.MaxInventoryResources + 1 {
						list.APIResources = append(list.APIResources, metav1.APIResource{Name: fmt.Sprintf("resource%d", i), Kind: "Test", Verbs: metav1.Verbs{"list", "watch"}})
					}
				}
				return list, nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			state, resources, gotLimited := discoverAPIInventory(ctx, client)
			if state != want || gotLimited != limited || len(resources) > operations.MaxInventoryResources {
				t.Fatalf("unexpected result %s limited=%v count=%d", state, gotLimited, len(resources))
			}
			if name == "complete" && (len(resources) != 3 || calls.Load() != 3) {
				t.Fatal("all advertised versions not discovered or subresources retained")
			}
			if (name == "version_cap" || name == "canceled") && calls.Load() != 0 {
				t.Fatal("request executed beyond discovery scope")
			}
			if name == "resource_cap" && (len(resources) != operations.MaxInventoryResources || calls.Load() != 1) {
				t.Fatal("resource cap not enforced before further requests")
			}
		})
	}
}

func TestAPIInventoryRunsWithinInformerLifetimeWithoutSecretReads(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "PodList"})
	var requests atomic.Int32
	disco := inventoryStub{groups: func(ctx context.Context) (*metav1.APIGroupList, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("background request has no deadline")
		}
		requests.Add(1)
		return &metav1.APIGroupList{}, nil
	}, resources: func(context.Context, string) (*metav1.APIResourceList, error) {
		requests.Add(1)
		list := inventoryResources("v1")
		list.APIResources = append(list.APIResources, metav1.APIResource{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}})
		return list, nil
	}}
	manager := NewInformerManager(client, memory.New(), WithGVRs([]schema.GroupVersionResource{gvr}), WithClusterID("prod"), WithCoverage(tracker), WithAPIInventory(disco))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	awaitCoverage(t, func() bool {
		s, err := tracker.SnapshotCoverage(context.Background(), "prod", []schema.GroupVersionResource{gvr})
		return err == nil && s.APIInventory != nil && s.APIInventory.State == operations.APIInventoryComplete && s.Resources[0].InitialDeliveryDone
	})
	if requests.Load() != 2 {
		t.Fatal("extra background request before refresh")
	}
	for _, a := range client.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Fatal("Secret object endpoint accessed")
		}
	}
	other, err := tracker.SnapshotCoverage(context.Background(), "hidden", nil)
	if err != nil || other.APIInventory != nil {
		t.Fatal("inventory crossed cluster scope")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inventory worker survived informer shutdown")
	}
	s, err := tracker.SnapshotCoverage(context.Background(), "prod", nil)
	if err != nil || !s.APIInventory.Stopped {
		t.Fatal("inventory not marked stopped")
	}
}

func TestStartupFilteringUsesExactResourceAndOneCheckPerVersion(t *testing.T) {
	gateway := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	httpRoute := gateway
	httpRoute.Resource = "httproutes"
	for _, name := range []string{"only_gateway", "empty_group", "missing_watch", "required_missing", "denied", "canceled"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			client := inventoryStub{resources: func(context.Context, string) (*metav1.APIResourceList, error) {
				calls++
				r := &metav1.APIResourceList{GroupVersion: gateway.GroupVersion().String(), APIResources: []metav1.APIResource{{Name: "gateways", Kind: "Gateway", Verbs: metav1.Verbs{"list", "watch"}}}}
				if name == "denied" {
					return nil, apierrors.NewUnauthorized("canary")
				}
				if name == "empty_group" || name == "required_missing" {
					r.APIResources = nil
				}
				if name == "missing_watch" {
					r.APIResources[0].Verbs = metav1.Verbs{"list"}
				}
				return r, nil
			}}
			want := []schema.GroupVersionResource{gateway, httpRoute}
			if name == "required_missing" {
				want = []schema.GroupVersionResource{{Group: "apps", Version: "v1", Resource: "deployments"}}
				client.resources = func(context.Context, string) (*metav1.APIResourceList, error) {
					calls++
					return &metav1.APIResourceList{GroupVersion: "apps/v1"}, nil
				}
			}
			got, err := FilterAvailableGVRs(ctx, client, want)
			fail := name == "required_missing" || name == "denied" || name == "canceled"
			if (err != nil) != fail {
				t.Fatalf("wrong filter result %v", err)
			}
			if name == "only_gateway" && (len(got) != 1 || got[0] != gateway) {
				t.Fatal("group presence substituted for resource presence")
			}
			if !fail && name != "only_gateway" && len(got) != 0 {
				t.Fatal("unsupported API retained")
			}
			wantCalls := 1
			if name == "canceled" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("discovery calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}
