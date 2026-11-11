#!/usr/bin/env bash
# Verdict: evaluate every threshold of the run's server release over the run's whole window (via read-thresholds.sh),
# then the lost work (load/lost-work.sh): Omes's exit, a completion floor, and a sweep of the run's task queue.
# Prints ADVANCE or BLOCK with the numbers; exit 0 on ADVANCE, 1 on BLOCK, 2 with no run metadata or a threshold set
# that does not resolve, 3 when the run is shorter than the window the thresholds trust and no lost-work rule BLOCKs.
# A metric that reads null (no data) EXCEEDS: a verdict with nothing behind it is not an ADVANCE. There are two
# exceptions, and neither row blocks. A threshold the release's file marks no_data: not_judged reads "not produced on
# this release" when it has no data, with a max or without; with data it is judged against its max, if it has one. A
# threshold with no max is read and recorded and judged by nothing: its row reads "no threshold yet" when it has data,
# or none and no such mark.
#
# The thresholds are the set load/thresholds.sh resolves from the run JSON's server tag (the cell's server_version when
# recorded, else server_tag): load/steady-state.yaml, the base, plus load/releases/<major>.<minor>.yaml when one exists.
# THRESHOLDS, when set, wins, and no release file applies; load/thresholds.sh refuses one that relaxes a rule itself
# (no_data, override: or resolved:) or has a max that is not a number, as it refuses such a base: exit 2. The verdict
# prints which files applied and each override with its reason, and a row an override decided names the file
# (before release files, a 1.32.1 run BLOCKed on activity schedule-to-start, which 1.32.1 does not produce: no data
# in 3 of 3 runs on the lab host, cells/results/2026-10-23-boot1.txt, D; the BLOCK is inference: no data EXCEEDS).
#
# Lost work BLOCKs when Omes exited nonzero, when completions fall under 0.95 x RATE x DURATION, or when the sweep
# finds a workflow started more than SWEEP_MARGIN_S (30) s before the run's end in a status other than
# SWEEP_EXPECTED (Completed), or finds none. Runs recorded before 2026-10-14 have no omes_exit and no task_queue: those rules print
# "not recorded" and do not BLOCK, so an old run keeps its verdict. The floor reads rate and duration, which every
# run JSON has; a run without a rate cap has no floor.
#
# VERDICT_JSON names a file to write the same verdict to as JSON, for a caller that keeps it (faults/candidate.sh):
# {verdict, exit, window_s, readings, rows, deciding, sweep, sweep_cutoff, thresholds_release, thresholds_note,
# overrides_applied}. rows holds every threshold and lost-work row as printed; deciding names the rows that BLOCKed
# (EXCEEDS or BELOW).
# thresholds_release is the release file applied (null for none); overrides_applied lists its overrides, each with
# not_produced, true when the reading was read and had no data. Written on exit 0, 1 and 3 only; an exit 2 or a reading
# that failed writes nothing. Set or unset, VERDICT_JSON does not change what the script prints.
set -euo pipefail

RUN_ID="${1:?run id}"
RESULTS_DIR="${RESULTS_DIR:-load/results}"
SWEEP_MARGIN_S="${SWEEP_MARGIN_S:-30}"
[[ "$SWEEP_MARGIN_S" =~ ^[0-9]+$ ]] || { echo "SWEEP_MARGIN_S must be whole seconds, not '$SWEEP_MARGIN_S'" >&2; exit 2; }
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=load/lost-work.sh
. "$here/lost-work.sh"
meta="$RESULTS_DIR/$RUN_ID.json"
[[ -f "$meta" ]] || { echo "no run metadata at $meta" >&2; exit 2; }
# The run's own Prometheus, recorded by run-omes.sh, so this reads the cell the load hit and not whatever
# .last-lease.json names now. PROM_URL overrides; runs recorded before 2026-10-13 have no field.
PROM_URL="${PROM_URL:-$(jq -r '.prometheus // empty' "$meta")}"
PROM_URL="${PROM_URL:-${FALLBACK_PROM_URL:-http://127.0.0.1:9090}}"
echo "reading Prometheus at $PROM_URL" >&2

start_epoch="$(date -u -d "$(jq -r .start "$meta")" +%s)"
end_epoch="$(date -u -d "$(jq -r .end "$meta")" +%s)"
# The release the run's cell ran (run-omes.sh records it; runs recorded before 2026-10-14 have none), and the one the
# thresholds came from. server_version is what the cell's own frontend reported; server_tag is what laug was told
# (SERVER_TAG, the lease, or the local cell's history image). When both are there and differ, the cell's word is the
# release, and it picks the release file too.
run_tag="$(jq -r '.server_tag // empty' "$meta")"; run_version="$(jq -r '.server_version // empty' "$meta")"
th_file="$(mktemp)"; trap 'rm -f "$th_file"' EXIT
"$here/thresholds.sh" "${run_version:-$run_tag}" > "$th_file" \
  || { echo "no threshold set for server ${run_version:-${run_tag:-(no tag)}}; nothing judged" >&2; exit 2; }
