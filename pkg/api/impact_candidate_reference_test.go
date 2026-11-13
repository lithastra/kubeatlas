// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"slices"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

type candidateReferenceEdge struct {
	from, to string
	kind     graph.EdgeType
}

// Each traversed reference edge has one sidecar, even when many returned
// paths share that edge. An explicit oracle covers the entire sidecar set.
func validCandidateReferenceEvidence(paths map[string][]analysis.ImpactStep, expected []analysis.ImpactReferenceEvidence) bool {
	required := make(map[candidateReferenceEdge]bool)
	for _, path := range paths {
		for _, step := range path {
			switch step.Type {
			case graph.EdgeTypeUsesConfigMap, graph.EdgeTypeUsesSecret, graph.EdgeTypeMountsVolume:
				required[candidateReferenceEdge{step.From, step.To, step.Type}] = true
			}
		}
	}
	for _, ref := range expected {
		key := candidateReferenceEdge{ref.From, ref.To, ref.Type}
		if !required[key] {
			return false
		}
		delete(required, key)
		switch ref.Status {
		case "unknown":
			if ref.Reason == "" || len(ref.Uses) != 0 {
				return false
			}
		case "version_bound":
			if ref.Reason != "" || ref.SourceUID == "" || ref.SourceResourceVersion == "" || len(ref.Uses) == 0 {
				return false
			}
			for _, use := range ref.Uses {
				if !strings.HasPrefix(use.FieldPath, "/") ||
					(use.MountFieldPath != "" && !strings.HasPrefix(use.MountFieldPath, "/")) ||
					use.Mode == "" || use.Optional == "" || use.Refresh == "" {
					return false
				}
			}
		default:
			return false
		}
	}
	return len(required) == 0
}

func matchesCandidateReferenceEvidence(want, got []analysis.ImpactReferenceEvidence) bool {
	if len(want) != len(got) {
		return false
	}
	byEdge := make(map[candidateReferenceEdge]analysis.ImpactReferenceEvidence, len(want))
	for _, ref := range want {
		byEdge[candidateReferenceEdge{ref.From, ref.To, ref.Type}] = ref
	}
	for _, ref := range got {
		key := candidateReferenceEdge{ref.From, ref.To, ref.Type}
		expected, ok := byEdge[key]
		if !ok || ref.Status != expected.Status || ref.Reason != expected.Reason ||
			ref.SourceUID != expected.SourceUID || ref.SourceResourceVersion != expected.SourceResourceVersion ||
			!slices.Equal(ref.Uses, expected.Uses) {
			return false
		}
		delete(byEdge, key)
	}
	return len(byEdge) == 0
}
