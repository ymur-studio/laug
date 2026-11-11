package main

// laug candidate: one dynamic config change judged on a leased cell. It leases the cell, then execs
// faults/candidate.sh, which gives the cell back itself on every exit, Ctrl-C and SIGTERM among them
// (CANDIDATE_RELEASE=1).

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

func runCandidate(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("candidate", "<name> [-duration 5m] [-rate 20] [-max-concurrent 50] [-scenario <name>] "+
		"[-namespace <name>] [-run-id <id>] [-pool <address>] [-ttl 30m] [-requester laug] [-server-tag <x.y.z>] "+
		"[-quiet-wait 300s] [-thresholds <file>] [-dry-run]")
	duration := f.String("duration", "5m", "the `length` of the load after the change, in whole h, m and s")
	rate := f.Int("rate", 20, "workflows the load starts a second")
	maxConcurrent := f.Int("max-concurrent", 50, "workflows the load keeps in flight at most")
	scenario := f.String("scenario", scenarioDefault, "the Omes `scenario` the load runs")
	namespace := f.String("namespace", namespaceDefault, "the Temporal `namespace`")
	runID := f.String("run-id", "", "the run's `id`, its results' name; unset, the UTC time, yyyymmdd-hhmmss")
	pool := f.String("pool", poolDefault, "the pool's `address`: a loopback host:port, or an http:// URL")
	ttl := f.Duration("ttl", 30*time.Minute, "how long the lease lasts, in whole seconds; the pool's max lease cuts it")
	requester := f.String("requester", "laug", "the `name` the pool's quota counts the lease under")
	tag := f.String("server-tag", "", "the server `tag` the cell runs, a release, x.y.z; empty takes any warm cell")
	quietWait := f.Duration("quiet-wait", 300*time.Second, "how long the before window waits, at most, for the "+
		"pool's other cells to finish building, whole seconds; 0s does not wait")
	thresholds := f.String("thresholds", "", "a threshold set `file` to judge by, in place of the cell's release's; "+
		"a relative path starts at the checkout's root")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.noChoice(f, candidateChoice)
	case len(pos) > 1:
		return a.usageError(f, "takes one candidate, got %q", pos)
	}
	name := pos[0]
	if *runID == "" {
		*runID = a.runIDNow()
	}
	poolAddr, err := poolURL(*pool)
	if err == nil {
		err = checkWords("-run-id", *runID, "-scenario", *scenario, "-namespace", *namespace, "candidate", name)
	}
	req, lerr := leaseRequest(*ttl, *requester, *tag)
	if err == nil {
		err = lerr
	}
	c := script.Command{Script: "faults/candidate.sh",
		Args: []script.Value{{Text: name}, {Text: poolapi.LastLeaseFile}},
		Set: []script.Var{script.Bare("RUN_ID", *runID), script.Quoted("POOL_ADDR", poolAddr),
			script.Quoted("OMES_DIR", omesDir), script.Bare("NAMESPACE", *namespace), script.Bare("SCENARIO", *scenario),
			script.Bare("CANDIDATE_RELEASE", "1")}}
	for _, k := range []struct {
		flag, name string
		value      func() (string, error)
	}{
		{"duration", "DURATION", func() (string, error) { return *duration, checkLength("-duration", *duration) }},
		{"rate", "RATE", func() (string, error) { return strconv.Itoa(*rate), checkCount("-rate", *rate) }},
		{"max-concurrent", "MAX_CONCURRENT", func() (string, error) {
			return strconv.Itoa(*maxConcurrent), checkCount("-max-concurrent", *maxConcurrent)
		}},
		{"quiet-wait", "POOL_QUIET_WAIT_S", func() (string, error) { return seconds("-quiet-wait", *quietWait) }},
		{"thresholds", "THRESHOLDS", func() (string, error) { return *thresholds, nil }},
	} {
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
	if err != nil {
		return a.refused("candidate", err)
	}
	if !a.inCheckout("candidate", c.Script) {
		return exitUsage
	}
	// The name is checked before the lease, so a typo leases nothing.
	if err := candidateChoice.check(a, name); err != nil {
		return a.refused("candidate", err)
	}
	c, env, code, ok := a.prepare("candidate", c, *dry)
	if !ok {
		return code
	}
	if *dry {
		fmt.Fprintln(a.Stdout, a.dryLine(c))
		return exitDone
	}

	client, _ := poolapi.New(*pool) // poolURL passed it
	l, doc, err := a.leaseSaying(ctx, "candidate", *pool, client, req)
	if err != nil {
		return poolFailedAs(ctx, a, "candidate", "lease", *pool, "", "", err)
	}
	path := lastLeasePath(a, poolapi.LastLeaseFile)
	giveBack := func() { a.giveBack(ctx, "candidate", client, *pool, l.ID, path, poolapi.LastLeaseFile, "") }
	// faults/candidate.sh reads the cell, and the id it gives back, from the last-lease file.
	if err := poolapi.WriteLast(path, doc); err != nil {
		fmt.Fprintf(a.Stderr, "laug · candidate: leased %s but could not keep it in %s, where faults/candidate.sh "+
			"reads it: %v\n", l.ID, poolapi.LastLeaseFile, a.errShown(err))
		giveBack()
		return exitFailed
	}
	if ctx.Err() != nil {
		fmt.Fprintf(a.Stderr, "laug · candidate: stopped before faults/candidate.sh started\n")
		giveBack()
		return exitFailed
	}
	first := fmt.Sprintf("laug · candidate: %s on lease %s (%s) until %s, run %s; faults/candidate.sh gives the cell "+
		"back when it ends, on Ctrl-C too", name, l.ID, l.Address, utc(l.ExpiresAt), *runID)
	// An exec that returns failed, and the script that would give the cell back never ran.
	if code := a.start("candidate", c, env, script.Exec, first, a.Stdout); code != exitDone {
		giveBack()
		return code
	}
	return exitDone
}
