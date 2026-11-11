#!/usr/bin/env bash
# Stop this checkout's pool and every gate's, then remove every laug-* compose cell, its volumes and networks with it,
# the per-cell dirs in cells/run and .last-lease.json, and print what is left. laug pool down -remove-cells runs it.
# This is the pool-down recipe of the Makefile laug replaced, moved here: the same steps in the same order.
# The pool laug pool serve keeps in cells/run/pool.pid is stopped first, only while that pid's command line is laug pool
# serve on a backend other than firecracker, so a stale file never stops another process: TERM, then 3 s at most. Then
# every gate's own pool, laug pool serve -pid-file none, found by its command line, since a gate keeps no file of it
# beside the checkout's: its cells go below. Pool cells keep Postgres's and Prometheus's data in named volumes
# (laug-<id>_pgdata, laug-<id>_promdata), which go with docker compose down -v. MicroVM cells are not compose cells:
# their pool runs as root, and sudo bin/laug pool down -backend firecracker -remove-cells stops it and removes them.
# Environment: none of its own. docker compose reads cells/compose/cell.yml, whose every variable has a default, the
# values the recipe passed.
# Usage: cells/pool-down.sh. Exit 0; 1 when a docker compose down failed, after every other step ran.
set -o pipefail
cd "$(dirname "$0")/.." || exit 2
p="$(cat cells/run/pool.pid 2>/dev/null)"; a=""
[[ $p =~ ^[1-9][0-9]*$ ]] && a="$(tr '\0' ' ' 2>/dev/null </proc/"$p"/cmdline)"
serve_re='^([^ ]*/)?laug pool serve( |$)'; fc_re=' --?backend[ =]firecracker( |$)'
gate_re='^([^ ]*/)?laug pool serve( .*)? --?pid-file[ =]none( |$)'
if [[ $a =~ $serve_re && ! $a =~ $fc_re ]] && kill -TERM "$p" 2>/dev/null; then
  for _ in $(seq 1 30); do kill -0 "$p" 2>/dev/null || break; sleep 0.1; done; echo "stopped laug pool serve, pid $p"
fi
pkill -f "$gate_re" 2>/dev/null
failed=()
for p in $(docker ps -a --format '{{.Names}}' | grep -oE '^laug-[0-9a-f]{8}' | sort -u); do
  echo "removing $p"
  docker compose -p "$p" --project-directory cells/compose -f cells/compose/cell.yml --profile es down -v --remove-orphans \
    >/dev/null 2>&1 || failed+=("$p")
done
rm -rf cells/run/laug-*
rm -f .last-lease.json
if ls -d /run/laug/b[0-9]*/ >/dev/null 2>&1; then echo "laug · Firecracker cells are still up: sudo bin/laug pool down -backend firecracker -remove-cells"; fi
echo "left: $(docker ps -a --format '{{.Names}}' | grep -c '^laug-' || true) containers, $(docker volume ls -q | grep -c '^laug-' || true) volumes, $(docker network ls --format '{{.Name}}' | grep -c '^laug-' || true) networks, $(ls -d cells/run/laug-* 2>/dev/null | wc -l) per-cell dirs"
if (( ${#failed[@]} )); then echo "laug · cells/pool-down.sh: docker compose down failed for ${failed[*]}" >&2; exit 1; fi
