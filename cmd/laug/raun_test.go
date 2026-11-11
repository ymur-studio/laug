package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// leasedIDs is the ids of the cells the pool on addr holds leased.
func leasedIDs(t *testing.T, addr string) []string {
	t.Helper()
	c, err := poolapi.New(addr)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, cell := range st.Cells {
		if cell.State == "leased" {
			ids = append(ids, cell.ID)
		}
	}
	return ids
}

// poolRow is a row of the env matrix for a verb that leases: "$POOL" in its args and knobs is a fresh pool's address,
// internal/pool's API under httptest. It returns the pool's address and its request count.
func poolRow(t *testing.T, c envCase, a *App, rec *recorder) (string, *atomic.Int32, int, string, string) {
	t.Helper()
	addr, n := leasePool(t)
	for i := range c.args {
		c.args[i] = strings.ReplaceAll(c.args[i], "$POOL", addr)
	}
	for i := range c.knobs {
		c.knobs[i] = strings.ReplaceAll(c.knobs[i], "$POOL", addr)
	}
	c.out = strings.ReplaceAll(c.out, "$POOL", addr)
	c.errOut = strings.ReplaceAll(c.errOut, "$POOL", addr)
	code, out, errOut := runApp(t, a, c.args...)
	checkRow(t, c, rec, code, out, errOut)
	return addr, n, code, out, errOut
}

var (
	raunFirst = regexp.MustCompile(`^laug · raun: persistence-latency on lease ([0-9a-f]{8}) ` +
		`\(fake-([0-9a-f]{8}):[0-9]+\) until [0-9-]{10} [0-9:]{8} UTC, run raun-20261022-120000; its windows take ` +
		`330 s, plus up to 60 s to recover; Ctrl-C stops the fault, then the cell goes back$`)
	givenBack = regexp.MustCompile(`(?m)^laug · (raun|candidate): ([0-9a-f]{8}) given back, its cell destroyed; ` +
		`\.last-lease\.json removed(; the raun's JSON is (\S+))?$`)
)

