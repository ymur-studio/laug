package main

// The table, dispatch and main. A row is a command; a command holds one or more verbs, and a verb's class says
// what its bare form may do.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/script"
)

const (
	header   = "laug · ephemeral Temporal cells from a warm pool, for release validation"
	exitLine = "Exit 0 done or ADVANCE, 1 BLOCK or failed, 2 usage or refused, 3 short run."
)

// class says what a verb's bare invocation may do. main_test.go holds each class to its rule.
type class int

const (
	_             class = iota
	classRead           // changes nothing in the tree or the pool; bare, it exits 0
	classAct            // does the one thing its name says; its first line says what started and how to stop it
	classNeeds          // needs a subject; bare, it prints the choices and its synopsis, and exits 2
	classDestroys       // destroys; bare, it says what would go, with counts, and exits 2
)

// group is a heading in the help, which prints them in this order and skips one with no command.
type group int

const (
	_              group = iota
	groupDaily           // pool to raun
	groupJudging         // gate to sweep
	groupCells           // cell, host
	groupWorkflows       // health, orchestrator
	groupProject         // deps to help
)

var groupHeadings = [...]string{
	groupDaily:     "The daily path",
	groupJudging:   "Judging a release",
	groupCells:     "Cells and hosts",
	groupWorkflows: "Temporal workflows",
	groupProject:   "The project",
}

// verb is one thing a command does. run returns the exit code. A command with one verb leaves its name and summary
// empty, and the arguments after the command go straight to run; a named verb's summary, as a command's, is lowercase,
// a verb first, no full stop, and its -h's first line stays under 80 columns.
type verb struct {
	name    string
	class   class
	run     func(ctx context.Context, a *App, args []string) int
	summary string
}

// command is one row of the table.
type command struct {
	name    string
	group   group
	summary string // lowercase, a verb first, no full stop; its help line stays under 80 columns
	verbs   []verb
}

// one is the verbs of a command that does one thing: a verb with no name, whose summary is the command's.
func one(cl class, run func(ctx context.Context, a *App, args []string) int) []verb {
	return []verb{{"", cl, run, ""}}
}

// commands is the table, in the order help lists each group. init fills it, since help's run reads it.
var commands []command

