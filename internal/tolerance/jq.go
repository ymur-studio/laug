package tolerance

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// This file holds what RULE and SHOW run on: jq 1.7's errors, its value operations and the builtins the two programs
// call, each as jq-1.7 does it (TestBuiltinsMatchJQ). They follow internal/judge's defs.go, which this package does
// not import: the judge's identity covers its own sources only, and the tolerance is no part of a verdict. The error
// texts are jq's own, values cut as jq cuts them, so a run that stops prints the line jq printed.

// A jqError is an error RULE's or SHOW's jq raises, such as indexing a number with a name. Nothing catches it: jq
// prints "jq: error (at <stdin>:N): <msg>" and exits 5. In jq 1.7 the alternative operator `//` does not catch one
// (seen on jq-1.7: `(1 | .a) // 2` fails).
type jqError struct{ msg string }

func (e *jqError) Error() string { return e.msg }

// fail raises a jq error. It unwinds to evaluate, the one place that recovers it.
func fail(format string, a ...any) {
	panic(&jqError{msg: fmt.Sprintf(format, a...)})
}

// evaluate runs f as jq runs a program: the jq error f raised, or nil. Any other panic goes on.
func evaluate(f func()) (err *jqError) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*jqError)
			if !ok {
				panic(r)
			}
			err = e
		}
	}()
	f()
	return nil
}

// dump is a value as jq 1.7 names it in an error (src/jv_print.c, jv_dump_string_trunc into 15 bytes): its -c form,
// cut to 11 bytes and "..." when it is longer than 14 (seen on jq-1.7: string ("abcdefghij...) number required).
func dump(v jsonx.Value) string {
	s := v.String()
	if len(s) > 14 {
		return s[:11] + "..."
	}
	return s
}

var null = jsonx.Null()

// constant is a number written in the jq source, a literal, as jq 1.7 reads one.
func constant(tok string) jsonx.Value {
	n, err := jsonx.Literal(tok)
	if err != nil {
		panic(err)
	}
	return jsonx.Num(n)
}

// str is the jq string s; count is a count jq computed, such as length's.
func str(s string) jsonx.Value { return jsonx.Str(s) }
func count(n int) jsonx.Value  { return jsonx.Float(float64(n)) }

// truthy is jq's truth: every value but false and null.
func truthy(v jsonx.Value) bool {
	return v.Kind() != jsonx.KindNull && v.Kind() != jsonx.KindFalse
}

// alt is `a // b` for an a that gives one value and a b that cannot fail.
func alt(a, b jsonx.Value) jsonx.Value {
	if truthy(a) {
		return a
	}
	return b
}

// eq, less and more are jq's ==, < and >: its total order, two literals as decimals.
func eq(a, b jsonx.Value) bool   { return jsonx.Equal(a, b) }
func less(a, b jsonx.Value) bool { return jsonx.Compare(a, b) < 0 }
func more(a, b jsonx.Value) bool { return jsonx.Compare(a, b) > 0 }

// is is v == the string s.
func is(v jsonx.Value, s string) bool {
	t, ok := v.Str()
	return ok && t == s
}

// object builds a jq object from key and value pairs, in order.
func object(kv ...any) jsonx.Value {
	o := jsonx.NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1].(jsonx.Value))
	}
	return jsonx.Obj(o)
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
	fail("%s", cannotIndex(v, name))
	return null
}

// cannotIndex is jq 1.7's error for indexing t by the string k: the key in quotes, as it is, when it is under 30
// bytes, else no key at all (seen on jq-1.7: 29 bytes are quoted, 30 are left out, "é" counting 2).
func cannotIndex(t jsonx.Value, k string) string {
	if len(k) < 30 {
		return "Cannot index " + t.Type() + " with string \"" + k + "\""
	}
	return "Cannot index " + t.Type() + " with string"
}

// path is `.a.b.c` on v, each step a field.
func path(v jsonx.Value, names ...string) jsonx.Value {
	for _, n := range names {
		v = field(v, n)
	}
	return v
}

// at is `.[i]` for a whole i in the jq source.
func at(v jsonx.Value, i int) jsonx.Value { return index(v, count(i)) }

