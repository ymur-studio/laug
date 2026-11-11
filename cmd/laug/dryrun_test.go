package main

// The goldens in testdata/dry-run: what make -n printed for the daily path's, the pool's, the gate's, the host's, the
// cell's, the stack's, the HealthCheck workflow's and the orchestrator's targets, and the tests that hold laug to them.
// testdata/dry-run/README.md says how they were captured.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// golden is one file of testdata/dry-run: its header, then make -n's output.
type golden struct {
	file    string
	make    []string          // the make variables and target after "make --no-print-directory -n"
	laug    []string          // the laug command it stands for, after "laug"
	lease   bool              // .last-lease.json held testdata/dry-run/lease.json
	docker  string            // "<project> <image>": docker ps found that project's temporal-history running image
	files   map[string]string // files the checkout held, path to content
	passes  []string          // NAME=value: what make passed unnamed from its command line, which the script reads
	adds    []string          // NAME=value: a knob laug sets that make's recipe did not, written after passes
	compare []string          // how laug is held to it: line, contains, resolved, request or refused, and arguments
	moved   []string          // <from> <to>: the directory make's recipe ran in, and where that package is now
	now     string            // laug's -dry-run line where it no longer matches make's, as -update wrote it
	body    string
}

// update is -update: go test ./cmd/laug -run TestDryRunGoldens -update writes laug's -dry-run line into each golden
// whose line no longer matches make's, as its now header, and drops the header where it matches again. make's output
// stays as it was captured: the goldens' make targets have left make.
var update = flag.Bool("update", false, "write laug's -dry-run line into each golden it no longer matches")

// dryRunClock is the time the goldens' default run id, 20261022-120000, stamps.
var dryRunClock = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func loadGoldens(t *testing.T) []golden {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "dry-run", "*.txt"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no goldens in testdata/dry-run: %v", err)
	}
	var gs []golden
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		g := golden{file: filepath.Base(p), files: map[string]string{}}
		rest := string(b)
		for strings.HasPrefix(rest, "# ") {
			line, after, _ := strings.Cut(rest, "\n")
			rest = after
			key, val, _ := strings.Cut(strings.TrimPrefix(line, "# "), ": ")
			switch key {
			case "make":
				f := strings.Fields(val)
				if len(f) < 4 || strings.Join(f[:3], " ") != "make --no-print-directory -n" {
					t.Errorf("%s: make: %q is not make --no-print-directory -n <target>", g.file, val)
				} else {
					g.make = f[3:]
				}
			case "laug":
				g.laug = strings.Fields(strings.TrimPrefix(val, "laug "))
			case "lease":
				g.lease = val == "present"
			case "docker":
				g.docker = val
			case "file":
				path, content, _ := strings.Cut(val, " ")
				g.files[path] = content
			case "passes":
				g.passes = strings.Fields(val)
			case "adds":
				g.adds = strings.Fields(val)
			case "compare":
				g.compare = compareFields(val)
			case "moved":
				if g.moved = strings.Fields(val); len(g.moved) != 2 {
					t.Errorf("%s: moved: %q is not <from> <to>", g.file, val)
				}
			case "now":
				g.now = val
			default:
				t.Errorf("%s: header %q", g.file, line)
			}
		}
		g.body = rest
		if g.make == nil || len(g.laug) == 0 || len(g.compare) == 0 {
			t.Errorf("%s: a golden names its make line, its laug line and how it compares", g.file)
		}
		gs = append(gs, g)
	}
	return gs
}

// absPath is an absolute path in make's output: a / that starts a word, a quoted string or a value.
var absPath = regexp.MustCompile(`(^|[\s'"=(])(/[A-Za-z0-9._-][^\s'"]*)`)

