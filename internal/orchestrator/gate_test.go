package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"
)

// gateFakes stand in for Step, Stop and AwaitItem, registered under the real names: each records its activity id
// (activity.GetInfo), the timeouts its activity got and its attempt, and Step answers from a table. The SDK's test env
// resolves a canceled activity at once and honors WaitForCancellation for child workflows only (SDK v1.49.0
// RequestCancelActivity and RequestCancelExternalWorkflow of the test env), so these tests show the order of the steps
// and what the workflow decides, not what a cancel does to a running step: steps_test.go, stepsh_test.go and the dev
// server do. Every env pins its start to the fake open's t0_ms, so the backstop each test sees is exact: the mock clock
// starts at wall time otherwise (setStartTime) and moves only when a timer fires (autoFireNextTimer).
type gateFakes struct {
	mu           sync.Mutex
	order        []string             // Step and Stop, by activity id
	awaits       []string             // AwaitItem, by activity id
	inputs       map[string]StepInput // by activity id
	times        map[string]string    // id: "<StartToClose> <ScheduleToClose> <Heartbeat>"
	attempts     map[string][]int32
	answers      map[string]string
	answer       func(in StepInput) (string, bool) // asked before answers and gateAnswers
	errs         map[string]error
	timeoutOnce  map[string]error // a step's first attempt ends with this error, after one beat; its retry answers
	cancelAt     string           // a step that asks for the workflow's cancel, then beats until its context ends
	cancelDuring string           // a step that asks for the workflow's cancel, then answers
	cancelAfter  time.Duration    // a cancel on the workflow's clock
	holdWave     bool             // the wave beats until its context ends, 3 s at most
	waveDelay    time.Duration    // the wave answers this much after both awaits answered
	awaitsDone   int
	holdAwaits   bool      // every await beats until its context ends, 3 s at most
	countKids    bool      // the spy counts the workflow context's children (optsSpy)
	stopExit     *int      // Stop's answer: Done, with this exit (143 when nil)
	stopCancel   bool      // Stop asks for the workflow's cancel, then answers
	verdictSize  int       // the judge's and stopped's verdict.json: this many bytes, when not 0
	start        time.Time // the env's start; t0 when zero
	spy          *optsSpy
	env          *testsuite.TestWorkflowEnvironment
	timers       []time.Duration
	cancelled    []string // activity ids, and "timer" for each timer cancelled

	interceptors []interceptor.WorkerInterceptor // after the spy, in this order: trace_test.go's tracing
	header       *commonpb.Header                // the workflow's start header, as a client's interceptor wrote it
	id           string                          // the workflow id; the env's own when empty
}

// t0 is the fake open's t0_ms.
var t0 = time.UnixMilli(1792152000123)

var gateAnswers = map[string]string{
	"open":         `{"result":"next","t0":{"wall":1792152000.123,"mono":81234.56},"t0_ms":1792152000123,"boot_id":"6f1c","locked":true}`,
	"preflight":    `{"result":"next","keys":["steady","pl"],"deadline_s":780,"kill_grace_s":30,"lease_ttl_s":1200,"quiet_s":10,"barrier_timeout_s":60,"poll_s":5,"harness_timeout_s":300,"wave_min_s":386}`,
	"laugd":        `{"result":"next","max_lease":"2h0m0.417123s"}`,
	"lease-steady": `{"result":"next","cell":"ab12cd34"}`,
	"lease-pl":     `{"result":"next","cell":"cd34ef56"}`,
	"barrier":      `{"result":"next","limit_ms":1792152231456}`,
	"wave-time":    `{"result":"next","left_s":563}`,
	"wave":         `{"result":"done","items":{"steady":{"exit":0,"stopped_by":null},"pl":{"exit":0,"stopped_by":null}}}`,
	"judge":        `{"result":"done","verdict":"ADVANCE","exit":0,"trap":false}`,
	"post":         `{"result":"done","exit":0}`,
	"release":      `{"result":"done","laugd":"stopped","released":2,"left":[]}`,
	"stopped":      `{"result":"done","stopped_at":"wave","verdict":"BLOCK","exit":1,"trap":true,"term":["pl"],"kill":[],"items":{"steady":{"exit":0,"stopped_by":null},"pl":{"exit":143,"stopped_by":"TERM"}}}`,
}

// fakeVerdict is the verdict.json a fake judge or stopped carries: its word, and the bytes the workflow must return.
func fakeVerdict(word string) ([]byte, string) {
	b := []byte(fmt.Sprintf("{\n  \"verdict\": %q,\n  \"wall\": 1792152000.120\n}\n", word))
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:])
}

// optsSpy records, per activity the workflow asks for, its id and ScheduleToStartTimeout (the test env shows an
// activity its other timeouts, not this one), and, when countKids is set, how many WithCancel children the workflow's
// context has at each activity and timer. Counting reads the SDK's unexported types (nearestCancel), so only
// TestGateKeepsOneCancelChild turns it on: an SDK whose layout differs fails that test alone, not every Gate test.
type optsSpy struct {
	interceptor.WorkerInterceptorBase
	countKids bool
	mu        sync.Mutex
	ids       []string
	sts       map[string]time.Duration
	root      workflow.Context
	// kids: "<id or timer d>: <children of the workflow's context>, <own>", own being "ctx" when the call's nearest
	// cancel context is the workflow's own, else "child" (waveCtx) or "other" (dctx).
	kids []string
}

func (s *optsSpy) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &spyIn{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, spy: s}
}

type spyIn struct {
	interceptor.WorkflowInboundInterceptorBase
	spy *optsSpy
}

func (i *spyIn) Init(out interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&spyOut{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: out}, spy: i.spy})
}

func (i *spyIn) ExecuteWorkflow(ctx workflow.Context, in *interceptor.ExecuteWorkflowInput) (any, error) {
	i.spy.mu.Lock()
	i.spy.root = ctx
	i.spy.mu.Unlock()
	return i.Next.ExecuteWorkflow(ctx, in)
}

