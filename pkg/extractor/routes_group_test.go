// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestHTTPRouteServiceBackendGroupIdentity(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, explicitKind := range []bool{false, true} {
			for _, tc := range []struct {
				name          string
				group         string
				omitted, want bool
			}{
				{name: "omitted", omitted: true, want: true},
				{name: "core", group: "", want: true},
				{name: "core-alias", group: "core"},
				{name: "foreign", group: "example.org"},
				{name: "gateway-group", group: "gateway.networking.k8s.io"},
			} {
				for _, remote := range []bool{false, true} {
					t.Run(fmt.Sprintf("cluster=%s/explicit-kind=%t/%s/remote=%t", cluster, explicitKind, tc.name, remote), func(t *testing.T) {
						ref := map[string]any{"name": "service"}
						if explicitKind {
							ref["kind"] = "Service"
						}
						if !tc.omitted {
							ref["group"] = tc.group
						}
						namespace := "demo"
						if remote {
							namespace, ref["namespace"] = "infra", "infra"
						}
						r := graph.Resource{ClusterID: cluster, Kind: "HTTPRoute", Namespace: "demo", Name: "route", Raw: map[string]any{"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{ref}}}}}}
						q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
							t.Fatal("backend declarations must not enumerate targets")
							return nil, nil
						})
						edges, err := (RoutesExtractor{}).Extract(context.Background(), r, q)
						var want []graph.Edge
						if tc.want {
							want = []graph.Edge{{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "Service", Namespace: namespace, Name: "service"}).ID(), Type: graph.EdgeTypeRoutesTo}}
						}
						if err != nil || !reflect.DeepEqual(edges, want) {
							t.Fatal("backend group was conflated with the core Service group", edges, want, err)
						}
					})
				}
			}
		}
	}
}

func TestHTTPRouteBackendGroupsDoNotPoisonDeduplication(t *testing.T) {
	for _, rejectedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected-first=%t", rejectedFirst), func(t *testing.T) {
			valid := []any{map[string]any{"name": "shared"}, map[string]any{"name": "shared", "kind": "Service", "group": ""}}
			foreign := []any{map[string]any{"name": "shared", "group": "example.org"}, map[string]any{"name": "other", "group": "core"}}
			refs := append(append([]any{}, valid...), foreign...)
			if rejectedFirst {
				refs = append(append([]any{}, foreign...), valid...)
			}
			r := graph.Resource{Kind: "HTTPRoute", Namespace: "demo", Name: "route", Raw: map[string]any{"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": refs}}}}}
			edges, err := (RoutesExtractor{}).Extract(context.Background(), r, nil)
			if err != nil || len(edges) != 1 || edges[0].From != r.ID() || edges[0].To != "demo/Service/shared" || edges[0].Type != graph.EdgeTypeRoutesTo {
				t.Fatal("unsupported groups hid valid backends or produced extra endpoints", edges, err)
			}
		})
	}
}

func TestHTTPRouteMalformedBackendGroupKeepsPrefixAndParent(t *testing.T) {
	for _, group := range []struct {
		name  string
		value any
	}{{"null", nil}, {"boolean", true}, {"array", []any{"backend-group-canary"}}, {"object", map[string]any{"private-key-canary": "backend-group-canary"}}} {
		for _, laterRule := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/later-rule=%t", group.name, laterRule), func(t *testing.T) {
				first := map[string]any{"name": "confirmed"}
				bad := map[string]any{"name": "invalid", "group": group.value}
				later := map[string]any{"name": "later"}
				rules := []any{map[string]any{"backendRefs": []any{first, bad, later}}}
				if laterRule {
					rules = []any{map[string]any{"backendRefs": []any{first}}, map[string]any{"backendRefs": []any{bad, later}}}
				}
				r := graph.Resource{Kind: "HTTPRoute", Namespace: "demo", Name: "route", Raw: map[string]any{"spec": map[string]any{"rules": rules, "parentRefs": []any{map[string]any{"name": "gateway"}}}}}
				registry := New()
				registry.Register(RoutesExtractor{})
				registry.Register(AttachedExtractor{})
				edges, err := registry.ExtractAll(context.Background(), r, nil)
				want := []graph.Edge{{From: r.ID(), To: "demo/Service/confirmed", Type: graph.EdgeTypeRoutesTo}, {From: r.ID(), To: "demo/Gateway/gateway", Type: graph.EdgeTypeAttachedTo}}
				if err == nil || !reflect.DeepEqual(edges, want) {
					t.Fatal("malformed backend group lost prefix/parent or became a default", edges, err)
				}
				if strings.Contains(err.Error(), "backend-group-canary") || strings.Contains(err.Error(), "private-key-canary") {
					t.Fatal("backend group error included input data", err)
				}
			})
		}
	}
}

func TestHTTPRouteCustomBackendKindCompatibility(t *testing.T) {
	for _, group := range []string{"", "multicluster.x-k8s.io", "example.org"} {
		t.Run("group="+group, func(t *testing.T) {
			r := graph.Resource{Kind: "HTTPRoute", Namespace: "demo", Name: "route", Raw: map[string]any{"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "imported", "kind": "ServiceImport", "group": group}}}}}}}
			edges, err := (RoutesExtractor{}).Extract(context.Background(), r, nil)
			if err != nil || len(edges) != 1 || edges[0].To != "demo/ServiceImport/imported" {
				t.Fatal("Service group guard changed legacy custom-kind modeling", edges, err)
			}
		})
	}
}
