package main

// The usage verbs: cgroup samples of laug cells, read by _obs/cellstat in this process. record writes a line every
// -interval into a new file, as make cell-usage ran cellstat; sample prints one line, as ONCE=1 did; stop ends a
// recording, as make cell-usage-stop did; report sums a file per item and service, as make cell-usage-report did.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ymur-studio/laug/_obs/cellstat"
)

// The defaults make cell-usage had in the Makefile laug replaced: USAGE_OUT, INTERVAL and USAGE_FOR.
// TestUsageDefaultsMatchKnobs holds them to internal/script's knob table.
const (
	usageDir             = "cells/run/cellstat"
	usageOutDefault      = usageDir + "/usage.jsonl"
	usageIntervalDefault = 5 * time.Second
	usageForDefault      = 20 * time.Minute
	// usageOutNone is the -out that writes no line: with -listen, the observability stack's cellstat, which _obs/obs.sh
	// starts as bin/obs usage record -out none -listen 127.0.0.1:7710.
	usageOutNone = "none"
)

// cellstatName is the process name of a recording: usage stop and _obs/obs.sh's batch_cellstat find one by it, as they
// found the cellstat make cell-usage started.
const cellstatName = "cellstat"

// usageRoot is the dir that holds the kernel's proc and cgroup trees cellstat reads: / on a host. Tests point it at
// _obs/cellstat's testdata.
var usageRoot = "/"

// usageStopWait is how long stop waits for the last line after TERM. A sample makes two docker calls of 4 s at most,
// given 1 s more to exit: 2 samples x 2 calls x 5 s = 20 s, and the Makefile's cell-usage-stop comment saw 19.7 s from
// TERM to exit with every docker call stalled. 25 s is 5 s over that, and a rerun waits 25 s more. Tests shorten it.
var usageStopWait = 25 * time.Second

