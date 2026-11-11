package main

// laug health: the HealthCheck workflow on a cell, whose round trip cells/health.sh and the compose and Firecracker
// backends run as bin/laug health check. check, loop, record and timer run internal/health's roles, CheckMain and
// TimerMain, in laug's own process; replay runs go test in internal/health through a filter. This file and
// orchestrator.go are the only ones here that import a package linking the Temporal SDK.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ymur-studio/laug/internal/health"
	"github.com/ymur-studio/laug/internal/script"
)

// healthRoles are internal/health's roles, by name.
var healthRoles = map[string]roleMain{"healthcheck": health.CheckMain, "timerprobe": health.TimerMain}

// healthDir is the package the replay tests are in, under the checkout's root, where replay runs go test.
const healthDir = "internal/health"

// healthFixture is the replay fixture health record writes over, relative to healthDir.
const healthFixture = "testdata/healthcheck_history.json"

// replayFilter is the grep -E pattern on replay's -dry-run line, and replay applies it itself. It matches the test
// results, and the lines that say why a build failed, go: (the toolchain), # (a package) and file:line (a compile
// error). On ASCII, Go's regexp reads [[:space:]] as grep -E does; its \s leaves out \v, which grep -E's \s matches
// (regexp/syntax's doc).
const replayFilter = `^(---|ok|FAIL|go: |# |\./[^[:space:]]+\.go:[0-9]+|\s+replay_test)`

var replayRE = regexp.MustCompile(replayFilter)

// healthFlags adds -address and -namespace, the cell HealthCheck runs on: port block 0's frontend unless given, never
// the last lease's.
func healthFlags(f *flags) (address, namespace *string) {
	address = f.String("address", cellAddressDefault, "the cell's frontend, `host:port`")
	namespace = f.String("namespace", namespaceDefault, "the `namespace` HealthCheck runs in")
	return address, namespace
}

// healthcheck is the healthcheck role against address in namespace, with more of its flags after. It reads no
// environment: CELL_ADDRESS and NAMESPACE are cleared, so a stray copy gets its line.
func healthcheck(address, namespace string, more ...string) role {
	return role{name: "healthcheck", args: append([]string{"-address", address, "-namespace", namespace}, more...),
		clear: []string{"CELL_ADDRESS", "NAMESPACE"}}
}

func runHealthCheck(ctx context.Context, a *App, args []string) int {
	wait := health.VisibilityWait.String()
	f := a.newFlags("health check", "[-address "+cellAddressDefault+"] [-namespace "+namespaceDefault+
		"] [-visibility-wait "+wait+"]")
	address, namespace := healthFlags(f)
	visibility := f.String("visibility-wait", wait, "how long the run may take to show in visibility, a Go "+
		"`duration` over 0")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -address names the cell", pos)
	}
	err := checkWords("-namespace", *namespace)
	if d, perr := time.ParseDuration(*visibility); err == nil && (perr != nil || d <= 0) {
		err = fmt.Errorf("-visibility-wait %s: a Go duration over 0, 10s say", shellWord(*visibility))
	}
	if err != nil {
		return a.refused("health check", err)
	}
	// -metrics "" serves no SDK metrics. -visibility-wait reaches the role only when given, so the role's own default
	// stands otherwise: the compose and Firecracker backends give 10s.
	more := []string{"-metrics", ""}
	if given(f, "visibility-wait") {
		more = append(more, "-visibility-wait", *visibility)
	}
	first := fmt.Sprintf("laug · health check: one HealthCheck workflow on %s, namespace %s, its round trip and "+
		"visibility, its worker in this process", shellWord(*address), *namespace)
	return a.runRole(ctx, "health check", healthcheck(*address, *namespace, more...), a.Environ, first)
}

func runHealthLoop(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("health loop", "[-address "+cellAddressDefault+"] [-namespace "+namespaceDefault+"] [-every "+
		healthEveryDefault+"]")
	address, namespace := healthFlags(f)
	every := f.String("every", healthEveryDefault, "how often the round trip runs, a Go `duration` over 0")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -address names the cell", pos)
	}
	err := checkWords("-namespace", *namespace)
	if d, perr := time.ParseDuration(*every); err == nil && (perr != nil || d <= 0) {
		err = fmt.Errorf("-every %s: a Go duration over 0, 10s say", shellWord(*every))
	}
	if err != nil {
		return a.refused("health loop", err)
	}
	first := fmt.Sprintf("laug · health loop: HealthCheck every %s on %s, namespace %s, its SDK metrics on :9091 for "+
		"the cell's sdk-workers target; Ctrl-C stops it", *every, shellWord(*address), *namespace)
	return a.runRole(ctx, "health loop", healthcheck(*address, *namespace, "-loop", *every), a.Environ, first)
}

