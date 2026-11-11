package main

// laug orchestrator: its own Temporal dev server and the programs around it. server runs the temporal CLI's dev server;
// worker, probe and record run internal/orchestrator's roles, WorkerMain, ProbeMain and RecordMain, in laug's own
// process. make test-go runs internal/orchestrator's tests. gate run -workflow, gate status, cancel, terminate and stop
// reach the same server through the starter's role, StarterMain (gateflow.go). This file and health.go are the only
// ones here that import a package linking the Temporal SDK.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ymur-studio/laug/internal/orchestrator"
	"github.com/ymur-studio/laug/internal/script"
)

// orchRoles are internal/orchestrator's roles, by name.
var orchRoles = map[string]roleMain{"worker": orchestrator.WorkerMain, "gate": orchestrator.StarterMain,
	"probe": orchestrator.ProbeMain, "record": orchestrator.RecordMain}

// orchFixtures are the replay fixtures, one finished Gate history each, in orchFixtureDir as <name>.json. make test-go
// replays every one.
var orchFixtures = []string{"advance", "refused", "harness", "cancelled", "adopted", "real"}

const orchFixtureDir = "internal/orchestrator/testdata/histories"

// denylistEnv names the variable that gives the record role a deny list to check a fixture against, read as
// check/denylist.sh reads it (internal/denylist). Unset or empty, the role checks no list. laug passes the path and
// never reads the file.
const denylistEnv = "LAUG_DENYLIST"

// orchFlag adds -orchestrator: the orchestrator's dev server, ORCH_ADDRESS.
func orchFlag(f *flags) *string {
	return f.String("orchestrator", orchAddressDefault, "the orchestrator's dev server, `host:port`")
}

// checkOrch returns an error naming -orchestrator when addr is not a host and a port from 1 to 65535, or holds a single
// quote or a blank. The laug and temporal lines the roles print carry addr unquoted (orchestrator.LaugFlags, CLIFlags).
func checkOrch(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	n, nerr := strconv.Atoi(port)
	if err != nil || host == "" || nerr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port ||
		strings.ContainsFunc(addr, func(r rune) bool { return r == '\'' || unicode.IsSpace(r) }) {
		return fmt.Errorf("-orchestrator %s: a host and a port, %s say, with no quote or blank", shellWord(addr),
			orchAddressDefault)
	}
	return nil
}

// orchEnv is TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE, which the worker, the starter, the probe and the recorder read
// from the environment laug builds for them; internal/script's environment holds no other TEMPORAL_* or OTEL_* name.
func orchEnv(addr string) []script.Var {
	return []script.Var{script.Quoted("TEMPORAL_ADDRESS", addr), script.Quoted("TEMPORAL_NAMESPACE", orchNamespace)}
}

// orchLine is a laug line that reaches the server at addr: -orchestrator after it when addr is not the default.
func orchLine(line, addr string) string {
	if addr != orchAddressDefault {
		line += " -orchestrator " + shellWord(addr)
	}
	return line
}

// otlpRE is -otlp's shape: the stack's Collector on 127.0.0.1, since it has no auth.
var otlpRE = regexp.MustCompile(`^127\.0\.0\.1:[1-9][0-9]{0,4}$`)

// otlpFlag adds -otlp: where workflow Gate's traces go, ORCH_OTLP. with, when not empty, names the flag it goes with.
func otlpFlag(f *flags, with string) *string {
	usage := "trace workflow Gate over OTLP/HTTP to the stack's Collector, `127.0.0.1:port`, 127.0.0.1:7751; unset, " +
		"no traces"
	if with != "" {
		usage = "with " + with + ", " + usage
	}
	return f.String("otlp", "", usage)
}

// checkOTLP refuses an -otlp that is not empty or 127.0.0.1:<port>.
func checkOTLP(v string) error {
	if v != "" && !otlpRE.MatchString(v) {
		return fmt.Errorf("-otlp %s: 127.0.0.1:<port>, the stack's Collector, 127.0.0.1:7751, or unset: it has no auth",
			shellWord(v))
	}
	return nil
}

// repo is the -repo the orchestrator roles take: the checkout's root.
func (a *App) repo() []string {
	return []string{"-repo", a.Root}
}

