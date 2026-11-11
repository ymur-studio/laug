# fc-lib.sh: shared by build-image.sh, snapshot.sh and restore.sh. Source it; run as root on Linux with /dev/kvm.
# One microVM at a time on tap0: host 172.16.0.1, guest 172.16.0.2 (the CI rootfs derives its IP from the MAC).
# The pool (cells/backend-firecracker.sh) runs many, each in its own namespaces, and never calls fc_start, vm_stop,
# vm_kill, net_up, api or gssh: those assume the one VM. They stop only the VMM on $API (vm_pids), and net_up and
# fc_start refuse while another firecracker holds tap0 (tap_free).

LAUG_ROOT="${LAUG_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
# Canonical, so path comparisons against findmnt output (which prints canonical paths) hold for any spelling.
FC_DIR="$(realpath -m -- "${FC_DIR:-/opt/fc}")"   # binaries, kernel, keys, image and snapshot live here
FC_VERSION="${FC_VERSION:-v1.17.0}"               # the Firecracker release every result used
FC_CI="${FC_CI:-firecracker-ci/20261008-40f3a4819490-0}"   # CI artifact build: kernel and rootfs
FC_KERNEL="${FC_KERNEL:-vmlinux-6.18.51}"
# The sha256 of each download build-image.sh makes, checked before use (sum_check). The tarball's equals the release's
# .sha256.txt. The kernel's and the rootfs's come from a download of each URL and are trust on first use: the CI build
# publishes none.
FC_SHA256_TGZ="${FC_SHA256_TGZ:-06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558}"      # firecracker-v1.17.0-x86_64.tgz
FC_SHA256_KERNEL="${FC_SHA256_KERNEL:-946e164b5a50675341a5d0a6ac2b516711806267ed0c73e17c6289c97aad81ea}"   # vmlinux-6.18.51
FC_SHA256_ROOTFS="${FC_SHA256_ROOTFS:-1d50b8fff4410e9fbe29de5b65f4a4b74efb901ab14071288cd8fb4a753f0fdd}"   # ubuntu-24.04.squashfs
# The drive's cache mode: under Unsafe a guest fsync reaches no disk, under Writeback it is an fsync on the host (v1.17
# block-caching.md). Fixed when fc-snapshot boots: a load keeps the drive as taken (v1.17 swagger, PartialDrive).
FC_CACHE_TYPE="${FC_CACHE_TYPE:-Writeback}"
[[ $FC_CACHE_TYPE == Unsafe || $FC_CACHE_TYPE == Writeback ]] \
  || { echo "FC_CACHE_TYPE=$FC_CACHE_TYPE: the drive's cache mode is Unsafe or Writeback" >&2; exit 2; }
# The drive's I/O engine: Sync, the default, or Async, io_uring, a developer preview in v1.17 (block-io-engine.md).
# Fixed when fc-snapshot boots, as the cache mode is.
FC_IO_ENGINE="${FC_IO_ENGINE:-Sync}"
[[ $FC_IO_ENGINE == Sync || $FC_IO_ENGINE == Async ]] \
  || { echo "FC_IO_ENGINE=$FC_IO_ENGINE: the drive's I/O engine is Sync or Async" >&2; exit 2; }
# FC_JAIL=1, the default, starts each pool VMM through Firecracker's jailer (cells/backend-firecracker.sh); 0 starts it
# as root in its own namespaces. The single-VM scripts never jail. The jailer ran on the lab host in boot 10: 51 jailed
# creates, 0 failed (cells/results/2026-10-29-boot10.txt, J and E).
FC_JAIL="${FC_JAIL:-1}"
[[ $FC_JAIL == 0 || $FC_JAIL == 1 ]] \
  || { echo "FC_JAIL=$FC_JAIL: 1 starts each pool VMM through the jailer, 0 as root without it" >&2; exit 2; }
