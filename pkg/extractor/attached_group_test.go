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

func TestAttachedParentGroupIdentity(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, tc := range []struct {
			name          string
			group         any
			omitted, want bool
		}{
			{name: "omitted", omitted: true, want: true},
			{name: "canonical", group: "gateway.networking.k8s.io", want: true},
			{name: "core", group: ""},
			{name: "foreign", group: "example.org"},
			{name: "version-as-group", group: "gateway.networking.k8s.io/v1"},
		} {
			for _, remote := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/%s/remote=%t", cluster, tc.name, remote), func(t *testing.T) {
					ref := map[string]any{"name": "gateway", "kind": "Gateway"}
					if !tc.omitted {
						ref["group"] = tc.group
					}
					namespace := "demo"
					if remote {
						namespace, ref["namespace"] = "infra", "infra"
					}
					r := graph.Resource{ClusterID: cluster, Kind: "HTTPRoute", Namespace: "demo", Name: "route", Raw: map[string]any{"spec": map[string]any{"parentRefs": []any{ref}}}}
					q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
						t.Fatal("parent reference extraction must not enumerate targets")
						return nil, nil
					})
					edges, err := (AttachedExtractor{}).Extract(context.Background(), r, q)
					var want []graph.Edge
					if tc.want {
						want = []graph.Edge{{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "Gateway", Namespace: namespace, Name: "gateway"}).ID(), Type: graph.EdgeTypeAttachedTo}}
					}
					if err != nil || !reflect.DeepEqual(edges, want) {
						t.Fatal("parent group was conflated with the modeled Gateway group", edges, want, err)
					}
				})
			}
		}
	}
}

func TestAttachedGroupsDoNotPoisonDeduplication(t *testing.T) {
	for _, rejectedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected-first=%t", rejectedFirst), func(t *testing.T) {
			valid := []any{map[string]any{"name": "shared"}, map[string]any{"name": "shared", "group": "gateway.networking.k8s.io"}}
			foreign := []any{map[string]any{"name": "shared", "group": "example.org"}, map[string]any{"name": "shared", "group": ""}, map[string]any{"name": "other", "group": "example.org"}, map[string]any{"name": "service", "kind": "Service", "group": ""}}
			refs := append(append([]any{}, valid...), foreign...)
			if rejectedFirst {
				refs = append(append([]any{}, foreign...), valid...)
			}
			r := graph.Resource{Kind: "HTTPRoute", Namespace: "demo", Name: "route", Raw: map[string]any{"spec": map[string]any{"parentRefs": refs}}}
			edges, err := (AttachedExtractor{}).Extract(context.Background(), r, nil)
			if err != nil || len(edges) != 1 || edges[0].To != "demo/Gateway/shared" || edges[0].From != r.ID() || edges[0].Type != graph.EdgeTypeAttachedTo {
				t.Fatal("unsupported groups hid valid parents or produced extra endpoints", edges, err)
			}
		})
	}
}

func TestAttachedMalformedGroupRetainsPrefixAndIndependentEdges(t *testing.T) {
	for _, group := range []struct {
		name  string
		value any
	}{{"null", nil}, {"boolean", true}, {"array", []any{"parent-group-canary"}}, {"object", map[string]any{"private-key-canary": "parent-group-canary"}}} {
		t.Run(group.name, func(t *testing.T) {
			r := graph.Resource{Kind: "HTTPRoute", Namespace: "demo", Name: "route", Raw: map[string]any{"spec": map[string]any{
				"parentRefs": []any{map[string]any{"name": "confirmed"}, map[string]any{"name": "invalid", "group": group.value}, map[string]any{"name": "later"}},
				"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": "backend"}}}},
			}}}
			registry := New()
			registry.Register(AttachedExtractor{})
			registry.Register(RoutesExtractor{})
			edges, err := registry.ExtractAll(context.Background(), r, nil)
			want := []graph.Edge{{From: r.ID(), To: "demo/Gateway/confirmed", Type: graph.EdgeTypeAttachedTo}, {From: r.ID(), To: "demo/Service/backend", Type: graph.EdgeTypeRoutesTo}}
			if err == nil || !reflect.DeepEqual(edges, want) {
				t.Fatal("malformed group became a default, erased prefix or suppressed another extractor", edges, err)
			}
			if strings.Contains(err.Error(), "parent-group-canary") || strings.Contains(err.Error(), "private-key-canary") {
				t.Fatal("parent group error included input values", err)
			}
		})
	}
}
