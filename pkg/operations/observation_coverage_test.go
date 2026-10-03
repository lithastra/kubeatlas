// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var coveragePods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
var coverageDeployments = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
var coverageTime = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func requireCoverage(t *testing.T, tracker *CoverageTracker, cluster string, now time.Time, gvrs ...schema.GroupVersionResource) CoverageSnapshot {
	t.Helper()
	out, err := tracker.Snapshot(cluster, gvrs, now)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func beginCoverage(t *testing.T, tracker *CoverageTracker, cluster string, gvrs ...schema.GroupVersionResource) *CoverageSession {
	t.Helper()
	s, err := tracker.Begin(cluster, gvrs, coverageTime)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readyCoverage(s *CoverageSession, gvr schema.GroupVersionResource) uint64 {
	s.ListResult(gvr, true, false, coverageTime)
	s.InitialDeliveryComplete(gvr)
	return s.WatchStarted(gvr)
}

func TestCoverageContinuityRevisions(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s := beginCoverage(t, tracker, "prod", coveragePods, coverageDeployments)
	epoch := readyCoverage(s, coveragePods)
	read := func() CoverageSnapshot { return requireCoverage(t, tracker, "prod", coverageTime, coveragePods) }
	before := read()
	if before.Generation == 0 || before.Resources[0].ContinuityRevision == 0 || !reflect.DeepEqual(before, read()) {
		t.Fatal("quiet captures lack stable continuity identity")
	}
	s.WatchEnded(coveragePods, epoch, CoverageWatchDenied, coverageTime)
	s.WatchStarted(coveragePods)
	after := read()
	if before.Generation != after.Generation || after.State != CoverageObserved || after.Resources[0].ContinuityRevision <= before.Resources[0].ContinuityRevision {
		t.Fatal("recovered outage lost its revision")
	}
	s.WatchEnded(coveragePods, epoch, CoverageWatchFailed, coverageTime)
	s.InitialDeliveryComplete(coveragePods)
	readyCoverage(s, coverageDeployments)
	other := beginCoverage(t, tracker, "other", coveragePods)
	readyCoverage(other, coveragePods)
	if !reflect.DeepEqual(after, read()) {
		t.Fatal("old stream, duplicate initial completion, or unrelated scope changed revision")
	}
	s.ListResult(coveragePods, false, true, coverageTime)
	s.ListResult(coveragePods, true, false, coverageTime)
	if recovered := read(); recovered.State != CoverageObserved || recovered.Resources[0].ContinuityRevision <= after.Resources[0].ContinuityRevision {
		t.Fatal("list denial/recovery lost its revision")
	}
	replacement := beginCoverage(t, tracker, "prod", coveragePods)
	readyCoverage(replacement, coveragePods)
	replaced := read()
	if replaced.Generation == before.Generation || replaced.State != CoverageObserved {
		t.Fatal("reattachment reused session identity")
	}
	s.Stop(coverageTime)
	s.RecordGap(coveragePods, CoveragePersistenceFailed, coverageTime)
	if !reflect.DeepEqual(replaced, read()) {
		t.Fatal("old session modified replacement continuity")
	}
}

func TestCoverageSnapshotCancellationIncludesLockWait(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := tracker.SnapshotCoverage(ctx, "prod", []schema.GroupVersionResource{coveragePods}); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, CoverageSnapshot{}) {
		t.Fatalf("cancelled read: %+v %v", got, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	tracker.mu.Lock()
	got, err := tracker.SnapshotCoverage(ctx, "prod", []schema.GroupVersionResource{coveragePods})
	tracker.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, CoverageSnapshot{}) {
		t.Fatalf("lock wait escaped deadline: %+v %v", got, err)
	}
	start := time.Now()
	got, err = tracker.SnapshotCoverage(context.Background(), "prod", []schema.GroupVersionResource{coveragePods})
	if err != nil || got.CapturedAt.Before(start) || got.CapturedAt.After(time.Now()) || got.State != CoverageUnknown {
		t.Fatalf("read lock leaked or capture timestamp is stale: %+v %v", got, err)
	}
}

func TestCoverageInitialDeliveryAndQuietWatch(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	if got := requireCoverage(t, tracker, "prod", coverageTime); got.State != CoverageUnknown || len(got.Resources) != 0 {
		t.Fatal("empty requirement became complete")
	}
	if got := requireCoverage(t, tracker, "prod", coverageTime, coveragePods); got.State != CoverageUnknown || !slices.Equal(got.Resources[0].Reasons, []CoverageReason{CoverageNotWatched}) {
		t.Fatal("unregistered type became observed")
	}
	s := beginCoverage(t, tracker, "prod", coveragePods)
	s.ListResult(coveragePods, true, false, coverageTime)
	s.WatchStarted(coveragePods)
	got := requireCoverage(t, tracker, "prod", coverageTime, coveragePods)
	if got.State != CoverageInitializing || got.Resources[0].InitialDeliveryDone {
		t.Fatal("cache/list success substituted for initial handler delivery")
	}
	s.InitialDeliveryComplete(coveragePods)
	got = requireCoverage(t, tracker, "prod", coverageTime.Add(24*time.Hour), coveragePods)
	if got.State != CoverageObserved || len(got.Resources[0].Reasons) != 0 || !got.Resources[0].LastListSuccess.Equal(coverageTime) {
		t.Fatalf("quiet watch or absent bookmarks became stale: %+v", got)
	}
}

func TestCoverageWatchOutageAndEpochRecovery(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s := beginCoverage(t, tracker, "", coveragePods)
	first := readyCoverage(s, coveragePods)
	s.WatchEnded(coveragePods, first, CoverageWatchDenied, coverageTime)
	s.WatchEnded(coveragePods, first, CoverageWatchClosed, coverageTime.Add(10*time.Second))
	got := requireCoverage(t, tracker, "", coverageTime.Add(59*time.Second), coveragePods)
	if got.State != CoveragePartial || !slices.Equal(got.Resources[0].Reasons, []CoverageReason{CoverageWatchDenied}) || got.Resources[0].WatchGapSince != coverageTime {
		t.Fatalf("close erased error or moved gap start: %+v", got)
	}
	s.WatchEnded(coveragePods, 0, CoverageWatchFailed, coverageTime.Add(50*time.Second))
	if got := requireCoverage(t, tracker, "", coverageTime.Add(time.Minute), coveragePods); got.State != CoverageStale {
		t.Fatal("repeated failed attempts postponed stale state")
	}
	second := s.WatchStarted(coveragePods)
	if second == first {
		t.Fatal("watch generation did not change")
	}
	s.WatchEnded(coveragePods, first, CoverageWatchFailed, coverageTime.Add(time.Hour))
	got = requireCoverage(t, tracker, "", coverageTime.Add(time.Hour), coveragePods)
	if got.State != CoverageObserved || !got.Resources[0].WatchGapSince.IsZero() {
		t.Fatal("old stream corrupted recovered watch")
	}
	s.Stop(coverageTime.Add(2 * time.Hour))
	s.WatchStarted(coveragePods)
	s.InitialDeliveryComplete(coveragePods)
	got = requireCoverage(t, tracker, "", coverageTime.Add(2*time.Hour), coveragePods)
	if got.State != CoveragePartial || !slices.Contains(got.Resources[0].Reasons, CoverageCollectorStopped) || got.Resources[0].WatchEstablished {
		t.Fatal("stopped session accepted callbacks")
	}
}

func TestCoverageScopeAndListDenials(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	prod := beginCoverage(t, tracker, "prod", coveragePods, coverageDeployments)
	other := beginCoverage(t, tracker, "other", coveragePods)
	readyCoverage(prod, coveragePods)
	readyCoverage(other, coveragePods)
	other.RecordGap(coveragePods, CoveragePersistenceFailed, coverageTime)
	prod.ListResult(coverageDeployments, false, true, coverageTime)
	got := requireCoverage(t, tracker, "prod", coverageTime, coveragePods, coverageDeployments, coveragePods)
	if got.State != CoveragePartial || len(got.Resources) != 2 || got.Resources[0].State != CoverageObserved || !slices.Contains(got.Resources[1].Reasons, CoverageListDenied) {
		t.Fatalf("GVR denial mixed with healthy types: %+v", got)
	}
	if pods := requireCoverage(t, tracker, "prod", coverageTime, coveragePods); pods.State != CoverageObserved {
		t.Fatal("foreign cluster/unrequested type contaminated selected scope")
	}
	prod.ListResult(coverageDeployments, true, false, coverageTime)
	prod.InitialDeliveryComplete(coverageDeployments)
	if got := requireCoverage(t, tracker, "prod", coverageTime, coverageDeployments); got.State != CoverageInitializing {
		t.Fatal("list success claimed watch established")
	}
	prod.WatchStarted(coverageDeployments)
	got = requireCoverage(t, tracker, "prod", coverageTime, coverageDeployments, coveragePods)
	if got.State != CoverageObserved {
		t.Fatal("successful list/watch recovery did not remove transient gap")
	}
	got.Resources[0].Reasons = append(got.Resources[0].Reasons, "modified")
	got.Resources[0].Resource = "modified"
	again := requireCoverage(t, tracker, "prod", coverageTime, coveragePods, coverageDeployments)
	if again.Resources[0].Resource != "pods" || len(again.Resources[0].Reasons) != 0 {
		t.Fatal("returned snapshot aliases state")
	}
	missing := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	if got := requireCoverage(t, tracker, "prod", coverageTime, coveragePods, missing); got.State != CoveragePartial {
		t.Fatal("missing required type was silently omitted")
	}
	if got := requireCoverage(t, tracker, "unknown", coverageTime, coveragePods, missing); got.State != CoverageUnknown {
		t.Fatal("no evidence should stay unknown")
	}
}

func TestCoverageStickyGapsAndReplacement(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s := beginCoverage(t, tracker, "prod", coveragePods)
	for _, reason := range []CoverageReason{CoverageProcessingFailed, CoverageExtractionFailed, CoveragePersistenceFailed} {
		s.RecordGap(coveragePods, reason, coverageTime)
	}
	readyCoverage(s, coveragePods)
	got := requireCoverage(t, tracker, "prod", coverageTime, coveragePods)
	want := []CoverageReason{CoverageProcessingFailed, CoverageExtractionFailed, CoveragePersistenceFailed}
	if got.State != CoveragePartial || !slices.Equal(got.Resources[0].Reasons, want) {
		t.Fatalf("later success cleared an unproven data repair: %+v", got)
	}
	// Even removing a GVR and then adding it back cannot erase sticky gaps.
	beginCoverage(t, tracker, "prod", coverageDeployments)
	replacement := beginCoverage(t, tracker, "prod", coveragePods)
	readyCoverage(replacement, coveragePods)
	s.Stop(coverageTime.Add(time.Hour))
	s.WatchEnded(coveragePods, 0, CoverageWatchDenied, coverageTime)
	got = requireCoverage(t, tracker, "prod", coverageTime, coveragePods)
	if got.State != CoveragePartial || !slices.Equal(got.Resources[0].Reasons, want) || !got.Resources[0].WatchEstablished {
		t.Fatal("reattachment lost gaps or old callback changed new session")
	}
	// API reachability remains a separate legacy signal, not a recovery event.
	m := New(Config{}, func(context.Context) error { return nil }, func(context.Context) error { return nil })
	m.coverage = tracker
	m.sample(context.Background(), coverageTime)
	if m.Snapshot().ObservationState(true, coverageTime) != ObservationSynced || requireCoverage(t, m.Coverage(), "prod", coverageTime, coveragePods).State != CoveragePartial {
		t.Fatal("API probe overwrote resource-level coverage")
	}
}

func TestCoverageBoundsAndClosedReasonVocabulary(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	for _, tc := range []struct {
		cluster string
		gvrs    []schema.GroupVersionResource
	}{
		{"bad:cluster", nil}, {"bad/cluster", nil}, {strings.Repeat("x", 254), nil},
		{"", []schema.GroupVersionResource{{Resource: "pods"}}},
		{"", []schema.GroupVersionResource{{Version: "v1", Resource: "a/b"}}},
		{"", make([]schema.GroupVersionResource, MaxCoverageTypes+1)},
	} {
		if _, err := tracker.Begin(tc.cluster, tc.gvrs, coverageTime); !errors.Is(err, ErrCoverageScope) {
			t.Fatal("invalid recording scope accepted")
		}
		if _, err := tracker.Snapshot(tc.cluster, tc.gvrs, coverageTime); !errors.Is(err, ErrCoverageScope) {
			t.Fatal("invalid read scope accepted")
		}
	}
	for i := range MaxCoverageClusters {
		beginCoverage(t, tracker, fmt.Sprintf("c%d", i), coveragePods)
	}
	if _, err := tracker.Begin("overflow", []schema.GroupVersionResource{coveragePods}, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("cluster bound exceeded")
	}
	s := beginCoverage(t, tracker, "c0", coveragePods)
	s.RecordGap(coveragePods, "synthetic-sensitive-error", coverageTime)
	s.WatchEnded(coveragePods, 0, "synthetic-sensitive-error", coverageTime)
	body, err := json.Marshal(requireCoverage(t, tracker, "c0", coverageTime, coveragePods))
	if err != nil || strings.Contains(string(body), "synthetic-sensitive-error") {
		t.Fatal("arbitrary reason escaped closed vocabulary")
	}
	types := make([]schema.GroupVersionResource, MaxCoverageTypes)
	for i := range types {
		types[i] = schema.GroupVersionResource{Version: "v1", Resource: fmt.Sprintf("kind%d", i)}
	}
	tracker = NewCoverageTracker(time.Minute)
	beginCoverage(t, tracker, "prod", types...)
	before := requireCoverage(t, tracker, "prod", coverageTime, types...)
	if _, err := tracker.Begin("prod", []schema.GroupVersionResource{coveragePods}, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("reattachment grew beyond retained type budget")
	}
	if after := requireCoverage(t, tracker, "prod", coverageTime, types...); !reflect.DeepEqual(before, after) {
		t.Fatal("failed registration replaced valid evidence")
	}
}

func TestCoverageConcurrentRecordingAndReads(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s := beginCoverage(t, tracker, "prod", coveragePods, coverageDeployments)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				epoch := readyCoverage(s, coveragePods)
				s.WatchEnded(coveragePods, epoch, CoverageWatchClosed, coverageTime)
				s.RecordGap(coverageDeployments, CoveragePersistenceFailed, coverageTime)
				got := requireCoverage(t, tracker, "prod", coverageTime, coveragePods, coverageDeployments)
				if len(got.Resources) != 2 || !slices.Contains(got.Resources[1].Reasons, CoveragePersistenceFailed) {
					t.Error("concurrent snapshot lost a known gap")
				}
			}
		})
	}
	wg.Wait()
}
