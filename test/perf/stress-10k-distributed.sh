#!/usr/bin/env bash

# Build the representative v1.6 10K fixture across ten namespaces. The legacy
# stress-10k-resources.sh deliberately puts everything in one namespace and is
# retained as the pathological-layout observation. This distributed fixture is
# the latency-gated production shape; both use the same deterministic generator.

set -euo pipefail

DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
NAMESPACE_PREFIX="${NAMESPACE_PREFIX:-stress-test-10k}"
NAMESPACE_COUNT="${NAMESPACE_COUNT:-10}"
NUM_CM_PER_NAMESPACE="${NUM_CM_PER_NAMESPACE:-1000}"
NUM_DEP_PER_NAMESPACE="${NUM_DEP_PER_NAMESPACE:-200}"
NUM_SVC_PER_NAMESPACE="${NUM_SVC_PER_NAMESPACE:-40}"

[[ "${NAMESPACE_COUNT}" =~ ^[1-9][0-9]*$ ]] \
  || { echo "NAMESPACE_COUNT must be a positive integer" >&2; exit 1; }

for index in $(seq 0 $((NAMESPACE_COUNT - 1))); do
  namespace=$(printf '%s-%02d' "${NAMESPACE_PREFIX}" "${index}")
  printf '==> Populating %s (%s/%s)\n' \
    "${namespace}" "$((index + 1))" "${NAMESPACE_COUNT}"
  NS="${namespace}" \
  NUM_CM="${NUM_CM_PER_NAMESPACE}" \
  NUM_DEP="${NUM_DEP_PER_NAMESPACE}" \
  NUM_SVC="${NUM_SVC_PER_NAMESPACE}" \
    bash "${DIR}/stress-5k-resources.sh"
done

printf '\nDistributed fixture ready. Namespaces:\n'
for index in $(seq 0 $((NAMESPACE_COUNT - 1))); do
  printf '  %s-%02d\n' "${NAMESPACE_PREFIX}" "${index}"
done
