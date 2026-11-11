package judge

import (
	"errors"
	"testing"
)

// The per-rule tables: one row per reason text a rule writes, each a fixture of the harness with patches.

// TestR1 is R1 harness (r1Of).
func TestR1(t *testing.T) {
	runRules(t, []ruleCase{
		{name: "missing", patch: []string{"harness=null"},
			reason: "R1 harness: harness.json is missing, so make harness-test was not seen to pass"},
		{name: "unreadable", patch: []string{`harness="unreadable"`},
			reason: "R1 harness: harness.json does not parse, so make harness-test was not seen to pass"},
		{name: "not an object", patch: []string{"harness=[0]"},
			reason: "R1 harness: harness.json is missing, so make harness-test was not seen to pass"},
		{name: "exit 1", patch: []string{"harness.exit=1"}, reason: "R1 harness: make harness-test exited 1"},
		{name: "exit as tojson", patch: []string{`harness.exit="0"`},
			reason: `R1 harness: make harness-test exited "0"`},
		{name: "no exit", patch: []string{"-harness.exit"}, reason: "R1 harness: make harness-test exited null"},
		{name: "exit 0.0 is 0", patch: []string{"harness.exit=0.0"}, paths: map[string]string{"rules.R1.pass": "true"}},
	})
}

// TestR2 is R2 coverage (r2AllOf, and r2Of for each item): the plan, its cut, the directories, and each item's.
func TestR2(t *testing.T) {
	const run = "pl/g20261014-140000-pl.json"
	runRules(t, []ruleCase{
		{name: "plan missing", patch: []string{"plan=null"}, reason: "R2 plan: plan.json is missing"},
		{name: "plan unreadable", patch: []string{`plan="unreadable"`}, reason: "R2 plan: plan.json does not parse"},
		{name: "deadline 0", patch: []string{"plan.deadline_s=0"},
			reason: "R2 plan: deadline_s is 0, not seconds above 0, so no item can be seen to end before the deadline"},
		{name: "deadline a string", patch: []string{`plan.deadline_s="780"`},
			reason: `R2 plan: deadline_s is "780", not seconds above 0, so no item can be seen to end before the deadline`},
		{name: "no deadline", patch: []string{"-plan.deadline_s"},
			reason: "R2 plan: deadline_s is null, not seconds above 0, so no item can be seen to end before the deadline"},
		{name: "no items", patch: []string{"plan.items=[]"}, reason: "R2 plan: plan.json lists no items"},
		{name: "items an object", patch: []string{`plan.items={"a":1}`}, reason: "R2 plan: plan.json lists no items"},
		{name: "an item no mapping", patch: []string{`plan.items[0]="steady"`},
			reason: "R2 plan: item 0 is not a mapping"},
		{name: "key not lowercase", patch: []string{`plan.items[1].key="PL"`},
			reason: `R2 plan: item 1: key "PL" is not a lowercase name`},
		{name: "key a number", patch: []string{`plan.items[1].key=7`},
			reason: `R2 plan: item 1: key 7 is not a lowercase name`},
		{name: "kind", patch: []string{`plan.items[1].kind="soak"`},
			reason: `R2 plan: item pl: kind is "soak", not load or fault`},
		{name: "kind of a numbered key", patch: []string{`plan.items[1].key=7`, `-plan.items[1].kind`},
			reason: `R2 plan: item 7: kind is null, not load or fault`},
		{name: "expect", patch: []string{`plan.items[1].expect="maybe"`},
			reason: `R2 plan: item pl: expect is "maybe", not ADVANCE or BLOCK`},
		{name: "listed twice", patch: []string{`plan.items[2].key="pl"`},
			reason: `R2 plan: item "pl" is listed 2 times`},
		{name: "no items_in_plan", patch: []string{"-plan.items_in_plan"},
			reason: "R2 plan: plan.json records no items_in_plan, so it cannot be seen that every item of the plan ran"},
		{name: "cut", patch: []string{"plan.items_in_plan=5"},
			reason: "R2 plan: cut to 3 of 5 items: 2 planned item(s) never ran"},
		{name: "cut by ITEMS", patch: []string{"plan.items_in_plan=5", "gate.items_limit=3"},
			reason: "R2 plan: cut to 3 of 5 items (ITEMS=3): 2 planned item(s) never ran"},
		{name: "fewer in plan", patch: []string{"plan.items_in_plan=2"},
			reason: "R2 plan: plan.json lists 3 items, and its items_in_plan says 2"},
		{name: "a directory no item names",
			patch:  []string{`dirs.zz={"lease":null,"exit":null,"run":null,"verdict":null}`},
			reason: "R2 zz/ holds results for an item the plan does not list"},
		{name: "stopped", patch: []string{`dirs.pl.exit.stopped_by="TERM"`},
			reason: "R2 pl: deadline: the watchdog stopped it (TERM)"},
		{name: "past the deadline", patch: []string{"dirs.pl.exit.end.wall=1791987190.123456"},
			reason: "R2 pl: deadline: it ended 790.123 s after the gate start, past the deadline at 780 s"},
		{name: "at the deadline", patch: []string{"dirs.pl.exit.end.wall=1791987180"},
			absent: "R2 pl: deadline: it ended 780 s after the gate start, past the deadline at 780 s"},
		{name: "no end time, no clock", patch: []string{"clock=null"},
			reason: "R2 pl: no end time: cannot tell whether pl ended before the deadline: clock.json is missing"},
		{name: "no end time, clock unreadable", patch: []string{`clock="unreadable"`},
			reason: "R2 pl: no end time: cannot tell whether pl ended before the deadline: clock.json does not parse"},
		{name: "no end time, no t0.wall", patch: []string{"-clock.t0.wall"},
			reason: "R2 pl: no end time: cannot tell whether pl ended before the deadline: clock.json has no t0.wall"},
		{name: "no end time, no end.wall", patch: []string{`dirs.pl.exit.end.wall="x"`},
			reason: "R2 pl: no end time: cannot tell whether pl ended before the deadline: pl/exit.json has no end.wall"},
		{name: "no directory", patch: []string{"-dirs.pl"}, reason: "R2 pl: no JSON: there is no pl/ directory"},
		{name: "no exit record", patch: []string{"dirs.pl.exit=null"},
			reason: "R2 pl: no exit record: pl/exit.json is missing"},
		{name: "exit record unreadable", patch: []string{`dirs.pl.exit="unreadable"`},
			reason: "R2 pl: no exit record: pl/exit.json does not parse"},
		{name: "no run JSON", patch: []string{"dirs.pl.run=null"}, reason: "R2 pl: no JSON: " + run + " is missing"},
		{name: "no verdict JSON", patch: []string{`dirs.steady.verdict="unreadable"`},
			reason: "R2 steady: no JSON: steady/g20261014-140000-steady.verdict.json does not parse"},
		{name: "partial", patch: []string{"dirs.pl.run.partial=true"}, reason: "R2 pl: partial JSON: " + run},
		{name: "partial is true only", patch: []string{`dirs.pl.run.partial="true"`},
			absent: "R2 pl: partial JSON: " + run},
		{name: "no gid names no file", patch: []string{"gate.gate_id=null", "dirs.pl.run=null"},
			reason: "R2 pl: no JSON: pl/-pl.json is missing"},
	})
}

