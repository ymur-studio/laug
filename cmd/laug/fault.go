package main

// laug fault: one fault through three windows on the leased cell, faults/run.sh by exec. Beside runFault, what raun
// shares with it: the flags faults/run.sh's knobs come from, and the Command they build.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// faultOpts is what fault and raun take for faults/run.sh: the scenario its load runs, the run id and the pool it waits
// on before the load (RUN_ID and POOL_ADDR, always set), and the script's other knobs, which go in Pass. Each of those
// reaches the script only when its flag is given, so the script's own default applies otherwise; the flags' defaults
// are the script's, which a test holds equal.
type faultOpts struct {
	scenario, runID, pool, thresholds string
	quietWait, sweepMargin            time.Duration
	rate, maxConcurrent               int
	dry                               *bool
}

func (o *faultOpts) register(f *flags) {
	f.StringVar(&o.scenario, "scenario", scenarioDefault, "the Omes `scenario` the load runs")
	f.StringVar(&o.runID, "run-id", "", "the run's `id`, its results' name; unset, the UTC time, yyyymmdd-hhmmss")
	f.StringVar(&o.pool, "pool", poolDefault, "the pool's `address`: a loopback host:port, or an http:// URL")
	f.DurationVar(&o.quietWait, "quiet-wait", 300*time.Second, "how long the load waits, at most, for the pool's "+
		"other cells to finish building, whole seconds; 0s does not wait")
	f.StringVar(&o.thresholds, "thresholds", "", "a threshold set `file` to judge by, in place of the cell's "+
		"release's; a relative path starts at the checkout's root")
	f.IntVar(&o.rate, "rate", 20, "workflows the load starts a second")
	f.IntVar(&o.maxConcurrent, "max-concurrent", 50, "workflows the load keeps in flight at most")
	f.DurationVar(&o.sweepMargin, "sweep-margin", 30*time.Second, "the sweep counts workflows started this long "+
		"before the after window's end, whole seconds")
	o.dry = dryRunFlag(f)
}

// command is faults/run.sh's Command for fault under run id runID, or an error that names the flag it refuses.
func (o *faultOpts) command(f *flags, fault, runID string) (script.Command, error) {
	pool, err := poolURL(o.pool)
	if err == nil {
		err = checkWords("-run-id", runID, "-scenario", o.scenario, "fault", fault)
	}
	c := script.Command{Script: "faults/run.sh", Args: []script.Value{{Text: o.scenario}, {Text: fault}},
		Set: []script.Var{script.Bare("RUN_ID", runID), script.Quoted("POOL_ADDR", pool)}}
	knobs := []struct {
		flag, name string
		value      func() (string, error)
	}{
		{"rate", "RATE", func() (string, error) { return strconv.Itoa(o.rate), checkCount("-rate", o.rate) }},
		{"max-concurrent", "MAX_CONCURRENT", func() (string, error) {
			return strconv.Itoa(o.maxConcurrent), checkCount("-max-concurrent", o.maxConcurrent)
		}},
		{"quiet-wait", "POOL_QUIET_WAIT_S", func() (string, error) { return seconds("-quiet-wait", o.quietWait) }},
		{"thresholds", "THRESHOLDS", func() (string, error) { return o.thresholds, nil }},
		{"sweep-margin", "SWEEP_MARGIN_S", func() (string, error) { return seconds("-sweep-margin", o.sweepMargin) }},
	}
	for _, k := range knobs {
		if !given(f, k.flag) {
			c.Clear = append(c.Clear, k.name)
			continue
		}
		v, verr := k.value()
		if err == nil {
			err = verr
		}
		c.Pass = append(c.Pass, script.Bare(k.name, v))
	}
	return c, err
}

func runFault(_ context.Context, a *App, args []string) int {
	var o faultOpts
	f := a.newFlags("fault", "<fault> [-scenario <name>] [-run-id <id>] [-pool <address>] [-quiet-wait 300s] "+
		"[-thresholds <file>] [-rate 20] [-max-concurrent 50] [-sweep-margin 30s] [-dry-run]")
	o.register(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.noChoice(f, faultChoice)
	case len(pos) > 1:
		return a.usageError(f, "takes one fault, got %q", pos)
	}
	fault, runID := pos[0], o.runID
	if runID == "" {
		runID = a.runIDNow()
	}
	c, err := o.command(f, fault, runID)
	if err != nil {
		return a.refused("fault", err)
	}
	if !a.inCheckout("fault", c.Script) {
		return exitUsage
	}
	if err := faultChoice.check(a, fault); err != nil {
		return a.refused("fault", err)
	}
	first := ""
	if !*o.dry {
		l := a.lastLease()
		if l.ID == "" {
			return a.refused("fault", fmt.Errorf("runs on the leased cell, and %s holds no lease: %s lease leases "+
				"one, or %s raun %s leases, runs the fault and gives the cell back", poolapi.LastLeaseFile, a.self(),
				a.self(), fault))
		}
		first = fmt.Sprintf("laug · fault: %s on lease %s (%s), run %s; Ctrl-C removes the fault and stops it", fault,
			l.ID, l.Address, runID)
	}
	return a.runScript("fault", c, *o.dry, script.Exec, first)
}
