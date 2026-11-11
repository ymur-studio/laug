package orchestrator

import (
	"context"
	"fmt"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// The worker's startup replay: before it polls, the worker replays every open Gate on its own task queue against
// its own build. A build whose Gate issues another command than history recorded would fail that gate's next workflow
// task with a nondeterminism error and leave it stuck mid-wave; the replay finds that first, and the worker refuses
// to poll. One query lists the open gates, and start-dev's SQLite visibility takes it. On a dev server, the query for a
// test queue listed a Gate as soon as it started there, and an unknown attribute erred "invalid search attribute".

// OpenGateQuery is the visibility query for the open Gate workflows on queue.
func OpenGateQuery(queue string) string {
	return fmt.Sprintf("TaskQueue = '%s' AND ExecutionStatus = 'Running' AND WorkflowType = 'Gate'", queue)
}

// NewGateReplayer is a replayer that knows workflow Gate only, with the given interceptors (tests use one to change
// Gate's order without a copy of gate.go).
func NewGateReplayer(ics ...interceptor.WorkerInterceptor) (worker.WorkflowReplayer, error) {
	r, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{Interceptors: ics})
	if err != nil {
		return nil, err
	}
	r.RegisterWorkflow(Gate)
	return r, nil
}

// ReplayRefusal is an open gate whose history this build does not replay: Err holds [TMPRL1100]. Input is the gate's
// workflow input from its first event, nil when that could not be read: the release by hand needs its GATE_RESULTS and
// FAKE_GATE_DIR (ReleaseLine).
type ReplayRefusal struct {
	ID, RunID string
	Err       error
	Input     *GateRunInput
}

func (r ReplayRefusal) Error() string {
	first, _, _ := strings.Cut(r.Err.Error(), "\n")
	return fmt.Sprintf("open gate %s (run %s) does not replay on this build: %s", r.ID, r.RunID, first)
}

func (r ReplayRefusal) Unwrap() error { return r.Err }

// isNondeterminism: the task handler's mismatch errors and the command state machine's panics both carry
// [TMPRL1100] (SDK v1.49.0 matchReplayWithHistory,
// commandsHelper.getCommand).
func isNondeterminism(err error) bool {
	return err != nil && strings.Contains(err.Error(), "[TMPRL1100]")
}

// startedEnough: a gate whose history holds fewer than 3 events has WorkflowExecutionStarted and WorkflowTaskScheduled
// only (its worker died between the starter's poller check and the first poll). It recorded no command that could
// differ, and the replayer refuses any history under 3 events (SDK v1.49.0 replayWorkflowHistoryRoot),
// so it is skipped: refusing it would leave it stuck with no worker.
func startedEnough(historyLength int64) bool { return historyLength >= 3 }

// ReplayOpenGates replays each open Gate on queue in namespace ns, following the list's pages. An execution counts
// only when its task queue is queue and its type Gate, so another worktree's open gate never blocks this worker. Each
// is described first: a history under 3 events is skipped (startedEnough), by the run's own length, since visibility
// writes a run's history length only when the run closes (server v1.31.0 and v1.32.1 getClosedVisibilityRequest). A
// replay that fails with [TMPRL1100] is a ReplayRefusal; any other error (the list, a describe, a fetch, a replay that
// fails some other way) is returned as it is. Either way the
// worker does not poll. On a scratch dev server, an open Gate whose history scheduled activity barrier
// first was refused with the command state machine's [TMPRL1100]; a Gate of 2 events was skipped as not started yet,
// and ran once the worker polled; an open Gate on another queue did not count.
func ReplayOpenGates(ctx context.Context, c client.Client, ns, queue string, logger log.Logger) (replayed, skipped []string, err error) {
	req := &workflowservice.ListWorkflowExecutionsRequest{Namespace: ns, Query: OpenGateQuery(queue), PageSize: 100}
	for {
		resp, err := c.ListWorkflow(ctx, req)
		if err != nil {
			return replayed, skipped, fmt.Errorf("list the open gates: %w", err)
		}
		for _, e := range resp.GetExecutions() {
			if e.GetTaskQueue() != queue || e.GetType().GetName() != "Gate" {
				continue
			}
			id, run := e.GetExecution().GetWorkflowId(), e.GetExecution().GetRunId()
			d, err := c.DescribeWorkflowExecution(ctx, id, run)
			if err != nil {
				return replayed, skipped, fmt.Errorf("describe %s: %w", id, err)
			}
			if !startedEnough(d.GetWorkflowExecutionInfo().GetHistoryLength()) {
				logger.Info("not started yet: " + id)
				skipped = append(skipped, id)
				continue
			}
			r, err := NewGateReplayer()
			if err != nil {
				return replayed, skipped, err
			}
			if err := replayExecution(ctx, c, r, id, run, logger); err != nil {
				if isNondeterminism(err) {
					ref := ReplayRefusal{ID: id, RunID: run, Err: err}
					if gi, err := StartedGateInput(ctx, c, id, run); err == nil {
						ref.Input = &gi
					}
					return replayed, skipped, ref
				}
				return replayed, skipped, fmt.Errorf("replay %s: %w", id, err)
			}
			replayed = append(replayed, id)
		}
		if len(resp.GetNextPageToken()) == 0 {
			return replayed, skipped, nil
		}
		req.NextPageToken = resp.GetNextPageToken()
	}
}

