#!/usr/bin/env bash

# Audit the exact public core release artifacts from an anonymous registry
# client and a clean Kubernetes cluster. The workflow prepares an empty Docker
# credential directory before invoking this script.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
RELEASE_TAG=${KUBEATLAS_RELEASE_TAG:-}
RELEASE_COMMIT=${KUBEATLAS_RELEASE_COMMIT:-}
EVIDENCE_FILE=${KUBEATLAS_ARTIFACT_EVIDENCE_FILE:-/tmp/kubeatlas-core-artifact-audit.json}
RELEASE_REPOSITORY=ghcr.io/lithastra/kubeatlas
CHART_REPOSITORY=ghcr.io/lithastra/charts/kubeatlas
EXPECTED_ISSUER=https://token.actions.githubusercontent.com
EXPECTED_GITHUB_REPOSITORY=lithastra/kubeatlas
AUDIT_NAMESPACE=kubeatlas-core-artifact-audit
AUDIT_RELEASE=kubeatlas-core-artifact-audit

APP_DIGEST=""
DATABASE_DIGEST=""
CHART_DIGEST=""
AUDIT_STATUS=failed

fail() {
  printf 'core artifact audit: %s\n' "$*" >&2
  exit 1
}

for required_command in cosign oras jq helm kubectl docker; do
  command -v "$required_command" >/dev/null || fail "$required_command is required"
done

