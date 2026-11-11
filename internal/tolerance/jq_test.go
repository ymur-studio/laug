package tolerance

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Some tests hold Go to jq 1.7.x itself, on programs the test writes: TestBuiltinsMatchJQ, TestRegexMatchesJQ,
// TestJQLine and TestStream. Without jq the Go side of each table still runs, and the test logs why jq did not;
// LAUG_TEST_SHELL=require turns that into a failure.

// root is the repository root, from this package's directory.
var root = filepath.Join("..", "..")

// findJQ is jq 1.7.x, jq --version printing jq-1.7 or jq-1.7.<n>, or why there is none.
var findJQ = sync.OnceValues(func() (string, string) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		return "", "no jq on PATH"
	}
	out, err := exec.Command(jq, "--version").Output()
	if err != nil {
		return "", "jq --version failed: " + err.Error()
	}
	if v := strings.TrimSpace(string(out)); !regexp.MustCompile(`^jq-1\.7(\.|$)`).MatchString(v) {
		return "", "jq is " + v + ", not 1.7.x"
	}
	return jq, ""
})

// maybe returns tool, or "" with why logged, or a failure under LAUG_TEST_SHELL=require.
func maybe(t *testing.T, tool, why string) string {
	t.Helper()
	if why == "" {
		return tool
	}
	if os.Getenv("LAUG_TEST_SHELL") == "require" {
		t.Fatalf("LAUG_TEST_SHELL=require: %s", why)
	}
	t.Logf("not checked against jq: %s", why)
	return ""
}

// maybeJQ is jq 1.7.x, or "".
func maybeJQ(t *testing.T) string {
	t.Helper()
	jq, why := findJQ()
	return maybe(t, jq, why)
}

