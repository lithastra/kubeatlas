// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

type availabilityStorageFunc func(context.Context) (operations.StorageAvailability, error)

func (f availabilityStorageFunc) StorageAvailability(ctx context.Context) (operations.StorageAvailability, error) {
	return f(ctx)
}

func availableStorage(context.Context) (operations.StorageAvailability, error) {
	now := time.Now()
	return operations.StorageAvailability{State: operations.StorageAvailable, DurabilityKnown: true, Durable: true, CheckedAt: now, LastSuccessAt: now, StaleAfter: time.Minute}, nil
}

func requireAvailability(t *testing.T, reader *AnalysisAvailabilityReader, cluster string) operations.AnalysisAvailability {
	t.Helper()
	start := time.Now()
	out, err := reader.SnapshotAvailability(context.Background(), cluster)
	if err != nil || out.ClusterID != cluster || out.CapturedAt.Before(start) || out.CapturedAt.After(time.Now()) || out.History.Coverage == operations.CoverageObserved || out.History.RetainedData != "not_queried" || out.History.Content != "metadata_only" {
		t.Fatalf("availability overstated scope/history or failed: %+v %v", out, err)
	}
	return out
}

func TestAnalysisAvailabilityFeatureAndClusterModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     AnalysisAvailabilityConfig
		cluster string
		state   operations.HistoryAvailabilityState
		reason  operations.HistoryAvailabilityReason
	}{
		{"disabled", AnalysisAvailabilityConfig{}, "", operations.HistoryDisabled, operations.HistoryNotConfigured},
		{"memory_unsupported", AnalysisAvailabilityConfig{HistoryEnabled: true}, "", operations.HistoryUnsupported, operations.HistoryRequiresTier2},
		{"missing_writer", AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true}, "", operations.HistoryUnknown, operations.HistoryWriterMissing},
		{"federation", AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true}, "prod", operations.HistoryUnsupported, operations.HistoryScopeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewAnalysisAvailabilityReader(nil, nil, tc.cfg)
			out := requireAvailability(t, r, tc.cluster)
			if out.History.State != tc.state || !slices.Equal(out.History.Reasons, []operations.HistoryAvailabilityReason{tc.reason}) || out.History.Retention != 0 || out.Storage.State != operations.StorageUnknown {
				t.Fatalf("incorrect feature boundary: %+v", out)
			}
		})
	}
	// A writer for other members must not be read or disclosed. A zero Writer
	// has nil metrics and would panic if the unsupported branch sampled it.
	r := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), &Writer{}, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true})
	prod, other := requireAvailability(t, r, "prod"), requireAvailability(t, r, "other")
	if !reflect.DeepEqual(prod.History, other.History) || !prod.History.ObservationStartedAt.IsZero() {
		t.Fatal("member-scoped evidence used global writer observations")
	}
	blocked := availabilityStorageFunc(func(context.Context) (operations.StorageAvailability, error) {
		t.Fatal("invalid cluster scope reached storage evidence")
		return operations.StorageAvailability{}, nil
	})
	for _, cfg := range []AnalysisAvailabilityConfig{{}, {Federated: true}} {
		for _, cluster := range []string{"bad/name", "bad:name", "bad\x00name", strings.Repeat("a", 254)} {
			if out, err := NewAnalysisAvailabilityReader(blocked, nil, cfg).SnapshotAvailability(context.Background(), cluster); !errors.Is(err, ErrAnalysisAvailabilityScope) || !reflect.DeepEqual(out, operations.AnalysisAvailability{}) {
				t.Fatalf("invalid scope: %+v %v", out, err)
			}
		}
	}
	for _, tc := range []struct {
		cfg     AnalysisAvailabilityConfig
		cluster string
	}{{AnalysisAvailabilityConfig{}, "prod"}, {AnalysisAvailabilityConfig{Federated: true}, ""}} {
		if _, err := NewAnalysisAvailabilityReader(blocked, nil, tc.cfg).SnapshotAvailability(context.Background(), tc.cluster); !errors.Is(err, ErrAnalysisAvailabilityScope) {
			t.Fatal("standalone/federation scope was silently substituted")
		}
	}
}

type availabilityBlockingSink struct {
	entered chan struct{}
	release chan struct{}
}

func (s *availabilityBlockingSink) AppendEvent(context.Context, graph.ResourceEvent) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

func TestAnalysisAvailabilityWriterLifecycleAndDelivery(t *testing.T) {
	sink := &availabilityBlockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := New(sink, Config{Workers: 1, QueueSize: 2}, nil)
	r := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Retention: 48 * time.Hour})
	if h := requireAvailability(t, r, "").History; h.State != operations.HistoryInitializing || h.Retention != 48*time.Hour || !h.ObservationStartedAt.IsZero() {
		t.Fatalf("unstarted writer: %+v", h)
	}
	w.Start(context.Background())
	t.Cleanup(func() { close(sink.release); w.Stop() })
	if h := requireAvailability(t, r, "").History; h.State != operations.HistoryRecording || h.ObservationStartedAt.IsZero() || h.Coverage != operations.CoverageUnknown {
		t.Fatalf("running writer became retained-data completeness: %+v", h)
	}
	w.Enqueue(ev("synthetic-private-name"))
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach sink")
	}
	if w.QueueDepth() != 0 {
		t.Fatal("fixture must have an empty queue and an in-flight event")
	}
	out := requireAvailability(t, r, "")
	if !slices.Contains(out.History.Reasons, operations.HistoryDeliveryPending) {
		t.Fatal("empty queue hid an in-flight write")
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "synthetic-private-name") || strings.Contains(string(encoded), "eventsProcessed") {
		t.Fatal("availability exposed queued identity or global counters")
	}
	// Returned reason storage is owned; mutating it cannot change later reads.
	out.History.Reasons[0] = "changed"
	if slices.Contains(requireAvailability(t, r, "").History.Reasons, "changed") {
		t.Fatal("history evidence aliases caller-owned memory")
	}
}

