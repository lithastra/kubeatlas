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

func TestConfigMapAllContainerAndProjectedSources(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		for _, cluster := range []string{"east", ""} {
			for _, tc := range []struct {
				name, spec string
				targets    []string
			}{
				{"regular", `{"containers":[{"envFrom":[{"configMapRef":{"name":"regular"}}],"env":[{"valueFrom":{"configMapKeyRef":{"name":"regular","key":"key-canary"}}}]}]}`, []string{"regular"}},
				{"init", `{"initContainers":[{"envFrom":[{"configMapRef":{"name":"init"}}],"env":[{"valueFrom":{"configMapKeyRef":{"name":"init"}}}]}]}`, []string{"init"}},
				{"ephemeral", `{"ephemeralContainers":[{"envFrom":[{"configMapRef":{"name":"ephemeral"}}],"env":[{"valueFrom":{"configMapKeyRef":{"name":"ephemeral"}}}]}]}`, []string{"ephemeral"}},
				{"projected", `{"volumes":[{"name":"mixed","projected":{"sources":[{"configMap":{"name":"first"}},{"secret":{"name":"ignored"}},{"configMap":{"name":"second"}},{"serviceAccountToken":{"path":"token-canary"}}]}}]}`, []string{"first", "second"}},
				{"shared-across-sources", `{"containers":[{"envFrom":[{"configMapRef":{"name":"shared"}}]}],"initContainers":[{"env":[{"valueFrom":{"configMapKeyRef":{"name":"shared"}}}]}],"ephemeralContainers":[{"envFrom":[{"configMapRef":{"name":"shared"}}]}],"volumes":[{"configMap":{"name":"shared"}},{"projected":{"sources":[{"configMap":{"name":"shared"}},{"configMap":{"name":"other"}},{"configMap":{"name":"other"}}]}}]}`, []string{"shared", "other"}},
				{"unrelated", `{"containers":[{"env":[{"name":"literal","value":"literal-canary"}]}],"volumes":[{"projected":{"sources":[{"secret":{"name":"ignored"}},{"downwardAPI":{"items":[]}}]}}]}`, nil},
				{"malformed-with-valid-row", `{"initContainers":[null,"invalid",{"envFrom":[{"configMapRef":null}]}],"ephemeralContainers":{},"volumes":[{"projected":{"sources":[null,"invalid",{"configMap":{"name":7}},{"configMap":{"name":"valid"}}]}},{"projected":{"sources":{}}}]}`, []string{"valid"}},
			} {
				t.Run(kind+"/cluster="+cluster+"/"+tc.name, func(t *testing.T) {
					var spec map[string]any
					if err := json.Unmarshal([]byte(tc.spec), &spec); err != nil {
						t.Fatal(err)
					}
					outer := spec
					if kind == "CronJob" {
						outer = map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
					} else if kind != "Pod" {
						outer = map[string]any{"template": map[string]any{"spec": spec}}
					}
					r := graph.Resource{ClusterID: cluster, Kind: kind, Namespace: "demo", Name: "consumer", Raw: map[string]any{"spec": outer}}
					before, err := json.Marshal(r.Raw)
					if err != nil {
						t.Fatal(err)
					}
					q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
						t.Fatal("ConfigMap declaration extraction read target objects")
						return nil, nil
					})
					edges, err := (ConfigMapExtractor{}).Extract(context.Background(), r, q)
					var want []graph.Edge
					for _, name := range tc.targets {
						to := graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "ConfigMap", Name: name}
						want = append(want, graph.Edge{From: r.ID(), To: to.ID(), Type: graph.EdgeTypeUsesConfigMap})
					}
					if err != nil || !reflect.DeepEqual(edges, want) {
						t.Fatal("container/projected ConfigMap references missing or duplicated", edges, want, err)
					}
					after, err := json.Marshal(r.Raw)
					if err != nil || string(after) != string(before) {
						t.Fatal("ConfigMap extraction mutated source", err)
					}
				})
			}
		}
	}
}
