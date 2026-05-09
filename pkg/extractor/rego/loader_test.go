// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/version"
)

const (
	sampleDir         = "testdata/rules/sample"
	futureAPIDir      = "testdata/rules/future-api"
	futureKubeAtlasDir = "testdata/rules/future-kubeatlas"
)

// withKubeatlasVersion swaps version.Version for the duration of the
// test so the kubeatlas semver constraint actually evaluates instead
// of taking the dev-build skip path.
func withKubeatlasVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

// TestLoadRulePackFromDir_HappyPath: sample pack loads, registers
// into the engine under <pack>/<module>, and evaluates against a
// matching input to produce the documented edge.
func TestLoadRulePackFromDir_HappyPath(t *testing.T) {
	rp, err := LoadRulePackFromDir(sampleDir)
	if err != nil {
		t.Fatalf("LoadRulePackFromDir: %v", err)
	}
	if rp.Name != "sample" || rp.Version != "0.1.0" {
		t.Errorf("rp = %+v, want name=sample version=0.1.0", rp)
	}
	if len(rp.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(rp.Modules))
	}
	if rp.Modules[0].Match.Kind != "Foo" {
		t.Errorf("Match.Kind = %q, want Foo", rp.Modules[0].Match.Kind)
	}

	e := newSilentEngine(t)
	if err := rp.RegisterTo(context.Background(), e); err != nil {
		t.Fatalf("RegisterTo: %v", err)
	}
	loaded := e.Loaded()
	if len(loaded) != 1 {
		t.Fatalf("engine has %d modules, want 1", len(loaded))
	}
	if loaded[0].Name != "sample/derive" {
		t.Errorf("module key = %q, want sample/derive", loaded[0].Name)
	}

	rs, err := e.Evaluate(context.Background(), "sample/derive", map[string]any{
		"kind": "Foo",
		"metadata": map[string]any{
			"namespace": "demo",
			"name":      "x",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		t.Fatal("empty result set")
	}
	// derive contains a set; OPA marshals it as []any. Each entry
	// is a map shaped like {"type", "from", "to"}.
	edges, ok := rs[0].Expressions[0].Value.([]any)
	if !ok {
		t.Fatalf("expression value %T, want []any", rs[0].Expressions[0].Value)
	}
	if len(edges) != 1 {
		t.Fatalf("derived %d edges, want 1", len(edges))
	}
	edge := edges[0].(map[string]any)
	if edge["type"] != "DERIVED_TO" {
		t.Errorf("edge.type = %v, want DERIVED_TO", edge["type"])
	}
	to := edge["to"].(map[string]any)
	if to["kind"] != "Bar" || to["name"] != "x-target" {
		t.Errorf("edge.to = %+v, want {Bar, x-target}", to)
	}
}

// TestLoadRulePackFromDir_RegoAPIv2 rejects packs that ask for an
// unsupported rego_api with a typed sentinel callers can match.
func TestLoadRulePackFromDir_RegoAPIv2(t *testing.T) {
	_, err := LoadRulePackFromDir(futureAPIDir)
	if err == nil {
		t.Fatal("expected rego_api=v2 to be rejected, got nil")
	}
	if !errors.Is(err, ErrIncompatibleRegoAPI) {
		t.Errorf("err = %v, want ErrIncompatibleRegoAPI", err)
	}
	if !strings.Contains(err.Error(), "future-api") {
		t.Errorf("err %q should name the offending pack", err)
	}
}

// TestLoadRulePackFromDir_KubeAtlasConstraint: pin version.Version
// to a real semver so the constraint check fires, then assert the
// future-kubeatlas pack is rejected.
func TestLoadRulePackFromDir_KubeAtlasConstraint(t *testing.T) {
	withKubeatlasVersion(t, "1.0.0")
	_, err := LoadRulePackFromDir(futureKubeAtlasDir)
	if err == nil {
		t.Fatal("expected kubeatlas constraint to be rejected, got nil")
	}
	if !errors.Is(err, ErrIncompatibleKubeAtlas) {
		t.Errorf("err = %v, want ErrIncompatibleKubeAtlas", err)
	}
	if !strings.Contains(err.Error(), "99.0.0") {
		t.Errorf("err %q should mention the failing constraint", err)
	}
}

// TestLoadRulePackFromDir_DevVersionSkipsConstraint: when version.Version
// is "dev" / non-semver, the kubeatlas constraint is intentionally
// not enforced — contributors can iterate without bumping a local
// version every time a pack tightens its range.
func TestLoadRulePackFromDir_DevVersionSkipsConstraint(t *testing.T) {
	withKubeatlasVersion(t, "dev")
	rp, err := LoadRulePackFromDir(futureKubeAtlasDir)
	if err != nil {
		t.Fatalf("dev build: expected no constraint enforcement, got %v", err)
	}
	if rp.Name != "future-kubeatlas" {
		t.Errorf("rp.Name = %q, want future-kubeatlas", rp.Name)
	}
}

// TestLoadRulePackFromDir_MissingMetadata: missing metadata.yaml
// surfaces a wrapped error with the offending path so the caller
// can log it and skip rather than crash.
func TestLoadRulePackFromDir_MissingMetadata(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadRulePackFromDir(dir)
	if err == nil {
		t.Fatal("expected missing-metadata error, got nil")
	}
	if !strings.Contains(err.Error(), "metadata.yaml") {
		t.Errorf("err %q should mention metadata.yaml", err)
	}
	// errors.Is(os.ErrNotExist) flows through %w so callers can
	// distinguish "no pack here" from "bad pack here".
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err %v should wrap os.ErrNotExist", err)
	}
}

