package judge

import (
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// An arm7 is $arm7, R7 the incumbent arm (docs/DESIGN.md section 7.1): byKey, R7's reasons by item key,
// and comparison, the record verdict.json keeps, null with no arm.
type arm7 struct {
	byKey, comparison jsonx.Value
}

// A why is $why: why the pair cannot be compared at all, and the item the reason goes on.
type why struct{ key, text string }

// arm7Of is $arm7: with an item of arm incumbent, the release's steady load against the incumbent's,
// reading by reading as thresholds.json's compare rows say.
func (j *judging) arm7Of() arm7 {
	if j.arm.IsNull() {
		return arm7{byKey: object(), comparison: null}
	}
	ikV := field(j.arm, "key") // $arm.key as $ik
	ik, _ := ikV.Str()
	pair := field(j.arm, "pair")
	rk, _ := pair.Str()                  // ($arm.pair | if type == "string" then . else "" end) as $rk
	nopair := !typeIs(pair, "string") || // $nopair
		!slices.ContainsFunc(j.planned, func(p jsonx.Value) bool { return eq(field(p, "key"), pair) })
	iv := obj(field(index(j.dirs, str(ik)), "verdict")) // ($dirs[$ik].verdict | obj) as $iv
	rv := obj(field(index(j.dirs, str(rk)), "verdict")) // ($dirs[$rk].verdict | obj) as $rv
	irun := obj(field(index(j.dirs, str(ik)), "run"))   // ($dirs[$ik].run | obj) as $irun
	rrun := obj(field(index(j.dirs, str(rk)), "run"))   // ($dirs[$rk].run | obj) as $rrun
	var crows []jsonx.Value                             // [ $cmp.rows[]? | objects ] as $crows
	for _, r := range eachOpt(field(j.cmp, "rows")) {
		if typeIs(r, "object") {
			crows = append(crows, r)
		}
	}
	judgingMode := slices.ContainsFunc(crows, func(c jsonx.Value) bool { // any(...) as $judging
		return !field(c, "tolerance").IsNull()
	})
	var bad []jsonx.Value // $bad
	for _, c := range crows {
		floor, tol := field(c, "floor"), field(c, "tolerance")
		if !tol.IsNull() && (!typeIs(floor, "number") || !more(floor, zero) || !typeIs(tol, "number") ||
			less(tol, floor)) {
			bad = append(bad, c)
		}
	}
	wave := waveSig(field(j.plan, "items")) // ($plan.items | wave_sig) as $wave
	names := j.namesOf(crows)               // $names
	starts := []jsonx.Value{                // [ $rrun.start, $irun.start | epoch ] as $starts
		epoch(field(rrun, "start")), epoch(field(irun, "start")),
	}
	apart := null // $apart
	if !starts[0].IsNull() && !starts[1].IsNull() {
		apart = abs(minus(starts[0], starts[1]))
	}
	lens := object("release", num(field(rv, "window_s")), "incumbent", num(field(iv, "window_s"))) // $len
	w := j.whyOf(nopair, pair, ik, rk, iv, rv, judgingMode, bad, wave, apart, lens)                // $why
	rows := j.rowsOf(names, crows, rv, iv, judgingMode, w, ik, rk)                                 // $rows
	byKey := j.byKeyOf(nopair, judgingMode, w, rows, ik, rk)                                       // $by_key
	notes := j.notesOf(judgingMode, iv, rv)                                                        // $notes
	mode := "recording"
	if judgingMode {
		mode = "judging"
	}
	out := make([]jsonx.Value, len(rows))
	for i, r := range rows {
		out[i] = del(r, "_nodata")
	}
	return arm7{byKey: byKey, comparison: object(
		"incumbent", ikV, "release", str(rk), "mode", str(mode),
		"wave", strs(wave), "wave_measured", alt(field(j.cmp, "wave"), null),
		"measured_with", alt(field(j.cmp, "release"), null),
		"evidence", alt(field(j.cmp, "evidence"), null), "gates", alt(field(j.cmp, "gates"), null),
		"windows", object("start_apart_s", apart, "length_s", lens), "notes", strs(notes),
		"rows", jsonx.Arr(out),
	)}
}

// namesOf is $names: the compare rows' names, then the set's other thresholds' names, then
// workflows_completed. Every threshold is read, so one that is not an object or null is jq's error.
func (j *judging) namesOf(crows []jsonx.Value) []jsonx.Value {
	crowNames := make([]jsonx.Value, len(crows)) // [$crows[].name]
	for i, c := range crows {
		crowNames[i] = field(c, "name")
	}
	var thNames []jsonx.Value // [$th.thresholds[]?.name]
	for _, t := range eachOpt(field(j.th, "thresholds")) {
		thNames = append(thNames, field(t, "name"))
	}
	rest, _ := minus(jsonx.Arr(thNames), jsonx.Arr(crowNames)).Arr()
	return append(append(crowNames, rest...), str("workflows_completed"))
}

// whyOf is $why: the pair names no item of the plan; the incumbent did not ADVANCE; the release has no
// verdict; a judged row's tolerance or floor is bad; the plan's wave is not the one the tolerances came from; a
// start is not recorded; the windows started more than 2 s apart; a window_s is missing; their lengths are more than
// 2 s apart. nil when none holds.
func (j *judging) whyOf(nopair bool, pair jsonx.Value, ik, rk string, iv, rv jsonx.Value, judgingMode bool,
	bad []jsonx.Value, wave []string, apart, lens jsonx.Value) *why {
	switch {
	case nopair:
		p := tojson(pair)
		if s, ok := pair.Str(); ok {
			p = s
		}
		return &why{ik, "not compared: the pair " + p + " is not an item of this plan"}
	case !is(alt(field(iv, "verdict"), null), "ADVANCE"):
		return &why{ik, "not compared: " + ik + " did not ADVANCE (verdict " +
			tostring(alt(field(iv, "verdict"), str("missing"))) + ")"}
	case alt(field(rv, "verdict"), null).IsNull():
		return &why{rk, "not compared: " + rk + " has no verdict to compare"}
	case judgingMode && len(bad) > 0:
		return &why{ik, "not compared: compare row " + tostring(field(bad[0], "name")) + " has tolerance " +
			tojson(field(bad[0], "tolerance")) + " and floor " + tojson(field(bad[0], "floor")) +
			" in thresholds.json: a tolerance is a number at or above its floor, and a floor a number above 0"}
	case judgingMode && !eq(strs(wave), field(j.cmp, "wave")):
		// A measured wave that is not an array or an object of strings, numbers and booleans cannot be joined:
		// jq's error. The command it names is v2's make target, or with ToleranceText v3's laug verb.
		run := "make arm-tolerance"
		if j.f.ToleranceText {
			run = "laug gate tolerance"
		}
		return &why{ik, "not compared: the tolerances in load/steady-state.yaml were measured on the wave [" +
			join(alt(field(j.cmp, "wave"), jsonx.Arr(nil)), ", ") + "], and this plan runs [" +
			strings.Join(wave, ", ") + "]: " + run + " on A/A gates of this plan"}
	case apart.IsNull():
		return &why{ik, "not compared: the run JSON of " + rk + " or " + ik +
			" records no start, so the two windows cannot be seen to be paired"}
	case more(apart, two):
		return &why{ik, "not compared: the windows of " + rk + " and " + ik + " started " + tostring(apart) +
			" s apart (2 s at most)"}
	}
	rel, inc := field(lens, "release"), field(lens, "incumbent")
	switch {
	case rel.IsNull() || inc.IsNull():
		return &why{ik, "not compared: the verdict JSON of " + rk + " or " + ik + " records no window_s"}
	case more(abs(minus(rel, inc)), two):
		return &why{ik, "not compared: the windows of " + rk + " and " + ik + " were " + tostring(rel) + " s and " +
			tostring(inc) + " s long (2 s apart at most)"}
	}
	return nil
}

// rowsOf is $rows: a row per name, its scale, both readings, d rounded on the scale, the tolerance and
// the result. A name a readings object cannot be indexed by (`[$n]?` gives nothing) has no row.
func (j *judging) rowsOf(names, crows []jsonx.Value, rv, iv jsonx.Value, judgingMode bool, w *why,
	ik, rk string) []jsonx.Value {
	var rows []jsonx.Value
	for _, n := range names { // $names[] as $n
		c := null // ([$crows[] | select(.name == $n)][0] // null) as $c
		for _, cr := range crows {
			if eq(field(cr, "name"), n) {
				c = cr
				break
			}
		}
		s := scaleOf(n, crows) // scale_of($n; $crows) as $s
		r, ok := indexOpt(field(rv, "readings"), n)
		if !ok {
			continue
		}
		r = num(r) // ($rv.readings[$n]? | num) as $r
		i, ok := indexOpt(field(iv, "readings"), n)
		if !ok {
			continue
		}
		i = num(i) // ($iv.readings[$n]? | num) as $i
		d := null  // $d
		if !r.IsNull() && !i.IsNull() {
			d = rd(minus(r, i), s)
		}
		t := num(field(c, "tolerance"))      // ($c.tolerance | num) as $t
		judged := judgingMode && !t.IsNull() // ($judging and $t != null) as $judged
		var nodata []string                  // $nodata: a null side its set does not mark not_judged
		if r.IsNull() && !nj(j.th, n) {
			nodata = append(nodata, rk)
		}
		if i.IsNull() && !nj(j.thi, n) {
			nodata = append(nodata, ik)
		}
		var res string // $res
		switch {
		case w != nil:
			res = "not compared"
		case !judged:
			res = "recorded"
		case r.IsNull() || i.IsNull():
			res = "not compared"
			if len(nodata) == 0 {
				res = "not produced"
			}
		case more(d, t):
			res = "WORSE"
		default:
			res = "ok"
		}
		nd := null
		if judged && w == nil && len(nodata) > 0 {
			nd = strs(nodata)
		}
		rows = append(rows, object("name", n, "scale", s, "release", r, "incumbent", i, "d", d, "tolerance", t,
			"judged", jsonx.Bool(judged), "result", str(res), "_nodata", nd))
	}
	return rows
}

// byKeyOf is $by_key, R7's reasons by item key: with no pair, $why on its item in either mode; in
// recording mode nothing else; judging, $why on its item, else each worse reading on the release's item, first,
// then each side with no data on its own.
func (j *judging) byKeyOf(nopair, judgingMode bool, w *why, rows []jsonx.Value, ik, rk string) jsonx.Value {
	switch {
	case nopair:
		return object(w.key, strs([]string{w.text}))
	case !judgingMode:
		return object()
	case w != nil:
		return object(w.key, strs([]string{w.text}))
	}
	var xs []why
	for _, row := range rows {
		if !is(field(row, "result"), "WORSE") {
			continue
		}
		wr := row // . as $w
		scale := field(wr, "scale")
		xs = append(xs, why{rk, "worse than the incumbent: " + tostring(field(wr, "name")) + " " +
			val(j.f, field(wr, "release"), scale) + " against " + val(j.f, field(wr, "incumbent"), scale) + " on " +
			ik + ", " + dval(j.f, field(wr, "d"), scale) + " past a tolerance of " +
			tval(j.f, field(wr, "tolerance"), scale)})
	}
	for _, row := range rows {
		nd := field(row, "_nodata")
		if nd.IsNull() {
			continue
		}
		n := field(row, "name") // .name as $n
		for _, side := range each(nd) {
			s, _ := side.Str()
			xs = append(xs, why{s, "not compared: no data on " + s + " for " + tostring(n)})
		}
	}
	byKey := jsonx.NewObject()
	for _, x := range xs { // reduce .[] as $x ({}; .[$x.key] += [$x.text])
		prev, _ := byKey.Get(x.key)
		byKey.Set(x.key, plus(prev, strs([]string{x.text})))
	}
	return jsonx.Obj(byKey)
}

// notesOf is $notes: the tolerances came from another major.minor than the incumbent, and both arms
// BLOCKed. An incumbent's deciding that cannot be joined is jq's error.
func (j *judging) notesOf(judgingMode bool, iv, rv jsonx.Value) []string {
	var notes []string
	if rel := field(j.cmp, "release"); judgingMode && typeIs(rel, "string") && typeIs(j.inc, "string") &&
		mm(rel) != mm(j.inc) {
		notes = append(notes, "Tolerances measured with "+tostring(rel)+" on both arms; the incumbent of this gate is "+
			tostring(j.inc)+".")
	}
	if is(field(iv, "verdict"), "BLOCK") && is(field(rv, "verdict"), "BLOCK") {
		notes = append(notes, "The incumbent BLOCKed too, on "+join(alt(field(iv, "deciding"), jsonx.Arr(nil)), ", ")+
			": the host, or the cell config both arms share, is the likelier cause.")
	}
	return notes
}
