#!/usr/bin/env bash
# telemetry-egress.sh — network-level proof of the opt-in telemetry
# contract (P4-T5 step 9 / invariant 2.3): with telemetry DISABLED (the
# default) KubeAtlas makes ZERO outbound connections to the telemetry
# receiver; a positive control with telemetry ENABLED confirms the probe
# can actually observe egress (so a passing "disabled" run is meaningful,
# not just a broken probe).
#
# The receiver host is pinned to a TEST-NET-3 blackhole address in
# /etc/hosts and an iptables OUTPUT counter watches packets to it, so the
# test needs neither the real (separately, human-deployed) endpoint nor
# live DNS resolution. The caller sets up the pin + the DROP rule once;
# this script zeroes the counter per run so each window is measured
# independently.
#
# Usage:
#   telemetry-egress.sh disabled   # expect zero packets to the endpoint
#   telemetry-egress.sh enabled    # expect >0 packets (positive control)
#
# Env:
#   KUBEATLAS_BIN   path to the kubeatlas binary (default ./bin/kubeatlas)
#   BLACKHOLE_IP    pinned receiver address (default 203.0.113.7)
#   WATCH_SECONDS   observation window (default 65 disabled / 20 enabled)

set -euo pipefail

MODE="${1:?usage: telemetry-egress.sh <disabled|enabled>}"
BIN="${KUBEATLAS_BIN:-./bin/kubeatlas}"
BLACKHOLE="${BLACKHOLE_IP:-203.0.113.7}"
API_HOSTPORT="127.0.0.1:8080"

# counter_pkts reads the packet count of the OUTPUT rule targeting the
# blackhole address (its first column in iptables' numeric verbose list).
counter_pkts() {
  sudo iptables -nvxL OUTPUT | awk -v ip="${BLACKHOLE}" '$0 ~ ip {print $1; exit}'
}

# Zero the counter so this run measures only its own window.
sudo iptables -Z OUTPUT

declare -a env_extra
case "${MODE}" in
  disabled)
    watch="${WATCH_SECONDS:-65}"
    env_extra=(KUBEATLAS_TELEMETRY_ENABLED=false)
    ;;
  enabled)
    watch="${WATCH_SECONDS:-20}"
    env_extra=(KUBEATLAS_TELEMETRY_ENABLED=true KUBEATLAS_TELEMETRY_INTERVAL_SECONDS=2)
    ;;
  *)
    echo "telemetry-egress.sh: unknown mode ${MODE} (want disabled|enabled)" >&2
    exit 2
    ;;
esac

# GODEBUG=netdns=go forces the pure-Go resolver so the /etc/hosts pin is
# honoured (the cgo resolver may bypass it).
env "${env_extra[@]}" GODEBUG=netdns=go KUBEATLAS_API_ADDR=":8080" \
  "${BIN}" >"/tmp/kubeatlas-${MODE}.log" 2>&1 &
pid=$!
trap 'kill "${pid}" 2>/dev/null || true; wait "${pid}" 2>/dev/null || true' EXIT

# Wait for the HTTP server to accept before starting the timer.
healthy=
for _ in $(seq 1 60); do
  if curl -fsS "http://${API_HOSTPORT}/healthz" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 1
done
[[ -n "${healthy}" ]] || { echo "telemetry-egress.sh: server never became healthy" >&2; exit 1; }

echo "Observing telemetry egress for ${watch}s (mode=${MODE})..."
sleep "${watch}"

pkts="$(counter_pkts)"
pkts="${pkts:-0}"
echo "packets to the telemetry endpoint (${BLACKHOLE}): ${pkts}"

case "${MODE}" in
  disabled)
    if [[ "${pkts}" -ne 0 ]]; then
      echo "FAIL: telemetry disabled but ${pkts} packet(s) left for the endpoint" >&2
      exit 1
    fi
    echo "PASS: zero egress to the telemetry endpoint while disabled"
    ;;
  enabled)
    if [[ "${pkts}" -lt 1 ]]; then
      echo "FAIL: telemetry enabled but the probe saw no egress (control broken)" >&2
      exit 1
    fi
    echo "PASS: telemetry reached the endpoint while enabled (control)"
    ;;
esac
