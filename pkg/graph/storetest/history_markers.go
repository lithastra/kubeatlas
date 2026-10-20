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

func RunHistoryMarkers(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	newStore := func(t *testing.T) (graph.GraphStore, graph.HistoryMarkerReader) {
		t.Helper()
		s := factory(t)
		r, ok := s.(graph.HistoryMarkerReader)
		if !ok {
			t.Fatal("explicit marker capability required")
		}
		return s, r
	}
	appendMarker := func(t *testing.T, s graph.GraphStore, m graph.SnapshotMeta) {
		t.Helper()
		if err := s.AppendSnapshotMeta(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("exact scope including unlabelled and SQL metacharacters", func(t *testing.T) {
		s, r := newStore(t)
		for _, cluster := range []string{"", "prod", "prod_", "prod%", "prodX", "prod' OR '1'='1"} {
			appendMarker(t, s, graph.SnapshotMeta{ClusterID: cluster, Timestamp: base, ResourceCount: 7, EdgeCount: 3, DurationMS: 2, Trigger: graph.SnapshotTriggerManual})
		}
		for _, cluster := range []string{"", "prod", "prod_", "prod%", "prodX", "prod' OR '1'='1"} {
			got, err := r.ReadHistoryMarkers(ctx, cluster)
			if err != nil || len(got) != 1 || got[0].ClusterID != cluster || got[0].ResourceCount != 7 {
				t.Fatalf("scope %q: %+v %v", cluster, got, err)
			}
		}
		empty, err := r.ReadHistoryMarkers(ctx, "absent")
		if err != nil || empty == nil || len(empty) != 0 {
			t.Fatal("empty scope must be an owned empty list")
		}
		legacy, err := s.ListSnapshotMeta(ctx)
		if err != nil || len(legacy) != 6 {
			t.Fatal("legacy store contract changed")
		}
	})
	t.Run("stable descending timestamp and ID order with owned metadata", func(t *testing.T) {
		s, r := newStore(t)
		for i, off := range []time.Duration{time.Hour, 0, time.Minute, time.Minute} {
			appendMarker(t, s, graph.SnapshotMeta{ClusterID: "prod", Timestamp: base.Add(off), ResourceCount: int64(i), Trigger: graph.SnapshotTriggerPeriodic})
		}
		got, err := r.ReadHistoryMarkers(ctx, "prod")
		if err != nil || len(got) != 4 {
			t.Fatal(err)
		}
		for i, want := range []int64{0, 3, 2, 1} {
			if got[i].ResourceCount != want {
				t.Fatal("marker ordering changed")
			}
		}
		got[0].ClusterID = "mutated"
		again, err := r.ReadHistoryMarkers(ctx, "prod")
		if err != nil || again[0].ClusterID != "prod" {
			t.Fatal("caller mutated stored marker")
		}
	})
	t.Run("scope filter before limits and overflow returns no partial list", func(t *testing.T) {
		s, r := newStore(t)
		for range graph.MaxHistoryEvents {
			appendMarker(t, s, graph.SnapshotMeta{ClusterID: "hidden", Timestamp: base, Trigger: graph.SnapshotTriggerManual})
		}
		appendMarker(t, s, graph.SnapshotMeta{ClusterID: "prod", Timestamp: base, Trigger: graph.SnapshotTriggerManual})
		got, err := r.ReadHistoryMarkers(ctx, "prod")
		if err != nil || len(got) != 1 {
			t.Fatal("hidden markers consumed selected scope budget")
		}
		// At the exact limit a success is valid; the extra row must fail closed.
		for range graph.MaxHistoryEvents - 1 {
			appendMarker(t, s, graph.SnapshotMeta{ClusterID: "prod", Timestamp: base, Trigger: graph.SnapshotTriggerManual})
		}
		got, err = r.ReadHistoryMarkers(ctx, "prod")
		if err != nil || len(got) != graph.MaxHistoryEvents {
			t.Fatal("exact marker limit rejected")
		}
		appendMarker(t, s, graph.SnapshotMeta{ClusterID: "prod", Timestamp: base, Trigger: graph.SnapshotTriggerManual})
		retained, readErr := s.ListSnapshotMeta(ctx)
		if readErr != nil {
			t.Fatal(readErr)
		}
		got, err = r.ReadHistoryMarkers(ctx, "prod")
		// Tier 1's test-support ring may have evicted the oldest row already.
		// The cap applies to retained rows, never a claim of complete retention.
		if len(retained) > graph.MaxHistoryEvents {
			if !errors.Is(err, graph.ErrHistoryEventLimit) || got != nil {
				t.Fatalf("overflow returned partial list: %d %v", len(got), err)
			}
		} else if err != nil || len(got) != graph.MaxHistoryEvents {
			t.Fatal("ring eviction changed bounded retained read")
		}

	})
	t.Run("invalid scope and cancellation", func(t *testing.T) {
		_, r := newStore(t)
		if got, err := r.ReadHistoryMarkers(ctx, "bad:scope"); !errors.Is(err, graph.ErrHistoryEventQuery) || got != nil {
			t.Fatal("invalid scope accepted")
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if got, err := r.ReadHistoryMarkers(cancelled, "prod"); !errors.Is(err, context.Canceled) || got != nil {
			t.Fatal("cancelled read accepted")
		}
	})
}
