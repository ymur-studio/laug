#!/usr/bin/env bash
# _obs/lint.sh: the board rules of make lint, which hold _obs/README.md to its word (a board judges nothing,
# draws no typed max, and reads the yaml's query with one selector added), with jq and yq only: no binary, no Docker,
# seconds. Run by make lint and make obs-test, from the repository's root; each line it prints starts "laug · make
# lint: ". It stays out of make harness-test on purpose: harness-test is R1 of every gate, so a board edit there would
# BLOCK gates, the stack judging them.
#
#   _obs/lint.sh [--cell-board <file>] [<board.json> ...]
#
# The boards are _obs/stack/*.json unless named; the cells' board is
# dashboards/provisioning/dashboards/boards/gate.json unless named. One line a broken rule, then exit 2; exit 0 when
# every rule holds. What each rule reads:
#   1  Every query on a cell family names a cell or a gate (cell= or gate_id=, to a variable or to literal ids), the
#      portable scope ($scope_label=) or a laug job (job="laug-..."). A cell or gate matcher whose value is a regex
#      (cell=~".*") or a variable whose All value matches the empty string does not count: such a matcher also selects
#      the series with no cell label, the dev server's among them. The cell families are the federation list in
#      _obs/prometheus.yml, each histogram's _count and _sum, and the SDK's temporal_* names, written as a name or
#      inside {__name__=...}. The dev server serves the same names as a cell, so an unscoped sum would add it to a
#      cell's reading. Panel and annotation queries; not variable queries.
#   2  A query whose refId names a row of load/steady-state.yaml (the six and the five shares) or of
#      faults/catalog.yaml (its signal) is that row's string with exactly one label matcher added: the lint takes the
#      matcher out of every selector, turns [$__range] back into [1m] (the catalog's into [30s]), and compares strings.
#      So is a query under any other refId that reads like a row, one series and no per-item grouping: one with a
#      row's quantile, families and matchers, or one with a row's families in a panel that draws that row's
#      laug_threshold or laug_item_reading. It may also be the row's string over the catalog's [30s]. A rolling line
#      groups by item, cell or arm, and the dev server's (job="laug-orchestrator") is no row.
#   3  No typed max: a reading panel carries no threshold step with a number, no max of its own (fieldConfig's max, in
#      its defaults or an override, which ends a bar gauge's bar or a graph's axis) and no constant query (23, vector(23)).
#      A reading panel is one with a refId of the six, laug_threshold, laug_item_reading of one of the six, or a query
#      with one of the six's quantile, read as a number, and families (histogram_quantile(0.99, ...
#      persistence_latency_bucket ...)). Its max line is laug_threshold.
#   4  ADVANCE and BLOCK appear only in panels that read laug_gate_verdict or laug_gate_rule, and nowhere else; the
#      cells' board reads no verdict.json, so it says neither.
#   5  No `or vector(...)`, nor `or on(...) vector(...)`, in a panel where a query groups by labels other than le: it
#      adds an unlabelled series beside the labelled ones.
#   6  Every histogram_quantile has a `by (..., le)`, and cell, item or arm in it when a selector can hold more than
#      one cell: one that pins no single cell (cell or the portable scope), nor one gate and in it one item or arm.
#      job="laug-orchestrator" is one server.
#   7  An annotation that reads a *_seconds value, or uses its value as the time, multiplies by 1000: Grafana reads
#      that value as milliseconds, and seconds draw in January 1970.
#   8  Nulls are never connected: every timeseries panel says spanNulls false, and nothing sets null as zero
#      (nullPointMode, nullValueMode, a number as noValue).
#   9  Every datasource uid exists (_obs/grafana/provisioning/datasources/; the cells' board, the cells'), a variable or
#      Grafana's own; timezone utc; every panel type a React one of Grafana 11.1.
#   10 The cells' board draws each of the six with load/steady-state.yaml's query, unchanged, and its max as the only
#      threshold step, and its timezone is utc.
# The review rules (11 to 15) are read by a person.
set -uo pipefail

