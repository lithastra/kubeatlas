// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// snapshotTriggerFixture seeds 3 resources + 2 edges in one
// namespace so the trigger handler's resource/edge totals are
// deterministic.
func snapshotTriggerFixture(s graph.GraphStore) {
	ctx := context.Background()
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "api"}
	cm := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cfg"}
	svc := graph.Resource{Kind: "Service", Namespace: "demo", Name: "api-svc"}
	for _, r := range []graph.Resource{dep, cm, svc} {
		_ = s.UpsertResource(ctx, r)
	}
	_ = s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap})
	_ = s.UpsertEdge(ctx, graph.Edge{From: svc.ID(), To: dep.ID(), Type: graph.EdgeTypeRoutesTo})
}

// postJSON issues a POST and decodes the JSON body. Mirrors the
// getJSON helper in handlers_test.go; the trigger endpoint is the
// first POST route so there was no shared helper before.
func postJSON(t *testing.T, url string, into any) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if into != nil && resp.StatusCode == 200 {
		if err := json.Unmarshal(body, into); err != nil {
			t.Fatalf("decode %s: %v (body: %s)", url, err, body)
		}
	}
	return resp, body
}

func TestSnapshotTrigger_DefaultsToManual(t *testing.T) {
	base, _, stop := seedAndServe(t, snapshotTriggerFixture)
	defer stop()

	var resp api.SnapshotTriggerResponse
	r, body := postJSON(t, base+"/api/_internal/snapshot/trigger", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", r.StatusCode, body)
	}
	if resp.Trigger != "manual" {
		t.Errorf("trigger = %q, want manual (the default)", resp.Trigger)
	}
	if resp.ResourceCount != 3 {
		t.Errorf("resourceCount = %d, want 3", resp.ResourceCount)
	}
	if resp.EdgeCount != 2 {
		t.Errorf("edgeCount = %d, want 2", resp.EdgeCount)
	}
}

func TestSnapshotTrigger_PeriodicTrigger(t *testing.T) {
	base, _, stop := seedAndServe(t, snapshotTriggerFixture)
	defer stop()

	var resp api.SnapshotTriggerResponse
	r, _ := postJSON(t, base+"/api/_internal/snapshot/trigger?trigger=periodic", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if resp.Trigger != "periodic" {
		t.Errorf("trigger = %q, want periodic", resp.Trigger)
	}
}

func TestSnapshotTrigger_RejectsUnknownTrigger(t *testing.T) {
	base, _, stop := seedAndServe(t, snapshotTriggerFixture)
	defer stop()

	r, _ := postJSON(t, base+"/api/_internal/snapshot/trigger?trigger=bogus", nil)
	if r.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for an unknown trigger kind", r.StatusCode)
	}
}

func TestSnapshotTrigger_EmptyCluster(t *testing.T) {
	// No fixture — the marker still records, with zero counts.
	base, _, stop := seedAndServe(t, nil)
	defer stop()

	var resp api.SnapshotTriggerResponse
	r, _ := postJSON(t, base+"/api/_internal/snapshot/trigger", &resp)
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if resp.ResourceCount != 0 || resp.EdgeCount != 0 {
		t.Errorf("empty cluster: counts = %d/%d, want 0/0", resp.ResourceCount, resp.EdgeCount)
	}
}
