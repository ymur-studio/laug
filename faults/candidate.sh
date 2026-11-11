#!/usr/bin/env bash
# Positive control: one candidate of faults/candidates.yaml, a dynamic config change a release could ship, judged by
# a plain verdict on the leased cell. laug candidate leases the cell and has this script release it.
#   1. refuse unless laugd shows the cell leased, the lease covers the run, the cell's laug.yaml holds the
#      candidate's from value, and every server role mounts this cell's copy (faults/lib.sh, dynconfig --check); and
#      unless the server tag the lease names, if any, is the one the cell's temporal-history runs (faults/lib.sh,
#      server_tag_read, as faults/run.sh checks it). That tag goes to both loads' run JSON and to the result JSON
#   2. before: CANDIDATE_BEFORE_S (90 s) of load at RATE on the unchanged cell; the last 60 s must pass every threshold
#      of the cell's release (faults/lib.sh, thresholds_resolve: load/steady-state.yaml plus
#      load/releases/<major>.<minor>.yaml when one exists, or THRESHOLDS as given), as faults/run.sh's before window
#      must, or the outcome is INVALID ("never steady") and nothing is edited. An unchanged 1.31.0 cell BLOCKed 1 run
#      in 6 on the lab host, slow for all 5 minutes (lab131-1, cells/results/2026-10-14-lab-host-131.txt); a candidate
#      judged on such a cell shows nothing
#   3. set key to value in the cell's own laug.yaml (faults/lib.sh, dynconfig: this host's copy for a compose cell,
#      the guest's over ssh for a microVM cell, each after checking every role mounts this cell's copy)
#   4. wait two re-read periods, 2 x CANDIDATE_REREAD_S (63 s): each role polls the file every 60 s of its own clock,
#      60.0 s of wall time on the lab host (cells/results/2026-10-23-boot1.txt, P6), and a rate limiter reads its
#      limit again on its first call 60 s or more after its last read (server 1.31.0
#      common/quotas/dynamic_rate_limiter_impl.go:9 and 130-139)
#   5. every server role must have logged "dynamic config changed for the key: <key>" since the edit; else the edit
#      did not land and nothing is judged. The server lowercases the key (server 1.31.0
#      common/dynamicconfig/client_diff.go:83-93, key.go:15-21), so the match ignores case. Seen on the lab host for
#      frontend.rps (cells/results/2026-10-23-boot1.txt, P6), and there for two keys with capitals,
#      matching.numTaskqueueReadPartitions and history.persistenceMaxQPS: logged by 5 of 5 roles in 7 of 7 runs, on
#      1.31.0 and 1.32.1 (.landed in faults/results/candidate-read-partitions-lab3-*.json and
#      candidate-persistence-starved-lab3-*.json; cells/results/2026-10-23-boot3.txt, C1 to C4)
#   6. DURATION (5m) of load at RATE (20/s), on past failed iterations so the window stays whole. Omes must run all
#      of DURATION, or the load ended early, a harness failure until shown otherwise. The window cannot say so alone:
#      it holds go run and the worker's start too, about 22 s on the base runs (load/run-omes.sh), so a window of
#      DURATION can hold an Omes that stopped that much early. Omes ran all of it when it exited 0, when it printed its
#      end-of-run tally ("run completed with N of M iterations failed", seen on the dev host, only
#      once DURATION is over), or when the window passed its --timeout, DURATION + 120 s
#      (omes f0de206, read in source)
#   7. load/verdict.sh over the load's window: the thresholds, the floor, Omes's exit and the sweep. verdict.sh resolves
#      the thresholds itself, from the tag the load's run JSON records, the same way
#   8. restore the cell's laug.yaml (the EXIT trap restores too, on any failure or signal once the edit has started)
# Outcome: PASS when the verdict is the candidate's expect and, for a BLOCK candidate, a row of its blocks_on is among
# the rows that decided it (exit 0); FAIL when the verdict is not the expect (exit 1); INVALID when there is nothing
# to compare (exit 2): never steady, the edit failed or did not land, the load ended early, verdict.sh gave no
# verdict, or a BLOCK that no blocks_on row decided, which an unchanged cell can give as well.
# Soft assertions are counted per role over the before window, the wait and the load, as faults/run.sh counts its
# windows: recorded, not judged. So are laugd's busy cells (faults/lib.sh, pool_quiet). laug candidate's lease makes
# laugd build a replacement cell on this host when the pool keeps warm cells, so just before the before load the script
# waits until laugd's /status (POOL_ADDR) shows no other cell busy, POOL_QUIET_WAIT_S (300) s at most. The result
# records the wait (pool_quiet_wait_s) and the most busy cells one poll saw in each window (pool_busy: before, wait,
# load; polled at each boundary and every 30 s of the load), and the summary warns when a window saw one. A cell still
# busy at the bound, or laugd not answering (null), does not stop the run; the leased cell no longer leased after the
# wait does (exit 2, no load).
# Writes $RESULTS_DIR/candidate-<name>-<RUN_ID>.json once the before load has run: the candidate, expect, blocks_on,
# the server tag, the thresholds of the before window (thresholds_release, thresholds_note, overrides_applied) and of
# the verdict (verdict_thresholds: release, note, overrides_applied, as load/verdict.sh resolved them from the load's
# own tag), the before window (with not_produced: readings with no data that the release file does not judge; failing
# and not_produced are null when the window was not read), the verdict, the rows that decided it, its readings and
# sweep, the load, the pool, and the outcome. A refusal before the before load exits 2 and writes nothing.
# CANDIDATE_RELEASE=1 (laug candidate): release the lease, by the cell id this script read from the lease file at
# start, on every exit, Ctrl-C and SIGTERM included, and remove the lease file only while it still names that cell.
# Usage: faults/candidate.sh <candidate> [lease-file]
set -euo pipefail

