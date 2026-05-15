// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// fakeSink is an in-memory EventSink. failUntil makes the first N
// AppendEvent calls return an error (simulating a store outage);
// delay slows every call (simulating a slow store so the queue can
// fill). Both are safe for concurrent worker access.
type fakeSink struct {
	mu        sync.Mutex
	events    []graph.ResourceEvent
	calls     int
	failUntil int
	failAll   bool
	delay     time.Duration
}

func (f *fakeSink) AppendEvent(_ context.Context, e graph.ResourceEvent) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failAll || f.calls <= f.failUntil {
		return errors.New("simulated store outage")
	}
	f.events = append(f.events, e)
	return nil
}

func (f *fakeSink) stored() []graph.ResourceEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]graph.ResourceEvent, len(f.events))
	copy(out, f.events)
	return out
}

func ev(name string) graph.ResourceEvent {
	return graph.ResourceEvent{
		Namespace: "demo", Kind: "Pod", Name: name, EventType: graph.EventTypeAdd,
	}
}

// TestWriter_NoLoss is the P3-T2/T3 "all events written" acceptance,
// scaled down from the doc's 1K-events/s-for-30s scenario to a
// deterministic burst: enqueue N events well under the queue cap,
// Stop (which drains), and confirm every one reached the sink.
func TestWriter_NoLoss(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, Config{QueueSize: 10000, Workers: 4}, NewMetrics())
	w.Start(context.Background())

	const n = 2000
	for i := 0; i < n; i++ {
		w.Enqueue(ev(fmt.Sprintf("pod-%04d", i)))
	}
	w.Stop()

	if got := len(sink.stored()); got != n {
		t.Fatalf("stored %d events, want %d (no loss expected under queue cap)", got, n)
	}
	m := w.Metrics().Snapshot()
	if m.EventsProcessed != n {
		t.Errorf("EventsProcessed = %d, want %d", m.EventsProcessed, n)
	}
	if m.QueueDropped != 0 || m.WriteFailed != 0 {
		t.Errorf("expected zero drops/failures, got dropped=%d failed=%d", m.QueueDropped, m.WriteFailed)
	}
}

// TestWriter_RetriesThroughOutage simulates a brief store outage:
// the first few AppendEvent calls fail, then recover. The per-event
// retry budget must carry the events through — nothing lost.
func TestWriter_RetriesThroughOutage(t *testing.T) {
	// One worker keeps call ordering deterministic. failUntil=2 →
	// the first event's attempts 1+2 fail, attempt 3 succeeds; the
	// rest succeed first try.
	sink := &fakeSink{failUntil: 2}
	w := New(sink, Config{QueueSize: 100, Workers: 1}, NewMetrics())
	w.Start(context.Background())

	for i := 0; i < 3; i++ {
		w.Enqueue(ev(fmt.Sprintf("e-%d", i)))
	}
	w.Stop()

	m := w.Metrics().Snapshot()
	if m.EventsProcessed != 3 {
		t.Errorf("EventsProcessed = %d, want 3 (retry must recover all)", m.EventsProcessed)
	}
	if m.WriteFailed != 0 {
		t.Errorf("WriteFailed = %d, want 0 — the outage was within the retry budget", m.WriteFailed)
	}
	if len(sink.stored()) != 3 {
		t.Errorf("sink stored %d, want 3", len(sink.stored()))
	}
}

// TestWriter_DropsAfterRetryBudget confirms a permanently-failing
// store does not pin a worker forever: after maxRetries the event
// is dropped and WriteFailed is incremented.
func TestWriter_DropsAfterRetryBudget(t *testing.T) {
	sink := &fakeSink{failAll: true}
	w := New(sink, Config{QueueSize: 10, Workers: 1}, NewMetrics())
	w.Start(context.Background())

	w.Enqueue(ev("doomed"))
	w.Stop()

	m := w.Metrics().Snapshot()
	if m.WriteFailed != 1 {
		t.Errorf("WriteFailed = %d, want 1", m.WriteFailed)
	}
	if m.EventsProcessed != 0 {
		t.Errorf("EventsProcessed = %d, want 0", m.EventsProcessed)
	}
	// maxRetries attempts were made.
	sink.mu.Lock()
	calls := sink.calls
	sink.mu.Unlock()
	if calls != maxRetries {
		t.Errorf("AppendEvent called %d times, want maxRetries=%d", calls, maxRetries)
	}
}

