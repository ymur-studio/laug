package orchestrator

// WorkerMain is this checkout's worker: laug orchestrator worker runs it in laug's own process, in the
// foreground against -orchestrator (127.0.0.1:7400, the server laug orchestrator server runs).
//
// It polls one task queue, this checkout's laug-gate-<8 hex> (orchestrator/run/queue), as laug-worker@<commit>, the
// commit laug was built from. It holds orchestrator/run/worker.lock while it runs, so a second worker for this
// checkout refuses, and writes its pid and identity into it, so the starter can tell which build holds it. It opens
// no metrics listener and no sessions. It sends credentials only when TEMPORAL_API_KEY is set, because the SDK turns
// TLS on for any API key (SDK v1.49.0 apiKeyCredentials.applyToOptions), and the dev server speaks no TLS: with
// TEMPORAL_API_KEY set, the dial fails with "tls: first record does not look like a TLS handshake"
// (docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt section 14).
// laug's environment for it holds no TEMPORAL_* but the address and the namespace, so through laug it sends none.
//
// With -otlp 127.0.0.1:<port> (laug orchestrator worker -otlp 127.0.0.1:7751) it traces Gate through the SDK's
// OpenTelemetry interceptor, service laug-worker, OTLP over HTTP to that address; without it, the default, it builds no
// interceptor and no provider (trace.go). Either way it opens no listener.
//
// It runs Probe, workflow Gate and the activities Step, Stop and AwaitItem, which run and read
// gate/step.sh in the checkout -repo names. Its identity reaches step.sh, whose open checks the commit in it against
// the checkout's HEAD. Before it polls, it replays every open Gate on its queue against this build (ReplayOpenGates):
// a gate this build would fail mid-run is refused here, and the worker returns 2 without polling, naming the gate and
// how to end it.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
)

// WorkerMain parses args with its own flags, -repo, -commit and -otlp, and runs the worker until ctx ends. cmd is the
// laug command it runs for, orchestrator worker, which names its refusal: "laug · <cmd>: <why>". environ is the
// environment laug built for it, which it reads in place of the process's: TEMPORAL_ADDRESS, TEMPORAL_NAMESPACE and
// TEMPORAL_API_KEY, and what step.sh's environment is built from. Its lines go to stdout, the SDK's log to stderr.
// Exit 0 stopped; 2 refused, or a flag it does not take.
func WorkerMain(ctx context.Context, cmd string, args, environ []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "the checkout's root; laug passes its own")
	commit := f.String("commit", "", "the commit laug was built from, <sha>[-dirty], which the worker's identity "+
		"names; laug passes its own")
	otlp := f.String("otlp", "", "127.0.0.1:<port>: trace Gate over OTLP/HTTP to the stack's Collector (laug's -otlp); "+
		"empty, the default, traces nothing")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := runWorker(ctx, *repo, *commit, *otlp, environ, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "laug · %s: %v\n", cmd, err)
		return 2
	}
	return 0
}

