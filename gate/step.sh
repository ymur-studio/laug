#!/usr/bin/env bash
# Workflow Gate's step driver: gate/step.sh <verb> <dir> [key] runs one step of the gate run in <dir>, the same lines
# gate/run.sh runs at that point, and prints one JSON answer line. The step runner (internal/orchestrator/steps.go)
# starts one step.sh per step, each in a session of its own, in run.sh's order. Each step is a process of its own, so
# what one step starts and a later one stops (the lock holder, laugd, make harness-test, the wave's poller and items) is
# found by its record, and what a later step needs is in .state.json. gate/step.sh check makes <dir>.
#
# The verbs, with the lines of gate/run.sh each runs. Each copied block below is headed "# run.sh:N-M" and is
# sed -n N,Mp gate/run.sh, verbatim unless its heading names an edit. run.sh keeps those lines where they are.
#   check              gate_env and run.sh:122-126: <dir>, with .steps/ and .state.json; check writes no .pid or .done
#   open               run.sh:120 (t0), 127-137 and 192-193; a lock holder in place of 194-200; the worker's commit
#   preflight          run.sh:201-275
#   omes               run.sh:278-282
#   laugd              run.sh:288-297; laugd runs on, detached
#   harness            run.sh:300-301; make harness-test runs on, detached
#   lease <key>        the holder check, then one pass of run.sh:305-327's loop: 306-326
#   barrier            the holder check, then run.sh:328-340
#   quiet <i>          one pass of run.sh:341-350's loop: 342-343 and 348, with no sleep (the workflow sleeps)
#   wave-time          the holder check, then run.sh:351-352
#   wave               run.sh:355-364, and each item's <key>/.pid
#   combine            run.sh:147-152; its step is named judge
#   post [stopped]     run.sh:153-154; with stopped, run.sh:183: no mark, and gate/post.sh only on a verdict
#   release [stopped]  cleanup (gate/lib.sh), then run.sh:181, the holder and run.sh:157; with stopped, no done event
#   stopped <why>      run.sh:178-182 by records: the items get TERM, then KILL; the poller, the wave and the harness
#                      are stopped; the trap verdict, unless the judge wrote a verdict. why is 1 to 300 bytes, one line
# A step's name is the verb, lease-<key>, quiet-<i>, or judge for combine.
#
# The answer is one JSON object on one line, on fd 3, which is stdout on entry; stdout then joins stderr, so what the
# lines print goes to the log. Its result is next, stop-leasing (no more leases), judge (to the judge, not refused),
# refused (gate.json.refused says why; to the judge), done or stopped (a signal), with each verb's data. combine,
# post and stopped carry the gate's own exit code as exit: 0 ADVANCE, 1 BLOCK, 2 when no verdict was written or
# handed on.
# Files, in <dir>: .state.json, what later steps read (t0, the locks, laugd's max_lease, the barrier's limit, and the
# records of the holder, laugd, the harness, the wave and its poller), written only by state_put; .steps/<name>.pid,
# this step's own record, written first and under noclobber; .steps/<name>.done, {exit, answer, why, at}, written by
# the EXIT trap; .steps/holder, the lock holder's status; <key>/.pid, each item's record, {pid, pgid, start, boot_id}.
# The step runner opens .steps/<name>.out, the answer, and .steps/<name>.log, stderr with stdout joined. A .log can
# name <dir> by its absolute path, as bin/laug judge and gate/post.sh print it: .steps/ is never committed
# (.gitignore), and a .log line reaches the workflow's history only through the runner's redact.
# A record is {pid, start, boot_id}, start being field 22 of /proc/<pid>/stat. A recorded pid gets a signal only while
# it is still the process recorded (alive_recorded, gate/lib.sh). An item's pid also names its session, which stopped's
# sweep TERMs unless the pid is another process now (sweep_item, gate/lib.sh).
# Exit 0: answered, in .done and on fd 3. 2 with no .pid: a usage error, gate_env's or gate/post.sh --check's, and
# nothing written. 75: this step's .pid exists already, and nothing written. 143, 130 or 129: a signal, and .done's
# answer is stopped. Any other, or 2 with a .pid: a command failed, and .done's why says which. A step that is sent
# KILL leaves a .pid and no .done.
# Environment: what gate_env reads (gate/run.sh's header lists it) and the fakes' FAKE_GATE_*, which the step runner
# sets from the workflow's input; and GATE_WORKFLOW_ID, GATE_WORKFLOW_RUN_ID and GATE_WORKER_COMMIT, each optional,
# which step.sh copies and unsets before it starts anything, so no child sees them. Nothing else: t0 comes from
# .state.json, the keys and timings from plan.json, a pid from a record.
set -eEuo pipefail
export LC_ALL=C
root="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$root"
exec 3>&1 1>&2   # fd 3 is the answer; stdout joins stderr
. gate/lib.sh; driver=step.sh   # relative, from root: bash's job notice for a KILLed item (watchdog.log) names it so