// TestLoadRulePackFromDir_BadYAML covers the parse-error branch:
// non-YAML content in metadata.yaml.
func TestLoadRulePackFromDir_BadYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.yaml"),
		[]byte("name: oops\n  : bad: indent"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRulePackFromDir(dir)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("err %q should mention parse failure", err)
	}
}

// TestLoadRulePackFromDir_MissingRegoFile: metadata references a
// .rego file that does not exist.
func TestLoadRulePackFromDir_MissingRegoFile(t *testing.T) {
	dir := t.TempDir()
	meta := `
name: ghost
version: 0.1.0
rego_api: v1
kubeatlas: ">= 1.0.0"
modules:
  - name: derive
    file: ghost.rego
    entrypoint: data.x.derive
    match:
      group: ""
      kind: Foo
`
	if err := os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRulePackFromDir(dir)
	if err == nil {
		t.Fatal("expected missing .rego file error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err %v should wrap os.ErrNotExist", err)
	}
}

// TestValidateMetadata_RequiredFields: each missing required field
// produces a distinct error mentioning the field name.
func TestValidateMetadata_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		md   metadataDoc
		want string
	}{
		{"missing name", metadataDoc{Version: "0.1.0", RegoAPI: "v1", KubeAtlas: ">= 1.0.0"}, "name required"},
		{"missing version", metadataDoc{Name: "x", RegoAPI: "v1", KubeAtlas: ">= 1.0.0"}, "version required"},
		{"missing rego_api", metadataDoc{Name: "x", Version: "0.1.0", KubeAtlas: ">= 1.0.0"}, "rego_api"},
		{"missing kubeatlas", metadataDoc{Name: "x", Version: "0.1.0", RegoAPI: "v1"}, "kubeatlas"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateMetadata(&c.md)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err %q should mention %q", err, c.want)
			}
		})
	}
}

// TestRegisterTo_NilPackOrEngine: defensive nil checks so a
// programming error in a future caller surfaces with a useful
// message rather than a nil-deref panic.
func TestRegisterTo_NilPackOrEngine(t *testing.T) {
	var rp *RulePack
	if err := rp.RegisterTo(context.Background(), newSilentEngine(t)); err == nil {
		t.Error("nil pack: expected error, got nil")
	}
	rp2 := &RulePack{}
	if err := rp2.RegisterTo(context.Background(), nil); err == nil {
		t.Error("nil engine: expected error, got nil")
	}
}

// TestRegisterTo_BadModuleSurfaces: a module with a syntactically
// broken source bubbles the OPA compile error up wrapped with the
// namespaced module key.
func TestRegisterTo_BadModuleSurfaces(t *testing.T) {
	rp := &RulePack{
		Name: "bad",
		Modules: []*ModuleSpec{
			{
				Name:       "broken",
				Source:     "this is not rego",
				Entrypoint: "data.x.deny",
			},
		},
	}
	err := rp.RegisterTo(context.Background(), newSilentEngine(t))
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
	if !strings.Contains(err.Error(), "bad/broken") {
		t.Errorf("err %q should mention namespaced module key bad/broken", err)
	}
}

// TestCheckKubeAtlasConstraint_BadConstraintSyntax: malformed
// constraint string surfaces a real error rather than silently
// passing or treating the pack as compatible.
func TestCheckKubeAtlasConstraint_BadConstraintSyntax(t *testing.T) {
	withKubeatlasVersion(t, "1.0.0")
	if err := checkKubeAtlasConstraint("not-a-constraint"); err == nil {
		t.Fatal("expected constraint syntax error, got nil")
	}
}

// TestEmbeddedOpenShift loads the binary-baked OpenShift pack,
// registers it into a fresh engine, and runs Route → Service
// derivation to assert the rule produces the documented edge.
func TestEmbeddedOpenShift(t *testing.T) {
	pack, err := EmbeddedOpenShift()
	if err != nil {
		t.Fatalf("EmbeddedOpenShift: %v", err)
	}
	if pack.Name != "openshift" || pack.Version != "0.1.0" {
		t.Errorf("pack = %+v, want openshift v0.1.0", pack)
	}
	if pack.RegoAPI != "v1" {
		t.Errorf("rego_api = %q, want v1", pack.RegoAPI)
	}
	if len(pack.Modules) == 0 {
		t.Fatal("expected at least one module")
	}

	e := newSilentEngine(t)
	if err := pack.RegisterTo(context.Background(), e); err != nil {
		t.Fatalf("RegisterTo: %v", err)
	}

	rs, err := e.Evaluate(context.Background(), "openshift/route", map[string]any{
		"kind":       "Route",
		"apiVersion": "route.openshift.io/v1",
		"metadata":   map[string]any{"namespace": "demo", "name": "front"},
		"spec":       map[string]any{"to": map[string]any{"kind": "Service", "name": "front-svc"}},
	})
	if err != nil {
		t.Fatalf("Evaluate route: %v", err)
	}
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		t.Fatal("empty result set from openshift/route")
	}
	edges, ok := rs[0].Expressions[0].Value.([]any)
	if !ok || len(edges) != 1 {
		t.Fatalf("expected exactly one ROUTES_TO edge, got %v", rs[0].Expressions[0].Value)
	}
	edge := edges[0].(map[string]any)
	if edge["type"] != "ROUTES_TO" {
		t.Errorf("edge.type = %v, want ROUTES_TO", edge["type"])
	}
}
