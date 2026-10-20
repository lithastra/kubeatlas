// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

type memberRecordingSink struct {
	fakeSink
	entered, release chan struct{}
	once             sync.Once
	failCluster      string
}

func (s *memberRecordingSink) AppendEvent(ctx context.Context, e graph.ResourceEvent) error {
	if e.Name == "hold" {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	if e.ClusterID == s.failCluster && s.failCluster != "" {
		return errors.New("private-member-write-error")
	}
	return s.fakeSink.AppendEvent(ctx, e)
}

func runningMemberWriter(t *testing.T, sink EventSink, cfg Config) *Writer {
	t.Helper()
	w := New(sink, cfg, nil)
	w.Start(context.Background())
	t.Cleanup(w.Stop)
	return w
}

func beginMemberRecording(t *testing.T, w *Writer, cluster string) *MemberRecordingSession {
	t.Helper()
	s := w.ForMember(cluster)
	if err := s.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func memberEvent(cluster, name string) graph.ResourceEvent {
	e := ev(name)
	e.ClusterID = cluster
	e.Data = map[string]any{"private": "payload-sentinel"}
	return e
}

func memberHistory(t *testing.T, w *Writer, cluster string) operations.HistoryAvailability {
	t.Helper()
	r := NewAnalysisAvailabilityReader(availabilityStorageFunc(availableStorage), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true, Retention: time.Hour})
	e, err := r.SnapshotAvailability(context.Background(), cluster)
	if err != nil || e.History.Coverage == operations.CoverageObserved {
		t.Fatalf("member availability overstated or failed: %+v %v", e, err)
	}
	return e.History
}

func TestMemberRecordingOverflowIsAttributedToDiscardedMember(t *testing.T) {
	sink := &memberRecordingSink{entered: make(chan struct{}), release: make(chan struct{})}
	w := runningMemberWriter(t, sink, Config{Workers: 1, QueueSize: 1})
	east, west, blocker := beginMemberRecording(t, w, "east"), beginMemberRecording(t, w, "west"), beginMemberRecording(t, w, "blocker")
	var once sync.Once
	release := func() { once.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	blocker.Enqueue(memberEvent("blocker", "hold"))
	<-sink.entered
	west.Enqueue(memberEvent("west", "oldest"))
	east.Enqueue(memberEvent("east", "newest"))
	wc, ec := memberHistory(t, w, "west"), memberHistory(t, w, "east")
	if wc.State != operations.HistoryDegraded || wc.Coverage != operations.CoveragePartial || !slices.Contains(wc.Reasons, operations.HistoryQueueGap) || slices.Contains(wc.Reasons, operations.HistoryDeliveryPending) {
		t.Fatalf("discarded member lost its loss attribution: %+v", wc)
	}
	if ec.State != operations.HistoryRecording || ec.Coverage != operations.CoverageUnknown || slices.Contains(ec.Reasons, operations.HistoryQueueGap) || !slices.Contains(ec.Reasons, operations.HistoryDeliveryPending) {
		t.Fatalf("incoming member inherited another queue loss: %+v", ec)
	}
	if !slices.Contains(memberHistory(t, w, "blocker").Reasons, operations.HistoryDeliveryPending) {
		t.Fatal("in-flight member write hidden behind queue depth")
	}
	release()
	east.Close()
	west.Close()
	blocker.Close()
	if w.Metrics().Snapshot().QueueDropped != 1 || len(sink.stored()) != 2 {
		t.Fatal("shared queue budget or global loss accounting changed")
	}
	for _, e := range sink.stored() {
		if len(e.Data) != 0 || e.ClusterID == "west" {
			t.Fatal("discarded/payload event reached history storage")
		}
	}
	fresh := beginMemberRecording(t, w, "west")
	if h := memberHistory(t, w, "west"); h.State != operations.HistoryDegraded || !slices.Contains(h.Reasons, operations.HistoryQueueGap) || h.ObservationStartedAt.IsZero() {
		t.Fatal("reattachment erased past loss or borrowed global lifecycle")
	}
	fresh.Close()
}

func TestMemberRecordingCloseJoinsWritesAndStaleTokensCannotAlterReplacement(t *testing.T) {
	sink := &memberRecordingSink{entered: make(chan struct{}), release: make(chan struct{})}
	w := runningMemberWriter(t, sink, Config{Workers: 1, QueueSize: 2})
	s := beginMemberRecording(t, w, "east")
	var once sync.Once
	release := func() { once.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	s.Enqueue(memberEvent("east", "hold"))
	<-sink.entered
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	awaitMemberRetention(t, func() bool { return s.closed.Load() })
	if err := w.ForMember("east").Begin(context.Background()); !errors.Is(err, ErrMemberRecordingStarted) {
		t.Fatal("same-name session overlapped an in-flight write")
	}
	if h := memberHistory(t, w, "east"); h.State != operations.HistoryStopped || !slices.Contains(h.Reasons, operations.HistoryDeliveryPending) {
		t.Fatal("draining member was reported as an empty running writer")
	}
	select {
	case <-done:
		t.Fatal("member close returned before its write")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	<-done
	fresh := beginMemberRecording(t, w, "east")
	s.Enqueue(memberEvent("east", "obsolete-token"))
	if h := memberHistory(t, w, "east"); h.State != operations.HistoryRecording || h.Coverage != operations.CoverageUnknown || len(h.Reasons) != 0 || fresh.pending.Load() != 0 {
		t.Fatalf("old token changed replacement evidence: %+v", h)
	}
	fresh.Enqueue(memberEvent("east", "fresh"))
	fresh.Close()
	if len(sink.stored()) != 2 {
		t.Fatal("stale token wrote into the new member lifetime")
	}
}

func TestMemberRecordingWriteFailureStaysScopedAndSticky(t *testing.T) {
	sink := &memberRecordingSink{failCluster: "east"}
	w := runningMemberWriter(t, sink, Config{Workers: 2, QueueSize: 4})
	east, west := beginMemberRecording(t, w, "east"), beginMemberRecording(t, w, "west")
	east.Enqueue(memberEvent("east", "failed"))
	west.Enqueue(memberEvent("west", "successful"))
	awaitMemberRetention(t, func() bool { return east.pending.Load() == 0 && west.pending.Load() == 0 })
	if h := memberHistory(t, w, "east"); h.State != operations.HistoryDegraded || h.Coverage != operations.CoveragePartial || !slices.Contains(h.Reasons, operations.HistoryWriteGap) {
		t.Fatalf("failed member lost write gap: %+v", h)
	}
	if h := memberHistory(t, w, "west"); h.State != operations.HistoryRecording || h.Coverage != operations.CoverageUnknown || len(h.Reasons) != 0 {
		t.Fatal("healthy member inherited global write failure")
	}
	east.Close()
	beginMemberRecording(t, w, "east")
	if !slices.Contains(memberHistory(t, w, "east").Reasons, operations.HistoryWriteGap) {
		t.Fatal("member reattachment reset a known history loss")
	}
	if w.Metrics().Snapshot().WriteFailed != 1 {
		t.Fatal("global write-failure accounting changed")
	}
}

func TestMemberRecordingForeignEventsAndGlobalCountersCannotQualifyOtherMembers(t *testing.T) {
	sink := &fakeSink{}
	w := runningMemberWriter(t, sink, Config{Workers: 1, QueueSize: 4})
	east, west := beginMemberRecording(t, w, "east"), beginMemberRecording(t, w, "west")
	east.Enqueue(memberEvent("west", "foreign-event"))
	w.metrics.incWriteFailed()
	w.metrics.incQueueDropped() // unscoped legacy facts
	if h := memberHistory(t, w, "west"); len(h.Reasons) != 0 || h.Coverage != operations.CoverageUnknown {
		t.Fatal("global counters were attributed to a member")
	}
	if h := memberHistory(t, w, "east"); !slices.Contains(h.Reasons, operations.HistoryQueueGap) || slices.Contains(h.Reasons, operations.HistoryWriteGap) {
		t.Fatal("foreign-event rejection lost exact attribution")
	}
	if h := memberHistory(t, w, "absent"); h.State != operations.HistoryUnsupported || !h.ObservationStartedAt.IsZero() {
		t.Fatal("unregistered member borrowed a writer lifetime")
	}
	east.Close()
	west.Close()
	if len(sink.stored()) != 0 {
		t.Fatal("foreign event was silently retagged")
	}
	body, _ := json.Marshal(memberHistory(t, w, "east"))
	for _, private := range []string{"foreign-event", "payload-sentinel", "queueDropped", "writeFailed", "eventsProcessed"} {
		if strings.Contains(string(body), private) {
			t.Fatal("member evidence disclosed identities, payload or counts")
		}
	}
}

func TestMemberRecordingCapacityScopeAndWriterLifecycle(t *testing.T) {
	w := New(&fakeSink{}, Config{Workers: 1}, nil)
	if err := w.ForMember("east").Begin(context.Background()); !errors.Is(err, ErrMemberRecordingWriter) {
		t.Fatal("unstarted writer qualified a member")
	}
	w.Start(context.Background())
	t.Cleanup(w.Stop)
	for _, name := range []string{"", "bad/name", "bad:name", strings.Repeat("a", 254)} {
		if err := w.ForMember(name).Begin(context.Background()); !errors.Is(err, ErrAnalysisAvailabilityScope) {
			t.Fatal("invalid member recording scope")
		}
	}
	for i := 0; i < maxMemberRecordingScopes; i++ {
		beginMemberRecording(t, w, fmt.Sprintf("member-%d", i))
	}
	over := beginMemberRecording(t, w, "over-capacity")
	if len(*w.members.Load()) != maxMemberRecordingScopes || memberHistory(t, w, "over-capacity").State != operations.HistoryUnsupported {
		t.Fatal("member recorder capacity was unbounded or falsely qualified")
	}
	over.Enqueue(memberEvent("over-capacity", "still-recorded"))
	over.Close()
	if len((*w.members.Load())) != maxMemberRecordingScopes {
		t.Fatal("overflow allocated a historical scope")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.ForMember("cancelled").Begin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled admission registered member recording")
	}
	current := (*w.members.Load())["member-0"]
	current.Close()
	beginMemberRecording(t, w, "member-0")
	if memberHistory(t, w, "member-0").State != operations.HistoryRecording {
		t.Fatal("known member could not reattach at capacity")
	}
}

func TestMemberRecordingConcurrentStopAndEnqueueDoesNotPanicOrStrandPending(t *testing.T) {
	w := runningMemberWriter(t, &fakeSink{}, Config{Workers: 2, QueueSize: 16})
	s := beginMemberRecording(t, w, "east")
	var producers sync.WaitGroup
	for range 8 {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for range 200 {
				s.Enqueue(memberEvent("east", "concurrent"))
			}
		}()
	}
	w.Stop()
	producers.Wait()
	s.Close()
	if s.pending.Load() != 0 || memberHistory(t, w, "east").State != operations.HistoryStopped {
		t.Fatal("concurrent shutdown stranded member delivery")
	}
}

func TestMemberRecordingSharedWriterCancellationAndStorageWarnings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := New(&fakeSink{}, Config{Workers: 1}, nil)
	w.Start(ctx)
	t.Cleanup(w.Stop)
	defer cancel()
	s := beginMemberRecording(t, w, "east")
	for _, state := range []operations.StorageAvailabilityState{operations.StorageUnknown, operations.StorageStale, operations.StorageUnavailable} {
		r := NewAnalysisAvailabilityReader(availabilityStorageFunc(func(context.Context) (operations.StorageAvailability, error) {
			return operations.StorageAvailability{State: state, DurabilityKnown: true, Durable: true}, nil
		}), w, AnalysisAvailabilityConfig{Durable: true, HistoryEnabled: true, Federated: true})
		e, err := r.SnapshotAvailability(context.Background(), "east")
		if err != nil || e.History.Coverage != operations.CoverageUnknown || slices.Contains(e.History.Reasons, operations.HistoryWriteGap) {
			t.Fatal("storage probe manufactured member write loss")
		}
		if state == operations.StorageUnavailable && e.History.State != operations.HistoryDegraded {
			t.Fatal("member recording ignored unavailable storage")
		}
	}
	cancel()
	if h := memberHistory(t, w, "east"); h.State != operations.HistoryStopped || !slices.Contains(h.Reasons, operations.HistoryWriterStopped) {
		t.Fatal("cancelled shared writer qualified member recording")
	}
	if err := w.ForMember("west").Begin(context.Background()); !errors.Is(err, ErrMemberRecordingWriter) {
		t.Fatal("cancelled writer admitted a new member")
	}
	s.Close()
}

func TestMemberRecordingDuplicateWriterStartKeepsWorkerBudget(t *testing.T) {
	sink := &availabilityBlockingSink{entered: make(chan struct{}, 2), release: make(chan struct{})}
	w := runningMemberWriter(t, sink, Config{Workers: 1, QueueSize: 2})
	w.Start(context.Background())
	var once sync.Once
	release := func() { once.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	w.Enqueue(ev("one"))
	w.Enqueue(ev("two"))
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	select {
	case <-sink.entered:
		t.Fatal("duplicate Start multiplied shared worker budget")
	case <-time.After(20 * time.Millisecond):
	}
	release()
}
