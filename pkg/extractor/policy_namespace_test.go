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

func policyNamespaceFixture(mode string, namespaces any) (graph.Resource, Extractor) {
	r, ext := policySelectorFixture(mode, nil)
	if mode == "gatekeeper" {
		r.Raw["spec"].(map[string]any)["match"].(map[string]any)["namespaces"] = namespaces
	} else {
		group := strings.Split(mode, "/")[1]
		rule := r.Raw["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
		rule["match"].(map[string]any)[group].([]any)[0].(map[string]any)["resources"].(map[string]any)["namespaces"] = namespaces
	}
	return r, ext
}

func TestPolicyMalformedNamespacesRejectTargetReads(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, mode := range []string{"gatekeeper", "ClusterPolicy/any", "ClusterPolicy/all", "Policy/any", "Policy/all"} {
			for _, tc := range []struct {
				name  string
				value any
			}{
				{"scalar", "namespace-selector-canary"},
				{"object", map[string]any{"private-key-canary": "namespace-selector-canary"}},
				{"boolean", true},
				{"boolean-item", []any{true}},
				{"mixed", []any{"demo", int64(7)}},
				{"null-item", []any{nil}},
				{"empty-item", []any{""}},
				{"nested-item", []any{map[string]any{"private-key-canary": "namespace-selector-canary"}}},
			} {
				t.Run(fmt.Sprintf("cluster=%s/%s/%s", cluster, mode, tc.name), func(t *testing.T) {
					r, ext := policyNamespaceFixture(mode, tc.value)
					r.ClusterID = cluster
					var reads []string
					q := kyvernoFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
						reads = append(reads, f.Kind)
						return nil, nil
					})
					edges, err := ext.Extract(context.Background(), r, q)
					var want []string
					if mode != "gatekeeper" {
						want = []string{"PolicyReport", "ClusterPolicyReport"}
					}
					if err == nil || len(edges) != 0 || !reflect.DeepEqual(reads, want) {
						t.Fatal("malformed namespaces became a partial or wildcard target query", edges, reads, err)
					}
					if strings.Contains(err.Error(), "namespace-selector-canary") || strings.Contains(err.Error(), "private-key-canary") {
						t.Fatal("namespace selector error included input data", err)
					}
				})
			}
		}
	}
}

func TestPolicyNamespaceCompatibility(t *testing.T) {
	for _, mode := range []string{"gatekeeper", "ClusterPolicy/any", "ClusterPolicy/all", "Policy/any", "Policy/all"} {
		for _, tc := range []struct {
			name  string
			value any
			want  []string
		}{
			{"null", nil, []string{"demo/Pod/local", "other/Pod/remote"}},
			{"empty", []any{}, []string{"demo/Pod/local", "other/Pod/remote"}},
			{"local", []any{"demo"}, []string{"demo/Pod/local"}},
			{"remote", []any{"other"}, []string{"other/Pod/remote"}},
			{"both", []any{"demo", "other"}, []string{"demo/Pod/local", "other/Pod/remote"}},
			{"literal-pattern", []any{"demo*"}, nil},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				r, ext := policyNamespaceFixture(mode, tc.value)
				rows := sliceLister{{Kind: "Pod", Namespace: "demo", Name: "local"}, {Kind: "Pod", Namespace: "other", Name: "remote"}}
				edges, err := ext.Extract(context.Background(), r, rows)
				var ids []string
				for _, edge := range edges {
					if edge.From != r.ID() || edge.Type != graph.EdgeTypeEnforces {
						t.Fatal("namespace selector changed the policy edge", edge)
					}
					ids = append(ids, edge.To)
				}
				want := tc.want
				if strings.HasPrefix(mode, "Policy/") {
					want = []string{"demo/Pod/local"} // A Policy remains confined to its own namespace.
				}
				if err != nil || !reflect.DeepEqual(ids, want) {
					t.Fatal("namespace shape guard changed empty, literal or Policy scope semantics", ids, want, err)
				}
			})
		}
	}
}
