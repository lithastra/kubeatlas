// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"slices"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
)

// The authorization oracle is independent of ordinary paths. Its steps may
// traverse either direction because a single RBAC motif can use both.
type impactCandidateAuthorization struct {
	Applicable *bool                                         `json:"applicable"`
	Truncated  bool                                          `json:"truncated,omitempty"`
	Paths      map[string][]analysis.ImpactAuthorizationStep `json:"paths"`
}

func (a impactCandidateAuthorization) valid(q impact.Query) bool {
	if a.Applicable == nil || a.Paths == nil || len(a.Paths) > q.Limit {
		return false
	}
	if !*a.Applicable {
		return len(a.Paths) == 0 && !a.Truncated
	}
	root := (graph.Resource{ClusterID: q.ClusterID, Namespace: q.Namespace, Kind: q.Kind, Name: q.Name}).ID()
	for target, steps := range a.Paths {
		if target == root || !graph.ImpactIDInCluster(target, q.ClusterID) || len(steps) == 0 || len(steps) > q.MaxDepth {
			return false
		}
		last := root
		visited := map[string]bool{root: true}
		for _, step := range steps {
			forward := step.TraversalFrom == step.From && step.TraversalTo == step.To
			reverse := step.TraversalFrom == step.To && step.TraversalTo == step.From
			to := step.TraversalTo
			if step.TraversalFrom != last || (!forward && !reverse) || visited[to] || !validCandidateAuthorizationStep(step, q.ClusterID) {
				return false
			}
			if _, ok := a.Paths[to]; !ok {
				return false
			}
			last, visited[to] = to, true
		}
		if last != target {
			return false
		}
	}
	return true
}

func validCandidateAuthorizationStep(step analysis.ImpactAuthorizationStep, cluster string) bool {
	if step.OwnerUIDStatus != "" {
		return false
	}
	switch step.Type {
	case graph.EdgeTypeUsesServiceAccount:
		return step.BindingScope == "" && step.BindingNamespace == ""
	case graph.EdgeTypeBindsRole, graph.EdgeTypeBindsSubject:
		binding := step.From
		if cluster != "" {
			binding = strings.TrimPrefix(binding, cluster+":")
		}
		parts := strings.SplitN(binding, "/", 3)
		if len(parts) != 3 {
			return false
		}
		switch parts[1] {
		case "RoleBinding":
			return step.BindingScope == "namespace" && parts[0] != "" && step.BindingNamespace == parts[0]
		case "ClusterRoleBinding":
			return step.BindingScope == "cluster" && parts[0] == "" && step.BindingNamespace == ""
		}
	}
	return false
}

func (a impactCandidateAuthorization) matches(got analysis.ImpactAuthorizationResult) bool {
	if got.Applicable != *a.Applicable || got.Truncated != a.Truncated ||
		got.EvidenceCategory != "stored_edge_association" || got.SourceVersionEvidence != "unknown" ||
		got.EffectivePermissions != "not_evaluated" || len(got.Resources) != len(a.Paths) {
		return false
	}
	for _, match := range got.Resources {
		if steps, ok := a.Paths[match.Resource.ID]; !ok || !slices.Equal(steps, match.Path) {
			return false
		}
	}
	return true
}
