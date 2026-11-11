package orchestrator

// ProbeMain starts the Probe workflow on this checkout's task queue and prints its answer as the server stored it:
// laug orchestrator probe runs it in laug's own process.
//
// While the workflow runs, it samples the server's and the worker's RSS and CPU every 5 s from /proc into
// orchestrator/run/<workflow id>.jsonl, and prints a summary at the end. It refuses when no process holds this
// checkout's worker lock, and when the server lists no poller on the queue. When ctx ends, at laug's Ctrl-C, it asks
// the server to cancel the probe.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// probeIdentity is the probe's identity in history; the SDK's default names the host (SDK v1.49.0
// getWorkerIdentity).
const probeIdentity = "laug-probe"

// ProbeMain parses args with its own flags, the ones the probe binary took, and runs the probe on ctx. cmd is the laug
// command it runs for, orchestrator probe, which names its failure: "laug · <cmd>: <why>". environ is the environment
// laug built for it, which it reads in place of the process's: TEMPORAL_ADDRESS, TEMPORAL_NAMESPACE and
// TEMPORAL_API_KEY. Exit 0 done; 1 the workflow failed; 2 refused, interrupted, or a flag it does not take.
func ProbeMain(ctx context.Context, cmd string, args, environ []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "the checkout's root; laug passes its own")
	seconds := f.Int("seconds", 60, "how long Idle runs; 0 skips it")
	unpolled := f.Bool("unpolled", false, "also send one HostStamp to <queue>-unpolled, which no worker polls")
	serverPid := f.Int("server-pid", 0, "the server's pid, sampled every 5 s; 0 samples no server")
	clkTck := f.Float64("clk-tck", 100, "clock ticks per second in /proc/<pid>/stat (getconf CLK_TCK)")
	every := f.Duration("every", 5*time.Second, "sampling interval")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	code, err := runProbe(ctx, *repo, *seconds, *unpolled, *serverPid, *clkTck, *every, environ, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "laug · %s: %v\n", cmd, err)
	}
	return code
}

