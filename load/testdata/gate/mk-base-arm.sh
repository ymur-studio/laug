#!/usr/bin/env bash
# Makes load/testdata/gate/base-arm from load/testdata/gate/base, by cp and jq only: one gate run of gate/plan.yaml's
# incumbent arm, every item ADVANCE, A/A on 1.31.0. Usage: load/testdata/gate/mk-base-arm.sh <out>, a directory that
# does not exist yet. make harness-test runs it and diffs its output against the committed base-arm, so an edit to
# base reaches base-arm only through this script.
#   - kh/ is gone. plan.json names incumbent 1.31.0 after wave_min_s and lists incumbent (steady's load, arm
#     incumbent, pair steady), steady and pl, items_in_plan 3, as gate/run.sh --plan prints
#     load/testdata/gate/plan-arm.yaml.
#   - incumbent/ is steady's load on kh's cell (9c0d1e2f) moved to block 0 and leased first: its lease is kh's with
#     block 0's address, ports and lease times and requester gate-<gate id>-incumbent; its exit.json, run JSON and
#     verdict JSON are steady's, with its own run id, task queue and kh's project.
#   - steady/ moves to block 1 (its lease, with block 1's lease times, and its run JSON's cell and prometheus), pl/ to
#     block 2 (its lease), so the leases follow the plan's order on base's timeline.
#   - thresholds-incumbent.json is a copy of thresholds.json. poll.jsonl is base's: its three projects are the three
#     leased here.
set -euo pipefail
out="${1:?usage: load/testdata/gate/mk-base-arm.sh <out>}"
here="$(cd "$(dirname "$0")" && pwd)"; base="$here/base"
[[ ! -e "$out" ]] || { echo "mk-base-arm.sh: $out exists" >&2; exit 2; }
gid="$(jq -r .gate_id "$base/gate.json")"
put() { local f="$1"; shift; jq "$@" "$f" > "$f.tmp" && mv "$f.tmp" "$f"; }   # put <file> <jq args>: edit in place
slot() { jq '{address, ui, grafana, prometheus, toxiproxy, leased_at, expires_at}' "$base/$1/lease.json"; }   # slot <key>: its block's

cp -r "$base" "$out"
rm -r "$out/kh"
cp "$base/thresholds.json" "$out/thresholds-incumbent.json"
put "$out/plan.json" '(.items[] | select(.key == "steady")) as $s
  | to_entries | map(if .key == "wave_min_s" then ., {key: "incumbent", value: "1.31.0"} else . end) | from_entries
  | .items = [({key: "incumbent", kind: $s.kind, arm: "incumbent", pair: "steady"} + ($s | del(.key, .kind))),
              (.items[] | select(.key == "steady" or .key == "pl"))]
  | .items_in_plan = 3'

mkdir "$out/incumbent"
jq --argjson p "$(slot steady)" --arg r "gate-$gid-incumbent" '. + $p | .block = 0 | .requester = $r' "$base/kh/lease.json" > "$out/incumbent/lease.json"
cp "$base/steady/exit.json" "$out/incumbent/exit.json"
jq --arg run "$gid-incumbent" --arg project "$(jq -r .project "$base/kh/lease.json")" \
  '.run_id = $run | .task_queue = "omes-\($run)" | .project = $project' "$base/steady/$gid-steady.json" > "$out/incumbent/$gid-incumbent.json"
jq --arg run "$gid-incumbent" '.run_id = $run' "$base/steady/$gid-steady.verdict.json" > "$out/incumbent/$gid-incumbent.verdict.json"

put "$out/steady/lease.json" --argjson p "$(slot pl)" '. + $p | .block = 1'
put "$out/steady/$gid-steady.json" --argjson p "$(slot pl)" '.cell = $p.address | .prometheus = $p.prometheus'
put "$out/pl/lease.json" --argjson p "$(slot kh)" '. + $p | .block = 2'
