// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

func retainedAvailabilityEvidence(cluster string) operations.AnalysisAvailability {
	e := validAvailabilityEvidence(cluster)
	now := e.CapturedAt
	e.History.RetainedData = "observed"
	e.History.RetentionEvidence = &operations.HistoryRetentionEvidence{State: operations.RetentionObserved, CheckedAt: now,
		Bounds: graph.HistoryBounds{ClusterID: cluster, From: now.Add(-e.History.Retention), To: now, FirstEventAt: now.Add(-time.Minute), LastEventAt: now, LatestMarkerAt: now}}
	return e
}

func retainedMemberEvidence(cluster string) operations.AnalysisAvailability {
	e := retainedAvailabilityEvidence(cluster)
	e.History.State = operations.HistoryUnsupported
	e.History.Reasons = []operations.HistoryAvailabilityReason{operations.HistoryScopeUnknown}
	e.History.ObservationStartedAt = time.Time{}
	return e
}

func TestImpactMemberRetentionRequiresExactScopeWithoutWriterQualification(t *testing.T) {
	g := authorizationFixture()
	g.Resources, g.Edges = g.Resources[:1], nil
	g.Resources[0].ClusterID = "prod"
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { return g, nil }}
	for name, mutate := range map[string]func(*operations.AnalysisAvailability){
		"valid scoped metadata": func(*operations.AnalysisAvailability) {},
		"standalone metadata":   func(e *operations.AnalysisAvailability) { e.History.RetentionEvidence.Bounds.ClusterID = "" },
		"hidden member":         func(e *operations.AnalysisAvailability) { e.History.RetentionEvidence.Bounds.ClusterID = "hidden" },
		"borrowed writer lifecycle": func(e *operations.AnalysisAvailability) {
			e.History.State = operations.HistoryRecording
			e.History.Reasons = nil
			e.History.ObservationStartedAt = e.CapturedAt.Add(-time.Minute)
		},
		"borrowed observation start": func(e *operations.AnalysisAvailability) {
			e.History.ObservationStartedAt = e.CapturedAt.Add(-time.Minute)
		},
		"complete history claim": func(e *operations.AnalysisAvailability) { e.History.Coverage = operations.CoverageObserved },
		"memory metadata":        func(e *operations.AnalysisAvailability) { e.Storage.Durable = false },
		"unknown durability": func(e *operations.AnalysisAvailability) {
			e.Storage = operations.StorageAvailability{State: operations.StorageUnknown}
		},
		"wrong retention window": func(e *operations.AnalysisAvailability) {
			e.History.RetentionEvidence.Bounds.From = e.CapturedAt.Add(-2 * time.Hour)
		},
		"unsupported without metadata": func(e *operations.AnalysisAvailability) {
			e.History.RetentionEvidence = nil
			e.History.RetainedData = "not_queried"
		},
	} {
		t.Run(name, func(t *testing.T) {
			var original operations.AnalysisAvailability
			provider := impactAvailabilityFunc(func(_ context.Context, cluster string) (operations.AnalysisAvailability, error) {
				original = retainedMemberEvidence(cluster)
				mutate(&original)
				return original, nil
			})
			out, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{ClusterID: "prod"}, graph.ImpactSnapshotOptions{ClusterID: "prod"}, analysis.ImpactEvidenceProviders{Availability: provider})
			if name != "valid scoped metadata" {
				if !errors.Is(err, analysis.ErrImpactAvailability) || out != nil {
					t.Fatalf("invalid member evidence accepted: %+v %v", out, err)
				}
				return
			}
			if err != nil || out.Availability.Evidence.History.State != operations.HistoryUnsupported || out.Availability.Evidence.History.Coverage != operations.CoverageUnknown || out.Observation.Ordinary.TypeScopeClosed {
				t.Fatalf("retained metadata qualified complete history: %+v %v", out, err)
			}
			out.Availability.Evidence.History.RetentionEvidence.Bounds.ClusterID = "changed"
			if original.History.RetentionEvidence.Bounds.ClusterID != "prod" {
				t.Fatal("member response aliases provider storage")
			}
		})
	}
}

