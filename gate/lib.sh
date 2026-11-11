# shellcheck shell=bash
# gate/run.sh's functions, all but usage, finish, on_signal and on_exit, live here so that gate/step.sh, the step
# driver the workflow runs, shares them; refuse joined them with step.sh. The text is run.sh's, apart from gate_env,
# which wraps run.sh's input checks, and what step.sh needs: mark, pid_alive and pid_stop where run.sh had kill -0
# and stop_group, the driver in trap_verdict, and refuse's to_judge. sweep_orphans matches by session, through
# sweep_item, not by name. Sourced, not run: it defines functions and two constants, PLAN_DEFAULTS and PLAN_JQ, and
# starts nothing.
# The caller sets root and driver (run.sh or step.sh), runs from root under set -eEuo pipefail, and defines usage and
# to_judge, since each driver has its own: refuse ends in to_judge, which is finish under run.sh and an answer under
# step.sh.
# gate_env checks the gate's inputs and sets what derives from them: SERVER_TAG and GATE_POST exported, PLAN, ITEMS,
# GATE_RESULTS, BASE, OMES_DIR, lock_dir and the two locks, GATE_POOL_LISTEN, fake, base_commit, backend, harness_cmd,
# pool_url and sigdefault. It also makes GATE_RESULTS, unsets the items' knobs and TEMPORAL_*, and, under GATE_FAKE=1,
# puts the fakes first on PATH. On a bad input it calls usage. When gate/post.sh --check fails, it exits 2.
# The other functions read globals the driver sets: dir, gate_id, root, driver, step, why, plan, keys, wiring, the
# item_* arrays, laugd_pid, harness_pid, harness_start_ms, poller_pid, bg_pid, locked, the plan's *_S values,
# max_lease, at_preflight, t0, t0_ms, and preflight's vcpu, mem_b and reasons. They read gate_env's SERVER_TAG,
# GATE_POOL_LISTEN, OMES_DIR, fake, harness_cmd, pool_url and sigdefault too. They set step, bg_pid, poller_pid,
# harness_pid, harness_start_ms, harness_rc, the item_* arrays, cleaned, combined, and host_type (host_reasons).
# Records, for step.sh, whose steps are processes of their own: state_put edits <dir>/.state.json; proc_stat,
# proc_start, sid_of and boot_id read /proc; state_rec and item_rec write a process's record, {pid, start, boot_id};
# the guard, alive_recorded and stop_recorded, acts on a recorded pid only while it is still the process recorded;
# pid_alive and pid_stop are kill -0 and stop_group under run.sh and the guard under step.sh. Under step.sh, mark also
# writes the step to .state.json.
# Sections, in run.sh's order: the plan, gate_env, clocks and records with event and mark, the guard, the host, items,
# the background processes, then release_id, cleanup, refuse and trap_verdict; last, gate_env's base_pick.

# The plan. Every timing has a default, the figures the gate was designed with; gate/plan.yaml states each one anyway.
PLAN_DEFAULTS='{"kill_grace_s": 30, "lease_ttl_s": 1200, "quiet_s": 10, "barrier_timeout_s": 60, "poll_s": 5, "harness_timeout_s": 300,
  "wave_min_s": 386}'
