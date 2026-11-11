#!/usr/bin/env bash
# Prints a threshold set as load/thresholds.sh resolved it, read on stdin: the note, each threshold with its max and
# what no data reads, the release file's overrides, the host its numbers were measured on, and the compare: block. The
# body is the Makefile's thresholds recipe as it stood, moved here unchanged but for $$ written $ and each "; \" a line
# end (laug thresholds runs load/thresholds.sh, then this; TestPrintThresholds holds the two equal). It runs as the
# recipe did, under pipefail and without errexit. -json prints the set as one JSON object instead.
#
# Usage: load/thresholds.sh <server-tag> | load/print-thresholds.sh [-json]
set -o pipefail

th="$(cat)"
if [[ "${1-}" == -json ]]; then yq -o=json -I=0 '.' <<<"$th"; exit; fi
echo "laug · thresholds: $(yq -r '.resolved.note' <<<"$th")"
yq -o=json -I=0 '.' <<<"$th" | jq -r '.resolved.base as $b | .thresholds[] | [.name, (.max | tostring), (if .max == null then "recorded, not judged (no max)" elif .no_data == "not_judged" then "not judged: not produced on this release (\(.override.file // $b))" else "EXCEEDS" end)] | @tsv' \
  | awk -F'\t' 'BEGIN { printf "%-41s %8s  %s\n", "metric", "max", "no data reads" } { printf "%-41s %8s  %s\n", $1, $2, $3 }'
yq -r '.resolved.overrides[] | "override " + .name + ": " + .reason + " (evidence: " + .evidence + ")"' <<<"$th"
yq -o=json -I=0 '.' <<<"$th" | jq -r '"host              \(.resolved.host.type), \(.resolved.host.vcpu) vCPU, \(.resolved.host.mem_gib) GiB, the host its numbers were measured on", (if .compare == null then "compare           none: R7 has nothing to compare" elif any(.compare.rows[]; .tolerance != null) then "compare           judging: tolerances from \(.compare.gates) A/A gates of \(.compare.release), \(.compare.evidence)" else "compare           recording: no tolerance yet; R7 records and judges nothing" end), (.compare.rows[]? | "  \(.name)  \(.scale), floor \(.floor), tolerance \(.tolerance // "none")")'
