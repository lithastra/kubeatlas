#!/usr/bin/env bash
# test/verify/phase4.sh - Phase 4 exit verification.
#
# Phase 4 ships in three release points; this script grows one part per
# release, gated by PHASE4_LEVEL so CI runs only the slice a given
# release needs:
#
#   bash test/verify/phase4.sh PHASE4_LEVEL=v1.4   # part-1 (M13-M14)
#   bash test/verify/phase4.sh PHASE4_LEVEL=v1.5   # parts 1-2 (adds M15-M16)
#   bash test/verify/phase4.sh PHASE4_LEVEL=v2.0   # all parts
#
# PART 1 (M13-M14) assertions implemented so far:
#
#   1. Offline diagnose report (P4-T1 / F-301): `kubeatlas diagnose`
#      scans the cluster the current KUBECONFIG points at and emits a
#      self-contained report. We assert the JSON form is well-formed
#      (carries an orphans array + resourceCount) and the HTML form is
#      non-empty, self-titled, and free of external (CDN) references.
#      The orphan count is reported, not asserted — the fixture decides
#      it.
#
# Part 1 is NOT yet complete: the policy-view, telemetry, and
# v1alpha1-counter segments append in W55-W58 (P4-T8). A green run here
# is not full v1.4 sign-off until then.
#
# Required tools on PATH: kubeatlas, jq.
# Assumes a cluster reachable via KUBECONFIG with the petclinic
# fixtures deployed (test/petclinic/). No running KubeAtlas server is
# needed for the diagnose segment — the CLI scans offline.

set -euo pipefail

# ----- args / config -------------------------------------------------

# PHASE4_LEVEL gates which parts run. Accept it as a KEY=VALUE argument
# (the form the Phase 4 guide documents) or as an environment variable;
# default to the first release point.
for arg in "$@"; do
  case "$arg" in
    PHASE4_LEVEL=*) PHASE4_LEVEL="${arg#PHASE4_LEVEL=}" ;;
  esac
done
PHASE4_LEVEL="${PHASE4_LEVEL:-v1.4}"

PETCLINIC_NS="${KUBEATLAS_PETCLINIC_NS:-petclinic}"
# Server-side checks hit a running kubeatlas. Skipped when unreachable so
# the diagnose (offline CLI) part still works without a live server.
KUBEATLAS_URL="${KUBEATLAS_URL:-http://localhost:8080}"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

# ----- output helpers (match phase3.sh) ------------------------------

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
require_cmd kubeatlas
require_cmd jq
pass "kubeatlas + jq on PATH"

green "Phase 4 verify — level ${PHASE4_LEVEL}"

# ----- part 1: diagnose (P4-T1 / F-301) ------------------------------

part1_diagnose() {
  step "[part-1] diagnose: JSON report is well-formed"
  local json="${WORKDIR}/diagnose.json"
  if ! kubeatlas diagnose -n "${PETCLINIC_NS}" --format json > "${json}"; then
    fail "kubeatlas diagnose --format json failed"
  fi
  jq -e '.orphans | type == "array"' "${json}" >/dev/null \
    || fail "diagnose JSON has no orphans array"
  jq -e '.resourceCount | type == "number"' "${json}" >/dev/null \
    || fail "diagnose JSON has no resourceCount"
  local orphans resources
  orphans="$(jq '.orphans | length' "${json}")"
  resources="$(jq '.resourceCount' "${json}")"
  # Orphan count is fixture-dependent — reported, not asserted.
  pass "diagnose JSON ok (resources: ${resources}, orphans: ${orphans})"

  step "[part-1] diagnose: HTML report is self-contained"
  local html="${WORKDIR}/diagnose.html"
  if ! kubeatlas diagnose -n "${PETCLINIC_NS}" --format html --output "${html}"; then
    fail "kubeatlas diagnose --format html failed"
  fi
  [[ -s "${html}" ]] || fail "diagnose HTML is empty"
  grep -q "KubeAtlas Diagnostic Report" "${html}" \
    || fail "diagnose HTML missing report title"
  # Air-gapped invariant: the report must pull nothing from a CDN.
  if grep -qiE 'https?://[^"]*(cdn|googleapis|jsdelivr|unpkg|cloudflare)' "${html}"; then
    fail "diagnose HTML references an external CDN (air-gapped invariant)"
  fi
  pass "diagnose HTML ok ($(wc -c < "${html}") bytes, no CDN refs)"
}

# part1_server hits a running kubeatlas for the policy, telemetry, and
# API-version-counter surfaces. Skipped (not failed) when the server is
# unreachable or curl is absent, so the offline diagnose checks above
# still gate in CLI-only environments.
part1_server() {
  if ! command -v curl >/dev/null 2>&1; then
    skip "[part-1] server checks: curl not on PATH; skipping policy/telemetry/counter checks"
    return
  fi
  if ! curl -fsS --max-time 5 "${KUBEATLAS_URL}/healthz" >/dev/null 2>&1; then
    skip "[part-1] server checks: ${KUBEATLAS_URL} not reachable; skipping policy/telemetry/counter checks"
    return
  fi

  step "[part-1] policy: /api/v1/policy/constraints is a list"
  local pol="${WORKDIR}/policy.json"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1/policy/constraints" > "${pol}" \
    || fail "policy constraints fetch failed"
  jq -e 'type == "array"' "${pol}" >/dev/null || fail "policy constraints is not an array"
  # Count is fixture-dependent (needs phase4-policy.yaml applied + a
  # policy engine); reported, not asserted.
  pass "policy constraints ok ($(jq 'length' "${pol}") constraints)"

  step "[part-1] telemetry: /api/v1/telemetry/preview is transparent"
  local prev="${WORKDIR}/preview.json"
  curl -fsS --max-time 10 "${KUBEATLAS_URL}/api/v1/telemetry/preview" > "${prev}" \
    || fail "telemetry preview fetch failed"
  jq -e '.kubeatlas_version | (type == "string") and (. != "")' "${prev}" >/dev/null \
    || fail "telemetry preview has no kubeatlas_version"
  jq -e 'has("session_nonce") and (has("install_uuid") | not) and (has("namespace") | not) and (has("ip") | not)' "${prev}" >/dev/null \
    || fail "telemetry preview leaks a sensitive field (or lacks session_nonce)"
  pass "telemetry preview ok (session_nonce present; no install_uuid/namespace/ip)"

  step "[part-1] metrics: v1alpha1/v1 usage counters present"
  local metrics
  metrics="$(curl -fsS --max-time 10 "${KUBEATLAS_URL}/metrics")" \
    || fail "metrics fetch failed"
  grep -q "^# TYPE kubeatlas_api_v1alpha1_requests_total counter$" <<<"${metrics}" \
    || fail "metrics missing kubeatlas_api_v1alpha1_requests_total (counter)"
  grep -q "^# TYPE kubeatlas_api_v1_requests_total counter$" <<<"${metrics}" \
    || fail "metrics missing kubeatlas_api_v1_requests_total (counter)"
  pass "v1alpha1 + v1 request counters present"
}

part1_diagnose
part1_server

# Later parts gate on PHASE4_LEVEL (v1.5 -> part-2, v2.0 -> part-3) and
# land with their milestones. Nothing to run yet.

green "Phase 4 verify (level ${PHASE4_LEVEL}) — all implemented checks passed"
