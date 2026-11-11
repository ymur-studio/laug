package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// healthStrays is a stray copy of each knob the health verbs map beyond strayEnviron's.
var healthStrays = []string{"TIMER=9s", "HISTORY=stray.json", "REPLAY_HISTORY=stray.json"}

// workflowApp is a matrix row's App for the health and orchestrator verbs: the checkout holds the files make built for
// their targets, executable, and PATH starts with an ss that names no listener, a getconf that prints 100, so no row
// reads this host's, and the stub git, whose HEAD is headSHA.
func workflowApp(t *testing.T, c envCase, bins ...string) (*App, *recorder) {
	t.Helper()
	a, rec := matrixApp(t, c)
	for _, b := range bins {
		writeExec(t, filepath.Join(a.Root, b), "#!/bin/sh\n")
	}
	stubs := t.TempDir()
	writeExec(t, filepath.Join(stubs, "ss"), "#!/bin/sh\nexit 0\n")
	writeExec(t, filepath.Join(stubs, "getconf"), "#!/bin/sh\n[ \"$1\" = CLK_TCK ] && echo 100\n")
	writeExec(t, filepath.Join(stubs, "git"), gitStub)
	a.Environ = append([]string{"PATH=" + stubs + ":" + a.getenv("PATH"), "STUB_HEAD=" + headSHA}, a.Environ...)
	return a, rec
}

// runWorkflowMatrix runs each row through run on workflowApp, with more strays, and holds it to what it says.
func runWorkflowMatrix(t *testing.T, strays []string, bins []string, cases []envCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.more = append(slices.Clone(strays), c.more...)
			a, rec := workflowApp(t, c, bins...)
			code, out, errOut := runApp(t, a, c.args...)
			checkRow(t, c, rec, code, out, errOut)
		})
	}
}

