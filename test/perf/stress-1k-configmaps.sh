#!/usr/bin/env bash
# test/perf/stress-1k-configmaps.sh - 1000-resource stress fixture.
#
# Creates a `petclinic-stress` namespace and populates it with:
#   - 1000 ConfigMaps      (cm-0000 .. cm-0999)
#   - 100  Deployments     (each referencing 10 ConfigMaps via envFrom)
#
# Used by P1-T14's perf verification:
#
#   bash test/perf/stress-1k-configmaps.sh
#   curl -w "%{time_total}s\n" -o /dev/null -s \
#     localhost:8080/api/v1alpha1/graph?level=cluster
#   # Expect < 0.5s
#   curl -w "%{time_total}s\n" -o /dev/null -s \
#     "localhost:8080/api/v1alpha1/graph?level=namespace&namespace=petclinic-stress"
#   # Expect < 2s
#
# Then refresh the topology view in a browser and Chrome DevTools
# Performance tab; expect FPS >= 30 while dragging the canvas.
#
# Cleanup:
#   kubectl delete namespace petclinic-stress
set -euo pipefail

NS="${NS:-petclinic-stress}"
NUM_CM="${NUM_CM:-1000}"
NUM_DEP="${NUM_DEP:-100}"
CM_PER_DEP="${CM_PER_DEP:-10}"

command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 1; }

echo "→ Creating namespace ${NS}"
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f -

echo "→ Generating ${NUM_CM} ConfigMaps + ${NUM_DEP} Deployments"
TMP="$(mktemp)"
trap 'rm -f "${TMP}"' EXIT

{
  for i in $(seq 0 $((NUM_CM - 1))); do
    name="$(printf 'cm-%04d' "$i")"
    cat <<YAML
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

  for i in $(seq 0 $((NUM_DEP - 1))); do
    name="$(printf 'dep-%04d' "$i")"
    cat <<YAML
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  namespace: ${NS}
  labels:
    app: ${name}
spec:
  replicas: 0
  selector:
    matchLabels:
      app: ${name}
  template:
    metadata:
      labels:
        app: ${name}
    spec:
      containers:
        - name: c
          image: busybox:1.36
          command: ["sleep", "infinity"]
          envFrom:
YAML
    for j in $(seq 0 $((CM_PER_DEP - 1))); do
      cm_name="$(printf 'cm-%04d' "$(( (i * CM_PER_DEP + j) % NUM_CM ))")"
      cat <<YAML
            - configMapRef:
                name: ${cm_name}
YAML
    done
  done
} > "${TMP}"

echo "→ Applying ${TMP} ($(wc -l < "${TMP}") lines)"
kubectl apply -f "${TMP}"

echo "✓ Stress namespace ready: ${NUM_CM} ConfigMaps + ${NUM_DEP} Deployments (replicas=0 to keep node pressure low)"
echo "  cleanup: kubectl delete namespace ${NS}"
