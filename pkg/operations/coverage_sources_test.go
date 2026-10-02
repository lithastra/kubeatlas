// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var coverageFoo = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "foos"}

func beginSource(t *testing.T, tracker *CoverageTracker, cluster string, source CoverageSource) *CoverageSession {
	t.Helper()
	s, err := tracker.BeginSource(cluster, source, coverageTime)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func beginType(t *testing.T, s *CoverageSession, gvr schema.GroupVersionResource) *CoverageSession {
	t.Helper()
	token, err := s.BeginType(gvr, coverageTime)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestCoverageSourcesIsolationLifecycleAndOwnership(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	core := beginCoverage(t, tracker, "prod", coveragePods)
	readyCoverage(core, coveragePods)
	source := beginSource(t, tracker, "prod", CoverageSourceCRD)
	first := beginType(t, source, coverageFoo)
	readyCoverage(first, coverageFoo)
	other := beginType(t, beginSource(t, tracker, "other", CoverageSourceCRD), coverageFoo)
	other.RecordGap(coverageFoo, CoveragePersistenceFailed, coverageTime)
	read := func() CoverageSnapshot { return requireCoverage(t, tracker, "prod", coverageTime, coveragePods) }
	before := read()
	if before.State != CoverageObserved || len(before.Sources) != 1 || before.Sources[0].State != CoverageObserved || before.Sources[0].ClusterID != "prod" {
		t.Fatalf("wrong scope: %+v", before)
	}
	first.RecordGap(coverageFoo, CoveragePersistenceFailed, coverageTime)
	first.Stop(coverageTime)
	second := beginType(t, source, coverageFoo)
	readyCoverage(second, coverageFoo)
	after := read()
	row := after.Sources[0].Resources[0]
	if row.ContinuityRevision <= before.Sources[0].Resources[0].ContinuityRevision || !slices.Contains(row.Reasons, CoveragePersistenceFailed) {
		t.Fatal("replacement erased continuity or sticky gap")
	}
	first.Stop(coverageTime)
	readyCoverage(first, coverageFoo)
	first.RecordGap(coverageFoo, CoverageExtractionFailed, coverageTime)
	if !reflect.DeepEqual(after, read()) {
		t.Fatal("late callbacks changed replacement")
	}
	after.Sources[0].Resources[0].Reasons[0] = "caller mutation"
	if read().Sources[0].Resources[0].Reasons[0] == "caller mutation" {
		t.Fatal("snapshot aliased recorder")
	}
	// A core restart does not replace dynamic evidence, or vice versa.
	beforeSource := read().Sources[0]
	core = beginCoverage(t, tracker, "prod", coveragePods)
	readyCoverage(core, coveragePods)
	if !reflect.DeepEqual(beforeSource, read().Sources[0]) {
		t.Fatal("core restart reset dynamic source")
	}
	replacement := beginSource(t, tracker, "prod", CoverageSourceCRD)
	token := beginType(t, replacement, coverageFoo)
	readyCoverage(token, coverageFoo)
	before = read()
	source.Stop(coverageTime)
	second.RecordGap(coverageFoo, CoverageProcessingFailed, coverageTime)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("old source changed new source")
	}
	replacement.Stop(coverageTime)
	if _, err := replacement.BeginType(coverageFoo, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("stopped source accepted registration")
	}
}

func TestCoverageSourcesBoundsAndNoVacuousCompleteness(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s := beginSource(t, tracker, "", CoverageSourceCRD)
	if got := requireCoverage(t, tracker, "", coverageTime); got.Sources[0].State != CoverageUnknown {
		t.Fatal("empty registry became complete inventory")
	}
	for _, source := range []CoverageSource{"", "raw-error", "third-party"} {
		if _, err := tracker.BeginSource("", source, coverageTime); !errors.Is(err, ErrCoverageScope) {
			t.Fatal("accepted open source label")
		}
	}
	if _, err := s.BeginType(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("accepted Secret watcher")
	}
	for i := range MaxCoverageTypes {
		gvr := coverageFoo
		gvr.Resource = fmt.Sprintf("type%d", i)
		token := beginType(t, s, gvr)
		readyCoverage(token, gvr)
		token.Stop(coverageTime)
	}
	if _, err := s.BeginType(coverageFoo, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("unbounded registry")
	}
	got := requireCoverage(t, tracker, "", coverageTime)
	if !got.Sources[0].InventoryLimited || len(got.Sources[0].Resources) != MaxCoverageTypes {
		t.Fatal("evicted history or hid capacity gap")
	}
	beginSource(t, tracker, "", CoverageSourceCRD)
	if !requireCoverage(t, tracker, "", coverageTime).Sources[0].InventoryLimited {
		t.Fatal("restart cleared incomplete inventory")
	}
	for i := 1; i < MaxCoverageClusters; i++ {
		beginSource(t, tracker, fmt.Sprintf("cluster%d", i), CoverageSourceCRD)
	}
	// Sharing an existing cluster does not spend another cluster slot.
	beginCoverage(t, tracker, "", coveragePods)
	if _, err := tracker.Begin("overflow", nil, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("core bypassed combined cluster cap")
	}
	if _, err := tracker.BeginSource("overflow", CoverageSourceCRD, coverageTime); !errors.Is(err, ErrCoverageScope) {
		t.Fatal("source bypassed cluster cap")
	}
}

func TestCoverageSourcesConcurrentRegisterStopAndCapture(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	s := beginSource(t, tracker, "prod", CoverageSourceCRD)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gvr := coverageFoo
			gvr.Resource = fmt.Sprintf("type%d", i)
			for range 20 {
				token, err := s.BeginType(gvr, coverageTime)
				if err != nil {
					t.Error(err)
					return
				}
				readyCoverage(token, gvr)
				if _, err := tracker.SnapshotCoverage(context.Background(), "prod", nil); err != nil {
					t.Error(err)
				}
				token.Stop(coverageTime)
			}
		}()
	}
	wg.Wait()
	s.Stop(coverageTime)
	if got := requireCoverage(t, tracker, "prod", coverageTime); got.Sources[0].State != CoveragePartial || len(got.Sources[0].Resources) != 16 {
		t.Fatal("lost stopped type evidence")
	}
}
