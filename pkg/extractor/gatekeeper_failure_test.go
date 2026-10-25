// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

type gatekeeperFailureLister func(context.Context, graph.Filter) ([]graph.Resource, error)

func (f gatekeeperFailureLister) ListResources(ctx context.Context, filter graph.Filter) ([]graph.Resource, error) {
	return f(ctx, filter)
}

func TestGatekeeper_TargetReadFailureKeepsEarlierMatches(t *testing.T) {
	for _, scenario := range []string{"later-kind", "later-namespace", "first-query"} {
		t.Run(scenario, func(t *testing.T) {
			kinds, namespaces := []any{"Pod", "Deployment"}, []any{"demo"}
			if scenario == "later-namespace" {
				kinds, namespaces = []any{"Pod"}, []any{"demo", "other"}
			}
			resource := gkConstraint("policy", map[string]any{
				"kinds": []any{map[string]any{"kinds": kinds}}, "namespaces": namespaces,
			}, []any{map[string]any{"kind": "Pod", "namespace": "demo", "name": "matched", "message": "missing label"}})
			failure := errors.New("target read failed")
			calls := 0
			q := gatekeeperFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
				calls++
				if scenario == "first-query" || calls == 2 {
					return []graph.Resource{{Kind: f.Kind, Namespace: f.Namespace, Name: "failed-read-canary"}}, failure
				}
				return []graph.Resource{{Kind: f.Kind, Namespace: f.Namespace, Name: "matched"}}, nil
			})
			edges, err := (GatekeeperExtractor{}).Extract(context.Background(), resource, q)
			wantEdges, wantCalls := 1, 2
			if scenario == "first-query" {
				wantEdges, wantCalls = 0, 1
			}
			if !errors.Is(err, failure) || len(edges) != wantEdges || calls != wantCalls {
				t.Fatalf("lost usable evidence or retried failed read: %+v calls=%d err=%v", edges, calls, err)
			}
			if wantEdges != 0 && (edges[0].To != "demo/Pod/matched" || edges[0].Attributes["violation_message"] != "missing label") {
				t.Fatal("partial failure lost the confirmed violation observation", edges)
			}
		})
	}
}

func TestGatekeeper_CancellationStopsTargetReads(t *testing.T) {
	for _, cancelAt := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("cancelAt=%d", cancelAt), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelAt == 0 {
				cancel()
			}
			resource := gkConstraint("policy", map[string]any{"kinds": []any{map[string]any{"kinds": []any{"Pod", "Deployment", "Service"}}}}, nil)
			calls := 0
			q := gatekeeperFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
				calls++
				if calls == cancelAt {
					cancel()
				}
				// Deliberately return rows after cancellation.
				return []graph.Resource{{Kind: f.Kind, Namespace: "demo", Name: "matched"}}, nil
			})
			edges, err := (GatekeeperExtractor{}).Extract(ctx, resource, q)
			if !errors.Is(err, context.Canceled) || len(edges) != 0 || calls != cancelAt {
				t.Fatalf("cancelled extraction continued: %+v calls=%d err=%v", edges, calls, err)
			}
		})
	}
}

func TestGatekeeper_RegistryKeepsOtherEdgesAfterTargetFailure(t *testing.T) {
	resource := gkConstraint("policy", map[string]any{"kinds": []any{map[string]any{"kinds": []any{"Pod", "Deployment"}}}}, nil)
	resource.OwnerReferences = []graph.OwnerRef{{Kind: "ConfigMap", Name: "owner"}}
	failure := errors.New("deployment read failed")
	q := gatekeeperFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
		if f.Kind == "Deployment" {
			return nil, failure
		}
		return []graph.Resource{{Kind: "Pod", Namespace: "demo", Name: "matched"}}, nil
	})
	registry := New()
	registry.Register(GatekeeperExtractor{})
	registry.Register(OwnsExtractor{})
	edges, err := registry.ExtractAll(context.Background(), resource, q)
	if !errors.Is(err, failure) || len(edges) != 2 || edges[0].Type != graph.EdgeTypeEnforces || edges[1].Type != graph.EdgeTypeOwns {
		t.Fatalf("registry discarded earlier or subsequent extraction: %+v %v", edges, err)
	}
}