// nameCellstat names this process cellstat. Writing /proc/self/comm renames the thread group's leader, whose name
// /proc/<pid>/comm gives (seen: TestUsageStop reads it from a recording bin/obs runs). Tests replace it.
var nameCellstat = func() error {
	f, err := os.OpenFile("/proc/self/comm", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(cellstatName))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// usageRecordOpts is usage record's flags.
type usageRecordOpts struct {
	interval, forDur  time.Duration
	out, listen, pids string
}

func (a *App) newUsageRecordFlags(o *usageRecordOpts) *flags {
	f := a.newFlags("usage record", "[-interval 5s] [-for 20m] [-out "+usageOutDefault+"] "+
		"[-listen 127.0.0.1:<port>] [-pids <dir>]")
	f.DurationVar(&o.interval, "interval", usageIntervalDefault, "time between lines, over 0; a sample that overruns "+
		"skips the ticks it missed")
	f.DurationVar(&o.forDur, "for", usageForDefault, "stop after this long with one last line; 0 waits for "+
		a.self()+" usage stop")
	f.StringVar(&o.out, "out", usageOutDefault, "the new `file` the lines go to, refused when it exists; FILE.pid "+
		"holds the pid while it records; a relative path starts at the checkout's root; "+usageOutNone+" writes no "+
		"line and takes -listen, as the observability stack's cellstat")
	f.StringVar(&o.listen, "listen", "", "also serve the last sample on GET /metrics at this `address`, "+
		"127.0.0.1:<port> (7710 for the observability stack); the listener has no auth")
	f.StringVar(&o.pids, "pids", "", "also read the processes the records in this `dir` name, <OBS_DIR>/run for the "+
		"observability stack's, and the orchestrator's dev server and worker")
	return f
}

// runUsageRecord records until -for, bin/obs usage stop, Ctrl-C or SIGTERM, then writes one last line. Exit 0 when it
// stopped so; 1 when the file, its pid file or the listener cannot be set up, a line cannot be written, or this
// process cannot take the name cellstat; 2 on a flag, an argument or a value it refuses, an -out that exists, or a
// host with no proc and cgroup trees. -out none writes no line and keeps no pid file: with -listen, it serves the last
// sample alone, as the observability stack's cellstat.
func runUsageRecord(ctx context.Context, a *App, args []string) int {
	var o usageRecordOpts
	f := a.newUsageRecordFlags(&o)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -out names the file", pos)
	}
	out := a.abs(o.out)
	if o.out == usageOutNone {
		out = ""
	}
	if err := checkRecord(a, o, out); err != nil {
		return a.refused("usage record", err)
	}
	if err := kernelTree(); err != nil {
		return a.refused("usage record", err)
	}
	cli := a.cellstatCLI("usage record")
	fail := func(format string, v ...any) int {
		fmt.Fprintf(a.Stderr, "laug · usage record: %s\n", a.textShown(fmt.Sprintf(format, v...)))
		return exitFailed
	}
	if out != "" {
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return fail("-out %s: %v", shellWord(o.out), err)
		}
	}
	if err := nameCellstat(); err != nil {
		return fail("naming this process %s, as usage stop finds a recording: %v", cellstatName, err)
	}
	// A second TERM or INT ends the recording as the first does, as cellstat by its own name keeps them: main stops
	// catching them after the first, and this channel keeps them caught until the last line is written.
	hold := make(chan os.Signal, 1)
	signal.Notify(hold, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(hold)

	cargs := []string{"-interval", o.interval.String(), "-for", o.forDur.String(), "-root", usageRoot, "-cli", cli}
	if out != "" {
		cargs = append(cargs, "-out", out)
	}
	if o.listen != "" {
		cargs = append(cargs, "-listen", o.listen)
	}
	if o.pids != "" {
		cargs = append(cargs, "-pids", a.abs(o.pids))
	}
	file := shellWord(o.out)
	ready := func() {
		until := "for " + o.forDur.String() + " at most"
		if o.forDur == 0 {
			until = "until stopped"
		}
		pids := ""
		if o.pids != "" {
			pids = "; also the processes recorded in " + shellWord(o.pids)
		}
		if out == "" {
			fmt.Fprintf(a.Stdout, "laug · usage record: no file (-out none): the last sample on http://%s/metrics, "+
				"one every %s %s, pid %d%s; Ctrl-C or SIGTERM stops it\n", o.listen, o.interval, until, os.Getpid(),
				pids)
			return
		}
		also := ""
		if o.listen != "" {
			also += "; the last sample on http://" + o.listen + "/metrics"
		}
		also += pids
		fmt.Fprintf(a.Stdout, "laug · usage record: a line every %s into %s %s, pid %d%s; %s usage stop %s, "+
			"Ctrl-C or SIGTERM stops it\n", o.interval, file, until, os.Getpid(), also, a.self(), file)
	}
	if code := cellstat.Run(ctx, cargs, a.Stdout, a.Stderr, ready); code != exitDone {
		return code
	}
	if out == "" {
		fmt.Fprintln(a.Stdout, "laug · usage record: stopped; -out none, so no file holds a line")
		return exitDone
	}
	fmt.Fprintf(a.Stdout, "laug · usage record: stopped: %s in %s; %s usage report %s sums them\n",
		plural(lineCount(out), "line"), file, a.self(), file)
	return exitDone
}

// checkRecord refuses what make cell-usage's recipe refused, in its order, and an -out that exists: a recording
// writes a new file. out is empty for -out none, which takes -listen, since it writes no line.
func checkRecord(a *App, o usageRecordOpts, out string) error {
	switch {
	case o.listen != "" && !cellstatListen(o.listen):
		return fmt.Errorf("-listen %s: 127.0.0.1:<port> (7710 for the observability stack); the listener has no auth",
			shellWord(o.listen))
	case o.pids != "" && notDir(a.abs(o.pids)):
		return fmt.Errorf("-pids %s: not a dir; name the dir of the stack's records, <OBS_DIR>/run", shellWord(o.pids))
	case o.interval <= 0:
		return fmt.Errorf("-interval %s: over 0 (5s, 500ms)", o.interval)
	case o.forDur < 0:
		return fmt.Errorf("-for %s: 0 or more; 0 waits for %s usage stop", o.forDur, a.self())
	case o.out == "":
		return errors.New("-out: name a file")
	case out == "" && o.listen == "":
		return errors.New("-out " + usageOutNone + " writes no line, so it takes -listen, where the last sample is " +
			"served")
	case out == "":
		return nil
	}
	fi, err := os.Lstat(out)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("-out %s: %w", shellWord(o.out), err)
	case fi.IsDir():
		return fmt.Errorf("-out %s: a directory; name a file", shellWord(o.out))
	}
	if pid, up := recording(out); up {
		return fmt.Errorf("-out %s: cellstat %d writes it; %s usage stop %s ends it, or name a new file",
			shellWord(o.out), pid, a.self(), shellWord(o.out))
	}
	return fmt.Errorf("-out %s: exists, %s; name a new file, or sum it with %s usage report %s",
		shellWord(o.out), plural(lineCount(out), "line"), a.self(), shellWord(o.out))
}

