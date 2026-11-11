package main

// The env matrix the daily path's verbs are held to, and tests of what they share: the choices a fault or a candidate
// takes, the flags' defaults against the scripts', and the small checks.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// strayEnviron is the caller's environment in every row of the env matrix: a stray copy of each knob the daily path
// maps, the login names, a name of Temporal's and a seam. No knob may reach a script from it, a verb names on stderr
// each stray it maps, and the seam passes through.
var strayEnviron = []string{
	"SERVER_TAG=9.9.9", "RUN_ID=stray", "RATE=99", "MAX_CONCURRENT=99", "THRESHOLDS=stray.yaml",
	"PROM_URL=http://127.0.0.1:1", "CELL_ADDRESS=127.0.0.1:1", "DURATION=1h", "ITERATIONS=9",
	"POOL_ADDR=http://127.0.0.1:1", "FORCE=1", "VERDICT_JSON=stray.json", "SWEEP_MARGIN_S=1", "POOL_QUIET_WAIT_S=1",
	"NAMESPACE=stray", "PROJECT=stray", "OMES_DIR=stray", "SCENARIO=stray", "CANDIDATE_RELEASE=0",
	"CUTOFF=2026-01-01T00:00:00Z", "FAULT=stray",
	"USER=someone", "LOGNAME=someone", "TEMPORAL_ADDRESS=127.0.0.1:1", "FAKE_SEAM=1",
}

// envCase is one row of a verb's env matrix.
type envCase struct {
	name   string
	args   []string // after laug
	lease  bool     // .last-lease.json holds testdata/dry-run/lease.json: c0ffee01 on block 1, server 1.32.1
	docker string   // "<project> <image>": the stub docker's temporal-history container
	files  map[string]string
	more   []string // the caller's environment beyond strayEnviron
	code   int
	// What the runner was handed first: how, the script and its arguments, and every knob in its environment,
	// NAME=value, in any order, $ROOT standing for the checkout in both. how 0 wants nothing handed over. tool and dir
	// are a program's: found on PATH, and run in that directory of the checkout.
	how    script.How
	script string
	tool   bool
	dir    string
	argv   []string
	knobs  []string
	strays []string // the knobs stderr names as strays
	out    string   // a line stdout holds, when not empty
	errOut string   // a line stderr holds, when not empty
}

// matrixApp is a row's App: a checkout holding the catalog, the candidates, the row's files and the last lease when
// it has one; PATH with the stub docker first; strayEnviron; a recorder, which runs the roles too; the goldens' clock.
func matrixApp(t *testing.T, c envCase) (*App, *recorder) {
	t.Helper()
	root := checkout(t)
	for _, p := range goldenInputs {
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, p), string(b))
	}
	if c.lease {
		b, err := os.ReadFile(filepath.Join("testdata", "dry-run", "lease.json"))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, ".last-lease.json"), string(b))
	}
	for p, content := range c.files {
		writeFile(t, filepath.Join(root, p), content)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	writeFile(t, filepath.Join(bin, "docker"), dockerStub)
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	environ := append([]string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + t.TempDir()}, strayEnviron...)
	if project, image, ok := strings.Cut(c.docker, " "); ok {
		environ = append(environ, "STUB_PROJECT="+project, "STUB_IMAGE="+image)
	}
	environ = append(environ, c.more...)
	rec := &recorder{root: root}
	return &App{Root: root, Environ: environ, Runner: rec, Roles: rec, Now: func() time.Time { return dryRunClock }},
		rec
}

// strayLine is the line a stray gets, from a verb of one word or two ("gate run", "cell dynconfig-check").
var strayLine = regexp.MustCompile(`(?m)^laug · [a-z]+(?: [a-z-]+)?: ([A-Z][A-Z0-9_]*)=.* in the environment is ` +
	`not passed on; `)

// runMatrix runs each row through run and holds it to what it says.
func runMatrix(t *testing.T, cases []envCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, rec := matrixApp(t, c)
			code, out, errOut := runApp(t, a, c.args...)
			checkRow(t, c, rec, code, out, errOut)
		})
	}
}

