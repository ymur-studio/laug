package health

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// The fixture is a history recorded on a real cell by `laug health record`. The replayer runs offline:
// it feeds the recorded events to the workflow code and checks that every command the code issues
// matches the event the server recorded at that point. No server is involved, so a pass proves the code
// is deterministic against that history, nothing about the cell. The live round trip proves the cell.
//
// The replay check compares the sequence of commands: command type, activity ID and type, timer ID. It
// does not compare timer durations, activity inputs, options or the result, so a 60 s timeout or
// Sum(1, 2) replays clean against this fixture (SDK 1.49.0, TestReplayChanges). Removing the Sleep does not.
// Re-record with `laug health record -overwrite-fixture` after any change anyway, so the fixture keeps
// describing the code.
//
// The identity fields read laug-healthcheck and the host part of the sticky task queue name reads host:, as
// `laug health record` writes them; it wrote the committed fixture on the lab host (docs/CAPABILITIES.md). Replay reads
// neither field (SDK 1.49.0 WorkflowReplayer.replayWorkflowHistoryRoot, isCommandMatchEvent).
const fixture = "testdata/healthcheck_history.json"

// history is the file both tests replay: the committed fixture, or REPLAY_HISTORY when set (laug health replay
// <file>), a history exported by healthcheck -export on a cell of another server release. Both tests run on
// it, so another release's history must replay clean and still reject the swapped build.
func history(t *testing.T) string {
	if p := os.Getenv("REPLAY_HISTORY"); p != "" {
		t.Logf("replaying %s (REPLAY_HISTORY), not the committed fixture", p)
		return p
	}
	return fixture
}

func TestReplayHealthCheck(t *testing.T) {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(HealthCheck)
	if err := r.ReplayWorkflowHistoryFromJSONFile(nil, history(t)); err != nil {
		t.Fatalf("replay of recorded history failed (changed HealthCheck? run `laug health record "+
			"-overwrite-fixture`): %v", err)
	}
}

// TestReplayDetectsSwappedActivities replays HealthCheck with Echo and Sum swapped, which must fail. The recorded
// history schedules Echo then Sum. Replay hands recorded results to the code by event ID, so the swapped build
// receives Echo's "laug" where it waits for Sum's int. The decode fails, the workflow returns early, and event 12
// then has no command: TMPRL1100, "lookup failed for scheduledEventID". With results that decode, the activity type
// check rejects the swap at event 6 instead. Either way the error must be a nondeterminism error; a missing workflow
// type also fails replay, and would pass this test without the TMPRL1100 check.
func TestReplayDetectsSwappedActivities(t *testing.T) {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflowWithOptions(swappedHealthCheck, workflow.RegisterOptions{Name: "HealthCheck"})
	err := r.ReplayWorkflowHistoryFromJSONFile(nil, history(t))
	if err == nil {
		t.Fatal("replay passed with the activities swapped; the fixture or the check is not doing its job")
	}
	if !strings.Contains(err.Error(), "TMPRL1100") {
		t.Fatalf("replay failed, but not with a nondeterminism error: %v", err)
	}
	t.Logf("replayer rejected the swapped build: %v", err)
}

// swappedHealthCheck is HealthCheck with the two activity calls in the other order. Echo and Sum return
// different types, which is why this swap fails at event 12 and not at event 6.
func swappedHealthCheck(ctx workflow.Context, name string) (Result, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	var res Result
	if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &res.Sum); err != nil {
		return res, fmt.Errorf("sum: %w", err)
	}
	if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
		return res, fmt.Errorf("echo: %w", err)
	}
	if err := workflow.Sleep(ctx, time.Second); err != nil {
		return res, err
	}
	ok, err := workflow.AwaitWithTimeout(ctx, 30*time.Second, func() bool {
		return workflow.GetSignalChannel(ctx, SignalGo).ReceiveAsync(&res.Signal)
	})
	if err != nil || !ok {
		return res, fmt.Errorf("signal: %v", err)
	}
	return res, nil
}

// Keep the activity signatures honest at compile time; the replayer never runs them.
var _ = []any{
	func(ctx context.Context, s string) (string, error) { return Echo(ctx, s) },
	func(ctx context.Context, a, b int) (int, error) { return Sum(ctx, a, b) },
}
