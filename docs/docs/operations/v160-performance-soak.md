---
sidebar_position: 2
title: v1.6 performance and 168-hour soak
---

# v1.6 performance and 168-hour soak

This is the release-gating procedure for the unreleased v1.6 candidate. The
repository contains the runner and verifier, but v1.6 must not be described as
having passed until the real evidence from the final candidate verifies.

Run this only on disposable Docker Desktop Kubernetes. The procedure creates
roughly 10K fixture objects, deletes application and PostgreSQL Pods, briefly
interrupts the local kube-apiserver, and performs a destructive database
restore in a separate namespace. It is not a production-cluster runbook.

## Freeze boundary

Choose one clean 40-character Git commit after dependency, migration,
instrumentation, resource-profile, recovery, and release-workflow changes are
complete. Build and load the candidate application and PostgreSQL images, and
record their immutable runtime image IDs. Any later change to those surfaces
invalidates both the performance evidence and the 168-hour run.

The gate requires:

- Kubernetes context exactly `docker-desktop`, on Kubernetes 1.34, 1.35, or
  1.36;
- CloudNativePG 1.30.0 already installed, with KubeAtlas using embedded Tier 2
  and snapshots;
- a clean checkout whose `HEAD` equals `KUBEATLAS_EXPECTED_GIT_SHA`;
- at least 100 samples per performance endpoint;
- the chart-default application resource profile and automatic 75% Go soft
  memory boundary (384MiB at the 512Mi limit) for 5K;
- `test/perf/v160-production-10k-values.yaml`, where the same boundary derives
  1536MiB from the 2Gi limit, for both 10K layouts;
- immutable application and PostgreSQL image IDs; and
- a new evidence directory. Runners refuse to overwrite prior evidence.

## Capture all performance rows

Create the 5K fixture with `test/perf/stress-5k-resources.sh`. For the
representative 10K row, use `test/perf/stress-10k-distributed.sh`; it spreads
the fixture over `stress-test-10k-00` through `stress-test-10k-09`. Retain the
legacy `test/perf/stress-10k-resources.sh` one-namespace layout as a separate
pathological observation.

For each deployment and fixture, set the matching profile, layout, namespace
list, frozen SHA, and one shared output directory before running:

```bash
export KUBEATLAS_EXPECTED_GIT_SHA=<40-character-candidate-commit>
export KUBEATLAS_EVIDENCE_DIR="$PWD/.evidence/v1.6-performance"

# Chart-default application resources with the 5K fixture.
KUBEATLAS_PERF_PROFILE=default-5k \
KUBEATLAS_PERF_LAYOUT=single-large-namespace \
KUBEATLAS_FIXTURE_NAMESPACES=stress-test-5k \
  bash test/perf/v160-performance-gate.sh

# Documented production resources with the distributed 10K fixture.
KUBEATLAS_PERF_PROFILE=production-10k \
KUBEATLAS_PERF_LAYOUT=distributed \
KUBEATLAS_FIXTURE_NAMESPACES="$(printf 'stress-test-10k-%02d,' {0..8})stress-test-10k-09" \
  bash test/perf/v160-performance-gate.sh

# The same production resources with the single-namespace pathological fixture.
KUBEATLAS_PERF_PROFILE=production-10k \
KUBEATLAS_PERF_LAYOUT=single-large-namespace \
KUBEATLAS_FIXTURE_NAMESPACES=stress-test-10k \
  bash test/perf/v160-performance-gate.sh

bash test/verify/v160-performance-evidence.sh \
  "$KUBEATLAS_EVIDENCE_DIR/default-5k-single-large-namespace.json" \
  "$KUBEATLAS_EVIDENCE_DIR/production-10k-distributed.json" \
  "$KUBEATLAS_EVIDENCE_DIR/production-10k-single-large-namespace.json"
```

Do not average rows, discard endpoint errors, or replace the distributed 10K
namespace result with the pathological result. The verifier requires the
complete set and one candidate SHA.

## Start the 168-hour run

Leave the production 10K distributed fixture and production resource profile
running. Use local candidate image tags that the final v1.5.2 upgrade/restore
drill can install with `imagePullPolicy=Never`.

```bash
KUBEATLAS_CONFIRM_168H_SOAK=docker-desktop \
KUBEATLAS_EXPECTED_GIT_SHA="$KUBEATLAS_EXPECTED_GIT_SHA" \
KUBEATLAS_PERFORMANCE_EVIDENCE_DIR="$KUBEATLAS_EVIDENCE_DIR" \
KUBEATLAS_SOAK_EVIDENCE_DIR="$PWD/.evidence/v1.6-soak" \
KUBEATLAS_CANDIDATE_IMAGE=kubeatlas:v1.6.0-candidate \
KUBEATLAS_CANDIDATE_PG_IMAGE=local-postgres-age:16.15-age1.6.0-rc0.2 \
  bash test/soak/v160-soak.sh
```

The runner samples every five minutes and fails closed. Hours 0–24 are warmup;
hours 24–48 are the stable baseline. It continuously mutates a canary
ConfigMap, queries cluster, namespace, and blast-radius endpoints, observes
scheduled snapshots, and records only bounded numeric/status evidence. It
schedules one application restart, resource storm, snapshot-writer storm,
PostgreSQL interruption, Docker Desktop API-server interruption, and—only when
OTel is enabled—receiver overload. The final event reruns the public v1.5.2 to
candidate upgrade and destructive backup/delete/restore proof.

The documented production profile leaves OTel disabled. If the candidate
enables it, also set `KUBEATLAS_TELEMETRYGEN_IMAGE` to an immutable
`repository@sha256:...` telemetrygen image; a mutable `latest` tag is rejected
for release evidence.

The API-server event never stops the whole node container. It verifies the
exact Docker Desktop context and `kindest/node` image, refuses an existing hold
file, temporarily moves only `/etc/kubernetes/manifests/kube-apiserver.yaml`,
probes KubeAtlas from inside the node, and restores the manifest through an
EXIT trap.

## Pass conditions and retained evidence

`test/verify/v160-soak-evidence.sh` requires continuous coverage for at least
604,800 seconds, no sample gap over twice the configured interval, all planned
events, dependency recovery within 120 seconds, exactly one planned Pod
replacement, no container restart or OOM, no normal-load event/snapshot/OTel
drops, healthy normal samples, and no sustained p95 growth over the frozen 20%
threshold. A failure is not resumable; fix the cause, freeze a new candidate,
repeat all performance rows, and start a new seven-day run.

A random Secret sentinel is kept only in the source Kubernetes Secret and in
runner memory. Live API surfaces are scanned with every sample; logs,
PostgreSQL, and exact fixture counts are checked at least every six hours and
after every planned event. Event logs and every retained artifact are also
scanned; the raw value is never written to the evidence directory. Other
cluster metadata is retained because it is needed to reproduce and audit the
result. The manifest stores only the sentinel SHA-256, scan count, artifact
hashes, candidate identity, environment, and bounded results.

After the verifier passes, preserve the evidence outside the repository and
link its immutable location from the release issue. Do not commit local
evidence, database dumps, kubeconfigs, credentials, tokens, or Secret values.
