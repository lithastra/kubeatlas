// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestEKSIdentity_NonSAResourceIsNoop(t *testing.T) {
	got, err := EKSIdentityExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "Pod", Name: "x", Namespace: "ns1",
			Annotations: map[string]string{irsaAnnotation: "arn:aws:iam::1:role/foo"}},
		memory.New())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Pod with IRSA annotation must emit no edges, got %+v", got)
	}
}

func TestEKSIdentity_SAWithoutAnnotationIsNoop(t *testing.T) {
	got, err := EKSIdentityExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		memory.New())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SA without IRSA annotation must emit no edges, got %+v", got)
	}
}

func TestEKSIdentity_SAWithAnnotationEmitsEdge(t *testing.T) {
	arn := "arn:aws:iam::123456789012:role/eks-irsa-payments"
	sa := graph.Resource{
		Kind: "ServiceAccount", Name: "payments", Namespace: "ns1",
		Annotations: map[string]string{irsaAnnotation: arn},
	}
	got, err := EKSIdentityExtractor{}.Extract(context.Background(), sa, memory.New())
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
	wantTo := "_external/IAMRole/" + arn
	if e.To != wantTo {
		t.Errorf("To = %q, want %q", e.To, wantTo)
	}
}

func TestEKSIdentity_MultiClusterIDsKeepARNsScoped(t *testing.T) {
	arn := "arn:aws:iam::1:role/shared"
	prodSA := graph.Resource{
		Kind: "ServiceAccount", Name: "payments", Namespace: "ns1", ClusterID: "prod",
		Annotations: map[string]string{irsaAnnotation: arn},
	}
	stagingSA := graph.Resource{
		Kind: "ServiceAccount", Name: "payments", Namespace: "ns1", ClusterID: "staging",
		Annotations: map[string]string{irsaAnnotation: arn},
	}
	prodGot, _ := EKSIdentityExtractor{}.Extract(context.Background(), prodSA, memory.New())
	stagingGot, _ := EKSIdentityExtractor{}.Extract(context.Background(), stagingSA, memory.New())
	if len(prodGot) != 1 || len(stagingGot) != 1 {
		t.Fatal("each cluster should emit exactly one edge")
	}
	if prodGot[0].To == stagingGot[0].To {
		t.Errorf("multi-cluster: ExternalIdentity IDs collapsed across clusters (%q == %q)", prodGot[0].To, stagingGot[0].To)
	}
	if prodGot[0].To != "prod:_external/IAMRole/"+arn {
		t.Errorf("prod To = %q", prodGot[0].To)
	}
	if stagingGot[0].To != "staging:_external/IAMRole/"+arn {
		t.Errorf("staging To = %q", stagingGot[0].To)
	}
}

func TestEKSIdentity_WhitespaceInAnnotationIsTrimmed(t *testing.T) {
	arn := "arn:aws:iam::1:role/payments"
	got, _ := EKSIdentityExtractor{}.Extract(context.Background(),
		graph.Resource{Kind: "ServiceAccount", Name: "payments", Namespace: "ns1",
			Annotations: map[string]string{irsaAnnotation: "  " + arn + "  "}},
		memory.New())
	if len(got) != 1 || got[0].To != "_external/IAMRole/"+arn {
		t.Errorf("whitespace not trimmed: To=%q", got[0].To)
	}
}

func TestEKSIdentity_TypeIsBindsPlatformIdentity(t *testing.T) {
	if got := (EKSIdentityExtractor{}).Type(); got != graph.EdgeTypeBindsPlatformIdentity {
		t.Errorf("Type() = %q, want BINDS_PLATFORM_IDENTITY", got)
	}
}
