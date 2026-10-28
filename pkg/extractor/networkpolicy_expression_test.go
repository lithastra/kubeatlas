// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func expressionSelector(operator string, values ...any) map[string]any {
	return map[string]any{"matchExpressions": []any{map[string]any{"key": "role", "operator": operator, "values": values}}}
}

func expressionPolicy(mode string, selector any) (graph.Resource, Extractor) {
	r := makeNP("demo", "policy", map[string]any{"podSelector": selector})
	if mode == "selects" {
		return r, SelectsNPExtractor{}
	}
	direction, field, policyType := "ingress", "from", "Ingress"
	var ext Extractor = AllowsFromExtractor{}
	if strings.HasPrefix(mode, "egress") {
		direction, field, policyType, ext = "egress", "to", "Egress", AllowsToExtractor{}
	}
	peer := map[string]any{"podSelector": selector}
	if strings.HasSuffix(mode, "namespace") {
		peer = map[string]any{"namespaceSelector": selector}
	}
	if strings.HasSuffix(mode, "combined") {
		peer["namespaceSelector"] = selector
	}
	r.Raw["spec"] = map[string]any{"policyTypes": []any{policyType}, direction: []any{map[string]any{field: []any{peer}}}}
	return r, ext
}

func TestNetworkPolicyExpressions(t *testing.T) {
	sets := []map[string]string{nil, {"role": ""}, {"role": "blue"}, {"role": "red"}, {"role": "green"}, {"role": "blue", "disabled": "yes"}}
	cases := []struct {
		name     string
		selector map[string]any
		want     []bool
	}{
		{"in", expressionSelector("In", "blue", "green"), []bool{false, false, true, false, true, true}},
		{"not-in", expressionSelector("NotIn", "red"), []bool{true, true, true, false, true, true}},
		{"exists", expressionSelector("Exists"), []bool{false, true, true, true, true, true}},
		{"does-not-exist", expressionSelector("DoesNotExist"), []bool{true, false, false, false, false, false}},
		{"in-empty", expressionSelector("In", ""), []bool{false, true, false, false, false, false}},
		{"not-in-empty", expressionSelector("NotIn", ""), []bool{true, false, true, true, true, true}},
		{"duplicate-values", expressionSelector("In", "blue", "blue"), []bool{false, false, true, false, false, true}},
		{"empty-list", map[string]any{"matchExpressions": []any{}}, []bool{true, true, true, true, true, true}},
		{"null-list", map[string]any{"matchExpressions": nil}, []bool{true, true, true, true, true, true}},
		{"and", map[string]any{"matchLabels": map[string]any{"role": "blue"}, "matchExpressions": []any{map[string]any{"key": "disabled", "operator": "DoesNotExist"}, map[string]any{"key": "role", "operator": "In", "values": []any{"blue", "green"}}}}, []bool{false, false, true, false, false, false}},
		{"contradiction", map[string]any{"matchLabels": map[string]any{"role": "red"}, "matchExpressions": expressionSelector("In", "blue")["matchExpressions"]}, []bool{false, false, false, false, false, false}},
	}
	for _, tc := range cases {
		// Independently decode the fixture into the pinned Kubernetes API oracle.
		data, err := json.Marshal(tc.selector)
		if err != nil {
			t.Fatal(err)
		}
		var typed metav1.LabelSelector
		if err := json.Unmarshal(data, &typed); err != nil {
			t.Fatal(err)
		}
		native, err := metav1.LabelSelectorAsSelector(&typed)
		if err != nil {
			t.Fatal(err)
		}
		for i, set := range sets {
			if native.Matches(labels.Set(set)) != tc.want[i] {
				t.Fatal("invalid native matching oracle", tc.name, i)
			}
		}
		for _, mode := range []string{"selects", "ingress-pod", "egress-pod", "ingress-namespace", "egress-namespace", "ingress-combined", "egress-combined"} {
			for _, cluster := range []string{"east", ""} {
				t.Run(fmt.Sprintf("%s/%s/cluster=%s", tc.name, mode, cluster), func(t *testing.T) {
					r, ext := expressionPolicy(mode, tc.selector)
					r.ClusterID = cluster
					before, _ := json.Marshal(r.Raw)
					var pods, namespaces []graph.Resource
					var want []graph.Edge
					nsOnly := strings.HasSuffix(mode, "namespace")
					combined := strings.HasSuffix(mode, "combined")
					for i, set := range sets {
						ns := fmt.Sprintf("ns-%d", i)
						namespaces = append(namespaces, graph.Resource{ClusterID: cluster, Kind: "Namespace", Name: ns, Labels: set})
						if !combined {
							ns = "demo"
						}
						for _, kind := range []string{"Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
							target := accountShapeResource(kind, map[string]any{})
							target.ClusterID, target.Namespace, target.Name = cluster, ns, fmt.Sprintf("%s-%d", kind, i)
							meta := map[string]any{"labels": map[string]any{}}
							for k, v := range set {
								meta["labels"].(map[string]any)[k] = v
							}
							switch kind {
							case "Pod":
								target.Labels = set
							case "CronJob":
								target.Raw["spec"].(map[string]any)["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)["metadata"] = meta
							default:
								target.Raw["spec"].(map[string]any)["template"].(map[string]any)["metadata"] = meta
							}
							pods = append(pods, target)
							if !nsOnly && tc.want[i] {
								want = append(want, graph.Edge{From: r.ID(), To: target.ID(), Type: ext.Type()})
							}
						}
						if nsOnly && tc.want[i] {
							want = append(want, graph.Edge{From: r.ID(), To: namespaces[i].ID(), Type: ext.Type()})
						}
					}
					var reads []graph.Filter
					q := networkPolicyFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
						reads = append(reads, f)
						if f.ClusterID != cluster || len(f.Labels) != 0 {
							t.Fatal("expression altered query scope/filter", f)
						}
						if f.Kind == "Namespace" {
							return namespaces, nil
						}
						if f.Namespace == "" {
							t.Fatal("unbounded Pod query", f)
						}
						var out []graph.Resource
						for _, p := range pods {
							if p.Namespace == f.Namespace {
								out = append(out, p)
							}
						}
						return out, nil
					})
					edges, err := ext.Extract(context.Background(), r, q)
					wantReads := 1
					if combined {
						for _, match := range tc.want {
							wantReads += boolEdgeCount(match)
						}
					}
					after, _ := json.Marshal(r.Raw)
					if err != nil || !reflect.DeepEqual(edges, want) || len(reads) != wantReads || string(before) != string(after) {
						t.Fatal("expression matching lost conjunction, canonical metadata or scoped reads", edges, want, reads, err)
					}
				})
			}
		}
	}
}

