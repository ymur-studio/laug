package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// The step runner: one activity per step of the gate run, each one gate/step.sh run in a
// session of its own. step.sh runs the lines gate/run.sh runs at that point and answers with one JSON line. Its files
// are in the gate run's directory, under .steps/: <name>.pid, its own record, written first; <name>.out, the answer;
// <name>.log, its stderr with stdout joined; <name>.done, {exit, answer, why, at}, written by its EXIT trap. Step
// runs one step; Stop stops one. A step runs at most once: a later attempt takes its .done, or adopts it while its
// record passes the guard (ProcRecord.Check), and never starts it again. AwaitItem waits for one item of the wave to
// end. Gate (gate.go) drives every verb in gate/run.sh's order. StartRun is gate/step.sh check for the starter: it
// makes the gate run's directory before the workflow exists. RemoveEmptyRun takes back a directory no workflow ran in.

// GateActivities run the gate's bash on this host: gate/step.sh, one step at a time. Repo is the checkout's root, the
// worker's -repo. Every exported method is an activity (Step, Stop and AwaitItem), so helpers stay unexported, and
// StartRun and RemoveEmptyRun are functions.
type GateActivities struct {
	Repo string
	// StepScript is what Step and check run, gate/step.sh when empty; tests point it at a fake. StopMargin is what
	// Stop waits beyond the plan's kill_grace_s, DefaultStopMargin when zero. Identity is the worker's,
	// laug-worker@<7 hex>[-dirty]: step.sh's open compares its commit with the checkout's HEAD.
	StepScript string
	StopMargin time.Duration
	Identity   string
	// Environ is the environment step.sh's is built from (StepEnv), one KEY=value a string: the one laug built for
	// the worker, which runs in laug's own process. nil is this process's, os.Environ.
	Environ []string
}

// environ is the environment step.sh's is built from: Environ, else this process's.
func (a *GateActivities) environ() []string {
	if a.Environ == nil {
		return os.Environ()
	}
	return a.Environ
}

// abs resolves a path the way run.sh does: relative to the checkout.
func (a *GateActivities) abs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(a.Repo, p)
}

// shown gives a path as a record keeps it (gate/lib.sh's shown): relative to the checkout, else "(outside the
// repository)/<name>", so that nothing in history or a result names a home path.
func (a *GateActivities) shown(p string) string {
	p = a.abs(p)
	root, err := filepath.EvalSymlinks(a.Repo)
	if err != nil {
		root = a.Repo
	}
	for _, r := range []string{root, a.Repo} {
		if rel, err := filepath.Rel(r, p); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return rel
		}
	}
	return "(outside the repository)/" + filepath.Base(p)
}

// StepInput is one step of a gate run. Dir is path.Join(Results, GID) as the workflow builds it; the activity resolves
// it against the checkout, as run.sh does. Name must be StepName(Verb, Key).
type StepInput struct {
	GateInput
	Dir  string `json:"dir"`
	GID  string `json:"gid"`
	Name string `json:"name"`
	Verb string `json:"verb"`
	Key  string `json:"key,omitempty"`
}

// StepHeartbeat is what Step and Stop report every 2 s: the step and the pid of its step.sh.
type StepHeartbeat struct {
	Name string `json:"name"`
	Pid  int    `json:"pid"`
}

// StepAnswer is a step that answered. Key is the step's key: a lease's item, a quiet pass, stopped for post and release
// on the stop path, or stopped's why. Exit is the answer's own exit, the gate's code from combine, post and stopped,
// and 0 when the answer has none. step.sh's process exit behind every StepAnswer is 0. Answer has the home paths taken
// out (redact), and crosses the data converter compacted, its numbers' digits kept: encoding/json compacts a RawMessage
// (go1.26.0 encode.go:487; 1792152000.120 keeps its 0). The bytes that must stay exact, verdict.json's,
// ride in Verdict: combine's and stopped's answers carry the file as it stood when the step answered, and SHA256 its
// hex digest. A []byte crosses the data converter as base64, which is exact (case C of
// docs/tables/records.jsonl#cells/results/2026-10-16-gate6-step3.txt, a 1 876-byte verdict as 2 504 bytes of base64, S1
// byte for byte).
type StepAnswer struct {
	Name    string          `json:"name"`
	Key     string          `json:"key,omitempty"`
	Exit    int             `json:"exit"`
	Result  string          `json:"result"`
	Answer  json.RawMessage `json:"answer"`
	Verdict []byte          `json:"verdict,omitempty"`
	SHA256  string          `json:"sha256,omitempty"`
}

// AwaitInput is one item of the wave, for AwaitItem. Dir is path.Join(Results, GID), as in StepInput.
type AwaitInput struct {
	GateInput
	Dir string `json:"dir"`
	GID string `json:"gid"`
	Key string `json:"key"`
}

// ItemEnd is an item's end as <key>/exit.json records it (gate/lib.sh, write_exit). Exit is null for an item still
// there 10 s after KILL (gate/lib.sh, watch_items). StoppedBy is TERM, KILL or null. Found is false when the item
// left no exit.json, and Why then says how that was seen.
type ItemEnd struct {
	Key       string  `json:"key"`
	Found     bool    `json:"found"`
	Exit      *int    `json:"exit"`
	StoppedBy *string `json:"stopped_by"`
	Why       string  `json:"why,omitempty"`
}