func init() {
	commands = []command{
		{"pool", groupDaily, "keep cells warm on " + pool.DefaultListen + " and lease them out", []verb{
			{"serve", classAct, runPoolServe, "run the warm pool in the foreground"},
			{"status", classRead, runPoolStatus, "print the pool's settings and its cells"},
			{"down", classDestroys, runPoolDown, "stop every pool and remove its cells; bare, says what goes"},
			{"bench", classNeeds, runPoolBench, "time a running pool's warm-up, leases and releases"}}},
		{"lease", groupDaily, "lease a warm cell and keep it as the last lease", one(classAct, runLease)},
		{"reset", groupDaily, "rebuild a leased cell under the same lease: <id> or -last", one(classNeeds, runReset)},
		{"release", groupDaily, "give a leased cell back: <id> or -last", one(classNeeds, runRelease)},
		{"load", groupDaily, "run an Omes scenario on the leased cell: -duration, -rate", one(classNeeds, runLoad)},
		{"verdict", groupDaily, "judge one run by its release's thresholds: ADVANCE or BLOCK",
			one(classNeeds, runVerdict)},
		{"fault", groupDaily, "run one fault through three windows on the leased cell", one(classNeeds, runFault)},
		{"raun", groupDaily, "lease, fault, judge and release, in one command", one(classNeeds, runRaun)},
		{"gate", groupJudging, "judge a server release on fresh cells, and read its runs", []verb{
			{"run", classNeeds, runGateRun, "judge a server release beside the one it replaces"},
			{"post", classNeeds, runGatePost, "hand a run's verdict on, to -post"},
			{"rejudge", classNeeds, runGateRejudge, "judge a run again in Go, and compare the bytes"},
			{"compare", classNeeds, runGateCompare, "compare two runs' signatures, field by field"},
			{"signature", classNeeds, runGateSignature, "print the 16 fields of a run's signature"},
			{"tolerance", classNeeds, runGateTolerance, "compute the incumbent's tolerances from A/A gates"},
			{"open", classNeeds, runGateOpen, "print a run's verdict and where its files are"},
			{"status", classNeeds, runGateStatus, "read a workflow gate's status and progress"},
			{"cancel", classNeeds, runGateCancel, "ask a workflow gate to cancel, by its stop path"},
			{"terminate", classNeeds, runGateTerminate, "end a workflow gate at once, with a reason"},
			{"stop", classNeeds, runGateStop, "run a workflow gate's stop path by hand"},
			{"control", classNeeds, runGateControl, "run a change the gate must catch, on fakes"}}},
		{"judge", groupJudging, "judge a gate run's directory in Go: ADVANCE or BLOCK", one(classNeeds, runJudge)},
		{"thresholds", groupJudging, "print the thresholds a server release is judged by",
			one(classRead, runThresholds)},
		{"candidate", groupJudging, "judge one dynamic config change on a leased cell", one(classNeeds, runCandidate)},
		{"baseline", groupJudging, "read every threshold over one run's whole window", one(classNeeds, runBaseline)},
		{"sweep", groupJudging, "count one run's workflows by status on its cell", one(classNeeds, runSweep)},
		{"cell", groupCells, "run one compose cell on this host, and check it", []verb{
			{"up", classAct, runCellUp, "bring one cell up and wait until it serves"},
			{"down", classDestroys, runCellDown, "remove a cell and its data; bare, says what goes"},
			{"mem", classRead, runCellMem, "print the memory a cell's containers hold"},
			{"health", classRead, runCellHealth, "check a cell: cluster health and one HealthCheck"},
			{"dynconfig-check", classRead, runCellDynconfigCheck, "validate a cell's dynamic config"},
			{"fi-template-check", classRead, runCellFiTemplateCheck, "check laug-server.yaml against a release"},
			{"softasserts", classRead, runCellSoftasserts, "count the server's soft assertions per role"}}},
		{"host", groupCells, "check this host as a gate does: its type and clock", []verb{
			{"type", classRead, runHostType, "print this host's type as a gate checks it, or declare it"},
			{"clock", classRead, runHostClock, "sample the wall clock against the uptime clock"}}},
		{"health", groupWorkflows, "run HealthCheck, a Temporal workflow, on a cell", []verb{
			{"check", classRead, runHealthCheck, "run one HealthCheck workflow on a cell"},
			{"loop", classAct, runHealthLoop, "run HealthCheck every 10 s until Ctrl-C"},
			{"record", classDestroys, runHealthRecord, "record HealthCheck's history as the replay fixture"},
			{"timer", classAct, runHealthTimer, "arm one durable timer on a cell, and time it"},
			{"replay", classRead, runHealthReplay, "replay the recorded HealthCheck history"}}},
		{"orchestrator", groupWorkflows, "run the orchestrator: its own dev server and worker", []verb{
			{"server", classAct, runOrchestratorServer, "run the orchestrator's own dev server"},
			{"worker", classAct, runOrchestratorWorker, "run this checkout's worker for workflow Gate"},
			{"probe", classAct, runOrchestratorProbe, "probe this checkout's worker and the server"},
			{"record", classDestroys, runOrchestratorRecord, "record a finished Gate's history as a fixture"}}},
		{"deps", groupProject, "check every tool the scripts call, each with its version", one(classRead, runDeps)},
		{"upstream", groupProject, "clone Omes at the commit every result used", one(classAct, runUpstream)},
		{"version", groupProject, "print the commit, Go version, server tag and Omes commit",
			one(classRead, runVersion)},
		{"runbook", groupProject, "run the runbooks' offline blocks and diff their lines", []verb{
			{"check", classRead, runRunbookCheck, "run a runbook's offline blocks and diff their lines"}}},
		{"help", groupProject, "print this list, a command's verbs or a verb's flags", one(classRead, runHelp)},
	}
}

// startHere is the help's first step, offline, with no Docker: each line's command, as typed after the word a line
// starts with, and what it does.
var startHere = [...]struct{ line, does string }{
	{"deps", "check every tool the scripts call"},
	{"gate run " + serverTagDefault + " -fake", "run a whole gate on fakes: ADVANCE, exit 0"},
	{"runbook check", "list the runbooks and what each block needs"},
}

// words are the four the help uses before it lists anything: laug's own, Temporal's and Omes's. docs/LEARNING.md says
// more of each.
var words = [...]struct{ word, means string }{
	{"cell", "one complete, throwaway Temporal server with its own Postgres"},
	{"raun", "one trial: lease a cell, load it, inject a fault, read the verdict"},
	{"gate", "a server release judged beside the one it replaces, on fresh cells"},
	{"Omes", "Temporal's load generator, which puts the load on a cell"},
}

// main runs laug by the command its arguments name, whatever name it was started by: no other name runs another
// program.
func main() {
	ctx, stop := signalContext()
	code := run(ctx, newApp(), os.Args[1:])
	stop()
	os.Exit(code)
}

// signalContext is the verb's context. The first SIGINT or SIGTERM cancels it, its cause the signal
// (script.Signalled), so a script laug waits for can tell Ctrl-C from a SIGTERM sent to laug alone; after it the
// default action is back, so a second kills. stop stops catching them.
func signalContext() (ctx context.Context, stop func()) {
	ctx, cancel := context.WithCancelCause(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case s := <-sigs:
			signal.Stop(sigs)
			cancel(script.Signalled{Signal: s})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(sigs)
		cancel(nil)
	}
}

