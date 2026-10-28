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

func TestRBACReferenceKinds(t *testing.T) {
	for _, binding := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, cluster := range []string{"east", ""} {
			for _, subject := range []bool{false, true} {
				for _, kind := range []string{"ServiceAccount", "User", "Group", "Role", "ClusterRole", "Secret", "kind-canary"} {
					t.Run(fmt.Sprintf("%s/cluster=%s/subject=%t/%s", binding, cluster, subject, kind), func(t *testing.T) {
						ref := map[string]any{"kind": kind, "name": "worker"}
						if subject && kind != "User" && kind != "Group" {
							ref["namespace"] = "app"
							ref["apiGroup"] = ""
						}
						valid := kind == "ClusterRole" || kind == "Role" && binding == "RoleBinding"
						if subject {
							valid = kind == "ServiceAccount" || kind == "User" || kind == "Group"
						}
						testRBACReferenceIdentity(t, binding, cluster, subject, ref, valid)
					})
				}
			}
		}
	}
}

func TestRBACReferenceIdentityFields(t *testing.T) {
	for _, binding := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, cluster := range []string{"east", ""} {
			for _, subject := range []bool{false, true} {
				for _, field := range []string{"kind", "name"} {
					for _, tc := range []struct {
						name    string
						value   any
						omitted bool
					}{
						{name: "omitted", omitted: true},
						{name: "null"},
						{name: "empty", value: ""},
						{name: "boolean", value: true},
						{name: "number", value: int64(3)},
						{name: "array", value: []any{"identity-canary"}},
						{name: "object", value: map[string]any{"private-key-canary": "identity-canary"}},
					} {
						t.Run(fmt.Sprintf("%s/cluster=%s/subject=%t/%s/%s", binding, cluster, subject, field, tc.name), func(t *testing.T) {
							ref := map[string]any{"kind": "ClusterRole", "name": "worker"}
							if subject {
								ref["kind"], ref["namespace"] = "ServiceAccount", "app"
							}
							if tc.omitted {
								delete(ref, field)
							} else {
								ref[field] = tc.value
							}
							testRBACReferenceIdentity(t, binding, cluster, subject, ref, false)
						})
					}
				}
			}
		}
	}
}

func testRBACReferenceIdentity(t *testing.T, binding, cluster string, subject bool, ref map[string]any, valid bool) {
	t.Helper()
	r := rbHelper("target", "binding", []map[string]any{{"kind": "ServiceAccount", "name": "independent", "namespace": "app"}}, map[string]any{"kind": "ClusterRole", "name": "independent"})
	if binding == "ClusterRoleBinding" {
		r.Kind, r.Namespace = binding, ""
	}
	r.ClusterID = cluster
	var check Extractor = BindsRoleExtractor{}
	var independent Extractor = BindsSubjectExtractor{}
	want := []graph.Edge{}
	independentEdge := graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "independent"}).ID(), Type: graph.EdgeTypeBindsSubject}
	kind, _ := ref["kind"].(string)
	ns := ""
	if subject {
		check, independent = BindsSubjectExtractor{}, BindsRoleExtractor{}
		r.Raw["subjects"] = []any{map[string]any{"kind": "ServiceAccount", "name": "prefix", "namespace": "app"}, ref, map[string]any{"kind": "ServiceAccount", "name": "tail", "namespace": "app"}}
		want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "prefix"}).ID(), Type: graph.EdgeTypeBindsSubject})
		independentEdge = graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ClusterRole", Name: "independent"}).ID(), Type: graph.EdgeTypeBindsRole}
		if kind == "ServiceAccount" {
			ns = "app"
		}
	} else {
		r.Raw["roleRef"] = ref
		if kind == "Role" {
			ns = r.Namespace
		}
	}
	if valid {
		name, _ := ref["name"].(string)
		want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: kind, Namespace: ns, Name: name}).ID(), Type: check.Type()})
		if subject {
			want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "tail"}).ID(), Type: graph.EdgeTypeBindsSubject})
		}
	}
	q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
		t.Fatal("RBAC identity checks must not query targets or rules")
		return nil, nil
	})
	edges, err := check.Extract(context.Background(), r, q)
	// Role extraction preserves a nil result on failure; subject extraction
	// retains its nonempty prefix. Compare edges independently of nil/empty.
	if (err == nil) != valid || len(edges) != len(want) || len(want) > 0 && !reflect.DeepEqual(edges, want) {
		t.Fatal("invalid RBAC identity was skipped or emitted an unsupported target", edges, want, err)
	}
	if err != nil && (strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "private-key")) {
		t.Fatal("identity error included input data", err)
	}
	registry := New()
	registry.Register(check)
	registry.Register(independent)
	batch, batchErr := registry.ExtractAll(context.Background(), r, q)
	want = append(want, independentEdge)
	if (batchErr == nil) != valid || !reflect.DeepEqual(batch, want) {
		t.Fatal("identity failure suppressed independent RBAC extraction", batch, want, batchErr)
	}
}
