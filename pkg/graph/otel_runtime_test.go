// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import "testing"

func TestRuntimeEdge_EdgeProjection(t *testing.T) {
	re := RuntimeEdge{
		FromID:      "app/Deployment/frontend",
		ToID:        "app/Deployment/api",
		FromService: "frontend",
		ToService:   "api",
		Namespace:   "app",
		CallCount:   7,
	}
	e := re.Edge()
	if e.Type != EdgeTypeCallsAtRuntime {
		t.Errorf("type = %q, want CALLS_AT_RUNTIME", e.Type)
	}
	if e.From != re.FromID || e.To != re.ToID {
		t.Errorf("endpoints = %s->%s, want %s->%s", e.From, e.To, re.FromID, re.ToID)
	}
	if e.Attributes["from_service"] != "frontend" || e.Attributes["to_service"] != "api" {
		t.Errorf("service attrs = %v", e.Attributes)
	}
	if e.Attributes["call_count"] != "7" {
		t.Errorf("call_count attr = %q, want 7", e.Attributes["call_count"])
	}
}

// CALLS_AT_RUNTIME must stay off the declarative edge-type list so it
// never leaks into /api/v1/graph or /api/v1alpha1/graph (invariant 2.2)
// and never gains an extractor.
func TestCallsAtRuntime_NotInAllEdgeTypes(t *testing.T) {
	for _, et := range AllEdgeTypes {
		if et == EdgeTypeCallsAtRuntime {
			t.Fatal("EdgeTypeCallsAtRuntime must NOT be a member of AllEdgeTypes")
		}
	}
}
