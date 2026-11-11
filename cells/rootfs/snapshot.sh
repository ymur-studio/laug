#!/usr/bin/env bash
# snapshot.sh: boot the cell image, bring the cell to warm inside the microVM, pause it, and take a full snapshot.
# Linux with /dev/kvm, as root (sudo make fc-snapshot), after build-image.sh. Output in $FC_DIR/snap: vm.snap (device
# state), vm.mem (all guest memory), snapshot.json (one line: the drive's cache_type, vcpus, mem_mib, the Firecracker
# and kernel versions, when it was taken, and frozen), guest.known_hosts (the host key of the image it booted, which
# prep and restore.sh compare with $FC_DIR/guest.known_hosts). The disk is not in a snapshot: the VM booted from
# $FC_DIR/snap/disk.ext4, a symlink to $FC_DISKS/golden.ext4, and restore.sh points that symlink at a fresh copy of
# golden.ext4 before every load.
set -euo pipefail
. "$(dirname "$0")/fc-lib.sh"
need_root_kvm; vm_lock; need_no_pool; need_laug
VCPUS="${VCPUS:-4}"; MEM_MIB="${MEM_MIB:-4096}"; FREEZE="${FREEZE:-1}"; SN="$FC_DIR/snap"
[[ $FREEZE == 0 || $FREEZE == 1 ]] || { echo "FREEZE=$FREEZE: 1 freezes the cell's containers before the pause, 0 does not" >&2; exit 2; }
[[ -f "$FC_DIR/image/cell.ext4" ]] || { echo "no image; run make fc-image first" >&2; exit 2; }
[[ -f "$KNOWN_HOSTS" ]] || { echo "no $KNOWN_HOSTS, the guest's host key; run make fc-image" >&2; exit 2; }
fc_version="$("$FC_DIR/firecracker" --version | awk 'NR==1{print $2}')"   # before the boot: a failure here costs nothing
vm_kill
disks_up; mkdir -p "$SN"; rm -f "$SN"/vm.snap "$SN"/vm.mem "$SN"/snapshot.json "$SN"/disk.ext4 "$SN"/guest.known_hosts "$FC_DISKS/golden.ext4"
t=$(now); mode=$(disk_copy "$FC_DIR/image/cell.ext4" "$FC_DISKS/golden.ext4"); echo "disk: $mode of the image, $(since "$t") s"
ln -sfn "$FC_DISKS/golden.ext4" "$SN/disk.ext4"

net_up; t0=$(now); vm_boot "$SN/disk.ext4" "$VCPUS" "$MEM_MIB"
wait_ssh || { echo "no ssh to the guest; see $FC_DIR/vmm.log and $FC_DIR/fc.stdout" >&2; exit 1; }; echo "boot to ssh $(since "$t0") s"; guest_net
gssh 'for i in $(seq 1 60); do docker info >/dev/null 2>&1 && break; sleep 0.5; done; docker info >/dev/null' \
  || { echo "dockerd did not come up in the guest" >&2; vm_kill; exit 1; }
echo "dockerd ready $(since "$t0") s"
# Inside the guest the ports bind 0.0.0.0: the guest is reachable only from the host, over tap0.
tc=$(now)
gssh 'cd /opt/laug/cells/compose && BIND_ADDR=0.0.0.0 SDK_HOST=172.16.0.1 ENABLE_ES=false docker compose -p laug --project-directory /opt/laug/cells/compose -f cell.yml up -d 2>&1 | grep -iE "error|fail" || true'
echo "compose up returned $(since "$tc") s; from compose up:"
warm_timeline "$tc" 300 || { gssh 'docker ps -a --format "{{.Names}} {{.Status}}"' || true; vm_kill; exit 1; }
gssh 'free -m | awk "/Mem/{print \"guest memory used \" \$3 \" MiB of \" \$2}"'
sleep "${SETTLE_S:-20}"   # let the cell idle so the snapshot holds a quiet cell, not one mid-startup
# The guest clock resumes at the pause's time: a pool cell ran on it through create's ssh wait, 0.97 to 2.11 s in
# batch E, before the set. Frozen here, nothing in a restored cell's containers runs until the set and the thaw; a
# countdown armed before the freeze still runs on after it. FREEZE=0 takes a snapshot without the freeze, for a
# comparison run.
frozen=false
if [[ $FREEZE == 1 ]]; then
  r=$(guest_freeze gssh) || { vm_kill; exit 1; }
  echo "froze ${r% *} containers of project laug in ${r#* } s"; frozen=true
fi
t=$(now); api PATCH vm '{"state":"Paused"}'; echo "paused $(since "$t") s"
t=$(now); api PUT snapshot/create "{\"snapshot_type\":\"Full\",\"snapshot_path\":\"$SN/vm.snap\",\"mem_file_path\":\"$SN/vm.mem\"}"
echo "snapshot/create $(since "$t") s"
# A jailed pool VMM (FC_JAIL=1, the default) reads both as gid JAIL_GID, its one group, and writes neither (fc-lib.sh).
# Firecracker v1.17.0 made both 0644 under umask 022 (seen on the lab host), as it makes its log and metrics files
# 0666 less the umask (dev host). Set only for FC_JAIL=1 and only when no group entry holds JAIL_GID; a jailed pool's
# prep sets them once after its own checks either way. The snapshot boots unjailed: fc-snapshot never runs the jailer.
if [[ $FC_JAIL == 1 && -z $(getent group "$JAIL_GID" 2>/dev/null || true) ]]; then
  chown "root:$JAIL_GID" "$SN/vm.snap" "$SN/vm.mem"; chmod 0640 "$SN/vm.snap" "$SN/vm.mem"
fi
# What the snapshot was taken with, for prep and the records: the cache mode is fixed here, since a load keeps the
# drive as taken (v1.17 swagger, PartialDrive).
jq -nc --arg c "$FC_CACHE_TYPE" --arg e "$FC_IO_ENGINE" --argjson v "$VCPUS" --argjson m "$MEM_MIB" --arg f "$fc_version" --arg k "$FC_KERNEL" \
  --arg t "$(date -u +%FT%TZ)" --argjson z "$frozen" \
  '{cache_type: $c, io_engine: $e, vcpus: $v, mem_mib: $m, firecracker: $f, kernel: $k, taken: $t, frozen: $z}' > "$SN/snapshot.json"
install -m 0644 "$KNOWN_HOSTS" "$SN/guest.known_hosts"   # the host key of the image this snapshot booted
vm_kill; sleep 0.3
echo "laug · snapshot in $SN: vm.mem $(du -h "$SN/vm.mem" | cut -f1), vm.snap $(du -h "$SN/vm.snap" | cut -f1); golden disk $(du -h "$FC_DISKS/golden.ext4" | cut -f1); cache_type $FC_CACHE_TYPE; io_engine $FC_IO_ENGINE; $([[ $frozen == true ]] && echo frozen || echo "not frozen")"
