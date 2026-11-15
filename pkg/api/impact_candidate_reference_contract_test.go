// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"github.com/lithastra/kubeatlas/pkg/version"
)

func candidateReferenceFixture(t *testing.T) (*memory.Store, graph.Resource, impactCandidateCase) {
	t.Helper()
	ctx := t.Context()
	store := memory.New()
	pod := graph.Resource{ClusterID: "prod", Namespace: "demo", Kind: "Pod", Name: "reader", UID: "pod-uid", ResourceVersion: "17", GroupVersion: "v1"}
	raw := `{
		"kind":"Pod","apiVersion":"v1",
		"metadata":{"name":"reader","namespace":"demo","uid":"pod-uid","resourceVersion":"17"},
		"spec":{
			"containers":[{
				"envFrom":[{"configMapRef":{"name":"settings","optional":true}}],
				"env":[{"valueFrom":{"configMapKeyRef":{"name":"settings","key":"ignored"}}}],
				"volumeMounts":[{"name":"secret","subPath":"ignored"},{"name":"claim"}]
			}],
			"volumes":[
				{"name":"secret","secret":{"secretName":"credentials"}},
				{"name":"claim","persistentVolumeClaim":{"claimName":"data"}}
			]
		}
	}`
	if err := json.Unmarshal([]byte(raw), &pod.Raw); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertResource(ctx, pod); err != nil {
		t.Fatal(err)
	}

	paths := make(map[string][]analysis.ImpactStep)
	refs := []analysis.ImpactReferenceEvidence{}
	for _, target := range []struct {
		resource graph.Resource
		kind     graph.EdgeType
		uses     []analysis.ImpactReferenceUse
	}{
		{
			resource: graph.Resource{ClusterID: "prod", Namespace: "demo", Kind: "ConfigMap", Name: "settings", GroupVersion: "v1"},
			kind:     graph.EdgeTypeUsesConfigMap,
			uses: []analysis.ImpactReferenceUse{
				{FieldPath: "/spec/containers/0/envFrom/0/configMapRef", Mode: "environment", Optional: "optional", Refresh: "container_restart_required"},
				{FieldPath: "/spec/containers/0/env/0/valueFrom/configMapKeyRef", Mode: "environment", Optional: "required", Refresh: "container_restart_required"},
			},
		},
		{
			resource: graph.SecretReferenceResource("demo", "credentials", "prod"),
			kind:     graph.EdgeTypeUsesSecret,
			uses: []analysis.ImpactReferenceUse{{
				FieldPath: "/spec/volumes/0/secret", MountFieldPath: "/spec/containers/0/volumeMounts/0",
				Mode: "volume_subpath", Optional: "required", Refresh: "no_automatic_updates",
			}},
		},
		{
			resource: graph.Resource{ClusterID: "prod", Namespace: "demo", Kind: "PersistentVolumeClaim", Name: "data", GroupVersion: "v1"},
			kind:     graph.EdgeTypeMountsVolume,
			uses: []analysis.ImpactReferenceUse{{
				FieldPath: "/spec/volumes/1/persistentVolumeClaim", MountFieldPath: "/spec/containers/0/volumeMounts/1",
				Mode: "volume_mount", Optional: "not_applicable", Refresh: "storage_dependent",
			}},
		},
	} {
		if err := store.UpsertResource(ctx, target.resource); err != nil {
			t.Fatal(err)
		}
		from, to := pod.ID(), target.resource.ID()
		if err := store.UpsertEdge(ctx, graph.Edge{From: from, To: to, Type: target.kind}); err != nil {
			t.Fatal(err)
		}
		paths[to] = []analysis.ImpactStep{{From: from, To: to, Type: target.kind, TraversalFrom: from, TraversalTo: to}}
		refs = append(refs, analysis.ImpactReferenceEvidence{
			From: from, To: to, Type: target.kind, SourceUID: string(pod.UID), SourceResourceVersion: pod.ResourceVersion,
			Status: "version_bound", Uses: target.uses,
		})
	}
	c := impactCandidateCase{
		Name: "workload-references", Status: http.StatusOK,
		Query: impact.Query{Namespace: pod.Namespace, Kind: pod.Kind, Name: pod.Name, ClusterID: pod.ClusterID,
			ExpectedUID: string(pod.UID), Relation: analysis.ImpactDependencies, MaxDepth: 5, Limit: 200},
		Paths: paths, ReferenceEvidence: &refs,
	}
	return store, pod, c
}

