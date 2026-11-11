package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/rejudge"
	"github.com/ymur-studio/laug/internal/script"
)

// gateStrays is the caller's environment beyond strayEnviron in gate and host rows: a stray copy of each knob the
// gate's verbs map.
var gateStrays = []string{"PLAN=stray.yaml", "ITEMS=9", "BASE=stray", "GATE_FAKE=1", "GATE_POST=stray",
	"GATE_RESULTS=stray", "GATE=stray", "A=stray", "B=stray", "GATES=stray", "PAIR=a:b", "EVIDENCE=stray", "SECONDS=5"}

// The two commits the stub git and the fake stamps name.
const (
	headSHA  = "2222222222222222222222222222222222222222"
	otherSHA = "1111111111111111111111111111111111111111"
)

// gitStub is a git that answers rev-parse HEAD with STUB_HEAD, status --porcelain with STUB_STATUS, the same by
// treePathspec with STUB_STATUS less its lines under gate/results, the same for internal/judge and internal/jsonx with
// STUB_JUDGE_STATUS, and fails on anything else.
const gitStub = `#!/bin/sh
case "$*" in
"rev-parse HEAD") [ -n "$STUB_HEAD" ] || exit 128; echo "$STUB_HEAD" ;;
"status --porcelain") [ -z "$STUB_STATUS" ] || echo "$STUB_STATUS" ;;
"status --porcelain -- . :(exclude)gate/results")
	[ -z "$STUB_STATUS" ] || echo "$STUB_STATUS" | grep -v '^.. gate/results/' || true ;;
"status --porcelain -- internal/judge internal/jsonx") [ -z "$STUB_JUDGE_STATUS" ] || echo "$STUB_JUDGE_STATUS" ;;
*) echo "git stub: $*" >&2; exit 2 ;;
esac
`

// writeExec writes an executable file.
func writeExec(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// gateApp is a matrix row's App for the gate's verbs: a bin/laug in the checkout, as make build builds it; git on
// PATH, the stub, with HEAD headSHA and a clean tree; bin/laug's stamp, by binaryStamp, headSHA; and a host type
// declared in HOME's .config, as laug host type -declare writes it, so a gate on cells is not refused for want of one.
func gateApp(t *testing.T, c envCase) (*App, *recorder) {
	t.Helper()
	a, rec := matrixApp(t, c)
	writeFile(t, filepath.Join(a.getenv("HOME"), ".config", "laug", "host-type"), "m8i.8xlarge\n")
	writeExec(t, filepath.Join(a.Root, "bin", "laug"), "#!/bin/sh\n")
	bin := filepath.Join(t.TempDir(), "git")
	writeExec(t, filepath.Join(bin, "git"), gitStub)
	path := a.getenv("PATH")
	a.Environ = append([]string{"PATH=" + bin + ":" + path, "STUB_HEAD=" + headSHA}, a.Environ...)
	stamp(t, headSHA, nil)
	return a, rec
}

// stamp makes binaryStamp answer s and err for the length of the test.
func stamp(t *testing.T, s string, err error) {
	t.Helper()
	saved := binaryStamp
	t.Cleanup(func() { binaryStamp = saved })
	binaryStamp = func(string) (string, error) { return s, err }
}

// runGateMatrix runs each row through run on gateApp and holds it to what it says.
func runGateMatrix(t *testing.T, cases []envCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.more = append(slices.Clone(gateStrays), c.more...)
			a, rec := gateApp(t, c)
			code, out, errOut := runApp(t, a, c.args...)
			checkRow(t, c, rec, code, out, errOut)
		})
	}
}