th="$(yq -o=json -I=0 '.' "$th_file")"
# A run whose Omes died early has a window shorter than steady-state.yaml trusts, and read-thresholds.sh exits 3 on
# it. Its thresholds are then read over the window as it is (30 s at least) and shown, not judged, and its lost work
# is judged as for any run: a BLOCK there is a BLOCK. Only a short run that loses nothing exits 3, with
# no verdict, and the verdict JSON's short_window names the window it fell short of.
short=""
rc=0; readings="$(PROM_URL="$PROM_URL" THRESHOLDS="$th_file" "$here/read-thresholds.sh" "$start_epoch" "$end_epoch")" || rc=$?
if (( rc == 3 )); then
  short="$(jq -r '.window' <<<"$th")"
  rc=0; readings="$(MIN_WINDOW_S=30 PROM_URL="$PROM_URL" THRESHOLDS="$th_file" "$here/read-thresholds.sh" "$start_epoch" "$end_epoch" 2>/dev/null)" || rc=$?
  case "$rc" in 0) ;; 3) readings="{}" ;; *) exit "$rc" ;; esac
elif (( rc != 0 )); then
  exit "$rc"
fi

block=0
VERDICT_JSON="${VERDICT_JSON:-}"; rows_json=""; sweep_json=null; cutoff=""
heading() { printf "%-41s %12s %12s  %s\n" "$@"; }   # 41: workflow_task_schedule_to_start_over_20ms
row() { # row <metric> <value> <limit> <result>: print one row, and keep it for VERDICT_JSON
  heading "$@"
  [[ -n "$VERDICT_JSON" ]] || return 0
  rows_json+="$(jq -cn --arg m "$1" --arg v "$2" --arg l "$3" --arg r "$4" \
    '{metric: $m, value: (if $v == "-" or $v == "null" or $v == "" then null else ($v | tonumber? // $v) end), limit: $l, result: $r,
      blocks: ($r | test("^(EXCEEDS|BELOW)"))}')"$'\n'
}
finish() { # finish <ADVANCE|BLOCK|""> <exit>: write VERDICT_JSON when it is set, then exit
  if [[ -n "$VERDICT_JSON" ]]; then
    printf '%s' "$rows_json" | jq -s --arg run "$RUN_ID" --arg v "$1" --argjson exit "$2" --argjson window "$(( end_epoch - start_epoch ))" \
      --argjson readings "$readings" --argjson sweep "$sweep_json" --arg cutoff "$cutoff" --arg short "$short" \
      --argjson th "$(jq -c .resolved <<<"$th")" \
      '{run_id: $run, verdict: (if $v == "" then null else $v end), exit: $exit, window_s: $window,
        short_window: (if $short == "" then null else $short end), readings: $readings, rows: .,
        deciding: [.[] | select(.blocks) | .metric], sweep: $sweep, sweep_cutoff: (if $cutoff == "" then null else $cutoff end),
        thresholds_release: $th.release_file, thresholds_note: $th.note,
        overrides_applied: [$th.overrides[] | . + {not_produced: (.name as $n | ($readings | has($n)) and $readings[$n] == null)}]}' > "$VERDICT_JSON"
  fi
  exit "$2"
}
printf "laug · verdict for run %s (%d s window)\n" "$RUN_ID" "$(( end_epoch - start_epoch ))"
base_tag="$(jq -r '.resolved.base_server_tag // ""' <<<"$th")"; base_name="$(jq -r '.resolved.base' <<<"$th")"
if [[ -z "$run_tag$run_version" ]]; then
  echo "server tag absent from the run JSON: every cell before 2026-10-14 ran 1.31.0"
else
  echo "server ${run_tag:-tag absent}, the cell said ${run_version:-nothing}"
  if [[ -n "$run_tag" && -n "$run_version" && "$run_tag" != "$run_version" ]]; then
    echo "note: server_tag $run_tag and the version the cell reported, $run_version, disagree; the run is taken as $run_version"
  fi
  shown_tag="${run_version:-$run_tag}"
  if [[ -n "$base_tag" && "$shown_tag" != "$base_tag" ]]; then
    echo "note: the thresholds come from $base_tag baselines ($base_name server_tag); this run is on $shown_tag"
  fi
fi
echo "thresholds: $(jq -r '.resolved.note' <<<"$th")"
jq -r '.resolved.overrides[] | "  override \(.name), no_data: \(.no_data): \(.reason) (evidence: \(.evidence))"' <<<"$th"
echo
heading metric value max result
# One line per threshold: name, max, what no data reads, and the file that said so (the release file, or the set itself).
while IFS=$'\t' read -r name max no_data src; do
  value="$(jq -r --arg k "$name" '.[$k] // "null"' <<<"$readings")"
  if [[ -n "$short" ]]; then
    result="not judged (window under $short)"
  elif [[ "$value" == null && "$no_data" == not_judged ]]; then
    result="not produced on this release ($src)"
  elif [[ "$max" == "null" || -z "$max" ]]; then
    result="no threshold yet"
  elif [[ "$value" == null ]]; then
    result="EXCEEDS (no data)"; block=1
  elif awk -v v="$value" -v m="$max" 'BEGIN { exit !(v+0 > m+0) }'; then
    result="EXCEEDS"; block=1
  else
    result="ok"
  fi
  # A row with no max, each a share past a bucket edge today, prints to 5 places and the rest to 3, so a share of
  # 0.0004 does not print and record as 0. readings keeps every value whole either way.
  fmt='%.3f'; [[ "$max" == "null" || -z "$max" ]] && fmt='%.5f'
  shown="$value"; [[ "$value" != null ]] && shown="$(printf "$fmt" "$value")"
  row "$name" "$shown" "$max" "$result"
