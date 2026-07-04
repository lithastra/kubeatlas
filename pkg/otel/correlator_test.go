// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// --- test doubles ---

type fakeLister struct {
	byNS  map[string][]graph.Resource
	err   error
	calls int
}

func (f *fakeLister) ListResources(_ context.Context, filter graph.Filter) ([]graph.Resource, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.byNS[filter.Namespace], nil
}

type fakeSpanSource struct {
	spans     []graph.Span
	err       error
	sinceSeen time.Time
}

func (f *fakeSpanSource) QuerySpans(_ context.Context, _ string, since time.Time, _ int) ([]graph.Span, error) {
	f.sinceSeen = since
	if f.err != nil {
		return nil, f.err
	}
	return f.spans, nil
}

type fakeEdgeSink struct {
	upserted    [][]graph.RuntimeEdge
	upsertErr   error
	pruneCutoff time.Time
	pruneCalls  int
	pruneN      int64
	pruneErr    error
}

func (f *fakeEdgeSink) UpsertRuntimeEdges(_ context.Context, edges []graph.RuntimeEdge) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	cp := make([]graph.RuntimeEdge, len(edges))
	copy(cp, edges)
	f.upserted = append(f.upserted, cp)
	return nil
}

func (f *fakeEdgeSink) DeleteOldRuntimeEdges(_ context.Context, cutoff time.Time) (int64, error) {
	f.pruneCalls++
	f.pruneCutoff = cutoff
	return f.pruneN, f.pruneErr
}

// --- helpers ---

var base = time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)

func dep(ns, name string) graph.Resource {
	return graph.Resource{Namespace: ns, Kind: "Deployment", Name: name}
}

// span builds a span; empty fields stay empty.
func span(id, parent, svc, ns, deployment, pod string, offset time.Duration) graph.Span {
	return graph.Span{
		SpanID:        id,
		ParentSpanID:  parent,
		ServiceName:   svc,
		K8sNamespace:  ns,
		K8sDeployment: deployment,
		K8sPod:        pod,
		StartTime:     base.Add(offset),
	}
}

// --- inferCalls ---

func TestInferCalls(t *testing.T) {
	tests := []struct {
		name  string
		spans []graph.Span
		want  int // number of calls
	}{
		{
			name: "cross-service parent/child emits a call",
			spans: []graph.Span{
				span("s1", "", "frontend", "app", "frontend", "", 0),
				span("s2", "s1", "api", "app", "api", "", time.Millisecond),
			},
			want: 1,
		},
		{
			name: "root span alone emits nothing",
			spans: []graph.Span{
				span("s1", "", "frontend", "app", "frontend", "", 0),
			},
			want: 0,
		},
		{
			name: "same service is an internal span, not a call",
			spans: []graph.Span{
				span("s1", "", "api", "app", "api", "", 0),
				span("s2", "s1", "api", "app", "api", "", time.Millisecond),
			},
			want: 0,
		},
		{
			name: "parent missing from window is skipped",
			spans: []graph.Span{
				span("s2", "missing", "api", "app", "api", "", 0),
			},
			want: 0,
		},
		{
			name: "empty service name on either end is skipped",
			spans: []graph.Span{
				span("s1", "", "", "app", "", "", 0),
				span("s2", "s1", "api", "app", "api", "", time.Millisecond),
			},
			want: 0,
		},
		{
			name: "two distinct cross-service calls",
			spans: []graph.Span{
				span("s1", "", "frontend", "app", "frontend", "", 0),
				span("s2", "s1", "api", "app", "api", "", time.Millisecond),
				span("s3", "s2", "db", "app", "db", "", 2*time.Millisecond),
			},
			want: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inferCalls(tc.spans)
			if len(got) != tc.want {
				t.Fatalf("inferCalls: got %d calls, want %d", len(got), tc.want)
			}
		})
	}
}

// --- resolver ---

