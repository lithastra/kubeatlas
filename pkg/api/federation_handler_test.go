// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// stubLister returns a fixed list of clusters and satisfies
// api.ClusterLister without dragging the real multicluster.Manager
// into pkg/api tests.
type stubLister struct{ clusters []string }

func (s stubLister) ListClusters() []string { return s.clusters }

func TestFederationClusters_SingleModeWhenNoListerWired(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()
	var body map[string]any
	resp, _ := getJSON(t, base+"/api/v1/federation/clusters", &body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["mode"] != "single" {
		t.Errorf("mode = %v, want single", body["mode"])
	}
}

func TestFederationClusters_FederatedModeListsAttached(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod", "staging"}}),
	)
	defer stop()
	var body map[string]any
	resp, raw := getJSON(t, base+"/api/v1/federation/clusters", &body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, raw)
	}
	if body["mode"] != "federated" {
		t.Errorf("mode = %v, want federated", body["mode"])
	}
	clusters, _ := body["clusters"].([]any)
	if len(clusters) != 2 {
		t.Errorf("clusters = %v, want 2 entries", clusters)
	}
}

func TestFederationGraph_503WhenNoListerWired(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/federation/graph?cluster=prod", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 with no lister wired", resp.StatusCode)
	}
}

func TestFederationGraph_400WithoutClusterParam(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod"}}),
	)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/federation/graph", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing cluster)", resp.StatusCode)
	}
}

func TestFederationGraph_400WithUnknownCluster(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod"}}),
	)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/federation/graph?cluster=prod,staging", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (staging not attached)", resp.StatusCode)
	}
}

func TestFederationGraph_HappyPathMergesAttachedClusters(t *testing.T) {
	// Seed two clusters' worth of resources sharing (ns, kind, name).
	seed := func(s graph.GraphStore) {
		ctx := context.Background()
		prodAPI := graph.Resource{Kind: "Pod", Name: "api", Namespace: "ns1", ClusterID: "prod"}
		prodCfg := graph.Resource{Kind: "ConfigMap", Name: "cfg", Namespace: "ns1", ClusterID: "prod"}
		stagingAPI := graph.Resource{Kind: "Pod", Name: "api", Namespace: "ns1", ClusterID: "staging"}
		for _, r := range []graph.Resource{prodAPI, prodCfg, stagingAPI} {
			_ = s.UpsertResource(ctx, r)
		}
		_ = s.UpsertEdge(ctx, graph.Edge{From: prodAPI.ID(), To: prodCfg.ID(), Type: graph.EdgeTypeUsesConfigMap})
	}
	base, _, stop := seedAndServe(t, seed,
		api.WithClusterLister(stubLister{clusters: []string{"prod", "staging"}}),
	)
	defer stop()

	resp, body := getJSON(t, base+"/api/v1/federation/graph?cluster=prod&cluster=staging", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var view struct {
		Clusters []string                 `json:"clusters"`
		Nodes    []map[string]interface{} `json:"nodes"`
		Edges    []map[string]interface{} `json:"edges"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(view.Clusters) != 2 {
		t.Errorf("Clusters = %v, want 2", view.Clusters)
	}
	if len(view.Nodes) != 3 {
		t.Errorf("Nodes = %d, want 3 (prod: api+cfg, staging: api)", len(view.Nodes))
	}
	if len(view.Edges) != 1 {
		t.Errorf("Edges = %d, want 1 (prod api→cfg)", len(view.Edges))
	}
}

func TestFederationGraph_LevelClusterReturnsSummaries(t *testing.T) {
	seed := func(s graph.GraphStore) {
		ctx := context.Background()
		for _, r := range []graph.Resource{
			{Kind: "Pod", Name: "api", Namespace: "ns1", ClusterID: "prod"},
			{Kind: "ConfigMap", Name: "cfg", Namespace: "ns1", ClusterID: "prod"},
			{Kind: "Pod", Name: "api", Namespace: "ns1", ClusterID: "staging"},
		} {
			_ = s.UpsertResource(ctx, r)
		}
	}
	base, _, stop := seedAndServe(t, seed,
		api.WithClusterLister(stubLister{clusters: []string{"prod", "staging"}}),
	)
	defer stop()

	resp, body := getJSON(t, base+"/api/v1/federation/graph?cluster=prod,staging&level=cluster", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var view struct {
		Level    string                   `json:"level"`
		Clusters []string                 `json:"clusters"`
		Nodes    []map[string]interface{} `json:"nodes"`
		Edges    []map[string]interface{} `json:"edges"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if view.Level != "cluster" {
		t.Errorf("Level = %q, want 'cluster'", view.Level)
	}
	if len(view.Nodes) != 2 {
		t.Fatalf("Nodes = %d, want 2 (one per cluster)", len(view.Nodes))
	}
	if len(view.Edges) != 0 {
		t.Errorf("Edges = %d, want 0", len(view.Edges))
	}
	// Each summary node has type='cluster' and the counts populated.
	byID := map[string]map[string]interface{}{}
	for _, n := range view.Nodes {
		byID[n["id"].(string)] = n
	}
	if byID["prod"]["type"] != "cluster" {
		t.Errorf("prod type = %v, want 'cluster'", byID["prod"]["type"])
	}
	if byID["prod"]["resourceCount"].(float64) != 2 {
		t.Errorf("prod.resourceCount = %v, want 2", byID["prod"]["resourceCount"])
	}
	if byID["staging"]["resourceCount"].(float64) != 1 {
		t.Errorf("staging.resourceCount = %v, want 1", byID["staging"]["resourceCount"])
	}
}

func TestFederationGraph_400OnUnknownLevel(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod"}}),
	)
	defer stop()
	resp, _ := getJSON(t, base+"/api/v1/federation/graph?cluster=prod&level=bogus", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestFederationGraph_DefaultLevelIsResource(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod"}}),
	)
	defer stop()
	resp, body := getJSON(t, base+"/api/v1/federation/graph?cluster=prod", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var view struct {
		Level string `json:"level"`
	}
	_ = json.Unmarshal(body, &view)
	if view.Level != "resource" {
		t.Errorf("Level = %q, want 'resource' as the default", view.Level)
	}
}
