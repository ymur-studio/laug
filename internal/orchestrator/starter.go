package orchestrator

// StarterMain starts a gate on this checkout's worker and waits for its verdict: laug gate run x.y.z -workflow runs it
// in laug's own process with laug gate run's environment, which it carries as the workflow's input (the gate's
// inputs are workflow input, so history records them).
//
// It starts workflow Gate (gate.go): it runs gate/step.sh check itself, so the gate run's directory exists and
// the workflow id is gate-<gid>, then starts Gate with the gid and directory in its input. It refuses before check, so
// a refusal makes no directory, when no process holds this checkout's worker lock, when the identity the lock's holder
// wrote into the lock file is not HEAD's, when the server lists no poller on the queue, and when no poller's identity
// names HEAD's commit: a worker built before the last commit, or in another worktree, would run other Go against this
// tree's bash. The lock decides; the pollers are a second check, since the server lists a stopped worker for about
// 5 min after its last poll (docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt
// section 14; WorkerLock). While the gate runs it queries the workflow's progress every 2 s, and once more at the end,
// and prints each step as it began and each item's end.
// When ctx ends, at laug's Ctrl-C, it detaches and prints the status and cancel commands; the gate runs on. At the
// end it checks S1 on the spot: the verdict the workflow returned against <dir>/verdict.json, byte for byte, then
// bin/laug gate rejudge -brief unless verdict.json is a trap verdict. It returns the gate's exit: 0 ADVANCE, 1 BLOCK,
// 2 no verdict, which laug gate run -workflow exits with.
//
// With -otlp 127.0.0.1:<port> (laug gate run -workflow -otlp 127.0.0.1:7751) its client traces the start through
// the SDK's OpenTelemetry interceptor, service laug-gate: one StartWorkflow:Gate span, whose context the workflow's
// first event carries to the worker; queries make none. Without it, the default, it builds no interceptor and no
// provider (trace.go).
//
// The same client serves laug's other verbs on a gate that runs as workflow Gate. -status <gid> describes workflow
// gate-<gid> and queries its progress; it needs no worker for the first line. -cancel <gid> asks a running gate-<gid>
// to cancel, and refuses one that is not running. -terminate <gid> -reason <why> ends it at once, history recording
// 'laug: <why>'. -release <gid> runs the stop path by hand, for a gate a terminate, a lost db or a stuck workflow
// task left: it stops the live step by its record, then runs stopped, post stopped and release stopped, as Gate's
// stop path does.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/sdk/client"
)

// starterIdentity is the starter's identity in history; the SDK's default names the host (SDK v1.49.0
// getWorkerIdentity).
const starterIdentity = "laug-gate"

var (
	tagRE    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	itemsRE  = regexp.MustCompile(`^[1-9][0-9]*$`)
	listenRE = regexp.MustCompile(`^127\.0\.0\.1:[1-9][0-9]{0,4}$`)
)

// starter is one StarterMain call: the checkout's root, the environment laug built for it, which it reads in place of
// the process's, its two streams, and -otlp's tracing, nil without it, whose interceptor every client it dials gets.
type starter struct {
	repo     string
	environ  []string
	laug     string // the word a line to type starts with (laugWord)
	out, err io.Writer
	tracing  *Tracing
}

// StarterMain parses args with its own flags, the ones the gate starter binary took and -cancel, -terminate and -reason,
// and runs one mode on ctx: the gate, or -status, -cancel, -terminate or -release on one. cmd is the laug command it
// runs for, gate run, status, cancel, terminate or stop, which names its refusal: "laug · <cmd>: <why>". environ is
// laug gate run's environment, or the verb's: TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE, the gate's knobs and seams. It
// returns the mode's exit: the gate's 0, 1 or 2; 2 refused, or a flag it does not take.
func StarterMain(ctx context.Context, cmd string, args, environ []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "the checkout's root; laug passes its own")
	status := f.String("status", "", "a gate id: describe workflow gate-<gid> and query its progress")
	release := f.String("release", "", "a gate id: the stop path by hand, after a terminate, a lost db or a stuck "+
		"workflow task")
	cancel := f.String("cancel", "", "a gate id: ask workflow gate-<gid> to cancel, while it runs")
	terminate := f.String("terminate", "", "a gate id: end workflow gate-<gid> at once, with -reason")
	reason := f.String("reason", "", "with -terminate, why; history records it as 'laug: <why>'")
	otlp := f.String("otlp", "", "127.0.0.1:<port>: trace the start over OTLP/HTTP to the stack's Collector (laug's "+
		"-otlp); empty, the default, traces nothing")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	s := &starter{repo: *repo, environ: environ, laug: laugWord(environ), out: stdout, err: stderr}
	who := "laug · " + cmd
	tr, err := NewTracing(*otlp, "laug-gate", func(err error) {
		fmt.Fprintf(stderr, "%s: otlp: spans not sent: %v\n", who, err)
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", who, err)
		return 2
	}
	if tr != nil {
		fmt.Fprintf(stdout, "%s: traces to %s (OTLP over HTTP, service laug-gate)\n", who, tr.Endpoint())
	}
	s.tracing = tr
	code, err := s.run(ctx, *status, *release, *cancel, *terminate, *reason)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", who, err)
	}
	// The last spans go out here: 2 s at most.
	if err := tr.Stop(); err != nil {
		fmt.Fprintf(stderr, "%s: otlp: at stop: %v\n", who, err)
	}
	return code
}

// get is the environment's value for name, or def when it is unset or empty.
func (s *starter) get(name, def string) string {
	if v := getenv(s.environ, name); v != "" {
		return v
	}
	return def
}

