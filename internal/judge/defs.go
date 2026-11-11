package judge

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// This file holds what JUDGE runs on: jq 1.7's errors, its value operations and the builtins JUDGE and DEFS call, each
// as jq-1.7 does it (TestBuiltins), then the DEFS under their jq names, but for the class table (classes.go) and epoch
// (epoch.go). The jq a comment quotes or names, here and in rules.go, arm.go, items.go and classes.go, is judge v2's
// (sha256 33b4e89dc2ec). v3's (ece65b424762) is v2's with two fixes; v1's (284a759c5455) is named where it differs.
// The package comment says what became of those scripts.
//
// The rules are typed functions over jsonx values. A value read from a run stays a jsonx.Value all the way: a Go
// struct would drop a literal's text, a key order or a type error jq raises. Go strings and bools stand only for
// what JUDGE itself made a string or a boolean, such as a reason text.

// A jqError is an error the judge's jq raises, such as indexing a number with a name. Nothing catches it but a `?` or a
// try, so jq stops, exits 5, and the judge exits 2 with no verdict written. In jq 1.7 the alternative operator `//`
// does not catch one either (seen on jq-1.7: `(1 | .a) // 2` fails).
type jqError struct{ msg string }

func (e *jqError) Error() string { return "jq: error: " + e.msg }

// fail raises a jq error. It unwinds to Evaluate, or to the one try or `?` that catches it.
func fail(format string, a ...any) {
	panic(&jqError{msg: fmt.Sprintf(format, a...)})
}

// try runs f as jq's try does: ok is false when f raised a jq error. Any other panic goes on.
func try[T any](f func() T) (v T, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, is := r.(*jqError); !is {
				panic(r)
			}
			ok = false
		}
	}()
	return f(), true
}

var null = jsonx.Null()

// constant is a number written in the jq source. jq 1.7 reads it as a literal, so it compares with a literal as an
// exact decimal: `$exit == 2` holds for 2.0 and not for 2.0000000000000001 (seen on jq-1.7).
func constant(tok string) jsonx.Value {
	n, err := jsonx.Literal(tok)
	if err != nil {
		panic(err)
	}
	return jsonx.Num(n)
}

var zero, one, two, three = constant("0"), constant("1"), constant("2"), constant("3")

// str is the jq string s; count is a count jq computed, such as length's.
func str(s string) jsonx.Value { return jsonx.Str(s) }
func count(n int) jsonx.Value  { return jsonx.Float(float64(n)) }

// truthy is jq's truth: every value but false and null.
func truthy(v jsonx.Value) bool {
	return v.Kind() != jsonx.KindNull && v.Kind() != jsonx.KindFalse
}

// alt is `a // b` for an a that gives one value: a, unless it is false or null, then b. The caller reaches b only
// through alt when b cannot fail; JUDGE's one right side that can (scale_of's test) is written out.
func alt(a, b jsonx.Value) jsonx.Value {
	if truthy(a) {
		return a
	}
	return b
}

// eq is jq's ==, and is is v == the string s.
func eq(a, b jsonx.Value) bool { return jsonx.Equal(a, b) }
func is(v jsonx.Value, s string) bool {
	t, ok := v.Str()
	return ok && t == s
}

// less and more are jq's < and >: its total order, two literals as decimals.
func less(a, b jsonx.Value) bool { return jsonx.Compare(a, b) < 0 }
func more(a, b jsonx.Value) bool { return jsonx.Compare(a, b) > 0 }

// typeIs is `(v | type) == t`.
func typeIs(v jsonx.Value, t string) bool { return v.Type() == t }

// object builds a jq object from key and value pairs, in order.
func object(kv ...any) jsonx.Value {
	o := jsonx.NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1].(jsonx.Value))
	}
	return jsonx.Obj(o)
}

// strs is a list of strings as a jq array.
func strs(ss []string) jsonx.Value {
	vs := make([]jsonx.Value, len(ss))
	for i, s := range ss {
		vs[i] = str(s)
	}
	return jsonx.Arr(vs)
}

// field is `.name` on v: an object's member or null, and null on null. Anything else is jq's error.
func field(v jsonx.Value, name string) jsonx.Value {
	switch v.Kind() {
	case jsonx.KindNull:
		return null
	case jsonx.KindObject:
		o, _ := v.Obj()
		m, _ := o.Get(name)
		return m
	}
	fail("Cannot index %s with string %q", v.Type(), name)
	return null
}

