#!/usr/bin/env bash
# Fake gate item for a fake gate (GATE_FAKE=1): gate/run.sh runs it in place of faults/run.sh, or of load/run-omes.sh
# and load/verdict.sh, with the same environment. Usage: fake-item.sh <kind> <key> <scenario> <fault|-> <lease-file>.
# It records what it was given in $RESULTS_DIR/fake-item.json (its arguments, RUN_ID, RESULTS_DIR, LEASE_FILE, both
# Omes metrics addresses, POOL_ADDR, POOL_QUIET_WAIT_S, its pid, process group and session, whether it started with
# SIGINT or SIGQUIT ignored, and when it started), appends its pid to $FAKE_GATE_DIR/started.<key>, one line per
# start (a test counts the starts: an adopted wave starts no item again), waits FAKE_GATE_ITEM_S (0.2) s, and
# writes what the real scripts write, from the gate fixtures in load/testdata/gate/base with its own run id and the
# lease's cell, address and server tag, and thresholds_release the one load/thresholds.sh resolves for that tag: a
# fault item's raun JSON at $RESULTS_DIR/$RUN_ID.json (pl's or kh's for those keys, else pl's); a load item's run JSON
# there and its verdict at VERDICT_JSON (steady's, whose start and end every load item shares, so two load items read
# windows of one length that start together). Exit 0, the fixtures' ADVANCE. Keys named in these, space-separated:
#   FAKE_GATE_DNR    a fault item that did not recover: BLOCK, exit 1
#   FAKE_GATE_EXIT2  exit 2 and no JSON, as faults/run.sh does when its load generator exits in its first 10 s
#   FAKE_GATE_HANG   runs until TERM, then exits 143; a fault item first writes the partial raun JSON faults/run.sh's
#                    EXIT trap writes, and a load item writes nothing, as its run-omes.sh dies before its JSON
#   FAKE_GATE_STUCK  ignores TERM; only KILL stops it
#   FAKE_GATE_SCALE  a load item whose verdict JSON reads FAKE_GATE_SCALE_BY (a number, 1 by default) times steady's
#                    latency readings and shares (every reading named _ms or _over_<n>ms), its error rate, completions
#                    and rows as they are: still ADVANCE, since a fake runs no load/verdict.sh to judge the maxima. The
#                    incumbent arm's R7 then reads the release slower than the incumbent beside it.
set -euo pipefail
kind="${1:?kind}"; key="${2:?key}"; fault="${4:-}"; lease="${5:?lease file}"
here="$(cd "$(dirname "$0")" && pwd)"; gfx="$here/base"; gid="$(jq -r .gate_id "$gfx/gate.json")"
out="$RESULTS_DIR/$RUN_ID"
named() { [[ " ${1:-} " == *" $key "* ]]; }   # named "<keys>": this item is one of them
by="${FAKE_GATE_SCALE_BY:-1}"
[[ "$by" =~ ^[0-9]+([.][0-9]+)?$ ]] || { echo "fake item: FAKE_GATE_SCALE_BY must be a number" >&2; exit 2; }
scale=false; ! named "${FAKE_GATE_SCALE:-}" || scale=true
read -r pgid sid < <(ps -o pgid=,sid= -p $$)
# awk reads its own status: bash ignores SIGQUIT for itself, and starts its children as it was started (seen)
ign="$(awk '$1 == "SigIgn:" { print $2 }' /proc/self/status)"
jq -n --arg kind "$kind" --arg key "$key" --arg scenario "${3:-}" --arg fault "$fault" --arg lease "$lease" --arg run "$RUN_ID" \
  --arg rd "$RESULTS_DIR" --arg lf "${LEASE_FILE:-}" --arg w "${WORKER_METRICS_ADDR:-}" --arg c "${CLIENT_METRICS_ADDR:-}" \
  --arg pool "${POOL_ADDR:-}" --arg quiet "${POOL_QUIET_WAIT_S-unset}" --argjson pid $$ --argjson pgid "$pgid" --argjson sid "$sid" \
  --argjson int_ign "$(( (0x$ign & 2) != 0 ))" --argjson quit_ign "$(( (0x$ign & 4) != 0 ))" --argjson start "$(date +%s.%N)" \
  '{kind: $kind, key: $key, scenario: $scenario, fault: $fault, lease_arg: $lease, run_id: $run, results_dir: $rd, lease_file: $lf,
    worker_metrics_addr: $w, client_metrics_addr: $c, pool_addr: $pool, pool_quiet_wait_s: $quiet, pid: $pid, pgid: $pgid, sid: $sid,
    sigint_ignored: ($int_ign == 1), sigquit_ignored: ($quit_ign == 1), start: $start}' > "$RESULTS_DIR/fake-item.json"
