#!/usr/bin/env bash
# test/perf/start-multicluster-fixture.sh
#
# Spin up the two-cluster federation perf fixture and start the
# kubeatlas binary in multicluster mode against it. Used by the
# v1.3 perf-baseline capture pipeline and the
# multicluster-merge-bench / cluster-disconnect chaos scripts.
#
# Outline:
#   1. Create two kind clusters: kubeatlas-prod, kubeatlas-staging.
#   2. Export each cluster's kubeconfig into ${STATE_DIR}/kubeconfigs/
#      where the file name (the cluster's logical name) becomes the
#      ClusterID kubeatlas tags every Resource with.
#   3. Load a stress fixture into each cluster — by default the
#      shared stress-5k generator (5000 ConfigMaps + 1000 Deployments
#      + 200 Services per cluster, so ~10K resources across the
#      federation total).
#   4. Build bin/kubeatlas if missing (CGO-free) and start it under
#      KUBEATLAS_MULTICLUSTER_ENABLED=true so both clusters' informers
#      run against a shared in-memory store.
#   5. Wait for /readyz, then print the curl recipes the bench and
#      chaos scripts assume.
#
# Idempotency: re-running the script with existing clusters re-uses
# them rather than failing. The fixture is namespace-scoped, so a
# stale namespace from a previous run is replaced. Use the
# companion stop-multicluster-fixture.sh to tear everything down.
#
# Environment knobs:
#   STATE_DIR        Where state files (kubeconfigs, PID, logs)
#                    land. Default: /tmp/kubeatlas-fixture.
#   FIXTURE          Path to the stress generator to run inside each
#                    cluster. Default: test/perf/stress-5k-resources.sh.
#   KUBEATLAS_PORT   Port the local kubeatlas process listens on.
#                    Default: 18080 (avoids clashing with anything
#                    on 8080).
#   SKIP_FIXTURE     "1" to skip the stress-fixture load (clusters
#                    are still created and kubeatlas still starts).
#                    Useful for quick smoke runs.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="${STATE_DIR:-/tmp/kubeatlas-fixture}"
FIXTURE="${FIXTURE:-${REPO_ROOT}/test/perf/stress-5k-resources.sh}"
KUBEATLAS_PORT="${KUBEATLAS_PORT:-18080}"

CLUSTERS=(prod staging)

PATH="${PATH}:/home/nick/go/bin"
export PATH

for cmd in docker kind kubectl curl jq go; do
  command -v "${cmd}" >/dev/null || { echo "missing required tool: ${cmd}" >&2; exit 1; }
done

mkdir -p "${STATE_DIR}/kubeconfigs" "${STATE_DIR}/logs"

echo "==> Creating kind clusters"
for c in "${CLUSTERS[@]}"; do
  name="kubeatlas-${c}"
  if kind get clusters | grep -qx "${name}"; then
    echo "  reusing existing cluster ${name}"
  else
    echo "  creating ${name}"
    kind create cluster --name "${name}" --wait 120s >/dev/null
  fi
  # Export the kubeconfig with internal kubelet-style endpoints so a
  # process on the docker host (this script) can dial the apiserver.
  # --internal would point at docker-container DNS names that only
  # resolve inside the docker network; the default form uses
  # 127.0.0.1:<published-port> on the host's bridge, which is what
  # we want here.
  kind get kubeconfig --name "${name}" > "${STATE_DIR}/kubeconfigs/${c}"
done

if [[ "${SKIP_FIXTURE:-0}" != "1" ]]; then
  echo "==> Loading stress fixture into each cluster"
  if [[ ! -x "${FIXTURE}" ]]; then
    echo "  fixture script ${FIXTURE} is not executable" >&2; exit 1
  fi
  for c in "${CLUSTERS[@]}"; do
    name="kubeatlas-${c}"
    echo "  ${name}: ${FIXTURE}"
    KUBECONFIG="${STATE_DIR}/kubeconfigs/${c}" bash "${FIXTURE}" \
      >"${STATE_DIR}/logs/fixture-${c}.log" 2>&1
  done
else
  echo "==> Skipping fixture load (SKIP_FIXTURE=1)"
fi

echo "==> Building kubeatlas binary"
( cd "${REPO_ROOT}" && CGO_ENABLED=0 go build -o bin/kubeatlas ./cmd/kubeatlas )

echo "==> Starting kubeatlas in multicluster mode on :${KUBEATLAS_PORT}"
LOG="${STATE_DIR}/logs/kubeatlas.log"
KUBEATLAS_BACKEND=memory \
KUBEATLAS_MULTICLUSTER_ENABLED=true \
KUBEATLAS_MULTICLUSTER_KUBECONFIG_DIR="${STATE_DIR}/kubeconfigs" \
KUBEATLAS_API_ADDR=":${KUBEATLAS_PORT}" \
  nohup "${REPO_ROOT}/bin/kubeatlas" \
  >"${LOG}" 2>&1 &
echo $! > "${STATE_DIR}/kubeatlas.pid"

echo "  pid $(cat "${STATE_DIR}/kubeatlas.pid"), logs at ${LOG}"
echo "==> Waiting for /readyz"
url="http://127.0.0.1:${KUBEATLAS_PORT}"
for _ in $(seq 60); do
  if curl -fsS "${url}/readyz" >/dev/null 2>&1; then
    echo "  ready"
    break
  fi
  sleep 1
done
if ! curl -fsS "${url}/readyz" >/dev/null 2>&1; then
  echo "kubeatlas did not become ready within 60s; tail of log:" >&2
  tail -30 "${LOG}" >&2
  exit 1
fi

echo
echo "Federation is up. Next steps:"
echo "  curl -s ${url}/api/v1/federation/clusters | jq"
echo "  KUBEATLAS_URL=${url} bash test/perf/multicluster-merge-bench.sh"
echo "  KUBEATLAS_URL=${url} bash test/perf/bench-v1.sh   # Tier 1 single-cluster numbers"
echo "  bash test/perf/stop-multicluster-fixture.sh        # tear it down"
