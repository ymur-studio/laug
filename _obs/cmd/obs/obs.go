package main

// The stack's verbs: the observability stack (_obs/README.md), as make obs-up and its siblings ran it. up, down,
// status, find, backfill, pack, view and purge run _obs/obs.sh <verb>, and follow runs _obs/follow in this process: the
// part obs up starts from its copy of bin/obs, and the answers _obs/obs.sh asks bin/obs for. make obs-test runs
// _obs/obs.sh test, and make lint _obs/lint.sh. A script verb passes _obs/obs.sh the names make passed it from its
// command line, so its -dry-run line is make -n's with those names in front.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ymur-studio/laug/_obs/follow"
	"github.com/ymur-studio/laug/internal/script"
)

const (
	obsScript = "_obs/obs.sh"
	// obsMarker is the file _obs/obs.sh writes into OBS_DIR when it makes it: a directory without it is not the stack's.
	obsMarker = ".laug-obs"
	// The addresses _obs/obs.sh starts follow with, FOLLOW_LISTEN and POOL. TestObsMatchesScript reads them there.
	obsFollowListen = "127.0.0.1:7720"
	obsPoolAddr     = "127.0.0.1:7700"
	obsFollowEvery  = 2 * time.Second // follow's own -every
)

// obsParts are the stack's parts in _obs/obs.sh's start order, its PARTS: -only takes one.
var obsParts = []string{"prometheus", "jaeger", "collector", "cellstat", "follow", "grafana"}

// obsDirFlag adds -dir, the stack's state directory, OBS_DIR.
func obsDirFlag(f *flags) *string {
	return f.String("dir", "", "the stack's state `dir`, absolute and outside every checkout; unset, "+
		"$XDG_STATE_HOME/laug-obs, else ~/.local/state/laug-obs")
}

// obsOnlyFlag adds -only, one part.
func obsOnlyFlag(f *flags, verb string) *string {
	return f.String("only", "", verb+" this `part` alone: "+strings.Join(obsParts, ", "))
}

// obsDir is the stack's state directory as _obs/obs.sh resolves OBS_DIR: dir when -dir gave one, else
// $XDG_STATE_HOME/laug-obs, else $HOME/.local/state/laug-obs, one trailing slash cut.
func (a *App) obsDir(dir string) string {
	if dir == "" {
		base := a.getenv("XDG_STATE_HOME")
		if base == "" {
			base = a.getenv("HOME") + "/.local/state"
		}
		dir = base + "/laug-obs"
	}
	return strings.TrimSuffix(dir, "/")
}

// obsDirShown is the stack's state directory as laug prints it, one shell word that names no home path: unset by
// -dir, $XDG_STATE_HOME/laug-obs, else ~/.local/state/laug-obs, as obsDir resolves it; a -dir as wordShown writes it.
// OBS_DIR holds letters, digits and . _ + - / alone (_obs/obs.sh), so neither default needs quotes.
func (a *App) obsDirShown(dir string) string {
	if dir == "" {
		switch {
		case a.getenv("XDG_STATE_HOME") != "":
			return "$XDG_STATE_HOME/laug-obs"
		case a.getenv("HOME") != "":
			return "~/.local/state/laug-obs"
		}
	}
	return a.wordShown(a.obsDir(dir))
}

// obsCommand is _obs/obs.sh <verb>, OBS_DIR passed when -dir was given, or an error when -dir is not absolute, which
// _obs/obs.sh refuses too.
func obsCommand(f *flags, verb, dir string) (script.Command, error) {
	c := script.Command{Script: obsScript, Args: []script.Value{{Text: verb}}}
	passIf(&c, given(f, "dir"), "OBS_DIR", dir)
	if dir != "" && !filepath.IsAbs(dir) {
		return c, fmt.Errorf("-dir %s: an absolute path, outside every checkout", shellWord(dir))
	}
	return c, nil
}

// passIf passes name=value to c's script when on, as make passed a variable typed on its command line, unnamed;
// otherwise it clears name, so the script's own default holds and a stray copy is named.
func passIf(c *script.Command, on bool, name, value string) {
	if on {
		c.Pass = append(c.Pass, script.Bare(name, value))
	} else {
		c.Clear = append(c.Clear, name)
	}
}

