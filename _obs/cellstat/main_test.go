package cellstat

// Tests 1, 7 to 9 and 12 to 14 of the plan, 16 and 17 from the review of its step 4, and the helpers every test file
// shares. Each test copies testdata/dev to t.TempDir() (test 1 testdata/lab too), lists containers through a fake
// lister (13, 14, 16 and 17 through testdata/fake-docker.sh) and asserts on the JSON bytes of the line. 16 and 17
// run this test binary as cellstat itself (TestMain), so a second process holds the lock and takes a real TERM.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain runs cellstat with this binary's arguments when LAUG_CELLSTAT_MAIN is set, cellstatProc's child, as laug
// usage record runs it: TERM and INT cancel its context for as long as it runs, so a second TERM ends the same way as
// the first.
func TestMain(m *testing.M) {
	if os.Getenv("LAUG_CELLSTAT_MAIN") != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// cellstatProc starts this test binary as cellstat with args. Its stderr is read after Wait; a child the test left
// running is killed at cleanup.
func cellstatProc(t *testing.T, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "LAUG_CELLSTAT_MAIN=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd, &stderr
}

// waitFor polls ok every millisecond for 5 s at most.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s within 5 s", what)
		}
	}
}

func lineCount(path string) int {
	b, _ := os.ReadFile(path)
	return bytes.Count(b, []byte("\n"))
}

// Made-up ids: A and D are full containers, B the systemd layout, C a group with no controllers, K the root's
// io.stat. Their pids in testdata/dev/proc are 101, 202, 303, 404 and 505.
var (
	idA = strings.Repeat("0123456789abcdef", 4)
	idB = strings.Repeat("b", 64)
	idC = strings.Repeat("c", 64)
	idD = strings.Repeat("d", 64)
	idK = strings.Repeat("e", 64)
	idL = strings.Repeat("fedcba9876543210", 4) // testdata/lab: one container read on the lab host, pid 606
)

const runID = "g20261015-144517-s1"

// devTree copies testdata/dev into a temp dir, so a test may change it.
func devTree(t *testing.T) string { return copyTree(t, "testdata/dev") }

// copyTree copies src into a temp dir.
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
	return dst
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cgroupDir(root, rel string) string { return filepath.Join(root, "sys", "fs", "cgroup", rel) }

// fc is one container the fake lister knows.
type fc struct {
	id, project, service string
	pid                  int
	running              bool
}

func laug(id, service string, pid int) fc { return fc{id, "laug-1dafb33b", service, pid, true} }

// fakeLister answers list and inspect from memory and records what inspect was asked.
type fakeLister struct {
	cs      []fc
	listErr error
	asked   [][]string
}

func (f *fakeLister) list() ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var ids []string
	for _, c := range f.cs {
		ids = append(ids, c.id)
	}
	return ids, nil
}

func (f *fakeLister) inspect(ids []string) ([]inspected, error) {
	f.asked = append(f.asked, ids)
	var out []inspected
	for _, id := range ids {
		for _, c := range f.cs {
			if c.id == id {
				out = append(out, inspection(c))
			}
		}
	}
	return out, nil
}

func inspection(c fc) inspected {
	var in inspected
	in.ID = c.id
	in.State.Pid, in.State.Running = c.pid, c.running
	in.Config.Labels = map[string]string{projectLabel: c.project, serviceLabel: c.service}
	return in
}

// testSampler is a sampler with a fixed clock and a fixed own CPU, so two samplers on equal trees write equal bytes.
func testSampler(root string, l lister, procs bool) *sampler {
	s := newSampler(root, l, regexp.MustCompile(defaultProject), regexp.MustCompile(defaultRunID), procs)
	s.now = func() time.Time { return time.UnixMilli(1792075705778) }
	s.usage = func() (*uint64, *uint64) { a, b := uint64(41000), uint64(30000); return &a, &b }
	return s
}