PLAN_JQ='
def int_in($lo; $hi): type == "number" and . == floor and . >= $lo and . <= $hi;
def unknown($n; $known): keys[] | select(. as $k | $known | index($k) | not) | "\($n): unknown key \(.)";
def tag: type == "string" and test("^[0-9]+\\.[0-9]+\\.[0-9]+$");
if type != "object" then {problems: ["the plan is not a mapping"]}
else
  ($defaults + .) as $p
  | ( [ unknown("the plan"; ["deadline_s", "incumbent", "items"] + ($defaults | keys)) ]
      + (if ($p | has("incumbent")) and ($p.incumbent | tag | not) then ["incumbent is \($p.incumbent | tojson), not a release tag, x.y.z"] else [] end)
      + [ [ ["deadline_s", 1, 3600], ["kill_grace_s", 0, 300], ["quiet_s", 0, 600], ["barrier_timeout_s", 1, 600],
          ["poll_s", 1, 60], ["harness_timeout_s", 1, 3600], ["wave_min_s", 0, 3600] ][]
          | . as [$k, $lo, $hi] | select($p[$k] | int_in($lo; $hi) | not)
          | "\($k) is \($p[$k] | tojson), not whole seconds from \($lo) to \($hi)" ]
      + (if ($p.deadline_s | type) == "number" and ($p.wave_min_s | type) == "number" and $p.wave_min_s > $p.deadline_s
         then ["wave_min_s is \($p.wave_min_s), past deadline_s \($p.deadline_s): no wave could end before the watchdog"] else [] end)
      + (if ($p.deadline_s | type) == "number" and ($p.kill_grace_s | type) == "number"
            and ($p.lease_ttl_s | int_in($p.deadline_s + $p.kill_grace_s + 60; 7200) | not)
         then ["lease_ttl_s is \($p.lease_ttl_s | tojson); a cell must outlive deadline_s and kill_grace_s by 60 s, 7200 s at most"]
         else [] end)
      + ( if ($p.items | type) != "array" or ($p.items | length) == 0 then ["items lists no item"]
          elif ($p.items | length) > 8 then ["items lists \($p.items | length); 8 at most, one Omes port pair each from 19100 to 19115"]
          else
            [ $p.items | to_entries[] | (.key + 1) as $i | .value as $it
              | if ($it | type) != "object" then "item \($i) is not a mapping"
                else ("item " + (if ($it.key | type) == "string" then $it.key else "\($i)" end)) as $n
                  | (if ($it.key | type) == "string" and ($it.key | test("^[a-z0-9][a-z0-9-]{0,31}$")) then empty
                     else "\($n): key \($it.key | tojson) is not a lowercase name of 32 characters at most" end),
                    (if $it.expect == "ADVANCE" then empty
                     elif $it.expect == "BLOCK" then "\($n): expect BLOCK is not built: a positive control must name the reason it expects, and a BLOCK because its cell never got steady is no control (the judge, R4)"
                     else "\($n): expect is \($it.expect | tojson), not ADVANCE" end),
                    (if ($it.scenario | type) == "string" and ($it.scenario | test("^[a-z0-9_]+$")) then empty
                     else "\($n): scenario \($it.scenario | tojson) is not an Omes scenario name" end),
                    (if $it.kind == "fault" then
                       ($it | del(.arm, .pair) | unknown($n; ["key", "kind", "expect", "scenario", "fault", "rate", "max_concurrent"])),
                       (if ($it | has("arm")) or ($it | has("pair")) then "\($n): arm and pair are for a load item; an incumbent fault item is not built" else empty end),
                       (if ($it.fault | type) != "string" or ($cat | has($it.fault) | not) then "\($n): fault \($it.fault | tojson) is not a row of faults/catalog.yaml"
                        elif ($cat[$it.fault] | test("(^|;)[[:space:]]*serverfi[[:space:]]")) then "\($n): fault \($it.fault) is a serverfi row, which needs a SERVER_FI=1 cell; the gate leases plain cells"
                        else empty end),
                       (if ($it | has("rate")) and ($it.rate | int_in(1; 1000) | not) then "\($n): rate is \($it.rate | tojson), not a whole number from 1 to 1000" else empty end),
                       (if ($it | has("max_concurrent")) and ($it.max_concurrent | int_in(1; 10000) | not) then "\($n): max_concurrent is \($it.max_concurrent | tojson), not a whole number from 1 to 10000" else empty end)
                     elif $it.kind == "load" then
                       ($it | unknown($n; ["key", "kind", "expect", "scenario", "duration", "rate", "max_concurrent", "arm", "pair"])),
                       (if ($it.duration | type) == "string" and ($it.duration | test("^([0-9]+h)?([0-9]+m)?([0-9]+s)?$")) and $it.duration != "" then empty
                        else "\($n): duration is \($it.duration | tojson), not a time such as 5m (load/verdict.sh trusts no window under 5m)" end),
                       (if $it.rate | int_in(1; 1000) then empty else "\($n): rate is \($it.rate | tojson), not a whole number from 1 to 1000" end),
                       (if $it.max_concurrent | int_in(1; 10000) then empty else "\($n): max_concurrent is \($it.max_concurrent | tojson), not a whole number from 1 to 10000" end),
                       (if ($it | has("arm")) and $it.arm != "incumbent" then "\($n): arm is \($it.arm | tojson); the one arm built is incumbent" else empty end),
                       (if ($it | has("arm")) != ($it | has("pair")) then "\($n): arm and pair go together: an incumbent item names the load it is compared with" else empty end),
                       (if $it.arm == "incumbent" and ($p | has("incumbent") | not) then "\($n): arm incumbent, and the plan names no incumbent release" else empty end),
                       (if ($it | has("pair")) | not then empty
                        else ([$p.items[] | objects | select(.key == $it.pair)][0] // null) as $q
                          | if $q == null then "\($n): pair \($it.pair | tojson) names no item of the plan"
                            elif $q.kind != "load" or ($q | has("arm")) then "\($n): pair \($q.key) is not a load item with no arm"
                            else ("scenario", "duration", "rate", "max_concurrent") as $f
                              | select($q[$f] != $it[$f])
                              | "\($n): pair \($q.key) runs \($f) \($q[$f]), and this item \($it[$f]): a pair compares like with like"
                            end end)
                     else "\($n): kind is \($it.kind | tojson), not load or fault" end)
                end ]
            + [ [$p.items[] | objects | .key | strings] | group_by(.)[] | select(length > 1) | "item \(.[0]) is listed \(length) times" ]
            + ([$p.items[] | objects | select(.kind == "load" and .arm == "incumbent")] | length | if . > 1 then ["items: \(.) incumbent items; one is built"] else [] end)
            + (if $n != null and $n > ($p.items | length) then ["ITEMS=\($n), and the plan lists \($p.items | length)"] else [] end)
          end ) ) as $problems
  | if ($problems | length) > 0 then {problems: $problems}
    else {deadline_s: $p.deadline_s, kill_grace_s: $p.kill_grace_s, lease_ttl_s: $p.lease_ttl_s, quiet_s: $p.quiet_s,
          barrier_timeout_s: $p.barrier_timeout_s, poll_s: $p.poll_s, harness_timeout_s: $p.harness_timeout_s, wave_min_s: $p.wave_min_s}
         + (if $p | has("incumbent") then {incumbent: $p.incumbent} else {} end)
         + {items_in_plan: ($p.items | length), items: (if $n == null then $p.items else $p.items[:$n] end)} end
end'
plan_read() { # plan_read <file> [n]: the plan as JSON, defaults filled in, cut to its first n items; else its problems on stderr, one a line, and 2
  local file="$1" n="${2:-}" raw cat out
  [[ -f "$file" ]] || { echo "no such file" >&2; return 2; }
  raw="$(yq -o=json -I=0 '.' < "$file" 2>&1)" || { echo "not YAML: $(head -1 <<<"$raw")" >&2; return 2; }
  cat="$(yq -o=json -I=0 '.faults | with_entries(.value = ((.value.inject // "") + ";" + (.value.remove // "")))' faults/catalog.yaml)" \
    || { echo "faults/catalog.yaml does not read" >&2; return 2; }
  out="$(jq -c --argjson cat "$cat" --argjson defaults "$PLAN_DEFAULTS" --argjson n "${n:-null}" "$PLAN_JQ" <<<"$raw")" \
    || { echo "the plan could not be checked" >&2; return 2; }
  if jq -e 'has("problems")' <<<"$out" >/dev/null; then jq -r '.problems[]' <<<"$out" >&2; return 2; fi
  jq . <<<"$out"
}

gate_env() {
SERVER_TAG="${SERVER_TAG:-}"
[[ "$SERVER_TAG" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || usage "SERVER_TAG must be a release tag, x.y.z; got '$SERVER_TAG'"
export SERVER_TAG GATE_POST="${GATE_POST:-local}"
gate/post.sh --check || exit 2   # a sink that is not wired stops the gate here, not after 10 minutes of host time
PLAN="${PLAN:-gate/plan.yaml}"; ITEMS="${ITEMS:-}"
[[ -z "$ITEMS" || "$ITEMS" =~ ^[1-9][0-9]*$ ]] || usage "ITEMS=$ITEMS: how many of the plan's items to run, from 1"
GATE_RESULTS="${GATE_RESULTS:-gate/results}"; BASE="${BASE:-main}"; OMES_DIR="${OMES_DIR:-.upstream/omes}"
GATE_LOCK="${GATE_LOCK:-gate/.lock}"; GATE_POOL_LISTEN="${GATE_POOL_LISTEN:-127.0.0.1:7700}"
lock_dir="${XDG_RUNTIME_DIR:-/tmp}"; GATE_HOST_LOCK="${GATE_HOST_LOCK:-${lock_dir%/}/laug-gate.lock}"
[[ "$GATE_POOL_LISTEN" =~ ^127\.0\.0\.1:([1-9][0-9]{0,4})$ ]] && (( BASH_REMATCH[1] <= 65535 )) \
  || usage "GATE_POOL_LISTEN must be 127.0.0.1:<port>: the gate's pool has no auth (SECURITY.md)"
fake="${GATE_FAKE:-}"; [[ -z "$fake" || "$fake" == 1 ]] || usage "GATE_FAKE is 1 or empty, not '$fake'"
base_pick   # base_commit: BASE's commit; with no local main, origin/main's or HEAD's, and base_note (base_pick, below)
mkdir -p "$GATE_RESULTS" 2>/dev/null && [[ -w "$GATE_RESULTS" ]] || usage "GATE_RESULTS=$GATE_RESULTS is not a directory this script can write"

unset THRESHOLDS THRESHOLDS_RELEASES CATALOG CANDIDATES RESULTS_DIR RUN_ID LEASE_FILE SCENARIO RATE DURATION ITERATIONS \
  MAX_CONCURRENT CONTINUE_ON_FAILURE NAMESPACE CELL_ADDRESS PROJECT PROM_URL FALLBACK_PROM_URL MIN_WINDOW_S VERDICT_JSON \
  SWEEP_MARGIN_S SWEEP_EXPECTED SWEEP_ADDRESS SWEEP_RETRIES SWEEP_RETRY_S SA_SLICE_S TDBG_IMAGE ROTATE_SLACK_S \
  WORKER_METRICS_ADDR CLIENT_METRICS_ADDR POOL_ADDR POOL_QUIET_WAIT_S VISIBILITY BIND_ADDR SERVER_FI FC_GUEST_KEY \
  FC_GUEST_KNOWN_HOSTS GUEST_DYNCONFIG_ORIG LAUG_BLOCK DYNCONFIG_TEMPLATE LAUG_COMPOSE_EXTRA
unset "${!LAUG_FI_@}"
# The caller's TEMPORAL_*, such as a TEMPORAL_API_KEY in the shell or the TEMPORAL_ADDRESS laug orchestrator worker
# gives the worker that starts a step, must not reach the temporal CLI that cells/backend-compose.sh runs against a
# new cell's frontend (temporal options, CLI 1.9.1: 18 TEMPORAL_* variables, TEMPORAL_ADDRESS and TEMPORAL_API_KEY
# among them; --tls is "Auto-enabled when api-key or TLS options are set").
unset "${!TEMPORAL_@}"
if [[ "$fake" == 1 ]]; then
  export PATH="$root/load/testdata/gate/fakebin:$PATH"
  backend="${GATE_BACKEND-$root/load/testdata/gate/fake-backend.sh}"
  harness_cmd=("$root/load/testdata/gate/fake-harness.sh")
else
  backend="$root/cells/backend-compose.sh"
  harness_cmd=(env -u MAKEFLAGS -u MAKELEVEL make --no-print-directory harness-test)
fi
pool_url="http://$GATE_POOL_LISTEN"
# A background job of a non-interactive shell starts with SIGINT and SIGQUIT ignored, and a script that starts so cannot
# take either back: neither set -m nor trap undoes it (seen). Every script the gate starts in the background
# gets both at their defaults, as from a terminal. Without this, make harness-test's three Ctrl-C checks failed inside
# the gate and its run went past harness_timeout_s (g20261014-184524, a dev host run in internal/rejudge/testdata/runs:
# exit 124 at 300 s, 798 checks done; 191 s alone). The items get the same; the gate stops them with TERM and KILL only.
sigdefault=(env --default-signal=INT,QUIT)
}

# Clocks and records.
stamp() { local w m _r; w="$(date +%s.%3N)"; read -r m _r < /proc/uptime; printf '{"wall":%s,"mono":%s}' "$w" "$m"; }
now_ms() { date +%s%3N; }
# since: the time since t0, as +m:ss; +?:?? under step.sh when open did not get as far as t0 (the stop path after it)
since() { [[ -n "${t0_ms:-}" ]] || { printf '+?:??'; return 0; }; local ms=$(( $(now_ms) - t0_ms )); printf '+%d:%02d' $(( ms / 60000 )) $(( ms / 1000 % 60 )); }
say() { printf '%s %-14s %s\n' "$(since)" "$1" "$2"; }
secs_since() { awk -v a="$1" -v b="$(now_ms)" 'BEGIN { printf "%.1f", (b - a) / 1000 }'; }
# shown <path>: a path as a record keeps it, relative to the repository root, else "(outside the repository)/<name>"
shown() { local p; p="$(realpath -m -- "$1")"; case "$p" in "$root"/*) echo "${p#"$root"/}" ;; *) echo "(outside the repository)/${p##*/}" ;; esac; }
# rel <path>: a path as a script is given it: relative under the root, so what the script writes names no home path
rel() { local p; p="$(realpath -m -- "$1")"; case "$p" in "$root"/*) echo "${p#"$root"/}" ;; *) echo "$p" ;; esac; }
jput() { local f="$1"; shift; jq "$@" "$f" > "$f.tmp" && mv "$f.tmp" "$f"; }   # jput <file> <jq args>: edit a JSON file in place
event() { printf '{"step":"%s",%s\n' "$1" "$(stamp | cut -c2-)" >> "$dir/timeline.jsonl"; }   # a line of timeline.jsonl
# A step begins: the EXIT trap names it. Under step.sh .state.json keeps it, for the next step's trap verdict.
mark() { step="$1"; event "$1"; [[ "${driver:-run.sh}" != step.sh ]] || state_put --arg s "$1" '.step = $s'; }

# The guard. Under gate/step.sh each step is a process of its own, and what one step starts in the background (the lock
# holder, laugd, make harness-test, the wave's poller and items) outlives it: a later step finds it by its record,
# {pid, start, boot_id}, never by a pid alone, which the host may have given to another process since. start is field
# 22 of /proc/<pid>/stat (starttime, in clock ticks since boot; seen: a new process's starttime over getconf
# CLK_TCK was /proc/uptime within 0.01 s, 3 of 3), set at fork and kept through exec. Each function returns its status
# itself: called in a condition, a function runs with set -e off.
state_put() { # state_put <jq args>: edit .state.json through a temp file in .steps/, then mv; jq's status, else mv's
  local t rc=0
  t="$(mktemp "$dir/.steps/state.XXXXXX")" || return 1
  jq "$@" "$dir/.state.json" > "$t" || rc=$?
  if (( rc == 0 )); then mv "$t" "$dir/.state.json" || rc=$?; fi
  (( rc == 0 )) || rm -f "$t"
  return "$rc"
}
proc_stat() { # proc_stat <pid>: "<state> <start>", fields 3 and 22 of /proc/<pid>/stat; nothing and 1 when it has no entry
  local st
  [[ "$1" =~ ^[1-9][0-9]*$ ]] || return 1
  { read -r st < "/proc/$1/stat"; } 2>/dev/null || return 1
  # shellcheck disable=SC2086  # the fields after the command name, split: field N is ${N-2}
  set -- ${st##*) }
  [[ "${20:-}" =~ ^[0-9]+$ ]] || return 1
  echo "$1 ${20}"
}
proc_start() { local o; o="$(proc_stat "$1")" || return 1; echo "${o#* }"; }   # proc_start <pid>: its starttime
sid_of() { # sid_of <pid>: its session id, field 6 of /proc/<pid>/stat; nothing and 1 when it has no entry, or a session 0 or 1
  local st
  [[ "$1" =~ ^[1-9][0-9]*$ ]] || return 1
  { read -r st < "/proc/$1/stat"; } 2>/dev/null || return 1
  # shellcheck disable=SC2086  # as in proc_stat
  set -- ${st##*) }
  [[ "${4:-}" =~ ^[1-9][0-9]*$ && "$4" != 1 ]] || return 1
  echo "$4"
}
boot_id() { local b; read -r b < /proc/sys/kernel/random/boot_id || return 1; echo "$b"; }
state_rec() { # state_rec <name> <pid>: .state.json's <name>, {pid, start, boot_id}; start null when the process has ended
  local o s=null
  if o="$(proc_stat "$2")"; then s="${o#* }"; fi
  state_put --arg n "$1" --argjson p "$2" --argjson s "$s" --arg b "$(boot_id)" '.[$n] = {pid: $p, start: $s, boot_id: $b}'
}
item_rec() { # item_rec <key>: <dir>/<key>/.pid, {pid, pgid, start, boot_id}; the item leads its own group (setsid)
  local k="$1" p="${item_pid[$1]}" st s=null b=""
  # Read builtins and printf only, no jq: the wave starts every item within 1 s.
  if { read -r st < "/proc/$p/stat"; } 2>/dev/null; then
    # shellcheck disable=SC2086  # as in proc_stat
    set -- ${st##*) }; [[ ! "${20:-}" =~ ^[0-9]+$ ]] || s="${20}"
  fi
  read -r b < /proc/sys/kernel/random/boot_id || true
  printf '{"pid":%d,"pgid":%d,"start":%s,"boot_id":"%s"}\n' "$p" "$p" "$s" "$b" > "$dir/$k/.pid" || return 1
}
record_of() { # record_of <name>: "<pid> <start> <boot_id>" of .state.json's <name>, or of <dir>/<k>/.pid for item/<k>
  local f="$dir/.state.json" q='.[$n]'
  if [[ "$1" == item/* ]]; then f="$dir/${1#item/}/.pid"; q='.'; fi
  jq -er --arg n "$1" "$q"' | select(type == "object") | "\(.pid) \(.start) \(.boot_id)"' "$f" 2>/dev/null
}
alive_recorded() { # alive_recorded <name>: 0 when the recorded process runs: not a zombie, its start and boot_id the record's
  local o p s b st now
  o="$(record_of "$1")" || return 1
  read -r p s b <<<"$o"
  [[ "$p" =~ ^[1-9][0-9]*$ && "$s" =~ ^[0-9]+$ ]] || return 1   # no record, or one taken after the process had ended
  o="$(proc_stat "$p")" || return 1
  read -r st now <<<"$o"
  o="$(boot_id)"
  if [[ "$b" != "$o" ]]; then echo "laug · guard: $1 pid $p was recorded on boot $b, this is boot $o: nothing sent" >&2; return 1; fi
  if [[ "$now" != "$s" ]]; then echo "laug · guard: $1 pid $p started at $now, recorded $s: nothing sent" >&2; return 1; fi
  [[ "$st" != Z && "$st" != X ]] || return 1   # a zombie still answers kill -0, and under step.sh no step reaps it
  return 0
}
stop_recorded() { # stop_recorded <name> [grace_s]: TERM to its group, grace_s (5) s, then KILL; no wait, as it is no child
  local g="${2:-5}" p _
  alive_recorded "$1" || return 0
  p="$(record_of "$1")" || return 0; p="${p%% *}"
  kill -TERM -- "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null || true
  for _ in $(seq 1 $(( g * 10 ))); do alive_recorded "$1" || return 0; sleep 0.1; done
  alive_recorded "$1" || return 0
  kill -KILL -- "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null || true
  return 0
}
# pid_alive <name> <pid> and pid_stop <name> <pid>: under run.sh, whose background processes are its children, kill -0
# and stop_group on the pid, as before step.sh; under step.sh, the guard on the record <name>.
pid_alive() { if [[ "${driver:-run.sh}" == step.sh ]]; then alive_recorded "$1"; else kill -0 "$2" 2>/dev/null; fi; }
pid_stop() { if [[ "${driver:-run.sh}" == step.sh ]]; then stop_recorded "$1"; else stop_group "$2"; fi; }

# The host, as the gate reads it.
pool_busy() { # laugd's busy cells (being created, reset or destroyed); fails when laugd does not answer with its status
  curl -sf -m 3 "$pool_url/status" 2>/dev/null | jq -e 'if type == "object" and has("cells") then [(.cells // [])[] | select(.state == "busy")] | length else error("not laugd") end' 2>/dev/null
}
projects_up() { # projects_up [-a]: the compose projects up on this host (with -a, stopped ones too), one a line, sorted
  # Only stdout is parsed: a warning the docker CLI prints on stderr while it succeeds (a CLI plugin it cannot load, a
  # context it warns about) must not read as a failed call. stderr is shown only when the call fails.
  local out names err rc=0
  err="$(mktemp)" || return 1
  out="$(timeout 20 docker compose ls ${1:+"$1"} --format json 2>"$err")" || rc=$?
  if (( rc != 0 )); then
    echo "docker compose ls exited $rc$( [[ ! -s "$err" ]] || echo ": $(tail -1 "$err")")" >&2; rm -f "$err"; return 1
  fi
  # Seen on the dev host: each entry names its project in Name (ConfigFiles, Name, Status: compose 5.1.0), and
  # docker compose ls --format json printed [] on an empty host (compose 5.1.4)
  if ! jq -e 'type == "array"' <<<"$out" >/dev/null 2>&1; then
    echo "docker compose ls printed no JSON list: $(head -1 <<<"$out" | cut -c1-200)$( [[ ! -s "$err" ]] || echo "; stderr: $(tail -1 "$err")")" >&2
    rm -f "$err"; return 1
  fi
  rm -f "$err"
  names="$(jq -r '.[] | .Name | strings' <<<"$out")" || return 1
  [[ -z "$names" ]] || sort -u <<<"$names"
}
# A gate's results may be committed, so its records and messages name laug-* compose projects only and count the
# others, which are not laug's to name. shown_projects (stdin): every other name becomes one "(not laug's)".
# projects_text <names>: the laug-* ones by name, then how many others.
shown_projects() { awk 'NF { print ($0 ~ /^laug-/ ? $0 : "(not laug\047s)") }' | sort -u; }
projects_text() {
  awk 'NF { if ($0 ~ /^laug-/) l = l (l == "" ? "" : " ") $0; else n++ }
    END { if (l != "" && n) print l ", and " n " project(s) laug did not start"; else if (l != "") print l
          else if (n) print n " project(s) laug did not start"; else print "none" }' <<<"$1"
}
# host_check <declared> <vcpu> <mem_b> <set host JSON>: why a host that declares <declared> (- for none) and has <vcpu>
# vCPU and <mem_b> bytes (free -b's total) is not the host its threshold set names, one line; nothing when it is. vcpu
# is the set's exactly; memory, in whole GiB as gate.json records it, within 1 of the set's. The declared line is never
# printed: a line names the set's type only.
host_check() {
  local decl="$1" v="$2" m=$(( $3 / 1073741824 )) t sv sm
  t="$(jq -r .type <<<"$4")"; sv="$(jq -r .vcpu <<<"$4")"; sm="$(jq -r .mem_gib <<<"$4")"
  if [[ "$decl" == - ]]; then
    echo "this host declares no host type, so it cannot be seen to be the $t ($sv vCPU, $sm GiB) load/steady-state.yaml was measured on; ${LAUG:-bin/laug} host type -declare <type> declares one"
  elif [[ ! "$decl" =~ ^[a-z0-9][a-z0-9.-]{0,31}$ ]]; then
    echo "the host type declared in (outside the repository)/host-type is not a type name"
  elif [[ "$decl" != "$t" ]]; then
    echo "this host's declared type is not $t, the type load/steady-state.yaml was measured on: its numbers judge no other host type (docs/DESIGN.md section 7, the R6 paragraph)"
  elif (( v != sv || m - sm > 1 || sm - m > 1 )); then
    echo "this host is declared $t and has $v vCPU and $m GiB, and load/steady-state.yaml's $t has $sv vCPU and $sm GiB"
  fi
}
# host_reasons: preflight's host type check, after the size check: host_check of this host's declaration, vcpu and
# mem_b against thresholds.json's resolved.host; its line, if any, joins reasons. Sets host_type, the type gate.json
# records, as JSON: the set's type when the check holds, "other" when not, null with no thresholds.json to compare.
# The declaration under GATE_FAKE=1 is FAKE_GATE_HOST_TYPE, "-" for none, or load/testdata/gate/host-type's line when
# it is unset, so make harness-test never reads the host's own.
host_reasons() {
  local want decl f r
  host_type=null
  want="$(jq -c '.resolved.host // empty' "$dir/thresholds.json" 2>/dev/null)" || want=""
  [[ -n "$want" ]] || return 0
  if [[ "$fake" == 1 ]]; then decl="${FAKE_GATE_HOST_TYPE-$(head -1 "$root/load/testdata/gate/host-type" 2>/dev/null)}"
  else f="${XDG_CONFIG_HOME:-${HOME:-}/.config}/laug/host-type"; decl=""; [[ ! -s "$f" ]] || decl="$(head -1 "$f")"; fi
  [[ -n "$decl" ]] || decl=-
  r="$(host_check "$decl" "$vcpu" "$mem_b" "$want")"
  if [[ -n "$r" ]]; then reasons+=("$r"); host_type='"other"'; else host_type="$(jq -c .type <<<"$want")"; fi
}
pressure() { # /proc/pressure's avg10 of some and full for cpu, memory and io; null where the kernel has none
  local r k a rest inner json=""
  for r in cpu memory io; do
    [[ -r "/proc/pressure/$r" ]] || continue
    inner=""; while read -r k a rest; do inner+="\"$k\":${a#avg10=},"; done < "/proc/pressure/$r"
    json+="\"$r\":{${inner%,}},"
  done
  if [[ -n "$json" ]]; then jq -c . <<<"{${json%,}}" 2>/dev/null || echo null; else echo null; fi
}
poll_once() { # one line of poll.jsonl: {wall, mono, busy, projects, pressure}; null for what did not answer
  local busy projects
  busy="$(pool_busy)" || busy=null
  projects="$(projects_up 2>/dev/null | shown_projects | jq -Rcs 'split("\n") | map(select(. != ""))')" || projects=null
  jq -cn --argjson t "$(stamp)" --argjson busy "$busy" --argjson p "$projects" --argjson pr "$(pressure)" '$t + {busy: $busy, projects: $p, pressure: $pr}'
}
leased_projects() { local k; for k in "${keys[@]}"; do jq -r '.project // ("laug-" + .id)' "$dir/$k/lease.json" 2>/dev/null; done | sort -u; }
own_status() { # own_status <status JSON>: whether a /status answer is the gate's own laugd's (step 3: max_lease is its mark)
  [[ -n "$max_lease" ]] && jq -e --arg ml "$max_lease" 'type == "object" and .max_lease == $ml' <<<"$1" >/dev/null 2>&1
}
own_laugd_why() { # why the laugd on GATE_POOL_LISTEN is not the one this gate started, with its settings; nothing when it is
  local st
  [[ -n "$laugd_pid" ]] && pid_alive laugd "$laugd_pid" \
    || { echo "the gate's own laugd exited: $(tail -1 "$dir/laugd.log" 2>/dev/null)"; return 0; }
  st="$(curl -sf -m 3 "$pool_url/status" 2>/dev/null)" || { echo "laugd did not answer /status"; return 0; }
  own_status "$st" || { echo "what answers there is another laugd: max_lease $(jq -r '.max_lease // "unknown"' <<<"$st" 2>/dev/null || echo unknown), not the gate's $max_lease"; return 0; }
  jq -r --argjson n "${#keys[@]}" 'if .min_warm != 0 or .max_cells != $n or .quota != $n
    then "it runs min \(.min_warm), max \(.max_cells), quota \(.quota), not min 0, max \($n), quota \($n)" else empty end' <<<"$st"
}
wave_time_why() { # why too little is left before the watchdog for the wave to end before it; nothing when enough is
  local left=$(( (t0_ms + DEADLINE_S * 1000 - $(now_ms)) / 1000 ))
  (( left >= WAVE_MIN_S )) || echo "$left s left before the watchdog at deadline_s ($DEADLINE_S s from the gate start), under the plan's wave_min_s ($WAVE_MIN_S s)"
}
quiet_why() { # why the host is not quiet for the wave yet; nothing when it is
  local busy up want
  busy="$(pool_busy)" || { echo "laugd at $GATE_POOL_LISTEN did not answer /status"; return 0; }
  (( busy == 0 )) || { echo "laugd shows $busy busy cell(s)"; return 0; }
  up="$(projects_up 2>&1)" || { echo "docker compose ls failed: $up"; return 0; }
  want="$(leased_projects)"
  [[ "$up" == "$want" ]] || echo "compose projects up: $(projects_text "$up"); the gate leased $(paste -sd ' ' <<<"$want")"
}
nap() { sleep "$1" & bg_pid=$!; wait "$bg_pid" || true; bg_pid=""; }   # a sleep that a signal to the gate cuts short

# Items.
write_exit() { # write_exit <key>: <key>/exit.json, the item's own exit and how it was stopped, with its wiring
  local k="$1" le=null
  [[ "$(cat "$dir/$k/load.exit" 2>/dev/null)" =~ ^[0-9]+$ ]] && le="$(cat "$dir/$k/load.exit")"
  jq -n --argjson exit "${item_exit[$k]}" --arg stopped "${item_stopped[$k]:-}" --argjson start "${item_start[$k]}" \
    --argjson end "${item_end[$k]}" --argjson w "$(jq -c --arg k "$k" '.[] | select(.key == $k) | del(.key)' <<<"$wiring")" --argjson le "$le" \
    '{exit: $exit, stopped_by: (if $stopped == "" then null else $stopped end), start: $start, end: $end} + $w
     + (if $le == null then {} else {load_exit: $le} end)' > "$dir/$k/exit.json"
}
reap() { # reap <key>: an item that ended: its exit and end, exit.json
  local k="$1" rc=0
  wait "${item_pid[$k]}" || rc=$?
  item_end[$k]="$(stamp)"; item_exit[$k]="$rc"; write_exit "$k"; event "ended $k"
  say "item $k" "exit $rc$([[ -z "${item_stopped[$k]:-}" ]] || echo " after ${item_stopped[$k]}")"
}
live_items() { local k; for k in "${keys[@]}"; do [[ -n "${item_pid[$k]:-}" && -z "${item_end[$k]:-}" ]] && echo "$k"; done; return 0; }
signal_live() { # signal_live TERM|KILL: to the process group of every item still running
  local k
  for k in $(live_items); do
    kill -s "$1" -- "-${item_pid[$k]}" 2>/dev/null || kill -s "$1" "${item_pid[$k]}" 2>/dev/null || true
    item_stopped[$k]="$1"
  done
  event "watchdog $1"; say watchdog "$1 to $(live_items | paste -sd ' ')"
}
watch_items() { # watch_items <epoch ms>: reap items as they end; at that time TERM the live ones, KILL_GRACE_S s later KILL, each with a stderr line of the epoch ms it acted on and of the look before
  local dl="$1" k t="" prev="" term_ms="" kill_ms=""
  while :; do
    for k in $(live_items); do kill -0 "${item_pid[$k]}" 2>/dev/null || reap "$k"; done
    [[ -n "$(live_items)" ]] || return 0
    prev="$t"; t="$(now_ms)"
    if [[ -z "$term_ms" ]] && (( t >= dl )); then term_ms="$t"; signal_live TERM; echo "watchdog: TERM at $t, deadline $dl, look before ${prev:-none}, gate start ${t0_ms:-unknown} (epoch ms)" >&2; fi
    if [[ -n "$term_ms" && -z "$kill_ms" ]] && (( t >= term_ms + KILL_GRACE_S * 1000 )); then kill_ms="$t"; signal_live KILL; echo "watchdog: KILL at $t, TERM at $term_ms, look before ${prev:-none}, kill_grace_s $KILL_GRACE_S (epoch ms)" >&2; fi
    if [[ -n "$kill_ms" ]] && (( t >= kill_ms + 10000 )); then   # a process KILL does not end: recorded, and left
      for k in $(live_items); do item_end[$k]="$(stamp)"; item_exit[$k]=null; write_exit "$k"; say "item $k" "still there 10 s after KILL; left"; done
      return 0
    fi
    sleep 0.5
  done
}
# sweep_item <key> [<pid> [<start>]]: after a stop, TERM to what the item <key> left running, by session; a say line
# names the match when anything matched. The item ran under setsid (launch_item), so <pid> is its session's id, which
# the session keeps while any process in it runs, its leader gone or not (seen: a timeout's group outlived
# a KILL to the leader's group, and pgrep -s listed it; kernel/pid.c frees no pid a session still uses). What
# the item started without setsid is in it, a group of its own included: Omes starts its worker in a group of its own,
# with cmd.Environ() (omes f0de206 internal/workerctl/run.go:145-146). faults/run.sh starts its load under
# setsid, in a session of its own, which is TERMed too when a process in it names run id
# <gate id>-<key> on its command line (Omes's --run-id and task queue) and holds the item's LEASE_FILE in its
# environment: faults/run.sh hands its load the item's; go run hands LEASE_FILE on, and so did a child started as Omes
# starts its worker (seen on go 1.24.1). Another gate run with this gate id, from a results directory of
# its own in the same second, gives its items another LEASE_FILE; two checkouts with one relative GATE_RESULTS give
# the same, and the host lock keeps their waves apart. <start> is the record's (step.sh): a <pid> that is another
# process now names no session of the item's. run.sh gives none: it sweeps within about 10 s of reaping the item, and
# a pid comes back only once the kernel's counter wraps (pid_max 4194304 on the dev host; kernel/pid.c
# allocates cyclically). With no pid (step.sh: a wave step that ended between an item's launch and its record), by
# LEASE_FILE alone: the session of every process whose environment holds the item's, the item among them, as
# launch_item gives it one; a name is no test here, since faults/run.sh's own command line names the lease file's
# path, <gate id>/<key>, and no run id.
sweep_item() {
  local k="$1" p="${2:-}" s="${3:-}" rx="$gate_id-$1([^a-z0-9-]|\$)" lf c f x o me list own=0 n how=""
  local -a sids=()
  lf="LEASE_FILE=$(rel "$dir/$k/.lease.json")"; me="$(sid_of $$)" || me=""
  if [[ ! "$p" =~ ^[1-9][0-9]*$ ]]; then
    for f in $(grep -lzxF -- "$lf" /proc/[0-9]*/environ 2>/dev/null); do
      c="${f#/proc/}"; c="${c%/environ}"
      x="$(sid_of "$c")" && [[ "$x" != "$me" && " ${sids[*]} " != *" $x "* ]] || continue
      sids+=("$x")
    done
    (( ${#sids[@]} > 0 )) || return 0
    list="$(printf '%s\n' "${sids[@]}" | sort -n | paste -sd ,)"   # the glob gives /proc's entries in text order
    pkill -TERM -s "$list" 2>/dev/null && say "item $k" "TERM by LEASE_FILE, no pid recorded: what is left in session(s) $list, which hold the item's" || true
    return 0
  fi
  if o="$(proc_stat "$p")" && [[ -n "$s" && "${o#* }" != "$s" ]]; then how="pid $p is another process now"; else sids+=("$p"); own=1; fi
  for c in $(pgrep -f -- "$rx" 2>/dev/null); do
    x="$(sid_of "$c")" && [[ "$x" != "$me" && " ${sids[*]} " != *" $x "* ]] || continue
    ! grep -qzxF -- "$lf" "/proc/$c/environ" 2>/dev/null || sids+=("$x")
  done
  (( ${#sids[@]} > 0 )) || return 0
  n=$(( ${#sids[@]} - own )); (( ! own )) || how="the item's"
  (( n == 0 )) || how="${how:+$how, and }$n by run id $gate_id-$k and its LEASE_FILE"
  printf -v list '%s,' "${sids[@]}"; list="${list%,}"
  pkill -TERM -s "$list" 2>/dev/null && say "item $k" "TERM by session to what is left in session(s) $list: $how" || true
}
sweep_orphans() { # after a KILL: faults/run.sh runs its load in a session of its own, which a KILL to run.sh leaves running
  local k
  for k in "${keys[@]}"; do
    [[ "${item_stopped[$k]:-}" == KILL ]] || continue
    sweep_item "$k" "${item_pid[$k]:-}"
  done
}
# item_tag <key>: the server tag the item's cell runs: plan.json's incumbent for the item with arm incumbent, else
# SERVER_TAG. item_tag_of <key>: what names it, for a message: "the plan's incumbent" or "the gate SERVER_TAG".
item_tag() { jq -r --arg k "$1" --arg t "$SERVER_TAG" 'if ([.items[] | select(.key == $k) | .arm][0] // "") == "incumbent" then .incumbent else $t end' <<<"$plan"; }
item_tag_of() { if [[ "$(jq -r --arg k "$1" '[.items[] | select(.key == $k) | .arm][0] // ""' <<<"$plan")" == incumbent ]]; then echo "the plan's incumbent"; else echo "the gate SERVER_TAG"; fi; }
# incumbent_reasons: at preflight, for a plan that names an incumbent: thresholds-incumbent.json, load/thresholds.sh
# <incumbent> as JSON, the set the incumbent's verdict is judged by (R6 and R7 read it), and, when the incumbent is not
# SERVER_TAG, its images as the release's are checked; each failure joins reasons. Nothing for a plan with no incumbent
# or no plan.
incumbent_reasons() {
  local inc th images missing img
  [[ -n "$plan" ]] || return 0
  inc="$(jq -r '.incumbent // empty' <<<"$plan")"; [[ -n "$inc" ]] || return 0
  if th="$(load/thresholds.sh "$inc" 2>&1)"; then yq -o=json '.' <<<"$th" > "$dir/thresholds-incumbent.json"
  else reasons+=("load/thresholds.sh does not resolve the plan's incumbent $inc: $(tail -1 <<<"$th")"); fi
  [[ "$inc" != "$SERVER_TAG" ]] || return 0
  if images="$(SERVER_TAG="$inc" timeout 30 docker compose -p laug-gate-preflight --project-directory cells/compose \
       -f cells/compose/cell.yml config --images 2>"$dir/.images.err" | sort -u)" && [[ -n "$images" ]]; then
    missing=(); while IFS= read -r img; do docker image inspect "$img" >/dev/null 2>&1 || missing+=("$img"); done <<<"$images"
    (( ${#missing[@]} == 0 )) || reasons+=("the plan's incumbent $inc is not pulled on this host: ${missing[*]} (the cell's 8 images at 1.31.0 saved to a 778238976-byte tar: cells/results/2026-10-23-boot1.txt, A1)")
  else reasons+=("docker compose config --images on cells/compose/cell.yml at the plan's incumbent $inc failed: $(tail -1 "$dir/.images.err")"); fi
  rm -f "$dir/.images.err"
}
# judge_input: preflight's last input, the judge, after the files run.sh's loop records: bin/laug judge -identity -json
# as {path: "internal/judge", sha256, changed_vs_base, version, go, tree_clean}, the sha256 of its sources and
# changed_vs_base git diff --quiet's against base_commit over internal/judge and internal/jsonx. bin/laug judge -write
# judges the run (finish), so when -identity fails a reason joins reasons and no entry is recorded. That refusal does
# not end as class refused: but for a write error on its stdout, -identity fails only for a bin/laug that cannot judge
# either (none, exit 127, or one built before laug judge, exit 2), so the judge step that refuse leads to fails too, and
# the run ends as the trap verdict at combine (trap_verdict: BLOCK, class invalid), its refused naming why (make
# harness-test pins it for both drivers). A gate on cells refuses too when bin/laug was not built from HEAD (-dirty or
# not), when git status lists a change under internal/judge or internal/jsonx, or when bin/laug's judge is not the one
# they hold (tree_clean not true). A gate on fakes (GATE_FAKE=1) records the entry as it is, so make harness-test and
# the orchestrator's tests run a judge edit before it is committed.
judge_input() {
  local id rc=0 changed c h t
  id="$(bin/laug judge -identity -json 2>/dev/null)" || rc=$?
  if (( rc != 0 )) || ! jq -e 'type == "object" and (.version | type) == "string" and (.sha256 | type) == "string"
      and (.go | type) == "string" and (.commit | type) == "string"' <<<"$id" >/dev/null 2>&1; then
    reasons+=("bin/laug judge -identity failed (exit $rc), so no judge can be recorded or run: make build builds bin/laug")
    return 0
  fi
  if git diff --quiet "$base_commit" -- internal/judge internal/jsonx 2>/dev/null; then changed=false; else changed=true; fi
  inputs+=("$(jq -c --argjson c "$changed" '{path: "internal/judge", sha256, changed_vs_base: $c, version, go, tree_clean}' <<<"$id")")
  [[ "$fake" != 1 ]] || return 0
  c="$(jq -r .commit <<<"$id")"; h="$(git rev-parse HEAD 2>/dev/null)" || h=none; t="$(jq -c .tree_clean <<<"$id")"
  if [[ "${c%-dirty}" != "$h" ]]; then reasons+=("bin/laug was built from ${c:0:7}, and HEAD is ${h:0:7}: make build")
  elif [[ -n "$(git status --porcelain -- internal/judge internal/jsonx 2>&1)" ]]; then
    reasons+=("internal/judge or internal/jsonx has changes not committed, and a gate on cells is judged by HEAD's judge: commit them, then make build")
  elif [[ "$t" != true ]]; then
    reasons+=("bin/laug's judge is not the one internal/judge and internal/jsonx hold (judge -identity says tree_clean $t): make build")
  fi
}
launch_item() { # launch_item <index> <key>: the item's script under setsid, in a session and process group of its own
  local i="$1" k="$2" kd="$dir/$2" it kind scen run_id="$gate_id-$2" lease="$dir/$2/.lease.json"
  local -a env_ cmd
  it="$(jq -c --arg k "$k" '.items[] | select(.key == $k)' <<<"$plan")"
  kind="$(jq -r .kind <<<"$it")"; scen="$(jq -r .scenario <<<"$it")"
  env_=(RUN_ID="$run_id" RESULTS_DIR="$(rel "$kd")" LEASE_FILE="$(rel "$lease")" POOL_ADDR="$pool_url" POOL_QUIET_WAIT_S=0
        OMES_DIR="$OMES_DIR" WORKER_METRICS_ADDR="127.0.0.1:$(( 19100 + 2 * i ))" CLIENT_METRICS_ADDR="127.0.0.1:$(( 19101 + 2 * i ))")
  if [[ "$kind" == load ]]; then
    env_+=(CELL_ADDRESS="$(jq -r .address "$lease")" PROM_URL="$(jq -r '.prometheus // ""' "$lease")" PROJECT="$(jq -r '.project // ""' "$lease")"
           SERVER_TAG="$(jq -r .server_tag "$lease")" NAMESPACE=default DURATION="$(jq -r .duration <<<"$it")" RATE="$(jq -r .rate <<<"$it")"
           MAX_CONCURRENT="$(jq -r .max_concurrent <<<"$it")" VERDICT_JSON="$(rel "$kd/$run_id.verdict.json")")
    # shellcheck disable=SC2016  # expanded by the item's own bash
    cmd=(bash -c 'rc=0; load/run-omes.sh "$1" || rc=$?; echo "$rc" > "$2"; exec load/verdict.sh "$3"' gate-load "$scen" "$(rel "$kd/load.exit")" "$run_id")
  else
    [[ "$(jq -r '.rate // ""' <<<"$it")" == "" ]] || env_+=(RATE="$(jq -r .rate <<<"$it")")
    [[ "$(jq -r '.max_concurrent // ""' <<<"$it")" == "" ]] || env_+=(MAX_CONCURRENT="$(jq -r .max_concurrent <<<"$it")")
    cmd=(faults/run.sh "$scen" "$(jq -r .fault <<<"$it")" "$(rel "$lease")")
  fi
  [[ "$fake" != 1 ]] || cmd=("$root/load/testdata/gate/fake-item.sh" "$kind" "$k" "$scen" "$(jq -r '.fault // "-"' <<<"$it")" "$(rel "$lease")")
  item_start[$k]="$(stamp)"
  "${sigdefault[@]}" "${env_[@]}" setsid "${cmd[@]}" > "$kd/item.log" 2>&1 8>&- 9>&- &
  item_pid[$k]=$!
}

# The background processes.
start_poller() {
  # Each pass first checks that the poller's parent is still the gate (field 4 of /proc/<pid>/stat, after the
  # command name in parentheses): a gate sent KILL runs no EXIT trap, and its poller, reparented, would poll forever.
  local gate_pid=$$   # in the subshell, $$ is still the gate's pid, and BASHPID its own
  ( trap - EXIT INT HUP ERR; set +eE; s=""
    trap '[[ -z "$s" ]] || kill "$s" 2>/dev/null; exit 0' TERM
    gate_alive() { local st rest; read -r st < "/proc/$BASHPID/stat" 2>/dev/null || { kill -0 "$gate_pid" 2>/dev/null; return; }
      rest="${st##*) }"; set -- $rest; [[ "${2:-}" == "$gate_pid" ]]; }
    while gate_alive; do
      sleep "$POLL_S" & s=$!; wait "$s"; s=""
      gate_alive || break
      poll_once >> "$dir/poll.jsonl"
    done ) 8>&- 9>&- &
  poller_pid=$!
}
stop_poller() { [[ -n "$poller_pid" ]] || return 0; kill -TERM "$poller_pid" 2>/dev/null || true; wait "$poller_pid" 2>/dev/null || true; poller_pid=""; }
start_harness() {
  # shellcheck disable=SC2016  # expanded by the wrapper's own bash
  setsid "${sigdefault[@]}" bash -c 's=$(date +%s.%N); "${@:2}"; rc=$?; echo "$rc $s $(date +%s.%N)" > "$1"; exit "$rc"' gate-harness "$dir/.harness" \
    "${harness_cmd[@]}" > "$dir/harness.log" 2>&1 8>&- 9>&- &
  harness_pid=$!; harness_start_ms="$(now_ms)"
}
stop_group() { # stop_group <pid>: TERM to its process group, 5 s, then KILL
  local p="$1" _
  kill -TERM -- "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null || true
  for _ in $(seq 1 50); do kill -0 "$p" 2>/dev/null || break; sleep 0.1; done
  kill -KILL -- "-$p" 2>/dev/null || true
  wait "$p" 2>/dev/null || true
}
wait_harness() { # sets harness_rc and writes harness.json: make harness-test's exit, its seconds and its last tally
  local limit=$(( harness_start_ms + HARNESS_TIMEOUT_S * 1000 )) timed_out=false rc=0 hrc s e secs=null
  while pid_alive harness "$harness_pid"; do
    if (( $(now_ms) >= limit )); then timed_out=true; break; fi
    sleep 0.5
  done
  if [[ "$timed_out" == true ]]; then pid_stop harness "$harness_pid"; hrc=124; secs="$HARNESS_TIMEOUT_S"
  else
    wait "$harness_pid" 2>/dev/null || rc=$?
    if [[ -s "$dir/.harness" ]] && read -r hrc s e < "$dir/.harness" && [[ "$hrc" =~ ^[0-9]+$ ]]; then secs="$(awk -v s="$s" -v e="$e" 'BEGIN { printf "%.1f", e - s }')"
    else hrc="$rc"; fi
  fi
  harness_pid=""; rm -f "$dir/.harness"
  jq -n --argjson exit "$hrc" --argjson secs "$secs" --argjson to "$timed_out" \
    --arg tally "$(grep -E '^[0-9]+ passed, [0-9]+ failed$' "$dir/harness.log" 2>/dev/null | tail -1)" \
    '{exit: $exit, seconds: $secs, timed_out: $to, tally: (if $tally == "" then null else $tally end)}' > "$dir/harness.json"
  harness_rc="$hrc"
}

release_id() { # release_id <id>: POST /release to the gate's laugd; on a refusal, prints laugd's error and fails
  local out
  out="$(curl -s --fail-with-body -m 330 -X POST "$pool_url/release" -H 'content-type: application/json' -d "{\"id\":\"$1\"}" 2>&1)" && return 0
  jq -r '.error // empty' <<<"$out" 2>/dev/null || true
  return 1
}
cleanup() { # step 11, off the clock: release every leased cell, stop laugd, record what is left; Ctrl-C cannot cut it short
  cleaned=1; set +e; trap '' INT TERM HUP
  mark release
  local k id out released=() errs=() state="not started" left st busy said=0 until_ms mine=()
  if [[ -n "$laugd_pid" ]]; then
    if pid_alive laugd "$laugd_pid"; then
      for k in "${keys[@]}"; do
        id="$(jq -r '.id // empty' "$dir/$k/.lease.json" 2>/dev/null)"; [[ "$id" =~ ^[0-9a-f]{8}$ ]] || continue
        if out="$(release_id "$id")"; then released+=("$id"); else errs+=("$id: $out"); fi
      done
      # A cold create under way when the gate stopped (a signal during a lease kills the lease's curl): laugd finishes
      # it, and gives the cell back itself once it sees the client has gone (handleLease). Stopping laugd before that
      # would kill cells/backend-compose.sh mid-create and leave a half-built cell. So: until laugd has no busy cell
      # and none leased to this gate (those are released here), 330 s at most, laugd's 300 s create timeout and a
      # destroy. Only while what answers is the gate's own laugd.
      until_ms=$(( $(now_ms) + 330000 ))
      while st="$(curl -sf -m 3 "$pool_url/status" 2>/dev/null)" && own_status "$st"; do
        busy="$(jq '[(.cells // [])[] | select(.state == "busy")] | length' <<<"$st" 2>/dev/null)"
        mapfile -t mine < <(jq -r --arg p "gate-$gate_id-" '(.cells // [])[] | select(.state == "leased" and ((.requester // "") | startswith($p))) | .id' <<<"$st" 2>/dev/null)
        (( ${busy:-0} == 0 && ${#mine[@]} == 0 )) && break
        if (( $(now_ms) >= until_ms )); then errs+=("laugd still had ${busy:-0} busy cell(s) and ${#mine[@]} leased to this gate 330 s on"); break; fi
        for id in "${mine[@]}"; do release_id "$id" >/dev/null && released+=("$id"); done   # a refusal: laugd's own give-back won
        (( said )) || { say release "laugd has ${busy:-0} busy cell(s), a create or destroy under way: waiting for it to end, 330 s at most"; said=1; }
        sleep 1
      done
      pid_stop laugd "$laugd_pid"; state=stopped
    else wait "$laugd_pid" 2>/dev/null; state="exited before the release"; fi
    event "laugd stopped"
  fi
  # left: the laug-* projects up now that were not up at preflight, so what this gate left, never what another gate
  # or laug pool serve runs.
  left="$(projects_up -a 2>/dev/null | { grep '^laug-' || true; } | jq -Rcs --argjson b "$at_preflight" \
    'split("\n") | map(select(. != "" and (. as $p | any($b[]; . == $p) | not)))')" || left=null
  jq -n --argjson t "$(stamp)" --argjson released "$(printf '%s\n' "${released[@]+"${released[@]}"}" | jq -Rcs 'split("\n") | map(select(. != ""))')" \
    --argjson errs "$(printf '%s\n' "${errs[@]+"${errs[@]}"}" | jq -Rcs 'split("\n") | map(select(. != ""))')" --arg laugd "$state" \
    --argjson left "${left:-null}" --argjson before "$at_preflight" \
    '{at: $t, released: $released, release_errors: $errs, laugd: $laugd, left: $left, up_at_preflight: $before}' > "$dir/cleanup.json"
  say release "$(( ${#released[@]} )) cell(s) released$( (( ${#errs[@]} == 0 )) || echo ", ${#errs[@]} refused: ${errs[*]}"); laugd $state; laug-* projects this gate left: $(jq -r 'if . == null then "unknown (docker compose ls failed)" elif length == 0 then "none" else join(" ") end' <<<"${left:-null}")"
  # The advice only from a gate that held the locks and started laugd: anything else's laug-* projects are not its own.
  if (( locked )) && [[ -n "$laugd_pid" && "${left:-null}" != null && "$left" != "[]" ]]; then
    say release "${LAUG:-bin/laug} pool down -remove-cells removes every laug-* project, volumes included"
  fi
}
refuse() { # refuse <reason>...: the gate will not run its wave; gate.json says why, and the judge judges BLOCK, class refused
  local r; r="$(printf '%s; ' "$@")"; r="${r%; }"
  # A make harness-test that ended is recorded so R1 says how (a refusal at a lease comes after it, as in the dev host
  # run internal/rejudge/testdata/runs/g20261014-212013); one still running is stopped; R1 reads it as not seen to pass.
  [[ -z "$harness_pid" || ! -s "$dir/.harness" ]] || wait_harness
  jput "$dir/gate.json" --arg r "$r" '.refused = $r'
  say refused "$r"
  to_judge
}
# trap_verdict: the gate stopped before its judge judged: a BLOCK verdict.json and verdict.md that name the step.
trap_verdict() {
  local g c pl
  jput "$dir/clock.json" --argjson t "$(stamp)" '.t_post = $t' 2>/dev/null || printf '{"t0":%s,"t_post":%s}\n' "${t0:-null}" "$(stamp)" > "$dir/clock.json"
  g="$(jq -c . "$dir/gate.json" 2>/dev/null)" || g=null; c="$(jq -c . "$dir/clock.json" 2>/dev/null)" || c=null
  pl="$(jq -c . "$dir/plan.json" 2>/dev/null)" || pl=null; [[ -n "$pl" ]] || pl=null
  # The reason below and verdict.md's text still name gate/combine.sh, the shell judge that bin/laug judge replaced.
  # The committed trap verdicts hold those bytes, and internal/judge's RerenderTrap gives them back byte for byte in
  # three forms. New text would be a fourth form, a change to the judge's code: it waits for the judge's next version.
  jq -n --argjson g "$g" --argjson c "$c" --argjson pl "$pl" --arg id "$gate_id" --arg tag "$SERVER_TAG" --arg step "$step" --arg why "$why" \
    --argjson deadline "${DEADLINE_S:-null}" --arg drv "gate/${driver:-run.sh}" '
    ($g // {}) as $g | ($c // {}) as $c | ($pl // {}) as $pl
    | {gate_id: $id, verdict: "BLOCK", class: "invalid", rerun: false, class_if_quiet: null, refused: ($g.refused // null),
       fake: ($g.fake == true), server_tag: $tag, incumbent: ($pl.incumbent // null), laug_commit: ($g.laug_commit // null), tree_dirty: (if ($g.tree_dirty | type) == "boolean" then $g.tree_dirty else null end),
       base: ($g.base // null), inputs: ($g.inputs // null), deadline_s: $deadline,
       items_in_plan: ($pl.items_in_plan // null), items_limit: ($g.items_limit // null), t0: ($c.t0 // null), t_post: ($c.t_post // null),
       elapsed_s: (if ($c.t0.wall | type) == "number" and ($c.t_post.wall | type) == "number"
                   then {wall: ((($c.t_post.wall - $c.t0.wall) * 1000 | round) / 1000), mono: ((($c.t_post.mono - $c.t0.mono) * 1000 | round) / 1000)} else null end),
       host: ($g.host // null), rules: null,
       reasons: ["stopped: \($drv) stopped at step \($step) (\($why)) before gate/combine.sh judged; nothing was judged"],
       items: [], comparison: null, host_quiet: false, host_noise: ["the gate stopped before its verdict"], stopped_at: $step}' > "$dir/verdict.json" || return 1
  jq -r '"## laug · verdict for raun \(.laug_commit // "unknown")\(if .tree_dirty == true then " (tree dirty)" else "" end)\(if .fake then " (GATE_FAKE=1: fixtures, no cell ran)" else "" end)", "",
    "Gate run \(.gate_id) on server \(.server_tag) stopped at step \(.stopped_at) before gate/combine.sh judged it: \(.reasons[0]).", "",
    "Class: \(.class).", "", "**BLOCK**"' "$dir/verdict.json" > "$dir/verdict.md" || return 1
  combined=1
  echo "laug · gate $gate_id, server $SERVER_TAG: BLOCK, class invalid: stopped at step $step ($why)$([[ "$fake" != 1 ]] || echo " (GATE_FAKE=1: fixtures, no cell ran)")"
}
# base_pick: gate_env's base_commit, the commit BASE names. BASE is main unless the caller gives another. A clone of
# another branch, or a checkout at a detached HEAD, has no local main: then origin/main, else HEAD, and base_note says
# which, on the line that names the gate run. BASE decides only what gate.json records, base, base_ref and each
# input's changed_vs_base, which no rule reads (internal/judge). A BASE that is not a commit, given
# or the last fallback, is refused, naming -base.
base_pick() {
  local b
  base_note=""
  if base_commit="$(git rev-parse --verify --quiet --short "$BASE^{commit}")"; then return 0; fi
  [[ "$BASE" == main ]] || usage "BASE=$BASE is not a commit in this repository: ${LAUG:-bin/laug} gate run -base <commit> names one"
  for b in origin/main HEAD; do
    if base_commit="$(git rev-parse --verify --quiet --short "$b^{commit}")"; then BASE="$b"; break; fi
  done
  [[ -n "$base_commit" ]] || usage "BASE=main, origin/main and HEAD are not commits in this repository: ${LAUG:-bin/laug} gate run -base <commit> names one"
  base_note="BASE $BASE ($base_commit), as there is no local main; -base names another"
}
