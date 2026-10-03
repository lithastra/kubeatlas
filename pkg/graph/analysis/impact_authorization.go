// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"container/heap"
	"context"
	"errors"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

var ErrImpactAuthorizationLimit = errors.New("impact: authorization explanation budget exceeded")

const (
	ImpactAuthorizationInvalid     ImpactNoticeReason = "invalid_authorization_association"
	ImpactAuthorizationAmbiguous   ImpactNoticeReason = "ambiguous_authorization_reference"
	ImpactAuthorizationMissingRole ImpactNoticeReason = "missing_role_reference"
	ImpactAuthorizationMissingSA   ImpactNoticeReason = "missing_serviceaccount_reference"
	ImpactAuthorizationSubject     ImpactNoticeReason = "user_or_group_not_modeled"
)

var impactAuthorizationNoticeOrder = [...]ImpactNoticeReason{
	ImpactCrossClusterExcluded, ImpactMissingResource, ImpactAuthorizationInvalid,
	ImpactAuthorizationAmbiguous, ImpactAuthorizationMissingRole, ImpactAuthorizationMissingSA,
	ImpactAuthorizationSubject,
}

// These are stored-edge associations, NOT effective permissions or evidence
// that a process used credentials. No rules, subjects' attributes, or Raw fields
// are read. Binding scope comes from the binding, never from a ClusterRole or
// the namespace of a subject. Edge/source-version binding remains unknown.
type ImpactAuthorizationStep struct {
	ImpactStep
	BindingScope     string `json:"bindingScope,omitempty"`
	BindingNamespace string `json:"bindingNamespace,omitempty"`
}

type ImpactAuthorizationMatch struct {
	Resource ImpactIdentity            `json:"resource"`
	Depth    int                       `json:"depth"`
	Path     []ImpactAuthorizationStep `json:"path"`
}

type ImpactAuthorizationCounts struct {
	Total           int  `json:"total"`
	Direct          int  `json:"direct"`
	Indirect        int  `json:"indirect"`
	Roles           int  `json:"roles"`
	Bindings        int  `json:"bindings"`
	ServiceAccounts int  `json:"serviceAccounts"`
	Workloads       int  `json:"workloads"`
	Pods            int  `json:"pods"`
	LowerBound      bool `json:"lowerBound"`
}

// Authorization is independent of ordinary relation/counts. The query's depth
// and result limits apply separately to this facet, not to their combined sum.
// Complete means complete only for the supplied, supported association motif.
type ImpactAuthorizationResult struct {
	Applicable               bool                       `json:"applicable"`
	Reason                   string                     `json:"reason,omitempty"`
	EvidenceCategory         string                     `json:"evidenceCategory"`
	SourceVersionEvidence    string                     `json:"sourceVersionEvidence"`
	EffectivePermissions     string                     `json:"effectivePermissions"`
	ObservationCoverage      string                     `json:"observationCoverage"`
	MaxDepth                 int                        `json:"maxDepth"`
	Limit                    int                        `json:"limit"`
	Resources                []ImpactAuthorizationMatch `json:"resources"`
	Counts                   ImpactAuthorizationCounts  `json:"counts"`
	Truncated                bool                       `json:"truncated"`
	TruncationReasons        []string                   `json:"truncationReasons"`
	Notices                  []ImpactNotice             `json:"notices"`
	ModeledTraversalComplete bool                       `json:"modeledTraversalComplete"`
}

