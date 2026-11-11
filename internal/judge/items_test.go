package judge

import "testing"

// The per-item rules R3 and R4, the item's one reason, and its record (items.go). In base, item 0 is
// steady (load), 1 pl and 2 kh (faults); in base-arm, 0 is incumbent (load, arm incumbent), 1 steady, 2 pl.

// TestR3 is R3 contract (r3Of).
func TestR3(t *testing.T) {
	const vj = "steady/g20261014-140000-steady.verdict.json"
	runRules(t, []ruleCase{
		{name: "no exit", patch: []string{"dirs.pl.exit.exit=null"}, reason: "R3 pl: contract: no exit code was recorded"},
		{name: "exit a string", patch: []string{`dirs.pl.exit.exit="0"`},
			reason: "R3 pl: contract: no exit code was recorded"},
		{name: "load exit 2", patch: []string{"dirs.steady.exit.exit=2"},
			reason: "R3 steady: contract: load/verdict.sh exited 2: no reading"},
		{name: "load exit 3", patch: []string{"dirs.steady.exit.exit=3"},
			reason: "R3 steady: contract: load/verdict.sh exited 3: the window is shorter than the thresholds trust"},
		{name: "fault exit 2", patch: []string{"dirs.pl.exit.exit=2"},
			reason: "R3 pl: contract: faults/run.sh exited 2: a refusal or a harness failure"},
		{name: "fault exit 2.0 is 2", patch: []string{"dirs.pl.exit.exit=2.0"},
			reason: "R3 pl: contract: faults/run.sh exited 2: a refusal or a harness failure"},
		{name: "fault exit 3", patch: []string{"dirs.pl.exit.exit=3"}, reason: "R3 pl: contract: faults/run.sh exited 3"},
		{name: "exit 0 with BLOCK", patch: []string{`dirs.pl.run.result="BLOCK"`},
			reason: "R3 pl: contract: faults/run.sh exited 0 with result BLOCK"},
		{name: "exit 1.0 with ADVANCE", patch: []string{"dirs.pl.exit.exit=1.0"},
			reason: "R3 pl: contract: faults/run.sh exited 1.0 with result ADVANCE"},
		{name: "exit 0 with no result", patch: []string{"-dirs.pl.run.result"},
			reason: "R3 pl: contract: faults/run.sh exited 0 with result null"},
		{name: "load exit 1 with ADVANCE", patch: []string{"dirs.steady.exit.exit=1", "dirs.steady.verdict.exit=1"},
			reason: "R3 steady: contract: load/verdict.sh exited 1 with verdict ADVANCE"},
		{name: "the verdict's own exit", patch: []string{"dirs.steady.verdict.exit=1"},
			reason: "R3 steady: contract: " + vj + " says exit 1, and the process exited 0"},
		{name: "the verdict's own exit missing", patch: []string{"-dirs.steady.verdict.exit"},
			reason: "R3 steady: contract: " + vj + " says exit null, and the process exited 0"},
		{name: "a short window", patch: []string{`dirs.steady.verdict.short_window="5m"`},
			reason: "R3 steady: contract: load/verdict.sh read a window under 5m"},
	})
}

// blocks is a load verdict that BLOCKed on its first row, the process and the JSON exiting 1.
var blocks = []string{`dirs.steady.verdict.verdict="BLOCK"`, "dirs.steady.verdict.exit=1", "dirs.steady.exit.exit=1",
	"dirs.steady.verdict.rows[0].blocks=true", `dirs.steady.verdict.rows[0].result="EXCEEDS 38"`}

