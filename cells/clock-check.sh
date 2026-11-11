#!/usr/bin/env bash
# Host diagnostic: does the wall clock keep pace with the uptime clock? Lease TTLs, the dynamic config poll and
# the fault windows are all intervals, and laug reads some of them off the wall clock and some off a ticker.
# /proc/uptime read the same as CLOCK_BOOTTIME, to 10 ms, on the dev host: the monotonic clock
# plus any time suspended (clock_gettime(2)). A wall clock that time sync steps forward runs ahead of
# it. The lab host read wall over uptime 0.9999 and 0 steps over 0.5 s in 60 s (cells/results/2026-10-23-boot1.txt,
# H1).
# Usage: cells/clock-check.sh [seconds]   (default 60, one sample a second). Reports only: exit 0 once it has
# measured, 2 when it cannot.
set -euo pipefail

secs="${1:-60}"
[[ "$secs" =~ ^[0-9]+$ ]] && (( secs >= 2 )) || { echo "usage: $0 [seconds, 2 or more]" >&2; exit 2; }
[[ -r /proc/uptime ]] || { echo "no /proc/uptime here; clock-check needs Linux" >&2; exit 2; }

# EPOCHREALTIME (bash 5) avoids a fork per sample; its decimal mark follows the locale.
wall() { if [[ -n "${EPOCHREALTIME:-}" ]]; then printf '%s\n' "${EPOCHREALTIME/,/.}"; else date +%s.%N; fi; }
uptime_s() { local u _; read -r u _ < /proc/uptime; printf '%s\n' "$u"; }

echo "laug · clock-check: wall clock against /proc/uptime over ${secs} s, one sample a second"
w0="$(wall)"; u0="$(uptime_s)"; wp="$w0"; up="$u0"; steps=0
for ((i = 1; i <= secs; i++)); do
  sleep 1
  w="$(wall)"; u="$(uptime_s)"
  # /proc/uptime has 10 ms resolution, so a step under 0.5 s is not reported.
  if step="$(awk -v w="$w" -v wp="$wp" -v u="$u" -v up="$up" \
       'BEGIN { d = (w - wp) - (u - up); printf "%+.3f", d; exit !(d > 0.5 || d < -0.5) }')"; then
    steps=$(( steps + 1 ))
    echo "  step  ${step} s at +${i} s"
  fi
  wp="$w"; up="$u"
done

awk -v w0="$w0" -v w="$w" -v u0="$u0" -v u="$u" -v n="$steps" 'BEGIN {
  dw = w - w0; du = u - u0; r = dw / du
  # Two 10 ms readings at each end bound what the ratio can resolve.
  tol = 0.02 / du + 0.0005
  printf "laug · uptime %.3f s, wall %.3f s, wall/uptime %.4f, %d step(s) over 0.5 s\n", du, dw, r, n
  if (n == 0 && r > 1 - tol && r < 1 + tol)
    print "laug · the clocks agree: an interval read off either one means what it says"
  else
    print "laug · the wall clock moves against the uptime clock here: wall-clock intervals (TTL reaps, dynamic config polls) run long or short by that ratio"
}'
