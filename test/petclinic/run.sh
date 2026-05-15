#!/usr/bin/env bash
# test/petclinic/run.sh - PetClinic incremental deployer.
#
# Usage:
#   test/petclinic/run.sh base           # apply base.yaml + wait for pods
#   test/petclinic/run.sh phase1         # base + multi-namespace + 1000-node stress
#   test/petclinic/run.sh phase3-netpol  # base + F-109 NetworkPolicy fixture
#   test/petclinic/run.sh status         # show resources in the namespace
#   test/petclinic/run.sh clean          # delete every petclinic-* namespace
set -euo pipefail

PHASE="${1:-base}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${DIR}/../.." && pwd)"

deploy_base() {
  echo "→ Deploying PetClinic base (16 resource kinds, 8 edge types)"
  echo "  Prerequisite: Envoy Gateway + an Ingress controller (Traefik recommended)."
  kubectl apply -f "${DIR}/base.yaml"
  echo "→ Waiting for managed pods to become Ready (Job 'migrate' runs sleep infinity)"
  kubectl wait --for=condition=ready pod --all -n petclinic --timeout=300s
}

deploy_phase1() {
  deploy_base
  echo "→ Applying Phase 1 incremental fixture (multi-namespace + extra workloads)"
  kubectl apply -f "${DIR}/phase1-incremental.yaml"
  kubectl wait --for=condition=ready pod --all -n petclinic --timeout=300s
  kubectl wait --for=condition=ready pod --all -n petclinic-staging --timeout=300s
  echo "→ Applying 1000-resource stress fixture (petclinic-stress namespace)"
  bash "${REPO_ROOT}/test/perf/stress-1k-configmaps.sh"
  echo
  echo "Suggested perf checks:"
  echo "  curl -w '%{time_total}s\\n' -o /dev/null -s localhost:8080/api/v1alpha1/graph?level=cluster"
  echo "  curl -w '%{time_total}s\\n' -o /dev/null -s 'localhost:8080/api/v1alpha1/graph?level=namespace&namespace=petclinic-stress'"
}

deploy_phase3_netpol() {
  deploy_base
  echo "→ Applying Phase 3 NetworkPolicy fixture (F-109 / P3-T1)"
  kubectl apply -f "${DIR}/phase3-networkpolicy.yaml"
  echo
  echo "Suggested F-109 checks:"
  echo "  curl -s localhost:8080/api/v1/networkpolicy/petclinic/default-deny/selected | jq"
  echo "  curl -s localhost:8080/api/v1/networkpolicy/petclinic/api-allow/allow-graph | jq"
}

clean() {
  kubectl delete namespace petclinic petclinic-staging petclinic-stress \
    petclinic-monitoring --ignore-not-found
}

status() {
  kubectl get all,gateway,httproute,ingress,configmap,secret,pvc,sa -n petclinic
}

case "${PHASE}" in
  base)          deploy_base ;;
  phase1)        deploy_phase1 ;;
  phase3-netpol) deploy_phase3_netpol ;;
  clean)         clean ;;
  status)        status ;;
  *) echo "Usage: $0 [base|phase1|phase3-netpol|status|clean]" >&2; exit 1 ;;
esac

echo "✓ '${PHASE}' done"
