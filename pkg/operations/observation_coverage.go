// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	MaxCoverageClusters = 128
	MaxCoverageTypes    = 256
)

var ErrCoverageScope = errors.New("coverage: invalid or excessive observation scope")

type CoverageState string

const (
	CoverageUnknown      CoverageState = "unknown"
	CoverageInitializing CoverageState = "initializing"
	CoverageObserved     CoverageState = "initialized_no_known_gap"
	CoveragePartial      CoverageState = "partial"
	CoverageStale        CoverageState = "stale"
)

// Reasons are a closed vocabulary: never retain or serialize error strings,
// resource identities, credentials, or Kubernetes object payloads.
type CoverageReason string

const (
	CoverageNotWatched        CoverageReason = "resource_type_not_watched"
	CoverageInitialPending    CoverageReason = "initial_delivery_pending"
	CoverageListFailed        CoverageReason = "list_failed"
	CoverageListDenied        CoverageReason = "list_permission_denied"
	CoverageWatchPending      CoverageReason = "watch_not_established"
	CoverageWatchClosed       CoverageReason = "watch_reconnecting"
	CoverageWatchFailed       CoverageReason = "watch_failed"
	CoverageWatchDenied       CoverageReason = "watch_permission_denied"
	CoverageCollectorStopped  CoverageReason = "collector_stopped"
	CoverageProcessingFailed  CoverageReason = "processing_gap"
	CoverageExtractionFailed  CoverageReason = "extraction_gap"
	CoveragePersistenceFailed CoverageReason = "persistence_gap"
)

type ResourceCoverage struct {
	// Monotonic within a session, including transient failures that recover
	// before a second capture. Ordinary successful object delivery is not a gap.
	ContinuityRevision  uint64           `json:"continuityRevision"`
	Group               string           `json:"group"`
	Version             string           `json:"version"`
	Resource            string           `json:"resource"`
	State               CoverageState    `json:"state"`
	Reasons             []CoverageReason `json:"reasons"`
	Since               time.Time        `json:"since"`
	InitialDeliveryDone bool             `json:"initialDeliveryDone"`
	WatchEstablished    bool             `json:"watchEstablished"`
	LastListSuccess     time.Time        `json:"lastListSuccess"`
	WatchGapSince       time.Time        `json:"watchGapSince"`
	ProcessingGapSince  time.Time        `json:"processingGapSince"`
	ExtractionGapSince  time.Time        `json:"extractionGapSince"`
	PersistenceGapSince time.Time        `json:"persistenceGapSince"`
}

// CoverageSnapshot is a point-in-time, single-cluster observation record, not a
// Kubernetes transactional snapshot or a traversal result. Callers must apply
// visibility BEFORE requesting a snapshot. Missing requested types stay unknown;
// requesting no types is unknown, never a vacuous complete observation.
type CoverageSnapshot struct {
	// Process-local session identity; not a durable restart/history token.
	Generation   uint64                   `json:"generation"`
	ClusterID    string                   `json:"clusterId"`
	CapturedAt   time.Time                `json:"capturedAt"`
	State        CoverageState            `json:"state"`
	Resources    []ResourceCoverage       `json:"resources"`
	Sources      []CoverageSourceSnapshot `json:"sources,omitempty"`
	APIInventory *APIInventoryEvidence    `json:"apiInventory,omitempty"`
}

// CoverageTracker is process-local, bounded by clusters/types, never objects.
// It is separate from legacy probe-based ObservationState. A successful API
// probe cannot clear a per-resource watch failure or a sticky processing gap.
type CoverageTracker struct {
	mu             sync.RWMutex
	staleAfter     time.Duration
	clusters       map[string]*CoverageSession
	sources        map[string]map[CoverageSource]*CoverageSession
	inventories    map[string]*APIInventorySession
	nextGeneration uint64
}

type coverageResource struct {
	ResourceCoverage
	listReason  CoverageReason
	watchReason CoverageReason
	watchEpoch  uint64
	stopped     bool
}

