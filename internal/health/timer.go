package health

// TimerMain arms one durable timer on a cell and reports, in this host's time, when it fired: laug health timer runs
// it in laug's own process.
//
// It answers a question the first microVM restores left open
// (docs/tables/records.jsonl#cells/results/2026-10-13-firecracker.txt). A restored microVM's clock resumes at the
// pause, and the pool sets it forward before health: what happens to a timer armed before such a step? The workflow
// notes the server's clock at its first task, sleeps -timer, notes the server's clock again, then runs an activity here
// that returns this host's clock. Due is start plus timer in host time; fired is the activity's host time.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TimerTaskQueue is the queue TimerMain's worker polls and its workflow runs on.
const TimerTaskQueue = "timer-probe"

// TimerOut is what TimerProbe returns. Server times come from workflow.Now, the time of the workflow task that read
// it, which history stamps with its own clock.
type TimerOut struct {
	ServerAtStart time.Time `json:"server_at_start"`
	ServerAtFire  time.Time `json:"server_at_fire"`
	HostAtFire    time.Time `json:"host_at_fire"`
}

// TimerProbe sleeps d on a durable timer, then asks this host for the time.
func TimerProbe(ctx workflow.Context, d time.Duration) (TimerOut, error) {
	var o TimerOut
	o.ServerAtStart = workflow.Now(ctx)
	if err := workflow.Sleep(ctx, d); err != nil {
		return o, err
	}
	o.ServerAtFire = workflow.Now(ctx)
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
	err := workflow.ExecuteActivity(ctx, HostNow).Get(ctx, &o.HostAtFire)
	return o, err
}

// HostNow returns the clock of the host the worker runs on.
func HostNow(context.Context) (time.Time, error) { return time.Now(), nil }

type timerReport struct {
	TimerOut
	HostAtStart  time.Time `json:"host_at_start"`
	TimerS       float64   `json:"timer_s"`
	LateS        float64   `json:"late_s"`         // host_at_fire minus (host_at_start plus timer)
	ServerSkewS  float64   `json:"server_skew_s"`  // server_at_start minus host_at_start: the cell's clock offset
	ServerTimerS float64   `json:"server_timer_s"` // server_at_fire minus server_at_start: the timer as the server saw it
}

// TimerMain parses args with its own flags, the ones the timerprobe binary took, arms the timer and waits on ctx for
// it. cmd is the laug command it runs for, health timer, which starts its stderr lines: "laug · <cmd>: ". It reads no
// environment. The report is one JSON line on stdout; what it armed and each wait go to stderr. Exit 0 fired; 1 it
// failed or ctx ended first; 2 a flag or an argument it does not take.
func TimerMain(ctx context.Context, cmd string, args, _ []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(stderr)
	address := f.String("address", "127.0.0.1:7233", "frontend address")
	namespace := f.String("namespace", "default", "namespace")
	timer := f.Duration("timer", time.Minute, "the durable timer to arm")
	id := f.String("id", fmt.Sprintf("timer-probe-%d", time.Now().UnixMilli()), "workflow ID")
	taskTimeout := f.Duration("task-timeout", 0, "workflow task timeout; 0 keeps the server default (10 s)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() > 0 {
		fmt.Fprintf(stderr, "laug · %s: takes no arguments, got %q\n", cmd, f.Args())
		return 2
	}
	who := "laug · " + cmd

	c, err := client.Dial(client.Options{HostPort: *address, Namespace: *namespace, Identity: "laug-timerprobe",
		Logger: quietLogger{stderr, who}})
	if err != nil {
		fmt.Fprintln(stderr, who+": dial:", err)
		return 1
	}
	defer c.Close()
	w := worker.New(c, TimerTaskQueue, worker.Options{})
	w.RegisterWorkflow(TimerProbe)
	w.RegisterActivity(HostNow)
	if err := w.Start(); err != nil {
		fmt.Fprintln(stderr, who+": worker:", err)
		return 1
	}
	defer w.Stop()

	r := timerReport{HostAtStart: time.Now(), TimerS: timer.Seconds()}
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: *id, TaskQueue: TimerTaskQueue, WorkflowExecutionTimeout: *timer + 30*time.Minute,
		WorkflowTaskTimeout: *taskTimeout,
	}, TimerProbe, *timer)
	if err != nil {
		fmt.Fprintln(stderr, who+": start:", err)
		return 1
	}
	fmt.Fprintf(stderr, "%s: %s armed a %s timer at %s\n", who, run.GetID(), *timer,
		r.HostAtStart.UTC().Format(time.RFC3339Nano))
	// The result is a long poll. While the cell does not answer (a paused VM), each poll ends in a deadline or an
	// unavailable error, so keep asking until the workflow's own timeout, or until ctx ends.
	deadline := time.Now().Add(*timer + 30*time.Minute)
	for {
		err := run.Get(ctx, &r.TimerOut)
		if err == nil {
			break
		}
		transient := strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(),
			"Unavailable") || strings.Contains(err.Error(), "connection error")
		if !transient || ctx.Err() != nil || time.Now().After(deadline) {
			fmt.Fprintln(stderr, who+": result:", err)
			return 1
		}
		fmt.Fprintln(stderr, who+": result not yet:", err)
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr, who+": result:", ctx.Err())
			return 1
		case <-time.After(time.Second):
		}
	}
	r.LateS = r.HostAtFire.Sub(r.HostAtStart.Add(*timer)).Seconds()
	r.ServerSkewS = r.ServerAtStart.Sub(r.HostAtStart).Seconds()
	r.ServerTimerS = r.ServerAtFire.Sub(r.ServerAtStart).Seconds()
	b, _ := json.Marshal(r)
	fmt.Fprintln(stdout, string(b))
	return 0
}
