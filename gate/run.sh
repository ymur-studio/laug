#!/usr/bin/env bash
# The gate run: every item of a plan at once on one host, each on a cell of its own leased from a laugd the
# gate starts for itself, each judged before its cell is released, then one verdict (bin/laug judge) handed on by the
# gate's one exit (gate/post.sh). laug gate run x.y.z [-plan gate/plan.yaml] [-items n] runs it once make build has made
# bin/laug, and the clock starts when this script does. gate/run.sh --plan <file> [n] checks a
# plan and prints it as plan.json would hold it, or its problems and exit 2. The design: one lockstep wave judged
# before release, with a host lock and a quiet check, unknown-prefix BLOCK and two clocks.
#
# The run, in order. Every step is a line of timeline.jsonl, on the wall clock and the monotonic one (/proc/uptime: the
# dev host's wall clock ran 1.045 to 1.048 times it, by laug host clock, cells/results/2026-10-23-boot1.txt:69).
#   1 lock        flock GATE_LOCK (gate/.lock), one gate per checkout, and GATE_HOST_LOCK (laug-gate.lock in
#                 XDG_RUNTIME_DIR, else /tmp), one gate per host for this user, whichever checkout or worktree it runs
#                 from. A gate that finds either held names it and gives no laug pool down advice: what is up is most
#                 likely the running gate's, and laug pool down would stop its laugd and remove its cells mid-wave.
#     plan        PLAN as plan.json, cut to its first ITEMS items (gate/plan.yaml says what each key means). A cut
#                 plan BLOCKs on R2 (internal/judge): the noise runs read its items' readings, not its verdict.
#     preflight   refused (verdict.json BLOCK, class refused) when any holds, each named: a lock is held; the plan has a
#                 problem; anything answers on GATE_POOL_LISTEN (127.0.0.1:7700); a compose project is up on this host;
#                 load/thresholds.sh does not resolve SERVER_TAG, or the plan's incumbent; an image of
#                 cells/compose/cell.yml at either tag is not local (a first pull is 3.2 GB, README.md); OMES_DIR has no
#                 cmd/omes; this host has under 16 vCPU or under 8 GiB available, or is not the host type and size its
#                 threshold set names (gate/lib.sh, host_reasons); ss is not on PATH, or an item's Omes metrics port is
#                 taken; bin/laug is not built; env has no --default-signal (gate/lib.sh, sigdefault); bin/laug's judge
#                 is not HEAD's (gate/lib.sh, judge_input).
#   2 omes_build  go build ./cmd/omes in OMES_DIR, 300 s at most, so the items' go run starts on a warm build cache
#                 (load/run-omes.sh). The first go run still links, later ones do not (go run -x, go 1.24.1 and 1.26.0).
#   3 laugd       bin/laug pool serve -pid-file none -listen GATE_POOL_LISTEN -min 0 -max N -quota N -server-tag
#                 SERVER_TAG, N the items: no pid file. With min 0 its replenish builds nothing (internal/pool/
#                 pool.go, replenish), so every cell on the host is one the gate leased. Its -max-lease is 2 h and a
#                 random fraction of a second, which laugd's /status reports back as max_lease: refused unless the
#                 process the gate started is alive and what answers on the port reports that max_lease, min 0, max N
#                 and quota N. A laugd that bound the port after preflight (laug pool serve, or another user's gate)
#                 makes the gate's own exit on its failed bind; without this check the gate would lease from it.
#                 The same check runs before each lease and at the barrier.
#   4 harness     make harness-test on this tree, in the background under setsid, harness_timeout_s (300) at most,
#                 with SIGINT and SIGQUIT at their defaults (gate/lib.sh, sigdefault).
#   5 lease       one cell per item, one lease at a time, so no two cold creates overlap; TTL lease_ttl_s (1200),
#                 requester gate-<gate id>-<key>, server_tag the item's (gate/lib.sh, item_tag: SERVER_TAG, or the
#                 plan's incumbent for the item with arm incumbent). Refused before a lease when less than wave_min_s
#                 (386 s, pl's long pole) is left before deadline_s: an item started later is certain to meet the
#                 watchdog. The lease laugd answered is the item's <key>/.lease.json; <key>/lease.json is the copy a
#                 result keeps, its dynconfig path made relative. Refused when laugd refuses a lease, or a cell's
#                 server_tag is not the item's (cells/backend-compose.sh prints it; laugd's own fake prints none).
#   6 barrier     make harness-test has exited 0 (else no item runs, and R1 BLOCKs, class harness); laugd shows 0 busy
#                 cells and the compose projects up are exactly the leased ones; quiet_s (10) s later all of that still
#                 holds. Refused when the host is not quiet barrier_timeout_s (60) s after the harness passed, when
#                 the laugd on the port is not the gate's own, or when less than wave_min_s is left before deadline_s.
#   7 wave        every item started within 1 s, each under setsid in a session and process group of its own, with
#                 RUN_ID=<gate id>-<key>, RESULTS_DIR=<key>/, its own lease file, POOL_QUIET_WAIT_S=0 (a busy cell is
#                 recorded, never waited for) and its own Omes metrics ports, 127.0.0.1:19100+2i and 19101+2i for the
#                 i-th item from 0 (faults/lib.sh, wait_omes_ports). Nothing listens on 9091 to 9093, so
#                 no cell scrapes another item's Omes (cells/compose/prometheus.yml). A fault item is faults/run.sh
#                 <scenario> <fault> <lease>; a load item is load/run-omes.sh, then load/verdict.sh with VERDICT_JSON.
#                 exit.json holds the script's own exit, read from the script and never from make, which turns 1, 2
#                 and 3 into 2.
#   8 poll        every poll_s (5) s during the wave: laugd's busy cells, docker compose ls, /proc/pressure, to
#                 poll.jsonl. The poller ends when the gate does, even a gate that was sent KILL: each pass first checks
#                 that its parent is still the gate's process.
#   9 watchdog    deadline_s (780) s after the gate start on the wall clock, TERM to every live item's process group,
#                 KILL kill_grace_s (30) s later (faults/run.sh's EXIT trap removes its fault and writes a partial JSON
#                 on TERM); after a KILL, TERM to what is left in the item's session and in its load's, which
#                 faults/run.sh starts under setsid (gate/lib.sh, sweep_item: by session, not by run id alone).
#  10 combine     bin/laug judge -write writes verdict.json and verdict.md; gate/post.sh hands them on. The clock
#     post        stops here: clock.json's t_post is taken just before the judge runs.
#  11 release     off the clock: every leased cell released; then, before laugd stops, laugd's /status read until no
#                 cell is busy and none is leased to this gate, 330 s at most (laugd's 300 s create timeout and a
#                 destroy): a signal during a lease leaves a cold create under way, which laugd finishes and gives back
#                 itself once the client has gone (handleLease), and a cell still leased to the gate is released here.
#                 Then laugd stopped, and every laug-* compose project up now that was not up at preflight recorded in
#                 cleanup.json as left.
# An EXIT trap covers every other way out (a signal, a failed step): it stops the items as the watchdog would, writes a
# BLOCK verdict.json that names the step when the judge wrote none, hands it on, and releases. A second Ctrl-C
# while it does is ignored.
#
# The results, in GATE_RESULTS/<gate id>/, are the directory the judge reads (internal/judge, Input): gate.json,
# clock.json, plan.json, thresholds.json, thresholds-incumbent.json (a plan with an incumbent), harness.json,
# poll.jsonl, and one directory per item, with an empty .item so git keeps one that stays empty. Besides those:
# timeline.jsonl, cleanup.json, and logs (*.log, which .gitignore keeps out): laugd.log, harness.log, omes-build.log,
# watchdog.log, <key>/item.log. No other directory is made at the top, since the judge reads every one as an item.
#
# Environment (laug gate run sets the first seven):
#   SERVER_TAG      the server release the gate judges, x.y.z; required
#   PLAN            the plan (gate/plan.yaml); ITEMS runs its first n items only (the noise runs, gate/plans/noise.yaml)
#   GATE_POST       the sink gate/post.sh hands the verdict to; local, the only one built, is checked before anything
#   GATE_RESULTS    where results go (gate/results)
#   BASE            the commit the bar's files are compared with, changed_vs_base (main; else origin/main, else HEAD)
#   OMES_DIR        Omes (.upstream/omes)
#   GATE_FAKE=1     no Docker, no cell, no Omes: load/testdata/gate/fakebin first on PATH (docker, ss, nproc, free, go),
#                   laugd with load/testdata/gate/fake-backend.sh (GATE_BACKEND overrides it; empty is laugd's own fake
#                   backend, whose leases name no server_tag), load/testdata/gate/fake-item.sh for every item and
#                   load/testdata/gate/fake-harness.sh for make harness-test. make harness-test runs it so.
#   GATE_LOCK       the checkout's lock (gate/.lock), GATE_HOST_LOCK the host's (laug-gate.lock in XDG_RUNTIME_DIR,
#                   else /tmp), and GATE_POOL_LISTEN laugd's address (127.0.0.1:7700): make harness-test points all
#                   three elsewhere, since a real gate holds them while its own make harness-test runs.
# The knobs the items' scripts read (THRESHOLDS, RATE, CATALOG, VISIBILITY, BIND_ADDR and the rest, listed in
# gate/lib.sh, gate_env) are unset: the bar and the cell are the checkout's, and the cells bind 127.0.0.1.
# Exit 0 on ADVANCE, 1 on BLOCK (verdict.json says why), 2 on a usage error before any gate run, or when no verdict
# could be written or handed on. That is this script's exit, and laug gate run's, which execs it. make gate exited 2 on
# a BLOCK too, since GNU make turns every failed recipe into 2; either way the exit is a summary, and verdict.json is
# the answer. A caller that must tell a BLOCK from a gate that could not judge reads verdict.json: its class, and
# stopped_at when the gate stopped before its judge judged; no verdict.json means none could be written.
#
# Every function but usage, finish, to_judge, on_signal and on_exit is in gate/lib.sh, refuse too, and so are the input
# checks, as gate_env. They live there so that workflow Gate's step driver, gate/step.sh, shares them;
# step.sh holds its own copy of each step body below. gate.json names the driver of a run: gate/run.sh says run.sh.
set -eEuo pipefail
export LC_ALL=C
root="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$root"
. gate/lib.sh
driver=run.sh
if [[ "${1:-}" == --plan ]]; then
  [[ -n "${2:-}" && ( -z "${3:-}" || "${3:-}" =~ ^[1-9][0-9]*$ ) ]] || { echo "usage: gate/run.sh --plan <file> [n], n from 1" >&2; exit 2; }
  plan_read "$2" "${3:-}"; exit
