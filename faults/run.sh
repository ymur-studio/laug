#!/usr/bin/env bash
# Fault harness. Three windows per run, read from faults/catalog.yaml:
#   before: load for steady_s; the last 60 s must pass every threshold, else "never steady" (run invalid)
#   during: inject; poll the fault's signal until it EXCEEDS (landed_at), else "did not land" (never ADVANCE; the fault
#           is removed before the record is written)
#   after:  remove; poll the signal back under max (recovered_at, ttr = recovered_at - remove_at, remove_at taken just
#           before the remove mechanism runs), else
#           "did not recover"; wait settle_s, then after_s more seconds must pass every threshold, else
#           "after window not clean". The drain, remove_at to the after window, is read and recorded, not judged:
#           a signal can recover seconds before clients do (kill-history, 2026-10-13: the after window opened at
#           +6 s on one run and +10 s on two, and only the +6 s run BLOCKed, most likely inside the drain).
# Lost work (load/lost-work.sh), judged at the end of the after window:
#   failed iterations: Omes's log, bucketed by the time each failure was logged into before, during, drain and after;
#           any in the after window BLOCKs. The others are recorded: a fault is expected to fail iterations. One
#           whose error says "workflow execution not found" BLOCKs in any window but late: the server acknowledged
#           that workflow and then lost it.
#   sweep:  every workflow the load started more than SWEEP_MARGIN_S (30) s before the after window ended, by status
#           (load/sweep.sh, from the host against the lease address, which reaches a microVM cell too); anything but
#           SWEEP_EXPECTED (Completed), or no workflows at all, BLOCKs. A scenario whose workflows may run longer than
#           30 s needs a larger SWEEP_MARGIN_S, or the ones still Running BLOCK.
#   floor:  after-window completions below RATE x (after_s - 10) BLOCK. raun-fvoom1's after window completed 1199
#           workflows in 90 s at 20/s; it BLOCKed on its thresholds, and the floor alone would have.
# Soft assertions (cells/softasserts.sh): the server's own invariant checks that failed, logged and went on, counted
#   per role in the same four windows. Recorded, not judged yet.
# Readings: a catalog row's own queries (readings), read over the same four windows. Recorded, not judged.
# ADVANCE only if landed, recovered within budget_s, the after window is clean, and no work was lost after it.
# BLOCK names the failed step.
# The raun JSON records the server tag the cell runs (server_tag), read from its history container's image, and the
# load's run JSON gets the same tag. Rauns recorded before 2026-10-14 have no server_tag; every cell then ran 1.31.0.
# After server_tag it records the lease's backend (compose or firecracker), a microVM's firecracker_version and
# cache_type (null on compose) and host_virtualized, whether laug itself ran on a VM, where a microVM cell runs nested
# (faults/lib.sh, lease_read and host_virtualized). Rauns recorded before these keys show a microVM cell only in its
# 10.254. address.
# Shard faults (tdbg close, pg bump; faults/catalog.yaml says why each one does what it does) add four catalog fields:
#   signal_base: inject   the signal is read just before inject, and landed means its rise since then exceeds max; for
#                         a counter whose series may not exist until the fault makes it (increase() reads a series that
#                         first appears at 64 as no increase)
#   recover: shards-acquired, recover_from: inject|remove
#                         recovered means every shard the inject touched logged "Acquired shard" since inject or since
#                         removal (faults/lib.sh, shards_acquired), not the signal back under its max
#   cleanup               mechanisms run once the result is decided, before the JSON is written, whatever the result:
#                         a self-test whose remove does nothing puts the cell back here
#   self_test: <reason>   the fault is a self-test of the harness: the raun JSON's self_test says passed only for a BLOCK
#                         whose reason starts with <reason> and whose every touched shard logged, during the hold, that
#                         its acquire was refused (faults/lib.sh, shards_held). Without that line a BLOCK shows nothing:
#                         an undisturbed shard never logs "Acquired shard" either.
# Thresholds: the set for the cell's server release (faults/lib.sh, thresholds_resolve; load/thresholds.sh), resolved
# from the tag read below before any load: load/steady-state.yaml plus load/releases/<major>.<minor>.yaml when one
# exists, or THRESHOLDS as given when it is set. The before and after windows are judged on it, the during and drain
# windows read with it, and a signal that names a threshold takes its query and max from it. A reading the release file
# marks no_data: not_judged is judged against its max when it has data; with none it does not fail the window, and the
# raun JSON lists it under not_produced. The signal is the exception: no data there neither lands nor recovers, whatever
# the release file says, since a fault seen by nothing has not been seen. The raun JSON records thresholds_release,
# thresholds_note and overrides_applied (without a release file every raun on 1.32.1 would end "never steady" on
# activity schedule-to-start, which 1.32.1 does not produce; cells/results/2026-10-14-lab-1.32.1.txt).
# The pool (faults/lib.sh, pool_quiet): laugd builds a replacement cell on this host as a lease takes a warm one, and
# with POOL_MIN 1 that build falls in the before window of each raun of a batch (inference). So once every check has
# passed, and just before the load, the raun waits until laugd's /status (POOL_ADDR) shows no other cell busy,
# POOL_QUIET_WAIT_S (300) s at most, and records the wait as pool_quiet_wait_s. A cell still busy at the bound does not
# stop the raun; the leased cell no longer leased after the wait does (exit 2, no load).
# The raun JSON records, as pool_busy, the most busy cells one poll saw in each window: polled at each window boundary,
# and every 5 s while the raun waits for its fault to land and then to recover. Recorded, not judged: the summary warns
# when a window saw one. laugd that does not answer records null.
# Every mechanism is checked before the load starts (a verb run.sh does not know, a shard out of range, no admin-tools
# image or no shards row stops the run there; a serverfi row, serverfi_preflight), and each tdbg or pg run is recorded
# in the raun JSON under mechanisms, each serverfi recreate under server_fi.
# A mechanism that fails at inject or remove is recorded there too, with inject_error or remove_error: the raun is a
# BLOCK that names it, the JSON is written, and the exit is 2 (a harness failure, the cell's state unknown). A remove
# still owed when the result is decided (the fault never landed, or the inject failed) runs before the JSON, so its
# record is in it. A remove that recreates history (serverfi off) stays owed until it succeeds, so one that failed or
# was cut short by Ctrl-C or SIGTERM is run once more by the EXIT trap: a recreate with no LAUG_FI_* is safe to repeat.
# Every other remove is owed no more once it starts, as a pg restore that found another value is not safe to repeat.
# Should the script stop on anything else after inject, the EXIT trap removes the fault and writes a partial raun JSON
# (partial: true, the mechanisms, the exit) at the raun's path.
# Usage: faults/run.sh <scenario> <fault> [lease-file]   (the cell comes from the lease JSON: id, prometheus, toxiproxy)
set -euo pipefail

SCENARIO="${1:?scenario}"; FAULT="${2:?fault}"; LEASE_FILE="${3:-.last-lease.json}"
CATALOG="${CATALOG:-faults/catalog.yaml}"
# THRESHOLDS, when set, is the threshold set as given; unset, the cell's release picks it (thresholds_resolve, below).
RESULTS_DIR="${RESULTS_DIR:-faults/results}"
RUN_ID="${RUN_ID:-fault-$FAULT-$(date -u +%Y%m%d-%H%M%S)}"
RATE="${RATE:-20}"; MAX_CONCURRENT="${MAX_CONCURRENT:-50}"
SWEEP_MARGIN_S="${SWEEP_MARGIN_S:-30}"
[[ "$SWEEP_MARGIN_S" =~ ^[0-9]+$ ]] || { echo "SWEEP_MARGIN_S must be whole seconds, not '$SWEEP_MARGIN_S'" >&2; exit 2; }
[[ "${POOL_QUIET_WAIT_S:-300}" =~ ^(0|[1-9][0-9]*)$ ]] || { echo "POOL_QUIET_WAIT_S must be whole seconds, not '$POOL_QUIET_WAIT_S'" >&2; exit 2; }
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/.." && pwd)"
# shellcheck source=load/lost-work.sh
. "$root/load/lost-work.sh"
# The cell: lease, guest, containers, dynamic config and soft assertions, shared with faults/candidate.sh.
# shellcheck source=faults/lib.sh
. "$here/lib.sh"
mkdir -p "$RESULTS_DIR"
# Omes's task queue for this run (omes f0de206, loadgen/scenario.go:434). run-omes.sh writes into a directory of its
# own, LOAD_DIR: its run JSON would otherwise land on this raun's path, $RESULTS_DIR/$RUN_ID.json, whenever Omes
# exits on its own and this script stops before its final write. LOAD_LOG is Omes's log, which run-omes.sh tees.
TASK_QUEUE="omes-$RUN_ID"; LOAD_DIR="$RESULTS_DIR/load"; LOAD_LOG="$LOAD_DIR/$RUN_ID.log"

lease_read "$LEASE_FILE"
HOST_VIRTUALIZED="$(host_virtualized)"
export PROM_URL
assert_leased

cat_get() { yq -r ".faults[\"$FAULT\"].$1 // .defaults.$1 // \"\"" "$CATALOG"; }
[[ "$(yq -r ".faults[\"$FAULT\"]" "$CATALOG")" != null ]] || { echo "unknown fault: $FAULT" >&2; exit 2; }
STEADY_S="$(cat_get steady_s)"; HOLD_S="$(cat_get hold_s)"; AFTER_S="$(cat_get after_s)"; BUDGET_S="$(cat_get budget_s)"
SETTLE_S="$(cat_get settle_s)"; SETTLE_S="${SETTLE_S:-30}"
SIGNAL_RANGE="$(cat_get signal_range)"
# A signal that names a threshold takes its query and max from the release's threshold set, looked up once the server
# tag is read (below); signal_query and signal_max give a custom one.
signal_name="$(cat_get signal)"
SIGNAL_BASE="$(cat_get signal_base)"; RECOVER="$(cat_get recover)"; RECOVER="${RECOVER:-signal}"; RECOVER_FROM="$(cat_get recover_from)"
SELF_TEST="$(cat_get self_test)"
[[ -z "$SELF_TEST" || ( "$RECOVER" == shards-acquired && "$RECOVER_FROM" == remove ) ]] \
  || { echo "fault $FAULT: self_test needs recover: shards-acquired and recover_from: remove (a shard held to its removal)" >&2; exit 2; }
[[ -z "$SIGNAL_BASE" || "$SIGNAL_BASE" == inject ]] || { echo "fault $FAULT: signal_base is '$SIGNAL_BASE', not inject" >&2; exit 2; }
case "$RECOVER" in
  signal) [[ -z "$RECOVER_FROM" ]] || { echo "fault $FAULT: recover_from goes with recover: shards-acquired" >&2; exit 2; } ;;
  shards-acquired)
    [[ "$RECOVER_FROM" == inject || "$RECOVER_FROM" == remove ]] || { echo "fault $FAULT: recover_from is '$RECOVER_FROM', not inject or remove" >&2; exit 2; }
    [[ "$(cat_get inject)" =~ (^|;)[[:space:]]*(tdbg|pg)[[:space:]] ]] || { echo "fault $FAULT: recover: shards-acquired needs a tdbg or pg inject to name the shards" >&2; exit 2; } ;;
  *) echo "fault $FAULT: recover is '$RECOVER', not signal or shards-acquired" >&2; exit 2 ;;