// replayExecution pages run's history from c and replays it on r as a history, not by ReplayWorkflowExecution. Only
// in the replayer's own namespace, ReplayNamespace, does the replay go on past the point where the replayed code
// returns. ReplayWorkflowHistoryWithOptions passes that namespace; ReplayWorkflowExecution passes the gate's real one.
// Through it a build whose Gate returns before the recorded history ends would replay clean (SDK v1.49.0
// workflowExecutionContextImpl.ProcessWorkflowTask: shouldForceReplayCheck is true only when IsReplayNamespace, the
// event loop stops once the workflow completed unless forced, and the command match is skipped then too;
// ReplayWorkflowHistoryWithOptions passes ReplayNamespace, ReplayWorkflowExecution passes the namespace through). Gate
// reads no namespace, so the replay is the same otherwise. An open history replays to its last event and returns nil
// unless a command differs (SDK v1.49.0 replayWorkflowHistoryRoot). TestReplayCatchesEarlyReturn runs
// both calls on a cut history.
func replayExecution(ctx context.Context, c client.Client, r worker.WorkflowReplayer, id, run string, logger log.Logger) error {
	h := &historypb.History{}
	iter := c.GetWorkflowHistory(ctx, id, run, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		e, err := iter.Next()
		if err != nil {
			return fmt.Errorf("fetch %s: %w", id, err)
		}
		h.Events = append(h.Events, e)
	}
	return r.ReplayWorkflowHistoryWithOptions(logger, h, worker.ReplayWorkflowHistoryOptions{OriginalExecution: workflow.Execution{ID: id, RunID: run}})
}

// StartedGateInput is workflow id's input, from its WorkflowExecutionStarted event: what the starter passed, so the
// gate's GATE_RESULTS and FAKE_GATE_DIR as laug gate run -workflow had them.
func StartedGateInput(ctx context.Context, c client.Client, id, runID string) (GateRunInput, error) {
	var in GateRunInput
	iter := c.GetWorkflowHistory(ctx, id, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	if !iter.HasNext() {
		return in, fmt.Errorf("%s has no history", id)
	}
	e, err := iter.Next()
	if err != nil {
		return in, err
	}
	attrs := e.GetWorkflowExecutionStartedEventAttributes()
	if attrs == nil {
		return in, fmt.Errorf("%s's first event is %s", id, e.GetEventType())
	}
	if err := converter.GetDefaultDataConverter().FromPayloads(attrs.GetInput(), &in); err != nil {
		return in, fmt.Errorf("%s's input: %w", id, err)
	}
	return in, nil
}

// CLIFlags is make's ORCH_CLI for one address and namespace: what every temporal command laug prints carries. The
// CLI's default identity, temporal-cli:<user>@<host>, would name the user and host in history,
// and a config file, TEMPORAL_ENV or another TEMPORAL_* in the environment would point it elsewhere or
// stop it (the Makefile, ORCH_CLI).
func CLIFlags(addr, ns string) string {
	return fmt.Sprintf("--address %s --namespace %s --env default --identity laug-cli --disable-config-file --disable-config-env", addr, ns)
}

// LaugFlags is what a laug command the roles print needs to reach the server it was printed from: -orchestrator
// and its address. laug's verbs pass namespace default, so for another the line says it cannot reach that one.
func LaugFlags(addr, ns string) string {
	s := " -orchestrator " + addr
	if ns != "default" {
		s += " (namespace " + ns + ": laug reaches default only)"
	}
	return s
}
