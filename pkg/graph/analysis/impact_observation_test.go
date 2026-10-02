// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type impactCoverageFunc func(context.Context, string, []schema.GroupVersionResource) (operations.CoverageSnapshot, error)

func (f impactCoverageFunc) SnapshotCoverage(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
	return f(ctx, cluster, types)
}

// Independent requirement oracle. Do not build it from successful graph matches
// or from the provider's requested types: both would hide missing requirements.
func observedImpactTypes(authorization bool) []schema.GroupVersionResource {
	types := map[string][]string{
		"v1":                           {"pods", "serviceaccounts"},
		"apps/v1":                      {"deployments", "replicasets", "statefulsets", "daemonsets"},
		"batch/v1":                     {"jobs", "cronjobs"},
		"rbac.authorization.k8s.io/v1": {"roles", "rolebindings", "clusterroles", "clusterrolebindings"},
	}
	if !authorization {
		types["v1"] = append(types["v1"], "namespaces", "services", "configmaps", "persistentvolumeclaims")
		types["autoscaling/v2"] = []string{"horizontalpodautoscalers"}
		types["networking.k8s.io/v1"] = []string{"ingresses", "networkpolicies"}
		types["gateway.networking.k8s.io/v1"] = []string{"gateways", "httproutes"}
		types["kyverno.io/v1"] = []string{"policies", "clusterpolicies"}
		types["wgpolicyk8s.io/v1alpha2"] = []string{"policyreports", "clusterpolicyreports"}
		types["route.openshift.io/v1"] = []string{"routes"}
	}
	var out []schema.GroupVersionResource
	for apiVersion, names := range types {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			panic(err)
		}
		for _, name := range names {
			out = append(out, gv.WithResource(name))
		}
	}
	slices.SortFunc(out, func(a, b schema.GroupVersionResource) int { return strings.Compare(a.String(), b.String()) })
	return out
}

func readyImpactObservation(t *testing.T, tracker *operations.CoverageTracker, cluster string, types []schema.GroupVersionResource) *operations.CoverageSession {
	t.Helper()
	now := time.Now()
	session, err := tracker.Begin(cluster, types, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, gvr := range types {
		session.ListResult(gvr, true, false, now)
		session.InitialDeliveryComplete(gvr)
		session.WatchStarted(gvr)
	}
	return session
}

func observedImpact(t *testing.T, g *graph.Graph, provider analysis.ImpactCoverageProvider, duringRead func()) *analysis.ImpactResult {
	t.Helper()
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		if duringRead != nil {
			duringRead()
		}
		return g, nil
	}}
	result, err := analysis.AnalyzeObservedImpact(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, provider)
	if err != nil || result == nil || result.Observation == nil {
		t.Fatalf("observed analysis: %+v %v", result, err)
	}
	return result
}

