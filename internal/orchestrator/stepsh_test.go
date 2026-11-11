package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// The real gate/step.sh and gate/run.sh on the fake gate (GATE_FAKE=1: the real bin/laug pool serve on a fake backend,
// fake items and a fake make harness-test), driven through runStep. A checkout without gate/step.sh fails them: a skip
// would let the 21-case parity matrix and these tests pass by running nothing. GATE_RESULTS, the locks and
// FAKE_GATE_DIR are in a temp dir, and the gate's pool listens on a free port from 17890 to 17989: make harness-test's
// gate cases take 17790 to 17889, and a real gate 7700.

// realGate is the checkout, its fake gate world, and the short plan of make harness-test's gate cases (gplan): deadline
// 30 s, kill grace 1 s, quiet 0 s, a poll each second, no wave_min_s, over the three-item plan steady, pl and kh.
func realGate(t *testing.T) (*GateActivities, GateInput) {
	t.Helper()
	repo := checkoutRoot(t)
	if !exists(filepath.Join(repo, "gate/step.sh")) {
		t.Fatalf("%s holds no gate/step.sh: the real gate's tests need the checkout's", repo)
	}
	if fi, err := os.Stat(filepath.Join(repo, "bin/laug")); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatal("bin/laug, whose pool serve is the gate's own pool, is not built: make build builds it, and make test-go")
	}
	for _, tool := range []string{"yq", "jq", "flock", "pgrep", "curl", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not on PATH", tool)
		}
	}
	w := t.TempDir()
	for _, d := range []string{"results", "fake", "omes/cmd/omes"} {
		if err := os.MkdirAll(filepath.Join(w, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := exec.Command("yq", `.deadline_s = 30 | .kill_grace_s = 1 | .lease_ttl_s = 120 | .quiet_s = 0 | .poll_s = 1 | .harness_timeout_s = 20
    | .barrier_timeout_s = 2 | .wave_min_s = 0`, filepath.Join(repo, "load/testdata/gate/plan.yaml")).Output()
	if err != nil {
		t.Fatalf("yq: %v", err)
	}
	if err := os.WriteFile(filepath.Join(w, "plan.yaml"), plan, 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil || len(head) < 7 {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	a := &GateActivities{Repo: repo, Identity: "laug-worker@" + string(head[:7])}
	in := GateInput{ServerTag: "1.31.0", Plan: filepath.Join(w, "plan.yaml"), Fake: true, Post: "local", Base: "main",
		OmesDir: filepath.Join(w, "omes"), Results: filepath.Join(w, "results"), Lock: filepath.Join(w, "step.lock"),
		HostLock: filepath.Join(w, "step-host.lock"), PoolListen: "127.0.0.1:" + freePort(t), Extra: map[string]string{"FAKE_GATE_DIR": filepath.Join(w, "fake")}}
	return a, in
}

// checkoutRoot is the checkout this package sits in: the first directory up from the test's own, internal/orchestrator,
// that holds go.mod.
func checkoutRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for !exists(filepath.Join(dir, "go.mod")) {
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory: the real gate's tests run in a laug checkout")
		}
		dir = parent
	}
	return dir
}

// ports are the ones freePort handed out and not yet given back: tests that run at once (the parity matrix runs 8
// gates) never get one port twice, though each gate's pool binds its own only later. A port goes back at its test's
// end, after the cleanups its caller registered later (they run first: stopPool, killResults), so go test -count=n
// never runs out. One pass hands out 50 of the 100, at most 8 held at once (4 matrix cases): 42 for the matrix, 4 for
// the lock test, one each for the 4 tests that run one gate.
//
// Each port is held for the host too, by holdPort, while this process has it. Two test processes at once (go test in
// two checkouts on one host) each saw a port free, and the second gate's preflight then found the first one's laugd
// on it. On the dev host, two checkouts ran TestGateMatchesRunSh at once. The gates refused with
// "something answers on 127.0.0.1:17892 (curl exit 0), a laugd from laug pool serve or another user's gate" and with
// "lease steady: laugd refused it (curl exit 52)".
var ports struct {
	sync.Mutex
	taken map[int]io.Closer
}

func freePort(t *testing.T) string {
	t.Helper()
	ports.Lock()
	defer ports.Unlock()
	if ports.taken == nil {
		ports.taken = map[int]io.Closer{}
	}
	for p := 17890; p <= 17989; p++ {
		if ports.taken[p] != nil {
			continue
		}
		hold, err := holdPort(p)
		if err != nil {
			continue // another process on this host holds it
		}
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			l.Close()
			ports.taken[p] = hold
			t.Cleanup(func() {
				ports.Lock()
				ports.taken[p].Close()
				delete(ports.taken, p)
				ports.Unlock()
			})
			return strconv.Itoa(p)
		}
		hold.Close()
	}
	t.Fatal("no free port from 17890 to 17989")
	return ""
}

// holdPort holds port p for this host: on Linux, a listener on the abstract Unix socket @laug-test-port-<p>, which no
// other process can bind until this one closes it or ends. On the dev host, with go 1.26.0, another
// process's bind got "address already in use". The name was free again after Close and after a kill -9 of the holder,
// and no file was made. Elsewhere, nothing is held.
func holdPort(p int) (io.Closer, error) {
	if runtime.GOOS != "linux" {
		return io.NopCloser(nil), nil
	}
	return net.Listen("unix", fmt.Sprintf("@laug-test-port-%d", p))
}

func portFree(listen string) bool {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// stopPool ends a gate's pool, bin/laug pool serve -pid-file none, that a failed run.sh leg left answering on listen.
// run.sh keeps no record, so it is found by its command line, which names the test's own port.
func stopPool(listen string) {
	if portFree(listen) {
		return
	}
	_ = exec.Command("pkill", "-TERM", "-f", "pool serve -pid-file none -listen "+regexp.QuoteMeta(listen)+"( |$)").Run()
}

// lockFree: flock -n <file> true exits 0 only when no process holds the lock.
func lockFree(p string) bool { return exec.Command("flock", "-n", p, "true").Run() == nil }

// pgrepGone runs pgrep with args until it finds nothing, 5 s at most, and says whether it did. A process sent KILL or
// TERM leaves pgrep -f's list once it has exited, and pgrep -g's only once its parent has reaped it: pgrep -g lists
// a zombie.
func pgrepGone(args ...string) bool {
	until := time.Now().Add(5 * time.Second)
	for exec.Command("pgrep", args...).Run() == nil {
		if time.Now().After(until) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// stateRecord reads a record from .state.json.
func stateRecord(t *testing.T, dir, key string) *ProcRecord {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".state.json"))), &m); err != nil {
		t.Fatal(err)
	}
	var r ProcRecord
	if err := json.Unmarshal(m[key], &r); err != nil || r.Pid <= 0 {
		t.Fatalf(".state.json's %s is no record: %s", key, m[key])
	}
	return &r
}

