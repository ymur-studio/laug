package judge

import (
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Render is verdict.md: RENDER as version ver has it, run on v as `jq -r "$DEFS$RENDER"` runs it on the verdict.json
// the judge just wrote. v is that file read back (readBack), so every number is a literal of its printed text: d's
// 4.5e-05 is 0.000045 here, and fx works on that. Each output is one line: a string as it is, any other value in jq's
// default form, each with a newline. v1's RENDER has no comparison block and runs without DEFS, which only that block
// calls.
//
// An error is one jq raises. RENDER has no try, `?` or `//` that could catch one (`//` does not in jq 1.7), so the
// first stops jq, the judge exits 2, and neither file is written: which error comes first does not matter, only
// whether one comes. A verdict Evaluate built can raise one: a gate.json host that is a string reaches verdict.json
// when the set names no host, and a judging comparison then indexes it (TestJudgeFails).
func Render(ver Version, v jsonx.Value) (md []byte, err error) {
	defer catch(&err)
	var w lines
	w.render(ver.Features, v)
	return w.b, nil
}

// lines is what jq -r prints: each output and a newline.
type lines struct{ b []byte }

func (w *lines) line(s string) { w.b = append(append(w.b, s...), '\n') }

// out prints one output of the program: a string raw, any other value as jq's default form.
func (w *lines) out(v jsonx.Value) {
	if s, ok := v.Str(); ok {
		w.line(s)
		return
	}
	w.b = append(w.b, jsonx.Marshal(v)...)
}

// catch turns a jq error raised below a deferred catch into err, as Evaluate does. Any other panic goes on.
func catch(err *error) {
	if r := recover(); r != nil {
		e, ok := r.(*jqError)
		if !ok {
			panic(r)
		}
		*err = e
	}
}

// cell is `def cell`: tostring, each | as \| and each newline as a space, so a value stays in its table cell.
// Both gsub patterns are one literal character, which Oniguruma and strings.ReplaceAll match alike.
//
//	def cell: tostring | gsub("\\|"; "\\|") | gsub("\n"; " ");
func cell(v jsonx.Value) string {
	return strings.ReplaceAll(strings.ReplaceAll(tostring(v), "|", `\|`), "\n", " ")
}

// render runs RENDER's outputs in order, each named by what it prints.
func (w *lines) render(f Features, v jsonx.Value) {
	// The heading, its commit, and whether the tree was dirty or the gate fake.
	h := "## laug · verdict for raun " + tostring(alt(field(v, "laug_commit"), str("unknown")))
	if eq(field(v, "tree_dirty"), jsonx.Bool(true)) {
		h += " (tree dirty)"
	}
	if eq(field(v, "fake"), jsonx.Bool(true)) {
		h += " (GATE_FAKE=1: fixtures, no cell ran)"
	}
	w.line(h)
	w.line("")

	// The run, its items, ITEMS, and the time from the gate start. The item count is computed (length), so it prints in
	// dtoa form; the elapsed times are the literals verdict.json holds, so 485.00 written as 485 reads 485.
	items := field(v, "items")
	g := "Gate run " + tostring(alt(field(v, "gate_id"), str("unknown"))) + " on server " +
		tostring(alt(field(v, "server_tag"), str("unknown"))) + ": " + tostring(length(items))
	if inPlan := field(v, "items_in_plan"); typeIs(inPlan, "number") && !eq(inPlan, length(items)) {
		g += " of " + tostring(inPlan) + " planned"
	}
	g += " item(s)"
	if limit := field(v, "items_limit"); !limit.IsNull() {
		g += " (ITEMS=" + tostring(limit) + ")"
	}
	if el := field(v, "elapsed_s"); !el.IsNull() {
		g += ", " + tostring(field(el, "wall")) + " s from the gate start to this verdict on the wall clock (" +
			tostring(field(el, "mono")) + " s monotonic)."
	} else {
		g += "."
	}
	w.line(g)
	w.line("")

	// The refusal and a blank line, when there is one.
	if refused := field(v, "refused"); !refused.IsNull() {
		w.line("Refused before the wave: " + tostring(refused) + ".")
		w.line("")
	}

	// The item table.
	w.line("| Item | Expect | Observed | Class | Reason |")
	w.line("| --- | --- | --- | --- | --- |")
	for _, it := range each(items) {
		w.line("| " + cell(field(it, "key")) + " | " + cell(field(it, "expect")) + " | " +
			cell(alt(field(it, "observed"), str("none"))) + " | " + cell(alt(field(it, "class"), str(""))) + " | " +
			cell(field(it, "reason")) + " |")
	}
	w.line("")

	// R7's mode, its notes and the comparison table, when there is a comparison. v1 reads no comparison.
	if c := field(v, "comparison"); f.Arm && !c.IsNull() {
		w.comparison(f, v, c)
	}

	// The rule table, each failing rule's reasons joined by "; ".
	w.line("| Rule | Result |")
	w.line("| --- | --- |")
	for _, e := range toEntries(field(v, "rules")) {
		result := "pass"
		if !truthy(field(e.value, "pass")) {
			result = cell(str(join(field(e.value, "reasons"), "; ")))
		}
		w.line("| " + tostring(e.key) + " " + tostring(field(e.value, "name")) + " | " + result + " |")
	}
	w.line("")

	// The host.
	if truthy(field(v, "host_quiet")) {
		w.line("Host quiet: yes.")
	} else {
		q := "Host quiet: no. " + join(field(v, "host_noise"), "; ") + "."
		if truthy(field(v, "rerun")) {
			q += " A BLOCK on a host that was not quiet is class invalid: rerun (class on a quiet host: " +
				tostring(field(v, "class_if_quiet")) + ")."
		}
		w.line(q)
	}
	w.line("")

	// The class of a BLOCK, and a blank line.
	if is(field(v, "verdict"), "BLOCK") {
		w.line("Class: " + tostring(field(v, "class")) + ".")
		w.line("")
	}
	w.line("**" + tostring(field(v, "verdict")) + "**") // the last line
}

// comparison is RENDER's R7 block, `.comparison as $c | .server_tag as $tag | .incumbent as $inc`: the
// mode line, each note, the comparison table and a blank line. `.` stays the verdict v, so the judging line names
// v's host type and v's item keys, not the comparison's wave. The table's cells do not pass through cell. f is the
// version's, for fx (ZeroFix).
func (w *lines) comparison(f Features, v, c jsonx.Value) {
	tag, inc := field(v, "server_tag"), field(v, "incumbent")
	if is(field(c, "mode"), "recording") {
		w.line("R7: recorded, not judged: no tolerance in load/steady-state.yaml.")
	} else {
		var ks []jsonx.Value // [.items[].key]
		for _, it := range each(field(v, "items")) {
			ks = append(ks, field(it, "key"))
		}
		w.line("Tolerances from " + tostring(field(c, "evidence")) + ": " + tostring(field(c, "gates")) +
			" A/A gates of " + tostring(field(c, "measured_with")) + " on " +
			tostring(alt(path(v, "host", "type"), str("a host of no recorded type"))) + ", the wave " +
			join(jsonx.Arr(ks), ", ") + ".")
	}
	for _, n := range each(field(c, "notes")) { // ($c.notes[]): each note is its own output
		w.out(n)
	}
	w.line("")
	w.line("| Reading | " + tostring(field(c, "release")) + " (" + tostring(tag) + ") | " +
		tostring(field(c, "incumbent")) + " (" + tostring(inc) + ") | d | Tolerance | Result |")
	w.line("| --- | --- | --- | --- | --- | --- |")
	for _, r := range each(field(c, "rows")) { // $c.rows[] | . as $r
		scale := field(r, "scale")
		w.line("| " + tostring(field(r, "name")) + " | " + mval(f, field(r, "release"), scale) + " | " +
			mval(f, field(r, "incumbent"), scale) + " | " + dval(f, field(r, "d"), scale) + " | " +
			tval(f, field(r, "tolerance"), scale) + " | " + tostring(field(r, "result")) + " |")
	}
	w.line("")
}