// input is laug gate run's environment as the workflow's input, with its defaults where laug did not set one.
func (s *starter) input() (GateInput, error) {
	in := GateInput{
		ServerTag:  getenv(s.environ, "SERVER_TAG"),
		Plan:       s.get("PLAN", "gate/plan.yaml"),
		Items:      getenv(s.environ, "ITEMS"),
		Post:       s.get("GATE_POST", "local"),
		Base:       s.get("BASE", "main"),
		OmesDir:    s.get("OMES_DIR", ".upstream/omes"),
		Results:    s.get("GATE_RESULTS", "gate/results"),
		Lock:       s.get("GATE_LOCK", "gate/.lock"),
		HostLock:   s.get("GATE_HOST_LOCK", filepath.Join(s.get("XDG_RUNTIME_DIR", "/tmp"), "laug-gate.lock")),
		PoolListen: s.get("GATE_POOL_LISTEN", "127.0.0.1:7700"),
	}
	if !tagRE.MatchString(in.ServerTag) {
		return in, fmt.Errorf("SERVER_TAG must be a release tag, x.y.z; got %q", in.ServerTag)
	}
	if in.Items != "" && !itemsRE.MatchString(in.Items) {
		return in, fmt.Errorf("ITEMS=%s: how many of the plan's items to run, from 1", in.Items)
	}
	if !listenOK(in.PoolListen) {
		return in, errors.New("GATE_POOL_LISTEN must be 127.0.0.1:<port>: the gate's pool has no auth (SECURITY.md)")
	}
	switch fake := getenv(s.environ, "GATE_FAKE"); fake {
	case "":
	case "1":
		in.Fake = true
		in.Extra = fakeKnobs(s.environ)
	default:
		return in, fmt.Errorf("GATE_FAKE is 1 or empty, not %q", fake)
	}
	return in, nil
}

func listenOK(l string) bool {
	port, err := strconv.Atoi(strings.TrimPrefix(l, "127.0.0.1:"))
	return listenRE.MatchString(l) && err == nil && port <= 65535
}

// fakeKnobs are the fake gate's knobs from environ: GATE_BACKEND and FAKE_GATE_*, as make harness-test sets them, and
// laug passes them on as seams.
func fakeKnobs(environ []string) map[string]string {
	m := map[string]string{}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if _, seen := m[name]; seen {
			continue // the first entry wins, as getenv's
		}
		if name == "GATE_BACKEND" || strings.HasPrefix(name, "FAKE_GATE_") {
			m[name] = value
		}
	}
	return m
}

// server is the address and namespace laug passes: TEMPORAL_ADDRESS (its -orchestrator) and TEMPORAL_NAMESPACE
// (default, and default when empty). verb is the laug verb that runs this mode.
func (s *starter) server(verb string) (addr, ns string, err error) {
	addr = getenv(s.environ, "TEMPORAL_ADDRESS")
	if addr == "" {
		return "", "", fmt.Errorf("TEMPORAL_ADDRESS is empty: run %s %s, which sets it from -orchestrator", s.laug, verb)
	}
	return addr, s.get("TEMPORAL_NAMESPACE", "default"), nil
}

func (s *starter) dial(ctx context.Context, addr, ns string) (client.Client, error) {
	opts := client.Options{HostPort: addr, Namespace: ns, Identity: starterIdentity,
		Interceptors: s.tracing.ClientInterceptors()}
	if key := getenv(s.environ, "TEMPORAL_API_KEY"); key != "" {
		opts.Credentials = client.NewAPIKeyStaticCredentials(key)
	}
	return client.DialContext(ctx, opts)
}

// git runs git -C <repo> args with the environment laug built, and returns its stdout.
func (s *starter) git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", s.repo}, args...)...)
	cmd.Env = s.environ
	return cmd.Output()
}

func (s *starter) run(ctx context.Context, status, release, cancel, terminate, reason string) (int, error) {
	if s.repo == "" {
		return 2, fmt.Errorf("-repo is required: run %s gate run -workflow", s.laug)
	}
	var modes []string
	for _, m := range []struct{ name, gid string }{{"-status", status}, {"-release", release}, {"-cancel", cancel},
		{"-terminate", terminate}} {
		if m.gid != "" {
			modes = append(modes, m.name)
		}
	}
	switch {
	case len(modes) > 1:
		return 2, fmt.Errorf("%s are different things: name one", strings.Join(modes, " and "))
	case reason != "" && terminate == "":
		return 2, errors.New("-reason goes with -terminate")
	case status != "":
		return s.status(ctx, status)
	case release != "":
		return s.release(ctx, release)
	case cancel != "":
		return s.cancel(ctx, cancel)
	case terminate != "":
		return s.terminate(ctx, terminate, reason)
	}
	in, err := s.input()
	if err != nil {
		return 2, err
	}
	addr, ns, err := s.server("gate run -workflow")
	if err != nil {
		return 2, err
	}
	runDir := filepath.Join(s.repo, RunDir)
	queue, err := Queue(runDir, false, s.laug)
	if err != nil {
		return 2, err
	}
	holder, err := WorkerHolder(runDir)
	if err != nil {
		return 2, err
	}
	wpid := holder.Pid
	if wpid == 0 {
		return 2, fmt.Errorf("no worker runs for this checkout (no process holds %s): %s orchestrator worker",
			filepath.Join(RunDir, WorkerLock), s.laug)
	}
	// The worker's identity, as WorkerMain names it from laug's build: HEAD's commit, -dirty when git status printed
	// anything at make build. A lock holder or a poller with neither form was built before the last commit or in
	// another worktree.
	head, err := s.git("rev-parse", "HEAD")
	if err != nil {
		return 2, fmt.Errorf("git rev-parse HEAD in the checkout: %w", err)
	}
	rev := strings.TrimSpace(string(head))
	if len(rev) != 40 || strings.Trim(rev, "0123456789abcdef") != "" {
		return 2, fmt.Errorf("git rev-parse HEAD gave %q, not a commit", rev)
	}
	porcelain, err := s.git("status", "--porcelain")
	if err != nil {
		return 2, fmt.Errorf("git status in the checkout: %w", err)
	}
	clean, dirty := WorkerIdentity(rev, ""), WorkerIdentity(rev, "true")
	if len(porcelain) == 0 {
		dirty = clean
	}
	if err := checkHolder(holder, clean, dirty, s.laug); err != nil {
		return 2, err
	}

	dctx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
	c, err := s.dial(dctx, addr, ns)
	cancelDial()
	if err != nil {
		return 2, fmt.Errorf("dial %s: %w (is %s orchestrator server running?)", addr, err, s.laug)
	}
	defer c.Close()

	dq, err := c.DescribeTaskQueue(ctx, queue, enumspb.TASK_QUEUE_TYPE_WORKFLOW)
	if err != nil {
		return 2, fmt.Errorf("describe %s: %w", queue, err)
	}
	var pollers []string
	for _, p := range dq.GetPollers() {
		pollers = append(pollers, p.GetIdentity())
	}
	if _, err := checkPollers(pollers, clean, dirty, queue, wpid, s.laug); err != nil {
		return 2, err
	}
	if holder.Identity != clean {
		fmt.Fprintf(s.out, "laug · the worker %s was built from a changed tree; gate.json will say tree_dirty true\n",
			holder.Identity)
	}
	return s.runGate(ctx, c, in, queue, addr, ns, wpid, pollers)
}