func analyzeImpactAuthorization(ctx context.Context, edges []graph.Edge, resources map[string]graph.Resource, root graph.Resource, opts ImpactOptions) (ImpactAuthorizationResult, error) {
	result := ImpactAuthorizationResult{
		EvidenceCategory: "stored_edge_association", SourceVersionEvidence: "unknown",
		EffectivePermissions: "not_evaluated", ObservationCoverage: "unknown",
		MaxDepth: opts.MaxDepth, Limit: opts.Limit, Resources: []ImpactAuthorizationMatch{},
		TruncationReasons: []string{}, Notices: []ImpactNotice{},
	}
	rootClass := impactAuthorizationClass(root)
	if rootClass == "" {
		result.Reason = "unsupported_authorization_root"
		return result, ctx.Err()
	}
	result.Applicable = true
	// Deduplicate before checking cardinality. Even a dangling/foreign second
	// role target must not be concealed by subsequently filtering endpoints.
	unique := make(map[impactEdgeKey]bool)
	roleRefs, accountRefs := make(map[string]int), make(map[string]int)
	for _, edge := range edges {
		if err := ctx.Err(); err != nil {
			return ImpactAuthorizationResult{}, err
		}
		if edge.Type != graph.EdgeTypeBindsRole && edge.Type != graph.EdgeTypeBindsSubject && edge.Type != graph.EdgeTypeUsesServiceAccount {
			continue
		}
		if _, from := resources[edge.From]; !from {
			if _, to := resources[edge.To]; !to {
				continue
			}
		}
		key := impactEdgeKey{edge.From, edge.To, edge.Type}
		if unique[key] {
			continue
		}
		unique[key] = true
		switch edge.Type {
		case graph.EdgeTypeBindsRole:
			roleRefs[edge.From]++
		case graph.EdgeTypeUsesServiceAccount:
			accountRefs[edge.From]++
		}
	}
	adjacency := make(map[string]*impactArcHeap)
	notices := make(map[string]map[ImpactNoticeReason]int)
	for edge := range unique {
		if err := ctx.Err(); err != nil {
			return ImpactAuthorizationResult{}, err
		}
		for _, reverse := range []bool{false, true} {
			from, to := edge.from, edge.to
			if reverse {
				from, to = to, from
			}
			if !impactAuthorizationTransition(rootClass, impactAuthorizationClass(resources[from]), edge.edgeType, reverse) {
				continue
			}
			reason := impactAuthorizationEdgeReason(edge, resources, opts.ClusterID, roleRefs, accountRefs)
			if reason != "" {
				addImpactNotice(notices, from, reason)
				continue
			}
			if adjacency[from] == nil {
				adjacency[from] = &impactArcHeap{}
			}
			heap.Push(adjacency[from], ImpactStep{From: edge.from, To: edge.to, Type: edge.edgeType, TraversalFrom: from, TraversalTo: to})
		}
	}
	queue := []ImpactAuthorizationMatch{{Resource: impactIdentity(root)}}
	visited := map[string]bool{root.ID(): true}
	totals := make(map[ImpactNoticeReason]int)
	depthLimited, resultLimited := false, false
	// Reserve fixed result metadata, counts, and the bounded notice vocabulary.
	// This bounds this facet's encoding, not the eventual combined HTTP result.
	remaining := (16 << 20) - 4096
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return ImpactAuthorizationResult{}, err
		}
		current := queue[head]
		id := current.Resource.ID
		for reason, count := range notices[id] {
			totals[reason] += count
		}
		class := impactAuthorizationClass(resources[id])
		if class == "binding" && roleRefs[id] == 0 {
			totals[ImpactAuthorizationMissingRole]++
		}
		if class == "workload" && accountRefs[id] == 0 {
			totals[ImpactAuthorizationMissingSA]++
		}
		arcs := adjacency[id]
		for arcs != nil && arcs.Len() > 0 {
			if err := ctx.Err(); err != nil {
				return ImpactAuthorizationResult{}, err
			}
			step := (*arcs)[0]
			heap.Pop(arcs)
			if visited[step.TraversalTo] {
				continue
			}
			if current.Depth == opts.MaxDepth {
				depthLimited = true
				continue
			}
			if len(result.Resources) == opts.Limit {
				resultLimited = true
				continue
			}
			resource := impactIdentity(resources[step.TraversalTo])
			next := ImpactAuthorizationStep{ImpactStep: step}
			if step.Type == graph.EdgeTypeBindsRole || step.Type == graph.EdgeTypeBindsSubject {
				binding := resources[step.From]
				next.BindingScope = "cluster"
				if binding.Kind == "RoleBinding" {
					next.BindingScope, next.BindingNamespace = "namespace", binding.Namespace
				}
			}
			// Charge before copying potentially large paths/identities. Strings
			// are immutable; path slices are independently owned per result.
			if err := chargeImpactAuthorization(&remaining, 512, resource.ID, resource.ClusterID, resource.Namespace, resource.Kind, resource.Name, resource.GroupVersion, resource.UID, resource.ResourceVersion); err != nil {
				return ImpactAuthorizationResult{}, err
			}
			for _, path := range [][]ImpactAuthorizationStep{current.Path, {next}} {
				for _, s := range path {
					if err := chargeImpactAuthorization(&remaining, 256, s.From, s.To, s.TraversalFrom, s.TraversalTo, s.BindingNamespace); err != nil {
						return ImpactAuthorizationResult{}, err
					}
				}
			}
			path := make([]ImpactAuthorizationStep, len(current.Path)+1)
			copy(path, current.Path)
			path[len(current.Path)] = next
			match := ImpactAuthorizationMatch{Resource: resource, Depth: current.Depth + 1, Path: path}
			visited[step.TraversalTo] = true
			queue = append(queue, match)
			result.Resources = append(result.Resources, match)
			result.Counts.add(match)
		}
	}
	if depthLimited {
		result.TruncationReasons = append(result.TruncationReasons, "max_depth")
	}
	if resultLimited {
		result.TruncationReasons = append(result.TruncationReasons, "max_results")
	}
	for _, reason := range impactAuthorizationNoticeOrder {
		if totals[reason] > 0 {
			result.Notices = append(result.Notices, ImpactNotice{Reason: reason, Count: totals[reason]})
		}
	}
	result.Truncated = depthLimited || resultLimited
	result.Counts.LowerBound = result.Truncated || len(result.Notices) > 0
	result.ModeledTraversalComplete = !result.Counts.LowerBound
	return result, ctx.Err()
}