func TestResolverResolve(t *testing.T) {
	resources := []graph.Resource{
		dep("app", "frontend"),
		dep("app", "api"),
		{Namespace: "app", Kind: "Pod", Name: "api-xyz"},
		{Namespace: "app", Kind: "Service", Name: "cache"},
		{Namespace: "app", Kind: "StatefulSet", Name: "db"},
	}
	rz := newResolver(resources)

	tests := []struct {
		name   string
		sp     graph.Span
		wantID string
		wantOK bool
	}{
		{
			name:   "deployment attribute wins",
			sp:     span("x", "", "api", "app", "api", "api-xyz", 0),
			wantID: "app/Deployment/api",
			wantOK: true,
		},
		{
			name:   "pod fallback when no deployment",
			sp:     graph.Span{ServiceName: "api", K8sNamespace: "app", K8sPod: "api-xyz"},
			wantID: "app/Pod/api-xyz",
			wantOK: true,
		},
		{
			name:   "service.name resolves to a Deployment",
			sp:     graph.Span{ServiceName: "frontend", K8sNamespace: "app"},
			wantID: "app/Deployment/frontend",
			wantOK: true,
		},
		{
			name:   "service.name resolves to a StatefulSet",
			sp:     graph.Span{ServiceName: "db", K8sNamespace: "app"},
			wantID: "app/StatefulSet/db",
			wantOK: true,
		},
		{
			name:   "service.name resolves to a Service when no workload matches",
			sp:     graph.Span{ServiceName: "cache", K8sNamespace: "app"},
			wantID: "app/Service/cache",
			wantOK: true,
		},
		{
			name:   "missing namespace is unmatched",
			sp:     graph.Span{ServiceName: "api"},
			wantOK: false,
		},
		{
			name:   "no matching resource is unmatched",
			sp:     graph.Span{ServiceName: "ghost", K8sNamespace: "app"},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, _, ok := rz.resolve(tc.sp)
			if ok != tc.wantOK {
				t.Fatalf("resolve ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && id != tc.wantID {
				t.Fatalf("resolve id = %q, want %q", id, tc.wantID)
			}
		})
	}
}

// --- correlate ---

func newTestCorrelator(lister graph.ResourceLister) *Correlator {
	return NewCorrelator(&fakeSpanSource{}, lister, &fakeEdgeSink{}, 0, nil)
}

func TestCorrelate_EmitsRuntimeEdge(t *testing.T) {
	lister := &fakeLister{byNS: map[string][]graph.Resource{
		"app": {dep("app", "frontend"), dep("app", "api")},
	}}
	c := newTestCorrelator(lister)
	spans := []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
		span("s2", "s1", "api", "app", "api", "", time.Millisecond),
	}
	edges, unmatched := c.correlate(context.Background(), spans)
	if unmatched != 0 {
		t.Fatalf("unmatched = %d, want 0", unmatched)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	e := edges[0]
	if e.FromID != "app/Deployment/frontend" || e.ToID != "app/Deployment/api" {
		t.Errorf("edge = %s -> %s, want frontend -> api", e.FromID, e.ToID)
	}
	if e.FromService != "frontend" || e.ToService != "api" {
		t.Errorf("services = %s -> %s", e.FromService, e.ToService)
	}
	if e.Namespace != "app" {
		t.Errorf("namespace = %q, want app", e.Namespace)
	}
	if e.CallCount != 1 {
		t.Errorf("callCount = %d, want 1", e.CallCount)
	}
}