// checkHolder is the decision that holds: the identity the lock's holder wrote must be this checkout's worker at HEAD,
// clean, or dirty while the tree has changes (dirty is clean when it has none). The poller list can name HEAD's worker
// for about 5 min after its last poll
// (docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt section 14), while another commit's worker holds
// the lock and would run the gate. An empty identity is a holder that has not written its line yet (LockWorker writes
// it just after F_SETLK), a build from before the lock held one, or a line that names another pid (Holder).
func checkHolder(h Holder, clean, dirty, laug string) error {
	if h.Identity != "" && (h.Identity == clean || h.Identity == dirty) {
		return nil
	}
	head := strings.TrimPrefix(clean, "laug-worker@")
	if dirty != clean {
		head += " with changes"
	}
	if h.Identity == "" {
		return fmt.Errorf("the worker holding the lock, pid %d, wrote no identity into %s yet, or is a build from before the lock held one, this checkout's HEAD is %s: make build, then restart %s orchestrator worker",
			h.Pid, filepath.Join(RunDir, WorkerLock), head, laug)
	}
	return fmt.Errorf("the worker holding the lock is %s, this checkout's HEAD is %s: make build, then restart %s orchestrator worker", h.Identity, head, laug)
}

// checkPollers is the poller decision: some poller must be this checkout's worker at HEAD, clean or dirty.
func checkPollers(pollers []string, clean, dirty, queue string, wpid int, laug string) (atHead string, err error) {
	for _, id := range pollers {
		if id == clean || id == dirty {
			atHead = id
		}
	}
	if len(pollers) == 0 {
		return "", fmt.Errorf("worker pid %d runs, but the server lists no poller on %s yet; after a server restart that lasts about 13 s: try again", wpid, queue)
	}
	if atHead == "" {
		return "", fmt.Errorf("no poller on %s is %s%s: pollers %s; the worker was built before the last commit or in another worktree: make build, then restart %s orchestrator worker",
			queue, clean, dirtyText(clean, dirty), strings.Join(pollers, ", "), laug)
	}
	return atHead, nil
}

// runGate is the default mode: check, then workflow Gate as gate-<gid>.
func (s *starter) runGate(ctx context.Context, c client.Client, in GateInput, queue, addr, ns string, wpid int,
	pollers []string) (int, error) {
	ca, err := StartRun(ctx, s.repo, in, s.environ)
	if err != nil {
		fmt.Fprintf(s.err, "laug · gate/step.sh check: %s\n", errText(err))
		return 2, nil
	}
	gid := ca.GateID
	id := "gate-" + gid
	fmt.Fprintf(s.out, "laug · gate %s: workflow %s on %s at %s, worker pid %d, pollers %s; server %s, plan %s%s, "+
		"results %s%s\n", gid, id, queue, addr, wpid, strings.Join(pollers, ", "), in.ServerTag, in.Plan,
		itemsText(in.Items), ca.Dir, fakeText(in.Fake))
	// No WorkflowRunTimeout or WorkflowExecutionTimeout: a run timeout ends the run as a terminate does, with no stop
	// path. Without WorkflowExecutionErrorWhenAlreadyStarted the SDK would return the existing run (SDK v1.49.0
	// workflowClientInterceptor.ExecuteWorkflow, StartWorkflowOptions.WorkflowExecutionErrorWhenAlreadyStarted).
	opts := client.StartWorkflowOptions{ID: id, TaskQueue: queue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true}
	wr, err := c.ExecuteWorkflow(ctx, opts, Gate, GateRunInput{GateInput: in, GID: gid, Dir: ca.Dir})
	if err != nil {
		if wr = s.afterStartError(ctx, c, in, ca, queue, err); wr == nil {
			return 2, nil
		}
	}
	mv := LaugFlags(addr, ns)
	fmt.Fprintf(s.out, "laug · status: %s gate status %s%s; cancel: %s gate cancel %s%s\n", s.laug, gid, mv, s.laug,
		gid, mv)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		followProgress(ctx, c, id, wr.GetRunID(), s.out, done)
	}()
	var res GateResult
	err = wr.Get(ctx, &res)
	close(done)
	wg.Wait()
	if ctx.Err() != nil {
		// One command a line, each ending with its last value, so a copy takes no punctuation with it.
		fmt.Fprintf(s.out, "laug · detached: workflow %s runs on.\n  status: %s gate status %s%s\n  cancel: %s gate "+
			"cancel %s%s\n  or:     temporal workflow cancel %s --workflow-id %s\n", id, s.laug, gid, mv, s.laug, gid, mv,
			CLIFlags(addr, ns), id)
		return 2, nil
	}
	if err != nil {
		return 2, fmt.Errorf("%s: %w", id, err)
	}
	s.report(s.out, in, gid, res)
	if (res.End == "done" || res.End == "stopped") && res.Exit >= 0 && res.Exit <= 2 {
		return res.Exit, nil
	}
	return 2, nil
}