func marshal(t *testing.T, l Line) []byte {
	t.Helper()
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// containers maps each container's 12-hex id to its keys and their raw JSON values.
func containers(t *testing.T, l Line) map[string]map[string]string {
	t.Helper()
	var v struct {
		Containers []map[string]json.RawMessage `json:"containers"`
	}
	if err := json.Unmarshal(marshal(t, l), &v); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	for _, c := range v.Containers {
		m := map[string]string{}
		for k, raw := range c {
			m[k] = string(raw)
		}
		out[strings.Trim(m["id"], `"`)] = m
	}
	return out
}

func one(t *testing.T, root string, cs ...fc) map[string]string {
	t.Helper()
	got := containers(t, testSampler(root, &fakeLister{cs: cs}, false).sample())
	c, ok := got[cs[0].id[:12]]
	if !ok {
		t.Fatalf("container %s not in the line: %v", cs[0].id[:12], got)
	}
	return c
}

func want(t *testing.T, c map[string]string, kv ...string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		if got, ok := c[kv[i]]; !ok || got != kv[i+1] {
			t.Errorf("%s: got %q (present %v), want %s", kv[i], got, ok, kv[i+1])
		}
	}
}

func absent(t *testing.T, c map[string]string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := c[k]; ok {
			t.Errorf("%s is %s, want absent", k, v)
		}
	}
}

// labelsOnly fails when a container carries any key past its labels and state: gone and unresolved read nothing.
func labelsOnly(t *testing.T, c map[string]string) {
	t.Helper()
	for k := range c {
		switch k {
		case "project", "service", "id", "via", "state", "why":
		default:
			t.Errorf("key %s = %s on a container that should carry no reading", k, c[k])
		}
	}
}

// Test 1: both layouts by the pid route, each read from its own dir; then the lab host's container, whose files
// carry keys the dev host's do not (cpu.stat 10 keys against 8, memory.events 7 against 6, 9 controllers).
func TestBothLayoutsByPid(t *testing.T) {
	root := devTree(t)
	got := containers(t, testSampler(root, &fakeLister{cs: []fc{laug(idA, "temporal-matching", 101),
		laug(idB, "temporal-history", 202)}}, false).sample())
	want(t, got[idA[:12]], "state", `"ok"`, "via", `"pid"`, "cpu_us", "3433220485", "anon_b", "16539648")
	want(t, got[idB[:12]], "state", `"ok"`, "via", `"pid"`, "cpu_us", "29797165", "anon_b", "231313408")
	absent(t, got[idA[:12]], "missing", "why")
	absent(t, got[idB[:12]], "missing", "why")

	lab := one(t, copyTree(t, "testdata/lab"), laug(idL, "temporal-history", 606))
	want(t, lab, "state", `"ok"`, "via", `"pid"`, "cpu_us", "3355464", "cpu_user_us", "2925987",
		"cpu_system_us", "429477", "throttled_n", "0", "throttled_us", "0", "anon_b", "157003776", "file_b", "0",
		"oom_kill", "0", "io_devs", "0", "cpu_some_us", "11218", "cpu_full_us", "4127", "io_some_us", "0",
		"io_full_us", "0", "mem_some_us", "0", "mem_full_us", "0")
	absent(t, lab, "missing", "why", "cpu_max_us", "cpu_period_us", "mem_max_b", "io_rbytes", "io_wbytes",
		"io_rios", "io_wios")
	if n := len(lab); n != 20 {
		t.Errorf("the lab container has %d keys, want 20 (5 labels and 15 readings): %v", n, lab)
	}
}

// Test 7: gone, three ways, and none carries a reading.
func TestGoneCarriesNoReading(t *testing.T) {
	root := devTree(t)
	l := &fakeLister{cs: []fc{laug(idA, "temporal-matching", 101)}}
	s := testSampler(root, l, false)
	want(t, containers(t, s.sample())[idA[:12]], "state", `"ok"`)
	if err := os.RemoveAll(cgroupDir(root, "docker/"+idA)); err != nil {
		t.Fatal(err)
	}
	c := containers(t, s.sample())[idA[:12]]
	want(t, c, "state", `"gone"`, "why", `"no cgroup dir"`)
	labelsOnly(t, c)

	write(t, cgroupDir(root, "system.slice/docker-"+idB+".scope/cgroup.events"), "populated 0\nfrozen 0\n")
	c = one(t, root, laug(idB, "temporal-history", 202))
	want(t, c, "state", `"gone"`, "why", `"empty"`)
	labelsOnly(t, c)

	l = &fakeLister{cs: []fc{laug(idD, "postgresql", 404)}}
	s = testSampler(root, l, false)
	want(t, containers(t, s.sample())[idD[:12]], "state", `"ok"`)
	l.cs = nil
	c = containers(t, s.sample())[idD[:12]]
	want(t, c, "state", `"gone"`, "why", `"left the list"`)
	labelsOnly(t, c)
	if b := marshal(t, s.sample()); bytes.Contains(b, []byte(idD[:12])) {
		t.Errorf("an id that left the list is written again: %s", b)
	}

	// A dir that vanishes during the read: readCgroup says so, and the container sits out the line.
	var gone Container
	if readCgroup(cgroupDir(root, "docker/"+idA), &gone) {
		t.Errorf("readCgroup on a dir that is not there returned true: %+v", gone)
	}
}