// path is `.a.b.c` on v, each step a field.
func path(v jsonx.Value, names ...string) jsonx.Value {
	for _, n := range names {
		v = field(v, n)
	}
	return v
}

// index is `.[k]` on t, as jq 1.7's jv_get does it: an object by a string; an array by a number, its whole part,
// from the end when below 0, null past either end; an array or a string by {start, end} as a slice; an array by an
// array as the places it starts; and null by a string, a number or an object. Anything else is jq's error. JUDGE
// indexes by a value it did not make only at `$rv.readings[$n]?` and `$iv.readings[$n]?`.
func index(t, k jsonx.Value) jsonx.Value {
	switch tk, kk := t.Kind(), k.Kind(); {
	case tk == jsonx.KindObject && kk == jsonx.KindString:
		s, _ := k.Str()
		return field(t, s)
	case tk == jsonx.KindArray && kk == jsonx.KindNumber:
		a, _ := t.Arr()
		n, _ := k.Num()
		if math.IsNaN(n.F) || math.Abs(n.F) >= 1<<31 {
			return null // past int's range C's (int) gives a place past either end, on amd64 and arm64 alike
		}
		i := int(n.F) // cut toward 0, as C's (int): .[1.5] is .[1] (seen on jq-1.7)
		if i < 0 {
			i += len(a)
		}
		if i < 0 || i >= len(a) {
			return null
		}
		return a[i]
	case (tk == jsonx.KindArray || tk == jsonx.KindString) && kk == jsonx.KindObject:
		return sliceBy(t, k)
	case tk == jsonx.KindArray && kk == jsonx.KindArray:
		return indices(t, k)
	case tk == jsonx.KindNull && (kk == jsonx.KindString || kk == jsonx.KindNumber || kk == jsonx.KindObject):
		return null
	}
	if k.Kind() == jsonx.KindString {
		s, _ := k.Str()
		fail("Cannot index %s with string %q", t.Type(), s)
	}
	fail("Cannot index %s with %s", t.Type(), k.Type())
	return null
}

// sliceBy is jv_get's slice of an array or a string by an object's start and end (jq 1.7, parse_slice): a missing
// key is jq's error, null is the start or the end, a value below 0 counts from the end, both are clamped, the start
// is cut down and the end rounded up to a whole place (seen on jq-1.7: {"start":0.5,"end":1.5} takes [1,2] of
// [1,2,3], and {"start":1} on "abc" is an error).
func sliceBy(t, k jsonx.Value) jsonx.Value {
	o, _ := k.Obj()
	l, _ := length(t).Num()
	bound := func(key string, missing float64) (float64, bool) {
		v, ok := o.Get(key)
		switch {
		case !ok:
			return 0, false
		case v.IsNull():
			return missing, true
		}
		x, ok := v.Num()
		return x.F, ok
	}
	start, ok1 := bound("start", 0)
	end, ok2 := bound("end", l.F)
	if !ok1 || !ok2 {
		fail("Start and end indices of an array slice must be numbers")
	}
	if start < 0 {
		start += l.F
	}
	if end < 0 {
		end += l.F
	}
	start = min(max(start, 0), l.F)
	end = max(min(end, l.F), start)
	i, j := int(start), int(end)
	if end > float64(j) {
		j++
	}
	if s, ok := t.Str(); ok {
		return str(runeSlice(s, i, j))
	}
	a, _ := t.Arr()
	return jsonx.Arr(slices.Clone(a[i:j]))
}

// indices is jv_get's array by an array (jq 1.7, jv_array_indexes): the places where b starts in a, [] when it
// starts nowhere or b is empty (seen on jq-1.7).
func indices(t, k jsonx.Value) jsonx.Value {
	a, _ := t.Arr()
	b, _ := k.Arr()
	at := []jsonx.Value{}
	for i := range a {
		if len(b) > 0 && i+len(b) <= len(a) && slices.EqualFunc(a[i:i+len(b)], b, eq) {
			at = append(at, count(i))
		}
	}
	return jsonx.Arr(at)
}

// indexOpt is `.[k]?`: index, or nothing when it raises an error (jq 1.7's INDEX_OPT covers the index alone).
func indexOpt(t, k jsonx.Value) (jsonx.Value, bool) {
	return try(func() jsonx.Value { return index(t, k) })
}

// each is `.[]` on v: an array's elements, an object's values in key order. Anything else is jq's error.
func each(v jsonx.Value) []jsonx.Value {
	if k := v.Kind(); k != jsonx.KindArray && k != jsonx.KindObject {
		fail("Cannot iterate over %s", v.Type())
	}
	return members(v)
}