func runHealthRecord(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("health record", "-overwrite-fixture [-address "+cellAddressDefault+"] [-namespace "+
		namespaceDefault+"]")
	address, namespace := healthFlags(f)
	fixture := filepath.Join(healthDir, healthFixture)
	overwrite := f.Bool("overwrite-fixture", false, "write one round trip's history over "+fixture+", the "+
		"committed replay fixture, then replay it")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -address names the cell", pos)
	}
	if err := checkWords("-namespace", *namespace); err != nil {
		return a.refused("health record", err)
	}
	if !a.inCheckout("health record", "go test in "+healthDir) {
		return exitUsage
	}
	held := fixtureHolds(a.abs(fixture))
	if !*overwrite {
		fmt.Fprintf(a.Stderr, "laug · health record: would replace %s, the committed replay fixture (%s), with the "+
			"history of one HealthCheck round trip on %s; -overwrite-fixture records it, then replays it:\n  %s\n",
			fixture, held, shellWord(*address), a.self()+" health record -overwrite-fixture"+addressArgs(f, *address,
				*namespace))
		return exitUsage
	}
	// The replay is checked before the round trip runs, so a refusal writes nothing.
	r, renv, code, ok := a.prepare("health record", replayCommand(""), false)
	if !ok {
		return code
	}
	// -metrics "" serves no SDK metrics; -export is the fixture's absolute path, since the role runs in laug's own
	// working directory.
	first := fmt.Sprintf("laug · health record: one HealthCheck round trip on %s, namespace %s, its history over %s "+
		"(%s), then the replay tests on it", shellWord(*address), *namespace, fixture, held)
	rec := healthcheck(*address, *namespace, "-metrics", "", "-export", a.abs(fixture))
	if code := a.runRole(ctx, "health record", rec, a.Environ, first); code != exitDone {
		return code
	}
	return a.replay("health record", r, renv, "")
}

// addressArgs is the -address and -namespace flags given, as a line to type repeats them.
func addressArgs(f *flags, address, namespace string) string {
	s := ""
	if given(f, "address") {
		s += " -address " + shellWord(address)
	}
	if given(f, "namespace") {
		s += " -namespace " + shellWord(namespace)
	}
	return s
}

// fixtureHolds says what the history file at path holds, for the line that says what would go: its bytes and its
// events, or that it is not there.
func fixtureHolds(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "not there"
	}
	var h struct {
		Events []json.RawMessage `json:"events"`
	}
	if json.Unmarshal(b, &h) != nil {
		return plural(len(b), "byte") + ", not a history"
	}
	return plural(len(b), "byte") + ", " + plural(len(h.Events), "event")
}

func runHealthTimer(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("health timer", "[<duration>] [-address <host:port>] [-namespace "+namespaceDefault+"] [-id <id>]")
	address := f.String("address", "", "the cell's frontend, `host:port`; unset, the leased cell's, else "+
		cellAddressDefault)
	namespace := f.String("namespace", namespaceDefault, "the `namespace` the timer runs in")
	id := f.String("id", "", "the workflow's `id`; unset, timerprobe's own, timer-probe-<Unix ms>")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 1 {
		return a.usageError(f, "takes one duration at most, got %q", pos)
	}
	timer := timerDefault
	if len(pos) == 1 {
		timer = pos[0]
	}
	err := checkWords("-namespace", *namespace)
	if d, perr := time.ParseDuration(timer); err == nil && (perr != nil || d <= 0) {
		err = fmt.Errorf("%s: the timer, a Go duration over 0, 60s say", shellWord(timer))
	}
	if err == nil && given(f, "id") {
		err = checkWords("-id", *id)
	}
	if err != nil {
		return a.refused("health timer", err)
	}
	// The cell: -address, else the last lease's, else port block 0's.
	l := a.lastLease()
	addr := pick(given(f, "address"), *address, l.Address, cellAddressDefault)
	from := "port block 0"
	switch {
	case given(f, "address"):
		from = "-address"
	case l.Address != "":
		from = "lease " + l.ID
	}
	r := role{name: "timerprobe", args: []string{"-address", addr, "-namespace", *namespace, "-timer", timer},
		clear: []string{"CELL_ADDRESS", "NAMESPACE", "TIMER"}}
	if given(f, "id") {
		r.args = append(r.args, "-id", *id)
	}
	first := fmt.Sprintf("laug · health timer: one %s durable timer on %s (%s), namespace %s; it prints when the "+
		"timer was due and when it fired, in this host's time; Ctrl-C stops it", timer, shellWord(addr), from,
		*namespace)
	return a.runRole(ctx, "health timer", r, a.Environ, first)
}

