// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func RunHistoryBounds(t *testing.T, factory func(*testing.T) graph.GraphStore) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	q := graph.HistoryBoundsQuery{ClusterID: "prod%_", From: base, To: base.Add(time.Hour)}
	t.Run("exact scope and window precede aggregation", func(t *testing.T) {
		s := factory(t)
		appendEvent := func(cluster, kind string, at time.Time) {
			t.Helper()
			if err := s.AppendEvent(ctx, graph.ResourceEvent{ClusterID: cluster, Kind: kind, Timestamp: at, Name: "same-name", EventType: graph.EventTypeUpdate, Data: map[string]any{"synthetic": "excluded-payload"}}); err != nil {
				t.Fatal(err)
			}
		}
		appendMarker := func(cluster string, at time.Time) {
			t.Helper()
			if err := s.AppendSnapshotMeta(ctx, graph.SnapshotMeta{ClusterID: cluster, Timestamp: at, Trigger: graph.SnapshotTriggerPeriodic, ResourceCount: 999}); err != nil {
				t.Fatal(err)
			}
		}
		first, last := base.Add(time.Minute), q.To.Add(-time.Minute)
		appendEvent(q.ClusterID, "Pod", last) // deliberately out of insertion order
		appendEvent(q.ClusterID, "Pod", first)
		for _, at := range []time.Time{q.From, q.To} {
			appendEvent("other", "Pod", at)
			appendEvent("prodXX", "Pod", at) // SQL wildcard must stay literal
			appendEvent("", "Pod", at)
			appendEvent(q.ClusterID, "Secret", at)
			appendMarker("other", at)
			appendMarker("", at)
		}
		for _, at := range []time.Time{q.From.Add(-time.Second), q.To.Add(time.Second)} {
			appendEvent(q.ClusterID, "Pod", at)
			appendMarker(q.ClusterID, at)
		}
		marker := base.Add(30 * time.Minute)
		appendMarker(q.ClusterID, marker)
		reader := requireHistoryBoundsReader(t, s)
		got, err := reader.ReadHistoryBounds(ctx, q)
		want := graph.HistoryBounds{ClusterID: q.ClusterID, From: q.From, To: q.To, FirstEventAt: first, LastEventAt: last, LatestMarkerAt: marker}
		if err != nil || !got.FirstEventAt.Equal(want.FirstEventAt) || !got.LastEventAt.Equal(want.LastEventAt) || !got.LatestMarkerAt.Equal(want.LatestMarkerAt) || !got.ValidFor(q) {
			t.Fatalf("scope/window bounds=%+v err=%v want=%+v", got, err, want)
		}
		legacy := q
		legacy.ClusterID = ""
		got, err = reader.ReadHistoryBounds(ctx, legacy)
		if err != nil || !got.FirstEventAt.Equal(q.From) || !got.LastEventAt.Equal(q.To) || !got.LatestMarkerAt.Equal(q.To) {
			t.Fatal("unlabelled scope or inclusive boundary was not preserved")
		}
		// Retention pruning can move the first retained row independently of
		// marker existence; the reader must not manufacture a full interval.
		if _, err := s.DeleteEventsBefore(ctx, first.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		got, err = reader.ReadHistoryBounds(ctx, q)
		if err != nil || !got.FirstEventAt.Equal(last) || !got.LatestMarkerAt.Equal(marker) {
			t.Fatal("prune not reflected independently of marker")
		}
		// Returned evidence owns its data.
		got.ClusterID = "changed"
		again, err := reader.ReadHistoryBounds(ctx, q)
		if err != nil || again.ClusterID != q.ClusterID {
			t.Fatal("caller changed subsequent evidence")
		}
	})
	t.Run("empty Secret-only and marker-only scopes stay distinct", func(t *testing.T) {
		s := factory(t)
		reader := requireHistoryBoundsReader(t, s)
		if err := s.AppendEvent(ctx, graph.ResourceEvent{ClusterID: q.ClusterID, Kind: "Secret", Timestamp: base, EventType: graph.EventTypeAdd}); err != nil {
			t.Fatal(err)
		}
		got, err := reader.ReadHistoryBounds(ctx, q)
		if err != nil || !got.FirstEventAt.IsZero() || !got.LastEventAt.IsZero() || !got.LatestMarkerAt.IsZero() || !got.ValidFor(q) {
			t.Fatal("empty projection included Secret metadata or failed")
		}
		if err := s.AppendSnapshotMeta(ctx, graph.SnapshotMeta{ClusterID: q.ClusterID, Timestamp: base, Trigger: graph.SnapshotTriggerManual}); err != nil {
			t.Fatal(err)
		}
		got, err = reader.ReadHistoryBounds(ctx, q)
		if err != nil || !got.FirstEventAt.IsZero() || !got.LatestMarkerAt.Equal(base) {
			t.Fatal("marker substituted for retained event data")
		}
	})
	t.Run("submicrosecond query boundaries do not include adjacent rows", func(t *testing.T) {
		s := factory(t)
		at := base.Add(time.Microsecond)
		if err := s.AppendEvent(ctx, graph.ResourceEvent{ClusterID: "prod", Kind: "Pod", Timestamp: at, EventType: graph.EventTypeAdd}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendSnapshotMeta(ctx, graph.SnapshotMeta{ClusterID: "prod", Timestamp: at, Trigger: graph.SnapshotTriggerManual}); err != nil {
			t.Fatal(err)
		}
		for _, window := range []graph.HistoryBoundsQuery{
			{ClusterID: "prod", From: at.Add(time.Nanosecond), To: at.Add(time.Microsecond)},
			{ClusterID: "prod", From: base, To: at.Add(-time.Nanosecond)},
		} {
			got, err := requireHistoryBoundsReader(t, s).ReadHistoryBounds(ctx, window)
			if err != nil || !got.FirstEventAt.IsZero() || !got.LatestMarkerAt.IsZero() {
				t.Fatalf("out-of-window timestamp leaked: %+v %v", got, err)
			}
		}
	})
	t.Run("cancellation and invalid queries return no partial evidence", func(t *testing.T) {
		reader := requireHistoryBoundsReader(t, factory(t))
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		got, err := reader.ReadHistoryBounds(cancelled, q)
		if !errors.Is(err, context.Canceled) || got != (graph.HistoryBounds{}) {
			t.Fatal("cancellation returned partial evidence")
		}
		for _, invalid := range []graph.HistoryBoundsQuery{{}, {From: q.To, To: q.From}, {From: q.From, To: q.From}, {ClusterID: "bad/scope", From: q.From, To: q.To}} {
			got, err := reader.ReadHistoryBounds(ctx, invalid)
			if !errors.Is(err, graph.ErrHistoryEventQuery) || got != (graph.HistoryBounds{}) {
				t.Fatal("invalid query reached successful read")
			}
		}
	})
}

func requireHistoryBoundsReader(t *testing.T, s graph.GraphStore) graph.HistoryBoundsReader {
	t.Helper()
	reader, ok := s.(graph.HistoryBoundsReader)
	if !ok {
		t.Fatal("store lacks the explicit history bounds capability")
	}
	return reader
}
