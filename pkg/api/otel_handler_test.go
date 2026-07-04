// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// fakeOtelReader implements api.OtelReader over in-memory fixtures.
type fakeOtelReader struct {
	spans []graph.Span
	edges []graph.RuntimeEdge
}

func (f *fakeOtelReader) QuerySpans(_ context.Context, service string, _ time.Time, _ int) ([]graph.Span, error) {
	if service == "" {
		return f.spans, nil
	}
	var out []graph.Span
	for _, sp := range f.spans {
		if sp.ServiceName == service {
			out = append(out, sp)
		}
	}
	return out, nil
}

func (f *fakeOtelReader) QueryRuntimeEdges(_ context.Context, ns string, _ time.Time) ([]graph.RuntimeEdge, error) {
	if ns == "" {
		return f.edges, nil
	}
	var out []graph.RuntimeEdge
	for _, e := range f.edges {
		if e.Namespace == ns {
			out = append(out, e)
		}
	}
	return out, nil
}

func TestOtelOverlay_503WhenDisabled(t *testing.T) {
	// No WithOtelOverlay option -> Tier 1 / otel-off behaviour.
	base, _, stop := seedAndServe(t, nil)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/otel/overlay", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestOtelOverlay_NotServedOnV1Alpha1(t *testing.T) {
	// The overlay is v1-only; v1alpha1 must never expose it (invariant 2.2).
	reader := &fakeOtelReader{edges: []graph.RuntimeEdge{
		{FromID: "app/Deployment/frontend", ToID: "app/Deployment/api", Namespace: "app", CallCount: 1},
	}}
	base, _, stop := seedAndServe(t, nil, api.WithOtelOverlay(reader))
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1alpha1/otel/overlay", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("v1alpha1 otel overlay status = %d, want 404 (frozen surface)", resp.StatusCode)
	}
}

func TestOtelOverlay_ReturnsRuntimeEdges(t *testing.T) {
	reader := &fakeOtelReader{edges: []graph.RuntimeEdge{
		{FromID: "app/Deployment/frontend", ToID: "app/Deployment/api",
			FromService: "frontend", ToService: "api", Namespace: "app", CallCount: 4},
	}}
	base, _, stop := seedAndServe(t, nil, api.WithOtelOverlay(reader))
	defer stop()

	var got api.OtelOverlayResponse
	resp, _ := getJSON(t, base+"/api/v1/otel/overlay?namespace=app", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got.Count != 1 || len(got.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", got.Count)
	}
	e := got.Edges[0]
	if e.Type != graph.EdgeTypeCallsAtRuntime {
		t.Errorf("edge type = %q, want CALLS_AT_RUNTIME", e.Type)
	}
	if e.From != "app/Deployment/frontend" || e.To != "app/Deployment/api" {
		t.Errorf("edge = %s -> %s", e.From, e.To)
	}
	if e.Attributes["call_count"] != "4" {
		t.Errorf("call_count attr = %q, want 4", e.Attributes["call_count"])
	}
}

func TestOtelOverlay_CompareClassifies(t *testing.T) {
	// Declared ROUTES_TO: frontend->api, api->billing.
	// Observed runtime : frontend->api (both), api->cache (observed_only).
	seed := func(s graph.GraphStore) {
		ctx := context.Background()
		for _, name := range []string{"frontend", "api", "billing"} {
			_ = s.UpsertResource(ctx, graph.Resource{Namespace: "app", Kind: "Deployment", Name: name})
		}
		_ = s.UpsertEdge(ctx, graph.Edge{From: "app/Deployment/frontend", To: "app/Deployment/api", Type: graph.EdgeTypeRoutesTo})
		_ = s.UpsertEdge(ctx, graph.Edge{From: "app/Deployment/api", To: "app/Deployment/billing", Type: graph.EdgeTypeRoutesTo})
	}
	reader := &fakeOtelReader{edges: []graph.RuntimeEdge{
		{FromID: "app/Deployment/frontend", ToID: "app/Deployment/api", Namespace: "app", CallCount: 9},
		{FromID: "app/Deployment/api", ToID: "app/Deployment/cache", Namespace: "app", CallCount: 2},
	}}
	base, _, stop := seedAndServe(t, seed, api.WithOtelOverlay(reader))
	defer stop()

	var got api.OtelOverlayCompareResponse
	resp, body := getJSON(t, base+"/api/v1/otel/overlay?namespace=app&compare=true", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if got.Summary.DeclaredOnly != 1 || got.Summary.ObservedOnly != 1 || got.Summary.Both != 1 {
		t.Fatalf("summary = %+v, want declaredOnly=1 observedOnly=1 both=1", got.Summary)
	}
	if got.Count != 3 {
		t.Errorf("edge count = %d, want 3", got.Count)
	}
}

func TestOtelOverlay_CompareRequiresNamespace(t *testing.T) {
	reader := &fakeOtelReader{}
	base, _, stop := seedAndServe(t, nil, api.WithOtelOverlay(reader))
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/otel/overlay?compare=true", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (compare needs a namespace)", resp.StatusCode)
	}
}

func TestOtelOverlay_InvalidLast(t *testing.T) {
	reader := &fakeOtelReader{}
	base, _, stop := seedAndServe(t, nil, api.WithOtelOverlay(reader))
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/otel/overlay?last=notaduration", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (bad 'last')", resp.StatusCode)
	}
}

func TestOtelTraces_SummarisesByTrace(t *testing.T) {
	t0 := time.Now().Add(-time.Minute)
	reader := &fakeOtelReader{spans: []graph.Span{
		{TraceID: "t1", SpanID: "s1", ServiceName: "frontend", StartTime: t0, DurationNS: 1_000_000},
		{TraceID: "t1", SpanID: "s2", ParentSpanID: "s1", ServiceName: "api", StartTime: t0.Add(time.Millisecond), DurationNS: 500_000},
	}}
	base, _, stop := seedAndServe(t, nil, api.WithOtelOverlay(reader))
	defer stop()

	var got api.OtelTracesResponse
	resp, body := getJSON(t, base+"/api/v1/otel/traces", &got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if got.Count != 1 || len(got.Traces) != 1 {
		t.Fatalf("traces = %d, want 1", got.Count)
	}
	tr := got.Traces[0]
	if tr.SpanCount != 2 {
		t.Errorf("spanCount = %d, want 2", tr.SpanCount)
	}
	if len(tr.Services) != 2 || tr.Services[0] != "api" || tr.Services[1] != "frontend" {
		t.Errorf("services = %v, want [api frontend] (sorted)", tr.Services)
	}
}

func TestOtelTraces_503WhenDisabled(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/otel/traces", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

// Guard: the overlay response JSON stays snake-case stable for the UI.
func TestOtelOverlay_JSONShape(t *testing.T) {
	reader := &fakeOtelReader{edges: []graph.RuntimeEdge{
		{FromID: "a", ToID: "b", Namespace: "app", CallCount: 1},
	}}
	base, _, stop := seedAndServe(t, nil, api.WithOtelOverlay(reader))
	defer stop()
	_, body := getJSON(t, base+"/api/v1/otel/overlay?namespace=app", nil)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"namespace", "edges", "count"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("overlay response missing %q key", k)
		}
	}
}