// ProcRecord is a process as a record keeps it: .steps/<name>.pid, a .state.json key, or <key>/.pid. Start is field 22
// of /proc/<pid>/stat, starttime in clock ticks since boot (over getconf CLK_TCK it is /proc/uptime at the
// fork), and nil for a process that had ended before its record was taken. Pgid is set in <key>/.pid only.
type ProcRecord struct {
	Pid    int     `json:"pid"`
	Pgid   int     `json:"pgid,omitempty"`
	Start  *uint64 `json:"start"`
	BootID string  `json:"boot_id"`
}

// Check is the guard: alive only when the record's start is set and the pid has a /proc entry whose state (field 3)
// is not Z or X and whose start and boot_id equal the record's. A zombie still answers kill -0, so it reads as gone.
// why is set only when the entry belongs to another process: a pid reused, or a boot since.
func (r ProcRecord) Check() (alive bool, why string) {
	if r.Start == nil || r.Pid <= 0 {
		return false, ""
	}
	state, start, ok := procStat(r.Pid)
	if !ok {
		return false, ""
	}
	if boot := bootID(); boot != r.BootID {
		return false, fmt.Sprintf("pid %d: boot_id %s, recorded %s", r.Pid, boot, r.BootID)
	}
	if start != *r.Start {
		return false, fmt.Sprintf("pid %d started at %d, recorded %d", r.Pid, start, *r.Start)
	}
	return state != "Z" && state != "X", ""
}

// procStat reads fields 3 and 22 of /proc/<pid>/stat: the state and starttime. The command name, field 2, can hold
// spaces and parentheses, so the fields are counted from the last ") ".
func procStat(pid int) (state string, start uint64, ok bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0, false
	}
	i := bytes.LastIndex(b, []byte(") "))
	if i < 0 {
		return "", 0, false
	}
	f := strings.Fields(string(b[i+2:]))
	if len(f) < 20 {
		return "", 0, false
	}
	start, err = strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return f[0], start, true
}

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// StepFault is the details of a StepLost or StepFailed error. Why is the .done's why, or the .log's last line, with
// home paths taken out (redact); Log is the .log as shown. Record is the .pid's, for a StepLost.
type StepFault struct {
	Name   string      `json:"name"`
	Exit   int         `json:"exit"`
	Why    string      `json:"why"`
	Log    string      `json:"log"`
	Record *ProcRecord `json:"record,omitempty"`
}

// CheckAnswer is what check made: the gate run's id and its directory, shown.
type CheckAnswer struct {
	GateID string `json:"gate_id"`
	Dir    string `json:"dir"`
}

// StopResult is what Stop did. Sent is "", "TERM" or "TERM, then KILL". Done is whether the step wrote its .done, and
// Exit the .done's exit then. Answer is the .done's answer, redacted as Step's, when Exit is 0: after the backstop, a
// wave that answered in that instant is judged on it (gate.go, stopPath), and its items come from it.
type StopResult struct {
	Name   string          `json:"name"`
	Sent   string          `json:"sent"`
	Done   bool            `json:"done"`
	Exit   *int            `json:"exit"`
	Answer json.RawMessage `json:"answer,omitempty"`
	Why    string          `json:"why,omitempty"`
}

// DefaultStopMargin is what Stop waits after TERM beyond the plan's kill_grace_s when GateActivities.StopMargin is
// zero. It covers the wave's TERM trap: TERM to the items, kill_grace_s, KILL, 10 s more (gate/lib.sh, watch_items),
// the sweep and the poller's stop.
const DefaultStopMargin = 40 * time.Second

// pidWait is how long runStep waits for a .pid. Once its context has ended, it waits for a step.sh that has not yet
// written its .pid: gate_env runs first, 11 to 13 ms from exec to the .pid on the dev host (5 of 5).
// adopt waits for a .pid that does not parse yet: step.sh opens it under noclobber, then writes it.
const pidWait = 5 * time.Second

var (
	gidRE   = regexp.MustCompile(`^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)
	leaseRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	quietRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})$`)
)

// StepName names a step's files and its activity: lease-<key>, quiet-<key>, judge for combine, else the verb. post,
// release and stopped leave their key out. gate/step.sh names its files the same way.
func StepName(verb, key string) string {
	switch verb {
	case "lease", "quiet":
		return verb + "-" + key
	case "combine":
		return "judge"
	}
	return verb
}

// stepKey checks a verb's key as gate/step.sh does.
func stepKey(verb, key string) error {
	switch verb {
	case "open", "preflight", "omes", "laugd", "harness", "barrier", "wave-time", "wave", "combine":
		if key != "" {
			return fmt.Errorf("%s takes no key, got %q", verb, key)
		}
	case "lease":
		if !leaseRE.MatchString(key) {
			return fmt.Errorf("lease key %q is not a plan item's key", key)
		}
	case "quiet":
		if !quietRE.MatchString(key) {
			return fmt.Errorf("quiet key %q is not a count from 0 to 9999", key)
		}
	case "post", "release":
		if key != "" && key != "stopped" {
			return fmt.Errorf("%s takes no key or stopped, got %q", verb, key)
		}
	case "stopped":
		if len(key) < 1 || len(key) > 300 || strings.ContainsAny(key, "\n\x00") {
			return fmt.Errorf("stopped takes one line of 1 to 300 bytes, got %d bytes", len(key))
		}
	default:
		return fmt.Errorf("no verb %q", verb)
	}
	return nil
}

// valid checks a StepInput before anything runs.
func (in StepInput) valid() error {
	if err := stepKey(in.Verb, in.Key); err != nil {
		return err
	}
	if n := StepName(in.Verb, in.Key); in.Name != n {
		return fmt.Errorf("name %q, and verb %s names %q", in.Name, in.Verb, n)
	}
	if !gidRE.MatchString(in.GID) {
		return fmt.Errorf("gate id %q is not g<date>-<time>", in.GID)
	}
	if d := path.Join(in.Results, in.GID); in.Dir != d {
		return fmt.Errorf("dir %q is not %q", in.Dir, d)
	}
	return nil
}

