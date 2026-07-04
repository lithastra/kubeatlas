// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// OverlayClass classifies one edge in the F-204 runtime-vs-declared
// overlay comparison (P5-T5 / P5-T6, the ?compare=true mode).
type OverlayClass string

const (
	// ClassDeclaredOnly: the declarative graph has this edge but no
	// runtime call was observed on it — a wired dependency that is
	// idle, mis-configured, or simply not exercised in the window. The
	// UI highlights these (yellow) as the interesting gaps.
	ClassDeclaredOnly OverlayClass = "declared_only"
	// ClassObservedOnly: a runtime call was observed with no declarative
	// edge to explain it — traffic the topology does not predict.
	ClassObservedOnly OverlayClass = "observed_only"
	// ClassBoth: declared and also observed — the healthy case.
	ClassBoth OverlayClass = "both"
)

// OverlayEdge is one classified edge in the overlay comparison.
type OverlayEdge struct {
	From string `json:"from"`
	To   string `json:"to"`

	Class OverlayClass `json:"class"`

	// CallCount is the observed runtime call count for the edge, present
	// (non-zero) only for ClassObservedOnly and ClassBoth.
	CallCount int64 `json:"callCount,omitempty"`
}

// Overlay compares declared edges against observed runtime edges and
// classifies every (from, to) resource pair as declared_only,
// observed_only, or both. It is a pure set operation over resource-ID
// pairs — the caller decides which declared edges are call-shaped
// enough to compare (P5-T6 feeds it the declarative edges it wants
// overlaid); this function makes no assumption about edge type.
//
// The result is sorted by (from, to) so callers and tests get a stable
// order. "both" requires an exact resource-ID match between a declared
// edge and a runtime edge; runtime call counts are summed across any
// duplicate observed pairs.
func Overlay(declared []graph.Edge, observed []graph.RuntimeEdge) []OverlayEdge {
	type pair struct{ from, to string }

	declaredSet := make(map[pair]struct{}, len(declared))
	for _, e := range declared {
		declaredSet[pair{e.From, e.To}] = struct{}{}
	}
	observedCount := make(map[pair]int64, len(observed))
	for _, e := range observed {
		observedCount[pair{e.FromID, e.ToID}] += e.CallCount
	}

	out := make([]OverlayEdge, 0, len(declaredSet)+len(observedCount))
	for p := range declaredSet {
		cls := ClassDeclaredOnly
		var cnt int64
		if c, ok := observedCount[p]; ok {
			cls = ClassBoth
			cnt = c
		}
		out = append(out, OverlayEdge{From: p.from, To: p.to, Class: cls, CallCount: cnt})
	}
	for p, cnt := range observedCount {
		if _, ok := declaredSet[p]; ok {
			continue // already emitted as ClassBoth above
		}
		out = append(out, OverlayEdge{From: p.from, To: p.to, Class: ClassObservedOnly, CallCount: cnt})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}