// TestWriter_QueueFullDropsOldest floods a tiny queue behind a slow
// sink. Enqueue must never block; the overflow is shed via
// QueueDropped; and every one of the 100 events ends up either
// processed or dropped (conservation — drop-oldest neither
// duplicates nor loses-silently beyond the counter).
func TestWriter_QueueFullDropsOldest(t *testing.T) {
	sink := &fakeSink{delay: 2 * time.Millisecond}
	w := New(sink, Config{QueueSize: 5, Workers: 1}, NewMetrics())
	w.Start(context.Background())

	const n = 100
	for i := 0; i < n; i++ {
		w.Enqueue(ev(fmt.Sprintf("flood-%03d", i)))
	}
	w.Stop()

	m := w.Metrics().Snapshot()
	if m.QueueDropped == 0 {
		t.Error("expected QueueDropped > 0 under a tiny queue + slow sink")
	}
	if m.EventsProcessed == 0 {
		t.Error("expected some events to still get through")
	}
	if total := m.EventsProcessed + m.QueueDropped; total != n {
		t.Errorf("processed(%d) + dropped(%d) = %d, want %d (conservation)",
			m.EventsProcessed, m.QueueDropped, total, n)
	}
}

// TestWriter_EnqueueAfterStopIsNoop verifies a late Enqueue (informer
// outliving the writer during shutdown) is a counted drop, not a
// panic on a closed channel.
func TestWriter_EnqueueAfterStopIsNoop(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, Config{}, NewMetrics())
	w.Start(context.Background())
	w.Stop()

	w.Enqueue(ev("late")) // must not panic

	if m := w.Metrics().Snapshot(); m.QueueDropped != 1 {
		t.Errorf("QueueDropped = %d, want 1 (post-Stop Enqueue is a drop)", m.QueueDropped)
	}
}

// TestWriter_StopIsIdempotent confirms a second Stop is a safe no-op.
func TestWriter_StopIsIdempotent(t *testing.T) {
	w := New(&fakeSink{}, Config{}, NewMetrics())
	w.Start(context.Background())
	w.Stop()
	w.Stop() // must not panic / double-close
}

// TestWriter_CapsOversizedData confirms an event whose Data exceeds
// rawDataMaxBytes is stored with the truncation marker instead — the
// resource_events table must not balloon on large Secrets/ConfigMaps.
func TestWriter_CapsOversizedData(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, Config{Workers: 1}, NewMetrics())
	w.Start(context.Background())

	big := graph.ResourceEvent{
		Namespace: "demo", Kind: "Secret", Name: "huge", EventType: graph.EventTypeUpdate,
		Data: map[string]any{"blob": strings.Repeat("x", rawDataMaxBytes+1)},
	}
	small := graph.ResourceEvent{
		Namespace: "demo", Kind: "ConfigMap", Name: "tiny", EventType: graph.EventTypeAdd,
		Data: map[string]any{"k": "v"},
	}
	w.Enqueue(big)
	w.Enqueue(small)
	w.Stop()

	stored := sink.stored()
	if len(stored) != 2 {
		t.Fatalf("stored %d events, want 2", len(stored))
	}
	byName := map[string]graph.ResourceEvent{}
	for _, e := range stored {
		byName[e.Name] = e
	}
	if v, ok := byName["huge"].Data["kubeatlas.io/truncated"]; !ok || v != true {
		t.Errorf("oversized event Data = %v, want truncation marker", byName["huge"].Data)
	}
	if byName["tiny"].Data["k"] != "v" {
		t.Errorf("small event Data was altered: %v", byName["tiny"].Data)
	}
}

// TestWriter_QueueDepth sanity-checks the gauge the /metrics handler
// reads.
func TestWriter_QueueDepth(t *testing.T) {
	// A blocked sink (long delay) + single worker so the queue
	// retains depth long enough to observe.
	sink := &fakeSink{delay: 200 * time.Millisecond}
	w := New(sink, Config{QueueSize: 100, Workers: 1}, NewMetrics())
	w.Start(context.Background())
	for i := 0; i < 10; i++ {
		w.Enqueue(ev(fmt.Sprintf("d-%d", i)))
	}
	if d := w.QueueDepth(); d < 1 {
		t.Errorf("QueueDepth = %d, want >= 1 while the sink is blocked", d)
	}
	w.Stop()
	if d := w.QueueDepth(); d != 0 {
		t.Errorf("QueueDepth after Stop = %d, want 0 (queue fully drained)", d)
	}
}

// TestConfig_Defaults verifies zero-valued Config fields fall back to
// the Default* constants.
func TestConfig_Defaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.QueueSize != DefaultQueueSize || c.Workers != DefaultWorkers {
		t.Errorf("withDefaults() = %+v, want QueueSize=%d Workers=%d",
			c, DefaultQueueSize, DefaultWorkers)
	}
	c = Config{QueueSize: 50, Workers: 2}.withDefaults()
	if c.QueueSize != 50 || c.Workers != 2 {
		t.Errorf("withDefaults() overrode explicit values: %+v", c)
	}
}

// BenchmarkEnqueue measures the informer-hot-path cost of Enqueue.
// It must stay allocation-light and non-blocking — the informer
// callback runs on the shared informer goroutine.
func BenchmarkEnqueue(b *testing.B) {
	sink := &fakeSink{}
	w := New(sink, Config{QueueSize: b.N + 16, Workers: 4}, NewMetrics())
	w.Start(context.Background())
	defer w.Stop()
	e := ev("bench")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Enqueue(e)
	}
}
