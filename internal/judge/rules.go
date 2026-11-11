package judge

import (
	"slices"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Evaluate is JUDGE as version ver has it, over in, a run as Gather gives it with ver's Arm: the verdict object that
// version writes to verdict.json. Each jq binding of v2's JUDGE has its Go statement or function, in the same order,
// with a comment that names it: `$name`, or the binding as jq writes it. v1, v2-r7off and v3 are v2 with features off
// or on (Features); each place a feature changes is marked, with what v1 or v3 has there (defs.go names the three
// scripts by sha256). An error is one jq raises: the judge's jq stops there, and laug judge exits 2 with nothing
// written.
func Evaluate(ver Version, in jsonx.Value) (v jsonx.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*jqError)
			if !ok {
				panic(r)
			}
			v, err = null, e
		}
	}()
	j := judging{f: ver.Features}
	return j.evaluate(in), nil
}

// judging holds JUDGE's bindings, each set once, in the order the jq program binds them. A binding that holds a
// value from the run is a jsonx.Value; a list JUDGE builds of strings, such as a rule's reasons, is a []string. f is
// the version's features.
type judging struct {
	f                                                  Features
	in, gate, gid, tag, refused, plan, th, clock, dirs jsonx.Value
	planProblems, cut                                  []string
	planned, extra                                     []jsonx.Value
	ctlMax                                             jsonx.Value
	thRel, thiRel                                      release
	leased                                             []jsonx.Value
	thi, inc, arm, cmp                                 jsonx.Value
	arm7                                               arm7
	r7ByKey                                            jsonx.Value
	items                                              []jsonx.Value
	h                                                  jsonx.Value
	r1, r2All, g5, g6                                  []string
	rules                                              jsonx.Value
	verdict, class0                                    string
	noise                                              []string
	quiet, rerun                                       bool
}

// A release is $th_rel or $thi_rel, `{ok, v}`: whether the set's resolved block holds the key release_file, and its
// value.
type release struct {
	ok bool
	v  jsonx.Value
}

func (j *judging) evaluate(in jsonx.Value) jsonx.Value {
	j.in = in                                       // . as $in
	j.gate = obj(field(in, "gate"))                 // ($in.gate | obj) as $gate
	j.gid = alt(field(j.gate, "gate_id"), null)     // ($gate.gate_id // null) as $gid
	j.tag = alt(field(j.gate, "server_tag"), null)  // ($gate.server_tag // null) as $tag
	j.refused = alt(field(j.gate, "refused"), null) // ($gate.refused // null) as $refused
	j.plan = obj(field(in, "plan"))                 // ($in.plan | obj) as $plan
	j.th = obj(field(in, "thresholds"))             // ($in.thresholds | obj) as $th
	j.clock = obj(field(in, "clock"))               // ($in.clock | obj) as $clock
	j.dirs = alt(field(in, "dirs"), object())       // ($in.dirs // {}) as $dirs

	// The plan
	j.planProblems = j.planProblemsOf() // $plan_problems
	j.cut = j.cutOf()                   // $cut
	j.planned = j.plannedOf()           // $planned
	j.extra = j.extraOf()               // $extra

	// What every item is judged against
	j.ctlMax = j.ctlMaxOf() // $ctl_max
	j.thRel = j.thRelOf()   // $th_rel
	j.leased = j.leasedOf() // $leased

	// R7: the incumbent arm. v1 has none of it: no incumbent, no arm, no R7 reason, no comparison.
	j.inc, j.arm, j.thi, j.cmp = null, null, null, null
	j.arm7, j.r7ByKey = arm7{byKey: object(), comparison: null}, object()
	if j.f.Arm {
		j.thi = obj(field(in, "thresholds_incumbent")) // ($in.thresholds_incumbent | obj) as $thi
		j.inc = alt(field(j.plan, "incumbent"), null)  // ($plan.incumbent // null) as $inc
		j.arm = j.armOf()                              // $arm
		j.cmp = obj(field(j.th, "compare"))            // ($th.compare | obj) as $cmp
		j.arm7 = j.arm7Of()                            // $arm7, arm.go
		j.thiRel = j.thiRelOf()                        // $thi_rel
		j.r7ByKey = j.arm7.byKey                       // $arm7.by_key as $r7_by_key
		if j.f.R7Off {
			j.r7ByKey = object() // R7Off, sha256 d7278dabbb85: `{} as $r7_by_key`; $arm7 is still computed
		}
	}

	// Each item
	j.items = j.itemsOf() // $items, items.go

	// The rules
	j.h = obj(field(in, "harness")) // ($in.harness | obj) as $h
	j.r1 = j.r1Of()                 // $r1
	j.r2All = j.r2AllOf()           // $R2
	j.g5 = j.g5Of()                 // $g5
	j.g6 = j.g6Of()                 // $g6
	j.rules = j.rulesOf()           // $rules
	j.verdict = j.verdictOf()       // $verdict

	// The class, and the host (classes.go)
	j.class0 = j.class0Of()     // $class0
	j.noise = j.noiseOf()       // $noise
	j.quiet = len(j.noise) == 0 // ($noise | length == 0) as $quiet
	// A BLOCK on a host that was not quiet is a rerun, but for refused and harness: $rerun.
	j.rerun = j.verdict == "BLOCK" && !j.quiet && j.class0 != "refused" && j.class0 != "harness"

	return j.output()
}