cell_board=dashboards/provisioning/dashboards/boards/gate.json
if [[ "${1:-}" == --cell-board ]]; then cell_board="${2:?--cell-board needs a file}"; shift 2; fi
boards=("$@")
if (( ${#boards[@]} == 0 )); then shopt -s nullglob; boards=(_obs/stack/*.json); shopt -u nullglob; fi
die() { echo "laug · make lint: $*" >&2; exit 2; }

# The context every board is read against, built from the files the rules name.
thresholds="$(yq -o=json -I=0 '.thresholds' load/steady-state.yaml)" || die "load/steady-state.yaml does not read"
catalog="$(yq -o=json -I=0 '{"range": (.defaults.signal_range // "30s"), "rows": (.faults | to_entries | map({"name": .key, "signal": (.value.signal // null), "query": (.value.signal_query // null), "range": (.value.signal_range // null)}))}' faults/catalog.yaml)" \
  || die "faults/catalog.yaml does not read"
match="$(yq -r '.scrape_configs[] | select(.job_name == "laug-cells") | .params["match[]"][0]' _obs/prometheus.yml)" || die "_obs/prometheus.yml does not read"
[[ "$match" =~ ^\{__name__=~\"([a-z_|]+)\"\}$ ]] || die "_obs/prometheus.yml: laug-cells' first match[] is not {__name__=~\"a|b|...\"}"
families="${BASH_REMATCH[1]}"
stack_uids="$(yq -o=json -I=0 '[.datasources[].uid]' _obs/grafana/provisioning/datasources/stack.yml)" || die "the stack's datasources do not read"
cell_uids="$(for f in dashboards/provisioning/datasources/*.yml; do yq -o=json -I=0 '[.datasources[].uid]' "$f"; done | jq -sc 'add')" \
  || die "the cells' datasources do not read"
ctx="$(jq -nc --argjson t "$thresholds" --argjson c "$catalog" --arg f "$families" --argjson su "$stack_uids" --argjson cu "$cell_uids" '
  ($f | split("|")) as $fams
  | (($fams + [$fams[] | select(endswith("_bucket")) | (sub("_bucket$"; "_count"), sub("_bucket$"; "_sum"))]) | unique) as $all
  | {readings: $t,
     six: [$t[] | select(.max != null)],
     range: $c.range,
     signals: [$c.rows[] | (.range // $c.range) as $r
               | if .query != null then {name, query, range: $r}
                 elif .signal != null then .signal as $s | {name, query: ($t[] | select(.name == $s) | .query | gsub("\\[1m\\]"; "[" + $r + "]")), range: $r}
                 else empty end],
     fams: $all,
     famre: (($all | sort_by(-length) | join("|")) + "|temporal_[a-z0-9_]+"),
     stack_uids: $su, cell_uids: $cu}')" || die "the rules' context could not be built"

read -r -d '' LINT_JQ <<'JQ'
def esc: gsub("(?<c>[.*+?^${}()|\\[\\]\\\\/])"; "\\\(.c)");
# A selector's braces: quoted values may hold } (a ${var}).
def brace_re: "\\{((?:[^{}\"]|\"(?:[^\"\\\\]|\\\\.)*\")*)\\}";
def matcher_re: "[A-Za-z_$][A-Za-z0-9_${}]*\\s*(?:=~|!~|!=|=)\\s*\"(?:[^\"\\\\]|\\\\.)*\"";
def matchers: [scan(matcher_re) | capture("^(?<l>[A-Za-z_$][A-Za-z0-9_${}]*)\\s*(?<op>=~|!~|!=|=)\\s*\"(?<v>.*)\"$") + {s: .}];
def selectors($famre): [scan("(?<![A-Za-z0-9_:\"$])(" + $famre + ")(?![A-Za-z0-9_:\"(])(" + brace_re + ")?") | {name: .[0], body: (.[2] // "")}];
# A cell family written inside the braces, {__name__="service_requests"} or {__name__=~"service_.*"}.
def name_selectors($ctx):
  [scan(brace_re)[0] as $body | ($body | matchers)[] | select(.l == "__name__")
   | select((.op == "=" and ((.v | IN($ctx.fams[])) or (.v | startswith("temporal_"))))
            or (.op == "=~" and (.v as $re | (.v | startswith("temporal_")) or (try any($ctx.fams[]; test("^(?:" + $re + ")$")) catch true))))
   | {name: .v, body: $body}];
def cell_selectors($ctx): selectors($ctx.famre) + name_selectors($ctx);
def varname: (capture("^\\$\\{?(?<n>[A-Za-z_][A-Za-z0-9_]*)(?::[A-Za-z]+)?\\}?$") | .n) // null;
# A cell or gate matcher's value that names cells or gates and nothing else: a variable whose All value cannot match
# the empty string, a literal for =, literal ids joined by | for =~. Never a regex such as .*, which also matches a
# series with no such label.
def okval($tv):
  (.v | varname) as $n
  | if $n != null then ((($tv[$n].inc // false) and ($tv[$n].all != null)
                         and (try ("" | test("^(?:" + $tv[$n].all + ")$")) catch true)) | not)
    elif .op == "=" then .v != ""
    elif .op == "=~" then (.v | test("^[A-Za-z0-9_-]+(?:\\|[A-Za-z0-9_-]+)*$"))
    else false end;
def scoped($tv):
  matchers | any(.[];
    ((.l | IN("cell", "gate_id", "$scope_label", "${scope_label}")) and (.op | IN("=", "=~")) and okval($tv))
    or (.l == "job" and ((.op == "=" and (.v | test("^laug-[a-z]+$")))
                         or (.op == "=~" and (.v | test("^laug-[a-z]+(?:\\|laug-[a-z]+)*$"))
                             and (.v == "laug-orchestrator" or (any(.v | split("|")[]; . == "laug-orchestrator") | not))))));
def strip($m): ($m | esc) as $e
  | gsub("\\{\\s*" + $e + "\\s*\\}"; "") | gsub("\\{\\s*" + $e + "\\s*,\\s*"; "{") | gsub(",\\s*" + $e + "\\s*\\}"; "}") | gsub(",\\s*" + $e + "(?=\\s*,)"; "");
# What a query computes, read from its text: its quantiles as numbers, its groupings, its matchers.
def qnums: [scan("histogram_quantile\\s*\\(\\s*([0-9]*\\.?[0-9]+(?:[eE][-+]?[0-9]+)?)\\s*,")[0] | tonumber];
def bylists: [scan("\\b(by|without)\\s*\\(([^)]*)\\)") | {k: .[0], ls: (.[1] | gsub("\\s"; "") | split(",") | map(select(. != "")))}];
def grouped: any(bylists[]; .k == "without" or ((.ls - ["le"]) | length > 0));
def mset: [scan(brace_re)[0] | matchers[] | "\(.l)\(.op)\"\(.v)\""] | unique;
def orch: any(mset[]; . == "job=\"laug-orchestrator\"");
# Rule 2's test: is this query a source's string (rows at [1m], or the catalog's range; signals at theirs) with
# exactly one matcher added, once [$__range] is turned back into the source's range?
def one_added($cands): . as $x | any($cands[]; . as $c
  | ($x | gsub("\\[\\$__range\\]"; "[" + $c.r + "]")) as $n
  | any(($n | [scan(brace_re)[0] | matchers[] | .s] | unique)[]; . as $m | ($n | strip($m)) == $c.q));
# Rule 6's question: can this query's selectors hold more than one cell? Not one that pins one cell (cell or the
# portable scope), or one gate and in it one item or arm, each to a single value; not job="laug-orchestrator".
def pin1($tv; $rep): (.op == "=" and .v != "")
  or (.op == "=~" and (((.v | varname) as $n | $n != null and ((($tv[$n].many // false) | not) or any($rep[]; . == $n)))
                       or (.v | test("^[A-Za-z0-9_-]+$"))));
def one_cell($tv; $rep): matchers as $ms
  | any($ms[]; .l == "job" and .op == "=" and .v == "laug-orchestrator")
    or any($ms[]; (.l | IN("cell", "$scope_label", "${scope_label}")) and pin1($tv; $rep))
    or (any($ms[]; .l == "gate_id" and pin1($tv; $rep)) and any($ms[]; (.l | IN("item", "arm")) and pin1($tv; $rep)));
def multi($ctx; $tv; $rep): any(cell_selectors($ctx)[]; .body | one_cell($tv; $rep) | not);

def walk_panels:
  reduce (.panels // [])[] as $p ({row: null, out: []};
    if $p.type == "row" then .row = $p | .out += [{p: $p, row: null}] | .out += [($p.panels // [])[] | {p: ., row: $p}]
    else .out += [{p: $p, row: .row}] end) | .out;
def targets: [(.p.targets // [])[] | select(.expr? | type == "string")];

def lint($ctx; $file; $kind):
  . as $b
  | ([(.templating.list // [])[] | {key: .name, value: {many: ((.multi // false) or (.includeAll // false)), inc: (.includeAll // false),
                                                       all: ((.allValue // "") | if . == "" then null else . end)}}] | from_entries) as $tv
  | ($ctx.six | map({name, q: ((.query | qnums)[0] // null), fams: [.query | selectors($ctx.famre)[] | .name] | unique})) as $sigs
  | ($ctx.readings | map({name, query, q: (.query | qnums), fams: ([.query | selectors($ctx.famre)[] | .name] | unique), ms: (.query | mset)})) as $rows
  | ([$ctx.readings[] | {q: .query, r: "1m"}, {q: (.query | gsub("\\[1m\\]"; "[" + $ctx.range + "]")), r: $ctx.range}]
     + [$ctx.signals[] | {q: .query, r: .range}]) as $cands
  | ([$ctx.readings[].name] + [$ctx.signals[].name]) as $named
  | ($ctx.six | map(.name)) as $sixnames
  | walk_panels as $ps
  | def fail($n; $what): "\($file): rule \($n): \($what)";
    def pfail($e; $n; $what): fail($n; "panel \($e.p.id // "?") \"\($e.p.title // "")\": \($what)");
    def reading_panel: . as $e | any(targets[];
        (.refId | IN($sixnames[])) or (.expr | test("\\blaug_threshold\\b"))
        or (.expr | test("\\blaug_item_reading\\{[^}]*reading=\"(" + ($sixnames | join("|")) + ")\""))
        or (.expr as $x | ($x | qnums) as $xq | any($sigs[]; . as $s | ($s.q == null or any($xq[]; . == $s.q)) and ($s.fams | length > 0)
              and all($s.fams[]; . as $f | $x | test("(?<![A-Za-z0-9_:])" + $f + "(?![A-Za-z0-9_:])")))));
  [
    # rule 9: timezone, panel types, datasource uids
    (if .timezone != "utc" then fail(if $kind == "cell" then 10 else 9 end; "timezone is \(.timezone // "unset"), not utc") else empty end),
    ($ps[] | . as $e | select(.p.type | IN("timeseries", "stat", "gauge", "bargauge", "table", "text", "state-timeline", "status-history",
        "barchart", "histogram", "heatmap", "piechart", "logs", "nodeGraph", "traces", "row", "dashlist", "annolist", "xychart", "trend",
        "canvas", "flamegraph", "news", "datagrid") | not) | pfail($e; 9; "type \(.p.type // "unset") is not a React panel of Grafana 11.1")),
    ([.. | objects | select(has("datasource")) | .datasource | if type == "object" then .uid elif type == "string" then . else null end
       | select(. != null)] | unique[]
     | select((IN((if $kind == "cell" then $ctx.cell_uids else $ctx.stack_uids end)[]) or startswith("$")
               or IN("-- Grafana --", "grafana", "-- Mixed --", "-- Dashboard --")) | not)
     | fail(9; "datasource uid \(.) is not provisioned")),
    if $kind == "cell" then
      # rule 4: the cells' board reads no verdict.json, so no word of it says ADVANCE or BLOCK
      (if tostring | test("ADVANCE|BLOCK") then fail(4; "the cells' board says ADVANCE or BLOCK; it reads no verdict.json") else empty end),
      # rule 10: the cells' board against load/steady-state.yaml
      ($ctx.six[] as $s
       | [$ps[] | select(any(targets[]; .expr == $s.query))] as $hits
       | if ($hits | length) == 0 then fail(10; "no panel draws \($s.name) with load/steady-state.yaml's query")
         else $hits[] | . as $e | [(.p.fieldConfig.defaults.thresholds.steps // [])[] | .value | numbers] as $v
              | select($v != [$s.max]) | pfail($e; 10; "draws \($v | map(tostring) | join(", ")) for \($s.name); load/steady-state.yaml's max is \($s.max)")
         end)
    else
      # rule 4: the board outside its panels
      (if (del(.panels) | tostring | test("ADVANCE|BLOCK")) then fail(4; "ADVANCE or BLOCK outside a panel that reads laug_gate_verdict") else empty end),
      ($ps[] | . as $e | (([.row.repeat // empty] + [.p.repeat // empty])) as $rep
       | (any(targets[]; .expr | grouped)) as $pgrouped
       | ([targets[] | .expr as $x | $rows[] | select(.name as $n | $x | test("\\blaug_(?:threshold|item_reading)\\{[^}]*reading=\"" + ($n | esc) + "\""))]
          | unique_by(.name)) as $tied
       | (
         # rule 4
         (if (.p | del(.panels) | tostring | test("ADVANCE|BLOCK")) and (any(targets[]; .expr | test("\\blaug_gate_(verdict|rule)\\b")) | not)
          then pfail($e; 4; "says ADVANCE or BLOCK and reads neither laug_gate_verdict nor laug_gate_rule") else empty end),
         # rule 3
         (if reading_panel then
            ([(.p.fieldConfig.defaults.thresholds.steps // [])[] | .value | numbers]
             + [(.p.fieldConfig.overrides // [])[] | (.properties // [])[] | select(.id == "thresholds") | (.value.steps // [])[] | .value | numbers]) as $typed
            | ([.p.fieldConfig.defaults.max // empty | select(type != "null")]
               + [(.p.fieldConfig.overrides // [])[] | (.properties // [])[] | select(.id == "max") | .value | select(type != "null")]) as $fmax
            | ([targets[] | select(.expr | test("^\\s*\\(*\\s*(?:vector\\s*\\(\\s*)?[-+]?[0-9]*\\.?[0-9]+(?:[eE][-+]?[0-9]+)?\\s*\\)*\\s*$")) | "\(.refId // "?") \(.expr)"]) as $consts
            | (if ($typed | length) > 0 then pfail($e; 3; "a reading panel with a typed max: threshold step \($typed | map(tostring) | join(", ")); draw laug_threshold instead") else empty end),
              (if ($fmax | length) > 0 then pfail($e; 3; "a reading panel with a typed max: the field's max \($fmax | map(tostring) | join(", ")), the bar's or the axis's end; take it from laug_threshold (config from query results) instead") else empty end),
              (if ($consts | length) > 0 then pfail($e; 3; "a reading panel with a constant query, a typed max: \($consts | join("; ")); draw laug_threshold instead") else empty end)
          else empty end),
         # rule 8
         (if ([.p | .. | objects | select(has("spanNulls")) | .spanNulls] + [(.p.fieldConfig.overrides // [])[] | (.properties // [])[] | select(.id == "custom.spanNulls") | .value]) | any(. != false)
          then pfail($e; 8; "connects nulls (spanNulls not false)") else empty end),
         (if .p.type == "timeseries" and .p.fieldConfig.defaults.custom.spanNulls != false then pfail($e; 8; "a timeseries panel without spanNulls false") else empty end),
         (if [.p | .. | objects | select(has("nullPointMode") or has("nullValueMode"))] | length > 0 then pfail($e; 8; "sets a null mode") else empty end),
         (if ([.p | .. | objects | select(has("noValue")) | .noValue] + [(.p.fieldConfig.overrides // [])[] | (.properties // [])[] | select(.id == "noValue") | .value])
             | any(tostring | test("^\\s*-?[0-9.]+\\s*$")) then pfail($e; 8; "shows a number when there is no value") else empty end),
         (targets[] | . as $t | .expr as $x
          | (
            # rule 1
            ($x | cell_selectors($ctx)[] | select(.body | scoped($tv) | not)
             | pfail($e; 1; "\(.name) names no cell, gate, scope or laug job in \($t.refId // "?") (a regex such as .* or an All of .* also selects the series with no cell)")),
            # rule 2, a query named by its row
            (first(($ctx.readings[] | select(.name == $t.refId) | {q: .query, r: "1m", src: "load/steady-state.yaml"}),
                   ($ctx.signals[] | select(.name == $t.refId) | {q: .query, r: .range, src: "faults/catalog.yaml"})) // null
             | select(. != null) | . as $s
             | ($x | gsub("\\[\\$__range\\]"; "[" + $s.r + "]")) as $n
             | select(any(($n | [scan(brace_re)[0] | matchers[] | .s] | unique)[]; . as $m | ($n | strip($m)) == $s.q) | not)
             | pfail($e; 2; "\($t.refId) is not \($s.src)'s query with exactly one matcher added: \($x)")),
            # rule 2, a query that reads like a row under another refId
            (select(($t.refId // "") | IN($named[]) | not)
             | select(($x | grouped | not) and ($x | orch | not))
             | ([$x | selectors($ctx.famre)[] | .name] | unique) as $xf
             | ($x | qnums) as $xq | ($x | mset) as $xm
             | ([$rows[] | select(.q == $xq and ((.fams - $xf) | length == 0) and (.fams | length > 0) and ((.ms - $xm) | length == 0)) | .name]
                + [$tied[] | select(any(.fams[]; IN($xf[]))) | .name] | unique) as $like
             | select(($like | length) > 0 and ($x | one_added($cands) | not))
             | pfail($e; 2; "\($t.refId // "?") reads like \($like | join(", ")), one series with no per-item grouping, but is not a source's query with exactly one matcher added: \($x); give it the row's name as its refId, or group a rolling line by item, cell or arm")),
            # rule 5
            (select(($x | test("\\bor\\b(?:\\s+(?:on|ignoring)\\s*\\([^)]*\\))?\\s*\\(*\\s*vector\\s*\\(")) and $pgrouped)
             | pfail($e; 5; "or vector(...) in a panel grouped by labels other than le: \($x)")),
            # rule 6
            (select($x | test("histogram_quantile\\s*\\("))
             | [$x | scan("\\bby\\s*\\(([^)]*)\\)")[0] | gsub("\\s"; "") | split(",")] as $by
             | [$by[] | select(index("le"))] as $le
             | if ($le | length) == 0 then pfail($e; 6; "a quantile without sum by (..., le): \($x)")
               elif ($x | multi($ctx; $tv; $rep)) and any($le[]; (index("cell") or index("item") or index("arm")) | not)
               then pfail($e; 6; "a quantile over more than one cell without cell, item or arm in its by (...): \($x)")
               else empty end)
          ))
       )),
      # rule 7, and rule 1 over annotations
      ((.annotations.list // [])[] | . as $a | (.expr // .target.expr // null) as $x | select($x | type == "string")
       | ((select((($a.useValueForTime // $a.target.useValueForTime // false) | . == true or . == "on")
                  or ($x | test("[a-z_]+_seconds(?![A-Za-z0-9_:])")))
           | select($x | test("\\*\\s*1000\\b|\\b1000\\s*\\*") | not)
           | fail(7; "annotation \"\($a.name // "")\" reads seconds without * 1000: \($x)")),
          ($x | cell_selectors($ctx)[] | select(.body | scoped($tv) | not) | fail(1; "annotation \"\($a.name // "")\": \(.name) names no cell, gate, scope or laug job"))))
    end
  ] as $fails
  | {fails: $fails, panels: ([$ps[] | select(.p.type != "row")] | length),
     queries: (([$ps[] | targets[]] | length) + ([($b.annotations.list // [])[] | select((.expr // .target.expr // null) != null)] | length))};
lint($ctx; $file; $kind)
JQ

fails=0; nb=0; np=0; nq=0
run() { # run <file> <kind>
  local out
  out="$(jq -c --argjson ctx "$ctx" --arg file "$1" --arg kind "$2" "$LINT_JQ" "$1" 2>&1)" \
    || { echo "laug · make lint: $1: does not read as a board: $(head -1 <<<"$out")" >&2; fails=$((fails + 1)); return; }
  jq -r '.fails[] | "laug · make lint: " + .' <<<"$out" >&2
  fails=$((fails + $(jq '.fails | length' <<<"$out")))
  [[ "$2" == stack ]] && { nb=$((nb + 1)); np=$((np + $(jq .panels <<<"$out"))); nq=$((nq + $(jq .queries <<<"$out"))); }
  [[ "$2" == cell ]] && cp_="$(jq .panels <<<"$out")"
}
cp_=0
for b in "${boards[@]}"; do run "$b" stack; done
run "$cell_board" cell
if (( fails > 0 )); then echo "laug · make lint: $fails rule(s) broken" >&2; exit 2; fi
if (( nb == 0 )); then what="no stack board yet"; else what="$nb stack board(s), $np panels, $nq queries"; fi
echo "laug · make lint: $what; the cells' board, $cp_ panels, draws load/steady-state.yaml's six maxima and no verdict word; rules 1 to 10 hold"
