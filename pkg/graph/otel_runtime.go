// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"strconv"
	"time"
)

// RuntimeEdge is one observed runtime call between two graph resources,
// inferred by the F-204 correlator (P5-T5) from OTLP trace spans: a
// parent span in service A calling a child span in service B becomes a
// caller -> callee edge.
//
// It is the storage-shaped projection persisted to the Tier 2
// otel_runtime_edges table, deliberately NOT a graph.Edge in the
// declarative store. Runtime edges are an opt-in overlay served only by
// GET /api/v1/otel/overlay and must never leak into /api/v1/graph or
// /api/v1alpha1/graph, which invariant 2.2 keeps byte-identical to
// v1.4. Like Span, it lives in pkg/graph so both pkg/otel (producer)
// and pkg/store/postgres (persistence) can reference it without an
// import cycle.
type RuntimeEdge struct {
	// FromID and ToID are graph.Resource IDs — the correlator resolved
	// each span's service to the resource it ran on. The edge points
	// caller -> callee.
	FromID string
	ToID   string

	// FromService and ToService are the OTel service.name values the
	// edge was inferred from, kept for display and debugging even after
	// resolution to resource IDs.
	FromService string
	ToService   string

	// Namespace is the callee's K8s namespace; the overlay API filters
	// on it. Empty when the producing span carried no namespace.
	Namespace string

	// FirstSeen / LastSeen bound the observation window; CallCount is
	// how many parent->child span pairs were folded into this edge
	// within a single correlation pass (see the correlator's
	// GREATEST-on-conflict upsert — it is a peak-per-window activity
	// indicator, not a monotonic lifetime total).
	FirstSeen time.Time
	LastSeen  time.Time
	CallCount int64
}

// Edge projects the runtime edge onto a declarative-shaped graph.Edge
// carrying the overlay-only CALLS_AT_RUNTIME type, so the overlay API
// can reuse the standard edge JSON shape. The observed service names
// and call count ride on Attributes (append-only, omitted when empty),
// exactly as ENFORCES rides its violation status there.
//
// This projection is the ONLY place a CALLS_AT_RUNTIME graph.Edge is
// materialised, and only ever inside the /api/v1/otel/overlay response
// — never in the declarative graph.
func (re RuntimeEdge) Edge() Edge {
	return Edge{
		From: re.FromID,
		To:   re.ToID,
		Type: EdgeTypeCallsAtRuntime,
		Attributes: map[string]string{
			"from_service": re.FromService,
			"to_service":   re.ToService,
			"call_count":   strconv.FormatInt(re.CallCount, 10),
		},
	}
}
