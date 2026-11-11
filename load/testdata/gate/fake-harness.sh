#!/usr/bin/env bash
# Fake make harness-test for a fake gate (GATE_FAKE=1): waits FAKE_GATE_HARNESS_S (0) s, prints the tally line the real
# one ends with, and exits FAKE_GATE_HARNESS_EXIT (0). The real one would run the gate it is part of: gate/run.sh runs
# it in the background of every gate run.
# It also fails, as the real one does in its three Ctrl-C checks, when it started with SIGINT ignored: the state a
# background job of a non-interactive shell starts in, which gate/run.sh undoes (sigdefault).
# FAKE_GATE_HARNESS_REAL=1 runs the real make harness-test in its place, as laug gate control does for a control: the
# items and the cells stay fakes, and R1 is the check a gate on cells runs. It gets the PATH the gate was given, the
# fakes' directory gate/lib.sh puts first taken off, and no MAKEFLAGS or MAKELEVEL, as gate/lib.sh runs the real one.
# The harness unsets every FAKE_*, so the gates it runs itself get this fake again.
if [[ "${FAKE_GATE_HARNESS_REAL:-}" == 1 ]]; then
  root="$(cd "$(dirname "$0")/../../.." && pwd -P)"; path=""
  IFS=: read -ra dirs <<<"$PATH"
  for d in "${dirs[@]}"; do [[ "$d" == "$root/load/testdata/gate/fakebin" ]] || path+="${path:+:}$d"; done
  PATH="$path" exec env -u MAKEFLAGS -u MAKELEVEL make -C "$root" --no-print-directory harness-test
fi
rc="${FAKE_GATE_HARNESS_EXIT:-0}"
# awk reads its own status: bash ignores SIGQUIT for itself, and starts its children as it was started (seen)
ign="$(awk '$1 == "SigIgn:" { print $2 }' /proc/self/status 2>/dev/null)"
if [[ -n "$ign" ]] && (( 0x$ign & 2 )); then echo "SIGINT ignored: the Ctrl-C checks would fail"; (( rc != 0 )) || rc=1
else echo "SIGINT at its default"; fi
sleep "${FAKE_GATE_HARNESS_S:-0}"
printf 'fake make harness-test\n\n%d passed, %d failed\n' 1 "$(( rc != 0 ))"
exit "$rc"
