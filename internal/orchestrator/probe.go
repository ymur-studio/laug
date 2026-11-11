// Package orchestrator is the gate workflow: laug's gate run driven by a Temporal workflow, on a server of its own
// (laug orchestrator server, 127.0.0.1:7400), with one worker per checkout (laug orchestrator worker).
//
// It holds the worker, this checkout's task queue and the Probe workflow below. steps.go is the step runner: Step
// runs one gate/step.sh step, Stop signals one, and a later attempt adopts a running step. gate.go is workflow
// Gate. It drives every step in gate/run.sh's order, with AwaitItem per item of the wave, a backstop timer, a
// progress query and the stop path. starter.go starts it for laug gate run -workflow. replay.go replays the open
// gates on the worker's queue at startup. record.go makes a history a replay fixture, and release.go is the stop
// path by hand.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Register puts every workflow and activity this worker runs on r: a worker.Worker, or the SDK's test env. repo is the
// checkout's root, where the step runner runs gate/step.sh. GateActivities' methods are Step, Stop and AwaitItem.
// identity is the worker's, laug-worker@<commit>, which step.sh's open checks against the checkout's HEAD; "" skips
// that check.
func Register(r interface {
	RegisterWorkflow(any)
	RegisterActivity(any)
}, repo, identity string) {
	register(r, &GateActivities{Repo: repo, Identity: identity})
}

// register is Register with the gate's activities as given: WorkerMain's carry the environment laug built.
func register(r interface {
	RegisterWorkflow(any)
	RegisterActivity(any)
}, gate *GateActivities) {
	r.RegisterWorkflow(Probe)
	r.RegisterActivity(&ProbeActivities{})
	r.RegisterWorkflow(Gate)
	r.RegisterActivity(gate)
}

// ProbeInput is what laug orchestrator probe sends.
type ProbeInput struct {
	// Seconds is how long Idle runs; 0 skips it.
	Seconds int `json:"seconds"`
	// Unpolled sends one HostStamp to <queue>-unpolled, a task queue no worker polls, with ScheduleToStartTimeout
	// UnpolledWait, and records how it fails.
	Unpolled bool `json:"unpolled"`
}

// UnpolledWait is the ScheduleToStartTimeout of the HostStamp sent to <queue>-unpolled.
const UnpolledWait = 20 * time.Second

// Stamp is this host's clocks, read by the worker.
type Stamp struct {
	// Wall is Unix time in seconds with exactly 3 decimals, written as the JSON number itself. A float64 would lose
	// a trailing 0 (1760496001.120 marshals as 1760496001.12), and a JSON payload keeps a RawMessage's digits.
	Wall json.RawMessage `json:"wall"`
	// Uptime is the first field of /proc/uptime, as the kernel wrote it.
	Uptime json.RawMessage `json:"uptime_s"`
	// BootID is /proc/sys/kernel/random/boot_id: a new value at every boot.
	BootID string `json:"boot_id"`
}

// Stamped is a Stamp next to the history's own clock: workflow.Now in the workflow task that scheduled HostStamp,
// and in the one that read its result. On one host the wall time falls between the two.
type Stamped struct {
	Before time.Time `json:"workflow_now_before"`
	Host   Stamp     `json:"host"`
	After  time.Time `json:"workflow_now_after"`
}

// IdleResult is what Idle did.
type IdleResult struct {
	Seconds    int `json:"seconds"`
	Heartbeats int `json:"heartbeats"`
}

// Unpolled is how the HostStamp sent to a task queue with no poller ended. WaitedS is history time.
type Unpolled struct {
	Queue            string  `json:"queue"`
	ScheduleToStartS float64 `json:"schedule_to_start_s"`
	WaitedS          float64 `json:"waited_s"`
	Error            string  `json:"error"`
	TimeoutType      string  `json:"timeout_type"`
	RetryState       string  `json:"retry_state"`
}

// ProbeResult is the Probe workflow's answer.
type ProbeResult struct {
	Queue    string      `json:"queue"`
	Start    Stamped     `json:"start"`
	Unpolled *Unpolled   `json:"unpolled,omitempty"`
	Idle     *IdleResult `json:"idle,omitempty"`
	End      Stamped     `json:"end"`
}