// Sessions isolate reattachments: callbacks from a stopped/replaced collector
// cannot change the active session. Known data gaps survive reattachment in the
// same process; relisting/resync does not prove lost deletes/edges repaired.
type CoverageSession struct {
	generation uint64
	tracker    *CoverageTracker
	cluster    string
	resources  map[schema.GroupVersionResource]*coverageResource
	source     CoverageSource
	parent     *CoverageSession
	stopped    bool
	limited    bool
	discovery  *SourceDiscoveryEvidence
}

func NewCoverageTracker(staleAfter time.Duration) *CoverageTracker {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	return &CoverageTracker{staleAfter: staleAfter, clusters: make(map[string]*CoverageSession), sources: make(map[string]map[CoverageSource]*CoverageSession), inventories: make(map[string]*APIInventorySession)}
}

func (t *CoverageTracker) Begin(cluster string, gvrs []schema.GroupVersionResource, now time.Time) (*CoverageSession, error) {
	return t.begin(cluster, "", gvrs, now)
}

func (t *CoverageTracker) begin(cluster string, source CoverageSource, gvrs []schema.GroupVersionResource, now time.Time) (*CoverageSession, error) {
	if !validCoverageScope(cluster, gvrs) || now.IsZero() {
		return nil, ErrCoverageScope
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := t.sessionLocked(cluster, source)
	if !t.hasClusterLocked(cluster) && t.clusterCountLocked() >= MaxCoverageClusters {
		return nil, ErrCoverageScope
	}
	s := &CoverageSession{tracker: t, cluster: cluster, source: source, resources: make(map[schema.GroupVersionResource]*coverageResource)}
	// Retain stopped types too: dropping/re-adding a GVR must not erase a
	// previously observed data gap. The union stays under the same hard cap.
	if previous != nil {
		s.limited = previous.limited
		for gvr, old := range previous.resources {
			copy := *old
			copy.stopped, copy.WatchEstablished = true, false
			if copy.WatchGapSince.IsZero() {
				copy.WatchGapSince = now
			}
			s.resources[gvr] = &copy
		}
	}
	for _, gvr := range gvrs {
		r := &coverageResource{ResourceCoverage: ResourceCoverage{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Since: now}, watchReason: CoverageWatchPending}
		if previous != nil && previous.resources[gvr] != nil {
			old := previous.resources[gvr]
			r.ProcessingGapSince, r.ExtractionGapSince, r.PersistenceGapSince = old.ProcessingGapSince, old.ExtractionGapSince, old.PersistenceGapSince
		}
		s.resources[gvr] = r
	}
	if len(s.resources) > MaxCoverageTypes {
		return nil, ErrCoverageScope
	}
	t.nextGeneration++
	s.generation = t.nextGeneration
	for _, r := range s.resources {
		r.ContinuityRevision = 1
	}
	if source == "" {
		t.clusters[cluster] = s
	} else {
		if t.sources[cluster] == nil {
			t.sources[cluster] = make(map[CoverageSource]*CoverageSession)
		}
		t.sources[cluster][source] = s
	}
	return s, nil
}

func validCoverageScope(cluster string, gvrs []schema.GroupVersionResource) bool {
	if len(cluster) > 253 || strings.ContainsAny(cluster, "/:") || len(gvrs) > MaxCoverageTypes {
		return false
	}
	for _, g := range gvrs {
		if g.Version == "" || g.Resource == "" || len(g.Group)+len(g.Version)+len(g.Resource) > 768 || strings.ContainsAny(g.Group+g.Version+g.Resource, "/:\x00") {
			return false
		}
	}
	return true
}

func (s *CoverageSession) update(gvr schema.GroupVersionResource, apply func(*coverageResource)) {
	if s == nil {
		return
	}
	s.tracker.mu.Lock()
	defer s.tracker.mu.Unlock()
	if s.activeLocked() {
		if r := s.resources[gvr]; r != nil && !r.stopped && s.ownsTypeLocked(gvr, r) {
			apply(r)
		}
	}
}

// InitialDeliveryComplete is called only when the handler registration (not
// merely the shared cache) has delivered its initial list. Failed writes remain
// gaps even though the handler has returned from all initial callbacks.
func (s *CoverageSession) InitialDeliveryComplete(gvr schema.GroupVersionResource) {
	s.update(gvr, func(r *coverageResource) {
		if !r.InitialDeliveryDone {
			r.ContinuityRevision++
			r.InitialDeliveryDone = true
		}
	})
}

func (s *CoverageSession) ListResult(gvr schema.GroupVersionResource, success, denied bool, now time.Time) {
	s.update(gvr, func(r *coverageResource) {
		r.ContinuityRevision++
		if success {
			r.LastListSuccess, r.listReason = now, ""
		} else if denied {
			r.listReason = CoverageListDenied
		} else {
			r.listReason = CoverageListFailed
		}
	})
}

func (s *CoverageSession) WatchStarted(gvr schema.GroupVersionResource) uint64 {
	var epoch uint64
	s.update(gvr, func(r *coverageResource) {
		r.ContinuityRevision++
		r.watchEpoch++
		epoch = r.watchEpoch
		r.WatchEstablished, r.watchReason, r.WatchGapSince = true, "", time.Time{}
	})
	return epoch
}

// epoch=0 records a failed attempt before a watch was established. Old streams
// cannot close or degrade a newer watch. No event/bookmark timestamp is needed.
func (s *CoverageSession) WatchEnded(gvr schema.GroupVersionResource, epoch uint64, reason CoverageReason, now time.Time) {
	if reason != CoverageWatchClosed && reason != CoverageWatchDenied && reason != CoverageWatchFailed {
		reason = CoverageWatchFailed
	}
	s.update(gvr, func(r *coverageResource) {
		if epoch != 0 && r.watchEpoch != epoch {
			return
		}
		r.ContinuityRevision++
		r.WatchEstablished = false
		if r.WatchGapSince.IsZero() {
			r.WatchGapSince = now
		}
		// A stream's final close must not erase the preceding error reason.
		if reason != CoverageWatchClosed || r.watchReason == "" || r.watchReason == CoverageWatchPending {
			r.watchReason = reason
		}
	})
}

func (s *CoverageSession) RecordGap(gvr schema.GroupVersionResource, reason CoverageReason, now time.Time) {
	s.update(gvr, func(r *coverageResource) {
		var at *time.Time
		switch reason {
		case CoverageExtractionFailed:
			at = &r.ExtractionGapSince
		case CoveragePersistenceFailed:
			at = &r.PersistenceGapSince
		default:
			at = &r.ProcessingGapSince
		}
		if at.IsZero() {
			r.ContinuityRevision++
			*at = now
		}
	})
}

func (s *CoverageSession) Stop(now time.Time) {
	if s == nil {
		return
	}
	s.tracker.mu.Lock()
	defer s.tracker.mu.Unlock()
	if !s.activeLocked() {
		return
	}
	for gvr, r := range s.resources {
		if !r.stopped && s.ownsTypeLocked(gvr, r) {
			r.ContinuityRevision++
			r.stopped, r.WatchEstablished = true, false
			if r.WatchGapSince.IsZero() {
				r.WatchGapSince = now
			}
		}
	}
	s.stopped = true
}

func (t *CoverageTracker) Snapshot(cluster string, required []schema.GroupVersionResource, now time.Time) (CoverageSnapshot, error) {
	return t.snapshot(context.Background(), cluster, required, func() time.Time { return now })
}

// SnapshotCoverage is the context-aware seam for a bounded analysis read. Its
// timestamp is taken under the read lock, not before a possibly contended wait.
func (t *CoverageTracker) SnapshotCoverage(ctx context.Context, cluster string, required []schema.GroupVersionResource) (CoverageSnapshot, error) {
	return t.snapshot(ctx, cluster, required, time.Now)
}

func (t *CoverageTracker) snapshot(ctx context.Context, cluster string, required []schema.GroupVersionResource, clock func() time.Time) (CoverageSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CoverageSnapshot{}, err
	}
	if !validCoverageScope(cluster, required) {
		return CoverageSnapshot{}, ErrCoverageScope
	}
	// Bound waiting as well as copying. No detached goroutine can acquire the
	// lock after a cancelled caller has returned.
	if !t.mu.TryRLock() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for !t.mu.TryRLock() {
			select {
			case <-ctx.Done():
				return CoverageSnapshot{}, ctx.Err()
			case <-tick.C:
			}
		}
	}
	defer t.mu.RUnlock()
	now := clock()
	if now.IsZero() {
		return CoverageSnapshot{}, ErrCoverageScope
	}
	out := CoverageSnapshot{ClusterID: cluster, CapturedAt: now, State: CoverageUnknown, Resources: []ResourceCoverage{}}
	if inventory := t.inventories[cluster]; inventory != nil {
		out.APIInventory = CloneAPIInventory(&inventory.evidence)
		out.APIInventory.Stale = !out.APIInventory.CheckedAt.IsZero() && now.Sub(out.APIInventory.CheckedAt) >= APIInventoryMaxAge
	}
	session := t.clusters[cluster]
	if session != nil {
		out.Generation = session.generation
	}
	for _, source := range []CoverageSource{CoverageSourceCRD, CoverageSourceGatekeeper} {
		if s := t.sessionLocked(cluster, source); s != nil {
			capture, err := s.snapshotSourceLocked(ctx, now, t.staleAfter)
			if err != nil {
				return CoverageSnapshot{}, err
			}
			out.Sources = append(out.Sources, capture)
		}
	}
	seen := make(map[schema.GroupVersionResource]bool)
	for _, gvr := range required {
		if err := ctx.Err(); err != nil {
			return CoverageSnapshot{}, err
		}
		if seen[gvr] {
			continue
		}
		seen[gvr] = true
		r := ResourceCoverage{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, State: CoverageUnknown, Reasons: []CoverageReason{CoverageNotWatched}}
		if session != nil && session.resources[gvr] != nil {
			r = session.resources[gvr].snapshot(now, t.staleAfter)
		}
		out.Resources = append(out.Resources, r)
	}
	slices.SortFunc(out.Resources, func(a, b ResourceCoverage) int {
		return strings.Compare(a.Group+"/"+a.Version+"/"+a.Resource, b.Group+"/"+b.Version+"/"+b.Resource)
	})
	if len(out.Resources) > 0 {
		out.State = CoverageObserved
		unknown := 0
		for _, r := range out.Resources {
			if r.State == CoverageUnknown {
				unknown++
			}
			if coverageRank(r.State) > coverageRank(out.State) {
				out.State = r.State
			}
		}
		if out.State == CoverageUnknown && unknown < len(out.Resources) {
			out.State = CoveragePartial
		}
	}
	if err := ctx.Err(); err != nil {
		return CoverageSnapshot{}, err
	}
	return out, nil
}

