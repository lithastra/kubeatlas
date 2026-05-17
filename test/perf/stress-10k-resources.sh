#!/usr/bin/env bash
# test/perf/stress-10k-resources.sh - 10000-resource stress fixture.
#
# P3-T15 doubles the Phase 2 stress-test-5k fixture to ~10K resources
# for the v1.2 performance baseline. It is a thin wrapper over
# stress-5k-resources.sh: that generator is already fully
# parameterised (NS / NUM_CM / NUM_DEP / NUM_SVC), so the 10K fixture
# is the same generator with the counts doubled, written into its
# own namespace.
#
# Populates the stress-test-10k namespace with:
#   - 10000 ConfigMaps   (cm-00000 .. cm-09999)
#   - 2000  Deployments  (each referencing 5 ConfigMaps via envFrom)
#   - 400   Services
#
# Usage:
#   bash test/perf/stress-10k-resources.sh
#   NS=stress-test-10k \
#   KUBEATLAS_BASELINE_OUT=test/verify/perf-baseline-v1.2.json \
#   KUBEATLAS_BASELINE_PHASE=3-v1.2.0 \
#     bash test/perf/bench-v1.sh
#
# Cleanup:
#   kubectl delete namespace stress-test-10k
#
# Every count stays overridable; the values below are just the 10K
# defaults. Deployments are replicas:0 — the fixture is API objects
# only, no scheduled Pods.

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

NS="${NS:-stress-test-10k}" \
NUM_CM="${NUM_CM:-10000}" \
NUM_DEP="${NUM_DEP:-2000}" \
NUM_SVC="${NUM_SVC:-400}" \
	exec bash "${DIR}/stress-5k-resources.sh"
