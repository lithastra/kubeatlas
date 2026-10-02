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

// ImpactRelation describes traversal, not a prediction of an outage.
type ImpactRelation string

const (
	ImpactDependents   ImpactRelation = "dependents"
	ImpactDependencies ImpactRelation = "dependencies"

	DefaultImpactDepth = 5
	MaxImpactDepth     = 10
	DefaultImpactLimit = 200
	MaxImpactLimit     = 1000
)

var (
	ErrImpactInvalidOptions    = errors.New("impact: invalid options")
	ErrImpactInvalidProjection = errors.New("impact: invalid or ambiguous graph projection")
	ErrImpactRootNotFound      = errors.New("impact: root not found in selected cluster")
	ErrImpactUIDMismatch       = errors.New("impact: root UID does not match expected instance")
	ErrImpactUIDUnknown        = errors.New("impact: root UID is unknown")
)

// ImpactOptions is an internal query contract, not an HTTP request decoder.
// Zero depth/limit and an empty relation select the defaults. Other invalid
// bounds are rejected, not clamped. ClusterID selects exactly one cluster;
// empty selects standalone resources, never every cluster.
type ImpactOptions struct {
	ClusterID   string
	Relation    ImpactRelation
	MaxDepth    int
	Limit       int
	ExpectedUID string
}

// ImpactIdentity is an allowlisted identity. It contains no object payload,
// labels, annotations, or arbitrary edge attributes. Secret identities are
// reference-only: even a malformed input cannot supply their UID/version.
type ImpactIdentity struct {
	ID              string `json:"id"`
	ClusterID       string `json:"clusterId,omitempty"`
	Namespace       string `json:"namespace"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	GroupVersion    string `json:"groupVersion,omitempty"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	ReferenceOnly   bool   `json:"referenceOnly"`
}

// ImpactStep retains the stored edge and the direction in which it was walked.
// OwnerUIDStatus is "matched" or "unknown" for OWNS; a known mismatch is
// unresolved and never traversed. These tuples do not prove source field paths,
// optionality, actual traffic, or the existence of a referenced Secret.
type ImpactStep struct {
	From           string         `json:"from"`
	To             string         `json:"to"`
	Type           graph.EdgeType `json:"type"`
	TraversalFrom  string         `json:"traversalFrom"`
	TraversalTo    string         `json:"traversalTo"`
	OwnerUIDStatus string         `json:"ownerUIDStatus,omitempty"`
}

type ImpactMatch struct {
	Resource ImpactIdentity `json:"resource"`
	Depth    int            `json:"depth"`
	Path     []ImpactStep   `json:"path"`
}

// ImpactCounts counts unique resource identities, excluding the root. Workloads
// means controller resources (including ReplicaSets), not rolled-up applications.
// Pods are disjoint from Workloads; neither count adds synthetic ancestors.
type ImpactCounts struct {
	Total      int  `json:"total"`
	Direct     int  `json:"direct"`
	Indirect   int  `json:"indirect"`
	Workloads  int  `json:"workloads"`
	Pods       int  `json:"pods"`
	Other      int  `json:"other"`
	LowerBound bool `json:"lowerBound"`
}

type ImpactNoticeReason string

const (
	ImpactAuthorizationSeparate ImpactNoticeReason = "authorization_association_excluded"
	ImpactUnmodeledRelationship ImpactNoticeReason = "unmodeled_relationship_excluded"
	ImpactRuntimeExcluded       ImpactNoticeReason = "runtime_relationship_excluded"
	ImpactCrossClusterExcluded  ImpactNoticeReason = "cross_cluster_relationship_excluded"
	ImpactMissingResource       ImpactNoticeReason = "missing_resource"
	ImpactOwnerUIDMismatch      ImpactNoticeReason = "owner_uid_mismatch"
	ImpactOwnerUIDUnknown       ImpactNoticeReason = "owner_uid_unknown"
)

var impactNoticeOrder = [...]ImpactNoticeReason{
	ImpactAuthorizationSeparate, ImpactUnmodeledRelationship, ImpactRuntimeExcluded,
	ImpactCrossClusterExcluded, ImpactMissingResource, ImpactOwnerUIDMismatch, ImpactOwnerUIDUnknown,
}