// TestRaun: the env matrix, with a pool. raun checks every flag and the fault's name before it leases, so a typo
// leases nothing; leases a cell and keeps it in .last-lease.json, where faults/run.sh reads it; runs faults/run.sh as
// a child with RUN_ID raun-<run id>; then gives the cell back and removes the file, however the script ended, and
// exits with the script's code, or 1 when the cell could not go back. The given-back line ends with the raun JSON's
// path when the script wrote one; a harness failure here writes none, and a JSON from an earlier raun is not named.
func TestRaun(t *testing.T) {
	knobs := func(more ...string) []string {
		return append([]string{"RUN_ID=raun-20261022-120000", "POOL_ADDR=http://$POOL"}, more...)
	}
	for _, c := range []struct {
		envCase
		script   int  // faults/run.sh's exit
		requests int  // requests the pool takes; -1 for any
		back     bool // the cell went back and the file went
	}{
		{envCase{name: "ADVANCE", args: []string{"raun", "persistence-latency", "-pool", "$POOL"}, how: script.Run,
			script: "faults/run.sh", argv: []string{scenarioDefault, "persistence-latency"}, knobs: knobs(),
			strays: faultStrays}, 0, 2, true},
		{envCase{name: "BLOCK", args: []string{"raun", "persistence-latency", "-pool", "$POOL"}, code: 1,
			how: script.Run, script: "faults/run.sh", argv: []string{scenarioDefault, "persistence-latency"},
			knobs: knobs(), strays: faultStrays}, 1, 2, true},
		{envCase{name: "a harness failure", args: []string{"raun", "persistence-latency", "-pool", "$POOL"}, code: 2,
			how: script.Run, script: "faults/run.sh", argv: []string{scenarioDefault, "persistence-latency"},
			knobs: knobs(), strays: faultStrays}, 2, 2, true},
		// A code faults/run.sh does not name is a tool's own, passed up by set -e: laug names it and exits 2.
		{envCase{name: "a tool's own code", args: []string{"raun", "persistence-latency", "-pool", "$POOL"}, code: 2,
			how: script.Run, script: "faults/run.sh", argv: []string{scenarioDefault, "persistence-latency"},
			knobs: knobs(), strays: faultStrays, errOut: "laug · raun: a harness failure: faults/run.sh exited 7, the " +
				"code of a command it ran, which raun does not name; exit 2"}, 7, 2, true},
		{envCase{name: "every flag", args: []string{"raun", "kill-history", "-pool", "$POOL", "-ttl", "10m",
			"-requester", "lab", "-server-tag", "1.32.1", "-scenario", "throughput_stress", "-run-id", "r1",
			"-quiet-wait", "0s", "-thresholds", "load/steady-state.yaml", "-rate", "30", "-max-concurrent", "10",
			"-sweep-margin", "1m"}, how: script.Run, script: "faults/run.sh",
			argv: []string{"throughput_stress", "kill-history"},
			knobs: []string{"RUN_ID=raun-r1", "POOL_ADDR=http://$POOL", "POOL_QUIET_WAIT_S=0",
				"THRESHOLDS=load/steady-state.yaml", "RATE=30", "MAX_CONCURRENT=10", "SWEEP_MARGIN_S=60"},
			strays: faultStrays}, 0, 2, true},
		{envCase{name: "-dry-run", args: []string{"raun", "kill-history", "-pool", "$POOL", "-run-id", "r1",
			"-quiet-wait", "0s", "-dry-run"}, strays: faultStrays,
			out: "POOL_QUIET_WAIT_S=0 RUN_ID=raun-r1 POOL_ADDR='http://$POOL' faults/run.sh " + scenarioDefault +
				" kill-history"}, 0, 0, false},
		{envCase{name: "a typo leases nothing", args: []string{"raun", "persistence-latancy", "-pool", "$POOL"},
			code: 2}, 0, 0, false},
		{envCase{name: "-ttl 0", args: []string{"raun", "persistence-latency", "-pool", "$POOL", "-ttl", "0s"}, code: 2,
			errOut: "laug · raun: -ttl 0s: 0 or less"}, 0, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := matrixApp(t, c.envCase)
			path := filepath.Join(a.Root, ".last-lease.json")
			json := "faults/results/raun-20261022-120000.json"
			if c.name == "every flag" {
				json = "faults/results/raun-r1.json"
			}
			if c.name == "a harness failure" { // a JSON an earlier raun of the same id left
				writeFile(t, filepath.Join(a.Root, json), "{}\n")
				old := time.Now().Add(-time.Hour)
				os.Chtimes(filepath.Join(a.Root, json), old, old)
			}
			var during string
			rec.run = func(script.Command, []string, io.Writer, io.Writer) int {
				b, _ := os.ReadFile(path)
				during = string(b)
				if c.name != "a harness failure" {
					writeFile(t, filepath.Join(a.Root, json), "{}\n")
				}
				return c.script
			}
			addr, n, _, out, _ := poolRow(t, c.envCase, a, rec)
			if c.requests >= 0 && int(n.Load()) != c.requests {
				t.Errorf("%d requests reached the pool, want %d", n.Load(), c.requests)
			}
			if !c.back {
				return
			}
			m := raunFirst.FindStringSubmatch(firstLine(out))
			if c.name == "every flag" {
				m = []string{"", regexp.MustCompile(`on lease ([0-9a-f]{8})`).FindStringSubmatch(out)[1]}
			}
			if m == nil || !strings.Contains(during, `"id":"`+m[1]+`"`) {
				t.Fatalf("first line %q; the file during the script: %s", firstLine(out), during)
			}
			want := json
			if c.name == "a harness failure" {
				want = ""
			}
			if g := givenBack.FindStringSubmatch(out); g == nil || g[2] != m[1] || g[4] != want {
				t.Errorf("no line that gives %s back and names %q:\n%s", m[1], want, out)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) || len(leasedIDs(t, addr)) != 0 {
				t.Errorf("after the raun: the file %v, leased %q", err, leasedIDs(t, addr))
			}
		})
	}

	t.Run("the cell cannot go back", func(t *testing.T) {
		a, rec := matrixApp(t, envCase{})
		addr, _ := leasePool(t)
		var id string
		rec.run = func(script.Command, []string, io.Writer, io.Writer) int {
			l, _ := poolapi.ReadLast(filepath.Join(a.Root, ".last-lease.json"))
			id = l.ID
			c, _ := poolapi.New(addr)
			c.Release(context.Background(), id) // given back behind raun's back
			return 0
		}
		code, _, errOut := runApp(t, a, "raun", "persistence-latency", "-pool", addr)
		want := "laug · raun: the pool on " + addr + " refused " + id + ": cell not found (404 Not Found); " +
			"laug release " + id + " -pool " + addr + " gives it back"
		if code != 1 || !strings.Contains(errOut, want+"\n") {
			t.Errorf("exit %d, want 1\nstderr:\n%s\nwant a line:\n%s", code, errOut, want)
		}
	})
	t.Run("a refused lease runs nothing", func(t *testing.T) {
		a, rec := matrixApp(t, envCase{})
		addr, _ := leasePool(t)
		for range 2 {
			if code, _, errOut := runApp(t, &App{Root: t.TempDir()}, "lease", "-pool", addr); code != 0 {
				t.Fatal(errOut)
			}
		}
		code, _, errOut := runApp(t, a, "raun", "persistence-latency", "-pool", addr)
		if code != 1 || len(rec.calls) != 0 || !strings.Contains(errOut, "laug · raun: the pool on "+addr+
			" refused: quota: laug already holds 2 lease(s) (409 Conflict)\n") {
			t.Errorf("exit %d, %d scripts\nstderr:\n%s", code, len(rec.calls), errOut)
		}
	})
	t.Run("outside a checkout", func(t *testing.T) {
		code, _, errOut := runApp(t, &App{Runner: &recorder{}}, "raun", "persistence-latency")
		if code != 2 || !strings.HasPrefix(errOut, "laug · raun: runs faults/run.sh, so it needs a laug checkout") {
			t.Errorf("exit %d\nstderr:\n%s", code, errOut)
		}
	})

	// A signal ends the context while faults/run.sh runs: raun says what comes, waits for the script, which stopped on
	// the terminal's Ctrl-C too, gives the cell back and exits with the script's 130.
	t.Run("a signal while the fault runs", func(t *testing.T) {
		a, rec := matrixApp(t, envCase{})
		addr, _ := leasePool(t)
		stdout, stderr := &syncBuffer{}, &syncBuffer{}
		a.Stdout, a.Stderr = stdout, stderr
		started, proceed := make(chan struct{}), make(chan struct{})
		rec.run = func(script.Command, []string, io.Writer, io.Writer) int {
			close(started)
			<-proceed
			return 130
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan int)
		go func() { done <- run(ctx, a, []string{"raun", "persistence-latency", "-pool", addr}) }()
		<-started
		if ids := leasedIDs(t, addr); len(ids) != 1 {
			t.Fatalf("leased %q while the fault runs", ids)
		}
		cancel()
		waitUntil(t, "the stopping line", func() bool { return strings.Contains(stderr.String(), "laug · raun: stopping: ") })
		close(proceed)
		code := <-done
		if code != 130 || givenBack.FindString(stdout.String()) == "" || len(leasedIDs(t, addr)) != 0 {
			t.Errorf("exit %d, leased %q\nstdout:\n%s\nstderr:\n%s", code, leasedIDs(t, addr), stdout.String(),
				stderr.String())
		}
		if _, err := os.Stat(filepath.Join(a.Root, ".last-lease.json")); !os.IsNotExist(err) {
			t.Errorf("the file stays: %v", err)
		}
	})
	t.Run("a signal before the lease", func(t *testing.T) {
		a, rec := matrixApp(t, envCase{})
		addr, _ := leasePool(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr strings.Builder
		a.Stdout, a.Stderr = &stdout, &stderr
		code := run(ctx, a, []string{"raun", "persistence-latency", "-pool", addr})
		if code != 1 || len(rec.calls) != 0 || !strings.Contains(stderr.String(), "laug · raun: stopped before the "+
			"pool on "+addr+" answered; the pool gives back a cell it could not hand over\n") {
			t.Errorf("exit %d, %d scripts\nstderr:\n%s", code, len(rec.calls), stderr.String())
		}
		waitUntil(t, "no leased cell", func() bool { return len(leasedIDs(t, addr)) == 0 })
	})
}

