package tolerance

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// show is SHOW, the tolerance table, over o, RULE's object as jq reads it back, with pair and evidence, PAIR and
// EVIDENCE: the lines jq -r prints, in order. jq prints each as it makes it, so a jq error stops the printout after
// the lines before it; show returns those and the error. The one SHOW can raise is joining a wave that is not a list
// of strings, numbers and booleans: every gate's comparison without a wave, say.
func show(o jsonx.Value, pair, evidence string) (lines []string, err *jqError) {
	err = evaluate(func() {
		emit := func(s string) { lines = append(lines, s) }
		head := "-pair " + pair + ": a look at two plain items, not a tolerance"
		if pair == "" {
			head = "A/A gates of " + tostring(field(o, "release")) + " on " + tostring(field(o, "host")) +
				", the incumbent arm"
		}
		emit("laug · gate tolerance: " + tostring(field(o, "ng")) + " gate(s), " + head)
		if pair == "" {
			emit("wave: " + join(field(o, "wave"), " | "))
		}
		d := "the release reading minus the incumbent reading"
		if pair != "" {
			parts := jsonx.Arr(split(str(pair), ":"))
			d = tostring(at(parts, 1)) + " minus " + tostring(at(parts, 0))
		}
		emit("d = " + d + ", per gate; T = max(m + 2 x (largest d - m), floor), rounded up to 0.01 ms or 0.0001")
		var gates []jsonx.Value
		for _, g := range each(field(o, "gates")) {
			gates = append(gates, str(tostring(field(g, "id"))+" "+tostring(field(g, "driver"))+" "+
				tostring(alt(field(g, "commit"), str("-")))))
		}
		emit("gates: " + join(jsonx.Arr(gates), ", "))
		emit("")
		emit(pad(str("reading"), 42) + " " + pad(str("scale"), 5) + " " + lpad(str("n"), 3) + " " + lpad(str("m"), 10) +
			" " + lpad(str("largest"), 10) + " " + lpad(str("T"), 8) + " " + lpad(str("floor"), 7) + "  jump")
		rows := each(field(o, "rows"))
		for _, r := range rows {
			if !truthy(field(r, "compare")) {
				continue
			}
			s := field(r, "scale")
			emit(pad(field(r, "name"), 42) + " " + pad(s, 5) + " " + lpad(field(r, "n"), 3) + " " +
				lpad(str(v(field(r, "m"), s)), 10) + " " + lpad(str(v(field(r, "largest"), s)), 10) + " " +
				lpad(str(t(field(r, "t"), s)), 8) + " " + lpad(str(tostring(field(r, "floor"))), 7) + "  " +
				jump(field(r, "jump"), s))
		}
		emit("")
		emit("recorded, not compared:")
		for _, r := range rows {
			if truthy(field(r, "compare")) {
				continue
			}
			s := field(r, "scale")
			emit(pad(field(r, "name"), 42) + " " + pad(s, 5) + " " + lpad(field(r, "n"), 3) + " " +
				lpad(str(v(field(r, "m"), s)), 10) + " " + lpad(str(v(field(r, "largest"), s)), 10))
		}
		emit("")
		emit("mean d by driver (standard error); gate run runs run.sh, gate run -workflow step.sh:")
		showMeans(rows, "by_driver", emit)
		emit("mean d by session, the UTC date in the gate id (standard error):")
		showMeans(rows, "by_session", emit)
		owner := field(o, "owner")
		if size(owner) == 0 {
			emit("to the owner: none (|m| under T/4 and the drivers under T/2 apart on every compared reading)")
		} else {
			emit("to the owner:")
		}
		for _, w := range each(owner) {
			r := field(w, "r")
			s, name := field(r, "scale"), tostring(field(r, "name"))
			if is(field(w, "kind"), "position") {
				emit("  position: |mean d| " + v(fabs(field(r, "m")), s) + " is over a quarter of T, " +
					v(divide(field(r, "t"), four), s) + ", on " + name + " (docs/DESIGN.md section 7.1, protocol 5)")
				continue
			}
			bd := field(r, "by_driver")
			emit("  drivers: the mean d of " + tostring(field(at(bd, 0), "key")) + " and " +
				tostring(field(at(bd, 1), "key")) + " are " +
				v(fabs(minus(field(at(bd, 0), "m"), field(at(bd, 1), "m"))), s) + " apart, over half of T, " +
				v(divide(field(r, "t"), two), s) + ", on " + name)
		}
		if pair != "" {
			return
		}
		emit("")
		emit("the compare: block for load/steady-state.yaml (it goes in place of compare:, its comments kept):")
		emit("compare:")
		emit("  release: " + tostring(field(o, "release")))
		emit("  wave: " + field(o, "wave").String())
		emit("  gates: " + tostring(field(o, "ng")))
		if evidence == "" {
			emit("  evidence: null   # -evidence cells/results/<file> names it; load/thresholds.sh refuses a tolerance " +
				"with none")
		} else {
			emit("  evidence: " + evidence)
		}
		emit("  rows:")
		for _, r := range rows {
			if !truthy(field(r, "compare")) {
				continue
			}
			tol := "null"
			if x := field(r, "t"); !eq(x, null) {
				tol = tostring(x)
			}
			emit("    - {name: " + tostring(field(r, "name")) + ", scale: " + tostring(field(r, "scale")) + ", floor: " +
				tostring(field(r, "floor")) + ", tolerance: " + tol + "}")
		}
	})
	return lines, err
}

