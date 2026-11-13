// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
)

type impactRetentionReader struct {
	graph.HistoryBoundsReader
	calls atomic.Int32
}

func TestImpactAPIMemberRecordingLossAndRetentionRemainIndependent(t *testing.T) {
	store := seedImpactAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := snapshot.New(store, snapshot.Config{Workers: 1, QueueSize: 4}, nil)
	writer.Start(ctx)
	t.Cleanup(writer.Stop)
	session := writer.ForMember("prod")
	if err := session.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	reader := &impactRetentionReader{HistoryBoundsReader: store}
	retained := snapshot.NewMemberHistoryRetention(reader, time.Hour)
	availability := snapshot.NewAnalysisAvailabilityReader(nil, writer, snapshot.AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true, Retention: time.Hour}, snapshot.WithMemberHistoryRetention(retained))
	s := New("", store, aggregator.NewRegistry(), WithClusterLister(&impactAPIClusters{clusters: []string{"prod"}}), WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "prod-token", Clusters: []string{"prod"}}})), WithImpactEvidence(analysis.ImpactEvidenceProviders{Availability: availability}))
	read := func() operations.HistoryAvailability {
		t.Helper()
		w := serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/demo/ConfigMap/settings?cluster=prod", "prod-token"))
		if w.Code != 200 {
			t.Fatalf("member recording response=%d %s", w.Code, w.Body.String())
		}
		var response ImpactResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Analysis.Availability.Evidence.History
	}
	if h := read(); h.State != operations.HistoryRecording || h.Coverage != operations.CoverageUnknown || h.ObservationStartedAt.IsZero() || h.RetainedData != "not_queried" {
		t.Fatalf("running member incorrectly qualified retention: %+v", h)
	}
	session.Enqueue(graph.ResourceEvent{ClusterID: "hidden", Kind: "Pod", Name: "rejected-foreign-event", EventType: graph.EventTypeUpdate})
	session.Enqueue(graph.ResourceEvent{ClusterID: "prod", Kind: "Pod", Name: "private-recorded-event", EventType: graph.EventTypeUpdate})
	session.Close()
	if h := read(); h.State != operations.HistoryStopped || h.Coverage != operations.CoveragePartial || !slices.Contains(h.Reasons, operations.HistoryQueueGap) {
		t.Fatal("closed member lost its scoped delivery gap")
	}
	done := make(chan error, 1)
	go func() { done <- retained.ForMember("prod").Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("member metadata did not stop")
		}
	})
	deadline := time.Now().Add(time.Second)
	for {
		e, _ := retained.SnapshotRetention(ctx, "prod")
		if e.State == operations.RetentionObserved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retention did not observe recorded member event")
		}
		time.Sleep(time.Millisecond)
	}
	fresh := writer.ForMember("prod")
	if err := fresh.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.Close)
	h := read()
	if h.State != operations.HistoryDegraded || h.Coverage != operations.CoveragePartial || h.RetainedData != "observed" || h.RetentionEvidence.Bounds.ClusterID != "prod" || h.RetentionEvidence.Bounds.FirstEventAt.IsZero() || !slices.Contains(h.Reasons, operations.HistoryQueueGap) {
		t.Fatalf("retained success erased member recording loss: %+v", h)
	}
	if reader.calls.Load() != 1 {
		t.Fatal("member HTTP reads refreshed history")
	}
}

