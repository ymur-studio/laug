package judge

import (
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// A classRow is one row of class_table: its pattern as the jq source has it, the class it gives, Go's matcher, and
// whether the row came with Arm. 27 of the 29 patterns are `^` and a literal prefix; `^recovered in [0-9]` and
// `^[0-9]+ failed iteration\(s\) ` get matchers of their own (match.go). TestClassTable holds each matcher to jq's test
// of its pattern.
type classRow struct {
	pattern string
	class   string
	match   func(string) bool
	arm     bool
}

// armRow is a row that v2 added with R7: v1's table, 26 rows, lacks it.
func armRow(r classRow) classRow {
	r.arm = true
	return r
}

// prefixRow is a row whose pattern is `^` and a literal: Oniguruma's `^` is the start of the string.
func prefixRow(prefix, class string) classRow {
	match := func(s string) bool { return strings.HasPrefix(s, prefix) }
	return classRow{pattern: "^" + prefix, class: class, match: match}
}

// classTable is `class_table`, v2's 29 rows in order: the first that matches gives the class. A version
// without Arm has the 26 that are not armRow, in the same order (classTableOf).
var classTable = []classRow{
	prefixRow("refused: ", "refused"),
	prefixRow("harness: ", "harness"),
	prefixRow("deadline: ", "deadline"),
	prefixRow("never steady: ", "invalid"),
	prefixRow("no signal reading before inject: ", "invalid"),
	prefixRow("inject failed: ", "invalid"),
	prefixRow("did not land: ", "invalid"),
	prefixRow("remove failed: ", "invalid"),
	prefixRow("stopped before the result ", "invalid"),
	prefixRow("load generator exited", "invalid"),
	prefixRow("sweep failed: ", "invalid"),
	prefixRow("no JSON: ", "invalid"),
	prefixRow("no exit record: ", "invalid"),
	prefixRow("no end time: ", "invalid"),
	prefixRow("partial JSON: ", "invalid"),
	prefixRow("contract: ", "invalid"),
	prefixRow("identity: ", "invalid"),
	armRow(prefixRow("incumbent: ", "invalid")),
	armRow(prefixRow("not compared: ", "invalid")),
	prefixRow("did not recover within ", "regression"),
	{pattern: `^recovered in [0-9]`, class: "regression", match: recoveredIn},
	prefixRow("after window not clean: ", "regression"),
	{pattern: `^[0-9]+ failed iteration\(s\) `, class: "regression", match: failedIterations},
	prefixRow("sweep found no workflows ", "regression"),
	prefixRow("sweep: ", "regression"),
	prefixRow("after window completed ", "regression"),
	prefixRow("load verdict BLOCK: ", "regression"),
	armRow(prefixRow("worse than the incumbent: ", "regression")),
	prefixRow("control: ", "control"),
}

// firstClause is `first_clause`: a string's text before the first "; ", "" for anything else.
//
//	def first_clause: if type == "string" then split("; ")[0] // "" else "" end;
func firstClause(v jsonx.Value) string {
	if v.Kind() != jsonx.KindString {
		return ""
	}
	if parts := split(v, "; "); len(parts) > 0 {
		return parts[0]
	}
	return "" // "" splits to no part: [][0] is null
}

// classTableOf is the class_table of a version with features f: v2's 29 rows with Arm, v1's 26 without.
func classTableOf(f Features) []classRow {
	if f.Arm {
		return classTable
	}
	return slices.DeleteFunc(slices.Clone(classTable), func(r classRow) bool { return r.arm })
}

// v1Table is v1's class_table, made once.
var v1Table = classTableOf(Features{})

// reasonClassIn is `reason_class` over the class table rows: the class of a reason by its first clause,
// unknown when no row matches. `laug judge -classify <reason>` prints it (Classify).
//
//	def reason_class: first_clause as $c
//	  | first(class_table[] | select(.[0] as $re | $c | test($re)) | .[1]) // "unknown";
func reasonClassIn(rows []classRow, v jsonx.Value) string {
	c := firstClause(v)
	for _, row := range rows {
		if row.match(c) {
			return row.class
		}
	}
	return "unknown"
}

// reasonClass is reason_class under the version being judged.
func (j *judging) reasonClass(v jsonx.Value) string {
	if j.f.Arm {
		return reasonClassIn(classTable, v)
	}
	return reasonClassIn(v1Table, v)
}

// ReasonClass is the class laug judge -classify prints for the reason r: reason_class of r as a string, under
// Current's table.
func ReasonClass(r string) string { return reasonClassIn(classTableOf(current.Features), str(r)) }

// classRank is `class_rank`: the order classes explain a BLOCK in, 9 for a name it does not know.
//
//	def class_rank: {refused: 0, harness: 1, deadline: 2, regression: 3, unknown: 4, control: 5, invalid: 6}[.] // 9;
func classRank(class string) int {
	if i := slices.Index(classRanks, class); i >= 0 {
		return i
	}
	return 9
}

var classRanks = []string{"refused", "harness", "deadline", "regression", "unknown", "control", "invalid"}

// The class, and the host.

// class0Of is $class0: the first, by class_rank, of the gate's refusal, the harness, every item's class,
// invalid for a plan, cut, directory, gate identity or control max problem, and control for a whole plan with no
// fault item; unknown when there is none.
func (j *judging) class0Of() string {
	var cands []string
	if !j.refused.IsNull() {
		cands = append(cands, "refused")
	}
	if len(j.r1) > 0 {
		cands = append(cands, "harness")
	}
	for _, it := range j.items {
		if c := field(it, "class"); truthy(c) { // .class // empty
			s, _ := c.Str()
			cands = append(cands, s)
		}
	}
	if len(j.planProblems)+len(j.cut)+len(j.extra)+len(j.g6) > 0 || !typeIs(j.ctlMax, "number") {
		cands = append(cands, "invalid")
	}
	if len(j.planProblems) == 0 && !j.anyFault() {
		cands = append(cands, "control")
	}
	slices.SortStableFunc(cands, func(a, b string) int { return classRank(a) - classRank(b) }) // sort_by(class_rank)
	if len(cands) == 0 {
		return "unknown" // .[0] // "unknown"
	}
	return cands[0]
}

// anyFault is `any($planned[]; .kind == "fault")`.
func (j *judging) anyFault() bool {
	return slices.ContainsFunc(j.planned, func(p jsonx.Value) bool { return is(field(p, "kind"), "fault") })
}

// noiseOf is $noise: why the host was not seen quiet. A raun that saw a busy cell at laugd, then the
// poller: no record, no poll, a busy cell, laugd or docker compose ls not answering, a compose project the gate did
// not lease.
func (j *judging) noiseOf() []string {
	var noise []string
	for _, it := range j.items {
		if !is(field(it, "kind"), "fault") {
			continue
		}
		k := field(it, "key") // .key as $k
		for _, e := range toEntries(alt(obj(field(it, "pool_busy")), object())) {
			if typeIs(e.value, "number") && more(e.value, zero) {
				noise = append(noise, tostring(k)+": laugd showed "+tostring(e.value)+" busy cell(s) in its "+
					tostring(e.key)+" window")
			}
		}
	}
	polls := field(j.in, "polls") // $in.polls as $polls
	switch {
	case !typeIs(polls, "array"):
		return append(noise, missing("poll.jsonl", polls)+", so the host was not seen quiet")
	case size(polls) == 0:
		return append(noise, "poll.jsonl holds no poll, so the host was not seen quiet")
	}
	all := each(polls)
	var busy []jsonx.Value // [$polls[] | obj | .busy? | num | select(. != null and . > 0)] as $busy
	noBusy, noProjects := 0, 0
	for _, p := range all {
		if b := num(field(obj(p), "busy")); !b.IsNull() && more(b, zero) {
			busy = append(busy, b)
		}
	}
	for _, p := range all { // ([$polls[] | obj // {} | .busy | select(type != "number")] | length) as $no_busy
		if !typeIs(field(alt(obj(p), object()), "busy"), "number") {
			noBusy++
		}
	}
	for _, p := range all { // ([... | .projects | select(type != "array")] | length) as $no_projects
		if !typeIs(field(alt(obj(p), object()), "projects"), "array") {
			noProjects++
		}
	}
	n := tostring(count(len(all)))
	if len(busy) > 0 {
		noise = append(noise, "the poller saw up to "+tostring(maxOf(busy))+" busy cell(s) at laugd, in "+
			tostring(count(len(busy)))+" of "+n+" polls")
	}
	if noBusy > 0 {
		noise = append(noise, "laugd did not answer "+tostring(count(noBusy))+" of "+n+" polls")
	}
	if noProjects > 0 {
		noise = append(noise, "docker compose ls failed in "+tostring(count(noProjects))+" of "+n+" polls")
	}
	var projects []jsonx.Value // [$polls[] | obj | .projects? | arrays | .[]]
	for _, p := range all {
		if ps := field(obj(p), "projects"); typeIs(ps, "array") {
			projects = append(projects, each(ps)...)
		}
	}
	for _, p := range unique(projects) {
		leased := slices.ContainsFunc(j.leased, func(l jsonx.Value) bool { return eq(l, p) }) // . as $p
		if !leased {
			noise = append(noise, "the poller saw compose project "+tostring(p)+" up, which the gate did not lease")
		}
	}
	return noise
}
