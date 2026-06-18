// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// fakeSink records the batches WriteSpans is called with, and can be
// told to fail or panic to exercise the worker's error/panic paths.
type fakeSink struct {
	mu      sync.Mutex
	batches [][]graph.Span
	err     error
	doPanic bool
}

func (f *fakeSink) WriteSpans(_ context.Context, spans []graph.Span) error {
	if f.doPanic {
		panic("simulated store panic")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, spans)
	return f.err
}

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   k,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
	}
}

func sampleRequest() *coltracepb.ExportTraceServiceRequest {
	return &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kv("service.name", "petclinic-api"),
					kv("k8s.namespace.name", "petclinic"),
					kv("k8s.pod.name", "api-7d9"),
					kv("k8s.deployment.name", "api"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{
					{
						TraceId:           []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
						SpanId:            []byte{0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8},
						ParentSpanId:      []byte{0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8},
						Name:              "GET /owners",
						StartTimeUnixNano: 1_000_000_000,
						EndTimeUnixNano:   1_000_500_000,
						Attributes:        []*commonpb.KeyValue{kv("http.method", "GET")},
					},
					{
						TraceId:           []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
						SpanId:            []byte{0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6, 0xc7, 0xc8},
						StartTimeUnixNano: 2_000_000_000,
					},
				},
			}},
		}},
	}
}

func TestExport_TranslatesAndEnqueues(t *testing.T) {
	// bufferSize big enough that nothing drops; no workers started, so
	// the batch stays on the queue for inspection.
	r := NewReceiver(":0", &fakeSink{}, 16, nil)
	if _, err := r.Export(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got := r.metrics.Snapshot().Received; got != 2 {
		t.Fatalf("received = %d, want 2", got)
	}
	select {
	case batch := <-r.queue:
		if len(batch) != 2 {
			t.Fatalf("batch len = %d, want 2", len(batch))
		}
		s0 := batch[0]
		if s0.TraceID != "0102030405060708090a0b0c0d0e0f10" {
			t.Errorf("trace_id = %q", s0.TraceID)
		}
		if s0.SpanID != "a1a2a3a4a5a6a7a8" {
			t.Errorf("span_id = %q", s0.SpanID)
		}
		if s0.ParentSpanID != "b1b2b3b4b5b6b7b8" {
			t.Errorf("parent_span_id = %q", s0.ParentSpanID)
		}
		if s0.ServiceName != "petclinic-api" || s0.K8sNamespace != "petclinic" ||
			s0.K8sPod != "api-7d9" || s0.K8sDeployment != "api" {
			t.Errorf("resource attrs not lifted: %+v", s0)
		}
		if s0.DurationNS != 500_000 {
			t.Errorf("duration = %d, want 500000", s0.DurationNS)
		}
		if s0.Attributes["http.method"] != "GET" {
			t.Errorf("span attributes not flattened: %+v", s0.Attributes)
		}
		// Second span: no parent, no end time → empty parent, zero dur.
		s1 := batch[1]
		if s1.ParentSpanID != "" {
			t.Errorf("root span should have empty parent, got %q", s1.ParentSpanID)
		}
		if s1.DurationNS != 0 {
			t.Errorf("span with no end time should have zero duration, got %d", s1.DurationNS)
		}
	default:
		t.Fatal("expected one batch on the queue")
	}
}

func TestExport_DropsWhenFull(t *testing.T) {
	// bufferSize 1, no workers draining: the first Export fills the
	// queue, the second must drop without blocking.
	r := NewReceiver(":0", &fakeSink{}, 1, nil)
	if _, err := r.Export(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Export(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	s := r.metrics.Snapshot()
	if s.Received != 4 {
		t.Errorf("received = %d, want 4 (2 spans x 2 calls)", s.Received)
	}
	if s.Dropped != 2 {
		t.Errorf("dropped = %d, want 2 (the second full batch)", s.Dropped)
	}
}

func TestWriteBatch_PanicRecovery(t *testing.T) {
	// A panicking sink must not escape writeBatch — the worker (and
	// the process) survive. written stays 0 because the write failed.
	r := NewReceiver(":0", &fakeSink{doPanic: true}, 16, nil)
	r.writeBatch([]graph.Span{{SpanID: "x"}}) // must not panic out
	if got := r.metrics.Snapshot().Written; got != 0 {
		t.Errorf("written = %d, want 0 after a panicking write", got)
	}
}

func TestWriteBatch_CountsWritten(t *testing.T) {
	sink := &fakeSink{}
	r := NewReceiver(":0", sink, 16, nil)
	r.writeBatch([]graph.Span{{SpanID: "a"}, {SpanID: "b"}})
	if got := r.metrics.Snapshot().Written; got != 2 {
		t.Errorf("written = %d, want 2", got)
	}
	if len(sink.batches) != 1 || len(sink.batches[0]) != 2 {
		t.Errorf("sink received %d batches, want one batch of 2", len(sink.batches))
	}
}

func TestExport_EmptyRequestNoEnqueue(t *testing.T) {
	r := NewReceiver(":0", &fakeSink{}, 16, nil)
	if _, err := r.Export(context.Background(), &coltracepb.ExportTraceServiceRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := r.metrics.Snapshot().Received; got != 0 {
		t.Errorf("received = %d, want 0 for an empty request", got)
	}
	if len(r.queue) != 0 {
		t.Errorf("queue depth = %d, want 0", len(r.queue))
	}
}

// TestStart_CleanShutdown exercises the real listen + serve + worker
// path and the ctx-cancel shutdown: stopGRPC drains, the queue closes,
// workers exit, and Start returns context.Canceled without hanging or
// panicking (send-on-closed-channel regression guard).
func TestStart_CleanShutdown(t *testing.T) {
	r := NewReceiver("127.0.0.1:0", &fakeSink{}, 16, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Start returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after ctx cancel — shutdown hang?")
	}
}

// TestStart_ListenFailureDegrades pins the invariant-2.5 "degraded
// mode" rule: a receiver that cannot bind its port must NOT return the
// listen error (which would cascade a process exit via runWatch's
// result loop) — it logs, parks, and returns context.Canceled on
// shutdown like any clean component.
func TestStart_ListenFailureDegrades(t *testing.T) {
	// Occupy a port, then point the receiver at it so net.Listen fails.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()

	r := NewReceiver(occupied.Addr().String(), &fakeSink{}, 16, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Start returned %v, want context.Canceled (degraded park, not a cascading listen error)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after ctx cancel on a failed listen")
	}
}
