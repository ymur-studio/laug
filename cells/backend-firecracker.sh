#!/usr/bin/env bash
# Firecracker backend for the pool: one microVM per cell, restored from the snapshot `make fc-snapshot` took.
# Contract, as backend-compose.sh: create <id> -> one JSON line on stdout; destroy <id>; health <id> -> exit 0 when
# warm; LAUG_BLOCK in env. Five more verbs: prep, sweep, status; vmmetrics <id>, which flushes a cell's VMM metrics
# and prints its metrics file and line count; and vmfault <id> drive <json>, which PATCHes a rate limiter on drive
# rootfs and nothing else (the vm mechanism in faults/run.sh, through laugd's POST /vmfault). Linux x86_64
# with /dev/kvm, as root: laug pool serve -backend firecracker runs prep and then the pool, and laug pool down
# -backend firecracker -remove-cells sweep.
#
# Every clone is the same machine inside: guest 172.16.0.2 behind tap0 at 172.16.0.1, and its disk at the path the
# snapshot recorded. So each VMM runs in its own network and mount namespaces, which only it holds:
#
#   host: laugfc<b> 10.254.<b>.1/30 ── veth ── cell netns: veth0 10.254.<b>.2/30, tap0 172.16.0.1/30 ── guest 172.16.0.2
#
# In the cell's netns, traffic from the host to 10.254.<b>.2 is DNATed to the guest. The guest can answer, and can
# start a connection to one place only: the host's SDK metrics ports 9091 to 9093, so the cell's Prometheus scrapes
# the HealthCheck and Omes workers (it dials 172.16.0.1, DNATed to 10.254.<b>.1). The netns has no default route. Inside the VMM's mount namespace only, the recorded disk path
# resolves to the cell's own reflink copy of golden.ext4. When the VMM dies, the kernel frees both namespaces, with
# the veth pair and the tap (seen: the host end gone 0.06 s after a kill -9, and the host's link count back to its
# baseline, so no tap returned to the host). b = LAUG_BLOCK names every per-cell resource, so the
# address survives a reset, as the compose backend's ports do (laug reset never re-reads the lease).
#
# The cells publish nothing on a host interface: 10.254.<b>.2 is reachable from this host only. Deny rules in
# FORWARD (and DOCKER-USER) drop anything forwarded to or from laugfc+, so containers and other machines cannot
# reach a cell. From another machine, use ssh -L to 10.254.<b>.2.
#
# Faults reach a cell's containers in the guest's Docker over ssh: the lease's ssh field is root@10.254.<b>.2, the
# guest's compose project is laug (snapshot.sh starts it so, and every clone keeps it), and its dynconfig is the
# file inside the guest. prep installs the guest key at $RUN_DIR/guest.key for FC_KEY_USER (default: the sudo
# user), mode 0600, and beside it guest.known_hosts, mode 0644: the guest's host key the image build recorded, which
# every ssh to a guest checks. Root in a guest is less than a compose pool gives the same user: the host's
# Docker socket.
#
# FC_JAIL=1 (fc-lib.sh), the default since boot 10, starts each VMM through Firecracker's jailer, still
# inside the network and mount namespaces unshare makes: as uid 20000 plus b and gid 20000, chrooted to
# $FC_DIR/jail/firecracker/laug-b<b>/root, in a pid namespace and the cgroup /sys/fs/cgroup/laug/laug-b<b> of its own
# (jail_argv in fc-lib.sh). Before the jailer runs, a short script in the new mount namespace binds the cell's disk,
# vm.snap and vm.mem (the last two read-only) onto empty files in the jail at their host paths, so the snapshot's
# recorded path holds inside the jail and the load's body is unchanged. $RUN_DIR/b<b> stays the interface: fc.sock,
# vmm.log and metrics.json there are links to the jail's, and fc.pid holds the VMM's pid once create accepts it. A
# jailed VMM's command line names its id, --id laug-b<b>, not its socket; every matcher here takes either. FC_JAIL=0
# starts the VMM as root in those two namespaces, without the jailer.
set -euo pipefail
. "$(dirname "$0")/rootfs/fc-lib.sh"