// standIn is faults/run.sh for TestRaunSignals: it notes its start, then waits FAKE_TICKS tenths of a second, a seam
// that passes through, and notes the end, or notes the signal that ends it first.
const standIn = `#!/bin/sh
trap 'echo INT >> run.log; exit 130' INT
trap 'echo TERM >> run.log; exit 143' TERM
echo "started $RUN_ID" >> run.log
i=0
while [ "$i" -lt "$FAKE_TICKS" ]; do sleep 0.1; i=$((i + 1)); done
echo done >> run.log
`

// TestRaunSignals runs the built binary. Ctrl-C at a terminal reaches laug and faults/run.sh, its process group:
// the script stops, raun gives the cell back and exits 130. A TERM sent to laug alone does not reach the script, as
// internal/script's Run says: the fault runs to its end, then the cell goes back, exit 0, the script's.
func TestRaunSignals(t *testing.T) {
	bin := laugBinary(t)
	for _, c := range []struct {
		name  string
		sig   syscall.Signal
		group bool
		ticks string
		code  int
		log   string
	}{
		{"Ctrl-C reaches the group", syscall.SIGINT, true, "100", 130, "started raun-r1\nINT\n"},
		{"TERM to laug alone", syscall.SIGTERM, false, "10", 0, "started raun-r1\ndone\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := checkout(t)
			catalog, err := os.ReadFile(filepath.Join("..", "..", "faults", "catalog.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, "faults", "catalog.yaml"), string(catalog))
			writeFile(t, filepath.Join(root, "faults", "run.sh"), standIn)
			if err := os.Chmod(filepath.Join(root, "faults", "run.sh"), 0o755); err != nil {
				t.Fatal(err)
			}
			addr, _ := leasePool(t)
			cmd := exec.Command(filepath.Join(bin, "laug"), "raun", "persistence-latency", "-pool", addr, "-run-id", "r1")
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "FAKE_TICKS=" + c.ticks}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			stdout, stderr := &syncBuffer{}, &syncBuffer{}
			cmd.Stdout, cmd.Stderr = stdout, stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					cmd.Wait()
				}
			})
			logFile := filepath.Join(root, "run.log")
			waitUntil(t, "the script's start", func() bool {
				b, _ := os.ReadFile(logFile)
				return strings.HasPrefix(string(b), "started ")
			})
			if c.group {
				err = syscall.Kill(-cmd.Process.Pid, c.sig)
			} else {
				err = cmd.Process.Signal(c.sig)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			var code int
			select {
			case err := <-done:
				if code, err = script.ExitCode(err); err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("no exit within 10 s of %s\nstderr:\n%s", c.sig, stderr.String())
			}
			b, _ := os.ReadFile(logFile)
			// The line that says the cell goes back once the script ends comes while raun waits: a script that stops
			// on the terminal's Ctrl-C may end first, and then there is nothing to say.
			if code != c.code || string(b) != c.log || givenBack.FindString(stdout.String()) == "" ||
				!c.group && !strings.Contains(stderr.String(), "laug · raun: stopping: ") {
				t.Errorf("exit %d, want %d; the script's log %q, want %q\nstdout:\n%s\nstderr:\n%s", code, c.code, b,
					c.log, stdout.String(), stderr.String())
			}
			if ids := leasedIDs(t, addr); len(ids) != 0 {
				t.Errorf("leased after the raun: %q", ids)
			}
			if _, err := os.Stat(filepath.Join(root, ".last-lease.json")); !os.IsNotExist(err) {
				t.Errorf("the lease file stays: %v", err)
			}
		})
	}
}
