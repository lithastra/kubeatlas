#!/usr/bin/env bash

# Keyless-sign one immutable core OCI artifact and immediately verify the
# exact release-workflow identity, tag, commit, repository, and digest.

set -euo pipefail

ARTIFACT_REPOSITORY=${1:-}
ARTIFACT_DIGEST=${2:-}
EXPECTED_WORKFLOW_PATH=.github/workflows/release.yml
EXPECTED_ISSUER=https://token.actions.githubusercontent.com

fail() {
  printf 'core artifact signing: %s\n' "$*" >&2
  exit 1
}

[[ "${GITHUB_ACTIONS:-}" == true ]] || fail "must run in GitHub Actions"
[[ "${GITHUB_REPOSITORY:-}" == lithastra/kubeatlas ]] ||
  fail "unexpected repository"
[[ "${GITHUB_REF:-}" =~ ^refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] ||
  fail "expected a release tag ref"
[[ "${GITHUB_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || fail "invalid release commit"
[[ "$ARTIFACT_REPOSITORY" =~ ^ghcr\.io/lithastra/[a-z0-9._/-]+$ ]] ||
  fail "invalid artifact repository"
[[ "$ARTIFACT_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] ||
  fail "invalid artifact digest"

EXPECTED_WORKFLOW_REF="${GITHUB_REPOSITORY}/${EXPECTED_WORKFLOW_PATH}@${GITHUB_REF}"
[[ "${GITHUB_WORKFLOW_REF:-}" == "$EXPECTED_WORKFLOW_REF" ]] ||
  fail "unexpected workflow identity"

RELEASE_TAG=${GITHUB_REF#refs/tags/}
IMMUTABLE_REF="${ARTIFACT_REPOSITORY}@${ARTIFACT_DIGEST}"
CERTIFICATE_IDENTITY="https://github.com/${EXPECTED_WORKFLOW_REF}"

cosign sign --yes \
  -a "release-tag=${RELEASE_TAG}" \
  -a "release-commit=${GITHUB_SHA}" \
  "$IMMUTABLE_REF"

cosign verify \
  --certificate-identity "$CERTIFICATE_IDENTITY" \
  --certificate-oidc-issuer "$EXPECTED_ISSUER" \
  --certificate-github-workflow-repository "$GITHUB_REPOSITORY" \
  --certificate-github-workflow-ref "$GITHUB_REF" \
  --certificate-github-workflow-sha "$GITHUB_SHA" \
  -a "release-tag=${RELEASE_TAG}" \
  -a "release-commit=${GITHUB_SHA}" \
  "$IMMUTABLE_REF" >/dev/null

printf 'core artifact signing: verified %s for %s at %s\n' \
  "$IMMUTABLE_REF" "$RELEASE_TAG" "$GITHUB_SHA"
