// Package health is the HealthCheck workflow a cell must round-trip before it counts as healthy.
//
// The workflow touches frontend, matching and history: two activities (matching dispatches the task, history
// records the result), a durable timer (history's timer queue; a timer is a persisted task, not a goroutine),
// and a signal (frontend accepts it, history appends it). The internal worker role (system workflows) is not on
// this path. Completion proves the cell ran it; the replay test in replay_test.go proves the code is
// deterministic against the recorded history: two different claims, and the design doc keeps them apart.
package health

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TaskQueue is the queue the worker polls and cell-health starts on.
const TaskQueue = "cell-health"

// SignalGo is the signal the workflow waits for after its timer fires.
const SignalGo = "go"

// Result is what HealthCheck returns. Every field is a number or a string the caller can print.
type Result struct {
	Echoed string `json:"echoed"`
	Sum    int    `json:"sum"`
	Signal string `json:"signal"`
}

// HealthCheck is the workflow: Echo, Sum, a 1 s timer, then wait for the "go" signal.
//
// Echo and Sum are different activity types on purpose. Replay compares each command with the recorded event by
// command type, activity ID and activity type, not by input (replay_test.go), so two calls to the same activity
// swapped would replay clean and TestReplayDetectsSwappedActivities would pass vacuously.
func HealthCheck(ctx workflow.Context, name string) (Result, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})

	var res Result
	if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
		return res, fmt.Errorf("echo: %w", err)
	}
	if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &res.Sum); err != nil {
		return res, fmt.Errorf("sum: %w", err)
	}

	// Activity timeouts are timer tasks too (server 1.31.0 timer_sequence.go, ActivityTimeoutTask), so an
	// activities-only workflow touches the timer queue. Only an explicit Sleep proves a timer fires.
	if err := workflow.Sleep(ctx, time.Second); err != nil {
		return res, fmt.Errorf("timer: %w", err)
	}

	// This is the form stop 1 measures (docs/LEARNING.md): event 23 of testdata/healthcheck_history.json is its 30 s
	// timer, started although the signal, event 3, was already in. The idiomatic form is
	// GetSignalChannel(ctx, SignalGo).ReceiveWithTimeout, and it starts the same timer: it calls AwaitWithTimeout with
	// c.Len() > 0 as its condition, and AwaitWithTimeout arms the timer before it tests the condition unless SDK flag 6,
	// SDKFlagCancelAwaitTimerOnCondition, is on, and the flag is off by default (read and not run: SDK v1.49.0
	// channelImpl.ReceiveWithTimeout, awaitWithOptions and sdkFlagsAllowed in internal/internal_flags.go). Kept as
	// recorded: stop 1, its figure and the fixture describe this code.
	ok, err := workflow.AwaitWithTimeout(ctx, 30*time.Second, func() bool {
		return workflow.GetSignalChannel(ctx, SignalGo).ReceiveAsync(&res.Signal)
	})
	if err != nil {
		return res, fmt.Errorf("signal: %w", err)
	}
	if !ok {
		return res, fmt.Errorf("signal %q did not arrive within 30 s", SignalGo)
	}
	return res, nil
}

// Echo returns its input. One activity type.
func Echo(ctx context.Context, name string) (string, error) {
	activity.GetLogger(ctx).Debug("echo", "name", name)
	return name, nil
}

// Sum adds two ints. The second, distinct activity type.
func Sum(ctx context.Context, a, b int) (int, error) {
	return a + b, nil
}
