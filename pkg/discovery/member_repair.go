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

// MemberRepair reconciles retained rows in one explicitly bound scope against fresh, complete lists
// for actively registered endpoints only. It never treats API absence, stopped
// registrations, failed/partial lists or local informer resync as deletion proof.
// The collector bundle owns it; each registration owns and joins its Run lifetime.
type MemberRepair struct {
	client     dynamic.Interface
	store      graph.GraphStore
	cluster    string
	standalone bool
	interval   time.Duration
	slot       chan struct{}
	mu         sync.Mutex
	writes     map[string]*memberResourceLock
}

type memberResourceLock struct {
	slot chan struct{}
	refs int
}

// NewMemberRepair binds reconciliation and callback locking to a member.
// A nil client leaves only callback locking available.
func NewMemberRepair(client dynamic.Interface, store graph.GraphStore, cluster string) *MemberRepair {
	return &MemberRepair{client: client, store: store, cluster: cluster, interval: memberRepairInterval, slot: make(chan struct{}, 1), writes: make(map[string]*memberResourceLock)}
}

// NewStandaloneRepair explicitly binds reconciliation to unlabelled graph rows.
// An empty member name passed to NewMemberRepair remains invalid for repair.
// Legacy rows must carry valid UID/version receipts; no provenance is inferred.
func NewStandaloneRepair(client dynamic.Interface, store graph.GraphStore) *MemberRepair {
	r := NewMemberRepair(client, store, "")
	r.standalone = true
	return r
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
	r.RunServedVersions(ctx, gvr, kind, coverage, synced, onDelete, nil)
}