// itemKnobs are the 41 names gate_env unsets (gate/lib.sh, gate_env): the items read them, the gate never does.
// LAUG_BLOCK and LAUG_COMPOSE_EXTRA also go by GateInput.Env's LAUG_ prefix.
var itemKnobs = []string{"THRESHOLDS", "THRESHOLDS_RELEASES", "CATALOG", "CANDIDATES", "RESULTS_DIR", "RUN_ID", "LEASE_FILE",
	"SCENARIO", "RATE", "DURATION", "ITERATIONS", "MAX_CONCURRENT", "CONTINUE_ON_FAILURE", "NAMESPACE", "CELL_ADDRESS",
	"PROJECT", "PROM_URL", "FALLBACK_PROM_URL", "MIN_WINDOW_S", "VERDICT_JSON", "SWEEP_MARGIN_S", "SWEEP_EXPECTED",
	"SWEEP_ADDRESS", "SWEEP_RETRIES", "SWEEP_RETRY_S", "SA_SLICE_S", "TDBG_IMAGE", "ROTATE_SLACK_S", "WORKER_METRICS_ADDR",
	"CLIENT_METRICS_ADDR", "POOL_ADDR", "POOL_QUIET_WAIT_S", "VISIBILITY", "BIND_ADDR", "SERVER_FI", "FC_GUEST_KEY",
	"FC_GUEST_KNOWN_HOSTS", "GUEST_DYNCONFIG_ORIG", "LAUG_BLOCK", "DYNCONFIG_TEMPLATE", "LAUG_COMPOSE_EXTRA"}

// StepEnv is step.sh's environment: Env's, without the items' knobs, plus the workflow's ids and the worker's commit,
// which step.sh copies and unsets before it starts anything. commit is the worker identity after laug-worker@, and
// GATE_WORKER_COMMIT is left unset when it is empty.
func (in GateInput) StepEnv(environ []string, wfID, runID, commit string) []string {
	knob := map[string]bool{}
	for _, k := range itemKnobs {
		knob[k] = true
	}
	var kept []string
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); !knob[name] {
			kept = append(kept, kv)
		}
	}
	env := append(in.Env(kept), "GATE_WORKFLOW_ID="+wfID, "GATE_WORKFLOW_RUN_ID="+runID)
	if commit != "" {
		env = append(env, "GATE_WORKER_COMMIT="+commit)
	}
	return env
}

func (a *GateActivities) stepScript() string {
	if a.StepScript == "" {
		return "gate/step.sh"
	}
	return a.StepScript
}

func (a *GateActivities) commit() string { return strings.TrimPrefix(a.Identity, "laug-worker@") }

// redact takes home paths out of a line that reaches history: the results directory and the checkout root, each as
// given and with its links resolved, become what shown gives.
func (a *GateActivities) redact(in GateInput, s string) string {
	return a.redactWith(in, s, func(p string) string { return p })
}

// redactAnswer is redact inside an answer's JSON, where a refused text can carry a command's stderr: each path as JSON
// writes it inside a string (jq -c escapes no "/"). An answer that would not parse after it comes back as it was.
func (a *GateActivities) redactAnswer(in GateInput, raw json.RawMessage) json.RawMessage {
	out := json.RawMessage(a.redactWith(in, string(raw), jsonInner))
	if !json.Valid(out) {
		return raw
	}
	return out
}

// jsonInner is s as it reads inside a JSON string: escaped, without its quotes, and with no HTML escapes, as jq
// writes it.
func jsonInner(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	t := strings.TrimSuffix(b.String(), "\n")
	return t[1 : len(t)-1]
}

func (a *GateActivities) redactWith(in GateInput, s string, esc func(string) string) string {
	res := a.abs(in.Results)
	shownRes := a.shown(res)
	type pair struct{ from, to string }
	var pairs []pair
	for _, p := range []string{res, resolved(res)} {
		pairs = append(pairs, pair{p, shownRes})
	}
	for _, p := range []string{filepath.Clean(a.Repo), resolved(a.Repo)} {
		pairs = append(pairs, pair{p, "."})
	}
	for _, p := range pairs {
		if p.from == "" || p.from == "/" || p.from == "." {
			continue
		}
		s = replacePath(s, esc(p.from), esc(p.to))
	}
	return s
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// replacePath replaces from as a whole path: from/x becomes to/x (x alone when to is "."), and from followed by a
// character a name cannot end at stays as it is, so /a/laug never matches inside /a/laug-other.
func replacePath(s, from, to string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(from) + `(/|[^A-Za-z0-9._-]|$)`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		rest := m[len(from):]
		if rest == "/" && to == "." {
			return ""
		}
		return to + rest
	})
}

func stepErr(kind, msg string, f StepFault) error {
	return temporal.NewNonRetryableApplicationError(msg, kind, nil, f)
}

func badInput(msg string) error {
	return temporal.NewNonRetryableApplicationError(msg, "StepFailed", nil, StepFault{Why: msg})
}

