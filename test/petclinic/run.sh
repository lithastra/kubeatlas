#!/usr/bin/env bash
# test/petclinic/run.sh - PetClinic incremental deployer (Phase 0 subset).
#
# Usage:
#   test/petclinic/run.sh base     # apply base.yaml + wait for pods
#   test/petclinic/run.sh status   # show resources in the namespace
#   test/petclinic/run.sh clean    # delete the namespace
set -euo pipefail

PHASE="${1:-base}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

deploy_base() {
  echo "→ Deploying PetClinic base (16 resource kinds, 8 edge types)"
  echo "  Prerequisite: Envoy Gateway + an Ingress controller (Traefik recommended)."
  kubectl apply -f "${DIR}/base.yaml"
  echo "→ Waiting for managed pods to become Ready (Job 'migrate' runs sleep infinity)"
  kubectl wait --for=condition=ready pod --all -n petclinic --timeout=300s
}

clean() {
  kubectl delete namespace petclinic --ignore-not-found
}

status() {
  kubectl get all,gateway,httproute,ingress,configmap,secret,pvc,sa -n petclinic
}

case "${PHASE}" in
  base)   deploy_base ;;
  clean)  clean ;;
  status) status ;;
  *) echo "Usage: $0 [base|clean|status]" >&2; exit 1 ;;
esac

echo "✓ '${PHASE}' done"