func TestImpactRetentionEvidenceScopeValidationAndOwnership(t *testing.T) {
	g := authorizationFixture()
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { return g, nil }}
	for name, mutate := range map[string]func(*operations.AnalysisAvailability){
		"hidden cluster": func(e *operations.AnalysisAvailability) {
			e.History.RetentionEvidence.Bounds.ClusterID = "synthetic-hidden-cluster"
		},
		"wrong retention": func(e *operations.AnalysisAvailability) {
			e.History.RetentionEvidence.Bounds.From = e.CapturedAt.Add(-24 * time.Hour)
		},
		"claimed current despite stale check": func(e *operations.AnalysisAvailability) {
			e.History.RetentionEvidence.CheckedAt = e.CapturedAt.Add(-time.Hour)
		},
		"raw state": func(e *operations.AnalysisAvailability) { e.History.RetentionEvidence.State = "synthetic-sensitive" },
		"missing event endpoint": func(e *operations.AnalysisAvailability) {
			e.History.RetentionEvidence.Bounds.FirstEventAt = time.Time{}
		},
		"failed with retained records": func(e *operations.AnalysisAvailability) {
			e.History.RetentionEvidence.State = operations.RetentionFailed
			e.History.RetainedData = "unavailable"
		},
		"claimed unqueried despite observed data": func(e *operations.AnalysisAvailability) { e.History.RetainedData = "not_queried" },
		"disabled with retained data": func(e *operations.AnalysisAvailability) {
			e.History.State, e.History.Retention = operations.HistoryDisabled, 0
			e.History.ObservationStartedAt = time.Time{}
			e.History.Reasons = []operations.HistoryAvailabilityReason{operations.HistoryNotConfigured}
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider := impactAvailabilityFunc(func(_ context.Context, cluster string) (operations.AnalysisAvailability, error) {
				e := retainedAvailabilityEvidence(cluster)
				mutate(&e)
				return e, nil
			})
			out, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Availability: provider})
			if !errors.Is(err, analysis.ErrImpactAvailability) || out != nil || strings.Contains(err.Error(), "synthetic") {
				t.Fatal("invalid retained evidence was accepted or disclosed")
			}
		})
	}
	var original operations.AnalysisAvailability
	provider := impactAvailabilityFunc(func(_ context.Context, cluster string) (operations.AnalysisAvailability, error) {
		original = retainedAvailabilityEvidence(cluster)
		return original, nil
	})
	out, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Availability: provider})
	if err != nil || out.Availability.Evidence.History.Coverage != operations.CoverageUnknown || out.Observation.Ordinary.TypeScopeClosed {
		t.Fatalf("retained timestamps qualified complete observation: %v", err)
	}
	out.Availability.Evidence.History.RetentionEvidence.Bounds.ClusterID = "changed"
	if original.History.RetentionEvidence.Bounds.ClusterID != "" {
		t.Fatal("response aliases provider-owned retention evidence")
	}
}

func TestImpactRetentionEmptyStaleAndFailedRemainIndependentOfCurrentGraph(t *testing.T) {
	g := authorizationFixture()
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { return g, nil }}
	for _, state := range []string{"empty", "stale", "failed", "stopped"} {
		t.Run(state, func(t *testing.T) {
			provider := impactAvailabilityFunc(func(_ context.Context, cluster string) (operations.AnalysisAvailability, error) {
				e := retainedAvailabilityEvidence(cluster)
				h := e.History.RetentionEvidence
				h.Bounds.FirstEventAt, h.Bounds.LastEventAt, h.Bounds.LatestMarkerAt = time.Time{}, time.Time{}, time.Time{}
				switch state {
				case "stale":
					h.CheckedAt = h.CheckedAt.Add(-time.Hour)
					h.Bounds.From, h.Bounds.To = h.Bounds.From.Add(-time.Hour), h.Bounds.To.Add(-time.Hour)
					h.Stale = true
				case "failed":
					h.State, e.History.RetainedData = operations.RetentionFailed, "unavailable"
				case "stopped":
					h.Stopped = true
				}
				return e, nil
			})
			out, err := analysis.AnalyzeImpactWithEvidence(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{Availability: provider})
			if err != nil || out.Availability.CurrentGraphRead != "succeeded" || out.Availability.Evidence.History.Coverage != operations.CoverageUnknown {
				t.Fatalf("retention state replaced independent current graph result: %v", err)
			}
		})
	}
}
