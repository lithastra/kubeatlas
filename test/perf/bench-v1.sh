#!/usr/bin/env bash
# test/perf/bench-v1.sh - v1.0.0 perf bench (P2-T23).
#
# Captures cluster-view and BlastRadius response-time percentiles
# against a running KubeAtlas + the stress-5k fixture, then
# writes the result to test/verify/perf-baseline-v1.0.json.
#
# Tier is selected via KUBEATLAS_TIER (tier1 | tier2). Each tier
# is benched separately so the baseline JSON carries both
# numbers — the playbook acceptance asks for cluster-view < 1s
# and BlastRadius P95 < 500ms on both tiers, and a single-shot
# script keeps the comparison apples-to-apples.
#
# Prerequisites:
#   - KubeAtlas already running and reachable on $KUBEATLAS_URL.
#   - stress-test-5k fixture applied (test/perf/stress-5k-resources.sh).
#   - kubectl, curl, jq on PATH.
#
# Usage:
#   KUBEATLAS_URL=http://127.0.0.1:8080 KUBEATLAS_TIER=tier1 \
#     bash test/perf/bench-v1.sh
#
# Anti-patterns guarded:
#   - The script does NOT spin up KubeAtlas itself (that is
#     install-config dependent). It probes the URL the operator
#     provided. Caller spins up the binary or helm install.
#   - Output JSON includes the runner's hardware so the baseline
#     stays comparable across re-runs.
#   - No PR triggers this — runs on demand, weekly, or before tag
#     cuts (see CHANGELOG / RELEASE.md).

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${DIR}/../.." && pwd)"
OUT="${REPO_ROOT}/test/verify/perf-baseline-v1.0.json"

KUBEATLAS_URL="${KUBEATLAS_URL:-http://127.0.0.1:8080}"
KUBEATLAS_TIER="${KUBEATLAS_TIER:-tier1}"
SAMPLES="${SAMPLES:-100}"
NS="${NS:-stress-test-5k}"
BLAST_TARGET_KIND="${BLAST_TARGET_KIND:-ConfigMap}"
BLAST_TARGET_NAME="${BLAST_TARGET_NAME:-cm-00000}"

for cmd in curl jq; do
  command -v "${cmd}" >/dev/null 2>&1 || { echo "missing: ${cmd}" >&2; exit 1; }
done

case "${KUBEATLAS_TIER}" in
  tier1|tier2) ;;
  *) echo "KUBEATLAS_TIER must be tier1 or tier2 (got: ${KUBEATLAS_TIER})" >&2; exit 1 ;;
esac

# -- helpers --------------------------------------------------------

# percentile <url> <samples> -> "p50 p95 p99" (milliseconds)
percentile() {
  local url="$1"
  local samples="$2"
  : > /tmp/timings
  for _ in $(seq 1 "${samples}"); do
    curl -w '%{time_total}\n' -o /dev/null -s "${url}" >> /tmp/timings
  done
  awk '{printf "%.3f\n", $1 * 1000}' /tmp/timings | sort -n > /tmp/timings.sorted
  local n p50_idx p95_idx p99_idx p50 p95 p99
  n=$(wc -l < /tmp/timings.sorted)
  p50_idx=$(( (n + 1) / 2 ))
  p95_idx=$(awk -v n="${n}" 'BEGIN{i=int(n*0.95); if(i<1)i=1; print i}')
  p99_idx=$(awk -v n="${n}" 'BEGIN{i=int(n*0.99); if(i<1)i=1; print i}')
  p50=$(awk "NR==${p50_idx}" /tmp/timings.sorted)
  p95=$(awk "NR==${p95_idx}" /tmp/timings.sorted)
  p99=$(awk "NR==${p99_idx}" /tmp/timings.sorted)
  echo "${p50} ${p95} ${p99}"
}

# -- preflight ------------------------------------------------------

echo "Probing ${KUBEATLAS_URL}/healthz"
curl -fsS --max-time 5 "${KUBEATLAS_URL}/healthz" >/dev/null \
  || { echo "kubeatlas not reachable on ${KUBEATLAS_URL}"; exit 1; }

echo "Sampling endpoints (${SAMPLES} iterations each)"
read cluster_p50 cluster_p95 cluster_p99 < <(percentile \
  "${KUBEATLAS_URL}/api/v1/graph?level=cluster" "${SAMPLES}")

read ns_p50 ns_p95 ns_p99 < <(percentile \
  "${KUBEATLAS_URL}/api/v1/graph?level=namespace&namespace=${NS}" "${SAMPLES}")

read blast_p50 blast_p95 blast_p99 < <(percentile \
  "${KUBEATLAS_URL}/api/v1/blast-radius/${NS}/${BLAST_TARGET_KIND}/${BLAST_TARGET_NAME}" \
  "${SAMPLES}")

# Resource + edge counts from the cluster snapshot (single sample).
counts=$(curl -fsS "${KUBEATLAS_URL}/api/v1/graph?level=cluster")
ns_count=$(echo "${counts}" | jq '.nodes | length')

# -- merge / write --------------------------------------------------

# If a baseline already exists, merge the per-tier block into it so
# tier1 + tier2 both end up in the same file across two invocations.
existing="{}"
if [[ -f "${OUT}" ]]; then
  existing=$(cat "${OUT}")
fi

tier_block=$(cat <<JSON
{
  "cluster_view_p50_ms": ${cluster_p50},
  "cluster_view_p95_ms": ${cluster_p95},
  "cluster_view_p99_ms": ${cluster_p99},
  "namespace_view_p50_ms": ${ns_p50},
  "namespace_view_p95_ms": ${ns_p95},
  "namespace_view_p99_ms": ${ns_p99},
  "blast_radius_p50_ms": ${blast_p50},
  "blast_radius_p95_ms": ${blast_p95},
  "blast_radius_p99_ms": ${blast_p99},
  "namespace_count": ${ns_count}
}
JSON
)

merged=$(jq -n \
  --argjson existing "${existing}" \
  --arg tier "${KUBEATLAS_TIER}" \
  --argjson tier_block "${tier_block}" \
  --arg captured_at "$(date -Iseconds)" \
  --arg os "$(uname -s | tr '[:upper:]' '[:lower:]')" \
  --arg arch "$(uname -m)" \
  --arg kernel "$(uname -r)" \
  --arg ns "${NS}" \
  --argjson samples "${SAMPLES}" \
  '
  $existing
  + {
    "$schema": "https://kubeatlas.lithastra.com/schemas/perf-baseline-v1.json",
    description: "v1.0.0 perf baseline (P2-T23). Captured against the stress-test-5k fixture across both Tier 1 and Tier 2 backends.",
    phase: "2-v1.0.0",
    captured_at: $captured_at,
    captured_on: { os: $os, arch: $arch, kernel: $kernel },
    fixture: { namespace: $ns, samples_per_endpoint: $samples },
    spec_targets: {
      cluster_view_p50_ms: 1000,
      cluster_view_p95_ms: 1500,
      blast_radius_p95_ms: 500,
      namespace_view_p50_ms: 2000
    }
  }
  | .[$tier] = $tier_block
  ')

echo "${merged}" > "${OUT}"
echo "Wrote ${OUT}"
echo
echo "Summary (${KUBEATLAS_TIER}):"
echo "${merged}" | jq ".\"${KUBEATLAS_TIER}\""