// RunServedVersions also checks retained representations at other explicitly
// served versions of the same resource. Each version supplies its own absence
// proof; this never infers version equivalence from the selected endpoint.
// versions is read once per round and must return an owned slice including the
// selected version. Nil preserves single-endpoint repair. The owner must stop
// this registration when its CRD identity changes.
func (r *MemberRepair) RunServedVersions(ctx context.Context, gvr schema.GroupVersionResource, kind string, coverage *operations.CoverageSession, synced cache.InformerSynced, onDelete func(graph.Resource), versions func() ServedRepairVersions) {
	if r == nil || r.client == nil || (r.cluster == "" && !r.standalone) {
		return
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced) {
		return
	}
	round := func() {
		deadline := time.Now().Add(memberRepairTimeout)
		parent := ctx
		served := []string{gvr.Version}
		var namespaced *bool
		if versions != nil {
			selection := versions()
			served = selection.Versions
			namespaced = selection.Namespaced
			if selection.Context != nil {
				parent = selection.Context
			}
		}
		// Derive directly from the metadata lifetime so its cancellation is
		// synchronous, including when a context-ignoring list returns late.
		bounded, cancel := context.WithDeadline(parent, deadline)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if ctx.Err() != nil {
			cancel()
		}
		r.repairScopedVersionsOnce(bounded, gvr, kind, served, namespaced, coverage, onDelete)
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

// ServedRepairVersions binds an owned served-version snapshot to its metadata
// lifetime. Cancelling Context invalidates an in-flight repair round without
// restarting the selected watch. Nil Context uses the registration lifetime.
type ServedRepairVersions struct {
	Versions   []string
	Context    context.Context
	Namespaced *bool // Optional exact scope; excludes retained rows from old scopes.
}

func (r *MemberRepair) repairOnce(ctx context.Context, gvr schema.GroupVersionResource, kind string, coverage *operations.CoverageSession, onDelete func(graph.Resource)) {
	r.repairVersionsOnce(ctx, gvr, kind, []string{gvr.Version}, coverage, onDelete)
}

func (r *MemberRepair) repairVersionsOnce(ctx context.Context, gvr schema.GroupVersionResource, kind string, versions []string, coverage *operations.CoverageSession, onDelete func(graph.Resource)) {
	r.repairScopedVersionsOnce(ctx, gvr, kind, versions, nil, coverage, onDelete)
}

func (r *MemberRepair) repairScopedVersionsOnce(ctx context.Context, gvr schema.GroupVersionResource, kind string, versions []string, namespaced *bool, coverage *operations.CoverageSession, onDelete func(graph.Resource)) {
	gap := func(reason operations.CoverageReason) {
		// Registration cancellation is ordinary teardown. A round deadline is
		// still a gap: it never authorizes deletion from an incomplete listing.
		if !errors.Is(ctx.Err(), context.Canceled) {
			coverage.RecordGap(gvr, reason, time.Now())
		}
	}
	if (r.cluster == "" && !r.standalone) || isCoreSecretGVR(gvr) || kind == "Secret" || !operations.ValidAPIResourceDescriptor(operations.APIResourceDescriptor{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: kind}) {
		gap(operations.CoverageProcessingFailed)
		return
	}
	if ctx.Err() != nil {
		gap(operations.CoverageProcessingFailed)
		return
	}
	// Metadata authorizes endpoints, not deletion. Reject invalid/ambiguous
	// configuration before reads and keep the original total page budget.
	if len(versions) == 0 || len(versions) > 32 {
		gap(operations.CoverageProcessingFailed)
		return
	}
	endpoints := make(map[string]schema.GroupVersionResource, len(versions))
	for _, version := range versions {
		endpoint := gvr
		endpoint.Version = version
		key := endpoint.GroupVersion().String()
		if _, duplicate := endpoints[key]; duplicate || !operations.ValidAPIResourceDescriptor(operations.APIResourceDescriptor{Group: endpoint.Group, Version: version, Resource: endpoint.Resource, Kind: kind}) {
			gap(operations.CoverageProcessingFailed)
			return
		}
		endpoints[key] = endpoint
	}
	if _, ok := endpoints[gvr.GroupVersion().String()]; !ok {
		gap(operations.CoverageProcessingFailed)
		return
	}
	// A single shared slot bounds extra repair work across all collectors in
	// this scope. Waiting is included in the round deadline.
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
	needed := make(map[string]bool)
	for _, row := range rows {
		if _, allowed := endpoints[row.GroupVersion]; !allowed || row.ClusterID != r.cluster || row.Kind != kind {
			continue
		}
		if namespaced != nil && (row.Namespace != "") != *namespaced {
			gap(operations.CoverageProcessingFailed)
			continue // Retired scope is not absence at the current endpoint.
		}
		if !graph.ValidResourceVersion(row) {
			gap(operations.CoverageProcessingFailed)
			continue
		}
		candidates = append(candidates, row)
		needed[row.GroupVersion] = true
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
	seen := make(map[string]map[string]struct{}, len(needed))
	pages, items := 0, 0
	// Preserve the owner's stable endpoint order. Counts and the context span
	// every endpoint; multiplying versions never multiplies the round budget.
	for _, served := range versions {
		endpoint := gvr
		endpoint.Version = served
		key := endpoint.GroupVersion().String()
		if !needed[key] {
			continue
		}
		var complete bool
		seen[key], complete = r.listRepairEndpoint(ctx, endpoint, kind, namespaced, &pages, &items)
		if !complete {
			gap(operations.CoverageProcessingFailed)
			return // Validate ALL required snapshots before any mutation.
		}
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			gap(operations.CoveragePersistenceFailed)
			return
		}
		if _, present := seen[candidate.GroupVersion][candidate.Namespace+"/"+candidate.Name]; present {
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

// listRepairEndpoint validates one version's complete snapshot. The counters
// are shared across the round; snapshot versions/continue tokens are per API.
func (r *MemberRepair) listRepairEndpoint(ctx context.Context, gvr schema.GroupVersionResource, kind string, namespaced *bool, pages, items *int) (map[string]struct{}, bool) {
	opts := metav1.ListOptions{Limit: memberRepairPageSize}
	seen := make(map[string]struct{})
	tokens := make(map[string]bool)
	version := ""
	for {
		if ctx.Err() != nil || *pages >= 32 {
			return nil, false
		}
		*pages += 1
		list, err := r.client.Resource(gvr).List(ctx, opts)
		if err != nil {
			// Keep repair failures sticky and separate from the informer's
			// transient list result. An informer reconnect cannot clear this.
			return nil, false
		}
		if list == nil || list.GetResourceVersion() == "" || (version != "" && version != list.GetResourceVersion()) {
			return nil, false
		}
		version = list.GetResourceVersion()
		for _, item := range list.Items {
			if item.GetName() == "" || item.GetKind() != kind || item.GetAPIVersion() != gvr.GroupVersion().String() || item.GetUID() == "" || item.GetResourceVersion() == "" {
				return nil, false
			}
			if namespaced != nil && (item.GetNamespace() != "") != *namespaced {
				return nil, false
			}
			key := item.GetNamespace() + "/" + item.GetName()
			if _, duplicate := seen[key]; duplicate {
				return nil, false
			}
			seen[key] = struct{}{}
			*items += 1
			if *items > memberRepairLimit {
				return nil, false
			}
		}
		opts.Continue = list.GetContinue()
		if opts.Continue == "" {
			if remaining := list.GetRemainingItemCount(); remaining != nil && *remaining > 0 {
				return nil, false
			}
			return seen, true
		}
		if tokens[opts.Continue] {
			return nil, false
		}
		tokens[opts.Continue] = true
	}
}