// TestR5 is R5 live control (ctlMaxOf, r5Of, g5Of).
func TestR5(t *testing.T) {
	const ser = "dirs.pl.run.during.service_error_rate="
	runRules(t, []ruleCase{
		{name: "no max", patch: []string{"thresholds.thresholds[4].max=null"},
			reason: "R5 thresholds.json gives service_error_rate no max to compare with"},
		{name: "no fault item", patch: []string{
			`plan.items=[{"key":"steady","kind":"load","scenario":"workflow_with_single_noop_activity",` +
				`"duration":"5m","rate":20,"max_concurrent":50,"expect":"ADVANCE"}]`, "plan.items_in_plan=1"},
			reason: "R5 the plan has no fault item, so no live control ran"},
		{name: "read null", patch: []string{ser + "null"},
			reason: "R5 pl: control: service_error_rate read null in the during window, not above its max 0.001"},
		{name: "read a string", patch: []string{ser + `"x"`},
			reason: "R5 pl: control: service_error_rate read x in the during window, not above its max 0.001"},
		{name: "during not an object", patch: []string{`dirs.pl.run.during="x"`},
			reason: "R5 pl: control: service_error_rate read null in the during window, not above its max 0.001"},
		{name: "at its max", patch: []string{ser + "0.001"},
			reason: "R5 pl: control: service_error_rate 0.001 in the during window, not above its max 0.001"},
		{name: "a literal keeps its text", patch: []string{ser + "0.0010"},
			reason: "R5 pl: control: service_error_rate 0.0010 in the during window, not above its max 0.001"},
		{name: "above as a decimal, equal as a double", patch: []string{ser + "0.00100000000000000001"},
			absent: "R5 pl: control: service_error_rate 0.00100000000000000001 in the during window, not above its max 0.001"},
	})
}