done < <(jq -r '.resolved.base as $b | .thresholds[] | [.name, (.max | tostring), (.no_data // "exceeds"), (.override.file // $b)] | @tsv' <<<"$th")

echo
heading "lost work" value limit result
# Completions against the rate Omes was held to. DURATION, not the window: the window adds the worker's start and
# the cooldown (base1-100551: 322 s for a 300 s run).
completed="$(jq -r '.workflows_completed // empty' <<<"$readings")"
rate="$(jq -r '.rate // empty' "$meta")"; dur_s="$(lw_dur_s "$(jq -r '.duration // empty' "$meta")")"
floor="$(lw_load_floor "$rate" "$dur_s")"
resets="$(jq -r '.counter_resets // 0' <<<"$readings")"
note=""; (( resets > 0 )) && note=", increase() over $resets counter reset(s)"
if [[ -z "$completed" ]]; then
  row workflows_completed - "${floor:+>= $floor}" "not read (window under 30 s)"
elif [[ -z "$floor" ]]; then
  row workflows_completed "$completed" "" "no floor: the run had no rate cap or no duration$note"
elif (( completed < floor )); then
  row workflows_completed "$completed" ">= $floor" "BELOW (0.95 x ${rate}/s x ${dur_s} s$note)"; block=1
else
  row workflows_completed "$completed" ">= $floor" "ok (0.95 x ${rate}/s x ${dur_s} s$note)"
fi

omes_exit="$(jq -r '.omes_exit // empty' "$meta")"
if [[ -z "$omes_exit" ]]; then row omes_exit - 0 "not recorded"
elif (( omes_exit != 0 )); then row omes_exit "$omes_exit" 0 "EXCEEDS (Omes failed; its log is $RESULTS_DIR/$RUN_ID.log)"; block=1
else row omes_exit 0 0 ok; fi

# Not judged on its own: a failed iteration already makes Omes exit nonzero, with or without
# --continue-on-iteration-failure. Shown so the count of lost iterations sits next to the verdict.
failed="$(jq -r '.iterations_failed // empty' "$meta")"
tally="$(jq -r '.omes_tally // empty | "Omes counted \(.succeeded) succeeded, \(.failed) failed"' "$meta")"
if [[ -z "$failed" ]]; then row iterations_failed - "" "not recorded"
else row iterations_failed "$failed" "" "not judged apart from omes_exit${tally:+; $tally}"; fi

tq="$(jq -r '.task_queue // empty' "$meta")"
if [[ -z "$tq" ]]; then
  row workflows_not_completed - 0 "not recorded (no task_queue in the run JSON)"
else
  # The cell the run recorded, as with Prometheus above; SWEEP_ADDRESS overrides.
  addr="${SWEEP_ADDRESS:-$(jq -r .cell "$meta")}"
  cutoff="$(date -u -d "@$(( end_epoch - SWEEP_MARGIN_S ))" +%Y-%m-%dT%H:%M:%SZ)"
  if sweep="$(NAMESPACE="$(jq -r '.namespace // "default"' "$meta")" "$here/sweep.sh" "$addr" "$tq" "$cutoff")"; then
    sweep_json="$sweep"; bad="$(lw_unexpected "$sweep")"; total="$(lw_sweep_total "$sweep")"
    n_bad="$(jq --arg ok "${SWEEP_EXPECTED:-Completed}" '($ok | split(",")) as $ok | [to_entries[] | select(.key as $k | any($ok[]; . == $k) | not) | .value] | add // 0' <<<"$sweep")"
    if (( total == 0 )); then row workflows_not_completed - 0 "EXCEEDS (no data: the sweep found no workflow on $tq started before $cutoff)"; block=1
    elif [[ -n "$bad" ]]; then row workflows_not_completed "$n_bad" 0 "EXCEEDS ($bad of $total started before $cutoff)"; block=1
    else row workflows_not_completed 0 0 "ok ($total started before $cutoff, all ${SWEEP_EXPECTED:-Completed})"; fi
  else
    row workflows_not_completed - 0 "EXCEEDS (no data: the sweep of $tq on $addr failed)"; block=1
  fi
fi

echo
if [[ $block -eq 1 ]]; then echo "VERDICT: BLOCK"; finish BLOCK 1; fi
if [[ -n "$short" ]]; then
  echo "no verdict: the window is $(( end_epoch - start_epoch )) s, steady-state.yaml trusts nothing under $short, and no lost-work rule BLOCKed" >&2
  finish "" 3
fi
echo "VERDICT: ADVANCE"
finish ADVANCE 0
