package main

// laug thresholds: the threshold set a server release is judged by, resolved by load/thresholds.sh and printed by
// load/print-thresholds.sh.

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

func runThresholds(_ context.Context, a *App, args []string) int {
	f := a.newFlags("thresholds", "[-server-tag 1.31.0] [-thresholds <file>] [-json] [-dry-run]")
	tag := f.String("server-tag", serverTagDefault, "the server release's `tag`, x.y.z; a release with no file of "+
		"its own, or none, '', gets the base, and the note says why")
	file := f.String("thresholds", "", "a threshold set `file` to print in place of the release's; a relative path "+
		"starts at the checkout's root")
	jsonOut := f.Bool("json", false, "print the set as one JSON object")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	// A tag that is not x.y.z names no release, and a script that read only the exit would take the base for it.
	if *tag != "" && !poolapi.TagRE.MatchString(*tag) {
		return a.refused("thresholds", fmt.Errorf("-server-tag %s: not a release tag, x.y.z; -server-tag '' gets "+
			"the base", shellWord(*tag)))
	}
	resolve := script.Command{Script: "load/thresholds.sh", Args: []script.Value{{Text: *tag, Quote: true}}}
	if *file != "" {
		resolve.Pass = []script.Var{script.Bare("THRESHOLDS", *file)}
	} else {
		resolve.Clear = []string{"THRESHOLDS"}
	}
	show := script.Command{Script: "load/print-thresholds.sh"}
	if *jsonOut {
		show.Args = []script.Value{{Text: "-json"}}
	}
	resolve, renv, code, ok := a.prepare("thresholds", resolve, *dry)
	if !ok {
		return code
	}
	show, penv, code, ok := a.prepare("thresholds", show, *dry)
	if !ok {
		return code
	}
	if *dry {
		// -dry-run's line: the resolve, refused with exit 2, then the print of what it resolved.
		fmt.Fprintf(a.Stdout, "th=$(%s) || exit 2; %s <<<\"$th\"\n", script.DryRun(resolve), script.DryRun(show))
		return exitDone
	}
	var set bytes.Buffer
	if code := a.start("thresholds", resolve, renv, script.Run, "", &set); code != exitDone {
		return exitUsage // load/thresholds.sh said why on stderr
	}
	show.Stdin = &set
	return a.start("thresholds", show, penv, script.Run, "", a.Stdout)
}
