#!/usr/bin/env bash
# test/perf/stop-multicluster-fixture.sh
#
# Tear down everything start-multicluster-fixture.sh created:
#   1. The local kubeatlas process (PID stored in ${STATE_DIR}/kubeatlas.pid).
#   2. Both kind clusters (kubeatlas-prod, kubeatlas-staging).
#   3. The state directory itself.
#
# Idempotent: missing PID, missing clusters, or a missing state
# directory are all logged but never errored — the script's job is
# to leave the host clean regardless of how the previous run died.

set -euo pipefail

STATE_DIR="${STATE_DIR:-/tmp/kubeatlas-fixture}"
CLUSTERS=(prod staging)

PATH="${PATH}:/home/nick/go/bin"
export PATH

if [[ -f "${STATE_DIR}/kubeatlas.pid" ]]; then
  pid=$(cat "${STATE_DIR}/kubeatlas.pid")
  if kill -0 "${pid}" 2>/dev/null; then
    echo "Stopping kubeatlas (pid ${pid})"
    kill "${pid}" || true
    # Give it a moment to flush; SIGKILL fallback if still alive.
    for _ in $(seq 10); do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "${pid}" 2>/dev/null; then
      kill -9 "${pid}" || true
    fi
  else
    echo "kubeatlas pid ${pid} not running"
  fi
else
  echo "no kubeatlas.pid in ${STATE_DIR}; skipping process stop"
fi

if command -v kind >/dev/null; then
  for c in "${CLUSTERS[@]}"; do
    name="kubeatlas-${c}"
    if kind get clusters | grep -qx "${name}"; then
      echo "Deleting kind cluster ${name}"
      kind delete cluster --name "${name}" >/dev/null
    fi
  done
else
  echo "kind not on PATH; skipping cluster deletion"
fi

if [[ -d "${STATE_DIR}" ]]; then
  echo "Removing state directory ${STATE_DIR}"
  rm -rf "${STATE_DIR}"
fi

echo "Done."
