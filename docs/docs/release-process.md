---
sidebar_position: 8
title: Release process
---

# Release process

KubeAtlas is released as a set of independently versioned products.
A core Git tag is therefore the start of a release, not proof that the
whole release is usable. The release owner closes the matrix below with
public-download evidence before announcing the version.

## v1.5.1 compatibility matrix

| Component | Required version | Distribution | Required evidence |
|---|---:|---|---|
| Core server and CLI | `v1.5.1` | GitHub Release | Checksums match every archive; binaries report `1.5.1` |
| Server image | `1.5.1` | `ghcr.io/lithastra/kubeatlas` | Anonymous multi-architecture manifest pull |
| PostgreSQL + AGE image | `16.6-age1.6.0-rc0.1` | `ghcr.io/lithastra/postgres-age` | Anonymous amd64 and arm64 manifest pull; SBOM and provenance attestations present |
| Helm chart | `1.5.1` | `oci://ghcr.io/lithastra/charts/kubeatlas` | Anonymous pull, lint, and clean-cluster install |
| Rule packs | Versions in each `metadata.yaml` | `ghcr.io/lithastra/rules/<pack>` | 14 formal packs resolve, verify against the release-workflow OIDC identity, pull, and load against their samples |
| GitHub Action | `v1.0.1` and moving `v1` | `lithastra/kubeatlas-action` | Real release download, checksum, extraction, execution, and temporary-directory cleanup |
| Krew manifest | `v1.5.1` | `kubernetes-sigs/krew-index` | All platform URLs and SHA256 values validate |
| Headlamp plugin | `1.2.0` | `headlamp-k8s/plugins` | Catalog metadata, archive checksum, tests, and production build agree |
| Backstage plugin | `1.0.0` | npm | Anonymous install and production build |
| Documentation | `1.5.1` current; `1.5.0` snapshot | Documentation site | Production build has no broken links or anchors; install commands resolve publicly |

Apache AGE does not publish a GA `1.6.0` tag for PostgreSQL 16. The
database image therefore names the upstream `1.6.0-rc0` dependency
explicitly and pins its Git commit in the Dockerfile. The final `.1`
is the KubeAtlas image recipe revision. Reusing this tag for different
bytes is not allowed.

## Required order

1. Merge only after the ordinary CI, Tier 2 upgrade/lifecycle E2E, and
   snapshot E2E checks are required and green on the protected default
   branch.
2. Push the signed `v1.5.1` tag. The release workflow publishes the
   PostgreSQL image and core release in parallel; Helm packaging waits
   for both.
3. Keep the GitHub Release in draft state while a clean environment
   anonymously pulls and verifies the binaries, images, and chart.
4. Run the rule repository distribution audit. It must verify and load
   all 14 formal packs, not merely find their tags.
5. Publish and smoke-test Action `v1.0.1`; move `v1` only after that
   immutable tag succeeds.
6. Update Krew and the Headlamp catalog using checksums from already
   published immutable archives. Verify the Backstage npm version.
7. Update version-pinned installation commands only after the matching
   chart is publicly pullable. This keeps documentation executable at
   every point in the release.
8. Publish the draft GitHub Release and announcement only when every
   matrix row has evidence.

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