// planProblemsOf is $plan_problems: plan.json missing, not parsing, with no deadline above 0 or no items,
// an item that is no mapping, a key that is no lowercase name, a kind or an expect it does not know, a key listed
// twice.
func (j *judging) planProblemsOf() []string {
	if j.plan.IsNull() {
		return []string{missing("plan.json", field(j.in, "plan"))}
	}
	var probs []string
	if dl := field(j.plan, "deadline_s"); !typeIs(dl, "number") || !more(dl, zero) {
		probs = append(probs, "deadline_s is "+tojson(dl)+", not seconds above 0, so no item can be seen to end "+
			"before the deadline")
	}
	items := field(j.plan, "items")
	if !typeIs(items, "array") || size(items) == 0 {
		return append(probs, "plan.json lists no items")
	}
	for _, e := range toEntries(items) {
		i := tostring(e.key) // .key as $i
		it := e.value
		if !typeIs(it, "object") {
			probs = append(probs, "item "+i+" is not a mapping")
			continue
		}
		key := field(it, "key")
		k := tostring(key) // (.key | tostring) as $k
		if !typeIs(key, "string") || !test(key, keyName) {
			probs = append(probs, "item "+i+": key "+tojson(key)+" is not a lowercase name")
		}
		if kind := field(it, "kind"); !is(kind, "load") && !is(kind, "fault") {
			probs = append(probs, "item "+k+": kind is "+tojson(kind)+", not load or fault")
		}
		if expect := field(it, "expect"); !is(expect, "ADVANCE") && !is(expect, "BLOCK") {
			probs = append(probs, "item "+k+": expect is "+tojson(expect)+", not ADVANCE or BLOCK")
		}
	}
	var ks []jsonx.Value // [$plan.items[] | obj | select(. != null) | .key]
	for _, it := range each(items) {
		if o := obj(it); !o.IsNull() {
			ks = append(ks, field(o, "key"))
		}
	}
	for _, g := range groupBy(ks) {
		if len(g) > 1 {
			probs = append(probs, "item "+tojson(g[0])+" is listed "+tostring(count(len(g)))+" times")
		}
	}
	return probs
}

// cutOf is $cut: a plan cut by ITEMS ran fewer items than the plan file lists, and what did not run cannot
// pass.
func (j *judging) cutOf() []string {
	items := field(j.plan, "items")
	if j.plan.IsNull() || !typeIs(items, "array") {
		return nil
	}
	inPlan, n := field(j.plan, "items_in_plan"), length(items)
	switch {
	case !typeIs(inPlan, "number"):
		return []string{"plan.json records no items_in_plan, so it cannot be seen that every item of the plan ran"}
	case more(inPlan, n):
		limit := ""
		if l := field(j.gate, "items_limit"); typeIs(l, "number") {
			limit = " (ITEMS=" + tostring(l) + ")"
		}
		return []string{"cut to " + tostring(n) + " of " + tostring(inPlan) + " items" + limit + ": " +
			tostring(minus(inPlan, n)) + " planned item(s) never ran"}
	case less(inPlan, n):
		return []string{"plan.json lists " + tostring(n) + " items, and its items_in_plan says " + tostring(inPlan)}
	}
	return nil
}

