package judge

import (
	"os"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// The tests of the feature flags judge one input under v1, v2, v2-r7off and v3. The committed runs leave much of each
// flag untried: no v1 run has an item of arm incumbent or a reason only v2's class rows know, v2-r7off's one run
// compared no row, and no run is v3's.

// A versionCase is one input judged under each version: values at verdict paths, by tojson, for each version by
// name. A path ending in "#keys" gives the keys of the object there, in order. fails names the versions whose
// Evaluate gives a jq error.
type versionCase struct {
	name  string
	base  string
	patch []string
	want  map[string]map[string]string
	fails map[string]bool
}

// keysAt is the keys of the object at path in v, in order, as a JSON array.
func keysAt(t *testing.T, v jsonx.Value, path string) string {
	t.Helper()
	o, ok := at(t, v, path).Obj()
	if !ok {
		return "not an object"
	}
	return strs(o.Keys()).String()
}

// v1Keys and v2Keys are verdict.json's top-level keys in each version's order (verdict.go, output).
const (
	v1Keys = `["gate_id","verdict","class","rerun","class_if_quiet","refused","fake","server_tag","laug_commit",` +
		`"tree_dirty","base","inputs","deadline_s","items_in_plan","items_limit","t0","t_post","elapsed_s","host",` +
		`"rules","reasons","items","host_quiet","host_noise"]`
	v2Keys = `["gate_id","verdict","class","rerun","class_if_quiet","refused","fake","server_tag","incumbent",` +
		`"laug_commit","tree_dirty","base","inputs","deadline_s","items_in_plan","items_limit","t0","t_post",` +
		`"elapsed_s","host","rules","reasons","items","comparison","host_quiet","host_noise"]`
	v1Item = `["key","kind","expect","observed","class","reason","failed","run_id","cell","block","exit",` +
		`"stopped_by","pool_busy","evidence"]`
	v2Item = `["key","kind","arm","expect","observed","class","reason","failed","run_id","cell","block","exit",` +
		`"stopped_by","pool_busy","evidence"]`
)

// TestVersions holds Arm, HostCheck, R7Off, ZeroFix and ToleranceText to each version's values, case by case.
func TestVersions(t *testing.T) {
	const (
		other    = "1.30.0"
		incOther = `dirs.incumbent.run.server_version="` + other + `"`
		worse    = "dirs.steady.verdict.readings.persistence_latency_p99_ms=100"
		blockInc = `dirs.incumbent.verdict.verdict="BLOCK"`
		v1BlockR = "R6 incumbent: identity: the cell said server_version 1.30.0, and the gate SERVER_TAG is 1.31.0"
		// A wave the tolerances were not measured on, and the reason's text up to the command it names.
		otherWave = `thresholds.compare.wave=["load:incumbent x","load x"]`
		waveWhy   = `"not compared: the tolerances in load/steady-state.yaml were measured on the wave [load:incumbent ` +
			`x, load x], and this plan runs [load:incumbent workflow_with_single_noop_activity 20/s 50 5m, load ` +
			`workflow_with_single_noop_activity 20/s 50 5m, fault:persistence-latency ` +
			`workflow_with_single_noop_activity]: `
		// An incumbent reading that rounds to -0 in a WORSE reason: val gives 0.0-0 in v2 and 0.000 in v3.
		worseWhy = `"worse than the incumbent: persistence_latency_p99_ms 100.000 against `
		worseEnd = ` on incumbent, +100.000 past a tolerance of 1.00"`
	)
	allAdvance := map[string]string{"verdict": `"ADVANCE"`}
	cases := []versionCase{
		{name: "the shape of each version", base: "base", want: map[string]map[string]string{
			"v1": {"#keys": v1Keys, "items[0]#keys": v1Item, "rules#keys": `["R1","R2","R3","R4","R5","R6"]`,
				"verdict": `"ADVANCE"`},
			"v2": {"#keys": v2Keys, "items[0]#keys": v2Item, "rules#keys": `["R1","R2","R3","R4","R5","R6","R7"]`,
				"verdict": `"ADVANCE"`, "comparison": "null"},
			"v2-r7off": {"#keys": v2Keys, "items[0]#keys": v2Item, "verdict": `"ADVANCE"`},
			"v3": {"#keys": v2Keys, "items[0]#keys": v2Item, "rules#keys": `["R1","R2","R3","R4","R5","R6","R7"]`,
				"verdict": `"ADVANCE"`, "comparison": "null"},
		}},
		{name: "an incumbent of another release", base: "base-arm",
			patch: []string{`plan.incumbent="` + other + `"`, incOther}, want: map[string]map[string]string{
				"v1": {"verdict": `"BLOCK"`, "class": `"invalid"`, "items[0].reason": `"identity: the cell said ` +
					`server_version 1.30.0, and the gate SERVER_TAG is 1.31.0"`, "reasons": `["` + v1BlockR + `"]`},
				"v2":       {"verdict": `"ADVANCE"`, "incumbent": `"1.30.0"`, "items[0].arm": `"incumbent"`},
				"v2-r7off": allAdvance,
			}},
		{name: "an arm item in a plan that names no incumbent", base: "base-arm", patch: []string{"-plan.incumbent"},
			want: map[string]map[string]string{
				"v1": allAdvance,
				"v2": {"verdict": `"BLOCK"`, "reasons": `["R6 incumbent: identity: the item says arm incumbent, ` +
					`and the plan names no incumbent"]`},
			}},
		{name: "no thresholds-incumbent.json", base: "base-arm", patch: []string{"thresholds_incumbent=null"},
			want: map[string]map[string]string{
				"v1": allAdvance,
				"v2": {"verdict": `"BLOCK"`, "reasons": `["R6 thresholds-incumbent.json is missing, so the ` +
					`thresholds of the incumbent are unknown"]`},
			}},
		{name: "a reading worse than the incumbent's", base: "judging", patch: []string{worse},
			want: map[string]map[string]string{
				"v1": {"verdict": `"ADVANCE"`, "#keys": v1Keys},
				"v2": {"verdict": `"BLOCK"`, "class": `"regression"`, "comparison.rows[1].result": `"WORSE"`,
					"items[1].failed": `["R7"]`, "rules.R7.pass": "false"},
				"v2-r7off": {"verdict": `"ADVANCE"`, "comparison.rows[1].result": `"WORSE"`,
					"items[1].failed": "[]", "rules.R7.pass": "true", "rules.R7.reasons": "[]"},
				"v3": {"verdict": `"BLOCK"`, "class": `"regression"`, "comparison.rows[1].result": `"WORSE"`,
					"items[1].failed": `["R7"]`, "rules.R7.pass": "false"},
			}},
		{name: "a WORSE reason with a reading that rounds to -0", base: "judging", patch: []string{worse,
			"dirs.incumbent.verdict.readings.persistence_latency_p99_ms=-0.0001"},
			want: map[string]map[string]string{
				"v1": {"verdict": `"ADVANCE"`},
				"v2": {"verdict": `"BLOCK"`, "items[1].reason": worseWhy + `0.0-0` + worseEnd,
					"comparison.rows[1].d": "100"},
				"v2-r7off": {"verdict": `"ADVANCE"`, "items[1].reason": `""`},
				"v3": {"verdict": `"BLOCK"`, "items[1].reason": worseWhy + `0.000` + worseEnd,
					"comparison.rows[1].d": "100"},
			}},
		{name: "a wave the tolerances were not measured on", base: "judging", patch: []string{otherWave},
			want: map[string]map[string]string{
				"v1": allAdvance,
				"v2": {"verdict": `"BLOCK"`, "class": `"invalid"`, "items[0].reason": waveWhy +
					`make arm-tolerance on A/A gates of this plan"`},
				"v2-r7off": {"verdict": `"ADVANCE"`, "items[0].reason": `""`, "comparison.rows[0].result": `"not compared"`},
				"v3": {"verdict": `"BLOCK"`, "class": `"invalid"`, "items[0].reason": waveWhy +
					`laug gate tolerance on A/A gates of this plan"`},
			}},
		{name: "a pair that is no item", base: "base-arm", patch: []string{`plan.items[0].pair="nope"`},
			want: map[string]map[string]string{
				"v1": allAdvance,
				"v2": {"verdict": `"BLOCK"`, "items[0].reason": `"not compared: the pair nope is not an item of ` +
					`this plan"`},
				"v2-r7off": {"verdict": `"ADVANCE"`, "comparison.rows[0].result": `"not compared"`},
			}},
		{name: "the incumbent's own BLOCK", base: "base-arm", patch: []string{blockInc, "dirs.incumbent.exit.exit=1",
			"dirs.incumbent.verdict.exit=1"}, want: map[string]map[string]string{
			"v1": {"items[0].reason": `"load verdict BLOCK: no row decided"`, "items[0].class": `"regression"`,
				"class": `"regression"`},
			"v2": {"items[0].reason": `"incumbent: load verdict BLOCK: no row decided"`,
				"items[0].class": `"invalid"`, "class": `"invalid"`},
			"v2-r7off": {"items[0].reason": `"incumbent: load verdict BLOCK: no row decided"`},
		}},
		{name: "reasons only v2's class rows know", base: "base", patch: []string{
			`dirs.pl.run.result="BLOCK"`, `dirs.pl.run.reason="not compared: x"`, "dirs.pl.exit.exit=1",
			`dirs.kh.run.result="BLOCK"`, `dirs.kh.run.reason="worse than the incumbent: x"`, "dirs.kh.exit.exit=1"},
			want: map[string]map[string]string{
				"v1": {"items[1].class": `"unknown"`, "items[2].class": `"unknown"`, "class": `"unknown"`},
				"v2": {"items[1].class": `"invalid"`, "items[2].class": `"regression"`, "class": `"regression"`},
			}},
		{name: "a host of another type", base: "base-arm", patch: []string{`gate.host.type="other"`},
			want: map[string]map[string]string{
				"v1": allAdvance,
				"v2": {"verdict": `"BLOCK"`, "reasons": `["R6 gate.json records host type other with 32 vCPU and ` +
					`123 GiB, and thresholds.json names a set measured on m8i.8xlarge with 32 vCPU and 123 GiB"]`},
			}},
		{name: "a gate.json host that is a string", base: "base-arm", patch: []string{`gate.host="x"`},
			want:  map[string]map[string]string{"v1": {"verdict": `"ADVANCE"`, "host": `"x"`}},
			fails: map[string]bool{"v2": true, "v2-r7off": true, "v3": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, v := range Versions() {
				in := input(t, c.base, c.patch...)
				if !v.Arm { // v1's gather() reads no thresholds-incumbent.json
					var err error
					if in, err = setPath(in, steps(t, "thresholds_incumbent"), null, true); err != nil {
						t.Fatal(err)
					}
				}
				got, err := judgeAs(t, v, in)
				if c.fails[v.Name] != (err != nil) {
					t.Errorf("%s: error %v, want one: %v", v.Name, err, c.fails[v.Name])
					continue
				}
				for p, want := range c.want[v.Name] {
					var have string
					if path, ok := strings.CutSuffix(p, "#keys"); ok {
						have = keysAt(t, got, path)
					} else {
						have = tojson(at(t, got, p))
					}
					if have != want {
						t.Errorf("%s: %s is %s, want %s", v.Name, p, have, want)
					}
				}
			}
		})
	}
}

// TestJudgeVersions judges copies of the harness's base-arm run under each version: the verdict, and where a case
// names one, the end of a verdict.md line. v1 gathers no thresholds-incumbent.json and renders without DEFS.
// A d of -0 prints 0.0-0 under v2 and 0.000 under v3, and the wave reason names make arm-tolerance under v2 and laug
// gate tolerance under v3.
func TestJudgeVersions(t *testing.T) {
	judging := []string{"compare.rows[0].tolerance=1", "compare.rows[1].tolerance=1",
		"compare.rows[2].tolerance=0.01", "compare.rows[3].tolerance=0.01", "compare.rows[4].tolerance=0.01",
		"compare.wave=" + judgingWave, `compare.evidence="cells/results/aa.txt"`, "compare.gates=20"}
	const steady = "steady/g20261014-140000-steady.verdict.json"
	// 19.7442 less the incumbent's 19.744218057975548 rounds to a d of -0.
	negZero := map[string][]string{steady: {"readings.persistence_latency_p99_ms=19.7442"}}
	otherWave := map[string][]string{"thresholds.json": append(append([]string(nil), judging...),
		`compare.wave=["load:incumbent x","load x"]`)}
	const waveEnd = "on A/A gates of this plan |"
	for _, c := range []struct {
		name, version, verdict string
		patches                map[string][]string
		md                     string // the end of a verdict.md line, when given
	}{
		{"base-arm", "v1", "ADVANCE", nil, ""},
		{"base-arm", "v2-r7off", "ADVANCE", nil, ""},
		{"base-arm", "v3", "ADVANCE", nil, ""},
		{"an incumbent of another release", "v1", "BLOCK", map[string][]string{
			"plan.json": {`incumbent="1.30.0"`}, "incumbent/g20261014-140000-incumbent.json": {
				`server_version="1.30.0"`}}, ""},
		{"a reading worse than the incumbent's", "v2-r7off", "ADVANCE", map[string][]string{
			"thresholds.json": judging, "steady/g20261014-140000-steady.verdict.json": {
				"readings.persistence_latency_p99_ms=100"}}, ""},
		{"a reading worse than the incumbent's", "v2", "BLOCK", map[string][]string{
			"thresholds.json": judging, "steady/g20261014-140000-steady.verdict.json": {
				"readings.persistence_latency_p99_ms=100"}}, ""},
		{"a reading worse than the incumbent's", "v3", "BLOCK", map[string][]string{
			"thresholds.json": judging, "steady/g20261014-140000-steady.verdict.json": {
				"readings.persistence_latency_p99_ms=100"}}, ""},
		{"a d of -0", "v2", "ADVANCE", negZero, "| 19.744 ms | 19.744 ms | 0.0-0 |  | recorded |"},
		{"a d of -0", "v3", "ADVANCE", negZero, "| 19.744 ms | 19.744 ms | 0.000 |  | recorded |"},
		{"a wave the tolerances were not measured on", "v2", "BLOCK", otherWave, "make arm-tolerance " + waveEnd},
		{"a wave the tolerances were not measured on", "v3", "BLOCK", otherWave, "laug gate tolerance " + waveEnd},
	} {
		t.Run(c.version+" "+c.name, func(t *testing.T) {
			v := mustNamed(t, c.version)
			dir := fixtureCopy(t, c.patches)
			res, err := Judge(os.DirFS(dir), v, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Verdict != c.verdict {
				t.Errorf("verdict %s, want %s", res.Verdict, c.verdict)
			}
			if c.md != "" && !strings.Contains(string(res.MD), c.md+"\n") {
				t.Errorf("no verdict.md line ends %q:\n%s", c.md, res.MD)
			}
		})
	}
}

// TestRenderV1 holds v1's RENDER: it has no comparison block, so a verdict with a comparison renders
// as v2's would with the block's lines left out.
func TestRenderV1(t *testing.T) {
	v1, v2 := mustNamed(t, "v1"), mustNamed(t, "v2")
	js := jsonx.Marshal(committedVerdict(t, blockRun))
	got1, err1 := Render(v1, readBack(js))
	got2, err2 := Render(v2, readBack(js))
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	// v2's comparison block runs from the mode line to the blank line after its table.
	lines := strings.SplitAfter(string(got2), "\n")
	from := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "Tolerances from ") || strings.HasPrefix(l, "R7: recorded") {
			from = i
		}
	}
	to := from
	for to >= 0 && to < len(lines) && lines[to] != "| --- | --- | --- | --- | --- | --- |\n" {
		to++
	}
	for to >= 0 && to < len(lines) && lines[to] != "\n" {
		to++
	}
	if from < 0 || to >= len(lines) {
		t.Fatalf("v2's render has no comparison block:\n%s", got2)
	}
	want := strings.Join(append(append([]string(nil), lines[:from]...), lines[to+1:]...), "")
	if string(got1) != want {
		t.Errorf("v1 renders:\n%s\nwant v2's without the comparison:\n%s", got1, want)
	}
}
