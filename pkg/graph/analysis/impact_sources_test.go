// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var impactSourceGVR = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}

func readyImpactSource(t *testing.T, tracker *operations.CoverageTracker, cluster string, name operations.CoverageSource) (*operations.CoverageSession, *operations.CoverageSession) {
	t.Helper()
	now := time.Now()
	source, err := tracker.BeginSource(cluster, name, now)
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.BeginType(impactSourceGVR, now)
	if err != nil {
		t.Fatal(err)
	}
	token.ListResult(impactSourceGVR, true, false, now)
	token.InitialDeliveryComplete(impactSourceGVR)
	token.WatchStarted(impactSourceGVR)
	return source, token
}

func TestImpactDynamicSourceWindowAndFacetIsolation(t *testing.T) {
	for _, change := range []string{"steady", "missing_gatekeeper", "reconnect", "stop", "replace_type", "replace_source", "new_type", "write_gap", "other_cluster"} {
		t.Run(change, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			source, token := readyImpactSource(t, tracker, "", operations.CoverageSourceCRD)
			if change != "missing_gatekeeper" {
				readyImpactSource(t, tracker, "", operations.CoverageSourceGatekeeper)
			}
			_, other := readyImpactSource(t, tracker, "other", operations.CoverageSourceCRD)
			result := observedImpact(t, authorizationFixture(), tracker, func() {
				switch change {
				case "reconnect":
					token.WatchEnded(impactSourceGVR, 0, operations.CoverageWatchFailed, time.Now())
					token.WatchStarted(impactSourceGVR)
				case "stop":
					token.Stop(time.Now())
				case "replace_type":
					token.Stop(time.Now())
					replacement, err := source.BeginType(impactSourceGVR, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					replacement.InitialDeliveryComplete(impactSourceGVR)
					replacement.WatchStarted(impactSourceGVR)
				case "replace_source":
					readyImpactSource(t, tracker, "", operations.CoverageSourceCRD)
				case "new_type":
					gvr := impactSourceGVR
					gvr.Resource = "newwidgets"
					if _, err := source.BeginType(gvr, time.Now()); err != nil {
						t.Fatal(err)
					}
				case "write_gap":
					token.RecordGap(impactSourceGVR, operations.CoveragePersistenceFailed, time.Now())
				case "other_cluster":
					other.Stop(time.Now())
				}
			})
			ordinary, auth := result.Observation.Ordinary, result.Observation.Authorization
			wantStable := change == "steady" || change == "other_cluster"
			if ordinary.StableWindow != wantStable || ordinary.TypeScopeClosed || ordinary.State == operations.CoverageObserved || ordinary.EmptyResultAssessment != "none_observed_incomplete" {
				t.Fatalf("dynamic scope overclaimed: stable=%v state=%s reasons=%v", ordinary.StableWindow, ordinary.State, ordinary.Reasons)
			}
			if len(ordinary.Before.Sources) == 0 || len(ordinary.After.Sources) == 0 {
				t.Fatal("source evidence dropped from result")
			}
			if change == "missing_gatekeeper" && !slices.Contains(ordinary.Reasons, "gatekeeper_source_coverage_unavailable") {
				t.Fatal("missing collector hidden")
			}
			if !wantStable && change != "missing_gatekeeper" && !slices.Contains(ordinary.Reasons, "crd_source_window_unstable") {
				t.Fatal("source transition not explained")
			}
			if auth.State != operations.CoverageObserved || !auth.StableWindow || len(auth.Before.Sources) != 0 {
				t.Fatal("unrelated dynamic collector changed closed authorization profile")
			}
		})
	}
}

func TestImpactDynamicSourceEvidenceValidationAndOwnership(t *testing.T) {
	for _, mutation := range []string{"source", "duplicate", "cluster", "generation", "state", "empty_observed", "limited_observed", "too_many_types", "duplicate_type", "core_secret", "invalid_type", "raw_reason", "future_time"} {
		t.Run(mutation, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			readyImpactSource(t, tracker, "", operations.CoverageSourceCRD)
			provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
				snapshot, err := tracker.SnapshotCoverage(ctx, cluster, types)
				if err != nil {
					return snapshot, err
				}
				s := &snapshot.Sources[0]
				switch mutation {
				case "source":
					s.Source = "untrusted-canary"
				case "duplicate":
					snapshot.Sources = append(snapshot.Sources, *s)
				case "cluster":
					s.ClusterID = "other"
				case "generation":
					s.Generation = 0
				case "state":
					s.State = "untrusted-canary"
				case "empty_observed":
					s.Resources = nil
				case "limited_observed":
					s.InventoryLimited = true
				case "too_many_types":
					s.Resources = make([]operations.ResourceCoverage, operations.MaxCoverageTypes+1)
				case "duplicate_type":
					s.Resources = append(s.Resources, s.Resources[0])
				case "core_secret":
					s.Resources[0].Group = ""
					s.Resources[0].Resource = "secrets"
				case "invalid_type":
					s.Resources[0].Resource = "bad/type"
				case "raw_reason":
					s.Resources[0].Reasons = []operations.CoverageReason{"untrusted-canary"}
				case "future_time":
					s.Resources[0].Since = time.Now().Add(time.Hour)
				}
				return snapshot, nil
			})
			reads := 0
			g := authorizationFixture()
			probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { reads++; return g, nil }}
			result, err := analysis.AnalyzeObservedImpact(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, provider)
			if result != nil || !errors.Is(err, analysis.ErrImpactObservation) || reads != 0 {
				t.Fatalf("invalid dynamic evidence accepted: reads=%d err=%v", reads, err)
			}
		})
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	readyImpactObservation(t, tracker, "", observedImpactTypes(false))
	_, token := readyImpactSource(t, tracker, "", operations.CoverageSourceCRD)
	token.RecordGap(impactSourceGVR, operations.CoverageProcessingFailed, time.Now())
	var captures []operations.CoverageSnapshot
	provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
		s, err := tracker.SnapshotCoverage(ctx, cluster, types)
		captures = append(captures, s)
		return s, err
	})
	result := observedImpact(t, authorizationFixture(), provider, nil)
	captures[0].Sources[0].Resources[0].Reasons[0] = "provider mutation"
	if result.Observation.Ordinary.Before.Sources[0].Resources[0].Reasons[0] == "provider mutation" {
		t.Fatal("result aliased provider evidence")
	}
}

