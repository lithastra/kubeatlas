// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// OtelReader is the read seam the F-204 overlay handlers use: recent
// spans and correlated runtime edges. *postgres.Store satisfies it.
// Kept OFF graph.GraphStore because spans and runtime edges are Tier 2
// only (invariant 2.2) — a Tier 1 install wires no reader and the
// handlers return 503.
type OtelReader interface {
	QuerySpans(ctx context.Context, serviceName string, since time.Time, limit int) ([]graph.Span, error)
	QueryRuntimeEdges(ctx context.Context, namespace string, since time.Time) ([]graph.RuntimeEdge, error)
}

const (
	// defaultOverlayWindow is the recency floor for overlay/traces when
	// the caller sends no ?last. An edge or span older than this is not
	// shown — the overlay is a "what's calling what lately" view.
	defaultOverlayWindow = time.Hour
	// maxOverlayWindow caps ?last so a caller can't ask the store for an
	// unbounded scan.
	maxOverlayWindow = 24 * time.Hour
	// traceSpanLimit bounds how many spans the traces endpoint pulls
	// before summarising.
	traceSpanLimit = 2000
)

// OtelTraceSummary is one trace collapsed to a topology-relevant
// summary — KubeAtlas condenses traces into a runtime-call view, it is
// not a full trace viewer (that is Jaeger/Tempo's job).
type OtelTraceSummary struct {
	TraceID    string    `json:"traceId"`
	Services   []string  `json:"services"`
	SpanCount  int       `json:"spanCount"`
	Start      time.Time `json:"start"`
	DurationNS int64     `json:"durationNs"`
}

// OtelTracesResponse is the body of GET /api/v1/otel/traces.
type OtelTracesResponse struct {
	Traces []OtelTraceSummary `json:"traces"`
	Count  int                `json:"count"`
}

// OtelOverlayResponse is the default (non-compare) body of
// GET /api/v1/otel/overlay: the observed CALLS_AT_RUNTIME edges only.
type OtelOverlayResponse struct {
	Namespace string       `json:"namespace"`
	Edges     []graph.Edge `json:"edges"`
	Count     int          `json:"count"`
}

// OtelOverlayCompareResponse is the ?compare=true body: every declared
// vs observed pair classified declared_only / observed_only / both.
type OtelOverlayCompareResponse struct {
	Namespace string                 `json:"namespace"`
	Edges     []analysis.OverlayEdge `json:"edges"`
	Summary   OverlayCompareSummary  `json:"summary"`
	Count     int                    `json:"count"`
}

// OverlayCompareSummary is the per-class tally in a compare response.
type OverlayCompareSummary struct {
	DeclaredOnly int `json:"declaredOnly"`
	ObservedOnly int `json:"observedOnly"`
	Both         int `json:"both"`
}

// otelUnavailable writes the 503 every overlay handler returns when the
// feature is not active. Spans + runtime edges are Tier 2 only and
// opt-in; main.go wires WithOtelOverlay solely on a Tier 2 install with
// otel.enabled, so a nil reader means "Tier 1, or otel switched off".
func otelUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, CodeUnavailable,
		"the OTel overlay requires Tier 2 (PostgreSQL) with otel.enabled=true")
}

// parseWindow reads ?last as a Go duration, defaulting to
// defaultOverlayWindow and clamping to maxOverlayWindow. A malformed
// value is a 400 so a typo surfaces rather than silently defaulting.
func parseWindow(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultOverlayWindow, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	if d > maxOverlayWindow {
		d = maxOverlayWindow
	}
	return d, nil
}

