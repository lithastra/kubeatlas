// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"
	"slices"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

func validateImpactOrigins(snapshot *operations.CoverageSnapshot) error {
	if len(snapshot.Origins) > 2*operations.MaxCoverageTypes {
		return ErrImpactObservation
	}
	snapshot.Origins = slices.Clone(snapshot.Origins)
	seen := map[string]bool{}
	for _, registration := range snapshot.Origins {
		o := registration.Origin
		key := o.Source + "/" + o.GVR().String()
		if !o.Valid() || o.ObjectUID != "" || o.ObjectVersion != "" || o.ClusterID != snapshot.ClusterID || seen[key] {
			return ErrImpactObservation
		}
		seen[key] = true
	}
	return nil
}

// Retired data is retained, with qualification of its stored writer receipt.
// This never drops nodes/edges, invents deletion events, fetches Kubernetes or
// exposes private owner identities. Registration is not per-object liveness.
func attachImpactRetention(ctx context.Context, result *ImpactResult, projection *graph.Graph, before, after operations.CoverageSnapshot) error {
	selected := map[string]bool{result.Root.ID: true}
	for _, match := range result.Resources {
		selected[match.Resource.ID] = true
	}
	for _, match := range result.Authorization.Resources {
		selected[match.Resource.ID] = true
	}
	states := map[string]bool{}
	unstable := false
	for _, r := range projection.Resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !selected[r.ID()] || r.Kind == "Secret" {
			continue
		}
		identity := ImpactIdentity{Kind: r.Kind, GroupVersion: r.GroupVersion}
		if r.Origin == (graph.ResourceOrigin{}) && knownImpactIdentitySchema(identity, false) {
			continue // This receipt strategy covers dynamic-source stored rows.
		}
		pre, post := graph.OriginQualification(r, before.Origins), graph.OriginQualification(r, after.Origins)
		if pre != "registered" {
			states[pre] = true
		}
		if post != "registered" {
			states[post] = true
		}
		unstable = unstable || pre != post
	}
	ordinary := &result.Observation.Ordinary
	for _, state := range []string{"unknown", "retired", "replaced"} {
		if states[state] {
			ordinary.Reasons = append(ordinary.Reasons, "retained_source_"+state)
			ordinary.State = worseImpactCoverage(ordinary.State, operations.CoveragePartial)
		}
	}
	if unstable {
		ordinary.Reasons = append(ordinary.Reasons, "retained_source_window_unstable")
		ordinary.StableWindow = false
	}
	ordinary.EmptyResultAssessment = impactEmptyAssessment(result.Counts.Total, result.ModeledTraversalComplete, *ordinary)
	result.ObservationCoverage = string(ordinary.State)
	return nil
}
