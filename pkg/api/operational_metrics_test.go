// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
)

func TestWriteOperationalPrometheusObservationStates(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		initialSync bool
		snapshot    operations.Snapshot
		want        operations.ObservationState
	}{
		{name: "initializing", snapshot: operations.Snapshot{StaleAfter: time.Minute}, want: operations.ObservationInitializing},
		{name: "synced", initialSync: true, snapshot: operations.Snapshot{KubernetesKnown: true, KubernetesReachable: true, StaleAfter: time.Minute}, want: operations.ObservationSynced},
		{name: "degraded", initialSync: true, snapshot: operations.Snapshot{KubernetesKnown: true, KubernetesLastSuccess: now.Add(-30 * time.Second), StaleAfter: time.Minute}, want: operations.ObservationDegraded},
		{name: "stale", initialSync: true, snapshot: operations.Snapshot{KubernetesKnown: true, KubernetesLastSuccess: now.Add(-time.Minute), StaleAfter: time.Minute}, want: operations.ObservationStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			writeOperationalPrometheus(&out, tt.snapshot, tt.initialSync, now)
			for _, state := range []operations.ObservationState{
				operations.ObservationInitializing,
				operations.ObservationSynced,
				operations.ObservationDegraded,
				operations.ObservationStale,
			} {
				want := 0
				if state == tt.want {
					want = 1
				}
				line := fmt.Sprintf("kubeatlas_graph_observation_state{state=%q} %d", state, want)
				if !strings.Contains(out.String(), line) {
					t.Fatalf("metrics missing %q:\n%s", line, out.String())
				}
			}
		})
	}
}

func TestWriteOperationalPrometheusDependencyAndBackupSignals(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	snapshot := operations.Snapshot{
		KubernetesKnown:       true,
		KubernetesReachable:   true,
		KubernetesLastSuccess: now.Add(-10 * time.Second),
		StorageKnown:          true,
		StorageReachable:      false,
		StorageDurable:        true,
		StorageLastSuccess:    now.Add(-20 * time.Second),
		BackupStatusAvailable: true,
		BackupLastSuccess:     now.Add(-time.Hour),
		StaleAfter:            time.Minute,
	}
	var out bytes.Buffer
	writeOperationalPrometheus(&out, snapshot, true, now)

	for _, line := range []string{
		"kubeatlas_kubernetes_api_reachable 1",
		fmt.Sprintf("kubeatlas_kubernetes_api_last_success_timestamp_seconds %d", now.Add(-10*time.Second).Unix()),
		"kubeatlas_storage_reachable 0",
		"kubeatlas_storage_durable 1",
		fmt.Sprintf("kubeatlas_storage_last_success_timestamp_seconds %d", now.Add(-20*time.Second).Unix()),
		"kubeatlas_backup_status_available 1",
		fmt.Sprintf("kubeatlas_backup_last_success_timestamp_seconds %d", now.Add(-time.Hour).Unix()),
		"kubeatlas_backup_age_seconds 3600",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("metrics missing %q:\n%s", line, out.String())
		}
	}
}
