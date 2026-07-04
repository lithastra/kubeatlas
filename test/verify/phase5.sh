#!/usr/bin/env bash
# test/verify/phase5.sh - Phase 5 (v1.5) exit verification.
#
# Phase 5 is a single non-breaking minor release (v1.5). This script is
# the single-stage exit gate; it has no LEVEL knob. It splits into:
#
#   OFFLINE checks  — always run. They gate the invariant that matters
#                     most in Phase 5: the frozen v1alpha1 surface did
#                     not change. They assert the committed golden
#                     api/openapi-v1alpha1.json gained no /otel/ path and
#                     no _sunset field, and that the v1.5 store migration
#                     landed. No server or cluster needed.
#
#   SERVER checks   — skipped (not failed) when KUBEATLAS_URL is
#                     unreachable, so the offline invariants still gate in
#                     CLI-only CI. They cover: graphstore_version == "v2",
#                     the v1alpha1 negative assertions against a live
#                     server, the OTel overlay + traces surface (F-204),
#                     the OTel /metrics counters, and — when a federated
#                     server + tokens are provided — multi-cluster RBAC
#                     visibility (F-206).
#
#   HEAVY checks    — the OTel chaos load test and the perf regression
#                     gate. Run only when explicitly opted in (they need a
#                     load generator / benchmark fixtures); otherwise
#                     reported as skipped with the command to run them.
#
# Required tools: jq. curl is required for the server checks (skipped
# without it). Assumes, for the server checks, a running kubeatlas with
# the petclinic fixtures and (for a non-zero overlay) OTel traces flowing.
#
#   bash test/verify/phase5.sh
#   KUBEATLAS_URL=http://localhost:8080 bash test/verify/phase5.sh
#   PHASE5_RUN_HEAVY=1 bash test/verify/phase5.sh   # also run chaos + perf

set -euo pipefail

# ----- config --------------------------------------------------------

PETCLINIC_NS="${KUBEATLAS_PETCLINIC_NS:-petclinic}"
KUBEATLAS_URL="${KUBEATLAS_URL:-http://localhost:8080}"
PHASE5_RUN_HEAVY="${PHASE5_RUN_HEAVY:-0}"

# Repo root: prefer git, fall back to walking up from this script.
if REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)"; then :; else
  REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fi
GOLDEN_OPENAPI="${REPO_ROOT}/api/openapi-v1alpha1.json"
MIGRATE_DIR="${REPO_ROOT}/pkg/store/postgres/migrate"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

# ----- output helpers (match phase4.sh) ------------------------------

red()    { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }

step() { yellow "▶ $*"; }
pass() { green  "  ✓ $*"; }
skip() { yellow "  ↷ $*"; }
fail() { red    "  ✗ $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { red "missing required tool: $1"; exit 1; }
}

# ----- preflight -----------------------------------------------------

step "preflight: required tools available"
require_cmd jq
pass "jq on PATH"
green "Phase 5 (v1.5) verify"

# ----- offline: v1alpha1 is frozen (invariant 2.2) -------------------

offline_v1alpha1_golden() {
  step "[offline] v1alpha1 OpenAPI golden is unchanged by Phase 5"
  [[ -f "${GOLDEN_OPENAPI}" ]] || fail "golden not found: ${GOLDEN_OPENAPI}"
  jq -e . "${GOLDEN_OPENAPI}" >/dev/null || fail "golden is not valid JSON"

  # Phase 5 adds OTel endpoints under /api/v1/ ONLY. The frozen v1alpha1
  # spec must contain no /otel/ path — its presence would mean an
  # endpoint leaked onto the frozen surface.
  if jq -e '.paths | keys[] | select(test("/otel/"))' "${GOLDEN_OPENAPI}" >/dev/null 2>&1; then
    fail "v1alpha1 golden contains an /otel/ path — the frozen surface changed"
  fi
  # No _sunset field anywhere: Phase 5 does not deprecate v1alpha1.
  if grep -q "_sunset" "${GOLDEN_OPENAPI}"; then
    fail "v1alpha1 golden mentions _sunset — Phase 5 must not deprecate v1alpha1"
  fi
  local paths
  paths="$(jq '.paths | keys | length' "${GOLDEN_OPENAPI}")"
  pass "v1alpha1 golden clean (${paths} paths, no /otel/, no _sunset)"
}

