#!/usr/bin/env bash
# restore.sh [N]: restore the snapshot N times (default 1) and time each restore to warm. Linux with /dev/kvm, as root
# (sudo make fc-restore RESTORES=3), after snapshot.sh. KEEP=1 leaves the last microVM running.
#
# Each restore: a fresh disk (a reflink copy of golden.ext4) behind the symlink the snapshot recorded, then
# snapshot/load with resume, then the guest clock. The guest wall clock resumes at the time of the pause (seen: 34 to
# 206 s behind over 12 restores), and Temporal timers and retention run on wall-clock time (batch E's T; retention
# is a timer task, workflow_cleanup_timer.go), so the clock is set from the host before anything is judged warm.
# A snapshot taken frozen (snapshot.json's frozen) holds the cell's containers paused, so each restore thaws them
# after the set, and nothing in them runs before it.
set -euo pipefail
. "$(dirname "$0")/fc-lib.sh"
need_root_kvm; vm_lock; need_no_pool; need_laug
N="${1:-1}"; SN="$FC_DIR/snap"
[[ "$N" =~ ^[1-9][0-9]*$ ]] || { echo "RESTORES must be a positive integer, got '$N'" >&2; exit 2; }
for f in vm.snap vm.mem; do [[ -f "$SN/$f" ]] || { echo "no $SN/$f; run make fc-snapshot first" >&2; exit 2; }; done
[[ -f "$KNOWN_HOSTS" ]] || { echo "no $KNOWN_HOSTS, the guest's host key; run make fc-image" >&2; exit 2; }
cmp -s "$KNOWN_HOSTS" "$SN/guest.known_hosts" || { echo "$SN/guest.known_hosts is missing or is not $KNOWN_HOSTS: the image was built after the snapshot; run make fc-snapshot again" >&2; exit 2; }
# The mode the snapshot was taken with, for the record this output goes into; a load keeps it (v1.17 swagger).
echo "snapshot cache_type $(jq -r '.cache_type // "not recorded"' "$SN/snapshot.json" 2>/dev/null || echo "not recorded")"
frozen=$(jq -r '.frozen // false' "$SN/snapshot.json" 2>/dev/null) || frozen=false
echo "snapshot frozen $frozen"
disks_up; [[ -f "$FC_DISKS/golden.ext4" ]] || { echo "no $FC_DISKS/golden.ext4; run make fc-snapshot" >&2; exit 2; }
trap 'ln -sfn "$FC_DISKS/golden.ext4" "$SN/disk.ext4"' EXIT   # leave the recorded disk path where the pool expects it
for n in $(seq 1 "$N"); do
  echo "=== restore $n of $N"
  vm_kill; sleep 0.3
  rm -f "$FC_DISKS/restore.ext4"
  t=$(now); mode=$(disk_copy "$FC_DISKS/golden.ext4" "$FC_DISKS/restore.ext4"); echo "disk: $mode, $(since "$t") s"
  ln -sfn "$FC_DISKS/restore.ext4" "$SN/disk.ext4"
  net_up; fc_start
  t0=$(now)
  api PUT snapshot/load "{\"snapshot_path\":\"$SN/vm.snap\",\"mem_backend\":{\"backend_type\":\"File\",\"backend_path\":\"$SN/vm.mem\"},\"resume_vm\":true}"
  echo "snapshot/load returned $(since "$t0") s"
  wait_ssh 100 || { echo "no ssh after restore" >&2; exit 1; }; echo "ssh $(since "$t0") s"
  g=$(gssh 'date +%s.%N'); echo "guest clock behind host by $(printf '%.1f' "$(echo "$(now) - $g" | bc)") s"
  gssh "date -u -s @$(now) >/dev/null"; echo "clock set $(since "$t0") s"
  # What is left after the set, from one more round trip (half the RTT is the uncertainty).
  a=$(now); g=$(gssh 'date +%s.%N'); b=$(now)
  # The offset line keeps its shape, which _report reads (restoreAfter), so it is printed after the thaw.
  o="guest clock offset after set $(printf '%+.3f' "$(echo "$g - ($a + $b) / 2" | bc -l)") s (round trip $(printf '%.3f' "$(echo "$b - $a" | bc)") s)"
  if [[ $frozen == true ]]; then
    r=$(guest_thaw gssh) || { echo "$o"; gssh 'docker ps -a --format "{{.Names}} {{.Status}}"' || true; vm_kill; exit 1; }
    echo "thawed ${r% *} containers in ${r#* } s, $(since "$t0") s from snapshot/load"
  fi
  echo "$o; from snapshot/load:"
  warm_timeline "$t0" 120 || { gssh 'docker ps -a --format "{{.Names}} {{.Status}}"' || true; vm_kill; exit 1; }
done
if [[ "${KEEP:-0}" == 1 ]]; then echo "microVM left running at $GUEST"; else vm_kill; echo "stopped"; fi