func TestImpactCandidateChecksReferenceEvidence(t *testing.T) {
	store, pod, c := candidateReferenceFixture(t)
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
	check := func(c impactCandidateCase) error {
		return checkImpactCandidateCase(t.Context(), srv.URL, "fixture", version.Version, "tier1", c)
	}
	if err := check(c); err != nil {
		t.Fatalf("rejected exact ConfigMap, Secret and PVC explanations: %v", err)
	}
	// Keep the stored paths and consumer receipt, but remove the source fields.
	pod.Raw = nil
	if err := store.UpsertResource(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	ordinaryOnly := c
	ordinaryOnly.ReferenceEvidence = nil
	if err := check(ordinaryOnly); err != nil {
		t.Fatalf("loss of source fields changed the ordinary paths: %v", err)
	}
	if err := check(c); err == nil || !strings.Contains(err.Error(), "reference evidence") {
		t.Fatalf("probe missed lost source fields: %v", err)
	}
	unknown := slices.Clone(*c.ReferenceEvidence)
	for i := range unknown {
		unknown[i].Status, unknown[i].Reason = "unknown", "source_fields_unavailable"
		unknown[i].Uses = []analysis.ImpactReferenceUse{}
	}
	c.ReferenceEvidence = &unknown
	if !validCandidateReferenceEvidence(c.Paths, unknown) {
		t.Fatal("rejected a correctly qualified unknown explanation")
	}
	if err := check(c); err != nil {
		t.Fatalf("rejected correctly qualified unknown evidence: %v", err)
	}
	unknown[0].Reason = "source_identity_or_version_mismatch"
	if err := check(c); err == nil || !strings.Contains(err.Error(), "reference evidence") {
		t.Fatalf("probe accepted a different unknown reason: %v", err)
	}
}

func TestImpactCandidateRejectsReferenceEvidenceDrift(t *testing.T) {
	for _, scenario := range []string{"order-only", "missing", "duplicate", "source-version", "source-uid", "field", "mount-field", "optional", "refresh", "mode"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, c := candidateReferenceFixture(t)
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
				refs := capture.Analysis.ReferenceEvidence
				if len(refs) != 3 || len(refs[0].Uses) == 0 {
					t.Error("fixture reference evidence is incomplete")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				ref := &refs[0]
				switch scenario {
				case "order-only":
					slices.Reverse(refs)
				case "missing":
					capture.Analysis.ReferenceEvidence = refs[1:]
				case "duplicate":
					refs[0] = refs[1]
				case "source-version":
					ref.SourceResourceVersion = "18"
				case "source-uid":
					ref.SourceUID = "different-instance"
				case "field":
					ref.Uses[0].FieldPath = "/spec/containers/1/envFrom/0/configMapRef"
				case "mount-field":
					ref.Uses[0].MountFieldPath = "/spec/containers/0/volumeMounts/9"
				case "optional":
					ref.Uses[0].Optional = "required"
				case "refresh":
					ref.Uses[0].Refresh = "eventual_projection"
				case "mode":
					ref.Uses[0].Mode = "volume_mount"
				}
				writeJSON(w, http.StatusOK, capture)
			}))
			defer srv.Close()

			err := checkImpactCandidateCase(t.Context(), srv.URL, "fixture", version.Version, "tier1", c)
			if scenario == "order-only" {
				if err != nil {
					t.Fatalf("record ordering changed the evidence verdict: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "reference evidence") {
				t.Fatalf("probe accepted altered reference evidence: %v", err)
			}
		})
	}
}

func TestImpactCandidateRejectsInvalidReferenceEvidenceOracle(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "foreign-edge", "missing-version", "missing-field", "unknown-with-use", "unknown-without-reason", "denial-with-oracle"} {
		t.Run(scenario, func(t *testing.T) {
			_, _, c := candidateReferenceFixture(t)
			refs := *c.ReferenceEvidence
			switch scenario {
			case "missing":
				refs = refs[1:]
			case "duplicate":
				refs[0] = refs[1]
			case "foreign-edge":
				refs[0].To = "hidden:demo/ConfigMap/settings"
			case "missing-version":
				refs[0].SourceResourceVersion = ""
			case "missing-field":
				refs[0].Uses[0].FieldPath = ""
			case "unknown-with-use":
				refs[0].Status, refs[0].Reason = "unknown", "source_fields_unavailable"
			case "unknown-without-reason":
				refs[0].Status, refs[0].Uses = "unknown", nil
			case "denial-with-oracle":
				c.Status, c.Paths = http.StatusForbidden, nil
			}
			c.ReferenceEvidence = &refs
			plan := impactCandidatePlan{Commit: candidateTestCommit, Version: "1.7.0", Tier: "tier1", Cases: []impactCandidateCase{c}}
			if err := plan.validate(); err == nil {
				t.Fatal("accepted invalid reference evidence expectations")
			}
		})
	}
}