// cellstatListen says whether addr is one cellstat's listener takes and the recipe let through: 127.0.0.1 and a port
// from 1 to 65535, written plain. localhost is refused, since it may resolve to ::1.
func cellstatListen(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}

// notDir says whether something that is not a directory is at path. Nothing there passes: -pids may name the stack's
// record dir before the stack makes it, as the recipe let it.
func notDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// kernelTree refuses a usageRoot with no /proc/uptime or /sys/fs/cgroup, as cellstat does, in laug's words.
func kernelTree() error {
	_, err := os.Stat(filepath.Join(usageRoot, "proc", "uptime"))
	fi, derr := os.Stat(filepath.Join(usageRoot, "sys", "fs", "cgroup"))
	if err != nil || derr != nil || !fi.IsDir() {
		return errors.New("no /proc/uptime or /sys/fs/cgroup here: cellstat reads a Linux host's proc and cgroup v2 " +
			"trees")
	}
	return nil
}

// cellstatCLI is the docker cellstat lists containers with, found on the App's PATH; with none there, cellstat.NoCLI,
// and one stderr line says what the lines will lack. The process's own PATH is never read.
func (a *App) cellstatCLI(cmd string) string {
	if p := a.lookPath("docker"); p != "" {
		return p
	}
	fmt.Fprintf(a.Stderr, "laug · %s: no docker on PATH: a line holds the host and Omes, no container, and says "+
		"list failed\n", cmd)
	return cellstat.NoCLI
}

// runUsageSample prints one line of what record writes: a table of what it found, or with -json the line itself.
// Exit 0 printed; 2 on a flag, an argument, a -pids that names a file, or a host with no proc and cgroup trees; 1 when
// the line cannot be read back.
func runUsageSample(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("usage sample", "[-pids <dir>] [-json]")
	pids := f.String("pids", "", "also read the processes the records in this `dir` name, as record -pids does")
	jsonOut := f.Bool("json", false, "print the line as record writes it, one JSON object")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	if *pids != "" && notDir(a.abs(*pids)) {
		return a.refused("usage sample", fmt.Errorf("-pids %s: not a dir; name the dir of the stack's records, "+
			"<OBS_DIR>/run", shellWord(*pids)))
	}
	if err := kernelTree(); err != nil {
		return a.refused("usage sample", err)
	}
	cargs := []string{"-once", "-root", usageRoot, "-cli", a.cellstatCLI("usage sample")}
	if *pids != "" {
		cargs = append(cargs, "-pids", a.abs(*pids))
	}
	var line bytes.Buffer
	if code := cellstat.Run(ctx, cargs, &line, a.Stderr, nil); code != exitDone {
		return code
	}
	if *jsonOut {
		_, err := a.Stdout.Write(line.Bytes())
		if err != nil {
			fmt.Fprintf(a.Stderr, "laug · usage sample: writing the line: %v\n", a.errShown(err))
			return exitFailed
		}
		return exitDone
	}
	var l cellstat.Line
	if err := json.Unmarshal(line.Bytes(), &l); err != nil {
		fmt.Fprintf(a.Stderr, "laug · usage sample: reading the line back: %v\n", a.errShown(err))
		return exitFailed
	}
	writeSample(a.Stdout, l)
	return exitDone
}

