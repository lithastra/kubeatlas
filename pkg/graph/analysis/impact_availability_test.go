// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

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
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type impactAvailabilityFunc func(context.Context, string) (operations.AnalysisAvailability, error)

func (f impactAvailabilityFunc) SnapshotAvailability(ctx context.Context, cluster string) (operations.AnalysisAvailability, error) {
	return f(ctx, cluster)
}

type impactStorageAvailabilityFunc func(context.Context) (operations.StorageAvailability, error)

func (f impactStorageAvailabilityFunc) StorageAvailability(ctx context.Context) (operations.StorageAvailability, error) {
	return f(ctx)
}

func impactAvailableStorage(context.Context) (operations.StorageAvailability, error) {
	now := time.Now()
	return operations.StorageAvailability{State: operations.StorageAvailable, DurabilityKnown: true, Durable: true, CheckedAt: now, LastSuccessAt: now, StaleAfter: time.Minute}, nil
}

type impactDiscardEventSink struct{}

func (impactDiscardEventSink) AppendEvent(context.Context, graph.ResourceEvent) error { return nil }

func TestImpactAvailabilityCurrentGraphAndHistoryRemainIndependent(t *testing.T) {
	for _, mode := range []string{"provider_missing", "disabled", "memory", "writer_missing", "initializing", "recording", "storage_stale", "storage_failed", "stopped", "queue_gap"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := authorizationFixture()
			g.Resources, g.Edges = g.Resources[:1], nil
			reads := 0
			store := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { reads++; return g, nil }}
			coverage := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, coverage, "", observedImpactTypes(true))
			cfg := snapshot.AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Retention: 72 * time.Hour}
			storage := impactStorageAvailabilityFunc(impactAvailableStorage)
			writer := snapshot.New(impactDiscardEventSink{}, snapshot.Config{Workers: 1, QueueSize: 1}, nil)
			switch mode {
			case "disabled":
				cfg.HistoryEnabled = false
			case "memory":
				cfg.Durable = false
				storage = func(ctx context.Context) (operations.StorageAvailability, error) {
					s, err := impactAvailableStorage(ctx)
					s.Durable = false
					return s, err
				}
			case "writer_missing":
				writer = nil
			case "recording", "storage_stale", "storage_failed", "stopped":
				writer.Start(ctx)
				defer writer.Stop()
				if mode == "stopped" {
					writer.Stop()
				}
			case "queue_gap":
				writer.Enqueue(graph.ResourceEvent{Name: "synthetic-private-first"})
				writer.Enqueue(graph.ResourceEvent{Name: "synthetic-private-second"})
			}
			if mode == "storage_stale" {
				storage = func(ctx context.Context) (operations.StorageAvailability, error) {
					s, err := impactAvailableStorage(ctx)
					s.CheckedAt = s.CheckedAt.Add(-time.Hour)
					s.LastSuccessAt = s.CheckedAt
					s.State = operations.StorageStale
					return s, err
				}
			}
			if mode == "storage_failed" {
				storage = func(ctx context.Context) (operations.StorageAvailability, error) {
					s, err := impactAvailableStorage(ctx)
					s.LastSuccessAt = s.CheckedAt.Add(-time.Minute)
					s.State = operations.StorageUnavailable
					return s, err
				}
			}
			var availability analysis.ImpactAvailabilityProvider = snapshot.NewAnalysisAvailabilityReader(storage, writer, cfg)
			if mode == "provider_missing" {
				availability = nil
			}
			result, err := analysis.AnalyzeImpactWithEvidence(ctx, store, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Coverage: coverage, Availability: availability})
			if err != nil || result.Availability == nil || result.Availability.CurrentGraphRead != "succeeded" || reads != 1 {
				t.Fatalf("availability added graph reads or hid successful projection: %+v %v reads=%d", result, err, reads)
			}
			history := result.Availability.Evidence.History
			want := map[string]operations.HistoryAvailabilityState{
				"provider_missing": operations.HistoryUnknown, "disabled": operations.HistoryDisabled,
				"memory": operations.HistoryUnsupported, "writer_missing": operations.HistoryUnknown,
				"initializing": operations.HistoryInitializing, "recording": operations.HistoryRecording,
				"storage_stale": operations.HistoryRecording, "storage_failed": operations.HistoryDegraded,
				"stopped": operations.HistoryStopped, "queue_gap": operations.HistoryDegraded,
			}[mode]
			if history.State != want || history.RetainedData != "not_queried" || history.Content != "metadata_only" || history.Coverage == operations.CoverageObserved {
				t.Fatalf("history state overclaimed data/completeness: %+v", history)
			}
			if result.Observation.Authorization.State != operations.CoverageObserved || result.Observation.Authorization.EmptyResultAssessment != "none_found_in_analyzed_scope" || result.Authorization.Counts.Total != 0 {
				t.Fatal("unavailable history contaminated independent current graph/watch evidence")
			}
			encoded, _ := json.Marshal(result.Availability)
			if strings.Contains(string(encoded), "synthetic-private") {
				t.Fatal("queued event identity leaked")
			}
			if mode == "provider_missing" && result.Availability.Evidence.Storage.DurabilityKnown {
				t.Fatal("missing provider inferred a storage tier")
			}
			plain, err := analysis.AnalyzeStoredImpact(ctx, store, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{})
			if err != nil || plain.Availability != nil || plain.Observation != nil {
				t.Fatal("M1-only seam acquired evidence or changed output")
			}
		})
	}
}