NAME="${1:?candidate (${LAUG:-bin/laug} candidate <name>)}"; LEASE_FILE="${2:-.last-lease.json}"
CANDIDATES="${CANDIDATES:-faults/candidates.yaml}"
# THRESHOLDS, when set, is the threshold set as given; unset, the cell's release picks it (thresholds_resolve, below).
RESULTS_DIR="${RESULTS_DIR:-faults/results}"
RUN_ID="${RUN_ID:-$(date -u +%Y%m%d-%H%M%S)}"
SCENARIO="${SCENARIO:-workflow_with_single_noop_activity}"
RATE="${RATE:-20}"; DURATION="${DURATION:-5m}"; MAX_CONCURRENT="${MAX_CONCURRENT:-50}"
CANDIDATE_REREAD_S="${CANDIDATE_REREAD_S:-63}"; CANDIDATE_BEFORE_S="${CANDIDATE_BEFORE_S:-90}"
CANDIDATE_RELEASE="${CANDIDATE_RELEASE:-0}"
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/.." && pwd)"
# shellcheck source=load/lost-work.sh
. "$root/load/lost-work.sh"
# shellcheck source=faults/lib.sh
. "$here/lib.sh"

# The release is set up before anything can refuse, so every exit after the lease gives the cell back. It names the
# cell read here, not whatever the lease file holds by then: another laug lease in this checkout rewrites the file.
POOL_ADDR="${POOL_ADDR:-http://127.0.0.1:7700}"; release_id=""
if [[ "$CANDIDATE_RELEASE" == 1 ]]; then
  release_id="$(jq -r '.id // empty' "$LEASE_FILE" 2>/dev/null)" || release_id=""
  [[ "$release_id" =~ ^[0-9a-f]{8}$ ]] || { echo "CANDIDATE_RELEASE=1 and $LEASE_FILE names no cell ('$release_id'); nothing to release" >&2; exit 2; }
fi
release_lease() {
  [[ -n "$release_id" ]] || return 0
  local id="$release_id"; release_id=""
  if curl -s --fail-with-body -X POST "$POOL_ADDR/release" -H 'content-type: application/json' -d "{\"id\":\"$id\"}" >/dev/null; then
    echo "laug · released cell $id" >&2
    [[ "$(jq -r '.id // empty' "$LEASE_FILE" 2>/dev/null)" != "$id" ]] || rm -f "$LEASE_FILE"
  else
    echo "laug · could not release cell $id at $POOL_ADDR; it lives until its lease ends, and $LEASE_FILE stays" >&2
  fi
}
# A signal ends the script through exit, so the EXIT trap (restore, then release) runs for it too.
trap release_lease EXIT; trap 'exit 130' INT; trap 'exit 143' TERM

