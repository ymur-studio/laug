package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// loadStrays are the knobs load maps that strayEnviron holds with another value.
var loadStrays = []string{"CELL_ADDRESS", "DURATION", "ITERATIONS", "MAX_CONCURRENT", "NAMESPACE", "OMES_DIR",
	"PROJECT", "PROM_URL", "RATE", "RUN_ID", "SERVER_TAG"}

// loadKnobs is what load/run-omes.sh gets for a cell, a project and a tag, beside the run's own knobs.
func loadKnobs(address, prom, project, tag string, more ...string) []string {
	return append([]string{"OMES_DIR=.upstream/omes", "CELL_ADDRESS=" + address, "PROM_URL=" + prom, "PROJECT=" + project,
		"NAMESPACE=default", "RUN_ID=20261022-120000", "SERVER_TAG=" + tag}, more...)
}

// TestLoad: the env matrix. load runs load/run-omes.sh as a child with the cell the Makefile chose: what was given, else the last
// lease's, else block 0's; the tag given, else the lease's for the leased cell, else the local project's history
// image; the length, rate and concurrency only when given. A stray copy of any knob it maps is named and dropped. It
// refuses before it runs anything on a value make's checks refused, both -duration and -iterations, and -address
// without -prom while a lease is kept. With neither -duration nor -iterations, bare among them, it starts nothing:
// it names the cell the load would go to and the line to type, exit 2.
func TestLoad(t *testing.T) {
	scenario := []string{scenarioDefault}
	runMatrix(t, []envCase{
		{name: "bare, no lease", args: []string{"load"}, code: 2,
			errOut: "laug · load: names no length: give it -duration, in whole h, m and s, or -iterations"},
		{name: "bare, a lease", args: []string{"load"}, lease: true, code: 2,
			errOut: "the load would go to 127.0.0.1:7243 (lease c0ffee01); for 5 minutes there:"},
		{name: "no length, a flag given", args: []string{"load", "-rate", "20", "-run-id", "r1"}, code: 2,
			errOut: "  laug load -duration 5m -rate 20 -run-id r1"},
		{name: "-iterations alone", args: []string{"load", "-iterations", "1000"}, how: script.Run,
			script: "load/run-omes.sh", argv: scenario,
			knobs:  loadKnobs("127.0.0.1:7233", "http://127.0.0.1:9090", "laug", "", "ITERATIONS=1000"),
			strays: loadStrays,
			out: "laug · load: workflow_with_single_noop_activity on 127.0.0.1:7233 (the local cell, port block 0), " +
				"1000 iterations, run 20261022-120000; Ctrl-C stops it"},
		{name: "the leased cell", args: []string{"load", "-duration", "5m", "-rate", "20"}, lease: true,
			how: script.Run, script: "load/run-omes.sh", argv: scenario,
			knobs: loadKnobs("127.0.0.1:7243", "http://127.0.0.1:9100", "laug-c0ffee01", "1.32.1", "DURATION=5m",
				"RATE=20"),
			strays: loadStrays,
			out: "laug · load: workflow_with_single_noop_activity on 127.0.0.1:7243 (lease c0ffee01), for 5m, run " +
				"20261022-120000; Ctrl-C stops it"},
		{name: "a local cell's tag", args: []string{"load", "-iterations", "500"}, docker: "laug temporalio/server:1.32.1",
			how: script.Run, script: "load/run-omes.sh", argv: scenario,
			knobs:  loadKnobs("127.0.0.1:7233", "http://127.0.0.1:9090", "laug", "1.32.1", "ITERATIONS=500"),
			strays: loadStrays},
		{name: "another cell beside a lease", lease: true, docker: "laug-c0ffee01 temporalio/server:1.31.0",
			args: []string{"load", "-address", "127.0.0.1:17233", "-prom", "http://127.0.0.1:19090", "-duration", "90s"},
			how:  script.Run, script: "load/run-omes.sh", argv: scenario,
			knobs:  loadKnobs("127.0.0.1:17233", "http://127.0.0.1:19090", "laug-c0ffee01", "1.31.0", "DURATION=90s"),
			strays: loadStrays},
		{name: "every flag", lease: true, args: []string{"load", "-iterations", "500", "-max-concurrent", "10",
			"-continue-on-failure", "-scenario", "throughput_stress", "-run-id", "r1", "-namespace", "ns1", "-project",
			"laug-x", "-server-tag", "1.32.0", "-address", "127.0.0.1:17233", "-prom", "http://127.0.0.1:19090"},
			how: script.Run, script: "load/run-omes.sh", argv: []string{"throughput_stress"},
			knobs: []string{"OMES_DIR=.upstream/omes", "CELL_ADDRESS=127.0.0.1:17233", "PROM_URL=http://127.0.0.1:19090",
				"PROJECT=laug-x", "NAMESPACE=ns1", "RUN_ID=r1", "SERVER_TAG=1.32.0", "ITERATIONS=500",
				"MAX_CONCURRENT=10", "CONTINUE_ON_FAILURE=1"},
			strays: loadStrays},
		{name: "-dry-run", args: []string{"load", "-duration", "5m", "-dry-run"}, strays: loadStrays,
			out: "DURATION=5m OMES_DIR='.upstream/omes' CELL_ADDRESS='127.0.0.1:7233' PROM_URL='http://127.0.0.1:9090' " +
				"PROJECT=laug NAMESPACE=default RUN_ID=20261022-120000 SERVER_TAG= load/run-omes.sh " + scenarioDefault},
		{name: "-duration and -iterations", args: []string{"load", "-duration", "5m", "-iterations", "5"}, code: 2,
			errOut: "laug · load: takes -duration or -iterations, not both: load/run-omes.sh runs -duration and drops " +
				"-iterations"},
		{name: "a length lw_dur_s cannot read", args: []string{"load", "-duration", "1.5h"}, code: 2,
			errOut: "laug · load: -duration 1.5h: a length in whole h, m and s, over 0: 5m, 90s, 1h30m"},
		{name: "-rate 0", args: []string{"load", "-rate", "0"}, code: 2,
			errOut: "laug · load: -rate 0: a whole number over 0"},
		{name: "a run id of two words", args: []string{"load", "-run-id", "a b"}, code: 2,
			errOut: "laug · load: -run-id 'a b': not one word of letters, digits, ., _ and -, from a letter or digit"},
		{name: "a tag that is no release", args: []string{"load", "-server-tag", "latest"}, code: 2,
			errOut: "laug · load: -server-tag latest: not a release tag, x.y.z"},
		{name: "-address without -prom, a lease kept", args: []string{"load", "-address", "127.0.0.1:17233"}, lease: true,
			code: 2, errOut: "laug · load: -address was given and -prom was not, so the run would record the leased " +
				"cell's Prometheus, http://127.0.0.1:9100; give -prom for 127.0.0.1:17233"},
		{name: "an argument", args: []string{"load", "5m"}, code: 2, errOut: `laug · load: takes no arguments, got ["5m"]`},
	})

	t.Run("the image is looked up in the project", func(t *testing.T) {
		c := envCase{docker: "laug-local1 temporalio/server:1.32.1"}
		a, rec := matrixApp(t, c)
		log := filepath.Join(t.TempDir(), "docker.args")
		a.Environ = append(a.Environ, "STUB_LOG="+log)
		code, _, errOut := runApp(t, a, "load", "-project", "laug-local1", "-iterations", "5")
		b, _ := os.ReadFile(log)
		want := "ps -q --filter label=com.docker.compose.project=laug-local1 --filter " +
			"label=com.docker.compose.service=temporal-history\ninspect -f {{.Config.Image}} 0123456789ab\n"
		if code != 0 || string(b) != want || !strings.Contains(strings.Join(rec.envs[0], "\n"), "SERVER_TAG=1.32.1") {
			t.Errorf("exit %d; docker ran:\n%s\nwant:\n%s\nstderr:\n%s", code, b, want, errOut)
		}
	})
	t.Run("bare, the whole message", func(t *testing.T) {
		a, rec := matrixApp(t, envCase{lease: true})
		code, out, errOut := runApp(t, a, "load")
		want := "laug · load: names no length: give it -duration, in whole h, m and s, or -iterations\n" +
			"the load would go to 127.0.0.1:7243 (lease c0ffee01); for 5 minutes there:\n" +
			"  laug load -duration 5m\nusage: laug load -duration <length> | -iterations <n> "
		if code != 2 || out != "" || len(rec.calls) != 0 || !strings.HasPrefix(errOut, want) {
			t.Errorf("exit %d, %d scripts\nstdout:\n%s\nstderr:\n%s", code, len(rec.calls), out, errOut)
		}
		a, _ = matrixApp(t, envCase{})
		if _, _, errOut := runApp(t, a, "load", "-scenario", "throughput_stress"); !strings.Contains(errOut,
			"\nthe load would go to 127.0.0.1:7233 (the local cell, port block 0: .last-lease.json holds no lease); "+
				"for 5 minutes there:\n  laug load -duration 5m -scenario throughput_stress\n") {
			t.Errorf("no lease:\n%s", errOut)
		}
	})
	// The script stays a child: its exit is load's, a tool's own and a signal's as they came, and while it runs a
	// line a minute goes to stderr, of -duration's seconds or toward -iterations.
	t.Run("a line a minute, and the script's exit", func(t *testing.T) {
		saved := loadTick
		loadTick = 20 * time.Millisecond
		t.Cleanup(func() { loadTick = saved })
		for _, c := range []struct {
			args   []string
			script int
			line   string
		}{
			{[]string{"-duration", "5m"}, 0, "laug · load: 0 s of 300 s; Ctrl-C stops it"},
			{[]string{"-iterations", "1000"}, 3, "laug · load: 0 s, toward 1000 iterations; Ctrl-C stops it"},
			{[]string{"-duration", "90s"}, 7, "laug · load: 0 s of 90 s; Ctrl-C stops it"},
			{[]string{"-duration", "90s"}, 130, "laug · load: 0 s of 90 s; Ctrl-C stops it"},
		} {
			a, rec := matrixApp(t, envCase{})
			rec.run = func(script.Command, []string, io.Writer, io.Writer) int {
				time.Sleep(110 * time.Millisecond)
				return c.script
			}
			code, out, errOut := runApp(t, a, append([]string{"load"}, c.args...)...)
			if n := strings.Count(errOut, c.line+"\n"); code != c.script || n < 2 || n > 6 ||
				!strings.HasPrefix(out, "laug · load: ") {
				t.Errorf("load %q, the script's %d: exit %d, %d lines a tick\nstdout:\n%s\nstderr:\n%s", c.args, c.script,
					code, n, out, errOut)
			}
		}
		for _, c := range []struct {
			gone       time.Duration
			duration   string
			iterations int
			want       string
		}{
			{time.Minute, "5m", 0, "laug · load: 60 s of 300 s; Ctrl-C stops it"},
			{5 * time.Minute, "5m", 0, "laug · load: 300 s of 300 s; Ctrl-C stops it"},
			{6 * time.Minute, "5m", 0, "laug · load: 360 s, past its 300 s: Omes waits up to 120 s for the workflows " +
				"in flight; Ctrl-C stops it"},
			{2 * time.Minute, "", 500, "laug · load: 120 s, toward 500 iterations; Ctrl-C stops it"},
		} {
			if got := loadSaid(c.gone, c.duration, c.iterations); got != c.want {
				t.Errorf("loadSaid(%s, %q, %d)\n got %q\nwant %q", c.gone, c.duration, c.iterations, got, c.want)
			}
		}
		// The 120 s is load/run-omes.sh's: Omes's --timeout is the duration and that much more.
		b, err := os.ReadFile(filepath.Join("..", "..", "load", "run-omes.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if want := `timeout="$(( $(lw_dur_s "$DURATION") + ` + strconv.Itoa(omesSlackS) + ` ))s"`; !strings.Contains(
			string(b), want+"\n") {
			t.Errorf("load/run-omes.sh lacks %s", want)
		}
	})
	// A SIGTERM sent to laug alone goes on to the script, and load exits with the script's own code; a SIGINT, which a
	// terminal sends the script as well, does not, and the script ends on its own.
	t.Run("a signal to laug alone", func(t *testing.T) {
		for _, c := range []struct {
			sig  os.Signal
			code int
			said string
		}{{syscall.SIGTERM, 9, "term\n"}, {os.Interrupt, 0, "ended\n"}} {
			a, _ := matrixApp(t, envCase{})
			writeExec(t, filepath.Join(a.Root, "load", "run-omes.sh"), "#!/bin/sh\ntrap 'echo term; exit 9' TERM\n"+
				": > ready\ni=0\nwhile [ ! -e go ]; do sleep 0.05; i=$((i + 1)); [ \"$i\" -lt 200 ] || exit 3; done\n"+
				"echo ended\n")
			var stdout, stderr syncBuffer
			a.Stdout, a.Stderr, a.Runner = &stdout, &stderr, script.Real
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			done := make(chan int, 1)
			go func() { done <- run(ctx, a, []string{"load", "-duration", "5m"}) }()
			for i := 0; !isFile(filepath.Join(a.Root, "ready")); i++ {
				if i == 200 {
					t.Fatalf("%v: the script never started\n%s%s", c.sig, stdout.String(), stderr.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel(script.Signalled{Signal: c.sig})
			time.Sleep(200 * time.Millisecond)
			writeFile(t, filepath.Join(a.Root, "go"), "")
			if code := <-done; code != c.code || !strings.HasSuffix(stdout.String(), "Ctrl-C stops it\n"+c.said) {
				t.Errorf("%v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.sig, code, c.code, stdout.String(),
					stderr.String())
			}
		}
	})
	t.Run("outside a checkout", func(t *testing.T) {
		code, out, errOut := runApp(t, &App{Runner: &recorder{}}, "load", "-duration", "5m")
		if code != 2 || out != "" || !strings.HasPrefix(errOut, "laug · load: runs load/run-omes.sh, so it needs a laug "+
			"checkout") {
			t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
}
