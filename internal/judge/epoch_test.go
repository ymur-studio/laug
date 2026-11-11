package judge

import (
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// TestEpochMatchesJQ holds epoch (epoch.go) over date probes: glibc's strptime for
// "%Y-%m-%dT%H:%M:%SZ" with its blanks and widths, jq's one byte after Z, timegm's normalizing and jq's -1 and -2.
// Go must give the table; with jq 1.7.x present, so must jq, under LC_ALL=C, on the def's body as the test writes it.
func TestEpochMatchesJQ(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"2026-10-18T09:29:26Z"`, "1792315766"},
		{`"2026-10-18T9:29:26Z"`, "1792315766"},            // a one-digit hour
		{`" 2026-10-18T09:29:26Z"`, "1792315766"},          // a leading blank
		{`"2026-10-18T 9:29:26Z"`, "1792315766"},           // a blank before a number
		{`"2026-1-8T9:2:6Z"`, "1767862926"},                // one digit each
		{`"2026-10-18T09:29:26Z\n"`, "1792315766"},         // a final newline
		{`"2026-10-18T09:29:26Z xyz"`, "1792315766"},       // text after a blank after Z
		{`"2026-10-18T09:29:26Z\njunk"`, "1792315766"},     // after a newline
		{`"2026-10-18T09:29:26Z\u000bx"`, "1792315766"},    // after a vertical tab
		{`"2026-10-18T09:29:26Z\u0000junk"`, "1792315766"}, // a NUL ends the C string
		{`"2026-10-18T09:29:26Zxyz"`, "null"},              // text right after Z
		{`"2026-10-18T09:29:26Zé"`, "null"},                // a byte past ASCII is no blank
		{`"2026-10-18T09:29:26.5Z"`, "null"},               // a fraction
		{`"2026-10-18T09:29:26+00:00"`, "null"},            // an offset
		{`"2026-10-18T09:29:26"`, "null"},                  // no Z
		{`"2026 -10-18T09:29:26Z"`, "null"},                // a blank before a literal
		{`"2026-10-18t09:29:26Z"`, "null"},                 // a lowercase t
		{`"2026-02-31T00:00:00Z"`, "1772496000"},           // Feb 31 is Mar 3
		{`"2026-10-18T23:59:60Z"`, "1792368000"},           // second 60
		{`"2026-10-18T23:59:61Z"`, "1792368001"},           // second 61
		{`"2026-10-18T23:59:62Z"`, "null"},
		{`"2026-10-18T24:00:00Z"`, "null"},
		{`"2026-10-18T23:60:00Z"`, "null"},
		{`"2026-13-01T00:00:00Z"`, "null"},
		{`"2026-00-01T00:00:00Z"`, "null"},
		{`"2026-10-00T00:00:00Z"`, "null"},
		{`"2026-10-32T00:00:00Z"`, "null"},
		{`"1969-12-31T23:59:59Z"`, "null"}, // -1: "invalid gmtime representation"
		{`"1969-12-31T23:59:58Z"`, "null"}, // -2: "mktime not supported on this platform"
		{`"1969-12-31T23:59:57Z"`, "-3"},
		{`"1970-01-01T00:00:00Z"`, "0"},
		{`"1960-01-01T00:00:00Z"`, "-315619200"},
		{`"0000-01-01T00:00:00Z"`, "-62167219200"},
		{`"9999-12-31T23:59:59Z"`, "253402300799"},
		{`"10000-01-01T00:00:00Z"`, "null"}, // %Y reads 4 digits
		{`"02026-10-18T09:29:26Z"`, "null"},
		{`""`, "null"},
		{`1792315766`, "null"},
		{`null`, "null"},
		{`true`, "null"},
	}
	var ins []jsonx.Value
	for _, c := range cases {
		in, err := jsonx.Parse([]byte(c.in))
		if err != nil {
			t.Fatal(err)
		}
		ins = append(ins, in)
		if got := tojson(epoch(in)); got != c.want {
			t.Errorf("epoch(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	jq := maybeJQ(t)
	if jq == "" {
		return
	}
	out, ok := jqRun(jq, jsonx.MarshalCompact(jsonx.Arr(ins)), "-c",
		`.[] | if type == "string" then (try fromdateiso8601 catch null) else null end`)
	if !ok {
		t.Fatalf("jq: %s", out)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("jq gave %d lines for %d cases", len(got), len(cases))
	}
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("jq: epoch(%s) = %s, the table says %s", c.in, got[i], c.want)
		}
	}
}
