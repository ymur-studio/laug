#!/usr/bin/env bash
# Tear a compose cell down: its containers, its volumes (Postgres's and Prometheus's data) and its network, every one
# compose made for PROJECT, the Elasticsearch profile's included. laug cell down runs it. This is the
# cell-down recipe of the Makefile laug replaced, moved here: the same command, its make variables now the
# environment's. The recipe ignored its exit; laug cell down -remove-volumes exits with it.
# Environment, set by the caller: PROJECT, the compose project; SERVER_FI=1 lays cells/compose/fi.yml over cell.yml, as
# laug cell up -server-fi does; and ENABLE_ES, COMPOSE_PROFILES, BIND_ADDR, SERVER_TAG and the five ports, which docker
# compose reads for cells/compose/cell.yml.
# Usage: cells/down.sh. Exit: docker compose's; 2 with PROJECT unset.
set -o pipefail
cd "$(dirname "$0")/.." || exit 2
[ -n "${PROJECT:-}" ] || { echo "laug · cells/down.sh: PROJECT is not set; ${LAUG:-bin/laug} cell down sets it" >&2; exit 2; }
compose=(docker compose -p "$PROJECT" --project-directory cells/compose -f cells/compose/cell.yml)
[ "${SERVER_FI:-}" != 1 ] || compose+=(-f cells/compose/fi.yml)
"${compose[@]}" --profile es down -v --remove-orphans
