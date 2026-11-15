// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"fmt"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestBuiltinSelectorMetadataPaths(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		for _, state := range []string{"match", "mismatch", "empty", "missing", "null", "wrong-type"} {
			for _, e := range []Extractor{SelectsExtractor{}, SelectsNPExtractor{}, AllowsFromExtractor{}, AllowsToExtractor{}} {
				t.Run(fmt.Sprintf("%s/%s/%s", kind, state, e.Type()), func(t *testing.T) {
					testSelectorMetadataPath(t, kind, state, e, false)
				})
			}
		}
	}
}

func TestBuiltinMetadataPathsPreserveEmptySelectors(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		for _, e := range []Extractor{SelectsExtractor{}, SelectsNPExtractor{}, AllowsFromExtractor{}, AllowsToExtractor{}} {
			t.Run(fmt.Sprintf("%s/%s", kind, e.Type()), func(t *testing.T) {
				testSelectorMetadataPath(t, kind, "missing", e, true)
			})
		}
	}
}

func testSelectorMetadataPath(t *testing.T, kind, state string, e Extractor, empty bool) {
	t.Helper()
	target := accountShapeResource(kind, map[string]any{})
	target.ClusterID = "east"
	decoyLabels := map[string]any{"app": "selected", "tier": ""}
	if state == "match" {
		decoyLabels["app"] = "other"
	}
	target.Raw["metadata"] = map[string]any{"labels": decoyLabels}
	target.Labels = map[string]string{"app": decoyLabels["app"].(string), "tier": ""}
	outer := target.Raw["spec"].(map[string]any)
	var canonical map[string]any
	switch kind {
	case "Pod":
		canonical = target.Raw
		outer["template"] = map[string]any{"metadata": map[string]any{"labels": decoyLabels}}
	case "CronJob":
		canonical = outer["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)
		outer["template"] = map[string]any{"metadata": map[string]any{"labels": decoyLabels}}
	default:
		canonical = outer["template"].(map[string]any)
	}
	if kind != "CronJob" {
		outer["jobTemplate"] = map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": decoyLabels}}}}
	}
	switch state {
	case "match", "mismatch":
		app := "selected"
		if state == "mismatch" {
			app = "other"
		}
		canonical["metadata"] = map[string]any{"labels": map[string]any{"app": app, "tier": ""}}
		if kind == "Pod" {
			target.Labels = map[string]string{"app": app, "tier": ""}
		}
	case "empty":
		canonical["metadata"] = map[string]any{}
	case "missing":
		delete(canonical, "metadata")
	case "null":
		canonical["metadata"] = nil
	case "wrong-type":
		canonical["metadata"] = true
	}
	if kind == "Pod" && state != "match" && state != "mismatch" {
		target.Labels = nil
	}
	selector := map[string]any{"app": "selected", "tier": ""}
	if empty {
		selector = map[string]any{}
	}
	source := graph.Resource{ClusterID: "east", Namespace: target.Namespace, Name: "source", Kind: "NetworkPolicy", Raw: map[string]any{"spec": map[string]any{
		"podSelector": map[string]any{"matchLabels": selector}, "policyTypes": []any{"Ingress", "Egress"},
		"ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": selector}}}}},
		"egress":  []any{map[string]any{"to": []any{map[string]any{"podSelector": map[string]any{"matchLabels": selector}}}}},
	}}}
	service := e.Type() == graph.EdgeTypeSelects
	if service {
		source.Kind = "Service"
		source.Raw = map[string]any{"spec": map[string]any{"selector": selector}}
	}
	reads := 0
	q := networkPolicyFailureLister(func(_ context.Context, filter graph.Filter) ([]graph.Resource, error) {
		reads++
		if filter.Namespace != source.Namespace || filter.ClusterID != source.ClusterID || filter.Kind != "" {
			t.Fatal("selector changed its existing namespace/member query", filter)
		}
		return []graph.Resource{target}, nil
	})
	edges, err := e.Extract(context.Background(), source, q)
	want := state == "match"
	wantReads := 1
	if empty {
		want = !service
		if service {
			wantReads = 0
		}
	}
	if err != nil || reads != wantReads || len(edges) != boolEdgeCount(want) {
		t.Fatal("selector used root/alternate metadata or changed empty-selector behavior", edges, want, reads, err)
	}
	if want && (edges[0].From != source.ID() || edges[0].To != target.ID() || edges[0].Type != e.Type()) {
		t.Fatal("selector changed canonical target identity or direction", edges)
	}
}

func boolEdgeCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
