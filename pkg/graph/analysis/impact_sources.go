// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"reflect"
	"slices"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/operations"
)

// Dynamic-source records supplement, never replace, the static requirements.
// Validate them before any names or provider-owned slices reach the API result.
func validateImpactSources(snapshot *operations.CoverageSnapshot) error {
	if len(snapshot.Sources) > 2 {
		return ErrImpactObservation
	}
	snapshot.Sources = slices.Clone(snapshot.Sources)
	seen := make(map[operations.CoverageSource]bool)
	for i, source := range snapshot.Sources {
		if (source.Source != operations.CoverageSourceCRD && source.Source != operations.CoverageSourceGatekeeper) || seen[source.Source] || source.ClusterID != snapshot.ClusterID || source.Generation == 0 || len(source.Resources) > operations.MaxCoverageTypes {
			return ErrImpactObservation
		}
		seen[source.Source] = true
		if source.Discovery != nil {
			d := *source.Discovery
			if source.Source != operations.CoverageSourceGatekeeper || !operations.ValidSourceDiscoveryState(d.State) || d.Revision == 0 || (d.State == operations.SourceAPIUnknown) != d.CheckedAt.IsZero() || d.CheckedAt.After(snapshot.CapturedAt) || d.Stale != (!d.CheckedAt.IsZero() && snapshot.CapturedAt.Sub(d.CheckedAt) >= operations.SourceDiscoveryMaxAge) || (source.State == operations.CoverageObserved && (d.State != operations.SourceAPIAdvertised || d.Stale)) {
				return ErrImpactObservation
			}
			source.Discovery = &d
		}
		switch source.State {
		case operations.CoverageUnknown, operations.CoverageInitializing, operations.CoverageObserved, operations.CoveragePartial, operations.CoverageStale:
		default:
			return ErrImpactObservation
		}
		if source.State == operations.CoverageObserved && (source.InventoryLimited || len(source.Resources) == 0) {
			return ErrImpactObservation
		}
		source.Resources = slices.Clone(source.Resources)
		types := make(map[string]bool)
		for j, row := range source.Resources {
			key := row.Group + "/" + row.Version + "/" + row.Resource
			if row.Group == "" || row.Version == "" || row.Resource == "" || len(key) > 770 || strings.ContainsAny(row.Group+row.Version+row.Resource, "/:\x00") || types[key] || !validImpactCoverageRow(row, snapshot.CapturedAt) || (source.State == operations.CoverageObserved && row.State != operations.CoverageObserved) {
				return ErrImpactObservation
			}
			types[key] = true
			source.Resources[j].Reasons = slices.Clone(row.Reasons)
		}
		slices.SortFunc(source.Resources, func(a, b operations.ResourceCoverage) int {
			return strings.Compare(a.Group+"/"+a.Version+"/"+a.Resource, b.Group+"/"+b.Version+"/"+b.Resource)
		})
		snapshot.Sources[i] = source
	}
	slices.SortFunc(snapshot.Sources, func(a, b operations.CoverageSourceSnapshot) int {
		return strings.Compare(string(a.Source), string(b.Source))
	})
	return nil
}

func attachImpactSourceCoverage(out *ImpactCoverageFacet, before, after []operations.CoverageSourceSnapshot) {
	out.Before.Sources, out.After.Sources = before, after
	for _, name := range []operations.CoverageSource{operations.CoverageSourceCRD, operations.CoverageSourceGatekeeper} {
		find := func(sources []operations.CoverageSourceSnapshot) *operations.CoverageSourceSnapshot {
			for i := range sources {
				if sources[i].Source == name {
					return &sources[i]
				}
			}
			return nil
		}
		pre, post := find(before), find(after)
		prefix := string(name) + "_source_"
		if pre == nil || post == nil {
			out.Reasons = append(out.Reasons, prefix+"coverage_unavailable")
			out.StableWindow = false
			continue
		}
		if !reflect.DeepEqual(pre, post) {
			out.StableWindow = false
			out.Reasons = append(out.Reasons, prefix+"window_unstable")
		}
		out.State = worseImpactCoverage(out.State, worseImpactCoverage(pre.State, post.State))
		if pre.InventoryLimited || post.InventoryLimited {
			out.Reasons = append(out.Reasons, prefix+"inventory_limited")
			out.State = worseImpactCoverage(out.State, operations.CoveragePartial)
		}
		if name == operations.CoverageSourceGatekeeper {
			if pre.Discovery == nil || post.Discovery == nil {
				out.Reasons = append(out.Reasons, prefix+"discovery_unverified")
			} else if post.Discovery.Stale {
				out.Reasons = append(out.Reasons, prefix+"discovery_stale")
			} else if post.Discovery.State != operations.SourceAPIAdvertised {
				out.Reasons = append(out.Reasons, prefix+"api_"+string(post.Discovery.State))
			}
		}
		if pre.State != operations.CoverageObserved || post.State != operations.CoverageObserved {
			out.Reasons = append(out.Reasons, prefix+"coverage_incomplete")
		}
	}
	if !out.StableWindow && out.State == operations.CoverageObserved {
		out.State = operations.CoveragePartial
	}
}
