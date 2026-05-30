// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"strings"
	"testing"
)

func TestHandleDiagnose_JSON(t *testing.T) {
	base, _, cleanup := seedAndServe(t, petClinicSeed)
	defer cleanup()

	var got struct {
		KubeAtlasVersion string `json:"kubeAtlasVersion"`
		ResourceCount    int    `json:"resourceCount"`
		Scope            struct {
			Namespace     string `json:"namespace"`
			AllNamespaces bool   `json:"allNamespaces"`
		} `json:"scope"`
		TopBlastRadius []struct {
			Affected int `json:"affected"`
		} `json:"topBlastRadius"`
	}
	resp, body := getJSON(t, base+"/api/v1/diagnose?namespace=petclinic", &got)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if got.Scope.Namespace != "petclinic" || got.Scope.AllNamespaces {
		t.Errorf("scope = %+v, want namespace=petclinic", got.Scope)
	}
	// petClinicSeed seeds four resources, all in petclinic.
	if got.ResourceCount != 4 {
		t.Errorf("resourceCount = %d, want 4", got.ResourceCount)
	}
	if got.KubeAtlasVersion == "" {
		t.Error("kubeAtlasVersion is empty")
	}
	// app-config has the deepest dependent chain (pod->rs->dep->cm), so
	// at least one resource must show a non-zero blast radius.
	if len(got.TopBlastRadius) == 0 {
		t.Error("topBlastRadius is empty, want at least one entry")
	}
}

func TestHandleDiagnose_DefaultFormatIsJSON(t *testing.T) {
	base, _, cleanup := seedAndServe(t, petClinicSeed)
	defer cleanup()

	resp, body := getJSON(t, base+"/api/v1/diagnose", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if len(body) == 0 || body[0] != '{' {
		t.Errorf("body is not a JSON object: %s", body)
	}
}

func TestHandleDiagnose_HTML(t *testing.T) {
	base, _, cleanup := seedAndServe(t, petClinicSeed)
	defer cleanup()

	resp, body := getJSON(t, base+"/api/v1/diagnose?namespace=petclinic&format=html", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	got := string(body)
	for _, want := range []string{"KubeAtlas Diagnostic Report", "petclinic"} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

func TestHandleDiagnose_InvalidFormat(t *testing.T) {
	base, _, cleanup := seedAndServe(t, petClinicSeed)
	defer cleanup()

	resp, _ := getJSON(t, base+"/api/v1/diagnose?format=bogus", nil)
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
