#!/usr/bin/env bash
# test/verify/phase3.sh - Phase 3 exit verification.
#
# This is PART 1 (M7): the F-109 NetworkPolicy edges and the F-111
# snapshot writer + diff endpoint. Parts 2 (M9) and 3 (M10-12) are
# appended as those milestones land — the script takes an optional
# part selector argument so CI can run just the slice it needs:
#
#   bash test/verify/phase3.sh            # all implemented parts
#   bash test/verify/phase3.sh m7-m8      # part 1 only
#
# Part 1 assertions:
#
#   1. NetworkPolicy edges (P3-T1 / F-109): applying the
#      phase3-networkpolicy.yaml fixture makes the
#      /api/v1/networkpolicy/{ns}/{name}/selected and /allow-graph
#      endpoints return the declared peers. The fixture's
#      NetworkPolicies are annotated to force immediate edge
#      re-extraction — SELECTS_NP edges are otherwise recomputed
#      only on the informer resync (the documented reverse-edge
#      staleness; see the fixture header).
#   2. Snapshot writer + diff (P3-T3/T5 / F-111): on a Tier 2
#      install with snapshots enabled, POST the internal trigger,
#      confirm /api/v1/snapshots lists the marker, scale a
#      Deployment, and confirm /api/v1/snapshots/diff reports the
#      change. Auto-skipped on Tier 1 (the endpoints answer 503).
#   3. EKS rule pack (P3R-T1/T2 / F-106): OFF by default — needs
#      AWS Load Balancer Controller installed and the eks pack
#      loaded from its OCI artifact. Enable with
#      KUBEATLAS_CHECK_EKS_PACK=1 once eks/v0.1.0 has shipped.
#
# Part 1 chaos (KUBEATLAS_RUN_CHAOS=1): snapshot-write-storm.
#
# Required tools on PATH: kubectl, jq, curl.
# Assumes a cluster reachable via KUBECONFIG with kubeatlas
# installed in namespace "kubeatlas". The snapshot assertions need
# a Tier 2 + snapshots.enabled install; they self-skip otherwise.

set -euo pipefail

PART="${1:-all}"

NS="${KUBEATLAS_NAMESPACE:-kubeatlas}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
DEPLOY="${KUBEATLAS_RELEASE:-kubeatlas}"
# Where the petclinic fixtures live, relative to the repo root.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE_DIR="${REPO_ROOT}/test/petclinic"
# KUBEATLAS_CHECK_EKS_PACK=1 enables the optional EKS rule-pack
# assertion. Off by default — see the header.
CHECK_EKS_PACK="${KUBEATLAS_CHECK_EKS_PACK:-0}"
# KUBEATLAS_RUN_CHAOS=1 runs the snapshot-write-storm chaos after
# the assertions. Off by default (chaos adds minutes to the run).
RUN_CHAOS="${KUBEATLAS_RUN_CHAOS:-0}"
# How long to give the async snapshot writer to flush a burst of
# events into PG before the diff endpoint should see them.
SNAPSHOT_FLUSH_SECONDS="${KUBEATLAS_SNAPSHOT_FLUSH:-20}"

# ----- helpers -------------------------------------------------------

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

# Port-forward to the kubeatlas API. The runtime image is distroless
# (no curl), so the host curls a backgrounded port-forward — same
# approach as phase2.sh.
KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT:-18081}"
KUBEATLAS_PF_PID=""

start_port_forward() {
  if [[ -n "${KUBEATLAS_PF_PID}" ]]; then
    return 0
  fi
  kubectl port-forward -n "${NS}" "deploy/${DEPLOY}" \
    "${KUBEATLAS_PF_PORT}:8080" >/tmp/kubeatlas-pf-phase3.log 2>&1 &
  KUBEATLAS_PF_PID=$!
  trap stop_port_forward EXIT
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 1 "http://127.0.0.1:${KUBEATLAS_PF_PORT}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "port-forward did not become ready on :${KUBEATLAS_PF_PORT}"
}

stop_port_forward() {
  if [[ -n "${KUBEATLAS_PF_PID}" ]] && kill -0 "${KUBEATLAS_PF_PID}" 2>/dev/null; then
    kill "${KUBEATLAS_PF_PID}" 2>/dev/null || true
    wait "${KUBEATLAS_PF_PID}" 2>/dev/null || true
  fi
  KUBEATLAS_PF_PID=""
}

