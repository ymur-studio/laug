package main

// The goldens in testdata/dry-run: what make -n printed for the stack's targets before laug had subcommands, and the
// test that holds bin/obs's verbs to them, after cmd/laug/dryrun_test.go, whose goldens these were. testdata/dry-run/
// README.md says what a header holds; cmd/laug/testdata/dry-run/README.md says how they were captured.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// golden is one file of testdata/dry-run: its header, then make -n's output.
type golden struct {
	file    string
	make    []string          // the make variables and target after "make --no-print-directory -n"
	obs     []string          // the bin/obs command it stands for, after "bin/obs"
	files   map[string]string // files the checkout held, path to content
	passes  []string          // NAME=value: what make passed unnamed from its command line, which the script reads
	compare []string          // how bin/obs is held to it: line, resolved or refused, and arguments
	body    string
}

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
			case "obs":
				g.obs = strings.Fields(strings.TrimPrefix(val, obsBin+" "))
			case "lease":
				if val != "absent" {
					t.Errorf("%s: lease: %q; the stack's targets read no lease", g.file, val)
				}
			case "file":
				path, content, _ := strings.Cut(val, " ")
				g.files[path] = content
			case "passes":
				g.passes = strings.Fields(val)
			case "compare":
				g.compare = strings.Fields(val)
			default:
				t.Errorf("%s: header %q", g.file, line)
			}
		}
		g.body = rest
		if g.make == nil || len(g.obs) == 0 || len(g.compare) == 0 {
			t.Errorf("%s: a golden names its make line, its bin/obs line and how it compares", g.file)
		}
		gs = append(gs, g)
	}
	return gs
}

// absPath is an absolute path in make's output: a / that starts a word, a quoted string or a value.
var absPath = regexp.MustCompile(`(^|[\s'"=(])(/[A-Za-z0-9._-][^\s'"]*)`)

// TestGoldensClean: no golden holds an absolute path; every header is whole; a knob it says make passed unnamed was
// on make's command line, and is a knob, in the knob table's order; and the line bin/obs is held to can be found in
// make's output.
func TestGoldensClean(t *testing.T) {
	for _, g := range loadGoldens(t) {
		for _, m := range absPath.FindAllStringSubmatch(g.body, -1) {
			t.Errorf("%s: an absolute path, %s; the checkout is written $ROOT", g.file, m[2])
		}
		last := -1
		for _, p := range g.passes {
			name, _, _ := strings.Cut(p, "=")
			k, ok := script.Lookup(name)
			if !ok || !slices.Contains(g.make, p) {
				t.Errorf("%s: passes %s, which is no knob or not on make's command line", g.file, p)
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

// expect is what bin/obs is held to by g: the -dry-run line it prints, or for refused, "".
func (g golden) expect() (string, error) {
	mode, args := g.compare[0], g.compare[1:]
	prefix := strings.Join(g.passes, " ")
	if prefix != "" {
		prefix += " "
	}
	switch mode {
	case "line", "resolved":
		if len(args) == 0 {
			return "", fmt.Errorf("compare: %s names no script", mode)
		}
		var found []string
		for _, c := range shellCommands(g.body) {
			i := slices.IndexFunc(c, func(w string) bool { return !assignment.MatchString(w) })
			if i >= 0 && c[i] == args[0] {
				// The Makefile laug replaced ran the script from obs/; it is obsScript now, the one word that moved.
				if c[i] == seedScript {
					c[i] = obsScript
				}
				found = append(found, strings.Join(c, " "))
			}
		}
		if len(found) != 1 {
			return "", fmt.Errorf("%d commands run %s, want 1", len(found), args[0])
		}
		line := found[0]
		// "$name" as the recipe quoted it, or $name where it left it bare, as make obs-down's OBS_FOLLOW_BUILD=$build.
		for _, r := range args[1:] {
			if mode != "resolved" {
				return "", fmt.Errorf("compare: line takes the script alone, got %q", r)
			}
			name, val, ok := strings.Cut(r, "=")
			bare := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"|` + regexp.QuoteMeta(name) + `\b`)
			if !ok || !bare.MatchString(line) {
				return "", fmt.Errorf("compare: %s: the line holds no %q or %s", r, `"`+name+`"`, name)
			}
			line = bare.ReplaceAllLiteralString(line, val)
		}
		return prefix + line, nil
	case "refused":
		if !strings.Contains(g.body, "exit 2;") {
			return "", fmt.Errorf("make's output has no check that exits 2")
		}
		return "", nil
	}
	return "", fmt.Errorf("compare: %q is not line, resolved or refused", mode)
}

// seedScript is where the Makefile laug replaced ran the stack's script, as every golden's make -n output names
// it. The goldens keep it; expect puts obsScript in its place.
const seedScript = "obs/obs.sh"

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shellCommands splits shell text, as make -n printed it, into simple commands, each a list of words that keep their
// quotes, as cmd/laug/dryrun_test.go's does, whose TestShellCommands holds it: outside quotes and $( ), a newline, ;,
// |, || and && end a command and blanks split words. The stack's goldens hold no cd.
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
			endCmd()
			continue
		}
		word.WriteByte(ch)
	}
	endCmd()
	return cmds
}

// TestDryRunGoldens holds bin/obs to each golden: a script verb prints, with -dry-run, the line make -n printed, the
// knobs make passed unnamed in front, whole (line) or with what the recipe resolved in its shell put in (resolved); a
// refused golden refuses here too, exit 2, with nothing run. A golden whose verb is not in the table fails.
func TestDryRunGoldens(t *testing.T) {
	for _, g := range loadGoldens(t) {
		t.Run(strings.TrimSuffix(g.file, ".txt"), func(t *testing.T) {
			if _, ok := lookup(g.obs[0]); !ok {
				t.Fatalf("bin/obs %s is not in the table", g.obs[0])
			}
			want, err := g.expect()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			for p, c := range g.files {
				writeFile(t, filepath.Join(root, p), c+"\n")
				if strings.HasPrefix(c, "#!") {
					if err := os.Chmod(filepath.Join(root, p), 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			rec := &recorder{root: root}
			var stdout, stderr bytes.Buffer
			a := &App{Root: root, Environ: []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}, Stdout: &stdout,
				Stderr: &stderr, Runner: rec}
			code := run(context.Background(), a, append(slices.Clone(g.obs), "-dry-run"))
			if len(rec.calls) != 0 {
				t.Errorf("-dry-run ran %d scripts", len(rec.calls))
			}
			if g.compare[0] == "refused" {
				if code != exitUsage || stdout.Len() != 0 {
					t.Errorf("exit %d, want 2 and nothing on stdout\nstdout:\n%s\nstderr:\n%s", code, stdout.String(),
						stderr.String())
				}
				return
			}
			got := strings.TrimSuffix(stdout.String(), "\n")
			if code != exitDone || strings.Contains(got, "\n") || got != want {
				t.Errorf("bin/obs %s -dry-run: exit %d\n got %s\nwant %s\nstderr:\n%s", strings.Join(g.obs, " "), code,
					got, want, stderr.String())
			}
		})
	}
}