func TestImpactObservationRequirementsAndFacetSeparation(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	readyImpactObservation(t, tracker, "", observedImpactTypes(false))
	var calls []string
	var deadline time.Time
	checkDeadline := func(ctx context.Context) {
		got, ok := ctx.Deadline()
		if !ok || time.Until(got) > graph.ImpactReadTimeout {
			t.Fatal("observation read escaped total deadline")
		}
		if deadline.IsZero() {
			deadline = got
		} else if !deadline.Equal(got) {
			t.Fatal("graph and observation reads use separate budgets")
		}
	}
	provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
		checkDeadline(ctx)
		calls = append(calls, "coverage")
		got := slices.Clone(types)
		slices.SortFunc(got, func(a, b schema.GroupVersionResource) int { return strings.Compare(a.String(), b.String()) })
		if cluster != "" || !slices.Equal(got, observedImpactTypes(false)) {
			t.Fatalf("requirements changed scope or inferred types from matches: %v %s", types, cluster)
		}
		return tracker.SnapshotCoverage(ctx, cluster, types)
	})
	g := authorizationFixture()
	probe := &impactSnapshotProbe{read: func(ctx context.Context, _ graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		checkDeadline(ctx)
		calls = append(calls, "graph")
		return g, nil
	}}
	for _, opts := range []analysis.ImpactOptions{{}, {MaxDepth: 1, Limit: 1, Relation: analysis.ImpactDependencies}} {
		calls, deadline = nil, time.Time{}
		got, err := analysis.AnalyzeObservedImpact(context.Background(), probe, g.Resources[0].ID(), opts, graph.ImpactSnapshotOptions{}, provider)
		if err != nil || !slices.Equal(calls, []string{"coverage", "graph", "coverage"}) {
			t.Fatalf("read ordering/count: %v %v", calls, err)
		}
		ordinary, auth := got.Observation.Ordinary, got.Observation.Authorization
		if ordinary.State != operations.CoveragePartial || ordinary.TypeScopeClosed || ordinary.StableWindow || ordinary.EmptyResultAssessment != "none_observed_incomplete" || !slices.Contains(ordinary.Reasons, "dynamic_source_scope_unverified") || !slices.Contains(ordinary.Reasons, "crd_source_coverage_unavailable") || !slices.Contains(ordinary.Reasons, "gatekeeper_source_coverage_unavailable") {
			t.Fatalf("core watches claimed dynamic source completeness: %+v", ordinary)
		}
		if auth.State != operations.CoverageObserved || !auth.TypeScopeClosed || !auth.StableWindow || auth.EmptyResultAssessment != "not_empty" || len(auth.Before.Resources) != 12 || len(ordinary.Before.Resources) != 26 {
			t.Fatalf("authorization scope/window incorrect: %+v", auth)
		}
		if got.ObservationCoverage != string(ordinary.State) || got.Authorization.ObservationCoverage != string(auth.State) || got.Authorization.EffectivePermissions != "not_evaluated" || got.Authorization.SourceVersionEvidence != "unknown" {
			t.Fatal("coverage overstated source-version or effective authorization evidence")
		}
	}
	// The original pure and store-backed M1 seams do not acquire observation data.
	deadline = time.Time{}
	plain, err := analysis.AnalyzeStoredImpact(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{})
	if err != nil || plain.Observation != nil || plain.ObservationCoverage != "unknown" || plain.Authorization.ObservationCoverage != "unknown" {
		t.Fatalf("legacy analysis contract changed: %+v %v", plain, err)
	}
}

func TestImpactObservationEmptyResultsRequireIndependentEvidence(t *testing.T) {
	for _, tc := range []string{"healthy", "missing_type", "list_denied", "stale", "schema_unknown", "no_provider"} {
		t.Run(tc, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			types := observedImpactTypes(true)
			if tc == "missing_type" {
				types = types[:len(types)-1]
			}
			session := readyImpactObservation(t, tracker, "", types)
			if tc == "list_denied" {
				session.ListResult(types[0], false, true, time.Now())
			}
			if tc == "stale" {
				session.WatchEnded(types[0], 0, operations.CoverageWatchFailed, time.Now().Add(-2*time.Minute))
			}
			g := authorizationFixture()
			g.Resources, g.Edges = g.Resources[:1], nil
			if tc == "schema_unknown" {
				g.Resources[0].GroupVersion = ""
			}
			var provider analysis.ImpactCoverageProvider = tracker
			if tc == "no_provider" {
				provider = nil
			}
			result := observedImpact(t, g, provider, nil)
			auth := result.Observation.Authorization
			if result.Authorization.Counts.Total != 0 || result.Observation.Ordinary.EmptyResultAssessment != "none_observed_incomplete" {
				t.Fatal("empty graph fabricated matches or a complete ordinary observation")
			}
			if tc == "healthy" {
				if auth.EmptyResultAssessment != "none_found_in_analyzed_scope" || auth.State != operations.CoverageObserved {
					t.Fatalf("healthy bounded authorization scope: %+v", auth)
				}
			} else if auth.EmptyResultAssessment != "none_observed_incomplete" || auth.State == operations.CoverageObserved {
				t.Fatalf("missing evidence became authoritative zero: %+v", auth)
			}
			if tc == "stale" && auth.State != operations.CoverageStale || tc == "no_provider" && auth.State != operations.CoverageUnknown || tc == "schema_unknown" && auth.TypeScopeClosed {
				t.Fatalf("lost the specific evidence limitation: %+v", auth)
			}
		})
	}
}

