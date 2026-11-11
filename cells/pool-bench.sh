#!/usr/bin/env bash
# The pool's timings, measured against a running pool (laug pool serve -backend compose), at POOL_ADDR: laug pool
# bench <host:port> runs it. Numbers decided before measuring:
#   1. time from laugd start to POOL_MIN warm (read from /status; laugd must be started just before)
#   2. lease latency from warm: p50 and p95 over CYCLES lease/release cycles, each from a full pool
#   3. one cold lease with every warm cell held, so the lease falls through to a create
#   4. TTL teardown at TTL_SHORT seconds: time from lease to the cell leaving /status
# Prints a markdown table; docs/CAPABILITIES.md's pool table holds the lab host's rows, with their record lines.
# Every lease it takes is in held until it is released or its cell is gone. The EXIT trap releases what is left, so
# a failure or a Ctrl-C leaves the pool as the bench found it. Exit: 0 with the table; 1 when the pool refuses a
# lease or a release, with its answer on stderr; 2 when the pool's max leaves no room for phase 3's cold lease, as
# read before the first lease and again before phase 3; 130 on Ctrl-C.
set -euo pipefail

POOL_ADDR="${POOL_ADDR:-http://127.0.0.1:7700}"
CYCLES="${CYCLES:-20}"
TTL_SHORT="${TTL_SHORT:-30}"
SKIP_WARMUP="${SKIP_WARMUP:-0}"

held=()                   # the ids this script holds a lease on, in the order taken
tmp=$(mktemp -d)
answer="$tmp/answer.json" # the pool's answer to the last lease or release

say() { echo "laug · cells/pool-bench.sh: $*" >&2; }
count() { if [ "$1" = 1 ]; then echo "1 $2"; else echo "$1 $2s"; fi; } # count <n> <noun>

status() { curl -sf "$POOL_ADDR/status"; }
min_warm() { status | jq -r .min_warm; }
warm_count() { status | jq -r '[.cells[] | select(.state=="warm")] | length'; }
cell_count() { status | jq -r '.cells | length'; }
has_cell() { status | jq -e --arg id "$1" '.cells[] | select(.id==$id)' >/dev/null; }

wait_warm() { # wait until warm == min
  local want; want=$(min_warm)
  until [ "$(warm_count)" -ge "$want" ]; do sleep 0.5; done
}

post() { # post <path> <json>: the pool's answer goes to $answer; prints "<http code> <seconds>", 000 if none answered
  : >"$answer"
  curl -s -o "$answer" -w '%{http_code} %{time_total}' -X POST "$POOL_ADDR/$1" \
       -H 'content-type: application/json' -d "$2" || true
}

pool_said() { # pool_said <http code>: the pool's answer in $answer, on one line
  if [ "$1" = 000 ]; then printf 'nothing answered at %s' "$POOL_ADDR"; return; fi
  printf 'HTTP %s %s' "$1" "$(tr -d '\n' <"$answer")"
}

