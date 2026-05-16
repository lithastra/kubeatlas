#!/usr/bin/env bash
# test/chaos/snapshot-write-storm.sh
#
# Scenario (P3-T6 / F-111): create ~1000 ConfigMaps as fast as
# kubectl can apply them. Every create is one informer event, so the
# snapshot writer's queue receives a burst far faster than four
# workers drain it into PostgreSQL.
#
# Expected behaviour:
#   - The kubeatlas process does NOT crash — Enqueue is non-blocking
#     and the worker pool absorbs the burst behind the queue.
#   - The queue-full backpressure valve sheds the overflow rather
#     than blocking the informer: kubeatlas_snapshot_queue_drop_total
#     may be non-zero, but the drop ratio stays under 10 %.
#   - kubeatlas_snapshot_events_processed_total climbs by roughly the
#     burst size (minus whatever was shed).
#
# This is a Tier 2 chaos scenario — it asserts nothing useful on a
# Tier 1 install (no snapshot writer), so it self-skips when the
# /metrics scrape shows no snapshot block.
#
# Exit code: 0 if the process survived and the drop ratio is within
# budget; 1 otherwise.
#
# Env:
#   KUBEATLAS_PF_PORT  port the kubeatlas API is reachable on
#                      (default 18081 — phase3.sh's port-forward).
#   COUNT              number of ConfigMaps (default 1000).
#   NS                 storm namespace (default petclinic-snap-storm).
#   DROP_BUDGET_PCT    max acceptable drop ratio (default 10).

set -euo pipefail

PORT="${KUBEATLAS_PF_PORT:-18081}"
COUNT="${COUNT:-1000}"
NS="${NS:-petclinic-snap-storm}"
DROP_BUDGET_PCT="${DROP_BUDGET_PCT:-10}"
CLEANUP="${CLEANUP:-1}"
[ "${1:-}" = "--no-cleanup" ] && CLEANUP=0

api() { curl -fsS --max-time 10 "http://127.0.0.1:${PORT}$1"; }

# metric NAME -> the counter value from /metrics, or 0 if absent.
metric() {
  api /metrics 2>/dev/null | awk -v n="$1" '$1 == n { print $2 }' | tail -n1
}

echo "==> Preflight: confirm the snapshot writer is running (Tier 2)"
if ! api /metrics 2>/dev/null | grep -q '^kubeatlas_snapshot_events_processed_total'; then
  echo "SKIP: /metrics has no snapshot block — Tier 1 or snapshots.enabled=false."
  echo "      The snapshot-write-storm scenario only applies to a Tier 2 install."
  exit 0
fi

processed_before=$(metric kubeatlas_snapshot_events_processed_total); processed_before=${processed_before:-0}
dropped_before=$(metric kubeatlas_snapshot_queue_drop_total);         dropped_before=${dropped_before:-0}
echo "    baseline: processed=${processed_before} dropped=${dropped_before}"

echo "==> Creating storm namespace ${NS}"
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

echo "==> Applying ${COUNT} ConfigMaps as fast as possible"
start=$(date +%s)
for i in $(seq 1 "${COUNT}"); do
  cat <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: snap-storm-${i}
  namespace: ${NS}
data:
  i: "${i}"
---
YAML
done | kubectl apply -f - >/dev/null
echo "    kubectl apply took $(( $(date +%s) - start ))s"

echo "==> Waiting 45s for the writer to drain the queue"
sleep 45

echo "==> Asserting the kubeatlas process survived the burst"
if ! kubectl get pod -n kubeatlas -l app.kubernetes.io/name=kubeatlas \
    -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null \
    | grep -qw True; then
  echo "FAIL: no Ready kubeatlas Pod after the storm — the process did not survive."
  rc=1
else
  echo "    kubeatlas Pod still Ready"
  rc=0
fi

processed_after=$(metric kubeatlas_snapshot_events_processed_total); processed_after=${processed_after:-0}
dropped_after=$(metric kubeatlas_snapshot_queue_drop_total);         dropped_after=${dropped_after:-0}
processed_delta=$(( processed_after - processed_before ))
dropped_delta=$(( dropped_after - dropped_before ))
total_delta=$(( processed_delta + dropped_delta ))
echo "    during storm: processed=${processed_delta} dropped=${dropped_delta}"

if (( total_delta == 0 )); then
  echo "FAIL: the writer recorded no events during the storm — is it wired in?"
  rc=1
elif (( rc == 0 )); then
  # drop ratio = dropped / (processed + dropped), as an integer %.
  drop_pct=$(( dropped_delta * 100 / total_delta ))
  echo "    drop ratio: ${drop_pct}% (budget ${DROP_BUDGET_PCT}%)"
  if (( drop_pct > DROP_BUDGET_PCT )); then
    echo "FAIL: drop ratio ${drop_pct}% exceeds the ${DROP_BUDGET_PCT}% budget."
    rc=1
  else
    echo "OK: process survived; drop ratio ${drop_pct}% within budget."
  fi
fi

if [ "${CLEANUP}" = "1" ]; then
  echo "==> Cleaning up namespace ${NS}"
  kubectl delete namespace "${NS}" --ignore-not-found --wait=false >/dev/null
fi

exit "${rc}"