// writeSample writes a line as a person reads it: a summary, then a row a container, then a row a process. Counters
// are totals since each started: CPU in seconds, memory in MiB; "-" is a reading that was not read.
func writeSample(w io.Writer, l cellstat.Line) {
	states := map[string]int{}
	for _, c := range l.Containers {
		states[c.State]++
	}
	var parts []string
	for _, s := range []string{"ok", "unresolved", "gone"} {
		if states[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", states[s], s))
		}
	}
	about := plural(len(l.Containers), "laug container")
	if len(parts) > 0 {
		about += " (" + strings.Join(parts, ", ") + ")"
	}
	about += fmt.Sprintf(", %d of other projects", l.Others)
	switch {
	case l.Procs == nil:
		about += ", Omes not read"
	default:
		about += ", " + plural(len(*l.Procs), "Omes process")
	}
	if l.Host.VCPU != nil {
		about += fmt.Sprintf(", host %d vCPU", *l.Host.VCPU)
	}
	if len(l.Errors) > 0 {
		about += "; errors: " + strings.Join(l.Errors, ", ")
	}
	fmt.Fprintf(w, "laug · usage sample: %s\n", about)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(l.Containers) > 0 {
		fmt.Fprintln(tw, "project\tservice\tid\tstate\tcpu_s\tanon_MiB\tfile_MiB\twhy")
		for _, c := range l.Containers {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", plain(c.Project), plain(c.Service), c.ID, c.State,
				scaled(c.CPU, 1e6, 3), scaled(c.Anon, 1<<20, 1), scaled(c.File, 1<<20, 1), plain(c.Why))
		}
		tw.Flush()
	}
	var procs []cellstat.Proc
	if l.Procs != nil {
		procs = append(procs, *l.Procs...)
	}
	if l.Pids != nil {
		procs = append(procs, *l.Pids...)
	}
	if len(procs) > 0 {
		fmt.Fprintln(tw, "role\trun_id\tpid\tthreads")
		for _, p := range procs {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", plain(p.Role), plain(p.RunID), p.PID, scaled(p.Threads, 1, 0))
		}
		tw.Flush()
	}
}

// scaled is a reading divided by unit, to prec decimals, or "-" when it was not read.
func scaled(v *uint64, unit float64, prec int) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(float64(*v)/unit, 'f', prec, 64)
}

// plural is n and noun, with an s past 1 or at 0.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "s") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// runUsageStop ends the recording that writes <file>, as make cell-usage-stop did: TERM to the pid in <file>.pid, then
// a wait of up to 25 s for its last line. A cellstat is writing <file> while <file>.pid names it, the process is named
// cellstat and it holds the file's lock: a pid file a killed cellstat left is not enough. Exit 0 stopped; 1 when none
// writes the file, TERM is refused, it is still up after the wait, or the wait is cut short; 2 bare or on a flag.
func runUsageStop(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("usage stop", "<file>")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		fmt.Fprintln(a.Stderr, "laug · usage stop: names no recording: give it the file a recording writes")
		a.listRecordings(a.Stderr, "stop", true)
		f.mistake(a.Stderr)
		return exitUsage
	case len(pos) > 1:
		return a.usageError(f, "takes one file, got %q", pos)
	}
	file, path := shellWord(pos[0]), a.abs(pos[0])
	pid, up := recording(path)
	if !up {
		fmt.Fprintf(a.Stderr, "laug · usage stop: no cellstat is writing %s\n", file)
		return exitFailed
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && writing(path, pid) {
		fmt.Fprintf(a.Stderr, "laug · usage stop: cellstat %d refused TERM: stop it as the user that started it\n", pid)
		return exitFailed
	}
	deadline := time.Now().Add(usageStopWait)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for writing(path, pid) {
		if time.Now().After(deadline) {
			fmt.Fprintf(a.Stderr, "laug · usage stop: cellstat %d is still up after %s (slow docker calls): %s usage "+
				"stop %s waits %s more\n", pid, usageStopWait, a.self(), file, usageStopWait)
			return exitFailed
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(a.Stderr, "laug · usage stop: stopped waiting: cellstat %d has TERM and may still write its "+
				"last line; %s usage stop %s waits again\n", pid, a.self(), file)
			return exitFailed
		case <-tick.C:
		}
	}
	fmt.Fprintf(a.Stdout, "laug · usage stop: cellstat %d stopped: %s in %s\n", pid, plural(lineCount(path), "line"),
		file)
	return exitDone
}

var pidRE = regexp.MustCompile(`^[0-9]+$`)