// stepCmd is a step.sh command with its settings: env --default-signal=INT,QUIT, the checkout as its directory, a
// session of its own.
func (a *GateActivities) stepCmd(env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("env", append([]string{"--default-signal=INT,QUIT", a.stepScript()}, args...)...)
	cmd.Dir = a.Repo
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// check runs gate/step.sh check: gate_env, then the gate run's directory with .steps/ and .state.json. It starts
// nothing detached, so its stdout and stderr go to buffers. It runs before the workflow exists (StartRun), so step.sh
// gets empty workflow ids and writes them as null.
func (a *GateActivities) check(ctx context.Context, in GateInput) (CheckAnswer, error) {
	if err := ctx.Err(); err != nil {
		return CheckAnswer{}, err
	}
	var out, errb bytes.Buffer
	cmd := a.stepCmd(in.StepEnv(a.environ(), "", "", a.commit()), "check")
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		why := a.redact(in, lastLineOf(errb.String()))
		if why == "" {
			why = err.Error()
		}
		return CheckAnswer{}, stepErr("StepFailed", why, StepFault{Name: "check", Exit: exitOf(cmd.ProcessState), Why: why})
	}
	var ans struct {
		Result string `json:"result"`
		CheckAnswer
	}
	line := lastLineOf(out.String())
	if json.Unmarshal([]byte(line), &ans) != nil || ans.Result != "next" || !gidRE.MatchString(ans.GateID) {
		why := a.redact(in, fmt.Sprintf("check answered %q", line))
		return CheckAnswer{}, stepErr("StepFailed", why, StepFault{Name: "check", Why: why})
	}
	// step.sh shows the directory (gate/lib.sh, shown); redact holds whatever else would reach history.
	ans.Dir = a.redact(in, ans.Dir)
	return ans.CheckAnswer, nil
}

// Step runs one step: gate/step.sh <verb> <dir> [key], in a session of its own, under the runner's rules (runStep).
// It heartbeats {name, pid} every 2 s through a context that a cancel does not end. The SDK sends a heartbeat with the
// context it is given (SDK v1.49.0 activityEnvironmentInterceptor.RecordHeartbeat), and its batcher flushes a
// window with the context of the call that opened it (temporalInvoker.Heartbeat), so one beat on a canceled
// context would chain dead windows: on the lab host, a Step made to wait through a cancel and beat on its own context
// timed out on its heartbeat 10.136 s after the request (cells/results/2026-10-27-boot6.txt, SAN10, X3).
func (a *GateActivities) Step(ctx context.Context, in StepInput) (StepAnswer, error) {
	we := activity.GetInfo(ctx).WorkflowExecution
	hctx := context.WithoutCancel(ctx)
	return a.runStep(ctx, in, we.ID, we.RunID, func(hb StepHeartbeat) { activity.RecordHeartbeat(hctx, hb) })
}

// runStep is Step without the activity environment, so a test can end ctx with a cause of its own. Its rules:
//  1. .done exists: exit 0 with an object answer is the StepAnswer; any other exit is StepFailed.
//  2. A .pid with no .done: adopt it, or StepLost (adopt). A .pid that does not parse yet is read again every 10 ms,
//     pidWait at most, since step.sh opens it before it writes it. The step is adopted when the record passes the
//     guard (ProcRecord.Check: the process runs, is no zombie, and has the record's start and boot_id). The runner then
//     heartbeats {name, pid} at once and every 2 s, polls for .done every 250 ms, and answers by rule 1 when it comes.
//     An adopted step that ends with no .done is StepLost. A record that fails the guard is StepLost at once, with the
//     guard's why when it has one, and nothing is sent. It fails for a process that has ended or is a zombie, a pid
//     with another start time, a record from another boot, and a .pid that still does not parse. A .done that came
//     after the first look is rule 1.
//  3. Otherwise start step.sh. When it ends: .done exists, rule 1; exit 75 with no .done, rule 2, since another copy
//     of this step wrote the .pid first; anything else, StepFailed with the .log's last line.
//  4. ctx ends, for any reason: a step.sh that ended in that same instant counts as rule 3's end. Otherwise return
//     ctx.Err() and send nothing, whatever the cause: a cancel; a heartbeat that failed past its retry window
//     (case B of docs/tables/records.jsonl#cells/results/2026-10-16-gate6-step3.txt); a NotFound, a pause or a reset
//     (SDK v1.49.0 temporalInvoker.internalHeartBeat); a worker shutdown
//     (baseWorker.Stop). Stop is what stops a step. The goroutine still reaps step.sh. A
//     step.sh that has not yet written its .pid is waited for first, pidWait at most (leave). An adopted step is left
//     the same way: a .done there at that instant is rule 1, else ctx.Err(), nothing sent.
func (a *GateActivities) runStep(ctx context.Context, in StepInput, wfID, runID string, heartbeat func(StepHeartbeat)) (StepAnswer, error) {
	if err := in.valid(); err != nil {
		return StepAnswer{}, badInput(fmt.Sprintf("step %q: %v", in.Name, err))
	}
	dir := a.abs(in.Dir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return StepAnswer{}, badInput(fmt.Sprintf("step %s: %s is not a gate run's directory", in.Name, a.shown(dir)))
	}
	steps := filepath.Join(dir, ".steps")
	donePath, pidPath := filepath.Join(steps, in.Name+".done"), filepath.Join(steps, in.Name+".pid")
	logPath := filepath.Join(steps, in.Name+".log")
	if _, err := os.Stat(donePath); err == nil {
		return a.fromDone(in, donePath, logPath)
	}
	if _, err := os.Stat(pidPath); err == nil {
		return a.adopt(ctx, in, pidPath, donePath, logPath, heartbeat)
	}
	if err := ctx.Err(); err != nil {
		return StepAnswer{}, err
	}
	if err := os.MkdirAll(steps, 0o755); err != nil {
		return StepAnswer{}, badInput(fmt.Sprintf("step %s: %v", in.Name, a.redact(in.GateInput, err.Error())))
	}
	outF, err := os.OpenFile(filepath.Join(steps, in.Name+".out"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StepAnswer{}, badInput(a.redact(in.GateInput, err.Error()))
	}
	defer outF.Close()
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StepAnswer{}, badInput(a.redact(in.GateInput, err.Error()))
	}
	defer logF.Close()

	args := []string{in.Verb, dir}
	if in.Key != "" {
		args = append(args, in.Key)
	}
	cmd := a.stepCmd(in.StepEnv(a.environ(), wfID, runID, a.commit()), args...)
	cmd.Stdout, cmd.Stderr = outF, logF
	if err := cmd.Start(); err != nil {
		why := a.redact(in.GateInput, fmt.Sprintf("%s did not start: %v", a.stepScript(), err))
		return StepAnswer{}, stepErr("StepFailed", "step "+in.Name+": "+why, StepFault{Name: in.Name, Exit: -1, Why: why, Log: a.shown(logPath)})
	}
	pid := cmd.Process.Pid
	done := make(chan struct{}, 1)
	go func() { _ = cmd.Wait(); done <- struct{}{} }()
	end := func() (StepAnswer, error) {
		if _, err := os.Stat(donePath); err == nil {
			return a.fromDone(in, donePath, logPath)
		}
		code := exitOf(cmd.ProcessState)
		if code == 75 {
			return a.adopt(ctx, in, pidPath, donePath, logPath, heartbeat)
		}
		why := a.redact(in.GateInput, lastLine(logPath))
		if why == "" {
			why = fmt.Sprintf("exit %d and nothing on stderr", code)
		}
		return StepAnswer{}, stepErr("StepFailed", fmt.Sprintf("step %s: exit %d, no .done: %s", in.Name, code, why),
			StepFault{Name: in.Name, Exit: code, Why: why, Log: a.shown(logPath)})
	}
	hb := StepHeartbeat{Name: in.Name, Pid: pid}
	heartbeat(hb)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return end()
		case <-tick.C:
			heartbeat(hb)
		case <-ctx.Done():
			return leave(ctx, done, pidPath, end)
		}
	}
}