// TestHealth: the env matrix of the health verbs, record's second step, replay's filter, a file to replay, what runs
// outside a checkout, replay through the built binary with a stub go first on PATH, and health check's refusals
// through the built binary, as the cell scripts run it.
func TestHealth(t *testing.T) {
	hc := func(more ...string) []string {
		return append([]string{"-address", "127.0.0.1:7233", "-namespace", "default"}, more...)
	}
	cellStray := []string{"CELL_ADDRESS", "NAMESPACE"}
	fixture := map[string]string{"internal/health/testdata/healthcheck_history.json": `{"events": [{}, {}, {}]}`}
	replayArgv := []string{"test", "./...", "-run", "Replay", "-v", "-count=1"}
	runWorkflowMatrix(t, healthStrays, nil, []envCase{
		{name: "check", args: []string{"health", "check"}, how: inProcess, script: "healthcheck",
			argv: hc("-metrics", ""), strays: cellStray,
			out: "laug · health check: one HealthCheck workflow on 127.0.0.1:7233, namespace default, its round trip " +
				"and visibility, its worker in this process",
			errOut: "laug · health check: CELL_ADDRESS=127.0.0.1:1 in the environment is not passed on; -address sets it"},
		{name: "check, a lease is no matter", args: []string{"health", "check"}, lease: true, how: inProcess,
			script: "healthcheck", argv: hc("-metrics", ""), strays: cellStray},
		{name: "check -address -namespace", args: []string{"health", "check", "-address", "127.0.0.1:7243",
			"-namespace", "ns1"}, how: inProcess, script: "healthcheck",
			argv: []string{"-address", "127.0.0.1:7243", "-namespace", "ns1", "-metrics", ""}, strays: cellStray},
		{name: "check, -namespace two words", args: []string{"health", "check", "-namespace", "a b"}, code: 2,
			errOut: "laug · health check: -namespace 'a b': not one word of letters, digits, ., _ and -, from a letter " +
				"or digit"},
		{name: "check, an argument", args: []string{"health", "check", "x"}, code: 2,
			errOut: `laug · health check: takes no arguments, got ["x"]; -address names the cell`},
		{name: "check -dry-run", args: []string{"health", "check", "-dry-run"}, code: 2,
			errOut: "laug · health check: no flag -dry-run"},
		{name: "check -visibility-wait", args: []string{"health", "check", "-address", "127.0.0.1:7263",
			"-visibility-wait", "10s"}, how: inProcess, script: "healthcheck", argv: []string{"-address",
			"127.0.0.1:7263", "-namespace", "default", "-metrics", "", "-visibility-wait", "10s"}, strays: cellStray},
		{name: "check -visibility-wait 0s", args: []string{"health", "check", "-visibility-wait", "0s"}, code: 2,
			errOut: "laug · health check: -visibility-wait 0s: a Go duration over 0, 10s say"},
		{name: "check -visibility-wait ten", args: []string{"health", "check", "-visibility-wait", "ten"}, code: 2},
		{name: "loop", args: []string{"health", "loop"}, how: inProcess, script: "healthcheck",
			argv: hc("-loop", "10s"), strays: cellStray,
			out: "laug · health loop: HealthCheck every 10s on 127.0.0.1:7233, namespace default, its SDK metrics on " +
				":9091 for the cell's sdk-workers target; Ctrl-C stops it"},
		{name: "loop -every", args: []string{"health", "loop", "-every", "1m30s"}, how: inProcess,
			script: "healthcheck", argv: hc("-loop", "1m30s"), strays: cellStray},
		{name: "loop -every 0s", args: []string{"health", "loop", "-every", "0s"}, code: 2,
			errOut: "laug · health loop: -every 0s: a Go duration over 0, 10s say"},
		{name: "loop -every ten", args: []string{"health", "loop", "-every", "ten"}, code: 2},
		{name: "record, bare", args: []string{"health", "record"}, files: fixture, code: 2,
			errOut: "laug · health record: would replace internal/health/testdata/healthcheck_history.json, the " +
				"committed replay fixture (24 bytes, 3 events), with the history of one HealthCheck round trip on " +
				"127.0.0.1:7233; -overwrite-fixture records it, then replays it:"},
		{name: "record, bare, with flags", args: []string{"health", "record", "-address", "127.0.0.1:7243",
			"-namespace", "ns1"}, code: 2, errOut: "  laug health record -overwrite-fixture -address 127.0.0.1:7243 " +
			"-namespace ns1"},
		{name: "record", args: []string{"health", "record", "-overwrite-fixture"}, files: fixture, how: inProcess,
			script: "healthcheck",
			argv:   hc("-metrics", "", "-export", "$ROOT/internal/health/testdata/healthcheck_history.json"),
			strays: []string{"HISTORY", "REPLAY_HISTORY", "CELL_ADDRESS", "NAMESPACE"},
			out: "laug · health record: one HealthCheck round trip on 127.0.0.1:7233, namespace default, its history " +
				"over internal/health/testdata/healthcheck_history.json (24 bytes, 3 events), then the replay tests " +
				"on it"},
		{name: "timer", args: []string{"health", "timer"}, how: inProcess, script: "timerprobe",
			argv:   []string{"-address", "127.0.0.1:7233", "-namespace", "default", "-timer", "60s"},
			strays: []string{"CELL_ADDRESS", "NAMESPACE", "TIMER"},
			out: "laug · health timer: one 60s durable timer on 127.0.0.1:7233 (port block 0), namespace default; it " +
				"prints when the timer was due and when it fired, in this host's time; Ctrl-C stops it",
			errOut: "laug · health timer: TIMER=9s in the environment is not passed on; health timer <duration> sets it"},
		{name: "timer, the lease", args: []string{"health", "timer"}, lease: true, how: inProcess,
			script: "timerprobe", argv: []string{"-address", "127.0.0.1:7243", "-namespace", "default",
				"-timer", "60s"}, strays: []string{"CELL_ADDRESS", "NAMESPACE", "TIMER"},
			out: "laug · health timer: one 60s durable timer on 127.0.0.1:7243 (lease c0ffee01), namespace default; " +
				"it prints when the timer was due and when it fired, in this host's time; Ctrl-C stops it"},
		{name: "timer, every flag", args: []string{"health", "timer", "90s", "-address", "127.0.0.1:7253",
			"-namespace", "ns1", "-id", "t1"}, lease: true, how: inProcess, script: "timerprobe",
			argv:   []string{"-address", "127.0.0.1:7253", "-namespace", "ns1", "-timer", "90s", "-id", "t1"},
			strays: []string{"CELL_ADDRESS", "NAMESPACE", "TIMER"}},
		{name: "timer 0s", args: []string{"health", "timer", "0s"}, code: 2,
			errOut: "laug · health timer: 0s: the timer, a Go duration over 0, 60s say"},
		{name: "timer, two", args: []string{"health", "timer", "1m", "2m"}, code: 2},
		{name: "timer -id two words", args: []string{"health", "timer", "-id", "a b"}, code: 2},
		{name: "replay", args: []string{"health", "replay"}, how: script.Run, script: "go", tool: true,
			dir: "internal/health", argv: replayArgv, strays: []string{"HISTORY", "REPLAY_HISTORY"},
			out: "laug · health replay: the replay tests in internal/health on the committed fixture, " +
				"internal/health/testdata/healthcheck_history.json, by go test; its results and why a build failed, " +
				"the rest left out",
			errOut: "laug · health replay: HISTORY=stray.json in the environment is not passed on; health replay " +
				"<file> sets it"},
		{name: "replay <file>", args: []string{"health", "replay", "h.json"}, files: map[string]string{"h.json": "{}"},
			how: script.Run, script: "go", tool: true, dir: "internal/health", argv: replayArgv,
			knobs: []string{"REPLAY_HISTORY=$ROOT/h.json"}, strays: []string{"HISTORY", "REPLAY_HISTORY"}},
		{name: "replay, an empty file", args: []string{"health", "replay", "h.json"},
			files: map[string]string{"h.json": ""}, code: 2,
			errOut: "laug · health replay: h.json is not a file with a history in it"},
		{name: "replay, a dir", args: []string{"health", "replay", "internal"}, code: 2},
		{name: "replay, two", args: []string{"health", "replay", "a.json", "b.json"}, code: 2},
	})

	t.Run("record replays after the round trip", func(t *testing.T) {
		a, rec := workflowApp(t, envCase{})
		code, _, errOut := runApp(t, a, "health", "record", "-overwrite-fixture")
		if code != 0 || len(rec.calls) != 2 || rec.hows[0] != inProcess || rec.hows[1] != script.Run ||
			rec.calls[1].Script != "go" || rec.calls[1].Args[0].Text != "test" || rec.calls[1].Dir != "internal/health" {
			t.Fatalf("exit %d, %d calls\n%s", code, len(rec.calls), errOut)
		}
		// A round trip that fails replays nothing, and its exit passes through.
		a, rec = workflowApp(t, envCase{})
		rec.role = func(string, []string, []string, io.Writer, io.Writer) int { return 1 }
		if code, _, _ := runApp(t, a, "health", "record", "-overwrite-fixture"); code != 1 || len(rec.calls) != 1 {
			t.Errorf("a failed round trip: exit %d, %d calls; want 1 and 1", code, len(rec.calls))
		}
	})
	t.Run("replay's filter", func(t *testing.T) {
		a, rec := workflowApp(t, envCase{})
		rec.run = func(_ script.Command, _ []string, stdout, stderr io.Writer) int {
			fmt.Fprint(stdout, "=== RUN   TestReplayHealthCheck\n")
			fmt.Fprint(stderr, "2026/10/22 12:00:00 INFO  a log line\n")
			fmt.Fprint(stdout, "    replay_test.go:36: replaying h.json\n--- PASS: TestReplayHealthCheck (0.01s)\n")
			fmt.Fprint(stderr, "# github.com/x/health\n./replay_test.go:9:2: undefined: y\n")
			fmt.Fprint(stdout, "PASS\nok  \tgithub.com/x/health\t0.02s")
			return 3
		}
		code, out, _ := runApp(t, a, "health", "replay")
		want := "    replay_test.go:36: replaying h.json\n--- PASS: TestReplayHealthCheck (0.01s)\n" +
			"# github.com/x/health\n./replay_test.go:9:2: undefined: y\nok  \tgithub.com/x/health\t0.02s\n"
		if _, body, _ := strings.Cut(out, "\n"); code != 3 || body != want {
			t.Errorf("exit %d, want 3, go test's\nstdout:\n%s\nwant after the first line:\n%s", code, out, want)
		}
	})
	// check, loop and timer run in laug's own process and need no checkout; record and replay run go test in one.
	t.Run("outside a checkout", func(t *testing.T) {
		for _, args := range [][]string{{"health", "check"}, {"health", "loop"}, {"health", "timer"}} {
			rec := &recorder{}
			code, _, errOut := runApp(t, &App{Runner: rec, Roles: rec}, args...)
			if code != 0 || len(rec.calls) != 1 || rec.hows[0] != inProcess {
				t.Errorf("%q: exit %d, %d calls\n%s", args, code, len(rec.calls), errOut)
			}
		}
		for _, args := range [][]string{{"health", "record", "-overwrite-fixture"}, {"health", "replay"}} {
			rec := &recorder{}
			code, _, errOut := runApp(t, &App{Runner: rec, Roles: rec}, args...)
			if code != 2 || len(rec.calls) != 0 || !strings.Contains(errOut, "runs go test in internal/health, so it "+
				"needs a laug checkout") {
				t.Errorf("%q: exit %d, %d calls\n%s", args, code, len(rec.calls), errOut)
			}
		}
	})
	t.Run("no roles", func(t *testing.T) {
		a, rec := workflowApp(t, envCase{})
		a.Roles = nil
		if code, out, errOut := runApp(t, a, "health", "check"); code != 1 || len(rec.calls) != 0 || out != "" ||
			!strings.HasSuffix(errOut, "laug · health check: no SDK roles, so healthcheck did not run\n") {
			t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
	t.Run("replay, the built binary and a stub go", func(t *testing.T) {
		laug := filepath.Join(laugBinary(t), "laug")
		stubs := t.TempDir()
		writeExec(t, filepath.Join(stubs, "go"), "#!/bin/sh\necho \"=== RUN   $*\"\necho \"--- FAIL: in $(pwd -P)\"\n"+
			"echo \"REPLAY_HISTORY=$REPLAY_HISTORY\" >&2\necho 'FAIL' >&2\nexit 1\n")
		root := repoRoot(t)
		h := filepath.Join(t.TempDir(), "h.json")
		writeFile(t, h, "{}")
		cmd := exec.Command(laug, "health", "replay", h)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + stubs + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "REPLAY_HISTORY=stray"}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code, _ := script.ExitCode(cmd.Run())
		health, _ := filepath.EvalSymlinks(filepath.Join(root, "internal", "health"))
		want := "--- FAIL: in " + health + "\nFAIL\n"
		if _, body, _ := strings.Cut(stdout.String(), "\n"); code != 1 || body != want ||
			!strings.Contains(stderr.String(), "REPLAY_HISTORY=stray in the environment is not passed on") {
			t.Errorf("exit %d, want 1\nstdout:\n%s\nwant after the first line:\n%s\nstderr:\n%s", code,
				stdout.String(), want, stderr.String())
		}
	})
	// The built binary by its own name, as the cell scripts run bin/laug health check: -h prints the flags, -metrics,
	// the healthcheck binary's flag, is not health check's, and an argument or a bad wait is refused; none dials a cell.
	t.Run("health check, the built binary", func(t *testing.T) {
		laug := filepath.Join(laugBinary(t), "laug")
		for _, c := range []struct {
			args         []string
			code         int
			stdout, line string
		}{
			{[]string{"-h"}, 0, "  -visibility-wait duration", ""},
			{[]string{"x"}, 2, "", `laug · health check: takes no arguments, got ["x"]`},
			{[]string{"-metrics", ""}, 2, "", "laug · health check: no flag -metrics"},
			{[]string{"-visibility-wait", "-1s"}, 2, "", "laug · health check: -visibility-wait -1s: a Go duration " +
				"over 0, 10s say"},
		} {
			cmd := exec.Command(laug, append([]string{"health", "check"}, c.args...)...)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code, _ := script.ExitCode(cmd.Run())
			if code != c.code || !strings.Contains(stdout.String(), c.stdout) ||
				!strings.Contains(stderr.String(), c.line) || (c.code == 2) != (stdout.Len() == 0) {
				t.Errorf("health check %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.args, code, c.code,
					stdout.String(), stderr.String())
			}
		}
	})
}

// TestLineFilter: whole lines that match, in order, newline and all, however the writes split them; a last line with
// no newline gets one at Flush, when it matches.
func TestLineFilter(t *testing.T) {
	var b bytes.Buffer
	l := &lineFilter{w: &b, keep: regexp.MustCompile(`^ok`)}
	for _, p := range []string{"o", "k 1\nno\nok", " 2\n", "nope\nok 3"} {
		if n, err := l.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	l.Flush()
	if b.String() != "ok 1\nok 2\nok 3\n" {
		t.Errorf("filtered %q", b.String())
	}
}

// TestFixtureHolds: bytes and events, or that the file is not there or holds no history.
func TestFixtureHolds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "h.json"), `{"events": [{"eventId": "1"}]}`)
	writeFile(t, filepath.Join(dir, "x.json"), `[]`)
	for name, want := range map[string]string{"h.json": "30 bytes, 1 event", "x.json": "2 bytes, not a history",
		"none.json": "not there"} {
		if got := fixtureHolds(filepath.Join(dir, name)); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// TestHealthCheckCallers: the scripts that ran the healthcheck binary, cells/health.sh, the compose backend's health
// verb, the Firecracker backend's and fc-lib.sh's warm_timeline, run bin/laug health check, each line as the script
// holds it, and health check takes each line's flags, a made-up value for each shell variable: the role gets the cell,
// the namespace, its metrics off, and the line's visibility wait when it gives one. cells/health.sh runs it with
// neither CELL_ADDRESS nor NAMESPACE in its environment, so health check names no stray copy.
func TestHealthCheckCallers(t *testing.T) {
	sh, err := os.ReadFile(filepath.Join(repoRoot(t), "cells", "health.sh"))
	if err != nil || !bytes.Contains(sh, []byte(`hc="${LAUG_ROOT:+$LAUG_ROOT/}bin/laug"`)) {
		t.Errorf("cells/health.sh no longer names bin/laug as hc: %v", err)
	}
	role := func(address, namespace string, more ...string) []string {
		return append([]string{"-address", address, "-namespace", namespace, "-metrics", ""}, more...)
	}
	for _, c := range []struct {
		file, line string
		args, argv []string
	}{
		{"cells/health.sh", `env -u CELL_ADDRESS -u NAMESPACE "$hc" health check -address "$CELL_ADDRESS" ` +
			`-namespace "$NAMESPACE" || exit`, []string{"-address", "127.0.0.1:7243", "-namespace", "ns1"},
			role("127.0.0.1:7243", "ns1")},
		{"cells/backend-compose.sh", `"$here/../bin/laug" health check -address "127.0.0.1:$frontend_port" ` +
			`-visibility-wait 10s >/dev/null 2>&1`, []string{"-address", "127.0.0.1:7263", "-visibility-wait", "10s"},
			role("127.0.0.1:7263", "default", "-visibility-wait", "10s")},
		{"cells/backend-firecracker.sh", `timeout 60 "$LAUG_ROOT/bin/laug" health check -address "$c:7233" ` +
			`-visibility-wait 10s >/dev/null`, []string{"-address", "cell-b3:7233", "-visibility-wait", "10s"},
			role("cell-b3:7233", "default", "-visibility-wait", "10s")},
		{"cells/rootfs/fc-lib.sh", `r="$(timeout 60 "$LAUG_ROOT/bin/laug" health check -address $a 2>&1 | tail -1)"`,
			[]string{"-address", "guest:7233"}, role("guest:7233", "default")},
	} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), c.file))
		if err != nil || !bytes.Contains(b, []byte(c.line)) {
			t.Errorf("%s no longer holds its health check line %s: %v", c.file, c.line, err)
		}
		rec := &recorder{}
		code, _, errOut := runApp(t, &App{Runner: rec, Roles: rec}, append([]string{"health", "check"}, c.args...)...)
		if code != 0 || len(rec.calls) != 1 || rec.calls[0].Script != "healthcheck" ||
			!slices.Equal(argTexts(rec.calls[0]), c.argv) || errOut != "" {
			t.Errorf("%s's health check %q: exit %d, %d calls %v, want the role with %q\nstderr:\n%s", c.file, c.args,
				code, len(rec.calls), rec.calls, c.argv, errOut)
		}
	}
}

// argTexts is c's arguments as text.
func argTexts(c script.Command) []string {
	var out []string
	for _, a := range c.Args {
		out = append(out, a.Text)
	}
	return out
}
