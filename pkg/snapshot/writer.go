// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Default queue + worker sizing. The Helm chart overrides these via
// snapshots.queueSize / snapshots.workers; the constants are the
// fallbacks main.go applies when a value is zero.
const (
	DefaultQueueSize = 10000
	DefaultWorkers   = 4

	// maxRetries bounds the per-event retry budget. After this many
	// failed AppendEvent attempts the event is dropped and
	// WriteFailed is incremented — the writer never retries forever
	// (a permanently-down store must not pin a worker indefinitely).
	maxRetries = 5
	// retryBaseDelay is the first backoff sleep; each subsequent
	// attempt doubles it (50ms, 100ms, 200ms, 400ms, 800ms).
	retryBaseDelay = 50 * time.Millisecond
)

// EventSink is the store-side seam the Writer needs. graph.GraphStore
// satisfies it; depending on the one method keeps the snapshot
// package decoupled from the full store interface (and trivially
// fakeable in tests).
type EventSink interface {
	AppendEvent(ctx context.Context, e graph.ResourceEvent) error
}

// Config tunes the Writer. A zero field falls back to the Default*
// constant.
type Config struct {
	QueueSize int
	Workers   int
}

func (c Config) withDefaults() Config {
	if c.QueueSize <= 0 {
		c.QueueSize = DefaultQueueSize
	}
	if c.Workers <= 0 {
		c.Workers = DefaultWorkers
	}
	return c
}

// Writer drains informer-observed resource events into an EventSink
// (the Tier 2 store) asynchronously. Enqueue is the informer-facing
// entry point — it never blocks and never panics, so the informer's
// hot path is fully insulated from store latency or outages.
//
// Lifecycle: New -> Start(ctx) -> Enqueue(...)* -> Stop(). Start
// spawns the worker pool; Stop closes the queue, lets workers drain
// every buffered event, and waits for them to exit.
type Writer struct {
	sink    EventSink
	queue   chan queuedEvent
	workers int
	metrics *Metrics

	wg sync.WaitGroup
	// closed guards against a send on the closed queue: once Stop has
	// run, a late Enqueue is counted as a drop instead of panicking.
	closed atomic.Bool
	// Analysis reads lifecycle/delivery status without inspecting queued events.
	observation atomic.Pointer[writerObservation]
	inFlight    atomic.Int64
	enqueueMu   sync.RWMutex
	membersMu   sync.Mutex
	members     atomic.Pointer[memberRecordingIndex]
}

type queuedEvent struct {
	event  graph.ResourceEvent
	member *MemberRecordingSession
}

// New builds a Writer. The Metrics pointer is shared with the
// api package so /metrics can surface the counters; pass
// NewMetrics() if no sharing is needed.
func New(sink EventSink, cfg Config, m *Metrics) *Writer {
	cfg = cfg.withDefaults()
	if m == nil {
		m = NewMetrics()
	}
	w := &Writer{
		sink:    sink,
		queue:   make(chan queuedEvent, cfg.QueueSize),
		workers: cfg.Workers,
		metrics: m,
	}
	index := make(memberRecordingIndex)
	w.members.Store(&index)
	return w
}

// Start spawns the worker pool once. ctx cancellation makes in-flight
// retry backoff sleeps return early; the clean shutdown path is
// Stop, which drains the queue first.
func (w *Writer) Start(ctx context.Context) {
	if w.closed.Load() || !w.observation.CompareAndSwap(nil, &writerObservation{startedAt: time.Now(), done: ctx.Done()}) {
		return
	}
	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go w.worker(ctx)
	}
}

// Enqueue hands one event to the queue. It is the only method the
// informer calls and it MUST NOT block — the informer's
// ResourceEventHandler runs on the shared informer goroutine.
//
// When the queue is full the oldest queued event is dropped to make
// room for the new one (recent state matters more than stale state
// for a diff feature). If even that fails — another producer refills
// the slot in the race window — the new event is dropped. Either
// way QueueDropped is incremented and Enqueue returns immediately.
func (w *Writer) Enqueue(e graph.ResourceEvent) {
	w.enqueue(queuedEvent{event: graph.MetadataOnlyEvent(e)})
}

func (w *Writer) enqueue(item queuedEvent) {
	// Stop holds this gate only while closing the channel. TryRLock keeps
	// informer delivery non-blocking and makes concurrent Stop/send safe.
	if !w.enqueueMu.TryRLock() {
		w.dropQueued(item)
		return
	}
	defer w.enqueueMu.RUnlock()
	if w.closed.Load() {
		w.dropQueued(item)
		return
	}

	select {
	case w.queue <- item:
		return
	default:
	}
	// Queue full: drop the oldest to make room, then try once more.
	select {
	case old := <-w.queue:
		w.dropQueued(old)
	default:
	}
	select {
	case w.queue <- item:
	default:
		w.dropQueued(item)
	}
}

func (w *Writer) dropQueued(item queuedEvent) {
	w.metrics.incQueueDropped()
	if item.member != nil {
		item.member.queueGap.Store(true)
		item.member.finish()
	}
}

// Stop closes the queue, lets every worker drain the events still
// buffered, and blocks until all workers have exited. Safe to call
// once; a second call is a no-op. After Stop, Enqueue is a no-op
// drop.
func (w *Writer) Stop() {
	w.enqueueMu.Lock()
	if w.closed.Swap(true) {
		w.enqueueMu.Unlock()
		return
	}
	close(w.queue)
	w.enqueueMu.Unlock()
	w.wg.Wait()
}

// QueueDepth reports how many events are currently buffered. Read by
// the /metrics handler for the kubeatlas_snapshot_queue_depth gauge.
func (w *Writer) QueueDepth() int { return len(w.queue) }

// Metrics returns the shared counter set.
func (w *Writer) Metrics() *Metrics { return w.metrics }

// worker consumes the queue until it is closed and drained. Each
// event gets up to maxRetries AppendEvent attempts with exponential
// backoff; a permanently-failing event is dropped (WriteFailed++).
//
// Payloads have already been removed before queueing; worker delivery does not
// retain or marshal a Kubernetes object's resource content.
func (w *Writer) worker(ctx context.Context) {
	defer w.wg.Done()
	for item := range w.queue {
		w.inFlight.Add(1)
		ok := w.writeWithRetry(ctx, item.event)
		w.inFlight.Add(-1)
		if item.member != nil {
			if !ok {
				item.member.writeGap.Store(true)
			}
			item.member.finish()
		}
	}
}

// writeWithRetry attempts AppendEvent up to maxRetries times with
// exponential backoff. ctx cancellation short-circuits the backoff
// sleeps (the worker still makes its remaining attempts back-to-back
// so a closed queue still drains) — see Stop for the clean path.
func (w *Writer) writeWithRetry(ctx context.Context, e graph.ResourceEvent) bool {
	delay := retryBaseDelay
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := w.sink.AppendEvent(ctx, e)
		if err == nil {
			w.metrics.incProcessed()
			return true
		}
		if attempt == maxRetries {
			slog.Warn("snapshot writer: dropping event after retry budget exhausted",
				"namespace", e.Namespace, "kind", e.Kind, "name", e.Name,
				"attempts", maxRetries, "err", err)
			w.metrics.incWriteFailed()
			return false
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			// Shutdown: skip the remaining sleeps but keep trying so
			// the drain completes promptly.
		}
		delay *= 2
	}
	return false
}
