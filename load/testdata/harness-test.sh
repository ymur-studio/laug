#!/usr/bin/env bash
# make harness-test: the lost-work rules, load/sweep.sh, load/run-omes.sh, load/verdict.sh, load/read-thresholds.sh,
# load/thresholds.sh and the release files in load/releases/, cells/softasserts.sh, faults/run.sh (its shard faults and
# serverfi too), faults/candidate.sh and faults/candidates.yaml, the gate's verdict rule and its one exit (laug judge,
# gate/post.sh, laug gate post), the same-verdict checks (laug gate rejudge and laug gate compare), whole gate runs
# (gate/run.sh, laug gate run -fake) with GATE_FAKE=1 and the fakes in gate/ here, and workflow Gate's step driver
# (gate/step.sh: usage, its first steps, the guard, its TERM trap and stopped's
# sweep), against fixtures in this directory and cells/testdata, and fake tools in
# fakebin/ (curl, temporal, go, git, date, sleep, docker, ssh, ss; not bin/, which .gitignore drops; the fake curl is
# laugd too, with other cells busy at set times for the wait before a load, and the fake ss holds Omes metrics ports at
# set times) put first on PATH. No cell, no Docker, no network but loopback: the gate cases' laugd on a free 127.0.0.1
# port, and the README block's pool and gate on 127.0.0.1:7700, or a free port when that one is taken. The fake date and
# sleep share a clock that starts at a fixed epoch and moves only when the fake sleep is called, so a fault run's windows
# land on known seconds and take no real time.
#
# Every part is driven through bin/laug, which make builds from this tree first: thresholds, fault, load, sweep,
# candidate, judge, gate post, rejudge, compare, run and tolerance, host type, pool serve, down and bench, cell and
# version; the Firecracker pool's verbs as far as they go without root. Near the end, runbook 1's block, the long walk
# README.md's block is cut from, runs through bin/laug: make build, a pool of fakes with a lease, the thresholds, a
# whole gate on fakes and a rejudge of a committed run.
#
# The server tag section checks that SERVER_TAG's default is one value everywhere and that cells/backend-compose.sh,
# laug pool serve, laug cell and laug load pass it on. The last section reads every committed faults/results/*.json and load baseline
# with the lost-work floors and prints the table; it fails if a floor flags a run whose committed result was ADVANCE.
set -uo pipefail
# The knobs the scripts under test read from the environment, and the fakes' FAKE_*, unset so that the caller's cannot
# change what a test expects: a shell that exported POOL_QUIET_WAIT_S=0 for a batch, or make harness-test
# THRESHOLDS=..., which make exports to the recipe, would fail checks that are right; and a gate runs this test with
# its own SERVER_TAG, PLAN and GATE_* exported. A test that wants one sets it.
# A gate that runs this test as its R1 holds its checkout's lock, GATE_LOCK, gate/.lock unless the gate was given
# another; the name is kept before the unset below, for the builds further down.
gate_lock="${GATE_LOCK:-gate/.lock}"
unset THRESHOLDS THRESHOLDS_RELEASES POOL_QUIET_WAIT_S POOL_ADDR LEASE_FILE CATALOG CANDIDATES RESULTS_DIR RUN_ID \
  SCENARIO RATE DURATION ITERATIONS MAX_CONCURRENT CONTINUE_ON_FAILURE NAMESPACE CELL_ADDRESS PROJECT PROM_URL \
  FALLBACK_PROM_URL MIN_WINDOW_S VERDICT_JSON SWEEP_MARGIN_S SWEEP_EXPECTED SWEEP_ADDRESS SWEEP_RETRIES SWEEP_RETRY_S \
  SA_SLICE_S CANDIDATE_BEFORE_S CANDIDATE_REREAD_S CANDIDATE_RELEASE SERVER_TAG TDBG_IMAGE ROTATE_SLACK_S FORCE \
  OMES_DIR WORKER_METRICS_ADDR CLIENT_METRICS_ADDR FC_GUEST_KEY FC_GUEST_KNOWN_HOSTS GUEST_DYNCONFIG_ORIG GUEST_COMPOSE_DIR GATE GATE_POST GATE_RESULTS \
  CYCLES TTL_SHORT SKIP_WARMUP INT_GRACE_S \
  PLAN ITEMS BASE GATE_FAKE GATE_LOCK GATE_HOST_LOCK GATE_POOL_LISTEN GATE_BACKEND VISIBILITY BIND_ADDR DYNCONFIG_TEMPLATE A B \
  LAUG_COMPOSE_EXTRA FC_NET \
  LAUG
unset "${!FAKE_@}"
# LAUG is the word a line to type starts with, which laug sets for each script it runs: laug when its PATH finds a
# laug, as Go's exec.LookPath does, by an absolute path, else bin/laug. A script this test runs on its own reads none,
# and its lines say bin/laug; one that bin/laug runs says lw, the word this PATH gives.
lw=bin/laug; [[ "$(command -v laug)" == /* ]] && lw=laug

root="$(cd "$(dirname "$0")/../.." && pwd)"; td="$root/load/testdata"
# Every fake cell starts from the fixture laug.yaml, not the checkout's, and faults/lib.sh restores the fixture: a
# gate run of a change to cells/compose/dynamicconfig/laug.yaml runs this test, and must not fail it for that alone.
export DYNCONFIG_TEMPLATE="$td/laug.yaml"
cd "$root" || exit 2
for t in jq yq awk bc setsid seq ss; do command -v "$t" >/dev/null || { echo "harness-test needs $t on PATH" >&2; exit 2; }; done
REAL_DATE="$(command -v date)"; REAL_SLEEP="$(command -v sleep)"; REAL_GIT="$(command -v git)"
export REAL_DATE REAL_SLEEP REAL_GIT
FAKE_PATH="$td/fakebin:$PATH"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
# shellcheck source=load/lost-work.sh
. "$root/load/lost-work.sh"

passed=0; failed=0
pass() { passed=$(( passed + 1 )); printf '  ok    %s\n' "$1"; }
fail() { failed=$(( failed + 1 )); printf '  FAIL  %s\n' "$1"; shift; local l; for l in "$@"; do printf '        %s\n' "$l"; done; }
# shown <path>: the path as laug writes one (cmd/laug, App.pathShown): ~/ and the rest under $HOME, else as it is. A lab
# batch sets TMPDIR under the batch user's home, so every $td path lands there and laug's lines name it so.
shown() { if [[ -n ${HOME:-} && $HOME != / && $1 == "$HOME"/* ]]; then echo "~/${1#"$HOME"/}"; else echo "$1"; fi; }

eq() { if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "got:  $2" "want: $3"; fi; }
has() { if grep -qF -- "$3" <<<"$2"; then pass "$1"; else fail "$1" "missing: $3" "in: $(tail -5 <<<"$2" | tr '\n' '|')"; fi; }
hasnt() { if grep -qF -- "$3" <<<"$2"; then fail "$1" "unexpected: $3"; else pass "$1"; fi; }
section() { printf '\n%s\n' "$1"; }
# note: a line beside ok and FAIL that counts as neither: what this host or this run changes about the checks near it.
note() { printf '  note  %s\n' "$1"; }
# gone <pid>: no /proc entry, or a zombie no one has reaped yet. The pid's state is field 3 of /proc/<pid>/stat, read
# after the last ") " since the command name, field 2, can hold spaces and parentheses. A zombie, state Z, answers
# kill -0 until its parent, or PID 1 for an orphan, reaps it. laug runbook check reads a process group the same way,
# and a Z pid's threads too, since a process whose main thread alone has exited reads Z. gone need not: the pids it
# reads, the fakes' bash, sleep and timeout, run one thread each.
gone() { local s; s="$(sed 's/.*) //; s/ .*//' "/proc/$1/stat" 2>/dev/null)"; [[ -z "$s" || "$s" == Z ]]; }

# A fresh fake world: its own state directory and a clock at 0 s past FAKE_T0.
fresh() {
  FAKE_DIR="$(mktemp -d "$work/f.XXXXXX")"; FAKE_CLOCK="$FAKE_DIR/clock"; echo 0 > "$FAKE_CLOCK"
  mkdir -p "$FAKE_DIR/results" "$FAKE_DIR/omes/cmd/omes"
  export FAKE_DIR FAKE_CLOCK
}
calls() { cat "$FAKE_DIR/temporal.calls" 2>/dev/null || echo 0; }
T0=1791900000   # 2026-10-13T14:00:00Z; omes-raun.txt is written against it, in +0530 and in Z

# Soft assertions. cells/testdata/softassert.txt holds one line in the shape server 1.31.0 logs a soft assertion in,
# printed by the server's own logger and softassert package (common/log, common/softassert) in a scratch program,
# plus a near miss ("failed-assertion":false), one of the image's plain stderr lines and one of its info lines
# (seen). File paths in the lines start at the module root: the image's own lines carry its build
# directory in front. sa_line copies the soft assertion line to a time and a message, for the fake docker's logs:
# "<seconds past T0><TAB><line>".
SA_FIXTURE="$root/cells/testdata/softassert.txt"
sa_line() { # sa_line <seconds past T0> <message> [false]
  local ts; ts="$("$REAL_DATE" -u -d "@$(( T0 + $1 ))" +%Y-%m-%dT%H:%M:%S.000Z)"
  printf '%s\t%s\n' "$1" "$(grep -m1 -F '"failed-assertion":true' "$SA_FIXTURE" \
    | jq -c --arg ts "$ts" --arg m "failed assertion: $2" --argjson fa "${3:-true}" '.ts = $ts | .msg = $m | .["failed-assertion"] = $fa')"
}
# One cell's logs, on the test catalog's timeline (before T0..60, during 60..80, drain 80..110, after 110..200).
# temporal-frontend2 has no container, so it reads null. The line at 60 s is on the boundary of before and during.
# Each role but the worker prints its first line 1 s after its container's StartedAt (the fake's T0 - 60 s), as the
# image does (cells/softasserts.sh, rotation); the worker has printed nothing.
SA_LOGS="$work/docker"; mkdir -p "$SA_LOGS"
sa_start() { printf -- '-59\t2026/10/13 13:59:01 Loading configuration from environment variables only\n'; }
{ sa_start; printf '5\t2026/10/13 14:00:05 Processing config file as template; filename=config_template_embedded.yaml\n'; } > "$SA_LOGS/temporal-frontend.txt"
{ sa_start; sa_line 10 "loadedTasks went negative"; sa_line 60 "completed task was already acked"
  sa_line 70 "completed task was already acked"; sa_line 95 "expected a task from match"; } > "$SA_LOGS/temporal-matching.txt"
{ sa_start; sa_line 65 "loadedTasks went negative"; sa_line 150 "ArchetypeID not specified, defaulting to Workflow."
  sa_line 150 "ArchetypeID not specified, defaulting to Workflow." false; } > "$SA_LOGS/temporal-history.txt"
: > "$SA_LOGS/temporal-worker.txt"

# Three checks send Ctrl-C (SIGINT) to a script under test. A shell that starts with SIGINT ignored, as a background job
# of a non-interactive shell does, passes that on, and no trap or set -m takes it back: those checks then fail for a
# reason not theirs and wait out their own timeouts (internal/rejudge/testdata/runs/g20261014-184524). Say so first.
# awk reads its own status: bash ignores SIGQUIT for itself, and starts its children as it was started (seen)
ign="$(awk '$1 == "SigIgn:" { print $2 }' /proc/self/status 2>/dev/null)"
if [[ -n "$ign" ]] && (( 0x$ign & 2 )); then
  fail "this test started with SIGINT ignored: its three Ctrl-C checks below will fail" \
    "run it in the foreground, or under env --default-signal=INT as gate/run.sh does"
fi
# The checks below drive bin/laug where a laug verb has taken a make target's place, and every gate case runs its
# pool serve: make builds it by its file rule first, so bin/laug is relinked only when a Go source is newer than it.
# After a commit that changed no Go source, or a tree whose changes went, its stamp is not the one make build would
# give it now, HEAD with -dirty when git status prints anything, and the checks that hold bin/laug to HEAD fail
# (cells/results/2026-10-23-fresh-clone.txt): then make builds it again with -B. A real gate runs this test as its R1
# while its own pool runs bin/laug pool serve, and holds its lock: then this test builds nothing, here or in
# the README block below, so no build relinks bin/laug under that gate or stamps it -dirty for the gate's own run
# directory. flock -n <file> true fails while a gate holds it, as gate/step.sh finds a gate; -E 75 tells
# that refusal from a lock file flock could not open.
in_gate=""; flock -n -E 75 "$gate_lock" true 2>/dev/null; [[ $? != 75 ]] || in_gate=1
if [[ -n "$in_gate" ]]; then
  printf '  note  a gate holds %s, so this test runs as its R1: it builds nothing, and drives the bin/laug that gate checked\n' "$gate_lock"
  [[ -x bin/laug ]] || fail "bin/laug: a gate holds $gate_lock, so this test builds nothing, and it is not there"
else
  env -u MAKEFLAGS make -s --no-print-directory bin/laug >/dev/null 2>&1 \
    || fail "make bin/laug: bin/laug, which the checks below drive and every gate case's pool runs"
  stamp_now="$(git rev-parse HEAD 2>/dev/null)" && { [[ -z "$(git status --porcelain 2>/dev/null)" ]] || stamp_now+=-dirty; }
  stamp_was="$(bin/laug version -json 2>/dev/null | jq -r .commit 2>/dev/null)"
  if [[ -n "$stamp_now" && "$stamp_was" != "$stamp_now" ]]; then
    printf '  note  bin/laug carries a stamp other than the one make build gives it now: built again\n'
    env -u MAKEFLAGS make -s --no-print-directory -B bin/laug >/dev/null 2>&1 \
      || fail "make -B bin/laug: bin/laug's stamp was not HEAD's, and the build again failed"
  fi
fi

section "lost-work.sh"
eq "duration 5m"  "$(lw_dur_s 5m)" 300
eq "duration 1m30s" "$(lw_dur_s 1m30s)" 90
eq "duration 320s" "$(lw_dur_s 320s)" 320
eq "load floor 0.95 x 20/s x 300 s" "$(lw_load_floor 20 300)" 5700
eq "load floor without a rate" "$(lw_load_floor "" 300)" ""
eq "after floor 20/s x (90 - 10)" "$(lw_after_floor 20 90)" 1600
eq "after floor 20/s x (91 - 10)" "$(lw_after_floor 20 91)" 1620
eq "failed iterations: ERROR lines only, not the FATAL line that quotes one" "$(lw_count_failed "$td/omes-abort.txt")" 2
eq "failed iterations, run that exits 1 with a tally" "$(lw_count_failed "$td/omes-fail.txt")" 3
eq "failed iterations, missing log" "$(lw_count_failed "$work/none.txt")" null
eq "tally from the error Omes exits with" "$(lw_tally "$td/omes-fail.txt")" '{"succeeded":5997,"failed":3}'
printf '2026-10-13T14:05:20.000Z\tINFO\tloadgen/generic_executor.go:231\tRun completed in 5m0.2s: 5997 iterations succeeded, 3 failed\n' > "$work/info.txt"
eq "tally from the info line" "$(lw_tally "$work/info.txt")" '{"succeeded":5997,"failed":3}'
eq "no tally on a clean run" "$(lw_tally "$td/omes-ok.txt")" null
eq "no tally when Omes stopped at the first failure" "$(lw_tally "$td/omes-abort.txt")" null
eq "buckets by window, offsets +0530 and Z" \
  "$(lw_buckets "$td/omes-raun.txt" $(( T0 + 60 )) $(( T0 + 80 )) $(( T0 + 110 )) $(( T0 + 200 )))" \
  '{"before":1,"during":2,"drain":1,"after":0,"late":1,"unplaced":0,"not_found":0,"total":5}'
eq "buckets, fault never removed: during runs to the end, drain and after never opened" \
  "$(lw_buckets "$td/omes-raun.txt" $(( T0 + 60 )) "" "" "")" \
  '{"before":1,"during":4,"drain":null,"after":null,"late":0,"unplaced":0,"not_found":0,"total":5}'
eq "buckets, never steady: everything is before" "$(lw_buckets "$td/omes-raun.txt" "" "" "" "")" \
  '{"before":5,"during":null,"drain":null,"after":null,"late":0,"unplaced":0,"not_found":0,"total":5}'
{ cat "$td/omes-raun.txt"; printf '13/10/2026 10:02:30\tERROR\tloadgen/generic_executor.go:196\titeration 9 failed: x\n'; } > "$work/unplaced.txt"
eq "a time that does not parse is unplaced" "$(lw_buckets "$work/unplaced.txt" $(( T0 + 60 )) $(( T0 + 80 )) $(( T0 + 110 )) $(( T0 + 200 )) | jq .unplaced)" 1
# "workflow execution not found" in both of the server's forms, during the fault and in the drain; the same words
# late, after the after window, as an Omes that outlived its cell logs them, do not count.
nf_line() { printf '%s\tERROR\tloadgen/generic_executor.go:196\titeration %s failed: failed to execute kitchen sink workflow: %s\n' "$@"; }
{ cat "$td/omes-raun.txt"
  nf_line 2026-10-13T14:01:10.000Z 1401 'workflow execution not found for workflow ID "w-ht-1401" and run ID "r"'
  nf_line 2026-10-13T14:01:30.000Z 1801 'Workflow execution not found'
  nf_line 2026-10-13T14:03:30.000Z 4201 'workflow execution not found for workflow ID "w-ht-4201" and run ID "r"'; } > "$work/not-found.txt"
eq "not found: counted in every window but late" \
  "$(lw_buckets "$work/not-found.txt" $(( T0 + 60 )) $(( T0 + 80 )) $(( T0 + 110 )) $(( T0 + 200 )) | jq -c '[.during, .drain, .late, .not_found]')" '[3,2,2,2]'
eq "unexpected statuses" "$(lw_unexpected '{"Completed":5,"Running":2,"Failed":1}')" "Running=2 Failed=1"
eq "ContinuedAsNew allowed when expected" "$(SWEEP_EXPECTED=Completed,ContinuedAsNew lw_unexpected '{"Completed":5,"ContinuedAsNew":2}')" ""
judge() { local out; out="$(lw_raun_judge "$(cat "$td/raun/$1.json")")"; echo "$?|$out"; }
eq "judge: clean raun passes" "$(judge clean)" "0|"
eq "judge: failed iteration in the after window" "$(judge after-failed)" "1|2 failed iteration(s) in the after window"
eq "judge: unplaced failed iteration" "$(judge unplaced)" "1|1 failed iteration(s) with a log time that did not parse"
eq "judge: sweep finds Running and TimedOut" "$(judge sweep-bad)" "1|sweep: Running=4 TimedOut=1 among workflows started before 2026-10-13T14:02:50Z"
eq "judge: sweep finds nothing" "$(judge sweep-empty)" "1|sweep found no workflows started before 2026-10-13T14:02:50Z"
eq "judge: sweep failed" "$(judge sweep-error)" "1|sweep failed: load/sweep.sh exited 1 against 127.0.0.1:17233"
eq "judge: after window one under the floor" "$(judge floor)" "1|after window completed 1599 workflows, floor 1600 (20/s x (90 s - 10))"
eq "judge: after window at the floor passes" "$(judge floor-edge)" "0|"
eq "judge: floor on after_s, not the wall clock's 96 s (20/s x 91 s was 1820 before)" "$(judge clock-step)" "0|"
eq "judge: no after_s, the floor takes window_seconds" "$(judge no-after-s)" "1|after window completed 1619 workflows, floor 1620 (20/s x (91 s - 10))"
eq "judge: workflow execution not found before the after window ended" "$(judge not-found)" \
  "1|1 failed iteration(s) before the after window ended: workflow execution not found"

section "read-thresholds.sh"
fresh
rt() { env PATH="$FAKE_PATH" FAKE_T0=1000 PROM_URL=http://127.0.0.1:19090 "$@" load/read-thresholds.sh 1000 1400; }
out="$(rt)"
eq "no reset: counter edges, 20/s over 400 s" "$(jq -c '{workflows_completed, counter_resets, window_seconds}' <<<"$out")" \
  '{"workflows_completed":8000,"counter_resets":0,"window_seconds":400}'
out="$(rt FAKE_RESETS=2 FAKE_INCREASE=7999.6)"
eq "a reset in the window: increase(), rounded" "$(jq -c '{workflows_completed, counter_resets}' <<<"$out")" \
  '{"workflows_completed":8000,"counter_resets":2}'

section "load/thresholds.sh (threshold sets per server release)"
# The base, load/steady-state.yaml, measured on 1.31.0, plus load/releases/<major>.<minor>.yaml when one exists for the
# tag. thr <tag> [env...]: runs the resolver; sets thr_rc (its exit), thr_out (its YAML) and j (that YAML as JSON, or
# empty), its stderr in work/thr.err.
thr() {
  local t="$1"; shift
  thr_out="$(env "$@" load/thresholds.sh "$t" 2>"$work/thr.err")"; thr_rc=$?
  j=""; [[ -z "$thr_out" ]] || j="$(yq -o=json -I=0 '.' <<<"$thr_out")"
}
th_core() { jq -c '[.thresholds[] | [.name, .query, .max]]'; }   # what a release file must not change
base_core="$(yq -o=json -I=0 '.' load/steady-state.yaml | th_core)"
thr 1.32.1
eq "1.32.1: the base plus load/releases/1.32.yaml, exit 0" "$thr_rc:$(jq -c '.resolved | [.release, .release_file, .given, .base, .base_server_tag]' <<<"$j")" \
  '0:["1.32","load/releases/1.32.yaml",false,"load/steady-state.yaml","1.31.0"]'
eq "1.32.1: the two activity rows alone are not judged on no data, and say which file" \
  "$(jq -c '[.thresholds[] | [.name, .no_data, .override.file]]' <<<"$j")" \
  '[["workflow_task_schedule_to_start_p99_ms","exceeds",null],["activity_schedule_to_start_p99_ms","not_judged","load/releases/1.32.yaml"],["workflow_end_to_end_p95_ms","exceeds",null],["persistence_latency_p99_ms","exceeds",null],["service_error_rate","exceeds",null],["frontend_request_p99_ms","exceeds",null],["workflow_task_schedule_to_start_over_20ms","exceeds",null],["activity_schedule_to_start_over_10ms","not_judged","load/releases/1.32.yaml"],["persistence_latency_over_10ms","exceeds",null],["frontend_request_over_10ms","exceeds",null],["workflow_end_to_end_over_50ms","exceeds",null]]'
eq "1.32.1: names, queries and maxes are the base's, window and server_tag too" "$(th_core <<<"$j"):$(jq -r '.window + " " + .server_tag' <<<"$j")" "$base_core:5m 1.31.0"
has "1.32.1: the override's reason is eager activity" "$(jq -r '.resolved.overrides[0].reason' <<<"$j")" "1.32 runs activities eagerly"
eq "1.32.1: the note names both files" "$(jq -r .resolved.note <<<"$j")" \
  "load/steady-state.yaml, the base (1.31.0 baselines), with load/releases/1.32.yaml for server 1.32.1: 2 overrides"
thr 1.32.0; eq "1.32.0: the same file, by major.minor" "$(jq -r .resolved.release_file <<<"$j")" "load/releases/1.32.yaml"
thr 1.31.0
eq "1.31.0: the base it was measured on, unchanged, every no data EXCEEDS" \
  "$(jq -c '[.resolved.release_file, ([.thresholds[].no_data] | unique), .resolved.overrides]' <<<"$j"):$(th_core <<<"$j")" "[null,[\"exceeds\"],[]]:$base_core"
eq "1.31.0: the note says no release file" "$(jq -r .resolved.note <<<"$j")" \
  "load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no load/releases/1.31.yaml for server 1.31.0"
thr 1.33.0
eq "1.33.0, a release with no file: the base unchanged, and the note says so" \
  "$(jq -c '[.resolved.release_file, ([.thresholds[].no_data] | unique)]' <<<"$j"):$(jq -r .resolved.note <<<"$j")" \
  '[null,["exceeds"]]:load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no load/releases/1.33.yaml for server 1.33.0'
thr ""; eq "no tag: the base, and why" "$(jq -r .resolved.note <<<"$j")" \
  "load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no server tag to pick a release file by"
thr latest; eq "a tag with no major.minor: the base, and why" "$(jq -r .resolved.note <<<"$j")" \
  "load/steady-state.yaml, the base (1.31.0 baselines), unchanged: server tag 'latest' names no major.minor"
thr 1.32.1 THRESHOLDS=load/steady-state.yaml
eq "THRESHOLDS given wins: no release file on 1.32.1, every no data EXCEEDS" \
  "$(jq -c '[.resolved.given, .resolved.release_file, ([.thresholds[].no_data] | unique)]' <<<"$j"):$(jq -r .resolved.note <<<"$j")" \
  '[true,null,["exceeds"]]:load/steady-state.yaml, given in THRESHOLDS: no release file applies'
# A set, the base or a THRESHOLDS file, is checked too: it must not relax a rule itself (a no_data other than exceeds,
# an override: key, or the resolved: key of a set the resolver printed), and every max is a number. A saved 1.32.1 set
# passed back as THRESHOLDS would otherwise not judge activity schedule-to-start on no data for any release, unreported.
load/thresholds.sh 1.32.1 > "$work/th-saved.yaml" 2>/dev/null
yq '.thresholds[1].no_data = "not_judged"' load/steady-state.yaml > "$work/th-nodata.yaml"
yq '.thresholds[1].override = {"reason": "r"}' load/steady-state.yaml > "$work/th-override.yaml"
yq '.thresholds[1].max = "100"' load/steady-state.yaml > "$work/th-quoted.yaml"
yq '.thresholds[].no_data = "exceeds"' load/steady-state.yaml > "$work/th-exceeds.yaml"
for c in th-saved:"it holds resolved:, so load/thresholds.sh printed it; give the base it came from" \
         th-nodata:'threshold activity_schedule_to_start_p99_ms: no_data is "not_judged"; no_data belongs in load/releases/<major>.<minor>.yaml' \
         th-override:"threshold activity_schedule_to_start_p99_ms: override: is not a key of a set" \
         th-quoted:'threshold activity_schedule_to_start_p99_ms: max is "100", not a number'; do
  thr 1.32.1 THRESHOLDS="$work/${c%%:*}.yaml"
  eq "THRESHOLDS ${c%%:*}: refused, exit 2, nothing printed" "$thr_rc:$thr_out" "2:"
  has "THRESHOLDS ${c%%:*}: says why" "$(cat "$work/thr.err")" "${c#*:}"
done
hasnt "THRESHOLDS with a quoted max alone: no word about no_data" "$(cat "$work/thr.err")" "no_data belongs"
thr 1.32.1 THRESHOLDS="$work/th-exceeds.yaml"
eq "THRESHOLDS with no_data: exceeds written out: accepted; outside the repository, no path is printed" \
  "$thr_rc:$(jq -c '[.resolved.base, .resolved.given, .resolved.release_file]' <<<"$j"):$(jq -r .resolved.note <<<"$j")" \
  '0:["a file outside the repository",true,null]:a file outside the repository, given in THRESHOLDS: no release file applies'
# A set yq cannot read: with mikefarah yq v4 the set is named; with another yq, which fails on every file, the yq is.
# The Python yq here is a fake: a version line in its shape, and an exit 2 on anything else.
# The Python yq (kislyuk/yq) prints "yq <version>" to --version and fails on -o=json -I=0
# (yq 4.4.5, run in a scratch venv).
printf 'thresholds: [\n' > "$work/th-parse.yaml"
thr 1.32.1 THRESHOLDS="$work/th-parse.yaml"
eq "THRESHOLDS that does not parse: refused, exit 2, nothing printed, and its last line names the set" \
  "$thr_rc:$thr_out:$(tail -n 1 "$work/thr.err")" "2::thresholds: $work/th-parse.yaml does not parse"
mkdir -p "$work/pyyq"
cat > "$work/pyyq/yq" <<'PYYQ'
#!/usr/bin/env bash
[[ "${1-}" == --version ]] && { echo "yq 3.4.3"; exit 0; }
echo "a fake Python yq: no -o=json here" >&2; exit 2
PYYQ
chmod +x "$work/pyyq/yq"
thr 1.32.1 PATH="$work/pyyq:$PATH"
eq "the Python yq on PATH: refused, exit 2, nothing printed, no set blamed, and its last line names the yq laug needs" \
  "$thr_rc:$thr_out:$(grep -c 'does not parse' "$work/thr.err"):$(tail -n 1 "$work/thr.err")" \
  "2::0:thresholds: yq failed: laug needs mikefarah yq v4 (bin/laug deps)"
# bin/laug runs it with a laug on PATH, so the line to type starts with laug, the word laug sets in LAUG.
mkdir -p "$work/laugpath"; ln -sf "$root/bin/laug" "$work/laugpath/laug"
out="$(PATH="$work/pyyq:$work/laugpath:$PATH" bin/laug thresholds -server-tag 1.32.1 2>&1)"; rc=$?
eq "laug thresholds with the Python yq and a laug on PATH: exit 2, and it says which yq laug needs, by laug deps" \
  "$rc:$(grep -c 'does not parse' <<<"$out"):$(grep -cF 'thresholds: yq failed: laug needs mikefarah yq v4 (laug deps)' <<<"$out")" "2:0:1"
# Release files the resolver must refuse: an override it cannot apply as written must not pass as applied.
rel_file() { mkdir -p "$work/$1"; printf '%s\n' "$2" > "$work/$1/1.32.yaml"; }   # rel_file <dir> <yaml>: work/<dir>/1.32.yaml
rel_head=$'release: "1.32"\nbase: load/steady-state.yaml\nbase_server_tag: 1.31.0'
rel_file rel-name "$rel_head"$'\noverrides:\n  - {name: activity_s2s, no_data: not_judged, reason: r, evidence: e}'
rel_file rel-max "$rel_head"$'\noverrides:\n  - {name: activity_schedule_to_start_p99_ms, no_data: not_judged, max: 200, reason: r, evidence: e}'
rel_file rel-kind "$rel_head"$'\noverrides:\n  - {name: activity_schedule_to_start_p99_ms, no_data: ok, reason: r, evidence: e}'
rel_file rel-why "$rel_head"$'\noverrides:\n  - {name: activity_schedule_to_start_p99_ms, no_data: not_judged, reason: " ", evidence: e}'
rel_file rel-twice "$rel_head"$'\noverrides:\n  - {name: service_error_rate, no_data: not_judged, reason: r, evidence: e}\n  - {name: service_error_rate, no_data: not_judged, reason: r, evidence: e}'
rel_file rel-release $'release: "1.33"\nbase: load/steady-state.yaml\nbase_server_tag: 1.31.0\noverrides: []'
rel_file rel-base $'release: "1.32"\nbase: load/steady-state.yaml\nbase_server_tag: 1.30.0\noverrides: []'
rel_file rel-ok "$rel_head"$'\noverrides: []'
rel_file rel-basefile $'release: "1.32"\nbase: load/other.yaml\nbase_server_tag: 1.31.0\noverrides: []'
rel_file rel-noevidence "$rel_head"$'\noverrides:\n  - {name: activity_schedule_to_start_p99_ms, no_data: not_judged, reason: r}'
rel_file rel-list "$rel_head"$'\noverrides: {name: activity_schedule_to_start_p99_ms, no_data: not_judged, reason: r, evidence: e}'
rel_file rel-map "$rel_head"$'\noverrides:\n  - activity_schedule_to_start_p99_ms'
rel_file rel-nonobj $'- name: activity_schedule_to_start_p99_ms\n  no_data: not_judged'
rel_file rel-scalar 'not_judged'
rel_file rel-empty ''
rel_file rel-parse $'release: "1.32\nbase: ['
rel_file rel-host "$rel_head"$'\nhost: {type: x}\noverrides: []'
for c in rel-name:"override activity_s2s names no threshold of load/steady-state.yaml" \
         rel-max:"override activity_schedule_to_start_p99_ms: max is not a key this script applies" \
         rel-kind:'override activity_schedule_to_start_p99_ms: no_data is "ok", and the one kind is no_data: not_judged' \
         rel-why:"override activity_schedule_to_start_p99_ms has no reason" \
         rel-twice:'"service_error_rate" is overridden twice' \
         rel-release:'release is "1.33", not "1.32"' \
         rel-base:'written against base_server_tag "1.30.0", and load/steady-state.yaml is now "1.31.0"' \
         rel-basefile:'base is "load/other.yaml", not load/steady-state.yaml' \
         rel-noevidence:"override activity_schedule_to_start_p99_ms has no evidence path" \
         rel-list:"overrides is not a list" \
         rel-map:"an override is not a mapping" \
         rel-nonobj:"it holds no release, base and overrides" \
         rel-scalar:"it holds no release, base and overrides" \
         rel-empty:"it holds no release, base and overrides" \
         rel-parse:"thresholds: (outside the repository)/1.32.yaml does not parse" \
         rel-host:"host is not a key a release file holds; this script applies release, base, base_server_tag and overrides"; do
  thr 1.32.1 THRESHOLDS_RELEASES="$work/${c%%:*}"
  eq "release file ${c%%:*}: refused, exit 2, nothing printed" "$thr_rc:$thr_out" "2:"
  has "release file ${c%%:*}: says why" "$(cat "$work/thr.err")" "${c#*:}"
done
thr 1.32.1 THRESHOLDS_RELEASES="$work/rel-ok"
eq "a release file with no overrides applies, and changes nothing; outside the repository, it is named without its directory" \
  "$thr_rc:$(th_core <<<"$j"):$(jq -r '.resolved.release_file + " | " + .resolved.note' <<<"$j")" \
  "0:$base_core:(outside the repository)/1.32.yaml | load/steady-state.yaml, the base (1.31.0 baselines), with (outside the repository)/1.32.yaml for server 1.32.1: 0 overrides"
hasnt "a release file outside the repository: no local path in what the resolver prints" "$thr_out" "$work"
thr 1.33.0 THRESHOLDS_RELEASES="$work/rel-ok"
eq "no release file for 1.33 outside the repository: the note names no local path" "$(jq -r .resolved.note <<<"$j")" \
  "load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no (outside the repository)/1.33.yaml for server 1.33.0"
# Every committed release file: named <major>.<minor>.yaml, applies to a tag of its release, and each override points
# at evidence that is in the repository.
for f in load/releases/*.yaml; do
  mm="$(basename "$f" .yaml)"
  [[ "$mm" =~ ^[0-9]+\.[0-9]+$ ]] && pass "$f: named <major>.<minor>.yaml" || fail "$f: named <major>.<minor>.yaml"
  thr "$mm.0"; eq "$f: applies to server $mm.0" "$thr_rc:$(jq -r .resolved.release_file <<<"$j")" "0:$f"
  for e in $(yq -r '.overrides[].evidence' "$f"); do
    [[ -e "$e" ]] && pass "$f: evidence $e is in the repository" || fail "$f: evidence $e is in the repository"
  done
done
out="$(bin/laug thresholds -server-tag 1.32.1 2>&1)"
has "laug thresholds -server-tag 1.32.1: names the files" "$out" \
  "laug · thresholds: load/steady-state.yaml, the base (1.31.0 baselines), with load/releases/1.32.yaml for server 1.32.1: 2 overrides"
has "laug thresholds: the activity row is not judged on no data" "$out" "not judged: not produced on this release (load/releases/1.32.yaml)"
has "laug thresholds: the override's reason and evidence" "$out" "(evidence: cells/results/2026-10-23-boot1.txt)"
# A threshold with no max is read and recorded and judged by nothing: in the base, each is a share past a bucket edge,
# 1 minus a bucket over the +Inf bucket of the same series, named <reading>_over_<ms>ms for its bucket edge; laug
# thresholds says recorded, never EXCEEDS, for each. shares <file> prints [rows with no max, every one of that shape].
NOMAX_N="$(yq '[.thresholds[] | select(.max == null)] | length' load/steady-state.yaml)"
shares() {
  yq -o=json -I=0 '[.thresholds[] | select(.max == null) | {"name": .name, "query": .query}]' "$1" | jq -c 'map(
    (.name | capture("_over_(?<ms>[0-9]+)ms$").ms | tonumber) as $ms
    | .query | capture("^1 - sum\\(rate\\((?<a>.*)\\[1m\\]\\)\\) / sum\\(rate\\((?<b>.*)\\[1m\\]\\)\\)$")
    | (.b | test("le=\"\\+Inf\"")) and (.a | test("le=\"[0-9.]+\"")) and ((.a | sub("le=\"[0-9.]+\""; "LE")) == (.b | sub("le=\"\\+Inf\""; "LE")))
      and ((.a | capture("le=\"(?<le>[0-9.]+)\"").le | tonumber * 1000 | round) == $ms))
    | [length, all]' 2>&1
}
eq "the base's rows with no max: each a share, 1 - a bucket over the +Inf bucket of the same series, at its name's edge" \
  "$(shares load/steady-state.yaml)" "[$NOMAX_N,true]"
yq '(.thresholds[] | select(.name == "persistence_latency_over_10ms") | .query) |= sub("le=\"0.01\""; "le=\"0.02\"")' \
  load/steady-state.yaml > "$work/edge.yaml"
eq "... and a share named over_10ms that reads the 20 ms bucket fails that check" "$(shares "$work/edge.yaml")" "[$NOMAX_N,false]"
out="$(bin/laug thresholds -server-tag 1.31.0 2>&1)"
eq "laug thresholds: every row with no max reads recorded, not judged, and none EXCEEDS" \
  "$(grep -cE '^[a-z0-9_]+ +null +recorded, not judged \(no max\)$' <<<"$out"):$(grep -cE ' null +EXCEEDS$' <<<"$out")" "$NOMAX_N:0"
# A set names its host (docs/DESIGN.md section 7, the R6 paragraph) and the readings the incumbent arm compares, each
# with a scale, a noise floor and a tolerance (section 7.1). The tolerances are not checked here: A/A gates set them.
thr 1.31.0
eq "1.31.0: the set's host, and resolved.host, are the lab host's type and size" "$(jq -c '[.host, .resolved.host]' <<<"$j")" \
  '[{"type":"m8i.8xlarge","vcpu":32,"mem_gib":123},{"type":"m8i.8xlarge","vcpu":32,"mem_gib":123}]'
eq "the tree's set: compare: release 1.31.0, five rows, each with its scale and noise floor" \
  "$(jq -c '[.compare.release, [.compare.rows[] | [.name, .scale, .floor]]]' <<<"$j")" \
  '["1.31.0",[["workflow_task_schedule_to_start_p99_ms","ms",0.1],["persistence_latency_p99_ms","ms",0.1],["activity_schedule_to_start_over_10ms","share",0.0005],["frontend_request_over_10ms","share",0.0005],["workflow_end_to_end_over_50ms","share",0.0005]]]'
# hcase <name> <yq edit> <text>...: a copy of load/steady-state.yaml with one edit, given as THRESHOLDS: refused, exit 2,
# nothing printed, and each text in what it says.
hcase() {
  local n="$1" e="$2" t miss=""; shift 2
  yq "$e" load/steady-state.yaml > "$work/hc-$n.yaml"
  thr 1.31.0 THRESHOLDS="$work/hc-$n.yaml"
  for t in "$@"; do grep -qF -- "$t" "$work/thr.err" || miss+="$t|"; done
  eq "THRESHOLDS $n: refused, exit 2, nothing printed, and says why" "$thr_rc:$thr_out:${miss:-said}" "2::said"
}
hcase no-host 'del(.host)' \
  "cannot be used as a threshold set: it names no host {type, vcpu, mem_gib}, the host its numbers were measured on (docs/DESIGN.md section 7, the R6 paragraph)"
hcase host-type '.host.type = "M8I"' 'host type "M8I" is not a type name: lowercase letters, digits, . and -, 32 at most'
hcase host-vcpu '.host.vcpu = 32.5' "host vcpu is 32.5, not a whole number from 1"
hcase under-floor '.compare.rows[0].tolerance = 0.05' \
  "compare row workflow_task_schedule_to_start_p99_ms: tolerance 0.05 is under its floor 0.1"
hcase quoted-tolerance '.compare.rows[0].tolerance = "0.3"' \
  'compare row workflow_task_schedule_to_start_p99_ms: tolerance is "0.3", neither null nor a number'
hcase no-wave '.compare.rows[0].tolerance = 0.3 | .compare.wave = null | .compare.gates = null | .compare.evidence = null' \
  "compare: a row has a tolerance, and wave is null: a tolerance names the wave it was measured on" \
  "compare: a row has a tolerance, and gates is null: a tolerance names how many A/A gates set it" \
  "compare: a row has a tolerance, and evidence is null: a tolerance names its results file in cells/results/ or docs/tables/gates.jsonl"
hcase foo-row '.compare.rows += [{"name": "foo_ms", "scale": "ms", "floor": 0.1, "tolerance": null}]' \
  "compare row foo_ms names no threshold of the set"
hcase share-as-ms '(.compare.rows[] | select(.name == "activity_schedule_to_start_over_10ms") | .scale) = "ms"' \
  "compare row activity_schedule_to_start_over_10ms: scale ms is for a reading in ms, named <reading>_ms"
yq '.compare.rows[0].tolerance = 0.3 | .compare.wave = ["steady", "pl", "incumbent"] | .compare.gates = 50
    | .compare.evidence = "cells/results/2026-10-17-arm-pilot.txt"' load/steady-state.yaml > "$work/hc-judging.yaml"
thr 1.31.0 THRESHOLDS="$work/hc-judging.yaml"
eq "THRESHOLDS with a tolerance, its wave, gates and evidence: accepted" "$thr_rc:$(jq -c '.compare.rows[0].tolerance' <<<"$j")" "0:0.3"
yq '.compare.evidence = "docs/tables/gates.jsonl"' "$work/hc-judging.yaml" > "$work/hc-tables.yaml"
thr 1.31.0 THRESHOLDS="$work/hc-tables.yaml"
eq "THRESHOLDS with evidence docs/tables/gates.jsonl, as laug gate tolerance -tables prints it: accepted" \
  "$thr_rc:$(jq -c '.compare.rows[0].tolerance' <<<"$j")" "0:0.3"
hcase other-evidence '.compare.rows[0].tolerance = 0.3 | .compare.wave = ["steady"] | .compare.gates = 20
    | .compare.evidence = "docs/tables/gate-items.jsonl"' \
  'compare: a row has a tolerance, and evidence is "docs/tables/gate-items.jsonl": a tolerance names its results file in cells/results/ or docs/tables/gates.jsonl'
out="$(bin/laug thresholds -server-tag 1.31.0 2>&1)"
if [[ "$(yq '[.compare.rows[] | select(.tolerance != null)] | length' load/steady-state.yaml)" == 0 ]]; then
  cmode="compare           recording: no tolerance yet; R7 records and judges nothing"
else cmode="compare           judging: tolerances from $(yq '.compare.gates' load/steady-state.yaml) A/A gates of"; fi
eq "laug thresholds -server-tag 1.31.0: the host line, the compare line of the set's mode, and a line per compare row" \
  "$(grep -cxF 'host              m8i.8xlarge, 32 vCPU, 123 GiB, the host its numbers were measured on' <<<"$out"):$(grep -cF "$cmode" <<<"$out"):$(grep -E '^  [a-z0-9_]+  ' <<<"$out" | awk '{ print $1 }' | paste -sd ' ')" \
  "1:1:$(yq -r '.compare.rows[].name' load/steady-state.yaml | paste -sd ' ')"

section "sweep.sh"
fresh
sw() { env PATH="$FAKE_PATH" "$@" load/sweep.sh 127.0.0.1:17233 omes-ht 2026-10-13T14:02:50Z 2>/dev/null; }
eq "status to count" "$(sw FAKE_SWEEP="$td/sweep/failed.json")" '{"Completed":3399,"Failed":1}'
has "query: task queue, start time, grouped by status" "$(cat "$FAKE_DIR/temporal.args")" \
  "TaskQueue='omes-ht' AND StartTime < '2026-10-13T14:02:50Z' GROUP BY ExecutionStatus"
has "query goes to the address given" "$(cat "$FAKE_DIR/temporal.args")" "127.0.0.1:17233"
eq "nothing matched" "$(sw FAKE_SWEEP="$td/sweep/empty.json")" '{}'
fresh
eq "Running read again, then Completed" "$(sw FAKE_SWEEP="$td/sweep/running.json:$td/sweep/ok.json"):$(calls)" '{"Completed":3400}:2'
fresh
eq "Running read again twice, then reported" "$(sw FAKE_SWEEP="$td/sweep/running.json"):$(calls)" '{"Completed":3390,"Running":10}:3'
sw FAKE_SWEEP="$td/sweep/ok.json" FAKE_TEMPORAL_EXIT=1 >/dev/null; eq "a failed count exits nonzero" "$?" 1
env PATH="$FAKE_PATH" load/sweep.sh 127.0.0.1:17233 "omes-x' OR 1=1" 2026-10-13T14:02:50Z 2>/dev/null; eq "a task queue that could close the quote is refused" "$?" 2
env PATH="$FAKE_PATH" load/sweep.sh 127.0.0.1:17233 omes-x "yesterday" 2>/dev/null; eq "a cutoff that is not RFC 3339 is refused" "$?" 2

section "run-omes.sh"
ro() { # ro <run-id> <omes-log> <omes-exit> [env...]
  local id="$1" log="$2" ex="$3"; shift 3; fresh
  env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_OMES_LOG="$log" FAKE_OMES_EXIT="$ex" OMES_DIR="$FAKE_DIR/omes" \
    RESULTS_DIR="$FAKE_DIR/results" LEASE_FILE="$FAKE_DIR/no-lease.json" RUN_ID="$id" CELL_ADDRESS=127.0.0.1:17233 "$@" \
    load/run-omes.sh workflow_with_single_noop_activity >/dev/null 2>&1
}
ro ht-fail "$td/omes-fail.txt" 1 DURATION=5m RATE=20 MAX_CONCURRENT=50 CONTINUE_ON_FAILURE=1; rc=$?
eq "Omes exits 1: run-omes.sh exits 1" "$rc" 1
j="$FAKE_DIR/results/ht-fail.json"
if [[ -f "$j" ]]; then
  pass "Omes exits 1: the run JSON is written"
  eq "run JSON: exit, failed iterations, tally, task queue, timeout" \
    "$(jq -c '{omes_exit, iterations_failed, omes_tally, task_queue, timeout}' "$j")" \
    '{"omes_exit":1,"iterations_failed":3,"omes_tally":{"succeeded":5997,"failed":3},"task_queue":"omes-ht-fail","timeout":"420s"}'
  cp "$j" "$work/ht-fail.json"
else fail "Omes exits 1: the run JSON is written"; fi
has "Omes gets --timeout DURATION + 120 s" "$(cat "$FAKE_DIR/go.args")" $'--timeout\n420s'
eq "run JSON: no SERVER_TAG given, server_tag null; server_version is what the cell says" \
  "$(jq -c '{server_tag, server_version}' "$FAKE_DIR/results/ht-fail.json" 2>&1)" '{"server_tag":null,"server_version":"1.31.0"}'
has "the version is asked of the cell under load" "$(cat "$FAKE_DIR/temporal.args" 2>&1)" $'operator\ncluster\nsystem\n--address\n127.0.0.1:17233'
ro ht-tag "$td/omes-ok.txt" 0 DURATION=5m RATE=20 SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1
eq "run JSON: SERVER_TAG 1.32.1 on a cell that says 1.32.1" "$(jq -c '{server_tag, server_version}' "$FAKE_DIR/results/ht-tag.json" 2>&1)" \
  '{"server_tag":"1.32.1","server_version":"1.32.1"}'
ro ht-novers "$td/omes-ok.txt" 0 DURATION=5m RATE=20 SERVER_TAG=1.31.0 FAKE_TEMPORAL_EXIT=1
eq "run JSON: a cell that does not answer the version: server_version null, the tag kept" \
  "$(jq -c '{server_tag, server_version, omes_exit}' "$FAKE_DIR/results/ht-novers.json" 2>&1)" '{"server_tag":"1.31.0","server_version":null,"omes_exit":0}'
fresh
out="$(env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_OMES_LOG="$td/omes-ok.txt" FAKE_OMES_EXIT=0 OMES_DIR="$FAKE_DIR/omes" RESULTS_DIR="$FAKE_DIR/results" \
  LEASE_FILE="$FAKE_DIR/no-lease.json" RUN_ID=ht-mismatch CELL_ADDRESS=127.0.0.1:17233 SERVER_TAG=1.31.0 FAKE_SERVER_VERSION=1.32.1 \
  load/run-omes.sh workflow_with_single_noop_activity 2>&1 >/dev/null)"
has "SERVER_TAG and the cell disagree: says so" "$out" "the cell at 127.0.0.1:17233 says it runs 1.32.1, SERVER_TAG says 1.31.0; the run JSON records both"
eq "SERVER_TAG and the cell disagree: both recorded" "$(jq -c '[.server_tag, .server_version]' "$FAKE_DIR/results/ht-mismatch.json" 2>&1)" '["1.31.0","1.32.1"]'
ro ht-ok "$td/omes-ok.txt" 0 DURATION=5m RATE=20; rc=$?
eq "Omes exits 0: run-omes.sh exits 0" "$rc" 0
eq "clean run JSON" "$(jq -c '{omes_exit, iterations_failed, omes_tally}' "$FAKE_DIR/results/ht-ok.json" 2>&1)" \
  '{"omes_exit":0,"iterations_failed":0,"omes_tally":null}'
# The backend and nesting keys follow server_tag: from the lease when it names this cell (a microVM lease here, with its
# snapshot values), null when no lease does; host_virtualized is what /proc/cpuinfo says of this host, a boolean either way.
eq "run JSON: no lease names the cell: backend and the snapshot values null, host_virtualized a boolean" \
  "$(jq -c '[.backend, .firecracker_version, .cache_type, (.host_virtualized | type), keys_unsorted[5:10]]' "$FAKE_DIR/results/ht-ok.json" 2>&1)" \
  '[null,null,null,"boolean",["server_tag","backend","firecracker_version","cache_type","host_virtualized"]]'
fresh
jq -n '{id: "abcdef12", address: "127.0.0.1:17233", ssh: "root@10.254.3.2", backend: "firecracker", firecracker_version: "v1.17.0", cache_type: "Unsafe"}' > "$FAKE_DIR/lease.json"
env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_OMES_LOG="$td/omes-ok.txt" FAKE_OMES_EXIT=0 OMES_DIR="$FAKE_DIR/omes" RESULTS_DIR="$FAKE_DIR/results" \
  LEASE_FILE="$FAKE_DIR/lease.json" RUN_ID=ht-backend CELL_ADDRESS=127.0.0.1:17233 ITERATIONS=6000 load/run-omes.sh workflow_with_single_noop_activity >/dev/null 2>&1
eq "run JSON: the lease names this cell, a microVM: its backend, firecracker_version and cache_type" \
  "$(jq -c '[.backend, .firecracker_version, .cache_type]' "$FAKE_DIR/results/ht-backend.json" 2>&1)" '["firecracker","v1.17.0","Unsafe"]'
jq -c 'del(.backend, .firecracker_version, .cache_type)' "$FAKE_DIR/lease.json" > "$FAKE_DIR/lease-old.json"
env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_OMES_LOG="$td/omes-ok.txt" FAKE_OMES_EXIT=0 OMES_DIR="$FAKE_DIR/omes" RESULTS_DIR="$FAKE_DIR/results" \
  LEASE_FILE="$FAKE_DIR/lease-old.json" RUN_ID=ht-backend-old CELL_ADDRESS=127.0.0.1:17233 ITERATIONS=6000 load/run-omes.sh workflow_with_single_noop_activity >/dev/null 2>&1
eq "run JSON: a lease with no backend key that names a guest: backend firecracker (the fallback), no snapshot values" \
  "$(jq -c '[.backend, .firecracker_version, .cache_type]' "$FAKE_DIR/results/ht-backend-old.json" 2>&1)" '["firecracker",null,null]'
# The pool writes backend "" for a backend whose create prints no backend key (Cell.Backend has no omitempty): the same
# fallback as for a missing key, as faults/lib.sh's lease_read reads it.
jq -c '.backend = "" | del(.firecracker_version, .cache_type)' "$FAKE_DIR/lease.json" > "$FAKE_DIR/lease-empty.json"
env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_OMES_LOG="$td/omes-ok.txt" FAKE_OMES_EXIT=0 OMES_DIR="$FAKE_DIR/omes" RESULTS_DIR="$FAKE_DIR/results" \
  LEASE_FILE="$FAKE_DIR/lease-empty.json" RUN_ID=ht-backend-empty CELL_ADDRESS=127.0.0.1:17233 ITERATIONS=6000 load/run-omes.sh workflow_with_single_noop_activity >/dev/null 2>&1
eq "run JSON: a lease with backend \"\" that names a guest: backend firecracker (the fallback), no snapshot values" \
  "$(jq -c '[.backend, .firecracker_version, .cache_type]' "$FAKE_DIR/results/ht-backend-empty.json" 2>&1)" '["firecracker",null,null]'
# The last line names the run JSON by RESULTS_DIR as given, as laug load gives load/results from the root: never by
# the absolute path the script writes to, which holds a home path on most hosts.
fresh
out="$(cd "$FAKE_DIR" && env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_OMES_LOG="$td/omes-ok.txt" FAKE_OMES_EXIT=0 \
  OMES_DIR="$FAKE_DIR/omes" RESULTS_DIR=results/ LEASE_FILE="$FAKE_DIR/no-lease.json" RUN_ID=ht-rel \
  CELL_ADDRESS=127.0.0.1:17233 DURATION=5m RATE=20 "$root/load/run-omes.sh" workflow_with_single_noop_activity 2>&1 >/dev/null)"
last="$(tail -1 <<<"$out")"
has "a relative RESULTS_DIR: the last line names the run JSON from it" "$last" " recorded in results/ht-rel.json; Omes exit 0, "
hasnt "a relative RESULTS_DIR: the last line holds no absolute path" "$last" "$FAKE_DIR"
eq "a relative RESULTS_DIR: the run JSON is written there" "$(jq -r .run_id "$FAKE_DIR/results/ht-rel.json" 2>&1)" ht-rel
ro ht-abort "$td/omes-abort.txt" 1 ITERATIONS=6000
eq "stopped at the first failure: 2 failed, no tally, no timeout without DURATION" \
  "$(jq -c '{omes_exit, iterations_failed, omes_tally, timeout}' "$FAKE_DIR/results/ht-abort.json" 2>&1)" \
  '{"omes_exit":1,"iterations_failed":2,"omes_tally":null,"timeout":""}'
hasnt "no --timeout with ITERATIONS" "$(cat "$FAKE_DIR/go.args")" "--timeout"
# A TERM or an INT to run-omes.sh alone, as laug load passes one on, mid-load. The stand-in Omes is a go run with a
# child, both in a group of their own, and the child's worker is in a group of its own, as Omes's is. The script sends
# INT to Omes's group: the child stops its worker, removes its build dir and exits 1, and go run exits 1 with it. No
# run JSON is written for the load cut short, and the script exits 128 plus the signal's number. The INT case starts
# the script with INT at its default, as a terminal's laug does: bash starts a background job with INT ignored, and a
# script cannot trap that. The others start it with INT ignored, as faults/run.sh starts a raun's load. In the stuck
# case the child ignores INT: its group gets TERM INT_GRACE_S later, and the worker, left in Omes's session, gets TERM
# once go run has gone. In the build case the INT comes during go run's own build, before go run catches INT: it ends
# go run, since the script starts Omes with INT at its default. The group case sends TERM to Omes's group alone, as
# when Omes dies of something else: the worker goes all the same, and the run JSON says 143. Under a PID 1 that does
# not reap orphans, as in a container without an init, Omes's orphans stay in its session as zombies. run-omes.sh
# counts them, so a check here can fail though the script is right. Under a PID 1 that waited for its own child alone,
# the stuck case said "Omes left 2 process(es)", or 3, where a reaping PID 1 gives 1 (seen). A note after these checks
# says so when one fails.
rostop() { # rostop <TERM|INT|stuck|build|group>: run-omes.sh in the background, then the signal; its exit in rc
  fresh
  local p i stuck=0 build=0 ready="$FAKE_DIR/omes.ready" int=--ignore-signal=INT
  [[ "$1" != stuck ]] || stuck=1; [[ "$1" != build ]] || { build=1; ready="$FAKE_DIR/omes.building"; }
  [[ "$1" != INT ]] || int=--default-signal=INT
  env "$int" PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_OMES_LOG="$td/omes-ok.txt" FAKE_OMES_CHILD=1 FAKE_OMES_EXIT=0 \
    FAKE_OMES_STUCK=$stuck FAKE_OMES_BUILD=$build INT_GRACE_S=1 OMES_DIR="$FAKE_DIR/omes" RESULTS_DIR="$FAKE_DIR/results" \
    LEASE_FILE="$FAKE_DIR/no-lease.json" RUN_ID="ht-$1" CELL_ADDRESS=127.0.0.1:17233 DURATION=5m \
    load/run-omes.sh workflow_with_single_noop_activity > /dev/null 2> "$FAKE_DIR/stop.err" &
  p=$!
  for i in $(seq 1 100); do [[ -f "$ready" ]] && break; "$REAL_SLEEP" 0.05; done
  ogo=""; ogroup=""; ochild=""; oworker=""
  [[ "$1" == build ]] || read -r ogo ogroup ochild oworker < "$FAKE_DIR/omes.pids"
  case "$1" in group) kill -TERM -- "-$ogroup" ;; stuck|build) kill -TERM "$p" ;; *) kill -s "$1" "$p" ;; esac
  for i in $(seq 1 100); do kill -0 "$p" 2>/dev/null || break; "$REAL_SLEEP" 0.05; done
  kill -0 "$p" 2>/dev/null && kill -KILL "$p"
  wait "$p"; rc=$?
  # kill -0 serves for $p, this shell's child, which bash reaps as it ends. Omes's processes are not: gone reads them.
  oup=""; for i in $ogo $ochild $oworker; do gone "$i" || { oup+="$i "; kill -KILL "$i"; }; done
  otemp="$(find "$FAKE_DIR/omes/workers/go" -maxdepth 1 -name 'omes-temp-*' 2>/dev/null | wc -l)"
}
ostopped() { grep -cx "stopped by $1: Omes got $2 and exited $3; no run JSON for a load cut short; exit $4" "$FAKE_DIR/stop.err"; }
rofailed="$failed"
for sig in TERM INT; do
  rostop "$sig"; code=$(( 128 + $(kill -l "$sig") ))
  eq "$sig to run-omes.sh alone: exit $code; Omes got INT, its child, worker and build dir gone; no run JSON" \
    "$rc|${oup:-none up}|$([[ "$ogroup" == "$ogo" ]] && echo own)|$otemp|$(ls "$FAKE_DIR/results" | paste -sd ' ')|$(ostopped "$sig" INT 1 "$code")" \
    "$code|none up|own|0|ht-$sig.log|1"
  hasnt "$sig to run-omes.sh alone: Omes stopped its worker itself, so nothing was left in its session" "$(cat "$FAKE_DIR/stop.err")" "Omes left"
done
rostop stuck
eq "TERM to run-omes.sh, an Omes that ignores INT: its group gets TERM INT_GRACE_S later, its worker after; exit 143" \
  "$rc|${oup:-none up}|$(ostopped TERM 'INT, then TERM 1 s on,' 143 143)" "143|none up|1"
has "an Omes that ignores INT: says the worker left in its session got TERM" "$(cat "$FAKE_DIR/stop.err")" \
  "Omes left 1 process(es) in its session, its worker among them; each got TERM"
rostop build
eq "TERM to run-omes.sh during go run's build: the INT ends go run, exit 130, before its INT_GRACE_S; exit 143" \
  "$rc|$(ostopped TERM INT 130 143)|$([[ -f "$FAKE_DIR/omes.pids" ]] && echo "omes ran" || echo "no omes")" "143|1|no omes"
rostop group
eq "TERM to Omes's group alone: run-omes.sh ends, exit 143; the worker gone; the run JSON says Omes exited 143" \
  "$rc|${oup:-none up}|$(jq -r .omes_exit "$FAKE_DIR/results/ht-group.json" 2>&1)" "143|none up|143"
(( failed == rofailed )) || note "a stop check of load/run-omes.sh failed: make harness-test wants a PID 1 that reaps orphans. In a container without an init, an orphan that a check stops can stay a zombie, which still reads as running"

section "verdict.sh"
# base1-100551 is the dev host's first baseline run, kept here as a fixture since the dev host's results leave
# load/results: its run JSON predates server_tag, timeout, the task queue and the tally.
base1="$td/base1-100551.json"
b1_start="$("$REAL_DATE" -u -d "$(jq -r .start "$base1")" +%s)"
vd() { # vd <run-json> [env...]: sets out to the verdict's output, then rc=<exit>
  local meta="$1"; shift; fresh; local id; id="$(jq -r .run_id "$meta")"
  cp "$meta" "$FAKE_DIR/results/$id.json"
  out="$(env PATH="$FAKE_PATH" FAKE_T0="$b1_start" FAKE_SWEEP="$td/sweep/ok.json" PROM_URL=http://127.0.0.1:19090 \
    RESULTS_DIR="$FAKE_DIR/results" "$@" load/verdict.sh "$id" 2>&1)"
  local rc=$?; out+=$'\n'"rc=$rc"
}
vd "$base1"
has "old run JSON (base1-100551) still ADVANCEs" "$out" "VERDICT: ADVANCE"
has "old run JSON: exit 0" "$out" "rc=0"
eq "old run JSON: three lost-work rows say not recorded" "$(grep -c 'not recorded' <<<"$out")" 3
has "old run JSON: no server tag, and the verdict says what that means" "$out" "server tag absent from the run JSON: every cell before 2026-10-14 ran 1.31.0"
has "old run JSON: the floor still applies, 6440 against 5700" "$out" "ok (0.95 x 20/s x 300 s)"
[[ -e "$FAKE_DIR/temporal.args" ]] && fail "old run JSON: no sweep is attempted" || pass "old run JSON: no sweep is attempted"
vd "$base1" FAKE_WF_RATE=17
has "old run JSON under the floor BLOCKs: 17/s x 322 s = 5474" "$out" "BELOW (0.95 x 20/s x 300 s)"
has "old run JSON under the floor: exit 1" "$out" "rc=1"
jq '. + {timeout:"420s", task_queue:"omes-base1-100551", omes_exit:0, iterations_failed:0, omes_tally:null}' "$base1" > "$work/new.json"
vd "$work/new.json"
has "new run JSON, all clean: ADVANCE" "$out" "VERDICT: ADVANCE"
jq '. + {server_tag:"1.32.1", server_version:"1.32.1"}' "$work/new.json" > "$work/tag.json"
vd "$work/tag.json"
has "a 1.32.1 run: the verdict names its server" "$out" "server 1.32.1, the cell said 1.32.1"
has "a 1.32.1 run: and that the thresholds are 1.31.0's" "$out" "note: the thresholds come from 1.31.0 baselines (load/steady-state.yaml server_tag); this run is on 1.32.1"
jq '. + {server_tag:"1.31.0", server_version:"1.31.0"}' "$work/new.json" > "$work/tag.json"
vd "$work/tag.json"
hasnt "a 1.31.0 run: no note about the thresholds" "$out" "note: the thresholds come from"
# A make load to a local cell used to record the Makefile default as server_tag whatever the cell ran.
jq '. + {server_tag:"1.31.0", server_version:"1.32.1"}' "$work/new.json" > "$work/tag.json"
vd "$work/tag.json"
has "tag 1.31.0, the cell said 1.32.1: the cell's word is the release" "$out" \
  "note: server_tag 1.31.0 and the version the cell reported, 1.32.1, disagree; the run is taken as 1.32.1"
has "tag 1.31.0, the cell said 1.32.1: the thresholds note still prints" "$out" "note: the thresholds come from 1.31.0 baselines (load/steady-state.yaml server_tag); this run is on 1.32.1"
jq '. + {server_version:"1.32.1"} | del(.server_tag)' "$work/new.json" > "$work/tag.json"
vd "$work/tag.json"
has "no tag, the cell said 1.32.1: the thresholds note prints" "$out" "this run is on 1.32.1"
has "new run JSON: sweep counts workflows started 30 s before the end" "$(cat "$FAKE_DIR/temporal.args")" \
  "TaskQueue='omes-base1-100551' AND StartTime < '2026-10-09T14:10:43Z' GROUP BY ExecutionStatus"
has "new run JSON: sweep reads the cell the run recorded" "$(cat "$FAKE_DIR/temporal.args")" "127.0.0.1:7243"
jq -s '.[1] + (.[0] | {omes_exit, iterations_failed, omes_tally, task_queue, timeout})' "$work/ht-fail.json" "$base1" > "$work/omes1.json"
vd "$work/omes1.json"
has "Omes exited 1: BLOCK" "$out" "VERDICT: BLOCK"
has "Omes exited 1: exit 1, not 2" "$out" "rc=1"
has "Omes exited 1: the row says so" "$out" "EXCEEDS (Omes failed"
has "Omes exited 1: its tally is shown" "$out" "Omes counted 5997 succeeded, 3 failed"
vd "$work/new.json" FAKE_SWEEP="$td/sweep/failed.json"
has "sweep finds a Failed workflow: BLOCK" "$out" "EXCEEDS (Failed=1 of 3400 started before 2026-10-09T14:10:43Z)"
has "sweep finds a Failed workflow: exit 1" "$out" "rc=1"
vd "$work/new.json" FAKE_SWEEP="$td/sweep/running.json"
has "sweep finds Running after two more reads: BLOCK" "$out" "EXCEEDS (Running=10 of 3400"
vd "$work/new.json" FAKE_SWEEP="$td/sweep/empty.json"
has "sweep finds nothing: BLOCK" "$out" "EXCEEDS (no data: the sweep found no workflow"
vd "$work/new.json" FAKE_TEMPORAL_EXIT=1
has "sweep cannot reach the cell: BLOCK" "$out" "EXCEEDS (no data: the sweep of omes-base1-100551 on 127.0.0.1:7243 failed)"
vd "$work/new.json" FAKE_SWEEP="$td/sweep/continued.json"
has "ContinuedAsNew is unexpected by default: BLOCK" "$out" "VERDICT: BLOCK"
vd "$work/new.json" FAKE_SWEEP="$td/sweep/continued.json" SWEEP_EXPECTED=Completed,ContinuedAsNew
has "ContinuedAsNew allowed by SWEEP_EXPECTED: ADVANCE" "$out" "VERDICT: ADVANCE"
vd "$work/new.json" FAKE_RESETS=1 FAKE_INCREASE=6400.4
has "a counter reset: increase() counts, and the row says so" "$out" "increase() over 1 counter reset(s)"
has "a counter reset: 6400 completions ADVANCE" "$out" "VERDICT: ADVANCE"
# A run whose Omes stopped on its first failed iteration ends before DURATION, so its window is under the 5 m
# steady-state.yaml trusts and read-thresholds.sh exits 3. Its lost work is still judged. sweep/terminated.json is
# the shape of a dev-host run of 2026-10-14: worker stopped 60 s, 5 Running workflows terminated, Omes exit 1.
b1_end() { "$REAL_DATE" -u -d "@$(( b1_start + $1 ))" +%Y-%m-%dT%H:%M:%SZ; }
jq --arg e "$(b1_end 150)" '.end = $e' "$work/omes1.json" > "$work/short1.json"
vd "$work/short1.json" FAKE_SWEEP="$td/sweep/terminated.json"
has "150 s window, Omes exited 1: BLOCK" "$out" "VERDICT: BLOCK"
has "150 s window, Omes exited 1: exit 1, not 3" "$out" "rc=1"
has "150 s window: thresholds shown, not judged" "$out" "not judged (window under 5m)"
has "150 s window: completions read over the window, 3000 under 5700" "$out" "BELOW (0.95 x 20/s x 300 s)"
has "150 s window: the sweep names the terminated workflows" "$out" "EXCEEDS (Running=5 Terminated=5 of 1719 started before"
jq --arg e "$(b1_end 20)" '.end = $e' "$work/omes1.json" > "$work/short20.json"
vd "$work/short20.json"
has "20 s window, Omes exited 1: BLOCK" "$out" "VERDICT: BLOCK"
has "20 s window, Omes exited 1: exit 1" "$out" "rc=1"
has "20 s window: completions not read" "$out" "not read (window under 30 s)"
jq --arg e "$(b1_end 150)" '.end = $e | .duration = "2m"' "$work/new.json" > "$work/short0.json"
vd "$work/short0.json"
has "150 s window, nothing lost: no verdict, exit 3" "$out" "rc=3"
hasnt "150 s window, nothing lost: no ADVANCE" "$out" "VERDICT: ADVANCE"
has "150 s window, nothing lost: says why" "$out" "no verdict: the window is 150 s"
# VERDICT_JSON: the same verdict as JSON, for faults/candidate.sh. The printed verdict must not change with it.
vd "$work/new.json"; plain="$out"
# A load's verdict, plain, a gate item's or a candidate's, heads a run: a raun is the whole trial, with its fault
# (README.md, The words).
eq "the verdict's heading: verdict for run <run id>, its window" "$(grep '^laug · verdict for ' <<<"$plain")" \
  "laug · verdict for run $(jq -r .run_id "$work/new.json") (322 s window)"
vd "$work/new.json" VERDICT_JSON="$work/vj.json"
eq "VERDICT_JSON: the printed verdict is the same with it" "$out" "$plain"
TH_N="$(yq '.thresholds | length' load/steady-state.yaml)"
eq "VERDICT_JSON, ADVANCE: verdict, exit, window, nothing deciding, the sweep and its cutoff, a row a threshold and 4 lost-work rows" \
  "$(jq -c '[.verdict, .exit, .window_s, .deciding, .sweep, .sweep_cutoff, (.rows | length)]' "$work/vj.json" 2>&1)" \
  '["ADVANCE",0,322,[],{"Completed":3400},"2026-10-09T14:10:43Z",'"$(( TH_N + 4 ))"']'
eq "VERDICT_JSON: each threshold with no max is read and recorded (0 here), its row no threshold yet, blocking nothing" \
  "$(yq -o=json -I=0 '[.thresholds[] | select(.max == null) | .name]' load/steady-state.yaml | jq -c --slurpfile v "$work/vj.json" '
    . as $n | [($n | length), ([$n[] | $v[0].readings[.]] | unique), ([$v[0].rows[] | select(.metric as $m | $n | index($m)) | [.limit, .result, .blocks]] | unique)]' 2>&1)" \
  "[$NOMAX_N,[0],[[\"null\",\"no threshold yet\",false]]]"
eq "VERDICT_JSON: a row as printed, with its value a number" \
  "$(jq -c '.rows[] | select(.metric == "workflows_completed")' "$work/vj.json" 2>&1)" \
  '{"metric":"workflows_completed","value":6440,"limit":">= 5700","result":"ok (0.95 x 20/s x 300 s)","blocks":false}'
eq "VERDICT_JSON: the readings" "$(jq -c '.readings | {workflows_completed, window_seconds}' "$work/vj.json" 2>&1)" '{"workflows_completed":6440,"window_seconds":322}'
vd "$work/new.json" VERDICT_JSON="$work/vj.json" FAKE_THRESHOLD_ONLY=persistence_latency FAKE_THRESHOLD_VALUE=80
eq "VERDICT_JSON, one threshold over: BLOCK, it alone decides" "$(jq -c '[.verdict, .exit, .deciding]' "$work/vj.json" 2>&1)" '["BLOCK",1,["persistence_latency_p99_ms"]]'
eq "... its share past 10 ms, which has no max, reads the same 80 and decides nothing" \
  "$(jq -c '[.readings.persistence_latency_over_10ms == 80, (.rows[] | select(.metric == "persistence_latency_over_10ms") | [.value == 80, .result, .blocks])]' "$work/vj.json" 2>&1)" \
  '[true,[true,"no threshold yet",false]]'
vd "$work/new.json" VERDICT_JSON="$work/vj.json" FAKE_THRESHOLD_ONLY=persistence_latency FAKE_THRESHOLD_VALUE=0.0004
eq "a share of 0.0004 prints and records to 5 places, the p99 beside it, which has a max, to 3 (row values in 0.00001)" \
  "$(grep -E '^persistence_latency_(p99_ms|over_10ms) ' <<<"$out" | awk '{ print $1, $2 }' | paste -sd '|'):$(jq -c '[.rows[] | select(.metric | startswith("persistence_latency")) | .value * 100000 | round]' "$work/vj.json" 2>&1)" \
  "persistence_latency_p99_ms 0.000|persistence_latency_over_10ms 0.00040:[0,40]"
vd "$work/short1.json" VERDICT_JSON="$work/vj.json" FAKE_SWEEP="$td/sweep/terminated.json"
eq "VERDICT_JSON, 150 s window, Omes exited 1: the lost-work rows decide, the thresholds were not judged" \
  "$(jq -c '[.verdict, .exit, .short_window, .deciding]' "$work/vj.json" 2>&1)" '["BLOCK",1,"5m",["workflows_completed","omes_exit","workflows_not_completed"]]'
vd "$work/short0.json" VERDICT_JSON="$work/vj.json"
eq "VERDICT_JSON, no verdict (exit 3): verdict null" "$(jq -c '[.verdict, .exit, .deciding]' "$work/vj.json" 2>&1)" '[null,3,[]]'
rm -f "$work/vj.json"; env PATH="$FAKE_PATH" RESULTS_DIR="$FAKE_DIR/results" VERDICT_JSON="$work/vj.json" load/verdict.sh no-such-run >/dev/null 2>&1; rc=$?
eq "VERDICT_JSON, no run metadata: exit 2, nothing written" "$rc:$([[ -e "$work/vj.json" ]] && echo written || echo none)" "2:none"
# Threshold sets per server release. FAKE_THRESHOLD_EMPTY gives the activity schedule-to-start query no series, as
# 1.32.1 cells under load gave (lab1-v1321-1 to -3, cells/results/2026-10-23-boot1.txt); every other reading is 0.
NO_ACT='task_type="Activity"'
# The maxima these checks expect are read from the base file, not written here: the bar is the checkout's, and a new
# threshold set (the lab host's replaced the dev host's) must not fail them for that alone.
ACT_MAX="$(yq '.thresholds[] | select(.name == "activity_schedule_to_start_p99_ms") | .max' load/steady-state.yaml)"
PERS_MAX="$(yq '.thresholds[] | select(.name == "persistence_latency_p99_ms") | .max' load/steady-state.yaml)"
act_row() { grep -E '^activity_schedule_to_start_p99_ms ' <<<"$out" | awk '{ $1 = ""; sub(/^ +/, ""); print }'; }
tagged() { jq --arg t "$2" --arg v "$3" '. + {server_tag: $t, server_version: $v}' "$work/new.json" > "$work/$1.json"; }
tagged v1321 1.32.1 1.32.1; tagged v1310 1.31.0 1.31.0; tagged v1330 1.33.0 1.33.0; tagged v1310c 1.31.0 1.32.1
vd "$work/v1321.json" FAKE_THRESHOLD_EMPTY="$NO_ACT" VERDICT_JSON="$work/vj.json"
eq "1.32.1, no activity data, every other reading in bounds: ADVANCE, exit 0" "$(grep -o 'VERDICT: [A-Z]*' <<<"$out"):$(tail -1 <<<"$out")" "VERDICT: ADVANCE:rc=0"
eq "1.32.1, no activity data: the row reads not produced on this release, with its file" "$(act_row)" "null $ACT_MAX not produced on this release (load/releases/1.32.yaml)"
eq "1.32.1, no activity data: both activity rows read not produced on this release (load/releases/1.32.yaml), ADVANCE" \
  "$(grep -E '^activity_schedule_to_start_(p99_ms|over_10ms) ' <<<"$out" | awk '{ n = $1; $1 = $2 = $3 = ""; sub(/^ +/, ""); print n ": " $0 }' | paste -sd '|'):$(grep -o 'VERDICT: [A-Z]*' <<<"$out")" \
  "activity_schedule_to_start_p99_ms: not produced on this release (load/releases/1.32.yaml)|activity_schedule_to_start_over_10ms: not produced on this release (load/releases/1.32.yaml):VERDICT: ADVANCE"
has "1.32.1: the verdict names the release file" "$out" \
  "thresholds: load/steady-state.yaml, the base (1.31.0 baselines), with load/releases/1.32.yaml for server 1.32.1: 2 overrides"
has "1.32.1: and prints the override with its reason and evidence" "$out" \
  "  override activity_schedule_to_start_p99_ms, no_data: not_judged: 1.32 runs activities eagerly"
has "1.32.1: the override's evidence" "$out" "(evidence: cells/results/2026-10-23-boot1.txt)"
eq "VERDICT_JSON, 1.32.1: thresholds_release, the override applied and not produced, the row kept and not deciding" \
  "$(jq -c '[.verdict, .thresholds_release, [.overrides_applied[] | [.name, .no_data, .not_produced]], .deciding, (.rows[] | select(.metric == "activity_schedule_to_start_p99_ms") | [.value, .blocks])]' "$work/vj.json" 2>&1)" \
  '["ADVANCE","load/releases/1.32.yaml",[["activity_schedule_to_start_p99_ms","not_judged",true],["activity_schedule_to_start_over_10ms","not_judged",true]],[],[null,false]]'
has "VERDICT_JSON, 1.32.1: the note" "$(jq -r .thresholds_note "$work/vj.json" 2>&1)" "with load/releases/1.32.yaml for server 1.32.1"
vd "$work/v1321.json" FAKE_THRESHOLD_ONLY="$NO_ACT" FAKE_THRESHOLD_VALUE=150 VERDICT_JSON="$work/vj.json"
eq "1.32.1, activity data over its max: still BLOCK, exit 1, on that row" "$(grep -o 'VERDICT: [A-Z]*' <<<"$out"):$(tail -1 <<<"$out"):$(act_row)" \
  "VERDICT: BLOCK:rc=1:150.000 $ACT_MAX EXCEEDS"
eq "VERDICT_JSON, 1.32.1 with data: the override is recorded, not produced false, the row decides" \
  "$(jq -c '[[.overrides_applied[] | .not_produced], .deciding]' "$work/vj.json" 2>&1)" '[[false,false],["activity_schedule_to_start_p99_ms"]]'
vd "$work/v1310.json" FAKE_THRESHOLD_EMPTY="$NO_ACT" VERDICT_JSON="$work/vj.json"
eq "1.31.0, no activity data: still BLOCK, exit 1, EXCEEDS (no data)" "$(grep -o 'VERDICT: [A-Z]*' <<<"$out"):$(tail -1 <<<"$out"):$(act_row)" \
  "VERDICT: BLOCK:rc=1:null $ACT_MAX EXCEEDS (no data)"
has "1.31.0: the verdict says the base applies unchanged" "$out" \
  "thresholds: load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no load/releases/1.31.yaml for server 1.31.0"
eq "VERDICT_JSON, 1.31.0: no release file, no overrides" "$(jq -c '[.thresholds_release, .overrides_applied, .deciding]' "$work/vj.json" 2>&1)" \
  '[null,[],["activity_schedule_to_start_p99_ms"]]'
vd "$work/v1330.json" FAKE_THRESHOLD_EMPTY="$NO_ACT"
eq "1.33.0, a release with no file, no activity data: BLOCK on the base" "$(grep -o 'VERDICT: [A-Z]*' <<<"$out"):$(act_row)" "VERDICT: BLOCK:null $ACT_MAX EXCEEDS (no data)"
has "1.33.0: says the base applies, and that there is no file for it" "$out" \
  "thresholds: load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no load/releases/1.33.yaml for server 1.33.0"
vd "$work/v1310c.json" FAKE_THRESHOLD_EMPTY="$NO_ACT"
eq "tag 1.31.0, the cell said 1.32.1: the cell's word picks load/releases/1.32.yaml, ADVANCE" "$(grep -o 'VERDICT: [A-Z]*' <<<"$out"):$(act_row)" \
  "VERDICT: ADVANCE:null $ACT_MAX not produced on this release (load/releases/1.32.yaml)"
vd "$work/v1321.json" FAKE_THRESHOLD_EMPTY="$NO_ACT" THRESHOLDS=load/steady-state.yaml
eq "1.32.1 with THRESHOLDS given: no release file, BLOCK on no data" "$(grep -o 'VERDICT: [A-Z]*' <<<"$out"):$(act_row)" "VERDICT: BLOCK:null $ACT_MAX EXCEEDS (no data)"
has "THRESHOLDS given: the verdict says so" "$out" "thresholds: load/steady-state.yaml, given in THRESHOLDS: no release file applies"
vd "$work/v1321.json" FAKE_THRESHOLD_EMPTY="$NO_ACT" THRESHOLDS_RELEASES="$work/rel-kind" VERDICT_JSON="$work/vj2.json"
eq "a release file that cannot be applied: exit 2, nothing judged, no VERDICT_JSON" \
  "$(tail -1 <<<"$out"):$(grep -c 'VERDICT:' <<<"$out"):$([[ -e "$work/vj2.json" ]] && echo written || echo none)" "rc=2:0:none"
has "a release file that cannot be applied: says why" "$out" "no threshold set for server 1.32.1; nothing judged"
# A saved 1.32.1 set passed back as THRESHOLDS would not judge activity schedule-to-start on no data on any release, and
# report no override: refused (load/thresholds.sh), so a 1.31.0 run with no activity data gets no verdict at all.
rm -f "$work/vj2.json"; vd "$work/v1310.json" FAKE_THRESHOLD_EMPTY="$NO_ACT" THRESHOLDS="$work/th-saved.yaml" VERDICT_JSON="$work/vj2.json"
eq "a saved 1.32.1 set as THRESHOLDS, a 1.31.0 run with no activity data: exit 2, no verdict, no VERDICT_JSON" \
  "$(tail -1 <<<"$out"):$(grep -c 'VERDICT:' <<<"$out"):$([[ -e "$work/vj2.json" ]] && echo written || echo none)" "rc=2:0:none"
has "a saved set as THRESHOLDS: says why" "$out" "it holds resolved:, so load/thresholds.sh printed it"

section "cells/softasserts.sh"
fm='"temporal-frontend":null,"temporal-frontend2":null,"temporal-history":null'
eq "the fixture: one soft assertion; the near miss and the lines that are not JSON do not count" \
  "$(cells/softasserts.sh --file temporal-matching="$SA_FIXTURE")" \
  "{$fm,\"temporal-matching\":1,\"temporal-worker\":null,\"first\":[\"failed assertion: loadedTasks went negative\"]}"
eq "a file on stdin" "$(cells/softasserts.sh --file temporal-history=- < "$SA_FIXTURE" | jq -c '[.["temporal-history"], .first[0]]')" \
  '[1,"failed assertion: loadedTasks went negative"]'
printf '{"level":"error","ts":"2026-10-14T02:51:32.000Z","msg":"unable to log","error":"quoted {\\"failed-assertion\\":true}"}\n' > "$work/quoted.txt"
eq "a line that quotes the tag in its text does not count" \
  "$(cells/softasserts.sh --file temporal-worker="$work/quoted.txt" | jq -c '.["temporal-worker"]')" 0
for i in $(seq 1 12); do sa_line $(( 100 - i * 2 )) "m$i" | cut -f2-; done > "$work/sa-a.txt"
for i in $(seq 1 12); do sa_line $(( 99 - i * 2 )) "n$i" | cut -f2-; sa_line 200 "m1" | cut -f2-; done > "$work/sa-b.txt"
eq "first: distinct messages by the time each first appeared, across roles, at most 10" \
  "$(cells/softasserts.sh --file temporal-history="$work/sa-a.txt" --file temporal-matching="$work/sa-b.txt" | jq -c '[.["temporal-history"], .["temporal-matching"], (.first | length), .first[0], .first[9]]')" \
  '[12,24,10,"failed assertion: n12","failed assertion: m8"]'
cells/softasserts.sh --file temporal-sql="$SA_FIXTURE" >/dev/null 2>&1; eq "a role that is not a server role is refused" "$?" 2
cells/softasserts.sh --file temporal-history="$SA_FIXTURE" laug-x >/dev/null 2>&1; eq "a project and --file together are refused" "$?" 2
cells/softasserts.sh --file temporal-history="$work/none.txt" --file temporal-history="$SA_FIXTURE" >/dev/null 2>&1
eq "a role named twice is refused" "$?" 2
out="$(cells/softasserts.sh --file temporal-history="$work/none.txt" --file temporal-matching="$SA_FIXTURE" 2>/dev/null)"; rc=$?
eq "a file that cannot be read: its role null, exit 1" "$(jq -c '[.["temporal-history"], .["temporal-matching"]]' <<<"$out"):$rc" '[null,1]:1'
fresh
sd() { env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_DOCKER_LOGS="$SA_LOGS" "$@"; }
out="$(sd cells/softasserts.sh laug-abcdef12 2>/dev/null)"; rc=$?
eq "docker: every role since its start; one with no container null, exit 1" \
  "$(jq -c 'del(.first)' <<<"$out"):$rc" \
  '{"temporal-frontend":0,"temporal-frontend2":null,"temporal-history":2,"temporal-matching":4,"temporal-worker":0}:1'
has "docker: --since defaults to the container's StartedAt" "$(cat "$FAKE_DIR/docker.args")" $'logs\n--since\n2026-10-13T13:59:00Z\nfake-temporal-history'
hasnt "docker: no role's lines rotated away, none says so" "$(sd cells/softasserts.sh laug-abcdef12 2>&1 >/dev/null)" "rotated"
out="$(sd FAKE_DOCKER_ROTATED=temporal-history@30 cells/softasserts.sh laug-abcdef12 2>"$work/rot.err")"; rc=$?
eq "docker: history's lines before T0+30 rotated away: history null, the others counted, exit 1" \
  "$(jq -c '[.["temporal-history"], .["temporal-matching"]]' <<<"$out"):$rc" '[null,4]:1'
has "docker: the rotation is named" "$(cat "$work/rot.err")" "temporal-history in laug-abcdef12: its oldest kept line is from 2026-10-13T14:01:05.000000000Z"
eq "docker: rotated, --since before the oldest kept line (T0+65): still null" \
  "$(sd FAKE_DOCKER_ROTATED=temporal-history@30 cells/softasserts.sh --since $(( T0 + 40 )) laug-abcdef12 2>/dev/null | jq -c '.["temporal-history"]')" null
eq "docker: rotated, --since after the oldest kept line: counted" \
  "$(sd FAKE_DOCKER_ROTATED=temporal-history@30 cells/softasserts.sh --since $(( T0 + 66 )) laug-abcdef12 2>/dev/null | jq -c '.["temporal-history"]')" 1
eq "docker: restarted at T0+60, its oldest kept line from before: counted from the restart" \
  "$(sd FAKE_STARTED_AT=2026-10-13T14:01:00Z cells/softasserts.sh laug-abcdef12 2>/dev/null | jq -c '[.["temporal-history"], .["temporal-matching"]]')" '[2,3]'
eq "docker: --since and --until pass through, both inclusive" \
  "$(sd cells/softasserts.sh --since $(( T0 + 60 )) --until $(( T0 + 70 )) laug-abcdef12 2>/dev/null | jq -c '[.["temporal-history"], .["temporal-matching"], .first]')" \
  '[1,2,["failed assertion: completed task was already acked","failed assertion: loadedTasks went negative"]]'
sd FAKE_DOCKER_PROJECT=laug-other cells/softasserts.sh laug-abcdef12 >/dev/null 2>&1; eq "docker: a project with no server containers exits 2" "$?" 2

section "faults/run.sh (compose and microVM leases, fake clock)"
# Timeline at the test catalog's windows: load from T0, before window T0+0..60, inject T0+60, landed T0+65, removed
# and recovered T0+80, after window T0+110..200, sweep cutoff T0+170 (2026-10-13T14:02:50Z).
# A lease's dynamic config: a compose cell's is a file on this host (run/dynamicconfig), a microVM cell's a path in the
# guest (guest/dynamicconfig). The fake ssh runs guest commands here, so both live under FAKE_DIR, each with the
# template's laug.yaml and the cell-id marker beside it, and the guest's laug.yaml.orig at laug.yaml.orig. A lease's
# marker, on this host or in the guest, says LEASE_CELL_ID (abcdef12), as create writes it. The guest's copy of
# cells/compose, where serverfi recreates history, is guest/compose: the checkout's cell.yml, fi.yml and
# config/laug-server.yaml, as cells/rootfs/build-image.sh copies them. LEASE_TAG puts a
# server_tag in the lease, as create prints it, and LEASE_BACKEND, LEASE_FC_VERSION and LEASE_CACHE_TYPE a backend,
# firecracker_version and cache_type; unset, the lease has no such key, as a lease from before the backends named
# themselves.
fake_lease() { # fake_lease [expires-in-seconds]: writes FAKE_DIR/lease.json for LEASE_SSH's kind of cell; sets dyn
  local ssh="${LEASE_SSH:-}" dir id=abcdef12
  if [[ -n "$ssh" ]]; then dir="$FAKE_DIR/guest/dynamicconfig"; else dir="$FAKE_DIR/run/dynamicconfig"; fi; id="${LEASE_CELL_ID:-abcdef12}"
  mkdir -p "$dir"; echo "$id" > "$dir/cell-id"; : > "$FAKE_DIR/guest.key"; : > "$FAKE_DIR/guest.known_hosts"
  cp "$DYNCONFIG_TEMPLATE" "$dir/laug.yaml"; cp "$DYNCONFIG_TEMPLATE" "$FAKE_DIR/laug.yaml.orig"
  mkdir -p "$FAKE_DIR/guest/compose/config"; cp cells/compose/cell.yml cells/compose/fi.yml "$FAKE_DIR/guest/compose/"
  cp cells/compose/config/laug-server.yaml "$FAKE_DIR/guest/compose/config/"
  dyn="$dir/laug.yaml"
  jq -n --arg exp "$("$REAL_DATE" -u -d "@$(( T0 + ${1:-3600} ))" +%Y-%m-%dT%H:%M:%SZ)" --arg ssh "$ssh" --arg dyn "$dyn" --arg tag "${LEASE_TAG:-}" \
    --arg backend "${LEASE_BACKEND:-}" --arg fcv "${LEASE_FC_VERSION:-}" --arg cache "${LEASE_CACHE_TYPE:-}" \
    '{id: "abcdef12", address: "127.0.0.1:17233", prometheus: "http://127.0.0.1:19090", toxiproxy: "http://127.0.0.1:18474",
      expires_at: $exp, dynconfig: $dyn} + (if $ssh == "" then {} else {ssh: $ssh} end)
      + (if $tag == "" then {} else {server_tag: $tag} end) + (if $backend == "" then {} else {backend: $backend} end)
      + (if $fcv == "" then {} else {firecracker_version: $fcv} end) + (if $cache == "" then {} else {cache_type: $cache} end)' \
    > "$FAKE_DIR/lease.json"
}
fr_env() { # fr_env <run-id>: fr_vars, the fake world of one raun, for env, once fake_lease has run
  fr_vars=(PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_CELL=abcdef12 FAKE_OMES_BLOCK=1 FAKE_WAIT_READY=1
    FAKE_OMES_LOG="$td/omes-raun.txt" FAKE_SWEEP="$td/sweep/ok.json" FAKE_DOCKER_LOGS="$SA_LOGS" OMES_DIR="$FAKE_DIR/omes" RESULTS_DIR="$FAKE_DIR/results"
    CATALOG="$td/catalog.yaml" RUN_ID="$1" POOL_ADDR=http://127.0.0.1:17700 FC_GUEST_KEY="$FAKE_DIR/guest.key"
    FC_GUEST_KNOWN_HOSTS="$FAKE_DIR/guest.known_hosts" GUEST_DYNCONFIG_ORIG="$FAKE_DIR/laug.yaml.orig"
    GUEST_COMPOSE_DIR="$FAKE_DIR/guest/compose")
}
fr() { # fr <run-id> [env...]: runs one raun of FR_FAULT (test-latency); sets out, rc, j, dyn
  local id="$1"; shift; fresh; fake_lease; fr_env "$id"
  out="$(env "${fr_vars[@]}" "$@" faults/run.sh workflow_with_single_noop_activity "${FR_FAULT:-test-latency}" "$FAKE_DIR/lease.json" 2>&1)"; rc=$?
  j="$FAKE_DIR/results/$id.json"
}
fr raun-ht1
eq "clean raun: ADVANCE, exit 0" "$(tail -1 <<<"$out"):$rc" "ADVANCE:0"
eq "clean raun: windows on the fake clock" "$(jq -c '[.inject_at, .landed_at, .remove_at, .after_start, .after_end] | map(tonumber - 1791900000)' "$j" 2>&1)" '[60,65,80,110,200]'
eq "clean raun: the removal ran 0 s after the hold ended; no readings on a row without them" "$(jq -c '[.remove_wait_s, has("readings")]' "$j" 2>&1)" '[0,false]'
eq "clean raun: failed iterations by window" "$(jq -c .iterations_failed "$j" 2>&1)" \
  '{"before":1,"during":2,"drain":1,"after":0,"late":1,"unplaced":0,"not_found":0,"total":5}'
eq "clean raun: records the lease address, after_s and the sweep margin" "$(jq -c '[.address, .after_s, .sweep_margin_s]' "$j" 2>&1)" '["127.0.0.1:17233",90,30]'
# The backend and nesting keys follow server_tag: a lease with no backend key and no ssh is compose (the fallback), a
# compose cell has no snapshot values, and host_virtualized is what /proc/cpuinfo says of this host, a boolean either way.
eq "clean raun: a lease with no backend key, no ssh: backend compose after server_tag, no snapshot values, host_virtualized a boolean" \
  "$(jq -c '[.backend, .firecracker_version, .cache_type, (.host_virtualized | type), keys_unsorted[4:9]]' "$j" 2>&1)" \
  '["compose",null,null,"boolean",["server_tag","backend","firecracker_version","cache_type","host_virtualized"]]'
has "clean raun: Omes's log is in the load directory" "$(ls "$FAKE_DIR/results/load" 2>&1)" "raun-ht1.log"
eq "clean raun: the server tag, read from the cell's history image" "$(jq -r .server_tag "$j" 2>&1)" "1.31.0"
has "clean raun: the summary names the server" "$out" "server      1.31.0"
eq "clean raun: sweep, cutoff, rate, task queue, after-window completions" \
  "$(jq -c '{sweep, sweep_cutoff, sweep_error, rate, task_queue, done: .after.workflows_completed}' "$j" 2>&1)" \
  '{"sweep":{"Completed":3400},"sweep_cutoff":"2026-10-13T14:02:50Z","sweep_error":null,"rate":20,"task_queue":"omes-raun-ht1","done":1800}'
has "clean raun: sweep query" "$(cat "$FAKE_DIR/temporal.args")" "TaskQueue='omes-raun-ht1' AND StartTime < '2026-10-13T14:02:50Z' GROUP BY ExecutionStatus"
has "clean raun: load ran with --continue-on-iteration-failure and a timeout" "$(cat "$FAKE_DIR/go.args")" $'--timeout\n440s'
# Soft assertions as {window: [matching, history, frontend2, first]}. Windows are [start, end), counted in slices of
# at most 30 s: before is [0, 40) and [40, 60), so the line at 60 s, injection, counts in during only; after is three.
sa_view() { jq -c '.softasserts | map_values(if . == null then null else [.["temporal-matching"], .["temporal-history"], .["temporal-frontend2"], (.first | map(sub("^failed assertion: "; "")))] end)' "$j" 2>&1; }
SA_WANT='{"before":[1,0,null,["loadedTasks went negative"]],"during":[2,1,null,["completed task was already acked","loadedTasks went negative"]],"drain":[1,0,null,["expected a task from match"]],"after":[0,1,null,["ArchetypeID not specified, defaulting to Workflow."]]}'
eq "clean raun: soft assertions by window, per role" "$(sa_view)" "$SA_WANT"
has "clean raun: the before window's last slice ends 1 ns before injection" "$(cat "$FAKE_DIR/docker.args")" $'logs\n--since\n1791900040\n--until\n1791900059.999999999'
has "clean raun: the after window in 30 s slices" "$(cat "$FAKE_DIR/docker.args")" $'logs\n--since\n1791900140\n--until\n1791900169.999999999'
has "clean raun: the summary counts them by window" "$out" "softassert  before 1, during 3, drain 1, after 1, by window (recorded, not judged)"
{ cat "$td/omes-raun.txt"; printf '2026-10-13T19:32:30.000+0530\tERROR\tloadgen/generic_executor.go:196\titeration 3000 failed: workflow execution error\n'; } > "$work/omes-after.txt"
LEASE_BACKEND=compose fr raun-ht1b
eq "clean raun: a lease that names backend compose: backend compose" "$(jq -c '[.result, .backend, .firecracker_version, .cache_type]' "$j" 2>&1)" '["ADVANCE","compose",null,null]'
fr raun-ht2 FAKE_OMES_LOG="$work/omes-after.txt"
eq "failed iteration in the after window: BLOCK, exit 1" "$(tail -1 <<<"$out"):$rc" "BLOCK:1"
eq "failed iteration in the after window: reason" "$(jq -r .reason "$j" 2>&1)" "1 failed iteration(s) in the after window"
fr raun-ht3 FAKE_SWEEP="$td/sweep/running.json"
eq "sweep still Running after two more reads: BLOCK" "$(jq -r '.result + ": " + .reason' "$j" 2>&1):$(calls)" \
  "BLOCK: sweep: Running=10 among workflows started before 2026-10-13T14:02:50Z:3"
fr raun-ht4 FAKE_SWEEP="$td/sweep/running.json:$td/sweep/ok.json"
eq "sweep Running once, then Completed: ADVANCE" "$(jq -r .result "$j" 2>&1):$(calls)" "ADVANCE:2"
fr raun-ht5 FAKE_WF_SLOW_FROM=$(( T0 + 110 )) FAKE_WF_SLOW_RATE=13
eq "after window under the floor: BLOCK" "$(jq -r '.result + ": " + .reason' "$j" 2>&1)" \
  "BLOCK: after window completed 1170 workflows, floor 1600 (20/s x (90 s - 10))"
fr raun-ht11 FAKE_OMES_LOG="$work/not-found.txt"
eq "workflow execution not found during the fault and the drain: BLOCK" "$(jq -r '.result + ": " + .reason' "$j" 2>&1)" \
  "BLOCK: 2 failed iteration(s) before the after window ended: workflow execution not found"
has "workflow execution not found: the summary says so" "$out" "; 2 said workflow execution not found"
fr raun-ht12 SWEEP_MARGIN_S=60
eq "SWEEP_MARGIN_S 60: the cutoff is 60 s before the after window ended" "$(jq -r '.sweep_cutoff + " " + (.sweep_margin_s | tostring)' "$j" 2>&1)" \
  "2026-10-13T14:02:20Z 60"
fr raun-ht13 FAKE_OMES_BLOCK=0 FAKE_OMES_EXIT=1 FAKE_WAIT_READY=results/load/raun-ht13.json
eq "load exits in its first 10 s: exit 2" "$rc" 2
has "load exits in its first 10 s: says so" "$out" "load generator exited"
[[ -e "$j" ]] && fail "load exits in its first 10 s: nothing at the raun's path" "found: $(cat "$j")" \
  || pass "load exits in its first 10 s: nothing at the raun's path"
eq "load exits in its first 10 s: its run JSON is in the load directory, with the cell's server tag" \
  "$(jq -c '{omes_exit, task_queue, server_tag}' "$FAKE_DIR/results/load/raun-ht13.json" 2>&1)" '{"omes_exit":1,"task_queue":"omes-raun-ht13","server_tag":"1.31.0"}'
fr raun-ht6 FAKE_TEMPORAL_EXIT=1
eq "sweep fails: BLOCK" "$(jq -r '.result + ": " + .reason' "$j" 2>&1)" "BLOCK: sweep failed: load/sweep.sh exited 1 against 127.0.0.1:17233"
fr raun-ht19 FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1
eq "a 1.32.1 cell: ADVANCE, server_tag 1.32.1" "$(jq -r '.result + " " + .server_tag' "$j" 2>&1)" "ADVANCE 1.32.1"
LEASE_TAG=1.32.1 fr raun-ht20
eq "the lease says 1.32.1, the cell runs 1.31.0: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "the lease and the cell disagree: says so" "$out" "the lease says server 1.32.1 and cell abcdef12's temporal-history runs temporalio/server:1.31.0"
LEASE_TAG=1.31.0 fr raun-ht21
eq "the lease and the cell agree: ADVANCE" "$(jq -r '.result + " " + .server_tag' "$j" 2>&1)" "ADVANCE 1.31.0"
# Threshold sets per server release in a raun: the before and after windows are judged on the cell's release's set.
fr raun-rel1 FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 FAKE_THRESHOLD_EMPTY="$NO_ACT"
eq "a 1.32.1 cell, no activity data: ADVANCE, not never steady" "$(jq -r '.result + " " + .reason' "$j" 2>&1):$rc" "ADVANCE :0"
eq "a 1.32.1 cell: the raun JSON names the release file, the override, and the windows where it was not produced" \
  "$(jq -c '[.thresholds_release, [.overrides_applied[] | [.name, .no_data]], .not_produced, .before.activity_schedule_to_start_p99_ms]' "$j" 2>&1)" \
  '["load/releases/1.32.yaml",[["activity_schedule_to_start_p99_ms","not_judged"],["activity_schedule_to_start_over_10ms","not_judged"]],{"before":["activity_schedule_to_start_p99_ms"],"after":["activity_schedule_to_start_p99_ms"]},null]'
has "a 1.32.1 cell: the summary names the files" "$out" "thresholds  load/steady-state.yaml, the base (1.31.0 baselines), with load/releases/1.32.yaml for server 1.32.1: 2 overrides"
has "a 1.32.1 cell: the summary shows the override and what it did" "$out" \
  "override    activity_schedule_to_start_p99_ms: no data in the before and after window, not produced on this release, not judged (load/releases/1.32.yaml: 1.32 runs activities eagerly"
has "a 1.32.1 cell: the override with no max says so, and that it had no data in either window" "$out" \
  "override    activity_schedule_to_start_over_10ms: no data in the before and after window; no max in the threshold set, so not judged (load/releases/1.32.yaml: "
has "a 1.32.1 cell: the before window's log says so" "$out" "before: no data, not produced on this release and not judged: activity_schedule_to_start_p99_ms (load/releases/1.32.yaml)"
eq "a 1.32.1 cell: the signal still comes from the set, persistence_latency_p99_ms at its max" "$(jq -c '[.signal, .signal_max]' "$j" 2>&1)" "[\"persistence_latency_p99_ms\",$PERS_MAX]"
fr raun-rel2 FAKE_THRESHOLD_EMPTY="$NO_ACT"
eq "a 1.31.0 cell, no activity data: still BLOCK never steady" "$(jq -r '.result + ": " + .reason' "$j" 2>&1)" "BLOCK: never steady: activity_schedule_to_start_p99_ms=null"
eq "a 1.31.0 cell: no release file, nothing listed as not produced" "$(jq -c '[.thresholds_release, .overrides_applied, .not_produced]' "$j" 2>&1)" \
  '[null,[],{"before":[],"after":null}]'
fr raun-rel3 FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 FAKE_THRESHOLD_ONLY="$NO_ACT" FAKE_THRESHOLD_VALUE=150
eq "a 1.32.1 cell, activity data over its max in the before window: BLOCK never steady" "$(jq -r '.result + ": " + .reason' "$j" 2>&1)" \
  "BLOCK: never steady: activity_schedule_to_start_p99_ms=150"
fr raun-rel4 FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 FAKE_THRESHOLD_ONLY="$NO_ACT" FAKE_THRESHOLD_VALUE=150 FAKE_THRESHOLD_FROM=110
eq "a 1.32.1 cell, activity data over its max from the after window on: BLOCK after window not clean" "$(jq -r '.result + ": " + .reason' "$j" 2>&1)" \
  "BLOCK: after window not clean: activity_schedule_to_start_p99_ms=150"
has "a 1.32.1 cell with activity data: the summary says it was judged" "$out" \
  "override    activity_schedule_to_start_p99_ms: had data where judged, so judged against its max"
fr raun-rel5 FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 THRESHOLDS_RELEASES="$work/rel-kind"
eq "a release file that cannot be applied: refused before the load, exit 2, nothing written" \
  "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load):$([[ -e "$j" ]] && echo written || echo none)" "2:no-load:none"
has "a release file that cannot be applied: says why" "$out" "no threshold set for server 1.32.1; nothing was run"
yq '.faults["test-act"] = .faults["test-latency"] + {"signal": "activity_schedule_to_start_p99_ms"} | .faults["test-nosig"] = .faults["test-latency"] + {"signal": "activity_s2s"}' \
  "$td/catalog.yaml" > "$work/sig-catalog.yaml"
FR_FAULT=test-act fr raun-rel6 CATALOG="$work/sig-catalog.yaml" FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1
has "a signal the release does not judge on no data: the log says no data neither lands nor recovers" "$out" \
  "note: signal activity_schedule_to_start_p99_ms is not judged on no data on this release (load/releases/1.32.yaml); as a signal, no data neither lands nor recovers"
FR_FAULT=test-nosig fr raun-rel7 CATALOG="$work/sig-catalog.yaml"
eq "a signal that names no threshold: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "a signal that names no threshold: says so" "$out" "fault test-nosig: signal activity_s2s is not a threshold with a max in load/steady-state.yaml"
fr raun-rel8 FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 FAKE_THRESHOLD_EMPTY="$NO_ACT" THRESHOLDS="$work/th-saved.yaml"
eq "a saved 1.32.1 set as THRESHOLDS: refused before the load, exit 2, nothing written" \
  "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load):$([[ -e "$j" ]] && echo written || echo none)" "2:no-load:none"
has "a saved set as THRESHOLDS: says why" "$out" "it holds resolved:, so load/thresholds.sh printed it"
# thresholds_judge (faults/lib.sh) compares numbers, as load/verdict.sh's awk does, even in a set the resolver did not
# check: a quoted max of "100" still fails a reading of 150 (in jq a number is never greater than a string).
printf 'thresholds:\n  - {name: a, max: "100"}\n  - {name: b, max: 100}\n  - {name: c}\n' > "$work/th-judge.yaml"
out="$(bash -c 'root="$PWD"; RUN_ID=tj; . faults/lib.sh; TH_FILE="$1"; thresholds_judge "{\"a\": 150, \"b\": 99, \"c\": 5}"' _ "$work/th-judge.yaml" 2>&1)"
eq "thresholds_judge: a quoted max still fails a reading over it; a threshold with no max is skipped" "$out" '{"failing":["a=150"],"not_produced":[]}'
# The pool (faults/lib.sh, pool_quiet and pool_poll): before its load a raun waits until the fake laugd shows no busy
# cell but its own, POOL_QUIET_WAIT_S at most, polling every 5 s, and records the most busy cells one poll saw in each
# window. FAKE_POOL_BUSY puts other cells busy at fake-clock times; status.calls lists when /status was read. The load
# moves later by the wait, so these rauns read omes-ok.txt, whose failures are not tied to the test catalog's windows.
fr raun-pool1 FAKE_POOL_BUSY=0:15 FAKE_OMES_LOG="$td/omes-ok.txt"
eq "a busy cell that clears after 3 polls: the load waits 15 s, the windows move 15 s, ADVANCE" \
  "$(jq -c '[.result, .pool_quiet_wait_s, .pool_quiet_bound_s, ((.inject_at | tonumber) - 1791900000)]' "$j" 2>&1):$rc" '["ADVANCE",15,300,75]:0'
eq "a busy cell that clears: polled every 5 s before the load, the lease checked again, then each boundary and loop" \
  "$(xargs < "$FAKE_DIR/status.calls")" "0 0 5 10 15 15 75 75 75 80 95 125 215"
has "a busy cell that clears: the before window's soft assertions start with the load, at 15 s" "$(cat "$FAKE_DIR/docker.args")" $'logs\n--since\n1791900015\n'
eq "a busy cell that clears: no window saw one" "$(jq -c .pool_busy "$j" 2>&1)" '{"before":0,"during":0,"drain":0,"after":0}'
has "a busy cell that clears: the log says why the load waited" "$out" \
  "pool: 1 other cell(s) busy at laugd (being created, reset or destroyed); waiting up to 300 s before the load"
has "a busy cell that clears: the summary says how long" "$out" \
  "pool        waited 15 s before the load until laugd showed no busy cell; busy cells by window, most at once: before 0, during 0, drain 0, after 0"
hasnt "a busy cell that clears: no warning" "$out" "warning     "
fr raun-pool2 FAKE_POOL_BUSY=0:21:2 POOL_QUIET_WAIT_S=20 FAKE_OMES_LOG="$td/omes-ok.txt"
eq "2 cells busy past the bound: the load starts at 20 s, the bound, and the raun is not judged on them" \
  "$(jq -c '[.result, .pool_quiet_wait_s, .pool_quiet_bound_s, ((.inject_at | tonumber) - 1791900000)]' "$j" 2>&1):$rc" '["ADVANCE",20,20,80]:0'
eq "2 cells busy past the bound: the before window records them" "$(jq -c .pool_busy "$j" 2>&1)" '{"before":2,"during":0,"drain":0,"after":0}'
has "2 cells busy past the bound: the log says the load started anyway" "$out" \
  "pool: 2 cell(s) still busy at laugd after 20 s, the bound (POOL_QUIET_WAIT_S); starting the load anyway"
has "2 cells busy past the bound: the summary says so" "$out" \
  "pool        2 cell(s) still busy at laugd after 20 s, the bound (POOL_QUIET_WAIT_S); the load started anyway; busy cells by window, most at once: before 2,"
has "2 cells busy past the bound: and warns" "$out" "warning     laugd showed busy cells (before 2): cells being created, reset or destroyed share this host"
# A poll at a boundary belongs to the windows on both sides: 3 cells busy at after_start (110) count in drain and after.
fr raun-pool3 FAKE_POOL_BUSY=62:66,100:111:3
eq "busy cells in the hold and at the after window's start: no wait, recorded per window, ADVANCE" \
  "$(jq -c '[.result, .pool_quiet_wait_s, .pool_busy]' "$j" 2>&1)" '["ADVANCE",0,{"before":0,"during":1,"drain":3,"after":3}]'
has "busy cells in the windows: the summary warns, naming each" "$out" "warning     laugd showed busy cells (during 1, drain 3, after 3): "
has "busy cells in the windows: no wait before the load" "$out" "pool        no busy cell at laugd before the load; busy cells by window, most at once: before 0, during 1, drain 3, after 3"
fr raun-pool4 FAKE_POOL_DOWN=61:1000
eq "laugd stops answering after inject: the windows none of whose polls it answered are null, ADVANCE" \
  "$(jq -c '[.result, .pool_quiet_wait_s, .pool_busy]' "$j" 2>&1):$rc" '["ADVANCE",0,{"before":0,"during":0,"drain":null,"after":null}]:0'
has "laugd stops answering: the summary says not read" "$out" "busy cells by window, most at once: before 0, during 0, drain not read, after not read"
fr raun-pool5 FAKE_THRESHOLD_EMPTY="$NO_ACT"
eq "never steady: only the before window was polled; the others are null" "$(jq -c '[.result, .pool_busy]' "$j" 2>&1)" \
  '["BLOCK",{"before":0,"during":null,"drain":null,"after":null}]'
fr raun-pool6 POOL_QUIET_WAIT_S=5m
eq "POOL_QUIET_WAIT_S not whole seconds: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "POOL_QUIET_WAIT_S not whole seconds: says so" "$out" "POOL_QUIET_WAIT_S must be whole seconds, not '5m'"
# The leased cell itself goes busy during the wait (laugd resetting or reaping it): the lease is checked again after the
# wait, and no load starts on a cell this lease may no longer hold.
fr raun-pool7 FAKE_POOL_BUSY=0:1000 POOL_QUIET_WAIT_S=20 FAKE_UNLEASED_FROM=10
eq "the leased cell goes busy during the wait: refused after it, exit 2, no load, no raun JSON" \
  "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load):$([[ -e "$j" ]] && echo written || echo none)" "2:no-load:none"
has "the leased cell goes busy during the wait: says so" "$out" "pool: cell abcdef12 is not leased after the wait for laugd's busy cells; no load was started"
eq "the leased cell goes busy during the wait: polled to the bound, then the lease checked once more" "$(xargs < "$FAKE_DIR/status.calls")" "0 0 5 10 15 20 20"
# A microVM lease waits the same way: the pool code reads laugd, not the cell, so it has no branch of its own.
LEASE_SSH=root@10.254.3.2 fr raun-pool8 FAKE_POOL_BUSY=0:15 FAKE_OMES_LOG="$td/omes-ok.txt"
eq "microVM lease, a busy cell that clears: the load waits 15 s, ADVANCE" \
  "$(jq -c '[.result, .pool_quiet_wait_s, ((.inject_at | tonumber) - 1791900000), .pool_busy]' "$j" 2>&1):$rc" \
  '["ADVANCE",15,75,{"before":0,"during":0,"drain":0,"after":0}]:0'
# laugd not answering before the load: pool_quiet records null and returns, and the run goes on. faults/run.sh and
# faults/candidate.sh cannot get there with laugd down from their start, since assert_leased refuses the cell first, so
# this calls faults/lib.sh's pool_quiet itself: pq [env...] prints the wait, the busy cells left and the window's value,
# then pool_summary's lines.
pq() {
  env PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_CELL=abcdef12 "$@" bash -c 'root="$PWD"; RUN_ID=pq; CELL=abcdef12; POOL_ADDR=http://127.0.0.1:17700
    . faults/lib.sh; pool_dir="$(mktemp -d)"; pool_quiet before; echo "$pool_quiet_wait_s $pool_quiet_left $(pool_window before)"
    pool_summary before; rm -rf "$pool_dir"' 2>&1
}
fresh; out="$(pq FAKE_POOL_DOWN=0:1000)"
has "laugd down before the load: no wait, null for the wait, the cells left and the window" "$out" "null null null"
has "laugd down before the load: the log says so" "$out" "pool: laugd at http://127.0.0.1:17700 did not answer; not waited for, recorded null"
has "laugd down before the load: the summary says so" "$out" \
  "pool        laugd at http://127.0.0.1:17700 did not answer before the load, so nothing was waited for; busy cells by window, most at once: before not read"
fresh; out="$(pq FAKE_POOL_BUSY=0:1000 FAKE_POOL_DOWN=10:1000)"
has "laugd stops answering in the wait: the wait ends there, at 10 s" "$out" "10 null null"
has "laugd stops answering in the wait: the summary says so" "$out" "pool        laugd stopped answering after 10 s of waiting before the load"
fresh; out="$(pq FAKE_POOL_BUSY=0:1000 FAKE_UNLEASED_FROM=0 POOL_QUIET_WAIT_S=5)"
eq "the leased cell busy itself is not counted, only the other cell" "$(grep -x '[0-9]* [0-9]* [0-9]*' <<<"$out")" "5 1 1"
fresh; out="$(pq FAKE_POOL_BUSY=0:1000 POOL_QUIET_WAIT_S=12)"
eq "a bound that is not a multiple of the 5 s poll: polled at 0, 5, 10 and 12, waited 12 s" \
  "$(grep -x '[0-9]* [0-9]* [0-9]*' <<<"$out"):$(xargs < "$FAKE_DIR/status.calls")" "12 1 1:0 5 10 12"
fresh; out="$(pq FAKE_POOL_BUSY=0:1000 POOL_QUIET_WAIT_S=0)"
eq "POOL_QUIET_WAIT_S 0 with a cell busy: one poll, no wait, the cell recorded" \
  "$(grep -x '[0-9]* [0-9]* [0-9]*' <<<"$out"):$(xargs < "$FAKE_DIR/status.calls")" "0 1 1:0"
has "POOL_QUIET_WAIT_S 0 with a cell busy: the log says the bound was reached" "$out" \
  "pool: 1 cell(s) still busy at laugd after 0 s, the bound (POOL_QUIET_WAIT_S); starting the load anyway"
has "POOL_QUIET_WAIT_S 0 with a cell busy: the summary says so" "$out" \
  "pool        1 cell(s) still busy at laugd after 0 s, the bound (POOL_QUIET_WAIT_S); the load started anyway; busy cells by window, most at once: before 1"
# An answer that is not laugd's status reads as laugd not answering; a pool with no cell lists cells as null (a nil
# slice in laugd's statusView), which is no busy cell.
for c in '<html>502 Bad Gateway</html>|not JSON' '{"error":"not found"}|JSON with no cells' '[]|a JSON list'; do
  fresh; out="$(pq FAKE_POOL_JUNK="${c%%|*}")"
  has "/status answers ${c#*|}: not laugd's status, recorded null" "$out" "null null null"
done
fresh; out="$(pq FAKE_POOL_JUNK='{"cells":null}')"
eq "/status lists cells as null: no busy cell, no wait" "$(grep -x '[0-9]* [0-9]* [0-9]*' <<<"$out")" "0 0 0"
# The Omes metrics ports (faults/lib.sh, wait_omes_ports): before a load, a raun or a candidate waits up to 40 s for each
# of its two ports, read from WORKER_METRICS_ADDR and CLIENT_METRICS_ADDR as load/run-omes.sh reads them, 9092 and 9093
# when unset or empty. A gate gives each item its own pair (gate/run.sh, step 7), 19100+2i and
# 19101+2i: the fake ss holds 19102, the second item's worker port, which must hold up the item that uses it and no
# other. wop [env...] runs wait_omes_ports alone, under pipefail as faults/run.sh and faults/candidate.sh run it, and
# prints what it said, then its exit and the seconds it slept on the fake clock.
wop() {
  env PATH="$FAKE_PATH" FAKE_T0=$T0 "$@" bash -c 'set -o pipefail; root="$PWD"; RUN_ID=wop; . faults/lib.sh; wait_omes_ports; echo "$? $(cat "$FAKE_CLOCK")"' 2>&1
}
gate2=(WORKER_METRICS_ADDR=127.0.0.1:19102 CLIENT_METRICS_ADDR=127.0.0.1:19103)
fresh; eq "Omes ports, unset: 9092 held to 12 s, then 9093 to 20 s: waits for both, 20 s" "$(wop FAKE_SS_LISTEN=9092:12,9093:20 | tail -1)" "0 20"
fresh; eq "Omes ports, unset: 19102 held throughout: no wait, the defaults are 9092 and 9093" "$(wop FAKE_SS_LISTEN=19102 | tail -1)" "0 0"
fresh; eq "Omes ports, set empty: read as unset, 9093 held to 7 s: waits 7 s" \
  "$(wop WORKER_METRICS_ADDR= CLIENT_METRICS_ADDR= FAKE_SS_LISTEN=9093:7 | tail -1)" "0 7"
fresh; eq "Omes ports 19102 and 19103: 19102 held to 15 s: waits 15 s" "$(wop "${gate2[@]}" FAKE_SS_LISTEN=19102:15 | tail -1)" "0 15"
fresh; eq "Omes ports 19102 and 19103: 19103 held to 9 s: the client port is waited for too, 9 s" \
  "$(wop "${gate2[@]}" FAKE_SS_LISTEN=19103:9 | tail -1)" "0 9"
fresh; eq "Omes ports 19100 and 19101: the second item's pair and the defaults held throughout: no wait" \
  "$(wop WORKER_METRICS_ADDR=127.0.0.1:19100 CLIENT_METRICS_ADDR=127.0.0.1:19101 FAKE_SS_LISTEN=19102,19103,9092,9093 | tail -1)" "0 0"
fresh; eq "Omes ports 19102 and 19103: 19102 held throughout: waits the 40 s bound, then goes on" "$(wop "${gate2[@]}" FAKE_SS_LISTEN=19102 | tail -1)" "0 40"
fresh; eq "Omes ports, IPv6 [::]:19102: the port after the last colon, held to 6 s: waits 6 s" \
  "$(wop WORKER_METRICS_ADDR='[::]:19102' FAKE_SS_LISTEN=19102:6 | tail -1)" "0 6"
for a in localhost 19102 127.0.0.1: 127.0.0.1:0 127.0.0.1:65536 127.0.0.1:9092x; do
  fresh; out="$(wop WORKER_METRICS_ADDR="$a" FAKE_SS_LISTEN=9093:30)"
  eq "Omes ports, worker address '$a': exit 2 before any wait, even with 9093 held" "$(tail -1 <<<"$out")" "2 0"
  has "Omes ports, worker address '$a': says why" "$out" "Omes metrics address '$a' (WORKER_METRICS_ADDR, CLIENT_METRICS_ADDR) does not end in :<port>, a port from 1 to 65535"
done
fresh; eq "Omes ports, worker held to 30 s and a client address with no port: exit 2 before any wait" \
  "$(wop WORKER_METRICS_ADDR=127.0.0.1:19102 CLIENT_METRICS_ADDR=127.0.0.1 FAKE_SS_LISTEN=19102:30 | tail -1)" "2 0"
fresh; eq "Omes ports 127.0.0.1:65535: the highest port is taken" "$(wop CLIENT_METRICS_ADDR=127.0.0.1:65535 FAKE_SS_LISTEN=65535:3 | tail -1)" "0 3"
# An ss that lists 3000 more listeners after the fake's lines. Piped into grep -q, ss would exit 141 under pipefail
# once grep quit at 19102, and 19102 would read as free; read from a string, the wait still holds 15 s.
mkdir -p "$work/ss-long"
cat > "$work/ss-long/ss" <<EOF
#!/usr/bin/env bash
"$td/fakebin/ss" "\$@"
for i in \$(seq 20000 22999); do printf 'LISTEN 0      4096       127.0.0.1:%s      0.0.0.0:*\n' "\$i"; done
EOF
chmod +x "$work/ss-long/ss"
fresh; eq "Omes ports 19102 and 19103: 19102 held to 15 s, 3000 listeners after it in ss's lines: waits 15 s" \
  "$(wop PATH="$work/ss-long:$FAKE_PATH" "${gate2[@]}" FAKE_SS_LISTEN=19102:15 | tail -1)" "0 15"
# The same through faults/run.sh: the raun waits for its own ports, Omes gets both addresses (load/run-omes.sh), and
# with neither set the load binds 0.0.0.0:9092 and 0.0.0.0:9093 as a raun always has. The wait moves the load later,
# so the raun that waits reads omes-ok.txt, as the pool's do.
fr raun-port1 "${gate2[@]}" FAKE_SS_LISTEN=19102:15 FAKE_OMES_LOG="$td/omes-ok.txt"
eq "raun, Omes ports 19102 and 19103, 19102 held to 15 s: the load waits 15 s, ADVANCE" \
  "$(jq -r .result "$j" 2>&1):$(( $(jq -r .inject_at "$j" 2>/dev/null || echo 0) - T0 )):$rc" "ADVANCE:75:0"
has "raun, Omes ports 19102 and 19103: Omes's worker gets 127.0.0.1:19102" "$(cat "$FAKE_DIR/go.args")" $'--worker-prom-listen-address\n127.0.0.1:19102'
has "raun, Omes ports 19102 and 19103: Omes's client gets 127.0.0.1:19103" "$(cat "$FAKE_DIR/go.args")" $'--prom-listen-address\n127.0.0.1:19103'
fr raun-port2 FAKE_SS_LISTEN=19102,19103
eq "raun, Omes ports unset, another item's pair held: no wait, ADVANCE" \
  "$(jq -r .result "$j" 2>&1):$(( $(jq -r .inject_at "$j" 2>/dev/null || echo 0) - T0 )):$rc" "ADVANCE:60:0"
has "raun, Omes ports unset: Omes's worker gets 0.0.0.0:9092" "$(cat "$FAKE_DIR/go.args")" $'--worker-prom-listen-address\n0.0.0.0:9092'
has "raun, Omes ports unset: Omes's client gets 0.0.0.0:9093" "$(cat "$FAKE_DIR/go.args")" $'--prom-listen-address\n0.0.0.0:9093'
fr raun-port3 WORKER_METRICS_ADDR=localhost
eq "raun, worker address with no port: refused before the load, exit 2, nothing written" \
  "$rc:$([[ -e "$j" ]] && echo written || echo none):$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:none:no-load"
has "raun, worker address with no port: says why" "$out" "Omes metrics address 'localhost'"
hasnt "laug fault, and so laug raun, which runs faults/run.sh as fault does: passes no Omes metrics address, so the load takes the defaults" \
  "$(env PATH="$FAKE_PATH" bin/laug fault persistence-latency -run-id ht -dry-run 2>&1)" "METRICS_ADDR"
fr raun-ht10 FAKE_DOCKER_FAIL=temporal-history@1791900140
eq "one slice cannot read history: history null for that window, the other roles summed" "$(jq -c '.softasserts.after' "$j" 2>&1)" \
  '{"temporal-frontend":0,"temporal-frontend2":null,"temporal-history":null,"temporal-matching":0,"temporal-worker":0,"first":[]}'
LEASE_SSH=root@10.254.3.2 fr raun-ht7
eq "microVM lease: ADVANCE" "$(jq -r .result "$j" 2>&1)" "ADVANCE"
eq "microVM lease with no backend key: backend firecracker (the fallback on ssh), no snapshot values" \
  "$(jq -c '[.backend, .firecracker_version, .cache_type, (.host_virtualized | type)]' "$j" 2>&1)" '["firecracker",null,null,"boolean"]'
has "microVM lease: the sweep runs on the host against the lease address" "$(cat "$FAKE_DIR/temporal.args" 2>&1)" $'--address\n127.0.0.1:17233'
hasnt "microVM lease: the sweep does not go through the guest" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" "workflow count"
eq "microVM lease: soft assertions by window, the logs fetched over ssh" "$(sa_view)" "$SA_WANT"
eq "microVM lease: the server tag read from the guest's Docker" "$(jq -r .server_tag "$j" 2>&1)" "1.31.0"
has "microVM lease: the image is inspected in the guest" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" "docker inspect -f"
LEASE_SSH=root@10.254.3.2 LEASE_BACKEND=firecracker LEASE_FC_VERSION=v1.17.0 LEASE_CACHE_TYPE=Unsafe fr raun-ht7b
eq "microVM lease with backend, firecracker_version and cache_type: the raun JSON carries them after server_tag" \
  "$(jq -c '[.result, .backend, .firecracker_version, .cache_type, keys_unsorted[4:8]]' "$j" 2>&1)" \
  '["ADVANCE","firecracker","v1.17.0","Unsafe",["server_tag","backend","firecracker_version","cache_type"]]'
has "microVM lease: docker logs runs in the guest, both streams merged there" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" \
  "docker logs --since 1791900040 --until 1791900059.999999999 fake-temporal-matching 2>&1"
LEASE_SSH=root@10.254.3.2 fr raun-ht14 FC_GUEST_KEY=/nonexistent/guest.key
eq "microVM lease, no guest key, a toxic fault: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "microVM lease, no guest key: says why" "$out" "/nonexistent/guest.key is not readable"
LEASE_SSH=root@10.254.3.2 fr raun-ht14b FC_GUEST_KNOWN_HOSTS=/nonexistent/guest.known_hosts
eq "microVM lease, no guest known_hosts, a toxic fault: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "microVM lease, no guest known_hosts: says why" "$out" "/nonexistent/guest.known_hosts is not readable"
# The lease's ssh and dynconfig fields (faults/lib.sh, lease_read). ssh goes into ssh's argument list and dynconfig into
# paths here and in the guest's shell. Only these shapes pass: ssh is root@<FC_NET>.<block>.2, 10.254 by default;
# dynconfig is an absolute path to a laug.yaml. lr <lease-file> [env...] runs lease_read alone and prints what it set;
# lf <run-id> <jq filter> [env...] runs a raun on fake_lease's lease after the filter. A refused lease stops the raun
# before anything touches the cell: no ssh, docker, temporal or go call.
lr() { local f="$1"; shift; env "$@" FC_GUEST_KEY="$FAKE_DIR/guest.key" FC_GUEST_KNOWN_HOSTS="$FAKE_DIR/guest.known_hosts" \
  bash -c 'root="$1"; RUN_ID=lr; . "$1/faults/lib.sh"; lease_read "$2"; echo "set $GUEST_SSH $DYNCONFIG $BACKEND"' _ "$root" "$f" 2>&1; }
lf() { local id="$1" filter="$2"; shift 2; fresh; fake_lease; jq "$filter" "$FAKE_DIR/lease.json" > "$FAKE_DIR/l.tmp" && mv "$FAKE_DIR/l.tmp" "$FAKE_DIR/lease.json"
  fr_env "$id"; out="$(env "${fr_vars[@]}" "$@" faults/run.sh workflow_with_single_noop_activity test-latency "$FAKE_DIR/lease.json" 2>&1)"; rc=$?
  ran="$(cd "$FAKE_DIR" && ls ssh.args docker.args temporal.args go.args 2>/dev/null | tr '\n' ' ')"; }
fresh; LEASE_SSH=root@10.254.3.2 fake_lease
eq "lease_read, ssh root@10.254.3.2 and the guest's dynconfig: passes as before, exit 0" "$(lr "$FAKE_DIR/lease.json"; echo "rc $?")" \
  "set root@10.254.3.2 $FAKE_DIR/guest/dynamicconfig/laug.yaml firecracker"$'\n'"rc 0"
fresh; fake_lease
eq "lease_read, no ssh and the compose cell's dynconfig: passes as before, exit 0" "$(lr "$FAKE_DIR/lease.json"; echo "rc $?")" \
  "set  $FAKE_DIR/run/dynamicconfig/laug.yaml compose"$'\n'"rc 0"
LEASE_SSH=root@10.254.3.2 lf raun-lf1 '.ssh = "-oProxyCommand=true"'
eq "lease ssh -oProxyCommand=true: refused, exit 2, one line, nothing runs" "$rc:$(wc -l <<<"$out"):$ran" "2:1:"
eq "lease ssh -oProxyCommand=true: the line names the field and the file" "$out" \
  "lease file $FAKE_DIR/lease.json: its ssh field is -oProxyCommand=true, not root@10.254.<block>.2 (FC_NET, which must be the pool's); refusing it"
for v in root@192.0.2.10 root@10.254.3.20 root@10.254.256.2 "root@10.254.3.2 -oProxyCommand=true" -- ""; do
  fresh; LEASE_SSH=root@10.254.3.2 fake_lease; jq --arg v "$v" '.ssh = $v' "$FAKE_DIR/lease.json" > "$FAKE_DIR/l.tmp" && mv "$FAKE_DIR/l.tmp" "$FAKE_DIR/lease.json"
  o="$(lr "$FAKE_DIR/lease.json")"; r=$?
  if [[ -z "$v" ]]; then eq "lease_read, ssh \"\": a compose lease by the fallback, exit 0" "$r:$o" "0:set  $FAKE_DIR/guest/dynamicconfig/laug.yaml compose"
  else eq "lease_read, ssh $(printf %q "$v"): refused, exit 2" "$r:$o" "2:lease file $FAKE_DIR/lease.json: its ssh field is $(printf %q "$v"), not root@10.254.<block>.2 (FC_NET, which must be the pool's); refusing it"; fi
done
fresh; LEASE_SSH=root@10.254.3.2 fake_lease
eq "lease_read, FC_NET 10.200 as the pool's: root@10.254.3.2 refused, exit 2" "$(lr "$FAKE_DIR/lease.json" FC_NET=10.200 | tail -1; echo "rc ${PIPESTATUS[0]}")" \
  "lease file $FAKE_DIR/lease.json: its ssh field is root@10.254.3.2, not root@10.200.<block>.2 (FC_NET, which must be the pool's); refusing it"$'\n'"rc 2"
eq "lease_read, FC_NET not two octets: refused, exit 2" "$(lr "$FAKE_DIR/lease.json" 'FC_NET=10.254.0'; echo "rc $?")" \
  "FC_NET is 10.254.0, not two octets such as 10.254; refusing lease file $FAKE_DIR/lease.json"$'\n'"rc 2"
lf raun-lf2 '.dynconfig = "-oProxyCommand=true/laug.yaml"'
eq "lease dynconfig -oProxyCommand=true/laug.yaml, compose lease: refused, exit 2, one line, nothing runs" "$rc:$(wc -l <<<"$out"):$ran" "2:1:"
eq "lease dynconfig, compose lease: the line names the field and the file" "$out" \
  "lease file $FAKE_DIR/lease.json: its dynconfig field is -oProxyCommand=true/laug.yaml, not an absolute path to a laug.yaml with no .. part or control character; refusing it"
LEASE_SSH=root@10.254.3.2 lf raun-lf3 '.dynconfig = "/opt/laug/../../etc/laug.yaml"'
eq "lease dynconfig with a .. part, microVM lease: refused, exit 2, nothing runs" "$rc:$ran" "2:"
has "lease dynconfig with a .. part: the line names the field and the file" "$out" "lease file $FAKE_DIR/lease.json: its dynconfig field is /opt/laug/../../etc/laug.yaml,"
for v in relative/laug.yaml /x/laug.yaml.bak $'/x\n/laug.yaml' 127.0.0.1:7700; do
  fresh; fake_lease; jq --arg v "$v" '.dynconfig = $v' "$FAKE_DIR/lease.json" > "$FAKE_DIR/l.tmp" && mv "$FAKE_DIR/l.tmp" "$FAKE_DIR/lease.json"
  o="$(lr "$FAKE_DIR/lease.json")"; r=$?
  eq "lease_read, dynconfig $(printf %q "$v"): refused, exit 2, one line, the reason" "$r:$(wc -l <<<"$o"):$o" \
    "2:1:lease file $FAKE_DIR/lease.json: its dynconfig field is $(printf %q "$v"), not an absolute path to a laug.yaml with no .. part or control character; refusing it"
done
fresh; fake_lease; jq '.dynconfig = ""' "$FAKE_DIR/lease.json" > "$FAKE_DIR/l.tmp" && mv "$FAKE_DIR/l.tmp" "$FAKE_DIR/lease.json"
eq "lease_read, compose lease with dynconfig \"\": passes, as before (dynconfig_check refuses it before an edit)" "$(lr "$FAKE_DIR/lease.json" | cut -c1-3; echo "rc ${PIPESTATUS[0]}")" "set"$'\n'"rc 0"
has "the guest's ssh: -- before the destination (faults/lib.sh, gsh)" "$(grep -A3 '^gsh()' faults/lib.sh)" '-- "$GUEST_SSH" "$@"'
LEASE_SSH=root@10.254.3.2 fr raun-ht8 FAKE_SSH_EXIT=255
eq "microVM lease, guest unreachable: soft assertions null, verdict unchanged" "$(jq -c '[.result, .softasserts]' "$j" 2>&1)" \
  '["ADVANCE",{"before":null,"during":null,"drain":null,"after":null}]'
has "microVM lease, guest unreachable: the log says why" "$out" "soft assertions, before window: "
eq "microVM lease, guest unreachable, no tag in the lease: server_tag null" "$(jq -c .server_tag "$j" 2>&1)" null
fr raun-ht9 FAKE_SIGNAL_HIGH=0
eq "fault never lands: during closes at the end, drain and after never opened" \
  "$(jq -r .result "$j" 2>&1):$(sa_view)" \
  'BLOCK:{"before":[1,0,null,["loadedTasks went negative"]],"during":[2,1,null,["completed task was already acked","loadedTasks went negative"]],"drain":null,"after":null}'
# The vm mechanism (disk-slow): a token bucket on a microVM cell's drive through laugd's POST /vmfault. The fake curl
# keeps each body laugd was sent, one line each, and the last drive body in vmdrive.json, so the signal EXCEEDs while
# its ops bucket is on. A row with backend: firecracker, and a vm mechanism, each refuse a compose lease before the
# load.
VM_WHEN='vmdrive.json|.rate_limiter.ops.size > 0'
LEASE_SSH=root@10.254.3.2 LEASE_BACKEND=firecracker FR_FAULT=test-disk-slow fr raun-vm1 FAKE_SIGNAL_WHEN="$VM_WHEN"
eq "vm drive, microVM lease: ADVANCE, exit 0" "$(tail -1 <<<"$out"):$rc" "ADVANCE:0"
eq "vm drive, microVM lease: two POSTs to /vmfault, inject 100 ops a second and no bandwidth bucket, remove both at 0" \
  "$(jq -c . "$FAKE_DIR/vmfault.jsonl" 2>&1 | paste -sd '|')" \
  '{"id":"abcdef12","kind":"drive","body":{"drive_id":"rootfs","rate_limiter":{"ops":{"size":100,"refill_time":1000}}}}|{"id":"abcdef12","kind":"drive","body":{"drive_id":"rootfs","rate_limiter":{"bandwidth":{"size":0,"refill_time":0},"ops":{"size":0,"refill_time":0}}}}'
eq "vm drive, microVM lease: the raun JSON's mechanisms hold each request and laugd's answer" \
  "$(jq -c '[.mechanisms[] | [.step, .mechanism, .request.body.rate_limiter.ops.size, .answer.ok, .curl_exit]]' "$j" 2>&1)" \
  '[["inject","vm drive ops=100",100,true,0],["remove","vm drive off",0,true,0]]'
LEASE_BACKEND=compose FR_FAULT=test-disk-slow fr raun-vm2 FAKE_SIGNAL_WHEN="$VM_WHEN"
eq "vm drive, compose lease: refused before the load, exit 2, nothing sent" \
  "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load):$([[ -e "$FAKE_DIR/vmfault.jsonl" ]] && echo sent || echo none)" "2:no-load:none"
has "vm drive, compose lease: says why, in one line" "$out" \
  "test-disk-slow runs on microVM cells only: the vm mechanism is a Firecracker drive limiter through laugd"
yq '.faults["test-bad"] = .faults["test-latency"] + {"backend": "firecracker"}' "$td/catalog.yaml" > "$work/vm-catalog.yaml"
LEASE_BACKEND=compose FR_FAULT=test-bad fr raun-vm3 CATALOG="$work/vm-catalog.yaml"
eq "a row with backend firecracker, a compose lease: refused before the load, exit 2" \
  "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "a row with backend firecracker, a compose lease: says why" "$out" "fault test-bad runs on firecracker cells only; cell abcdef12 is compose"
for bad in "vm drive ops=0" "vm drive iops=100" "vm drive ops=100 ops=200" "vm drive bw=1000" "vm net ops=100"; do
  yq ".faults[\"test-bad\"] = .faults[\"test-disk-slow\"] + {\"inject\": \"$bad\"}" "$td/catalog.yaml" > "$work/vm-catalog.yaml"
  LEASE_SSH=root@10.254.3.2 LEASE_BACKEND=firecracker FR_FAULT=test-bad fr raun-vm4 CATALOG="$work/vm-catalog.yaml"
  eq "vm mechanism '$bad': refused before the load, exit 2, nothing sent" \
    "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load):$([[ -e "$FAKE_DIR/vmfault.jsonl" ]] && echo sent || echo none)" "2:no-load:none"
  has "vm mechanism '$bad': the usage line" "$out" "usage: vm drive ops=<n> [bw=<bytes>], or vm drive off"
done
LEASE_SSH=root@10.254.3.2 LEASE_BACKEND=firecracker FR_FAULT=test-disk-slow fr raun-vm5 FAKE_SIGNAL_WHEN="$VM_WHEN" \
  FAKE_VMFAULT_FAIL="backend vmfault abcdef12 drive: exit status 1: laug · fc vmfault abcdef12 b0: no VMM for block 0"
eq "vm drive, laugd refuses the inject: BLOCK, exit 2, the remove still sent, laugd's answer recorded" \
  "$rc:$(jq -c '[.result, .inject_error, [.mechanisms[] | [.step, .curl_exit, (.answer.error | contains("no VMM for block 0"))]]]' "$j" 2>&1)" \
  "2:[\"BLOCK\",\"'vm drive ops=100' exited 2\",[[\"inject\",22,true],[\"remove\",22,true]]]"
has "vm drive, laugd refuses the inject: says laugd's answer" "$out" \
  "vm drive: laugd at http://127.0.0.1:17700 refused POST /vmfault: backend vmfault abcdef12 drive: exit status 1: laug · fc vmfault abcdef12 b0: no VMM for block 0"
# The dynconfig mechanism (faults/lib.sh) on both kinds of lease: frontend.rps 10 in the cell's own laug.yaml, then
# the template back. The fake signal EXCEEDs while that file says 10, so landed means the edit was made and recovered
# means it was undone. dynconfig checks every role's marker, so every role needs a container here, frontend2 too.
DC5="$work/docker5"; mkdir -p "$DC5"
for r in temporal-frontend temporal-frontend2 temporal-history temporal-matching temporal-worker; do
  if [[ -f "$SA_LOGS/$r.txt" ]]; then cp "$SA_LOGS/$r.txt" "$DC5/"; else sa_start > "$DC5/$r.txt"; fi
done
tmpl="$DYNCONFIG_TEMPLATE"
# Every case here restores the fixture. A real raun or candidate restores the default, so check that it is the template.
eq "faults/lib.sh: with DYNCONFIG_TEMPLATE unset, restore copies cells/compose/dynamicconfig/laug.yaml, which exists" \
  "$(env -u DYNCONFIG_TEMPLATE bash -c 'root="$1"; . "$1/faults/lib.sh"; printf "%s:" "${DYNCONFIG_TEMPLATE#"$root"/}"; [[ -f "$DYNCONFIG_TEMPLATE" ]] && echo exists || echo missing' _ "$root" 2>&1)" \
  "cells/compose/dynamicconfig/laug.yaml:exists"
FR_FAULT=test-dynconfig fr raun-ht15 FAKE_DOCKER_LOGS="$DC5" FAKE_SIGNAL_WHEN='run/dynamicconfig/laug.yaml|.["frontend.rps"][0].value == 10'
eq "dynconfig fault, compose lease: ADVANCE, landed 5 s after the edit" "$(jq -r '.result + " " + ((.landed_at | tonumber) - (.inject_at | tonumber) | tostring)' "$j" 2>&1)" "ADVANCE 5"
has "dynconfig fault, compose lease: every role's marker read before the edit" "$(cat "$FAKE_DIR/docker.args")" \
  $'exec\nfake-temporal-frontend2\ncat\n/etc/temporal/config/dynamicconfig/cell-id'
cmp -s "$dyn" "$tmpl" && pass "dynconfig fault, compose lease: the template is back after the run" \
  || fail "dynconfig fault, compose lease: the template is back after the run" "$(grep -A1 frontend.rps "$dyn" | tail -1)"
LEASE_SSH=root@10.254.3.2 FR_FAULT=test-dynconfig fr raun-ht16 FAKE_DOCKER_LOGS="$DC5" FAKE_SIGNAL_WHEN='guest/dynamicconfig/laug.yaml|.["frontend.rps"][0].value == 10'
eq "dynconfig fault, microVM lease: ADVANCE" "$(jq -r .result "$j" 2>&1)" "ADVANCE"
has "dynconfig fault, microVM lease: the edit runs in the guest" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" "yq -i '.[\"frontend.rps\"][0].value = 10'"
has "dynconfig fault, microVM lease: restore copies the file the cell started with" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" "cp $FAKE_DIR/laug.yaml.orig"
cmp -s "$dyn" "$tmpl" && pass "dynconfig fault, microVM lease: the guest's file is back after the run" \
  || fail "dynconfig fault, microVM lease: the guest's file is back after the run"
FR_FAULT=test-dynconfig fr raun-ht17 FAKE_DOCKER_LOGS="$DC5" FAKE_EXEC_CELL_ID=deadbeef
eq "dynconfig fault, a role mounts another cell's copy: refused, exit 2" "$rc" 2
has "dynconfig fault, refused: says which role" "$out" "temporal-frontend in laug-abcdef12 mounts the dynamic config of cell 'deadbeef', not abcdef12"
cmp -s "$dyn" "$tmpl" && pass "dynconfig fault, refused: the file is untouched" || fail "dynconfig fault, refused: the file is untouched"
LEASE_SSH=root@10.254.3.2 FR_FAULT=test-dynconfig fr raun-ht18 FAKE_DOCKER_LOGS="$DC5" FAKE_EXEC_CELL_ID=deadbeef
eq "dynconfig fault, microVM lease, another cell's copy: refused, exit 2, file untouched" \
  "$rc:$(cmp -s "$dyn" "$tmpl" && echo same || echo changed)" "2:same"
has "dynconfig fault, microVM lease, refused: says so" "$out" "mounts the dynamic config of another cell"

section "shard faults: tdbg close and pg bump (compose and microVM leases, fake clock)"
# The shard draw. Seed 1014 is the catalog's: its first 5 draws are pinned, so a change to shard_draw that would move
# close-shards and stuck-shard to other shards fails here.
sdraw() { ( root="$root"; . "$root/faults/lib.sh"; shard_draw "$@" ) 2>/dev/null; }
eq "shard draw: seed 1014, the first 5 of 64" "$(sdraw 64 1014 | head -5 | paste -sd ' ')" "379 2 203 28 357"
eq "shard draw: 64 distinct ids, all from 1 to 512" "$(sdraw 64 1014 | awk '$1 >= 1 && $1 <= 512' | sort -nu | wc -l)" 64
eq "shard draw: the same seed draws the same shards" "$(sdraw 64 1014 | md5sum)" "$(sdraw 64 1014 | md5sum)"
eq "shard draw: all 512 is every shard once" "$(sdraw 512 1014 | sort -n | paste -sd ' ')" "$(seq 1 512 | paste -sd ' ')"
eq "shard draw: the largest seed works" "$(sdraw 3 2147483646 | wc -l)" 3
for bad in "0 1014" "513 1014" "64 0" "64 2147483647" "64 x"; do
  # shellcheck disable=SC2086  # k and seed as two arguments
  sdraw $bad >/dev/null; eq "shard draw: k and seed '$bad' refused, exit 2" "$?" 2
done
# One cell's logs for the shard faults: DC5's five server roles, plus nginx (the network is read from it) and postgresql
# (the pg mechanism's psql runs in it). sh_logs <dir> [<seconds past T0> <shard id>]...: history also logs "Acquired
# shard" for each pair, in the shape the server's logger builds (faults/lib.sh, shards_acquired).
acq_line() {
  printf '%s\t{"level":"info","ts":"%s","msg":"Acquired shard","shard-id":%s,"address":"192.0.2.5:7234","logging-call-at":"context_impl.go:2005"}\n' \
    "$1" "$("$REAL_DATE" -u -d "@$(( T0 + $1 ))" +%Y-%m-%dT%H:%M:%S.000Z)" "$2"
}
sh_logs() {
  local d="$1"; shift; mkdir -p "$d"; cp "$DC5"/*.txt "$d/"; sa_start > "$d/temporal-nginx.txt"; sa_start > "$d/postgresql.txt"
  while (( $# >= 2 )); do acq_line "$1" "$2" >> "$d/temporal-history.txt"; shift 2; done
}
# held_lines <dir> <seconds past T0> <shard id>: the three lines a shard logs when its acquire is refused, in the shape
# server 1.31.0 builds them (service/history/shard/context_impl.go:1214-1219, 2041-2062, tagged shard-id; read in source).
held_lines() {
  local ts; ts="$("$REAL_DATE" -u -d "@$(( T0 + $2 ))" +%Y-%m-%dT%H:%M:%S.000Z)"
  printf '%s\t{"level":"error","ts":"%s","msg":"Persistent store operation failure","shard-id":%s,"store-operation":"update-shard","error":"Failed to update shard. Previous range ID: 5; new range ID: 6"}\n' "$2" "$ts" "$3"
  printf '%s\t{"level":"error","ts":"%s","msg":"Error acquiring shard","shard-id":%s,"is-retryable":false,"error":"Failed to update shard. Previous range ID: 5; new range ID: 6"}\n' "$2" "$ts" "$3"
  printf '%s\t{"level":"error","ts":"%s","msg":"Couldn'"'"'t acquire shard","shard-id":%s,"error":"Failed to update shard. Previous range ID: 5; new range ID: 6"}\n' "$2" "$ts" "$3"
} >> "$1/temporal-history.txt"
draw64="$(sdraw 64 1014)"; stuck="$(sdraw 1 1014)"
SH_CLOSED="$work/sh-closed"; args=(); for id in $draw64; do args+=(62 "$id"); done; sh_logs "$SH_CLOSED" "${args[@]}"
SH_CLOSED63="$work/sh-closed63"; sh_logs "$SH_CLOSED63" "${args[@]:2}"   # the first drawn shard never acquires again
SH_RESTORED="$work/sh-restored"; sh_logs "$SH_RESTORED" 85 "$stuck"; held_lines "$SH_RESTORED" 70 "$stuck"
SH_NONE="$work/sh-none"; sh_logs "$SH_NONE"
SH_HOLD="$work/sh-hold"; sh_logs "$SH_HOLD" 70 "$stuck"                  # acquired during the hold, never after removal
SH_HELD="$work/sh-held"; sh_logs "$SH_HELD"; held_lines "$SH_HELD" 65 "$stuck"; held_lines "$SH_HELD" 75 "$stuck"   # refused twice in the hold
WHEN_CLOSED='closed.yaml|length >= 64'; WHEN_BUMPED="pg.yaml|.[\"$stuck\"] == 1005"

# close-shards: tdbg closes 64 shards, the removal counter rises 64, and every closed shard logs "Acquired shard" 2 s
# after inject. Recovery is read at removal, which does nothing.
FR_FAULT=test-close fr raun-sh1 FAKE_DOCKER_LOGS="$SH_CLOSED" FAKE_SIGNAL_WHEN="$WHEN_CLOSED"
eq "close-shards, compose lease: ADVANCE, exit 0" "$(tail -1 <<<"$out"):$rc" "ADVANCE:0"
eq "close-shards: the signal's base, peak and recovery" "$(jq -c '[.signal_base, .signal_peak, .recover, .time_to_recover_s]' "$j" 2>&1)" '[0,500,"shards-acquired","0"]'
eq "close-shards: the 64 drawn shards closed, recorded at inject" \
  "$(jq -c '.mechanisms[0] | [.step, .mechanism, .k, .seed, .network, (.shards | length), (.closed == .shards), .failed, .docker_exit]' "$j" 2>&1)" \
  '["inject","tdbg close 64 1014",64,1014,"fake_cell",64,true,[],0]'
eq "close-shards: the recorded shards are seed 1014's draw" "$(jq -r '.mechanisms[0].shards[]' "$j" 2>&1 | paste -sd ' ')" "$(sort -n <<<"$draw64" | paste -sd ' ')"
eq "close-shards: all 64 acquired again, the last 2 s after inject" \
  "$(jq -c '.shards_acquired | [.from, .shards, (.acquired_s | length), .missing, .last_s, .acquired_before_remove]' "$j" 2>&1)" '["inject",64,64,[],2,null]'
has "close-shards: tdbg runs in the cell's admin-tools image, on its network, never pulling" "$(cat "$FAKE_DIR/docker.args")" \
  $'run\n--rm\n--pull\nnever\n--network\nfake_cell\ntemporalio/admin-tools:1.31.0\nsh\n-c'
has "close-shards: one tdbg call per shard, through the cell's nginx" "$(cat "$FAKE_DIR/docker.args")" \
  'tdbg --address temporal-nginx:7233 shard close-shard --shard-id "$s"'
has "close-shards: the image is checked before the load" "$(cat "$FAKE_DIR/docker.args")" $'image\ninspect\ntemporalio/admin-tools:1.31.0'
has "close-shards: the summary" "$out" 'shards      64 of 64 logged "Acquired shard" since inject, the last 2 s after'
FR_FAULT=test-close fr raun-sh2 FAKE_DOCKER_LOGS="$SH_CLOSED63" FAKE_SIGNAL_WHEN="$WHEN_CLOSED"
eq "close-shards, one shard never acquired again: BLOCK, names it" "$(jq -r '.result + ": " + .reason' "$j" 2>&1)" \
  "BLOCK: did not recover within 90s: 1 of 64 shard(s) logged no \"Acquired shard\" since inject: $(head -1 <<<"$draw64")"
eq "close-shards, one shard missing: recorded" "$(jq -c '.shards_acquired | [(.acquired_s | length), .missing, .last_s]' "$j" 2>&1)" "[63,[$(head -1 <<<"$draw64")],null]"
FR_FAULT=test-close fr raun-sh3 FAKE_DOCKER_LOGS="$SH_CLOSED" FAKE_SIGNAL_WHEN="$WHEN_CLOSED" FAKE_TDBG_FAIL="203 28"
eq "close-shards, tdbg fails on 2 shards: exit 2, a BLOCK that names the inject" "$rc:$(jq -r '.result + ": " + .reason' "$j" 2>&1)" \
  "2:BLOCK: inject failed: 'tdbg close 64 1014' exited 2"
eq "close-shards, tdbg fails: what it did to the cell is in the raun JSON, 62 closed and 2 failed" \
  "$(jq -c '[.inject_error, (.mechanisms[0] | [.step, (.closed | length), (.failed | length)]), .landed_at]' "$j" 2>&1)" \
  "[\"'tdbg close 64 1014' exited 2\",[\"inject\",62,2],\"\"]"
has "close-shards, tdbg fails: says how many closed, and the first error" "$out" "tdbg close: 62 of 64 shards closed (docker run exit 0); failed 203 unable to close Shard Task: fake"
FR_FAULT=test-close fr raun-sh4 FAKE_DOCKER_LOGS="$SH_CLOSED" FAKE_SIGNAL_WHEN="$WHEN_CLOSED" FAKE_DOCKER_NO_IMAGE=1
eq "close-shards, no admin-tools image: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "close-shards, no image: says which" "$out" "tdbg: no temporalio/admin-tools:1.31.0 in the Docker that runs laug-abcdef12"
LEASE_SSH=root@10.254.3.2 FR_FAULT=test-close fr raun-sh5 FAKE_DOCKER_LOGS="$SH_CLOSED" FAKE_SIGNAL_WHEN="$WHEN_CLOSED"
eq "close-shards, microVM lease: ADVANCE, 64 closed, 64 acquired again" \
  "$(jq -c '[.result, (.mechanisms[0].closed | length), (.shards_acquired.acquired_s | length)]' "$j" 2>&1)" '["ADVANCE",64,64]'
has "close-shards, microVM lease: tdbg runs in the guest's Docker" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" \
  "docker run --rm --pull never --network fake_cell temporalio/admin-tools:1.31.0 sh -c"
has "close-shards, microVM lease: history's logs come from the guest" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" \
  "docker logs --since 1791900060 --until 1791900080 fake-temporal-history 2>&1"
# tdbg runs the admin-tools image at the cell's own server tag, read from its history image, as cell.yml pins both.
FR_FAULT=test-close fr raun-sh22 FAKE_DOCKER_LOGS="$SH_CLOSED" FAKE_SIGNAL_WHEN="$WHEN_CLOSED" FAKE_SERVER_TAG=1.32.1
eq "close-shards on a 1.32.1 cell: ADVANCE, server_tag 1.32.1" "$(jq -r '.result + " " + .server_tag' "$j" 2>&1)" "ADVANCE 1.32.1"
has "close-shards on a 1.32.1 cell: tdbg runs temporalio/admin-tools:1.32.1" "$(cat "$FAKE_DIR/docker.args")" \
  $'--network\nfake_cell\ntemporalio/admin-tools:1.32.1\nsh\n-c'

# stuck-shard: psql adds 1000 to shard 379's range_id (5 in the fake table), the signal rises while it is bumped,
# restore puts 5 back at removal, and history logs "Acquired shard" for it 5 s later.
FR_FAULT=test-stuck fr raun-sh6 FAKE_DOCKER_LOGS="$SH_RESTORED" FAKE_SIGNAL_WHEN="$WHEN_BUMPED"
eq "stuck-shard, compose lease: ADVANCE, recovered 5 s after removal" "$(jq -r '.result + " " + .time_to_recover_s' "$j" 2>&1):$rc" "ADVANCE 5:0"
eq "stuck-shard: the bump and the restore, with the range ids" \
  "$(jq -c '[.mechanisms[] | [.step, .mechanism, .shard, .range_id_before, .range_id_bumped, .range_id_restored, .restored]]' "$j" 2>&1)" \
  "[[\"inject\",\"pg bump seed:1014 1000\",$stuck,5,1005,null,null],[\"remove\",\"pg restore\",$stuck,null,1005,5,true]]"
eq "stuck-shard: acquired 5 s after removal, nothing before" \
  "$(jq -c '.shards_acquired | [.from, .acquired_s, .missing, .last_s, .acquired_before_remove]' "$j" 2>&1)" "[\"remove\",{\"$stuck\":5},[],5,{}]"
eq "stuck-shard: the refused acquire in the hold is recorded as held_evidence" "$(jq -c '.shards_acquired.held_evidence' "$j" 2>&1)" \
  "{\"$stuck\":{\"Persistent store operation failure\":1,\"Error acquiring shard\":1,\"Couldn't acquire shard\":1}}"
has "stuck-shard: the summary says the hold was seen" "$out" "held        shard $stuck: Persistent store operation failure 1, Error acquiring shard 1, Couldn't acquire shard 1, in the hold"
eq "stuck-shard: not a self-test" "$(jq -c .self_test "$j" 2>&1)" null
eq "stuck-shard: the cell's range_id is back after the run" "$(yq -r ".[\"$stuck\"]" "$FAKE_DIR/pg.yaml" 2>&1)" 5
has "stuck-shard: one statement bumps and returns both values" "$(cat "$FAKE_DIR/docker.args")" \
  "UPDATE shards SET range_id = range_id + 1000 WHERE shard_id = $stuck RETURNING range_id - 1000, range_id"
has "stuck-shard: restore writes only over the bumped value" "$(cat "$FAKE_DIR/docker.args")" \
  "UPDATE shards SET range_id = 5 WHERE shard_id = $stuck AND range_id = 1005 RETURNING range_id"
has "stuck-shard: psql runs in the cell's postgresql" "$(cat "$FAKE_DIR/docker.args")" $'exec\nfake-postgresql\npsql'
FR_FAULT=test-stuck-norestore fr raun-sh7 FAKE_DOCKER_LOGS="$SH_NONE" FAKE_SIGNAL_WHEN="$WHEN_BUMPED"
eq "stuck-shard-norestore: BLOCK, exit 1, did not recover" "$(jq -r '.result + ": " + .reason' "$j" 2>&1):$rc" \
  "BLOCK: did not recover within 90s: 1 of 1 shard(s) logged no \"Acquired shard\" since removal: $stuck:1"
eq "stuck-shard-norestore: no after window, no sweep" "$(jq -c '[.after, .sweep, .recovered_at]' "$j" 2>&1)" '[{},null,""]'
eq "stuck-shard-norestore: cleanup restored the range_id once the result was decided" \
  "$(jq -c '[.mechanisms[] | [.step, .restored]] | .[1:]' "$j" 2>&1):$(yq -r ".[\"$stuck\"]" "$FAKE_DIR/pg.yaml" 2>&1)" '[["cleanup",true]]:5'
eq "stuck-shard-norestore, no refused acquire in the hold: the self-test is not passed, a BLOCK alone shows nothing" \
  "$(jq -c '.self_test | [.expect, .passed, .why]' "$j" 2>&1)" \
  "[\"did not recover\",false,\"no \\\"Couldn't acquire shard\\\" line in the hold from shard $stuck: nothing shows the bump held\"]"
has "stuck-shard-norestore: the summary says so" "$out" "self-test   NOT passed: no \"Couldn't acquire shard\" line in the hold from shard $stuck"
FR_FAULT=test-stuck-norestore fr raun-sh17 FAKE_DOCKER_LOGS="$SH_HELD" FAKE_SIGNAL_WHEN="$WHEN_BUMPED"
eq "stuck-shard-norestore, refused twice in the hold: BLOCK did not recover, the self-test passed" \
  "$(jq -c '[.result, (.reason | startswith("did not recover")), .self_test.passed, .self_test.held_evidence["'"$stuck"'"]["Couldn'"'"'t acquire shard"]]' "$j" 2>&1):$rc" '["BLOCK",true,true,2]:1'
has "stuck-shard-norestore, held: the summary says passed" "$out" "self-test   passed: a BLOCK that did not recover, and every touched shard logged"
FR_FAULT=test-stuck-norestore fr raun-sh18 FAKE_DOCKER_LOGS="$SH_HELD" FAKE_SIGNAL_WHEN="$WHEN_BUMPED" FAKE_SIGNAL_HIGH=0
eq "stuck-shard-norestore, the bump never lands: BLOCK did not land, the self-test is not passed" \
  "$(jq -c '[.result, (.reason | startswith("did not land")), .self_test.passed]' "$j" 2>&1)" '["BLOCK",true,false]'
FR_FAULT=test-stuck-norestore fr raun-sh8 FAKE_DOCKER_LOGS="$SH_HOLD" FAKE_SIGNAL_WHEN="$WHEN_BUMPED"
eq "stuck-shard-norestore, acquired during the hold only: still BLOCK, the early acquire recorded" \
  "$(jq -c '[.result, .shards_acquired.acquired_before_remove, .shards_acquired.missing]' "$j" 2>&1)" "[\"BLOCK\",{\"$stuck\":10},[$stuck]]"
FR_FAULT=test-stuck fr raun-sh9 FAKE_DOCKER_LOGS="$SH_RESTORED" FAKE_SIGNAL_WHEN="$WHEN_BUMPED" FAKE_PG_MOVED=7
eq "stuck-shard, the row holds another value at restore: exit 2, left as found" "$rc:$(yq -r ".[\"$stuck\"]" "$FAKE_DIR/pg.yaml" 2>&1)" "2:7"
eq "stuck-shard, another value: says what it found, once" "$(grep -c "pg restore: shard $stuck holds range_id 7, not the 1005 the bump wrote; left as it is (it was 5)" <<<"$out")" 1
eq "stuck-shard, another value: the raun JSON is a BLOCK that names the remove, with what restore found" \
  "$(jq -c '[.result, .reason, .remove_error, (.mechanisms[1] | [.step, .restored, .range_id_found]), .recovered_at]' "$j" 2>&1)" \
  "[\"BLOCK\",\"remove failed: 'pg restore' exited 2; the fault may still be on the cell, so recovery was not read\",\"'pg restore' exited 2\",[\"remove\",false,7],\"\"]"
FR_FAULT=test-stuck fr raun-sh19 FAKE_DOCKER_LOGS="$SH_RESTORED" FAKE_SIGNAL_WHEN="$WHEN_BUMPED" FAKE_SIGNAL_HIGH=0
eq "stuck-shard, the bump never lands: BLOCK, and the restore it owed ran before the JSON, so it is recorded" \
  "$(jq -c '[.result, (.reason | startswith("did not land")), [.mechanisms[] | [.step, .mechanism, .restored]], .remove_error]' "$j" 2>&1):$(yq -r ".[\"$stuck\"]" "$FAKE_DIR/pg.yaml" 2>&1):$rc" \
  '["BLOCK",true,[["inject","pg bump seed:1014 1000",null],["remove","pg restore",true]],null]:5:1'
FR_FAULT=test-stuck fr raun-sh20 FAKE_DOCKER_LOGS="$SH_RESTORED" FAKE_SIGNAL_WHEN="$WHEN_BUMPED" FAKE_PSQL_BUMP_EXIT=255
eq "stuck-shard, psql's session drops after the bump committed: exit 2, the inject and the unknown bump recorded" \
  "$(jq -c '[.reason, (.mechanisms[] | select(.step == "remove") | [.bump, .delta, .range_id_found, .restored])]' "$j" 2>&1):$rc" \
  "[\"inject failed: 'pg bump seed:1014 1000' exited 2; remove failed: 'pg restore' exited 2\",[\"unknown\",1000,1005,false]]:2"
has "stuck-shard, psql drops: says the bump may have committed" "$out" "pg restore: shard $stuck's bump of 1000 may have committed (its psql call failed); it holds range_id 1005; left as it is"
FR_FAULT=test-stuck fr raun-sh21 FAKE_DOCKER_LOGS="$SH_RESTORED" FAKE_SIGNAL_WHEN="$WHEN_BUMPED" FAKE_PROM_FAIL_FROM=150
eq "stuck-shard, Prometheus gone for the after window: the trap writes a partial raun JSON with the bump and restore" \
  "$(jq -c '[.partial, .result, (.exit != 0), [.mechanisms[] | [.step, .mechanism]]]' "$j" 2>&1):$(yq -r ".[\"$stuck\"]" "$FAKE_DIR/pg.yaml" 2>&1):$(( rc != 0 ))" \
  '[true,"BLOCK",true,[["inject","pg bump seed:1014 1000"],["remove","pg restore"]]]:5:1'
has "stuck-shard, Prometheus gone: says where the partial JSON is" "$out" "partial raun JSON at $FAKE_DIR/results/raun-sh21.json"
FR_FAULT=test-stuck fr raun-sh10 FAKE_DOCKER_LOGS="$DC5" FAKE_SIGNAL_WHEN="$WHEN_BUMPED"
eq "stuck-shard, no postgresql container: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "stuck-shard, no postgresql: says so" "$out" "expected one postgresql container in laug-abcdef12, found: none"
LEASE_SSH=root@10.254.3.2 FR_FAULT=test-stuck fr raun-sh11 FAKE_DOCKER_LOGS="$SH_RESTORED" FAKE_SIGNAL_WHEN="$WHEN_BUMPED"
eq "stuck-shard, microVM lease: ADVANCE, the guest's range_id back" "$(jq -r .result "$j" 2>&1):$(yq -r ".[\"$stuck\"]" "$FAKE_DIR/pg.yaml" 2>&1)" "ADVANCE:5"
has "stuck-shard, microVM lease: psql runs in the guest's postgresql" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" \
  "docker exec fake-postgresql psql -X -A -t -q -v ON_ERROR_STOP=1 -U temporal -d temporal -c UPDATE\\ shards\\ SET\\ range_id\\ =\\ range_id\\ +\\ 1000"
FR_FAULT=test-close fr raun-sh14 FAKE_DOCKER_LOGS="$SH_CLOSED" FAKE_SIGNAL_NULL=1
eq "signal_base, no reading before inject: BLOCK, nothing injected" \
  "$(jq -r '.result + ": " + .reason' "$j" 2>&1):$(grep -c '^tdbg-close$' "$FAKE_DIR/docker.args")" \
  "BLOCK: no signal reading before inject: custom read null, so a rise cannot be measured:0"
FR_FAULT=test-close fr raun-sh15 FAKE_DOCKER_LOGS="$SH_CLOSED" FAKE_SIGNAL_WHEN="$WHEN_CLOSED" FAKE_DOCKER_FAIL=temporal-history@1791900060
eq "close-shards, history's logs unreadable: not recovered, BLOCK" "$(jq -r '.reason + " | " + (.shards_acquired.acquired_s | tostring)' "$j" 2>&1)" \
  "did not recover within 90s: history's logs could not be read for \"Acquired shard\" since inject | null"
FR_FAULT=test-stuck-norestore fr raun-sh16 FAKE_DOCKER_LOGS="$SH_NONE" FAKE_SIGNAL_WHEN="$WHEN_BUMPED" FAKE_PG_MOVED=7
eq "stuck-shard-norestore, cleanup finds another value: BLOCK as before, the cleanup failure recorded" \
  "$(jq -c '[.result, .cleanup_error, (.mechanisms[1] | [.step, .restored, .range_id_found])]' "$j" 2>&1):$rc" \
  "[\"BLOCK\",\"'pg restore' exited 2\",[\"cleanup\",false,7]]:1"
# The catalog's fields are checked before anything runs.
yq '.faults["test-bad"] = .faults["test-latency"] + {"recover": "shards-acquired", "recover_from": "inject"}' "$td/catalog.yaml" > "$work/bad-catalog.yaml"
FR_FAULT=test-bad fr raun-sh12 CATALOG="$work/bad-catalog.yaml"
eq "recover: shards-acquired with a toxic inject: refused, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "recover: shards-acquired with a toxic inject: says why" "$out" "needs a tdbg or pg inject to name the shards"
yq '.faults["test-bad"] = .faults["test-latency"] + {"inject": "tdbg close 64"}' "$td/catalog.yaml" > "$work/bad-catalog.yaml"
FR_FAULT=test-bad fr raun-sh13 CATALOG="$work/bad-catalog.yaml" FAKE_DOCKER_LOGS="$SH_CLOSED"
eq "tdbg close without a seed: refused before the load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
has "tdbg close without a seed: names the usage" "$out" "usage: tdbg close <k> <seed>"

section "faults/catalog.yaml: shard faults"
fc() { yq -r ".faults[\"$1\"].$2 // \"\"" faults/catalog.yaml; }
eq "close-shards: 64 shards from seed 1014, nothing to remove, recovered by Acquired shard since inject" \
  "$(fc close-shards inject) | $(fc close-shards remove) | $(fc close-shards recover) $(fc close-shards recover_from) | $(fc close-shards signal_base)" \
  "tdbg close 64 1014 | none | shards-acquired inject | inject"
eq "close-shards: landed on the removal counter, not shard_closed_count or numshards_gauge" "$(fc close-shards signal_query)" \
  'sum(sharditem_removed_count{job="temporal-history"}) or vector(0)'
eq "stuck-shard: one seeded shard, held 120 s, restored, recovered by Acquired shard since removal" \
  "$(fc stuck-shard inject) | $(fc stuck-shard hold_s) | $(fc stuck-shard remove) | $(fc stuck-shard recover) $(fc stuck-shard recover_from)" \
  "pg bump seed:1014 1000 | 120 | pg restore | shards-acquired remove"
eq "stuck-shard-norestore is stuck-shard with remove none, a cleanup restore and a self-test" \
  "$(yq -o=json -I=0 '.faults["stuck-shard-norestore"] | del(.hypothesis, .status, .remove, .cleanup, .self_test)' faults/catalog.yaml)" \
  "$(yq -o=json -I=0 '.faults["stuck-shard"] | del(.hypothesis, .status, .remove)' faults/catalog.yaml)"
eq "stuck-shard-norestore: remove none, cleanup pg restore, passed only as a BLOCK that did not recover" \
  "$(fc stuck-shard-norestore remove) | $(fc stuck-shard-norestore cleanup) | $(fc stuck-shard-norestore self_test)" "none | pg restore | did not recover"
eq "every catalog mechanism is a verb faults/run.sh knows" \
  "$(yq -r '.faults[] | (.inject, .remove, .cleanup) | select(. != null) | split(";")[] | sub("^ +"; "") | split(" ")[0]' faults/catalog.yaml | sort -u | grep -cvxE 'toxic|docker|dynconfig|tdbg|pg|none|serverfi|vm')" 0
eq "tdbg runs the image the cell's own schema setup runs" "$(sed -n 's/^TDBG_IMAGE="\${TDBG_IMAGE:-\(.*\)}"$/\1/p' faults/run.sh)" \
  "$(yq -r '.services["temporal-setup"].image' cells/compose/cell.yml)"

section "faults/run.sh serverfi (the server's persistence fault injection, compose and microVM leases)"
# The fake docker records each compose call's LAUG_FI_*, ENABLE_ES and DYNCONFIG_DIR in compose.env, one block per call,
# and turns the signal on while LAUG_FI_ERROR or LAUG_FI_PROFILE is set. fe <n>: compose call n's environment.
fe() { awk -v n="$1" '/^call$/ { c++; next } c == n' "$FAKE_DIR/compose.env" 2>/dev/null | paste -sd ' '; }
compose_line() { grep -n '^compose$' "$FAKE_DIR/docker.args" 2>/dev/null | sed -n "${1}p" | cut -d: -f1; }
last_line() { grep -nxF -- "$1" "$FAKE_DIR/docker.args" 2>/dev/null | tail -1 | cut -d: -f1; }
dyn="DYNCONFIG_DIR=\$FAKE_DIR/run/dynamicconfig"
FR_FAULT=test-serverfi fr raun-fi1
eq "serverfi: ADVANCE, exit 0" "$(tail -1 <<<"$out"):$rc" "ADVANCE:0"
eq "serverfi: two compose calls, inject and remove" "$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null)" 2
eq "serverfi: inject sets the five LAUG_FI_* of the row, this cell's dynamic config directory, visibility and server tag" "$(fe 1)" \
  "DYNCONFIG_DIR=$FAKE_DIR/run/dynamicconfig ENABLE_ES=false LAUG_FI_ERROR=Timeout LAUG_FI_METHOD=UpdateWorkflowExecution LAUG_FI_RATE=0.01 LAUG_FI_SEED=6001 LAUG_FI_STORE=ExecutionStore SERVER_TAG=1.31.0"
eq "serverfi: remove leaves every LAUG_FI_* unset" "$(fe 2)" "DYNCONFIG_DIR=$FAKE_DIR/run/dynamicconfig ENABLE_ES=false SERVER_TAG=1.31.0"
has "serverfi: compose recreates temporal-history alone, on cell.yml and fi.yml, in the lease's project" "$(cat "$FAKE_DIR/docker.args")" \
  "$(printf '%s\n' compose -p laug-abcdef12 --project-directory "$root/cells/compose" -f "$root/cells/compose/cell.yml" -f "$root/cells/compose/fi.yml" up -d --no-deps --force-recreate temporal-history)"
eq "serverfi: the raun JSON records the seed, the env asked for, what history held after each recreate, the config" \
  "$(jq -c '.server_fi | [.seed, .env.LAUG_FI_ERROR, .history_env_inject.LAUG_FI_SEED, .history_env_inject.TEMPORAL_SERVER_CONFIG_FILE_PATH, (.history_env_remove | keys), .config, .image]' "$j" 2>&1)" \
  '[6001,"Timeout","6001","/etc/temporal/laug-server.yaml",["TEMPORAL_SERVER_CONFIG_FILE_PATH"],"cells/compose/config/laug-server.yaml","temporalio/server:1.31.0"]'
eq "serverfi: config_sha256 is the vendored file's" "$(jq -r .server_fi.config_sha256 "$j" 2>&1)" "$(sha256sum cells/compose/config/laug-server.yaml | cut -d' ' -f1)"
has "serverfi: the summary names it" "$out" "server fi   serverfi on store=ExecutionStore method=UpdateWorkflowExecution error=Timeout rate=0.01 seed=6001, recorded in server_fi"
# A recreated container takes its logs with it: the slice that ends at injection (T0+60) and the one that ends at removal
# (T0+80) are read before the compose call that replaces history.
a="$(last_line 1791900059.999999999)"; b="$(compose_line 1)"
[[ -n "$a" && -n "$b" ]] && (( a < b )) && pass "serverfi: the before window's last slice is read before the inject recreate" \
  || fail "serverfi: the before window's last slice is read before the inject recreate" "slice at line ${a:-none}, compose at ${b:-none}"
a="$(last_line 1791900079.999999999)"; b="$(compose_line 2)"
[[ -n "$a" && -n "$b" ]] && (( a < b )) && pass "serverfi: the during window's last slice is read before the remove recreate" \
  || fail "serverfi: the during window's last slice is read before the remove recreate" "slice at line ${a:-none}, compose at ${b:-none}"
eq "serverfi: soft assertions by window, as with a fault that keeps its container" "$(sa_view)" "$SA_WANT"
eq "serverfi: readings per window, with each window's length" \
  "$(jq -c '.readings | map_values(if . == null then null else [.window_seconds, .update_shard, .acquire_passes] end)' "$j" 2>&1)" \
  '{"before":[60,0,0],"during":[15,0,0],"drain":[30,0,0],"after":[90,0,0]}'
has "serverfi: every [1m] in a reading becomes the window's length" "$(cat "$FAKE_DIR/prom.queries" 2>&1)" \
  "sum(increase(acquire_shards_count[15s])) + 0 * sum(rate(acquire_shards_count[15s]))"
has "serverfi: the summary prints each reading by window" "$out" "reading     update_shard: before 0, during 0, drain 0, after 0"
has "serverfi: the summary says the fault was removed" "$out" "recorded in server_fi; history held"
eq "serverfi: removed, remove_error null" "$(jq -c '[.server_fi.history_env_remove != null, .server_fi.remove_error]' "$j" 2>&1)" '[true,null]'
# serverfi off does nothing until serverfi on has reached compose: the EXIT trap runs the remove whenever a raun stops
# early, and an off there would restart history unasked.
FR_FAULT=test-serverfi fr raun-fi5 FAKE_THRESHOLD_VALUE=100000
eq "serverfi, never steady: BLOCK, and no compose call at all" \
  "$(jq -r '.result + " " + (.reason | split(":")[0])' "$j" 2>&1):$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null || echo 0)" "BLOCK never steady:0"
has "serverfi, never steady: the trap's off says why it did nothing" "$out" "serverfi off: serverfi on never reached docker compose in this raun"
FR_FAULT=test-serverfi fr raun-fi6 FAKE_OMES_BLOCK=0 FAKE_OMES_EXIT=1 FAKE_WAIT_READY=results/load/raun-fi6.json
eq "serverfi, load exits in its first 10 s: exit 2, no compose call" "$rc:$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null || echo 0)" "2:0"
FR_FAULT=test-serverfi fr raun-fi7 FAKE_UNLEASED_FROM=60
eq "serverfi, the cell not leased at injection: exit 2, no compose call" "$rc:$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null || echo 0)" "2:0"
has "serverfi, the cell not leased at injection: says so" "$out" "cell abcdef12 is not leased at http://127.0.0.1:17700/status; refusing to touch it"
# A failed remove is a harness failure (exit 2, remove_error in the raun JSON), and a serverfi off that failed is tried
# once more by the EXIT trap: so the refusal is printed twice, at removal and by the trap.
FR_FAULT=test-serverfi fr raun-fi8 FAKE_UNLEASED_FROM=70
eq "serverfi, the lease lost after injection: exit 2, the off refused at removal and by the trap, one compose call" \
  "$rc:$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null || echo 0):$(grep -c '^cell abcdef12 is not leased at' <<<"$out")" "2:1:2"
eq "serverfi, the lease lost after injection: a BLOCK that names the remove, the refusal in server_fi.remove_error" \
  "$(jq -c '[.result, .remove_error, (.server_fi.remove_error | test("is not leased")), .recovered_at]' "$j" 2>&1)" \
  "[\"BLOCK\",\"'serverfi off' exited 2\",true,\"\"]"
# A fault that does not land is removed before the record is written, so the raun JSON says what the off did.
FR_FAULT=test-serverfi fr raun-fi9 FAKE_SIGNAL_HIGH=0
eq "serverfi, did not land: BLOCK, removed before the JSON, the trap does not remove again" \
  "$(jq -r '.result + " " + (.reason | split(":")[0]) + " " + (.server_fi.history_env_remove | keys | join(","))' "$j" 2>&1):$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null)" \
  "BLOCK did not land TEMPORAL_SERVER_CONFIG_FILE_PATH:2"
eq "serverfi, did not land: the off unset every LAUG_FI_*" "$(fe 2)" "DYNCONFIG_DIR=$FAKE_DIR/run/dynamicconfig ENABLE_ES=false SERVER_TAG=1.31.0"
has "serverfi, did not land: the summary says removed" "$out" "; removed"
eq "serverfi, did not land: during readings only from a landed fault" "$(jq -c '.readings | map_values(. != null)' "$j" 2>&1)" \
  '{"before":true,"during":false,"drain":false,"after":false}'
FR_FAULT=test-serverfi fr raun-fi10 FAKE_SIGNAL_HIGH=0 FAKE_COMPOSE_FAIL_CALL=2
eq "serverfi, did not land and the off failed: remove_error recorded, the trap tries again" \
  "$(jq -c '[.result, .server_fi.history_env_remove, (.server_fi.remove_error | test("compose up failed"))]' "$j" 2>&1):$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null)" \
  '["BLOCK",null,true]:3'
has "serverfi, the off failed: the summary says so" "$out" "; removal failed: serverfi: docker compose up failed"
# Stopped after the removal (Prometheus gone for the after window): the EXIT trap's partial raun JSON carries server_fi.
FR_FAULT=test-serverfi fr raun-fi12 FAKE_PROM_FAIL_FROM=150
eq "serverfi, Prometheus gone for the after window: the partial raun JSON has the server tag and server_fi, removed" \
  "$(jq -c '[.partial, .server_tag, .server_fi.history_env_inject.LAUG_FI_SEED, (.server_fi.history_env_remove != null)]' "$j" 2>&1):$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null)" \
  '[true,"1.31.0","6001",true]:2'
# Ctrl-C while the off recreates history: an off cut short is still owed, so the EXIT trap runs it again and history
# is left with no LAUG_FI_*. Job control (set -m) gives the raun a process group of its own and SIGINT, as the
# candidate's Ctrl-C test does below. The fake compose call 2, the off at removal (T0+80), waits for the signal.
fresh; fake_lease; fr_env raun-fi13; j="$FAKE_DIR/results/raun-fi13.json"
got="$( { set -m
  env "${fr_vars[@]}" FAKE_COMPOSE_BLOCK_CALL=2 \
    faults/run.sh workflow_with_single_noop_activity test-serverfi "$FAKE_DIR/lease.json" > "$work/fi-int.out" 2>&1 &
  pid=$!
  for _ in $(seq 1 400); do [[ -e "$FAKE_DIR/compose.blocked" ]] && break; "$REAL_SLEEP" 0.05; done
  kill -INT -- "-$pid"; wait "$pid"; echo "$?"; } 2>/dev/null )"
eq "serverfi, Ctrl-C in the off: exit 130, the trap ran the off again (call 3), every LAUG_FI_* unset, the fault off" \
  "$got:$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null):$(fe 3):$(cat "$FAKE_DIR/toxic" 2>&1)" \
  "130:3:DYNCONFIG_DIR=$FAKE_DIR/run/dynamicconfig ENABLE_ES=false SERVER_TAG=1.31.0:off"
eq "serverfi, Ctrl-C in the off: the partial raun JSON records the trap's off" \
  "$(jq -c '[.partial, (.server_fi.history_env_remove | keys), .server_fi.remove_error]' "$j" 2>&1)" \
  '[true,["TEMPORAL_SERVER_CONFIG_FILE_PATH"],null]'
eq "serverfi, Ctrl-C in the off: the partial raun JSON names the backend and nesting after server_tag" \
  "$(jq -c '[.backend, .firecracker_version, (.host_virtualized | type), keys_unsorted[4:9]]' "$j" 2>&1)" \
  '["compose",null,"boolean",["server_tag","backend","firecracker_version","cache_type","host_virtualized"]]'
yq '.faults["test-serverfi"].readings = {"Bad-Name": "up"}' "$td/catalog.yaml" > "$work/bad-readings.yaml" 2>/dev/null
FR_FAULT=test-serverfi fr raun-fi11 CATALOG="$work/bad-readings.yaml"
eq "readings that are not lower_case names to PromQL: exit 2 before any load" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
FR_FAULT=test-serverfi fr raun-fi2 FAKE_ENABLE_ES=true
eq "serverfi: an Elasticsearch cell's history is recreated with ENABLE_ES=true both times" \
  "$(fe 1 | grep -o 'ENABLE_ES=[a-z]*'):$(fe 2 | grep -o 'ENABLE_ES=[a-z]*')" "ENABLE_ES=true:ENABLE_ES=true"
# A microVM lease: the same raun, both recreates run by docker compose in the guest over ssh (the fake ssh runs them
# here), on the guest's copy of cells/compose, each in the session that checks the cell-id marker first.
LEASE_SSH=root@10.254.3.2 FR_FAULT=test-serverfi fr raun-fi-guest
gc="$(grep -F 'docker compose' "$FAKE_DIR/ssh.args" 2>/dev/null)"
eq "serverfi, microVM lease: ADVANCE, exit 0, two compose calls, both through ssh" \
  "$(tail -1 <<<"$out"):$rc:$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null):$(grep -c . <<<"$gc")" "ADVANCE:0:2:2"
eq "serverfi, microVM lease: each recreates temporal-history alone, project laug, on the guest's cell.yml and fi.yml" \
  "$(grep -cF -- "docker compose -p laug --project-directory $FAKE_DIR/guest/compose -f cell.yml -f fi.yml up -d --no-deps --force-recreate temporal-history" <<<"$gc")" 2
eq "serverfi, microVM lease: each compose call follows the cell-id check in its own ssh session" \
  "$(grep -cE "cat $FAKE_DIR/guest/dynamicconfig/cell-id .*exit 3; }; cd $FAKE_DIR/guest/compose && env " <<<"$gc")" 2
a="$(grep -n 'exit 3; }; true$' "$FAKE_DIR/ssh.args" 2>/dev/null | head -1 | cut -d: -f1)"; b="$(grep -nF 'docker compose' "$FAKE_DIR/ssh.args" 2>/dev/null | head -1 | cut -d: -f1)"
[[ -n "$a" && -n "$b" ]] && (( a < b )) && pass "serverfi, microVM lease: the cell-id check runs on its own before the first recreate" \
  || fail "serverfi, microVM lease: the cell-id check runs on its own before the first recreate" "check at line ${a:-none}, compose at ${b:-none}"
has "serverfi, microVM lease: inject passes the row's five LAUG_FI_* to the guest's compose" "$(sed -n 1p <<<"$gc")" \
  "LAUG_FI_STORE=ExecutionStore LAUG_FI_METHOD=UpdateWorkflowExecution LAUG_FI_ERROR=Timeout LAUG_FI_RATE=0.01 LAUG_FI_SEED=6001 docker compose"
eq "serverfi, microVM lease: remove passes no LAUG_FI_*, and unsets all six" \
  "$(sed -n 2p <<<"$gc" | grep -oE 'LAUG_FI_[A-Z]+=' | wc -l):$(sed -n 2p <<<"$gc" | grep -oE -- '-u LAUG_FI_[A-Z]+' | wc -l)" "0:6"
eq "serverfi, microVM lease: the guest's compose saw the row's env, the guest's dynamic config directory, the tag" "$(fe 1)" \
  "DYNCONFIG_DIR=$FAKE_DIR/guest/dynamicconfig ENABLE_ES=false LAUG_FI_ERROR=Timeout LAUG_FI_METHOD=UpdateWorkflowExecution LAUG_FI_RATE=0.01 LAUG_FI_SEED=6001 LAUG_FI_STORE=ExecutionStore SERVER_TAG=1.31.0"
eq "serverfi, microVM lease: config_sha256 is the guest's laug-server.yaml's, removed" \
  "$(jq -c '[.server_fi.config_sha256, (.server_fi.history_env_remove | keys), .server_fi.remove_error]' "$j" 2>&1)" \
  "[\"$(sha256sum "$FAKE_DIR/guest/compose/config/laug-server.yaml" | cut -d' ' -f1)\",[\"TEMPORAL_SERVER_CONFIG_FILE_PATH\"],null]"
eq "serverfi, microVM lease: history's env is read in the guest before and after each recreate, its image after each and once for the server tag" \
  "$(grep -c 'docker inspect -f .*Config\.Env' "$FAKE_DIR/ssh.args" 2>/dev/null):$(grep -c 'docker inspect -f .*Config\.Image' "$FAKE_DIR/ssh.args" 2>/dev/null)" "4:3"
hasnt "serverfi, microVM lease: no compose on this host's cells/compose" "$(cat "$FAKE_DIR/docker.args" 2>/dev/null)" "$root/cells/compose"
fic() { # fic <inject> [remove]: a test catalog whose test-serverfi row injects (and removes) with these
  yq ".faults[\"test-serverfi\"].inject = \"$1\" | .faults[\"test-serverfi\"].remove = \"${2:-serverfi off}\"" "$td/catalog.yaml" > "$work/fi-catalog.yaml" 2>/dev/null
}
fic "serverfi on profile=default seed=6008"; FR_FAULT=test-serverfi fr raun-fi3 CATALOG="$work/fi-catalog.yaml"
eq "serverfi profile=default: LAUG_FI_PROFILE and the seed, nothing else" "$(jq -r .result "$j" 2>&1):$(fe 1)" \
  "ADVANCE:DYNCONFIG_DIR=$FAKE_DIR/run/dynamicconfig ENABLE_ES=false LAUG_FI_PROFILE=default LAUG_FI_SEED=6008 SERVER_TAG=1.31.0"
refused() { # refused <name> <inject> <remove> <expected message> [env...]: exit 2 before any load, saying why
  local name="$1" inj="$2" rem="$3" msg="$4"; shift 4
  fic "$inj" "$rem"; FR_FAULT=test-serverfi fr raun-fi-refused CATALOG="$work/fi-catalog.yaml" "$@"
  eq "serverfi refused, $name: exit 2, no load, no compose" \
    "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load):$([[ -e "$FAKE_DIR/compose.env" ]] && echo compose || echo no-compose)" "2:no-load:no-compose"
  has "serverfi refused, $name: says why" "$out" "$msg"
}
ok_row="store=ShardStore method=UpdateShard error=ShardOwnershipLost rate=0.5 seed=6007"
refused "the example name in config.go's comment" "serverfi on store=ShardStore method=UpdateShard error=ShardOwnershipLostError rate=1.0 seed=1" \
  "serverfi off" "error 'ShardOwnershipLostError' is not one the server knows"
refused "seed 0, time-based" "serverfi on ${ok_row/seed=6007/seed=0}" "serverfi off" "seed must be a nonzero integer without a leading zero, got '0'"
refused "no seed" "serverfi on ${ok_row% seed=6007}" "serverfi off" "seed must be a nonzero integer without a leading zero, got ''"
refused "a seed with a leading zero, which sprig reads as octal" "serverfi on ${ok_row/seed=6007/seed=06007}" "serverfi off" \
  "seed must be a nonzero integer without a leading zero, got '06007'"
refused "rate above 1" "serverfi on ${ok_row/rate=0.5/rate=1.5}" "serverfi off" "rate '1.5' is not in (0, 1]"
refused "rate 0" "serverfi on ${ok_row/rate=0.5/rate=0.0}" "serverfi off" "rate '0.0' is not in (0, 1]"
refused "a store the server does not name" "serverfi on ${ok_row/store=ShardStore/store=VisibilityStore}" "serverfi off" "store 'VisibilityStore' is not one of"
refused "a profile with an error" "serverfi on profile=default error=Timeout seed=1" "serverfi off" "profile= takes only seed="
refused "an unknown key" "serverfi on ${ok_row} ratio=0.5" "serverfi off" "unknown key 'ratio'"
refused "on without off" "serverfi on ${ok_row}" "toxic remove latency" "injects with serverfi on and never runs serverfi off"
# The guest's laug-server.yaml is the image's copy: one that is not this checkout's is refused, naming both sums.
mkdir -p "$work/guest-old/config"; cp cells/compose/cell.yml cells/compose/fi.yml "$work/guest-old/"
{ cat cells/compose/config/laug-server.yaml; echo "# an older image's copy"; } > "$work/guest-old/config/laug-server.yaml"
LEASE_SSH=root@10.254.3.2 refused "a microVM image whose laug-server.yaml is not the checkout's" "serverfi on ${ok_row}" "serverfi off" \
  "this checkout's cells/compose/config/laug-server.yaml has $(sha256sum cells/compose/config/laug-server.yaml | cut -d' ' -f1); the image holds another laug-server.yaml, and make fc-image, then make fc-snapshot, builds it again from this one" \
  GUEST_COMPOSE_DIR="$work/guest-old"
has "serverfi refused, a microVM image whose laug-server.yaml is not the checkout's: names the guest's sum" "$out" \
  "has sha256 $(sha256sum "$work/guest-old/config/laug-server.yaml" | cut -d' ' -f1)"
LEASE_SSH=root@10.254.3.2 LEASE_CELL_ID=deadbeef refused "a guest that holds another cell" "serverfi on ${ok_row}" "serverfi off" \
  "serverfi: the guest at root@10.254.3.2 no longer holds cell abcdef12 ("
LEASE_SSH=root@10.254.3.2 refused "a guest that does not answer" "serverfi on ${ok_row}" "serverfi off" \
  "the guest at root@10.254.3.2 did not answer (ssh exit 255)" FAKE_SSH_EXIT=255
mkdir -p "$work/guest-nofi/config"; cp cells/compose/cell.yml "$work/guest-nofi/"; cp cells/compose/config/laug-server.yaml "$work/guest-nofi/config/"
LEASE_SSH=root@10.254.3.2 refused "a microVM image with no fi.yml" "serverfi on ${ok_row}" "serverfi off" \
  "$work/guest-nofi/fi.yml or $work/guest-nofi/config/laug-server.yaml is missing in the guest at root@10.254.3.2" GUEST_COMPOSE_DIR="$work/guest-nofi"
LEASE_CELL_ID=deadbeef refused "a dynamic config directory of another cell" "serverfi on ${ok_row}" "serverfi off" "belongs to cell 'deadbeef', not abcdef12"
refused "a cell on another server tag" "serverfi on ${ok_row}" "serverfi off" \
  "cells/compose/config/laug-server.yaml is the 1.31.0 template and this cell runs 1.32.1" FAKE_SERVER_TAG=1.32.1
# A failed inject is a harness failure like any mechanism's: exit 2, and a raun JSON that names it, with no fault window.
FR_FAULT=test-serverfi fr raun-fi4 FAKE_RECREATE_TAG=1.32.1
eq "serverfi: a recreate that comes back on another image fails the inject: exit 2, a BLOCK that names it, nothing landed" \
  "$rc:$(jq -c '[.result, (.reason | startswith("inject failed: '"'"'serverfi on ")), (.inject_error != null), .landed_at]' "$j" 2>&1)" '2:["BLOCK",true,true,""]'
has "serverfi: and says which image" "$out" "the new temporal-history runs 'temporalio/server:1.32.1', not temporalio/server:1.31.0"
# The lists run.sh checks names against are the ones laug-server.yaml renders with; laug cell fi-template-check holds
# those to the server source.
tpl_list() { grep -F "$1" cells/compose/config/laug-server.yaml | head -1 | grep -oE '"[A-Za-z0-9]+"' | tr -d '"' | sort | xargs; }
run_list() { sed -n "s/^$1=\"\\(.*\\)\"\$/\\1/p" faults/run.sh | tr ' ' '\n' | sort | xargs; }
eq "serverfi: run.sh's error names are laug-server.yaml's" "$(run_list FI_ERRORS)" "$(tpl_list '(has $fiErr (list')"
eq "serverfi: run.sh's store names are laug-server.yaml's" "$(run_list FI_STORES)" "$(tpl_list '(has $fiStore (list')"
# Every serverfi row in the real catalog passes run.sh's own checks (its serverfi_env, read out of run.sh) with a nonzero
# seed and removes with serverfi off. Two rows, one per store and the profile, also run end to end: their signal, inject
# and remove as written, on the test catalog's windows and a 30 s budget. A full run costs about 2 s.
eval "$(sed -n '/^FI_ERRORS=/p; /^FI_STORES=/p; /^in_list() {/p; /^serverfi_env() {/,/^}/p' faults/run.sh)"
rows="$(yq -r '.faults | to_entries[] | select(.value.inject | test("^serverfi ")) | .key' faults/catalog.yaml 2>/dev/null)"
n="$(wc -w <<<"$rows")"; eq "catalog: serverfi rows to check ($n)" "$(( n > 0 ))" 1
for f in $rows; do
  inj="$(yq -r ".faults[\"$f\"].inject" faults/catalog.yaml 2>/dev/null)"; rem="$(yq -r ".faults[\"$f\"].remove" faults/catalog.yaml 2>/dev/null)"
  read -r -a w <<<"$inj"
  eq "catalog row $f: run.sh's checks pass, removed by serverfi off" "$(serverfi_env "${w[@]:1}" >/dev/null 2>&1 && echo ok):$rem" "ok:serverfi off"
done
yq '.defaults = load("load/testdata/catalog.yaml").defaults | .faults[].budget_s = 30' faults/catalog.yaml > "$work/real-catalog.yaml" 2>/dev/null
for f in acquire-lost persistence-chaos; do
  FR_FAULT="$f" fr "raun-cat-$f" CATALOG="$work/real-catalog.yaml"
  eq "catalog row $f: ADVANCE on the fake cell, injected with its seed, removed" \
    "$(jq -r '[.result == "ADVANCE", (.server_fi.env.LAUG_FI_SEED == (.server_fi.seed | tostring)), .server_fi.history_env_remove != null] | all' "$j" 2>&1):$(fe 1 | grep -o 'LAUG_FI_SEED=[0-9]*')" \
    "true:LAUG_FI_SEED=$(yq -r ".faults[\"$f\"].inject" faults/catalog.yaml | grep -oE 'seed=[0-9]+' | cut -d= -f2)"
done

section "server tag (SERVER_TAG)"
# One default everywhere: laug's (version -json), the Makefile's, the three image lines in cell.yml, and
# cells/backend-compose.sh's.
laug_default="$(bin/laug version -json 2>&1 | jq -r .server_tag 2>&1)"
mk_default="$(sed -n 's/^SERVER_TAG_DEFAULT := \([0-9.]*\)$/\1/p' Makefile)"
yml_defaults="$(grep -oE 'temporalio/(server|admin-tools):\$\{SERVER_TAG:-[0-9.]+\}' cells/compose/cell.yml | sed 's/.*:-\([0-9.]*\)}/\1/' | sort | uniq -c | xargs)"
be_default="$(sed -n 's/^server_tag="\${SERVER_TAG:-\([0-9.]*\)}"$/\1/p' cells/backend-compose.sh)"
eq "SERVER_TAG default: laug version -json, Makefile, cell.yml (3 images), backend-compose.sh" \
  "$laug_default:$mk_default:$yml_defaults:$be_default" "1.31.0:1.31.0:3 1.31.0:1.31.0"
eq "cell.yml pins no server or admin-tools image outside SERVER_TAG" "$(grep -cE 'image: temporalio/(server|admin-tools):[0-9]' cells/compose/cell.yml)" 0
# cells/backend-compose.sh create, in a copy of the tree it reads (the compose directory, the dynamic config template and
# a stand-in bin/laug, whose health check its health verb runs, which writes its arguments to laug.args), with the fake
# docker: SERVER_TAG reaches compose and comes back in the create JSON.
fresh
bt="$work/bt"; mkdir -p "$bt/cells/compose/dynamicconfig" "$bt/bin" "$bt/stub"
cp cells/backend-compose.sh "$bt/cells/"; cp cells/compose/dynamicconfig/laug.yaml "$bt/cells/compose/dynamicconfig/"
printf '%s\n' '#!/bin/sh' 'echo "$*" >> "$FAKE_DIR/laug.args"' > "$bt/bin/laug"; chmod +x "$bt/bin/laug"
out="$(env PATH="$FAKE_PATH" LAUG_BLOCK=3 SERVER_TAG=1.32.1 "$bt/cells/backend-compose.sh" create srvtest1 2>/dev/null)"
eq "backend create: the JSON names the tag and the block's ports" "$(jq -c '{server_tag, address, project}' <<<"$out" 2>&1)" \
  '{"server_tag":"1.32.1","address":"127.0.0.1:7263","project":"laug-srvtest1"}'
has "backend create: compose ran with SERVER_TAG 1.32.1" "$(cat "$FAKE_DIR/compose.env" 2>&1)" "SERVER_TAG=1.32.1"
out="$(env PATH="$FAKE_PATH" LAUG_BLOCK=3 "$bt/cells/backend-compose.sh" create srvtest2 2>/dev/null)"
eq "backend create, SERVER_TAG unset: the default" "$(jq -r .server_tag <<<"$out" 2>&1)" "1.31.0"
env PATH="$FAKE_PATH" LAUG_BLOCK=3 SERVER_TAG=latest "$bt/cells/backend-compose.sh" create srvtest3 >/dev/null 2>&1
eq "backend create, SERVER_TAG latest: refused" "$?" 2
# Its health verb, with stand-ins for temporal (first on PATH), shards-ready.sh and softasserts.sh: cluster health, the
# namespace and the shards pass, then one HealthCheck round trip through bin/laug health check on block 3's frontend.
printf '%s\n' '#!/bin/sh' 'exit 0' | tee "$bt/stub/temporal" "$bt/cells/shards-ready.sh" > "$bt/cells/softasserts.sh"
chmod +x "$bt/stub/temporal" "$bt/cells/shards-ready.sh" "$bt/cells/softasserts.sh"
env PATH="$bt/stub:$FAKE_PATH" LAUG_BLOCK=3 "$bt/cells/backend-compose.sh" health srvtest1 >/dev/null 2>&1; rc=$?
eq "backend health: exit 0, bin/laug health check once, on the block's frontend, visible within 10 s" \
  "$rc|$(cat "$FAKE_DIR/laug.args" 2>&1)" "0|health check -address 127.0.0.1:7263 -visibility-wait 10s"
rm -f "$bt/bin/laug"
out="$(env PATH="$FAKE_PATH" LAUG_BLOCK=3 "$bt/cells/backend-compose.sh" create srvtest4 2>&1 >/dev/null)"; rc=$?
eq "backend create, no bin/laug: refused before compose, naming make build" "$rc|$out|$(ls "$bt/cells/run" 2>&1 | xargs)" \
  "2|bin/laug is not built: make build builds it|laug-srvtest1 laug-srvtest2"
# LAUG_COMPOSE_EXTRA: a second compose file after cell.yml on create's compose up, relative to the checkout or absolute;
# set and missing, refused before compose; destroy passes cell.yml alone; unset, create's call is as it was. dc prints
# each docker call on one line, its arguments joined by spaces.
dc() { awk '$0 == "--" { print l; l = ""; next } { l = l (l == "" ? "" : " ") $0 }' "$FAKE_DIR/docker.args" 2>/dev/null; }
printf '%s\n' '#!/bin/sh' 'echo "$*" >> "$FAKE_DIR/laug.args"' > "$bt/bin/laug"; chmod +x "$bt/bin/laug"
cp cells/compose/cpus4.yml "$bt/cells/compose/"
ym() { yq -r "$1" "cells/compose/$2" 2>/dev/null; }
eq "cpus4.yml: every service of cell.yml, each with cpuset 0-3 and no other key; elasticsearch keeps cell.yml's es profile" \
  "$(ym '.services | keys | sort | join(" ")' cpus4.yml):$(ym '[.services[] | keys | join(",")] | unique | join(" ")' cpus4.yml):$(ym '[.services[].cpuset] | unique | join(" ")' cpus4.yml):$(ym '.services.elasticsearch.profiles | join(" ")' cell.yml)" \
  "$(ym '.services | keys | sort | join(" ")' cell.yml):cpuset:0-3:es"
cf="compose -p laug-%s --project-directory $bt/cells/compose -f $bt/cells/compose/cell.yml"
eq "backend create, LAUG_COMPOSE_EXTRA unset: compose up on cell.yml alone, as before" \
  "$(dc | grep -cxF "$(printf "$cf up -d" srvtest2)")" 1
out="$(env PATH="$FAKE_PATH" LAUG_BLOCK=3 LAUG_COMPOSE_EXTRA=cells/compose/cpus4.yml "$bt/cells/backend-compose.sh" create xtest1 2>/dev/null)"; rc=$?
eq "backend create, LAUG_COMPOSE_EXTRA=cells/compose/cpus4.yml: compose up on cell.yml, then cpus4.yml" \
  "$rc:$(dc | grep -cxF "$(printf "$cf -f $bt/cells/compose/cpus4.yml up -d" xtest1)")" "0:1"
eq "backend create, LAUG_COMPOSE_EXTRA set: the JSON gains no key" "$(jq -c 'keys' <<<"$out" 2>&1)" \
  '["address","backend","dynconfig","grafana","id","project","prometheus","server_tag","toxiproxy","ui"]'
env PATH="$FAKE_PATH" LAUG_BLOCK=3 LAUG_COMPOSE_EXTRA="$bt/cells/compose/cpus4.yml" "$bt/cells/backend-compose.sh" create xtest2 >/dev/null 2>&1; rc=$?
eq "backend create, LAUG_COMPOSE_EXTRA absolute: the same two files" \
  "$rc:$(dc | grep -cxF "$(printf "$cf -f $bt/cells/compose/cpus4.yml up -d" xtest2)")" "0:1"
env PATH="$FAKE_PATH" LAUG_BLOCK=3 LAUG_COMPOSE_EXTRA=cells/compose/cpus4.yml "$bt/cells/backend-compose.sh" destroy xtest1 >/dev/null 2>&1; rc=$?
eq "backend destroy, LAUG_COMPOSE_EXTRA set: down on cell.yml alone, by the project" \
  "$rc:$(dc | grep -cxF "$(printf "$cf --profile es down -v --remove-orphans" xtest1)")" "0:1"
# A failed down is destroy's failure: after a release or a failed create, laugd keeps the cell as stuck; its directory stays.
env PATH="$FAKE_PATH" LAUG_BLOCK=3 FAKE_COMPOSE_EXIT=1 "$bt/cells/backend-compose.sh" destroy xtest2 >/dev/null 2>&1
rc=$?; dirs="$(cd "$bt/cells/run" && echo laug-xtest*)"
eq "backend destroy, down exits 1: destroy exits 1, the cell's directory kept; xtest1's, whose down passed, is gone" \
  "$rc|$dirs" "1|laug-xtest2"
n0="$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null)"
out="$(env PATH="$FAKE_PATH" LAUG_BLOCK=3 LAUG_COMPOSE_EXTRA=cells/compose/nope.yml "$bt/cells/backend-compose.sh" create xtest3 2>&1 >/dev/null)"; rc=$?
eq "backend create, LAUG_COMPOSE_EXTRA names no file: exit 2, one line, before compose and the cell's directory" \
  "$rc|$out|$(grep -c '^call$' "$FAKE_DIR/compose.env" 2>/dev/null)|$([[ -e "$bt/cells/run/laug-xtest3" ]] && echo dir || echo no-dir)" \
  "2|LAUG_COMPOSE_EXTRA=cells/compose/nope.yml: no such file|$n0|no-dir"
# laug pool serve and laug load: the tag reaches the pool's backend and the load, and a tag that is not x.y.z stops a
# verb before Docker. The pool runs in a dir of this test's, so it keeps no pid file in the checkout, on a free port,
# with a backend that keeps its create's environment; a stray SERVER_TAG is named and not passed on. ml prints laug
# load's -dry-run line for 5m, as load needs a length.
fresh
ml() { env PATH="$FAKE_PATH" bin/laug load -dry-run -duration 5m "$@" 2>&1; }
mkdir -p "$work/ps"
printf '#!/bin/sh\n[ "$1" = create ] && { env > %s/ps/create.env; printf '"'"'{"id":"%%s"}\\n'"'"' "$2"; }\nexit 0\n' "$work" > "$work/ps/backend.sh"
chmod +x "$work/ps/backend.sh"
( cd "$work/ps" && exec env SERVER_TAG=9.9.9 LAUG_COMPOSE_EXTRA=cells/compose/cpus4.yml "$root/bin/laug" pool serve -listen 127.0.0.1:0 -min 1 -max 1 -backend "$work/ps/backend.sh" -server-tag 1.32.1 ) \
  > "$work/ps/out" 2> "$work/ps/err" & pspid=$!
for _ in $(seq 1 50); do [[ -s "$work/ps/create.env" ]] && break; "$REAL_SLEEP" 0.1; done
kill "$pspid" 2>/dev/null; wait "$pspid" 2>/dev/null
eq "laug pool serve -server-tag 1.32.1: the backend's create gets it, and the stray SERVER_TAG=9.9.9 is named, not passed on" \
  "$(grep -c '^SERVER_TAG=1.32.1$' "$work/ps/create.env" 2>&1)|$(grep -c 'SERVER_TAG=9.9.9 in the environment is not passed on; -server-tag sets it' "$work/ps/err")" "1|1"
eq "laug pool serve: LAUG_COMPOSE_EXTRA reaches the backend's create as the caller has it" \
  "$(grep -c '^LAUG_COMPOSE_EXTRA=cells/compose/cpus4.yml$' "$work/ps/create.env" 2>&1)" 1
has "laug load -server-tag 1.32.1: run-omes.sh gets it" "$(ml -server-tag 1.32.1 -address 127.0.0.1:17233 -prom http://127.0.0.1:19090)" "SERVER_TAG=1.32.1 load/run-omes.sh"
# laug cell the same: the tag reaches compose, a tag that is no release stops before a script, and cells/up.sh, which
# laug cell up execs, refuses -server-fi on a tag laug-server.yaml was not vendored from.
lc() { env PATH="$FAKE_PATH" bin/laug cell "$@" 2>&1; }
has "laug cell up -server-tag 1.32.1: compose gets it" "$(lc up -dry-run -server-tag 1.32.1)" "SERVER_TAG=1.32.1 FRONTEND_PORT="
for v in "cell up" "cell fi-template-check" "cell dynconfig-check -template" "pool serve"; do
  read -r -a w <<<"$v"
  out="$(env PATH="$FAKE_PATH" bin/laug "${w[@]}" -server-tag 1.32 2>&1)"; rc=$?
  eq "laug $v -server-tag 1.32: exit 2, says why" "$rc:$(grep -c -- "-server-tag 1.32: not a release tag, x.y.z" <<<"$out")" "2:1"
done
out="$(lc up -server-fi -server-tag 1.32.1)"; rc=$?
eq "laug cell up -server-fi on 1.32.1: exit 2" "$rc" 2
has "laug cell up -server-fi on 1.32.1: says why" "$out" "compose/config/laug-server.yaml is the 1.31.0 template and this cell would run 1.32.1"
[[ -e "$FAKE_DIR/docker.args" ]] && fail "no target above reached docker" "$(head -3 "$FAKE_DIR/docker.args")" || pass "no target above reached docker"
# laug load with no -server-tag and no lease reads the tag from -project's temporal-history image, and passes none when
# the project has no such container; make load used to pass the Makefile default whatever the cell ran.
lo=(-address 127.0.0.1:17233 -prom http://127.0.0.1:19090 -project laug-local1)
has "laug load, a local 1.32.1 cell: SERVER_TAG read from its history image" \
  "$(FAKE_DOCKER_LOGS="$SA_LOGS" FAKE_SERVER_TAG=1.32.1 FAKE_DOCKER_PROJECT=laug-local1 ml "${lo[@]}")" "SERVER_TAG=1.32.1 load/run-omes.sh"
has "laug load, the image is looked up in -project" "$(cat "$FAKE_DIR/docker.args" 2>&1)" "label=com.docker.compose.project=laug-local1"
has "laug load, no local cell: SERVER_TAG empty, the run JSON records null" \
  "$(FAKE_DOCKER_LOGS="$SA_LOGS" FAKE_DOCKER_PROJECT=laug-other ml "${lo[@]}")" "SERVER_TAG= load/run-omes.sh"
# laug pool serve -backend firecracker runs as root, so nothing here starts it; it keeps make fc-pool's check of the tag,
# ahead of its check of root.
out="$(env PATH="$FAKE_PATH" bin/laug pool serve -backend firecracker -server-tag 1.32.1 2>&1)"; rc=$?
eq "laug pool serve -backend firecracker -server-tag 1.32.1: refused, microVM cells run the image's tag" "$rc:$(grep -c 'microVM cells run server 1.31.0' <<<"$out")" "2:1"

section "laug baseline and laug cell dynconfig-check (compose and microVM leases)"
# A microVM cell's containers run in its guest's Docker as project laug, and this host has no laug-<id>. When the last
# lease (LEASE_FILE) is the cell and names a guest, both verbs run docker there over the fake ssh, each call after
# gcell's cell-id check; otherwise this host's Docker. The fake docker's roles are SA_LOGS's four. bin/laug
# passes no REAL_DATE on, so the PATH here holds the fake docker, ssh and curl, and the real date and sleep.
blp="$work/bl-bin"; mkdir -p "$blp"; for t in docker ssh curl; do ln -sf "$td/fakebin/$t" "$blp/$t"; done
bl() { # bl <run-id> <docker-project> [flag...]: laug baseline on a run of the fake lease's cell, T0 to T0+400; sets out, rc
  local id="$1" p="$2"; shift 2
  jq -n --arg id "$id" --arg s "$("$REAL_DATE" -u -d "@$T0" +%Y-%m-%dT%H:%M:%SZ)" \
    --arg e "$("$REAL_DATE" -u -d "@$(( T0 + 400 ))" +%Y-%m-%dT%H:%M:%SZ)" \
    '{run_id: $id, cell: "127.0.0.1:17233", prometheus: "http://127.0.0.1:19090", project: "laug-abcdef12", start: $s, end: $e}' \
    > "$FAKE_DIR/results/$id.json"
  out="$(env PATH="$blp:$PATH" FAKE_T0=$T0 FAKE_STARTED_AT=2026-10-13T13:59:00Z FAKE_DOCKER_LOGS="$SA_LOGS" FAKE_DOCKER_PROJECT="$p" RESULTS_DIR="$FAKE_DIR/results" \
    LEASE_FILE="$FAKE_DIR/lease.json" FC_GUEST_KEY="$FAKE_DIR/guest.key" FC_GUEST_KNOWN_HOSTS="$FAKE_DIR/guest.known_hosts" \
    bin/laug baseline "$id" "$@" 2>&1)"; rc=$?
}
bl_services() { jq -c '[(.services // {} | keys), .services["temporal-history"]]' "$FAKE_DIR/results/$1.baseline.json" 2>&1; }
BL_WANT='[["temporal-frontend","temporal-history","temporal-matching","temporal-worker"],{"cpu_percent":12.25,"mem_mib":256}]'
fresh; LEASE_SSH=root@10.254.3.2 fake_lease; bl bl-guest laug
eq "laug baseline, a microVM lease of the run's cell: exit 0" "$rc" 0
has "laug baseline, a microVM lease: says it reads the guest" "$out" \
  "reading CPU and memory in cell abcdef12's guest, root@10.254.3.2, over the lease's ssh"
eq "laug baseline, a microVM lease: docker ps and docker stats run in the guest, each after the cell-id check" \
  "$(grep -F cell-id "$FAKE_DIR/ssh.args" 2>/dev/null | grep -cE 'docker (ps -q --filter label=com\.docker\.compose\.project=laug |stats --no-stream )')" 2
eq "laug baseline, a microVM lease: the rows name the services, no project prefix" "$(bl_services bl-guest)" "$BL_WANT"
fresh; LEASE_SSH=root@10.254.3.2 fake_lease; bl bl-skip laug -project ''
eq "laug baseline -project '', a microVM lease: exit 0, no rows, nothing over ssh" \
  "$rc:$(jq -c .services "$FAKE_DIR/results/bl-skip.baseline.json" 2>&1):$([[ -e "$FAKE_DIR/ssh.args" ]] && echo ssh || echo no-ssh)" "0:null:no-ssh"
fresh; fake_lease; bl bl-host laug-abcdef12
eq "laug baseline, a compose lease: exit 0, this host's Docker, nothing over ssh" \
  "$rc:$([[ -e "$FAKE_DIR/ssh.args" ]] && echo ssh || echo no-ssh)" "0:no-ssh"
eq "laug baseline, a compose lease: the rows name the services, laug-abcdef12- taken off" "$(bl_services bl-host)" "$BL_WANT"
# A block's address passes to the next cell on it: a microVM lease at the run's address but of another cell is not the
# run's cell, so the script looks in this host's Docker, finds no laug-abcdef12, and exits 2 with nothing over ssh.
fresh; LEASE_SSH=root@10.254.3.2 fake_lease; jq '.id = "0badcafe"' "$FAKE_DIR/lease.json" > "$FAKE_DIR/lease.tmp" && mv "$FAKE_DIR/lease.tmp" "$FAKE_DIR/lease.json"
bl bl-other laug
eq "laug baseline, a microVM lease of another cell at the run's address: exit 2, nothing over ssh" \
  "$rc:$([[ -e "$FAKE_DIR/ssh.args" ]] && echo ssh || echo no-ssh)" "2:no-ssh"
has "laug baseline, a microVM lease of another cell: names the project it found empty" "$out" \
  "no running containers in compose project laug-abcdef12; PROJECT= skips the per-service rows"
dc() { # dc <docker-project> [flag...]: laug cell dynconfig-check with the fake lease as LEASE_FILE; sets out, rc
  local p="$1"; shift
  out="$(env PATH="$blp:$PATH" FAKE_T0=$T0 FAKE_STARTED_AT=2026-10-13T13:59:00Z FAKE_DOCKER_LOGS="$SA_LOGS" FAKE_DOCKER_PROJECT="$p" LEASE_FILE="$FAKE_DIR/lease.json" \
    FC_GUEST_KEY="$FAKE_DIR/guest.key" FC_GUEST_KNOWN_HOSTS="$FAKE_DIR/guest.known_hosts" bin/laug cell dynconfig-check "$@" 2>&1)"; rc=$?
}
fresh; LEASE_SSH=root@10.254.3.2 fake_lease; dc laug -cell abcdef12
eq "laug cell dynconfig-check -cell, the last lease that cell's guest: exit 0, the first line says the guest" \
  "$rc:$(head -1 <<<"$out")" "0:laug · cell dynconfig-check: cell abcdef12's laug.yaml, in its guest at root@10.254.3.2 over the lease's ssh, with its own server's validator"
has "laug cell dynconfig-check -cell, a microVM lease: the script names the image and the guest" "$out" \
  "temporalio/server:1.31.0 validate-dynamic-config on cell abcdef12's laug.yaml, in its guest at root@10.254.3.2"
has "laug cell dynconfig-check -cell, a microVM lease: the validator runs over ssh in the guest's history container" \
  "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" \
  "docker exec fake-temporal-history temporal-server validate-dynamic-config /etc/temporal/config/dynamicconfig/laug.yaml"
eq "laug cell dynconfig-check -cell, a microVM lease: ps, inspect and exec each after the cell-id check" \
  "$(grep -F cell-id "$FAKE_DIR/ssh.args" 2>/dev/null | grep -cE 'docker (ps|inspect|exec) ')" 3
fresh; LEASE_SSH=root@10.254.3.2 fake_lease; dc laug-0badcafe -cell 0badcafe
eq "laug cell dynconfig-check -cell, the last lease another cell's: this host's Docker, exit 0, nothing over ssh" \
  "$rc:$(head -1 <<<"$out"):$([[ -e "$FAKE_DIR/ssh.args" ]] && echo ssh || echo no-ssh)" \
  "0:laug · cell dynconfig-check: project laug-0badcafe's laug.yaml, with its own server's validator:no-ssh"
has "laug cell dynconfig-check -cell, the last lease another cell's: the validator runs in this host's Docker" \
  "$(tr '\n' ' ' < "$FAKE_DIR/docker.args" 2>/dev/null)" \
  "exec fake-temporal-history temporal-server validate-dynamic-config /etc/temporal/config/dynamicconfig/laug.yaml --"

section "the Firecracker pool: laug pool serve and pool down -backend firecracker, fc-lib.sh's pid file reader"
# No KVM and no root: the verbs' refusals, the sweep's line, the scripts' syntax, and how fc-lib.sh reads the pid file
# laug pool serve keeps, against stand-ins in a scratch checkout: real sleeps whose argv[0] is a whole pool serve
# command line (bash's exec -a), so /proc shows one, and an iptables that finds no rule.
if (( EUID != 0 )); then
  out="$(bin/laug pool serve -backend firecracker -listen 127.0.0.1:0 2>&1)"; rc=$?
  eq "laug pool serve -backend firecracker, not root: refused, exit 2" "$rc:$(grep -c 'runs as root, for /dev/kvm' <<<"$out")" "2:1"
  out="$(bin/laug pool down -backend firecracker -remove-cells 2>&1)"; rc=$?
  eq "laug pool down -backend firecracker -remove-cells, not root: refused, exit 2" \
    "$rc:$(grep -c 'root.s: sudo bin/laug pool down -backend firecracker -remove-cells$' <<<"$out")" "2:1"
else
  pass "the not-root refusals: skipped, this runs as root"
fi
out="$(bin/laug pool down -backend firecracker -remove-cells -dry-run 2>&1)"; rc=$?
eq "laug pool down -backend firecracker -remove-cells -dry-run: the sweep's line" "$rc:$out" "0:cells/backend-firecracker.sh sweep"
for f in cells/backend-firecracker.sh cells/rootfs/*.sh cells/pool-down.sh faults/lib.sh; do
  out="$(bash -n "$f" 2>&1)"; rc=$?
  eq "bash -n $f" "$rc:$out" "0:"
done
fcw="$work/fc-pid"; mkdir -p "$fcw/cells/run" "$fcw/bin" "$fcw/run"
printf '#!/bin/sh\nexit 1\n' > "$fcw/bin/iptables"; chmod +x "$fcw/bin/iptables"
fcl() { env PATH="$fcw/bin:$PATH" LAUG_ROOT="$fcw" RUN_DIR="$fcw/run" FC_DIR="$fcw/fc" bash -c '. cells/rootfs/fc-lib.sh; "$@"' fcl "$@" 2>&1; }
# vmmetrics checks the block's directory before root, so a block with no directory refuses for any user, before any socket.
out="$(env RUN_DIR="$fcw/run" FC_DIR="$fcw/fc" LAUG_BLOCK=7 bash cells/backend-firecracker.sh vmmetrics abcdef12 2>&1)"; rc=$?
eq "backend-firecracker.sh vmmetrics, a block with no directory: refused, exit 1, one line" "$rc:$out" \
  "1:laug · fc vmmetrics abcdef12 b7: block 7 holds no cell abcdef12"
# vmfault checks its kind and body before root, so a bad one refuses for any user, exit 2, one line. The body is a
# rate limiter on rootfs and nothing else: path_on_host would point the root VMM's drive at any host file.
vmbad='vmfault: the body must be {"drive_id": "rootfs", "rate_limiter": {"bandwidth"|"ops": {size, refill_time[, one_time_burst]}}} and nothing else, got'
for c in "net|{}|usage: vmfault <id> drive <json> (drive is the one kind; pause, resume and net are not built)" \
  "drive|[1]|$vmbad '[1]'" "drive|{} {}|$vmbad '{} {}'" \
  "drive|{\"drive_id\":\"rootfs\",\"path_on_host\":\"/x\"}|$vmbad '{\"drive_id\":\"rootfs\",\"path_on_host\":\"/x\"}'" \
  "drive|{\"drive_id\":\"rootfs\",\"path_on_host\":\"/x\",\"rate_limiter\":{\"ops\":{\"size\":1,\"refill_time\":1}}}|$vmbad '{\"drive_id\":\"rootfs\",\"path_on_host\":\"/x\",\"rate_limiter\":{\"ops\":{\"size\":1,\"refill_time\":1}}}'"; do
  IFS='|' read -r kind body want <<<"$c"
  out="$(env RUN_DIR="$fcw/run" FC_DIR="$fcw/fc" LAUG_BLOCK=7 bash cells/backend-firecracker.sh vmfault abcdef12 "$kind" "$body" 2>&1)"; rc=$?
  eq "backend-firecracker.sh vmfault $kind '$body': refused, exit 2, one line" "$rc:$out" "2:$want"
done
# The drive's cache mode pin: any value but Unsafe or Writeback is refused when fc-lib.sh is sourced.
out="$(FC_CACHE_TYPE=bogus fcl true)"; rc=$?
eq "fc-lib.sh: FC_CACHE_TYPE=bogus refused at source time, exit 2, the value and the two allowed" "$rc:$out" \
  "2:FC_CACHE_TYPE=bogus: the drive's cache mode is Unsafe or Writeback"
eq "fc-lib.sh: FC_CACHE_TYPE=Unsafe and the default both source, exit 0" \
  "$(FC_CACHE_TYPE=Unsafe fcl true; echo "Unsafe $?"; fcl true; echo "default $?")" $'Unsafe 0\ndefault 0'
# sum_check, which build-image.sh runs on each download: a file whose sha256 is known, against a wrong sum and the right one.
printf 'laug\n' > "$fcw/sum.txt"; sum_ok=$(sha256sum "$fcw/sum.txt" | cut -d' ' -f1); sum_bad=$(printf '%064d' 0)
out="$(fcl sum_check "$fcw/sum.txt" "$sum_bad")"; rc=$?
eq "fc-lib.sh sum_check, a wrong sum: exit 2, one line naming the file, the sum found and the sum pinned" "$rc:$out" \
  "2:$fcw/sum.txt: sha256 $sum_ok, pinned $sum_bad; refusing it"
out="$(fcl sum_check "$fcw/sum.txt" "$sum_ok")"; rc=$?
eq "fc-lib.sh sum_check, the right sum: exit 0, silent" "$rc:$out" "0:"
# Firecracker's pin (SECURITY.md): the release's .sha256.txt must say fc-lib.sh's FC_SHA256_TGZ, or the build stops
# before it fetches the tarball, and fetch then holds the tarball to the same pin. Step 1's Firecracker lines and fetch,
# read out of build-image.sh, run in a scratch directory under its set -euo pipefail. A stub curl serves a stub release
# from a directory (a tarball of the two binaries and its .sha256.txt) and logs each URL. No network, no root.
# shellcheck disable=SC2016  # expanded by fc-lib.sh's bash
fcv=$(fcl eval 'echo "$FC_VERSION"') fpin=$(FC_SHA256_TGZ='' fcl eval 'echo "$FC_SHA256_TGZ"')
ftgz="firecracker-$fcv-x86_64.tgz"; frel="https://github.com/firecracker-microvm/firecracker/releases/download/$fcv/$ftgz"
mkdir -p "$fcw/fbin" "$fcw/rel/release-$fcv-x86_64" "$fcw/rel-swap"
cat > "$fcw/fbin/curl" <<'EOF2'
#!/bin/sh
# curl [-flags] [-o <file>] <url>: $FAKE_REL's file named by the URL's last part, to <file> or stdout. None there: exit
# 22, curl -f's exit for a 404 (curl 8.5.0). Each URL goes to $FAKE_CURL_LOG.
o=""; u=""
while [ $# -gt 0 ]; do case $1 in -o) o=$2; shift 2 ;; -*) shift ;; *) u=$1; shift ;; esac; done
echo "$u" >> "$FAKE_CURL_LOG"
f="$FAKE_REL/${u##*/}"
[ -f "$f" ] || exit 22
if [ -n "$o" ]; then cp "$f" "$o"; else cat "$f"; fi
EOF2
for b in firecracker jailer; do printf '#!/bin/sh\necho "%s %s"\n' "$b" "$fcv" > "$fcw/rel/release-$fcv-x86_64/$b-$fcv-x86_64"; done
chmod +x "$fcw/fbin/curl" "$fcw/rel/release-$fcv-x86_64/"*
tar -czf "$fcw/rel/$ftgz" -C "$fcw/rel" "release-$fcv-x86_64"; fsum=$(sha256sum "$fcw/rel/$ftgz" | cut -d' ' -f1)
printf '%s  %s\n' "$fsum" "$ftgz" > "$fcw/rel/$ftgz.sha256.txt"
# rel-swap: the same .sha256.txt beside another tarball.
cp "$fcw/rel/$ftgz.sha256.txt" "$fcw/rel-swap/"; printf 'another\n' | gzip > "$fcw/rel-swap/$ftgz"
ssum=$(sha256sum "$fcw/rel-swap/$ftgz" | cut -d' ' -f1)
# fcb <dir> <release> [<pin>]: those lines in <dir> on <release>, with FC_SHA256_TGZ=<pin>, or fc-lib.sh's without one.
# They are bash -c's own script, as in the build, not an eval or a function: there a failed && list would end the run.
# Each piece runs from the line matching its anchor to its end. A build-image.sh with no line matching an anchor fails
# a check naming it, and fcb prints that line in place of running the lines, so the three cases name it too.
ffetch=$(sed -n '/^fetch() {/,/^}/p' cells/rootfs/build-image.sh)
fhave=$(sed -n '/^have=/,/^fi$/p' cells/rootfs/build-image.sh)
fmiss=""
fpiece() { # fpiece <anchor> <lines>: a check that <lines>, read from build-image.sh at <anchor>, are there
  if [[ -n $2 ]]; then
    pass "build-image.sh has a line matching $1, where one of the two pieces the pin's cases run starts"; return
  fi
  fail "build-image.sh has no line matching $1"; fmiss+="build-image.sh has no line matching $1"$'\n'
}
fpiece '^fetch() {' "$ffetch"; fpiece '^have=' "$fhave"
# shellcheck disable=SC2016  # expanded by the inner bash
fbs=$(printf '%s\n' 'set -euo pipefail; . cells/rootfs/fc-lib.sh' "$ffetch" 'cd "$1"' "$fhave")
fcb() {
  : > "$fcw/curl.log"; mkdir -p "$1"
  [[ -z $fmiss ]] || { printf '%s' "$fmiss"; return 1; }
  env FC_SHA256_TGZ="${3:-}" PATH="$fcw/fbin:$PATH" FAKE_REL="$2" FAKE_CURL_LOG="$fcw/curl.log" FC_DIR="$fcw/fc" \
    bash -c "$fbs" fcb "$1" 2>&1
}
out="$(fcb "$fcw/fcb-a" "$fcw/rel")"; rc=$?
eq "build-image.sh, a tarball whose sum is not fc-lib.sh's pin: exit 2, the pin's line, only the .sha256.txt fetched, nothing left" \
  "$rc|$out|$(paste -sd ' ' "$fcw/curl.log")|$(ls -A "$fcw/fcb-a")" \
  "2|the release's $ftgz.sha256.txt says $fsum, fc-lib.sh pins $fpin; refusing both|$frel.sha256.txt|"
out="$(fcb "$fcw/fcb-b" "$fcw/rel" "$fsum")"; rc=$?
eq "build-image.sh, a pin equal to the tarball's sum: exit 0, silent, the tarball fetched, its two binaries in place, fc.tgz gone" \
  "$rc|$out|$(paste -sd ' ' "$fcw/curl.log")|$(ls -A "$fcw/fcb-b")|$("$fcw/fcb-b/firecracker"; "$fcw/fcb-b/jailer")" \
  "0||$frel.sha256.txt $frel|firecracker"$'\njailer\n'"release-$fcv-x86_64|firecracker $fcv"$'\n'"jailer $fcv"
out="$(fcb "$fcw/fcb-c" "$fcw/rel-swap" "$fsum")"; rc=$?
eq "build-image.sh, the .sha256.txt says the pin but the tarball is another: exit 2, fetch's line, no tarball or .part left" \
  "$rc|$out|$(paste -sd ' ' "$fcw/curl.log")|$(ls -A "$fcw/fcb-c")" \
  "2|fc.tgz.part: sha256 $ssum, pinned $fsum; refusing it|$frel.sha256.txt $frel|"
# jail_argv, the jailer's command line for a pool VMM, one argument a line: the uid is 20000 plus the block, the gid
# 20000 for every cell, the paths after -- inside the jail. An FC_DIR with a blank stays one argument. A long FC_DIR is
# not refused: the host reaches the jail's socket through a link, whose target sun_path does not hold.
fja() { env FC_DIR="$1" bash -c '. cells/rootfs/fc-lib.sh; jail_argv "$1"' fja "$2" 2>&1; }
jtail=$'--cgroup-version\n2\n--parent-cgroup\nlaug\n--cgroup\ncpu.weight=100\n--cgroup\nmemory.max=max\n--\n--api-sock\n/run/firecracker.socket\n--enable-pci\n--log-path\n/vmm.log\n--metrics-path\n/metrics.json'
for c in "/opt/fc|0|20000" "/opt/fc|250|20250" "/srv/laug fc|7|20007"; do
  IFS='|' read -r jd jb ju <<<"$c"
  out="$(fja "$jd" "$jb")"; rc=$?
  eq "fc-lib.sh jail_argv $jb, FC_DIR=$jd: exit 0, the jailer's argv byte for byte" "$rc:$out" \
    "0:$jd/jailer"$'\n--id\n'"laug-b$jb"$'\n--exec-file\n'"$jd/firecracker"$'\n--uid\n'"$ju"$'\n--gid\n20000\n--chroot-base-dir\n'"$jd/jail"$'\n--new-pid-ns\n'"$jtail"
done
jd="/srv/$(printf 'x%.0s' $(seq 1 48))"
out="$(fja "$jd" 250)"; rc=$?
eq "jail_argv 250, an FC_DIR of 53 bytes (the jail's socket 108 bytes as the host names it): exit 0" \
  "$rc:$(sed -n 2,3p <<<"$out" | paste -sd ' ')" "0:--id laug-b250"
for jb in 251 -1 x ""; do
  out="$(fja /opt/fc "$jb")"; rc=$?
  eq "jail_argv '$jb': refused, exit 2, one line" "$rc:$out" "2:jail_argv: the block is 0 to 250, got '$jb'"
done
# FC_JAIL, the jailer knob: 1 (the default) or 0; anything else is refused when fc-lib.sh is sourced.
out="$(FC_JAIL=2 fcl true)"; rc=$?
eq "fc-lib.sh: FC_JAIL=2 refused at source time, exit 2, one line" "$rc:$out" \
  "2:FC_JAIL=2: 1 starts each pool VMM through the jailer, 0 as root without it"
eq "fc-lib.sh: FC_JAIL=1, FC_JAIL=0 and the default all source, exit 0" \
  "$(FC_JAIL=1 fcl true; echo "1 $?"; FC_JAIL=0 fcl true; echo "0 $?"; fcl true; echo "default $?")" $'1 0\n0 0\ndefault 0'
eq "fc-lib.sh: FC_JAIL unset reads 1, the jailer; an empty FC_JAIL too; FC_JAIL=0 reads 0" \
  "$(env -u FC_JAIL bash -c '. cells/rootfs/fc-lib.sh; echo "$FC_JAIL"'; FC_JAIL= fcl eval 'echo "$FC_JAIL"'; FC_JAIL=0 fcl eval 'echo "$FC_JAIL"')" $'1\n1\n0'
# jail_check, prep's checks for FC_JAIL=1, on stand-ins: a jailer in FC_DIR that prints its version line, a findmnt
# that gives the mount options (FAKE_MNT) and the cgroup mount's type (FAKE_CGFS), a getent that gives a passwd or a
# group entry (FAKE_PW, FAKE_GR). Each refusal is exit 2 and one line, in the check's order; the checks after these
# read the host's cgroup controllers, the jailer's paths and FC_DIR's parents, and are not run here.
mkdir -p "$fcw/jbin" "$fcw/fc"
cat > "$fcw/jbin/findmnt" <<'EOF2'
#!/bin/sh
case " $* " in *" FSTYPE "*) echo "${FAKE_CGFS-cgroup2}" ;; *) echo "${FAKE_MNT-rw,relatime}" ;; esac
EOF2
cat > "$fcw/jbin/getent" <<'EOF2'
#!/bin/sh
case $1 in passwd) [ -n "$FAKE_PW" ] && echo "$FAKE_PW" ;; group) [ -n "$FAKE_GR" ] && echo "$FAKE_GR" ;; esac
exit 0
EOF2
chmod +x "$fcw/jbin/findmnt" "$fcw/jbin/getent"
jck() { env "$@" PATH="$fcw/jbin:$fcw/bin:$PATH" LAUG_ROOT="$fcw" RUN_DIR="$fcw/run" FC_DIR="$fcw/fc" bash -c '. cells/rootfs/fc-lib.sh; jail_check' 2>&1; }
out="$(jck)"; rc=$?
eq "jail_check, no jailer in FC_DIR: refused, exit 2, one line" "$rc:$out" \
  "2:no $fcw/fc/jailer; run make fc-image, which fetches it beside firecracker, or set FC_JAIL=0 to run the pool without it"
printf '#!/bin/sh\nprintf "Jailer v1.16.0\\n\\n"\n' > "$fcw/fc/jailer"; chmod +x "$fcw/fc/jailer"
out="$(jck)"; rc=$?
eq "jail_check, a jailer of another version: refused, exit 2, both versions" "$rc:$out" \
  "2:$fcw/fc/jailer is v1.16.0, not v1.17.0; run make fc-image, or set FC_JAIL=0 to run the pool without it"
printf '#!/bin/sh\nprintf "Jailer v1.17.0\\n\\n"\n' > "$fcw/fc/jailer"
for o in rw,nosuid,nodev,relatime rw,noexec; do
  out="$(jck FAKE_MNT=$o)"; rc=$?
  eq "jail_check, the jails' mount $o: refused, exit 2, one line" "$rc:$out" \
    "2:$fcw/fc/jail is on a mount with $o: a jail needs device nodes and exec; set FC_DIR on another filesystem"
done
out="$(jck FAKE_PW='someone:x:20007:20007::/nonexistent:/bin/sh')"; rc=$?
eq "jail_check, a passwd entry for a jail's uid: refused, exit 2, the uid and the name" "$rc:$out" \
  "2:uid 20007 has a passwd entry, someone; the jails take uids 20000 to 20250"
out="$(jck FAKE_GR='staff2:x:20000:someone')"; rc=$?
eq "jail_check, a group entry for gid 20000: refused, exit 2, the group" "$rc:$out" \
  "2:gid 20000 has a group entry, staff2; every jailed VMM reads vm.mem as that gid"
out="$(jck FAKE_CGFS=tmpfs)"; rc=$?
eq "jail_check, /sys/fs/cgroup not cgroup v2: refused, exit 2, one line" "$rc:$out" \
  "2:/sys/fs/cgroup is not cgroup v2; the jails' cgroups need it"
# guest_freeze and guest_thaw, which snapshot.sh, restore.sh and create run over ssh, on a fake guest: an ssh that runs
# its command with sh, and a docker whose containers are lines "<id> <status> <compose project>" in a state file.
gd="$fcw/guest"; mkdir -p "$gd/bin"
printf '#!/bin/sh\nexec env PATH="$GD/bin:$PATH" sh -c "$*"\n' > "$fcw/bin/fakessh"
cat > "$gd/bin/docker" <<'EOF'
#!/bin/sh
st="$GD/state"
case $1 in
  ps) w=""; p=""
      for a; do case $a in status=*) w=${a#status=} ;; label=com.docker.compose.project=*) p=${a#*project=} ;; esac; done
      awk -v w="$w" -v p="$p" '(w == "" || $2 == w) && (p == "" || $3 == p) { print $1 }' "$st" ;;
  pause|unpause)
      op=$1; shift
      [ ! -e "$GD/fail" ] || { echo "Error response from daemon: fake" >&2; exit 1; }
      [ $# -gt 0 ] || { echo "\"docker $op\" requires at least 1 argument." >&2; exit 1; }
      to=paused; [ "$op" = unpause ] && [ ! -e "$GD/stuck" ] && to=running
      for i; do awk -v i="$i" -v t="$to" '$1 == i { $2 = t } { print }' "$st" > "$st.new" && mv "$st.new" "$st"; echo "$i"; done ;;
esac
EOF
chmod +x "$fcw/bin/fakessh" "$gd/bin/docker"
gfz() { GD="$gd" fcl "$@"; }
printf 'c1 running laug\nc2 running laug\nc3 running laug\nx1 running other\n' > "$gd/state"
out="$(gfz guest_freeze fakessh)"; rc=$?
eq "fc-lib.sh guest_freeze: exit 0, the 3 containers of project laug and the seconds" \
  "$rc:${out% *}:$([[ ${out#* } =~ ^[0-9]+\.[0-9]{2}$ ]] && echo seconds)" "0:3:seconds"
eq "guest_freeze: project laug's 3 paused, another project's left running" "$(cat "$gd/state")" \
  $'c1 paused laug\nc2 paused laug\nc3 paused laug\nx1 running other'
out="$(gfz guest_thaw fakessh)"; rc=$?
eq "fc-lib.sh guest_thaw: exit 0, 3 thawed" "$rc:${out% *}" "0:3"
eq "guest_thaw: all 4 running" "$(grep -c ' running ' "$gd/state")" "4"
out="$(gfz guest_thaw fakessh)"; rc=$?
eq "guest_thaw, nothing paused: refused, exit 1, two lines" "$rc:$out" \
  $'1:docker ps lists no paused container of project laug\nthe thaw failed: docker unpause in the guest'
gfz guest_freeze fakessh >/dev/null; : > "$gd/stuck"
out="$(gfz guest_thaw fakessh)"; rc=$?
eq "guest_thaw, a container still paused after docker unpause: refused, exit 1, naming them" "$rc:$out" \
  $'1:still paused after docker unpause: c1 c2 c3\nthe thaw failed: docker unpause in the guest'
rm -f "$gd/stuck"; : > "$gd/fail"
out="$(gfz guest_thaw fakessh)"; rc=$?
eq "guest_thaw, docker unpause fails: refused, exit 1" "$rc:$out" \
  $'1:Error response from daemon: fake\nthe thaw failed: docker unpause in the guest'
printf 'c1 running laug\n' > "$gd/state"
out="$(gfz guest_freeze fakessh)"; rc=$?
eq "guest_freeze, docker pause fails: refused, exit 1" "$rc:$out" \
  $'1:Error response from daemon: fake\nthe freeze failed: docker pause in the guest'
rm -f "$gd/fail"; printf 'x1 running other\n' > "$gd/state"
out="$(gfz guest_freeze fakessh)"; rc=$?
eq "guest_freeze, no container of project laug: refused, exit 1, nothing paused" "$rc:$out:$(cat "$gd/state")" \
  $'1:docker ps lists no container of project laug\nthe freeze failed: docker pause in the guest:x1 running other'
( exec -a "bin/laug pool serve -backend firecracker -listen 127.0.0.1:7700" "$REAL_SLEEP" 60 ) >/dev/null 2>&1 & p_fc=$!
( exec -a "/x/bin/laug pool serve -min 1" "$REAL_SLEEP" 60 ) >/dev/null 2>&1 & p_other=$!
( exec -a "sleep" "$REAL_SLEEP" 60 ) >/dev/null 2>&1 & p_plain=$!
for _ in $(seq 1 100); do
  [[ "$(tr '\0' ' ' </proc/$p_fc/cmdline)$(tr '\0' ' ' </proc/$p_other/cmdline)" == "bin/laug pool serve"*"/x/bin/laug pool serve"* ]] && break
  "$REAL_SLEEP" 0.05
done
printf '%s\n' "$p_fc" > "$fcw/cells/run/pool.pid"
eq "fc-lib.sh pool_serve: the pid file names a Firecracker pool serve" "$(fcl pool_serve)" "$p_fc firecracker"
out="$(fcl need_no_serve)"; rc=$?
eq "need_no_serve: refused while it runs, exit 2, the pid and the verb that stops it" "$rc:$out" \
  "2:the Firecracker pool runs, laug pool serve pid $p_fc (cells/run/pool.pid); sudo bin/laug pool down -backend firecracker stops it"
out="$(fcl need_no_pool)"; rc=$?
eq "need_no_pool: refused while a Firecracker pool serve runs" "$rc:$out" \
  "2:a Firecracker pool is up (laug pool serve, a cell, or prep's deny rule); sudo bin/laug pool down -backend firecracker -remove-cells first"
printf '%s\n' "$p_other" > "$fcw/cells/run/pool.pid"
eq "pool_serve: a pool serve on another backend" "$(fcl pool_serve)" "$p_other other"
out="$(fcl need_no_serve)"; rc=$?
eq "need_no_serve: refused for another backend's pool serve too" "$rc:$out" \
  "2:laug pool serve runs, pid $p_other (cells/run/pool.pid), on another backend; bin/laug pool down -remove-cells stops it"
out="$(fcl need_no_pool)"; rc=$?
eq "need_no_pool: another backend's pool serve is no Firecracker pool" "$rc:$out" "0:"
for c in "$p_plain:a pid another program took" "999999999:a pid that does not run" "x:a file with no pid"; do
  printf '%s\n' "${c%%:*}" > "$fcw/cells/run/pool.pid"
  out="$(fcl pool_serve)"; rc=$?
  eq "pool_serve: none for $(cut -d: -f2- <<<"$c")" "$rc:$out" "1:"
  out="$(fcl need_no_serve)"; rc=$?
  eq "need_no_serve: passes for $(cut -d: -f2- <<<"$c")" "$rc:$out" "0:"
done
rm -f "$fcw/cells/run/pool.pid"
out="$(fcl pool_serve)"; rc=$?
eq "pool_serve: none with no pid file" "$rc:$out" "1:"
# need_no_other_fc: another checkout's Firecracker pool serve, by its command line, whatever the pid file says.
out="$(fcl need_no_other_fc)"; rc=$?
eq "need_no_other_fc: refused while another Firecracker pool serve runs, exit 2, its pid" "$rc:$out" \
  "2:another Firecracker pool runs, laug pool serve pid $p_fc, another checkout's; sudo bin/laug pool down -backend firecracker in that checkout stops it"
kill "$p_fc" "$p_other" "$p_plain" 2>/dev/null; wait "$p_fc" "$p_other" "$p_plain" 2>/dev/null
out="$(fcl need_no_other_fc)"; rc=$?
eq "need_no_other_fc: passes with none, another backend's pool serve gone too" "$rc:$out" "0:"
# need_no_pool finds a pool VMM by its command line: a root one by its block's socket, a jailed one by its id, --id
# and the id as two words or --id=<id>. An id that runs on past the block number is not one.
for c in "/opt/fc/firecracker --api-sock $fcw/run/b7/fc.sock --enable-pci|2" "/firecracker --id laug-b3 --start-time-us 1|2" \
  "/firecracker --id=laug-b250|2" "/firecracker --id laug-b3x|0"; do
  ( exec -a "${c%|*}" "$REAL_SLEEP" 60 ) >/dev/null 2>&1 & p_v=$!
  for _ in $(seq 1 100); do [[ "$(tr '\0' ' ' </proc/$p_v/cmdline)" == "${c%|*} "* ]] && break; "$REAL_SLEEP" 0.05; done
  out="$(fcl need_no_pool)"; rc=$?
  eq "need_no_pool, a process '${c%|*}': exit ${c##*|}" "$rc" "${c##*|}"
  kill "$p_v" 2>/dev/null; wait "$p_v" 2>/dev/null
done
# prep runs as the pool serve's child: a parent whose command line is a Firecracker pool serve is not another pool, nor
# is the shell itself.
out="$( ( exec -a "bin/laug pool serve -backend firecracker -listen 127.0.0.1:0" bash -c \
  'bash -c ". cells/rootfs/fc-lib.sh; need_no_other_fc" 2>&1; echo "exit $?"' ) )"
eq "need_no_other_fc: passes under its own pool serve, its parent" "$out" "exit 0"
out="$( ( exec -a "bin/laug pool serve --backend=firecracker" bash -c '. cells/rootfs/fc-lib.sh; need_no_other_fc 2>&1; echo "exit $?"' ) )"
eq "need_no_other_fc: passes in a shell whose own command line is one" "$out" "exit 0"

section "laug sweep"
# laug sweep reads a run's cell from load/results under its checkout. A scratch checkout holds the fixture there, with
# the two files laug finds a checkout by and the script it runs, so the case needs no dev-host result in the tree.
swr="$work/sweep-root"; mkdir -p "$swr/gate" "$swr/load/results"
cp go.mod "$swr/"; cp gate/run.sh "$swr/gate/"; cp load/sweep.sh "$swr/load/"
cp "$td/base1-100551.json" "$swr/load/results/"
fresh
msw() { (cd "$swr" && env PATH="$FAKE_PATH" FAKE_SWEEP="$td/sweep/ok.json" "$root/bin/laug" sweep base1-100551 "$@" 2>&1); }
out="$(msw)"
has "laug sweep: the cell the run recorded, not the lease" "$(cat "$FAKE_DIR/temporal.args")" $'--address\n127.0.0.1:7243'
has "laug sweep: says where the address came from" "$out" "on 127.0.0.1:7243 (recorded in load/results/${base1##*/})"
has "laug sweep: cutoff 30 s before the run's end" "$(cat "$FAKE_DIR/temporal.args")" "StartTime < '2026-10-09T14:10:43Z'"
fresh
out="$(msw -address 127.0.0.1:17999 -sweep-margin 60s)"
has "laug sweep: an -address given wins" "$(cat "$FAKE_DIR/temporal.args")" $'--address\n127.0.0.1:17999'
has "laug sweep: and the line says so" "$out" "(-address, given)"
has "laug sweep: -sweep-margin 60s" "$(cat "$FAKE_DIR/temporal.args")" "StartTime < '2026-10-09T14:10:13Z'"

section "faults/candidates.yaml"
# The fixture holds the template entry for entry, comments aside: this tree's, or main's when a gated branch edits the
# template alone. A fixture that matches neither has drifted, and so would every from value checked against it.
fx="$(yq -o=json -I=0 . "$tmpl" 2>&1)"; base_tmpl="$("$REAL_GIT" show main:cells/compose/dynamicconfig/laug.yaml 2>/dev/null)"
if [[ "$fx" == "$(yq -o=json -I=0 . cells/compose/dynamicconfig/laug.yaml 2>&1)" ]]; then pass "load/testdata/laug.yaml holds this tree's cells/compose/dynamicconfig/laug.yaml, entry for entry"
elif [[ -n "$base_tmpl" && "$fx" == "$(yq -o=json -I=0 . <<<"$base_tmpl" 2>&1)" ]]; then pass "load/testdata/laug.yaml holds main's cells/compose/dynamicconfig/laug.yaml, entry for entry (this tree edits the template)"
else fail "load/testdata/laug.yaml holds neither this tree's cells/compose/dynamicconfig/laug.yaml nor main's" \
  "diff: $(diff <(yq -P . "$tmpl") <(yq -P . cells/compose/dynamicconfig/laug.yaml) | head -4 | tr '\n' '|')"; fi
cj="$(yq -o=json '.candidates' faults/candidates.yaml)"
eq "names are unique" "$(jq '[.[].name] | length == (unique | length)' <<<"$cj")" true
# The rows load/verdict.sh can BLOCK on (a threshold with no max decides nothing), and the ones lab131-1, an unchanged
# cell, BLOCKed on.
vrows="$(yq -o=json -I=0 '[.thresholds[] | select(.max != null) | .name] + ["workflows_completed", "omes_exit", "workflows_not_completed"]' load/steady-state.yaml)"
slow="$(yq -o=json -I=0 '.slow_cell_rows' faults/candidates.yaml)"
eq "slow_cell_rows: rows load/verdict.sh judges, the four lab131-1 BLOCKed on" "$(jq -c --argjson v "$vrows" '[.[] | select(. as $r | any($v[]; . == $r))] | length' <<<"$slow")" 4
for c in $(jq -r '.[].name' <<<"$cj"); do
  e="$(jq -c --arg n "$c" '.[] | select(.name == $n)' <<<"$cj")"; k="$(jq -r .key <<<"$e")"
  eq "$c: name, key, from, value, expect and reason set; expect ADVANCE or BLOCK; value is not from" \
    "$(jq '[(.name, .key, .from, .value, .expect, .reason) | . != null and . != ""] + [.expect == "ADVANCE" or .expect == "BLOCK", .value != .from] | all' <<<"$e")" true
  eq "$c: from is what the template holds for $k" "$(yq -r ".[\"$k\"][0].value" "$tmpl")" "$(jq -r .from <<<"$e")"
  eq "$c: blocks_on only for a BLOCK, there a verdict row and not a slow-cell row" \
    "$(jq --argjson v "$vrows" --argjson s "$slow" 'if .expect == "BLOCK" then (.blocks_on | type == "array" and length > 0
        and all(.[]; . as $r | any($v[]; . == $r)) and all(.[]; . as $r | all($s[]; . != $r))) else .blocks_on == null end' <<<"$e")" true
done
eq "read-partitions is a reach probe: its own source analysis predicts ADVANCE at 20/s" "$(jq -r '.[] | select(.name == "read-partitions") | .expect' <<<"$cj")" ADVANCE

section "faults/candidate.sh (compose and microVM leases, fake clock)"
# Timeline: the before load from T0, its window read over T0+30 to T0+90; the edit at T0+90, the wait to T0+216
# (2 x 63 s), the load T0+216 to T0+516, sweep cutoff T0+486 (2026-10-13T14:08:06Z). Every role logs each candidate's
# change 31 s after the edit, in the line the server builds (server 1.31.0 common/dynamicconfig/client_diff.go:83-93,
# key lowercased; read in source, and seen for frontend.rps in cells/results/2026-10-23-boot1.txt, P6). History logs
# a soft assertion in the before window and in the wait, matching one in the load.
dc_line() { # dc_line <seconds past T0> <key, lowercased> <old> <new>
  printf '%s\t{"level":"info","ts":"%s","msg":"dynamic config changed for the key: %s oldValue: { constraints: {} value: %s } newValue: { constraints: {} value: %s }"}\n' \
    "$1" "$("$REAL_DATE" -u -d "@$(( T0 + $1 ))" +%Y-%m-%dT%H:%M:%S.000Z)" "$2" "$3" "$4"
}
dc_all() { dc_line 121 frontend.rps 2400 2000; dc_line 121 history.persistencemaxqps 9000 50
  dc_line 121 matching.numtaskqueuereadpartitions 4 1; dc_line 121 system.forcesearchattributescacherefreshonread true false; }
CD_LOGS="$work/docker-candidate"; CD_LOGS_NO_HISTORY="$work/docker-candidate-nh"; mkdir -p "$CD_LOGS" "$CD_LOGS_NO_HISTORY"
for r in temporal-frontend temporal-frontend2 temporal-history temporal-matching temporal-worker; do
  sa_start > "$CD_LOGS_NO_HISTORY/$r.txt"
  [[ "$r" == temporal-history ]] || dc_all >> "$CD_LOGS_NO_HISTORY/$r.txt"
  { sa_start; dc_all; } > "$CD_LOGS/$r.txt"
done
sa_line 45 "loadedTasks went negative" >> "$CD_LOGS/temporal-history.txt"
sa_line 150 "loadedTasks went negative" >> "$CD_LOGS/temporal-history.txt"
sa_line 300 "completed task was already acked" >> "$CD_LOGS/temporal-matching.txt"
cd_env() { # cd_vars: the fake world of one candidate run, for env, once fake_lease has set dyn
  cd_vars=(PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_CELL=abcdef12 FAKE_OMES_BEFORE_UNTIL=90 FAKE_OMES_LOG="$td/omes-ok.txt"
    FAKE_OMES_COPY="$dyn:$FAKE_DIR/during.yaml" FAKE_SWEEP="$td/sweep/ok.json" FAKE_DOCKER_LOGS="$CD_LOGS" OMES_DIR="$FAKE_DIR/omes"
    RESULTS_DIR="$FAKE_DIR/results" RUN_ID=ht POOL_ADDR=http://127.0.0.1:17700 FC_GUEST_KEY="$FAKE_DIR/guest.key"
    FC_GUEST_KNOWN_HOSTS="$FAKE_DIR/guest.known_hosts" GUEST_DYNCONFIG_ORIG="$FAKE_DIR/laug.yaml.orig")
}
cd_run() { # cd_run <candidate> [env...]: faults/candidate.sh on a fresh fake lease (LEASE_SSH: a microVM cell;
  # LEASE_EXPIRES: seconds the lease has left, 3600); CD_SETUP runs first, with dyn set. Sets out, rc, j.
  # FAKE_OMES_COPY keeps the cell's laug.yaml as each load saw it, in during.yaml: the last is the candidate load's.
  local name="$1"; shift; fresh; fake_lease "${LEASE_EXPIRES:-3600}"
  [[ -z "${CD_SETUP:-}" ]] || eval "$CD_SETUP"
  cd_env; out="$(env "${cd_vars[@]}" FAKE_OMES_UNTIL=516 "$@" faults/candidate.sh "$name" "$FAKE_DIR/lease.json" 2>&1)"; rc=$?
  j="$FAKE_DIR/results/candidate-$name-ht.json"
}
during() { yq -r ".[\"$1\"][0].value" "$FAKE_DIR/during.yaml" 2>&1; }
restored() { if cmp -s "$dyn" "$tmpl"; then pass "$1"; else fail "$1" "$(diff "$tmpl" "$dyn" | tail -2 | tr '\n' '|')"; fi; }
loaded() { [[ -e "$FAKE_DIR/results/load/candidate-$1-ht.json" ]] && echo load || echo no-load; }   # the candidate's own load ran

cd_run benign CANDIDATE_RELEASE=1
eq "benign: PASS, exit 0" "$(grep -oE "^(PASS|FAIL|INVALID):" <<<"$out" | tr -d :):$rc" "PASS:0"
eq "benign: the result" "$(jq -c '[.candidate, .key, .from, .value, .expect, .blocks_on, .verdict, .deciding, .outcome, .backend, .wait_s]' "$j" 2>&1)" \
  '["benign","frontend.rps",2400,2000,"ADVANCE",null,"ADVANCE",[],"PASS","compose",126]'
eq "benign: the before window, its last 60 s read before the edit, every threshold under its max" \
  "$(jq -c '[.before.start, .before.end, .before.failing, .before.readings.service_error_rate, .before.load.task_queue, .before.load.duration, .edit_at]' "$j" 2>&1)" \
  '["2026-10-13T14:00:30Z","2026-10-13T14:01:30Z",[],0,"omes-candidate-benign-ht-before","90s","2026-10-13T14:01:30Z"]'
eq "benign: every role logged the change" "$(jq -c .landed "$j" 2>&1)" \
  '{"temporal-frontend":1,"temporal-frontend2":1,"temporal-history":1,"temporal-matching":1,"temporal-worker":1}'
has "benign: the change is read from each role's logs since the edit" "$(cat "$FAKE_DIR/docker.args")" $'logs\n--since\n1791900090\n--until\n1791900216\nfake-temporal-history'
eq "benign: the cell's laug.yaml said 2000 while the load ran" "$(during frontend.rps)" 2000
restored "benign: the template is back after the run"
eq "benign: the load ran after the wait, 300 s at 20/s" "$(jq -c '.load | [.start, .window_s, .rate, .duration, .task_queue]' "$j" 2>&1)" \
  '["2026-10-13T14:03:36Z",300,"20","5m","omes-candidate-benign-ht"]'
has "benign: the load continues past failed iterations" "$(cat "$FAKE_DIR/go.args")" "--continue-on-iteration-failure"
eq "benign: the verdict's readings, sweep and cutoff are kept, a row a threshold and 4 lost-work rows" \
  "$(jq -c '[.readings.workflows_completed, .sweep, .sweep_cutoff, (.rows | length)]' "$j" 2>&1)" \
  '[6000,{"Completed":3400},"2026-10-13T14:08:06Z",'"$(( $(yq '.thresholds | length' load/steady-state.yaml) + 4 ))"']'
eq "benign: soft assertions, history's in the before window and the wait, matching's in the load" \
  "$(jq -c '.softasserts | map_values([.["temporal-history"], .["temporal-matching"]])' "$j" 2>&1)" '{"before":[1,0],"wait":[1,0],"load":[0,1]}'
has "benign: the summary names the result" "$out" "verdict     ADVANCE, expect ADVANCE"
eq "benign: the server tag, read from the cell's history image, in the result and in both loads' run JSON" \
  "$(jq -r .server_tag "$j" 2>&1):$(jq -r .server_tag "$FAKE_DIR/results/load/candidate-benign-ht-before.json" 2>&1):$(jq -r .server_tag "$FAKE_DIR/results/load/candidate-benign-ht.json" 2>&1)" \
  "1.31.0:1.31.0:1.31.0"
has "benign: the summary names the server" "$out" "server      1.31.0"
eq "benign, CANDIDATE_RELEASE=1: released the cell once, and the lease file with it" \
  "$(cat "$FAKE_DIR/released" 2>&1):$([[ -e "$FAKE_DIR/lease.json" ]] && echo kept || echo removed)" '{"id":"abcdef12"}:removed'

cd_run persistence-starved FAKE_THRESHOLD_ONLY=service_error_with_type FAKE_THRESHOLD_VALUE=0.5 FAKE_THRESHOLD_FROM=200
eq "persistence-starved, error rate 0.5 after the edit: PASS, exit 0" "$(grep -oE "^(PASS|FAIL|INVALID):" <<<"$out" | tr -d :):$rc" "PASS:0"
eq "persistence-starved: BLOCK, decided by service_error_rate, a blocks_on row; its reading kept" \
  "$(jq -c '[.verdict, .deciding, .readings.service_error_rate, .verdict_exit, .blocks_on, .outcome_reason]' "$j" 2>&1)" \
  '["BLOCK",["service_error_rate"],0.5,1,["service_error_rate","omes_exit"],"verdict BLOCK, as expected; blocks_on rows among the deciding: service_error_rate"]'
has "persistence-starved: the summary says what BLOCKed" "$out" "verdict     BLOCK, expect BLOCK; BLOCKed by service_error_rate (blocks_on: service_error_rate, omes_exit)"
eq "persistence-starved: the cell's laug.yaml said 50 while the load ran" "$(during history.persistenceMaxQPS)" 50
# lab131-1's shape: a BLOCK on the floor and the sweep only, rows an unchanged cell trips. It used to PASS.
cd_run persistence-starved FAKE_WF_RATE=10 FAKE_SWEEP="$td/sweep/running.json"
eq "persistence-starved, BLOCKed on the floor and the sweep only: INVALID, exit 2, not PASS" \
  "$(jq -c '[.outcome, .verdict, .deciding]' "$j" 2>&1):$rc" '["INVALID","BLOCK",["workflows_completed","workflows_not_completed"]]:2'
has "persistence-starved, no blocks_on row decided: says why" "$(jq -r .outcome_reason "$j" 2>&1)" \
  "BLOCKed by workflows_completed, workflows_not_completed, none of them a blocks_on row (service_error_rate omes_exit)"
cd_run persistence-starved FAKE_OMES_LOG="$td/omes-fail.txt" FAKE_OMES_EXIT=1
eq "persistence-starved, Omes ran all 300 s and exited 1 with its tally: judged, PASS on omes_exit" \
  "$(jq -c '[.outcome, .deciding, .load.omes_tally]' "$j" 2>&1):$rc" '["PASS",["omes_exit"],{"succeeded":5997,"failed":3}]:0'
cd_run persistence-starved
eq "persistence-starved, the verdict ADVANCEs: FAIL, exit 1" "$(jq -r '.outcome + ": " + .outcome_reason' "$j" 2>&1):$rc" "FAIL: verdict ADVANCE, expected BLOCK:1"
restored "persistence-starved, FAIL: the template is back after the run"
# lab131-1 before the edit: slow from the start. Nothing is edited and nothing judged.
cd_run persistence-starved FAKE_THRESHOLD_ONLY='task_type="Workflow"' FAKE_THRESHOLD_VALUE=16305.9
eq "a cell slow before the edit: INVALID, exit 2, never steady, no edit, no load" \
  "$(jq -c '[.outcome, .edit_at, .landed, .load, .before.failing]' "$j" 2>&1):$rc:$(loaded persistence-starved)" \
  '["INVALID",null,null,null,["workflow_task_schedule_to_start_p99_ms=16305.9"]]:2:no-load'
has "a cell slow before the edit: says so" "$(jq -r .outcome_reason "$j" 2>&1)" "never steady before the edit: workflow_task_schedule_to_start_p99_ms=16305.9"
restored "a cell slow before the edit: the file is untouched"

cd_run frontend-starved FAKE_THRESHOLD_ONLY=service_error_with_type FAKE_THRESHOLD_VALUE=0.049 FAKE_THRESHOLD_FROM=200
eq "frontend-starved, error rate 0.049 (throttle-frontend's at 10): PASS, exit 0, frontend.rps 5 while it ran" \
  "$(jq -c '[.outcome, .deciding]' "$j" 2>&1):$rc:$(during frontend.rps)" '["PASS",["service_error_rate"]]:0:5'

cd_run read-partitions
eq "read-partitions, a reach probe, the verdict ADVANCEs: PASS, exit 0, the prediction kept" \
  "$(jq -r '.outcome + " " + .verdict + " " + (.prediction | startswith("Tasks do not strand") | tostring)' "$j" 2>&1):$rc" "PASS ADVANCE true:0"
eq "read-partitions: the edit keeps the namespace constraint" \
  "$(yq -o=json -I=0 '.["matching.numTaskqueueReadPartitions"][0]' "$FAKE_DIR/during.yaml" 2>&1)" '{"value":1,"constraints":{"namespace":"default"}}'

# Candidates the shipped file does not hold: a bool change, and blocks_on that candidate.sh refuses.
yq '.candidates += [
  {"name": "bool-off", "key": "system.forceSearchAttributesCacheRefreshOnRead", "from": true, "value": false, "expect": "ADVANCE", "reason": "test"},
  {"name": "block-bare", "key": "frontend.rps", "from": 2400, "value": 5, "expect": "BLOCK", "reason": "test"},
  {"name": "block-slow", "key": "frontend.rps", "from": 2400, "value": 5, "expect": "BLOCK", "blocks_on": ["service_error_rate", "workflows_completed"], "reason": "test"},
  {"name": "block-typo", "key": "frontend.rps", "from": 2400, "value": 5, "expect": "BLOCK", "blocks_on": ["service_error_rates"], "reason": "test"},
  {"name": "advance-on", "key": "frontend.rps", "from": 2400, "value": 2000, "expect": "ADVANCE", "blocks_on": ["omes_exit"], "reason": "test"}]' \
  faults/candidates.yaml > "$work/candidates.yaml"
# A BLOCK candidate on a reading with no max, the base's first, would wait for a BLOCK that row cannot give.
NOMAX_ROW="$(yq -r '[.thresholds[] | select(.max == null) | .name][0] // "frontend_request_over_10ms"' load/steady-state.yaml)"
yq -i '.candidates += [{"name": "block-nomax", "key": "frontend.rps", "from": 2400, "value": 5, "expect": "BLOCK", "blocks_on": ["'"$NOMAX_ROW"'"], "reason": "test"}]' \
  "$work/candidates.yaml"
cd_run bool-off CANDIDATES="$work/candidates.yaml"
eq "a bool from true to false: PASS, exit 0, false in the file while the load ran" \
  "$(jq -c '[.outcome, .from, .value]' "$j" 2>&1):$rc:$(during system.forceSearchAttributesCacheRefreshOnRead)" '["PASS",true,false]:0:false'
for c in block-bare:"names no blocks_on row" block-slow:"blocks_on row 'workflows_completed' is in slow_cell_rows" \
         block-typo:"blocks_on row 'service_error_rates' is not a row" advance-on:"expects ADVANCE; blocks_on is for a BLOCK" \
         block-nomax:"blocks_on row '$NOMAX_ROW' is not a row load/verdict.sh judges"; do
  cd_run "${c%%:*}" CANDIDATES="$work/candidates.yaml"
  eq "${c%%:*}: refused before any load, exit 2" "$rc:$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:no-load"
  has "${c%%:*}: says why" "$out" "${c#*:}"
done

cd_run benign FAKE_DOCKER_LOGS="$CD_LOGS_NO_HISTORY"
eq "edit not logged by history: INVALID, exit 2, no load" "$(jq -r .outcome "$j" 2>&1):$rc:$(loaded benign)" "INVALID:2:no-load"
eq "edit not logged by history: says which role, no verdict" "$(jq -c '[.outcome_reason, .verdict, .landed["temporal-history"]]' "$j" 2>&1)" \
  '["the edit did not land: no \"dynamic config changed for the key: frontend.rps\" line from temporal-history in 126 s",null,0]'
restored "edit not logged: the template is back after the run"
# Omes exits at T0+290, 74 s in; the window ends when run-omes.sh next reads the fake clock, a 30 s slice later or so.
cd_run benign FAKE_OMES_UNTIL=290 FAKE_OMES_EXIT=1
eq "the load ends 74 s in: INVALID, exit 2" "$(jq -r .outcome "$j" 2>&1):$rc" "INVALID:2"
has "the load ends early: says it is the harness until shown otherwise" "$(jq -r .outcome_reason "$j" 2>&1)" \
  "s window for 300 s of load (Omes exit 1): a harness failure until shown otherwise"
# A window as long as DURATION, but no tally: the window holds go run and the worker's start, so Omes itself may
# have run that much less. It used to be judged.
cd_run benign FAKE_OMES_EXIT=1
eq "Omes exits 1 with no end-of-run tally in a 300 s window: INVALID, exit 2, not judged" "$(jq -r .outcome_reason "$j" 2>&1):$rc" \
  "the load ended early: Omes exited 1 with no end-of-run tally after 300 s, before its timeout at 420 s: a harness failure until shown otherwise:2"

CD_SETUP='yq -i ".[\"frontend.rps\"][0].value = 1000" "$dyn"' cd_run benign
eq "the cell's copy holds another value: refused, exit 2, nothing written" "$rc:$([[ -e "$j" ]] && echo written || echo none):$(yq -r '.["frontend.rps"][0].value' "$dyn")" "2:none:1000"
has "another value: says so" "$out" "laug.yaml holds frontend.rps 1000, not 2400"
cd_run benign FAKE_EXEC_CELL_ID=deadbeef CANDIDATE_RELEASE=1
eq "a role mounts another cell's copy: refused before any load, exit 2, nothing written" \
  "$rc:$([[ -e "$j" ]] && echo written || echo none):$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:none:no-load"
has "another cell's copy: says which role" "$out" "mounts the dynamic config of cell 'deadbeef', not abcdef12"
restored "another cell's copy: the file is untouched"
eq "a refusal with CANDIDATE_RELEASE=1 still releases the cell" "$(cat "$FAKE_DIR/released" 2>&1)" '{"id":"abcdef12"}'
LEASE_TAG=1.32.1 cd_run benign CANDIDATE_RELEASE=1
eq "the lease says 1.32.1, the cell runs 1.31.0: refused before any load, exit 2, nothing written, the cell released" \
  "$rc:$([[ -e "$j" ]] && echo written || echo none):$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load):$(cat "$FAKE_DIR/released" 2>&1)" \
  '2:none:no-load:{"id":"abcdef12"}'
has "the lease and the cell disagree: says so, as faults/run.sh does" "$out" \
  "the lease says server 1.32.1 and cell abcdef12's temporal-history runs temporalio/server:1.31.0"
LEASE_TAG=1.31.0 cd_run benign
eq "the lease and the cell agree on 1.31.0: PASS, server_tag 1.31.0" "$(jq -r '.outcome + " " + .server_tag' "$j" 2>&1):$rc" "PASS 1.31.0:0"
# Threshold sets per server release: the before window on the cell's release's set, the verdict on the load's.
cd_run benign FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 FAKE_THRESHOLD_EMPTY="$NO_ACT"
eq "a 1.32.1 cell, no activity data: PASS, steady before the edit, the reading listed as not produced" \
  "$(jq -c '[.outcome, .verdict, .before.failing, .before.not_produced, .thresholds_release, [.overrides_applied[].name]]' "$j" 2>&1):$rc" \
  '["PASS","ADVANCE",[],["activity_schedule_to_start_p99_ms"],"load/releases/1.32.yaml",["activity_schedule_to_start_p99_ms","activity_schedule_to_start_over_10ms"]]:0'
eq "a 1.32.1 cell: the verdict's row for it, kept in the result" "$(jq -c '.rows[] | select(.metric == "activity_schedule_to_start_p99_ms") | [.value, .result, .blocks]' "$j" 2>&1)" \
  '[null,"not produced on this release (load/releases/1.32.yaml)",false]'
has "a 1.32.1 cell: the summary names the files" "$out" "thresholds  load/steady-state.yaml, the base (1.31.0 baselines), with load/releases/1.32.yaml for server 1.32.1: 2 overrides"
has "a 1.32.1 cell: the summary shows the override" "$out" \
  "override    activity_schedule_to_start_p99_ms: no data in the before window, not produced on this release, not judged (load/releases/1.32.yaml: 1.32 runs activities eagerly"
has "a 1.32.1 cell: the override with no max says so, and that it had no data" "$out" \
  "override    activity_schedule_to_start_over_10ms: no data in the before window; no max in the threshold set, so not judged (load/releases/1.32.yaml: "
hasnt "a 1.32.1 cell: no override line says a reading with no data had data" "$out" "had data in the before window"
eq "a 1.32.1 cell: the verdict's own thresholds are kept, the same release file, no warning" \
  "$(jq -c '[.verdict_thresholds.release, [.verdict_thresholds.overrides_applied[] | [.name, .not_produced]]]' "$j" 2>&1):$(grep -c 'warning     the before window' <<<"$out")" \
  '["load/releases/1.32.yaml",[["activity_schedule_to_start_p99_ms",true],["activity_schedule_to_start_over_10ms",true]]]:0'
# The before window is judged on SERVER_TAG's set (the history image), the verdict on the set of the version the cell's
# frontend reported: when the two differ, the result keeps both and the summary warns.
cd_run benign FAKE_SERVER_VERSION=1.32.1
eq "image 1.31.0, the frontend says 1.32.1: PASS; the before window on the base, the verdict on load/releases/1.32.yaml" \
  "$(jq -c '[.outcome, .thresholds_release, .overrides_applied, .verdict_thresholds.release, [.verdict_thresholds.overrides_applied[] | [.name, .not_produced]]]' "$j" 2>&1):$rc" \
  '["PASS",null,[],"load/releases/1.32.yaml",[["activity_schedule_to_start_p99_ms",false],["activity_schedule_to_start_over_10ms",false]]]:0'
has "the two sets differ: the summary warns" "$out" \
  "warning     the before window was judged with no release file (server 1.31.0), the load's verdict with load/releases/1.32.yaml: load/steady-state.yaml, the base (1.31.0 baselines), with load/releases/1.32.yaml for server 1.32.1: 2 overrides"
# A before window that could not be read is not one where every reading had data.
cd_run benign FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 FAKE_PROM_FAIL_FROM=0
eq "a 1.32.1 cell whose Prometheus does not answer: INVALID, exit 2; failing and not_produced null, not []" \
  "$(jq -c '[.outcome, .outcome_reason, .before.readings, .before.failing, .before.not_produced]' "$j" 2>&1):$rc:$(loaded benign)" \
  '["INVALID","never steady before the edit: the thresholds could not be read",null,null,null]:2:no-load'
has "the before window not read: the override line says not read" "$out" \
  "override    activity_schedule_to_start_p99_ms: not read in the before window (load/releases/1.32.yaml: "
hasnt "the before window not read: no override line says it had data" "$out" "had data in the before window"
cd_run benign FAKE_THRESHOLD_EMPTY="$NO_ACT"
eq "a 1.31.0 cell, no activity data: INVALID, exit 2, never steady, no edit, no load" \
  "$(jq -r .outcome_reason "$j" 2>&1):$rc:$(loaded benign)" "never steady before the edit: activity_schedule_to_start_p99_ms=null:2:no-load"
cd_run benign FAKE_SERVER_TAG=1.32.1 FAKE_SERVER_VERSION=1.32.1 THRESHOLDS_RELEASES="$work/rel-kind"
eq "a release file that cannot be applied: refused before any load, exit 2, nothing written" \
  "$rc:$([[ -e "$j" ]] && echo written || echo none):$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:none:no-load"
restored "a release file that cannot be applied: the file is untouched"
LEASE_EXPIRES=300 cd_run benign
eq "a lease that ends before the run would: refused, exit 2, no edit" "$rc:$([[ -e "$j" ]] && echo written || echo none)" "2:none"
has "a short lease: says how long to lease" "$out" "lease again with TTL="
restored "a short lease: the file is untouched"
# The pool (faults/lib.sh, pool_quiet): the before load waits for laugd's busy cells, and the result records the most
# each window saw. The wait moves the whole timeline by its length, so the fake loads end that much later too.
cd_run benign FAKE_POOL_BUSY=0:15 FAKE_OMES_BEFORE_UNTIL=105 FAKE_OMES_UNTIL=531
eq "a busy cell that clears after 3 polls: the before load waits 15 s, PASS" \
  "$(jq -c '[.outcome, .pool_quiet_wait_s, .pool_quiet_bound_s, .before.start, .edit_at, .pool_busy]' "$j" 2>&1):$rc" \
  '["PASS",15,300,"2026-10-13T14:00:45Z","2026-10-13T14:01:45Z",{"before":0,"wait":0,"load":0}]:0'
eq "a busy cell that clears: polled every 5 s before the load, at each boundary, and every 30 s of the load" \
  "$(xargs < "$FAKE_DIR/status.calls")" "0 0 5 10 15 15 105 105 105 231 231 231 261 291 321 351 381 411 441 471 501 531 531"
has "a busy cell that clears: the summary says how long" "$out" \
  "pool        waited 15 s before the load until laugd showed no busy cell; busy cells by window, most at once: before 0, wait 0, load 0"
hasnt "a busy cell that clears: no warning" "$out" "warning     "
cd_run benign FAKE_POOL_BUSY=0:1000 POOL_QUIET_WAIT_S=10 FAKE_OMES_BEFORE_UNTIL=100 FAKE_OMES_UNTIL=526
eq "a cell busy past the bound: the before load starts at 10 s, the bound; recorded in every window, not judged: PASS" \
  "$(jq -c '[.outcome, .pool_quiet_wait_s, .pool_quiet_bound_s, .pool_busy]' "$j" 2>&1):$rc" '["PASS",10,10,{"before":1,"wait":1,"load":1}]:0'
has "a cell busy past the bound: the summary warns" "$out" "warning     laugd showed busy cells (before 1, wait 1, load 1): "
cd_run benign FAKE_POOL_BUSY=240:250:2
eq "2 cells busy in the load: seen by the load's 30 s poll, recorded, PASS" "$(jq -c '[.outcome, .pool_quiet_wait_s, .pool_busy]' "$j" 2>&1)" \
  '["PASS",0,{"before":0,"wait":0,"load":2}]'
LEASE_EXPIRES=1000 cd_run benign
eq "a lease that covers the run but not the wait for busy cells as well: refused, exit 2" "$rc:$([[ -e "$j" ]] && echo written || echo none)" "2:none"
LEASE_EXPIRES=1000 cd_run benign POOL_QUIET_WAIT_S=0
eq "the same lease with POOL_QUIET_WAIT_S 0: PASS" "$(jq -r .outcome "$j" 2>&1):$rc" "PASS:0"
cd_run benign POOL_QUIET_WAIT_S=5m
eq "POOL_QUIET_WAIT_S not whole seconds: refused before any load, exit 2, nothing written" \
  "$rc:$([[ -e "$j" ]] && echo written || echo none):$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:none:no-load"
has "POOL_QUIET_WAIT_S not whole seconds: says so" "$out" "POOL_QUIET_WAIT_S must be whole seconds, not '5m'"
cd_run benign FAKE_POOL_BUSY=0:1000 POOL_QUIET_WAIT_S=20 FAKE_UNLEASED_FROM=10
eq "the leased cell goes busy during the wait: refused after it, exit 2, no load, nothing written" \
  "$rc:$([[ -e "$j" ]] && echo written || echo none):$([[ -e "$FAKE_DIR/go.args" ]] && echo load || echo no-load)" "2:none:no-load"
has "the leased cell goes busy during the wait: says so" "$out" "pool: cell abcdef12 is not leased after the wait for laugd's busy cells; no load was started"
restored "the leased cell goes busy during the wait: the file is untouched"
LEASE_SSH=root@10.254.3.2 cd_run benign FAKE_POOL_BUSY=0:15 FAKE_OMES_BEFORE_UNTIL=105 FAKE_OMES_UNTIL=531
eq "microVM lease, a busy cell that clears: the before load waits 15 s, PASS" \
  "$(jq -c '[.outcome, .backend, .pool_quiet_wait_s, .pool_busy]' "$j" 2>&1):$rc" '["PASS","firecracker",15,{"before":0,"wait":0,"load":0}]:0'
restored "microVM lease, a busy cell that clears: the guest's file is back after the run"
# The Omes ports (faults/lib.sh, wait_omes_ports): the before load waits for the candidate's own pair, and both loads
# get it. The wait moves the timeline by its length, as the wait for busy cells does.
cd_run benign "${gate2[@]}" FAKE_SS_LISTEN=19102:15 FAKE_OMES_BEFORE_UNTIL=105 FAKE_OMES_UNTIL=531
eq "candidate, Omes ports 19102 and 19103, 19102 held to 15 s: the before load waits 15 s, PASS" \
  "$(jq -c '[.outcome, .before.start, .edit_at]' "$j" 2>&1):$rc" '["PASS","2026-10-13T14:00:45Z","2026-10-13T14:01:45Z"]:0'
has "candidate, Omes ports 19102 and 19103: the candidate's load gets both" "$(cat "$FAKE_DIR/go.args")" \
  $'--worker-prom-listen-address\n127.0.0.1:19102\n--prom-listen-address\n127.0.0.1:19103'
cd_run nope
eq "an unknown candidate: refused, exit 2" "$rc" 2
has "an unknown candidate: names the candidates" "$out" "one of: benign, persistence-starved, frontend-starved, read-partitions"

LEASE_SSH=root@10.254.3.2 cd_run benign
eq "microVM lease, benign: PASS, exit 0" "$(jq -r '.outcome + " " + .backend' "$j" 2>&1):$rc" "PASS firecracker:0"
eq "microVM lease, benign: a lease with no backend key is firecracker on ssh, no snapshot values; host_virtualized a boolean, after server_tag" \
  "$(jq -c '[.firecracker_version, .cache_type, (.host_virtualized | type), (keys_unsorted | index("server_tag")) as $i | keys_unsorted[$i:$i+5]]' "$j" 2>&1)" \
  '[null,null,"boolean",["server_tag","backend","firecracker_version","cache_type","host_virtualized"]]'
has "microVM lease: the edit runs in the guest" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" "yq -i '.[\"frontend.rps\"][0].value = 2000'"
has "microVM lease: the guest's cp and yq are statements of their own, so set -e stops a failed cp" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" \
  "cp $dyn \$t; yq -i"
has "microVM lease: the change is read from the guest's docker logs" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" \
  "docker logs --since 1791900090 --until 1791900216 fake-temporal-matching 2>&1"
has "microVM lease: restore copies the file the cell started with" "$(cat "$FAKE_DIR/ssh.args" 2>/dev/null)" "cp $FAKE_DIR/laug.yaml.orig"
eq "microVM lease: the guest's laug.yaml said 2000 while the load ran" "$(during frontend.rps)" 2000
restored "microVM lease: the guest's file is back after the run"
LEASE_SSH=root@10.254.3.2 LEASE_BACKEND=firecracker LEASE_FC_VERSION=v1.17.0 LEASE_CACHE_TYPE=Unsafe cd_run benign
eq "microVM lease with the snapshot values: the candidate JSON carries them" "$(jq -c '[.outcome, .backend, .firecracker_version, .cache_type]' "$j" 2>&1):$rc" '["PASS","firecracker","v1.17.0","Unsafe"]:0'
LEASE_SSH=root@10.254.3.2 cd_run benign FAKE_SSH_EXIT=255
eq "microVM lease, guest unreachable: refused before the edit, exit 2" "$rc:$([[ -e "$j" ]] && echo written || echo none)" "2:none"
# The session drops after the guest moved the edited file in: the edit returns 255, and the file must still go back.
LEASE_SSH=root@10.254.3.2 cd_run benign FAKE_SSH_DROP_AFTER="yq -i"
eq "microVM lease, ssh drops after the edit landed in the guest: INVALID, exit 2, no load" \
  "$(jq -r .outcome_reason "$j" 2>&1):$rc:$(loaded benign)" "the edit failed (exit 255); the file may have changed, so it was restored:2:no-load"
restored "microVM lease, ssh drops after the edit: the guest's file is back"

# A failed cp in the guest's edit: the cell's file must not be replaced by the empty temp file. The fake guest is this
# machine, so a mode 000 file makes cp fail for any user but root.
fresh; LEASE_SSH=root@10.254.3.2 fake_lease
if (( $(id -u) == 0 )); then pass "guest edit, cp fails: skipped as root, which reads a mode 000 file"
else
  chmod 000 "$dyn"
  ( export PATH="$FAKE_PATH" FAKE_T0=$T0 FAKE_CELL=abcdef12 FAKE_DOCKER_LOGS="$CD_LOGS" FC_GUEST_KEY="$FAKE_DIR/guest.key"
    FC_GUEST_KNOWN_HOSTS="$FAKE_DIR/guest.known_hosts" RUN_ID=ht; . "$root/faults/lib.sh"; lease_read "$FAKE_DIR/lease.json"; set -e; dynconfig frontend.rps 10 ) >/dev/null 2>&1; rc=$?
  m="$(stat -c %a "$dyn")"; chmod 644 "$dyn"
  eq "guest edit, cp fails: exits nonzero, the cell's file kept, mode and all" "$rc:$m:$(cmp -s "$dyn" "$tmpl" && echo same || echo replaced)" "1:0:same"
fi

# Ctrl-C or SIGTERM in the load: the EXIT trap restores the file and releases the cell read at start, even after
# another laug lease rewrote the lease file.
fresh; fake_lease; cd_env
env "${cd_vars[@]}" FAKE_OMES_BLOCK=1 CANDIDATE_RELEASE=1 faults/candidate.sh benign "$FAKE_DIR/lease.json" > "$work/sig.out" 2>&1 &
pid=$!
for _ in $(seq 1 400); do [[ "$(during frontend.rps 2>/dev/null)" == 2000 ]] && break; "$REAL_SLEEP" 0.05; done
"$REAL_SLEEP" 0.5
jq -c '.id = "feedf00d"' "$FAKE_DIR/lease.json" > "$FAKE_DIR/lease2.json" && mv "$FAKE_DIR/lease2.json" "$FAKE_DIR/lease.json"
kill -TERM "$pid"; wait "$pid"; rc=$?
eq "SIGTERM in the load: exit 143" "$rc" 143
restored "SIGTERM in the load: the template is back"
eq "SIGTERM in the load: released the cell read at start, once" "$(cat "$FAKE_DIR/released" 2>&1)" '{"id":"abcdef12"}'
eq "SIGTERM in the load: the lease file, which names another cell now, stays" "$(jq -r .id "$FAKE_DIR/lease.json" 2>&1)" feedf00d
# Ctrl-C sends SIGINT to the whole foreground process group. A background job here has SIGINT ignored unless job
# control is on (set -m), which also gives it a process group of its own, as a terminal's foreground job has.
fresh; fake_lease; cd_env
got="$( { set -m
  env "${cd_vars[@]}" FAKE_OMES_BLOCK=1 CANDIDATE_RELEASE=1 faults/candidate.sh benign "$FAKE_DIR/lease.json" > "$work/int.out" 2>&1 &
  pid=$!
  for _ in $(seq 1 400); do [[ "$(during frontend.rps 2>/dev/null)" == 2000 ]] && break; "$REAL_SLEEP" 0.05; done
  "$REAL_SLEEP" 0.5; kill -INT -- "-$pid"; wait "$pid"; echo "$?"; } 2>/dev/null )"
eq "Ctrl-C in the load: exit 130, the template back, the cell released, the lease file gone" \
  "$got:$(cmp -s "$dyn" "$tmpl" && echo restored || echo edited):$(cat "$FAKE_DIR/released" 2>&1):$([[ -e "$FAKE_DIR/lease.json" ]] && echo kept || echo removed)" \
  '130:restored:{"id":"abcdef12"}:removed'

section "laug candidate"
out="$(env PATH="$FAKE_PATH" bin/laug candidate nope 2>&1)"; rc=$?
eq "an unknown name: exit 2 before any lease" "$rc" 2
eq "an unknown name: names the candidates, a line each" "$(grep -A4 'which holds 4:$' <<<"$out" | tail -n +2 | paste -sd,)" \
  "  benign,  persistence-starved,  frontend-starved,  read-partitions"
out="$(env PATH="$FAKE_PATH" bin/laug candidate 2>&1)"; rc=$?
eq "no name: exit 2" "$rc" 2
# -dry-run prints the script's line and leases nothing: the script releases, by the lease file it reads at start, and
# no release follows it.
out="$(bin/laug candidate benign -dry-run 2>&1)"
has "laug candidate: the script releases the lease it read" "$out" "CANDIDATE_RELEASE=1"
has "laug candidate: from .last-lease.json, named" "$out" "faults/candidate.sh benign .last-lease.json"
hasnt "laug candidate: no release after the script, which Ctrl-C skipped" "$out" "/release"

section "the verdict path and the observability stack"
# The verdict depends on nothing of the observability stack (_obs/README.md: it judges nothing). No file under
# load/, faults/ or gate/ names a stack port (7710 to 7769 after a colon, and no digit after), OBS_ in any name, the
# store's datasource uid or a record's name, the only check of the stack in this test. Fixtures and results aside:
# they are data, and a committed poll.jsonl holds "mono":7714.29. A four-digit value from 7710 to 7769 after a colon,
# as compact JSON writes one, still reads as a port here.
eq "the verdict path names nothing of the observability stack: no port 7710 to 7769, OBS_, laug-store or laug record" \
  "$(grep -rnE --exclude-dir=testdata --exclude-dir=results -e ':77[1-6][0-9]([^0-9]|$)|OBS_|laug-store|laug_(gate|item)_' load faults gate 2>&1)" ""

section "laug judge and gate/post.sh (the gate's verdict rule and its one exit)"
# load/testdata/gate/base is one gate run's results directory, every item ADVANCE, in the layout the judge reads
# (internal/judge/input.go, Input). steady is a load, pl and kh rauns, with the readings of dev-host runs of 2026-10-14 and
# every field faults/run.sh writes now. All three are moved onto one timeline: t0 2026-10-14T14:00:00Z, leases at +42,
# +64 and +87 s, the wave at +150 s, the post at +530 s. Monotonic time runs at 1/1.044 of wall time, near the dev
# host's 1.045 and 1.048 (cells/results/2026-10-23-boot1.txt:69; the lab host read 0.9999, :61). The
# poller's lines are made up: every 5 s from the wave, laugd busy 0 and the three leased projects up, no pressure
# reading. Each case is a copy with one thing changed, judged by bin/laug judge -write, the gate's judge, which writes
# its verdict into the copy.
GFX="$td/gate/base"; GID="$(jq -r .gate_id "$GFX/gate.json")"
gcase() { gd="$work/gate/$1"; rm -rf "$gd"; mkdir -p "$work/gate"; cp -r "$GFX" "$gd"; }   # gcase <name>: a fresh copy, in gd
gedit() { local f="$gd/$1" p="$2"; shift 2; jq "$@" "$p" "$f" > "$f.tmp" && mv "$f.tmp" "$f"; }   # gedit <file> <jq> [jq args]
graun() { local k="$1"; shift; gedit "$k/$GID-$k.json" "$@"; }   # graun <key> <jq> [jq args]: the item's raun or run JSON
gexit() { gedit "$1/exit.json" ".exit = $2 | .stopped_by = ${3:-null}"; }   # gexit <key> <exit> ["TERM"]
gjudge() {
  local out err
  out="$(bin/laug judge -write "$gd" 2>"$work/judge.err")"; grc=$?; err="$(cat "$work/judge.err")"
  gout="$out${err:+$'\n'$err}"; gv="$gd/verdict.json"
}
gsum() { jq -c '[.verdict, .class, [.rules | to_entries[] | select(.value.pass | not) | .key]]' "$gv" 2>&1; }   # verdict, class, rules failed
gitem() { jq -c --arg k "$1" '.items[] | select(.key == $k) | [.observed, .class, .reason]' "$gv" 2>&1; }
gwhy() { jq -r '.reasons[]' "$gv" 2>&1; }
# What faults/run.sh writes for a raun that did not recover, for one stopped after inject (its EXIT trap, with $rc), and
# for one never steady, and what load/verdict.sh writes for a BLOCK on one threshold.
DNR='.result = "BLOCK" | .reason = "did not recover within 120s: signal 9876.5" | .recovered_at = "" | .time_to_recover_s = ""
  | .after_start = "" | .after_end = "" | .after = {} | .sweep = null'
PARTIAL='{run_id, fault, cell, address, server_tag, scenario, partial: true, exit: $rc, inject_at, landed_at, remove_at, mechanisms: [],
  result: "BLOCK", reason: "stopped before the result (exit \($rc)); the fault was removed on the way out; the log says why"}'
NEVER='.result = "BLOCK" | .reason = "never steady: workflow_end_to_end_p95_ms=346.16279069767415" | .inject_at = "" | .landed_at = ""
  | .remove_at = "" | .recovered_at = "" | .after_start = "" | .after_end = "" | .during = {} | .drain = {} | .after = {} | .sweep = null'
SLOW='.verdict = "BLOCK" | .exit = 1 | .deciding = ["workflow_end_to_end_p95_ms"]
  | .rows |= map(if .metric == "workflow_end_to_end_p95_ms" then .value = 346.163 | .result = "EXCEEDS" | .blocks = true else . end)'

# The fixture keeps the shape of what the scripts write today: every key of a raun JSON from the fake faults/run.sh,
# of a run JSON from load/run-omes.sh and of load/verdict.sh's VERDICT_JSON is in the fixture's.
fr gate-shape
eq "fixture: pl and kh have every key a raun JSON from faults/run.sh has" \
  "$(jq -cn --slurpfile a "$j" --slurpfile p "$GFX/pl/$GID-pl.json" --slurpfile k "$GFX/kh/$GID-kh.json" \
    '[($a[0] | keys) - ($p[0] | keys), ($a[0] | keys) - ($k[0] | keys)]' 2>&1)" '[[],[]]'
ro gate-shape "$td/omes-ok.txt" 0 DURATION=5m RATE=20 MAX_CONCURRENT=50 SERVER_TAG=1.31.0
eq "fixture: steady has every key a run JSON from load/run-omes.sh has" \
  "$(jq -cn --slurpfile a "$FAKE_DIR/results/gate-shape.json" --slurpfile s "$GFX/steady/$GID-steady.json" '($a[0] | keys) - ($s[0] | keys)' 2>&1)" '[]'
vd "$work/new.json" VERDICT_JSON="$work/gate-vj.json"   # new.json: the clean run JSON of the verdict.sh section
eq "fixture: steady's verdict has every key load/verdict.sh's VERDICT_JSON has" \
  "$(jq -cn --slurpfile a "$work/gate-vj.json" --slurpfile s "$GFX/steady/$GID-steady.verdict.json" '($a[0] | keys) - ($s[0] | keys)' 2>&1)" '[]'
eq "fixture: thresholds.json agrees with load/thresholds.sh 1.31.0 on what the judge reads (the error rate's max, the release file, the host)" \
  "$(jq -c '[(.thresholds[] | select(.name == "service_error_rate") | .max), .resolved.release_file, .resolved.host]' "$GFX/thresholds.json")" \
  "$(load/thresholds.sh 1.31.0 | yq -o=json -I=0 '[(.thresholds[] | select(.name == "service_error_rate") | .max), .resolved.release_file, .resolved.host]')"

# 1. All ADVANCE.
gcase advance; gjudge
eq "all ADVANCE: ADVANCE, no class, no rule failed, exit 0" "$(gsum):$grc" '["ADVANCE",null,[]]:0'
eq "all ADVANCE: each item with its cell and block from its lease, its exit and its JSON, relative to the run" \
  "$(jq -c '[.items[] | [.key, .observed, .cell, .block, .exit, .evidence]]' "$gv")" \
  "[[\"steady\",\"ADVANCE\",\"1a2b3c4d\",0,0,[\"steady/$GID-steady.json\",\"steady/$GID-steady.verdict.json\"]],[\"pl\",\"ADVANCE\",\"5e6f7a8b\",1,0,[\"pl/$GID-pl.json\"]],[\"kh\",\"ADVANCE\",\"9c0d1e2f\",2,0,[\"kh/$GID-kh.json\"]]]"
eq "all ADVANCE: host quiet; 530 s on the wall clock and 507.663 s monotonic from t0 to t_post; the host by size" \
  "$(jq -c '[.host_quiet, .host_noise, .elapsed_s, .host, .server_tag, .laug_commit, .tree_dirty, (.inputs | length)]' "$gv")" \
  '[true,[],{"wall":530,"mono":507.663},{"type":"m8i.8xlarge","vcpu":32,"mem_gib":123,"mem_available_gib":36},"1.31.0","d91874f",false,5]'
eq "all ADVANCE: verdict.md opens with the job summary heading (docs/BRAND.md, Surfaces) and ends with the verdict" \
  "$(head -1 "$gd/verdict.md")|$(tail -1 "$gd/verdict.md")" "## laug · verdict for raun d91874f|**ADVANCE**"
has "all ADVANCE: verdict.md has the item table" "$(cat "$gd/verdict.md")" "| pl | ADVANCE | ADVANCE |  |  |"
eq "all ADVANCE: the summary opens with laug · and ends with the verdict on its own line" \
  "$(head -1 <<<"$gout")|$(tail -1 <<<"$gout")" "laug · gate $GID, server 1.31.0: ADVANCE|ADVANCE"
cp "$gv" "$work/gate/advance-1.json"; gjudge
if cmp -s "$gv" "$work/gate/advance-1.json"; then pass "the same directory judged twice: verdict.json byte for byte the same"
else fail "the same directory judged twice: verdict.json byte for byte the same" "$(diff "$work/gate/advance-1.json" "$gv" | head -3 | tr '\n' '|')"; fi
hasnt "verdict.json names no local path" "$(cat "$gv" "$gd/verdict.md")" "$work"

# 2. Did not recover: a regression.
gcase dnr; graun pl "$DNR"; gexit pl 1; gjudge
eq "pl did not recover: BLOCK, class regression, R4, exit 1" "$(gsum):$grc" '["BLOCK","regression",["R4"]]:1'
eq "pl did not recover: the item, its own reason" "$(gitem pl)" '["BLOCK","regression","did not recover within 120s: signal 9876.5"]'
has "pl did not recover: R4 names the item, what was expected and why" "$(gwhy)" "R4 pl: expected ADVANCE, observed BLOCK: did not recover within 120s: signal 9876.5"
eq "pl did not recover: verdict.md ends BLOCK, after its class" "$(tail -3 "$gd/verdict.md" | paste -sd '|')" "Class: regression.||**BLOCK**"

# 3. Never steady: the raun could not say, and it never reached its during window.
gcase never; graun kh "$NEVER"; gexit kh 1; gjudge
eq "kh never steady: BLOCK, class invalid; R4, and R5 with no during window" "$(gsum)" '["BLOCK","invalid",["R4","R5"]]'
eq "kh never steady: the item" "$(gitem kh)" '["BLOCK","invalid","never steady: workflow_end_to_end_p95_ms=346.16279069767415"]'
has "kh never steady: R5 says what it read" "$(gwhy)" "R5 kh: control: service_error_rate read null in the during window, not above its max 0.001"

# 4. Missing JSON: faults/run.sh exited 2 before writing any (a load generator that exits in its first 10 s does).
gcase nojson; rm "$gd/kh/$GID-kh.json"; gexit kh 2; gjudge
eq "kh wrote no JSON: BLOCK, class invalid, every rule an item can fail" "$(gsum)" '["BLOCK","invalid",["R2","R3","R4","R5","R6"]]'
eq "kh wrote no JSON: the item" "$(gitem kh)" "[null,\"invalid\",\"no JSON: kh/$GID-kh.json is missing\"]"
has "kh wrote no JSON: R3 says exit 2 is a refusal or a harness failure" "$(gwhy)" "R3 kh: contract: faults/run.sh exited 2: a refusal or a harness failure"

# 5. Partial JSON: the EXIT trap's (faults/run.sh, cleanup), after an error past inject.
gcase partial; graun pl "$PARTIAL" --argjson rc 1; gexit pl 1; gjudge
eq "pl partial: BLOCK, class invalid; R2, R4, R5 and R6 (no thresholds_release)" "$(gsum)" '["BLOCK","invalid",["R2","R4","R5","R6"]]'
eq "pl partial: the item, its reason after the path" "$(gitem pl)" \
  "[\"BLOCK\",\"invalid\",\"partial JSON: pl/$GID-pl.json; stopped before the result (exit 1); the fault was removed on the way out; the log says why\"]"

# 6. faults/run.sh exit 2 with its JSON: a mechanism failed.
gcase exit2; graun kh "$NEVER"' | .reason = $r | .inject_at = "1791986640" | .inject_error = $e' \
  --arg r "inject failed: 'docker kill temporal-history' exited 2" --arg e "'docker kill temporal-history' exited 2"; gexit kh 2; gjudge
eq "kh exit 2, inject failed: BLOCK, class invalid; R3, R4, R5" "$(gsum)" '["BLOCK","invalid",["R3","R4","R5"]]'
eq "kh exit 2: the item, its own reason" "$(gitem kh)" "[\"BLOCK\",\"invalid\",\"inject failed: 'docker kill temporal-history' exited 2\"]"
has "kh exit 2: R3" "$(gwhy)" "R3 kh: contract: faults/run.sh exited 2: a refusal or a harness failure"

# 7. load/verdict.sh exit 3: a short window and nothing lost, so no verdict.
gcase short; gedit "steady/$GID-steady.verdict.json" '.verdict = null | .exit = 3 | .short_window = "5m" | .window_s = 150'; gexit steady 3; gjudge
eq "steady exit 3: BLOCK, class invalid; R3, R4" "$(gsum)" '["BLOCK","invalid",["R3","R4"]]'
eq "steady exit 3: the item" "$(gitem steady)" '[null,"invalid","contract: load/verdict.sh exited 3: the window is shorter than the thresholds trust"]'
has "steady exit 3: R3 also names the short window" "$(gwhy)" "R3 steady: contract: load/verdict.sh read a window under 5m"

# 8. Tag mismatch: a raun on another release, and a load whose cell said another version.
gcase tag; graun pl '.server_tag = "1.32.1"'; gjudge
eq "pl on 1.32.1, the gate on 1.31.0: BLOCK, class invalid, R6" "$(gsum)" '["BLOCK","invalid",["R6"]]'
eq "pl on 1.32.1: the item" "$(gitem pl)" '["ADVANCE","invalid","identity: server_tag 1.32.1, and the gate SERVER_TAG is 1.31.0"]'
gcase tag-load; graun steady '.server_version = "1.32.1"'; gjudge
eq "the steady cell said 1.32.1: BLOCK on R6" "$(gsum):$(gitem steady)" \
  '["BLOCK","invalid",["R6"]]:["ADVANCE","invalid","identity: the cell said server_version 1.32.1, and the gate SERVER_TAG is 1.31.0"]'

# 9. Thresholds mismatch: judged by another set than the gate resolved, or by an unnamed one.
gcase th; graun kh '.thresholds_release = "load/releases/1.32.yaml"'; gjudge
eq "kh judged with load/releases/1.32.yaml: BLOCK, class invalid, R6" "$(gsum):$(gitem kh)" \
  '["BLOCK","invalid",["R6"]]:["ADVANCE","invalid","identity: thresholds_release load/releases/1.32.yaml, and thresholds.json names null"]'
gcase th-none; gedit "steady/$GID-steady.verdict.json" 'del(.thresholds_release)'; gjudge
eq "a verdict that records no thresholds_release: R6" "$(gsum):$(gitem steady)" \
  "[\"BLOCK\",\"invalid\",[\"R6\"]]:[\"ADVANCE\",\"invalid\",\"identity: steady/$GID-steady.verdict.json records no thresholds_release\"]"

# 10. R5: the fault did not show in the error rate, so a renamed error metric cannot read as a permanent 0.
gcase r5; graun kh '.during.service_error_rate = 0.0004'; gjudge
eq "kh error rate 0.0004 in the during window, max 0.001: BLOCK, class control, R5" "$(gsum):$(gitem kh)" \
  '["BLOCK","control",["R5"]]:["ADVANCE","control","control: service_error_rate 0.0004 in the during window, not above its max 0.001"]'
graun kh '.during.service_error_rate = 0.001'; gjudge
eq "kh error rate at its max, 0.001: above means above, R5" "$(gsum)" '["BLOCK","control",["R5"]]'

# 11. pool_busy 1: the host was not quiet. An ADVANCE stands; a BLOCK becomes invalid, to rerun.
gcase busy; graun pl '.pool_busy.during = 1'; gjudge
eq "pl saw 1 busy cell in its during window, all ADVANCE: ADVANCE stands, host not quiet" \
  "$(jq -c '[.verdict, .class, .rerun, .host_quiet, .host_noise]' "$gv")" \
  '["ADVANCE",null,false,false,["pl: laugd showed 1 busy cell(s) in its during window"]]'
graun pl "$DNR"; gexit pl 1; gjudge
eq "the same, and pl did not recover: BLOCK, class invalid, rerun, a regression on a quiet host" \
  "$(jq -c '[.verdict, .class, .rerun, .class_if_quiet, .items[1].class]' "$gv")" '["BLOCK","invalid",true,"regression","regression"]'
has "busy and BLOCK: verdict.md says rerun" "$(cat "$gd/verdict.md")" "A BLOCK on a host that was not quiet is class invalid: rerun (class on a quiet host: regression)."

# 12. Deadline: the watchdog's TERM, and the EXIT trap's partial JSON.
gcase deadline; graun kh "$PARTIAL" --argjson rc 143; gexit kh 143 '"TERM"'; gjudge
eq "kh stopped by the watchdog: BLOCK, class deadline" "$(gsum)" '["BLOCK","deadline",["R2","R3","R4","R5","R6"]]'
eq "kh stopped by the watchdog: the item" "$(gitem kh)" '["BLOCK","deadline","deadline: the watchdog stopped it (TERM)"]'
gcase late; gedit kh/exit.json '.end.wall = 1791987200'; gjudge
eq "kh ended 800 s after t0, past deadline_s 780, and the watchdog never fired: class deadline, R2" "$(gsum):$(gitem kh)" \
  '["BLOCK","deadline",["R2"]]:["ADVANCE","deadline","deadline: it ended 800 s after the gate start, past the deadline at 780 s"]'

# 13. A reason no class knows: still BLOCK.
gcase unknown; graun pl '.result = "BLOCK" | .reason = "a reason no table knows: 3"'; gexit pl 1; gjudge
eq "a reason with an unknown prefix: BLOCK, class unknown" "$(gsum):$(gitem pl)" '["BLOCK","unknown",["R4"]]:["BLOCK","unknown","a reason no table knows: 3"]'
graun pl '.reason = ""'; gjudge
eq "a BLOCK with no reason: class unknown" "$(gitem pl)" '["BLOCK","unknown","BLOCKed with no reason recorded"]'

# The other ways a gate fails, and the order of classes.
gcase harness; gedit harness.json '.exit = 1'; gjudge
eq "harness-test exited 1: BLOCK, class harness, R1" "$(gsum)" '["BLOCK","harness",["R1"]]'
printf '%s\n' '{"wall":1791986600,"mono":81426.139,"busy":1,"projects":["laug-0000aaaa"],"pressure":null}' >> "$gd/poll.jsonl"; gjudge
eq "harness-test exited 1 on a host that was not quiet: still class harness, no rerun" "$(jq -c '[.class, .rerun, .host_quiet]' "$gv")" '["harness",false,false]'
gcase noharness; rm "$gd/harness.json"; gjudge
has "no harness.json: R1 fails" "$(gwhy)" "R1 harness: harness.json is missing, so make harness-test was not seen to pass"
gcase refused; gedit gate.json '.refused = "a compose project is up: laug-0000aaaa"'; rm -r "$gd/steady" "$gd/pl" "$gd/kh" "$gd/harness.json" "$gd/poll.jsonl"; gjudge
eq "refused at preflight: BLOCK, class refused, every rule failed" "$(gsum)" '["BLOCK","refused",["R1","R2","R3","R4","R5","R6"]]'
has "refused: the reason first" "$(gwhy | head -1)" "refused: a compose project is up: laug-0000aaaa"
gcase extra; mkdir "$gd/zz"; echo '{"exit": 0}' > "$gd/zz/exit.json"; gjudge
eq "results for an item the plan does not list: BLOCK, class invalid, R2" "$(gsum)" '["BLOCK","invalid",["R2"]]'
has "the extra item is named" "$(gwhy)" "R2 zz/ holds results for an item the plan does not list"
gcase faultless; gedit plan.json '.items |= map(select(.kind == "load")) | .items_in_plan = 1'; rm -r "$gd/pl" "$gd/kh"
jq -c '.projects = ["laug-1a2b3c4d"]' "$GFX/poll.jsonl" > "$gd/poll.jsonl"; gjudge
eq "a plan with no fault item has no live control: BLOCK, class control, R5" "$(gsum)" '["BLOCK","control",["R5"]]'
gcase project; printf '%s\n' '{"wall":1791986600,"mono":81426.139,"busy":0,"projects":["laug-1a2b3c4d","laug-0000aaaa"],"pressure":null}' >> "$gd/poll.jsonl"; gjudge
eq "the poller saw a project the gate did not lease: ADVANCE stands, host not quiet" "$(jq -c '[.verdict, .host_quiet, .host_noise]' "$gv")" \
  '["ADVANCE",false,["the poller saw compose project laug-0000aaaa up, which the gate did not lease"]]'
gcase nopoll; rm "$gd/poll.jsonl"; gjudge
eq "no poller record: ADVANCE stands, host not seen quiet" "$(jq -c '[.verdict, .host_quiet, .host_noise]' "$gv")" \
  '["ADVANCE",false,["poll.jsonl is missing, so the host was not seen quiet"]]'
gcase order; gedit "steady/$GID-steady.verdict.json" "$SLOW"; gexit steady 1; graun kh "$NEVER"; gexit kh 1; gjudge
eq "steady BLOCKed on a threshold, kh never steady: class regression, which a seen failure explains better" "$(gsum)" '["BLOCK","regression",["R4","R5"]]'
eq "steady BLOCKed: its reason is its deciding row" "$(gitem steady)" '["BLOCK","regression","load verdict BLOCK: workflow_end_to_end_p95_ms 346.163 EXCEEDS"]'
gcase sweep; gedit "steady/$GID-steady.verdict.json" '.verdict = "BLOCK" | .exit = 1 | .deciding = ["workflows_not_completed"]
  | .rows |= map(if .metric == "workflows_not_completed" then .value = null | .result = "EXCEEDS (no data: the sweep of omes-x on 127.0.0.1:7233 failed)" | .blocks = true else . end)'
gexit steady 1; gjudge
eq "steady BLOCKed on a sweep that failed, nothing else: class invalid" "$(gitem steady)" \
  '["BLOCK","invalid","sweep failed: EXCEEDS (no data: the sweep of omes-x on 127.0.0.1:7233 failed)"]'
gcase mismatch; gexit steady 1; gjudge
eq "verdict.sh exited 1 and its JSON says ADVANCE and exit 0: R3" "$(gsum):$(gitem steady)" \
  '["BLOCK","invalid",["R3"]]:["ADVANCE","invalid","contract: load/verdict.sh exited 1 with verdict ADVANCE"]'
gcase garbled; echo 'not json' > "$gd/pl/$GID-pl.json"; gjudge
eq "a raun JSON that does not parse: no JSON" "$(gitem pl)" "[null,\"invalid\",\"no JSON: pl/$GID-pl.json does not parse\"]"
gcase noplan; rm "$gd/plan.json"; gjudge
eq "no plan.json: BLOCK, class invalid, R2 and R5; every directory is an item the plan does not list" "$(gsum)" '["BLOCK","invalid",["R2","R5"]]'

# 14. A plan cut by ITEMS: the items that did not run cannot pass, and the verdict says how many ran.
gcase cut; gedit plan.json '.items |= .[:2]'; gedit gate.json '.items_limit = 2'; rm -r "$gd/kh"; gjudge
eq "plan cut to 2 of 3 items by ITEMS=2, kh never ran: BLOCK, class invalid, R2, exit 1" "$(gsum):$grc" '["BLOCK","invalid",["R2"]]:1'
has "plan cut: R2 says so" "$(gwhy)" "R2 plan: cut to 2 of 3 items (ITEMS=2): 1 planned item(s) never ran"
eq "plan cut: verdict.json records items_in_plan and items_limit" "$(jq -c '[.items_in_plan, .items_limit, (.items | length)]' "$gv")" '[3,2,2]'
has "plan cut: verdict.md says 2 of 3" "$(cat "$gd/verdict.md")" "Gate run $GID on server 1.31.0: 2 of 3 planned item(s) (ITEMS=2), 530 s"
gcase uncounted; gedit plan.json 'del(.items_in_plan)'; gjudge
eq "plan.json with no items_in_plan: BLOCK, R2" "$(gsum)" '["BLOCK","invalid",["R2"]]'
has "no items_in_plan: R2 says why" "$(gwhy)" "R2 plan: plan.json records no items_in_plan"

# 15. A GATE_FAKE=1 run says so, in verdict.json, verdict.md, the summary and gate/post.sh's verdict line.
gcase fake; gedit gate.json '.fake = true'; gjudge
eq "fake: ADVANCE, verdict.json fake true, the heading and the summary say GATE_FAKE=1" \
  "$(jq -c '[.verdict, .fake]' "$gv")|$(head -1 "$gd/verdict.md")|$(head -1 <<<"$gout")" \
  '["ADVANCE",true]|## laug · verdict for raun d91874f (GATE_FAKE=1: fixtures, no cell ran)|laug · gate '"$GID"', server 1.31.0: ADVANCE (GATE_FAKE=1: fixtures, no cell ran)'
eq "fake: gate/post.sh's verdict line says so" "$(gate/post.sh "$gd" 2>&1 | tail -1)" "VERDICT: ADVANCE (GATE_FAKE=1: fixtures, no cell ran)"
eq "a real run: verdict.json fake false" "$(gcase real; gjudge; jq -c .fake "$gv")" false

# 16. An expected BLOCK counts only from a regression: one that BLOCKs because it could not say did not fire.
gcase ctl-invalid; gedit plan.json '.items |= map(if .key == "steady" or .key == "pl" then .expect = "BLOCK" else . end)'
gedit "steady/$GID-steady.verdict.json" '.verdict = "BLOCK" | .exit = 1 | .deciding = ["workflows_not_completed"]
  | .rows |= map(if .metric == "workflows_not_completed" then .value = null | .result = "EXCEEDS (no data: the sweep of omes-x on 127.0.0.1:7233 failed)" | .blocks = true else . end)'
gexit steady 1; graun pl "$NEVER"; gexit pl 1; gjudge
eq "expect BLOCK, observed BLOCK on a failed sweep and on never steady: BLOCK, class invalid, R4 (and R5, pl had no during window)" \
  "$(gsum)|$(gitem steady)|$(gitem pl)" \
  '["BLOCK","invalid",["R4","R5"]]|["BLOCK","invalid","sweep failed: EXCEEDS (no data: the sweep of omes-x on 127.0.0.1:7233 failed)"]|["BLOCK","invalid","never steady: workflow_end_to_end_p95_ms=346.16279069767415"]'
has "expect BLOCK on never steady: R4 says the BLOCK was not a regression" "$(gwhy)" \
  "R4 pl: expected BLOCK from a regression, observed BLOCK of class invalid: never steady: "
gcase ctl-fired; gedit plan.json '.items |= map(if .key == "pl" then .expect = "BLOCK" else . end)'; graun pl "$DNR"; gexit pl 1; gjudge
eq "expect BLOCK, pl did not recover: the control fired, ADVANCE" "$(gsum)|$(gitem pl)" '["ADVANCE",null,[]]|["BLOCK",null,""]'

# 17. The deadline needs its three numbers: kh ended 900 s after t0, past deadline_s 780, and one number is gone.
gcase noclock; gedit kh/exit.json '.end.wall = 1791987300'; rm "$gd/clock.json"; gjudge
eq "kh ended past the deadline, no clock.json: BLOCK, class invalid, R2" "$(gsum)|$(gitem kh)" \
  '["BLOCK","invalid",["R2"]]|["ADVANCE","invalid","no end time: cannot tell whether kh ended before the deadline: clock.json is missing"]'
gcase nodeadline; gedit kh/exit.json '.end.wall = 1791987300'; gedit plan.json 'del(.deadline_s)'; gjudge
eq "kh ended past the deadline, no deadline_s: BLOCK, R2" "$(gsum)" '["BLOCK","invalid",["R2"]]'
has "no deadline_s: R2 says why" "$(gwhy)" "R2 plan: deadline_s is null, not seconds above 0"
gcase noend; gedit kh/exit.json 'del(.end)'; gjudge
eq "kh/exit.json with no end: BLOCK, R2" "$(gsum)|$(gitem kh)" \
  '["BLOCK","invalid",["R2"]]|["ADVANCE","invalid","no end time: cannot tell whether kh ended before the deadline: kh/exit.json has no end.wall"]'

# 18. A poll that read nothing is not a quiet one.
gcase nullpoll; jq -c '.busy = null | .projects = null' "$GFX/poll.jsonl" > "$gd/poll.jsonl"; graun pl "$DNR"; gexit pl 1; gjudge
eq "every poll unanswered and pl did not recover: host not quiet, BLOCK class invalid, rerun, regression on a quiet host" \
  "$(jq -c '[.verdict, .class, .rerun, .class_if_quiet, .host_quiet, .host_noise]' "$gv")" \
  '["BLOCK","invalid",true,"regression",false,["laugd did not answer 76 of 76 polls","docker compose ls failed in 76 of 76 polls"]]'
gcase onenull; printf '%s\n' '{"wall":1791986931,"mono":81742.230,"busy":0,"projects":null,"pressure":null}' >> "$gd/poll.jsonl"; gjudge
eq "one poll where docker compose ls failed, all ADVANCE: ADVANCE stands, host not quiet" "$(jq -c '[.verdict, .host_quiet, .host_noise]' "$gv")" \
  '["ADVANCE",false,["docker compose ls failed in 1 of 77 polls"]]'

# 19. The incumbent arm, R7 (docs/DESIGN.md section 7.1). load/testdata/gate/base-arm is base on gate/plan.yaml's arm:
# incumbent (steady's load on 1.31.0, arm incumbent, pair steady), steady and pl, every item ADVANCE, A/A, so every d
# is 0; load/testdata/gate/mk-base-arm.sh makes it from base. acase is gcase on it. Its thresholds.json is the tree's
# set as shipped, recording mode: every compare row's tolerance null. ajudging puts it in judging mode: the wave
# gate/plan.yaml runs, 20 gates, an evidence file and a tolerance on every compare row, 0.3 ms or 0.005 of a share.
# A case that recording mode decides too runs in both (amodes), since recording is what the first A/A gates run.
AFX="$td/gate/base-arm"
acase() { gd="$work/gate/$1"; rm -rf "$gd"; mkdir -p "$work/gate"; cp -r "$AFX" "$gd"; }   # acase <name>: a fresh copy, in gd
AWAVE='["load:incumbent workflow_with_single_noop_activity 20/s 50 5m","load workflow_with_single_noop_activity 20/s 50 5m","fault:persistence-latency workflow_with_single_noop_activity"]'
ajudging() { gedit thresholds.json '.compare.wave = $w | .compare.gates = 20 | .compare.evidence = "cells/results/2026-10-20-lab-arm-calibration.txt"
  | .compare.rows |= map(.tolerance = (if .scale == "ms" then 0.3 else 0.005 end))' --argjson w "$AWAVE"; }
amode() { acase "$1-$2"; [[ "$2" != judging ]] || ajudging; }   # amode <name> recording|judging: a fresh copy in that mode
avj() { echo "$1/$GID-$1.verdict.json"; }   # avj <key>: the item's verdict JSON, relative to gd
arows() { jq -c '[.comparison.rows[].result] | unique' "$gv" 2>&1; }   # every row's result, as a set
arow() { jq -c --arg n "$1" '.comparison.rows[] | select(.name == $n) | [.release, .incumbent, .d, .tolerance, .judged, .result]' "$gv" 2>&1; }
ar7() { jq -c '.rules.R7.reasons' "$gv" 2>&1; }
gcase arm-none; gjudge
eq "base, no arm: R7 holds, comparison null, verdict.json's incumbent null, every item's arm null" \
  "$(jq -c '[.rules.R7, .comparison, .incumbent, ([.items[].arm] | unique)]' "$gv")" '[{"name":"incumbent","pass":true,"reasons":[]},null,null,[null]]'
acase arm-rec; gjudge
eq "base-arm in recording mode: ADVANCE, no rule failed, mode recording, 12 rows, every one recorded, every d 0" \
  "$(gsum):$grc|$(jq -c '.comparison | [.mode, (.rows | length), ([.rows[].result] | unique), ([.rows[].d] | unique)]' "$gv")" \
  '["ADVANCE",null,[]]:0|["recording",12,["recorded"],[0]]'
eq "recording mode's verdict.md says R7: recorded, not judged: no tolerance in load/steady-state.yaml." \
  "$(grep -cxF 'R7: recorded, not judged: no tolerance in load/steady-state.yaml.' "$gd/verdict.md")" 1
eq "verdict.json's keys in order, and items[].arm after kind" "$(jq -c '[keys_unsorted, (.items[0] | keys_unsorted)]' "$gv")" \
  '[["gate_id","verdict","class","rerun","class_if_quiet","refused","fake","server_tag","incumbent","laug_commit","tree_dirty","base","inputs","deadline_s","items_in_plan","items_limit","t0","t_post","elapsed_s","host","rules","reasons","items","comparison","host_quiet","host_noise"],["key","kind","arm","expect","observed","class","reason","failed","run_id","cell","block","exit","stopped_by","pool_busy","evidence"]]'
rm -rf "$work/base-arm"; out="$(load/testdata/gate/mk-base-arm.sh "$work/base-arm" 2>&1)"; rc=$?
eq "base-arm is what mk-base-arm.sh makes from base, byte for byte" "$rc|$out|$(diff -r "$AFX" "$work/base-arm" 2>&1 | head -3)" "0||"
acase arm-aa; ajudging; gjudge
eq "base-arm judging, A/A: ADVANCE, the five compared rows ok, the rest recorded" \
  "$(gsum)|$(jq -c '.comparison | [.mode, [.rows[] | select(.judged) | .result], ([.rows[] | select(.judged | not) | .result] | unique)]' "$gv")" \
  '["ADVANCE",null,[]]|["judging",["ok","ok","ok","ok","ok"],["recorded"]]'
acase arm-worse; ajudging; gedit "$(avj steady)" '.readings.persistence_latency_p99_ms += 0.42'; gjudge
eq "judging, steady's persistence +0.42: BLOCK, class regression, R7 alone failed; steady observed ADVANCE, its reason worse than the incumbent; R4 holds" \
  "$(gsum)|$(gitem steady)" \
  '["BLOCK","regression",["R7"]]|["ADVANCE","regression","worse than the incumbent: persistence_latency_p99_ms 20.164 against 19.744 on incumbent, +0.420 past a tolerance of 0.30"]'
eq "verdict.md's comparison table: the tolerance line, the header, the persistence row, a share row with d in points to 4 decimals, the count" \
  "$(grep -E '^(Tolerances from |\| (Reading|persistence_latency_p99_ms|frontend_request_over_10ms|workflows_completed) \|)' "$gd/verdict.md" | paste -sd '#')" \
  'Tolerances from cells/results/2026-10-20-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.#| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |#| persistence_latency_p99_ms | 20.164 ms | 19.744 ms | +0.420 | 0.30 | WORSE |#| frontend_request_over_10ms | 8.61% | 8.61% | 0.0000 points | 0.50 points | ok |#| workflows_completed | 5881 | 5881 | 0 |  | recorded |'
cp "$gv" "$work/gate/arm-1.json"; cp "$gd/verdict.md" "$work/gate/arm-1.md"; gjudge
eq "the same directory judged twice: verdict.json and verdict.md byte for byte" \
  "$(cmp -s "$gv" "$work/gate/arm-1.json" && cmp -s "$gd/verdict.md" "$work/gate/arm-1.md" && echo same)" same
printf '%s\n' '{"wall":1791986600,"mono":81426.139,"busy":1,"projects":["laug-1a2b3c4d","laug-5e6f7a8b","laug-9c0d1e2f"],"pressure":null}' >> "$gd/poll.jsonl"; gjudge
eq "the same on a host not quiet (a poll with busy 1): BLOCK, class invalid, rerun true, class_if_quiet regression" \
  "$(jq -c '[.verdict, .class, .rerun, .class_if_quiet]' "$gv")" '["BLOCK","invalid",true,"regression"]'
acase arm-at; ajudging; gedit "$(avj steady)" '.readings.persistence_latency_p99_ms += 0.3004'; gjudge
eq "judging, d at its tolerance holds: persistence +0.3004 is d 0.300, ok, ADVANCE" "$(gsum)|$(arow persistence_latency_p99_ms | jq -c '.[2:]')" \
  '["ADVANCE",null,[]]|[0.3,0.3,true,"ok"]'
acase arm-past; ajudging; gedit "$(avj steady)" '.readings.persistence_latency_p99_ms += 0.3006'; gjudge
eq "judging, just past it: persistence +0.3006 is d 0.301, WORSE, BLOCK class regression" "$(gsum)|$(arow persistence_latency_p99_ms | jq -c '.[2:]')" \
  '["BLOCK","regression",["R7"]]|[0.301,0.3,true,"WORSE"]'
acase arm-faster; ajudging; gedit "$(avj steady)" '.readings.persistence_latency_p99_ms -= 5'; gjudge
eq "judging, the release faster: persistence -5 ms is d -5, ok, ADVANCE" "$(gsum)|$(arow persistence_latency_p99_ms | jq -c '.[2:]')" \
  '["ADVANCE",null,[]]|[-5,0.3,true,"ok"]'
for m in recording judging; do
  amode arm-incslow "$m"; gedit "$(avj incumbent)" "$SLOW"; gexit incumbent 1; gjudge
  if [[ "$m" == judging ]]; then want='["BLOCK","invalid",["R4","R7"]]|["incumbent: not compared: incumbent did not ADVANCE (verdict BLOCK)"]'
  else want='["BLOCK","invalid",["R4"]]|[]'; fi
  eq "$m, the incumbent BLOCKed on a max: BLOCK, class invalid by its prefix, R4 on incumbent, R7 $([[ "$m" == judging ]] && echo "not compared on it" || echo holds); every row not compared" \
    "$(gsum)|$(ar7)|$(gitem incumbent)|$(arows)" \
    "$want|[\"BLOCK\",\"invalid\",\"incumbent: load verdict BLOCK: workflow_end_to_end_p95_ms 346.163 EXCEEDS\"]|[\"not compared\"]"
done
for m in recording judging; do
  amode arm-bothslow "$m"; gedit "$(avj incumbent)" "$SLOW"; gexit incumbent 1; gedit "$(avj steady)" "$SLOW"; gexit steady 1; gjudge
  if [[ "$m" == judging ]]; then want='["BLOCK","regression",["R4","R7"]]|["incumbent: not compared: incumbent did not ADVANCE (verdict BLOCK)"]'
  else want='["BLOCK","regression",["R4"]]|[]'; fi
  eq "$m, both loads BLOCKed: class regression, R7 $([[ "$m" == judging ]] && echo "fails on the incumbent" || echo holds), every row not compared, and verdict.md says the incumbent BLOCKed too" \
    "$(gsum)|$(ar7)|$(arows)|$(grep -cxF 'The incumbent BLOCKed too, on workflow_end_to_end_p95_ms: the host, or the cell config both arms share, is the likelier cause.' "$gd/verdict.md")" \
    "$want|[\"not compared\"]|1"
done
acase arm-wave; ajudging; gedit thresholds.json '.compare.wave |= map(sub(" 20/s "; " 10/s "))'; gjudge
eq "judging, compare.wave of another rate: BLOCK, class invalid, the wave text on the incumbent, every row not compared" "$(gsum)|$(gitem incumbent)|$(arows)" \
  '["BLOCK","invalid",["R7"]]|["ADVANCE","invalid","not compared: the tolerances in load/steady-state.yaml were measured on the wave [load:incumbent workflow_with_single_noop_activity 10/s 50 5m, load workflow_with_single_noop_activity 10/s 50 5m, fault:persistence-latency workflow_with_single_noop_activity], and this plan runs [load:incumbent workflow_with_single_noop_activity 20/s 50 5m, load workflow_with_single_noop_activity 20/s 50 5m, fault:persistence-latency workflow_with_single_noop_activity]: laug gate tolerance on A/A gates of this plan"]|["not compared"]'
# Cut to its first two items, as ITEMS=2 cuts it, the plan has no fault item, so R5 fails too and the class is control;
# pl was never leased, so its project is in no poll.
for m in recording judging; do
  amode arm-cut "$m"; gedit plan.json '.items |= .[:2]'; rm -r "$gd/pl"
  jq -c '.projects = ["laug-1a2b3c4d","laug-9c0d1e2f"]' "$AFX/poll.jsonl" > "$gd/poll.jsonl"; gjudge
  if [[ "$m" == judging ]]; then want='["BLOCK","control",["R2","R5","R7"]]|["not compared"]'; else want='["BLOCK","control",["R2","R5"]]|["recorded"]'; fi
  eq "$m, a cut plan (plan.json's items the first two, items_in_plan 3): R2 fails, R7 $([[ "$m" == judging ]] && echo "too, on the wave" || echo "holds, every row recorded: the wave guard is judging only")" \
    "$(gsum)|$(arows)" "$want"
done
for m in recording judging; do
  amode arm-start "$m"; graun steady '.start = "2026-10-14T14:02:33Z"'; gjudge
  if [[ "$m" == judging ]]; then want='["BLOCK","invalid",["R7"]]|["incumbent: not compared: the windows of steady and incumbent started 3 s apart (2 s at most)"]'
  else want='["ADVANCE",null,[]]|[]'; fi
  eq "$m, steady's run JSON starts 3 s later: $([[ "$m" == judging ]] && echo "BLOCK, class invalid, the windows text on the incumbent" || echo "ADVANCE, R7 holds"), every row not compared" \
    "$(gsum)|$(ar7)|$(arows)" "$want|[\"not compared\"]"
done
for m in recording judging; do
  amode arm-length "$m"; gedit "$(avj steady)" '.window_s = 299'; gjudge
  if [[ "$m" == judging ]]; then want='["BLOCK","invalid",["R7"]]|["incumbent: not compared: the windows of steady and incumbent were 299 s and 322 s long (2 s apart at most)"]'
  else want='["ADVANCE",null,[]]|[]'; fi
  eq "$m, steady's window_s 299: $([[ "$m" == judging ]] && echo "BLOCK, class invalid, the lengths on the incumbent" || echo "ADVANCE, R7 holds"), every row not compared" \
    "$(gsum)|$(ar7)|$(arows)" "$want|[\"not compared\"]"
done
anj() { gedit "$1" '.thresholds |= map(if .name == $n then .no_data = "not_judged" else . end)' --arg n "$2"; }   # anj <set file> <reading>
acase arm-nj; ajudging; anj thresholds.json activity_schedule_to_start_over_10ms; gedit "$(avj steady)" '.readings.activity_schedule_to_start_over_10ms = null'; gjudge
eq "judging, steady's activity share null, thresholds.json marking it not_judged: not produced, ADVANCE" \
  "$(gsum)|$(arow activity_schedule_to_start_over_10ms)" '["ADVANCE",null,[]]|[null,0.0905,null,0.005,true,"not produced"]'
for m in recording judging; do
  amode arm-nodata "$m"; gedit "$(avj steady)" '.readings.frontend_request_over_10ms = null'; gjudge
  if [[ "$m" == judging ]]; then
    want='["BLOCK","invalid",["R7"]]|["ADVANCE","invalid","not compared: no data on steady for frontend_request_over_10ms"]|[]|[null,0.0861,null,0.005,true,"not compared"]'
  else want='["ADVANCE",null,[]]|["ADVANCE",null,""]|[]|[null,0.0861,null,null,false,"recorded"]'; fi
  eq "$m, steady's frontend share null, unmarked: $([[ "$m" == judging ]] && echo "BLOCK, class invalid, no data on steady's item, none on the incumbent's" || echo "ADVANCE, the row recorded with release null")" \
    "$(gsum)|$(gitem steady)|$(jq -c '.items[] | select(.key == "incumbent") | .failed' "$gv")|$(arow frontend_request_over_10ms)" "$want"
done
acase arm-tag; ajudging; graun incumbent '.server_version = "1.32.1"'; gjudge
eq "judging, the incumbent's run JSON says server_version 1.32.1: BLOCK, R6 on incumbent against the plan's incumbent" "$(gsum)|$(gitem incumbent)" \
  '["BLOCK","invalid",["R6"]]|["ADVANCE","invalid","identity: the cell said server_version 1.32.1, and the plan names incumbent 1.31.0"]'
acase arm-nothi; ajudging; rm "$gd/thresholds-incumbent.json"; gjudge
eq "judging, thresholds-incumbent.json removed: BLOCK, class invalid, R6 says so" "$(gsum)|$(jq -c '.rules.R6.reasons' "$gv")" \
  '["BLOCK","invalid",["R6"]]|["thresholds-incumbent.json is missing, so the thresholds of the incumbent are unknown"]'
acase arm-host; ajudging; gedit gate.json '.host.type = "other"'; gjudge
eq "judging, gate.json host type other: BLOCK, class invalid, R6 with the host text" "$(gsum)|$(jq -c '.rules.R6.reasons' "$gv")" \
  '["BLOCK","invalid",["R6"]]|["gate.json records host type other with 32 vCPU and 123 GiB, and thresholds.json names a set measured on m8i.8xlarge with 32 vCPU and 123 GiB"]'
acase arm-error; ajudging; gedit "$(avj steady)" '.readings.service_error_rate = 0.01'; gjudge
eq "judging, the error rate 0.01 on steady: recorded, R7 holds, ADVANCE" "$(gsum)|$(arow service_error_rate)" '["ADVANCE",null,[]]|[0.01,0,0.01,null,false,"recorded"]'
acase arm-minor; ajudging; gedit plan.json '.incumbent = "1.32.1"'; graun incumbent '.server_version = "1.32.1"'; gjudge
eq "judging, the incumbent of another minor (plan incumbent 1.32.1, its run JSON 1.32.1): ADVANCE, R7 judges, the note in verdict.md" \
  "$(gsum)|$(jq -c '[.comparison.mode, .comparison.notes]' "$gv")|$(grep -cxF 'Tolerances measured with 1.31.0 on both arms; the incumbent of this gate is 1.32.1.' "$gd/verdict.md")" \
  '["ADVANCE",null,[]]|["judging",["Tolerances measured with 1.31.0 on both arms; the incumbent of this gate is 1.32.1."]]|1'
eq "--classify: worse than the incumbent: x is regression; incumbent: x and not compared: x are invalid" \
  "$(bin/laug judge -classify 'worse than the incumbent: x')|$(bin/laug judge -classify 'incumbent: x')|$(bin/laug judge -classify 'not compared: x')" "regression|invalid|invalid"
acase arm-novj; ajudging; rm "$gd/$(avj steady)"; gjudge
eq "judging, steady's verdict JSON removed: BLOCK, class invalid; R2 on steady with its own reason, R7 in steady's failed list, no verdict to compare" \
  "$(gsum)|$(gitem steady)|$(jq -c '.items[] | select(.key == "steady") | .failed' "$gv")|$(ar7)" \
  "[\"BLOCK\",\"invalid\",[\"R2\",\"R3\",\"R4\",\"R6\",\"R7\"]]|[null,\"invalid\",\"no JSON: steady/$GID-steady.verdict.json is missing\"]|[\"R2\",\"R3\",\"R4\",\"R6\",\"R7\"]|[\"steady: not compared: steady has no verdict to compare\"]"
acase arm-nostart; ajudging; graun incumbent '.start = "x"'; gjudge
eq "judging, the incumbent's run JSON start is x: BLOCK, class invalid, no start to pair the windows by" "$(gsum)|$(ar7)" \
  '["BLOCK","invalid",["R7"]]|["incumbent: not compared: the run JSON of steady or incumbent records no start, so the two windows cannot be seen to be paired"]'
acase arm-nowindow; ajudging; gedit "$(avj incumbent)" 'del(.window_s)'; gjudge
eq "judging, the incumbent's verdict JSON has no window_s: BLOCK, class invalid, no window_s" "$(gsum)|$(ar7)" \
  '["BLOCK","invalid",["R7"]]|["incumbent: not compared: the verdict JSON of steady or incumbent records no window_s"]'
acase arm-inc-nj; ajudging; anj thresholds-incumbent.json activity_schedule_to_start_over_10ms
gedit "$(avj incumbent)" '.readings.activity_schedule_to_start_over_10ms = null'; gjudge
eq "judging, the incumbent's activity share null, thresholds-incumbent.json marking it not_judged: not produced, ADVANCE" \
  "$(gsum)|$(arow activity_schedule_to_start_over_10ms)" '["ADVANCE",null,[]]|[0.0905,null,null,0.005,true,"not produced"]'
acase arm-inc-nodata; ajudging; gedit "$(avj incumbent)" '.readings.activity_schedule_to_start_over_10ms = null'; gjudge
eq "judging, the same unmarked: BLOCK, class invalid, no data on the incumbent's item" "$(gsum)|$(gitem incumbent)" \
  '["BLOCK","invalid",["R7"]]|["ADVANCE","invalid","not compared: no data on incumbent for activity_schedule_to_start_over_10ms"]'
acase arm-share; ajudging; gedit "$(avj steady)" '.readings.frontend_request_over_10ms += 0.006'; gjudge
eq "judging, steady's frontend share 0.006 over the incumbent's on a 0.005 tolerance: WORSE, in percent and points" "$(gsum)|$(gitem steady)" \
  '["BLOCK","regression",["R7"]]|["ADVANCE","regression","worse than the incumbent: frontend_request_over_10ms 9.21% against 8.61% on incumbent, +0.6000 points past a tolerance of 0.50 points"]'
acase arm-share-at; ajudging; gedit "$(avj steady)" '.readings.frontend_request_over_10ms += 0.005'; gjudge; o1="$(gsum)|$(arow frontend_request_over_10ms | jq -c '.[2:]')"
acase arm-share-past; ajudging; gedit "$(avj steady)" '.readings.frontend_request_over_10ms += 0.005001'; gjudge
eq "judging, a share at its tolerance and just past it: 0.005 over holds, ok; 0.005001 over fails, +0.5001 points" "$o1#$(gsum)|$(gitem steady)" \
  '["ADVANCE",null,[]]|[0.005,0.005,true,"ok"]#["BLOCK","regression",["R7"]]|["ADVANCE","regression","worse than the incumbent: frontend_request_over_10ms 9.11% against 8.61% on incumbent, +0.5001 points past a tolerance of 0.50 points"]'
acase arm-norel; ajudging; gedit thresholds-incumbent.json 'del(.resolved.release_file)'; gjudge
eq "judging, thresholds-incumbent.json with no resolved.release_file: BLOCK, R6 says so" "$(gsum)|$(jq -c '.rules.R6.reasons' "$gv")" \
  '["BLOCK","invalid",["R6"]]|["thresholds-incumbent.json has no resolved.release_file"]'
acase arm-nohost; ajudging; gedit thresholds.json 'del(.resolved.host)'; gjudge
eq "judging, thresholds.json with no resolved.host: BLOCK, R6 says this host cannot be seen to be the set's" "$(gsum)|$(jq -c '.rules.R6.reasons' "$gv")" \
  '["BLOCK","invalid",["R6"]]|["thresholds.json names no host type, so this host cannot be seen to be the one its numbers came from"]'
acase arm-negative; ajudging; gedit thresholds.json '.compare.rows |= map(if .name == "persistence_latency_p99_ms" then .tolerance = -0.1 else . end)'; gjudge
eq "judging, a tolerance -0.1 in thresholds.json: BLOCK, class invalid, the row named on the incumbent, every row not compared" "$(gsum)|$(ar7)|$(arows)" \
  '["BLOCK","invalid",["R7"]]|["incumbent: not compared: compare row persistence_latency_p99_ms has tolerance -0.1 and floor 0.1 in thresholds.json: a tolerance is a number at or above its floor, and a floor a number above 0"]|["not compared"]'
acase arm-quoted; gedit thresholds.json '.compare.rows |= map(if .name == "persistence_latency_p99_ms" then .tolerance = "0.3" else . end)'; gjudge
eq "a tolerance \"0.3\", quoted, and every other tolerance null: judging all the same, BLOCK, class invalid, the text with \"0.3\"" \
  "$(gsum)|$(jq -c '.comparison.mode' "$gv")|$(ar7)" \
  '["BLOCK","invalid",["R7"]]|"judging"|["incumbent: not compared: compare row persistence_latency_p99_ms has tolerance \"0.3\" and floor 0.1 in thresholds.json: a tolerance is a number at or above its floor, and a floor a number above 0"]'

# A plan.json no preflight read (gate/lib.sh refuses each): the incumbent's pair names no item of the plan, so R7
# fails in either mode, class invalid, on the incumbent, every row not compared: that gate compared nothing, and is no
# A/A sample either. An item with arm incumbent in a plan that names no incumbent fails R6 on that item.
for m in recording judging; do
  amode arm-nopair "$m"; gedit plan.json '(.items[] | select(.arm == "incumbent") | .pair) = "nosuch"'; gjudge
  eq "$m, the incumbent's pair nosuch, no item of the plan: BLOCK, class invalid, R7 alone, the pair named on the incumbent, every row not compared" \
    "$(gsum)|$(ar7)|$(gitem incumbent)|$(arows)" \
    '["BLOCK","invalid",["R7"]]|["incumbent: not compared: the pair nosuch is not an item of this plan"]|["ADVANCE","invalid","not compared: the pair nosuch is not an item of this plan"]|["not compared"]'
done
acase arm-pair3; gedit plan.json '(.items[] | select(.arm == "incumbent") | .pair) = 3'; gjudge
eq "recording, the incumbent's pair 3, not a key: exit 1, a verdict written, R7 names the pair as JSON" "$grc|$(gsum)|$(ar7)" \
  '1|["BLOCK","invalid",["R7"]]|["incumbent: not compared: the pair 3 is not an item of this plan"]'
acase arm-noinc; gedit plan.json 'del(.incumbent)'; gjudge
eq "an item with arm incumbent, and plan.json names no incumbent: BLOCK, class invalid, R6 on the incumbent's item" \
  "$(gsum)|$(gitem incumbent)|$(jq -c '.rules.R6.reasons' "$gv")" \
  '["BLOCK","invalid",["R6"]]|["ADVANCE","invalid","identity: the item says arm incumbent, and the plan names no incumbent"]|["incumbent: identity: the item says arm incumbent, and the plan names no incumbent"]'

# Every reason faults/run.sh and load/lost-work.sh can write has a class (laug judge -classify; internal/judge's
# classes.go, class_table). The literals
# are read out of the scripts: each reason="..." and reason: "..." in run.sh, each reasons+=("...") in lost-work.sh,
# cut at the first variable, which reads as 1. One that starts with $reason or $lost_reasons joins these; $n is a count.
reasons_in() {
  { grep -oE '(^|[^_[:alnum:]])reason(=|: )"[^"]*' faults/run.sh; grep -oE 'reasons\+=\("[^"]*' load/lost-work.sh; } | sed 's/^[^"]*"//' \
    | while IFS= read -r lit; do
        [[ -n "$lit" && "$lit" != '$reason'* && "$lit" != '$lost_reasons'* ]] || continue
        [[ "$lit" != '$n '* ]] || lit="1${lit#\$n}"
        r="${lit%%\$*}"; [[ "$r" == "$lit" ]] || r+=1
        echo "$r"
      done | sort -u
}
n_reasons=0
while IFS= read -r r; do
  n_reasons=$(( n_reasons + 1 )); c="$(bin/laug judge -classify "$r")"
  if [[ "$c" != unknown ]]; then pass "reason \"$r\" is class $c"; else fail "reason \"$r\" has no class" "add its prefix to class_table in internal/judge/classes.go"; fi
done < <(reasons_in)
(( n_reasons >= 16 )) && pass "$n_reasons reasons read out of faults/run.sh and load/lost-work.sh (16 on 2026-10-14)" \
  || fail "only $n_reasons reasons read out of faults/run.sh and load/lost-work.sh; 16 on 2026-10-14" "$(reasons_in | paste -sd '|')"

# gate/post.sh: GATE_POST=local hands the verdict on and changes neither file; anything else is not wired.
gcase post; gjudge; cp "$gd/verdict.json" "$gd/verdict.md" "$work/gate/"
out="$(gate/post.sh "$gd" 2>&1)"; rc=$?
eq "post, local: exit 0, both paths, the verdict on the last line" "$rc|$(sed -n '2,3p' <<<"$out" | paste -sd '|')|$(tail -1 <<<"$out")" \
  "0|verdict.json  $gd/verdict.json|verdict.md    $gd/verdict.md|VERDICT: ADVANCE"
has "post, local: says nothing is published" "$(head -1 <<<"$out")" "laug · gate $GID handed on to local (GATE_POST=local): nothing is published"
cmp -s "$gd/verdict.json" "$work/gate/verdict.json" && cmp -s "$gd/verdict.md" "$work/gate/verdict.md" \
  && pass "post, local: verdict.json and verdict.md unchanged" || fail "post, local: verdict.json and verdict.md unchanged"
out="$(GATE_POST=github-summary gate/post.sh "$gd" 2>&1)"; rc=$?
eq "post, any other sink: exit 2, not wired" "$rc|$out" "2|laug · GATE_POST=github-summary: not wired: laug has no publish target yet; GATE_POST=local is the only sink"
gcase post-block; graun pl "$DNR"; gexit pl 1; gjudge
out="$(gate/post.sh "$gd" 2>&1)"; rc=$?
eq "post, local, a BLOCK: still exit 0, the class and the verdict" "$rc|$(tail -2 <<<"$out" | paste -sd '|')" "0|class         regression|VERDICT: BLOCK"
sed -i 's/^\*\*BLOCK\*\*$/**ADVANCE**/' "$gd/verdict.md"; out="$(gate/post.sh "$gd" 2>&1)"; rc=$?
eq "post: a verdict.md that disagrees with verdict.json: exit 2" "$rc" 2
rm "$gd/verdict.json"; out="$(gate/post.sh "$gd" 2>&1)"; rc=$?
eq "post: no verdict.json: exit 2, nothing handed on" "$rc|$out" "2|laug · nothing to hand on: no $gd/verdict.json (bin/laug judge writes it)"
# A run is a name in -results, or a path, which holds a slash; .. is neither.
mgp() { bin/laug gate post "$@" -results "$work/gate" 2>&1; }
out="$(mgp post)"; rc=$?
eq "laug gate post post: exit 0, the verdict" "$rc|$(tail -1 <<<"$out")" "0|VERDICT: ADVANCE"
out="$(mgp post -post pr-comment)"; rc=$?
eq "laug gate post -post pr-comment: exit 2, not wired" "$rc|$(head -1 <<<"$out")" \
  "2|laug · GATE_POST=pr-comment: not wired: laug has no publish target yet; GATE_POST=local is the only sink"
out="$(mgp)"; rc=$?
eq "laug gate post with no run: exit 2" "$rc|$(head -1 <<<"$out")" "2|laug · gate post: names no gate run: give it a gate id in $(shown "$work/gate"), or the path to a run's directory"
out="$(mgp ..)"; rc=$?
eq "laug gate post ..: exit 2, neither a name in -results nor a path" "$rc" 2

section "laug gate rejudge and laug gate compare (the same-verdict checks: S1 rejudge and S2 signatures)"
# laug gate rejudge judges a run again in Go (internal/rejudge), under the judge version its gate.json records, and
# compares verdict.json and verdict.md with the run's byte for byte (S1); laug gate compare <A> <B> compares two runs'
# signatures (S2). The cases are judged copies of the base fixture, which has no timeline.jsonl: gtl writes one in
# gate/run.sh's shape, the marks and events of a three-item run, its items ending in the order given. Each copy's
# gate.json records gate/combine.sh with CSHA, v3's sha256 in internal/judge/versions.txt, as a run from before
# bin/laug judge records its judge. laug holds its own judge to HEAD's with git status under internal/judge and
# internal/jsonx; mgc runs it with the git in cmp/bin, which says that is clean, so a judge edit not yet committed fails
# no case here.
CR="$work/cmp"; mkdir -p "$CR/bin"; CSHA="$(awk '$1 == "v3" { print $2 }' internal/judge/versions.txt)"
printf '%s\n' '#!/usr/bin/env bash' '[[ "$*" == "status --porcelain -- internal/judge internal/jsonx" ]] && exit 0' \
  'exec "$REAL_GIT" "$@"' > "$CR/bin/git"
chmod +x "$CR/bin/git"
gtl() { # gtl [<key> ...]: a timeline, items ending in this order (steady kh pl by default)
  local s i=0 ended=("$@"); (( $# )) || ended=(steady kh pl)
  for s in start lock plan preflight omes_build laugd harness "lease steady" "leased steady" "lease pl" "leased pl" "lease kh" \
      "leased kh" barrier wave "${ended[@]/#/ended }" "wave end" combine post release "laugd stopped" done; do
    printf '{"step":"%s","wall":%d.000,"mono":%d.00}\n' "$s" $(( 1791986400 + i * 20 )) $(( 81000 + i * 19 )); i=$(( i + 1 ))
  done
}
ccase() { # ccase <name> [<key> ...]: a judged copy of the fixture, with a timeline, in gd
  gd="$CR/$1"; shift; rm -rf "$gd"; cp -r "$GFX" "$gd"; gtl "$@" > "$gd/timeline.jsonl"
  gedit gate.json '.inputs += [{path: "gate/combine.sh", sha256: $s, changed_vs_base: false}]' --arg s "$CSHA"; gjudge
}
# g20261014-212013 and -212526, dev host runs in internal/rejudge/testdata/runs, were refused at a lease with steady/
# made and empty, which git does not carry: a fresh clone judges "no JSON: there is no steady/ directory" where the run
# judged "steady/exit.json is missing". cempty makes that case with kh: judged with kh/ empty, then kh/ removed.
cempty() { ccase "$1"; rm "$gd"/kh/*; gjudge; rmdir "$gd/kh"; }
# cstop <name>: a run stopped at the wave, with a trap verdict in gate/run.sh's shape
cstop() {
  ccase "$1"; gedit verdict.json '.stopped_at = "wave" | .rules = null | .items = []'
  printf '%s\n' "## laug · verdict for raun 0000000" "" "Gate run $GID on server 1.31.0 stopped at step wave before gate/combine.sh judged it: stopped: gate/run.sh stopped at step wave (signal TERM) before gate/combine.sh judged; nothing was judged." \
    "" "Class: invalid." "" "**BLOCK**" > "$gd/verdict.md"
}
mgc() { local r="$1"; shift; PATH="$CR/bin:$PATH" bin/laug gate "$@" -results "$r" 2>&1; }   # mgc <results> <verb> [<arg>...]
rja() { # rja <results>: laug gate rejudge -all's first line
  printf 'laug · gate rejudge -all: every run in %s, judged again in Go under the judge version its gate.json records, a line each, then the counts' "$1"
}

# The runs laug gate rejudge -all reads below: same, judged as it is; stopped, cstop's; emptymd, judged with kh/ empty,
# then kh/ gone, and one line added to its verdict.md; nojson, with no verdict.json.
ccase same
cstop stopped
cempty emptymd; echo tampered >> "$gd/verdict.md"
ccase nojson; rm "$gd/verdict.json"
# Every committed run names a judge version laug holds: a gate/combine.sh sha256, or for a run bin/laug judge judged,
# an internal/judge version, that internal/judge/versions.txt has a line for.
jn=0; jo=""
for jf in gate/results/*/gate.json; do
  [[ -f "$jf" ]] || continue; jn=$(( jn + 1 )); jd="${jf%/gate.json}"
  js="$(jq -r '[.inputs[]? | objects | select(.path == "gate/combine.sh") | .sha256 | strings][0] // ""' "$jf" 2>/dev/null)"
  jv="$(jq -r '[.inputs[]? | objects | select(.path == "internal/judge") | .version | strings][0] // ""' "$jf" 2>/dev/null)"
  { [[ "$js" =~ ^[0-9a-f]{64}$ ]] && awk -v s="$js" '$2 == s { f = 1 } END { exit !f }' internal/judge/versions.txt; } \
    || { [[ -n "$jv" ]] && awk -v n="$jv" '$1 == n { f = 1 } END { exit !f }' internal/judge/versions.txt; } || jo+="${jo:+ }${jd##*/}"
done
eq "every gate run in gate/results names a judge version internal/judge/versions.txt holds ($jn runs)" "$jo" ""

ccase other kh pl steady; gedit gate.json '.laug_commit = "0000000" | .host.mem_available_gib = 12.5'; gedit clock.json '.t_post.wall += 7 | .t_post.mono += 6.7'; gjudge

out="$(mgc "$CR" compare same)"; rc=$?
out2="$(mgc "$CR" rejudge ..)"; rc2=$?
eq "laug gate compare with one run: exit 2, the usage; laug gate rejudge ..: exit 2, neither a name in -results nor a path" \
  "$rc|$(head -1 <<<"$out")|$rc2|$(head -1 <<<"$out2")" \
  "2|laug · gate compare: takes two gate runs, A and B, got [\"same\"]|2|laug · gate rejudge: ..: neither a gate id in $(shown "$CR"), one word of letters, digits, ., _ and -, nor the path to a run's directory"
out="$(mgc "$CR" rejudge same)"; rc=$?
eq "laug gate rejudge same, and laug gate compare same other: exit 0, identical, equal" \
  "$rc|$(sed -n '2,3p' <<<"$out" | paste -sd '|')|$(mgc "$CR" compare same other)" \
  "0|verdict.json  identical|verdict.md    identical|laug · S2, A same, B other: equal"
# cmp-all holds, besides copies of same and the cases above, kn, an empty item directory the GONE rule names, and t, a
# committed trap verdict (T2), g20261017-014632 as internal/rejudge/testdata/lab holds it. s, cstop's stopped run, is
# not a trap form's output: laug, which re-renders a trap verdict by its form, calls it unexplained.
cempty kn
CA="$work/cmp-all"; mkdir -p "$CA"
cp -r "$CR/same" "$CA/g1"; cp -r "$CR/same" "$CA/g1-2"; cp -r "$CR/same" "$CA/my run"; cp -r "$CR/nojson" "$CA/n"; cp -r "$CR/stopped" "$CA/s"
cp -r "$CR/emptymd" "$CA/u"; cp -r "$CR/kn" "$CA/e"; cp -r internal/rejudge/testdata/lab/g20261017-014632 "$CA/t"
out="$(mgc "$CA" rejudge -all)"; rc=$?
eq "laug gate rejudge -all: its first line, a line each, by name (g1 before g1-2, a name that is no id judged too), then the counts; exit 2, one not judged" \
  "$rc|$(paste -sd '|' <<<"$out")" \
  "2|$(rja "$CA")|e  differs: an empty item directory (kh/), which git does not carry: a fresh clone judges it as no directory|g1  identical|g1-2  identical|my run  identical|n  not judged: no verdict.json in (outside the repository)/n to compare with|s  differs: unexplained|t  re-rendered: trap T2|u  differs: unexplained|laug · rejudge, 8 runs: 3 identical, 1 known, 1 re-rendered, 2 unexplained, 1 not judged"
rm -r "$CA/n" "$CA/u" "$CA/s"; out="$(mgc "$CA" rejudge -all)"; rc=$?
eq "with none unexplained and none not judged, laug gate rejudge -all exits 0" "$rc|$(tail -1 <<<"$out")" \
  "0|laug · rejudge, 5 runs: 3 identical, 1 known, 1 re-rendered, 0 unexplained, 0 not judged"
mkdir -p "$work/cmp-none"; out="$(mgc "$work/cmp-none" rejudge -all)"; rc=$?; o1="$(paste -sd '|' <<<"$out")|$rc"
out="$(mgc "$work/cmp-nowhere" rejudge -all)"; rc=$?
eq "laug gate rejudge -all over an empty -results, and over none: exit 2 each, never 0 runs judged as a pass" "$o1|$(paste -sd '|' <<<"$out")|$rc" \
  "$(rja "$work/cmp-none")|laug · gate rejudge: no gate run in (outside the repository)/cmp-none|2|$(rja "$work/cmp-nowhere")|laug · gate rejudge: no results directory at (outside the repository)/cmp-nowhere; -results names none|2"

section "gate/run.sh and laug gate run (whole gate runs with GATE_FAKE=1, laugd on a fake backend, fake items)"
# Each case is one whole gate run. With GATE_FAKE=1, gate/run.sh puts load/testdata/gate/fakebin first on PATH (docker,
# ss, nproc, free, go), runs the real bin/laug pool serve with load/testdata/gate/fake-backend.sh (a cell is a file,
# which the fake docker's compose ls lists), load/testdata/gate/fake-item.sh in place of every item's scripts (it records
# what it was given and writes the gate fixtures' JSON) and load/testdata/gate/fake-harness.sh in place of make
# harness-test.
# Each case has its own results directory and fake world. All share two lock files and one free loopback port, none of
# them a real gate's: a real gate runs this test while it holds gate/.lock, the host lock and 127.0.0.1:7700. The plan
# is load/testdata/gate/plan.yaml's (the three-item plan as built: steady, pl and kh) with short times: deadline 30 s,
# kill grace 1 s, quiet 0 s, a poll each second, no wave_min_s. Cases 1 and 9b run as a reader runs a gate, laug gate
# run -fake, with -allow-stale: make build at the top stamped bin/laug from this tree, and on a tree with changes not
# committed the stamp check's warning line would land in what they read; the README block's gate keeps that check.
# The rest run gate/run.sh itself (grun).
# grep reads ss's lines from a string, here and below, as gate/run.sh's preflight does. Under pipefail, grep -q can
# find the port and quit while ss still writes. ss then exits 141, and a taken port reads as free.
GP=""; for p in $(seq 17790 17889); do grep -qE "[.:]$p[[:space:]]" <<<"$(ss -ltn 2>/dev/null)" || { GP="$p"; break; }; done
GLOCK="$work/gate.lock"; GHLOCK="$work/gate-host.lock"
GENV=(GATE_FAKE=1 SERVER_TAG=1.31.0 GATE_LOCK="$GLOCK" GATE_HOST_LOCK="$GHLOCK" GATE_POOL_LISTEN="127.0.0.1:$GP")
gplan() { # gplan <out> [<yq edit>] [<plan>]: a plan with the short times
  yq '.deadline_s = 30 | .kill_grace_s = 1 | .lease_ttl_s = 120 | .quiet_s = 0 | .poll_s = 1 | .harness_timeout_s = 20
    | .barrier_timeout_s = 2 | .wave_min_s = 0'"${2:+ | $2}" "${3:-$td/gate/plan.yaml}" > "$1"
}
gplan "$work/gp.yaml"; gplan "$work/gp-deadline.yaml" '.deadline_s = 5'; gplan "$work/gp-harness.yaml" '.harness_timeout_s = 1'
gplan "$work/gp-late.yaml" '.deadline_s = 12 | .wave_min_s = 7'; gplan "$work/gp-later.yaml" '.wave_min_s = 30'
gplan "$work/gp-noise.yaml" '' gate/plans/noise.yaml
gworld() { gw="$work/gr/$1"; rm -rf "$gw"; mkdir -p "$gw/results" "$gw/fake" "$gw/omes/cmd/omes"; }
gfound() { gr="$(ls -d "$gw/results"/g* 2>/dev/null | head -1)"; gv="$gr/verdict.json"; GID2="$(basename "$gr")"; }
grun() { # grun <case> [VAR=value...]: one whole gate run by gate/run.sh; its output in gout, its exit in grc, its directory in gr
  local c="$1"; shift; gworld "$c"
  gout="$(env "${GENV[@]}" PLAN="$work/gp.yaml" GATE_RESULTS="$gw/results" OMES_DIR="$gw/omes" FAKE_GATE_DIR="$gw/fake" "$@" gate/run.sh 2>&1)"; grc=$?
  gfound
}
gj() { jq -c "$1" "$gr/${2:-verdict.json}" 2>&1; }   # gj <jq> [file]: one of the run's files, verdict.json by default
gi() { jq -c --arg k "$1" '.items[] | select(.key == $k) | [.observed, .class, .stopped_by, .exit]' "$gv" 2>&1; }
gfi() { jq -s -c "${@:2}" "$1" "$gr"/steady/fake-item.json "$gr"/pl/fake-item.json "$gr"/kh/fake-item.json 2>&1; }   # what each item was given
gstarted() { ls "$gw/fake" | grep -c '^started\.'; }
# gstopped: nothing listens on GP, and no gate's own pool, bin/laug pool serve -pid-file none, runs for it.
gstopped() { ! grep -qE "[.:]$GP[[:space:]]" <<<"$(ss -ltn 2>/dev/null)" && ! pgrep -f "pool serve -pid-file none -listen 127.0.0.1:$GP( |$)" >/dev/null; }
gend() { # gend <case>: every way out leaves verdict.json and verdict.md, handed on by gate/post.sh, and laugd stopped
  eq "$1: verdict.json and verdict.md written and handed on (one VERDICT line), laugd stopped" \
    "$([[ -s "$gv" && -s "$gr/verdict.md" ]] && echo written)|$(grep -c '^VERDICT: ' <<<"$gout")|$(gstopped && echo stopped)" "written|1|stopped"
}
# gstopped's own check: a stand-in whose command line is a gate's pool on GP, a sleep that binds nothing, is not
# stopped; once it ends, nothing is.
( exec -a "bin/laug pool serve -pid-file none -listen 127.0.0.1:$GP -min 0" "$REAL_SLEEP" 30 ) >/dev/null 2>&1 & gsp=$!
for _ in $(seq 1 100); do [[ "$(tr '\0' ' ' </proc/$gsp/cmdline 2>/dev/null)" == "bin/laug pool serve "* ]] && break; "$REAL_SLEEP" 0.05; done
eq "gstopped: a gate's pool for 127.0.0.1:$GP that binds nothing yet is not stopped" "$(gstopped && echo stopped || echo up)" "up"
kill "$gsp" 2>/dev/null; wait "$gsp" 2>/dev/null
eq "gstopped: with it gone, nothing for 127.0.0.1:$GP runs" "$(gstopped && echo stopped || echo up)" "stopped"

# 1. Every item ADVANCE, through laug gate run -fake: the run in gw/results, the fakes' files in gw/fake, and the empty
# Omes -fake makes in its dir under TMPDIR, gw/tmp.
gworld main; mkdir -p "$gw/tmp"
gout="$(env GATE_LOCK="$GLOCK" GATE_HOST_LOCK="$GHLOCK" GATE_POOL_LISTEN="127.0.0.1:$GP" FAKE_GATE_DIR="$gw/fake" FAKE_GATE_CREATE_S=0.3 TMPDIR="$gw/tmp" \
  bin/laug gate run 1.31.0 -fake -allow-stale -plan "$work/gp.yaml" -results "$gw/results" 2>&1)"; grc=$?
gfound; gomes="$(ls -d "$gw"/tmp/laug-fake-gate-*/omes 2>/dev/null)"
eq "laug gate run 1.31.0 -fake: ADVANCE, exit 0, every item ADVANCE, host quiet" "$grc|$(gj '[.verdict, .class, [.items[].observed], .host_quiet]')" \
  '0|["ADVANCE",null,["ADVANCE","ADVANCE","ADVANCE"],true]'
eq "laug gate run: its first line, then gate/run.sh's, which names the gate run; then the verdict, and last its dir" \
  "$(head -1 <<<"$gout" | cut -d: -f1)|$(sed -n 2p <<<"$gout" | cut -d: -f1)|$(tail -2 <<<"$gout" | head -1)|$(tail -1 <<<"$gout" | sed 's|in .*/|in .../|')" \
  "laug · gate run 1.31.0 -fake|laug · gate $GID2|ADVANCE|laug · gate $GID2: on fakes, its files are in .../$(basename "$(dirname "$gomes")")"
# The fakebin's nproc, free and docker answer for this host under GATE_FAKE=1: the lines that quote them say so.
eq "laug gate run -fake: the preflight and barrier lines mark the fixtures' values as not this host's" \
  "$(grep -c "preflight .* no compose project up (GATE_FAKE=1: the fixtures', not this host's), nothing on 127.0.0.1:$GP;" <<<"$gout")|$(grep -c "barrier .* the 3 leased (GATE_FAKE=1: the fake backend's), still so" <<<"$gout")" "1|1"
eq "laug gate run -fake: verdict.json, verdict.md and the VERDICT line say no cell ran" \
  "$(gj .fake)|$(head -1 "$gr/verdict.md" | grep -c 'GATE_FAKE=1: fixtures, no cell ran')|$(grep '^VERDICT: ' <<<"$gout")" \
  'true|1|VERDICT: ADVANCE (GATE_FAKE=1: fixtures, no cell ran)'
eq "laugd's /status reported the gate's mark, a max_lease of 2 h and a 6-digit fraction" \
  "$(grep -cE 'max lease 2h0m0\.[0-9]{5}[1-9]s' "$gr/laugd.log")" 1
eq "each item: RUN_ID <gate id>-<key>, RESULTS_DIR its own directory, its own lease file, given as its last argument" \
  "$(gfi 'map([.run_id == "\($id)-\(.key)", .results_dir == "\($d)/\(.key)", .lease_file == "\($d)/\(.key)/.lease.json", .lease_arg == .lease_file]) | flatten | unique' \
     --arg id "$GID2" --arg d "$gr")" '[true]'
eq "the three items: three run ids, three results directories, three lease files" "$(gfi '[(map(.run_id), map(.results_dir), map(.lease_file)) | unique | length]')" '[3,3,3]'
eq "three leases: three cells, three port blocks, requesters gate-<gate id>-<key>" \
  "$(jq -s -c --arg id "$GID2" '[(map(.id) | unique | length), (map(.block) | unique | length), (map(.requester) == ["gate-\($id)-steady", "gate-\($id)-pl", "gate-\($id)-kh"])]' \
     "$gr"/steady/lease.json "$gr"/pl/lease.json "$gr"/kh/lease.json 2>&1)" '[3,3,true]'
eq "Omes metrics ports: 127.0.0.1:19100+2i and 19101+2i, six of them" "$(gfi '[.[] | .worker_metrics_addr, .client_metrics_addr]')" \
  '["127.0.0.1:19100","127.0.0.1:19101","127.0.0.1:19102","127.0.0.1:19103","127.0.0.1:19104","127.0.0.1:19105"]'
eq "each item under setsid: a session and process group of its own, which it leads" "$(gfi 'map(.pid == .pgid and .pid == .sid) + [map(.sid) | unique | length]')" '[true,true,true,3]'
eq "the three items started within 1 s of one another" "$(gfi '(map(.start) | max - min) < 1')" true
eq "each item started once: started.<key> holds one line, the pid fake-item.json records" \
  "$(for k in steady pl kh; do wc -l < "$gw/fake/started.$k"; done 2>&1 | paste -sd ' ')|$(cat "$gw"/fake/started.{steady,pl,kh} 2>&1 | paste -sd ' ')" \
  "1 1 1|$(gfi 'map(.pid | tostring) | join(" ")' -r)"
# A background job of a non-interactive shell starts with SIGINT and SIGQUIT ignored, and no trap can take them back:
# make harness-test's Ctrl-C checks failed so in internal/rejudge/testdata/runs/g20261014-184524; the gate resets both.
eq "each item: SIGINT and SIGQUIT at their defaults, as from a terminal" "$(gfi 'map([.sigint_ignored, .sigquit_ignored]) | unique')" '[[false,false]]'
eq "make harness-test (the fake): SIGINT at its default, the tally 1 passed, 0 failed" \
  "$(grep -c '^SIGINT at its default$' "$gr/harness.log")|$(jq -r .tally "$gr/harness.json")" "1|1 passed, 0 failed"
eq "each item: POOL_QUIET_WAIT_S=0, and POOL_ADDR the gate's laugd" "$(gfi 'map([.pool_quiet_wait_s, .pool_addr == $p]) | unique' --arg p "http://127.0.0.1:$GP")" '[["0",true]]'
eq "laugd built 3 cells, one a lease and none more (min 0), and no two creates overlapped" \
  "$(awk 'NR > 1 && $2 < end { overlap = 1 } { end = $3 } END { print NR, overlap + 0 }' "$gw/fake/creates" 2>&1)|$(grep -c 'min 0, max 3, quota 3' "$gr/laugd.log")" "3 0|1"
eq "Omes built once, go build -o /dev/null ./cmd/omes in OMES_DIR, the one -fake made" "$(cat "$gw/fake/go.args" 2>&1)" "$gomes	build -o /dev/null ./cmd/omes"
eq "exit.json: each item's own exit, not stopped, start and end on both clocks; the load's own run-omes exit" \
  "$(jq -s -c 'map([.exit, .stopped_by, (.start | keys), (.end.wall >= .start.wall), .load_exit])' "$gr"/steady/exit.json "$gr"/pl/exit.json "$gr"/kh/exit.json 2>&1)" \
  '[[0,null,["mono","wall"],true,0],[0,null,["mono","wall"],true,null],[0,null,["mono","wall"],true,null]]'
eq "harness.json: make harness-test's exit, seconds and tally" "$(gj '[.exit, (.seconds | type), .timed_out, .tally]' harness.json)" '[0,"number",false,"1 passed, 0 failed"]'
eq "gate.json: the host by type and size, every input with a sha256 and changed_vs_base, the cell config both arms run, no refusal" \
  "$(gj '[.host.type, .host.vcpu, .host.mem_gib, (.inputs[:6] | map(.path)), all(.inputs[6:-1][]; .path | startswith("load/releases/")),
          (.inputs[-1] | [.path, .version]), all(.inputs[]; (.sha256 | test("^[0-9a-f]{64}$")) and (.changed_vs_base | type) == "boolean"), .refused,
          (.tree_dirty | type), .fake]' gate.json)" \
  '["m8i.8xlarge",32,123,["(outside the repository)/gp.yaml","load/steady-state.yaml","load/thresholds.sh","faults/catalog.yaml","cells/compose/dynamicconfig/laug.yaml","cells/compose/cell.yml"],true,["internal/judge","v3"],true,null,"boolean",true]'
eq "gate.json: the judge last in inputs, as bin/laug judge -identity -json gives it, changed_vs_base as git diff --quiet <base> says" \
  "$(gj '.inputs[-1] | [keys_unsorted, .sha256, .go, .tree_clean, .changed_vs_base]' gate.json)" \
  "$(bin/laug judge -identity -json 2>/dev/null | jq -c --argjson c "$(git diff --quiet "$(jq -r .base "$gr/gate.json")" -- internal/judge internal/jsonx && echo false || echo true)" \
     '[["path","sha256","changed_vs_base","version","go","tree_clean"], .sha256, .go, .tree_clean, $c]' 2>&1)"
eq "each item directory holds an empty .item, so git keeps one that stays empty" \
  "$(for k in steady pl kh; do [[ -f "$gr/$k/.item" && ! -s "$gr/$k/.item" ]] && echo "$k"; done | paste -sd ' ')" "steady pl kh"
eq "thresholds.json: load/thresholds.sh 1.31.0, as JSON" "$(gj '.resolved.release_file, .resolved.server_tag' thresholds.json | paste -sd ' ')" 'null "1.31.0"'
eq "clock.json: t0 and t_post on both clocks; verdict.json's elapsed_s from them" \
  "$(gj '[(.t0 | keys), (.t_post | keys), (.elapsed_s.wall > 0 and .elapsed_s.wall < 60), (.elapsed_s.mono > 0)]')" '[["mono","wall"],["mono","wall"],true,true]'
eq "timeline.jsonl: the steps in order" "$(jq -r .step "$gr/timeline.jsonl" | grep -v '^ended ' | paste -sd ,)" \
  "start,lock,plan,preflight,omes_build,laugd,harness,lease steady,leased steady,lease pl,leased pl,lease kh,leased kh,barrier,wave,wave end,combine,post,release,laugd stopped,done"
eq "poll.jsonl: laugd 0 busy, and exactly the three leased projects up" \
  "$(jq -s -c --argjson l "$(jq -s -c 'map(.project) | sort' "$gr"/steady/lease.json "$gr"/pl/lease.json "$gr"/kh/lease.json)" 'map([.busy, .projects == $l]) | unique' "$gr/poll.jsonl" 2>&1)" '[[0,true]]'
eq "lease.json, the copy a result keeps: dynconfig named outside the repository, times in UTC; .lease.json is laugd's own" \
  "$(jq -c '[.dynconfig, (.leased_at | endswith("Z"))]' "$gr/pl/lease.json" 2>&1)|$(jq -r .dynconfig "$gr/pl/.lease.json" | grep -c "^$gw/fake/")" \
  '["(outside the repository)/laug.yaml",true]|1'
grecords() { cat "$gr"/{gate,clock,plan,thresholds,thresholds-incumbent,harness,verdict,cleanup}.json "$gr"/{poll,timeline}.jsonl "$gr"/verdict.md "$gr"/*/lease.json "$gr"/*/exit.json 2>/dev/null; }
hasnt "the gate's own records name no path of this test" "$(grecords)" "$work"
hasnt "the gate's own records name no path of this checkout" "$(grecords)" "$root"
eq "cleanup.json: the 3 leased cells released, laugd stopped, no laug-* project left" "$(gj '[(.released | sort), .release_errors, .laugd, .left]' cleanup.json)" \
  "$(jq -s -c '[(map(.id) | sort), [], "stopped", []]' "$gr"/steady/lease.json "$gr"/pl/lease.json "$gr"/kh/lease.json)"
gend "all ADVANCE"

# 2. The watchdog: kh hangs until TERM, pl ignores TERM.
grun deadline PLAN="$work/gp-deadline.yaml" FAKE_GATE_HANG=kh FAKE_GATE_STUCK=pl
eq "kh hangs and pl ignores TERM: BLOCK, class deadline, exit 1" "$grc|$(gsum)" '1|["BLOCK","deadline",["R2","R3","R4","R5","R6"]]'
eq "kh: TERM at deadline_s, its EXIT trap's partial JSON, exit 143, class deadline" "$(gi kh)|$(jq -r .partial "$gr/kh/$GID2-kh.json" 2>&1)" \
  '["BLOCK","deadline","TERM",143]|true'
eq "pl: still running after TERM, KILL kill_grace_s later, exit 137, class deadline" "$(gi pl)" '[null,"deadline","KILL",137]'
eq "pl's KILL: bash's job notice in watchdog.log names gate/lib.sh as sourced, by no path of this checkout" \
  "$(grep -c '^gate/lib.sh: line [0-9]*: *[0-9]* Killed' "$gr/watchdog.log" 2>&1)|$(grep -cF "$root/" "$gr/watchdog.log" 2>&1)" "1|0"
eq "steady ended on its own before the deadline: ADVANCE" "$(gi steady)" '["ADVANCE",null,null,0]'
# The watchdog's lines in watchdog.log give the times it acted on, whole epoch ms on the wall clock: its deadline is
# clock.json's t0 plus deadline_s, to the ms; its TERM went at the first look at or past it, its KILL at the first look
# kill_grace_s, 1 s, after the TERM: the look before each fell short, or no look came before the TERM's. A loaded host
# makes each look later, never earlier, so these hold however long a look takes.
read -r wterm wdl wprev wt0 <<<"$(sed -n 's/^watchdog: TERM at \([0-9]*\), deadline \([0-9]*\), look before \([0-9]*\|none\), gate start \([0-9]*\) (epoch ms)$/\1 \2 \3 \4/p' "$gr/watchdog.log")"
read -r wkill wkterm wkprev <<<"$(sed -n 's/^watchdog: KILL at \([0-9]*\), TERM at \([0-9]*\), look before \([0-9]*\), kill_grace_s 1 (epoch ms)$/\1 \2 \3/p' "$gr/watchdog.log")"
eq "the watchdog: TERM at deadline_s, 5 s after the gate start on the wall clock, then KILL kill_grace_s later" \
  "$(jq -r 'select(.step | startswith("watchdog")) | .step' "$gr/timeline.jsonl" | paste -sd ,)|$(( ${wdl:-0} - ${wt0:-0} ))|$(jq -n --argjson s "${wt0:-0}" --argjson t0 "$(gj .t0.wall clock.json)" '$s / 1000 - $t0 | fabs < 0.0005' 2>&1)|$(( ${wterm:-0} >= ${wdl:-1} ))|$(( ${wkterm:-0} == ${wterm:-1} && ${wkill:-0} >= ${wterm:-0} + 1000 ))|$([[ "${wprev:-}" == none ]] && echo 1 || echo $(( ${wprev:-99999999999999} < ${wdl:-0} )))|$(( ${wkprev:-99999999999999} < ${wkterm:-0} + 1000 ))" \
  "watchdog TERM,watchdog KILL|5000|true|1|1|1|1"
eq "the cells released after a deadline" "$(gj '[(.released | length), .laugd, .left]' cleanup.json)" '[3,"stopped",[]]'
gend deadline

# 3. TERM to the gate itself, mid-wave: the EXIT trap stops the items, writes the verdict, hands it on and releases.
# A git first on PATH prints nothing for status --porcelain, so gate.json says tree_dirty false whatever this tree
# holds: the one value the trap verdict once lost (it wrote null for false).
mkdir -p "$work/cleangit"; printf '%s\n' '#!/usr/bin/env bash' '[[ "$1" != status ]] || exit 0' 'exec "$REAL_GIT" "$@"' > "$work/cleangit/git"; chmod +x "$work/cleangit/git"
gworld trap
env "${GENV[@]}" PATH="$work/cleangit:$PATH" PLAN="$work/gp.yaml" GATE_RESULTS="$gw/results" OMES_DIR="$gw/omes" FAKE_GATE_DIR="$gw/fake" FAKE_GATE_HANG="steady pl kh" \
  gate/run.sh > "$gw/out" 2>&1 &
gpid=$!
for _ in $(seq 1 100); do [[ "$(gstarted)" == 3 ]] && break; sleep 0.1; done
kill -TERM "$gpid"; wait "$gpid"; grc=$?; gout="$(cat "$gw/out")"; gfound
eq "TERM to the gate mid-wave: exit 1, a BLOCK verdict.json from the EXIT trap that names the step" \
  "$grc|$(gj '[.verdict, .class, .stopped_at, (.reasons[0] | startswith("stopped: gate/run.sh stopped at step wave (signal TERM)"))]')" '1|["BLOCK","invalid","wave",true]'
eq "TERM to the gate: each item got TERM and has its exit.json" "$(jq -s -c 'map([.stopped_by, .exit])' "$gr"/steady/exit.json "$gr"/pl/exit.json "$gr"/kh/exit.json 2>&1)" \
  '[["TERM",143],["TERM",143],["TERM",143]]'
eq "TERM to the gate: verdict.md ends BLOCK; the cells released" "$(tail -1 "$gr/verdict.md")|$(gj '[(.released | length), .laugd, .left]' cleanup.json)" '**BLOCK**|[3,"stopped",[]]'
eq "TERM to the gate: the trap verdict keeps gate.json's tree_dirty, false, as a boolean" "$(gj '[.tree_dirty, (.tree_dirty | type)]')|$(gj .tree_dirty gate.json)" '[false,"boolean"]|false'
# The judge section's all-ADVANCE case kept the judge's verdict.json of base in advance-1.json.
eq "TERM to the gate: the trap verdict has the judge's verdict.json keys, in order, then stopped_at" \
  "$(gj keys_unsorted)" "$(jq -c 'keys_unsorted + ["stopped_at"]' "$work/gate/advance-1.json" 2>&1)"
gend "TERM to the gate"

# 4. Refused at preflight, every reason at once: the lock held, a laugd on the port, a compose project up, an image not
# pulled, no Omes, 8 vCPU, 6 GiB available, an Omes port taken.
exec 8>>"$GLOCK"; flock -n 8 || fail "this test holds $GLOCK"
exec 7>>"$GHLOCK"; flock -n 7 || fail "this test holds $GHLOCK"
bin/laug pool serve -pid-file none -listen "127.0.0.1:$GP" -min 0 > "$work/other-laugd.log" 2>&1 & other=$!
for _ in $(seq 1 50); do curl -sf -m 1 -o /dev/null "http://127.0.0.1:$GP/healthz" && break; sleep 0.1; done
grun refused FAKE_GATE_PROJECTS=laug-0000aaaa,mygame FAKE_GATE_NO_IMAGE=temporalio/admin-tools:1.31.0 FAKE_GATE_VCPU=8 FAKE_GATE_AVAIL_GIB=6 \
  FAKE_GATE_LISTEN=19103 OMES_DIR="$work/no-omes" 7>&- 8>&-
kill "$other" 2>/dev/null; wait "$other" 2>/dev/null; exec 7>&- 8>&-
eq "refused at preflight: BLOCK, class refused, exit 1" "$grc|$(gj '[.verdict, .class]')" '1|["BLOCK","refused"]'
refused="$(jq -r .refused "$gv" 2>&1)"
for r in "another gate run holds (outside the repository)/gate.lock: wait for it to end" \
  "another gate run on this host holds (outside the repository)/gate-host.lock: wait for it to end" "something answers on 127.0.0.1:$GP" \
  "compose project(s) up on this host: laug-0000aaaa, and 1 project(s) laug did not start" "not pulled on this host: temporalio/admin-tools:1.31.0" \
  "no Omes at (outside the repository)/no-omes" "this host has 8 vCPU, under the 16 a gate needs" \
  "this host has 6.0 GiB available, under the 8 GiB a gate needs" "port 19103, an item's Omes metrics port, is taken" \
  "this host is declared m8i.8xlarge and has 8 vCPU"; do
  has "refused at preflight names: $r" "$refused" "$r"
done
eq "refused at preflight: no lease, no laugd, no harness-test, no Omes build" \
  "$(ls -d "$gr"/*/ 2>/dev/null | wc -l)|$(gj .laugd cleanup.json)|$([[ -e "$gr/harness.json" ]] && echo harness)|$([[ -e "$gw/fake/go.args" ]] && echo built)" '0|"not started"||'
has "refused: verdict.md says so" "$(cat "$gr/verdict.md")" "Refused before the wave: another gate run holds"
hasnt "refused while another gate holds the lock: no laug pool down advice, which would stop that gate mid-wave" "$gout" "pool down"
eq "refused at the lock: cleanup.json leaves out what was up at preflight, which is not this gate's" \
  "$(gj '[.left, any(.up_at_preflight[]; . == "laug-0000aaaa")]' cleanup.json)" '[[],true]'
hasnt "refused: a project laug did not start is counted, and named in no record" "$(grecords)" mygame
gend "refused at preflight"

# 4a. A pool serve left on the port and no lock held, as a walk that skipped a kill had it: refused at preflight, and
# the reason says how to stop a pool serve, Ctrl-C or kill, never laug pool down, which removes every pool cell.
bin/laug pool serve -pid-file none -listen "127.0.0.1:$GP" -min 0 > "$work/left-laugd.log" 2>&1 & other=$!
for _ in $(seq 1 50); do curl -sf -m 1 -o /dev/null "http://127.0.0.1:$GP/healthz" && break; sleep 0.1; done
grun left-pool
kill "$other" 2>/dev/null; wait "$other" 2>/dev/null
eq "a pool serve left on the port: BLOCK, class refused, exit 1" "$grc|$(gj '[.verdict, .class]')" '1|["BLOCK","refused"]'
has "a pool serve left on the port: the reason says Ctrl-C or kill %1" "$(jq -r .refused "$gv" 2>&1)" \
  "something answers on 127.0.0.1:$GP (curl exit 0), a laugd from laug pool serve or another user's gate; a pool serve stops with Ctrl-C in its terminal, or kill %1 when this shell started it with &"
hasnt "a pool serve left on the port: no laug pool down advice, which removes every pool cell" "$gout" "pool down"
gend "a pool serve left on the port"

# 4b. The judge at preflight (gate/lib.sh, judge_input), on stand-ins in a directory of this test's: a bin/laug whose
# judge -identity -json prints FAKE_JI, or exits FAKE_JI_RC, and a git whose rev-parse prints FAKE_JI_HEAD, status
# FAKE_JI_STATUS, and diff exits FAKE_JI_DIFF. A gate on fakes records the entry as -identity gives it and refuses only
# when -identity fails; a gate on cells refuses too a bin/laug not built from HEAD, -dirty or not, a judge with
# changes not committed, and one -identity does not call clean. jid prints the entry, or none, then each reason.
JI="$work/ji"; mkdir -p "$JI/bin" "$JI/git"
printf '%s\n' '#!/usr/bin/env bash' '[[ "$*" == "judge -identity -json" ]] || exit 9' \
  '[[ -z "${FAKE_JI_RC:-}" ]] || exit "$FAKE_JI_RC"' 'printf "%s\n" "$FAKE_JI"' > "$JI/bin/laug"
printf '%s\n' '#!/usr/bin/env bash' 'case "$1" in rev-parse) echo "$FAKE_JI_HEAD" ;; status) printf "%s" "${FAKE_JI_STATUS:-}" ;;' \
  'diff) exit "${FAKE_JI_DIFF:-0}" ;; *) exit 9 ;; esac' > "$JI/git/git"
chmod +x "$JI/bin/laug" "$JI/git/git"
JH="$(printf 'a%.0s' {1..40})"; JS="$(printf 'b%.0s' {1..64})"
jident() { printf '{"version":"v3","sha256":"%s","go":"go1.0","commit":"%s","tree_clean":%s}' "$JS" "$1" "$2"; }   # jident <commit> <tree_clean>
jid() { # jid <fake, 1 or ""> [VAR=value...]
  local f="$1"; shift
  ( cd "$JI" && export PATH="$JI/git:$PATH" FAKE_JI_HEAD="$JH" "$@" && driver=run.sh && . "$root/gate/lib.sh" || exit 9
    fake="$f"; base_commit=abc1234; reasons=(); inputs=(); judge_input
    printf '%s\n' "${inputs[0]:-none}" ${reasons[@]+"${reasons[@]}"} ) 2>&1 | paste -sd '|'
}
entry() { printf '{"path":"internal/judge","sha256":"%s","changed_vs_base":%s,"version":"v3","go":"go1.0","tree_clean":%s}' "$JS" "$1" "$2"; }
eq "judge_input, a gate on fakes: the entry as -identity gives it, tree_clean false and all; no reason" \
  "$(jid 1 FAKE_JI="$(jident "${JH:0:39}c" false)")" "$(entry false false)"
eq "judge_input, a gate on fakes: -identity fails, or prints what is no identity: no entry, and a reason (4c: the run then ends as the trap verdict)" \
  "$(jid 1 FAKE_JI_RC=3)|$(jid 1 FAKE_JI='{"version":"v3"}')" \
  "none|bin/laug judge -identity failed (exit 3), so no judge can be recorded or run: make build builds bin/laug|none|bin/laug judge -identity failed (exit 0), so no judge can be recorded or run: make build builds bin/laug"
eq "judge_input, a gate on cells: built from HEAD (-dirty or not), clean: the entry, changed_vs_base as git diff says; no reason" \
  "$(jid "" FAKE_JI="$(jident "$JH" true)" FAKE_JI_DIFF=1)|$(jid "" FAKE_JI="$(jident "$JH-dirty" true)")" "$(entry true true)|$(entry false true)"
eq "judge_input, a gate on cells: refused when bin/laug was built from another commit" \
  "$(jid "" FAKE_JI="$(jident "$(printf 'c%.0s' {1..40})-dirty" true)")" "$(entry false true)|bin/laug was built from ccccccc, and HEAD is aaaaaaa: make build"
eq "judge_input, a gate on cells: refused when the judge has changes not committed, or -identity does not call it clean" \
  "$(jid "" FAKE_JI="$(jident "$JH" false)" FAKE_JI_STATUS=' M internal/judge/rules.go')|$(jid "" FAKE_JI="$(jident "$JH" null)")" \
  "$(entry false false)|internal/judge or internal/jsonx has changes not committed, and a gate on cells is judged by HEAD's judge: commit them, then make build|$(entry false null)|bin/laug's judge is not the one internal/judge and internal/jsonx hold (judge -identity says tree_clean null): make build"

# 4c. A bin/laug that cannot judge, as one built before laug judge: judge -identity -json and judge -write <dir> each
# exit 2. The gate runs from a tree of this test's, nj/tree, which links each top-level entry of this checkout but bin/,
# where the stand-in is, with a git first on PATH that runs in this checkout. judge_input adds its reason, and refuse
# leads to finish, whose judge writes nothing, so the EXIT trap writes the verdict: BLOCK, class invalid, stopped at
# combine, its refused naming why, exit 1. Not class refused: the bin/laug that would judge the refusal is the one that
# failed. The step.sh section's case 17 runs the same stand-in through gate/step.sh.
NJ="$work/nj"; mkdir -p "$NJ/tree/bin" "$NJ/git"
for e in "$root"/*; do [[ "${e##*/}" == bin ]] || ln -s "$e" "$NJ/tree/${e##*/}"; done
printf '%s\n' '#!/usr/bin/env bash' "printf '%s\\n' \"\$*\" >> '$NJ/calls'" 'echo "laug · no such command: $1" >&2' 'exit 2' > "$NJ/tree/bin/laug"
printf '%s\n' '#!/usr/bin/env bash' "exec '$REAL_GIT' -C '$root' \"\$@\"" > "$NJ/git/git"
chmod +x "$NJ/tree/bin/laug" "$NJ/git/git"
njr="bin/laug judge -identity failed (exit 2), so no judge can be recorded or run: make build builds bin/laug"
njv() { jq -cn --arg r "$njr" --arg d "$1" '["BLOCK","invalid","combine",$r,"stopped: gate/\($d) stopped at step combine (the judge wrote no verdict) before gate/combine.sh judged; nothing was judged"]'; }
gworld nojudge
gout="$(env "${GENV[@]}" PATH="$NJ/git:$PATH" PLAN="$work/gp.yaml" GATE_RESULTS="$gw/results" OMES_DIR="$gw/omes" FAKE_GATE_DIR="$gw/fake" \
  "$NJ/tree/gate/run.sh" 2>&1)"; grc=$?
gfound
eq "a bin/laug that cannot judge, through gate/run.sh: exit 1, the trap verdict at combine, BLOCK, class invalid, refused naming -identity" \
  "$grc|$(gj '[.verdict, .class, .stopped_at, .refused, .reasons[0]]')" "1|$(njv run.sh)"
eq "... the stand-in asked twice, judge -identity at preflight and judge -write at combine; gate.json records no judge" \
  "$(paste -sd '|' "$NJ/calls" 2>&1)|$(gj '[.inputs[].path] | any(. == "internal/judge")' gate.json)" \
  "judge -identity -json|judge -write $(cd "$gr" 2>/dev/null && pwd -P)|false"
gend "a bin/laug that cannot judge"

# 5. Refused at a lease.
grun builtin GATE_BACKEND=
eq "laugd's own fake backend (POOL_BACKEND empty): its lease names no server_tag, refused at the first lease" \
  "$grc|$(gj .class)|$(jq -r .refused "$gv" | grep -c '^lease steady: cell [0-9a-f]\{8\} names no server_tag')" '1|"refused"|1'
eq "... the one lease released, one item directory, laugd stopped" "$(gj '[(.released | length), .laugd]' cleanup.json)|$(ls -d "$gr"/*/ | wc -l)" '[1,"stopped"]|1'
gend "laugd's own fake backend"
grun tag FAKE_GATE_BACKEND_TAG=1.32.1
eq "a cell on 1.32.1 for a gate on 1.31.0: refused at the first lease, which is released" "$grc|$(jq -r .refused "$gv" | sed 's/cell [0-9a-f]*/cell <id>/')|$(gj '.released | length' cleanup.json)" \
  "1|lease steady: cell <id> is on server_tag 1.32.1, and the gate SERVER_TAG is 1.31.0|1"
gend "a cell on another tag"
grun createfail FAKE_GATE_CREATE_FAIL=2 FAKE_GATE_CREATE_S=0.5
eq "laugd fails the second create: refused at lease pl; steady's cell released, no project left" \
  "$grc|$(jq -r .refused "$gv" | cut -d: -f1-2)|$(gj '[(.released | length), .left]' cleanup.json)|$(ls "$gw/fake/projects" | wc -l)" \
  '1|lease pl: laugd refused it (curl exit 22)|[1,[]]|0'
eq "refused after make harness-test ended: harness.json records its exit and tally, and R1 holds" \
  "$(gj '[.exit, .tally]' harness.json)|$(gj '.rules.R1.pass')" '[0,"1 passed, 0 failed"]|true'
gend "a create that fails"

# 6. make harness-test past harness_timeout_s: no item runs, R1.
grun harness PLAN="$work/gp-harness.yaml" FAKE_GATE_HARNESS_S=30
eq "make harness-test past harness_timeout_s: BLOCK, class harness; exit 124, timed out; no item started" \
  "$grc|$(gsum)|$(gj '[.exit, .timed_out]' harness.json)|$(gstarted)" '1|["BLOCK","harness",["R1","R2","R3","R4","R5","R6"]]|[124,true]|0'
eq "... the cells released all the same" "$(gj '[(.released | length), .laugd, .left]' cleanup.json)" '[3,"stopped",[]]'
gend "harness-test timed out"
grun harness-fails FAKE_GATE_HARNESS_EXIT=1 FAKE_GATE_CREATE_S=0.3
eq "make harness-test exits 1 at once: class harness, and no lease is taken once it has (at most the first, whose create was under way)" \
  "$grc|$(gj '[.class, (.rules.R1.reasons | join(""))]')|$(gj '(.released | length) < 2' cleanup.json)|$(grep -c 'not taken: make harness-test already exited 1' <<<"$gout")" \
  '1|["harness","harness: make harness-test exited 1"]|true|1'
gend "harness-test failed"

# 7. The barrier: a project the gate did not lease comes up after preflight.
grun barrier FAKE_GATE_PROJECTS=laug-0000aaaa FAKE_GATE_EXTRA_FROM=2
has "a project the gate did not lease, up at the barrier: refused barrier_timeout_s after the harness passed" "$(jq -r .refused "$gv")" \
  "the host was not quiet 2 s after make harness-test passed: compose projects up: "
has "... naming the project" "$(jq -r .refused "$gv")" "laug-0000aaaa"
eq "... class refused, no item started, the cells released" "$grc|$(gj .class)|$(gstarted)|$(gj '.released | length' cleanup.json)" '1|"refused"|0|3'
gend "the barrier"

# 8. Items that BLOCK: pl did not recover, kh exited 2 with no JSON.
grun block FAKE_GATE_DNR=pl FAKE_GATE_EXIT2=kh
eq "pl did not recover and kh exited 2: BLOCK, class regression, exit 1" "$grc|$(gsum)" '1|["BLOCK","regression",["R2","R3","R4","R5","R6"]]'
eq "... pl regression with its exit 1, kh invalid with its exit 2" "$(gi pl)|$(gi kh)" '["BLOCK","regression",null,1]|[null,"invalid",null,2]'
gend "items that BLOCK"

# A project laug did not start comes up as the wave does (compose ls call 4: preflight, the barrier twice, the first
# poll).
grun other FAKE_GATE_PROJECTS=mygame FAKE_GATE_EXTRA_FROM=4
eq "a project laug did not start, up during the wave: ADVANCE stands, host not quiet, the project not named" \
  "$grc|$(gj '[.verdict, .host_quiet, .host_noise]')" "0|[\"ADVANCE\",false,[\"the poller saw compose project (not laug's) up, which the gate did not lease\"]]"
hasnt "... in no record" "$(grecords)" mygame
gend "a project laug did not start"

# 9. gate/plans/noise.yaml, its first 2 items.
grun noise PLAN="$work/gp-noise.yaml" ITEMS=2
eq "noise plan, ITEMS=2: plan.json holds s1 and s2 of 8; laugd max 2" "$(gj '[.items_in_plan, [.items[].key]]' plan.json)|$(grep -c 'min 0, max 2, quota 2' "$gr/laugd.log")" \
  '[8,["s1","s2"]]|1'
eq "... no fault item, so no live control, and cut to 2 of 8: BLOCK, class control, R2 and R5; both ADVANCE, host quiet" \
  "$grc|$(gsum)|$(gj '[[.items[].observed], .host_quiet, .items_in_plan, .items_limit]')" '1|["BLOCK","control",["R2","R5"]]|[["ADVANCE","ADVANCE"],true,8,2]'
has "... the run said ITEMS cut the plan" "$gout" "ITEMS=2 runs 2 of the plan's 8 items: R2 BLOCKs a cut plan"
gend "the noise plan"

# 9b. gate/plans/arm-pilot.yaml, whole, through laug gate run -fake: the incumbent arm's pilot, steady's load under the
# key incumbent leased first, then steady and pl. It has a fault item and is not cut, so R2 and R5 hold: ADVANCE,
# class null.
gplan "$work/gp-arm-pilot.yaml" '' gate/plans/arm-pilot.yaml
gworld armpilot; mkdir -p "$gw/tmp"
gout="$(env GATE_LOCK="$GLOCK" GATE_HOST_LOCK="$GHLOCK" GATE_POOL_LISTEN="127.0.0.1:$GP" FAKE_GATE_DIR="$gw/fake" TMPDIR="$gw/tmp" \
  bin/laug gate run 1.31.0 -fake -allow-stale -plan "$work/gp-arm-pilot.yaml" -results "$gw/results" 2>&1)"; grc=$?
gfound
eq "arm pilot plan, laug gate run -fake: ADVANCE, exit 0, class null, every item ADVANCE, host quiet; laugd max 3" \
  "$grc|$(gj '[.verdict, .class, [.items[] | [.key, .observed]], .host_quiet, .items_in_plan, .items_limit]')|$(grep -c 'min 0, max 3, quota 3' "$gr/laugd.log")" \
  '0|["ADVANCE",null,[["incumbent","ADVANCE"],["steady","ADVANCE"],["pl","ADVANCE"]],true,3,null]|1'
eq "... leased one at a time in the plan's order, incumbent first" \
  "$(jq -r 'select(.step | startswith("lease ")) | .step' "$gr/timeline.jsonl" 2>&1 | paste -sd ' ')" "lease incumbent lease steady lease pl"
eq "... incumbent ran steady's load with its own run id" \
  "$(jq -s -c '[.[] | [.kind, .key, .scenario, .run_id]]' "$gr/incumbent/fake-item.json" "$gr/steady/fake-item.json" 2>&1)" \
  "[[\"load\",\"incumbent\",\"workflow_with_single_noop_activity\",\"$GID2-incumbent\"],[\"load\",\"steady\",\"workflow_with_single_noop_activity\",\"$GID2-steady\"]]"
gend "the arm pilot plan"

# 9c. The incumbent arm (gate/plan.yaml's, as load/testdata/gate/plan-arm.yaml holds it): incumbent, steady and pl, the
# incumbent item on the plan's incumbent, 1.31.0, and the others on SERVER_TAG. Each lease names its item's tag, laugd
# hands it to the backend as SERVER_TAG, and the fake backend's creates file records the tag each cell says. R6 judges
# each item by its own tag and R7 compares steady with incumbent. The expectations read load/steady-state.yaml's mode,
# so the commit that sets its tolerances edits no case: recording while no compare row has a tolerance, the compared
# rows recorded; judging after, the compared rows ok.
gplan "$work/gp-arm.yaml" '' "$td/gate/plan-arm.yaml"
AMODE=recording; AOK=recorded; (( $(yq '[.compare.rows[] | select(.tolerance != null)] | length' load/steady-state.yaml) == 0 )) || { AMODE=judging; AOK=ok; }
ACMP="$(yq -o=json -I=0 '[.compare.rows[].name]' load/steady-state.yaml)"   # the compared readings, by name
acmp() { jq -c --argjson c "$ACMP" "[.comparison.rows[] | select(.name as \$n | \$c | index(\$n)) | $1]" "$gv" 2>&1; }   # acmp <jq>: per compared row
garm() { jq -s -c "$1" "$gr"/incumbent/"${2:-lease.json}" "$gr"/steady/"${2:-lease.json}" "$gr"/pl/"${2:-lease.json}" 2>&1; }   # one file of each item
gtags() { awk '{ print $4 }' "$gw/fake/creates" 2>&1 | paste -sd ' '; }   # the tag each create's cell said, in order
grun arm PLAN="$work/gp-arm.yaml"
eq "arm, A/A at SERVER_TAG=1.31.0: ADVANCE, exit 0, class null, every item ADVANCE, host quiet; plan.json names the incumbent and the arms" \
  "$grc|$(gj '[.verdict, .class, [.items[] | [.key, .observed]], .host_quiet]')|$(gj '[.incumbent, [.items[] | .arm]]' plan.json)" \
  '0|["ADVANCE",null,[["incumbent","ADVANCE"],["steady","ADVANCE"],["pl","ADVANCE"]],true]|["1.31.0",["incumbent",null,null]]'
eq "arm: leased one at a time, incumbent first, on blocks 0, 1 and 2, each lease on 1.31.0" \
  "$(jq -r 'select(.step | startswith("lease ")) | .step' "$gr/timeline.jsonl" 2>&1 | paste -sd ,)|$(garm 'map(.block)')|$(garm 'map(.server_tag)')" \
  'lease incumbent,lease steady,lease pl|[0,1,2]|["1.31.0","1.31.0","1.31.0"]'
eq "arm: the three creates' cells say 1.31.0, and thresholds-incumbent.json is load/thresholds.sh 1.31.0" \
  "$(gtags)|$(gj '[.resolved.server_tag, .resolved.release_file]' thresholds-incumbent.json)" '1.31.0 1.31.0 1.31.0|["1.31.0",null]'
eq "arm: gate.json's inputs record the cell config both arms ran, laug.yaml and cell.yml" \
  "$(gj '[.inputs[] | select(.path | startswith("cells/")) | .path]' gate.json)" '["cells/compose/dynamicconfig/laug.yaml","cells/compose/cell.yml"]'
hasnt "arm: the gate's own records name no path of this test" "$(grecords)" "$work"
eq "arm, A/A: R7 holds, comparison mode $AMODE, every compared row $AOK, every d 0, the windows 0 s apart" \
  "$(gj '[.rules.R7.pass, .comparison.mode, ([.comparison.rows[].d] | unique), .comparison.windows.start_apart_s]')|$(acmp .result | jq -c unique)" \
  "[true,\"$AMODE\",[0],0]|[\"$AOK\"]"
gend "the arm, A/A"
grun arm-ab PLAN="$work/gp-arm.yaml" SERVER_TAG=1.32.1
eq "arm, A/B at SERVER_TAG=1.32.1: incumbent's lease on 1.31.0, steady's and pl's on 1.32.1; the creates' cells say the same" \
  "$(garm 'map(.server_tag)')|$(gtags)" '["1.31.0","1.32.1","1.32.1"]|1.31.0 1.32.1 1.32.1'
eq "arm, A/B: each load's verdict JSON records its own tag's release file, thresholds.json 1.32.1's and thresholds-incumbent.json 1.31.0's" \
  "$(jq -c .thresholds_release "$gr/incumbent/$GID2-incumbent.verdict.json" "$gr/steady/$GID2-steady.verdict.json" 2>&1 | paste -sd ' ')|$(gj '[.resolved.server_tag, .resolved.release_file]' thresholds.json)|$(gj .resolved.server_tag thresholds-incumbent.json)" \
  'null "load/releases/1.32.yaml"|["1.32.1","load/releases/1.32.yaml"]|"1.31.0"'
eq "arm, A/B: ADVANCE, exit 0, R6 holds on every item, the activity share $AOK" \
  "$grc|$(gj '[.verdict, .rules.R6.pass, [.items[] | select(.failed | index("R6"))], (.comparison.rows[] | select(.name == "activity_schedule_to_start_over_10ms") | .result)]')" \
  "0|[\"ADVANCE\",true,[],\"$AOK\"]"
gend "the arm, A/B"
# FAKE_GATE_SCALE: steady's latency readings and shares 1.5 times the incumbent's, its maxima unjudged by the fake.
grun arm-scale PLAN="$work/gp-arm.yaml" FAKE_GATE_SCALE=steady FAKE_GATE_SCALE_BY=1.5
if [[ "$AMODE" == recording ]]; then want='0|["ADVANCE",null,[]]|[true]'; else want='1|["BLOCK","regression",["R7"]]|[true]'; fi
eq "arm, steady 1.5 times slower than incumbent: $([[ "$AMODE" == recording ]] && echo "recording, ADVANCE" || echo "judging, BLOCK class regression on R7 alone"), every compared d over 0" \
  "$grc|$(gsum)|$(acmp '.d > 0' | jq -c unique)" "$want"
grun arm-tag PLAN="$work/gp-arm.yaml" "FAKE_GATE_BACKEND_TAG_AT=1 1.32.1"
eq "arm: incumbent's cell on 1.32.1: refused at lease incumbent, naming the plan's incumbent; the one cell released" \
  "$grc|$(gj .class)|$(jq -r .refused "$gv" | sed 's/cell [0-9a-f]*/cell <id>/')|$(gj '.released | length' cleanup.json)" \
  "1|\"refused\"|lease incumbent: cell <id> is on server_tag 1.32.1, and the plan's incumbent is 1.31.0|1"
gend "the arm, incumbent on another tag"
grun arm-noimage PLAN="$work/gp-arm.yaml" SERVER_TAG=1.32.1 FAKE_GATE_NO_IMAGE=temporalio/server:1.31.0
eq "arm, A/B with 1.31.0's server image not local: refused at preflight on the incumbent's images alone, no lease" \
  "$grc|$(gj .class)|$(gj .refused gate.json)|$(ls -d "$gr"/*/ 2>/dev/null | wc -l)" \
  "1|\"refused\"|\"the plan's incumbent 1.31.0 is not pulled on this host: temporalio/server:1.31.0 (the cell's 8 images at 1.31.0 saved to a 778238976-byte tar: cells/results/2026-10-23-boot1.txt, A1)\"|0"
gend "the arm, the incumbent's image not local"

# 10. A plan that is not there: refused before anything.
grun badplan PLAN="$work/no-plan.yaml"
eq "a plan that is not there: refused, no laugd" "$grc|$(gj .class)|$(jq -r .refused "$gv")|$(gj .laugd cleanup.json)" \
  '1|"refused"|plan (outside the repository)/no-plan.yaml: no such file|"not started"'
gend "no plan"
# 10a. GATE_POOL_LISTEN must be 127.0.0.1:<port>, since the gate's pool has no auth (SECURITY.md). Case 10's run, on
# the free port GP, got past gate_env's check of it: exit 1, not 2, and gate.json's pool names GP. Another host, the
# name localhost in place of 127.0.0.1, and a port past 65535 are refused before any gate run: exit 2, gate_env's one
# line, nothing in the results directory. Each also gets case 10's missing plan, so a gate that took the address would
# stop at preflight, before laugd.
eq "GATE_POOL_LISTEN=127.0.0.1:$GP, a free port: past the check, to its own run; gate.json's pool names it" \
  "$grc|$(grep -c 'GATE_POOL_LISTEN must be' <<<"$gout")|$(gj .pool gate.json)" "1|0|\"127.0.0.1:$GP\""
for a in 0.0.0.0:7700 localhost:7700 127.0.0.1:99999; do
  grun "listen-${a%%:*}" GATE_POOL_LISTEN="$a" PLAN="$work/no-plan.yaml"
  eq "GATE_POOL_LISTEN=$a: exit 2 before any gate run, gate_env's one line, its results directory left empty" "$grc|$gout|$(find "$gw/results" -mindepth 1 | wc -l)" \
    "2|laug · GATE_POOL_LISTEN must be 127.0.0.1:<port>: the gate's pool has no auth (SECURITY.md)|0"
done

# 11. Another laugd binds the gate's port after preflight, during the Omes build, with the gate's own settings (min 0,
# max 3, quota 3): the gate's laugd exits on its failed bind, and the gate refuses rather than lease from the other.
gworld foreign
env "${GENV[@]}" PLAN="$work/gp.yaml" GATE_RESULTS="$gw/results" OMES_DIR="$gw/omes" FAKE_GATE_DIR="$gw/fake" FAKE_GATE_GO_S=2 \
  gate/run.sh > "$gw/out" 2>&1 &
gpid=$!
for _ in $(seq 1 100); do [[ -e "$gw/fake/go.args" ]] && break; sleep 0.1; done
bin/laug pool serve -pid-file none -listen "127.0.0.1:$GP" -min 0 -max 3 -quota 3 > "$work/foreign-laugd.log" 2>&1 & other=$!
wait "$gpid"; grc=$?; gout="$(cat "$gw/out")"; gfound
fcells="$(curl -sf -m 2 "http://127.0.0.1:$GP/status" | jq '.cells // [] | length' 2>&1)"
kill "$other" 2>/dev/null; wait "$other" 2>/dev/null
eq "another laugd on the port from the Omes build on: refused, class refused, no lease taken from it, no item directory" \
  "$grc|$(gj .class)|$(jq -r .refused "$gv" | grep -c "^the laugd on 127.0.0.1:$GP is not the gate's own: ")|$fcells|$(ls -d "$gr"/*/ 2>/dev/null | wc -l)" \
  '1|"refused"|1|0|0'
gend "another laugd on the port"

# 12. TERM to the gate during a cold create (the fake backend makes the project first, as compose up does): the gate
# waits for laugd to finish the create and give the cell back, then stops laugd. Stopping laugd at once would kill the
# create and leave the project up.
gworld midlease
env "${GENV[@]}" PLAN="$work/gp.yaml" GATE_RESULTS="$gw/results" OMES_DIR="$gw/omes" FAKE_GATE_DIR="$gw/fake" FAKE_GATE_CREATE_S=2 \
  gate/run.sh > "$gw/out" 2>&1 &
gpid=$!
for _ in $(seq 1 100); do [[ -e "$gw/fake/creates.n" ]] && break; sleep 0.1; done
kill -TERM "$gpid"; wait "$gpid"; grc=$?; gout="$(cat "$gw/out")"; gfound
eq "TERM during a cold create: BLOCK at step lease steady; the create finished, the cell given back, laugd stopped, no project left" \
  "$grc|$(gj .stopped_at)|$(wc -l < "$gw/fake/creates" 2>&1)|$(gj '[.laugd, .left]' cleanup.json)|$(ls "$gw/fake/projects" | wc -l)" \
  '1|"lease steady"|1|["stopped",[]]|0'
has "... and said it waited" "$gout" "busy cell(s), a create or destroy under way: waiting for it to end"
gend "TERM during a cold create"

# 13. KILL to the gate mid-wave runs no EXIT trap: its poller, left with no parent, stops polling all the same.
gworld kill
env "${GENV[@]}" PLAN="$work/gp.yaml" GATE_RESULTS="$gw/results" OMES_DIR="$gw/omes" FAKE_GATE_DIR="$gw/fake" FAKE_GATE_ITEM_S=3 \
  gate/run.sh > "$gw/out" 2>&1 &
gpid=$!
for _ in $(seq 1 100); do [[ "$(gstarted)" == 3 ]] && break; sleep 0.1; done
kill -KILL "$gpid"; { wait "$gpid"; } 2>/dev/null; gfound
sleep 1.5; n1="$(wc -l < "$gr/poll.jsonl")"; sleep 2; n2="$(wc -l < "$gr/poll.jsonl")"
eq "KILL to the gate mid-wave: no poll recorded 1.5 s on and later (poll_s 1)" "$(( n2 - n1 ))" 0
pkill -f "pool serve -pid-file none -listen 127.0.0.1:$GP " 2>/dev/null; for _ in $(seq 1 50); do gstopped && break; sleep 0.1; done   # what a KILL leaves
gstopped && pass "... its laugd, which a KILL leaves, stopped here" || fail "... its laugd, which a KILL leaves, stopped here"

# 14. docker compose ls prints a CLI warning on stderr and succeeds: only stdout is parsed.
grun warn FAKE_GATE_DOCKER_WARN=1
eq "docker warns on stderr: ADVANCE, host quiet, every poll read the projects" \
  "$grc|$(gj '[.verdict, .host_quiet]')|$(jq -s -c 'map(.projects | type) | unique' "$gr/poll.jsonl" 2>&1)" '0|["ADVANCE",true]|["array"]'
gend "a docker that warns"

# 15. Too little left before the watchdog for the wave (wave_min_s): no cell is built, or no item starts.
grun later PLAN="$work/gp-later.yaml"
eq "wave_min_s 30 of deadline_s 30: refused before the first lease, nothing leased" \
  "$grc|$(gj .class)|$(jq -r .refused "$gv" | grep -c '^too late for the wave at lease steady: ')|$(ls -d "$gr"/*/ 2>/dev/null | wc -l)|$(gj '.released | length' cleanup.json)" \
  '1|"refused"|1|0|0'
gend "too late for the first lease"
grun late PLAN="$work/gp-late.yaml" FAKE_GATE_HARNESS_S=6
eq "deadline_s 12, wave_min_s 7, make harness-test 6 s: refused at the barrier, no item started, the cells released" \
  "$grc|$(gj .class)|$(jq -r .refused "$gv" | grep -c '^too late for the wave at the barrier: ')|$(gstarted)|$(gj '.released | length' cleanup.json)" \
  '1|"refused"|1|0|3'
gend "too late at the barrier"

# 16. The host type (gate/lib.sh, host_check): a fake gate declares FAKE_GATE_HOST_TYPE, "-" for none, else
# load/testdata/gate/host-type's line, m8i.8xlarge, the type load/steady-state.yaml names, and its fake nproc and free
# give that host's size, 32 vCPU and 123 GiB. Each case below is refused at preflight on the host check alone.
ghost() { # ghost <case> <the refusal> <VAR=value>...
  local c="$1" want="$2"; shift 2
  grun "host-$c" "$@"
  eq "host $c: BLOCK, class refused, exit 1, no lease; gate.json's refused is the host check's line, host.type other" \
    "$grc|$(gj '[.verdict, .class]')|$(ls -d "$gr"/*/ 2>/dev/null | wc -l)|$(gj .refused gate.json)|$(gj .host.type gate.json)" \
    "1|[\"BLOCK\",\"refused\"]|0|$(jq -cn --arg r "$want" '$r')|\"other\""
}
ghost none "this host declares no host type, so it cannot be seen to be the m8i.8xlarge (32 vCPU, 123 GiB) load/steady-state.yaml was measured on; bin/laug host type -declare <type> declares one" \
  FAKE_GATE_HOST_TYPE=-
ghost m7i "this host's declared type is not m8i.8xlarge, the type load/steady-state.yaml was measured on: its numbers judge no other host type (docs/DESIGN.md section 7, the R6 paragraph)" \
  FAKE_GATE_HOST_TYPE=m7i.8xlarge
hasnt "host m7i: the declared type is in no record of the run" "$(grecords)" "m7i"
ghost size "this host is declared m8i.8xlarge and has 24 vCPU and 47 GiB, and load/steady-state.yaml's m8i.8xlarge has 32 vCPU and 123 GiB" \
  FAKE_GATE_VCPU=24 FAKE_GATE_MEM_GIB=47
ghost name "the host type declared in (outside the repository)/host-type is not a type name" "FAKE_GATE_HOST_TYPE=M8I 8XL"
gend "a host of another type"
grun host-122 FAKE_GATE_MEM_GIB=122
eq "122 GiB, within 1 GiB of the set's 123: ADVANCE, exit 0; gate.json host.type m8i.8xlarge" \
  "$grc|$(gj .verdict)|$(gj .host gate.json)" '0|"ADVANCE"|{"type":"m8i.8xlarge","vcpu":32,"mem_gib":122,"mem_available_gib":36}'
gend "122 GiB"

# 17. laug host type, with XDG_CONFIG_HOME in the work directory: it writes the declaration only from -declare, and reads
# the set with THRESHOLDS unset, as a gate does. It reads this host's own nproc and free. mht [VAR=value...] runs it
# bare.
XH="$work/xdg-ht"; HTF="$XH/laug/host-type"
mht() { env XDG_CONFIG_HOME="$XH" "$@" bin/laug host type 2>&1; }
SETH='{"type":"m8i.8xlarge","vcpu":32,"mem_gib":123}'
out="$(mht)"; rc=$?
eq "laug host type, nothing declared: exit 0, declared none, the virtualization line, the set's host, and laug gate run refuses this host" \
  "$rc|$(grep -c '^declared          none$' <<<"$out")|$(grep -c '^virtualization    .*hypervisor flag in /proc/cpuinfo' <<<"$out")|$(grep -cxF 'the set           m8i.8xlarge, 32 vCPU, 123 GiB (load/steady-state.yaml, host)' <<<"$out")|$(grep -c '^laug gate run     refuses this host: this host declares no host type' <<<"$out")" \
  "0|1|1|1|1"
out="$(env XDG_CONFIG_HOME="$XH" bin/laug host type -declare m8i.8xlarge 2>&1)"; rc=$?
hv="$(nproc)"; hm="$(free -b | awk '$1 == "Mem:" { print $2 }')"
hr_want="$(bash -c '. gate/lib.sh; host_check "$@"' x m8i.8xlarge "$hv" "$hm" "$SETH")"
if [[ -n "$hr_want" ]]; then hr_want="laug gate run     refuses this host: $hr_want"; else hr_want="laug gate run     passes the host check"; fi
eq "laug host type -declare m8i.8xlarge: exit 0, the file holds that one line, and laug gate run's line is the host check's for this host's nproc and free" \
  "$rc|$(wc -l < "$HTF" 2>&1)|$(cat "$HTF" 2>&1)|$(grep -c '^laug · host type: declared m8i.8xlarge$' <<<"$out")|$(tail -1 <<<"$out")" \
  "0|1|m8i.8xlarge|1|$hr_want"
out="$(env XDG_CONFIG_HOME="$XH" bin/laug host type -json 2>&1)"; rc=$?
eq "laug host type -json: exit 0, one line, the declaration, this host's nproc, virtualized a boolean, the set's host and the host check's answer" \
  "$rc|$(wc -l <<<"$out")|$(jq -c '[.declared, .vcpu, (.virtualized | type), .set, .passes, (if .passes then "laug gate run     passes the host check" else "laug gate run     refuses this host: \(.reason)" end)]' <<<"$out" 2>&1)" \
  "0|1|$(jq -cn --argjson v "$hv" --argjson h "$SETH" --arg r "$hr_want" '["m8i.8xlarge", $v, "boolean", $h, ($r | endswith("passes the host check")), $r]')"
s1="$(sha256sum < "$HTF")"
out="$(env XDG_CONFIG_HOME="$XH" bin/laug host type -declare 'bad type' 2>&1)"; rc=$?
eq "laug host type -declare 'bad type': exit 2, refused, the file unchanged" \
  "$rc|$(head -1 <<<"$out")|$([[ "$(sha256sum < "$HTF")" == "$s1" ]] && echo unchanged)" \
  "2|laug · host type: -declare 'bad type': a type name, lowercase letters, digits, . and -, 32 at most, as EC2 names one (m8i.8xlarge)|unchanged"
out="$(mht TYPE=m7i.8xlarge)"; rc=$?
eq "TYPE=m7i.8xlarge in the environment, not -declare: exit 0, the file unchanged, the declaration read back" \
  "$rc|$([[ "$(sha256sum < "$HTF")" == "$s1" ]] && echo unchanged)|$(grep -c '^declared          m8i.8xlarge, in (outside the repository)/host-type$' <<<"$out")" "0|unchanged|1"
yq '.host.type = "dev-host"' load/steady-state.yaml > "$work/ht-dev.yaml"
out="$(mht THRESHOLDS="$work/ht-dev.yaml")"
eq "THRESHOLDS in the environment naming a set of another host: laug host type still reads load/steady-state.yaml's" \
  "$(grep '^the set ' <<<"$out")" "the set           m8i.8xlarge, 32 vCPU, 123 GiB (load/steady-state.yaml, host)"
hasnt "laug host type: no path of this test in what it prints" "$out" "$work"

# 18. host_reasons' own read of the declaration, which every real gate takes and no fake gate does (GATE_FAKE=1 reads
# FAKE_GATE_HOST_TYPE or the fixture): $XDG_CONFIG_HOME/laug/host-type, its first line. hreal <bytes, or - for no file>
# prints host_type|reasons, on a thresholds.json with resolved.host m8i.8xlarge, 32 vCPU and 123 GiB, on this size.
hreal() {
  rm -rf "$work/hreal"; mkdir -p "$work/hreal/xdg/laug"
  [[ "$1" == - ]] || printf '%b' "$1" > "$work/hreal/xdg/laug/host-type"
  jq -n --argjson h "$SETH" '{resolved: {host: $h}}' > "$work/hreal/thresholds.json"
  ( fake=""; root="$PWD"; dir="$work/hreal"; vcpu=32; mem_b=$(( 123 * 1073741824 )); reasons=(); . gate/lib.sh
    XDG_CONFIG_HOME="$work/hreal/xdg" host_reasons; echo "$host_type|${reasons[*]:-none}" )
}
NODECL="this host declares no host type, so it cannot be seen to be the m8i.8xlarge (32 vCPU, 123 GiB) load/steady-state.yaml was measured on; bin/laug host type -declare <type> declares one"
eq "host_reasons, the real read: m8i.8xlarge and a newline passes; with a CR before it, not a type name; an empty file and no file, none declared" \
  "$(hreal 'm8i.8xlarge\n')#$(hreal 'm8i.8xlarge\r\n')#$(hreal '')#$(hreal -)" \
  "\"m8i.8xlarge\"|none#\"other\"|the host type declared in (outside the repository)/host-type is not a type name#\"other\"|$NODECL#\"other\"|$NODECL"

# The plans, checked without a gate run (gate/run.sh --plan), and laug gate run's usage errors, which start no gate run.
eq "gate/run.sh --plan load/testdata/gate/plan.yaml: the fixture's plan.json, every key" \
  "$(gate/run.sh --plan "$td/gate/plan.yaml" | jq -S -c .)" "$(jq -S -c . "$GFX/plan.json")"
# The real plans are the checkout's, like the thresholds: a gate run records their sha256 and does not judge them, so
# these checks hold their shape only, and a gated change to either plan does not fail them.
eq "gate/run.sh --plan gate/plan.yaml: a plan the gate accepts, with a load item and a fault item (R5 needs one)" \
  "$(gate/run.sh --plan gate/plan.yaml 2>&1 | jq -c '[any(.items[]; .kind == "load"), any(.items[]; .kind == "fault")]' 2>&1)" '[true,true]'
eq "gate/run.sh --plan gate/plans/noise.yaml: 8 load items, s1 to s8, the same item eight times" \
  "$(gate/run.sh --plan gate/plans/noise.yaml 2>&1 | jq -c '[[.items[].key], ([.items[] | del(.key)] | unique | length), ([.items[].kind] | unique)]' 2>&1)" \
  '[["s1","s2","s3","s4","s5","s6","s7","s8"],1,["load"]]'
eq "gate/run.sh --plan gate/plans/arm-pilot.yaml: incumbent, steady and pl; incumbent is steady's load under another key" \
  "$(gate/run.sh --plan gate/plans/arm-pilot.yaml 2>&1 | jq -c '[[.items[] | [.key, .kind]], ([.items[] | select(.kind == "load") | del(.key)] | unique | length)]' 2>&1)" \
  '[[["incumbent","load"],["steady","load"],["pl","fault"]],1]'
# The one pin on gate/plan.yaml's values: its incumbent, which a commit bumps when a release is promoted, edits this check.
eq "gate/run.sh --plan gate/plan.yaml: incumbent 1.31.0; incumbent, steady and pl; one item with arm incumbent, its pair steady" \
  "$(gate/run.sh --plan gate/plan.yaml 2>&1 | jq -c '[.incumbent, [.items[].key], [.items[].arm]], [.items[] | select(.arm == "incumbent") | .pair]' 2>&1 | paste -sd ' ')" \
  '["1.31.0",["incumbent","steady","pl"],["incumbent",null,null]] ["steady"]'
yq -o=json . "$td/gate/plan-arm.yaml" | jq '.incumbent = "latest" | .items[0] as $inc | .items = [($inc | .key = "inc1" | .rate = 10),
  ($inc | .key = "inc2" | .pair = "nosuch"), ($inc | .key = "inc3" | .arm = "baseline"), ($inc | .key = "inc4" | del(.arm)), .items[1],
  {key: "kh", kind: "fault", fault: "kill-history", scenario: "workflow_with_single_noop_activity", expect: "ADVANCE", arm: "incumbent"}]' > "$work/gp-arm-bad.yaml"
out="$(gate/run.sh --plan "$work/gp-arm-bad.yaml" 2>&1)"; rc=$?
eq "--plan on an arm plan with 7 problems: exit 2, one line each" "$rc|$out" "2|$(printf '%s\n' \
  'incumbent is "latest", not a release tag, x.y.z' 'item inc1: pair steady runs rate 20, and this item 10: a pair compares like with like' \
  'item inc2: pair "nosuch" names no item of the plan' 'item inc3: arm is "baseline"; the one arm built is incumbent' \
  'item inc4: arm and pair go together: an incumbent item names the load it is compared with' \
  'item kh: arm and pair are for a load item; an incumbent fault item is not built' 'items: 2 incumbent items; one is built')"
yq 'del(.incumbent)' "$td/gate/plan-arm.yaml" > "$work/gp-arm-noinc.yaml"
out="$(gate/run.sh --plan "$work/gp-arm-noinc.yaml" 2>&1)"; rc=$?
eq "--plan on the arm's plan with no incumbent: exit 2, its one line" "$rc|$out" "2|item incumbent: arm incumbent, and the plan names no incumbent release"
out="$(gate/run.sh --plan gate/plans/noise.yaml 9 2>&1)"; rc=$?
eq "--plan with more items asked for than the plan lists: exit 2" "$rc|$out" "2|ITEMS=9, and the plan lists 8"
yq '.dedline = 3 | .lease_ttl_s = 600 | .items[0] |= del(.duration) | .items[1].fault = "persistence-latenc" | .items[2].key = "pl"
  | .items[2].expect = "maybe" | .items += [{"key": "Big", "kind": "chaos", "scenario": "x", "expect": "BLOCK"}]
  | .items += [{"key": "fi", "kind": "fault", "fault": "wf-update-ownership-lost", "scenario": "x", "expect": "BLOCK", "rate": 0}]' "$td/gate/plan.yaml" > "$work/gp-bad.yaml"
out="$(gate/run.sh --plan "$work/gp-bad.yaml" 2>&1)"; rc=$?
eq "--plan on a plan with 12 problems: exit 2, one line each" "$rc|$(wc -l <<<"$out")" "2|12"
for r in "the plan: unknown key dedline" "lease_ttl_s is 600; a cell must outlive deadline_s and kill_grace_s by 60 s" \
  "item steady: duration is null, not a time such as 5m" "item pl: fault \"persistence-latenc\" is not a row of faults/catalog.yaml" \
  "item pl: expect is \"maybe\", not ADVANCE" "item pl is listed 2 times" "item Big: key \"Big\" is not a lowercase name" \
  "item fi: expect BLOCK is not built: a positive control must name the reason it expects" \
  "item Big: kind is \"chaos\", not load or fault" "item fi: fault wf-update-ownership-lost is a serverfi row" "item fi: rate is 0, not a whole number"; do
  has "--plan names: $r" "$out" "$r"
done
mkdir -p "$work/gr/usage-tmp"
mg() { env GATE_LOCK="$GLOCK" GATE_HOST_LOCK="$GHLOCK" GATE_POOL_LISTEN="127.0.0.1:$GP" TMPDIR="$work/gr/usage-tmp" bin/laug gate run "$@" -fake -allow-stale -results "$work/gr/usage" 2>&1; }
out="$(mg)"; rc=$?
eq "laug gate run with no tag: exit 2 before any gate run" "$rc|$(head -1 <<<"$out")" "2|laug · gate run: names no server release: give it the tag it judges, x.y.z ($lw gate run 1.31.0 -fake runs a gate on fakes)"
out="$(mg 1.31)"; rc=$?
eq "laug gate run 1.31: exit 2" "$rc|$(head -1 <<<"$out")" "2|laug · gate run: 1.31: not a release tag, x.y.z"
out="$(mg 1.31.0 -post pr-comment)"; rc=$?
eq "laug gate run -post pr-comment: exit 2, not wired, before any gate run: laug's first line, then gate/run.sh's refusal" \
  "$rc|$(head -1 <<<"$out" | cut -d: -f1)|$(sed -n 2p <<<"$out")|$(ls "$work/gr/usage" 2>/dev/null | wc -l)" \
  "2|laug · gate run 1.31.0 -fake|laug · GATE_POST=pr-comment: not wired: laug has no publish target yet; GATE_POST=local is the only sink|0"
eq "gate/post.sh --check: exit 0 for local, 2 for anything else" "$(gate/post.sh --check; echo $?)|$(GATE_POST=x gate/post.sh --check 2>/dev/null; echo $?)" "0|2"
# BASE main on a clone with no local main, as a clone of another branch and a pull request's checkout have: base_pick
# (gate/lib.sh) takes origin/main, else HEAD, and base_note says which on the line that names the run; a BASE that is
# not a commit is refused, naming -base. The fake git fails main^{commit}, and reads origin/main^{commit} as HEAD's, or
# fails it too with NM_ORIGIN=0.
NM="$work/nomain"; mkdir -p "$NM"
printf '%s\n' '#!/usr/bin/env bash' 'a=()' 'for x in "$@"; do case "$x" in' '  "main^{commit}") exit 1 ;;' \
  '  "origin/main^{commit}") [[ "${NM_ORIGIN:-1}" == 1 ]] || exit 1; x="HEAD^{commit}" ;;' 'esac; a+=("$x"); done' \
  'exec "$REAL_GIT" "${a[@]}"' > "$NM/git"; chmod +x "$NM/git"
nmpick() { ( export PATH="$NM:$PATH" BASE="$1" NM_ORIGIN="${2:-1}"; usage() { echo "laug · $*"; exit 2; }; . gate/lib.sh; base_pick 2>&1; echo "$BASE|$base_commit|$base_note" ); }
nmh="$("$REAL_GIT" rev-parse --short HEAD)"
eq "a BASE given that is a commit: its commit, and no note" "$(nmpick HEAD 0)" "HEAD|$nmh|"
eq "no local main: BASE is origin/main, and base_note says so" "$(nmpick main)" \
  "origin/main|$nmh|BASE origin/main ($nmh), as there is no local main; -base names another"
eq "no local main and no origin/main: BASE is HEAD, and base_note says so" "$(nmpick main 0)" \
  "HEAD|$nmh|BASE HEAD ($nmh), as there is no local main; -base names another"
eq "a BASE that is not a commit: exit 2, the refusal names -base" "$(nmpick nosuch; echo "exit $?")" \
  "laug · BASE=nosuch is not a commit in this repository: bin/laug gate run -base <commit> names one"$'\n'"exit 2"
grun nomain PATH="$NM:$PATH" NM_ORIGIN=0
eq "a gate on fakes with no main and no origin/main: ADVANCE; gate.json's base is HEAD's, base_ref HEAD; its id line says so" \
  "$grc|$(gj '[.base, .base_ref]' gate.json)|$(grep -c "^laug · gate $GID2: .*; BASE HEAD ($nmh), as there is no local main; -base names another\$" <<<"$gout")" \
  "0|[\"$nmh\",\"HEAD\"]|1"
gstopped && pass "no laugd left on 127.0.0.1:$GP after the gate cases" || fail "a laugd is left on 127.0.0.1:$GP after the gate cases"

section "gate/step.sh (workflow Gate's step driver: a gate run one step at a time, records and the guard; no laugd)"
# One gate run's first steps, its guard and its traps, with the fakes and the short-time plan, in a world of its own: two
# lock files, a results directory and GP's port, free again since the gate cases. Every call goes through st, which
# unsets the three workflow names: a gate whose steps run this test must not hand them its own. lsh runs one gate/lib.sh
# function on a run's records. The section prints its seconds: the plan gives it 10 s, which the lab run reports.
st_t0="$("$REAL_DATE" +%s%3N)"
SENV=(GATE_FAKE=1 SERVER_TAG=1.31.0 GATE_LOCK="$work/step.lock" GATE_HOST_LOCK="$work/step-host.lock"
  GATE_POOL_LISTEN="127.0.0.1:$GP" PLAN="$work/gp.yaml" GATE_RESULTS="$work/st/results" OMES_DIR="$work/st/omes"
  FAKE_GATE_DIR="$work/st/fake")
st() { env -u GATE_WORKFLOW_ID -u GATE_WORKFLOW_RUN_ID -u GATE_WORKER_COMMIT "${SENV[@]}" "$@"; }
lsh() { ( dir="$1"; shift; driver=step.sh; . gate/lib.sh; "$@" ); }   # lsh <dir> <function> [arg...]
pstart() { awk '{ print $22 }' "/proc/$1/stat" 2>&1; }   # field 22 of /proc/<pid>/stat, starttime
mkdir -p "$work/st/results" "$work/st/omes/cmd/omes" "$work/st/fake" "$work/st/elsewhere/g20261016-120000/.steps"
echo '{}' > "$work/st/elsewhere/g20261016-120000/.state.json"
bid="$(cat /proc/sys/kernel/random/boot_id)"

# The copies: every step.sh head that ends ", verbatim" is followed by the lines of gate/run.sh it names, byte for byte,
# and run.sh keeps its 367 lines, so every head's numbers still hold (run.sh edits its lines in place). 22 such heads
# is a floor: a head added later needs no edit here.
heads=0; bad=""
while IFS=: read -r ln h; do
  [[ "$h" =~ ^#\ run\.sh:([0-9]+)(-([0-9]+))?,\ verbatim$ ]] || continue
  n="${BASH_REMATCH[1]}"; m="${BASH_REMATCH[3]:-$n}"; heads=$(( heads + 1 ))
  cmp -s <(sed -n "${n},${m}p" gate/run.sh) <(sed -n "$(( ln + 1 )),$(( ln + 1 + m - n ))p" gate/step.sh) || bad+="$ln "
done < <(grep -n '^# run\.sh:' gate/step.sh)
eq "gate/step.sh: every '# run.sh:N-M, verbatim' head is followed by those lines of gate/run.sh, byte for byte (22 heads or more; run.sh 367 lines)" \
  "$(( heads >= 22 ))|${bad:-none}|$(wc -l < gate/run.sh)" "1|none|367"

# 1. Usage: nothing written.
o1="$(st gate/step.sh nope x 2>&1)"; r1=$?
o2="$(st gate/step.sh open /no/such 2>&1)"; r2=$?
o3="$(st gate/step.sh open "$work/st/elsewhere/g20261016-120000" 2>&1)"; r3=$?
eq "step.sh usage: a verb it does not know, a directory that is not there, a gate directory outside GATE_RESULTS: exit 2 each" \
  "$r1|$r2|$r3" "2|2|2"
eq "... nothing written: no .pid, no .done" "$(ls -A "$work/st/elsewhere/g20261016-120000/.steps" | wc -l)" 0
has "... the third says why" "$o3" "(outside the repository)/g20261016-120000 is not in GATE_RESULTS, (outside the repository)/results"
hasnt "... and no message names a path of this test" "$o1$o2$o3" "$work"

# 2. check makes the directory, with nothing the judge reads as an item.
st gate/step.sh check > "$work/st/check.out" 2> "$work/st/check.log"; rc=$?
sid="$(jq -r .gate_id "$work/st/check.out" 2>/dev/null)"; sdir="$work/st/results/$sid"
eq "check: exit 0, next, a gate id, the directory as shown gives it" \
  "$rc|$(jq -c '[.result, (.gate_id | test("^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$")), .dir == "(outside the repository)/\(.gate_id)"]' "$work/st/check.out" 2>&1)" \
  '0|["next",true,true]'
eq "check: ls shows nothing, ls -A .state.json and .steps; no workflow, this boot, no step yet" \
  "$(ls "$sdir" | wc -l)|$(ls -A "$sdir" | paste -sd ' ')|$(jq -c --arg b "$bid" '[.workflow_id, .workflow_run_id, .boot_id == $b, .step]' "$sdir/.state.json" 2>&1)" \
  '0|.state.json .steps|[null,null,true,null]'

# 3. open, as a workflow's step: GATE_WORKFLOW_ID and GATE_WORKER_COMMIT given after st's env -u.
h7="$(git rev-parse HEAD | cut -c1-7)"
st GATE_WORKFLOW_ID=wf-test GATE_WORKER_COMMIT="$h7" gate/step.sh open "$sdir" > "$sdir/.steps/open.out" 2> "$sdir/.steps/open.log"; rc=$?
hp="$(jq -r '.holder.pid // empty' "$sdir/.state.json" 2>/dev/null)"
eq "open: exit 0, next, locked true" "$rc|$(jq -c '[.result, .locked]' "$sdir/.steps/open.out" 2>&1)" '0|["next",true]'
eq "open: the timeline start, lock" "$(jq -r .step "$sdir/timeline.jsonl" 2>&1 | paste -sd ,)" "start,lock"
eq "open: .state.json's step lock; the holder's record has its start, field 22 of /proc/<pid>/stat" \
  "$(jq -r .step "$sdir/.state.json")|$(jq -r .holder.start "$sdir/.state.json")" "lock|$(pstart "$hp")"
eq "open: gate.json names the driver, the workflow and the worker's commit" "$(jq -c '[.driver, .workflow_id, .worker_commit]' "$sdir/gate.json" 2>&1)" \
  "[\"step.sh\",\"wf-test\",\"$h7\"]"
eq "open: clock.json's t0 is .state.json's" "$(jq -c .t0 "$sdir/clock.json" 2>&1)" "$(jq -c .t0 "$sdir/.state.json" 2>&1)"
eq "open: the answer is one line on fd 3, and .done's answer" "$(wc -l < "$sdir/.steps/open.out")|$(jq -c . "$sdir/.steps/open.out" 2>&1)" \
  "1|$(jq -c .answer "$sdir/.steps/open.done" 2>&1)"

# 4. The same step again exits 75 and writes nothing. The holder, by its record and by its lock: after its exec, its
# command line is sleep 2400, so pgrep by name would find nothing.
s1="$(sha256sum < "$sdir/.steps/open.done")"
st gate/step.sh open "$sdir" >> "$sdir/.steps/open.out" 2>> "$sdir/.steps/open.log"; rc=$?
eq "open again: exit 75, open.done unchanged, the timeline still 2 lines" \
  "$rc|$([[ "$(sha256sum < "$sdir/.steps/open.done")" == "$s1" ]] && echo unchanged)|$(wc -l < "$sdir/timeline.jsonl")" "75|unchanged|2"
flock -n "$work/step.lock" true; fr=$?
eq "the holder: its pid answers kill -0, with the recorded start, and step.lock is held (flock -n exits 1)" \
  "$(kill -0 "$hp" 2>/dev/null && echo alive)|$(pstart "$hp")|$fr" "alive|$(jq -r .holder.start "$sdir/.state.json")|1"

# 5. A second gate run beside it, its worker on another commit: every reason waits for preflight's refusal.
st gate/step.sh check > "$work/st/check2.out" 2>/dev/null
sdir2="$work/st/results/$(jq -r .gate_id "$work/st/check2.out" 2>/dev/null)"
st GATE_WORKER_COMMIT=0000000 gate/step.sh open "$sdir2" > "$sdir2/.steps/open.out" 2> "$sdir2/.steps/open.log"
st gate/step.sh preflight "$sdir2" > "$sdir2/.steps/preflight.out" 2> "$sdir2/.steps/preflight.log"; rc=$?
eq "a second run: open says locked false; preflight answers refused, exit 0, .done's exit 0" \
  "$(jq -c .locked "$sdir2/.steps/open.out" 2>&1)|$rc|$(jq -r .result "$sdir2/.steps/preflight.out" 2>&1)|$(jq -c .exit "$sdir2/.steps/preflight.done" 2>&1)" \
  "false|0|refused|0"
for r in "another gate run holds (outside the repository)/step.lock: wait for it to end" \
  "another gate run on this host holds (outside the repository)/step-host.lock: wait for it to end" \
  "the worker runs commit 0000000, and this checkout's HEAD is $h7: make build, then restart bin/laug orchestrator worker"; do
  has "... refused, naming: $r" "$(jq -r .refused "$sdir2/.steps/preflight.out" 2>&1)" "$r"
done

# 6. preflight writes what gate/run.sh wrote (the first gate case, the same plan and world).
st gate/step.sh preflight "$sdir" > "$sdir/.steps/preflight.out" 2> "$sdir/.steps/preflight.log"; rc=$?
mr="$(ls -d "$work/gr/main/results"/g* 2>/dev/null | head -1)"; gdel='del(.gate_id, .wiring, .driver, .workflow_id, .worker_commit)'
eq "preflight: exit 0, next, the keys steady pl kh" "$rc|$(jq -c '[.result, .keys]' "$sdir/.steps/preflight.out" 2>&1)" '0|["next",["steady","pl","kh"]]'
eq "preflight: plan.json and thresholds.json byte for byte the first gate case's" \
  "$(cmp -s "$sdir/plan.json" "$mr/plan.json" && echo same)|$(cmp -s "$sdir/thresholds.json" "$mr/thresholds.json" && echo same)" "same|same"
eq "preflight: gate.json the first gate case's, but for its ids and driver fields" "$(jq -S -c "$gdel" "$sdir/gate.json" 2>&1)" "$(jq -S -c "$gdel" "$mr/gate.json" 2>&1)"
eq "preflight: gate.json host.type m8i.8xlarge, the set's: the fake host passes the host check" "$(jq -c .host.type "$sdir/gate.json" 2>&1)" '"m8i.8xlarge"'

# 7. The guard. A pid whose start is not the record's gets nothing; a zombie, whose start still reads, and a record
# taken after its process ended (start null) are gone, with no guard line.
setsid sleep 30 > /dev/null 2>&1 & hsp=$!; hst="$(pstart "$hsp")"   # starttime is set at fork, and kept through exec
lsh "$sdir" state_put --argjson p "$hsp" --argjson s "$(( hst + 1 ))" --arg b "$bid" '.harness = {pid: $p, start: $s, boot_id: $b}'
out="$(lsh "$sdir" alive_recorded harness 2>&1)"; rc=$?
eq "the guard: a harness record with another start than its pid's: false, and the guard line" "$rc|$out" \
  "1|laug · guard: harness pid $hsp started at $hst, recorded $(( hst + 1 )): nothing sent"
lsh "$sdir" stop_recorded harness 2>/dev/null
kill -0 "$hsp" 2>/dev/null && pass "... stop_recorded sends that pid nothing" || fail "... stop_recorded sends that pid nothing"
lsh "$sdir" state_put --argjson p "$hsp" --argjson s "$hst" '.harness = {pid: $p, start: $s, boot_id: "00000000-0000-0000-0000-000000000000"}'
out="$(lsh "$sdir" alive_recorded harness 2>&1)"; rc=$?
eq "the guard: a harness record with its pid's start, from another boot: false, and the guard line names both boots" "$rc|$out" \
  "1|laug · guard: harness pid $hsp was recorded on boot 00000000-0000-0000-0000-000000000000, this is boot $bid: nothing sent"
( sleep 0.05 & echo $! > "$work/st/zpid"; exec sleep 3 ) > /dev/null 2>&1 &
zp=""; for _ in $(seq 1 40); do zp="$(cat "$work/st/zpid" 2>/dev/null)"; [[ -n "$zp" && "$(awk '{ print $3 }' "/proc/$zp/stat" 2>/dev/null)" == Z ]] && break; sleep 0.05; done
lsh "$sdir" state_put --argjson p "${zp:-0}" --argjson s "$(pstart "$zp")" --argjson h "$hsp" --arg b "$bid" \
  '.zombie = {pid: $p, start: $s, boot_id: $b} | .ended = {pid: $h, start: null, boot_id: $b}'
o1="$(lsh "$sdir" alive_recorded zombie 2>&1)"; r1=$?; o2="$(lsh "$sdir" alive_recorded ended 2>&1)"; r2=$?
eq "the guard: a zombie with its true start, and a live pid recorded with start null: false each, no guard line" \
  "$(awk '{ print $3 }' "/proc/$zp/stat" 2>&1)|$r1|$o1|$r2|$o2" "Z|1||1|"

# 8. TERM to a step in wait_harness, to its pid, as Stop sends it: the trap answers stopped and leaves the harness.
lsh "$sdir" state_put --argjson p "$hsp" --argjson s "$hst" --arg b "$bid" --argjson ms "$("$REAL_DATE" +%s%3N)" \
  '.harness = {pid: $p, start: $s, boot_id: $b} | .harness_start_ms = $ms'
st setsid gate/step.sh barrier "$sdir" > "$sdir/.steps/barrier.out" 2> "$sdir/.steps/barrier.log" & bp=$!
for _ in $(seq 1 100); do [[ "$(jq -r .step "$sdir/.state.json" 2>/dev/null)" == barrier ]] && break; sleep 0.05; done
kill -TERM "$(jq -r .pid "$sdir/.steps/barrier.pid" 2>/dev/null)" 2>/dev/null; t="$("$REAL_DATE" +%s%3N)"
for _ in $(seq 1 40); do [[ -s "$sdir/.steps/barrier.done" ]] && break; sleep 0.05; done
el=$(( $("$REAL_DATE" +%s%3N) - t )); wait "$bp" 2>/dev/null
eq "TERM to the barrier step in wait_harness: .done within 2 s, exit 143, stopped; the harness still runs" \
  "$(( el < 2000 ))|$(jq -c '[.exit, .answer]' "$sdir/.steps/barrier.done" 2>&1)|$(kill -0 "$hsp" 2>/dev/null && echo alive)" \
  '1|[143,{"result":"stopped"}]|alive'
eq "... the timeline's last line and .state.json's step: barrier" "$(tail -1 "$sdir/timeline.jsonl" | jq -r .step 2>&1)|$(jq -r .step "$sdir/.state.json" 2>&1)" \
  "barrier|barrier"

# 9. The holder stopped by its record frees both locks, and the next step that needs them refuses.
lsh "$sdir" stop_recorded holder
flock -n "$work/step.lock" true; f1=$?; flock -n "$work/step-host.lock" true; f2=$?
st gate/step.sh wave-time "$sdir" > "$sdir/.steps/wave-time.out" 2> "$sdir/.steps/wave-time.log"; rc=$?
eq "the holder stopped by its record: both locks free; wave-time refused, exit 0" \
  "$f1|$f2|$rc|$(jq -c '[.result, .refused]' "$sdir/.steps/wave-time.out" 2>&1)" '0|0|0|["refused","the lock holder exited; the locks are free"]'

# 10. release: no laugd to stop, the harness stopped by its record, then done. hsp is this test's child: a zombie
# until reaped here.
st gate/step.sh release "$sdir" > "$sdir/.steps/release.out" 2> "$sdir/.steps/release.log"; rc=$?
hs="$(awk '{ print $3 }' "/proc/$hsp/stat" 2>/dev/null)"; [[ -z "$hs" || "$hs" == Z ]] && hs=gone || kill "$hsp" 2>/dev/null; wait "$hsp" 2>/dev/null
eq "release: done, laugd not started, the harness stopped, the timeline ends release, done" \
  "$rc|$(jq -c '[.result, .laugd]' "$sdir/.steps/release.out" 2>&1)|$hs|$(tail -2 "$sdir/timeline.jsonl" | jq -r .step 2>&1 | paste -sd ,)" \
  '0|["done","not started"]|gone|release,done'
st gate/step.sh release "$sdir2" > "$sdir2/.steps/release.out" 2> "$sdir2/.steps/release.log"; rc=$?
eq "release of the second run: done; neither holder record passes the guard" \
  "$rc|$(jq -r .result "$sdir2/.steps/release.out" 2>&1)|$(lsh "$sdir" alive_recorded holder 2>&1 || echo gone)|$(lsh "$sdir2" alive_recorded holder 2>&1 || echo gone)" \
  "0|done|gone|gone"
# 11. A lease names one of plan.json's keys, or writes nothing. A run whose open never got as far as t0 still gets JSON
# from the stop path: clock.json with t0 null, stopped at step start, and release's lines with no time.
st gate/step.sh check > "$work/st/check3.out" 2>/dev/null
sdir3="$work/st/results/$(jq -r .gate_id "$work/st/check3.out" 2>/dev/null)"
st gate/step.sh lease "$sdir" zz > /dev/null 2>&1; r1=$?; st gate/step.sh lease "$sdir3" steady > /dev/null 2>&1; r2=$?
eq "lease of a key plan.json does not list, and a lease before plan.json: exit 2 each, nothing written" \
  "$r1|$r2|$(ls -A "$sdir/.steps" "$sdir3/.steps" | grep -c '^lease-')" "2|2|0"
st gate/step.sh stopped "$sdir3" "step open: StepFailed" > "$sdir3/.steps/stopped.out" 2> "$sdir3/.steps/stopped.log"; rc=$?
eq "stopped before open's t0: done, stopped at start, a trap BLOCK; clock.json parses, t0 null" \
  "$rc|$(jq -c '[.result, .stopped_at, .verdict, .trap]' "$sdir3/.steps/stopped.out" 2>&1)|$(jq -c .t0 "$sdir3/clock.json" 2>&1)" \
  '0|["done","start","BLOCK",true]|null'
has "... verdict.json's reason names step start" "$(jq -r '.reasons[0]' "$sdir3/verdict.json" 2>&1)" "stopped at step start (step open: StepFailed)"
st gate/step.sh release "$sdir3" stopped > "$sdir3/.steps/release.out" 2> "$sdir3/.steps/release.log"; rc=$?
eq "release stopped before open's t0: done, and its line gives no time" \
  "$rc|$(jq -r .result "$sdir3/.steps/release.out" 2>&1)|$(grep -cF '+?:?? release ' "$sdir3/.steps/release.log")" "0|done|1"

# 12. A .state.json that does not parse stops a step under set -e, before anything else runs.
st gate/step.sh check > "$work/st/check4.out" 2>/dev/null
sdir4="$work/st/results/$(jq -r .gate_id "$work/st/check4.out" 2>/dev/null)"
printf '{"workflow_id":null,"boot_' > "$sdir4/.state.json"
st gate/step.sh release "$sdir4" > /dev/null 2>&1; rc=$?
eq "release on a .state.json that does not parse: exit not 0 and .done's exit the same, no cleanup.json" \
  "$(( rc != 0 ))|$(jq -r .exit "$sdir4/.steps/release.done" 2>&1)|$([[ -e "$sdir4/cleanup.json" ]] && echo cleanup || echo none)" "1|$rc|none"
has "... .done's why names the line of gate/step.sh that failed" "$(jq -r .why "$sdir4/.steps/release.done" 2>&1)" "gate/step.sh line "
# 13. stopped's sweep, by records alone: pl's item, in pl/.pid, gets TERM and leaves in its session a timeout (a process
# group of its own, which a TERM to the item's group misses) and, under setsid, a load that names run id <gate id>-pl
# with the item's LEASE_FILE, as faults/run.sh's does: both get TERM by session. Another run's item with this gate id
# (fake-item.sh, from a results directory of its own, its scenario argument the run id) and a lease request for steady,
# a key with no record before any wave, run on: the match by name gate/step.sh once used TERMed both.
st gate/step.sh check > "$work/st/check5.out" 2>/dev/null
g5="$(jq -r .gate_id "$work/st/check5.out" 2>/dev/null)"; sdir5="$work/st/results/$g5"; sw5="$work/st/sweep"
mkdir -p "$sdir5/pl" "$sw5/other/pl"; cp "$sdir/plan.json" "$sdir5/plan.json"
printf '{"id":"0000abcd","address":"127.0.0.1:1","server_tag":"1.31.0"}\n' > "$sw5/other/pl/.lease.json"
env RUN_ID="$g5-pl" RESULTS_DIR="$sw5/other/pl" LEASE_FILE="$sw5/other/pl/.lease.json" FAKE_GATE_DIR="$sw5/other" FAKE_GATE_HANG=pl \
  setsid "$td/gate/fake-item.sh" fault pl "$g5-pl" - "$sw5/other/pl/.lease.json" > /dev/null 2>&1 & op5=$!
setsid bash -c 'sleep 30; :' "gate-$g5-steady" > /dev/null 2>&1 & lq5=$!
# shellcheck disable=SC2016  # expanded by the item's own bash
env LEASE_FILE="$(lsh "$sdir5" rel "$sdir5/pl/.lease.json")" setsid bash -c \
  'timeout 30 sleep 30 & echo $! > "$1/to"; setsid bash -c "sleep 30; :" "$2" & echo $! > "$1/load"; exec sleep 30' \
  item "$sw5" "$g5-pl" > /dev/null 2>&1 & ip5=$!
for _ in $(seq 1 60); do [[ -s "$sw5/to" && -s "$sw5/load" && -s "$sw5/other/pl/fake-item.json" ]] && break; sleep 0.05; done
to5="$(cat "$sw5/to" 2>/dev/null)"; ld5="$(cat "$sw5/load" 2>/dev/null)"
printf '{"pid":%d,"pgid":%d,"start":%s,"boot_id":"%s"}\n' "$ip5" "$ip5" "$(pstart "$ip5")" "$bid" > "$sdir5/pl/.pid"
st gate/step.sh stopped "$sdir5" "sweep test" > "$sdir5/.steps/stopped.out" 2> "$sdir5/.steps/stopped.log"; rc=$?
ps5() { local _; for _ in $(seq 1 40); do   # gone or running, as gone reads it; with wait, it waits for gone
    gone "$1" && { echo gone; return 0; }
    [[ "${2:-}" != wait ]] && { echo running; return 0; }; sleep 0.05; done; echo running; }
eq "stopped's sweep: another run's item that names run id <gate id>-pl, and a lease request for steady before any wave, run on" \
  "$rc|$(ps5 "$op5")|$(ps5 "$lq5")" "0|running|running"
eq "... and pl's leftovers get TERM by session: a timeout's group in the item's session, and its load under setsid" \
  "$(ps5 "${to5:-0}" wait)|$(ps5 "${ld5:-0}" wait)|$(grep -c "TERM by session to what is left in session(s) $ip5,$ld5: the item's, and 1 by run id $g5-pl" "$sdir5/.steps/stopped.log")" \
  "gone|gone|1"
for q5 in "$op5" "$lq5" ${to5:+"$to5"} ${ld5:+"$ld5"}; do kill -- "-$q5" 2>/dev/null; done; wait "$op5" "$lq5" "$ip5" 2>/dev/null
# 14. A key with no record once the wave began (a wave step that ended between an item's launch and its record): by the
# item's LEASE_FILE alone. pl's item, whose command line names its lease file's path, <gate id>/pl, and no run id, as
# faults/run.sh's does, gets TERM, and so does its load under setsid; another run's process that names run id
# <gate id>-pl, with a LEASE_FILE of its own, runs on. The match by name that stopped's sweep once used here TERMed
# it and missed both.
st gate/step.sh check > "$work/st/check6.out" 2>/dev/null
g6="$(jq -r .gate_id "$work/st/check6.out" 2>/dev/null)"; sdir6="$work/st/results/$g6"; sw6="$work/st/sweep6"
mkdir -p "$sdir6/pl" "$sw6/other/pl"; cp "$sdir/plan.json" "$sdir6/plan.json"; lf6="$(lsh "$sdir6" rel "$sdir6/pl/.lease.json")"
( exit 0 ) & wp6=$!; wait "$wp6"   # the wave's record names a process that has ended
lsh "$sdir6" state_put --argjson p "$wp6" --arg b "$bid" '.wave = {pid: $p, start: null, boot_id: $b}'
env RUN_ID="$g6-pl" LEASE_FILE="$sw6/other/pl/.lease.json" setsid bash -c 'sleep 30; :' "omes --run-id $g6-pl" > /dev/null 2>&1 & op6=$!
# shellcheck disable=SC2016  # expanded by the item's own bash
env LEASE_FILE="$lf6" setsid bash -c 'setsid bash -c "sleep 30; :" load & echo $! > "$1/load"; exec sleep 30' \
  faults/run.sh "$sw6" "$lf6" > /dev/null 2>&1 & ip6=$!
for _ in $(seq 1 60); do [[ -s "$sw6/load" ]] && break; sleep 0.05; done; ld6="$(cat "$sw6/load" 2>/dev/null)"
st gate/step.sh stopped "$sdir6" "sweep test" > "$sdir6/.steps/stopped.out" 2> "$sdir6/.steps/stopped.log"; rc=$?
eq "stopped's sweep, pl with no record once the wave began: another run's process that names run id <gate id>-pl runs on" \
  "$rc|$(ps5 "$op6")" "0|running"
eq "... and pl's item and its load under setsid, which name no run id, get TERM by LEASE_FILE" \
  "$(ps5 "$ip6" wait)|$(ps5 "${ld6:-0}" wait)|$(grep -c "TERM by LEASE_FILE, no pid recorded: what is left in session(s) $(printf '%s\n' "$ip6" "${ld6:-0}" | sort -n | paste -sd ,), which hold the item's" "$sdir6/.steps/stopped.log")" \
  "gone|gone|1"
for q6 in "$op6" "$ip6" ${ld6:+"$ld6"}; do kill -- "-$q6" 2>/dev/null; done; wait "$op6" "$ip6" 2>/dev/null
# 15. pl's record names a pid that is another process now (its start is not the record's): that process, in a session
# of its own, runs on; pl's load under setsid, which names run id <gate id>-pl and holds pl's LEASE_FILE, gets TERM.
st gate/step.sh check > "$work/st/check7.out" 2>/dev/null
g7="$(jq -r .gate_id "$work/st/check7.out" 2>/dev/null)"; sdir7="$work/st/results/$g7"
mkdir -p "$sdir7/pl"; cp "$sdir/plan.json" "$sdir7/plan.json"
setsid sleep 30 > /dev/null 2>&1 & np7=$!
env LEASE_FILE="$(lsh "$sdir7" rel "$sdir7/pl/.lease.json")" setsid bash -c 'sleep 30; :' "omes --run-id $g7-pl" > /dev/null 2>&1 & ld7=$!
for _ in $(seq 1 40); do [[ "$(pstart "$np7")" =~ ^[0-9]+$ && -r "/proc/$ld7/environ" ]] && break; sleep 0.05; done
printf '{"pid":%d,"pgid":%d,"start":%s,"boot_id":"%s"}\n' "$np7" "$np7" "$(( $(pstart "$np7") + 1 ))" "$bid" > "$sdir7/pl/.pid"
st gate/step.sh stopped "$sdir7" "sweep test" > "$sdir7/.steps/stopped.out" 2> "$sdir7/.steps/stopped.log"; rc=$?
eq "stopped's sweep, pl's record naming a pid that is another process now: that process runs on" "$rc|$(ps5 "$np7")" "0|running"
eq "... and pl's load under setsid gets TERM by session, the say line naming the pid as another process" \
  "$(ps5 "$ld7" wait)|$(grep -c "TERM by session to what is left in session(s) $ld7: pid $np7 is another process now, and 1 by run id $g7-pl and its LEASE_FILE" "$sdir7/.steps/stopped.log")" \
  "gone|1"
for q7 in "$np7" "$ld7"; do kill -- "-$q7" 2>/dev/null; done; wait "$np7" "$ld7" 2>/dev/null
# 16. preflight on the arm's plan writes thresholds-incumbent.json, as run.sh's does (step.sh's preflight lines are
# run.sh's, the head check above). Both locks are free again: the other runs' holders were stopped above.
st gate/step.sh check > "$work/st/check8.out" 2>/dev/null
sdir8="$work/st/results/$(jq -r .gate_id "$work/st/check8.out" 2>/dev/null)"
st PLAN="$work/gp-arm.yaml" gate/step.sh open "$sdir8" > "$sdir8/.steps/open.out" 2> "$sdir8/.steps/open.log"
st PLAN="$work/gp-arm.yaml" gate/step.sh preflight "$sdir8" > "$sdir8/.steps/preflight.out" 2> "$sdir8/.steps/preflight.log"; rc=$?
eq "preflight on the arm's plan: next, the keys incumbent steady pl, and thresholds-incumbent.json load/thresholds.sh 1.31.0" \
  "$rc|$(jq -c '[.result, .keys]' "$sdir8/.steps/preflight.out" 2>&1)|$(jq -c '[.resolved.server_tag, .resolved.release_file]' "$sdir8/thresholds-incumbent.json" 2>&1)" \
  '0|["next",["incumbent","steady","pl"]]|["1.31.0",null]'
st PLAN="$work/gp-arm.yaml" gate/step.sh release "$sdir8" > "$sdir8/.steps/release.out" 2> "$sdir8/.steps/release.log"
# 17. The bin/laug that cannot judge of the gate/run.sh section's case 4c, through gate/step.sh from nj/tree: preflight
# answers refused, naming -identity; combine finds the judge wrote nothing, writes the trap verdict itself and answers
# done, BLOCK, exit 1, trap true, as run.sh's EXIT trap would.
njs() { st PATH="$NJ/git:$PATH" "$NJ/tree/gate/step.sh" "$@"; }
njs check > "$work/st/check9.out" 2>/dev/null
sdir9="$work/st/results/$(jq -r .gate_id "$work/st/check9.out" 2>/dev/null)"
njs open "$sdir9" > "$sdir9/.steps/open.out" 2> "$sdir9/.steps/open.log"
njs preflight "$sdir9" > "$sdir9/.steps/preflight.out" 2> "$sdir9/.steps/preflight.log"; r1=$?
njs combine "$sdir9" > "$sdir9/.steps/judge.out" 2> "$sdir9/.steps/judge.log"; r2=$?
njs release "$sdir9" > "$sdir9/.steps/release.out" 2> "$sdir9/.steps/release.log"
eq "a bin/laug that cannot judge, through gate/step.sh: preflight refused, naming -identity; combine done, a trap BLOCK, exit 1" \
  "$r1|$(jq -c '[.result, .refused]' "$sdir9/.steps/preflight.out" 2>&1)|$r2|$(jq -c . "$sdir9/.steps/judge.out" 2>&1)" \
  "0|$(jq -cn --arg r "$njr" '["refused", $r]')|0|{\"result\":\"done\",\"verdict\":\"BLOCK\",\"exit\":1,\"trap\":true}"
eq "... verdict.json the trap verdict at combine, BLOCK, class invalid, refused naming -identity" \
  "$(jq -c '[.verdict, .class, .stopped_at, .refused, .reasons[0]]' "$sdir9/verdict.json" 2>&1)" "$(njv step.sh)"
printf 'step.sh section: %s s\n' "$(awk -v a="$st_t0" -v b="$("$REAL_DATE" +%s%3N)" 'BEGIN { printf "%.1f", (b - a) / 1000 }')"

section "laug gate tolerance (the incumbent arm's tolerances from A/A gates)"
# load/testdata/gate/mk-tolerance.sh writes the gate runs, each holding the files laug gate tolerance reads, none
# committed: 18 into tol (three A/A gates with known d, fourteen refused by one rule each, and -115000, counted with a
# reading not produced), and 20 A/A gates each into jump and steady. tol <gate ids> [flag ...] runs bin/laug gate
# tolerance on the gate ids, with -results the tol runs unless a flag after it names others: stdout in out, stderr in
# err, the exit in rc. trow <reading> prints its row's n, m, the largest d and T. Check 9 reads the six committed noise
# waves in gate/results through laug gate tolerance.
TL="$work/tol"
for s in "" jump steady; do load/testdata/gate/mk-tolerance.sh "$TL/${s:-tol}" $s || fail "mk-tolerance.sh $s wrote its gate runs"; done
tol() { local g="$1"; shift; out="$(bin/laug gate tolerance $g -results "$TL/tol" "$@" 2>"$TL/err")"; rc=$?; err="$(cat "$TL/err")"; }
trow() { awk -v n="$1" '$1 == n { print $3, $4, $5, $6 }' <<<"$out" | sed 's/ *$//'; }
mtol() { bin/laug gate tolerance "$@" 2>&1; }
CMP5=(workflow_task_schedule_to_start_p99_ms persistence_latency_p99_ms activity_schedule_to_start_over_10ms frontend_request_over_10ms
  workflow_end_to_end_over_50ms)
A3="g20261020-100000 g20261020-101000 g20261020-102000"
# 1. The three A/A gates: d per gate is mk-tolerance.sh's; T = max(2 x largest - m, floor), rounded up, by hand.
tol "$A3"; out1="$out"
eq "the three A/A gates ($A3): exit 0; n 3; workflow task m 0.200, largest 0.500, T 0.80; persistence -0.003, 0.030, T 0.10 (its floor); activity share 0.001667, 0.004000, T 0.0064; frontend share 0.000400, 0.001000, T 0.0016; end-to-end share 0, 0, T 0.0005 (its floor); the gates line names each gate's driver and commit abc1234" \
  "$rc|$(for r in "${CMP5[@]}"; do trow "$r"; done | paste -sd '|')|$(grep '^gates: ' <<<"$out")" \
  "0|3 0.200 0.500 0.80|3 -0.003 0.030 0.10|3 0.001667 0.004000 0.0064|3 0.000400 0.001000 0.0016|3 0.000000 0.000000 0.0005|gates: g20261020-100000 run.sh abc1234, g20261020-101000 step.sh abc1234, g20261020-102000 run.sh abc1234"
# 2. to 4. The jump check: the first 20 gates by id, the largest d of 11 to 20 against the largest of 1 to 10, over 2 sd.
eq "under 20 gates: the jump check on each of the five compared readings says not checked: n under 20" \
  "$(for r in "${CMP5[@]}"; do grep "^$r " <<<"$out1" | sed -E 's/^.*  //'; done | sort | uniq -c | sed 's/^ *//')" "5 not checked: n under 20"
tol "$(ls "$TL/jump" | paste -sd ' ')" -results "$TL/jump"
eq "twenty gates, the last 1.4 ms past the rest: FIRES (first half 0.100, later half 1.500, 2 sd 0.626)" \
  "$rc|$(grep '^workflow_task_schedule_to_start_p99_ms ' <<<"$out" | sed -E 's/^.*  //')" "0|FIRES (first half 0.100, later half 1.500, 2 sd 0.626)"
tol "$(ls "$TL/steady" | paste -sd ' ')" -results "$TL/steady"
eq "twenty gates alternating 0.1 and 0.2: holds (first half 0.200, later half 0.200, 2 sd 0.103)" \
  "$rc|$(grep '^workflow_task_schedule_to_start_p99_ms ' <<<"$out" | sed -E 's/^.*  //')" "0|holds (first half 0.200, later half 0.200, 2 sd 0.103)"
# 5. The means by driver (gate.json's driver), and the line to the owner when the two are over half of T apart.
eq "by driver: run.sh n 2 m 0.350 (0.150) and step.sh n 1 m -0.100 on workflow task; to the owner: drivers 0.450 apart, over half of T, 0.400" \
  "$(grep -cF '  run.sh: workflow_task_schedule_to_start_p99_ms n 2 m 0.350 (0.150);' <<<"$out1")|$(grep -cF '  step.sh: workflow_task_schedule_to_start_p99_ms n 1 m -0.100 (-);' <<<"$out1")|$(grep -cxF '  drivers: the mean d of run.sh and step.sh are 0.450 apart, over half of T, 0.400, on workflow_task_schedule_to_start_p99_ms' <<<"$out1")" \
  "1|1|1"
# 6. Every refusal, one gate a line, and nothing computed.
tol "$A3 $(seq -f 'g20261020-1%02g000' 3 14 | paste -sd ' ') g20261020-116000 g20261020-117000"
eq "the fourteen refused gates, with the three A/A first: exit 2, 14 gate(s) refused; nothing computed, one line each, nothing on stdout" \
  "$rc|$out|$err" "2||$(printf '%s\n' 'laug · gate tolerance: 14 gate(s) refused; nothing computed' \
  '  g20261020-103000: not A/A: server 1.32.1, incumbent 1.31.0' \
  '  g20261020-104000: the host was not quiet: the poller saw up to 1 busy cell(s) at laugd, in 1 of 77 polls' \
  '  g20261020-105000: runs another wave than g20261020-100000: [load:incumbent workflow_with_single_noop_activity 20/s 50 5m, load workflow_with_single_noop_activity 20/s 50 5m]' \
  '  g20261020-106000: R4 outcome failed: steady: expected ADVANCE, observed BLOCK' \
  '  g20261020-107000: a trap verdict (stopped at wave): no rule was judged' \
  '  g20261020-108000: plan.json names no item with arm incumbent; -pair <a>:<b> reads two plain items' \
  '  g20261020-109000: verdict.json has no comparison' \
  '  g20261020-110000: ran on host type other, and load/steady-state.yaml names m8i.8xlarge' \
  '  g20261020-111000: A/A gates of 1.32.1, and g20261020-100000 ran 1.31.0: one release a calibration' \
  '  g20261020-112000: its comparison reads not compared on persistence_latency_p99_ms: R7 saw no pair to compare' \
  '  g20261020-113000: no data on steady for frontend_request_over_10ms' \
  '  g20261020-114000: steady/known-slow.json is there: a gate of the arm-known-slow test branch, a control, not an A/A gate' \
  '  g20261020-116000: no verdict.json' \
  '  g20261020-117000: no verdict JSON for steady')"
# 7. A null that both sets mark not_judged is not produced: the gate counts, and that reading's n is one less.
tol g20261020-115000
eq "a reading not produced on both sides: g20261020-115000 alone is counted, n 1 on every compared reading but frontend_request_over_10ms, n 0 there, its T and its block's tolerance null" \
  "$rc|$(for r in "${CMP5[@]}"; do trow "$r"; done | paste -sd '|')|$(grep -cxF '    - {name: frontend_request_over_10ms, scale: share, floor: 0.0005, tolerance: null}' <<<"$out")" \
  "0|1 0.000 0.000 0.10|1 0.000 0.000 0.10|1 0.000000 0.000000 0.0005|0 - - -|1 0.000000 0.000000 0.0005|1"
# 8. The compare: block set in place of load/steady-state.yaml's, its comments kept: through the set check.
tsplice() { sed -n '/^compare:$/,$p' <<<"$out" > "$TL/block.yaml"
  awk -v b="$TL/block.yaml" '/^compare:/ { while ((getline l < b) > 0) print l; skip = 1; next } skip && /^[^ #]/ { skip = 0 } !skip' \
    load/steady-state.yaml > "$TL/set.yaml"; }
tol "$A3" -evidence cells/results/arm-tolerance-check.txt; tsplice
o8="$(yq -o=json -I=0 '.compare | [.gates, .evidence, [.rows[].tolerance]]' "$TL/block.yaml" 2>&1)|$(THRESHOLDS="$TL/set.yaml" load/thresholds.sh "" 2>&1 | yq -o=json -I=0 '[.compare.gates, [.compare.rows[].tolerance], .verdict.block_if_any_exceeds]' 2>&1)"
tol "$A3"; tsplice
eq "the compare: block parses, and load/thresholds.sh accepts a copy of the set carrying it with -evidence given; without -evidence it prints evidence null and the set check refuses it" \
  "$o8|$(grep -c '^  evidence: null   # -evidence cells/results/<file> names it' <<<"$out")|$(THRESHOLDS="$TL/set.yaml" load/thresholds.sh "" 2>&1 >/dev/null; echo "rc $?")" \
  "[3,\"cells/results/arm-tolerance-check.txt\",[0.8,0.1,0.0064,0.0016,0.0005]]|[3,[0.8,0.1,0.0064,0.0016,0.0005],true]|1|thresholds: $TL/set.yaml cannot be used as a threshold set: compare: a row has a tolerance, and evidence is null: a tolerance names its results file in cells/results/ or docs/tables/gates.jsonl
rc 2"
# 9. PAIR=s1:s2 on the six committed noise waves (gate/plans/noise.yaml), read from their rows in docs/tables
# (-tables), which give what their directories gave (internal/tolerance's TestRowsMatchDirectories), d = s2 minus s1,
# the second leased minus the first. They predate the shares, so the five shares read n 0.
out="$(mtol g20261015-141922 g20261015-142759 g20261015-143633 g20261015-144517 g20261015-145418 g20261015-150315 -pair s1:s2 -tables)"; rc=$?
eq "-pair s1:s2 on the six committed noise waves: n 6; largest d 0.449, 0.040, 2.510, 1.382, 0.072 ms and mean d 0.062, -0.050, 0.619, 0.409, 0.007 ms for workflow task, persistence, activity, frontend and end-to-end (the last three their p-readings, recorded); T 0.84 and 0.13; no compare: block" \
  "$rc|$(for r in workflow_task_schedule_to_start_p99_ms persistence_latency_p99_ms activity_schedule_to_start_p99_ms frontend_request_p99_ms workflow_end_to_end_p95_ms; do trow "$r"; done | paste -sd '|')|$(grep -c '^compare:' <<<"$out")" \
  "0|6 0.062 0.449 0.84|6 -0.050 0.040 0.13|6 0.619 2.510|6 0.409 1.382|6 0.007 0.072|0"
# 10. Usage, through laug gate tolerance: exit 2 and one line, laug's or internal/tolerance's usage line, before any gate
# is read. laug checks each gate id in its own words; internal/tolerance's Check checks -pair and each gate's place in
# -results, with its usage line. t10 prints the first line.
U='laug · gate tolerance <gate id>... [-pair <a>:<b>] [-evidence cells/results/<file>]'
t10() { local o rc; o="$(mtol "$@")"; rc=$?; printf '%s\nrc %s' "$(head -1 <<<"$o")" "$rc"; }
o10="$(t10 -results "$TL/tol")#$(t10 nope -results "$TL/tol")#$(t10 g20261020-100000 -pair a:a -results "$TL/tol")#$(t10 g20261020-999999 -results "$TL/tol")"
eq "usage: no gate, a word that is no gate id, -pair a:a, a run not in -results: exit 2, laug's line or internal/tolerance's usage line" "$o10" \
  "laug · gate tolerance: names no gate run: give it the A/A gates' ids in $(shown "$TL/tol")
rc 2#laug · gate tolerance: nope is not a gate id, g<date>-<time>
rc 2#$U: -pair a:a names one item twice
rc 2#$U: no gate run g20261020-999999 in -results, $(shown "$TL/tol")
rc 2"
# 11. The set is laug gate run's: THRESHOLDS and THRESHOLDS_RELEASES from the environment are unset first.
yq '.host.type = "dev-host"' load/steady-state.yaml > "$TL/dev.yaml"
THRESHOLDS="$TL/dev.yaml" tol "$A3"
eq "THRESHOLDS in the environment naming a copy of the set whose host type is dev-host: check 1's gates are taken and the header says m8i.8xlarge" \
  "$rc|$(head -1 <<<"$out")" "0|laug · gate tolerance: 3 gate(s), A/A gates of 1.31.0 on m8i.8xlarge, the incumbent arm"

section "runbook 1's block, the long walk README.md's block is cut from, by bin/laug (a pool of fakes, a lease, the thresholds, a gate on fakes, a rejudge)"
# The block a fresh clone runs to try laug with no Docker and no cell: docs/runbooks/1-offline.md's one command block,
# line for line but its last, make test, which is this test and the suites beside it:
#   make build; bin/laug deps -offline
#   bin/laug pool serve & sleep 2
#   bin/laug lease; bin/laug pool status; bin/laug release -last
#   kill $!; wait $!
#   bin/laug thresholds -server-tag 1.32.1
#   bin/laug gate run 1.31.0 -fake
#   bin/laug gate rejudge g20261018-092540
# (and, beyond the runbook, the gate on fakes rejudged in Go). The first check holds runbook 1's
# block to these lines: each line of it cut at its first "  #", trailing blanks trimmed, is one of them in this order,
# then make test, so a change to the block fails here until this case follows it. README.md's block, under its heading
# "Try it in five minutes, no Docker", is the short walk: the second check holds each of its lines to be one of runbook
# 1's, comments and all, in runbook 1's order, so a line README adds fails here until runbook 1 has it too (cmd/laug's
# runbook test holds the same).
# Each line runs as written but for what keeps it apart from a reader's and a real gate's, which runs this test while it
# holds its two locks and 127.0.0.1:7700. The pool runs from this test's work directory, so it keeps no pid file in the
# checkout, where a reader's pool serve keeps one; the lease goes to a file of this test's (-file), not the checkout's
# .last-lease.json; the gate takes this test's locks (GATE_LOCK and GATE_HOST_LOCK, seams laug passes on) and TMPDIR, where
# -fake makes its dir. Then, when something answers or listens on 127.0.0.1:7700, the pool and the gate run on a free
# port instead (-listen, -pool and GATE_POOL_LISTEN), and a note line says so.
# The rejudge judges in Go and needs no jq, but holds bin/laug's judge to HEAD's with git status: on a tree whose
# internal/judge or internal/jsonx has changes not committed, as make harness-test runs on before such a commit, it runs
# with a git of this test's that says they are clean, and a note says so. deps exits 1 only for a tool the offline path
# calls, so this host's temporal CLI and Docker decide nothing here. Inside a real gate (in_gate, above) make
# build is skipped, and the block's gate takes -allow-stale: the bin/laug there is the one that gate runs, whatever it
# was built from, and make build would relink it as <HEAD>-dirty, the gate's run directory being a change git sees.
rb1="$(awk '/^```sh$/ { s = 1; next } s == 1 && /^```$/ { exit } s == 1' docs/runbooks/1-offline.md)"
eq "docs/runbooks/1-offline.md: one command block, the seven lines this case runs, as the comment above gives them, then make test" \
  "$(grep -c '^```sh' docs/runbooks/1-offline.md)|$(sed -e 's/  #.*$//' -e 's/[[:space:]]*$//' <<<"$rb1")" \
  "1|$(printf '%s\n' 'make build; bin/laug deps -offline' 'bin/laug pool serve & sleep 2' \
    'bin/laug lease; bin/laug pool status; bin/laug release -last' 'kill $!; wait $!' \
    'bin/laug thresholds -server-tag 1.32.1' 'bin/laug gate run 1.31.0 -fake' 'bin/laug gate rejudge g20261018-092540' \
    'make test')"
rblock="$(awk '/^## Try it in five minutes, no Docker$/ { s = 1; next } s == 1 && /^```sh$/ { s = 2; next } s == 2 && /^```$/ { exit }
  s == 2' README.md)"
# notin prints each line of its first argument that its second has no match for at or after the second's line that
# matched the one before it: nothing when the first's lines are all the second's, in the second's order.
notin() { local l j=0 k long; mapfile -t long <<<"$2"; while IFS= read -r l; do
    k=$j; while (( k < ${#long[@]} )) && [[ "${long[k]}" != "$l" ]]; do k=$(( k + 1 )); done
    if (( k < ${#long[@]} )); then j=$(( k + 1 )); else printf '%s\n' "$l"; fi; done <<<"$1"; }
eq "README.md's block under Try it in five minutes, no Docker: at least one line, each a line of runbook 1's block, comments and all, in its order" \
  "$(( ${#rblock} > 0 ))|$(notin "$rblock" "$rb1")" "1|"
RB="$work/readme"; mkdir -p "$RB/tmp" "$RB/run"
RSTALE=()
if [[ -z "$in_gate" ]]; then
  out="$(env -u MAKEFLAGS make -s --no-print-directory build 2>&1)"; rc=$?
  eq "make build: exit 0, bin/laug stamped with HEAD's commit" "$rc|$(grep -c "^go build -ldflags '-X main.commit=$(git rev-parse HEAD)" <<<"$out")" "0|1"
  eq "make build: bin/laug and bin/obs, the stack's, no other name linked to either" "$(tail -1 <<<"$out")|$(grep -c 'ln -s' < <(env -u MAKEFLAGS make -n --no-print-directory build 2>/dev/null))" \
    "laug · make build: bin/laug, bin/obs|0"
else
  note "a gate holds $gate_lock: make build is skipped, and the block's gate takes -allow-stale"
  RSTALE=(-allow-stale)
  eq "make build, skipped inside a gate: bin/laug is there" "$([[ -x bin/laug ]] && echo there)" "there"
fi
RGIT=()
if [[ -n "$(git status --porcelain -- internal/judge internal/jsonx 2>&1)" ]]; then
  mkdir -p "$RB/git"
  printf '%s\n' '#!/usr/bin/env bash' '[[ "$*" == "status --porcelain -- internal/judge internal/jsonx" ]] && exit 0' \
    "exec '$REAL_GIT' \"\$@\"" > "$RB/git/git"
  chmod +x "$RB/git/git"; RGIT=(PATH="$RB/git:$PATH")
  note "internal/judge or internal/jsonx has changes not committed: the rejudge runs with this test's git, which says they are clean"
fi
out="$(bin/laug deps -offline 2>&1)"; rc=$?
eq "bin/laug deps -offline: exit 0, none of the offline path's 12 missing, bin/laug built from HEAD" \
  "$rc|$(head -1 <<<"$out" | grep -cE "^laug · deps -offline: 0 of 12 missing: ")|$(grep -cE '^  (ok|note) +bin/laug +[0-9a-f]{7}' <<<"$out")" "0|1|1"
RP=7700; RPOOL=(); RLISTEN=(); RGATE=()
# As gate/run.sh's preflight asks: curl got any answer, or something listens and did not answer in 1 s.
rc=0; curl -s -o /dev/null -m 1 http://127.0.0.1:7700/healthz || rc=$?
if (( rc != 7 && rc != 28 )) || grep -qE "[.:]7700[[:space:]]" <<<"$(ss -ltn 2>/dev/null)"; then
  RP=""; for p in $(seq 17890 17989); do grep -qE "[.:]$p[[:space:]]" <<<"$(ss -ltn 2>/dev/null)" || { RP="$p"; break; }; done
  RPOOL=(-pool "127.0.0.1:$RP"); RLISTEN=(-listen "127.0.0.1:$RP"); RGATE=(GATE_POOL_LISTEN="127.0.0.1:$RP")
  note "loopback:7700 is taken (a gate's own pool while a gate runs this test, or a pool of the reader's): the block's pool and gate run on 127.0.0.1:$RP"
fi
( cd "$RB/run" && exec "$root/bin/laug" pool serve "${RLISTEN[@]}" ) > "$RB/pool.out" 2> "$RB/pool.err" & rpid=$!
for _ in $(seq 1 50); do curl -sf -m 1 -o /dev/null "http://127.0.0.1:$RP/healthz" && break; "$REAL_SLEEP" 0.1; done
eq "bin/laug pool serve &: a pool of fake cells on 127.0.0.1:$RP, its first line says so, no pid file in the checkout" \
  "$(head -1 "$RB/pool.out" | grep -c "^laug · pool serve: listening on 127.0.0.1:$RP (.*backend fake); not in a laug checkout, so no pid file;")|$([[ -e cells/run/pool.pid ]] && grep -qx "$rpid" cells/run/pool.pid && echo ours)" \
  "1|"
out="$(bin/laug lease "${RPOOL[@]}" -file "$RB/lease.json" 2>&1)"; rc=$?
lid="$(jq -r '.id // empty' "$RB/lease.json" 2>/dev/null)"
eq "bin/laug lease: exit 0, a cell for laug, kept in the file" "$rc|$(head -1 <<<"$out" | grep -c "^laug · lease: $lid for laug until ")" "0|1"
out="$(bin/laug pool status "${RPOOL[@]}" 2>&1)"; rc=$?
eq "bin/laug pool status: exit 0, the lease's cell leased to laug" "$rc|$(grep -cE "^$lid +[0-9]+ +leased +- +laug " <<<"$out")" "0|1"
out="$(bin/laug release -last "${RPOOL[@]}" -file "$RB/lease.json" 2>&1)"; rc=$?
eq "bin/laug release -last: exit 0, the cell given back, the file removed" "$rc|$(grep -c "^laug · release: $lid " <<<"$out")|$([[ -e "$RB/lease.json" ]] && echo kept)" "0|1|"
t0="$(date +%s%N)"; kill "$rpid"; wait "$rpid"; rc=$?; ms=$(( ($(date +%s%N) - t0) / 1000000 ))
eq "kill \$!; wait \$!: the pool stops on TERM, exit 0, in under 2 s, the port free" \
  "$rc|$(( ms < 2000 ))|$(ss -ltn 2>/dev/null | grep -cE "[.:]$RP[[:space:]]")" "0|1|0"
out="$(bin/laug thresholds -server-tag 1.32.1 2>&1)"; rc=$?
eq "bin/laug thresholds -server-tag 1.32.1: exit 0, the note names both files" \
  "$rc|$(head -1 <<<"$out" | grep -c '^laug · thresholds: .*load/releases/1.32.yaml')" "0|1"
before="$(ls gate/results | wc -l)"
out="$(env GATE_LOCK="$RB/gate.lock" GATE_HOST_LOCK="$RB/gate-host.lock" TMPDIR="$RB/tmp" "${RGATE[@]}" bin/laug gate run 1.31.0 -fake "${RSTALE[@]}" 2>"$RB/gate.err")"; rc=$?
rgr="$(ls -d "$RB"/tmp/laug-fake-gate-*/results/g* 2>/dev/null)"
eq "bin/laug gate run 1.31.0 -fake: ADVANCE, exit 0; its first line names its dir in TMPDIR, and its last; the VERDICT line says no cell ran" \
  "$rc|$(head -1 <<<"$out" | grep -c "^laug · gate run 1.31.0 -fake: fakes stand in for Docker, the cells and Omes; the run in $(shown "$RB/tmp")/laug-fake-gate-")|$(grep -c '^VERDICT: ADVANCE (GATE_FAKE=1: fixtures, no cell ran)$' <<<"$out")|$(tail -2 <<<"$out" | head -1)|$(tail -1 <<<"$out" | grep -c "^laug · gate ${rgr##*/}: on fakes, its files are in .*/$(basename "$(dirname "$(dirname "$rgr")")")\$")" \
  "0|1|1|ADVANCE|1"
eq "the gate on fakes: one run in its dir under TMPDIR, verdict.json ADVANCE and fake, an empty Omes; none in gate/results" \
  "$(wc -l <<<"$rgr")|$(jq -c '[.verdict, .fake]' "$rgr/verdict.json" 2>&1)|$(find "$RB"/tmp/laug-fake-gate-*/omes -mindepth 1 | sed "s|^$RB/tmp/laug-fake-gate-[^/]*/||" | paste -sd ' ')|$(ls gate/results | wc -l)" \
  "1|[\"ADVANCE\",true]|omes/cmd omes/cmd/omes|$before"
hasnt "the gate on fakes: no stale-binary refusal" "$(cat "$RB/gate.err")" "-allow-stale runs the gate anyway"
# Beyond the README's lines: the gate on fakes was judged by bin/laug judge, so its gate.json names internal/judge v3,
# and it rejudges identical in Go, with this test's git where the note above says so.
rp="$PATH"; (( ${#RGIT[@]} == 0 )) || rp="$RB/git:$rp"
out="$(env TMPDIR="$RB/tmp" PATH="$rp" bin/laug gate rejudge "$rgr" 2>&1)"; rc=$?
eq "bin/laug gate rejudge <the gate on fakes>: its gate.json names internal/judge v3; identical in Go, exit 0" \
  "$(jq -c '.inputs[-1] | [.path, .version]' "$rgr/gate.json" 2>&1)|$rc|$(sed -n '2,4p' <<<"$out" | cut -c1-35 | paste -sd '|')" \
  "[\"internal/judge\",\"v3\"]|0|verdict.json  identical|verdict.md    identical|judge         v3: gate.json records"
out="$(env TMPDIR="$RB/tmp" "${RGIT[@]}" bin/laug gate rejudge g20261018-092540 2>&1)"; rc=$?
eq "bin/laug gate rejudge g20261018-092540: exit 0, the 1.32.1 BLOCK judged again, both files byte for byte" \
  "$rc|$(sed -n '2,3p' <<<"$out" | paste -sd '|')|$(jq -r .verdict gate/results/g20261018-092540/verdict.json)" \
  "0|verdict.json  identical|verdict.md    identical|BLOCK"

section "the runbooks, by bin/laug runbook check: each block that needs no cell and no lab host"
# docs/runbooks holds a page a task. Runbook 1 is the long walk, which the case above runs line by line, its last
# line, make test, being this test; the others run here, each through bin/laug runbook check, which runs their offline
# blocks from the checkout's root and holds each to its exit and its expected lines. They run with this test's locks,
# TMPDIR, port and git, as the README's gate and rejudges do (RGATE, RGIT above): runbook 4 runs a gate on
# fakes, and runbooks 5 and 7 rejudge every committed run. Inside a gate they do not run: that gate holds the lock and
# drives a bin/laug it checked, and a block's gate on fakes takes no -allow-stale, which the README's takes there.
if [[ -n "$in_gate" ]]; then
  note "a gate holds $gate_lock: the runbooks' blocks run outside a gate, so none runs here"
else
  for rbk in docs/runbooks/*.md; do
    [[ "$rbk" != docs/runbooks/1-* ]] || continue
    out="$(env GATE_LOCK="$RB/gate.lock" GATE_HOST_LOCK="$RB/gate-host.lock" TMPDIR="$RB/tmp" PATH="$rp" "${RGATE[@]}" \
      bin/laug runbook check "$rbk" 2>&1)"; rc=$?
    held_re='^laug · runbook check: ([0-9]+) of ([0-9]+) blocks held'
    if (( rc == 0 )) && [[ "$(tail -1 <<<"$out")" =~ $held_re ]] && [[ "${BASH_REMATCH[1]}" == "${BASH_REMATCH[2]}" ]] \
      && (( BASH_REMATCH[1] > 0 )); then
      pass "bin/laug runbook check $rbk: exit 0, ${BASH_REMATCH[1]} offline block(s) held"
    else
      mapfile -t why < <(grep -v '^ok ' <<<"$out" | head -40)
      fail "bin/laug runbook check $rbk: exit $rc, want 0 with every offline block held" "${why[@]}"
    fi
  done
fi

section "laug pool bench: cells/pool-bench.sh against a pool of fake cells"
# Each case serves a pool of its own, bin/laug pool serve on 127.0.0.1:0, whose first line names the port it got, with
# the gate's fake backend (a cell is a file, a create takes 0.3 s), and runs laug pool bench against it. The bench reads
# the room for phase 3's cold lease once the pool is warm, before its first lease, so a -max without room ends it
# there, exit 2. A refused lease ends it with the pool's answer, exit 1, once its EXIT trap has released what it holds.
# A pool with room gets the table, exit 0. After each case the pool holds no lease.
bw="$work/bench"; mkdir -p "$bw"
# A backend whose creates fail from the third on. It reads the fake's own count, which only a create that reaches the
# fake moves, so the drain lease's refill and the cold lease, which start together, both fail, in either order.
printf '%s\n' '#!/usr/bin/env bash' \
  '[[ $1 != create || $(cat "$FAKE_GATE_DIR/creates.n" 2>/dev/null || echo 0) -lt 2 ]] || { echo "create fails" >&2; exit 1; }' \
  "exec '$td/gate/fake-backend.sh' \"\$@\"" > "$bw/third-fails.sh"
chmod +x "$bw/third-fails.sh"
bup() { # bup <case> <backend> <pool serve flags...>: the pool's address in bp, its pid in bpid, its files in bd
  bd="$bw/$1"; local be="$2"; shift 2; mkdir -p "$bd/fake"; bp=""
  ( exec env FAKE_GATE_DIR="$bd/fake" FAKE_GATE_CREATE_S=0.3 bin/laug pool serve -pid-file none -listen 127.0.0.1:0 \
      -backend "$be" "$@" ) > "$bd/pool.out" 2> "$bd/pool.err" & bpid=$!
  for _ in $(seq 1 50); do
    bp="$(sed -n 's/^laug · pool serve: listening on \(127\.0\.0\.1:[0-9]*\) .*/\1/p' "$bd/pool.out")"
    [[ -z "$bp" ]] || return 0; "$REAL_SLEEP" 0.1
  done
  fail "bin/laug pool serve for case $1: no listening line in 5 s" "$(tail -3 "$bd/pool.err")"
}
bbench() { # bbench <bench flags...>: laug pool bench against bp; its stdout in bout, its stderr in berr, its exit in brc
  bout="$(bin/laug pool bench "$bp" "$@" 2> "$bd/bench.err")"; brc=$?; berr="$(cat "$bd/bench.err")"
}
bstates() { curl -sf "http://$bp/status" | jq -c '[(.cells // [])[] | .state]' 2>&1; }
bdown() { kill "$bpid" 2>/dev/null; wait "$bpid" 2>/dev/null; }
# a. Min 1 and max 2, the Firecracker defaults: the cold lease would be the third cell. No -cycles, so 20 cycles would
# come first; the bench says so before any of them.
bup a "$td/gate/fake-backend.sh" -min 1 -max 2
bbench
eq "laug pool bench, min 1 and max 2: exit 2 before any lease cycle, one line with the numbers and the -max that fits" \
  "$brc|$(grep -c '^  cycle ' <<<"$bout")|$berr" \
  "2|0|laug · cells/pool-bench.sh: phase 3's cold lease needs 3 cells and the pool's max is 2: the 1 warm cell it holds, 1 the pool builds to refill min 1, and the cold one; serve the pool again with the flags it has and -max 3 or more, then run the bench again"
eq "laug pool bench, min 1 and max 2: nothing leased, the pool's one warm cell its only create" \
  "$(bstates)|$(cat "$bd/fake/creates.n" 2>&1)" '["warm"]|1'
bdown
# b. Min 1 and max 3, creates failing from the third on: one cycle, then the cold lease's create fails and the pool
# answers 502, a backend failure. The bench prints that answer, releases the drain lease and exits 1.
bup b "$bw/third-fails.sh" -min 1 -max 3
bbench -cycles 1 -ttl-short 2s
rid="$(sed -n 's/^laug · cells\/pool-bench\.sh: exit 1: releasing the 1 lease it still holds: \([0-9a-f]*\)$/\1/p' <<<"$berr")"
eq "laug pool bench, the cold lease refused: exit 1 after 1 cycle, the pool's answer, the drain lease released, 3 lines" \
  "$brc|$(grep -c '^  cycle ' <<<"$bout")|$(grep -c '^laug · cells/pool-bench\.sh: the pool refused lease cold: HTTP 502 {"error":"backend create [0-9a-f]*: exit status 1"}$' <<<"$berr")|${rid:+held}|$(grep -cxF "laug · cells/pool-bench.sh: released $rid" <<<"$berr")|$(wc -l <<<"$berr")" \
  "1|1|1|held|1|3"
eq "laug pool bench, the cold lease refused: the pool holds no lease after" "$(bstates)" '[]'
bdown
# c. Min 3 and max 6, laug pool serve's defaults, with a 1 s reap for the 2 s TTL: the table, and a cold lease that
# took a create, 0.3 s or more, not a warm cell. The first drain lease starts the pool's refill, and a refill that ends
# before the cold lease asks gives it a warm cell: so here a create made while the bench holds a drain lease waits, 10 s
# at most, until the pool lists the cold lease's cell, which the pool lists before it builds it (internal/pool, Lease).
# The cold lease then always meets a pool with no warm cell, however slowly the bench's drain leases go.
cat > "$bw/cold-waits.sh" <<EOF
#!/usr/bin/env bash
if [[ \$1 == create ]]; then
  addr="\$(sed -n 's/^laug · pool serve: listening on \(127\.0\.0\.1:[0-9]*\) .*/\1/p' "\$FAKE_GATE_DIR/../pool.out")"
  held() { curl -sf "http://\$addr/status" | jq -e --arg r "\$1" 'any(.cells[]?; (.requester // "") | startswith(\$r))' >/dev/null; }
  if held drain-; then
    for _ in \$(seq 1 100); do held cold && break; "$REAL_SLEEP" 0.1; done
  fi
fi
exec "$td/gate/fake-backend.sh" "\$@"
EOF
chmod +x "$bw/cold-waits.sh"
bup c "$bw/cold-waits.sh" -min 3 -max 6 -reap-interval 1s
bbench -cycles 2 -ttl-short 2s
eq "laug pool bench, min 3 and max 6: exit 0, nothing on stderr, 2 cycles, the table's rows, a cold lease from a create" \
  "$brc|$berr|$(grep -c '^  cycle ' <<<"$bout")|$(grep '^| ' <<<"$bout" | cut -d'|' -f2 | sed 's/^ //; s/ $//' | paste -sd ';')|$(awk -F'|' '/^\| cold lease/ { print ($3 + 0 >= 0.3) }' <<<"$bout")" \
  "0||2|What;---;laug pool serve to 3 warm (serial replenish);lease from warm, p50 over 2 cycles;lease from warm, p95;lease from warm, max;cold lease, pool drained (create + health);TTL 2 s: lease to cell gone from /status|1"
eq "laug pool bench, min 3 and max 6: the pool holds no lease after" "$(bstates | jq -c 'map(select(. == "leased"))' 2>&1)" '[]'
bdown

section "committed results, judged again with the lost-work floors"
# committed <glob>...: the files git tracks that each glob names, a * that stops at a slash, a line each. A runbook
# walk leaves its runs beside the committed ones (rb<N>-<UTC time>, which .gitignore keeps out), and they are not
# committed results, so the loops below read this list and not the disk.
committed() { local g; for g in "$@"; do "$REAL_GIT" ls-files -- ":(glob)$g"; done; }
cg="$work/committed"; mkdir -p "$cg/faults/results/deeper" "$cg/load/results"; cp .gitignore "$cg/"
( cd "$cg" && "$REAL_GIT" -c init.defaultBranch=main init -q \
  && touch faults/results/raun-lab9-x.json faults/results/raun-rb3-20261027-120000.json \
    faults/results/deeper/raun-lab9-x.json load/results/lab9-1.baseline.json load/results/rb2-20261027-120000.baseline.json \
  && "$REAL_GIT" add -A )
eq "committed: a walk's raun beside a tracked one, which .gitignore keeps out, is not listed, nor a file a level down" \
  "$(cd "$cg" && committed 'faults/results/*.json' | paste -sd,)" "faults/results/raun-lab9-x.json"
eq "committed: a walk's baseline beside a tracked one is not listed" \
  "$(cd "$cg" && committed 'load/results/*.baseline.json' | paste -sd,)" "load/results/lab9-1.baseline.json"
# Every committed raun ran at RATE 20, the faults/run.sh default: each before window counted 1199 to 1201 in 60 s.
# Committed rauns predate iterations_failed and the sweep, so only the after-window floor can flag them.
printf '\n  %-46s %-8s %9s %7s %6s  %s\n' run committed completed window floor flagged
flagged=()
mapfile -t runs < <(committed 'faults/results/*.json')
for f in "${runs[@]}"; do
  [[ -f "$f" ]] || continue
  [[ "$(basename "$f")" != candidate-* ]] || continue   # a candidate is judged by load/verdict.sh, not by windows
  r="$(jq -c '{after, rate}' "$f")"; res="$(jq -r .result "$f")"
  reasons="$(lw_raun_judge "$r" 20)"; flag=no; [[ -n "$reasons" ]] && flag=yes
  printf '  %-46s %-8s %9s %7s %6s  %s\n' "$(basename "$f" .json)" "$res" "$(jq -r '.after.workflows_completed // "-"' "$f")" \
    "$(jq -r '.after.window_seconds // "-"' "$f")" "$(lw_after_floor 20 "$(jq -r '.after.window_seconds // empty' "$f")")" "$flag"
  if [[ "$flag" == yes ]]; then
    flagged+=("$(basename "$f" .json)")
    [[ "$res" == BLOCK ]] || fail "floor flags $(basename "$f") whose committed result is $res" "$reasons"
  fi
done
# A baseline records readings, not a verdict: its committed result is what those readings give against the committed
# thresholds (any over its max, or missing, is BLOCK). lab131-1 (2026-10-14) is a BLOCK on thresholds alone.
# The thresholds are those of the run's own release (load/thresholds.sh), as laug verdict would judge it.
mapfile -t runs < <(committed 'load/results/*.baseline.json')
for b in "${runs[@]}"; do
  meta="${b%.baseline.json}.json"; [[ -f "$b" && -f "$meta" ]] || continue
  th_json="$(load/thresholds.sh "$(jq -r '.server_version // .server_tag // ""' "$meta")" \
    | yq -o=json -I=0 '[.thresholds[] | select(.max != null) | {"name": .name, "max": .max, "no_data": .no_data}]')"
  done_n="$(jq -r .workflows_completed "$b")"; floor="$(lw_load_floor "$(jq -r '.rate // empty' "$meta")" "$(lw_dur_s "$(jq -r '.duration // empty' "$meta")")")"
  res="$(jq -r --argjson th "$th_json" '. as $r | if any($th[]; ($r[.name] // null) as $v | ($v == null and .no_data != "not_judged") or ($v != null and $v > .max)) then "BLOCK" else "ADVANCE" end' "$b")"
  flag=no; [[ -n "$floor" ]] && (( done_n < floor )) && flag=yes
  printf '  %-46s %-8s %9s %7s %6s  %s\n' "$(basename "$meta" .json)" "$res" "$done_n" "$(jq -r .window_seconds "$b")" "$floor" "$flag"
  if [[ "$flag" == yes ]]; then
    flagged+=("$(basename "$meta" .json)")
    [[ "$res" == BLOCK ]] || fail "load floor flags baseline $(basename "$meta") whose readings pass every threshold"
  fi
done
echo
pass "floors flag ${#flagged[@]} committed run(s), each already BLOCK: ${flagged[*]:-none}"
# A committed candidate result must agree with itself: PASS only when the verdict is the expect (for a BLOCK, with a
# blocks_on row among the deciding), FAIL only when it is not, INVALID only with a reason why.
mapfile -t runs < <(committed 'faults/results/candidate-*.json')
for f in "${runs[@]}"; do
  [[ -e "$f" ]] || continue
  eq "committed $(basename "$f" .json): outcome agrees with verdict, expect and blocks_on" \
    "$(jq -r '(.blocks_on // []) as $b | if .outcome == "INVALID" then (.outcome_reason | length > 0)
      elif .verdict != .expect then .outcome == "FAIL"
      else .outcome == "PASS" and (.expect == "ADVANCE" or any(.deciding[]; . as $d | any($b[]; . == $d))) end' "$f")" true
done

printf '\n%d passed, %d failed\n' "$passed" "$failed"
(( failed == 0 ))