// eachOpt is `.[]?`: each, or nothing when v is not an array or an object (registry.go's members).
func eachOpt(v jsonx.Value) []jsonx.Value { return members(v) }

// length is jq's length: a string's code points, an array's elements, an object's members, 0 for null and a number's
// absolute value. A boolean has none: jq's error.
func length(v jsonx.Value) jsonx.Value {
	switch v.Kind() {
	case jsonx.KindNull:
		return count(0)
	case jsonx.KindNumber:
		n, _ := v.Num()
		return jsonx.Float(math.Abs(n.F))
	case jsonx.KindString:
		s, _ := v.Str()
		return count(utf8.RuneCountInString(s))
	case jsonx.KindArray:
		a, _ := v.Arr()
		return count(len(a))
	case jsonx.KindObject:
		o, _ := v.Obj()
		return count(o.Len())
	}
	fail("%s (%s) has no length", v.Type(), v)
	return null
}

// size is length as a Go int, for the arrays JUDGE counts.
func size(v jsonx.Value) int {
	n, _ := length(v).Num()
	return int(n.F)
}

// tojson is jq's tojson: the -c form, a literal by its canonical text, a computed number in dtoa form.
func tojson(v jsonx.Value) string { return v.String() }

// tostring is jq's tostring, and what "\(v)" interpolates: a string as it is, anything else as tojson.
func tostring(v jsonx.Value) string {
	if s, ok := v.Str(); ok {
		return s
	}
	return tojson(v)
}

// join is jq 1.7's join($sep) (builtin.jq): over `.[]`, so an array's elements or an object's values, else jq's
// error; null joins as "", a number or a boolean as its tojson, a string as it is; an array or an object is jq's
// error, a string and it cannot be added. Nothing to join gives "".
func join(v jsonx.Value, sep string) string {
	var b strings.Builder
	for i, e := range each(v) {
		if i > 0 {
			b.WriteString(sep)
		}
		switch e.Kind() {
		case jsonx.KindNull:
		case jsonx.KindFalse, jsonx.KindTrue, jsonx.KindNumber:
			b.WriteString(tojson(e))
		case jsonx.KindString:
			s, _ := e.Str()
			b.WriteString(s)
		default:
			fail("string (%s) and %s (%s) cannot be added", str(b.String()), e.Type(), e)
		}
	}
	return b.String()
}

// split is jq 1.7's split($sep) for a string sep (jv_string_split): "" gives no part, a sep at the end an empty last
// part. Both must be strings: jq's error.
func split(v jsonx.Value, sep string) []string {
	s, ok := v.Str()
	if !ok {
		fail("split input and separator must be strings")
	}
	var parts []string
	for p := 0; p < len(s); {
		i := strings.Index(s[p:], sep)
		if i < 0 {
			parts = append(parts, s[p:])
			break
		}
		parts = append(parts, s[p:p+i])
		p += i + len(sep)
		if p == len(s) {
			parts = append(parts, "")
		}
	}
	return parts
}

// startswith is jq's startswith($prefix); both must be strings: jq's error.
func startswith(v jsonx.Value, prefix string) bool {
	s, ok := v.Str()
	if !ok {
		fail("startswith() requires string inputs")
	}
	return strings.HasPrefix(s, prefix)
}

// keys is jq's keys: an object's keys by their bytes, an array's places; anything else is jq's error.
func keys(v jsonx.Value) []jsonx.Value {
	if o, ok := v.Obj(); ok {
		ks := o.SortedKeys()
		vs := make([]jsonx.Value, len(ks))
		for i, k := range ks {
			vs[i] = str(k)
		}
		return vs
	}
	if a, ok := v.Arr(); ok {
		vs := make([]jsonx.Value, len(a))
		for i := range a {
			vs[i] = count(i)
		}
		return vs
	}
	fail("%s (%s) has no keys", v.Type(), v)
	return nil
}

// An entry is one of to_entries' {key, value}.
type entry struct{ key, value jsonx.Value }

// toEntries is jq's to_entries: `[keys_unsorted[] as $k | {key: $k, value: .[$k]}]`, an object's members in key
// order or an array's elements by place.
func toEntries(v jsonx.Value) []entry {
	if o, ok := v.Obj(); ok {
		es := make([]entry, o.Len())
		for i := range es {
			k, m := o.Member(i)
			es[i] = entry{str(k), m}
		}
		return es
	}
	if a, ok := v.Arr(); ok {
		es := make([]entry, len(a))
		for i, e := range a {
			es[i] = entry{count(i), e}
		}
		return es
	}
	fail("%s (%s) has no keys", v.Type(), v)
	return nil
}