// checkPart refuses an -only that names no part.
func checkPart(f *flags, part string) error {
	if given(f, "only") && !slices.Contains(obsParts, part) {
		return fmt.Errorf("-only %s: one of %s", shellWord(part), strings.Join(obsParts, ", "))
	}
	return nil
}

// obsBuilt refuses, before anything runs, what make built for its obs-* targets and this binary does not build: the
// checkout's bin/obs, which _obs/obs.sh runs for bin/obs follow's answers and copies, under obs up, to run the stack's
// cellstat and follow. Exit 2 outside a checkout, or without it.
func (a *App) obsBuilt(cmd string) (int, bool) {
	if !a.inCheckout(cmd, obsScript) {
		return exitUsage, false
	}
	if !a.executable(obsBin) {
		fmt.Fprintf(a.Stderr, "laug · %s: %s, which _obs/obs.sh runs, is not built: make build builds it\n", cmd, obsBin)
		return exitUsage, false
	}
	return exitDone, true
}

func runObsUp(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs up", "[-only <part>] [-no-cellstat] [-dir <dir>] [-dry-run]")
	only := obsOnlyFlag(f, "start")
	noCellstat := f.Bool("no-cellstat", false, "start no cellstat: a batch's "+a.self()+" usage record "+
		"-listen 127.0.0.1:7710 serves the store")
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -only names a part", pos)
	}
	err := checkPart(f, *only)
	if err == nil && *only == "cellstat" && *noCellstat {
		err = errors.New("-only cellstat with -no-cellstat starts nothing")
	}
	if err != nil {
		return a.refused("obs up", err)
	}
	c, err := obsCommand(f, "up", *dir)
	if err != nil {
		return a.refused("obs up", err)
	}
	passIf(&c, given(f, "only"), "ONLY", *only)
	passIf(&c, *noCellstat, "OBS_CELLSTAT", "0")
	if !*dry {
		if code, ok := a.obsBuilt("obs up"); !ok {
			return code
		}
	}
	what := "the stack's six parts"
	if *only != "" {
		what = *only + " alone"
	}
	if *noCellstat {
		what += ", no cellstat"
	}
	first := fmt.Sprintf("laug · obs up: %s on 127.0.0.1, ports 7710 to 7769, state in %s; the store settles first, "+
		"20 min at most; %s down stops it", what, a.obsDirShown(*dir), a.self())
	return a.runScript("obs up", c, *dry, script.Exec, first)
}

func runObsDown(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs down", "[-only <part>] [-while-gate-runs] [-dir <dir>] [-dry-run]")
	only := obsOnlyFlag(f, "stop")
	force := f.Bool("while-gate-runs", false, "stop it even while a gate runs, which it refuses otherwise")
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -only names a part", pos)
	}
	if err := checkPart(f, *only); err != nil {
		return a.refused("obs down", err)
	}
	c, err := obsCommand(f, "down", *dir)
	if err != nil {
		return a.refused("obs down", err)
	}
	passIf(&c, given(f, "only"), "ONLY", *only)
	passIf(&c, *force, "FORCE", "1")
	c.By = map[string]string{"FORCE": "-while-gate-runs"}
	// make obs-down built its follow in its recipe and told the script whether that worked, so a failed build still
	// let FORCE=1 stop the stack. This binary builds nothing: bin/obs, which _obs/obs.sh asks, is built or it is not.
	build := "ok"
	if a.Root != "" && !a.executable(obsBin) {
		build = "failed"
		if !*dry {
			fmt.Fprintln(a.Stderr, "laug · obs down: bin/obs is not built, so _obs/obs.sh cannot ask whether a gate "+
				"runs, and counts one as running; -while-gate-runs stops the stack anyway, and make build builds it")
		}
	}
	c.Set = append(c.Set, script.Bare("OBS_FOLLOW_BUILD", build))
	what := "the stack's parts stop by their records"
	if *only != "" {
		what = *only + " stops by its record"
	}
	gate := ""
	if *force {
		gate = ", even while a gate runs"
	}
	first := fmt.Sprintf("laug · obs down: %s, TERM then KILL after 30 s%s; the data stays in %s; %s up starts "+
		"it again", what, gate, a.obsDirShown(*dir), a.self())
	return a.runScript("obs down", c, *dry, script.Exec, first)
}

