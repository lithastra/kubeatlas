// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Sources are a closed vocabulary, not caller-supplied collector names.
type CoverageSource string

const (
	CoverageSourceCRD        CoverageSource = "crd"
	CoverageSourceGatekeeper CoverageSource = "gatekeeper"
)

// CoverageSourceSnapshot describes only registered types, including stopped
// ones. Even an initialized source is NOT proof of a complete API inventory.
// No object identity, raw error, or object payload is retained here.
type CoverageSourceSnapshot struct {
	ClusterID        string                   `json:"clusterId"`
	Source           CoverageSource           `json:"source"`
	Generation       uint64                   `json:"generation"`
	State            CoverageState            `json:"state"`
	InventoryLimited bool                     `json:"inventoryLimited"`
	Resources        []ResourceCoverage       `json:"resources"`
	Discovery        *SourceDiscoveryEvidence `json:"discovery,omitempty"`
}

func (t *CoverageTracker) BeginSource(cluster string, source CoverageSource, now time.Time) (*CoverageSession, error) {
	if source != CoverageSourceCRD && source != CoverageSourceGatekeeper {
		return nil, ErrCoverageScope
	}
	return t.begin(cluster, source, nil, now)
}

// BeginType creates a callback token for exactly one registration. Replacement
// cannot accept late callbacks from an old registration, including list/watch
// adapters. Stopped rows and sticky data gaps survive re-registration. Capacity
// exhaustion stays visible; collectors may continue without claiming coverage.
func (s *CoverageSession) BeginType(gvr schema.GroupVersionResource, now time.Time) (*CoverageSession, error) {
	if s == nil || s.source == "" || s.parent != nil || !validCoverageScope(s.cluster, []schema.GroupVersionResource{gvr}) || now.IsZero() || (gvr.Group == "" && gvr.Resource == "secrets") {
		return nil, ErrCoverageScope
	}
	s.tracker.mu.Lock()
	defer s.tracker.mu.Unlock()
	if !s.activeLocked() {
		return nil, ErrCoverageScope
	}
	old := s.resources[gvr]
	if old != nil && !old.stopped {
		return nil, ErrCoverageScope
	}
	if old == nil && len(s.resources) >= MaxCoverageTypes {
		s.limited = true
		return nil, ErrCoverageScope
	}
	r := &coverageResource{ResourceCoverage: ResourceCoverage{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Since: now, ContinuityRevision: 1}, watchReason: CoverageWatchPending}
	if old != nil {
		r.ContinuityRevision = old.ContinuityRevision + 1
		r.ProcessingGapSince, r.ExtractionGapSince, r.PersistenceGapSince = old.ProcessingGapSince, old.ExtractionGapSince, old.PersistenceGapSince
	}
	s.resources[gvr] = r
	return &CoverageSession{tracker: s.tracker, cluster: s.cluster, source: s.source, parent: s, resources: map[schema.GroupVersionResource]*coverageResource{gvr: r}}, nil
}

func (t *CoverageTracker) sessionLocked(cluster string, source CoverageSource) *CoverageSession {
	if source == "" {
		return t.clusters[cluster]
	}
	return t.sources[cluster][source]
}

func (s *CoverageSession) activeLocked() bool {
	owner := s
	if s.parent != nil {
		owner = s.parent
	}
	return !s.stopped && !owner.stopped && s.tracker.sessionLocked(s.cluster, s.source) == owner
}

func (s *CoverageSession) ownsTypeLocked(gvr schema.GroupVersionResource, r *coverageResource) bool {
	return s.parent == nil || s.parent.resources[gvr] == r
}

func (t *CoverageTracker) hasClusterLocked(cluster string) bool {
	return t.clusters[cluster] != nil || t.sources[cluster] != nil || t.inventories[cluster] != nil
}

func (t *CoverageTracker) clusterCountLocked() int {
	n := len(t.clusters)
	for cluster := range t.sources {
		if t.clusters[cluster] == nil {
			n++
		}
	}
	for cluster := range t.inventories {
		if t.clusters[cluster] == nil && t.sources[cluster] == nil {
			n++
		}
	}
	return n
}

func (s *CoverageSession) snapshotSourceLocked(ctx context.Context, now time.Time, staleAfter time.Duration) (CoverageSourceSnapshot, error) {
	out := CoverageSourceSnapshot{ClusterID: s.cluster, Source: s.source, Generation: s.generation, State: CoverageObserved, InventoryLimited: s.limited, Resources: []ResourceCoverage{}}
	for _, r := range s.resources {
		if err := ctx.Err(); err != nil {
			return CoverageSourceSnapshot{}, err
		}
		row := r.snapshot(now, staleAfter)
		out.Resources = append(out.Resources, row)
		if coverageRank(row.State) > coverageRank(out.State) {
			out.State = row.State
		}
	}
	if len(out.Resources) == 0 {
		out.State = CoverageUnknown
	}
	if (s.limited || s.stopped) && coverageRank(out.State) < coverageRank(CoveragePartial) {
		out.State = CoveragePartial
	}
	if s.discovery != nil {
		copy := *s.discovery
		copy.Stale = !copy.CheckedAt.IsZero() && now.Sub(copy.CheckedAt) >= SourceDiscoveryMaxAge
		out.Discovery = &copy
		if (copy.State != SourceAPIAdvertised || copy.Stale) && coverageRank(out.State) < coverageRank(CoveragePartial) {
			out.State = CoveragePartial
		}
	}
	slices.SortFunc(out.Resources, func(a, b ResourceCoverage) int {
		return strings.Compare(a.Group+"/"+a.Version+"/"+a.Resource, b.Group+"/"+b.Version+"/"+b.Resource)
	})
	return out, nil
}
