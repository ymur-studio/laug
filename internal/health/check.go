package health

// CheckMain runs the HealthCheck workflow against a cell and reports what it saw, as numbers. laug health check, loop
// and record run it in laug's own process; cells/health.sh and the compose and Firecracker backends run it as
// bin/laug health check.
//
// It starts a worker in this process, starts the workflow, sends the signal, waits for the result, then asks
// visibility for the run. One call, one exit code. -export writes the event history as protojson for the replay
// test; -loop repeats the round trip so Grafana has a series to draw.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/uber-go/tally/v4"
	"github.com/uber-go/tally/v4/prometheus"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	sdktally "go.temporal.io/sdk/contrib/tally"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/encoding/protojson"
)

// VisibilityWait is how long the round trip waits for its run to show in visibility, unless -visibility-wait says.
const VisibilityWait = 15 * time.Second

// CheckMain parses args with its own flags, the ones the healthcheck binary took, and runs the round trip on ctx:
// once, or every -loop until ctx ends. cmd is the laug command it runs for, health check, loop or record, which names
// its stderr lines: "laug · <cmd>: <what failed>: <why>". It reads no environment: the cell is -address and the
// namespace -namespace, whatever CELL_ADDRESS or NAMESPACE say, so its environment argument goes unread. Its lines go
// to stdout and a failure, one line, to stderr. Exit 0 the round trip held; 1 it did not; 2 a flag or an argument it
// does not take.
func CheckMain(ctx context.Context, cmd string, args, _ []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(stderr)
	address := f.String("address", "127.0.0.1:7233", "frontend address")
	namespace := f.String("namespace", "default", "namespace")
	metrics := f.String("metrics", ":9091", "SDK Prometheus metrics listen address; empty disables")
	export := f.String("export", "", "write the run's event history as protojson to this path")
	loop := f.Duration("loop", 0, "repeat the round trip at this interval until interrupted; 0 runs once")
	visWait := f.Duration("visibility-wait", VisibilityWait, "how long to wait for the run to appear in visibility")
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
	if err := check(ctx, who, *address, *namespace, *metrics, *export, *loop, *visWait, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", who, err)
		return 1
	}
	return 0
}

// check runs the round trip, once or every loop; who, "laug · <cmd>", starts each line it writes on stderr.
func check(ctx context.Context, who, address, namespace, metricsAddr, export string, loop, visWait time.Duration,
	stdout, stderr io.Writer) error {
	// Identity is fixed so a recorded history names no host or PID. The SDK default is "<pid>@<hostname>@"
	// (SDK 1.49.0 getWorkerIdentity), and the worker inherits the client's identity
	// (NewAggregatedWorker).
	opts := client.Options{HostPort: address, Namespace: namespace, Identity: recorderIdentity,
		Logger: quietLogger{stderr, who}}
	if metricsAddr != "" {
		// Summaries (tally's default) have no _bucket series, so histogram_quantile has nothing to read.
		reporter := prometheus.NewReporter(prometheus.Options{
			DefaultTimerType:        prometheus.HistogramTimerType,
			DefaultHistogramBuckets: prometheus.DefaultHistogramBuckets(),
		})
		scope, closer := tally.NewRootScope(tally.ScopeOptions{
			CachedReporter:  reporter,
			Separator:       prometheus.DefaultSeparator,
			SanitizeOptions: &sdktally.PrometheusSanitizeOptions,
			Prefix:          "laug_health",
		}, time.Second)
		defer closer.Close()
		opts.MetricsHandler = sdktally.NewMetricsHandler(sdktally.NewPrometheusNamingScope(scope))
		// A mux and a server of its own, not net/http's defaults: laug runs this in its own process, so the
		// listener closes when the round trips end, and nothing is registered on a mux laug shares.
		mux := http.NewServeMux()
		mux.Handle("/metrics", reporter.HTTPHandler())
		srv := &http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(stderr, "%s: metrics listener: %v\n", who, err)
			}
		}()
		defer srv.Close()
	}

	c, err := client.Dial(opts)
	if err != nil {
		return fmt.Errorf("dial %s: %w", address, err)
	}
	defer c.Close()

	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(HealthCheck)
	w.RegisterActivity(Echo)
	w.RegisterActivity(Sum)
	if err := w.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	defer w.Stop()

	for {
		if err := roundTrip(ctx, c, namespace, export, visWait, stdout); err != nil {
			return err
		}
		if loop <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(loop):
		}
	}
}

