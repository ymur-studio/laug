package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// The replay fixtures: six Gate histories, scrubbed (record.go). Five were recorded on the dev server from fake gates
// by laug orchestrator record; real is a real gate's, from the lab host (docs/CAPABILITIES.md,
// Replay). The replayer feeds each recorded history to this build's Gate and checks that every command Gate
// issues matches the event the server recorded at that point: command type, activity id and type, timer id
// (SDK v1.49.0 isCommandMatchEvent). It compares neither timer durations nor activity inputs
// nor the result. This file needs the fixtures: a missing one fails its test, which names the record command, and is
// never skipped, so a lost fixture cannot leave make test-go green.

var fixtures = []string{"advance", "refused", "harness", "cancelled", "adopted", "real"}

// fixture is testdata/histories/<name>.json; the test fails when it is not there.
func fixture(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("testdata", "histories", name+".json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("no %s: record it: laug orchestrator record <gate id> %s -overwrite-fixture", p, name)
	}
	return p
}

func TestReplayGateHistories(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, name)
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			h, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if n := len(h.GetEvents()); n == 0 || n >= 900 {
				t.Fatalf("%s holds %d events; the budget is under 900 (gate.go, Event budget)", p, n)
			}
			if typ := h.GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetWorkflowType().GetName(); typ != "Gate" {
				t.Fatalf("%s is a %q history, not Gate", p, typ)
			}
			r, err := NewGateReplayer()
			if err != nil {
				t.Fatal(err)
			}
			if err := r.ReplayWorkflowHistoryFromJSONFile(nil, p); err != nil {
				t.Fatalf("%s does not replay on this build (changed Gate? re-record it): %v", p, err)
			}
		})
	}
}

// A cancel in the wave cancels ctx's one child, waveCtx, whose Done channel then cancels the wave, both awaits and the
// timer in the order they were scheduled. A second WithCancel child of ctx would be canceled in map order (SDK v1.49.0
// cancelCtx.cancel; flag 9, SDKFlagOrderedChildCancel, is off in sdkFlagsAllowed), and replay would fail at
// random: 50 replays, each with a new replayer, all clean.
func TestReplayCancelledFiftyTimes(t *testing.T) {
	p := fixture(t, "cancelled")
	for i := 0; i < 50; i++ {
		r, err := NewGateReplayer()
		if err != nil {
			t.Fatal(err)
		}
		if err := r.ReplayWorkflowHistoryFromJSONFile(nil, p); err != nil {
			t.Fatalf("replay %d of %s: %v", i+1, p, err)
		}
	}
}

// Gate with the barrier before the last lease, by an interceptor, fails replay with [TMPRL1100]: the activity ids
// make the order part of the check. With the SDK's sequence-number ids it would replay clean (gate.go, top).
func TestReplayCatchesAnotherOrder(t *testing.T) {
	p := fixture(t, "advance")
	// The control: the same file replays clean without the interceptor, so the error below is the reorder's.
	plain, err := NewGateReplayer()
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.ReplayWorkflowHistoryFromJSONFile(nil, p); err != nil {
		t.Fatalf("%s does not replay even in order: %v", p, err)
	}
	r, err := NewGateReplayer(&reorder{last: "pl"}) // advance ran gate/plan.yaml, whose last item is pl
	if err != nil {
		t.Fatal(err)
	}
	// The error names the history's activity at that position, lease-pl (the SDK's words: "matching
	// Activity command was expected in history event position lease-pl").
	err = r.ReplayWorkflowHistoryFromJSONFile(nil, p)
	if err == nil || !strings.Contains(err.Error(), "[TMPRL1100]") || !isNondeterminism(err) || !strings.Contains(err.Error(), "lease-pl") {
		t.Fatalf("Gate with the barrier before lease-pl replayed %s with %v; want a [TMPRL1100] error at lease-pl", p, err)
	}
}

// reorder schedules the barrier step just before lease-<last>, under activity id barrier, waits for it, and hands
// that future to Gate when Gate asks for the barrier later: Gate with Barrier before the last Lease, and no copy of
// gate.go. Replayers take interceptors (SDK v1.49.0 WorkflowReplayerOptions.Interceptors).
type reorder struct {
	interceptor.WorkerInterceptorBase
	last string
}

func (r *reorder) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &reorderIn{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, last: r.last}
}

type reorderIn struct {
	interceptor.WorkflowInboundInterceptorBase
	last string
}

func (i *reorderIn) Init(out interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&reorderOut{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: out}, last: i.last})
}

type reorderOut struct {
	interceptor.WorkflowOutboundInterceptorBase
	last    string
	barrier workflow.Future
}

