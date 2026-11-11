package main

// laug gate control: one of gate/controls' diffs, a change the gate must catch, applied to HEAD in a worktree of its
// own under TMPDIR; then a gate on fakes runs there, with make harness-test for real as R1, and laug says whether the
// gate did what the control's first line says it must.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ymur-studio/laug/internal/script"
)

// controlsDir holds the controls, one file a control: <name>.diff, one comment line that names it and says what it
// must make the gate do, then git diff's output, which git apply reads past that line.
const controlsDir = "gate/controls"

// gateControl is one control and what a gate on its tree must do.
type gateControl struct {
	name string
	// cells says it acts only on real cells: a gate on fakes starts no cell and runs no item's own script, so on
	// fakes the gate ADVANCEs, R1 held. Else make harness-test fails on its tree, and the gate BLOCKs on R1 on fakes as
	// on cells.
	cells bool
	class string // the class a gate on cells must BLOCK with
	rule  string // the rule that must fail on cells; "" when the class says enough
	// recorded are the gate runs in gate/results that did it on cells, on the lab host; none for a control not run in
	// its form yet. A run of an earlier form is listed only with a comment on its row that says so.
	recorded []string
}

// gateControls are the controls, one diff each in gate/controls.
var gateControls = []gateControl{
	{"gate5-known-bad", true, "regression", "", []string{"g20261018-074337"}},
	{"gate5-always-advance", false, "harness", "R1", []string{"g20261017-225512"}},
	{"arm-known-slow", true, "regression", "R7", []string{"g20261018-163914", "g20261018-170110", "g20261018-172301"}},
	// g20261018-080445 is the record of arm-always-pass's earlier form: its branch's gate/combine.sh, the shell judge
	// v2-r7off, in which R7 never fails. The Go form, a patch to internal/judge/registry.go, has not run on cells yet.
	{"arm-always-pass", false, "harness", "R1", []string{"g20261018-080445"}},
	{"persistence-maxqps-50", true, "refused", "", nil},
}

// must is what the control must make a gate on cells do, as its file's first line says it.
func (c gateControl) must() string {
	s := "BLOCK, class " + c.class
	if c.rule != "" {
		s += ", on " + c.rule
	}
	return s
}

// controlNames lists the controls' names, comma-separated.
func controlNames() string {
	var names []string
	for _, c := range gateControls {
		names = append(names, c.name)
	}
	return strings.Join(names, ", ")
}

// lookupControl finds the control called name.
func lookupControl(name string) (gateControl, bool) {
	for _, c := range gateControls {
		if c.name == name {
			return c, true
		}
	}
	return gateControl{}, false
}

// controlVerdict is what laug reads of the control's gate run: verdict.json's verdict, class and each rule's pass,
// and harness.json, make harness-test's exit, seconds and tally.
type controlVerdict struct {
	Verdict string  `json:"verdict"`
	Class   *string `json:"class"`
	Fake    bool    `json:"fake"`
	Rules   map[string]struct {
		Pass bool `json:"pass"`
	} `json:"rules"`
}

type controlHarness struct {
	Exit     *int     `json:"exit"`
	Seconds  *float64 `json:"seconds"`
	TimedOut bool     `json:"timed_out"`
	Tally    *string  `json:"tally"`
}

// failed lists the rules the run failed, in order.
func (v controlVerdict) failed() []string {
	var rules []string
	for i := 1; i <= 9; i++ {
		r := "R" + strconv.Itoa(i)
		if got, ok := v.Rules[r]; ok && !got.Pass {
			rules = append(rules, r)
		}
	}
	return rules
}

// String is the verdict as the last line names it: ADVANCE, or BLOCK with its class and the rules that failed.
func (v controlVerdict) String() string {
	if v.Verdict != "BLOCK" {
		return v.Verdict
	}
	s := "BLOCK, class "
	if v.Class != nil {
		s += *v.Class
	} else {
		s += "null"
	}
	if f := v.failed(); len(f) > 0 {
		s += ", rules failed " + strings.Join(f, " ")
	}
	return s
}

