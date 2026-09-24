---
sidebar_position: 3
title: One-hour release functional validation
---

# One-hour release functional validation

## Explicit, bounded release-owner decision

On 2026-09-24, the release owner approved **one hour of functional validation,
not another complete 72-hour soak**, for the v1.6.0 release-metadata promotion.
This exception applies only after the successful performance and complete
72-hour run of `1ad2ff430b014cd9ddf67e996e448764ed0d3601` on September 21–24,
including final upgrade/destructive restore/cleanup and independent verification.

Keep that evidence unchanged and separately identified. The final candidate
must be recorded with its own Git commit, binary version, image identities,
rendered Chart identity, start/end times, bounded results, and artifact hashes.
An hour on the final candidate is **functional validation**, not endurance
validation or a 72-hour claim for that commit. Public signing, attestations,
manual platform preflight, and anonymous installation gates remain required.

## Allowed delta

Before building, compare the final candidate with the completed-soak commit:

- Only `version` and `appVersion` may change in `helm/kubeatlas/Chart.yaml`.
- Release notes and documentation, including generated versioned documentation
  and documentation navigation/configuration, may change.
- The release owner also approved the test-only correction in `phase2.sh`
  and its Deployment-Pod selection helper/regression test: historical snapshot
  Jobs must not be mistaken for replacement application Pods. Recovery
  deadlines and runtime behavior remain unchanged. This is not a change to
  the original 72-hour runner or its evidence verifier.
- Application/CLI/frontend code, dependency locks, migrations, Chart templates
  and values, resource profiles, database recipe, build/publishing workflows,
  and the existing 72-hour runner/verifiers must remain byte-identical.
- The existing PostgreSQL image and database/PVC remain unchanged. Build the
  application for the final commit using the recorded runtime base and verify
  the embedded frontend against the earlier build; version/commit stamps are
  expected to differ. A local image is not a published production artifact.

If the actual delta exceeds this list, stop and ask the release owner for a
new scope decision. Do not silently extend this exception or start another
72-hour run. Keep the helper scripts and their hashes with the new functional
evidence, outside the original completed-run directory.

## Functional procedure

Use only the disposable `docker-desktop` environment. Do not use this
procedure on a production cluster.

1. Verify the completed baseline evidence again. Validate `v1.6.0` Chart and
   CHANGELOG metadata, lint/render the Chart, build the documentation, and
   verify the allowed source delta before freezing the new commit.
2. Build and load the final local candidate, install the updated Chart with
   the existing production profile, and wait for Ready and graph sync. Preserve
   the database and its immutable image; do not run destructive restore or
   control-plane/database fault injection for this short validation.
3. Start the clock at the first successful functional sample, not at launcher
   or deployment time. Sample at least once per minute for at least 3,600
   monotonic seconds; require at least 61 samples and no gap above 120 seconds.
4. Check runtime version/commit, application/database Pod identity and Ready,
   restarts/OOM, live/ready endpoints, graph/namespace/blast-radius queries,
   diagnosis, snapshot listing, policy and telemetry surfaces, the Web entry,
   process memory/GC, goroutines, queue depth and failure/drop counter deltas.
   Exercise a run-owned ConfigMap and prove its change reaches the API and
   snapshot history. Persist only bounded numeric/status receipts, not API bodies.
5. Keep a synthetic Secret sentinel only in memory and its run-owned source
   Secret. Never read existing Kubernetes Secret values or retain the sentinel.
   Check the live surfaces and bounded logs in memory; verify database safety
   through aggregate invariant counts, not database dumps or stored values.
6. Require zero unexpected Pod replacements, new restarts/OOM, endpoint
   failures, queue drops, or snapshot write failures during the timed window.
   Record memory diagnostics without pretending one hour establishes the
   historical warmup/baseline/growth-window leak gate. OTel remains disabled.
7. Clean up only the run-owned canary and sentinel, and stop owned forwarding
   processes. Preserve all evidence on failure. Verify duration, identity,
   sample coverage, functional assertions, cleanup, and file hashes independently
   before recording a pass. Failure is not resumable or silently retried.

This short run does not repeat the completed baseline's destructive restore,
database/control-plane interruptions, enabled-OTel overload, or long-window
memory-growth proof. Release notes must preserve those coverage limits.

## Release handoff

Keep the protected-branch CI and exact-final-commit manual `Release preflight`
requirements. If a PR is squash-merged to a different SHA, run the one-hour
functional check on that merged SHA rather than relabeling a branch result.
Do not create a signed tag or publish without the release owner's separate
publication authorization. A tag publishes images and the Chart even while
the GitHub Release is a draft.