func runObsStatus(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs status", "[-json] [-dir <dir>] [-dry-run]")
	jsonOut := f.Bool("json", false, "print one JSON object: parts, store, disk_bytes, disk_of, free_bytes, "+
		"target_files, target_files_past_expiry, configs, last_gate and view")
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	c, err := obsCommand(f, "status", *dir)
	if err != nil {
		return a.refused("obs status", err)
	}
	if !*jsonOut {
		// The script's first line is a laug · line of its own: "laug · obs status, <UTC time> UTC".
		c.Clear = append(c.Clear, "JSON")
		return a.runScript("obs status", c, *dry, script.Exec, "")
	}
	// -json keeps the script a child, as every -json does: JSON=1 has it print one object, which jq writes over
	// several lines, so laug prints it on one.
	c.Pass = append(c.Pass, script.Bare("JSON", "1"))
	c, env, code, ok := a.prepare("obs status", c, *dry)
	if !ok {
		return code
	}
	if *dry {
		fmt.Fprintln(a.Stdout, script.DryRun(c))
		return exitDone
	}
	var buf bytes.Buffer
	code = a.start("obs status", c, env, script.Run, "", &buf)
	if code != exitDone && code != exitFailed {
		// _obs/obs.sh's own codes: 0 every part up, 1 one down, 2 refused (check_vars).
		return a.childExit("obs status", "no status", c.Script, code, exitUsage)
	}
	if err := writeCompact(a.Stdout, buf.Bytes()); err != nil {
		fmt.Fprintf(a.Stderr, "laug · obs status: %s printed no JSON object: %v\n", c.Script, a.errShown(err))
		return exitFailed
	}
	return code
}

func runObsFind(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs find", "[<gate id> | -run-id <run id>] [-results "+gateResultsDefault+"] [-dir <dir>] "+
		"[-dry-run]")
	runID := f.String("run-id", "", "find a load run outside a gate instead: its `id`, load/results/<id>.json")
	results := resultsFlag(f)
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) > 1:
		return a.usageError(f, "takes one gate id, got %q", pos)
	case len(pos) == 1 && given(f, "run-id"):
		return a.usageError(f, "takes a gate id or -run-id, not both")
	case given(f, "results") && given(f, "run-id"):
		return a.usageError(f, "-results is where a gate run is; -run-id reads load/results")
	}
	c, err := obsCommand(f, "find", *dir)
	if err != nil {
		return a.refused("obs find", err)
	}
	passIf(&c, given(f, "results"), "GATE_RESULTS", *results)
	c.By = map[string]string{"GATE": "<gate id>"}
	switch {
	case given(f, "run-id"):
		if err := checkWords("-run-id", *runID); err != nil {
			return a.refused("obs find", err)
		}
		c.Pass = append(c.Pass, script.Bare("RUN", *runID))
		c.Clear = append(c.Clear, "GATE")
	case len(pos) == 1:
		if err := checkGateID(pos[0]); err != nil {
			return a.refused("obs find", err)
		}
		c.Pass = append(c.Pass, script.Bare("GATE", pos[0]))
		c.Clear = append(c.Clear, "RUN")
	default:
		if !a.inCheckout("obs find", obsScript) {
			return exitUsage
		}
		id := a.latestRun(*results)
		if id == "" {
			fmt.Fprintf(a.Stderr, "laug · obs find: names no gate or run, and %s holds no gate run: %s find "+
				"<gate id>, or -run-id <run id>\n", shellWord(*results), a.self())
			return exitFailed
		}
		fmt.Fprintf(a.Stderr, "laug · obs find: names no gate or run, so the newest gate run in %s, %s\n",
			shellWord(*results), id)
		c.Pass = append(c.Pass, script.Bare("GATE", id))
		c.Clear = append(c.Clear, "RUN")
	}
	// The script's first line is a laug · line of its own: "laug · gate <id>: ..." or "laug · run <id>: ...".
	return a.runScript("obs find", c, *dry, script.Exec, "")
}

