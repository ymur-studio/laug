#!/usr/bin/env bash
# Run one Omes scenario with a Go worker against a cell.
# Flags checked against `go run ./cmd/omes run-scenario-with-worker --help` at omes f0de206 (2026-10-09), the
# commit laug upstream checks out (its -ref default).
# The worker serves SDK metrics on WORKER_METRICS_ADDR and the scenario client on CLIENT_METRICS_ADDR; both are
# scraped by the cell's Prometheus as job sdk-workers (cells/compose/prometheus.yml).
# The run JSON is written whatever Omes's exit, with that exit, the failed iterations in its log and its own tally, and
# the script then exits with Omes's status. It records the server the load ran on twice: server_tag, the image tag laug
# started the cell with (SERVER_TAG, from laug load: the lease's server_tag, or the local cell's history image), and
# server_version, what the cell's frontend says it runs, read before the load (temporal operator cluster system;
# "1.32.1" on a temporalio/server:1.32.1 cell, seen). Runs recorded before 2026-10-14 have neither; every cell then ran
# 1.31.0. Until then a failed Omes ended the script before its run JSON, and the verdict exited 2 with no reading;
# now it BLOCKs. A TERM or an INT to this script stops Omes, writes no run JSON and exits 143 or 130.
# After server_tag it records backend, the lease's (compose or firecracker; null when no lease names this cell), a
# microVM's firecracker_version and cache_type from the lease (null on compose) and host_virtualized, whether this host
# is a VM (the hypervisor flag in /proc/cpuinfo), where a microVM cell runs nested.
set -euo pipefail

SCENARIO="${1:-workflow_with_single_noop_activity}"
OMES_DIR="${OMES_DIR:-.upstream/omes}"
CELL_ADDRESS="${CELL_ADDRESS:-127.0.0.1:7233}"
NAMESPACE="${NAMESPACE:-default}"
RUN_ID="${RUN_ID:-$(date -u +%Y%m%d-%H%M%S)}"
DURATION="${DURATION:-}"          # e.g. 10m; leave empty to use ITERATIONS
ITERATIONS="${ITERATIONS:-1000}"
MAX_CONCURRENT="${MAX_CONCURRENT:-}"
RATE="${RATE:-}"                  # iterations per second cap; empty lets the scenario run as fast as it can
CONTINUE_ON_FAILURE="${CONTINUE_ON_FAILURE:-0}"   # 1: keep going past failed iterations (fault runs); Omes stops on the first by default
RESULTS_DIR="${RESULTS_DIR:-load/results}"
WORKER_METRICS_ADDR="${WORKER_METRICS_ADDR:-0.0.0.0:9092}"
CLIENT_METRICS_ADDR="${CLIENT_METRICS_ADDR:-0.0.0.0:9093}"
INT_GRACE_S="${INT_GRACE_S:-10}"   # a load stopped by a TERM or an INT: how long Omes has to end on its INT before TERM

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=load/lost-work.sh
. "$here/lost-work.sh"

[[ -d "$OMES_DIR/cmd/omes" ]] || { echo "no Omes at $OMES_DIR; run ${LAUG:-bin/laug} upstream first" >&2; exit 2; }
mkdir -p "$RESULTS_DIR"
# The last line names the run JSON by RESULTS_DIR as given, load/results under laug load, not by the absolute path
# below, which holds a home path on most hosts.
results_shown="${RESULTS_DIR%/}"
RESULTS_DIR="$(cd "$RESULTS_DIR" && pwd)"

# A reaped lease mid-run loses the cell and the run (seen once on the dev host; the reading is kept outside the
# tree). Refuse up front when the last lease cannot cover the run plus a minute of slack.
LEASE_FILE="${LEASE_FILE:-.last-lease.json}"
if [[ -n "$DURATION" && -f "$LEASE_FILE" ]] && [[ "$(jq -r .address "$LEASE_FILE")" == "$CELL_ADDRESS" ]]; then
  exp=$(date -d "$(jq -r .expires_at "$LEASE_FILE")" +%s)
  need=$(( $(date +%s) + $(lw_dur_s "$DURATION") + 60 ))
  if (( exp < need )); then
    echo "lease on $CELL_ADDRESS expires $(date -u -d "@$exp" +%H:%M:%SZ), run needs until $(date -u -d "@$need" +%H:%M:%SZ); lease again with TTL=$(( need - $(date +%s) + 60 ))" >&2
    exit 3
  fi
