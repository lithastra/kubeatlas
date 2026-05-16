// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"

	"github.com/lithastra/kubeatlas/pkg/version"
)

//go:embed embedded/openshift/*.rego embedded/openshift/*.yaml
var embeddedFS embed.FS

// EmbeddedOpenShift returns the rule pack baked into the kubeatlas
// binary at build time. This is what the OpenShift API-group
// detector loads when it sees route.openshift.io (guide §2.4 + the
// "OpenShift is a first-class platform support" promise from §1.3).
//
// Embedding (vs an OCI fetch) is intentional: source distribution
// keeps the binary self-contained, makes the bytes auditable in
// kubeatlas's own git history, and dodges the "CI network down at
// release time" failure mode (anti-pattern #21). The lithastra/
// kubeatlas-rules repo ships the same files for community contrib;
// release tooling syncs the two paths.
func EmbeddedOpenShift() (*RulePack, error) {
	return LoadRulePackFromFS(embeddedFS, "embedded/openshift")
}

// LoadRulePackFromOCI pulls a rule pack OCI artifact, extracts it
// to a temp directory, and reuses LoadRulePackFromDir so the
// validation path is identical to the on-disk loader. Returns the
// pack ready for RegisterTo; the temp directory is cleaned up
// automatically — RulePack.Source bytes are already in memory.
//
// ref accepts both the bare repository:tag form
// ("ghcr.io/lithastra/rules/openshift:0.1.0") and the oci:// scheme
// form ("oci://ghcr.io/...:tag") that operator-friendly Helm
// values prefer.
//
// Sigstore signature verification (P3-T-COS / invariant 2.9) runs
// when WithSignatureVerification(true) is passed: after the pull,
// the artifact's manifest digest is verified against the keyless
// signature attached as an OCI referrer. A verification failure is
// returned wrapped in ErrSignatureVerification so the bootstrap can
// make it fatal — "failed but continued" equals "not verified".
// With verification off (the v1.1 default, and the only sane mode
// for an air-gapped install) the pull proceeds unchecked, exactly
// as it did before this option existed.
func LoadRulePackFromOCI(ctx context.Context, ref string, opts ...OCIOption) (*RulePack, error) {
	o := newOCIOptions(opts)

	tmpDir, err := os.MkdirTemp("", "kubeatlas-rulepack-*")
	if err != nil {
		return nil, fmt.Errorf("LoadRulePackFromOCI: tempdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	manifest, err := pullOCIArtifact(ctx, ref, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("LoadRulePackFromOCI %s: %w", ref, err)
	}

	if o.verify {
		if err := verifyOCISignature(ctx, ref, manifest, o); err != nil {
			// Already wrapped in ErrSignatureVerification.
			return nil, fmt.Errorf("LoadRulePackFromOCI %s: %w", ref, err)
		}
	}

	return LoadRulePackFromDir(tmpDir)
}

// supportedRegoAPI is the rule-pack interface version this engine
// understands. Bumping it = breaking change for every published
// rule pack (guide §2.5: rego_api is a contract, v1 stays for at
// least 6 months after v2 ships before the engine drops it).
const supportedRegoAPI = "v1"

// ErrIncompatibleRegoAPI / ErrIncompatibleKubeAtlas distinguish the
// two metadata-rejection causes so the rule-pack loader's caller
// (P2-T13 OCI loader, main.go bootstrap) can react differently —
// e.g. surface "needs KubeAtlas 1.2+" in the UI.
var (
	ErrIncompatibleRegoAPI   = errors.New("rule pack rego_api is not supported by this engine")
	ErrIncompatibleKubeAtlas = errors.New("rule pack requires a different KubeAtlas version")
)

// RulePack is the in-memory shape of a loaded rule-pack directory:
// metadata.yaml's required fields plus every .rego module the
// metadata references. Source bytes live here; the engine never
// persists them after RegisterTo.
type RulePack struct {
	Name         string
	Version      string
	RegoAPI      string
	KubeAtlasMin string
	Modules      []*ModuleSpec
}

// ModuleSpec is one .rego file plus the GVK match the rule pack
// declares for it. The router (P2-T9) consumes Match to decide
// which modules see a given resource.
type ModuleSpec struct {
	Name       string
	Match      GVKMatch
	Source     string
	Entrypoint string
}

// GVKMatch narrows a module to a single Group + Kind. Empty fields
// match anything; both empty matches every resource (used for
// catch-all built-in rules — not exposed via metadata in P2-T8 but
// the type is shaped to support it for P2-T9's router).
type GVKMatch struct {
	Group string
	Kind  string
}

// metadataDoc mirrors the on-disk YAML shape. Kept private so the
// public type RulePack stays unchanged when we add fields to the
// file format (e.g. P2-T13 may add `signature` for cosign verify).
type metadataDoc struct {
	Name      string `yaml:"name"`
	Version   string `yaml:"version"`
	RegoAPI   string `yaml:"rego_api"`
	KubeAtlas string `yaml:"kubeatlas"`
	Modules   []struct {
		Name       string `yaml:"name"`
		File       string `yaml:"file"`
		Entrypoint string `yaml:"entrypoint"`
		Match      struct {
			Group string `yaml:"group"`
			Kind  string `yaml:"kind"`
		} `yaml:"match"`
	} `yaml:"modules"`
}

// LoadRulePackFromDir reads metadata.yaml + every referenced .rego
// file from dir, validates rego_api and the kubeatlas constraint,
// and returns a RulePack ready for RegisterTo. Thin wrapper over
// LoadRulePackFromFS that adapts the on-disk filesystem to fs.FS.
func LoadRulePackFromDir(dir string) (*RulePack, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("load %s: resolve abs: %w", dir, err)
	}
	return LoadRulePackFromFS(os.DirFS(abs), ".")
}

