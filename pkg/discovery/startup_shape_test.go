// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// Independent expected endpoint shapes; do not derive these from the production
// repair-kind map or the startup validator under test.
var startupShapeCases = []struct {
	version, resource, kind string
	namespaced, optional    bool
}{
	{"v1", "namespaces", "Namespace", false, false},
	{"v1", "pods", "Pod", true, false},
	{"v1", "services", "Service", true, false},
	{"v1", "configmaps", "ConfigMap", true, false},
	{"v1", "persistentvolumeclaims", "PersistentVolumeClaim", true, false},
	{"v1", "serviceaccounts", "ServiceAccount", true, false},
	{"apps/v1", "deployments", "Deployment", true, false},
	{"apps/v1", "replicasets", "ReplicaSet", true, false},
	{"apps/v1", "statefulsets", "StatefulSet", true, false},
	{"apps/v1", "daemonsets", "DaemonSet", true, false},
	{"batch/v1", "jobs", "Job", true, false},
	{"batch/v1", "cronjobs", "CronJob", true, false},
	{"autoscaling/v2", "horizontalpodautoscalers", "HorizontalPodAutoscaler", true, false},
	{"networking.k8s.io/v1", "ingresses", "Ingress", true, false},
	{"networking.k8s.io/v1", "networkpolicies", "NetworkPolicy", true, false},
	{"gateway.networking.k8s.io/v1", "gateways", "Gateway", true, true},
	{"gateway.networking.k8s.io/v1", "httproutes", "HTTPRoute", true, true},
	{"rbac.authorization.k8s.io/v1", "roles", "Role", true, false},
	{"rbac.authorization.k8s.io/v1", "rolebindings", "RoleBinding", true, false},
	{"rbac.authorization.k8s.io/v1", "clusterroles", "ClusterRole", false, false},
	{"rbac.authorization.k8s.io/v1", "clusterrolebindings", "ClusterRoleBinding", false, false},
	{"kyverno.io/v1", "policies", "Policy", true, true},
	{"kyverno.io/v1", "clusterpolicies", "ClusterPolicy", false, true},
	{"wgpolicyk8s.io/v1alpha2", "policyreports", "PolicyReport", true, true},
	{"wgpolicyk8s.io/v1alpha2", "clusterpolicyreports", "ClusterPolicyReport", false, true},
}

func TestStartupCanonicalEndpointShapes(t *testing.T) {
	for _, shape := range startupShapeCases {
		for _, mode := range []string{"supported", "wrong-kind", "wrong-scope"} {
			t.Run(shape.resource+"/"+mode, func(t *testing.T) {
				gv, _ := schema.ParseGroupVersion(shape.version)
				gvr := gv.WithResource(shape.resource)
				row := metav1.APIResource{Name: shape.resource, Kind: shape.kind, Namespaced: shape.namespaced, Verbs: metav1.Verbs{"list", "watch"}}
				switch mode {
				case "wrong-kind":
					row.Kind = "OtherKind"
				case "wrong-scope":
					row.Namespaced = !row.Namespaced
				}
				calls := 0
				client := inventoryStub{resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
					calls++
					if version != shape.version {
						t.Fatal("startup queried the wrong endpoint", version)
					}
					return &metav1.APIResourceList{GroupVersion: version, APIResources: []metav1.APIResource{row}}, nil
				}}
				got, err := FilterAvailableGVRs(context.Background(), client, []schema.GroupVersionResource{gvr})
				wantError := mode != "supported" && !shape.optional
				if (err != nil) != wantError || calls != 1 {
					t.Fatal("unsupported shape did not follow required/optional policy", got, err, calls)
				}
				if mode == "supported" && !slices.Equal(got, []schema.GroupVersionResource{gvr}) {
					t.Fatal("supported endpoint was dropped", got)
				}
				if mode != "supported" && len(got) != 0 {
					t.Fatal("startup accepted an unsupported endpoint shape", got)
				}
			})
		}
	}
	if len(startupShapeCases) != len(CoreGVRs) {
		t.Fatal("canonical shape oracle no longer covers the startup set")
	}
}

func TestStartupRejectsAmbiguousOrMalformedTargetMetadata(t *testing.T) {
	for _, target := range []schema.GroupVersionResource{{Version: "v1", Resource: "pods"}, {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, {Group: "custom.example", Version: "v1", Resource: "widgets"}} {
		for _, mode := range []string{"missing-kind", "invalid-kind", "wrong-row-group", "wrong-row-version", "duplicate", "conflicting-duplicate", "late-duplicate"} {
			t.Run(target.Resource+"/"+mode, func(t *testing.T) {
				kind := map[string]string{"pods": "Pod", "gateways": "Gateway", "widgets": "Widget"}[target.Resource]
				row := metav1.APIResource{Name: target.Resource, Kind: kind, Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}
				rows := []metav1.APIResource{row}
				switch mode {
				case "missing-kind":
					rows[0].Kind = ""
				case "invalid-kind":
					rows[0].Kind = "invalid/kind"
				case "wrong-row-group":
					rows[0].Group = "other.example"
				case "wrong-row-version":
					rows[0].Version = "v2"
				case "duplicate":
					rows = append(rows, row)
				case "conflicting-duplicate":
					bad := row
					bad.Verbs = metav1.Verbs{"list"}
					rows = append(rows, bad)
				case "late-duplicate":
					rows = append(rows, metav1.APIResource{Name: "unrelated", Kind: "Other"}, row)
				}
				calls := 0
				client := inventoryStub{resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
					calls++
					return &metav1.APIResourceList{GroupVersion: version, APIResources: rows}, nil
				}}
				got, err := FilterAvailableGVRs(context.Background(), client, []schema.GroupVersionResource{target})
				if err == nil || len(got) != 0 || calls != 1 {
					t.Fatal("ambiguous discovery was accepted or treated as optional absence", got, err, calls)
				}
			})
		}
	}
}