// TestR6 is R6 identity (thRelOf, thiRelOf, idOf, r6Of, g6Of).
func TestR6(t *testing.T) {
	const tag = "the gate SERVER_TAG is 1.31.0"
	runRules(t, []ruleCase{
		{name: "gate missing", patch: []string{"gate=null"}, reason: "R6 gate.json is missing, so SERVER_TAG is unknown"},
		{name: "gate unreadable", patch: []string{`gate="unreadable"`},
			reason: "R6 gate.json does not parse, so SERVER_TAG is unknown"},
		{name: "no server_tag", patch: []string{"-gate.server_tag"}, reason: "R6 gate.json names no server_tag"},
		{name: "thresholds missing", patch: []string{"thresholds=null"}, reason: "R6 thresholds.json is missing"},
		{name: "thresholds unreadable", patch: []string{`thresholds="unreadable"`},
			reason: "R6 thresholds.json does not parse"},
		{name: "no release_file", patch: []string{"-thresholds.resolved.release_file"},
			reason: "R6 thresholds.json has no resolved.release_file"},
		{name: "incumbent set missing", base: "base-arm", patch: []string{"thresholds_incumbent=null"},
			reason: "R6 thresholds-incumbent.json is missing, so the thresholds of the incumbent are unknown"},
		{name: "incumbent set unreadable", base: "base-arm", patch: []string{`thresholds_incumbent="unreadable"`},
			reason: "R6 thresholds-incumbent.json does not parse, so the thresholds of the incumbent are unknown"},
		{name: "incumbent set, no release_file", base: "base-arm",
			patch:  []string{"-thresholds_incumbent.resolved.release_file"},
			reason: "R6 thresholds-incumbent.json has no resolved.release_file"},
		{name: "no host type", patch: []string{"thresholds.resolved.host=null"},
			reason: "R6 thresholds.json names no host type, so this host cannot be seen to be the one its numbers came from"},
		{name: "another vcpu", patch: []string{"gate.host.vcpu=24"},
			reason: "R6 gate.json records host type m8i.8xlarge with 24 vCPU and 123 GiB, and thresholds.json names " +
				"a set measured on m8i.8xlarge with 32 vCPU and 123 GiB"},
		{name: "memory within 1 GiB", patch: []string{"gate.host.mem_gib=124"},
			absent: "R6 gate.json records host type m8i.8xlarge with 32 vCPU and 124 GiB, and thresholds.json names " +
				"a set measured on m8i.8xlarge with 32 vCPU and 123 GiB"},
		{name: "memory past 1 GiB", patch: []string{"gate.host.mem_gib=121.5"},
			reason: "R6 gate.json records host type m8i.8xlarge with 32 vCPU and 121.5 GiB, and thresholds.json names " +
				"a set measured on m8i.8xlarge with 32 vCPU and 123 GiB"},
		{name: "no host", patch: []string{"gate.host=null"},
			reason: "R6 gate.json records host type null with null vCPU and null GiB, and thresholds.json names " +
				"a set measured on m8i.8xlarge with 32 vCPU and 123 GiB"},
		{name: "server_version", patch: []string{`dirs.steady.run.server_version="1.30.0"`},
			reason: "R6 steady: identity: the cell said server_version 1.30.0, and " + tag},
		{name: "server_tag", patch: []string{"-dirs.pl.run.server_tag"},
			reason: "R6 pl: identity: server_tag null, and " + tag},
		{name: "no incumbent named", base: "base-arm", patch: []string{"plan.incumbent=null"},
			reason: "R6 incumbent: identity: the item says arm incumbent, and the plan names no incumbent"},
		{name: "the incumbent's server_version", base: "base-arm",
			patch:  []string{`dirs.incumbent.run.server_version="1.30.0"`},
			reason: "R6 incumbent: identity: the cell said server_version 1.30.0, and the plan names incumbent 1.31.0"},
		{name: "a run records no thresholds_release", patch: []string{"-dirs.pl.run.thresholds_release"},
			reason: "R6 pl: identity: pl/g20261014-140000-pl.json records no thresholds_release"},
		{name: "a verdict records no thresholds_release", patch: []string{"-dirs.steady.verdict.thresholds_release"},
			reason: "R6 steady: identity: steady/g20261014-140000-steady.verdict.json records no thresholds_release"},
		{name: "another thresholds_release", patch: []string{`dirs.pl.run.thresholds_release="load/releases/1.32.yaml"`},
			reason: "R6 pl: identity: thresholds_release load/releases/1.32.yaml, and thresholds.json names null"},
		{name: "the incumbent's set", base: "base-arm",
			patch:  []string{`thresholds_incumbent.resolved.release_file="load/releases/1.30.yaml"`},
			reason: "R6 incumbent: identity: thresholds_release null, and thresholds-incumbent.json names load/releases/1.30.yaml"},
	})
}