// ImpactNotice counts unique edge tuples encountered from the visited portion
// of the selected traversal, not a cluster-wide coverage inventory. It never
// includes the identity of an excluded or missing endpoint.
type ImpactNotice struct {
	Reason ImpactNoticeReason `json:"reason"`
	Count  int                `json:"count"`
}

// ImpactResult is the shared analysis DTO embedded by the v1 API envelope.
// ModeledTraversalComplete describes only this supplied graph. The pure core
// leaves observation coverage unknown; the evidence wrapper qualifies it.
// An empty/complete traversal alone cannot establish informer health, resource
// visibility, snapshot consistency, or safe deletion.
type ImpactResult struct {
	Root                     ImpactIdentity            `json:"root"`
	Relation                 ImpactRelation            `json:"relation"`
	MaxDepth                 int                       `json:"maxDepth"`
	Limit                    int                       `json:"limit"`
	Resources                []ImpactMatch             `json:"resources"`
	Counts                   ImpactCounts              `json:"counts"`
	Truncated                bool                      `json:"truncated"`
	TruncationReasons        []string                  `json:"truncationReasons"`
	Notices                  []ImpactNotice            `json:"notices"`
	ModeledTraversalComplete bool                      `json:"modeledTraversalComplete"`
	ObservationCoverage      string                    `json:"observationCoverage"`
	ReferenceEvidence        []ImpactReferenceEvidence `json:"referenceEvidence"`
	Authorization            ImpactAuthorizationResult `json:"authorization"`
	Observation              *ImpactObservation        `json:"observation,omitempty"`
	Availability             *ImpactAvailability       `json:"availability,omitempty"`
}