func TestCorrelate_DedupsAndCounts(t *testing.T) {
	lister := &fakeLister{byNS: map[string][]graph.Resource{
		"app": {dep("app", "frontend"), dep("app", "api")},
	}}
	c := newTestCorrelator(lister)
	// Two separate frontend->api calls in different traces.
	spans := []graph.Span{
		span("a1", "", "frontend", "app", "frontend", "", 0),
		span("a2", "a1", "api", "app", "api", "", time.Millisecond),
		span("b1", "", "frontend", "app", "frontend", "", time.Minute),
		span("b2", "b1", "api", "app", "api", "", time.Minute+time.Millisecond),
	}
	edges, _ := c.correlate(context.Background(), spans)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1 (deduped)", len(edges))
	}
	if edges[0].CallCount != 2 {
		t.Errorf("callCount = %d, want 2", edges[0].CallCount)
	}
	// FirstSeen/LastSeen bracket the two observations.
	if !edges[0].FirstSeen.Equal(base.Add(time.Millisecond)) {
		t.Errorf("firstSeen = %v", edges[0].FirstSeen)
	}
	if !edges[0].LastSeen.Equal(base.Add(time.Minute + time.Millisecond)) {
		t.Errorf("lastSeen = %v", edges[0].LastSeen)
	}
}

func TestCorrelate_CountsUnmatched(t *testing.T) {
	// api Deployment is absent, so the callee cannot resolve.
	lister := &fakeLister{byNS: map[string][]graph.Resource{
		"app": {dep("app", "frontend")},
	}}
	c := newTestCorrelator(lister)
	spans := []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
		span("s2", "s1", "api", "app", "api", "", time.Millisecond),
	}
	edges, unmatched := c.correlate(context.Background(), spans)
	if len(edges) != 0 {
		t.Fatalf("edges = %d, want 0 (callee unmatched)", len(edges))
	}
	if unmatched != 1 {
		t.Errorf("unmatched = %d, want 1", unmatched)
	}
}

func TestCorrelate_SkipsSelfCall(t *testing.T) {
	// Both spans carry different service names but resolve to the same
	// Deployment (service.name matches the deployment either way) — not
	// a topology edge.
	lister := &fakeLister{byNS: map[string][]graph.Resource{
		"app": {dep("app", "frontend")},
	}}
	c := newTestCorrelator(lister)
	spans := []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
		// child's service "sidecar" but its deployment attr is frontend
		span("s2", "s1", "sidecar", "app", "frontend", "", time.Millisecond),
	}
	edges, _ := c.correlate(context.Background(), spans)
	if len(edges) != 0 {
		t.Fatalf("edges = %d, want 0 (self-call after resolution)", len(edges))
	}
}

func TestCorrelate_NoCallsReturnsNil(t *testing.T) {
	c := newTestCorrelator(&fakeLister{})
	edges, unmatched := c.correlate(context.Background(), []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
	})
	if edges != nil || unmatched != 0 {
		t.Fatalf("got edges=%v unmatched=%d, want nil,0", edges, unmatched)
	}
}

// --- runOnce / Start ---

func TestRunOnce_UpsertsAndPrunes(t *testing.T) {
	lister := &fakeLister{byNS: map[string][]graph.Resource{
		"app": {dep("app", "frontend"), dep("app", "api")},
	}}
	source := &fakeSpanSource{spans: []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
		span("s2", "s1", "api", "app", "api", "", time.Millisecond),
	}}
	sink := &fakeEdgeSink{}
	c := NewCorrelator(source, lister, sink, 7*24*time.Hour, NewMetrics())

	now := base.Add(time.Hour)
	c.runOnce(context.Background(), now)

	if len(sink.upserted) != 1 || len(sink.upserted[0]) != 1 {
		t.Fatalf("upserted = %v, want one batch of one edge", sink.upserted)
	}
	// Span lookback window applied.
	if !source.sinceSeen.Equal(now.Add(-defaultCorrelateWindow)) {
		t.Errorf("query since = %v, want %v", source.sinceSeen, now.Add(-defaultCorrelateWindow))
	}
	// Retention prune fired with now-retention.
	if sink.pruneCalls != 1 {
		t.Errorf("pruneCalls = %d, want 1", sink.pruneCalls)
	}
	if !sink.pruneCutoff.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Errorf("prune cutoff = %v", sink.pruneCutoff)
	}
	if got := c.metrics.Snapshot().RuntimeEdges; got != 1 {
		t.Errorf("runtime edge metric = %d, want 1", got)
	}
}

