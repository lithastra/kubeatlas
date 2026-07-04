// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestOverlay_Classifies(t *testing.T) {
	declared := []graph.Edge{
		{From: "app/Deployment/frontend", To: "app/Deployment/api", Type: graph.EdgeTypeRoutesTo},
		{From: "app/Deployment/api", To: "app/Deployment/billing", Type: graph.EdgeTypeRoutesTo},
	}
	observed := []graph.RuntimeEdge{
		{FromID: "app/Deployment/frontend", ToID: "app/Deployment/api", CallCount: 5},
		{FromID: "app/Deployment/api", ToID: "app/Deployment/cache", CallCount: 3},
	}

	got := Overlay(declared, observed)
	if len(got) != 3 {
		t.Fatalf("overlay edges = %d, want 3", len(got))
	}

	// Deterministic (from,to) order lets us assert positionally.
	// api->billing : declared only
	// api->cache   : observed only
	// frontend->api: both
	want := []OverlayEdge{
		{From: "app/Deployment/api", To: "app/Deployment/billing", Class: ClassDeclaredOnly},
		{From: "app/Deployment/api", To: "app/Deployment/cache", Class: ClassObservedOnly, CallCount: 3},
		{From: "app/Deployment/frontend", To: "app/Deployment/api", Class: ClassBoth, CallCount: 5},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("edge[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestOverlay_SumsDuplicateObserved(t *testing.T) {
	observed := []graph.RuntimeEdge{
		{FromID: "a", ToID: "b", CallCount: 2},
		{FromID: "a", ToID: "b", CallCount: 3},
	}
	got := Overlay(nil, observed)
	if len(got) != 1 {
		t.Fatalf("edges = %d, want 1", len(got))
	}
	if got[0].Class != ClassObservedOnly || got[0].CallCount != 5 {
		t.Errorf("got %+v, want observed_only callCount 5", got[0])
	}
}

func TestOverlay_Empty(t *testing.T) {
	if got := Overlay(nil, nil); len(got) != 0 {
		t.Errorf("Overlay(nil,nil) = %v, want empty", got)
	}
}