fi

# The lease's backend and snapshot values when the lease is this cell's, read as faults/lib.sh's lease_read reads them:
# a lease from before the backends named themselves, no backend key or an empty one, is firecracker when it names a
# guest, else compose.
backend=""; fc_version=""; cache_type=""
if [[ -f "$LEASE_FILE" ]] && [[ "$(jq -r .address "$LEASE_FILE")" == "$CELL_ADDRESS" ]]; then
  backend="$(jq -r '(.backend // "") as $b | if $b != "" then $b elif (.ssh // "") == "" then "compose" else "firecracker" end' "$LEASE_FILE")"
  fc_version="$(jq -r '.firecracker_version // ""' "$LEASE_FILE")"; cache_type="$(jq -r '.cache_type // ""' "$LEASE_FILE")"
fi
host_virtualized=false; ! grep -qw hypervisor /proc/cpuinfo 2>/dev/null || host_virtualized=true

# The version the frontend reports, or empty when it does not answer within 10 s; a load against a dead cell fails on
# its own below.
server_version="$(timeout 10 temporal operator cluster system --address "$CELL_ADDRESS" -o json 2>/dev/null | jq -r '.serverVersion // empty' 2>/dev/null)" || server_version=""
if [[ -n "${SERVER_TAG:-}" && -n "$server_version" && "$server_version" != "$SERVER_TAG" ]]; then
  echo "the cell at $CELL_ADDRESS says it runs $server_version, SERVER_TAG says $SERVER_TAG; the run JSON records both" >&2
fi

start_ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cmd=(go run ./cmd/omes run-scenario-with-worker
     --scenario "$SCENARIO"
     --language go
     --run-id "$RUN_ID"
     --server-address "$CELL_ADDRESS"
     --namespace "$NAMESPACE"
     --worker-prom-listen-address "$WORKER_METRICS_ADDR"
     --prom-listen-address "$CLIENT_METRICS_ADDR"
     --log-level warn)
# --timeout ends a run whose cooldown hangs: Omes stops starting iterations at DURATION, then waits for the ones in
# flight with no limit of its own. At DURATION plus 120 s it cancels them and exits nonzero (omes f0de206,
# cmd/omes/run_scenario.go:80 and loadgen/generic_executor.go:70-74; the cancel is read in source only).
# run-scenario-with-worker took --timeout 140s on a 20 s run against a dev server and exited 0. The
# base runs' windows ran 22 s past DURATION, worker start included; 120 s is slack, not a measurement.
timeout=""
if [[ -n "$DURATION" ]]; then
  timeout="$(( $(lw_dur_s "$DURATION") + 120 ))s"
  cmd+=(--duration "$DURATION" --timeout "$timeout")
else cmd+=(--iterations "$ITERATIONS"); fi
[[ -n "$MAX_CONCURRENT" ]] && cmd+=(--max-concurrent "$MAX_CONCURRENT")
[[ -n "$RATE" ]] && cmd+=(--max-iterations-per-second "$RATE")
[[ "$CONTINUE_ON_FAILURE" == 1 ]] && cmd+=(--continue-on-iteration-failure)

