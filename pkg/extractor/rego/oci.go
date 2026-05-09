// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"fmt"
	"strings"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// ociScheme is the user-friendly prefix Helm values commonly
// document. We strip it before handing the ref to oras-go, which
// expects bare repository:tag form.
const ociScheme = "oci://"

// pullOCIArtifact downloads a rule-pack OCI artifact into destDir.
// metadata.yaml + every shipped .rego file land at the directory
// root, matching what LoadRulePackFromDir expects.
//
// Authentication uses the standard Docker credential helpers via
// oras-go's `credentials.NewStoreFromDocker` — i.e. the same flow
// `docker login ghcr.io` already established. Anonymous pulls work
// without setup; private artifacts require the operator to have
// done a `docker login` against the registry beforehand.
func pullOCIArtifact(ctx context.Context, ref, destDir string) error {
	ref = strings.TrimPrefix(ref, ociScheme)

	repoRef, tag, err := splitRefTag(ref)
	if err != nil {
		return err
	}

	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return fmt.Errorf("parse repository %q: %w", repoRef, err)
	}

	// Plug in Docker credentials when available; fall through to
	// anonymous pulls when the helper chain has nothing for this
	// registry.
	if store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{}); err == nil {
		repo.Client = &auth.Client{
			Client:     auth.DefaultClient.Client,
			Credential: credentials.Credential(store),
		}
	}

	dst, err := file.New(destDir)
	if err != nil {
		return fmt.Errorf("create file store at %q: %w", destDir, err)
	}
	defer dst.Close()

	if _, err := oras.Copy(ctx, repo, tag, dst, tag, oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("oras copy %s:%s: %w", repoRef, tag, err)
	}
	return nil
}

// splitRefTag pulls the trailing :tag off an OCI reference.
// "ghcr.io/lithastra/rules/openshift:0.1.0" → ("ghcr.io/...", "0.1.0").
// Tagless references default to "latest" but we treat that as a
// hard error per anti-pattern #26 — the OCI catalogue must always
// pin semver-shaped tags.
func splitRefTag(ref string) (string, string, error) {
	// Look for the last ':' AFTER the registry hostname's port colon.
	// Easiest way: find the last '/' first, then split the trailing
	// segment on ':'.
	slash := strings.LastIndex(ref, "/")
	tail := ref
	prefix := ""
	if slash >= 0 {
		prefix = ref[:slash+1]
		tail = ref[slash+1:]
	}
	colon := strings.LastIndex(tail, ":")
	if colon < 0 {
		return "", "", fmt.Errorf("ref %q is missing a tag (anti-pattern: pin semver tags)", ref)
	}
	repo := prefix + tail[:colon]
	tag := tail[colon+1:]
	if tag == "" {
		return "", "", fmt.Errorf("ref %q has empty tag", ref)
	}
	if tag == "latest" {
		return "", "", fmt.Errorf("ref %q uses :latest; pin a semver tag (anti-pattern #26)", ref)
	}
	return repo, tag, nil
}
