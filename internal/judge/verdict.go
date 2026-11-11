package judge

import (
	"github.com/ymur-studio/laug/internal/jsonx"
)

// verdict.json is the object JUDGE builds last, in that order, as `jq "$DEFS$JUDGE"` prints it with no flag: jq's
// default form, which jsonx.Marshal writes. A number in it has one of two forms, and Evaluate keeps each as jq does,
// field by field:
//
//   - A literal keeps its text: deadline_s, items_in_plan and items_limit, t0 and t_post, host, each item's block,
//     exit and pool_busy, and the comparison's gates, length_s and each row's release, incumbent and tolerance.
//     jq 1.7 prints a literal in decNumber's canonical form, so 1792076002.490 keeps its last zero.
//   - A computed number prints in jq's dtoa form, shortest first: elapsed_s (r3), start_apart_s (an abs of two
//     epochs) and each row's d (rd), so a d of 0.000045 prints 4.5e-05, and a d that rounds to 0 from below -0.
//
// verdict.md and the summary are rendered from these bytes read back, not from the object
// Evaluate built (readBack). Read back, every number is a literal of its printed text: a d written 4.5e-05 is the
// literal 0.000045, and a count's d written 1e+16 is the literal 1E+16, which verdict.md prints so
// (TestJudgeReadsBack).

// output is the verdict object, in verdict.json's key order.
func (j *judging) output() jsonx.Value {
	class, ifQuiet := str(j.class0), null
	switch {
	case j.verdict == "ADVANCE":
		class = null
	case j.rerun:
		class, ifQuiet = str("invalid"), str(j.class0)
	}
	elapsed := j.elapsedOf()
	var reasons []string
	if !j.refused.IsNull() {
		reasons = append(reasons, "refused: "+tostring(j.refused))
	}
	for _, e := range toEntries(j.rules) {
		for _, x := range each(field(e.value, "reasons")) {
			reasons = append(reasons, tostring(e.key)+" "+tostring(x)) // .key as $r
		}
	}
	items := make([]jsonx.Value, len(j.items))
	for i, it := range j.items {
		items[i] = del(it, "_r")
	}
	// v1's object has no incumbent and no comparison.
	out := []any{
		"gate_id", j.gid, "verdict", str(j.verdict),
		"class", class, "rerun", jsonx.Bool(j.rerun), "class_if_quiet", ifQuiet, "refused", j.refused,
		"fake", jsonx.Bool(eq(field(j.gate, "fake"), jsonx.Bool(true))),
		"server_tag", j.tag,
	}
	if j.f.Arm {
		out = append(out, "incumbent", j.inc)
	}
	out = append(out, "laug_commit", alt(field(j.gate, "laug_commit"), null),
		"tree_dirty", field(j.gate, "tree_dirty"), "base", alt(field(j.gate, "base"), null),
		"inputs", alt(field(j.gate, "inputs"), null), "deadline_s", alt(field(j.plan, "deadline_s"), null),
		"items_in_plan", alt(field(j.plan, "items_in_plan"), null),
		"items_limit", alt(field(j.gate, "items_limit"), null),
		"t0", alt(field(j.clock, "t0"), null), "t_post", alt(field(j.clock, "t_post"), null),
		"elapsed_s", elapsed,
		"host", alt(field(j.gate, "host"), null),
		"rules", j.rules,
		"reasons", strs(reasons),
		"items", jsonx.Arr(items),
	)
	if j.f.Arm {
		out = append(out, "comparison", j.arm7.comparison)
	}
	return object(append(out, "host_quiet", jsonx.Bool(j.quiet), "host_noise", strs(j.noise))...)
}

// elapsedOf is elapsed_s: `{wall, mono}` of t_post less t0, each r3, when all four are numbers. A t0 or a
// t_post that is not an object or null cannot be indexed: jq's error, in the order the `and`s reach them.
func (j *judging) elapsedOf() jsonx.Value {
	if typeIs(path(j.clock, "t0", "wall"), "number") && typeIs(path(j.clock, "t_post", "wall"), "number") &&
		typeIs(path(j.clock, "t0", "mono"), "number") && typeIs(path(j.clock, "t_post", "mono"), "number") {
		return object(
			"wall", r3(minus(path(j.clock, "t_post", "wall"), path(j.clock, "t0", "wall"))),
			"mono", r3(minus(path(j.clock, "t_post", "mono"), path(j.clock, "t0", "mono"))),
		)
	}
	return null
}

// readBack is verdict.json's bytes as the next jq reads them: RENDER, SUMMARY and `jq -r .verdict`
// each read the file the judge wrote. jsonx reads every number it wrote back as a literal of that text, as jq does.
// Marshal's output always parses, so an error here is a fault in jsonx.
func readBack(js []byte) jsonx.Value {
	v, err := jsonx.Parse(js)
	if err != nil {
		panic("internal/judge: verdict.json does not read back: " + err.Error())
	}
	return v
}