// TestGoldensClean: no golden holds an absolute path, the checkout's or any other, or the login name of whoever runs
// the test; every header is whole, and a now header sits only on a golden that prints a line; a knob it says make
// passed unnamed was on make's command line, and is a knob; and the line expect takes from make's output can be found
// there.
func TestGoldensClean(t *testing.T) {
	names := map[string]bool{os.Getenv("USER"): true, os.Getenv("LOGNAME"): true}
	if u, err := user.Current(); err == nil {
		names[u.Username] = true
	}
	for _, g := range loadGoldens(t) {
		for _, m := range absPath.FindAllStringSubmatch(g.body+"\n"+g.now, -1) {
			t.Errorf("%s: an absolute path, %s; the checkout is written $ROOT", g.file, m[2])
		}
		for n := range names {
			if len(n) >= 3 && n != "laug" && n != "lab" &&
				regexp.MustCompile(`\b`+regexp.QuoteMeta(n)+`\b`).MatchString(g.body+strings.Join(g.make, " ")+g.now) {
				t.Errorf("%s: holds the login name of the user running this test", g.file)
			}
		}
		if strings.Contains(g.body, "Entering directory") {
			t.Errorf("%s: make printed its directory; capture with --no-print-directory", g.file)
		}
		if g.now != "" && (g.compare[0] == "refused" || g.compare[0] == "request") {
			t.Errorf("%s: now: a %s golden prints no -dry-run line to hold", g.file, g.compare[0])
		}
		last := -1
		for i, p := range slices.Concat(g.passes, g.adds) {
			name, _, _ := strings.Cut(p, "=")
			k, ok := script.Lookup(name)
			switch added := i >= len(g.passes); {
			case !added && (!ok || !slices.Contains(g.make, p)):
				t.Errorf("%s: passes %s, which is no knob or not on make's command line", g.file, p)
			case added && (!ok || slices.ContainsFunc(g.make, func(w string) bool { return strings.HasPrefix(w, name+"=") })):
				t.Errorf("%s: adds %s, which is no knob or was on make's command line", g.file, p)
			}
			if i := slices.IndexFunc(script.Knobs, func(x script.Knob) bool { return x.Name == k.Name }); i < last {
				t.Errorf("%s: passes lists %s out of the knob table's order", g.file, name)
			} else {
				last = i
			}
		}
		if _, err := g.expect(); err != nil {
			t.Errorf("%s: %v", g.file, err)
		}
	}
}

// expect is what make's output holds laug to for g: the -dry-run line, or the part of it, laug prints; for request, the
// URL; for refused, "". A moved golden's line runs in the package's new directory: "cd <to> && " where make printed
// "cd <from> && ". A now header, where g has one, holds laug instead.
func (g golden) expect() (string, error) {
	line, err := g.expectMade()
	if err != nil || len(g.moved) != 2 {
		return line, err
	}
	from, to := "cd "+g.moved[0]+" && ", "cd "+g.moved[1]+" && "
	if !strings.Contains(line, from) {
		return "", fmt.Errorf("moved: the line holds no %q", from)
	}
	return strings.Replace(line, from, to, 1), nil
}