func TestAnalysisAvailabilityGapsRemainAfterRecoveryAndStop(t *testing.T) {
	// Overflow before Start deterministically exercises the real drop-oldest
	// path without racing Enqueue against Stop (not a supported writer contract).
	w := New(&fakeSink{}, Config{Workers: 1, QueueSize: 1}, nil)
	w.Enqueue(ev("first"))
	w.Enqueue(ev("second"))
	r := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true})
	if h := requireAvailability(t, r, "").History; h.State != operations.HistoryDegraded || h.Coverage != operations.CoveragePartial || !slices.Contains(h.Reasons, operations.HistoryQueueGap) {
		t.Fatalf("queue loss became zero history: %+v", h)
	}
	w.Start(context.Background())
	w.Stop()
	h := requireAvailability(t, r, "").History
	if h.State != operations.HistoryStopped || !slices.Contains(h.Reasons, operations.HistoryQueueGap) || !slices.Contains(h.Reasons, operations.HistoryWriterStopped) || h.Retention != DefaultRetention {
		t.Fatalf("successful drain erased loss or mislabeled lifecycle: %+v", h)
	}
	failed := New(&fakeSink{failAll: true}, Config{Workers: 1}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Retains maxRetries but avoids waiting for retry backoffs.
	failed.Start(ctx)
	failed.Enqueue(ev("failed"))
	failed.Stop()
	r = NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), failed, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true})
	h = requireAvailability(t, r, "").History
	if h.State != operations.HistoryStopped || h.Coverage != operations.CoveragePartial || !slices.Contains(h.Reasons, operations.HistoryWriteGap) {
		t.Fatal("exhausted retries lost write-gap evidence")
	}
}

func TestAnalysisAvailabilityStorageWarningsAndCancellation(t *testing.T) {
	w := New(&fakeSink{}, Config{Workers: 1}, nil)
	w.Start(context.Background())
	defer w.Stop()
	for _, tc := range []struct {
		state  operations.StorageAvailabilityState
		reason operations.HistoryAvailabilityReason
	}{
		{operations.StorageUnknown, operations.HistoryStorageUnknown}, {operations.StorageStale, operations.HistoryStorageStale}, {operations.StorageUnavailable, operations.HistoryStorageFailed},
	} {
		r := NewAnalysisAvailabilityReader(availabilityStorageFunc(func(context.Context) (operations.StorageAvailability, error) {
			return operations.StorageAvailability{State: tc.state, DurabilityKnown: true, Durable: true}, nil
		}), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true})
		h := requireAvailability(t, r, "").History
		if !slices.Contains(h.Reasons, tc.reason) || h.Coverage != operations.CoverageUnknown || tc.state == operations.StorageUnavailable && h.State != operations.HistoryDegraded {
			t.Fatalf("storage probe confused with historical completeness: %+v", h)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := NewAnalysisAvailabilityReader(availabilityStorageFunc(func(ctx context.Context) (operations.StorageAvailability, error) {
		<-ctx.Done()
		return operations.StorageAvailability{}, ctx.Err()
	}), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true})
	if out, err := r.SnapshotAvailability(ctx, ""); !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(out, operations.AnalysisAvailability{}) {
		t.Fatalf("provider timeout returned partial evidence: %+v %v", out, err)
	}
}

func TestAnalysisAvailabilityRejectsInconsistentConfig(t *testing.T) {
	for _, cfg := range []AnalysisAvailabilityConfig{
		{Durable: true, Retention: -time.Second},
		{Durable: false, HistoryEnabled: true}, // Reader reports durable=true.
	} {
		r := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), nil, cfg)
		if out, err := r.SnapshotAvailability(context.Background(), ""); !errors.Is(err, ErrAnalysisAvailabilityConfig) || !reflect.DeepEqual(out, operations.AnalysisAvailability{}) {
			t.Fatalf("inconsistent configuration became history evidence: %+v %v", out, err)
		}
	}
}

func TestAnalysisAvailabilityCancelledWriterDoesNotClaimRecording(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := New(&fakeSink{}, Config{Workers: 1}, nil)
	w.Start(ctx)
	defer w.Stop()
	cancel() // Start's context ended, even though Stop has not drained yet.
	r := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true})
	h := requireAvailability(t, r, "").History
	if h.State != operations.HistoryStopped || !slices.Contains(h.Reasons, operations.HistoryWriterStopped) {
		t.Fatal("cancelled writer reported normal recording")
	}
}

func TestAnalysisAvailabilityCancellationStopsBeforeWriterRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	storage := availabilityStorageFunc(func(ctx context.Context) (operations.StorageAvailability, error) {
		cancel()
		return availableStorage(ctx) // A provider can finish successfully as cancellation arrives.
	})
	// The nil metric sink would panic if a cancelled request sampled the writer.
	r := NewAnalysisAvailabilityReader(storage, &Writer{}, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true})
	if out, err := r.SnapshotAvailability(ctx, ""); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(out, operations.AnalysisAvailability{}) {
		t.Fatalf("cancelled request continued reading process evidence: %+v %v", out, err)
	}
}