func TestImpactGatekeeperDiscoveryValidation(t *testing.T) {
	for _, mutation := range []string{"state", "revision", "unknown_time", "missing_time", "future", "wrong_stale", "healthy_stale", "healthy_denied", "wrong_source"} {
		t.Run(mutation, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			source, _ := readyImpactSource(t, tracker, "", operations.CoverageSourceGatekeeper)
			if err := source.RecordDiscovery(operations.SourceAPIAdvertised, time.Now()); err != nil {
				t.Fatal(err)
			}
			provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
				s, err := tracker.SnapshotCoverage(ctx, cluster, types)
				if err != nil {
					return s, err
				}
				d := s.Sources[0].Discovery
				switch mutation {
				case "state":
					d.State = "untrusted-canary"
				case "revision":
					d.Revision = 0
				case "unknown_time":
					d.State = operations.SourceAPIUnknown
				case "missing_time":
					d.CheckedAt = time.Time{}
				case "future":
					d.CheckedAt = s.CapturedAt.Add(time.Second)
				case "wrong_stale":
					d.Stale = true
				case "healthy_stale":
					d.CheckedAt = s.CapturedAt.Add(-operations.SourceDiscoveryMaxAge)
					d.Stale = true
				case "healthy_denied":
					d.State = operations.SourceAPIDenied
				case "wrong_source":
					s.Sources[0].Source = operations.CoverageSourceCRD
				}
				return s, nil
			})
			reads := 0
			g := authorizationFixture()
			store := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { reads++; return g, nil }}
			result, err := analysis.AnalyzeObservedImpact(context.Background(), store, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, provider)
			if result != nil || !errors.Is(err, analysis.ErrImpactObservation) || reads != 0 {
				t.Fatalf("invalid discovery reached graph/result: %d %v", reads, err)
			}
		})
	}
}

func TestImpactGatekeeperDiscoveryWindowOwnershipAndIsolation(t *testing.T) {
	for _, state := range []operations.SourceDiscoveryState{operations.SourceAPIUnknown, operations.SourceAPIAdvertised, operations.SourceAPINotAdvertised, operations.SourceAPIDenied, operations.SourceAPIFailed} {
		t.Run(string(state), func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			readyImpactSource(t, tracker, "", operations.CoverageSourceCRD)
			source, _ := readyImpactSource(t, tracker, "", operations.CoverageSourceGatekeeper)
			if err := source.RecordDiscovery(operations.SourceAPIAdvertised, time.Now()); err != nil {
				t.Fatal(err)
			}
			var captures []operations.CoverageSnapshot
			provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
				s, err := tracker.SnapshotCoverage(ctx, cluster, types)
				captures = append(captures, s)
				return s, err
			})
			result := observedImpact(t, authorizationFixture(), provider, func() {
				at := time.Now()
				if state == operations.SourceAPIUnknown {
					at = time.Time{}
				}
				if err := source.RecordDiscovery(state, at); err != nil {
					t.Fatal(err)
				}
			})
			o := result.Observation.Ordinary
			if o.StableWindow || o.TypeScopeClosed || o.EmptyResultAssessment != "none_observed_incomplete" || !slices.Contains(o.Reasons, "gatekeeper_source_window_unstable") {
				t.Fatal("discovery change hidden or inventory overclaimed")
			}
			if state != operations.SourceAPIAdvertised && !slices.Contains(o.Reasons, "gatekeeper_source_api_"+string(state)) {
				t.Fatal("discovery result not explained")
			}
			if !result.Observation.Authorization.StableWindow || result.Observation.Authorization.State != operations.CoverageObserved {
				t.Fatal("discovery changed unrelated authorization facet")
			}
			captures[0].Sources[1].Discovery.State = "provider mutation"
			if o.Before.Sources[1].Discovery.State != operations.SourceAPIAdvertised {
				t.Fatal("result aliases provider discovery pointer")
			}
		})
	}
}