// plannedOf is $planned: `[ $plan.items[]? | obj | select(. != null and (.key | type) == "string") ]`.
func (j *judging) plannedOf() []jsonx.Value {
	var planned []jsonx.Value
	for _, it := range eachOpt(field(j.plan, "items")) {
		if o := obj(it); !o.IsNull() && typeIs(field(o, "key"), "string") {
			planned = append(planned, o)
		}
	}
	return planned
}

// extraOf is $extra: the item directories, by key in byte order, that no planned item names.
func (j *judging) extraOf() []jsonx.Value {
	var extra []jsonx.Value
	for _, n := range keys(j.dirs) { // $dirs | keys[] | select(. as $n | any($planned[]; .key == $n) | not)
		if !slices.ContainsFunc(j.planned, func(p jsonx.Value) bool { return eq(field(p, "key"), n) }) {
			extra = append(extra, n)
		}
	}
	return extra
}

// ctlMaxOf is $ctl_max: `[$th.thresholds[]? | select(.name == "service_error_rate") | .max | num][0] //
// null`. Every threshold is read, so one that is not an object or null is jq's error.
func (j *judging) ctlMaxOf() jsonx.Value {
	var maxes []jsonx.Value
	for _, t := range eachOpt(field(j.th, "thresholds")) {
		if is(field(t, "name"), "service_error_rate") {
			maxes = append(maxes, num(field(t, "max")))
		}
	}
	if len(maxes) == 0 {
		return null
	}
	return alt(maxes[0], null)
}

// thRelOf is $th_rel.
func (j *judging) thRelOf() release {
	if res := field(j.th, "resolved"); typeIs(res, "object") && hasKey(res, "release_file") {
		return release{true, field(res, "release_file")}
	}
	return release{false, null}
}

// leasedOf is $leased: the compose project of each planned item's lease, `.project // "laug-\(.id)"`.
func (j *judging) leasedOf() []jsonx.Value {
	var leased []jsonx.Value
	for _, p := range j.planned {
		l := obj(field(index(j.dirs, field(p, "key")), "lease"))
		if !l.IsNull() {
			leased = append(leased, alt(field(l, "project"), str("laug-"+tostring(field(l, "id")))))
		}
	}
	return leased
}

// armOf is $arm: `([ $planned[] | select(.arm == "incumbent") ][0] // null)`, the first item of arm incumbent.
func (j *judging) armOf() jsonx.Value {
	for _, p := range j.planned {
		if is(field(p, "arm"), "incumbent") {
			return p
		}
	}
	return null
}

// thiRelOf is $thi_rel.
func (j *judging) thiRelOf() release {
	if typeIs(j.thi, "object") && typeIs(field(j.thi, "resolved"), "object") &&
		hasKey(field(j.thi, "resolved"), "release_file") {
		return release{true, path(j.thi, "resolved", "release_file")}
	}
	return release{false, null}
}

// r1Of is $r1, R1 harness: harness.json's exit is 0.
func (j *judging) r1Of() []string {
	switch {
	case j.h.IsNull():
		return []string{"harness: " + missing("harness.json", field(j.in, "harness")) +
			", so make harness-test was not seen to pass"}
	case eq(field(j.h, "exit"), zero):
		return nil
	}
	return []string{"harness: make harness-test exited " + tojson(field(j.h, "exit"))}
}

// per is `def per($r)`: every item's reasons under rule r, each after its key.
func (j *judging) per(r string) []string {
	var out []string
	for _, it := range j.items {
		k := field(it, "key") // .key as $k
		for _, x := range each(field(field(it, "_r"), r)) {
			out = append(out, tostring(k)+": "+tostring(x))
		}
	}
	return out
}

// r2AllOf is $R2, R2 coverage: the plan's problems and its cut, the directories no item names, each item's.
func (j *judging) r2AllOf() []string {
	var r2 []string
	for _, p := range append(slices.Clone(j.planProblems), j.cut...) {
		r2 = append(r2, "plan: "+p)
	}
	for _, e := range j.extra {
		r2 = append(r2, tostring(e)+"/ holds results for an item the plan does not list")
	}
	return append(r2, j.per("R2")...)
}

