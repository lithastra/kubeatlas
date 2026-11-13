#!/usr/bin/env bash

# Run the same-origin Web, API and CLI smoke against an isolated CI install.
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "${ROOT_DIR}"
namespace=${1:?usage: impact-deployed.sh NAMESPACE RELEASE TIER}
release=${2:?missing Helm release}
tier=${3:?missing storage tier}
case "$tier" in
  tier1) durable=0 ;;
  tier2) durable=1 ;;
  *) printf 'unknown storage tier: %s\n' "$tier" >&2; exit 1 ;;
esac

kubectl apply -n "$namespace" -f - <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: impact-settings
data:
  mode: smoke
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: impact-consumer
spec:
  replicas: 0
  selector:
    matchLabels:
      app: impact-consumer
  template:
    metadata:
      labels:
        app: impact-consumer
    spec:
      containers:
        - name: consumer
          image: registry.k8s.io/pause:3.10
          envFrom:
            - configMapRef:
                name: impact-settings
YAML

export KUBEATLAS_E2E_IMPACT_URL=http://127.0.0.1:18087
export KUBEATLAS_E2E_IMPACT_CLI="${ROOT_DIR}/bin/kubectl-atlas"
export KUBEATLAS_E2E_IMPACT_NAMESPACE="$namespace"
export KUBEATLAS_E2E_IMPACT_CONFIGMAP=impact-settings
export KUBEATLAS_E2E_IMPACT_DEPLOYMENT=impact-consumer
KUBEATLAS_E2E_IMPACT_CONFIGMAP_UID=$(kubectl get -n "$namespace" configmap impact-settings -o jsonpath='{.metadata.uid}')
KUBEATLAS_E2E_IMPACT_DEPLOYMENT_UID=$(kubectl get -n "$namespace" deployment impact-consumer -o jsonpath='{.metadata.uid}')
export KUBEATLAS_E2E_IMPACT_CONFIGMAP_UID KUBEATLAS_E2E_IMPACT_DEPLOYMENT_UID

log_file="${TMPDIR:-/tmp}/kubeatlas-impact-${tier}-port-forward.log"
kubectl port-forward -n "$namespace" --address 127.0.0.1 \
  "service/${release}" 18087:80 >"$log_file" 2>&1 &
forward_pid=$!
stop_forward() {
  kill "$forward_pid" 2>/dev/null || true
  wait "$forward_pid" 2>/dev/null || true
}
trap stop_forward EXIT

for _ in $(seq 1 30); do
  kill -0 "$forward_pid" 2>/dev/null || break
  if curl -fsS --max-time 1 "${KUBEATLAS_E2E_IMPACT_URL}/readyz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! kill -0 "$forward_pid" 2>/dev/null; then
  cat "$log_file" >&2
  exit 1
fi

commit=$(git rev-parse HEAD)
version=$(helm show chart helm/kubeatlas | awk '/^version: / {print $2}')
curl -fsS --max-time 5 "${KUBEATLAS_E2E_IMPACT_URL}/api/v1/info" \
  | jq -e --arg commit "$commit" --arg version "$version" \
    '.commit == $commit and .version == $version and .graphstore_version == "v2"'
"$KUBEATLAS_E2E_IMPACT_CLI" --version | grep -F "${version} (commit ${commit},"
curl -fsS --max-time 5 "${KUBEATLAS_E2E_IMPACT_URL}/metrics" \
  | grep -Fx "kubeatlas_storage_durable ${durable}"

PLAYWRIGHT_HTML_REPORT="playwright-deployed-${tier}-report" \
  npm --prefix web run test:impact-deployed -- --output "test-results-impact-${tier}"
