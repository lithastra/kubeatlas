// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestRoutesIngressDefaultServiceBackend(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, tc := range []struct {
			name, spec string
			services   []string
		}{
			{"default-only", `{"defaultBackend":{"service":{"name":"fallback","port":{"number":80}}}}`, []string{"fallback"}},
			{"default-and-rule", `{"defaultBackend":{"service":{"name":"fallback"}},"rules":[{"http":{"paths":[{"backend":{"service":{"name":"specific"}}}]}}]}`, []string{"fallback", "specific"}},
			{"shared-service", `{"defaultBackend":{"service":{"name":"fallback","port":{"number":80}}},"rules":[{"http":{"paths":[{"backend":{"service":{"name":"fallback","port":{"number":8080}}}},{"backend":{"service":{"name":"specific"}}},{"backend":{"service":{"name":"specific"}}}]}}]}`, []string{"fallback", "specific"}},
			{"rule-only", `{"rules":[{"http":{"paths":[{"backend":{"service":{"name":"specific"}}}]}}]}`, []string{"specific"}},
			{"resource-only", `{"defaultBackend":{"resource":{"apiGroup":"example.com","kind":"Bucket","name":"storage"}}}`, nil},
			{"null-default", `{"defaultBackend":null}`, nil},
			{"malformed-default", `{"defaultBackend":["invalid"]}`, nil},
			{"malformed-service", `{"defaultBackend":{"service":"invalid"}}`, nil},
			{"missing-service-name", `{"defaultBackend":{"service":{"port":{"number":80}}}}`, nil},
			{"invalid-name-keeps-rule", `{"defaultBackend":{"service":{"name":7}},"rules":[{"http":{"paths":[{"backend":{"service":{"name":"specific"}}}]}}]}`, []string{"specific"}},
		} {
			t.Run("cluster="+cluster+"/"+tc.name, func(t *testing.T) {
				var spec map[string]any
				if err := json.Unmarshal([]byte(tc.spec), &spec); err != nil {
					t.Fatal(err)
				}
				r := graph.Resource{ClusterID: cluster, Kind: "Ingress", GroupVersion: "networking.k8s.io/v1", Namespace: "demo", Name: "entry", Raw: map[string]any{"spec": spec}}
				before, err := json.Marshal(r.Raw)
				if err != nil {
					t.Fatal(err)
				}
				q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
					t.Fatal("declarative backend extraction queried targets")
					return nil, nil
				})
				edges, err := (RoutesExtractor{}).Extract(context.Background(), r, q)
				var want []graph.Edge
				for _, name := range tc.services {
					to := graph.Resource{ClusterID: cluster, Kind: "Service", Namespace: r.Namespace, Name: name}
					want = append(want, graph.Edge{From: r.ID(), To: to.ID(), Type: graph.EdgeTypeRoutesTo})
				}
				if err != nil || !reflect.DeepEqual(edges, want) {
					t.Fatal("default/rule Service references missing or duplicated", edges, want, err)
				}
				after, err := json.Marshal(r.Raw)
				if err != nil || string(before) != string(after) {
					t.Fatal("backend extraction mutated input", err)
				}
			})
		}
	}
}
