package judge

import "testing"

// TestR7 is R7 incumbent (arm7Of, and item's R7): the comparison and its reasons. base has no arm; base-arm
// compares steady with incumbent in recording mode; judging is base-arm with a tolerance on each compare row.
func TestR7(t *testing.T) {
	const (
		ms      = "dirs.steady.verdict.readings.persistence_latency_p99_ms="
		ik      = "R7 incumbent: not compared: "
		pairs   = "steady and incumbent"
		planRun = "load:incumbent workflow_with_single_noop_activity 20/s 50 5m, " +
			"load workflow_with_single_noop_activity 20/s 50 5m, " +
			"fault:persistence-latency workflow_with_single_noop_activity"
	)
	notJudged := []string{`thresholds.thresholds[0].no_data="not_judged"`,
		"-dirs.steady.verdict.readings.workflow_task_schedule_to_start_p99_ms",
		"-dirs.incumbent.verdict.readings.workflow_task_schedule_to_start_p99_ms"}
	runRules(t, []ruleCase{
		{name: "no arm", paths: map[string]string{"comparison": "null", "rules.R7.pass": "true"}},
		{name: "recording", base: "base-arm", paths: map[string]string{
			"comparison.mode": `"recording"`, "comparison.rows[0].result": `"recorded"`, "comparison.rows[0].d": "0",
			"comparison.rows[0].judged": "false", "rules.R7.pass": "true"}},
		{name: "judging", base: "judging", paths: map[string]string{
			"comparison.mode": `"judging"`, "rules.R7.pass": "true",
			"comparison.incumbent": `"incumbent"`, "comparison.release": `"steady"`,
			"comparison.wave_measured": judgingWave, "comparison.measured_with": `"1.31.0"`,
			"comparison.evidence": `"cells/results/aa.txt"`, "comparison.gates": "20", "comparison.notes": "[]",
			"comparison.windows": `{"start_apart_s":0,"length_s":{"release":322,"incumbent":322}}`,
			"comparison.rows[0]": `{"name":"workflow_task_schedule_to_start_p99_ms","scale":"ms",` +
				`"release":49.79083694083693,"incumbent":49.79083694083693,"d":0,"tolerance":1,"judged":true,` +
				`"result":"ok"}`,
			"comparison.rows[5]": `{"name":"activity_schedule_to_start_p99_ms","scale":"ms",` +
				`"release":46.51331719128328,"incumbent":46.51331719128328,"d":0,"tolerance":null,"judged":false,` +
				`"result":"recorded"}`,
			"comparison.rows[11]": `{"name":"workflows_completed","scale":"count","release":5881,"incumbent":5881,` +
				`"d":0,"tolerance":null,"judged":false,"result":"recorded"}`}},
		{name: "a pair that is no item", base: "base-arm", patch: []string{`plan.items[0].pair="nope"`},
			reason: ik + "the pair nope is not an item of this plan",
			paths:  map[string]string{"comparison.rows[0].result": `"not compared"`}},
		{name: "a pair that is a number", base: "judging", patch: []string{"plan.items[0].pair=5"},
			reason: ik + "the pair 5 is not an item of this plan"},
		{name: "no pair", base: "judging", patch: []string{"-plan.items[0].pair"},
			reason: ik + "the pair null is not an item of this plan"},
		{name: "the incumbent did not ADVANCE", base: "judging", patch: []string{`dirs.incumbent.verdict.verdict="BLOCK"`},
			reason: ik + "incumbent did not ADVANCE (verdict BLOCK)"},
		{name: "the incumbent did not ADVANCE, recording", base: "base-arm",
			patch:  []string{`dirs.incumbent.verdict.verdict="BLOCK"`},
			absent: ik + "incumbent did not ADVANCE (verdict BLOCK)",
			paths:  map[string]string{"comparison.rows[0].result": `"not compared"`}},
		{name: "no incumbent verdict", base: "judging", patch: []string{"dirs.incumbent.verdict=null"},
			reason: ik + "incumbent did not ADVANCE (verdict missing)"},
		{name: "no release verdict", base: "judging", patch: []string{"dirs.steady.verdict.verdict=null"},
			reason: "R7 steady: not compared: steady has no verdict to compare"},
		{name: "a tolerance under its floor", base: "judging", patch: []string{"thresholds.compare.rows[0].tolerance=0.05"},
			reason: ik + "compare row workflow_task_schedule_to_start_p99_ms has tolerance 0.05 and floor 0.1 in " +
				"thresholds.json: a tolerance is a number at or above its floor, and a floor a number above 0"},
		{name: "a floor of 0", base: "judging", patch: []string{"thresholds.compare.rows[1].floor=0"},
			reason: ik + "compare row persistence_latency_p99_ms has tolerance 1 and floor 0 in " +
				"thresholds.json: a tolerance is a number at or above its floor, and a floor a number above 0"},
		{name: "a tolerance that is a string", base: "judging", patch: []string{`thresholds.compare.rows[1].tolerance="1"`},
			reason: ik + `compare row persistence_latency_p99_ms has tolerance "1" and floor 0.1 in ` +
				"thresholds.json: a tolerance is a number at or above its floor, and a floor a number above 0"},
		{name: "a tolerance at its floor", base: "judging", patch: []string{"thresholds.compare.rows[1].tolerance=0.10"},
			paths: map[string]string{"rules.R7.pass": "true"}},
		{name: "another wave", base: "judging", patch: []string{`thresholds.compare.wave=["load x"]`},
			reason: ik + "the tolerances in load/steady-state.yaml were measured on the wave [load x], and this plan " +
				"runs [" + planRun + "]: make arm-tolerance on A/A gates of this plan"},
		{name: "no wave", base: "judging", patch: []string{"thresholds.compare.wave=null"},
			reason: ik + "the tolerances in load/steady-state.yaml were measured on the wave [], and this plan " +
				"runs [" + planRun + "]: make arm-tolerance on A/A gates of this plan"},
		{name: "no start", base: "judging", patch: []string{"dirs.steady.run.start=null"},
			reason: ik + "the run JSON of steady or incumbent records no start, so the two windows cannot be seen to " +
				"be paired"},
		{name: "a start in another form", base: "judging", patch: []string{`dirs.steady.run.start="2026-10-14T14:02:30+00:00"`},
			reason: ik + "the run JSON of steady or incumbent records no start, so the two windows cannot be seen to " +
				"be paired"},
		{name: "3 s apart", base: "judging", patch: []string{`dirs.steady.run.start="2026-10-14T14:02:33Z"`},
			reason: ik + "the windows of " + pairs + " started 3 s apart (2 s at most)"},
		{name: "2 s apart", base: "judging", patch: []string{`dirs.incumbent.run.start="2026-10-14T14:02:32Z"`},
			paths: map[string]string{"rules.R7.pass": "true", "comparison.windows.start_apart_s": "2"}},
		{name: "a one-digit minute", base: "judging", patch: []string{`dirs.steady.run.start="2026-10-14T14:2:30Z"`},
			paths: map[string]string{"comparison.windows.start_apart_s": "0"}},
		{name: "no window_s", base: "judging", patch: []string{"dirs.steady.verdict.window_s=null"},
			reason: ik + "the verdict JSON of " + "steady or incumbent records no window_s"},
		{name: "windows of other lengths", base: "judging", patch: []string{"dirs.steady.verdict.window_s=325.0"},
			reason: ik + "the windows of " + pairs + " were 325.0 s and 322 s long (2 s apart at most)"},
		{name: "a worse reading in ms", base: "judging", patch: []string{ms + "21.0"},
			reason: "R7 steady: worse than the incumbent: persistence_latency_p99_ms 21.000 against 19.744 on " +
				"incumbent, +1.256 past a tolerance of 1.00",
			paths: map[string]string{"comparison.rows[1].d": "1.256", "comparison.rows[1].result": `"WORSE"`}},
		{name: "a worse share", base: "judging", patch: []string{"dirs.steady.verdict.readings.frontend_request_over_10ms=0.1"},
			reason: "R7 steady: worse than the incumbent: frontend_request_over_10ms 10.00% against 8.61% on " +
				"incumbent, +1.3900 points past a tolerance of 1.00 points",
			paths: map[string]string{"comparison.rows[3].d": "0.0139"}},
		{name: "a d at its tolerance holds", base: "judging", patch: []string{ms + "20.744218057975548"},
			paths: map[string]string{"comparison.rows[1].d": "1", "comparison.rows[1].result": `"ok"`}},
		{name: "a d that rounds to -0", base: "judging", patch: []string{ms + "19.7442"},
			paths: map[string]string{"comparison.rows[1].d": "-0", "comparison.rows[1].result": `"ok"`}},
		{name: "a d in exponent form", base: "judging", patch: []string{
			"dirs.steady.verdict.readings.frontend_request_over_10ms=0.086145"},
			paths: map[string]string{"comparison.rows[3].d": "4.5e-05"}},
		{name: "no release reading", base: "judging",
			patch:  []string{"-dirs.steady.verdict.readings.workflow_task_schedule_to_start_p99_ms"},
			reason: "R7 steady: not compared: no data on steady for workflow_task_schedule_to_start_p99_ms"},
		{name: "no incumbent reading", base: "judging",
			patch:  []string{`dirs.incumbent.verdict.readings.persistence_latency_p99_ms="x"`},
			reason: "R7 incumbent: not compared: no data on incumbent for persistence_latency_p99_ms"},
		{name: "not produced on both sides", base: "judging",
			patch: append([]string{`thresholds_incumbent.thresholds[0].no_data="not_judged"`}, notJudged...),
			paths: map[string]string{"comparison.rows[0].result": `"not produced"`, "rules.R7.pass": "true"}},
		{name: "not judged on one side only", base: "judging", patch: notJudged,
			reason: "R7 incumbent: not compared: no data on incumbent for workflow_task_schedule_to_start_p99_ms",
			paths:  map[string]string{"comparison.rows[0].result": `"not compared"`}},
		{name: "no data in recording mode", base: "base-arm",
			patch: []string{"-dirs.steady.verdict.readings.workflow_task_schedule_to_start_p99_ms"},
			paths: map[string]string{"rules.R7.pass": "true", "comparison.rows[0].result": `"recorded"`}},
		{name: "worse first on the release, then no data", base: "judging",
			patch: []string{ms + "21.0", "-dirs.incumbent.verdict.readings.workflow_task_schedule_to_start_p99_ms"},
			paths: map[string]string{"rules.R7.reasons": `["incumbent: not compared: no data on incumbent for ` +
				`workflow_task_schedule_to_start_p99_ms","steady: worse than the incumbent: persistence_latency_p99_ms ` +
				`21.000 against 19.744 on incumbent, +1.256 past a tolerance of 1.00"]`}},
		{name: "readings that are an array drop every row", base: "judging",
			patch: []string{"dirs.steady.verdict.readings=[1]"}, paths: map[string]string{"comparison.rows": "[]"}},
		{name: "a note on the measured release", base: "judging", patch: []string{`thresholds.compare.release="1.30.2"`},
			paths: map[string]string{"comparison.notes": `["Tolerances measured with 1.30.2 on both arms; the ` +
				`incumbent of this gate is 1.31.0."]`}},
		{name: "a note on both arms BLOCKing", base: "base-arm",
			patch: []string{`dirs.incumbent.verdict.verdict="BLOCK"`, `dirs.steady.verdict.verdict="BLOCK"`,
				`dirs.incumbent.verdict.deciding=["a",null,2]`},
			paths: map[string]string{"comparison.notes": `["The incumbent BLOCKed too, on a, , 2: the host, or the ` +
				`cell config both arms share, is the likelier cause."]`}},
	})
}