// afterStartError acts on a failed start, as decideStart decides it: it prints the decision's line, and returns the
// run to follow, or nil. The server is asked on a fresh context, since ctx may be the one a Ctrl-C ended.
func (s *starter) afterStartError(ctx context.Context, c client.Client, in GateInput, ca CheckAnswer, queue string,
	err error) client.WorkflowRun {
	id := "gate-" + ca.GateID
	describe := func() (*workflowpb.WorkflowExecutionInfo, error) {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d, err := c.DescribeWorkflowExecution(dctx, id, "")
		return d.GetWorkflowExecutionInfo(), err
	}
	remove := func() (bool, error) { return RemoveEmptyRun(s.repo, in, ca.GateID) }
	d := decideStart(id, ca.Dir, queue, err, describe, remove)
	if !d.follow {
		fmt.Fprintln(s.err, d.line)
		return nil
	}
	fmt.Fprintln(s.out, d.line)
	return c.GetWorkflow(ctx, id, d.runID)
}

// startDecision is what a failed start means: follow, with runID, when the run under the id is this start's; line is
// what to print, to stdout when it follows, else to stderr.
type startDecision struct {
	follow bool
	runID  string
	line   string
}

// decideStart decides after a start error. Workflow ids are namespace-wide and one dev server serves every checkout on
// the host, while check names the run g<UTC second> per results directory: AlreadyStarted means another checkout
// started gate-<gid> in the same second, or earlier (REJECT_DUPLICATE), and the run's own directory, still empty, is
// removed. Any other error asks the server (describe): a Gate running under the id on this queue is this start's, and
// is followed; NotFound removes the empty directory, or leaves one that is not empty; anything else leaves it and
// says what the server said. remove is RemoveEmptyRun on the run's directory; each func is called only where needed.
func decideStart(id, dir, queue string, startErr error, describe func() (*workflowpb.WorkflowExecutionInfo, error), remove func() (bool, error)) startDecision {
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(startErr, &already) {
		_, _ = remove()
		return startDecision{line: fmt.Sprintf("laug · %s belongs to another run (another checkout started a gate in the same second): start again", id)}
	}
	info, derr := describe()
	var nf *serviceerror.NotFound
	switch {
	case derr == nil && info.GetStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING && info.GetTaskQueue() == queue:
		return startDecision{follow: true, runID: info.GetExecution().GetRunId(),
			line: fmt.Sprintf("laug · the start answered %v, but %s runs on %s", startErr, id, queue)}
	case errors.As(derr, &nf):
		removed, rerr := remove()
		switch {
		case removed:
			return startDecision{line: fmt.Sprintf("laug · start %s failed: %v; removed %s", id, startErr, dir)}
		case errors.Is(rerr, syscall.ENOTEMPTY) || errors.Is(rerr, syscall.EEXIST):
			return startDecision{line: fmt.Sprintf("laug · start %s failed: %v; left %s: it is not empty", id, startErr, dir)}
		default:
			return startDecision{line: fmt.Sprintf("laug · start %s failed: %v; left %s: %v", id, startErr, dir, rerr)}
		}
	default:
		says := fmt.Sprint(derr)
		if derr == nil {
			says = fmt.Sprintf("%s on %s", statusText(info.GetStatus()), info.GetTaskQueue())
		}
		return startDecision{line: fmt.Sprintf("laug · start %s failed: %v; the server says %s; left %s", id, startErr, says, dir)}
	}
}

// followProgress queries the workflow's progress at once and every 2 s until done is closed, then once more, the last
// answer, so the steps and item ends of the last 2 s print too; after a Ctrl-C it stops at once. progressPrinter
// decides what an answer prints.
func followProgress(ctx context.Context, c client.Client, id, runID string, w io.Writer, done <-chan struct{}) {
	var pp progressPrinter
	query := func(qctx context.Context, last bool) {
		qctx, cancel := context.WithTimeout(qctx, 5*time.Second)
		defer cancel()
		v, err := c.QueryWorkflow(qctx, id, runID, "progress")
		var p GateProgress
		if err == nil {
			err = v.Get(&p)
		}
		pp.print(w, p, err, ctx.Err() != nil, last)
	}
	last := func() {
		if ctx.Err() == nil {
			query(context.Background(), true)
		} else {
			pp.flush(w) // detached: no last answer, so the lines held print as they are
		}
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			last()
			return
		default:
		}
		query(ctx, false)
		select {
		case <-done:
			last()
			return
		case <-t.C:
		}
	}
}

