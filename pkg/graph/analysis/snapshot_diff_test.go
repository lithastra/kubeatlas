// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// diffBase is the anchor time every snapshot-diff test builds its
// event timestamps around.
var diffBase = time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

// appendEvent is a tiny helper so the test fixtures read cleanly.
func appendEvent(t *testing.T, s graph.GraphStore, off time.Duration, ns, name, uid string, et graph.EventType) {
	t.Helper()
	if err := s.AppendEvent(context.Background(), graph.ResourceEvent{
		Timestamp: diffBase.Add(off),
		Namespace: ns,
		Kind:      "Pod",
		UID:       uid,
		Name:      name,
		EventType: et,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
}

func names(es []analysis.DiffEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDiffWindow_Classification is the P3-T5 acceptance fixture:
// events covering five resources exercise all three diff buckets.
//
//	A: add                  -> Added
//	D: add, then update      -> Added (appeared inside the window)
//	B: update                -> Modified
//	C: delete                -> Removed
//	E: update, then delete   -> Removed
func TestDiffWindow_Classification(t *testing.T) {
	s := memory.New()
	ctx := context.Background()

	appendEvent(t, s, 1*time.Minute, "demo", "a", "uid-a", graph.EventTypeAdd)
	appendEvent(t, s, 2*time.Minute, "demo", "d", "uid-d", graph.EventTypeAdd)
	appendEvent(t, s, 3*time.Minute, "demo", "d", "uid-d", graph.EventTypeUpdate)
	appendEvent(t, s, 4*time.Minute, "demo", "b", "uid-b", graph.EventTypeUpdate)
	appendEvent(t, s, 5*time.Minute, "demo", "c", "uid-c", graph.EventTypeDelete)
	appendEvent(t, s, 6*time.Minute, "demo", "e", "uid-e", graph.EventTypeUpdate)
	appendEvent(t, s, 7*time.Minute, "demo", "e", "uid-e", graph.EventTypeDelete)

	res, err := analysis.DiffWindow(ctx, s, diffBase, diffBase.Add(time.Hour), "")
	if err != nil {
		t.Fatalf("DiffWindow: %v", err)
	}
	if got := names(res.Added); !eq(got, []string{"a", "d"}) {
		t.Errorf("Added = %v, want [a d]", got)
	}
	if got := names(res.Modified); !eq(got, []string{"b"}) {
		t.Errorf("Modified = %v, want [b]", got)
	}
	if got := names(res.Removed); !eq(got, []string{"c", "e"}) {
		t.Errorf("Removed = %v, want [c e]", got)
	}
}

// TestDiffWindow_LastEventWins confirms the representative entry is
// the LAST event of a resource — D's entry should carry the update
// timestamp, not the add.
func TestDiffWindow_LastEventWins(t *testing.T) {
	s := memory.New()
	appendEvent(t, s, 2*time.Minute, "demo", "d", "uid-d", graph.EventTypeAdd)
	appendEvent(t, s, 3*time.Minute, "demo", "d", "uid-d", graph.EventTypeUpdate)

	res, err := analysis.DiffWindow(context.Background(), s, diffBase, diffBase.Add(time.Hour), "")
	if err != nil {
		t.Fatalf("DiffWindow: %v", err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("Added len = %d, want 1", len(res.Added))
	}
	e := res.Added[0]
	if !e.Timestamp.Equal(diffBase.Add(3 * time.Minute)) {
		t.Errorf("entry timestamp = %v, want the update at +3m", e.Timestamp)
	}
	if e.EventType != graph.EventTypeUpdate {
		t.Errorf("entry eventType = %q, want update (the last event)", e.EventType)
	}
}

// TestDiffWindow_AddThenDeleteInWindow pins the documented corner
// case: a resource created and removed inside the same window is
// classified Removed (its final state is gone).
func TestDiffWindow_AddThenDeleteInWindow(t *testing.T) {
	s := memory.New()
	appendEvent(t, s, 1*time.Minute, "demo", "ephemeral", "uid-x", graph.EventTypeAdd)
	appendEvent(t, s, 2*time.Minute, "demo", "ephemeral", "uid-x", graph.EventTypeDelete)

	res, err := analysis.DiffWindow(context.Background(), s, diffBase, diffBase.Add(time.Hour), "")
	if err != nil {
		t.Fatalf("DiffWindow: %v", err)
	}
	if got := names(res.Removed); !eq(got, []string{"ephemeral"}) {
		t.Errorf("Removed = %v, want [ephemeral]", got)
	}
	if len(res.Added) != 0 {
		t.Errorf("Added = %v, want empty", names(res.Added))
	}
}

// TestDiffWindow_NamespaceScope confirms a non-empty namespace
// scopes the diff.
func TestDiffWindow_NamespaceScope(t *testing.T) {
	s := memory.New()
	appendEvent(t, s, 1*time.Minute, "demo", "in", "uid-in", graph.EventTypeAdd)
	appendEvent(t, s, 1*time.Minute, "other", "out", "uid-out", graph.EventTypeAdd)

	res, err := analysis.DiffWindow(context.Background(), s, diffBase, diffBase.Add(time.Hour), "demo")
	if err != nil {
		t.Fatalf("DiffWindow: %v", err)
	}
	if got := names(res.Added); !eq(got, []string{"in"}) {
		t.Errorf("Added = %v, want only the demo resource [in]", got)
	}
}

// TestDiffWindow_TimeWindowExcludesOutsideEvents confirms events
// outside [from, to] do not appear in the diff.
func TestDiffWindow_TimeWindowExcludesOutsideEvents(t *testing.T) {
	s := memory.New()
	appendEvent(t, s, -10*time.Minute, "demo", "before", "uid-b", graph.EventTypeAdd)
	appendEvent(t, s, 5*time.Minute, "demo", "inside", "uid-i", graph.EventTypeAdd)
	appendEvent(t, s, 90*time.Minute, "demo", "after", "uid-a", graph.EventTypeAdd)

	res, err := analysis.DiffWindow(context.Background(), s, diffBase, diffBase.Add(time.Hour), "")
	if err != nil {
		t.Fatalf("DiffWindow: %v", err)
	}
	if got := names(res.Added); !eq(got, []string{"inside"}) {
		t.Errorf("Added = %v, want only [inside]", got)
	}
}

// TestDiffWindow_EmptyWindow confirms a window with no events
// returns non-nil empty buckets so the JSON encodes [] not null.
func TestDiffWindow_EmptyWindow(t *testing.T) {
	s := memory.New()
	res, err := analysis.DiffWindow(context.Background(), s, diffBase, diffBase.Add(time.Hour), "")
	if err != nil {
		t.Fatalf("DiffWindow: %v", err)
	}
	if res.Added == nil || res.Removed == nil || res.Modified == nil {
		t.Error("diff buckets must be non-nil empty slices")
	}
	if len(res.Added)+len(res.Removed)+len(res.Modified) != 0 {
		t.Errorf("empty window should yield no changes, got %+v", res)
	}
	if !res.From.Equal(diffBase) || !res.To.Equal(diffBase.Add(time.Hour)) {
		t.Errorf("From/To not echoed: %v..%v", res.From, res.To)
	}
}
