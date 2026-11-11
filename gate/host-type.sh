#!/usr/bin/env bash
# The host's type, declared once outside the repository, one line in $XDG_CONFIG_HOME/laug/host-type (~/.config when
# unset), which a gate checks, with nproc and free, against the host its threshold set names (load/steady-state.yaml,
# host; gate/lib.sh, host_check). laug host type -declare <type> runs it with the type, which it writes first; laug host
# type with no -declare runs it with no argument, and it writes nothing. Either way it then prints the declaration, this
# host's size, whether it is a VM, the set's host and what a gate would say. With JSON=1 (laug host type -json) it
# prints those as one JSON object on one line instead, and no line for the declaration: declared (null for none), vcpu,
# mem_gib, virtualized (the hypervisor flag in /proc/cpuinfo: this host is a VM, so microVM cells run nested), set (the
# set's host), passes and reason (null when a gate passes this host). laug never records the declared line, only the
# set's type or "other" (gate.json, host.type). This is the host-type recipe of the Makefile laug replaced, moved
# here: the same lines and exits, and JSON=1 besides.
# Usage: [JSON=1] gate/host-type.sh [<type>]. Exit 0 once it has printed; 2 on a type that is not a type name, a
# declaration it could not write, or a threshold set that does not resolve.
set -o pipefail
cd "$(dirname "$0")/.." || exit 2
f="${XDG_CONFIG_HOME:-${HOME:-}/.config}/laug/host-type"
if (( $# > 0 )); then
  t="$1"; [[ "$t" =~ ^[a-z0-9][a-z0-9.-]{0,31}$ ]] \
    || { echo "laug · host type -declare <type>: a type name, lowercase letters, digits, . and -, as EC2 names one (m8i.8xlarge)" >&2; exit 2; }
  mkdir -p "${f%/*}" && printf '%s\n' "$t" > "$f.tmp" && mv "$f.tmp" "$f" || exit 2
  [[ "${JSON:-}" == 1 ]] || echo "laug · host type: declared $t"
fi
h=$(env -u THRESHOLDS -u THRESHOLDS_RELEASES load/thresholds.sh "" | yq -o=json -I=0 '.resolved.host') || exit 2
d=-; [[ ! -s "$f" ]] || d=$(head -1 "$f"); v=$(nproc); m=$(free -b | awk '$1 == "Mem:" { print $2 }')
z=false; ! grep -qw hypervisor /proc/cpuinfo 2>/dev/null || z=true
r=$(bash -c '. gate/lib.sh; host_check "$@"' host-type "$d" "$v" "$m" "$h")
if [[ "${JSON:-}" == 1 ]]; then
  jq -cn --arg d "$d" --argjson v "$v" --argjson g "$(( m / 1073741824 ))" --argjson z "$z" --argjson h "$h" --arg r "$r" \
    '{declared: (if $d == "-" then null else $d end), vcpu: $v, mem_gib: $g, virtualized: $z, set: $h, passes: ($r == ""),
      reason: (if $r == "" then null else $r end)}' || exit 2
  exit 0
fi
if [[ "$d" == - ]]; then echo "declared          none"; else echo "declared          $d, in (outside the repository)/host-type"; fi
echo "this host         $v vCPU, $(( m / 1073741824 )) GiB (nproc, and free -b's total in whole GiB)"
if [[ "$z" == true ]]; then echo "virtualization    a hypervisor flag in /proc/cpuinfo: this host is a VM, so microVM cells run nested"
else echo "virtualization    no hypervisor flag in /proc/cpuinfo"; fi
echo "the set           $(jq -r '"\(.type), \(.vcpu) vCPU, \(.mem_gib) GiB"' <<<"$h") (load/steady-state.yaml, host)"
if [[ -n "$r" ]]; then echo "laug gate run     refuses this host: $r"; else echo "laug gate run     passes the host check"; fi
