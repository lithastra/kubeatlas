// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"
	"errors"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

var ErrImpactSnapshotUnsupported = errors.New("impact: store lacks consistent impact projection capability")

// AnalyzeStoredImpact reads and analyzes within one five-second context budget.
// The caller must authorize opts.ClusterID first. A visibility wrapper lacking
// the explicit capability fails closed; there is no full-snapshot fallback.
// Projection row/byte budgets are separate from query depth/result bounds.
func AnalyzeStoredImpact(ctx context.Context, store graph.GraphStore, rootID string, opts ImpactOptions, limits graph.ImpactSnapshotOptions) (*ImpactResult, error) {
	return analyzeStoredImpact(ctx, store, rootID, opts, limits, ImpactEvidenceProviders{}, false)
}

// AnalyzeObservedImpact adds a before/after observation window around the one
// graph projection. It performs no Kubernetes calls, probes, or history reads.
// A nil provider leaves explicit unknown coverage, not false complete results.
// Authorization before BOTH coverage and graph reads remains the caller's duty.
func AnalyzeObservedImpact(ctx context.Context, store graph.GraphStore, rootID string, opts ImpactOptions, limits graph.ImpactSnapshotOptions, provider ImpactCoverageProvider) (*ImpactResult, error) {
	return AnalyzeImpactWithEvidence(ctx, store, rootID, opts, limits, ImpactEvidenceProviders{Coverage: provider})
}

// AnalyzeImpactWithEvidence composes watch coverage and cached availability
// without converting a successful graph read into a historical guarantee.
// Authorize opts.ClusterID before calling; all reads share the same deadline.
func AnalyzeImpactWithEvidence(ctx context.Context, store graph.GraphStore, rootID string, opts ImpactOptions, limits graph.ImpactSnapshotOptions, providers ImpactEvidenceProviders) (*ImpactResult, error) {
	return analyzeStoredImpact(ctx, store, rootID, opts, limits, providers, true)
}

func analyzeStoredImpact(ctx context.Context, store graph.GraphStore, rootID string, opts ImpactOptions, limits graph.ImpactSnapshotOptions, providers ImpactEvidenceProviders, observed bool) (*ImpactResult, error) {
	ctx, cancel := context.WithTimeout(ctx, graph.ImpactReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limits.ClusterID != opts.ClusterID {
		return nil, ErrImpactInvalidOptions
	}
	var err error
	opts, err = normalizeImpactOptions(rootID, opts)
	if err != nil {
		return nil, err
	}
	limits, err = limits.Normalize()
	if err != nil {
		return nil, err
	}
	reader, ok := store.(graph.ImpactSnapshotter)
	if !ok {
		return nil, ErrImpactSnapshotUnsupported
	}
	var before, after operations.CoverageSnapshot
	if observed {
		before, err = readImpactCoverage(ctx, providers.Coverage, opts.ClusterID)
		if err != nil {
			return nil, err
		}
	}
	projection, err := reader.SnapshotImpact(ctx, limits)
	if err != nil {
		return nil, err
	}
	if observed {
		after, err = readImpactCoverage(ctx, providers.Coverage, opts.ClusterID)
		if err != nil {
			return nil, err
		}
	}
	result, err := AnalyzeImpact(ctx, projection, rootID, opts)
	if err != nil {
		return nil, err
	}
	if observed {
		attachImpactObservation(result, before, after, providers.Coverage != nil)
		availability, err := readImpactAvailability(ctx, providers.Availability, opts.ClusterID)
		if err != nil {
			return nil, err
		}
		result.Availability = &ImpactAvailability{CurrentGraphRead: "succeeded", Evidence: availability}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
