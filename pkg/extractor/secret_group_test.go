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
)

func TestGatewayCertificateGroupIdentity(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, explicitKind := range []bool{false, true} {
			for _, tc := range []struct {
				name, group   string
				omitted, want bool
			}{
				{name: "omitted", omitted: true, want: true},
				{name: "core", group: "", want: true},
				{name: "core-alias", group: "core"},
				{name: "foreign", group: "example.org"},
				{name: "gateway-group", group: "gateway.networking.k8s.io"},
			} {
				for _, remote := range []bool{false, true} {
					t.Run(fmt.Sprintf("cluster=%s/explicit-kind=%t/%s/remote=%t", cluster, explicitKind, tc.name, remote), func(t *testing.T) {
						ref := map[string]any{"name": "certificate"}
						if explicitKind {
							ref["kind"] = "Secret"
						}
						if !tc.omitted {
							ref["group"] = tc.group
						}
						namespace := "demo"
						if remote {
							namespace, ref["namespace"] = "certs", "certs"
						}
						r := graph.Resource{ClusterID: cluster, Kind: "Gateway", Namespace: "demo", Name: "gateway", Raw: map[string]any{"spec": map[string]any{"listeners": []any{map[string]any{"tls": map[string]any{"certificateRefs": []any{ref}}}}}}}
						q := networkPolicyFailureLister(func(context.Context, graph.Filter) ([]graph.Resource, error) {
							t.Fatal("certificate extraction must not query Secret objects")
							return nil, nil
						})
						edges, err := (SecretExtractor{}).Extract(context.Background(), r, q)
						var want []graph.Edge
						if tc.want {
							want = []graph.Edge{{From: r.ID(), To: graph.SecretReferenceResource(namespace, "certificate", cluster).ID(), Type: graph.EdgeTypeUsesSecret}}
						}
						if err != nil || !reflect.DeepEqual(edges, want) {
							t.Fatal("certificate group was conflated with the core Secret group", edges, want, err)
						}
					})
				}
			}
		}
	}
}

func TestGatewayCertificateGroupsDoNotPoisonDeduplication(t *testing.T) {
	for _, rejectedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected-first=%t", rejectedFirst), func(t *testing.T) {
			valid := []any{map[string]any{"name": "shared"}, map[string]any{"name": "shared", "kind": "Secret", "group": ""}}
			foreign := []any{map[string]any{"name": "shared", "group": "example.org"}, map[string]any{"name": "other", "group": "core"}, map[string]any{"name": "config", "kind": "ConfigMap", "group": ""}}
			first, second := valid, foreign
			if rejectedFirst {
				first, second = second, first
			}
			r := graph.Resource{Kind: "Gateway", Namespace: "demo", Name: "gateway", Raw: map[string]any{"spec": map[string]any{"listeners": []any{map[string]any{"tls": map[string]any{"certificateRefs": first}}, map[string]any{"tls": map[string]any{"certificateRefs": second}}}}}}
			edges, err := (SecretExtractor{}).Extract(context.Background(), r, nil)
			if err != nil || len(edges) != 1 || edges[0].From != r.ID() || edges[0].To != "demo/Secret/shared" || edges[0].Type != graph.EdgeTypeUsesSecret {
				t.Fatal("certificate group filtering changed valid cross-listener deduplication", edges, err)
			}
		})
	}
}

func TestGatewayMalformedCertificateGroupKeepsPrefixAndIndependentEdges(t *testing.T) {
	for _, group := range []struct {
		name  string
		value any
	}{{"null", nil}, {"boolean", true}, {"array", []any{"certificate-group-canary"}}, {"object", map[string]any{"private-key-canary": "certificate-group-canary"}}} {
		for _, laterListener := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/later-listener=%t", group.name, laterListener), func(t *testing.T) {
				first := map[string]any{"name": "confirmed"}
				bad := map[string]any{"name": "invalid", "group": group.value}
				later := map[string]any{"name": "later"}
				listeners := []any{map[string]any{"tls": map[string]any{"certificateRefs": []any{first, bad, later}}}}
				if laterListener {
					listeners = []any{map[string]any{"tls": map[string]any{"certificateRefs": []any{first}}}, map[string]any{"tls": map[string]any{"certificateRefs": []any{bad, later}}}}
				}
				r := graph.Resource{Kind: "Gateway", Namespace: "demo", Name: "gateway", OwnerReferences: []graph.OwnerRef{{Kind: "ConfigMap", Name: "controller"}}, Raw: map[string]any{"spec": map[string]any{"listeners": listeners}}}
				registry := New()
				registry.Register(SecretExtractor{})
				registry.Register(OwnsExtractor{})
				edges, err := registry.ExtractAll(context.Background(), r, nil)
				want := []graph.Edge{{From: r.ID(), To: "demo/Secret/confirmed", Type: graph.EdgeTypeUsesSecret}, {From: r.ID(), To: "demo/ConfigMap/controller", Type: graph.EdgeTypeOwns}}
				if err == nil || !reflect.DeepEqual(edges, want) {
					t.Fatal("malformed certificate group became a default or erased usable edges", edges, err)
				}
				if strings.Contains(err.Error(), "certificate-group-canary") || strings.Contains(err.Error(), "private-key-canary") {
					t.Fatal("certificate group error included input data", err)
				}
			})
		}
	}
}
