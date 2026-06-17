// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// snapEventFixture seeds a small resource_events stream + a couple
// of snapshot_meta markers so the snapshot handlers have data.
func snapEventFixture(s graph.GraphStore) {
	ctx := context.Background()
	base := time.Now().Add(-30 * time.Minute)
	ev := func(off time.Duration, name, uid string, et graph.EventType) {
		_ = s.AppendEvent(ctx, graph.ResourceEvent{
			Timestamp: base.Add(off), Namespace: "demo", Kind: "Pod",
			UID: uid, Name: name, EventType: et,
		})
	}
	ev(1*time.Minute, "added-pod", "uid-a", graph.EventTypeAdd)
	ev(2*time.Minute, "gone-pod", "uid-g", graph.EventTypeDelete)
	ev(3*time.Minute, "changed-pod", "uid-c", graph.EventTypeUpdate)
	_ = s.AppendSnapshotMeta(ctx, graph.SnapshotMeta{
		Timestamp: base, ResourceCount: 7, Trigger: graph.SnapshotTriggerPeriodic,
	})
}

// --- 503 gating (invariant 2.2) ------------------------------------

func TestSnapshots_List_503WithoutSnapshotsEnabled(t *testing.T) {
	// No WithSnapshots option -> the server treats snapshots as
	// unavailable (Tier 1 / not enabled).
	base, _, stop := seedAndServe(t, snapEventFixture)
	defer stop()

	r, _ := getJSON(t, base+"/api/v1/snapshots", nil)
	if r.StatusCode != 503 {
		t.Errorf("status = %d, want 503 when snapshots are not enabled", r.StatusCode)
	}
}

func TestSnapshots_Diff_503WithoutSnapshotsEnabled(t *testing.T) {
	base, _, stop := seedAndServe(t, snapEventFixture)
	defer stop()

	r, _ := getJSON(t, base+"/api/v1/snapshots/diff?from=1h", nil)
	if r.StatusCode != 503 {
		t.Errorf("status = %d, want 503 when snapshots are not enabled", r.StatusCode)
	}
}

// --- list ----------------------------------------------------------

func TestSnapshots_List_ReturnsMarkers(t *testing.T) {
	base, _, stop := seedAndServe(t, snapEventFixture, api.WithSnapshots(7*24*time.Hour))
	defer stop()

	var resp api.SnapshotListResponse
	r, _ := getJSON(t, base+"/api/v1/snapshots", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if resp.Count != 1 || len(resp.Snapshots) != 1 {
		t.Fatalf("got %d markers, want 1", resp.Count)
	}
	if resp.Snapshots[0].ResourceCount != 7 {
		t.Errorf("resourceCount = %d, want 7", resp.Snapshots[0].ResourceCount)
	}
}

// --- diff ----------------------------------------------------------

func TestSnapshots_Diff_ClassifiesChanges(t *testing.T) {
	base, _, stop := seedAndServe(t, snapEventFixture, api.WithSnapshots(7*24*time.Hour))
	defer stop()

	var resp analysis.DiffResult
	r, _ := getJSON(t, base+"/api/v1/snapshots/diff?from=1h&to=now", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if len(resp.Added) != 1 || resp.Added[0].Name != "added-pod" {
		t.Errorf("added = %+v, want [added-pod]", resp.Added)
	}
	if len(resp.Removed) != 1 || resp.Removed[0].Name != "gone-pod" {
		t.Errorf("removed = %+v, want [gone-pod]", resp.Removed)
	}
	if len(resp.Modified) != 1 || resp.Modified[0].Name != "changed-pod" {
		t.Errorf("modified = %+v, want [changed-pod]", resp.Modified)
	}
}

func TestSnapshots_Diff_RequiresFrom(t *testing.T) {
	base, _, stop := seedAndServe(t, snapEventFixture, api.WithSnapshots(7*24*time.Hour))
	defer stop()

	r, _ := getJSON(t, base+"/api/v1/snapshots/diff", nil)
	if r.StatusCode != 400 {
		t.Errorf("status = %d, want 400 when from is missing", r.StatusCode)
	}
}

func TestSnapshots_Diff_RejectsInvertedWindow(t *testing.T) {
	base, _, stop := seedAndServe(t, snapEventFixture, api.WithSnapshots(7*24*time.Hour))
	defer stop()

	// from=now, to=1h-ago -> from is after to.
	r, _ := getJSON(t, base+"/api/v1/snapshots/diff?from=now&to=1h", nil)
	if r.StatusCode != 400 {
		t.Errorf("status = %d, want 400 when from is not before to", r.StatusCode)
	}
}

func TestSnapshots_Diff_RejectsWindowWiderThanRetention(t *testing.T) {
	// Retention is 1h; a 30d-wide window must be rejected.
	base, _, stop := seedAndServe(t, snapEventFixture, api.WithSnapshots(time.Hour))
	defer stop()

	r, body := getJSON(t, base+"/api/v1/snapshots/diff?from=30d&to=now", nil)
	if r.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for a window wider than retention (body: %s)",
			r.StatusCode, body)
	}
}

func TestSnapshots_Diff_RejectsUnparseableTime(t *testing.T) {
	base, _, stop := seedAndServe(t, snapEventFixture, api.WithSnapshots(7*24*time.Hour))
	defer stop()

	r, _ := getJSON(t, base+"/api/v1/snapshots/diff?from=banana", nil)
	if r.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for an unparseable time", r.StatusCode)
	}
}

func TestSnapshots_Diff_AbsoluteRFC3339(t *testing.T) {
	base, _, stop := seedAndServe(t, snapEventFixture, api.WithSnapshots(7*24*time.Hour))
	defer stop()

	// An RFC3339 from spanning the fixture window. to defaults to now.
	from := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	var resp analysis.DiffResult
	r, _ := getJSON(t, base+"/api/v1/snapshots/diff?from="+from, &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 for an RFC3339 from", r.StatusCode)
	}
	if len(resp.Added)+len(resp.Removed)+len(resp.Modified) != 3 {
		t.Errorf("expected the 3 fixture changes, got %+v", resp)
	}
}