func runObsBackfill(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs backfill", "[<gate id>] [-results "+gateResultsDefault+"] [-dir <dir>] [-dry-run]")
	results := resultsFlag(f)
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 1 {
		return a.usageError(f, "takes one gate id, got %q", pos)
	}
	c, err := obsCommand(f, "backfill", *dir)
	if err != nil {
		return a.refused("obs backfill", err)
	}
	what := "each gate run in " + shellWord(*results) + " over 3 h old that the store lacks"
	if len(pos) == 1 {
		if err := checkGateID(pos[0]); err != nil {
			return a.refused("obs backfill", err)
		}
		what = "gate " + pos[0] + " of " + shellWord(*results)
	}
	passIf(&c, len(pos) == 1, "GATE", strings.Join(pos, ""))
	passIf(&c, given(f, "results"), "GATE_RESULTS", *results)
	c.By = map[string]string{"GATE": "<gate id>"}
	if !*dry {
		if code, ok := a.obsBuilt("obs backfill"); !ok {
			return code
		}
	}
	first := fmt.Sprintf("laug · obs backfill: %s, rendered, checked by promtool, moved into the store in %s, then "+
		"read back against its files; Ctrl-C stops it", what, a.obsDirShown(*dir))
	return a.runScript("obs backfill", c, *dry, script.Exec, first)
}

func runObsPack(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs pack", "[-pack <file>] [-dir <dir>] [-dry-run]")
	pack := f.String("pack", "", "the tarball's `file`, <name>.tar.gz, outside every checkout; unset, "+
		"OBS_DIR/packs/laug-obs-<UTC time>.tar.gz")
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -pack names the tarball", pos)
	}
	c, err := obsCommand(f, "pack", *dir)
	if err != nil {
		return a.refused("obs pack", err)
	}
	passIf(&c, given(f, "pack"), "PACK", *pack)
	if !*dry {
		if code, ok := a.obsBuilt("obs pack"); !ok {
			return code
		}
	}
	d := a.obsDirShown(*dir)
	into := d + "/packs/"
	if given(f, "pack") {
		into = a.wordShown(*pack)
	}
	first := fmt.Sprintf("laug · obs pack: the stopped stack's store and traces in %s into one tarball in %s, every "+
		"file checked first against check/deny-shapes.txt; Ctrl-C stops it", d, into)
	return a.runScript("obs pack", c, *dry, script.Exec, first)
}

func runObsView(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs view", "<pack> [-dir <dir>] [-dry-run]")
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		fmt.Fprintf(a.Stderr, "laug · obs view: names no pack: give it a tarball %s pack wrote\n", a.self())
		a.listPacks(a.Stderr, a.obsDir(*dir), a.obsDirShown(*dir))
		f.mistake(a.Stderr)
		return exitUsage
	case len(pos) > 1:
		return a.usageError(f, "takes one pack, got %q", pos)
	}
	c, err := obsCommand(f, "view", *dir)
	if err != nil {
		return a.refused("obs view", err)
	}
	c.Pass = append(c.Pass, script.Bare("PACK", pos[0]))
	c.By = map[string]string{"PACK": "<pack>"}
	if !*dry {
		if code, ok := a.obsBuilt("obs view"); !ok {
			return code
		}
		if !isFile(a.abs(pos[0])) {
			return a.refused("obs view", fmt.Errorf("%s: no such file; %s pack writes one", shellWord(pos[0]),
				a.self()))
		}
	}
	first := fmt.Sprintf("laug · obs view: %s read by the store and Grafana alone on 127.0.0.1:7730 and 7740, no "+
		"scrape job, so it measures nothing; %s down stops it", shellWord(pos[0]), a.self())
	return a.runScript("obs view", c, *dry, script.Exec, first)
}

// listPacks writes the packs in dir/packs, where obs pack writes unless -pack says, each with the line that views
// it, dir written as shown, obsDirShown's word. A bare view prints them, so the line to type is there.
func (a *App) listPacks(w io.Writer, dir, shown string) {
	packs := packsIn(dir)
	if len(packs) == 0 {
		fmt.Fprintf(w, "%s/packs holds no pack; %s pack makes one there\n", shown, a.self())
		return
	}
	fmt.Fprintf(w, "in %s/packs:\n", shown)
	for _, p := range packs {
		fmt.Fprintf(w, "  %s view %s/packs/%s   (%s MiB)\n", a.self(), shown, shellWord(p),
			mib(fileBytes(filepath.Join(dir, "packs", p))))
	}
}