func TestImpactObservationTransientWindowChanges(t *testing.T) {
	for _, tc := range []string{"watch_recovers", "list_recovers", "session_replaced", "write_gap", "collector_stops", "ordinary_only", "other_cluster"} {
		t.Run(tc, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			types := observedImpactTypes(false)
			session := readyImpactObservation(t, tracker, "", types)
			pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
			result := observedImpact(t, authorizationFixture(), tracker, func() {
				switch tc {
				case "watch_recovers":
					session.WatchEnded(pods, 0, operations.CoverageWatchDenied, time.Now())
					session.WatchStarted(pods)
				case "list_recovers":
					session.ListResult(pods, false, true, time.Now())
					session.ListResult(pods, true, false, time.Now())
				case "session_replaced":
					readyImpactObservation(t, tracker, "", types)
				case "write_gap":
					session.RecordGap(pods, operations.CoveragePersistenceFailed, time.Now())
				case "collector_stops":
					session.Stop(time.Now())
				case "ordinary_only":
					session.RecordGap(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, operations.CoverageExtractionFailed, time.Now())
				case "other_cluster":
					other := readyImpactObservation(t, tracker, "other", types)
					other.RecordGap(pods, operations.CoveragePersistenceFailed, time.Now())
				}
			})
			auth := result.Observation.Authorization
			if tc == "ordinary_only" || tc == "other_cluster" {
				if !auth.StableWindow || auth.State != operations.CoverageObserved {
					t.Fatal("unrelated type/cluster contaminated authorization coverage")
				}
				if tc == "ordinary_only" && result.Observation.Ordinary.StableWindow {
					t.Fatal("ordinary gap was not detected")
				}
				return
			}
			if auth.StableWindow || auth.State != operations.CoveragePartial || !slices.Contains(auth.Reasons, "observation_window_unstable") {
				t.Fatalf("query crossed an unreported observation discontinuity: %+v", auth)
			}
			if tc == "watch_recovers" || tc == "list_recovers" || tc == "session_replaced" {
				if auth.Before.State != operations.CoverageObserved || auth.After.State != operations.CoverageObserved {
					t.Fatal("fixture did not exercise healthy-before/healthy-after recovery")
				}
			}
		})
	}
}

func TestImpactObservationRejectsInvalidProviderEvidence(t *testing.T) {
	for _, tc := range []string{"cluster", "missing", "duplicate", "secret_type", "stale_capture", "future_capture", "future_row", "bad_state", "raw_reason", "duplicate_reason", "observed_gap", "observed_pending", "observed_no_revision", "provider_error_before", "provider_error_after"} {
		t.Run(tc, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			reads, captures := 0, 0
			probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) {
				reads++
				return authorizationFixture(), nil
			}}
			provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
				captures++
				snapshot, err := tracker.SnapshotCoverage(ctx, cluster, types)
				if err != nil {
					return snapshot, err
				}
				switch tc {
				case "cluster":
					snapshot.ClusterID = "other"
				case "missing":
					snapshot.Resources = snapshot.Resources[1:]
				case "duplicate":
					snapshot.Resources[1] = snapshot.Resources[0]
				case "secret_type":
					snapshot.Resources[0].Resource = "secrets"
				case "stale_capture":
					snapshot.CapturedAt = snapshot.CapturedAt.Add(-time.Minute)
				case "future_capture":
					snapshot.CapturedAt = snapshot.CapturedAt.Add(time.Minute)
				case "future_row":
					snapshot.Resources[0].Since = snapshot.CapturedAt.Add(time.Minute)
				case "bad_state":
					snapshot.Resources[0].State = "unrecognized"
				case "raw_reason":
					snapshot.Resources[0].Reasons = []operations.CoverageReason{"synthetic-sensitive-canary"}
				case "duplicate_reason":
					snapshot.Resources[0].Reasons = []operations.CoverageReason{operations.CoverageNotWatched, operations.CoverageNotWatched}
				case "observed_gap":
					snapshot.Resources[0].PersistenceGapSince = snapshot.CapturedAt
				case "observed_pending":
					snapshot.Resources[0].InitialDeliveryDone = false
				case "observed_no_revision":
					snapshot.Resources[0].ContinuityRevision = 0
				case "provider_error_before":
					return operations.CoverageSnapshot{}, errors.New("synthetic-sensitive-canary")
				case "provider_error_after":
					if captures == 2 {
						return operations.CoverageSnapshot{}, errors.New("synthetic-sensitive-canary")
					}
				}
				return snapshot, nil
			})
			result, err := analysis.AnalyzeObservedImpact(context.Background(), probe, authorizationFixture().Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, provider)
			if result != nil || !errors.Is(err, analysis.ErrImpactObservation) || strings.Contains(err.Error(), "synthetic-sensitive-canary") {
				t.Fatalf("invalid evidence became a result or leaked provider error: %+v %v", result, err)
			}
			wantReads := 0
			if tc == "provider_error_after" {
				wantReads = 1
			}
			if reads != wantReads {
				t.Fatalf("unexpected graph access: %d want %d", reads, wantReads)
			}
		})
	}
}

