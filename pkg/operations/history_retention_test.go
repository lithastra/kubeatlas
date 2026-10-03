// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestHistoryRetentionEvidenceValidation(t *testing.T) {
	now := time.Now()
	valid := HistoryRetentionEvidence{State: RetentionObserved, CheckedAt: now,
		Bounds: graph.HistoryBounds{ClusterID: "prod", From: now.Add(-time.Hour), To: now, FirstEventAt: now.Add(-time.Minute), LastEventAt: now, LatestMarkerAt: now}}
	if !valid.ValidFor("prod", time.Hour, now) {
		t.Fatal("valid metadata evidence rejected")
	}
	for name, mutate := range map[string]func(*HistoryRetentionEvidence){
		"scope":         func(e *HistoryRetentionEvidence) { e.Bounds.ClusterID = "hidden" },
		"window":        func(e *HistoryRetentionEvidence) { e.Bounds.From = e.Bounds.From.Add(-time.Second) },
		"future query":  func(e *HistoryRetentionEvidence) { e.Bounds.To = e.Bounds.To.Add(time.Second) },
		"future check":  func(e *HistoryRetentionEvidence) { e.CheckedAt = e.CheckedAt.Add(time.Second) },
		"missing check": func(e *HistoryRetentionEvidence) { e.CheckedAt = time.Time{} },
		"fresh check for ancient window": func(e *HistoryRetentionEvidence) {
			e.Bounds.From, e.Bounds.To = e.Bounds.From.Add(-time.Hour), e.Bounds.To.Add(-time.Hour)
			e.Bounds.FirstEventAt, e.Bounds.LastEventAt, e.Bounds.LatestMarkerAt = time.Time{}, time.Time{}, time.Time{}
		},
		"false stale":                 func(e *HistoryRetentionEvidence) { e.Stale = true },
		"inverted events":             func(e *HistoryRetentionEvidence) { e.Bounds.FirstEventAt = now.Add(time.Second) },
		"one event bound missing":     func(e *HistoryRetentionEvidence) { e.Bounds.LastEventAt = time.Time{} },
		"marker before window":        func(e *HistoryRetentionEvidence) { e.Bounds.LatestMarkerAt = e.Bounds.From.Add(-time.Second) },
		"raw state":                   func(e *HistoryRetentionEvidence) { e.State = "synthetic-private-error" },
		"failed with success records": func(e *HistoryRetentionEvidence) { e.State = RetentionFailed },
		"unqueried with data":         func(e *HistoryRetentionEvidence) { e.State = RetentionNotQueried },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if bad.ValidFor("prod", time.Hour, now) {
				t.Fatal("invalid retained evidence accepted")
			}
		})
	}
	valid.Bounds.FirstEventAt, valid.Bounds.LastEventAt, valid.Bounds.LatestMarkerAt = time.Time{}, time.Time{}, time.Time{}
	if !valid.ValidFor("prod", time.Hour, now) {
		t.Fatal("empty retained window is a valid observation")
	}
	valid.State = RetentionFailed
	if !valid.ValidFor("prod", time.Hour, now) {
		t.Fatal("failed check without stale success records rejected")
	}
	valid.Stale = true
	if !valid.ValidFor("prod", time.Hour, now.Add(HistoryRetentionMaxAge)) {
		t.Fatal("exact expiry boundary rejected")
	}
}
