package judge

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// defsNames are the defs of JUDGE's DEFS: a row of TestBuiltins that calls one needs the DEFS, and jq alone does not
// run it.
var defsNames = regexp.MustCompile(`\b(class_rank|class_table|dval|epoch|first_clause|fx|missing|mm|mval|nj|num|obj|` +
	`r3|rd|reason_class|scale_of|tval|val|wave_sig)\b`)

// TestBuiltins holds the jq builtins and the DEFS the rules call to jq 1.7's answers: each row is a jq expression,
// its -c output after v2's DEFS with -n (or "error"), and the Go that ports it. Go must give the row. With jq 1.7.x
// present, jq -nc must give it too on each row that calls no def of the DEFS (defsNames). A literal keeps its text
// through tostring, tojson, interpolation and join; arithmetic makes a computed number in dtoa form; `//` does not
// catch an error.
func TestBuiltins(t *testing.T) {
	v := func(text string) jsonx.Value {
		x, err := jsonx.Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		return x
	}
	s := str
	b := jsonx.Bool
	neg0 := func() jsonx.Value { return negate(zero) }
	// The rows run v2's DEFS, so fx and its callers run with v2's features; TestZeroFix runs them under each version.
	f := mustNamed(t, "v2").Features
	opt := func(x jsonx.Value, ok bool) []jsonx.Value { // the outputs of a `?`: none on an error
		if !ok {
			return nil
		}
		return []jsonx.Value{x}
	}
	scales := func(name, crows string) jsonx.Value { a, _ := v(crows).Arr(); return scaleOf(v(name), a) }
	cases := []struct {
		expr string
		want string
		go_  func() jsonx.Value
	}{
		// tostring, tojson and interpolation: a literal's text, a computed number's dtoa form.
		{`1.50 | tostring`, `"1.50"`, func() jsonx.Value { return s(tostring(v("1.50"))) }},
		{`1.50 + 0 | tostring`, `"1.5"`, func() jsonx.Value { return s(tostring(plus(v("1.50"), zero))) }},
		{`1e-05 | tostring`, `"0.00001"`, func() jsonx.Value { return s(tostring(v("1e-05"))) }},
		{`1e-05 * 1 | tostring`, `"1e-05"`, func() jsonx.Value { return s(tostring(times(v("1e-05"), 1))) }},
		{`0 | -. | tostring`, `"-0"`, func() jsonx.Value { return s(tostring(neg0())) }},
		{`1.000 | -.`, `-1`, func() jsonx.Value { return negate(v("1.000")) }},
		{`"\(1.0E2) \(null) \(true) \("s") \({"b":1,"a":"x\u007f<&>"})"`, `"1.0E+2 null true s {\"b\":1,\"a\":\"x\\u007f<&>\"}"`,
			func() jsonx.Value {
				return s(tostring(v("1.0E2")) + " " + tostring(null) + " " + tostring(b(true)) + " " + tostring(s("s")) +
					" " + tostring(v(`{"b":1,"a":"x\u007f<&>"}`)))
			}},
		{`"a " | tojson`, `"\"a` + " " + `\""`, func() jsonx.Value { return s(tojson(v(`"a "`))) }},
		// join: null as "", a number or a boolean as tojson, an object or an array an error.
		{`[1.50, null, "a"] | join(", ")`, `"1.50, , a"`, func() jsonx.Value { return s(join(v(`[1.50, null, "a"]`), ", ")) }},
		{`[1.0e2, false] | join(",")`, `"1.0E+2,false"`, func() jsonx.Value { return s(join(v(`[1.0e2, false]`), ",")) }},
		{`[] | join(",")`, `""`, func() jsonx.Value { return s(join(v(`[]`), ",")) }},
		{`{"a":"x","b":1} | join(",")`, `"x,1"`, func() jsonx.Value { return s(join(v(`{"a":"x","b":1}`), ",")) }},
		{`[{"b":2}] | join(", ")`, "error", func() jsonx.Value { return s(join(v(`[{"b":2}]`), ", ")) }},
		{`["a",[1]] | join(",")`, "error", func() jsonx.Value { return s(join(v(`["a",[1]]`), ",")) }},
		{`"abc" | join(",")`, "error", func() jsonx.Value { return s(join(v(`"abc"`), ",")) }},
		{`null | join(",")`, "error", func() jsonx.Value { return s(join(null, ",")) }},
		// split, first_clause and mm.
		{`"" | split("; ")`, `[]`, func() jsonx.Value { return strs(split(s(""), "; ")) }},
		{`"a; " | split("; ")`, `["a",""]`, func() jsonx.Value { return strs(split(s("a; "), "; ")) }},
		{`"a;  b; c" | split("; ")`, `["a"," b","c"]`, func() jsonx.Value { return strs(split(s("a;  b; c"), "; ")) }},
		{`5 | split(".")`, "error", func() jsonx.Value { return strs(split(v("5"), ".")) }},
		{`"" | first_clause`, `""`, func() jsonx.Value { return s(firstClause(s(""))) }},
		{`"a; b" | first_clause`, `"a"`, func() jsonx.Value { return s(firstClause(s("a; b"))) }},
		{`"; b" | first_clause`, `""`, func() jsonx.Value { return s(firstClause(s("; b"))) }},
		{`5 | first_clause`, `""`, func() jsonx.Value { return s(firstClause(v("5"))) }},
		{`"1.31.0" | mm`, `"1.31"`, func() jsonx.Value { return s(mm(s("1.31.0"))) }},
		{`"1" | mm`, `"1"`, func() jsonx.Value { return s(mm(s("1"))) }},
		{`"" | mm`, `""`, func() jsonx.Value { return s(mm(s(""))) }},
		// length, max, unique, group_by and array subtraction, numbers by jq's order.
		{`true | length`, "error", func() jsonx.Value { return length(b(true)) }},
		{`-5.5 | length`, `5.5`, func() jsonx.Value { return length(v("-5.5")) }},
		{`null | length`, `0`, func() jsonx.Value { return length(null) }},
		{`"héllo" | length`, `5`, func() jsonx.Value { return length(s("héllo")) }},
		{`[2.0, 2] | max`, `2`, func() jsonx.Value { a, _ := v(`[2.0, 2]`).Arr(); return maxOf(a) }},
		{`[2, 2.0] | max`, `2.0`, func() jsonx.Value { a, _ := v(`[2, 2.0]`).Arr(); return maxOf(a) }},
		{`[] | max`, `null`, func() jsonx.Value { return maxOf(nil) }},
		{`[1.0, 1, "a", 1.00] | unique`, `[1.0,"a"]`,
			func() jsonx.Value { a, _ := v(`[1.0, 1, "a", 1.00]`).Arr(); return jsonx.Arr(unique(a)) }},
		{`[1, null, "b", 1.0, "a", null] | group_by(.)`, `[[null,null],[1,1.0],["a"],["b"]]`, func() jsonx.Value {
			a, _ := v(`[1, null, "b", 1.0, "a", null]`).Arr()
			var gs []jsonx.Value
			for _, g := range groupBy(a) {
				gs = append(gs, jsonx.Arr(g))
			}
			return jsonx.Arr(gs)
		}},
		{`[1,2,1,3] - [1]`, `[2,3]`, func() jsonx.Value { return minus(v(`[1,2,1,3]`), v(`[1]`)) }},
		{`[1.0] - [1]`, `[]`, func() jsonx.Value { return minus(v(`[1.0]`), v(`[1]`)) }},
		{`[0.1] - [0.10000000000000000555]`, `[0.1]`,
			func() jsonx.Value { return minus(v(`[0.1]`), v(`[0.10000000000000000555]`)) }},
		// +, -, *, round, has.
		{`null + "a"`, `"a"`, func() jsonx.Value { return plus(null, s("a")) }},
		{`"a" + 1`, "error", func() jsonx.Value { return plus(s("a"), one) }},
		{`"a" - 1`, "error", func() jsonx.Value { return minus(s("a"), one) }},
		{`2.5 | round`, `3`, func() jsonx.Value { return round(v("2.5")) }},
		{`-0.5 | round`, `-1`, func() jsonx.Value { return round(v("-0.5")) }},
		{`-0.4 | round`, `-0`, func() jsonx.Value { return round(v("-0.4")) }},
		{`"x" | round`, "error", func() jsonx.Value { return round(s("x")) }},
		{`"0" * 0`, `""`, func() jsonx.Value { return repeat("0", 0) }},
		{`"0" * 0.5`, `""`, func() jsonx.Value { return repeat("0", 0.5) }},
		{`"0" * 1.5`, `"0"`, func() jsonx.Value { return repeat("0", 1.5) }},
		{`"0" * -1`, `null`, func() jsonx.Value { return repeat("0", -1) }},
		{`null | has("a")`, `false`, func() jsonx.Value { return b(hasKey(null, "a")) }},
		{`{"a":null} | has("a")`, `true`, func() jsonx.Value { return b(hasKey(v(`{"a":null}`), "a")) }},
		{`[1] | has("a")`, "error", func() jsonx.Value { return b(hasKey(v(`[1]`), "a")) }},
		{`5 | startswith("a")`, "error", func() jsonx.Value { return b(startswith(v("5"), "a")) }},
		// Indexing, as jv_get does it.
		{`"abc" | .a`, "error", func() jsonx.Value { return field(s("abc"), "a") }},
		{`null | .a.b`, `null`, func() jsonx.Value { return path(null, "a", "b") }},
		{`[1,2,3] | .[1.5]`, `2`, func() jsonx.Value { return index(v(`[1,2,3]`), v("1.5")) }},
		{`[1,2,3] | .[-1]`, `3`, func() jsonx.Value { return index(v(`[1,2,3]`), v("-1")) }},
		{`[1,2,3] | .[-4]`, `null`, func() jsonx.Value { return index(v(`[1,2,3]`), v("-4")) }},
		{`[1,2,3] | .[{"start":0.5,"end":1.5}]`, `[1,2]`,
			func() jsonx.Value { return index(v(`[1,2,3]`), v(`{"start":0.5,"end":1.5}`)) }},
		{`[1,2,3] | .[{"start":null,"end":-1}]`, `[1,2]`,
			func() jsonx.Value { return index(v(`[1,2,3]`), v(`{"start":null,"end":-1}`)) }},
		{`[1,2,3] | .[{}]`, "error", func() jsonx.Value { return index(v(`[1,2,3]`), v(`{}`)) }},
		{`"abc" | .[{"start":1,"end":null}]`, `"bc"`,
			func() jsonx.Value { return index(s("abc"), v(`{"start":1,"end":null}`)) }},
		{`[1,2,1,2] | .[[1,2]]`, `[0,2]`, func() jsonx.Value { return index(v(`[1,2,1,2]`), v(`[1,2]`)) }},
		{`[1,2] | .[[3]]`, `[]`, func() jsonx.Value { return index(v(`[1,2]`), v(`[3]`)) }},
		{`[1,2] | .[[]]`, `[]`, func() jsonx.Value { return index(v(`[1,2]`), v(`[]`)) }},
		{`null | .[null]`, "error", func() jsonx.Value { return index(null, null) }},
		{`null | .[{}]`, `null`, func() jsonx.Value { return index(null, v(`{}`)) }},
		{`null | .[5]`, `null`, func() jsonx.Value { return index(null, v("5")) }},
		{`{} | .[1]`, "error", func() jsonx.Value { return index(v(`{}`), one) }},
		{`[[1] | .["a"]?]`, `[]`, func() jsonx.Value { return jsonx.Arr(opt(indexOpt(v(`[1]`), s("a")))) }},
		{`[null | .["a"]?]`, `[null]`, func() jsonx.Value { return jsonx.Arr(opt(indexOpt(null, s("a")))) }},
		{`["abc" | .[]?]`, `[]`, func() jsonx.Value { return jsonx.Arr(eachOpt(s("abc"))) }},
		{`["abc" | .[]]`, "error", func() jsonx.Value { return jsonx.Arr(each(s("abc"))) }},
		{`{"b":1,"a":2} | keys`, `["a","b"]`, func() jsonx.Value { return jsonx.Arr(keys(v(`{"b":1,"a":2}`))) }},
		{`{"b":1,"a":2} | to_entries`, `[{"key":"b","value":1},{"key":"a","value":2}]`, func() jsonx.Value {
			var es []jsonx.Value
			for _, e := range toEntries(v(`{"b":1,"a":2}`)) {
				es = append(es, object("key", e.key, "value", e.value))
			}
			return jsonx.Arr(es)
		}},
		{`{"a":1,"b":2,"c":3} | del(.b)`, `{"a":1,"c":3}`, func() jsonx.Value { return del(v(`{"a":1,"b":2,"c":3}`), "b") }},
		// The alternative operator keeps 0 and "", and does not catch an error on its left.
		{`0 // 2`, `0`, func() jsonx.Value { return alt(zero, two) }},
		{`false // 2`, `2`, func() jsonx.Value { return alt(b(false), two) }},
		{`(1 | .a) // 2`, "error", func() jsonx.Value { return alt(field(one, "a"), two) }},
		// The DEFS.
		{`5 | obj`, `null`, func() jsonx.Value { return obj(v("5")) }},
		{`"5" | num`, `null`, func() jsonx.Value { return num(s("5")) }},
		{`missing("x"; "unreadable")`, `"x does not parse"`, func() jsonx.Value { return s(missing("x", s("unreadable"))) }},
		{`missing("x"; 5)`, `"x is missing"`, func() jsonx.Value { return s(missing("x", v("5"))) }},
		{`1791987190.123456 - 1791986400 | r3`, `790.123`,
			func() jsonx.Value { return r3(minus(v("1791987190.123456"), v("1791986400"))) }},
		{`0 | -. | fx(3)`, `"0.0-0"`, func() jsonx.Value { return s(fx(f, neg0(), 3)) }},
		{`0 | -. | . * 100 | fx(4)`, `"0.00-0"`, func() jsonx.Value { return s(fx(f, times(neg0(), 100), 4)) }},
		{`-0.2 | fx(3)`, `"-0.200"`, func() jsonx.Value { return s(fx(f, v("-0.2"), 3)) }},
		{`4.5e-05 * 100 | fx(4)`, `"0.0045"`, func() jsonx.Value { return s(fx(f, times(v("4.5e-05"), 100), 4)) }},
		{`1e17 | fx(3)`, `"1e.+20"`, func() jsonx.Value { return s(fx(f, v("1e17"), 3)) }},
		{`19.7442 - 19.744218057975548 | rd("ms")`, `-0`,
			func() jsonx.Value { return rd(minus(v("19.7442"), v("19.744218057975548")), s("ms")) }},
		{`0.086145 - 0.0861 | rd("share")`, `4.5e-05`,
			func() jsonx.Value { return rd(minus(v("0.086145"), v("0.0861")), s("share")) }},
		{`5880.5 | rd("count")`, `5881`, func() jsonx.Value { return rd(v("5880.5"), s("count")) }},
		{`2.0005 | rd(5)`, `2.001`, func() jsonx.Value { return rd(v("2.0005"), v("5")) }},
		{`scale_of("x_over_10ms"; [])`, `"share"`, func() jsonx.Value { return scales(`"x_over_10ms"`, `[]`) }},
		{`scale_of("p99_ms"; [])`, `"ms"`, func() jsonx.Value { return scales(`"p99_ms"`, `[]`) }},
		{`scale_of("workflows_completed"; [])`, `"count"`, func() jsonx.Value { return scales(`"workflows_completed"`, `[]`) }},
		{`scale_of("x"; [])`, `"share"`, func() jsonx.Value { return scales(`"x"`, `[]`) }},
		{`scale_of("p99_ms"; [{"name":"p99_ms","scale":"share"}])`, `"share"`,
			func() jsonx.Value { return scales(`"p99_ms"`, `[{"name":"p99_ms","scale":"share"}]`) }},
		{`scale_of("p99_ms"; [{"name":"p99_ms","scale":false}])`, `"ms"`,
			func() jsonx.Value { return scales(`"p99_ms"`, `[{"name":"p99_ms","scale":false}]`) }},
		{`scale_of(5; [{"name":5,"scale":"ms"}])`, `"ms"`,
			func() jsonx.Value { return scales(`5`, `[{"name":5,"scale":"ms"}]`) }},
		{`scale_of(5; [])`, "error", func() jsonx.Value { return scales(`5`, `[]`) }},
		{`nj({"thresholds":[{"name":"a","no_data":"not_judged"}]}; "a")`, `true`,
			func() jsonx.Value { return b(nj(v(`{"thresholds":[{"name":"a","no_data":"not_judged"}]}`), s("a"))) }},
		{`nj({"thresholds":[{"name":"a","no_data":"exceeds"},{"name":"a","no_data":"not_judged"}]}; "a")`, `false`,
			func() jsonx.Value {
				return b(nj(v(`{"thresholds":[{"name":"a","no_data":"exceeds"},{"name":"a","no_data":"not_judged"}]}`), s("a")))
			}},
		{`nj(null; "a")`, `false`, func() jsonx.Value { return b(nj(null, s("a"))) }},
		{`nj({"thresholds":[{"name":"a","no_data":"not_judged"},5]}; "a")`, "error",
			func() jsonx.Value { return b(nj(v(`{"thresholds":[{"name":"a","no_data":"not_judged"},5]}`), s("a"))) }},
		{`0.0861 | val("share")`, `"8.61%"`, func() jsonx.Value { return s(val(f, v("0.0861"), s("share"))) }},
		{`5881 | val("count")`, `"5881"`, func() jsonx.Value { return s(val(f, v("5881"), s("count"))) }},
		{`null | val("ms")`, `"none"`, func() jsonx.Value { return s(val(f, null, s("ms"))) }},
		{`49.79083694083693 | val("ms")`, `"49.791"`,
			func() jsonx.Value { return s(val(f, v("49.79083694083693"), s("ms"))) }},
		{`0.0139 | dval("share")`, `"+1.3900 points"`, func() jsonx.Value { return s(dval(f, v("0.0139"), s("share"))) }},
		{`-0.2 | dval("ms")`, `"-0.200"`, func() jsonx.Value { return s(dval(f, v("-0.2"), s("ms"))) }},
		{`0 | dval("ms")`, `"0.000"`, func() jsonx.Value { return s(dval(f, zero, s("ms"))) }},
		{`0 | -. | dval("ms")`, `"0.0-0"`, func() jsonx.Value { return s(dval(f, neg0(), s("ms"))) }},
		{`3 | dval("count")`, `"+3"`, func() jsonx.Value { return s(dval(f, three, s("count"))) }},
		{`null | dval("ms")`, `""`, func() jsonx.Value { return s(dval(f, null, s("ms"))) }},
		{`0.01 | tval("share")`, `"1.00 points"`, func() jsonx.Value { return s(tval(f, v("0.01"), s("share"))) }},
		{`1 | tval("ms")`, `"1.00"`, func() jsonx.Value { return s(tval(f, one, s("ms"))) }},
		{`null | tval("ms")`, `""`, func() jsonx.Value { return s(tval(f, null, s("ms"))) }},
		{`10 | mval("ms")`, `"10.000 ms"`, func() jsonx.Value { return s(mval(f, v("10"), s("ms"))) }},
		{`0.01 | mval("share")`, `"1.00%"`, func() jsonx.Value { return s(mval(f, v("0.01"), s("share"))) }},
		{`null | mval("ms")`, `"none"`, func() jsonx.Value { return s(mval(f, null, s("ms"))) }},
		{`[{"kind":"load","arm":"incumbent","scenario":"s","rate":20,"max_concurrent":50,"duration":"5m"},` +
			`{"kind":"fault","fault":"f","scenario":"s"},5,{"kind":"load","scenario":"s","rate":0.50}] | wave_sig`,
			`["load:incumbent s 20/s 50 5m","fault:f s","load s 0.50/s"]`, func() jsonx.Value {
				return strs(waveSig(v(`[{"kind":"load","arm":"incumbent","scenario":"s","rate":20,"max_concurrent":50,` +
					`"duration":"5m"},{"kind":"fault","fault":"f","scenario":"s"},5,{"kind":"load","scenario":"s","rate":0.50}]`)))
			}},
		{`"x" | wave_sig`, `[]`, func() jsonx.Value { return strs(waveSig(s("x"))) }},
		{`"invalid" | class_rank`, `6`, func() jsonx.Value { return count(classRank("invalid")) }},
		{`"x" | class_rank`, `9`, func() jsonx.Value { return count(classRank("x")) }},
	}
	jq, ran := maybeJQ(t), 0
	for _, c := range cases {
		got, ok := try(c.go_)
		gotText := "error"
		if ok {
			gotText = tojson(got)
		}
		if gotText != c.want {
			t.Errorf("%s: Go gives %s, want %s", c.expr, gotText, c.want)
		}
		if jq == "" || defsNames.MatchString(c.expr) {
			continue
		}
		ran++
		out, ok := jqRun(jq, nil, "-nc", c.expr)
		jqText := "error"
		if ok {
			jqText = strings.TrimSuffix(string(out), "\n")
		} else if strings.Contains(string(out), "is not defined") {
			t.Errorf("%s: jq says %s; add the def to defsNames", c.expr, strings.TrimSpace(string(out)))
			continue
		}
		if jqText != c.want {
			t.Errorf("%s: jq gives %s, the table says %s", c.expr, jqText, c.want)
		}
	}
	if jq != "" {
		t.Logf("%d of %d rows run by jq alone", ran, len(cases))
	}
}