func runWorker(ctx context.Context, repo, commit, otlp string, environ []string, stdout, stderr io.Writer) error {
	if repo == "" {
		return fmt.Errorf("-repo is required: run %s orchestrator worker", laugWord(environ))
	}
	// The SDK reads TEMPORAL_SDK_FLAG_<n> and TEMPORAL_DEBUG once, at init, before main (SDK v1.49.0
	// internal_flags.go's init, internal_worker.go's debugMode): from laug's own environment, which is why
	// this reads the process's and not environ. TEMPORAL_DEBUG would lift the deadlock detector's limit, but the
	// detector's timeout is set to 1 s at :187, so it does not lift it here.
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "TEMPORAL_SDK_FLAG_") || name == "TEMPORAL_DEBUG" {
			return fmt.Errorf("%s is set; the SDK read it when laug started: unset it, then start %s orchestrator "+
				"worker again", name, laugWord(environ))
		}
	}
	addr := getenv(environ, "TEMPORAL_ADDRESS")
	if addr == "" {
		// The SDK's default is localhost:7233 (SDK v1.49.0 LocalHostPort), block 0's cell
		// frontend during a gate.
		return fmt.Errorf("TEMPORAL_ADDRESS is empty: run %s orchestrator worker, which sets it from -orchestrator",
			laugWord(environ))
	}
	ns := getenv(environ, "TEMPORAL_NAMESPACE")
	if ns == "" {
		ns = "default"
	}
	runDir := filepath.Join(repo, RunDir)
	queue, err := Queue(runDir, true, laugWord(environ))
	if err != nil {
		return err
	}
	sha, dirty := strings.CutSuffix(commit, "-dirty")
	modified := ""
	if dirty {
		modified = "true"
	}
	id := WorkerIdentity(sha, modified)

	// Before dialing, which can take seconds: the kernel gives the lock to one of two workers started at once. The
	// holder's identity goes into the lock file, and the starter refuses a gate unless it names HEAD.
	lock, err := LockWorker(runDir, id)
	if err != nil {
		return err
	}
	defer lock.Close()

	// The SDK's default logger writes local time; laug writes UTC.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{ReplaceAttr: func(groups []string,
		a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(groups) == 0 {
			a.Value = slog.TimeValue(a.Value.Time().UTC())
		}
		return a
	}}))
	// Before the dial, so a refused -otlp starts nothing. Stop runs after the worker stopped and the client closed
	// (defers run last in, first out), so the last spans go out: 2 s at most.
	tr, err := NewTracing(otlp, "laug-worker", func(err error) { logger.Warn("otlp: spans not sent", "error", err) })
	if err != nil {
		return err
	}
	defer func() {
		if err := tr.Stop(); err != nil {
			logger.Warn("otlp: at stop", "error", err)
		}
	}()
	opts := client.Options{HostPort: addr, Namespace: ns, Identity: id, Logger: sdklog.NewStructuredLogger(logger),
		Interceptors: tr.ClientInterceptors()}
	if key := getenv(environ, "TEMPORAL_API_KEY"); key != "" {
		opts.Credentials = client.NewAPIKeyStaticCredentials(key)
	}
	c, err := client.Dial(opts)
	if err != nil {
		return fmt.Errorf("dial %s: %w (is %s orchestrator server running?)", addr, err, laugWord(environ))
	}
	defer c.Close()

	// The startup replay, before the worker polls.
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ids, skipped, err := ReplayOpenGates(rctx, c, ns, queue, opts.Logger)
	cancel()
	var refusal ReplayRefusal
	switch {
	case errors.As(err, &refusal):
		// Each command on a line of its own, ending with its last value, so a copy takes no punctuation with it.
		mv := LaugFlags(addr, ns)
		var in GateRunInput
		if refusal.Input != nil {
			in = *refusal.Input
		}
		gid := strings.TrimPrefix(refusal.ID, "gate-")
		return fmt.Errorf("%s. This worker does not poll. Start the build that runs it, or end it and release it by "+
			"hand:\n  %s gate terminate %s -reason 'does not replay on this build'%s\n  %s",
			strings.TrimSuffix(refusal.Error(), "."), laugWord(environ), gid, mv, ReleaseLine(laugWord(environ), gid, in,
				mv))
	case err != nil:
		return fmt.Errorf("the startup replay could not check the open gates on %s: %v; the worker does not poll "+
			"unchecked", queue, err)
	case len(ids) == 0 && len(skipped) == 0:
		fmt.Fprintf(stdout, "laug · worker %s: no open gate on %s\n", id, queue)
	default:
		names := strings.Join(ids, ", ")
		if names == "" {
			names = "none"
		}
		line := fmt.Sprintf("laug · worker %s: replayed %d open gate(s) on %s on this build: %s", id, len(ids), queue,
			names)
		if len(skipped) > 0 {
			line += "; not started yet: " + strings.Join(skipped, ", ")
		}
		fmt.Fprintln(stdout, line)
	}

	// 1 s is the SDK's default (SDK v1.49.0 defaultDeadlockDetectionTimeout); set here so that nothing in the
	// environment can lift it. The SDK sends a pending heartbeat at most every 0.8 x the activity's heartbeat timeout,
	// 8 s against the step runner's 10 s, capped by MaxHeartbeatThrottleInterval, 60 s by default (SDK v1.49.0
	// getHeartbeatThrottleInterval, WorkerOptions.MaxHeartbeatThrottleInterval). 2 s of margin was not enough on the
	// dev host, whose wall clock steps about 3 s at a time: the wave and both awaits timed out on heartbeats and
	// retried in 5 of 5 runs with no restart (docs/tables/records.jsonl#cells/results/2026-10-16-gate6-step6.txt
	// section 2.1). At 2 s the runner's beats, every 2 s, go out as sent, and the margin is 8 s.
	w := worker.New(c, queue, worker.Options{Identity: id, DeadlockDetectionTimeout: time.Second,
		MaxHeartbeatThrottleInterval: 2 * time.Second})
	register(w, &GateActivities{Repo: repo, Identity: id, Environ: environ})
	traces := ""
	if tr != nil {
		traces = ", traces to " + tr.Endpoint() + " (OTLP over HTTP, service laug-worker)"
	}
	fmt.Fprintf(stdout, "laug · worker %s polls %s at %s, namespace %s%s\n", id, queue, addr, ns, traces)
	// Run stops the worker when this channel closes: when ctx ends, at laug's first Ctrl-C or TERM.
	stop := make(chan any)
	go func() {
		<-ctx.Done()
		close(stop)
	}()
	return w.Run(stop)
}
