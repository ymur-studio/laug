#!/usr/bin/env bash
# Fake laugd backend for a fake gate (GATE_FAKE=1; gate/run.sh, make harness-test): the contract of
# cells/backend-compose.sh (create <id> prints the cell as JSON, destroy <id>, health <id>; LAUG_BLOCK in the
# environment) with no Docker. A cell is a file, $FAKE_GATE_DIR/projects/laug-<id>, which the fake docker's compose ls
# lists as a running project; destroy removes it. A create makes the file first, as compose up starts containers before
# the cell is whole, so a create killed midway leaves its project up. Each create that ends appends "<id> <start> <end>
# <tag>", times in real seconds, to $FAKE_GATE_DIR/creates, so a test can see that no two overlapped and which tag each
# cell says. FAKE_GATE_CREATE_S (0.05) is how long a create takes; FAKE_GATE_CREATE_FAIL=<n> fails the n-th create. The
# tag a cell says is SERVER_TAG, which laugd sets per lease to the tag the lease named (1.31.0 when unset);
# FAKE_GATE_BACKEND_TAG says another for every create, and FAKE_GATE_BACKEND_TAG_AT="<n> <tag>" says <tag> for the n-th
# create alone. FAKE_GATE_DESTROY_FAIL set fails every destroy and leaves the project up. dynconfig is a path under
# FAKE_GATE_DIR, outside the repository.
set -euo pipefail
cmd="${1:?create|destroy|health}"; id="${2:?cell id}"; d="${FAKE_GATE_DIR:?}"; block="${LAUG_BLOCK:-0}"
mkdir -p "$d/projects"
off=$(( block * 10 ))
case "$cmd" in
  create)
    n=$(( $(cat "$d/creates.n" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$d/creates.n"
    start="$(date +%s.%N)"; : > "$d/projects/laug-$id"; sleep "${FAKE_GATE_CREATE_S:-0.05}"
    [[ "$n" != "${FAKE_GATE_CREATE_FAIL:-0}" ]] || { echo "fake backend: create $n fails" >&2; exit 1; }
    tag="${FAKE_GATE_BACKEND_TAG:-${SERVER_TAG:-1.31.0}}"; at="${FAKE_GATE_BACKEND_TAG_AT:-}"
    [[ -z "$at" || "${at%% *}" != "$n" ]] || tag="${at#* }"
    echo "$id $start $(date +%s.%N) $tag" >> "$d/creates"
    jq -cn --arg id "$id" --argjson off "$off" --arg dyn "$d/run/laug-$id/laug.yaml" --arg tag "$tag" \
      '{id: $id, address: "127.0.0.1:\(7233 + $off)", ui: "http://127.0.0.1:\(8080 + $off)", grafana: "http://127.0.0.1:\(3000 + $off)",
        prometheus: "http://127.0.0.1:\(9090 + $off)", toxiproxy: "http://127.0.0.1:\(8474 + $off)", project: "laug-\($id)",
        dynconfig: $dyn, server_tag: $tag}' ;;
  destroy)
    [[ -z "${FAKE_GATE_DESTROY_FAIL:-}" ]] || { echo "fake backend: destroy fails" >&2; exit 1; }
    rm -f "$d/projects/laug-$id" ;;
  health) ;;
  *) echo "unknown command: $cmd" >&2; exit 2 ;;
esac
