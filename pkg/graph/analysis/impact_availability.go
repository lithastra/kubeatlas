// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
)

var ErrImpactAvailability = errors.New("impact: invalid availability evidence")

type ImpactAvailabilityProvider interface {
	SnapshotAvailability(context.Context, string) (operations.AnalysisAvailability, error)
}

type ImpactEvidenceProviders struct {
	Coverage     ImpactCoverageProvider
	Availability ImpactAvailabilityProvider
}

// A completed graph projection and a recent background probe are independent
// observations. Neither proves retained history, lossless recording, a backup,
// or historical topology. No history counts or payloads are included here.
type ImpactAvailability struct {
	CurrentGraphRead string                          `json:"currentGraphRead"`
	Evidence         operations.AnalysisAvailability `json:"evidence"`
}

func readImpactAvailability(ctx context.Context, provider ImpactAvailabilityProvider, cluster string) (operations.AnalysisAvailability, error) {
	if err := ctx.Err(); err != nil {
		return operations.AnalysisAvailability{}, err
	}
	start := time.Now()
	if provider == nil {
		return operations.AnalysisAvailability{
			ClusterID: cluster, CapturedAt: start,
			Storage: operations.StorageAvailability{State: operations.StorageUnknown},
			History: operations.HistoryAvailability{State: operations.HistoryUnknown, Reasons: []operations.HistoryAvailabilityReason{}, Coverage: operations.CoverageUnknown, Content: "metadata_only", RetainedData: "not_queried"},
		}, nil
	}
	evidence, err := provider.SnapshotAvailability(ctx, cluster)
	if ctx.Err() != nil {
		return operations.AnalysisAvailability{}, ctx.Err()
	}
	if err != nil || evidence.ClusterID != cluster || evidence.CapturedAt.Before(start) || evidence.CapturedAt.After(time.Now()) || !validImpactStorage(evidence.Storage, evidence.CapturedAt) || !validImpactHistory(evidence.History, evidence.CapturedAt) {
		// Closed error text, never underlying provider errors or returned data.
		return operations.AnalysisAvailability{}, ErrImpactAvailability
	}
	history := evidence.History
	active := history.State == operations.HistoryInitializing || history.State == operations.HistoryRecording || history.State == operations.HistoryDegraded || history.State == operations.HistoryStopped
	switch {
	case active && (!evidence.Storage.DurabilityKnown || !evidence.Storage.Durable),
		slices.Contains(history.Reasons, operations.HistoryRequiresTier2) && (!evidence.Storage.DurabilityKnown || evidence.Storage.Durable),
		slices.Contains(history.Reasons, operations.HistoryScopeUnknown) && cluster == "",
		history.State == operations.HistoryRecording && evidence.Storage.State == operations.StorageUnavailable:
		return operations.AnalysisAvailability{}, ErrImpactAvailability
	}
	evidence.History.Reasons = slices.Clone(evidence.History.Reasons)
	if h := evidence.History.RetentionEvidence; h != nil {
		// A selected member must not inherit the standalone cache or global
		// writer evidence. Member retention integration is not enabled yet.
		if cluster != "" || !active || !h.ValidFor(cluster, history.Retention, evidence.CapturedAt) {
			return operations.AnalysisAvailability{}, ErrImpactAvailability
		}
		owned := *h
		evidence.History.RetentionEvidence = &owned
	}
	return evidence, nil
}

func validImpactStorage(s operations.StorageAvailability, captured time.Time) bool {
	if s.CheckedAt.After(captured) || s.LastSuccessAt.After(s.CheckedAt) || s.StaleAfter < 0 || !s.DurabilityKnown && (s.Durable || s.State != operations.StorageUnknown) {
		return false
	}
	switch s.State {
	case operations.StorageUnknown:
		return s.CheckedAt.IsZero() && s.LastSuccessAt.IsZero()
	case operations.StorageAvailable:
		return !s.CheckedAt.IsZero() && s.LastSuccessAt.Equal(s.CheckedAt) && s.StaleAfter > 0 && captured.Sub(s.CheckedAt) < s.StaleAfter
	case operations.StorageUnavailable:
		return !s.CheckedAt.IsZero() && s.StaleAfter > 0
	case operations.StorageStale:
		return !s.CheckedAt.IsZero() && s.StaleAfter > 0 && captured.Sub(s.CheckedAt) >= s.StaleAfter
	default:
		return false
	}
}