// TestGate: the env matrix of the gate's verbs, then -fake's dir under TMPDIR, the stale-binary refusal on a fake
// stamp, -json, signature, open, the bare forms' pointer to the newest run, gate/run.sh's exit passed through by the
// built binary, and what the stamp is read from.
func TestGate(t *testing.T) {
	runStrays := []string{"BASE", "GATE_FAKE", "GATE_POST", "GATE_RESULTS", "ITEMS", "OMES_DIR", "PLAN", "SERVER_TAG"}
	runKnobs := func(tag, plan, items, base, results string) []string {
		return []string{"SERVER_TAG=" + tag, "PLAN=" + plan, "ITEMS=" + items, "BASE=" + base, "GATE_FAKE=",
			"GATE_POST=local", "GATE_RESULTS=" + results, "OMES_DIR=.upstream/omes"}
	}
	runGateMatrix(t, []envCase{
		{name: "run", args: []string{"gate", "run", "1.31.0"}, how: script.Exec, script: "gate/run.sh",
			knobs: runKnobs("1.31.0", "gate/plan.yaml", "", "main", "gate/results"), strays: runStrays,
			errOut: "laug · gate run: SERVER_TAG=9.9.9 in the environment is not passed on; <tag> sets it"},
		{name: "run, every flag", args: []string{"gate", "run", "-plan", "gate/plans/noise.yaml", "1.32.1", "-items",
			"2", "-base", "HEAD", "-post", "local", "-results", "results-x"}, how: script.Exec, script: "gate/run.sh",
			knobs: runKnobs("1.32.1", "gate/plans/noise.yaml", "2", "HEAD", "results-x"), strays: runStrays},
		{name: "run, no tag", args: []string{"gate", "run"}, code: 2, errOut: "laug · gate run: names no server " +
			"release: give it the tag it judges, x.y.z (laug gate run 1.31.0 -fake runs a gate on fakes)"},
		{name: "run, not a release", args: []string{"gate", "run", "1.31"}, code: 2,
			errOut: "laug · gate run: 1.31: not a release tag, x.y.z"},
		{name: "run, two tags", args: []string{"gate", "run", "1.31.0", "1.32.1"}, code: 2},
		{name: "run, -items 0", args: []string{"gate", "run", "1.31.0", "-items", "0"}, code: 2,
			errOut: "laug · gate run: -items 0: how many of the plan's items to run, from 1"},
		{name: "post", args: []string{"gate", "post", "g20261022-120000"}, how: script.Exec, script: "gate/post.sh",
			argv: []string{"gate/results/g20261022-120000"}, knobs: []string{"GATE_POST=local"},
			strays: []string{"GATE", "GATE_POST", "GATE_RESULTS"}},
		{name: "post, a path and a sink", args: []string{"gate", "post", "runs/g1", "-post", "pr-comment"},
			how: script.Exec, script: "gate/post.sh", argv: []string{"runs/g1"}, knobs: []string{"GATE_POST=pr-comment"},
			strays: []string{"GATE", "GATE_POST", "GATE_RESULTS"}},
		{name: "post, two words", args: []string{"gate", "post", "a b"}, code: 2, errOut: "laug · gate post: 'a b': " +
			"neither a gate id in gate/results, one word of letters, digits, ., _ and -, nor the path to a run's directory"},
		{name: "rejudge, a run and -all", args: []string{"gate", "rejudge", "g1", "-all"}, code: 2,
			errOut: "laug · gate rejudge: takes a gate run or -all, not both"},
		{name: "rejudge, -all and -brief", args: []string{"gate", "rejudge", "-all", "-brief"}, code: 2,
			errOut: "laug · gate rejudge: -brief is one run's line; -all prints a line a run already"},
		{name: "rejudge, two runs", args: []string{"gate", "rejudge", "g1", "g2"}, code: 2,
			errOut: `laug · gate rejudge: takes one gate run, got ["g1" "g2"]; -all judges every one`},
		{name: "rejudge, no such run", args: []string{"gate", "rejudge", "g1"}, code: 2,
			errOut: "laug · gate rejudge: no results directory at gate/results/g1"},
		{name: "compare, one run", args: []string{"gate", "compare", "g1"}, code: 2,
			errOut: `laug · gate compare: takes two gate runs, A and B, got ["g1"]`},
		{name: "compare, no such run", args: []string{"gate", "compare", "g1", "g2"}, code: 2,
			errOut: "laug · gate compare: no results directory at gate/results/g1"},
		{name: "signature, no such run", args: []string{"gate", "signature", "runs/g1"}, code: 2,
			errOut: "laug · gate signature: no results directory at runs/g1"},
		// gate tolerance hands load/thresholds.sh no knob, its four cleared and each stray named, then reads the set
		// through yq, which this PATH lacks: the set does not resolve, exit 2. TestGateToleranceSet goes further.
		{name: "tolerance", args: []string{"gate", "tolerance", "g20261017-120000", "-pair", "s1:s2",
			"g20261017-130000-2", "-evidence", "cells/results/x.txt"}, code: 2, how: script.Run,
			script: "load/thresholds.sh", argv: []string{""},
			files: map[string]string{"gate/results/g20261017-120000/gate.json": "{}",
				"gate/results/g20261017-130000-2/gate.json": "{}"},
			strays: []string{"EVIDENCE", "GATES", "GATE_RESULTS", "PAIR"},
			errOut: "laug · gate tolerance: GATES=stray in the environment is not passed on; gate tolerance <gate id>... " +
				"sets it"},
		{name: "tolerance, -pair one item twice", args: []string{"gate", "tolerance", "g20261017-120000", "-pair", "a:a"},
			code: 2, files: map[string]string{"gate/results/g20261017-120000/gate.json": "{}"},
			strays: []string{"EVIDENCE", "GATES", "GATE_RESULTS", "PAIR"},
			errOut: "laug · gate tolerance <gate id>... [-pair <a>:<b>] [-evidence cells/results/<file>]: " +
				"-pair a:a names one item twice"},
		{name: "tolerance, no such gate", args: []string{"gate", "tolerance", "g20261017-120000", "-results", "runs"},
			code: 2, strays: []string{"EVIDENCE", "GATES", "GATE_RESULTS", "PAIR"},
			errOut: "laug · gate tolerance <gate id>... [-pair <a>:<b>] [-evidence cells/results/<file>]: " +
				"no gate run g20261017-120000 in -results, runs"},
		{name: "tolerance, -tables and -results", args: []string{"gate", "tolerance", "g20261017-120000", "-tables",
			"-results", "runs"}, code: 2,
			errOut: "laug · gate tolerance: -tables reads docs/tables, and -results names directories: give one"},
		{name: "tolerance, -tables and -results at its default", args: []string{"gate", "tolerance",
			"g20261017-120000", "-tables", "-results", "gate/results"}, code: 2,
			errOut: "laug · gate tolerance: -tables reads docs/tables, and -results names directories: give one"},
		{name: "tolerance, not a gate id", args: []string{"gate", "tolerance", "g20261017-120000", "g1"}, code: 2,
			errOut: "laug · gate tolerance: g1 is not a gate id, g<date>-<time>"},
		{name: "tolerance, no gate", args: []string{"gate", "tolerance", "-pair", "s1:s2"}, code: 2,
			errOut: "laug · gate tolerance: names no gate run: give it the A/A gates' ids in gate/results"},
	})

	t.Run("-fake", func(t *testing.T) { testGateFake(t) })
	t.Run("stale", func(t *testing.T) { testGateStale(t) })
	t.Run("host type", func(t *testing.T) { testGateHostType(t) })
	t.Run("stale, by git", func(t *testing.T) { testGateStaleGit(t) })
	t.Run("-json", func(t *testing.T) { testGateJSON(t) })
	t.Run("rejudge", func(t *testing.T) { testGateRejudge(t) })
	t.Run("judgeNotHeld", func(t *testing.T) { testJudgeNotHeld(t) })
	t.Run("signature and compare", func(t *testing.T) { testGateSignature(t) })
	t.Run("open", func(t *testing.T) { testGateOpen(t) })
	t.Run("bare", func(t *testing.T) { testGateBare(t) })
	t.Run("exit", func(t *testing.T) { testGateExit(t) })
	t.Run("stampFrom", func(t *testing.T) { testStampFrom(t) })
}