func runOrchestratorServer(_ context.Context, a *App, args []string) int {
	f := a.newFlags("orchestrator server", "[-orchestrator "+orchAddressDefault+"] [-http-port "+
		strconv.Itoa(orchHTTPPortDefault)+"] [-metrics-port "+strconv.Itoa(orchMetricsPortDefault)+"] [-ui-port "+
		strconv.Itoa(orchUIPortDefault)+"] [-dry-run]")
	addr := f.String("orchestrator", orchAddressDefault, "where the dev server listens, `127.0.0.1:port`: it has no "+
		"auth")
	ports := []struct {
		flag, what string
		n          *int
	}{
		{"http-port", "its HTTP API's", f.Int("http-port", orchHTTPPortDefault, "its HTTP API's `port`")},
		{"metrics-port", "its Prometheus metrics'", f.Int("metrics-port", orchMetricsPortDefault,
			"its Prometheus metrics' `port`")},
		{"ui-port", "its web UI's", f.Int("ui-port", orchUIPortDefault, "its web UI's `port`")},
	}
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	err := checkOrch(*addr)
	host, port, _ := net.SplitHostPort(*addr)
	if err == nil && host != "127.0.0.1" {
		err = fmt.Errorf("-orchestrator %s: the dev server has no auth, so it listens on 127.0.0.1 alone",
			shellWord(*addr))
	}
	for _, p := range ports {
		if err == nil && (*p.n < 1 || *p.n > 65535) {
			err = fmt.Errorf("-%s %d: a port, 1 to 65535", p.flag, *p.n)
		}
	}
	if err != nil {
		return a.refused("orchestrator server", err)
	}
	db := orchRunDir + "/dev.db"
	c := script.Command{Script: "temporal", Tool: true, Args: []script.Value{{Text: "server"}, {Text: "start-dev"},
		{Text: "--ip"}, {Text: host}, {Text: "--port"}, {Text: port, Quote: true},
		{Text: "--http-port"}, {Text: strconv.Itoa(*ports[0].n)}, {Text: "--metrics-port"},
		{Text: strconv.Itoa(*ports[1].n)}, {Text: "--ui-port"}, {Text: strconv.Itoa(*ports[2].n)},
		{Text: "--db-filename"}, {Text: db, Quote: true}},
		Clear: []string{"ORCH_ADDRESS", "ORCH_HTTP_PORT", "ORCH_METRICS_PORT", "ORCH_UI_PORT"}}
	c, env, code, ok := a.prepare("orchestrator server", c, *dry)
	if !ok {
		return code
	}
	if *dry {
		fmt.Fprintln(a.Stdout, script.DryRun(c))
		return exitDone
	}
	// orchestrator/run/ is gitignored, so a running server never makes a gate's tree dirty.
	if err := os.MkdirAll(filepath.Join(a.Root, orchRunDir), 0o755); err != nil {
		fmt.Fprintf(a.Stderr, "laug · orchestrator server: %v\n", a.errShown(err))
		return exitFailed
	}
	first := fmt.Sprintf("laug · orchestrator server: the orchestrator's dev server on %s, HTTP %d, metrics %d, UI %d, its "+
		"state in %s; Ctrl-C stops it", *addr, *ports[0].n, *ports[1].n, *ports[2].n, db)
	return a.start("orchestrator server", c, env, script.Exec, first, a.Stdout)
}

func runOrchestratorWorker(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("orchestrator worker", "[-orchestrator "+orchAddressDefault+"] [-otlp 127.0.0.1:<port>]")
	addr := orchFlag(f)
	otlp := otlpFlag(f, "")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	err := checkOrch(*addr)
	if err == nil {
		err = checkOTLP(*otlp)
	}
	if err != nil {
		return a.refused("orchestrator worker", err)
	}
	if !a.inCheckout("orchestrator worker", "gate/step.sh") {
		return exitUsage
	}
	if why := a.workerAtHead(ctx); why != "" {
		return a.refused("orchestrator worker", fmt.Errorf("%s", why))
	}
	r := role{name: "worker", args: append(a.repo(), "-commit", ownCommit()), set: orchEnv(*addr),
		clear: []string{"ORCH_ADDRESS", "ORCH_OTLP"}}
	traces := ""
	if *otlp != "" {
		r.args = append(r.args, "-otlp", *otlp)
		traces = ", workflow Gate traced to " + *otlp
	}
	first := fmt.Sprintf("laug · orchestrator worker: this checkout's worker on %s, its task queue named in "+
		"%s/queue; it replays the open gates on the queue, then polls%s; Ctrl-C stops it", *addr, orchRunDir, traces)
	return a.runRole(ctx, "orchestrator worker", r, a.Environ, first)
}

