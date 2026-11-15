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
	"k8s.io/apimachinery/pkg/labels"
)

func TestLabelEqualityMatchesNativeKeyPresence(t *testing.T) {
	for _, selector := range []struct {
		name string
		set  map[string]string
	}{{"one-empty", map[string]string{"flag": ""}}, {"two-empty", map[string]string{"flag": "", "other": ""}}, {"mixed", map[string]string{"flag": "", "app": "selected"}}, {"nonempty", map[string]string{"flag": "value"}}, {"empty", nil}} {
		for index, have := range []map[string]string{nil, {}, {"other": ""}, {"flag": ""}, {"flag": "value"}, {"flag": "", "other": ""}, {"flag": "", "app": "selected"}} {
			t.Run(fmt.Sprintf("%s/labels=%d", selector.name, index), func(t *testing.T) {
				want := labels.SelectorFromSet(labels.Set(selector.set)).Matches(labels.Set(have))
				if got := matchLabelSelector(have, selector.set); got != want {
					t.Fatal("NetworkPolicy equality differs from native key-presence semantics", have, selector.set, got, want)
				}
				// Selectorless Services remain distinct from empty LabelSelectors.
				want = len(selector.set) != 0 && want
				if got := labelsMatch(have, selector.set); got != want {
					t.Fatal("Service equality confused absent keys with empty values", have, selector.set, got, want)
				}
			})
		}
	}
}

func TestEmptyLabelServiceAndNetworkPolicyExtraction(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, mode := range []string{"service", "selects", "ingress-pod", "egress-pod", "ingress-namespace", "egress-namespace", "ingress-combined", "egress-combined"} {
			t.Run("cluster="+cluster+"/"+mode, func(t *testing.T) {
				selector := map[string]any{"matchLabels": map[string]any{"flag": ""}}
				r := graph.Resource{ClusterID: cluster, Namespace: "test", Kind: "NetworkPolicy", Name: "source", Raw: map[string]any{"spec": map[string]any{"podSelector": selector}}}
				var ext Extractor = SelectsNPExtractor{}
				if mode == "service" {
					r.Kind, ext = "Service", SelectsExtractor{}
					r.Raw = map[string]any{"spec": map[string]any{"selector": map[string]any{"flag": ""}}}
				} else if mode != "selects" {
					direction, field, policyType := "ingress", "from", "Ingress"
					ext = AllowsFromExtractor{}
					if strings.HasPrefix(mode, "egress") {
						direction, field, policyType, ext = "egress", "to", "Egress", AllowsToExtractor{}
					}
					peer := map[string]any{"podSelector": selector}
					switch mode {
					case "ingress-namespace", "egress-namespace":
						peer = map[string]any{"namespaceSelector": selector}
					case "ingress-combined", "egress-combined":
						peer["namespaceSelector"] = selector
					}
					r.Raw = map[string]any{"spec": map[string]any{"policyTypes": []any{policyType}, direction: []any{map[string]any{field: []any{peer}}}}}
				}
				var candidates, expected []graph.Resource
				for _, kind := range []string{"Pod", "Deployment", "CronJob"} {
					for _, present := range []bool{false, true} {
						set := map[string]string{"app": "selected"}
						if present {
							set["flag"] = ""
						}
						target := graph.Resource{ClusterID: cluster, Namespace: "test", Kind: kind, Name: fmt.Sprintf("%s-%t", kind, present), Labels: set}
						if kind != "Pod" {
							meta := map[string]any{"labels": map[string]any{"app": "selected"}}
							if present {
								meta["labels"].(map[string]any)["flag"] = ""
							}
							template := map[string]any{"template": map[string]any{"metadata": meta}}
							if kind == "CronJob" {
								template = map[string]any{"jobTemplate": map[string]any{"spec": template}}
							}
							target.Raw = map[string]any{"spec": template}
						}
						candidates = append(candidates, target)
						if present {
							expected = append(expected, target)
						}
					}
				}
				namespace := graph.Resource{ClusterID: cluster, Kind: "Namespace", Name: "test", Labels: map[string]string{"flag": ""}}
				q := networkPolicyFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
					if f.ClusterID != cluster {
						t.Fatal("selector query changed cluster scope", f)
					}
					if f.Kind == "Namespace" {
						return []graph.Resource{namespace, {ClusterID: cluster, Kind: "Namespace", Name: "absent"}}, nil
					}
					if f.Namespace != "test" {
						t.Fatal("missing namespace key expanded a peer query", f)
					}
					return candidates, nil
				})
				if mode == "ingress-namespace" || mode == "egress-namespace" {
					expected = []graph.Resource{namespace}
				}
				edges, err := ext.Extract(context.Background(), r, q)
				var want []graph.Edge
				for _, target := range expected {
					want = append(want, graph.Edge{From: r.ID(), To: target.ID(), Type: ext.Type()})
				}
				if err != nil || !reflect.DeepEqual(edges, want) {
					t.Fatal("empty label selector selected objects without the key", edges, want, err)
				}
			})
		}
	}
}
