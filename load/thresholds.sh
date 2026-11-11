#!/usr/bin/env bash
# The one threshold resolver: given the server tag a run ran on, prints the threshold set that run is judged by.
# load/verdict.sh, faults/run.sh and faults/candidate.sh (through faults/lib.sh, thresholds_resolve) all call it, and
# laug thresholds prints it. load/read-thresholds.sh reads what it prints, as it reads load/steady-state.yaml.
#
#   THRESHOLDS set       that file, checked as the base is (below), and no release file: a set given explicitly wins
#   tag x.y.z            load/steady-state.yaml, the base (measured on its server_tag, 1.31.0), plus
#                        load/releases/x.y.yaml when that file exists
#   no file for x.y      the base unchanged, and the note says so: 1.31.x gets the base it was measured on
#   no tag, or no x.y    the base unchanged, and the note says why
#
# A release file names the base it was written against (base, base_server_tag) and lists overrides, each naming a
# threshold of the base, with a reason and an evidence path. The one kind is no_data: not_judged: the reading is still
# judged against its max whenever it has data, and reads "not produced on this release" instead of EXCEEDS when it has
# none. Exit 2 on a release file this script cannot apply as written: its release is not its file name, its base or
# base_server_tag is not the base's, or an override names no threshold of the base, names one twice, has another kind
# or another key (a max: here would otherwise read as applied and do nothing), or has no reason or no evidence.
# Exit 2 as well on a set, the base or a THRESHOLDS file, that relaxes a rule itself: a threshold with a no_data other
# than exceeds, or an override: key, or the set a top-level resolved: key (what this script prints; passed back as
# THRESHOLDS, its overrides would apply to any tag and be reported nowhere). no_data belongs in a release file, which
# says why and for which release. And on a max that is neither a number nor absent, so that every reader compares
# numbers: in jq a number is never greater than a string, so a quoted max of "100" would pass a reading of 150.
#
# A set names the host its numbers were measured on, host: {type, vcpu, mem_gib}, and may hold compare:, the readings
# the incumbent arm compares, each with a scale (ms, or share for a share past a bucket edge), a noise floor and a
# tolerance, null until A/A gates set it (internal/judge, R7; docs/DESIGN.md section 7.1). Exit 2 on a set with no host
# or a host that is not {type, vcpu, mem_gib} with a type name and whole numbers, and on a compare: with another key, a
# release that is not x.y.z, no row, or a row that names no threshold of the set or names one twice, has a scale its
# name does not fit, a floor not above 0, or a tolerance that is not a number at or above its floor; a tolerance also
# needs the wave, the number of gates and the results file that set it. And exit 2 on a release file key other than
# release, base, base_server_tag and overrides: a release file can neither name a host nor loosen the arm.
#
# Usage: load/thresholds.sh <server-tag>   (an empty tag: the run recorded none)
# Output, YAML on stdout: the base's own keys (window, server_tag, host, thresholds, compare when it has one, verdict),
# every threshold with no_data (exceeds, or what its override says) and an overridden one with override: {file, reason,
# evidence}; plus resolved: {base, base_server_tag, server_tag, release, release_file, given, note, overrides, host},
# where note is one line naming the files and why, and host is the base's. THRESHOLDS_RELEASES (load/releases) is
# where release files are looked up; make harness-test points it at files of its own. Every path printed is relative to
# the repository root, and a file outside it is printed without its directory, as
# "(outside the repository)/<major>.<minor>.yaml" or "a file outside the repository" for THRESHOLDS: raun and candidate
# results carry the note and the files, and a committed result carries no local path.
set -euo pipefail

