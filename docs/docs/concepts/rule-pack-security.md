---
sidebar_position: 7
title: Rule-pack signature verification
---

# Rule-pack signature verification

A rule pack is executable policy: Rego modules that KubeAtlas
evaluates against your cluster's resources. A pack pulled from an
OCI registry you do not control is code you did not write running
inside KubeAtlas. Signature verification answers one question
before that code loads: **did the publisher you trust actually
produce these bytes?**

## The signing model

KubeAtlas verifies rule packs with [Sigstore](https://www.sigstore.dev/) —
the same keyless signing the Kubernetes project itself uses.

There is **no signing key embedded in the KubeAtlas binary**.
Verification is *keyless*: the signature is backed by a short-lived
certificate that [Fulcio](https://docs.sigstore.dev/certificate_authority/overview/)
issued to a CI workload, bound to that workload's OIDC identity,
and logged in the public [Rekor](https://docs.sigstore.dev/logging/overview/)
transparency log. KubeAtlas verifies the certificate chains to the
Sigstore public-good trust root and that its identity matches a
policy you trust.

Keyless verification means a compromised key is not a thing that
can happen — there is no long-lived key. Rotating the signing
identity never requires a KubeAtlas rebuild.

## First-party vs third-party packs

KubeAtlas splits trust into two tiers by the pack's registry path:

**First-party packs** — anything published under
`ghcr.io/lithastra/`. These are pinned to a single identity: a
signature minted by the `kubeatlas-rules` release workflow, running
on a release tag, through GitHub Actions OIDC. This policy is
compiled in and **cannot be widened** — an operator cannot add
trusted identities for Lithastra's namespace, so a pack that
appears to come from Lithastra but was signed by anyone else is
rejected.

**Third-party packs** — every other registry path. These have no
built-in trust. To load one with verification on, you tell
KubeAtlas which identity is allowed to sign it via
`rulePacks.trustedIdentities` (below). A third-party pack whose
signer matches none of the configured identities is rejected.

## Enabling verification

```yaml
rulePacks:
  verifySignature: true
  extras:
    - oci://ghcr.io/lithastra/rules/eks:0.1.0
```

`verifySignature` defaults to **`false`** in v1.1. This is
deliberate: existing installs that already load packs must be able
to upgrade to v1.1 without their packs suddenly failing to load. It
gives operators a window to turn verification on once their packs
are signed. **v1.2 changes the default to `true`.**

When verification is on, a pack whose signature does not verify is
**fatal** — the KubeAtlas Pod fails to start, and the log says
exactly which pack and why. There is no "warn and continue" mode: a
verification failure that is logged and ignored is identical to no
verification at all. If a pack must load, fix its signature or its
trust policy; do not work around the check.

### Trusting a third-party publisher

```yaml
rulePacks:
  verifySignature: true
  trustedIdentities:
    - issuer: https://token.actions.githubusercontent.com
      identityRegexp: ^https://github\.com/acme/rules/\.github/workflows/release\.yml@refs/tags/.+$
  extras:
    - oci://ghcr.io/acme/rules/argo:0.2.0
```

Each entry constrains the signing certificate's OIDC `issuer` and
its subject identity (`identity` for an exact match, or
`identityRegexp` for a pattern). At least one of `issuer` /
`issuerRegexp` is required. The example above trusts any signature
the `acme/rules` release workflow produced on a tag.

## Air-gapped clusters

Keyless verification needs to reach the Sigstore public-good trust
root. A cluster with no path to it cannot verify, and must run with
`verifySignature: false` — the only supported air-gapped mode:

```yaml
rulePacks:
  verifySignature: false
```

This is an explicit, logged choice, not a silent bypass. KubeAtlas
will not "quietly skip" verification when the trust root is
unreachable — verification is either on (and a fetch failure is
fatal) or off (and you said so). An air-gapped operator who still
wants assurance should verify packs out-of-band with
`cosign verify` before mirroring them into the local registry.

## Contributing a signed third-party pack

To publish a pack other KubeAtlas operators can verify:

1. Distribute the pack as an OCI artifact (the same format
   `rulePacks.extras` already consumes).
2. Sign it in CI with `cosign` keyless signing, so the signature
   is attached to the artifact as a Sigstore bundle referrer.
3. Publish the workflow identity that signs your releases —
   operators put it in `rulePacks.trustedIdentities` to trust you.

Because trust is keyless and identity-scoped, you never ship a
public key and operators never manage one. The unit of trust is
"the CI workflow at this repository path", which is auditable in
your repository's history and in the public transparency log.
