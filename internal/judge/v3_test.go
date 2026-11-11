package judge

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// v3 is v2 with two fixes and nothing else: fx turns a -0 into 0 (ZeroFix, defs.go's fx), and the wave reason
// names laug gate tolerance (ToleranceText, arm.go's whyOf). The tests here hold that on the 84 committed v2 runs
// and on fx and its callers.

// zeroRuns are the committed v2 runs whose verdict.md prints a -0 d as 0.0-0 or 0.00-0 points: the runs v3 judges
// to other bytes than v2.
var zeroRuns = []string{"g20261017-191109", "g20261017-192202", "g20261018-091444", "g20261018-171208"}

// v2AsV3 is v2's output text as v3 prints it: fx's three -0 forms, "0.-0", "0.0-0" and "0.00-0" with 2, 3 and 4
// decimals, as zero, and the wave reason's command.
var v2AsV3 = strings.NewReplacer("0.00-0", "0.0000", "0.0-0", "0.000", "0.-0", "0.00",
	"]: make arm-tolerance on A/A gates of this plan", "]: laug gate tolerance on A/A gates of this plan")

// TestV3IsV2Fixed holds v3 to v2 and its two fixes. Its features are v2's and the two; and Judge under v3 gives every
// committed v2 run's verdict.json and verdict.md with v2AsV3 applied, which changes the verdict.md of the 4 zeroRuns
// and nothing else.
func TestV3IsV2Fixed(t *testing.T) {
	v2, v3 := mustNamed(t, "v2"), mustNamed(t, "v3")
	if want := (Features{Arm: v2.Arm, HostCheck: v2.HostCheck, R7Off: v2.R7Off, ZeroFix: true,
		ToleranceText: true}); v3.Features != want || v2.ZeroFix || v2.ToleranceText {
		t.Errorf("v3's features %+v, v2's %+v: want v2's and ZeroFix and ToleranceText", v3.Features, v2.Features)
	}
	var changed []string
	n := 0
	for _, run := range committedRuns(t) {
		if recordedVersion(t, run).Name != "v2" {
			continue
		}
		n++
		res, err := Judge(os.DirFS(filepath.Join(results, run)), v3, Options{})
		if err != nil {
			t.Errorf("%s: %v", run, err)
			continue
		}
		js := string(readAll(t, filepath.Join(results, run, "verdict.json")))
		md := string(readAll(t, filepath.Join(results, run, "verdict.md")))
		if string(res.JSON) != v2AsV3.Replace(js) || string(res.MD) != v2AsV3.Replace(md) {
			t.Errorf("%s: v3 does not give the committed files with the two fixes:\n%s\n%s", run,
				firstLineDiff(v2AsV3.Replace(js), string(res.JSON)), firstLineDiff(v2AsV3.Replace(md), string(res.MD)))
		}
		if string(res.JSON) != js {
			t.Errorf("%s: v3 changes verdict.json", run)
		}
		if string(res.MD) != md {
			changed = append(changed, run)
		}
	}
	cs := census(t)
	isV2 := func(row jsonx.Value) bool { return rowVersion(t, row).Name == "v2" }
	if all := cs.Count(nil, isV2); all != 84 {
		t.Errorf("the gates table records %d v2 runs, want 84", all)
	}
	if want, zero := cs.Count(cs.Here, isV2), hereOf(cs, zeroRuns); n != want || !slices.Equal(changed, zero) {
		t.Errorf("%d v2 runs; v3 changes the verdict.md of %q, want %d and %q", n, changed, want, zero)
	}
}

