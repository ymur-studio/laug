# shellcheck shell=bash
# The leased cell, as faults/run.sh and faults/candidate.sh reach it: its lease, its containers (this host's Docker
# for a compose cell, the guest's over ssh for a microVM cell), its per-cell dynamic config, its logs, and the
# server's soft assertions counted from them. Sourced; defines functions and constants only. The caller sets root
# (the repo root) and RUN_ID, then calls lease_read before anything that touches the cell. Its main functions, in order:
# lease_read runs the lease checks. The pool functions (pool_poll, pool_quiet, pool_json, pool_summary) serve the wait
# for laugd's busy cells. wait_omes_ports waits for Omes's two metrics ports, read from WORKER_METRICS_ADDR and
# CLIENT_METRICS_ADDR so that each item of a gate waits for its own. server_tag_read reads the SERVER_TAG that
# faults/run.sh and faults/candidate.sh both record. thresholds_resolve and thresholds_judge resolve and apply the
# threshold sets per server release (load/thresholds.sh, load/releases/). dynconfig_guest and dynconfig edit the cell's
# own dynamic config. The guest's edit is two statements, cp and then yq, so that set -e stops a failed cp. dynconfig
# --check runs the checks without the edit. cell_logs reads a container's logs, here or in the guest. sa_wait waits for
# every open slice to be counted; faults/run.sh calls it before serverfi, the server fault injector, recreates history.
# The shard helpers (shard_draw, cell_network, shards_acquired, shards_held) serve the shard faults.
# GUEST_DYNCONFIG_ORIG takes an override for make harness-test.