func validImpactHistory(h operations.HistoryAvailability, captured time.Time) bool {
	retainedData := "not_queried"
	if h.RetentionEvidence != nil {
		switch h.RetentionEvidence.State {
		case operations.RetentionObserved:
			retainedData = "observed"
		case operations.RetentionFailed, operations.RetentionUnsupported:
			retainedData = "unavailable"
		}
	}
	if h.ObservationStartedAt.After(captured) || h.Retention < 0 || h.Content != "metadata_only" || h.RetainedData != retainedData || h.Coverage != operations.CoverageUnknown && h.Coverage != operations.CoveragePartial {
		return false
	}
	known := []operations.HistoryAvailabilityReason{
		operations.HistoryNotConfigured, operations.HistoryRequiresTier2, operations.HistoryScopeUnknown,
		operations.HistoryWriterMissing, operations.HistoryStartPending, operations.HistoryWriterStopped,
		operations.HistoryQueueGap, operations.HistoryWriteGap, operations.HistoryDeliveryPending,
		operations.HistoryStorageUnknown, operations.HistoryStorageStale, operations.HistoryStorageFailed,
	}
	if len(h.Reasons) > len(known) {
		return false
	}
	for i, reason := range h.Reasons {
		if !slices.Contains(known, reason) || slices.Contains(h.Reasons[:i], reason) {
			return false
		}
		switch reason {
		case operations.HistoryNotConfigured:
			if h.State != operations.HistoryDisabled {
				return false
			}
		case operations.HistoryRequiresTier2, operations.HistoryScopeUnknown:
			if h.State != operations.HistoryUnsupported {
				return false
			}
		case operations.HistoryWriterMissing:
			if h.State != operations.HistoryUnknown {
				return false
			}
		case operations.HistoryStartPending:
			if !h.ObservationStartedAt.IsZero() || h.State != operations.HistoryInitializing && h.State != operations.HistoryDegraded && h.State != operations.HistoryStopped {
				return false
			}
		case operations.HistoryWriterStopped:
			if h.State != operations.HistoryStopped {
				return false
			}
		case operations.HistoryQueueGap, operations.HistoryWriteGap:
			if h.State != operations.HistoryDegraded && h.State != operations.HistoryStopped {
				return false
			}
		default:
			if h.State == operations.HistoryUnknown || h.State == operations.HistoryDisabled || h.State == operations.HistoryUnsupported {
				return false
			}
		}
	}
	gap := slices.Contains(h.Reasons, operations.HistoryQueueGap) || slices.Contains(h.Reasons, operations.HistoryWriteGap)
	if gap != (h.Coverage == operations.CoveragePartial) {
		return false
	}
	switch h.State {
	case operations.HistoryUnknown:
		return h.ObservationStartedAt.IsZero() && h.Coverage == operations.CoverageUnknown
	case operations.HistoryDisabled:
		return h.ObservationStartedAt.IsZero() && h.Retention == 0 && slices.Equal(h.Reasons, []operations.HistoryAvailabilityReason{operations.HistoryNotConfigured})
	case operations.HistoryUnsupported:
		return h.ObservationStartedAt.IsZero() && h.Retention == 0 && len(h.Reasons) == 1 && (h.Reasons[0] == operations.HistoryRequiresTier2 || h.Reasons[0] == operations.HistoryScopeUnknown)
	case operations.HistoryInitializing:
		return h.ObservationStartedAt.IsZero() && h.Retention > 0 && !gap && slices.Contains(h.Reasons, operations.HistoryStartPending)
	case operations.HistoryRecording:
		return !h.ObservationStartedAt.IsZero() && h.Retention > 0 && !gap && !slices.Contains(h.Reasons, operations.HistoryWriterStopped) && !slices.Contains(h.Reasons, operations.HistoryStartPending) && !slices.Contains(h.Reasons, operations.HistoryStorageFailed)
	case operations.HistoryDegraded:
		return h.Retention > 0 && (gap || slices.Contains(h.Reasons, operations.HistoryStorageFailed))
	case operations.HistoryStopped:
		return h.Retention > 0 && slices.Contains(h.Reasons, operations.HistoryWriterStopped)
	default:
		return false
	}
}
