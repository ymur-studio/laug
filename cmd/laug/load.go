package main

// laug load: an Omes scenario on a cell, load/run-omes.sh as a child, with a line a minute while it runs. It needs a
// length, -duration or -iterations: bare, it starts nothing and names the cell and the line to type.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// loadOpts is load's flags. Each but -dry-run goes on load/run-omes.sh's line: the length, rate and concurrency, the
// cell, run, scenario and namespace, and whether Omes goes on past a failure.
type loadOpts struct {
	duration, address, prom, project, serverTag, namespace, scenario, runID string
	iterations, rate, maxConcurrent                                         int
	continueOnFailure, dry                                                  bool
}

func runLoad(ctx context.Context, a *App, args []string) int {
	var o loadOpts
	f := a.newFlags("load", "-duration <length> | -iterations <n> [-rate <n>] [-max-concurrent <n>] [-scenario <name>] "+
		"[-run-id <id>] [-address <host:port> -prom <url>] [-project <name>] [-server-tag <x.y.z>] [-namespace <name>] "+
		"[-continue-on-failure] [-dry-run]")
	f.StringVar(&o.duration, "duration", "", "how long the load runs, a `length` in whole h, m and s, 5m say; this or "+
		"-iterations is required")
	f.IntVar(&o.iterations, "iterations", 0, "how many workflows the load runs; this or -duration is required")
	f.IntVar(&o.rate, "rate", 0, "workflows started a second at most; unset, as fast as the scenario goes")
	f.IntVar(&o.maxConcurrent, "max-concurrent", 0, "workflows in flight at most; unset, Omes's own")
	f.BoolVar(&o.continueOnFailure, "continue-on-failure", false, "keep going past a failed iteration; Omes stops on "+
		"the first by default")
	f.StringVar(&o.scenario, "scenario", scenarioDefault, "the Omes `scenario`")
	f.StringVar(&o.runID, "run-id", "", "the run's `id`, its results' name; unset, the UTC time, yyyymmdd-hhmmss")
	f.StringVar(&o.address, "address", "", "the cell's frontend, `host:port`; unset, the leased cell's, else "+
		cellAddressDefault)
	f.StringVar(&o.prom, "prom", "", "the cell's Prometheus `url`, which the run records; unset, the leased cell's, "+
		"else "+promDefault+"; given with -address when a lease is kept")
	f.StringVar(&o.project, "project", "", "the cell's compose `project`; unset, the leased cell's, else "+
		projectDefault)
	f.StringVar(&o.serverTag, "server-tag", "", "the server `tag` the cell runs, x.y.z, which the run records; unset, "+
		"the lease's for the leased cell, else read from the project's temporal-history image")
	f.StringVar(&o.namespace, "namespace", namespaceDefault, "the Temporal `namespace`")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	o.dry = *dry
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	if o.runID == "" {
		o.runID = a.runIDNow()
	}
	var err error
	switch {
	case o.duration != "" && given(f, "iterations"):
		return a.usageError(f, "takes -duration or -iterations, not both: load/run-omes.sh runs -duration and "+
			"drops -iterations")
	case o.duration != "":
		err = checkLength("-duration", o.duration)
	case given(f, "iterations"):
		err = checkCount("-iterations", o.iterations)
	}
	for _, n := range []struct {
		name  string
		value int
	}{{"rate", o.rate}, {"max-concurrent", o.maxConcurrent}} {
		if err == nil && given(f, n.name) {
			err = checkCount("-"+n.name, n.value)
		}
	}
	if err == nil {
		err = checkWords("-run-id", o.runID, "-scenario", o.scenario, "-namespace", o.namespace)
	}
	if err == nil && given(f, "project") {
		err = checkWords("-project", o.project)
	}
	if err == nil && o.serverTag != "" && !poolapi.TagRE.MatchString(o.serverTag) {
		err = fmt.Errorf("-server-tag %s: not a release tag, x.y.z", shellWord(o.serverTag))
	}
	if err != nil {
		return a.refused("load", err)
	}

	// The cell: what was given, else the last lease's, else port block 0's; the tag is the lease's for a load to
	// the leased cell, else the one the project's temporal-history container runs.
	l := a.lastLease()
	if given(f, "address") && !given(f, "prom") && l.Prometheus != "" {
		return a.refused("load", fmt.Errorf("-address was given and -prom was not, so the run would record the "+
			"leased cell's Prometheus, %s; give -prom for %s", l.Prometheus, shellWord(o.address)))
	}
	address := pick(given(f, "address"), o.address, l.Address, cellAddressDefault)
	prom := pick(given(f, "prom"), o.prom, l.Prometheus, promDefault)
	project := pick(given(f, "project"), o.project, l.Project, projectDefault)
	from := "the local cell, port block 0"
	switch {
	case given(f, "address"):
		from = "-address"
	case l.Address != "":
		from = "lease " + l.ID
	}
	if o.duration == "" && !given(f, "iterations") {
		if from == "the local cell, port block 0" {
			from += ": " + poolapi.LastLeaseFile + " holds no lease"
		}
		return a.noLength(f, args, address, from)
	}
	if !a.inCheckout("load", "load/run-omes.sh") {
		return exitUsage
	}
	tag := o.serverTag
	if !given(f, "server-tag") {
		if l.Address != "" && l.Address == address {
			tag = l.ServerTag
			if tag == "" {
				tag = serverTagDefault // a lease with no server_tag, a microVM pool's among them: the default's cell
			}
		} else {
			tag = a.localServerTag(ctx, project)
		}
	}

	c := script.Command{Script: "load/run-omes.sh", Args: []script.Value{{Text: o.scenario}},
		Set: []script.Var{script.Quoted("OMES_DIR", omesDir), script.Quoted("CELL_ADDRESS", address),
			script.Quoted("PROM_URL", prom), script.Bare("PROJECT", project), script.Bare("NAMESPACE", o.namespace),
			script.Bare("RUN_ID", o.runID), script.Bare("SERVER_TAG", tag)}}
	length := strconv.Itoa(o.iterations) + " iterations"
	if o.duration != "" {
		length = "for " + o.duration
	}
	for _, k := range []struct {
		name, value string
		set         bool
	}{
		{"DURATION", o.duration, o.duration != ""},
		{"ITERATIONS", strconv.Itoa(o.iterations), given(f, "iterations")},
		{"RATE", strconv.Itoa(o.rate), given(f, "rate")},
		{"MAX_CONCURRENT", strconv.Itoa(o.maxConcurrent), given(f, "max-concurrent")},
		{"CONTINUE_ON_FAILURE", "1", o.continueOnFailure},
	} {
		if k.set {
			c.Pass = append(c.Pass, script.Bare(k.name, k.value))
		} else {
			c.Clear = append(c.Clear, k.name)
		}
	}
	first := fmt.Sprintf("laug · load: %s on %s (%s), %s, run %s; Ctrl-C stops it", o.scenario, address, from, length,
		o.runID)
	c, env, code, ok := a.prepare("load", c, o.dry)
	if !ok {
		return code
	}
	if o.dry {
		fmt.Fprintln(a.Stdout, a.dryLine(c))
		return exitDone
	}
	// The script stays a child, so laug can say a line a minute while Omes is silent. Ctrl-C reaches the script from
	// the terminal, and a SIGTERM sent to laug alone, laug passes on (Stop); its exit, a tool's own among them, is
	// load's.
	c.Stop = ctx
	stop := a.ticking(func(gone time.Duration) string { return loadSaid(gone, o.duration, o.iterations) })
	code = a.start("load", c, env, script.Run, first, a.Stdout)
	stop()
	return code
}

