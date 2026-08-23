// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type secretEdgeExtractor struct{}

func (secretEdgeExtractor) ExtractAll(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	return []graph.Edge{{
		From: r.ID(),
		To:   graph.Resource{Kind: "Secret", Namespace: r.Namespace, Name: "database", ClusterID: r.ClusterID}.ID(),
		Type: graph.EdgeTypeUsesSecret,
	}}, nil
}

func TestUnstructuredToResource_ReducesSecretToReferenceOnly(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"namespace": "demo",
			"name":      "database",
			"labels":    map[string]any{"team": "platform"},
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"password":"canary"}}`,
			},
		},
		"data":       map[string]any{"password": "canary"},
		"stringData": map[string]any{"token": "canary"},
	}}

	got := UnstructuredToResource(u, "Secret")
	if got.Raw != nil || got.Labels != nil || got.OwnerReferences != nil || got.UID != "" || got.ResourceVersion != "" {
		t.Fatalf("Secret retained discovered metadata or payload: %+v", got)
	}
	if got.Annotations[graph.ReferenceOnlyAnnotation] != "true" || len(got.Annotations) != 1 {
		t.Fatalf("annotations = %v, want only reference-only marker", got.Annotations)
	}
}

func TestHandleUpsert_MaterialisesReferencedSecretPlaceholder(t *testing.T) {
	store := memory.New()
	mgr := NewInformerManager(nil, store, WithExtractor(secretEdgeExtractor{}))
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace": "demo",
			"name":      "api",
		},
	}}
	mgr.handleUpsert(context.Background(), schema.GroupVersionResource{
		Group: "apps", Version: "v1", Resource: "deployments",
	}, u, graph.EventTypeAdd)

	placeholder, err := store.GetResource(context.Background(), "demo/Secret/database")
	if err != nil {
		t.Fatalf("referenced Secret placeholder was not stored: %v", err)
	}
	if placeholder.Annotations[graph.ReferenceOnlyAnnotation] != "true" || placeholder.Raw != nil {
		t.Errorf("placeholder = %+v, want reference-only Secret without Raw", placeholder)
	}
	edges, err := store.ListEdges(context.Background(), "demo/Deployment/api", graph.DirectionOutgoing)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].To != placeholder.ID() || edges[0].Type != graph.EdgeTypeUsesSecret {
		t.Fatalf("edges = %v, want one USES_SECRET edge to placeholder", edges)
	}
}

func TestSecretGVRIsAlwaysSkipped(t *testing.T) {
	tests := []struct {
		name string
		gvr  schema.GroupVersionResource
		want bool
	}{
		{
			name: "core v1 Secret",
			gvr:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
			want: true,
		},
		{
			name: "core future-version Secret",
			gvr:  schema.GroupVersionResource{Group: "", Version: "v2", Resource: "secrets"},
			want: true,
		},
		{
			name: "same resource name outside core group",
			gvr:  schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "secrets"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSkipped(tt.gvr); got != tt.want {
				t.Fatalf("isSkipped(%s) = %v, want %v", tt.gvr, got, tt.want)
			}
		})
	}
}