// envOf is the value of name in env, the first entry's.
func envOf(env []string, name string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// testGateFake: -fake makes a dir of its own under TMPDIR, laug-fake-gate-*, holding results/, an empty
// omes/cmd/omes and fake/, and hands gate/run.sh GATE_FAKE=1 and those three, and the dir as FAKE_GATE_TMP, whose
// last line names it; nothing lands in the checkout. A FAKE_GATE_DIR in the environment stands, and -results puts the
// run where it says. -dry-run makes nothing.
func testGateFake(t *testing.T) {
	for _, c := range []struct {
		name          string
		args, more    []string
		results, fake string // the run's dir and FAKE_GATE_DIR, under the new dir unless absolute or relative
		made          []string
	}{
		{"own dirs", nil, nil, "results", "fake", []string{"fake", "omes", "omes/cmd", "omes/cmd/omes", "results"}},
		{"FAKE_GATE_DIR set", nil, []string{"FAKE_GATE_DIR=/elsewhere"}, "results", "/elsewhere",
			[]string{"omes", "omes/cmd", "omes/cmd/omes", "results"}},
		{"-results", []string{"-results", "results-x"}, nil, "results-x", "fake",
			[]string{"fake", "omes", "omes/cmd", "omes/cmd/omes"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tmp := t.TempDir()
			a, rec := gateApp(t, envCase{more: append([]string{"TMPDIR=" + tmp}, c.more...)})
			code, out, errOut := runApp(t, a, append([]string{"gate", "run", "1.31.0", "-fake"}, c.args...)...)
			if code != 0 || len(rec.calls) != 1 {
				t.Fatalf("exit %d, %d calls\n%s%s", code, len(rec.calls), out, errOut)
			}
			dirs, _ := filepath.Glob(filepath.Join(tmp, "laug-fake-gate-*"))
			if len(dirs) != 1 {
				t.Fatalf("%d dirs laug-fake-gate-* in TMPDIR, want 1", len(dirs))
			}
			dir := dirs[0]
			var made []string
			filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if p != dir {
					rel, _ := filepath.Rel(dir, p)
					made = append(made, rel)
				}
				return err
			})
			if !slices.Equal(made, c.made) {
				t.Errorf("the dir holds %q, want %q", made, c.made)
			}
			at := func(p string) string {
				if filepath.IsAbs(p) || p == "results-x" {
					return p
				}
				return filepath.Join(dir, p)
			}
			env := rec.envs[0]
			for name, want := range map[string]string{"GATE_FAKE": "1", "GATE_RESULTS": at(c.results),
				"OMES_DIR": filepath.Join(dir, "omes"), "FAKE_GATE_DIR": at(c.fake), "FAKE_GATE_TMP": dir} {
				if got := envOf(env, name); got != want {
					t.Errorf("%s=%s, want %s", name, got, want)
				}
			}
			if strings.Count(strings.Join(env, "\n"), "FAKE_GATE_DIR=") != 1 {
				t.Errorf("FAKE_GATE_DIR is in the script's environment more than once: %q", env)
			}
			if !strings.HasPrefix(out, "laug · gate run 1.31.0 -fake: fakes stand in for Docker, the cells and Omes; "+
				"the run in "+at(c.results)+", the fakes' files in ") {
				t.Errorf("first line: %s", firstLine(out))
			}
			var names []string
			ents, _ := os.ReadDir(a.Root)
			for _, e := range ents {
				names = append(names, e.Name())
			}
			if !slices.Equal(names, []string{"bin", "faults", "gate", "go.mod"}) {
				t.Errorf("the checkout holds %q after -fake, more than gateApp made", names)
			}
			if _, err := os.Stat(filepath.Join(a.Root, "gate", "results")); !os.IsNotExist(err) {
				t.Errorf("gate/results exists after -fake: %v", err)
			}
		})
	}
	t.Run("-dry-run", func(t *testing.T) {
		tmp := t.TempDir()
		a, rec := gateApp(t, envCase{more: []string{"TMPDIR=" + tmp}})
		code, out, errOut := runApp(t, a, "gate", "run", "1.31.0", "-fake", "-dry-run")
		d := tmp + "/laug-fake-gate-*"
		want := "FAKE_GATE_TMP='" + d + "' FAKE_GATE_DIR='" + d + "/fake' SERVER_TAG='1.31.0' PLAN='gate/plan.yaml' " +
			"ITEMS='' BASE='main' GATE_FAKE='1' GATE_POST='local' GATE_RESULTS='" + d + "/results' OMES_DIR='" + d +
			"/omes' gate/run.sh\n"
		if code != 0 || out != want || len(rec.calls) != 0 {
			t.Errorf("exit %d, %d calls\n got %s\nwant %s%s", code, len(rec.calls), out, want, errOut)
		}
		if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
			t.Errorf("-dry-run made %d entries in TMPDIR", len(ents))
		}
	})
	// TMPDIR under HOME: the first line, and -dry-run's line, write the dirs ~/ and the rest, a shell word that reads
	// as $HOME, and never the home path.
	t.Run("TMPDIR under HOME", func(t *testing.T) {
		home := t.TempDir()
		tmp := filepath.Join(home, "tmp")
		if err := os.Mkdir(tmp, 0o755); err != nil {
			t.Fatal(err)
		}
		a, _ := gateApp(t, envCase{more: []string{"TMPDIR=" + tmp}})
		a.Environ = append([]string{"HOME=" + home}, a.Environ...)
		code, out, errOut := runApp(t, a, "gate", "run", "1.31.0", "-fake")
		dirs, _ := filepath.Glob(filepath.Join(tmp, "laug-fake-gate-*"))
		if len(dirs) != 1 {
			t.Fatalf("exit %d, %d dirs laug-fake-gate-* in TMPDIR, want 1\n%s%s", code, len(dirs), out, errOut)
		}
		d := "~/tmp/" + filepath.Base(dirs[0])
		if want := "laug · gate run 1.31.0 -fake: fakes stand in for Docker, the cells and Omes; the run in " + d +
			"/results, the fakes' files in " + d + "/fake\n"; code != 0 || out != want || strings.Contains(errOut, home) {
			t.Errorf("exit %d\n got %s\nwant %s%s", code, out, want, errOut)
		}
		a, _ = gateApp(t, envCase{more: []string{"TMPDIR=" + tmp}})
		a.Environ = append([]string{"HOME=" + home}, a.Environ...)
		code, out, errOut = runApp(t, a, "gate", "run", "1.31.0", "-fake", "-dry-run")
		d = "~/'tmp/laug-fake-gate-*"
		want := "FAKE_GATE_TMP=" + d + "' FAKE_GATE_DIR=" + d + "/fake' SERVER_TAG='1.31.0' PLAN='gate/plan.yaml' " +
			"ITEMS='' BASE='main' GATE_FAKE='1' GATE_POST='local' GATE_RESULTS=" + d + "/results' OMES_DIR=" + d +
			"/omes' gate/run.sh\n"
		if code != 0 || out != want || strings.Contains(errOut, home) {
			t.Errorf("-dry-run: exit %d\n got %s\nwant %s%s", code, out, want, errOut)
		}
	})
}