type spyOut struct {
	interceptor.WorkflowOutboundInterceptorBase
	spy *optsSpy
}

func (o *spyOut) ExecuteActivity(ctx workflow.Context, activityType string, args ...any) workflow.Future {
	opts := workflow.GetActivityOptions(ctx)
	o.spy.mu.Lock()
	o.spy.ids = append(o.spy.ids, opts.ActivityID)
	if o.spy.sts == nil {
		o.spy.sts = map[string]time.Duration{}
	}
	o.spy.sts[opts.ActivityID] = opts.ScheduleToStartTimeout
	o.spy.kid(opts.ActivityID, ctx)
	o.spy.mu.Unlock()
	return o.Next.ExecuteActivity(ctx, activityType, args...)
}

func (o *spyOut) NewTimer(ctx workflow.Context, d time.Duration) workflow.Future {
	o.spy.mu.Lock()
	o.spy.kid("timer "+d.String(), ctx)
	o.spy.mu.Unlock()
	return o.Next.NewTimer(ctx, d)
}

func (o *spyOut) Sleep(ctx workflow.Context, d time.Duration) error {
	o.spy.mu.Lock()
	o.spy.kid("sleep "+d.String(), ctx)
	o.spy.mu.Unlock()
	return o.Next.Sleep(ctx, d)
}

// kid records the children at one call, when countKids is set. s.mu is held.
func (s *optsSpy) kid(what string, ctx workflow.Context) {
	if s.countKids {
		s.kids = append(s.kids, what+": "+s.children(ctx))
	}
}

// children says how many cancelable children the workflow's context has, and whose the call's context is: ctx (the
// workflow's own), child (one of its children) or other (dctx, which is none).
func (s *optsSpy) children(ctx workflow.Context) string {
	root, ok := nearestCancel(s.root)
	if !ok {
		return "unknown"
	}
	kids, ok := cancelChildren(root)
	if !ok {
		return "unknown: SDK layout changed: re-read internal/context.go cancelCtx.children"
	}
	own := "other"
	if c, ok := nearestCancel(ctx); ok && c.Pointer() == root.Pointer() {
		own = "ctx"
	} else if ok {
		for _, k := range kids {
			if k == c.Pointer() {
				own = "child"
			}
		}
	}
	return fmt.Sprintf("%d, %s", len(kids), own)
}

// nearestCancel finds the SDK's cancel context nearest ctx, through its value contexts (SDK v1.49.0
// parentCancelCtx). The test reads the SDK's unexported types by reflection, as nothing
// else shows them.
func nearestCancel(ctx workflow.Context) (reflect.Value, bool) {
	v := reflect.ValueOf(ctx)
	for v.IsValid() {
		if v.Kind() == reflect.Interface {
			v = v.Elem()
			continue
		}
		if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		switch e := v.Elem(); e.Type().Name() {
		case "cancelCtx":
			return v, true
		case "valueCtx":
			v = e.FieldByName("Context")
		default:
			return reflect.Value{}, false
		}
	}
	return reflect.Value{}, false
}

// cancelChildren are a cancelCtx's children, the keys of its children.nodes map (SDK v1.49.0
// childList and cancelCtx's children), each by its pointer. ok is false when the SDK keeps them another way: v1.41.1
// had children map[canceler]bool, on which FieldByName("nodes") panics; TestCancelChildrenOnAnotherLayout runs
// that layout.
func cancelChildren(c reflect.Value) (ps []uintptr, ok bool) {
	if c.Kind() != reflect.Pointer || c.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	ch := c.Elem().FieldByName("children")
	if !ch.IsValid() || ch.Kind() != reflect.Struct {
		return nil, false
	}
	nodes := ch.FieldByName("nodes")
	if !nodes.IsValid() || nodes.Kind() != reflect.Map {
		return nil, false
	}
	for _, k := range nodes.MapKeys() {
		for k.Kind() == reflect.Interface {
			k = k.Elem()
		}
		if k.Kind() != reflect.Pointer {
			return nil, false
		}
		ps = append(ps, k.Pointer())
	}
	return ps, true
}

