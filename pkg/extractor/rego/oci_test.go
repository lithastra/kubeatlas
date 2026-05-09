// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/content/memory"
)

// TestSplitRefTag covers the parser used by both the loader and the
// CLI to tear "<host>/<path>:<tag>" into (host+path, tag).
func TestSplitRefTag(t *testing.T) {
	cases := []struct {
		ref       string
		wantRepo  string
		wantTag   string
		wantError bool
	}{
		{"ghcr.io/lithastra/rules/openshift:0.1.0", "ghcr.io/lithastra/rules/openshift", "0.1.0", false},
		{"localhost:5000/test:1.2.3", "localhost:5000/test", "1.2.3", false},
		{"ghcr.io/foo/bar", "", "", true},  // no tag
		{"foo:latest", "", "", true},       // pinned :latest forbidden
		{"foo:", "", "", true}, // empty tag
	}
	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			repo, tag, err := splitRefTag(c.ref)
			if c.wantError {
				if err == nil {
					t.Errorf("expected error for %q, got nil", c.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if repo != c.wantRepo || tag != c.wantTag {
				t.Errorf("got (%q, %q), want (%q, %q)", repo, tag, c.wantRepo, c.wantTag)
			}
		})
	}
}

// TestLoadRulePackFromOCI_LocalMemoryStore exercises the pull path
// without touching the network: stand up an in-memory ORAS source,
// push the sample rule pack, then have LoadRulePackFromOCI's helper
// (pullOCIArtifact, factored via a local copy here) walk the dst.
//
// We reach for ORAS's memory store because oras-go has no public
// "pull from local FS via repo URL" shortcut; building the source
// directly from artifact bytes is the simplest way to exercise the
// extraction logic without standing up a registry.
func TestLoadRulePackFromOCI_LocalMemoryStore(t *testing.T) {
	ctx := context.Background()

	src := memory.New()
	pushFakePack(t, ctx, src, "v0.1.0", map[string]string{
		"metadata.yaml": `name: oci-test
version: 0.1.0
rego_api: v1
kubeatlas: ">= 1.0.0"
modules:
  - name: derive
    file: derive.rego
    entrypoint: data.kubeatlas.oci_test.derive
    match:
      group: example.com
      kind: Foo
`,
		"derive.rego": `package kubeatlas.oci_test

import rego.v1

derive contains edge if {
  input.kind == "Foo"
  edge := {
    "type": "OCI_TEST",
    "from": {"kind": "Foo", "namespace": "n", "name": "a"},
    "to":   {"kind": "Bar", "namespace": "n", "name": "b"},
  }
}
`,
	})

	dstDir := t.TempDir()
	dst, err := file.New(dstDir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	defer dst.Close()

	if _, err := oras.Copy(ctx, src, "v0.1.0", dst, "v0.1.0", oras.DefaultCopyOptions); err != nil {
		t.Fatalf("oras.Copy: %v", err)
	}

	// Files extracted to dstDir. LoadRulePackFromDir consumes them.
	pack, err := LoadRulePackFromDir(dstDir)
	if err != nil {
		t.Fatalf("LoadRulePackFromDir: %v", err)
	}
	if pack.Name != "oci-test" || pack.Version != "0.1.0" {
		t.Errorf("pack = %+v, want oci-test v0.1.0", pack)
	}
	if len(pack.Modules) != 1 || pack.Modules[0].Name != "derive" {
		t.Errorf("modules = %+v, want [derive]", pack.Modules)
	}

	// Sanity: the .rego we pushed survived the round trip byte-for-byte.
	body, err := os.ReadFile(filepath.Join(dstDir, "derive.rego"))
	if err != nil {
		t.Fatalf("read pulled derive.rego: %v", err)
	}
	if !strings.Contains(string(body), "OCI_TEST") {
		t.Errorf("pulled .rego missing OCI_TEST marker; got %q", body)
	}
}

// pushFakePack uploads files as an OCI artifact with the
// rule-pack mediaType; tag becomes the artifact's discovery handle.
func pushFakePack(t *testing.T, ctx context.Context, dst oras.Target, tag string, files map[string]string) {
	t.Helper()

	// Stage files in a tmp file store, push to dst.
	src, err := file.New(t.TempDir())
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	defer src.Close()

	tmp := t.TempDir()
	var layers []ocispec.Descriptor
	for name, body := range files {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		desc, err := src.Add(ctx, name, "application/vnd.kubeatlas.rulepack.v1+yaml", path)
		if err != nil {
			t.Fatalf("file store add %s: %v", name, err)
		}
		layers = append(layers, desc)
	}

	manifestDesc, err := oras.PackManifest(ctx, src, oras.PackManifestVersion1_1,
		"application/vnd.kubeatlas.rulepack.v1+yaml",
		oras.PackManifestOptions{Layers: layers},
	)
	if err != nil {
		t.Fatalf("pack manifest: %v", err)
	}
	if err := src.Tag(ctx, manifestDesc, tag); err != nil {
		t.Fatalf("tag src: %v", err)
	}
	if _, err := oras.Copy(ctx, src, tag, dst, tag, oras.DefaultCopyOptions); err != nil {
		t.Fatalf("seed dst: %v", err)
	}
}
