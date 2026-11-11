package main

// The rules cmd/laug's main_test.go holds every laug command to, held here to bin/obs's verbs: help and mistakes on
// the right stream with the right code, a paragraph and a test for each command, a class for each verb kept bare, a
// -h under 80 columns that starts "laug · <verb>: <summary>", -json one object, and no home path in a line.

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/ymur-studio/laug/internal/script"
)

// verbArgs is how a verb is named on the command line: the command, then the verb unless it has no name.
func verbArgs(c command, v verb) []string {
	if v.name == "" {
		return []string{c.name}
	}
	return []string{c.name, v.name}
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
		{"bare", nil, 2, "", "usage: bin/obs <verb>", header},
		{"help", []string{"help"}, 0, "usage: bin/obs <verb>", "", header},
		{"-h", []string{"-h"}, 0, "usage: bin/obs <verb>", "", header},
		{"--help", []string{"--help"}, 0, "usage: bin/obs <verb>", "", header},
		{"unknown", []string{"fleet"}, 2, "", "usage: bin/obs <verb>", `laug · obs: unknown verb "fleet"`},
		{"a typo", []string{"dwon"}, 2, "", `"bin/obs help" lists the verbs.`,
			`laug · obs: unknown verb "dwon"; did you mean down?`},
		{"up -h", []string{"up", "-h"}, 0, "usage: bin/obs up [-only <part>]", "", "laug · obs up: start the"},
		{"help up", []string{"help", "up"}, 0, "usage: bin/obs up [-only <part>]", "", "laug · obs up: start the"},
		{"usage", []string{"usage"}, 2, "", "usage: bin/obs usage <verb>", "laug · usage: record and sum"},
		{"usage -h", []string{"usage", "-h"}, 0, "\n  record  write cgroup samples", "", "laug · usage: record"},
		{"usage, unknown verb", []string{"usage", "recrod"}, 2, "", "usage: bin/obs usage <verb>",
			`laug · usage: unknown verb "recrod"; did you mean record?`},
		{"help usage record", []string{"help", "usage", "record"}, 0, "usage: bin/obs usage record [-interval 5s]",
			"", "laug · usage record: write cgroup samples"},
		{"help help", []string{"help", "help"}, 0, "usage: bin/obs help [<verb> | usage [<verb>]]", "",
			"laug · help: print this list"},
		{"help, unknown", []string{"help", "fleet"}, 2, "", "usage: bin/obs <verb>",
			`laug · help: unknown verb "fleet"`},
		{"help, a verb for a command with none", []string{"help", "up", "x"}, 2, "", "usage: bin/obs help",
			"laug · help: up has no verbs; bin/obs help up prints its flags"},
		{"help, three", []string{"help", "usage", "record", "x"}, 2, "", "usage: bin/obs help",
			`laug · help: takes a verb, or usage and a verb, at most, got ["usage" "record" "x"]`},
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

// TestHelp: the list starts with the header and ends with the exit line, and lists each command once, under its
// group's heading.
func TestHelp(t *testing.T) {
	code, out, errOut := runArgs(t, "help")
	if code != 0 || errOut != "" {
		t.Fatalf("help: exit %d, stderr %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != header || lines[len(lines)-1] != exitLine {
		t.Errorf("first line %q, last %q", lines[0], lines[len(lines)-1])
	}
	for _, c := range commands {
		heading := groupHeadings[c.group]
		i := slices.Index(lines, heading)
		j := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "  "+c.name+" ") })
		if i < 0 || j < i || strings.Count(out, "\n  "+c.name+" ") != 1 {
			t.Errorf("%s: not listed once under %q:\n%s", c.name, heading, out)
		}
	}
}

// kebab is the shape of a command's or a verb's name.
var kebab = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// TestTableRules holds every row to the table's rules, as cmd/laug's TestTableRules: a name, a group, a summary
// whose help line stays under 80 columns, a paragraph in doc.go, and verbs, each with a class and a -h that starts
// "laug · <verb>: <summary>". The stack's verbs are tested in obs_test.go and the usage verbs in usage_test.go.
func TestTableRules(t *testing.T) {
	var help bytes.Buffer
	usage(&help, obsBin)
	for _, l := range strings.Split(help.String(), "\n") {
		if n := utf8.RuneCountInString(l); n >= 80 {
			t.Errorf("a help line of %d columns: %q", n, l)
		}
	}
	doc := packageDoc(t)
	tests := map[string]bool{"usage": hasTest(t, "usage_test.go", "TestUsage"), "help": hasTest(t, "main_test.go",
		"TestHelp")}
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
		if ok, named := tests[c.name]; named && !ok || c.group == groupStack && !hasTest(t, "obs_test.go", "TestObs") {
			t.Errorf("%s: no test", c.name)
		}
		for _, v := range c.verbs {
			if v.class != classRead && v.class != classAct && v.class != classNeeds && v.class != classDestroys {
				t.Errorf("%s %s: no class", c.name, v.name)
			}
			checkHelp(t, append(verbArgs(c, v), "-h")...)
		}
		if len(c.verbs) > 1 {
			var w strings.Builder
			c.usage(&w, obsBin)
			for _, l := range strings.Split(w.String(), "\n") {
				if n := utf8.RuneCountInString(l); n >= 80 {
					t.Errorf("%s -h: a line of %d columns: %q", c.name, n, l)
				}
			}
		}
	}
}

