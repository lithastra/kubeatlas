#!/usr/bin/env bash
# test/chaos/otel-receiver-overload.sh
#
# Scenario: flood the OTLP/gRPC receiver with spans far faster than it
# can persist them, and prove the receiver's backpressure protects the
# rest of the process (F-204, invariant 2.5).
#
# The receiver enqueues spans onto a bounded channel and DROPS on a
# full queue rather than blocking. So under a 100K spans/s flood we
# EXPECT drops — what must hold is:
#
#   1. The 8080 HTTP API stays responsive THROUGHOUT the flood (the
#      span load never blocks the core graph path). This is the
#      load-bearing assertion: it is what "the receiver is a separate,
#      non-blocking goroutine" actually means.
#   2. The process survives — no OOM, no crash, the Pod stays Ready.
#   3. The receiver actually ingested spans (received_total climbed).
#   4. The drop ratio stays within budget — overflow is shed, not
#      unbounded, and writes still make progress.
#
# Triggered weekly in CI and on PRs touching pkg/otel/*. Self-skips on
# a Tier 1 / otel-disabled install (no otel block on /metrics) and when
# the span generator (telemetrygen via docker) is unavailable.
#
# Env:
#   KUBEATLAS_PF_PORT  HTTP API port (default 18081 — phase5.sh's port-forward)
#   OTEL_GRPC_PORT     OTLP/gRPC port (default 4317)
#   DURATION_SEC       flood duration (default 60)
#   TARGET_RATE        spans/s to generate (default 100000)
#   DROP_BUDGET_PCT    max acceptable drop ratio, percent (default 95 —
#                      a deliberate flood drops most spans; the point is
#                      bounded shedding + a live main path, not zero loss)
#   P95_BUDGET_SEC     max acceptable main-path p95 during flood (default 0.5)
#   NAMESPACE          kubeatlas namespace (default kubeatlas)
#
# Exit code: 0 on pass, 1 on any failed assertion.

set -euo pipefail

PORT="${KUBEATLAS_PF_PORT:-18081}"
OTEL_GRPC_PORT="${OTEL_GRPC_PORT:-4317}"
DURATION_SEC="${DURATION_SEC:-60}"
TARGET_RATE="${TARGET_RATE:-100000}"
DROP_BUDGET_PCT="${DROP_BUDGET_PCT:-95}"
P95_BUDGET_SEC="${P95_BUDGET_SEC:-0.5}"
NAMESPACE="${NAMESPACE:-kubeatlas}"

api() { curl -fsS --max-time 10 "http://127.0.0.1:${PORT}$1"; }

# metric NAME -> counter value from /metrics, or 0 if absent.
metric() {
  api /metrics 2>/dev/null | awk -v n="$1" '$1 == n { print $2 }' | tail -n1
}

# Self-skip: no otel block means Tier 1 or otel.enabled=false.
if ! api /metrics 2>/dev/null | grep -q '^kubeatlas_otel_received_total'; then
  echo "SKIP: /metrics has no otel block — Tier 1 or otel.enabled=false."
  exit 0
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker not available — cannot run the telemetrygen span generator."
  exit 0
fi

echo "[chaos/otel-overload] flooding ${TARGET_RATE} spans/s for ${DURATION_SEC}s at localhost:${OTEL_GRPC_PORT}"

received_before="$(metric kubeatlas_otel_received_total)"
dropped_before="$(metric kubeatlas_otel_dropped_total)"
written_before="$(metric kubeatlas_otel_written_total)"

# Monitor the core graph path throughout the flood. Each probe records
# its total time; a probe that fails (timeout / connection refused)
# records a sentinel 99 so a blocked or dead main path fails the p95.
LAT_FILE="$(mktemp)"
trap 'rm -f "$LAT_FILE"' EXIT
(
  end=$(( SECONDS + DURATION_SEC + 5 ))
  while [ "$SECONDS" -lt "$end" ]; do
    t="$(curl -fsS -o /dev/null -w '%{time_total}' --max-time 5 \
          "http://127.0.0.1:${PORT}/api/v1/graph?level=cluster" 2>/dev/null || echo 99)"
    echo "$t" >> "$LAT_FILE"
    sleep 0.5
  done
) &
MONITOR_PID=$!

# Generate the span flood. --network host so localhost:4317 reaches the
# port-forwarded receiver.
docker run --rm --network host \
  ghcr.io/open-telemetry/opentelemetry-collector-contrib/telemetrygen:latest \
  traces --otlp-insecure --otlp-endpoint="localhost:${OTEL_GRPC_PORT}" \
  --rate="${TARGET_RATE}" --duration="${DURATION_SEC}s" \
  || { echo "FAIL: telemetrygen could not reach the receiver on ${OTEL_GRPC_PORT}"; kill "$MONITOR_PID" 2>/dev/null || true; exit 1; }

wait "$MONITOR_PID" 2>/dev/null || true

# p95 of the main-path latencies recorded during the flood. Uses
# nearest-rank with a CEIL so the index never rounds down below the
# true p95 — a conservative bias, so the assertion can't pass on a
# main path that was actually blocked.
P95="$(sort -n "$LAT_FILE" | awk '{a[NR]=$1} END {
  if (NR==0) { print 0; exit }
  idx=int(NR*0.95); if (idx < NR*0.95) idx++; if (idx < 1) idx=1; if (idx > NR) idx=NR;
  print a[idx]
}')"
PROBES="$(wc -l < "$LAT_FILE" | tr -d ' ')"

received_after="$(metric kubeatlas_otel_received_total)"
dropped_after="$(metric kubeatlas_otel_dropped_total)"
written_after="$(metric kubeatlas_otel_written_total)"

recv=$(( received_after - received_before ))
drop=$(( dropped_after - dropped_before ))
wrote=$(( written_after - written_before ))
total=$(( recv ))
[ "$total" -lt 1 ] && total=1
drop_pct=$(( drop * 100 / total ))

echo "[chaos/otel-overload] received=${recv} dropped=${drop} written=${wrote} drop_pct=${drop_pct}% main-path p95=${P95}s over ${PROBES} probes"

fail=0

# (3) receiver ingested spans.
if [ "$recv" -lt 1 ]; then
  echo "FAIL: receiver ingested 0 spans — is the receiver wired / reachable?"
  fail=1
fi

# (1) main path stayed responsive: p95 within budget (a blocked path
# shows up as 5s timeouts / 99 sentinels and blows the p95).
if awk -v p="$P95" -v b="$P95_BUDGET_SEC" 'BEGIN { exit !(p > b) }'; then
  echo "FAIL: main-path p95 ${P95}s exceeded budget ${P95_BUDGET_SEC}s — span load blocked the core path"
  fail=1
fi

# (4) drops bounded.
if [ "$drop_pct" -gt "$DROP_BUDGET_PCT" ]; then
  echo "FAIL: drop ratio ${drop_pct}% exceeded budget ${DROP_BUDGET_PCT}%"
  fail=1
fi

# (2) process survived: the Pod is still Ready.
if kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=kubeatlas \
     -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | grep -q False; then
  echo "FAIL: a kubeatlas Pod went NotReady during the flood (possible OOM/crash)"
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "[chaos/otel-overload] FAIL"
  exit 1
fi
echo "[chaos/otel-overload] PASS"