# The pool's jails (cells/backend-firecracker.sh with FC_JAIL=1): Firecracker's jailer builds each under
# $JAIL_BASE/firecracker/laug-b<b>/root (v1.17.0 docs/jailer.md, <chroot_base>/<exec_file_name>/<id>/root). Not
# under /run, which mounts nodev on the dev host, so a device node made there cannot be opened (inference): under
# $FC_DIR, whose mount prep checks allows dev and exec. Each VMM runs as uid JAIL_UID_BASE plus its block, 20000 to
# 20250, and every one in the one group JAIL_GID, which may read vm.snap and vm.mem (root:JAIL_GID 0640) and nothing
# else of laug's.
JAIL_BASE="$FC_DIR/jail"; JAIL_UID_BASE=20000; JAIL_GID=20000
JAIL_CG=/sys/fs/cgroup/laug   # the jailer's --parent-cgroup laug on the cgroup v2 mount; each cell's is laug-b<b> in it
S3="https://s3.amazonaws.com/spec.ccfc.min"
KERNEL_PATH="$FC_DIR/${FC_CI##*/}-$FC_KERNEL"     # keyed by CI build, so a pin change fetches a new kernel
API="$FC_DIR/fc.sock"; TAP=tap0; GUEST=172.16.0.2; KEY="$FC_DIR/guest.id_rsa"
RUN_DIR="${RUN_DIR:-/run/laug}"                  # the pool's per-cell state, on tmpfs
# The guest's host key, which build-image.sh records at the image's first boot, for any address (*). The build
# deletes the host keys in the CI rootfs and makes its own in the chroot (ssh-keygen -A; run on the lab host in boot 10,
# cells/results/2026-10-29-boot10.txt, I), so the pin names the image's build, not which cell. Every clone of the
# image keeps them: each of boot 9's 87 creates passed ssh against this file (cells/results/2026-10-29-boot9.txt, P).
KNOWN_HOSTS="$FC_DIR/guest.known_hosts"
SSHO=(-o StrictHostKeyChecking=yes -o UserKnownHostsFile="$KNOWN_HOSTS" -o ConnectTimeout=2 -o LogLevel=ERROR -i "$KEY")

