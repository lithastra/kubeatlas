// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"github.com/lithastra/kubeatlas/pkg/version"
)

func candidateAuthorizationFixture(t *testing.T, bindingKind string) (*memory.Store, impactCandidateCase, graph.Edge) {
	t.Helper()
	ctx := t.Context()
	store := memory.New()
	role := graph.Resource{ClusterID: "prod", Namespace: "grant", Kind: "Role", Name: "reader", UID: "role-uid", GroupVersion: "rbac.authorization.k8s.io/v1"}
	binding := graph.Resource{ClusterID: "prod", Namespace: "grant", Kind: bindingKind, Name: "reader", GroupVersion: role.GroupVersion}
	scope := "namespace"
	if bindingKind == "ClusterRoleBinding" {
		role.Kind, role.Namespace, binding.Namespace, scope = "ClusterRole", "", "", "cluster"
	}
	account := graph.Resource{ClusterID: "prod", Namespace: "app", Kind: "ServiceAccount", Name: "worker", GroupVersion: "v1"}
	pod := graph.Resource{ClusterID: "prod", Namespace: "app", Kind: "Pod", Name: "worker", GroupVersion: "v1"}
	for _, resource := range []graph.Resource{role, binding, account, pod} {
		if err := store.UpsertResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
	}
	roleEdge := graph.Edge{From: binding.ID(), To: role.ID(), Type: graph.EdgeTypeBindsRole}
	for _, edge := range []graph.Edge{
		roleEdge,
		{From: binding.ID(), To: account.ID(), Type: graph.EdgeTypeBindsSubject},
		{From: pod.ID(), To: account.ID(), Type: graph.EdgeTypeUsesServiceAccount},
	} {
		if err := store.UpsertEdge(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
	toBinding := analysis.ImpactAuthorizationStep{
		ImpactStep:       candidateDependentStep(binding.ID(), role.ID(), graph.EdgeTypeBindsRole),
		BindingScope:     scope,
		BindingNamespace: binding.Namespace,
	}
	toAccount := analysis.ImpactAuthorizationStep{
		ImpactStep: analysis.ImpactStep{From: binding.ID(), To: account.ID(), Type: graph.EdgeTypeBindsSubject,
			TraversalFrom: binding.ID(), TraversalTo: account.ID()},
		BindingScope:     scope,
		BindingNamespace: binding.Namespace,
	}
	toPod := analysis.ImpactAuthorizationStep{ImpactStep: candidateDependentStep(pod.ID(), account.ID(), graph.EdgeTypeUsesServiceAccount)}
	applicable := true
	c := impactCandidateCase{
		Name: "role-associations", Status: http.StatusOK,
		Query: impact.Query{Namespace: role.Namespace, Kind: role.Kind, Name: role.Name, ClusterID: "prod",
			ExpectedUID: string(role.UID), Relation: analysis.ImpactDependents, MaxDepth: 5, Limit: 200},
		Paths: map[string][]analysis.ImpactStep{},
		Authorization: &impactCandidateAuthorization{Applicable: &applicable, Paths: map[string][]analysis.ImpactAuthorizationStep{
			binding.ID(): {toBinding}, account.ID(): {toBinding, toAccount}, pod.ID(): {toBinding, toAccount, toPod},
		}},
	}
	return store, c, roleEdge
}

func TestImpactCandidateChecksIndependentAuthorization(t *testing.T) {
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			store, c, roleEdge := candidateAuthorizationFixture(t, kind)
			plan := impactCandidatePlan{Commit: candidateTestCommit, Version: "1.7.0", Tier: "tier1", Cases: []impactCandidateCase{c}}
			data, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := readImpactCandidatePlan(bytes.NewReader(data), candidateTestSHA(data))
			if err != nil {
				t.Fatal(err)
			}
			c = decoded.Cases[0]

			srv := httptest.NewServer(candidateFixtureHandler(store))
			defer srv.Close()
			if err := checkImpactCandidateCase(ctx, srv.URL, "fixture", version.Version, "tier1", c); err != nil {
				t.Fatalf("rejected exact authorization paths: %v", err)
			}
			depthLimited := c
			depthLimited.Query.MaxDepth = 1
			depthLimited.Authorization = &impactCandidateAuthorization{
				Applicable: c.Authorization.Applicable, Truncated: true,
				Paths: map[string][]analysis.ImpactAuthorizationStep{roleEdge.From: c.Authorization.Paths[roleEdge.From]},
			}
			if err := checkImpactCandidateCase(ctx, srv.URL, "fixture", version.Version, "tier1", depthLimited); err != nil {
				t.Fatalf("authorization depth limit changed the ordinary result: %v", err)
			}
			if err := store.DeleteEdge(ctx, roleEdge.From, roleEdge.To, roleEdge.Type); err != nil {
				t.Fatal(err)
			}
			ordinaryOnly := c
			ordinaryOnly.Authorization = nil
			if err := checkImpactCandidateCase(ctx, srv.URL, "fixture", version.Version, "tier1", ordinaryOnly); err != nil {
				t.Fatalf("authorization loss unexpectedly changed the ordinary result: %v", err)
			}
			if err := checkImpactCandidateCase(ctx, srv.URL, "fixture", version.Version, "tier1", c); err == nil || !strings.Contains(err.Error(), "authorization evidence") {
				t.Fatalf("probe missed the lost authorization paths: %v", err)
			}
			c.Authorization.Paths = map[string][]analysis.ImpactAuthorizationStep{}
			if err := checkImpactCandidateCase(ctx, srv.URL, "fixture", version.Version, "tier1", c); err != nil {
				t.Fatalf("probe rejected an applicable but empty facet: %v", err)
			}
		})
	}
}

