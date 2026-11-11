#!/usr/bin/env bash
# The sweep: every workflow on one task queue started before a cutoff, counted by execution status in the cell's
# visibility store. A threshold reads rates from Prometheus; the sweep reads what the server holds for each workflow,
# so a workflow still Running, or one that Failed, TimedOut or was Terminated, is lost work even when every rate
# looked clean. load/verdict.sh and faults/run.sh call it with a cutoff SWEEP_MARGIN_S (30) s before the end of the
# window they judge, so a workflow started then has had that long to finish. 30 s left nothing Running for
# workflow_with_single_noop_activity in the two rauns below; a scenario whose workflows run longer needs more.
#
# Usage: load/sweep.sh <cell-address> <task-queue> <cutoff-rfc3339>
# Output: one JSON object {status: count}, e.g. {"Completed":5893,"Running":2}; {} when nothing matched.
# Exit 2 on bad arguments, else the temporal CLI's exit when the count fails.
#
# The query, checked in server 1.31.0 source (the cells' version):
#   - TaskQueue is a system search attribute, a keyword (common/searchattribute/sadefs/constants.go:22, 134).
#   - SQL visibility accepts GROUP BY on one field, and only ExecutionStatus or a TemporalLowCardinalityKeyword*
#     attribute. The Postgres store counts through the legacy converter unless
#     system.visibilityEnableUnifiedQueryConverter is on (default false, common/dynamicconfig/constants.go:95-97;
#     laug.yaml leaves it), and both converters apply the rule (store/sql/query_converter_legacy.go:262-278,
#     store/query/converter.go:79-86 and 272-289, under common/persistence/visibility/).
#   - Group values are the status names Running, Completed, Failed, Canceled, Terminated, ContinuedAsNew, TimedOut
#     (common/persistence/sql/sqlplugin/visibility.go:193-199, through the API enum's String()).
# Run against `temporal server start-dev` (CLI 1.9.1, server 1.32.0, SQLite visibility, not a cell):
# the query was accepted, `-o json` printed {"count":"3","groups":[{"groupValues":["Running"],"count":"2"},...]},
# and printed {} when nothing matched. Run on a pool cell (server 1.31.0, Postgres visibility, the dev
# host) inside two rauns of faults/run.sh at 20/s: the query was accepted and read {"Completed":3023} and
# {"Completed":3043}.
#
# Visibility is written after the workflow, so a workflow that just closed can still read Running. When the count
# shows Running, it is read again SWEEP_RETRIES times (2), SWEEP_RETRY_S (5) seconds apart; a terminal status never
# changes, so only Running can fall. The last reading is the output.
set -euo pipefail

ADDRESS="${1:-}"; TASK_QUEUE="${2:-}"; CUTOFF="${3:-}"
NAMESPACE="${NAMESPACE:-default}"
SWEEP_RETRIES="${SWEEP_RETRIES:-2}"; SWEEP_RETRY_S="${SWEEP_RETRY_S:-5}"
[[ -n "$ADDRESS" && -n "$TASK_QUEUE" && -n "$CUTOFF" ]] || { echo "usage: load/sweep.sh <cell-address> <task-queue> <cutoff-rfc3339>" >&2; exit 2; }
# Both values go inside single quotes in the query; refuse anything that could close them.
[[ "$TASK_QUEUE" =~ ^[A-Za-z0-9._:-]+$ ]] || { echo "task queue '$TASK_QUEUE' has characters the query does not take" >&2; exit 2; }
[[ "$CUTOFF" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$ ]] \
  || { echo "cutoff '$CUTOFF' is not RFC 3339 (2026-10-14T02:25:41Z)" >&2; exit 2; }

query="TaskQueue='$TASK_QUEUE' AND StartTime < '$CUTOFF' GROUP BY ExecutionStatus"
count() {
  temporal workflow count --address "$ADDRESS" --namespace "$NAMESPACE" -o json --query "$query" \
    | jq -c '[.groups[]? | {key: (.groupValues[0] | tostring), value: (.count | tonumber)}] | from_entries'
}

out="$(count)"
for ((i = 0; i < SWEEP_RETRIES; i++)); do
  [[ "$(jq '.Running // 0' <<<"$out")" -gt 0 ]] || break
  echo "sweep: $(jq -r '.Running' <<<"$out") Running on $TASK_QUEUE before $CUTOFF; reading again in ${SWEEP_RETRY_S} s" >&2
  sleep "$SWEEP_RETRY_S"
  out="$(count)"
done
echo "$out"
