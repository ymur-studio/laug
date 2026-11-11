package main

// The usage verbs over _obs/cellstat. record and sample read cellstat's testdata/dev tree through usageRoot and
// list containers through a stub docker on the App's PATH, whose answers are cellstat's fake-docker.sh's; stop stops
// real processes, the built binary as usage record, one with a docker that stalls. No test runs Docker or reads the
// process's PATH for a docker.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/_obs/cellstat"
	"github.com/ymur-studio/laug/internal/script"
)

// usageIDA is container A of cellstat's dev tree, whose group /proc/101/cgroup names.
var usageIDA = strings.Repeat("0123456789abcdef", 4)

// usageTree points usageRoot at cellstat's dev tree for the test, and nameCellstat at a counter it returns.
func usageTree(t *testing.T) *int {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "cellstat", "testdata", "dev"))
	if err != nil {
		t.Fatal(err)
	}
	savedRoot, savedName := usageRoot, nameCellstat
	t.Cleanup(func() { usageRoot, nameCellstat = savedRoot, savedName })
	named := 0
	usageRoot, nameCellstat = root, func() error { named++; return nil }
	return &named
}

// stubDocker is an environment whose PATH holds one docker: with list, cellstat's fake-docker.sh answering ps with
// container A and inspect with A as compose project laug's temporal-history, pid 101; without, a docker that lists
// nothing and needs no other tool, for a child whose PATH is this alone. The fake's dir is returned too.
func stubDocker(t *testing.T, list bool) ([]string, string) {
	t.Helper()
	dir := t.TempDir()
	answers := filepath.Join(dir, "answers")
	body := "#!/bin/sh\nexit 0\n"
	if list {
		fake, err := filepath.Abs(filepath.Join("..", "..", "cellstat", "testdata", "fake-docker.sh"))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(answers, "ps.out"), usageIDA+"\n")
		writeFile(t, filepath.Join(answers, "inspect.out"), `[{"Id":"`+usageIDA+`","State":{"Pid":101,"Running":true},`+
			`"Config":{"Labels":{"com.docker.compose.project":"laug","com.docker.compose.service":"temporal-history"}}}]`+"\n")
		body = "#!/bin/sh\nFAKE_DOCKER='" + answers + "' exec '" + fake + "' \"$@\"\n"
	}
	writeExec(t, filepath.Join(dir, "docker"), body)
	return []string{"PATH=" + dir}, answers
}

// usageSampleText is sample's text over the dev tree with container A listed: A's cpu.stat usage_usec 3433220485,
// memory.stat anon 16539648 and file 41648128; the Omes client, 4 threads, and worker, 2; /proc/stat's 24 cpuN lines.
const usageSampleText = `laug · usage sample: 1 laug container (1 ok), 0 of other projects, 2 Omes processes, host 24 vCPU
project  service           id            state  cpu_s     anon_MiB  file_MiB  why
laug     temporal-history  0123456789ab  ok     3433.220  15.8      39.7      -
role         run_id               pid   threads
omes-client  g20261015-144517-s1  4241  4
omes-worker  g20261015-144517-s1  4242  2
`

