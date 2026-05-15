// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// TestSnapshots_HundredEventsInWindow is the P3-T2 acceptance
// scenario: write 100 events spanning a wide time range, then query
// a 5-minute window and confirm only the in-window events come back.
func TestSnapshots_HundredEventsInWindow(t *testing.T) {
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

	// 100 events, one per minute starting at base. base..base+99m.
	base := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		if err := s.AppendEvent(ctx, graph.ResourceEvent{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Namespace: "demo",
			Kind:      "ConfigMap",
			Name:      fmt.Sprintf("cm-%03d", i),
			EventType: graph.EventTypeUpdate,
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	// A 5-minute window [base+30m, base+35m] inclusive — events at
	// minutes 30,31,32,33,34,35 → 6 events.
	from := base.Add(30 * time.Minute)
	to := base.Add(35 * time.Minute)
	got, err := s.QueryEvents(ctx, "demo", from, to)
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("5-minute window: got %d events, want 6", len(got))
	}
	if got[0].Name != "cm-030" || got[5].Name != "cm-035" {
		t.Errorf("window edges: got [%s..%s], want [cm-030..cm-035]",
			got[0].Name, got[5].Name)
	}
	// Oldest-first ordering across the whole window.
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Errorf("not oldest-first at index %d", i)
		}
	}
}

// TestSnapshots_DurableBeyondMemoryCap proves the postgres backend
// is genuinely durable history — it retains far more than the
// memory store's 1000-event ring buffer. This is the property that
// makes snapshots a Tier 2 feature (invariant 2.2): write 1500
// events, get all 1500 back.
func TestSnapshots_DurableBeyondMemoryCap(t *testing.T) {
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

	base := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	const n = 1500
	for i := 0; i < n; i++ {
		if err := s.AppendEvent(ctx, graph.ResourceEvent{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Namespace: "stress",
			Kind:      "Pod",
			Name:      fmt.Sprintf("p-%04d", i),
			EventType: graph.EventTypeAdd,
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	got, err := s.QueryEvents(ctx, "stress", base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(got) != n {
		t.Errorf("durable history: got %d events, want %d (postgres must not drop like the memory ring buffer)", len(got), n)
	}
}

// TestSnapshots_DeleteEventHasNilData confirms a delete event — the
// resource is gone, only its identity is recorded — round-trips
// with Data nil rather than an empty map or an error.
func TestSnapshots_DeleteEventHasNilData(t *testing.T) {
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

	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	if err := s.AppendEvent(ctx, graph.ResourceEvent{
		Timestamp: ts,
		Namespace: "demo",
		Kind:      "Pod",
		UID:       "uid-gone",
		Name:      "deleted-pod",
		EventType: graph.EventTypeDelete,
		// Data intentionally nil — the resource no longer exists.
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	got, err := s.QueryEvents(ctx, "demo", ts.Add(-time.Minute), ts.Add(time.Minute))
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].EventType != graph.EventTypeDelete {
		t.Errorf("eventType = %q, want delete", got[0].EventType)
	}
	if got[0].Data != nil {
		t.Errorf("delete event Data = %v, want nil", got[0].Data)
	}
}

// TestSnapshots_InvalidEventTypeRejected confirms the CHECK
// constraint in migrate/005 rejects an event_type outside the
// {add,update,delete} set rather than silently storing garbage.
func TestSnapshots_InvalidEventTypeRejected(t *testing.T) {
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

	err = s.AppendEvent(ctx, graph.ResourceEvent{
		Namespace: "demo",
		Kind:      "Pod",
		Name:      "p",
		EventType: graph.EventType("bogus"),
	})
	if err == nil {
		t.Fatal("AppendEvent with invalid event_type must fail the CHECK constraint")
	}
}
