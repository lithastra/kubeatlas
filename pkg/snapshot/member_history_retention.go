// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

const maxMemberRetentionScopes = 128

type memberRetentionEntry struct {
	observer *HistoryRetentionObserver
	active   bool // guarded by MemberHistoryRetention.mu; never read by requests
}

// Published indexes are immutable. Request reads do not wait for registration
// locks, running queries, member removal, or another member's storage failure.
type memberRetentionIndex map[string]*memberRetentionEntry

// MemberHistoryRetention owns bounded, independent retained-metadata caches.
// It never uses the global writer's counters or the unlabelled standalone scope
// as member evidence. Stopped caches are retained until same-name replacement.
type MemberHistoryRetention struct {
	reader    graph.HistoryBoundsReader
	retention time.Duration
	mu        sync.Mutex
	index     atomic.Pointer[memberRetentionIndex]
}

func NewMemberHistoryRetention(reader graph.HistoryBoundsReader, retention time.Duration) *MemberHistoryRetention {
	if retention == 0 {
		retention = DefaultRetention
	}
	r := &MemberHistoryRetention{retention: retention}
	if reader != nil {
		r.reader = &memberBoundsReader{reader: reader, slots: make(chan struct{}, 2)}
	}
	index := make(memberRetentionIndex)
	r.index.Store(&index)
	return r
}

// MemberHistoryRetentionStarter reserves a cache only when Start is invoked,
// not while a multicluster factory is being constructed or losing an add race.
type MemberHistoryRetentionStarter struct {
	registry *MemberHistoryRetention
	cluster  string
}

func (r *MemberHistoryRetention) ForMember(cluster string) *MemberHistoryRetentionStarter {
	return &MemberHistoryRetentionStarter{registry: r, cluster: cluster}
}

func (s *MemberHistoryRetentionStarter) Start(ctx context.Context) error {
	r := s.registry
	if r.retention <= 0 {
		return ErrAnalysisAvailabilityConfig
	}
	if !validMemberRetentionScope(s.cluster) {
		return ErrAnalysisAvailabilityScope
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	index := *r.index.Load()
	if previous := index[s.cluster]; previous != nil && previous.active {
		r.mu.Unlock()
		return ErrHistoryRetentionStarted
	}
	if index[s.cluster] == nil && len(index) >= maxMemberRetentionScopes {
		r.mu.Unlock()
		// Exhausting metadata capacity must not tear down this member's graph
		// collectors. An uncached scope reports unsupported without querying.
		<-ctx.Done()
		return nil
	}
	entry := &memberRetentionEntry{observer: NewHistoryRetentionObserver(r.reader, s.cluster, r.retention), active: true}
	fresh := make(memberRetentionIndex, len(index)+1)
	for cluster, old := range index {
		fresh[cluster] = old
	}
	fresh[s.cluster] = entry
	r.index.Store(&fresh)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); entry.active = false; r.mu.Unlock() }()
	// Start includes the in-flight query. Do not release the name while a
	// context-ignoring backend still owns this observer's previous lifetime.
	return entry.observer.Start(ctx)
}

func validMemberRetentionScope(cluster string) bool {
	now := time.Now()
	return cluster != "" && (graph.HistoryBoundsQuery{ClusterID: cluster, From: now.Add(-time.Hour), To: now}).Validate() == nil
}

// SnapshotRetention loads one fixed-size owned cache record. It does not
// register scopes, query events/markers, refresh stale data or read writer state.
// Caller authorization must precede this read.
func (r *MemberHistoryRetention) SnapshotRetention(ctx context.Context, cluster string) (operations.HistoryRetentionEvidence, error) {
	if err := ctx.Err(); err != nil {
		return operations.HistoryRetentionEvidence{}, err
	}
	if !validMemberRetentionScope(cluster) {
		return operations.HistoryRetentionEvidence{}, ErrAnalysisAvailabilityScope
	}
	index := *r.index.Load()
	if entry := index[cluster]; entry != nil {
		return entry.observer.SnapshotRetention(ctx, cluster)
	}
	e := operations.HistoryRetentionEvidence{State: operations.RetentionNotQueried, Bounds: graph.HistoryBounds{ClusterID: cluster}}
	if len(index) >= maxMemberRetentionScopes {
		e.State = operations.RetentionUnsupported
	}
	return e, nil
}

type memberBoundsReader struct {
	reader graph.HistoryBoundsReader
	slots  chan struct{}
}

func (r *memberBoundsReader) ReadHistoryBounds(ctx context.Context, q graph.HistoryBoundsQuery) (graph.HistoryBounds, error) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return graph.HistoryBounds{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return graph.HistoryBounds{}, err
	}
	return r.reader.ReadHistoryBounds(ctx, q)
}