func runProbe(ctx context.Context, repo string, seconds int, unpolled bool, serverPid int, clkTck float64,
	every time.Duration, environ []string, stdout io.Writer) (int, error) {
	if repo == "" || seconds < 0 || clkTck <= 0 {
		return 2, fmt.Errorf("run %s orchestrator probe [-idle 60s] [-unpolled]", laugWord(environ))
	}
	addr := getenv(environ, "TEMPORAL_ADDRESS")
	if addr == "" {
		return 2, fmt.Errorf("TEMPORAL_ADDRESS is empty: run %s orchestrator probe, which sets it from -orchestrator",
			laugWord(environ))
	}
	ns := getenv(environ, "TEMPORAL_NAMESPACE")
	if ns == "" {
		ns = "default"
	}
	runDir := filepath.Join(repo, RunDir)
	queue, err := Queue(runDir, false, laugWord(environ))
	if err != nil {
		return 2, err
	}
	// The server's poller list alone cannot tell a stopped worker from a live one: it keeps listing a stopped worker
	// for about 5 min (docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt section 14). The worker
	// lock is held only while the worker runs.
	wpid, err := WorkerPid(runDir)
	if err != nil {
		return 2, err
	}
	if wpid == 0 {
		return 2, fmt.Errorf("no worker runs for this checkout (no process holds %s): %s orchestrator worker",
			filepath.Join(RunDir, WorkerLock), laugWord(environ))
	}
	opts := client.Options{HostPort: addr, Namespace: ns, Identity: probeIdentity}
	if key := getenv(environ, "TEMPORAL_API_KEY"); key != "" {
		opts.Credentials = client.NewAPIKeyStaticCredentials(key)
	}
	c, err := client.Dial(opts)
	if err != nil {
		return 2, fmt.Errorf("dial %s: %w (is %s orchestrator server running?)", addr, err, laugWord(environ))
	}
	defer c.Close()

	// Refuse before starting anything: with no poller the workflow would wait, not fail. After a server restart a
	// running worker was missing from the list for 11 and 13 s
	// (docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt sections 14 and 10).
	dq, err := c.DescribeTaskQueue(ctx, queue, enumspb.TASK_QUEUE_TYPE_WORKFLOW)
	if err != nil {
		return 2, fmt.Errorf("describe %s: %w", queue, err)
	}
	var pollers []string
	for _, p := range dq.GetPollers() {
		pollers = append(pollers, p.GetIdentity())
	}
	if len(pollers) == 0 {
		return 2, fmt.Errorf("worker pid %d runs, but the server lists no poller on %s yet; "+
			"after a server restart that lasts about 13 s: try again", wpid, queue)
	}

	id := "probe-" + time.Now().UTC().Format("20060102-150405.000")
	fmt.Fprintf(stdout, "laug · probe %s on %s at %s, worker pid %d, pollers %s\n", id, queue, addr, wpid,
		strings.Join(pollers, ", "))
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       id,
		TaskQueue:                queue,
		WorkflowExecutionTimeout: time.Duration(seconds)*time.Second + 10*time.Minute,
	}, Probe, ProbeInput{Seconds: seconds, Unpolled: unpolled})
	if err != nil {
		return 2, fmt.Errorf("start %s: %w", id, err)
	}

	roles := []probeRole{{name: "server", pid: serverPid, want: "start-dev"}, {name: "worker", pid: wpid,
		want: "worker"}}
	samplesPath := filepath.Join(runDir, id+".jsonl")
	done := make(chan struct{})
	summary := make(chan string, 1)
	go func() { summary <- sampleProbe(samplesPath, roles, clkTck, every, done) }()

	// json.RawMessage takes the payload's bytes as they are, so the wall time prints with the digits HostStamp wrote.
	var raw json.RawMessage
	err = run.Get(ctx, &raw)
	if ctx.Err() != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cerr := c.CancelWorkflow(cctx, id, "")
		cancel()
		close(done)
		<-summary
		return 2, fmt.Errorf("interrupted; cancel requested for %s (%v)", id, cerr)
	}
	close(done)
	sum := <-summary
	if err != nil {
		return 1, fmt.Errorf("%s: %w", id, err)
	}
	fmt.Fprintln(stdout, string(raw))
	var res ProbeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return 1, err
	}
	for _, s := range []struct {
		name string
		st   Stamped
	}{{"start", res.Start}, {"end", res.End}} {
		wall, err := parseWall(s.st.Host.Wall)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "%-5s host wall %s: %+.1f ms after workflow.Now before, %+.1f ms before workflow.Now "+
			"after\n", s.name, s.st.Host.Wall, ms(wall.Sub(s.st.Before)), ms(s.st.After.Sub(wall)))
	}
	if u := res.Unpolled; u != nil {
		fmt.Fprintf(stdout, "unpolled %s: %s after %.3f s of history time, retry state %s: %s\n", u.Queue,
			u.TimeoutType, u.WaitedS, u.RetryState, u.Error)
	}
	if res.Idle != nil {
		fmt.Fprintf(stdout, "idle %d s, %d heartbeat calls\n", res.Idle.Seconds, res.Idle.Heartbeats)
	}
	fmt.Fprint(stdout, sum)
	return 0, nil
}

