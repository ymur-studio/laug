package main

// laug verdict: one run judged by its release's thresholds, load/verdict.sh as a child, its verdict line through
// writeVerdict, the verdict JSON kept beside the run's.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// verdictOpts is verdict's flags.
type verdictOpts struct {
	prom, thresholds string
	sweepMargin      time.Duration
	jsonOut, dry     bool
	runID, out       string
}

func runVerdict(ctx context.Context, a *App, args []string) int {
	var o verdictOpts
	f := a.newFlags("verdict", "<run-id> [-prom <url>] [-thresholds <file>] [-sweep-margin 30s] [-out <file>] [-json] "+
		"[-dry-run]")
	f.StringVar(&o.prom, "prom", "", "the Prometheus `url` to read; unset, the one the run recorded, else the leased "+
		"cell's, else "+promDefault)
	f.StringVar(&o.thresholds, "thresholds", "", "a threshold set `file` to judge by, in place of the run's release's; "+
		"a relative path starts at the checkout's root")
	f.DurationVar(&o.sweepMargin, "sweep-margin", 30*time.Second, "the sweep counts workflows started this long "+
		"before the run's end, whole seconds")
	f.StringVar(&o.out, "out", "", "a new `file` for the verdict JSON, beside the run's JSON when unset; a relative "+
		"path starts at the checkout's root")
	f.BoolVar(&o.jsonOut, "json", false, "print the verdict as one JSON object, from load/verdict.sh's VERDICT_JSON; "+
		"its text goes to stderr")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	o.dry = *dry
	switch len(pos) {
	case 0:
		return a.usageError(f, "names no run: give it a run id, the name of load/results/<run-id>.json")
	case 1:
		o.runID = pos[0]
	default:
		return a.usageError(f, "takes one run id, got %q", pos)
	}
	err := checkWords("run id", o.runID)
	margin := ""
	if err == nil && given(f, "sweep-margin") {
		margin, err = seconds("-sweep-margin", o.sweepMargin)
	}
	if err != nil {
		return a.refused("verdict", err)
	}
	if !a.inCheckout("verdict", "load/verdict.sh") {
		return exitUsage
	}
	// The verdict JSON goes beside the run's, as a gate item's does, or to -out, and never over a file: a committed
	// run's verdict stays as it was recorded.
	keep, there := o.out, false
	if keep == "" {
		dir := a.getenv("RESULTS_DIR") // load/verdict.sh's seam, where it reads the run
		if dir == "" {
			dir = "load/results"
		}
		keep = filepath.Join(dir, o.runID+".verdict.json")
	}
	keepPath := keep
	if !filepath.IsAbs(keepPath) {
		keepPath = filepath.Join(a.Root, keepPath)
	}
	if _, err := os.Lstat(keepPath); err == nil {
		there = true
	}
	switch fi, err := os.Stat(filepath.Dir(keepPath)); {
	case o.out != "" && there:
		return a.refused("verdict", fmt.Errorf("-out %s: the file is there already, and verdict writes only a new one",
			shellWord(o.out)))
	case o.out != "" && (err != nil || !fi.IsDir()):
		return a.refused("verdict", fmt.Errorf("-out %s: no directory %s", shellWord(o.out),
			a.pathShown(filepath.Dir(keepPath))))
	}
	// The Prometheus given is PROM_URL, and the fallback for a run that recorded none is the one given, else the
	// leased cell's, else block 0's.
	l := a.lastLease()
	explicit := ""
	if given(f, "prom") {
		explicit = o.prom
	}
	c := script.Command{Script: "load/verdict.sh", Args: []script.Value{{Text: o.runID}},
		Set: []script.Var{script.Quoted("PROM_URL", explicit),
			script.Quoted("FALLBACK_PROM_URL", pick(given(f, "prom"), o.prom, l.Prometheus, promDefault))}}
	jsonFile := ""
	if !there {
		jsonFile = keep
	} else if o.jsonOut {
		// The file there stays, so -json reads the script's document from a temp dir of laug's own.
		dir := filepath.Join(a.tmpDir(), "laug-verdict-*")
		if !o.dry {
			if dir, err = os.MkdirTemp(a.tmpDir(), "laug-verdict-"); err != nil {
				fmt.Fprintf(a.Stderr, "laug · verdict: a temp dir for -json: %v\n", a.errShown(err))
				return exitFailed
			}
			defer os.RemoveAll(dir)
		}
		jsonFile = filepath.Join(dir, "verdict.json")
	}
	for _, k := range []struct{ name, value string }{
		{"THRESHOLDS", o.thresholds}, {"SWEEP_MARGIN_S", margin}, {"VERDICT_JSON", jsonFile},
	} {
		if k.value != "" {
			c.Pass = append(c.Pass, script.Bare(k.name, k.value))
		} else {
			c.Clear = append(c.Clear, k.name)
		}
	}
	c, env, code, ok := a.prepare("verdict", c, o.dry)
	if !ok {
		return code
	}
	if o.dry {
		fmt.Fprintln(a.Stdout, a.dryLine(c))
		return exitDone
	}
	// load/verdict.sh's own codes are 0 ADVANCE, 1 BLOCK, 2 no verdict and 3 a short run (load/verdict.sh's header).
	exit := func(code int) int {
		return a.childExit("verdict", "no verdict", c.Script, code, exitDone, exitFailed, exitUsage, exitShortRun)
	}
	// kept says where the verdict JSON went, on w, after the script's text. The script writes it on exit 0, 1 and 3
	// only; a file it left on any other exit, cut short, goes, since none was there before.
	kept := func(w io.Writer, code int) {
		judged := code == exitDone || code == exitFailed || code == exitShortRun
		switch _, err := os.Stat(keepPath); {
		case there && judged:
			fmt.Fprintf(w, "laug · verdict: %s was there already and stays as it was; -out <file> keeps this "+
				"verdict\n", a.pathShown(keepPath))
		case there:
		case err == nil && judged:
			fmt.Fprintf(w, "laug · verdict: kept in %s%s\n", a.pathShown(keepPath), a.untracked(ctx, keepPath))
		case err == nil:
			os.Remove(keepPath)
		}
	}
	if o.jsonOut {
		// The text goes to stderr, so stdout holds the one document, written on exit 0, 1 and 3 only.
		raw := a.start("verdict", c, env, script.Run, "", a.Stderr)
		code := exit(raw)
		kept(a.Stderr, raw)
		read := jsonFile
		if !filepath.IsAbs(read) {
			read = filepath.Join(a.Root, read)
		}
		doc, err := os.ReadFile(read)
		if errors.Is(err, os.ErrNotExist) {
			return code
		}
		if err == nil {
			err = writeCompact(a.Stdout, doc)
		}
		if err != nil {
			fmt.Fprintf(a.Stderr, "laug · verdict: load/verdict.sh's JSON: %v\n", a.errShown(err))
			return exitFailed
		}
		return code
	}
	w := &verdictLines{a: a, w: a.Stdout}
	raw := a.start("verdict", c, env, script.Run, "", w)
	w.flush()
	kept(a.Stdout, raw)
	return exit(raw)
}

