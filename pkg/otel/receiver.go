// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// writeWorkers is how many goroutines drain the span queue into the
// store in parallel. A small fixed pool: batches already amortise the
// per-write cost, and the drop-on-full valve absorbs any overflow the
// workers can't keep up with.
const writeWorkers = 2

// SpanSink is the narrow store seam the receiver writes spans
// through. *postgres.Store satisfies it; keeping it narrow means the
// receiver never depends on the full graph.GraphStore (spans are a
// Tier 2-only concern, deliberately off that interface).
type SpanSink interface {
	WriteSpans(ctx context.Context, spans []graph.Span) error
}

// Receiver is an OTLP/gRPC trace receiver. It listens on its own port
// (default :4317, never the HTTP API's 8080), translates incoming
// OTLP spans into graph.Span values, and hands them to a SpanSink
// through a bounded, drop-on-full queue so a span flood or a slow
// store can never block the gRPC caller or the core graph path
// (invariant 2.5).
type Receiver struct {
	coltracepb.UnimplementedTraceServiceServer

	addr    string
	sink    SpanSink
	queue   chan []graph.Span
	metrics *Metrics

	grpcSrv *grpc.Server
	wg      sync.WaitGroup
}

// NewReceiver builds a Receiver. A non-positive bufferSize falls back
// to DefaultBufferSize; a nil Metrics gets a fresh set.
func NewReceiver(addr string, sink SpanSink, bufferSize int, m *Metrics) *Receiver {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}
	if m == nil {
		m = NewMetrics()
	}
	return &Receiver{
		addr:    addr,
		sink:    sink,
		queue:   make(chan []graph.Span, bufferSize),
		metrics: m,
	}
}

// Export is the OTLP TraceService handler. It translates the request
// into graph.Span values, counts them as received, then enqueues the
// batch without blocking: a full queue drops the batch and counts it.
// It always reports full success — partial-success accounting is not
// worth a round-trip for an opt-in overlay.
func (r *Receiver) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	spans := translate(req)
	if n := len(spans); n > 0 {
		r.metrics.addReceived(uint64(n))
		select {
		case r.queue <- spans:
		default:
			// Queue full: shed the batch rather than block the caller
			// or the core graph path. This is the backpressure valve.
			r.metrics.addDropped(uint64(n))
		}
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// Start runs the receiver until ctx is cancelled. It blocks — run it
// as one of runWatch's components.
//
// A listen failure does NOT take the process down (invariant 2.5,
// "degraded mode"): it is logged and the receiver parks until
// shutdown, so a port clash disables span ingestion without harming
// the core graph path. Start returns ctx.Err() on clean shutdown,
// which runWatch's result loop treats as non-fatal.
func (r *Receiver) Start(ctx context.Context) error {
	lis, err := net.Listen("tcp", r.addr)
	if err != nil {
		slog.Error("otel receiver: listen failed; span ingestion disabled for this run",
			"addr", r.addr, "err", err)
		<-ctx.Done()
		return ctx.Err()
	}

	r.grpcSrv = grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(r.grpcSrv, r)

	for i := 0; i < writeWorkers; i++ {
		r.wg.Add(1)
		go r.worker()
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("otel receiver listening", "addr", r.addr)
		err := r.grpcSrv.Serve(lis)
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		// Clean shutdown. stopGRPC drains in-flight Exports BEFORE we
		// close the queue — that ordering is what makes closing it safe
		// (no Export can still be mid-send on a closed channel).
		r.stopGRPC()
		close(r.queue)
		r.wg.Wait()
		<-serveErr
		return ctx.Err()
	case err := <-serveErr:
		// Serve died on its own (e.g. the listener broke). Degrade
		// rather than cascade a process exit. Serve returning does NOT
		// wait for in-flight unary RPCs, so we must still stopGRPC to
		// drain any Export mid-send before close(r.queue) — otherwise a
		// late send on the closed queue panics the process, the exact
		// failure invariant 2.5 forbids.
		slog.Error("otel receiver: grpc serve stopped early; span ingestion disabled", "err", err)
		r.stopGRPC()
		close(r.queue)
		r.wg.Wait()
		<-ctx.Done()
		return ctx.Err()
	}
}

// grpcStopGrace bounds GracefulStop on the shutdown path. Export is
// unary and never blocks, so handlers drain near-instantly in
// practice; the bound is defensive against a misbehaving client
// holding a connection busy, so receiver shutdown can never stall the
// whole runWatch result loop.
const grpcStopGrace = 10 * time.Second

// stopGRPC stops the gRPC server, draining in-flight Export handlers
// (the barrier that makes closing the span queue race-free) but
// falling back to a hard Stop if GracefulStop overruns the grace
// window.
func (r *Receiver) stopGRPC() {
	done := make(chan struct{})
	go func() {
		r.grpcSrv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grpcStopGrace):
		r.grpcSrv.Stop()
		<-done
	}
}