// TestR4 is R4 outcome (r4Of) and the item's own reason (ownOf).
func TestR4(t *testing.T) {
	block := []string{`dirs.pl.run.result="BLOCK"`, "dirs.pl.exit.exit=1"}
	runRules(t, []ruleCase{
		{name: "an unexpected BLOCK, with its own reason",
			patch:  append([]string{`dirs.pl.run.reason="did not recover within 60 s"`}, block...),
			reason: "R4 pl: expected ADVANCE, observed BLOCK: did not recover within 60 s"},
		{name: "nothing observed", patch: []string{"-dirs.pl.run.result"},
			reason: "R4 pl: expected ADVANCE, observed nothing"},
		{name: "an expected BLOCK of another class",
			patch: append([]string{`plan.items[1].expect="BLOCK"`, `dirs.pl.run.reason="never steady: no reading"`},
				block...),
			reason: "R4 pl: expected BLOCK from a regression, observed BLOCK of class invalid: never steady: no reading"},
		{name: "an expected BLOCK of a regression",
			patch: append([]string{`plan.items[1].expect="BLOCK"`,
				`dirs.pl.run.reason="recovered in 95 s, over a budget of 60 s"`}, block...),
			paths: map[string]string{"rules.R4.pass": "true", "items[1].reason": `""`}},
		{name: "an expected BLOCK that ADVANCEd", patch: []string{`plan.items[1].expect="BLOCK"`},
			reason: "R4 pl: expected BLOCK, observed ADVANCE"},
		{name: "a load verdict's deciding rows", patch: blocks,
			reason: "R4 steady: expected ADVANCE, observed BLOCK: load verdict BLOCK: " +
				"workflow_task_schedule_to_start_p99_ms 49.791 EXCEEDS 38"},
		{name: "two deciding rows, one with no value",
			patch: append([]string{"dirs.steady.verdict.rows[2].blocks=true", "dirs.steady.verdict.rows[2].value=null",
				`dirs.steady.verdict.rows[2].result="EXCEEDS 84"`}, blocks...),
			reason: "R4 steady: expected ADVANCE, observed BLOCK: load verdict BLOCK: " +
				"workflow_task_schedule_to_start_p99_ms 49.791 EXCEEDS 38, workflow_end_to_end_p95_ms null EXCEEDS 84"},
		{name: "no row decided", patch: append(append([]string(nil), blocks...), "dirs.steady.verdict.rows[0].blocks=1"),
			reason: "R4 steady: expected ADVANCE, observed BLOCK: load verdict BLOCK: no row decided"},
		{name: "a failed sweep",
			patch: append(append([]string(nil), blocks...), `dirs.steady.verdict.rows[0].metric="workflows_not_completed"`,
				`dirs.steady.verdict.rows[0].result="the sweep of 2 namespaces failed: timeout"`),
			reason: "R4 steady: expected ADVANCE, observed BLOCK: sweep failed: the sweep of 2 namespaces failed: timeout",
			paths:  map[string]string{"items[0].class": `"invalid"`}},
		{name: "a failed sweep over a line break is not one",
			patch: append(append([]string(nil), blocks...), `dirs.steady.verdict.rows[0].metric="workflows_not_completed"`,
				`dirs.steady.verdict.rows[0].result="the sweep of 2\nnamespaces failed"`),
			reason: "R4 steady: expected ADVANCE, observed BLOCK: load verdict BLOCK: " +
				"workflows_not_completed 49.791 the sweep of 2\nnamespaces failed",
			paths: map[string]string{"items[0].class": `"regression"`}},
		{name: "the incumbent's own reason", base: "base-arm",
			patch: []string{`dirs.incumbent.verdict.verdict="BLOCK"`, "dirs.incumbent.verdict.exit=1",
				"dirs.incumbent.exit.exit=1", "dirs.incumbent.verdict.rows[0].blocks=true"},
			reason: "R4 incumbent: expected ADVANCE, observed BLOCK: incumbent: load verdict BLOCK: " +
				"workflow_task_schedule_to_start_p99_ms 49.791 ok",
			paths: map[string]string{"items[0].class": `"invalid"`}},
	})
}

