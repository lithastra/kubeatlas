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

part1_diagnose

# Later parts gate on PHASE4_LEVEL (v1.5 -> part-2, v2.0 -> part-3) and
# land with their milestones (P4-T19 / P4-T31). Nothing to run yet.

green "Phase 4 verify (level ${PHASE4_LEVEL}) — all implemented checks passed"
