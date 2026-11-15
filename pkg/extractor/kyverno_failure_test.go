// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

type kyvernoFailureLister func(context.Context, graph.Filter) ([]graph.Resource, error)

func (f kyvernoFailureLister) ListResources(ctx context.Context, filter graph.Filter) ([]graph.Resource, error) {
	return f(ctx, filter)
}

func TestKyverno_ReportReadFailurePreservesUsableEdges(t *testing.T) {
	for _, failed := range []string{"PolicyReport", "ClusterPolicyReport", "both"} {
		t.Run(failed, func(t *testing.T) {
			failure := errors.New("report store unavailable")
			pol := kyvernoPolicy("ClusterPolicy", "policy", "", []any{validateRule([]any{"Pod"}, []any{"demo"})})
			target := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "web"}
			var calls []string
			q := kyvernoFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
				calls = append(calls, f.Kind)
				if f.Kind == "Pod" {
					return []graph.Resource{target}, nil
				}
				report := policyReport("demo", []any{reportResult("policy", "pass", map[string]any{"kind": "Pod", "namespace": "demo", "name": "web"})})
				if failed == "both" || f.Kind == failed {
					// A partial response accompanying an error must not become evidence.
					report.Raw = map[string]any{"results": []any{reportResult("policy", "failed-read-canary", map[string]any{"kind": "Pod", "namespace": "demo", "name": "web"})}}
					return []graph.Resource{report}, failure
				}
				return []graph.Resource{report}, nil
			})
			edges, err := (KyvernoExtractor{}).Extract(context.Background(), pol, q)
			if !errors.Is(err, failure) || len(edges) != 1 || edges[0].To != target.ID() {
				t.Fatalf("report failure lost edges or qualification: %+v %v", edges, err)
			}
			wantResult := "pass"
			if failed == "both" {
				wantResult = ""
			}
			if edges[0].Attributes["result"] != wantResult {
				t.Fatal("failed response supplied report evidence", edges)
			}
			if !reflect.DeepEqual(calls, []string{"PolicyReport", "ClusterPolicyReport", "Pod"}) {
				t.Fatal("unexpected retry or missing healthy lookup", calls)
			}
		})
	}
}

func TestKyverno_TargetReadFailurePreservesEarlierMatches(t *testing.T) {
	for _, reportFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("reportFailure=%t", reportFails), func(t *testing.T) {
			reportErr, targetErr := errors.New("report failure"), errors.New("target failure")
			pol := kyvernoPolicy("ClusterPolicy", "policy", "", []any{validateRule([]any{"Pod", "Deployment"}, []any{"demo"})})
			q := kyvernoFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
				switch f.Kind {
				case "PolicyReport":
					if reportFails {
						return nil, reportErr
					}
				case "Pod":
					return []graph.Resource{{Kind: "Pod", Namespace: "demo", Name: "web"}}, nil
				case "Deployment":
					return []graph.Resource{{Kind: "Deployment", Namespace: "demo", Name: "failed-read-canary"}}, targetErr
				}
				return nil, nil
			})
			edges, err := (KyvernoExtractor{}).Extract(context.Background(), pol, q)
			if !errors.Is(err, targetErr) || errors.Is(err, reportErr) != reportFails || len(edges) != 1 || edges[0].To != "demo/Pod/web" {
				t.Fatalf("partial target failure lost earlier evidence or error: %+v %v", edges, err)
			}
		})
	}
}

func TestKyverno_CancellationStopsFurtherReads(t *testing.T) {
	for _, cancelAt := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprintf("cancelAt=%d", cancelAt), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelAt == 0 {
				cancel()
			}
			pol := kyvernoPolicy("ClusterPolicy", "policy", "", []any{validateRule([]any{"Pod", "Deployment"}, []any{"demo"})})
			calls := 0
			q := kyvernoFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
				calls++
				if calls == cancelAt {
					cancel() // A context-ignoring dependency can still return rows.
				}
				return []graph.Resource{{Kind: f.Kind, Namespace: "demo", Name: "web"}}, nil
			})
			edges, err := (KyvernoExtractor{}).Extract(ctx, pol, q)
			if !errors.Is(err, context.Canceled) || len(edges) != 0 || calls != cancelAt {
				t.Fatalf("cancelled extraction continued: edges=%+v calls=%d err=%v", edges, calls, err)
			}
		})
	}
}

func TestKyverno_RegistryKeepsOtherExtractorEdgesOnReportFailure(t *testing.T) {
	pol := kyvernoPolicy("ClusterPolicy", "policy", "", []any{validateRule([]any{"Pod"}, []any{"demo"})})
	pol.OwnerReferences = []graph.OwnerRef{{Kind: "ConfigMap", Name: "owner"}}
	failure := errors.New("report read failure")
	q := kyvernoFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
		if f.Kind == "Pod" {
			return []graph.Resource{{Kind: "Pod", Namespace: "demo", Name: "web"}}, nil
		}
		return nil, failure
	})
	registry := New()
	registry.Register(KyvernoExtractor{})
	registry.Register(OwnsExtractor{}) // The registry must continue after the error.
	edges, err := registry.ExtractAll(context.Background(), pol, q)
	if !errors.Is(err, failure) || len(edges) != 2 || edges[0].Type != graph.EdgeTypeEnforces || edges[1].Type != graph.EdgeTypeOwns {
		t.Fatalf("registry discarded partial or subsequent extraction: %+v %v", edges, err)
	}
}