// killRecorded ends what a failed test left: every recorded process that is still the one recorded.
func killRecorded(dir string) {
	var recs []*ProcRecord
	if b, err := os.ReadFile(filepath.Join(dir, ".state.json")); err == nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) == nil {
			for _, k := range []string{"holder", "laugd", "harness", "poller", "wave"} {
				var r ProcRecord
				if json.Unmarshal(m[k], &r) == nil {
					recs = append(recs, &r)
				}
			}
		}
	}
	items, _ := filepath.Glob(filepath.Join(dir, "*", ".pid"))
	steps, _ := filepath.Glob(filepath.Join(dir, ".steps", "*.pid"))
	for _, p := range append(items, steps...) {
		if r, err := readRecord(p); err == nil {
			recs = append(recs, r)
		}
	}
	for _, r := range recs {
		if alive, _ := r.Check(); alive {
			_ = syscall.Kill(-r.Pid, syscall.SIGKILL)
			_ = syscall.Kill(r.Pid, syscall.SIGKILL)
		}
	}
}

func timeline(t *testing.T, dir string) []string {
	t.Helper()
	var steps []string
	sc := bufio.NewScanner(strings.NewReader(readFile(t, filepath.Join(dir, "timeline.jsonl"))))
	for sc.Scan() {
		var l struct {
			Step string `json:"step"`
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("timeline.jsonl: %q", sc.Text())
		}
		steps = append(steps, l.Step)
	}
	return steps
}