// packsIn is the packs in dir/packs, by name, as _obs/obs.sh's purge finds them: files that end .tar.gz.
func packsIn(dir string) []string {
	ents, _ := os.ReadDir(filepath.Join(dir, "packs"))
	var packs []string
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".tar.gz") {
			packs = append(packs, e.Name())
		}
	}
	slices.Sort(packs)
	return packs
}

func runObsPurge(_ context.Context, a *App, args []string) int {
	f := a.newFlags("obs purge", "-remove-state [-remove-packs] [-dir <dir>] [-dry-run]")
	removeState := f.Bool("remove-state", false, "stop the stack, then remove OBS_DIR: its releases, configs, store, "+
		"traces, Grafana's state and a view's copy")
	removePacks := f.Bool("remove-packs", false, "with -remove-state, remove the packs in OBS_DIR/packs too, which it "+
		"refuses otherwise")
	dir := obsDirFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) > 0:
		return a.usageError(f, "takes no arguments, got %q; -dir names the stack's dir", pos)
	case *removePacks && !*removeState:
		return a.usageError(f, "-remove-packs takes -remove-state: the packs go with OBS_DIR")
	}
	d, ds := a.obsDir(*dir), a.obsDirShown(*dir)
	if !*removeState {
		readObsState(d).report(a.Stderr, *dir, ds, a.self())
		return exitUsage
	}
	c, err := obsCommand(f, "purge", *dir)
	if err != nil {
		return a.refused("obs purge", err)
	}
	passIf(&c, *removePacks, "FORCE", "1")
	c.By = map[string]string{"FORCE": "-remove-packs"}
	if *dry {
		return a.runScript("obs purge", c, true, script.Exec, "")
	}
	if !filepath.IsAbs(d) || d == "/" {
		return a.refused("obs purge", fmt.Errorf("OBS_DIR %s: an absolute path, not /", ds))
	}
	st := readObsState(d)
	switch {
	case st.exists && !st.marker:
		return a.refused("obs purge", fmt.Errorf("%s holds no %s marker, so it is not the stack's; nothing stopped "+
			"or removed", ds, obsMarker))
	case len(st.packs) > 0 && !*removePacks:
		for _, p := range st.packs {
			fmt.Fprintf(a.Stderr, "laug · obs purge: a pack: %s/packs/%s\n", ds, shellWord(p))
		}
		which := "1 pack, named above, which goes with OBS_DIR: copy it out first, or -remove-packs removes it too"
		if len(st.packs) > 1 {
			which = fmt.Sprintf("%d packs, named above, which go with OBS_DIR: copy each out first, or -remove-packs "+
				"removes them too", len(st.packs))
		}
		return a.refused("obs purge", fmt.Errorf("%s/packs holds %s; nothing stopped or removed", ds, which))
	}
	if code, ok := a.obsBuilt("obs purge"); !ok {
		return code
	}
	return a.runScript("obs purge", c, false, script.Exec, st.firstLine(ds))
}

// obsLoopback says whether addr is one follow takes: 127.0.0.1 and a port from 0 to 65535, written plain.
func obsLoopback(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 0 && n <= 65535 && strconv.Itoa(n) == port
}

