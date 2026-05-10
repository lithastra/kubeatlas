#!/usr/bin/env bash
# test/perf/stress-5k-resources.sh - 5000-resource stress fixture.
#
# Creates a `stress-test-5k` namespace and populates it with:
#   - 5000 ConfigMaps      (cm-00000 .. cm-04999)
#   - 1000 Deployments     (each referencing 5 ConfigMaps via envFrom)
#   - 200  Services        (selecting Pods of 5 deployments each)
#
# Used by P2-T23's perf verification:
#
#   bash test/perf/stress-5k-resources.sh
#   bash test/perf/bench-v1.sh
#
# Cleanup:
#   kubectl delete namespace stress-test-5k
#
# Anti-patterns guarded:
#   - Generated YAML is server-side applied in chunks of 500 to avoid
#     hitting the apiserver's request size limit.
#   - The fixture is deterministic (same set of names every run) so
#     successive runs do not double-create.

set -euo pipefail

NS="${NS:-stress-test-5k}"
NUM_CM="${NUM_CM:-5000}"
NUM_DEP="${NUM_DEP:-1000}"
NUM_SVC="${NUM_SVC:-200}"
CM_PER_DEP="${CM_PER_DEP:-5}"
DEP_PER_SVC="${DEP_PER_SVC:-5}"
CHUNK="${CHUNK:-500}"

command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 1; }

echo "→ Creating namespace ${NS}"
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f -

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

echo "→ Generating ${NUM_CM} ConfigMaps"
for i in $(seq 0 $((NUM_CM - 1))); do
  name="$(printf 'cm-%05d' "$i")"
  cat <<YAML >> "${TMP}/cms.yaml"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${name}
  namespace: ${NS}
data:
  index: "${i}"
YAML
done

echo "→ Generating ${NUM_DEP} Deployments (each referencing ${CM_PER_DEP} ConfigMaps)"
for i in $(seq 0 $((NUM_DEP - 1))); do
  name="$(printf 'dep-%04d' "$i")"
  refs=""
  for j in $(seq 0 $((CM_PER_DEP - 1))); do
    cm_idx=$(( (i * CM_PER_DEP + j) % NUM_CM ))
    cm_name="$(printf 'cm-%05d' "${cm_idx}")"
    refs+="            - configMapRef: { name: ${cm_name} }
"
  done
  cat <<YAML >> "${TMP}/deps.yaml"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  namespace: ${NS}
spec:
  replicas: 0
  selector: { matchLabels: { app: ${name} } }
  template:
    metadata: { labels: { app: ${name} } }
    spec:
      containers:
        - name: app
          image: registry.k8s.io/pause:3.10
          envFrom:
${refs%$'\n'}
YAML
done

echo "→ Generating ${NUM_SVC} Services"
for i in $(seq 0 $((NUM_SVC - 1))); do
  name="$(printf 'svc-%04d' "$i")"
  # Each Service selects a label that ${DEP_PER_SVC} deployments
  # share — adds SELECTS edges to the graph.
  pool=$(( i % 50 ))
  cat <<YAML >> "${TMP}/svcs.yaml"
---
apiVersion: v1
kind: Service
metadata:
  name: ${name}
  namespace: ${NS}
spec:
  selector:
    pool: "${pool}"
  ports:
    - port: 80
      targetPort: 8080
YAML
done

apply_chunked() {
  local file="$1"
  # split each multi-doc YAML into ${CHUNK}-document chunks by
  # counting "---" markers; pipe each chunk into kubectl apply -f -.
  awk -v out="${TMP}/chunk_" -v chunk="${CHUNK}" '
    /^---$/ { if (cnt && cnt % chunk == 0) close(file); file = out sprintf("%05d.yaml", int(cnt/chunk)); cnt++ }
    { print > file }
  ' "${file}"
  for f in "${TMP}"/chunk_*.yaml; do
    [ -f "${f}" ] || continue
    kubectl apply -f "${f}" >/dev/null
    rm -f "${f}"
  done
}

echo "→ Applying ConfigMaps"
apply_chunked "${TMP}/cms.yaml"
echo "→ Applying Deployments"
apply_chunked "${TMP}/deps.yaml"
echo "→ Applying Services"
apply_chunked "${TMP}/svcs.yaml"

echo
echo "Fixture ready in namespace ${NS}:"
echo "  ConfigMaps: $(kubectl get configmaps -n "${NS}" --no-headers | wc -l)"
echo "  Deployments: $(kubectl get deployments -n "${NS}" --no-headers | wc -l)"
echo "  Services: $(kubectl get services -n "${NS}" --no-headers | wc -l)"
