package jsonx

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// The helpers below follow the judge's DEFS on jsonx's numbers, only so that TestNumberForms can show each
// committed form whole. internal/judge holds the port.

// lit is the literal jq reads from s.
func lit(t *testing.T, s string) Number {
	t.Helper()
	n, err := Literal(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// readBack is n as verdict.md's renderer sees it: the judge renders from the verdict.json bytes it wrote, read back,
// so every number is a literal of its printed text (internal/judge's readBack).
func readBack(t *testing.T, n Number) Number { return lit(t, n.String()) }

// rd is `rd($s)` and r3 is `r3`, internal/judge's rd and r3: jq's round is C's, half away from zero.
func rd(scale string, x float64) Number {
	switch scale {
	case "share":
		return Computed(math.Round(x*1000000) / 1000000)
	case "count":
		return Computed(math.Round(x))
	}
	return Computed(math.Round(x*1000) / 1000)
}

func r3(x float64) Number { return Computed(math.Round(x*1000) / 1000) }

// fx is `fx($n)` (v2's, internal/judge), negative zero fault and all: tostring of -0 is "-0", and -0 < 0 is false.
func fx(n int, x Number) string {
	i := math.Round(x.F * math.Pow(10, float64(n)))
	a := i
	if i < 0 {
		a = -i
	}
	s := Computed(a).String()
	p := s
	if len(s) <= n {
		p = strings.Repeat("0", n-len(s)+1) + s
	}
	sign := ""
	if i < 0 {
		sign = "-"
	}
	return sign + p[:len(p)-n] + "." + p[len(p)-n:]
}

// val, dval and mval are the judge's table cells (internal/judge); `. > 0` compares two literals, as decimals.
func val(scale string, x Number) string {
	switch scale {
	case "share":
		return fx(2, Computed(x.F*100)) + "%"
	case "count":
		return x.String()
	}
	return fx(3, x)
}

func dval(t *testing.T, scale string, x Number) string {
	plus := ""
	if CompareNumbers(x, lit(t, "0")) > 0 {
		plus = "+"
	}
	switch scale {
	case "share":
		return plus + fx(4, Computed(x.F*100)) + " points"
	case "count":
		return plus + x.String()
	}
	return plus + fx(3, x)
}

func mval(scale string, x Number) string {
	if scale == "ms" {
		return fx(3, x) + " ms"
	}
	return val(scale, x)
}

// TestNumberForms holds 28 committed numbers, each with the literals it came from, what jq did to them, the JSON
// text and, where verdict.md prints one, the md text. A comparison row's d is rd of release less incumbent, a
// computed number in dtoa form, and md renders it read back as a literal; a reading passes through as a literal,
// trailing zeros and all; elapsed_s is r3 of t_post less t0. Rows 19 and 20 are dev host runs, copied to
// internal/rejudge/testdata/runs.
func TestNumberForms(t *testing.T) {
	d := func(scale, release, incumbent string) func(t *testing.T) (string, string) {
		return func(t *testing.T) (string, string) {
			n := rd(scale, lit(t, release).F-lit(t, incumbent).F)
			return n.String(), dval(t, scale, readBack(t, n))
		}
	}
	// reading is a load verdict's reading and the value load/verdict.sh printed from it with printf: both pass
	// through as literals, and md prints the reading as a comparison row's cell, when the run has one.
	reading := func(scale, r, value string, md bool) func(t *testing.T) (string, string) {
		return func(t *testing.T) (string, string) {
			json := lit(t, r).String() + " " + lit(t, value).String()
			if !md {
				return json, ""
			}
			return json, mval(scale, lit(t, r))
		}
	}
	elapsed := func(t0, tPost string) func(t *testing.T) (string, string) {
		return func(t *testing.T) (string, string) {
			n := r3(lit(t, tPost).F - lit(t, t0).F)
			return n.String(), readBack(t, n).String()
		}
	}
	rows := []struct {
		name     string
		form     func(t *testing.T) (json, md string)
		json, md string
	}{
		{"1 g20261017-191109/verdict.json:298 d, md:24", d("share", "0.002844827586206966", "0.002844827586207077"),
			"-0", "0.00-0 points"},
		{"2 g20261017-192202/verdict.json:258 d, md:20", d("ms", "9.988931312087102", "9.989077172813735"),
			"-0", "0.0-0"},
		{"3 g20261018-091444/verdict.json:302 d, md:24", d("share", "0.00398271332937894", "0.003983050847457648"),
			"-0", "0.00-0 points"},
		{"4 g20261018-171208/verdict.json:272 d, md:21", d("ms", "49.403638588189786", "49.40394907500849"),
			"-0", "0.0-0"},
		{"5 g20261018-171208/verdict.json:252 d", d("share", "0.030732202937385655", "0.030742437782848353"),
			"-1e-05", "-0.0010 points"},
		{"6 g20261018-075357/verdict.json:232 d", d("share", "0.009265956266924724", "0.009220784988040798"),
			"4.5e-05", "+0.0045 points"},
		{"7 g20261018-062701/verdict.json:312 d", d("share", "0.0014602650360161507", "0.0013610535407243551"),
			"9.9e-05", "+0.0099 points"},
		{"8 g20261017-184922/verdict.json:308 d", d("share", "0.0014206789261920205", "0.0013186699242514477"),
			"0.000102", "+0.0102 points"},
		{"9 g20261018-091444/verdict.json:242 d", d("share", "0.010582227308890135", "0.010581025330216876"),
			"1e-06", "+0.0001 points"},
		{"10 g20261018-075357/verdict.json:262 d", d("ms", "9.995609304874648", "9.995375075734714"),
			"0", "0.000"},
		{"11 g20261018-075357/verdict.json:212 d", d("ms", "16.886543382355622", "17.086628703175624"),
			"-0.2", "-0.200"},
		{"12 g20261019-002158/verdict.json:212 d, 7e-6 from a tie", d("ms", "17.176975984671248", "17.248475977934298"),
			"-0.071", "-0.071"},
		{"13 g20261018-182815/verdict.json:272 d, near a tie", d("ms", "49.43775820290805", "49.430257268606894"),
			"0.008", "+0.008"},
		{"14 g20261017-221200/verdict.json:254 d, near a tie", d("share", "0.22496934105763333", "0.02897284121025301"),
			"0.195996", "+19.5996 points"},
		{"15 g20261017-182733 steady p99 from below", reading("ms", "9.999999999999998", "10.000", true),
			"9.999999999999998 10.000", "10.000 ms"},
		{"16 g20261017-191109 steady p99 from above", reading("ms", "10.0000000000002", "10.000", true),
			"10.0000000000002 10.000", "10.000 ms"},
		{"17 g20261015-151214 s3 p99 near a tie", reading("ms", "14.062499999999961", "14.062", false),
			"14.062499999999961 14.062", ""},
		{"18 g20261015-151214 s3 p95 at 50 ms", reading("ms", "49.99999999999999", "50.000", false),
			"49.99999999999999 50.000", ""},
		{"19 g20261014-215905 steady value in a reason", func(t *testing.T) (string, string) {
			return fmt.Sprintf("%s EXCEEDS", lit(t, "28850.000")), ""
		}, "28850.000 EXCEEDS", ""},
		{"20 g20261014-220820 steady int reading, value in a reason", func(t *testing.T) (string, string) {
			return lit(t, "48590").String() + " " + fmt.Sprintf("%s EXCEEDS", lit(t, "48590.000")), ""
		}, "48590 48590.000 EXCEEDS", ""},
		{"21 g20261017-212148 steady share of 1", reading("share", "1", "1.00000", true),
			"1 1.00000", "100.00%"},
		{"22 g20261017-182733 steady share at a percent edge", reading("share", "0.009999999999999898", "0.01000", true),
			"0.009999999999999898 0.01000", "1.00%"},
		{"23 g20261018-075357/verdict.json:300 release, md:24", reading("share", "0.0037053227087815443", "0.0037053227087815443", true),
			"0.0037053227087815443 0.0037053227087815443", "0.37%"},
		{"24 g20261018-075357 steady service_error_rate value", reading("share", "0", "0.000", false),
			"0 0.000", ""},
		{"25 g20261015-144517/verdict.json:53 t_post.wall", func(t *testing.T) (string, string) {
			return lit(t, "1792076002.490").String(), ""
		}, "1792076002.490", ""},
		{"26 g20261015-150315/verdict.json:58 elapsed_s.mono, md:3", elapsed("8476.33", "8961.33"), "485", "485"},
		{"26 g20261015-150315/verdict.json:57 elapsed_s.wall", elapsed("1792076595.641", "1792077080.637"),
			"484.996", "484.996"},
		{"27 g20261017-211051/verdict.json:68 elapsed_s.wall", elapsed("1792271451.397", "1792272042.497"),
			"591.1", "591.1"},
		{"28 g20261018-075357 steady workflows_completed limit", func(t *testing.T) (string, string) {
			return string(AppendString(nil, ">= 5700")), ""
		}, `">= 5700"`, ""},
	}
	for _, r := range rows {
		json, md := r.form(t)
		if json != r.json || md != r.md {
			t.Errorf("row %s: JSON %q, md %q; committed %q, %q", r.name, json, md, r.json, r.md)
		}
	}
}

// TestLiteralForms pins the decNumber canonical form of literals, seen on jq-1.7, and the double each converts to.
func TestLiteralForms(t *testing.T) {
	for _, c := range []struct{ in, text, float string }{
		{"8.460", "8.460", "8.46"},
		{"1e-05", "0.00001", "1e-05"},
		{"1E2", "1E+2", "100"},
		{"0.000000123", "1.23E-7", "1.23e-07"},
		{"0.0000001", "1E-7", "1e-07"},
		{"0.000001", "0.000001", "1e-06"},
		{"12e-8", "1.2E-7", "1.2e-07"},
		{"10e-1", "1.0", "1"},
		{"123e-2", "1.23", "1.23"},
		{"1.50e+3", "1.50E+3", "1500"},
		{"1e0", "1", "1"},
		{"0", "0", "0"},
		{"-0", "-0", "-0"},
		{"-0.0", "-0.0", "-0"},
		{"0.00", "0.00", "0"},
		{"0e10", "0E+10", "0"},
		{"0E-10", "0E-10", "0"},
		{"12345678901234567890", "12345678901234567890", "12345678901234567000"},
		{"1e400", "1E+400", "1.7976931348623157e+308"},
		{"-1e-400", "-1E-400", "-0"},
		{"3.0000000000000004", "3.0000000000000004", "3.0000000000000004"},
		{"1.00000000000000011102230246251565404236316680908203126", "1.00000000000000011102230246251565404236316680908203126", "1"},
		{"1e1000000000", "Infinity", "1.7976931348623157e+308"},
		{"-1e1000000000", "-Infinity", "-1.7976931348623157e+308"},
		{"0e1000000000", "0E+999999999", "0"},
		{"12e999999998", "1.2E+999999999", "1.7976931348623157e+308"},
		{"10e999999999", "Infinity", "1.7976931348623157e+308"},
		{"1e-1147483646", "1E-1147483646", "0"},
		{"1e-1147483647", "0E-1147483646", "0"},
		{"5e-1147483647", "1E-1147483646", "0"},
		{"-1e-1200000000", "-0E-1147483646", "-0"},
		{"1e99999999999", "Infinity", "1.7976931348623157e+308"},
		{"1e-0000000000000005", "0.00001", "1e-05"},
	} {
		n, err := Literal(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if n.Text != c.text || FormatFloat(n.F) != c.float || !n.Lit {
			t.Errorf("%s: Text %s, F %s; want %s, %s", c.in, n.Text, FormatFloat(n.F), c.text, c.float)
		}
		if want := c.text; strings.HasSuffix(want, "Infinity") {
			if n.String() != c.float {
				t.Errorf("%s prints %s, want %s", c.in, n.String(), c.float)
			}
		} else if n.String() != want {
			t.Errorf("%s prints %s, want %s", c.in, n.String(), want)
		}
	}
	for _, bad := range []string{"", "-", "01", "-01", "1.", ".5", "+1", "1e", "1e+", "1.e5", "0x10", "nan", "NaN",
		"Infinity", "-Infinity", "1 ", " 1", "1_000"} {
		if n, err := Literal(bad); err == nil {
			t.Errorf("Literal(%q) = %v, want an error", bad, n)
		}
	}
}

// TestFormatFloat pins jq 1.7's dtoa form at its two switches, seen on jq-1.7.
func TestFormatFloat(t *testing.T) {
	for _, c := range []struct {
		f    float64
		want string
	}{
		{0, "0"}, {math.Copysign(0, -1), "-0"}, {1, "1"}, {-2.5, "-2.5"},
		{1e-05, "1e-05"}, {-1e-05, "-1e-05"}, {4.5e-05, "4.5e-05"}, {9.9e-05, "9.9e-05"}, {0.0001, "0.0001"},
		{0.000102, "0.000102"}, {1e-06, "1e-06"}, {0.195996, "0.195996"}, {587.454999923706, "587.454999923706"},
		{1e15, "1000000000000000"}, {1e16, "1e+16"}, {1.5e16, "15000000000000000"}, {1e17, "1e+17"},
		{123456789012345680, "123456789012345680"}, {1e21, "1e+21"}, {5e-324, "5e-324"},
		{math.MaxFloat64, "1.7976931348623157e+308"}, {math.Inf(1), "1.7976931348623157e+308"},
		{math.Inf(-1), "-1.7976931348623157e+308"}, {math.NaN(), "null"}, {1e100, "1e+100"},
	} {
		if got := FormatFloat(c.f); got != c.want {
			t.Errorf("FormatFloat(%v) = %s, want %s", c.f, got, c.want)
		}
	}
}

// literalProbes are number tokens of every shape RFC 8259 allows: signs, leading zeros in fractions, trailing
// zeros, both exponent letters, exponents with signs and leading zeros, coefficients past 17 digits, and exponents
// near the double's range and near decNumber's.
func literalProbes(rng *rand.Rand, n int) []string {
	probes := []string{
		"0", "-0", "0.0", "-0.0", "0e10", "0E-10", "-0e-0", "1e-7", "1e-6", "0.000001", "0.0000001", "1E2", "1e400",
		"1e-400", "-1e-400", "1e1000000000", "-1e1000000000", "1e-1147483646", "1e-1147483647", "5e-1147483647",
		"4e-1147483647", "15e-1147483647", "25e-1147483647", "-25e-1147483647", "123456789e-1147483650",
		"0e99999999999", "0e-99999999999", "1e99999999999", "1e-99999999999", "1e1999999999", "1e2000000000",
		"1e-1999999999", "1e0000000000005", "9.99999999999999999e999999999", "99e999999998", "1.7976931348623157e308",
		"1.7976931348623158e308", "1.7976931348623159e308", "2.4703282292062327e-324", "2.4703282292062328e-324",
		"4.9406564584124654e-324", "2.2250738585072011e-308", "2.2250738585072012e-308",
		"1.00000000000000011102230246251565404236316680908203125",
		"1.00000000000000011102230246251565404236316680908203126", "9007199254740993", "9007199254740992.5",
		"8.460", "1792076002.490", "0.0037053227087815443", "0.000", "1.00000", "11681.70", "28849.999999999774",
	}
	digits := func(k int, lead bool) string {
		b := make([]byte, k)
		for i := range b {
			b[i] = byte('0' + rng.IntN(10))
		}
		if lead && b[0] == '0' {
			b[0] = byte('1' + rng.IntN(9))
		}
		return string(b)
	}
	for len(probes) < n {
		var b strings.Builder
		if rng.IntN(4) == 0 {
			b.WriteByte('-')
		}
		if rng.IntN(5) == 0 {
			b.WriteByte('0')
		} else {
			b.WriteString(digits(1+rng.IntN(22), true))
		}
		if rng.IntN(2) == 0 {
			b.WriteByte('.')
			b.WriteString(digits(1+rng.IntN(22), false))
		}
		if rng.IntN(2) == 0 {
			b.WriteByte("eE"[rng.IntN(2)])
			b.WriteString([]string{"", "+", "-"}[rng.IntN(3)])
			if rng.IntN(6) == 0 {
				b.WriteString(strings.Repeat("0", 1+rng.IntN(3)))
			}
			var e int
			switch rng.IntN(4) {
			case 0:
				e = rng.IntN(30)
			case 1:
				e = 280 + rng.IntN(60)
			case 2:
				e = rng.IntN(400)
			default:
				e = 999999990 + rng.IntN(20)
			}
			b.WriteString(strconv.Itoa(e))
		}
		probes = append(probes, b.String())
	}
	return probes
}

// doubleProbes are doubles across the range: random bit patterns, values around the 1e-4 and 1e16 switches of the
// dtoa form, short decimals, integers near 2^53, every power of two and the subnormals' edges.
func doubleProbes(rng *rand.Rand, n int) []float64 {
	fs := []float64{0, math.Copysign(0, -1), 5e-324, math.SmallestNonzeroFloat64 * 3, 2.2250738585072014e-308,
		2.225073858507201e-308, math.MaxFloat64, -math.MaxFloat64, 1e-4, 9.999999999999999e-05, 1e16, 9999999999999998,
		1e17, 123456789012345680, 0.1, 0.2, 0.30000000000000004, 1.0 / 3}
	for e := -1074; e <= 1023; e++ {
		fs = append(fs, math.Ldexp(1, e))
	}
	for len(fs) < n {
		var f float64
		switch rng.IntN(5) {
		case 0:
			f = math.Float64frombits(rng.Uint64())
		case 1:
			f = math.Pow(10, -7+float64(4*rng.Float64())) // float64(): no fused multiply-add on arm64
		case 2:
			f = float64(rng.Int64N(1 << 60))
		case 3:
			f, _ = strconv.ParseFloat(fmt.Sprintf("%de%d", rng.IntN(100000), -10+rng.IntN(30)), 64)
		default:
			f = rng.NormFloat64() * math.Pow(10, float64(rng.IntN(40)-20))
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		if rng.IntN(2) == 0 {
			f = -f
		}
		fs = append(fs, f)
	}
	return fs
}

// TestJQNumbers checks the two number forms and the two comparisons against jq 1.7 itself: the canonical text of
// about 4000 literals, the double each converts to, the dtoa form of about 6000 doubles, and about 3000 pairs
// compared as literals and as a computed number against a literal.
func TestJQNumbers(t *testing.T) {
	jq := needJQ(t)
	rng := rand.New(rand.NewPCG(20261022, 31))
	lines := func(b []byte) []string { return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") }

	toks := literalProbes(rng, 4000)
	in := []byte("[" + strings.Join(toks, ",") + "]")
	texts := lines(runJQ(t, jq, in, "-c", ".[]"))
	floats := lines(runJQ(t, jq, in, "-c", ".[] | . * 1"))
	if len(texts) != len(toks) || len(floats) != len(toks) {
		t.Fatalf("jq printed %d and %d lines for %d literals", len(texts), len(floats), len(toks))
	}
	bad := 0
	for i, tok := range toks {
		n := lit(t, tok)
		if n.String() != texts[i] || Computed(n.F).String() != floats[i] {
			bad++
			if bad <= 20 {
				t.Errorf("literal %s: jsonx prints %s and converts to %s; jq %s and %s", tok, n.String(),
					Computed(n.F).String(), texts[i], floats[i])
			}
		}
	}

	fs := doubleProbes(rng, 6000)
	var b bytes.Buffer
	b.WriteByte('[')
	for i, f := range fs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(f, 'g', -1, 64)) // shortest digits: jq's 17-digit step keeps them
	}
	b.WriteByte(']')
	dtoa := lines(runJQ(t, jq, b.Bytes(), "-c", ".[] | . * 1"))
	if len(dtoa) != len(fs) {
		t.Fatalf("jq printed %d lines for %d doubles", len(dtoa), len(fs))
	}
	for i, f := range fs {
		if got := FormatFloat(f); got != dtoa[i] {
			bad++
			if bad <= 20 {
				t.Errorf("double %v: jsonx prints %s, jq %s", f, got, dtoa[i])
			}
		}
		if back := lit(t, FormatFloat(f)); back.F != f || math.Signbit(back.F) != math.Signbit(f) {
			t.Errorf("double %v prints %s, which reads back as %v", f, FormatFloat(f), back.F)
		}
	}

	// Pairs: a probe against itself with a trailing zero, with digits past the 17th, against a near neighbour,
	// and at random; and the pairs equal as doubles and unequal as decimals.
	pairs := [][2]string{{"0.1", "0.10000000000000000555"}, {"0", "-0"}, {"2", "2.0"}, {"2.0", "2"}, {"1e400", "1e401"},
		{"1e1000000000", "2e1000000000"}, {"1e1000000000", "9e999999999"}, {"-1e1000000000", "-9e999999999"},
		{"1E+999999999", "1e1000000000"}, {"9007199254740993", "9007199254740992"}, {"1.5", "15e-1"}, {"0e5", "0e-5"}}
	for len(pairs) < 3000 {
		a := toks[rng.IntN(len(toks))]
		i := strings.IndexAny(a, "eE")
		if i < 0 {
			i = len(a)
		}
		mant, exp := a[:i], a[i:]
		if !strings.Contains(mant, ".") {
			mant += "."
		}
		var c string
		switch rng.IntN(4) {
		case 0:
			c = mant + "0" + exp // the same decimal
		case 1:
			c = mant + "000000000000000000001" + exp // past the 17th digit
		case 2:
			c = strconv.FormatFloat(math.Nextafter(lit(t, a).F, math.Inf(1)), 'g', -1, 64)
			if strings.Contains(c, "Inf") {
				c = a
			}
		default:
			c = toks[rng.IntN(len(toks))]
		}
		pairs = append(pairs, [2]string{a, c})
	}
	b.Reset()
	b.WriteByte('[')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "[%s,%s]", p[0], p[1])
	}
	b.WriteByte(']')
	cmps := lines(runJQ(t, jq, b.Bytes(), "-c", ".[] | [.[0] < .[1], .[0] == .[1], .[0] * 1 < .[1], .[0] * 1 == .[1]]"))
	if len(cmps) != len(pairs) {
		t.Fatalf("jq printed %d lines for %d pairs", len(cmps), len(pairs))
	}
	split := 0
	for i, p := range pairs {
		x, y := Num(lit(t, p[0])), Num(lit(t, p[1]))
		cx := Float(lit(t, p[0]).F)
		got := fmt.Sprintf("[%t,%t,%t,%t]", Compare(x, y) < 0, Equal(x, y), Compare(cx, y) < 0, Equal(cx, y))
		if got != cmps[i] {
			bad++
			if bad <= 20 {
				t.Errorf("%s against %s: jsonx %s, jq %s", p[0], p[1], got, cmps[i])
			}
		}
		if Equal(x, y) != Equal(cx, y) {
			split++
		}
	}
	if split == 0 {
		t.Errorf("no pair compared one way as literals and the other with a computed side")
	}
	t.Logf("%d literals, %d doubles, %d pairs (%d decided by the decimal comparison); %d differ from jq",
		len(toks), len(fs), len(pairs), split, bad)
}