// Finite, root-specific motifs, not a connected-component walk. In particular,
// a workload cannot reach peer workloads or other subjects via a shared role.
func impactAuthorizationTransition(root, current string, edge graph.EdgeType, reverse bool) bool {
	switch current {
	case "role":
		return root == "role" && edge == graph.EdgeTypeBindsRole && reverse
	case "binding":
		return edge == graph.EdgeTypeBindsRole && !reverse && root != "role" ||
			edge == graph.EdgeTypeBindsSubject && !reverse && (root == "role" || root == "binding")
	case "account":
		return edge == graph.EdgeTypeBindsSubject && reverse && (root == "account" || root == "workload") ||
			edge == graph.EdgeTypeUsesServiceAccount && reverse && root != "workload"
	case "workload":
		return root == "workload" && edge == graph.EdgeTypeUsesServiceAccount && !reverse
	}
	return false
}

func impactAuthorizationClass(r graph.Resource) string {
	class, version, namespaced := "", "", true
	switch r.Kind {
	case "Role", "ClusterRole":
		class, version, namespaced = "role", "rbac.authorization.k8s.io/v1", r.Kind == "Role"
	case "RoleBinding", "ClusterRoleBinding":
		class, version, namespaced = "binding", "rbac.authorization.k8s.io/v1", r.Kind == "RoleBinding"
	case "ServiceAccount":
		class, version = "account", "v1"
	case "Pod":
		class, version = "workload", "v1"
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		class, version = "workload", "apps/v1"
	case "Job", "CronJob":
		class, version = "workload", "batch/v1"
	default:
		return ""
	}
	if (r.Namespace != "") != namespaced || r.GroupVersion != "" && r.GroupVersion != version {
		return ""
	}
	return class
}

func impactAuthorizationEdgeReason(edge impactEdgeKey, resources map[string]graph.Resource, cluster string, roles, accounts map[string]int) ImpactNoticeReason {
	if !graph.ImpactIDInCluster(edge.from, cluster) || !graph.ImpactIDInCluster(edge.to, cluster) {
		return ImpactCrossClusterExcluded
	}
	if edge.edgeType == graph.EdgeTypeBindsRole && roles[edge.from] > 1 || edge.edgeType == graph.EdgeTypeUsesServiceAccount && accounts[edge.from] > 1 {
		return ImpactAuthorizationAmbiguous
	}
	from, hasFrom := resources[edge.from]
	to, hasTo := resources[edge.to]
	if edge.edgeType == graph.EdgeTypeBindsSubject {
		// Users/groups are often intentionally dangling IDs: do not synthesize
		// resource nodes, assume membership, or reveal their names in notices.
		parts := strings.SplitN(edge.to, "/", 3)
		if len(parts) == 3 && (parts[1] == "User" || parts[1] == "Group") {
			return ImpactAuthorizationSubject
		}
	}
	if !hasFrom || !hasTo {
		return ImpactMissingResource
	}
	fromClass, toClass := impactAuthorizationClass(from), impactAuthorizationClass(to)
	valid := false
	switch edge.edgeType {
	case graph.EdgeTypeBindsRole:
		valid = fromClass == "binding" && toClass == "role" &&
			(to.Kind == "ClusterRole" || from.Kind == "RoleBinding" && from.Namespace == to.Namespace)
	case graph.EdgeTypeBindsSubject:
		valid = fromClass == "binding" && toClass == "account"
	case graph.EdgeTypeUsesServiceAccount:
		valid = fromClass == "workload" && toClass == "account" && from.Namespace == to.Namespace
	}
	if !valid {
		return ImpactAuthorizationInvalid
	}
	return ""
}

func chargeImpactAuthorization(remaining *int, overhead int, fields ...string) error {
	if *remaining < overhead {
		return ErrImpactAuthorizationLimit
	}
	*remaining -= overhead
	for _, field := range fields {
		if len(field) > *remaining/6 {
			return ErrImpactAuthorizationLimit
		}
		*remaining -= 6 * len(field)
	}
	return nil
}

func (c *ImpactAuthorizationCounts) add(match ImpactAuthorizationMatch) {
	c.Total++
	if match.Depth == 1 {
		c.Direct++
	} else {
		c.Indirect++
	}
	switch match.Resource.Kind {
	case "Role", "ClusterRole":
		c.Roles++
	case "RoleBinding", "ClusterRoleBinding":
		c.Bindings++
	case "ServiceAccount":
		c.ServiceAccounts++
	case "Pod":
		c.Pods++
	default:
		c.Workloads++
	}
}
