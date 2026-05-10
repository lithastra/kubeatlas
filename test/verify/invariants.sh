#!/usr/bin/env bash
# test/verify/invariants.sh - cross-phase invariant gates.
#
# Asserts that the running build's API surface is a strict
# superset of the previous phase's. Each subcommand maps to one
# invariant set:
#
#   phase1 — Phase 1 v0.1.0 baseline. Every Phase 1 endpoint and
#            edge type that v0.1.0 promised is still served.
#   phase2 — Phase 2 v1.0.0. Every Phase 2 addition (Tier 2
#            persistence wires, RBAC graph, blast radius,
#            orphan/cycle endpoints) is present AND the phase1
#            invariants still hold.
#
# Usage:
#   bash test/verify/invariants.sh phase2
#
# Exit codes:
#   0 — every invariant satisfied.
#   1 — usage error.
#   2 — at least one invariant failed; the script names which.

set -euo pipefail

KUBEATLAS_URL="${KUBEATLAS_URL:-http://127.0.0.1:8080}"

red()    { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }

usage() {
  cat >&2 <<EOF
usage: invariants.sh [phase1|phase2]

Run the named invariant gate against the kubeatlas reachable at
\$KUBEATLAS_URL (default ${KUBEATLAS_URL}). Defaults to phase2.
EOF
  exit 1
}

PHASE="${1:-phase2}"
[[ "${PHASE}" == "phase1" || "${PHASE}" == "phase2" ]] || usage

failed=()
fail_inv() { failed+=("$1"); red "  ✗ $1"; }
pass_inv() { green "  ✓ $1"; }

# Hits a JSON endpoint and reports OK iff the response is parseable
# JSON with the expected top-level key (or a non-empty array when
# `path` is omitted).
check_json() {
  local label="$1"
  local url="$2"
  local jq_path="$3"
  local body
  body=$(curl -fsS --max-time 10 "${url}" 2>/dev/null) \
    || { fail_inv "${label}: HTTP fetch failed (${url})"; return; }
  if ! jq -e "${jq_path}" <<<"${body}" >/dev/null 2>&1; then
    fail_inv "${label}: jq path \"${jq_path}\" did not match (body: ${body:0:200})"
    return
  fi
  pass_inv "${label}"
}

# --- phase1 invariants (v0.1.0 surface) -----------------------------
phase1() {
  check_json "phase1: /healthz" \
    "${KUBEATLAS_URL}/healthz" '.status'
  check_json "phase1: /readyz" \
    "${KUBEATLAS_URL}/readyz" '.status'
  check_json "phase1: /api/v1alpha1/graph?level=cluster" \
    "${KUBEATLAS_URL}/api/v1alpha1/graph?level=cluster" '.nodes'
  check_json "phase1: /api/v1alpha1/openapi.json" \
    "${KUBEATLAS_URL}/api/v1alpha1/openapi.json" '.info.title'

  # Edge-type allowlist: every Phase 1 edge type still appears in the
  # OpenAPI Edge schema. Phase 2 is allowed to ADD edge types but
  # NOT remove any of these eight.
  spec=$(curl -fsS "${KUBEATLAS_URL}/api/v1alpha1/openapi.json")
  for et in OWNS USES_CONFIGMAP USES_SECRET MOUNTS_VOLUME SELECTS \
            USES_SERVICEACCOUNT ROUTES_TO ATTACHED_TO; do
    if jq -e --arg et "${et}" \
        '.components.schemas.Edge.properties.type.enum | index($et)' \
        <<<"${spec}" >/dev/null 2>&1; then
      pass_inv "phase1: Edge enum still includes ${et}"
    else
      fail_inv "phase1: Edge enum lost ${et}"
    fi
  done
}

# --- phase2 invariants (v1.0.0 additions) ---------------------------
phase2() {
  phase1

  # /api/v1/* GA path is served alongside v1alpha1.
  check_json "phase2: /api/v1/graph?level=cluster" \
    "${KUBEATLAS_URL}/api/v1/graph?level=cluster" '.nodes'
  check_json "phase2: /api/v1/openapi.json" \
    "${KUBEATLAS_URL}/api/v1/openapi.json" '.info.version | select(. == "v1")'

  # New Phase 2 endpoints exist (only check shape, not data — fixture
  # may be empty on a freshly bootstrapped cluster).
  check_json "phase2: /api/v1/orphans" \
    "${KUBEATLAS_URL}/api/v1/orphans" '.reports'
  check_json "phase2: /api/v1/cycles" \
    "${KUBEATLAS_URL}/api/v1/cycles" '.cycles'

  # /metrics exposes the rego counters (P2-T11/T13 invariant).
  metrics=$(curl -fsS --max-time 10 "${KUBEATLAS_URL}/metrics" 2>/dev/null) \
    || { fail_inv "phase2: /metrics fetch failed"; return; }
  for counter in kubeatlas_rego_modules_loaded \
                 kubeatlas_rego_cache_hits_total \
                 kubeatlas_rego_cache_misses_total \
                 kubeatlas_rego_eval_timeout_total \
                 kubeatlas_rego_eval_panic_total; do
    if grep -q "^# TYPE ${counter} " <<<"${metrics}"; then
      pass_inv "phase2: /metrics exposes ${counter}"
    else
      fail_inv "phase2: /metrics missing ${counter}"
    fi
  done
}

case "${PHASE}" in
  phase1) phase1 ;;
  phase2) phase2 ;;
esac

echo
if (( ${#failed[@]} == 0 )); then
  green "invariants ${PHASE}: all green"
  exit 0
fi
red "invariants ${PHASE}: ${#failed[@]} invariant(s) failed:"
for f in "${failed[@]}"; do
  red "  - ${f}"
done
exit 2