echo "run $RUN_ID: $SCENARIO against $CELL_ADDRESS/$NAMESPACE" >&2
log="$RESULTS_DIR/$RUN_ID.log"
# Omes runs in the background under setsid: go run and its omes child, in a session and process group of their own, as
# faults/run.sh starts a raun's load. faults/run.sh starts this script with INT ignored, as bash starts a background
# command, and go run keeps it ignored through its build of omes: it catches INT only once it runs omes (read in go's
# source, cmd/go/internal/base/base.go, RunStdin). So env sets INT back to its default (coreutils 8.31 or later). A TERM
# or an INT to this script reaches Omes, by the trap, as INT to that group. go run passes no signal on to its program
# (cmd/go/internal/base/signal.go), and waits for it through an INT. omes cancels its run on INT: it sends INT
# to its worker, which it starts in a process group of its own, waits for it, and on the way out removes its
# omes-temp-<n> build dir, 48 MB (omes f0de206 cmd/omes/run_worker.go:78-90, internal/workerctl/run.go:89-95, :146). A
# load so stopped against a dev server ended 0.3 s after the signal, with no process of its run and no build dir left,
# and Omes exited 1: once by TERM to this script's group, as a raun's cleanup sends it, and once by INT to this script,
# as Ctrl-C sends it. omes KILLs a worker still up 30 s after its INT (internal/workerctl/run.go:192-213;
# read in source). A TERM to Omes ends go run and omes at once, and leaves both the worker and the dir: the worker held
# the fifo below open, and tee and this script waited on it, 17 minutes past a raun's end on the dev host. So Omes's
# group gets TERM only if Omes is still up INT_GRACE_S after the INT. Once go run has exited, each process still in
# Omes's session gets TERM: a load stopped by TERM alone against a dev server ended within 1 s of it, its worker with
# it. tee reads Omes's output through the fifo and ignores TERM, so the log keeps all of it. A load stopped
# this way gets no run JSON, as when the signal killed this script: a raun's cleanup stops its load so at every raun's
# end. set -e would end the script on a failed Omes before the JSON below. go run prints a failed program's status
# ("exit status 3") and exits 1 itself (seen on go 1.24.1).
pipe="$(mktemp -d)"; trap 'rm -rf "$pipe"' EXIT; mkfifo "$pipe/out" "$pipe/tick"
stopped=""; signals=0; omes_pid=""; esc_pid=""
# escalate runs in the background from the first stop on. It sends INT to Omes's group, again each 0.2 s until setsid
# has made the group: until then the subshell that becomes Omes ignores INT, as bash starts a background command. If
# go run is still up INT_GRACE_S after the stop, it sends TERM to that group and marks it. It ends once go run has. A
# look is read's timeout on a fifo that nothing writes: no fork, and no sleep, which the harness fakes. The wait below
# needs no look of its own, so it sees Omes end at once, and a stop that comes just before it starts still escalates.
escalate() {
  trap '' TERM
  local t sent="" t0="${EPOCHREALTIME/[.,]/}"
  exec {t}<>"$pipe/tick"
  while kill -0 "$omes_pid" 2>/dev/null; do
    [[ -n "$sent" ]] || { kill -INT -- "-$omes_pid" 2>/dev/null && sent=1; }
    if (( ${EPOCHREALTIME/[.,]/} - t0 >= INT_GRACE_S * 1000000 )); then
      : > "$pipe/termed"; kill -TERM -- "-$omes_pid" 2>/dev/null || kill -TERM "$omes_pid" 2>/dev/null; return 0
    fi
    read -r -t 0.2 -u "$t" _
  done
}
stop() { # stop <TERM|INT>: what came; with Omes started, escalate once
  stopped="$1"; signals=$(( signals + 1 ))
  [[ -n "$omes_pid" && -z "$esc_pid" ]] || return 0
  escalate < /dev/null > /dev/null 2>&1 &
  esc_pid=$!
}
# omes_left prints the pid of each process still in Omes's session, whose id is omes_pid: field 6 of /proc/<pid>/stat,
# read past the name in parentheses, which can hold a space, as gate/lib.sh's sid_of reads it.
omes_left() {
  local f st w
  for f in /proc/[0-9]*/stat; do
    { read -r st < "$f"; } 2>/dev/null || continue
    read -r -a w <<<"${st##*) }"
    [[ "${w[3]:-}" != "$omes_pid" ]] || { f="${f#/proc/}"; echo "${f%/stat}"; }
  done
}
trap 'stop TERM' TERM; trap 'stop INT' INT
set +e
( cd "$OMES_DIR" && exec env --default-signal=INT setsid "${cmd[@]}" ) > "$pipe/out" 2>&1 &
omes_pid=$!
( trap '' TERM; exec tee "$log" ) < "$pipe/out" &
tee_pid=$!
(( signals == 0 )) || stop "$stopped"   # a signal before omes_pid was set reached no Omes
# wait ends early, at 128 plus its number, when a trapped signal comes: then Omes is waited for again.
omes_exit=""
while [[ -z "$omes_exit" ]]; do
  n="$signals"; wait "$omes_pid"; rc=$?
  (( signals != n )) || omes_exit="$rc"
