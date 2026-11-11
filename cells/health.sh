#!/usr/bin/env bash
# Is a cell healthy end to end: its frontend answers cluster health; one HealthCheck workflow round-trips on it and is
# visible (bin/laug health check, which runs it in laug's own process, SDK metrics off); and Prometheus scrapes the
# five server targets, frontend twice, history, matching and worker, up within 20 s.
# laug cell health runs it. This is the cell-health recipe of the Makefile laug replaced, moved here: the same
# lines and exits, its make variables now the environment's, and the round trip bin/laug's where the recipe built it
# with go run. It prints the first two commands before it runs each, as make echoed them.
# Environment, set by the caller: CELL_ADDRESS, the frontend; NAMESPACE, where HealthCheck runs; PROM_URL, the cell's
# Prometheus. LAUG_ROOT, a seam, as the Firecracker scripts read it: the checkout whose bin/laug runs, this one unless
# set. health check takes the cell and the namespace as flags and names a stray CELL_ADDRESS or NAMESPACE, so neither
# reaches it.
# Usage: cells/health.sh. Exit 0 healthy; 1 fewer than 5 targets up after 20 s, or the round trip failed; the code of
# temporal when it fails; 2 with a name above unset, or bin/laug not built.
set -o pipefail
cd "$(dirname "$0")/.." || exit 2
for v in CELL_ADDRESS NAMESPACE PROM_URL; do
  [ -n "${!v:-}" ] || { echo "laug · cells/health.sh: $v is not set; ${LAUG:-bin/laug} cell health sets it" >&2; exit 2; }
done
hc="${LAUG_ROOT:+$LAUG_ROOT/}bin/laug"
[ -x "$hc" ] || {
  echo "laug · cells/health.sh: $hc is not built: make build builds it" >&2; exit 2; }
# q writes a value as make's shq did: in single quotes, each ' as '\''.
q() { printf "'%s'" "${1//\'/\'\\\'\'}"; }
echo "temporal operator cluster health --address $(q "$CELL_ADDRESS")"
temporal operator cluster health --address "$CELL_ADDRESS" || exit
echo "$hc health check -address $(q "$CELL_ADDRESS") -namespace $NAMESPACE"
env -u CELL_ADDRESS -u NAMESPACE "$hc" health check -address "$CELL_ADDRESS" -namespace "$NAMESPACE" || exit
for _ in $(seq 1 10); do
  up=$(curl -sf "$PROM_URL/api/v1/targets" | jq -r '[.data.activeTargets[] | select(.labels.job | startswith("temporal-")) | select(.health=="up")] | length')
  [ "$up" -ge 5 ] && break; sleep 2
done
curl -sf "$PROM_URL/api/v1/targets" | jq -r '.data.activeTargets[] | "\(.labels.job)\t\(.labels.instance)\t\(.health)"'
[ "$up" -ge 5 ] || { echo "only $up server targets up after 20 s; expected 5 (frontend x2, history, matching, worker)"; exit 1; }