// showMeans is `means($f)`: for each key of the compared rows' groups, in jq's order, one line of each
// compared row's group under that key: its n, m and standard error.
func showMeans(rows []jsonx.Value, f string, emit func(string)) {
	var keys []jsonx.Value
	for _, r := range rows {
		if truthy(field(r, "compare")) {
			for _, g := range each(field(r, f)) {
				keys = append(keys, field(g, "key"))
			}
		}
	}
	for _, k := range unique(keys) {
		var parts []jsonx.Value
		for _, r := range rows {
			if !truthy(field(r, "compare")) {
				continue
			}
			s := field(r, "scale")
			for _, g := range each(field(r, f)) {
				if eq(field(g, "key"), k) {
					parts = append(parts, str(tostring(field(r, "name"))+" n "+tostring(field(g, "n"))+" m "+
						v(field(g, "m"), s)+" ("+v(field(g, "se"), s)+")"))
				}
			}
		}
		emit("  " + tostring(k) + ": " + join(jsonx.Arr(parts), "; "))
	}
}

// fx is `fx($n)`, a number with n decimals as text, by string slicing, as judge v2's fx is: a -0 is
// not below 0, so its tostring "-0" is padded as digits, and fx(3) of -0 is "0.0-0".
func fx(x jsonx.Value, n int) string {
	i := round(times(x, jsonx.Float(math.Pow(10, float64(n)))))
	a := i
	if less(i, zeroF) {
		a = jsonx.Float(-number(i, "cannot be negated"))
	}
	s := tostring(a)
	p := s
	if l := utf8.RuneCountInString(s); l <= n {
		p = strings.Repeat("0", n-l+1) + s
	}
	k := utf8.RuneCountInString(p) - n
	sign := ""
	if less(i, zeroF) {
		sign = "-"
	}
	r := []rune(p)
	return sign + string(r[:k]) + "." + string(r[k:])
}

// zeroF is the 0 of `. < 0`.
var zeroF = constant("0")

// v is `v($s)`: a reading or a d on its scale, "-" for none.
func v(x, s jsonx.Value) string {
	switch {
	case eq(x, null):
		return "-"
	case is(s, "share"):
		return fx(x, 6)
	case is(s, "count"):
		return tostring(round(x))
	}
	return fx(x, 3)
}

// t is `t($s)`: a T on its scale, "-" for none.
func t(x, s jsonx.Value) string {
	switch {
	case eq(x, null):
		return "-"
	case is(s, "share"):
		return fx(x, 4)
	}
	return fx(x, 2)
}

// pad and lpad are `pad($w)` and `lpad($w)`: tostring, then blanks after or before it to w code points.
func pad(x jsonx.Value, w int) string {
	s := tostring(x)
	return s + strings.Repeat(" ", max(w-utf8.RuneCountInString(s), 0))
}

func lpad(x jsonx.Value, w int) string {
	s := tostring(x)
	return strings.Repeat(" ", max(w-utf8.RuneCountInString(s), 0)) + s
}

// jump is `jump($s)`: the jump check's line, or why it did not run.
func jump(j, s jsonx.Value) string {
	if eq(j, null) {
		return "not checked: n under 20"
	}
	word := "holds"
	if truthy(field(j, "fires")) {
		word = "FIRES"
	}
	return word + " (first half " + v(field(j, "first"), s) + ", later half " + v(field(j, "later"), s) + ", 2 sd " +
		v(field(j, "two_sd"), s) + ")"
}