echo "$$" >> "$FAKE_GATE_DIR/started.$key"
cell="$(jq -r .id "$lease")"; address="$(jq -r .address "$lease")"; tag="$(jq -r .server_tag "$lease")"
# The release file its cell's tag is judged with, as the real scripts record it (load/thresholds.sh, resolved), so a
# gate whose items run two tags passes R6's release-file check per item.
rel="$("$here/../../thresholds.sh" "$tag" 2>/dev/null | yq -o=json -I=0 '.resolved.release_file')" || rel=null

src="$gfx/pl/$gid-pl.json"; [[ ! -f "$gfx/$key/$gid-$key.json" ]] || src="$gfx/$key/$gid-$key.json"
raun() { jq --arg run "$RUN_ID" --arg cell "$cell" --arg addr "$address" --arg tag "$tag" --argjson rel "$rel" \
  '.run_id = $run | .cell = $cell | .address = $addr | .server_tag = $tag | .thresholds_release = $rel | .task_queue = "omes-\($run)" | '"$1" "$src" > "$out.json"; }
# A hung load item writes nothing on TERM: its load/run-omes.sh dies before its run JSON, and load/verdict.sh never runs.
if named "${FAKE_GATE_HANG:-}"; then
  trap '[[ "$kind" != fault ]] || raun "{run_id, fault, cell, address, server_tag, scenario, partial: true, exit: 143, inject_at,
    landed_at, remove_at, mechanisms: [], result: \"BLOCK\",
    reason: \"stopped before the result (exit 143); the fault was removed on the way out; the log says why\"}"; exit 143' TERM
  while :; do sleep 0.1; done
fi
if named "${FAKE_GATE_STUCK:-}"; then trap '' TERM; while :; do sleep 0.1; done; fi
sleep "${FAKE_GATE_ITEM_S:-0.2}"
if named "${FAKE_GATE_EXIT2:-}"; then echo "load generator exited: fake" >&2; exit 2; fi
if [[ "$kind" == fault ]]; then
  if named "${FAKE_GATE_DNR:-}"; then
    raun '.result = "BLOCK" | .reason = "did not recover within 60s: signal 9876.5" | .recovered_at = "" | .time_to_recover_s = ""
      | .after_start = "" | .after_end = "" | .after = {} | .sweep = null'
    echo BLOCK; exit 1
  fi
  raun '.'; echo ADVANCE; exit 0
fi
jq --arg run "$RUN_ID" --arg addr "$address" --arg tag "$tag" --arg prom "$(jq -r .prometheus "$lease")" --arg project "$(jq -r .project "$lease")" \
  '.run_id = $run | .cell = $addr | .prometheus = $prom | .project = $project | .server_tag = $tag | .server_version = $tag | .task_queue = "omes-\($run)"' \
  "$gfx/steady/$gid-steady.json" > "$out.json"
jq --arg run "$RUN_ID" --argjson rel "$rel" --argjson by "$by" --argjson scale "$scale" '.run_id = $run | .thresholds_release = $rel
  | if $scale then .readings |= with_entries(if (.key | test("_ms$|_over_[0-9]+ms$")) and (.value | type) == "number" then .value *= $by else . end) else . end' \
  "$gfx/steady/$gid-steady.verdict.json" > "${VERDICT_JSON:?}"
echo 0 > "$RESULTS_DIR/load.exit"
echo "VERDICT: ADVANCE"
