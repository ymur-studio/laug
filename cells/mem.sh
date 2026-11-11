#!/usr/bin/env bash
# The memory a compose cell's containers hold right now, from one docker stats: a line a container on stderr, sorted by
# name, then "total <n> MiB across <m> containers" on stdout. laug cell mem runs it. This is the
# cell-mem recipe of the Makefile laug replaced, moved here: the same pipeline, its make variables now the
# environment's.
# With no container in PROJECT, docker stats gets no name and reads every running container, as the recipe did.
# Environment, set by the caller: PROJECT, the compose project; SERVER_FI=1 lays cells/compose/fi.yml over cell.yml;
# and ENABLE_ES, COMPOSE_PROFILES, BIND_ADDR, SERVER_TAG and the five ports, which docker compose reads for
# cells/compose/cell.yml.
# Usage: cells/mem.sh. Exit: the pipeline's, docker's own code when a docker call fails; 2 with PROJECT unset.
set -o pipefail
cd "$(dirname "$0")/.." || exit 2
[ -n "${PROJECT:-}" ] || { echo "laug · cells/mem.sh: PROJECT is not set; ${LAUG:-bin/laug} cell mem sets it" >&2; exit 2; }
compose=(docker compose -p "$PROJECT" --project-directory cells/compose -f cells/compose/cell.yml)
[ "${SERVER_FI:-}" != 1 ] || compose+=(-f cells/compose/fi.yml)
# The ids are split into words on purpose: one argument a container.
# shellcheck disable=SC2046
docker stats --no-stream --format '{{.Name}}\t{{.MemUsage}}' $("${compose[@]}" ps -q) | sort | tee /dev/stderr \
  | awk -F'\t' '{ split($2, a, " "); v=a[1]; if (v ~ /GiB/) { sub(/GiB/, "", v); v*=1024 } else { sub(/MiB/, "", v) } ; s+=v } END { printf "total %.0f MiB across %d containers\n", s, NR }'
