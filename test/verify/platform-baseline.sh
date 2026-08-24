#!/usr/bin/env bash

# Verify that the v1.6 platform and persistence dependency baseline stays
# aligned across Helm metadata, image recipes, CI, and current documentation.
# Historical release notes and versioned docs are intentionally out of scope.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$ROOT_DIR"

fail() {
  printf 'platform baseline: %s\n' "$*" >&2
  exit 1
}

require_text() {
  local file=$1
  local expected=$2
  grep -Fq -- "$expected" "$file" ||
    fail "$file is missing: $expected"
}

EXPECTED_KUBE_RANGE='>=1.34.0-0 <1.37.0-0'
EXPECTED_CNPG_CHART=0.29.0
EXPECTED_CNPG_OPERATOR=1.30.0
EXPECTED_POSTGRES_AGE_TAG=16.15-age1.6.0-rc0.2
EXPECTED_CNPG_IMAGE='ghcr.io/cloudnative-pg/postgresql:16.15-standard-trixie@sha256:777ad6774e00fff6514c7e9bd6e892fe148c5db2845532a8f456e42317bfc2d2'
EXPECTED_AGE_VERSION=PG16/v1.6.0-rc0
EXPECTED_AGE_COMMIT=2db2f060c4c9265a14d40f007eb8c56febf31e4c

chart_kube_range=$(awk -F'"' '$1 ~ /^kubeVersion:/ { print $2; exit }' helm/kubeatlas/Chart.yaml)
[[ "$chart_kube_range" == "$EXPECTED_KUBE_RANGE" ]] ||
  fail "Helm kubeVersion is $chart_kube_range, expected $EXPECTED_KUBE_RANGE"

# shellcheck disable=SC1091
source images/postgres-age/image.env
[[ "$POSTGRES_AGE_TAG" == "$EXPECTED_POSTGRES_AGE_TAG" ]] ||
  fail "image.env tag is $POSTGRES_AGE_TAG, expected $EXPECTED_POSTGRES_AGE_TAG"

chart_postgres_age_image=$(awk '
  /^persistence:/ { in_persistence = 1 }
  in_persistence && /^    image:/ { print $2; exit }
' helm/kubeatlas/values.yaml)
expected_postgres_age_image="${POSTGRES_AGE_REPOSITORY}:${EXPECTED_POSTGRES_AGE_TAG}"
[[ "$chart_postgres_age_image" == "$expected_postgres_age_image" ]] ||
  fail "Helm image is $chart_postgres_age_image, expected $expected_postgres_age_image"

require_text images/postgres-age/Dockerfile "ARG CNPG_IMAGE=${EXPECTED_CNPG_IMAGE}"
require_text images/postgres-age/Dockerfile "ARG AGE_VERSION=${EXPECTED_AGE_VERSION}"
require_text images/postgres-age/Dockerfile "ARG AGE_COMMIT=${EXPECTED_AGE_COMMIT}"
require_text images/postgres-age/Dockerfile 'LABEL io.kubeatlas.postgresql.version="16.15"'
require_text images/postgres-age/Dockerfile 'LABEL io.kubeatlas.apache-age.version="${AGE_VERSION}"'
require_text images/postgres-age/Dockerfile 'LABEL io.kubeatlas.apache-age.commit="${AGE_COMMIT}"'

tier2_workflow=.github/workflows/e2e-kind-tier2.yml
require_text "$tier2_workflow" 'version: v0.32.0'
require_text "$tier2_workflow" 'kindest/node:v1.34.8@sha256:02722c2dedddcfc00febf5d27fbeb9b7b2c14294c82109ff4a85d89ac9ba3256'
require_text "$tier2_workflow" 'kindest/node:v1.35.5@sha256:ce977ae6d65918d0b58a5f8b5e940429c2ce42fa3a5619ec2bbc60b949c0ac95'
require_text "$tier2_workflow" 'kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5'
require_text "$tier2_workflow" "--version ${EXPECTED_CNPG_CHART}"
require_text "$tier2_workflow" "expected_operator: ${EXPECTED_CNPG_OPERATOR}"
require_text "$tier2_workflow" "local-postgres-age:${EXPECTED_POSTGRES_AGE_TAG}"
require_text "$tier2_workflow" 'name: PostgreSQL + AGE (linux/${{ matrix.arch }})'
require_text "$tier2_workflow" '--platform "linux/${TARGET_ARCH}"'
require_text "$tier2_workflow" 'name: Public v1.5.2 upgrade + embedded restore'
require_text "$tier2_workflow" 'bash test/verify/v160-upgrade-recovery.sh'
require_text "$tier2_workflow" '- v152-upgrade-recovery'
require_text "$tier2_workflow" '- postgres-age-architectures'

recovery_verifier=test/verify/v160-upgrade-recovery.sh
require_text "$recovery_verifier" 'PUBLIC_VERSION=1.5.2'
require_text "$recovery_verifier" 'pg_dump -Fc'
require_text "$recovery_verifier" 'pg_restore --clean --if-exists --exit-on-error'
require_text "$recovery_verifier" 'Secret values and runtime credentials are absent from every gated surface'

for workflow in \
  .github/workflows/e2e.yml \
  .github/workflows/e2e-kind-snapshots.yml \
  .github/workflows/telemetry-opt-in.yml; do
  require_text "$workflow" 'version: v0.32.0'
  require_text "$workflow" 'kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5'
done

for workflow in \
  .github/workflows/e2e-kind-snapshots.yml \
  .github/workflows/e2e-openshift-local.yml; do
  require_text "$workflow" "--version ${EXPECTED_CNPG_CHART}"
  require_text "$workflow" "16.15-age1.6.0-rc0.2"
done

require_text docs/docs/quick-start.md 'Kubernetes 1.34–1.36'
require_text docs/docs/installation/persistence.md 'Current `main` / planned v1.6'
require_text docs/docs/installation/persistence.md '0.29.0→1.30.0'
require_text docs/docs/installation/helm.md "CloudNativePG chart ${EXPECTED_CNPG_CHART} / operator ${EXPECTED_CNPG_OPERATOR}"
require_text docs/docusaurus.config.ts "label: 'Next (v1.6)'"
require_text docs/versions.json '"1.5.2"'

for kubernetes_version in 1.34.0 1.35.0 1.36.0; do
  helm template kubeatlas helm/kubeatlas \
    --kube-version "$kubernetes_version" >/dev/null ||
    fail "Helm rejected supported Kubernetes $kubernetes_version"
done

for kubernetes_version in 1.33.0 1.37.0; do
  if helm template kubeatlas helm/kubeatlas \
    --kube-version "$kubernetes_version" >/dev/null 2>&1; then
    fail "Helm accepted unsupported Kubernetes $kubernetes_version"
  fi
done

rendered_tier2=$(helm template kubeatlas helm/kubeatlas \
  --kube-version 1.36.0 \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true)
grep -Fq -- "imageName: \"${expected_postgres_age_image}\"" <<<"$rendered_tier2" ||
  fail "rendered embedded Cluster does not use $expected_postgres_age_image"

printf 'platform baseline: Kubernetes %s\n' "$EXPECTED_KUBE_RANGE"
printf 'platform baseline: CloudNativePG chart %s / operator %s\n' \
  "$EXPECTED_CNPG_CHART" "$EXPECTED_CNPG_OPERATOR"
printf 'platform baseline: PostgreSQL + AGE image %s\n' "$expected_postgres_age_image"
