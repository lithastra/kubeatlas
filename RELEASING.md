# Releasing KubeAtlas

Maintainer checklist for future releases and post-release documentation.
Use the actual candidate version throughout; the published v1.6.0 source is
`69609e320045cbd943185dfa7900c3302359999e`. Its artifacts, evidence, and limits
are recorded in the [release matrix](docs/docs/release-process.md).
Later `main` commits and documentation corrections do not change that release.

## 1. Prepare and freeze the candidate

1. Prepare version, Chart, dependency-recipe, and CHANGELOG changes in a PR.
   Use Conventional Commits with DCO sign-off; do not bypass review by pushing
   release changes directly to `main`.
2. State the supported Kubernetes matrix, authentication and replica limits,
   validation duration, and explicitly untested areas before validation.
   A one-hour functional check does not establish endurance, performance
   acceptance, HA, or general production reliability.
3. Check the release contract, Chart metadata, documentation, and all CI gates.
   A changed database image recipe needs a new immutable recipe tag.
   Candidate docs must not promote installation commands for unpublished
   artifacts. Retain historical version snapshots and upgrade paths.
4. Merge the approved PR, freeze the exact source SHA, and run the full manual
   [Release preflight](.github/workflows/release-preflight.yml) on that SHA.
   Record the resolved workflow SHA and all job results; a PR build-only
   preflight is not the complete release rehearsal.
5. Confirm the working tree is clean, the intended source equals the reviewed
   commit, and validation evidence belongs to it. Do not combine observations
   from failed or superseded candidates. Any source change requires reviewing
   which gates must run again.

Run checks from this repository, not a parent multi-repository workspace:

```bash
go test ./...
go run ./tools/api-compat-check
helm unittest helm/kubeatlas
(cd web && npm ci && npm run typecheck && npm run lint && npm test && npm run build)
(cd docs && npm ci && npm run typecheck && npm run test:dependencies && npm run build)
make changelog-extract VERSION=vX.Y.Z
```

Use the workflow's pinned toolchain and explicit candidate ref for the manual
preflight. Confirm the run's `headSha` matches the frozen source, not simply that
the latest run is green. Keep workstation-specific runners, credentials,
database backups, and private evidence outside the public repository.

## 2. Authorize and push the signed tag

Tag creation and publication require an explicit release decision. Create an
annotated, cryptographically signed tag at the frozen commit, verify its
signature and target, then push only that tag. DCO sign-off is not a tag
signature; do not silently downgrade to an unsigned tag.

The [release workflow](.github/workflows/release.yml) publishes OCI artifacts
before the GitHub Release becomes public. Pushing the tag is therefore a
publication action, not a reversible draft preview.

The workflow:

1. Enforces release metadata and the immutable database-recipe contract.
2. Reuses and verifies an existing PostgreSQL + AGE recipe image, or publishes
   it once if absent. Registry errors fail closed; they are not absence.
3. Builds the server and CLI archives, checksums, application image, and draft
   GitHub Release through GoReleaser.
4. Publishes the Helm OCI chart after the image jobs succeed.
5. Keyless-signs the application, database, and Chart by immutable digest.
   Both runtime images have per-platform SPDX SBOM and SLSA provenance checks.
6. Independently audits anonymous pulls, exact-release signatures and
   attestations, and a clean-cluster install.

Match each signature's workflow identity, issuer, repository, tag, source SHA,
annotations, and digest. A valid signature from an unrelated identity is not
sufficient. The standalone archives remain checksum-verified but unsigned;
OCI signatures do not cover their contents.

## 3. Audit and publish the draft

Inspect every publishing and audit job for the exact tag. Record the signed
tag object, source SHA, numeric Release ID, immutable OCI digests, asset names,
checksums, and validation limits. Draft lookup must use an authorized draft
listing or numeric ID; a published-tag endpoint returning 404 does not prove
the draft is missing.

