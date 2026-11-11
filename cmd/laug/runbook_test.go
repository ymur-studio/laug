package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// rbZombie is testRunbookZombie on Linux, where runbook_linux_test.go sets it, and nil elsewhere.
var rbZombie func(*testing.T)

// rbApp is an App in dir, a checkout as far as runbook check reads one, with PATH alone and extra in its environment.
func rbApp(dir string, extra ...string) *App {
	return &App{Root: dir, Environ: append([]string{"PATH=" + os.Getenv("PATH")}, extra...)}
}

// rbPage writes a runbook into dir's docs/runbooks and returns its path from dir.
func rbPage(t *testing.T, dir, name, body string) string {
	t.Helper()
	rel := runbookDir + "/" + name
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

// TestRunbook holds laug runbook check to its paragraph in doc.go: how a page's blocks are read, how lines match, how
// a block runs and is judged, the bare list and the refusals; and the committed runbooks to the rules a page keeps.
func TestRunbook(t *testing.T) {
	t.Run("parse", testRunbookParse)
	t.Run("match", testRunbookMatch)
	t.Run("check", testRunbookCheck)
	t.Run("refusals", testRunbookRefusals)
	t.Run("left running", testRunbookLeft)
	t.Run("a zombie left", func(t *testing.T) {
		if rbZombie == nil {
			t.Skip("needs Linux: the test makes itself a subreaper with prctl, which is Linux's alone")
		}
		rbZombie(t)
	})
	t.Run("a signal", testRunbookSignal)
	t.Run("bare", testRunbookBare)
	t.Run("the committed runbooks", testRunbookCommitted)
}

func testRunbookParse(t *testing.T) {
	page := strings.Join([]string{
		"# A runbook",
		"",
		"```sh",
		"echo one  # a comment",
		"```",
		"",
		"```text",
		"one",
		"```",
		"",
		"```sh needs=cell exit=3",
		"bin/laug pool serve -backend compose &",
		"```",
		"Prose between, so the next text fence is the page's own.",
		"```text",
		"not expected",
		"```",
		"1. A step:",
		"",
		"   ~~~~sh needs=lab",
		"   echo three",
		"     indented",
		"   ~~~~",
		"   ```text",
		"   three",
		"     indented",
		"   ```",
		"```yaml",
		"a: 1",
		"```",
		"```sh",
		"echo four",
		"```",
		"```text",
		"```",
	}, "\n") + "\n"
	got, err := parseRunbook(page)
	if err != nil {
		t.Fatal(err)
	}
	want := []rbBlock{
		{line: 3, cmds: []string{"echo one  # a comment"}, expect: []string{"one"}, expLine: 7},
		{line: 11, cmds: []string{"bin/laug pool serve -backend compose &"}, needs: "cell", exit: 3},
		{line: 20, cmds: []string{"echo three", "  indented"}, needs: "lab", expect: []string{"three", "  indented"},
			expLine: 24},
		{line: 31, cmds: []string{"echo four"}, expect: []string{}, expLine: 34},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%#v\nwant\n%#v", got, want)
	}
	if f := got[0].first(); f != "echo one" {
		t.Errorf("first command %q, want %q", f, "echo one")
	}
	for _, c := range []struct{ name, page, want string }{
		{"no command block", "# Prose\n\n```text\nx\n```\n", "no command block"},
		{"a fence not closed", "```sh\necho x\n", "line 1: a fence that is not closed"},
		{"an unknown mark", "x\n```sh needs=disk\necho x\n```\n", `line 2: the mark "needs=disk"`},
		{"a mark twice", "```sh exit=1 exit=2\nfalse\n```\n", `the mark "exit=2"`},
		{"needs twice", "```sh needs=cell needs=lab\nx\n```\n", `the mark "needs=lab"`},
		{"an exit out of range", "```sh exit=256\nx\n```\n", `the mark "exit=256"`},
		{"an exit not a number", "```sh exit=01\nx\n```\n", `the mark "exit=01"`},
		{"a mark on the expected lines", "```sh\necho x\n```\n```text needs=cell\nx\n```\n",
			"line 4: the expected lines' block takes no mark"},
		{"a command block with no command", "```sh\n\n```\n", "line 1: a command block with no command"},
	} {
		if _, err := parseRunbook(c.page); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one holding %q", c.name, err, c.want)
		}
	}
}