func (f *gateFakes) record(ctx context.Context, await bool) string {
	i := activity.GetInfo(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if await {
		f.awaits = append(f.awaits, i.ActivityID)
	} else {
		f.order = append(f.order, i.ActivityID)
	}
	f.times[i.ActivityID] = fmt.Sprintf("%s %s %s", i.StartToCloseTimeout, i.ScheduleToCloseTimeout, i.HeartbeatTimeout)
	f.attempts[i.ActivityID] = append(f.attempts[i.ActivityID], i.Attempt)
	return i.ActivityID
}

// hold beats until ctx ends, 3 s at most: the test env cancels an activity's context on the answer to a heartbeat it
// sends, and sends at most one each 8 s at a 10 s HeartbeatTimeout (SDK v1.49.0
// activityTaskHandlerImpl.getHeartbeatThrottleInterval).
func hold(ctx context.Context, name string) error {
	until := time.Now().Add(3 * time.Second)
	for ctx.Err() == nil && time.Now().Before(until) {
		activity.RecordHeartbeat(ctx, StepHeartbeat{Name: name, Pid: 1})
		time.Sleep(20 * time.Millisecond)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return temporal.NewCanceledError()
}

func (f *gateFakes) step(ctx context.Context, in StepInput) (StepAnswer, error) {
	id := f.record(ctx, false)
	f.mu.Lock()
	f.inputs[id] = in
	f.mu.Unlock()
	if id == f.cancelAt {
		f.env.RegisterDelayedCallback(f.env.CancelWorkflow, 0)
		_ = hold(ctx, id)
		return StepAnswer{}, temporal.NewCanceledError()
	}
	if id == "wave" && f.holdWave {
		return StepAnswer{}, hold(ctx, id)
	}
	if id == "wave" {
		// As on a host, the wave answers after its items ended: once both awaits answered, 2 s at most, so the env
		// takes their results first (its callbacks run in order).
		for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(5 * time.Millisecond) {
			f.mu.Lock()
			n := f.awaitsDone
			f.mu.Unlock()
			if n >= 2 {
				break
			}
		}
		time.Sleep(f.waveDelay + 20*time.Millisecond)
	}
	if err := f.timeoutOnce[id]; err != nil && activity.GetInfo(ctx).Attempt == 1 {
		activity.RecordHeartbeat(ctx, StepHeartbeat{Name: in.Name, Pid: 4242})
		return StepAnswer{}, err
	}
	if err := f.errs[id]; err != nil {
		return StepAnswer{}, err
	}
	raw, ok := "", false
	if f.answer != nil {
		raw, ok = f.answer(in)
	}
	if !ok {
		raw, ok = f.answers[id]
	}
	if !ok {
		raw, ok = gateAnswers[in.Name]
	}
	if !ok && in.Verb == "quiet" {
		raw, ok = `{"result":"next","quiet":true,"why":"","wall_ms":1792152212345}`, true
	}
	if !ok {
		raw = `{"result":"next"}`
	}
	ans, err := answerFrom(in.Name, json.RawMessage(raw))
	ans.Key = in.Key
	if err == nil && (in.Verb == "combine" || in.Verb == "stopped") {
		if w := verdictOf(dataOf(ans)); w != "" {
			ans.Verdict, ans.SHA256 = fakeVerdict(w)
		}
		if f.verdictSize > 0 {
			ans.Verdict = []byte(strings.Repeat("x", f.verdictSize))
			sum := sha256.Sum256(ans.Verdict)
			ans.SHA256 = hex.EncodeToString(sum[:])
		}
	}
	if id == f.cancelDuring {
		// The cancel reaches the workflow through two callbacks of the env's main loop (SDK v1.49.0
		// registerDelayedCallback and cancelWorkflowByID of the test env); 200 ms lets both run before this answer.
		f.env.RegisterDelayedCallback(f.env.CancelWorkflow, 0)
		time.Sleep(200 * time.Millisecond)
	}
	return ans, err
}

func (f *gateFakes) stop(ctx context.Context, in StepInput) (StopResult, error) {
	f.record(ctx, false)
	if f.stopCancel {
		// As cancelDuring: the cancel reaches the workflow before this answer.
		f.env.RegisterDelayedCallback(f.env.CancelWorkflow, 0)
		time.Sleep(200 * time.Millisecond)
	}
	x := 143
	if f.stopExit != nil {
		x = *f.stopExit
	}
	sr := StopResult{Name: in.Name, Sent: "TERM", Done: true, Exit: &x}
	if x == 0 {
		sr.Answer = json.RawMessage(gateAnswers[in.Name])
	}
	return sr, nil
}

func (f *gateFakes) await(ctx context.Context, in AwaitInput) (ItemEnd, error) {
	id := f.record(ctx, true)
	if f.holdAwaits {
		return ItemEnd{}, hold(ctx, id)
	}
	defer func() {
		f.mu.Lock()
		f.awaitsDone++
		f.mu.Unlock()
	}()
	x := 0
	return ItemEnd{Key: in.Key, Found: true, Exit: &x}, nil
}

func gateInput() GateRunInput {
	return GateRunInput{GateInput: GateInput{ServerTag: "1.31.0", Results: "gate/results", Fake: true}, GID: "g20261016-120000",
		Dir: "gate/results/g20261016-120000"}
}

// runGate runs Gate once on the fakes.
func runGate(t *testing.T, f *gateFakes) (GateResult, GateProgress) {
	t.Helper()
	return runGateIn(t, f, gateInput())
}

func runGateIn(t *testing.T, f *gateFakes, in GateRunInput) (GateResult, GateProgress) {
	t.Helper()
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	f.env, f.inputs, f.times, f.attempts = env, map[string]StepInput{}, map[string]string{}, map[string][]int32{}
	f.spy = &optsSpy{countKids: f.countKids}
	env.SetWorkerOptions(worker.Options{Interceptors: append([]interceptor.WorkerInterceptor{f.spy}, f.interceptors...)})
	if f.header != nil {
		env.SetHeader(f.header)
	}
	if f.id != "" {
		env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: f.id})
	}
	start := f.start
	if start.IsZero() {
		start = t0
	}
	env.SetStartTime(start)
	env.SetOnTimerScheduledListener(func(_ string, d time.Duration) {
		f.mu.Lock()
		f.timers = append(f.timers, d)
		f.mu.Unlock()
	})
	env.SetOnTimerCanceledListener(func(string) {
		f.mu.Lock()
		f.cancelled = append(f.cancelled, "timer")
		f.mu.Unlock()
	})
	env.SetOnActivityCanceledListener(func(i *activity.Info) {
		f.mu.Lock()
		f.cancelled = append(f.cancelled, i.ActivityID)
		f.mu.Unlock()
	})
	if f.cancelAfter > 0 {
		env.RegisterDelayedCallback(env.CancelWorkflow, f.cancelAfter)
	}
	env.RegisterWorkflow(Gate)
	env.RegisterActivityWithOptions(f.step, activity.RegisterOptions{Name: "Step"})
	env.RegisterActivityWithOptions(f.stop, activity.RegisterOptions{Name: "Stop"})
	env.RegisterActivityWithOptions(f.await, activity.RegisterOptions{Name: "AwaitItem"})
	env.ExecuteWorkflow(Gate, in)
	if !env.IsWorkflowCompleted() {
		t.Fatal("Gate did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Gate failed instead of completing: %v", err)
	}
	var res GateResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}
	var p GateProgress
	v, err := env.QueryWorkflow("progress")
	if err != nil {
		t.Fatalf("query progress after the end: %v", err)
	}
	if err := v.Get(&p); err != nil {
		t.Fatal(err)
	}
	return res, p
}

