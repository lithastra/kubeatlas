#!/usr/bin/env bash
# test/verify/perf-regression.sh - P2-T23 regression gate.
#
# Reads a freshly captured perf JSON ($CURRENT, default
# /tmp/perf-current.json) and compares every numeric measurement
# against test/verify/perf-baseline-v1.0.json. A measurement that
# is more than (1 + REGRESSION_PCT/100) times the baseline number
# fails the script.
#
# REGRESSION_PCT defaults to 20 — the playbook bar.
#
# Placeholder baseline values (zeros) are skipped so the script
# stays useful while the first real run is pending.
#
# Usage:
#   KUBEATLAS_URL=... bash test/perf/bench-v1.sh           # writes baseline file
#   cp test/verify/perf-baseline-v1.0.json /tmp/perf-current.json
#   bash test/verify/perf-regression.sh
#
# Exit codes:
#   0 — every metric within budget.
#   1 — usage / missing file.
#   2 — at least one regression detected.

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASELINE="${BASELINE:-${DIR}/perf-baseline-v1.0.json}"
CURRENT="${CURRENT:-/tmp/perf-current.json}"
REGRESSION_PCT="${REGRESSION_PCT:-20}"

[[ -f "${BASELINE}" ]] || { echo "missing baseline: ${BASELINE}" >&2; exit 1; }
[[ -f "${CURRENT}"  ]] || { echo "missing current: ${CURRENT}"   >&2; exit 1; }
command -v jq >/dev/null || { echo "missing: jq" >&2; exit 1; }

regressed=0
for tier in tier1 tier2; do
  base_block=$(jq -c ".${tier}" "${BASELINE}")
  cur_block=$(jq -c  ".${tier}" "${CURRENT}")
  [[ "${base_block}" == "null" || "${cur_block}" == "null" ]] && continue

  # Only compare numeric fields. P3-T0c added per-tier captured_at /
  # captured_on metadata to each tier block; those are strings/objects,
  # not measurements, and would otherwise produce noisy ✓ lines.
  for metric in $(jq -r 'to_entries | map(select(.value | type == "number")) | .[].key' <<<"${base_block}"); do
    base=$(jq -r ".${metric}" <<<"${base_block}")
    cur=$(jq -r  ".${metric}" <<<"${cur_block}")
    # Skip zero-baseline placeholders + non-numeric values.
    [[ "${base}" == "0" || "${base}" == "null" || -z "${base}" ]] && continue
    [[ "${cur}"  == "null" || -z "${cur}"  ]] && continue

    ratio=$(awk -v b="${base}" -v c="${cur}" 'BEGIN{ if (b==0) { print 0 } else { printf "%.4f", c/b } }')
    over=$(awk -v r="${ratio}" -v p="${REGRESSION_PCT}" 'BEGIN{ print (r > 1 + p/100) ? 1 : 0 }')
    if [[ "${over}" == "1" ]]; then
      printf '  ✗ %s.%s: %s ms (baseline %s ms, +%.0f%%)\n' \
        "${tier}" "${metric}" "${cur}" "${base}" \
        "$(awk -v r="${ratio}" 'BEGIN{ printf "%.0f", (r - 1) * 100 }')"
      regressed=1
    else
      printf '  ✓ %s.%s: %s ms (baseline %s ms)\n' \
        "${tier}" "${metric}" "${cur}" "${base}"
    fi
  done
done

if (( regressed == 1 )); then
  echo
  echo "perf-regression: at least one metric regressed by > ${REGRESSION_PCT}%"
  exit 2
fi
echo
echo "perf-regression: OK (no metric regressed by > ${REGRESSION_PCT}%)"
