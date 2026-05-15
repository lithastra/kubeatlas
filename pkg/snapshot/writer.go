// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
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

	// rawDataMaxBytes caps how big a single event's Data payload may
	// be before the writer replaces it with a truncation marker.
	// Stops one oversized Secret/ConfigMap from bloating the
	// resource_events table (F-109 anti-pattern: events table must
	// not grow without bound).
	rawDataMaxBytes = 10 * 1024
)

// truncatedMarker replaces an event's Data when the original
// exceeds rawDataMaxBytes. Consumers that need the full object
// re-fetch it from the K8s API rather than from the event stream.
var truncatedMarker = map[string]any{"kubeatlas.io/truncated": true}

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
	queue   chan graph.ResourceEvent
	workers int
	metrics *Metrics

	wg sync.WaitGroup
	// closed guards against a send on the closed queue: once Stop has
	// run, a late Enqueue is counted as a drop instead of panicking.
	closed atomic.Bool
}

// New builds a Writer. The Metrics pointer is shared with the
// api package so /metrics can surface the counters; pass
// NewMetrics() if no sharing is needed.
func New(sink EventSink, cfg Config, m *Metrics) *Writer {
	cfg = cfg.withDefaults()
	if m == nil {
		m = NewMetrics()
	}
	return &Writer{
		sink:    sink,
		queue:   make(chan graph.ResourceEvent, cfg.QueueSize),
		workers: cfg.Workers,
		metrics: m,
	}
}

// Start spawns the worker pool. ctx cancellation makes in-flight
// retry backoff sleeps return early; the clean shutdown path is
// Stop, which drains the queue first.
func (w *Writer) Start(ctx context.Context) {
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
	if w.closed.Load() {
		w.metrics.incQueueDropped()
		return
	}
	// Note: data-size capping happens worker-side (capEventData in
	// worker), NOT here — Enqueue runs on the informer's hot path
	// and must stay allocation-light.

	select {
	case w.queue <- e:
		return
	default:
	}
	// Queue full: drop the oldest to make room, then try once more.
	select {
	case <-w.queue:
		w.metrics.incQueueDropped()
	default:
	}
	select {
	case w.queue <- e:
	default:
		w.metrics.incQueueDropped()
	}
}

// Stop closes the queue, lets every worker drain the events still
// buffered, and blocks until all workers have exited. Safe to call
// once; a second call is a no-op. After Stop, Enqueue is a no-op
// drop.
func (w *Writer) Stop() {
	if w.closed.Swap(true) {
		return
	}
	close(w.queue)
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
// Data-size capping runs here (not in Enqueue) so the informer hot
// path stays light — measuring a payload means marshalling it.
func (w *Writer) worker(ctx context.Context) {
	defer w.wg.Done()
	for e := range w.queue {
		w.writeWithRetry(ctx, capEventData(e))
	}
}

// writeWithRetry attempts AppendEvent up to maxRetries times with
// exponential backoff. ctx cancellation short-circuits the backoff
// sleeps (the worker still makes its remaining attempts back-to-back
// so a closed queue still drains) — see Stop for the clean path.
func (w *Writer) writeWithRetry(ctx context.Context, e graph.ResourceEvent) {
	delay := retryBaseDelay
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := w.sink.AppendEvent(ctx, e)
		if err == nil {
			w.metrics.incProcessed()
			return
		}
		if attempt == maxRetries {
			slog.Warn("snapshot writer: dropping event after retry budget exhausted",
				"namespace", e.Namespace, "kind", e.Kind, "name", e.Name,
				"attempts", maxRetries, "err", err)
			w.metrics.incWriteFailed()
			return
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			// Shutdown: skip the remaining sleeps but keep trying so
			// the drain completes promptly.
		}
		delay *= 2
	}
}

// capEventData replaces an oversized Data payload with a truncation
// marker. The store's JSONB column can hold large blobs, but the
// event stream is history, not a resource cache — a 1 MB Secret
// should not be copied into resource_events on every update.
//
// Size is measured by marshalling to JSON, which is what the store
// does on insert anyway; doing it worker-side keeps that cost off
// the informer hot path.
func capEventData(e graph.ResourceEvent) graph.ResourceEvent {
	if e.Data == nil {
		return e
	}
	b, err := json.Marshal(e.Data)
	if err != nil {
		// Unmarshalable Data can't be stored as JSONB anyway —
		// replace it with the marker rather than fail the event.
		e.Data = truncatedMarker
		return e
	}
	if len(b) > rawDataMaxBytes {
		e.Data = truncatedMarker
	}
	return e
}