Before promoting the draft:

- Verify archive inventories, embedded versions and source identities,
  platform architectures, and checksums. Distinguish static inspection from
  executing a binary on its target operating system.
- Verify anonymous Chart/image access and the digest-bound trust policy.
  Use fresh temporary registry configuration; never delete or log out the
  operator's ordinary Helm/Docker credentials to simulate anonymity.
- Verify the declared install and recovery scope in an explicitly selected,
  disposable cluster. Record its context and ownership before creating or
  cleaning resources; never assume the current kubeconfig context is disposable.
- Review the public release notes against actual evidence. Local source-build
  validation does not substitute for testing the distributed artifacts.
- Obtain approval to publish the draft. After publication, independently
  download every archive anonymously and verify its recorded checksum.

Use [the artifact verification policy](https://docs.kubeatlas.lithastra.com/release-process#verifying-a-v16-core-candidate)
and [recovery guidance](docs/docs/installation/persistence.md), not a successful
workflow status alone. A published release may still need a clearly reported
post-publication download or deployment check.

Do not delete, move, or overwrite a published tag or artifact. Investigate a
failure before retrying any publishing job. If source or artifact bytes must
change, fix forward through review and a new semantic patch release, not a
retag or a build-metadata suffix that reuses an existing version.

## 4. Deliver independently versioned integrations

- **Krew:** update `plugins/atlas.yaml` with all six published CLI URLs and
  checksums. Validate it in an isolated `KREW_ROOT` without replacing the
  operator's installed plugin. Submit the upstream index PR and distinguish
  submission from merge and official-index availability.
- **Headlamp:** verify the standalone plugin release separately from upstream
  catalog availability. A pending catalog PR is not a published catalog entry.
- **Action and rule packs:** retain their own versions and record which core
  version their compatibility evidence actually exercises.
- **Backstage:** follow the current delivery decision. v1.6.0 closeout uses the
  fixed 1.0.1 source-only delivery; do not imply npm 1.0.0 contains that fix or
  publish a package without a separate decision.

Core release success does not automatically publish or validate these products.
Use the [integration matrix](docs/docs/release-process.md#independently-versioned-integrations)
to record facts, deferred work, and compatibility gaps independently.

## 5. Close out documentation after public verification

1. Update README, CHANGELOG, current docs, and the relevant release snapshot
   to the actual publication date, source identity, verified artifacts, and
   accepted validation scope. Keep `Unreleased` changes separate from the
   already published tag.
2. Promote the verified stable version in `docs/docusaurus.config.ts`.
   The stable version serves at `/`; development docs serve at `/next/`
   with an unreleased banner. Keep the version dropdown available.
3. Pin fresh-install examples to the published stable version. Do not blindly
   replace older versions in migration sequences, historical releases, or
   CloudNativePG/Kubernetes prerequisites. Do not create a historical snapshot
   for every patch release.
4. Preserve inbound links. While 1.6.0 occupies the root, temporary Cloudflare
   Pages aliases in `docs/static/_redirects` map `/1.6.0/*` to the same stable
   page. When the next stable version takes the root, remove those aliases:
   Docusaurus must then serve historical 1.6.0 content at its original paths.
5. Run the docs typecheck, dependency tests, and production build. The build
   runs portable regression tests against generated release routes and
   content. Review the stable, next, historical, and upgrade pages in a browser.
6. Merge the reviewed docs PR after its final checks pass. Verify the existing
   Cloudflare Pages deployment at
   [docs.kubeatlas.lithastra.com](https://docs.kubeatlas.lithastra.com):
   version labels, installation pins, old links, and release evidence must
   match the merged content. CI build success is not deployment proof.

Do not assume a tag deploys the docs or create a separate hosting project.
If the existing deployment fails, report that boundary and diagnose it before
changing hosting configuration. Documentation-only closeout does not rebuild
images, replace release assets, create a new tag, or require a new core release.
