// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakePruner records every PruneEventsBefore call. cutoffs is the
// list of cutoff times it was asked to prune before; deleteN is what
// it reports deleted; failWith, when set, is returned as the error.
type fakePruner struct {
	mu      sync.Mutex
	cutoffs []time.Time
	deleteN int64
	failWith error
}

func (f *fakePruner) PruneEventsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutoffs = append(f.cutoffs, cutoff)
	if f.failWith != nil {
		return 0, f.failWith
	}
	return f.deleteN, nil
}

func (f *fakePruner) calls() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.cutoffs))
	copy(out, f.cutoffs)
	return out
}

func TestParseRetention(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		bad  bool
	}{
		{"", DefaultRetention, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"0d", 0, false},
		{"24h", 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"  7d  ", 7 * 24 * time.Hour, false}, // trimmed
		{"7days", 0, true},                   // not a valid suffix
		{"-3d", 0, true},                     // negative day count
		{"-1h", 0, true},                     // negative duration
		{"banana", 0, true},
	}
	for _, c := range cases {
		got, err := ParseRetention(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseRetention(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRetention(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseRetention(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNewRetainer_DefaultsRetention(t *testing.T) {
	r := NewRetainer(&fakePruner{}, 0)
	if r.retention != DefaultRetention {
		t.Errorf("retention = %v, want DefaultRetention %v", r.retention, DefaultRetention)
	}
	r = NewRetainer(&fakePruner{}, -time.Hour)
	if r.retention != DefaultRetention {
		t.Errorf("negative retention should fall back to default, got %v", r.retention)
	}
	r = NewRetainer(&fakePruner{}, 48*time.Hour)
	if r.retention != 48*time.Hour {
		t.Errorf("explicit retention overridden: %v", r.retention)
	}
}

func TestRetainer_PrunesImmediatelyOnStart(t *testing.T) {
	fp := &fakePruner{deleteN: 5}
	r := NewRetainer(fp, 24*time.Hour)
	// A long interval means the only prune in this test is the
	// immediate one Start fires before entering its ticker loop.
	r.interval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()

	// Give Start a moment to run its immediate prune, then stop.
	deadline := time.After(time.Second)
	for len(fp.calls()) == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("Retainer did not prune within 1s of Start")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	<-done

	calls := fp.calls()
	if len(calls) < 1 {
		t.Fatalf("expected at least one prune, got %d", len(calls))
	}
	// The cutoff must be ~retention in the past.
	wantCutoff := time.Now().Add(-24 * time.Hour)
	if diff := calls[0].Sub(wantCutoff); diff > time.Minute || diff < -time.Minute {
		t.Errorf("cutoff %v is not ~24h ago (want ~%v)", calls[0], wantCutoff)
	}
}

func TestRetainer_PrunesOnTick(t *testing.T) {
	fp := &fakePruner{}
	r := NewRetainer(fp, time.Hour)
	r.interval = 20 * time.Millisecond // fast ticker for the test

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()

	// Immediate prune + several ticked prunes within ~150ms.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if n := len(fp.calls()); n < 3 {
		t.Errorf("expected several prunes (immediate + ticks), got %d", n)
	}
}

func TestRetainer_StopsOnContextCancel(t *testing.T) {
	fp := &fakePruner{}
	r := NewRetainer(fp, time.Hour)
	r.interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Retainer.Start did not return after context cancel")
	}
}

func TestRetainer_SurvivesPruneError(t *testing.T) {
	// A failing pruner must not crash the worker — the next tick
	// retries. We confirm the loop keeps calling after an error.
	fp := &fakePruner{failWith: context.DeadlineExceeded}
	r := NewRetainer(fp, time.Hour)
	r.interval = 15 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()

	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	if n := len(fp.calls()); n < 2 {
		t.Errorf("worker stopped after a prune error: only %d calls", n)
	}
}