// TestJQErrorSites holds every place JUDGE makes jq fail on a gathered run, each by a patch that reaches it: jq
// stops, laug judge exits 2 with nothing written, and Evaluate returns its error. A number here, and each row's
// line, is a line of judge v2's script. JUDGE's other error sites cannot be reached from files: Gather makes $in and
// $dirs objects, and each dirs member an object. Five rows show what does not fail: a `?` (264, 360), the try in
// epoch (169), `.[]?` (220) and `has` on null. Three of the sites are an alternative operator's left side (220, 416,
// 421): in jq 1.7 `//` does not catch an error (seen on jq-1.7: `(1 | .a) // 2` fails), so neither does Go.
func TestJQErrorSites(t *testing.T) {
	cases := []struct {
		line  string // the line of judge v2's script that holds the jq expression that fails
		name  string
		base  string
		patch []string
		fails bool
	}{
		{"220", "a threshold that is a number", "base", []string{"thresholds.thresholds[0]=5"}, true},
		{"220", "a threshold that is a string, past the control's", "base", []string{`thresholds.thresholds[5]="x"`}, true},
		{"220", "a threshold that is null is no error", "base", []string{"thresholds.thresholds[0]=null"}, false},
		{"220", "thresholds that are a string give nothing", "base", []string{`thresholds.thresholds="x"`}, false},
		{"168", "a compare row named by a number with no scale", "base-arm",
			[]string{"thresholds.compare.rows[0].name=5", "thresholds.compare.rows[0].scale=null"}, true},
		{"168", "a compare row named by a number with a scale is no error", "base-arm",
			[]string{"thresholds.compare.rows[0].name=5"}, false},
		{"168", "a threshold with no name, with an arm", "base-arm", []string{"-thresholds.thresholds[2].name"}, true},
		{"171", "an incumbent threshold that is a number, with a reading missing", "base-arm",
			[]string{"thresholds_incumbent.thresholds[0]=5",
				"-dirs.incumbent.verdict.readings.workflow_task_schedule_to_start_p99_ms"}, true},
		{"171", "an incumbent threshold that is a number, every reading there", "base-arm",
			[]string{"thresholds_incumbent.thresholds[0]=5"}, false},
		{"254", "a measured wave that is a string", "judging", []string{`thresholds.compare.wave="x"`}, true},
		{"254", "a measured wave with an object in it", "judging", []string{`thresholds.compare.wave=["a",{}]`}, true},
		{"254", "a measured wave that is an object of strings is joined", "judging",
			[]string{`thresholds.compare.wave={"a":"x","b":1}`}, false},
		{"264", "readings that are an array drop every row", "base-arm", []string{"dirs.steady.verdict.readings=[1]"}, false},
		{"291", "both arms BLOCKed, the incumbent's deciding a string", "base-arm",
			[]string{`dirs.incumbent.verdict.verdict="BLOCK"`, `dirs.steady.verdict.verdict="BLOCK"`,
				`dirs.incumbent.verdict.deciding="x"`}, true},
		{"318", "an end that is a number", "base", []string{"dirs.pl.exit.end=5"}, true},
		{"318", "a t0 that is a string", "base", []string{`clock.t0="x"`}, true},
		{"323", "a t0 that is a string, no end.wall", "base",
			[]string{`clock.t0="x"`, "dirs.steady.exit.end=null", "dirs.pl.exit.end=null", "dirs.kh.exit.end=null"}, true},
		{"346", "a verdict row that is a number", "base", []string{"dirs.steady.verdict.rows[0]=5"}, true},
		{"346", "verdict rows that are a string give nothing", "base", []string{`dirs.steady.verdict.rows="x"`}, false},
		{"354", "an incumbent fault item whose reason is a number", "base",
			[]string{`plan.items[1].arm="incumbent"`, "dirs.pl.run.reason=5"}, true},
		{"360", "a during window that is a string reads null", "base", []string{`dirs.pl.run.during="x"`}, false},
		{"169", "a start with a fraction is no time", "base-arm",
			[]string{`dirs.steady.run.start="2026-10-14T14:02:30.5Z"`}, false},
		{"382", "a partial run whose reason is a number", "base",
			[]string{"dirs.pl.run.partial=true", "dirs.pl.run.reason=5"}, true},
		{"397", "a BLOCK whose reason is an object, expected ADVANCE", "base",
			[]string{`dirs.pl.run.result="BLOCK"`, `dirs.pl.run.reason={"a":1}`, "dirs.pl.exit.exit=1"}, true},
		{"397", "a BLOCK whose reason is a number, expected BLOCK", "base",
			[]string{`plan.items[1].expect="BLOCK"`, `dirs.pl.run.result="BLOCK"`, "dirs.pl.run.reason=5",
				"dirs.pl.exit.exit=1"}, true},
		{"416", "a resolved block that is a string", "base", []string{`thresholds.resolved="x"`}, true},
		{"421", "a gate host that is a string", "base", []string{`gate.host="x"`}, true},
		{"421", "a gate host that is false reads null", "base", []string{"gate.host=false"}, false},
		{"462", "a t_post that is a number", "base", []string{"clock.t_post=5"}, true},
		{"462", "a t_post that is a number, with no t0.wall", "base", []string{"clock.t_post=5", "-clock.t0.wall"}, false},
		{"375", "has on a run that is not an object is not reached", "base", []string{"dirs.pl.run=[1]"}, false},
	}
	for _, c := range cases {
		t.Run(c.line+" "+c.name, func(t *testing.T) {
			_, err := judgeV2(t, input(t, c.base, c.patch...))
			var e *jqError
			switch {
			case c.fails && !errors.As(err, &e):
				t.Errorf("Evaluate gave no jq error: %v", err)
			case !c.fails && err != nil:
				t.Errorf("Evaluate: %v", err)
			}
		})
	}
}
