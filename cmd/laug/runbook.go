package main

// runbook: laug runbook check runs a runbook's command blocks, the ones that need no cell and no lab host unless a flag
// says, and holds each block's lines to the expected lines under it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// runbookDir holds the runbooks, a page a task, under the checkout's root.
const runbookDir = "docs/runbooks"

// rbWait is how long check waits, once a block's bash has ended, for the pipe a process the block left behind holds,
// and then for what the block's group still runs to stop after SIGTERM. A test shortens it.
var rbWait = 5 * time.Second

// rbShown is how many of a block's lines a failure shows.
const rbShown = 8

// rbBlock is one command block of a runbook, with the expected lines under it.
type rbBlock struct {
	line    int      // the opening fence's line, from 1
	cmds    []string // the block's lines
	needs   string   // "", "cell" or "lab"
	exit    int      // the exit the block should end with: 0 unless marked exit=<n>
	expect  []string // the lines of the text block right after it; nil when none follows
	expLine int      // that text block's opening fence's line
}

// rbFence is a fence's line: 0 to 3 spaces, 3 or more backticks or tildes, then the info string.
var rbFence = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})(.*)$")

// parseRunbook reads a runbook's command blocks: a fence whose info string's first word is sh, its other words the
// block's marks, and the text block right after it, with blank lines alone between, as its expected lines. Every other
// block is the page's own. A fence not closed, a mark it does not take, a command block with no command and a page
// with no command block are errors, each naming its line.
func parseRunbook(body string) ([]rbBlock, error) {
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), "\n")
	var blocks []rbBlock
	closed := -1 // the index of the line that closed the last command block
	for i := 0; i < len(lines); i++ {
		m := rbFence.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		indent, fence, info := len(m[1]), m[2], strings.Fields(m[3])
		j := i + 1
		for ; j < len(lines); j++ {
			c := rbFence.FindStringSubmatch(lines[j])
			if c != nil && c[2][0] == fence[0] && len(c[2]) >= len(fence) && strings.TrimSpace(c[3]) == "" {
				break
			}
		}
		if j == len(lines) {
			return nil, fmt.Errorf("line %d: a fence that is not closed", i+1)
		}
		body := make([]string, 0, j-i-1)
		for _, l := range lines[i+1 : j] {
			n := 0
			for n < indent && n < len(l) && l[n] == ' ' {
				n++
			}
			body = append(body, l[n:])
		}
		lang := ""
		if len(info) > 0 {
			lang = info[0]
		}
		switch {
		case lang == "sh":
			b := rbBlock{line: i + 1, cmds: body}
			if err := b.mark(info[1:]); err != nil {
				return nil, fmt.Errorf("line %d: %v", i+1, err)
			}
			if strings.TrimSpace(strings.Join(body, "")) == "" {
				return nil, fmt.Errorf("line %d: a command block with no command", i+1)
			}
			blocks = append(blocks, b)
			closed = j
		case lang == "text" && closed >= 0 && blank(lines[closed+1:i]):
			if len(info) > 1 {
				return nil, fmt.Errorf("line %d: the expected lines' block takes no mark, %q", i+1, info[1])
			}
			b := &blocks[len(blocks)-1]
			b.expect, b.expLine = body, i+1
			closed = -1
		default:
			closed = -1
		}
		i = j
	}
	if len(blocks) == 0 {
		return nil, errors.New("no command block: a runbook holds at least one block fenced as sh")
	}
	return blocks, nil
}

func blank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// mark sets b's needs and exit from the words after sh.
func (b *rbBlock) mark(words []string) error {
	exitSet := false
	for _, w := range words {
		k, v, _ := strings.Cut(w, "=")
		switch {
		case k == "needs" && (v == "cell" || v == "lab") && b.needs == "":
			b.needs = v
			continue
		case k == "exit" && !exitSet:
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 255 && strconv.Itoa(n) == v {
				b.exit, exitSet = n, true
				continue
			}
		}
		return fmt.Errorf("the mark %q: a command block takes needs=cell or needs=lab, and exit=<0 to 255>, "+
			"each once", w)
	}
	return nil
}

// first is the block's first command, less its comment, for its line in the report.
func (b rbBlock) first() string {
	for _, l := range b.cmds {
		c := strings.TrimSpace(uncommented(l))
		if c == "" {
			continue
		}
		if utf8.RuneCountInString(c) > 64 {
			c = string([]rune(c)[:61]) + "..."
		}
		return c
	}
	return ""
}

// uncommented cuts a line at a # that starts it or follows a blank.
func uncommented(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
	}
	return s
}

