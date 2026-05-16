// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

// ErrSignatureVerification is the sentinel every signature-related
// failure wraps. The bootstrap (cmd/kubeatlas) treats any error that
// errors.Is-es this sentinel as FATAL — guide invariant 2.9: a rule
// pack whose signature does not verify must abort startup, never
// "warn and continue". "Failed but continued" equals "not verified".
var ErrSignatureVerification = errors.New("rule pack signature verification failed")

// sigstoreBundleArtifactType is the OCI artifactType a Sigstore
// signature bundle carries when it is attached as a referrer of the
// signed artifact (cosign's new-bundle-format default).
const sigstoreBundleArtifactType = "application/vnd.dev.sigstore.bundle.v0.3+json"

// githubActionsIssuer is the OIDC issuer every keyless signature
// minted inside a GitHub Actions workflow carries.
const githubActionsIssuer = "https://token.actions.githubusercontent.com"

// firstPartyRepoPrefix identifies Lithastra-published ("first-party")
// rule packs by their registry path. A pack pulled from here is held
// to firstPartyPolicy; anything else must match an operator-supplied
// trusted identity.
const firstPartyRepoPrefix = "ghcr.io/lithastra/"

// TrustPolicy is one acceptable signing identity. A rule-pack
// signature passes the policy if its Fulcio certificate's OIDC
// issuer and SAN match — either exactly (Issuer / Identity) or by
// regular expression (IssuerRegexp / IdentityRegexp). An empty exact
// field with an empty regexp means "do not constrain this field";
// at least one of issuer/identity should always be constrained.
// The json tags match the Helm values.yaml shape so the chart can
// render rulePacks.trustedIdentities straight into the env var
// main.go decodes.
type TrustPolicy struct {
	Issuer         string `json:"issuer,omitempty"`
	IssuerRegexp   string `json:"issuerRegexp,omitempty"`
	Identity       string `json:"identity,omitempty"`
	IdentityRegexp string `json:"identityRegexp,omitempty"`
}

// firstPartyPolicy pins the keyless identity of Lithastra's own rule
// packs: a signature minted by the kubeatlas-rules release workflow,
// running on a tag, attested through GitHub Actions OIDC. No public
// key is embedded in the binary — verification is keyless against the
// Sigstore public-good trust root, so rotating the signing identity
// never needs a kubeatlas rebuild.
var firstPartyPolicy = TrustPolicy{
	Issuer: githubActionsIssuer,
	// The tag suffix is .+ (not [^/]+): kubeatlas-rules is a monorepo
	// whose per-pack tags carry a slash, e.g. "eks/v0.1.0".
	IdentityRegexp: `^https://github\.com/lithastra/kubeatlas-rules/\.github/workflows/release\.yml@refs/tags/.+$`,
}

// certificateIdentity translates a TrustPolicy into the sigstore-go
// matcher the verifier consumes.
func (p TrustPolicy) certificateIdentity() (verify.CertificateIdentity, error) {
	return verify.NewShortCertificateIdentity(p.Issuer, p.IssuerRegexp, p.Identity, p.IdentityRegexp)
}

// OCIOptions controls how LoadRulePackFromOCI fetches and trusts a
// rule pack. The zero value verifies nothing — the v1.1 default,
// kept so existing Tier 1 / air-gapped installs are unaffected.
// v1.2 flips the default on once every first-party pack is signed.
type OCIOptions struct {
	verify            bool
	trustedIdentities []TrustPolicy
	// trustedMaterial is a test seam. Production leaves it nil and
	// fetches the Sigstore public-good trusted root over TUF.
	trustedMaterial root.TrustedMaterial
}

// OCIOption is a functional option for LoadRulePackFromOCI.
type OCIOption func(*OCIOptions)

// WithSignatureVerification turns cosign/Sigstore signature
// verification on or off. Off is the v1.1 default (see OCIOptions).
func WithSignatureVerification(enabled bool) OCIOption {
	return func(o *OCIOptions) { o.verify = enabled }
}

// WithTrustedIdentities registers signing identities for third-party
// rule packs — packs not published under ghcr.io/lithastra. With
// verification on, a third-party pack whose signer matches none of
// these is rejected.
func WithTrustedIdentities(policies ...TrustPolicy) OCIOption {
	return func(o *OCIOptions) {
		o.trustedIdentities = append(o.trustedIdentities, policies...)
	}
}

// withTrustedMaterial injects a Sigstore trust root. Test-only —
// production verification fetches the public-good root.
func withTrustedMaterial(tm root.TrustedMaterial) OCIOption {
	return func(o *OCIOptions) { o.trustedMaterial = tm }
}

func newOCIOptions(opts []OCIOption) *OCIOptions {
	o := &OCIOptions{}
	for _, fn := range opts {
		fn(o)
	}
	return o
}

// applicablePolicies returns the trust policies a pack at ref must
// satisfy. First-party packs (ghcr.io/lithastra/...) are pinned to
// firstPartyPolicy and ignore operator-supplied identities — an
// operator cannot widen trust for Lithastra's namespace. Every other
// ref is verified against the configured trusted identities only.
func (o *OCIOptions) applicablePolicies(ref string) []TrustPolicy {
	bare := strings.TrimPrefix(ref, ociScheme)
	if strings.HasPrefix(bare, firstPartyRepoPrefix) {
		return []TrustPolicy{firstPartyPolicy}
	}
	return o.trustedIdentities
}