// AnalyzeImpact computes one deterministic shortest path per reachable resource.
// The caller must provide an immutable, authorized projection for the duration
// of the call. Cluster selection is defense in depth, NOT authorization, and this
// function does not make a GraphStore snapshot transactional. It never contacts
// Kubernetes, a store, or a runtime overlay, nor mutates the input.
//
// Breadth-first discovery breaks ties by each parent's neighbor ID, then edge
// type. Depth/result limits bound path construction; indexing requires O(V+E)
// space for the supplied graph. Context checks cover indexing and individual
// heap operations, avoiding an uninterruptible whole-graph sort. The store/API
// adapters must also bound projection reads, the overall deadline, and
// encoded response size; those are not established by this pure function.
func AnalyzeImpact(ctx context.Context, projection *graph.Graph, rootID string, opts ImpactOptions) (*ImpactResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	opts, err = normalizeImpactOptions(rootID, opts)
	if err != nil {
		return nil, err
	}
	if projection == nil {
		return nil, ErrImpactInvalidProjection
	}
	resources := make(map[string]graph.Resource)
	for _, resource := range projection.Resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resource.ClusterID != opts.ClusterID {
			continue
		}
		resource = graph.SanitizeResource(resource)
		if resource.Kind == "" || resource.Name == "" || strings.ContainsAny(resource.Namespace+resource.Kind, "/:") || strings.Contains(resource.Name, "/") {
			return nil, ErrImpactInvalidProjection
		}
		id := resource.ID()
		if _, exists := resources[id]; exists {
			// IDs omit API group. Reject collisions instead of picking whichever
			// instance/group happened to arrive last in the projection.
			return nil, ErrImpactInvalidProjection
		}
		resources[id] = resource
	}
	root, found := resources[rootID]
	if !found {
		return nil, ErrImpactRootNotFound
	}
	if opts.ExpectedUID != "" {
		if root.UID == "" {
			return nil, ErrImpactUIDUnknown
		}
		if string(root.UID) != opts.ExpectedUID {
			return nil, ErrImpactUIDMismatch
		}
	}

	adjacency := make(map[string]*impactArcHeap)
	notices := make(map[string]map[ImpactNoticeReason]int)
	seenEdges := make(map[impactEdgeKey]bool)
	for _, edge := range projection.Edges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		from, to := edge.From, edge.To
		if opts.Relation == ImpactDependents {
			from, to = to, from
		}
		if _, exists := resources[from]; !exists {
			continue
		}
		key := impactEdgeKey{edge.From, edge.To, edge.Type}
		if seenEdges[key] {
			continue
		}
		seenEdges[key] = true
		if reason := impactExcludedType(edge.Type); reason != "" {
			addImpactNotice(notices, from, reason)
			continue
		}
		if _, exists := resources[to]; !exists {
			reason := ImpactMissingResource
			if impactIDCluster(to) != opts.ClusterID {
				reason = ImpactCrossClusterExcluded
			}
			addImpactNotice(notices, from, reason)
			continue
		}
		step := ImpactStep{From: edge.From, To: edge.To, Type: edge.Type, TraversalFrom: from, TraversalTo: to}
		if edge.Type == graph.EdgeTypeOwns {
			status, err := impactOwnerUIDStatus(ctx, resources[edge.From], resources[edge.To])
			if err != nil {
				return nil, err
			}
			if status == "mismatch" {
				addImpactNotice(notices, from, ImpactOwnerUIDMismatch)
				continue
			}
			step.OwnerUIDStatus = status
			if status == "unknown" {
				addImpactNotice(notices, from, ImpactOwnerUIDUnknown)
			}
		}
		if adjacency[from] == nil {
			adjacency[from] = &impactArcHeap{}
		}
		heap.Push(adjacency[from], step)
	}

	result := &ImpactResult{
		Root: impactIdentity(root), Relation: opts.Relation, MaxDepth: opts.MaxDepth, Limit: opts.Limit,
		Resources: []ImpactMatch{}, TruncationReasons: []string{}, Notices: []ImpactNotice{},
		ObservationCoverage: "unknown",
		ReferenceEvidence:   []ImpactReferenceEvidence{},
	}
	referenceReader := newImpactReferenceReader()
	// Queue paths are at most MaxImpactDepth long; the root is visited up front
	// so cycles and self-loops cannot return it as an affected resource.
	queue := []ImpactMatch{{Resource: result.Root}}
	visited := map[string]bool{rootID: true}
	totals := make(map[ImpactNoticeReason]int)
	depthLimited, resultLimited := false, false
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[head]
		for reason, count := range notices[current.Resource.ID] {
			totals[reason] += count
		}
		arcs := adjacency[current.Resource.ID]
		for arcs != nil && arcs.Len() > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
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
			if isImpactReference(step.Type) {
				evidence, err := referenceReader.explain(ctx, resources[step.From], resources[step.To], step)
				if err != nil {
					return nil, err
				}
				result.ReferenceEvidence = append(result.ReferenceEvidence, evidence)
			}
			visited[step.TraversalTo] = true
			path := make([]ImpactStep, len(current.Path)+1)
			copy(path, current.Path)
			path[len(current.Path)] = step
			match := ImpactMatch{Resource: impactIdentity(resources[step.TraversalTo]), Depth: current.Depth + 1, Path: path}
			queue = append(queue, match)
			result.Resources = append(result.Resources, match)
			result.Counts.Total++
			if match.Depth == 1 {
				result.Counts.Direct++
			} else {
				result.Counts.Indirect++
			}
			switch match.Resource.Kind {
			case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
				result.Counts.Workloads++
			case "Pod":
				result.Counts.Pods++
			default:
				result.Counts.Other++
			}
		}
	}
	if depthLimited {
		result.TruncationReasons = append(result.TruncationReasons, "max_depth")
	}
	if resultLimited {
		result.TruncationReasons = append(result.TruncationReasons, "max_results")
	}
	for _, reason := range impactNoticeOrder {
		if totals[reason] != 0 {
			result.Notices = append(result.Notices, ImpactNotice{Reason: reason, Count: totals[reason]})
		}
	}
	result.Truncated = depthLimited || resultLimited
	result.Counts.LowerBound = result.Truncated || totals[ImpactMissingResource] > 0 || totals[ImpactOwnerUIDMismatch] > 0
	result.ModeledTraversalComplete = !result.Counts.LowerBound
	result.Authorization, err = analyzeImpactAuthorization(ctx, projection.Edges, resources, root, opts)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeImpactOptions(rootID string, opts ImpactOptions) (ImpactOptions, error) {
	if opts.Relation == "" {
		opts.Relation = ImpactDependents
	}
	if opts.MaxDepth == 0 {
		opts.MaxDepth = DefaultImpactDepth
	}
	if opts.Limit == 0 {
		opts.Limit = DefaultImpactLimit
	}
	if rootID == "" || strings.ContainsAny(opts.ClusterID, "/:") ||
		(opts.Relation != ImpactDependents && opts.Relation != ImpactDependencies) ||
		opts.MaxDepth < 1 || opts.MaxDepth > MaxImpactDepth || opts.Limit < 1 || opts.Limit > MaxImpactLimit {
		return opts, ErrImpactInvalidOptions
	}
	return opts, nil
}