func TestImpactAPIMemberRetainedMetadataAuthorizationAndCacheIsolation(t *testing.T) {
	store := seedImpactAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now().Truncate(time.Microsecond)
	first, marker := now.Add(-time.Minute), now.Add(-10*time.Second)
	for _, e := range []graph.ResourceEvent{
		{ClusterID: "prod", Kind: "Pod", Timestamp: first},
		{ClusterID: "hidden", Kind: "Pod", Timestamp: now.Add(-30 * time.Minute)},
		{Kind: "Pod", Timestamp: now.Add(-40 * time.Minute)},
		{ClusterID: "prod", Kind: "Secret", Timestamp: now.Add(-50 * time.Minute)},
	} {
		e.EventType, e.Name = graph.EventTypeUpdate, "private-member-history-object"
		if err := store.AppendEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []graph.SnapshotMeta{
		{ClusterID: "prod", Timestamp: marker, Trigger: graph.SnapshotTriggerManual, ResourceCount: 192837465},
		{ClusterID: "hidden", Timestamp: now.Add(-time.Second), Trigger: graph.SnapshotTriggerManual},
		{Timestamp: now.Add(-2 * time.Second), Trigger: graph.SnapshotTriggerManual},
	} {
		if err := store.AppendSnapshotMeta(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	reader := &impactRetentionReader{HistoryBoundsReader: store}
	retained := snapshot.NewMemberHistoryRetention(reader, time.Hour)
	done := make(chan error, 2)
	for _, cluster := range []string{"prod", "hidden"} {
		go func() { done <- retained.ForMember(cluster).Start(ctx) }()
	}
	t.Cleanup(func() {
		cancel()
		for range 2 {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(time.Second):
				t.Error("member retention observer not joined")
			}
		}
	})
	deadline := time.Now().Add(time.Second)
	for {
		prod, _ := retained.SnapshotRetention(ctx, "prod")
		hidden, _ := retained.SnapshotRetention(ctx, "hidden")
		if prod.State == operations.RetentionObserved && hidden.State == operations.RetentionObserved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("member retention observations not ready")
		}
		time.Sleep(time.Millisecond)
	}
	availability := snapshot.NewAnalysisAvailabilityReader(nil, &snapshot.Writer{}, snapshot.AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true, Retention: time.Hour}, snapshot.WithMemberHistoryRetention(retained))
	var evidenceReads atomic.Int32
	provider := impactAPIAvailability(func(ctx context.Context, cluster string) (operations.AnalysisAvailability, error) {
		evidenceReads.Add(1)
		return availability.SnapshotAvailability(ctx, cluster)
	})
	s := New("", store, aggregator.NewRegistry(), WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}), WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "prod-token", Clusters: []string{"prod"}}})), WithImpactEvidence(analysis.ImpactEvidenceProviders{Availability: provider}))
	for range 2 {
		w := serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/demo/ConfigMap/settings?cluster=prod", "prod-token"))
		if w.Code != 200 {
			t.Fatalf("member status=%d body=%s", w.Code, w.Body.String())
		}
		var body ImpactResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		h := body.Analysis.Availability.Evidence.History
		if h.State != operations.HistoryUnsupported || h.Coverage != operations.CoverageUnknown || !h.ObservationStartedAt.IsZero() || h.RetainedData != "observed" || h.RetentionEvidence == nil || h.RetentionEvidence.Bounds.ClusterID != "prod" || !h.RetentionEvidence.Bounds.FirstEventAt.Equal(first) || !h.RetentionEvidence.Bounds.LastEventAt.Equal(first) || !h.RetentionEvidence.Bounds.LatestMarkerAt.Equal(marker) {
			t.Fatalf("member cache leaked/qualified recording: %+v", h)
		}
		for _, private := range []string{"private-member-history-object", "192837465", "hidden", now.Add(-30 * time.Minute).Format(time.RFC3339Nano)} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("response exposed foreign/payload/count history data")
			}
		}
	}
	before := evidenceReads.Load()
	for _, request := range []struct {
		query, token string
		status       int
	}{{"prod", "", 401}, {"hidden", "prod-token", 403}, {"absent", "prod-token", 403}} {
		w := serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/demo/ConfigMap/settings?cluster="+request.query, request.token))
		if w.Code != request.status || evidenceReads.Load() != before {
			t.Fatal("denied request reached member evidence cache")
		}
	}
	if reader.calls.Load() != 2 || evidenceReads.Load() != 2 {
		t.Fatal("HTTP requests refreshed history or broadened its member scope")
	}
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
