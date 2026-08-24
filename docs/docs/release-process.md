---
sidebar_position: 8
title: Release process
---

# Release process

KubeAtlas is released as a set of independently versioned products.
A core Git tag is therefore the start of a release, not proof that the
whole release is usable. The release owner closes the matrix below with
public-download evidence before announcing the version.

For the planned v1.6 release, the manual `Release preflight` has two required
parts: build all candidate artifacts without publishing, and run the exact
candidate across the frozen vanilla Kubernetes 1.34, 1.35, and 1.36 Tier 2
matrix. A build-only result is not a release result. After publication, the
weekly public clean-cluster workflow anonymously verifies the current GitHub
binary/checksum, public OCI Chart, versioned documentation, and clean Tier 1
and Tier 2 installs. See [Signals, alerts, and recovery](./operations/runbooks.md).

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
- The published v1.5.2 application image, Helm chart, and binary archives do
  not have KubeAtlas Cosign signatures. Do not retroactively call them signed.
- The v1.6 release workflow is configured to keyless-sign the application
  image, PostgreSQL + AGE image, and Helm OCI chart by immutable digest. It
  also explicitly creates and verifies per-platform application-image and
  database-image SPDX SBOM and SLSA provenance statements.
- Configuration is not public evidence. Do not call a v1.6 artifact signed or
  attested until its tag workflow and anonymous clean-cluster audit pass for
  the exact candidate commit and digests that completed the 168-hour gate.
- Binary archives remain checksum-verified but unsigned. Their contents are
  not covered by the OCI image signatures.

The v1.5.2 unsigned application artifacts are a documented historical
limitation, not evidence that signature verification for separately
distributed Rego rule packs is disabled. Those are different release
pipelines.

## Verifying a v1.6 core candidate

The release audit runs these checks from a fresh job with an empty registry
credential configuration. An operator can repeat them after replacing the
example tag and commit with the values in the retained
`core-artifact-audit-<tag>` workflow artifact:

```bash
tag=v1.6.0
commit=<40-character-commit-from-the-audit-evidence>
version=${tag#v}
issuer=https://token.actions.githubusercontent.com
identity="https://github.com/lithastra/kubeatlas/.github/workflows/release.yml@refs/tags/${tag}"

app_repository=ghcr.io/lithastra/kubeatlas
database_repository=ghcr.io/lithastra/postgres-age
chart_repository=ghcr.io/lithastra/charts/kubeatlas
database_tag=16.15-age1.6.0-rc0.2

app_digest=$(oras resolve "${app_repository}:${version}")
database_digest=$(oras resolve "${database_repository}:${database_tag}")
chart_digest=$(oras resolve "${chart_repository}:${version}")

for artifact in \
  "${app_repository}@${app_digest}" \
  "${database_repository}@${database_digest}" \
  "${chart_repository}@${chart_digest}"; do
  cosign verify \
    --certificate-identity "${identity}" \
    --certificate-oidc-issuer "${issuer}" \
    --certificate-github-workflow-repository lithastra/kubeatlas \
    --certificate-github-workflow-ref "refs/tags/${tag}" \
    --certificate-github-workflow-sha "${commit}" \
    -a "release-tag=${tag}" \
    -a "release-commit=${commit}" \
    "${artifact}"
done
```

Checking only that *some* valid Sigstore signature exists is insufficient. The
identity, GitHub repository, tag ref, commit SHA, signed annotations, and
immutable digest are all part of the verification policy. The retained audit
also inspects both image indexes and proves that their linux/amd64 and
linux/arm64 manifests each have SPDX and SLSA statements bound to that exact
platform digest.

After verification, pin the application image digest during installation so a
later tag lookup is not introduced between verification and deployment:

```bash
helm upgrade --install kubeatlas \
  oci://ghcr.io/lithastra/charts/kubeatlas \
  --version "${version}" \
  --namespace kubeatlas --create-namespace \
  --set-string "image.digest=${app_digest}"
```

The Helm chart is still selected by version for Helm client compatibility, but
the operator should verify its resolved OCI digest immediately before the
install. The release audit performs the same resolution, then installs the
chart with the verified application-image digest on a clean kind cluster.

## v1.6 trust-gate order

1. Freeze the release workflow, runtime images, Chart, dependencies, recovery
   behavior, and instrumentation.
2. Complete the performance gates and 168 continuous hours on that exact
   candidate commit. A later change to those surfaces invalidates the run.
3. Run the manual frozen candidate preflight on the same commit.
4. Create the signed release tag. The tag workflow publishes the database and
   application image indexes with attestations, creates a draft GitHub Release,
   pushes the Chart, and keyless-signs all three OCI digests.
5. Keep the GitHub Release draft. A separate no-package-permission job creates
   an empty registry credential configuration, resolves the public artifacts,
   verifies exact workflow/ref/SHA signatures, validates per-platform
   attestations, anonymously pulls the database image and Chart, and installs
   the digest-pinned application on clean Kubernetes.
6. Preserve the bounded JSON audit artifact even on failure. Publish the draft
   release only when the clean audit and every other core row are green.

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
