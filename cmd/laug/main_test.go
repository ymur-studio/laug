package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// runArgs runs laug with args on an App of two buffers and an empty environment.
func runArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), &App{Stdout: &stdout, Stderr: &stderr}, args)
	return code, stdout.String(), stderr.String()
}

// verbArgs is how a verb is named on the command line: the command, then the verb unless it has no name.
func verbArgs(c command, v verb) []string {
	if v.name == "" {
		return []string{c.name}
	}
	return []string{c.name, v.name}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// needTools skips the test when one of tools is not on PATH, and says which tool and why, what the test runs it for.
// Under LAUG_TEST_SHELL=require, as .github/workflows/test.yml sets it, that skip is a failure.
func needTools(t *testing.T, why string, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("LAUG_TEST_SHELL") == "require" {
				t.Fatalf("LAUG_TEST_SHELL=require: %s is not on PATH: %s", tool, why)
			}
			t.Skipf("%s is not on PATH: %s", tool, why)
		}
	}
}

// TestRun drives the binary through run: help asked for on stdout with 0, a mistake on stderr with 2.
func TestRun(t *testing.T) {
	for _, c := range []struct {
		name     string
		args     []string
		code     int
		out, err string // each must hold this; "" wants that stream empty
		first    string // the first line of the stream that has output
	}{
		{"bare", nil, 2, "", "usage: laug <command>", header},
		{"help", []string{"help"}, 0, "usage: laug <command>", "", header},
		{"-h", []string{"-h"}, 0, "usage: laug <command>", "", header},
		{"-help", []string{"-help"}, 0, "usage: laug <command>", "", header},
		{"--help", []string{"--help"}, 0, "usage: laug <command>", "", header},
		{"unknown", []string{"fleet"}, 2, "", "usage: laug <command>", `laug · unknown command "fleet"`},
		{"a typo", []string{"gaet"}, 2, "", `"laug help" lists the commands.`,
			`laug · unknown command "gaet"; did you mean gate?`},
		{"a flag first", []string{"-json"}, 2, "", "usage: laug <command>", `laug · unknown command "-json"`},
		{"version -h", []string{"version", "-h"}, 0, "usage: laug version [-json]", "", "laug · version: print the commit"},
		{"version --help", []string{"version", "--help"}, 0, "-json", "", "laug · version: print the commit"},
		{"version", []string{"version"}, 0, " server " + serverTagDefault + " omes ", "", "laug · version "},
		{"version, bad flag", []string{"version", "-bogus"}, 2, "", "usage: laug version",
			"laug · version: no flag -bogus"},
		{"version, an argument", []string{"version", "x"}, 2, "", "usage: laug version",
			`laug · version: takes no arguments, got ["x"]`},
		{"help version", []string{"help", "version"}, 0, "usage: laug version [-json]", "",
			"laug · version: print the commit"},
		{"help help", []string{"help", "help"}, 0, "usage: laug help [<command> [<verb>]]", "",
			"laug · help: print this list"},
		{"help, unknown", []string{"help", "fleet"}, 2, "", "usage: laug <command>", `laug · help: unknown command "fleet"`},
		{"help, a verb for a command with none", []string{"help", "version", "help"}, 2, "", "usage: laug help",
			"laug · help: version has no verbs; laug help version prints its flags"},
		{"help, three", []string{"help", "gate", "run", "x"}, 2, "", "usage: laug help",
			`laug · help: takes a command and a verb at most, got ["gate" "run" "x"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runArgs(t, c.args...)
			if code != c.code {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
			}
			for _, s := range []struct{ stream, got, want string }{{"stdout", out, c.out}, {"stderr", errOut, c.err}} {
				if s.want == "" && s.got != "" {
					t.Errorf("%s should be empty:\n%s", s.stream, s.got)
				}
				if s.want != "" && !strings.Contains(s.got, s.want) {
					t.Errorf("%s lacks %q:\n%s", s.stream, s.want, s.got)
				}
				if s.want != "" && !strings.HasPrefix(firstLine(s.got), c.first) {
					t.Errorf("%s's first line is %q, want it to start %q", s.stream, firstLine(s.got), c.first)
				}
			}
		})
	}
}

// TestRunInterspersed: flags after an argument still parse, so "version x -json" names only x.
func TestRunInterspersed(t *testing.T) {
	code, _, errOut := runArgs(t, "version", "x", "-json")
	if code != 2 || firstLine(errOut) != `laug · version: takes no arguments, got ["x"]` {
		t.Errorf("exit %d, stderr:\n%s", code, errOut)
	}
}

// TestParse holds the interspersed parse on the shape "gate run 1.31.0 -fake" will take.
func TestParse(t *testing.T) {
	type got struct {
		Pos   []string
		Fake  bool
		Plan  string
		Items int
	}
	for _, c := range []struct {
		name string
		args []string
		want got
	}{
		{"flag after", []string{"1.31.0", "-fake"}, got{[]string{"1.31.0"}, true, "", 0}},
		{"flag before", []string{"-fake", "1.31.0"}, got{[]string{"1.31.0"}, true, "", 0}},
		{"both sides", []string{"-items", "3", "1.31.0", "-plan", "p.yaml", "-fake"},
			got{[]string{"1.31.0"}, true, "p.yaml", 3}},
		{"between", []string{"a", "-fake", "b"}, got{[]string{"a", "b"}, true, "", 0}},
		{"two dashes", []string{"1.31.0", "--fake", "--plan=p.yaml"}, got{[]string{"1.31.0"}, true, "p.yaml", 0}},
		{"-- ends", []string{"--", "-fake"}, got{[]string{"-fake"}, false, "", 0}},
		{"-- after an argument", []string{"1.31.0", "--", "-fake", "x"}, got{[]string{"1.31.0", "-fake", "x"}, false, "", 0}},
		{"-- after a bool", []string{"-fake", "--", "-plan"}, got{[]string{"-plan"}, true, "", 0}},
		{"-- as a value", []string{"-plan", "--", "1.31.0", "-fake"}, got{[]string{"1.31.0"}, true, "--", 0}},
		{"-- as a value, then --", []string{"-plan", "--", "--", "-fake"}, got{[]string{"-fake"}, false, "--", 0}},
		{"a dash alone", []string{"-", "-fake"}, got{[]string{"-"}, true, "", 0}},
		{"bool with =", []string{"-fake=false", "x", "-fake=true"}, got{[]string{"x"}, true, "", 0}},
		{"none", nil, got{nil, false, "", 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := (&App{}).newFlags("gate run", "<tag> [flags]")
			var g got
			f.BoolVar(&g.Fake, "fake", false, "fake cells")
			f.StringVar(&g.Plan, "plan", "", "the plan")
			f.IntVar(&g.Items, "items", 0, "items")
			var stdout, stderr bytes.Buffer
			pos, code, ok := f.parse(c.args, &stdout, &stderr)
			if !ok || code != 0 || stdout.Len()+stderr.Len() != 0 {
				t.Fatalf("parse: ok %t, code %d\n%s%s", ok, code, stdout.String(), stderr.String())
			}
			g.Pos = pos
			if !reflect.DeepEqual(g, c.want) {
				t.Errorf("got %+v, want %+v", g, c.want)
			}
		})
	}
	t.Run("-h after an argument", func(t *testing.T) {
		f := (&App{}).newFlags("gate run", "<tag> [flags]")
		f.Bool("fake", false, "fake cells")
		var stdout, stderr bytes.Buffer
		if _, code, ok := f.parse([]string{"1.31.0", "-h"}, &stdout, &stderr); ok || code != 0 || stderr.Len() != 0 ||
			!strings.HasPrefix(stdout.String(), "laug · gate run: judge a server release beside the one it replaces\n"+
				"usage: laug gate run <tag> [flags]\n") ||
			!strings.Contains(stdout.String(), "-fake") {
			t.Errorf("ok %t, code %d\nstdout:\n%s\nstderr:\n%s", ok, code, stdout.String(), stderr.String())
		}
	})
	t.Run("a bad flag after an argument", func(t *testing.T) {
		f := (&App{}).newFlags("gate run", "<tag> [flags]")
		var stdout, stderr bytes.Buffer
		if _, code, ok := f.parse([]string{"1.31.0", "-nope"}, &stdout, &stderr); ok || code != 2 || stdout.Len() != 0 ||
			firstLine(stderr.String()) != "laug · gate run: no flag -nope" {
			t.Errorf("ok %t, code %d\nstdout:\n%s\nstderr:\n%s", ok, code, stdout.String(), stderr.String())
		}
	})
	// A mistake gets its line, the nearest flag, the synopsis and the line that prints the flags, and no flag: those
	// are -h's. A bad value gets laug's line for each kind of flag, the value as typed and what the flag takes, its
	// default the example when it has one, then the same two.
	for args, first := range map[string]string{
		"1.31.0 -fak":     "laug · gate run: no flag -fak; did you mean -fake?",
		"1.31.0 --plna=x": "laug · gate run: no flag -plna; did you mean -plan?",
		"-items x":        "laug · gate run: -items x: a whole number, 3 say",
		"-min x":          "laug · gate run: -min x: a whole number, 2 say",
		"-items 99999999999999999999": "laug · gate run: -items 99999999999999999999: a whole number, and that one is " +
			"too large",
		"-ttl 5x":           "laug · gate run: -ttl 5x: a duration, 30m say",
		"-ttl=1800":         "laug · gate run: -ttl 1800: a duration, 30m say",
		"-grace 'two s'":    "laug · gate run: -grace 'two s': a duration, 30s say",
		"-ratio half":       "laug · gate run: -ratio half: a number, 0.5 say",
		"-ratio 1e999":      "laug · gate run: -ratio 1e999: a number, and that one is too large",
		"1.31.0 -fake=what": "laug · gate run: -fake=what: true or false, and -fake alone is true",
		"1.31.0 -ttl":       "laug · gate run: -ttl needs a value after it: a duration, 30m say",
		"1.31.0 -plan":      "laug · gate run: -plan needs a value after it: a file",
		"1.31.0 -pool":      "laug · gate run: -pool needs a value after it: an address",
		"1.31.0 -run-id":    "laug · gate run: -run-id needs a value after it: an id",
		"1.31.0 -sink":      "laug · gate run: -sink needs a value after it: a url",
		"---plan":           "laug · gate run: ---plan: not a flag; a flag is -name, -name value or -name=value",
	} {
		f := (&App{Self: "bin/laug"}).newFlags("gate run", "<tag> [flags]")
		f.Bool("fake", false, "fake cells")
		f.String("plan", "", "the plan `file`")
		f.String("pool", "", "the pool's `address`")
		f.String("run-id", "", "the run's `id`")
		f.String("sink", "", "where it goes, a `url`")
		f.Int("items", 0, "items")
		f.Int("min", 2, "the least")
		f.Duration("ttl", 30*time.Minute, "how long")
		f.Duration("grace", 0, "how long after")
		f.Float64("ratio", 0, "how much")
		var stdout, stderr bytes.Buffer
		want := first + "\nusage: bin/laug gate run <tag> [flags]\n\"bin/laug gate run -h\" prints its flags.\n"
		words := strings.Fields(args)
		if args == "-grace 'two s'" {
			words = []string{"-grace", "two s"}
		}
		if _, code, ok := f.parse(words, &stdout, &stderr); ok || code != 2 || stdout.Len() != 0 ||
			stderr.String() != want {
			t.Errorf("%s: ok %t, code %d\nstderr:\n%s\nwant:\n%s", args, ok, code, stderr.String(), want)
		}
	}
	// The same line from two verbs whose value names start with a vowel.
	for args, first := range map[string]string{
		"lease -pool":  "laug · lease: -pool needs a value after it: an address",
		"load -run-id": "laug · load: -run-id needs a value after it: an id",
	} {
		code, out, errOut := runApp(t, &App{Root: checkout(t), Self: "bin/laug"}, strings.Fields(args)...)
		if code != exitUsage || out != "" || firstLine(errOut) != first {
			t.Errorf("%s: exit %d\nstdout:\n%s\nstderr:\n%s\nwant first %q", args, code, out, errOut, first)
		}
	}
}

// TestFixedSets: a string flag that takes one of a fixed set refuses another value in laug's words, as TestParse holds
// every other kind of flag to: the flag, the value as typed, then what it takes. One line, exit 2, nothing run.
func TestFixedSets(t *testing.T) {
	for _, c := range []struct {
		args []string
		line string
	}{
		{[]string{"cell", "up", "-visibility", "nope", "-dry-run"}, "laug · cell up: -visibility nope: postgres or es"},
		{[]string{"cell", "up", "-bind", "nope", "-dry-run"},
			"laug · cell up: -bind nope: loopback, 127.0.0.1, or all, 0.0.0.0; laug dials a cell on 127.0.0.1"},
		{[]string{"pool", "down", "-backend", "nope"}, "laug · pool down: -backend nope: compose or firecracker"},
	} {
		rec := &recorder{}
		code, out, errOut := runApp(t, &App{Root: checkout(t), Runner: rec, Self: "bin/laug"}, c.args...)
		if code != exitUsage || out != "" || errOut != c.line+"\n" || len(rec.calls) != 0 {
			t.Errorf("%s: exit %d, %d scripts\nstdout:\n%s\nstderr:\n%s\nwant %q", strings.Join(c.args, " "), code,
				len(rec.calls), out, errOut, c.line)
		}
	}
}

// TestNearest holds "did you mean" to its bound: 2 edits at most from 6 letters on, 1 below, none for one letter; a
// swap of two letters side by side is one edit; ties come in the names' order; a name never suggests itself.
func TestNearest(t *testing.T) {
	names := []string{"gate", "lease", "release", "reset", "run", "raun", "server-tag", "open", "lab"}
	for typed, want := range map[string]string{
		"gaet":       "; did you mean gate?",
		"lesae":      "; did you mean lease?",
		"relase":     "; did you mean release?",
		"rnu":        "; did you mean run?",
		"ran":        "; did you mean run or raun?",
		"sever-tag":  "; did you mean server-tag?",
		"servr-tg":   "; did you mean server-tag?",
		"srvr-tg":    "",
		"nope":       "",
		"all":        "",
		"x":          "",
		"gate":       "",
		"relaese":    "; did you mean release?",
		"resest":     "; did you mean reset?",
		"leasereset": "",
	} {
		if got := didYouMean("", typed, names); got != want {
			t.Errorf("%s: %q, want %q", typed, got, want)
		}
	}
	if got := didYouMean("-", "fak", []string{"dry-run", "fake", "plan"}); got != "; did you mean -fake?" {
		t.Errorf("a flag: %q", got)
	}
	for _, c := range []struct {
		a, b string
		n    int
	}{{"", "abc", 3}, {"abc", "", 3}, {"abc", "abc", 0}, {"abc", "acb", 1}, {"abc", "ab", 1}, {"kitten", "sitting", 3},
		{"ca", "abc", 3}, {"é", "e", 1}} {
		if got := edits(c.a, c.b); got != c.n {
			t.Errorf("edits(%q, %q) = %d, want %d", c.a, c.b, got, c.n)
		}
	}
}

// screenRows is how many rows s takes on a terminal 80 columns wide, a tab to the next stop of 8.
func screenRows(s string) int {
	rows := 0
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		col := 0
		for _, r := range l {
			if r == '\t' {
				col += 8 - col%8
			} else {
				col++
			}
		}
		rows += max(1, (col+79)/80)
	}
	return rows
}

// TestMistakeFits: a mistake fits on one screen, 24 rows less the prompt after it, so its first line stays in sight.
// An unknown command; for every command with named verbs, an unknown verb; for every verb, a flag it does not hold;
// and every needs verb bare: each writes at most 23 rows on stderr, lists no flag, and ends with the line that prints
// them, or for a command, its verbs. In this empty directory a bare raun lists no faults; mistakeFitsChoices holds it
// to the catalogs.
func TestMistakeFits(t *testing.T) {
	t.Run("the catalogs", mistakeFitsChoices)
	runs := [][]string{{"lese"}, {"help", "lese"}}
	for _, c := range commands {
		named := len(c.verbs) > 1 || c.verbs[0].name != ""
		if named {
			runs = append(runs, []string{c.name}, []string{c.name, "fleet"})
		}
		for _, v := range c.verbs {
			runs = append(runs, append(verbArgs(c, v), "-nope"))
			if v.class == classNeeds {
				runs = append(runs, verbArgs(c, v))
			}
		}
	}
	for _, args := range runs {
		dir := t.TempDir()
		t.Chdir(dir)
		rec := &recorder{root: dir}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), &App{Root: dir, Stdout: &stdout, Stderr: &stderr, Runner: rec, Roles: rec,
			Self: "bin/laug"}, args)
		errOut := stderr.String()
		if code != exitUsage || stdout.Len() != 0 || screenRows(errOut) > 23 || strings.Contains(errOut, "\n  -") ||
			!strings.HasSuffix(errOut, " prints its flags.\n") && !strings.HasSuffix(errOut, " lists the commands.\n") &&
				!strings.HasSuffix(errOut, " prints a verb's flags.\n") {
			t.Errorf("%s: exit %d, %d rows, want 2 and 23 at most, no flag, the line that prints them last\n%s",
				strings.Join(args, " "), code, screenRows(errOut), errOut)
		}
	}
}

// mistakeFitsChoices: bare raun, fault and candidate, and raun and fault with a fault none is near, list every name
// the file holds, down and across, and fit on one screen: with the checkout's catalog and candidates, with the
// catalog and runbook 6's 19th fault, and with 34 faults as long as its longest, the most two columns hold.
func mistakeFitsChoices(t *testing.T) {
	base, _ := matrixApp(t, envCase{})
	_, faults, err := faultChoice.names(base)
	if err != nil {
		t.Fatal(err)
	}
	_, candidates, err := candidateChoice.names(base)
	if err != nil {
		t.Fatal(err)
	}
	long := slices.MaxFunc(faults, func(x, y string) int { return len(x) - len(y) })
	var many []string
	for i := range 34 {
		many = append(many, fmt.Sprintf("%s%02d", long[:len(long)-2], i))
	}
	catalog := func(names []string) string {
		return "faults:\n  " + strings.Join(names, ":\n    signal: x\n  ") + ":\n    signal: x\n"
	}
	for _, c := range []struct {
		name  string
		file  string // the catalog's text, under CATALOG; "" for the checkout's
		names []string
	}{
		{"the checkout's", "", faults},
		{"runbook 6's 19th", catalog(append(slices.Clone(faults), "persistence-latency-slow")),
			append(slices.Clone(faults), "persistence-latency-slow")},
		{"34 faults", catalog(many), many},
	} {
		file, more := "faults/catalog.yaml", []string(nil)
		if c.file != "" {
			file, more = "big.yaml", []string{"CATALOG=big.yaml"}
		}
		for _, args := range [][]string{{"raun"}, {"fault"}, {"raun", "nope"}, {"fault", "nope"}} {
			a, rec := matrixApp(t, envCase{lease: true, files: map[string]string{"big.yaml": c.file}, more: more})
			code, out, errOut := runApp(t, a, args...)
			first := fmt.Sprintf("laug · %s: names no fault: %s holds %d:", args[0], file, len(c.names))
			if len(args) > 1 {
				first = fmt.Sprintf("laug · %s: no fault nope in %s, which holds %d:", args[0], file, len(c.names))
			}
			head, _, _ := strings.Cut(errOut, "\n")
			names, rest := downAcross(errOut)
			if code != exitUsage || out != "" || len(rec.calls) != 0 || head != first || !slices.Equal(names, c.names) ||
				screenRows(errOut) > 23 || len(args) == 1 && !strings.HasPrefix(rest, "usage: ") ||
				len(args) > 1 && rest != "" {
				t.Errorf("%s, %q: exit %d, %d rows, want 2 and 23 at most, %q first, every name\n%s", c.name, args,
					code, screenRows(errOut), first, errOut)
			}
		}
	}
	a, _ := matrixApp(t, envCase{})
	code, _, errOut := runApp(t, a, "candidate")
	names, rest := downAcross(errOut)
	if code != exitUsage || !slices.Equal(names, candidates) || screenRows(errOut) > 23 ||
		!strings.HasPrefix(rest, "usage: ") {
		t.Errorf("candidate: exit %d, %d rows\n%s", code, screenRows(errOut), errOut)
	}
}

// downAcross reads the names under a mistake's first line, the lines two spaces in, down each column and then across,
// and returns them and the text after them.
func downAcross(s string) (names []string, rest string) {
	lines := strings.Split(s, "\n")[1:]
	var rows [][]string
	for len(lines) > 0 && strings.HasPrefix(lines[0], "  ") {
		rows, lines = append(rows, strings.Fields(lines[0])), lines[1:]
	}
	for col := 0; len(rows) > 0 && col < len(rows[0]); col++ {
		for _, r := range rows {
			if col < len(r) {
				names = append(names, r[col])
			}
		}
	}
	return names, strings.Join(lines, "\n")
}

// TestSignalContext: a SIGTERM or a SIGINT laug gets cancels the verb's context, its cause the signal, so Run can
// pass a SIGTERM on and leave Ctrl-C to the terminal; stop cancels it with no signal.
func TestSignalContext(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		ctx, stop := signalContext()
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: the context did not end", sig)
		}
		var s script.Signalled
		if !errors.As(context.Cause(ctx), &s) || s.Signal != sig || script.Interrupted(ctx) != (sig == syscall.SIGINT) {
			t.Errorf("%v: the cause is %v, Interrupted %v", sig, context.Cause(ctx), script.Interrupted(ctx))
		}
		stop()
	}
	ctx, stop := signalContext()
	stop()
	if !errors.Is(context.Cause(ctx), context.Canceled) || script.Interrupted(ctx) {
		t.Errorf("stop: the cause is %v", context.Cause(ctx))
	}
}

// kebab is the shape of a command's or a verb's name.
var kebab = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// TestTableRules holds every row to the table's rules: a name, a group, a summary whose help line stays under 80
// columns, a paragraph in doc.go, a test in <name>_test.go, and verbs. A verb has a class; a named one has a summary,
// lowercase first, no full stop. Its -h prints its usage on stdout, first line "laug · <command>[ <verb>]: <summary>"
// under 80 columns, nothing on stderr, exit 0. A command with named verbs does the same with its own -h, a line a verb
// under 80 columns. No help says Gate 6, a stage of building laug, or make -n, the Makefile laug replaced; and no flag
// says gate-open, which reads as laug gate open, the verb that names a run's files: a gate that runs is -gate-running.
func TestTableRules(t *testing.T) {
	if len(commands) == 0 {
		t.Fatal("the table is empty")
	}
	var help bytes.Buffer
	usage(&help, "bin/laug") // the longer of the two words a line to type starts with
	for _, l := range strings.Split(help.String(), "\n") {
		if n := utf8.RuneCountInString(l); n >= 80 {
			t.Errorf("a help line of %d columns: %q", n, l)
		}
	}
	for _, old := range []string{"Gate 6", "make -n"} {
		if strings.Contains(help.String(), old) {
			t.Errorf("the help says %s:\n%s", old, help.String())
		}
	}
	doc := packageDoc(t)
	seen := map[string]bool{}
	for _, c := range commands {
		if !kebab.MatchString(c.name) || seen[c.name] {
			t.Errorf("%q: a name is lowercase kebab-case and appears once", c.name)
		}
		seen[c.name] = true
		if c.group <= 0 || int(c.group) >= len(groupHeadings) {
			t.Errorf("%s: no group", c.name)
		}
		r, _ := utf8.DecodeRuneInString(c.summary)
		if c.summary == "" || !unicode.IsLower(r) || strings.HasSuffix(c.summary, ".") {
			t.Errorf("%s: summary %q: lowercase first, no full stop", c.name, c.summary)
		}
		if !hasParagraph(doc, c.name) {
			t.Errorf("%s: doc.go has no paragraph that starts %q", c.name, c.name+" ")
		}
		fn := "Test"
		for _, part := range strings.Split(c.name, "-") {
			if part != "" {
				fn += strings.ToUpper(part[:1]) + part[1:]
			}
		}
		if !hasTest(t, c.name+"_test.go", fn) {
			t.Errorf("%s: %s_test.go has no %s", c.name, c.name, fn)
		}
		checkVerbs(t, c)
	}
}

// checkVerbs holds one command's verbs to the rules TestTableRules names.
func checkVerbs(t *testing.T, c command) {
	t.Helper()
	if len(c.verbs) == 0 {
		t.Errorf("%s: no verb", c.name)
	}
	named := len(c.verbs) > 1 || len(c.verbs) == 1 && c.verbs[0].name != ""
	seen := map[string]bool{}
	for _, v := range c.verbs {
		if named && (!kebab.MatchString(v.name) || seen[v.name]) {
			t.Errorf("%s %q: beside another verb, a verb is named in kebab-case, once", c.name, v.name)
		}
		seen[v.name] = true
		if v.run == nil {
			t.Errorf("%s %s: no run function", c.name, v.name)
		}
		if v.class != classRead && v.class != classAct && v.class != classNeeds && v.class != classDestroys {
			t.Errorf("%s %s: no class", c.name, v.name)
		}
		if v.run != nil {
			checkHelp(t, append(verbArgs(c, v), "-h")...)
		}
	}
	if named {
		checkHelp(t, c.name, "-h")
		var w strings.Builder
		c.usage(&w, "bin/laug")
		for _, l := range strings.Split(w.String(), "\n") {
			if n := utf8.RuneCountInString(l); n >= 80 {
				t.Errorf("%s -h: a line of %d columns: %q", c.name, n, l)
			}
		}
		for _, v := range c.verbs {
			r, _ := utf8.DecodeRuneInString(v.summary)
			if v.summary == "" || !unicode.IsLower(r) || strings.HasSuffix(v.summary, ".") {
				t.Errorf("%s %s: summary %q: lowercase first, no full stop", c.name, v.name, v.summary)
			}
		}
	}
}

// checkHelp runs args, which end in -h: exit 0, nothing on stderr, stdout's first line "laug · ", the words before
// -h and their summary, every line under 80 columns, and no line that names Gate 6, make -n or a gate-open flag.
func checkHelp(t *testing.T, args ...string) {
	t.Helper()
	code, out, errOut := runArgs(t, args...)
	name := strings.Join(args[:len(args)-1], " ")
	summary, _ := summaryOf(name)
	if c, ok := lookup(name); ok {
		summary = c.summary
	}
	want := "laug · " + name + ": " + summary
	if first := firstLine(out); code != 0 || errOut != "" || summary == "" || first != want ||
		utf8.RuneCountInString(first) >= 80 {
		t.Errorf("%s: exit %d, want 0; first stdout line %q, want %q, under 80 columns\nstderr:\n%s",
			strings.Join(args, " "), code, first, want, errOut)
	}
	for _, old := range []string{"Gate 6", "make -n", "gate-open"} {
		if strings.Contains(out, old) {
			t.Errorf("%s says %s:\n%s", strings.Join(args, " "), old, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if n := utf8.RuneCountInString(l); n >= 80 || strings.Contains(l, "\t") {
			t.Errorf("%s: a line of %d columns, or with a tab: %q", strings.Join(args, " "), n, l)
		}
		if longDuration.MatchString(l) {
			t.Errorf("%s: a default in Go's long form, not as the synopsis writes it: %q", strings.Join(args, " "), l)
		}
	}
}

// longDuration is a default as Go writes a duration of whole minutes or hours: 30m0s, 2h0m0s.
var longDuration = regexp.MustCompile(`\(default [0-9hm]*[hm]0s\)`)

// TestFlagText: -h's flag text, as usage writes it. A flag's name and value word on a line, its text beneath at 8
// columns, wrapped under 80, then its default: none for a zero value, a duration short, a one-word string bare, any
// other string quoted. A synopsis wraps under 80 at the word after "usage:", a bracketed group whole when it fits.
func TestFlagText(t *testing.T) {
	for d, want := range map[time.Duration]string{30 * time.Minute: "30m", 2 * time.Hour: "2h",
		90 * time.Minute: "1h30m", 90 * time.Second: "1m30s", 300 * time.Millisecond: "300ms", 5 * time.Second: "5s",
		2*time.Hour + 417*time.Millisecond: "2h0m0.417s"} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%s) = %s, want %s", d, got, want)
		}
	}
	f := (&App{Self: "bin/laug"}).newFlags("x y", "-a <n> [-b 30m] [-c gate/results] [-d 'one two'] "+
		"[-e [-f <word>] [-g <word>]] [-long-flag-name <some value>] [-another-long-flag <more>] [-z]")
	f.Duration("b", 30*time.Minute, "how long, in a `length`")
	f.String("c", "gate/results", "the `dir` the runs go in")
	f.String("d", "one two", "a string with a space")
	f.Bool("e", false, "a switch")
	f.Int("a", 0, "a count that has no default worth naming, and text long enough that it goes on to a second line "+
		"and a third, each under 80 columns")
	f.Duration("z", 0, "a duration whose default is zero")
	var b strings.Builder
	f.usage(&b)
	want := "usage: bin/laug x y -a <n> [-b 30m] [-c gate/results] [-d 'one two']\n" +
		"       [-e [-f <word>] [-g <word>]] [-long-flag-name <some value>]\n" +
		"       [-another-long-flag <more>] [-z]\n" +
		"  -a int\n" +
		"        a count that has no default worth naming, and text long enough that it\n" +
		"        goes on to a second line and a third, each under 80 columns\n" +
		"  -b length\n" +
		"        how long, in a length (default 30m)\n" +
		"  -c dir\n" +
		"        the dir the runs go in (default gate/results)\n" +
		"  -d string\n" +
		"        a string with a space (default \"one two\")\n" +
		"  -e\n" +
		"        a switch\n" +
		"  -z duration\n" +
		"        a duration whose default is zero\n"
	if b.String() != want {
		t.Errorf("usage:\n%s\nwant:\n%s", b.String(), want)
	}
}

// TestSynopsisGroups: every verb's synopsis, as its -h wraps it, keeps every word in order, goes on at the word after
// "usage:", and breaks inside no bracket group that fits on a line, at any depth: a group too wide breaks between its
// inner groups, not between their words. A synopsis of the gate run kind, nested, is checked as well, since its group
// is the one that once broke.
func TestSynopsisGroups(t *testing.T) {
	f := (&App{}).newFlags("gate run", "<tag> [-plan gate/plan.yaml] [-items <n>] [-fake [-allow-stale]] "+
		"[-post local] [-results <dir>] [-base main] [-json | -workflow [-orchestrator 127.0.0.1:7400] "+
		"[-otlp 127.0.0.1:<port>]] [-dry-run]")
	var b strings.Builder
	f.synopsisLines(&b)
	if want := "usage: laug gate run <tag> [-plan gate/plan.yaml] [-items <n>]\n" +
		"       [-fake [-allow-stale]] [-post local] [-results <dir>] [-base main]\n" +
		"       [-json | -workflow [-orchestrator 127.0.0.1:7400]\n" +
		"       [-otlp 127.0.0.1:<port>]] [-dry-run]\n"; b.String() != want {
		t.Errorf("a nested group:\n%s\nwant:\n%s", b.String(), want)
	}
	checkSynopsis(t, "a nested group", "gate run", b.String())
	n := 0
	for _, c := range commands {
		if len(c.verbs) > 1 || c.verbs[0].name != "" {
			_, out, _ := runArgs(t, c.name, "-h")
			checkSynopsis(t, c.name+" -h", c.name, out)
		}
		for _, v := range c.verbs {
			args := verbArgs(c, v)
			name := strings.Join(args, " ")
			_, out, _ := runArgs(t, append(slices.Clone(args), "-h")...)
			checkSynopsis(t, name+" -h", name, out)
			_, _, errOut := runArgs(t, append(slices.Clone(args), "-nope")...)
			checkSynopsis(t, name+" -nope", name, errOut)
			n++
		}
	}
	if n != 51 {
		t.Errorf("%d verbs' synopses, want 51: a verb came or went, so say so here", n)
	}
}

// TestFromRoot: a flag that takes a path laug reads from the checkout's root, not from where it runs, says so in its
// -h, as -thresholds does.
func TestFromRoot(t *testing.T) {
	for _, c := range [][]string{{"pool", "serve", "-log"}, {"verdict", "-out"}, {"verdict", "-thresholds"},
		{"fault", "-thresholds"}, {"candidate", "-thresholds"}, {"lease", "-file"}, {"release", "-file"}} {
		args, name := c[:len(c)-1], c[len(c)-1]
		_, out, _ := runArgs(t, append(slices.Clone(args), "-h")...)
		if text := flagText(out, name); !strings.Contains(text, "a relative path starts at the checkout's root") {
			t.Errorf("%s -h, %s: %q says not where a relative path starts", strings.Join(args, " "), name, text)
		}
	}
}

// flagText is one flag's text in a -h's out, its wrapped lines joined by a space.
func flagText(out, name string) string {
	var text []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case l == "  "+name || strings.HasPrefix(l, "  "+name+" "):
			in = true
		case in && strings.HasPrefix(l, flagIndent):
			text = append(text, strings.TrimSpace(l))
		default:
			in = false
		}
	}
	return strings.Join(text, " ")
}

// checkSynopsis holds the usage lines in out, those from "usage: laug <cmd> " to the first flag, to
// TestSynopsisGroups' rules.
func checkSynopsis(t *testing.T, name, cmd, out string) {
	t.Helper()
	head := "usage: laug " + cmd + " "
	var text string
	var breaks []int // where each line after the first starts, in text
lines:
	for _, l := range strings.Split(out, "\n") {
		switch {
		case text == "" && strings.HasPrefix(l, head):
			text = strings.TrimPrefix(l, head)
		case text == "":
		case len(l) > len(synopsisIndent) && strings.HasPrefix(l, synopsisIndent) && l[len(synopsisIndent)] != ' ':
			breaks = append(breaks, len(text)+1)
			text += " " + strings.TrimPrefix(l, synopsisIndent)
		default:
			break lines
		}
	}
	if text == "" {
		t.Errorf("%s: no line starts %q:\n%s", name, head, out)
		return
	}
	if text != strings.Join(strings.Fields(text), " ") {
		t.Errorf("%s: the synopsis lost or doubled a space: %q", name, text)
	}
	var open []int
	for i, r := range text {
		switch r {
		case '[':
			open = append(open, i)
		case ']':
			if len(open) == 0 {
				t.Errorf("%s: a ] with no [ at %d: %q", name, i, text)
				return
			}
			start := open[len(open)-1]
			open = open[:len(open)-1]
			group := text[start : i+1]
			if utf8.RuneCountInString(synopsisIndent+group) > helpWidth {
				continue
			}
			for _, at := range breaks {
				if at > start && at <= i {
					t.Errorf("%s: %q fits on a line, and a line breaks inside it:\n%s", name, group, out)
				}
			}
		}
	}
	if len(open) > 0 {
		t.Errorf("%s: a [ with no ]: %q", name, text)
	}
}

func packageDoc(t *testing.T) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil || f.Doc == nil {
		t.Fatalf("doc.go: no package comment: %v", err)
	}
	return f.Doc.Text()
}

func hasParagraph(doc, name string) bool {
	for _, p := range strings.Split(doc, "\n\n") {
		if strings.HasPrefix(p, name+" ") {
			return true
		}
	}
	return false
}

// hasTest reports whether file declares the test function fn.
func hasTest(t *testing.T, file, fn string) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		return false
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == fn {
			return true
		}
	}
	return false
}

// printOnly are the scripts a bare read verb may hand to the runner: they print or measure, and write nothing in the
// tree or the pool. thresholds runs the first two; host type, given no type, the third; host clock the fourth; cell
// mem, health, dynconfig-check, fi-template-check and softasserts the next five. Those read the local cell or a server
// image through docker, and cell health starts one HealthCheck workflow on the cell, as doc.go's cell paragraph says.
// health check runs the healthcheck role, one HealthCheck workflow on the cell, and health replay go test in
// internal/health, named with the directory it runs in and its first two arguments.
var printOnly = []string{"load/thresholds.sh", "load/print-thresholds.sh", "gate/host-type.sh", "cells/clock-check.sh",
	"cells/mem.sh", "cells/health.sh", "cells/dynconfig-check.sh", "cells/fi-template-check.sh", "cells/softasserts.sh",
	"healthcheck", "cd internal/health && go test ./..."}

// printsOnly says whether a bare read verb may hand c to the runner: its script is in printOnly, alone or with its
// first argument, or with "cd <dir> && " before it and its first two, as go's.
func printsOnly(c script.Command) bool {
	if slices.Contains(printOnly, c.Script) {
		return true
	}
	if len(c.Args) > 0 && slices.Contains(printOnly, c.Script+" "+c.Args[0].Text) {
		return true
	}
	return c.Dir != "" && len(c.Args) > 1 &&
		slices.Contains(printOnly, "cd "+c.Dir+" && "+c.Script+" "+c.Args[0].Text+" "+c.Args[1].Text)
}

// bareAnswer is the exit of a read verb whose bare form gives a negative answer here, where the App has no PATH: deps
// finds no tool, so every one is missing, and it answers 1.
var bareAnswer = map[string]int{"deps": exitFailed}

// TestBareByClass runs each verb bare in an empty directory: a read verb exits 0, or its bareAnswer, a needs or
// destroys verb 2, none writes a file, and none hands a script to the runner but a read verb one of printOnly. An act
// acts, so its own test covers it.
func TestBareByClass(t *testing.T) {
	for _, c := range commands {
		for _, v := range c.verbs {
			if v.class == classAct {
				continue
			}
			args := verbArgs(c, v)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				dir := t.TempDir()
				t.Chdir(dir)
				rec := &recorder{root: dir}
				var stdout, stderr bytes.Buffer
				code := run(context.Background(), &App{Root: dir, Stdout: &stdout, Stderr: &stderr, Runner: rec,
					Roles: rec}, args)
				want := map[class]int{classRead: 0, classNeeds: 2, classDestroys: 2}[v.class]
				if answer, ok := bareAnswer[strings.Join(args, " ")]; ok && v.class == classRead {
					want = answer
				}
				if code != want {
					t.Errorf("bare %s: exit %d, want %d\n%s%s", strings.Join(args, " "), code, want, stdout.String(),
						stderr.String())
				}
				for _, call := range rec.calls {
					if v.class != classRead || !printsOnly(call) {
						t.Errorf("bare %s ran %s", strings.Join(args, " "), call.Script)
					}
				}
				if ents, _ := os.ReadDir(dir); len(ents) != 0 {
					t.Errorf("bare %s wrote %d entries", strings.Join(args, " "), len(ents))
				}
			})
		}
	}
}

// TestSelf: a line laug prints for a person to type starts with the App's word, laug when PATH finds one and
// bin/laug when not, never a path from argv. With the word bin/laug, the help, every command's usage, every verb's -h
// and every verb's bare form but an act's name no laug command without it.
func TestSelf(t *testing.T) {
	if got := selfWord(func(string) (string, error) { return "/elsewhere/laug", nil }); got != "laug" {
		t.Errorf("selfWord, a laug on PATH: %q, want laug", got)
	}
	if got := selfWord(func(string) (string, error) { return "", exec.ErrNotFound }); got != "bin/laug" {
		t.Errorf("selfWord, none on PATH: %q, want bin/laug", got)
	}
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	os.Args = []string{"/elsewhere/checkout/bin/laug", "help"}
	onPath := t.TempDir()
	writeExec(t, filepath.Join(onPath, "laug"), "#!/bin/sh\n")
	for path, want := range map[string]string{onPath: "laug", t.TempDir(): "bin/laug"} {
		t.Setenv("PATH", path)
		if got := newApp().Self; got != want {
			t.Errorf("newApp with argv[0] %s and PATH %s: Self %q, want %q", os.Args[0], path, got, want)
		}
	}

	names := make([]string, 0, len(commands))
	for _, c := range commands {
		names = append(names, c.name)
	}
	bare := regexp.MustCompile(`(^|[^/\w-])laug (` + strings.Join(names, "|") + `)\b`)
	runs := [][]string{{}, {"help"}}
	for _, c := range commands {
		if len(c.verbs) > 1 || c.verbs[0].name != "" {
			runs = append(runs, []string{c.name}, []string{c.name, "-h"})
		}
		for _, v := range c.verbs {
			runs = append(runs, append(verbArgs(c, v), "-h"))
			if v.class != classAct {
				runs = append(runs, verbArgs(c, v))
			}
		}
	}
	for _, args := range runs {
		dir := t.TempDir()
		t.Chdir(dir)
		rec := &recorder{root: dir}
		var stdout, stderr bytes.Buffer
		run(context.Background(), &App{Root: dir, Stdout: &stdout, Stderr: &stderr, Runner: rec, Roles: rec,
			Self: "bin/laug"}, args)
		for _, l := range strings.Split(stdout.String()+stderr.String(), "\n") {
			if bare.MatchString(l) {
				t.Errorf("laug %s names a laug command without bin/: %q", strings.Join(args, " "), l)
			}
		}
	}
}

// TestVerbs drives a command with named verbs, a stand-in until the first real one: bare, it prints the command's
// usage on stderr with 2, as an unknown verb does; -h prints it on stdout with 0; a verb gets the arguments after
// its name; help <command> prints the usage as -h does. The table rules hold the stand-in too.
func TestVerbs(t *testing.T) {
	saved := commands
	t.Cleanup(func() { commands = saved })
	var got []string
	skim := func(_ context.Context, a *App, args []string) int {
		f := a.newFlags("pond skim", "[-deep] [<arguments>]")
		deep := f.Bool("deep", false, "skim the bottom too")
		pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
		if !ok {
			return code
		}
		got = append(pos, fmt.Sprint(*deep))
		return exitDone
	}
	drain := func(_ context.Context, a *App, args []string) int {
		f := a.newFlags("pond drain", "-remove-water")
		if _, code, ok := f.parse(args, a.Stdout, a.Stderr); !ok {
			return code
		}
		fmt.Fprintln(a.Stderr, "laug · pond drain: would remove 1 pond; -remove-water removes it")
		return exitUsage
	}
	pond := command{"pond", groupDaily, "keep a stand-in pond", []verb{{"skim", classRead, skim, "skim the top"},
		{"drain", classDestroys, drain, "drain the pond; bare, says what goes"}}}
	commands = append(slices.Clone(saved), pond)

	usageFirst := "laug · pond: keep a stand-in pond"
	for _, c := range []struct {
		name     string
		args     []string
		code     int
		out, err string // the first line of each stream; "" wants it empty
	}{
		{"bare", []string{"pond"}, 2, "", usageFirst},
		{"-h", []string{"pond", "-h"}, 0, usageFirst, ""},
		{"help pond", []string{"help", "pond"}, 0, usageFirst, ""},
		{"help pond skim", []string{"help", "pond", "skim"}, 0, "laug · pond skim: skim the top", ""},
		{"help pond fill", []string{"help", "pond", "fill"}, 2, "", `laug · pond: unknown verb "fill"`},
		{"unknown verb", []string{"pond", "fill"}, 2, "", `laug · pond: unknown verb "fill"`},
		{"a typo", []string{"pond", "skmi"}, 2, "", `laug · pond: unknown verb "skmi"; did you mean skim?`},
		{"a flag first", []string{"pond", "-json"}, 2, "", `laug · pond: unknown verb "-json"`},
		{"skim", []string{"pond", "skim", "a", "-deep"}, 0, "", ""},
		{"drain", []string{"pond", "drain"}, 2, "", "laug · pond drain: would remove 1 pond; -remove-water removes it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got = nil
			code, out, errOut := runArgs(t, c.args...)
			if code != c.code || firstLine(out) != c.out || firstLine(errOut) != c.err {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
			}
		})
	}
	if _, out, _ := runArgs(t, "pond", "-h"); !strings.Contains(out, "\n  skim   skim the top\n  drain  drain the pond; "+
		"bare, says what goes\n") {
		t.Errorf("pond -h lists no verbs, a line each with its summary:\n%s", out)
	}
	if runArgs(t, "pond", "skim", "a", "-deep"); !reflect.DeepEqual(got, []string{"a", "true"}) {
		t.Errorf("skim got %q, want [a true]", got)
	}
	checkVerbs(t, pond)
}

// jsonEnviron is the environment TestJSONContract gives a verb that the test's own would not answer the same on every
// host: deps asks each tool on PATH its version, so it asks depsStubs, which all answer, and holds bin/laug to their
// git's HEAD, so its stamp is that HEAD.
var jsonEnviron = map[string]func(t *testing.T) []string{
	"deps": func(t *testing.T) []string { stamp(t, headSHA, nil); return depsWorld(t, nil) },
}

// TestJSONContract runs every read verb that lists -json with it: one JSON object on stdout, snake_case keys, no
// "laug ·" line, nothing on stderr, exit 0. A verb whose document a script prints runs it for real in this checkout,
// with yq and jq, as thresholds does; one in jsonEnviron runs in the environment it gives.
func TestJSONContract(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	environ := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	for _, c := range commands {
		for _, v := range c.verbs {
			args := verbArgs(c, v)
			name := strings.Join(args, " ")
			_, help, _ := runArgs(t, append(args, "-h")...)
			if v.class != classRead || !strings.Contains(help, "  -json") {
				continue
			}
			t.Run(name, func(t *testing.T) {
				env := environ
				if f, ok := jsonEnviron[name]; ok {
					env = f(t)
				}
				code, out, errOut := runApp(t, &App{Root: root, Environ: env, Runner: script.Real},
					append(args, "-json")...)
				if code != exitDone {
					needTools(t, name+" -json prints what a script makes with it", "yq", "jq")
				}
				if code != exitDone || errOut != "" {
					t.Fatalf("%s -json: exit %d, want 0; stderr %q", name, code, errOut)
				}
				if strings.Contains(out, "laug ·") {
					t.Errorf("%s -json printed a laug · line:\n%s", name, out)
				}
				dec := json.NewDecoder(strings.NewReader(out))
				var obj map[string]any
				if err := dec.Decode(&obj); err != nil {
					t.Fatalf("%s -json: not one JSON object: %v\n%s", name, err, out)
				}
				if dec.More() || strings.TrimSpace(out[dec.InputOffset():]) != "" {
					t.Errorf("%s -json: more than one document:\n%s", name, out)
				}
				for k := range obj {
					if !snake.MatchString(k) {
						t.Errorf("%s -json: key %q is not snake_case", name, k)
					}
				}
			})
		}
	}
}

// TestNoHomePath: with the checkout, TMPDIR and a tool on PATH all under a planted $HOME, no line laug prints holds the
// home path; each path in it is relative to the checkout's root, or ~/ and the rest. It runs every verb with -h, with
// a flag it does not hold and with -dry-run, every verb but an act bare, and every -dry-run golden; then the lines that
// print an error from the file system or exec, which names its path whole: a last-lease file that is a directory, a
// catalog that is one, a TMPDIR that is not there, a gate run whose verdict.json is a directory, and a tool on PATH
// that cannot run.
func TestNoHomePath(t *testing.T) {
	saved := poolDefault
	poolDefault = pool.DefaultListen
	t.Cleanup(func() { poolDefault = saved })
	planted := func(t *testing.T, files map[string]string) (*App, *recorder, string) {
		t.Helper()
		home := t.TempDir()
		root, bin, tmp := filepath.Join(home, "laug"), filepath.Join(home, "bin"), filepath.Join(home, "tmp")
		for _, f := range []string{"go.mod", "gate/run.sh"} {
			writeFile(t, filepath.Join(root, f), "")
		}
		for _, p := range goldenInputs {
			b, err := os.ReadFile(filepath.Join("..", "..", p))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, p), string(b))
		}
		for p, c := range files {
			writeFile(t, filepath.Join(root, p), c)
		}
		writeExec(t, filepath.Join(bin, "docker"), dockerStub)
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			t.Fatal(err)
		}
		rec := &recorder{root: root}
		return &App{Root: root, Environ: []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + tmp},
			Runner: rec, Roles: rec, Self: "bin/laug", Now: func() time.Time { return dryRunClock }}, rec, home
	}
	check := func(t *testing.T, a *App, home string, args ...string) {
		t.Helper()
		code, out, errOut := runApp(t, a, args...)
		if strings.Contains(out+errOut, home) {
			t.Errorf("%s: exit %d, a line holds the home path %s\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "),
				code, home, out, errOut)
		}
	}
	for _, c := range commands {
		for _, v := range c.verbs {
			runs := [][]string{{"-h"}, {"-nope"}, {"-dry-run"}}
			if v.class != classAct {
				runs = append(runs, nil)
			}
			for _, more := range runs {
				a, _, home := planted(t, nil)
				check(t, a, home, append(verbArgs(c, v), more...)...)
			}
		}
	}
	leaseDoc, err := os.ReadFile(filepath.Join("testdata", "dry-run", "lease.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range loadGoldens(t) {
		files := map[string]string{}
		for p, c := range g.files {
			files[p] = c + "\n"
		}
		if g.lease {
			files[poolapi.LastLeaseFile] = string(leaseDoc)
		}
		a, _, home := planted(t, files)
		args := slices.Clone(g.laug)
		if !g.inGo() {
			args = append(args, "-dry-run")
		}
		check(t, a, home, args...)
	}
	// The four verbs that print script.DryRun's line themselves, each given a path under $HOME outside the checkout:
	// prepare sets the line's Home, so the path is ~/ and the rest.
	t.Run("the verbs that print script.DryRun's line", func(t *testing.T) {
		a, _, home := planted(t, nil)
		file := filepath.Join(home, "fixtures", "h.json")
		writeFile(t, file, "{}\n")
		for _, args := range [][]string{{"thresholds", "-thresholds", file}, {"health", "replay", file},
			{"host", "type", "-json"}, {"orchestrator", "server"}} {
			check(t, a, home, append(args, "-dry-run")...)
		}
	})
	// Errors that name a path: each is printed, and its path is shown as the rest are.
	t.Run("a last-lease file that is a directory", func(t *testing.T) {
		a, _, home := planted(t, map[string]string{poolapi.LastLeaseFile + "/x": ""})
		for _, verb := range []string{"release", "reset"} {
			check(t, a, home, verb, "-last")
			check(t, a, home, verb)
		}
	})
	t.Run("a catalog that is a directory", func(t *testing.T) {
		a, _, home := planted(t, nil)
		os.Remove(filepath.Join(a.Root, "faults", "catalog.yaml"))
		writeFile(t, filepath.Join(a.Root, "faults", "catalog.yaml", "x"), "")
		for _, args := range [][]string{{"raun"}, {"fault"}, {"raun", "nope"}} {
			check(t, a, home, args...)
		}
	})
	t.Run("a TMPDIR that is not there", func(t *testing.T) {
		a, rec, home := planted(t, map[string]string{"load/verdict.sh": "", "load/results/r1.verdict.json": "{}\n"})
		rec.run = func(_ script.Command, _ []string, stdout, _ io.Writer) int {
			fmt.Fprint(stdout, "VERDICT: BLOCK\n")
			return 1
		}
		a.Environ = append(a.Environ[:2:2], "TMPDIR="+filepath.Join(home, "gone"))
		check(t, a, home, "verdict", "r1", "-json")
	})
	t.Run("a verdict.json that is a directory", func(t *testing.T) {
		a, _, home := planted(t, map[string]string{"gate/results/g20261027-120000/verdict.json/x": ""})
		check(t, a, home, "gate", "open", "g20261027-120000")
	})
	t.Run("gate control's results dir that is a file", func(t *testing.T) {
		a, _, home := planted(t, nil)
		results := filepath.Join(home, "tmp", "laug-control-x", "results")
		writeFile(t, results, "")
		_, err := onlyRun(results, a.pathShown(results))
		if want := "~/tmp/laug-control-x/results: not a directory"; err == nil || err.Error() != want {
			t.Errorf("onlyRun of a file: %v, want %q", err, want)
		}
	})
	t.Run("a tool that cannot run", func(t *testing.T) {
		a, _, home := planted(t, nil)
		writeFile(t, filepath.Join(home, "bin", "docker"), "not a program\n") // on PATH, not executable
		check(t, a, home, "cell", "down")
		check(t, a, home, "pool", "down")
	})
	// pool serve and lease print a path given or made from one: each is shown as the rest are.
	t.Run("pool serve's pid file that cannot be written", func(t *testing.T) {
		a, _, home := planted(t, map[string]string{pidFile + ".new/x": ""}) // a directory where the new file goes
		check(t, a, home, "pool", "serve", "-min", "0", "-max", "1", "-listen", "127.0.0.1:0")
	})
	t.Run("pool serve's -log under HOME", func(t *testing.T) {
		a, _, home := planted(t, nil)
		writeFile(t, filepath.Join(home, "a-file"), "")
		check(t, a, home, "pool", "serve", "-listen", "127.0.0.1:0", "-log", filepath.Join(home, "a-file", "x.log"))
		s := startServeApp(t, a, "-listen", "127.0.0.1:0", "-min", "0", "-max", "1", "-log",
			filepath.Join(home, "logs", "pool.log"))
		code, _ := s.stop(t)
		if got := s.stdout.String() + s.stderr.String(); strings.Contains(got, home) {
			t.Errorf("pool serve -log under HOME: exit %d, a line holds the home path %s:\n%s", code, home, got)
		}
	})
	t.Run("lease's -file under HOME", func(t *testing.T) {
		s := startServe(t, "", "-listen", "127.0.0.1:0", "-min", "0", "-max", "2")
		a, _, home := planted(t, nil)
		writeFile(t, filepath.Join(home, "a-file"), "")
		check(t, a, home, "lease", "-pool", s.addr, "-file", filepath.Join(home, "l.json"))
		check(t, a, home, "lease", "-pool", s.addr, "-file", filepath.Join(home, "a-file", "l.json"))
	})
}

// TestTextShown: a path in a line laug prints is written from the checkout's root, or as ~/ and the rest, wherever it
// starts in the line: first, after a space, a quote, =, :, ( or [; in single quotes with its ~/ outside them. A path
// that only shares the root's or the home's first letters, or one inside a word, stays as it is.
func TestTextShown(t *testing.T) {
	a := &App{Root: "/h/u/laug", Environ: []string{"HOME=/h/u"}}
	for in, want := range map[string]string{
		"open /h/u/laug/gate/results/x: permission denied": "open gate/results/x: permission denied",
		"/h/u/laug/x and /h/u/other/y":                     "x and ~/other/y",
		`"/h/u/a" '/h/u/b c' '/h/u/laug/d e'`:              `"~/a" ~/'b c' 'd e'`,
		"PATH=/h/u/bin:/h/u/go/bin (in [/h/u/x])":          "PATH=~/bin:~/go/bin (in [~/x])",
		"/h/u/laugh/x /h/uv/y /x/h/u/z /h/u":               "~/laugh/x /h/uv/y /x/h/u/z /h/u",
		"no path at all":                                   "no path at all",
	} {
		if got := a.textShown(in); got != want {
			t.Errorf("textShown(%q) = %q, want %q", in, got, want)
		}
	}
	if got := (&App{Environ: []string{"HOME=/"}}).textShown("/etc/x"); got != "/etc/x" {
		t.Errorf("HOME=/: %q", got)
	}
}