// progressPrinter prints the progress answers as they come, one list in time order: a line per step as it began,
// `<HH:MM:SS> UTC <id>`, and a line per item end, `<HH:MM:SS> UTC  item <k>: exit <n>[ after TERM|KILL]`, each once.
// A line prints once two answers in a row give it the same time, so about 2 s after the event, and the last answer
// prints what is left: a worker that has just restarted can answer a query on the clock of the workflow task before
// (on a scratch dev server, judge and both item ends came 33 s early, at the wave's task time, once in 2 runs of a
// worker KILLed mid-wave), and the next answer has the true time. Lines print in order, so one that waits holds back
// the lines after it. A failed query prints once, until one succeeds, and not at all after a Ctrl-C (quiet); with no
// last answer, after a Ctrl-C or a failed last query, the lines held print as they are.
type progressPrinter struct {
	printed map[string]bool // the lines printed, by key
	held    []progressLine  // the last answer's lines not printed yet, in order
	failed  bool
}

// progressLine is one line: key is "step <n> <id>" (n its place in Began) or "item <k>".
type progressLine struct {
	key  string
	at   time.Time
	text string
}

// progressLines is an answer's steps and item ends in one list by time: steps in Began's order, item ends by time
// then key, an item end before a step of the same time. Items an answer gives (the wave's, stopped's) are learned in
// the workflow task that schedules the next step, so they share its time and came first.
func progressLines(p GateProgress) []progressLine {
	var ls []progressLine
	for _, e := range itemsByTime(p.Items) {
		ls = append(ls, progressLine{"item " + e.Key, e.At, fmt.Sprintf("%s UTC  item %s", e.At.UTC().Format("15:04:05"), itemLine(e.ItemEnd))})
	}
	for i, m := range p.Began {
		ls = append(ls, progressLine{fmt.Sprintf("step %d %s", i, m.ID), m.At, fmt.Sprintf("%s UTC %s", m.At.UTC().Format("15:04:05"), m.ID)})
	}
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].at.Before(ls[j].at) })
	return ls
}

// flush prints the lines held, as they are.
func (pp *progressPrinter) flush(w io.Writer) {
	for _, l := range pp.held {
		fmt.Fprintln(w, l.text)
	}
	pp.held = nil
}

func (pp *progressPrinter) print(w io.Writer, p GateProgress, err error, quiet, last bool) {
	if pp.printed == nil {
		pp.printed = map[string]bool{}
	}
	if err != nil {
		if !pp.failed && !quiet {
			fmt.Fprintf(w, "progress: no answer (%v)\n", err)
		}
		pp.failed = true
		if last {
			pp.flush(w) // no last answer: the lines held are all there is
		}
		return
	}
	pp.failed = false
	before := map[string]time.Time{}
	for _, l := range pp.held {
		before[l.key] = l.at
	}
	var held []progressLine
	for _, l := range progressLines(p) {
		if pp.printed[l.key] {
			continue
		}
		if t, ok := before[l.key]; len(held) == 0 && (last || (ok && t.Equal(l.at))) {
			fmt.Fprintln(w, l.text)
			pp.printed[l.key] = true
			continue
		}
		held = append(held, l)
	}
	pp.held = held
}

func itemsByTime(m map[string]ItemEndAt) []ItemEndAt {
	var es []ItemEndAt
	for _, e := range m {
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool {
		if !es[i].At.Equal(es[j].At) {
			return es[i].At.Before(es[j].At)
		}
		return es[i].Key < es[j].Key
	})
	return es
}

// itemLine is "<k>: exit <n>[ after TERM|KILL]", or "<k>: not found: <why>".
func itemLine(e ItemEnd) string {
	if !e.Found {
		return fmt.Sprintf("%s: not found: %s", e.Key, e.Why)
	}
	x := "null"
	if e.Exit != nil {
		x = strconv.Itoa(*e.Exit)
	}
	s := fmt.Sprintf("%s: exit %s", e.Key, x)
	if e.StoppedBy != nil && *e.StoppedBy != "" {
		s += " after " + *e.StoppedBy
	}
	return s
}

// report prints how the gate ended and S1: the bytes the workflow returned against verdict.json, then the rejudge in
// Go, bin/laug gate rejudge -brief, which a trap verdict is left out of (verdict.json's stopped_at, as rejudge's
// explain reads it). End alone cannot say: End is stopped too when the judge activity erred after bin/laug judge wrote
// verdict.json and judge.done says exit 0, and stopped then keeps the judge's verdict (gate/step.sh, stopped).
func (s *starter) report(w io.Writer, in GateInput, gid string, res GateResult) {
	word := res.VerdictWord
	if word == "" {
		word = "no verdict"
	}
	sent := ""
	if res.Stop != nil && res.Stop.Sent != "" {
		sent = ", stop sent " + res.Stop.Sent
	}
	fmt.Fprintf(w, "laug · gate %s: end %s, exit %d, %s%s\n", gid, res.End, res.Exit, word, sent)
	if res.Why != "" {
		fmt.Fprintf(w, "why: %s\n", res.Why)
	}
	dir := filepath.Join(absTo(s.repo, in.Results), gid)
	file, rerr := os.ReadFile(filepath.Join(dir, "verdict.json"))
	switch {
	case errors.Is(rerr, fs.ErrNotExist):
		fmt.Fprintln(w, "S1: no verdict.json")
	case rerr != nil:
		sum := sha256.Sum256(res.Verdict)
		fmt.Fprintf(w, "S1: %s/verdict.json could not be read (%v); the workflow returned %d bytes, sha256 %s\n", res.Dir, rerr, len(res.Verdict), hex.EncodeToString(sum[:12]))
	default:
		sum := sha256.Sum256(res.Verdict)
		if len(res.Verdict) == 0 {
			fmt.Fprintf(w, "S1: the workflow carried no verdict; %s/verdict.json has %d bytes\n", res.Dir, len(file))
		} else if bytes.Equal(file, res.Verdict) && res.SHA256 == hex.EncodeToString(sum[:]) {
			fmt.Fprintf(w, "S1: the workflow's verdict equals %s/verdict.json byte for byte (%d bytes, sha256 %s)\n", res.Dir, len(file), res.SHA256[:12])
		} else {
			fmt.Fprintf(w, "S1: the workflow's verdict DIFFERS from %s/verdict.json (%d against %d bytes)\n", res.Dir, len(res.Verdict), len(file))
		}
		var v struct {
			StoppedAt json.RawMessage `json:"stopped_at"`
		}
		_ = json.Unmarshal(file, &v)
		if at := strings.TrimSpace(string(v.StoppedAt)); at != "" && at != "null" {
			var s string
			if json.Unmarshal(v.StoppedAt, &s) != nil {
				s = at
			}
			fmt.Fprintf(w, "S1: left out: verdict.json is the trap verdict (stopped_at %s)\n", s)
		} else {
			cmd := exec.Command("bin/laug", "gate", "rejudge", "-brief", dir)
			cmd.Dir, cmd.Env = s.repo, s.environ
			out, err := cmd.Output()
			line := strings.TrimSpace(string(out))
			if line == "" && err != nil {
				line = err.Error()
			}
			fmt.Fprintf(w, "S1: rejudge: %s\n", line)
		}
	}
	fmt.Fprintln(w, word)
}