func validAvailabilityEvidence(cluster string) operations.AnalysisAvailability {
	s, _ := impactAvailableStorage(context.Background())
	return operations.AnalysisAvailability{ClusterID: cluster, CapturedAt: time.Now(), Storage: s, History: operations.HistoryAvailability{
		State: operations.HistoryRecording, ObservationStartedAt: s.CheckedAt.Add(-time.Minute), Retention: time.Hour,
		Coverage: operations.CoverageUnknown, Content: "metadata_only", RetainedData: "not_queried", Reasons: []operations.HistoryAvailabilityReason{},
	}}
}

func TestImpactAvailabilityRejectsInvalidEvidenceAndSanitizesErrors(t *testing.T) {
	mutations := map[string]func(*operations.AnalysisAvailability){
		"wrong_cluster":         func(e *operations.AnalysisAvailability) { e.ClusterID = "hidden" },
		"cached_capture":        func(e *operations.AnalysisAvailability) { e.CapturedAt = e.CapturedAt.Add(-time.Minute) },
		"future_capture":        func(e *operations.AnalysisAvailability) { e.CapturedAt = e.CapturedAt.Add(time.Hour) },
		"unknown_storage_state": func(e *operations.AnalysisAvailability) { e.Storage.State = "synthetic-sensitive" },
		"future_check":          func(e *operations.AnalysisAvailability) { e.Storage.CheckedAt = e.CapturedAt.Add(time.Hour) },
		"stale_claims_available": func(e *operations.AnalysisAvailability) {
			e.Storage.CheckedAt = e.CapturedAt.Add(-time.Hour)
			e.Storage.LastSuccessAt = e.Storage.CheckedAt
		},
		"unknown_durability":      func(e *operations.AnalysisAvailability) { e.Storage.DurabilityKnown = false },
		"missing_last_success":    func(e *operations.AnalysisAvailability) { e.Storage.LastSuccessAt = time.Time{} },
		"unknown_history_state":   func(e *operations.AnalysisAvailability) { e.History.State = "available" },
		"claims_complete_history": func(e *operations.AnalysisAvailability) { e.History.Coverage = operations.CoverageObserved },
		"claims_history_queried":  func(e *operations.AnalysisAvailability) { e.History.RetainedData = "empty" },
		"claims_full_history":     func(e *operations.AnalysisAvailability) { e.History.Content = "full_object" },
		"raw_reason": func(e *operations.AnalysisAvailability) {
			e.History.Reasons = []operations.HistoryAvailabilityReason{"synthetic-sensitive"}
		},
		"duplicate_reason": func(e *operations.AnalysisAvailability) {
			e.History.Reasons = []operations.HistoryAvailabilityReason{operations.HistoryDeliveryPending, operations.HistoryDeliveryPending}
		},
		"contradictory_reason": func(e *operations.AnalysisAvailability) {
			e.History.Reasons = []operations.HistoryAvailabilityReason{operations.HistoryNotConfigured}
		},
		"claims_running_without_start": func(e *operations.AnalysisAvailability) { e.History.ObservationStartedAt = time.Time{} },
		"future_writer_start":          func(e *operations.AnalysisAvailability) { e.History.ObservationStartedAt = e.CapturedAt.Add(time.Hour) },
		"negative_retention":           func(e *operations.AnalysisAvailability) { e.History.Retention = -1 },
		"gap_hidden_by_coverage": func(e *operations.AnalysisAvailability) {
			e.History.State = operations.HistoryDegraded
			e.History.Reasons = []operations.HistoryAvailabilityReason{operations.HistoryWriteGap}
		},
		"memory_recording": func(e *operations.AnalysisAvailability) { e.Storage.Durable = false },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			g := authorizationFixture()
			probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { return g, nil }}
			provider := impactAvailabilityFunc(func(_ context.Context, cluster string) (operations.AnalysisAvailability, error) {
				e := validAvailabilityEvidence(cluster)
				mutate(&e)
				return e, nil
			})
			result, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Availability: provider})
			if result != nil || !errors.Is(err, analysis.ErrImpactAvailability) || strings.Contains(err.Error(), "synthetic-sensitive") {
				t.Fatalf("invalid evidence returned data or leaked error: %+v %v", result, err)
			}
		})
	}
	g := authorizationFixture()
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { return g, nil }}
	provider := impactAvailabilityFunc(func(context.Context, string) (operations.AnalysisAvailability, error) {
		return operations.AnalysisAvailability{}, errors.New("synthetic-sensitive-provider-error")
	})
	result, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Availability: provider})
	if result != nil || !errors.Is(err, analysis.ErrImpactAvailability) || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatalf("provider error leaked: %+v %v", result, err)
	}
}