func impactIdentity(resource graph.Resource) ImpactIdentity {
	resource = graph.SanitizeResource(resource)
	return ImpactIdentity{
		ID: resource.ID(), ClusterID: resource.ClusterID, Namespace: resource.Namespace,
		Kind: resource.Kind, Name: resource.Name, GroupVersion: resource.GroupVersion,
		UID: string(resource.UID), ResourceVersion: resource.ResourceVersion, ReferenceOnly: resource.Kind == "Secret",
	}
}

func impactExcludedType(edgeType graph.EdgeType) ImpactNoticeReason {
	switch edgeType {
	case graph.EdgeTypeOwns, graph.EdgeTypeUsesConfigMap, graph.EdgeTypeUsesSecret, graph.EdgeTypeMountsVolume,
		graph.EdgeTypeSelects, graph.EdgeTypeRoutesTo, graph.EdgeTypeAttachedTo, graph.EdgeTypeUsesServiceAccount:
		return ""
	case graph.EdgeTypeBindsRole, graph.EdgeTypeBindsSubject:
		return ImpactAuthorizationSeparate
	case graph.EdgeTypeCallsAtRuntime:
		return ImpactRuntimeExcluded
	default:
		return ImpactUnmodeledRelationship
	}
}

func impactIDCluster(id string) string {
	head, _, _ := strings.Cut(id, "/")
	if cluster, _, found := strings.Cut(head, ":"); found {
		return cluster
	}
	return ""
}

func addImpactNotice(notices map[string]map[ImpactNoticeReason]int, id string, reason ImpactNoticeReason) {
	if notices[id] == nil {
		notices[id] = make(map[ImpactNoticeReason]int)
	}
	notices[id][reason]++
}

func impactOwnerUIDStatus(ctx context.Context, child, owner graph.Resource) (string, error) {
	if owner.UID == "" {
		return "unknown", nil
	}
	knownMismatch := false
	for _, ref := range child.OwnerReferences {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if ref.Kind != owner.Kind || ref.Name != owner.Name {
			continue
		}
		if ref.UID == owner.UID {
			return "matched", nil
		}
		if ref.UID != "" {
			knownMismatch = true
		}
	}
	// An incomplete duplicate reference must not conceal a known replacement.
	if knownMismatch {
		return "mismatch", nil
	}
	return "unknown", nil
}

type impactEdgeKey struct {
	from, to string
	edgeType graph.EdgeType
}

// Each adjacency heap is built and consumed locally; no input slices are sorted
// or modified. A heap also permits checking cancellation between insertions and
// removals, even for a resource with a very large number of incident edges.
type impactArcHeap []ImpactStep

func (h impactArcHeap) Len() int { return len(h) }
func (h impactArcHeap) Less(i, j int) bool {
	if h[i].TraversalTo != h[j].TraversalTo {
		return h[i].TraversalTo < h[j].TraversalTo
	}
	return h[i].Type < h[j].Type
}
func (h impactArcHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *impactArcHeap) Push(value any) {
	step, ok := value.(ImpactStep)
	if !ok {
		panic("analysis: invalid impact heap element")
	}
	*h = append(*h, step)
}
func (h *impactArcHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	(*h)[last] = ImpactStep{}
	*h = (*h)[:last]
	return value
}