func TestNetworkPolicyMalformedExpressions(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"scalar", "expression-value-canary"}, {"object", map[string]any{}}, {"null-entry", []any{nil}}, {"scalar-entry", []any{true}}, {"array-entry", []any{[]any{}}},
		{"missing-key", []any{map[string]any{"operator": "Exists"}}}, {"null-key", []any{map[string]any{"key": nil, "operator": "Exists"}}}, {"wrong-key", []any{map[string]any{"key": true, "operator": "Exists"}}},
		{"empty-key", []any{map[string]any{"key": "", "operator": "Exists"}}}, {"invalid-key", []any{map[string]any{"key": "expression-key-canary/invalid/key", "operator": "Exists"}}},
		{"missing-operator", []any{map[string]any{"key": "role"}}}, {"wrong-operator", []any{map[string]any{"key": "role", "operator": true}}},
		{"unsupported-operator", expressionSelector("Gt", "1")["matchExpressions"]}, {"empty-operator", expressionSelector("")["matchExpressions"]},
		{"in-empty", expressionSelector("In")["matchExpressions"]}, {"not-in-empty", expressionSelector("NotIn")["matchExpressions"]},
		{"exists-values", expressionSelector("Exists", "blue")["matchExpressions"]}, {"absent-values", expressionSelector("DoesNotExist", "blue")["matchExpressions"]},
		{"values-object", []any{map[string]any{"key": "role", "operator": "In", "values": map[string]any{}}}},
		{"values-scalar", []any{map[string]any{"key": "role", "operator": "In", "values": "expression-value-canary"}}},
		{"values-type", expressionSelector("In", "blue", true)["matchExpressions"]}, {"invalid-value", expressionSelector("In", "expression-value-canary/invalid")["matchExpressions"]},
		{"later-entry", append(expressionSelector("In", "blue")["matchExpressions"].([]any), nil)},
	}
	for _, tc := range cases {
		for _, mode := range []string{"selects", "ingress-pod", "egress-pod", "ingress-namespace", "egress-namespace", "ingress-combined", "egress-combined"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				r, ext := expressionPolicy(mode, map[string]any{"matchLabels": map[string]any{"role": "blue"}, "matchExpressions": tc.value})
				reads := 0
				q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
					reads++
					return []graph.Resource{{Kind: "Pod", Name: "must-not-match", Namespace: "demo", Labels: map[string]string{"role": "blue"}}}, nil
				})
				edges, err := ext.Extract(context.Background(), r, q)
				if err == nil || len(edges) != 0 || reads != 0 {
					t.Fatal("malformed expression broadened selector or read candidates", edges, reads, err)
				}
				if strings.Contains(err.Error(), "expression-key-canary") || strings.Contains(err.Error(), "expression-value-canary") {
					t.Fatal("selector error leaked input", err)
				}
			})
		}
	}
}