usage() { why="$*"; echo "laug · gate/step.sh: $*" >&2; exit 2; }
# refuse (gate/lib.sh) ends here, and so does a barrier whose make harness-test did not pass: to the judge.
to_judge() { answer="$(jq -c '{result: (if .refused == null then "judge" else "refused" end), refused}' "$dir/gate.json")"; exit 0; }
# No signal between a detached start and its record: on_term stops neither the holder, laugd nor the harness, and
# without a record release cannot either. A signal that lands meanwhile runs on_term at release_signals. The traps
# hold a command, never '': an ignored signal stays ignored in every child, across fork and exec.
hold_signals() { held=""; trap 'held="TERM 143"' TERM; trap 'held="INT 130"' INT; trap 'held="HUP 129"' HUP; }
release_signals() {
  trap 'on_term TERM 143' TERM; trap 'on_term INT 130' INT; trap 'on_term HUP 129' HUP
  # shellcheck disable=SC2086  # held is "<signal> <exit>"
  [[ -z "$held" ]] || on_term $held
}
on_term() { # on_term <signal> <exit>: what run.sh's EXIT trap does for this step's own children, then the answer stopped
  trap '' TERM INT HUP; set +eE; why="signal $1"   # a second signal is ignored
# run.sh:178-180, verbatim
  [[ -z "$bg_pid" ]] || kill "$bg_pid" 2>/dev/null
  [[ -z "$(live_items)" ]] || { watch_items 0 2>>"$dir/watchdog.log"; sweep_orphans; }
  stop_poller
  answer='{"result":"stopped"}'; exit "$2"
}
on_exit() { # the answer on fd 3 and, but for check, in .steps/<name>.done
  local rc=$? t
  trap '' TERM INT HUP; set +eE
  if (( rc != 0 )); then [[ -n "$why" ]] || why="${err:-exit $rc}"; fi
  [[ -n "$answer" ]] && jq -e 'type == "object"' <<<"$answer" >/dev/null 2>&1 || answer=null
  printf '%s\n' "$answer" >&3
  if [[ "$verb" != check ]]; then
    t="$(mktemp "$dir/.steps/$name.XXXXXX")" \
      && jq -cn --argjson x "$rc" --argjson a "$answer" --arg w "$why" --argjson at "$(stamp)" \
           '{exit: $x, answer: $a, why: (if $w == "" then null else $w end), at: $at}' > "$t" \
      && mv "$t" "$dir/.steps/$name.done"
  fi
  exit "$rc"
}

