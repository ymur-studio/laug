#!/usr/bin/env bash
# Bring one compose cell up, Prometheus and Grafana with it, its five ports on BIND_ADDR, and wait until it serves:
# the frontend answers cluster health, NAMESPACE is registered and the current history process owns every shard
# (cells/shards-ready.sh), 300 s at most. The last two lines say how long that took and where the UI, Prometheus and
# Grafana answer. laug cell up runs it. This is the cell-up recipe of the Makefile laug replaced,
# moved here: the same lines and exits, its make variables now the environment's.
# Environment, all set by the caller: docker compose reads ENABLE_ES, COMPOSE_PROFILES, BIND_ADDR, SERVER_TAG and the
# five ports (FRONTEND_PORT, UI_PORT, PROMETHEUS_PORT, GRAFANA_PORT, TOXIPROXY_PORT) for cells/compose/cell.yml.
# PROJECT is the compose project; SERVER_FI=1 lays cells/compose/fi.yml over cell.yml, with LAUG_FI_* from the
# environment, and runs only on the tag cells/compose/config/laug-server.yaml was vendored from; CELL_ADDRESS is the
# frontend it waits on and PROM_URL the Prometheus it names; NAMESPACE is the namespace it waits for; VISIBILITY,
# postgres or es, is named in the last lines.
# Usage: cells/up.sh. Exit 0 serving; 1 not serving after 300 s; 2 SERVER_FI=1 on a tag laug-server.yaml was not
# vendored from, or a name above unset.
set -o pipefail
cd "$(dirname "$0")/.." || exit 2
for v in SERVER_TAG PROJECT CELL_ADDRESS PROM_URL NAMESPACE VISIBILITY UI_PORT GRAFANA_PORT; do
  [ -n "${!v:-}" ] || { echo "laug · cells/up.sh: $v is not set; ${LAUG:-bin/laug} cell up sets it" >&2; exit 2; }
done
fi_tag=$(sed -n 's/^# laug-server-tag: \([0-9.]*\)$/\1/p' cells/compose/config/laug-server.yaml 2>/dev/null)
[ "${SERVER_FI:-}" != 1 ] || [ "$SERVER_TAG" = "$fi_tag" ] || { echo "laug · SERVER_FI=1: compose/config/laug-server.yaml is the ${fi_tag:-unknown} template and this cell would run $SERVER_TAG; ${LAUG:-bin/laug} cell fi-template-check -server-tag $SERVER_TAG shows what changed" >&2; exit 2; }
compose=(docker compose -p "$PROJECT" --project-directory cells/compose -f cells/compose/cell.yml)
[ "${SERVER_FI:-}" != 1 ] || compose+=(-f cells/compose/fi.yml)
start=$(date +%s)
"${compose[@]}" up -d
until temporal operator cluster health --address "$CELL_ADDRESS" >/dev/null 2>&1 \
      && temporal operator namespace describe -n "$NAMESPACE" --address "$CELL_ADDRESS" >/dev/null 2>&1 \
      && cells/shards-ready.sh "$PROJECT"; do
  [ $(( $(date +%s) - start )) -lt 300 ] || { echo "cell not SERVING after 300 s"; exit 1; }; sleep 2
done
echo "cell $PROJECT SERVING, server $SERVER_TAG, namespace $NAMESPACE registered, all shards owned, visibility $VISIBILITY, after $(( $(date +%s) - start )) s"
echo "UI http://localhost:$UI_PORT  Prometheus $PROM_URL  Grafana http://localhost:$GRAFANA_PORT"