// index is `.[k]` on t, as jq 1.7's jv_get does it: an object by a string; an array by a number, its whole part,
// from the end when below 0, null past either end; an array or a string by {start, end} as a slice; an array by an
// array as the places it starts; and null by a string, a number or an object. Anything else is jq's error, its text
// as jq-1.7 gives it for each pair of kinds (TestBuiltinsMatchJQ). RULE indexes by a value it did not make at
// `.readings[$n]`, where $n is a reading's name as the set gives it.
func index(t, k jsonx.Value) jsonx.Value {
	switch tk, kk := t.Kind(), k.Kind(); {
	case tk == jsonx.KindObject && kk == jsonx.KindString:
		s, _ := k.Str()
		return field(t, s)
	case tk == jsonx.KindArray && kk == jsonx.KindNumber:
		a, _ := t.Arr()
		n, _ := k.Num()
		if math.IsNaN(n.F) || math.Abs(n.F) >= 1<<31 {
			return null
		}
		i := int(n.F) // cut toward 0, as C's (int)
		if i < 0 {
			i += len(a)
		}
		if i < 0 || i >= len(a) {
			return null
		}
		return a[i]
	case (tk == jsonx.KindArray || tk == jsonx.KindString) && kk == jsonx.KindObject:
		o, _ := k.Obj()
		start, ok1 := o.Get("start")
		end, ok2 := o.Get("end")
		if !ok1 || !ok2 {
			fail("Array/string slice indices must be integers")
		}
		return slice(t, start, end)
	case tk == jsonx.KindArray && kk == jsonx.KindArray:
		a, _ := t.Arr()
		b, _ := k.Arr()
		places := []jsonx.Value{}
		for i := range a {
			if len(b) > 0 && i+len(b) <= len(a) && slices.EqualFunc(a[i:i+len(b)], b, eq) {
				places = append(places, count(i))
			}
		}
		return jsonx.Arr(places)
	case tk == jsonx.KindNull && (kk == jsonx.KindString || kk == jsonx.KindNumber || kk == jsonx.KindObject):
		return null
	}
	if s, ok := k.Str(); ok {
		fail("%s", cannotIndex(t, s))
	}
	fail("Cannot index %s with %s", t.Type(), k.Type())
	return null
}

// slice is `.[start:end]` on an array or a string, each bound null or a number (jq 1.7, parse_slice): a bound below
// 0 counts from the end, both are clamped, the start is cut down and the end rounded up to a whole place. On null
// it is null; on anything else jq's error, as `.[{start, end}]` is.
func slice(t, start, end jsonx.Value) jsonx.Value {
	switch t.Kind() {
	case jsonx.KindNull:
		return null
	case jsonx.KindArray, jsonx.KindString:
	default:
		fail("Cannot index %s with object", t.Type())
	}
	l, _ := length(t).Num()
	bound := func(v jsonx.Value, missing float64) float64 {
		if v.IsNull() {
			return missing
		}
		n, ok := v.Num()
		if !ok {
			fail("Array/string slice indices must be integers")
		}
		return n.F
	}
	from, to := bound(start, 0), bound(end, l.F)
	if from < 0 {
		from += l.F
	}
	if to < 0 {
		to += l.F
	}
	from = min(max(from, 0), l.F)
	to = max(min(to, l.F), from)
	i, j := int(from), int(to)
	if to > float64(j) {
		j++
	}
	if s, ok := t.Str(); ok {
		r := []rune(s)
		return str(string(r[i:j]))
	}
	a, _ := t.Arr()
	return jsonx.Arr(slices.Clone(a[i:j]))
}

// sliceTo and sliceFrom are `.[:n]` and `.[n:]` for a whole n in the jq source.
func sliceTo(t jsonx.Value, n int) jsonx.Value   { return slice(t, null, count(n)) }
func sliceFrom(t jsonx.Value, n int) jsonx.Value { return slice(t, count(n), null) }

// members is an array's elements or an object's values, in key order; nothing for anything else.
func members(v jsonx.Value) []jsonx.Value {
	if a, ok := v.Arr(); ok {
		return a
	}
	if o, ok := v.Obj(); ok {
		vs := make([]jsonx.Value, o.Len())
		for i := range vs {
			_, vs[i] = o.Member(i)
		}
		return vs
	}
	return nil
}

