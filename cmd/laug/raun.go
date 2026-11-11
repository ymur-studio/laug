package main

// laug raun: one trial. It leases a cell, runs one fault through it under load with faults/run.sh as a child, and gives
// the cell back however the fault ends, a signal included.

import (
	"context"
	"fmt"
	"time"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

func runRaun(ctx context.Context, a *App, args []string) int {
	var o faultOpts
	f := a.newFlags("raun", "<fault> [-pool <address>] [-ttl 30m] [-requester laug] [-server-tag <x.y.z>] "+
		"[-scenario <name>] [-run-id <id>] [-quiet-wait 300s] [-thresholds <file>] [-rate 20] [-max-concurrent 50] "+
		"[-sweep-margin 30s] [-dry-run]")
	o.register(f)
	ttl := f.Duration("ttl", 30*time.Minute, "how long the lease lasts, in whole seconds; the pool's max lease cuts it")
	requester := f.String("requester", "laug", "the `name` the pool's quota counts the lease under")
	tag := f.String("server-tag", "", "the server `tag` the cell runs, a release, x.y.z; empty takes any warm cell")
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
	runID = "raun-" + runID
	c, err := o.command(f, fault, runID)
	req, lerr := leaseRequest(*ttl, *requester, *tag)
	if err == nil {
		err = lerr
	}
	if err != nil {
		return a.refused("raun", err)
	}
	if !a.inCheckout("raun", c.Script) {
		return exitUsage
	}
	// The name is checked before the lease, so a typo leases nothing.
	if err := faultChoice.check(a, fault); err != nil {
		return a.refused("raun", err)
	}
	c, env, code, ok := a.prepare("raun", c, *o.dry)
	if !ok {
		return code
	}
	if *o.dry {
		fmt.Fprintln(a.Stdout, a.dryLine(c))
		return exitDone
	}

	client, _ := poolapi.New(o.pool) // poolURL passed it
	l, doc, err := a.leaseSaying(ctx, "raun", o.pool, client, req)
	if err != nil {
		return poolFailedAs(ctx, a, "raun", "lease", o.pool, "", "", err)
	}
	path := lastLeasePath(a, poolapi.LastLeaseFile)
	tail := "" // the raun JSON's path, once the script wrote it
	giveBack := func() bool {
		return a.giveBack(ctx, "raun", client, o.pool, l.ID, path, poolapi.LastLeaseFile, tail)
	}
	// faults/run.sh reads the cell from the last-lease file, so the lease is kept there first.
	if err := poolapi.WriteLast(path, doc); err != nil {
		fmt.Fprintf(a.Stderr, "laug · raun: leased %s but could not keep it in %s, where faults/run.sh reads it: %v\n",
			l.ID, poolapi.LastLeaseFile, a.errShown(err))
		giveBack()
		return exitFailed
	}
	length := ""
	if windows, budget, ok := a.raunLength(fault); ok {
		length = fmt.Sprintf("; its windows take %d s, plus up to %d s to recover", windows, budget)
	}
	fmt.Fprintf(a.Stdout, "laug · raun: %s on lease %s (%s) until %s, run %s%s; Ctrl-C stops the fault, then the "+
		"cell goes back\n", fault, l.ID, l.Address, utc(l.ExpiresAt), runID, length)
	if ctx.Err() != nil {
		fmt.Fprintf(a.Stderr, "laug · raun: stopped before the fault started\n")
		giveBack()
		return exitFailed
	}
	// A signal ends ctx. From a terminal, Ctrl-C reaches faults/run.sh too, which removes its fault and stops; one sent
	// to laug alone is not passed on (internal/script, Run), and the fault runs to its end. Either way the cell goes
	// back once the script returns. A second signal ends laug at once, and the pool reaps the lease at its expiry.
	said := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(said)
		fmt.Fprintf(a.Stderr, "laug · raun: stopping: %s goes back once faults/run.sh ends; a second signal ends laug, "+
			"and the pool reaps the lease at %s\n", l.ID, utc(l.ExpiresAt))
	})
	began := time.Now().Add(-time.Second) // a file system's clock may round a write's time down
	code = a.start("raun", c, env, script.Run, "", a.Stdout)
	if !stop() && ctx.Err() != nil {
		<-said // the line was under way when the script ended; it comes before the release's
	}
	if json := a.raunJSON(runID, began); json != "" {
		tail = "; the raun's JSON is " + json
	}
	// faults/run.sh's own codes are 0 ADVANCE, 1 BLOCK and 2 a harness failure.
	code = a.childExit("raun", "a harness failure", c.Script, code, exitDone, exitFailed, exitUsage)
	if !giveBack() && code == exitDone {
		return exitFailed
	}
	return code
}
