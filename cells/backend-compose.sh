#!/usr/bin/env bash
# Compose backend for laugd: one compose project per cell, host ports from the block laugd allotted.
# Contract: create <id> -> JSON on stdout; destroy <id>; health <id> -> exit 0 if healthy. LAUG_BLOCK in env.
# cell.yml takes PROJECT (via -p) and FRONTEND_PORT / UI_PORT / PROMETHEUS_PORT / GRAFANA_PORT / TOXIPROXY_PORT,
# BIND_ADDR, DYNCONFIG_DIR and SERVER_TAG from env. The block comes from laugd's free list, so two live cells cannot share
# ports; this script never picks them. Ports bind BIND_ADDR, 127.0.0.1 unless set, the same default as laug's
# -bind loopback; the addresses printed below stay 127.0.0.1, which also reaches a 0.0.0.0 bind.
# Pooled cells default to Postgres visibility; VISIBILITY=es opts in.
# SERVER_TAG (default 1.31.0, the default in cell.yml and the Makefile; harness-test checks they agree) is the
# temporalio/server and admin-tools tag every cell of this pool runs. laug pool serve -server-tag x.y.z
# sets it; create prints it as server_tag, which laugd puts in the lease.
# LAUG_COMPOSE_EXTRA, unset by default and always under the gate, names a second compose file, relative to the checkout
# or absolute, that create lays over cell.yml: -f cell.yml -f <file> on its compose up. It is a lab arm's override, such
# as cells/compose/cpus4.yml; set and missing, create refuses before compose runs. destroy passes cell.yml alone, as
# cells/pool-down.sh does: down finds the cell's containers by the project name, and an override that adds no service,
# network or volume leaves down the same work (docker compose config lists the same with cpus4.yml).
# faults/run.sh serverfi recreates temporal-history on cell.yml and fi.yml alone, so a recreated history runs without
# the override.
set -euo pipefail

cmd="${1:?create|destroy|health}"; id="${2:?cell id}"
here="$(cd "$(dirname "$0")" && pwd)"
compose_dir="$here/compose"
compose_files=(-f "$compose_dir/cell.yml")
project="laug-$id"
# Per-cell dynamic config: create copies the template here and the cell mounts this directory, so a fault that
# edits one cell's frontend.rps cannot reach another cell. The directory, not the file, is mounted: yq -i writes
# a new inode, which a single-file bind mount would never see. cell-id is the marker faults/run.sh checks.
run_dir="$here/run/$project"
dynconfig_dir="$run_dir/dynamicconfig"
visibility="${VISIBILITY:-postgres}"
block="${LAUG_BLOCK:-0}"
bind_addr="${BIND_ADDR:-127.0.0.1}"
server_tag="${SERVER_TAG:-1.31.0}"
[[ "$server_tag" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "SERVER_TAG must be a release tag, x.y.z; got '$server_tag'" >&2; exit 2; }
# laug dials 127.0.0.1 for health and everything after, so a single interface address would leave nothing on loopback.
[[ "$bind_addr" == 127.0.0.1 || "$bind_addr" == 0.0.0.0 ]] || { echo "BIND_ADDR must be 127.0.0.1 or 0.0.0.0; laug dials 127.0.0.1" >&2; exit 2; }

offset=$(( block * 10 ))
frontend_port=$(( 7233 + offset ))
ui_port=$(( 8080 + offset ))
grafana_port=$(( 3000 + offset ))
prometheus_port=$(( 9090 + offset ))
toxiproxy_port=$(( 8474 + offset ))

compose() {
  local enable_es=false profiles=""
  [[ "$visibility" == es ]] && { enable_es=true; profiles=es; }
  ENABLE_ES="$enable_es" COMPOSE_PROFILES="$profiles" BIND_ADDR="$bind_addr" SERVER_TAG="$server_tag" \
  FRONTEND_PORT="$frontend_port" UI_PORT="$ui_port" GRAFANA_PORT="$grafana_port" PROMETHEUS_PORT="$prometheus_port" TOXIPROXY_PORT="$toxiproxy_port" \
  DYNCONFIG_DIR="$dynconfig_dir" \
  docker compose -p "$project" --project-directory "$compose_dir" "${compose_files[@]}" "$@"
}

case "$cmd" in
  create)
    # laugd drops the health verb's output, so a missing bin/laug would only show as a create timeout 5 min later.
    [[ -x "$here/../bin/laug" ]] \
      || { echo "bin/laug is not built: make build builds it" >&2; exit 2; }
    if [[ -n "${LAUG_COMPOSE_EXTRA:-}" ]]; then
      extra="$LAUG_COMPOSE_EXTRA"; [[ "$extra" == /* ]] || extra="$(cd "$here/.." && pwd)/$extra"
      [[ -f "$extra" ]] || { echo "LAUG_COMPOSE_EXTRA=$LAUG_COMPOSE_EXTRA: no such file" >&2; exit 2; }
      compose_files+=(-f "$extra")
    fi
    mkdir -p "$dynconfig_dir"
    cp "$compose_dir/dynamicconfig/laug.yaml" "$dynconfig_dir/laug.yaml"
    printf '%s\n' "$id" > "$dynconfig_dir/cell-id"
    compose up -d >&2
    printf '{"id":"%s","address":"127.0.0.1:%s","ui":"http://127.0.0.1:%s","grafana":"http://127.0.0.1:%s","prometheus":"http://127.0.0.1:%s","toxiproxy":"http://127.0.0.1:%s","project":"%s","dynconfig":"%s","server_tag":"%s","backend":"compose"}\n' \
      "$id" "$frontend_port" "$ui_port" "$grafana_port" "$prometheus_port" "$toxiproxy_port" "$project" "$dynconfig_dir/laug.yaml" "$server_tag"
    ;;
  destroy)
    # destroy exits with down's status (set -e), before the rm below: a down that fails may leave containers on the
    # block's ports. laugd then keeps the block, and after a release or a failed create it keeps the cell, as stuck.
    # The cell's directory stays too, for laug pool down -remove-cells.
    compose --profile es down -v --remove-orphans >&2
    if [[ "$run_dir" == "$here/run/laug-"?* ]]; then rm -rf -- "$run_dir"; fi
    ;;
  health)
    # Warm means: the frontend answers, the namespace exists (cluster health passes before it does), the current
    # history process owns every shard, and one workflow completed. On a fresh cell the shards were ready within
    # 1 s of cluster health on both hosts measured, but after a history restart they are not.
    temporal operator cluster health --address "127.0.0.1:$frontend_port" >/dev/null
    temporal operator namespace describe -n default --address "127.0.0.1:$frontend_port" >/dev/null 2>&1
    "$here/shards-ready.sh" "$project"
    # One HealthCheck round trip in bin/laug's own process, namespace default, no SDK metrics, visible within 10 s.
    "$here/../bin/laug" health check -address "127.0.0.1:$frontend_port" -visibility-wait 10s >/dev/null 2>&1
    # The cell is warm. Record its soft assertions so far, each role since its last start (cells/softasserts.sh),
    # beside its dynamic config, in cells/run/laug-<id>/softasserts.json, by the health call that passes. Recorded
    # only: a count does not fail warm yet, and a count that printed nothing leaves no file.
    { "$here/softasserts.sh" "$project" > "$run_dir/.softasserts.json" 2>/dev/null
      [[ -s "$run_dir/.softasserts.json" ]] && mv -f "$run_dir/.softasserts.json" "$run_dir/softasserts.json"; } || true
    rm -f "$run_dir/.softasserts.json"
    ;;
  *) echo "unknown command: $cmd" >&2; exit 2 ;;
esac