const head = "open preflight omes laugd harness"

func steps(names string) string { return strings.Join(strings.Fields(names), " ") }

func wantOrder(t *testing.T, f *gateFakes, want string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.order, " "); got != steps(want) {
		t.Fatalf("order\n got %s\nwant %s", got, steps(want))
	}
}

// wantTimes checks each id's StartToClose, its ScheduleToClose 15 m more, and a 10 s heartbeat.
func wantTimes(t *testing.T, f *gateFakes, want map[string]time.Duration) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, stc := range want {
		if w := fmt.Sprintf("%s %s %s", stc, stc+15*time.Minute, 10*time.Second); f.times[id] != w {
			t.Errorf("%s: timeouts %q, want %q", id, f.times[id], w)
		}
	}
}

func wantTimers(t *testing.T, f *gateFakes, want ...time.Duration) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if fmt.Sprint(f.timers) != fmt.Sprint(want) {
		t.Fatalf("timers %v, want %v", f.timers, want)
	}
}

func TestGateRunsEveryStepInOrder(t *testing.T) {
	f := &gateFakes{}
	res, p := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release")
	if got := strings.Join(f.awaits, " "); got != "await-steady await-pl" && got != "await-pl await-steady" {
		t.Fatalf("awaits %q", got)
	}
	verdict, sum := fakeVerdict("ADVANCE")
	if res.End != "done" || res.Exit != 0 || res.VerdictWord != "ADVANCE" || string(res.Verdict) != string(verdict) || res.SHA256 != sum ||
		res.GateID != "g20261016-120000" || res.Dir != "gate/results/g20261016-120000" || res.Stop != nil || len(res.Steps) != 15 || res.Why != "" {
		t.Fatalf("result %+v", res)
	}
	if len(res.Items) != 2 || !res.Items["steady"].Found || *res.Items["pl"].Exit != 0 {
		t.Fatalf("items %+v", res.Items)
	}
	in := f.inputs["lease-pl"]
	if in.Verb != "lease" || in.Key != "pl" || in.GID != "g20261016-120000" || in.Dir != "gate/results/g20261016-120000" || in.ServerTag != "1.31.0" || !in.Fake {
		t.Fatalf("lease-pl's input %+v", in)
	}
	if p := f.inputs["post"]; p.Key != "" {
		t.Fatalf("post after a judge got key %q", p.Key)
	}
	// Section 1.3's timeouts, as each activity saw them. The barrier's, the wave's and the awaits' come from
	// preflight's answer: harness_timeout_s 300, deadline_s 780, kill_grace_s 30.
	wantTimes(t, f, map[string]time.Duration{"open": 2 * time.Minute, "preflight": 2 * time.Minute, "omes": 6 * time.Minute,
		"laugd": time.Minute, "harness": time.Minute, "lease-steady": 6 * time.Minute, "lease-pl": 6 * time.Minute,
		"barrier": 360 * time.Second, "quiet-0": time.Minute, "quiet-1": time.Minute, "wave-time": time.Minute,
		"wave": 930 * time.Second, "await-steady": 930 * time.Second, "await-pl": 930 * time.Second, "judge": 2 * time.Minute,
		"post": time.Minute, "release": 7 * time.Minute})
	f.spy.mu.Lock()
	ids := strings.Join(f.spy.ids, " ")
	sts := f.spy.sts
	f.spy.mu.Unlock()
	if want := steps(head + " lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave await-steady await-pl judge post release"); ids != want {
		t.Fatalf("activity ids\n got %s\nwant %s", ids, want)
	}
	for id, d := range sts {
		if w := map[bool]time.Duration{true: time.Minute}[id == "open"]; d != w {
			t.Errorf("%s: ScheduleToStartTimeout %s, want %s (open's 1m, every other activity's none)", id, d, w)
		}
	}
	// quiet_s 10, then the backstop: t0 + 930 s, less the 10 s the mock clock skipped in the Sleep.
	wantTimers(t, f, 10*time.Second, 920*time.Second)
	if p.Phase != "ended" || p.End != "done" || p.Step != "" || p.QuietPasses != 2 || len(p.Items) != 2 || p.CancelRequested ||
		p.Text != "gate g20261016-120000: ended done, exit 0" {
		t.Fatalf("progress after the end %+v", p)
	}
	// Done holds every step in order. An await is there when its answer reached the workflow before the wave's: the
	// fakes answer together, and an await answer that comes after cancelWave is dropped (TestGateItemsEndFirst has
	// the awaits first).
	var done []string
	for _, id := range p.Done {
		if !strings.HasPrefix(id, "await-") {
			done = append(done, id)
		}
	}
	if d := strings.Join(done, " "); d != steps(head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release") {
		t.Fatalf("progress's done %q", strings.Join(p.Done, " "))
	}
	// Began holds every step as it began, Sleeps left out, with the mock clock's time: what the starter prints.
	var began []string
	for i, m := range p.Began {
		began = append(began, m.ID)
		if m.At.Before(t0) || (i > 0 && m.At.Before(p.Began[i-1].At)) {
			t.Fatalf("progress's began %+v: times out of order", p.Began)
		}
	}
	if b := strings.Join(began, " "); b != steps(head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release") {
		t.Fatalf("progress's began %q", b)
	}
}

