// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

const maxMemberRecordingScopes = 128

var ErrMemberRecordingStarted = errors.New("snapshot: member recording already started")
var ErrMemberRecordingWriter = errors.New("snapshot: member recording requires a running writer")

type memberRecordingIndex map[string]*MemberRecordingSession

// MemberRecordingSession binds queued events to an exact member lifetime.
// Scalars describe only this token's delivery, never another member's/global
// counters. No resource identities are retained outside the existing queue.
type MemberRecordingSession struct {
	writer      *Writer
	cluster     string
	used        atomic.Bool
	closed      atomic.Bool
	observation atomic.Pointer[writerObservation]
	pending     atomic.Int64
	queueGap    atomic.Bool
	writeGap    atomic.Bool
	drained     chan struct{}
	drainOnce   sync.Once
	active      bool // guarded by Writer.membersMu
}

// ForMember does not register the scope. Begin must precede collector startup.
func (w *Writer) ForMember(cluster string) *MemberRecordingSession {
	return &MemberRecordingSession{writer: w, cluster: cluster, drained: make(chan struct{})}
}

func (s *MemberRecordingSession) Begin(ctx context.Context) error {
	if !validMemberRetentionScope(s.cluster) {
		return ErrAnalysisAvailabilityScope
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w := s.writer
	life := w.observation.Load()
	if life == nil || w.closed.Load() || s.closed.Load() {
		return ErrMemberRecordingWriter
	}
	select {
	case <-life.done:
		return ErrMemberRecordingWriter
	default:
	}
	w.membersMu.Lock()
	defer w.membersMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	index := *w.members.Load()
	if previous := index[s.cluster]; previous != nil && previous.active {
		return ErrMemberRecordingStarted
	}
	if !s.used.CompareAndSwap(false, true) {
		return ErrMemberRecordingStarted
	}
	s.observation.Store(&writerObservation{startedAt: time.Now(), done: ctx.Done()})
	if previous := index[s.cluster]; previous != nil {
		// Past loss is sticky within the process, but stale tokens cannot alter
		// the successor's lifetime, pending count or new loss flags.
		if previous.queueGap.Load() {
			s.queueGap.Store(true)
		}
		if previous.writeGap.Load() {
			s.writeGap.Store(true)
		}
	}
	s.active = true
	if index[s.cluster] == nil && len(index) >= maxMemberRecordingScopes {
		// Keep graph collection and the existing writer budget operational.
		// This lifetime may enqueue, but has no qualified member cache entry.
		return nil
	}
	fresh := make(memberRecordingIndex, len(index)+1)
	for cluster, old := range index {
		fresh[cluster] = old
	}
	fresh[s.cluster] = s
	w.members.Store(&fresh)
	return nil
}

// Enqueue is a non-blocking member-bound metadata sink. A foreign cluster is
// rejected rather than silently retagged. Old closed tokens cannot touch a new
// lifetime's evidence, even when a caller retains an obsolete sink reference.
func (s *MemberRecordingSession) Enqueue(e graph.ResourceEvent) {
	if s.closed.Load() {
		s.writer.metrics.incQueueDropped()
		return
	}
	if s.observation.Load() == nil || e.ClusterID != s.cluster {
		s.queueGap.Store(true)
		s.writer.metrics.incQueueDropped()
		return
	}
	s.pending.Add(1)
	if s.closed.Load() {
		s.writer.metrics.incQueueDropped()
		s.finish()
		return
	}
	s.writer.enqueue(queuedEvent{event: graph.MetadataOnlyEvent(e), member: s})
}

func (s *MemberRecordingSession) finish() {
	if s.pending.Add(-1) == 0 && s.closed.Load() {
		s.drainOnce.Do(func() { close(s.drained) })
	}
}

// Close runs after collectors have joined. It stops accepting delivery and
// waits for this token's queued/in-flight writes, without stopping other
// members or closing the shared writer. Same-name replacement stays excluded.
func (s *MemberRecordingSession) Close() {
	s.closed.Store(true)
	if s.pending.Load() == 0 {
		s.drainOnce.Do(func() { close(s.drained) })
	}
	<-s.drained
	s.writer.membersMu.Lock()
	s.active = false
	s.writer.membersMu.Unlock()
}

func unsupportedMemberHistory() operations.HistoryAvailability {
	return operations.HistoryAvailability{State: operations.HistoryUnsupported, Reasons: []operations.HistoryAvailabilityReason{operations.HistoryScopeUnknown}, Coverage: operations.CoverageUnknown, Content: "metadata_only", RetainedData: "not_queried"}
}

func (w *Writer) memberHistoryAvailability(cluster string, retention time.Duration) operations.HistoryAvailability {
	index := w.members.Load()
	if index == nil {
		return unsupportedMemberHistory()
	}
	s := (*index)[cluster]
	if s == nil {
		return unsupportedMemberHistory()
	}
	out := operations.HistoryAvailability{State: operations.HistoryRecording, Reasons: []operations.HistoryAvailabilityReason{}, Retention: retention, Coverage: operations.CoverageUnknown, Content: "metadata_only", RetainedData: "not_queried"}
	if life := s.observation.Load(); life != nil {
		out.ObservationStartedAt = life.startedAt
	}
	queueGap, writeGap := s.queueGap.Load(), s.writeGap.Load()
	if queueGap {
		out.Reasons = append(out.Reasons, operations.HistoryQueueGap)
	}
	if writeGap {
		out.Reasons = append(out.Reasons, operations.HistoryWriteGap)
	}
	if queueGap || writeGap {
		out.State, out.Coverage = operations.HistoryDegraded, operations.CoveragePartial
	}
	if s.pending.Load() > 0 {
		out.Reasons = append(out.Reasons, operations.HistoryDeliveryPending)
	}
	stopped := s.closed.Load() || w.closed.Load()
	for _, life := range []*writerObservation{s.observation.Load(), w.observation.Load()} {
		if life != nil {
			select {
			case <-life.done:
				stopped = true
			default:
			}
		}
	}
	if stopped {
		out.State = operations.HistoryStopped
		out.Reasons = append(out.Reasons, operations.HistoryWriterStopped)
	}
	return out
}
