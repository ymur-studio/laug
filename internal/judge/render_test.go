package judge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// The tests of verdict.md and the summary start from a committed v2 verdict.json, patched as the rule tables patch
// a fixture (jq_test.go's input), and cover what no committed run prints: a fake gate, a rerun, ITEMS, a | or a
// newline in a cell, notes that are not strings, a judging line with no evidence or host type, and every place
// RENDER and SUMMARY raise a jq error.

// Three committed v2 runs: a judging ADVANCE; a judging BLOCK, class regression, with a note; a refusal on a host
// that was not quiet, in recording mode.
const (
	advanceRun = "g20261018-075357"
	blockRun   = "g20261018-074337"
	refusedRun = "g20261017-171604"
)

// committedVerdict is a run's verdict.json as RENDER reads it, with patches applied as input applies them.
func committedVerdict(t *testing.T, run string, patches ...string) jsonx.Value {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir(run), "verdict.json"))
	if err != nil {
		t.Fatal(err)
	}
	v := readBack(data)
	for _, p := range patches {
		var err error
		if path, ok := strings.CutPrefix(p, "-"); ok {
			v, err = setPath(v, steps(t, path), jsonx.Value{}, true)
		} else {
			path, text, _ := strings.Cut(p, "=")
			nv, perr := jsonx.Parse([]byte(text))
			if perr != nil {
				t.Fatalf("patch %q: %v", p, perr)
			}
			v, err = setPath(v, steps(t, path), nv, false)
		}
		if err != nil {
			t.Fatalf("patch %q: %v", p, err)
		}
	}
	return v
}

// A printCase is one verdict to render or summarize: a committed run and patches, then lines the output must
// hold, or a jq error.
type printCase struct {
	name  string
	run   string
	patch []string
	lines []string // whole lines the output holds
	fails bool     // a jq error: no output
}

// runPrint runs cases through print (Render or Summary) on verdict.json's bytes read back.
func runPrint(t *testing.T, print func(jsonx.Value) ([]byte, error), cases []printCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			js := jsonx.Marshal(committedVerdict(t, c.run, c.patch...))
			got, err := print(readBack(js))
			if err != nil {
				var e *jqError
				if !errors.As(err, &e) {
					t.Fatalf("not a jq error: %v", err)
				}
			}
			switch {
			case c.fails && err == nil:
				t.Errorf("no error; printed:\n%s", got)
			case !c.fails && err != nil:
				t.Errorf("error: %v", err)
			}
			have := strings.Split(string(got), "\n")
			for _, l := range c.lines {
				if !contains(have, l) {
					t.Errorf("no line %q in:\n%s", l, got)
				}
			}
			if err == nil && !bytes.HasSuffix(got, []byte("\n")) {
				t.Errorf("does not end in a newline: %q", got)
			}
		})
	}
}

// renderAs is Render under version v, as runPrint takes it.
func renderAs(v Version) func(jsonx.Value) ([]byte, error) {
	return func(verdict jsonx.Value) ([]byte, error) { return Render(v, verdict) }
}

func contains(lines []string, l string) bool {
	for _, x := range lines {
		if x == l {
			return true
		}
	}
	return false
}