fi

usage() { echo "laug · $*" >&2; exit 2; }
(( $# == 0 )) || usage "gate/run.sh takes no arguments: ${LAUG:-bin/laug} gate run x.y.z [-plan gate/plan.yaml] [-items n], or gate/run.sh --plan <file> [n]"
gate_env
t0="$(stamp)"; t0_ms="$(sed -E 's/.*"wall":([0-9]+)\.([0-9]{3}).*/\1\2/' <<<"$t0")"

gate_id="g$(date -u +%Y%m%d-%H%M%S)"; dir="$(cd "$GATE_RESULTS" && pwd -P)/$gate_id"; n=1
until mkdir "$dir" 2>/dev/null; do   # two gate runs in one second, as make harness-test makes them
  n=$(( n + 1 )); (( n < 100 )) || usage "no results directory could be made beside $dir"
  gate_id="g$(date -u +%Y%m%d-%H%M%S)-$n"; dir="$(dirname "$dir")/$gate_id"
done
step=start
mark start
printf '{"t0":%s,"t_post":null}\n' "$t0" | jq . > "$dir/clock.json"
jq -n --arg id "$gate_id" --arg tag "$SERVER_TAG" --arg commit "$(git rev-parse --short HEAD)" \
  --argjson dirty "$([[ -n "$(git status --porcelain -- . ':(exclude)gate/results')" ]] && echo true || echo false)" \
  --arg base "$base_commit" --arg base_ref "$BASE" --arg plan "$(shown "$PLAN")" --arg items "$ITEMS" --argjson fake "$([[ "$fake" == 1 ]] && echo true || echo false)" \
  --arg pool "$GATE_POOL_LISTEN" \
  '{gate_id: $id, server_tag: $tag, laug_commit: $commit, tree_dirty: $dirty, base: $base, base_ref: $base_ref, inputs: null,
    host: null, refused: null, plan: $plan, items_limit: (if $items == "" then null else ($items | tonumber) end), fake: $fake,
    pool: $pool, driver: "run.sh", workflow_id: null, worker_commit: null}' > "$dir/gate.json"
echo "laug · gate $gate_id: server $SERVER_TAG, plan $(shown "$PLAN")${ITEMS:+ (its first $ITEMS)}, results $(shown "$dir")$([[ "$fake" != 1 ]] || echo ", GATE_FAKE=1: no Docker, cell or Omes")${base_note:+; $base_note}"

# State the EXIT trap reads.
combined=0; posted=0; cleaned=0; final=2; why=""; err=""; plan=""; wiring="[]"; keys=()
laugd_pid=""; harness_pid=""; poller_pid=""; bg_pid=""; at_preflight="[]"; harness_start_ms=0; locked=0; max_lease=""
declare -A item_pid=() item_start=() item_end=() item_exit=() item_stopped=()
DEADLINE_S=""; KILL_GRACE_S=30; WAVE_MIN_S=0

# The end of every run that gets as far as a verdict: combine, post, release, exit.
finish() {
  local rc=0 prc=0 verdict
  mark combine
  jput "$dir/clock.json" --argjson t "$(stamp)" '.t_post = $t'
  bin/laug judge -write "$dir" || rc=$?
  if (( rc != 0 && rc != 1 )); then why="the judge wrote no verdict"; exit 2; fi   # the EXIT trap writes one that says so
  combined=1; verdict="$(jq -r .verdict "$dir/verdict.json")"
  mark post
  gate/post.sh "$dir" || prc=$?; posted=1
  cleanup
  if (( prc != 0 )); then final=2; elif [[ "$verdict" == ADVANCE ]]; then final=0; else final=1; fi
  event done
  echo "$verdict"
  exit "$final"
}
# refuse <reason>... is in gate/lib.sh, shared with gate/step.sh: it records in gate.json why the gate will not run its
# wave, says so, and ends in to_judge, which each driver defines. Here to_judge is finish: bin/laug judge judges
# BLOCK, class refused, and the run posts, releases and exits 1. Under step.sh it answers refused, and the workflow
# runs the judge.
# gate/step.sh holds a copy of each step body below, headed "# run.sh:N-M, verbatim" with the lines it copies, which
# make harness-test holds to sed -n N,Mp of this file. So this block keeps the nine lines refuse had here, and every
# line below stays where it was before step.sh came.
to_judge() { finish; }

on_signal() { why="signal $1"; exit "$2"; }
on_exit() {
  local rc=$?
  trap '' INT TERM HUP; set +eE
  if (( ! combined )); then
    [[ -n "$why" ]] || why="${err:-exit $rc}"
    echo "laug · gate $gate_id stopping at step $step: $why; items get TERM and up to ${KILL_GRACE_S} s, then the cells are released (Ctrl-C is ignored now)" >&2
  fi
  [[ -z "$bg_pid" ]] || kill "$bg_pid" 2>/dev/null
  [[ -z "$(live_items)" ]] || { watch_items 0 2>>"$dir/watchdog.log"; sweep_orphans; }
  stop_poller
  [[ -z "$harness_pid" ]] || { stop_group "$harness_pid"; harness_pid=""; }
  if (( ! combined )); then if trap_verdict; then final=1; else final=2; fi; fi
  if (( ! posted )) && [[ -s "$dir/verdict.json" ]]; then gate/post.sh "$dir" || final=2; posted=1; fi
  (( cleaned )) || cleanup
  [[ -z "${FAKE_GATE_TMP:-}" ]] || { d="$FAKE_GATE_TMP"; [[ "${HOME:-/}" == / || "$d" != "$HOME"/* ]] || d="~/${d#"$HOME"/}"; echo "laug · gate $gate_id: on fakes, its files are in $d"; }; exit "$final"   # FAKE_GATE_TMP: laug gate run -fake's dir
}
trap on_exit EXIT
trap 'on_signal INT 130' INT; trap 'on_signal TERM 143' TERM; trap 'on_signal HUP 129' HUP
trap 'err="gate/${BASH_SOURCE[0]##*/} line $LINENO: $BASH_COMMAND failed"' ERR

# 1. Lock, plan, preflight. Every reason found is named; any one refuses.
reasons=()
mark lock
mkdir -p "$(dirname "$GATE_LOCK")"
exec 9>>"$GATE_LOCK"
locked=1
flock -n 9 || { locked=0; reasons+=("another gate run holds $(shown "$GATE_LOCK"): wait for it to end"); }
if { exec 8>>"$GATE_HOST_LOCK"; } 2>/dev/null; then
  flock -n 8 || { locked=0; reasons+=("another gate run on this host holds $(shown "$GATE_HOST_LOCK"): wait for it to end"); }
else locked=0; reasons+=("the host lock $(shown "$GATE_HOST_LOCK") cannot be opened (GATE_HOST_LOCK)"); fi
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

# 2. Omes, built once so the items' go run finds the cache warm.
mark omes_build; t="$(now_ms)"; rc=0
( cd "$OMES_DIR" && exec timeout 300 go build -o /dev/null ./cmd/omes ) > "$dir/omes-build.log" 2>&1 8>&- 9>&- & bg_pid=$!
wait "$bg_pid" || rc=$?; bg_pid=""
(( rc == 0 )) || refuse "go build ./cmd/omes in $(shown "$OMES_DIR") exited $rc$( (( rc != 124 )) || echo " after 300 s; a first build belongs before the gate"); see omes-build.log"
say omes "go build ./cmd/omes in $(secs_since "$t") s"

# 3. laugd, the gate's own: min 0, so it builds nothing the gate did not lease. laugd writes a lease's times in UTC
# itself; TZ=UTC stays for the backend scripts it starts, which inherit it. Its -max-lease, 2 h and a random 6-digit
# fraction of a second whose last digit is not 0, comes back from /status as max_lease in Go's duration format
# (2h0m0.417000s prints as 2h0m0.417s, so no trailing 0): the mark that what answers on the port is this laugd.
mark laugd
nonce=$(( (RANDOM * 32768 + RANDOM) % 1000000 )); (( nonce % 10 )) || nonce=$(( nonce + 1 ))
max_lease="$(printf '2h0m0.%06ds' "$nonce")"
TZ=UTC setsid bin/laug pool serve -pid-file none -listen "$GATE_POOL_LISTEN" -min 0 -max "${#keys[@]}" -quota "${#keys[@]}" \
  -max-lease "$max_lease" -server-tag "$SERVER_TAG" -backend "$backend" > "$dir/laugd.log" 2>&1 8>&- 9>&- &
laugd_pid=$!
for _ in $(seq 1 100); do curl -sf -m 1 -o /dev/null "$pool_url/healthz" && break; kill -0 "$laugd_pid" 2>/dev/null || break; sleep 0.1; done
curl -sf -m 1 -o /dev/null "$pool_url/healthz" || refuse "laugd did not answer on $GATE_POOL_LISTEN within 10 s: $(tail -1 "$dir/laugd.log")"
w="$(own_laugd_why)"; [[ -z "$w" ]] || refuse "the laugd on $GATE_POOL_LISTEN is not the gate's own: $w"
say laugd "$GATE_POOL_LISTEN, min 0, max ${#keys[@]}, quota ${#keys[@]}, max lease $max_lease, backend $(if [[ -z "$backend" ]]; then echo "laugd's own fake"; else shown "$backend"; fi)"

# 4. make harness-test, in the background while the cells are leased.
mark harness; start_harness
say harness-test "started in the background, ${HARNESS_TIMEOUT_S} s at most"

# 5. One lease at a time: no two cold creates overlap. Once make harness-test has failed, no item will run, so no more
# leases.
for k in "${keys[@]}"; do
  if [[ -s "$dir/.harness" ]] && read -r hrc _ < "$dir/.harness" && [[ "$hrc" != 0 ]]; then say "lease $k" "not taken: make harness-test already exited $hrc"; break; fi
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
done
leases=(); for k in "${keys[@]}"; do [[ ! -e "$dir/$k/lease.json" ]] || leases+=("$dir/$k/lease.json"); done
dup=""; (( ${#leases[@]} == 0 )) || dup="$(jq -rs 'if ([.[].id] | unique | length) < length or ([.[].block] | unique | length) < length then "x" else empty end' "${leases[@]}")"
[[ -z "$dup" ]] || refuse "laugd leased one cell or one port block to two items"

# 6. Barrier: harness-test passed, laugd idle, only the leased projects up, and still so quiet_s later.
mark barrier
wait_harness
say harness-test "exit $harness_rc in $(jq -r '.seconds // "?"' "$dir/harness.json") s$(jq -r 'if .tally then ": \(.tally)" else "" end' "$dir/harness.json")$( [[ "$(jq -r .timed_out "$dir/harness.json")" != true ]] || echo ", stopped at its ${HARNESS_TIMEOUT_S} s")"
if (( harness_rc != 0 )); then
  say barrier "make harness-test did not pass: no item runs (R1)"
  finish
fi
limit=$(( $(now_ms) + BARRIER_TIMEOUT_S * 1000 ))
while :; do
  w="$(own_laugd_why)"; [[ -z "$w" ]] || refuse "the laugd on $GATE_POOL_LISTEN is not the gate's own at the barrier: $w"
  w="$(quiet_why)"
  if [[ -z "$w" ]]; then
    (( QUIET_S == 0 )) || nap "$QUIET_S"
    w="$(quiet_why)"; [[ -n "$w" ]] || break
  fi
  (( $(now_ms) < limit )) || refuse "the host was not quiet ${BARRIER_TIMEOUT_S} s after make harness-test passed: $w"
  sleep 1
done
w="$(wave_time_why)"; [[ -z "$w" ]] || refuse "too late for the wave at the barrier: $w"
say barrier "laugd 0 busy, compose projects up: the ${#keys[@]} leased$( [[ "$fake" != 1 ]] || echo " (GATE_FAKE=1: the fake backend's)"), still so after ${QUIET_S} s; $(( (t0_ms + DEADLINE_S * 1000 - $(now_ms)) / 1000 )) s left for the wave, wave_min_s $WAVE_MIN_S"

# 7 to 9. The wave, the poller, the watchdog.
mark wave
poll_once >> "$dir/poll.jsonl"
start_poller
for i in "${!keys[@]}"; do launch_item "$i" "${keys[$i]}"; done
say wave "${#keys[@]} item(s) started under setsid; the watchdog TERMs what is left at $(printf '+%d:%02d' $(( DEADLINE_S / 60 )) $(( DEADLINE_S % 60 )))"
watch_items $(( t0_ms + DEADLINE_S * 1000 )) 2>>"$dir/watchdog.log"   # bash notes a KILLed job on stderr: kept out of the terminal
sweep_orphans
stop_poller
mark "wave end"
say wave "every item ended; $(wc -l < "$dir/poll.jsonl") poll(s) recorded"

# 10 and 11.
finish
