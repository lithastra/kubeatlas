// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestRetiredOriginQualificationSurvivesAPIClientAndReports(t *testing.T) {
	ctx := context.Background()
	tracker := operations.NewCoverageTracker(time.Minute)
	source, err := tracker.BeginSource("", operations.CoverageSourceCRD, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := storetest.ResourceOriginFixture("")
	token, err := source.BeginType(r.Origin.GVR(), time.Now())
	if err != nil || !token.BindOrigin(r.Origin) {
		t.Fatal("binding failed", err)
	}
	r = token.StampOrigin(r)
	store := memory.New()
	if err := store.UpsertResource(ctx, r); err != nil {
		t.Fatal(err)
	}
	token.Stop(time.Now())
	server := New("", store, aggregator.NewRegistry(), WithImpactEvidence(analysis.ImpactEvidenceProviders{Coverage: tracker}))
	mux := impactTestMux(server)
	var requests atomic.Int64
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { requests.Add(1); mux.ServeHTTP(w, req) }))
	defer host.Close()
	captured, err := impact.Fetch(ctx, host.URL, "", impact.Query{Namespace: r.Namespace, Kind: r.Kind, Name: r.Name, Relation: analysis.ImpactDependents, MaxDepth: 5, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json", "html"} {
		body, err := impact.Render(captured, format)
		if err != nil || !strings.Contains(string(body), "retained_source_retired") {
			t.Fatal("report lost retirement qualification", format, err)
		}
		if strings.Contains(string(body), r.Origin.OwnerUID) || strings.Contains(string(body), r.Origin.Registration) {
			t.Fatal("private origin escaped report", format)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("report recomputed or re-queried", requests.Load())
	}
	if _, err := store.GetResource(ctx, r.ID()); err != nil {
		t.Fatal("read/report dropped retained resource", err)
	}
}