// testGateStale: gate run holds bin/laug's stamp to git rev-parse HEAD, and refuses, exit 2 with nothing run, a stamp
// of another commit, none, a binary it cannot read, or one built with changes the tree no longer holds; a tree with
// changes gets a warning line and runs. An earlier run's directory under gate/results is no change, as gate.json's
// tree_dirty reads the tree, with bin/laug built before the run or after it, -dirty for it. With -fake, the refusal
// names -allow-stale, which runs anyway; on cells it names none, and -allow-stale without -fake is a usage error, since
// gate/run.sh's preflight refuses the same bin/laug. What make gate built first is refused too.
func testGateStale(t *testing.T) {
	refused := "laug · gate run: %s"
	refusedFake := refused + "; -allow-stale runs the gate anyway"
	for _, c := range []struct {
		name      string
		stamp     string
		err       error
		status    string // git status --porcelain
		head      string // git rev-parse HEAD's answer, when not headSHA; "-" for none
		args      []string
		code      int
		line      string // a stderr line, when not empty
		drop      string // a file removed from the checkout first
		warnLines int    // lines that say gate.json records tree_dirty
	}{
		{name: "HEAD", stamp: headSHA},
		{name: "another commit", stamp: otherSHA, code: 2, line: fmt.Sprintf(refused,
			"bin/laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD")},
		{name: "another commit, -fake", stamp: otherSHA, args: []string{"-fake"}, code: 2, line: fmt.Sprintf(refusedFake,
			"bin/laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD")},
		{name: "another commit, -fake -allow-stale", stamp: otherSHA, args: []string{"-fake", "-allow-stale"}},
		{name: "another commit, -allow-stale without -fake", stamp: otherSHA, args: []string{"-allow-stale"}, code: 2,
			line: "laug · gate run: -allow-stale takes -fake: gate/run.sh's preflight refuses a gate on cells whose " +
				"bin/laug was not built from HEAD all the same, as a refused run in -results"},
		{name: "HEAD, -allow-stale without -fake", stamp: headSHA, args: []string{"-allow-stale", "-dry-run"}, code: 2},
		{name: "no bin/laug, -fake", err: os.ErrNotExist, args: []string{"-fake"}, code: 2, line: fmt.Sprintf(refusedFake,
			"there is no bin/laug: make build builds it")},
		{name: "dev", stamp: "dev", code: 2, line: fmt.Sprintf(refused,
			"bin/laug carries no commit: make build stamps it with HEAD's")},
		{name: "no bin/laug", err: os.ErrNotExist, code: 2, line: fmt.Sprintf(refused,
			"there is no bin/laug: make build builds it")},
		{name: "not a Go binary", err: errors.New("unrecognized file format"), code: 2, line: fmt.Sprintf(refused,
			"bin/laug's build info: unrecognized file format: make build builds it again")},
		{name: "built dirty, the tree clean", stamp: headSHA + "-dirty", code: 2, line: fmt.Sprintf(refused,
			"bin/laug was built from 2222222-dirty with changes the tree no longer holds: make build builds it from HEAD")},
		{name: "built dirty, the tree dirty", stamp: headSHA + "-dirty", status: " M gate/run.sh", warnLines: 1,
			line: "laug · gate run: bin/laug was built from HEAD with changes not committed, and the tree has some; " +
				"laug cannot tell whether they are the same (gate.json records tree_dirty)"},
		{name: "built clean, the tree dirty", stamp: headSHA, status: "?? new.txt", warnLines: 1,
			line: "laug · gate run: the tree has changes not committed, and bin/laug was built from HEAD without them; " +
				"make build builds them in (gate.json records tree_dirty)"},
		{name: "built clean, an earlier run's directory", stamp: headSHA, status: "?? gate/results/g20261024-092853/"},
		{name: "built dirty, an earlier run's directory", stamp: headSHA + "-dirty",
			status: "?? gate/results/g20261024-092853/"},
		{name: "built clean, an earlier run's directory and a change", stamp: headSHA,
			status: "?? gate/results/g20261024-092853/\n M gate/run.sh", warnLines: 1,
			line: "laug · gate run: the tree has changes not committed, and bin/laug was built from HEAD without them; " +
				"make build builds them in (gate.json records tree_dirty)"},
		{name: "built dirty, an earlier run's directory and a change", stamp: headSHA + "-dirty",
			status: "?? gate/results/g20261024-092853/\n M gate/run.sh", warnLines: 1,
			line: "laug · gate run: bin/laug was built from HEAD with changes not committed, and the tree has some; " +
				"laug cannot tell whether they are the same (gate.json records tree_dirty)"},
		{name: "no HEAD", stamp: headSHA, head: "-", code: 2},
		{name: "no bin/laug", stamp: headSHA, drop: "bin/laug", code: 2, line: "laug · gate run: bin/laug, whose " +
			"pool serve is the gate's own pool, is not built: make build builds it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := gateApp(t, envCase{more: []string{"TMPDIR=" + t.TempDir()}})
			if c.status != "" {
				a.Environ = append([]string{"STUB_STATUS=" + c.status}, a.Environ...)
			}
			if c.head == "-" {
				a.Environ = append([]string{"STUB_HEAD="}, a.Environ...)
			}
			stamp(t, c.stamp, c.err)
			if c.drop != "" {
				os.Remove(filepath.Join(a.Root, c.drop))
			}
			code, out, errOut := runApp(t, a, append([]string{"gate", "run", "1.31.0"}, c.args...)...)
			ran := len(rec.calls) == 1
			if code != c.code || ran != (c.code == 0) {
				t.Errorf("exit %d, ran %t; want %d\n%s%s", code, ran, c.code, out, errOut)
			}
			if c.line != "" && !slices.Contains(strings.Split(errOut, "\n"), c.line) {
				t.Errorf("stderr lacks %q:\n%s", c.line, errOut)
			}
			if n := strings.Count(errOut, "tree_dirty"); n != c.warnLines {
				t.Errorf("stderr holds %d lines on tree_dirty, want %d:\n%s", n, c.warnLines, errOut)
			}
		})
	}
}

// testGateHostType: without -fake, gate run refuses a host that declares no host type, exit 2 with nothing run or
// written, since gate/run.sh's preflight would refuse it and keep a BLOCK run in -results; -workflow too. The type is
// read where laug host type -declare writes it, XDG_CONFIG_HOME's laug/host-type, else HOME's .config. A host that
// declares a type, any type, goes on to the script, whose preflight refuses another type than the set's as a record.
// -fake and -dry-run read no declaration.
func testGateHostType(t *testing.T) {
	const refused = "laug · gate run: this host declares no host type, which a gate on cells refuses: laug host type " +
		"-declare <type> declares one; laug gate run 1.32.1 -fake runs a gate on fakes"
	for _, c := range []struct {
		name    string
		file    *string // HOME's .config/laug/host-type; nil, none
		xdg     *string // XDG_CONFIG_HOME's laug/host-type, and XDG_CONFIG_HOME set; nil, unset
		args    []string
		refused bool
	}{
		{name: "none declared", refused: true},
		{name: "none declared, -workflow", args: []string{"-workflow"}, refused: true},
		{name: "an empty file", file: ptr(""), refused: true},
		{name: "an empty first line", file: ptr("\nm8i.8xlarge\n"), refused: true},
		{name: "declared", file: ptr("m8i.8xlarge\n")},
		{name: "declared, no newline", file: ptr("m8i.8xlarge")},
		{name: "another type: the script refuses it, as a record", file: ptr("m7i.8xlarge\n")},
		{name: "not a type name: the script refuses it, as a record", file: ptr("M8I 8XL\n")},
		{name: "in XDG_CONFIG_HOME", xdg: ptr("m8i.8xlarge\n")},
		{name: "XDG_CONFIG_HOME set, HOME's ignored", file: ptr("m8i.8xlarge\n"), xdg: ptr(""), refused: true},
		{name: "none declared, -fake", args: []string{"-fake"}},
		{name: "none declared, -dry-run", args: []string{"-dry-run"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := gateApp(t, envCase{more: []string{"TMPDIR=" + t.TempDir()}})
			home := filepath.Join(a.getenv("HOME"), ".config", "laug", "host-type")
			os.Remove(home)
			if c.file != nil {
				writeFile(t, home, *c.file)
			}
			if c.xdg != nil {
				xdg := t.TempDir()
				if *c.xdg != "" {
					writeFile(t, filepath.Join(xdg, "laug", "host-type"), *c.xdg)
				}
				a.Environ = append([]string{"XDG_CONFIG_HOME=" + xdg}, a.Environ...)
			}
			code, out, errOut := runApp(t, a, append([]string{"gate", "run", "1.32.1"}, c.args...)...)
			if c.refused {
				if code != 2 || len(rec.calls) != 0 || out != "" || errOut != refused+"\n" {
					t.Errorf("exit %d, %d calls\nstdout:\n%s\nstderr:\n%s\nwant:\n%s", code, len(rec.calls), out, errOut,
						refused)
				}
				if _, err := os.Stat(filepath.Join(a.Root, "gate", "results")); !os.IsNotExist(err) {
					t.Errorf("gate/results exists after the refusal: %v", err)
				}
				return
			}
			if code != 0 || strings.Contains(errOut, "host type") {
				t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			if dry := slices.Contains(c.args, "-dry-run"); len(rec.calls) != map[bool]int{true: 0, false: 1}[dry] {
				t.Errorf("%d calls, -dry-run %t", len(rec.calls), dry)
			}
		})
	}
}

// testGateStaleGit: checkStamp with git itself, in a repository of its own whose one commit holds a run under
// gate/results. A run's directory left there, or a committed run changed, is no change to the tree gate.json records,
// with bin/laug built before it or after it (-dirty, as make build's git status over the whole tree stamps it then); a
// file outside gate/results is one. gate/run.sh and gate/step.sh read tree_dirty by the same pathspec.
func testGateStaleGit(t *testing.T) {
	const changed = "the tree has changes not committed, and bin/laug was built from HEAD without them; make build " +
		"builds them in (gate.json records tree_dirty)"
	for _, c := range []struct {
		name         string
		dirty        bool              // the stamp is HEAD's -dirty form
		files        map[string]string // written after the commit
		refuse, warn string
	}{
		{name: "clean"},
		{name: "a run's directory", files: map[string]string{"gate/results/g1/gate.json": "{}\n"}},
		{name: "a run's directory, built after it", dirty: true,
			files: map[string]string{"gate/results/g1/gate.json": "{}\n"}},
		{name: "a committed run changed", files: map[string]string{"gate/results/g0/gate.json": "{\"x\":1}\n"}},
		{name: "a file outside gate/results", files: map[string]string{"gate/results/g1/gate.json": "{}\n",
			"new.txt": "x\n"}, warn: changed},
		{name: "built dirty, the tree clean", dirty: true,
			refuse: "bin/laug was built from %s-dirty with changes the tree no longer holds: make build builds it from HEAD"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, repo := gitEnv(t), t.TempDir()
			writeFile(t, filepath.Join(repo, "go.mod"), "module x\n")
			writeFile(t, filepath.Join(repo, "gate", "results", "g0", "gate.json"), "{}\n")
			gitDo(t, env, repo, "init", "-q", "-b", "main")
			gitDo(t, env, repo, "add", ".")
			gitDo(t, env, repo, "commit", "-q", "-m", "one")
			head := gitDo(t, env, repo, "rev-parse", "HEAD")
			for p, content := range c.files {
				writeFile(t, filepath.Join(repo, p), content)
			}
			s := head
			if c.dirty {
				s += "-dirty"
			}
			stamp(t, s, nil)
			want := c.refuse
			if want != "" {
				want = fmt.Sprintf(want, head[:7])
			}
			a := &App{Root: repo, Environ: env}
			if refuse, warn := a.checkStamp(context.Background()); refuse != want || warn != c.warn {
				t.Errorf("refuse %q, warn %q; want %q, %q", refuse, warn, want, c.warn)
			}
		})
	}
	shell := "git status --porcelain " + strings.Join(treePathspec[:2], " ") + " '" + treePathspec[2] + "'"
	for _, f := range []string{"gate/run.sh", "gate/step.sh"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), f))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(b), shell); n != 1 {
			t.Errorf("%s holds %q %d times, want 1: tree_dirty and the warning read the tree alike", f, shell, n)
		}
	}
}