// hasKey is jq 1.7's has($k) for a string k: an object's member, false on null (seen on jq-1.7); an array is jq's
// error, as is anything else.
func hasKey(v jsonx.Value, k string) bool {
	switch v.Kind() {
	case jsonx.KindNull:
		return false
	case jsonx.KindObject:
		o, _ := v.Obj()
		_, ok := o.Get(k)
		return ok
	}
	fail("Cannot check whether %s has a string key", v.Type())
	return false
}

// del is `del(.name)` on an object: the object less that member, the others in their order.
func del(v jsonx.Value, name string) jsonx.Value {
	o, ok := v.Obj()
	if !ok {
		fail("Cannot delete field at object index of %s", v.Type())
	}
	out := jsonx.NewObject()
	for i := range o.Len() {
		if k, m := o.Member(i); k != name {
			out.Set(k, m)
		}
	}
	return jsonx.Obj(out)
}

// groupBy is `group_by(.)`: a stable sort by jq's order, then runs of equal values (jq 1.7, jv_group).
func groupBy(vs []jsonx.Value) [][]jsonx.Value {
	sorted := slices.Clone(vs)
	jsonx.Sort(sorted)
	var groups [][]jsonx.Value
	for i, v := range sorted {
		if i == 0 || !eq(sorted[i-1], v) {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], v)
	}
	return groups
}

// unique is jq 1.7's unique, `group_by(.) | map(.[0])`: the first of each run of equals, so [1.0, 1] gives [1.0].
func unique(vs []jsonx.Value) []jsonx.Value {
	var out []jsonx.Value
	for _, g := range groupBy(vs) {
		out = append(out, g[0])
	}
	return out
}

// maxOf is jq's max: null for none, and the last of equal greatest values (seen on jq-1.7: [2.0, 2] gives 2).
func maxOf(vs []jsonx.Value) jsonx.Value {
	m := null
	for i, v := range vs {
		if i == 0 || jsonx.Compare(v, m) >= 0 {
			m = v
		}
	}
	return m
}

// number is v's number, or jq's error naming what op could not take v.
func number(v jsonx.Value, op string) float64 {
	n, ok := v.Num()
	if !ok {
		fail("%s (%s) %s", v.Type(), v, op)
	}
	return n.F
}

// plus is jq's +: null adds nothing, two numbers add as doubles, strings and arrays join, objects merge; any other
// pair is jq's error.
func plus(a, b jsonx.Value) jsonx.Value {
	switch {
	case a.IsNull():
		return b
	case b.IsNull():
		return a
	case a.Kind() == jsonx.KindNumber && b.Kind() == jsonx.KindNumber:
		x, _ := a.Num()
		y, _ := b.Num()
		return jsonx.Float(x.F + y.F)
	case a.Kind() == jsonx.KindString && b.Kind() == jsonx.KindString:
		x, _ := a.Str()
		y, _ := b.Str()
		return str(x + y)
	case a.Kind() == jsonx.KindArray && b.Kind() == jsonx.KindArray:
		x, _ := a.Arr()
		y, _ := b.Arr()
		return jsonx.Arr(append(slices.Clone(x), y...))
	case a.Kind() == jsonx.KindObject && b.Kind() == jsonx.KindObject:
		x, _ := a.Obj()
		y, _ := b.Obj()
		out := jsonx.NewObject()
		for i := range x.Len() {
			out.Set(x.Member(i))
		}
		for i := range y.Len() {
			out.Set(y.Member(i))
		}
		return jsonx.Obj(out)
	}
	fail("%s (%s) and %s (%s) cannot be added", a.Type(), a, b.Type(), b)
	return null
}

// minus is jq's - on two numbers, a double's difference, and on two arrays, a's elements that equal none of b's, in
// order. Any other pair is jq's error.
func minus(a, b jsonx.Value) jsonx.Value {
	if a.Kind() == jsonx.KindArray && b.Kind() == jsonx.KindArray {
		x, _ := a.Arr()
		y, _ := b.Arr()
		var out []jsonx.Value
		for _, e := range x {
			if !slices.ContainsFunc(y, func(f jsonx.Value) bool { return eq(e, f) }) {
				out = append(out, e)
			}
		}
		return jsonx.Arr(out)
	}
	if a.Kind() != jsonx.KindNumber || b.Kind() != jsonx.KindNumber {
		fail("%s (%s) and %s (%s) cannot be subtracted", a.Type(), a, b.Type(), b)
	}
	x, _ := a.Num()
	y, _ := b.Num()
	return jsonx.Float(x.F - y.F)
}

