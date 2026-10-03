// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"time"
)

// AnalysisAvailability is sanitized, cached operational evidence. It does not
// contain Kubernetes reachability, backup markers, event counts or event data.
// A caller must authorize ClusterID before requesting this evidence.
type AnalysisAvailability struct {
	ClusterID  string              `json:"clusterId"`
	CapturedAt time.Time           `json:"capturedAt"`
	Storage    StorageAvailability `json:"storage"`
	History    HistoryAvailability `json:"history"`
}

type StorageAvailabilityState string

const (
	StorageUnknown     StorageAvailabilityState = "unknown"
	StorageAvailable   StorageAvailabilityState = "available"
	StorageUnavailable StorageAvailabilityState = "unavailable"
	StorageStale       StorageAvailabilityState = "stale"
)

// Available describes the last background probe, not this query or history
// completeness. Even a successful probe expires if the sampler stops running.
type StorageAvailability struct {
	State           StorageAvailabilityState `json:"state"`
	DurabilityKnown bool                     `json:"durabilityKnown"`
	Durable         bool                     `json:"durable"`
	CheckedAt       time.Time                `json:"checkedAt"`
	LastSuccessAt   time.Time                `json:"lastSuccessAt"`
	StaleAfter      time.Duration            `json:"staleAfterNanos"`
}

type HistoryAvailabilityState string
type HistoryAvailabilityReason string

const (
	HistoryUnknown      HistoryAvailabilityState = "unknown"
	HistoryDisabled     HistoryAvailabilityState = "disabled"
	HistoryUnsupported  HistoryAvailabilityState = "unsupported"
	HistoryInitializing HistoryAvailabilityState = "initializing"
	HistoryRecording    HistoryAvailabilityState = "recording"
	HistoryDegraded     HistoryAvailabilityState = "degraded"
	HistoryStopped      HistoryAvailabilityState = "stopped"

	HistoryNotConfigured   HistoryAvailabilityReason = "history_not_configured"
	HistoryRequiresTier2   HistoryAvailabilityReason = "history_requires_durable_storage"
	HistoryScopeUnknown    HistoryAvailabilityReason = "history_cluster_scope_unavailable"
	HistoryWriterMissing   HistoryAvailabilityReason = "history_writer_unavailable"
	HistoryStartPending    HistoryAvailabilityReason = "history_writer_not_started"
	HistoryWriterStopped   HistoryAvailabilityReason = "history_writer_stopped"
	HistoryQueueGap        HistoryAvailabilityReason = "history_queue_loss"
	HistoryWriteGap        HistoryAvailabilityReason = "history_write_gap"
	HistoryDeliveryPending HistoryAvailabilityReason = "history_delivery_pending"
	HistoryStorageUnknown  HistoryAvailabilityReason = "storage_state_unknown"
	HistoryStorageStale    HistoryAvailabilityReason = "storage_state_stale"
	HistoryStorageFailed   HistoryAvailabilityReason = "storage_unavailable"
)

// Recording means only that the metadata writer is running. RetentionEvidence
// separately reports cached reads of retained metadata. Configured retention
// and the process interval are NOT durable completeness evidence.
// No global counters are exposed as evidence for a selected member cluster.
type HistoryAvailability struct {
	State                HistoryAvailabilityState    `json:"state"`
	Reasons              []HistoryAvailabilityReason `json:"reasons"`
	ObservationStartedAt time.Time                   `json:"observationStartedAt"`
	Retention            time.Duration               `json:"retentionNanos"`
	Coverage             CoverageState               `json:"coverage"`
	Content              string                      `json:"content"`
	RetainedData         string                      `json:"retainedData"`
	RetentionEvidence    *HistoryRetentionEvidence   `json:"retentionEvidence,omitempty"`
}

// StorageAvailability copies only cached storage evidence. Waiting for the
// monitor lock observes the request deadline; this never invokes a probe.
func (m *Monitor) StorageAvailability(ctx context.Context) (StorageAvailability, error) {
	if err := ctx.Err(); err != nil {
		return StorageAvailability{}, err
	}
	if !m.mu.TryRLock() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for !m.mu.TryRLock() {
			select {
			case <-ctx.Done():
				return StorageAvailability{}, ctx.Err()
			case <-tick.C:
			}
		}
	}
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return StorageAvailability{}, err
	}
	s := m.snapshot
	out := StorageAvailability{State: StorageUnknown, DurabilityKnown: true, Durable: s.StorageDurable, CheckedAt: m.storageCheckedAt, LastSuccessAt: s.StorageLastSuccess, StaleAfter: s.StaleAfter}
	now := time.Now()
	if !s.StorageKnown || out.CheckedAt.IsZero() || out.CheckedAt.After(now) || out.LastSuccessAt.After(out.CheckedAt) {
		// Invalid/future probe times cannot be positive availability evidence.
		out.CheckedAt, out.LastSuccessAt = time.Time{}, time.Time{}
		return out, nil
	}
	switch {
	case now.Sub(out.CheckedAt) >= out.StaleAfter:
		out.State = StorageStale
	case s.StorageReachable:
		out.State = StorageAvailable
	default:
		out.State = StorageUnavailable
	}
	return out, nil
}
