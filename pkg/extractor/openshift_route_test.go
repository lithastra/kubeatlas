// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// routeResource builds an OpenShift Route fixture. ns is the route's
// namespace; the first to (kind/name) is spec.to; alts are
// spec.alternateBackends[].
func routeResource(ns, name string, toKind, toName string, alts ...[2]string) graph.Resource {
	spec := map[string]any{
		"to": map[string]any{"kind": toKind, "name": toName},
	}
	if len(alts) > 0 {
		var arr []any
		for _, a := range alts {
			arr = append(arr, map[string]any{"kind": a[0], "name": a[1]})
		}
		spec["alternateBackends"] = arr
	}
	return graph.Resource{
		Kind: "Route", Name: name, Namespace: ns,
		Raw: map[string]any{"spec": spec},
	}
}

func TestOpenShiftRoute_PrimaryToServiceEdge(t *testing.T) {
	r := routeResource("petclinic", "api", "Service", "api-svc")
	got, err := RoutesExtractor{}.Extract(context.Background(), r, memory.New())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 1 || got[0].To != "petclinic/Service/api-svc" {
		t.Errorf("got %+v, want one edge to petclinic/Service/api-svc", got)
	}
	if got[0].Type != graph.EdgeTypeRoutesTo {
		t.Errorf("Type = %q, want ROUTES_TO", got[0].Type)
	}
}

func TestOpenShiftRoute_AlternateBackendsEdges(t *testing.T) {
	r := routeResource("petclinic", "api",
		"Service", "api-v1",
		[2]string{"Service", "api-v2"},
		[2]string{"Service", "api-canary"},
	)
	got, _ := RoutesExtractor{}.Extract(context.Background(), r, memory.New())
	if len(got) != 3 {
		t.Fatalf("want 3 edges (primary + 2 alt), got %d", len(got))
	}
}

func TestOpenShiftRoute_DefaultKindIsService(t *testing.T) {
	r := graph.Resource{
		Kind: "Route", Name: "api", Namespace: "petclinic",
		Raw: map[string]any{
			"spec": map[string]any{
				"to": map[string]any{"name": "api-svc"}, // no kind
			},
		},
	}
	got, _ := RoutesExtractor{}.Extract(context.Background(), r, memory.New())
	if len(got) != 1 || got[0].To != "petclinic/Service/api-svc" {
		t.Errorf("default kind should be Service: got %+v", got)
	}
}

func TestOpenShiftRoute_DuplicatesCollapsed(t *testing.T) {
	r := routeResource("petclinic", "api",
		"Service", "api-svc",
		[2]string{"Service", "api-svc"}, // duplicate of primary
	)
	got, _ := RoutesExtractor{}.Extract(context.Background(), r, memory.New())
	if len(got) != 1 {
		t.Errorf("want 1 deduped edge, got %d (%+v)", len(got), got)
	}
}

func TestOpenShiftRoute_EmptyNameNoEdge(t *testing.T) {
	r := graph.Resource{
		Kind: "Route", Name: "api", Namespace: "petclinic",
		Raw: map[string]any{
			"spec": map[string]any{"to": map[string]any{"kind": "Service"}}, // no name
		},
	}
	got, _ := RoutesExtractor{}.Extract(context.Background(), r, memory.New())
	if len(got) != 0 {
		t.Errorf("missing target name must emit no edge, got %+v", got)
	}
}

func TestOpenShiftRoute_MultiClusterEdgeKeepsClusterPrefix(t *testing.T) {
	r := routeResource("petclinic", "api", "Service", "api-svc")
	r.ClusterID = "prod"
	got, _ := RoutesExtractor{}.Extract(context.Background(), r, memory.New())
	if len(got) != 1 {
		t.Fatalf("want 1 edge, got %d", len(got))
	}
	if got[0].From != "prod:petclinic/Route/api" {
		t.Errorf("From = %q, want prod-prefixed", got[0].From)
	}
	if got[0].To != "prod:petclinic/Service/api-svc" {
		t.Errorf("To = %q, want prod-prefixed Service id", got[0].To)
	}
}

func TestOpenShiftRoute_NonRouteKindNoop(t *testing.T) {
	// Sanity: the RoutesExtractor must not match non-route kinds.
	got, _ := RoutesExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "ConfigMap", Name: "cfg", Namespace: "petclinic"},
		memory.New())
	if len(got) != 0 {
		t.Errorf("ConfigMap should not produce ROUTES_TO edges, got %+v", got)
	}
}