func TestImpactObservationValidationAndCancellation(t *testing.T) {
	provider := impactCoverageFunc(func(context.Context, string, []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
		t.Fatal("invalid request or missing capability reached provider")
		return operations.CoverageSnapshot{}, nil
	})
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		t.Fatal("invalid request reached graph")
		return nil, nil
	}}
	for _, tc := range []struct {
		store graph.GraphStore
		opts  analysis.ImpactOptions
		want  error
	}{
		{probe, analysis.ImpactOptions{MaxDepth: -1}, analysis.ErrImpactInvalidOptions},
		{probe, analysis.ImpactOptions{ClusterID: "other"}, analysis.ErrImpactInvalidOptions},
		{&impactRestrictedStore{}, analysis.ImpactOptions{}, analysis.ErrImpactSnapshotUnsupported},
	} {
		result, err := analysis.AnalyzeObservedImpact(context.Background(), tc.store, "demo/Pod/root", tc.opts, graph.ImpactSnapshotOptions{}, provider)
		if result != nil || !errors.Is(err, tc.want) {
			t.Fatalf("invalid request: %+v %v", result, err)
		}
	}
	for _, phase := range []string{"before", "graph", "after"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			reads, captures := 0, 0
			provider := impactCoverageFunc(func(readCtx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
				captures++
				if phase == "before" || phase == "after" && captures == 2 {
					<-readCtx.Done()
					return operations.CoverageSnapshot{}, readCtx.Err()
				}
				return tracker.SnapshotCoverage(readCtx, cluster, types)
			})
			probe := &impactSnapshotProbe{read: func(readCtx context.Context, _ graph.ImpactSnapshotOptions) (*graph.Graph, error) {
				reads++
				if phase == "graph" {
					<-readCtx.Done()
					return nil, readCtx.Err()
				}
				return authorizationFixture(), nil
			}}
			result, err := analysis.AnalyzeObservedImpact(ctx, probe, authorizationFixture().Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, provider)
			if result != nil || !errors.Is(err, context.DeadlineExceeded) || phase == "before" && reads != 0 || phase == "graph" && captures != 1 {
				t.Fatalf("timeout returned partial result or continued reading: %+v %v reads=%d captures=%d", result, err, reads, captures)
			}
		})
	}
}

