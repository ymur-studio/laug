#!/usr/bin/env bash
# load/testdata/gate/mk-tolerance.sh <dir> [jump|steady]: gate runs for make harness-test's gate tolerance cases,
# each holding only what laug gate tolerance reads. No run it writes is committed.
# <dir>: 18 runs, g20261020-100000 to -117000: three A/A gates with known d, fourteen that gate tolerance refuses, one
# rule each, and one it counts with a reading not produced (g20261020-115000). jump and steady: 20 A/A gates each,
# g20261021-100000 to -100019, whose workflow task d is 0.1 with 1.5 on the last (jump), or 0.1 and 0.2 in turn (steady).
set -euo pipefail
out="${1:?usage: mk-tolerance.sh <dir> [jump|steady]}"; set="${2:-}"
mkdir -p "$out"
WAVE='["load:incumbent workflow_with_single_noop_activity 20/s 50 5m","load workflow_with_single_noop_activity 20/s 50 5m","fault:persistence-latency workflow_with_single_noop_activity"]'
BASE='{"workflow_task_schedule_to_start_p99_ms":17.681,"activity_schedule_to_start_p99_ms":9.98,"workflow_end_to_end_p95_ms":49.2,"persistence_latency_p99_ms":8.69,"service_error_rate":0,"frontend_request_p99_ms":10.1,"workflow_task_schedule_to_start_over_20ms":0.004,"activity_schedule_to_start_over_10ms":0.008,"persistence_latency_over_10ms":0.002,"frontend_request_over_10ms":0.009,"workflow_end_to_end_over_50ms":0.04,"workflows_completed":5900,"counter_resets":0,"window_seconds":302}'
# gate <id> <driver> <d JSON> [verdict.json edit] [plan.json edit] [steady verdict JSON edit] [incumbent verdict JSON
# edit]: one A/A arm gate whose steady reads the incumbent's readings plus d, each edit a jq program (. by default)
gate() {
  local id="$1" drv="$2" d="$3" edit="${4:-.}" pedit="${5:-.}" sedit="${6:-.}" iedit="${7:-.}" g
  g="$out/$id"; mkdir -p "$g/incumbent" "$g/steady"
  jq -n --arg d "$drv" '{driver: $d}' > "$g/gate.json"
  jq -n '{incumbent: "1.31.0", items: [{key: "incumbent", kind: "load", arm: "incumbent", pair: "steady"}, {key: "steady", kind: "load"}, {key: "pl", kind: "fault"}]}' | jq "$pedit" > "$g/plan.json"
  jq -n --argjson w "$WAVE" '{verdict: "ADVANCE", server_tag: "1.31.0", incumbent: "1.31.0", laug_commit: "abc1234", host: {type: "m8i.8xlarge", vcpu: 32, mem_gib: 123},
    host_quiet: true, host_noise: [], stopped_at: null, rules: ({R1: "harness", R2: "coverage", R3: "contract", R4: "outcome", R5: "live control", R6: "identity", R7: "incumbent"}
    | map_values({name: ., pass: true, reasons: []})), comparison: {mode: "recording", wave: $w}}' | jq "$edit" > "$g/verdict.json"
  jq -n --argjson r "$BASE" --arg id "$id" '{run_id: "\($id)-incumbent", verdict: "ADVANCE", window_s: 302, readings: $r}' | jq "$iedit" > "$g/incumbent/$id-incumbent.verdict.json"
  jq -n --argjson r "$BASE" --argjson d "$d" --arg id "$id" '{run_id: "\($id)-steady", verdict: "ADVANCE", window_s: 302,
    readings: ($r | with_entries(.value += ($d[.key] // 0)))}' | jq "$sedit" > "$g/steady/$id-steady.verdict.json"
}
case "$set" in
  "")
    gate g20261020-100000 run.sh '{"workflow_task_schedule_to_start_p99_ms": 0.2, "persistence_latency_p99_ms": -0.05, "activity_schedule_to_start_over_10ms": 0.002, "frontend_request_over_10ms": 0.001}'
    gate g20261020-101000 step.sh '{"workflow_task_schedule_to_start_p99_ms": -0.1, "persistence_latency_p99_ms": 0.03, "activity_schedule_to_start_over_10ms": 0.004}'
    gate g20261020-102000 run.sh '{"workflow_task_schedule_to_start_p99_ms": 0.5, "persistence_latency_p99_ms": 0.01, "activity_schedule_to_start_over_10ms": -0.001, "frontend_request_over_10ms": 0.0002}'
    # each of the next twelve, and -116000 and -117000, is refused by one rule
    gate g20261020-103000 run.sh '{}' '.server_tag = "1.32.1"'
    gate g20261020-104000 run.sh '{}' '.host_quiet = false | .host_noise = ["the poller saw up to 1 busy cell(s) at laugd, in 1 of 77 polls"]'
    gate g20261020-105000 run.sh '{}' '.comparison.wave |= .[:2]'
    gate g20261020-106000 run.sh '{}' '.verdict = "BLOCK" | .rules.R4 = {name: "outcome", pass: false, reasons: ["steady: expected ADVANCE, observed BLOCK"]}'
    gate g20261020-107000 run.sh '{}' '.stopped_at = "wave" | .rules = null | .comparison = null'
    gate g20261020-108000 run.sh '{}' . '.items |= map(del(.arm, .pair))'
    gate g20261020-109000 run.sh '{}' '.comparison = null'
    gate g20261020-110000 run.sh '{}' '.host.type = "other"'
    gate g20261020-111000 run.sh '{}' '.server_tag = "1.32.1" | .incumbent = "1.32.1"'
    gate g20261020-112000 run.sh '{}' '.comparison.rows = [{name: "persistence_latency_p99_ms", result: "not compared"}]'
    gate g20261020-113000 run.sh '{}' . . '.readings.frontend_request_over_10ms = null'
    gate g20261020-114000 run.sh '{}'; echo '{"name": "known-slow", "type": "latency", "attributes": {"latency": 1}, "toxicity": 1}' > "$out/g20261020-114000/steady/known-slow.json"
    # counted: the frontend share null on both sides, and both sets mark it not_judged, so it is not produced
    gate g20261020-115000 run.sh '{}' . . '.readings.frontend_request_over_10ms = null' '.readings.frontend_request_over_10ms = null'
    for f in thresholds.json thresholds-incumbent.json; do
      jq -n '{thresholds: [{name: "frontend_request_over_10ms", no_data: "not_judged"}]}' > "$out/g20261020-115000/$f"
    done
    # two more refused: no verdict.json; no verdict JSON for steady
    gate g20261020-116000 run.sh '{}'; rm "$out/g20261020-116000/verdict.json"
    gate g20261020-117000 run.sh '{}'; rm "$out/g20261020-117000/steady/g20261020-117000-steady.verdict.json" ;;
  jump|steady)
    for i in $(seq 0 19); do
      d=0.1; [[ "$set" == steady ]] && (( i % 2 )) && d=0.2; [[ "$set" == jump && $i == 19 ]] && d=1.5
      gate "$(printf 'g20261021-1000%02d' "$i")" "$( (( i % 2 )) && echo step.sh || echo run.sh)" "{\"workflow_task_schedule_to_start_p99_ms\": $d}"
    done ;;
  *) echo "usage: mk-tolerance.sh <dir> [jump|steady]" >&2; exit 2 ;;
esac