// drive runs the steps through runStep in Gate's order, as its main and judge path do with every answer as
// wanted; with toWave it stops before the wave.
func drive(t *testing.T, a *GateActivities, in GateInput, gid string, toWave bool) {
	t.Helper()
	dir := filepath.Join(in.Results, gid)
	step := func(verb, key string, want ...string) StepAnswer {
		t.Helper()
		ans, err := a.runStep(context.Background(), stepIn(in, gid, verb, key), "steps-test", "run-1", noBeat)
		if err != nil {
			t.Fatalf("step %s: %v\n%s", StepName(verb, key), err, readFile(t, filepath.Join(dir, ".steps", StepName(verb, key)+".log")))
		}
		for _, w := range want {
			if ans.Result == w {
				return ans
			}
		}
		t.Fatalf("step %s answered %s, want %v\n%s", ans.Name, ans.Answer, want, readFile(t, filepath.Join(dir, ".steps", ans.Name+".log")))
		return ans
	}
	step("open", "", "next")
	d := dataOf(step("preflight", "", "next"))
	if len(d.Keys) != 3 || d.QuietS == nil {
		t.Fatalf("preflight's keys %v, quiet_s %v", d.Keys, d.QuietS)
	}
	for _, v := range []string{"omes", "laugd", "harness"} {
		step(v, "", "next")
	}
	for _, k := range d.Keys {
		if step("lease", k, "next", "stop-leasing").Result == "stop-leasing" {
			break
		}
	}
	step("barrier", "", "next")
	i := 0
	quiet := func() bool {
		q := dataOf(step("quiet", strconv.Itoa(i), "next")).Quiet
		i++
		return q != nil && *q
	}
	for {
		if quiet() {
			time.Sleep(secs(*d.QuietS))
			if quiet() {
				break
			}
		}
		time.Sleep(time.Second)
	}
	step("wave-time", "", "next")
	if toWave {
		return
	}
	step("wave", "", "done")
	step("combine", "", "done")
	step("post", "", "done")
	step("release", "", "done")
}

func gateJSON(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("jq", "-S", "del(.gate_id, .wiring, .driver, .workflow_id, .worker_commit)", filepath.Join(dir, "gate.json")).Output()
	if err != nil {
		t.Fatalf("jq on %s/gate.json: %v", dir, err)
	}
	return string(out)
}