done
while [[ -n "$esc_pid" ]] && kill -0 "$esc_pid" 2>/dev/null; do wait "$esc_pid"; done
left="$(omes_left | paste -sd ' ')"
if [[ -n "$left" ]]; then
  # shellcheck disable=SC2086 # one pid a word
  kill -TERM $left 2>/dev/null || :
  echo "Omes left $(wc -w <<<"$left") process(es) in its session, its worker among them; each got TERM" >&2
fi
while kill -0 "$tee_pid" 2>/dev/null; do wait "$tee_pid"; done
set -e
if [[ -n "$stopped" ]]; then
  code=$(( 128 + $(kill -l "$stopped") ))
  termed=""; [[ ! -e "$pipe/termed" ]] || termed=", then TERM $INT_GRACE_S s on,"
  echo "stopped by $stopped: Omes got INT$termed and exited $omes_exit; no run JSON for a load cut short; exit $code" >&2
  exit "$code"
fi

end_ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
omes_commit="$(git -C "$OMES_DIR" rev-parse --short HEAD)"
# Omes names the run's task queue omes-<run-id> (omes f0de206, loadgen/scenario.go:434; every load log here opens
# with the worker's "Go worker will run on task queue omes-<run-id>"). load/sweep.sh counts its workflows.
jq -n --arg run "$RUN_ID" --arg scenario "$SCENARIO" --arg cell "$CELL_ADDRESS" \
      --arg ns "$NAMESPACE" --arg start "$start_ts" --arg end "$end_ts" --arg omes "$omes_commit" \
      --arg iter "$ITERATIONS" --arg dur "$DURATION" --arg rate "$RATE" --arg conc "$MAX_CONCURRENT" \
      --arg prom "${PROM_URL:-}" --arg project "${PROJECT:-}" --arg timeout "$timeout" --arg tq "omes-$RUN_ID" \
      --arg tag "${SERVER_TAG:-}" --arg version "$server_version" \
      --arg backend "$backend" --arg fcv "$fc_version" --arg cache "$cache_type" --argjson virt "$host_virtualized" \
      --argjson omes_exit "$omes_exit" --argjson failed "$(lw_count_failed "$log")" --argjson tally "$(lw_tally "$log")" \
      '{run_id:$run, scenario:$scenario, cell:$cell, prometheus:$prom, project:$project,
        server_tag:(if $tag == "" then null else $tag end),
        backend:(if $backend == "" then null else $backend end), firecracker_version:(if $fcv == "" then null else $fcv end),
        cache_type:(if $cache == "" then null else $cache end), host_virtualized:$virt,
        server_version:(if $version == "" then null else $version end),
        namespace:$ns, start:$start, end:$end, omes_commit:$omes, iterations:$iter, duration:$dur, rate:$rate, max_concurrent:$conc,
        timeout:$timeout, task_queue:$tq, omes_exit:$omes_exit, iterations_failed:$failed, omes_tally:$tally}' \
      > "$RESULTS_DIR/$RUN_ID.json"
echo "window $start_ts .. $end_ts recorded in $results_shown/$RUN_ID.json; Omes exit $omes_exit, $(jq -r .iterations_failed "$RESULTS_DIR/$RUN_ID.json") failed iteration(s) in its log" >&2
exit "$omes_exit"