// worker drains span batches and writes them, each behind panic
// recovery so a bad span or a store-driver panic can never kill the
// goroutine or the process (invariant 2.5).
func (r *Receiver) worker() {
	defer r.wg.Done()
	for batch := range r.queue {
		r.writeBatch(batch)
	}
}

func (r *Receiver) writeBatch(batch []graph.Span) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("otel receiver: panic writing span batch; recovered",
				"panic", rec, "count", len(batch))
		}
	}()
	// A short, detached deadline: the receiver's own ctx may already be
	// cancelled during shutdown drain, and span writes are best-effort.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.sink.WriteSpans(ctx, batch); err != nil {
		slog.Warn("otel receiver: write span batch failed", "err", err, "count", len(batch))
		return
	}
	r.metrics.addWritten(uint64(len(batch)))
}

// translate flattens an OTLP export request into storage-shaped
// spans, lifting the K8s Semantic-Convention resource attributes
// (service.name / k8s.namespace.name / k8s.pod.name /
// k8s.deployment.name) onto each span so the store can index them.
func translate(req *coltracepb.ExportTraceServiceRequest) []graph.Span {
	var out []graph.Span
	for _, rs := range req.GetResourceSpans() {
		ra := attrMap(rs.GetResource().GetAttributes())
		svc := stringAttr(ra, "service.name")
		ns := stringAttr(ra, "k8s.namespace.name")
		pod := stringAttr(ra, "k8s.pod.name")
		dep := stringAttr(ra, "k8s.deployment.name")
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				start := sp.GetStartTimeUnixNano()
				var dur int64
				if end := sp.GetEndTimeUnixNano(); end > start {
					dur = int64(end - start)
				}
				out = append(out, graph.Span{
					TraceID:       hex.EncodeToString(sp.GetTraceId()),
					SpanID:        hex.EncodeToString(sp.GetSpanId()),
					ParentSpanID:  hex.EncodeToString(sp.GetParentSpanId()),
					ServiceName:   svc,
					K8sNamespace:  ns,
					K8sPod:        pod,
					K8sDeployment: dep,
					StartTime:     time.Unix(0, int64(start)).UTC(),
					DurationNS:    dur,
					Attributes:    flattenAttrs(sp.GetAttributes()),
				})
			}
		}
	}
	return out
}

func attrMap(kvs []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	m := make(map[string]*commonpb.AnyValue, len(kvs))
	for _, kv := range kvs {
		m[kv.GetKey()] = kv.GetValue()
	}
	return m
}

func stringAttr(m map[string]*commonpb.AnyValue, key string) string {
	if v, ok := m[key]; ok {
		return v.GetStringValue()
	}
	return ""
}

// flattenAttrs reduces span attributes to a JSON-storable map of
// scalar values. Non-scalar values (arrays, nested kvlists) fall back
// to their string form; the overlay does not need deep attribute
// structure.
func flattenAttrs(kvs []*commonpb.KeyValue) map[string]any {
	if len(kvs) == 0 {
		return nil
	}
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[kv.GetKey()] = scalar(kv.GetValue())
	}
	return m
}

func scalar(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	default:
		return v.GetStringValue()
	}
}