// testGateJSON: -json keeps gate/run.sh a child, sends its text to stderr and prints the verdict.json of the run its
// first line names, on one line; with no run named, nothing; the exit is the script's either way.
func testGateJSON(t *testing.T) {
	for _, c := range []struct {
		name, first string
		code        int
		want        string
	}{
		{"BLOCK", "laug · gate g20261022-120000-2: server 1.31.0, plan gate/plan.yaml", 1,
			`{"gate_id":"g20261022-120000-2","verdict":"BLOCK"}` + "\n"},
		{"no run", "laug · SERVER_TAG must be a release tag", 2, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := gateApp(t, envCase{})
			rec.run = func(_ script.Command, _ []string, stdout, _ io.Writer) int {
				dir := filepath.Join(a.Root, "gate", "results", "g20261022-120000-2")
				writeFile(t, filepath.Join(dir, "verdict.json"),
					"{\n  \"gate_id\": \"g20261022-120000-2\",\n  \"verdict\": \"BLOCK\"\n}\n")
				// The first line comes in two writes, as a pipe may give it.
				io.WriteString(stdout, c.first[:9])
				io.WriteString(stdout, c.first[9:]+"\nBLOCK\n")
				return c.code
			}
			code, out, errOut := runApp(t, a, "gate", "run", "1.31.0", "-json")
			if code != c.code || out != c.want || !strings.Contains(errOut, c.first+"\nBLOCK\n") {
				t.Errorf("exit %d, want %d\nstdout %q, want %q\nstderr:\n%s", code, c.code, out, c.want, errOut)
			}
			if rec.hows[0] != script.Run {
				t.Errorf("-json ran the script by %v, want Run", rec.hows[0])
			}
		})
	}
}

// testGateOpen: open reads a run's verdict.json in-process, on a committed run and on made ones.
func testGateOpen(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// Its paths are relative to the checkout's root, given by id or by its absolute path.
	dir := "gate/results/g20261018-092540"
	want := "laug · gate open g20261018-092540: server 1.32.1, class regression\ndirectory   " + dir +
		"\nverdict.md  " + dir + "/verdict.md\nBLOCK\n"
	for _, arg := range []string{"g20261018-092540", filepath.Join(root, dir)} {
		code, out, errOut := runApp(t, &App{Root: root}, "gate", "open", arg)
		if code != 0 || out != want {
			t.Errorf("a committed run, %s: exit %d\n got %q\nwant %q\n%s", arg, code, out, want, errOut)
		}
	}
	// A run outside the checkout, under $HOME, is ~/ and the rest; elsewhere, as it is.
	home := t.TempDir()
	copyRun(t, "g20261018-092540", filepath.Join(home, "runs", "g2"))
	for _, c := range []struct{ env, want string }{{"HOME=" + home, "~/runs/g2"}, {"HOME=/nowhere",
		filepath.Join(home, "runs", "g2")}} {
		_, out, _ := runApp(t, &App{Root: root, Environ: []string{c.env}}, "gate", "open", filepath.Join(home, "runs",
			"g2"))
		if w := "\ndirectory   " + c.want + "\nverdict.md  " + c.want + "/verdict.md\n"; !strings.Contains(out, w) {
			t.Errorf("a run outside the checkout, %s:\n%s\nwant it to hold %q", c.env, out, w)
		}
	}
	r := checkout(t)
	writeFile(t, filepath.Join(r, "runs", "g1", "verdict.json"),
		`{"gate_id": "g20261022-120000", "verdict": "ADVANCE", "class": null, "server_tag": "1.31.0", `+
			`"stopped_at": null, "fake": true}`)
	writeFile(t, filepath.Join(r, "gate", "results", "g20261022-130000", "gate.json"), "{}")
	for _, c := range []struct {
		name  string
		args  []string
		code  int
		first string // stdout's first line, or stderr's when the exit is not 0
	}{
		{"a path, on fakes", []string{"runs/g1"}, 0, "laug · gate open g20261022-120000: server 1.31.0, on fakes"},
		{"no such run", []string{"g20261022-140000"}, 1, "laug · gate open: no gate run g20261022-140000: " +
			"gate/results/g20261022-140000 is not a directory"},
		{"no verdict yet", []string{"g20261022-130000"}, 1, "laug · gate open g20261022-130000: no verdict: there " +
			"is no verdict.json in it; a gate that runs, or stopped before it judged, has none yet"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runApp(t, &App{Root: r}, append([]string{"gate", "open"}, c.args...)...)
			got := firstLine(out)
			if code != 0 {
				got = firstLine(errOut)
			}
			if code != c.code || got != c.first {
				t.Errorf("exit %d, want %d\n got %q\nwant %q", code, c.code, got, c.first)
			}
		})
	}
}