// ownCommit is the commit this laug was built from, buildCommit's, which the worker's identity names. Tests replace
// it.
var ownCommit = buildCommit

// workerAtHead says why the worker is refused, "" when this laug was built from HEAD's commit: the worker's identity,
// laug-worker@<commit>, names the commit laug was built from, and a gate on it refuses a worker whose commit is not
// HEAD's (internal/orchestrator's checkHolder, gate/step.sh's preflight).
func (a *App) workerAtHead(ctx context.Context) string {
	stamp := ownCommit()
	if stamp == "" || stamp == "dev" {
		return "this laug carries no commit, which the worker's identity names: make build stamps bin/laug with HEAD's"
	}
	head, err := a.gitHead(ctx)
	if err != nil {
		return fmt.Sprintf("%v, so laug's commit, which the worker's identity names, cannot be held to HEAD", err)
	}
	if sha, _ := strings.CutSuffix(stamp, "-dirty"); sha != head {
		return fmt.Sprintf("this laug was built from %s, and HEAD is %s: make build builds bin/laug from HEAD",
			short(stamp), short(head))
	}
	return ""
}

// ssPidRE is the first pid ss -p names.
var ssPidRE = regexp.MustCompile(`pid=([0-9]+)`)

// serverPid is the pid that listens on port: ss -ltnpH 'sport = :<port>', the first pid= it names; 0, which samples no
// server, when there is none, with why. ss names another user's process to root alone.
func (a *App) serverPid(ctx context.Context, port string) (int, string) {
	ss := a.lookPath("ss")
	if ss == "" {
		return 0, "no ss on PATH to name it"
	}
	out, err := a.probe(ctx, ss, "-ltnpH", "sport = :"+port)
	if m := ssPidRE.FindStringSubmatch(out); m != nil {
		if pid, perr := strconv.Atoi(m[1]); perr == nil {
			return pid, ""
		}
	}
	if err != nil {
		return 0, "ss: " + a.errShown(err)
	}
	return 0, "ss names no process on port " + port
}

// clockTicks is getconf CLK_TCK, the ticks a second /proc/<pid>/stat counts in.
func (a *App) clockTicks(ctx context.Context) (string, error) {
	getconf := a.lookPath("getconf")
	if getconf == "" {
		return "", fmt.Errorf("no getconf on PATH to read CLK_TCK with, which the probe's CPU samples need")
	}
	out, err := a.probe(ctx, getconf, "CLK_TCK")
	tck := strings.TrimSpace(out)
	if n, nerr := strconv.Atoi(tck); err != nil || nerr != nil || n <= 0 {
		return "", fmt.Errorf("getconf CLK_TCK printed %q (%v), not ticks a second", tck, err)
	}
	return tck, nil
}

func runOrchestratorProbe(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("orchestrator probe", "[-idle 60s] [-unpolled] [-orchestrator "+orchAddressDefault+"]")
	idle := f.Duration("idle", probeIdleDefault*time.Second, "how long workflow Idle runs, heartbeating every 2 s, in "+
		"whole seconds; 0 skips it")
	unpolled := f.Bool("unpolled", false, "send one HostStamp to <queue>-unpolled too, which no worker polls")
	addr := orchFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	secs, err := seconds("-idle", *idle)
	if err == nil {
		err = checkOrch(*addr)
	}
	if err != nil {
		return a.refused("orchestrator probe", err)
	}
	if !a.inCheckout("orchestrator probe", "the Probe workflow on this checkout's task queue") {
		return exitUsage
	}
	// The server's pid and the clock ticks are read before the probe starts.
	_, port, _ := net.SplitHostPort(*addr)
	pid, why := a.serverPid(ctx, port)
	tck, err := a.clockTicks(ctx)
	if err != nil {
		return a.refused("orchestrator probe", err)
	}
	r := role{name: "probe", args: append(a.repo(), "-seconds", secs), set: orchEnv(*addr),
		clear: []string{"ORCH_ADDRESS", "SECONDS", "UNPOLLED"}}
	if *unpolled {
		r.args = append(r.args, "-unpolled")
	}
	r.args = append(r.args, "-server-pid", strconv.Itoa(pid), "-clk-tck", tck)
	server := fmt.Sprintf("the server, pid %d,", pid)
	if pid == 0 {
		server = "no server (" + why + ")"
	}
	first := fmt.Sprintf("laug · orchestrator probe: this checkout's worker through %s: host time against history "+
		"time, Idle %s s heartbeating every 2 s, %s and the worker sampled every 5 s; Ctrl-C cancels it", *addr, secs,
		server)
	return a.runRole(ctx, "orchestrator probe", r, a.Environ, first)
}

