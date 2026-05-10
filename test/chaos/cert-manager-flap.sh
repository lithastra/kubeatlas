#!/usr/bin/env bash
# test/chaos/cert-manager-flap.sh - P2-T26 chaos scenario.
#
# Scenario: restart every cert-manager Pod (controller +
# webhook + cainjector) and verify any chart-managed
# Certificate stays Ready or returns to Ready within a budget.
#
# Skipped when no chart-managed Certificate exists in the
# kubeatlas namespace — running the scenario without one would
# only verify "cert-manager survives a restart", which is
# cert-manager's own gate to keep, not ours.
#
# Expected behaviour:
#   - The Certificate's Ready condition stays True or returns
#     to True within 90s of the last cert-manager Pod becoming
#     Ready again.
#   - kubeatlas /healthz keeps returning 200 — the kubeatlas
#     process is unaffected by cert-manager flapping; it only
#     consumes the secret cert-manager produces.

set -euo pipefail

NS="${NS:-kubeatlas}"
RELEASE="${RELEASE:-kubeatlas}"
CERT_MANAGER_NS="${CERT_MANAGER_NS:-cert-manager}"
KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT:-18080}"

for cmd in kubectl curl; do
  command -v "${cmd}" >/dev/null || { echo "missing: ${cmd}" >&2; exit 1; }
done

CERT_NAME=$(kubectl get certificate -n "${NS}" \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "${CERT_NAME}" ]]; then
  echo "cert-manager-flap: SKIPPED (no Certificate found in ${NS}; chart-managed TLS is off)"
  exit 0
fi
echo "Targeting Certificate: ${NS}/${CERT_NAME}"

echo "==> Snapshot Certificate Ready condition"
ready_before=$(kubectl get certificate "${CERT_NAME}" -n "${NS}" \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
echo "Ready before chaos: ${ready_before}"

echo "==> Restarting every cert-manager Deployment"
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  if kubectl get deploy -n "${CERT_MANAGER_NS}" "${d}" >/dev/null 2>&1; then
    kubectl rollout restart -n "${CERT_MANAGER_NS}" "deploy/${d}" >/dev/null
  fi
done

echo "==> Waiting up to 120s for cert-manager rollouts to converge"
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  if kubectl get deploy -n "${CERT_MANAGER_NS}" "${d}" >/dev/null 2>&1; then
    kubectl rollout status -n "${CERT_MANAGER_NS}" "deploy/${d}" --timeout=120s >/dev/null
  fi
done

echo "==> Waiting up to 90s for Certificate Ready=True"
deadline=$((SECONDS + 90))
ready_after=""
while (( SECONDS < deadline )); do
  ready_after=$(kubectl get certificate "${CERT_NAME}" -n "${NS}" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
  [[ "${ready_after}" == "True" ]] && break
  sleep 3
done
echo "Ready after chaos: ${ready_after}"
if [[ "${ready_after}" != "True" ]]; then
  echo "Certificate did not return to Ready=True within 90s of cert-manager restart"
  exit 1
fi

echo "==> Confirming kubeatlas /healthz still 200"
curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/healthz" >/dev/null

echo
echo "cert-manager-flap: Certificate ${NS}/${CERT_NAME} survived cert-manager restart."