// TestZeroFix holds fx and its callers (defs.go) under each version that has them, v1 aside: each
// input through each function, in Go. v2 and v2-r7off print a -0 as 0.-0, 0.0-0 or 0.00-0; v3 prints zero there and
// the same text everywhere else.
func TestZeroFix(t *testing.T) {
	inputs := []struct {
		expr string
		v    jsonx.Value
	}{
		// jq reads -0.0004 in a program as 0.0004 negated, a computed number.
		{"0", zero}, {"(0 | -.)", negate(zero)}, {"-0.0004", negate(constant("0.0004"))},
		{"0.0004", constant("0.0004")}, {"-0.0000004", negate(constant("0.0000004"))},
		{"-0.00004", negate(constant("0.00004"))}, {"-0.0005", negate(constant("0.0005"))},
		{"-0.2", negate(constant("0.2"))}, {"(1e-9 | -.)", negate(constant("1e-9"))}, {"1e17", constant("1e17")},
		{"5954", constant("5954")}, {"-34", negate(constant("34"))}, {"nan", jsonx.Float(math.NaN())},
		{"null", null}, {`"x"`, str("x")},
	}
	fns := []struct {
		expr string
		f    func(f Features, v jsonx.Value) string
	}{
		{"fx(2)", func(f Features, v jsonx.Value) string { return fx(f, v, 2) }},
		{"fx(3)", func(f Features, v jsonx.Value) string { return fx(f, v, 3) }},
		{"fx(4)", func(f Features, v jsonx.Value) string { return fx(f, v, 4) }},
	}
	for _, scale := range []string{"ms", "share", "count"} {
		sc := str(scale)
		fns = append(fns, []struct {
			expr string
			f    func(f Features, v jsonx.Value) string
		}{
			{`val("` + scale + `")`, func(f Features, v jsonx.Value) string { return val(f, v, sc) }},
			{`dval("` + scale + `")`, func(f Features, v jsonx.Value) string { return dval(f, v, sc) }},
			{`tval("` + scale + `")`, func(f Features, v jsonx.Value) string { return tval(f, v, sc) }},
			{`mval("` + scale + `")`, func(f Features, v jsonx.Value) string { return mval(f, v, sc) }},
		}...)
	}
	// got[version][input][fn]: the text, or "error".
	got := map[string][][]string{}
	for _, v := range Versions() {
		if !v.Arm {
			continue // v1's DEFS have no fx
		}
		for i, in := range inputs {
			got[v.Name] = append(got[v.Name], nil)
			for _, fn := range fns {
				text, ok := try(func() string { return fn.f(v.Features, in.v) })
				if !ok {
					text = "error"
				}
				got[v.Name][i] = append(got[v.Name][i], text)
			}
		}
	}
	pinned := map[string][2]string{ // input | fn: v2's, v3's
		"(0 | -.) | fx(2)":             {"0.-0", "0.00"},
		"(0 | -.) | fx(3)":             {"0.0-0", "0.000"},
		"(0 | -.) | fx(4)":             {"0.00-0", "0.0000"},
		"-0.0004 | val(\"ms\")":        {"0.0-0", "0.000"},
		"-0.0000004 | dval(\"share\")": {"0.00-0 points", "0.0000 points"},
		"-0.00004 | val(\"share\")":    {"0.-0%", "0.00%"},
		"(1e-9 | -.) | mval(\"ms\")":   {"0.0-0 ms", "0.000 ms"},
		"(0 | -.) | tval(\"ms\")":      {"0.-0", "0.00"},
		"(0 | -.) | dval(\"count\")":   {"-0", "-0"},
		"-0.0005 | fx(3)":              {"-0.001", "-0.001"},
		"0 | fx(3)":                    {"0.000", "0.000"},
		"nan | fx(3)":                  {"-n.ull", "-n.ull"},
	}
	differ := 0
	for i, in := range inputs {
		for k, fn := range fns {
			v2, r7off, v3 := got["v2"][i][k], got["v2-r7off"][i][k], got["v3"][i][k]
			if r7off != v2 || v3 != v2AsV3.Replace(v2) {
				t.Errorf("%s | %s: v2 %q, v2-r7off %q, v3 %q", in.expr, fn.expr, v2, r7off, v3)
			}
			if v3 != v2 {
				differ++
			}
			if p, ok := pinned[in.expr+" | "+fn.expr]; ok {
				if v2 != p[0] || v3 != p[1] {
					t.Errorf("%s | %s: v2 %q, v3 %q; want %q, %q", in.expr, fn.expr, v2, v3, p[0], p[1])
				}
				delete(pinned, in.expr+" | "+fn.expr)
			}
		}
	}
	for k := range pinned {
		t.Errorf("no row %s", k)
	}
	t.Logf("%d inputs through %d functions: v3 prints %d of them otherwise than v2", len(inputs), len(fns), differ)
}

// TestRenderV3 holds v3's Render on the comparison cells that hold a -0: the d, a reading and a tolerance print as
// zero, and every other cell as v2 prints it.
func TestRenderV3(t *testing.T) {
	v3 := mustNamed(t, "v3")
	runPrint(t, renderAs(v3), []printCase{
		{name: "a d of -0 and of 4.5e-05", run: advanceRun, patch: []string{"comparison.rows[0].d=-0",
			"comparison.rows[2].d=-0", "comparison.rows[3].d=4.5e-05", "comparison.rows[11].d=-0"}, lines: []string{
			"| workflow_task_schedule_to_start_p99_ms | 16.887 ms | 17.087 ms | 0.000 | 1.02 | ok |",
			"| activity_schedule_to_start_over_10ms | 0.93% | 0.92% | 0.0000 points | 0.72 points | ok |",
			"| frontend_request_over_10ms | 1.08% | 1.16% | +0.0045 points | 0.32 points | ok |",
			"| workflows_completed | 5920 | 5954 | -0 |  | recorded |"}},
		{name: "readings and tolerances of -0", run: advanceRun, patch: []string{"comparison.rows[0].release=-0",
			"comparison.rows[0].tolerance=-0.001", "comparison.rows[2].incumbent=-0.00001",
			"comparison.rows[2].tolerance=-0"}, lines: []string{
			"| workflow_task_schedule_to_start_p99_ms | 0.000 ms | 17.087 ms | -0.200 | 0.00 | ok |",
			"| activity_schedule_to_start_over_10ms | 0.93% | 0.00% | +0.0045 points | 0.00 points | ok |"}},
		{name: "the committed d of -0", run: "g20261017-192202", lines: []string{
			"| activity_schedule_to_start_p99_ms | 9.989 ms | 9.989 ms | 0.000 |  | recorded |"}},
		{name: "the committed d of -0 in a share", run: "g20261017-191109", lines: []string{
			"| workflow_task_schedule_to_start_over_20ms | 0.28% | 0.28% | 0.0000 points |  | recorded |"}},
	})
}
