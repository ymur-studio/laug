#!/usr/bin/env bash
# The one window reader. Evaluates every threshold query in THRESHOLDS over [start, end] by turning the query's [1m]
# range into the window length, so a quantile is over the whole window and not the last minute.
# baseline.sh, verdict.sh, faults/run.sh and faults/candidate.sh all call this; nothing else reads thresholds from
# Prometheus. verdict.sh, run.sh and candidate.sh pass the set load/thresholds.sh resolved for the run's server release;
# baseline.sh passes none, so it reads load/steady-state.yaml. Every set has the base's names and queries (a release
# file changes how a reading is judged, not what is read), so the readings are the same whichever set is passed.
#
# Usage: read-thresholds.sh <start-epoch> <end-epoch> [prom-url]
# Output: one JSON object {name: value|null, ...} plus workflows_completed, counter_resets and window_seconds.
# Exit 3 when the window is shorter than the set's `window:` (the minimum a quantile is trusted over).
set -euo pipefail

start="${1:?start epoch}"; end="${2:?end epoch}"
PROM_URL="${3:-${PROM_URL:-http://localhost:9090}}"
THRESHOLDS="${THRESHOLDS:-load/steady-state.yaml}"

window=$(( end - start ))
# MIN_WINDOW_S lets the fault harness read 60 s before/after windows; the floor is 6 scrapes at 5 s.
min_window="${MIN_WINDOW_S:-$(yq -r '.window' "$THRESHOLDS" | sed -E 's/([0-9]+)m/\1*60+/; s/([0-9]+)s/\1+/; s/\+$//' | bc)}"
(( min_window >= 30 )) || min_window=30
if (( window < min_window )); then
  echo "window is ${window} s; steady-state.yaml trusts nothing shorter than ${min_window} s" >&2
  exit 3
fi

q() { curl -sf -G "$PROM_URL/api/v1/query" --data-urlencode "query=$1" --data-urlencode "time=$2" | jq -r '.data.result[0].value[1] // "null"'; }

out="{}"
count="$(yq '.thresholds | length' "$THRESHOLDS")"
for ((i=0; i<count; i++)); do
  name="$(yq -r ".thresholds[$i].name" "$THRESHOLDS")"
  query="$(yq -r ".thresholds[$i].query" "$THRESHOLDS" | sed "s/\[1m\]/[${window}s]/g")"
  v="$(q "$query" "$end")"
  [[ "$v" == NaN || "$v" == nan ]] && v=null
  out="$(jq -c --arg k "$name" --argjson v "$v" '.[$k]=$v' <<<"$out")"
done

# Throughput from the counter at both edges, not rate(), which extrapolates short windows. A history process that
# restarts (kill-history, oom-history) starts its counter at 0, and an edge difference across that reset undercounts
# or goes negative. When resets() sees one in the window, increase() counts instead: it adds back what the counter
# held before each reset, and extrapolates to the window's edges, which the edge difference avoids (Prometheus
# query functions).
resets="$(q "sum(resets(workflow_success[${window}s]))" "$end")"
[[ "$resets" == null ]] && resets=0
if awk -v r="$resets" 'BEGIN { exit !(r+0 > 0) }'; then
  n="$(q "sum(increase(workflow_success[${window}s]))" "$end")"
  [[ "$n" == null || "$n" == NaN ]] && n=0
  done_wf="$(awk -v n="$n" 'BEGIN { printf "%.0f", n }')"
else
  c0="$(q 'sum(workflow_success)' "$start")"; c1="$(q 'sum(workflow_success)' "$end")"
  [[ "$c0" == null ]] && c0=0; [[ "$c1" == null ]] && c1=0
  done_wf="$(awk -v a="$c0" -v b="$c1" 'BEGIN { printf "%d", b-a }')"
fi
jq -c --argjson n "$done_wf" --argjson r "$(awk -v r="$resets" 'BEGIN { printf "%d", r }')" --argjson w "$window" \
  '.workflows_completed=$n | .counter_resets=$r | .window_seconds=$w' <<<"$out"