# lease_read <lease-file>: the cell from the lease JSON (id, address, prometheus, toxiproxy, dynconfig, ssh, backend,
# firecracker_version, cache_type). Sets CELL, CELL_ADDRESS, PROM_URL, TOXIPROXY, DYNCONFIG, GUEST_SSH, BACKEND,
# FC_VERSION_LEASE, CACHE_TYPE, POOL_ADDR, PROJECT, GUEST_KEY and GUEST_KNOWN_HOSTS. It exits 2 on a lease that names no
# cell, one whose ssh field is not root@<FC_NET>.<block>.2 or whose dynconfig is not an absolute laug.yaml path, or a
# microVM lease this host cannot reach. It exits 2 too when FC_NET is not two octets. BACKEND is the lease's backend key
# (compose or firecracker); a lease from before the backends named themselves is firecracker when it names a guest, else
# compose. FC_VERSION_LEASE and CACHE_TYPE are a microVM lease's snapshot values, empty when the lease has none.
lease_read() {
  [[ -f "$1" ]] || { echo "no lease at $1; ${LAUG:-bin/laug} lease first" >&2; exit 2; }
  CELL="$(jq -r .id "$1")"; CELL_ADDRESS="$(jq -r .address "$1")"
  PROM_URL="$(jq -r .prometheus "$1")"; TOXIPROXY="$(jq -r .toxiproxy "$1")"
  DYNCONFIG="$(jq -r '.dynconfig // ""' "$1")"; GUEST_SSH="$(jq -r '.ssh // ""' "$1")"
  BACKEND="$(jq -r '.backend // ""' "$1")"
  if [[ -z "$BACKEND" ]]; then if [[ -n "$GUEST_SSH" ]]; then BACKEND=firecracker; else BACKEND=compose; fi; fi
  FC_VERSION_LEASE="$(jq -r '.firecracker_version // ""' "$1")"; CACHE_TYPE="$(jq -r '.cache_type // ""' "$1")"
  POOL_ADDR="${POOL_ADDR:-http://127.0.0.1:7700}"
  # The ssh field goes into ssh's argument list, and the dynconfig field into paths here and in the guest's shell. A
  # lease file holds whatever a local pool answer or an edit put there, so a value shaped like -oProxyCommand=... would
  # run as the user who runs the raun. Only these shapes pass: ssh is root@<net>.<block>.2, the far end of the cell's
  # veth pair (cells/backend-firecracker.sh: block_vars sets c=$NET.$b.2, and create writes ssh as root@$c; NET is
  # FC_NET, 10.254 by default), or nothing for a compose cell. dynconfig is a path, never an address: an absolute path
  # to a file named laug.yaml, with no .. part and no control character. A compose cell's is on this host
  # ($here/run/laug-<id>/dynamicconfig/laug.yaml, cells/backend-compose.sh); a microVM cell's is in the guest. Empty
  # passes here, and dynconfig_check refuses it before an edit. Either refused value is printed quoted, on one line.
  local net="${FC_NET:-10.254}" oct='(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])'
  [[ "$net" =~ ^$oct\.$oct$ ]] || { echo "FC_NET is $(printf %q "$net"), not two octets such as 10.254; refusing lease file $1" >&2; exit 2; }
  if [[ -n "$GUEST_SSH" && ! "$GUEST_SSH" =~ ^root@${net//./\\.}\.$oct\.2$ ]]; then
    echo "lease file $1: its ssh field is $(printf %q "$GUEST_SSH"), not root@$net.<block>.2 (FC_NET, which must be the pool's); refusing it" >&2; exit 2
  fi
  if [[ -n "$DYNCONFIG" ]] && { [[ "$DYNCONFIG" != /*/laug.yaml || "$DYNCONFIG" == *[[:cntrl:]]* ]] \
      || [[ "/$DYNCONFIG/" == */../* ]]; }; then
    echo "lease file $1: its dynconfig field is $(printf %q "$DYNCONFIG"), not an absolute path to a laug.yaml with no .. part or control character; refusing it" >&2; exit 2
  fi

  # Every mechanism below acts on one cell, so the lease must name a cell laugd still holds as leased. An empty
  # or stale lease file would otherwise send load and faults to block 0's cell, which may be someone else's.
  [[ "$CELL" =~ ^[0-9a-f]{8}$ ]] || { echo "lease file $1 has no cell id ('$CELL'); ${LAUG:-bin/laug} lease first" >&2; exit 2; }
  PROJECT="laug-$CELL"
  # A microVM cell (cells/backend-firecracker.sh) runs its containers in the guest's Docker, as compose project laug
  # in every clone. The lease names the guest as ssh, and its dynconfig is a path inside the guest. The key is the
  # pool's guest key, which laug pool serve -backend firecracker installs for the user who started it. Every raun on
  # a microVM cell needs it, whatever the fault: the soft assertions are read from the guest's Docker over ssh, besides
  # the docker and dynconfig mechanisms. Without it every window's soft assertions would record null.
  GUEST_KEY="${FC_GUEST_KEY:-/run/laug/guest.key}"
  GUEST_KNOWN_HOSTS="${FC_GUEST_KNOWN_HOSTS:-/run/laug/guest.known_hosts}"
  if [[ -n "$GUEST_SSH" ]]; then
    PROJECT=laug
    [[ "$DYNCONFIG" == /*/laug.yaml ]] || { echo "the lease is a microVM cell with no dynconfig path in the guest ('$DYNCONFIG')" >&2; exit 2; }
    [[ -r "$GUEST_KEY" ]] || { echo "the lease is a microVM cell and $GUEST_KEY is not readable; a raun reaches the guest over ssh; ${LAUG:-bin/laug} pool serve -backend firecracker installs the key for FC_KEY_USER" >&2; exit 2; }
    [[ -r "$GUEST_KNOWN_HOSTS" ]] || { echo "the lease is a microVM cell and $GUEST_KNOWN_HOSTS is not readable; a raun checks the guest's host key over ssh; ${LAUG:-bin/laug} pool serve -backend firecracker installs the file for every user" >&2; exit 2; }
  fi
}
# host_virtualized: true when /proc/cpuinfo carries the hypervisor flag, so laug itself runs on a VM, else false. On a
# VM host a microVM cell runs nested; a compose cell is a container on that VM. Every record carries it beside BACKEND.
host_virtualized() { if grep -qw hypervisor /proc/cpuinfo 2>/dev/null; then echo true; else echo false; fi; }
# -n: never read this script's stdin. -F /dev/null and IdentitiesOnly: only the guest key, whatever the user's
# ssh config and agent hold (the guest's sshd allows 6 tries). StrictHostKeyChecking=yes against GUEST_KNOWN_HOSTS,
# the host key the image build recorded: the pin proves the guest is laug's image, not which cell, since every clone
# answers with the same key; gcell's marker still names the cell.
gsh() {
  ssh -n -F /dev/null -i "$GUEST_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes \
    -o UserKnownHostsFile="$GUEST_KNOWN_HOSTS" -o LogLevel=ERROR -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 \
    -- "$GUEST_SSH" "$@"
}
# Every clone answers to project laug, and a block's address passes to the next cell on it, so each guest command
# first checks, in the same session, that the guest still holds this lease's cell: the marker create wrote.
gcell() {
  gsh "[ \"\$(cat $(printf %q "$(dirname "$DYNCONFIG")/cell-id") 2>/dev/null)\" = $CELL ] || { echo 'the guest at $GUEST_SSH no longer holds cell $CELL' >&2; exit 3; }; $1"
}
dk() { # docker against the cell's own daemon: the host's, or the guest's over ssh (arguments quoted for its shell)
  if [[ -n "$GUEST_SSH" ]]; then gcell "docker $(printf '%q ' "$@")"; else docker "$@"; fi
}
assert_leased() {
  curl -sf "$POOL_ADDR/status" | jq -e --arg id "$CELL" '.cells[] | select(.id == $id and .state == "leased")' >/dev/null \
    || { echo "cell $CELL is not leased at $POOL_ADDR/status; refusing to touch it" >&2; return 2; }
}
log() { echo "[$RUN_ID] $(date -u +%H:%M:%SZ) $*" >&2; }

# The pool. laugd builds a cell on this host whenever a lease leaves it under its min warm cells, and destroys the
# cell a lease ends; /status shows such a cell busy, being created, reset or destroyed (internal/pool/pool.go,
# stateBusy, create, Lease, Release; read in source). With POOL_MIN 1
# every lease makes laugd build a replacement cell during the raun's before window, and that build's load falls in a
# window the raun judges (inference).
# So before its first load a raun or a candidate waits until laugd shows no busy cell, POOL_QUIET_WAIT_S (300) s at most
# (pool_quiet), and records the most busy cells it saw at once in each window (pool_poll, pool_json): recorded, not
# judged. The polls are a sample: one in each pass of a loop the scripts already run (every 5 s while a raun waits for
# its fault to land and then to recover, every 30 s of a candidate's load) and one at each window boundary, so a cell
# built and finished between two of them is not seen. A compose cell's create lasts longer than one
# 5 s poll (a cold lease's time: cells/results/2026-10-23-boot1.txt:367, in docs/CAPABILITIES.md's pool table).
# The leased cell itself is not counted here: faults/run.sh and faults/candidate.sh call assert_leased again right after
# the wait, so a leased cell that laugd resets or reaps while the raun waits stops the run before its load. laugd that
# does not answer is recorded as null, and the run goes on: the wait is null, and so is a window none of whose polls
# was answered.
POOL_QUIET_WAIT_S="${POOL_QUIET_WAIT_S:-300}"; POOL_QUIET_POLL_S=5; pool_quiet_wait_s=null; pool_quiet_left=null
# pool_busy: how many cells other than CELL laugd's /status shows busy; fails when laugd cannot be read, or answers
# with something that is not its status. A pool with no cell lists cells as null (a nil slice in main.go's statusView).
pool_busy() {
  local s; s="$(curl -sf -m 5 "$POOL_ADDR/status" 2>/dev/null)" || return 1
  jq -e --arg id "$CELL" 'if type == "object" and has("cells") and ((.cells | type) == "array" or .cells == null)
    then [(.cells // [])[] | select(.state == "busy" and .id != $id)] | length else error("not laugd status") end' <<<"$s" 2>/dev/null
}
# pool_poll <window>...: one read of /status. Its busy count, or null when laugd did not answer, goes to each window
# named: a poll at a boundary belongs to the windows on both sides. The caller sets pool_dir, an empty directory.
pool_poll() {
  local n w; n="$(pool_busy)" || n=null
  for w in "$@"; do echo "$n" >> "$pool_dir/$w"; done
}
# pool_quiet <window>: before a load, poll /status every POOL_QUIET_POLL_S until it shows no busy cell, for
# POOL_QUIET_WAIT_S at most; the last poll goes to window, the one the load opens. Sets pool_quiet_wait_s (seconds
# waited; null when laugd did not answer the first poll) and pool_quiet_left (busy cells when the wait ended; null when
# laugd did not answer). A cell still busy at the bound is logged and the load starts anyway; the window records it.
pool_quiet() {
  local t0 now n
  t0=$(date +%s); pool_quiet_wait_s=null; pool_quiet_left=null
  if ! n="$(pool_busy)"; then
    echo null >> "$pool_dir/$1"; log "pool: laugd at $POOL_ADDR did not answer; not waited for, recorded null"; return 0
  fi
  (( n == 0 )) || log "pool: $n other cell(s) busy at laugd (being created, reset or destroyed); waiting up to ${POOL_QUIET_WAIT_S} s before the load"
  now=$t0
  while (( n > 0 && now - t0 < POOL_QUIET_WAIT_S )); do
    sleep $(( POOL_QUIET_WAIT_S - (now - t0) < POOL_QUIET_POLL_S ? POOL_QUIET_WAIT_S - (now - t0) : POOL_QUIET_POLL_S ))
    now=$(date +%s)
    n="$(pool_busy)" || { n=null; break; }
  done
  pool_quiet_wait_s=$(( $(date +%s) - t0 )); pool_quiet_left="$n"
  echo "$n" >> "$pool_dir/$1"
  if [[ "$n" == null ]]; then log "pool: laugd stopped answering after ${pool_quiet_wait_s} s of waiting; starting the load"
  elif (( n > 0 )); then log "pool: $n cell(s) still busy at laugd after ${pool_quiet_wait_s} s, the bound (POOL_QUIET_WAIT_S); starting the load anyway"
  elif (( pool_quiet_wait_s > 0 )); then log "pool: no busy cell at laugd after ${pool_quiet_wait_s} s; starting the load"; fi
}
# pool_window <window>: the most busy cells one poll saw; null when laugd answered none of its polls, or none was made
# (jq -s on a missing file prints its result for no input and exits 2, so the file is checked first.)
pool_window() {
  local v=""; [[ ! -s "$pool_dir/$1" ]] || v="$(jq -sc 'map(select(. != null)) | if length == 0 then null else max end' "$pool_dir/$1" 2>/dev/null)" || v=""
  echo "${v:-null}"
}
pool_json() { # pool_json <window>...: {"<window>": pool_window, ...}, for the run's JSON as pool_busy
  local out="{}" w; for w in "$@"; do out="$(jq -c --arg w "$w" --argjson v "$(pool_window "$w")" '.[$w] = $v' <<<"$out")"; done; echo "$out"
}
# pool_summary <window>...: the summary's pool line: the wait, then each window that was polled, and a warning line
# when a window saw a busy cell.
pool_summary() {
  local w v by="" busy=""
  for w in "$@"; do
    [[ -e "$pool_dir/$w" ]] || continue
    v="$(pool_window "$w")"; by+="${by:+, }$w ${v/#null/not read}"
    [[ "$v" == null || "$v" == 0 ]] || busy+="${busy:+, }$w $v"
  done
  if [[ "$pool_quiet_wait_s" == null ]]; then printf 'pool        laugd at %s did not answer before the load, so nothing was waited for' "$POOL_ADDR"
  elif [[ "$pool_quiet_left" == null ]]; then printf 'pool        laugd stopped answering after %s s of waiting before the load' "$pool_quiet_wait_s"
  elif (( pool_quiet_left > 0 )); then printf 'pool        %s cell(s) still busy at laugd after %s s, the bound (POOL_QUIET_WAIT_S); the load started anyway' "$pool_quiet_left" "$pool_quiet_wait_s"
  elif (( pool_quiet_wait_s > 0 )); then printf 'pool        waited %s s before the load until laugd showed no busy cell' "$pool_quiet_wait_s"
  else printf 'pool        no busy cell at laugd before the load'; fi
  [[ -z "$by" ]] || printf '; busy cells by window, most at once: %s' "$by"
  echo
  [[ -z "$busy" ]] || echo "warning     laugd showed busy cells ($busy): cells being created, reset or destroyed share this host with the cell under test; recorded, not judged"
}

# Omes's worker and client serve metrics on WORKER_METRICS_ADDR and CLIENT_METRICS_ADDR, 0.0.0.0:9092 and 0.0.0.0:9093
# when unset or empty, as load/run-omes.sh reads them; faults/run.sh and faults/candidate.sh pass both on to it through
# their environment. A load generator from the last run can hold a port for its 30 s graceful shutdown. Wait up to 40 s
# for each. The ports come from the addresses: a gate runs its items at once on one host, each with
# its own pair (gate/run.sh, step 7), so an item waits for its own two ports and not for another item's. laug fault
# and laug raun set neither, so they wait for 9092 and 9093 as before. Returns 2, and says why, when ss is not on
# PATH, or an address does not end in :<port> with a port from 1 to 65535; all three are checked before any wait.
# grep reads ss's lines from a string, not a pipe. Under pipefail, which both callers set, grep -q can find the port
# and quit while ss still writes. ss then exits 141, and a taken port reads as free.
wait_omes_ports() {
  local addr port ports=() i
  command -v ss >/dev/null || { echo "no ss on PATH (iproute2): the wait for the Omes metrics ports reads the listening ports with it, and without it a taken port would read as free" >&2; return 2; }
  for addr in "${WORKER_METRICS_ADDR:-0.0.0.0:9092}" "${CLIENT_METRICS_ADDR:-0.0.0.0:9093}"; do
    [[ "$addr" =~ :([1-9][0-9]{0,4})$ ]] && (( BASH_REMATCH[1] <= 65535 )) \
      || { echo "Omes metrics address '$addr' (WORKER_METRICS_ADDR, CLIENT_METRICS_ADDR) does not end in :<port>, a port from 1 to 65535" >&2; return 2; }
    ports+=("${BASH_REMATCH[1]}")
  done
  for port in "${ports[@]}"; do
    for i in $(seq 1 40); do grep -q ":$port " <<<"$(ss -ltn 2>/dev/null)" || break; sleep 1; done
  done
}

# Mechanisms. Containers are found by compose labels, so a service name can only resolve inside the leased
# cell's project, and anything but exactly one match stops the run.
container() {
  local ids; ids="$(dk ps -aq --filter "label=com.docker.compose.project=$PROJECT" --filter "label=com.docker.compose.service=$1")"
  [[ -n "$ids" && "$(wc -l <<<"$ids")" -eq 1 ]] || { echo "expected one $1 container in $PROJECT, found: ${ids:-none}" >&2; return 2; }
  echo "$ids"
}
# server_tag_read <lease-file>: SERVER_TAG, the server tag the cell runs: its temporal-history container's image tag,
# read through the cell's own Docker (the guest's for a microVM cell). A lease from cells/backend-compose.sh names the
# tag it created the cell with, and the two must agree, or it says so and returns 2. Unread (the guest unreachable),
# the lease's tag stands, else none: the run records null. Moved here from faults/run.sh so that faults/candidate.sh
# reads and checks the tag the same way.
server_tag_read() {
  local lease_tag cid img
  lease_tag="$(jq -r '.server_tag // ""' "$1")"; SERVER_TAG=""
  if cid="$(container temporal-history 2>/dev/null)" && img="$(dk inspect -f '{{.Config.Image}}' "$cid" 2>/dev/null)" \
      && [[ "$img" =~ ^temporalio/server:([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
    SERVER_TAG="${BASH_REMATCH[1]}"
    [[ -z "$lease_tag" || "$lease_tag" == "$SERVER_TAG" ]] \
      || { echo "the lease says server $lease_tag and cell $CELL's temporal-history runs $img; refusing to judge one as the other" >&2; return 2; }
  else
    SERVER_TAG="$lease_tag"; log "server tag: could not read cell $CELL's temporal-history image; recording ${SERVER_TAG:-null}${SERVER_TAG:+ from the lease}"
  fi
}
# Thresholds. thresholds_resolve <server-tag> <file>: the threshold set a run on that release is judged by, written to
# file by load/thresholds.sh: load/steady-state.yaml plus load/releases/<major>.<minor>.yaml when one exists, or
# THRESHOLDS as given when it is set. Sets TH_FILE (the file, for load/read-thresholds.sh and the signal lookup), TH_NOTE
# (one line naming the files, and why), TH_RELEASE (the release file applied, or empty) and TH_OVERRIDES (its
# overrides, a JSON list of {name, no_data, reason, evidence}). Returns 2 when the set does not resolve, a release file
# that cannot be applied as written among the reasons; load/thresholds.sh says why on stderr.
thresholds_resolve() {
  "$root/load/thresholds.sh" "$1" > "$2" || return 2
  TH_FILE="$2"
  TH_NOTE="$(yq -r '.resolved.note' "$2")"; TH_RELEASE="$(yq -r '.resolved.release_file // ""' "$2")"
  TH_OVERRIDES="$(yq -o=json -I=0 '.resolved.overrides' "$2")"
}
# thresholds_judge <readings-json>: {"failing": ["<name>=<value>", ...], "not_produced": ["<name>", ...]} against TH_FILE,
# for faults/run.sh's before and after windows and faults/candidate.sh's before window. A threshold with no max is
# skipped. A reading over its max fails, and so does one with no data, unless its threshold says no_data: not_judged (a
# release file): then it is listed under not_produced, so a caller records it rather than drop it. Both sides go through
# tonumber, as load/verdict.sh compares in awk: in jq a number is never greater than a string (load/thresholds.sh refuses
# a max that is not a number, so this is a second guard).
thresholds_judge() {
  jq -c --argjson th "$(yq -o=json -I=0 '[.thresholds[] | select(.max != null) | {"name": .name, "max": .max, "no_data": (.no_data // "exceeds")}]' "$TH_FILE")" \
    '. as $r | [$th[] | . + {v: ($r[.name] // null)}] as $t
     | {failing: [$t[] | select((.v == null and .no_data != "not_judged") or (.v != null and (.v | tonumber) > (.max | tonumber))) | "\(.name)=\(.v // "null")"],
        not_produced: [$t[] | select(.v == null and .no_data == "not_judged") | .name]}' <<<"$1"
}

# Dynamic config is per cell (cells/backend-compose.sh copies the template to cells/run/laug-<id>/). Edit only
# that copy, and only after every server role of the leased cell shows this cell's id in the mounted directory.
# Each role polls the file every 60 s of its own clock (pollInterval in the 1.31.0 image's config template),
# measured 0.2 to 62.2 s after an edit on the dev host, separately on each role; the frontend rate limiter then
# refreshes within another minute (server 1.31.0 service/frontend/configs/quotas.go:348,
# common/quotas/dynamic_rate_limiter_impl.go:130-139).
SERVER_ROLES="temporal-frontend temporal-frontend2 temporal-history temporal-matching temporal-worker"
dynconfig_check() {
  [[ -n "$DYNCONFIG" && -f "$DYNCONFIG" ]] || { echo "lease has no per-cell dynamic config file ('$DYNCONFIG')" >&2; return 2; }
  local svc cid got
  # The file to edit sits beside this cell's marker, and every role mounts a directory with the same marker.
  got="$(cat "$(dirname "$DYNCONFIG")/cell-id" 2>/dev/null)" || got=""
  [[ "$got" == "$CELL" ]] || { echo "$DYNCONFIG belongs to cell '$got', not $CELL" >&2; return 2; }
  for svc in $SERVER_ROLES; do
    cid="$(container "$svc")" || return 2
    got="$(docker exec "$cid" cat /etc/temporal/config/dynamicconfig/cell-id 2>/dev/null)" || got=""
    [[ "$got" == "$CELL" ]] || { echo "$svc in $PROJECT mounts the dynamic config of cell '$got', not $CELL" >&2; return 2; }
  done
}
# The same checks and edit for a microVM cell, in one ssh session inside the guest. Restore copies the file the cell
# started with (create saves it outside the mounted directory), not the host's template, which may be newer.
# The edit is two statements, not cp && yq: set -e does not stop on a failed command that is not the last of an &&
# list (seen: dash -c 'set -e; false && true; echo on' prints on), so a failed cp went on to move the empty
# mktemp file over the cell's laug.yaml.
GUEST_DYNCONFIG_ORIG="${GUEST_DYNCONFIG_ORIG:-/opt/laug/cells/laug.yaml.orig}"
# The image's copy of cells/compose (cells/rootfs/build-image.sh), where serverfi recreates history in the guest. Both
# guest paths take an override because make harness-test points them at a fake guest.
GUEST_COMPOSE_DIR="${GUEST_COMPOSE_DIR:-/opt/laug/cells/compose}"
dynconfig_guest() {
  local f d edit=""
  f="$(printf %q "$DYNCONFIG")"; d="$(printf %q "$(dirname "$DYNCONFIG")")"
  if [[ "$1" == restore ]]; then edit="cp $GUEST_DYNCONFIG_ORIG \$t"
  elif [[ "$1" != --check ]]; then edit="cp $f \$t; yq -i '.[\"$1\"][0].value = $2' \$t"; fi
  gcell "set -e; test -f $f || { echo 'no $DYNCONFIG in the guest' >&2; exit 2; }
    for s in $SERVER_ROLES; do
      ids=\$(docker ps -aq --filter label=com.docker.compose.project=laug --filter label=com.docker.compose.service=\$s)
      [ \$(printf '%s' \"\$ids\" | grep -c .) -eq 1 ] || { echo \"expected one \$s container in laug, found: \${ids:-none}\" >&2; exit 2; }
      [ \"\$(docker exec \$ids cat /etc/temporal/config/dynamicconfig/cell-id 2>/dev/null)\" = $CELL ] \\
        || { echo \"\$s mounts the dynamic config of another cell\" >&2; exit 2; }
    done${edit:+
    t=\$(mktemp $d/.laug.yaml.XXXXXX); $edit; chmod 0644 \$t; mv \$t $f}"
}
# dynconfig <key> <value>, dynconfig restore to put the template back, or dynconfig --check: the checks alone, no edit.
# DYNCONFIG_TEMPLATE is the template restore copies on a compose cell, cells/compose/dynamicconfig/laug.yaml unless set:
# make harness-test points it at a fixture, so its checks hold whatever the checkout's laug.yaml says.
DYNCONFIG_TEMPLATE="${DYNCONFIG_TEMPLATE:-$root/cells/compose/dynamicconfig/laug.yaml}"
dynconfig() {
  if [[ -n "$GUEST_SSH" ]]; then dynconfig_guest "$@"; return; fi
  dynconfig_check || return 2
  [[ "$1" != --check ]] || return 0
  # mktemp makes the file 0600 and mv keeps it; the server runs as uid 1000, which can read it only while
  # the host user is uid 1000 too, so make it world-readable like the copy create writes.
  local tmp; tmp="$(mktemp "$(dirname "$DYNCONFIG")/.laug.yaml.XXXXXX")"; chmod 0644 "$tmp"
  if [[ "$1" == restore ]]; then cp "$DYNCONFIG_TEMPLATE" "$tmp"
  else cp "$DYNCONFIG" "$tmp"; yq -i ".[\"$1\"][0].value = $2" "$tmp"; fi
  mv "$tmp" "$DYNCONFIG"
}
dynconfig_get() { # dynconfig_get <key>: the value of the key's first entry in the cell's file, here or in the guest
  if [[ -n "$GUEST_SSH" ]]; then gcell "yq -r '.[\"$1\"][0].value' $(printf %q "$DYNCONFIG")"
  else [[ -f "$DYNCONFIG" ]] || { echo "lease has no per-cell dynamic config file ('$DYNCONFIG')" >&2; return 2; }; yq -r ".[\"$1\"][0].value" "$DYNCONFIG"; fi
}

# Soft assertions per window, [start, end) in epoch seconds, the same windows as the failed iterations. A window is
# counted in slices of at most SA_SLICE_S (30) seconds, each as it closes and in the background, so nothing waits on
# it: the json-file driver keeps 10 MB x 3 per container (cells/compose/cell.yml), and history logged 8.3 MB in the
# first 31 s of persistence-latency on the dev host, so a 120 s hold counted only at its close would
# lose its start. The logs come from the cell's own daemon, the guest's over ssh for a microVM cell, and are counted
# here with --file, so the guest image needs no laug script. docker logs takes both ends as inclusive (seen on
# Docker 29.5.3; the guest's docker.io runs them in every microVM raun, ends not checked), so --until is the end less
# 1 ns. Read that way on the dev host, one raun's during, drain and after windows held 4891, 100 and 0 history lines,
# and the three as one span 4991.
# The caller sets sa_dir (an empty directory) and sa_pids=() before the first sa_open, waits on sa_pids (sa_wait)
# before sa_window, and removes sa_dir.
SA_SLICE_S="${SA_SLICE_S:-30}"
cell_logs() { # cell_logs <container> <since> <until>: both streams, merged where docker runs so lines stay whole
  if [[ -n "$GUEST_SSH" ]]; then gcell "docker logs --since $2 --until $3 $(printf %q "$1") 2>&1"
  else docker logs --since "$2" --until "$3" "$1" 2>&1; fi
}
sa_count() { # sa_count <start> <end>: cells/softasserts.sh's JSON; a role whose container or logs failed is null
  local d svc cid args=() rc=0
  d="$(mktemp -d "$sa_dir/logs.XXXXXX")"
  for svc in $SERVER_ROLES; do
    cid="$(container "$svc")" && cell_logs "$cid" "$1" "$(( $2 - 1 )).999999999" > "$d/$svc" && args+=(--file "$svc=$d/$svc")
  done
  if (( ${#args[@]} )); then "$root/cells/softasserts.sh" "${args[@]}" || rc=$?
  else echo "no role's logs could be read" >&2; rc=1; fi
  rm -rf "$d"; return "$rc"
}
sa_win=""; sa_from=""; sa_n=0
sa_open() { sa_win="$1"; sa_from="$2"; }  # sa_open <window> <start>
sa_cut() { # sa_cut <t>: count the open slice, [sa_from, t), in the background; the next slice starts at t
  [[ -n "$sa_win" ]] && (( $1 > sa_from )) || return 0
  sa_n=$(( sa_n + 1 )); echo "$sa_n" >> "$sa_dir/$sa_win.slices"
  ( sa_count "$sa_from" "$1" > "$sa_dir/$sa_n.json" 2> "$sa_dir/$sa_n.err" ) &
  sa_pids+=("$!"); sa_from="$1"
}
sa_close() { sa_cut "$1"; sa_win=""; }  # sa_close <t>: the open window ends at t
sa_wait() { local p; for p in "${sa_pids[@]+"${sa_pids[@]}"}"; do wait "$p" || true; done; sa_pids=(); }  # every slice counted
sa_tick() { [[ -n "$sa_win" ]] || return 0; local now; now=$(date +%s); (( now - sa_from < SA_SLICE_S )) || sa_cut "$now"; }
sa_sleep() { # sa_sleep <s>: sleep, cutting a slice every SA_SLICE_S
  local left="$1" n
  while (( left > 0 )); do n=$(( left < SA_SLICE_S ? left : SA_SLICE_S )); sleep "$n"; left=$(( left - n )); sa_tick; done
}
sa_window() { # sa_window <window>: its slices merged; a role is null if any slice could not read it; null if never opened
  [[ -s "$sa_dir/$1.slices" ]] || { echo null; return 0; }
  local n; while read -r n; do jq -ce 'select(type == "object")' "$sa_dir/$n.json" 2>/dev/null || echo null; done < "$sa_dir/$1.slices" \
    | jq -sc 'if all(.[]; . == null) then null else . as $s
        | ([$s[] | select(. != null) | keys_unsorted[]] | unique - ["first"]) as $roles
        | reduce $roles[] as $r ({}; .[$r] = (if any($s[]; . == null or .[$r] == null) then null else [$s[][$r]] | add end))
        + {first: ([$s[] | select(. != null) | .first[]] | reduce .[] as $m ([]; if any(.[]; . == $m) then . else . + [$m] end) | .[:10])}
      end'
}

# Shards. A cell has NUM_HISTORY_SHARDS (512, cells/compose/cell.yml) history shards, numbered 1 to 512: the server
# maps a workflow to hash % shards + 1 and refuses an id under 1 or over the count (server 1.31.0 common/util.go:389-396,
# service/history/shard/controller_impl.go:546-554; read in source).
NUM_SHARDS="$(yq -r '."x-server-env".NUM_HISTORY_SHARDS' "$root/cells/compose/cell.yml")"
# shard_draw <k> <seed> [n]: k distinct shard ids out of 1..n (NUM_SHARDS), one per line in draw order. A partial
# Fisher-Yates shuffle driven by the Park-Miller generator (x = 16807 x mod 2^31 - 1, exact in awk's doubles), so a
# seed names the same shards on every host and every awk; make harness-test pins seed 1014's first draws. The seed is
# 1 to 2147483646 (0 is a fixed point of the generator).
shard_draw() {
  local k="$1" seed="$2" n="${3:-$NUM_SHARDS}"
  [[ "$n" =~ ^[0-9]+$ && "$k" =~ ^[0-9]+$ ]] && (( n >= 1 && k >= 1 && k <= n )) \
    || { echo "shard draw: k must be 1 to $n shards, not '$k'" >&2; return 2; }
  [[ "$seed" =~ ^[0-9]{1,10}$ ]] && (( seed >= 1 && seed <= 2147483646 )) \
    || { echo "shard draw: the seed must be 1 to 2147483646, not '$seed'" >&2; return 2; }
  awk -v k="$k" -v x="$seed" -v n="$n" 'BEGIN {
    for (i = 1; i <= n; i++) a[i] = i
    for (i = 1; i <= k; i++) { x = (x * 16807) % 2147483647; j = i + x % (n - i + 1); t = a[i]; a[i] = a[j]; a[j] = t; print a[i] }
  }'
}
# cell_network: the cell's compose network, read from its nginx container, the only gRPC address a client sees
# (temporal-nginx:7233). A container started on it reaches the cell's frontends the way the cell's own
# temporal-create-namespace does. Exactly one network, or the run stops.
cell_network() {
  local cid nets
  cid="$(container temporal-nginx)" || return 2
  nets="$(dk inspect -f '{{json .NetworkSettings.Networks}}' "$cid" | jq -r 'keys[]')" || nets=""
  [[ -n "$nets" && "$(wc -l <<<"$nets")" -eq 1 ]] \
    || { echo "expected temporal-nginx in $PROJECT on one network, found: ${nets:-none}" >&2; return 2; }
  echo "$nets"
}
# shards_acquired <since> <id>...: {"<id>": seconds from since to that shard's first "Acquired shard" line at or after
# since}, for each id that logged one; exits 2 when history's logs could not be read. History logs the line once a
# shard holds its range id again (server 1.31.0 service/history/shard/context_impl.go:2005, after renewRangeLocked),
# through a logger tagged "shard-id" (context_impl.go:2114, common/log/tag/tags.go:555-557). Not a counter:
# sharditem_created_count counts a shard context put in the controller's map before its acquire runs
# (controller_impl.go:262-272), so it counts a shard that never acquires, and it says nothing about which shard.
# The line's shape is read in source and seen on the lab host (cells/results/2026-10-23-boot2.txt, R5); times are
# whole seconds of the line's ts.
shards_acquired() {
  local since="$1" cid f rc=0; shift
  cid="$(container temporal-history)" || return 2
  f="$(mktemp)"
  cell_logs "$cid" "$since" "$(date +%s)" > "$f" || rc=$?
  if (( rc )); then rm -f "$f"; echo "could not read temporal-history's logs in $PROJECT (exit $rc)" >&2; return 2; fi
  jq -Rnc --argjson since "$since" --argjson ids "$(printf '%s\n' "$@" | jq -Rsc 'split("\n") | map(select(. != ""))')" '
    reduce (inputs | fromjson? | select(type == "object" and .msg == "Acquired shard")
            | {id: (.["shard-id"] | tostring), t: (try (.ts | sub("\\.[0-9]+"; "") | fromdateiso8601) catch null)}
            | select(.t != null and .t >= $since and (.id as $i | any($ids[]; . == $i)))) as $l
      ({}; if has($l.id) then . else .[$l.id] = ($l.t - $since) end)' "$f" || rc=$?
  rm -f "$f"; return "$rc"
}
# shards_held <since> <until> <id>...: {"<id>": {"<message>": n, ...}} for each id that logged one of the lines a shard
# logs when its acquire is refused, in [since, until]: "Persistent store operation failure" (the range id write,
# store-operation update-shard), then "Error acquiring shard" and "Couldn't acquire shard" (server 1.31.0
# service/history/shard/context_impl.go:1214-1219, 2041-2062), all through the logger tagged "shard-id"
# (context_impl.go:2114-2125). A shard whose row was bumped and then failed a write is stopped, and every new context
# for it logs these on its acquire, once per request or acquire pass. A shard that was never disturbed logs none, so
# these lines, not the absence of "Acquired shard", show that a bump held the shard. Exits 2 when history's logs could
# not be read. The lines' shape is read in source and seen on the lab host (cells/results/2026-10-23-boot2.txt, R7).
SHARD_HELD_MSGS='["Persistent store operation failure", "Error acquiring shard", "Couldn'"'"'t acquire shard"]'
shards_held() {
  local since="$1" until="$2" cid f rc=0; shift 2
  cid="$(container temporal-history)" || return 2
  f="$(mktemp)"
  cell_logs "$cid" "$since" "$until" > "$f" || rc=$?
  if (( rc )); then rm -f "$f"; echo "could not read temporal-history's logs in $PROJECT (exit $rc)" >&2; return 2; fi
  jq -Rnc --argjson msgs "$SHARD_HELD_MSGS" --argjson ids "$(printf '%s\n' "$@" | jq -Rsc 'split("\n") | map(select(. != ""))')" '
    reduce (inputs | fromjson? | select(type == "object" and (.msg as $m | any($msgs[]; . == $m)))
            | {id: (.["shard-id"] | tostring), msg} | select(.id as $i | any($ids[]; . == $i))) as $l
      ({}; .[$l.id][$l.msg] += 1)' "$f" || rc=$?
  rm -f "$f"; return "$rc"
}
