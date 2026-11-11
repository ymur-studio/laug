#!/usr/bin/env bash
# Check a cell's dynamic config, laug.yaml, with the server's own validator, temporal-server validate-dynamic-config,
# run inside the cell's temporal-history container. With NOCELL=1 it checks the template,
# cells/compose/dynamicconfig/laug.yaml, with the validator in temporalio/server:SERVER_TAG instead, in a container with
# no network: no cell, so a new release's key names can be checked before any cell runs it (1.31.0 and 1.32.1 passed).
# The validator warns on key names it does not know, and exits 0 on its own warnings and errors (seen on server
# 1.31.0 and 1.32.1), so this fails on any warning: or error: line. Ran on a local cell and a pool cell.
# laug cell dynconfig-check runs it. This is the
# dynconfig-check recipe of the Makefile laug replaced, moved here: the same lines and exits, its make variables
# now the environment's.
# Environment, set by the caller: NOCELL, 1 for the template; SERVER_TAG, the image the template is checked with;
# PROJECT, the compose project of the cell, laug-<id> for a pool cell.
# Usage: cells/dynconfig-check.sh [lease-file]. A lease that names a guest is a microVM cell, whose containers run in
# the guest's Docker as project laug: lease_read (faults/lib.sh) reads it, PROJECT becomes laug, and every docker call
# runs in the guest over the lease's ssh (dk), the validator in the guest's temporal-history container on the laug.yaml
# mounted there. Exit 0 clean; 1 a warning: or error: line; 2 not one temporal-history container in PROJECT (a guest
# that does not answer, or holds another cell, finds none), a name unset, or a lease lease_read refuses; else the
# validator's or docker's own code.
set -o pipefail
cd "$(dirname "$0")/.." || exit 2
if [ "${NOCELL:-}" = 1 ]; then
  [ -n "${SERVER_TAG:-}" ] || { echo "laug · cells/dynconfig-check.sh: SERVER_TAG is not set; ${LAUG:-bin/laug} cell dynconfig-check sets it" >&2; exit 2; }
  echo "temporalio/server:$SERVER_TAG validate-dynamic-config on cells/compose/dynamicconfig/laug.yaml, the template (no cell)"
  out=$(docker run --rm --network none --entrypoint temporal-server -v "$PWD/cells/compose/dynamicconfig:/d:ro" \
    "temporalio/server:$SERVER_TAG" validate-dynamic-config /d/laug.yaml 2>&1); rc=$?
else
  dk() { docker "$@"; }; GUEST_SSH=""
  if [ -n "${1:-}" ]; then
    root="$PWD"
    # shellcheck source=faults/lib.sh
    . faults/lib.sh; lease_read "$1"
  fi
  [ -n "${PROJECT:-}" ] || { echo "laug · cells/dynconfig-check.sh: PROJECT is not set; ${LAUG:-bin/laug} cell dynconfig-check sets it" >&2; exit 2; }
  cid=$(dk ps -q --filter "label=com.docker.compose.project=$PROJECT" --filter label=com.docker.compose.service=temporal-history)
  [ -n "$cid" ] && [ "$(wc -l <<<"$cid")" -eq 1 ] || { echo "expected one temporal-history container in $PROJECT, found: ${cid:-none}" >&2; exit 2; }
  where="$PROJECT's laug.yaml"; [ -z "$GUEST_SSH" ] || where="cell $CELL's laug.yaml, in its guest at $GUEST_SSH"
  echo "$(dk inspect -f '{{.Config.Image}}' "$cid") validate-dynamic-config on $where"
  out=$(dk exec "$cid" temporal-server validate-dynamic-config /etc/temporal/config/dynamicconfig/laug.yaml 2>&1); rc=$?
fi
printf '%s\n' "$out"
[ "$rc" -eq 0 ] || exit "$rc"
! grep -qE '^[[:space:]]*(warning|error):' <<<"$out" || { echo "laug · dynconfig-check: the validator flagged the lines above (it exits 0 on them)" >&2; exit 1; }
