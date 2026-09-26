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
RECOVERY_WORKFLOW=.github/workflows/release-artifact-audit.yml
PREFLIGHT_WORKFLOW=.github/workflows/release-preflight.yml
SIGN_SCRIPT=test/verify/sign-core-artifact.sh
AUDIT_SCRIPT=test/verify/core-artifact-audit.sh
ATTESTATION_SCRIPT=test/verify/image-attestations.sh

require_text .goreleaser.yml 'sbom: true'
require_text .goreleaser.yml '"--provenance={{ if .IsSnapshot }}false{{ else }}true{{ end }}"'

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
require_text "$RELEASE_WORKFLOW" 'verifyDraftRelease'
require_text "$RELEASE_WORKFLOW" 'test "$DRAFT_COMMIT" = "$RELEASE_COMMIT"'
require_text "$RELEASE_WORKFLOW" '[[ "$DRAFT_RELEASE_ID" =~ ^[1-9][0-9]*$ ]]'
require_text .github/scripts/release-draft.cjs 'github.paginate(github.rest.repos.listReleases'
require_text .github/scripts/release-draft.cjs 'release_id: candidate.id'
require_text .github/scripts/release-draft.cjs 'release.draft === true && release.published_at === null'
if grep -Fq '/releases/tags/' "$RELEASE_WORKFLOW" "$RECOVERY_WORKFLOW"; then
  fail "draft gates must not use the published-only release-by-tag endpoint"
fi
if grep -Eq 'packages: write|id-token: write|docker/login-action|cosign sign|helm push|goreleaser-action' "$RECOVERY_WORKFLOW"; then
  fail "audit-only recovery must not publish or sign artifacts"
fi
require_text "$RECOVERY_WORKFLOW" "github.ref == 'refs/heads/main'"
require_text "$RECOVERY_WORKFLOW" 'validateAuditInputs(inputs)'
require_text "$RECOVERY_WORKFLOW" 'KUBEATLAS_EXPECTED_APP_DIGEST: ${{ inputs.app_digest }}'
require_text "$RECOVERY_WORKFLOW" 'KUBEATLAS_EXPECTED_DATABASE_DIGEST: ${{ inputs.database_digest }}'
require_text "$RECOVERY_WORKFLOW" 'KUBEATLAS_EXPECTED_CHART_DIGEST: ${{ inputs.chart_digest }}'
require_text "$RECOVERY_WORKFLOW" 'bash test/verify/core-artifact-audit.sh'
require_text "$RECOVERY_WORKFLOW" 'needs: [draft-gate, audit]'
require_text "$RECOVERY_WORKFLOW" 'KUBEATLAS_PRIOR_AUDIT_RESULT: ${{ needs.audit.result }}'
require_text "$RECOVERY_WORKFLOW" 'node .github/scripts/core-artifact-tier2.cjs'
require_text "$RECOVERY_WORKFLOW" 'cluster_name: kubeatlas-core-tier2-audit'
require_text "$RECOVERY_WORKFLOW" 'kind delete cluster --name kubeatlas-core-tier2-audit'
require_text "$RECOVERY_WORKFLOW" "evidence.clusterCleanupPassed = process.env.CLEANUP_OUTCOME === 'success'"
require_text "$PREFLIGHT_WORKFLOW" 'node --test .github/scripts/release-draft.test.cjs'
require_text "$PREFLIGHT_WORKFLOW" 'node --test .github/scripts/core-artifact-audit.test.cjs'
require_text "$PREFLIGHT_WORKFLOW" 'node --test .github/scripts/core-artifact-tier2.test.cjs'
require_text .github/scripts/core-artifact-tier2.cjs 'validateAuditInputs(inputs)'
require_text .github/scripts/core-artifact-tier2.cjs "env.KUBEATLAS_PRIOR_AUDIT_RESULT === 'success'"
require_text .github/scripts/core-artifact-tier2.cjs 'image.digest=${inputs.appDigest}'
require_text .github/scripts/core-artifact-tier2.cjs 'persistence.embedded.image=${databaseImage}'
require_text .github/scripts/core-artifact-tier2.cjs "'--wait', '--timeout', '5m'"
require_text .github/scripts/core-artifact-tier2.cjs "'--wait', '--timeout', '10m'"
require_text .github/scripts/core-artifact-tier2.cjs "current.metadata.uid === resource.uid"

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
require_text "$AUDIT_SCRIPT" '"$APP_DIGEST" == "$EXPECTED_APP_DIGEST"'
require_text "$AUDIT_SCRIPT" '"$DATABASE_DIGEST" == "$EXPECTED_DATABASE_DIGEST"'
require_text "$AUDIT_SCRIPT" '"$CHART_DIGEST" == "$EXPECTED_CHART_DIGEST"'
require_text "$AUDIT_SCRIPT" 'helm pull "oci://${CHART_REPOSITORY}@${CHART_DIGEST}"'
require_text "$AUDIT_SCRIPT" '"${#chart_archives[@]}" -eq 1'
require_text "$AUDIT_SCRIPT" 'helm install "$AUDIT_RELEASE" "$CHART_ARCHIVE"'
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
