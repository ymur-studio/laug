package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// orchStrays is a stray copy of each knob a verb reaching the orchestrator maps, beyond strayEnviron's.
var orchStrays = []string{"ORCH_ADDRESS=127.0.0.1:1", "ORCH_HTTP_PORT=1", "ORCH_METRICS_PORT=1", "ORCH_UI_PORT=1",
	"ORCH_OTLP=127.0.0.1:1", "SECONDS=5", "UNPOLLED=1", "GATE=stray", "NAME=stray", "WF=stray", "REASON=stray",
	"TEMPORAL_NAMESPACE=stray"}

// ownStamp makes ownCommit, the commit the worker's identity names, s for the test.
func ownStamp(t *testing.T, s string) {
	t.Helper()
	saved := ownCommit
	t.Cleanup(func() { ownCommit = saved })
	ownCommit = func() string { return s }
}

// orchKnobs is ORCH_ENV's two names for the server at addr.
func orchKnobs(addr string) []string {
	return []string{"TEMPORAL_ADDRESS=" + addr, "TEMPORAL_NAMESPACE=default"}
}

// TestOrchestrator: the env matrix of the orchestrator verbs, then what each refuses before it runs, the worker's
// commit, laug's own, held to HEAD, the server's pid as ss names it, and what needs a checkout.
func TestOrchestrator(t *testing.T) {
	// The worker's commit is HEAD's, by workflowApp's stub git and ownStamp.
	ownStamp(t, headSHA)
	server := []string{"server", "start-dev", "--ip", "127.0.0.1", "--port", "7400", "--http-port", "7401",
		"--metrics-port", "7402", "--ui-port", "8400", "--db-filename", "orchestrator/run/dev.db"}
	serverStrays := []string{"ORCH_ADDRESS", "ORCH_HTTP_PORT", "ORCH_METRICS_PORT", "ORCH_UI_PORT"}
	fixtures := map[string]string{"internal/orchestrator/testdata/histories/advance.json": `{"events": [{}, {}]}`}
	cases := []envCase{
		{name: "server", args: []string{"orchestrator", "server"}, how: script.Exec, script: "temporal", tool: true,
			argv: server, strays: serverStrays,
			out: "laug · orchestrator server: the orchestrator's dev server on 127.0.0.1:7400, HTTP 7401, metrics 7402, UI 8400, " +
				"its state in orchestrator/run/dev.db; Ctrl-C stops it",
			errOut: "laug · orchestrator server: ORCH_ADDRESS=127.0.0.1:1 in the environment is not passed on; " +
				"-orchestrator sets it"},
		{name: "server, every flag", args: []string{"orchestrator", "server", "-orchestrator", "127.0.0.1:7500",
			"-http-port", "7501", "-metrics-port", "7502", "-ui-port", "8500"}, how: script.Exec, script: "temporal",
			tool: true, argv: []string{"server", "start-dev", "--ip", "127.0.0.1", "--port", "7500", "--http-port",
				"7501", "--metrics-port", "7502", "--ui-port", "8500", "--db-filename", "orchestrator/run/dev.db"},
			strays: serverStrays},
		{name: "server, off loopback", args: []string{"orchestrator", "server", "-orchestrator", "10.0.0.1:7400"},
			code: 2, errOut: "laug · orchestrator server: -orchestrator 10.0.0.1:7400: the dev server has no auth, so it " +
				"listens on 127.0.0.1 alone"},
		{name: "server, no port", args: []string{"orchestrator", "server", "-orchestrator", "127.0.0.1"}, code: 2,
			errOut: "laug · orchestrator server: -orchestrator 127.0.0.1: a host and a port, 127.0.0.1:7400 say, with " +
				"no quote or blank"},
		{name: "server, -ui-port 0", args: []string{"orchestrator", "server", "-ui-port", "0"}, code: 2,
			errOut: "laug · orchestrator server: -ui-port 0: a port, 1 to 65535"},
		{name: "worker", args: []string{"orchestrator", "worker"}, how: inProcess, script: "worker",
			argv: []string{"-repo", "$ROOT", "-commit", headSHA}, knobs: orchKnobs("127.0.0.1:7400"),
			strays: []string{"ORCH_ADDRESS", "ORCH_OTLP", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE"},
			out: "laug · orchestrator worker: this checkout's worker on 127.0.0.1:7400, its task queue named in " +
				"orchestrator/run/queue; it replays the open gates on the queue, then polls; Ctrl-C stops it",
			errOut: "laug · orchestrator worker: TEMPORAL_NAMESPACE=stray in the environment is not passed on; laug " +
				"sets it"},
		{name: "worker -otlp -orchestrator", args: []string{"orchestrator", "worker", "-otlp", "127.0.0.1:7751",
			"-orchestrator", "127.0.0.1:7500"}, how: inProcess, script: "worker",
			argv:  []string{"-repo", "$ROOT", "-commit", headSHA, "-otlp", "127.0.0.1:7751"},
			knobs: orchKnobs("127.0.0.1:7500"), strays: []string{"ORCH_ADDRESS", "ORCH_OTLP", "TEMPORAL_ADDRESS",
				"TEMPORAL_NAMESPACE"},
			out: "laug · orchestrator worker: this checkout's worker on 127.0.0.1:7500, its task queue named in " +
				"orchestrator/run/queue; it replays the open gates on the queue, then polls, workflow Gate traced to " +
				"127.0.0.1:7751; Ctrl-C stops it"},
		{name: "worker, -otlp off loopback", args: []string{"orchestrator", "worker", "-otlp", "10.0.0.1:7751"},
			code: 2, errOut: "laug · orchestrator worker: -otlp 10.0.0.1:7751: 127.0.0.1:<port>, the stack's Collector, " +
				"127.0.0.1:7751, or unset: it has no auth"},
		{name: "worker, a quote", args: []string{"orchestrator", "worker", "-orchestrator", "127.0.0.1:7'00"},
			code: 2},
		{name: "worker -dry-run", args: []string{"orchestrator", "worker", "-dry-run"}, code: 2,
			errOut: "laug · orchestrator worker: no flag -dry-run"},
		{name: "probe", args: []string{"orchestrator", "probe"}, how: inProcess, script: "probe",
			argv:   []string{"-repo", "$ROOT", "-seconds", "60", "-server-pid", "0", "-clk-tck", "100"},
			knobs:  orchKnobs("127.0.0.1:7400"),
			strays: []string{"ORCH_ADDRESS", "SECONDS", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE", "UNPOLLED"},
			out: "laug · orchestrator probe: this checkout's worker through 127.0.0.1:7400: host time against history " +
				"time, Idle 60 s heartbeating every 2 s, no server (ss names no process on port 7400) and the worker " +
				"sampled every 5 s; Ctrl-C cancels it"},
		{name: "probe -idle 0s -unpolled", args: []string{"orchestrator", "probe", "-idle", "0s", "-unpolled"},
			how: inProcess, script: "probe", argv: []string{"-repo", "$ROOT", "-seconds", "0",
				"-unpolled", "-server-pid", "0", "-clk-tck", "100"}, knobs: orchKnobs("127.0.0.1:7400"),
			strays: []string{"ORCH_ADDRESS", "SECONDS", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE", "UNPOLLED"}},
		{name: "probe -idle 1.5s", args: []string{"orchestrator", "probe", "-idle", "1.5s"}, code: 2,
			errOut: "laug · orchestrator probe: -idle 1.5s: whole seconds, 0 or more"},
		{name: "record, bare", args: []string{"orchestrator", "record"}, files: fixtures, code: 2,
			errOut: "  advance    20 bytes, 2 events"},
		{name: "record, no -overwrite-fixture", args: []string{"orchestrator", "record", "g20261016-120000",
			"advance"}, files: fixtures, code: 2,
			errOut: "laug · orchestrator record: would replace internal/orchestrator/testdata/histories/advance.json, " +
				"a committed replay fixture (20 bytes, 2 events), with workflow gate-g20261016-120000's history from " +
				"127.0.0.1:7400; -overwrite-fixture records it:"},
		{name: "record", args: []string{"orchestrator", "record", "g20261016-120000", "advance", "-overwrite-fixture",
			"-orchestrator", "127.0.0.1:7500"}, files: fixtures, how: inProcess, script: "record",
			argv: []string{"-repo", "$ROOT", "-gate", "g20261016-120000", "-out",
				"internal/orchestrator/testdata/histories/advance.json"},
			knobs: orchKnobs("127.0.0.1:7500"), strays: []string{"GATE", "ORCH_ADDRESS", "TEMPORAL_ADDRESS",
				"TEMPORAL_NAMESPACE"},
			out: "laug · orchestrator record: workflow gate-g20261016-120000's history from 127.0.0.1:7500, scrubbed " +
				"and checked for leaks, over internal/orchestrator/testdata/histories/advance.json (20 bytes, 2 " +
				"events); a leak writes nothing"},
		// LAUG_DENYLIST names a deny list: the role gets it as -denylist, as given.
		{name: "record, LAUG_DENYLIST", args: []string{"orchestrator", "record", "g20261016-120000", "advance",
			"-overwrite-fixture"}, files: fixtures, more: []string{"LAUG_DENYLIST=lists/words.txt"}, how: inProcess,
			script: "record", argv: []string{"-repo", "$ROOT", "-gate", "g20261016-120000", "-out",
				"internal/orchestrator/testdata/histories/advance.json", "-denylist", "lists/words.txt"},
			knobs: orchKnobs("127.0.0.1:7400"), strays: []string{"GATE", "ORCH_ADDRESS", "TEMPORAL_ADDRESS",
				"TEMPORAL_NAMESPACE"}},
		{name: "record, not a fixture", args: []string{"orchestrator", "record", "g20261016-120000", "other",
			"-overwrite-fixture"}, code: 2, errOut: "laug · orchestrator record: other: not a fixture's name: one of " +
			"advance, refused, harness, cancelled, adopted, real"},
		{name: "record, not a gate id", args: []string{"orchestrator", "record", "g1", "advance"}, code: 2},
		{name: "record, one argument", args: []string{"orchestrator", "record", "g20261016-120000"}, code: 2},
	}
	runWorkflowMatrix(t, orchStrays, nil, cases)

	t.Run("server makes its run dir", func(t *testing.T) {
		a, rec := workflowApp(t, envCase{})
		if code, _, errOut := runApp(t, a, "orchestrator", "server"); code != 0 || len(rec.calls) != 1 {
			t.Fatalf("exit %d, %d calls\n%s", code, len(rec.calls), errOut)
		}
		if fi, err := os.Stat(filepath.Join(a.Root, "orchestrator", "run")); err != nil || !fi.IsDir() {
			t.Errorf("orchestrator/run: %v", err)
		}
		// -dry-run makes nothing.
		a, _ = workflowApp(t, envCase{})
		if code, _, _ := runApp(t, a, "orchestrator", "server", "-dry-run"); code != 0 {
			t.Errorf("-dry-run: exit %d", code)
		}
		if _, err := os.Stat(filepath.Join(a.Root, "orchestrator")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("-dry-run made orchestrator/: %v", err)
		}
	})
	t.Run("worker not built from HEAD", func(t *testing.T) {
		for _, c := range []struct {
			stamp string
			want  string
		}{
			{otherSHA, "this laug was built from 1111111, and HEAD is 2222222: make build builds bin/laug from HEAD"},
			{otherSHA + "-dirty", "this laug was built from 1111111-dirty, and HEAD is 2222222: make build builds " +
				"bin/laug from HEAD"},
			{"dev", "this laug carries no commit, which the worker's identity names: make build stamps bin/laug with " +
				"HEAD's"},
			{"", "this laug carries no commit, which the worker's identity names: make build stamps bin/laug with " +
				"HEAD's"},
		} {
			ownStamp(t, c.stamp)
			a, rec := workflowApp(t, envCase{})
			code, _, errOut := runApp(t, a, "orchestrator", "worker")
			if code != 2 || len(rec.calls) != 0 || errOut != "laug · orchestrator worker: "+c.want+"\n" {
				t.Errorf("stamp %q: exit %d, %d calls\n%s", c.stamp, code, len(rec.calls), errOut)
			}
		}
		// A dirty build from HEAD runs: the worker's identity says -dirty, and the gate checks the tree.
		ownStamp(t, headSHA+"-dirty")
		a, rec := workflowApp(t, envCase{})
		if code, _, errOut := runApp(t, a, "orchestrator", "worker"); code != 0 || len(rec.calls) != 1 ||
			!slices.Equal(argvOf(rec.calls[0]), []string{"-repo", a.Root, "-commit", headSHA + "-dirty"}) {
			t.Errorf("a dirty build of HEAD: exit %d, %d calls\n%s", code, len(rec.calls), errOut)
		}
		// With no git, HEAD cannot be read, and the worker is refused.
		ownStamp(t, headSHA)
		a, _ = workflowApp(t, envCase{})
		a.Environ = append([]string{"PATH=" + t.TempDir()}, a.Environ...)
		if code, _, errOut := runApp(t, a, "orchestrator", "worker"); code != 2 || errOut != "laug · orchestrator "+
			"worker: no git on PATH to read HEAD with, so laug's commit, which the worker's identity names, cannot "+
			"be held to HEAD\n" {
			t.Errorf("no git: exit %d\n%s", code, errOut)
		}
	})
	t.Run("the server's pid", func(t *testing.T) {
		a, rec := workflowApp(t, envCase{})
		stubs := t.TempDir()
		writeExec(t, filepath.Join(stubs, "ss"), "#!/bin/sh\n[ \"$*\" = \"-ltnpH sport = :7500\" ] || exit 9\n"+
			"echo 'LISTEN 0 4096 127.0.0.1:7500 0.0.0.0:* users:((\"temporal\",pid=4242,fd=9),(\"x\",pid=7,fd=1))'\n")
		a.Environ = append([]string{"PATH=" + stubs + ":" + a.getenv("PATH")}, a.Environ...)
		code, out, errOut := runApp(t, a, "orchestrator", "probe", "-orchestrator", "127.0.0.1:7500")
		if code != 0 || len(rec.calls) != 1 || !slices.Contains(argvOf(rec.calls[0]), "4242") ||
			!strings.Contains(out, "the server, pid 4242, and the worker") {
			t.Errorf("exit %d, argv %q\n%s%s", code, argvOf(rec.calls[0]), out, errOut)
		}
		// No getconf answer, no ticks: refused, as the probe would refuse an empty -clk-tck.
		a, rec = workflowApp(t, envCase{})
		writeExec(t, filepath.Join(stubs, "getconf"), "#!/bin/sh\nexit 1\n")
		a.Environ = append([]string{"PATH=" + stubs + ":" + a.getenv("PATH")}, a.Environ...)
		if code, _, errOut := runApp(t, a, "orchestrator", "probe"); code != 2 || len(rec.calls) != 0 ||
			!strings.HasPrefix(errOut, `laug · orchestrator probe: getconf CLK_TCK printed "" (exit status 1), not `+
				"ticks a second") {
			t.Errorf("getconf fails: exit %d\n%s", code, errOut)
		}
	})
	// A role's exit is the verb's.
	t.Run("the role's exit", func(t *testing.T) {
		for _, args := range [][]string{{"orchestrator", "worker"}, {"orchestrator", "probe"},
			{"orchestrator", "record", "g20261016-120000", "real", "-overwrite-fixture"}} {
			a, rec := workflowApp(t, envCase{})
			rec.role = func(string, []string, []string, io.Writer, io.Writer) int { return 2 }
			if code, _, errOut := runApp(t, a, args...); code != 2 || len(rec.calls) != 1 {
				t.Errorf("%q: exit %d, %d calls\n%s", args, code, len(rec.calls), errOut)
			}
		}
	})
	t.Run("outside a checkout", func(t *testing.T) {
		for _, args := range [][]string{{"orchestrator", "server"}, {"orchestrator", "worker"},
			{"orchestrator", "probe"}, {"orchestrator", "record", "g20261016-120000", "real", "-overwrite-fixture"}} {
			rec := &recorder{}
			code, _, errOut := runApp(t, &App{Runner: rec, Roles: rec}, args...)
			if code != 2 || len(rec.calls) != 0 || !strings.Contains(errOut, "so it needs a laug checkout") {
				t.Errorf("%q: exit %d, %d calls\n%s", args, code, len(rec.calls), errOut)
			}
		}
	})
}

// argvOf is the arguments a command was handed, as text.
func argvOf(c script.Command) []string {
	var argv []string
	for _, v := range c.Args {
		argv = append(argv, v.Text)
	}
	return argv
}

// TestOrchestratorMatchesKnobs holds the orchestrator's defaults to the ones the Makefile laug replaced held, as
// internal/script's knob table keeps them: ORCH_ADDRESS, the three ports and SECONDS, the probe's idle. ORCH_CLI is
// internal/script's OrchCLI, held to orchestrator.CLIFlags by internal/orchestrator's TestCLIFlagsAreORCHCLI; and make
// test-go unsets, before go test, what the goldens' ORCH_NOFLAGS unset before an orchestrator binary.
func TestOrchestratorMatchesKnobs(t *testing.T) {
	for name, want := range map[string]string{"ORCH_ADDRESS": orchAddressDefault,
		"ORCH_HTTP_PORT": strconv.Itoa(orchHTTPPortDefault), "ORCH_METRICS_PORT": strconv.Itoa(orchMetricsPortDefault),
		"ORCH_UI_PORT": strconv.Itoa(orchUIPortDefault), "SECONDS": strconv.Itoa(probeIdleDefault),
		"TEMPORAL_NAMESPACE": orchNamespace} {
		if k, ok := script.Lookup(name); !ok || k.Default != want {
			t.Errorf("knob %s: default %q, laug %q", name, k.Default, want)
		}
	}
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ORCH_NOFLAGS += (.*)$`).FindSubmatch(b)
	if m == nil || strings.ReplaceAll(string(m[1]), "$$", "$") != orchNoFlags {
		t.Errorf("the Makefile's ORCH_NOFLAGS, $$ as $:\n%s\nthe goldens drop:\n%s", m, orchNoFlags)
	}
}