// TestReasonChain is the item's one reason (reasonOf), the first of: a deadline; any other R2 reason, with its own
// after "; " when the JSON is partial; its own BLOCK when that is not an expected regression; R3; an ADVANCE that
// should have BLOCKed; R5; R6; R7. Its class is reasonClass of it, null for "".
func TestReasonChain(t *testing.T) {
	block := []string{`dirs.pl.run.result="BLOCK"`, "dirs.pl.exit.exit=1"}
	reason := func(item int, text, class string) map[string]string {
		return map[string]string{
			"items[" + string(rune('0'+item)) + "].reason": tojson(str(text)),
			"items[" + string(rune('0'+item)) + "].class":  class,
		}
	}
	runRules(t, []ruleCase{
		{name: "none", paths: reason(1, "", "null")},
		{name: "a deadline first",
			patch: []string{`dirs.pl.exit.stopped_by="KILL"`, "dirs.pl.run.partial=true",
				`dirs.pl.run.reason="did not land: x"`, "dirs.pl.exit.exit=7"},
			paths: reason(1, "deadline: the watchdog stopped it (KILL)", `"deadline"`)},
		{name: "past the deadline before no JSON",
			patch: []string{"dirs.pl.exit.end.wall=1791987200", "dirs.pl.run.partial=true"},
			paths: reason(1, "deadline: it ended 800 s after the gate start, past the deadline at 780 s", `"deadline"`)},
		{name: "R2 and the own reason when partial",
			patch: []string{"dirs.pl.run.partial=true", `dirs.pl.run.reason="did not land: x"`},
			paths: reason(1, "partial JSON: pl/g20261014-140000-pl.json; did not land: x", `"invalid"`)},
		{name: "R2 alone when not partial",
			patch: []string{"dirs.pl.exit=null", `dirs.pl.run.reason="did not land: x"`},
			paths: reason(1, "no exit record: pl/exit.json is missing", `"invalid"`)},
		{name: "its own BLOCK before R3",
			patch: []string{`dirs.pl.run.result="BLOCK"`, `dirs.pl.run.reason="did not recover within 60 s"`},
			paths: map[string]string{"items[1].reason": `"did not recover within 60 s"`, "items[1].class": `"regression"`,
				"items[1].failed": `["R3","R4"]`}},
		{name: "a BLOCK with no reason", patch: append([]string{`dirs.pl.run.reason=""`}, block...),
			paths: reason(1, "BLOCKed with no reason recorded", `"unknown"`)},
		{name: "an expected BLOCK of a regression is no reason",
			patch: append([]string{`plan.items[1].expect="BLOCK"`, `dirs.pl.run.reason="did not recover within 60 s"`},
				block...),
			paths: reason(1, "", "null")},
		{name: "an expected BLOCK of another class",
			patch: append([]string{`plan.items[1].expect="BLOCK"`, `dirs.pl.run.reason="inject failed: x; sweep: y"`},
				block...),
			paths: reason(1, "inject failed: x; sweep: y", `"invalid"`)},
		{name: "R3", patch: []string{"dirs.pl.exit.exit=7"}, paths: reason(1, "contract: faults/run.sh exited 7", `"invalid"`)},
		{name: "R3 before an ADVANCE that should have BLOCKed",
			patch: []string{`plan.items[1].expect="BLOCK"`, "dirs.pl.exit.exit=7"},
			paths: reason(1, "contract: faults/run.sh exited 7", `"invalid"`)},
		{name: "an ADVANCE that should have BLOCKed", patch: []string{`plan.items[1].expect="BLOCK"`},
			paths: reason(1, "control: expected BLOCK, observed ADVANCE", `"control"`)},
		{name: "R5 before R6",
			patch: []string{"dirs.pl.run.during.service_error_rate=0", `dirs.pl.run.server_tag="1.30.0"`},
			paths: reason(1, "control: service_error_rate 0 in the during window, not above its max 0.001", `"control"`)},
		{name: "R6", patch: []string{`dirs.pl.run.server_tag="1.30.0"`},
			paths: reason(1, "identity: server_tag 1.30.0, and the gate SERVER_TAG is 1.31.0", `"invalid"`)},
		{name: "R7", base: "judging",
			patch: []string{"dirs.steady.verdict.readings.persistence_latency_p99_ms=21.0"},
			paths: reason(1, "worse than the incumbent: persistence_latency_p99_ms 21.000 against 19.744 on incumbent, "+
				"+1.256 past a tolerance of 1.00", `"regression"`)},
		{name: "the incumbent's deadline keeps its class", base: "base-arm",
			patch: []string{`dirs.incumbent.exit.stopped_by="TERM"`},
			paths: reason(0, "deadline: the watchdog stopped it (TERM)", `"deadline"`)},
	})
}

// TestItemRecord is the record item returns, as verdict.json keeps it, _r dropped: a load and a fault, a cell from
// the lease or else from a fault's run, the pool_busy of a fault only.
func TestItemRecord(t *testing.T) {
	runRules(t, []ruleCase{
		{name: "base", paths: map[string]string{
			"items[0]": `{"key":"steady","kind":"load","arm":null,"expect":"ADVANCE","observed":"ADVANCE","class":null,` +
				`"reason":"","failed":[],"run_id":"g20261014-140000-steady","cell":"1a2b3c4d","block":0,"exit":0,` +
				`"stopped_by":null,"pool_busy":null,"evidence":["steady/g20261014-140000-steady.json",` +
				`"steady/g20261014-140000-steady.verdict.json"]}`,
			"items[1]": `{"key":"pl","kind":"fault","arm":null,"expect":"ADVANCE","observed":"ADVANCE","class":null,` +
				`"reason":"","failed":[],"run_id":"g20261014-140000-pl","cell":"5e6f7a8b","block":1,"exit":0,` +
				`"stopped_by":null,"pool_busy":{"before":0,"during":0,"drain":0,"after":0},` +
				`"evidence":["pl/g20261014-140000-pl.json"]}`}},
		{name: "a fault's cell from its run", patch: []string{"-dirs.pl.lease.id", `dirs.pl.run.cell="c0ffee00"`},
			paths: map[string]string{"items[1].cell": `"c0ffee00"`}},
		{name: "a load's cell from its lease only", patch: []string{"-dirs.steady.lease.id", `dirs.steady.run.cell="x"`},
			paths: map[string]string{"items[0].cell": "null"}},
		{name: "the arm", base: "base-arm", paths: map[string]string{"items[0].arm": `"incumbent"`, "items[1].arm": "null"}},
		{name: "a gid that is a number", patch: []string{"gate.gate_id=7"},
			paths: map[string]string{"items[1].run_id": `"7-pl"`, "gate_id": "7"}},
	})
}