// One WithCancel child of the workflow's context, and only while the wave runs: every activity before it is the
// workflow context's own with no child; the wave, its awaits and the backstop timer share one child (waveCtx), which
// has none of its own; cancelWave takes it off; judge, post and release are dctx's, which is no child. A second child
// would be canceled in map order on a workflow cancel (SDK cancelCtx.cancel), and replay could
// differ. The count reads the SDK's unexported cancelCtx (go.mod: bump the SDK, re-read internal/context.go).
func TestGateKeepsOneCancelChild(t *testing.T) {
	for name, f := range map[string]*gateFakes{"ADVANCE": {countKids: true}, "a cancel in the wave": {cancelAt: "wave", holdAwaits: true, countKids: true}} {
		runGate(t, f)
		f.spy.mu.Lock()
		kids := strings.Join(f.spy.kids, "; ")
		f.spy.mu.Unlock()
		want := "open: 0, ctx; preflight: 0, ctx; omes: 0, ctx; laugd: 0, ctx; harness: 0, ctx; lease-steady: 0, ctx; lease-pl: 0, ctx; " +
			"barrier: 0, ctx; quiet-0: 0, ctx; sleep 10s: 0, ctx; quiet-1: 0, ctx; wave-time: 0, ctx; " +
			"wave: 1, child; await-steady: 1, child; await-pl: 1, child; timer 15m20s: 1, child; "
		if name == "ADVANCE" {
			want += "judge: 0, other; post: 0, other; release: 0, other"
		} else {
			// The cancel canceled ctx, which drops its children as it cancels them (cancelCtx.cancel).
			want += "stop-wave: 0, other; stopped: 0, other; post-stopped: 0, other; release-stopped: 0, other"
		}
		if kids != want {
			t.Fatalf("%s: children of the workflow's context at each activity and timer\n got %s\nwant %s", name, kids, want)
		}
	}
	// The control: the same spy on a workflow with two WithCancel children counts both.
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	spy := &optsSpy{countKids: true}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{spy}})
	two := func(ctx workflow.Context) error {
		c1, cancel1 := workflow.WithCancel(ctx)
		c2, cancel2 := workflow.WithCancel(ctx)
		defer cancel1()
		defer cancel2()
		_ = c1
		return workflow.NewTimer(c2, time.Second).Get(c2, nil)
	}
	env.RegisterWorkflowWithOptions(two, workflow.RegisterOptions{Name: "Two"})
	env.ExecuteWorkflow("Two")
	if got := strings.Join(spy.kids, "; "); got != "timer 1s: 2, child" {
		t.Fatalf("two WithCancel children: the spy saw %q", got)
	}
}

// cancelChildren on another SDK's layout says so, and never panics: v1.41.1 kept children map[canceler]bool.
func TestCancelChildrenOnAnotherLayout(t *testing.T) {
	type old struct{ children map[*int]bool }
	type now struct{ children struct{ nodes map[*int]*int } }
	k := new(int)
	if ps, ok := cancelChildren(reflect.ValueOf(&old{children: map[*int]bool{k: true}})); ok || ps != nil {
		t.Fatalf("v1.41.1's layout: %v, %v", ps, ok)
	}
	n := &now{}
	n.children.nodes = map[*int]*int{k: nil}
	if ps, ok := cancelChildren(reflect.ValueOf(n)); !ok || len(ps) != 1 {
		t.Fatalf("v1.49.0's layout: %v, %v", ps, ok)
	}
}

func TestGateGoesToTheJudgeOnARefusal(t *testing.T) {
	f := &gateFakes{answers: map[string]string{
		"preflight": `{"result":"refused","refused":"another gate run holds gate/.lock: wait for it to end"}`,
		"judge":     `{"result":"done","verdict":"BLOCK","exit":1,"trap":false}`,
	}}
	res, _ := runGate(t, f)
	wantOrder(t, f, "open preflight judge post release")
	if res.End != "done" || res.Exit != 1 || res.VerdictWord != "BLOCK" || len(f.awaits) != 0 {
		t.Fatalf("result %+v", res)
	}
}

func TestGateStopsLeasing(t *testing.T) {
	f := &gateFakes{answers: map[string]string{
		"lease-steady": `{"result":"stop-leasing","harness_exit":1}`,
		"barrier":      `{"result":"judge","refused":null}`,
		"judge":        `{"result":"done","verdict":"BLOCK","exit":1,"trap":false}`,
	}}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady barrier judge post release")
	if res.End != "done" || res.Exit != 1 || res.VerdictWord != "BLOCK" {
		t.Fatalf("result %+v", res)
	}
}

func TestGateWaitsForQuiet(t *testing.T) {
	f := &gateFakes{answers: map[string]string{
		"quiet-0": `{"result":"next","quiet":false,"why":"laugd shows 1 busy cell(s)"}`,
		"quiet-1": `{"result":"next","quiet":false,"why":"compose projects up: laug-x"}`,
	}}
	res, p := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 quiet-2 quiet-3 wave-time wave judge post release")
	if res.End != "done" || res.Exit != 0 || p.QuietPasses != 4 {
		t.Fatalf("result %+v, quiet passes %d", res, p.QuietPasses)
	}
	wantTimers(t, f, time.Second, time.Second, 10*time.Second, 918*time.Second)
}

// A pass whose first look is quiet and whose second is not sleeps 1 s and goes round, as gate/run.sh's step 6 does.
func TestGateQuietThenNot(t *testing.T) {
	notQuiet := `{"result":"next","quiet":false,"why":"compose projects up: laug-x"}`
	f := &gateFakes{answers: map[string]string{"quiet-0": notQuiet, "quiet-2": notQuiet}}
	res, p := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 quiet-2 quiet-3 quiet-4 wave-time wave judge post release")
	if res.End != "done" || res.Exit != 0 || p.QuietPasses != 5 {
		t.Fatalf("result %+v, quiet passes %d", res, p.QuietPasses)
	}
	// 1 s after quiet-0, quiet_s between quiet-1 and quiet-2, 1 s after quiet-2, quiet_s between quiet-3 and quiet-4,
	// then the backstop: t0 + 930 s, less the 22 s the mock clock skipped.
	wantTimers(t, f, time.Second, 10*time.Second, time.Second, 10*time.Second, 908*time.Second)
}