// Test 8: unresolved reads nothing, six ways, and is asked again next time. The id must be in the path's last
// element: a group below the container's, there with a cpu.stat, is not read.
func TestUnresolvedReadsNothing(t *testing.T) {
	root := devTree(t)
	other, dots, nopid, stopped := strings.Repeat("a", 64), strings.Repeat("ab", 32), strings.Repeat("ac", 32), strings.Repeat("ad", 32)
	sub, nodir := strings.Repeat("ae", 32), strings.Repeat("af", 32)
	write(t, filepath.Join(root, "proc/606/cgroup"), "0::/docker/"+idA+"\n")
	write(t, filepath.Join(root, "proc/607/cgroup"), "0::/docker/../docker/"+dots+"\n")
	write(t, filepath.Join(root, "proc/608/cgroup"), "0::/docker/"+sub+"/sub\n")
	write(t, filepath.Join(cgroupDir(root, "docker/"+sub+"/sub"), "cpu.stat"), "usage_usec 7\nuser_usec 4\nsystem_usec 3\n")
	write(t, filepath.Join(root, "proc/609/cgroup"), "0::/docker/"+nodir+"\n")
	l := &fakeLister{cs: []fc{laug(other, "temporal-frontend", 606), laug(dots, "temporal-history", 607),
		laug(nopid, "temporal-matching", 999), {stopped, "laug-1dafb33b", "temporal-worker", 0, false},
		laug(sub, "temporal-ui", 608), laug(nodir, "postgresql", 609)}}
	s := testSampler(root, l, false)
	got := containers(t, s.sample())
	for id, why := range map[string]string{other: "path lacks id", dots: "bad path", nopid: "no such pid", stopped: "not running",
		sub: "path lacks id", nodir: "no such dir"} {
		c := got[id[:12]]
		want(t, c, "state", `"unresolved"`, "why", strconv.Quote(why))
		labelsOnly(t, c)
		absent(t, c, "via")
	}
	s.sample()
	if len(l.asked) != 2 || len(l.asked[1]) != 6 {
		t.Errorf("inspect asked %v, want the 6 unresolved ids twice", l.asked)
	}
}

// Test 9: another project's container is counted in others, and its name, service and id are in no byte. So is an
// Omes worker of a run id that is not laug's, in other_procs.
func TestOtherProjectsCountedNeverNamed(t *testing.T) {
	root := devTree(t)
	nosy := strings.Repeat("f0", 32)
	write(t, filepath.Join(root, "proc/4250/cmdline"), "program\x00worker\x00--task-queue\x00omes-nosy-run\x00")
	write(t, filepath.Join(root, "proc/4250/task/4250/schedstat"), "1 2 3\n")
	l := &fakeLister{cs: []fc{laug(idA, "temporal-matching", 101), {nosy, "not-laug", "nosy-service", 202, true}}}
	s := testSampler(root, l, true)
	for i := 0; i < 2; i++ {
		b := marshal(t, s.sample())
		if !bytes.Contains(b, []byte(`"others":1,`)) || !bytes.Contains(b, []byte(`"other_procs":1}`)) {
			t.Errorf("line %d: others or other_procs is not 1: %s", i, b)
		}
		for _, w := range []string{"not-laug", "nosy-service", nosy[:12], "nosy-run", `"pid":4250`} {
			if bytes.Contains(b, []byte(w)) {
				t.Errorf("line %d holds %q", i, w)
			}
		}
	}
	if len(l.asked) != 1 {
		t.Errorf("inspect ran %d times, want once: another project's id is not asked again", len(l.asked))
	}
}