# kubeatlas_curl: GET that fails the script on a non-2xx response.
kubeatlas_curl() {
  curl -fsS --max-time 10 "http://127.0.0.1:${KUBEATLAS_PF_PORT}$1"
}

# http_status: GET (or METHOD via $2) returning only the status
# code, never failing the script — used to probe Tier 1 vs Tier 2.
http_status() {
  curl -s -o /dev/null -w '%{http_code}' -X "${2:-GET}" --max-time 10 \
    "http://127.0.0.1:${KUBEATLAS_PF_PORT}$1"
}

wait_for_pod_ready() {
  local timeout=$1
  local deadline=$((SECONDS + timeout))
  while (( SECONDS < deadline )); do
    if kubectl get pod -n "${NS}" -l "app.kubernetes.io/name=${RELEASE}" \
        -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null \
        | grep -qw "True"; then
      return 0
    fi
    sleep 1
  done
  fail "no Ready kubeatlas Pod after ${timeout}s"
}

# ----- preflight -----------------------------------------------------

step "preflight: required tools available"
require_cmd kubectl
require_cmd jq
require_cmd curl
pass "kubectl + jq + curl on PATH"

step "preflight: kubeatlas Pod is Ready"
wait_for_pod_ready 60
pass "kubeatlas Ready"

step "preflight: open port-forward to kubeatlas /api"
start_port_forward
pass "kubeatlas API reachable on :${KUBEATLAS_PF_PORT}"

# ====================================================================
# Part 1A — NetworkPolicy edges (P3-T1 / F-109)
# ====================================================================

if [[ "${PART}" == "all" || "${PART}" == "m7-m8" ]]; then
  step "F-109: apply base + NetworkPolicy fixture"
  kubectl apply -f "${FIXTURE_DIR}/base.yaml" >/dev/null
  kubectl apply -f "${FIXTURE_DIR}/phase3-networkpolicy.yaml" >/dev/null
  # Best-effort wait — some petclinic workloads (the migrate Job)
  # never reach Ready; tolerate that, the extractor only needs the
  # resources to exist in the informer cache.
  kubectl wait --for=condition=ready pod --all -n petclinic --timeout=120s >/dev/null 2>&1 || true
  pass "base + phase3-networkpolicy.yaml applied"

  step "F-109: force NetworkPolicy edge re-extraction"
  # SELECTS_NP edges are derived when the NetworkPolicy is processed;
  # workloads synced afterwards are missed until the resync. Annotate
  # each NetworkPolicy so the informer reprocesses it against the now-
  # complete snapshot (see the fixture header for the full rationale).
  for np in default-deny api-allow monitoring-ingress; do
    kubectl annotate networkpolicy "${np}" -n petclinic \
      "kubeatlas.io/verify-touch=$(date +%s)" --overwrite >/dev/null 2>&1 || true
  done
  sleep 8
  pass "NetworkPolicies re-annotated"

  step "F-109: /networkpolicy/{ns}/{name}/selected returns matched workloads"
  selected_count=$(kubeatlas_curl \
    "/api/v1/networkpolicy/petclinic/default-deny/selected" | jq -r '.count')
  if ! [[ "${selected_count}" =~ ^[0-9]+$ ]]; then
    fail "selected count not numeric: ${selected_count}"
  fi
  if (( selected_count < 1 )); then
    fail "default-deny selected ${selected_count} workloads, want >= 1"
  fi
  pass "default-deny selects ${selected_count} workload(s)"

  step "F-109: /networkpolicy/{ns}/{name}/allow-graph returns declared peers"
  allow_body=$(kubeatlas_curl "/api/v1/networkpolicy/petclinic/api-allow/allow-graph")
  allow_from=$(jq -r '.allowFrom | length' <<<"${allow_body}")
  allow_to=$(jq -r '.allowTo | length' <<<"${allow_body}")
  if (( allow_from < 1 || allow_to < 1 )); then
    fail "api-allow allow-graph: allowFrom=${allow_from} allowTo=${allow_to}, want both >= 1"
  fi
  pass "api-allow allow-graph: allowFrom=${allow_from} allowTo=${allow_to}"

  # ==================================================================
  # Part 1B — Snapshot writer + diff (P3-T3/T5 / F-111)
  # ==================================================================

  step "F-111: probe whether snapshots are enabled (Tier 2)"
  snap_status=$(http_status "/api/v1/snapshots")
  if [[ "${snap_status}" == "503" ]]; then
    skip "snapshots disabled (Tier 1 or snapshots.enabled=false) — /api/v1/snapshots returned 503"
  elif [[ "${snap_status}" != "200" ]]; then
    fail "/api/v1/snapshots returned ${snap_status}, want 200 or 503"
  else
    pass "snapshots enabled — /api/v1/snapshots returned 200"

    step "F-111: POST internal trigger records a snapshot marker"
    before=$(kubeatlas_curl "/api/v1/snapshots" | jq -r '.count')
    trig_status=$(http_status "/api/_internal/snapshot/trigger?trigger=manual" POST)
    if [[ "${trig_status}" != "200" ]]; then
      fail "snapshot trigger returned ${trig_status}, want 200"
    fi
    after=$(kubeatlas_curl "/api/v1/snapshots" | jq -r '.count')
    if (( after <= before )); then
      fail "snapshot marker count did not grow: before=${before} after=${after}"
    fi
    pass "snapshot marker recorded (count ${before} -> ${after})"

    step "F-111: scale a Deployment, then diff the change window"
    kubectl scale deployment api -n petclinic --replicas=3 >/dev/null 2>&1 || true
    sleep "${SNAPSHOT_FLUSH_SECONDS}"
    diff_body=$(kubeatlas_curl \
      "/api/v1/snapshots/diff?from=5m&to=now&namespace=petclinic")
    changed=$(jq -r '(.added | length) + (.removed | length) + (.modified | length)' \
      <<<"${diff_body}")
    if ! [[ "${changed}" =~ ^[0-9]+$ ]]; then
      fail "diff change count not numeric (body: ${diff_body})"
    fi
    if (( changed < 1 )); then
      fail "diff reported no changes after scaling api Deployment; the snapshot writer may not have flushed"
    fi
    pass "diff reported ${changed} change(s) after the scale"
  fi