// String says how make harness-test ended: its exit and seconds, its tally, or that it ran past the plan's
// harness_timeout_s.
func (h controlHarness) String() string {
	if h.Exit == nil {
		return "no exit recorded"
	}
	s := "exited " + strconv.Itoa(*h.Exit)
	if h.TimedOut {
		s = "ran past the plan's harness_timeout_s, stopped (" + strconv.Itoa(*h.Exit) + ")"
	}
	if h.Seconds != nil {
		s += " in " + strconv.FormatFloat(*h.Seconds, 'f', 1, 64) + " s"
	}
	if h.Tally != nil {
		s += ", " + *h.Tally
	}
	return s
}

// held says whether the control's gate on fakes did what it must there, and why not.
func (c gateControl) held(v controlVerdict, h *controlHarness) (bool, string) {
	r1, ok := v.Rules["R1"]
	switch {
	case !v.Fake:
		return false, "verdict.json says the run was not on fakes"
	case !ok:
		return false, "verdict.json holds no R1"
	case h == nil:
		return false, "the run holds no harness.json, so make harness-test was not seen to end"
	case c.cells && (v.Verdict != "ADVANCE" || !r1.Pass):
		return false, "want ADVANCE, R1 held: no fake cell reads the change"
	case c.cells:
		return true, ""
	case v.Verdict != "BLOCK" || v.Class == nil || *v.Class != "harness" || r1.Pass:
		return false, "want BLOCK, class harness, R1 failed"
	case h.TimedOut || h.Exit == nil || *h.Exit == 0:
		return false, "want make harness-test to fail its checks, as the recorded run's did, not to time out or pass"
	}
	return true, ""
}

