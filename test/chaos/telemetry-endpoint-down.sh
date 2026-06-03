#!/usr/bin/env bash
# test/chaos/telemetry-endpoint-down.sh
#
# Scenario: opt-in telemetry is enabled, but the receiver endpoint
# (telemetry.kubeatlas.dev) is unreachable. A NetworkPolicy blocks the
# kubeatlas Pod's egress except to DNS and the apiserver, so the
# fire-and-forget telemetry POST fails.
#
# Expected behaviour (anti-pattern 89 / invariant 2.3): the failed send
# is logged and counted (kubeatlas_telemetry_send_errors_total), but the
# main path is completely unaffected — /api/v1/graph keeps serving 200
# and the process never blocks or exits.
#
# Prerequisites:
#   - A cluster (kind works) with a CNI that ENFORCES NetworkPolicy
#     egress (kind's default kindnet does NOT enforce egress; install
#     Calico, or run this where egress policy is honoured).
#   - kubeatlas installed with telemetry enabled and a short interval:
#       helm upgrade kubeatlas ... \
#         --set telemetry.enabled=true \
#         --set-string 'extraEnv[0].name=KUBEATLAS_TELEMETRY_INTERVAL_SECONDS' \
#         --set-string 'extraEnv[0].value=15'
#     (or set KUBEATLAS_TELEMETRY_INTERVAL_SECONDS on the Deployment).
#
# Exit code: 0 if the invariants held.

set -euo pipefail

NS="${KUBEATLAS_NAMESPACE:-kubeatlas}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
PF_PORT="${KUBEATLAS_PF_PORT:-18083}"
WAIT_SECONDS="${WAIT_SECONDS:-90}"

red()   { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
step()  { printf '\033[33m▶ %s\033[0m\n' "$*"; }
fail()  { red "  ✗ $*"; exit 1; }
pass()  { green "  ✓ $*"; }

cleanup() {
  kubectl delete networkpolicy -n "${NS}" chaos-telemetry-block --ignore-not-found >/dev/null 2>&1 || true
  if [[ -n "${PF_PID:-}" ]]; then kill "${PF_PID}" 2>/dev/null || true; fi
}
trap cleanup EXIT

step "block egress from the kubeatlas Pod except DNS + apiserver"
kubectl apply -n "${NS}" -f - >/dev/null <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: chaos-telemetry-block
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: ${RELEASE}
  policyTypes: [Egress]
  egress:
    # DNS only — every other egress (the HTTPS telemetry POST) is denied.
    - to: []
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
EOF
pass "egress NetworkPolicy applied"

step "port-forward the API"
kubectl port-forward -n "${NS}" "deploy/${RELEASE}" "${PF_PORT}:8080" >/tmp/ka-chaos-tel.log 2>&1 &
PF_PID=$!
for _ in $(seq 1 30); do
  curl -fsS --max-time 1 "http://127.0.0.1:${PF_PORT}/healthz" >/dev/null 2>&1 && break
  sleep 1
done

step "wait for at least one telemetry send to fail (${WAIT_SECONDS}s budget)"
errors=0
deadline=$((SECONDS + WAIT_SECONDS))
while (( SECONDS < deadline )); do
  errors=$(curl -fsS "http://127.0.0.1:${PF_PORT}/metrics" 2>/dev/null \
    | awk '/^kubeatlas_telemetry_send_errors_total/ {print $2}')
  errors=${errors:-0}
  (( errors > 0 )) && break
  # The main path must stay up throughout.
  code=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PF_PORT}/api/v1/graph?level=cluster")
  [[ "${code}" == "200" ]] || fail "main path returned ${code} during the outage"
  sleep 3
done

(( errors > 0 )) || fail "no telemetry send errors after ${WAIT_SECONDS}s (is telemetry enabled with a short interval? is egress actually enforced?)"
pass "telemetry send failed and was counted (errors=${errors})"

step "confirm the main path is still healthy"
code=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PF_PORT}/api/v1/graph?level=cluster")
[[ "${code}" == "200" ]] || fail "main path returned ${code} after the outage"
pass "main path still serves 200"

green "[chaos/telemetry-endpoint-down] PASS"