func TestImpactAvailabilityOrderingDeadlineScopeAndOwnership(t *testing.T) {
	g := authorizationFixture()
	coverage := operations.NewCoverageTracker(time.Minute)
	readyImpactObservation(t, coverage, "", observedImpactTypes(true))
	var order []string
	var deadline time.Time
	check := func(ctx context.Context) {
		got, ok := ctx.Deadline()
		if !ok || time.Until(got) > graph.ImpactReadTimeout {
			t.Fatal("missing shared deadline")
		}
		if deadline.IsZero() {
			deadline = got
		} else if !deadline.Equal(got) {
			t.Fatal("evidence reset total budget")
		}
	}
	cov := impactCoverageFunc(func(ctx context.Context, cluster string, required []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
		check(ctx)
		order = append(order, "coverage")
		return coverage.SnapshotCoverage(ctx, cluster, required)
	})
	probe := &impactSnapshotProbe{read: func(ctx context.Context, _ graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		check(ctx)
		order = append(order, "graph")
		return g, nil
	}}
	var retained operations.AnalysisAvailability
	provider := impactAvailabilityFunc(func(ctx context.Context, cluster string) (operations.AnalysisAvailability, error) {
		check(ctx)
		order = append(order, "availability")
		retained = validAvailabilityEvidence(cluster)
		retained.History.Reasons = []operations.HistoryAvailabilityReason{operations.HistoryDeliveryPending}
		return retained, nil
	})
	result, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Coverage: cov, Availability: provider})
	if err != nil || !slices.Equal(order, []string{"coverage", "graph", "coverage", "availability"}) {
		t.Fatalf("read order: %v %v", order, err)
	}
	result.Availability.Evidence.History.Reasons[0] = "changed"
	if retained.History.Reasons[0] != operations.HistoryDeliveryPending {
		t.Fatal("result mutated provider-owned reason storage")
	}
	// Invalid options fail before every evidence read, and a replaced root does
	// not acquire availability evidence after the graph identity check fails.
	for _, opts := range []analysis.ImpactOptions{{Limit: -1}, {ExpectedUID: "different"}} {
		deadline, order = time.Time{}, nil
		result, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), opts, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Coverage: cov, Availability: provider})
		if err == nil || result != nil || slices.Contains(order, "availability") || opts.Limit == -1 && len(order) != 0 {
			t.Fatalf("invalid request accessed availability: %v %v", order, err)
		}
	}
	deadline, order = time.Time{}, nil
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	provider = func(ctx context.Context, _ string) (operations.AnalysisAvailability, error) {
		check(ctx)
		<-ctx.Done()
		return operations.AnalysisAvailability{}, ctx.Err()
	}
	result, err = analysis.AnalyzeImpactWithEvidence(ctx, probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Availability: provider})
	if result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out availability returned partial success: %+v %v", result, err)
	}
	// Explicit member scope reaches the reader but cannot expose global writer
	// state or counters as that member's history evidence.
	for i := range g.Resources {
		g.Resources[i].ClusterID = "prod"
	}
	g.Edges = nil
	deadline = time.Time{}
	r := snapshot.NewAnalysisAvailabilityReader(impactStorageAvailabilityFunc(impactAvailableStorage), &snapshot.Writer{}, snapshot.AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true})
	result, err = analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{ClusterID: "prod"}, graph.ImpactSnapshotOptions{ClusterID: "prod"}, analysis.ImpactEvidenceProviders{Availability: r})
	if err != nil || result.Availability.Evidence.ClusterID != "prod" || result.Availability.Evidence.History.State != operations.HistoryUnsupported || !reflect.DeepEqual(result.Availability.Evidence.History.Reasons, []operations.HistoryAvailabilityReason{operations.HistoryScopeUnknown}) {
		t.Fatalf("member history scope: %+v %v", result, err)
	}
}