// adopt is rule 2: a step that a .pid records and no .done answers yet. The record is read until it parses
// (waitRecord), and the guard decides once, before anything is sent; then the step is polled until its .done comes,
// it ends without one, or ctx ends. adopt never waits for the process: the goroutine of the runStep that started it
// reaps it, or init does once that worker is gone.
func (a *GateActivities) adopt(ctx context.Context, in StepInput, pidPath, donePath, logPath string, heartbeat func(StepHeartbeat)) (StepAnswer, error) {
	isDone := func() bool { _, err := os.Stat(donePath); return err == nil }
	// A .done that came since the first look: the step answered.
	if isDone() {
		return a.fromDone(in, donePath, logPath)
	}
	rec, err := waitRecord(pidPath)
	alive, guard := false, ""
	if err == nil {
		alive, guard = rec.Check()
	}
	if !alive {
		// step.sh's EXIT trap writes .done before step.sh exits: one that ended since the look above has answered.
		if isDone() {
			return a.fromDone(in, donePath, logPath)
		}
		return StepAnswer{}, a.lost(in, logPath, "a .pid with no .done", rec, err, guard)
	}
	if err := ctx.Err(); err != nil {
		return StepAnswer{}, err
	}
	hb := StepHeartbeat{Name: in.Name, Pid: rec.Pid}
	heartbeat(hb)
	beat := time.NewTicker(2 * time.Second)
	defer beat.Stop()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			if isDone() {
				return a.fromDone(in, donePath, logPath)
			}
			return StepAnswer{}, ctx.Err()
		case <-beat.C:
			heartbeat(hb)
		case <-poll.C:
			if isDone() {
				return a.fromDone(in, donePath, logPath)
			}
			if alive, guard := rec.Check(); !alive {
				if isDone() {
					return a.fromDone(in, donePath, logPath)
				}
				return StepAnswer{}, a.lost(in, logPath, "the adopted step ended without a .done", rec, nil, guard)
			}
		}
	}
}

