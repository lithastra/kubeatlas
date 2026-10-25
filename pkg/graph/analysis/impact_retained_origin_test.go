// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestRetiredDynamicRowsRemainVisibleAndQualified(t *testing.T) {
	for _, state := range []string{"registered", "retired", "replaced", "unknown", "mid-read-retirement"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			tracker := operations.NewCoverageTracker(time.Minute)
			source, _ := tracker.BeginSource("", operations.CoverageSourceCRD, time.Now())
			r := storetest.ResourceOriginFixture("")
			token, _ := source.BeginType(r.Origin.GVR(), time.Now())
			if !token.BindOrigin(r.Origin) {
				t.Fatal("cannot bind fixture")
			}
			r = token.StampOrigin(r)
			s := memory.New()
			if err := s.UpsertResource(ctx, r); err != nil {
				t.Fatal(err)
			}
			if state == "retired" || state == "replaced" {
				token.Stop(time.Now())
			}
			if state == "replaced" {
				fresh, _ := source.BeginType(r.Origin.GVR(), time.Now())
				if !fresh.BindOrigin(r.Origin) {
					t.Fatal("replacement binding failed")
				}
			}
			if state == "unknown" {
				tracker = operations.NewCoverageTracker(time.Minute)
			}
			var result *analysis.ImpactResult
			var err error
			if state == "mid-read-retirement" {
				result = observedImpact(t, &graph.Graph{Resources: []graph.Resource{r}}, tracker, func() { token.Stop(time.Now()) })
			} else {
				result, err = analysis.AnalyzeObservedImpact(ctx, s, r.ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, tracker)
			}
			if err != nil {
				t.Fatal(err)
			}
			reason := "retained_source_" + state
			if state == "mid-read-retirement" {
				reason = "retained_source_retired"
			}
			if state != "registered" && !slices.Contains(result.Observation.Ordinary.Reasons, reason) {
				t.Fatal("retained data not qualified", result.Observation.Ordinary.Reasons)
			}
			if state == "registered" && slices.Contains(result.Observation.Ordinary.Reasons, "retained_source_unknown") {
				t.Fatal("known origin misclassified")
			}
			if state == "mid-read-retirement" && !slices.Contains(result.Observation.Ordinary.Reasons, "retained_source_window_unstable") {
				t.Fatal("retirement during read hidden")
			}
			if _, err := s.GetResource(ctx, r.ID()); err != nil {
				t.Fatal("qualification deleted retained data")
			}
			events, _ := s.ListEvents(ctx, "", time.Time{}, time.Now().Add(time.Minute))
			if len(events) != 0 {
				t.Fatal("qualification fabricated delete history")
			}
			body, err := json.Marshal(result)
			if err != nil || strings.Contains(string(body), r.Origin.OwnerUID) || strings.Contains(string(body), r.Origin.Registration) {
				t.Fatal("private origin escaped analysis", string(body), err)
			}
		})
	}
}