// TestUsage holds the verbs' refusals, which act on nothing and name no process cellstat, and sample, which reads one
// line and writes nothing.
func TestUsage(t *testing.T) {
	named := usageTree(t)
	env, answers := stubDocker(t, true)
	root := checkout(t)
	file := filepath.Join(t.TempDir(), "a-file")
	writeFile(t, file, "x\n")
	for _, c := range []struct {
		name string
		args []string
		code int
		err  string // stderr's first line
	}{
		{"record, -listen any host", []string{"record", "-listen", "0.0.0.0:7710"}, 2,
			"laug · usage record: -listen 0.0.0.0:7710: 127.0.0.1:<port> (7710 for the observability stack); the " +
				"listener has no auth"},
		{"record, -listen localhost", []string{"record", "-listen", "localhost:7710"}, 2,
			"laug · usage record: -listen localhost:7710: 127.0.0.1:<port>"},
		{"record, -listen port 0", []string{"record", "-listen", "127.0.0.1:0"}, 2,
			"laug · usage record: -listen 127.0.0.1:0: 127.0.0.1:<port>"},
		{"record, -listen port 07710", []string{"record", "-listen", "127.0.0.1:07710"}, 2,
			"laug · usage record: -listen 127.0.0.1:07710: 127.0.0.1:<port>"},
		{"record, -listen no port", []string{"record", "-listen", "127.0.0.1"}, 2,
			"laug · usage record: -listen 127.0.0.1: 127.0.0.1:<port>"},
		{"record, -pids a file", []string{"record", "-pids", file}, 2,
			"laug · usage record: -pids " + file + ": not a dir; name the dir of the stack's records, <OBS_DIR>/run"},
		{"record, -interval 0", []string{"record", "-interval", "0s"}, 2,
			"laug · usage record: -interval 0s: over 0 (5s, 500ms)"},
		{"record, -interval in bare seconds", []string{"record", "-interval", "5"}, 2,
			"laug · usage record: -interval 5: a duration, 5s say"},
		{"record, -for under 0", []string{"record", "-for", "-1s"}, 2,
			"laug · usage record: -for -1s: 0 or more; 0 waits for bin/obs usage stop"},
		{"record, -out empty", []string{"record", "-out", ""}, 2, "laug · usage record: -out: name a file"},
		{"record, -out none without -listen", []string{"record", "-out", "none"}, 2,
			"laug · usage record: -out none writes no line, so it takes -listen, where the last sample is served"},
		{"record, -out a dir", []string{"record", "-out", "gate"}, 2,
			"laug · usage record: -out gate: a directory; name a file"},
		{"record, an argument", []string{"record", "x"}, 2,
			`laug · usage record: takes no arguments, got ["x"]; -out names the file`},
		{"sample, -pids a file", []string{"sample", "-pids", file}, 2,
			"laug · usage sample: -pids " + file + ": not a dir; name the dir of the stack's records, <OBS_DIR>/run"},
		{"sample, an argument", []string{"sample", "x"}, 2, `laug · usage sample: takes no arguments, got ["x"]`},
		{"stop, two files", []string{"stop", "a", "b"}, 2, `laug · usage stop: takes one file, got ["a" "b"]`},
		{"report, two files", []string{"report", "a", "b"}, 2, `laug · usage report: takes one file, got ["a" "b"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runApp(t, &App{Root: root, Environ: env}, append([]string{"usage"}, c.args...)...)
			if code != c.code || out != "" || firstLine(errOut) != c.err && !strings.HasPrefix(firstLine(errOut), c.err) {
				t.Errorf("exit %d, want %d; stderr's first line %q, want %q\nstdout:\n%s", code, c.code,
					firstLine(errOut), c.err, out)
			}
		})
	}
	if *named != 0 {
		t.Errorf("a refused record named its process cellstat %d times", *named)
	}
	if _, err := os.Stat(filepath.Join(root, "cells")); !os.IsNotExist(err) {
		t.Errorf("a refusal made cells/ in the checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(answers, "calls.log")); !os.IsNotExist(err) {
		t.Errorf("a refusal called docker: %v", err)
	}

	t.Run("no kernel tree", func(t *testing.T) {
		saved := usageRoot
		usageRoot = t.TempDir()
		defer func() { usageRoot = saved }()
		for _, verb := range []string{"record", "sample"} {
			code, out, errOut := runApp(t, &App{Root: root, Environ: env}, "usage", verb)
			if want := "laug · usage " + verb + ": no /proc/uptime or /sys/fs/cgroup here: cellstat reads a Linux " +
				"host's proc and cgroup v2 trees\n"; code != 2 || out != "" || errOut != want {
				t.Errorf("%s: exit %d, stdout %q, stderr %q; want 2 and %q", verb, code, out, errOut, want)
			}
		}
	})

	t.Run("sample", func(t *testing.T) {
		code, out, errOut := runApp(t, &App{Root: root, Environ: env}, "usage", "sample")
		if code != 0 || errOut != "" || out != usageSampleText {
			t.Errorf("exit %d, stderr %q\nstdout:\n%s\nwant:\n%s", code, errOut, out, usageSampleText)
		}
		if calls, _ := os.ReadFile(filepath.Join(answers, "calls.log")); string(calls) !=
			"ps -q --no-trunc --filter label=com.docker.compose.project\ninspect "+usageIDA+"\n" {
			t.Errorf("docker calls:\n%s", calls)
		}
		if ents, _ := os.ReadDir(root); len(ents) != 2 {
			t.Errorf("sample wrote in the checkout: %d entries, want go.mod and gate", len(ents))
		}
	})

	t.Run("sample -json", func(t *testing.T) {
		code, out, errOut := runApp(t, &App{Root: root, Environ: env}, "usage", "sample", "-json")
		var l cellstat.Line
		if code != 0 || errOut != "" || strings.Count(out, "\n") != 1 || json.Unmarshal([]byte(out), &l) != nil {
			t.Fatalf("exit %d, stderr %q, stdout %q: want one JSON line", code, errOut, out)
		}
		if l.V != 1 || len(l.Containers) != 1 || l.Containers[0].Service != "temporal-history" ||
			l.Containers[0].State != "ok" || l.Procs == nil || len(*l.Procs) != 2 || len(l.Errors) != 0 {
			t.Errorf("the line: %s", out)
		}
	})

	t.Run("sample, no docker on PATH", func(t *testing.T) {
		code, out, errOut := runApp(t, &App{Root: root}, "usage", "sample")
		want := "laug · usage sample: 0 laug containers, 0 of other projects, 2 Omes processes, host 24 vCPU; " +
			"errors: list failed\n"
		if code != 0 || errOut != "laug · usage sample: no docker on PATH: a line holds the host and Omes, no "+
			"container, and says list failed\n" || firstLine(out)+"\n" != want {
			t.Errorf("exit %d\nstderr:\n%s\nstdout:\n%s\nwant first:\n%s", code, errOut, out, want)
		}
	})
}

// TestUsageRecord runs record in this process: its first line once the file is locked, a line at start and one at
// the end, the pid file while it records, the name cellstat taken once; -for ends it, -listen serves the last sample,
// an -out that exists is refused, a recording's or not, and -out none with -listen serves it and writes no file, as
// the observability stack's cellstat.
func TestUsageRecord(t *testing.T) {
	named := usageTree(t)
	env, _ := stubDocker(t, true)
	root := checkout(t)
	out := filepath.Join(root, usageOutDefault)
	pid := strconv.Itoa(os.Getpid())

	r := startRecord(t, &App{Root: root, Environ: env}, "-interval", "1h")
	waitUntil(t, "the first line in the file", func() bool { return lineCount(out) == 1 })
	if want := "laug · usage record: a line every 1h0m0s into " + usageOutDefault + " for 20m0s at most, pid " + pid +
		"; bin/obs usage stop " + usageOutDefault + ", Ctrl-C or SIGTERM stops it"; r.first != want {
		t.Errorf("first line %q\nwant %q", r.first, want)
	}
	if b, err := os.ReadFile(out + ".pid"); err != nil || string(b) != pid+"\n" {
		t.Errorf("pid file %q, %v; want %s", b, err, pid)
	}
	code, stdout := r.stop(t)
	if want := "laug · usage record: stopped: 2 lines in " + usageOutDefault + "; bin/obs usage report " +
		usageOutDefault + " sums them"; code != 0 || lastLine(stdout) != want {
		t.Errorf("exit %d, last line %q; want 0 and %q\nstderr:\n%s", code, lastLine(stdout), want, r.stderr)
	}
	for i, line := range strings.Split(strings.TrimSuffix(readFile(t, out), "\n"), "\n") {
		var l cellstat.Line
		if err := json.Unmarshal([]byte(line), &l); err != nil || l.Seq != i || len(l.Containers) != 1 ||
			l.Containers[0].Project != "laug" || len(l.Errors) != 0 {
			t.Errorf("line %d: %s, %v", i, line, err)
		}
	}
	if _, err := os.Stat(out + ".pid"); !os.IsNotExist(err) {
		t.Errorf("the pid file is still there: %v", err)
	}
	if *named != 1 {
		t.Errorf("named the process cellstat %d times, want once", *named)
	}

	code, so, errOut := runApp(t, &App{Root: root, Environ: env}, "usage", "record")
	if want := "laug · usage record: -out " + usageOutDefault + ": exists, 2 lines; name a new file, or sum it with " +
		"bin/obs usage report " + usageOutDefault + "\n"; code != 2 || so != "" || errOut != want {
		t.Errorf("record over a file with lines: exit %d, stdout %q, stderr %q; want 2 and %q", code, so, errOut, want)
	}
	empty := filepath.Join(root, "empty.jsonl")
	writeFile(t, empty, "")
	if code, _, errOut := runApp(t, &App{Root: root, Environ: env}, "usage", "record", "-out", "empty.jsonl"); code != 2 ||
		errOut != "laug · usage record: -out empty.jsonl: exists, 0 lines; name a new file, or sum it with bin/obs usage "+
			"report empty.jsonl\n" {
		t.Errorf("record over an empty file: exit %d, stderr %q", code, errOut)
	}

	t.Run("-for", func(t *testing.T) {
		code, so, errOut := runApp(t, &App{Root: root, Environ: env}, "usage", "record", "-out", "for.jsonl",
			"-interval", "1h", "-for", "50ms")
		if code != 0 || !strings.Contains(firstLine(so), " into for.jsonl for 50ms at most, pid ") ||
			lineCount(filepath.Join(root, "for.jsonl")) != 2 {
			t.Errorf("exit %d, %d lines\nstdout:\n%s\nstderr:\n%s", code, lineCount(filepath.Join(root, "for.jsonl")),
				so, errOut)
		}
	})

	t.Run("-listen and -pids, then a second record on the file", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		pids := filepath.Join(root, "records")
		r := startRecord(t, &App{Root: root, Environ: env}, "-out", "l.jsonl", "-interval", "1h", "-for", "0",
			"-listen", addr, "-pids", "records")
		waitUntil(t, "the first line in the file", func() bool { return lineCount(filepath.Join(root, "l.jsonl")) == 1 })
		if want := " into l.jsonl until stopped, pid " + pid + "; the last sample on http://" + addr + "/metrics; " +
			"also the processes recorded in records; bin/obs usage stop l.jsonl, Ctrl-C or SIGTERM stops it"; !strings.
			HasSuffix(r.first, want) {
			t.Errorf("first line %q\nwant it to end %q", r.first, want)
		}
		// cellstat writes the line, then renders the body /metrics serves; until then /metrics answers 503.
		waitUntil(t, "200 from GET /metrics", func() bool {
			resp, err := http.Get("http://" + addr + "/metrics")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		})
		code, so, errOut := runApp(t, &App{Root: root, Environ: env}, "usage", "record", "-out", "l.jsonl")
		if code != 2 || so != "" || !strings.HasPrefix(errOut, "laug · usage record: -out l.jsonl: ") {
			t.Errorf("a second record: exit %d, stdout %q, stderr %q", code, so, errOut)
		}
		if code, _ := r.stop(t); code != 0 {
			t.Errorf("exit %d\nstderr:\n%s", code, r.stderr)
		}
		var l cellstat.Line
		if err := json.Unmarshal([]byte(firstLine(readFile(t, filepath.Join(root, "l.jsonl")))), &l); err != nil ||
			l.Pids == nil {
			t.Errorf("-pids %s: the line has no pids: %v", pids, err)
		}
	})

	t.Run("-out none", func(t *testing.T) { recordRunNone(t, root, env) })
}

// recordRunNone is TestUsageRecord's -out none case: the last sample on -listen, no file and no pid file, in the
// checkout or anywhere -out would name, and a last line that says so.
func recordRunNone(t *testing.T, root string, env []string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	r := startRecord(t, &App{Root: root, Environ: env}, "-out", "none", "-interval", "1h", "-for", "0", "-listen", addr,
		"-pids", "records")
	if want := "laug · usage record: no file (-out none): the last sample on http://" + addr + "/metrics, one every " +
		"1h0m0s until stopped, pid " + strconv.Itoa(os.Getpid()) + "; also the processes recorded in records; Ctrl-C " +
		"or SIGTERM stops it"; r.first != want {
		t.Errorf("first line %q\nwant %q", r.first, want)
	}
	var status int
	for deadline := time.Now().Add(5 * time.Second); status != http.StatusOK && time.Now().Before(deadline); {
		if resp, err := http.Get("http://" + addr + "/metrics"); err == nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != http.StatusOK {
		t.Errorf("GET /metrics: %d", status)
	}
	code, stdout := r.stop(t)
	if want := "laug · usage record: stopped; -out none, so no file holds a line"; code != 0 || lastLine(stdout) != want {
		t.Errorf("exit %d, last line %q; want 0 and %q\nstderr:\n%s", code, lastLine(stdout), want, r.stderr)
	}
	for _, p := range []string{"none", "none.pid", usageDir + "/none"} {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("-out none: %s is there: %v", p, err)
		}
	}
}

// recordRun is usage record running in a goroutine of this test.
type recordRun struct {
	cancel         context.CancelFunc
	done           chan int
	stdout, stderr *syncBuffer
	first          string
}

// startRecord runs usage record with args on a and waits for its first line.
func startRecord(t *testing.T, a *App, args ...string) *recordRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &recordRun{cancel: cancel, done: make(chan int, 1), stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	a.Stdout, a.Stderr = r.stdout, r.stderr
	go func() { r.done <- run(ctx, a, append([]string{"usage", "record"}, args...)) }()
	t.Cleanup(func() {
		cancel()
		<-r.done
		r.done <- 0
	})
	waitUntil(t, "record's first line", func() bool { return strings.Contains(r.stdout.String(), "\n") })
	r.first = firstLine(r.stdout.String())
	return r
}

// stop cancels the record, as TERM does, and returns its exit and stdout.
func (r *recordRun) stop(t *testing.T) (int, string) {
	t.Helper()
	r.cancel()
	select {
	case code := <-r.done:
		r.done <- code
		return code, r.stdout.String()
	case <-time.After(5 * time.Second):
		t.Fatal("record did not end within 5 s of its cancel")
	}
	return 0, ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// usageProc is a process of the built binary, started with env alone.
type usageProc struct {
	cmd            *exec.Cmd
	stdout, stderr *syncBuffer
	done           chan error
}

func startUsageProc(t *testing.T, env []string, args ...string) *usageProc {
	t.Helper()
	cmd := exec.Command(filepath.Join(obsBinary(t), "obs"), args...)
	cmd.Env, cmd.Dir = env, t.TempDir()
	p := &usageProc{cmd: cmd, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-p.done
	})
	return p
}

// exit waits up to 5 s for the process and returns its exit code.
func (p *usageProc) exit(t *testing.T) int {
	t.Helper()
	select {
	case err := <-p.done:
		p.done <- err
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not exit within 5 s\nstderr:\n%s", p.cmd.Path, p.stderr)
	}
	return 0
}

// TestUsageStop stops real recordings, the built binary's usage record, named cellstat, each with one last line. It
// refuses a pid file whose process is not named cellstat or does not hold the file's lock, as usage record -out none
// does not, says when the recording is still up after the wait, and bare, lists what runs.
func TestUsageStop(t *testing.T) {
	if err := kernelTree(); err != nil {
		t.Skipf("bin/obs usage record reads this host: %v", err)
	}
	usageTree(t)
	usageRoot = "/"
	env, _ := stubDocker(t, false)
	dir := t.TempDir()

	t.Run("bin/obs usage record", func(t *testing.T) {
		out := filepath.Join(dir, "a.jsonl")
		p := startUsageProc(t, env, "usage", "record", "-out", out, "-interval", "1h", "-for", "0")
		waitUntil(t, "a line", func() bool { return lineCount(out) == 1 && strings.Contains(p.stdout.String(), "\n") })
		pid := strconv.Itoa(p.cmd.Process.Pid)
		if want := "laug · usage record: a line every 1h0m0s into " + out + " until stopped, pid " + pid +
			"; bin/obs usage stop " + out + ", Ctrl-C or SIGTERM stops it"; firstLine(p.stdout.String()) != want {
			t.Errorf("first line %q\nwant %q", firstLine(p.stdout.String()), want)
		}
		if comm, err := os.ReadFile(filepath.Join("/proc", pid, "comm")); err != nil || string(comm) != "cellstat\n" {
			t.Errorf("/proc/%s/comm %q, %v; want cellstat", pid, comm, err)
		}
		code, so, errOut := runApp(t, &App{}, "usage", "record", "-out", out)
		if want := "laug · usage record: -out " + out + ": cellstat " + pid + " writes it; bin/obs usage stop " + out +
			" ends it, or name a new file\n"; code != 2 || so != "" || errOut != want {
			t.Errorf("record onto it: exit %d, stdout %q, stderr %q; want 2 and %q", code, so, errOut, want)
		}
		code, so, errOut = runApp(t, &App{}, "usage", "stop", out)
		if want := "laug · usage stop: cellstat " + pid + " stopped: 2 lines in " + out + "\n"; code != 0 || so != want ||
			errOut != "" {
			t.Errorf("stop: exit %d, stdout %q, stderr %q; want 0 and %q", code, so, errOut, want)
		}
		if code := p.exit(t); code != 0 || lastLine(p.stdout.String()) != "laug · usage record: stopped: 2 lines in "+
			out+"; bin/obs usage report "+out+" sums them" {
			t.Errorf("record: exit %d\nstdout:\n%s\nstderr:\n%s", code, p.stdout, p.stderr)
		}
		code, so, errOut = runApp(t, &App{}, "usage", "stop", out)
		if code != 1 || so != "" || errOut != "laug · usage stop: no cellstat is writing "+out+"\n" {
			t.Errorf("stop again: exit %d, stdout %q, stderr %q; want 1, none writes it", code, so, errOut)
		}
	})

	t.Run("no lock, or not cellstat", func(t *testing.T) {
		// usage record -out none holds no file's lock, as the stack's cellstat, and this test is not named cellstat.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		p := startUsageProc(t, env, "usage", "record", "-out", "none", "-listen", addr, "-interval", "1h", "-for", "0")
		waitUntil(t, "its first line", func() bool { return strings.Contains(p.stdout.String(), "\n") })
		for name, pid := range map[string]int{"no lock": p.cmd.Process.Pid, "not cellstat": os.Getpid()} {
			out := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".jsonl")
			writeFile(t, out, "{}\n")
			writeFile(t, out+".pid", strconv.Itoa(pid)+"\n")
			if code, so, errOut := runApp(t, &App{}, "usage", "stop", out); code != 1 || so != "" ||
				errOut != "laug · usage stop: no cellstat is writing "+out+"\n" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q; want 1", name, code, so, errOut)
			}
		}
		if err := p.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Errorf("stop signalled a cellstat that writes no file: %v", err)
		}
	})

	t.Run("still up after the wait", func(t *testing.T) {
		saved := usageStopWait
		usageStopWait = 200 * time.Millisecond
		defer func() { usageStopWait = saved }()
		stall := filepath.Join(dir, "stall")
		sleep, err := exec.LookPath("sleep") // the child's PATH holds the stub alone
		if err != nil {
			t.Skip("no sleep on PATH to stall a docker call with")
		}
		slow := t.TempDir()
		writeExec(t, filepath.Join(slow, "docker"), "#!/bin/sh\n[ -e '"+stall+"' ] && '"+sleep+"' 1\nexit 0\n")
		out := filepath.Join(dir, "c.jsonl")
		p := startUsageProc(t, []string{"PATH=" + slow}, "usage", "record", "-out", out, "-interval", "1h", "-for", "0")
		waitUntil(t, "a line", func() bool { return lineCount(out) == 1 })
		writeFile(t, stall, "")
		code, so, errOut := runApp(t, &App{}, "usage", "stop", out)
		if want := fmt.Sprintf("laug · usage stop: cellstat %d is still up after 200ms (slow docker calls): bin/obs usage "+
			"stop %s waits 200ms more\n", p.cmd.Process.Pid, out); code != 1 || so != "" || errOut != want {
			t.Errorf("exit %d, stdout %q, stderr %q; want 1 and %q", code, so, errOut, want)
		}
		if code := p.exit(t); code != 0 || lineCount(out) != 2 {
			t.Errorf("after its last docker call: exit %d, %d lines; want 0 and 2", code, lineCount(out))
		}
	})

	t.Run("a second TERM", func(t *testing.T) {
		sleep, err := exec.LookPath("sleep")
		if err != nil {
			t.Skip("no sleep on PATH to stall a docker call with")
		}
		stall := filepath.Join(dir, "stall2")
		slow := t.TempDir()
		writeExec(t, filepath.Join(slow, "docker"), "#!/bin/sh\n[ -e '"+stall+"' ] && '"+sleep+"' 1\nexit 0\n")
		out := filepath.Join(dir, "d.jsonl")
		p := startUsageProc(t, []string{"PATH=" + slow}, "usage", "record", "-out", out, "-interval", "1h")
		waitUntil(t, "a line", func() bool { return lineCount(out) == 1 })
		writeFile(t, stall, "")
		for range 2 {
			if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if code := p.exit(t); code != 0 || lineCount(out) != 2 {
			t.Errorf("TERM twice, the last line's docker call stalled: exit %d, %d lines; want 0 and 2\nstderr:\n%s",
				code, lineCount(out), p.stderr)
		}
	})

	t.Run("bare", func(t *testing.T) {
		root := checkout(t)
		run := filepath.Join(root, usageDir)
		writeFile(t, filepath.Join(run, "done.jsonl"), "{}\n{}\n{}\n")
		code, so, errOut := runApp(t, &App{Root: root}, "usage", "stop")
		if code != 2 || so != "" || !strings.HasPrefix(errOut, "laug · usage stop: names no recording: give it the "+
			"file a recording writes\nno cellstat writes a file in cells/run/cellstat\nusage: bin/obs usage stop <file>\n") {
			t.Errorf("none running: exit %d, stdout %q\nstderr:\n%s", code, so, errOut)
		}
		now := filepath.Join(run, "now.jsonl")
		p := startUsageProc(t, env, "usage", "record", "-out", now, "-interval", "1h", "-for", "0")
		waitUntil(t, "a line", func() bool { return lineCount(filepath.Join(run, "now.jsonl")) == 1 })
		code, _, errOut = runApp(t, &App{Root: root}, "usage", "stop")
		if want := fmt.Sprintf("in cells/run/cellstat:\n  bin/obs usage stop cells/run/cellstat/now.jsonl   (1 line, "+
			"cellstat %d writing)\nusage: ", p.cmd.Process.Pid); code != 2 || !strings.Contains(errOut, want) {
			t.Errorf("one running: exit %d\nstderr:\n%s\nwant it to hold:\n%s", code, errOut, want)
		}
		code, _, errOut = runApp(t, &App{Root: root}, "usage", "report")
		if want := fmt.Sprintf("laug · usage report: names no file: give it a file of lines a recording wrote\nin "+
			"cells/run/cellstat:\n  bin/obs usage report cells/run/cellstat/done.jsonl   (3 lines)\n  bin/obs usage report "+
			"cells/run/cellstat/now.jsonl   (1 line, cellstat %d writing)\nusage: bin/obs usage report <file>",
			p.cmd.Process.Pid); code != 2 || !strings.HasPrefix(errOut, want) {
			t.Errorf("report, bare: exit %d\nstderr:\n%s\nwant it to start:\n%s", code, errOut, want)
		}
		if code, so, _ := runApp(t, &App{Root: root}, "usage", "stop", "cells/run/cellstat/now.jsonl"); code != 0 ||
			!strings.HasSuffix(so, " stopped: 2 lines in cells/run/cellstat/now.jsonl\n") {
			t.Errorf("stop by the path from the checkout's root: exit %d, stdout %q", code, so)
		}
	})
}

// usageLines writes n lines of cells/run/cellstat's shape to path, 5 s apart from wall, inside
// g20261015-144517's wave from 1792075705.778 on: compose project laug-1dafb33b's temporal-history at 1 core and the
// host at 24 vCPU, 4.8 of them busy.
func usageLines(t *testing.T, path string, n int, wall float64) {
	t.Helper()
	u := func(x uint64) *uint64 { return &x }
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		mono := 7586.47 + 5*float64(i)
		k := uint64(i)
		l := cellstat.Line{V: 1, Seq: i, Wall: wall + 5*float64(i), Mono: &mono, SelfCPU: u(20000 * k),
			ChildrenCPU: u(5000 * k), Containers: []cellstat.Container{{Project: "laug-1dafb33b",
				Service: "temporal-history", ID: "a1a1a1a1a1a1", Via: "pid", State: "ok", CPU: u(5000000 * k)}},
			Host: cellstat.Host{VCPU: u(24), Ticks: &cellstat.Ticks{User: u(1000000 + 2000*k), Nice: u(0),
				System: u(500000 + 400*k), Idle: u(50000000 + 9600*k), IOWait: u(0), IRQ: u(0), SoftIRQ: u(0),
				Steal: u(0)}}}
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(append(j, '\n'))
	}
	writeFile(t, path, b.String())
}

// TestUsageReport sums a file over the whole of it and over a gate run's wave, by id in -results or by its path, in
// text and JSON, and refuses or fails as make cell-usage-report did.
func TestUsageReport(t *testing.T) {
	root := checkout(t)
	src, err := filepath.Abs(filepath.Join("..", "..", "cellstat", "testdata", "gate"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, "gate", "results", "g20261015-144517"), os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	usageLines(t, filepath.Join(root, "g.jsonl"), 4, 1792075705.778)
	usageLines(t, filepath.Join(root, "one.jsonl"), 1, 1792075705.778)
	head := regexp.MustCompile(`^cellstat window [0-9.]+ to [0-9.]+ wall, [0-9.]+ s, 4 lines over 15\.0 s mono, ` +
		`0 resets(, items s1 s2 s3)?$`)

	for _, c := range []struct {
		name     string
		args     []string
		first    string
		item     string
		gateSpan bool
	}{
		{"the whole file", []string{"g.jsonl"}, "laug · usage report: g.jsonl over the whole file", "laug-1dafb33b", false},
		{"a gate id", []string{"g.jsonl", "-gate", "g20261015-144517"},
			"laug · usage report: g.jsonl over gate g20261015-144517's wave", "s1", true},
		{"a gate dir", []string{"g.jsonl", "-gate", src}, "laug · usage report: g.jsonl over gate " + shellWord(src) + "'s wave",
			"s1", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runApp(t, &App{Root: root}, append([]string{"usage", "report"}, c.args...)...)
			lines := strings.Split(out, "\n")
			if code != 0 || errOut != "" || lines[0] != c.first || len(lines) < 3 || !head.MatchString(lines[1]) ||
				!strings.HasPrefix(lines[2], "item  key") {
				t.Errorf("exit %d, stderr %q\nstdout:\n%s", code, errOut, out)
			}
			code, out, errOut = runApp(t, &App{Root: root}, append([]string{"usage", "report", "-json"}, c.args...)...)
			var r struct {
				Window struct {
					Lines int      `json:"lines"`
					Items []string `json:"items"`
				} `json:"window"`
				Items []struct {
					Item  string   `json:"item"`
					Cores *float64 `json:"cores"`
				} `json:"items"`
				Host struct {
					BusyCores *float64 `json:"busy_cores"`
				} `json:"host"`
			}
			if code != 0 || errOut != "" || strings.Count(out, "\n") != 1 || json.Unmarshal([]byte(out), &r) != nil {
				t.Fatalf("-json: exit %d, stderr %q, stdout %q; want one JSON line", code, errOut, out)
			}
			if r.Window.Lines != 4 || len(r.Items) != 1 || r.Items[0].Item != c.item || r.Items[0].Cores == nil ||
				*r.Items[0].Cores != 1 || r.Host.BusyCores == nil || *r.Host.BusyCores != 4.8 ||
				(len(r.Window.Items) == 3) != c.gateSpan {
				t.Errorf("-json: %s", out)
			}
		})
	}

	for _, c := range []struct {
		name string
		args []string
		code int
		err  string
	}{
		{"one line", []string{"one.jsonl"}, 1,
			"laug · usage report: one.jsonl over the whole file: 1 line in the window; a rate needs 2"},
		{"no file", []string{"none.jsonl"}, 1,
			"laug · usage report: none.jsonl over the whole file: the file of lines cannot be read"},
		{"no such gate run", []string{"g.jsonl", "-gate", "g20261015-144518"}, 1,
			"laug · usage report: g.jsonl over gate g20261015-144518's wave: the gate dir has no timeline.jsonl"},
		{"a gate id of two words", []string{"g.jsonl", "-gate", "a b"}, 2,
			"laug · usage report: -gate 'a b': neither a gate id in gate/results, one word of letters, digits, ., _ " +
				"and -, nor the path to a run's directory"},
		{"-results alone", []string{"g.jsonl", "-results", "elsewhere"}, 2,
			"laug · usage report: -results is where -gate's id is: give -gate too"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runApp(t, &App{Root: root}, append([]string{"usage", "report"}, c.args...)...)
			if code != c.code || out != "" || errOut != c.err+"\n" {
				t.Errorf("exit %d, stdout %q, stderr %q; want %d and %q", code, out, errOut, c.code, c.err)
			}
		})
	}
}

// TestUsageDefaultsMatchKnobs: record's defaults are the INTERVAL, USAGE_FOR and USAGE_OUT of the Makefile laug
// replaced, as internal/script's knob table keeps them.
func TestUsageDefaultsMatchKnobs(t *testing.T) {
	var o usageRecordOpts
	(&App{}).newUsageRecordFlags(&o)
	for name, want := range map[string]any{"INTERVAL": o.interval, "USAGE_FOR": o.forDur, "USAGE_OUT": o.out} {
		k, ok := script.Lookup(name)
		if !ok {
			t.Errorf("no knob %s", name)
			continue
		}
		var got any = k.Default
		if d, ok := want.(time.Duration); ok {
			got, _ = time.ParseDuration(k.Default)
			want = d
		}
		if got != want {
			t.Errorf("knob %s %q, record's default %v", name, k.Default, want)
		}
	}
}