offline_migration() {
  step "[offline] GraphStore v2 + OTel overlay migration present"
  [[ -f "${MIGRATE_DIR}/010_otel_runtime_edges.sql" ]] \
    || fail "missing migration 010_otel_runtime_edges.sql"
  # currentSchemaVersion must have moved to 10 in lockstep with the file.
  grep -q "currentSchemaVersion = 10" "${REPO_ROOT}/pkg/store/postgres/schema.go" \
    || fail "currentSchemaVersion is not 10 (out of sync with migration 010)"
  # CALLS_AT_RUNTIME must NOT be a declarative edge type (kept off
  # AllEdgeTypes so it never enters /api/v1/graph or /api/v1alpha1/graph).
  if grep -A40 "var AllEdgeTypes" "${REPO_ROOT}/pkg/graph/model.go" | grep -q "EdgeTypeCallsAtRuntime"; then
    fail "EdgeTypeCallsAtRuntime is in AllEdgeTypes — it must stay overlay-only"
  fi
  pass "migration 010 + schema v10 present; CALLS_AT_RUNTIME kept off AllEdgeTypes"
}

# ----- server checks -------------------------------------------------

server_up() {
  command -v curl >/dev/null 2>&1 || return 1
  curl -fsS --max-time 5 "${KUBEATLAS_URL}/healthz" >/dev/null 2>&1
}

server_store_version() {
  step "[server] graphstore_version is \"v2\""
  local info="${WORKDIR}/info.json"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1/info" > "${info}" \
    || fail "/api/v1/info fetch failed"
  local v
  v="$(jq -r '.graphstore_version // empty' "${info}")"
  [[ "${v}" == "v2" ]] || fail "graphstore_version = '${v}', want 'v2'"
  pass "graphstore_version == v2"
}

server_v1alpha1_frozen() {
  step "[server] v1alpha1 negative assertions"
  # 200, not 404 — Phase 5 does not remove v1alpha1.
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
    "${KUBEATLAS_URL}/api/v1alpha1/graph?level=cluster")"
  [[ "${code}" == "200" ]] || fail "/api/v1alpha1/graph returned ${code}, want 200"

  # No _sunset field on the v1alpha1 response.
  local body="${WORKDIR}/v1a1graph.json"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1alpha1/graph?level=cluster" > "${body}"
  if jq -e '.. | objects | has("_sunset")' "${body}" >/dev/null 2>&1; then
    fail "/api/v1alpha1/graph carries a _sunset field"
  fi

  # The served v1alpha1 spec's path set must equal the committed golden's
  # (no additions, no removals) and contain no /otel/ path.
  local served="${WORKDIR}/served-openapi.json"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1alpha1/openapi.json" > "${served}"
  local served_paths golden_paths
  served_paths="$(jq -S '.paths | keys' "${served}")"
  golden_paths="$(jq -S '.paths | keys' "${GOLDEN_OPENAPI}")"
  [[ "${served_paths}" == "${golden_paths}" ]] \
    || fail "served v1alpha1 path set differs from the committed golden"
  jq -e '.paths | keys[] | select(test("/otel/"))' "${served}" >/dev/null 2>&1 \
    && fail "served v1alpha1 spec exposes an /otel/ path"

  # The overlay must 404 under v1alpha1 (it is v1-only).
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
    "${KUBEATLAS_URL}/api/v1alpha1/otel/overlay?namespace=${PETCLINIC_NS}")"
  [[ "${code}" == "404" ]] || fail "/api/v1alpha1/otel/overlay returned ${code}, want 404"
  pass "v1alpha1 frozen: 200, no _sunset, path set matches golden, no otel"
}

server_otel_overlay() {
  step "[server] OTel overlay (F-204)"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
    "${KUBEATLAS_URL}/api/v1/otel/overlay?namespace=${PETCLINIC_NS}")"
  if [[ "${code}" == "503" ]]; then
    skip "[server] otel overlay returns 503 (otel.enabled=false or Tier 1) — skipping F-204 checks"
    return
  fi
  [[ "${code}" == "200" ]] || fail "/api/v1/otel/overlay returned ${code}, want 200"

  local ov="${WORKDIR}/overlay.json"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1/otel/overlay?namespace=${PETCLINIC_NS}" > "${ov}"
  jq -e '.edges | type == "array"' "${ov}" >/dev/null || fail "overlay has no edges array"
  jq -e '.count | type == "number"' "${ov}" >/dev/null || fail "overlay has no count"
  local n; n="$(jq '.count' "${ov}")"
  # count is fixture/traffic-dependent — reported, not asserted.
  pass "overlay ok (${n} runtime edges in ${PETCLINIC_NS})"

  step "[server] OTel overlay compare mode classifies"
  local cmp="${WORKDIR}/compare.json"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1/otel/overlay?namespace=${PETCLINIC_NS}&compare=true" > "${cmp}"
  jq -e '.summary | has("declaredOnly") and has("observedOnly") and has("both")' "${cmp}" >/dev/null \
    || fail "compare summary missing a class"
  pass "compare summary ok ($(jq -c '.summary' "${cmp}"))"

  step "[server] OTel traces summary"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1/otel/traces" \
    | jq -e '.traces | type == "array"' >/dev/null \
    || fail "/api/v1/otel/traces has no traces array"
  pass "traces summary ok"

  step "[server] OTel /metrics counters present"
  local metrics; metrics="$(curl -fsS --max-time 10 "${KUBEATLAS_URL}/metrics")"
  local m
  for m in received dropped written retention_deleted unmatched_spans runtime_edges; do
    grep -q "^# TYPE kubeatlas_otel_${m}_total counter$" <<<"${metrics}" \
      || fail "metrics missing kubeatlas_otel_${m}_total"
  done
  pass "otel counters present (received/dropped/written/retention_deleted/unmatched_spans/runtime_edges)"
}