cmd="${1:?create|destroy|health|prep|sweep|status|vmmetrics|vmfault}"
SN="$FC_DIR/snap"
NET="${FC_NET:-10.254}"                        # 10.254.<b>.0/30 per cell; create refuses if the host routes it
POOL_DISKS="$FC_DISKS/pool"
MIN_AVAIL_MIB="${FC_MIN_AVAIL_MIB:-5120}"      # a 4 GiB guest plus headroom; create refuses below this
GUEST_DYNCONFIG_DIR=/opt/laug/cells/compose/dynamicconfig   # cell.yml's ./dynamicconfig in the guest (build-image.sh)
[[ "$RUN_DIR" == /* && "$RUN_DIR" != / ]] || { echo "RUN_DIR must be an absolute path other than /" >&2; exit 2; }

log() { echo "laug · fc $cmd${id:+ $id}${b:+ b$b}: $*" >&2; }

block_vars() { # every per-cell name, from b
  dir="$RUN_DIR/b$b"; lock="$RUN_DIR/b$b.lock"; disk="$POOL_DISKS/b$b.ext4"; link="laugfc$b"
  h="$NET.$b.1"; c="$NET.$b.2"
  jail="$JAIL_BASE/firecracker/laug-b$b"; root="$jail/root"; cg="$JAIL_CG/laug-b$b"   # FC_JAIL=1's, made by create
}
capi() { # capi <METHOD> <path> <json>: this cell's Firecracker API, bounded (fc-lib.sh's api has no timeout)
  curl -sf -m 10 -X "$1" --unix-socket "$dir/fc.sock" -H 'content-type: application/json' --data "$3" "http://localhost/$2" \
    || { log "API $1 $2 failed; see $dir/vmm.log, kept as $RUN_DIR/b$b.last.vmm.log at destroy"; return 1; }
}
cssh() { timeout 5 ssh "${SSHO[@]}" "root@$c" "$@"; }
# The drive's cache mode the snapshot was taken with (snapshot.sh writes it), or nothing. A load keeps it (v1.17
# swagger, PartialDrive); create cannot change it, only say it.
snap_cache_type() { jq -r '.cache_type // empty' "$SN/snapshot.json" 2>/dev/null || true; }
# guest_clock read|set: one ssh session, so the host's time crosses an open channel rather than a new connection
# (connection setup is most of a 0.3 s ssh round trip). Prints "<before> <after> <round trip>" in seconds: the guest
# clock minus the host's from the first sample, then (after setting it, for set) from the best of 5 samples.
guest_clock() {
  local rd wr l a z g off rtt before="" best_off="" best_rtt="" i
  coproc GC { exec 3>&- 9>&-; exec timeout 10 ssh "${SSHO[@]}" "root@$c" \
    'echo ready; while read -r op t; do [ "$op" = set ] && date -u -s "@$t" >/dev/null; date +%s.%N; done' 2>/dev/null; }
  rd=${GC[0]}; wr=${GC[1]}
  { read -r -t 5 l <&"$rd" && [[ $l == ready ]]; } || { kill "$GC_PID" 2>/dev/null; return 1; }
  for i in 0 1 2 3 4 5; do
    if [[ $i == 1 && $1 == set ]]; then echo "set $(now)" >&"$wr"; read -r -t 5 l <&"$rd" || return 1; fi
    a=$(now); echo read >&"$wr"; read -r -t 5 g <&"$rd" || return 1; z=$(now)
    rtt=$(echo "$z - $a" | bc); off=$(echo "$g - ($a + $z) / 2" | bc -l)
    if [[ $i == 0 ]]; then before=$off; continue; fi
    if [[ -z $best_rtt ]] || (( $(echo "$rtt < $best_rtt" | bc) )); then best_off=$off; best_rtt=$rtt; fi
  done
  eval "exec $wr>&-"; wait "$GC_PID" 2>/dev/null || true
  printf '%+.4f %+.4f %.4f\n' "$before" "$best_off" "$best_rtt"
}
alive() { [[ -r /proc/$1/status ]] && ! grep -q '^State:[[:space:]]*Z' "/proc/$1/status" 2>/dev/null; }
# The VMM's command line: by this block's socket as root, by its id under the jailer (--id and the id as two arguments,
# or --id=<id>; fc-lib.sh's jail_argv). The jailer and the script that starts it match the id too for create's first
# second; both are this block's.
vmm_re() { echo "--api-sock $dir/fc.sock( |\$)|--id[ =]laug-b$b( |\$)"; }
vmm_pids() { # this block's VMM: fc.pid if its command line is this block's, the cell cgroup's processes, any by matcher
  local p="" a
  [[ -s $dir/fc.pid ]] && p=$(<"$dir/fc.pid")
  { if [[ $p =~ ^[0-9]+$ ]]; then
      a=$(tr '\0' ' ' 2>/dev/null <"/proc/$p/cmdline") || a=""
      { grep -qF "$dir/fc.sock" <<<"$a" || grep -qE -- "--id[ =]laug-b$b( |\$)" <<<"$a"; } && echo "$p"
    fi
    cat "$cg/cgroup.procs" 2>/dev/null || true
    pgrep -f -- "$(vmm_re)" || true; } | sort -u
}

# cg_rm <cgroup dir>: rmdir it, retried for about 5 s while it is busy, since a VMM's last thread leaves the cgroup
# only after its memory is torn down (inference; Linux cgroup-v2.rst, a cgroup with no process may be
# removed). 0 when it is gone.
cg_rm() {
  for _ in $(seq 1 100); do
    [[ -d $1 ]] || return 0
    rmdir "$1" 2>/dev/null && return 0
    sleep 0.05
  done
  [[ ! -d $1 ]]
}

# Kill this block's VMM, then remove its link, disk and directory, and under the jailer its jail and cgroup. Never
# kills by name. Returns 1 and names what is left if anything is. Callers hold the block lock.
block_clean() {
  local pids x a g cid="" left=""
  cid=$(cat "$dir/id" 2>/dev/null) || cid=""   # the cell on this block, if create got as far as writing it
  pids=$(vmm_pids)
  if [[ -n $pids ]]; then
    # The guest clock against the host's at the end of a lease: drift evidence for long leases.
    if g=$(guest_clock read 2>/dev/null); then
      g=${g#* }; log "guest clock ${g% *} s against the host, $(( $(date +%s) - $(stat -c %Y "$dir/id" 2>/dev/null || date +%s) )) s after create"
    fi
    # The last metrics line before the kill: SIGKILL writes none.
    curl -sf -m 2 -X PUT --unix-socket "$dir/fc.sock" -H 'content-type: application/json' \
      --data '{"action_type":"FlushMetrics"}' http://localhost/actions >/dev/null 2>&1 || true
    # shellcheck disable=SC2086  # one pid per word
    kill -9 $pids 2>/dev/null || true
    for _ in $(seq 1 50); do
      a=""; for x in $pids; do alive "$x" && a+=" $x"; done
      [[ -z $a ]] && break; sleep 0.1
    done
    # A jailer read above between its cgroup and its clone leaves a VMM it cloned after: read once more.
    a=$(vmm_pids)
    if [[ -n $a ]]; then
      # shellcheck disable=SC2086  # one pid per word
      kill -9 $a 2>/dev/null || true; pids+=$'\n'"$a"
      for _ in $(seq 1 50); do x=""; for g in $a; do alive "$g" && x+=" $g"; done; [[ -z $x ]] && break; sleep 0.1; done
    fi
  fi
  for _ in $(seq 1 50); do ip link show "$link" >/dev/null 2>&1 || break; sleep 0.1; done
  ip link del "$link" 2>/dev/null || true
  [[ -f $dir/fc.log ]] && mv -f "$dir/fc.log" "$RUN_DIR/b$b.last.log"
  # The VMM's own log lines, its errors among them, kept by block as its console is (inference, v1.17 logger).
  # Under the jailer vmm.log is a link into the jail, and a move would move the link. The jail's names are the VMM
  # uid's (the jailer makes its root the uid's, 0700; v1.17.0 src/jailer/src/env.rs), so it could swap either
  # file for a link to any host file, which root would copy out, or a FIFO, which would hang destroy. So the jail's
  # file is read only when it is a regular file, not a link, opened with nofollow and a time bound.
  if [[ -L $dir/vmm.log ]]; then
    if [[ -f $root/vmm.log && ! -L $root/vmm.log ]]; then
      timeout 10 dd if="$root/vmm.log" iflag=nofollow status=none of="$RUN_DIR/b$b.last.vmm.log" 2>/dev/null || true
    fi
  elif [[ -f $dir/vmm.log ]]; then mv -f "$dir/vmm.log" "$RUN_DIR/b$b.last.vmm.log"; fi
  # The VMM's metrics, appended per cell id: a reset keeps the id and the block (internal/pool, Reset), so a move
  # would let the next VMM's file replace this one's. Under the jailer, the same rule as the log.
  if [[ $cid =~ ^[0-9a-f]{8}$ && -L $dir/metrics.json ]]; then
    if [[ -f $root/metrics.json && ! -L $root/metrics.json ]]; then
      timeout 10 dd if="$root/metrics.json" iflag=nofollow status=none 2>/dev/null >>"$RUN_DIR/$cid.metrics.json" || true
    fi
  elif [[ $cid =~ ^[0-9a-f]{8}$ && -f $dir/metrics.json ]]; then cat -- "$dir/metrics.json" >>"$RUN_DIR/$cid.metrics.json"; fi
  rm -f "$disk"
  # The jail after the copies above: the jail's names for the disk and the snapshot files are empty files on the host,
  # bound only in the dead VMM's mount namespace, so rm takes the placeholders, never vm.mem or a cell disk.
  [[ $b =~ ^[0-9]+$ && $jail == "$JAIL_BASE/firecracker/laug-b$b" && -e $jail ]] && rm -rf --one-file-system -- "$jail"
  cg_rm "$cg" || true
  [[ $b =~ ^[0-9]+$ && $dir == "$RUN_DIR/b$b" ]] && rm -rf --one-file-system -- "$dir"
  for x in $pids; do alive "$x" && left+=" pid $x"; done
  [[ -z $(pgrep -f -- "$(vmm_re)" || true) ]] || left+=" vmm"
  ip link show "$link" >/dev/null 2>&1 && left+=" link $link"
  [[ -e $disk ]] && left+=" disk $disk"
  [[ -e $jail ]] && left+=" jail $jail"
  [[ -d $cg ]] && left+=" cgroup $cg"
  [[ -e $dir ]] && left+=" dir $dir"
  [[ -z $left ]] || { log "left after clean:$left"; return 1; }
}

deny_rules() { # deny_rules add|del: drop anything forwarded to or from a cell link, in FORWARD and DOCKER-USER
  local ch d
  for ch in FORWARD DOCKER-USER; do
    iptables -w 5 -n -L "$ch" >/dev/null 2>&1 || continue
    for d in -i -o; do
      if [[ $1 == add ]]; then
        iptables -w 5 -C "$ch" "$d" 'laugfc+' -j DROP 2>/dev/null || iptables -w 5 -I "$ch" 1 "$d" 'laugfc+' -j DROP
      else
        while iptables -w 5 -D "$ch" "$d" 'laugfc+' -j DROP 2>/dev/null; do :; done
      fi
    done
  done
}

# Any block's VMM, as root by its socket or under the jailer by its id: sweep's pkill and status's count.
VMM_ANY_RE="--api-sock $RUN_DIR/b[0-9]+/fc.sock|--id[ =]laug-b[0-9]+( |\$)"

status() { # jails and cell cgroups are named when FC_JAIL=1 or any is left
  local vm links disks dirs rules jails cgs
  vm=$(pgrep -fc -- "$VMM_ANY_RE" || true)
  links=$(ip -o link show | grep -cE '^[0-9]+: laugfc[0-9]+[@:]' || true)
  disks=$(find "$POOL_DISKS" -maxdepth 1 -name 'b*.ext4' 2>/dev/null | wc -l || true)
  dirs=$(find "$RUN_DIR" -mindepth 1 -maxdepth 1 -type d -name 'b[0-9]*' 2>/dev/null | wc -l || true)
  rules=$({ iptables -w 5 -S FORWARD; iptables -w 5 -S DOCKER-USER 2>/dev/null || true; } | grep -c 'laugfc+' || true)
  jails=$(find "$JAIL_BASE/firecracker" -mindepth 1 -maxdepth 1 -name 'laug-b[0-9]*' 2>/dev/null | wc -l || true)
  cgs=$(find "$JAIL_CG" -mindepth 1 -maxdepth 1 -type d -name 'laug-b[0-9]*' 2>/dev/null | wc -l || true)
  if [[ $FC_JAIL == 1 || $jails != 0 || $cgs != 0 ]]; then
    echo "laug · fc pool left: $vm firecracker, $links laugfc links, $disks cell disks, $dirs block dirs, $jails jails, $cgs cell cgroups, $rules deny rules"
  else
    echo "laug · fc pool left: $vm firecracker, $links laugfc links, $disks cell disks, $dirs block dirs, $rules deny rules"
  fi
}

no_serve() { # the checkout's pool serve, by its pid file, and another checkout's Firecracker pool serve (fc-lib.sh)
  need_no_serve
  need_no_other_fc
}

sweep() { # every block, then anything the blocks missed
  # It leaves $RUN_DIR/<id>.metrics.json, as it leaves b<b>.last.log and b<b>.last.vmm.log: it removes block
  # directories only through block_clean, which keeps each cell's metrics. $RUN_DIR is tmpfs, so they go at a reboot.
  local d
  no_serve
  # pool serve's SIGTERM does not reach the verbs it started; let them finish, or a create past this point would start a
  # VMM after the sweep (create holds its block lock for at most 60 s plus its ssh waits).
  for _ in $(seq 1 90); do pgrep -f -- '[b]ackend-firecracker[.]sh (create|destroy|health) ' >/dev/null || break; sleep 1; done
  for d in "$RUN_DIR"/b*/; do
    [[ -d $d ]] || continue
    b=${d%/}; b=${b##*/b}; [[ $b =~ ^[0-9]+$ ]] || continue
    block_vars
    exec 9>"$lock"; flock -w 60 9 || { log "still locked after 60 s"; continue; }
    block_clean || true
    exec 9>&-
  done
  pkill -9 -f -- "$VMM_ANY_RE" 2>/dev/null || true
  for _ in $(seq 1 50); do pgrep -f -- "$VMM_ANY_RE" >/dev/null || break; sleep 0.1; done   # gone, before their jails and cgroups go
  # Jails and cell cgroups a block whose directory is gone left behind; block_clean removes each block's own. Numeric
  # ids only, and no other name under $JAIL_BASE; a jail whose cgroup still lists a process after 5 s is left, named.
  local j n; b=""
  for j in "$JAIL_BASE"/firecracker/laug-b*; do
    [[ -e $j ]] || continue
    n=${j##*/laug-b}; [[ $n =~ ^[0-9]+$ ]] || continue
    if ! cg_rm "$JAIL_CG/laug-b$n"; then
      log "jail $j left: its cgroup lists $(paste -sd ' ' "$JAIL_CG/laug-b$n/cgroup.procs" 2>/dev/null)"; continue
    fi
    rm -rf --one-file-system -- "$j"
  done
  for j in "$JAIL_CG"/laug-b*/; do
    [[ -d $j ]] || continue
    n=${j%/}; n=${n##*/laug-b}; [[ $n =~ ^[0-9]+$ ]] || continue
    cg_rm "${j%/}" || log "cgroup ${j%/} left: it lists $(paste -sd ' ' "${j%/}/cgroup.procs" 2>/dev/null)"
  done
  [[ -d $JAIL_CG ]] && { rmdir "$JAIL_CG" 2>/dev/null || true; }
  for d in $(ip -o link show | grep -oE '^[0-9]+: laugfc[0-9]+' | cut -d' ' -f2); do ip link del "$d" 2>/dev/null || true; done
  rm -f "$POOL_DISKS"/b*.ext4 "$RUN_DIR/guest.key" "$RUN_DIR/guest.known_hosts"
  deny_rules del
  status
}

prep() { # once, before the pool serves: the shared state every create checks for
  no_serve
  need_root_kvm; need_laug
  local t f
  for t in nsenter unshare flock setsid pgrep; do command -v "$t" >/dev/null || { echo "$t is not on root's PATH" >&2; exit 2; }; done
  for f in vm.snap vm.mem; do [[ -f $SN/$f ]] || { echo "no $SN/$f; run make fc-snapshot first" >&2; exit 2; }; done
  vm_lock    # not while build-image, snapshot or restore runs
  disks_up   # once here: its check-then-mount is not safe to race
  [[ -f $FC_DISKS/golden.ext4 ]] || { echo "no $FC_DISKS/golden.ext4; run make fc-snapshot" >&2; exit 2; }
  [[ -f $KNOWN_HOSTS ]] || { echo "no $KNOWN_HOSTS, the guest's host key; run make fc-image, then make fc-snapshot" >&2; exit 2; }
  cmp -s "$KNOWN_HOSTS" "$SN/guest.known_hosts" || { echo "$SN/guest.known_hosts is missing or is not $KNOWN_HOSTS: the image was built after the snapshot; run make fc-snapshot again" >&2; exit 2; }
  # The VMM resolves the disk path vm.snap recorded; restore.sh moves the symlink, so put it back on golden.ext4.
  grep -a -F -q "$SN/disk.ext4" "$SN/vm.snap" || { echo "$SN/vm.snap does not record $SN/disk.ext4; run make fc-snapshot again" >&2; exit 2; }
  [[ -n $(snap_cache_type) ]] || { echo "$SN/snapshot.json is missing or has no cache_type; run make fc-snapshot again" >&2; exit 2; }
  case $(snap_cache_type) in Unsafe|Writeback) ;; *) echo "$SN/snapshot.json records cache_type $(snap_cache_type), not Unsafe or Writeback; run make fc-snapshot again" >&2; exit 2 ;; esac
  [[ $(snap_cache_type) == "$FC_CACHE_TYPE" ]] || echo "laug · the snapshot was taken with cache_type $(snap_cache_type) and a load keeps it; FC_CACHE_TYPE=$FC_CACHE_TYPE here changes nothing, FC_CACHE_TYPE=$FC_CACHE_TYPE make fc-snapshot would" >&2
  ln -sfn "$FC_DISKS/golden.ext4" "$SN/disk.ext4"
  mkdir -p "$RUN_DIR" "$POOL_DISKS"
  sweep >/dev/null
  if [[ $FC_JAIL == 1 ]]; then
    # After the sweep, which stops a jailed VMM a pool before left, so its uid's process is not taken for another's.
    install -d -m 0700 "$JAIL_BASE"
    jail_check
    # Every jailed VMM reads vm.snap and vm.mem as gid JAIL_GID and writes neither. snapshot.sh sets this when it runs
    # with FC_JAIL=1; any other snapshot gets it once here, after jail_check. chgrp and chmod change no content.
    for f in vm.snap vm.mem; do
      [[ $(stat -c %U:%g:%a "$SN/$f") == "root:$JAIL_GID:640" ]] && continue
      chown "root:$JAIL_GID" "$SN/$f"; chmod 0640 "$SN/$f"
      echo "laug · $SN/$f set to root:$JAIL_GID 0640 for the jails" >&2
    done
  fi
  deny_rules add
  local ku="${FC_KEY_USER:-${SUDO_USER:-}}"
  chmod 0755 "$RUN_DIR"   # a sudo umask can make it 0700, and the key's owner must reach the key
  install -m 0644 "$KNOWN_HOSTS" "$RUN_DIR/guest.known_hosts"   # for every user: it holds the guest's public key only
  if [[ -n $ku ]]; then
    install -m 0600 -o "$ku" -g "$(id -gn "$ku")" "$KEY" "$RUN_DIR/guest.key"
    runuser -u "$ku" -- test -r "$RUN_DIR/guest.key" || { echo "$ku cannot read $RUN_DIR/guest.key" >&2; exit 2; }
    echo "laug · guest key for $ku at $RUN_DIR/guest.key: faults reach the cells' containers over ssh"
  else
    echo "laug · no FC_KEY_USER or sudo user: faults that use a cell's containers will refuse; the toxiproxy faults run" >&2
  fi
  status
}

preflight() { # every check exits 2 with one line, before anything is made
  local t avail
  need_root_kvm; need_laug
  for t in nsenter unshare flock setsid pgrep; do command -v "$t" >/dev/null || { log "$t is not on root's PATH"; exit 2; }; done
  for t in vm.snap vm.mem; do [[ -f $SN/$t ]] || { log "no $SN/$t; run make fc-snapshot"; exit 2; }; done
  [[ -n $(snap_cache_type) ]] || { log "$SN/snapshot.json is missing or has no cache_type; run make fc-snapshot again"; exit 2; }
  if [[ $FC_JAIL == 1 ]]; then
    local r
    r=$(jail_argv "$b" 2>&1 >/dev/null) || { log "$r"; exit 2; }
    [[ -x $FC_DIR/jailer && -d $JAIL_BASE ]] || { log "no $FC_DIR/jailer or $JAIL_BASE; run ${LAUG:-bin/laug} pool serve -backend firecracker, which preps"; exit 2; }
  fi
  [[ $(readlink -e "$SN/disk.ext4") == "$FC_DISKS/golden.ext4" ]] || { log "$SN/disk.ext4 is not golden.ext4; run ${LAUG:-bin/laug} pool serve -backend firecracker, which preps"; exit 2; }
  mountpoint -q "$FC_DISKS" || { log "$FC_DISKS is not mounted; run ${LAUG:-bin/laug} pool serve -backend firecracker, which preps"; exit 2; }
  iptables -w 5 -C FORWARD -o 'laugfc+' -j DROP 2>/dev/null || { log "no deny rule in FORWARD; run ${LAUG:-bin/laug} pool serve -backend firecracker, which preps"; exit 2; }
  avail=$(awk '/^MemAvailable:/{print int($2/1024)}' /proc/meminfo)
  (( avail >= MIN_AVAIL_MIB )) || { log "MemAvailable ${avail} MiB, below FC_MIN_AVAIL_MIB ${MIN_AVAIL_MIB}"; exit 2; }
  (( $(df --output=avail -k "$FC_DISKS" | tail -1) >= 2097152 )) || { log "less than 2 GiB free on $FC_DISKS"; exit 2; }
}

# jail_launch: create's start of the VMM under FC_JAIL=1, in place of the root one. Sets p, the VMM's pid, once
# accepted, and t_jail; exits 1, one line, when no VMM is accepted, and create's EXIT trap cleans the block. Called
# bare, so set -e holds inside it. The jail holds the cell's disk, vm.snap and vm.mem at their host paths: a short
# script, run by unshare in the cell's new network and mount namespaces, binds each onto an empty file in the jail,
# then execs the jailer. The jailer's own mount namespace starts as a copy of that one and binds the jail root onto
# itself recursively before its pivot_root (v1.17.0 src/jailer/src/chroot.rs, MS_BIND | MS_REC; jailer.md says
# MS_BIND), so the three mounts are inside the jail; they go when the VMM dies. vm.snap and vm.mem are read-only there
# (util-linux mounts a bind, then remounts it ro; mount(8)). The launcher's pid goes to $dir/launch.pid, for the log
# only: under --new-pid-ns the jailer clones the VMM into a pid namespace of its own, writes its host pid to
# <root>/firecracker.pid, and exits (v1.17.0 src/jailer/src/env.rs).
jail_launch() {
  local uid=$(( JAIL_UID_BASE + b )) hostnet hostmnt q ino t
  local -a ja
  mapfile -t ja < <(jail_argv "$b")   # preflight refused a block it would not print
  chown "$uid:$JAIL_GID" "$disk"; chmod 0600 "$disk"
  # The snapshot's directory at its host path, root's and 0755, so the uid only searches it; the jailer makes the root,
  # dev and run the uid's, 0700 (env.rs). The VMM's log and metrics files are made first, 0600 and the uid's.
  ( umask 022; mkdir -p -- "$root$SN" )
  : > "$root$SN/disk.ext4"; : > "$root$SN/vm.snap"; : > "$root$SN/vm.mem"
  install -m 0600 -o "$uid" -g "$JAIL_GID" /dev/null "$root/vmm.log"
  install -m 0600 -o "$uid" -g "$JAIL_GID" /dev/null "$root/metrics.json"
  # $dir stays the interface: capi, vmfault, vmmetrics and block_clean read these. connect(2) and open(2) follow a
  # link (seen on the dev host and on the lab host).
  ln -s -- "$root/run/firecracker.socket" "$dir/fc.sock"
  ln -s -- "$root/vmm.log" "$dir/vmm.log"; ln -s -- "$root/metrics.json" "$dir/metrics.json"
  t=$(now)
  # shellcheck disable=SC2016  # the script's $1 to $4 are its own arguments
  setsid unshare --net --mount --propagation private -- /bin/sh -ec 'echo $$ > "$4/launch.pid"
    mount --bind "$1" "$3$2/disk.ext4"; mount --bind -o ro "$2/vm.snap" "$3$2/vm.snap"; mount --bind -o ro "$2/vm.mem" "$3$2/vm.mem"
    shift 4; exec "$@"' laug-jail "$disk" "$SN" "$root" "$dir" "${ja[@]}" </dev/null >"$dir/fc.log" 2>&1 3>&- 9>&- &
  # The pid the jailer writes is accepted when its exe is the jail's copy of the binary, by inode (a path read from
  # another mount namespace may not print as a host path; inference), its namespaces are not the host's, and the cell
  # cgroup lists it. The jailer puts itself in the cgroup before the clone; after it, the parent writes the pid file
  # while the child execs, in no set order (cgroup.rs attach_pid; env.rs exec_into_new_pid_ns), so the loop
  # polls past both. 5 s: the jailer copies the binary, makes the device nodes and the cgroup first (estimate; the time
  # is logged).
  hostnet=$(readlink /proc/self/ns/net); hostmnt=$(readlink /proc/self/ns/mnt); p=""
  for _ in $(seq 1 100); do   # never nsenter before this holds, or the tap, sysctl and DNAT would land on the host
    if [[ -s $root/firecracker.pid ]]; then
      q=$(cat "$root/firecracker.pid" 2>/dev/null) || q=""
      ino=$(stat -c %d:%i "$root/firecracker" 2>/dev/null) || ino=""
      if [[ $q =~ ^[1-9][0-9]*$ && -n $ino && $(stat -L -c %d:%i "/proc/$q/exe" 2>/dev/null) == "$ino" \
         && $(readlink "/proc/$q/ns/net" 2>/dev/null) != "$hostnet" && $(readlink "/proc/$q/ns/mnt" 2>/dev/null) != "$hostmnt" ]] \
         && grep -qx -- "$q" "$cg/cgroup.procs" 2>/dev/null; then p=$q; break; fi
    fi
    sleep 0.05
  done
  [[ -n $p ]] || { log "the jailer started no VMM in $jail and $cg within 5 s; see $RUN_DIR/b$b.last.log and b$b.last.vmm.log"; exit 1; }
  # Within 1 s the cgroup lists the VMM alone: the jailer has exited.
  for _ in $(seq 1 20); do [[ $(cat "$cg/cgroup.procs" 2>/dev/null) == "$p" ]] && break; sleep 0.05; done
  [[ $(cat "$cg/cgroup.procs" 2>/dev/null) == "$p" ]] \
    || { log "$cg lists $(paste -sd ' ' "$cg/cgroup.procs" 2>/dev/null), not the VMM $p alone"; exit 1; }
  printf '%s\n' "$p" > "$dir/fc.pid"
  t_jail=$(since "$t")
  log "jailer: VMM pid $p, launcher pid $(cat "$dir/launch.pid" 2>/dev/null || echo unknown), uid $uid, accepted in $t_jail s; /proc/$p/exe reads $(readlink "/proc/$p/exe" 2>/dev/null || echo nothing) from the host"
}

create() {
  exec 3>&1 1>&2   # the pool reads stdout to EOF: only the JSON goes there, on fd 3, which nothing started here keeps
  local t0 t p hostnet hostmnt want gold n g f a off rtt t_disk t_load t_ssh cache fcv frozen r thaw="not frozen"
  local t_jail="" tapown=""
  t0=$(now)
  preflight
  # From the snapshot, once: the lease records cache and fcv, so every record names what the cell ran on, and a
  # snapshot taken frozen (snapshot.sh) holds the cell's containers paused until the thaw below. fcv is last, as it
  # may be empty.
  read -r frozen cache fcv <<<"$(jq -r '"\(.frozen // false) \(.cache_type // "") \(.firecracker // "")"' "$SN/snapshot.json")"
  mkdir -p "$RUN_DIR" "$POOL_DISKS"
  exec 9>"$lock"; flock -w 60 9 || { log "still locked after 60 s"; exit 1; }
  block_clean || exit 1
  [[ -z $(ip -o route show match "$c" | grep -v '^default' || true) ]] || { log "$c is already routed on this host; set FC_NET"; exit 2; }
  # Any exit before the JSON is a failure, a signal included (bash runs the EXIT trap with $? 0 after a signal).
  trap 'block_clean || true' EXIT; trap 'exit 130' INT; trap 'exit 143' TERM; trap 'exit 129' HUP

  mkdir "$dir"; printf '%s\n' "$id" > "$dir/id"
  # Fail fast without reflink: a plain copy of the 5 GB disk took 41 to 58 s (fc-lib.sh, above disks_up).
  t=$(now); cp --reflink=always "$FC_DISKS/golden.ext4" "$disk" || { log "no reflink copy on $POOL_DISKS (XFS reflink=1 needed)"; exit 1; }
  t_disk=$(since "$t")

  if [[ $FC_JAIL == 1 ]]; then
    jail_launch
    # The VMM opens tap0 at the load as its uid, with no CAP_NET_ADMIN, which tun allows only the tap's owner and group
    # (Linux drivers/net/tun.c, tun_not_capable).
    tapown=" user $(( JAIL_UID_BASE + b )) group $JAIL_GID"
  else
    # One pid from bash to firecracker: setsid does not fork outside a process group leader, and unshare without
    # --fork execs. The VMM keeps neither fd 3 (the pool would wait for it) nor fd 9 (the lock). Its own log and
    # metrics go beside the socket, made empty first: Firecracker writes over an old file, not truncating it. A metrics
    # line about every 60 s or on FlushMetrics (v1.17 metrics.md); fc.log keeps the console.
    : > "$dir/vmm.log"; : > "$dir/metrics.json"
    setsid bash -c 'echo $$ > "$0"; exec unshare --net --mount --propagation private -- "$1" --api-sock "$2" --enable-pci --log-path "$3" --metrics-path "$4"' \
      "$dir/fc.pid" "$FC_DIR/firecracker" "$dir/fc.sock" "$dir/vmm.log" "$dir/metrics.json" </dev/null >"$dir/fc.log" 2>&1 3>&- 9>&- &
    hostnet=$(readlink /proc/self/ns/net); hostmnt=$(readlink /proc/self/ns/mnt); p=""
    for _ in $(seq 1 40); do   # never nsenter before this holds, or the tap, sysctl and DNAT would land on the host
      if [[ -s $dir/fc.pid ]]; then
        p=$(<"$dir/fc.pid")
        [[ $(readlink "/proc/$p/exe" 2>/dev/null) == "$FC_DIR/firecracker" \
           && $(readlink "/proc/$p/ns/net" 2>/dev/null) != "$hostnet" && $(readlink "/proc/$p/ns/mnt" 2>/dev/null) != "$hostmnt" ]] && break
      fi
      p=""; sleep 0.05
    done
    [[ -n $p ]] || { log "the VMM did not start in its own namespaces; see $RUN_DIR/b$b.last.log and b$b.last.vmm.log"; exit 1; }
  fi
  for _ in $(seq 1 50); do [[ -S $dir/fc.sock ]] && break; sleep 0.1; done
  [[ -S $dir/fc.sock ]] || { log "no API socket; see $RUN_DIR/b$b.last.log and b$b.last.vmm.log"; exit 1; }

  ip link add "$link" type veth peer name veth0 netns "$p"
  ip addr add "$h/30" dev "$link"; ip link set "$link" up
  # icmp_ratelimit 0: the kernel limits the ICMP errors it sends, port unreachable among them (Linux
  # Documentation/networking/ip-sysctl.rst). The value is per network namespace (seen on the dev host: 0 in a new
  # one left the host's 1000), so this lifts it for this cell's guest alone. A likely cause of batch E's 5.011 s
  # lookup (cells/results/2026-10-24-batchE.txt; inference, not measured yet).
  nsenter -t "$p" -n sh -ec "
    ip link set lo up
    ip tuntap add dev tap0 mode tap$tapown; ip addr add 172.16.0.1/30 dev tap0; ip link set tap0 up
    ip addr add $c/30 dev veth0; ip link set veth0 up
    sysctl -qw net.ipv4.ip_forward=1
    sysctl -qw net.ipv4.icmp_ratelimit=0
    iptables -w 5 -t nat -A PREROUTING -i veth0 -s $h -d $c -j DNAT --to-destination 172.16.0.2
    iptables -w 5 -P FORWARD DROP
    iptables -w 5 -A FORWARD -i veth0 -o tap0 -s $h -d 172.16.0.2 -j ACCEPT
    iptables -w 5 -A FORWARD -i tap0 -o veth0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    iptables -w 5 -t nat -A PREROUTING -i tap0 -s 172.16.0.2 -d 172.16.0.1 -p tcp -m multiport --dports 9091:9093 -j DNAT --to-destination $h
    iptables -w 5 -t nat -A POSTROUTING -o veth0 -s 172.16.0.2 -d $h -j MASQUERADE
    iptables -w 5 -A FORWARD -i tap0 -o veth0 -s 172.16.0.2 -d $h -p tcp -m multiport --dports 9091:9093 -j ACCEPT"

  # The recorded path is $SN/disk.ext4 -> golden.ext4. Inside this VMM's mount namespace only, golden.ext4 is the
  # cell's disk. Checked by inode before the load, and again by open file after it, before the guest runs.
  want=$(stat -c %d:%i "$disk"); gold=$(stat -c %d:%i "$FC_DISKS/golden.ext4")
  if [[ $FC_JAIL == 1 ]]; then
    # Under the jailer the recorded path is the cell's disk itself, bound in the jail; the host's paths are gone from
    # the VMM's mount namespace, so the check reads through /proc/<pid>/root, the VMM's root (proc(5)).
    [[ $(stat -L -c %d:%i "/proc/$p/root$SN/disk.ext4" 2>/dev/null) == "$want" ]] \
      || { log "$SN/disk.ext4 in the jail is not the cell's disk; the bind mount did not reach the jail"; exit 1; }
    [[ $(stat -L -c %d:%i "/proc/$p/root$SN/vm.mem" 2>/dev/null) == "$(stat -c %d:%i "$SN/vm.mem")" ]] \
      || { log "$SN/vm.mem in the jail is not the snapshot's; the bind mount did not reach the jail"; exit 1; }
  else
    nsenter -t "$p" -m mount --bind "$disk" "$FC_DISKS/golden.ext4"
    [[ $(nsenter -t "$p" -m stat -L -c %d:%i "$SN/disk.ext4") == "$want" ]] || { log "the bind mount did not take"; exit 1; }
  fi
  t=$(now)
  capi PUT snapshot/load "{\"snapshot_path\":\"$SN/vm.snap\",\"mem_backend\":{\"backend_type\":\"File\",\"backend_path\":\"$SN/vm.mem\"},\"resume_vm\":false}"
  n=0; g=0
  for f in /proc/"$p"/fd/*; do
    case $(stat -L -c %d:%i "$f" 2>/dev/null) in "$want") n=$((n + 1)) ;; "$gold") g=$((g + 1)) ;; esac
  done
  (( n >= 1 && g == 0 )) || { log "the VMM holds $n handles on the cell disk and $g on golden.ext4; not resuming"; exit 1; }
  capi PATCH vm '{"state":"Resumed"}'
  t_load=$(since "$t")

  a=$(( $(date +%s) + 20 ))
  until cssh true 2>/dev/null; do (( $(date +%s) < a )) || { log "no ssh to $c after 20 s"; exit 1; }; sleep 0.2; done
  t_ssh=$(since "$t")
  # The guest clock resumes at the pause (34 to 206 s behind over 12 restores). Fault and verdict windows are host
  # times queried against the guest's Prometheus, so set it before health and refuse an offset over 0.1 s.
  read -r g off rtt <<<"$(guest_clock set)" && [[ -n $rtt ]] || { log "could not set the guest clock"; exit 1; }
  awk -v o="$off" 'BEGIN { exit !(o < 0.1 && o > -0.1) }' || { log "guest clock off by $off s after the set"; exit 1; }
  # Thawed only now, so nothing in the cell's containers ran on the pause's clock; countdowns armed before the freeze
  # run on.
  if [[ $frozen == true ]]; then
    r=$(guest_thaw cssh) || { log "could not thaw the cell's containers after the clock set"; exit 1; }
    thaw="thawed ${r% *} containers in ${r#* } s"
  fi
  # The cell id beside the guest's dynamic config, which every server role mounts as a directory: faults/run.sh
  # edits that file only after each role shows this id, as it does for a compose cell's own copy.
  # Also keep the file the cell starts with, outside the mounted directory, for faults/run.sh's restore.
  cssh "printf '%s\n' $id > $GUEST_DYNCONFIG_DIR/cell-id && cp -p $GUEST_DYNCONFIG_DIR/laug.yaml /opt/laug/cells/laug.yaml.orig"

  trap - EXIT INT TERM HUP
  log "up in $(since "$t0") s: reflink ${t_disk} s, load and resume ${t_load} s, ssh ${t_ssh} s; guest clock $g s before the set, $off s after (round trip $rtt s); cache_type $cache; $thaw${t_jail:+; jailed as uid $(( JAIL_UID_BASE + b )) in $jail, the VMM accepted in $t_jail s}"
  printf '{"id":"%s","address":"%s:7233","ui":"http://%s:8080","grafana":"http://%s:3000","prometheus":"http://%s:9090","toxiproxy":"http://%s:8474","project":"laug-%s","ssh":"root@%s","dynconfig":"%s/laug.yaml","backend":"firecracker","firecracker_version":"%s","cache_type":"%s"}\n' \
    "$id" "$c" "$c" "$c" "$c" "$c" "$id" "$c" "$GUEST_DYNCONFIG_DIR" "$fcv" "$cache" >&3
}

health() { # the pool drops health's output, so the last run's goes to health.last
  [[ -d $dir ]] || exit 1
  exec 2>"$dir/health.last" 1>&2
  [[ $(cat "$dir/id" 2>/dev/null) == "$id" ]] || { echo "block $b holds another cell"; exit 1; }
  [[ -n $(vmm_pids) ]] || { echo "no VMM for block $b"; exit 1; }
  # The same four steps as backend-compose.sh, against the clone address; shards-ready.sh runs in the guest.
  timeout 5 temporal operator cluster health --address "$c:7233" >/dev/null
  timeout 5 temporal operator namespace describe -n default --address "$c:7233" >/dev/null
  timeout 10 ssh "${SSHO[@]}" "root@$c" /opt/laug/cells/shards-ready.sh laug
  timeout 60 "$LAUG_ROOT/bin/laug" health check -address "$c:7233" -visibility-wait 10s >/dev/null
}

# vmmetrics: a metrics line now, then "<file> <n> lines" on stdout. Exit 1, one line, when the block holds no such cell,
# no VMM or no metrics file, or the flush fails; exit 2 when not root, since the socket is root's.
vmmetrics() {
  local n
  [[ -d $dir ]] || { log "block $b holds no cell $id"; exit 1; }
  [[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 2; }
  [[ $(cat "$dir/id" 2>/dev/null) == "$id" ]] || { log "block $b holds no cell $id"; exit 1; }
  [[ -n $(vmm_pids) ]] || { log "no VMM for block $b"; exit 1; }
  [[ -f $dir/metrics.json ]] || { log "no $dir/metrics.json: this VMM started without --metrics-path"; exit 1; }
  capi PUT actions '{"action_type":"FlushMetrics"}' >/dev/null || exit 1
  n=$(wc -l 2>/dev/null <"$dir/metrics.json") || { log "$dir/metrics.json went away; the cell was destroyed"; exit 1; }
  printf '%s %s lines\n' "$dir/metrics.json" "$n"
}

# vmfault <id> drive <json>: PATCH a rate limiter on drives/rootfs, the cell's one drive (fc-lib.sh's vm_boot), and
# nothing else. Prints nothing on success. Exit 1, one line with Firecracker's answer when it gave one, when the block
# holds no such cell or no VMM, or the PATCH fails; exit 2 on a kind other than drive or a body other than
# {drive_id: rootfs, rate_limiter}. The body is PartialDrive: drive_id, and a rate_limiter whose buckets a PATCH keeps
# when the body leaves them out (v1.17 src/vmm/src/vmm_config/mod.rs, get_bucket_update; the swagger gives
# PartialDrive's fields and 'Post-boot only'). PartialDrive's path_on_host is refused: under the jailer, the default,
# it would point the guest's disk at any file the VMM's uid reaches in its jail; with FC_JAIL=0 the VMM runs as root,
# so at any host file.
vmfault() {
  local out
  [[ -d $dir ]] || { log "block $b holds no cell $id"; exit 1; }
  [[ $(cat "$dir/id" 2>/dev/null) == "$id" ]] || { log "block $b holds no cell $id"; exit 1; }
  [[ -n $(vmm_pids) ]] || { log "no VMM for block $b"; exit 1; }
  out=$(curl -s --fail-with-body -m 10 -X PATCH --unix-socket "$dir/fc.sock" -H 'content-type: application/json' \
    --data "$fault_body" http://localhost/drives/rootfs) \
    || { log "API PATCH drives/rootfs failed${out:+: $out}; see $dir/vmm.log"; exit 1; }
}

destroy() {
  mkdir -p "$RUN_DIR"
  exec 9>"$lock"; flock -w 60 9 || { log "still locked after 60 s"; exit 1; }   # waits out a killed create's children
  if [[ -f $dir/id && $(<"$dir/id") != "$id" ]]; then log "block $b holds cell $(<"$dir/id"), not $id; left alone"; exit 0; fi
  block_clean
}

case "$cmd" in
  create|destroy|health|vmmetrics|vmfault)
    id="${2:?cell id}"
    if [[ $cmd == vmfault ]]; then
      [[ ${3:-} == drive && $# -eq 4 ]] \
        || { echo "usage: vmfault <id> drive <json> (drive is the one kind; pause, resume and net are not built)" >&2; exit 2; }
      # A rate limiter on rootfs and nothing else: path_on_host, or another drive, is refused here.
      jq -n -e --argjson v "$4" '$v | type == "object" and (keys - ["drive_id", "rate_limiter"]) == [] and .drive_id == "rootfs"
        and (.rate_limiter | type == "object" and length > 0 and (keys - ["bandwidth", "ops"]) == []
          and all(.[]; type == "object" and has("size") and has("refill_time") and (keys - ["size", "one_time_burst", "refill_time"]) == []
            and all(.[]; type == "number" and . >= 0 and . == floor)))' >/dev/null 2>&1 \
        || { echo "vmfault: the body must be {\"drive_id\": \"rootfs\", \"rate_limiter\": {\"bandwidth\"|\"ops\": {size, refill_time[, one_time_burst]}}} and nothing else, got '$4'" >&2; exit 2; }
      fault_body=$4
    fi
    # vmmetrics checks root after the block's directory, so a block with none refuses for any user.
    [[ $EUID -eq 0 || $cmd == vmmetrics ]] || { echo "run as root (sudo bin/laug pool serve -backend firecracker)" >&2; exit 2; }
    [[ $id =~ ^[0-9a-f]{8}$ ]] || { echo "cell id must be 8 lowercase hex characters, got '$id'" >&2; exit 2; }
    b="${LAUG_BLOCK:?LAUG_BLOCK is not set; the pool sets it}"
    [[ $b =~ ^[0-9]+$ ]] && (( b <= 250 )) || { echo "LAUG_BLOCK must be 0 to 250, got '$b'" >&2; exit 2; }
    block_vars
    "$cmd"
    ;;
  prep|sweep)
    [[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 2; }
    "$cmd"
    ;;
  status) status ;;
  *) echo "unknown command: $cmd" >&2; exit 2 ;;
esac