// Test 12: the Omes client and worker by argv, the go run parent skipped, sums over task/*, each cmdline read at the
// first two samples that see it and then kept; a process with no thread read carries no thread readings.
func TestOmesClientAndWorker(t *testing.T) {
	root := devTree(t)
	s := testSampler(root, &fakeLister{}, true)
	procs := func() string {
		var v struct {
			Procs json.RawMessage `json:"procs"`
		}
		if err := json.Unmarshal(marshal(t, s.sample()), &v); err != nil {
			t.Fatal(err)
		}
		return string(v.Procs)
	}
	client := `{"role":"omes-client","run_id":"` + runID + `","pid":4241,"threads":4,"run_ns":6330284084,"wait_ns":295546293,"runs":124899,"cpu_ticks":633}`
	worker := `{"role":"omes-worker","run_id":"` + runID + `","pid":4242,"threads":2,"run_ns":10264204943,"wait_ns":68287063,"runs":333356,"cpu_ticks":1027}`
	if got := procs(); got != "["+client+","+worker+"]" {
		t.Fatalf("procs:\n got %s\nwant [%s,%s]", got, client, worker)
	}
	// The second sample reads each cmdline again; after it a changed cmdline is not read while the pid lives: 4242
	// stays the worker, 4240 stays skipped.
	if got := procs(); got != "["+client+","+worker+"]" {
		t.Fatalf("the second sample: %s", got)
	}
	write(t, filepath.Join(root, "proc/4242/cmdline"), "sleep\x00600\x00")
	b, _ := os.ReadFile(filepath.Join(root, "proc/4241/cmdline"))
	write(t, filepath.Join(root, "proc/4240/cmdline"), string(b))
	if got := procs(); got != "["+client+","+worker+"]" {
		t.Errorf("after the cmdlines changed: %s", got)
	}
	// Omes may put --app <name> before worker (omes f0de206, internal/workerctl/run.go:124-127; source only).
	if o := omesOf([]string{"program", "--app", "x", "worker", "--task-queue", "omes-" + runID}); o == nil || *o != (omesProc{"omes-worker", runID}) {
		t.Errorf("--app before worker: %+v", o)
	}
	for _, task := range []string{"4242", "4243"} {
		if err := os.Remove(filepath.Join(root, "proc/4242/task", task, "schedstat")); err != nil {
			t.Fatal(err)
		}
	}
	bare := `{"role":"omes-worker","run_id":"` + runID + `","pid":4242,"cpu_ticks":1027}`
	if got := procs(); got != "["+client+","+bare+"]" {
		t.Errorf("no thread read: got %s, want the worker with no thread readings", got)
	}
	// A pid that leaves drops its verdict; the same pid back is read anew.
	if err := os.RemoveAll(filepath.Join(root, "proc/4242")); err != nil {
		t.Fatal(err)
	}
	if got := procs(); got != "["+client+"]" {
		t.Errorf("after 4242 left: %s", got)
	}
	write(t, filepath.Join(root, "proc/4242/cmdline"), "sleep\x00600\x00")
	write(t, filepath.Join(root, "proc/4242/task/4242/schedstat"), "1 2 3\n")
	if got := procs(); got != "["+client+"]" {
		t.Errorf("a new 4242 running sleep: %s", got)
	}
}

// fakeDocker points a dockerCLI at testdata/fake-docker.sh, the -cli stand-in, with its answers in a temp dir.
func fakeDocker(t *testing.T) (dockerCLI, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FAKE_DOCKER", dir)
	sh, err := filepath.Abs("testdata/fake-docker.sh")
	if err != nil {
		t.Fatal(err)
	}
	return dockerCLI{path: sh, timeout: 500 * time.Millisecond}, dir
}