// testGateBare: bare, a verb that reads a run names the newest in -results and the line that reads it; the newest
// goes by time, and a second run in a second after the first.
func testGateBare(t *testing.T) {
	r := checkout(t)
	for _, id := range []string{"g20261022-120000", "g20261022-120000-2", "g20261021-235959", "g20261022-120000-10"} {
		writeFile(t, filepath.Join(r, "gate", "results", id, "verdict.json"), `{"verdict": "ADVANCE"}`)
	}
	writeFile(t, filepath.Join(r, "gate", "results", "notes", "x"), "")
	code, out, errOut := runApp(t, &App{Root: r}, "gate", "rejudge")
	want := "laug · gate rejudge: names no gate run: give it a gate id in gate/results, or the path to a run's " +
		"directory; or -all, every run there\nthe newest run in gate/results is g20261022-120000-10 (ADVANCE):\n" +
		"  laug gate rejudge g20261022-120000-10\nusage: laug gate rejudge"
	if code != 2 || out != "" || !strings.HasPrefix(errOut, want) {
		t.Errorf("exit %d\nstdout %q\nstderr:\n%s\nwant it to start:\n%s", code, out, errOut, want)
	}
	code, _, errOut = runApp(t, &App{Root: r}, "gate", "open", "-results", "elsewhere")
	if code != 2 || !strings.Contains(errOut, "\nelsewhere holds no gate run; laug gate run <tag> makes one\n") {
		t.Errorf("an empty -results: exit %d\n%s", code, errOut)
	}
}

// testGateExit: the built binary, stamped as make build stamps it, runs a stub gate/run.sh in place of itself, so the
// script's exit is laug's: 0 ADVANCE, 1 BLOCK, 2 and 3 as they come. The stamp is read from bin/laug's build info;
// one of another commit is refused, exit 2, with the stub never run.
func testGateExit(t *testing.T) {
	root := checkout(t)
	out, err := exec.Command("go", "build", "-ldflags", "-X main.commit="+headSHA, "-o",
		filepath.Join(root, "bin", "laug"), ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	writeExec(t, filepath.Join(root, "gate", "run.sh"), "#!/bin/sh\n"+
		`echo "laug · gate g20261022-120000: server $SERVER_TAG, from $0"`+"\n"+`exit "$FAKE_STUB_EXIT"`+"\n")
	bin, config := t.TempDir(), t.TempDir()
	writeExec(t, filepath.Join(bin, "git"), gitStub)
	writeFile(t, filepath.Join(config, "laug", "host-type"), "m8i.8xlarge\n")
	for _, c := range []struct {
		head string
		exit int
		want int
	}{{headSHA, 0, 0}, {headSHA, 1, 1}, {headSHA, 2, 2}, {headSHA, 3, 3}, {otherSHA, 0, 2}} {
		t.Run(fmt.Sprintf("HEAD %.7s, exit %d", c.head, c.exit), func(t *testing.T) {
			cmd := exec.Command(filepath.Join(root, "bin", "laug"), "gate", "run", "1.31.0")
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "STUB_HEAD=" + c.head,
				"FAKE_STUB_EXIT=" + fmt.Sprint(c.exit), "XDG_CONFIG_HOME=" + config}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			ran := stdout.String() == "laug · gate g20261022-120000: server 1.31.0, from gate/run.sh\n"
			if code != c.want || ran != (c.head == headSHA) {
				t.Errorf("exit %d, want %d; the stub ran: %t\nstdout:\n%s\nstderr:\n%s", code, c.want, ran,
					stdout.String(), stderr.String())
			}
		})
	}
}