// needsText is what a block that needs a cell or the lab host needs, and the flag that runs it.
func needsText(needs string) (what, flag string) {
	if needs == "lab" {
		return "the lab host", "-lab"
	}
	return "a cell", "-cell"
}

// rbLine says whether got matches want, where each ... in want matches any text, none included. Trailing blanks
// count on neither side.
func rbLine(want, got string) bool {
	want, got = strings.TrimRight(want, " \t\r"), strings.TrimRight(got, " \t\r")
	parts := strings.Split(want, "...")
	if len(parts) == 1 {
		return want == got
	}
	rest, ok := strings.CutPrefix(got, parts[0])
	if !ok {
		return false
	}
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// rbMatch holds got, a block's lines, to want, line for line, where a line of ... alone matches any number of lines.
// bad is -1 when they match; else the index in want of the first line that matched nothing, with from the first of
// got's lines it was held to, or len(want) when got has lines past the last of want's, from from on.
func rbMatch(want, got []string) (bad, from int) {
	reach := make([]bool, len(got)+1) // reach[p]: want's lines so far can end with got's first p lines
	reach[0] = true
	firstOf := func(r []bool) int {
		for p, ok := range r {
			if ok {
				return p
			}
		}
		return -1
	}
	for k, w := range want {
		next := make([]bool, len(got)+1)
		if strings.TrimSpace(w) == "..." {
			for p := firstOf(reach); p <= len(got); p++ {
				next[p] = true
			}
		} else {
			for p := 0; p < len(got); p++ {
				next[p+1] = reach[p] && rbLine(w, got[p])
			}
		}
		if firstOf(next) < 0 {
			return k, firstOf(reach)
		}
		reach = next
	}
	if reach[len(got)] {
		return -1, 0
	}
	last := 0
	for p, ok := range reach {
		if ok {
			last = p
		}
	}
	return len(want), last
}

// rbLines splits a block's output into its lines.
func rbLines(out []byte) []string {
	s := strings.TrimSuffix(string(out), "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

// rbRun is how one block ran.
type rbRun struct {
	code int    // bash's exit, 128+n when signal n ended it
	out  []byte // its stdout and stderr, in one pipe
	left bool   // its group still ran once bash had ended, and got SIGTERM
	stop bool   // a signal to laug stopped it
	err  error  // bash could not start
}

// runBlock runs b's lines as bash -e -o pipefail -c, from the checkout's root, with the App's environment whole, stdin
// from /dev/null, and in a process group of its own. A signal to laug, and what the group still runs once bash has
// ended (groupRuns: a zombie runs nothing), get SIGTERM there, then SIGKILL rbWait on.
func (a *App) runBlock(ctx context.Context, bash string, b rbBlock) rbRun {
	var out bytes.Buffer
	cmd := exec.Command(bash, "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", strings.Join(b.cmds, "\n")+"\n")
	cmd.Dir = a.Root
	cmd.Env = append([]string{}, a.Environ...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = rbWait
	if err := cmd.Start(); err != nil {
		return rbRun{err: err}
	}
	pgid := cmd.Process.Pid
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	var r rbRun
	select {
	case <-done:
	case <-ctx.Done():
		r.stop = true
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(rbWait):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
		}
	}
	if groupRuns(pgid) {
		r.left = true
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		for end := time.Now().Add(rbWait); time.Now().Before(end) && groupRuns(pgid); {
			time.Sleep(50 * time.Millisecond)
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	r.code = cmd.ProcessState.ExitCode()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		r.code = 128 + int(ws.Signal())
	}
	r.out = out.Bytes()
	return r
}

// groupRuns says whether process group pgid still runs a process: one whose /proc entry names pgid as its group and
// whose state is not Z. A member that reads Z still runs while a thread of it reads another state (threadRuns). A
// zombie has ended and waits only for its parent, or PID 1 for an orphan, to reap it. But /proc/<pid>/stat gives the
// main thread's state, and a process whose main thread alone has exited, while its other threads run, reads Z too.
// kill(-pgid, 0) still answers for a zombie, so under a PID 1 that reaps late, kill alone would fail a block that
// stopped all it started. The harness's gone (load/testdata/harness-test.sh) reads one pid by its state alone: the
// pids it reads, its fakes' bash, sleep and timeout, run one thread each.
// kill(-pgid, 0) goes first, so a group with no process left, the usual case, costs no read of /proc; with no /proc
// to read, its answer stands.
func groupRuns(pgid int) bool {
	if syscall.Kill(-pgid, 0) != nil {
		return false
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	g := strconv.Itoa(pgid)
	for _, e := range ents {
		if n := e.Name(); n[0] >= '0' && n[0] <= '9' {
			if f := procFields(n); len(f) > 2 && f[2] == g && (f[0] != "Z" || threadRuns(n)) {
				return true
			}
		}
	}
	return false
}

// threadRuns says whether a thread of pid, an entry of /proc/<pid>/task, reads a state other than Z: so pid, whose
// main thread may have exited alone, still runs. false when no entry can be read.
func threadRuns(pid string) bool {
	ts, _ := os.ReadDir("/proc/" + pid + "/task")
	for _, t := range ts {
		if f := procFields(pid + "/task/" + t.Name()); len(f) > 0 && f[0] != "Z" {
			return true
		}
	}
	return false
}

// procFields is /proc/<pid>/stat from its third field on: the state, the parent's pid, the process group, and the
// rest. The command name, the second field, can hold spaces and parentheses, so the fields are counted from the last
// ") ". nil when pid has no /proc entry. A pid of <pid>/task/<tid> reads that one thread's stat.
func procFields(pid string) []string {
	b, err := os.ReadFile("/proc/" + pid + "/stat")
	i := bytes.LastIndex(b, []byte(") "))
	if err != nil || i < 0 {
		return nil
	}
	return strings.Fields(string(b[i+2:]))
}

// rbBook is one runbook as check reads it: its name as given and its blocks.
type rbBook struct {
	name   string
	blocks []rbBlock
}

func runRunbookCheck(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("runbook check", "[-cell] [-lab] <file>...")
	cell := f.Bool("cell", false, "also run the blocks marked needs=cell, which need Docker and cells on this host")
	lab := f.Bool("lab", false, "also run the blocks marked needs=lab, which need the lab host")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if a.Root == "" {
		fmt.Fprintln(a.Stderr, "laug · runbook check: runs a runbook's blocks from the checkout's root, so it needs a "+
			"laug checkout: no directory above this one holds go.mod and gate/run.sh")
		return exitUsage
	}
	if len(pos) == 0 {
		if *cell || *lab {
			return a.usageError(f, "-cell and -lab choose the blocks a runbook runs, so they take a <file>")
		}
		a.listRunbooks()
		return exitDone
	}
	var books []rbBook
	for _, p := range pos {
		body, err := os.ReadFile(a.abs(p))
		if err != nil {
			return a.refused("runbook check", fmt.Errorf("%s: %w", p, pathErr(err)))
		}
		blocks, err := parseRunbook(string(body))
		if err != nil {
			return a.refused("runbook check", fmt.Errorf("%s: not a runbook: %w", p, err))
		}
		books = append(books, rbBook{p, blocks})
	}
	bash := a.lookPath("bash")
	if bash == "" {
		fmt.Fprintln(a.Stderr, "laug · runbook check: no bash on PATH, which runs each block")
		return exitUsage
	}
	runs := func(b rbBlock) bool { return b.needs == "" || b.needs == "cell" && *cell || b.needs == "lab" && *lab }
	total, toRun, skip := 0, 0, map[string]int{}
	for _, bk := range books {
		for _, b := range bk.blocks {
			total++
			if runs(b) {
				toRun++
			} else {
				skip[b.needs]++
			}
		}
	}
	what := books[0].name
	if len(books) > 1 {
		what = plural(len(books), "runbook")
	}
	counts := []string{fmt.Sprintf("%d to run", toRun)}
	for _, n := range []string{"cell", "lab"} {
		if skip[n] > 0 {
			need, flag := needsText(n)
			counts = append(counts, fmt.Sprintf("%d %s %s (%s runs %s)", skip[n], agree(skip[n], "needs", "need"),
				need, flag, agree(skip[n], "it", "them")))
		}
	}
	fmt.Fprintf(a.Stdout, "laug · runbook check: %s, %s: %s; Ctrl-C stops it\n", what, plural(total, "block"),
		strings.Join(counts, ", "))
	held, skipped := 0, skip["cell"]+skip["lab"]
	var failed []string
	for _, bk := range books {
		for _, b := range bk.blocks {
			at := fmt.Sprintf("%s:%d", bk.name, b.line)
			if !runs(b) {
				need, flag := needsText(b.needs)
				fmt.Fprintf(a.Stdout, "skipped  %s  %s: needs %s, %s runs it\n", at, b.first(), need, flag)
				continue
			}
			t0 := time.Now()
			r := a.runBlock(ctx, bash, b)
			secs := time.Since(t0).Seconds()
			why := judgeBlock(bk.name, b, r)
			if len(why) == 0 {
				held++
				fmt.Fprintf(a.Stdout, "ok       %s  %s, %.1f s\n", at, b.first(), secs)
				continue
			}
			failed = append(failed, at)
			fmt.Fprintf(a.Stdout, "FAILED   %s  %s, %.1f s\n", at, b.first(), secs)
			for _, l := range why {
				fmt.Fprintf(a.Stdout, "         %s\n", l)
			}
			if r.stop {
				fmt.Fprintf(a.Stdout, "laug · runbook check: a signal stopped %s; %d of %d blocks held before it\n",
					at, held, toRun)
				return exitFailed
			}
		}
	}
	last := fmt.Sprintf("laug · runbook check: %d of %d blocks held", held, toRun)
	if len(failed) > 0 {
		last += fmt.Sprintf(", %d failed: %s", len(failed), strings.Join(failed, ", "))
	}
	if skipped > 0 {
		last += fmt.Sprintf("; %d skipped", skipped)
	}
	fmt.Fprintln(a.Stdout, last)
	if len(failed) > 0 {
		return exitFailed
	}
	return exitDone
}

// judgeBlock says why block b, of the runbook called name, did not hold when it ran as r: nothing when it held.
func judgeBlock(name string, b rbBlock, r rbRun) []string {
	if r.err != nil {
		return []string{fmt.Sprintf("bash could not start: %v", r.err)}
	}
	got := rbLines(r.out)
	var why []string
	if r.stop {
		why = append(why, "a signal to laug stopped it: SIGTERM to its process group")
	}
	if r.left {
		why = append(why, "it left a process running once bash had ended: SIGTERM to its process group; a block "+
			"stops what it starts")
	}
	if r.code != b.exit {
		why = append(why, fmt.Sprintf("exit %d, want %d", r.code, b.exit))
	}
	if b.expect != nil {
		switch bad, from := rbMatch(b.expect, got); {
		case bad == len(b.expect):
			why = append(why, fmt.Sprintf("it printed %s past the expected ones (%s:%d), from its line %d:",
				plural(len(got)-from, "line"), name, b.expLine, from+1))
			return append(why, shown(got[from:])...)
		case bad >= 0:
			why = append(why, fmt.Sprintf("expected line %d (%s:%d) matched none of its lines from its line %d on:",
				bad+1, name, b.expLine+1+bad, from+1), "- "+b.expect[bad])
			return append(why, shown(got[from:])...)
		}
	}
	switch tail := got[max(0, len(got)-rbShown):]; {
	case len(why) == 0:
	case len(tail) == 0:
		why = append(why, "it printed nothing")
	default:
		why = append(why, fmt.Sprintf("its last %s:", plural(len(tail), "line")))
		why = append(why, shown(tail)...)
	}
	return why
}

// shown is a failure's view of a block's lines: rbShown at most, each after "+ ".
func shown(lines []string) []string {
	var out []string
	for i, l := range lines {
		if i == rbShown {
			out = append(out, fmt.Sprintf("+ ... %s more", plural(len(lines)-rbShown, "line")))
			break
		}
		out = append(out, "+ "+l)
	}
	return out
}

// agree is one or many by n.
func agree(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// listRunbooks prints the runbooks in docs/runbooks, each with its blocks by what they need, and runs nothing.
func (a *App) listRunbooks() {
	paths, _ := filepath.Glob(filepath.Join(a.Root, filepath.FromSlash(runbookDir), "*.md"))
	if len(paths) == 0 {
		fmt.Fprintf(a.Stdout, "laug · runbook check: no runbook in %s\n", runbookDir)
		return
	}
	fmt.Fprintf(a.Stdout, "laug · runbook check: %s in %s; name one or more to run its blocks that need no cell and "+
		"no lab host\n", plural(len(paths), "runbook"), runbookDir)
	width := 0
	for _, p := range paths {
		width = max(width, len(runbookDir)+1+len(filepath.Base(p)))
	}
	for _, p := range paths {
		name := runbookDir + "/" + filepath.Base(p)
		fmt.Fprintf(a.Stdout, "%-*s  %s\n", width, name, runbookSummary(p))
	}
}

// runbookSummary is one runbook's row in the list: its blocks by what they need, or why it is not a runbook.
func runbookSummary(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("not read: %v", pathErr(err))
	}
	blocks, err := parseRunbook(string(body))
	if err != nil {
		return "not a runbook: " + err.Error()
	}
	n := map[string]int{}
	for _, b := range blocks {
		n[b.needs]++
	}
	parts := []string{fmt.Sprintf("%d offline", n[""])}
	for _, k := range []string{"cell", "lab"} {
		if n[k] > 0 {
			need, _ := needsText(k)
			parts = append(parts, fmt.Sprintf("%d %s %s", n[k], agree(n[k], "needs", "need"), need))
		}
	}
	return plural(len(blocks), "block") + ": " + strings.Join(parts, ", ")
}

// pathErr is err less the path a *fs.PathError names, which the line names itself.
func pathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