# The arguments. A usage error writes nothing: no trap is armed yet. Its text names a path as shown gives it, but for
# gate_env's GATE_RESULTS text and check's run.sh:124, which name it as given: the step runner's redact covers both.
verb="${1:-}"; dir="${2:-}"; key="${3:-}"; name=""
synopsis="gate/step.sh check, or gate/step.sh <verb> <dir> [key], verb one of open preflight omes laugd harness lease barrier quiet wave-time wave combine post release stopped"
if [[ "$verb" == check ]]; then
  (( $# == 1 )) || usage "check takes no other argument: $synopsis"
else
  case "$verb" in
    open|preflight|omes|laugd|harness|lease|barrier|quiet|wave-time|wave|combine|post|release|stopped) ;;
    *) usage "no such verb: $synopsis" ;;
  esac
  (( $# == 2 || $# == 3 )) || usage "$verb takes <dir> and at most a key: $synopsis"
  [[ -n "$dir" ]] || usage "$verb takes <dir>: $synopsis"
  [[ "${dir##*/}" =~ ^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$ && -d "$dir/.steps" && -f "$dir/.state.json" ]] \
    || usage "$(shown "$dir") is not a gate run's directory with .steps/ and .state.json; gate/step.sh check makes one"
  case "$verb" in
    lease) [[ "$key" =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]] || usage "lease takes an item's key, a lowercase name of 32 characters at most" ;;
    quiet) [[ "$key" =~ ^(0|[1-9][0-9]{0,3})$ ]] || usage "quiet takes the pass, a whole number from 0 to 9999" ;;
    post|release) [[ -z "$key" || "$key" == stopped ]] || usage "$verb takes no key, or stopped" ;;
    stopped) [[ -n "$key" && ${#key} -le 300 && "$key" != *$'\n'* ]] || usage "stopped takes why the gate stopped, one line of 1 to 300 bytes" ;;
    *) (( $# == 2 )) || usage "$verb takes no key" ;;
  esac
  case "$verb" in lease|quiet) name="$verb-$key" ;; combine) name=judge ;; *) name="$verb" ;; esac
fi

# The state run.sh's EXIT trap reads, which this step's traps and gate/lib.sh read too; then step.sh's own.
# run.sh:140-143, verbatim
combined=0; posted=0; cleaned=0; final=2; why=""; err=""; plan=""; wiring="[]"; keys=()
laugd_pid=""; harness_pid=""; poller_pid=""; bg_pid=""; at_preflight="[]"; harness_start_ms=0; locked=0; max_lease=""
declare -A item_pid=() item_start=() item_end=() item_exit=() item_stopped=()
DEADLINE_S=""; KILL_GRACE_S=30; WAVE_MIN_S=0
answer=""; step=""; held=""; t0=""; t0_ms=""; barrier_limit_ms=""
wf_id="${GATE_WORKFLOW_ID:-}"; wf_run_id="${GATE_WORKFLOW_RUN_ID:-}"; worker_commit="${GATE_WORKER_COMMIT:-}"
unset GATE_WORKFLOW_ID GATE_WORKFLOW_RUN_ID GATE_WORKER_COMMIT

gate_env
if [[ "$verb" != check ]]; then
  [[ "$(realpath -e -- "$(dirname "$dir")" 2>/dev/null)" == "$(realpath -e -- "$GATE_RESULTS" 2>/dev/null)" ]] \
    || usage "$(shown "$dir") is not in GATE_RESULTS, $(shown "$GATE_RESULTS")"
  dir="$(cd "$dir" && pwd -P)"; gate_id="${dir##*/}"
  # A lease's key is one of plan.json's, checked before the .pid, so that this usage error writes nothing either.
  if [[ "$verb" == lease ]]; then
    [[ -s "$dir/plan.json" ]] || usage "lease $key: this gate run has no plan.json yet; preflight writes it"
    jq -e --arg k "$key" 'any(.items[]?; .key == $k)' "$dir/plan.json" >/dev/null 2>&1 || usage "lease $key: plan.json lists no such item"
  fi
  # This step's record, the first file it writes: a second copy of the step exits 75 here and writes nothing.
  mkdir -p "$dir/.steps"
  set -C; { printf '{"pid":%d,"start":%s,"boot_id":"%s"}\n' $$ "$(proc_start $$)" "$(boot_id)" > "$dir/.steps/$name.pid"; } 2>/dev/null || exit 75; set +C
fi
trap on_exit EXIT; trap 'on_term TERM 143' TERM; trap 'on_term INT 130' INT; trap 'on_term HUP 129' HUP; trap 'err="gate/${BASH_SOURCE[0]##*/} line $LINENO: $BASH_COMMAND failed"' ERR

load_state() { # the earlier steps' state: .state.json, plan.json and gate.json's wiring; a key not there keeps its default
  # One jq, its output quoted by @sh for eval. Assigned first: a jq that fails inside eval's argument leaves eval an
  # empty string, status 0, while a failed assignment stops the step here (set -e), this line in .done's why.
  local q s
  q='def s: if . == null then "" elif type == "string" then . else tojson end;
    @sh "t0=\(.t0 | s) t0_ms=\(.t0_ms | s) step=\(.step | s) locked=\(.locked // 0 | s) max_lease=\(.max_lease | s)",
    @sh "at_preflight=\(.at_preflight // [] | s) harness_start_ms=\(.harness_start_ms // 0 | s)",
    @sh "barrier_limit_ms=\(.barrier_limit_ms | s) laugd_pid=\(.laugd.pid | s) harness_pid=\(.harness.pid | s)",
    "reasons=(\([.reasons[]? | @sh] | join(" ")))"'
  s="$(jq -r "$q" "$dir/.state.json")"
  eval "$s"
  wiring="$(jq -c '.wiring // []' "$dir/gate.json" 2>/dev/null)" || wiring="[]"
  [[ -n "$wiring" ]] || wiring="[]"
  [[ -s "$dir/plan.json" ]] || return 0
  plan="$(cat "$dir/plan.json")"
# run.sh:205-208, verbatim
  mapfile -t keys < <(jq -r '.items[].key' <<<"$plan")
  DEADLINE_S="$(jq .deadline_s <<<"$plan")"; KILL_GRACE_S="$(jq .kill_grace_s <<<"$plan")"; LEASE_TTL_S="$(jq .lease_ttl_s <<<"$plan")"
  QUIET_S="$(jq .quiet_s <<<"$plan")"; BARRIER_TIMEOUT_S="$(jq .barrier_timeout_s <<<"$plan")"; POLL_S="$(jq .poll_s <<<"$plan")"
  HARNESS_TIMEOUT_S="$(jq .harness_timeout_s <<<"$plan")"; WAVE_MIN_S="$(jq .wave_min_s <<<"$plan")"
}

verb_check() {
# run.sh:122-126, verbatim
gate_id="g$(date -u +%Y%m%d-%H%M%S)"; dir="$(cd "$GATE_RESULTS" && pwd -P)/$gate_id"; n=1
until mkdir "$dir" 2>/dev/null; do   # two gate runs in one second, as make harness-test makes them
  n=$(( n + 1 )); (( n < 100 )) || usage "no results directory could be made beside $dir"
  gate_id="g$(date -u +%Y%m%d-%H%M%S)-$n"; dir="$(dirname "$dir")/$gate_id"
done
mkdir "$dir/.steps"
t="$(mktemp "$dir/.steps/state.XXXXXX")"
jq -n --arg w "$wf_id" --arg r "$wf_run_id" --arg b "$(boot_id)" \
  '{workflow_id: (if $w == "" then null else $w end), workflow_run_id: (if $r == "" then null else $r end), boot_id: $b, step: null}' > "$t"
mv "$t" "$dir/.state.json"
answer="$(jq -cn --arg id "$gate_id" --arg d "$(shown "$dir")" '{result: "next", gate_id: $id, dir: $d}')"
}

verb_open() {
local hs a b h i
# run.sh:120, verbatim
t0="$(stamp)"; t0_ms="$(sed -E 's/.*"wall":([0-9]+)\.([0-9]{3}).*/\1\2/' <<<"$t0")"
state_put --argjson t "$t0" --argjson ms "$t0_ms" '.t0 = $t | .t0_ms = $ms'
# run.sh:127-137, verbatim but :133 and :136, which add the driver keys
step=start
mark start
printf '{"t0":%s,"t_post":null}\n' "$t0" | jq . > "$dir/clock.json"
jq -n --arg id "$gate_id" --arg tag "$SERVER_TAG" --arg commit "$(git rev-parse --short HEAD)" \
  --argjson dirty "$([[ -n "$(git status --porcelain -- . ':(exclude)gate/results')" ]] && echo true || echo false)" \
  --arg base "$base_commit" --arg base_ref "$BASE" --arg plan "$(shown "$PLAN")" --arg items "$ITEMS" --argjson fake "$([[ "$fake" == 1 ]] && echo true || echo false)" \
  --arg pool "$GATE_POOL_LISTEN" --arg wf "$wf_id" --arg wc "$worker_commit" \
  '{gate_id: $id, server_tag: $tag, laug_commit: $commit, tree_dirty: $dirty, base: $base, base_ref: $base_ref, inputs: null,
    host: null, refused: null, plan: $plan, items_limit: (if $items == "" then null else ($items | tonumber) end), fake: $fake,
    pool: $pool, driver: "step.sh", workflow_id: (if $wf == "" then null else $wf end), worker_commit: (if $wc == "" then null else $wc end)}' > "$dir/gate.json"
echo "laug · gate $gate_id: server $SERVER_TAG, plan $(shown "$PLAN")${ITEMS:+ (its first $ITEMS)}, results $(shown "$dir")$([[ "$fake" != 1 ]] || echo ", GATE_FAKE=1: no Docker, cell or Omes")${base_note:+; $base_note}"
# run.sh:192-193, verbatim
reasons=()
mark lock
# In place of run.sh:194-200, a holder: a process of its own that takes both locks and keeps them, as sleep keeps
# fds 8 and 9 through exec, until release stops it, or for 2400 s. It writes its status and exits 75 unless it holds
# both. It is found by its record and by its locks (flock -n <file> true exits 1 while it holds one): after its exec,
# its command line is sleep 2400.
hold_signals
# shellcheck disable=SC2016  # expanded by the holder's own bash
setsid bash -c 'mkdir -p "$(dirname "$1")" && exec 9>>"$1" || exit 1; a=held; flock -n 9 || a=busy
  if { exec 8>>"$2"; } 2>/dev/null; then b=held; flock -n 8 || b=busy; else b=unopenable; fi
  echo "$a $b" > "$3"; [[ "$a $b" == "held held" ]] || exit 75; exec sleep 2400' gate-holder \
  "$GATE_LOCK" "$GATE_HOST_LOCK" "$dir/.steps/holder" </dev/null >/dev/null 2>&1 3>&- &
state_rec holder $!
release_signals
# Its status, 5 s at most: -s, since the file exists before echo writes into it.
hs=""
for i in $(seq 1 100); do
  [[ ! -s "$dir/.steps/holder" ]] || hs="$(cat "$dir/.steps/holder")"
  if [[ "$hs" =~ ^(held|busy)\ (held|busy|unopenable)$ ]]; then break; fi
  hs=""; sleep 0.05
done
[[ -n "$hs" ]] || { why="the lock holder wrote no status"; exit 1; }
a="${hs% *}"; b="${hs#* }"; locked=0; [[ "$hs" != "held held" ]] || locked=1
# run.sh:197-200's reasons, which wait for preflight's refuse, as run.sh's do
[[ "$a" == held ]] || reasons+=("another gate run holds $(shown "$GATE_LOCK"): wait for it to end")
[[ "$b" != busy ]] || reasons+=("another gate run on this host holds $(shown "$GATE_HOST_LOCK"): wait for it to end")
[[ "$b" != unopenable ]] || reasons+=("the host lock $(shown "$GATE_HOST_LOCK") cannot be opened (GATE_HOST_LOCK)")
# GATE_WORKER_COMMIT is the worker identity's <7 hex>[-dirty] (internal/orchestrator/queue.go takes rev[:7]), so 7
# characters here: git rev-parse --short=7 prints more when 7 are ambiguous (git help rev-parse, --short).
if [[ -n "$worker_commit" ]]; then
  h="$(git rev-parse HEAD | cut -c1-7)"
  [[ "${worker_commit%%-*}" == "$h" ]] || reasons+=("the worker runs commit $worker_commit, and this checkout's HEAD is $h: make build, then restart ${LAUG:-bin/laug} orchestrator worker")
fi
state_put --argjson l "$locked" --argjson r "$(printf '%s\n' "${reasons[@]+"${reasons[@]}"}" | jq -Rcs 'split("\n") | map(select(. != ""))')" \
  '.locked = $l | .reasons = $r'
answer="$(jq -cn --argjson t "$t0" --argjson ms "$t0_ms" --arg b "$(boot_id)" --argjson l "$locked" \
  '{result: "next", t0: $t, t0_ms: $ms, boot_id: $b, locked: ($l == 1)}')"
}
# shellcheck disable=SC2317  # called by name, "verb_${verb//-/_}" below, which shellcheck does not follow
verb_preflight() {
# run.sh:201-241, verbatim
mark plan
plan_err="$(mktemp)"
if plan="$(plan_read "$PLAN" "$ITEMS" 2>"$plan_err")"; then
  printf '%s\n' "$plan" > "$dir/plan.json"
  mapfile -t keys < <(jq -r '.items[].key' <<<"$plan")
  DEADLINE_S="$(jq .deadline_s <<<"$plan")"; KILL_GRACE_S="$(jq .kill_grace_s <<<"$plan")"; LEASE_TTL_S="$(jq .lease_ttl_s <<<"$plan")"
  QUIET_S="$(jq .quiet_s <<<"$plan")"; BARRIER_TIMEOUT_S="$(jq .barrier_timeout_s <<<"$plan")"; POLL_S="$(jq .poll_s <<<"$plan")"
  HARNESS_TIMEOUT_S="$(jq .harness_timeout_s <<<"$plan")"; WAVE_MIN_S="$(jq .wave_min_s <<<"$plan")"
  n_in_plan="$(jq .items_in_plan <<<"$plan")"
  (( ${#keys[@]} == n_in_plan )) || say plan "ITEMS=$ITEMS runs ${#keys[@]} of the plan's $n_in_plan items: R2 BLOCKs a cut plan, whose items' readings are what the noise runs (gate/plans/noise.yaml) read"
  # Per item, everything that must not be shared: lease file, requester, RUN_ID, task queue (faults/run.sh,
  # omes-<RUN_ID>), results directory and Omes port pair. They are distinct by construction; checked all the same.
  wiring="$(jq -c --arg id "$gate_id" --arg d "$(shown "$dir")" '[.items | to_entries[] | .key as $i | .value.key as $k
    | {key: $k, run_id: "\($id)-\($k)", requester: "gate-\($id)-\($k)", results_dir: "\($d)/\($k)", lease_file: "\($d)/\($k)/.lease.json",
       task_queue: "omes-\($id)-\($k)", omes_metrics: {worker: "127.0.0.1:\(19100 + 2 * $i)", client: "127.0.0.1:\(19101 + 2 * $i)"}}]' <<<"$plan")"
  dup="$(jq -r '. as $w | ["run_id", "requester", "results_dir", "lease_file", "task_queue"] as $f
    | ($f[] | select(([$w[][.]] | unique | length) < ($w | length))), (if ([$w[].omes_metrics[]] | unique | length) < 2 * ($w | length) then "omes_metrics" else empty end)' <<<"$wiring")"
  [[ -z "$dup" ]] || reasons+=("the wiring repeats $(paste -sd ' ' <<<"$dup") across items")
else
  plan=""
  while IFS= read -r l; do reasons+=("plan $(shown "$PLAN"): $l"); done < "$plan_err"
fi
rm -f "$plan_err"

mark preflight
# Something answers (curl got any reply), or something listens and did not answer in 1 s. A port nothing listens on is
# refused at once on the dev host (curl exit 7); a sandbox that drops loopback connections times out instead (exit 28).
rc=0; curl -s -o /dev/null -m 1 "$pool_url/healthz" || rc=$?; command -v ss >/dev/null || reasons+=("no ss on PATH (iproute2): the preflight reads the listening ports with it, the pool's port and each item's Omes metrics port, and without it a taken port would read as free")
# Without a lock, what is up is most likely the gate run that holds it, and stopping its laugd would end it mid-wave:
# the advice is the lock's alone, Ctrl-C or kill for a pool serve, not laug pool down, which removes every pool cell.
if (( rc != 7 && rc != 28 )) || grep -qE "[.:]${GATE_POOL_LISTEN##*:}[[:space:]]" <<<"$(ss -ltn 2>/dev/null)"; then   # not ss | grep -q, here or below: under pipefail, ss's 141 when grep quits first reads a taken port as free
  if (( locked )); then reasons+=("something answers on $GATE_POOL_LISTEN (curl exit $rc), a laugd from laug pool serve or another user's gate; a pool serve stops with Ctrl-C in its terminal, or kill %1 when this shell started it with &")
  else reasons+=("something answers on $GATE_POOL_LISTEN (curl exit $rc), most likely the laugd of the gate run that holds the lock"); fi
fi
if up="$(projects_up 2>&1)"; then
  at_preflight="$(shown_projects <<<"$up" | jq -Rcs 'split("\n") | map(select(. != ""))')"
  if [[ -n "$up" ]]; then
    if (( locked )); then reasons+=("compose project(s) up on this host: $(projects_text "$up"); ${LAUG:-bin/laug} pool down -remove-cells removes laug-* ones")
    else reasons+=("compose project(s) up on this host: $(projects_text "$up"), most likely the cells of the gate run that holds the lock"); fi
  fi
else reasons+=("docker compose ls failed: $up"); fi
# release reads it (cleanup.json's left), also after the refusal at run.sh:274, below.
state_put --argjson a "$at_preflight" '.at_preflight = $a'
# run.sh:242-275, verbatim
if th="$(load/thresholds.sh "$SERVER_TAG" 2>&1)"; then yq -o=json '.' <<<"$th" > "$dir/thresholds.json"
else reasons+=("load/thresholds.sh does not resolve $SERVER_TAG: $(tail -1 <<<"$th")"); fi
# Every image the cell names at SERVER_TAG, listed by compose itself (8 at 1.31.0 and at 1.32.1, seen).
if images="$(SERVER_TAG="$SERVER_TAG" timeout 30 docker compose -p laug-gate-preflight --project-directory cells/compose \
     -f cells/compose/cell.yml config --images 2>"$dir/.images.err" | sort -u)" && [[ -n "$images" ]]; then
  missing=(); while IFS= read -r img; do docker image inspect "$img" >/dev/null 2>&1 || missing+=("$img"); done <<<"$images"
  (( ${#missing[@]} == 0 )) || reasons+=("not pulled on this host: ${missing[*]} (the cell's 8 images at 1.31.0 saved to a 778238976-byte tar: cells/results/2026-10-23-boot1.txt, A1)")
else reasons+=("docker compose config --images on cells/compose/cell.yml failed: $(tail -1 "$dir/.images.err")"); fi
rm -f "$dir/.images.err"; incumbent_reasons
[[ -d "$OMES_DIR/cmd/omes" ]] || reasons+=("no Omes at $(shown "$OMES_DIR"); ${LAUG:-bin/laug} upstream clones it")
vcpu="$(nproc 2>/dev/null)" || vcpu=0; read -r mem_b avail_b < <(free -b 2>/dev/null | awk '$1 == "Mem:" { print $2, $7 }') || true
[[ "$vcpu" =~ ^[0-9]+$ ]] || vcpu=0; [[ "${mem_b:-}" =~ ^[0-9]+$ ]] || mem_b=0; [[ "${avail_b:-}" =~ ^[0-9]+$ ]] || avail_b=0
(( vcpu >= 16 )) || reasons+=("this host has $vcpu vCPU, under the 16 a gate needs")
(( avail_b >= 8 * 1073741824 )) || reasons+=("this host has $(awk -v b="$avail_b" 'BEGIN { printf "%.1f", b / 1073741824 }') GiB available, under the 8 GiB a gate needs"); host_reasons
jput "$dir/gate.json" --argjson v "$vcpu" --argjson m "$mem_b" --argjson a "$avail_b" --argjson t "$host_type" \
  '.host = {type: $t, vcpu: $v, mem_gib: ($m / 1073741824 | floor), mem_available_gib: (($a / 1073741824 * 10 | floor) / 10)}'
for port in $(jq -r '.[].omes_metrics[] | sub(".*:"; "")' <<<"$wiring"); do
  ! grep -qE "[.:]$port[[:space:]]" <<<"$(ss -ltn 2>/dev/null)" || reasons+=("port $port, an item's Omes metrics port, is taken")
done
[[ "$fake" != 1 || -x bin/laug ]] || reasons+=("bin/laug is not built (make build builds it; the gate's own pool runs it)")
"${sigdefault[@]}" true 2>/dev/null || reasons+=("env has no --default-signal, which the gate starts its harness and items with (GNU coreutils 8.31 or later)")
[[ "$fake" == 1 || -x bin/laug ]] || reasons+=("bin/laug is not built (make build builds it; the gate's own pool runs it, and cells/backend-compose.sh its health check)")
inputs=()
for f in "$PLAN" load/steady-state.yaml load/thresholds.sh faults/catalog.yaml cells/compose/dynamicconfig/laug.yaml cells/compose/cell.yml load/releases/*.yaml; do
  [[ -e "$f" ]] || continue
  p="$(shown "$f")"; sha="$(sha256sum < "$f" | cut -d' ' -f1)"; changed=true
  if [[ "$p" != "(outside"* ]] && git cat-file -e "$base_commit:$p" 2>/dev/null; then
    [[ "$(git show "$base_commit:$p" | sha256sum | cut -d' ' -f1)" != "$sha" ]] || changed=false
  fi
  inputs+=("$(jq -cn --arg p "$p" --arg s "$sha" --argjson c "$changed" '{path: $p, sha256: $s, changed_vs_base: $c}')")
done; judge_input   # the judge last: bin/laug judge -identity's entry, or a reason (gate/lib.sh)
jput "$dir/gate.json" --argjson in "$(printf '%s\n' "${inputs[@]}" | jq -cs .)" --argjson w "$wiring" '.inputs = $in | .wiring = $w'
(( ${#reasons[@]} == 0 )) || refuse "${reasons[@]}"
say preflight "ok: $vcpu vCPU, $(jq -r '.host.mem_available_gib' "$dir/gate.json") GiB available, $(wc -l <<<"$images") images local, no compose project up$( [[ "$fake" != 1 ]] || echo " (GATE_FAKE=1: the fixtures', not this host's)"), nothing on $GATE_POOL_LISTEN; ${#keys[@]} item(s): ${keys[*]}"
answer="$(jq -c '{result: "next", keys: [.items[].key], deadline_s, kill_grace_s, lease_ttl_s, quiet_s, barrier_timeout_s, poll_s,
  harness_timeout_s, wave_min_s}' "$dir/plan.json")"
}

verb_omes() {
# run.sh:278-282, verbatim
mark omes_build; t="$(now_ms)"; rc=0
( cd "$OMES_DIR" && exec timeout 300 go build -o /dev/null ./cmd/omes ) > "$dir/omes-build.log" 2>&1 8>&- 9>&- & bg_pid=$!
wait "$bg_pid" || rc=$?; bg_pid=""
(( rc == 0 )) || refuse "go build ./cmd/omes in $(shown "$OMES_DIR") exited $rc$( (( rc != 124 )) || echo " after 300 s; a first build belongs before the gate"); see omes-build.log"
say omes "go build ./cmd/omes in $(secs_since "$t") s"
answer='{"result":"next"}'
}

verb_laugd() {
# run.sh:288-290, verbatim
mark laugd
nonce=$(( (RANDOM * 32768 + RANDOM) % 1000000 )); (( nonce % 10 )) || nonce=$(( nonce + 1 ))
max_lease="$(printf '2h0m0.%06ds' "$nonce")"
# own_status reads max_lease (gate/lib.sh), so release has it after a refusal or a stop; and no signal before the record
state_put --arg m "$max_lease" '.max_lease = $m'
hold_signals
# run.sh:291-293, verbatim
TZ=UTC setsid bin/laug pool serve -pid-file none -listen "$GATE_POOL_LISTEN" -min 0 -max "${#keys[@]}" -quota "${#keys[@]}" \
  -max-lease "$max_lease" -server-tag "$SERVER_TAG" -backend "$backend" > "$dir/laugd.log" 2>&1 8>&- 9>&- &
laugd_pid=$!
state_rec laugd "$laugd_pid"; release_signals
# run.sh:294-297, verbatim
for _ in $(seq 1 100); do curl -sf -m 1 -o /dev/null "$pool_url/healthz" && break; kill -0 "$laugd_pid" 2>/dev/null || break; sleep 0.1; done
curl -sf -m 1 -o /dev/null "$pool_url/healthz" || refuse "laugd did not answer on $GATE_POOL_LISTEN within 10 s: $(tail -1 "$dir/laugd.log")"
w="$(own_laugd_why)"; [[ -z "$w" ]] || refuse "the laugd on $GATE_POOL_LISTEN is not the gate's own: $w"
say laugd "$GATE_POOL_LISTEN, min 0, max ${#keys[@]}, quota ${#keys[@]}, max lease $max_lease, backend $(if [[ -z "$backend" ]]; then echo "laugd's own fake"; else shown "$backend"; fi)"
answer="$(jq -cn --arg m "$max_lease" '{result: "next", max_lease: $m}')"
}

verb_harness() {
# run.sh:300, with the harness's record and start between hold_signals and release_signals
mark harness; hold_signals; start_harness; state_rec harness "$harness_pid"
state_put --argjson s "$harness_start_ms" '.harness_start_ms = $s'; release_signals
# run.sh:301, verbatim
say harness-test "started in the background, ${HARNESS_TIMEOUT_S} s at most"
answer='{"result":"next"}'
}

holder_check() { alive_recorded holder || refuse "the lock holder exited; the locks are free"; }
# item_answer <key>: {"<key>": {exit, stopped_by}} from <key>/exit.json, or null: missing, empty or not an object.
# jq reads an empty file as no input, printing nothing with exit 0 (seen on jq-1.7): the file is tested first.
item_answer() {
  local o=""
  [[ ! -s "$dir/$1/exit.json" ]] || o="$(jq -c --arg k "$1" '{($k): {exit, stopped_by}}' "$dir/$1/exit.json" 2>/dev/null)" || o=""
  [[ -n "$o" ]] || o="$(jq -cn --arg k "$1" '{($k): null}')"
  printf '%s\n' "$o"
}

verb_lease() {
k="$key"
holder_check
# run.sh:306, but for its break, which here is the answer stop-leasing
  if [[ -s "$dir/.harness" ]] && read -r hrc _ < "$dir/.harness" && [[ "$hrc" != 0 ]]; then say "lease $k" "not taken: make harness-test already exited $hrc"; answer="$(jq -cn --arg h "$hrc" '{result: "stop-leasing", harness_exit: ($h | tonumber? // $h)}')"; exit 0; fi
# run.sh:307-326, verbatim
  w="$(wave_time_why)"; [[ -z "$w" ]] || refuse "too late for the wave at lease $k: $w"
  w="$(own_laugd_why)"; [[ -z "$w" ]] || refuse "the laugd on $GATE_POOL_LISTEN is not the gate's own at lease $k: $w"
  mark "lease $k"; mkdir "$dir/$k"; : > "$dir/$k/.item"; t="$(now_ms)"; rc=0
  curl -s --fail-with-body -m 330 -X POST "$pool_url/lease" -H 'content-type: application/json' -o "$dir/$k/.lease.json" \
    -d "$(jq -cn --argjson ttl "$LEASE_TTL_S" --arg r "gate-$gate_id-$k" --arg t "$(item_tag "$k")" '{ttl_seconds: $ttl, requester: $r, server_tag: $t}')" 8>&- 9>&- & bg_pid=$!
  wait "$bg_pid" || rc=$?; bg_pid=""
  (( rc == 0 )) || refuse "lease $k: laugd refused it (curl exit $rc): $(jq -r '.error // empty' "$dir/$k/.lease.json" 2>/dev/null || true)"
  bad="$(jq -r --arg tag "$(item_tag "$k")" --arg of "$(item_tag_of "$k")" --arg req "gate-$gate_id-$k" '
    if (.id | type) != "string" or (.id | test("^[0-9a-f]{8}$") | not) then "names no cell id"
    elif .requester != $req then "is held by \(.requester | tojson), not \($req)"
    elif .state != "leased" then "is \(.state | tojson), not leased"
    elif (.server_tag // "") == "" then "names no server_tag; cells/backend-compose.sh prints the tag it started the cell with"
    elif .server_tag != $tag then "is on server_tag \(.server_tag), and \($of) is \($tag)"
    else empty end' "$dir/$k/.lease.json" 2>&1)" || bad="is not a lease laugd would write"
  [[ -z "$bad" ]] || refuse "lease $k: cell $(jq -r '.id // "unknown"' "$dir/$k/.lease.json" 2>/dev/null || echo unknown) $bad"
  jq --arg root "$root" 'if (.dynconfig | type) == "string" and (.dynconfig | startswith("/")) then
      .dynconfig |= (if startswith($root + "/") then .[($root | length) + 1:] else "(outside the repository)/" + (split("/") | last) end) else . end' \
    "$dir/$k/.lease.json" > "$dir/$k/lease.json"
  event "leased $k"
  say "lease $k" "cell $(jq -r '"\(.id), block \(.block), server \(.server_tag)"' "$dir/$k/lease.json"), in $(secs_since "$t") s"
answer="$(jq -c '{result: "next", cell: .id}' "$dir/$k/lease.json")"
}

verb_barrier() {
holder_check
# run.sh:328-330, verbatim
leases=(); for k in "${keys[@]}"; do [[ ! -e "$dir/$k/lease.json" ]] || leases+=("$dir/$k/lease.json"); done
dup=""; (( ${#leases[@]} == 0 )) || dup="$(jq -rs 'if ([.[].id] | unique | length) < length or ([.[].block] | unique | length) < length then "x" else empty end' "${leases[@]}")"
[[ -z "$dup" ]] || refuse "laugd leased one cell or one port block to two items"
# run.sh:333-339, verbatim but :338, where to_judge is finish
mark barrier
wait_harness
say harness-test "exit $harness_rc in $(jq -r '.seconds // "?"' "$dir/harness.json") s$(jq -r 'if .tally then ": \(.tally)" else "" end' "$dir/harness.json")$( [[ "$(jq -r .timed_out "$dir/harness.json")" != true ]] || echo ", stopped at its ${HARNESS_TIMEOUT_S} s")"
if (( harness_rc != 0 )); then
  say barrier "make harness-test did not pass: no item runs (R1)"
  to_judge
fi
# run.sh:340, as a record: the quiet steps read the limit
state_put --argjson l "$(( $(now_ms) + BARRIER_TIMEOUT_S * 1000 ))" '.barrier_limit_ms = $l'
answer="$(jq -c '{result: "next", limit_ms: .barrier_limit_ms}' "$dir/.state.json")"
}

verb_quiet() {
# run.sh:342-343, verbatim
  w="$(own_laugd_why)"; [[ -z "$w" ]] || refuse "the laugd on $GATE_POOL_LISTEN is not the gate's own at the barrier: $w"
  w="$(quiet_why)"
if [[ -n "$w" ]]; then
limit="$barrier_limit_ms"
# run.sh:348, verbatim
  (( $(now_ms) < limit )) || refuse "the host was not quiet ${BARRIER_TIMEOUT_S} s after make harness-test passed: $w"
fi
answer="$(jq -cn --arg w "$w" --argjson ms "$(now_ms)" '{result: "next", quiet: ($w == ""), why: $w, wall_ms: $ms}')"
}

verb_wave_time() {
holder_check
# run.sh:351-352, verbatim
w="$(wave_time_why)"; [[ -z "$w" ]] || refuse "too late for the wave at the barrier: $w"
say barrier "laugd 0 busy, compose projects up: the ${#keys[@]} leased$( [[ "$fake" != 1 ]] || echo " (GATE_FAKE=1: the fake backend's)"), still so after ${QUIET_S} s; $(( (t0_ms + DEADLINE_S * 1000 - $(now_ms)) / 1000 )) s left for the wave, wave_min_s $WAVE_MIN_S"
answer="$(jq -cn --argjson l "$(( (t0_ms + DEADLINE_S * 1000 - $(now_ms)) / 1000 ))" '{result: "next", left_s: $l}')"
}

verb_wave() {
local k
state_rec wave $$
# run.sh:355-357, verbatim
mark wave
poll_once >> "$dir/poll.jsonl"
start_poller
state_rec poller "$poller_pid"
# run.sh:358, with each item's record right after its launch
for i in "${!keys[@]}"; do launch_item "$i" "${keys[$i]}"; item_rec "${keys[$i]}"; done
# run.sh:359-364, verbatim
say wave "${#keys[@]} item(s) started under setsid; the watchdog TERMs what is left at $(printf '+%d:%02d' $(( DEADLINE_S / 60 )) $(( DEADLINE_S % 60 )))"
watch_items $(( t0_ms + DEADLINE_S * 1000 )) 2>>"$dir/watchdog.log"   # bash notes a KILLed job on stderr: kept out of the terminal
sweep_orphans
stop_poller
mark "wave end"
say wave "every item ended; $(wc -l < "$dir/poll.jsonl") poll(s) recorded"
answer="$(for k in "${keys[@]}"; do item_answer "$k"; done | jq -cs '{result: "done", items: (add // {})}')"
}

verb_combine() {
local x
# run.sh:147-150, verbatim
  local rc=0 prc=0 verdict
  mark combine
  jput "$dir/clock.json" --argjson t "$(stamp)" '.t_post = $t'
  bin/laug judge -write "$dir" || rc=$?
# run.sh:151, but for its exit 2: the trap verdict here, as run.sh's EXIT trap writes it (:182)
  if (( rc != 0 && rc != 1 )); then why="the judge wrote no verdict"; step=combine; if trap_verdict; then x=1; else x=2; fi; answer="$(jq -cn --argjson x "$x" '{result: "done", verdict: (if $x == 1 then "BLOCK" else null end), exit: $x, trap: true}')"; exit 0; fi
# run.sh:152, verbatim
  combined=1; verdict="$(jq -r .verdict "$dir/verdict.json")"
answer="$(jq -cn --arg v "$verdict" --argjson x "$rc" '{result: "done", verdict: $v, exit: $x, trap: false}')"
}

verb_post() {
# run.sh:147's prc, then 153-154; after a stop, run.sh:183: no mark, and gate/post.sh only on a verdict
local prc=0
[[ "$key" == stopped ]] || mark post
if [[ "$key" != stopped || -s "$dir/verdict.json" ]]; then gate/post.sh "$dir" || prc=$?; else prc=2; fi
answer="$(jq -cn --argjson x "$prc" '{result: "done", exit: $x}')"
}

verb_release() {
# cleanup turns set -e off and ignores TERM (gate/lib.sh), so the lines after it test their own status.
cleanup
# run.sh:181 on the way out, by record; then the holder, which frees both locks; then run.sh:157's event
! alive_recorded harness || stop_recorded harness
stop_recorded holder
[[ "$key" == stopped ]] || event done
answer="$(jq -c '{result: "done", laugd, released: (.released | length), left}' "$dir/cleanup.json")" \
  || { why="cleanup wrote no cleanup.json"; exit 1; }
}

verb_stopped() { # run.sh:178-182, by records: the items, the poller, the wave and the harness are no children of this step
# No step yet: open stopped before its first mark. run.sh names start there, set before its traps (:127).
local stopped_at="${step:-start}" k p t x tr=false v=null term=() killed=() live=()
for k in "${keys[@]}"; do
  [[ -e "$dir/$k/.pid" ]] && alive_recorded "item/$k" || continue
  p="$(record_of "item/$k")" || continue; p="${p%% *}"
  kill -TERM -- "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null || true; term+=("$k")
done
if (( ${#term[@]} > 0 )); then
  event "watchdog TERM"; say watchdog "TERM to ${term[*]}"
  t=$(( $(now_ms) + KILL_GRACE_S * 1000 ))
  while :; do
    live=(); for k in "${term[@]}"; do ! alive_recorded "item/$k" || live+=("$k"); done
    (( ${#live[@]} > 0 && $(now_ms) < t )) || break
    sleep 0.5
  done
  for k in "${live[@]}"; do
    alive_recorded "item/$k" || continue
    p="$(record_of "item/$k")" || continue; p="${p%% *}"
    kill -KILL -- "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null || true; killed+=("$k")
  done
  if (( ${#killed[@]} > 0 )); then
    event "watchdog KILL"; say watchdog "KILL to ${killed[*]}"
    t=$(( $(now_ms) + 10000 ))
    while :; do
      live=(); for k in "${killed[@]}"; do ! alive_recorded "item/$k" || live+=("$k"); done
      (( ${#live[@]} > 0 && $(now_ms) < t )) || break
      sleep 0.5
    done
    (( ${#live[@]} == 0 )) || say watchdog "still there 10 s after KILL, left: ${live[*]}"
  fi
fi
# sweep_orphans' sweep (gate/lib.sh, sweep_item), for every key: this step cannot tell which item the wave's watchdog
# sent KILL. By session, from the item's record when it is this boot's. A key with no record is swept by its LEASE_FILE
# only once the wave began: the wave records each item right after its launch, and a wave step that ended in between
# leaves an item with none. Before the wave no item started, so no process holds an item's LEASE_FILE; a lease request
# in flight is its lease step's to stop (on_term).
local r rp rs rb
for k in "${keys[@]}"; do
  if r="$(record_of "item/$k")"; then
    read -r rp rs rb <<<"$r"; [[ "$rb" != "$(boot_id)" ]] || sweep_item "$k" "$rp" "$rs"
  elif record_of wave >/dev/null; then sweep_item "$k"; fi
done
stop_recorded poller; stop_recorded wave; stop_recorded harness
# The trap verdict, unless the judge answered with a verdict: run.sh's combined is set only once one exists (:152).
if [[ "$(jq -r .exit "$dir/.steps/judge.done" 2>/dev/null)" == 0 && -s "$dir/verdict.json" ]]; then
  v="$(jq -c .verdict "$dir/verdict.json")"; if [[ "$v" == '"ADVANCE"' ]]; then x=0; else x=1; fi
else
  step="$stopped_at"; why="$key"; tr=true
  if trap_verdict; then x=1; v='"BLOCK"'; else x=2; fi
fi
answer="$(for k in "${keys[@]}"; do
    [[ ! -e "$dir/$k/.pid" ]] || item_answer "$k"
  done | jq -cs --arg at "$stopped_at" --argjson v "$v" --argjson x "$x" --argjson tr "$tr" \
    --argjson term "$(printf '%s\n' "${term[@]+"${term[@]}"}" | jq -Rcs 'split("\n") | map(select(. != ""))')" \
    --argjson kill "$(printf '%s\n' "${killed[@]+"${killed[@]}"}" | jq -Rcs 'split("\n") | map(select(. != ""))')" \
    '{result: "done", stopped_at: $at, verdict: $v, exit: $x, trap: $tr, term: $term, kill: $kill, items: (add // {})}')"
}

case "$verb" in check|open) ;; *) load_state ;; esac
"verb_${verb//-/_}"
exit 0