// quietUntilRefused answers each quiet-<i> as step.sh does on a host that is never quiet: wall_ms 1 s later each pass,
// and refused once wall_ms reaches the barrier's limit_ms.
func quietUntilRefused(refuse bool) func(StepInput) (string, bool) {
	return func(in StepInput) (string, bool) {
		if in.Verb != "quiet" {
			return "", false
		}
		i, _ := strconv.Atoi(in.Key)
		limit := int64(1792152231456)
		wall := limit - int64(60-i)*1000
		if refuse && wall >= limit {
			return `{"result":"refused"}`, true
		}
		return fmt.Sprintf(`{"result":"next","quiet":false,"why":"compose projects up: laug-0000aaaa","wall_ms":%d}`, wall), true
	}
}

func TestGateNeverQuietRefuses(t *testing.T) {
	f := &gateFakes{answer: quietUntilRefused(true), answers: map[string]string{"judge": `{"result":"done","verdict":"BLOCK","exit":1,"trap":false}`}}
	res, p := runGate(t, f)
	var q []string
	for i := 0; i <= 60; i++ {
		q = append(q, "quiet-"+strconv.Itoa(i))
	}
	wantOrder(t, f, head+" lease-steady lease-pl barrier "+strings.Join(q, " ")+" judge post release")
	if res.End != "done" || res.Exit != 1 || res.Stop != nil || p.QuietPasses != 61 {
		t.Fatalf("result %+v, quiet passes %d", res, p.QuietPasses)
	}
}

func TestGateCeiling(t *testing.T) {
	f := &gateFakes{answer: quietUntilRefused(false)}
	res, p := runGate(t, f)
	var q []string
	for i := 0; i < 64; i++ {
		q = append(q, "quiet-"+strconv.Itoa(i))
	}
	wantOrder(t, f, head+" lease-steady lease-pl barrier "+strings.Join(q, " ")+" stopped post-stopped release-stopped")
	why := "the barrier ran 64 passes; barrier_timeout_s is 60 s and no quiet-<i> refused"
	if st := f.inputs["stopped"]; st.Key != why || res.End != "stopped" || res.Stop != nil || res.Why != why || p.QuietPasses != 64 {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}
}

func TestGateStopPath(t *testing.T) {
	lost := temporal.NewNonRetryableApplicationError("step lease-pl: a .pid with no .done", "StepLost", nil)
	f := &gateFakes{errs: map[string]error{"lease-pl": lost}}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl stop-lease-pl stopped post-stopped release-stopped")
	wantTimes(t, f, map[string]time.Duration{"stop-lease-pl": 100 * time.Second, "stopped": 90 * time.Second,
		"post-stopped": time.Minute, "release-stopped": 7 * time.Minute})
	if st := f.inputs["stopped"]; st.Key != "step lease-pl: StepLost" || st.Verb != "stopped" {
		t.Fatalf("stopped got key %q", st.Key)
	}
	if res.End != "stopped" || res.Exit != 1 || res.VerdictWord != "BLOCK" || res.Stop == nil || res.Stop.Name != "lease-pl" || res.Why != "step lease-pl: StepLost" {
		t.Fatalf("result %+v", res)
	}

	f = &gateFakes{cancelAt: "wave", holdAwaits: true}
	res, p := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave stop-wave stopped post-stopped release-stopped")
	if st := f.inputs["stopped"]; st.Key != "the workflow was cancelled" {
		t.Fatalf("stopped got key %q", st.Key)
	}
	if res.End != "stopped" || res.Exit != 1 || res.Stop == nil || res.Stop.Name != "wave" || res.Stop.Sent != "TERM" || !p.CancelRequested {
		t.Fatalf("result %+v, progress %+v", res, p)
	}
	f.mu.Lock()
	cancelled := fmt.Sprint(f.cancelled)
	f.mu.Unlock()
	for _, c := range []string{"wave", "await-steady", "await-pl", "timer"} {
		if !strings.Contains(cancelled, c) {
			t.Errorf("cancelled %s, no %s", cancelled, c)
		}
	}
	// Items no await answered come from stopped's answer.
	if e := res.Items["pl"]; !e.Found || e.StoppedBy == nil || *e.StoppedBy != "TERM" || *e.Exit != 143 {
		t.Fatalf("items %+v", res.Items)
	}
}

// The wave step itself fails: Gate cancels the awaits and the backstop timer (cancelWave) before the stop path, which
// stops the wave by its record.
func TestGateWaveLost(t *testing.T) {
	lost := temporal.NewNonRetryableApplicationError("step wave: a .pid with no .done", "StepLost", nil)
	f := &gateFakes{errs: map[string]error{"wave": lost}, holdAwaits: true}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave stop-wave stopped post-stopped release-stopped")
	if st := f.inputs["stopped"]; st.Key != "step wave: StepLost" || res.End != "stopped" || res.Stop == nil || res.Stop.Name != "wave" {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}
	f.mu.Lock()
	cancelled := fmt.Sprint(f.cancelled)
	f.mu.Unlock()
	for _, c := range []string{"await-steady", "await-pl", "timer"} {
		if !strings.Contains(cancelled, c) {
			t.Errorf("cancelled %s, no %s", cancelled, c)
		}
	}
}

