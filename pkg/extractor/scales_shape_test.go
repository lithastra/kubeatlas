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
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestScalesReferenceShapes(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, tc := range []struct {
			name    string
			mutate  func(map[string]any, map[string]any, map[string]any)
			wantErr bool
		}{
			{"omitted-spec", func(raw, _, _ map[string]any) { delete(raw, "spec") }, false},
			{"null-spec", func(raw, _, _ map[string]any) { raw["spec"] = nil }, false},
			{"wrong-spec", func(raw, _, _ map[string]any) { raw["spec"] = "shape-canary" }, true},
			{"omitted-reference", func(_, spec, _ map[string]any) { delete(spec, "scaleTargetRef") }, false},
			{"null-reference", func(_, spec, _ map[string]any) { spec["scaleTargetRef"] = nil }, false},
			{"wrong-reference", func(_, spec, _ map[string]any) { spec["scaleTargetRef"] = []any{"shape-canary"} }, true},
			{"empty-reference", func(_, spec, _ map[string]any) { spec["scaleTargetRef"] = map[string]any{} }, true},
			{"omitted-kind", func(_, _, ref map[string]any) { delete(ref, "kind") }, true},
			{"null-kind", func(_, _, ref map[string]any) { ref["kind"] = nil }, true},
			{"empty-kind", func(_, _, ref map[string]any) { ref["kind"] = "" }, true},
			{"wrong-kind", func(_, _, ref map[string]any) { ref["kind"] = map[string]any{"private-key-canary": "shape-canary"} }, true},
			{"omitted-name", func(_, _, ref map[string]any) { delete(ref, "name") }, true},
			{"null-name", func(_, _, ref map[string]any) { ref["name"] = nil }, true},
			{"empty-name", func(_, _, ref map[string]any) { ref["name"] = "" }, true},
			{"wrong-name", func(_, _, ref map[string]any) { ref["name"] = true }, true},
		} {
			t.Run(fmt.Sprintf("cluster=%s/%s", cluster, tc.name), func(t *testing.T) {
				r := scalesShapeResource(cluster, "Deployment")
				spec := r.Raw["spec"].(map[string]any)
				ref := spec["scaleTargetRef"].(map[string]any)
				tc.mutate(r.Raw, spec, ref)
				checkScalesShape(t, r, tc.wantErr, false)
			})
		}
	}
}

func TestScalesAPIVersionParsing(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, kind := range []string{"Deployment", "StatefulSet", "ReplicationController", "Widget"} {
			for _, tc := range []struct {
				name    string
				value   any
				omitted bool
			}{
				{name: "omitted", omitted: true},
				{name: "null"},
				{name: "empty", value: ""},
				{name: "apps", value: "apps/v1"},
				{name: "core", value: "v1"},
				{name: "custom-group", value: "example.org/v1"},
				{name: "slash", value: "/"},
				{name: "empty-version", value: "apps/"},
				{name: "extra-slash", value: "version-canary/v1/extra"},
				{name: "boolean", value: true},
				{name: "array", value: []any{"version-canary"}},
				{name: "object", value: map[string]any{"private-key-canary": "version-canary"}},
			} {
				t.Run(fmt.Sprintf("cluster=%s/%s/%s", cluster, kind, tc.name), func(t *testing.T) {
					r := scalesShapeResource(cluster, kind)
					ref := r.Raw["spec"].(map[string]any)["scaleTargetRef"].(map[string]any)
					if tc.omitted {
						delete(ref, "apiVersion")
					} else {
						ref["apiVersion"] = tc.value
					}
					version, isString := tc.value.(string)
					_, parseErr := schema.ParseGroupVersion(version)
					wantErr := tc.value != nil && !isString || parseErr != nil
					checkScalesShape(t, r, wantErr, !wantErr)
				})
			}
		}
	}
}

func scalesShapeResource(cluster, kind string) graph.Resource {
	return graph.Resource{ClusterID: cluster, Kind: "HorizontalPodAutoscaler", Namespace: "app", Name: "autoscaler",
		OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "manager", UID: "owner-uid"}},
		Raw:             map[string]any{"spec": map[string]any{"scaleTargetRef": map[string]any{"kind": kind, "name": "worker", "apiVersion": "apps/v1"}}}}
}

func checkScalesShape(t *testing.T, r graph.Resource, wantErr, wantEdge bool) {
	t.Helper()
	q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
		t.Fatal("HPA reference extraction must not query targets or scale subresources")
		return nil, nil
	})
	edges, err := (ScalesExtractor{}).Extract(context.Background(), r, q)
	var want []graph.Edge
	if wantEdge {
		kind := r.Raw["spec"].(map[string]any)["scaleTargetRef"].(map[string]any)["kind"].(string)
		want = []graph.Edge{{From: r.ID(), To: (graph.Resource{ClusterID: r.ClusterID, Kind: kind, Namespace: r.Namespace, Name: "worker"}).ID(), Type: graph.EdgeTypeScales}}
	}
	if (err != nil) != wantErr || !reflect.DeepEqual(edges, want) {
		t.Fatal("HPA reference shape/parse handling lost an error or changed a valid target", edges, want, err)
	}
	if err != nil && (strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "private-key")) {
		t.Fatal("HPA reference error included input data", err)
	}
	registry := New()
	registry.Register(ScalesExtractor{})
	registry.Register(OwnsExtractor{})
	batch, batchErr := registry.ExtractAll(context.Background(), r, q)
	want = append(want, graph.Edge{From: r.ID(), To: (graph.Resource{ClusterID: r.ClusterID, Kind: "Deployment", Namespace: r.Namespace, Name: "manager"}).ID(), Type: graph.EdgeTypeOwns})
	if (batchErr != nil) != wantErr || !reflect.DeepEqual(batch, want) {
		t.Fatal("HPA reference failure suppressed independent owner extraction", batch, want, batchErr)
	}
}