[[ "$NAME" =~ ^[a-z0-9-]+$ ]] || { echo "candidate name '$NAME' is not lower-case letters, digits and dashes" >&2; exit 2; }
[[ "$RUN_ID" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "RUN_ID '$RUN_ID' has characters a task queue name does not take" >&2; exit 2; }
[[ "$CANDIDATE_REREAD_S" =~ ^[0-9]+$ ]] || { echo "CANDIDATE_REREAD_S must be whole seconds, not '$CANDIDATE_REREAD_S'" >&2; exit 2; }
[[ "$CANDIDATE_BEFORE_S" =~ ^[0-9]+$ ]] && (( CANDIDATE_BEFORE_S >= 60 )) \
  || { echo "CANDIDATE_BEFORE_S must be whole seconds, 60 or more (the window read is its last 60 s), not '$CANDIDATE_BEFORE_S'" >&2; exit 2; }
[[ "${POOL_QUIET_WAIT_S:-300}" =~ ^(0|[1-9][0-9]*)$ ]] || { echo "POOL_QUIET_WAIT_S must be whole seconds, not '$POOL_QUIET_WAIT_S'" >&2; exit 2; }
DURATION_S="$(lw_dur_s "$DURATION" 2>/dev/null)" || DURATION_S=""
[[ "$DURATION_S" =~ ^[0-9]+$ ]] && (( DURATION_S > 0 )) || { echo "DURATION '$DURATION' is not a duration like 5m or 300s" >&2; exit 2; }
WAIT_S=$(( 2 * CANDIDATE_REREAD_S )); BEFORE_S="$CANDIDATE_BEFORE_S"

n="$(yq "[.candidates[] | select(.name == \"$NAME\")] | length" "$CANDIDATES")"
[[ "$n" == 1 ]] || { echo "candidate $NAME is in $CANDIDATES $n times; one of: $(yq -r '[.candidates[].name] | join(", ")' "$CANDIDATES")" >&2; exit 2; }
entry="$(yq -o=json -I=0 ".candidates[] | select(.name == \"$NAME\")" "$CANDIDATES")"
# tostring, not // "": // falls through on false as on null, so a bool false read as empty.
field() { jq -r --arg k "$1" '.[$k] | if . == null then "" else tostring end' <<<"$entry"; }
KEY="$(field key)"; VALUE="$(field value)"; FROM="$(field from)"; EXPECT="$(field expect)"
# Both go into a yq expression and a shell command line in the guest, so only a dotted name and a number or a bool.
[[ "$KEY" =~ ^[A-Za-z][A-Za-z0-9.]*$ ]] || { echo "candidate $NAME: key '$KEY' is not a dynamic config key" >&2; exit 2; }
for v in "$VALUE" "$FROM"; do
  [[ "$v" =~ ^(-?[0-9]+(\.[0-9]+)?|true|false)$ ]] || { echo "candidate $NAME: '$v' is not a number or a bool" >&2; exit 2; }
done
[[ "$VALUE" != "$FROM" ]] || { echo "candidate $NAME: value and from are both $VALUE; the server logs no change" >&2; exit 2; }
[[ "$EXPECT" == ADVANCE || "$EXPECT" == BLOCK ]] || { echo "candidate $NAME: expect is '$EXPECT', not ADVANCE or BLOCK" >&2; exit 2; }
# blocks_on: a BLOCK candidate's own rows. Each must be a row load/verdict.sh can BLOCK on and none a row an unchanged
# slow cell trips (slow_cell_rows), or a BLOCK on it would show nothing about the change.
BLOCKS_ON="$(jq -r 'if .blocks_on == null then "" elif (.blocks_on | type) == "array" then .blocks_on | join(" ") else "!" end' <<<"$entry")"
# The names are the same in every release's set: an override changes how a reading is judged, not which readings exist.
# A threshold with no max is recorded and decides nothing, so it is no row to BLOCK on.
rows="$("$root/load/thresholds.sh" "" | yq -r '.thresholds[] | select(.max != null) | .name')"$'\n'"workflows_completed"$'\n'"omes_exit"$'\n'"workflows_not_completed"
slow_rows="$(yq -r '(.slow_cell_rows // [])[]' "$CANDIDATES")"
[[ "$BLOCKS_ON" != "!" ]] || { echo "candidate $NAME: blocks_on is not a list" >&2; exit 2; }
if [[ "$EXPECT" == BLOCK ]]; then
  [[ -n "$BLOCKS_ON" ]] || { echo "candidate $NAME expects BLOCK and names no blocks_on row; a BLOCK on any row would PASS" >&2; exit 2; }
  for r in $BLOCKS_ON; do
    grep -qxF -- "$r" <<<"$rows" || { echo "candidate $NAME: blocks_on row '$r' is not a row load/verdict.sh judges" >&2; exit 2; }
    ! grep -qxF -- "$r" <<<"$slow_rows" || { echo "candidate $NAME: blocks_on row '$r' is in slow_cell_rows; an unchanged cell trips it" >&2; exit 2; }
  done
elif [[ -n "$BLOCKS_ON" ]]; then echo "candidate $NAME expects ADVANCE; blocks_on is for a BLOCK candidate" >&2; exit 2; fi
key_lc="$(tr '[:upper:]' '[:lower:]' <<<"$KEY")"

lease_read "$LEASE_FILE"
HOST_VIRTUALIZED="$(host_virtualized)"
assert_leased
server_tag_read "$LEASE_FILE" || exit 2
# The lease must outlast the wait for laugd's busy cells (POOL_QUIET_WAIT_S at most), the before window (its load,
# Omes's cooldown and timeout, BEFORE_S + 120 s, and the port wait), the edit's wait, the load (DURATION + 120 s,
# load/run-omes.sh) and the verdict; laugd reaps an expired lease and the cell with it.
exp="$(jq -r '.expires_at // empty' "$LEASE_FILE")"
[[ -z "$exp" ]] || exp="$(date -d "$exp" +%s 2>/dev/null)" || exp=""
need=$(( $(date +%s) + POOL_QUIET_WAIT_S + BEFORE_S + 120 + 40 + WAIT_S + DURATION_S + 120 + 60 ))
if [[ -n "$exp" ]] && (( exp < need )); then
  echo "lease on cell $CELL expires $(date -u -d "@$exp" +%H:%M:%SZ), the candidate needs until $(date -u -d "@$need" +%H:%M:%SZ); lease again with TTL=$(( need - $(date +%s) + 60 ))" >&2
  exit 2
fi
cur="$(dynconfig_get "$KEY")" || { echo "could not read $KEY from cell $CELL's laug.yaml" >&2; exit 2; }
[[ "$cur" == "$FROM" ]] || { echo "cell $CELL's laug.yaml holds $KEY $cur, not $FROM; candidate $NAME is a change from $FROM" >&2; exit 2; }
# dynconfig runs in a subshell with errexit on: inside an if or a || list bash would switch errexit off for the whole
# function, and a failed cp or yq would go on to move a half-written file over the cell's laug.yaml.
set +e; ( set -e; dynconfig --check ); rc=$?; set -e
(( rc == 0 )) || { echo "cell $CELL's dynamic config cannot be edited (exit $rc); nothing was run" >&2; exit 2; }

mkdir -p "$RESULTS_DIR"
LOAD_RUN_ID="candidate-$NAME-$RUN_ID"; BEFORE_RUN_ID="$LOAD_RUN_ID-before"; LOAD_DIR="$RESULTS_DIR/load"
RESULT="$RESULTS_DIR/candidate-$NAME-$RUN_ID.json"
tmp="$(mktemp -d)"; sa_dir="$tmp/sa"; pool_dir="$tmp/pool"; mkdir "$sa_dir" "$pool_dir"; sa_pids=()
edited=0; restored=0; load_pid=""
cleanup() {
  [[ -z "$load_pid" ]] || kill -- "-$load_pid" 2>/dev/null || kill "$load_pid" 2>/dev/null || true
  if (( edited && ! restored )); then
    set +e; ( set -e; dynconfig restore ); (( $? == 0 )) || echo "laug · could not restore cell $CELL's laug.yaml; release destroys it" >&2
  fi
  local p; for p in "${sa_pids[@]+"${sa_pids[@]}"}"; do kill "$p" 2>/dev/null || true; done; rm -rf "$tmp"
  release_lease
}
trap cleanup EXIT
# The thresholds for the cell's release, read before any load.
thresholds_resolve "$SERVER_TAG" "$tmp/thresholds.yaml" || { echo "no threshold set for server ${SERVER_TAG:-unread}; nothing was run" >&2; exit 2; }

# landed <since> <until>: per role, the lines that say the key changed; null for a role whose logs could not be read.
landed() {
  local out="{}" svc cid n
  for svc in $SERVER_ROLES; do
    if ! cid="$(container "$svc")" \
       || ! n="$(cell_logs "$cid" "$1" "$2" | grep -ciF -- "dynamic config changed for the key: $key_lc "; exit "${PIPESTATUS[0]}")"; then
      n=null
    fi
    out="$(jq -c --arg r "$svc" --argjson n "$n" '.[$r] = $n' <<<"$out")"
  done
  echo "$out"
}
# load_start <run-id> <duration>: load/run-omes.sh in its own process group, in the background (load_pid), back once
# run-omes.sh has recorded the window's start and opened Omes's log.
load_start() {
  rm -f "$LOAD_DIR/$1.log" "$LOAD_DIR/$1.json"
  CELL_ADDRESS="$CELL_ADDRESS" PROM_URL="$PROM_URL" PROJECT="$PROJECT" DURATION="$2" RATE="$RATE" MAX_CONCURRENT="$MAX_CONCURRENT" \
    RUN_ID="$1" CONTINUE_ON_FAILURE=1 RESULTS_DIR="$LOAD_DIR" LEASE_FILE="$LEASE_FILE" SERVER_TAG="$SERVER_TAG" \
    setsid "$root/load/run-omes.sh" "$SCENARIO" >/dev/null 2>"$RESULTS_DIR/$1.load.log" &
  load_pid=$!
  local i; for i in $(seq 1 300); do { [[ -e "$LOAD_DIR/$1.log" ]] || ! kill -0 "$load_pid" 2>/dev/null; } && break; sleep 0.1; done
}
# load_summary <run-id> <run-omes.sh exit>: the run JSON's fields and its window in seconds, or null without one.
load_summary() {
  [[ -s "$LOAD_DIR/$1.json" ]] || { echo null; return 0; }
  jq -c --argjson rc "${2:-null}" '{run_id, task_queue, scenario, start, end, duration, rate, max_concurrent, omes_exit, iterations_failed,
    omes_tally, window_s: ((.end | fromdateiso8601) - (.start | fromdateiso8601)), exit: $rc}' "$LOAD_DIR/$1.json"
}

# 2. The before window.
invalid=""; edit_at=""; landed_by=null; landed_at=""; load_rc=""; t_load=""; t_end=""; vrc=""
log "thresholds: $TH_NOTE"
log "candidate $NAME on cell $CELL, server ${SERVER_TAG:-unread}: $KEY $FROM -> $VALUE, expect $EXPECT${BLOCKS_ON:+ on $BLOCKS_ON}; ${BEFORE_S} s before window, then the edit, a ${WAIT_S} s wait and $DURATION of load at ${RATE}/s"
wait_omes_ports
# No cell being built or destroyed on this host as the before load starts (faults/lib.sh, pool_quiet): its last poll is
# the before window's first. The wait can last POOL_QUIET_WAIT_S, and laugd may reset or reap the leased cell meanwhile,
# so the lease is checked again before the load starts: a refusal there exits 2 and writes nothing.
pool_quiet before
assert_leased || { log "pool: cell $CELL is not leased after the wait for laugd's busy cells; no load was started"; exit 2; }
t_before=$(date +%s); sa_open before "$t_before"
load_start "$BEFORE_RUN_ID" "${BEFORE_S}s"
sa_sleep $(( t_before + BEFORE_S - $(date +%s) ))
t_before_end=$(date +%s); pool_poll before
before_readings="$(MIN_WINDOW_S=60 PROM_URL="$PROM_URL" THRESHOLDS="$TH_FILE" "$root/load/read-thresholds.sh" $(( t_before_end - 60 )) "$t_before_end" 2>/dev/null)" \
  || before_readings=null
# Judged on the release's set, as faults/run.sh's before window is (faults/lib.sh, thresholds_judge): failing lists each
# threshold over its max, or with no data where the set judges no data; not_produced, the readings with no data that a
# release file does not judge. Both stay null when the window was not read or not judged (the outcome says which): not
# read is not "had data", and the reason is not a list of readings.
before_np=null
if [[ "$before_readings" == null ]]; then before_failing="the thresholds could not be read"
elif ! before_j="$(thresholds_judge "$before_readings")"; then before_failing="the readings could not be judged"
else before_failing="$(jq -r '.failing | join(" ")' <<<"$before_j")"; before_np="$(jq -c '.not_produced' <<<"$before_j")"; fi
before_rc=0; wait "$load_pid" || before_rc=$?; load_pid=""
before_json="$(jq -nc --argjson s "$(( t_before_end - 60 ))" --argjson e "$t_before_end" --argjson r "$before_readings" --arg f "$before_failing" \
  --argjson np "$before_np" --argjson load "$(load_summary "$BEFORE_RUN_ID" "$before_rc")" \
  '{start: ($s | todate), end: ($e | todate), readings: $r, failing: (if $np == null then null elif $f == "" then [] else ($f | split(" ")) end), not_produced: $np, load: $load}')"
if [[ -n "$before_failing" ]]; then
  invalid="never steady before the edit: $before_failing"; log "before: $invalid"
else log "before: every threshold under its max over the last 60 s"; fi
[[ "$before_np" == "[]" || "$before_np" == null ]] || log "before: no data, not produced on this release and not judged: $(jq -r 'join(", ")' <<<"$before_np") (${TH_RELEASE:-the threshold set})"

# 3 to 7. The edit, the wait, the load and the verdict.
if [[ -z "$invalid" ]] && ! assert_leased; then invalid="cell $CELL is no longer leased; nothing was edited"; fi
if [[ -n "$invalid" ]]; then sa_close "$(date +%s)"
else
  wait_omes_ports
  edit_at=$(date +%s); pool_poll before wait; sa_close "$edit_at"; sa_open wait "$edit_at"
  # edited before the edit: a signal or a dropped ssh session can come after the file moved and before the edit
  # returns. Restoring an edit that did not happen copies the same template, or the guest's .orig, again.
  edited=1
  set +e; ( set -e; dynconfig "$KEY" "$VALUE" ); rc=$?; set -e
  if (( rc )); then
    invalid="the edit failed (exit $rc); the file may have changed, so it was restored"; sa_close "$(date +%s)"
  else
    log "set $KEY to $VALUE; waiting ${WAIT_S} s, 2 re-read periods of ${CANDIDATE_REREAD_S} s"
    sa_sleep "$WAIT_S"
    landed_at=$(date +%s); pool_poll wait; landed_by="$(landed "$edit_at" "$landed_at")"
    missing="$(jq -r '[to_entries[] | select(.value == null or .value == 0) | .key] | join(" ")' <<<"$landed_by")"
    log "landed: $landed_by"
    if [[ -n "$missing" ]]; then
      invalid="the edit did not land: no \"dynamic config changed for the key: $key_lc\" line from $missing in ${WAIT_S} s"
      sa_close "$landed_at"
    elif ! assert_leased; then
      invalid="cell $CELL is no longer leased; nothing was loaded"
      sa_close "$landed_at"
    else
      wait_omes_ports
      t_load=$(date +%s); pool_poll load; sa_close "$t_load"; sa_open load "$t_load"
      load_start "$LOAD_RUN_ID" "$DURATION"
      left="$DURATION_S"
      while (( left > 0 )) && kill -0 "$load_pid" 2>/dev/null; do
        s=$(( left < SA_SLICE_S ? left : SA_SLICE_S )); sleep "$s"; left=$(( left - s )); sa_tick; pool_poll load
      done
      load_rc=0; wait "$load_pid" || load_rc=$?; load_pid=""
      t_end=$(date +%s); pool_poll load; sa_close "$t_end"
      log "load ended, run-omes.sh exit $load_rc"
      vrc=0
      env VERDICT_JSON="$tmp/verdict.json" RESULTS_DIR="$LOAD_DIR" FALLBACK_PROM_URL="$PROM_URL" PROM_URL="" \
        "$root/load/verdict.sh" "$LOAD_RUN_ID" || vrc=$?
    fi
  fi
fi

# 8. Restore.
restored_at=""; restore_error=""
if (( edited )); then
  set +e; ( set -e; dynconfig restore ); rc=$?; set -e
  if (( rc == 0 )); then restored=1; restored_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; log "restored $KEY"
  else restore_error="dynconfig restore exited $rc"; log "restore failed: $restore_error; release destroys the cell"; fi
fi

for p in "${sa_pids[@]+"${sa_pids[@]}"}"; do wait "$p" || true; done; sa_pids=()
softasserts="{}"
for w in before wait load; do softasserts="$(jq -c --arg w "$w" --argjson v "$(sa_window "$w")" '.[$w] = $v' <<<"$softasserts")"; done
pool_busy_json="$(pool_json before wait load)"

load_json=null; [[ -z "$t_load" ]] || load_json="$(load_summary "$LOAD_RUN_ID" "$load_rc")"
verdict_json=null; [[ -s "$tmp/verdict.json" ]] && verdict_json="$(jq -c . "$tmp/verdict.json")"
# load/verdict.sh resolves its own threshold set, from the load's run JSON: the version the cell's frontend reported,
# else server_tag. This script resolved the before window's from SERVER_TAG (the history image, else the lease). Every
# cell laug builds gives one tag both ways; when the two sets differ anyway, the result keeps both (verdict_thresholds)
# and the summary warns.
th_warn=""
if [[ "$verdict_json" != null ]] && [[ "$(jq -r '.thresholds_release // ""' <<<"$verdict_json")" != "$TH_RELEASE" ]]; then
  th_warn="the before window was judged with ${TH_RELEASE:-no release file} (server ${SERVER_TAG:-not read}), the load's verdict with $(jq -r '.thresholds_release // "no release file"' <<<"$verdict_json"): $(jq -r '.thresholds_note // ""' <<<"$verdict_json")"
  log "warning: $th_warn"
fi
verdict="$(jq -r '.verdict // ""' <<<"$verdict_json")"
decided="$(jq -r '(.deciding // []) | join(" ")' <<<"$verdict_json")"
own="$(jq -r --arg b "$BLOCKS_ON" '($b | split(" ")) as $b | [(.deciding // [])[] | select(. as $d | any($b[]; . == $d))] | join(" ")' <<<"$verdict_json")"
if [[ -z "$invalid" ]]; then
  if [[ "$load_json" == null ]]; then invalid="the load wrote no run JSON (run-omes.sh exit $load_rc; its stderr is in $RESULTS_DIR/$LOAD_RUN_ID.load.log)"
  elif ! jq -e --argjson d "$DURATION_S" '.window_s >= $d and (.omes_exit == 0 or .omes_tally != null or .window_s >= $d + 120)' <<<"$load_json" >/dev/null; then
    invalid="$(jq -r --argjson d "$DURATION_S" '"the load ended early: "
      + (if .window_s < $d then "a \(.window_s) s window for \($d) s of load (Omes exit \(.omes_exit))"
         else "Omes exited \(.omes_exit) with no end-of-run tally after \(.window_s) s, before its timeout at \($d + 120) s" end)
      + ": a harness failure until shown otherwise"' <<<"$load_json")"
  elif [[ -z "$verdict" ]]; then invalid="load/verdict.sh gave no verdict (exit $vrc)"
  elif [[ "$verdict" == BLOCK && "$EXPECT" == BLOCK && -z "$own" ]]; then
    invalid="BLOCKed by ${decided// /, }, none of them a blocks_on row ($BLOCKS_ON): an unchanged cell can BLOCK that way (lab131-1), so it shows nothing about the change"
  fi
fi
if [[ -n "$invalid" ]]; then outcome=INVALID; outcome_reason="$invalid"
elif [[ "$verdict" == "$EXPECT" ]]; then
  outcome=PASS; outcome_reason="verdict $verdict, as expected"
  [[ -z "$own" ]] || outcome_reason+="; blocks_on rows among the deciding: ${own// /, }"
else outcome=FAIL; outcome_reason="verdict $verdict, expected $EXPECT"; fi

edit_iso=""; [[ -z "$edit_at" ]] || edit_iso="$(date -u -d "@$edit_at" +%Y-%m-%dT%H:%M:%SZ)"
jq -n --arg candidate "$NAME" --argjson entry "$entry" --arg run "$RUN_ID" --arg cell "$CELL" --arg address "$CELL_ADDRESS" --arg tag "$SERVER_TAG" \
      --arg backend "$BACKEND" --arg fcv "$FC_VERSION_LEASE" --arg cache "$CACHE_TYPE" --argjson virt "$HOST_VIRTUALIZED" --argjson before "$before_json" \
      --arg edit_at "$edit_iso" --argjson reread "$CANDIDATE_REREAD_S" --argjson wait "$WAIT_S" \
      --argjson landed "$landed_by" --argjson load "$load_json" --argjson v "$verdict_json" --argjson softasserts "$softasserts" \
      --arg restored_at "$restored_at" --arg restore_error "$restore_error" --arg outcome "$outcome" --arg why "$outcome_reason" \
      --arg th_release "$TH_RELEASE" --arg th_note "$TH_NOTE" --argjson th_overrides "$TH_OVERRIDES" \
      --argjson pool_wait "$pool_quiet_wait_s" --argjson pool_bound "$POOL_QUIET_WAIT_S" --argjson pool_busy "$pool_busy_json" \
      '{candidate: $candidate, key: $entry.key, from: $entry.from, value: $entry.value, expect: $entry.expect, blocks_on: ($entry.blocks_on // null),
        verdict: ($v.verdict // null), deciding: ($v.deciding // null), outcome: $outcome, outcome_reason: $why,
        reason: $entry.reason, prediction: ($entry.prediction // null),
        run_id: $run, cell: $cell, address: $address, server_tag: (if $tag == "" then null else $tag end),
        backend: $backend, firecracker_version: (if $fcv == "" then null else $fcv end), cache_type: (if $cache == "" then null else $cache end), host_virtualized: $virt,
        thresholds_release: (if $th_release == "" then null else $th_release end), thresholds_note: $th_note, overrides_applied: $th_overrides,
        verdict_thresholds: (if $v == null then null else {release: $v.thresholds_release, note: $v.thresholds_note, overrides_applied: $v.overrides_applied} end),
        before: $before,
        edit_at: (if $edit_at == "" then null else $edit_at end), reread_s: $reread, wait_s: $wait, landed: $landed,
        load: $load, readings: ($v.readings // null), rows: ($v.rows // null), sweep: ($v.sweep // null), sweep_cutoff: ($v.sweep_cutoff // null),
        verdict_exit: ($v.exit // null), softasserts: $softasserts,
        pool_quiet_wait_s: $pool_wait, pool_quiet_bound_s: $pool_bound, pool_busy: $pool_busy,
        restored_at: (if $restored_at == "" then null else $restored_at end), restore_error: (if $restore_error == "" then null else $restore_error end)}' > "$RESULT"

echo
echo "laug · candidate $NAME on cell $CELL, run $RUN_ID"
echo "server      ${SERVER_TAG:-not read}"
echo "thresholds  $TH_NOTE"
[[ -z "$th_warn" ]] || echo "warning     $th_warn"
if [[ -n "$before_failing" ]]; then echo "before      not steady over the last 60 s of ${BEFORE_S} s: $before_failing"
else echo "before      every threshold under its max over the last 60 s of ${BEFORE_S} s"; fi
# Every override of the release file, with what it did in the before window; the verdict's rows say the same for the load.
# thresholds_judge leaves out a threshold with no max, so such an override is in neither list: it says so, with or
# without data.
jq -rn --argjson o "$TH_OVERRIDES" --argjson np "$before_np" --argjson read "$([[ "$before_readings" == null ]] && echo false || echo true)" \
  --argjson r "$before_readings" \
  --argjson nomax "$(yq -o=json -I=0 '[.thresholds[] | select(.max == null) | .name]' "$TH_FILE" 2>/dev/null || echo '[]')" \
  --arg f "$TH_RELEASE" '$o[] | .name as $n
  | "override    \($n): " + (if $np == null then (if $read then "not judged in the before window" else "not read in the before window" end)
      elif any($np[]; . == $n) then "no data in the before window, not produced on this release, not judged"
      elif any($nomax[]; . == $n) then (if $r[$n] == null then "no data" else "had data" end) + " in the before window; no max in the threshold set, so not judged"
      else "had data in the before window, so judged against its max" end)
    + " (\($f): \(.reason))"'
[[ "$landed_by" == null ]] || echo "edit        $KEY $FROM -> $VALUE; logged by $(jq '[.[] | select(. != null and . > 0)] | length' <<<"$landed_by") of $(wc -w <<<"$SERVER_ROLES") roles within ${WAIT_S} s"
[[ "$load_json" == null ]] || echo "load        $(jq -r '"\(.window_s) s window, \(.duration) at \(.rate)/s; Omes exit \(.omes_exit), \(.iterations_failed) failed iteration(s)"' <<<"$load_json")"
[[ -z "$verdict" ]] || echo "verdict     $verdict, expect $EXPECT${decided:+; BLOCKed by ${decided// /, }}${BLOCKS_ON:+ (blocks_on: ${BLOCKS_ON// /, })}"
jq -r '[to_entries[] | select(.value != null) | "\(.key) \([.value | to_entries[] | select(.key != "first") | .value // empty] | add // 0)"]
  | if length > 0 then "softassert  \(join(", ")), by window (recorded, not judged)" else empty end' <<<"$softasserts"
pool_summary before wait load
[[ -z "$restore_error" ]] || echo "restore     $restore_error"
echo "result      $RESULT"
echo "$outcome: $outcome_reason"
case "$outcome" in PASS) exit 0 ;; FAIL) exit 1 ;; *) exit 2 ;; esac
