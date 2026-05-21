// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestGKEIdentity_NonSAResourceIsNoop(t *testing.T) {
	got, _ := GKEIdentityExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "Pod", Name: "x", Namespace: "ns1",
			Annotations: map[string]string{gkeWorkloadIdentityAnnotation: "svc@proj.iam.gserviceaccount.com"}},
		memory.New())
	if len(got) != 0 {
		t.Errorf("non-SA with GKE annotation must emit no edges, got %+v", got)
	}
}

func TestGKEIdentity_SAWithoutAnnotationIsNoop(t *testing.T) {
	got, _ := GKEIdentityExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		memory.New())
	if len(got) != 0 {
		t.Errorf("SA without GKE annotation must emit no edges, got %+v", got)
	}
}

func TestGKEIdentity_SAWithAnnotationEmitsEdge(t *testing.T) {
	gsa := "payments-sa@my-project.iam.gserviceaccount.com"
	sa := graph.Resource{
		Kind: "ServiceAccount", Name: "payments", Namespace: "ns1",
		Annotations: map[string]string{gkeWorkloadIdentityAnnotation: gsa},
	}
	got, err := GKEIdentityExtractor{}.Extract(context.Background(), sa, memory.New())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 edge, got %d", len(got))
	}
	e := got[0]
	if e.Type != graph.EdgeTypeBindsPlatformIdentity {
		t.Errorf("Type = %q, want BINDS_PLATFORM_IDENTITY", e.Type)
	}
	if want := "_external/GCPServiceAccount/" + gsa; e.To != want {
		t.Errorf("To = %q, want %q", e.To, want)
	}
}

func TestGKEIdentity_MultiClusterScopesByCluster(t *testing.T) {
	gsa := "shared@proj.iam.gserviceaccount.com"
	prodSA := graph.Resource{
		Kind: "ServiceAccount", Name: "payments", Namespace: "ns1", ClusterID: "prod",
		Annotations: map[string]string{gkeWorkloadIdentityAnnotation: gsa},
	}
	got, _ := GKEIdentityExtractor{}.Extract(context.Background(), prodSA, memory.New())
	if len(got) != 1 || got[0].To != "prod:_external/GCPServiceAccount/"+gsa {
		t.Errorf("multi-cluster To = %q", got[0].To)
	}
}
