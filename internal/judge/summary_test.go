package judge

import (
	"testing"
)

// TestSummary holds Summary to SUMMARY (summary.go), which runs with no DEFS, on committed v2 verdicts
// and on patches of them: a class that is not null, a rerun, a fake gate, a refusal that is not a string, a rule
// label past 17 code points, and the places SUMMARY raises a jq error.
func TestSummary(t *testing.T) {
	runPrint(t, Summary, []printCase{
		{name: "advance", run: advanceRun, lines: []string{
			"laug · gate g20261018-075357, server 1.31.0: ADVANCE",
			"R1 harness       pass", "R5 live control  pass", "R7 incumbent     pass", "host quiet       yes"}},
		{name: "block", run: blockRun, lines: []string{"laug · gate g20261018-074337, server 1.31.0: BLOCK, class " +
			"regression", "R1 harness       pass"}},
		{name: "refused", run: refusedRun, lines: []string{
			"laug · gate g20261017-171604, server 1.31.0: BLOCK, class refused",
			"host quiet       no: poll.jsonl is missing, so the host was not seen quiet"}},
		{name: "rerun and fake", run: blockRun, patch: []string{"rerun=1", "fake=true", "class=false",
			"gate_id=null", "server_tag=false"}, lines: []string{"laug · gate unknown, server unknown: BLOCK, class " +
			"false (rerun: the host was not quiet) (GATE_FAKE=1: fixtures, no cell ran)"}},
		{name: "a refusal that is a value", run: advanceRun, patch: []string{`refused={"at":[1,2.50]}`},
			lines: []string{`refused          {"at":[1,2.50]}`}},
		{name: "labels cut at 17 code points", run: blockRun, patch: []string{
			`rules.R1.name="a much longer name"`, `rules.R2.name="ééééééééééééééé"`, `rules.R3.name=1.50`,
			`rules.R4.pass=null`, `rules.R4.reasons=[]`, `rules.R5.reasons=["a|b",null,2]`},
			lines: []string{"R1 a much longer pass", "R2 éééééééééééééépass", "R3 1.50          pass", "R4 outcome       ",
				"R5 live control  a|b; ; 2"}},
		{name: "quiet is truth", run: refusedRun, patch: []string{`host_quiet="no"`, "host_noise=null"},
			lines: []string{"host quiet       yes"}},
		{name: "fails: rules null", run: advanceRun, patch: []string{"rules=null"}, fails: true},
		{name: "fails: reasons null on a failing rule", run: advanceRun, patch: []string{"rules.R2.pass=false",
			"rules.R2.reasons=null"}, fails: true},
		{name: "fails: a rule a string", run: advanceRun, patch: []string{`rules.R3="x"`}, fails: true},
		{name: "fails: noise an object of arrays", run: refusedRun, patch: []string{`host_noise={"a":[1]}`},
			fails: true},
	})
}

// TestClassify holds Classify, what `laug judge -classify <reason>` prints: reason_class and a newline, for a
// row of each class, unknown, the empty reason a missing argument gives, a first clause cut at "; ", and bad UTF-8
// before a prefix.
func TestClassify(t *testing.T) {
	for reason, want := range map[string]string{
		"":                                        "unknown",
		"refused: lease steady":                   "refused",
		"harness: make harness-test exited 2":     "harness",
		"deadline: ended past 780 s":              "deadline",
		"never steady: a=1; b=2":                  "invalid",
		"recovered in 9 s":                        "regression",
		"12 failed iteration(s) of 400":           "regression",
		"control: service_error_rate never rose":  "control",
		"no such reason":                          "unknown",
		"x; control: two clauses":                 "unknown",
		"\xffcontrol: a byte that is not UTF-8":   "unknown",
		"control: \xff after the prefix":          "control",
		"worse than the incumbent: d over 1.02\n": "regression",
	} {
		if got := string(Classify(reason)); got != want+"\n" {
			t.Errorf("Classify(%q) = %q, want %q", reason, got, want+"\n")
		}
	}
}
