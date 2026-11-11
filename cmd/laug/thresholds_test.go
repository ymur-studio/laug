package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// TestThresholds: the env matrix, then the two scripts in turn. thresholds runs load/thresholds.sh with the tag, the
// default's when none is given, and THRESHOLDS only when -thresholds is; then load/print-thresholds.sh, -json with
// -json, on what the first printed. A resolve that fails exits 2, as the recipe's "|| exit 2" did, and prints nothing.
func TestThresholds(t *testing.T) {
	runMatrix(t, []envCase{
		{name: "bare", args: []string{"thresholds"}, how: script.Run, script: "load/thresholds.sh",
			argv: []string{serverTagDefault}, strays: []string{"THRESHOLDS"}},
		{name: "a tag and a set", args: []string{"thresholds", "-server-tag", "1.32.1", "-thresholds",
			"load/steady-state.yaml"}, how: script.Run, script: "load/thresholds.sh", argv: []string{"1.32.1"},
			knobs: []string{"THRESHOLDS=load/steady-state.yaml"}, strays: []string{"THRESHOLDS"}},
		{name: "-dry-run", args: []string{"thresholds", "-server-tag", "1.32.1", "-dry-run"},
			strays: []string{"THRESHOLDS"},
			out:    `th=$(load/thresholds.sh '1.32.1') || exit 2; load/print-thresholds.sh <<<"$th"`},
		{name: "-dry-run -json", args: []string{"thresholds", "-json", "-dry-run", "-thresholds", "x.yaml"},
			strays: []string{"THRESHOLDS"},
			out:    `th=$(THRESHOLDS=x.yaml load/thresholds.sh '1.31.0') || exit 2; load/print-thresholds.sh -json <<<"$th"`},
		{name: "an argument", args: []string{"thresholds", "1.32.1"}, code: 2,
			errOut: `laug · thresholds: takes no arguments, got ["1.32.1"]`},
		// A tag that is not x.y.z is refused before either script runs; a release with no file, or none, gets the base.
		{name: "not a release tag", args: []string{"thresholds", "-server-tag", "nope"}, code: 2,
			errOut: "laug · thresholds: -server-tag nope: not a release tag, x.y.z; -server-tag '' gets the base"},
		{name: "major.minor alone", args: []string{"thresholds", "-server-tag", "1.32", "-dry-run"}, code: 2,
			errOut: "laug · thresholds: -server-tag 1.32: not a release tag, x.y.z; -server-tag '' gets the base"},
		{name: "a release with no file", args: []string{"thresholds", "-server-tag", "1.33.0"}, how: script.Run,
			script: "load/thresholds.sh", argv: []string{"1.33.0"}, strays: []string{"THRESHOLDS"}},
		{name: "none", args: []string{"thresholds", "-server-tag", ""}, how: script.Run, script: "load/thresholds.sh",
			argv: []string{""}, strays: []string{"THRESHOLDS"}},
	})

	for _, c := range []struct {
		name     string
		args     []string
		resolved int // load/thresholds.sh's exit
		code     int
		show     []string // load/print-thresholds.sh's arguments; nil when it does not run
	}{
		{"text", nil, 0, 0, []string{}},
		{"-json", []string{"-json"}, 0, 0, []string{"-json"}},
		{"a set that does not resolve", nil, 2, 2, nil},
		{"a resolve that exits 1", nil, 1, 2, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := matrixApp(t, envCase{})
			rec.run = func(s script.Command, _ []string, stdout, _ io.Writer) int {
				if s.Script == "load/thresholds.sh" {
					io.WriteString(stdout, "window: 300\nresolved:\n  note: n\n")
					return c.resolved
				}
				io.WriteString(stdout, "laug · thresholds: n\n")
				return 0
			}
			code, out, errOut := runApp(t, a, append([]string{"thresholds"}, c.args...)...)
			if code != c.code {
				t.Fatalf("exit %d, want %d\nstderr:\n%s", code, c.code, errOut)
			}
			if c.show == nil {
				if len(rec.calls) != 1 || out != "" {
					t.Errorf("%d scripts ran, stdout %q; want the resolve alone and nothing printed", len(rec.calls), out)
				}
				return
			}
			var args []string
			for _, v := range rec.calls[1].Args {
				args = append(args, v.Text)
			}
			if len(rec.calls) != 2 || rec.calls[1].Script != "load/print-thresholds.sh" ||
				strings.Join(args, " ") != strings.Join(c.show, " ") || rec.hows[1] != script.Run ||
				rec.stdins[1] != "window: 300\nresolved:\n  note: n\n" || out != "laug · thresholds: n\n" {
				t.Errorf("then %s %q by %v, stdin %q, stdout %q", rec.calls[1].Script, args, rec.hows[1], rec.stdins[1],
					out)
			}
		})
	}
}

// TestPrintThresholds: load/print-thresholds.sh is the thresholds recipe of the Makefile laug replaced, as make -n
// printed it, its first line aside: line for line, with each "; \" a line end. Then, with yq and jq on PATH, laug
// thresholds prints what that recipe printed, byte for byte, for a release with a file, one without, no tag and a set
// given, and exits as it did.
func TestPrintThresholds(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "dry-run", "thresholds.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var recipe []string
	for _, l := range strings.Split(strings.TrimSpace(string(golden)), "\n") {
		if !strings.HasPrefix(l, "# ") {
			recipe = append(recipe, strings.TrimSuffix(l, "; \\"))
		}
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "load", "print-thresholds.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Split(strings.TrimSpace(string(src)), "\n")
	i := 0
	for i < len(body) && !strings.HasPrefix(body[i], "echo ") {
		i++
	}
	if first := `th=$(load/thresholds.sh '1.31.0') || exit 2`; recipe[0] != first {
		t.Fatalf("the golden's recipe starts %q, want %q", recipe[0], first)
	}
	if got, want := strings.Join(body[i:], "\n"), strings.Join(recipe[1:], "\n"); got != want {
		t.Errorf("load/print-thresholds.sh's body is not the recipe's:\n got:\n%s\nwant:\n%s", got, want)
	}

	needTools(t, "the recipe and laug thresholds run it", "yq", "jq", "bash")
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tag, set string }{{"1.31.0", ""}, {"1.32.1", ""}, {"", ""},
		{"1.32.1", "load/steady-state.yaml"}, {"1.31.0", "load/nope.yaml"}} {
		t.Run(c.tag+" "+c.set, func(t *testing.T) {
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
			old := exec.Command("bash", "-o", "pipefail", "-c", strings.Replace(strings.Join(recipe, "\n"), "'1.31.0'",
				"'"+c.tag+"'", 1))
			old.Dir, old.Env = root, env
			if c.set != "" {
				old.Env = append(old.Env, "THRESHOLDS="+c.set)
			}
			var oldOut bytes.Buffer
			old.Stdout = &oldOut
			oldCode, err := script.ExitCode(old.Run())
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"thresholds", "-server-tag", c.tag}
			if c.set != "" {
				args = append(args, "-thresholds", c.set)
			}
			code, out, errOut := runApp(t, &App{Root: root, Environ: env, Runner: script.Real}, args...)
			if code != oldCode || out != oldOut.String() || oldOut.Len() == 0 && c.set == "" {
				t.Errorf("laug: exit %d, the recipe %d\nlaug:\n%s\nthe recipe:\n%s\nstderr:\n%s", code, oldCode, out,
					oldOut.String(), errOut)
			}
		})
	}
}
