// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
)

type impactRetentionReader struct {
	graph.HistoryBoundsReader
	calls atomic.Int32
}

func (r *impactRetentionReader) ReadHistoryBounds(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
	r.calls.Add(1)
	return r.HistoryBoundsReader.ReadHistoryBounds(ctx, q)
}

func TestImpactAPIRetainedMetadataUsesBackgroundCache(t *testing.T) {
	store := seedImpactAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now().Truncate(time.Microsecond)
	first, marker := now.Add(-time.Minute), now.Add(-10*time.Second)
	for _, e := range []graph.ResourceEvent{
		{Kind: "Pod", Timestamp: first},
		{ClusterID: "hidden", Kind: "Pod", Timestamp: now.Add(-time.Hour)},
		{Kind: "Secret", Timestamp: now.Add(-2 * time.Hour)},
	} {
		e.EventType, e.Name = graph.EventTypeUpdate, "synthetic-history-private-name"
		if err := store.AppendEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []graph.SnapshotMeta{
		{Timestamp: marker, Trigger: graph.SnapshotTriggerManual, ResourceCount: 987654321},
		{ClusterID: "hidden", Timestamp: now.Add(-time.Second), Trigger: graph.SnapshotTriggerPeriodic},
	} {
		if err := store.AppendSnapshotMeta(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	reader := &impactRetentionReader{HistoryBoundsReader: store}
	observer := snapshot.NewHistoryRetentionObserver(reader, "", time.Hour)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := observer.Start(ctx); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("retention observer did not stop")
		}
	})
	deadline := time.Now().Add(time.Second)
	for {
		e, err := observer.SnapshotRetention(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if e.State == operations.RetentionObserved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial retained metadata check did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	writer := snapshot.New(store, snapshot.Config{Workers: 1}, nil)
	writer.Start(ctx)
	t.Cleanup(writer.Stop)
	availability := snapshot.NewAnalysisAvailabilityReader(nil, writer, snapshot.AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Retention: time.Hour}, snapshot.WithHistoryRetention(observer))
	s := New("", store, aggregator.NewRegistry(), WithImpactEvidence(analysis.ImpactEvidenceProviders{Availability: availability}))
	for range 2 {
		w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings", ""))
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var body ImpactResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		h := body.Analysis.Availability.Evidence.History
		if h.RetainedData != "observed" || h.RetentionEvidence == nil || !h.RetentionEvidence.Bounds.FirstEventAt.Equal(first) ||
			!h.RetentionEvidence.Bounds.LastEventAt.Equal(first) || !h.RetentionEvidence.Bounds.LatestMarkerAt.Equal(marker) || h.Coverage != operations.CoverageUnknown {
			t.Fatal("retained evidence lost scope or qualified historical completeness")
		}
		for _, private := range []string{"synthetic-history-private-name", "987654321", "hidden"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("historical identities/counts/other clusters entered impact response")
			}
		}
	}
	if reader.calls.Load() != 1 {
		t.Fatal("HTTP requests queried history instead of reading cached evidence")
	}
}