// parseWall reads "<seconds>.<3 digits>" exactly.
func parseWall(w json.RawMessage) (time.Time, error) {
	s, frac, ok := strings.Cut(string(w), ".")
	sec, err1 := strconv.ParseInt(s, 10, 64)
	msec, err2 := strconv.ParseInt(frac, 10, 64)
	if !ok || len(frac) != 3 || err1 != nil || err2 != nil {
		return time.Time{}, fmt.Errorf("wall %q is not <seconds>.<3 digits>", w)
	}
	return time.UnixMilli(sec*1000 + msec).UTC(), nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// probeRole is a process the probe samples.
type probeRole struct {
	name string
	pid  int
	want string // a word the process's command line holds, so a reused pid is not sampled
}

// probeReading is one line of the samples file.
type probeReading struct {
	T      string  `json:"t"`
	Role   string  `json:"role"`
	RSSKiB int64   `json:"rss_kib"`
	CPUS   float64 `json:"cpu_s"`
}

// sampleProbe reads each role's RSS and CPU time at the start, every interval, and once more when done closes. It
// returns a summary line per role.
func sampleProbe(path string, roles []probeRole, clkTck float64, every time.Duration, done <-chan struct{}) string {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Sprintf("samples: %v\n", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	type agg struct {
		n            int
		minRSS       int64
		maxRSS       int64
		cpu0, cpu1   float64
		t0, t1       time.Time
		missing, bad bool
	}
	aggs := make([]agg, len(roles))
	for i, r := range roles {
		aggs[i].minRSS = math.MaxInt64
		aggs[i].bad = r.pid <= 0 || !cmdlineHas(r.pid, r.want)
	}
	read := func() {
		now := time.Now()
		for i, r := range roles {
			a := &aggs[i]
			if a.bad {
				continue
			}
			rss, cpu, err := readProc(r.pid, clkTck)
			if err != nil {
				a.missing = true
				continue
			}
			b, _ := json.Marshal(probeReading{T: now.UTC().Format("2006-01-02T15:04:05.000Z"), Role: r.name, RSSKiB: rss, CPUS: cpu})
			w.Write(append(b, '\n'))
			if a.n == 0 {
				a.cpu0, a.t0 = cpu, now
			}
			a.n++
			a.cpu1, a.t1 = cpu, now
			a.minRSS = min(a.minRSS, rss)
			a.maxRSS = max(a.maxRSS, rss)
		}
		w.Flush()
	}
	read()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			read()
		case <-done:
			read()
			var out strings.Builder
			fmt.Fprintf(&out, "samples every %s in %s\n", every, filepath.Join(RunDir, filepath.Base(path)))
			for i, r := range roles {
				a := aggs[i]
				switch {
				case a.bad:
					fmt.Fprintf(&out, "  %-6s not sampled: no live process for pid %d\n", r.name, r.pid)
				case a.n == 0:
					fmt.Fprintf(&out, "  %-6s no readings\n", r.name)
				default:
					span := a.t1.Sub(a.t0).Seconds()
					pct := 0.0
					if span > 0 {
						pct = 100 * (a.cpu1 - a.cpu0) / span
					}
					note := ""
					if a.missing {
						note = " (it exited during the probe)"
					}
					fmt.Fprintf(&out, "  %-6s %d readings over %.1f s: RSS %.1f to %.1f MiB, CPU %.2f s, %.2f%% of one CPU%s\n",
						r.name, a.n, span, float64(a.minRSS)/1024, float64(a.maxRSS)/1024, a.cpu1-a.cpu0, pct, note)
				}
			}
			return out.String()
		}
	}
}

func cmdlineHas(pid int, word string) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	return err == nil && strings.Contains(string(b), word)
}

// readProc returns VmRSS in KiB and utime plus stime in seconds.
func readProc(pid int, clkTck float64) (int64, float64, error) {
	st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	// Field 2, the command, is in parentheses and may hold spaces; utime and stime are fields 14 and 15.
	i := strings.LastIndexByte(string(st), ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("/proc/%d/stat: no command field", pid)
	}
	f := strings.Fields(string(st[i+1:]))
	if len(f) < 13 {
		return 0, 0, fmt.Errorf("/proc/%d/stat: %d fields after the command", pid, len(f))
	}
	ut, err1 := strconv.ParseFloat(f[11], 64)
	stt, err2 := strconv.ParseFloat(f[12], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("/proc/%d/stat: utime %q, stime %q", pid, f[11], f[12])
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("/proc/%d/status: VmRSS %q", pid, v)
			}
			return kb, (ut + stt) / clkTck, nil
		}
	}
	return 0, 0, fmt.Errorf("/proc/%d/status: no VmRSS", pid)
}