// status is -status: it describes gate-<gid> and queries its progress. No worker check: the first line is the
// server's.
func (s *starter) status(ctx context.Context, gid string) (int, error) {
	if !IsGateID(gid) {
		return 2, fmt.Errorf("-status %q: a gate id, g<date>-<time>, as %s gate run -workflow printed it", gid, s.laug)
	}
	addr, ns, err := s.server("gate status")
	if err != nil {
		return 2, err
	}
	id := "gate-" + gid
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.dial(dctx, addr, ns)
	if err != nil {
		fmt.Fprintf(s.err, "laug · gate %s: no server answered at %s (%v): %s orchestrator server\n", gid, addr, err,
			s.laug)
		return 2, nil
	}
	defer c.Close()
	d, err := c.DescribeWorkflowExecution(dctx, id, "")
	var nf *serviceerror.NotFound
	if errors.As(err, &nf) {
		fmt.Fprintf(s.err, "laug · gate %s: the server at %s, namespace %s, does not know workflow %s\n", gid, addr,
			ns, id)
		return 2, nil
	}
	if err != nil {
		fmt.Fprintf(s.err, "laug · gate %s: describe %s: %v\n", gid, id, err)
		return 2, nil
	}
	info := d.GetWorkflowExecutionInfo()
	fmt.Fprintln(s.out, statusLine(gid, id, info))
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	v, err := c.QueryWorkflow(qctx, id, "", "progress")
	var p GateProgress
	if err == nil {
		err = v.Get(&p)
	}
	if err != nil {
		fmt.Fprintf(s.out, "progress: no answer (%v); a query needs this checkout's worker (%s orchestrator "+
			"worker)\n", err, s.laug)
		return 0, nil
	}
	if p.Step != "" {
		vk := strings.TrimSpace(p.Verb + " " + p.Key)
		if vk != "" {
			vk = " (" + vk + ")"
		}
		fmt.Fprintf(s.out, "step %s%s, phase %s, since %s UTC\n", p.Step, vk, p.Phase,
			p.Since.UTC().Format("15:04:05"))
	}
	done := strings.Join(p.Done, ", ")
	if done == "" {
		done = "none"
	}
	fmt.Fprintf(s.out, "done: %s\n", done)
	fmt.Fprintf(s.out, "quiet passes: %d\n", p.QuietPasses)
	var ends []string
	for _, e := range itemsByTime(p.Items) {
		ends = append(ends, strings.Replace(itemLine(e.ItemEnd), ":", "", 1))
	}
	if len(ends) == 0 {
		ends = []string{"none"}
	}
	fmt.Fprintf(s.out, "items ended: %s\n", strings.Join(ends, ", "))
	fmt.Fprintf(s.out, "cancel requested: %s\n", map[bool]string{true: "yes", false: "no"}[p.CancelRequested])
	if p.Phase == "ended" {
		fmt.Fprintf(s.out, "end %s\n", p.End)
	}
	return 0, nil
}

// statusLine is -status's first line: "laug · gate <gid>: workflow <id> <status>, started <HH:MM:SS> UTC", then
// ", closed <HH:MM:SS> UTC" once it has closed.
func statusLine(gid, id string, info *workflowpb.WorkflowExecutionInfo) string {
	line := fmt.Sprintf("laug · gate %s: workflow %s %s, started %s UTC", gid, id, statusText(info.GetStatus()),
		info.GetStartTime().AsTime().UTC().Format("15:04:05"))
	if ct := info.GetCloseTime(); ct != nil && info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		line += ", closed " + ct.AsTime().UTC().Format("15:04:05") + " UTC"
	}
	return line
}