// The backstop: the env starts 1000 s after t0, so t0 + 930 s has passed and the timer is 1 s. The wave beats on and
// never answers; the timer fires, and the stop path stops the wave by its record. A Stop that finds the wave's .done
// with exit 0 sends the gate to the judge instead (decision 11), and with no await answered, the items and the wave's
// answer come from that .done.
func TestGateBackstop(t *testing.T) {
	f := &gateFakes{holdWave: true, start: t0.Add(1000 * time.Second)}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave stop-wave stopped post-stopped release-stopped")
	wantTimers(t, f, 10*time.Second, time.Second)
	why := "step wave: no answer by t0 + deadline_s + kill_grace_s + 120 s (the backstop)"
	if st := f.inputs["stopped"]; st.Key != why || res.End != "stopped" || res.Stop == nil || res.Stop.Name != "wave" {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}

	zero := 0
	f = &gateFakes{holdWave: true, holdAwaits: true, start: t0.Add(1000 * time.Second), stopExit: &zero}
	res, p := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave stop-wave judge post release")
	if res.End != "done" || res.Exit != 0 || res.VerdictWord != "ADVANCE" || !strings.Contains(res.Why, "backstop") {
		t.Fatalf("after a Stop that found the wave's .done with exit 0: result %+v", res)
	}
	for _, k := range []string{"steady", "pl"} {
		if e, ok := res.Items[k]; !ok || !e.Found || e.Exit == nil || *e.Exit != 0 {
			t.Fatalf("no await answered: the items from the wave's .done, result's %+v", res.Items)
		}
		if _, ok := p.Items[k]; !ok {
			t.Fatalf("progress's items %+v", p.Items)
		}
	}
	var names []string
	for _, s := range res.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "wave-time wave judge post release") {
		t.Fatalf("the wave's answer is not among the steps: %s", got)
	}
}

// A cancel that arrives while Stop reads the wave's .done with exit 0 came after the wave answered and before the
// judge was scheduled: the stop path, not the judge, and Why names stop-wave.
func TestGateBackstopThenACancel(t *testing.T) {
	zero := 0
	f := &gateFakes{holdWave: true, holdAwaits: true, start: t0.Add(1000 * time.Second), stopExit: &zero, stopCancel: true}
	res, p := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave stop-wave stopped post-stopped release-stopped")
	why := "the backstop fired as the wave answered: its .done says exit 0; a cancel arrived during stop-wave; the workflow was cancelled"
	if st := f.inputs["stopped"]; st.Key != "the workflow was cancelled" || res.End != "stopped" || res.Exit != 1 || res.Why != why ||
		res.Stop == nil || res.Stop.Name != "wave" || !p.CancelRequested {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}
	var names []string
	for _, s := range res.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "wave-time wave stopped post release") {
		t.Fatalf("steps %s: want the wave's answer, from its .done, then the stop path", got)
	}
}

// The result carries verdict.json once: a verdict of maxVerdict bytes, from the judge or from stopped, makes a result
// payload under the server's 2 MiB limit (limit.blobSize.error), and no step in Steps keeps its bytes.
func TestGateResultCarriesTheVerdictOnce(t *testing.T) {
	for name, f := range map[string]*gateFakes{"judge": {verdictSize: maxVerdict}, "stopped": {verdictSize: maxVerdict, cancelAt: "wave", holdAwaits: true}} {
		res, _ := runGate(t, f)
		if len(res.Verdict) != maxVerdict || res.SHA256 == "" {
			t.Fatalf("%s: the result's verdict has %d bytes, want %d", name, len(res.Verdict), maxVerdict)
		}
		for _, s := range res.Steps {
			if s.Verdict != nil || s.SHA256 != "" {
				t.Fatalf("%s: step %s keeps %d verdict bytes in Steps", name, s.Name, len(s.Verdict))
			}
		}
		ps, err := converter.GetDefaultDataConverter().ToPayloads(res)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, p := range ps.GetPayloads() {
			n += proto.Size(p)
		}
		if n >= 2<<20 {
			t.Fatalf("%s: the result's payload is %d bytes, over 2 MiB", name, n)
		}
		t.Logf("%s: verdict.json %d bytes, the result's payload %d bytes", name, len(res.Verdict), n)
	}
}

func TestGateItemsEndFirst(t *testing.T) {
	f := &gateFakes{waveDelay: 200 * time.Millisecond}
	res, p := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release")
	for _, k := range []string{"steady", "pl"} {
		if e, ok := res.Items[k]; !ok || !e.Found || *e.Exit != 0 {
			t.Fatalf("result's items %+v", res.Items)
		}
		if e, ok := p.Items[k]; !ok || !e.Found || e.At.IsZero() {
			t.Fatalf("progress's items %+v", p.Items)
		}
	}
	d := strings.Join(p.Done, " ")
	if i, j := strings.Index(d, "await-"), strings.Index(d, " wave "); i < 0 || j < 0 || i > j {
		t.Fatalf("the awaits did not answer before the wave: done %q", d)
	}
}

// An attempt that ends without the step's own error is retried, and the retry's answer carries the gate on: no Stop,
// no stop path. The fake's first attempt of the wave beats once and ends with a heartbeat timeout, a stand-in for the
// attempt of a worker that is gone (cells/results/2026-10-16-gate6-step5.txt, case 1).
func TestGateGoesOnAfterAnAttemptTimesOut(t *testing.T) {
	f := &gateFakes{timeoutOnce: map[string]error{"wave": temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_HEARTBEAT, nil)}}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave wave judge post release")
	f.mu.Lock()
	tries := fmt.Sprint(f.attempts["wave"], f.attempts["judge"])
	f.mu.Unlock()
	if tries != "[1 2] [1]" {
		t.Fatalf("attempts: wave and judge %s, want [1 2] [1]", tries)
	}
	if res.End != "done" || res.Exit != 0 || res.VerdictWord != "ADVANCE" || res.Stop != nil || len(res.Steps) != 15 || res.Why != "" {
		t.Fatalf("result %+v", res)
	}
}

// A cancel inside quiet_s's Sleep: no step is live, so nothing is stopped, and the stop path runs from stopped.
func TestGateCancelledInASleep(t *testing.T) {
	f := &gateFakes{cancelAfter: 5 * time.Second}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 stopped post-stopped release-stopped")
	if st := f.inputs["stopped"]; st.Key != "the workflow was cancelled" || res.End != "stopped" || res.Exit != 1 || res.Stop != nil {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}
}

