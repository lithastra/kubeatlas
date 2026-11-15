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

func TestBindsSubjectAPIGroupIdentity(t *testing.T) {
	for _, binding := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, kind := range []string{"ServiceAccount", "User", "Group"} {
			for _, cluster := range []string{"east", ""} {
				for _, tc := range []struct {
					name    string
					group   any
					omitted bool
				}{
					{name: "omitted", omitted: true},
					{name: "empty", group: ""},
					{name: "rbac", group: "rbac.authorization.k8s.io"},
					{name: "foreign", group: "subject-group-canary.example"},
					{name: "core-alias", group: "core"},
					{name: "null", group: nil},
					{name: "boolean", group: true},
					{name: "array", group: []any{"subject-group-canary"}},
					{name: "object", group: map[string]any{"private-key-canary": "subject-group-canary"}},
				} {
					t.Run(fmt.Sprintf("%s/%s/cluster=%s/%s", binding, kind, cluster, tc.name), func(t *testing.T) {
						subject := map[string]any{"kind": kind, "name": "worker"}
						ns := ""
						if kind == "ServiceAccount" {
							ns = "app"
							// Exercise the existing RoleBinding namespace fallback.
							if binding == "ClusterRoleBinding" {
								subject["namespace"] = ns
							}
						}
						if !tc.omitted {
							subject["apiGroup"] = tc.group
						}
						r := rbHelper("app", "binding", []map[string]any{
							{"kind": "ServiceAccount", "name": "prefix", "namespace": "other"},
							subject,
							{"kind": "ServiceAccount", "name": "tail", "namespace": "other"},
						}, map[string]any{"kind": "ClusterRole", "name": "reader", "apiGroup": "rbac.authorization.k8s.io"})
						if binding == "ClusterRoleBinding" {
							r.Kind, r.Namespace = binding, ""
						}
						r.ClusterID = cluster
						q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
							t.Fatal("subject extraction must not query accounts, users or groups")
							return nil, nil
						})
						valid := tc.omitted || tc.name == "empty" || tc.name == "rbac" && kind != "ServiceAccount"
						want := []graph.Edge{{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "other", Name: "prefix"}).ID(), Type: graph.EdgeTypeBindsSubject}}
						if valid {
							want = append(want,
								graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: kind, Namespace: ns, Name: "worker"}).ID(), Type: graph.EdgeTypeBindsSubject},
								graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "other", Name: "tail"}).ID(), Type: graph.EdgeTypeBindsSubject})
						}
						edges, err := (BindsSubjectExtractor{}).Extract(context.Background(), r, q)
						if (err == nil) != valid || !reflect.DeepEqual(edges, want) {
							t.Fatal("subject group changed defaulting, scope or the confirmed prefix", edges, want, err)
						}
						if err != nil && (strings.Contains(err.Error(), "subject-group-canary") || strings.Contains(err.Error(), "private-key-canary")) {
							t.Fatal("subject group error included input data", err)
						}
						registry := New()
						registry.Register(BindsSubjectExtractor{})
						registry.Register(BindsRoleExtractor{})
						batch, batchErr := registry.ExtractAll(context.Background(), r, q)
						want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ClusterRole", Name: "reader"}).ID(), Type: graph.EdgeTypeBindsRole})
						if (batchErr == nil) != valid || !reflect.DeepEqual(batch, want) {
							t.Fatal("subject group error suppressed independent role extraction", batch, want, batchErr)
						}
					})
				}
			}
		}
	}
}
