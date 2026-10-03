// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

type retentionReaderFunc func(context.Context, graph.HistoryBoundsQuery) (graph.HistoryBounds, error)

func (f retentionReaderFunc) ReadHistoryBounds(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
	return f(ctx, q)
}

func observedBounds(q graph.HistoryBoundsQuery) graph.HistoryBounds {
	return graph.HistoryBounds{ClusterID: q.ClusterID, From: q.From, To: q.To,
		FirstEventAt: q.From.Add(time.Minute), LastEventAt: q.To.Add(-time.Minute), LatestMarkerAt: q.To.Add(-2 * time.Minute)}
}

func retainedSnapshot(t *testing.T, o *HistoryRetentionObserver) operations.HistoryRetentionEvidence {
	t.Helper()
	e, err := o.SnapshotRetention(context.Background(), o.cluster)
	if err != nil || !e.ValidFor(o.cluster, o.retention, time.Now()) {
		t.Fatalf("invalid cached evidence: %+v %v", e, err)
	}
	return e
}

func TestHistoryRetentionBackgroundLifecycleAndCacheOnlyReads(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	o := NewHistoryRetentionObserver(retentionReaderFunc(func(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		calls.Add(1)
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > graph.HistoryReadTimeout || q.To.Sub(q.From) != 24*time.Hour || q.ClusterID != "" {
			t.Error("background query lost time/scope bound")
		}
		close(entered)
		select {
		case <-release:
			return observedBounds(q), nil
		case <-ctx.Done():
			return graph.HistoryBounds{}, ctx.Err()
		}
	}), "", 24*time.Hour)
	if e := retainedSnapshot(t, o); e.State != operations.RetentionNotQueried || calls.Load() != 0 {
		t.Fatal("construction/read queried storage or manufactured evidence")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("observer did not stop")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not sample at startup")
	}
	if err := o.Start(ctx); !errors.Is(err, ErrHistoryRetentionStarted) {
		t.Fatal("duplicate Start created overlapping samplers")
	}
	for range 10 {
		if e := retainedSnapshot(t, o); e.State != operations.RetentionNotQueried {
			t.Fatal("blocked query looked complete")
		}
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for retainedSnapshot(t, o).State != operations.RetentionObserved {
		if time.Now().After(deadline) {
			t.Fatal("completed observation was not cached")
		}
		time.Sleep(time.Millisecond)
	}
	before := retainedSnapshot(t, o)
	before.Bounds.ClusterID = "mutated"
	for range 20 {
		if got := retainedSnapshot(t, o); got.Bounds.ClusterID != "" || got.Stale || got.Stopped {
			t.Fatal("cache ownership/freshness failed")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("request-side reads issued storage queries")
	}
}

func TestHistoryRetentionFailureExpiryAndScope(t *testing.T) {
	var fail bool
	o := NewHistoryRetentionObserver(retentionReaderFunc(func(_ context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		if fail {
			return observedBounds(q), errors.New("synthetic-private-database-error")
		}
		return observedBounds(q), nil
	}), "prod", 24*time.Hour)
	o.observe(context.Background())
	e := retainedSnapshot(t, o)
	// Expiry must not fabricate an empty current window or refresh CheckedAt.
	e.CheckedAt = e.CheckedAt.Add(-time.Hour)
	e.Bounds.From, e.Bounds.To = e.Bounds.From.Add(-time.Hour), e.Bounds.To.Add(-time.Hour)
	e.Bounds.FirstEventAt, e.Bounds.LastEventAt, e.Bounds.LatestMarkerAt = time.Time{}, time.Time{}, time.Time{}
	o.evidence.Store(&e)
	if got := retainedSnapshot(t, o); !got.Stale || !got.CheckedAt.Equal(e.CheckedAt) {
		t.Fatal("old evidence was treated as fresh")
	}
	fail = true
	o.observe(context.Background())
	got := retainedSnapshot(t, o)
	if got.State != operations.RetentionFailed || !got.Bounds.FirstEventAt.IsZero() || !got.Bounds.LastEventAt.IsZero() || !got.Bounds.LatestMarkerAt.IsZero() || got.Stale {
		t.Fatal("failed check retained old success records")
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "synthetic-private") {
		t.Fatal("raw provider error entered evidence")
	}
	if out, err := o.SnapshotRetention(context.Background(), "hidden"); !errors.Is(err, ErrAnalysisAvailabilityScope) || out != (operations.HistoryRetentionEvidence{}) {
		t.Fatal("wrong-scope cache was disclosed")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.SnapshotRetention(cancelled, "prod"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled cache read succeeded")
	}
	missing := NewHistoryRetentionObserver(nil, "", 0)
	missing.observe(context.Background())
	if e := retainedSnapshot(t, missing); e.State != operations.RetentionUnsupported || !e.CheckedAt.IsZero() {
		t.Fatal("missing optional capability claimed a storage observation")
	}
	stopped := NewHistoryRetentionObserver(nil, "", 0)
	if err := stopped.Start(cancelled); err != nil || !retainedSnapshot(t, stopped).Stopped {
		t.Fatal("shutdown not reflected in cache")
	}
}

func TestHistoryRetentionRejectsInvalidReaderBoundsAndLateSuccess(t *testing.T) {
	for _, mutate := range []func(*graph.HistoryBounds){
		func(b *graph.HistoryBounds) { b.ClusterID = "private-other-cluster" },
		func(b *graph.HistoryBounds) { b.To = b.To.Add(time.Second) },
		func(b *graph.HistoryBounds) { b.FirstEventAt = time.Time{} },
		func(b *graph.HistoryBounds) { b.LatestMarkerAt = b.To.Add(time.Hour) },
	} {
		o := NewHistoryRetentionObserver(retentionReaderFunc(func(_ context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
			b := observedBounds(q)
			mutate(&b)
			return b, nil
		}), "", DefaultRetention)
		o.observe(context.Background())
		if got := retainedSnapshot(t, o); got.State != operations.RetentionFailed || !got.Bounds.LastEventAt.IsZero() || got.Bounds.ClusterID != "" {
			t.Fatal("invalid bounds were accepted or disclosed")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := NewHistoryRetentionObserver(retentionReaderFunc(func(_ context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		cancel()
		return observedBounds(q), nil
	}), "", DefaultRetention)
	o.observe(ctx)
	if got := retainedSnapshot(t, o); got.State != operations.RetentionFailed {
		t.Fatal("cancelled check accepted late success")
	}
}

func TestHistoryRetentionAvailabilityModesAndRecordingGaps(t *testing.T) {
	o := NewHistoryRetentionObserver(retentionReaderFunc(func(_ context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		return observedBounds(q), nil
	}), "", DefaultRetention)
	o.observe(context.Background())
	w := New(&fakeSink{}, Config{QueueSize: 1}, nil)
	w.Enqueue(ev("first"))
	w.Enqueue(ev("second")) // actual queue loss must survive a retained read
	r := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true}, WithHistoryRetention(o))
	got, err := r.SnapshotAvailability(context.Background(), "")
	if err != nil || got.History.RetainedData != "observed" || got.History.RetentionEvidence.State != operations.RetentionObserved || got.History.Coverage != operations.CoveragePartial || got.History.State != operations.HistoryDegraded {
		t.Fatalf("retained metadata replaced writer loss evidence: %+v %v", got, err)
	}
	for _, cfg := range []AnalysisAvailabilityConfig{{}, {HistoryEnabled: true}, {Durable: true, HistoryEnabled: true, Federated: true}} {
		cluster := ""
		if cfg.Federated {
			cluster = "prod"
		}
		// A zero writer would panic if disabled/unsupported modes read it.
		r := NewAnalysisAvailabilityReader(nil, &Writer{}, cfg, WithHistoryRetention(o))
		got, err := r.SnapshotAvailability(context.Background(), cluster)
		if err != nil || got.History.RetentionEvidence != nil || got.History.RetainedData != "not_queried" {
			t.Fatal("disabled/unsupported mode inherited cached standalone data")
		}
	}
	r = NewAnalysisAvailabilityReader(nil, w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Retention: time.Hour}, WithHistoryRetention(o))
	if _, err := r.SnapshotAvailability(context.Background(), ""); !errors.Is(err, ErrAnalysisAvailabilityConfig) {
		t.Fatal("cache retention configuration was silently substituted")
	}
}