// checkHelp runs args, which end in -h: exit 0, nothing on stderr, stdout's first line "laug · ", the verb as its
// lines name it and its summary, every line under 80 columns, and no line with a tab.
func checkHelp(t *testing.T, args ...string) {
	t.Helper()
	code, out, errOut := runArgs(t, args...)
	name := strings.Join(args[:len(args)-1], " ")
	summary, _ := summaryOf(name)
	cmd := name
	if c, ok := lookup(args[0]); ok && c.group == groupStack {
		cmd = "obs " + name
	}
	want := "laug · " + cmd + ": " + summary
	if first := firstLine(out); code != 0 || errOut != "" || summary == "" || first != want ||
		utf8.RuneCountInString(first) >= 80 {
		t.Errorf("%s: exit %d, want 0; first stdout line %q, want %q, under 80 columns\nstderr:\n%s",
			strings.Join(args, " "), code, first, want, errOut)
	}
	for _, l := range strings.Split(out, "\n") {
		if n := utf8.RuneCountInString(l); n >= 80 || strings.Contains(l, "\t") {
			t.Errorf("%s: a line of %d columns, or with a tab: %q", strings.Join(args, " "), n, l)
		}
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

// printOnly are the _obs/obs.sh verbs a bare read verb may hand to the runner: status and find read, and the script's
// other verbs act.
var printOnly = []string{"status", "find"}

// bareAnswer is the exit of a read verb whose bare form gives a negative answer here, where the checkout holds no
// gate run: find finds none to read, the newest one being its bare subject, and answers 1.
var bareAnswer = map[string]int{"find": exitFailed}

// TestBareByClass runs each verb bare in an empty directory: a read verb exits 0, or its bareAnswer, a needs or
// destroys verb 2, none writes a file, and none hands a script to the runner but a read verb one of printOnly. An act
// acts, so its own test covers it.
func TestBareByClass(t *testing.T) {
	usageTree(t)
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
				env, _ := stubDocker(t, false)
				code := run(context.Background(), &App{Root: dir, Environ: env, Stdout: &stdout, Stderr: &stderr,
					Runner: rec}, args)
				want := map[class]int{classRead: 0, classNeeds: 2, classDestroys: 2}[v.class]
				if answer, ok := bareAnswer[strings.Join(args, " ")]; ok && v.class == classRead {
					want = answer
				}
				if code != want {
					t.Errorf("bare %s: exit %d, want %d\n%s%s", strings.Join(args, " "), code, want, stdout.String(),
						stderr.String())
				}
				for _, call := range rec.calls {
					if v.class != classRead || call.Script != obsScript || len(call.Args) == 0 ||
						!slices.Contains(printOnly, call.Args[0].Text) {
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

// TestSelf: a line this binary prints for a person to type starts with bin/obs, never with laug, and bin/obs takes
// the stack's verbs without the word obs: the help, the usage verbs' list, every verb's -h and every verb's bare form
// but an act's name no laug command and no bin/obs obs.
func TestSelf(t *testing.T) {
	if a := newApp(); a.Self != obsBin {
		t.Errorf("newApp: Self %q, want %s", a.Self, obsBin)
	}
	bare := regexp.MustCompile(`(^|[^·] )(bin/)?laug (obs|usage|up|down|status|find|backfill|follow|pack|view|purge)\b|` +
		`bin/obs obs\b`)
	runs := [][]string{{}, {"help"}, {"usage"}, {"usage", "-h"}}
	for _, c := range commands {
		for _, v := range c.verbs {
			runs = append(runs, append(verbArgs(c, v), "-h"))
			if v.class != classAct {
				runs = append(runs, verbArgs(c, v))
			}
		}
	}
	usageTree(t)
	for _, args := range runs {
		dir := t.TempDir()
		t.Chdir(dir)
		var stdout, stderr bytes.Buffer
		env, _ := stubDocker(t, false)
		run(context.Background(), &App{Root: dir, Environ: env, Stdout: &stdout, Stderr: &stderr,
			Runner: &recorder{root: dir}}, args)
		for _, l := range strings.Split(stdout.String()+stderr.String(), "\n") {
			if bare.MatchString(l) {
				t.Errorf("bin/obs %s names a laug command: %q", strings.Join(args, " "), l)
			}
		}
	}
}

// jsonEnviron is the environment TestJSONContract gives a verb: usage sample lists containers with the docker on
// PATH, so it gets a stub that lists none, and reads the dev tree of cellstat's testdata, and no test runs Docker.
var jsonEnviron = map[string]func(t *testing.T) []string{
	"usage sample": func(t *testing.T) []string { usageTree(t); env, _ := stubDocker(t, false); return env },
}

// jsonAnswer is the exit of a read verb whose -json gives a negative answer in TestJSONContract's environment, its
// object printed all the same: status, with no stack under the test's HOME, answers 1, as with a part down.
var jsonAnswer = map[string]int{"status": exitFailed}

// TestJSONContract runs every read verb that lists -json with it: one JSON object on stdout, snake_case keys, no
// "laug ·" line, nothing on stderr, exit 0 or its jsonAnswer. status runs _obs/obs.sh for real in this checkout, with
// jq; one in jsonEnviron runs in the environment it gives.
func TestJSONContract(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	environ := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	n := 0
	for _, c := range commands {
		for _, v := range c.verbs {
			args := verbArgs(c, v)
			name := strings.Join(args, " ")
			_, help, _ := runArgs(t, append(args, "-h")...)
			if v.class != classRead || !strings.Contains(help, "  -json") {
				continue
			}
			n++
			t.Run(name, func(t *testing.T) {
				env := environ
				if f, ok := jsonEnviron[name]; ok {
					env = f(t)
				}
				code, out, errOut := runApp(t, &App{Root: root, Environ: env, Runner: script.Real},
					append(args, "-json")...)
				if _, err := exec.LookPath("jq"); err != nil && code != jsonAnswer[name] {
					t.Skipf("jq is not on PATH: %s -json prints what a script makes with it", name)
				}
				if code != jsonAnswer[name] || errOut != "" {
					t.Fatalf("%s -json: exit %d, want %d; stderr %q", name, code, jsonAnswer[name], errOut)
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
	if n != 2 {
		t.Errorf("%d read verbs list -json, want 2, status and usage sample", n)
	}
}

// TestNoHomePath: with the checkout and a tool on PATH under a planted $HOME, no line this binary prints holds the home
// path. It runs every verb with -h, with a flag it does not hold and with -dry-run, every verb but an act bare, and
// every -dry-run golden; then status -json -dry-run, which prints script.DryRun's line itself, with -dir under $HOME;
// and usage record's -out under a link to nothing, a path it prints in an error.
func TestNoHomePath(t *testing.T) {
	usageTree(t)
	goldens := loadGoldens(t)
	planted := func(t *testing.T, files map[string]string) (*App, string) {
		t.Helper()
		home := t.TempDir()
		root, bin := filepath.Join(home, "laug"), filepath.Join(home, "bin")
		for _, f := range []string{"go.mod", "gate/run.sh"} {
			writeFile(t, filepath.Join(root, f), "")
		}
		for p, c := range files {
			writeFile(t, filepath.Join(root, p), c)
		}
		writeExec(t, filepath.Join(bin, "docker"), "#!/bin/sh\nexit 0\n")
		t.Chdir(root)
		return &App{Root: root, Environ: []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home},
			Runner: &recorder{root: root}, Self: obsBin}, home
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
				a, home := planted(t, nil)
				check(t, a, home, append(verbArgs(c, v), more...)...)
			}
		}
	}
	for _, g := range goldens {
		files := map[string]string{}
		for p, c := range g.files {
			files[p] = c + "\n"
		}
		a, home := planted(t, files)
		check(t, a, home, append(slices.Clone(g.obs), "-dry-run")...)
	}
	a, home := planted(t, nil)
	check(t, a, home, "status", "-json", "-dir", filepath.Join(home, "obs"), "-dry-run")
	a, home = planted(t, nil)
	if err := os.Symlink(filepath.Join(home, "gone"), filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	check(t, a, home, "usage", "record", "-out", filepath.Join(home, "link", "u.jsonl"))
}
