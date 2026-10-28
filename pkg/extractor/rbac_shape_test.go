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

func TestRBACDeclarationShapes(t *testing.T) {
	for _, binding := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, cluster := range []string{"east", ""} {
			for _, field := range []string{"subjects", "roleRef"} {
				for _, tc := range []struct {
					name           string
					value          any
					omitted, valid bool
				}{
					{name: "omitted", omitted: true, valid: true},
					{name: "null", value: nil, valid: true},
					{name: "empty", valid: true},
					{name: "string", value: "shape-canary"},
					{name: "boolean", value: true},
					{name: "number", value: int64(3)},
					{name: "wrong-container"},
				} {
					t.Run(fmt.Sprintf("%s/cluster=%s/%s/%s", binding, cluster, field, tc.name), func(t *testing.T) {
						r := rbHelper("target", "binding", []map[string]any{{"kind": "ServiceAccount", "name": "worker", "namespace": "app"}}, map[string]any{"kind": "ClusterRole", "name": "reader"})
						if binding == "ClusterRoleBinding" {
							r.Kind, r.Namespace = binding, ""
						}
						r.ClusterID = cluster
						value := tc.value
						if tc.name == "empty" {
							if field == "subjects" {
								value = []any{}
							} else {
								value = map[string]any{}
							}
						}
						if tc.name == "wrong-container" {
							if field == "subjects" {
								value = map[string]any{"private-key-canary": "shape-canary"}
							} else {
								value = []any{map[string]any{"kind": "ClusterRole", "name": "reader"}}
							}
						}
						if tc.omitted {
							delete(r.Raw, field)
						} else {
							r.Raw[field] = value
						}
						var check Extractor = BindsSubjectExtractor{}
						independent := graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ClusterRole", Name: "reader"}).ID(), Type: graph.EdgeTypeBindsRole}
						if field == "roleRef" {
							check = BindsRoleExtractor{}
							independent = graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "worker"}).ID(), Type: graph.EdgeTypeBindsSubject}
						}
						q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
							t.Fatal("RBAC shape checks must not query target objects")
							return nil, nil
						})
						edges, err := check.Extract(context.Background(), r, q)
						valid := tc.valid && (field != "roleRef" || tc.name != "empty")
						if (err == nil) != valid || len(edges) != 0 {
							t.Fatal("malformed RBAC container was ignored or invented edges", edges, err)
						}
						if err != nil && (strings.Contains(err.Error(), "shape-canary") || strings.Contains(err.Error(), "private-key-canary")) {
							t.Fatal("shape error included input data", err)
						}
						registry := New()
						registry.Register(BindsSubjectExtractor{})
						registry.Register(BindsRoleExtractor{})
						batch, batchErr := registry.ExtractAll(context.Background(), r, q)
						if (batchErr == nil) != valid || !reflect.DeepEqual(batch, []graph.Edge{independent}) {
							t.Fatal("shape error suppressed independent RBAC extraction", batch, batchErr)
						}
					})
				}
			}
		}
	}
}

func TestRBACSubjectEntryShapes(t *testing.T) {
	for _, binding := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, cluster := range []string{"east", ""} {
			for _, prefix := range []bool{false, true} {
				for _, tc := range []struct {
					name  string
					value any
				}{
					{name: "null"},
					{name: "nil-object", value: map[string]any(nil)},
					{name: "string", value: "shape-canary"},
					{name: "boolean", value: true},
					{name: "number", value: int64(3)},
					{name: "array", value: []any{map[string]any{"private-key-canary": "shape-canary"}}},
				} {
					t.Run(fmt.Sprintf("%s/cluster=%s/prefix=%t/%s", binding, cluster, prefix, tc.name), func(t *testing.T) {
						r := rbHelper("target", "binding", nil, map[string]any{"kind": "ClusterRole", "name": "reader"})
						if binding == "ClusterRoleBinding" {
							r.Kind, r.Namespace = binding, ""
						}
						r.ClusterID = cluster
						subjects := []any{}
						want := []graph.Edge{}
						if prefix {
							subjects = append(subjects, map[string]any{"kind": "ServiceAccount", "name": "prefix", "namespace": "app"})
							want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "prefix"}).ID(), Type: graph.EdgeTypeBindsSubject})
						}
						r.Raw["subjects"] = append(subjects, tc.value, map[string]any{"kind": "ServiceAccount", "name": "tail", "namespace": "app"})
						q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
							t.Fatal("subject shape checks must not query target objects")
							return nil, nil
						})
						edges, err := (BindsSubjectExtractor{}).Extract(context.Background(), r, q)
						if err == nil || !reflect.DeepEqual(edges, want) {
							t.Fatal("malformed subject lost its prefix or continued to later entries", edges, want, err)
						}
						if strings.Contains(err.Error(), "shape-canary") || strings.Contains(err.Error(), "private-key-canary") {
							t.Fatal("subject shape error included input data", err)
						}
						registry := New()
						registry.Register(BindsSubjectExtractor{})
						registry.Register(BindsRoleExtractor{})
						batch, batchErr := registry.ExtractAll(context.Background(), r, q)
						want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ClusterRole", Name: "reader"}).ID(), Type: graph.EdgeTypeBindsRole})
						if batchErr == nil || !reflect.DeepEqual(batch, want) {
							t.Fatal("subject shape error suppressed independent role extraction", batch, want, batchErr)
						}
					})
				}
			}
		}
	}
}