func (r *coverageResource) snapshot(now time.Time, staleAfter time.Duration) ResourceCoverage {
	out := r.ResourceCoverage
	out.Reasons = []CoverageReason{}
	out.State = CoverageObserved
	if !r.InitialDeliveryDone {
		out.State = CoverageInitializing
		out.Reasons = append(out.Reasons, CoverageInitialPending)
	}
	if r.listReason != "" {
		out.State = CoveragePartial
		out.Reasons = append(out.Reasons, r.listReason)
	}
	if r.stopped {
		out.State = CoveragePartial
		out.Reasons = append(out.Reasons, CoverageCollectorStopped)
	} else if !r.WatchEstablished {
		if r.watchReason != CoverageWatchPending {
			out.State = CoveragePartial
		}
		out.Reasons = append(out.Reasons, r.watchReason)
		if out.State == CoverageObserved {
			out.State = CoverageInitializing
		}
	}
	for _, gap := range []struct {
		at     time.Time
		reason CoverageReason
	}{{r.ProcessingGapSince, CoverageProcessingFailed}, {r.ExtractionGapSince, CoverageExtractionFailed}, {r.PersistenceGapSince, CoveragePersistenceFailed}} {
		if !gap.at.IsZero() {
			out.State = CoveragePartial
			out.Reasons = append(out.Reasons, gap.reason)
		}
	}
	// Staleness requires an actual disconnected/stopped watch, never an old
	// resource event, missing bookmark, or a quiet but established watch.
	if !r.WatchGapSince.IsZero() && now.Sub(r.WatchGapSince) >= staleAfter {
		out.State = CoverageStale
	}
	return out
}

func coverageRank(s CoverageState) int {
	switch s {
	case CoverageStale:
		return 4
	case CoveragePartial:
		return 3
	case CoverageInitializing:
		return 2
	case CoverageUnknown:
		return 1
	default:
		return 0
	}
}