// checkRow holds one row's run to the row.
func checkRow(t *testing.T, c envCase, rec *recorder, code int, out, errOut string) {
	t.Helper()
	if code != c.code {
		t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
	}
	if c.out != "" && !slices.Contains(strings.Split(out, "\n"), c.out) {
		t.Errorf("stdout lacks the line %q:\n%s", c.out, out)
	}
	if c.errOut != "" && !slices.Contains(strings.Split(errOut, "\n"), c.errOut) {
		t.Errorf("stderr lacks the line %q:\n%s", c.errOut, errOut)
	}
	var strays []string
	for _, m := range strayLine.FindAllStringSubmatch(errOut, -1) {
		strays = append(strays, m[1])
	}
	if !slices.Equal(strays, c.strays) {
		t.Errorf("strays named %q, want %q\nstderr:\n%s", strays, c.strays, errOut)
	}
	if c.how == 0 {
		if len(rec.calls) != 0 {
			t.Errorf("handed over %s, want nothing", rec.calls[0].Script)
		}
		return
	}
	if len(rec.calls) == 0 {
		t.Fatalf("nothing handed over, want %s\nstderr:\n%s", c.script, errOut)
	}
	got := rec.calls[0]
	var argv []string
	for _, v := range got.Args {
		argv = append(argv, v.Text)
	}
	root := strings.NewReplacer("$ROOT", got.Root)
	wantArgv := slices.Clone(c.argv)
	for i, w := range wantArgv {
		wantArgv[i] = root.Replace(w)
	}
	if rec.hows[0] != c.how || got.Script != c.script || !slices.Equal(argv, wantArgv) {
		t.Errorf("handed over %v %s %q, want %v %s %q", rec.hows[0], got.Script, argv, c.how, c.script, wantArgv)
	}
	if got.Tool != c.tool || got.Dir != c.dir {
		t.Errorf("handed over tool %t in %q, want tool %t in %q", got.Tool, got.Dir, c.tool, c.dir)
	}
	var knobs []string
	for _, kv := range rec.envs[0] {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := script.Lookup(name); ok {
			knobs = append(knobs, kv)
		}
		// The stray TEMPORAL_ADDRESS never reaches a script; a verb for the orchestrator's dev server sets its own.
		if slices.Contains([]string{"USER", "LOGNAME"}, name) || kv == "TEMPORAL_ADDRESS=127.0.0.1:1" {
			t.Errorf("the script's environment holds %s", kv)
		}
	}
	want := slices.Clone(c.knobs)
	for i, k := range want {
		want[i] = root.Replace(k)
	}
	slices.Sort(knobs)
	slices.Sort(want)
	if !slices.Equal(knobs, want) {
		t.Errorf("the script's knobs:\n got %q\nwant %q", knobs, want)
	}
	if !slices.Contains(rec.envs[0], "FAKE_SEAM=1") {
		t.Errorf("the seam FAKE_SEAM did not pass through: %q", rec.envs[0])
	}
}