tag="${1-}"
root="$(cd "$(dirname "$0")/.." && pwd)"
RELEASES="${THRESHOLDS_RELEASES:-load/releases}"
fail() { echo "thresholds: $*" >&2; exit 2; }
# unparsed <file>: why yq could not read a file. Another yq (the Python one, say) fails on every file, and a PATH
# with no yq fails too. So the file is blamed only when yq --version says mikefarah and v4, as bin/laug deps reads it.
# The Python yq (kislyuk/yq) fails on -o=json -I=0 for any file (yq 4.4.5, run in a scratch venv); the harness
# fakes it.
unparsed() {
  local v; v="$(yq --version 2>&1)" || true
  if [[ "$v" =~ mikefarah.*\ v4\. ]]; then echo "$1 does not parse"
  else echo "yq failed: laug needs mikefarah yq v4 (${LAUG:-bin/laug} deps)"; fi
}
inroot() { if [[ "$1" == /* ]]; then echo "$1"; else echo "$root/$1"; fi; }
root_real="$(realpath -- "$root")"
# shown <path> <outside>: the path relative to the repository root, or <outside> for a file that is not under it.
shown() { local r; r="$(realpath -m --relative-base="$root_real" -- "$1")"; if [[ "$r" == /* ]]; then echo "$2"; else echo "$r"; fi; }

given="${THRESHOLDS:-}"
if [[ -n "$given" ]]; then base_file="$given"; base="$(shown "$given" "a file outside the repository")"   # from the caller's directory
else base="load/steady-state.yaml"; base_file="$root/$base"; fi
named="${given:-$base}"   # the set as the caller named it, for a message on stderr
[[ -f "$base_file" ]] || fail "no threshold set at $named"
base_json="$(yq -o=json -I=0 '.' "$base_file")" || fail "$(unparsed "$named")"
jq -e '(.thresholds | type) == "array" and all(.thresholds[]; (.name | type) == "string")' <<<"$base_json" >/dev/null \
  || fail "$named holds no list of named thresholds"
# A set judges as it is written only when no rule in it is relaxed and every max is a number.
problems="$(jq -r '
  (if has("resolved") then "it holds resolved:, so load/thresholds.sh printed it; give the base it came from, and the release file applies by tag" else empty end),
  (.thresholds[] | .name as $n
   | (if has("no_data") and .no_data != "exceeds" then "threshold \($n): no_data is \(.no_data | tojson)" else empty end),
     (if has("override") then "threshold \($n): override: is not a key of a set" else empty end),
     (if (.max | type) as $t | $t != "number" and $t != "null" then "threshold \($n): max is \(.max | tojson), not a number" else empty end))' \
  <<<"$base_json")" || fail "$named could not be checked"
if [[ -n "$problems" ]]; then
  hint=""; ! grep -qE 'no_data|override|resolved' <<<"$problems" || hint="; no_data belongs in load/releases/<major>.<minor>.yaml, with a reason and evidence"
  fail "$named cannot be used as a threshold set: $(awk 'NR > 1 { printf "; " } { printf "%s", $0 }' <<<"$problems")$hint"
fi
# A set's host (docs/DESIGN.md section 7, the R6 paragraph) and the readings the incumbent arm compares (section 7.1):
# a set that names no host judges any host, and a compare: row that cannot be read as written would compare nothing.
hproblems="$(jq -r '
  def whole: type == "number" and . == floor and . >= 1;
  (if (.host | type) != "object" then "it names no host {type, vcpu, mem_gib}, the host its numbers were measured on (docs/DESIGN.md section 7, the R6 paragraph)"
   else
     ((.host | keys - ["type", "vcpu", "mem_gib"])[] | "host: \(.) is not a key of a host"),
     (if (.host.type | type) == "string" and (.host.type | test("^[a-z0-9][a-z0-9.-]{0,31}$")) then empty
      else "host type \(.host.type | tojson) is not a type name: lowercase letters, digits, . and -, 32 at most" end),
     (["vcpu", "mem_gib"][] as $k | if .host[$k] | whole then empty else "host \($k) is \(.host[$k] | tojson), not a whole number from 1" end)
   end),
  (if has("compare") | not then empty
   elif (.compare | type) != "object" then "compare is not a mapping"
   else
     [.thresholds[].name] as $names | .compare as $c
     | (($c | keys - ["release", "wave", "gates", "evidence", "rows"])[] | "compare: \(.) is not a key of compare"),
       (if $c.release | type == "string" and test("^[0-9]+\\.[0-9]+\\.[0-9]+$") then empty
        else "compare: release is \($c.release | tojson), not the release tag both arms ran, x.y.z" end),
       (if ($c.rows | type) != "array" or ($c.rows | length) == 0 then "compare: rows lists no reading"
        else
          ($c.rows[] | if type != "object" then "compare: a row is not a mapping" else
             (.name | tostring) as $n
             | ((keys - ["name", "scale", "floor", "tolerance"])[] | "compare row \($n): \(.) is not a key of a row"),
               (if any($names[]; . == $n) then empty else "compare row \($n) names no threshold of the set" end),
               (if .scale == "share" then (if $n | test("_over_[0-9]+ms$") then empty else "compare row \($n): scale share is for a share past a bucket edge, named <reading>_over_<n>ms" end)
                elif .scale == "ms" then (if $n | test("_ms$") then empty else "compare row \($n): scale ms is for a reading in ms, named <reading>_ms" end)
                else "compare row \($n): scale is \(.scale | tojson), not ms or share" end),
               (if (.floor | type) == "number" and .floor > 0 then empty else "compare row \($n): floor is \(.floor | tojson), not a number above 0" end),
               (if .tolerance == null then empty
                elif (.tolerance | type) != "number" then "compare row \($n): tolerance is \(.tolerance | tojson), neither null nor a number"
                elif (.floor | type) == "number" and .tolerance < .floor then "compare row \($n): tolerance \(.tolerance) is under its floor \(.floor)"
                else empty end)
           end),
          ([$c.rows[] | objects | .name] | group_by(.)[] | select(length > 1) | "compare row \(.[0]) is listed \(length) times")
        end),
       (if any($c.rows[]?; type == "object" and .tolerance != null) then
          (if ($c.wave | type) == "array" and ($c.wave | length) > 0 and all($c.wave[]; type == "string") then empty
           else "compare: a row has a tolerance, and wave is \($c.wave | tojson): a tolerance names the wave it was measured on" end),
          (if $c.gates | whole then empty else "compare: a row has a tolerance, and gates is \($c.gates | tojson): a tolerance names how many A/A gates set it" end),
          (if ($c.evidence | type) == "string" and ($c.evidence | test("^(cells/results/[A-Za-z0-9._-]+|docs/tables/gates\\.jsonl)$")) then empty
           else "compare: a row has a tolerance, and evidence is \($c.evidence | tojson): a tolerance names its results file in cells/results/ or docs/tables/gates.jsonl" end)
        else empty end)
   end)' <<<"$base_json")" || fail "$named could not be checked"
[[ -z "$hproblems" ]] || fail "$named cannot be used as a threshold set: $(awk 'NR > 1 { printf "; " } { printf "%s", $0 }' <<<"$hproblems")"
base_tag="$(jq -r '.server_tag // ""' <<<"$base_json")"

mm=""
if [[ "$tag" =~ ^([0-9]+)\.([0-9]+)(\.|$) ]]; then mm="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"; fi
desc="$base, the base${base_tag:+ ($base_tag baselines)}"
rel=""; rel_json='{"overrides": []}'
rel_path=""; [[ -z "$mm" ]] || rel_path="$(inroot "$RELEASES/$mm.yaml")"
if [[ -n "$given" ]]; then note="$base, given in THRESHOLDS: no release file applies"
elif [[ -z "$tag" ]]; then note="$desc, unchanged: no server tag to pick a release file by"
elif [[ -z "$mm" ]]; then note="$desc, unchanged: server tag '$tag' names no major.minor"
elif [[ ! -e "$rel_path" ]]; then note="$desc, unchanged: no $(shown "$rel_path" "(outside the repository)/$mm.yaml") for server $tag"
else
  rel="$(shown "$rel_path" "(outside the repository)/$mm.yaml")"
  rel_json="$(yq -o=json -I=0 '.' "$rel_path")" || fail "$(unparsed "$rel")"
  problems="$(jq -r --argjson b "$base_json" --arg mm "$mm" --arg base "$base" --arg btag "$base_tag" '
    def text: (strings | test("\\S")) // false;
    ($b.thresholds | map(.name)) as $names
    | if type != "object" then "it holds no release, base and overrides"
      else
        ((keys - ["release", "base", "base_server_tag", "overrides"])[] | "\(.) is not a key a release file holds; this script applies release, base, base_server_tag and overrides"),
        (if (.release | tostring) != $mm then "release is \(.release | tojson), not \"\($mm)\"" else empty end),
        (if .base != $base then "base is \(.base | tojson), not \($base)" else empty end),
        (if (.base_server_tag | tostring) != $btag
         then "written against base_server_tag \(.base_server_tag | tojson), and \($base) is now \($btag | tojson): check each override against the base as measured now"
         else empty end),
        (if (.overrides | type) != "array" then "overrides is not a list"
         else
           ([.overrides[] | objects | .name] | group_by(.) | map(select(length > 1) | .[0])[] | "\(tojson) is overridden twice"),
           (.overrides[] | if type != "object" then "an override is not a mapping" else
              (.name | tostring) as $n
              | (if any($names[]; . == $n) | not then "override \($n) names no threshold of \($base)" else empty end),
                (if .no_data != "not_judged" then "override \($n): no_data is \(.no_data | tojson), and the one kind is no_data: not_judged" else empty end),
                ((keys - ["name", "no_data", "reason", "evidence"])[] | "override \($n): \(.) is not a key this script applies"),
                (if .reason | text then empty else "override \($n) has no reason" end),
                (if .evidence | text then empty else "override \($n) has no evidence path" end)
            end)
         end)
      end' <<<"$rel_json")" || fail "$rel could not be checked"
  [[ -z "$problems" ]] || fail "$rel cannot be applied: $(awk 'NR > 1 { printf "; " } { printf "%s", $0 }' <<<"$problems")"
  n="$(jq '.overrides | length' <<<"$rel_json")"
  note="$desc, with $rel for server $tag: $n override$( (( n == 1 )) || echo s)"
fi

jq --argjson rel "$rel_json" --arg relfile "$rel" --arg base "$base" --arg btag "$base_tag" --arg tag "$tag" --arg mm "$mm" \
   --argjson given "$([[ -n "$given" ]] && echo true || echo false)" --arg note "$note" '
  def nul: if . == "" then null else . end;
  ($rel.overrides | map({key: .name, value: .}) | from_entries) as $o
  | .thresholds |= map(if $o[.name] then . + {no_data: $o[.name].no_data, override: ($o[.name] | {file: $relfile, reason, evidence})}
                       else . + {no_data: (.no_data // "exceeds")} end)
  | .resolved = {base: $base, base_server_tag: ($btag | nul), server_tag: ($tag | nul), release: ($mm | nul),
                 release_file: ($relfile | nul), given: $given, note: $note,
                 overrides: [$rel.overrides[] | {name, no_data, reason, evidence}], host: .host}' <<<"$base_json" \
  | yq -p=json -o=yaml '.'