func TestStepsMatchRunSh(t *testing.T) {
	a, in := realGate(t)
	start := time.Now()
	ca, err := a.check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	dirA := filepath.Join(in.Results, ca.GateID)
	t.Cleanup(func() { killRecorded(dirA) })
	if ca.Dir != "(outside the repository)/"+ca.GateID {
		t.Fatalf("check's dir %q is not shown", ca.Dir)
	}
	drive(t, a, in, ca.GateID, false)

	// After release: both locks free, the holder gone, laugd gone.
	if s := stateRecord(t, dirA, "holder"); func() bool { alive, _ := s.Check(); return alive }() {
		t.Fatalf("the holder (pid %d) outlived release", s.Pid)
	}
	if !lockFree(in.Lock) || !lockFree(in.HostLock) {
		t.Fatal("a lock is still held after release")
	}
	var st struct {
		Step string `json:"step"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dirA, ".state.json"))), &st); err != nil || st.Step != "release" {
		t.Fatalf(".state.json's step %q, want release", st.Step)
	}
	if !portFree(in.PoolListen) {
		t.Fatalf("laugd still listens on %s after release", in.PoolListen)
	}
	stepped := time.Since(start)

	// gate/run.sh on the same environment, in a session of its own. Its timeout sends TERM, not KILL, so that its EXIT
	// trap stops its items and its pool; a pool left anyway is stopped by stopPool.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "env", "--default-signal=INT,QUIT", "gate/run.sh")
	cmd.Dir, cmd.Env = a.Repo, in.Env(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Minute
	t.Cleanup(func() { stopPool(in.PoolListen) })
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate/run.sh: %v\n%s", err, out)
	}
	runs, _ := filepath.Glob(filepath.Join(in.Results, "g*"))
	dirB := ""
	for _, r := range runs {
		if r != dirA {
			dirB = r
		}
	}
	if len(runs) != 2 || dirB == "" {
		t.Fatalf("results hold %v, want the two runs", runs)
	}
	cmp := exec.Command(filepath.Join(a.Repo, "bin", "laug"), "gate", "compare", dirA, dirB)
	cmp.Dir = a.Repo
	if out, err := cmp.CombinedOutput(); err != nil || !strings.HasSuffix(strings.TrimSpace(string(out)), ": equal") {
		t.Fatalf("bin/laug gate compare step.sh's run against run.sh's: %v\n%s", err, out)
	}
	for _, f := range []string{"plan.json", "thresholds.json"} {
		if !bytes.Equal([]byte(readFile(t, filepath.Join(dirA, f))), []byte(readFile(t, filepath.Join(dirB, f)))) {
			t.Errorf("%s differs between the two runs", f)
		}
	}
	if ga, gb := gateJSON(t, dirA), gateJSON(t, dirB); ga != gb {
		t.Errorf("gate.json differs beyond its id, wiring and driver keys:\n step.sh %s\n run.sh  %s", ga, gb)
	}
	// Both drivers judge with bin/laug judge -write: each gate.json records its identity last in inputs (gate/lib.sh,
	// judge_input), and each item directory holds an empty .item, so git keeps one that stays empty.
	id, err := exec.Command(filepath.Join(a.Repo, "bin", "laug"), "judge", "-identity", "-json").Output()
	if err != nil {
		t.Fatalf("bin/laug judge -identity -json: %v", err)
	}
	for _, d := range []string{dirA, dirB} {
		cmd := exec.Command("jq", "-c", `.inputs[-1] | [.path, .version, .sha256, .go]`, filepath.Join(d, "gate.json"))
		got, err := cmd.Output()
		wantCmd := exec.Command("jq", "-c", `["internal/judge", .version, .sha256, .go]`)
		wantCmd.Stdin = bytes.NewReader(id)
		w, werr := wantCmd.Output()
		if err != nil || werr != nil || !bytes.Equal(got, w) {
			t.Errorf("%s/gate.json's last input: %s, want %s (%v, %v)", filepath.Base(d), got, w, err, werr)
		}
		for _, k := range []string{"steady", "pl", "kh"} {
			if fi, err := os.Stat(filepath.Join(d, k, ".item")); err != nil || fi.Size() != 0 {
				t.Errorf("%s/%s/.item: %v", filepath.Base(d), k, err)
			}
		}
	}
	t.Logf("step.sh's run in %s, run.sh's in %s", stepped.Round(100*time.Millisecond), (time.Since(start) - stepped).Round(100*time.Millisecond))
}

func TestStopTheFakeWave(t *testing.T) {
	a, in := realGate(t)
	in.Extra["FAKE_GATE_HANG"], in.Extra["FAKE_GATE_STUCK"] = "kh", "pl"
	ca, err := a.check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	gid := ca.GateID
	dir := filepath.Join(in.Results, gid)
	t.Cleanup(func() { killRecorded(dir) })
	drive(t, a, in, gid, true)

	wave := stepIn(in, gid, "wave", "")
	ctx, cancel := context.WithCancelCause(context.Background())
	got := make(chan error, 1)
	go func() { _, err := a.runStep(ctx, wave, "stop-wave", "run-1", noBeat); got <- err }()
	fake := in.Extra["FAKE_GATE_DIR"]
	waitFor(t, "kh and pl started", func() bool {
		return exists(filepath.Join(fake, "started.kh")) && exists(filepath.Join(fake, "started.pl"))
	})
	cancel(temporal.NewCanceledError())
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runStep after a cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runStep did not return within 1 s of the cancel")
	}
	rec, err := readRecord(filepath.Join(dir, ".steps/wave.pid"))
	if err != nil {
		t.Fatal(err)
	}
	if alive, why := rec.Check(); !alive {
		t.Fatalf("the wave ended with its activity's context (%s)", why)
	}

	res, err := a.stop(context.Background(), wave, noBeat, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != "TERM" || !res.Done || res.Exit == nil || *res.Exit != 143 {
		t.Fatalf("stop: %+v", res)
	}
	if d, err := readDone(filepath.Join(dir, ".steps/wave.done")); err != nil || string(d.Answer) != `{"result":"stopped"}` {
		t.Fatalf("wave.done: %+v, %v", d, err)
	}
	// What each item left. The stop path waits for the item itself, its group's leader: the wave reaps it and writes
	// exit.json before .done (gate/lib.sh, watch_items). The rest of its group gets the leader's signals (TERM, and
	// KILL for pl; signal_live), a KILLed item's session a TERM (sweep_item), and nothing waits for them, so both
	// pgrep looks poll. pgrep -f matches this gate's own lease path, its results directory in it, as step.sh names it
	// (pwd -P). A gate id is the second the gate started, so a gate in another test process that started in the same
	// second has the same <gid>/<key>/.lease.json. Seen: two copies of this test in one second, and the earlier one
	// found the later one's pl, still running before that gate's KILL.
	phys, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"kh": `[143,"TERM"]`, "pl": `[137,"KILL"]`} {
		out, err := exec.Command("jq", "-c", "[.exit, .stopped_by]", filepath.Join(dir, k, "exit.json")).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("%s/exit.json: %s, %v; want %s", k, out, err, want)
		}
		r, err := readRecord(filepath.Join(dir, k, ".pid"))
		if err != nil {
			t.Fatal(err)
		}
		if alive, _ := r.Check(); alive || r.Pgid != r.Pid {
			t.Errorf("%s: its record %+v still passes the guard, or its pgid is not its pid", k, r)
		}
		if !pgrepGone("-g", strconv.Itoa(r.Pgid)) {
			t.Errorf("%s: pgrep -g %d finds a process", k, r.Pgid)
		}
		if !pgrepGone("-f", regexp.QuoteMeta(filepath.Join(phys, k, ".lease.json"))) {
			t.Errorf("%s: pgrep -f %s/%s/.lease.json finds a process", k, gid, k)
		}
	}
	if out, err := exec.Command("jq", "-r", ".partial", filepath.Join(dir, "kh", gid+"-kh.json")).Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Errorf("kh's partial JSON: %s, %v", out, err)
	}
	if alive, _ := stateRecord(t, dir, "poller").Check(); alive {
		t.Error("the poller outlived the wave's TERM")
	}

	// The stop path: stopped, post stopped, release stopped.
	for _, s := range []struct{ verb, key string }{{"stopped", "the workflow was cancelled"}, {"post", "stopped"}, {"release", "stopped"}} {
		ans, err := a.runStep(context.Background(), stepIn(in, gid, s.verb, s.key), "stop-wave", "run-1", noBeat)
		if err != nil || ans.Result != "done" {
			t.Fatalf("%s %s: %+v, %v", s.verb, s.key, ans, err)
		}
		if s.verb == "stopped" {
			d := dataOf(ans)
			var at struct {
				StoppedAt string `json:"stopped_at"`
			}
			_ = json.Unmarshal(ans.Answer, &at)
			if at.StoppedAt != "wave" || verdictOf(d) != "BLOCK" || ans.Exit != 1 || !d.Trap {
				t.Fatalf("stopped answered %s", ans.Answer)
			}
		}
	}
	if out, err := exec.Command("jq", "-c", "[.verdict, .stopped_at]", filepath.Join(dir, "verdict.json")).Output(); err != nil || strings.TrimSpace(string(out)) != `["BLOCK","wave"]` {
		t.Errorf("verdict.json: %s, %v", out, err)
	}
	if out, err := exec.Command("jq", "-r", ".laugd", filepath.Join(dir, "cleanup.json")).Output(); err != nil || strings.TrimSpace(string(out)) != "stopped" {
		t.Errorf("cleanup.json's laugd: %s, %v", out, err)
	}
	tl := strings.Join(timeline(t, dir), ",")
	for _, want := range []string{"watchdog TERM", "watchdog KILL", "ended kh", "ended pl", "laugd stopped"} {
		if !strings.Contains(","+tl+",", ","+want+",") {
			t.Errorf("timeline.jsonl lacks %q: %s", want, tl)
		}
	}
	if strings.Contains(","+tl+",", ",done,") {
		t.Errorf("timeline.jsonl has a done event after a stop: %s", tl)
	}
	if !portFree(in.PoolListen) {
		t.Errorf("laugd still listens on %s", in.PoolListen)
	}
}

// stepWrap wraps the Step activity a gate leg registers; it may use the env, to cancel the workflow.
type stepWrap func(*testsuite.TestWorkflowEnvironment, func(context.Context, StepInput) (StepAnswer, error)) func(context.Context, StepInput) (StepAnswer, error)

// gateLeg runs workflow Gate in the SDK's test env on the real gate/step.sh: StartRun, then Gate with a.Step, a.Stop
// and a.AwaitItem registered as Step, Stop and AwaitItem; wrap, when set, wraps Step. The env's mock clock starts at
// wall time, close to the real t0 (SDK v1.49.0 setStartTime of the test env), and runs
// ahead by the Sleeps it skips (gate.go, wave).
func gateLeg(t *testing.T, a *GateActivities, in GateInput, wrap stepWrap) (GateResult, string) {
	t.Helper()
	t.Cleanup(func() { killResults(in.Results) })
	res, dir, err := runGateLeg(a, in, wrap)
	if err != nil {
		t.Fatal(err)
	}
	return res, dir
}

// runGateLeg is gateLeg for a goroutine of its own: it reports by its error, never by t.
func runGateLeg(a *GateActivities, in GateInput, wrap stepWrap) (GateResult, string, error) {
	ca, err := StartRun(context.Background(), a.Repo, in, nil)
	if err != nil {
		return GateResult{}, "", fmt.Errorf("StartRun: %w", err)
	}
	dir := filepath.Join(in.Results, ca.GateID)
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(2 * time.Minute)
	step := a.Step
	if wrap != nil {
		step = wrap(env, a.Step)
	}
	env.RegisterWorkflow(Gate)
	env.RegisterActivityWithOptions(step, activity.RegisterOptions{Name: "Step"})
	env.RegisterActivityWithOptions(a.Stop, activity.RegisterOptions{Name: "Stop"})
	env.RegisterActivityWithOptions(a.AwaitItem, activity.RegisterOptions{Name: "AwaitItem"})
	env.ExecuteWorkflow(Gate, GateRunInput{GateInput: in, GID: ca.GateID, Dir: ca.Dir})
	if !env.IsWorkflowCompleted() {
		return GateResult{}, dir, fmt.Errorf("Gate did not complete in %s", dir)
	}
	if err := env.GetWorkflowError(); err != nil {
		return GateResult{}, dir, fmt.Errorf("Gate failed instead of completing: %w", err)
	}
	var res GateResult
	if err := env.GetWorkflowResult(&res); err != nil {
		return GateResult{}, dir, err
	}
	return res, dir, nil
}

// killResults ends what a failed test left in any gate run under results.
func killResults(results string) {
	runs, _ := filepath.Glob(filepath.Join(results, "g*"))
	for _, d := range runs {
		killRecorded(d)
	}
}

// TestReleaseByHand: a gate left mid-wave, as a terminate leaves one (its wave step running, its activity gone), and
// laug gate stop's ReleaseByHand: Stop sends the wave TERM, stopped writes the trap verdict, release stops
// laugd and the holder. A second run answers from the .done files and changes nothing.
func TestReleaseByHand(t *testing.T) {
	a, in := realGate(t)
	in.Extra["FAKE_GATE_HANG"] = "kh"
	ca, err := a.check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	gid := ca.GateID
	dir := filepath.Join(in.Results, gid)
	t.Cleanup(func() { killRecorded(dir) })
	drive(t, a, in, gid, true)
	ctx, cancel := context.WithCancelCause(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := a.runStep(ctx, stepIn(in, gid, "wave", ""), "release-by-hand", "run-1", noBeat)
		got <- err
	}()
	waitFor(t, "kh started", func() bool { return exists(filepath.Join(in.Extra["FAKE_GATE_DIR"], "started.kh")) })
	cancel(errors.New("the workflow was terminated"))
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("runStep after its context ended: %v", err)
	}

	var out bytes.Buffer
	exit, err := ReleaseByHand(context.Background(), a.Repo, in, gid, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stop wave: TERM, done, exit 143\n", "stopped: done, BLOCK, exit 1, stopped_at wave\n", "post stopped: exit 0\n",
		"release stopped: laugd stopped, 3 released, left none\n", "laug · released gate " + gid + " by hand: exit 1\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("ReleaseByHand's output lacks %q:\n%s", want, out.String())
		}
	}
	if exit != 1 {
		t.Fatalf("exit %d:\n%s", exit, out.String())
	}
	if out, err := exec.Command("jq", "-c", "[.verdict, .stopped_at]", filepath.Join(dir, "verdict.json")).Output(); err != nil || strings.TrimSpace(string(out)) != `["BLOCK","wave"]` {
		t.Errorf("verdict.json: %s, %v", out, err)
	}
	if !portFree(in.PoolListen) || !lockFree(in.Lock) || !lockFree(in.HostLock) {
		t.Fatal("laugd still listens, or a lock is still held, after the release by hand")
	}

	var again bytes.Buffer
	if exit, err := ReleaseByHand(context.Background(), a.Repo, in, gid, &again, nil); err != nil || exit != 1 ||
		!strings.Contains(again.String(), "no live step\n") || !strings.Contains(again.String(), "stopped: done, BLOCK, exit 1, stopped_at wave\n") {
		t.Fatalf("a second release by hand: exit %d, %v:\n%s", exit, err, again.String())
	}
}

// TestGateLostStep: the plan's lost step on the real step.sh. lease-pl's first attempt is KILLed with its group once it
// has marked lease pl, while its lease is under way (FAKE_GATE_CREATE_S=2), and the attempt ends with a retryable
// error, as a worker that died would leave it. The retry finds the .pid, no .done and a dead process: StepLost, not
// retried. The stop path runs: Stop sends nothing, stopped writes the trap verdict at the step .state.json names, lease
// pl, and release frees the port and both locks.
func TestGateLostStep(t *testing.T) {
	a, in := realGate(t)
	in.Extra["FAKE_GATE_CREATE_S"] = "2"
	var killed bool
	wrap := func(_ *testsuite.TestWorkflowEnvironment, step func(context.Context, StepInput) (StepAnswer, error)) func(context.Context, StepInput) (StepAnswer, error) {
		return func(ctx context.Context, si StepInput) (StepAnswer, error) {
			if si.Name != "lease-pl" || activity.GetInfo(ctx).Attempt != 1 {
				return step(ctx, si)
			}
			done := make(chan error, 1)
			go func() { _, err := step(ctx, si); done <- err }()
			// The .pid comes first (gate/step.sh, before its verb), the mark lease pl later (verb_lease), then the
			// lease's curl, about 2 s: the KILL waits for the mark, so it lands during the curl.
			dir := a.abs(si.Dir)
			var rec *ProcRecord
			marked := func() bool {
				var st struct {
					Step string `json:"step"`
				}
				b, err := os.ReadFile(filepath.Join(dir, ".state.json"))
				return err == nil && json.Unmarshal(b, &st) == nil && st.Step == "lease pl"
			}
			for until := time.Now().Add(10 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
				if rec, _ = readRecord(filepath.Join(dir, ".steps", "lease-pl.pid")); rec != nil && marked() {
					break
				}
			}
			if rec == nil || !marked() {
				return StepAnswer{}, temporal.NewNonRetryableApplicationError("lease-pl wrote no .pid or no mark in 10 s", "TestSetup", nil)
			}
			_ = syscall.Kill(-rec.Pid, syscall.SIGKILL)
			<-done
			killed = true
			return StepAnswer{}, temporal.NewApplicationError("the test KILLed lease-pl's group", "TestKill")
		}
	}
	res, dir := gateLeg(t, a, in, wrap)
	if !killed {
		t.Fatal("lease-pl's first attempt never ran")
	}
	if res.End != "stopped" || res.Exit != 1 || res.VerdictWord != "BLOCK" || res.Stop == nil || res.Stop.Name != "lease-pl" || res.Stop.Sent != "" {
		t.Fatalf("result %+v, stop %+v", res, res.Stop)
	}
	var names []string
	for _, s := range res.Steps {
		names = append(names, strings.TrimSpace(s.Name+" "+map[bool]string{true: s.Key}[s.Name == "post" || s.Name == "release" || s.Name == "stopped"]))
	}
	if got := strings.Join(names, ", "); !strings.HasSuffix(got, "lease-steady, stopped step lease-pl: StepLost, post stopped, release stopped") {
		t.Fatalf("steps %s", got)
	}
	if out, err := exec.Command("jq", "-c", "[.verdict, .stopped_at]", filepath.Join(dir, "verdict.json")).Output(); err != nil || strings.TrimSpace(string(out)) != `["BLOCK","lease pl"]` {
		t.Errorf("verdict.json: %s, %v", out, err)
	}
	if !portFree(in.PoolListen) || !lockFree(in.Lock) || !lockFree(in.HostLock) {
		t.Fatal("laugd still listens, or a lock is still held, after the stop path")
	}
}
