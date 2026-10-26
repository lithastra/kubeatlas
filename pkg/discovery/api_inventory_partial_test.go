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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

var partialInventoryVersions = []string{"gateway.networking.k8s.io/v1", "route.openshift.io/v1", "v1"}

func partialInventoryGroups() *metav1.APIGroupList {
	groups := &metav1.APIGroupList{}
	for _, version := range partialInventoryVersions[:2] {
		gv, _ := schema.ParseGroupVersion(version)
		groups.Groups = append(groups.Groups, metav1.APIGroup{Name: gv.Group, Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: version, Version: gv.Version}}})
	}
	return groups
}

func partialInventoryResources(version string) *metav1.APIResourceList {
	name, kind := "pods", "Pod"
	switch version {
	case partialInventoryVersions[0]:
		name, kind = "gateways", "Gateway"
	case partialInventoryVersions[1]:
		name, kind = "routes", "Route"
	}
	return &metav1.APIResourceList{GroupVersion: version, APIResources: []metav1.APIResource{{Name: name, Kind: kind, Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}}}
}

func partialInventoryError(mode string) error {
	switch mode {
	case "forbidden":
		return apierrors.NewForbidden(schema.GroupResource{Resource: "apis"}, "", errors.New("partial-error-canary"))
	case "unauthorized":
		return apierrors.NewUnauthorized("partial-error-canary")
	default:
		return errors.New("partial-error-canary")
	}
}

func TestAPIInventoryRetainsCompletedVersionsOnFailure(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for position := range 3 {
			for _, mode := range []string{"forbidden", "unauthorized", "transport"} {
				t.Run(fmt.Sprintf("cluster=%s/position=%d/%s", cluster, position, mode), func(t *testing.T) {
					testPartialInventoryRound(t, cluster, position, mode)
				})
			}
		}
	}
}

