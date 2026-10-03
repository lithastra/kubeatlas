// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// Hand-authored graph/oracles and complete API captures are shared with Web and
// command tests. Only version and capture timestamps are normalized; paths,
// counts, identity, coverage, limits and all other evidence must match exactly.
func TestImpactClientMatrixWireContract(t *testing.T) {
	var fixture struct {
		Resources []graph.Resource
		Edges     []graph.Edge
		Cases     []struct {
			Name, Namespace, Kind, RootName, UID, Cluster, Relation string
			Depth, Direct                                           int
			Ordinary, Authorization                                 []string
			Truncated                                               bool
		}
	}
	load := func(path string, dst any) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, dst); err != nil {
			t.Fatal(err)
		}
	}
	load("../../test/fixtures/impact-analysis/client-matrix.json", &fixture)
	var captures map[string]json.RawMessage
	load("../../test/fixtures/impact-analysis/client-captures.json", &captures)
	if len(captures) != len(fixture.Cases) {
		t.Fatal("matrix/capture case count differs")
	}
	store := memory.New()
	ctx := context.Background()
	for _, cluster := range []string{"", "east", "west"} {
		prefix := ""
		if cluster != "" {
			prefix = cluster + ":"
		}
		for _, resource := range fixture.Resources {
			resource.ClusterID = cluster
			resource.Raw = map[string]any{"data": "synthetic-matrix-payload-canary"}
			if err := store.UpsertResource(ctx, resource); err != nil {
				t.Fatal(err)
			}
		}
		for _, edge := range fixture.Edges {
			edge.From, edge.To = prefix+edge.From, prefix+edge.To
			if err := store.UpsertEdge(ctx, edge); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var opts []ServerOption
			token := ""
			if tc.Cluster != "" {
				token = "synthetic-matrix-east"
				opts = []ServerOption{WithClusterLister(&impactAPIClusters{clusters: []string{"east", "west"}}),
					WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: token, Clusters: []string{"east"}}}))}
			}
			s := New("", store, aggregator.NewRegistry(), opts...)
			path := fmt.Sprintf("/api/v1/impact/%s/%s/%s?relation=%s&max_depth=%d&limit=200&expected_uid=%s", tc.Namespace, tc.Kind, tc.RootName, tc.Relation, tc.Depth, tc.UID)
			if tc.Cluster != "" {
				path += "&cluster=" + tc.Cluster
			}
			w := serveImpactTest(s, impactTestRequest(ctx, path, token))
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var got, want ImpactResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(captures[tc.Name], &want); err != nil {
				t.Fatal(err)
			}
			ids := func(matches []analysis.ImpactMatch) []string {
				out := make([]string, 0, len(matches))
				for _, match := range matches {
					out = append(out, match.Resource.ID)
				}
				slices.Sort(out)
				return out
			}
			a := got.Analysis
			authIDs := make([]string, 0, len(a.Authorization.Resources))
			for _, match := range a.Authorization.Resources {
				authIDs = append(authIDs, match.Resource.ID)
			}
			slices.Sort(authIDs)
			if !slices.Equal(ids(a.Resources), tc.Ordinary) || !slices.Equal(authIDs, tc.Authorization) || a.Counts.Total != len(tc.Ordinary) || a.Counts.Direct != tc.Direct || a.Counts.Indirect != len(tc.Ordinary)-tc.Direct || a.Authorization.Counts.Total != len(tc.Authorization) || a.Truncated != tc.Truncated {
				t.Fatalf("hand-authored oracle differs: ordinary=%v authorization=%v direct=%d truncated=%v", ids(a.Resources), authIDs, a.Counts.Direct, a.Truncated)
			}
			if a.Observation.Ordinary.State != "unknown" || a.Observation.Ordinary.TypeScopeClosed || a.Authorization.EffectivePermissions != "not_evaluated" {
				t.Fatal("synthetic evidence overclaimed")
			}
			fixed := time.Date(2026, 10, 1, 0, 0, 0, 123456789, time.UTC)
			got.GeneratedAt, got.KubeAtlasVersion = fixed, "synthetic-matrix"
			a.Availability.Evidence.CapturedAt = fixed
			for _, coverage := range []*analysis.ImpactCoverageFacet{&a.Observation.Ordinary, &a.Observation.Authorization} {
				coverage.Before.CapturedAt, coverage.After.CapturedAt = fixed, fixed
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Fatal("full capture drifted; review the API/CLI/Web contract together, do not blindly update the fixture")
			}
			for _, private := range []string{"synthetic-matrix-east", "synthetic-matrix-payload-canary", "west:"} {
				if strings.Contains(string(gotJSON), private) {
					t.Fatalf("capture exposed %q", private)
				}
			}
		})
	}
}
