// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func accountShapeResource(kind string, spec map[string]any) graph.Resource {
	outer := spec
	if kind == "CronJob" {
		outer = map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
	} else if kind != "Pod" {
		outer = map[string]any{"template": map[string]any{"spec": spec}}
	}
	return graph.Resource{Kind: kind, Namespace: "demo", Name: "consumer", Raw: map[string]any{"spec": outer}}
}

func TestServiceAccountMalformedNameDoesNotFallback(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		for _, cluster := range []string{"east", ""} {
			for _, field := range []string{"serviceAccountName", "serviceAccount"} {
				for _, tc := range []struct {
					name  string
					value any
				}{{"boolean", true}, {"number", int64(7)}, {"array", []any{"account-name-canary"}}, {"object", map[string]any{"private-key-canary": "account-name-canary"}}} {
					t.Run(fmt.Sprintf("%s/cluster=%s/%s/%s", kind, cluster, field, tc.name), func(t *testing.T) {
						spec := map[string]any{"serviceAccountName": "", "serviceAccount": "fallback", "containers": []any{map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "config"}}}}}}
						spec[field] = tc.value
						r := accountShapeResource(kind, spec)
						r.ClusterID = cluster
						q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
							t.Fatal("account declaration extraction read target objects")
							return nil, nil
						})
						edges, err := (ServiceAccountExtractor{}).Extract(context.Background(), r, q)
						if err == nil || len(edges) != 0 {
							t.Fatal("malformed account field became fallback/default association", edges, err)
						}
						if strings.Contains(err.Error(), "account-name-canary") || strings.Contains(err.Error(), "private-key-canary") {
							t.Fatal("account name error included input values", err)
						}
						registry := New()
						registry.Register(ServiceAccountExtractor{})
						registry.Register(ConfigMapExtractor{})
						edges, err = registry.ExtractAll(context.Background(), r, q)
						want := []graph.Edge{{From: r.ID(), To: (graph.Resource{ClusterID: cluster, Kind: "ConfigMap", Namespace: r.Namespace, Name: "config"}).ID(), Type: graph.EdgeTypeUsesConfigMap}}
						if err == nil || !reflect.DeepEqual(edges, want) {
							t.Fatal("account error suppressed independent valid references", edges, err)
						}
					})
				}
			}
		}
	}
}

func TestServiceAccountNamePrecedenceAndEmptyCompatibility(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		for _, tc := range []struct {
			name string
			spec map[string]any
			want string
		}{
			{"omitted", map[string]any{}, "default"},
			{"null", map[string]any{"serviceAccountName": nil, "serviceAccount": nil}, "default"},
			{"empty", map[string]any{"serviceAccountName": ""}, "default"},
			{"legacy", map[string]any{"serviceAccount": "legacy"}, "legacy"},
			{"empty-modern-legacy", map[string]any{"serviceAccountName": "", "serviceAccount": "legacy"}, "legacy"},
			{"null-modern-legacy", map[string]any{"serviceAccountName": nil, "serviceAccount": "legacy"}, "legacy"},
			{"modern-priority", map[string]any{"serviceAccountName": "selected", "serviceAccount": "other"}, "selected"},
			{"unused-malformed-legacy", map[string]any{"serviceAccountName": "selected", "serviceAccount": true}, "selected"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				r := accountShapeResource(kind, tc.spec)
				before, err := json.Marshal(r.Raw)
				if err != nil {
					t.Fatal(err)
				}
				edges, err := (ServiceAccountExtractor{}).Extract(context.Background(), r, nil)
				want := []graph.Edge{{From: r.ID(), To: "demo/ServiceAccount/" + tc.want, Type: graph.EdgeTypeUsesServiceAccount}}
				if err != nil || !reflect.DeepEqual(edges, want) {
					t.Fatal("account field validation changed valid precedence or empty-field behavior", edges, want, err)
				}
				after, err := json.Marshal(r.Raw)
				if err != nil || string(before) != string(after) {
					t.Fatal("account extraction modified the source", err)
				}
			})
		}
	}
}