// LoadRulePackFromFS is the FS-driven core. Used by LoadRulePackFromDir
// for filesystem packs and by EmbeddedOpenShift for the binary-baked
// pack. Path joins use io/fs's path package so embed.FS slashes
// stay consistent across operating systems (anti-pattern: using
// filepath.Join here would break the embed lookup on Windows hosts).
func LoadRulePackFromFS(fsys fs.FS, dir string) (*RulePack, error) {
	metaPath := path.Join(dir, "metadata.yaml")
	body, err := fs.ReadFile(fsys, metaPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", metaPath, err)
	}

	var md metadataDoc
	if err := yaml.Unmarshal(body, &md); err != nil {
		return nil, fmt.Errorf("parse %s: %w", metaPath, err)
	}

	if err := validateMetadata(&md); err != nil {
		return nil, fmt.Errorf("validate %s: %w", metaPath, err)
	}

	rp := &RulePack{
		Name:         md.Name,
		Version:      md.Version,
		RegoAPI:      md.RegoAPI,
		KubeAtlasMin: md.KubeAtlas,
		Modules:      make([]*ModuleSpec, 0, len(md.Modules)),
	}
	for i := range md.Modules {
		m := &md.Modules[i]
		if m.Name == "" || m.File == "" || m.Entrypoint == "" {
			return nil, fmt.Errorf(
				"validate %s: module[%d]: name, file, entrypoint all required",
				metaPath, i,
			)
		}
		srcPath := path.Join(dir, m.File)
		src, err := fs.ReadFile(fsys, srcPath)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", srcPath, err)
		}
		rp.Modules = append(rp.Modules, &ModuleSpec{
			Name:       m.Name,
			Match:      GVKMatch{Group: m.Match.Group, Kind: m.Match.Kind},
			Source:     string(src),
			Entrypoint: m.Entrypoint,
		})
	}
	return rp, nil
}

// validateMetadata enforces rego_api and the kubeatlas semver
// constraint. Each rejection wraps a typed sentinel so callers can
// errors.Is and react.
func validateMetadata(md *metadataDoc) error {
	if md.Name == "" {
		return errors.New("metadata.name required")
	}
	if md.Version == "" {
		return errors.New("metadata.version required")
	}
	if md.RegoAPI != supportedRegoAPI {
		return fmt.Errorf("%w: pack=%s requires rego_api=%q, engine supports %q",
			ErrIncompatibleRegoAPI, md.Name, md.RegoAPI, supportedRegoAPI)
	}
	if md.KubeAtlas == "" {
		return errors.New("metadata.kubeatlas (semver constraint) required")
	}
	if err := checkKubeAtlasConstraint(md.KubeAtlas); err != nil {
		return fmt.Errorf("%w: pack=%s constraint=%q kubeatlas=%s: %v",
			ErrIncompatibleKubeAtlas, md.Name, md.KubeAtlas, version.Version, err)
	}
	return nil
}

// checkKubeAtlasConstraint compares the metadata's semver constraint
// against the build-time-injected version.Version. Dev builds
// (Version=="dev" / non-semver) skip the check — pinning a release
// to a specific KubeAtlas version is meaningful only for binaries
// goreleaser shipped, not for `go run` invocations.
func checkKubeAtlasConstraint(constraint string) error {
	v, err := semver.NewVersion(strings.TrimPrefix(version.Version, "v"))
	if err != nil {
		// Non-semver version → dev / snapshot build; skip silently
		// so contributors can iterate without bumping their local
		// kubeatlas version every time a pack tightens its range.
		return nil
	}
	c, err := semver.NewConstraint(constraint)
	if err != nil {
		return fmt.Errorf("invalid constraint %q: %w", constraint, err)
	}
	if !c.Check(v) {
		return fmt.Errorf("version %s does not satisfy %s", v, constraint)
	}
	return nil
}

// RegisterTo loads every module of the pack into the given engine.
// Module names are namespaced as "<pack>/<module>" so two packs that
// happen to use the same module name do not collide.
//
// On the first failure, returns wrapped error and stops; partial
// registration is left in place (engine.LoadModule replaces by
// name, so a re-attempt after the source is fixed is a clean swap).
// Caller decides whether to ignore the failure (warn + skip, anti-
// pattern #35) or treat it as fatal — Phase 2 main.go opts for
// skip so one bad pack does not kill the engine.
func (rp *RulePack) RegisterTo(ctx context.Context, e *Engine) error {
	if rp == nil {
		return errors.New("RulePack.RegisterTo: nil pack")
	}
	if e == nil {
		return errors.New("RulePack.RegisterTo: nil engine")
	}
	for _, m := range rp.Modules {
		regKey := rp.Name + "/" + m.Name
		if err := e.LoadModule(ctx, regKey, m.Source, m.Entrypoint); err != nil {
			return fmt.Errorf("register %s: %w", regKey, err)
		}
	}
	return nil
}
