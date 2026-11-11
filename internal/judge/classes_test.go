package judge

import (
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// TestClassTable holds class_table, first_clause and reason_class (classes.go). v1 has 26 rows, and v2,
// v2-r7off and v3 have 29. Each probe gets the class `laug judge -classify` prints (Classify), unknown when no
// row matches. With jq 1.7.x present, jq's test of each row's pattern, as Go writes it, agrees with the row's Go
// matcher on each probe's first clause.
func TestClassTable(t *testing.T) {
	for _, v := range Versions() {
		table := classTableOf(v.Features)
		if n := map[bool]int{false: 26, true: 29}[v.Arm]; len(table) != n {
			t.Fatalf("%s has %d class rows in Go, want %d", v.Name, len(table), n)
		}
	}
	if len(v1Table) != 26 {
		t.Errorf("v1Table has %d rows, want 26", len(v1Table))
	}

	probes := []struct{ reason, class string }{
		{"", "unknown"},
		{"a reason no row knows", "unknown"},
		{" control: a blank first", "unknown"},
		{"control:no blank", "unknown"},
		{"recovered in x s", "unknown"},
		{"failed iteration(s) of 50", "unknown"},
		{"3 failed iteration(s)", "unknown"},
		{"3 failed iteration(s) of 50", "regression"},
		{"12 failed iteration(s) ", "regression"},
		{"recovered in 95 s, over a budget of 60 s", "regression"},
		{"load generator exited", "invalid"},
		{"load generator exited 3", "invalid"},
		{"contract: x; deadline: y", "invalid"},
		{"a; control: the second clause", "unknown"},
		{"control: the first; a", "control"},
		{"; control: an empty first clause", "unknown"},
		{"deadline: a final newline\n", "deadline"},
		{"\ncontrol: a newline first", "unknown"},
		{"sweep failed: before sweep: ", "invalid"},
		{"sweep found no workflows in 60 s", "regression"},
		{"incumbent: load verdict BLOCK: x", "invalid"},
	}
	for _, r := range classTable {
		p := strings.TrimPrefix(r.pattern, "^")
		switch r.pattern {
		case `^recovered in [0-9]`:
			p = "recovered in 7"
		case `^[0-9]+ failed iteration\(s\) `:
			p = "40 failed iteration(s) of 1000"
		}
		probes = append(probes, struct{ reason, class string }{p + "x", r.class})
	}
	for _, p := range probes {
		if got := ReasonClass(p.reason); got != p.class {
			t.Errorf("ReasonClass(%q) = %s, want %s", p.reason, got, p.class)
		}
	}
	if got := reasonClassIn(classTable, jsonx.Float(5)); got != "unknown" {
		t.Errorf("reason_class of a number = %s, want unknown", got)
	}
	jq := maybeJQ(t)
	if jq == "" {
		return
	}
	// jq itself: test($re) with each row's pattern over each probe's first clause, one line a pair, rows in order.
	var in []jsonx.Value
	for _, row := range classTable {
		for _, p := range probes {
			in = append(in, object("re", str(row.pattern), "s", str(firstClause(str(p.reason)))))
		}
	}
	out, ok := jqRun(jq, jsonx.MarshalCompact(jsonx.Arr(in)), "-c", ".[] | .re as $re | .s | test($re)")
	if !ok {
		t.Fatalf("jq: %s", out)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(in) {
		t.Fatalf("jq gave %d lines for %d pairs", len(got), len(in))
	}
	k := 0
	for _, row := range classTable {
		for _, p := range probes {
			if want := tojson(jsonx.Bool(row.match(firstClause(str(p.reason))))); got[k] != want {
				t.Errorf("row %q on %q: jq %s, Go %s", row.pattern, p.reason, got[k], want)
			}
			k++
		}
	}
}

// TestClass0 is the gate's class (class0Of, j.rerun, output): the first by class_rank of refused, harness, each item's
// class, invalid and control; a BLOCK on a host that was not quiet is invalid with rerun, unless refused or harness.
func TestClass0(t *testing.T) {
	class := func(class, rerun, ifQuiet string) map[string]string {
		return map[string]string{"class": class, "rerun": rerun, "class_if_quiet": ifQuiet}
	}
	const (
		refused  = `gate.refused="preflight: the host is busy"`
		harness  = "harness.exit=1"
		busy     = "polls[3].busy=1"
		vcpu     = "gate.host.vcpu=24"
		control  = `plan.items[1].expect="BLOCK"`
		stopped  = `dirs.pl.exit.stopped_by="TERM"`
		khBlock  = `dirs.kh.run.result="BLOCK"`
		khExit1  = "dirs.kh.exit.exit=1"
		khRegr   = `dirs.kh.run.reason="did not recover within 60 s"`
		khNoRow  = `dirs.kh.run.reason="a reason no row knows"`
		onlyLoad = `plan.items=[{"key":"steady","kind":"load","scenario":"workflow_with_single_noop_activity",` +
			`"duration":"5m","rate":20,"max_concurrent":50,"expect":"ADVANCE"}]`
	)
	runRules(t, []ruleCase{
		{name: "ADVANCE", paths: class("null", "false", "null")},
		{name: "refused", patch: []string{refused}, reason: "refused: preflight: the host is busy",
			paths: class(`"refused"`, "false", "null")},
		{name: "refused before harness", patch: []string{refused, harness}, paths: class(`"refused"`, "false", "null")},
		{name: "harness", patch: []string{harness, stopped}, paths: class(`"harness"`, "false", "null")},
		{name: "deadline before regression", patch: []string{stopped, khBlock, khExit1, khRegr},
			paths: class(`"deadline"`, "false", "null")},
		{name: "regression before unknown, control and invalid",
			patch: []string{khBlock, khExit1, khRegr, control, vcpu, `dirs.steady.run.server_version="x"`},
			paths: class(`"regression"`, "false", "null")},
		{name: "unknown before control", patch: []string{khBlock, khExit1, khNoRow, control},
			paths: class(`"unknown"`, "false", "null")},
		{name: "control before invalid", patch: []string{control, vcpu}, paths: class(`"control"`, "false", "null")},
		{name: "invalid: a plan problem", patch: []string{`plan.deadline_s="780"`},
			paths: class(`"invalid"`, "false", "null")},
		{name: "a deadline of 0 makes every item late", patch: []string{"plan.deadline_s=0"},
			paths: class(`"deadline"`, "false", "null")},
		{name: "invalid: no control max", patch: []string{"thresholds.thresholds[4].max=null"},
			paths: class(`"invalid"`, "false", "null")},
		{name: "invalid: the gate's identity", patch: []string{vcpu}, paths: class(`"invalid"`, "false", "null")},
		// The fault items' compose projects are in every poll and no longer leased: noise, so a rerun.
		{name: "control: no fault item, before the directories no item names",
			patch: []string{onlyLoad, "plan.items_in_plan=1"}, paths: class(`"invalid"`, "true", `"control"`)},
		{name: "no control for a plan with problems",
			patch: []string{onlyLoad, "plan.items_in_plan=1", `plan.deadline_s="780"`},
			paths: class(`"invalid"`, "true", `"invalid"`)},
		{name: "a BLOCK on a busy host is a rerun", patch: []string{khBlock, khExit1, khRegr, busy},
			paths: class(`"invalid"`, "true", `"regression"`)},
		{name: "refused on a busy host", patch: []string{refused, busy}, paths: class(`"refused"`, "false", "null")},
		{name: "harness on a busy host", patch: []string{harness, busy}, paths: class(`"harness"`, "false", "null")},
		{name: "an ADVANCE on a busy host stands", patch: []string{busy},
			paths: map[string]string{"verdict": `"ADVANCE"`, "class": "null", "rerun": "false", "host_quiet": "false"}},
		{name: "refused is not null only", patch: []string{"gate.refused=false"},
			paths: map[string]string{"verdict": `"ADVANCE"`, "refused": "null"}},
	})
	// No candidate at all is unknown: not reached through a BLOCK, since every rule that fails gives one.
	j := judging{refused: null, ctlMax: constant("0.001"), planned: []jsonx.Value{object("kind", str("fault"))}}
	if got := j.class0Of(); got != "unknown" {
		t.Errorf("class0 with no candidate = %s, want unknown", got)
	}
	for c, want := range map[string]int{"refused": 0, "harness": 1, "deadline": 2, "regression": 3, "unknown": 4,
		"control": 5, "invalid": 6, "other": 9} {
		if got := classRank(c); got != want {
			t.Errorf("class_rank(%s) = %d, want %d", c, got, want)
		}
	}
}

// TestHostNoise is $noise (noiseOf): what keeps the host from being seen quiet, in its order.
func TestHostNoise(t *testing.T) {
	noise := func(lines ...string) map[string]string {
		return map[string]string{"host_noise": tojson(strs(lines)), "host_quiet": tojson(jsonx.Bool(len(lines) == 0))}
	}
	const unleased = ", which the gate did not lease"
	runRules(t, []ruleCase{
		{name: "quiet", paths: noise()},
		{name: "a raun saw a busy cell", patch: []string{"dirs.pl.run.pool_busy.during=2"},
			paths: noise("pl: laugd showed 2 busy cell(s) in its during window")},
		{name: "a load's pool_busy is not read", patch: []string{`dirs.steady.run.pool_busy={"during":3}`}, paths: noise()},
		{name: "a busy count that is no number", patch: []string{`dirs.pl.run.pool_busy.during="2"`}, paths: noise()},
		{name: "a pool_busy that is no object", patch: []string{"dirs.pl.run.pool_busy=5"}, paths: noise()},
		{name: "no poll record", patch: []string{"polls=null"},
			paths: noise("poll.jsonl is missing, so the host was not seen quiet")},
		{name: "polls unreadable", patch: []string{`polls="unreadable"`},
			paths: noise("poll.jsonl does not parse, so the host was not seen quiet")},
		{name: "no poll", patch: []string{"polls=[]"}, paths: noise("poll.jsonl holds no poll, so the host was not seen quiet")},
		{name: "busy at laugd", patch: []string{"polls[1].busy=3", "polls[2].busy=2"},
			paths: noise("the poller saw up to 3 busy cell(s) at laugd, in 2 of 76 polls")},
		{name: "the last of equal maxima", patch: []string{"polls[1].busy=2.0", "polls[2].busy=2"},
			paths: noise("the poller saw up to 2 busy cell(s) at laugd, in 2 of 76 polls")},
		{name: "the last of equal maxima, the other way", patch: []string{"polls[1].busy=2", "polls[2].busy=2.0"},
			paths: noise("the poller saw up to 2.0 busy cell(s) at laugd, in 2 of 76 polls")},
		{name: "laugd did not answer", patch: []string{"polls[4].busy=null"},
			paths: noise("laugd did not answer 1 of 76 polls")},
		{name: "docker compose ls failed", patch: []string{`polls[4].projects="x"`},
			paths: noise("docker compose ls failed in 1 of 76 polls")},
		{name: "a poll that is no object", patch: []string{"polls[0]=5"},
			paths: noise("laugd did not answer 1 of 76 polls", "docker compose ls failed in 1 of 76 polls")},
		{name: "projects the gate did not lease, each once, by bytes",
			patch: []string{`polls[0].projects=["laug-1a2b3c4d","zz","(not laug's)","zz"]`, `polls[9].projects=[5]`},
			paths: noise("the poller saw compose project 5 up"+unleased,
				"the poller saw compose project (not laug's) up"+unleased, "the poller saw compose project zz up"+unleased)},
		{name: "a lease's project from its id", patch: []string{"-dirs.pl.lease.project"}, paths: noise()},
		{name: "every kind, in order",
			patch: []string{"dirs.kh.run.pool_busy.after=1", "polls[3].busy=4", "polls[5].busy=null",
				"polls[6].projects=null", `polls[7].projects=["x"]`},
			paths: noise("kh: laugd showed 1 busy cell(s) in its after window",
				"the poller saw up to 4 busy cell(s) at laugd, in 1 of 76 polls", "laugd did not answer 1 of 76 polls",
				"docker compose ls failed in 1 of 76 polls", "the poller saw compose project x up"+unleased)},
	})
}