func TestNetworkPolicyExpressionReadFailures(t *testing.T) {
	for _, mode := range []string{"ingress-combined", "egress-combined"} {
		for _, state := range []string{"read-failure", "cancel", "malformed-later-peer"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				r, ext := expressionPolicy(mode, expressionSelector("In", "blue"))
				spec := r.Raw["spec"].(map[string]any)
				field, peerField := "ingress", "from"
				if mode == "egress-combined" {
					field, peerField = "egress", "to"
				}
				rule := spec[field].([]any)[0].(map[string]any)
				if state == "malformed-later-peer" {
					rule[peerField] = append(rule[peerField].([]any), map[string]any{"podSelector": expressionSelector("Exists", "invalid")}, map[string]any{"podSelector": map[string]any{}})
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				failure := errors.New("injected query failure")
				q := networkPolicyFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
					calls++
					if f.Kind == "Namespace" {
						return []graph.Resource{{Kind: "Namespace", Name: "alpha", Labels: map[string]string{"role": "blue"}}, {Kind: "Namespace", Name: "beta", Labels: map[string]string{"role": "blue"}}}, nil
					}
					row := graph.Resource{Kind: "Pod", Namespace: f.Namespace, Name: "pod", Labels: map[string]string{"role": "blue"}}
					if f.Namespace == "beta" {
						if state == "read-failure" {
							return []graph.Resource{row}, failure
						}
						if state == "cancel" {
							cancel()
						}
					}
					return []graph.Resource{row}, nil
				})
				edges, err := ext.Extract(ctx, r, q)
				var want []graph.Edge
				if state != "cancel" {
					want = append(want, graph.Edge{From: r.ID(), To: "alpha/Pod/pod", Type: ext.Type()})
				}
				if state == "malformed-later-peer" {
					want = append(want, graph.Edge{From: r.ID(), To: "beta/Pod/pod", Type: ext.Type()})
				}
				if err == nil || !reflect.DeepEqual(edges, want) || calls != 3 || errors.Is(err, failure) != (state == "read-failure") || errors.Is(err, context.Canceled) != (state == "cancel") {
					t.Fatal("expression query failure accepted partial rows, lost prefix/error, or continued reads", edges, calls, err)
				}
			})
		}
	}
}
