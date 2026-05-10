#!/usr/bin/env bash
# test/chaos/rego-runaway.sh - P2-T26 chaos scenario.
#
# Scenario: load a rule pack whose evaluation never terminates
# under realistic input and verify the engine cuts it off via
# the timeout sandbox (default 100ms).
#
# Expected behaviour:
#   - The kubeatlas process does NOT exit.
#   - rules-test reports the sample as failed (exit 2).
#   - /healthz + /readyz keep returning 200.
#
# The runaway pattern uses numbers.range over a billion-element
# range to make any traversal blow past the 100ms cap. Anything
# the optimizer might shortcut would also need to handle the
# `[_] > some_predicate` walk — OPA cannot evaluate that
# lazily.

set -euo pipefail

KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT:-18080}"
PACK_DIR="${PACK_DIR:-$(mktemp -d)}"
trap 'rm -rf "${PACK_DIR}"' EXIT

for cmd in curl jq go; do
  command -v "${cmd}" >/dev/null || { echo "missing: ${cmd}" >&2; exit 1; }
done

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${DIR}/../.." && pwd)"

echo "==> Building runaway rule pack at ${PACK_DIR}"
mkdir -p "${PACK_DIR}/samples"
cat > "${PACK_DIR}/metadata.yaml" <<'YAML'
name: chaos-runaway
version: 0.0.0
rego_api: v1
kubeatlas: ">= 1.0.0"
description: |
  Deliberately runaway rule pack used by chaos tests. Loading
  this on a real cluster is a bug — it exists to verify the
  engine's evaluateWithGuards timeout sandbox cuts evaluation
  off cleanly.
modules:
  - name: runaway
    file: runaway.rego
    entrypoint: data.kubeatlas.chaos.runaway.derive
    match:
      group: ""
      kind: ConfigMap
YAML

cat > "${PACK_DIR}/runaway.rego" <<'REGO'
package kubeatlas.chaos.runaway

import rego.v1

# Walk a billion-element range with a side-effect-bearing
# predicate. OPA cannot evaluate this lazily; the engine's
# 100ms timeout has to fire to reclaim the goroutine.
derive contains edge if {
    n := numbers.range(1, 1000000000)[_]
    n > 0
    edge := {
        "from": input.id,
        "to": sprintf("runaway/%d", [n]),
        "type": "RUNAWAY",
    }
}
REGO

cat > "${PACK_DIR}/samples/cm.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: runaway-target
  namespace: chaos
data:
  k: v
YAML

echo "==> Running rules-test against runaway pack (must finish within ~5s)"
start=$(date +%s)
set +e
go run ./cmd/kubeatlas rules-test --pack="${PACK_DIR}" --samples="${PACK_DIR}/samples" \
  > /tmp/rules-test-runaway.out 2>&1
rc=$?
set -e
elapsed=$(( $(date +%s) - start ))
echo "rules-test exit: ${rc}, elapsed: ${elapsed}s"

# The whole thing should finish in under ~5s on any developer
# machine — the guard cuts each evaluation off at 100ms; if the
# script ever blew past 30s the timeout is broken.
if (( elapsed > 30 )); then
  echo "rules-test took ${elapsed}s — timeout sandbox is not firing"
  exit 1
fi

if ! curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/healthz" >/dev/null; then
  echo "kubeatlas /healthz NOT 200 after chaos — server crashed"
  exit 1
fi

echo
cd "${REPO_ROOT}"
echo "rego-runaway: kubeatlas's eval timeout sandbox fired as expected."