func runOrchestratorRecord(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("orchestrator record", "<gate id> <name> -overwrite-fixture [-orchestrator "+orchAddressDefault+"]")
	overwrite := f.Bool("overwrite-fixture", false, "write workflow gate-<gate id>'s history over "+orchFixtureDir+
		"/<name>.json, a committed replay fixture")
	addr := orchFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) == 0 {
		return a.fixtures(f)
	}
	if len(pos) != 2 {
		return a.usageError(f, "takes a gate id and a fixture's name, got %q", pos)
	}
	gid, name := pos[0], pos[1]
	err := checkGateID(gid)
	if err == nil && !slices.Contains(orchFixtures, name) {
		err = fmt.Errorf("%s: not a fixture's name: one of %s", shellWord(name), strings.Join(orchFixtures, ", "))
	}
	if err == nil {
		err = checkOrch(*addr)
	}
	if err != nil {
		return a.refused("orchestrator record", err)
	}
	out := orchFixtureDir + "/" + name + ".json"
	if !a.inCheckout("orchestrator record", "the recorder into "+out) {
		return exitUsage
	}
	held := fixtureHolds(a.abs(out))
	if !*overwrite {
		fmt.Fprintf(a.Stderr, "laug · orchestrator record: would replace %s, a committed replay fixture (%s), with "+
			"workflow gate-%s's history from %s; -overwrite-fixture records it:\n  %s\n", out, held, gid, *addr,
			orchLine(a.self()+" orchestrator record "+gid+" "+name+" -overwrite-fixture", *addr))
		return exitUsage
	}
	// -out is relative to -repo, where the role resolves it.
	rargs := append(a.repo(), "-gate", gid, "-out", out)
	if l := a.getenv(denylistEnv); l != "" {
		rargs = append(rargs, "-denylist", l)
	}
	r := role{name: "record", args: rargs, set: orchEnv(*addr), clear: []string{"ORCH_ADDRESS", "GATE"}}
	// NAME stays out of clear, so a stray copy gets no line: some hosts set NAME to the machine's name in every shell,
	// and the line would print it on each record. Nothing laug runs reads NAME.
	first := fmt.Sprintf("laug · orchestrator record: workflow gate-%s's history from %s, scrubbed and checked for "+
		"leaks, over %s (%s); a leak writes nothing", gid, *addr, out, held)
	return a.runRole(ctx, "orchestrator record", r, a.Environ, first)
}

// fixtures is orchestrator record bare: each fixture with what it holds, the line to type, and its synopsis, on stderr,
// exit 2.
func (a *App) fixtures(f *flags) int {
	fmt.Fprintf(a.Stderr, "laug · orchestrator record: names no gate and no fixture; it replaces one of %d committed "+
		"replay fixtures in %s:\n", len(orchFixtures), orchFixtureDir)
	for _, n := range orchFixtures {
		held := "not there"
		if a.Root != "" {
			held = fixtureHolds(filepath.Join(a.Root, orchFixtureDir, n+".json"))
		}
		fmt.Fprintf(a.Stderr, "  %-10s %s\n", n, held)
	}
	fmt.Fprintf(a.Stderr, "a finished Gate's history replaces one:\n  %s orchestrator record <gate id> <name> "+
		"-overwrite-fixture\n", a.self())
	f.mistake(a.Stderr)
	return exitUsage
}
