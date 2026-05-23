#!/usr/bin/env bash
# scripts/dev-tier2.sh
#
# Bring up a local Tier 2 KubeAtlas: PostgreSQL container + the
# kubeatlas binary in postgres mode with snapshots enabled. Used
# while testing the time-axis diff view and any other feature that
# needs persistent state.
#
# Outline:
#   1. Start (or re-use) a postgres:16 container named kubeatlas-pg
#      on :5432 with kubeatlas/kubeatlas/kubeatlas creds.
#   2. Wait for it to accept connections.
#   3. Build bin/kubeatlas if missing (CGO-free).
#   4. Start kubeatlas with KUBEATLAS_BACKEND=postgres + snapshots
#      enabled. Writes PID + logs under STATE_DIR for the companion
#      stop script.
#   5. Wait for /readyz, then print the local URLs and how to tear
#      down.
#
# Idempotency: re-running re-uses both the container and the
# kubeconfig the user already has on $KUBECONFIG. Run
# scripts/dev-tier2-stop.sh to tear everything down.
#
# Environment knobs:
#   STATE_DIR        Where PID + logs land. Default: /tmp/kubeatlas-dev.
#   KUBEATLAS_PORT   Port the local kubeatlas process listens on.
#                    Default: 8080 so the web dev server's proxy works
#                    out of the box.
#   PG_PORT          Host port for the postgres container.
#                    Default: 5432.
#   PG_IMAGE         Postgres image to run. Default: postgres:16.
#   PG_NAME          Container name. Default: kubeatlas-pg.
#   SNAPSHOT_RETENTION  Passed through as KUBEATLAS_SNAPSHOTS_RETENTION.
#                       Default: 7d (the server's own default).
#   KUBEATLAS_BIN    Override the binary path. Default:
#                    <repo>/bin/kubeatlas (rebuilt on demand).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${STATE_DIR:-/tmp/kubeatlas-dev}"
KUBEATLAS_PORT="${KUBEATLAS_PORT:-8080}"
PG_PORT="${PG_PORT:-5432}"
PG_IMAGE="${PG_IMAGE:-postgres:16}"
PG_NAME="${PG_NAME:-kubeatlas-pg}"
SNAPSHOT_RETENTION="${SNAPSHOT_RETENTION:-7d}"
KUBEATLAS_BIN="${KUBEATLAS_BIN:-${REPO_ROOT}/bin/kubeatlas}"

PG_DSN="postgres://kubeatlas:kubeatlas@localhost:${PG_PORT}/kubeatlas?sslmode=disable"

for cmd in docker go curl; do
  command -v "${cmd}" >/dev/null || { echo "missing required tool: ${cmd}" >&2; exit 1; }
done

mkdir -p "${STATE_DIR}/logs"

# 1. Postgres container ------------------------------------------------
if docker inspect "${PG_NAME}" >/dev/null 2>&1; then
  if [[ "$(docker inspect -f '{{.State.Running}}' "${PG_NAME}")" == "true" ]]; then
    echo "==> Re-using running postgres container ${PG_NAME}"
  else
    echo "==> Restarting existing postgres container ${PG_NAME}"
    docker start "${PG_NAME}" >/dev/null
  fi
else
  echo "==> Starting postgres container ${PG_NAME} on :${PG_PORT}"
  docker run -d \
    --name "${PG_NAME}" \
    -e POSTGRES_USER=kubeatlas \
    -e POSTGRES_PASSWORD=kubeatlas \
    -e POSTGRES_DB=kubeatlas \
    -p "${PG_PORT}:5432" \
    "${PG_IMAGE}" >/dev/null
fi

echo "==> Waiting for postgres to accept connections"
for _ in $(seq 30); do
  if docker exec "${PG_NAME}" pg_isready -U kubeatlas -d kubeatlas >/dev/null 2>&1; then
    echo "  ready"
    break
  fi
  sleep 1
done
if ! docker exec "${PG_NAME}" pg_isready -U kubeatlas -d kubeatlas >/dev/null 2>&1; then
  echo "postgres did not become ready within 30s" >&2
  docker logs --tail 20 "${PG_NAME}" >&2
  exit 1
fi

# 2. Build the binary if missing ---------------------------------------
if [[ ! -x "${KUBEATLAS_BIN}" ]]; then
  echo "==> Building kubeatlas binary"
  ( cd "${REPO_ROOT}" && CGO_ENABLED=0 go build -o bin/kubeatlas ./cmd/kubeatlas )
fi

# 3. Kill any prior kubeatlas-dev process the script started ----------
if [[ -f "${STATE_DIR}/kubeatlas.pid" ]]; then
  prev_pid="$(cat "${STATE_DIR}/kubeatlas.pid")"
  if kill -0 "${prev_pid}" 2>/dev/null; then
    echo "==> Stopping previous kubeatlas pid ${prev_pid}"
    kill "${prev_pid}" || true
    sleep 1
  fi
fi

# 4. Start kubeatlas in Tier 2 mode -----------------------------------
echo "==> Starting kubeatlas in Tier 2 mode on :${KUBEATLAS_PORT}"
LOG="${STATE_DIR}/logs/kubeatlas.log"
KUBEATLAS_BACKEND=postgres \
PGCONN="${PG_DSN}" \
KUBEATLAS_SNAPSHOTS_ENABLED=true \
KUBEATLAS_SNAPSHOTS_RETENTION="${SNAPSHOT_RETENTION}" \
KUBEATLAS_API_ADDR=":${KUBEATLAS_PORT}" \
  nohup "${KUBEATLAS_BIN}" \
  >"${LOG}" 2>&1 &
echo $! > "${STATE_DIR}/kubeatlas.pid"

echo "  pid $(cat "${STATE_DIR}/kubeatlas.pid"), logs at ${LOG}"

# 5. Wait for /readyz --------------------------------------------------
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

cat <<EOF

Tier 2 is up.

  API           ${url}
  Postgres DSN  ${PG_DSN}
  Logs          ${LOG}

Quick checks:
  curl -s ${url}/api/v1/snapshots | jq
  curl -s "${url}/api/v1/snapshots/diff?from=1h&to=now" | jq

To exercise the time-axis diff:
  1. Wait for the first snapshot (default cron ~5 min).
  2. kubectl apply / scale / delete something.
  3. Pick "1h ago" on the TimeAxisBar after the next snapshot fires.

Tear down with:
  bash scripts/dev-tier2-stop.sh
EOF