gssh() { ssh "${SSHO[@]}" root@$GUEST "$@"; }
sum_check() { # sum_check <file> <sha256>: exit 2, one line, when the file's sha256 is not the one pinned
  local s
  s=$(sha256sum -- "$1" 2>/dev/null | cut -d' ' -f1) || s=""
  [[ -n $s && $s == "$2" ]] || { echo "$1: sha256 ${s:-unreadable}, pinned $2; refusing it" >&2; exit 2; }
}
now() { date +%s.%N; }
since() { printf '%.2f' "$(echo "$(now) - $1" | bc)"; }
need_root_kvm() {
  [[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 2; }
  [[ -e /dev/kvm ]] || { echo "no /dev/kvm on this host" >&2; exit 2; }
  local t; for t in temporal jq yq bc curl docker ip iptables ssh ssh-keygen unsquashfs mkfs.ext4 findmnt mountpoint tar realpath; do
    command -v "$t" >/dev/null || { echo "$t is not on root's PATH (sudo uses secure_path)" >&2; exit 2; }
  done
}
# The checkout's pool serve, by the pid file laug pool serve keeps while it runs (cmd/laug/pool.go): "<pid> firecracker"
# when the file names a process that runs laug pool serve -backend firecracker, "<pid> other" when it runs another
# backend, and status 1 when it names none. A gone pid, a zombie's empty command line and a pid another program took
# read as none.
POOL_PID="$LAUG_ROOT/cells/run/pool.pid"
pool_serve() {
  local p a serve_re='^([^ ]*/)?laug pool serve( |$)' fc_re=' --?backend[ =]firecracker( |$)'
  p=$(cat "$POOL_PID" 2>/dev/null) && [[ $p =~ ^[1-9][0-9]*$ ]] || return 1
  a=$(tr '\0' ' ' 2>/dev/null <"/proc/$p/cmdline") && [[ $a =~ $serve_re ]] || return 1
  if [[ $a =~ $fc_re ]]; then echo "$p firecracker"; else echo "$p other"; fi
}
need_no_serve() { # exit 2, one line, while the checkout's pool serve runs, on any backend
  local s
  s=$(pool_serve) || return 0
  if [[ ${s#* } == firecracker ]]; then
    echo "the Firecracker pool runs, laug pool serve pid ${s% *} (cells/run/pool.pid); sudo bin/laug pool down -backend firecracker stops it" >&2
  else
    echo "laug pool serve runs, pid ${s% *} (cells/run/pool.pid), on another backend; ${LAUG:-bin/laug} pool down -remove-cells stops it" >&2
  fi
  exit 2
}
# Another checkout's Firecracker pool: laug pool serve -backend firecracker, by its command line, other than this shell
# and its parent, the pool serve whose prep this is. Its pid file is in its own checkout, and RUN_DIR is the host's, so
# a sweep here would take its cells. Exit 2, one line, while one runs.
need_no_other_fc() {
  local p re='^([^ ]*/)?laug pool serve( .*)? --?backend[ =]firecracker( |$)'
  for p in $(pgrep -f -- "$re"); do
    [[ $p == "$$" || $p == "$PPID" ]] && continue
    echo "another Firecracker pool runs, laug pool serve pid $p, another checkout's; sudo bin/laug pool down -backend firecracker in that checkout stops it" >&2
    exit 2
  done
}
# The single-VM scripts rewrite golden.ext4 and disk.ext4, which pool cells run on, and stop only their own VMM.
# A pool counts as up from prep to sweep: laug pool serve on this backend, by its pid file, a pool VMM, a block dir,
# a jail or a cell cgroup, or prep's deny rule.
need_no_pool() {
  local s=""
  s=$(pool_serve) || s=""
  if [[ ${s#* } == firecracker ]] || pgrep -f -- "--api-sock $RUN_DIR/b[0-9]+/fc.sock|--id[ =]laug-b[0-9]+( |\$)" >/dev/null \
     || compgen -G "$RUN_DIR/b[0-9]*/" >/dev/null || compgen -G "$JAIL_BASE/firecracker/laug-b[0-9]*" >/dev/null \
     || compgen -G "$JAIL_CG/laug-b[0-9]*/" >/dev/null || iptables -w 5 -C FORWARD -o 'laugfc+' -j DROP 2>/dev/null; then
    echo "a Firecracker pool is up (laug pool serve, a cell, or prep's deny rule); sudo bin/laug pool down -backend firecracker -remove-cells first" >&2; exit 2
  fi
}
# jail_argv <block>: the jailer's command line for a pool VMM on that block, one argument a line, with no side effects
# (the harness checks it byte for byte). The jailer copies $FC_DIR/firecracker into the jail, drops to the uid and gid,
# and execs it chrooted, in a pid namespace of its own, with --id and the id as two arguments and then the arguments
# after -- (v1.17.0 src/jailer/src/env.rs, exec_command), so every matcher reads --id[ =]laug-b<N>( |$). No
# --netns: create starts the jailer inside the network namespace unshare made, which dies with the VMM. The cgroup,
# /sys/fs/cgroup/laug/laug-b<b> (v2), sets cpu.weight and memory.max to the kernel's defaults: it counts the VMM's CPU
# and memory and caps neither (Linux cgroup-v2.rst). No --resource-limit: the jailer's defaults stand (jailer.md
# says no-file 2048, prod-host-setup.md 4096), and an fsize low enough to bound the log would fail the
# guest's disk writes (inference from setrlimit(2)). The paths after -- are inside the jail. Exit 2, one line, when the
# block is not 0 to 250. The socket needs no length check: the VMM binds /run/firecracker.socket in its chroot, and the
# host connects through $RUN_DIR/b<b>/fc.sock, a link, whose target is not held to sun_path's 108 bytes (inference
# from unix(7); seen on the dev host: curl and bash's -S over a link to a 293-byte socket path).
jail_argv() {
  local b=${1:-}
  [[ $b =~ ^[0-9]+$ ]] && (( b <= 250 )) || { echo "jail_argv: the block is 0 to 250, got '$b'" >&2; return 2; }
  printf '%s\n' "$FC_DIR/jailer" --id "laug-b$b" --exec-file "$FC_DIR/firecracker" \
    --uid "$(( JAIL_UID_BASE + b ))" --gid "$JAIL_GID" --chroot-base-dir "$JAIL_BASE" --new-pid-ns \
    --cgroup-version 2 --parent-cgroup laug --cgroup cpu.weight=100 --cgroup memory.max=max \
    -- --api-sock /run/firecracker.socket --enable-pci --log-path /vmm.log --metrics-path /metrics.json
}
# jail_check: prep's checks for FC_JAIL=1, each exit 2 with one line, in this order: the jailer at $FC_DIR/jailer, at
# $FC_VERSION (the second field of its first line, "Jailer v<version>"; v1.17.0 src/jailer/src/main.rs); the
# mount under $JAIL_BASE, which prep makes first, without nodev or noexec, since each jail holds the binary's copy and
# the device nodes the jailer makes (jailer.md); no passwd entry and no process for the uids 20000 to 20250;
# no group entry for gid 20000, whose members would read vm.mem, the guest's memory; cgroup v2 at /sys/fs/cgroup with
# cpu and memory among its controllers; $FC_DIR/firecracker, $FC_DIR/jailer, $FC_DIR and every parent root's and not
# writable by group or others (prod-host-setup.md, Jailer Configuration: the paths given to the jailer and
# their parents not writable by unprivileged users).
jail_check() {
  local v o x d m last=$(( JAIL_UID_BASE + 250 ))
  [[ -x $FC_DIR/jailer ]] \
    || { echo "no $FC_DIR/jailer; run make fc-image, which fetches it beside firecracker, or set FC_JAIL=0 to run the pool without it" >&2; exit 2; }
  v=$("$FC_DIR/jailer" --version 2>/dev/null | awk 'NR==1{print $2}') || v=""
  [[ $v == "$FC_VERSION" ]] \
    || { echo "$FC_DIR/jailer is ${v:-of no version}, not $FC_VERSION; run make fc-image, or set FC_JAIL=0 to run the pool without it" >&2; exit 2; }
  o=$(findmnt -n -o OPTIONS --target "$JAIL_BASE" 2>/dev/null) || o=""
  [[ -n $o ]] || { echo "findmnt names no mount for $JAIL_BASE" >&2; exit 2; }
  [[ ,$o, != *,nodev,* && ,$o, != *,noexec,* ]] \
    || { echo "$JAIL_BASE is on a mount with $o: a jail needs device nodes and exec; set FC_DIR on another filesystem" >&2; exit 2; }
  # shellcheck disable=SC2046  # one key per uid
  x=$({ getent passwd $(seq "$JAIL_UID_BASE" "$last") 2>/dev/null || true; } | head -1 | cut -d: -f1,3)
  [[ -z $x ]] || { echo "uid ${x#*:} has a passwd entry, ${x%%:*}; the jails take uids $JAIL_UID_BASE to $last" >&2; exit 2; }
  x=$({ pgrep -u "$(seq -s, "$JAIL_UID_BASE" "$last")" 2>/dev/null || true; } | head -1)
  [[ -z $x ]] || { echo "pid $x runs as a uid in $JAIL_UID_BASE to $last, which the jails take" >&2; exit 2; }
  x=$({ getent group "$JAIL_GID" 2>/dev/null || true; } | cut -d: -f1)
  [[ -z $x ]] || { echo "gid $JAIL_GID has a group entry, $x; every jailed VMM reads vm.mem as that gid" >&2; exit 2; }
  [[ $(findmnt -n -o FSTYPE --mountpoint /sys/fs/cgroup 2>/dev/null) == cgroup2 ]] \
    || { echo "/sys/fs/cgroup is not cgroup v2; the jails' cgroups need it" >&2; exit 2; }
  for x in cpu memory; do
    grep -qw "$x" /sys/fs/cgroup/cgroup.controllers 2>/dev/null \
      || { echo "cgroup v2 has no $x controller in /sys/fs/cgroup/cgroup.controllers" >&2; exit 2; }
  done
  for d in "$FC_DIR/firecracker" "$FC_DIR/jailer"; do
    m=$(stat -c '%u %a' "$d" 2>/dev/null) || m=""
    [[ $m =~ ^0\ ([0-7]+)$ ]] && (( (8#${BASH_REMATCH[1]} & 8#022) == 0 )) \
      || { echo "$d is ${m:+owner and mode $m, }not root's, writable by group or others, or unreadable; the jailer's paths must not be" >&2; exit 2; }
  done
  d=$FC_DIR
  while :; do
    m=$(stat -c '%u %a' "$d" 2>/dev/null) || m=""
    [[ $m =~ ^0\ ([0-7]+)$ ]] && (( (8#${BASH_REMATCH[1]} & 8#022) == 0 )) \
      || { echo "$d is ${m:+owner and mode $m, }not root's, writable by group or others, or unreadable; the jailer's paths and their parents must not be" >&2; exit 2; }
    [[ $d == / ]] && break
    d=$(dirname "$d")
  done
}
# One Firecracker script at a time: build-image, snapshot, restore and the pool's prep take this lock for their run,
# so prep cannot point disk.ext4 back at golden.ext4 while a restore is about to load it.
vm_lock() {
  exec 8>"$FC_DIR/vm.lock"
  flock -n 8 || { echo "another Firecracker script (build, snapshot, restore or pool prep) is running" >&2; exit 2; }
}
need_laug() { # built as the user, not as root: make build. Its health check is the HealthCheck round trip.
  [[ -x "$LAUG_ROOT/bin/laug" ]] || { echo "bin/laug is not built: run make build first, not as root" >&2; exit 2; }
}

# vm_pids: the one VM's VMM, a process named firecracker whose arguments hold the pair --api-sock $API, as fc_start
# starts it. A pool cell's (--api-sock $RUN_DIR/b<N>/fc.sock, stopped by cells/backend-firecracker.sh by pid and
# socket), another FC_DIR's, and any Firecracker not laug's are not it, and nothing here stops them. Seen
# with fakes, programs named firecracker: vm_pids listed the one on $API alone, and vm_kill and vm_stop stopped it; one
# on another socket, one that named $API after another flag, and one in a network namespace of its own ran on.
# Seen on the lab host: make fc-restore RESTORES=1 restored the snapshot, vm_pids listed the one VMM on $API during
# the restore and none after vm_kill (cells/results/2026-10-17-arm-pilot.txt, the small items).
vm_pids() {
  local p i; local -a a
  for p in $(pgrep -x firecracker || true); do
    mapfile -d '' -t a 2>/dev/null < "/proc/$p/cmdline" || continue
    for (( i = 1; i + 1 < ${#a[@]}; i++ )); do
      [[ "${a[i]}" != --api-sock || "${a[i+1]}" != "$API" ]] || { echo "$p"; break; }
    done
  done
}
vm_kill() { local p; for p in $(vm_pids); do kill "$p" 2>/dev/null || true; done; }   # TERM to the one VM's VMM, if any
# tap_free: 1, with a message, while a firecracker that is not the one VM's holds tap0 in this network namespace: a fd
# of its on /dev/net/tun reads "iff:<TAB>tap0" in /proc/<pid>/fdinfo. net_up's ip link del succeeds under a holder and
# leaves its fd on no device, so net_up asks first. Seen on the dev host, in a user and network namespace of
# its own: a holder's fdinfo read iff: tap0, and after ip link del, iff: and nothing; with the fakes above, a fake on
# another socket holding tap0 made tap_free, net_up and fc_start refuse, tap0 left and nothing started, and one on the
# tap0 of a network namespace of its own did not (drivers/net/tun.c, tun_chr_show_fdinfo).
tap_free() {
  local p f me own
  me="$(readlink /proc/self/ns/net)"; own=" $(vm_pids | paste -sd ' ') "
  for p in $(pgrep -x firecracker || true); do
    [[ "$own" != *" $p "* && "$(readlink "/proc/$p/ns/net" 2>/dev/null)" == "$me" ]] || continue
    for f in /proc/"$p"/fdinfo/*; do
      grep -qxF "iff:"$'\t'"$TAP" "$f" 2>/dev/null || continue
      echo "firecracker pid $p holds $TAP and is not this FC_DIR's VMM (--api-sock $API): stop it first" >&2; return 1
    done
  done
}
net_up() { # tap0, IP forwarding, and NAT out through the default route for the guest's subnet only.
  # Firecracker's getting-started sets the whole FORWARD policy to ACCEPT; these rules open only tap0 to the
  # uplink and its replies, so nothing else is forwarded to the guest. They stay on the host (idempotent).
  tap_free || return 2
  ip link del $TAP 2>/dev/null || true
  ip tuntap add dev $TAP mode tap; ip addr add 172.16.0.1/30 dev $TAP; ip link set dev $TAP up
  echo 1 > /proc/sys/net/ipv4/ip_forward
  local dev; dev="$(ip -j route list default | jq -r '.[0].dev')"
  iptables -C FORWARD -i $TAP -o "$dev" -j ACCEPT 2>/dev/null || iptables -I FORWARD -i $TAP -o "$dev" -j ACCEPT
  iptables -C FORWARD -i "$dev" -o $TAP -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null \
    || iptables -I FORWARD -i "$dev" -o $TAP -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
  iptables -t nat -C POSTROUTING -s 172.16.0.0/30 -o "$dev" -j MASQUERADE 2>/dev/null \
    || iptables -t nat -A POSTROUTING -s 172.16.0.0/30 -o "$dev" -j MASQUERADE
}
fc_start() { # the one VM's VMM on $API, in place of the last one (vm_kill); refused while another holds tap0
  tap_free || return 1
  vm_kill
  sleep 0.2; rm -f "$API"
  # vmm.log holds the VMM's own log lines, metrics.json a JSON line of counters about every 60 s or on FlushMetrics
  # (v1.17 metrics.md). Firecracker writes over an old file without truncating it (dev host), so each starts empty.
  : > "$FC_DIR/vmm.log"; : > "$FC_DIR/metrics.json"
  setsid "$FC_DIR/firecracker" --api-sock "$API" --enable-pci \
    --log-path "$FC_DIR/vmm.log" --metrics-path "$FC_DIR/metrics.json" > "$FC_DIR/fc.stdout" 2>&1 < /dev/null 8>&- &
  for _ in $(seq 1 50); do [[ -S "$API" ]] && return 0; sleep 0.1; done
  echo "firecracker API socket did not appear" >&2; return 1
}
api() { # api <METHOD> <path> <json>
  curl -sf -X "$1" --unix-socket "$API" -H 'content-type: application/json' --data "$3" "http://localhost/$2" \
    || { echo "API $1 $2 failed; see $FC_DIR/vmm.log and $FC_DIR/fc.stdout" >&2; return 1; }
}
vm_boot() { # vm_boot <disk> <vcpus> <mem_mib>
  fc_start || return 1
  api PUT machine-config "{\"vcpu_count\":$2,\"mem_size_mib\":$3}" &&
  api PUT boot-source "{\"kernel_image_path\":\"$KERNEL_PATH\",\"boot_args\":\"console=ttyS0 reboot=k panic=1\"}" &&
  api PUT drives/rootfs "{\"drive_id\":\"rootfs\",\"path_on_host\":\"$1\",\"is_root_device\":true,\"is_read_only\":false,\"cache_type\":\"$FC_CACHE_TYPE\",\"io_engine\":\"$FC_IO_ENGINE\"}" &&
  api PUT network-interfaces/net1 '{"iface_id":"net1","guest_mac":"06:00:AC:10:00:02","host_dev_name":"tap0"}' &&
  api PUT actions '{"action_type":"InstanceStart"}'
}
wait_ssh() { for _ in $(seq 1 "${1:-300}"); do gssh true 2>/dev/null && return 0; sleep 0.2; done; return 1; }
# The guest's nameserver is the tap's address, where nothing listens on 53, so a lookup of a missing name fails at
# once (ICMP port unreachable), as on a compose host. With 8.8.8.8, which the guest cannot reach, Docker's embedded
# DNS waited out the caller's timeout: Prometheus took 9.7 to 10.1 s to see a killed history, against 3.8 to 5.1 s on
# a compose cell. The guest needs no name from outside: its images are loaded at build time.
guest_net() { gssh 'ip route replace default via 172.16.0.1 dev eth0; printf "nameserver 172.16.0.1\n" > /etc/resolv.conf'; }
# guest_freeze <ssh> and guest_thaw <ssh>: docker pause, or docker unpause, every container of compose project laug in
# the guest, in one call of <ssh> (gssh for the one VM, a pool cell's cssh) with the command as one argument. A pause
# freezes a container's processes with the cgroup freezer, and an unpause thaws them (Docker's docs, docker
# container pause). docker ps lists a paused container without -a (Docker's docs, docker container ls, the
# status=paused example), and filters on two keys must both match (moby v28.5.2 daemon/list.go, includeContainerInList).
# Each prints "<containers> <seconds>", timed on the host, the ssh call included, and returns 1 with a line on stderr
# when docker fails or lists no container; a thaw also when one is still paused.
guest_freeze() {
  local t n
  t=$(now)
  n=$("$1" 'ids=$(docker ps -q --filter label=com.docker.compose.project=laug) || exit 1
[ -n "$ids" ] || { echo "docker ps lists no container of project laug" >&2; exit 1; }
docker pause $ids >/dev/null || exit 1
set -- $ids; echo $#') && [[ $n =~ ^[1-9][0-9]*$ ]] || { echo "the freeze failed: docker pause in the guest" >&2; return 1; }
  echo "$n $(since "$t")"
}
guest_thaw() {
  local t n
  t=$(now)
  n=$("$1" 'f="--filter label=com.docker.compose.project=laug"
ids=$(docker ps -q --filter status=paused $f) || exit 1
[ -n "$ids" ] || { echo "docker ps lists no paused container of project laug" >&2; exit 1; }
docker unpause $ids >/dev/null || exit 1
left=$(docker ps -q --filter status=paused $f) || exit 1
[ -z "$left" ] || { echo "still paused after docker unpause:" $left >&2; exit 1; }
set -- $ids; echo $#') && [[ $n =~ ^[1-9][0-9]*$ ]] || { echo "the thaw failed: docker unpause in the guest" >&2; return 1; }
  echo "$n $(since "$t")"
}
vm_stop() { # a guest reboot exits Firecracker (it has no guest power management); 10 s later, TERM (vm_kill)
  gssh 'sync; reboot' 2>/dev/null || true   # the guest may drop the session before ssh returns
  for _ in $(seq 1 50); do [[ -n "$(vm_pids)" ]] || return 0; sleep 0.2; done; vm_kill
}
# Per-VM disks live on an XFS volume with reflink=1, made on first use as a sparse loop file, so a copy of the
# 5 GB golden disk shares its blocks: 0.01 to 0.22 s, against 41 to 58 s for a plain copy from EBS gp3.
FC_DISKS="$(realpath -m -- "${FC_DISKS:-$FC_DIR/disks}")"
disks_up() {
  mountpoint -q "$FC_DISKS" && return 0
  command -v mkfs.xfs >/dev/null || apt-get install -y -qq xfsprogs >/dev/null
  mkdir -p "$FC_DISKS"
  if [[ ! -f "$FC_DIR/disks.xfs" ]]; then   # made under a temp name, so an interrupted mkfs leaves nothing behind
    rm -f "$FC_DIR/disks.xfs.new"; truncate -s 60G "$FC_DIR/disks.xfs.new"
    mkfs.xfs -q -m reflink=1 "$FC_DIR/disks.xfs.new" && mv "$FC_DIR/disks.xfs.new" "$FC_DIR/disks.xfs"
  fi
  mount -o loop "$FC_DIR/disks.xfs" "$FC_DISKS"
}
disk_copy() { # disk_copy <src> <dst>: a reflink when the filesystem shares blocks (XFS reflink=1), else a sparse copy
  cp --reflink=always "$1" "$2" 2>/dev/null && { echo reflink; return 0; }
  cp --sparse=always "$1" "$2" && echo copy
}

# Warm from the host, the same four steps as backend-compose.sh: cluster health, the namespace, every shard owned
# by the current history process, a HealthCheck that passes. Prints seconds from <t0> for each, and the attempt that
# passed; returns 1 if not warm in time.
# The HealthCheck runs again on failure, every 2 s, each call capped at 60 s, until one passes or <timeout_s> ends. The
# first call can miss a namespace that describe already sees. On the lab host, a call that ended 80 ms after
# describe first passed got "Namespace default is not found", and the snapshot exited 1. Boot 1's A4 saw the same first
# call fail on compose cells, and the next one pass (cells/results/2026-10-23-boot1.txt). The cause is the frontend's
# namespace registry (inferred from the server's source at v1.31.0 and v1.32.1, not seen on a running server).
# Describe reads the database, but each request's interceptors look the name up in the registry, and a lookup before
# the namespace exists keeps a not found for 1 s (nsregistry/registry.go, readthroughCacheTTL). create-namespace.sh
# describes before it creates, and in a snapshot the describe polls below start at compose up, each through nginx to one
# of 2 frontends. The retry's 2 s wait is longer than that 1 s. The registry's refresh,
# system.namespaceCacheRefreshInterval (2 s), does not decide it: a lookup that misses reads through to the database.
warm_timeline() { # warm_timeline <t0> <timeout_s>
  local t0=$1 end=$(( $(date +%s) + $2 )) a=$GUEST:7233 cl="" ns="" sh="" hc="" r="" k=0
  while (( $(date +%s) < end )); do
    [[ -z $cl ]] && timeout 3 temporal operator cluster health --address $a >/dev/null 2>&1 && cl=$(since "$t0")
    [[ -n $cl && -z $ns ]] && timeout 3 temporal operator namespace describe -n default --address $a >/dev/null 2>&1 && ns=$(since "$t0")
    [[ -z $sh ]] && timeout 5 ssh "${SSHO[@]}" root@$GUEST /opt/laug/cells/shards-ready.sh laug 2>/dev/null && sh=$(since "$t0")
    [[ -n $ns && -n $sh ]] && break; sleep 0.2
  done
  [[ -n $ns && -n $sh ]] || { echo "not warm after $2 s: cluster ${cl:-no}, namespace ${ns:-no}, shards ${sh:-no}"; return 1; }
  while :; do
    k=$(( k + 1 ))
    r="$(timeout 60 "$LAUG_ROOT/bin/laug" health check -address $a 2>&1 | tail -1)" || true; hc=$(since "$t0")
    [[ "$r" == health-* ]] && break
    (( $(date +%s) + 2 < end )) || break
    sleep 2
  done
  echo "cluster health ${cl} s, namespace ${ns} s, shards owned ${sh} s, HealthCheck done ${hc} s, attempt ${k}"
  echo "  $r"
  [[ "$r" == health-* ]]
}
