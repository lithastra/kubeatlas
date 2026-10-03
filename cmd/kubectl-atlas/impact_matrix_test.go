// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/impact"
)

// These complete captures are independently checked against the real handler,
// store and engine in pkg/api. Web consumes the exact same fixture bytes.
func TestImpactCommandClientMatrix(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name, Namespace, Kind, RootName, UID, Cluster, Relation string
			Depth                                                   int
		}
	}
	read := func(path string, dst any) {
		t.Helper()
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, dst); e != nil {
			t.Fatal(e)
		}
	}
	read("../../test/fixtures/impact-analysis/client-matrix.json", &fixture)
	var captures map[string]json.RawMessage
	read("../../test/fixtures/impact-analysis/client-captures.json", &captures)
	for _, tc := range fixture.Cases {
		for _, format := range []string{"text", "json", "html"} {
			t.Run(tc.Name+"/"+format, func(t *testing.T) {
				t.Setenv("KUBEATLAS_TOKEN", "synthetic-command-token")
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					q := r.URL.Query()
					if r.URL.Path != "/api/v1/impact/"+tc.Namespace+"/"+tc.Kind+"/"+tc.RootName || q.Get("cluster") != tc.Cluster || q.Get("relation") != tc.Relation || q.Get("max_depth") != strconv.Itoa(tc.Depth) || q.Get("limit") != "200" || q.Get("expected_uid") != tc.UID || r.Header.Get("Authorization") != "Bearer synthetic-command-token" {
						t.Error("CLI changed the matrix query")
					}
					w.Header().Set("Content-Type", "application/json")
					w.Write(captures[tc.Name])
				}))
				defer server.Close()
				a, opened := newTestApp()
				closed := false
				a.resolve = func(context.Context, string, string, kubeFlags) (string, func(), bool, error) {
					return server.URL, func() { closed = true }, false, nil
				}
				args := []string{"impact", tc.Kind, tc.RootName, "-n", tc.Namespace, "--relation", tc.Relation, "--max-depth", strconv.Itoa(tc.Depth), "--limit", "200", "--expected-uid", tc.UID, "--output", format}
				if tc.Cluster != "" {
					args = append(args, "--cluster", tc.Cluster)
				}
				out, stderr, err := executeImpact(t, a, args...)
				if err != nil || stderr != "" || !closed || *opened != "" || calls.Load() != 1 {
					t.Fatalf("command failed/queried again: err=%v calls=%d", err, calls.Load())
				}
				if strings.Contains(out, "synthetic-command-token") || strings.Contains(out, "west:") {
					t.Fatal("private evidence leaked")
				}
				if format == "text" {
					if !strings.Contains(out, "sensitive cluster topology") || !strings.Contains(out, tc.Kind+"/"+tc.RootName) {
						t.Fatal("missing root/sharing qualification")
					}
					var response impact.Response
					if err := json.Unmarshal(captures[tc.Name], &response); err != nil {
						t.Fatal(err)
					}
					a := response.Analysis
					for _, required := range []string{
						fmt.Sprintf("Total: %d | Direct: %d | Indirect: %d", a.Counts.Total, a.Counts.Direct, a.Counts.Indirect),
						fmt.Sprintf("Lower bound: %t", a.Counts.LowerBound),
						fmt.Sprintf("Truncated: %t | Observation: %q", a.Truncated, a.ObservationCoverage),
						"Authorization associations (separate counts; not effective permissions or credential-use proof)",
					} {
						if !strings.Contains(out, required) {
							t.Fatalf("text omits %q", required)
						}
					}
					for _, match := range a.Resources {
						if !strings.Contains(out, fmt.Sprintf("depth=%d %q", match.Depth, match.Resource.ID)) {
							t.Fatal("text omits ordinary match/depth")
						}
						for _, step := range match.Path {
							if !strings.Contains(out, fmt.Sprintf("walk %q -> %q; stored %q -[%q]-> %q", step.TraversalFrom, step.TraversalTo, step.From, step.Type, step.To)) {
								t.Fatal("text changed traversal/stored direction")
							}
						}
					}
					for _, match := range a.Authorization.Resources {
						if !strings.Contains(out, strconv.Quote(match.Resource.ID)) {
							t.Fatal("text omits separate authorization match")
						}
					}
					if len(a.Resources) == 0 && !strings.Contains(out, "No "+tc.Relation+" observed; analysis incomplete.") {
						t.Fatal("text overclaims incomplete empty evidence")
					}
					for _, reason := range a.TruncationReasons {
						if !strings.Contains(out, strconv.Quote(reason)) {
							t.Fatal("text omits truncation reason")
						}
					}
					return
				}
				if format == "html" {
					_, out, _ = strings.Cut(out, `<pre id="captured-json">`)
					out, _, _ = strings.Cut(out, "</pre>")
					out = html.UnescapeString(out)
				}
				var got, want any
				if err = json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(captures[tc.Name], &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("command/report differs from shared API capture")
				}
			})
		}
	}
}
