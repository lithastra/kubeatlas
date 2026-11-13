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

func TestBindsRoleAPIGroupIdentity(t *testing.T) {
	for _, fixture := range []struct{ binding, role string }{{"RoleBinding", "Role"}, {"RoleBinding", "ClusterRole"}, {"ClusterRoleBinding", "ClusterRole"}} {
		for _, cluster := range []string{"east", ""} {
			for _, tc := range []struct {
				name          string
				group         any
				omitted, want bool
			}{
				{name: "omitted", omitted: true, want: true},
				{name: "empty", group: "", want: true},
				{name: "rbac", group: "rbac.authorization.k8s.io", want: true},
				{name: "foreign", group: "role-group-canary.example"},
				{name: "core-alias", group: "core"},
				{name: "null", group: nil},
				{name: "boolean", group: true},
				{name: "array", group: []any{"role-group-canary"}},
				{name: "object", group: map[string]any{"private-key-canary": "role-group-canary"}},
			} {
				t.Run(fmt.Sprintf("%s/%s/cluster=%s/%s", fixture.binding, fixture.role, cluster, tc.name), func(t *testing.T) {
					ref := map[string]any{"kind": fixture.role, "name": "reader"}
					if !tc.omitted {
						ref["apiGroup"] = tc.group
					}
					r := rbHelper("target", "binding", []map[string]any{{"kind": "ServiceAccount", "name": "worker", "namespace": "app"}}, ref)
					if fixture.binding == "ClusterRoleBinding" {
						r.Kind, r.Namespace = fixture.binding, ""
					}
					r.ClusterID = cluster
					targetNS := ""
					if fixture.role == "Role" {
						targetNS = r.Namespace
					}
					q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
						t.Fatal("role reference extraction must not query role objects or rules")
						return nil, nil
					})
					edges, err := (BindsRoleExtractor{}).Extract(context.Background(), r, q)
					var want []graph.Edge
					if tc.want {
						want = []graph.Edge{{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: fixture.role, Namespace: targetNS, Name: "reader"}).ID(), Type: graph.EdgeTypeBindsRole}}
					}
					if (err == nil) != tc.want || !reflect.DeepEqual(edges, want) {
						t.Fatal("role reference group was ignored or changed valid defaulting/scope", edges, want, err)
					}
					if err != nil && (strings.Contains(err.Error(), "role-group-canary") || strings.Contains(err.Error(), "private-key-canary")) {
						t.Fatal("role group error included input data", err)
					}
					registry := New()
					registry.Register(BindsRoleExtractor{})
					registry.Register(BindsSubjectExtractor{})
					batch, batchErr := registry.ExtractAll(context.Background(), r, q)
					want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "worker"}).ID(), Type: graph.EdgeTypeBindsSubject})
					if (batchErr == nil) != tc.want || !reflect.DeepEqual(batch, want) {
						t.Fatal("role group error suppressed independent subject extraction", batch, want, batchErr)
					}
				})
			}
		}
	}
}
