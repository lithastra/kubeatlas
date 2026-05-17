#!/usr/bin/env bash
# test/verify/perf-regression.sh - perf regression gate.
#
# For every baseline in $BASELINES, compares a freshly captured
# current run against it and fails if any numeric measurement, on
# any tier, regressed by more than $REGRESSION_PCT percent.
#
# P2-T23 built this against a single baseline (v1.0). P3-T15
# extended it to check the v1.0 (5K fixture) and v1.2 (10K fixture)
# baselines in one pass — a regression in either fails the gate.
#
# The current run for perf-baseline-<v>.json is read from
# $CURRENT_DIR/perf-current-<v>.json. A baseline with no matching
# current file is skipped, so the gate stays useful when only one
# fixture was re-benched. REGRESSION_PCT defaults to 20 — the
# playbook bar. Zero-valued baseline metrics are treated as
# unrecorded placeholders and skipped.
#
# Usage:
#   # capture a fresh 5K run as the v1.0 current file:
#   KUBEATLAS_URL=... NS=stress-test-5k \
#     KUBEATLAS_BASELINE_OUT=/tmp/perf-current-v1.0.json \
#     bash test/perf/bench-v1.sh
#   # likewise the 10K fixture -> /tmp/perf-current-v1.2.json
#   bash test/verify/perf-regression.sh
#
# Exit codes:
#   0 — every metric within budget.
#   1 — usage / missing baseline / nothing to check.
#   2 — at least one regression detected.

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CURRENT_DIR="${CURRENT_DIR:-/tmp}"
REGRESSION_PCT="${REGRESSION_PCT:-20}"
# Space-separated baseline file names (resolved relative to this
# directory). Override to check a different set.
BASELINES="${BASELINES:-perf-baseline-v1.0.json perf-baseline-v1.2.json}"

command -v jq >/dev/null || { echo "missing: jq" >&2; exit 1; }

# compare_pair BASELINE_FILE CURRENT_FILE
# Prints one ✓/✗ line per metric; returns 1 if anything regressed.
compare_pair() {
  local baseline="$1" current="$2" regressed=0
  local tier base_block cur_block metric base cur over
  for tier in tier1 tier2; do
    base_block=$(jq -c ".${tier}" "${baseline}")
    cur_block=$(jq -c ".${tier}" "${current}")
    [[ "${base_block}" == "null" || "${cur_block}" == "null" ]] && continue

    # Only compare numeric fields — per-tier captured_at / captured_on
    # metadata are strings/objects, not measurements.
    for metric in $(jq -r 'to_entries | map(select(.value | type == "number")) | .[].key' <<<"${base_block}"); do
      base=$(jq -r ".${metric}" <<<"${base_block}")
      cur=$(jq -r ".${metric}" <<<"${cur_block}")
      [[ "${base}" == "0" || "${base}" == "null" || -z "${base}" ]] && continue
      [[ "${cur}" == "null" || -z "${cur}" ]] && continue

      over=$(awk -v b="${base}" -v c="${cur}" -v p="${REGRESSION_PCT}" \
        'BEGIN{ print (b > 0 && c / b > 1 + p / 100) ? 1 : 0 }')
      if [[ "${over}" == "1" ]]; then
        printf '  ✗ %s.%s: %s ms (baseline %s ms)\n' "${tier}" "${metric}" "${cur}" "${base}"
        regressed=1
      else
        printf '  ✓ %s.%s: %s ms (baseline %s ms)\n' "${tier}" "${metric}" "${cur}" "${base}"
      fi
    done
  done
  return "${regressed}"
}

exit_code=0
checked=0
for name in ${BASELINES}; do
  baseline="${DIR}/${name}"
  [[ -f "${baseline}" ]] || { echo "missing baseline: ${baseline}" >&2; exit 1; }

  # perf-baseline-v1.0.json -> perf-current-v1.0.json
  version="${name#perf-baseline-}"
  current="${CURRENT_DIR}/perf-current-${version}"
  if [[ ! -f "${current}" ]]; then
    echo "==> ${name}: no current run at ${current} — skipped"
    continue
  fi

  echo "==> ${name} vs $(basename "${current}")"
  if ! compare_pair "${baseline}" "${current}"; then
    exit_code=2
  fi
  checked=$((checked + 1))
done

if (( checked == 0 )); then
  echo "perf-regression: no current runs found for any baseline — nothing checked" >&2
  exit 1
fi

echo
if (( exit_code == 2 )); then
  echo "perf-regression: at least one metric regressed by > ${REGRESSION_PCT}%"
else
  echo "perf-regression: OK (no metric regressed by > ${REGRESSION_PCT}%)"
fi
exit "${exit_code}"