// TestChoices: the line scan of the catalog and the candidates reads the names yq reads, in the same order.
func TestChoices(t *testing.T) {
	needTools(t, "the test reads the names with it", "yq")
	a := &App{Root: filepath.Join("..", "..")}
	for _, c := range []struct {
		ch    choice
		query string
	}{{faultChoice, ".faults | keys | .[]"}, {candidateChoice, ".candidates[].name"}} {
		_, names, err := c.ch.names(a)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("yq", "-r", c.query, filepath.Join("..", "..", c.ch.file)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.Fields(string(out)); !slices.Equal(names, want) {
			t.Errorf("%s: the scan reads %q, yq %q", c.ch.file, names, want)
		}
	}
	// The seam: CATALOG names another file, relative to the checkout, and the scan reads that one.
	root := checkout(t)
	writeFile(t, filepath.Join(root, "fixture.yaml"), "defaults:\n  steady_s: 60\nfaults:\n  test-act: # a test\n"+
		"    signal: x\n  test-two:\n    signal: y\nother:\n  not-a-fault:\n")
	a = &App{Root: root, Environ: []string{"CATALOG=fixture.yaml"}}
	if file, names, err := faultChoice.names(a); err != nil || file != "fixture.yaml" ||
		!slices.Equal(names, []string{"test-act", "test-two"}) {
		t.Errorf("CATALOG=fixture.yaml: %s %q %v", file, names, err)
	}
	if err := faultChoice.check(a, "persistence-latency"); err == nil ||
		err.Error() != "no fault persistence-latency in fixture.yaml, which holds 2:\n  test-act\n  test-two" {
		t.Errorf("a fault the fixture lacks: %v", err)
	}
	if err := faultChoice.check(a, "test-atc"); err == nil ||
		err.Error() != "no fault test-atc in fixture.yaml; did you mean test-act?" {
		t.Errorf("a fault 1 edit from one: %v", err)
	}
	// The names a line each when they fit in the rows given, else down and across in as few columns as fit, each as
	// wide as its longest name, two spaces apart, no blank at a line's end; never past 79 columns, a line each when
	// two would not fit.
	long := strings.Repeat("x", 37)
	for _, c := range []struct {
		names []string
		rows  int
		want  string
	}{
		{[]string{"a", "bb", "c"}, 3, "  a\n  bb\n  c"},
		{[]string{"a", "bb", "c"}, 0, "  a\n  bb\n  c"},
		{[]string{"a", "bb", "c"}, 2, "  a   c\n  bb"},
		{[]string{"a", "bb", "c", "dddd", "e"}, 2, "  a   c     e\n  bb  dddd"},
		{[]string{"a", "bb", "c", "dddd", "e"}, 4, "  a   dddd\n  bb  e\n  c"},
		{[]string{long, long, long}, 2, "  " + long + "  " + long + "\n  " + long},
		{[]string{long + "xx", long, long}, 2, "  " + long + "xx\n  " + long + "\n  " + long},
	} {
		if got := inRows(c.names, c.rows); got != c.want {
			t.Errorf("inRows(%q, %d):\n%s\nwant:\n%s", c.names, c.rows, got, c.want)
		}
	}
}

// TestRaunLength: the line scan of the catalog sums a fault's windows as faults/run.sh reads them, the row's own field
// else the defaults', settle_s 30 when neither has one, with budget_s beside: on the checkout's catalog, for every
// fault, as yq reads it, and on a fixture that CATALOG names. A field set nowhere, or a catalog that cannot be read,
// gives no length.
func TestRaunLength(t *testing.T) {
	root := checkout(t)
	writeFile(t, filepath.Join(root, "fixture.yaml"), "defaults:\n  steady_s: 90   # a comment\n  hold_s: 120\n"+
		"  after_s: 90\n  budget_s: 60\n  readings:\n    q: up\nfaults:\n  short:\n    hypothesis: long text, 5\n"+
		"    hold_s: 30\n    settle_s: 10\n    readings:\n      hold_s: 999\n  plain: # a row\n    budget_s: 120\n")
	writeFile(t, filepath.Join(root, "no-after.yaml"), "defaults:\n  steady_s: 90\n  hold_s: 120\nfaults:\n  x:\n"+
		"    budget_s: 60\n")
	for _, c := range []struct {
		catalog, fault  string
		windows, budget int
		ok              bool
	}{
		{"fixture.yaml", "short", 90 + 30 + 10 + 90, 60, true},
		{"fixture.yaml", "plain", 90 + 120 + 30 + 90, 120, true},
		{"no-after.yaml", "x", 0, 0, false},
		{"missing.yaml", "x", 0, 0, false},
	} {
		a := &App{Root: root, Environ: []string{"CATALOG=" + c.catalog}}
		if w, b, ok := a.raunLength(c.fault); w != c.windows || b != c.budget || ok != c.ok {
			t.Errorf("%s %s: %d s, budget %d s, %t; want %d, %d, %t", c.catalog, c.fault, w, b, ok, c.windows, c.budget,
				c.ok)
		}
	}
	needTools(t, "the test reads each fault's windows with it", "yq")
	a := &App{Root: filepath.Join("..", "..")}
	_, names, err := faultChoice.names(a)
	if err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join("..", "..", faultChoice.file)
	for _, f := range names {
		get := func(key string) string {
			out, err := exec.Command("yq", "-r", ".faults[\""+f+"\"]."+key+" // .defaults."+key+" // \"\"", catalog).Output()
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(out))
		}
		want := 0
		for _, key := range []string{"steady_s", "hold_s", "settle_s", "after_s"} {
			v := get(key)
			if v == "" && key == "settle_s" {
				v = "30"
			}
			n, _ := strconv.Atoi(v)
			want += n
		}
		budget, _ := strconv.Atoi(get("budget_s"))
		if w, b, ok := a.raunLength(f); w != want || b != budget || !ok {
			t.Errorf("%s: the scan reads %d s, budget %d s, %t; yq %d s, budget %d s", f, w, b, ok, want, budget)
		}
	}
	if w, b, _ := a.raunLength("persistence-latency"); w != 330 || b != 60 {
		t.Errorf("persistence-latency: %d s, budget %d s; want 330 and 60, faults/catalog.yaml:26-30 and :37", w, b)
	}
}

// flagDefault is the default -h prints for flag, at the end of the text wrapped beneath its name, or "".
func flagDefault(t *testing.T, cmd, flag string) string {
	t.Helper()
	_, out, _ := runArgs(t, cmd, "-h")
	m := regexp.MustCompile(`(?m)^  -` + regexp.QuoteMeta(flag) + `( \S+)?\n((?: {8}.*\n)+)`).FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	text := strings.Join(strings.Fields(m[2]), " ")
	d := regexp.MustCompile(`\(default "?([^")]*)"?\)$`).FindStringSubmatch(text)
	if d == nil {
		return ""
	}
	return d[1]
}

