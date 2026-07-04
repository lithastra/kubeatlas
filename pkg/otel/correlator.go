// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

const (
	// defaultCorrelateWindow is the span lookback each correlation pass
	// scans. Repeated calls between the same two resources within it
	// collapse to a single runtime edge (the guide's "10-minute
	// dedup window").
	defaultCorrelateWindow = 10 * time.Minute
	// defaultCorrelateInterval is how often the correlator runs. It is
	// shorter than the window so an edge shows up within ~a minute of
	// the first call (the guide allows <=1min correlation latency); the
	// GREATEST-on-conflict upsert keeps overlapping windows from
	// inflating call_count.
	defaultCorrelateInterval = time.Minute
	// maxCorrelateSpans caps how many spans one pass pulls, bounding a
	// pass's memory on a high-throughput cluster. Older spans roll into
	// the next pass; the overlay is best-effort (invariant 2.5).
	maxCorrelateSpans = 50000
)

// SpanSource is the read seam the correlator pulls recent spans from.
// *postgres.Store satisfies it (QuerySpans).
type SpanSource interface {
	QuerySpans(ctx context.Context, serviceName string, since time.Time, limit int) ([]graph.Span, error)
}

// RuntimeEdgeSink is the store seam the correlator writes inferred
// runtime edges through and prunes stale ones with. *postgres.Store
// satisfies it. Kept narrow, and off graph.GraphStore, because runtime
// edges are a Tier 2-only overlay concern (invariant 2.2) — the
// in-memory backend never sees them.
type RuntimeEdgeSink interface {
	UpsertRuntimeEdges(ctx context.Context, edges []graph.RuntimeEdge) error
	DeleteOldRuntimeEdges(ctx context.Context, cutoff time.Time) (int64, error)
}

// Correlator turns persisted OTLP spans into CALLS_AT_RUNTIME overlay
// edges. It is a detached background job (never the critical path,
// invariant 2.5): each pass reads the recent span window, infers
// caller->callee calls from parent/child span pairs whose service.name
// differs, resolves each service to a graph resource, and upserts the
// deduped edges into the Tier 2 otel_runtime_edges table. Missing
// attributes degrade to an "unmatched" count, never a panic.
type Correlator struct {
	source    SpanSource
	lister    graph.ResourceLister
	sink      RuntimeEdgeSink
	window    time.Duration
	interval  time.Duration
	retention time.Duration
	metrics   *Metrics
}

// NewCorrelator builds a Correlator. A non-positive retention falls
// back to DefaultRetention (runtime edges expire on the same window as
// the spans they came from); a nil Metrics gets a fresh set.
func NewCorrelator(source SpanSource, lister graph.ResourceLister, sink RuntimeEdgeSink, retention time.Duration, m *Metrics) *Correlator {
	if retention <= 0 {
		retention = DefaultRetention
	}
	if m == nil {
		m = NewMetrics()
	}
	return &Correlator{
		source:    source,
		lister:    lister,
		sink:      sink,
		window:    defaultCorrelateWindow,
		interval:  defaultCorrelateInterval,
		retention: retention,
		metrics:   m,
	}
}

// Start runs the correlation loop until ctx is cancelled. It blocks —
// run it in a goroutine. A pass fires immediately on Start, then once
// per interval. Mirrors SpanRetainer; terminated only by ctx (no Stop).
func (c *Correlator) Start(ctx context.Context) {
	c.runOnce(ctx, time.Now())
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.runOnce(ctx, now)
		}
	}
}

// runOnce executes a single correlation pass at wall-clock time now. A
// failure at any step is logged, not fatal — the next tick retries, so
// a transient store outage never crashes the process or the core graph
// path.
func (c *Correlator) runOnce(ctx context.Context, now time.Time) {
	spans, err := c.source.QuerySpans(ctx, "", now.Add(-c.window), maxCorrelateSpans)
	if err != nil {
		slog.Warn("otel correlator: query spans failed", "err", err)
		return
	}
	edges, unmatched := c.correlate(ctx, spans)
	if unmatched > 0 {
		c.metrics.addUnmatched(uint64(unmatched))
	}
	if len(edges) > 0 {
		if err := c.sink.UpsertRuntimeEdges(ctx, edges); err != nil {
			slog.Warn("otel correlator: upsert runtime edges failed", "err", err, "count", len(edges))
		} else {
			c.metrics.addRuntimeEdges(uint64(len(edges)))
		}
	}
	// Expire runtime edges not re-observed within the retention window,
	// so a decommissioned call path drops out of the overlay.
	if n, err := c.sink.DeleteOldRuntimeEdges(ctx, now.Add(-c.retention)); err != nil {
		slog.Warn("otel correlator: prune runtime edges failed", "err", err)
	} else if n > 0 {
		slog.Info("otel correlator: pruned stale runtime edges", "deleted", n)
	}
}