// each is `.[]` on v: an array's elements, an object's values. Anything else is jq's error.
func each(v jsonx.Value) []jsonx.Value {
	if k := v.Kind(); k != jsonx.KindArray && k != jsonx.KindObject {
		fail("Cannot iterate over %s (%s)", v.Type(), dump(v))
	}
	return members(v)
}

// eachOpt is `.[]?`: each, or nothing when v is not an array or an object. In jq 1.7 the `?` covers the iteration
// alone, so `.a[]?` on a number still fails at `.a` (seen on jq-1.7).
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
	fail("%s (%s) has no length", v.Type(), dump(v))
	return null
}

// size is length as a Go int, for the arrays RULE and SHOW count.
func size(v jsonx.Value) int {
	n, _ := length(v).Num()
	return int(n.F)
}

// tostring is jq's tostring, and what "\(v)" interpolates: a string as it is, anything else as tojson.
func tostring(v jsonx.Value) string {
	if s, ok := v.Str(); ok {
		return s
	}
	return v.String()
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
			b.WriteString(e.String())
		case jsonx.KindString:
			s, _ := e.Str()
			b.WriteString(s)
		default:
			fail("string (%s) and %s (%s) cannot be added", dump(str(b.String())), e.Type(), dump(e))
		}
	}
	return b.String()
}

// split is jq 1.7's split($sep) on a string, for a sep that is not empty.
func split(v jsonx.Value, sep string) []jsonx.Value {
	s, ok := v.Str()
	if !ok {
		fail("split input and separator must be strings")
	}
	parts := []jsonx.Value{}
	for p := 0; p < len(s); {
		i := strings.Index(s[p:], sep)
		if i < 0 {
			parts = append(parts, str(s[p:]))
			break
		}
		parts = append(parts, str(s[p:p+i]))
		p += i + len(sep)
		if p == len(s) {
			parts = append(parts, str(""))
		}
	}
	return parts
}

// A pattern is one of the tolerance's test() patterns: jq's text and Go's. jq's `$` also matches before one final
// newline, where Go's matches at the end only, so a pattern that ends in `$` ends in `\n?\z` here (internal/judge's
// match.go, TestRegexMatchesJQ there and here).
type pattern struct {
	jq string
	re *regexp.Regexp
}

var (
	// overMsEnd and msEnd are scaleOf's, and rRule the rule names R1 to R6 that refusals reads.
	overMsEnd = pattern{`_over_[0-9]+ms$`, regexp.MustCompile(`_over_[0-9]+ms\n?\z`)}
	msEnd     = pattern{`_ms$`, regexp.MustCompile(`_ms\n?\z`)}
	rRule     = pattern{`^R[1-6]$`, regexp.MustCompile(`^R[1-6]\n?\z`)}
)

// patterns is every test() pattern above, for TestRegexMatchesJQ.
var patterns = []pattern{overMsEnd, msEnd, rRule}

// test is jq's test($re) on v: true when re matches somewhere in v. A v that is not a string is jq's error.
func test(v jsonx.Value, p pattern) bool {
	s, ok := v.Str()
	if !ok {
		fail("%s (%s) cannot be matched, as it is not a string", v.Type(), dump(v))
	}
	return p.re.MatchString(s)
}

// An entry is one of to_entries' {key, value}.
type entry struct{ key, value jsonx.Value }

// toEntries is jq 1.7's to_entries, `[keys_unsorted[] as $k | {key: $k, value: .[$k]}]`: an object's members in key
// order, an array's elements by place. Anything else has no keys: jq's error.
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
	fail("%s (%s) has no keys", v.Type(), dump(v))
	return nil
}

// groupBy is `group_by(f)` on vs: a stable sort by f's value in jq's order, then the runs of equal values.
func groupBy(vs []jsonx.Value, f func(jsonx.Value) jsonx.Value) [][]jsonx.Value {
	type keyed struct{ k, v jsonx.Value }
	ks := make([]keyed, len(vs))
	for i, v := range vs {
		ks[i] = keyed{f(v), v}
	}
	slices.SortStableFunc(ks, func(a, b keyed) int { return jsonx.Compare(a.k, b.k) })
	var groups [][]jsonx.Value
	for i, kv := range ks {
		if i == 0 || !eq(ks[i-1].k, kv.k) {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], kv.v)
	}
	return groups
}