// Probe checks the plumbing: routing to this checkout's worker, the data converter, history time against host
// time, and, with in.Seconds, what a worker heartbeating every 2 s costs the host (laug orchestrator probe samples
// the server and the worker meanwhile).
func Probe(ctx workflow.Context, in ProbeInput) (ProbeResult, error) {
	res := ProbeResult{Queue: workflow.GetInfo(ctx).TaskQueueName}
	var a *ProbeActivities
	sctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Second,
		ScheduleToCloseTimeout: 2 * time.Minute,
	})
	var err error
	if res.Start, err = stamp(sctx, a); err != nil {
		return res, err
	}
	if in.Unpolled {
		u := Unpolled{Queue: res.Queue + "-unpolled", ScheduleToStartS: UnpolledWait.Seconds()}
		// No RetryPolicy: the default retries without limit, so a second attempt would show here as a longer wait.
		uctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			TaskQueue:              u.Queue,
			ScheduleToStartTimeout: UnpolledWait,
			StartToCloseTimeout:    30 * time.Second,
		})
		t0 := workflow.Now(ctx)
		var s Stamp
		err := workflow.ExecuteActivity(uctx, a.HostStamp).Get(uctx, &s)
		u.WaitedS = workflow.Now(ctx).Sub(t0).Seconds()
		describeUnpolled(err, &u)
		res.Unpolled = &u
	}
	if in.Seconds > 0 {
		ictx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Duration(in.Seconds)*time.Second + time.Minute,
			HeartbeatTimeout:    10 * time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
		})
		var r IdleResult
		if err := workflow.ExecuteActivity(ictx, a.Idle, in.Seconds).Get(ictx, &r); err != nil {
			return res, err
		}
		res.Idle = &r
	}
	res.End, err = stamp(sctx, a)
	return res, err
}

func stamp(ctx workflow.Context, a *ProbeActivities) (Stamped, error) {
	s := Stamped{Before: workflow.Now(ctx).UTC()}
	err := workflow.ExecuteActivity(ctx, a.HostStamp).Get(ctx, &s.Host)
	s.After = workflow.Now(ctx).UTC()
	return s, err
}

// describeUnpolled records the failure's shape: the timeout type, and the retry state the server gave it.
func describeUnpolled(err error, u *Unpolled) {
	if err == nil {
		return
	}
	u.Error = err.Error()
	var te *temporal.TimeoutError
	if errors.As(err, &te) {
		u.TimeoutType = te.TimeoutType().String()
	}
	var ae *temporal.ActivityError
	if errors.As(err, &ae) {
		u.RetryState = ae.RetryState().String()
	}
}

// ProbeActivities are the probe's two activities. The zero value reads the real clock and /proc and heartbeats
// every 2 s; tests set the fields.
type ProbeActivities struct {
	Now   func() time.Time
	Proc  string
	Every time.Duration
}

// HostStamp reads this host's wall clock, /proc/uptime and boot_id.
func (a *ProbeActivities) HostStamp(ctx context.Context) (Stamp, error) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	proc := "/proc"
	if a.Proc != "" {
		proc = a.Proc
	}
	wall := WallJSON(now())
	up, err := os.ReadFile(filepath.Join(proc, "uptime"))
	if err != nil {
		return Stamp{}, err
	}
	f := strings.Fields(string(up))
	if len(f) == 0 || !json.Valid([]byte(f[0])) {
		return Stamp{}, fmt.Errorf("%s/uptime: not a number: %q", proc, up)
	}
	boot, err := os.ReadFile(filepath.Join(proc, "sys/kernel/random/boot_id"))
	if err != nil {
		return Stamp{}, err
	}
	return Stamp{Wall: wall, Uptime: json.RawMessage(f[0]), BootID: strings.TrimSpace(string(boot))}, nil
}

// WallJSON writes t as Unix seconds with exactly 3 decimals.
func WallJSON(t time.Time) json.RawMessage {
	ms := t.UnixMilli()
	return json.RawMessage(fmt.Sprintf("%d.%03d", ms/1000, ms%1000))
}

// Idle waits seconds and heartbeats every 2 s. Heartbeats counts calls, not requests: at a 10 s HeartbeatTimeout the
// SDK sends at most one per 8 s unless the worker caps the throttle (SDK v1.49.0
// activityTaskHandlerImpl.getHeartbeatThrottleInterval), and 29 calls in 60 s reached the server as 8
// RecordActivityTaskHeartbeat requests (docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt section 8).
// The worker caps it at 2 s (WorkerMain), so each call goes out. A cancel reaches it on a heartbeat's answer.
func (a *ProbeActivities) Idle(ctx context.Context, seconds int) (IdleResult, error) {
	every := 2 * time.Second
	if a.Every > 0 {
		every = a.Every
	}
	r := IdleResult{Seconds: seconds}
	end := time.NewTimer(time.Duration(seconds) * time.Second)
	defer end.Stop()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-end.C:
			return r, nil
		case <-tick.C:
			r.Heartbeats++
			activity.RecordHeartbeat(ctx, r.Heartbeats)
		case <-ctx.Done():
			return r, ctx.Err()
		}
	}
}
