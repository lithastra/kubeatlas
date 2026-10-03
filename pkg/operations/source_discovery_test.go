// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSourceDiscoveryEvidenceLifecycle(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	source := beginSource(t, tracker, "prod", CoverageSourceGatekeeper)
	token := beginType(t, source, coverageFoo)
	readyCoverage(token, coverageFoo)
	read := func(at time.Time) CoverageSourceSnapshot { return requireCoverage(t, tracker, "prod", at).Sources[0] }
	for i, state := range []SourceDiscoveryState{SourceAPIUnknown, SourceAPINotAdvertised, SourceAPIDenied, SourceAPIFailed, SourceAPIAdvertised} {
		checked := coverageTime
		if state == SourceAPIUnknown {
			checked = time.Time{}
		}
		if err := source.RecordDiscovery(state, checked); err != nil {
			t.Fatal(err)
		}
		got := read(coverageTime)
		if got.Discovery.State != state || got.Discovery.Revision != uint64(i+1) || got.Discovery.Stale || (got.State == CoverageObserved) != (state == SourceAPIAdvertised) {
			t.Fatalf("incorrect discovery evidence: %+v %+v", got, got.Discovery)
		}
		got.Discovery.State = "caller mutation"
		if read(coverageTime).Discovery.State != state {
			t.Fatal("snapshot aliases recorder")
		}
	}
	if read(coverageTime.Add(SourceDiscoveryMaxAge - time.Nanosecond)).Discovery.Stale {
		t.Fatal("premature aging")
	}
	aged := read(coverageTime.Add(SourceDiscoveryMaxAge))
	if !aged.Discovery.Stale || aged.State != CoveragePartial || aged.Resources[0].State != CoverageObserved {
		t.Fatal("probe age confused with watch age")
	}
	token.RecordGap(coverageFoo, CoveragePersistenceFailed, coverageTime)
	if err := source.RecordDiscovery(SourceAPIAdvertised, coverageTime); err != nil {
		t.Fatal(err)
	}
	if read(coverageTime).State != CoveragePartial {
		t.Fatal("probe healed write gap")
	}
	replacement := beginSource(t, tracker, "prod", CoverageSourceGatekeeper)
	before := read(coverageTime)
	if before.Discovery != nil {
		t.Fatal("new session inherited an old probe")
	}
	if err := source.RecordDiscovery(SourceAPIAdvertised, coverageTime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read(coverageTime)) {
		t.Fatal("old session changed replacement")
	}
	replacement.Stop(coverageTime)
	before = read(coverageTime)
	if err := replacement.RecordDiscovery(SourceAPIFailed, coverageTime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read(coverageTime)) {
		t.Fatal("stopped session accepted probe")
	}
}

func TestSourceDiscoveryRejectsInvalidScopeAndVocabulary(t *testing.T) {
	tracker := NewCoverageTracker(time.Minute)
	source := beginSource(t, tracker, "", CoverageSourceGatekeeper)
	for _, tc := range []struct {
		session *CoverageSession
		state   SourceDiscoveryState
		at      time.Time
	}{
		{source, "raw-error-canary", coverageTime}, {source, SourceAPIUnknown, coverageTime},
		{source, SourceAPIFailed, time.Time{}},
		{beginType(t, source, coverageFoo), SourceAPIAdvertised, coverageTime},
		{beginSource(t, tracker, "", CoverageSourceCRD), SourceAPIAdvertised, coverageTime},
		{beginCoverage(t, tracker, "", coveragePods), SourceAPIAdvertised, coverageTime},
	} {
		if err := tc.session.RecordDiscovery(tc.state, tc.at); !errors.Is(err, ErrCoverageScope) {
			t.Fatalf("accepted invalid evidence: %v", err)
		}
	}
}