// unique is jq's unique: sorted in jq's order, the first of each run of equals.
func unique(vs []jsonx.Value) []jsonx.Value {
	var out []jsonx.Value
	for _, g := range groupBy(vs, func(v jsonx.Value) jsonx.Value { return v }) {
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

// add is jq 1.7's add, `reduce .[] as $x (null; . + $x)`: the first value as it is, then each added, in order.
func add(vs []jsonx.Value) jsonx.Value {
	acc := null
	for _, v := range vs {
		acc = plus(acc, v)
	}
	return acc
}

// number is v's double, or jq's error naming what op could not take.
func number(v jsonx.Value, op string) float64 {
	n, ok := v.Num()
	if !ok {
		fail("%s (%s) %s", v.Type(), dump(v), op)
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
	fail("%s (%s) and %s (%s) cannot be added", a.Type(), dump(a), b.Type(), dump(b))
	return null
}

// minus is jq's -: two numbers as doubles; two arrays, a's elements that equal none of b's, in order. Any other
// pair is jq's error.
func minus(a, b jsonx.Value) jsonx.Value {
	if a.Kind() == jsonx.KindArray && b.Kind() == jsonx.KindArray {
		x, _ := a.Arr()
		y, _ := b.Arr()
		out := []jsonx.Value{}
		for _, e := range x {
			if !slices.ContainsFunc(y, func(f jsonx.Value) bool { return eq(e, f) }) {
				out = append(out, e)
			}
		}
		return jsonx.Arr(out)
	}
	if a.Kind() != jsonx.KindNumber || b.Kind() != jsonx.KindNumber {
		fail("%s (%s) and %s (%s) cannot be subtracted", a.Type(), dump(a), b.Type(), dump(b))
	}
	x, _ := a.Num()
	y, _ := b.Num()
	return jsonx.Float(x.F - y.F)
}

// times is jq's * of a value by a number written in the jq source, k. Two numbers give a double's product, which jq
// rounds. The float64() is not a no-op: the Go spec lets a compiler fuse a product with a later add, across statements
// too, unless an explicit conversion rounds the product first (the Go spec, Floating-point operators); Go's arm64
// compiler fuses and its amd64 compiler does not. internal/judge's TestNoFusableExpressions holds every float product
// here to this form, and only a run on arm64 checks the result. A string by k is k copies of it, its whole part,
// none at 0 and null below (seen on jq-1.7: "ab" * 2.7 gives "abab", "x" * -1 null); anything else is jq's error.
func times(v, k jsonx.Value) jsonx.Value {
	n, _ := k.Num()
	switch v.Kind() {
	case jsonx.KindNumber:
		x, _ := v.Num()
		return jsonx.Float(float64(x.F * n.F))
	case jsonx.KindString:
		s, _ := v.Str()
		if n.F < 0 || math.IsNaN(n.F) {
			return null
		}
		return str(strings.Repeat(s, int(n.F)))
	}
	fail("%s (%s) and %s (%s) cannot be multiplied", v.Type(), dump(v), k.Type(), dump(k))
	return null
}

// divide is jq's / of two numbers, a double's quotient; 0 as the divisor is jq's error, as is anything but numbers.
// Neither program divides a string.
func divide(a, b jsonx.Value) jsonx.Value {
	x, ok1 := a.Num()
	y, ok2 := b.Num()
	if !ok1 || !ok2 {
		fail("%s (%s) and %s (%s) cannot be divided", a.Type(), dump(a), b.Type(), dump(b))
	}
	if y.F == 0 {
		fail("%s (%s) and %s (%s) cannot be divided because the divisor is zero", a.Type(), dump(a), b.Type(),
			dump(b))
	}
	return jsonx.Float(x.F / y.F)
}

// round, ceil, fabs and sqrt are jq's math functions, C's: round half away from zero.
func round(v jsonx.Value) jsonx.Value { return jsonx.Float(math.Round(number(v, "number required"))) }
func ceil(v jsonx.Value) jsonx.Value  { return jsonx.Float(math.Ceil(number(v, "number required"))) }
func fabs(v jsonx.Value) jsonx.Value  { return jsonx.Float(math.Abs(number(v, "number required"))) }
func sqrt(v jsonx.Value) jsonx.Value  { return jsonx.Float(math.Sqrt(number(v, "number required"))) }