// untracked is ", untracked by git" when git status, in the checkout, lists path as a file it does not track and does
// not ignore, and "" otherwise: for a file outside the checkout, an ignored one, or with no git. A verdict JSON beside
// a run that is not committed is such a file, and git status lists it beside the run's own; a runbook walk's runs,
// rb<N>-<UTC time>, are ignored (.gitignore).
func (a *App) untracked(ctx context.Context, path string) string {
	out, err := a.git(ctx, "status", "--porcelain", "--ignored", "--untracked-files=all", "--", path)
	if err != nil || !strings.HasPrefix(out, "?? ") {
		return ""
	}
	return ", untracked by git"
}

// verdictLines passes load/verdict.sh's stdout through a line at a time, and writes its verdict line, "VERDICT:
// ADVANCE" or "VERDICT: BLOCK", with the word through writeVerdict: bold in moss or ember on a terminal, plain
// otherwise, so a reader of the text sees the line as the script printed it.
type verdictLines struct {
	a       *App
	w       io.Writer
	partial []byte
}

func (v *verdictLines) Write(p []byte) (int, error) {
	v.partial = append(v.partial, p...)
	for {
		i := bytes.IndexByte(v.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(v.partial[:i])
		v.partial = v.partial[i+1:]
		var err error
		switch line {
		case "VERDICT: ADVANCE", "VERDICT: BLOCK":
			if _, err = io.WriteString(v.w, "VERDICT: "); err == nil {
				err = v.a.writeVerdict(v.w, line[len("VERDICT: "):], false)
			}
		default:
			_, err = io.WriteString(v.w, line+"\n")
		}
		if err != nil {
			return len(p), err
		}
	}
}

// flush writes what the script left after its last newline.
func (v *verdictLines) flush() {
	if len(v.partial) > 0 {
		v.w.Write(v.partial)
		v.partial = nil
	}
}
