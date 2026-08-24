---
sidebar_position: 8
title: Release process
---

# Release process

KubeAtlas is released as a set of independently versioned products.
A core Git tag is therefore the start of a release, not proof that the
whole release is usable. The release owner closes the matrix below with
public-download evidence before announcing the version.

## v1.5.2 release matrix

| Component | Required version | Distribution | Required evidence |
|---|---:|---|---|
| Core server and CLI | `v1.5.2` | GitHub Release | Checksums match every archive; binaries report `1.5.2` |
| Server image | `1.5.2` | `ghcr.io/lithastra/kubeatlas` | Anonymous multi-architecture manifest pull |
| PostgreSQL + AGE image | `16.6-age1.6.0-rc0.1` | `ghcr.io/lithastra/postgres-age` | Anonymous amd64 and arm64 manifest pull; SBOM and provenance attestations present |
| Helm chart | `1.5.2` | `oci://ghcr.io/lithastra/charts/kubeatlas` | Anonymous pull, lint, and clean-cluster install |
| Rule packs | Versions in each `metadata.yaml` | `ghcr.io/lithastra/rules/<pack>` | 14 formal packs resolve, verify against the release-workflow OIDC identity, pull, and load against their samples |
| GitHub Action | `v1.0.1` and moving `v1` | `lithastra/kubeatlas-action` | Real release download, checksum, extraction, execution, and temporary-directory cleanup |
| Krew manifest | `v1.5.2` | `kubernetes-sigs/krew-index` | All platform URLs and SHA256 values validate |
| Headlamp plugin | `1.2.0` | `headlamp-k8s/plugins` | Catalog metadata, archive checksum, tests, and production build agree |
| Backstage plugin | `1.0.0` | npm | Anonymous install and production build |
| Documentation | `1.5.2` | Documentation site | Production build has no broken links or anchors; current install commands resolve publicly |

The core v1.5.2 security patch is gated by its binaries, application and
database images, Helm chart, rule-pack audit, and documentation. The GitHub
Action, Krew manifest, Headlamp catalog, and Backstage npm package are
independently versioned integrations: record their evidence honestly, but do
not keep a published core security fix out of the production install path while
an external catalog review is pending. The Headlamp catalog PR may therefore
remain open without blocking v1.5.2; update it only for substantive code,
artifact, or maintainer feedback.

Recorded v1.5.2 evidence:

- The [public GitHub Release](https://github.com/lithastra/kubeatlas/releases/tag/v1.5.2)
  and [release workflow](https://github.com/lithastra/kubeatlas/actions/runs/32648122333)
  publish the tagged binaries, image, and Chart. The public Chart resolves to
  OCI digest `sha256:6fda2e18b31537aa99db0b63366387c64e7e526e0c1ba7ccf7999048e3e1cee5`.
- The [rule-pack distribution audit](https://github.com/lithastra/kubeatlas-rules/actions/runs/32675302764)
  resolves, verifies, pulls, and loads all 14 formal OCI packs.
- The [GitHub Action compatibility run](https://github.com/lithastra/kubeatlas-action/actions/runs/32686038064)
  downloads and exercises the v1.5.2 release against a real kind cluster.
- The [Krew v1.5.2 manifest](https://github.com/kubernetes-sigs/krew-index/pull/6234)
  is merged. Backstage `1.0.0` resolves anonymously from npm. The
  [Headlamp catalog PR](https://github.com/headlamp-k8s/plugins/pull/757)
  remains under external review and is not represented as merged.

Apache AGE does not publish a GA `1.6.0` tag for PostgreSQL 16. The
database image therefore names the upstream `1.6.0-rc0` dependency
explicitly and pins its Git commit in the Dockerfile. The final `.1`
is the KubeAtlas image recipe revision. Reusing this tag for different
bytes is not allowed. The release workflow first inspects the registry:
if the tag already exists, it verifies and reuses its amd64, arm64, and
attestation manifests. It builds only when the tag is absent. A registry
error is fatal rather than being treated as absence, and the release
contract rejects a changed Dockerfile unless the image tag also changes.

## Artifact trust boundary

The release evidence must describe each artifact independently:

- Binary archives have a GoReleaser-generated SHA-256 checksum file.
- The PostgreSQL + AGE build explicitly enables BuildKit SBOM and
  provenance attestations, and the release verifies their manifests.
- The application image, Helm chart, and binary archives do not currently
  have a KubeAtlas release job that creates Cosign signatures. Do not call
  them signed.
- Do not claim an application-image or binary SBOM/provenance guarantee
  until the workflow explicitly enables it and a public release audit
  verifies the resulting attestations.

The unsigned application artifacts are a documented current limitation,
not evidence that signature verification for separately distributed Rego
rule packs is disabled. Those are different release pipelines.

## Required order

1. Run `Release preflight` for `v1.5.2` on the exact `main` commit. It
   builds the Web bundle, binaries, archives, checksums, local snapshot
   images, and Helm package without registry login or publication.
2. Merge only after ordinary CI, Tier 2 upgrade/lifecycle E2E, snapshot
   E2E, and release preflight are required and green on the protected
   default branch.
3. Push the signed `v1.5.2` tag. The release workflow reuses the immutable
   PostgreSQL image when it already exists, while the core release publishes
   in parallel; Helm packaging waits for both.
4. Keep the GitHub Release in draft state while a clean environment
   anonymously pulls and verifies the binaries, images, and chart.
5. Run the rule repository distribution audit. It must verify and load
   all 14 formal packs, not merely find their tags.
6. Publish and smoke-test Action `v1.0.1`; move `v1` only after that
   immutable tag succeeds.
7. Update Krew and the Headlamp catalog using checksums from already
   published immutable archives. Verify the Backstage npm version. Track each
   integration independently when an external review remains pending.
8. Update version-pinned installation commands only after the matching
   chart is publicly pullable. This keeps documentation executable at
   every point in the release.
9. Publish the draft GitHub Release and core announcement only when every core
   release row has evidence. Announce an independently versioned integration
   only after its own row is complete.

The GitHub Release being a draft does not make the tag run a dry run:
the versioned application image and Helm chart are already public by that
point, and the application workflow also updates its moving `latest` tag.
Creating the Git tag is therefore the production publication decision.

If any row fails, stop the release and preserve its logs and diagnostic
artifacts. A local Kind result is useful evidence but never substitutes
for the required protected-branch workflow.

## Default-branch protection

The five Lithastra-owned repositories require pull requests, DCO,
CODEOWNERS review, passing CI, and blocked force-pushes. For the core
repository, Tier 2 and snapshot E2E are required checks rather than
optional evidence. Rulesets are repository settings and must be audited
after workflow job names change; a workflow file alone cannot make its
own check mandatory.