// leave is rule 4. A step.sh that has ended counts as rule 3's end. One that has not yet written its .pid is waited
// for, pidWait at most, sending nothing: Stop finds a step by its .pid, and would read a step.sh still in gate_env as a
// step that never started, while it goes on to write its .pid and run its verb. Then ctx.Err().
func leave(ctx context.Context, done <-chan struct{}, pidPath string, end func() (StepAnswer, error)) (StepAnswer, error) {
	until := time.Now().Add(pidWait)
	for {
		select {
		case <-done:
			return end()
		default:
		}
		if _, err := readRecord(pidPath); err == nil || !time.Now().Before(until) {
			return StepAnswer{}, ctx.Err()
		}
		select {
		case <-done:
			return end()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// exitOf is a process's exit as the shell gives it: 128 plus the signal when one ended it.
func exitOf(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func lastLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return lastLineOf(string(b))
}

// lastLineOf is the last line of s that is not blank, trimmed.
func lastLineOf(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

type doneFile struct {
	Exit   *int            `json:"exit"`
	Answer json.RawMessage `json:"answer"`
	Why    *string         `json:"why"`
}

func readDone(p string) (doneFile, error) {
	var d doneFile
	b, err := os.ReadFile(p)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil || d.Exit == nil {
		return d, fmt.Errorf("%s does not parse: %q", filepath.Base(p), lastLineOf(string(b)))
	}
	return d, nil
}

// fromDone is rule 1: the .done's answer, or a StepFailed that carries its exit and why.
func (a *GateActivities) fromDone(in StepInput, donePath, logPath string) (StepAnswer, error) {
	d, err := readDone(donePath)
	if err != nil {
		why := a.redact(in.GateInput, err.Error())
		return StepAnswer{}, stepErr("StepFailed", "step "+in.Name+": "+why, StepFault{Name: in.Name, Exit: -1, Why: why, Log: a.shown(logPath)})
	}
	if *d.Exit == 0 {
		if ans, err := answerFrom(in.Name, a.redactAnswer(in.GateInput, d.Answer)); err == nil {
			ans.Key = in.Key
			if in.Verb == "combine" || in.Verb == "stopped" {
				ans.Verdict, ans.SHA256 = a.verdictFile(in.Dir)
			}
			return ans, nil
		}
	}
	why := ""
	if d.Why != nil {
		why = a.redact(in.GateInput, *d.Why)
	}
	if *d.Exit == 0 && why == "" {
		why = fmt.Sprintf("exit 0 with the answer %s", a.redact(in.GateInput, string(d.Answer)))
	}
	return StepAnswer{}, stepErr("StepFailed", fmt.Sprintf("step %s: exit %d: %s", in.Name, *d.Exit, why),
		StepFault{Name: in.Name, Exit: *d.Exit, Why: why, Log: a.shown(logPath)})
}

// maxVerdict is the largest verdict.json a StepAnswer carries: 1 MiB. Real gates' are under 14 KB: the 158 lab gates
// of docs/tables/gates.jsonl, all fake false, wrote verdicts of 1 883 to 13 593 bytes.
const maxVerdict = 1 << 20

// verdictFile is <dir>/verdict.json and its sha256 hex digest, when it exists and is at most maxVerdict bytes. A read
// that fails leaves both empty: the starter's S1 then says the workflow carried no verdict.
func (a *GateActivities) verdictFile(dir string) ([]byte, string) {
	p := filepath.Join(a.abs(dir), "verdict.json")
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxVerdict {
		return nil, ""
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) > maxVerdict {
		return nil, ""
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:])
}

// answerFrom reads an answer's result and exit. An answer is a JSON object with a result.
func answerFrom(name string, raw json.RawMessage) (StepAnswer, error) {
	var f struct {
		Result string `json:"result"`
		Exit   *int   `json:"exit"`
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &f) != nil || f.Result == "" {
		return StepAnswer{}, fmt.Errorf("%s answered %q, not an object with a result", name, raw)
	}
	ans := StepAnswer{Name: name, Result: f.Result, Answer: json.RawMessage(t)}
	if f.Exit != nil {
		ans.Exit = *f.Exit
	}
	return ans, nil
}

// waitRecord reads a .pid until it parses, every 10 ms and pidWait at most. step.sh opens its .pid under noclobber and
// then writes it, so a look in between finds it empty. On the dev host, a second runStep, started the moment the
// first one's .pid appeared, found it empty 3 times in 100. A .pid that is not there is not waited for.
func waitRecord(p string) (*ProcRecord, error) {
	until := time.Now().Add(pidWait)
	for {
		rec, err := readRecord(p)
		if err == nil || errors.Is(err, fs.ErrNotExist) || !time.Now().Before(until) {
			return rec, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readRecord(p string) (*ProcRecord, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var r ProcRecord
	if err := json.Unmarshal(b, &r); err != nil || r.Pid <= 0 {
		return nil, fmt.Errorf("%s does not parse: %q", filepath.Base(p), lastLineOf(string(b)))
	}
	return &r, nil
}

// lost is a StepLost: what was found, then why adopt let the record go, as adopt read it: the .pid did not parse
// (readErr), or the guard failed, the pid being another process (guard, the guard's why) or having ended. The record
// goes in the details.
func (a *GateActivities) lost(in StepInput, logPath, what string, rec *ProcRecord, readErr error, guard string) error {
	why := what
	switch {
	case readErr != nil:
		why += "; " + a.redact(in.GateInput, readErr.Error())
	case guard != "":
		why += fmt.Sprintf("; pid %d is another process (%s)", rec.Pid, guard)
	default:
		why += fmt.Sprintf("; pid %d has ended", rec.Pid)
	}
	return stepErr("StepLost", "step "+in.Name+": "+why, StepFault{Name: in.Name, Why: why, Log: a.shown(logPath), Record: rec})
}

// Stop stops one step: TERM to its step.sh, whose trap stops what it started and writes .done; KILL to its process
// group when no .done comes within the plan's kill_grace_s plus StopMargin (DefaultStopMargin when zero). It never
// stops judge, post, release or stopped, which are the stop path's own steps. It heartbeats {name, pid} every 2 s
// through a context that a cancel does not end.
func (a *GateActivities) Stop(ctx context.Context, in StepInput) (StopResult, error) {
	hctx := context.WithoutCancel(ctx)
	logger := activity.GetLogger(ctx)
	return a.stop(ctx, in, func(hb StepHeartbeat) { activity.RecordHeartbeat(hctx, hb) }, func(s string) { logger.Warn(s) })
}

// stop is Stop without the activity environment. Its rules:
//  1. .done exists: Done, nothing sent. No .pid: nothing sent. The guard fails: nothing sent, its why returned and
//     logged.
//  2. TERM to step.sh's own pid, never to its group: the group holds step.sh's own helpers, and a TERM to a jq
//     writing an item's exit.json would leave it empty. step.sh's trap stops the rest. Then wait for .done, polling
//     every 0.25 s; a step.sh that is gone (or a zombie) with no .done ends the wait at once.
//  3. No .done at the deadline: KILL to the group, then up to 5 s for the guard to fail.
func (a *GateActivities) stop(ctx context.Context, in StepInput, heartbeat func(StepHeartbeat), logf func(string)) (StopResult, error) {
	if err := in.valid(); err != nil {
		return StopResult{}, badInput(fmt.Sprintf("stop %q: %v", in.Name, err))
	}
	switch in.Verb {
	case "combine", "post", "release", "stopped":
		return StopResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("stop %s: refused; judge, post, release and stopped are the stop path's own steps", in.Name), "StopRefused", nil)
	}
	res := StopResult{Name: in.Name}
	dir := a.abs(in.Dir)
	steps := filepath.Join(dir, ".steps")
	donePath, pidPath := filepath.Join(steps, in.Name+".done"), filepath.Join(steps, in.Name+".pid")
	isDone := func() bool {
		d, err := readDone(donePath)
		if errors.Is(err, fs.ErrNotExist) {
			return false
		}
		res.Done = true
		if err == nil {
			res.Exit = d.Exit
			if *d.Exit == 0 && len(d.Answer) > 0 {
				res.Answer = a.redactAnswer(in.GateInput, d.Answer)
			}
		} else {
			res.Why = a.redact(in.GateInput, err.Error())
		}
		return true
	}
	if isDone() {
		return res, nil
	}
	rec, err := readRecord(pidPath)
	if errors.Is(err, fs.ErrNotExist) {
		res.Why = "no .pid: the step never started"
		return res, nil
	}
	if err != nil {
		res.Why = a.redact(in.GateInput, err.Error()) + ": nothing sent"
		return res, nil
	}
	gone := func() (bool, string) {
		alive, why := rec.Check()
		return !alive, why
	}
	if g, why := gone(); g {
		if isDone() {
			return res, nil
		}
		if why != "" {
			res.Why = fmt.Sprintf("guard: %s %s: nothing sent", in.Name, why)
			logf("laug · " + res.Why)
		} else {
			res.Why = "the step ended without .done"
		}
		return res, nil
	}

	res.Sent = "TERM"
	_ = syscall.Kill(rec.Pid, syscall.SIGTERM)
	hb := StepHeartbeat{Name: in.Name, Pid: rec.Pid}
	heartbeat(hb)
	beat := time.NewTicker(2 * time.Second)
	defer beat.Stop()
	margin := a.StopMargin
	if margin <= 0 {
		margin = DefaultStopMargin
	}
	grace := time.Duration(killGrace(dir))*time.Second + margin
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-beat.C:
			heartbeat(hb)
		case <-poll.C:
			if isDone() {
				return res, nil
			}
			if g, _ := gone(); g {
				if isDone() {
					return res, nil
				}
				res.Why = "the step ended without .done"
				return res, nil
			}
		case <-deadline.C:
			break wait
		}
	}
	if isDone() {
		return res, nil
	}
	if g, _ := gone(); g {
		if isDone() {
			return res, nil
		}
		res.Why = "the step ended without .done"
		return res, nil
	}
	res.Sent = "TERM, then KILL"
	if syscall.Kill(-rec.Pid, syscall.SIGKILL) != nil {
		_ = syscall.Kill(rec.Pid, syscall.SIGKILL)
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if g, _ := gone(); g {
			break
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-beat.C:
			heartbeat(hb)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if isDone() {
		return res, nil
	}
	if g, _ := gone(); !g {
		res.Why = fmt.Sprintf("no .done %s after TERM, and pid %d still there 5 s after KILL", grace, rec.Pid)
	} else {
		res.Why = fmt.Sprintf("no .done %s after TERM: KILL to its group", grace)
	}
	return res, nil
}

// killGrace is the plan's kill_grace_s, from the gate run's plan.json; 30, the plan's default, before preflight
// wrote it.
func killGrace(dir string) int {
	var p struct {
		KillGraceS *int `json:"kill_grace_s"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil || json.Unmarshal(b, &p) != nil || p.KillGraceS == nil || *p.KillGraceS < 0 {
		return 30
	}
	return *p.KillGraceS
}

// StepOptions are a step's activity options. A step is retried only on errors that are not its own: a StepLost,
// StepFailed or StopRefused is final. Attempts are unlimited, bounded by ScheduleToClose: a retry of a step that
// started takes its .done or adopts it (rules 1 and 2), so a worker restart or a server outage costs an attempt and
// never a second run.
func StepOptions(stc time.Duration, wait bool) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    stc,
		ScheduleToCloseTimeout: stc + 15*time.Minute,
		HeartbeatTimeout:       10 * time.Second,
		WaitForCancellation:    wait,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        time.Second,
			BackoffCoefficient:     2,
			MaximumInterval:        10 * time.Second,
			MaximumAttempts:        0,
			NonRetryableErrorTypes: []string{"StepLost", "StepFailed", "StopRefused"},
		},
	}
}

// AwaitItem waits for one item of the wave to end and answers how it ended. It has no gate/step.sh verb: it reads the
// files the wave step writes, and starts and stops nothing. The workflow schedules one per item beside the wave, in
// the same workflow task, so an item's end shows in the workflow's progress while the wave still runs. Its rules:
//  0. Key fails leaseRE, GID fails gidRE, Dir is not path.Join(Results, GID), or no such directory: StepFailed.
//  1. Every 500 ms, <dir>/<key>/exit.json: bytes that parse as a JSON object with the key exit are the answer. Empty
//     or unparsable bytes are read again, since write_exit's jq writes the file in place (gate/lib.sh, write_exit).
//  2. .steps/wave.done exists: rule 1 once more, then Found false, "the wave step ended without <key>/exit.json".
//  3. .steps/wave.pid parses, its record fails the guard (ProcRecord.Check), and wave.done is still absent on a second
//     look: Found false, "the wave step ended without a .done". No wave.pid yet: wait, since the wave step and the
//     awaits are scheduled in one workflow task and either may start first.
//  4. ctx ends: ctx.Err().
//
// It heartbeats {await-<key>, the item's pid from <key>/.pid, 0 until that parses} at once and every 2 s, through a
// context that a cancel does not end, as Step does. It ends by rules 0 to 4 only; its StartToClose is the wave's.
func (a *GateActivities) AwaitItem(ctx context.Context, in AwaitInput) (ItemEnd, error) {
	hctx := context.WithoutCancel(ctx)
	return a.awaitItem(ctx, in, func(hb StepHeartbeat) { activity.RecordHeartbeat(hctx, hb) })
}

func (a *GateActivities) awaitItem(ctx context.Context, in AwaitInput, heartbeat func(StepHeartbeat)) (ItemEnd, error) {
	name := "await-" + in.Key
	switch {
	case !leaseRE.MatchString(in.Key):
		return ItemEnd{}, badInput(fmt.Sprintf("%s: key %q is not a plan item's key", name, in.Key))
	case !gidRE.MatchString(in.GID):
		return ItemEnd{}, badInput(fmt.Sprintf("%s: gate id %q is not g<date>-<time>", name, in.GID))
	case in.Dir != path.Join(in.Results, in.GID):
		return ItemEnd{}, badInput(fmt.Sprintf("%s: dir %q is not %q", name, in.Dir, path.Join(in.Results, in.GID)))
	}
	dir := a.abs(in.Dir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ItemEnd{}, badInput(fmt.Sprintf("%s: %s is not a gate run's directory", name, a.shown(dir)))
	}
	exitPath := filepath.Join(dir, in.Key, "exit.json")
	wavePid, waveDone := filepath.Join(dir, ".steps", "wave.pid"), filepath.Join(dir, ".steps", "wave.done")
	hb := StepHeartbeat{Name: name}
	beat := func() {
		if hb.Pid == 0 {
			if r, err := readRecord(filepath.Join(dir, in.Key, ".pid")); err == nil {
				hb.Pid = r.Pid
			}
		}
		heartbeat(hb)
	}
	look := func() (ItemEnd, bool) {
		if e, ok := readExit(exitPath, in.Key); ok {
			return e, true
		}
		if fileExists(waveDone) {
			if e, ok := readExit(exitPath, in.Key); ok {
				return e, true
			}
			return ItemEnd{Key: in.Key, Why: fmt.Sprintf("the wave step ended without %s/exit.json", in.Key)}, true
		}
		rec, err := readRecord(wavePid)
		if err != nil {
			return ItemEnd{}, false
		}
		if alive, _ := rec.Check(); alive {
			return ItemEnd{}, false
		}
		// The wave's EXIT trap writes .done before step.sh exits: one that ended since the look above has answered.
		if fileExists(waveDone) {
			if e, ok := readExit(exitPath, in.Key); ok {
				return e, true
			}
			return ItemEnd{Key: in.Key, Why: fmt.Sprintf("the wave step ended without %s/exit.json", in.Key)}, true
		}
		return ItemEnd{Key: in.Key, Why: "the wave step ended without a .done"}, true
	}
	beat()
	if e, ok := look(); ok {
		return e, nil
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return ItemEnd{}, ctx.Err()
		case <-tick.C:
			beat()
		case <-poll.C:
			if e, ok := look(); ok {
				return e, nil
			}
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// readExit reads <key>/exit.json: ok only when its bytes parse as a JSON object with the key exit, whose value is a
// whole number or null.
func readExit(p, key string) (ItemEnd, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return ItemEnd{}, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return ItemEnd{}, false
	}
	if _, ok := m["exit"]; !ok {
		return ItemEnd{}, false
	}
	var e struct {
		Exit      *int    `json:"exit"`
		StoppedBy *string `json:"stopped_by"`
	}
	if json.Unmarshal(b, &e) != nil {
		return ItemEnd{}, false
	}
	return ItemEnd{Key: key, Found: true, Exit: e.Exit, StoppedBy: e.StoppedBy}, true
}

// StartRun is gate/step.sh check for the starter: it makes the gate run's directory before the workflow starts, so
// the workflow id gate-<gid> can name it and history records the gid and directory from its first event. step.sh
// reads the empty GATE_WORKFLOW_ID and GATE_WORKFLOW_RUN_ID as null; open, which runs under gate-<gid>, writes
// gate.json's workflow_id. step.sh's environment is built from environ, nil for this process's. It is a function,
// not a method: every exported method of GateActivities is an activity.
func StartRun(ctx context.Context, repo string, in GateInput, environ []string) (CheckAnswer, error) {
	return (&GateActivities{Repo: repo, Environ: environ}).check(ctx, in)
}

// RemoveEmptyRun takes back the directory StartRun made when the workflow did not start: os.Remove of .state.json,
// .steps/ and the directory, in that order, so a directory that holds anything else stays, with its other files. It
// never removes a tree. removed is true only when the directory is gone.
func RemoveEmptyRun(repo string, in GateInput, gid string) (removed bool, err error) {
	if !gidRE.MatchString(gid) {
		return false, fmt.Errorf("gate id %q is not g<date>-<time>", gid)
	}
	dir := filepath.Join((&GateActivities{Repo: repo}).abs(in.Results), gid)
	for _, p := range []string{filepath.Join(dir, ".state.json"), filepath.Join(dir, ".steps")} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	if err := os.Remove(dir); err != nil {
		return false, err
	}
	return true, nil
}