func inspectJSON(t *testing.T, cs ...fc) string {
	t.Helper()
	var out []inspected
	for _, c := range cs {
		out = append(out, inspection(c))
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func errorsOf(t *testing.T, l Line) string {
	t.Helper()
	return fmt.Sprint(l.Errors)
}

// Test 13: the docker CLI, faked. A stderr warning parses; ps exit 1 keeps the last list; inspect exit 1 with
// partial stdout parses what it found; a call that hangs is list timeout, and so is one whose child holds stdout open,
// 1 s later.
func TestDockerCLIFake(t *testing.T) {
	root := devTree(t)
	cli, dir := fakeDocker(t)
	a := laug(idA, "temporal-matching", 101)
	write(t, filepath.Join(dir, "ps.out"), idA+"\n")
	write(t, filepath.Join(dir, "ps.err"), "WARNING: the daemon says something on stderr\n")
	write(t, filepath.Join(dir, "inspect.out"), inspectJSON(t, a))
	s := testSampler(root, cli, false)
	l := s.sample()
	want(t, containers(t, l)[idA[:12]], "state", `"ok"`)
	if len(l.Errors) != 0 {
		t.Errorf("errors %v after a warning on stderr", l.Errors)
	}

	write(t, filepath.Join(dir, "ps.exit"), "1\n")
	write(t, filepath.Join(dir, "ps.out"), "")
	l = s.sample()
	if got := errorsOf(t, l); got != "[list exit 1]" {
		t.Errorf("errors %s, want [list exit 1]", got)
	}
	want(t, containers(t, l)[idA[:12]], "state", `"ok"`)

	os.Remove(filepath.Join(dir, "ps.exit"))
	left := strings.Repeat("9", 64)
	write(t, filepath.Join(dir, "ps.out"), idA+"\n"+left+"\n")
	write(t, filepath.Join(dir, "inspect.err"), "Error: No such object: "+left+"\n")
	write(t, filepath.Join(dir, "inspect.exit"), "1\n")
	s = testSampler(root, cli, false)
	l = s.sample()
	got := containers(t, l)
	want(t, got[idA[:12]], "state", `"ok"`)
	if _, ok := got[left[:12]]; ok || len(l.Errors) != 0 {
		t.Errorf("inspect exit 1: containers %v, errors %v; want A alone and no error", got, l.Errors)
	}

	write(t, filepath.Join(dir, "ps.sleep"), "5\n")
	start := time.Now()
	l = s.sample()
	if got := errorsOf(t, l); got != "[list timeout]" || time.Since(start) > 3*time.Second {
		t.Errorf("errors %s after %s, want [list timeout] within the 500 ms timeout", got, time.Since(start))
	}
	want(t, containers(t, l)[idA[:12]], "state", `"ok"`)

	// The fake's delay runs sleep as bash's child, which the timeout's kill does not reach and which keeps stdout
	// open: the call ends when WaitDelay (1 s) closes the pipe, 1.5 s in, not when the 5 s sleep does.
	os.Remove(filepath.Join(dir, "ps.sleep"))
	write(t, filepath.Join(dir, "ps.delay"), "5\n")
	start = time.Now()
	l = s.sample()
	if got, took := errorsOf(t, l), time.Since(start); got != "[list timeout]" || took < time.Second || took > 3*time.Second {
		t.Errorf("errors %s after %s, want [list timeout] after the 500 ms timeout and the 1 s WaitDelay", got, took)
	}
	want(t, containers(t, l)[idA[:12]], "state", `"ok"`)
}

// Test 14: a non-empty -out, an -out that is a dir and a -root with no kernel tree exit 2; FILE.pid holds the pid
// while it runs and is removed; a cancelled loop writes one last line and exits 0.
func TestOutputRefusalPidFileAndLastLine(t *testing.T) {
	root := devTree(t)
	cli, _ := fakeDocker(t)
	out := filepath.Join(t.TempDir(), "usage.jsonl")
	write(t, out, "x\n")
	for name, args := range map[string][]string{"non-empty -out": {"-out", out, "-root", root}, "-out a dir": {"-out", t.TempDir(), "-root", root},
		"-root empty": {"-once", "-root", t.TempDir()}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), append(args, "-cli", cli.path), &stdout, &stderr); code != 2 || stdout.Len() > 0 {
			t.Errorf("%s: exit %d, %d bytes out; want 2 and none; stderr %s", name, code, stdout.Len(), stderr.String())
		}
	}
	if b, _ := os.ReadFile(out); string(b) != "x\n" {
		t.Errorf("the refused file changed: %q", b)
	}
	if _, err := os.Stat(out + ".pid"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused run left a pid file")
	}

	os.Remove(out)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int)
	var stderr bytes.Buffer // the loop's own: read only after done
	go func() {
		done <- run(ctx, []string{"-out", out, "-interval", "1h", "-root", root, "-cli", cli.path, "-procs=false"}, &bytes.Buffer{}, &stderr)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(out)
		p, err := os.ReadFile(out + ".pid")
		if bytes.Count(b, []byte("\n")) == 1 && err == nil {
			if string(p) != strconv.Itoa(os.Getpid())+"\n" {
				t.Errorf("pid file holds %q, want %d", p, os.Getpid())
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("no first line and pid file within 5 s; stderr: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("cancelled loop: exit %d, want 0", code)
	}
	b, _ := os.ReadFile(out)
	var seqs []int
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var l Line
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		seqs = append(seqs, l.Seq)
	}
	if !sort.IntsAreSorted(seqs) || fmt.Sprint(seqs) != "[0 1]" {
		t.Errorf("lines have seq %v, want [0 1]: the first sample and one last", seqs)
	}
	if _, err := os.Stat(out + ".pid"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("pid file left after exit")
	}
}

// Test 16: one cellstat a file. A second one on the -out a first one holds exits 2 and names the file by its base
// name alone, before the first has written a line and after; it leaves the file and the pid file as they were. Once
// the first has stopped, the lock is free: the file is refused for its lines, by runs started at once too, and once
// emptied it takes a line.
func TestOutLockedByAnotherCellstat(t *testing.T) {
	root := devTree(t)
	cli, dir := fakeDocker(t)
	write(t, filepath.Join(dir, "ps.delay"), "0.3\n") // the first line comes 0.3 s after the pid file
	out := filepath.Join(t.TempDir(), "usage.jsonl")
	args := []string{"-out", out, "-interval", "1h", "-root", root, "-cli", cli.path, "-procs=false"}
	first, firstErr := cellstatProc(t, args...)
	pid := strconv.Itoa(first.Process.Pid) + "\n"
	waitFor(t, "pid file from the first cellstat", func() bool { p, _ := os.ReadFile(out + ".pid"); return string(p) == pid })

	const refused = "cellstat: another cellstat writes usage.jsonl; name a new one\n"
	second := func(when string, lines int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // a run that is not refused stops here
		defer cancel()
		var stdout, stderr bytes.Buffer
		if code := run(ctx, args, &stdout, &stderr); code != 2 || stderr.String() != refused || stdout.Len() > 0 {
			t.Errorf("%s: exit %d, stderr %q, %d bytes out; want 2 and %q", when, code, stderr.String(), stdout.Len(), refused)
		}
		if p, _ := os.ReadFile(out + ".pid"); string(p) != pid {
			t.Errorf("%s: the refused run left the pid file holding %q, want %q", when, p, pid)
		}
		if n := lineCount(out); n != lines {
			t.Errorf("%s: %d lines in the file, want the first cellstat's %d", when, n, lines)
		}
	}
	second("before the first line", 0)
	waitFor(t, "first line", func() bool { return lineCount(out) == 1 })
	second("after the first line", 1)

	first.Process.Signal(syscall.SIGTERM)
	if err := first.Wait(); err != nil || lineCount(out) != 2 {
		t.Fatalf("the first cellstat after TERM: %v, %d lines; want exit 0 and 2 lines; stderr %s", err, lineCount(out), firstErr)
	}
	// Runs started at once on a file with lines each hold the lock from taking it to reading the size, so they see
	// each other's lock; each must still be refused as not empty. 100 rounds of 4: with lockWait 0, 20 of 20 runs of
	// this test failed.
	const notEmpty = "cellstat: -out names a file that is not empty; name a new one\n"
	for round := 0; round < 100; round++ {
		start, codes, stderrs := make(chan struct{}), make([]int, 4), make([]bytes.Buffer, 4)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = run(context.Background(), args, &bytes.Buffer{}, &stderrs[i])
			}()
		}
		close(start)
		wg.Wait()
		for i, code := range codes {
			if code != 2 || stderrs[i].String() != notEmpty {
				t.Errorf("round %d, run %d of 4 at once on a file with lines, its lock free: exit %d, stderr %q; want 2 and %q",
					round, i, code, stderrs[i].String(), notEmpty)
			}
		}
	}
	if n := lineCount(out); n != 2 {
		t.Errorf("after the runs at once: %d lines, want 2", n)
	}
	var stderr bytes.Buffer
	os.Remove(filepath.Join(dir, "ps.delay"))
	if err := os.Truncate(out, 0); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run(context.Background(), append(args, "-once"), &bytes.Buffer{}, &stderr); code != 0 || lineCount(out) != 1 {
		t.Errorf("an emptied file, its lock free: exit %d, %d lines, stderr %q; want 0 and 1", code, lineCount(out), stderr.String())
	}
}