func roundTrip(ctx context.Context, c client.Client, namespace, export string, visWait time.Duration,
	stdout io.Writer) error {
	id := fmt.Sprintf("health-%d", time.Now().UnixMilli())
	t0 := time.Now()

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       id,
		TaskQueue:                TaskQueue,
		WorkflowExecutionTimeout: 2 * time.Minute,
	}, HealthCheck, "laug")
	if err != nil {
		return fmt.Errorf("start %s: %w", id, err)
	}
	if err := c.SignalWorkflow(ctx, id, run.GetRunID(), SignalGo, "go"); err != nil {
		return fmt.Errorf("signal %s: %w", id, err)
	}
	var res Result
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("result %s: %w", id, err)
	}
	completed := time.Since(t0)
	if res.Echoed != "laug" || res.Sum != 42 || res.Signal != "go" {
		return fmt.Errorf("%s returned %+v, expected {laug 42 go}", id, res)
	}

	// Visibility is indexed asynchronously by history's visibility tasks. Nothing else in the round trip
	// reads from it, so a cell with a broken visibility pipeline would pass without this.
	visible, err := waitVisible(ctx, c, namespace, id, visWait)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "%s  completed %d ms  visible %d ms  echo=%s sum=%d signal=%s\n",
		id, completed.Milliseconds(), visible.Milliseconds(), res.Echoed, res.Sum, res.Signal)

	if export != "" {
		if err := exportHistory(ctx, c, id, run.GetRunID(), export); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "history written to %s\n", export)
	}
	return nil
}

func waitVisible(ctx context.Context, c client.Client, namespace, id string, wait time.Duration) (time.Duration,
	error) {
	t0 := time.Now()
	query := fmt.Sprintf(`WorkflowType="HealthCheck" AND WorkflowId="%s"`, id)
	for {
		resp, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{Namespace: namespace,
			Query: query})
		if err != nil {
			return 0, fmt.Errorf("list %s: %w", id, err)
		}
		if len(resp.Executions) > 0 {
			return time.Since(t0), nil
		}
		if time.Since(t0) > wait {
			return 0, fmt.Errorf("%s not in visibility after %s", id, wait)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func exportHistory(ctx context.Context, c client.Client, id, runID, path string) error {
	iter := c.GetWorkflowHistory(ctx, id, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var h historypb.History
	for iter.HasNext() {
		e, err := iter.Next()
		if err != nil {
			return fmt.Errorf("history %s: %w", id, err)
		}
		h.Events = append(h.Events, e)
	}
	scrubHost(&h)
	b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(&h)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// recorderIdentity is the client and worker identity, so fixture events read the same on every host.
const recorderIdentity = "laug-healthcheck"

// scrubHost keeps the host name out of an exported fixture. The sticky task queue name is
// "<hostname>:<uuid>" whatever Identity says (SDK 1.49.0 getWorkerTaskQueue). Replay matches
// on neither the identity nor this name: it polls the first event's task queue as "replayID"
// (WorkflowReplayer.replayWorkflowHistoryRoot) and compares command type, activity ID and type, and timer ID
// (isCommandMatchEvent).
func scrubHost(h *historypb.History) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return
	}
	for _, e := range h.Events {
		tq := e.GetWorkflowTaskScheduledEventAttributes().GetTaskQueue()
		if tq.GetKind() != enumspb.TASK_QUEUE_KIND_STICKY {
			continue
		}
		if rest, ok := strings.CutPrefix(tq.GetName(), host+":"); ok {
			tq.Name = "host:" + rest
		}
	}
}

// quietLogger drops the SDK's info and debug lines so the output stays the numbers; a warning or an error goes to
// w, one line each, after name, "laug · <cmd>".
type quietLogger struct {
	w    io.Writer
	name string
}

func (quietLogger) Debug(string, ...interface{}) {}
func (quietLogger) Info(string, ...interface{})  {}
func (l quietLogger) Warn(msg string, kv ...interface{}) {
	fmt.Fprintln(l.w, append([]interface{}{l.name + ": warn:", msg}, kv...)...)
}
func (l quietLogger) Error(msg string, kv ...interface{}) {
	fmt.Fprintln(l.w, append([]interface{}{l.name + ": error:", msg}, kv...)...)
}