func (o *reorderOut) ExecuteActivity(ctx workflow.Context, activityType string, args ...any) workflow.Future {
	switch id := workflow.GetActivityOptions(ctx).ActivityID; {
	case id == "lease-"+o.last && o.barrier == nil:
		si := args[0].(StepInput)
		si.Verb, si.Key, si.Name = "barrier", "", "barrier"
		opts := workflow.GetActivityOptions(ctx)
		opts.ActivityID = "barrier"
		o.barrier = o.Next.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), activityType, si)
		_ = o.barrier.Get(ctx, nil)
	case id == "barrier" && o.barrier != nil:
		return o.barrier
	}
	return o.Next.ExecuteActivity(ctx, activityType, args...)
}

// The startup replay's check on a Gate that returns early. advance-cut.json, under testdata/histories, is
// advance.json's first 79 events, cut by hand, not recorded. It ends at event 79, TimerStarted, the last event of
// workflow task 75, which scheduled the wave (76), its two awaits (77, 78) and the backstop timer (79). That is an
// open gate mid-wave as the server holds it while the wave runs. A build whose Gate returns at once completes on the
// first workflow task and issues no ScheduleActivityTask for open (event 5). replayExecution, the worker's path, must
// refuse it with [TMPRL1100]: the replay goes on to event 5, which no command of the stub's matches. The
// error holds the command state machine's "lookup failed for scheduledEventID to activityID: scheduleEventID: 5,
// activityID: open". This build's Gate replays the cut clean: its commands match through event 79 and it then waits
// on the wave. The SDK's ReplayWorkflowExecution with the gate's real namespace replays the early return clean. That
// is the hole replayExecution closes (replay.go, replayExecution).
func TestReplayCatchesEarlyReturn(t *testing.T) {
	cut := loadHistory(t, filepath.Join("testdata", "histories", "advance-cut.json"))
	full := loadHistory(t, fixture(t, "advance"))
	if n := len(cut.GetEvents()); n != 79 || cut.GetEvents()[n-1].GetEventType() != enumspb.EVENT_TYPE_TIMER_STARTED {
		t.Fatalf("advance-cut.json holds %d events; want 79, the last TimerStarted", n)
	}
	for i, e := range cut.GetEvents() {
		if !proto.Equal(e, full.GetEvents()[i]) {
			t.Fatalf("advance-cut.json's event %d is not advance.json's", i+1)
		}
	}
	const id, run = "gate-g20261016-200032", "advance-cut-run"
	c := cutClient{historyClient{events: cut.GetEvents()}}
	ctx := context.Background()

	if err := replayExecution(ctx, c, earlyGateReplayer(t), id, run, nil); !isNondeterminism(err) || !strings.Contains(err.Error(), "activityID: open") {
		t.Fatalf("a Gate that returns at once replayed the cut history with %v; want [TMPRL1100]", err)
	}
	r, err := NewGateReplayer()
	if err != nil {
		t.Fatal(err)
	}
	if err := replayExecution(ctx, c, r, id, run, nil); err != nil {
		t.Fatalf("this build's Gate does not replay the cut history: %v", err)
	}
	if err := earlyGateReplayer(t).ReplayWorkflowExecution(ctx, c.WorkflowService(), nil, "default", workflow.Execution{ID: id, RunID: run}); err != nil {
		t.Fatalf("ReplayWorkflowExecution on namespace default refused the early return (%v): the SDK now checks it, re-read replayExecution's comment", err)
	}
}

func loadHistory(t *testing.T, p string) *historypb.History {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// earlyGateReplayer knows a workflow named Gate that returns at once.
func earlyGateReplayer(t *testing.T) worker.WorkflowReplayer {
	t.Helper()
	r, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterWorkflowWithOptions(func(workflow.Context, GateRunInput) (GateResult, error) { return GateResult{}, nil },
		workflow.RegisterOptions{Name: "Gate"})
	return r
}

// cutClient is historyClient with a WorkflowService that answers GetWorkflowExecutionHistory from the same events, in
// one page, for the SDK's ReplayWorkflowExecution.
type cutClient struct{ historyClient }

func (c cutClient) WorkflowService() workflowservice.WorkflowServiceClient {
	return historyService{events: c.events}
}

type historyService struct {
	workflowservice.WorkflowServiceClient
	events []*historypb.HistoryEvent
}

func (s historyService) GetWorkflowExecutionHistory(context.Context, *workflowservice.GetWorkflowExecutionHistoryRequest, ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	return &workflowservice.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: s.events}}, nil
}
