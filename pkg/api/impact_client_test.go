// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
)

// Actual API handler + store + engine + HTTP client + all report formats. This
// proves portable wire parity, not acceptance against an existing cluster.
func TestImpactClientActualAPICaptureParity(t *testing.T) {
	for _, cluster := range []string{"", "prod"} {
		for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependents, analysis.ImpactDependencies} {
			t.Run(cluster+"/"+string(relation), func(t *testing.T) {
				opts := []ServerOption{}
				token := ""
				if cluster != "" {
					token = "synthetic-prod-token"
					opts = append(opts, WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}),
						WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: token, Clusters: []string{"prod"}}})))
				}
				s := New("", seedImpactAPI(t), aggregator.NewRegistry(), opts...)
				var calls atomic.Int32
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					recorder := serveImpactTest(s, r)
					body := recorder.Body.Bytes()
					captured <- append([]byte(nil), body...)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(recorder.Code)
					w.Write(body)
				}))
				defer server.Close()
				r, err := impact.Fetch(context.Background(), server.URL, token, impact.Query{Namespace: "demo", Kind: "ConfigMap", Name: "settings",
					ClusterID: cluster, ExpectedUID: "root-instance", Relation: relation, MaxDepth: 5, Limit: 200})
				if err != nil {
					t.Fatal(err)
				}
				wire := <-captured
				var wireObject, jsonObject any
				if err := json.Unmarshal(wire, &wireObject); err != nil {
					t.Fatal(err)
				}
				jsonOutput, err := impact.Render(r, "json")
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(jsonOutput, &jsonObject); err != nil || !reflect.DeepEqual(wireObject, jsonObject) {
					t.Fatalf("JSON differs from actual API wire: %v", err)
				}
				page, err := impact.Render(r, "html")
				if err != nil {
					t.Fatal(err)
				}
				_, embedded, ok := strings.Cut(string(page), `<pre id="captured-json">`)
				embedded, _, _ = strings.Cut(embedded, "</pre>")
				if !ok || html.UnescapeString(embedded) != strings.TrimSpace(string(jsonOutput)) {
					t.Fatal("HTML and JSON do not share the identical capture")
				}
				text, err := impact.Render(r, "text")
				if err != nil {
					t.Fatal(err)
				}
				for _, output := range [][]byte{page, jsonOutput, text} {
					for _, forbidden := range []string{"synthetic-prod-token", "synthetic-payload-canary", "hidden:"} {
						if strings.Contains(string(output), forbidden) {
							t.Fatalf("export exposed %q", forbidden)
						}
					}
				}
				if calls.Load() != 1 {
					t.Fatal("report rendering made an extra HTTP request")
				}
				if relation == analysis.ImpactDependents && (r.Analysis.Counts.Total != 1 || r.Analysis.Resources[0].Resource.Namespace != "other") {
					t.Fatal("lost cross-namespace match")
				}
				if relation == analysis.ImpactDependencies && !strings.Contains(string(text), "No dependencies observed; analysis incomplete.") {
					t.Fatal("empty incomplete API result overstated")
				}
			})
		}
	}
}

func TestImpactClientAuthorizationAndTruncation(t *testing.T) {
	store := seedImpactAPI(t)
	resources := []graph.Resource{
		{Kind: "ClusterRole", Name: "system:reader", GroupVersion: "rbac.authorization.k8s.io/v1"},
		{Kind: "RoleBinding", Namespace: "target", Name: "reader", GroupVersion: "rbac.authorization.k8s.io/v1"},
		{Kind: "ServiceAccount", Namespace: "app", Name: "worker", GroupVersion: "v1"},
		{Kind: "Deployment", Namespace: "app", Name: "api", GroupVersion: "apps/v1"},
	}
	for _, resource := range resources {
		if err := store.UpsertResource(context.Background(), resource); err != nil {
			t.Fatal(err)
		}
	}
	for _, edge := range []graph.Edge{
		{From: resources[1].ID(), To: resources[0].ID(), Type: graph.EdgeTypeBindsRole},
		{From: resources[1].ID(), To: resources[2].ID(), Type: graph.EdgeTypeBindsSubject},
		{From: resources[3].ID(), To: resources[2].ID(), Type: graph.EdgeTypeUsesServiceAccount},
	} {
		if err := store.UpsertEdge(context.Background(), edge); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(impactTestMux(New("", store, aggregator.NewRegistry())))
	defer server.Close()
	for _, tc := range []struct {
		kind, namespace, name         string
		depth, limit, total, ordinary int
		truncated                     bool
	}{
		{"ClusterRole", "", "system:reader", 5, 200, 3, 0, false},
		{"ClusterRole", "", "system:reader", 1, 200, 1, 0, true},
		{"ClusterRole", "", "system:reader", 5, 1, 1, 0, true},
		{"ServiceAccount", "app", "worker", 5, 200, 3, 1, false},
	} {
		r, err := impact.Fetch(context.Background(), server.URL, "", impact.Query{Namespace: tc.namespace, Kind: tc.kind, Name: tc.name,
			Relation: analysis.ImpactDependents, MaxDepth: tc.depth, Limit: tc.limit})
		if err != nil {
			t.Fatal(err)
		}
		a := r.Analysis.Authorization
		if !a.Applicable || a.Counts.Total != tc.total || r.Analysis.Counts.Total != tc.ordinary || a.Truncated != tc.truncated || a.Counts.LowerBound != tc.truncated || a.EffectivePermissions != "not_evaluated" {
			t.Fatalf("authorization counts/truncation changed: %+v", a)
		}
		if a.Resources[0].Path[0].BindingNamespace == "" && tc.kind == "ClusterRole" {
			t.Fatal("lost binding namespace")
		}
		for _, format := range []string{"text", "json", "html"} {
			if _, err := impact.Render(r, format); err != nil {
				t.Fatalf("authorization rendering %s: %v", format, err)
			}
		}
	}
}
