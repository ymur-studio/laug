#!/usr/bin/env bash
# A stand-in for the docker CLI in cellstat's tests (main_test.go). It answers `ps` and `inspect` from files in
# $FAKE_DOCKER, per verb: <verb>.sleep seconds to hang (the call never answers), <verb>.delay seconds to wait before it
# answers, <verb>.err on stderr, <verb>.out on stdout, <verb>.exit the exit code (0 when absent). Every call appends
# its arguments to calls.log.
set -u
d="${FAKE_DOCKER:?FAKE_DOCKER names the dir of answers}"
v="${1:-}"
printf '%s\n' "$*" >> "$d/calls.log"
if [[ -f "$d/$v.sleep" ]]; then exec sleep "$(cat "$d/$v.sleep")"; fi
if [[ -f "$d/$v.delay" ]]; then sleep "$(cat "$d/$v.delay")"; fi
if [[ -f "$d/$v.err" ]]; then cat "$d/$v.err" >&2; fi
if [[ -f "$d/$v.out" ]]; then cat "$d/$v.out"; fi
exit "$(cat "$d/$v.exit" 2>/dev/null || echo 0)"
