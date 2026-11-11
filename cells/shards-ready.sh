#!/usr/bin/env bash
# shards-ready.sh <compose-project> [--ts]: exit 0 once the cell's current, running history process has logged
# "initial shards ready" with total equal to NUM_HISTORY_SHARDS. With --ts, print that line's timestamp (UTC).
#
# History logs this line once every shard it owns has asserted ownership; its own gRPC health turns SERVING 5 s
# after that (server 1.31.0 service/history/service.go:79-90, shard/controller_impl.go:519; read in source).
# The line's shape is seen: {"msg":"initial shards ready",...,"total":512,...}. The server image has no
# gRPC health probe, so the log line is the signal. Only the logs since the container's last start count: after
# a restart, docker logs still holds the previous process's line, which would call a cell ready while it
# reacquires. A container in restart backoff is listed by docker ps but runs nothing, so it is not ready.
set -euo pipefail

project="${1:?compose project, e.g. laug or laug-<id>}"
here="$(cd "$(dirname "$0")" && pwd)"
want="$(yq -r '."x-server-env".NUM_HISTORY_SHARDS' "$here/compose/cell.yml")"

cid="$(docker ps -q --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=temporal-history)"
[[ -n "$cid" && "$(wc -l <<<"$cid")" -eq 1 ]] || exit 1
st="$(docker inspect -f '{{.State.Status}} {{.State.StartedAt}}' "$cid")"
[[ "${st%% *}" == running ]] || exit 1
started="${st#* }"
line="$(docker logs --since "$started" "$cid" 2>&1 | grep -m1 '"msg":"initial shards ready"' || true)"
[[ -n "$line" ]] && grep -q "\"total\":$want," <<<"$line" || exit 1
if [[ "${2:-}" == --ts ]]; then jq -r .ts <<<"$line"; fi
