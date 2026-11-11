package judge

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The committed trap runs, by the form that wrote each (internal/rejudge's expected file). The two T1
// runs are dev host runs, read from their copy (runDir).
var trapRuns = map[string]Form{
	"g20261014-213037": T1, "g20261014-214041": T1, "g20261017-014142": T2, "g20261017-014632": T2,
}

// trapScript is trap_verdict's lines that read the run and write the two files, cut from gate/lib.sh (T3) from the
// line that reads gate.json to the one that writes verdict.md, as a script over "$1" the directory and the shell
// variables the trap reads: gate_id, SERVER_TAG, step, why, DEADLINE_S and driver. T2 and T1 are T3 less what the
// history added (seen with git log -S and git show of gate/run.sh and gate/lib.sh in the frozen checkout): 0097a57
// added incumbent and comparison, 06ffb0b the driver in the reason, and 7edb11d the boolean tree_dirty. Each change
// must apply once.
func trapScript(t *testing.T, f Form) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(gateDir, "lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(data), "\ntrap_verdict() {")
	body, _, ok2 := strings.Cut(body, "\n}\n")
	_, body, ok3 := strings.Cut(body, "\n  g=\"$(jq -c . \"$dir/gate.json\"")
	body, _, ok4 := strings.Cut(body, "\n  combined=1")
	if !ok || !ok2 || !ok3 || !ok4 || !strings.HasSuffix(body, `> "$dir/verdict.md" || return 1`) {
		t.Fatal("gate/lib.sh: no trap_verdict that reads gate.json and writes verdict.md")
	}
	body = `  g="$(jq -c . "$dir/gate.json"` + body
	change := func(old, new string) {
		if strings.Count(body, old) != 1 {
			t.Fatalf("%s: trap_verdict holds %q %d times, want once", f, old, strings.Count(body, old))
		}
		body = strings.Replace(body, old, new, 1)
	}
	if f == T1 || f == T2 {
		change(`incumbent: ($pl.incumbent // null), `, "")
		change(`comparison: null, `, "")
	}
	if f == T1 {
		change(`tree_dirty: (if ($g.tree_dirty | type) == "boolean" then $g.tree_dirty else null end)`,
			`tree_dirty: ($g.tree_dirty // null)`)
		change(`"stopped: \($drv) stopped at step`, `"stopped: gate/run.sh stopped at step`)
	}
	if strings.Count(body, "|| return 1") != 2 {
		t.Fatal("trap_verdict: want 2 `|| return 1`")
	}
	body = strings.ReplaceAll(body, "|| return 1", "|| exit 1")
	return "export LC_ALL=C\n" +
		`dir="$1" gate_id="$2" SERVER_TAG="$3" step="$4" why="$5" DEADLINE_S="$6" driver="$7"` + "\n" + body + "\n"
}

// shellTrap runs form f's trap lines on dir with args and gives the two files it wrote, or ok false when it failed.
func shellTrap(t *testing.T, bash string, f Form, dir string, a TrapArgs) (js, md []byte, ok bool) {
	t.Helper()
	deadline := ""
	if !a.Deadline.IsNull() {
		deadline = a.Deadline.String()
	}
	cmd := exec.Command(bash, "-c", trapScript(t, f), "trap", dir, a.GateID, a.ServerTag, a.Step, a.Why, deadline,
		strings.TrimPrefix(a.Driver, "gate/"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil, nil, false
	} else if err != nil {
		t.Fatal(err)
	}
	js, err = os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil {
		t.Fatal(err)
	}
	md, err = os.ReadFile(filepath.Join(dir, "verdict.md"))
	if err != nil {
		t.Fatal(err)
	}
	return js, md, true
}

// TestTrapArgsOf reads the args back from each committed trap verdict, and refuses a verdict a judge wrote and a
// reason that does not name the step as a trap does.
func TestTrapArgsOf(t *testing.T) {
	want := map[string]string{
		"g20261014-213037": `g20261014-213037|1.31.0|780|lease steady|signal TERM|gate/run.sh`,
		"g20261014-214041": `g20261014-214041|1.31.0|780|barrier|signal TERM|gate/run.sh`,
		"g20261017-014142": `g20261017-014142|1.31.0|780|wave|the workflow was cancelled|gate/step.sh`,
		"g20261017-014632": `g20261017-014632|1.31.0|780|wave|signal TERM|gate/run.sh`,
	}
	for run := range trapRuns {
		a, err := TrapArgsOf(readTestFile(t, filepath.Join(runDir(run), "verdict.json")))
		got := strings.Join([]string{a.GateID, a.ServerTag, a.Deadline.String(), a.Step, a.Why, a.Driver}, "|")
		if err != nil || got != want[run] {
			t.Errorf("%s: %s, %v, want %s", run, got, err, want[run])
		}
	}
	const reason = `"stopped: gate/run.sh stopped at step wave (signal TERM) before gate/combine.sh judged; ` +
		`nothing was judged"`
	for name, text := range map[string]string{
		"a judge's verdict":  string(readTestFile(t, filepath.Join(runDir(advanceRun), "verdict.json"))),
		"not JSON":           "{",
		"an array":           "[]",
		"another step":       `{"gate_id":"g","server_tag":"s","stopped_at":"barrier","reasons":[` + reason + `]}`,
		"two reasons":        `{"gate_id":"g","server_tag":"s","stopped_at":"wave","reasons":[` + reason + `,"x"]}`,
		"no reason":          `{"gate_id":"g","server_tag":"s","stopped_at":"wave","reasons":[]}`,
		"a gate_id not text": `{"gate_id":1,"server_tag":"s","stopped_at":"wave","reasons":[` + reason + `]}`,
		"a reason cut short": `{"gate_id":"g","server_tag":"s","stopped_at":"wave","reasons":` +
			`["stopped: gate/run.sh stopped at step wave (x)"]}`,
		"a step that is null": `{"gate_id":"g","server_tag":"s","stopped_at":null,"reasons":[` + reason + `]}`,
	} {
		if a, err := TrapArgsOf([]byte(text)); !errors.Is(err, ErrTrap) {
			t.Errorf("%s: %+v, %v, want ErrTrap", name, a, err)
		}
	}
}

// readTestFile is the file at name.
func readTestFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestRerenderTrap re-renders each committed trap run under its own form, which gives both of its files, and under
// the other two, which do not. Then it holds each form to trap_verdict's own lines (trapScript): on the 4 runs and on
// copies with gate.json, clock.json and plan.json changed, Go and the shell must write the same two files, or both
// fail. Without bash and jq 1.7.x only the first part runs.
func TestRerenderTrap(t *testing.T) {
	for run, form := range trapRuns {
		dir := runDir(run)
		want := [2][]byte{readTestFile(t, filepath.Join(dir, "verdict.json")),
			readTestFile(t, filepath.Join(dir, "verdict.md"))}
		a, err := TrapArgsOf(want[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range Forms {
			js, md, err := RerenderTrap(os.DirFS(dir), f, a)
			same := err == nil && bytes.Equal(js, want[0]) && bytes.Equal(md, want[1])
			if same != (f == form) {
				t.Errorf("%s as %s: both files %v (%v), want %v", run, f, same, err, f == form)
			}
		}
	}
	if _, _, err := RerenderTrap(os.DirFS(runDir("g20261017-014142")), T1, TrapArgs{
		Driver: "gate/step.sh"}); !errors.Is(err, ErrTrap) {
		t.Errorf("T1 with gate/step.sh: %v, want ErrTrap", err)
	}
	if _, _, err := RerenderTrap(os.DirFS(results), Form("T4"), TrapArgs{}); !errors.Is(err, ErrTrap) {
		t.Errorf("T4: %v, want ErrTrap", err)
	}

	tools := maybeShell(t)
	if tools.bash == "" {
		return
	}
	const base = "g20261017-014632"
	args := func(run string) TrapArgs {
		a, err := TrapArgsOf(readTestFile(t, filepath.Join(runDir(run), "verdict.json")))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	type change struct {
		file, text string // the file's new text; "-" removes it, "/" makes it a directory
	}
	cases := []struct {
		name    string
		run     string
		changes []change
		args    func(*TrapArgs)
		fails   bool // the trap fails: --argjson refuses the text, or jq cannot index or subtract
	}{
		{name: "as committed", run: "g20261014-213037"},
		{name: "as committed", run: "g20261014-214041"},
		{name: "as committed", run: "g20261017-014142"},
		{name: "as committed", run: base},
		{name: "tree dirty", changes: []change{{"gate.json", `{"tree_dirty":true,"laug_commit":"abc"}`}}},
		{name: "tree_dirty a string", changes: []change{{"gate.json", `{"tree_dirty":"x"}`}}},
		{name: "a fake gate", changes: []change{{"gate.json", `{"fake":true,"refused":"no","host":{"vcpu":2}}`}}},
		{name: "fake not true", changes: []change{{"gate.json", `{"fake":1,"laug_commit":123,"items_limit":1.50}`}}},
		{name: "no gate.json", changes: []change{{"gate.json", "-"}}},
		{name: "gate.json a directory", changes: []change{{"gate.json", "/"}}},
		{name: "gate.json empty", changes: []change{{"gate.json", " \n"}}, fails: true},
		{name: "gate.json two values", changes: []change{{"gate.json", "{} {}"}}, fails: true},
		{name: "gate.json a parse error", changes: []change{{"gate.json", `{"tree_dirty":true} {`}}},
		{name: "gate.json a number", changes: []change{{"gate.json", "5"}}, fails: true},
		{name: "gate.json false", changes: []change{{"gate.json", "false"}}},
		{name: "gate.json null", changes: []change{{"gate.json", "null"}}},
		{name: "no clock.json", changes: []change{{"clock.json", "-"}}},
		{name: "clock.json empty", changes: []change{{"clock.json", ""}}, fails: true},
		{name: "no mono", changes: []change{{"clock.json", `{"t0":{"wall":1.5},"t_post":{"wall":2.25}}`}},
			fails: true},
		{name: "a wall that is a string", changes: []change{{"clock.json", `{"t0":{"wall":"1"},"t_post":{"wall":2}}`}}},
		{name: "t0 a number", changes: []change{{"clock.json", `{"t0":5,"t_post":{"wall":2}}`}}, fails: true},
		{name: "times that round", changes: []change{{"clock.json",
			`{"t0":{"wall":1792013437.924,"mono":0.0004},"t_post":{"wall":1792013478.9565,"mono":1e-9}}`}}},
		{name: "no plan.json", changes: []change{{"plan.json", "-"}}},
		{name: "plan.json empty", changes: []change{{"plan.json", ""}}},
		{name: "plan.json two values", changes: []change{{"plan.json", "{} {}"}}, fails: true},
		{name: "plan.json an array", changes: []change{{"plan.json", "[]"}}, fails: true},
		{name: "plan.json with an incumbent", changes: []change{{"plan.json",
			`{"incumbent":"1.30.0","items_in_plan":3.0}`}}},
		{name: "no deadline", args: func(a *TrapArgs) { a.Deadline = null }},
		{name: "a why and a step with parentheses", args: func(a *TrapArgs) {
			a.Why, a.Step = "signal (TERM)", "lease (steady)"
		}},
		{name: "step.sh", args: func(a *TrapArgs) { a.Driver = "gate/step.sh" }},
	}
	for _, c := range cases {
		run := c.run
		if run == "" {
			run = base
		}
		a := args(run)
		if c.args != nil {
			c.args(&a)
		}
		for _, f := range Forms {
			if f == T1 && a.Driver != "gate/run.sh" {
				continue // T1's text names gate/run.sh only
			}
			t.Run(string(f)+" "+c.name+" "+run, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.CopyFS(dir, os.DirFS(runDir(run))); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"verdict.json", "verdict.md"} {
					if err := os.Remove(filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
				for _, ch := range c.changes {
					p := filepath.Join(dir, ch.file)
					var err error
					switch ch.text {
					case "-":
						err = os.Remove(p)
					case "/":
						if err = os.Remove(p); err == nil {
							err = os.Mkdir(p, 0o755)
						}
					default:
						err = os.WriteFile(p, []byte(ch.text), 0o644)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				js, md, err := RerenderTrap(os.DirFS(dir), f, a)
				if err != nil && !errors.Is(err, ErrTrap) {
					t.Fatalf("not an ErrTrap: %v", err)
				}
				if c.fails != (err != nil) {
					t.Errorf("error %v, want one: %v", err, c.fails)
				}
				sjs, smd, ok := shellTrap(t, tools.bash, f, dir, a)
				switch {
				case ok && err != nil:
					t.Errorf("Go fails and the shell does not: %v", err)
				case !ok && err == nil:
					t.Errorf("the shell fails and Go does not; Go wrote:\n%s", js)
				case ok && (!bytes.Equal(js, sjs) || !bytes.Equal(md, smd)):
					t.Errorf("Go and the shell differ:\n%s\n%s", firstLineDiff(string(sjs), string(js)),
						firstLineDiff(string(smd), string(md)))
				}
			})
		}
	}
}
