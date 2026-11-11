package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/tables"
)

// TestGateControls holds gate/controls to the table: a file a control and a control a file; each file's first line
// names its control, what it must make a gate on cells do and the runs that did it, and the rest is a diff that
// applies to HEAD's tree; and each recorded run did what the table says, by its row of docs/tables/gates.jsonl and,
// where its directory is here, by its verdict.json and harness.json.
func TestGateControls(t *testing.T) {
	root := repoRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, controlsDir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	var files, names []string
	for _, p := range paths {
		files = append(files, filepath.Base(p))
	}
	for _, c := range gateControls {
		names = append(names, c.name+".diff")
	}
	slices.Sort(files)
	slices.Sort(names)
	if !slices.Equal(files, names) {
		t.Fatalf("%s holds %q; the table names %q", controlsDir, files, names)
	}

	// git apply --check --cached against a temporary index read from HEAD: the diff is checked on HEAD's tree, not
	// on what the working tree holds, and the checkout's own index is not touched.
	env := gitEnv(t)
	head := exec.Command("git", "rev-parse", "--verify", "HEAD")
	head.Dir, head.Env = root, env
	index := filepath.Join(t.TempDir(), "index")
	env = append(env, "GIT_INDEX_FILE="+index)
	inGit := head.Run() == nil
	if inGit {
		gitDo(t, env, root, "read-tree", "HEAD")
	}

	rows, err := tables.Read(os.DirFS(root), tables.Dir, "gates")
	if err != nil {
		t.Fatal(err)
	}
	gateRows := map[string][]byte{}
	for _, r := range rows {
		gateRows[r.Key] = r.Line
	}

	for _, c := range gateControls {
		t.Run(c.name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, controlsDir, c.name+".diff"))
			if err != nil {
				t.Fatal(err)
			}
			first, rest, _ := strings.Cut(string(b), "\n")
			if !strings.HasPrefix(first, "# "+c.name+": ") || !strings.Contains(first, "must "+c.must()) {
				t.Errorf("first line %q: want %q, then what it must make the gate do, %q", first, "# "+c.name+": ",
					"must "+c.must())
			}
			for _, id := range c.recorded {
				if !strings.Contains(first, id) {
					t.Errorf("first line %q does not name %s, a run that did it", first, id)
				}
			}
			if len(c.recorded) == 0 && !strings.Contains(first, "no run of this form is recorded yet") {
				t.Errorf("first line %q: no run is recorded, so it says so", first)
			}
			if !strings.HasPrefix(rest, "diff --git a/") || strings.Contains(rest, "\n# ") {
				t.Errorf("after the first line, git diff's output and no other comment; it starts %q",
					rest[:min(len(rest), 40)])
			}
			for _, h := range []string{"From ", "Author:", "Date:", "Signed-off-by:"} {
				if strings.Contains(rest, "\n"+h) || strings.HasPrefix(rest, h) {
					t.Errorf("a header line %q: the diff carries no identity or date", h)
				}
			}
			if inGit {
				cmd := exec.Command("git", "apply", "--check", "--cached", filepath.Join(controlsDir, c.name+".diff"))
				cmd.Dir, cmd.Env = root, env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("git apply --check on HEAD: %v\n%s", err, out)
				}
			}
			for _, id := range c.recorded {
				// The gates row first, which holds the run whether or not its directory is here: R1, make
				// harness-test, passes where the harness exited 0.
				var r controlVerdict
				line, ok := gateRows[id]
				if !ok || json.Unmarshal(line, &r) != nil {
					t.Fatalf("%s: no row of docs/tables/gates.jsonl to read", id)
				}
				if r.Fake || r.Verdict != "BLOCK" || r.Class == nil || *r.Class != c.class ||
					c.rule != "" && r.Rules[c.rule].Pass {
					t.Errorf("%s's gates row gave %s, fake %t; the table says %s", id, r, r.Fake, c.must())
				}
				if c.cells != r.Rules["R1"].Pass {
					t.Errorf("%s's gates row: R1 pass %t; a control that acts on cells passes make harness-test, "+
						"one that does not fails it", id, r.Rules["R1"].Pass)
				}
				dir := filepath.Join(root, "gate", "results", id)
				if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
					continue
				}
				var v controlVerdict
				if b, err := os.ReadFile(filepath.Join(dir, "verdict.json")); err != nil || json.Unmarshal(b, &v) != nil {
					t.Fatalf("%s: no verdict.json to read: %v", id, err)
				}
				if v.Fake || v.Verdict != "BLOCK" || v.Class == nil || *v.Class != c.class ||
					c.rule != "" && v.Rules[c.rule].Pass {
					t.Errorf("%s gave %s, fake %t; the table says %s", id, v, v.Fake, c.must())
				}
				var h controlHarness
				if b, err := os.ReadFile(filepath.Join(dir, "harness.json")); err != nil || json.Unmarshal(b, &h) != nil {
					t.Fatalf("%s: no harness.json to read: %v", id, err)
				}
				if c.cells == (h.Exit == nil || *h.Exit != 0) {
					t.Errorf("%s: make harness-test %s; a control that acts on cells passes it, one that does not "+
						"fails it", id, h)
				}
			}
		})
	}
	if !inGit {
		t.Log("not a git checkout: no diff was checked on HEAD")
	}
}