func TestStartupValidatedGroupResponseIsReusedWithoutBroadeningScope(t *testing.T) {
	for _, explicitIdentity := range []bool{false, true} {
		t.Run(fmt.Sprint(explicitIdentity), func(t *testing.T) {
			want := []schema.GroupVersionResource{{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, {Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}}
			calls := 0
			client := inventoryStub{resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
				calls++
				rows := []metav1.APIResource{{Name: "gateways", Kind: "Gateway", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}, {Name: "httproutes", Kind: "HTTPRoute", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}, {Name: "gateways/status", Kind: "Gateway"}, {Name: "unrelated", Kind: "Malformed/Unrequested"}}
				if explicitIdentity {
					for i := range 2 {
						rows[i].Group, rows[i].Version = "gateway.networking.k8s.io", "v1"
					}
				}
				return &metav1.APIResourceList{GroupVersion: version, APIResources: rows}, nil
			}}
			got, err := FilterAvailableGVRs(context.Background(), client, want)
			if err != nil || !slices.Equal(got, want) || calls != 1 {
				t.Fatal("target validation broadened scope or repeated discovery", got, err, calls)
			}
		})
	}
}

func TestStartupShapeFailureDoesNotReturnAPartialSelection(t *testing.T) {
	for _, mode := range []string{"required-unsupported", "optional-unsupported", "optional-malformed", "cached-duplicate"} {
		t.Run(mode, func(t *testing.T) {
			first := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
			second := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
			if mode == "required-unsupported" {
				second = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
			}
			if mode == "cached-duplicate" {
				first = second
				second.Resource = "httproutes"
			}
			calls := 0
			client := inventoryStub{resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
				calls++
				if mode == "cached-duplicate" {
					return &metav1.APIResourceList{GroupVersion: version, APIResources: []metav1.APIResource{{Name: "gateways", Kind: "Gateway", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}, {Name: "httproutes", Kind: "HTTPRoute", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}, {Name: "httproutes", Kind: "HTTPRoute"}}}, nil
				}
				name, kind := "pods", "Pod"
				if version != "v1" {
					name, kind = second.Resource, "WrongKind"
					if mode == "optional-malformed" {
						kind = ""
					}
				}
				return &metav1.APIResourceList{GroupVersion: version, APIResources: []metav1.APIResource{{Name: name, Kind: kind, Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}}}, nil
			}}
			got, err := FilterAvailableGVRs(context.Background(), client, []schema.GroupVersionResource{first, second})
			if mode == "optional-unsupported" {
				if err != nil || !slices.Equal(got, []schema.GroupVersionResource{first}) {
					t.Fatal("unsupported optional API discarded a valid required selection", got, err)
				}
			} else if err == nil || len(got) != 0 {
				t.Fatal("invalid required/ambiguous API returned a partial startup set", got, err)
			}
			wantCalls := 2
			if mode == "cached-duplicate" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatal("shape validation repeated discovery", calls)
			}
		})
	}
}

func TestStartupCustomEndpointDoesNotInferACanonicalShape(t *testing.T) {
	for _, namespaced := range []bool{false, true} {
		t.Run(fmt.Sprint(namespaced), func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "custom.example", Version: "v1", Resource: "widgets"}
			client := inventoryStub{resources: func(_ context.Context, version string) (*metav1.APIResourceList, error) {
				return &metav1.APIResourceList{GroupVersion: version, APIResources: []metav1.APIResource{{Name: "widgets", Kind: "CustomWidget", Namespaced: namespaced, Verbs: metav1.Verbs{"list", "watch"}}}}, nil
			}}
			got, err := FilterAvailableGVRs(context.Background(), client, []schema.GroupVersionResource{gvr})
			if err != nil || !slices.Equal(got, []schema.GroupVersionResource{gvr}) {
				t.Fatal("custom metadata was assigned an invented canonical kind/scope", got, err)
			}
		})
	}
}

func TestStartupProductionClientValidatesTargetShapes(t *testing.T) {
	for _, optional := range []bool{false, true} {
		for _, mode := range []string{"supported", "wrong-kind", "wrong-scope", "duplicate"} {
			t.Run(fmt.Sprintf("optional=%t/%s", optional, mode), func(t *testing.T) {
				gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
				kind, path := "Pod", "/api/v1"
				if optional {
					gvr = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
					kind, path = "Gateway", "/apis/gateway.networking.k8s.io/v1"
				}
				row := metav1.APIResource{Name: gvr.Resource, Kind: kind, Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}
				if mode == "wrong-kind" {
					row.Kind = "OtherKind"
				}
				if mode == "wrong-scope" {
					row.Namespaced = false
				}
				rows := []metav1.APIResource{row}
				if mode == "duplicate" {
					rows = append(rows, row)
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path != path {
						t.Error("startup requested another discovery/object endpoint", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(&metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gvr.GroupVersion().String(), APIResources: rows}); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				client, err := k8sdiscovery.NewDiscoveryClientForConfig(&rest.Config{Host: server.URL})
				if err != nil {
					t.Fatal(err)
				}
				got, err := FilterAvailableGVRs(context.Background(), client, []schema.GroupVersionResource{gvr})
				wantError := mode == "duplicate" || (mode != "supported" && !optional)
				if (err != nil) != wantError || calls.Load() != 1 || (mode == "supported" && !slices.Equal(got, []schema.GroupVersionResource{gvr})) || (mode != "supported" && len(got) != 0) {
					t.Fatal("production startup selected an unsupported/ambiguous target or repeated requests", got, err, calls.Load())
				}
			})
		}
	}
}
