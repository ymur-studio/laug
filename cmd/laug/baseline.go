package main

// laug baseline: every threshold read over one run's whole window, load/baseline.sh by exec.

import (
	"context"

	"github.com/ymur-studio/laug/internal/script"
)

func runBaseline(_ context.Context, a *App, args []string) int {
	f := a.newFlags("baseline", "<run-id> [-prom <url>] [-project <name>] [-overwrite] [-dry-run]")
	prom := f.String("prom", "", "the Prometheus `url` to read; unset, the one the run recorded, else the leased "+
		"cell's, else "+promDefault)
	project := f.String("project", "", "the compose `project` whose containers' CPU and memory it reads; unset, the "+
		"one the run recorded, or laug in its guest when the last lease is that microVM cell; empty skips those rows")
	overwrite := f.Bool("overwrite", false, "replace load/results/<run-id>.baseline.json, which may be committed "+
		"evidence; without it, an existing file stops the read")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.usageError(f, "names no run: give it a run id, the name of load/results/<run-id>.json")
	case len(pos) > 1:
		return a.usageError(f, "takes one run id, got %q", pos)
	}
	runID := pos[0]
	err := checkWords("run id", runID)
	if err == nil && given(f, "project") && *project != "" {
		err = checkWords("-project", *project)
	}
	if err != nil {
		return a.refused("baseline", err)
	}
	if !a.inCheckout("baseline", "load/baseline.sh") {
		return exitUsage
	}
	l := a.lastLease()
	explicit := ""
	if given(f, "prom") {
		explicit = *prom
	}
	c := script.Command{Script: "load/baseline.sh", Args: []script.Value{{Text: runID}},
		Set: []script.Var{script.Quoted("PROM_URL", explicit),
			script.Quoted("FALLBACK_PROM_URL", pick(given(f, "prom"), *prom, l.Prometheus, promDefault))}}
	// PROJECT is set only when -project is given, so the script reads the run's own; empty skips the rows.
	if given(f, "project") {
		c.Set = append(c.Set, script.Bare("PROJECT", *project))
	} else {
		c.Clear = append(c.Clear, "PROJECT")
	}
	if *overwrite {
		c.Pass = append(c.Pass, script.Bare("FORCE", "1"))
	} else {
		c.Clear = append(c.Clear, "FORCE")
	}
	c.By = map[string]string{"FORCE": "-overwrite"}
	return a.runScript("baseline", c, *dry, script.Exec, "")
}