esac
# A row's readings: name to PromQL query, each read over the before, during, drain and after windows at the window's
# end, every [1m] rewritten to the window's length as load/read-thresholds.sh does. Recorded in the raun JSON
# (readings), not judged: they keep the numbers a hypothesis predicts beyond its signal, such as range renewals or shard
# stops, which would otherwise go with the cell's Prometheus at release.
READINGS="$(yq -o json -I0 ".faults[\"$FAULT\"].readings // .defaults.readings // {}" "$CATALOG")"
jq -e 'type == "object" and all(to_entries[]; (.key | test("^[a-z][a-z0-9_]*$")) and (.value | type == "string" and length > 0))' \
  <<<"$READINGS" >/dev/null || { echo "$FAULT's readings must map lower_case names to PromQL strings, got $READINGS" >&2; exit 2; }

# Both forms of no data read as null: an empty result, and NaN (histogram_quantile over a range with no observations).
signal_raw() { curl -sf -G "$PROM_URL/api/v1/query" --data-urlencode "query=$SIGNAL_QUERY" | jq -r '.data.result[0].value[1] // "null" | if . == "NaN" then "null" else . end'; }
# With signal_base, the rise since the reading taken just before inject (base, set there).
base="null"
signal() {
  local v; v="$(signal_raw)" || return   # a failed query fails the caller, as it did before signal_base
  if [[ -n "$SIGNAL_BASE" && "$v" != null && "$base" != null ]]; then awk -v v="$v" -v b="$base" 'BEGIN { printf "%.10g\n", v - b }'; else echo "$v"; fi
}
exceeds() { [[ "$1" == null ]] && return 1; awk -v v="$1" -v m="$SIGNAL_MAX" 'BEGIN { exit !(v+0 > m+0) }'; }
# Recovery fails closed: a signal with no data (a dead process exports no series) is not recovered.
recovered() { [[ "$1" != null ]] && ! exceeds "$1"; }

# Mechanisms (container, dynconfig and the guest's docker are in faults/lib.sh).
toxic_add() { # toxic_add <type> key=val...
  # toxicity and stream are fields of the toxic itself; inside attributes toxiproxy drops them without an
  # error and applies the toxic to every connection (toxicity 1.0).
  local type="$1"; shift; local attrs="{}" top="{}" k v
  for kv in "$@"; do
    k="${kv%%=*}"; v="${kv#*=}"
    case "$k" in
      toxicity) top="$(jq -c --argjson v "$v" '.toxicity=$v' <<<"$top")" ;;
      stream) top="$(jq -c --arg v "$v" '.stream=$v' <<<"$top")" ;;
      *) attrs="$(jq -c --arg k "$k" --argjson v "$v" '.[$k]=$v' <<<"$attrs")" ;;
    esac
  done
  curl -sf -X POST "$TOXIPROXY/proxies/postgres/toxics" \
    -d "$(jq -c -n --arg t "$type" --argjson a "$attrs" --argjson top "$top" '{name:$t, type:$t, attributes:$a} + $top')" >/dev/null
}
toxic_remove() { curl -sf -X DELETE "$TOXIPROXY/proxies/postgres/toxics/$1" >/dev/null || true; }