fi

# ====================================================================
# Part 1C — EKS rule pack (P3R-T1/T2 / F-106), optional
# ====================================================================

if [[ "${PART}" == "all" || "${PART}" == "m7-m8" ]]; then
  if [[ "${CHECK_EKS_PACK}" != "1" ]]; then
    step "F-106: EKS rule-pack assertion"
    skip "disabled — set KUBEATLAS_CHECK_EKS_PACK=1 after eks/v0.1.0 ships (needs AWS Load Balancer Controller + the eks OCI pack loaded)"
  else
    step "F-106: TargetGroupBinding -> Service ROUTES_TO edge"
    tgb_fixture="${FIXTURE_DIR}/phase3-eks.yaml"
    if [[ ! -f "${tgb_fixture}" ]]; then
      fail "KUBEATLAS_CHECK_EKS_PACK=1 but ${tgb_fixture} is absent"
    fi
    kubectl apply -f "${tgb_fixture}" >/dev/null
    sleep 30
    routes_to=$(kubeatlas_curl \
      "/api/v1/resources/petclinic/TargetGroupBinding/petclinic/outgoing" \
      | jq -r '[.[] | select(.type == "ROUTES_TO")] | length')
    if (( routes_to < 1 )); then
      fail "TargetGroupBinding has ${routes_to} ROUTES_TO edges, want >= 1"
    fi
    pass "TargetGroupBinding -> Service ROUTES_TO edge present"
  fi
fi

# ====================================================================
# Chaos (optional) — snapshot-write-storm
# ====================================================================

if [[ "${RUN_CHAOS}" == "1" && ( "${PART}" == "all" || "${PART}" == "m7-m8" ) ]]; then
  step "chaos: snapshot-write-storm"
  KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT}" \
    bash "${REPO_ROOT}/test/chaos/snapshot-write-storm.sh"
  pass "snapshot-write-storm completed"
fi

echo
green "phase3 verification (part: ${PART}): all assertions passed"