// expectMade is expect before a move: the line, or the part of it, as make printed it.
func (g golden) expectMade() (string, error) {
	mode, args := g.compare[0], g.compare[1:]
	prefix := strings.Join(slices.Concat(g.passes, g.adds), " ")
	if prefix != "" {
		prefix += " "
	}
	// make's ORCH_NOFLAGS unset TEMPORAL_SDK_FLAG_*, TEMPORAL_DEBUG and OTEL_* before an orchestrator binary or go;
	// internal/script hands a program none of them, so the line laug is held to leaves it out.
	body := strings.ReplaceAll(g.body, orchNoFlags+" ", "")
	switch mode {
	case "line", "resolved":
		if len(args) == 0 {
			return "", fmt.Errorf("compare: %s names no script", mode)
		}
		// line <script> <first argument>: of the commands that run the script, the one whose first argument that is,
		// as when two branches of an if run one script with two first arguments.
		first := ""
		if mode == "line" && len(args) > 1 {
			first = args[1]
		}
		var found, cds []string
		for _, c := range shellCommands(body) {
			for len(c) > 0 && slices.Contains([]string{"then", "else", "do"}, c[0]) {
				c = c[1:]
			}
			// A recipe's "cd health && go ...": laug's line keeps the cd, and the knobs passed go after it.
			cd := ""
			if len(c) > 3 && c[0] == "cd" && c[2] == "&&" {
				cd, c = strings.Join(c[:3], " ")+" ", c[3:]
			}
			i := slices.IndexFunc(c, func(w string) bool { return !assignment.MatchString(w) })
			if i >= 0 && c[i] == args[0] && (first == "" || i+1 < len(c) && c[i+1] == first) {
				found, cds = append(found, strings.Join(c, " ")), append(cds, cd)
			}
		}
		if len(found) != 1 {
			return "", fmt.Errorf("%d commands run %s %s, want 1", len(found), args[0], first)
		}
		line := found[0]
		if mode == "line" {
			return cds[0] + prefix + line, nil
		}
		// "$name" as the recipe quoted it, or $name where it left it bare, as make obs-down's OBS_FOLLOW_BUILD=$build;
		// a name given in double quotes, "${srv:-0}" say, is replaced as written, quotes and all.
		for _, r := range args[1:] {
			name, val, ok := strings.Cut(r, "=")
			bare := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"|` + regexp.QuoteMeta(name) + `\b`)
			if strings.HasPrefix(name, `"`) {
				bare = regexp.MustCompile(regexp.QuoteMeta(name))
			}
			if !ok || !bare.MatchString(line) {
				return "", fmt.Errorf("compare: %s: the line holds no %q or %s", r, `"`+name+`"`, name)
			}
			line = bare.ReplaceAllLiteralString(line, val)
		}
		return cds[0] + prefix + line, nil
	case "whole":
		// whole <word>: the one line of make's output that holds the word, a backslash and a newline joined, for a
		// recipe line laug prints whole: replay-test's pipe into grep.
		if len(args) != 1 || len(g.passes) > 0 {
			return "", fmt.Errorf("compare: whole names one word, and passes nothing")
		}
		var found []string
		for _, l := range strings.Split(strings.ReplaceAll(body, "\\\n", " "), "\n") {
			if slices.Contains(strings.Fields(l), args[0]) {
				found = append(found, l)
			}
		}
		if len(found) != 1 {
			return "", fmt.Errorf("%d lines hold %s, want 1", len(found), args[0])
		}
		return found[0], nil
	case "contains":
		m := regexp.MustCompile(regexp.QuoteMeta(args[0]) + `( ('[^']*'|[^\s'"();|&]+))*`).FindString(g.body)
		if m == "" {
			return "", fmt.Errorf("make's output never runs %s", args[0])
		}
		return prefix + m, nil
	case "request":
		re := regexp.MustCompile(`-X ` + args[0] + ` '(http://[^']*` + regexp.QuoteMeta(args[1]) + `)'`)
		m := re.FindStringSubmatch(g.body)
		if m == nil {
			return "", fmt.Errorf("make's output sends no %s %s", args[0], args[1])
		}
		return m[1], nil
	case "refused":
		if !strings.Contains(g.body, "exit 2;") {
			return "", fmt.Errorf("make's output has no check that exits 2")
		}
		return "", nil
	}
	return "", fmt.Errorf("compare: %q is not line, contains, resolved, whole, request or refused", mode)
}

// orchNoFlags is the Makefile's ORCH_NOFLAGS as make -n printed it, $$ made $. TestOrchestratorMatchesKnobs holds
// it to the Makefile's line, which make test-go runs before go test.
const orchNoFlags = "env $(compgen -e | sed -n -e 's/^TEMPORAL_SDK_FLAG_/-u &/p' -e 's/^TEMPORAL_DEBUG$/-u &/p' " +
	"-e 's/^OTEL_/-u &/p')"