// times is jq's * on a number and a constant, a double's product, which jq rounds. The float64() is not a no-op: the
// Go spec lets a compiler fuse a product with a later add, across statements too, unless an explicit conversion
// rounds the product first (the Go spec, Floating-point operators). Go's arm64 compiler fuses and its amd64 compiler
// does not, so without it r3, rd and fx could differ on arm64 alone. TestNoFusableExpressions holds every float
// product to this form; only a run on arm64 checks the result, as cells/results/2026-10-22-judge-arm64.txt did.
func times(a jsonx.Value, k float64) jsonx.Value {
	return jsonx.Float(float64(number(a, "and number cannot be multiplied") * k))
}

// negate is jq 1.7's unary minus: a computed number, so -(1.000) prints -1 and -(0) prints -0 (seen on jq-1.7).
func negate(v jsonx.Value) jsonx.Value {
	return jsonx.Float(-number(v, "cannot be negated"))
}

// abs is `if . < 0 then -. else . end`, which keeps a number that is not below 0 as it is, -0 among them.
func abs(v jsonx.Value) jsonx.Value {
	if less(v, zero) {
		return negate(v)
	}
	return v
}

// round is jq's round, C's: half away from zero, as math.Round.
func round(v jsonx.Value) jsonx.Value {
	return jsonx.Float(math.Round(number(v, "number required")))
}

// divide is jq's / of a number by a constant that is not 0, a double's quotient.
func divide(v jsonx.Value, k float64) jsonx.Value {
	return jsonx.Float(number(v, "and number cannot be divided") / k)
}

// repeat is jq 1.7's string times a number: n copies for n at or above 0, its whole part (seen on jq-1.7: "0" * 0.5
// gives ""), and null below 0.
func repeat(s string, n float64) jsonx.Value {
	if n < 0 || math.IsNaN(n) {
		return null
	}
	return str(strings.Repeat(s, int(n)))
}

// runeSlice is jq's `.[i:j]` on a string, by code points, with i and j already inside it.
func runeSlice(s string, i, j int) string {
	r := []rune(s)
	return string(r[i:j])
}

// The DEFS, which JUDGE and RENDER both start with, but for class_table, first_clause, reason_class and class_rank
// (classes.go) and epoch (epoch.go).

// obj is `obj`: v when it is an object, else null.
func obj(v jsonx.Value) jsonx.Value {
	if v.Kind() == jsonx.KindObject {
		return v
	}
	return null
}

// num is `num`: v when it is a number, else null.
func num(v jsonx.Value) jsonx.Value {
	if v.Kind() == jsonx.KindNumber {
		return v
	}
	return null
}

// missing is `missing($what; $v)`: "does not parse" for the string "unreadable", else "is missing".
func missing(what string, v jsonx.Value) string {
	if is(v, "unreadable") {
		return what + " does not parse"
	}
	return what + " is missing"
}

// r3 is `r3`: `. * 1000 | round / 1000`, a double.
func r3(v jsonx.Value) jsonx.Value {
	return divide(round(times(v, 1000)), 1000)
}

// fx is `fx($n)`, a number with n decimals as text, by string slicing:
//
//	(. * pow(10; $n) | round) as $i | ($i | if . < 0 then -. else . end | tostring) as $s
//	| (if ($s | length) <= $n then ("0" * ($n - ($s | length) + 1)) + $s else $s end) as $p
//	| (if $i < 0 then "-" else "" end) + $p[:($p | length) - $n] + "." + $p[($p | length) - $n:]
//
// A -0 is not below 0, so its tostring "-0" is padded as digits: fx(3) gives "0.0-0" and fx(4) "0.00-0", the fault
// 4 committed runs print. With f.ZeroFix, v3's fx reads `round | if . == 0 then 0 else . end`: -0 == 0 holds,
// so a -0 becomes the literal 0 and prints "0.000". A $s in exponent form is sliced as it is, as jq does.
func fx(f Features, v jsonx.Value, n int) string {
	i := round(times(v, math.Pow(10, float64(n))))
	if f.ZeroFix && eq(i, zero) {
		i = zero // ZeroFix: `| if . == 0 then 0 else . end`
	}
	s := tostring(abs(i))
	p := s
	if l := utf8.RuneCountInString(s); l <= n {
		p, _ = repeat("0", float64(n-l+1)).Str()
		p += s
	}
	k := utf8.RuneCountInString(p) - n
	sign := ""
	if less(i, zero) {
		sign = "-"
	}
	return sign + runeSlice(p, 0, k) + "." + runeSlice(p, k, k+n)
}

