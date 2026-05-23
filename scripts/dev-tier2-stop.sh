#!/usr/bin/env bash
# scripts/dev-tier2-stop.sh
#
# Companion teardown for scripts/dev-tier2.sh. Stops the kubeatlas
# process started by the companion script and removes the postgres
# container. Idempotent — re-running after a clean teardown is a
# no-op.
#
# Environment knobs (match dev-tier2.sh):
#   STATE_DIR        Default: /tmp/kubeatlas-dev.
#   PG_NAME          Postgres container name. Default: kubeatlas-pg.
#   KEEP_PG          "1" to leave the postgres container running
#                    (useful if you want to inspect the schema or
#                    re-attach a kubeatlas to the same DB).

set -euo pipefail

STATE_DIR="${STATE_DIR:-/tmp/kubeatlas-dev}"
PG_NAME="${PG_NAME:-kubeatlas-pg}"
KEEP_PG="${KEEP_PG:-0}"

# 1. Stop the kubeatlas process if we have its PID.
if [[ -f "${STATE_DIR}/kubeatlas.pid" ]]; then
  pid="$(cat "${STATE_DIR}/kubeatlas.pid")"
  if kill -0 "${pid}" 2>/dev/null; then
    echo "==> Stopping kubeatlas pid ${pid}"
    kill "${pid}" || true
    # Give it a moment to drain. SIGKILL only if it lingers.
    for _ in $(seq 10); do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 0.5
    done
    kill -9 "${pid}" 2>/dev/null || true
  fi
  rm -f "${STATE_DIR}/kubeatlas.pid"
fi

# 2. Remove the postgres container unless asked to keep it.
if [[ "${KEEP_PG}" == "1" ]]; then
  echo "==> KEEP_PG=1 — leaving ${PG_NAME} running"
else
  if docker inspect "${PG_NAME}" >/dev/null 2>&1; then
    echo "==> Removing postgres container ${PG_NAME}"
    docker rm -f "${PG_NAME}" >/dev/null
  fi
fi

echo "Done."