// recording reads path's pid file and says whether the cellstat it names is writing path.
func recording(path string) (int, bool) {
	b, err := os.ReadFile(path + ".pid")
	if err != nil {
		return 0, false
	}
	s := strings.TrimRight(string(b), "\n")
	if !pidRE.MatchString(s) {
		return 0, false
	}
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, writing(path, pid)
}

// writing is make cell-usage-stop's check: path and its pid file are there, pid is named cellstat, and path's lock is
// held. Any failure to take the lock counts as held, as flock -n failing did in the recipe.
func writing(path string, pid int) bool {
	if _, err := os.Stat(path + ".pid"); err != nil {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil || strings.TrimRight(string(comm), "\n") != cellstatName {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	// The lock is the open file's: closing f gives up one this call took.
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil
}

// lineCount is the number of newlines in path, as wc -l counts them; 0 when it cannot be read.
func lineCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte("\n"))
}

// listRecordings writes the files in cells/run/cellstat, each with the line that names it to verb: with running, only
// those a cellstat writes now. A bare stop or report prints them, so the line to type is there.
func (a *App) listRecordings(w io.Writer, verb string, running bool) {
	ents, _ := os.ReadDir(a.abs(usageDir))
	var names []string
	for _, e := range ents {
		if e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".pid") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	n := 0
	for _, name := range names {
		path := filepath.Join(a.abs(usageDir), name)
		pid, up := recording(path)
		if running && !up {
			continue
		}
		if n == 0 {
			fmt.Fprintf(w, "in %s:\n", usageDir)
		}
		n++
		about := plural(lineCount(path), "line")
		if up {
			about += fmt.Sprintf(", cellstat %d writing", pid)
		}
		fmt.Fprintf(w, "  %s usage %s %s   (%s)\n", a.self(), verb, shellWord(filepath.Join(usageDir, name)), about)
	}
	if n == 0 && running {
		fmt.Fprintf(w, "no cellstat writes a file in %s\n", usageDir)
	} else if n == 0 {
		fmt.Fprintf(w, "no file in %s\n", usageDir)
	}
}

// runUsageReport sums a file of lines per item and service, per Omes process, for the host and for cellstat, over a
// gate run's wave or the whole file, as make cell-usage-report did. Exit 0 printed; 1 when the file or the gate run
// cannot be read, or the window holds fewer than 2 lines; 2 bare, or on a flag, an argument or a value it refuses.
func runUsageReport(_ context.Context, a *App, args []string) int {
	f := a.newFlags("usage report", "<file> [-gate <run>] [-results gate/results] [-json]")
	gate := f.String("gate", "", "sum over this gate run's wave, its items naming the rows: a gate `id` in "+
		"-results, or the path to a run's directory; unset, the whole file")
	results := resultsFlag(f)
	jsonOut := f.Bool("json", false, "print one JSON object: window, items, omes, host and cellstat")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		fmt.Fprintln(a.Stderr, "laug · usage report: names no file: give it a file of lines a recording wrote")
		a.listRecordings(a.Stderr, "report", false)
		f.mistake(a.Stderr)
		return exitUsage
	case len(pos) > 1:
		return a.usageError(f, "takes one file, got %q", pos)
	case given(f, "results") && *gate == "":
		return a.refused("usage report", errors.New("-results is where -gate's id is: give -gate too"))
	}
	file, path := shellWord(pos[0]), a.abs(pos[0])
	dir, over := "", "the whole file"
	if *gate != "" {
		r, err := gateRun(*results, *gate)
		if err != nil {
			return a.refused("usage report", fmt.Errorf("-gate %v", err))
		}
		dir, over = a.abs(r.path), "gate "+shellWord(r.arg)+"'s wave"
	}
	var buf bytes.Buffer
	if err := cellstat.Report(path, dir, *jsonOut, &buf); err != nil {
		fmt.Fprintf(a.Stderr, "laug · usage report: %s over %s: %v\n", file, over, a.errShown(err))
		return exitFailed
	}
	var err error
	if *jsonOut {
		err = writeCompact(a.Stdout, buf.Bytes())
	} else {
		_, err = fmt.Fprintf(a.Stdout, "laug · usage report: %s over %s\n%s", file, over, buf.Bytes())
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · usage report: writing the report: %v\n", a.errShown(err))
		return exitFailed
	}
	return exitDone
}