// rd is `rd($s)`, a d rounded on its scale: 0.000001 of a share, a whole count, else 0.001 ms.
func rd(v, scale jsonx.Value) jsonx.Value {
	switch {
	case is(scale, "share"):
		return divide(round(times(v, 1000000)), 1000000)
	case is(scale, "count"):
		return round(v)
	}
	return divide(round(times(v, 1000)), 1000)
}

// scaleOf is `scale_of($n; $crows)`: the scale of the first compare row named n, unless that is false or
// null, else by the name, whose test is jq's error when the name is not a string.
func scaleOf(n jsonx.Value, crows []jsonx.Value) jsonx.Value {
	var scales []jsonx.Value // [$crows[] | select(.name == $n) | .scale]
	for _, c := range crows {
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

// mm is `mm`: the major.minor of a tag, `split(".")[:2] | join(".")`.
func mm(v jsonx.Value) string {
	parts := split(v, ".")
	return strings.Join(parts[:min(2, len(parts))], ".")
}

// nj is `nj($set; $n)`: the set marks the reading n not_judged. Every threshold is read, so one that is not an
// object or null is jq's error even past the first named n.
func nj(set, n jsonx.Value) bool {
	var marks []jsonx.Value // [($set // {}).thresholds[]? | select(.name == $n) | .no_data]
	for _, t := range eachOpt(field(alt(set, object()), "thresholds")) {
		if eq(field(t, "name"), n) {
			marks = append(marks, field(t, "no_data"))
		}
	}
	return len(marks) > 0 && is(marks[0], "not_judged")
}

// val is `val($s)`: a reading as verdict.md prints it. f is the version's, for fx; so for dval, tval and mval.
func val(f Features, v, scale jsonx.Value) string {
	switch {
	case v.IsNull():
		return "none"
	case is(scale, "share"):
		return fx(f, times(v, 100), 2) + "%"
	case is(scale, "count"):
		return tostring(v)
	}
	return fx(f, v, 3)
}

// dval is `dval($s)`: a d, "+" when above 0.
func dval(f Features, v, scale jsonx.Value) string {
	if v.IsNull() {
		return ""
	}
	sign := ""
	if more(v, zero) {
		sign = "+"
	}
	switch {
	case is(scale, "share"):
		return sign + fx(f, times(v, 100), 4) + " points"
	case is(scale, "count"):
		return sign + tostring(v)
	}
	return sign + fx(f, v, 3)
}

// tval is `tval($s)`: a tolerance.
func tval(f Features, v, scale jsonx.Value) string {
	switch {
	case v.IsNull():
		return ""
	case is(scale, "share"):
		return fx(f, times(v, 100), 2) + " points"
	}
	return fx(f, v, 2)
}

// waveSig is `wave_sig`: each object item of a plan as R7 compares waves, from `.[]?`.
func waveSig(items jsonx.Value) []string {
	var sig []string
	for _, it := range eachOpt(items) {
		if it.Kind() != jsonx.KindObject {
			continue // objects
		}
		var b strings.Builder
		b.WriteString(tostring(field(it, "kind")))
		switch {
		case is(field(it, "arm"), "incumbent"):
			b.WriteString(":incumbent")
		case is(field(it, "kind"), "fault"):
			b.WriteString(":" + tostring(field(it, "fault")))
		}
		b.WriteString(" " + tostring(field(it, "scenario")))
		if r := field(it, "rate"); !r.IsNull() {
			b.WriteString(" " + tostring(r) + "/s")
		}
		if m := field(it, "max_concurrent"); !m.IsNull() {
			b.WriteString(" " + tostring(m))
		}
		if d := field(it, "duration"); !d.IsNull() {
			b.WriteString(" " + tostring(d))
		}
		sig = append(sig, b.String())
	}
	return sig
}

// mval is `mval($s)`: a table cell of R7's comparison in verdict.md.
func mval(f Features, v, scale jsonx.Value) string {
	switch {
	case v.IsNull():
		return "none"
	case is(scale, "ms"):
		return fx(f, v, 3) + " ms"
	}
	return val(f, v, scale)
}