server_multicluster_rbac() {
  step "[server] multi-cluster RBAC visibility (F-206)"
  local clusters="${WORKDIR}/clusters.json"
  if ! curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1/federation/clusters" > "${clusters}" 2>/dev/null; then
    skip "[server] federation endpoint unavailable — skipping RBAC checks"
    return
  fi
  local mode; mode="$(jq -r '.mode // "single"' "${clusters}")"
  if [[ "${mode}" != "federated" ]]; then
    skip "[server] single-cluster mode — RBAC visibility not exercised"
    return
  fi
  # To assert token filtering we need a token and a cluster it must NOT
  # see. Provide them via env; otherwise report federation is up and skip.
  if [[ -z "${KUBEATLAS_RBAC_TOKEN:-}" || -z "${KUBEATLAS_RBAC_FORBIDDEN_CLUSTER:-}" ]]; then
    skip "[server] set KUBEATLAS_RBAC_TOKEN + KUBEATLAS_RBAC_FORBIDDEN_CLUSTER to assert token filtering"
    return
  fi
  # An authorised token requesting a cluster outside its allow-set must
  # get 403 — never 200 with an empty body.
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
    -H "Authorization: Bearer ${KUBEATLAS_RBAC_TOKEN}" \
    "${KUBEATLAS_URL}/api/v1/federation/graph?cluster=${KUBEATLAS_RBAC_FORBIDDEN_CLUSTER}")"
  [[ "${code}" == "403" ]] \
    || fail "forbidden cluster returned ${code}, want 403 (not 200+empty)"
  # No token at all → 401.
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
    "${KUBEATLAS_URL}/api/v1/federation/clusters")"
  [[ "${code}" == "401" ]] \
    || fail "unauthenticated federation request returned ${code}, want 401"
  pass "RBAC visibility enforced (403 on forbidden cluster, 401 without a token)"
}

# ----- heavy checks (opt-in) -----------------------------------------

heavy_checks() {
  if [[ "${PHASE5_RUN_HEAVY}" != "1" ]]; then
    skip "[heavy] chaos + perf skipped — set PHASE5_RUN_HEAVY=1 to run"
    skip "        chaos: bash test/chaos/otel-receiver-overload.sh"
    skip "        perf : bash test/verify/perf-regression.sh test/verify/perf-baseline-v1.5.json"
    return
  fi
  step "[heavy] OTel receiver overload chaos"
  bash "${REPO_ROOT}/test/chaos/otel-receiver-overload.sh" \
    || fail "otel receiver overload chaos test failed"
  pass "otel chaos passed"

  step "[heavy] perf regression vs v1.5 baseline"
  if [[ -f "${REPO_ROOT}/test/verify/perf-baseline-v1.5.json" ]]; then
    bash "${REPO_ROOT}/test/verify/perf-regression.sh" \
      "${REPO_ROOT}/test/verify/perf-baseline-v1.5.json" \
      || fail "perf regression gate failed"
    pass "perf within v1.5 baseline"
  else
    skip "[heavy] no perf-baseline-v1.5.json yet (P5-T9)"
  fi
}

# ----- run -----------------------------------------------------------

offline_v1alpha1_golden
offline_migration

if server_up; then
  server_store_version
  server_v1alpha1_frozen
  server_otel_overlay
  server_multicluster_rbac
else
  skip "[server] ${KUBEATLAS_URL} not reachable (or curl missing) — offline checks only"
fi

heavy_checks

green "Phase 5 (v1.5) verify — all implemented checks passed"