lease() { # lease <requester> <ttl>: sets lease_id and lease_s, its time in seconds, and adds the id to held
  local out code
  out=$(post lease "{\"requester\":\"$1\",\"ttl_seconds\":$2}")
  code=${out%% *}; lease_s=${out#* }
  if [ "$code" != 200 ]; then say "the pool refused lease $1: $(pool_said "$code")"; exit 1; fi
  lease_id=$(jq -r .id "$answer")
  held+=("$lease_id")
}

forget() { # forget <id>: takes the id out of held
  local id keep=()
  for id in "${held[@]}"; do [ "$id" = "$1" ] || keep+=("$id"); done
  held=("${keep[@]}")
}

release() { # release <id>: gives the cell back and takes the id out of held
  local out code
  out=$(post release "{\"id\":\"$1\"}"); code=${out%% *}
  if [ "$code" != 200 ]; then say "the pool refused release $1: $(pool_said "$code")"; exit 1; fi
  forget "$1"
}

on_exit() { # releases every lease still held, then exits with the script's status; a second Ctrl-C stops it
  local rc=$? id out code
  trap - EXIT
  if [ "${#held[@]}" -gt 0 ]; then
    say "exit $rc: releasing the $(count "${#held[@]}" lease) it still holds: ${held[*]}"
    for id in "${held[@]}"; do
      out=$(post release "{\"id\":\"$id\"}"); code=${out%% *}
      if [ "$code" = 200 ]; then say "released $id"; else say "release $id refused: $(pool_said "$code")"; fi
    done
  fi
  rm -rf "$tmp"
  exit "$rc"
}
trap on_exit EXIT; trap 'exit 130' INT; trap 'exit 143' TERM; trap 'exit 129' HUP

room() { # exit 2 when the pool's max leaves no room for phase 3's cold lease; sets n, the warm cells to hold
  # Leasing a warm cell starts the pool's refill at once, and the refill takes its slot before it builds
  # (internal/pool, Lease and replenish). So the cold lease needs room for every cell in the table, the refill's and
  # its own. On the lab host, a Firecracker pool on its defaults, min 1 and max 2, had none: the cold
  # lease was the third cell, the pool refused it with 409 "pool is at max cells", and the bench stopped with the warm
  # cell still held, so every later lease was refused. A pool's min and max hold for its life, so the bench asks once
  # the pool is warm, before its first lease, and again before phase 3, for a pool another requester leases from.
  local st min max cells refill need why
  st=$(status)
  min=$(jq -r .min_warm <<<"$st"); max=$(jq -r .max_cells <<<"$st")
  n=$(jq -r '[(.cells // [])[] | select(.state=="warm")] | length' <<<"$st")
  cells=$(jq -r '(.cells // []) | length' <<<"$st")
  refill=0; if [ "$min" -gt 0 ]; then refill=1; fi
  need=$(( cells + refill + 1 ))
  if [ "$max" -ge "$need" ]; then return; fi
  why="the $(count "$n" "warm cell") it holds"
  if [ "$cells" -gt "$n" ]; then why+=", the $(count $(( cells - n )) "other cell") in its table"; fi
  if [ "$refill" = 1 ]; then why+=", 1 the pool builds to refill min $min"; fi
  # The pool's /status does not name its backend, so the line names the flag, not a command to copy.
  say "phase 3's cold lease needs $need cells and the pool's max is $max: $why, and the cold one; serve the pool" \
      "again with the flags it has and -max $need or more, then run the bench again"
  exit 2
}

pct() { # pct <p> file-of-numbers
  sort -n "$2" | awk -v p="$1" '{a[NR]=$1} END { i=int((NR*p)/100 + 0.999999); if (i<1) i=1; printf "%.3f", a[i] }'
}

echo "laug · pool bench against $POOL_ADDR"
echo

# 1. time to min warm
if [ "$SKIP_WARMUP" != 1 ]; then
  t0=$(date +%s.%N)
  wait_warm
  t_warm=$(printf '%.1f' "$(echo "$(date +%s.%N) - $t0" | bc)")
  echo "min warm ($(min_warm) cells) reached ${t_warm} s after this script started"
else
  wait_warm; t_warm="skipped"
fi
room # before the first lease

# 2. warm lease latency
lat="$tmp/lat"
for i in $(seq 1 "$CYCLES"); do
  wait_warm
  lease "gate2-$i" 600; id=$lease_id; t=$lease_s
  echo "$t" >> "$lat"
  printf '  cycle %2d  lease %s in %6.0f ms\n' "$i" "$id" "$(echo "$t * 1000" | bc)"
  release "$id"
done
p50=$(pct 50 "$lat"); p95=$(pct 95 "$lat"); pmax=$(pct 100 "$lat")

# 3. cold lease: hold every warm cell, then one more
wait_warm
room
for i in $(seq 1 "$n"); do lease "drain-$i" 600; done
lease "cold" 600; t_cold=$lease_s
for id in "${held[@]}"; do release "$id"; done

# 4. TTL teardown
wait_warm
lease "ttl" "$TTL_SHORT"; ttl_id=$lease_id
t0=$(date +%s.%N)
while has_cell "$ttl_id"; do sleep 0.5; done
t_ttl=$(printf '%.1f' "$(echo "$(date +%s.%N) - $t0" | bc)")
forget "$ttl_id" # the pool's reaper released it

echo
echo "| What | Number |"
echo "| --- | --- |"
echo "| laug pool serve to $(min_warm) warm (serial replenish) | ${t_warm} s |"
echo "| lease from warm, p50 over $CYCLES cycles | $(echo "$p50 * 1000" | bc | cut -d. -f1) ms |"
echo "| lease from warm, p95 | $(echo "$p95 * 1000" | bc | cut -d. -f1) ms |"
echo "| lease from warm, max | $(echo "$pmax * 1000" | bc | cut -d. -f1) ms |"
echo "| cold lease, pool drained (create + health) | $(printf '%.1f' "$t_cold") s |"
echo "| TTL ${TTL_SHORT} s: lease to cell gone from /status | ${t_ttl} s |"