func testRunbookMatch(t *testing.T) {
	for _, c := range []struct {
		name      string
		want, got []string
		bad, from int
	}{
		{"equal", []string{"a", "b"}, []string{"a", "b"}, -1, 0},
		{"trailing blanks", []string{"a  "}, []string{"a\t \r"}, -1, 0},
		{"... in a line", []string{"laug · lease: ... for laug until ..."},
			[]string{"laug · lease: 3fc986a7 for laug until 2026-10-24 10:00:00 UTC"}, -1, 0},
		{"... matches nothing too", []string{"a...b...c"}, []string{"abc"}, -1, 0},
		{"... line alone, many", []string{"first", "...", "last"}, []string{"first", "x", "y", "last"}, -1, 0},
		{"... line alone, none", []string{"first", "...", "last"}, []string{"first", "last"}, -1, 0},
		{"... line at the end", []string{"first", "..."}, []string{"first", "x"}, -1, 0},
		{"... line, a later match", []string{"...", "x", "z"}, []string{"x", "y", "x", "z"}, -1, 0},
		{"none expected, none printed", []string{}, []string{}, -1, 0},
		{"a line that differs", []string{"a", "b"}, []string{"a", "c"}, 1, 1},
		{"a prefix that differs", []string{"x...", "b"}, []string{"a", "b"}, 0, 0},
		{"a suffix overlapping a prefix", []string{"ab...ba"}, []string{"aba"}, 0, 0},
		{"lines past the expected", []string{"a"}, []string{"a", "b", "c"}, 1, 1},
		{"printed, none expected", []string{}, []string{"a"}, 0, 0},
		{"expected, none printed", []string{"a"}, []string{}, 0, 0},
		{"after ..., the first it was held to", []string{"a", "...", "z"}, []string{"a", "b", "c"}, 2, 1},
	} {
		if bad, from := rbMatch(c.want, c.got); bad != c.bad || (bad >= 0 && from != c.from) {
			t.Errorf("%s: bad %d from %d, want %d from %d", c.name, bad, from, c.bad, c.from)
		}
	}
}

func testRunbookCheck(t *testing.T) {
	dir := t.TempDir()
	page := rbPage(t, dir, "2-try.md", strings.Join([]string{
		"# Try",
		"```sh",
		"pwd",
		`echo "$RB_SEEN" >&2`,
		"echo out",
		"```",
		"```text",
		dir,
		"seen",
		"out",
		"```",
		"```sh exit=3",
		"echo before",
		"exit 3",
		"echo never",
		"```",
		"```sh needs=cell",
		"touch cell-ran",
		"```",
		"```sh needs=lab",
		"touch lab-ran",
		"```",
		"```sh",
		"false",
		"echo never",
		"```",
		"```sh",
		"printf 'a\\nb\\nc\\n'",
		"```",
		"```text",
		"a",
		"x",
		"...",
		"```",
		"```sh",
		"printf 'a\\nb\\n'",
		"```",
		"```text",
		"a",
		"```",
	}, "\n")+"\n")
	code, out, errOut := runApp(t, rbApp(dir, "RB_SEEN=seen"), "runbook", "check", page)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if code != exitFailed || errOut != "" {
		t.Errorf("exit %d, want 1; stderr %q\n%s", code, errOut, out)
	}
	at := func(n int) string { return page + ":" + strconv.Itoa(n) }
	for _, want := range []string{
		"laug · runbook check: " + page + ", 7 blocks: 5 to run, 1 needs a cell (-cell runs it), 1 needs the lab host " +
			"(-lab runs it); Ctrl-C stops it",
		"ok       " + at(2) + "  pwd, ",
		"ok       " + at(12) + "  echo before, ",
		"skipped  " + at(17) + "  touch cell-ran: needs a cell, -cell runs it",
		"skipped  " + at(20) + "  touch lab-ran: needs the lab host, -lab runs it",
		"FAILED   " + at(23) + "  false, ",
		"         exit 1, want 0",
		"         it printed nothing",
		"FAILED   " + at(27) + "  printf 'a\\nb\\nc\\n', ",
		"         expected line 2 (" + at(32) + ") matched none of its lines from its line 2 on:",
		"         - x",
		"         + b",
		"         + c",
		"FAILED   " + at(35) + "  printf 'a\\nb\\n', ",
		"         it printed 1 line past the expected ones (" + at(38) + "), from its line 2:",
		"         + b",
		"laug · runbook check: 2 of 5 blocks held, 3 failed: " + at(23) + ", " + at(27) + ", " + at(35) + "; 2 skipped",
	} {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, want) }) {
			t.Errorf("no line starting %q:\n%s", want, out)
		}
	}
	for _, f := range []string{"cell-ran", "lab-ran"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s: a block marked needs ran without its flag", f)
		}
	}
	code, out, _ = runApp(t, rbApp(dir), "runbook", "check", "-cell", "-lab", page)
	if code != exitFailed || !strings.Contains(out, "ok       "+at(17)) || !strings.Contains(out, "ok       "+at(20)) ||
		!strings.Contains(out, "7 blocks: 7 to run; Ctrl-C stops it") {
		t.Errorf("-cell -lab: exit %d\n%s", code, out)
	}
	for _, f := range []string{"cell-ran", "lab-ran"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("-cell -lab: %s: %v", f, err)
		}
	}
	held := rbPage(t, dir, "3-held.md", "```sh\necho held\n```\n\n```text\nheld\n```\n")
	code, out, _ = runApp(t, rbApp(dir), "runbook", "check", held, "docs/runbooks/../runbooks/3-held.md")
	if code != exitDone || !strings.HasPrefix(out, "laug · runbook check: 2 runbooks, 2 blocks: 2 to run; ") ||
		!strings.HasSuffix(out, "\nlaug · runbook check: 2 of 2 blocks held\n") {
		t.Errorf("two runbooks that hold: exit %d\n%s", code, out)
	}
}