func runHealthReplay(_ context.Context, a *App, args []string) int {
	f := a.newFlags("health replay", "[<file>] [-dry-run]")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 1 {
		return a.usageError(f, "takes one history file at most, got %q", pos)
	}
	if !a.inCheckout("health replay", "go test in "+healthDir) {
		return exitUsage
	}
	file, on := "", "the committed fixture, "+filepath.Join(healthDir, healthFixture)
	if len(pos) == 1 {
		// A relative file starts at the checkout's root.
		file, on = filepath.Clean(a.abs(pos[0])), shellWord(pos[0])
		if fi, err := os.Stat(file); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			return a.refused("health replay", fmt.Errorf("%s is not a file with a history in it", shellWord(pos[0])))
		}
	}
	c, env, code, ok := a.prepare("health replay", replayCommand(file), *dry)
	if !ok {
		return code
	}
	if *dry {
		fmt.Fprintln(a.Stdout, replayLine(c))
		return exitDone
	}
	first := fmt.Sprintf("laug · health replay: the replay tests in %s on %s, by go test; its results and why a "+
		"build failed, the rest left out", healthDir, on)
	return a.replay("health replay", c, env, first)
}

// replayCommand is replay's go test, in internal/health: REPLAY_HISTORY the file, absolute, when one is named, else
// none, and the committed fixture replays.
func replayCommand(file string) script.Command {
	c := script.Command{Script: "go", Tool: true, Dir: healthDir, Args: []script.Value{{Text: "test"},
		{Text: "./..."}, {Text: "-run"}, {Text: "Replay"}, {Text: "-v"}, {Text: "-count=1"}},
		Clear: []string{"HISTORY"}}
	if file != "" {
		c.Set = []script.Var{script.Quoted("REPLAY_HISTORY", file)}
	} else {
		c.Clear = append(c.Clear, "REPLAY_HISTORY")
	}
	return c
}

// replayLine is replay's -dry-run line: go test's, then the filter.
func replayLine(c script.Command) string {
	return script.DryRun(c) + " 2>&1 | grep -E '" + replayFilter + "'"
}

// replay runs c, replayCommand's, as a child: its stdout and stderr together through replayFilter, as the -dry-run
// line's 2>&1 | grep -E does, onto laug's stdout, after first when it is not empty. It returns go test's exit, 0 when
// every replay passed.
func (a *App) replay(cmd string, c script.Command, env []string, first string) int {
	if first != "" {
		fmt.Fprintln(a.Stdout, first)
	}
	w := &lineFilter{w: a.Stdout, keep: replayRE}
	code, err := a.Runner.Run(c, env, w, w)
	w.Flush()
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · %s: could not run go test: %v\n", cmd, a.errShown(err))
		return exitFailed
	}
	return code
}

// lineFilter writes on to w each line keep matches, newline and all, as grep does. Flush writes a last line that has no
// newline, with one, when it matches.
type lineFilter struct {
	w    io.Writer
	keep *regexp.Regexp
	part []byte
}

func (l *lineFilter) Write(p []byte) (int, error) {
	l.part = append(l.part, p...)
	for {
		i := bytes.IndexByte(l.part, '\n')
		if i < 0 {
			break
		}
		line := l.part[:i+1]
		l.part = l.part[i+1:]
		if l.keep.Match(line[:i]) {
			if _, err := l.w.Write(line); err != nil {
				return len(p), err
			}
		}
	}
	l.part = append([]byte(nil), l.part...)
	return len(p), nil
}

// Flush writes the last line, when there is one and it matches.
func (l *lineFilter) Flush() {
	if len(l.part) > 0 && l.keep.Match(l.part) {
		l.w.Write(append(l.part, '\n'))
	}
	l.part = nil
}