// correlate turns a batch of spans into deduped runtime edges, resolving
// each span's service to a graph resource. It returns the edges plus the
// number of call endpoints that could not be resolved to a resource
// (surfaced as kubeatlas_otel_unmatched_spans_total). It is pure with
// respect to the sink — the resolver reads through c.lister only — which
// is what makes it table-testable without a database.
func (c *Correlator) correlate(ctx context.Context, spans []graph.Span) ([]graph.RuntimeEdge, int) {
	calls := inferCalls(spans)
	if len(calls) == 0 {
		return nil, 0
	}
	rz := c.buildResolver(ctx, spans)

	agg := make(map[[2]string]*graph.RuntimeEdge)
	unmatched := 0
	for _, cl := range calls {
		fromID, _, okF := rz.resolve(cl.parent)
		toID, toNS, okT := rz.resolve(cl.child)
		if !okF {
			unmatched++
		}
		if !okT {
			unmatched++
		}
		if !okF || !okT {
			continue
		}
		if fromID == toID {
			// Both spans resolved to the same workload (e.g. an internal
			// re-entrant call). Not a topology edge.
			continue
		}
		key := [2]string{fromID, toID}
		ts := cl.child.StartTime // when the call was observed
		if e, ok := agg[key]; ok {
			e.CallCount++
			if ts.Before(e.FirstSeen) {
				e.FirstSeen = ts
			}
			if ts.After(e.LastSeen) {
				e.LastSeen = ts
			}
		} else {
			agg[key] = &graph.RuntimeEdge{
				FromID:      fromID,
				ToID:        toID,
				FromService: cl.parent.ServiceName,
				ToService:   cl.child.ServiceName,
				Namespace:   toNS,
				FirstSeen:   ts,
				LastSeen:    ts,
				CallCount:   1,
			}
		}
	}

	out := make([]graph.RuntimeEdge, 0, len(agg))
	for _, e := range agg {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FromID != out[j].FromID {
			return out[i].FromID < out[j].FromID
		}
		return out[i].ToID < out[j].ToID
	})
	return out, unmatched
}

// buildResolver loads, once per pass, the resources in every namespace
// the span batch touches, and indexes them by ID. Scoping to the
// namespaces actually seen keeps a pass off the O(all-resources)
// Snapshot path the pushdown work removed from the request path — a
// background job can afford a few namespace list queries.
func (c *Correlator) buildResolver(ctx context.Context, spans []graph.Span) *resolver {
	nsSet := make(map[string]struct{})
	for _, s := range spans {
		if s.K8sNamespace != "" {
			nsSet[s.K8sNamespace] = struct{}{}
		}
	}
	var resources []graph.Resource
	for ns := range nsSet {
		rs, err := c.lister.ListResources(ctx, graph.Filter{Namespace: ns})
		if err != nil {
			slog.Warn("otel correlator: list resources failed", "namespace", ns, "err", err)
			continue
		}
		resources = append(resources, rs...)
	}
	return newResolver(resources)
}

// call is one inferred parent->child runtime call.
type call struct {
	parent graph.Span
	child  graph.Span
}

// inferCalls pairs each non-root span with its parent (by span id) and
// emits a call whenever the two belong to different, named services —
// the parent's service called the child's service. Intra-service spans
// and spans whose parent is not in the batch are skipped (best-effort:
// a call whose halves land in different windows simply reappears next
// pass). span_id is globally unique in storage, so a flat map is a
// correct parent index.
func inferCalls(spans []graph.Span) []call {
	byID := make(map[string]graph.Span, len(spans))
	for _, s := range spans {
		if s.SpanID != "" {
			byID[s.SpanID] = s
		}
	}
	var out []call
	for _, s := range spans {
		if s.ParentSpanID == "" {
			continue // root span: no caller
		}
		parent, ok := byID[s.ParentSpanID]
		if !ok {
			continue // parent outside this window
		}
		if parent.ServiceName == "" || s.ServiceName == "" {
			continue // cannot attribute a call without both service names
		}
		if parent.ServiceName == s.ServiceName {
			continue // same service: an internal span, not a call edge
		}
		out = append(out, call{parent: parent, child: s})
	}
	return out
}

// resolver maps a span's K8s Semantic-Convention attributes to the id
// of the graph resource it ran on, using an in-memory set of resource
// IDs built once per correlation pass.
type resolver struct {
	ids map[string]struct{}
}

func newResolver(resources []graph.Resource) *resolver {
	ids := make(map[string]struct{}, len(resources))
	for _, r := range resources {
		ids[r.ID()] = struct{}{}
	}
	return &resolver{ids: ids}
}

// resolve maps a span to the graph resource it most likely originated
// from, preferring the workload identity (k8s.deployment.name) over the
// Pod, then falling back to a workload or Service named service.name in
// the same namespace. It returns ok=false when nothing matches — the
// caller counts that as unmatched and degrades, never panics
// (invariant 2.5). Resolution is single-cluster: spans carry no cluster
// id, so candidate IDs use the bare <ns>/<kind>/<name> form.
func (rz *resolver) resolve(sp graph.Span) (id, namespace string, ok bool) {
	ns := sp.K8sNamespace
	if ns == "" {
		return "", "", false
	}
	var cands []string
	if sp.K8sDeployment != "" {
		cands = append(cands, ns+"/Deployment/"+sp.K8sDeployment)
	}
	if sp.K8sPod != "" {
		cands = append(cands, ns+"/Pod/"+sp.K8sPod)
	}
	if sp.ServiceName != "" {
		cands = append(cands,
			ns+"/Deployment/"+sp.ServiceName,
			ns+"/StatefulSet/"+sp.ServiceName,
			ns+"/DaemonSet/"+sp.ServiceName,
			ns+"/Service/"+sp.ServiceName,
		)
	}
	for _, cand := range cands {
		if _, present := rz.ids[cand]; present {
			return cand, ns, true
		}
	}
	return "", ns, false
}
