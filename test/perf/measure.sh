#!/usr/bin/env bash
# test/perf/measure.sh
#
# Captures the v0.1.0 Tier 1 performance baseline against a real
# Kubernetes cluster running the PetClinic phase1 fixture (base +
# multi-namespace + 1000 ConfigMap stress). Emits JSON to
# test/baseline/perf-phase1.json that downstream Phase 2 work
# compares against — a >20% regression on any measured field is the
# bar for "this PR has slowed something down".
#
# Prerequisites:
#   - kubectl talks to the target cluster.
#   - test/petclinic/run.sh phase1 has been applied.
#   - kubeatlas binary on PATH (or set KUBEATLAS_BIN=/path/to/binary).
#   - jq installed.
#
# Usage:
#   bash test/perf/measure.sh
#
# This script does NOT wire into CI for v0.1.0 — perf is gated by a
# human reading the diff in test/baseline/perf-phase1.json.

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${DIR}/../.." && pwd)"
OUT="${REPO_ROOT}/test/baseline/perf-phase1.json"

KUBEATLAS_BIN="${KUBEATLAS_BIN:-kubeatlas}"
PORT="${PORT:-8080}"
SAMPLES="${SAMPLES:-50}"  # iterations per endpoint for percentile math
NAMESPACE="${NAMESPACE:-petclinic}"
WORKLOAD_KIND="${WORKLOAD_KIND:-Deployment}"
WORKLOAD_NAME="${WORKLOAD_NAME:-customers}"

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing dependency: $1" >&2; exit 1; }
}
require curl
require jq
command -v "${KUBEATLAS_BIN}" >/dev/null 2>&1 || {
  echo "kubeatlas binary not found (set KUBEATLAS_BIN to override)" >&2; exit 1;
}

# Start kubeatlas in the background and capture cold-start time
# (process start -> /readyz returns 200).
echo "Starting ${KUBEATLAS_BIN} on :${PORT}"
start_ns=$(date +%s%N)
"${KUBEATLAS_BIN}" > /tmp/kubeatlas.log 2>&1 &
KUBEATLAS_PID=$!
trap 'kill ${KUBEATLAS_PID} 2>/dev/null || true' EXIT

ready_ms=""
for i in $(seq 1 120); do
  if curl -fsS "http://localhost:${PORT}/readyz" >/dev/null 2>&1; then
    end_ns=$(date +%s%N)
    ready_ms=$(( (end_ns - start_ns) / 1000000 ))
    break
  fi
  sleep 0.5
done
[ -n "${ready_ms}" ] || { echo "kubeatlas never became ready"; exit 1; }
echo "Cold start to /readyz: ${ready_ms} ms"

# Steady-state RSS from /proc.
rss_kb=$(awk '/VmRSS/ {print $2}' "/proc/${KUBEATLAS_PID}/status" 2>/dev/null || echo 0)
rss_mb=$(( rss_kb / 1024 ))

# resource_count + edge_count from a single cluster snapshot.
cluster=$(curl -fsS "http://localhost:${PORT}/api/v1alpha1/graph?level=cluster")
ns_count=$(echo "${cluster}" | jq '.nodes | length')
echo "Namespaces visible: ${ns_count}"

# Count resources + edges by hitting the namespace level for each
# namespace and summing. Cheap on PetClinic-sized fixtures.
resource_count=0
edge_count=0
for ns in $(echo "${cluster}" | jq -r '.nodes[].id' | grep -v '^_'); do
  view=$(curl -fsS "http://localhost:${PORT}/api/v1alpha1/graph?level=namespace&namespace=${ns}")
  resource_count=$(( resource_count + $(echo "${view}" | jq '.nodes | length') ))
  edge_count=$(( edge_count + $(echo "${view}" | jq '.edges | length') ))
done

# Percentile helper: emit milliseconds for $SAMPLES curls of $1.
percentile() {
  local url="$1"
  local samples="$2"
  : > /tmp/timings
  for i in $(seq 1 "${samples}"); do
    curl -w '%{time_total}\n' -o /dev/null -s "${url}" >> /tmp/timings
  done
  # Convert seconds -> ms, sort, then pick p50 / p99.
  awk '{printf "%.3f\n", $1 * 1000}' /tmp/timings | sort -n > /tmp/timings.sorted
  local n p50_idx p99_idx p50 p99
  n=$(wc -l < /tmp/timings.sorted)
  p50_idx=$(( (n + 1) / 2 ))
  p99_idx=$(awk -v n="${n}" 'BEGIN{i=int(n*0.99); if(i<1)i=1; print i}')
  p50=$(awk "NR==${p50_idx}" /tmp/timings.sorted)
  p99=$(awk "NR==${p99_idx}" /tmp/timings.sorted)
  echo "${p50} ${p99}"
}

echo "Sampling endpoints (${SAMPLES} iterations each)"
read cluster_p50 cluster_p99 < <(percentile "http://localhost:${PORT}/api/v1alpha1/graph?level=cluster" "${SAMPLES}")
read ns_p50 ns_p99 < <(percentile "http://localhost:${PORT}/api/v1alpha1/graph?level=namespace&namespace=${NAMESPACE}" "${SAMPLES}")
read wl_p50 wl_p99 < <(percentile "http://localhost:${PORT}/api/v1alpha1/graph?level=workload&namespace=${NAMESPACE}&kind=${WORKLOAD_KIND}&name=${WORKLOAD_NAME}" "${SAMPLES}")
read res_p50 res_p99 < <(percentile "http://localhost:${PORT}/api/v1alpha1/resources/${NAMESPACE}/${WORKLOAD_KIND}/${WORKLOAD_NAME}" "${SAMPLES}")

cat > "${OUT}" <<JSON
{
  "\$schema": "https://kubeatlas.lithastra.com/schemas/perf-baseline-v1.json",
  "description": "Phase 1 v0.1.0 perf baseline. Captured against the PetClinic phase1 fixture (base + multi-namespace + 1000 ConfigMap stress).",
  "phase": "1-v0.1.0",
  "captured_at": "$(date -Iseconds)",
  "captured_on": {
    "os": "$(uname -s | tr '[:upper:]' '[:lower:]')",
    "arch": "$(uname -m)",
    "kernel": "$(uname -r)"
  },
  "fixture": {
    "namespace": "${NAMESPACE}",
    "workload_kind": "${WORKLOAD_KIND}",
    "workload_name": "${WORKLOAD_NAME}",
    "samples_per_endpoint": ${SAMPLES}
  },
  "l3_cluster_measurements": {
    "cluster_view_p50_ms": ${cluster_p50},
    "cluster_view_p99_ms": ${cluster_p99},
    "namespace_view_p50_ms": ${ns_p50},
    "namespace_view_p99_ms": ${ns_p99},
    "workload_view_p50_ms": ${wl_p50},
    "workload_view_p99_ms": ${wl_p99},
    "resource_view_p50_ms": ${res_p50},
    "resource_view_p99_ms": ${res_p99},
    "informer_cold_start_ms": ${ready_ms},
    "memory_rss_steady_mb": ${rss_mb},
    "resource_count": ${resource_count},
    "edge_count": ${edge_count}
  },
  "spec_targets": {
    "cluster_view_p50_ms": 500,
    "namespace_view_p50_ms": 2000,
    "informer_cold_start_ms": 5000,
    "topology_drag_fps": 30
  }
}
JSON

echo "Wrote ${OUT}"
echo
echo "Summary:"
jq '.l3_cluster_measurements' "${OUT}"
