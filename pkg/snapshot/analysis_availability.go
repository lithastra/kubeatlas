// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
)

var ErrAnalysisAvailabilityScope = errors.New("snapshot: invalid analysis availability scope")
var ErrAnalysisAvailabilityConfig = errors.New("snapshot: inconsistent analysis availability configuration")

type StorageAvailabilityReader interface {
	StorageAvailability(context.Context) (operations.StorageAvailability, error)
}

// AnalysisAvailabilityConfig is frozen at construction, like the writer's
// runtime configuration. Federation is explicit: writer metrics are still
// global and legacy events may lack ClusterID. Newly attributed events alone
// cannot establish a member's recording lifecycle or historical continuity.
type AnalysisAvailabilityConfig struct {
	Durable        bool
	HistoryEnabled bool
	Federated      bool
	Retention      time.Duration
}

type AnalysisAvailabilityReader struct {
	storage  StorageAvailabilityReader
	writer   *Writer
	cfg      AnalysisAvailabilityConfig
	retained *HistoryRetentionObserver
}

type writerObservation struct {
	startedAt time.Time
	done      <-chan struct{}
}

type AnalysisAvailabilityOption func(*AnalysisAvailabilityReader)

func WithHistoryRetention(observer *HistoryRetentionObserver) AnalysisAvailabilityOption {
	return func(r *AnalysisAvailabilityReader) { r.retained = observer }
}

func NewAnalysisAvailabilityReader(storage StorageAvailabilityReader, writer *Writer, cfg AnalysisAvailabilityConfig, opts ...AnalysisAvailabilityOption) *AnalysisAvailabilityReader {
	if cfg.Retention == 0 {
		cfg.Retention = DefaultRetention // Same effective value as NewRetainer.
	}
	r := &AnalysisAvailabilityReader{storage: storage, writer: writer, cfg: cfg}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// SnapshotAvailability reads cached process state only. It never reads events,
// backups, Kubernetes objects, or probes a database. Caller authorization must
// precede this method. A graph read, a storage probe, and retained history are
// separate facts; successful current reads cannot prove historical continuity.
func (r *AnalysisAvailabilityReader) SnapshotAvailability(ctx context.Context, cluster string) (operations.AnalysisAvailability, error) {
	if err := ctx.Err(); err != nil {
		return operations.AnalysisAvailability{}, err
	}
	if len(cluster) > 253 || strings.ContainsAny(cluster, "/:\x00") || r.cfg.Federated && cluster == "" || !r.cfg.Federated && cluster != "" {
		return operations.AnalysisAvailability{}, ErrAnalysisAvailabilityScope
	}
	if r.cfg.Retention < 0 {
		return operations.AnalysisAvailability{}, ErrAnalysisAvailabilityConfig
	}
	storage := operations.StorageAvailability{State: operations.StorageUnknown, DurabilityKnown: true, Durable: r.cfg.Durable}
	if r.storage != nil {
		var err error
		storage, err = r.storage.StorageAvailability(ctx)
		if ctx.Err() != nil {
			return operations.AnalysisAvailability{}, ctx.Err()
		}
		if err != nil {
			return operations.AnalysisAvailability{}, err
		}
		if !storage.DurabilityKnown || storage.Durable != r.cfg.Durable {
			return operations.AnalysisAvailability{}, ErrAnalysisAvailabilityConfig
		}
	}
	history := operations.HistoryAvailability{
		State: operations.HistoryUnknown, Reasons: []operations.HistoryAvailabilityReason{},
		Coverage: operations.CoverageUnknown, Content: "metadata_only", RetainedData: "not_queried",
	}
	switch {
	case !r.cfg.HistoryEnabled:
		history.State = operations.HistoryDisabled
		history.Reasons = append(history.Reasons, operations.HistoryNotConfigured)
	case !r.cfg.Durable:
		history.State = operations.HistoryUnsupported
		history.Reasons = append(history.Reasons, operations.HistoryRequiresTier2)
	case r.cfg.Federated:
		history.State = operations.HistoryUnsupported
		history.Reasons = append(history.Reasons, operations.HistoryScopeUnknown)
	case r.writer == nil:
		history.Reasons = append(history.Reasons, operations.HistoryWriterMissing)
	default:
		history = r.writer.historyAvailability(r.cfg.Retention)
		if r.retained != nil {
			if r.retained.retention != r.cfg.Retention {
				return operations.AnalysisAvailability{}, ErrAnalysisAvailabilityConfig
			}
			retained, err := r.retained.SnapshotRetention(ctx, cluster)
			if err != nil {
				return operations.AnalysisAvailability{}, err
			}
			history.RetentionEvidence = &retained
			switch retained.State {
			case operations.RetentionObserved:
				history.RetainedData = "observed"
			case operations.RetentionFailed, operations.RetentionUnsupported:
				history.RetainedData = "unavailable"
			}
		}
		switch storage.State {
		case operations.StorageUnknown:
			history.Reasons = append(history.Reasons, operations.HistoryStorageUnknown)
		case operations.StorageStale:
			history.Reasons = append(history.Reasons, operations.HistoryStorageStale)
		case operations.StorageUnavailable:
			history.Reasons = append(history.Reasons, operations.HistoryStorageFailed)
			if history.State == operations.HistoryRecording {
				history.State = operations.HistoryDegraded
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return operations.AnalysisAvailability{}, err
	}
	captured := time.Now()
	if retained := history.RetentionEvidence; retained != nil {
		retained.Stale = !retained.CheckedAt.IsZero() && captured.Sub(retained.CheckedAt) >= operations.HistoryRetentionMaxAge
	}
	return operations.AnalysisAvailability{ClusterID: cluster, CapturedAt: captured, Storage: storage, History: history}, nil
}

func (w *Writer) historyAvailability(retention time.Duration) operations.HistoryAvailability {
	out := operations.HistoryAvailability{
		State: operations.HistoryInitializing, Reasons: []operations.HistoryAvailabilityReason{},
		Retention: retention, Coverage: operations.CoverageUnknown, Content: "metadata_only", RetainedData: "not_queried",
	}
	lifecycle := w.observation.Load()
	if lifecycle != nil {
		out.ObservationStartedAt = lifecycle.startedAt
		out.State = operations.HistoryRecording
	} else {
		out.Reasons = append(out.Reasons, operations.HistoryStartPending)
	}
	metrics := w.metrics.Snapshot()
	if metrics.QueueDropped > 0 {
		out.Reasons = append(out.Reasons, operations.HistoryQueueGap)
	}
	if metrics.WriteFailed > 0 {
		out.Reasons = append(out.Reasons, operations.HistoryWriteGap)
	}
	if metrics.QueueDropped > 0 || metrics.WriteFailed > 0 {
		out.State, out.Coverage = operations.HistoryDegraded, operations.CoveragePartial
	}
	// Queue depth alone misses an event already taken by a blocked worker.
	// This is only a pending-delivery hint, never a zero-backlog guarantee.
	if w.QueueDepth() > 0 || w.inFlight.Load() > 0 {
		out.Reasons = append(out.Reasons, operations.HistoryDeliveryPending)
	}
	stopped := w.closed.Load()
	if lifecycle != nil {
		select {
		case <-lifecycle.done:
			stopped = true
		default:
		}
	}
	if stopped {
		out.State = operations.HistoryStopped
		out.Reasons = append(out.Reasons, operations.HistoryWriterStopped)
	}
	return out
}
