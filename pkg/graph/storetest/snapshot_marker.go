// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// RunSnapshotMarker checks the optional atomic recorder in both stores.
func RunSnapshotMarker(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	recordingStore := func(t *testing.T) (graph.GraphStore, graph.SnapshotMarkerRecorder) {
		t.Helper()
		s := factory(t)
		recorder, ok := s.(graph.SnapshotMarkerRecorder)
		if !ok {
			t.Fatal("store lacks explicit atomic marker capability")
		}
		return s, recorder
	}
	t.Run("exact scope counts and persisted receipt", func(t *testing.T) {
		s, recorder := recordingStore(t)
		reader, ok := s.(graph.HistoryMarkerReader)
		if !ok {
			t.Fatal("store lacks scoped marker reads")
		}
		scopes := []string{"", "prod", "west", "prod_", "prod%", "prod' OR '1'='1"}
		var all []graph.Resource
		for _, scope := range scopes {
			a := graph.Resource{ClusterID: scope, Namespace: "demo", Kind: "Pod", Name: "same"}
			b := graph.Resource{ClusterID: scope, Namespace: "demo", Kind: "ConfigMap", Name: "target"}
			for _, r := range []graph.Resource{a, b} {
				if err := s.UpsertResource(ctx, r); err != nil {
					t.Fatal(err)
				}
				all = append(all, r)
			}
			for _, typ := range []graph.EdgeType{graph.EdgeTypeUsesConfigMap, graph.EdgeTypeOwns} {
				if err := s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: typ}); err != nil {
					t.Fatal(err)
				}
			}
			// Neither a missing destination nor a missing source belongs in the count.
			missing := graph.Resource{ClusterID: scope, Namespace: "demo", Kind: "Pod", Name: "absent"}
			for _, e := range []graph.Edge{{From: a.ID(), To: missing.ID(), Type: graph.EdgeTypeOwns}, {From: missing.ID(), To: b.ID(), Type: graph.EdgeTypeOwns}} {
				if err := s.UpsertEdge(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := s.UpsertEdge(ctx, graph.Edge{From: all[2].ID(), To: all[4].ID(), Type: graph.EdgeTypeOwns}); err != nil {
			t.Fatal(err)
		}
		scopes = append(scopes, "absent")
		for _, scope := range scopes {
			for _, trigger := range []graph.SnapshotTrigger{graph.SnapshotTriggerManual, graph.SnapshotTriggerPeriodic} {
				got, err := recorder.RecordSnapshotMarker(ctx, scope, trigger)
				wantResources, wantEdges := int64(2), int64(2)
				if scope == "absent" {
					wantResources, wantEdges = 0, 0
				}
				if err != nil || got.ID < 1 || got.Timestamp.IsZero() || got.ClusterID != scope || got.Trigger != trigger || got.ResourceCount != wantResources || got.EdgeCount != wantEdges || got.DurationMS < 0 {
					t.Fatalf("%q: %+v %v", scope, got, err)
				}
				markers, err := reader.ReadHistoryMarkers(ctx, scope)
				if err != nil || len(markers) == 0 || !reflect.DeepEqual(markers[0], got) {
					t.Fatalf("receipt differs from persisted metadata: %+v %+v %v", got, markers, err)
				}
			}
		}
		for _, r := range all {
			got, err := s.GetResource(ctx, r.ID())
			if err != nil || got.ID() != r.ID() {
				t.Fatal("marker modified graph resources", err)
			}
		}
	})
	t.Run("rejected writes leave no markers", func(t *testing.T) {
		s, recorder := recordingStore(t)
		for _, tc := range []struct {
			cluster string
			trigger graph.SnapshotTrigger
		}{{"bad:scope", graph.SnapshotTriggerManual}, {"prod", "bogus"}, {"prod", ""}} {
			got, err := recorder.RecordSnapshotMarker(ctx, tc.cluster, tc.trigger)
			if !errors.Is(err, graph.ErrHistoryEventQuery) || !reflect.DeepEqual(got, graph.SnapshotMeta{}) {
				t.Fatalf("invalid input accepted: %+v %v", got, err)
			}
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		got, err := recorder.RecordSnapshotMarker(cancelled, "prod", graph.SnapshotTriggerManual)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, graph.SnapshotMeta{}) {
			t.Fatal("cancelled write accepted")
		}
		markers, err := s.ListSnapshotMeta(ctx)
		if err != nil || len(markers) != 0 {
			t.Fatal("rejected write persisted a marker")
		}
	})
}
