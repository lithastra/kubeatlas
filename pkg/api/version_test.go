// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestAPIVersions_BothPathsServeIdenticalV1Alpha1Shape(t *testing.T) {
	// The v1alpha1 endpoint and the v1 endpoint return the same
	// resource + edges blob. v1 layers extra fields on top; the
	// shared subset must be byte-identical so consumers that drift
	// from one to the other never see a regression on existing
	// fields.
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{
			Kind: "ConfigMap", Namespace: "demo", Name: "cm",
		})
	})
	defer stop()

	var alpha api.ResourceDetailResponse
	getJSON(t, base+"/api/v1alpha1/resources/demo/ConfigMap/cm", &alpha)

	var v1 api.ResourceDetailResponseV1
	getJSON(t, base+"/api/v1/resources/demo/ConfigMap/cm", &v1)

	if alpha.Resource.Name != "cm" || v1.Resource.Name != "cm" {
		t.Errorf("resource.name mismatch: alpha=%q v1=%q", alpha.Resource.Name, v1.Resource.Name)
	}
	if alpha.Resource.ID() != v1.Resource.ID() {
		t.Errorf("resource id mismatch: alpha=%q v1=%q", alpha.Resource.ID(), v1.Resource.ID())
	}
}

func TestAPIVersions_V1AddsEnrichmentFields(t *testing.T) {
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		// A ReplicaSet with no owner — DetectOrphans flags it.
		_ = s.UpsertResource(ctx, graph.Resource{
			Kind: "ReplicaSet", Namespace: "demo", Name: "ghost",
		})
	})
	defer stop()

	// The v1alpha1 body must NOT carry isOrphan / inCycle /
	// blastRadiusCount, even as null — they were added in v1.
	_, alphaBody := getJSON(t, base+"/api/v1alpha1/resources/demo/ReplicaSet/ghost", nil)
	for _, key := range []string{"isOrphan", "inCycle", "blastRadiusCount"} {
		if strings.Contains(string(alphaBody), key) {
			t.Errorf("v1alpha1 body unexpectedly carries %q (frozen surface):\n%s", key, alphaBody)
		}
	}

	var v1 api.ResourceDetailResponseV1
	getJSON(t, base+"/api/v1/resources/demo/ReplicaSet/ghost", &v1)
	if !v1.IsOrphan {
		t.Errorf("v1: ghost should be flagged as orphan, got %+v", v1)
	}
}

func TestAPIVersions_OpenAPIPathsScopedPerVersion(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()

	// /api/v1alpha1/openapi.json: paths under v1alpha1 only.
	_, alphaBody := getJSON(t, base+"/api/v1alpha1/openapi.json", nil)
	var alphaSpec map[string]any
	if err := json.Unmarshal(alphaBody, &alphaSpec); err != nil {
		t.Fatalf("alpha unmarshal: %v", err)
	}
	for k := range alphaSpec["paths"].(map[string]any) {
		if strings.HasPrefix(k, "/api/v1/") {
			t.Errorf("v1alpha1 spec leaked v1 path: %q", k)
		}
	}
	if alphaSpec["info"].(map[string]any)["version"] != "v1alpha1" {
		t.Errorf("alpha info.version = %v, want v1alpha1", alphaSpec["info"].(map[string]any)["version"])
	}

	// /api/v1/openapi.json: paths remapped to v1.
	_, v1Body := getJSON(t, base+"/api/v1/openapi.json", nil)
	var v1Spec map[string]any
	if err := json.Unmarshal(v1Body, &v1Spec); err != nil {
		t.Fatalf("v1 unmarshal: %v", err)
	}
	saw := 0
	for k := range v1Spec["paths"].(map[string]any) {
		if strings.HasPrefix(k, "/api/v1alpha1/") {
			t.Errorf("v1 spec leaked v1alpha1 path: %q", k)
		}
		if strings.HasPrefix(k, "/api/v1/") {
			saw++
		}
	}
	if saw == 0 {
		t.Error("v1 spec contained zero /api/v1/ paths")
	}
	if v1Spec["info"].(map[string]any)["version"] != "v1" {
		t.Errorf("v1 info.version = %v, want v1", v1Spec["info"].(map[string]any)["version"])
	}

	// Components: v1 has the GA superset; v1alpha1 doesn't.
	v1Schemas := v1Spec["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := v1Schemas["ResourceDetailResponseV1"]; !ok {
		t.Error("v1 spec components missing ResourceDetailResponseV1")
	}
	alphaSchemas := alphaSpec["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := alphaSchemas["ResourceDetailResponseV1"]; ok {
		t.Error("v1alpha1 spec components should NOT carry the v1-only schema")
	}
}
