// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestAKSIdentity_NonSAResourceIsNoop(t *testing.T) {
	got, err := AKSIdentityExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "Pod", Name: "x", Namespace: "ns1",
			Labels: map[string]string{aksWorkloadIdentityLabel: "deadbeef-1234"}},
		memory.New())
	if err != nil || len(got) != 0 {
		t.Errorf("non-SA with AKS label must emit no edges (err=%v, edges=%+v)", err, got)
	}
}

func TestAKSIdentity_SAWithoutLabelIsNoop(t *testing.T) {
	got, _ := AKSIdentityExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		memory.New())
	if len(got) != 0 {
		t.Errorf("SA without AKS label must emit no edges, got %+v", got)
	}
}

func TestAKSIdentity_SAWithLabelEmitsEdge(t *testing.T) {
	clientID := "9d8c1a2b-3e4f-5a6b-7c8d-9e0f1a2b3c4d"
	sa := graph.Resource{
		Kind: "ServiceAccount", Name: "payments", Namespace: "ns1",
		Labels: map[string]string{aksWorkloadIdentityLabel: clientID},
	}
	got, err := AKSIdentityExtractor{}.Extract(context.Background(), sa, memory.New())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 edge, got %d (%+v)", len(got), got)
	}
	e := got[0]
	if e.Type != graph.EdgeTypeBindsPlatformIdentity {
		t.Errorf("Type = %q, want BINDS_PLATFORM_IDENTITY", e.Type)
	}
	if e.From != sa.ID() {
		t.Errorf("From = %q, want %q", e.From, sa.ID())
	}
	if want := "_external/AADManagedIdentity/" + clientID; e.To != want {
		t.Errorf("To = %q, want %q", e.To, want)
	}
}

func TestAKSIdentity_MultiClusterScopesByCluster(t *testing.T) {
	clientID := "shared-client-id"
	prodSA := graph.Resource{
		Kind: "ServiceAccount", Name: "payments", Namespace: "ns1", ClusterID: "prod",
		Labels: map[string]string{aksWorkloadIdentityLabel: clientID},
	}
	prod, _ := AKSIdentityExtractor{}.Extract(context.Background(), prodSA, memory.New())
	if len(prod) != 1 || prod[0].To != "prod:_external/AADManagedIdentity/"+clientID {
		t.Errorf("multi-cluster To = %q", prod[0].To)
	}
}