// compareFields splits a compare header into its words at blanks, a word in double quotes kept whole, quotes and all:
// resolved's "$(getconf CLK_TCK)"=100.
func compareFields(s string) []string {
	var (
		fields []string
		word   strings.Builder
		quoted bool
	)
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ' ' && !quoted:
			if word.Len() > 0 {
				fields = append(fields, word.String())
				word.Reset()
			}
			continue
		}
		word.WriteRune(r)
	}
	if word.Len() > 0 {
		fields = append(fields, word.String())
	}
	return fields
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shellCommands splits shell text, as make -n printed it, into simple commands, each a list of words that keep their
// quotes. A backslash and a newline join two lines. Outside quotes and $( ), a newline, ;, |, || and && end a command
// and blanks split words; but "cd <dir> &&" starts the command after it, "&&" a word of its own.
func shellCommands(text string) [][]string {
	text = strings.ReplaceAll(text, "\\\n", " ")
	var (
		cmds  [][]string
		words []string
		word  strings.Builder
		stack []byte // ' and " for quotes, ( for $( and the parens inside it
	)
	endWord := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	for i := 0; i < len(text); i++ {
		ch := text[i]
		top := byte(0)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch {
		case top == '\'':
			if ch == '\'' {
				stack = stack[:len(stack)-1]
			}
		case ch == '\\' && i+1 < len(text):
			word.WriteByte(ch)
			i++
			ch = text[i]
		case top == '"':
			switch {
			case ch == '"':
				stack = stack[:len(stack)-1]
			case ch == '$' && i+1 < len(text) && text[i+1] == '(':
				stack = append(stack, '(')
				word.WriteByte(ch)
				i++
				ch = text[i]
			}
		case ch == '\'' || ch == '"':
			stack = append(stack, ch)
		case ch == '$' && i+1 < len(text) && text[i+1] == '(' || top == '(' && ch == '(':
			stack = append(stack, '(')
			if ch == '$' {
				word.WriteByte(ch)
				i++
				ch = text[i]
			}
		case top == '(' && ch == ')':
			stack = stack[:len(stack)-1]
		case top == '(':
		case ch == ' ' || ch == '\t':
			endWord()
			continue
		case ch == '\n' || ch == ';' || ch == '|':
			endCmd()
			continue
		case ch == '&' && i+1 < len(text) && text[i+1] == '&':
			endWord()
			i++
			// "cd <dir> &&" stays on the command after it, as the directory it runs in.
			if len(words) == 2 && words[0] == "cd" {
				words = append(words, "&&")
				continue
			}
			endCmd()
			continue
		}
		word.WriteByte(ch)
	}
	endCmd()
	return cmds
}