// runObsFollow runs follow, _obs/follow, in this process. Bare, as obs up starts it from its copy of bin/obs,
// it is the stack's part: each leased cell's federation target into OBS_DIR/targets and each gate's records on GET
// /metrics, a poll every -every, until Ctrl-C or SIGTERM. -gate-running, -openmetrics and -decode answer once, as
// _obs/obs.sh asks them. Exit 0 stopped so, a gate running, or the answer printed; 1 when the listener cannot bind or
// OBS_DIR cannot be written, no gate runs, or the gate directory has no timeline; 2 on a value it refuses, an OBS_DIR
// that is not the stack's, or a store -decode cannot read.
func runObsFollow(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("obs follow", "[-listen "+obsFollowListen+"] [-pool "+obsPoolAddr+"] [-every 2s] [-dir <dir>] | "+
		"-gate-running [-pool <address>] | -openmetrics <gate dir> [-dir <dir>] | -decode -out <dir> [-dir <dir>]")
	listen := f.String("listen", obsFollowListen, "serve each gate's records on GET /metrics at this `address`, "+
		"127.0.0.1:<port>; the listener has no auth")
	pool := f.String("pool", obsPoolAddr, "the `address` the pool listens on, a gate's or the checkout's, "+
		"127.0.0.1:<port>")
	every := f.Duration("every", obsFollowEvery, "time between polls, over 0")
	gateOpen := f.Bool("gate-running", false, "say whether a gate runs on this host, and exit: 0 and why when one "+
		"does, 1 when none does")
	om := f.String("openmetrics", "", "print this gate `dir`'s records as OpenMetrics, in the gate's own times, and "+
		"exit; stdout holds them alone, for promtool")
	decode := f.Bool("decode", false, "decode OBS_DIR's store and trace records into -out, print one JSON object, "+
		"and exit: "+a.self()+" pack's check")
	out := f.String("out", "", "with -decode: an empty `dir`, outside OBS_DIR/prometheus and OBS_DIR/jaeger")
	dir := obsDirFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	modes := 0
	for _, on := range []bool{*gateOpen, given(f, "openmetrics"), *decode} {
		if on {
			modes++
		}
	}
	d, ds := a.obsDir(*dir), a.obsDirShown(*dir)
	var err error
	switch {
	case modes > 1:
		err = errors.New("-gate-running, -openmetrics and -decode: one at a time")
	case modes == 1 && given(f, "listen"):
		err = errors.New("-listen serves until stopped; -gate-running, -openmetrics and -decode answer once")
	case given(f, "openmetrics") && *om == "":
		err = errors.New("-openmetrics: name a gate directory")
	case given(f, "out") && !*decode:
		err = errors.New("-out goes with -decode")
	case *decode && *out == "":
		err = errors.New("-decode needs -out, an empty dir for the decoded records")
	case !obsLoopback(*listen):
		err = fmt.Errorf("-listen %s: 127.0.0.1:<port>; the listener has no auth", shellWord(*listen))
	case !obsLoopback(*pool):
		err = fmt.Errorf("-pool %s: 127.0.0.1:<port>; a pool listens on loopback only", shellWord(*pool))
	case *every <= 0:
		err = fmt.Errorf("-every %s: over 0", *every)
	case *gateOpen:
		// The answer reads the host's locks and the pool on -pool, and nothing in OBS_DIR.
	case !filepath.IsAbs(d):
		err = fmt.Errorf("OBS_DIR %s: an absolute path", ds)
	case !isFile(filepath.Join(d, obsMarker)):
		err = fmt.Errorf("%s holds no %s marker, so it is not the stack's, and follow writes its targets and records "+
			"there: %s up makes it", ds, obsMarker, a.self())
	}
	if err != nil {
		return a.refused("obs follow", err)
	}
	switch {
	case *gateOpen:
		var why bytes.Buffer
		code := follow.Run(ctx, []string{"-gate-running", "-pool", *pool}, &why, a.Stderr, nil)
		switch code {
		case exitDone:
			fmt.Fprintf(a.Stdout, "laug · obs follow: a gate runs: %s\n", strings.TrimSpace(why.String()))
		case exitFailed:
			fmt.Fprintf(a.Stdout, "laug · obs follow: no gate runs on this host, by its locks and the pool on %s\n",
				*pool)
		}
		return code
	case given(f, "openmetrics"):
		return follow.Run(ctx, []string{"-openmetrics", *om, "-dir", d}, a.Stdout, a.Stderr, nil)
	case *decode:
		return follow.Run(ctx, []string{"-decode", d, "-out", *out}, a.Stdout, a.Stderr, nil)
	}
	ready := func(addr net.Addr) {
		fmt.Fprintf(a.Stdout, "laug · obs follow: each gate's records on http://%s/metrics, each leased cell's target "+
			"in %s/targets, a poll of the pool on %s every %s; Ctrl-C or SIGTERM stops it\n", addr, ds,
			*pool, *every)
	}
	fargs := []string{"-listen", *listen, "-pool", *pool, "-every", every.String(), "-dir", d}
	return follow.Run(ctx, fargs, a.Stdout, a.Stderr, ready)
}

// obsArea is one part of OBS_DIR as purge counts it: the files under its top directories, and their blocks on disk.
type obsArea struct {
	name  string
	tops  []string // the top directories; none for the rest
	files int
	bytes int64
}

// obsState is what OBS_DIR holds, read before purge removes it.
type obsState struct {
	dir            string
	exists, marker bool
	files          int
	bytes          int64
	areas          []obsArea
	packs          []string
}

