#!/usr/bin/env bash
# test/perf/multicluster-merge-bench.sh - federation read-path perf.
#
# Captures latency percentiles for the v1.3 federation endpoints:
#
#   - GET /api/v1/federation/clusters
#   - GET /api/v1/federation/graph?cluster=<all-members>
#
# and appends them to a baseline JSON (defaults to
# test/verify/perf-baseline-v1.3.json) under a "federation" key so
# the file stays cross-comparable with the single-cluster numbers
# captured by bench-v1.sh.
#
# Prerequisites:
#   - KubeAtlas already running with multicluster.enabled=true.
#   - At least one member cluster attached and synced (the script
#     reads the attached list from /api/v1/federation/clusters; a
#     mode=single response aborts with a clear message).
#   - Each member cluster has a populated fixture loaded — the
#     bench measures merge latency, not informer cold-start. The
#     stress-test-10k fixture from stress-10k-resources.sh applied
#     to each member is the canonical setup.
#   - kubectl, curl, jq on PATH.
#
# Usage:
#   KUBEATLAS_URL=http://127.0.0.1:8080 \
#     bash test/perf/multicluster-merge-bench.sh
#
# Anti-patterns guarded:
#   - The script does NOT install KubeAtlas or attach clusters —
#     that is install-config dependent. It probes the URL the
#     operator provided.
#   - No PR triggers this — runs on demand before v1.3.x tag cuts.
#   - The script measures wall-clock against the federation
#     endpoints, including network. Operators running it across a
#     port-forward see port-forward overhead; running it from
#     inside the cluster (via kubectl exec into the kubeatlas pod
#     with curl) is the lower bound.

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${DIR}/../.." && pwd)"

OUT="${KUBEATLAS_BASELINE_OUT:-${REPO_ROOT}/test/verify/perf-baseline-v1.3.json}"
KUBEATLAS_URL="${KUBEATLAS_URL:-http://127.0.0.1:8080}"
SAMPLES="${SAMPLES:-100}"

for cmd in curl jq; do
  command -v "${cmd}" >/dev/null || { echo "missing: ${cmd}" >&2; exit 1; }
done

clusters_body=$(curl -fsSL "${KUBEATLAS_URL}/api/v1/federation/clusters")
mode=$(echo "${clusters_body}" | jq -r '.mode')
if [[ "${mode}" != "federated" ]]; then
  echo "multicluster-merge-bench: SKIPPED (server reports mode=${mode}; this bench requires multicluster.enabled=true with at least one attached cluster)" >&2
  exit 0
fi

mapfile -t clusters < <(echo "${clusters_body}" | jq -r '.clusters[]')
if [[ ${#clusters[@]} -lt 1 ]]; then
  echo "multicluster-merge-bench: federation has zero attached clusters; nothing to bench" >&2
  exit 0
fi

cluster_param=$(IFS=, ; echo "${clusters[*]}")
echo "Benching federation across ${#clusters[@]} cluster(s): ${cluster_param}"

# Quick: /federation/clusters timing — a tiny endpoint but it sits
# on the request hot path the UI cluster switcher will eventually
# poll, so a baseline keeps us honest if a future change adds
# expensive locking.
clusters_url="${KUBEATLAS_URL}/api/v1/federation/clusters"
graph_url="${KUBEATLAS_URL}/api/v1/federation/graph?cluster=${cluster_param}"

declare -a clusters_ms=()
declare -a graph_ms=()
for _ in $(seq "${SAMPLES}"); do
  t=$(curl -o /dev/null -s -w '%{time_total}' "${clusters_url}")
  clusters_ms+=("$(awk -v t="${t}" 'BEGIN {printf "%.3f", t*1000}')")
  t=$(curl -o /dev/null -s -w '%{time_total}' "${graph_url}")
  graph_ms+=("$(awk -v t="${t}" 'BEGIN {printf "%.3f", t*1000}')")
done

percentile() {
  local p="$1"; shift
  printf '%s\n' "$@" | sort -n | awk -v p="${p}" '
    { values[NR]=$1 } END {
      if (NR==0) { print 0; exit }
      idx = int((p/100.0) * (NR-1)) + 1
      if (idx < 1) idx=1; if (idx > NR) idx=NR
      printf "%.3f", values[idx]
    }
  '
}

c_p50=$(percentile 50 "${clusters_ms[@]}")
c_p95=$(percentile 95 "${clusters_ms[@]}")
c_p99=$(percentile 99 "${clusters_ms[@]}")
g_p50=$(percentile 50 "${graph_ms[@]}")
g_p95=$(percentile 95 "${graph_ms[@]}")
g_p99=$(percentile 99 "${graph_ms[@]}")

mkdir -p "$(dirname "${OUT}")"

# Merge into the v1.3 baseline file, adding/replacing the
# "federation" block while leaving any single-cluster blocks
# untouched. jq's -n with --slurpfile keeps the merge robust to
# the file not existing yet.
prior=$(test -f "${OUT}" && cat "${OUT}" || echo '{}')
echo "${prior}" | jq \
  --arg captured_at "$(date -Iseconds)" \
  --argjson c_p50 "${c_p50}" --argjson c_p95 "${c_p95}" --argjson c_p99 "${c_p99}" \
  --argjson g_p50 "${g_p50}" --argjson g_p95 "${g_p95}" --argjson g_p99 "${g_p99}" \
  --argjson members "${#clusters[@]}" \
  --argjson samples "${SAMPLES}" '
    . + {
      federation: {
        captured_at: $captured_at,
        member_count: $members,
        samples_per_endpoint: $samples,
        federation_clusters_p50_ms: $c_p50,
        federation_clusters_p95_ms: $c_p95,
        federation_clusters_p99_ms: $c_p99,
        federation_graph_p50_ms: $g_p50,
        federation_graph_p95_ms: $g_p95,
        federation_graph_p99_ms: $g_p99
      }
    }
  ' > "${OUT}.tmp" && mv "${OUT}.tmp" "${OUT}"

echo "Wrote federation block to ${OUT}"
echo "  /federation/clusters p50=${c_p50}ms p95=${c_p95}ms p99=${c_p99}ms"
echo "  /federation/graph    p50=${g_p50}ms p95=${g_p95}ms p99=${g_p99}ms (cluster=${cluster_param})"