// Test 17: a TERM during a slow sample ends that sample, then one last line, though a tick is due by then: after the
// TERM, one docker call starts, the last line's, and it answers. Six runs, since select takes a ready case at random
// and the bug this guards took a due tick half the time.
func TestTermDuringSlowSample(t *testing.T) {
	root := devTree(t)
	cli, dir := fakeDocker(t)
	write(t, filepath.Join(dir, "ps.delay"), "0.05\n") // each sample 50 ms or more, against a 1 ms interval
	calls := filepath.Join(dir, "calls.log")
	for i := 0; i < 6; i++ {
		os.Remove(calls)
		out := filepath.Join(t.TempDir(), "usage.jsonl")
		p, stderr := cellstatProc(t, "-out", out, "-interval", "1ms", "-root", root, "-cli", cli.path, "-procs=false")
		waitFor(t, "second sample's docker call", func() bool { return lineCount(calls) >= 2 })
		before := lineCount(calls) // one sample takes 50 ms, and this is read within about 1 ms of its call
		start := time.Now()
		p.Process.Signal(syscall.SIGTERM)
		err := p.Wait()
		took := time.Since(start)
		after, n := lineCount(calls), lineCount(out)
		if err != nil || after != before+1 || n != after {
			t.Errorf("run %d: exit %v, docker calls %d before TERM and %d after, %d lines, %s from TERM to exit; want exit 0, "+
				"1 call after TERM and a line a call; stderr %s", i, err, before, after, n, took.Round(time.Millisecond), stderr)
			continue
		}
		b, _ := os.ReadFile(out)
		var last Line
		if err := json.Unmarshal(b[bytes.LastIndexByte(b[:len(b)-1], '\n')+1:], &last); err != nil || len(last.Errors) > 0 {
			t.Errorf("run %d: last line %s, %v; want one whose docker call answered", i, b, err)
		}
	}
}

