// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
)

const (
	memberRepairInterval = 5 * time.Minute
	memberRepairTimeout  = 10 * time.Second
	memberRepairLimit    = 10000
	memberRepairPageSize = 500
)

// MemberRepair reconciles retained member rows against fresh, complete lists
// for actively registered endpoints only. It never treats API absence, stopped
// registrations, failed/partial lists or local informer resync as deletion proof.
// The member bundle owns it; each registration owns and joins its Run lifetime.
type MemberRepair struct {
	client   dynamic.Interface
	store    graph.GraphStore
	cluster  string
	interval time.Duration
	slot     chan struct{}
	mu       sync.Mutex
	writes   map[string]*memberResourceLock
}

type memberResourceLock struct {
	slot chan struct{}
	refs int
}

func NewMemberRepair(client dynamic.Interface, store graph.GraphStore, cluster string) *MemberRepair {
	return &MemberRepair{client: client, store: store, cluster: cluster, interval: memberRepairInterval, slot: make(chan struct{}, 1), writes: make(map[string]*memberResourceLock)}
}

// LockResource serializes a complete object callback with repair deletion and
// its notification. This prevents a blocked extractor from writing incident
// edges after repair has removed the node. Locks are retained only while owned
// or awaited; unrelated objects do not share a graph-delivery lock.
func (r *MemberRepair) LockResource(ctx context.Context, id string) (func(), error) {
	r.mu.Lock()
	lock := r.writes[id]
	if lock == nil {
		lock = &memberResourceLock{slot: make(chan struct{}, 1)}
		r.writes[id] = lock
	}
	lock.refs++
	r.mu.Unlock()
	releaseRef := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		lock.refs--
		if lock.refs == 0 {
			delete(r.writes, id)
		}
	}
	select {
	case lock.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock.slot
			releaseRef()
			return nil, err
		}
		return func() { <-lock.slot; releaseRef() }, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

// Run waits for initial handler delivery, then reconciles immediately and every
// five minutes. onDelete is called only after a committed conditional deletion;
// callers may record a detection-time event, never a fabricated upstream time.
func (r *MemberRepair) Run(ctx context.Context, gvr schema.GroupVersionResource, kind string, coverage *operations.CoverageSession, synced cache.InformerSynced, onDelete func(graph.Resource)) {
	if r == nil || r.cluster == "" {
		return
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced) {
		return
	}
	round := func() {
		bounded, cancel := context.WithTimeout(ctx, memberRepairTimeout)
		defer cancel()
		r.repairOnce(bounded, gvr, kind, coverage, onDelete)
	}
	round()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			round()
		}
	}
}

func (r *MemberRepair) repairOnce(ctx context.Context, gvr schema.GroupVersionResource, kind string, coverage *operations.CoverageSession, onDelete func(graph.Resource)) {
	gap := func(reason operations.CoverageReason) {
		// Registration cancellation is ordinary teardown. A round deadline is
		// still a gap: it never authorizes deletion from an incomplete listing.
		if !errors.Is(ctx.Err(), context.Canceled) {
			coverage.RecordGap(gvr, reason, time.Now())
		}
	}
	if r.cluster == "" || isCoreSecretGVR(gvr) || kind == "Secret" || !operations.ValidAPIResourceDescriptor(operations.APIResourceDescriptor{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: kind}) {
		gap(operations.CoverageProcessingFailed)
		return
	}
	if ctx.Err() != nil {
		gap(operations.CoverageProcessingFailed)
		return
	}
	// A single shared slot bounds extra repair work across all collectors in
	// this member. Waiting is included in the round deadline.
	select {
	case r.slot <- struct{}{}:
		defer func() { <-r.slot }()
	case <-ctx.Done():
		gap(operations.CoverageProcessingFailed)
		return
	}
	deleter, ok := r.store.(graph.ResourceVersionDeleter)
	if !ok {
		// Never fall back to a racy GetResource + unconditional DeleteResource.
		gap(operations.CoveragePersistenceFailed)
		return
	}
	// Capture candidates BEFORE requesting the current list. Rows arriving or
	// changing while the list is in flight cannot be swept by this round.
	rows, err := r.store.ListResourcesInCluster(ctx, r.cluster, graph.Filter{Kind: kind})
	if err != nil {
		gap(operations.CoveragePersistenceFailed)
		return
	}
	candidates := make([]graph.Resource, 0, len(rows))
	for _, row := range rows {
		if row.ClusterID != r.cluster || row.Kind != kind || row.GroupVersion != gvr.GroupVersion().String() {
			continue
		}
		if !graph.ValidResourceVersion(row) {
			gap(operations.CoverageProcessingFailed)
			continue
		}
		candidates = append(candidates, row)
	}
	if len(candidates) == 0 {
		return
	}
	if len(candidates) > memberRepairLimit {
		gap(operations.CoverageProcessingFailed)
		return
	}
	// Empty resourceVersion asks for a current consistent list, rather than
	// the reflectors' cache-friendly resourceVersion=0. Follow every continue
	// token; no mutation takes place until the whole snapshot is validated.
	opts := metav1.ListOptions{Limit: memberRepairPageSize}
	seen := make(map[string]struct{})
	tokens := make(map[string]bool)
	version := ""
	for pages := 0; ; pages++ {
		if ctx.Err() != nil || pages >= 32 {
			gap(operations.CoverageProcessingFailed)
			return
		}
		list, err := r.client.Resource(gvr).List(ctx, opts)
		if err != nil {
			// Keep repair failures sticky and separate from the informer's
			// transient list result. An informer reconnect cannot clear this.
			gap(operations.CoverageProcessingFailed)
			return
		}
		if list == nil || list.GetResourceVersion() == "" || (version != "" && version != list.GetResourceVersion()) {
			gap(operations.CoverageProcessingFailed)
			return
		}
		version = list.GetResourceVersion()
		for _, item := range list.Items {
			if item.GetName() == "" || item.GetKind() != kind || item.GetAPIVersion() != gvr.GroupVersion().String() || item.GetUID() == "" || item.GetResourceVersion() == "" {
				gap(operations.CoverageProcessingFailed)
				return
			}
			key := item.GetNamespace() + "/" + item.GetName()
			if _, duplicate := seen[key]; duplicate {
				gap(operations.CoverageProcessingFailed)
				return
			}
			seen[key] = struct{}{}
			if len(seen) > memberRepairLimit {
				gap(operations.CoverageProcessingFailed)
				return
			}
		}
		opts.Continue = list.GetContinue()
		if opts.Continue == "" {
			if remaining := list.GetRemainingItemCount(); remaining != nil && *remaining > 0 {
				gap(operations.CoverageProcessingFailed)
				return
			}
			break
		}
		if tokens[opts.Continue] {
			gap(operations.CoverageProcessingFailed)
			return
		}
		tokens[opts.Continue] = true
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			gap(operations.CoveragePersistenceFailed)
			return
		}
		if _, present := seen[candidate.Namespace+"/"+candidate.Name]; present {
			continue // Presence of any UID protects a same-name replacement.
		}
		unlock, err := r.LockResource(ctx, candidate.ID())
		if err != nil {
			gap(operations.CoveragePersistenceFailed)
			return
		}
		deleted, err := deleter.DeleteResourceVersion(ctx, candidate)
		if err != nil {
			unlock()
			gap(operations.CoveragePersistenceFailed)
			continue // Next scheduled full round retries remaining rows.
		}
		if deleted && onDelete != nil {
			onDelete(candidate)
		}
		unlock()
	}
}
