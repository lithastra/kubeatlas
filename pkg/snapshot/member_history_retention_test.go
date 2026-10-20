// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func memberRetained(t *testing.T, r *MemberHistoryRetention, cluster string) operations.HistoryRetentionEvidence {
	t.Helper()
	e, err := r.SnapshotRetention(context.Background(), cluster)
	if err != nil || !e.ValidFor(cluster, r.retention, time.Now()) {
		t.Fatalf("invalid member retention evidence: %+v %v", e, err)
	}
	return e
}

func awaitMemberRetention(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("member retention condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func startMemberRetention(t *testing.T, r *MemberHistoryRetention, cluster string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.ForMember(cluster).Start(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("member observer exit: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Error("member observer did not join")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestMemberRetentionScopesCacheAndAvailabilityWithoutGlobalWriter(t *testing.T) {
	store := memory.New()
	base := time.Now().Truncate(time.Microsecond)
	for i, cluster := range []string{"east", "west", "", "hidden"} {
		if err := store.AppendEvent(context.Background(), graph.ResourceEvent{ClusterID: cluster, Kind: "Pod", Name: "private-history-object", EventType: graph.EventTypeUpdate, Timestamp: base.Add(-time.Duration(i+1) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendSnapshotMeta(context.Background(), graph.SnapshotMeta{ClusterID: cluster, Timestamp: base.Add(-time.Duration(i+1) * time.Second), Trigger: graph.SnapshotTriggerManual, ResourceCount: 918273645}); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.AppendEvent(context.Background(), graph.ResourceEvent{ClusterID: "east", Kind: "Secret", Name: "secret-sentinel", Timestamp: base.Add(-30 * time.Minute), EventType: graph.EventTypeUpdate})
	var calls atomic.Int32
	r := NewMemberHistoryRetention(retentionReaderFunc(func(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		calls.Add(1)
		if q.ClusterID == "" || q.ClusterID == "hidden" || q.To.Sub(q.From) != time.Hour {
			t.Error("member observer broadened its scope/window")
		}
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > graph.HistoryReadTimeout {
			t.Error("history query lost its bounded deadline")
		}
		if q.ClusterID == "denied" {
			return graph.HistoryBounds{}, errors.New("private-database-error")
		}
		return store.ReadHistoryBounds(ctx, q)
	}), time.Hour)
	_ = r.ForMember("east") // construction and request reads must not register/query
	if e := memberRetained(t, r, "east"); e.State != operations.RetentionNotQueried || calls.Load() != 0 || len(*r.index.Load()) != 0 {
		t.Fatal("construction queried history or allocated a scope")
	}
	for _, cluster := range []string{"east", "west", "denied"} {
		startMemberRetention(t, r, cluster)
	}
	awaitMemberRetention(t, func() bool {
		return memberRetained(t, r, "east").State == operations.RetentionObserved && memberRetained(t, r, "west").State == operations.RetentionObserved && memberRetained(t, r, "denied").State == operations.RetentionFailed
	})
	for i, cluster := range []string{"east", "west"} {
		e := memberRetained(t, r, cluster)
		if !e.Bounds.FirstEventAt.Equal(base.Add(-time.Duration(i+1)*time.Minute)) || !e.Bounds.LastEventAt.Equal(e.Bounds.FirstEventAt) || !e.Bounds.LatestMarkerAt.Equal(base.Add(-time.Duration(i+1)*time.Second)) {
			t.Fatalf("member %s inherited another scope: %+v", cluster, e)
		}
		e.Bounds.ClusterID = "changed"
		if memberRetained(t, r, cluster).Bounds.ClusterID != cluster {
			t.Fatal("request mutated member cache")
		}
	}
	// A zero writer would panic if global writer metrics were sampled here.
	availability := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), &Writer{}, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true, Retention: time.Hour}, WithMemberHistoryRetention(r))
	for range 10 {
		out, err := availability.SnapshotAvailability(context.Background(), "east")
		if err != nil || out.History.State != operations.HistoryUnsupported || !slices.Equal(out.History.Reasons, []operations.HistoryAvailabilityReason{operations.HistoryScopeUnknown}) || !out.History.ObservationStartedAt.IsZero() || out.History.Coverage != operations.CoverageUnknown || out.History.RetainedData != "observed" || out.History.Retention != time.Hour || out.History.RetentionEvidence.Bounds.ClusterID != "east" {
			t.Fatalf("retained metadata overstated recording/continuity: %+v %v", out, err)
		}
		body, _ := json.Marshal(out)
		for _, private := range []string{"private-history-object", "private-database-error", "secret-sentinel", "918273645", "hidden", "queueDropped"} {
			if strings.Contains(string(body), private) {
				t.Fatalf("member evidence leaked %s", private)
			}
		}
	}
	if calls.Load() != 3 {
		t.Fatal("request-side reads queried retained history")
	}
	if denied := memberRetained(t, r, "denied"); !denied.Bounds.FirstEventAt.IsZero() || !denied.Bounds.LatestMarkerAt.IsZero() {
		t.Fatal("failed member query reused success data")
	}
}

func TestMemberRetentionCancellationJoinAndFreshReattachment(t *testing.T) {
	first, second, releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r := NewMemberHistoryRetention(retentionReaderFunc(func(_ context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		if calls.Add(1) == 1 {
			close(first)
			<-releaseFirst
		} else {
			close(second)
			<-releaseSecond
		}
		return observedBounds(q), nil
	}), time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.ForMember("east").Start(ctx) }()
	<-first
	old := (*r.index.Load())["east"].observer
	cancel()
	if err := r.ForMember("east").Start(context.Background()); !errors.Is(err, ErrHistoryRetentionStarted) {
		t.Fatal("same-name observer overlapped draining query")
	}
	select {
	case <-done:
		t.Fatal("observer released name before query exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e := memberRetained(t, r, "east"); !e.Stopped || e.State != operations.RetentionFailed || !e.Bounds.FirstEventAt.IsZero() {
		t.Fatal("cancelled query manufactured successful retained data")
	}
	stop := startMemberRetention(t, r, "east")
	<-second
	if e := memberRetained(t, r, "east"); e.State != operations.RetentionNotQueried || e.Stopped || !e.CheckedAt.IsZero() {
		t.Fatal("reattached member inherited previous evidence")
	}
	// A stale observer cannot publish into the replacement's cache.
	old.evidence.Store(&operations.HistoryRetentionEvidence{State: operations.RetentionObserved})
	if memberRetained(t, r, "east").State != operations.RetentionNotQueried {
		t.Fatal("stale lifetime replaced new member evidence")
	}
	close(releaseSecond)
	awaitMemberRetention(t, func() bool { return memberRetained(t, r, "east").State == operations.RetentionObserved })
	stop()
	if e := memberRetained(t, r, "east"); !e.Stopped || e.State != operations.RetentionObserved {
		t.Fatal("stopped cache lost its qualified retained observation")
	}
}

func TestMemberRetentionProfileScopeConfigAndCancellation(t *testing.T) {
	r := NewMemberHistoryRetention(nil, time.Hour)
	for _, cluster := range []string{"", "bad/name", "bad:name", "bad\x00name", strings.Repeat("a", 254)} {
		if _, err := r.SnapshotRetention(context.Background(), cluster); !errors.Is(err, ErrAnalysisAvailabilityScope) {
			t.Fatal("invalid request scope accepted")
		}
		if err := r.ForMember(cluster).Start(context.Background()); !errors.Is(err, ErrAnalysisAvailabilityScope) {
			t.Fatal("invalid worker scope accepted")
		}
	}
	for _, cfg := range []AnalysisAvailabilityConfig{{Federated: true}, {Federated: true, HistoryEnabled: true}} {
		out, err := NewAnalysisAvailabilityReader(nil, &Writer{}, cfg, WithMemberHistoryRetention(r)).SnapshotAvailability(context.Background(), "east")
		if err != nil || out.History.RetentionEvidence != nil || out.History.Retention != 0 {
			t.Fatal("disabled/Tier1 history sampled member retention")
		}
	}
	if _, err := NewAnalysisAvailabilityReader(nil, &Writer{}, AnalysisAvailabilityConfig{Federated: true, Durable: true, HistoryEnabled: true, Retention: 2 * time.Hour}, WithMemberHistoryRetention(r)).SnapshotAvailability(context.Background(), "east"); !errors.Is(err, ErrAnalysisAvailabilityConfig) {
		t.Fatal("mismatched retention configuration accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.SnapshotRetention(ctx, "east"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request returned evidence")
	}
	if err := r.ForMember("east").Start(ctx); !errors.Is(err, context.Canceled) || len(*r.index.Load()) != 0 {
		t.Fatal("cancelled start registered a member")
	}
	if err := NewMemberHistoryRetention(nil, -time.Hour).ForMember("east").Start(context.Background()); !errors.Is(err, ErrAnalysisAvailabilityConfig) {
		t.Fatal("invalid configuration started a worker")
	}
}

func TestMemberRetentionCapacityDoesNotStopCollectionOrEvictOldScopes(t *testing.T) {
	r := NewMemberHistoryRetention(nil, time.Hour)
	var stops []func()
	for i := 0; i < maxMemberRetentionScopes; i++ {
		stops = append(stops, startMemberRetention(t, r, fmt.Sprintf("member-%d", i)))
	}
	awaitMemberRetention(t, func() bool { return len(*r.index.Load()) == maxMemberRetentionScopes })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.ForMember("over-capacity").Start(ctx) }()
	select {
	case <-done:
		t.Fatal("capacity exhaustion would stop member graph collectors")
	case <-time.After(20 * time.Millisecond):
	}
	if e := memberRetained(t, r, "over-capacity"); e.State != operations.RetentionUnsupported || len(*r.index.Load()) != maxMemberRetentionScopes {
		t.Fatal("unbounded registry or falsely observed scope")
	}
	stops[0]()
	old := (*r.index.Load())["member-0"].observer
	startMemberRetention(t, r, "member-0")
	awaitMemberRetention(t, func() bool { return (*r.index.Load())["member-0"].observer != old })
	if len(*r.index.Load()) != maxMemberRetentionScopes {
		t.Fatal("reattachment allocated another historical scope")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMemberRetentionLimitsConcurrentReadsAndCancelsQueuedMember(t *testing.T) {
	entered, release := make(chan string, 3), make(chan struct{})
	var calls, inFlight, maximum atomic.Int32
	r := NewMemberHistoryRetention(retentionReaderFunc(func(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		calls.Add(1)
		active := inFlight.Add(1)
		defer inFlight.Add(-1)
		for previous := maximum.Load(); active > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, active) {
				break
			}
		}
		entered <- q.ClusterID
		select {
		case <-release:
			return observedBounds(q), nil
		case <-ctx.Done():
			return graph.HistoryBounds{}, ctx.Err()
		}
	}), time.Hour)
	startMemberRetention(t, r, "east")
	startMemberRetention(t, r, "west")
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("initial member queries did not start")
		}
	}
	stopThird := startMemberRetention(t, r, "queued")
	awaitMemberRetention(t, func() bool { return (*r.index.Load())["queued"] != nil })
	stopThird()
	if calls.Load() != 2 || maximum.Load() != 2 {
		t.Fatal("queued member exceeded background read bound")
	}
	if e := memberRetained(t, r, "queued"); e.State != operations.RetentionFailed || !e.Stopped {
		t.Fatal("cancelled queued member appeared observed")
	}
	close(release)
	awaitMemberRetention(t, func() bool {
		return memberRetained(t, r, "east").State == operations.RetentionObserved && memberRetained(t, r, "west").State == operations.RetentionObserved
	})
}

func TestMemberRetentionExpiredCacheReadDoesNotQueryOrWaitForMembershipLock(t *testing.T) {
	var calls atomic.Int32
	r := NewMemberHistoryRetention(retentionReaderFunc(func(_ context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
		calls.Add(1)
		return observedBounds(q), nil
	}), time.Hour)
	startMemberRetention(t, r, "east")
	awaitMemberRetention(t, func() bool { return memberRetained(t, r, "east").State == operations.RetentionObserved })
	observer := (*r.index.Load())["east"].observer
	e := *observer.evidence.Load()
	shift := func(at time.Time) time.Time { return at.Add(-20 * time.Minute) }
	e.CheckedAt = shift(e.CheckedAt)
	e.Bounds.From = shift(e.Bounds.From)
	e.Bounds.To = shift(e.Bounds.To)
	e.Bounds.FirstEventAt = shift(e.Bounds.FirstEventAt)
	e.Bounds.LastEventAt = shift(e.Bounds.LastEventAt)
	e.Bounds.LatestMarkerAt = shift(e.Bounds.LatestMarkerAt)
	observer.evidence.Store(&e)
	// Registration writers can be paused without blocking the request path.
	r.mu.Lock()
	result := make(chan operations.HistoryRetentionEvidence, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		e, err := r.SnapshotRetention(ctx, "east")
		if err != nil {
			t.Error(err)
		}
		result <- e
	}()
	select {
	case current := <-result:
		if !current.Stale || current.State != operations.RetentionObserved || current.CheckedAt != e.CheckedAt || calls.Load() != 1 {
			t.Error("stale request refreshed or relabelled history")
		}
	case <-ctx.Done():
		t.Error("cache read waited on membership mutation")
	}
	r.mu.Unlock()
}
