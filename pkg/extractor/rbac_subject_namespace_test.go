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

func TestBindsSubjectServiceAccountNamespace(t *testing.T) {
	for _, binding := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, cluster := range []string{"east", ""} {
			for _, tc := range []struct {
				name      string
				namespace any
				omitted   bool
			}{
				{name: "omitted", omitted: true},
				{name: "null", namespace: nil},
				{name: "empty", namespace: ""},
				{name: "own", namespace: "target"},
				{name: "other", namespace: "app"},
				{name: "boolean", namespace: true},
				{name: "number", namespace: int64(3)},
				{name: "array", namespace: []any{"namespace-canary"}},
				{name: "object", namespace: map[string]any{"private-key-canary": "namespace-canary"}},
			} {
				t.Run(fmt.Sprintf("%s/cluster=%s/%s", binding, cluster, tc.name), func(t *testing.T) {
					subject := map[string]any{"kind": "ServiceAccount", "name": "worker", "apiGroup": ""}
					if !tc.omitted {
						subject["namespace"] = tc.namespace
					}
					r := rbHelper("target", "binding", []map[string]any{
						{"kind": "ServiceAccount", "name": "prefix", "namespace": "app"},
						subject,
						{"kind": "ServiceAccount", "name": "tail", "namespace": "app"},
					}, map[string]any{"kind": "ClusterRole", "name": "reader"})
					if binding == "ClusterRoleBinding" {
						r.Kind, r.Namespace = binding, ""
					}
					r.ClusterID = cluster
					ns, stringValue := tc.namespace.(string)
					valid := tc.omitted || tc.namespace == nil || stringValue
					if ns == "" {
						ns = r.Namespace
					}
					valid = valid && ns != ""
					want := []graph.Edge{{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "prefix"}).ID(), Type: graph.EdgeTypeBindsSubject}}
					if valid {
						want = append(want,
							graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: ns, Name: "worker"}).ID(), Type: graph.EdgeTypeBindsSubject},
							graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "tail"}).ID(), Type: graph.EdgeTypeBindsSubject})
					}
					q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
						t.Fatal("namespace resolution must not query accounts or namespaces")
						return nil, nil
					})
					edges, err := (BindsSubjectExtractor{}).Extract(context.Background(), r, q)
					if (err == nil) != valid || !reflect.DeepEqual(edges, want) {
						t.Fatal("subject namespace was coerced, invented or lost its prefix", edges, want, err)
					}
					if err != nil && (strings.Contains(err.Error(), "namespace-canary") || strings.Contains(err.Error(), "private-key-canary")) {
						t.Fatal("subject namespace error included input data", err)
					}
					registry := New()
					registry.Register(BindsSubjectExtractor{})
					registry.Register(BindsRoleExtractor{})
					batch, batchErr := registry.ExtractAll(context.Background(), r, q)
					want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ClusterRole", Name: "reader"}).ID(), Type: graph.EdgeTypeBindsRole})
					if (batchErr == nil) != valid || !reflect.DeepEqual(batch, want) {
						t.Fatal("subject namespace failure suppressed independent role extraction", batch, want, batchErr)
					}
				})
			}
		}
	}
}