// verifyOCISignature fetches the Sigstore signature attached to the
// rule-pack artifact and verifies it against the trust policies that
// apply to ref. Every failure path wraps ErrSignatureVerification so
// the caller can make it fatal (invariant 2.9).
func verifyOCISignature(ctx context.Context, ref string, manifest ocispec.Descriptor, opts *OCIOptions) error {
	policies := opts.applicablePolicies(ref)
	if len(policies) == 0 {
		return fmt.Errorf("%w: %s has no trusted signing identity — "+
			"first-party packs come from %s, third-party packs need rulePacks.trustedIdentities",
			ErrSignatureVerification, ref, firstPartyRepoPrefix)
	}

	tm := opts.trustedMaterial
	if tm == nil {
		tr, err := root.FetchTrustedRoot()
		if err != nil {
			return fmt.Errorf("%w: fetch Sigstore trusted root: %v",
				ErrSignatureVerification, err)
		}
		tm = tr
	}

	repo, _, err := newRepository(ref)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignatureVerification, err)
	}

	entity, err := fetchSignatureBundle(ctx, repo, manifest)
	if err != nil {
		return err // already wrapped
	}

	digest, err := manifestDigestBytes(manifest)
	if err != nil {
		return err
	}
	return verifyEntity(tm, entity, digest, policies)
}

// verifyEntity is the trust core, kept registry-free so it is fully
// unit-testable against an in-process Sigstore. It accepts the
// signature if any one policy matches the certificate; it rejects
// (and returns a wrapped error) only when none does.
func verifyEntity(tm root.TrustedMaterial, entity verify.SignedEntity, artifactDigest []byte, policies []TrustPolicy) error {
	v, err := verify.NewVerifier(tm,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("%w: build verifier: %v", ErrSignatureVerification, err)
	}

	var errs []error
	for _, p := range policies {
		certID, err := p.certificateIdentity()
		if err != nil {
			return fmt.Errorf("%w: invalid trust policy %+v: %v",
				ErrSignatureVerification, p, err)
		}
		_, err = v.Verify(entity, verify.NewPolicy(
			verify.WithArtifactDigest("sha256", artifactDigest),
			verify.WithCertificateIdentity(certID),
		))
		if err == nil {
			return nil // a trusted identity signed this pack
		}
		errs = append(errs, err)
	}
	return fmt.Errorf("%w: no trusted identity matched the signature: %w",
		ErrSignatureVerification, errors.Join(errs...))
}

// fetchSignatureBundle locates the Sigstore bundle attached as an
// OCI referrer of the signed manifest and parses it into a
// verify.SignedEntity.
func fetchSignatureBundle(ctx context.Context, repo *remote.Repository, manifest ocispec.Descriptor) (*bundle.Bundle, error) {
	var bundleDesc ocispec.Descriptor
	found := false
	err := repo.Referrers(ctx, manifest, sigstoreBundleArtifactType,
		func(refs []ocispec.Descriptor) error {
			if !found && len(refs) > 0 {
				bundleDesc = refs[0]
				found = true
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("%w: list signature referrers: %v",
			ErrSignatureVerification, err)
	}
	if !found {
		return nil, fmt.Errorf("%w: artifact carries no Sigstore signature "+
			"(expected a %s referrer)", ErrSignatureVerification, sigstoreBundleArtifactType)
	}

	// The referrer is itself a manifest; its single layer blob holds
	// the bundle JSON.
	manBytes, err := content.FetchAll(ctx, repo, bundleDesc)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch signature manifest: %v",
			ErrSignatureVerification, err)
	}
	var sigManifest ocispec.Manifest
	if err := json.Unmarshal(manBytes, &sigManifest); err != nil {
		return nil, fmt.Errorf("%w: parse signature manifest: %v",
			ErrSignatureVerification, err)
	}
	if len(sigManifest.Layers) == 0 {
		return nil, fmt.Errorf("%w: signature manifest has no layers",
			ErrSignatureVerification)
	}
	blob, err := content.FetchAll(ctx, repo, sigManifest.Layers[0])
	if err != nil {
		return nil, fmt.Errorf("%w: fetch signature bundle: %v",
			ErrSignatureVerification, err)
	}

	var b bundle.Bundle
	if err := b.UnmarshalJSON(blob); err != nil {
		return nil, fmt.Errorf("%w: parse signature bundle: %v",
			ErrSignatureVerification, err)
	}
	return &b, nil
}

// manifestDigestBytes returns the raw bytes of the manifest's
// sha256 digest — the value cosign signs when it signs the artifact.
func manifestDigestBytes(d ocispec.Descriptor) ([]byte, error) {
	if d.Digest.Algorithm() != "sha256" {
		return nil, fmt.Errorf("%w: artifact digest algorithm is %q, want sha256",
			ErrSignatureVerification, d.Digest.Algorithm())
	}
	b, err := hex.DecodeString(d.Digest.Encoded())
	if err != nil {
		return nil, fmt.Errorf("%w: decode artifact digest: %v",
			ErrSignatureVerification, err)
	}
	return b, nil
}
