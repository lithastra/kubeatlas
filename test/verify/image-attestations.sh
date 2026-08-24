#!/usr/bin/env bash

# Verify that an immutable multi-platform image index contains one linux/amd64
# and one linux/arm64 image, and that each platform has BuildKit SPDX SBOM and
# SLSA provenance statements whose in-toto subject is that platform digest.

set -euo pipefail

IMAGE_REPOSITORY=${1:-}
IMAGE_DIGEST=${2:-}

fail() {
  printf 'image attestations: %s\n' "$*" >&2
  exit 1
}

[[ "$IMAGE_REPOSITORY" =~ ^ghcr\.io/lithastra/[a-z0-9._/-]+$ ]] ||
  fail "invalid image repository"
[[ "$IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid image digest"
command -v oras >/dev/null || fail "oras is required"
command -v jq >/dev/null || fail "jq is required"

WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

INDEX_FILE="$WORK_DIR/index.json"
oras manifest fetch --output "$INDEX_FILE" \
  "${IMAGE_REPOSITORY}@${IMAGE_DIGEST}" >/dev/null

jq -e '
  .mediaType == "application/vnd.oci.image.index.v1+json" and
  ([.manifests[] | select(
    .platform.os == "linux" and .platform.architecture == "amd64" and
    .annotations["vnd.docker.reference.type"] != "attestation-manifest"
  )] | length == 1) and
  ([.manifests[] | select(
    .platform.os == "linux" and .platform.architecture == "arm64" and
    .annotations["vnd.docker.reference.type"] != "attestation-manifest"
  )] | length == 1)
' "$INDEX_FILE" >/dev/null || fail "expected exactly one amd64 and one arm64 manifest"

for architecture in amd64 arm64; do
  platform_digest=$(jq -er --arg architecture "$architecture" '
    [.manifests[] | select(
      .platform.os == "linux" and .platform.architecture == $architecture and
      .annotations["vnd.docker.reference.type"] != "attestation-manifest"
    )] | if length == 1 then .[0].digest else empty end
  ' "$INDEX_FILE")

  attestation_digest=$(jq -er --arg subject "$platform_digest" '
    [.manifests[] | select(
      .annotations["vnd.docker.reference.type"] == "attestation-manifest" and
      .annotations["vnd.docker.reference.digest"] == $subject
    )] | if length == 1 then .[0].digest else empty end
  ' "$INDEX_FILE") || fail "missing unique attestation manifest for linux/$architecture"

  attestation_manifest="$WORK_DIR/attestation-${architecture}.json"
  oras manifest fetch --output "$attestation_manifest" \
    "${IMAGE_REPOSITORY}@${attestation_digest}" >/dev/null

  for predicate_type in \
    https://spdx.dev/Document \
    https://slsa.dev/provenance/v1; do
    layer_digest=$(jq -er --arg predicate "$predicate_type" '
      [.layers[] | select(
        .mediaType == "application/vnd.in-toto+json" and
        .annotations["in-toto.io/predicate-type"] == $predicate
      )] | if length == 1 then .[0].digest else empty end
    ' "$attestation_manifest") ||
      fail "missing unique $predicate_type statement for linux/$architecture"

    statement_file="$WORK_DIR/${architecture}-$(basename "$predicate_type").json"
    oras blob fetch --output "$statement_file" \
      "${IMAGE_REPOSITORY}@${layer_digest}" >/dev/null

    jq -e --arg predicate "$predicate_type" \
      --arg subject "${platform_digest#sha256:}" '
      .predicateType == $predicate and
      any(.subject[]?; .digest.sha256 == $subject)
    ' "$statement_file" >/dev/null ||
      fail "$predicate_type statement does not bind linux/$architecture"
  done
done

printf 'image attestations: verified amd64/arm64 SPDX and SLSA statements for %s@%s\n' \
  "$IMAGE_REPOSITORY" "$IMAGE_DIGEST"