// g5Of is $g5, R5's gate-level reasons: no control max, no fault item.
func (j *judging) g5Of() []string {
	var g5 []string
	if !typeIs(j.ctlMax, "number") {
		g5 = append(g5, "thresholds.json gives service_error_rate no max to compare with")
	}
	if !j.anyFault() {
		g5 = append(g5, "the plan has no fault item, so no live control ran")
	}
	return g5
}

// g6Of is $g6, R6's gate-level reasons: gate.json, server_tag, the two sets and their release files, and
// the host mirror of preflight: the set's host type, its vcpu, and its memory within 1 GiB.
func (j *judging) g6Of() []string {
	var g6 []string
	switch {
	case j.gate.IsNull():
		g6 = append(g6, missing("gate.json", field(j.in, "gate"))+", so SERVER_TAG is unknown")
	case !typeIs(j.tag, "string"):
		g6 = append(g6, "gate.json names no server_tag")
	}
	switch {
	case j.th.IsNull():
		g6 = append(g6, missing("thresholds.json", field(j.in, "thresholds")))
	case !j.thRel.ok:
		g6 = append(g6, "thresholds.json has no resolved.release_file")
	}
	switch {
	case !j.f.Arm: // v1 ends $g6 here
	case !j.arm.IsNull() && j.thi.IsNull():
		g6 = append(g6, missing("thresholds-incumbent.json", field(j.in, "thresholds_incumbent"))+
			", so the thresholds of the incumbent are unknown")
	case !j.arm.IsNull() && !j.thiRel.ok:
		g6 = append(g6, "thresholds-incumbent.json has no resolved.release_file")
	}
	if !j.f.HostCheck {
		return g6
	}
	sh := alt(path(j.th, "resolved", "host"), null) // ($th.resolved.host // null) as $sh
	gh := alt(field(j.gate, "host"), null)          // ($gate.host // null) as $gh
	switch {
	case j.th.IsNull():
	case !typeIs(sh, "object"):
		g6 = append(g6, "thresholds.json names no host type, so this host cannot be seen to be the one its numbers "+
			"came from")
	case !typeIs(gh, "object") || !eq(field(gh, "type"), field(sh, "type")) ||
		!eq(field(gh, "vcpu"), field(sh, "vcpu")) || !typeIs(field(gh, "mem_gib"), "number") ||
		!typeIs(field(sh, "mem_gib"), "number") || more(minus(field(gh, "mem_gib"), field(sh, "mem_gib")), one) ||
		more(minus(field(sh, "mem_gib"), field(gh, "mem_gib")), one):
		// A gate.json host that is a string, a number, true or an array cannot be indexed here: jq's error.
		g6 = append(g6, "gate.json records host type "+tostring(alt(field(gh, "type"), str("null")))+" with "+
			tostring(alt(field(gh, "vcpu"), str("null")))+" vCPU and "+tostring(alt(field(gh, "mem_gib"), str("null")))+
			" GiB, and thresholds.json names a set measured on "+tostring(field(sh, "type"))+" with "+
			tostring(field(sh, "vcpu"))+" vCPU and "+tostring(field(sh, "mem_gib"))+" GiB")
	}
	return g6
}

// rulesOf is $rules: R1 to R7, each `{name, pass, reasons}`, passing when it has no reason, in the order verdict.json
// and verdict.md list them. v1 has R1 to R6.
func (j *judging) rulesOf() jsonx.Value {
	rule := func(name string, reasons []string) jsonx.Value {
		return object("name", str(name), "pass", jsonx.Bool(len(reasons) == 0), "reasons", strs(reasons))
	}
	rules := []any{
		"R1", rule("harness", j.r1),
		"R2", rule("coverage", j.r2All),
		"R3", rule("contract", j.per("R3")),
		"R4", rule("outcome", j.per("R4")),
		"R5", rule("live control", append(slices.Clone(j.g5), j.per("R5")...)),
		"R6", rule("identity", append(slices.Clone(j.g6), j.per("R6")...)),
	}
	if j.f.Arm {
		rules = append(rules, "R7", rule("incumbent", j.per("R7")))
	}
	return object(rules...)
}

// verdictOf is $verdict: ADVANCE when the gate did not refuse and every rule passes.
func (j *judging) verdictOf() string {
	if j.refused.IsNull() && !slices.ContainsFunc(each(j.rules), func(r jsonx.Value) bool {
		return !truthy(field(r, "pass"))
	}) {
		return "ADVANCE"
	}
	return "BLOCK"
}
