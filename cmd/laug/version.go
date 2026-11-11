package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
)

// versionInfo is what version prints. Its field names are the -json contract.
type versionInfo struct {
	Commit    string `json:"commit"`
	Go        string `json:"go"`
	ServerTag string `json:"server_tag"`
	OmesRef   string `json:"omes_ref"`
	// OmesCommit is the commit the checkout's .upstream/omes is at, the name load/run-omes.sh records it under in
	// load/results; null with no clone, outside a checkout, or a HEAD that does not read.
	OmesCommit *string `json:"omes_commit"`
}

func runVersion(_ context.Context, a *App, args []string) int {
	f := a.newFlags("version", "[-json]")
	jsonOut := f.Bool("json", false, "print one JSON object: commit, go, server_tag, omes_ref and omes_commit, the "+
		"commits in full")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(a.Stderr, "laug · version: takes no arguments, got %q\n", pos)
		f.mistake(a.Stderr)
		return exitUsage
	}
	v := versionInfo{Commit: buildCommit(), Go: runtime.Version(), ServerTag: serverTagDefault, OmesRef: omesRef}
	sha, present, err := omesCommit(a.Root)
	if err == nil && sha != "" {
		v.OmesCommit = &sha
	}
	if *jsonOut {
		if err := writeJSON(a.Stdout, v); err != nil {
			fmt.Fprintf(a.Stderr, "laug · version: writing JSON: %v\n", a.errShown(err))
			return exitFailed
		}
		return exitDone
	}
	line := fmt.Sprintf("laug · version %s %s server %s omes %s", short(v.Commit), v.Go, v.ServerTag, short(v.OmesRef))
	switch {
	case v.OmesCommit != nil && sha == omesRef:
		line += ", " + omesDir + " at " + short(sha)
	case v.OmesCommit != nil:
		line += ", " + omesDir + " at " + short(sha) + ", not " + short(omesRef)
	case present:
		line += ", " + omesDir + " at a commit laug cannot read"
	}
	fmt.Fprintln(a.Stdout, line)
	return exitDone
}

// short cuts a 40-hex commit to 7 hex digits and keeps its -dirty. Anything else, dev included, stays whole.
func short(c string) string {
	sha, dirty := strings.CutSuffix(c, "-dirty")
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return c
	}
	if dirty {
		return sha[:7] + "-dirty"
	}
	return sha[:7]
}