// readObsState walks dir: each file under it counted once, in the area of its top directory, with its blocks on disk
// as du counts them, so Badger's sparse files count what they hold.
func readObsState(dir string) obsState {
	st := obsState{dir: dir, areas: []obsArea{
		{name: "the store", tops: []string{"prometheus"}}, {name: "the traces", tops: []string{"jaeger"}},
		{name: "the releases", tops: []string{"dist", "bin"}}, {name: "Grafana", tops: []string{"grafana"}},
		{name: "a view's copy", tops: []string{"view", "view.new"}}, {name: "the packs", tops: []string{"packs",
			"pack.new"}}, {name: "the rest"}}}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return st
	}
	st.exists, st.marker = true, isFile(filepath.Join(dir, obsMarker))
	st.packs = packsIn(dir)
	_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		top, _, _ := strings.Cut(rel, string(filepath.Separator))
		i := slices.IndexFunc(st.areas, func(ar obsArea) bool { return slices.Contains(ar.tops, top) })
		if i < 0 {
			i = len(st.areas) - 1
		}
		n := diskBytes(path)
		st.areas[i].files++
		st.areas[i].bytes += n
		st.files++
		st.bytes += n
		return nil
	})
	return st
}

// diskBytes is the space path takes on disk, its blocks as stat gives them, as du counts it; its length where stat
// gives no blocks. A link counts as the link.
func diskBytes(path string) int64 {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if s, ok := fi.Sys().(*syscall.Stat_t); ok {
		return s.Blocks * 512
	}
	return fi.Size()
}

// fileBytes is path's length, 0 when it cannot be read.
func fileBytes(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// mib is n bytes in MiB, to one decimal, as _obs/obs.sh prints them.
func mib(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64)
}

// report writes what a purge would remove, by area, and the line that removes it, starting with self, with -dir when
// dir gave one: bare purge's answer. shown is st.dir as obsDirShown writes it.
func (st obsState) report(w io.Writer, dir, shown, self string) {
	d := shown
	switch {
	case !st.exists:
		fmt.Fprintf(w, "laug · obs purge: no OBS_DIR at %s: nothing to remove\n", d)
		return
	case !st.marker:
		fmt.Fprintf(w, "laug · obs purge: %s holds %s and no %s marker, so it is not the stack's, and purge "+
			"refuses it\n", d, plural(st.files, "file"), obsMarker)
		return
	}
	fmt.Fprintf(w, "laug · obs purge: would stop the stack's parts, then remove %s, OBS_DIR: %s, %s MiB on disk\n", d,
		plural(st.files, "file"), mib(st.bytes))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, ar := range st.areas {
		where := "everything else"
		if len(ar.tops) > 0 {
			where = strings.Join(ar.tops, "/, ") + "/"
		}
		packs := ""
		if ar.name == "the packs" && len(st.packs) > 0 {
			packs = "\t" + plural(len(st.packs), "pack") + ": " + strings.Join(st.packs, ", ")
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s MiB%s\n", ar.name, where, plural(ar.files, "file"), mib(ar.bytes), packs)
	}
	tw.Flush()
	line, also := self+" purge -remove-state", ""
	switch len(st.packs) {
	case 0:
	case 1:
		line, also = line+" -remove-packs", ", the pack with it; copy the pack out first to keep it"
	default:
		line, also = line+" -remove-packs", fmt.Sprintf(", the %d packs with it; copy a pack out first to keep it",
			len(st.packs))
	}
	if dir != "" {
		line += " -dir " + d
	}
	fmt.Fprintf(w, "%s removes it%s\n", line, also)
}

// firstLine is purge's first line: what goes, with counts. shown is st.dir as obsDirShown writes it.
func (st obsState) firstLine(shown string) string {
	d := shown
	if !st.exists {
		return fmt.Sprintf("laug · obs purge: no OBS_DIR at %s, nothing to remove; it stops any part a record names "+
			"and counts what is left", d)
	}
	packs := ""
	if len(st.packs) > 0 {
		packs = ", " + plural(len(st.packs), "pack") + " among them"
	}
	return fmt.Sprintf("laug · obs purge: the stack's parts stop, then %s goes: %s, %s MiB on disk%s; then it counts "+
		"what is left", d, plural(st.files, "file"), mib(st.bytes), packs)
}
