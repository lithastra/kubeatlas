// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStorageAvailabilityCachedProbeAndExpiration(t *testing.T) {
	ctx := context.Background()
	calls, failed := 0, false
	m := New(Config{StorageDurable: true, StaleAfter: time.Minute}, func(context.Context) error { return nil }, func(context.Context) error {
		calls++
		if failed {
			return errors.New("synthetic-sensitive-probe-error")
		}
		return nil
	})
	read := func() StorageAvailability {
		t.Helper()
		s, err := m.StorageAvailability(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Durable {
			t.Fatal("availability lost independent durable configuration")
		}
		return s
	}
	if s := read(); s.State != StorageUnknown || calls != 0 {
		t.Fatal("unstarted sampler became available or issued a probe")
	}
	now := time.Now()
	m.sample(ctx, now)
	if s := read(); s.State != StorageAvailable || !s.CheckedAt.Equal(now) || !s.LastSuccessAt.Equal(now) || calls != 1 {
		t.Fatalf("fresh successful probe: %+v calls=%d", s, calls)
	}
	failed = true
	failureAt := time.Now()
	m.sample(ctx, failureAt)
	s := read()
	if s.State != StorageUnavailable || !s.CheckedAt.Equal(failureAt) || !s.LastSuccessAt.Equal(now) || calls != 2 {
		t.Fatalf("failure confused last check with last success: %+v", s)
	}
	encoded, err := json.Marshal(s)
	if err != nil || strings.Contains(string(encoded), "synthetic-sensitive") || strings.Contains(string(encoded), "Backup") || strings.Contains(string(encoded), "Kubernetes") {
		t.Fatal("availability leaked raw errors or unrelated operational surfaces")
	}
	failed = false
	old := time.Now().Add(-2 * time.Minute)
	m.sample(ctx, old)
	if s := read(); s.State != StorageStale || !m.Snapshot().StorageReachable || calls != 3 {
		t.Fatal("stopped sampler's old success was treated as live, or legacy status changed")
	}
	m.sample(ctx, time.Now().Add(time.Hour))
	if s := read(); s.State != StorageUnknown || !s.CheckedAt.IsZero() || !s.LastSuccessAt.IsZero() {
		t.Fatal("future-dated check became positive evidence")
	}
}

func TestStorageAvailabilityCancellationIncludesLockWait(t *testing.T) {
	m := New(Config{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := m.StorageAvailability(ctx); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(s, StorageAvailability{}) {
		t.Fatalf("cancelled read: %+v %v", s, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	m.mu.Lock()
	s, err := m.StorageAvailability(ctx)
	m.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(s, StorageAvailability{}) {
		t.Fatalf("blocked monitor escaped deadline: %+v %v", s, err)
	}
	if _, err := m.StorageAvailability(context.Background()); err != nil {
		t.Fatal("cancelled read retained monitor lock")
	}
}
