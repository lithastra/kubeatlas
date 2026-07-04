// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
)

// getWithToken issues an authenticated GET (getJSON has no header hook).
func getWithToken(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// The F-206 tests drive the real multicluster.RBACScope through the
// federation handlers, so token hashing + lookup are exercised end-to-end.
var rbacRule = []multicluster.RBACRule{{Token: "tok-prod", Clusters: []string{"prod"}}}

func TestFederationRBAC_UnconfiguredIsAllVisible(t *testing.T) {
	// A scope with no rules is disabled: v1.4 behaviour, no token needed.
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod", "staging"}}),
		api.WithClusterRBAC(multicluster.NewRBACScope(nil)),
	)
	defer stop()

	resp, body := getWithToken(t, base+"/api/v1/federation/clusters", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Mode     string   `json:"mode"`
		Clusters []string `json:"clusters"`
	}
	_ = json.Unmarshal(body, &got)
	if len(got.Clusters) != 2 {
		t.Fatalf("clusters = %v, want both visible", got.Clusters)
	}
}

func TestFederationRBAC_MatchedTokenFiltersClusterList(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod", "staging"}}),
		api.WithClusterRBAC(multicluster.NewRBACScope(rbacRule)),
	)
	defer stop()

	resp, body := getWithToken(t, base+"/api/v1/federation/clusters", "tok-prod")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Clusters []string `json:"clusters"`
	}
	_ = json.Unmarshal(body, &got)
	if len(got.Clusters) != 1 || got.Clusters[0] != "prod" {
		t.Fatalf("clusters = %v, want [prod] only", got.Clusters)
	}
}

func TestFederationRBAC_MissingTokenIs401(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod"}}),
		api.WithClusterRBAC(multicluster.NewRBACScope(rbacRule)),
	)
	defer stop()

	resp, _ := getWithToken(t, base+"/api/v1/federation/clusters", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (not 200+empty)", resp.StatusCode)
	}
}

func TestFederationRBAC_UnknownTokenIs403(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod"}}),
		api.WithClusterRBAC(multicluster.NewRBACScope(rbacRule)),
	)
	defer stop()

	resp, _ := getWithToken(t, base+"/api/v1/federation/clusters", "bogus")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestFederationRBAC_GraphRejectsUnauthorisedCluster(t *testing.T) {
	base, _, stop := seedAndServe(t, nil,
		api.WithClusterLister(stubLister{clusters: []string{"prod", "staging"}}),
		api.WithClusterRBAC(multicluster.NewRBACScope(rbacRule)),
	)
	defer stop()

	// tok-prod may see prod → 200.
	resp, _ := getWithToken(t, base+"/api/v1/federation/graph?cluster=prod", "tok-prod")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorised cluster: status = %d, want 200", resp.StatusCode)
	}

	// tok-prod may NOT see staging → 403 (not silently dropped).
	resp, _ = getWithToken(t, base+"/api/v1/federation/graph?cluster=staging", "tok-prod")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorised cluster: status = %d, want 403", resp.StatusCode)
	}

	// No token → 401.
	resp, _ = getWithToken(t, base+"/api/v1/federation/graph?cluster=prod", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", resp.StatusCode)
	}
}