// jqRun runs jq with args and stdin under LC_ALL=C: its stdout, its stderr and its exit code.
func jqRun(t *testing.T, jq string, stdin []byte, args ...string) (stdout, stderr []byte, code int) {
	t.Helper()
	cmd := exec.Command(jq, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return o.Bytes(), e.Bytes(), code
}

// value parses one JSON value, or fails the test.
func value(t *testing.T, s string) jsonx.Value {
	t.Helper()
	v, err := jsonx.Parse([]byte(s))
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return v
}

// goAnswer is what jq -nc prints for f's value, or jq's error line for the jq error f raises.
func goAnswer(f func() jsonx.Value) string {
	var v jsonx.Value
	if e := evaluate(func() { v = f() }); e != nil {
		return "jq: error (at <unknown>): " + e.msg + "\n"
	}
	return string(jsonx.MarshalCompact(v))
}

// TestBuiltinsMatchJQ holds each builtin RULE and SHOW call to jq-1.7, value and error text, over values of every
// kind: a long string and a 30-byte key among them, which jq cuts in its messages.
func TestBuiltinsMatchJQ(t *testing.T) {
	inputs := []string{`null`, `true`, `false`, `5`, `-0`, `1.50`, `1e-9`, `""`, `"abc"`, `"abcdefghijklmnopqrstuvwxyz"`,
		`[]`, `[1,"a",null,true]`, `[[1]]`, `[{"a":1}]`, `[3,1,2,1.0]`, `{}`, `{"a":1,"b":[2]}`, `{"b":2,"a":1}`,
		`"a_ms\n"`, `"é"`}
	keys := []string{`"a"`, `"kkkkkkkkkkkkkkkkkkkkkkkkkkkkk"`, `"kkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"`, `0`, `-1`, `1.7`,
		`null`, `true`, `[1]`, `{"start":1,"end":null}`, `{"start":0}`, `{"x":1}`}
	type builtin struct {
		jq string
		fn func(x jsonx.Value) jsonx.Value
	}
	// RULE and SHOW call these on an array they made, and on nothing else.
	arraysOnly := map[string]bool{"max": true, "add": true, "unique": true, "group_by(.)": true}
	builtins := []builtin{
		{`.a`, func(x jsonx.Value) jsonx.Value { return field(x, "a") }},
		{`.kkkkkkkkkkkkkkkkkkkkkkkkkkkkkk`, func(x jsonx.Value) jsonx.Value {
			return field(x, "kkkkkkkkkkkkkkkkkkkkkkkkkkkkkk")
		}},
		{`[.[]]`, func(x jsonx.Value) jsonx.Value { return jsonx.Arr(each(x)) }},
		{`[.[]?]`, func(x jsonx.Value) jsonx.Value { return jsonx.Arr(eachOpt(x)) }},
		{`[.a[]?]`, func(x jsonx.Value) jsonx.Value { return jsonx.Arr(eachOpt(field(x, "a"))) }},
		{`length`, length},
		{`tostring`, func(x jsonx.Value) jsonx.Value { return str(tostring(x)) }},
		{`join(", ")`, func(x jsonx.Value) jsonx.Value { return str(join(x, ", ")) }},
		{`split(":")`, func(x jsonx.Value) jsonx.Value { return jsonx.Arr(split(x, ":")) }},
		{`test("_ms$")`, func(x jsonx.Value) jsonx.Value { return jsonx.Bool(test(x, msEnd)) }},
		{`to_entries`, func(x jsonx.Value) jsonx.Value {
			var es []jsonx.Value
			for _, e := range toEntries(x) {
				es = append(es, object("key", e.key, "value", e.value))
			}
			return jsonx.Arr(es)
		}},
		{`max`, func(x jsonx.Value) jsonx.Value { return maxOf(members(x)) }},
		{`add`, func(x jsonx.Value) jsonx.Value { return add(members(x)) }},
		{`unique`, func(x jsonx.Value) jsonx.Value { return jsonx.Arr(unique(members(x))) }},
		{`group_by(.)`, func(x jsonx.Value) jsonx.Value {
			var gs []jsonx.Value
			for _, g := range groupBy(members(x), func(v jsonx.Value) jsonx.Value { return v }) {
				gs = append(gs, jsonx.Arr(g))
			}
			return jsonx.Arr(gs)
		}},
		{`. + [1]`, func(x jsonx.Value) jsonx.Value { return plus(x, value(t, `[1]`)) }},
		{`. + 1`, func(x jsonx.Value) jsonx.Value { return plus(x, constant("1")) }},
		{`. - 1e-9`, func(x jsonx.Value) jsonx.Value { return minus(x, c1em9) }},
		{`. - [1]`, func(x jsonx.Value) jsonx.Value { return minus(x, value(t, `[1]`)) }},
		{`. * 100`, func(x jsonx.Value) jsonx.Value { return times(x, c100) }},
		{`. * 2.5`, func(x jsonx.Value) jsonx.Value { return times(x, constant("2.5")) }},
		{`. * -1`, func(x jsonx.Value) jsonx.Value { return times(x, constant("-1")) }},
		{`. / 4`, func(x jsonx.Value) jsonx.Value { return divide(x, four) }},
		{`. / 0`, func(x jsonx.Value) jsonx.Value { return divide(x, constant("0")) }},
		{`round`, round},
		{`ceil`, ceil},
		{`fabs`, fabs},
		{`sqrt`, sqrt},
		{`.[1:9]`, func(x jsonx.Value) jsonx.Value { return slice(x, one, constant("9")) }},
		{`.[:2]`, func(x jsonx.Value) jsonx.Value { return sliceTo(x, 2) }},
		{`.[2:]`, func(x jsonx.Value) jsonx.Value { return sliceFrom(x, 2) }},
		{`.[0]`, func(x jsonx.Value) jsonx.Value { return at(x, 0) }},
	}
	jq := maybeJQ(t)
	check := func(t *testing.T, prog string, args []string, got string) {
		t.Helper()
		if jq == "" {
			return
		}
		out, errOut, _ := jqRun(t, jq, nil, append(append([]string{"-nc"}, args...), prog)...)
		if want := string(out) + string(errOut); got != want {
			t.Errorf("%s: Go %q, jq %q", prog, got, want)
		}
	}
	n := 0
	for _, b := range builtins {
		for _, in := range inputs {
			x := value(t, in)
			if arraysOnly[b.jq] && x.Kind() != jsonx.KindArray {
				continue
			}
			check(t, "$x | "+b.jq, []string{"--argjson", "x", in}, goAnswer(func() jsonx.Value { return b.fn(x) }))
			n++
		}
	}
	for _, in := range inputs {
		for _, k := range keys {
			x, kv := value(t, in), value(t, k)
			check(t, "$x | .[$k]", []string{"--argjson", "x", in, "--argjson", "k", k},
				goAnswer(func() jsonx.Value { return index(x, kv) }))
			n++
		}
	}
	t.Logf("%d cases", n)
}

// TestDump holds dump to jq's cut: 14 bytes whole, 15 and more cut to 11 and "...".
func TestDump(t *testing.T) {
	for in, want := range map[string]string{
		`"abcdefghijkl"`:    `"abcdefghijkl"`,
		`"abcdefghijklm"`:   `"abcdefghij...`,
		`[1,2,3,4,5,6,7]`:   `[1,2,3,4,5,...`,
		`12345678901234`:    `12345678901234`,
		`123456789012345`:   `12345678901...`,
		`{"a":[1,2],"b":1}`: `{"a":[1,2],...`,
	} {
		if got := dump(value(t, in)); got != want {
			t.Errorf("dump(%s) = %s, want %s", in, got, want)
		}
	}
}

// TestRegexMatchesJQ holds the three test() patterns to the table, and, with jq, to jq-1.7: jq's $ matches before
// one final newline, and not before two.
func TestRegexMatchesJQ(t *testing.T) {
	cases := []struct {
		s    string
		want [3]bool // overMsEnd, msEnd, rRule
	}{
		{"a_ms", [3]bool{false, true, false}},
		{"a_ms\n", [3]bool{false, true, false}},
		{"a_ms\n\n", [3]bool{false, false, false}},
		{"a_msx", [3]bool{false, false, false}},
		{"frontend_request_over_10ms", [3]bool{true, false, false}},
		{"frontend_request_over_10ms\n", [3]bool{true, false, false}},
		{"x_over_ms", [3]bool{false, true, false}},
		{"x_over_1_ms", [3]bool{false, true, false}},
		{"workflow_task_schedule_to_start_p99_ms", [3]bool{false, true, false}},
		{"R1", [3]bool{false, false, true}},
		{"R6\n", [3]bool{false, false, true}},
		{"R7", [3]bool{false, false, false}},
		{"R1\n\n", [3]bool{false, false, false}},
		{"\nR1", [3]bool{false, false, false}},
		{"xR1", [3]bool{false, false, false}},
		{"R12", [3]bool{false, false, false}},
		{"", [3]bool{false, false, false}},
	}
	jq := maybeJQ(t)
	for _, c := range cases {
		for i, p := range patterns {
			if got := p.re.MatchString(c.s); got != c.want[i] {
				t.Errorf("%q on %q: Go %t, the table %t", p.jq, c.s, got, c.want[i])
			}
			if jq == "" {
				continue
			}
			out, errOut, _ := jqRun(t, jq, nil, "-n", "--arg", "s", c.s, "--arg", "p", p.jq, "$s | test($p)")
			if want := strings.TrimSpace(string(out)); want != map[bool]string{true: "true", false: "false"}[c.want[i]] {
				t.Errorf("%q on %q: jq %q %s, the table %t", p.jq, c.s, want, errOut, c.want[i])
			}
		}
	}
}
