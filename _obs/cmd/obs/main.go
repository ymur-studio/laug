package main

// The table, dispatch and main, after cmd/laug/main.go: the stack's verbs, each a command of its own, as bin/laug's obs
// command held them; the usage verbs, one command, as bin/laug's usage held them; and help. A verb's class says what
// its bare form may do, as in cmd/laug.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ymur-studio/laug/internal/script"
)

const (
	header   = "laug · obs: the observability stack, one place to receive what laug emits"
	exitLine = "Exit 0 done, 1 failed, 2 usage or refused."
)

// class says what a verb's bare invocation may do, cmd/laug's four. main_test.go holds each class to its rule.
type class int

const (
	_             class = iota
	classRead           // changes nothing; bare, it exits 0
	classAct            // does the one thing its name says; its first line says what started and how to stop it
	classNeeds          // needs a subject; bare, it prints the choices and its synopsis, and exits 2
	classDestroys       // destroys; bare, it says what would go, with counts, and exits 2
)

// group is a heading in the help, which prints them in this order.
type group int

const (
	_          group = iota
	groupStack       // up to purge
	groupUsage       // usage
	groupHelp        // help
)

var groupHeadings = [...]string{
	groupStack: "The stack, on 127.0.0.1 in 7710 to 7769",
	groupUsage: "Cell usage, from cgroups",
	groupHelp:  "This binary",
}

// verb is one thing a command does. run returns the exit code. A command with one verb leaves its name and summary
// empty, and the arguments after the command go straight to run; a named verb's summary, as a command's, is lowercase,
// a verb first, no full stop.
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

// commands is the table, in the order help lists each group. init fills it, since help's run reads it. The summaries
// are bin/laug's obs and usage verbs'.
var commands []command

func init() {
	commands = []command{
		{"up", groupStack, "start the observability stack on 127.0.0.1", one(classAct, runObsUp)},
		{"down", groupStack, "stop the stack's parts; the data stays", one(classAct, runObsDown)},
		{"status", groupStack, "print each part of the stack, and the store", one(classRead, runObsStatus)},
		{"find", groupStack, "find what the stack holds of one gate or run", one(classRead, runObsFind)},
		{"backfill", groupStack, "put gate runs the store lacks into it", one(classAct, runObsBackfill)},
		{"follow", groupStack, "run follow, which writes the leased cells as targets", one(classAct, runObsFollow)},
		{"pack", groupStack, "pack the stopped stack's store and traces", one(classAct, runObsPack)},
		{"view", groupStack, "read a pack: the store and Grafana, no scrape", one(classNeeds, runObsView)},
		{"purge", groupStack, "remove the stack's state; bare, says what goes", one(classDestroys, runObsPurge)},
		{"usage", groupUsage, "record and sum cgroup samples of laug cells", []verb{
			{"record", classAct, runUsageRecord, "write cgroup samples of laug cells into a file"},
			{"sample", classRead, runUsageSample, "read one sample and write nothing"},
			{"stop", classNeeds, runUsageStop, "end a recording, with its last line"},
			{"report", classNeeds, runUsageReport, "sum a recording per item, service and process"}}},
		{"help", groupHelp, "print this list, a verb's flags or the usage verbs", one(classRead, runHelp)},
	}
}

// main runs the verb its arguments name, whatever name it was started by.
func main() {
	ctx, stop := signalContext()
	code := run(ctx, newApp(), os.Args[1:])
	stop()
	os.Exit(code)
}

// signalContext is the verb's context, as cmd/laug's: the first SIGINT or SIGTERM cancels it, its cause the signal
// (script.Signalled); after it the default action is back, so a second kills. stop stops catching them.
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
// stderr with 2. Bare, it prints the help on stderr; an unknown command gets three lines.
func run(ctx context.Context, a *App, args []string) int {
	if len(args) == 0 {
		usage(a.Stderr, a.self())
		return exitUsage
	}
	switch args[0] {
	case "-h", "-help", "--help":
		usage(a.Stdout, a.self())
		return exitDone
	}
	if c, ok := lookup(args[0]); ok {
		return c.dispatch(ctx, a, args[1:])
	}
	return unknownCommand(a, "laug · obs: ", args[0])
}

// unknownCommand says, after head, that name is no verb, with the nearest; then the synopsis and the line that lists
// them. It returns 2.
func unknownCommand(a *App, head, name string) int {
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.name
	}
	fmt.Fprintf(a.Stderr, "%sunknown verb %q%s\n", head, name, didYouMean("", name, names))
	fmt.Fprintf(a.Stderr, "usage: %s <verb> [flags] [arguments]\n", a.self())
	fmt.Fprintf(a.Stderr, "%q lists the verbs.\n", a.self()+" help")
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

// usage writes the help: the header, the verbs by group, and the exit codes. self is the word a line to type starts
// with, the App's.
func usage(w io.Writer, self string) {
	fmt.Fprintln(w, header)
	fmt.Fprintf(w, "usage: %s <verb> [flags] [arguments]\n", self)
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
	fmt.Fprintf(w, "%q prints its flags, %q the usage verbs.\n", self+" help <verb>", self+" help usage")
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

// summaryOf is the summary of name, a verb as bin/obs takes it ("up", "usage record"): the named verb's, or for a
// command with one unnamed verb, the command's.
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
