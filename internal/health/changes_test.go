package health

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// The nine changes to HealthCheck a review replayed by hand against the recorded history, each a test now, against
// the committed fixture or REPLAY_HISTORY. The five that change a duration, an input, an option or the result replay
// clean; the four that change which commands run, or their order, fail with TMPRL1100. Replay compares command type,
// activity type, activity ID and timer ID, and nothing else (SDK 1.49.0 isCommandMatchEvent),
// so determinism means the same commands in the same order.

// hcOptions are HealthCheck's activity options, with the two a change may touch.
func hcOptions(ctx workflow.Context, startToClose time.Duration, attempts int32) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: startToClose,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: attempts},
	})
}

// awaitGo is HealthCheck's wait for the signal, for at most d.
func awaitGo(ctx workflow.Context, res *Result, d time.Duration) error {
	ok, err := workflow.AwaitWithTimeout(ctx, d, func() bool {
		return workflow.GetSignalChannel(ctx, SignalGo).ReceiveAsync(&res.Signal)
	})
	if err != nil || !ok {
		return fmt.Errorf("signal: %v, %t", err, ok)
	}
	return nil
}

// change is HealthCheck with one thing changed: what, what replay says, and the code.
type change struct {
	what string
	// fails is false for a clean replay. says is what a failure's error says after [TMPRL1100], a regexp; "" takes
	// any text.
	fails bool
	says  string
	code  func(ctx workflow.Context, name string) (Result, error)
}

var changes = []change{
	{what: "AwaitWithTimeout 30 s to 60 s", code: func(ctx workflow.Context, name string) (Result, error) {
		ctx = hcOptions(ctx, 10*time.Second, 3)
		var res Result
		if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
			return res, err
		}
		if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &res.Sum); err != nil {
			return res, err
		}
		if err := workflow.Sleep(ctx, time.Second); err != nil {
			return res, err
		}
		return res, awaitGo(ctx, &res, 60*time.Second)
	}},
	{what: "Sleep 1 s to 5 s", code: func(ctx workflow.Context, name string) (Result, error) {
		ctx = hcOptions(ctx, 10*time.Second, 3)
		var res Result
		if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
			return res, err
		}
		if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &res.Sum); err != nil {
			return res, err
		}
		if err := workflow.Sleep(ctx, 5*time.Second); err != nil {
			return res, err
		}
		return res, awaitGo(ctx, &res, 30*time.Second)
	}},
	{what: "StartToClose 10 s to 3 s, MaximumAttempts 3 to 1",
		code: func(ctx workflow.Context, name string) (Result, error) {
			ctx = hcOptions(ctx, 3*time.Second, 1)
			var res Result
			if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
				return res, err
			}
			if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &res.Sum); err != nil {
				return res, err
			}
			if err := workflow.Sleep(ctx, time.Second); err != nil {
				return res, err
			}
			return res, awaitGo(ctx, &res, 30*time.Second)
		}},
	{what: "Sum(19, 23) to Sum(1, 2)", code: func(ctx workflow.Context, name string) (Result, error) {
		ctx = hcOptions(ctx, 10*time.Second, 3)
		var res Result
		if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
			return res, err
		}
		// Replay hands back the recorded result, event 14's 42, whatever the inputs.
		if err := workflow.ExecuteActivity(ctx, Sum, 1, 2).Get(ctx, &res.Sum); err != nil {
			return res, err
		}
		if res.Sum != 42 {
			return res, fmt.Errorf("Sum(1, 2) gave %d on replay, not the recorded 42", res.Sum)
		}
		if err := workflow.Sleep(ctx, time.Second); err != nil {
			return res, err
		}
		return res, awaitGo(ctx, &res, 30*time.Second)
	}},
	{what: "a different return value", code: func(ctx workflow.Context, name string) (Result, error) {
		res, err := HealthCheck(ctx, name)
		res.Echoed, res.Sum = "other", -1
		return res, err
	}},
	{what: "remove the Sleep", fails: true, code: func(ctx workflow.Context, name string) (Result, error) {
		ctx = hcOptions(ctx, 10*time.Second, 3)
		var res Result
		if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
			return res, err
		}
		if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &res.Sum); err != nil {
			return res, err
		}
		return res, awaitGo(ctx, &res, 30*time.Second)
	}},
	{what: "AwaitWithTimeout to Await (no timer)", fails: true,
		code: func(ctx workflow.Context, name string) (Result, error) {
			ctx = hcOptions(ctx, 10*time.Second, 3)
			var res Result
			if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &res.Echoed); err != nil {
				return res, err
			}
			if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &res.Sum); err != nil {
				return res, err
			}
			if err := workflow.Sleep(ctx, time.Second); err != nil {
				return res, err
			}
			err := workflow.Await(ctx, func() bool {
				return workflow.GetSignalChannel(ctx, SignalGo).ReceiveAsync(&res.Signal)
			})
			return res, err
		}},
	{what: "two string activities, swapped", fails: true,
		says: `history event is ActivityTaskScheduled: \(ActivityId:6, ActivityType:\(Name:Echo\).*` +
			`replay command is ScheduleActivityTask: \(ActivityId:6, ActivityType:\(Name:Sum\)`,
		// Echo and Sum in the other order, each result read as any, so both recorded results decode where the
		// swapped code reads them, as two string activities' results would: the code then runs on to the end, and
		// the activity type check rejects the swap at event 6.
		code: func(ctx workflow.Context, name string) (Result, error) {
			ctx = hcOptions(ctx, 10*time.Second, 3)
			var res Result
			var first, second any
			if err := workflow.ExecuteActivity(ctx, Sum, 19, 23).Get(ctx, &first); err != nil {
				return res, err
			}
			if err := workflow.ExecuteActivity(ctx, Echo, name).Get(ctx, &second); err != nil {
				return res, err
			}
			if err := workflow.Sleep(ctx, time.Second); err != nil {
				return res, err
			}
			return res, awaitGo(ctx, &res, 30*time.Second)
		}},
	{what: "Echo and Sum swapped (TestReplayDetectsSwappedActivities)", fails: true,
		says: `lookup failed for scheduledEventID to activityID: scheduleEventID: 12,`, code: swappedHealthCheck},
}

// TestReplayChanges replays each change against the history and holds it to the table.
func TestReplayChanges(t *testing.T) {
	if len(changes) != 9 {
		t.Fatalf("%d changes, want the review's 9", len(changes))
	}
	for _, c := range changes {
		t.Run(c.what, func(t *testing.T) {
			r := worker.NewWorkflowReplayer()
			r.RegisterWorkflowWithOptions(c.code, workflow.RegisterOptions{Name: "HealthCheck"})
			err := r.ReplayWorkflowHistoryFromJSONFile(nil, history(t))
			t.Logf("replay: %v", err)
			switch {
			case !c.fails && err != nil:
				t.Fatalf("replay failed, want clean: %v", err)
			case c.fails && err == nil:
				t.Fatal("replay passed, want TMPRL1100")
			case c.fails && !regexp.MustCompile(`\[TMPRL1100\] .*`+c.says).MatchString(err.Error()):
				t.Fatalf("replay failed, but not with [TMPRL1100] %s: %v", c.says, err)
			}
		})
	}
}
