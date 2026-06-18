// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func newSpanStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}
	h := StartPostgresWithAGE(t)
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s, ctx
}

func TestOtelSpans_RoundTrip(t *testing.T) {
	s, ctx := newSpanStore(t)
	start := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	spans := []graph.Span{
		{
			TraceID: "aaaa", SpanID: "s1", ParentSpanID: "",
			ServiceName: "frontend", K8sNamespace: "petclinic", K8sPod: "fe-1", K8sDeployment: "frontend",
			StartTime: start, DurationNS: 1500,
			Attributes: map[string]any{"http.method": "GET", "http.status": int64(200)},
		},
		{
			TraceID: "aaaa", SpanID: "s2", ParentSpanID: "s1",
			ServiceName: "api", K8sNamespace: "petclinic", K8sPod: "api-1", K8sDeployment: "api",
			StartTime: start.Add(time.Millisecond), DurationNS: 900,
		},
		{
			TraceID: "bbbb", SpanID: "s3",
			ServiceName: "frontend", K8sNamespace: "petclinic",
			StartTime: start.Add(2 * time.Millisecond),
		},
	}
	if err := s.WriteSpans(ctx, spans); err != nil {
		t.Fatalf("WriteSpans: %v", err)
	}

	// Query all services since before the window.
	since := start.Add(-time.Hour)
	all, err := s.QuerySpans(ctx, "", since, 0)
	if err != nil {
		t.Fatalf("QuerySpans all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("QuerySpans all: got %d, want 3", len(all))
	}
	// Newest-first ordering by start_time.
	if all[0].SpanID != "s3" {
		t.Errorf("first (newest) span = %q, want s3", all[0].SpanID)
	}

	// Filter by service.
	fe, err := s.QuerySpans(ctx, "frontend", since, 0)
	if err != nil {
		t.Fatalf("QuerySpans frontend: %v", err)
	}
	if len(fe) != 2 {
		t.Errorf("frontend spans: got %d, want 2", len(fe))
	}

	// Attribute + field round-trip on s1.
	var s1 *graph.Span
	for i := range all {
		if all[i].SpanID == "s1" {
			s1 = &all[i]
		}
	}
	if s1 == nil {
		t.Fatal("s1 not returned")
	}
	if s1.K8sPod != "fe-1" || s1.K8sDeployment != "frontend" || s1.DurationNS != 1500 {
		t.Errorf("s1 fields not round-tripped: %+v", s1)
	}
	if s1.Attributes["http.method"] != "GET" {
		t.Errorf("s1 string attribute not round-tripped: %+v", s1.Attributes)
	}
	// JSONB numbers come back as float64.
	if got, ok := s1.Attributes["http.status"].(float64); !ok || got != 200 {
		t.Errorf("s1 int attribute round-trip: got %v (%T), want 200", s1.Attributes["http.status"], s1.Attributes["http.status"])
	}
}

func TestOtelSpans_UpsertOnSpanID(t *testing.T) {
	s, ctx := newSpanStore(t)
	start := time.Now().UTC()
	// Same span_id sent twice (OTLP retransmit) must upsert, not error
	// or duplicate.
	if err := s.WriteSpans(ctx, []graph.Span{{TraceID: "t", SpanID: "dup", ServiceName: "a", StartTime: start}}); err != nil {
		t.Fatalf("first WriteSpans: %v", err)
	}
	if err := s.WriteSpans(ctx, []graph.Span{{TraceID: "t", SpanID: "dup", ServiceName: "b", StartTime: start}}); err != nil {
		t.Fatalf("second WriteSpans: %v", err)
	}
	got, err := s.QuerySpans(ctx, "", start.Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 (upsert on span_id)", len(got))
	}
	if got[0].ServiceName != "b" {
		t.Errorf("service_name = %q, want b (last write wins)", got[0].ServiceName)
	}
}

func TestOtelSpans_DeleteOld(t *testing.T) {
	s, ctx := newSpanStore(t)
	now := time.Now().UTC()

	// Two recent spans via the normal write path (received_at = now()).
	if err := s.WriteSpans(ctx, []graph.Span{
		{TraceID: "t", SpanID: "recent1", ServiceName: "svc", StartTime: now},
		{TraceID: "t", SpanID: "recent2", ServiceName: "svc", StartTime: now},
	}); err != nil {
		t.Fatalf("WriteSpans recent: %v", err)
	}
	// Two old spans inserted with a backdated received_at via raw SQL —
	// WriteSpans always uses the now() default, so the test sets it
	// directly to exercise the received_at-based retention cutoff.
	for _, id := range []string{"old1", "old2"} {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO otel_spans (trace_id, span_id, service_name, start_time, received_at)
			 VALUES ($1, $2, 'svc', $3, $3)`,
			"t", id, now.Add(-48*time.Hour),
		); err != nil {
			t.Fatalf("insert old span %s: %v", id, err)
		}
	}

	// Cutoff 24h ago: the two backdated rows are older, the two recent
	// rows survive.
	deleted, err := s.DeleteOldSpans(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteOldSpans: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	var remaining int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM otel_spans`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 2 {
		t.Errorf("remaining = %d, want 2 (the recent spans)", remaining)
	}
}

func TestOtelSpans_DeleteOldEmpty(t *testing.T) {
	s, ctx := newSpanStore(t)
	deleted, err := s.DeleteOldSpans(ctx, time.Now())
	if err != nil {
		t.Fatalf("DeleteOldSpans on empty table: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 on an empty table", deleted)
	}
}
