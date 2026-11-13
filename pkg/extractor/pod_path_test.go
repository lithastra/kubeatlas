// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestBuiltinPodSpecPathsIgnoreDecoys(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		states := []string{"populated", "empty-lists", "null-lists", "omitted-lists"}
		if kind != "Pod" {
			states = append(states, "missing-spec", "null-spec", "wrong-spec")
		}
		for _, cluster := range []string{"east", ""} {
			for _, state := range states {
				t.Run(fmt.Sprintf("%s/cluster=%s/%s", kind, cluster, state), func(t *testing.T) {
					spec := podPathTestSpec("selected")
					if state != "populated" {
						for field := range spec {
							switch state {
							case "empty-lists":
								if field == "serviceAccountName" {
									spec[field] = ""
								} else {
									spec[field] = []any{}
								}
							case "null-lists":
								spec[field] = nil
							case "omitted-lists":
								delete(spec, field)
							}
						}
					}
					r := accountShapeResource(kind, spec)
					r.ClusterID = cluster
					outer := r.Raw["spec"].(map[string]any)
					missingSpec := false
					switch state {
					case "missing-spec", "null-spec", "wrong-spec":
						missingSpec = true
						template := outer["template"]
						if kind == "CronJob" {
							template = outer["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"]
						}
						podTemplate := template.(map[string]any)
						switch state {
						case "missing-spec":
							delete(podTemplate, "spec")
						case "null-spec":
							podTemplate["spec"] = nil
						case "wrong-spec":
							podTemplate["spec"] = true
						}
					}
					decoy := podPathTestSpec("decoy")
					if kind != "Pod" {
						for field, value := range decoy {
							outer[field] = value
						}
					}
					if kind == "Pod" || kind == "CronJob" {
						outer["template"] = map[string]any{"spec": decoy}
					}
					if kind != "CronJob" {
						outer["jobTemplate"] = map[string]any{"spec": map[string]any{"template": map[string]any{"spec": decoy}}}
					}
					before, err := json.Marshal(r.Raw)
					if err != nil {
						t.Fatal(err)
					}
					q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
						t.Fatal("builtin reference extraction must not query targets")
						return nil, nil
					})
					registry := New()
					for _, e := range []Extractor{ConfigMapExtractor{}, SecretExtractor{}, VolumeExtractor{}, ServiceAccountExtractor{}} {
						registry.Register(e)
					}
					edges, err := registry.ExtractAll(context.Background(), r, q)
					want := map[graph.EdgeType][]string{graph.EdgeTypeUsesServiceAccount: {"default"}}
					if missingSpec {
						want = nil
					}
					if state == "populated" {
						want = map[graph.EdgeType][]string{
							graph.EdgeTypeUsesConfigMap:      {"selected-containers-config", "selected-initContainers-config", "selected-ephemeralContainers-config", "selected-volume-config", "selected-projected-config"},
							graph.EdgeTypeUsesSecret:         {"selected-containers-secret", "selected-initContainers-secret", "selected-ephemeralContainers-secret", "selected-volume-secret", "selected-projected-secret", "selected-pull"},
							graph.EdgeTypeMountsVolume:       {"selected-claim"},
							graph.EdgeTypeUsesServiceAccount: {"selected-account"},
						}
					}
					expected := make(map[[3]string]bool)
					for edgeType, names := range want {
						targetKind := map[graph.EdgeType]string{graph.EdgeTypeUsesConfigMap: "ConfigMap", graph.EdgeTypeUsesSecret: "Secret", graph.EdgeTypeMountsVolume: "PersistentVolumeClaim", graph.EdgeTypeUsesServiceAccount: "ServiceAccount"}[edgeType]
						for _, name := range names {
							expected[[3]string{r.ID(), (graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: targetKind, Name: name}).ID(), string(edgeType)}] = true
						}
					}
					if err != nil || len(edges) != len(expected) {
						t.Fatal("Pod spec lookup fell back to decoy fields or lost canonical references", edges, expected, err)
					}
					for _, edge := range edges {
						key := [3]string{edge.From, edge.To, string(edge.Type)}
						if !expected[key] {
							t.Fatal("unexpected reference outside the kind's Pod spec", edge)
						}
						delete(expected, key)
					}
					after, err := json.Marshal(r.Raw)
					if err != nil || string(before) != string(after) {
						t.Fatal("Pod path lookup mutated the source", err)
					}
				})
			}
		}
	}
}

func podPathTestSpec(prefix string) map[string]any {
	spec := map[string]any{"serviceAccountName": prefix + "-account", "imagePullSecrets": []any{map[string]any{"name": prefix + "-pull"}}}
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		spec[field] = []any{map[string]any{"envFrom": []any{
			map[string]any{"configMapRef": map[string]any{"name": prefix + "-" + field + "-config"}},
			map[string]any{"secretRef": map[string]any{"name": prefix + "-" + field + "-secret"}},
		}}}
	}
	spec["volumes"] = []any{
		map[string]any{"configMap": map[string]any{"name": prefix + "-volume-config"}},
		map[string]any{"secret": map[string]any{"secretName": prefix + "-volume-secret"}},
		map[string]any{"persistentVolumeClaim": map[string]any{"claimName": prefix + "-claim"}},
		map[string]any{"projected": map[string]any{"sources": []any{
			map[string]any{"configMap": map[string]any{"name": prefix + "-projected-config"}},
			map[string]any{"secret": map[string]any{"name": prefix + "-projected-secret"}},
		}}},
	}
	return spec
}