// testStampFrom: the stamp is make build's -X main.commit, else the VCS stamp, -dirty for a modified tree, else "".
func testStampFrom(t *testing.T) {
	info := func(kv ...string) *debug.BuildInfo {
		bi := &debug.BuildInfo{}
		for i := 0; i+1 < len(kv); i += 2 {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return bi
	}
	for _, c := range []struct {
		info *debug.BuildInfo
		want string
	}{
		{info("-ldflags", "-X main.commit="+headSHA, "vcs.revision", otherSHA), headSHA},
		{info("-ldflags", "-s -X=main.commit="+headSHA+"-dirty"), headSHA + "-dirty"},
		{info("-ldflags", "-s -w", "vcs.revision", otherSHA, "vcs.modified", "true"), otherSHA + "-dirty"},
		{info("vcs.revision", otherSHA, "vcs.modified", "false"), otherSHA},
		{info("-ldflags", "-X main.other=x"), ""},
		{info(), ""},
	} {
		if got := stampFrom(c.info); got != c.want {
			t.Errorf("stampFrom(%v) = %q, want %q", c.info.Settings, got, c.want)
		}
	}
	if _, err := stampOf(filepath.Join(t.TempDir(), "laug")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stampOf on no file: %v, want os.ErrNotExist", err)
	}
}

// testGateRejudge: rejudge in Go on committed runs, a dev host run by the path of its copy (runPath), and on made
// ones, its judge held to HEAD's by a stand-in: the long form, -brief, -all's lines and counts, each exit, and every
// run not judged when the judge is not held.
func testGateRejudge(t *testing.T) {
	root := repoRoot(t)
	held(t, "")
	self := judge.Self().SHA256[:12]
	for _, c := range []struct {
		name string
		args []string
		code int
		out  string
	}{
		{"identical", []string{"g20261018-092540"}, 0, "laug · gate rejudge g20261018-092540: the run in " +
			"gate/results/g20261018-092540, judged again in Go; nothing is written\nverdict.json  identical\n" +
			"verdict.md    identical\njudge         v2: gate.json records gate/combine.sh 33b4e89dc2ec; laug's judge " +
			"sources " + self + "\n"},
		{"-brief", []string{"g20261018-092540", "-brief"}, 0, "g20261018-092540  identical\n"},
		// An id and -results. The App's environment is empty, so no jq is on PATH: the rejudge judges in Go and
		// calls none.
		{"an id and -results, -brief", []string{"g20261018-075357", "-brief", "-results", filepath.Join(root, "gate",
			"results")}, 0, "g20261018-075357  identical\n"},
		{"a path, -brief", []string{runPath("g20261014-215905"), "-brief"}, 0, "g20261014-215905  identical\n"},
		{"empty-dirs, -brief", []string{runPath("g20261014-212013"), "-brief"}, 1, "g20261014-212013  differs: an " +
			"empty item directory (steady/), which git does not carry: a fresh clone judges it as no directory\n"},
		{"a trap, -brief", []string{runPath("g20261017-014142"), "-brief"}, 1,
			"g20261017-014142  re-rendered: trap T2\n"},
		{"a trap", []string{runPath("g20261014-213037")}, 1, "laug · gate rejudge g20261014-213037: the run in " +
			runPath("g20261014-213037") + ", judged again in Go; nothing is written\nverdict.json  differs, first at " +
			"line 4\n" +
			"  the run's    \"class\": \"invalid\",\n  rejudge      \"class\": \"harness\",\nverdict.md    differs, first " +
			"at line 3\n  the run's  Gate run g20261014-213037 on server 1.31.0 stopped at step lease steady before " +
			"gate/combine.sh judged it: stopped: gate/run.sh stopped at step lease steady (signal TERM) before " +
			"gate/combine.sh judged; nothing was judged.\n  rejudge    Gate run g20261014-213037 on server 1.31.0: 2 " +
			"item(s), 41.032 s from the gate start to this verdict on the wall clock (40.77 s monotonic).\n" +
			"judge         v1: gate.json records gate/combine.sh 284a759c5455; laug's judge sources " + self +
			"\nre-rendered   trap T1: verdict.json is the gate's trap verdict, not a judge's, and that form re-renders " +
			"both files byte for byte\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runApp(t, &App{Root: root}, append([]string{"gate", "rejudge"}, c.args...)...)
			if code != c.code || out != c.out || errOut != "" {
				t.Errorf("exit %d, want %d\n got:\n%s\nwant:\n%s\nstderr:\n%s", code, c.code, out, c.out, errOut)
			}
		})
	}

	// Made runs in a results directory of their own: -all lists them by name, the counts after, and the exit says
	// the worst.
	res := t.TempDir()
	copyRun(t, "g20261018-092540", filepath.Join(res, "a"))
	copyRun(t, "g20261014-212013", filepath.Join(res, "b"))
	copyRun(t, "g20261017-014632", filepath.Join(res, "c"))
	copyRun(t, "g20261018-092540", filepath.Join(res, "d"))
	appendTo(t, filepath.Join(res, "d", "verdict.md"), "tampered\n")
	all := func(t *testing.T) (int, string, string) {
		t.Helper()
		return runApp(t, &App{Root: root}, "gate", "rejudge", "-all", "-results", res)
	}
	first := "laug · gate rejudge -all: every run in " + res + ", judged again in Go under the judge version its " +
		"gate.json records, a line each, then the counts\n"
	code, out, errOut := all(t)
	want := first + "a  identical\nb  differs: an empty item directory (steady/), which git does not carry: a " +
		"fresh clone judges it as no directory\nc  re-rendered: trap T2\nd  differs: unexplained\n" +
		"laug · rejudge, 4 runs: 1 identical, 1 known, 1 re-rendered, 1 unexplained, 0 not judged\n"
	if code != 1 || out != want || errOut != "" {
		t.Errorf("-all: exit %d\n got:\n%s\nwant:\n%s\nstderr:\n%s", code, out, want, errOut)
	}
	code, out, _ = runApp(t, &App{Root: root}, "gate", "rejudge", "d", "-results", res)
	if code != 1 || !strings.Contains(out, "\nverdict.json  identical\nverdict.md    differs, first at line 43\n"+
		"  the run's  tampered\n  rejudge    (the file ends)\n") ||
		!strings.HasSuffix(out, "\nwhy           unexplained\n") {
		t.Errorf("one run, unexplained: exit %d\n%s", code, out)
	}
	must(t, os.RemoveAll(filepath.Join(res, "d")))
	if code, out, _ := all(t); code != 0 || !strings.HasSuffix(out, "laug · rejudge, 3 runs: 1 identical, 1 known, "+
		"1 re-rendered, 0 unexplained, 0 not judged\n") {
		t.Errorf("-all, none unexplained: exit %d\n%s", code, out)
	}
	must(t, os.Remove(filepath.Join(res, "a", "verdict.json")))
	if code, out, _ := all(t); code != 2 || !strings.Contains(out, "\na  not judged: no verdict.json in (outside the "+
		"repository)/a to compare with\n") {
		t.Errorf("-all, one not judged: exit %d\n%s", code, out)
	}
	code, out, _ = runApp(t, &App{Root: root}, "gate", "rejudge", "a", "-results", res)
	if want := "laug · gate rejudge a: the run in (outside the repository)/a, judged again in Go; nothing is " +
		"written\nnot judged    no verdict.json in (outside the repository)/a to compare with\n"; code != 2 || out != want {
		t.Errorf("one run, nothing to compare with: exit %d\n got:\n%s\nwant:\n%s", code, out, want)
	}
	held(t, "laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD")
	code, out, _ = all(t)
	want = first + "a  not judged: no verdict.json in (outside the repository)/a to compare with\n" +
		"b  not judged: laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD\n" +
		"c  not judged: laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD\n" +
		"laug · rejudge, 3 runs: 0 identical, 0 known, 0 re-rendered, 0 unexplained, 3 not judged\n"
	if code != 2 || out != want {
		t.Errorf("-all, the judge not held: exit %d\n got:\n%s\nwant:\n%s", code, out, want)
	}
	// One run, the judge not held: why comes second, and no file line says identical for a judge it then disowns.
	code, out, _ = runApp(t, &App{Root: root}, "gate", "rejudge", "g20261018-092540")
	if want := "laug · gate rejudge g20261018-092540: the run in gate/results/g20261018-092540, judged again in Go; " +
		"nothing is written\nnot judged    laug was built from 1111111, and HEAD is 2222222: make build builds it " +
		"from HEAD\njudge         v2: gate.json records gate/combine.sh 33b4e89dc2ec; laug's judge sources " + self +
		"\n"; code != 2 || out != want {
		t.Errorf("one run, the judge not held: exit %d\n got:\n%s\nwant:\n%s", code, out, want)
	}
	held(t, "")
	// A run bin/laug judge -write judged records the internal/judge entry in place of gate/combine.sh (gate/lib.sh,
	// judge_input): judged again under the version it names, whatever sources judged it, with both sha256s shown
	// side by side; a version no line names is not judged. judge -write writes v3's
	// verdict over each, and says on stderr when gate.json records another.
	for _, c := range []struct {
		version string
		code    int
		tail    string
	}{
		{"v3", 0, "verdict.json  identical\nverdict.md    identical\njudge         v3: gate.json records " +
			"internal/judge v3, sources 33b4e89dc2ec; laug's judge sources " + self + "\n"},
		{"v9", 2, "not judged    gate.json records internal/judge version v9, which internal/judge/versions.txt " +
			"does not name\njudge         v3, the current one: gate.json records internal/judge v9, which no version " +
			"names; laug's judge sources " + self + "\n"},
	} {
		e := filepath.Join(t.TempDir(), "e")
		copyRun(t, "g20261018-092540", e)
		gj := readText(t, filepath.Join(e, "gate.json"))
		writeFile(t, filepath.Join(e, "gate.json"), strings.Replace(gj, `"path": "gate/combine.sh",`,
			`"path": "internal/judge", "version": "`+c.version+`",`, 1))
		if code, _, errOut := runApp(t, &App{Root: root}, "judge", "-write", e); code != 1 || (c.version ==
			judge.Current) != (errOut == "") {
			t.Fatalf("judge -write: exit %d: %s", code, errOut)
		}
		code, out, _ := runApp(t, &App{Root: root}, "gate", "rejudge", e)
		if code != c.code || !strings.HasSuffix(out, "nothing is written\n"+c.tail) {
			t.Errorf("an internal/judge entry %s: exit %d, want %d\n got:\n%s\nwant it to end:\n%s", c.version, code,
				c.code, out, c.tail)
		}
	}
	must(t, os.Mkdir(filepath.Join(res, "empty"), 0o755))
	for _, c := range []struct{ dir, err string }{
		{filepath.Join(res, "none"), "laug · gate rejudge: no results directory at (outside the repository)/none; " +
			"-results names none\n"},
		{filepath.Join(res, "empty"), "laug · gate rejudge: no gate run in (outside the repository)/empty\n"},
	} {
		code, out, errOut := runApp(t, &App{Root: root}, "gate", "rejudge", "-all", "-results", c.dir)
		if code != 2 || errOut != c.err || strings.Count(out, "\n") != 1 {
			t.Errorf("-all -results %s: exit %d\nstdout:\n%s\nstderr:\n%s", c.dir, code, out, errOut)
		}
	}
}

