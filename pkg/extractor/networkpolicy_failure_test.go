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

type networkPolicyFailureLister func(context.Context, graph.Filter) ([]graph.Resource, error)

func (f networkPolicyFailureLister) ListResources(ctx context.Context, filter graph.Filter) ([]graph.Resource, error) {
	return f(ctx, filter)
}

func networkPolicyFailureFixture(direction, shape string) (graph.Resource, Extractor, sliceLister) {
	podPeer := map[string]any{"podSelector": map[string]any{}}
	nsPeer := map[string]any{"namespaceSelector": map[string]any{}}
	bothPeer := map[string]any{"podSelector": map[string]any{}, "namespaceSelector": map[string]any{}}
	field, peerField, policyType := "ingress", "from", "Ingress"
	var ext Extractor = AllowsFromExtractor{}
	if direction == "egress" {
		field, peerField, policyType, ext = "egress", "to", "Egress", AllowsToExtractor{}
	}
	rules := []any{map[string]any{peerField: []any{bothPeer}}}
	switch shape {
	case "later-peer", "first-query":
		rules = []any{map[string]any{peerField: []any{podPeer, nsPeer, podPeer}}}
	case "later-rule":
		rules = []any{map[string]any{peerField: []any{podPeer}}, map[string]any{peerField: []any{nsPeer}}, map[string]any{peerField: []any{podPeer}}}
	case "nested-prefix":
		rules = []any{map[string]any{peerField: []any{podPeer, bothPeer}}}
	case "pod-only":
		rules = []any{map[string]any{peerField: []any{podPeer}}}
	case "namespace-only":
		rules = []any{map[string]any{peerField: []any{nsPeer}}}
	}
	r := makeNP("demo", "policy", map[string]any{"policyTypes": []any{policyType}, "podSelector": map[string]any{}, field: rules})
	rows := sliceLister{
		{Kind: "Namespace", Name: "alpha"}, {Kind: "Namespace", Name: "beta"}, {Kind: "Namespace", Name: "gamma"},
		{Kind: "Pod", Namespace: "demo", Name: "local"}, {Kind: "Pod", Namespace: "alpha", Name: "a"},
		{Kind: "Pod", Namespace: "beta", Name: "b"}, {Kind: "Pod", Namespace: "gamma", Name: "c"},
	}
	return r, ext, rows
}

func TestNetworkPolicy_LaterReadFailureKeepsEarlierEdges(t *testing.T) {
	for _, direction := range []string{"ingress", "egress"} {
		for _, shape := range []string{"later-peer", "later-rule", "later-namespace", "nested-prefix", "first-query"} {
			t.Run(direction+"/"+shape, func(t *testing.T) {
				r, ext, rows := networkPolicyFailureFixture(direction, shape)
				failure := errors.New("network policy lookup failed")
				calls := 0
				q := networkPolicyFailureLister(func(ctx context.Context, f graph.Filter) ([]graph.Resource, error) {
					calls++
					failed := f.Namespace == "beta"
					if shape == "later-peer" || shape == "later-rule" {
						failed = f.Kind == "Namespace"
					}
					if shape == "first-query" {
						failed = true
					}
					found, err := rows.ListResources(ctx, f)
					if failed {
						return found, failure // Failed-query partial rows cannot become edges.
					}
					return found, err
				})
				edges, err := ext.Extract(context.Background(), r, q)
				var want []string
				wantCalls := 1
				switch shape {
				case "later-peer", "later-rule":
					want, wantCalls = []string{"demo/Pod/local"}, 2
				case "later-namespace":
					want, wantCalls = []string{"alpha/Pod/a"}, 3
				case "nested-prefix":
					want, wantCalls = []string{"demo/Pod/local", "alpha/Pod/a"}, 4
				}
				var got []string
				for _, edge := range edges {
					if edge.From != r.ID() || edge.Type != ext.Type() {
						t.Fatal("partial result changed source or direction", edge)
					}
					got = append(got, edge.To)
				}
				if !errors.Is(err, failure) || calls != wantCalls || !reflect.DeepEqual(got, want) {
					t.Fatalf("lost partial edges or continued failed lookup: %v calls=%d err=%v", got, calls, err)
				}
			})
		}
	}
}

func TestNetworkPolicy_CancellationStopsFurtherReads(t *testing.T) {
	for _, direction := range []string{"ingress", "egress"} {
		for _, shape := range []string{"pod-only", "namespace-only", "later-namespace"} {
			stages := []int{0, 1}
			if shape == "later-namespace" {
				stages = append(stages, 2, 3)
			}
			for _, cancelAt := range stages {
				t.Run(fmt.Sprintf("%s/%s/cancelAt=%d", direction, shape, cancelAt), func(t *testing.T) {
					r, ext, rows := networkPolicyFailureFixture(direction, shape)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if cancelAt == 0 {
						cancel()
					}
					calls := 0
					q := networkPolicyFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
						calls++
						if calls == cancelAt {
							cancel()
						}
						return rows.ListResources(context.Background(), f)
					})
					edges, err := ext.Extract(ctx, r, q)
					if !errors.Is(err, context.Canceled) || len(edges) != 0 || calls != cancelAt {
						t.Fatalf("cancelled policy continued: %+v calls=%d err=%v", edges, calls, err)
					}
				})
			}
		}
	}
}

func TestNetworkPolicy_SelectsCancellationDiscardsLateRows(t *testing.T) {
	for _, cancelAt := range []int{0, 1} {
		t.Run(fmt.Sprintf("cancelAt=%d", cancelAt), func(t *testing.T) {
			r, _, rows := networkPolicyFailureFixture("ingress", "pod-only")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelAt == 0 {
				cancel()
			}
			calls := 0
			q := networkPolicyFailureLister(func(_ context.Context, f graph.Filter) ([]graph.Resource, error) {
				calls++
				cancel()
				return rows.ListResources(context.Background(), f)
			})
			edges, err := (SelectsNPExtractor{}).Extract(ctx, r, q)
			if !errors.Is(err, context.Canceled) || len(edges) != 0 || calls != cancelAt {
				t.Fatalf("cancelled selection accepted rows: %+v calls=%d err=%v", edges, calls, err)
			}
		})
	}
}

func TestNetworkPolicy_RegistryKeepsIndependentAndPartialEdges(t *testing.T) {
	for _, direction := range []string{"ingress", "egress"} {
		t.Run(direction, func(t *testing.T) {
			r, ext, rows := networkPolicyFailureFixture(direction, "later-namespace")
			r.OwnerReferences = []graph.OwnerRef{{Kind: "ConfigMap", Name: "owner"}}
			failure := errors.New("beta lookup failed")
			q := networkPolicyFailureLister(func(ctx context.Context, f graph.Filter) ([]graph.Resource, error) {
				if f.Namespace == "beta" {
					return nil, failure
				}
				return rows.ListResources(ctx, f)
			})
			reg := New()
			reg.Register(SelectsNPExtractor{})
			reg.Register(ext)
			reg.Register(OwnsExtractor{})
			edges, err := reg.ExtractAll(context.Background(), r, q)
			if !errors.Is(err, failure) || len(edges) != 3 || edges[0].Type != graph.EdgeTypeSelectsNP || edges[1].Type != ext.Type() || edges[2].Type != graph.EdgeTypeOwns {
				t.Fatalf("registry lost independent or partial results: %+v %v", edges, err)
			}
		})
	}
}
