// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotObservationState(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		sync bool
		snap Snapshot
		want ObservationState
	}{
		{name: "initial sync", snap: Snapshot{}, want: ObservationInitializing},
		{name: "probe pending", sync: true, snap: Snapshot{}, want: ObservationInitializing},
		{name: "synced", sync: true, snap: Snapshot{KubernetesKnown: true, KubernetesReachable: true}, want: ObservationSynced},
		{name: "degraded", sync: true, snap: Snapshot{KubernetesKnown: true, KubernetesLastSuccess: now.Add(-30 * time.Second), StaleAfter: time.Minute}, want: ObservationDegraded},
		{name: "stale", sync: true, snap: Snapshot{KubernetesKnown: true, KubernetesLastSuccess: now.Add(-time.Minute), StaleAfter: time.Minute}, want: ObservationStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.snap.ObservationState(tt.sync, now); got != tt.want {
				t.Fatalf("ObservationState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMonitorSampleTracksDependenciesAndBackup(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	marker := filepath.Join(t.TempDir(), "last-successful")
	if err := os.WriteFile(marker, []byte(now.Add(-time.Hour).Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{
		ProbeTimeout:        time.Second,
		StaleAfter:          2 * time.Minute,
		BackupTimestampFile: marker,
		StorageDurable:      true,
	}, func(context.Context) error { return nil }, func(context.Context) error { return nil })
	m.sample(context.Background(), now)

	got := m.Snapshot()
	if !got.KubernetesKnown || !got.KubernetesReachable || got.KubernetesLastSuccess != now {
		t.Fatalf("Kubernetes snapshot = %+v", got)
	}
	if !got.StorageKnown || !got.StorageReachable || !got.StorageDurable || got.StorageLastSuccess != now {
		t.Fatalf("storage snapshot = %+v", got)
	}
	if !got.BackupStatusAvailable || !got.BackupLastSuccess.Equal(now.Add(-time.Hour)) {
		t.Fatalf("backup snapshot = %+v", got)
	}
}

func TestMonitorFailurePreservesLastSuccess(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fail := false
	probe := func(context.Context) error {
		if fail {
			return errors.New("unavailable")
		}
		return nil
	}
	m := New(Config{ProbeTimeout: time.Second, StaleAfter: time.Minute}, probe, probe)
	m.sample(context.Background(), now)
	fail = true
	m.sample(context.Background(), now.Add(30*time.Second))

	got := m.Snapshot()
	if got.KubernetesReachable || got.StorageReachable {
		t.Fatalf("failed probes reported reachable: %+v", got)
	}
	if got.KubernetesLastSuccess != now || got.StorageLastSuccess != now {
		t.Fatalf("last success moved on failure: %+v", got)
	}
	if state := got.ObservationState(true, now.Add(30*time.Second)); state != ObservationDegraded {
		t.Fatalf("state after short failure = %q", state)
	}
}

func TestReadBackupTimestampRejectsInvalidAndFutureContent(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	marker := filepath.Join(t.TempDir(), "last-successful")
	for _, body := range []string{"not-a-timestamp", "-1", now.Add(2 * time.Minute).Format(time.RFC3339)} {
		if err := os.WriteFile(marker, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := readBackupTimestamp(marker, now); ok {
			t.Fatalf("readBackupTimestamp accepted %q", body)
		}
	}
}

func TestLoadConfigRejectsInvalidDuration(t *testing.T) {
	t.Setenv("KUBEATLAS_OPERATIONS_PROBE_INTERVAL", "never")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig accepted invalid probe interval")
	}
}