[[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] ||
  fail "invalid release tag"
[[ "$RELEASE_COMMIT" =~ ^[0-9a-f]{40}$ ]] || fail "invalid release commit"

RELEASE_VERSION=${RELEASE_TAG#v}
RELEASE_REF="refs/tags/${RELEASE_TAG}"
CERTIFICATE_IDENTITY="https://github.com/${EXPECTED_GITHUB_REPOSITORY}/.github/workflows/release.yml@${RELEASE_REF}"

# shellcheck disable=SC1091
source "$ROOT_DIR/images/postgres-age/image.env"
DATABASE_TAGGED_REF="${POSTGRES_AGE_REPOSITORY}:${POSTGRES_AGE_TAG}"
APP_TAGGED_REF="${RELEASE_REPOSITORY}:${RELEASE_VERSION}"
CHART_TAGGED_REF="${CHART_REPOSITORY}:${RELEASE_VERSION}"

write_evidence() {
  local evidence_tmp
  mkdir -p "$(dirname "$EVIDENCE_FILE")"
  evidence_tmp="${EVIDENCE_FILE}.tmp"
  jq -n \
    --arg status "$AUDIT_STATUS" \
    --arg releaseTag "$RELEASE_TAG" \
    --arg releaseCommit "$RELEASE_COMMIT" \
    --arg certificateIdentity "$CERTIFICATE_IDENTITY" \
    --arg oidcIssuer "$EXPECTED_ISSUER" \
    --arg githubRepository "$EXPECTED_GITHUB_REPOSITORY" \
    --arg workflowPath ".github/workflows/release.yml" \
    --arg appRepository "$RELEASE_REPOSITORY" \
    --arg appDigest "$APP_DIGEST" \
    --arg databaseRepository "$POSTGRES_AGE_REPOSITORY" \
    --arg databaseDigest "$DATABASE_DIGEST" \
    --arg chartRepository "$CHART_REPOSITORY" \
    --arg chartDigest "$CHART_DIGEST" '
      {
        status: $status,
        release: {tag: $releaseTag, commit: $releaseCommit},
        signer: {
          certificateIdentity: $certificateIdentity,
          oidcIssuer: $oidcIssuer,
          githubRepository: $githubRepository,
          workflowPath: $workflowPath
        },
        artifacts: {
          applicationImage: {repository: $appRepository, digest: $appDigest},
          databaseImage: {repository: $databaseRepository, digest: $databaseDigest},
          helmChart: {repository: $chartRepository, digest: $chartDigest}
        }
      }
    ' >"$evidence_tmp"
  mv "$evidence_tmp" "$EVIDENCE_FILE"
}

finish() {
  local exit_code
  exit_code=$1
  trap - EXIT
  set +e
  helm uninstall "$AUDIT_RELEASE" --namespace "$AUDIT_NAMESPACE" >/dev/null 2>&1
  kubectl delete namespace "$AUDIT_NAMESPACE" --wait=false >/dev/null 2>&1
  if [[ -n "${WORK_DIR:-}" ]]; then
    rm -rf "$WORK_DIR"
  fi
  if [[ "$exit_code" -eq 0 ]]; then
    AUDIT_STATUS=passed
  fi
  if ! write_evidence; then
    exit_code=1
  fi
  exit "$exit_code"
}
trap 'finish $?' EXIT

if [[ "${KUBEATLAS_REQUIRE_ANONYMOUS:-}" == true ]]; then
  [[ -n "${DOCKER_CONFIG:-}" ]] || fail "DOCKER_CONFIG must identify an empty credential directory"
  [[ -f "$DOCKER_CONFIG/config.json" ]] || fail "anonymous Docker config does not exist"
  jq -e '((.auths // {}) | length) == 0' "$DOCKER_CONFIG/config.json" >/dev/null ||
    fail "registry client is not anonymous"
  [[ -n "${HELM_REGISTRY_CONFIG:-}" ]] || fail "HELM_REGISTRY_CONFIG must be isolated"
  [[ -f "$HELM_REGISTRY_CONFIG" ]] || fail "anonymous Helm registry config does not exist"
  jq -e '((.auths // {}) | length) == 0' "$HELM_REGISTRY_CONFIG" >/dev/null ||
    fail "Helm registry client is not anonymous"
fi

WORK_DIR=$(mktemp -d)
write_evidence

APP_DIGEST=$(oras resolve "$APP_TAGGED_REF")
DATABASE_DIGEST=$(oras resolve "$DATABASE_TAGGED_REF")
CHART_DIGEST=$(oras resolve "$CHART_TAGGED_REF")
for digest in "$APP_DIGEST" "$DATABASE_DIGEST" "$CHART_DIGEST"; do
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "registry returned an invalid digest"
done
write_evidence

verify_signature() {
  local artifact_repository
  local artifact_digest
  artifact_repository=$1
  artifact_digest=$2
  cosign verify \
    --certificate-identity "$CERTIFICATE_IDENTITY" \
    --certificate-oidc-issuer "$EXPECTED_ISSUER" \
    --certificate-github-workflow-repository "$EXPECTED_GITHUB_REPOSITORY" \
    --certificate-github-workflow-ref "$RELEASE_REF" \
    --certificate-github-workflow-sha "$RELEASE_COMMIT" \
    -a "release-tag=${RELEASE_TAG}" \
    -a "release-commit=${RELEASE_COMMIT}" \
    "${artifact_repository}@${artifact_digest}" >/dev/null
}

verify_signature "$RELEASE_REPOSITORY" "$APP_DIGEST"
verify_signature "$POSTGRES_AGE_REPOSITORY" "$DATABASE_DIGEST"
verify_signature "$CHART_REPOSITORY" "$CHART_DIGEST"

bash "$ROOT_DIR/test/verify/image-attestations.sh" \
  "$RELEASE_REPOSITORY" "$APP_DIGEST"
bash "$ROOT_DIR/test/verify/image-attestations.sh" \
  "$POSTGRES_AGE_REPOSITORY" "$DATABASE_DIGEST"

# Pull the database image through the deliberately empty Docker credential
# configuration. The application image is pulled independently by the clean
# kind node during the digest-pinned Helm installation below.
docker pull --platform linux/amd64 \
  "${POSTGRES_AGE_REPOSITORY}@${DATABASE_DIGEST}" >/dev/null

helm pull "oci://${CHART_REPOSITORY}" \
  --version "$RELEASE_VERSION" \
  --destination "$WORK_DIR"
mkdir -p "$WORK_DIR/chart"
tar -xzf "$WORK_DIR/kubeatlas-${RELEASE_VERSION}.tgz" -C "$WORK_DIR/chart"

CHART_FILE="$WORK_DIR/chart/kubeatlas/Chart.yaml"
VALUES_FILE="$WORK_DIR/chart/kubeatlas/values.yaml"
chart_version=$(awk '$1 == "version:" { print $2; exit }' "$CHART_FILE")
app_version=$(awk '$1 == "appVersion:" { gsub(/"/, "", $2); print $2; exit }' "$CHART_FILE")
chart_database_image=$(awk '
  /^persistence:/ { in_persistence = 1 }
  in_persistence && /^    image:/ { print $2; exit }
' "$VALUES_FILE")
[[ "$chart_version" == "$RELEASE_VERSION" ]] || fail "chart version does not match release"
[[ "$app_version" == "$RELEASE_VERSION" ]] || fail "chart appVersion does not match release"
[[ "$chart_database_image" == "$DATABASE_TAGGED_REF" ]] ||
  fail "chart database image does not match the audited dependency"

helm lint "$WORK_DIR/chart/kubeatlas"
helm install "$AUDIT_RELEASE" "$WORK_DIR/kubeatlas-${RELEASE_VERSION}.tgz" \
  --namespace "$AUDIT_NAMESPACE" --create-namespace \
  --set-string "image.digest=${APP_DIGEST}" \
  --wait --timeout 5m

kubectl rollout status "deployment/${AUDIT_RELEASE}" \
  --namespace "$AUDIT_NAMESPACE" --timeout=2m
deployed_image=$(kubectl get deployment "$AUDIT_RELEASE" \
  --namespace "$AUDIT_NAMESPACE" \
  -o jsonpath='{.spec.template.spec.containers[0].image}')
[[ "$deployed_image" == "${RELEASE_REPOSITORY}@${APP_DIGEST}" ]] ||
  fail "installed deployment did not use the verified application digest"

printf 'core artifact audit: verified and installed %s from anonymous OCI clients\n' \
  "$RELEASE_TAG"