// No worker took open within its ScheduleToStartTimeout: the stop path's activities get one too, so a gate that no
// worker serves ends in about 4 m, not 71 m.
func TestGateWhenNoWorkerTookOpen(t *testing.T) {
	f := &gateFakes{errs: map[string]error{"open": temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START, nil)}}
	res, _ := runGate(t, f)
	wantOrder(t, f, "open stop-open stopped post-stopped release-stopped")
	if st := f.inputs["stopped"]; st.Key != "step open: timeout ScheduleToStart" || res.End != "stopped" {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}
	f.spy.mu.Lock()
	defer f.spy.mu.Unlock()
	for _, id := range []string{"open", "stop-open", "stopped", "post-stopped", "release-stopped"} {
		if f.spy.sts[id] != time.Minute {
			t.Errorf("%s after no worker took open: ScheduleToStartTimeout %s, want 1m", id, f.spy.sts[id])
		}
	}
}

// post after the judge: an exit that is not 0 is Exit 2 with the judge path's release; an error is End failed, Exit 2
// and the stop path's release, with no stopped.
func TestGatePostFails(t *testing.T) {
	f := &gateFakes{answers: map[string]string{"post": `{"result":"done","exit":3}`}}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release")
	if res.End != "done" || res.Exit != 2 || res.VerdictWord != "ADVANCE" {
		t.Fatalf("post exit 3: result %+v", res)
	}
	f = &gateFakes{errs: map[string]error{"post": temporal.NewNonRetryableApplicationError("step post: exit 1", "StepFailed", nil)}}
	res, _ = runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release-stopped")
	if res.End != "failed" || res.Exit != 2 || res.VerdictWord != "ADVANCE" || res.Why != "step post: exit 1" {
		t.Fatalf("post erred: result %+v", res)
	}
}

// The gate's exit never depends on its cleanup: a failed release changes Why and nothing else.
func TestGateReleaseErrs(t *testing.T) {
	f := &gateFakes{errs: map[string]error{"release": temporal.NewNonRetryableApplicationError("step release: exit 1", "StepFailed", nil)}}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release")
	if res.End != "done" || res.Exit != 0 || res.VerdictWord != "ADVANCE" || res.Why != "step release: exit 1" {
		t.Fatalf("release erred: result %+v", res)
	}
}

func TestGateAfterATrapJudge(t *testing.T) {
	f := &gateFakes{answers: map[string]string{"judge": `{"result":"done","verdict":"BLOCK","exit":1,"trap":true}`}}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post-stopped release-stopped")
	if b, _ := fakeVerdict("BLOCK"); res.End != "stopped" || res.Exit != 1 || res.VerdictWord != "BLOCK" || string(res.Verdict) != string(b) || res.Stop != nil {
		t.Fatalf("result %+v", res)
	}

	f = &gateFakes{errs: map[string]error{"judge": temporal.NewNonRetryableApplicationError("step judge: exit 1", "StepFailed", nil)}}
	res, _ = runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge stopped post-stopped release-stopped")
	if st := f.inputs["stopped"]; st.Key != "step judge: StepFailed" {
		t.Fatalf("stopped after a failed judge got key %q", st.Key)
	}
	if res.End != "stopped" || res.Exit != 1 || res.Stop != nil {
		t.Fatalf("result %+v", res)
	}
}

func TestGateStopsOnAnAnswerItDoesNotTake(t *testing.T) {
	f := &gateFakes{answers: map[string]string{"wave": `{"result":"stopped"}`}}
	res, _ := runGate(t, f)
	wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave stopped post-stopped release-stopped")
	if st := f.inputs["stopped"]; st.Key != "step wave: result stopped" || res.Stop != nil || res.End != "stopped" {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}
}

// A cancel on the judge path changes nothing but Why: the order, the end, the exit and the verdict stay.
func TestGateCancelOnTheJudgePath(t *testing.T) {
	verdict, _ := fakeVerdict("ADVANCE")
	for _, id := range []string{"post", "release"} {
		f := &gateFakes{cancelDuring: id}
		res, p := runGate(t, f)
		wantOrder(t, f, head+" lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave judge post release")
		if res.End != "done" || res.Exit != 0 || string(res.Verdict) != string(verdict) ||
			res.Why != "a cancel arrived during "+id+"; the judge path ran on" || !p.CancelRequested {
			t.Fatalf("a cancel during %s: result %+v, progress's cancel_requested %v", id, res, p.CancelRequested)
		}
	}
}

func TestGateRefusesABadGateID(t *testing.T) {
	f := &gateFakes{}
	in := gateInput()
	in.GID = "../g1"
	res, p := runGateIn(t, f, in)
	if len(f.order) != 0 || res.End != "failed" || res.Exit != 2 || res.Why != `input: gate id "../g1" is not g<date>-<time>` || p.Phase != "ended" {
		t.Fatalf("order %v; result %+v", f.order, res)
	}
}

// open's answer without t0_ms: no backstop is ever counted from the epoch; the stop path runs from stopped.
func TestGateOpenWithoutT0(t *testing.T) {
	f := &gateFakes{answers: map[string]string{"open": `{"result":"next"}`}}
	res, _ := runGate(t, f)
	wantOrder(t, f, "open stopped post-stopped release-stopped")
	wantTimers(t, f)
	if st := f.inputs["stopped"]; st.Key != "step open: answer without t0_ms" || res.Stop != nil || res.End != "stopped" {
		t.Fatalf("stopped's key %q; result %+v", st.Key, res)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\nb\r\nc", 300); got != "a b c" {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("é", 200) // 400 bytes
	if got := oneLine(long, 300); len(got) != 300 || !strings.HasSuffix(got, "é") {
		t.Fatalf("%d bytes, %q", len(got), got[len(got)-4:])
	}
}