func runGateControl(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("gate control", "-name <control> -fake [-keep]")
	name := f.String("name", "", "the `control`, a diff in "+controlsDir+" by its name; bare, "+a.self()+" gate "+
		"control lists them")
	fake := f.Bool("fake", false, "run the gate on fakes, with make harness-test for real as R1; required, since a "+
		"control on cells is not built")
	keep := f.Bool("keep", false, "keep the worktree and the run under TMPDIR; without it both go")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) > 0:
		return a.usageError(f, "takes no arguments, got %q; -name names the control", pos)
	case *name == "":
		fmt.Fprintf(a.Stderr, "laug · gate control: names no control: the controls in %s, each with what it must "+
			"make a gate on cells do:\n", controlsDir)
		for _, c := range gateControls {
			on := "the same on fakes"
			if c.cells {
				on = "on fakes ADVANCE"
			}
			fmt.Fprintf(a.Stderr, "  %-22s %s; %s\n", c.name, c.must(), on)
		}
		fmt.Fprintf(a.Stderr, "%s gate control -name <control> -fake runs one, about 4 min\n", a.self())
		f.mistake(a.Stderr)
		return exitUsage
	}
	c, found := lookupControl(*name)
	switch {
	case !found:
		return a.refused("gate control", fmt.Errorf("-name %s: no such control; the controls: %s", shellWord(*name),
			controlNames()))
	case !*fake:
		return a.refused("gate control", errors.New("-fake is required: a control on cells is not built; -fake runs "+
			"the gate on fakes, with make harness-test for real as R1"))
	case a.Root == "":
		return a.refused("gate control", fmt.Errorf("applies %s/%s.diff to a checkout's HEAD, so it needs a laug "+
			"checkout: no directory above this one holds go.mod and gate/run.sh", controlsDir, c.name))
	}
	diff := filepath.Join(a.Root, controlsDir, c.name+".diff")
	if !isFile(diff) {
		return a.refused("gate control", fmt.Errorf("%s/%s.diff: no such file in this checkout", controlsDir, c.name))
	}
	git, mk := a.lookPath("git"), a.lookPath("make")
	switch {
	case git == "":
		return a.refused("gate control", errors.New("no git on PATH to make the worktree with"))
	case mk == "":
		return a.refused("gate control", errors.New("no make on PATH to build bin/laug in the worktree with"))
	}
	head, err := a.gitHead(ctx)
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate control: %v\n", a.errShown(err))
		return exitFailed
	}
	port, err := freePort()
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate control: a free port for the gate's own pool: %v\n", a.errShown(err))
		return exitFailed
	}
	tmp, err := os.MkdirTemp(a.tmpDir(), "laug-control-"+c.name+"-")
	if err == nil {
		tmp, err = filepath.Abs(tmp)
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate control: %v\n", a.errShown(err))
		return exitFailed
	}
	tree, results := filepath.Join(tmp, "tree"), filepath.Join(tmp, "results")
	stays := "the worktree and the run go after it"
	if *keep {
		stays = "both stay"
	}
	fmt.Fprintf(a.Stdout, "laug · gate control %s: %s/%s.diff on HEAD %s in a worktree, %s, then a gate on fakes "+
		"there, make harness-test for real as R1, about 4 min; %s; Ctrl-C stops it\n", c.name, controlsDir, c.name,
		short(head), a.pathShown(tree), stays)

	// env is what every step gets: the caller's environment, with no MAKEFLAGS, MAKELEVEL or MFLAGS, so a make the
	// caller runs in does not steer the build, no FAKE_*, so the fakes are the ones the control names, and no PWD or
	// OLDPWD, so exec sets PWD to each step's dir; TMPDIR the dir laug made, which holds the gate's own temporary
	// files and the harness's.
	env := []string{"TMPDIR=" + tmp}
	for _, kv := range firstWins(a.Environ) {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "TMPDIR", k == "MAKEFLAGS", k == "MAKELEVEL", k == "MFLAGS", k == "PWD", k == "OLDPWD",
			strings.HasPrefix(k, "FAKE_"):
		default:
			env = append(env, kv)
		}
	}
	// step runs one command in dir, its stdout to out and its stderr to laug's. A signal stops it with SIGTERM, as
	// Ctrl-C at a terminal would, and laug waits for it: a gate's stop path ends what it started.
	step := func(dir string, env []string, out io.Writer, path string, args ...string) (int, error) {
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, a.Stderr
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		return script.ExitCode(cmd.Run())
	}
	// fail says which step did not end with 0, or that a signal stopped it, and returns 1.
	fail := func(what string, code int, err error) int {
		switch {
		case ctx.Err() != nil:
			fmt.Fprintf(a.Stderr, "laug · gate control %s: a signal stopped %s\n", c.name, what)
		case err != nil:
			fmt.Fprintf(a.Stderr, "laug · gate control %s: %s: %v\n", c.name, what, a.errShown(err))
		default:
			fmt.Fprintf(a.Stderr, "laug · gate control %s: %s exited %d\n", c.name, what, code)
		}
		return exitFailed
	}
	added := false
	defer func() {
		if *keep {
			return
		}
		// The cleanup runs after a signal too, so it does not take ctx. git worktree remove --force takes the
		// worktree's files, ignored ones too, and its record in the checkout's .git; when it fails, the files go
		// all the same, and git worktree prune drops the record of a worktree whose dir is gone.
		removed := !added
		if added {
			cmd := exec.Command(git, "worktree", "remove", "--force", tree)
			cmd.Dir, cmd.Env, cmd.Stderr = a.Root, env, a.Stderr
			removed = cmd.Run() == nil
		}
		if err := os.RemoveAll(tmp); err != nil {
			fmt.Fprintf(a.Stderr, "laug · gate control %s: %v\n", c.name, a.errShown(err))
		}
		if !removed {
			cmd := exec.Command(git, "worktree", "prune")
			cmd.Dir, cmd.Env, cmd.Stderr = a.Root, env, a.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(a.Stderr, "laug · gate control %s: git worktree prune: %v\n", c.name, a.errShown(err))
			}
		}
	}()

	if code, err := step(a.Root, env, a.Stderr, git, "worktree", "add", "--detach", "--quiet", tree,
		head); code != 0 || err != nil {
		return fail("git worktree add "+a.pathShown(tree), code, err)
	}
	added = true
	if code, err := step(tree, env, a.Stderr, git, "apply", diff); code != 0 || err != nil {
		return fail("git apply "+controlsDir+"/"+c.name+".diff", code, err)
	}
	if code, err := step(tree, env, a.Stderr, mk, "-s", "--no-print-directory", "bin/laug"); code != 0 || err != nil {
		return fail("make bin/laug in the worktree", code, err)
	}
	// The gate's pool listens on a free port, and the fake harness runs the real one. exec.Cmd takes the last of a
	// name, so the two go last.
	var gateEnv []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GATE_POOL_LISTEN=") {
			gateEnv = append(gateEnv, kv)
		}
	}
	gateEnv = append(gateEnv, "GATE_POOL_LISTEN=127.0.0.1:"+strconv.Itoa(port), "FAKE_GATE_HARNESS_REAL=1")
	gcode, err := step(tree, gateEnv, a.Stdout, filepath.Join(tree, "bin", "laug"), "gate", "run",
		serverTagDefault, "-fake", "-results", results)
	if err != nil || ctx.Err() != nil {
		return fail("laug gate run "+serverTagDefault+" -fake", gcode, err)
	}
	dir, err := onlyRun(results, a.pathShown(results))
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate control %s: laug gate run %s -fake exited %d: %v\n", c.name,
			serverTagDefault, gcode, a.errShown(err))
		return exitFailed
	}
	var v controlVerdict
	if b, err := os.ReadFile(filepath.Join(dir, "verdict.json")); err != nil || json.Unmarshal(b, &v) != nil {
		fmt.Fprintf(a.Stderr, "laug · gate control %s: %s holds no verdict.json laug can read; laug gate run %s "+
			"-fake exited %d\n", c.name, filepath.Base(dir), serverTagDefault, gcode)
		return exitFailed
	}
	var h *controlHarness
	if b, err := os.ReadFile(filepath.Join(dir, "harness.json")); err == nil {
		h = &controlHarness{}
		if json.Unmarshal(b, h) != nil {
			h = nil
		}
	}
	harness := "make harness-test: no harness.json"
	if h != nil {
		harness = "make harness-test " + h.String()
	}
	recorded := "no run of this form recorded yet"
	if len(c.recorded) > 0 {
		recorded = "as " + strings.Join(c.recorded, ", ") + " did"
	}
	held, why := c.held(v, h)
	kept := ""
	if *keep {
		kept = "; kept: the worktree " + a.pathShown(tree) + " and the run " + a.pathShown(dir)
	}
	switch {
	case !held:
		fmt.Fprintf(a.Stdout, "laug · gate control %s: not held: the gate on fakes gave %s; %s; %s%s\n", c.name, v,
			why, harness, kept)
		return exitFailed
	case c.cells:
		fmt.Fprintf(a.Stdout, "laug · gate control %s: held on fakes: %s, R1 held (%s); it acts only on real cells, "+
			"where the gate must %s, %s%s\n", c.name, v, harness, c.must(), recorded, kept)
	default:
		fmt.Fprintf(a.Stdout, "laug · gate control %s: held: %s; %s; on cells it must %s, %s%s\n", c.name, v, harness,
			c.must(), recorded, kept)
	}
	return exitDone
}

// onlyRun is the one gate run in results, the dir the control's gate wrote it in; an error names results as shown,
// os.ReadDir's own path left out.
func onlyRun(results, shown string) (string, error) {
	ents, err := os.ReadDir(results)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s: %w", shown, pathErr(err))
	}
	var runs []string
	for _, e := range ents {
		if e.IsDir() && gateIDRE.MatchString(e.Name()) {
			runs = append(runs, e.Name())
		}
	}
	if len(runs) != 1 {
		return "", fmt.Errorf("%d gate runs in %s, want 1", len(runs), shown)
	}
	return filepath.Join(results, runs[0]), nil
}

// freePort is a TCP port on 127.0.0.1 that nothing listens on now, for the gate's own pool, so that a pool on 7700
// does not refuse the control's gate.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