# Shard mechanisms. They keep a raun's state in mech_dir (set once the load starts): shards, the ids an inject touched,
# one per line; pg-<id>, a bumped shard's range ids; records.jsonl, one object per run, which the raun JSON carries as
# mechanisms. With MECH_CHECK=1 each one checks its arguments and the cell it needs, and changes nothing.
MECH_CHECK=0; MECH_STEP=""; MECH_LINE=""
mech_record() { jq -c --arg step "$MECH_STEP" --arg m "$MECH_LINE" --argjson at "$(date +%s)" '{step: $step, mechanism: $m, at: $at} + .' <<<"$1" >> "$mech_dir/records.jsonl"; }
# tdbg close <k> <seed>: AdminService.CloseShard on k shards drawn from seed (faults/lib.sh, shard_draw), by tdbg in
# the cell's own admin-tools image on the cell's network: this host's Docker for a compose cell, the guest's for a
# microVM cell. CloseShard goes frontend, history, controller.CloseShardByID, which takes the shard's context out of
# the map and stops it (server 1.31.0 service/frontend/admin_handler.go:853-861, service/history/handler.go:870-873,
# shard/controller_impl.go:189-200). The next request for the shard, or the controller's acquire pass every
# history.acquireShardInterval (1 m; common/dynamicconfig/constants.go:1688-1692, shard/ownership.go:79-90), builds a
# new context, which acquires the shard again (read in source). The flags are seen: tdbg --address
# <host:port> shard close-shard --shard-id N (tdbg 1.31.0 --help). One container makes every call and
# prints "closed N", or "failed N <error>", per shard. --pull never: the guest has no route to a registry and holds the
# images its build loaded (the guest's docker.io takes it: close-shards-2 on a microVM cell,
# cells/results/2026-10-29-boot9.txt:383-384). TDBG_IMAGE is set once the cell's server tag is read, below: the
# admin-tools image at that tag, the one the cell's own schema setup ran.
TDBG_ADDRESS=temporal-nginx:7233
tdbg_close() {
  local k="$1" seed="$2" ids net out="" rc=0 closed failed idv=()
  ids="$(shard_draw "$k" "$seed")" || return 2
  mapfile -t idv <<<"$ids"   # mechanism splits on spaces only, so the ids go as an array
  net="$(cell_network)" || return 2
  if (( MECH_CHECK )); then
    dk image inspect "$TDBG_IMAGE" >/dev/null 2>&1 || { echo "tdbg: no $TDBG_IMAGE in the Docker that runs $PROJECT" >&2; return 2; }
    return 0
  fi
  printf '%s\n' "${idv[@]}" >> "$mech_dir/shards"
  # One line: the guest's shell gets it through printf %q, which turns a newline into a form only bash reads.
  # shellcheck disable=SC2016  # expanded by sh in the container
  out="$(dk run --rm --pull never --network "$net" "$TDBG_IMAGE" sh -c \
    'for s in "$@"; do if e=$(tdbg --address '"$TDBG_ADDRESS"' shard close-shard --shard-id "$s" 2>&1); then echo "closed $s"; else echo "failed $s" $e; fi; done' \
    tdbg-close "${idv[@]}")" || rc=$?
  closed="$(awk '$1 == "closed" { print $2 }' <<<"$out")"; failed="$(grep '^failed ' <<<"$out" || true)"
  mech_record "$(jq -nc --argjson k "$k" --argjson seed "$seed" --arg image "$TDBG_IMAGE" --arg net "$net" --arg ids "$ids" \
    --arg closed "$closed" --arg failed "$failed" --argjson rc "$rc" \
    'def ids: split("\n") | map(select(. != "") | tonumber);
     {k: $k, seed: $seed, image: $image, network: $net, shards: ($ids | ids | sort), closed: ($closed | ids | sort),
      failed: ($failed | split("\n") | map(select(. != ""))), docker_exit: $rc}')"
  if (( rc )) || [[ -n "$failed" || "$(wc -w <<<"$closed")" -ne "$k" ]]; then
    echo "tdbg close: $(wc -w <<<"$closed") of $k shards closed (docker run exit $rc)${failed:+; $(head -1 <<<"$failed")}" >&2; return 2
  fi
}
# pg bump <shard|seed:N> <delta> and pg restore: the shards row in the cell's own Postgres, through psql in its
# postgresql container. On SQL every write a shard makes runs in a transaction that first reads its row's range_id and
# compares it with the range id the shard holds: FOR SHARE for workflow and task writes, FOR UPDATE for UpdateShard
# (server 1.31.0 common/persistence/sql/shard.go:82-175, execution.go:39-58, execution_tasks.go:17-29). A mismatch is
# ShardOwnershipLostError: the shard stops (service/history/shard/context_impl.go:1455-1459) and the controller drops
# it. The next context for the shard reads the shard's range id from the row's data blob, not from the column
# (common/persistence/sql/shard.go:37-48, common/persistence/shard_manager.go:36-68), and its acquire writes that value
# + 1 conditioned on the column still holding it (context_impl.go:1189-1219), which the bumped column refuses again; an
# ownership loss is not retried (context_impl.go:2041-2057). So the shard stays unowned until the column holds the
# blob's value again (inferred from source). bump adds delta in one statement and records the value
# before and after; restore puts the value before back only while the column still holds the bumped one. bump leaves
# pg-<id>.pending before the UPDATE and replaces it with pg-<id> once psql's answer parses: a docker exec or ssh session
# that fails after the UPDATE committed leaves the pending file, and restore records what the column holds then and
# leaves it, since the value before is unknown.
pg_sql() { # pg_sql <statement>: its rows, unaligned, fields split by |
  local cid; cid="$(container postgresql)" || return 2
  dk exec "$cid" psql -X -A -t -q -v ON_ERROR_STOP=1 -U temporal -d temporal -c "$1"
}
pg_shard() { # pg_shard <id|seed:N>: the shard id, or a refusal
  local s="$1"
  if [[ "$s" == seed:* ]]; then shard_draw 1 "${s#seed:}"; return; fi
  [[ "$s" =~ ^[0-9]{1,4}$ ]] && (( 10#$s >= 1 && 10#$s <= NUM_SHARDS )) || { echo "pg: shard must be 1 to $NUM_SHARDS or seed:<n>, not '$s'" >&2; return 2; }
  echo "$(( 10#$s ))"
}
pg_bump() {
  local shard delta="$2" out old new
  shard="$(pg_shard "$1")" || return 2
  [[ "$delta" =~ ^[1-9][0-9]{0,8}$ ]] || { echo "pg bump: delta must be a whole number from 1 to 999999999, not '$delta'" >&2; return 2; }
  if (( MECH_CHECK )); then
    out="$(pg_sql "SELECT range_id FROM shards WHERE shard_id = $shard")" || return 2
    [[ "$out" =~ ^[0-9]+$ ]] || { echo "pg bump: no shards row for shard $shard in $PROJECT's postgresql ('$out')" >&2; return 2; }
    return 0
  fi
  [[ ! -e "$mech_dir/pg-$shard" && ! -e "$mech_dir/pg-$shard.pending" ]] || { echo "pg bump: shard $shard is bumped already in this raun" >&2; return 2; }
  echo "$shard" >> "$mech_dir/shards"; echo "$shard $delta" > "$mech_dir/pg-$shard.pending"
  out="$(pg_sql "UPDATE shards SET range_id = range_id + $delta WHERE shard_id = $shard RETURNING range_id - $delta, range_id")" \
    || { echo "pg bump: psql in $PROJECT's postgresql failed on shard $shard; the UPDATE may have committed" >&2; return 2; }
  [[ "$out" =~ ^[0-9]+\|[0-9]+$ ]] || { rm -f "$mech_dir/pg-$shard.pending"; echo "pg bump: no shards row for shard $shard ('$out')" >&2; return 2; }
  old="${out%|*}"; new="${out#*|}"
  echo "$shard $old $new" > "$mech_dir/pg-$shard"; rm -f "$mech_dir/pg-$shard.pending"
  mech_record "$(jq -nc --argjson s "$shard" --argjson d "$delta" --argjson old "$old" --argjson new "$new" \
    '{shard: $s, delta: $d, range_id_before: $old, range_id_bumped: $new}')"
}
pg_restore() {
  local f shard old new delta out found rc=0
  (( ! MECH_CHECK )) || return 0
  for f in "$mech_dir"/pg-*; do
    [[ -e "$f" ]] || continue
    if [[ "$f" == *.pending ]]; then
      read -r shard delta < "$f"
      found="$(pg_sql "SELECT range_id FROM shards WHERE shard_id = $shard")" || found=""
      mech_record "$(jq -nc --argjson s "$shard" --argjson d "$delta" --arg found "$found" \
        '{shard: $s, delta: $d, bump: "unknown", range_id_found: ($found | tonumber? // null), restored: false}')"
      echo "pg restore: shard $shard's bump of $delta may have committed (its psql call failed); it holds range_id ${found:-unknown}; left as it is" >&2
      rm -f "$f"; rc=2; continue
    fi
    read -r shard old new < "$f"
    out="$(pg_sql "UPDATE shards SET range_id = $old WHERE shard_id = $shard AND range_id = $new RETURNING range_id")" || return 2
    if [[ "$out" == "$old" ]]; then
      rm -f "$f"
      mech_record "$(jq -nc --argjson s "$shard" --argjson old "$old" --argjson new "$new" '{shard: $s, range_id_bumped: $new, range_id_restored: $old, restored: true}')"
    else
      found="$(pg_sql "SELECT range_id FROM shards WHERE shard_id = $shard")" || found=""
      mech_record "$(jq -nc --argjson s "$shard" --argjson old "$old" --argjson new "$new" --arg found "$found" \
        '{shard: $s, range_id_bumped: $new, range_id_restored: null, range_id_found: ($found | tonumber? // null), restored: false}')"
      echo "pg restore: shard $shard holds range_id ${found:-unknown}, not the $new the bump wrote; left as it is (it was $old)" >&2
      rc=2
    fi
  done
  return "$rc"
}
# The server's own persistence fault injection, per cell (cells/compose/fi.yml, cells/compose/config/laug-server.yaml):
#   serverfi on store=<Store> method=<Method> error=<Error> rate=<(0,1]> seed=<n>   one error on one store method
#   serverfi on profile=default seed=<n>                                            DefaultFaultInjection's 15 errors
#   serverfi off
# The server reads the config once, when history builds its data store factory, so on and off both recreate the
# cell's temporal-history with docker compose and fi.yml, LAUG_FI_* set or unset: each is a history restart and a
# 512-shard reacquisition besides the fault (kill-history measures that part). The faults sit under history's
# persistence metrics client, so persistence_error_with_type{error_type=...} counts each one (server 1.31.0
# common/persistence/client/factory.go:203-217, persistence_metric_clients.go:1436-1438; seen on a lab-host cell for
# UpdateShard, cells/results/2026-10-23-boot1.txt, A7a). On a compose cell the recreate is docker compose on this
# host. On a microVM cell it is docker compose inside the guest, over ssh, on the image's copy of cells/compose
# (GUEST_COMPOSE_DIR, faults/lib.sh), after the guest's laug-server.yaml is found to be this checkout's (its
# sha256). make harness-test runs the guest path on a fake guest, and boot 9 ran it on real guests, 6 ADVANCEs
# (cells/results/2026-10-29-boot9.txt:531-533); that the recreate keeps the guest's dynamic config mount is not seen.
# serverfi off does nothing until serverfi on has reached docker compose in this raun: the EXIT trap runs the remove
# whenever a raun stops before its own removal (never steady, a load that exits, a lost lease), and an off there would
# be an unasked-for history restart. Both check that laugd still reports the cell as leased just before the recreate,
# so a reaped, released or resetting cell is never recreated.
# The names are the server's (fault.go newFault, config.go DataStoreName); load/testdata/harness-test.sh checks these
# lists against the ones laug-server.yaml renders with, and laug cell fi-template-check those against the server
# source. A method is checked here for shape only: the server draws only for the methods its wrappers call generate()
# with, and laug cell fi-template-check holds each catalog row's method to those (a misspelt one would inject nothing).
FI_ERRORS="ShardOwnershipLost DeadlineExceeded Timeout ExecuteAndTimeout ResourceExhausted Unavailable"
FI_STORES="ShardStore TaskStore MetadataStore ExecutionStore Queue QueueV2 ClusterMDStore NexusEndpointStore"
FI_VARS="LAUG_FI_STORE LAUG_FI_METHOD LAUG_FI_ERROR LAUG_FI_RATE LAUG_FI_SEED LAUG_FI_PROFILE"
FI_COMPOSE_FILES=(-f "$root/cells/compose/cell.yml" -f "$root/cells/compose/fi.yml")
FI_CONFIG="cells/compose/config/laug-server.yaml"
# The release laug-server.yaml was vendored from (its laug-server-tag line). On any other tag the file is not that
# server's template: 1.32.1's adds a sqlite branch and renders the cluster addresses from BIND_ON_IP (the template
# check at 1.32.1), so serverfi refuses the cell rather than run history on a mix.
FI_TAG="$(sed -n 's/^# laug-server-tag: \([0-9.]*\)$/\1/p' "$root/$FI_CONFIG" 2>/dev/null)"
FI_SHA=""   # the sha256 of the laug-server.yaml history will load: serverfi_check sets it, server_fi records it
in_list() { local w; for w in $2; do [[ "$1" == "$w" ]] && return 0; done; return 1; }
serverfi_env() { # serverfi_env on key=val...: prints LAUG_FI_<KEY>=<val>, one per line; exit 2 naming what is wrong
  [[ "${1:-}" == on ]] || { echo "serverfi: want 'on key=value...' or 'off', got '$*'" >&2; return 2; }
  shift; local kv k v store="" method="" error="" rate="" seed="" profile=""
  for kv in "$@"; do
    k="${kv%%=*}"; v="${kv#*=}"
    [[ "$kv" == *=* && -n "$v" ]] || { echo "serverfi: '$kv' is not key=value" >&2; return 2; }
    case "$k" in
      store) store="$v" ;; method) method="$v" ;; error) error="$v" ;; rate) rate="$v" ;; seed) seed="$v" ;; profile) profile="$v" ;;
      *) echo "serverfi: unknown key '$k'; keys are store method error rate seed profile" >&2; return 2 ;;
    esac
  done
  # A fixed seed makes each method's sequence of draws repeatable in one history process, not which call gets which
  # draw under concurrent load; 0 or none is time-based in the server (method_fault_generator.go:24-27). No leading
  # zero: sprig, which adds the profile's per-method offsets in laug-server.yaml, reads 0600 as octal.
  [[ "$seed" =~ ^-?[1-9][0-9]{0,17}$ ]] || { echo "serverfi: seed must be a nonzero integer without a leading zero, got '$seed'" >&2; return 2; }
  if [[ -n "$profile" ]]; then
    [[ -z "$store$method$error$rate" ]] || { echo "serverfi: profile= takes only seed=, not store, method, error or rate" >&2; return 2; }
    [[ "$profile" == default ]] || { echo "serverfi: unknown profile '$profile'; the one profile is default" >&2; return 2; }
    printf 'LAUG_FI_PROFILE=%s\nLAUG_FI_SEED=%s\n' "$profile" "$seed"; return 0
  fi
  in_list "$error" "$FI_ERRORS" || { echo "serverfi: error '$error' is not one the server knows ($FI_ERRORS); any other name panics history at start" >&2; return 2; }
  in_list "$store" "$FI_STORES" || { echo "serverfi: store '$store' is not one of $FI_STORES" >&2; return 2; }
  [[ "$method" =~ ^[A-Z][A-Za-z0-9]+$ ]] || { echo "serverfi: method '$method' is not a method name" >&2; return 2; }
  [[ "$rate" =~ ^(0\.[0-9]*[1-9][0-9]*|1(\.0+)?)$ ]] || { echo "serverfi: rate '$rate' is not in (0, 1], written 0.x or 1" >&2; return 2; }
  printf 'LAUG_FI_STORE=%s\nLAUG_FI_METHOD=%s\nLAUG_FI_ERROR=%s\nLAUG_FI_RATE=%s\nLAUG_FI_SEED=%s\n' "$store" "$method" "$error" "$rate" "$seed"
}
serverfi_check() { # the lease's cell, whose dynamic config directory history will mount, is its own; its fi files are there
  [[ -n "$DYNCONFIG" ]] || { echo "serverfi: the lease names no dynconfig, so history could not be recreated on this cell's own dynamic config" >&2; return 2; }
  local tag="$FI_TAG" where="$FI_CONFIG"
  if [[ -n "$GUEST_SSH" ]]; then
    # gcell checks the guest's marker before any command, so one gcell true is the cell-id check; exit 3 is a mismatch.
    local rc=0 g want
    gcell true || rc=$?
    (( rc != 3 )) || { echo "serverfi: the guest at $GUEST_SSH no longer holds cell $CELL ($(dirname "$DYNCONFIG")/cell-id there names another), so history could not be recreated on this cell's own dynamic config" >&2; return 2; }
    (( rc == 0 )) || { echo "serverfi: the guest at $GUEST_SSH did not answer (ssh exit $rc)" >&2; return 2; }
    g="$(gcell "cd $(printf %q "$GUEST_COMPOSE_DIR") && test -f fi.yml && test -f config/laug-server.yaml || exit 4
      sha256sum config/laug-server.yaml; grep '^# laug-server-tag: ' config/laug-server.yaml; exit 0")" || rc=$?
    (( rc != 4 )) || { echo "serverfi: $GUEST_COMPOSE_DIR/fi.yml or $GUEST_COMPOSE_DIR/config/laug-server.yaml is missing in the guest at $GUEST_SSH; make fc-image, then make fc-snapshot, builds an image with both" >&2; return 2; }
    (( rc == 0 )) || { echo "serverfi: could not read $GUEST_COMPOSE_DIR/config/laug-server.yaml in the guest at $GUEST_SSH (exit $rc)" >&2; return 2; }
    [[ -f "$root/$FI_CONFIG" ]] || { echo "serverfi: $FI_CONFIG is missing" >&2; return 2; }
    FI_SHA="$(sed -n '1s/ .*//p' <<<"$g")"; want="$(sha256sum "$root/$FI_CONFIG" | cut -d' ' -f1)"
    [[ "$FI_SHA" == "$want" ]] || { echo "serverfi: the guest's $GUEST_COMPOSE_DIR/config/laug-server.yaml has sha256 ${FI_SHA:-unread} and this checkout's $FI_CONFIG has $want; the image holds another laug-server.yaml, and make fc-image, then make fc-snapshot, builds it again from this one" >&2; return 2; }
    tag="$(sed -n 's/^# laug-server-tag: \([0-9.]*\)$/\1/p' <<<"$g")"; where="the guest's $GUEST_COMPOSE_DIR/config/laug-server.yaml"
  else
    local got; got="$(cat "$(dirname "$DYNCONFIG")/cell-id" 2>/dev/null)" || got=""
    [[ "$got" == "$CELL" ]] || { echo "serverfi: $DYNCONFIG belongs to cell '$got', not $CELL" >&2; return 2; }
    [[ -f "$root/cells/compose/fi.yml" && -f "$root/$FI_CONFIG" ]] || { echo "serverfi: cells/compose/fi.yml or $FI_CONFIG is missing" >&2; return 2; }
    FI_SHA="$(sha256sum "$root/$FI_CONFIG" | cut -d' ' -f1)"
  fi
  [[ -n "$SERVER_TAG" && "$SERVER_TAG" == "$tag" ]] || { echo "serverfi: $where is the ${tag:-unknown} template and this cell runs ${SERVER_TAG:-a server tag that could not be read}; ${LAUG:-bin/laug} cell fi-template-check -server-tag ${SERVER_TAG:-x.y.z} shows what changed, and the file is vendored again from that release before serverfi runs on it" >&2; return 2; }
}
fi_env_json() { grep -E '^(LAUG_FI_[A-Z]+|TEMPORAL_SERVER_CONFIG_FILE_PATH)=' | jq -Rn '[inputs | capture("^(?<k>[^=]+)=(?<v>.*)$") | {(.k): .v}] | add // {}'; }
# serverfi runs inside mech_run's subshell, so what it keeps for the raun is in mech_dir: serverfi.on once serverfi on
# has reached docker compose, and server_fi.json, the raun JSON's server_fi (server_fi_get reads it, null without one).
server_fi_get() { local v; v="$(cat "$mech_dir/server_fi.json" 2>/dev/null)" || v=""; echo "${v:-null}"; }
server_fi_put() { printf '%s\n' "$1" > "$mech_dir/server_fi.json.tmp" && mv "$mech_dir/server_fi.json.tmp" "$mech_dir/server_fi.json"; }
serverfi() { # serverfi on key=val... | serverfi off: recreate this cell's temporal-history; record what it now holds
  local want="" cid es flog="$RESULTS_DIR/$RUN_ID.serverfi.log" unset=() v
  if [[ "${1:-}" == on ]]; then want="$(serverfi_env "$@")" || return 2
  elif [[ "${1:-}" != off || $# -ne 1 ]]; then echo "serverfi: want 'on key=value...' or 'off', got '$*'" >&2; return 2; fi
  if [[ "$1" == off && ! -e "$mech_dir/serverfi.on" ]]; then
    log "serverfi off: serverfi on never reached docker compose in this raun, so history is left as it is"; return 0
  fi
  serverfi_check || return 2
  assert_leased || return 2
  cid="$(container temporal-history)" || return 2
  # Visibility is the one setting history takes from the backend's env that the recreate must keep.
  es="$(dk inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$cid" | sed -n 's/^ENABLE_ES=//p')"
  for v in $FI_VARS; do unset+=(-u "$v"); done
  local assigns=(); [[ -z "$want" ]] || mapfile -t assigns <<<"$want"
  log "serverfi $*: recreating temporal-history in $PROJECT${GUEST_SSH:+ in the guest at $GUEST_SSH}"
  # Set before compose runs: a compose that fails halfway may still have recreated history with the fault on.
  if [[ "$1" == on ]]; then : > "$mech_dir/serverfi.on"; fi
  # SERVER_TAG keeps the cell's release: cell.yml's default would put a 1.31.0 history in a cell of any other tag.
  if [[ -n "$GUEST_SSH" ]]; then
    # The same recreate in the guest, on the image's cells/compose, in one ssh session after the cell-id check; each
    # assignment is quoted for the guest's shell, and compose's output comes back on the session's stdout and stderr.
    local gd; gd="$(printf %q "$GUEST_COMPOSE_DIR")"
    if ! gcell "cd $gd && env $(printf '%q ' "${unset[@]}" ENABLE_ES="${es:-false}" DYNCONFIG_DIR="$(dirname "$DYNCONFIG")" SERVER_TAG="$SERVER_TAG" "${assigns[@]}")docker compose -p laug --project-directory $gd -f cell.yml -f fi.yml up -d --no-deps --force-recreate temporal-history" >> "$flog" 2>&1; then
      echo "serverfi: docker compose up failed in the guest: $(tail -3 "$flog" | paste -sd ' ')" >&2; return 2
    fi
  elif ! env "${unset[@]}" ENABLE_ES="${es:-false}" DYNCONFIG_DIR="$(dirname "$DYNCONFIG")" SERVER_TAG="$SERVER_TAG" "${assigns[@]}" \
      docker compose -p "$PROJECT" --project-directory "$root/cells/compose" "${FI_COMPOSE_FILES[@]}" \
      up -d --no-deps --force-recreate temporal-history >> "$flog" 2>&1; then
    echo "serverfi: docker compose up failed: $(tail -3 "$flog" | paste -sd ' ')" >&2; return 2
  fi
  cid="$(container temporal-history)" || return 2
  local image; image="$(dk inspect -f '{{.Config.Image}}' "$cid")"
  [[ "$image" == "temporalio/server:$SERVER_TAG" ]] || { echo "serverfi: the new temporal-history runs '$image', not temporalio/server:$SERVER_TAG" >&2; return 2; }
  local seen; seen="$(dk inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$cid" | fi_env_json)"
  local wanted; wanted="$( { [[ -z "$want" ]] || printf '%s\n' "$want"; echo "TEMPORAL_SERVER_CONFIG_FILE_PATH=/etc/temporal/laug-server.yaml"; } | fi_env_json)"
  [[ "$(jq -S -c . <<<"$seen")" == "$(jq -S -c . <<<"$wanted")" ]] \
    || { echo "serverfi: the new temporal-history holds $(jq -c . <<<"$seen"), not $(jq -c . <<<"$wanted")" >&2; return 2; }
  # The raun JSON's server_fi: what was asked, what each recreated container held, and which file rendered it.
  if [[ "$1" == on ]]; then
    server_fi_put "$(jq -c -n --arg m "serverfi $*" --argjson env "$(printf '%s\n' "$want" | fi_env_json)" --argjson seen "$seen" \
      --arg config "$FI_CONFIG" --arg sha "$FI_SHA" \
      --argjson files "$(if [[ -n "$GUEST_SSH" ]]; then jq -cn --arg d "$GUEST_COMPOSE_DIR" '[$d + "/cell.yml", $d + "/fi.yml"]'; else echo '["cells/compose/cell.yml", "cells/compose/fi.yml"]'; fi)" \
      --arg image "$image" \
      '{inject: $m, seed: ($env.LAUG_FI_SEED | tonumber), env: $env, history_env_inject: $seen, history_env_remove: null,
        remove_error: null, config: $config, config_sha256: $sha, image: $image,
        compose_files: $files}')"
  elif [[ "$(server_fi_get)" != null ]]; then
    server_fi_put "$(jq -c --argjson seen "$seen" '.history_env_remove = $seen' <<<"$(server_fi_get)")"
  fi
}
# vm drive ops=<n> [bw=<bytes>] | vm drive off: a token bucket on a microVM cell's one drive, which holds Postgres,
# Docker's logs and the guest's Prometheus store, so the limit slows the signal's store too (inference). The cell's
# API socket is root's, so the bucket goes through laugd: POST /vmfault runs cells/backend-firecracker.sh vmfault as
# root, which PATCHes drives/rootfs. ops=<n> allows n requests a second, flushes included; bw=<bytes> n bytes a
# second; each bucket refills its size over 1000 ms, and one not given is left out, which a PATCH keeps (v1.17
# src/vmm/src/vmm_config/mod.rs, get_bucket_update). off sets both to size 0 and refill_time 0, which turns each off.
# Seen on the lab host in boot 10: Firecracker v1.17.0 answered the ops body and the off body 204, GET /vm/config read
# the bucket back, size 100000 and refill_time 1000, then null; a limit of 100 held the drive at 100.0 to 100.1
# requests a second, and after the off it ran 523.8 to 622.2 a second with 0 limiter events
# (cells/results/2026-10-29-boot10.txt, J and DS).
# Whether a flush takes a token is not seen, since the guest sent none under Unsafe (v1.17
# src/vmm/src/devices/virtio/block/virtio/request.rs, rate_limit). Each call is recorded with its request and answer.
vm_drive_body() { # vm_drive_body <args after drive>: the PartialDrive body on stdout, or a refusal (exit 2)
  local a k v ops="" bw="" usage="usage: vm drive ops=<n> [bw=<bytes>], or vm drive off; not 'vm drive $*'"
  if [[ $# -eq 1 && "$1" == off ]]; then
    jq -nc '{drive_id: "rootfs", rate_limiter: {bandwidth: {size: 0, refill_time: 0}, ops: {size: 0, refill_time: 0}}}'
    return
  fi
  for a in "$@"; do
    k="${a%%=*}"; v="${a#*=}"
    [[ "$a" == *=* && "$v" =~ ^[1-9][0-9]{0,11}$ ]] || { echo "$usage" >&2; return 2; }
    case "$k" in
      ops) [[ -z "$ops" ]] || { echo "$usage" >&2; return 2; }; ops="$v" ;;
      bw) [[ -z "$bw" ]] || { echo "$usage" >&2; return 2; }; bw="$v" ;;
      *) echo "$usage" >&2; return 2 ;;
    esac
  done
  [[ -n "$ops" ]] || { echo "$usage" >&2; return 2; }
  jq -nc --argjson ops "$ops" --arg bw "$bw" \
    '{drive_id: "rootfs", rate_limiter: ({ops: {size: $ops, refill_time: 1000}}
      + (if $bw == "" then {} else {bandwidth: {size: ($bw | tonumber), refill_time: 1000}} end))}'
}
vm() { # vm drive ...: checked under MECH_CHECK, else sent to laugd's POST /vmfault
  local body req ans rc=0 err
  [[ "${1:-}" == drive && $# -ge 2 ]] || { echo "usage: vm drive ops=<n> [bw=<bytes>], or vm drive off; not 'vm $*'" >&2; return 2; }
  shift
  body="$(vm_drive_body "$@")" || return 2
  if (( MECH_CHECK )); then
    [[ "$BACKEND" == firecracker ]] \
      || { echo "$FAULT runs on microVM cells only: the vm mechanism is a Firecracker drive limiter through laugd" >&2; return 2; }
    return 0
  fi
  req="$(jq -nc --arg id "$CELL" --argjson body "$body" '{id: $id, kind: "drive", body: $body}')"
  ans="$(curl -s --fail-with-body -m 30 -X POST "$POOL_ADDR/vmfault" -H 'content-type: application/json' -d "$req")" || rc=$?
  mech_record "$(jq -nc --argjson req "$req" --arg ans "$ans" --argjson rc "$rc" \
    '{request: $req, answer: ($ans | fromjson? // $ans), curl_exit: $rc}')"
  if (( rc )) || ! jq -e '.ok == true' >/dev/null 2>&1 <<<"$ans"; then
    err="$(jq -r '.error // empty' 2>/dev/null <<<"$ans")" || err=""
    echo "vm drive: laugd at $POOL_ADDR refused POST /vmfault: ${err:-curl exit $rc${ans:+, answer $ans}}" >&2; return 2
  fi
}
# A mechanism that recreates a container takes its logs with it, so the soft-assertion slice open before it is counted,
# and waited for, before it runs.
recreates() { [[ "$1" == *serverfi* ]]; }
mechanism() { # mechanism "<verb> <args...>"
  local IFS=' '
  # shellcheck disable=SC2086  # split the mechanism on spaces on purpose
  set -- $1
  case "$1" in
    toxic) (( MECH_CHECK )) && return 0; if [[ "$2" == add ]]; then shift 2; toxic_add "$@"; else toxic_remove "$3"; fi ;;
    docker)
      (( MECH_CHECK )) && return 0
      local verb="$2"; shift 2; local args=() a
      for a in "$@"; do
        if [[ "$a" == temporal-* ]]; then a="$(container "$a")" || return 2; fi
        args+=("$a")
      done
      dk "$verb" "${args[@]}" >/dev/null ;;
    dynconfig) (( MECH_CHECK )) && return 0; dynconfig "$2" "${3:-}" ;;
    tdbg) [[ "${2:-}" == close && $# -eq 4 ]] || { echo "usage: tdbg close <k> <seed>, not '$*'" >&2; return 2; }; tdbg_close "$3" "$4" ;;
    pg)
      case "${2:-} $#" in
        "bump 4") pg_bump "$3" "$4" ;;
        "restore 2") pg_restore ;;
        *) echo "usage: pg bump <shard|seed:N> <delta>, or pg restore; not '$*'" >&2; return 2 ;;
      esac ;;
    none) ;;   # a remove that does nothing: the fault stays until cleanup or the cell's release
    serverfi) (( MECH_CHECK )) && return 0; shift; serverfi "$@" ;;   # checked before the load by serverfi_preflight
    vm) shift; vm "$@" ;;
    *) echo "unknown mechanism: $*" >&2; return 2 ;;
  esac
}
run_mechanisms() { # run_mechanisms <step> "<mechanism>; <mechanism>..."
  local IFS=';' m; MECH_STEP="$1"
  for m in $2; do
    MECH_LINE="${m#"${m%%[![:space:]]*}"}"; MECH_LINE="${MECH_LINE%"${MECH_LINE##*[![:space:]]}"}"
    mechanism "$m"
  done
}
# mech_run <step> "<mechanisms>": run_mechanisms with errexit on, in a subshell, and its exit in mech_rc. Called as a
# statement of its own, never in an if or a || list: there bash switches errexit off for everything the call runs, so
# a failed cp in dynconfig would go on to its mv, and the first failed mechanism of a list would not stop the rest.
# What the mechanisms keep (records.jsonl, shards, pg-*, serverfi.on, server_fi.json) is in mech_dir, so nothing is
# lost with the subshell.
mech_rc=0
mech_run() { set +e; ( set -e; run_mechanisms "$@" ); mech_rc=$?; set -e; }
# mechanisms_check: every mechanism of inject, remove and cleanup, checked against the cell before the load starts,
# then the row's backend, when it names one, against the lease's.
mechanisms_check() {
  local IFS=';' m step want
  for step in inject remove cleanup; do
    for m in $(cat_get "$step"); do MECH_CHECK=1 mechanism "$m" || { echo "fault $FAULT, $step: '${m# }' cannot run on cell $CELL" >&2; return 2; }; done
  done
  want="$(cat_get backend)"
  [[ -z "$want" || "$want" == "$BACKEND" ]] || { echo "fault $FAULT runs on $want cells only; cell $CELL is $BACKEND" >&2; return 2; }
}
# remove_run: the remove step, by mech_run, its stderr kept in mech_dir/remove.err and passed on, so that a failed
# serverfi off can name its error in server_fi.remove_error. On a failure it sets remove_error, mech_failed (exit 2)
# and a BLOCK, and the caller words the reason from mech_rc. A remove that recreates history stays owed (removed=0)
# until it has succeeded, so the EXIT trap tries it once more after a failure, or after Ctrl-C or SIGTERM in the
# recreate; any other remove is owed no more once it starts (removed=1), so a pg restore never runs twice. A statement
# of its own, as mech_run is.
remove_error=""; mech_failed=0
remove_run() {
  if recreates "$(cat_get remove)"; then removed=0; else removed=1; fi
  mech_run remove "$(cat_get remove)" 2> "$mech_dir/remove.err"; cat "$mech_dir/remove.err" >&2
  (( mech_rc )) || { removed=1; return 0; }
  remove_error="'$(cat_get remove)' exited $mech_rc"; mech_failed=1; result="BLOCK"
  if [[ "$(server_fi_get)" != null ]]; then
    local e; e="$(grep -v "^\[$RUN_ID\] " "$mech_dir/remove.err" | tail -1)" || e=""
    server_fi_put "$(jq -c --arg e "${e:-the remove mechanism failed}" '.remove_error = $e' <<<"$(server_fi_get)")"
  fi
}

# Soft assertions per window (sa_open, sa_close, sa_sleep, sa_window) are counted by faults/lib.sh.
read_window() { MIN_WINDOW_S=60 THRESHOLDS="$TH_FILE" "$root/load/read-thresholds.sh" "$1" "$2"; }
read_drain() { MIN_WINDOW_S=30 THRESHOLDS="$TH_FILE" "$root/load/read-thresholds.sh" "$1" "$2"; }
read_readings() { # read_readings <start> <end>: the row's readings over [start, end], plus window_seconds; null when none
  local s="$1" e="$2" w out name q v
  [[ "$READINGS" != "{}" && -n "$s" && -n "$e" ]] && (( e > s )) || { echo null; return 0; }
  w=$(( e - s )); out="{\"window_seconds\":$w}"
  for name in $(jq -r 'keys_unsorted[]' <<<"$READINGS"); do  # names are [a-z0-9_], checked above
    q="$(jq -r --arg k "$name" '.[$k]' <<<"$READINGS")"
    v="$(curl -sf -G "$PROM_URL/api/v1/query" --data-urlencode "query=${q//\[1m\]/[${w}s]}" --data-urlencode "time=$e" \
      | jq -r '.data.result[0].value[1] // "null"' 2>/dev/null)" || v=null
    out="$(jq -c --arg k "$name" --arg v "${v:-null}" '.[$k] = (if $v == "null" or $v == "NaN" then null else ($v | tonumber? // $v) end)' <<<"$out")"
  done
  echo "$out"
}
all_ok() { # all_ok <readings-json> -> prints failing names=values (faults/lib.sh, thresholds_judge), returns 1 if any
  local j f; j="$(thresholds_judge "$1")" || { echo "the readings could not be judged"; return 1; }
  f="$(jq -r '.failing | join(" ")' <<<"$j")"
  [[ -z "$f" ]] || { echo "$f"; return 1; }
}
# not_produced <readings-json>: the readings with no data that the release file does not judge, a JSON list
not_produced() { thresholds_judge "$1" | jq -c '.not_produced'; }

# A serverfi row is checked before any load runs: a bad name would otherwise surface at injection, after steady_s,
# and a fault left on would outlive the raun.
serverfi_preflight() {
  local m w on=0 off=0
  serverfi_check || return 2
  while IFS= read -r m; do
    IFS=' ' read -r -a w <<<"$m"
    [[ "${w[0]:-}" == serverfi ]] || continue
    if [[ "${w[1]:-}" == on ]]; then serverfi_env "${w[@]:1}" >/dev/null || return 2; on=1
    elif [[ "${w[1]:-}" == off && ${#w[@]} -eq 2 ]]; then off=1
    else echo "serverfi: want 'serverfi on key=value...' or 'serverfi off', got '$m'" >&2; return 2; fi
  done < <(printf '%s;%s\n' "$(cat_get inject)" "$(cat_get remove)" | tr ';' '\n')
  (( !on || off )) || { echo "serverfi: $FAULT injects with serverfi on and never runs serverfi off" >&2; return 2; }
}
# The server tag the cell runs, read from its temporal-history image and checked against the lease's (faults/lib.sh,
# server_tag_read). Unread, the lease's tag stands, else none: the raun records null.
server_tag_read "$LEASE_FILE" || exit 2
TH_FILE="$(mktemp)"; pool_dir="$(mktemp -d)"; trap 'rm -rf "$TH_FILE" "$pool_dir"' EXIT
thresholds_resolve "$SERVER_TAG" "$TH_FILE" || { echo "no threshold set for server ${SERVER_TAG:-unread}; nothing was run" >&2; exit 2; }
if [[ -n "$signal_name" ]]; then
  sig="$(yq -o=json -I=0 ".thresholds[] | select(.name == \"$signal_name\")" "$TH_FILE")"
  [[ -n "$sig" ]] && [[ "$(jq -r '.max // "null"' <<<"$sig")" != null ]] \
    || { echo "fault $FAULT: signal $signal_name is not a threshold with a max in $TH_NOTE" >&2; exit 2; }
  SIGNAL_QUERY="$(jq -r .query <<<"$sig" | sed "s/\[1m\]/[$SIGNAL_RANGE]/g")"; SIGNAL_MAX="$(jq -r .max <<<"$sig")"
  [[ "$(jq -r .no_data <<<"$sig")" != not_judged ]] \
    || log "note: signal $signal_name is not judged on no data on this release ($(jq -r '.override.file // "the threshold set"' <<<"$sig")); as a signal, no data neither lands nor recovers"
else
  SIGNAL_QUERY="$(cat_get signal_query)"; SIGNAL_MAX="$(cat_get signal_max)"; signal_name="custom"
fi
# tdbg runs the cell's own admin-tools image, the one its schema setup ran: cells/compose/cell.yml pins temporalio/server
# and temporalio/admin-tools to one SERVER_TAG, and a microVM cell's image holds the default's (1.31.0).
TDBG_IMAGE="${TDBG_IMAGE:-temporalio/admin-tools:${SERVER_TAG:-1.31.0}}"
if recreates "$(cat_get inject) $(cat_get remove)"; then serverfi_preflight || exit 2; fi

total=$(( STEADY_S + HOLD_S + BUDGET_S + SETTLE_S + AFTER_S + 90 ))
log "thresholds: $TH_NOTE"
log "fault $FAULT on cell $CELL, server ${SERVER_TAG:-unread}: signal $signal_name > $SIGNAL_MAX; steady ${STEADY_S}s hold ${HOLD_S}s budget ${BUDGET_S}s after ${AFTER_S}s; load ${total}s at ${RATE}/s"
# Omes exits on the first failed iteration by default; a fault run expects failures, so it continues. Run 1 of
# persistence-latency died 10 s after injection on one StartWorkflowExecution deadline and measured nothing after.
# setsid gives the load a process group of its own, which cleanup TERMs; load/run-omes.sh passes the TERM on to the go
# run and omes child it started. Run 3 found run 2's worker still holding :9092 after only the parent had been killed.
# Checked before any load: a mechanism that cannot run on this cell would otherwise surface at inject, steady_s in.
mechanisms_check
wait_omes_ports
# No cell being built or destroyed on this host as the load starts (faults/lib.sh, pool_quiet): its last poll is the
# before window's first. The wait can last POOL_QUIET_WAIT_S, and laugd may reset or reap the leased cell meanwhile
# (its port block can then pass to another cell), so the lease is checked again before the load starts.
pool_quiet before
assert_leased || { log "pool: cell $CELL is not leased after the wait for laugd's busy cells; no load was started"; exit 2; }
t_load=$(date +%s); sa_open before "$t_load"
CELL_ADDRESS="$CELL_ADDRESS" DURATION="${total}s" RATE="$RATE" MAX_CONCURRENT="$MAX_CONCURRENT" RUN_ID="$RUN_ID" CONTINUE_ON_FAILURE=1 \
  RESULTS_DIR="$LOAD_DIR" LEASE_FILE="$LEASE_FILE" SERVER_TAG="$SERVER_TAG" setsid "$root/load/run-omes.sh" "$SCENARIO" >/dev/null 2>"$RESULTS_DIR/$RUN_ID.load.log" &
load_pid=$!
removed=0; cleaned=0; written=0; sa_dir="$(mktemp -d)"; sa_pids=(); mech_dir="$(mktemp -d)"; inject_at=""; landed_at=""; remove_at=""
cleanup() {
  local rc=$? p mechs
  kill -- "-$load_pid" 2>/dev/null || kill "$load_pid" 2>/dev/null || true
  (( removed )) || { removed=1; mech_run remove "$(cat_get remove)"; }
  (( cleaned )) || { cleaned=1; mech_run cleanup "$(cat_get cleanup)"; }
  # Stopped after inject without the raun JSON: what was done to the cell goes to the raun's path all the same.
  if (( ! written )) && [[ -n "$inject_at" ]]; then
    # jq -s prints [] for a missing file and exits 2, so a fallback after || would add a second []: an assignment.
    mechs="$(jq -sc . "$mech_dir/records.jsonl" 2>/dev/null)" || mechs="[]"
    jq -n --arg run "$RUN_ID" --arg fault "$FAULT" --arg cell "$CELL" --arg address "$CELL_ADDRESS" --arg tag "$SERVER_TAG" --arg scenario "$SCENARIO" \
      --arg backend "$BACKEND" --arg fcv "$FC_VERSION_LEASE" --arg cache "$CACHE_TYPE" --argjson virt "$HOST_VIRTUALIZED" \
      --arg inject_at "$inject_at" --arg landed_at "$landed_at" --arg remove_at "$remove_at" --argjson rc "$rc" \
      --argjson mechanisms "$mechs" --argjson server_fi "$(server_fi_get)" \
      '{run_id: $run, fault: $fault, cell: $cell, address: $address, server_tag: (if $tag == "" then null else $tag end),
        backend: $backend, firecracker_version: (if $fcv == "" then null else $fcv end), cache_type: (if $cache == "" then null else $cache end), host_virtualized: $virt, scenario: $scenario,
        partial: true, exit: $rc, inject_at: $inject_at, landed_at: $landed_at, remove_at: $remove_at, mechanisms: $mechanisms, result: "BLOCK",
        reason: "stopped before the result (exit \($rc)); the fault was removed on the way out; the log says why"}
        + (if $server_fi == null then {} else {server_fi: $server_fi} end)' \
      > "$RESULTS_DIR/$RUN_ID.json" 2>/dev/null && echo "laug · stopped after inject (exit $rc); partial raun JSON at $RESULTS_DIR/$RUN_ID.json" >&2
  fi
  for p in "${sa_pids[@]+"${sa_pids[@]}"}"; do kill "$p" 2>/dev/null || true; done; rm -rf "$sa_dir" "$mech_dir" "$TH_FILE" "$pool_dir"
}
trap cleanup EXIT

result="ADVANCE"; reason=""
# A load generator that exits is a failed run with its own reason, not "never steady" 90 s later.
sleep 10
if ! kill -0 "$load_pid" 2>/dev/null; then
  echo "load generator exited: $(tail -1 "$RESULTS_DIR/$RUN_ID.load.log")" >&2
  exit 2
fi
sa_sleep $(( STEADY_S - 10 ))
t_before_end=$(date +%s); pool_poll before; before="$(read_window $(( t_before_end - 60 )) "$t_before_end")"
if ! bad="$(all_ok "$before")"; then result="BLOCK"; reason="never steady: $bad"; fi
np_before="$(not_produced "$before")"; np_after="null"
log "before: $(jq -c 'del(.window_seconds)' <<<"$before")"
[[ "$np_before" == "[]" ]] || log "before: no data, not produced on this release and not judged: $(jq -r 'join(", ")' <<<"$np_before") (${TH_RELEASE:-the threshold set})"

hold_end=""; remove_wait_s="null"; restart_at=""; recovered_at=""; after_start=""; after_end=""
during="{}"; drain="{}"; after="{}"; peak="null"; sweep="null"; sweep_cutoff=""; sweep_error=""
readings_before="$(read_readings $(( t_before_end - 60 )) "$t_before_end")"; readings_during=null; readings_drain=null; readings_after=null
# recover: shards-acquired. touched: the shard ids the inject recorded. shards_recovered: every one of them logged
# "Acquired shard" since inject or since removal; acquired keeps the last reading ({"<id>": seconds after that start}),
# null when history's logs could not be read, which is not recovered.
acquired="null"; acquired_in_hold="null"
touched() { sort -nu "$mech_dir/shards" 2>/dev/null || true; }
acquired_since() { if [[ "$RECOVER_FROM" == inject ]]; then echo "$inject_at"; else echo "$remove_at"; fi; }
shards_recovered() {
  # shellcheck disable=SC2046  # one argument per shard id
  acquired="$(shards_acquired "$(acquired_since)" $(touched) 2>/dev/null)" || { acquired="null"; return 1; }
  [[ "$(jq length <<<"$acquired")" -eq "$(touched | wc -l)" ]]
}
not_acquired_text() {
  local since; since="since $([[ "$RECOVER_FROM" == inject ]] && echo inject || echo removal)"
  [[ "$acquired" != null ]] || { echo "history's logs could not be read for \"Acquired shard\" $since"; return; }
  # shellcheck disable=SC2046  # one argument per shard id
  jq -rn --argjson a "$acquired" --arg since "$since" '[$ARGS.positional[] | select(. as $i | $a | has($i) | not)]
    | "\(length) of \($ARGS.positional | length) shard(s) logged no \"Acquired shard\" \($since): \(.[:10] | join(" "))\(if length > 10 then " ..." else "" end)"' \
    --args $(touched)
}
if [[ "$result" == ADVANCE && -n "$SIGNAL_BASE" ]]; then
  base="$(signal_raw 2>/dev/null)" || base="null"
  if [[ "$base" == null ]]; then result="BLOCK"; reason="no signal reading before inject: $signal_name read null, so a rise cannot be measured"
  else log "signal base $base, read before inject"; fi
fi
# A mechanism that recreates a container first waits, the fault as it was, for the open soft-assertion slices to be
# counted (recreates, above). inject_at and remove_at are taken after that wait, when the mechanism runs, so the hold
# and time_to_recover_s start at the mechanism itself. remove_wait_s is how long the fault stayed on after the during
# window ended (hold_end): the during reading and the readings, plus that wait for a recreate, plus a shard fault's
# read of history's logs for the hold.
inject_error=""; held="null"
if [[ "$result" == ADVANCE ]]; then
  assert_leased; pool_poll before during
  t=$(date +%s); if recreates "$(cat_get inject)"; then sa_close "$t"; sa_wait; fi
  inject_at=$(date +%s); mech_run inject "$(cat_get inject)"
  if (( mech_rc )); then
    inject_error="'$(cat_get inject)' exited $mech_rc"; mech_failed=1; result="BLOCK"; reason="inject failed: $inject_error"
    log "inject failed: $inject_error"
  else log "injected"; fi
  sa_close "$inject_at"; sa_open during "$inject_at"
  if [[ -z "$inject_error" ]]; then
    for ((t=0; t<HOLD_S; t+=5)); do
      sleep 5; sa_tick; pool_poll during; s="$(signal)"
      if exceeds "$s"; then landed_at=$(date +%s); peak="$s"; log "landed after $(( landed_at - inject_at )) s: signal $s"; break; fi
    done
    if [[ -z "$landed_at" ]]; then result="BLOCK"; reason="did not land: signal $signal_name stayed at $(signal) over ${HOLD_S}s"; fi
  fi
fi
if [[ -n "$landed_at" ]]; then
  sa_sleep $(( inject_at + HOLD_S - $(date +%s) ))
  s="$(signal)"; awk -v a="$s" -v b="$peak" 'BEGIN { exit !(a+0 > b+0) }' && peak="$s"
  hold_end=$(date +%s); during="$(read_window "$landed_at" "$hold_end" 2>/dev/null || echo '{}')"
  readings_during="$(read_readings "$landed_at" "$hold_end")"
  if recreates "$(cat_get remove)"; then sa_close "$hold_end"; sa_wait; fi
  # A shard held from inject to removal (recover_from remove) should log no "Acquired shard" before its removal; any
  # it did log is recorded: the fault did not hold the shard.
  # held: the lines that say the shard's acquire was refused, from inject to now, just before the removal (faults/lib.sh,
  # shards_held).
  if [[ "$RECOVER" == shards-acquired && "$RECOVER_FROM" == remove ]]; then
    # shellcheck disable=SC2046  # one argument per shard id
    acquired_in_hold="$(shards_acquired "$inject_at" $(touched) 2>/dev/null)" || acquired_in_hold="null"
    # shellcheck disable=SC2046  # one argument per shard id
    held="$(shards_held "$inject_at" "$(date +%s)" $(touched) 2>/dev/null)" || held="null"
  fi
  pool_poll during drain
  remove_at=$(date +%s); remove_wait_s=$(( remove_at - hold_end ))
  restart_at="$(date +%s.%N)"; remove_run
  if (( mech_rc )); then
    reason="remove failed: $remove_error; the fault may still be on the cell, so recovery was not read"
    log "remove failed: $remove_error"; (( removed )) || log "the EXIT trap tries the remove again"
  else log "removed ${remove_wait_s} s after the hold ended; during: $(jq -c 'del(.window_seconds)' <<<"$during")"; fi
  sa_close "$remove_at"; sa_open drain "$remove_at"
  for ((t=0; t<=BUDGET_S+60; t+=5)); do
    [[ -z "$remove_error" ]] || break
    if [[ "$RECOVER" == signal ]]; then s="$(signal)"; recovered "$s" && { recovered_at=$(date +%s); break; }
    elif shards_recovered; then recovered_at=$(date +%s); break; fi
    sleep 5; sa_tick; pool_poll drain
  done
  if [[ -n "$remove_error" ]]; then :
  elif [[ -z "$recovered_at" ]]; then
    result="BLOCK"
    if [[ "$RECOVER" == signal ]]; then reason="did not recover within $(( BUDGET_S + 60 ))s: signal $(signal)"
    else reason="did not recover within $(( BUDGET_S + 60 ))s: $(not_acquired_text)"; fi
  elif (( recovered_at - remove_at > BUDGET_S )); then result="BLOCK"; reason="recovered in $(( recovered_at - remove_at ))s, budget ${BUDGET_S}s"
  fi
  if [[ -n "$recovered_at" ]]; then
    log "recovered $(( recovered_at - remove_at )) s after removal; settling ${SETTLE_S}s, then ${AFTER_S}s for the after window"
    sa_sleep "$SETTLE_S"; after_start=$(date +%s); pool_poll drain after; sa_close "$after_start"; sa_open after "$after_start"
    drain="$(read_drain "$remove_at" "$after_start" 2>/dev/null || echo '{}')"; log "drain: $(jq -c 'del(.window_seconds)' <<<"$drain")"
    readings_drain="$(read_readings "$remove_at" "$after_start")"
    sa_sleep "$AFTER_S"; after_end=$(date +%s); pool_poll after; sa_close "$after_end"; after="$(read_window "$after_start" "$after_end")"
    readings_after="$(read_readings "$after_start" "$after_end")"
    if ! bad="$(all_ok "$after")"; then result="BLOCK"; reason="after window not clean: $bad"; fi
    np_after="$(not_produced "$after")"
    log "after: $(jq -c 'del(.window_seconds)' <<<"$after")"
    [[ "$np_after" == "[]" ]] || log "after: no data, not produced on this release and not judged: $(jq -r 'join(", ")' <<<"$np_after") (${TH_RELEASE:-the threshold set})"
    sweep_cutoff="$(date -u -d "@$(( after_end - SWEEP_MARGIN_S ))" +%Y-%m-%dT%H:%M:%SZ)"
    if sweep="$("$root/load/sweep.sh" "$CELL_ADDRESS" "$TASK_QUEUE" "$sweep_cutoff")"; then log "sweep: $sweep, started before $sweep_cutoff"
    else rc=$?; sweep="null"; sweep_error="load/sweep.sh exited $rc against $CELL_ADDRESS"; log "sweep failed: $sweep_error"; fi
  fi
fi

ttr=""; [[ -n "$recovered_at" && -n "$remove_at" ]] && ttr=$(( recovered_at - remove_at ))
# Failed iterations by window. Omes logs in the host's offset; lw_buckets applies it. The log is read now, while the
# load still runs, so a line after the after window counts as late.
iterations_failed="$(lw_buckets "$LOAD_LOG" "$inject_at" "$remove_at" "$after_start" "$after_end")"
lost="$(jq -n --argjson after "$after" --argjson after_s "$AFTER_S" --argjson it "$iterations_failed" --argjson sweep "$sweep" \
          --arg err "$sweep_error" --arg cutoff "$sweep_cutoff" --argjson rate "$RATE" \
          '{after:$after, after_s:$after_s, iterations_failed:$it, sweep:$sweep, sweep_error:$err, sweep_cutoff:$cutoff, rate:$rate}')"
if ! lost_reasons="$(lw_raun_judge "$lost")"; then
  lost_reasons="$(paste -sd ';' <<<"$lost_reasons" | sed 's/;/; /g')"
  if [[ "$result" == ADVANCE ]]; then reason="$lost_reasons"; else reason="$reason; $lost_reasons"; fi
  result="BLOCK"
fi
# The window still open closes now: before at its reading when the run was never steady, during when the fault
# never landed (its remove runs below, after every slice is counted), drain when the signal never recovered. A window
# that never opened is null.
# Its last pool poll too: the window sa_win names, during or drain (the before window's last was at its reading).
if [[ -z "$inject_at" ]]; then sa_close "$t_before_end"; else [[ -z "$sa_win" ]] || pool_poll "$sa_win"; sa_close "$(date +%s)"; fi
sa_wait
softasserts="{}"
for w in before during drain after; do
  softasserts="$(jq -c --arg w "$w" --argjson v "$(sa_window "$w")" '.[$w] = $v' <<<"$softasserts")"
  if [[ -s "$sa_dir/$w.slices" ]] && errs="$(while read -r n; do cat "$sa_dir/$n.err"; done < "$sa_dir/$w.slices" | sort -u)" && [[ -n "$errs" ]]; then
    log "soft assertions, $w window: $(paste -sd ';' <<<"$errs" | sed 's/;/; /g')"
  fi
done
# When the fault restarted history, the new process's "initial shards ready" line is the server's own end of
# reacquisition; record it next to the signal's view, from the moment the remove mechanism ran (sub-second), so
# it includes the container start (null when history was not restarted after the removal).
shards_ready_s="null"
if [[ -n "$GUEST_SSH" ]]; then shards_ts() { gcell "/opt/laug/cells/shards-ready.sh laug --ts"; }
else shards_ts() { "$root/cells/shards-ready.sh" "$PROJECT" --ts; }; fi
if [[ -n "$restart_at" ]] && ts="$(shards_ts 2>/dev/null)"; then
  e="$(date -u -d "$ts" +%s.%N)"; awk -v e="$e" -v r="$restart_at" 'BEGIN { exit !(e > r) }' && shards_ready_s="$(printf '%.1f' "$(echo "$e - $restart_at" | bc)")"
fi
# A remove still owed (the fault never landed, or the inject failed) runs now, so its record is in the JSON (for serverfi,
# server_fi.history_env_remove or server_fi.remove_error). Every soft-assertion slice is counted by now, so a recreate
# takes no uncounted logs with it. A failed one is a harness failure, as at removal.
if [[ -n "$inject_at" && -z "$remove_error" ]] && (( ! removed )); then
  remove_run
  if (( mech_rc )); then
    reason="$reason; remove failed: $remove_error"
    log "remove failed: $remove_error"; (( removed )) || log "the EXIT trap tries the remove again"
  else log "removed: $(cat_get remove)"; fi
fi
# cleanup runs now that the result is decided, whatever it is: a self-test whose remove does nothing puts the cell back
# here. A failed cleanup is logged and recorded, not judged.
cleanup_error=""
if [[ -n "$(cat_get cleanup)" ]]; then
  cleaned=1; mech_run cleanup "$(cat_get cleanup)"
  if (( mech_rc == 0 )); then log "cleanup: $(cat_get cleanup)"
  else cleanup_error="'$(cat_get cleanup)' exited $mech_rc"; log "cleanup failed: $cleanup_error"; fi
fi
mechanisms="$(jq -sc . "$mech_dir/records.jsonl" 2>/dev/null)" || mechanisms="[]"
server_fi="$(server_fi_get)"
pool_busy_json="$(pool_json before during drain after)"
# The row's readings by window, when it has any (READINGS); a window that never opened is null.
readings="null"
[[ "$READINGS" == "{}" ]] || readings="$(jq -c -n --argjson b "$readings_before" --argjson d "$readings_during" --argjson r "$readings_drain" \
  --argjson a "$readings_after" '{before: $b, during: $d, drain: $r, after: $a}')"
# The shards the inject touched and when each logged "Acquired shard" (recover: shards-acquired): seconds after inject
# or removal, the ones that never did, the last, and for a shard held to its removal any acquire before that.
shards_json="null"
if [[ "$RECOVER" == shards-acquired && -n "$inject_at" ]]; then
  # shellcheck disable=SC2046  # one argument per shard id
  shards_json="$(jq -nc --arg from "$RECOVER_FROM" --argjson a "$acquired" --argjson hold "$acquired_in_hold" --argjson held "$held" '
    ($ARGS.positional | map(tonumber)) as $ids
    | {from: $from, shards: ($ids | length), acquired_s: $a,
       missing: (if $a == null then null else [$ids[] | select(tostring as $i | $a | has($i) | not)] end),
       last_s: (if $a == null or ($a | length) < ($ids | length) or ($ids | length) == 0 then null else ([$a[]] | max) end),
       acquired_before_remove: $hold, held_evidence: $held}' --args $(touched))"
fi
# A self-test passes on a BLOCK for the reason it names, and only with the hold seen: every touched shard logged
# "Couldn't acquire shard" between inject and removal.
self_test="null"
if [[ -n "$SELF_TEST" ]]; then
  # shellcheck disable=SC2046  # one argument per shard id
  self_test="$(jq -nc --arg want "$SELF_TEST" --arg result "$result" --arg reason "$reason" --argjson held "$held" '
    ($ARGS.positional) as $ids
    | [$ids[] | select(($held // {})[.]["Couldn'"'"'t acquire shard"] // 0 | . == 0)] as $unheld
    | {expect: $want, held_evidence: $held,
       passed: ($result == "BLOCK" and ($reason | startswith($want)) and ($ids | length) > 0 and ($unheld | length) == 0),
       why: (if $result != "BLOCK" then "the raun ADVANCEd"
             elif ($reason | startswith($want) | not) then "BLOCKed for another reason: \($reason)"
             elif $held == null then "history'"'"'s logs could not be read for the hold"
             elif ($ids | length) == 0 then "no shard was touched"
             elif ($unheld | length) > 0 then "no \"Couldn'"'"'t acquire shard\" line in the hold from shard \($unheld | join(" ")): nothing shows the bump held"
             else "a BLOCK that \($want), and every touched shard logged \"Couldn'"'"'t acquire shard\" in the hold" end)}' --args $(touched))"
fi
jq -n --arg run "$RUN_ID" --arg fault "$FAULT" --arg cell "$CELL" --arg address "$CELL_ADDRESS" --arg tag "$SERVER_TAG" --arg scenario "$SCENARIO" --arg signal "$signal_name" \
      --arg backend "$BACKEND" --arg fcv "$FC_VERSION_LEASE" --arg cache "$CACHE_TYPE" --argjson virt "$HOST_VIRTUALIZED" \
      --argjson signal_max "$SIGNAL_MAX" --argjson peak "$peak" --arg result "$result" --arg reason "$reason" \
      --argjson base "$base" --arg recover "$RECOVER" --argjson mechanisms "$mechanisms" --argjson shards "$shards_json" --arg cleanup_error "$cleanup_error" \
      --arg inject_error "$inject_error" --arg remove_error "$remove_error" --argjson self_test "$self_test" \
      --arg inject_at "$inject_at" --arg landed_at "$landed_at" --arg remove_at "$remove_at" --arg recovered_at "$recovered_at" --arg ttr "$ttr" --argjson shards_ready "$shards_ready_s" --arg after_start "$after_start" --argjson settle "$SETTLE_S" --argjson after_s "$AFTER_S" --argjson drain "$drain" \
      --argjson before "$before" --argjson during "$during" --argjson after "$after" \
      --argjson rate "$RATE" --arg tq "$TASK_QUEUE" --arg after_end "$after_end" --argjson iterations_failed "$iterations_failed" \
      --argjson sweep "$sweep" --arg sweep_cutoff "$sweep_cutoff" --argjson sweep_margin "$SWEEP_MARGIN_S" --arg sweep_error "$sweep_error" --argjson softasserts "$softasserts" \
      --argjson server_fi "$server_fi" --argjson remove_wait "$remove_wait_s" --argjson readings "$readings" \
      --arg th_release "$TH_RELEASE" --arg th_note "$TH_NOTE" --argjson th_overrides "$TH_OVERRIDES" --argjson np_before "$np_before" --argjson np_after "$np_after" \
      --argjson pool_wait "$pool_quiet_wait_s" --argjson pool_bound "$POOL_QUIET_WAIT_S" --argjson pool_busy "$pool_busy_json" \
      '{run_id:$run, fault:$fault, cell:$cell, address:$address, server_tag:(if $tag == "" then null else $tag end),
        backend:$backend, firecracker_version:(if $fcv == "" then null else $fcv end), cache_type:(if $cache == "" then null else $cache end), host_virtualized:$virt,
        scenario:$scenario, rate:$rate, task_queue:$tq, signal:$signal, signal_max:$signal_max, signal_peak:$peak,
        thresholds_release:(if $th_release == "" then null else $th_release end), thresholds_note:$th_note, overrides_applied:$th_overrides,
        not_produced:{before:$np_before, after:$np_after},
        inject_at:$inject_at, landed_at:$landed_at, remove_at:$remove_at, recovered_at:$recovered_at, time_to_recover_s:$ttr, remove_wait_s:$remove_wait, history_shards_ready_s:$shards_ready, settle_s:$settle, after_s:$after_s, after_start:$after_start, after_end:$after_end,
        before:$before, during:$during, drain:$drain, after:$after,
        iterations_failed:$iterations_failed, sweep:$sweep, sweep_cutoff:$sweep_cutoff, sweep_margin_s:$sweep_margin, sweep_error:(if $sweep_error == "" then null else $sweep_error end),
        softasserts:$softasserts, pool_quiet_wait_s:$pool_wait, pool_quiet_bound_s:$pool_bound, pool_busy:$pool_busy,
        signal_base:$base, recover:$recover, mechanisms:$mechanisms, shards_acquired:$shards, cleanup_error:(if $cleanup_error == "" then null else $cleanup_error end),
        inject_error:(if $inject_error == "" then null else $inject_error end), remove_error:(if $remove_error == "" then null else $remove_error end),
        self_test:$self_test, result:$result, reason:$reason} + (if $readings == null then {} else {readings: $readings} end)
        + (if $server_fi == null then {} else {server_fi: $server_fi} end)' > "$RESULTS_DIR/$RUN_ID.json"
written=1

echo
echo "laug · fault $FAULT on raun $RUN_ID"
echo "server      ${SERVER_TAG:-not read}"
echo "thresholds  $TH_NOTE"
# Every override of the release file, with what it did on this raun: never left out of the summary. thresholds_judge
# leaves out a threshold with no max, so such an override is in neither not_produced list: it says so for each window
# judged (before, and after once read), with or without data.
jq -rn --argjson o "$TH_OVERRIDES" --argjson b "$np_before" --argjson a "$np_after" --argjson rb "$before" --argjson ra "$after" \
  --argjson nomax "$(yq -o=json -I=0 '[.thresholds[] | select(.max == null) | .name]' "$TH_FILE" 2>/dev/null || echo '[]')" \
  --arg f "$TH_RELEASE" '
  $o[] | .name as $n | [("before" | select(any($b[]?; . == $n))), ("after" | select(any(($a // [])[]; . == $n)))] as $w
  | "override    \($n): " + (if $w != [] then "no data in the \($w | join(" and ")) window, not produced on this release, not judged"
      elif any($nomax[]; . == $n) then
        ([["before", $rb[$n]], (select($a != null) | ["after", $ra[$n]])]
         | [.[] | select(.[1] != null) | .[0]] as $d | [.[] | select(.[1] == null) | .[0]] as $nd
         | [("had data in the \($d | join(" and ")) window" | select($d != [])), ("no data in the \($nd | join(" and ")) window" | select($nd != []))]
         | join(", ")) + "; no max in the threshold set, so not judged"
      else "had data where judged, so judged against its max" end)
    + " (\($f): \(.reason))"'
pool_summary before during drain after
[[ -n "$landed_at" ]] && echo "landed      $(( landed_at - inject_at )) s after inject; signal peak $peak (max $SIGNAL_MAX)"
[[ -n "$ttr" ]] && echo "recovered   ${ttr} s after removal (budget ${BUDGET_S} s); the removal ran ${remove_wait_s} s after the hold ended"
[[ "$iterations_failed" != null ]] && echo "failed      $(jq -r '[to_entries[] | select(.value != null and .key != "total" and .key != "not_found") | "\(.key) \(.value)"] | join(", ")' <<<"$iterations_failed") iteration(s), by window$(jq -r 'if (.not_found // 0) > 0 then "; \(.not_found) said workflow execution not found" else "" end' <<<"$iterations_failed")"
[[ "$sweep" != null ]] && echo "sweep       $(jq -r 'to_entries | map("\(.key) \(.value)") | join(", ") | if . == "" then "nothing" else . end' <<<"$sweep") started before $sweep_cutoff"
jq -r '[to_entries[] | select(.value != null) | "\(.key) \([.value | to_entries[] | select(.key != "first") | .value // empty] | add // 0)"]
  | if length > 0 then "softassert  \(join(", ")), by window (recorded, not judged)" else empty end' <<<"$softasserts"
jq -r '[.[] | select(. != null) | .first[] | sub("^failed assertion: "; "")] | reduce .[] as $m ([]; if any(.[]; . == $m) then . else . + [$m] end)
  | if length > 0 then "            first: \(join("; "))" else empty end' <<<"$softasserts"
jq -r '.[] | if .k != null then "closed      \(.closed | length) of \(.k) shards, seed \(.seed), at \(.step)"
  elif .bump == "unknown" then "range_id    shard \(.shard) bumped by \(.delta) or not (psql failed): found \(.range_id_found), left as it is, at \(.step)"
  elif .restored == true then "range_id    shard \(.shard) back to \(.range_id_restored) from \(.range_id_bumped), at \(.step)"
  elif .restored == false then "range_id    shard \(.shard) not restored: found \(.range_id_found), the bump wrote \(.range_id_bumped), at \(.step)"
  elif .range_id_bumped != null then "range_id    shard \(.shard) \(.range_id_before) -> \(.range_id_bumped), at \(.step)"
  else empty end' <<<"$mechanisms"
[[ "$shards_json" == null ]] || jq -r '"shards      \((.acquired_s // {}) | length) of \(.shards) logged \"Acquired shard\" since \(if .from == "remove" then "removal" else "inject" end)"
  + (if .last_s != null then ", the last \(.last_s) s after" elif (.missing // []) != [] then "; not: \(.missing[:10] | join(" "))" else "" end)
  + (if (.acquired_before_remove // {}) != {} then "; before removal: \(.acquired_before_remove | keys | join(" "))" else "" end)' <<<"$shards_json"
[[ "$shards_json" == null ]] || jq -r 'select(.from == "remove") | .held_evidence
  | if . == null then "held        history'"'"'s logs could not be read for the hold"
    elif . == {} then "held        no shard logged a refused acquire in the hold"
    else "held        " + ([to_entries[] | "shard \(.key): " + ([.value | to_entries[] | "\(.key) \(.value)"] | join(", "))] | join("; ")) + ", in the hold" end' <<<"$shards_json"
[[ "$self_test" == null ]] || jq -r '"self-test   \(if .passed then "passed" else "NOT passed" end): \(.why)"' <<<"$self_test"
[[ "$server_fi" == null ]] || echo "server fi   $(jq -r '"\(.inject), recorded in server_fi; history held \(.history_env_inject | del(.TEMPORAL_SERVER_CONFIG_FILE_PATH) | to_entries | map("\(.key)=\(.value)") | join(" "))"
  + (if .history_env_remove != null then "; removed" elif .remove_error != null then "; removal failed: \(.remove_error)" else "; not removed" end)' <<<"$server_fi")"
[[ "$readings" == null ]] || jq -r '. as $r | [.[] | select(. != null) | keys_unsorted[] | select(. != "window_seconds")] | unique[] as $n
  | "reading     \($n): " + ([("before", "during", "drain", "after") as $w | select($r[$w] != null) | "\($w) \($r[$w][$n] // "null")"] | join(", "))' <<<"$readings"
[[ -z "$inject_error" ]] || echo "inject      failed: $inject_error"
[[ -z "$remove_error" ]] || echo "remove      failed: $remove_error"
[[ -z "$cleanup_error" ]] || echo "cleanup     $cleanup_error"
[[ -n "$reason" ]] && echo "reason      $reason"
echo "$result"
# A mechanism that failed at inject or remove leaves the cell in a state nobody checked: a harness failure, exit 2.
(( ! mech_failed )) || exit 2
[[ "$result" == ADVANCE ]]
