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

var impactGatewayAPI = operations.APIResourceDescriptor{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways", Kind: "Gateway", Namespaced: true, List: true, Watch: true}

func impactInventory(t *testing.T, tracker *operations.CoverageTracker, cluster string, state operations.APIInventoryState, resources []operations.APIResourceDescriptor, limited bool, at time.Time) *operations.APIInventorySession {
	t.Helper()
	s, err := tracker.BeginAPIInventory(cluster)
	if err != nil {
		t.Fatal(err)
	}
	if state != operations.APIInventoryUnknown {
		if err := s.Record(state, resources, limited, at); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestImpactAPIInventoryOptionalAbsenceAndMissingCollectors(t *testing.T) {
	for _, name := range []string{"absent", "advertised", "other_version", "unsupported", "wrong_scope", "cluster_scoped_policy", "partial", "denied", "failed", "unknown", "limited", "stale", "stopped"} {
		t.Run(name, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			state, resources, limited, at := operations.APIInventoryComplete, []operations.APIResourceDescriptor{}, false, time.Now()
			want := analysis.ImpactAPINotAdvertised
			switch name {
			case "advertised":
				resources = append(resources, impactGatewayAPI)
				want = analysis.ImpactAPIAdvertised
			case "other_version":
				r := impactGatewayAPI
				r.Version = "v1beta1"
				resources = append(resources, r)
				want = analysis.ImpactAPIOtherVersion
			case "unsupported":
				r := impactGatewayAPI
				r.Watch = false
				resources = append(resources, r)
				want = analysis.ImpactAPIUnsupported
			case "wrong_scope":
				r := impactGatewayAPI
				r.Namespaced = false
				resources = append(resources, r)
				want = analysis.ImpactAPIUnsupported
			case "cluster_scoped_policy":
				resources = append(resources, operations.APIResourceDescriptor{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies", Kind: "ClusterPolicy", List: true, Watch: true})
			case "partial":
				state = operations.APIInventoryPartial
				want = analysis.ImpactAPIUnknown
			case "denied":
				state = operations.APIInventoryDenied
				want = analysis.ImpactAPIUnknown
			case "failed":
				state = operations.APIInventoryFailed
				want = analysis.ImpactAPIUnknown
			case "unknown":
				state = operations.APIInventoryUnknown
				want = analysis.ImpactAPIUnknown
			case "limited":
				state, limited = operations.APIInventoryPartial, true
				want = analysis.ImpactAPIUnknown
			case "stale":
				at = at.Add(-operations.APIInventoryMaxAge)
				want = analysis.ImpactAPIUnknown
			case "stopped":
				want = analysis.ImpactAPIUnknown
			}
			// API metadata alone must never require Secret object observation.
			if state == operations.APIInventoryComplete {
				resources = append(resources, operations.APIResourceDescriptor{Group: "example.com", Version: "v1", Resource: "widgets", Kind: "Widget", Namespaced: true, List: true, Watch: true}, operations.APIResourceDescriptor{Version: "v1", Resource: "secrets", Kind: "Secret", Namespaced: true, List: true, Watch: true})
			}
			s := impactInventory(t, tracker, "", state, resources, limited, at)
			if name == "stopped" {
				s.Stop()
			}
			result := observedImpact(t, authorizationFixture(), tracker, nil)
			o := result.Observation.Ordinary
			if len(o.OptionalAPIs) != 7 || o.Before.APIInventory == nil || o.TypeScopeClosed || o.EmptyResultAssessment != "none_observed_incomplete" {
				t.Fatal("inventory scope lost or completeness overclaimed")
			}
			for _, api := range o.OptionalAPIs {
				if api.Resource == "gateways" && (api.Before != want || api.After != want) {
					t.Fatalf("presence=%s/%s want=%s", api.Before, api.After, want)
				}
				if name == "cluster_scoped_policy" && api.Resource == "clusterpolicies" && api.After != analysis.ImpactAPIAdvertised {
					t.Fatal("cluster policy assumed namespaced")
				}
			}
			if name == "absent" && (len(o.UnobservedAPIs) != 1 || o.UnobservedAPIs[0].Resource != "widgets" || !slices.Contains(o.Reasons, "advertised_resource_types_unobserved")) {
				t.Fatal("unobserved endpoint lost or Secret observation invented")
			}
			if len(result.Observation.Authorization.OptionalAPIs) != 0 || result.Observation.Authorization.Before.APIInventory != nil || result.Observation.Authorization.State != operations.CoverageObserved {
				t.Fatal("optional inventory contaminated RBAC profile")
			}
		})
	}
}

func TestImpactAPIInventoryInvalidProviderFailsBeforeGraphRead(t *testing.T) {
	for _, name := range []string{"cluster", "generation", "revision", "state", "future", "stale", "limited_complete", "unknown_checked", "unknown_resources", "duplicate", "descriptor", "count", "failed_resources"} {
		t.Run(name, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			readyImpactObservation(t, tracker, "", observedImpactTypes(false))
			impactInventory(t, tracker, "", operations.APIInventoryComplete, []operations.APIResourceDescriptor{impactGatewayAPI}, false, time.Now())
			provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
				s, err := tracker.SnapshotCoverage(ctx, cluster, types)
				if err != nil {
					return s, err
				}
				e := s.APIInventory
				switch name {
				case "cluster":
					e.ClusterID = "hidden"
				case "generation":
					e.Generation = 0
				case "revision":
					e.Revision = 0
				case "state":
					e.State = "raw-error-canary"
				case "future":
					e.CheckedAt = s.CapturedAt.Add(time.Hour)
				case "stale":
					e.Stale = true
				case "limited_complete":
					e.Limited = true
				case "unknown_checked", "unknown_resources":
					e.State = operations.APIInventoryUnknown
				case "duplicate":
					e.Resources = append(e.Resources, e.Resources[0])
				case "descriptor":
					e.Resources[0].Resource = "resource/value-canary"
				case "count":
					e.Resources = make([]operations.APIResourceDescriptor, operations.MaxInventoryResources+1)
				case "failed_resources":
					e.State = operations.APIInventoryFailed
				}
				if name == "unknown_resources" {
					e.CheckedAt = time.Time{}
					e.Revision = 0
				}
				return s, nil
			})
			reads := 0
			g := authorizationFixture()
			store := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { reads++; return g, nil }}
			result, err := analysis.AnalyzeObservedImpact(context.Background(), store, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, provider)
			if result != nil || !errors.Is(err, analysis.ErrImpactObservation) || reads != 0 {
				t.Fatalf("invalid inventory disclosed: reads=%d err=%v", reads, err)
			}
		})
	}
}

