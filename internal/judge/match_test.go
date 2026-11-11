package judge

import (
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// TestRegexMatchesJQ holds JUDGE's test() patterns, as Go writes them, to Oniguruma's answers in jq 1.7: `$` also
// matches before one final newline, `^` only at the start, `.` never a newline. Go must give the table; with jq
// 1.7.x present, so must jq. The class table's two patterns are TestClassTable's.
func TestRegexMatchesJQ(t *testing.T) {
	cases := []struct {
		p    pattern
		s    string
		want bool
	}{
		{keyName, "pl", true}, {keyName, "pl\n", true}, {keyName, "pl\n\n", false}, {keyName, "pl\nx", false},
		{keyName, "\npl", false}, {keyName, "pl\r\n", false}, {keyName, "p-l9", true}, {keyName, "-pl", false},
		{keyName, "PL", false}, {keyName, "pl ", false}, {keyName, "", false}, {keyName, "pé", false},
		{overMsEnd, "frontend_request_over_10ms", true}, {overMsEnd, "frontend_request_over_10ms\n", true},
		{overMsEnd, "frontend_request_over_10ms\n\n", false}, {overMsEnd, "x_over_ms", false},
		{overMsEnd, "x_over_10ms_p99", false}, {overMsEnd, "x_over_10ms\nmore", false},
		{msEnd, "persistence_latency_p99_ms", true}, {msEnd, "p99_ms\n", true}, {msEnd, "p99_ms\n\n", false},
		{msEnd, "ms", false}, {msEnd, "_msx", false}, {msEnd, "x_ms\ny", false}, {msEnd, "x_over_10ms", false},
		{sweepFailed, "the sweep of 2 namespaces failed: timeout", true}, {sweepFailed, "the sweep  failed", true},
		{sweepFailed, "the sweep failed", false}, {sweepFailed, "before the sweep x failed after", true},
		{sweepFailed, "the sweep of 2\nnamespaces failed", false}, {sweepFailed, "the sweep x failed\n", true},
	}
	var ss []jsonx.Value
	for _, c := range cases {
		if got := test(str(c.s), c.p); got != c.want {
			t.Errorf("%q on %q: Go %v, want %v", c.p.jq, c.s, got, c.want)
		}
		ss = append(ss, object("re", str(c.p.jq), "s", str(c.s)))
	}
	jq := maybeJQ(t)
	if jq == "" {
		return
	}
	out, ok := jqRun(jq, jsonx.MarshalCompact(jsonx.Arr(ss)), "-c", ".[] | .re as $re | .s | test($re)")
	if !ok {
		t.Fatalf("jq: %s", out)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("jq gave %d lines for %d cases", len(got), len(cases))
	}
	for i, c := range cases {
		if got[i] != tojson(jsonx.Bool(c.want)) {
			t.Errorf("jq: %q on %q is %s, the table says %v", c.p.jq, c.s, got[i], c.want)
		}
	}
}