// cancel is -cancel, laug gate cancel: it asks the server for workflow gate-<gid>'s status first, since a cancel
// request on an ended workflow changes nothing, and asks a running one to cancel. Before the judge, the gate's stop
// path stops the live step, writes a BLOCK that names it, hands it on and releases; from the judge on nothing changes.
func (s *starter) cancel(ctx context.Context, gid string) (int, error) {
	if !IsGateID(gid) {
		return 2, fmt.Errorf("-cancel %q: a gate id, g<date>-<time>, as %s gate run -workflow printed it", gid, s.laug)
	}
	addr, ns, err := s.server("gate cancel")
	if err != nil {
		return 2, err
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.dial(dctx, addr, ns)
	if err != nil {
		fmt.Fprintf(s.err, "laug · gate cancel: no server answered at %s (%v), so whether gate-%s runs is not "+
			"known; nothing done\n", addr, err, gid)
		return 2, nil
	}
	defer c.Close()
	id := "gate-" + gid
	describe := func() (*workflowpb.WorkflowExecutionInfo, error) {
		d, err := c.DescribeWorkflowExecution(dctx, id, "")
		return d.GetWorkflowExecutionInfo(), err
	}
	return cancelGate(gid, addr, ns, s.laug, describe, func() error { return c.CancelWorkflow(dctx, id, "") }, s.out,
		s.err), nil
}

// cancelGate is cancel's decision on what describe answers: a refusal, exit 2, unless gate-<gid> runs; else its
// first line, then send, the cancel request, and its line, exit 0; 1 when the request fails.
func cancelGate(gid, addr, ns, laug string, describe func() (*workflowpb.WorkflowExecutionInfo, error), send func() error,
	out, errw io.Writer) int {
	id, mv := "gate-"+gid, LaugFlags(addr, ns)
	info, err := describe()
	var nf *serviceerror.NotFound
	switch {
	case errors.As(err, &nf):
		fmt.Fprintf(errw, "laug · gate cancel: the server at %s, namespace %s, does not know workflow %s; nothing "+
			"done\n", addr, ns, id)
		return 2
	case err != nil:
		fmt.Fprintf(errw, "laug · gate cancel: describe %s: %v, so whether it runs is not known; nothing done\n", id,
			err)
		return 2
	case info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
		fmt.Fprintf(errw, "laug · gate cancel: workflow %s is %s, not running: nothing to cancel; how it ended: "+
			"%s gate status %s%s\n", id, strings.ToLower(statusText(info.GetStatus())), laug, gid, mv)
		return 2
	}
	fmt.Fprintf(out, "laug · gate cancel %s: before the judge its stop path stops the live step, writes a BLOCK that "+
		"names it, hands it on and releases; from the judge on nothing changes; its status: %s gate status %s%s\n",
		gid, laug, gid, mv)
	if err := send(); err != nil {
		fmt.Fprintf(errw, "laug · gate cancel: the cancel request for %s: %v\n", id, err)
		return 1
	}
	fmt.Fprintf(out, "cancel requested for workflow %s\n", id)
	return 0
}

// terminate is -terminate, laug gate terminate: workflow gate-<gid> ends at once, history recording 'laug: <why>'
// as the reason. No workflow code runs, so the gate gets no stop path: laug gate stop runs it by hand after.
func (s *starter) terminate(ctx context.Context, gid, reason string) (int, error) {
	if !IsGateID(gid) {
		return 2, fmt.Errorf("-terminate %q: a gate id, g<date>-<time>, as %s gate run -workflow printed it", gid,
			s.laug)
	}
	if reason == "" {
		return 2, errors.New("-terminate takes -reason: history records why")
	}
	addr, ns, err := s.server("gate terminate")
	if err != nil {
		return 2, err
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.dial(dctx, addr, ns)
	if err != nil {
		return 2, fmt.Errorf("dial %s: %w (is %s orchestrator server running?)", addr, err, s.laug)
	}
	defer c.Close()
	id := "gate-" + gid
	if err := c.TerminateWorkflow(dctx, id, "", "laug: "+reason); err != nil {
		return 1, fmt.Errorf("terminate %s: %w", id, err)
	}
	fmt.Fprintf(s.out, "terminated workflow %s, reason 'laug: %s'\n", id, reason)
	return 0, nil
}

// release is -release, laug gate stop: the stop path by hand. The gate's own SERVER_TAG, laugd address and GATE_FAKE
// come from its gate.json (gateJSONInput), so release reaches the gate's own laugd; FAKE_GATE_* from the environment.
// A gate not under the GATE_RESULTS laug passes (-results), or a fake one without FAKE_GATE_DIR, is refused before
// anything runs, with the line that has them when the server still holds the gate's input (its first event).
func (s *starter) release(ctx context.Context, gid string) (int, error) {
	if !IsGateID(gid) {
		return 2, fmt.Errorf("-release %q: a gate id, g<date>-<time>, as %s gate run -workflow printed it", gid, s.laug)
	}
	in, err := s.input()
	if err != nil {
		return 2, err
	}
	addr, ns, err := s.server("gate stop")
	if err != nil {
		return 2, err
	}
	id := "gate-" + gid
	mv := LaugFlags(addr, ns)
	// hint is the release command with the gate's own -results and FAKE_GATE_DIR, from the server, or "".
	hint := func() string {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		c, err := s.dial(hctx, addr, ns)
		if err != nil {
			return ""
		}
		defer c.Close()
		gi, err := StartedGateInput(hctx, c, id, "")
		if err != nil {
			return ""
		}
		return fmt.Sprintf("; workflow %s ran with these:\n  %s", id, ReleaseLine(s.laug, gid, gi, mv))
	}
	shown := filepath.Join(in.Results, gid)
	dir := filepath.Join(absTo(s.repo, in.Results), gid)
	if st, err := os.Stat(filepath.Join(dir, ".state.json")); err != nil || !st.Mode().IsRegular() {
		fmt.Fprintf(s.err, "laug · %s is no gate run gate/step.sh made%s\n", shown, hint())
		return 2, nil
	}
	// A fake gate with no FAKE_GATE_DIR is refused below, once the server has said whether there is anything to do.
	in, fromJSON, err := gateJSONInput(in, dir, shown, s.environ)
	var noFakeDir error
	if errors.Is(err, errNoFakeDir) {
		noFakeDir, err = err, nil
	}
	if err != nil {
		return 2, err
	}
	if !fromJSON {
		fmt.Fprintf(s.out, "laug · no gate.json: SERVER_TAG %s from laug\n", in.ServerTag)
	}
	what := "no server answered"
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if c, err := s.dial(dctx, addr, ns); err == nil {
		d, err := c.DescribeWorkflowExecution(dctx, id, "")
		var nf *serviceerror.NotFound
		status := d.GetWorkflowExecutionInfo().GetStatus()
		switch {
		case err == nil && status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
			cancel()
			c.Close()
			fmt.Fprintf(s.err, "laug · workflow %s runs. Cancel it, and its own stop path does this:\n  %s gate "+
				"cancel %s%s\nor, when it is stuck, terminate it, then run %s gate stop again:\n  %s gate terminate "+
				"%s -reason 'stuck; laug gate stop follows'%s\n", id, s.laug, gid, mv, s.laug, s.laug, gid, mv)
			return 2, nil
		case err == nil && status == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED &&
			fileExists(filepath.Join(dir, ".steps", "release.done")):
			// Gate ran its own path through release: a release by hand would only add stopped's files to the run.
			var res GateResult
			gerr := c.GetWorkflow(dctx, id, "").Get(dctx, &res)
			cancel()
			c.Close()
			if gerr != nil {
				fmt.Fprintf(s.out, "laug · workflow %s is Completed and ran its own path through release; its "+
					"result: %v. Nothing to release by hand\n", id, gerr)
				return 2, nil
			}
			fmt.Fprintf(s.out, "laug · workflow %s is Completed: it ran its own path through release, end %s, exit "+
				"%d, %s. Nothing to release by hand\n", id, res.End, res.Exit, wordOf(res))
			if (res.End == "done" || res.End == "stopped") && res.Exit >= 0 && res.Exit <= 2 {
				return res.Exit, nil
			}
			return 2, nil
		case err == nil:
			what = statusText(status)
		case errors.As(err, &nf):
			what = "the server does not know it"
		default:
			what = fmt.Sprintf("unknown (%v)", err)
		}
		c.Close()
	}
	cancel()
	if noFakeDir != nil {
		fmt.Fprintf(s.err, "laug · %v%s\n", noFakeDir, hint())
		return 2, nil
	}
	if what == "the server does not know it" || what == "no server answered" {
		fmt.Fprintf(s.out, "laug · release gate %s: workflow %s: %s\n", gid, id, what)
	} else {
		fmt.Fprintf(s.out, "laug · release gate %s: workflow %s is %s\n", gid, id, what)
	}
	return releaseByHand(ctx, &GateActivities{Repo: s.repo, Environ: s.environ}, in, gid, s.out)
}

// errNoFakeDir: a fake gate's release needs FAKE_GATE_DIR, where the fake docker keeps its projects.
var errNoFakeDir = errors.New("needs FAKE_GATE_DIR")

// gateJSONInput is in with gate.json's server_tag, pool and fake in place of laug's SERVER_TAG, GATE_POOL_LISTEN and
// GATE_FAKE, so release reaches the gate's own laugd, and Extra from this environment (GATE_BACKEND and FAKE_GATE_*)
// for a fake gate, else none. found is false when dir holds no gate.json: in is laug's, unchanged. A gate.json without
// a tag x.y.z and a pool 127.0.0.1:<port> is an error, and so is a fake one when FAKE_GATE_DIR is not set
// (errNoFakeDir): the fake docker's compose ls fails without it, and release could not say what it left.
func gateJSONInput(in GateInput, dir, shown string, environ []string) (out GateInput, found bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "gate.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return in, false, nil
	}
	if err != nil {
		return in, false, err
	}
	var g struct {
		ServerTag string `json:"server_tag"`
		Pool      string `json:"pool"`
		Fake      bool   `json:"fake"`
	}
	if err := json.Unmarshal(b, &g); err != nil || !tagRE.MatchString(g.ServerTag) || !listenOK(g.Pool) {
		return in, true, fmt.Errorf("%s/gate.json does not name a server_tag x.y.z and a pool 127.0.0.1:<port>", shown)
	}
	in.ServerTag, in.PoolListen, in.Fake = g.ServerTag, g.Pool, g.Fake
	in.Extra = nil
	if in.Fake {
		in.Extra = fakeKnobs(environ)
		if in.Extra["FAKE_GATE_DIR"] == "" {
			return in, true, fmt.Errorf("%s/gate.json says fake: the release %w, as %s gate run -workflow had it", shown, errNoFakeDir, laugWord(environ))
		}
	}
	return in, true, nil
}

// wordOf is the result's verdict word, or "no verdict".
func wordOf(res GateResult) string {
	if res.VerdictWord == "" {
		return "no verdict"
	}
	return res.VerdictWord
}

// statusText is a workflow's status as the CLI prints it: Running, Completed, Terminated and so on.
func statusText(s enumspb.WorkflowExecutionStatus) string { return s.String() }

func itemsText(n string) string {
	if n == "" {
		return ""
	}
	return " (its first " + n + ")"
}

func fakeText(f bool) string {
	if f {
		return ", GATE_FAKE=1: no Docker, cell or Omes"
	}
	return ""
}

func dirtyText(clean, dirty string) string {
	if dirty == clean {
		return ""
	}
	return " or " + dirty
}

func absTo(repo, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(repo, p)
}