// handleOtelTraces serves GET /api/v1/otel/traces?service=<name>&last=<dur>:
// recent trace summaries (condensed to services + span count), newest
// first.
func (s *Server) handleOtelTraces(w http.ResponseWriter, r *http.Request) {
	if s.otelReader == nil {
		otelUnavailable(w)
		return
	}
	q := r.URL.Query()
	window, err := parseWindow(q.Get("last"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid 'last': "+err.Error())
		return
	}
	since := time.Now().Add(-window)
	spans, err := s.otelReader.QuerySpans(r.Context(), q.Get("service"), since, traceSpanLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	traces := summariseTraces(spans)
	writeJSON(w, http.StatusOK, OtelTracesResponse{Traces: traces, Count: len(traces)})
}

// handleOtelOverlay serves GET /api/v1/otel/overlay. Without compare it
// returns the observed CALLS_AT_RUNTIME edges; with ?compare=true it
// classifies them against the namespace's declared ROUTES_TO edges.
func (s *Server) handleOtelOverlay(w http.ResponseWriter, r *http.Request) {
	if s.otelReader == nil {
		otelUnavailable(w)
		return
	}
	q := r.URL.Query()
	ns := q.Get("namespace")
	window, err := parseWindow(q.Get("last"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid 'last': "+err.Error())
		return
	}
	since := time.Now().Add(-window)

	observed, err := s.otelReader.QueryRuntimeEdges(r.Context(), ns, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	if q.Get("compare") == "true" {
		s.writeOverlayCompare(w, r, ns, observed)
		return
	}

	edges := make([]graph.Edge, 0, len(observed))
	for _, re := range observed {
		edges = append(edges, re.Edge())
	}
	writeJSON(w, http.StatusOK, OtelOverlayResponse{Namespace: ns, Edges: edges, Count: len(edges)})
}

// writeOverlayCompare renders the three-way classification. Compare mode
// needs a namespace to scope the declared edges it overlays (a cluster-
// wide compare would walk the whole graph — the OOM path the pushdown
// work removed from request handling), so an empty namespace is a 400.
func (s *Server) writeOverlayCompare(w http.ResponseWriter, r *http.Request, ns string, observed []graph.RuntimeEdge) {
	if ns == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"compare=true requires a namespace")
		return
	}
	sub, err := s.store.GetNamespaceSubgraph(r.Context(), ns, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	// Overlay observed runtime calls against declared service routing
	// (ROUTES_TO) — the declarative edge that means "traffic flows
	// here". Other declarative edge types (OWNS, USES_CONFIGMAP, ...)
	// are not call-shaped, so comparing them to runtime calls would be
	// noise; see docs/concepts/otel-overlay.md for the granularity
	// caveat.
	declared := make([]graph.Edge, 0)
	for _, e := range sub.Edges {
		if e.Type == graph.EdgeTypeRoutesTo {
			declared = append(declared, e)
		}
	}
	classified := analysis.Overlay(declared, observed)

	var summary OverlayCompareSummary
	for _, e := range classified {
		switch e.Class {
		case analysis.ClassDeclaredOnly:
			summary.DeclaredOnly++
		case analysis.ClassObservedOnly:
			summary.ObservedOnly++
		case analysis.ClassBoth:
			summary.Both++
		}
	}
	writeJSON(w, http.StatusOK, OtelOverlayCompareResponse{
		Namespace: ns,
		Edges:     classified,
		Summary:   summary,
		Count:     len(classified),
	})
}

// summariseTraces collapses a span slice (newest-first from the store)
// into per-trace summaries, preserving that newest-first trace order.
func summariseTraces(spans []graph.Span) []OtelTraceSummary {
	type acc struct {
		services  map[string]struct{}
		spanCount int
		minStart  time.Time
		maxEnd    time.Time
	}
	order := make([]string, 0)
	byTrace := make(map[string]*acc)
	for _, sp := range spans {
		a := byTrace[sp.TraceID]
		if a == nil {
			a = &acc{services: make(map[string]struct{}), minStart: sp.StartTime, maxEnd: sp.StartTime}
			byTrace[sp.TraceID] = a
			order = append(order, sp.TraceID)
		}
		if sp.ServiceName != "" {
			a.services[sp.ServiceName] = struct{}{}
		}
		a.spanCount++
		if sp.StartTime.Before(a.minStart) {
			a.minStart = sp.StartTime
		}
		end := sp.StartTime.Add(time.Duration(sp.DurationNS))
		if end.After(a.maxEnd) {
			a.maxEnd = end
		}
	}
	out := make([]OtelTraceSummary, 0, len(order))
	for _, id := range order {
		a := byTrace[id]
		services := make([]string, 0, len(a.services))
		for svc := range a.services {
			services = append(services, svc)
		}
		sort.Strings(services)
		out = append(out, OtelTraceSummary{
			TraceID:    id,
			Services:   services,
			SpanCount:  a.spanCount,
			Start:      a.minStart,
			DurationNS: a.maxEnd.Sub(a.minStart).Nanoseconds(),
		})
	}
	return out
}
