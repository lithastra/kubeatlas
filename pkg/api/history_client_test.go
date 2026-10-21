// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"encoding/json"
	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/historyclient"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHistoryClientConsumesActualScopedAPI(t *testing.T) {
	store := memory.New()
	now := time.Now()
	for _, cluster := range []string{"", "prod", "hidden"} {
		if err := store.AppendSnapshotMeta(context.Background(), graph.SnapshotMeta{ClusterID: cluster, Timestamp: now, Trigger: graph.SnapshotTriggerManual}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendEvent(context.Background(), graph.ResourceEvent{ClusterID: cluster, Namespace: "demo", Kind: "Pod", Name: cluster + "-only", UID: "same-uid", Timestamp: now.Add(-time.Minute), EventType: graph.EventTypeUpdate}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(impactTestMux(New("", store, aggregator.NewRegistry(), WithSnapshots(time.Hour), WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}), WithClusterRBAC(historyRBAC()))))
	defer server.Close()
	list, err := historyclient.Fetch(context.Background(), server.URL, "prod-token", historyclient.Query{ClusterID: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	var got historyclient.ListResponse
	if err := json.Unmarshal(list, &got); err != nil || got.Count != 1 || got.Snapshots[0].ClusterID != "prod" {
		t.Fatal("real API list scope mismatch")
	}
	diff, err := historyclient.Fetch(context.Background(), server.URL, "prod-token", historyclient.Query{Diff: true, ClusterID: "prod", From: "1h", To: "now", Namespace: "demo"})
	if err != nil || !strings.Contains(string(diff), "prod-only") || strings.Contains(string(diff), "hidden-only") {
		t.Fatalf("real API diff: %v %s", err, diff)
	}
	if body, err := historyclient.Fetch(context.Background(), server.URL, "prod-token", historyclient.Query{ClusterID: "hidden"}); err == nil || body != nil {
		t.Fatal("client read a forbidden member")
	}
}

func TestTriggerClientConsumesScopedInternalAPIWithoutFallback(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	for _, cluster := range []string{"", "prod", "hidden"} {
		if err := store.UpsertResource(ctx, graph.Resource{ClusterID: cluster, Kind: "Pod", Namespace: "demo", Name: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	s := New("", store, aggregator.NewRegistry(), WithSnapshots(time.Hour), WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}), WithClusterRBAC(historyRBAC()))
	server := httptest.NewServer(impactTestMux(s))
	defer server.Close()
	body, err := historyclient.Trigger(ctx, server.URL, "prod-token", historyclient.TriggerQuery{ClusterID: "prod", Trigger: graph.SnapshotTriggerManual})
	var receipt historyclient.TriggerReceipt
	if err != nil || json.Unmarshal(body, &receipt) != nil || receipt.ClusterID != "prod" || receipt.ResourceCount != 1 {
		t.Fatalf("scoped mutation receipt: %s %v", body, err)
	}
	for _, scope := range []string{"hidden", ""} {
		if body, err := historyclient.Trigger(ctx, server.URL, "prod-token", historyclient.TriggerQuery{ClusterID: scope, Trigger: graph.SnapshotTriggerManual}); body != nil || err == nil {
			t.Fatal("unauthorized/unscoped trigger accepted")
		}
	}
	markers, err := store.ListSnapshotMeta(ctx)
	if err != nil || len(markers) != 1 || markers[0].ClusterID != "prod" {
		t.Fatal("client wrote outside selected member or retried", markers, err)
	}
}
