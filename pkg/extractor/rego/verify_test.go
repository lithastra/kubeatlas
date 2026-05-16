// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
)

// firstPartyIdentity is a SAN that matches firstPartyPolicy — a
// signature minted by the kubeatlas-rules release workflow on a tag.
const firstPartyIdentity = "https://github.com/lithastra/kubeatlas-rules/" +
	".github/workflows/release.yml@refs/tags/eks/v0.1.0"

// signArtifact mints a keyless signature over artifact with the given
// identity/issuer against an in-process Sigstore, and returns the
// trust material, the signed entity, and the artifact's sha256.
func signArtifact(t *testing.T, identity, issuer string, artifact []byte) (*ca.VirtualSigstore, *ca.TestEntity, []byte) {
	t.Helper()
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore: %v", err)
	}
	entity, err := vs.Sign(identity, issuer, artifact)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sum := sha256.Sum256(artifact)
	return vs, entity, sum[:]
}

// --- verifyEntity: the registry-free trust core ---------------------

func TestVerifyEntity_FirstPartySignaturePasses(t *testing.T) {
	artifact := []byte("rule-pack-manifest-bytes")
	vs, entity, digest := signArtifact(t, firstPartyIdentity, githubActionsIssuer, artifact)

	if err := verifyEntity(vs, entity, digest, []TrustPolicy{firstPartyPolicy}); err != nil {
		t.Fatalf("a correctly-signed first-party pack must verify, got: %v", err)
	}
}

func TestVerifyEntity_WrongIdentityRejected(t *testing.T) {
	artifact := []byte("rule-pack-manifest-bytes")
	// Signed by some unrelated identity, not the release workflow.
	vs, entity, digest := signArtifact(t,
		"https://github.com/attacker/evil/.github/workflows/x.yml@refs/heads/main",
		githubActionsIssuer, artifact)

	err := verifyEntity(vs, entity, digest, []TrustPolicy{firstPartyPolicy})
	if err == nil {
		t.Fatal("a signature from an untrusted identity must be rejected")
	}
	if !errors.Is(err, ErrSignatureVerification) {
		t.Errorf("error must wrap ErrSignatureVerification, got: %v", err)
	}
}

func TestVerifyEntity_WrongDigestRejected(t *testing.T) {
	artifact := []byte("rule-pack-manifest-bytes")
	vs, entity, _ := signArtifact(t, firstPartyIdentity, githubActionsIssuer, artifact)

	// A valid signature, but presented as covering a different artifact.
	tampered := sha256.Sum256([]byte("a-different-artifact"))
	err := verifyEntity(vs, entity, tampered[:], []TrustPolicy{firstPartyPolicy})
	if err == nil {
		t.Fatal("a signature must not verify against a digest it does not cover")
	}
	if !errors.Is(err, ErrSignatureVerification) {
		t.Errorf("error must wrap ErrSignatureVerification, got: %v", err)
	}
}

func TestVerifyEntity_AnyMatchingPolicyPasses(t *testing.T) {
	artifact := []byte("third-party-pack")
	const issuer = githubActionsIssuer
	identity := "https://github.com/acme/rules/.github/workflows/release.yml@refs/tags/v2.0.0"
	vs, entity, digest := signArtifact(t, identity, issuer, artifact)

	policies := []TrustPolicy{
		{Issuer: issuer, IdentityRegexp: `^https://github\.com/other/.+$`},      // no match
		{Issuer: issuer, IdentityRegexp: `^https://github\.com/acme/rules/.+$`}, // match
	}
	if err := verifyEntity(vs, entity, digest, policies); err != nil {
		t.Fatalf("verification must pass when any policy matches, got: %v", err)
	}
}

// --- applicablePolicies: first-party vs third-party routing ---------

func TestApplicablePolicies_FirstPartyRefIsPinned(t *testing.T) {
	o := newOCIOptions([]OCIOption{
		WithTrustedIdentities(TrustPolicy{Issuer: "https://evil.example"}),
	})
	for _, ref := range []string{
		"ghcr.io/lithastra/rules/eks:0.1.0",
		"oci://ghcr.io/lithastra/rules/openshift:0.1.0",
	} {
		got := o.applicablePolicies(ref)
		if len(got) != 1 || got[0] != firstPartyPolicy {
			t.Errorf("%s: first-party ref must pin firstPartyPolicy, got %+v", ref, got)
		}
	}
}

func TestApplicablePolicies_ThirdPartyUsesTrustedIdentities(t *testing.T) {
	tp := TrustPolicy{Issuer: githubActionsIssuer, IdentityRegexp: `^https://github\.com/acme/.+$`}
	o := newOCIOptions([]OCIOption{WithTrustedIdentities(tp)})

	got := o.applicablePolicies("oci://ghcr.io/acme/rules/thing:1.0.0")
	if len(got) != 1 || got[0] != tp {
		t.Errorf("third-party ref must use the configured identities, got %+v", got)
	}
}

func TestApplicablePolicies_ThirdPartyWithNoIdentitiesIsEmpty(t *testing.T) {
	o := newOCIOptions(nil)
	if got := o.applicablePolicies("oci://ghcr.io/acme/rules/thing:1.0.0"); len(got) != 0 {
		t.Errorf("an unconfigured third-party ref must yield no policies, got %+v", got)
	}
}

// --- OCIOptions plumbing -------------------------------------------

func TestOCIOptions_Defaults(t *testing.T) {
	o := newOCIOptions(nil)
	if o.verify {
		t.Error("verification must be off by default (v1.1 default)")
	}
	if len(o.trustedIdentities) != 0 {
		t.Error("no trusted identities by default")
	}
}

func TestOCIOptions_WithSignatureVerification(t *testing.T) {
	o := newOCIOptions([]OCIOption{WithSignatureVerification(true)})
	if !o.verify {
		t.Error("WithSignatureVerification(true) must enable verification")
	}
}

func TestTrustPolicy_CertificateIdentity_RejectsBadRegexp(t *testing.T) {
	p := TrustPolicy{IdentityRegexp: "([unclosed"}
	if _, err := p.certificateIdentity(); err == nil {
		t.Error("a malformed regexp must be rejected, not silently ignored")
	}
}