func testRunbookRefusals(t *testing.T) {
	dir := t.TempDir()
	good := rbPage(t, dir, "1-good.md", "```sh\ntouch ran\n```\n")
	bad := rbPage(t, dir, "2-bad.md", "```sh needs=disk\ntouch ran\n```\n")
	empty := t.TempDir()
	for _, c := range []struct {
		name string
		a    *App
		args []string
		err  string // stderr's first line
	}{
		{"outside a checkout", &App{}, []string{good}, "laug · runbook check: runs a runbook's blocks from the " +
			"checkout's root, so it needs a laug checkout: no directory above this one holds go.mod and gate/run.sh"},
		{"no such file", rbApp(dir), []string{good, "docs/runbooks/9-none.md"},
			"laug · runbook check: docs/runbooks/9-none.md: no such file or directory"},
		{"not a runbook", rbApp(dir), []string{good, bad}, "laug · runbook check: " + bad + ": not a runbook: line 1: " +
			`the mark "needs=disk": a command block takes needs=cell or needs=lab, and exit=<0 to 255>, each once`},
		{"-cell with no file", rbApp(dir), []string{"-cell"},
			"laug · runbook check: -cell and -lab choose the blocks a runbook runs, so they take a <file>"},
		{"a flag it does not take", rbApp(dir), []string{"-json", good},
			"laug · runbook check: no flag -json"},
		{"no bash", &App{Root: dir, Environ: []string{"PATH=" + empty}}, []string{good},
			"laug · runbook check: no bash on PATH, which runs each block"},
	} {
		code, out, errOut := runApp(t, c.a, append([]string{"runbook", "check"}, c.args...)...)
		if code != exitUsage || out != "" || firstLine(errOut) != c.err {
			t.Errorf("%s: exit %d, want 2; stdout %q; stderr:\n%s", c.name, code, out, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Error("a refusal ran a block")
	}
}

func testRunbookLeft(t *testing.T) {
	saved := rbWait
	rbWait = 200 * time.Millisecond
	t.Cleanup(func() { rbWait = saved })
	dir := t.TempDir()
	page := rbPage(t, dir, "4-left.md", "```sh\nsleep 30 &\necho $! > left.pid\n```\n")
	code, out, _ := runApp(t, rbApp(dir), "runbook", "check", page)
	if code != exitFailed || !strings.Contains(out, "\n         it left a process running once bash had ended: "+
		"SIGTERM to its process group; a block stops what it starts\n") {
		t.Errorf("exit %d\n%s", code, out)
	}
	pid := rbPid(t, dir, "left.pid")
	// Once stopped, the process has no /proc entry, or is a zombie init has yet to reap.
	for end := time.Now().Add(2 * time.Second); rbRuns(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("the process the block left, pid %d, still runs", pid)
			break
		}
	}
}

// rbRuns says whether pid runs: it has a /proc entry, and its state is not Z, or a thread of it reads a state other
// than Z, as groupRuns reads one.
func rbRuns(pid int) bool {
	p := strconv.Itoa(pid)
	f := procFields(p)
	return len(f) > 0 && (f[0] != "Z" || threadRuns(p))
}

// rbPid reads the pid a block wrote into dir's file name.
func rbPid(t *testing.T, dir, name string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func testRunbookSignal(t *testing.T) {
	dir := t.TempDir()
	page := rbPage(t, dir, "5-long.md", "```sh\nsleep 30\n```\n\n```sh\ntouch ran\n```\n")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	a := rbApp(dir)
	var out, errOut strings.Builder
	a.Stdout, a.Stderr = &out, &errOut
	t0 := time.Now()
	code := run(ctx, a, []string{"runbook", "check", page})
	if code != exitFailed || time.Since(t0) > 10*time.Second ||
		!strings.Contains(out.String(), "         a signal to laug stopped it: SIGTERM to its process group\n") ||
		!strings.Contains(out.String(), "laug · runbook check: a signal stopped "+page+":1; 0 of 2 blocks held before it") {
		t.Errorf("exit %d after %s\n%s%s", code, time.Since(t0), out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Error("a block ran after the signal")
	}
}

func testRunbookBare(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runApp(t, rbApp(dir), "runbook", "check")
	if code != exitDone || out != "laug · runbook check: no runbook in docs/runbooks\n" || errOut != "" {
		t.Errorf("no runbook: exit %d\n%s%s", code, out, errOut)
	}
	rbPage(t, dir, "1-a.md", "```sh\ntouch ran\n```\n```sh needs=cell\nx\n```\n```sh needs=cell\nx\n```\n")
	rbPage(t, dir, "2-bb.md", "```sh needs=lab\nx\n```\n")
	rbPage(t, dir, "3-c.md", "No block.\n")
	rbPage(t, dir, "notes.txt", "```sh\nx\n```\n")
	code, out, errOut = runApp(t, rbApp(dir), "runbook", "check")
	want := "laug · runbook check: 3 runbooks in docs/runbooks; name one or more to run its blocks that need no cell " +
		"and no lab host\n" +
		"docs/runbooks/1-a.md   3 blocks: 1 offline, 2 need a cell\n" +
		"docs/runbooks/2-bb.md  1 block: 0 offline, 1 needs the lab host\n" +
		"docs/runbooks/3-c.md   not a runbook: no command block: a runbook holds at least one block fenced as sh\n"
	if code != exitDone || out != want || errOut != "" {
		t.Errorf("exit %d\n%s%s\nwant\n%s", code, out, errOut, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Error("bare, it ran a block")
	}
}

// The committed runbooks: each a page check reads, each listed in check/docs.txt, so the page rules hold it too, and
// runbook 1 one offline block, the long walk, of which README.md's block under readmeHeading is a subset: each of
// README's lines is one of runbook 1's, comments and all, in runbook 1's order. load/testdata/harness-test.sh runs
// runbook 1's lines and holds the two blocks the same way.
func testRunbookCommitted(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pages, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(runbookDir), "*.md"))
	if len(pages) == 0 {
		t.Fatalf("no runbook in %s", runbookDir)
	}
	list, err := os.ReadFile(filepath.Join(root, "check", "docs.txt"))
	if err != nil {
		t.Fatal(err)
	}
	listed := strings.Fields(string(list))
	ones := 0
	for _, p := range pages {
		name := runbookDir + "/" + filepath.Base(p)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := parseRunbook(string(b))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !slices.Contains(listed, name) {
			t.Errorf("%s is not listed in check/docs.txt", name)
		}
		if strings.HasPrefix(filepath.Base(p), "1-") {
			ones++
			if len(blocks) != 1 || blocks[0].needs != "" || blocks[0].exit != 0 {
				t.Errorf("%s: runbook 1 holds one offline block, the long walk; it holds %d", name, len(blocks))
			} else if l := notInOrder(readmeBlock(t, root), blocks[0].cmds); l != "" {
				t.Errorf("README.md's block under %q: %q is not a line of %s's block, in its order", readmeHeading, l, name)
			}
		}
	}
	if ones != 1 {
		t.Errorf("%d runbooks named 1-*.md; runbook 1 is one page", ones)
	}
}

// readmeHeading is the heading README.md's command block sits under; load/testdata/harness-test.sh finds the block by
// the same line.
const readmeHeading = "## Try it in five minutes, no Docker"

// readmeBlock is README.md's command block: the lines of the first sh fence after readmeHeading. The test fails when the
// heading or the fence is missing, or the block is empty.
func readmeBlock(t *testing.T, root string) []string {
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	i := slices.Index(lines, readmeHeading)
	if i < 0 {
		t.Fatalf("README.md: no line %q", readmeHeading)
	}
	rest := lines[i+1:]
	j := slices.Index(rest, "```sh")
	if j < 0 {
		t.Fatalf("README.md: no sh fence after %q", readmeHeading)
	}
	k := slices.Index(rest[j+1:], "```")
	if k <= 0 {
		t.Fatalf("README.md: the sh fence after %q is empty or not closed", readmeHeading)
	}
	return rest[j+1 : j+1+k]
}

// notInOrder is the first line of short that is not found in long at or after the line that matched the one before it:
// "" when short's lines are long's, in long's order.
func notInOrder(short, long []string) string {
	j := 0
	for _, l := range short {
		for j < len(long) && long[j] != l {
			j++
		}
		if j == len(long) {
			return l
		}
		j++
	}
	return ""
}
