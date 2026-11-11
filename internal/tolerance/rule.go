package tolerance

import (
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// The numbers RULE and SHOW write in their source, as jq reads them: literals.
var (
	one, two, four   = constant("1"), constant("2"), constant("4")
	c100, c1e3, c1e4 = constant("100"), constant("1000"), constant("10000")
	c1e6, c1em9      = constant("1000000"), constant("1e-9")
)

// rule is RULE, the tolerance rule, over gates, the gathered array as jq reads it, with set, the threshold set
// --argjson gives it, and pair, PAIR. It returns {refused: [lines]} when a gate is refused, else {refused: [], ng,
// rows, release, wave, host, gates, owner}, in that key order. A jq error panics as a *jqError (evaluate).
//
// The order of evaluation is jq's wherever it decides which error comes first: a binary operator evaluates its right
// side first, a string's interpolations run from the last, and an object's values and an array's elements in order
// (seen on jq-1.7: `(error("a")) + (error("b"))` and `"\(error("a")) \(error("b"))"` fail with b, `{x: error("a"),
// y: error("b")}` with a). and and or stop at their left side.
func rule(gates, set jsonx.Value, pair string) jsonx.Value {
	crows := alt(path(set, "compare", "rows"), jsonx.Arr(nil)) // ($set.compare.rows // []) as $crows
	var cnames []jsonx.Value                                   // ($crows | map(.name)) as $cnames
	for _, c := range each(crows) {
		cnames = append(cnames, field(c, "name"))
	}
	// ($cnames + ([$set.thresholds[].name] - $cnames) + ["workflows_completed"]) as $names
	var tnames []jsonx.Value
	for _, t := range each(field(set, "thresholds")) {
		tnames = append(tnames, field(t, "name"))
	}
	names := plus(plus(jsonx.Arr(cnames), minus(jsonx.Arr(tnames), jsonx.Arr(cnames))),
		jsonx.Arr([]jsonx.Value{str("workflows_completed")}))
	host := field(set, "host")                                   // $set.host as $host
	g0 := at(gates, 0)                                           // $gates[0]
	wave := alt(path(g0, "verdict", "comparison", "wave"), null) // $wave
	rel0 := alt(path(g0, "verdict", "incumbent"), null)          // $rel0
	var refused []jsonx.Value                                    // $refused
	for _, g := range each(gates) {
		if why := refusals(g, g0, crows, host, wave, rel0, pair); len(why) > 0 {
			refused = append(refused, str(tostring(field(g, "id"))+": "+strings.Join(why, "; ")))
		}
	}
	if len(refused) > 0 {
		return object("refused", jsonx.Arr(refused))
	}
	var rows []jsonx.Value // $rows
	for _, n := range each(names) {
		rows = append(rows, row(gates, crows, n))
	}
	// The object, its values in jq's order: host's type is the one that can fail here, under PAIR.
	ng, release, hostType := length(gates), alt(path(g0, "verdict", "incumbent"), null), field(host, "type")
	var listed []jsonx.Value
	for _, g := range each(gates) {
		listed = append(listed, object("id", field(g, "id"), "driver", field(g, "driver"),
			"commit", alt(path(g, "verdict", "laug_commit"), null)))
	}
	return object("refused", jsonx.Arr([]jsonx.Value{}), "ng", ng, "rows", jsonx.Arr(rows), "release", release,
		"wave", wave, "host", hostType, "gates", jsonx.Arr(listed), "owner", jsonx.Arr(owner(rows)))
}

// refusals is why RULE refuses the gate g, each reason in order; none when it takes it.
func refusals(g, g0, crows, host, wave, rel0 jsonx.Value, pair string) []string {
	v := field(g, "verdict")
	isObj := v.Kind() == jsonx.KindObject
	var why []string
	if !isObj {
		why = append(why, "no verdict.json")
	}
	if isObj && !eq(field(v, "stopped_at"), null) {
		why = append(why, "a trap verdict (stopped at "+tostring(field(v, "stopped_at"))+"): no rule was judged")
	}
	if isObj && !eq(field(v, "host_quiet"), jsonx.Bool(true)) {
		why = append(why, "the host was not quiet: "+join(alt(field(v, "host_noise"), jsonx.Arr(nil)), "; "))
	}
	a, b := field(g, "a"), field(g, "b")
	if pair == "" && is(a, "") {
		why = append(why, "plan.json names no item with arm incumbent; -pair <a>:<b> reads two plain items")
	} else {
		if field(g, "va").Kind() != jsonx.KindObject {
			why = append(why, "no verdict JSON for "+tostring(a))
		}
		if field(g, "vb").Kind() != jsonx.KindObject {
			why = append(why, "no verdict JSON for "+tostring(b))
		}
	}
	if !(pair == "" && !is(a, "") && isObj && eq(field(v, "stopped_at"), null)) {
		return why
	}
	// The arm's own checks.
	if st, inc := field(v, "server_tag"), field(v, "incumbent"); !eq(st, inc) {
		why = append(why, "not A/A: server "+tostring(alt(st, str("null")))+", incumbent "+
			tostring(alt(inc, str("null"))))
	} else if !eq(inc, rel0) {
		rel, id := tostring(alt(rel0, str("null"))), tostring(field(g0, "id"))
		why = append(why, "A/A gates of "+tostring(alt(inc, str("null")))+", and "+id+" ran "+rel+
			": one release a calibration")
	}
	for _, e := range toEntries(alt(field(v, "rules"), object())) { // R1 to R6 that did not pass
		if !test(e.key, rRule) || eq(field(e.value, "pass"), jsonx.Bool(true)) {
			continue
		}
		reason := tostring(alt(at(field(e.value, "reasons"), 0), str("")))
		name := tostring(field(e.value, "name"))
		why = append(why, tostring(e.key)+" "+name+" failed: "+reason)
	}
	if cmp := field(v, "comparison"); cmp.Kind() != jsonx.KindObject {
		why = append(why, "verdict.json has no comparison")
	} else if !eq(field(cmp, "wave"), wave) {
		waves := join(alt(field(cmp, "wave"), jsonx.Arr(nil)), ", ")
		why = append(why, "runs another wave than "+tostring(field(g0, "id"))+": ["+waves+"]")
	} else {
		var nc []jsonx.Value
		for _, r := range eachOpt(field(cmp, "rows")) {
			if is(field(r, "result"), "not compared") {
				nc = append(nc, field(r, "name"))
			}
		}
		if len(nc) > 0 {
			why = append(why, "its comparison reads not compared on "+join(jsonx.Arr(nc), ", ")+
				": R7 saw no pair to compare")
		}
	}
	if ht := field(host, "type"); !eq(alt(path(v, "host", "type"), null), ht) {
		why = append(why, "ran on host type "+tostring(alt(path(v, "host", "type"), str("null")))+
			", and load/steady-state.yaml names "+tostring(ht))
	}
	for _, c := range each(crows) {
		n := field(c, "name")
		if field(g, "vb").Kind() == jsonx.KindObject && num(index(path(g, "vb", "readings"), n)).IsNull() &&
			!nj(field(g, "th"), n) {
			why = append(why, "no data on "+tostring(b)+" for "+tostring(n))
		}
		if field(g, "va").Kind() == jsonx.KindObject && num(index(path(g, "va", "readings"), n)).IsNull() &&
			!nj(field(g, "thi"), n) {
			why = append(why, "no data on "+tostring(a)+" for "+tostring(n))
		}
	}
	for _, s := range eachOpt(field(g, "slow")) {
		why = append(why, tostring(s)+" is there: a gate of the arm-known-slow test branch, a control, not an A/A "+
			"gate")
	}
	return why
}

// row is one reading's row: its scale, its compare row when the set has one, and over the gates where both
// sides read a number, the d per gate, rounded as R7 rounds it, with n, m, the largest d, T, the jump check and the
// means by driver and by session.
func row(gates, crows, n jsonx.Value) jsonx.Value {
	s := scaleOf(n, crows)
	var matched []jsonx.Value // ([$crows[] | select(.name == $n)][0] // null) as $c
	for _, r := range each(crows) {
		if eq(field(r, "name"), n) {
			matched = append(matched, r)
		}
	}
	c := null
	if len(matched) > 0 {
		c = alt(matched[0], null)
	}
	var ds, d []jsonx.Value // $ds and $d
	for _, g := range each(gates) {
		r := num(index(path(g, "vb", "readings"), n))
		i := num(index(path(g, "va", "readings"), n))
		if r.IsNull() || i.IsNull() {
			continue
		}
		x := rd(minus(r, i), s)
		ds = append(ds, object("d", x, "driver", field(g, "driver"), "session", slice(field(g, "id"), one, constant("9"))))
		d = append(d, x)
	}
	k := len(d)
	m, mx, t, jump := null, null, null, null
	if k > 0 {
		m = mean(d)
		mx = maxOf(d)
	}
	if !eq(c, null) && k > 0 {
		t = up(maxOf([]jsonx.Value{minus(times(mx, two), m), field(c, "floor")}), s)
	}
	if k >= 20 {
		j := sliceTo(jsonx.Arr(d), 20)
		half, rest := members(sliceTo(j, 10)), members(sliceFrom(j, 10))
		first, later, twoSD := maxOf(half), maxOf(rest), times(sd(members(j)), two)
		jump = object("first", first, "later", later, "two_sd", twoSD,
			"fires", jsonx.Bool(more(minus(later, first), twoSD)))
	}
	return object("name", n, "scale", s, "compare", jsonx.Bool(!eq(c, null)), "floor", field(c, "floor"),
		"n", count(k), "m", m, "largest", mx, "t", t, "jump", jump,
		"by_driver", means(ds, "driver"), "by_session", means(ds, "session"))
}

// means is `$ds | group_by(.<key>) | map({key: .[0].<key>, n: length, m: (map(.d) | mean), se: (map(.d) | se)})`.
func means(ds []jsonx.Value, key string) jsonx.Value {
	groups := []jsonx.Value{}
	for _, grp := range groupBy(ds, func(v jsonx.Value) jsonx.Value { return field(v, key) }) {
		d := make([]jsonx.Value, len(grp))
		for i, x := range grp {
			d[i] = field(x, "d")
		}
		groups = append(groups, object("key", field(grp[0], key), "n", count(len(grp)), "m", mean(d), "se", se(d)))
	}
	return jsonx.Arr(groups)
}

// owner is the lines to the owner: a compared reading whose |m| is over T/4, or whose two drivers' means
// are over T/2 apart.
func owner(rows []jsonx.Value) []jsonx.Value {
	out := []jsonx.Value{}
	for _, r := range rows {
		t := field(r, "t")
		if eq(t, null) {
			continue
		}
		if more(fabs(field(r, "m")), divide(t, four)) {
			out = append(out, object("kind", str("position"), "r", r))
		}
		bd := field(r, "by_driver")
		if eq(length(bd), two) && more(fabs(minus(field(at(bd, 0), "m"), field(at(bd, 1), "m"))), divide(t, two)) {
			out = append(out, object("kind", str("drivers"), "r", r))
		}
	}
	return out
}

// num is `num`: v when it is a number, else null.
func num(v jsonx.Value) jsonx.Value {
	if v.Kind() == jsonx.KindNumber {
		return v
	}
	return null
}

// rd is `rd($s)`, a d rounded on its scale: 0.000001 of a share, a whole count, else 0.001 ms.
func rd(v, s jsonx.Value) jsonx.Value {
	switch {
	case is(s, "share"):
		return divide(round(times(v, c1e6)), c1e6)
	case is(s, "count"):
		return round(v)
	}
	return divide(round(times(v, c1e3)), c1e3)
}

// scaleOf is `scale_of($n; $crows)`: the scale of the first compare row named n, unless that is false or
// null, else by the name, whose test is jq's error when the name is not a string.
func scaleOf(n, crows jsonx.Value) jsonx.Value {
	var scales []jsonx.Value
	for _, c := range each(crows) {
		if eq(field(c, "name"), n) {
			scales = append(scales, field(c, "scale"))
		}
	}
	if len(scales) > 0 && truthy(scales[0]) {
		return scales[0]
	}
	switch {
	case test(n, overMsEnd):
		return str("share")
	case test(n, msEnd):
		return str("ms")
	case is(n, "workflows_completed"):
		return str("count")
	}
	return str("share")
}

// up is `up($s)`, T rounded up to 0.0001 of a share or 0.01 ms, less 1e-9 first so that a T already on the
// step stays: `(. * 10000 - 1e-9 | ceil) / 10000`. times() rounds the product before the subtraction, as jq does.
func up(v, s jsonx.Value) jsonx.Value {
	if is(s, "share") {
		return divide(ceil(minus(times(v, c1e4), c1em9)), c1e4)
	}
	return divide(ceil(minus(times(v, c100), c1em9)), c100)
}

// mean is `mean`, `add / length`.
func mean(vs []jsonx.Value) jsonx.Value { return divide(add(vs), count(len(vs))) }

// sd is `sd`, the sample standard deviation, null under 2 values: each (x - m) squared, by times(), which
// rounds the square before add sums it.
func sd(vs []jsonx.Value) jsonx.Value {
	if len(vs) < 2 {
		return null
	}
	m := mean(vs)
	sq := make([]jsonx.Value, len(vs))
	for i, v := range vs {
		x := minus(v, m)
		sq[i] = times(x, x)
	}
	return sqrt(divide(add(sq), minus(count(len(sq)), one)))
}

// se is `se`, the standard error of the mean, null under 2 values.
func se(vs []jsonx.Value) jsonx.Value {
	if len(vs) < 2 {
		return null
	}
	return divide(sd(vs), sqrt(count(len(vs))))
}

// nj is `nj($set; $n)`: the set marks the reading n not_judged. Every threshold is read, so one that is not an
// object or null is jq's error even past the first named n.
func nj(set, n jsonx.Value) bool {
	var marks []jsonx.Value
	for _, t := range eachOpt(field(alt(set, object()), "thresholds")) {
		if eq(field(t, "name"), n) {
			marks = append(marks, field(t, "no_data"))
		}
	}
	return len(marks) > 0 && is(marks[0], "not_judged")
}
