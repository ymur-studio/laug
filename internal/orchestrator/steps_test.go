package orchestrator

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
)

const fakeGID = "g20260101-000001"

// stepGate is a checkout for the fake step.sh: a temp dir as the repo, the fake by its absolute path, results under
// the repo, and the gate run made by check.
func stepGate(t *testing.T, knobs ...string) (*GateActivities, GateInput) {
	t.Helper()
	script, err := filepath.Abs("testdata/fake-step.sh")
	if err != nil {
		t.Fatal(err)
	}
	a := &GateActivities{Repo: t.TempDir(), StepScript: script, Identity: "laug-worker@abc1234"}
	in := withFake(GateInput{ServerTag: "1.31.0", Plan: "gate/plan.yaml", Post: "local", Base: "main", OmesDir: ".upstream/omes",
		Results: "results", Lock: "gate/.lock", HostLock: "/tmp/laug-gate-test.lock", PoolListen: "127.0.0.1:17899"}, knobs...)
	if err := os.MkdirAll(filepath.Join(a.Repo, "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.check(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	return a, in
}

func stepIn(in GateInput, gid, verb, key string) StepInput {
	return StepInput{GateInput: in, Dir: path.Join(in.Results, gid), GID: gid, Name: StepName(verb, key), Verb: verb, Key: key}
}

// stepBeats collects heartbeats.
type stepBeats struct {
	sync.Mutex
	hbs []StepHeartbeat
}

func (b *stepBeats) record(hb StepHeartbeat) { b.Lock(); b.hbs = append(b.hbs, hb); b.Unlock() }
func (b *stepBeats) first() (StepHeartbeat, bool) {
	b.Lock()
	defer b.Unlock()
	if len(b.hbs) == 0 {
		return StepHeartbeat{}, false
	}
	return b.hbs[0], true
}

func noBeat(StepHeartbeat) {}

// appErr unwraps a non-retryable application error of one type.
func appErr(t *testing.T, err error, typ string) *temporal.ApplicationError {
	t.Helper()
	var ae *temporal.ApplicationError
	if !errors.As(err, &ae) || ae.Type() != typ || !ae.NonRetryable() {
		t.Fatalf("want a non-retryable %s, got %v", typ, err)
	}
	return ae
}

// waitFor polls until f is true, 10 s at most.
func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: not within 10 s", what)
}

func alive(pid int) bool {
	err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run()
	return err == nil
}

func TestShownPaths(t *testing.T) {
	a := &GateActivities{Repo: t.TempDir()}
	for _, c := range []struct{ in, want string }{
		{"gate/results/g1", "gate/results/g1"},
		{filepath.Join(a.Repo, "orchestrator/run/x.out"), "orchestrator/run/x.out"},
		{"/elsewhere/results/g1", "(outside the repository)/g1"},
		{"../laug-bad/gate/results/g1", "(outside the repository)/g1"},
	} {
		if got := a.shown(c.in); got != c.want {
			t.Errorf("shown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func faultOf(t *testing.T, ae *temporal.ApplicationError) StepFault {
	t.Helper()
	var f StepFault
	if err := ae.Details(&f); err != nil {
		t.Fatalf("details: %v", err)
	}
	return f
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// pidFile reads a pid a fake wrote, waiting for it.
func pidFile(t *testing.T, p string) int {
	t.Helper()
	var pid int
	waitFor(t, "pid in "+filepath.Base(p), func() bool {
		b, err := os.ReadFile(p)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	return pid
}

func selfRecord(t *testing.T, pid int) ProcRecord {
	t.Helper()
	_, start, ok := procStat(pid)
	if !ok {
		t.Fatalf("no /proc entry for pid %d", pid)
	}
	return ProcRecord{Pid: pid, Start: &start, BootID: bootID()}
}

func TestStepName(t *testing.T) {
	for _, c := range []struct{ verb, key, want string }{
		{"open", "", "open"}, {"lease", "pl", "lease-pl"}, {"quiet", "3", "quiet-3"}, {"combine", "", "judge"},
		{"post", "stopped", "post"}, {"release", "stopped", "release"}, {"stopped", "the workflow was cancelled", "stopped"},
		{"wave-time", "", "wave-time"},
	} {
		if got := StepName(c.verb, c.key); got != c.want {
			t.Errorf("StepName(%q, %q) = %q, want %q", c.verb, c.key, got, c.want)
		}
	}
	in := GateInput{Results: "gate/results"}
	ok := stepIn(in, "g20261016-120000", "lease", "pl")
	if err := ok.valid(); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]StepInput{
		"check is no step":    stepIn(in, "g20261016-120000", "check", ""),
		"name not the verb's": func() StepInput { s := ok; s.Name = "lease-kh"; return s }(),
		"lease key":           stepIn(in, "g20261016-120000", "lease", "PL"),
		"quiet key":           stepIn(in, "g20261016-120000", "quiet", "01"),
		"post key":            stepIn(in, "g20261016-120000", "post", "x"),
		"stopped, no why":     stepIn(in, "g20261016-120000", "stopped", ""),
		"stopped, two lines":  stepIn(in, "g20261016-120000", "stopped", "a\nb"),
		"stopped, 301 bytes":  stepIn(in, "g20261016-120000", "stopped", strings.Repeat("x", 301)),
		"open with a key":     stepIn(in, "g20261016-120000", "open", "x"),
		"gate id":             stepIn(in, "../etc", "open", ""),
		"dir not results/gid": func() StepInput { s := ok; s.Dir = "elsewhere/g20261016-120000"; return s }(),
	} {
		if bad.valid() == nil {
			t.Errorf("%s: %+v passed", name, bad)
		}
	}
}

// lib.sh's unset list, as gate_env runs it: from "unset THRESHOLDS" to the first line without a trailing backslash.
func libKnobs(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(checkoutRoot(t), "gate", "lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "unset THRESHOLDS ") {
			in = true
			l = strings.TrimPrefix(l, "unset ")
		}
		if !in {
			continue
		}
		cont := strings.HasSuffix(l, `\`)
		names = append(names, strings.Fields(strings.TrimSuffix(l, `\`))...)
		if !cont {
			break
		}
	}
	return names
}

func TestStepEnv(t *testing.T) {
	knobs := libKnobs(t)
	if len(knobs) != 41 {
		t.Fatalf("gate/lib.sh unsets %d item knobs, want 41: %v", len(knobs), knobs)
	}
	a, b := append([]string(nil), knobs...), append([]string(nil), itemKnobs...)
	sort.Strings(a)
	sort.Strings(b)
	if strings.Join(a, " ") != strings.Join(b, " ") {
		t.Fatalf("itemKnobs and gate/lib.sh's unset list differ:\n lib.sh %v\n  Go    %v", a, b)
	}
	in := GateInput{ServerTag: "1.31.0", Plan: "p.yaml", Items: "2", Post: "local", Base: "main", OmesDir: "o", Results: "r",
		Lock: "l", HostLock: "h", PoolListen: "127.0.0.1:7700", Fake: true, Extra: map[string]string{"FAKE_GATE_ITEM_S": "3", "GATE_BACKEND": "", "OTHER": "x"}}
	environ := []string{"PATH=/bin", "HOME=/h", "TEMPORAL_ADDRESS=127.0.0.1:7400", "FAKE_GATE_HANG=pl", "GATE_LOCK=/elsewhere",
		"GATE_WORKFLOW_ID=outer", "LAUG_FI_X=1", "MAKEFLAGS=-s", "SERVER_TAG=9.9.9"}
	for _, k := range knobs {
		environ = append(environ, k+"=from-the-worker")
	}
	got := in.StepEnv(environ, "gate-g20261016-120000", "0199-run", "abc1234-dirty")
	want := map[string]string{"PATH": "/bin", "HOME": "/h", "SERVER_TAG": "1.31.0", "PLAN": "p.yaml", "ITEMS": "2", "GATE_FAKE": "1",
		"GATE_POST": "local", "BASE": "main", "OMES_DIR": "o", "GATE_RESULTS": "r", "GATE_LOCK": "l", "GATE_HOST_LOCK": "h",
		"GATE_POOL_LISTEN": "127.0.0.1:7700", "FAKE_GATE_ITEM_S": "3", "GATE_BACKEND": "",
		"GATE_WORKFLOW_ID": "gate-g20261016-120000", "GATE_WORKFLOW_RUN_ID": "0199-run", "GATE_WORKER_COMMIT": "abc1234-dirty"}
	have := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s is set twice", k)
		}
		have[k] = v
	}
	for k, v := range want {
		if hv, ok := have[k]; !ok || hv != v {
			t.Errorf("%s = %q, %v; want %q", k, hv, ok, v)
		}
	}
	for k := range have {
		if _, ok := want[k]; !ok {
			t.Errorf("%s=%s leaked into step.sh's environment", k, have[k])
		}
	}
	for _, kv := range in.StepEnv(nil, "w", "r", "") {
		if strings.HasPrefix(kv, "GATE_WORKER_COMMIT=") {
			t.Errorf("an empty identity set %s", kv)
		}
	}
}

func TestProcRecordCheck(t *testing.T) {
	self := selfRecord(t, os.Getpid())
	if alive, why := self.Check(); !alive || why != "" {
		t.Fatalf("this process: %v, %q", alive, why)
	}
	later := *self.Start + 1
	moved := ProcRecord{Pid: self.Pid, Start: &later, BootID: self.BootID}
	if alive, why := moved.Check(); alive || !strings.Contains(why, fmt.Sprintf("pid %d started at %d, recorded %d", self.Pid, *self.Start, later)) {
		t.Fatalf("start + 1: %v, %q", alive, why)
	}
	reboot := ProcRecord{Pid: self.Pid, Start: self.Start, BootID: "00000000-0000-0000-0000-000000000000"}
	if alive, why := reboot.Check(); alive || !strings.Contains(why, "boot_id") {
		t.Fatalf("another boot: %v, %q", alive, why)
	}
	if alive, why := (ProcRecord{Pid: self.Pid, BootID: self.BootID}).Check(); alive || why != "" {
		t.Fatalf("start nil: %v, %q", alive, why)
	}
	// A child that exited and is not yet waited for: a zombie, which still answers kill -0.
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	waitFor(t, "the child is a zombie", func() bool { st, _, ok := procStat(cmd.Process.Pid); return ok && st == "Z" })
	z := selfRecord(t, cmd.Process.Pid)
	if syscall.Kill(z.Pid, 0) != nil {
		t.Fatal("the zombie does not answer kill -0; the reason for the state check is gone")
	}
	if alive, why := z.Check(); alive || why != "" {
		t.Fatalf("a zombie with its true start: %v, %q", alive, why)
	}
}

func TestCheckMakesTheRun(t *testing.T) {
	a, in := stepGate(t)
	ca, err := a.check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if ca.GateID != fakeGID || ca.Dir != "results/"+fakeGID {
		t.Fatalf("check answered %+v; its dir must come back shown", ca)
	}
	env := readFile(t, filepath.Join(a.Repo, "results", fakeGID, ".steps/check.env"))
	for _, want := range []string{"GATE_WORKFLOW_ID=\n", "GATE_WORKFLOW_RUN_ID=\n", "GATE_WORKER_COMMIT=abc1234\n", "GATE_RESULTS=results\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("check's environment lacks %q", want)
		}
	}
	in = withFake(in, "CHECK_EXIT=2")
	_, err = a.check(context.Background(), in)
	f := faultOf(t, appErr(t, err, "StepFailed"))
	if f.Exit != 2 || f.Why != "laug · gate/step.sh: fake check failed in results/"+fakeGID || strings.Contains(err.Error(), a.Repo) {
		t.Fatalf("a failed check: %+v, %v", f, err)
	}
}

// check and Step build step.sh's environment from StepEnv over the worker's own, never by inheriting it: a knob, a
// workflow id or an address that only the worker has reaches neither, and the fake's exit knobs, set in the worker
// alone, change nothing.
func TestStepsTakeNothingFromTheWorkersEnvironment(t *testing.T) {
	worker := map[string]string{"FAKE_GATE_CHECK_EXIT": "2", "FAKE_GATE_STEP_EXIT": "3", "GATE_RESULTS": "/elsewhere",
		"GATE_WORKFLOW_ID": "outer", "TEMPORAL_ADDRESS": "127.0.0.1:7400", "MAKEFLAGS": "-s", "PROJECT": "from-the-worker"}
	for k, v := range worker {
		t.Setenv(k, v)
	}
	a, in := stepGate(t) // check: it fails here if FAKE_GATE_CHECK_EXIT reached it
	ans, err := a.runStep(context.Background(), stepIn(in, fakeGID, "lease", "pl"), "wf-1", "run-1", noBeat)
	if err != nil || ans.Exit != 0 || ans.Result != "next" {
		t.Fatalf("a step under a worker with FAKE_GATE_STEP_EXIT set: %+v, %v", ans, err)
	}
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	for _, f := range []string{"check.env", "lease-pl.env"} {
		lines := map[string]bool{}
		for _, l := range strings.Split(readFile(t, filepath.Join(steps, f)), "\n") {
			lines[l] = true
		}
		for k, v := range worker {
			if lines[k+"="+v] {
				t.Errorf("%s: the worker's %s=%s reached step.sh", f, k, v)
			}
		}
		if !lines["GATE_RESULTS=results"] {
			t.Errorf("%s: GATE_RESULTS is not the gate's", f)
		}
	}
}

func TestRunStepAnswers(t *testing.T) {
	a, in := stepGate(t, `STEP_ANSWER={"result":"next","cell":"ab12cd34"}`)
	var b stepBeats
	ans, err := a.runStep(context.Background(), stepIn(in, fakeGID, "lease", "pl"), "wf-1", "run-1", b.record)
	if err != nil {
		t.Fatal(err)
	}
	if ans.Name != "lease-pl" || ans.Result != "next" || ans.Exit != 0 || string(ans.Answer) != `{"result":"next","cell":"ab12cd34"}` {
		t.Fatalf("answer %+v (%s)", ans, ans.Answer)
	}
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	rec, err := readRecord(filepath.Join(steps, "lease-pl.pid"))
	if err != nil {
		t.Fatal(err)
	}
	if hb, ok := b.first(); !ok || hb != (StepHeartbeat{Name: "lease-pl", Pid: rec.Pid}) {
		t.Fatalf("first beat %+v, %v; the .pid names %d", hb, ok, rec.Pid)
	}
	if out := readFile(t, filepath.Join(steps, "lease-pl.out")); out != string(ans.Answer)+"\n" {
		t.Fatalf(".out holds %q", out)
	}
	if !exists(filepath.Join(steps, "lease-pl.log")) || !exists(filepath.Join(steps, "lease-pl.done")) {
		t.Fatal("no .log or .done")
	}
	env := readFile(t, filepath.Join(steps, "lease-pl.env"))
	dir := filepath.Join(a.Repo, "results", fakeGID)
	if !strings.Contains(env, "argv: lease "+dir+" pl\n") {
		t.Errorf("step.sh got other arguments, or a relative dir:\n%s", env)
	}
	m := regexp.MustCompile(`(?m)^ids: ([0-9]+) ([0-9]+) ([0-9]+)$`).FindStringSubmatch(env)
	if m == nil || m[1] != m[2] || m[1] != m[3] || m[1] != strconv.Itoa(rec.Pid) {
		t.Errorf("step.sh is not the session and group leader Go started: pid pgid sid %v, .pid %d", m, rec.Pid)
	}
	for _, want := range []string{"GATE_WORKFLOW_ID=wf-1\n", "GATE_WORKFLOW_RUN_ID=run-1\n", "GATE_WORKER_COMMIT=abc1234\n", "FAKE_GATE_STEP_ANSWER="} {
		if !strings.Contains(env, want) {
			t.Errorf("step.sh's environment lacks %q", want)
		}
	}
}

func TestRunStepTakesADoneOrAPidAsFound(t *testing.T) {
	a, in := stepGate(t)
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	if err := os.WriteFile(filepath.Join(steps, "judge.done"), []byte(`{"exit":0,"answer":{"result":"done","verdict":"BLOCK","exit":1,"trap":false},"why":null,"at":null}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ans, err := a.runStep(context.Background(), stepIn(in, fakeGID, "combine", ""), "wf-1", "run-1", noBeat)
	if err != nil || ans.Name != "judge" || ans.Result != "done" || ans.Exit != 1 {
		t.Fatalf("a .done written first: %+v, %v", ans, err)
	}
	if exists(filepath.Join(steps, "judge.pid")) || exists(filepath.Join(steps, "judge.env")) {
		t.Fatal("a .done written first, and step.sh started anyway")
	}
	start := uint64(12345)
	rec := ProcRecord{Pid: 999999, Start: &start, BootID: bootID()}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(steps, "omes.pid"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = a.runStep(context.Background(), stepIn(in, fakeGID, "omes", ""), "wf-1", "run-1", noBeat)
	f := faultOf(t, appErr(t, err, "StepLost"))
	if f.Record == nil || f.Record.Pid != 999999 || f.Record.Start == nil || *f.Record.Start != 12345 || exists(filepath.Join(steps, "omes.env")) {
		t.Fatalf("a .pid with no .done: %+v", f)
	}
}

// Two calls on one step, both before either .pid: the first call's step.sh is still in its gate_env (STEP_PRE_S 1 s)
// when the second call starts one that writes its .pid at once. The first copy then exits 75, having written nothing,
// and its call adopts the second's step (rule 3, then rule 2): one run of the step, two answers. A call that comes
// once the .pid is there adopts at once: TestRunStepAdoptsTheStepItsLastAttemptLeft.
func TestRunStepTwoCallsOnOneName(t *testing.T) {
	a, in := stepGate(t, "STEP_PRE_S=1", "STEP_S=2")
	si := stepIn(in, fakeGID, "laugd", "")
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	type out struct {
		ans StepAnswer
		err error
	}
	var b1, b2 stepBeats
	first := make(chan out, 1)
	go func() {
		ans, err := a.runStep(context.Background(), si, "wf-1", "run-1", b1.record)
		first <- out{ans, err}
	}()
	waitFor(t, "the first step.sh's start", func() bool { return exists(filepath.Join(steps, "laugd.started")) })
	if exists(filepath.Join(steps, "laugd.pid")) {
		t.Fatal("the first step.sh wrote its .pid before the second call; the case needs it still in its gate_env")
	}
	second := si
	second.Extra = maps.Clone(in.Extra)
	second.GateInput = withFake(second.GateInput, "STEP_PRE_S=0")
	ans2, err2 := a.runStep(context.Background(), second, "wf-1", "run-2", b2.record)
	o1 := <-first
	if o1.err != nil || err2 != nil || o1.ans.Result != "next" || ans2.Result != "next" {
		t.Fatalf("first call %+v, %v; second call %+v, %v; want two answers", o1.ans, o1.err, ans2, err2)
	}
	hb1, _ := b1.first()
	hb2, _ := b2.first()
	t.Cleanup(func() { _ = syscall.Kill(-hb2.Pid, syscall.SIGKILL) })
	if s := readFile(t, filepath.Join(steps, "laugd.started")); s != fmt.Sprintf("%d\n%d\n", hb1.Pid, hb2.Pid) {
		t.Fatalf("laugd.started holds %q, want the first call's pid %d, then the second's %d", s, hb1.Pid, hb2.Pid)
	}
	rec, err := readRecord(filepath.Join(steps, "laugd.pid"))
	if err != nil || rec.Pid != hb2.Pid || !strings.Contains(readFile(t, filepath.Join(steps, "laugd.env")), "GATE_WORKFLOW_RUN_ID=run-2\n") {
		t.Fatalf("the .pid names %+v, %v; want the second call's step.sh, %d, the one that ran", rec, err, hb2.Pid)
	}
	// The first call beat its own step.sh's pid until it exited 75, then the adopted one's.
	beats := b1.all()
	if last := beats[len(beats)-1]; last != (StepHeartbeat{Name: "laugd", Pid: hb2.Pid}) {
		t.Fatalf("the first call's beats %+v end with %+v, want {laugd %d}: it did not adopt", beats, last, hb2.Pid)
	}
	for _, hb := range b2.all() {
		if hb.Pid != hb2.Pid {
			t.Fatalf("the second call beat %+v", hb)
		}
	}
	if exists(filepath.Join(steps, "laugd.signals")) {
		t.Fatal("a copy of the step got a signal")
	}
}

func TestRunStepFailures(t *testing.T) {
	a, in := stepGate(t, "STEP_EXIT=3")
	_, err := a.runStep(context.Background(), stepIn(in, fakeGID, "omes", ""), "wf-1", "run-1", noBeat)
	ae := appErr(t, err, "StepFailed")
	f := faultOf(t, ae)
	if f.Exit != 3 || f.Why != "gate/step.sh: fake failed in results/"+fakeGID || f.Log != "results/"+fakeGID+"/.steps/omes.log" {
		t.Fatalf("exit 3 with a .done: %+v", f)
	}
	if strings.Contains(ae.Message(), a.Repo) || strings.Contains(f.Why, a.Repo) {
		t.Fatalf("a home path reached the error: %s", ae.Message())
	}

	usage := filepath.Join(a.Repo, "usage.sh")
	if err := os.WriteFile(usage, []byte("#!/usr/bin/env bash\necho \"laug · gate/step.sh: $2 is not a gate run's directory\" >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := *a
	u.StepScript = usage
	_, err = u.runStep(context.Background(), stepIn(in, fakeGID, "laugd", ""), "wf-1", "run-1", noBeat)
	f = faultOf(t, appErr(t, err, "StepFailed"))
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	if f.Exit != 2 || f.Why != "laug · gate/step.sh: results/"+fakeGID+" is not a gate run's directory" || exists(filepath.Join(steps, "laugd.done")) {
		t.Fatalf("exit 2 with no .pid: %+v", f)
	}

	a, in = stepGate(t, "STEP_S=30")
	var b stepBeats
	got := make(chan error, 1)
	go func() {
		_, err := a.runStep(context.Background(), stepIn(in, fakeGID, "harness", ""), "wf-1", "run-1", b.record)
		got <- err
	}()
	waitFor(t, "a beat", func() bool { _, ok := b.first(); return ok })
	hb, _ := b.first()
	pidFile(t, filepath.Join(a.Repo, "results", fakeGID, ".steps/harness.sleep"))
	t.Cleanup(func() { _ = syscall.Kill(-hb.Pid, syscall.SIGKILL) })
	_ = syscall.Kill(hb.Pid, syscall.SIGKILL)
	f = faultOf(t, appErr(t, <-got, "StepFailed"))
	if f.Exit != 137 || exists(filepath.Join(a.Repo, "results", fakeGID, ".steps/harness.done")) {
		t.Fatalf("a KILLed step: %+v", f)
	}
}

func TestRunStepLeavesTheStepWhenItsContextEnds(t *testing.T) {
	a, in := stepGate(t, "STEP_S=30")
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	for i, cause := range []error{temporal.NewCanceledError("the workflow asked"), errors.New("heartbeat: connection refused")} {
		verb := []string{"omes", "laugd"}[i]
		ctx, cancel := context.WithCancelCause(context.Background())
		var b stepBeats
		got := make(chan error, 1)
		go func() { _, err := a.runStep(ctx, stepIn(in, fakeGID, verb, ""), "wf-1", "run-1", b.record); got <- err }()
		waitFor(t, "a beat", func() bool { _, ok := b.first(); return ok })
		hb, _ := b.first()
		t.Cleanup(func() { _ = syscall.Kill(-hb.Pid, syscall.SIGKILL) })
		pidFile(t, filepath.Join(steps, verb+".sleep"))
		cancel(cause)
		select {
		case err := <-got:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%v: runStep returned %v, want ctx.Err()", cause, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%v: runStep did not return within 1 s", cause)
		}
		time.Sleep(200 * time.Millisecond)
		if !alive(hb.Pid) || exists(filepath.Join(steps, verb+".signals")) || exists(filepath.Join(steps, verb+".done")) {
			t.Fatalf("%v: the step got a signal or ended (alive %v)", cause, alive(hb.Pid))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.runStep(ctx, stepIn(in, fakeGID, "harness", ""), "wf-1", "run-1", noBeat); !errors.Is(err, context.Canceled) || exists(filepath.Join(steps, "harness.pid")) {
		t.Fatalf("a context ended before the start: %v", err)
	}
}

func TestRunStepRefusesABadInputBeforeAnything(t *testing.T) {
	a, in := stepGate(t)
	bad := stepIn(in, fakeGID, "lease", "pl")
	bad.Name = "lease-kh"
	_, err := a.runStep(context.Background(), bad, "wf-1", "run-1", noBeat)
	appErr(t, err, "StepFailed")
	_, err = a.runStep(context.Background(), stepIn(in, "g20260101-000002", "open", ""), "wf-1", "run-1", noBeat)
	appErr(t, err, "StepFailed")
	if exists(filepath.Join(a.Repo, "results", "g20260101-000002")) || exists(filepath.Join(a.Repo, "results", fakeGID, ".steps/lease-kh.pid")) {
		t.Fatal("a bad input made a file")
	}
}

func TestRedact(t *testing.T) {
	a := &GateActivities{Repo: "/srv/c/laug"}
	in := GateInput{Results: "/tmp/x/results"}
	for _, c := range []struct{ in, want string }{
		{"line 3: /srv/c/laug/gate/step.sh failed", "line 3: gate/step.sh failed"},
		{"in /tmp/x/results/g20260101-000001: no", "in (outside the repository)/results/g20260101-000001: no"},
		{"cd /srv/c/laug: ok", "cd .: ok"},
		{"/srv/c/laug-other/x", "/srv/c/laug-other/x"},
	} {
		if got := a.redact(in, c.in); got != c.want {
			t.Errorf("redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Inside an answer, each path as JSON writes it: a quote in a name is escaped on both sides.
	q := GateInput{Results: `/tmp/x/re"s`}
	for _, c := range []struct {
		in   GateInput
		raw  string
		want string
	}{
		{in, `{"refused":"open /tmp/x/results/g1/.images.err: no; /srv/c/laug/cells/compose/cell.yml"}`,
			`{"refused":"open (outside the repository)/results/g1/.images.err: no; cells/compose/cell.yml"}`},
		{q, `{"refused":"in /tmp/x/re\"s/g1"}`, `{"refused":"in (outside the repository)/re\"s/g1"}`},
		{in, `{"result":"next","t0":{"wall":1792152000.120}}`, `{"result":"next","t0":{"wall":1792152000.120}}`},
	} {
		if got := string(a.redactAnswer(c.in, json.RawMessage(c.raw))); got != c.want {
			t.Errorf("redactAnswer(%s) = %s, want %s", c.raw, got, c.want)
		}
	}
}

// An answer's refused text can carry a command's stderr, and so a home path: the answer comes back redacted, with its
// key.
func TestRunStepRedactsTheAnswer(t *testing.T) {
	a, in := stepGate(t)
	dir := filepath.Join(a.Repo, "results", fakeGID)
	in = withFake(in, `STEP_ANSWER={"result":"refused","refused":"docker compose config failed: open `+dir+`/.images.err: no such file"}`)
	ans, err := a.runStep(context.Background(), stepIn(in, fakeGID, "lease", "pl"), "wf-1", "run-1", noBeat)
	want := `{"result":"refused","refused":"docker compose config failed: open results/` + fakeGID + `/.images.err: no such file"}`
	if err != nil || string(ans.Answer) != want || ans.Key != "pl" || ans.Result != "refused" {
		t.Fatalf("answer %s, key %q, %v; want %s", ans.Answer, ans.Key, err, want)
	}
}

// A ctx that ends before step.sh has written its .pid (its gate_env runs first): runStep returns once the .pid is
// there, so Stop finds the step and sends it TERM, where it would have answered "no .pid: the step never started".
func TestRunStepWaitsForThePidWhenItsContextEnds(t *testing.T) {
	a, in := stepGate(t, "STEP_PRE_S=0.5", "STEP_S=30")
	si := stepIn(in, fakeGID, "omes", "")
	pidPath := filepath.Join(a.Repo, "results", fakeGID, ".steps/omes.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var b stepBeats
	start := time.Now()
	// The cancel comes with the first beat, sent right after the start, as a cancel can reach an activity.
	_, err := a.runStep(ctx, si, "wf-1", "run-1", func(hb StepHeartbeat) { b.record(hb); cancel() })
	took := time.Since(start)
	hb, _ := b.first()
	t.Cleanup(func() { _ = syscall.Kill(-hb.Pid, syscall.SIGKILL) })
	if !errors.Is(err, context.Canceled) || took < 400*time.Millisecond || took > 3*time.Second || !exists(pidPath) {
		t.Fatalf("runStep returned %v after %s, the .pid there: %v; want ctx.Err() once the .pid is there", err, took.Round(time.Millisecond), exists(pidPath))
	}
	res, err := a.stop(context.Background(), si, noBeat, func(string) {})
	if err != nil || res.Sent != "TERM" {
		t.Fatalf("stop after runStep left: %+v, %v", res, err)
	}
	waitFor(t, "the step is gone", func() bool { st, _, ok := procStat(hb.Pid); return !ok || st == "Z" })
}

// runInBackground starts runStep on a step that runs on, and returns its pid and where its result comes.
func runInBackground(t *testing.T, a *GateActivities, si StepInput) (int, chan error) {
	t.Helper()
	var b stepBeats
	got := make(chan error, 1)
	go func() { _, err := a.runStep(context.Background(), si, "wf-1", "run-1", b.record); got <- err }()
	waitFor(t, "a beat", func() bool { _, ok := b.first(); return ok })
	hb, _ := b.first()
	t.Cleanup(func() { _ = syscall.Kill(-hb.Pid, syscall.SIGKILL) })
	pidFile(t, filepath.Join(a.abs(si.Dir), ".steps", si.Name+".sleep"))
	return hb.Pid, got
}

func TestStopSendsTERMToThePidOnly(t *testing.T) {
	a, in := stepGate(t, "STEP_S=30")
	si := stepIn(in, fakeGID, "wave", "")
	pid, got := runInBackground(t, a, si)
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	sleep := pidFile(t, filepath.Join(steps, "wave.sleep"))
	t.Cleanup(func() { _ = syscall.Kill(sleep, syscall.SIGKILL) })
	var b stepBeats
	res, err := a.stop(context.Background(), si, b.record, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != "TERM" || !res.Done || res.Exit == nil || *res.Exit != 143 || res.Why != "" {
		t.Fatalf("stop: %+v", res)
	}
	if hb, ok := b.first(); !ok || hb != (StepHeartbeat{Name: "wave", Pid: pid}) {
		t.Fatalf("stop's first beat %+v", hb)
	}
	if f := faultOf(t, appErr(t, <-got, "StepFailed")); f.Exit != 143 {
		t.Fatalf("the stopped step: %+v", f)
	}
	// The state, not kill -0, which a zombie answers: a sleep that TERM reached is a zombie until init reaps it.
	if st, _, ok := procStat(sleep); !ok || st == "Z" {
		t.Fatalf("TERM reached the step's group: its sleep is gone or a zombie (%q)", st)
	}
	if s := readFile(t, filepath.Join(steps, "wave.signals")); s != "TERM\n" {
		t.Fatalf("the step saw %q, want one TERM", s)
	}
}

func TestStopKillsTheGroupAfterTheGrace(t *testing.T) {
	a, in := stepGate(t, "STEP_S=30", "STEP_STUCK=1")
	a.StopMargin = time.Second
	dir := filepath.Join(a.Repo, "results", fakeGID)
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(`{"kill_grace_s": 0}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	si := stepIn(in, fakeGID, "barrier", "")
	pid, got := runInBackground(t, a, si)
	sleep := pidFile(t, filepath.Join(dir, ".steps/barrier.sleep"))
	start := time.Now()
	res, err := a.stop(context.Background(), si, noBeat, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < time.Second || d > 4*time.Second {
		t.Fatalf("KILL came %s after TERM, want 1 s", d)
	}
	if res.Sent != "TERM, then KILL" || res.Done || res.Exit != nil {
		t.Fatalf("stop: %+v", res)
	}
	if f := faultOf(t, appErr(t, <-got, "StepFailed")); f.Exit != 137 {
		t.Fatalf("the KILLed step: %+v", f)
	}
	waitFor(t, "the group is gone", func() bool { return !alive(pid) && !alive(sleep) })
	if s := readFile(t, filepath.Join(dir, ".steps/barrier.signals")); s != "TERM\n" {
		t.Fatalf("the step saw %q, want one TERM", s)
	}
}

func TestStopReturnsOnceTheStepIsGone(t *testing.T) {
	a, in := stepGate(t, "STEP_S=30", "STEP_STUCK=1", "STEP_NODONE=1")
	si := stepIn(in, fakeGID, "omes", "")
	pid, got := runInBackground(t, a, si)
	type out struct {
		res StopResult
		err error
	}
	stopped := make(chan out, 1)
	go func() { r, e := a.stop(context.Background(), si, noBeat, func(string) {}); stopped <- out{r, e} }()
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	waitFor(t, "the step saw TERM", func() bool { return exists(filepath.Join(steps, "omes.signals")) })
	_ = syscall.Kill(pid, syscall.SIGKILL)
	killed := time.Now()
	var o out
	select {
	case o = <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return within 1 s of the step's end")
	}
	if o.err != nil || o.res.Done || o.res.Sent != "TERM" || o.res.Why != "the step ended without .done" {
		t.Fatalf("stop after a KILL from outside: %+v, %v (%s)", o.res, o.err, time.Since(killed))
	}
	<-got
}

func TestStopSendsNothingToAnotherProcess(t *testing.T) {
	a, in := stepGate(t)
	sleep := exec.Command("sleep", "30")
	sleep.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	rec := selfRecord(t, sleep.Process.Pid)
	*rec.Start++
	b, _ := json.Marshal(rec)
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	if err := os.WriteFile(filepath.Join(steps, "omes.pid"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	var logged []string
	res, err := a.stop(context.Background(), stepIn(in, fakeGID, "omes", ""), noBeat, func(s string) { logged = append(logged, s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != "" || res.Done || !strings.HasPrefix(res.Why, fmt.Sprintf("guard: omes pid %d started at ", rec.Pid)) || len(logged) != 1 {
		t.Fatalf("a record of another start: %+v, logged %q", res, logged)
	}
	time.Sleep(100 * time.Millisecond)
	if st, _, ok := procStat(sleep.Process.Pid); !ok || st == "Z" {
		t.Fatal("the sleep under a wrong start got a signal")
	}
}

func TestStopRefusesTheEndAndReadsWhatIsThere(t *testing.T) {
	a, in := stepGate(t)
	for _, verb := range []string{"combine", "post", "release"} {
		_, err := a.stop(context.Background(), stepIn(in, fakeGID, verb, ""), noBeat, func(string) {})
		appErr(t, err, "StopRefused")
	}
	_, err := a.stop(context.Background(), stepIn(in, fakeGID, "stopped", "the workflow was cancelled"), noBeat, func(string) {})
	appErr(t, err, "StopRefused")
	res, err := a.stop(context.Background(), stepIn(in, fakeGID, "omes", ""), noBeat, func(string) {})
	if err != nil || res.Sent != "" || res.Done || res.Why != "no .pid: the step never started" {
		t.Fatalf("no .pid: %+v, %v", res, err)
	}
	if _, err := a.runStep(context.Background(), stepIn(in, fakeGID, "omes", ""), "wf-1", "run-1", noBeat); err != nil {
		t.Fatal(err)
	}
	res, err = a.stop(context.Background(), stepIn(in, fakeGID, "omes", ""), noBeat, func(string) {})
	if err != nil || res.Sent != "" || !res.Done || res.Exit == nil || *res.Exit != 0 {
		t.Fatalf("a step that is done: %+v, %v", res, err)
	}
	// Exit 0 carries the .done's answer, which the backstop's judge path reads (gate.go, stopPath).
	if ans, err := answerFrom("omes", res.Answer); err != nil || ans.Result == "" {
		t.Fatalf("a step that is done with exit 0: answer %s, %v", res.Answer, err)
	}
}

func TestStepOptions(t *testing.T) {
	o := StepOptions(2*time.Minute, true)
	r := o.RetryPolicy
	if o.StartToCloseTimeout != 2*time.Minute || o.ScheduleToCloseTimeout != 17*time.Minute || o.HeartbeatTimeout != 10*time.Second ||
		!o.WaitForCancellation || o.ScheduleToStartTimeout != 0 || r == nil || r.InitialInterval != time.Second || r.BackoffCoefficient != 2 ||
		r.MaximumInterval != 10*time.Second || r.MaximumAttempts != 0 || strings.Join(r.NonRetryableErrorTypes, " ") != "StepLost StepFailed StopRefused" {
		t.Fatalf("StepOptions = %+v, retry %+v", o, r)
	}
	if StepOptions(time.Minute, false).WaitForCancellation {
		t.Fatal("wait false gave WaitForCancellation true")
	}
}

// leaveRunning starts runStep on a step that runs on, then ends its ctx with a cause that is no cancel, as a failed
// heartbeat does: runStep returns ctx.Err() and the step runs on (rule 4). It returns the step's pid.
func leaveRunning(t *testing.T, a *GateActivities, si StepInput) int {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	var b stepBeats
	got := make(chan error, 1)
	go func() { _, err := a.runStep(ctx, si, "wf-1", "run-1", b.record); got <- err }()
	waitFor(t, "a beat", func() bool { _, ok := b.first(); return ok })
	hb, _ := b.first()
	t.Cleanup(func() { _ = syscall.Kill(-hb.Pid, syscall.SIGKILL) })
	pidFile(t, filepath.Join(a.abs(si.Dir), ".steps", si.Name+".sleep"))
	cancel(errors.New("heartbeat: connection refused"))
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the first runStep returned %v, want ctx.Err()", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the first runStep did not return within 1 s of its ctx's end")
	}
	if !alive(hb.Pid) {
		t.Fatalf("pid %d ended with the first runStep's ctx", hb.Pid)
	}
	return hb.Pid
}

// all is every beat so far.
func (b *stepBeats) all() []StepHeartbeat {
	b.Lock()
	defer b.Unlock()
	return append([]StepHeartbeat(nil), b.hbs...)
}

// A second runStep for the same step adopts the one the first left running: it beats with the recorded pid, starts
// nothing, and answers with the .done when the step ends.
func TestRunStepAdoptsTheStepItsLastAttemptLeft(t *testing.T) {
	a, in := stepGate(t, "STEP_S=3")
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	// The answer names the gate run by its absolute path, as a refused text can: it comes back redacted.
	in = withFake(in, `STEP_ANSWER={"result":"next","max_lease":"2h0m0.417123s","log":"`+steps+`/laugd.log"}`)
	si := stepIn(in, fakeGID, "laugd", "")
	pid := leaveRunning(t, a, si)
	var b stepBeats
	start := time.Now()
	ans, err := a.runStep(context.Background(), si, "wf-1", "run-2", b.record)
	took := time.Since(start)
	want := `{"result":"next","max_lease":"2h0m0.417123s","log":"results/` + fakeGID + `/.steps/laugd.log"}`
	if err != nil || ans.Name != "laugd" || ans.Result != "next" || string(ans.Answer) != want {
		t.Fatalf("the adopted step answered %+v (%s), %v; want %s", ans, ans.Answer, err, want)
	}
	if took < time.Second || took > 6*time.Second || !exists(filepath.Join(steps, "laugd.done")) {
		t.Fatalf("runStep returned after %s; want it to wait for the step's .done, about 3 s after the start", took.Round(time.Millisecond))
	}
	rec, err := readRecord(filepath.Join(steps, "laugd.pid"))
	if err != nil || rec.Pid != pid {
		t.Fatalf("the .pid names %+v, %v; the first runStep started pid %d", rec, err, pid)
	}
	hbs := b.all()
	// At once, then every 2 s: 2 beats in about 3 s.
	if len(hbs) < 2 {
		t.Fatalf("%d beats in %s, want one at once and one 2 s later", len(hbs), took.Round(time.Millisecond))
	}
	for _, hb := range hbs {
		if hb != (StepHeartbeat{Name: "laugd", Pid: pid}) {
			t.Fatalf("beat %+v, want {laugd %d}", hb, pid)
		}
	}
	if s := readFile(t, filepath.Join(steps, "laugd.started")); s != strconv.Itoa(pid)+"\n" {
		t.Fatalf("laugd.started holds %q: step.sh started more than once, or not as pid %d", s, pid)
	}
	if env := readFile(t, filepath.Join(steps, "laugd.env")); !strings.Contains(env, "GATE_WORKFLOW_RUN_ID=run-1\n") {
		t.Fatal("laugd.env is not the first start's")
	}
	if exists(filepath.Join(steps, "laugd.signals")) {
		t.Fatal("the adopted step got a signal")
	}
	t.Logf("adopted pid %d: %d beats, all {laugd %d}; laugd.started %q; answered after %s", pid, len(hbs), pid,
		strings.TrimSpace(readFile(t, filepath.Join(steps, "laugd.started"))), took.Round(time.Millisecond))
}

// An adopted step whose runStep's ctx ends is left as rule 4 leaves a step it started: ctx.Err(), nothing sent, the
// step running on. A ctx that ended before the adoption beats nothing.
func TestRunStepLeavesAnAdoptedStepWhenItsContextEnds(t *testing.T) {
	a, in := stepGate(t, "STEP_S=30")
	si := stepIn(in, fakeGID, "harness", "")
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	pid := leaveRunning(t, a, si)

	ended, end := context.WithCancel(context.Background())
	end()
	var b0 stepBeats
	if _, err := a.runStep(ended, si, "wf-1", "run-2", b0.record); !errors.Is(err, context.Canceled) || len(b0.all()) != 0 {
		t.Fatalf("a ctx ended before the adoption: %v, %d beats", err, len(b0.all()))
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	var b stepBeats
	got := make(chan error, 1)
	go func() { _, err := a.runStep(ctx, si, "wf-1", "run-3", b.record); got <- err }()
	waitFor(t, "a beat", func() bool { _, ok := b.first(); return ok })
	if hb, _ := b.first(); hb != (StepHeartbeat{Name: "harness", Pid: pid}) {
		t.Fatalf("the adoption's beat %+v, want {harness %d}", hb, pid)
	}
	cancel(temporal.NewCanceledError("the workflow asked"))
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runStep returned %v, want ctx.Err()", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runStep did not return within 1 s of its ctx's end")
	}
	time.Sleep(300 * time.Millisecond)
	if !alive(pid) || exists(filepath.Join(steps, "harness.signals")) || exists(filepath.Join(steps, "harness.done")) {
		t.Fatalf("the adopted step got a signal or ended (alive %v)", alive(pid))
	}
	if s := readFile(t, filepath.Join(steps, "harness.started")); s != strconv.Itoa(pid)+"\n" {
		t.Fatalf("harness.started holds %q, want one start, pid %d", s, pid)
	}
}

// An adopted step that ends without a .done (KILL, from outside) is StepLost, with its record.
func TestRunStepLosesAnAdoptedStepThatEndsWithoutADone(t *testing.T) {
	a, in := stepGate(t, "STEP_S=30")
	si := stepIn(in, fakeGID, "barrier", "")
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	pid := leaveRunning(t, a, si)
	var b stepBeats
	got := make(chan error, 1)
	go func() { _, err := a.runStep(context.Background(), si, "wf-1", "run-2", b.record); got <- err }()
	waitFor(t, "a beat", func() bool { _, ok := b.first(); return ok })
	_ = syscall.Kill(pid, syscall.SIGKILL)
	var err error
	select {
	case err = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("runStep did not return within 2 s of the adopted step's end")
	}
	ae := appErr(t, err, "StepLost")
	f := faultOf(t, ae)
	want := fmt.Sprintf("the adopted step ended without a .done; pid %d has ended", pid)
	if ae.Message() != "step barrier: "+want || f.Why != want || f.Record == nil || f.Record.Pid != pid || f.Record.Start == nil {
		t.Fatalf("StepLost %q, %+v", ae.Message(), f)
	}
	if exists(filepath.Join(steps, "barrier.done")) || exists(filepath.Join(steps, "barrier.signals")) {
		t.Fatal("a .done or a TERM after a KILL")
	}
}

// A .pid whose record fails the guard is StepLost at once, with the guard's why, and nothing is sent: another start
// time or another boot (the pid is a live fake step, which records any TERM), a zombie, a pid that has ended.
func TestRunStepAdoptsNothingTheGuardRefuses(t *testing.T) {
	a, in := stepGate(t, "STEP_S=30")
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	pid := leaveRunning(t, a, stepIn(in, fakeGID, "omes", ""))
	live := selfRecord(t, pid)
	later := *live.Start + 1
	other := "00000000-0000-0000-0000-000000000000"
	lostOn := func(verb string, rec ProcRecord, want string) {
		t.Helper()
		si := stepIn(in, fakeGID, verb, "")
		b, _ := json.Marshal(rec)
		if err := os.WriteFile(filepath.Join(steps, si.Name+".pid"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		var beats stepBeats
		_, err := a.runStep(context.Background(), si, "wf-1", "run-2", beats.record)
		ae := appErr(t, err, "StepLost")
		f := faultOf(t, ae)
		if ae.Message() != "step "+si.Name+": a .pid with no .done; "+want || f.Record == nil || f.Record.Pid != rec.Pid ||
			f.Record.Start == nil || *f.Record.Start != *rec.Start || f.Record.BootID != rec.BootID {
			t.Fatalf("%s: StepLost %q, %+v; want %q", verb, ae.Message(), f, want)
		}
		if n := len(beats.all()); n != 0 || exists(filepath.Join(steps, si.Name+".started")) {
			t.Fatalf("%s: %d beats, or a step.sh started", verb, n)
		}
	}
	lostOn("laugd", ProcRecord{Pid: pid, Start: &later, BootID: live.BootID},
		fmt.Sprintf("pid %d is another process (pid %d started at %d, recorded %d)", pid, pid, *live.Start, later))
	lostOn("harness", ProcRecord{Pid: pid, Start: live.Start, BootID: other},
		fmt.Sprintf("pid %d is another process (pid %d: boot_id %s, recorded %s)", pid, pid, live.BootID, other))
	z := exec.Command("true")
	if err := z.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the child is a zombie", func() bool { st, _, ok := procStat(z.Process.Pid); return ok && st == "Z" })
	zrec := selfRecord(t, z.Process.Pid)
	lostOn("barrier", zrec, fmt.Sprintf("pid %d has ended", zrec.Pid))
	_ = z.Wait()
	lostOn("wave-time", zrec, fmt.Sprintf("pid %d has ended", zrec.Pid))

	time.Sleep(200 * time.Millisecond)
	if st, _, ok := procStat(pid); !ok || st == "Z" || exists(filepath.Join(steps, "omes.signals")) {
		t.Fatalf("the live step under the wrong records got a signal (state %q, %v)", st, ok)
	}
}

// A .pid that does not parse yet is read again, pidWait at most, since step.sh opens its .pid before it writes it. One
// emptied, then filled again 50 ms after the call, is adopted; one never filled is StepLost once pidWait has passed,
// with why, no record and nothing started.
func TestRunStepWaitsForAPidToParse(t *testing.T) {
	a, in := stepGate(t, "STEP_S=3")
	steps := filepath.Join(a.Repo, "results", fakeGID, ".steps")
	if err := os.WriteFile(filepath.Join(steps, "omes.pid"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	type out struct {
		err   error
		took  time.Duration
		beats int
	}
	never := make(chan out, 1)
	go func() {
		var b stepBeats
		start := time.Now()
		_, err := a.runStep(context.Background(), stepIn(in, fakeGID, "omes", ""), "wf-1", "run-2", b.record)
		never <- out{err, time.Since(start), len(b.all())}
	}()

	si := stepIn(in, fakeGID, "laugd", "")
	pid := leaveRunning(t, a, si)
	pidPath := filepath.Join(steps, "laugd.pid")
	rec := readFile(t, pidPath)
	if err := os.WriteFile(pidPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var b stepBeats
	type answer struct {
		ans StepAnswer
		err error
	}
	got := make(chan answer, 1)
	go func() {
		ans, err := a.runStep(context.Background(), si, "wf-1", "run-3", b.record)
		got <- answer{ans, err}
	}()
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(pidPath, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	o := <-got
	if o.err != nil || o.ans.Result != "next" {
		t.Fatalf("a .pid filled 50 ms late: %+v, %v; want the adopted step's answer", o.ans, o.err)
	}
	hbs := b.all()
	if len(hbs) == 0 {
		t.Fatal("the adoption sent no beat")
	}
	for _, hb := range hbs {
		if hb != (StepHeartbeat{Name: "laugd", Pid: pid}) {
			t.Fatalf("beat %+v, want {laugd %d}", hb, pid)
		}
	}
	if s := readFile(t, filepath.Join(steps, "laugd.started")); s != strconv.Itoa(pid)+"\n" {
		t.Fatalf("laugd.started holds %q, want one start, pid %d", s, pid)
	}

	n := <-never
	ae := appErr(t, n.err, "StepLost")
	f := faultOf(t, ae)
	want := `a .pid with no .done; omes.pid does not parse: ""`
	if ae.Message() != "step omes: "+want || f.Why != want || f.Record != nil {
		t.Fatalf("a .pid never filled: StepLost %q, %+v; want %q", ae.Message(), f, want)
	}
	if n.took < pidWait || n.took > pidWait+2*time.Second || n.beats != 0 || exists(filepath.Join(steps, "omes.started")) {
		t.Fatalf("a .pid never filled: StepLost after %s, %d beats, started %v; want it after pidWait, %s, and nothing started",
			n.took.Round(time.Millisecond), n.beats, exists(filepath.Join(steps, "omes.started")), pidWait)
	}
}

// awaitGate is a gate run's directory for AwaitItem: results/<fakeGID> with .steps/ and the item directory pl.
func awaitGate(t *testing.T) (*GateActivities, AwaitInput, string) {
	t.Helper()
	a := &GateActivities{Repo: t.TempDir()}
	dir := filepath.Join(a.Repo, "results", fakeGID)
	for _, d := range []string{".steps", "pl"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return a, AwaitInput{GateInput: GateInput{Results: "results"}, Dir: "results/" + fakeGID, GID: fakeGID, Key: "pl"}, dir
}

// awaitIn runs awaitItem in the background and returns its answer, 5 s at most.
func awaitIn(t *testing.T, ctx context.Context, a *GateActivities, in AwaitInput, b *stepBeats) (ItemEnd, error) {
	t.Helper()
	type r struct {
		e   ItemEnd
		err error
	}
	got := make(chan r, 1)
	go func() { e, err := a.awaitItem(ctx, in, b.record); got <- r{e, err} }()
	select {
	case x := <-got:
		return x.e, x.err
	case <-time.After(5 * time.Second):
		t.Fatal("awaitItem did not answer within 5 s")
	}
	return ItemEnd{}, nil
}

func TestAwaitItem(t *testing.T) {
	// exit.json 300 ms in, after the item's .pid: found, with its exit and how it was stopped; the beats name the item.
	a, in, dir := awaitGate(t)
	if err := os.WriteFile(filepath.Join(dir, "pl", ".pid"), []byte(`{"pid":4242,"pgid":4242,"start":1,"boot_id":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(300*time.Millisecond, func() {
		_ = os.WriteFile(filepath.Join(dir, "pl", "exit.json"), []byte(`{"exit":143,"stopped_by":"TERM","start":{},"end":{}}`), 0o644)
	})
	var b stepBeats
	start := time.Now()
	e, err := awaitIn(t, context.Background(), a, in, &b)
	if err != nil || !e.Found || e.Key != "pl" || e.Exit == nil || *e.Exit != 143 || e.StoppedBy == nil || *e.StoppedBy != "TERM" {
		t.Fatalf("exit.json 300 ms in: %+v, %v", e, err)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("exit.json 300 ms in took %s", took)
	}
	if hb, ok := b.first(); !ok || hb != (StepHeartbeat{Name: "await-pl", Pid: 4242}) {
		t.Fatalf("first beat %+v, %v", hb, ok)
	}

	// Empty, as write_exit's jq leaves it for a moment, then filled: read again until it parses.
	a, in, dir = awaitGate(t)
	if err := os.WriteFile(filepath.Join(dir, "pl", "exit.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(700*time.Millisecond, func() {
		_ = os.WriteFile(filepath.Join(dir, "pl", "exit.json"), []byte(`{"exit":0,"stopped_by":null}`), 0o644)
	})
	e, err = awaitIn(t, context.Background(), a, in, &stepBeats{})
	if err != nil || !e.Found || *e.Exit != 0 || e.StoppedBy != nil {
		t.Fatalf("empty, then filled: %+v, %v", e, err)
	}

	// exit null, as watch_items writes it for an item still there 10 s after KILL: found, no exit.
	a, in, dir = awaitGate(t)
	if err := os.WriteFile(filepath.Join(dir, "pl", "exit.json"), []byte(`{"exit":null,"stopped_by":"KILL","start":{},"end":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err = awaitIn(t, context.Background(), a, in, &stepBeats{})
	if err != nil || !e.Found || e.Exit != nil || e.StoppedBy == nil || *e.StoppedBy != "KILL" || itemText(e) != "pl exit null after KILL" {
		t.Fatalf("exit null: %+v, %v", e, err)
	}

	// An object without the key exit, then filled: read again, as rule 1 takes only an object with exit.
	a, in, dir = awaitGate(t)
	if err := os.WriteFile(filepath.Join(dir, "pl", "exit.json"), []byte(`{"start":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(700*time.Millisecond, func() {
		_ = os.WriteFile(filepath.Join(dir, "pl", "exit.json"), []byte(`{"exit":3,"stopped_by":null}`), 0o644)
	})
	e, err = awaitIn(t, context.Background(), a, in, &stepBeats{})
	if err != nil || !e.Found || e.Exit == nil || *e.Exit != 3 {
		t.Fatalf("no exit, then filled: %+v, %v", e, err)
	}

	// wave.done and no exit.json: not found.
	a, in, dir = awaitGate(t)
	if err := os.WriteFile(filepath.Join(dir, ".steps", "wave.done"), []byte(`{"exit":0,"answer":{"result":"done"},"why":null,"at":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err = awaitIn(t, context.Background(), a, in, &stepBeats{})
	if err != nil || e.Found || e.Why != "the wave step ended without pl/exit.json" {
		t.Fatalf("wave.done, no exit.json: %+v, %v", e, err)
	}

	// A wave.pid whose process has ended, and no wave.done: not found.
	a, in, dir = awaitGate(t)
	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rec := selfRecord(t, cmd.Process.Pid)
	_ = cmd.Wait()
	b2, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, ".steps", "wave.pid"), b2, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err = awaitIn(t, context.Background(), a, in, &stepBeats{})
	if err != nil || e.Found || e.Why != "the wave step ended without a .done" {
		t.Fatalf("an ended wave.pid, no wave.done: %+v, %v", e, err)
	}

	// A wave.pid of a live process, nothing else: it waits; its context ends: ctx.Err() within 1 s.
	a, in, dir = awaitGate(t)
	b3, _ := json.Marshal(selfRecord(t, os.Getpid()))
	if err := os.WriteFile(filepath.Join(dir, ".steps", "wave.pid"), b3, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(600*time.Millisecond, cancel)
	start = time.Now()
	e, err = awaitIn(t, ctx, a, in, &stepBeats{})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 1600*time.Millisecond {
		t.Fatalf("a cancel: %+v, %v after %s", e, err, time.Since(start))
	}

	// A bad key, a bad gate id, a dir that is not results/<gid>, no directory: StepFailed before anything.
	for name, bad := range map[string]AwaitInput{
		"key":     {GateInput: in.GateInput, Dir: in.Dir, GID: in.GID, Key: "PL"},
		"gate id": {GateInput: in.GateInput, Dir: "results/x", GID: "x", Key: "pl"},
		"dir":     {GateInput: in.GateInput, Dir: "elsewhere/" + fakeGID, GID: in.GID, Key: "pl"},
		"no dir":  {GateInput: GateInput{Results: "nowhere"}, Dir: "nowhere/" + fakeGID, GID: in.GID, Key: "pl"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := a.awaitItem(context.Background(), bad, noBeat)
			appErr(t, err, "StepFailed")
		})
	}
}

// combine's and stopped's answers carry verdict.json's bytes and digest, as the file stood; post's carries none.
func TestFromDoneCarriesTheVerdict(t *testing.T) {
	a, in := stepGate(t)
	dir := filepath.Join(a.Repo, "results", fakeGID)
	verdict := []byte("{\n  \"verdict\": \"BLOCK\",\n  \"wall\": 1792152000.120\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "verdict.json"), verdict, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(verdict)
	for _, s := range []struct{ verb, key, answer string }{
		{"combine", "", `{"result":"done","verdict":"BLOCK","exit":1,"trap":false}`},
		{"stopped", "the workflow was cancelled", `{"result":"done","stopped_at":"wave","verdict":"BLOCK","exit":1,"trap":true}`},
		{"post", "", `{"result":"done","exit":0}`},
	} {
		si := stepIn(in, fakeGID, s.verb, s.key)
		if err := os.WriteFile(filepath.Join(dir, ".steps", si.Name+".done"), []byte(`{"exit":0,"answer":`+s.answer+`,"why":null,"at":null}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ans, err := a.runStep(context.Background(), si, "wf-1", "run-1", noBeat)
		if err != nil {
			t.Fatal(err)
		}
		if s.verb == "post" {
			if ans.Verdict != nil || ans.SHA256 != "" {
				t.Fatalf("post's answer carries a verdict: %+v", ans)
			}
			continue
		}
		if string(ans.Verdict) != string(verdict) || ans.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s's answer: verdict %q, sha256 %s", s.verb, ans.Verdict, ans.SHA256)
		}
	}
}

// StartRun is check with no workflow yet; RemoveEmptyRun takes back what it made, and leaves a directory that holds
// anything else.
func TestStartRunAndRemoveEmptyRun(t *testing.T) {
	script, err := filepath.Abs("testdata/fake-step.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	for _, d := range []string{"gate", "results"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(script, filepath.Join(repo, "gate", "step.sh")); err != nil {
		t.Fatal(err)
	}
	in := GateInput{ServerTag: "1.31.0", Plan: "gate/plan.yaml", Results: "results", PoolListen: "127.0.0.1:17899"}
	dir := filepath.Join(repo, "results", fakeGID)
	for _, extra := range []bool{false, true} {
		ca, err := StartRun(context.Background(), repo, in, nil)
		if err != nil || ca.GateID != fakeGID || ca.Dir != "results/"+fakeGID {
			t.Fatalf("StartRun: %+v, %v", ca, err)
		}
		env := readFile(t, filepath.Join(dir, ".steps", "check.env"))
		for _, want := range []string{"GATE_WORKFLOW_ID=\n", "GATE_WORKFLOW_RUN_ID=\n"} {
			if !strings.Contains(env, want) {
				t.Errorf("check's environment lacks %q", want)
			}
		}
		if strings.Contains(env, "GATE_WORKER_COMMIT=") {
			t.Error("check got GATE_WORKER_COMMIT with no worker")
		}
		// The fake writes .steps/check.env, which the real check does not: taken out, as the real one leaves .steps/.
		if err := os.Remove(filepath.Join(dir, ".steps", "check.env")); err != nil {
			t.Fatal(err)
		}
		if extra {
			if err := os.WriteFile(filepath.Join(dir, "other"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		removed, err := RemoveEmptyRun(repo, in, fakeGID)
		if !extra && (!removed || err != nil || exists(dir)) {
			t.Fatalf("an empty run: removed %v, %v; still there %v", removed, err, exists(dir))
		}
		if extra && (removed || err == nil || !exists(filepath.Join(dir, "other"))) {
			t.Fatalf("a run with a file of its own: removed %v, %v", removed, err)
		}
	}
	if _, err := RemoveEmptyRun(repo, in, "../x"); err == nil {
		t.Fatal("RemoveEmptyRun took a gate id that is not one")
	}
}
