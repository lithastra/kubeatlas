// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestSelectorStringMapPreservesEmptyFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want map[string]string
	}{
		{"nil", nil, nil},
		{"omitted-spec", map[string]any{}, nil},
		{"null-spec", map[string]any{"spec": nil}, nil},
		{"omitted-selector", map[string]any{"spec": map[string]any{}}, nil},
		{"null-selector", map[string]any{"spec": map[string]any{"selector": nil}}, nil},
		{"empty-selector", map[string]any{"spec": map[string]any{"selector": map[string]any{}}}, nil},
		{"empty-value", map[string]any{"spec": map[string]any{"selector": map[string]any{"flag": ""}}}, map[string]string{"flag": ""}},
		{"conjunction", map[string]any{"spec": map[string]any{"selector": map[string]any{"flag": "", "app": "web"}}}, map[string]string{"flag": "", "app": "web"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectorStringMap(tc.raw, "spec", "selector")
			if err != nil || !maps.Equal(got, tc.want) {
				t.Fatal("selector shape guard changed valid or empty fields", got, tc.want, err)
			}
			// Successful decoding owns its output map.
			if len(got) != 0 {
				got["flag"] = "changed"
				again, err := selectorStringMap(tc.raw, "spec", "selector")
				if err != nil || !maps.Equal(again, tc.want) {
					t.Fatal("selector decode mutated its source", again, err)
				}
			}
		})
	}
}

func TestMalformedLabelSelectorsDoNotBroadenQueries(t *testing.T) {
	bad := []struct {
		name  string
		value any
	}{
		{"scalar", "selector-payload-canary"},
		{"array", []any{"selector-payload-canary"}},
		{"label-map-scalar", map[string]any{"matchLabels": "selector-payload-canary"}},
		{"boolean-value", map[string]any{"matchLabels": map[string]any{"app": true}}},
		{"mixed-values", map[string]any{"matchLabels": map[string]any{"app": "web", "invalid": int64(7)}}},
		{"nested-value", map[string]any{"matchLabels": map[string]any{"app": "web", "invalid": map[string]any{"private": "selector-payload-canary"}}}},
	}
	for _, cluster := range []string{"east", ""} {
		for _, mode := range []string{"service", "selects", "ingress-pod", "ingress-namespace", "ingress-combined-pod", "ingress-combined-namespace", "egress-pod", "egress-namespace", "egress-combined-pod", "egress-combined-namespace"} {
			for _, tc := range bad {
				t.Run(fmt.Sprintf("cluster=%s/%s/%s", cluster, mode, tc.name), func(t *testing.T) {
					r, ext := malformedSelectorSource(mode, tc.value)
					r.ClusterID = cluster
					calls := 0
					q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
						calls++
						return []graph.Resource{{ClusterID: cluster, Kind: "Pod", Namespace: r.Namespace, Name: "unproven", Labels: map[string]string{"app": "web"}}, {ClusterID: cluster, Kind: "Namespace", Name: r.Namespace}}, nil
					})
					edges, err := ext.Extract(context.Background(), r, q)
					if err == nil || len(edges) != 0 || calls != 0 {
						t.Fatal("malformed selector became a partial or wildcard query", edges, calls, err)
					}
					if strings.Contains(err.Error(), "selector-payload-canary") || strings.Contains(err.Error(), "private") {
						t.Fatal("selector error included input values", err)
					}
				})
			}
		}
	}
}

func malformedSelectorSource(mode string, invalid any) (graph.Resource, Extractor) {
	r := makeNP("demo", "policy", map[string]any{"podSelector": invalid})
	if mode == "service" {
		// Service selectors are the label map itself, without matchLabels.
		if selector, ok := invalid.(map[string]any); ok {
			invalid = selector["matchLabels"]
		}
		r.Kind, r.Name, r.Raw = "Service", "service", map[string]any{"spec": map[string]any{"selector": invalid}}
		return r, SelectsExtractor{}
	}
	if mode == "selects" {
		return r, SelectsNPExtractor{}
	}
	direction, field, policyType := "ingress", "from", "Ingress"
	var ext Extractor = AllowsFromExtractor{}
	if strings.HasPrefix(mode, "egress") {
		direction, field, policyType, ext = "egress", "to", "Egress", AllowsToExtractor{}
	}
	peer := map[string]any{}
	switch {
	case strings.HasSuffix(mode, "combined-pod"):
		peer["podSelector"], peer["namespaceSelector"] = invalid, map[string]any{}
	case strings.HasSuffix(mode, "combined-namespace"):
		peer["podSelector"], peer["namespaceSelector"] = map[string]any{}, invalid
	case strings.HasSuffix(mode, "namespace"):
		peer["namespaceSelector"] = invalid
	default:
		peer["podSelector"] = invalid
	}
	r.Raw = map[string]any{"spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{policyType}, direction: []any{map[string]any{field: []any{peer}}}}}
	return r, ext
}

func TestMalformedLaterPeerRetainsEarlierEdges(t *testing.T) {
	for _, direction := range []string{"ingress", "egress"} {
		for _, laterRule := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/later-rule=%t", direction, laterRule), func(t *testing.T) {
				r, ext := malformedSelectorSource(direction+"-pod", "selector-payload-canary")
				field := "from"
				if direction == "egress" {
					field = "to"
				}
				spec := r.Raw["spec"].(map[string]any)
				badPeer := spec[direction].([]any)[0].(map[string]any)[field].([]any)[0]
				good := map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}
				if laterRule {
					spec[direction] = []any{map[string]any{field: []any{good}}, map[string]any{field: []any{badPeer, good}}}
				} else {
					spec[direction] = []any{map[string]any{field: []any{good, badPeer, good}}}
				}
				calls := 0
				q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
					calls++
					return []graph.Resource{{Kind: "Pod", Namespace: r.Namespace, Name: "confirmed", Labels: map[string]string{"app": "web"}}}, nil
				})
				edges, err := ext.Extract(context.Background(), r, q)
				if err == nil || calls != 1 || len(edges) != 1 || edges[0].From != r.ID() || edges[0].To != "demo/Pod/confirmed" || edges[0].Type != ext.Type() {
					t.Fatal("malformed later peer erased its prefix or started another read", edges, calls, err)
				}
			})
		}
	}
}