// TestScriptDefaults: a flag whose knob reaches the script only when given shows the script's own default, and the
// script still has it.
func TestScriptDefaults(t *testing.T) {
	for _, c := range []struct {
		cmd, flag, want, file, line string
	}{
		{"fault", "rate", "20", "faults/run.sh", `RATE="${RATE:-20}"`},
		{"fault", "max-concurrent", "50", "faults/run.sh", `MAX_CONCURRENT="${MAX_CONCURRENT:-50}"`},
		{"fault", "quiet-wait", "5m", "faults/run.sh", `"${POOL_QUIET_WAIT_S:-300}"`},
		{"fault", "sweep-margin", "30s", "faults/run.sh", `SWEEP_MARGIN_S="${SWEEP_MARGIN_S:-30}"`},
		{"raun", "rate", "20", "faults/run.sh", `RATE="${RATE:-20}"`},
		{"raun", "quiet-wait", "5m", "faults/lib.sh", `${POOL_QUIET_WAIT_S:-300}`},
		{"candidate", "duration", "5m", "faults/candidate.sh", `DURATION="${DURATION:-5m}"`},
		{"candidate", "rate", "20", "faults/candidate.sh", `RATE="${RATE:-20}"`},
		{"candidate", "max-concurrent", "50", "faults/candidate.sh", `MAX_CONCURRENT="${MAX_CONCURRENT:-50}"`},
		{"candidate", "quiet-wait", "5m", "faults/candidate.sh", `"${POOL_QUIET_WAIT_S:-300}"`},
		{"verdict", "sweep-margin", "30s", "load/verdict.sh", `SWEEP_MARGIN_S="${SWEEP_MARGIN_S:-30}"`},
		{"sweep", "sweep-margin", "30s", "load/verdict.sh", `SWEEP_MARGIN_S="${SWEEP_MARGIN_S:-30}"`},
		{"load", "scenario", scenarioDefault, "load/run-omes.sh", `SCENARIO="${1:-` + scenarioDefault + `}"`},
		{"load", "namespace", namespaceDefault, "load/run-omes.sh", `NAMESPACE="${NAMESPACE:-default}"`},
	} {
		if got := flagDefault(t, c.cmd, c.flag); got != c.want {
			t.Errorf("laug %s -%s: default %q, want %q", c.cmd, c.flag, got, c.want)
		}
		b, err := os.ReadFile(filepath.Join("..", "..", c.file))
		if err != nil || !bytes.Contains(b, []byte(c.line)) {
			t.Errorf("%s no longer holds %s: %v", c.file, c.line, err)
		}
	}
}

// TestDailyChecks holds the small checks the verbs share.
func TestDailyChecks(t *testing.T) {
	for v, ok := range map[string]bool{"5m": true, "90s": true, "1h30m": true, "1h2m3s": true, "": false, "0s": false,
		"1.5h": false, "5": false, "300ms": false, "m": false, "1m1h": false} {
		if err := checkLength("-duration", v); (err == nil) != ok {
			t.Errorf("checkLength(%q): %v, want ok %t", v, err, ok)
		}
	}
	for d, want := range map[time.Duration]string{0: "0", 300 * time.Second: "300", -time.Second: "",
		1500 * time.Millisecond: ""} {
		if got, err := seconds("-quiet-wait", d); got != want || (err == nil) != (want != "") {
			t.Errorf("seconds(%s) = %q, %v; want %q", d, got, err, want)
		}
	}
	for raw, want := range map[string]int64{`1792668600`: 1792668600, `"1792668600"`: 1792668600, `null`: -1, ``: -1,
		`"x"`: -1} {
		if got := epoch([]byte(raw)); got != want {
			t.Errorf("epoch(%s) = %d, want %d", raw, got, want)
		}
	}
	if err := checkWords("-run-id", "r1", "-scenario", "a b"); err == nil ||
		err.Error() != "-scenario 'a b': not one word of letters, digits, ., _ and -, from a letter or digit" {
		t.Errorf("checkWords: %v", err)
	}
	if _, err := poolURL("10.0.0.1:7700"); err == nil {
		t.Error("poolURL took a host off loopback")
	}
	if u, err := poolURL("127.0.0.1:17700"); err != nil || u != "http://127.0.0.1:17700" {
		t.Errorf("poolURL: %s, %v", u, err)
	}
	// lookPath reads the App's PATH alone: with none, no docker, so no lookup runs.
	if p := (&App{}).lookPath("sh"); p != "" {
		t.Errorf("lookPath with no PATH found %s", p)
	}
	if tag := (&App{Root: t.TempDir()}).localServerTag(context.Background(), "laug"); tag != "" {
		t.Errorf("localServerTag with no docker: %q", tag)
	}
}
