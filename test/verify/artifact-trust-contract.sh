#!/usr/bin/env bash

# Static, no-publish contract for the v1.6 core artifact trust path. Runtime
# signatures can only be produced on a release tag, so PR preflight verifies
# that every publishing and clean-audit edge remains wired fail closed.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$ROOT_DIR"

fail() {
  printf 'artifact trust contract: %s\n' "$*" >&2
  exit 1
}

require_text() {
  file=$1
  expected=$2
  grep -Fq -- "$expected" "$file" || fail "$file is missing: $expected"
}

RELEASE_WORKFLOW=.github/workflows/release.yml
PREFLIGHT_WORKFLOW=.github/workflows/release-preflight.yml
SIGN_SCRIPT=test/verify/sign-core-artifact.sh
AUDIT_SCRIPT=test/verify/core-artifact-audit.sh
ATTESTATION_SCRIPT=test/verify/image-attestations.sh

require_text .goreleaser.yml 'sbom: true'
require_text .goreleaser.yml '"--provenance=true"'

sign_calls=$(grep -Fc 'bash test/verify/sign-core-artifact.sh' "$RELEASE_WORKFLOW")
[[ "$sign_calls" -eq 3 ]] || fail "release workflow must sign exactly three core OCI artifacts"

id_token_grants=$(grep -Fc 'id-token: write' "$RELEASE_WORKFLOW")
[[ "$id_token_grants" -eq 3 ]] || fail "only the three signing jobs may receive OIDC tokens"

require_text "$RELEASE_WORKFLOW" 'sigstore/cosign-installer@v4.1.2'
require_text "$RELEASE_WORKFLOW" 'oras-project/setup-oras@v2.0.1'
require_text "$RELEASE_WORKFLOW" 'version: 1.3.3'
require_text "$RELEASE_WORKFLOW" 'name: Anonymous signed core artifacts on clean Kubernetes'
require_text "$RELEASE_WORKFLOW" 'if: always()'
require_text "$RELEASE_WORKFLOW" 'bash test/verify/core-artifact-audit.sh'
require_text "$RELEASE_WORKFLOW" 'retention-days: 30'
require_text "$RELEASE_WORKFLOW" '.draft == true'

require_text "$SIGN_SCRIPT" 'cosign sign --yes'
require_text "$SIGN_SCRIPT" '--certificate-identity'
require_text "$SIGN_SCRIPT" '--certificate-oidc-issuer'
require_text "$SIGN_SCRIPT" '--certificate-github-workflow-repository'
require_text "$SIGN_SCRIPT" '--certificate-github-workflow-ref'
require_text "$SIGN_SCRIPT" '--certificate-github-workflow-sha'
require_text "$SIGN_SCRIPT" 'release-tag='
require_text "$SIGN_SCRIPT" 'release-commit='

require_text "$AUDIT_SCRIPT" 'KUBEATLAS_REQUIRE_ANONYMOUS'
require_text "$AUDIT_SCRIPT" 'oras resolve'
require_text "$AUDIT_SCRIPT" 'cosign verify'
require_text "$AUDIT_SCRIPT" 'image.digest='
require_text "$AUDIT_SCRIPT" 'docker pull --platform linux/amd64'
require_text "$ATTESTATION_SCRIPT" 'https://spdx.dev/Document'
require_text "$ATTESTATION_SCRIPT" 'https://slsa.dev/provenance/v1'
require_text "$ATTESTATION_SCRIPT" '.annotations["vnd.docker.reference.digest"] == $subject'

require_text helm/kubeatlas/templates/_helpers.tpl 'printf "%s@%s"'
require_text helm/kubeatlas/values.yaml 'digest: ""'
require_text helm/kubeatlas/values.schema.json '^$|^sha256:[0-9a-f]{64}$'

require_text "$PREFLIGHT_WORKFLOW" 'bash test/verify/artifact-trust-contract.sh'

printf 'artifact trust contract: signing, attestations, anonymous audit, and digest install aligned\n'