// Run, as bin/obs usage runs it: -cli none runs no CLI, so the line says list failed and holds no container, as with no
// docker installed; ready is called once, after the pid file is written and before the first line, and not at all
// when -out is refused.
func TestRunNoCLIAndReady(t *testing.T) {
	root := devTree(t)
	var stdout, stderr bytes.Buffer
	readies := 0
	if code := Run(context.Background(), []string{"-once", "-root", root, "-cli", NoCLI, "-procs=false"}, &stdout,
		&stderr, func() { readies++ }); code != 0 || readies != 1 {
		t.Fatalf("-once -cli none: exit %d, ready %d times; want 0 and once; stderr %s", code, readies, stderr.String())
	}
	var l Line
	if err := json.Unmarshal(stdout.Bytes(), &l); err != nil || errorsOf(t, l) != "[list failed]" || len(l.Containers) != 0 {
		t.Errorf("-cli none: %s, %v; want errors [list failed] and no container", stdout.String(), err)
	}

	out := filepath.Join(t.TempDir(), "usage.jsonl")
	write(t, out, "x\n")
	readies = 0
	if code := Run(context.Background(), []string{"-out", out, "-root", root, "-cli", NoCLI}, &bytes.Buffer{},
		&bytes.Buffer{}, func() { readies++ }); code != 2 || readies != 0 {
		t.Errorf("a refused -out: exit %d, ready %d times; want 2 and never", code, readies)
	}

	os.Remove(out)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen string
	ready := func() {
		readies++
		p, err := os.ReadFile(out + ".pid")
		seen = fmt.Sprintf("pid file %q %v, %d lines", p, err, lineCount(out))
		cancel()
	}
	if code := Run(ctx, []string{"-out", out, "-interval", "1h", "-root", root, "-cli", NoCLI, "-procs=false"},
		&bytes.Buffer{}, &bytes.Buffer{}, ready); code != 0 || readies != 1 {
		t.Errorf("-out: exit %d, ready %d times; want 0 and once", code, readies)
	}
	if want := fmt.Sprintf("pid file %q <nil>, 0 lines", strconv.Itoa(os.Getpid())+"\n"); seen != want {
		t.Errorf("at ready: %s; want %s", seen, want)
	}
	if n := lineCount(out); n != 2 {
		t.Errorf("cancelled at ready: %d lines, want the first and the last", n)
	}
}
