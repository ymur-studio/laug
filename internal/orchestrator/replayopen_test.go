package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// The worker's startup replay without a fixture: the refusal's text, a gate whose first workflow task never started,
// and the visibility query. No name here starts TestReplay, so go test -skip TestReplay runs them.

func TestRefusalNamesTheGate(t *testing.T) {
	r := ReplayRefusal{ID: "gate-g20261016-120000", RunID: "0199-run",
		Err: errors.New("[TMPRL1100] nondeterministic workflow: history event is ActivityTaskScheduled: (ActivityId:lease-pl), replay command is ScheduleActivityTask: (ActivityId:barrier)\nmore")}
	got := r.Error()
	if !strings.Contains(got, "gate-g20261016-120000") || !strings.Contains(got, "[TMPRL1100]") || strings.Contains(got, "more") ||
		!strings.HasPrefix(got, "open gate gate-g20261016-120000 (run 0199-run) does not replay on this build: ") {
		t.Fatalf("refusal %q", got)
	}
	if !isNondeterminism(r) || !isNondeterminism(r.Err) || isNondeterminism(errors.New("connection refused")) || isNondeterminism(nil) {
		t.Fatal("isNondeterminism")
	}
}

// A gate whose worker died before its first poll: WorkflowExecutionStarted and WorkflowTaskScheduled. The replayer
// refuses any history under 3 events, with no [TMPRL1100]; the worker skips it by its length.
func TestShortHistoryIsNoRefusal(t *testing.T) {
	h := &historypb.History{Events: []*historypb.HistoryEvent{
		{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				WorkflowType: &commonpb.WorkflowType{Name: "Gate"}, TaskQueue: &taskqueuepb.TaskQueue{Name: "laug-gate-00000000"}}}},
		{EventId: 2, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{
				TaskQueue: &taskqueuepb.TaskQueue{Name: "laug-gate-00000000"}}}},
	}}
	r, err := NewGateReplayer()
	if err != nil {
		t.Fatal(err)
	}
	err = r.ReplayWorkflowHistory(nil, h)
	if err == nil || !strings.Contains(err.Error(), "at least 3 events expected in the history") || isNondeterminism(err) {
		t.Fatalf("a 2-event history: %v", err)
	}
	if startedEnough(2) || !startedEnough(3) {
		t.Fatal("startedEnough")
	}
}

func TestOpenGateQuery(t *testing.T) {
	if got := OpenGateQuery("laug-gate-1a2b3c4d"); got != "TaskQueue = 'laug-gate-1a2b3c4d' AND ExecutionStatus = 'Running' AND WorkflowType = 'Gate'" {
		t.Fatalf("%q", got)
	}
}

// historyClient answers GetWorkflowHistory from events; every other method is the nil interface's.
type historyClient struct {
	client.Client
	events []*historypb.HistoryEvent
}

type eventIter struct{ events []*historypb.HistoryEvent }

func (i *eventIter) HasNext() bool { return len(i.events) > 0 }
func (i *eventIter) Next() (*historypb.HistoryEvent, error) {
	e := i.events[0]
	i.events = i.events[1:]
	return e, nil
}

func (c historyClient) GetWorkflowHistory(context.Context, string, string, bool, enumspb.HistoryEventFilterType) client.HistoryEventIterator {
	return &eventIter{events: c.events}
}

// The refusal's release command takes the gate's GATE_RESULTS and FAKE_GATE_DIR from its first event.
func TestStartedGateInput(t *testing.T) {
	in := GateRunInput{GateInput: GateInput{ServerTag: "1.31.0", Results: "orchestrator/run/record/advance", Fake: true,
		Extra: map[string]string{"FAKE_GATE_DIR": "/tmp/fake"}}, GID: "g20261016-120000", Dir: "orchestrator/run/record/advance/g20261016-120000"}
	ps, err := converter.GetDefaultDataConverter().ToPayloads(in)
	if err != nil {
		t.Fatal(err)
	}
	c := historyClient{events: []*historypb.HistoryEvent{{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
			WorkflowType: &commonpb.WorkflowType{Name: "Gate"}, Input: ps}}}}}
	got, err := StartedGateInput(context.Background(), c, "gate-g20261016-120000", "")
	if err != nil || got.Results != in.Results || got.GID != in.GID || got.Extra["FAKE_GATE_DIR"] != "/tmp/fake" {
		t.Fatalf("%+v, %v", got, err)
	}
	if v := ReleaseLine("laug", got.GID, got, ""); v != "FAKE_GATE_DIR=/tmp/fake laug gate stop g20261016-120000 -results orchestrator/run/record/advance" {
		t.Fatalf("ReleaseLine %q", v)
	}
	if _, err := StartedGateInput(context.Background(), historyClient{}, "gate-x", ""); err == nil {
		t.Fatal("no history: want an error")
	}
}
