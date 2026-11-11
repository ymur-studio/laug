#!/usr/bin/env bash
# The gate's one exit: hands a gate run's verdict.md and verdict.json, written by bin/laug judge, to the sink
# GATE_POST names. laug gate post <id> runs it on gate/results/<id>. Usage: gate/post.sh <results-dir>.
#
# GATE_POST=local, the default, is the only sink built: it prints both paths and the verdict, publishes nothing, and
# exits 0 whatever the verdict says (gate/run.sh's own exit, laug gate run's, carries the verdict). A verdict from a
# fake gate (laug gate run -fake, GATE_FAKE=1) says so on its VERDICT line: its items were fixtures, and no cell ran.
# Any other GATE_POST exits 2, "not wired": laug has no publish target yet, and which sink comes next (a job summary as
# .github/workflows/test.yml writes for its gate on fakes, a pull request comment, or none) is not chosen.
# This script reads the two files and never changes them, and makes no network call.
#
# Exit 2 as well when there is nothing sound to hand on: either file missing or empty, a verdict.json whose verdict is
# not ADVANCE or BLOCK, or a verdict.md whose last line does not carry the same verdict.
# gate/post.sh --check reads no file: exit 0 when GATE_POST names a sink that is wired, 2 and why when not. gate/run.sh
# runs it before anything, so a gate whose verdict could not be handed on does not take the host for 10 minutes first.
set -euo pipefail

dir="${1:?usage: gate/post.sh <results-dir>, or gate/post.sh --check}"
sink="${GATE_POST:-local}"
case "$sink" in
  local) ;;
  *) echo "laug · GATE_POST=$sink: not wired: laug has no publish target yet; GATE_POST=local is the only sink" >&2; exit 2 ;;
esac
[[ "$dir" != --check ]] || exit 0

json="$dir/verdict.json"; md="$dir/verdict.md"
for f in "$json" "$md"; do
  [[ -s "$f" ]] || { echo "laug · nothing to hand on: no $f (bin/laug judge writes it)" >&2; exit 2; }
done
verdict="$(jq -r 'if type == "object" and (.verdict == "ADVANCE" or .verdict == "BLOCK") then .verdict else "" end' "$json" 2>/dev/null)" || verdict=""
[[ -n "$verdict" ]] || { echo "laug · nothing to hand on: $json holds no ADVANCE or BLOCK verdict" >&2; exit 2; }
last="$(grep -v '^[[:space:]]*$' "$md" | tail -1)"
[[ "$last" == "**$verdict**" ]] || { echo "laug · nothing to hand on: $md ends '$last', and $json says $verdict" >&2; exit 2; }

echo "laug · gate $(jq -r '.gate_id // "unknown"' "$json") handed on to $sink (GATE_POST=$sink): nothing is published"
echo "verdict.json  $json"
echo "verdict.md    $md"
jq -r 'if .class != null then "class         \(.class)\(if .rerun then " (rerun: the host was not quiet; on a quiet host \(.class_if_quiet))" else "" end)" else empty end' "$json"
echo "VERDICT: $verdict$(jq -r 'if .fake == true then " (GATE_FAKE=1: fixtures, no cell ran)" else "" end' "$json")"