func testPartialInventoryRound(t *testing.T, cluster string, position int, mode string) {
	t.Helper()
	ctx := context.Background()
	var calls []string
	client := inventoryStub{groups: func(context.Context) (*metav1.APIGroupList, error) { return partialInventoryGroups(), nil }, resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
		calls = append(calls, version)
		if version == partialInventoryVersions[position] {
			return &metav1.APIResourceList{GroupVersion: version, APIResources: []metav1.APIResource{{Name: "failedrow", Kind: "FailedRow"}}}, partialInventoryError(mode)
		}
		return partialInventoryResources(version), nil
	}}
	state, resources, limited := discoverAPIInventory(ctx, client)
	want := operations.APIInventoryPartial
	if position == 0 {
		want = operations.APIInventoryDenied
		if mode == "transport" {
			want = operations.APIInventoryFailed
		}
	}
	if state != want || limited || len(resources) != position || !slices.Equal(calls, partialInventoryVersions[:position+1]) {
		t.Fatal("failed discovery lost the completed prefix or continued requests", state, resources, calls)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	session, err := tracker.BeginAPIInventory(cluster)
	if err != nil {
		t.Fatal(err)
	}
	old := operations.APIResourceDescriptor{Group: "obsolete.example", Version: "v1", Resource: "obsolete", Kind: "Obsolete"}
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
	if err := session.Record(state, resources, limited, time.Now()); err != nil {
		t.Fatal("partial prefix was rejected by the evidence recorder", err)
	}
	store := memory.New()
	root := graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Pod", Name: "root", GroupVersion: "v1", UID: "root-uid", ResourceVersion: "opaque-version"}
	if err := store.UpsertResource(ctx, root); err != nil {
		t.Fatal(err)
	}
	result, err := analysis.AnalyzeObservedImpact(ctx, store, root.ID(), analysis.ImpactOptions{ClusterID: cluster}, graph.ImpactSnapshotOptions{ClusterID: cluster}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	o := result.Observation.Ordinary
	for _, capture := range []*operations.APIInventoryEvidence{o.Before.APIInventory, o.After.APIInventory} {
		if capture == nil || capture.ClusterID != cluster || capture.State != want || capture.Revision != 2 || len(capture.Resources) != position || capture.Stale || capture.Stopped || capture.Limited {
			t.Fatal("analysis lost fresh scoped prefix evidence", capture)
		}
	}
	if !slices.Contains(o.Reasons, "api_inventory_incomplete") || o.TypeScopeClosed || len(o.UnobservedAPIs) != 0 || o.EmptyResultAssessment != "none_observed_incomplete" {
		t.Fatal("partial inventory qualified completeness or unobserved endpoints", o)
	}
	for _, api := range o.OptionalAPIs {
		if api.Before != analysis.ImpactAPIUnknown || api.After != analysis.ImpactAPIUnknown {
			t.Fatal("partial metadata qualified optional API presence/absence", api)
		}
	}
	body, err := json.Marshal(result)
	if err != nil || strings.Contains(string(body), "partial-error-canary") || strings.Contains(string(body), "ForeignCanary") || strings.Contains(string(body), "obsolete") || strings.Contains(string(body), "failedrow") {
		t.Fatal("old, foreign or failed-response evidence escaped", string(body), err)
	}
	// A later successful round replaces this prefix; no old-round merge occurs.
	client.resources = func(_ context.Context, version string) (*metav1.APIResourceList, error) {
		return partialInventoryResources(version), nil
	}
	state, resources, limited = discoverAPIInventory(ctx, client)
	if state != operations.APIInventoryComplete || len(resources) != 3 || limited {
		t.Fatal("later round did not recover", state, resources)
	}
	if err := session.Record(state, resources, limited, time.Now()); err != nil {
		t.Fatal(err)
	}
	recovered, err := tracker.SnapshotCoverage(ctx, cluster, nil)
	if err != nil || recovered.APIInventory.Revision != 3 || len(recovered.APIInventory.Resources) != 3 {
		t.Fatal("complete round failed to replace partial evidence", recovered, err)
	}
	result, err = analysis.AnalyzeObservedImpact(ctx, store, root.ID(), analysis.ImpactOptions{ClusterID: cluster}, graph.ImpactSnapshotOptions{ClusterID: cluster}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range result.Observation.Ordinary.OptionalAPIs {
		if (api.Resource == "gateways" || api.Resource == "routes") && (api.Before != analysis.ImpactAPIAdvertised || api.After != analysis.ImpactAPIAdvertised) {
			t.Fatal("fresh complete round failed to restore qualified API assessment", api)
		}
	}
}

func TestAPIInventoryCancellationDiscardsCompletedPrefix(t *testing.T) {
	for _, stage := range []string{"groups", "first-version", "last-version"} {
		for _, mode := range []string{"success", "forbidden", "transport"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				respond := func() error {
					cancel()
					if mode == "success" {
						return nil
					}
					return partialInventoryError(mode)
				}
				client := inventoryStub{groups: func(context.Context) (*metav1.APIGroupList, error) {
					if stage == "groups" {
						return partialInventoryGroups(), respond()
					}
					return partialInventoryGroups(), nil
				}, resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
					calls++
					if (stage == "first-version" && calls == 1) || (stage == "last-version" && calls == 3) {
						return partialInventoryResources(version), respond()
					}
					return partialInventoryResources(version), nil
				}}
				state, rows, limited := discoverAPIInventory(ctx, client)
				wantCalls := map[string]int{"groups": 0, "first-version": 1, "last-version": 3}[stage]
				if state != operations.APIInventoryFailed || len(rows) != 0 || limited || calls != wantCalls {
					t.Fatal("cancelled dependency published partial evidence or started another request", state, rows, calls)
				}
			})
		}
	}
}

func TestAPIInventoryProductionClientRetainsPrefixAfterDenial(t *testing.T) {
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
			response = partialInventoryGroups()
			response.(*metav1.APIGroupList).TypeMeta = metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}
		case "/apis/gateway.networking.k8s.io/v1":
			response = partialInventoryResources(partialInventoryVersions[0])
			response.(*metav1.APIResourceList).TypeMeta = metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}
		case "/apis/route.openshift.io/v1":
			w.WriteHeader(http.StatusForbidden)
			response = &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: metav1.StatusReasonForbidden, Code: 403}
		default:
			t.Error("continued or object request", r.URL.Path)
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
	state, resources, limited := discoverAPIInventory(context.Background(), client)
	if state != operations.APIInventoryPartial || limited || len(resources) != 1 || resources[0].Resource != "gateways" {
		t.Fatal("production client dropped confirmed metadata", state, resources)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(paths, []string{"/api", "/apis", "/apis/gateway.networking.k8s.io/v1", "/apis/route.openshift.io/v1"}) {
		t.Fatal("discovery retried, fell back or read objects", paths)
	}
}