// TestRender holds Render to RENDER (render.go) on the paths the 84 committed v2 runs do not take,
// and on the ones they do.
func TestRender(t *testing.T) {
	runPrint(t, renderAs(mustNamed(t, "v2")), []printCase{
		{name: "advance", run: advanceRun, lines: []string{
			"## laug · verdict for raun 515e50e", "",
			"Gate run g20261018-075357 on server 1.31.0: 3 item(s), 587.039 s from the gate start to this verdict " +
				"on the wall clock (587.04 s monotonic).",
			"| activity_schedule_to_start_over_10ms | 0.93% | 0.92% | +0.0045 points | 0.72 points | ok |",
			"| workflows_completed | 5920 | 5954 | -34 |  | recorded |", "Host quiet: yes.", "**ADVANCE**"}},
		{name: "block with a note", run: blockRun, lines: []string{
			"The incumbent BLOCKed too, on workflow_task_schedule_to_start_p99_ms, activity_schedule_to_start_p99_ms, " +
				"workflow_end_to_end_p95_ms, service_error_rate, workflows_completed: the host, or the cell config " +
				"both arms share, is the likelier cause.",
			"Class: regression.", "**BLOCK**"}},
		{name: "refused", run: refusedRun, lines: []string{"R7: recorded, not judged: no tolerance in " +
			"load/steady-state.yaml.", "Host quiet: no. poll.jsonl is missing, so the host was not seen quiet.",
			"Class: refused."}},
		{name: "fake and dirty", run: advanceRun, patch: []string{"fake=true", "tree_dirty=true"}, lines: []string{
			"## laug · verdict for raun 515e50e (tree dirty) (GATE_FAKE=1: fixtures, no cell ran)"}},
		{name: "fake not true", run: advanceRun, patch: []string{`fake="true"`, "tree_dirty=1"}, lines: []string{
			"## laug · verdict for raun 515e50e"}},
		{name: "unknowns", run: advanceRun, patch: []string{"laug_commit=null", "gate_id=false", "server_tag=null"},
			lines: []string{"## laug · verdict for raun unknown", "Gate run unknown on server unknown: 3 item(s), " +
				"587.039 s from the gate start to this verdict on the wall clock (587.04 s monotonic)."}},
		{name: "cut by ITEMS", run: advanceRun, patch: []string{"items_in_plan=5", "items_limit=3",
			"elapsed_s=null"}, lines: []string{"Gate run g20261018-075357 on server 1.31.0: 3 of 5 planned item(s) " +
			"(ITEMS=3)."}},
		{name: "items_in_plan equal as decimals", run: advanceRun, patch: []string{"items_in_plan=3.0",
			"items_limit=false"}, lines: []string{"Gate run g20261018-075357 on server 1.31.0: 3 item(s) " +
			"(ITEMS=false), 587.039 s from the gate start to this verdict on the wall clock (587.04 s monotonic)."}},
		{name: "items_in_plan not a number", run: advanceRun, patch: []string{`items_in_plan="5"`},
			lines: []string{"Gate run g20261018-075357 on server 1.31.0: 3 item(s), 587.039 s from the gate start " +
				"to this verdict on the wall clock (587.04 s monotonic)."}},
		{name: "elapsed literals keep their text", run: advanceRun, patch: []string{
			`elapsed_s={"wall":485.00,"mono":1E2}`}, lines: []string{"Gate run g20261018-075357 on server 1.31.0: " +
			"3 item(s), 485.00 s from the gate start to this verdict on the wall clock (1E+2 s monotonic)."}},
		{name: "elapsed with no wall", run: advanceRun, patch: []string{`elapsed_s={}`}, lines: []string{
			"Gate run g20261018-075357 on server 1.31.0: 3 item(s), null s from the gate start to this verdict on " +
				"the wall clock (null s monotonic)."}},
		{name: "refused, as a value", run: advanceRun, patch: []string{`refused={"at":"lease"}`}, lines: []string{
			`Refused before the wave: {"at":"lease"}.`}},
		{name: "a cell escapes | and newlines", run: blockRun, patch: []string{`items[0].key="a|b"`,
			`items[0].reason="x | y\nz\r"`, `items[1].observed=false`, `items[1].class=7`, `items[2].expect=null`},
			lines: []string{"| a\\|b | ADVANCE | BLOCK | invalid | x \\| y z\r |",
				"| steady | ADVANCE | none | 7 | load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 99610.046 " +
					"EXCEEDS, activity_schedule_to_start_p99_ms 82063.388 EXCEEDS, workflow_end_to_end_p95_ms " +
					"166311.215 EXCEEDS, service_error_rate 0.075 EXCEEDS, workflows_completed 283 BELOW (0.95 x 20/s " +
					"x 300 s) |"}},
		{name: "a reason that is not a string", run: blockRun, patch: []string{`items[0].reason=null`,
			`items[1].reason=[1,"a|b"]`}, lines: []string{"| incumbent | ADVANCE | BLOCK | invalid | null |"}},
		{name: "rule reasons escape", run: blockRun, patch: []string{`rules.R1={"name":"h|x","pass":false,` +
			`"reasons":["a|b","c\nd",null,1.50,true]}`}, lines: []string{"| R1 h|x | a\\|b; c d; ; 1.50; true |"}},
		{name: "pass is truth", run: blockRun, patch: []string{`rules.R4.pass=0`, `rules.R5.pass=null`,
			`rules.R5.reasons=[]`}, lines: []string{"| R4 outcome | pass |", "| R5 live control |  |"}},
		{name: "notes that are not strings", run: blockRun, patch: []string{`comparison.notes=[1.50,{"a":[1,` +
			`"x"]},null,"n"]`}, lines: []string{"1.50", "{", `  "a": [`, "    1,", `    "x"`, "  ]", "}", "null",
			"n"}},
		{name: "judging with no evidence or host type", run: advanceRun, patch: []string{"comparison.evidence=null",
			"comparison.gates=20.0", "host.type=null", `items[1].key=7`}, lines: []string{"Tolerances from null: 20.0 " +
			"A/A gates of 1.31.0 on a host of no recorded type, the wave incumbent, 7, pl."}},
		{name: "a mode that is not recording", run: refusedRun, patch: []string{"comparison.mode=null"},
			lines: []string{"Tolerances from null: null A/A gates of 1.31.0 on other, the wave incumbent, steady, pl."}},
		{name: "no comparison", run: advanceRun, patch: []string{"comparison=null"}, lines: []string{"",
			"| Rule | Result |"}},
		{name: "a d of -0 and of 4.5e-05", run: advanceRun, patch: []string{"comparison.rows[0].d=-0",
			"comparison.rows[2].d=-0", "comparison.rows[3].d=4.5e-05", "comparison.rows[11].d=-0"}, lines: []string{
			"| workflow_task_schedule_to_start_p99_ms | 16.887 ms | 17.087 ms | 0.0-0 | 1.02 | ok |",
			"| activity_schedule_to_start_over_10ms | 0.93% | 0.92% | 0.00-0 points | 0.72 points | ok |",
			"| frontend_request_over_10ms | 1.08% | 1.16% | +0.0045 points | 0.32 points | ok |",
			"| workflows_completed | 5920 | 5954 | -0 |  | recorded |"}},
		{name: "count cells and other scales", run: advanceRun, patch: []string{`comparison.rows[11].d="x"`,
			`comparison.rows[11].release=12.50`, `comparison.rows[0].scale="count"`, `comparison.rows[1].scale=null`,
			`comparison.rows[1].tolerance=0.5`, `comparison.rows[4].release=null`, `comparison.rows[4].incumbent=1`},
			lines: []string{"| workflow_task_schedule_to_start_p99_ms | 16.886543382355622 | 17.086628703175624 | -0.2 | " +
				"1.02 | ok |",
				"| persistence_latency_p99_ms | 7.901 | 7.975 | -0.074 | 0.50 | ok |",
				"| workflow_end_to_end_over_50ms | none | 100.00% | +0.1988 points | 0.64 points | ok |",
				"| workflows_completed | 12.50 | 5954 | +x |  | recorded |"}},
		{name: "rerun", run: refusedRun, patch: []string{"rerun=true", `class_if_quiet="regression"`,
			`host_noise=["a","b"]`}, lines: []string{"Host quiet: no. a; b. A BLOCK on a host that was not quiet is " +
			"class invalid: rerun (class on a quiet host: regression)."}},
		{name: "quiet is truth", run: refusedRun, patch: []string{"host_quiet=0", "host_noise=null"},
			lines: []string{"Host quiet: yes."}},
		{name: "a verdict that is neither", run: advanceRun, patch: []string{"verdict=null"}, lines: []string{
			"**null**"}},
		{name: "fails: items null", run: advanceRun, patch: []string{"items=null"}, fails: true},
		{name: "fails: an item is a number", run: advanceRun, patch: []string{"items[1]=2"}, fails: true},
		{name: "fails: rules null", run: advanceRun, patch: []string{"rules=null"}, fails: true},
		{name: "fails: rules a number", run: advanceRun, patch: []string{"rules=1"}, fails: true},
		{name: "fails: notes null", run: advanceRun, patch: []string{"comparison.notes=null"}, fails: true},
		{name: "fails: rows a string", run: advanceRun, patch: []string{`comparison.rows="x"`}, fails: true},
		{name: "fails: a d that is a string", run: advanceRun, patch: []string{`comparison.rows[0].d="x"`},
			fails: true},
		{name: "fails: a reading that is true", run: advanceRun, patch: []string{"comparison.rows[2].release=true"},
			fails: true},
		{name: "fails: a share tolerance that is a string", run: advanceRun, patch: []string{
			`comparison.rows[2].tolerance="x"`}, fails: true},
		{name: "fails: a host that is a string", run: advanceRun, patch: []string{`host="x"`}, fails: true},
		{name: "fails: a key that is an array", run: advanceRun, patch: []string{`items[0].key=[1]`}, fails: true},
		{name: "fails: noise null on a host not quiet", run: refusedRun, patch: []string{"host_noise=null"},
			fails: true},
		{name: "fails: a reason an object", run: blockRun, patch: []string{`rules.R4.reasons=[{}]`}, fails: true},
		{name: "fails: elapsed a number", run: advanceRun, patch: []string{"elapsed_s=1"}, fails: true},
		{name: "fails: items true", run: advanceRun, patch: []string{"items=true"}, fails: true},
	})
}

// TestRenderNotAnObject: a verdict that is not an object cannot be indexed, and null renders as RENDER renders it.
func TestRenderNotAnObject(t *testing.T) {
	v2 := mustNamed(t, "v2")
	if _, err := Render(v2, str("x")); err == nil {
		t.Error("Render of a string: no error")
	}
	if _, err := Render(v2, null); err == nil {
		t.Error("Render of null: no error, though null[] cannot be iterated") // .items[] on null
	}
}