func TestRunOnce_QueryErrorDegrades(t *testing.T) {
	source := &fakeSpanSource{err: errors.New("db down")}
	sink := &fakeEdgeSink{}
	c := NewCorrelator(source, &fakeLister{}, sink, 0, nil)
	c.runOnce(context.Background(), base) // must not panic
	if len(sink.upserted) != 0 || sink.pruneCalls != 0 {
		t.Errorf("query error should short-circuit: upserts=%v prunes=%d", sink.upserted, sink.pruneCalls)
	}
}

func TestStart_RunsImmediatePassThenStopsOnCancel(t *testing.T) {
	lister := &fakeLister{byNS: map[string][]graph.Resource{
		"app": {dep("app", "frontend"), dep("app", "api")},
	}}
	source := &fakeSpanSource{spans: []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
		span("s2", "s1", "api", "app", "api", "", time.Millisecond),
	}}
	sink := &fakeEdgeSink{}
	c := NewCorrelator(source, lister, sink, 0, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: Start runs the immediate pass, then returns
	c.Start(ctx)

	if len(sink.upserted) != 1 {
		t.Fatalf("immediate pass upserts = %d, want 1", len(sink.upserted))
	}
}

func TestRunOnce_UpsertErrorDegrades(t *testing.T) {
	lister := &fakeLister{byNS: map[string][]graph.Resource{
		"app": {dep("app", "frontend"), dep("app", "api")},
	}}
	source := &fakeSpanSource{spans: []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
		span("s2", "s1", "api", "app", "api", "", time.Millisecond),
	}}
	// Upsert fails, but prune still reports rows deleted (n>0 branch).
	sink := &fakeEdgeSink{upsertErr: errors.New("write failed"), pruneN: 4}
	c := NewCorrelator(source, lister, sink, 0, NewMetrics())
	c.runOnce(context.Background(), base) // must not panic

	if sink.pruneCalls != 1 {
		t.Errorf("prune should still run after an upsert error")
	}
	if got := c.metrics.Snapshot().RuntimeEdges; got != 0 {
		t.Errorf("runtime edge metric = %d, want 0 when upsert failed", got)
	}
}

func TestRunOnce_PruneErrorDegrades(t *testing.T) {
	// No spans -> no upsert; prune returns an error, which must be
	// swallowed (logged, not fatal).
	sink := &fakeEdgeSink{pruneErr: errors.New("prune failed")}
	c := NewCorrelator(&fakeSpanSource{}, &fakeLister{}, sink, 0, nil)
	c.runOnce(context.Background(), base) // must not panic
	if sink.pruneCalls != 1 {
		t.Errorf("prune should run even with no edges to upsert")
	}
}

func TestRunOnce_ResourceListErrorYieldsUnmatched(t *testing.T) {
	// Resource listing fails, so neither call endpoint resolves: both
	// count as unmatched and no edge is produced (degrade, don't crash).
	lister := &fakeLister{err: errors.New("list failed")}
	source := &fakeSpanSource{spans: []graph.Span{
		span("s1", "", "frontend", "app", "frontend", "", 0),
		span("s2", "s1", "api", "app", "api", "", time.Millisecond),
	}}
	sink := &fakeEdgeSink{}
	c := NewCorrelator(source, lister, sink, 0, NewMetrics())
	c.runOnce(context.Background(), base)

	if len(sink.upserted) != 0 {
		t.Errorf("no edges expected when resource listing fails, got %v", sink.upserted)
	}
	if got := c.metrics.Snapshot().Unmatched; got != 2 {
		t.Errorf("unmatched metric = %d, want 2 (both endpoints unresolved)", got)
	}
}