func TestImpactCandidateAuthorizationRejectsChangedScopeAndClaims(t *testing.T) {
	for _, scenario := range []string{"binding-namespace", "binding-scope", "applicability", "truncation", "permission-claim", "version-evidence"} {
		t.Run(scenario, func(t *testing.T) {
			store, c, edge := candidateAuthorizationFixture(t, "RoleBinding")
			handler := candidateFixtureHandler(store)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				var capture ImpactResponse
				if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &capture) != nil {
					t.Error("fixture query failed")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				a := &capture.Analysis.Authorization
				switch scenario {
				case "binding-namespace", "binding-scope":
					for i := range a.Resources {
						if a.Resources[i].Resource.ID != edge.From {
							continue
						}
						if scenario == "binding-namespace" {
							a.Resources[i].Path[0].BindingNamespace = "app"
						} else {
							a.Resources[i].Path[0].BindingScope = "cluster"
						}
					}
				case "applicability":
					a.Applicable = false
				case "truncation":
					a.Truncated = true
				case "permission-claim":
					a.EffectivePermissions = "evaluated"
				case "version-evidence":
					a.SourceVersionEvidence = "version_bound"
				}
				writeJSON(w, http.StatusOK, capture)
			}))
			defer srv.Close()
			if err := checkImpactCandidateCase(t.Context(), srv.URL, "fixture", version.Version, "tier1", c); err == nil || !strings.Contains(err.Error(), "authorization evidence") {
				t.Fatalf("probe accepted altered authorization evidence: %v", err)
			}
		})
	}
}

func TestImpactCandidateRejectsInvalidAuthorizationOracle(t *testing.T) {
	for _, scenario := range []string{"missing-applicability", "missing-paths", "foreign-target", "wrong-binding-namespace", "broken-traversal", "denial-with-oracle"} {
		t.Run(scenario, func(t *testing.T) {
			_, c, edge := candidateAuthorizationFixture(t, "RoleBinding")
			switch scenario {
			case "missing-applicability":
				c.Authorization.Applicable = nil
			case "missing-paths":
				c.Authorization.Paths = nil
			case "foreign-target":
				c.Authorization.Paths["hidden:grant/RoleBinding/reader"] = c.Authorization.Paths[edge.From]
			case "wrong-binding-namespace":
				c.Authorization.Paths[edge.From][0].BindingNamespace = "app"
			case "broken-traversal":
				c.Authorization.Paths[edge.From][0].TraversalFrom = edge.From
			case "denial-with-oracle":
				c.Status, c.Paths = http.StatusForbidden, nil
			}
			plan := impactCandidatePlan{Commit: candidateTestCommit, Version: "1.7.0", Tier: "tier1", Cases: []impactCandidateCase{c}}
			if err := plan.validate(); err == nil {
				t.Fatal("accepted invalid authorization expectations")
			}
		})
	}
}