// controlMake is a make that logs where it ran and with what, then, unless STUB_MAKE_EXIT says it fails, puts
// STUB_LAUG in the worktree as bin/laug, as make's file rule builds it.
const controlMake = `#!/bin/sh
printf 'make\t%s\t%s\tMAKEFLAGS=%s\n' "$PWD" "$*" "${MAKEFLAGS-unset}" >> "$STUB_LOG"
[ "${STUB_MAKE_EXIT:-0}" = 0 ] || exit "$STUB_MAKE_EXIT"
mkdir -p bin && cp "$STUB_LAUG" bin/laug && chmod +x bin/laug
`

// controlLaug is the worktree's bin/laug: it logs where it ran, with what and which of the names laug sets it got,
// and what control.txt holds there; then writes one gate run in -results as STUB_GATE says, and prints a gate's first
// line. slow runs until a signal.
const controlLaug = `#!/bin/sh
printf 'laug\t%s\t%s\tharness=%s listen=%s tmpdir=%s dnr=%s\n' "$PWD" "$*" "${FAKE_GATE_HARNESS_REAL-}" \
  "${GATE_POOL_LISTEN-}" "${TMPDIR-}" "${FAKE_GATE_DNR-unset}" >> "$STUB_LOG"
printf 'control.txt\t%s\n' "$(cat control.txt)" >> "$STUB_LOG"
[ "$5" = -results ] || exit 9
run="$6/g20261023-010203"
rules() { printf '{"R1":{"pass":%s},"R2":{"pass":%s},"R3":{"pass":true},"R4":{"pass":true},"R5":{"pass":true},"R6":{"pass":true},"R7":{"pass":true}}' "$1" "$1"; }
case "$STUB_GATE" in
slow) while :; do sleep 0.05; done ;;
none) exit 2 ;;
esac
mkdir -p "$run"
echo "laug · gate g20261023-010203: server 1.31.0, plan gate/plan.yaml, GATE_FAKE=1: no Docker, cell or Omes"
case "$STUB_GATE" in
advance) printf '{"verdict":"ADVANCE","class":null,"fake":true,"rules":%s}' "$(rules true)" > "$run/verdict.json"
  echo '{"exit":0,"seconds":251.5,"timed_out":false,"tally":"1137 passed, 0 failed"}' > "$run/harness.json"; code=0 ;;
harness) printf '{"verdict":"BLOCK","class":"harness","fake":true,"rules":%s}' "$(rules false)" > "$run/verdict.json"
  echo '{"exit":2,"seconds":249.0,"timed_out":false,"tally":"1050 passed, 87 failed"}' > "$run/harness.json"; code=1 ;;
timeout) printf '{"verdict":"BLOCK","class":"harness","fake":true,"rules":%s}' "$(rules false)" > "$run/verdict.json"
  echo '{"exit":124,"seconds":300,"timed_out":true,"tally":null}' > "$run/harness.json"; code=1 ;;
cells) printf '{"verdict":"BLOCK","class":"harness","fake":false,"rules":%s}' "$(rules false)" > "$run/verdict.json"
  echo '{"exit":2,"seconds":249.0,"timed_out":false,"tally":"1050 passed, 87 failed"}' > "$run/harness.json"; code=1 ;;
noharness) printf '{"verdict":"ADVANCE","class":null,"fake":true,"rules":%s}' "$(rules true)" > "$run/verdict.json"; code=0 ;;
esac
exit "$code"
`