func TestImpactObservationOwnershipAndSchemaBoundaries(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	types := observedImpactTypes(false)
	session := readyImpactObservation(t, tracker, "", types)
	session.ListResult(types[0], false, true, time.Now())
	var retained []operations.CoverageSnapshot
	var original [][]byte
	provider := impactCoverageFunc(func(ctx context.Context, cluster string, required []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
		snapshot, err := tracker.SnapshotCoverage(ctx, cluster, required)
		retained = append(retained, snapshot)
		encoded, _ := json.Marshal(snapshot)
		original = append(original, encoded)
		return snapshot, err
	})
	result := observedImpact(t, authorizationFixture(), provider, nil)
	beforeAuth, _ := json.Marshal(result.Observation.Authorization)
	beforeAfter, _ := json.Marshal(result.Observation.Ordinary.After)
	for i := range result.Observation.Ordinary.Before.Resources {
		row := &result.Observation.Ordinary.Before.Resources[i]
		if len(row.Reasons) > 0 {
			row.Reasons[0] = "mutated"
		}
		row.Resource = "mutated"
	}
	afterAuth, _ := json.Marshal(result.Observation.Authorization)
	afterAfter, _ := json.Marshal(result.Observation.Ordinary.After)
	if !slices.Equal(beforeAuth, afterAuth) || !slices.Equal(beforeAfter, afterAfter) {
		t.Fatal("facets/captures share mutable evidence")
	}
	for i, snapshot := range retained {
		encoded, _ := json.Marshal(snapshot)
		if !slices.Equal(original[i], encoded) {
			t.Fatal("result modified provider-owned evidence")
		}
	}
	clean := operations.NewCoverageTracker(time.Minute)
	readyImpactObservation(t, clean, "", types)
	g := authorizationFixture()
	g.Resources[1].GroupVersion = ""
	result = observedImpact(t, g, clean, nil)
	if result.Observation.Authorization.TypeScopeClosed || result.Observation.Authorization.State != operations.CoveragePartial {
		t.Fatal("unknown schema on a matched binding became verified coverage")
	}
	for _, kind := range []string{"ConfigMap", "Secret"} {
		g = &graph.Graph{Resources: []graph.Resource{{Kind: kind, Namespace: "demo", Name: "reference", GroupVersion: "v1"}}}
		result = observedImpact(t, g, clean, nil)
		if result.Authorization.Applicable || result.Observation.Authorization.EmptyResultAssessment != "not_applicable" || result.Observation.Authorization.State != operations.CoverageUnknown {
			t.Fatalf("non-RBAC root became authorization completeness claim: %s", kind)
		}
		for _, row := range result.Observation.Ordinary.Before.Resources {
			if row.Resource == "secrets" {
				t.Fatal("reference-only Secret triggered a Secret watcher requirement")
			}
		}
	}
}

func TestImpactObservationRealStoreClusterScopeAndUnknownGeneration(t *testing.T) {
	for _, cluster := range []string{"", "prod"} {
		t.Run("cluster="+cluster, func(t *testing.T) {
			ctx := context.Background()
			store := memory.New()
			g := authorizationFixture()
			ids := make(map[string]string)
			for i := range g.Resources {
				old := g.Resources[i].ID()
				g.Resources[i].ClusterID = cluster
				ids[old] = g.Resources[i].ID()
				if err := store.UpsertResource(ctx, g.Resources[i]); err != nil {
					t.Fatal(err)
				}
			}
			for _, edge := range g.Edges {
				edge.From, edge.To = ids[edge.From], ids[edge.To]
				if err := store.UpsertEdge(ctx, edge); err != nil {
					t.Fatal(err)
				}
			}
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, cluster, observedImpactTypes(false))
			var hideGeneration bool
			provider := impactCoverageFunc(func(ctx context.Context, selected string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
				if selected != cluster {
					t.Fatal("coverage read used a different scope than the projection")
				}
				snapshot, err := tracker.SnapshotCoverage(ctx, selected, types)
				if hideGeneration {
					snapshot.Generation = 0
				}
				return snapshot, err
			})
			for _, hide := range []bool{false, true} {
				hideGeneration = hide
				result, err := analysis.AnalyzeObservedImpact(ctx, store, g.Resources[0].ID(), analysis.ImpactOptions{ClusterID: cluster}, graph.ImpactSnapshotOptions{ClusterID: cluster}, provider)
				if err != nil || result.Authorization.Counts.Total != 3 || result.Counts.Total != 0 {
					t.Fatalf("store/facet scope: %+v %v", result, err)
				}
				auth := result.Observation.Authorization
				if !hide && (auth.State != operations.CoverageObserved || !auth.StableWindow) || hide && (auth.State != operations.CoveragePartial || auth.StableWindow) {
					t.Fatalf("missing session identity became verified continuity: %+v", auth)
				}
			}
			result, err := analysis.AnalyzeObservedImpact(ctx, store, g.Resources[0].ID(), analysis.ImpactOptions{ClusterID: cluster, ExpectedUID: "replaced-root"}, graph.ImpactSnapshotOptions{ClusterID: cluster}, provider)
			if result != nil || !errors.Is(err, analysis.ErrImpactUIDMismatch) {
				t.Fatalf("observation wrapper hid root identity conflict: %+v %v", result, err)
			}
		})
	}
}