// TestShellCommands holds the splitter on the shapes the goldens hold.
func TestShellCommands(t *testing.T) {
	got := shellCommands(`A='x y' B= load/run-omes.sh s \
  more; c="$(yq "[.c[] | select(.name == \"$c\")] | length" f.yaml)" && [ -n "$c" ] || { echo "a; b" >&2; exit 2; }
th=$(x.sh '1') | y
cd health && go test ./... 2>&1 | grep -E 'x'
cd a b && c
`)
	want := [][]string{{"A='x y'", "B=", "load/run-omes.sh", "s", "more"},
		{`c="$(yq "[.c[] | select(.name == \"$c\")] | length" f.yaml)"`}, {"[", "-n", `"$c"`, "]"},
		{"{", "echo", `"a; b"`, ">&2"}, {"exit", "2"}, {"}"}, {"th=$(x.sh '1')"}, {"y"},
		{"cd", "health", "&&", "go", "test", "./...", "2>&1"}, {"grep", "-E", "'x'"}, {"cd", "a", "b"}, {"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

// requestVerbs run in-process: their goldens hold the request make's curl sent, not a script's line.
var requestVerbs = []string{"lease", "reset", "release"}

// goVerbs run in Go: gate rejudge, compare and signature by internal/rejudge, and gate tolerance by
// internal/tolerance; the verbs that run a Temporal SDK role, in laug's own process, and
// gate run -workflow with them (inGo). They take no -dry-run, so a golden holds one only
// to make's refusal. The roles' calls are held by TestHealth, TestOrchestrator and TestGateWorkflow.
var goVerbs = []string{"gate rejudge", "gate compare", "gate signature", "gate tolerance", "health check",
	"health loop", "health record", "health timer", "orchestrator worker", "orchestrator probe", "orchestrator record",
	"gate status", "gate cancel", "gate terminate", "gate stop"}

// inGo says whether g's laug line runs in Go: a verb of goVerbs, or gate run -workflow.
func (g golden) inGo() bool {
	if len(g.laug) < 2 {
		return false
	}
	verb := g.laug[0] + " " + g.laug[1]
	return slices.Contains(goVerbs, verb) || verb == "gate run" && slices.Contains(g.laug, "-workflow")
}

// goldenInputs are the checkout's files a verb reads before its script runs, copied into each golden's checkout: the
// catalog and the candidates, whose names fault, raun and candidate check.
var goldenInputs = []string{"faults/catalog.yaml", "faults/candidates.yaml"}

// TestDryRunGoldens holds laug to each golden. A script verb prints, with -dry-run, the line make -n printed, the knobs
// make passed unnamed in front: the whole line (line), the line with what the recipe resolved in its shell put in
// (resolved), or the script's call within it (contains). lease, reset and release send the request make's curl sent
// (request). A refused golden refuses in laug too, exit 2, with nothing run or sent; one that runs in Go (inGo) is run
// without -dry-run, which it does not take. A golden whose verb is not in the table fails. A golden with a now header
// holds laug to that line instead, as -update wrote it.
func TestDryRunGoldens(t *testing.T) {
	leaseDoc, err := os.ReadFile(filepath.Join("testdata", "dry-run", "lease.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range loadGoldens(t) {
		t.Run(strings.TrimSuffix(g.file, ".txt"), func(t *testing.T) {
			if _, ok := lookup(g.laug[0]); !ok {
				t.Fatalf("laug %s is not in the table", g.laug[0])
			}
			want, err := g.expect()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if g.lease {
				writeFile(t, filepath.Join(root, poolapi.LastLeaseFile), string(leaseDoc))
			}
			for p, c := range g.files {
				writeFile(t, filepath.Join(root, p), c+"\n")
				if strings.HasPrefix(c, "#!") {
					if err := os.Chmod(filepath.Join(root, p), 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, p := range goldenInputs {
				b, err := os.ReadFile(filepath.Join("..", "..", p))
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(root, p), string(b))
			}
			if slices.Contains(requestVerbs, g.laug[0]) {
				requestParity(t, g, root, want, leaseDoc)
				return
			}
			// make's POOL_ADDR default is the pool on 127.0.0.1:7700, where TestMain's fake pool is not.
			saved := poolDefault
			poolDefault = pool.DefaultListen
			defer func() { poolDefault = saved }()
			bin := filepath.Join(t.TempDir(), "bin")
			writeExec(t, filepath.Join(bin, "docker"), dockerStub)
			// orchestrator probe reads the dev server's pid with ss and CLK_TCK with getconf, as its recipe's shell
			// did at run time: no listener, and 100 ticks a second.
			writeExec(t, filepath.Join(bin, "ss"), "#!/bin/sh\nexit 0\n")
			writeExec(t, filepath.Join(bin, "getconf"), "#!/bin/sh\n[ \"$1\" = CLK_TCK ] && echo 100\n")
			environ := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
			if project, image, ok := strings.Cut(g.docker, " "); ok {
				environ = append(environ, "STUB_PROJECT="+project, "STUB_IMAGE="+image)
			}
			rec := &recorder{root: root}
			var stdout, stderr bytes.Buffer
			a := &App{Root: root, Environ: environ, Stdout: &stdout, Stderr: &stderr, Runner: rec, Roles: rec,
				Now: func() time.Time { return dryRunClock }}
			args := append(slices.Clone(g.laug), "-dry-run")
			if g.inGo() {
				if g.compare[0] != "refused" {
					t.Fatalf("laug %s runs in Go, with no -dry-run line to hold to make's: its golden can only be "+
						"refused", strings.Join(g.laug, " "))
				}
				args = slices.Clone(g.laug)
			}
			code := run(context.Background(), a, args)
			if len(rec.calls) != 0 {
				t.Errorf("-dry-run ran %d scripts or roles", len(rec.calls))
			}
			if g.compare[0] == "refused" {
				if code != exitUsage || stdout.Len() != 0 {
					t.Errorf("exit %d, want 2 and nothing on stdout\nstdout:\n%s\nstderr:\n%s", code, stdout.String(),
						stderr.String())
				}
				return
			}
			got := strings.TrimSuffix(stdout.String(), "\n")
			if code != exitDone || strings.Contains(got, "\n") {
				t.Fatalf("exit %d, want 0 and one line\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
			}
			matched := got == want
			if g.compare[0] == "contains" {
				matched = strings.Contains(got, want)
			}
			switch {
			case *update:
				g.writeNow(t, got, matched)
			case g.now != "":
				want = g.now
				matched = got == want
			}
			if !matched && !*update {
				t.Errorf("laug %s -dry-run:\n got %s\nwant %s\n-update writes got as the golden's now line",
					strings.Join(g.laug, " "), got, want)
			}
		})
	}
}

// writeNow is -update for g: got, laug's -dry-run line, goes into g's file as its now header, or the header goes when
// laug's line matches make's again. The other headers and make's output stay as they were.
func (g golden) writeNow(t *testing.T, got string, matched bool) {
	t.Helper()
	p := filepath.Join("testdata", "dry-run", g.file)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var head strings.Builder
	rest := string(b)
	for strings.HasPrefix(rest, "# ") {
		line, after, _ := strings.Cut(rest, "\n")
		if !strings.HasPrefix(line, "# now: ") {
			head.WriteString(line + "\n")
		}
		rest = after
	}
	if !matched {
		head.WriteString("# now: " + got + "\n")
	}
	if err := os.WriteFile(p, []byte(head.String()+rest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dockerStub is the docker the goldens were captured with: no container, unless STUB_PROJECT and STUB_IMAGE name the
// temporal-history container of that compose project and its image. With STUB_LOG set it appends its arguments there.
const dockerStub = `#!/bin/sh
[ -n "$STUB_LOG" ] && echo "$*" >> "$STUB_LOG"
case "$1" in
ps)
  if [ -n "$STUB_IMAGE" ]; then
    case "$*" in *"label=com.docker.compose.project=$STUB_PROJECT "*) echo 0123456789ab ;; esac
  fi ;;
inspect) [ -n "$STUB_IMAGE" ] && echo "$STUB_IMAGE" ;;
esac
exit 0
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sent is one request a stub pool took.
type sent struct {
	method, path string
	body         map[string]any
}

// requestParity runs an in-process verb against a stub pool that answers as the pool would: the request it sends is
// the one make's curl sent (method, path, body), to the address make's POOL_ADDR named, and the last-lease file ends
// as make's recipe left it. url is the URL make's curl posted to.
func requestParity(t *testing.T, g golden, root, url string, leaseDoc []byte) {
	t.Helper()
	var got []sent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		got = append(got, sent{r.Method, r.URL.Path, body})
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/release" {
			fmt.Fprintln(w, `{"released":"c0ffee01"}`)
			return
		}
		w.Write(leaseDoc)
	}))
	defer srv.Close()
	stub := strings.TrimPrefix(srv.URL, "http://")

	// make's POOL_ADDR became -pool; laug talks to the same URL, here the stub.
	args := slices.Clone(g.laug)
	addr := "http://" + pool.DefaultListen
	if i := slices.Index(args, "-pool"); i >= 0 && i+1 < len(args) {
		c, err := poolapi.New(args[i+1])
		if err != nil {
			t.Fatal(err)
		}
		addr, args[i+1] = c.Base(), stub
	} else {
		saved := poolDefault
		poolDefault = stub
		defer func() { poolDefault = saved }()
	}
	if g.compare[0] == "request" && addr+g.compare[2] != url {
		t.Errorf("laug %s talks to %s%s; make's curl posted to %s", strings.Join(g.laug, " "), addr, g.compare[2], url)
	}
	var stdout, stderr bytes.Buffer
	a := &App{Root: root, Stdout: &stdout, Stderr: &stderr, Now: func() time.Time { return dryRunClock }}
	code := run(context.Background(), a, args)
	file := filepath.Join(root, poolapi.LastLeaseFile)
	if g.compare[0] == "refused" {
		if code != exitUsage || len(got) != 0 {
			t.Errorf("exit %d, %d requests; want 2 and none\nstderr:\n%s", code, len(got), stderr.String())
		}
		return
	}
	if code != exitDone || len(got) != 1 {
		t.Fatalf("exit %d, %d requests; want 0 and 1\nstderr:\n%s", code, len(got), stderr.String())
	}
	want := sent{g.compare[1], g.compare[2], nil}
	switch g.laug[0] {
	case "lease":
		m := regexp.MustCompile(`--argjson ttl ([0-9]+) --arg requester '([^']*)' '\{ttl_seconds: \$ttl, requester: ` +
			`\$requester\}'\)" -o "\$tmp"; then .*\n\s*mv "\$tmp" \.last-lease\.json`).FindStringSubmatch(g.body)
		if m == nil {
			t.Fatalf("the golden's lease body is not jq -cn's {ttl_seconds, requester}, kept in .last-lease.json")
		}
		var ttl float64
		fmt.Sscan(m[1], &ttl)
		want.body = map[string]any{"ttl_seconds": ttl, "requester": m[2]}
		if b, err := os.ReadFile(file); err != nil || !bytes.Equal(b, leaseDoc) {
			t.Errorf("%s holds %q, %v; want the pool's answer as it came, as mv left it", poolapi.LastLeaseFile, b, err)
		}
	default:
		if !strings.Contains(g.body, `-d "$(jq '{id}' .last-lease.json)"`) {
			t.Fatalf("the golden's body is not jq's {id} of .last-lease.json")
		}
		want.body = map[string]any{"id": "c0ffee01"}
		_, err := os.Stat(file)
		removed := strings.Contains(g.body, "&& rm -f .last-lease.json")
		if removed != os.IsNotExist(err) {
			t.Errorf("%s removed: %t; make's recipe removes it: %t", poolapi.LastLeaseFile, os.IsNotExist(err), removed)
		}
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("laug %s sent %+v; make's curl sent %+v", strings.Join(g.laug, " "), got[0], want)
	}
}

// TestDryRunSeams: with CATALOG or CANDIDATES set, the -dry-run line of a verb whose script reads that file starts
// with it, so the line run alone reads the same file; a path under $HOME is written ~/, and one under the checkout
// relative to it. Unset, the line is the golden's (TestDryRunGoldens).
func TestDryRunSeams(t *testing.T) {
	home := t.TempDir()
	catalog := "defaults:\n  steady_s: 90\nfaults:\n  test-act:\n    signal: x\n"
	writeFile(t, filepath.Join(home, "c", "catalog.yaml"), catalog)
	for _, c := range []struct {
		args []string
		env  string
		want string
	}{
		{[]string{"raun", "test-act", "-dry-run"}, "CATALOG=fixture.yaml", "CATALOG=fixture.yaml RUN_ID=raun-"},
		{[]string{"fault", "test-act", "-dry-run"}, "CATALOG=fixture.yaml", "CATALOG=fixture.yaml RUN_ID="},
		{[]string{"raun", "test-act", "-dry-run"}, "CATALOG=" + filepath.Join(home, "c", "catalog.yaml"),
			"CATALOG=~/c/catalog.yaml RUN_ID=raun-"},
		{[]string{"raun", "test-act", "-dry-run"}, "CATALOG=$ROOT/fixture.yaml", "CATALOG=fixture.yaml RUN_ID=raun-"},
		{[]string{"cell", "fi-template-check", "-dry-run"}, "CATALOG=fixture.yaml",
			"CATALOG=fixture.yaml cells/fi-template-check.sh 1.31.0"},
		{[]string{"candidate", "test-cand", "-dry-run"}, "CANDIDATES=cands.yaml", "CANDIDATES=cands.yaml RUN_ID="},
	} {
		a, _ := matrixApp(t, envCase{files: map[string]string{"fixture.yaml": catalog,
			"cands.yaml": "candidates:\n  - name: test-cand\n"}})
		a.Environ = append([]string{"HOME=" + home, strings.ReplaceAll(c.env, "$ROOT", a.Root)}, a.Environ...)
		code, out, errOut := runApp(t, a, c.args...)
		if code != 0 || !strings.HasPrefix(out, c.want) || strings.Count(out, "\n") != 1 {
			t.Errorf("%s with %s: exit %d\nstdout:\n%s\nstderr:\n%s\nwant it to start %q", strings.Join(c.args, " "),
				c.env, code, out, errOut, c.want)
		}
	}
}