// controlRepo is a git repository that stands in for a checkout: go.mod, gate/run.sh and control.txt, one commit,
// and gate/controls with a diff for gate5-always-advance and gate5-known-bad, each changing control.txt, and one for
// arm-always-pass that does not apply. It returns the repository and the environment the verb gets: the stub make
// first on PATH, then git's directory, TMPDIR a dir of its own, the stubs' names, and a stray FAKE_GATE_DNR,
// MAKEFLAGS and GATE_POOL_LISTEN.
func controlRepo(t *testing.T) (repo string, env []string, log, tmp string) {
	t.Helper()
	genv := gitEnv(t)
	repo = checkout(t)
	writeFile(t, filepath.Join(repo, "control.txt"), "as committed\n")
	gitDo(t, genv, repo, "init", "-q", "-b", "main")
	gitDo(t, genv, repo, "add", ".")
	gitDo(t, genv, repo, "commit", "-q", "-m", "one")
	for name, text := range map[string]string{"gate5-always-advance": "always advance\n", "gate5-known-bad": "known bad\n"} {
		writeFile(t, filepath.Join(repo, "control.txt"), text)
		d := gitDo(t, genv, repo, "diff")
		writeFile(t, filepath.Join(repo, controlsDir, name+".diff"), "# "+name+": a test's\n"+d+"\n")
	}
	writeFile(t, filepath.Join(repo, "control.txt"), "as committed\n")
	writeFile(t, filepath.Join(repo, controlsDir, "arm-always-pass.diff"), "# arm-always-pass: a test's\n"+
		"diff --git a/control.txt b/control.txt\n--- a/control.txt\n+++ b/control.txt\n@@ -1 +1 @@\n-not what HEAD holds\n"+
		"+always pass\n")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "make"), controlMake)
	writeFile(t, filepath.Join(bin, "laug.sh"), controlLaug)
	log, tmp = filepath.Join(t.TempDir(), "log"), t.TempDir()
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	path := ""
	for _, kv := range genv {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	env = append([]string{"PATH=" + bin + ":" + path, "TMPDIR=" + tmp, "STUB_LOG=" + log,
		"STUB_LAUG=" + filepath.Join(bin, "laug.sh"), "FAKE_GATE_DNR=steady", "MAKEFLAGS=-j9",
		"GATE_POOL_LISTEN=127.0.0.1:7700"}, genv...)
	return repo, env, log, tmp
}

// worktrees lists the worktrees git knows in repo.
func worktrees(t *testing.T, repo string) []string {
	t.Helper()
	var trees []string
	for _, l := range strings.Split(gitDo(t, gitEnv(t), repo, "worktree", "list", "--porcelain"), "\n") {
		if p, ok := strings.CutPrefix(l, "worktree "); ok {
			trees = append(trees, p)
		}
	}
	return trees
}