// run dispatches args to a command and returns the exit code: help asked for goes to stdout with 0, a mistake to
// stderr with 2. Bare, it prints the help; an unknown command gets three lines, which fit on one screen.
// --version and -version run version, as --help runs help.
func run(ctx context.Context, a *App, args []string) int {
	if len(args) == 0 {
		usage(a.Stderr, a.self())
		return exitUsage
	}
	switch args[0] {
	case "-h", "-help", "--help":
		usage(a.Stdout, a.self())
		return exitDone
	case "-version", "--version":
		return runVersion(ctx, a, args[1:])
	}
	if c, ok := lookup(args[0]); ok {
		return c.dispatch(ctx, a, args[1:])
	}
	return unknownCommand(a, "laug · ", args[0])
}

// unknownCommand says, after head, that name is no command, with the nearest commands; then the synopsis and the line
// that lists them. It returns 2.
func unknownCommand(a *App, head, name string) int {
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.name
	}
	fmt.Fprintf(a.Stderr, "%sunknown command %q%s\n", head, name, didYouMean("", name, names))
	fmt.Fprintf(a.Stderr, "usage: %s <command> [flags] [arguments]\n", a.self())
	fmt.Fprintf(a.Stderr, "%q lists the commands.\n", a.self()+" help")
	return exitUsage
}

func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// usage writes the help: the header, where to start, laug's words, the command list by group, and the exit codes.
// self is the word a line to type starts with, the App's.
func usage(w io.Writer, self string) {
	fmt.Fprintln(w, header)
	fmt.Fprintf(w, "usage: %s <command> [flags] [arguments]\n", self)
	fmt.Fprintf(w, "\nStart here, offline, with no Docker\n")
	width := 0
	for _, l := range startHere {
		width = max(width, len(self)+1+len(l.line))
	}
	for _, l := range startHere {
		fmt.Fprintf(w, "  %-*s  %s\n", width, self+" "+l.line, l.does)
	}
	fmt.Fprintf(w, "\nThe words\n")
	for _, l := range words {
		fmt.Fprintf(w, "  %s  %s\n", l.word, l.means)
	}
	fmt.Fprintln(w, "A raun and a gate each end ADVANCE or BLOCK.")
	for g := range groupHeadings {
		heading := true
		for _, c := range commands {
			if int(c.group) != g {
				continue
			}
			if heading {
				fmt.Fprintf(w, "\n%s\n", groupHeadings[g])
				heading = false
			}
			fmt.Fprintf(w, "  %-12s  %s\n", c.name, c.summary)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%q prints its verbs or its flags.\n", self+" help <command> [<verb>]")
	fmt.Fprintln(w, "Flags go before or after the arguments.")
	fmt.Fprintln(w, exitLine)
}

// dispatch runs the verb args names. A command whose one verb has no name passes it every argument. Otherwise -h
// prints the command's usage on stdout with 0, and no verb or an unknown one prints it on stderr with 2, an unknown
// one first with the nearest verbs.
func (c command) dispatch(ctx context.Context, a *App, args []string) int {
	if len(c.verbs) == 1 && c.verbs[0].name == "" {
		return c.verbs[0].run(ctx, a, args)
	}
	if len(args) > 0 {
		switch args[0] {
		case "-h", "-help", "--help":
			c.usage(a.Stdout, a.self())
			return exitDone
		}
		names := make([]string, len(c.verbs))
		for i, v := range c.verbs {
			if v.name == args[0] {
				return v.run(ctx, a, args[1:])
			}
			names[i] = v.name
		}
		fmt.Fprintf(a.Stderr, "laug · %s: unknown verb %q%s\n", c.name, args[0], didYouMean("", args[0], names))
	}
	c.usage(a.Stderr, a.self())
	return exitUsage
}

// usage writes a command's summary, synopsis and verbs, a line each with its summary, each line to type starting
// with self.
func (c command) usage(w io.Writer, self string) {
	width := 0
	for _, v := range c.verbs {
		width = max(width, len(v.name))
	}
	fmt.Fprintf(w, "laug · %s: %s\n", c.name, c.summary)
	fmt.Fprintf(w, "usage: %s %s <verb> [flags] [arguments]\n", self, c.name)
	for _, v := range c.verbs {
		fmt.Fprintf(w, "  %-*s  %s\n", width, v.name, v.summary)
	}
	fmt.Fprintf(w, "%q prints a verb's flags.\n", self+" "+c.name+" <verb> -h")
}

// summaryOf is the summary of name, a command and its verb as the user typed them ("lease", "gate run"): the named
// verb's, or for a command with one unnamed verb, the command's.
func summaryOf(name string) (string, bool) {
	cmd, vname, _ := strings.Cut(name, " ")
	c, ok := lookup(cmd)
	if !ok {
		return "", false
	}
	for _, v := range c.verbs {
		if v.name == vname {
			if v.name == "" {
				return c.summary, true
			}
			return v.summary, v.summary != ""
		}
	}
	return "", false
}