// testJudgeNotHeld: the judge a rejudge runs is held to HEAD's: laug built from HEAD, with -dirty or without, git
// status clean under internal/judge and internal/jsonx, and the sources there the ones this binary embeds. Each way
// one fails is named.
func testJudgeNotHeld(t *testing.T) {
	saved := commit
	t.Cleanup(func() { commit = saved })
	r := judgeCheckout(t)
	bin := filepath.Join(t.TempDir(), "bin")
	writeExec(t, filepath.Join(bin, "git"), gitStub)
	for _, c := range []struct {
		name, commit string
		env          []string
		root         string
		edit         bool
		want         string
	}{
		{name: "held", commit: headSHA},
		{name: "built with changes", commit: headSHA + "-dirty"},
		{name: "another commit", commit: otherSHA,
			want: "laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD"},
		{name: "no commit", commit: "",
			want: "this laug carries no commit, so its judge cannot be held to HEAD's: make build stamps HEAD's"},
		{name: "the judge changed, git says", commit: headSHA, env: []string{"STUB_JUDGE_STATUS= M internal/judge/rules.go"},
			want: "internal/judge and internal/jsonx have changes not committed: git status lists 1"},
		{name: "the judge changed on disk", commit: headSHA, edit: true, want: "laug's judge (sources' sha256 "},
		{name: "no HEAD", commit: headSHA, env: []string{"STUB_HEAD="},
			want: "git rev-parse HEAD: exit status 128, so laug's judge cannot be held to HEAD's"},
		{name: "no git", commit: headSHA, env: []string{"PATH=/nowhere"},
			want: "git rev-parse HEAD: no git on PATH, so laug's judge cannot be held to HEAD's"},
		{name: "outside a checkout", commit: headSHA, root: "-",
			want: "laug runs outside a checkout, so its judge cannot be held to HEAD's"},
	} {
		t.Run(c.name, func(t *testing.T) {
			commit = c.commit
			if c.commit == "" {
				savedInfo := readBuildInfo
				t.Cleanup(func() { readBuildInfo = savedInfo })
				readBuildInfo = func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{}, true }
			}
			root := r
			if c.root == "-" {
				root = ""
			}
			if c.edit {
				p := filepath.Join(r, "internal", "jsonx", "value.go")
				data := readText(t, p)
				writeFile(t, p, data+"\n")
				t.Cleanup(func() { writeFile(t, p, data) })
			}
			env := append(slices.Clone(c.env), "PATH="+bin, "STUB_HEAD="+headSHA)
			got := judgeNotHeld(context.Background(), &App{Root: root, Environ: env})
			if c.want == "" && got != "" || !strings.HasPrefix(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// testGateSignature: signature and compare in Go, on committed runs: the signature's fields under a first line,
// with -json on one line; compare's lines; and a run whose signature cannot be read, exit 2.
func testGateSignature(t *testing.T) {
	root := repoRoot(t)
	sig, err := rejudge.Signature(filepath.Join(root, "gate", "results", "g20261018-092540"), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args []string
		code int
		out  string
	}{
		{"text", []string{"signature", "g20261018-092540"}, 0, "laug · gate signature g20261018-092540: the fields S2 " +
			"compares, from verdict.json and timeline.jsonl\n" + string(jsonx.Marshal(sig))},
		{"-json", []string{"signature", "g20261018-092540", "-json"}, 0, string(jsonx.MarshalCompact(sig))},
		{"compare, equal", []string{"compare", "g20261018-092540", "gate/results/g20261018-092540"}, 0,
			"laug · S2, A g20261018-092540, B g20261018-092540: equal\n"},
		{"compare, a v1 run with itself", []string{"compare", runPath("g20261014-215905"),
			runPath("g20261014-215905")}, 0, "laug · S2, A g20261014-215905, B g20261014-215905: equal\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runApp(t, &App{Root: root}, append([]string{"gate"}, c.args...)...)
			if code != c.code || out != c.out || errOut != "" {
				t.Errorf("exit %d\n got:\n%s\nwant:\n%s\nstderr:\n%s", code, out, c.out, errOut)
			}
		})
	}
	code, out, _ := runApp(t, &App{Root: root}, "gate", "compare", "g20261018-092540", "g20261018-091444")
	if lines := strings.Split(out, "\n"); code != 1 || lines[0] != "laug · S2, A g20261018-092540, B g20261018-091444: "+
		"5 of 16 fields differ" || lines[1] != `verdict: A "BLOCK" B "ADVANCE"` {
		t.Errorf("compare, two cases: exit %d\n%s", code, out)
	}
	code, out, _ = runApp(t, &App{Root: root}, "gate", "compare", runPath("g20261014-215905"), "g20261018-091444")
	if code != 1 || !strings.Contains(out, "\njudge: A \"v1\" B \"v2\"\n") {
		t.Errorf("compare, two judges: exit %d\n%s", code, out)
	}
	dir := t.TempDir()
	copyRun(t, "g20261018-092540", filepath.Join(dir, "r"))
	must(t, os.Remove(filepath.Join(dir, "r", "timeline.jsonl")))
	for _, args := range [][]string{{"signature", filepath.Join(dir, "r")}, {"compare", "g20261018-092540",
		filepath.Join(dir, "r")}} {
		code, out, errOut := runApp(t, &App{Root: root}, append([]string{"gate"}, args...)...)
		if want := "laug · gate " + args[0] + ": no timeline.jsonl in (outside the repository)/r\n"; code != 2 ||
			out != "" || errOut != want {
			t.Errorf("%s, no timeline: exit %d\nstdout %q\nstderr %q, want %q", args[0], code, out, errOut, want)
		}
	}
}