// loadTick is how often load says it still runs. Tests shorten it.
var loadTick = time.Minute

// omesSlackS is the time load/run-omes.sh gives Omes past -duration, its --timeout, for the workflows in flight.
const omesSlackS = 120

// loadSaid is the line load prints once gone has passed since it started: the seconds, and of what, -duration's
// seconds or -iterations; past -duration, that Omes waits for the workflows in flight, omesSlackS at most.
func loadSaid(gone time.Duration, duration string, iterations int) string {
	s := int(gone / time.Second)
	if duration == "" {
		return fmt.Sprintf("laug · load: %d s, toward %d iterations; Ctrl-C stops it", s, iterations)
	}
	d, _ := time.ParseDuration(duration) // checkLength passed it
	if gone <= d {
		return fmt.Sprintf("laug · load: %d s of %d s; Ctrl-C stops it", s, int(d/time.Second))
	}
	return fmt.Sprintf("laug · load: %d s, past its %d s: Omes waits up to %d s for the workflows in flight; Ctrl-C "+
		"stops it", s, int(d/time.Second), omesSlackS)
}

// ticking prints say's line on stderr every loadTick, from now until the stop it returns is called. stop waits for a
// line under way, so nothing is written after it returns.
func (a *App) ticking(say func(gone time.Duration) string) (stop func()) {
	done, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		t := time.NewTicker(loadTick)
		defer t.Stop()
		for n := 1; ; n++ {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintln(a.Stderr, say(time.Duration(n)*loadTick))
			}
		}
	}()
	return func() {
		close(done)
		<-ended
	}
}

// noLength says on stderr that load names no length, the cell the load would go to and where that cell came from,
// and the line to type, the arguments given after -duration 5m; then its synopsis. It returns 2.
func (a *App) noLength(f *flags, args []string, address, from string) int {
	fmt.Fprintf(a.Stderr, "laug · load: names no length: give it -duration, in whole h, m and s, or -iterations\n")
	line := []string{a.self(), "load", "-duration", "5m"}
	for _, arg := range args {
		line = append(line, shellWord(arg))
	}
	fmt.Fprintf(a.Stderr, "the load would go to %s (%s); for 5 minutes there:\n  %s\n", address, from,
		strings.Join(line, " "))
	f.mistake(a.Stderr)
	return exitUsage
}

// pick is a cell setting: the value given, else the last lease's when it has one, else the default.
func pick(isGiven bool, value, leased, def string) string {
	switch {
	case isGiven:
		return value
	case leased != "":
		return leased
	}
	return def
}
