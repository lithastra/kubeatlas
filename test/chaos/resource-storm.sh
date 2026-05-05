#!/usr/bin/env bash
# test/chaos/resource-storm.sh
#
# Scenario: 100 ConfigMaps applied as fast as kubectl can talk to
# the apiserver. Validates that the informer's event throughput keeps
# up with bursty creates and that WatchHub's per-subscription send
# buffer doesn't drop legitimate events.
#
# Expected behaviour (v0.1.0):
#   - Within 30 s of the last create, all 100 ConfigMaps are visible
#     on the Resources page (filter by namespace petclinic-storm).
#   - WatchHub's defaultSendBuffer (1024) is large enough that no
#     "dropping update for slow subscriber" warnings appear in
#     kubeatlas.log for a connected client.
#   - Informer event-handler latency stays <100 ms per event in
#     normal operation (eyeball with the metrics endpoint).
#
# Exit code: 0 if kubectl operations succeed.

set -euo pipefail

NS="${NS:-petclinic-storm}"
COUNT="${COUNT:-100}"
CLEANUP="${CLEANUP:-1}"

[ "${1:-}" = "--no-cleanup" ] && CLEANUP=0

echo "==> Creating storm namespace ${NS}"
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f -

echo "==> Applying ${COUNT} ConfigMaps as fast as possible"
start=$(date +%s)
for i in $(seq 1 "${COUNT}"); do
  cat <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: storm-${i}
  namespace: ${NS}
data:
  i: "${i}"
---
YAML
done | kubectl apply -f -
end=$(date +%s)
echo "kubectl apply took $((end - start)) seconds"

echo "==> Waiting up to 30 s for the namespace view to reach ${COUNT} ConfigMaps"
deadline=$(( $(date +%s) + 30 ))
last_count=0
while [ "$(date +%s)" -lt "${deadline}" ]; do
  last_count=$(curl -fsS "http://localhost:8080/api/v1alpha1/graph?level=namespace&namespace=${NS}" \
    | jq '[.nodes[] | select(.kind=="ConfigMap")] | length')
  echo "  ${last_count}/${COUNT}"
  if [ "${last_count}" -ge "${COUNT}" ]; then
    break
  fi
  sleep 2
done

echo
if [ "${last_count}" -ge "${COUNT}" ]; then
  echo "OK: kubeatlas saw all ${COUNT} ConfigMaps within 30 s."
else
  echo "WARN: only ${last_count}/${COUNT} visible after 30 s — check kubeatlas.log for"
  echo "      'dropping update for slow subscriber' or informer backpressure errors."
fi

if [ "${CLEANUP}" = "1" ]; then
  echo "==> Cleaning up namespace ${NS}"
  kubectl delete namespace "${NS}" --ignore-not-found
fi
