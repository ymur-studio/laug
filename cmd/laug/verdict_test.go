package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// verdictStrays are the knobs verdict maps that strayEnviron holds with another value.
var verdictStrays = []string{"PROM_URL", "SWEEP_MARGIN_S", "THRESHOLDS", "VERDICT_JSON"}

// TestVerdict: the env matrix, then the verdict line and the exits. verdict runs load/verdict.sh as a child with the
// Prometheus given as PROM_URL and, for a run that recorded none, the one given, else the leased cell's, else block
// 0's; -thresholds and -sweep-margin only when given. Its stdout passes through but for the verdict line, whose word
// goes through writeVerdict, and the script's exit comes back as it was: 0 ADVANCE, 1 BLOCK, 2 no verdict, 3 a short
// run. -json prints the script's VERDICT_JSON on one line and moves the text to stderr. The JSON stays beside the
// run's, or at -out, and never over a file.
func TestVerdict(t *testing.T) {
	const vj = "VERDICT_JSON=load/results/r1.verdict.json"
	runMatrix(t, []envCase{
		{name: "bare", args: []string{"verdict"}, code: 2,
			errOut: "laug · verdict: names no run: give it a run id, the name of load/results/<run-id>.json"},
		{name: "no lease", args: []string{"verdict", "r1"}, how: script.Run, script: "load/verdict.sh",
			argv: []string{"r1"}, knobs: []string{"PROM_URL=", "FALLBACK_PROM_URL=http://127.0.0.1:9090", vj},
			strays: verdictStrays},
		{name: "the lease's Prometheus", args: []string{"verdict", "r1"}, lease: true, how: script.Run,
			script: "load/verdict.sh", argv: []string{"r1"},
			knobs: []string{"PROM_URL=", "FALLBACK_PROM_URL=http://127.0.0.1:9100", vj}, strays: verdictStrays},
		{name: "-prom", args: []string{"verdict", "-prom", "http://127.0.0.1:19090", "r1"}, lease: true,
			how: script.Run, script: "load/verdict.sh", argv: []string{"r1"},
			knobs:  []string{"PROM_URL=http://127.0.0.1:19090", "FALLBACK_PROM_URL=http://127.0.0.1:19090", vj},
			strays: verdictStrays},
		{name: "-thresholds, -sweep-margin", args: []string{"verdict", "r1", "-thresholds", "load/steady-state.yaml",
			"-sweep-margin", "1m"}, how: script.Run, script: "load/verdict.sh", argv: []string{"r1"},
			knobs: []string{"PROM_URL=", "FALLBACK_PROM_URL=http://127.0.0.1:9090",
				"THRESHOLDS=load/steady-state.yaml", "SWEEP_MARGIN_S=60", vj},
			strays: verdictStrays},
		{name: "-sweep-margin not whole", args: []string{"verdict", "r1", "-sweep-margin", "1.5s"}, code: 2,
			errOut: "laug · verdict: -sweep-margin 1.5s: whole seconds, 0 or more"},
		{name: "a run id of two words", args: []string{"verdict", "a b"}, code: 2,
			errOut: "laug · verdict: run id 'a b': not one word of letters, digits, ., _ and -, from a letter or digit"},
		{name: "two run ids", args: []string{"verdict", "r1", "r2"}, code: 2,
			errOut: `laug · verdict: takes one run id, got ["r1" "r2"]`},
	})

	// stand stands in for load/verdict.sh: its text, then its exit.
	stand := func(text string, code int) func(script.Command, []string, io.Writer, io.Writer) int {
		return func(_ script.Command, _ []string, stdout, _ io.Writer) int {
			io.WriteString(stdout, text)
			return code
		}
	}
	const text = "laug · verdict for run r1 (600 s window)\nworkflows_not_completed    0    0  ok\n\n"
	saved := isTerminal
	t.Cleanup(func() { isTerminal = saved })
	for _, c := range []struct {
		name, out string
		code      int
		terminal  bool
		want      string
	}{
		{"ADVANCE", text + "VERDICT: ADVANCE\n", 0, false, text + "VERDICT: ADVANCE\n"},
		{"BLOCK", text + "VERDICT: BLOCK\n", 1, false, text + "VERDICT: BLOCK\n"},
		{"BLOCK on a terminal", text + "VERDICT: BLOCK\n", 1, true, text + "VERDICT: " + sgrBlock + "BLOCK" + sgrReset +
			"\n"},
		{"ADVANCE on a terminal", text + "VERDICT: ADVANCE\n", 0, true, text + "VERDICT: " + sgrAdvance + "ADVANCE" +
			sgrReset + "\n"},
		{"a short run", text, 3, true, text},
		{"no verdict, no newline at the end", "no run metadata", 2, false, "no run metadata"},
		{"a line that only holds the word", "VERDICT: ADVANCE (GATE_FAKE=1)\n", 0, true,
			"VERDICT: ADVANCE (GATE_FAKE=1)\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			isTerminal = func(io.Writer, string) bool { return c.terminal }
			a, rec := matrixApp(t, envCase{})
			a.Environ = append(a.Environ, "TERM=xterm-256color")
			rec.run = stand(c.out, c.code)
			code, out, errOut := runApp(t, a, "verdict", "r1")
			if code != c.code || out != c.want {
				t.Errorf("exit %d, want %d\nstdout %q\nwant   %q\nstderr:\n%s", code, c.code, out, c.want, errOut)
			}
		})
	}

	// The verdict JSON: VERDICT_JSON is <run-id>.verdict.json beside the run's, or -out, and the last line says where it
	// went. A file there already stays as it was, and -json reads the script's document from a temp dir of laug's own,
	// which goes once read; -out must be new, in a directory that is there. A file the script left on an exit but 0,
	// 1 and 3 goes. With no verdict there is no file and nothing on stdout.
	const doc = "{\n  \"run_id\": \"r1\",\n  \"verdict\": \"BLOCK\",\n  \"exit\": 1\n}\n"
	for _, c := range []struct {
		name      string
		args      []string
		before    bool   // load/results/r1.verdict.json is there before
		code      int    // the script's
		exit      int    // verdict's
		write     bool   // the script writes VERDICT_JSON
		file      string // VERDICT_JSON as the script gets it; "tmp" a temp dir's
		last, out string // a line stderr or stdout ends with; stdout under -json
		stays     string // the file at -out or beside the run after, "" none
	}{
		{"BLOCK", nil, false, 1, 1, true, "load/results/r1.verdict.json",
			"laug · verdict: kept in load/results/r1.verdict.json\n", "", doc},
		{"-json, BLOCK", []string{"-json"}, false, 1, 1, true, "load/results/r1.verdict.json",
			"laug · verdict: kept in load/results/r1.verdict.json\n", `{"run_id":"r1","verdict":"BLOCK","exit":1}` + "\n",
			doc},
		{"-json, no verdict", []string{"-json"}, false, 2, 2, false, "load/results/r1.verdict.json", "VERDICT: BLOCK\n",
			"", ""},
		{"a file cut short", nil, false, 7, 2, true, "load/results/r1.verdict.json",
			"which verdict does not name; exit 2\n", "", ""},
		{"a file there already", nil, true, 1, 1, false, "",
			"laug · verdict: load/results/r1.verdict.json was there already and stays as it was; -out <file> keeps " +
				"this verdict\n", "", "{}\n"},
		{"-json, a file there already", []string{"-json"}, true, 1, 1, true, "tmp",
			"laug · verdict: load/results/r1.verdict.json was there already and stays as it was; -out <file> keeps " +
				"this verdict\n", `{"run_id":"r1","verdict":"BLOCK","exit":1}` + "\n", "{}\n"},
		{"-out", []string{"-out", "mine.json"}, true, 0, 0, true, "mine.json",
			"laug · verdict: kept in mine.json\n", "", doc},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := matrixApp(t, envCase{})
			tmp := t.TempDir()
			a.Environ = append(a.Environ, "TMPDIR="+tmp)
			beside := filepath.Join(a.Root, "load", "results", "r1.verdict.json")
			if c.before {
				writeFile(t, beside, "{}\n")
			}
			var file string
			rec.run = func(_ script.Command, env []string, stdout, _ io.Writer) int {
				for _, kv := range env {
					if v, ok := strings.CutPrefix(kv, "VERDICT_JSON="); ok {
						file = v
					}
				}
				fmt.Fprint(stdout, text+"VERDICT: BLOCK\n")
				if c.write {
					path := file
					if !filepath.IsAbs(path) {
						path = filepath.Join(a.Root, path)
					}
					writeFile(t, path, doc)
				}
				return c.code
			}
			code, out, errOut := runApp(t, a, append([]string{"verdict", "r1"}, c.args...)...)
			ends := errOut
			if !slices.Contains(c.args, "-json") && c.code < 2 {
				ends = out
			}
			if code != c.exit || !strings.HasSuffix(ends, c.last) || c.out != "" && out != c.out {
				t.Errorf("exit %d, want %d\nstdout %q, want %q\nstderr:\n%s\nwant it to end %q", code, c.exit, out, c.out,
					errOut, c.last)
			}
			switch {
			case c.file == "tmp" && filepath.Dir(filepath.Dir(file)) != tmp:
				t.Errorf("VERDICT_JSON %s is not in a dir of TMPDIR's", file)
			case c.file == "tmp":
				if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
					t.Errorf("the temp dir stays: %v", err)
				}
			case file != c.file:
				t.Errorf("VERDICT_JSON=%s, want %s", file, c.file)
			}
			at := beside
			if slices.Contains(c.args, "-out") {
				at = filepath.Join(a.Root, "mine.json")
			}
			if b, _ := os.ReadFile(at); string(b) != c.stays {
				t.Errorf("%s holds %q after, want %q", at, b, c.stays)
			}
		})
	}
	// In a git checkout the last line says when git does not track the verdict JSON and does not ignore it, as for a
	// run that is not committed; a runbook walk's run, rb<N>-<UTC time>, is ignored, so its line says nothing more.
	for id, want := range map[string]string{
		"r1":                  "laug · verdict: kept in load/results/r1.verdict.json, untracked by git\n",
		"rb2-20261027-120000": "laug · verdict: kept in load/results/rb2-20261027-120000.verdict.json\n",
	} {
		a, rec := matrixApp(t, envCase{files: map[string]string{".gitignore": "load/results/rb[0-9]-*\n"}})
		git := exec.Command("git", "init", "-q", a.Root)
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		rec.run = func(_ script.Command, env []string, stdout, _ io.Writer) int {
			for _, kv := range env {
				if v, ok := strings.CutPrefix(kv, "VERDICT_JSON="); ok {
					writeFile(t, filepath.Join(a.Root, v), doc)
				}
			}
			fmt.Fprint(stdout, text+"VERDICT: BLOCK\n")
			return 1
		}
		if code, out, errOut := runApp(t, a, "verdict", id); code != 1 || !strings.HasSuffix(out, want) {
			t.Errorf("verdict %s in a git checkout: exit %d\nstdout:\n%s\nstderr:\n%s\nwant it to end %q", id, code, out,
				errOut, want)
		}
	}

	// -out refuses a file that is there and a directory that is not, exit 2 with nothing run.
	for _, c := range []struct{ out, line string }{
		{"taken.json", "laug · verdict: -out taken.json: the file is there already, and verdict writes only a new one"},
		{"no/such/v.json", "laug · verdict: -out no/such/v.json: no directory no/such"},
	} {
		a, rec := matrixApp(t, envCase{files: map[string]string{"taken.json": "{}"}})
		code, out, errOut := runApp(t, a, "verdict", "r1", "-out", c.out)
		if code != 2 || out != "" || !strings.Contains(errOut, c.line+"\n") || len(rec.calls) != 0 {
			t.Errorf("-out %s: exit %d, %d scripts\nstdout:\n%s\nstderr:\n%s", c.out, code, len(rec.calls), out, errOut)
		}
	}

	// A code load/verdict.sh's header does not name is a tool's own, passed up by set -e: curl's 7 when no Prometheus
	// answered (seen: load/read-thresholds.sh, then load/verdict.sh, which exits with that code). laug names it on one
	// line and exits 2, no verdict, text or -json. A signal's 128+n passes as it came.
	for _, c := range []struct {
		args       []string
		script     int
		code       int
		line       bool
		stdoutWant string
	}{
		{[]string{"verdict", "r1"}, 7, 2, true, "reading Prometheus at http://127.0.0.1:9090\n"},
		{[]string{"verdict", "r1", "-json"}, 7, 2, true, ""},
		{[]string{"verdict", "r1"}, 126, 2, true, "reading Prometheus at http://127.0.0.1:9090\n"},
		{[]string{"verdict", "r1"}, 130, 130, false, "reading Prometheus at http://127.0.0.1:9090\n"},
	} {
		t.Run(fmt.Sprintf("%s, the script's %d", strings.Join(c.args[2:], " "), c.script), func(t *testing.T) {
			a, rec := matrixApp(t, envCase{})
			a.Environ = append(a.Environ, "TMPDIR="+t.TempDir())
			rec.run = stand("reading Prometheus at http://127.0.0.1:9090\n", c.script)
			code, out, errOut := runApp(t, a, c.args...)
			line := fmt.Sprintf("laug · verdict: no verdict: load/verdict.sh exited %d, the code of a command it ran, "+
				"which verdict does not name; exit 2\n", c.script)
			if code != c.code || out != c.stdoutWant || strings.Contains(errOut, line) != c.line {
				t.Errorf("exit %d, want %d\nstdout %q\nstderr:\n%s", code, c.code, out, errOut)
			}
		})
	}
}