// TestGateControl drives gate control: its refusals, then each step on a repository of the test's, with the real
// git, a stub make and a stub bin/laug: the worktree, the diff applied there and not in the checkout, the build, the
// gate's arguments and environment, what laug says of each verdict, and the worktree and its dir gone after, a
// failed step and a signal among the causes. Then the fake harness's seam.
func TestGateControl(t *testing.T) {
	t.Run("refusals", func(t *testing.T) {
		repo, env, _, tmp := controlRepo(t)
		gitOnly, makeOnly := t.TempDir(), t.TempDir()
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(git, filepath.Join(gitOnly, "git")); err != nil {
			t.Fatal(err)
		}
		writeExec(t, filepath.Join(makeOnly, "make"), controlMake)
		noMake := []string{"PATH=" + gitOnly, "TMPDIR=" + tmp}
		noGit := []string{"PATH=" + makeOnly, "TMPDIR=" + tmp}
		for _, c := range []struct {
			name  string
			root  string
			env   []string
			args  []string
			first string
		}{
			{"bare", repo, env, nil, "laug · gate control: names no control: the controls in gate/controls, each " +
				"with what it must make a gate on cells do:"},
			{"unknown", repo, env, []string{"-name", "known-good", "-fake"}, "laug · gate control: -name known-good: " +
				"no such control; the controls: " + controlNames()},
			{"no -fake", repo, env, []string{"-name", "gate5-known-bad"}, "laug · gate control: -fake is required: a " +
				"control on cells is not built; -fake runs the gate on fakes, with make harness-test for real as R1"},
			{"an argument", repo, env, []string{"gate5-known-bad", "-fake"}, "laug · gate control: takes no " +
				"arguments, got [\"gate5-known-bad\"]; -name names the control"},
			{"outside a checkout", "", env, []string{"-name", "gate5-known-bad", "-fake"}, "laug · gate control: " +
				"applies gate/controls/gate5-known-bad.diff to a checkout's HEAD, so it needs a laug checkout: no " +
				"directory above this one holds go.mod and gate/run.sh"},
			{"no such file", repo, env, []string{"-name", "arm-known-slow", "-fake"}, "laug · gate control: " +
				"gate/controls/arm-known-slow.diff: no such file in this checkout"},
			{"no git", repo, noGit, []string{"-name", "gate5-known-bad", "-fake"}, "laug · gate control: no git on " +
				"PATH to make the worktree with"},
			{"no make", repo, noMake, []string{"-name", "gate5-known-bad", "-fake"}, "laug · gate control: no make " +
				"on PATH to build bin/laug in the worktree with"},
		} {
			t.Run(c.name, func(t *testing.T) {
				code, out, errOut := runApp(t, &App{Root: c.root, Environ: c.env},
					append([]string{"gate", "control"}, c.args...)...)
				if code != 2 || out != "" || firstLine(errOut) != c.first {
					t.Errorf("exit %d, want 2\nstdout:\n%s\nstderr:\n%s\nwant first: %s", code, out, errOut, c.first)
				}
				if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
					t.Errorf("TMPDIR holds %d entries", len(ents))
				}
			})
		}
		_, _, errOut := runApp(t, &App{Root: repo, Environ: env}, "gate", "control")
		for _, c := range gateControls {
			if !strings.Contains(errOut, "\n  "+c.name+" ") || !strings.Contains(errOut, c.must()) {
				t.Errorf("bare lists no %s, %s:\n%s", c.name, c.must(), errOut)
			}
		}
	})

	for _, c := range []struct {
		name, control, gate string
		code                int
		last                string // the last stdout line, past "laug · gate control <control>: "
		stderr              string // a line stderr holds, when the run fails
	}{
		{"an R1 control held", "gate5-always-advance", "harness", 0, "held: BLOCK, class harness, rules failed R1 " +
			"R2; make harness-test exited 2 in 249.0 s, 1050 passed, 87 failed; on cells it must BLOCK, class " +
			"harness, on R1, as g20261017-225512 did", ""},
		{"an R1 control the gate ADVANCEs on", "gate5-always-advance", "advance", 1, "not held: the gate on fakes " +
			"gave ADVANCE; want BLOCK, class harness, R1 failed; make harness-test exited 0 in 251.5 s, 1137 " +
			"passed, 0 failed", ""},
		{"an R1 control whose harness timed out", "gate5-always-advance", "timeout", 1, "not held: the gate on " +
			"fakes gave BLOCK, class harness, rules failed R1 R2; want make harness-test to fail its checks, as " +
			"the recorded run's did, not to time out or pass; make harness-test ran past the plan's " +
			"harness_timeout_s, stopped (124) in 300.0 s", ""},
		{"a run not on fakes", "gate5-always-advance", "cells", 1, "not held: the gate on fakes gave BLOCK, class " +
			"harness, rules failed R1 R2; verdict.json says the run was not on fakes; make harness-test exited 2 " +
			"in 249.0 s, 1050 passed, 87 failed", ""},
		{"a cell control held on fakes", "gate5-known-bad", "advance", 0, "held on fakes: ADVANCE, R1 held (make " +
			"harness-test exited 0 in 251.5 s, 1137 passed, 0 failed); it acts only on real cells, where the gate " +
			"must BLOCK, class regression, as g20261018-074337 did", ""},
		{"a cell control R1 failed on", "gate5-known-bad", "harness", 1, "not held: the gate on fakes gave BLOCK, " +
			"class harness, rules failed R1 R2; want ADVANCE, R1 held: no fake cell reads the change; make " +
			"harness-test exited 2 in 249.0 s, 1050 passed, 87 failed", ""},
		{"no harness.json", "gate5-known-bad", "noharness", 1, "not held: the gate on fakes gave ADVANCE; the run " +
			"holds no harness.json, so make harness-test was not seen to end; make harness-test: no harness.json", ""},
		{"no run written", "gate5-known-bad", "none", 1, "", "laug · gate control gate5-known-bad: laug gate run " +
			"1.31.0 -fake exited 2: 0 gate runs in "},
		{"a diff that does not apply", "arm-always-pass", "advance", 1, "", "laug · gate control arm-always-pass: " +
			"git apply gate/controls/arm-always-pass.diff exited 1"},
		{"make fails", "gate5-known-bad", "advance", 1, "", "laug · gate control gate5-known-bad: make bin/laug in " +
			"the worktree exited 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, env, log, tmp := controlRepo(t)
			env = append([]string{"STUB_GATE=" + c.gate}, env...)
			if c.name == "make fails" {
				env = append([]string{"STUB_MAKE_EXIT=2"}, env...)
			}
			code, out, errOut := runApp(t, &App{Root: repo, Environ: env}, "gate", "control", "-name", c.control,
				"-fake")
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			head := gitDo(t, gitEnv(t), repo, "rev-parse", "HEAD")
			first := "laug · gate control " + c.control + ": gate/controls/" + c.control + ".diff on HEAD " + head[:7] +
				" in a worktree, " + tmp + "/laug-control-" + c.control + "-"
			if code != c.code || !strings.HasPrefix(lines[0], first) || !strings.HasSuffix(lines[0], "/tree, then "+
				"a gate on fakes there, make harness-test for real as R1, about 4 min; the worktree and the run go "+
				"after it; Ctrl-C stops it") {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
			}
			if c.last != "" && lines[len(lines)-1] != "laug · gate control "+c.control+": "+c.last {
				t.Errorf("last line\n got %s\nwant %s", lines[len(lines)-1], "laug · gate control "+c.control+": "+
					c.last)
			}
			if c.stderr != "" && !strings.Contains(errOut, "\n"+c.stderr) && !strings.HasPrefix(errOut, c.stderr) {
				t.Errorf("stderr holds no %q:\n%s", c.stderr, errOut)
			}
			if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
				t.Errorf("TMPDIR holds %d entries after: the worktree's dir stays", len(ents))
			}
			if trees := worktrees(t, repo); len(trees) != 1 {
				t.Errorf("git knows %d worktrees after, want 1: %q", len(trees), trees)
			}
			if got := readText(t, filepath.Join(repo, "control.txt")); got != "as committed\n" {
				t.Errorf("the checkout's control.txt holds %q: the diff went into the checkout", got)
			}
			b, _ := os.ReadFile(log)
			calls := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			tree := `\Q` + tmp + `\E/laug-control-` + c.control + `-[0-9]+/tree`
			want := []string{"make\t" + tree + "\t-s --no-print-directory bin/laug\tMAKEFLAGS=unset"}
			if c.name != "make fails" {
				want = append(want, "laug\t"+tree+`\tgate run 1\.31\.0 -fake -results `+tree[:len(tree)-len("/tree")]+
					`/results\tharness=1 listen=127\.0\.0\.1:[0-9]+ tmpdir=`+tree[:len(tree)-len("/tree")]+
					` dnr=unset`, "control.txt\t"+map[string]string{"gate5-always-advance": "always advance",
					"gate5-known-bad": "known bad"}[c.control])
			}
			if c.name == "a diff that does not apply" {
				want = nil
			}
			if len(calls) == 1 && calls[0] == "" {
				calls = nil
			}
			if len(calls) != len(want) {
				t.Fatalf("the stubs ran %d times, want %d:\n%s", len(calls), len(want), b)
			}
			for i, w := range want {
				if !regexp.MustCompile("^" + w + "$").MatchString(calls[i]) {
					t.Errorf("call %d\n got %s\nwant %s", i, calls[i], w)
				}
				if strings.Contains(calls[i], "listen=127.0.0.1:7700 ") {
					t.Errorf("the gate got the caller's GATE_POOL_LISTEN: %s", calls[i])
				}
			}
		})
	}

	t.Run("keep", func(t *testing.T) {
		repo, env, _, tmp := controlRepo(t)
		env = append([]string{"STUB_GATE=harness"}, env...)
		code, out, errOut := runApp(t, &App{Root: repo, Environ: env}, "gate", "control", "-name",
			"gate5-always-advance", "-fake", "-keep")
		ents, _ := os.ReadDir(tmp)
		if code != 0 || len(ents) != 1 || !strings.Contains(firstLine(out), "; both stay; Ctrl-C stops it") {
			t.Fatalf("exit %d, %d entries in TMPDIR\nstdout:\n%s\nstderr:\n%s", code, len(ents), out, errOut)
		}
		dir := filepath.Join(tmp, ents[0].Name())
		if !strings.HasSuffix(out, "; kept: the worktree "+dir+"/tree and the run "+dir+"/results/g20261023-010203\n") {
			t.Errorf("the last line names no worktree and run:\n%s", out)
		}
		if got := readText(t, filepath.Join(dir, "tree", "control.txt")); got != "always advance\n" {
			t.Errorf("the kept worktree's control.txt holds %q", got)
		}
		if trees := worktrees(t, repo); len(trees) != 2 {
			t.Errorf("git knows %d worktrees, want 2: %q", len(trees), trees)
		}
		gitDo(t, gitEnv(t), repo, "worktree", "remove", "--force", filepath.Join(dir, "tree"))
	})

	// TMPDIR under HOME: the first line and the last name the worktree and the run ~/ and the rest, and no line
	// names the home path; so does the line for a gate that wrote no run.
	t.Run("TMPDIR under HOME", func(t *testing.T) {
		for _, gate := range []string{"harness", "none"} {
			repo, env, _, tmp := controlRepo(t)
			home := filepath.Dir(tmp)
			env = append([]string{"STUB_GATE=" + gate, "HOME=" + home}, env...)
			code, out, errOut := runApp(t, &App{Root: repo, Environ: env}, "gate", "control", "-name",
				"gate5-always-advance", "-fake", "-keep")
			ents, _ := os.ReadDir(tmp)
			if len(ents) != 1 {
				t.Fatalf("%s: exit %d, %d entries in TMPDIR\nstdout:\n%s\nstderr:\n%s", gate, code, len(ents), out,
					errOut)
			}
			dir := "~/" + filepath.Base(tmp) + "/" + ents[0].Name()
			if !strings.Contains(firstLine(out), " in a worktree, "+dir+"/tree, then a gate on fakes") ||
				strings.Contains(out+errOut, home+"/") {
				t.Errorf("%s: the lines name the home path or no ~/:\nstdout:\n%s\nstderr:\n%s", gate, out, errOut)
			}
			switch gate {
			case "harness":
				if !strings.HasSuffix(out, "; kept: the worktree "+dir+"/tree and the run "+dir+
					"/results/g20261023-010203\n") {
					t.Errorf("the last line names no worktree and run as ~/:\n%s", out)
				}
			case "none":
				if !strings.Contains(errOut, "0 gate runs in "+dir+"/results, want 1\n") {
					t.Errorf("no run written: the line names no results dir as ~/:\n%s", errOut)
				}
			}
			gitDo(t, gitEnv(t), repo, "worktree", "remove", "--force", filepath.Join(tmp, ents[0].Name(), "tree"))
		}
	})

	t.Run("a signal", func(t *testing.T) {
		repo, env, _, tmp := controlRepo(t)
		env = append([]string{"STUB_GATE=slow"}, env...)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()
		var stdout, stderr strings.Builder
		start := time.Now()
		code := run(ctx, &App{Root: repo, Environ: env, Stdout: &stdout, Stderr: &stderr}, []string{"gate",
			"control", "-name", "gate5-known-bad", "-fake"})
		line := "laug · gate control gate5-known-bad: a signal stopped laug gate run 1.31.0 -fake"
		if code != 1 || !slices.Contains(strings.Split(stderr.String(), "\n"), line) || time.Since(start) > 10*time.Second {
			t.Errorf("exit %d in %s, want 1 and %q\nstdout:\n%s\nstderr:\n%s", code, time.Since(start), line,
				stdout.String(), stderr.String())
		}
		if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
			t.Errorf("TMPDIR holds %d entries after a signal", len(ents))
		}
		if trees := worktrees(t, repo); len(trees) != 1 {
			t.Errorf("git knows %d worktrees after a signal, want 1: %q", len(trees), trees)
		}
	})

	// The fake harness with FAKE_GATE_HARNESS_REAL=1 runs make harness-test in the checkout, the gate's fakebin off
	// PATH and MAKEFLAGS and MAKELEVEL unset, and exits with its code; without it, it is the fake.
	t.Run("the fake harness's seam", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(repoRoot(t))
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		writeExec(t, filepath.Join(bin, "make"), "#!/bin/sh\necho \"make $*\"\necho \"PATH=$PATH\"\n"+
			"echo \"MAKEFLAGS=${MAKEFLAGS-unset} MAKELEVEL=${MAKELEVEL-unset}\"\nexit 3\n")
		fakebin := filepath.Join(root, "load", "testdata", "gate", "fakebin")
		for _, c := range []struct {
			seam string
			code int
			out  string
		}{
			{"1", 3, "make -C " + root + " --no-print-directory harness-test\nPATH=" + bin + ":/usr/bin:/bin\n" +
				"MAKEFLAGS=unset MAKELEVEL=unset\n"},
			{"", 0, "SIGINT at its default\nfake make harness-test\n\n1 passed, 0 failed\n"},
		} {
			cmd := exec.Command(filepath.Join(root, "load", "testdata", "gate", "fake-harness.sh"))
			cmd.Env = []string{"PATH=" + fakebin + ":" + bin + ":/usr/bin:/bin", "MAKEFLAGS=-j9", "MAKELEVEL=1",
				"FAKE_GATE_HARNESS_REAL=" + c.seam}
			out, err := cmd.Output()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
			if code != c.code || string(out) != c.out {
				t.Errorf("FAKE_GATE_HARNESS_REAL=%q: exit %d, want %d\n got %q\nwant %q", c.seam, code, c.code, out,
					c.out)
			}
		}
	})
}