func TestImpactAPIInventoryWindowAndResponseOwnership(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	readyImpactObservation(t, tracker, "", observedImpactTypes(false))
	readyImpactSource(t, tracker, "", operations.CoverageSourceCRD)
	readyImpactSource(t, tracker, "", operations.CoverageSourceGatekeeper)
	resources := []operations.APIResourceDescriptor{impactGatewayAPI}
	s := impactInventory(t, tracker, "", operations.APIInventoryComplete, resources, false, time.Now())
	var captures []operations.CoverageSnapshot
	provider := impactCoverageFunc(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
		v, err := tracker.SnapshotCoverage(ctx, cluster, types)
		captures = append(captures, v)
		return v, err
	})
	result := observedImpact(t, authorizationFixture(), provider, func() {
		if err := s.Record(operations.APIInventoryComplete, resources, false, time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	if result.Observation.Ordinary.StableWindow || !slices.Contains(result.Observation.Ordinary.Reasons, "api_inventory_window_unstable") || !result.Observation.Authorization.StableWindow {
		t.Fatal("discovery revision changed wrong window")
	}
	captures[0].APIInventory.Resources[0].Kind = "caller mutation"
	if result.Observation.Ordinary.Before.APIInventory.Resources[0].Kind != "Gateway" {
		t.Fatal("response aliases inventory provider")
	}
}
