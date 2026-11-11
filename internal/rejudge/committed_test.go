package rejudge

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
)

// results is gate/results, from this package's directory; gateDir is gate/; devRuns is the copy of the 7 dev host
// runs the tests name (testdata/runs/README.md), labRuns the copy of the lab host runs they name that are not kept in
// gate/results (testdata/lab/README.md).
var (
	gateDir = filepath.Join("..", "..", "gate")
	results = filepath.Join(gateDir, "results")
	devRuns = filepath.Join("testdata", "runs")
	labRuns = filepath.Join("testdata", "lab")
)

// runDir is the directory of the run gid a test names: its copy under devRuns for a dev host run or under labRuns
// for a lab host run kept as a fixture, else gate/results'.
func runDir(gid string) string {
	for _, d := range []string{devRuns, labRuns} {
		if fi, err := os.Stat(filepath.Join(d, gid)); err == nil && fi.IsDir() {
			return filepath.Join(d, gid)
		}
	}
	return filepath.Join(results, gid)
}

// committed is every committed run here, which TestRuns counts: the runs of the gates table whose directories are
// in gate/results (census).
func committed(t *testing.T) []Run {
	t.Helper()
	runs, err := Runs(os.DirFS(results))
	if err != nil {
		t.Fatal(err)
	}
	c := census(t)
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.Gid)
	}
	if fmt.Sprint(ids) != fmt.Sprint(c.Here) || len(runs) == 0 {
		t.Fatalf("gate/results holds the runs %v; the gates table's rows here are %v", ids, c.Here)
	}
	return runs
}

// TestEveryCommittedRun is the Go judge's proof: each committed run in gate/results,
// judged under the version its gate.json records, gives its own verdict.json and verdict.md byte for byte, unless
// testdata/expected.txt names it. A line there names a kind, and the kind's proof must hold:
//
//   - empty-dirs <keys>: judged as a clone holds it, the run differs, and by the GONE rule (Gone)
//     alone: the committed R2 says a file of <key>/ is missing and Go's that there is no <key>/ directory, for these
//     keys and no other. Judged with those directories made again in memory, it gives both files. Counted known.
//   - trap <form>: judged under its version, the run differs, since its verdict.json is the gate's trap verdict.
//     Re-rendered by its form from gate.json, clock.json, plan.json and the step and reason its verdict.json records
//     (judge.TrapArgsOf), it gives both files, and no other form does. Counted re-rendered, never rejudged.
//
// A line whose run judges identical as it stands is stale and fails, as does a line that names no committed run.
// The shell judges every committed run with exit 0 or 1, none "not judged" (cells/results/2026-10-18-lab-obs.txt:
// 49-50), so Judge must not fail on one. For each run whose files a judge wrote, it also checks:
//
//   - Render over the committed verdict.json gives the committed verdict.md, which holds RENDER apart from JUDGE.
//   - Stdout, with "gate/results/<gid>" as the directory, ends in the files' line and the verdict, and Exit is 0 for
//     ADVANCE and 1 for BLOCK.
//
// The counts are pinned in the rows: the rejudge rows of this judge give 158 runs, 156 identical, 0 known, 2
// re-rendered, and the runs here count as their rows do. A line of expected.txt whose run is archived must name a
// gates row whose rejudge row is the line's outcome, re-rendered for a trap, known for empty-dirs, and where its copy
// is in testdata/lab, the line's proof holds there.
func TestEveryCommittedRun(t *testing.T) {
	expected, err := ReadExpected(os.DirFS("testdata"), "expected.txt")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	byVersion := map[string]int{}
	seen := map[string]bool{}
	for _, r := range committed(t) {
		t.Run(r.Gid, func(t *testing.T) {
			x, listed := expected[r.Gid]
			seen[r.Gid] = true
			got := judgeCommitted(t, r, x, listed)
			counts[got]++
			if got == "identical" {
				byVersion[r.Version.Name]++
			}
		})
	}
	c := census(t)
	by := outcomes(t, c)
	lab, err := Runs(os.DirFS(labRuns))
	if err != nil {
		t.Fatal(err)
	}
	for gid, x := range expected {
		if seen[gid] {
			continue
		}
		want, ok := map[Kind]Outcome{Trap: Rerendered, EmptyDirs: Known}[x.Kind]
		if !ok {
			t.Errorf("expected.txt: %s %s %s names no run in gate/results, and kind %s gives no rejudge outcome",
				gid, x.Kind, x.Arg, x.Kind)
			continue
		}
		if _, ok := c.Rows[gid]; !ok || by[gid] != want {
			t.Errorf("expected.txt: %s %s %s names no run in gate/results, and no gates row whose rejudge row is %s",
				gid, x.Kind, x.Arg, want)
		}
		for _, r := range lab {
			if r.Gid == gid {
				t.Run(gid+" in testdata/lab", func(t *testing.T) {
					if got := judgeCommitted(t, r, x, true); got != string(want) {
						t.Errorf("%s, want %s", got, want)
					}
				})
			}
		}
	}
	if got := countRows(c.IDs, by).String(); got != "158 runs: 156 identical, 0 known, 2 re-rendered, 0 unexplained, "+
		"0 not judged" {
		t.Errorf("the rejudge rows: %s", got)
	}
	identical := func(id string) bool { return by[id] == Identical }
	if got := versions(c, c.IDs, identical); got != "map[v1:50 v2:84 v2-r7off:1 v3:21]" {
		t.Errorf("the rejudge rows, identical by version: %s", got)
	}
	want := map[string]int{}
	for o, n := range countRows(c.Here, by) {
		if n > 0 {
			want[string(o)] = n
		}
	}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Errorf("counts %v, want %v", counts, want)
	}
	if got, want := fmt.Sprint(byVersion), versions(c, c.Here, identical); got != want {
		t.Errorf("identical by version: %s, want %s", got, want)
	}
	t.Logf("%s/%s: %v; identical by version %v", runtime.GOOS, runtime.GOARCH, counts, byVersion)
}

// TestDevRuns holds TestEveryCommittedRun's proofs on the copy of the 7 dev host runs (testdata/runs/README.md),
// since no committed run in gate/results is known or a T1 trap: each run testdata/expected-dev.txt does not name is
// identical, its 2 empty-dirs runs are known and its 2 T1 traps re-rendered, and laug gate rejudge's lines and counts
// on the copy say the same: 7 runs: 3 identical, 2 known, 2 re-rendered, 0 unexplained, 0 not judged.
func TestDevRuns(t *testing.T) {
	expected, err := ReadExpected(os.DirFS("testdata"), "expected-dev.txt")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := Runs(os.DirFS(devRuns))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, r := range runs {
		t.Run(r.Gid, func(t *testing.T) {
			x, listed := expected[r.Gid]
			seen[r.Gid] = true
			counts[judgeCommitted(t, r, x, listed)]++
		})
	}
	for gid, x := range expected {
		if !seen[gid] {
			t.Errorf("expected-dev.txt: %s %s %s names no run in testdata/runs", gid, x.Kind, x.Arg)
		}
	}
	want := map[string]int{"identical": 3, "known": 2, "re-rendered": 2}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Errorf("counts %v, want %v", counts, want)
	}
	answers, err := RejudgeAll(devRuns, Checkout{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range answers {
		want := a.ID + "  identical"
		if x, ok := expected[a.ID]; ok {
			switch x.Kind {
			case EmptyDirs:
				want = a.ID + "  differs: an empty item directory (" + strings.Join(x.Dirs(), "/, ") + "/), which " +
					"git does not carry: a fresh clone judges it as no directory"
			case Trap:
				want = a.ID + "  re-rendered: trap " + x.Arg
			}
		}
		if got := a.Line(); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	}
	if got := Count(answers).String(); got != "7 runs: 3 identical, 2 known, 2 re-rendered, 0 unexplained, 0 not judged" {
		t.Errorf("counts: %s", got)
	}
}

// The two files a run holds, or a judge or a trap makes.
type files struct{ json, md []byte }

// diff names the first line of each file where want and got differ, or "" when both are equal.
func (want files) diff(got files) string {
	var out []string
	if !bytes.Equal(want.json, got.json) {
		out = append(out, "verdict.json differs: "+firstLineDiff(want.json, got.json))
	}
	if !bytes.Equal(want.md, got.md) {
		out = append(out, "verdict.md differs: "+firstLineDiff(want.md, got.md))
	}
	return strings.Join(out, "\n")
}

// judgeCommitted judges the run r as TestEveryCommittedRun says, with x its expected line when listed, and gives the
// count it falls in: identical, known, re-rendered, differing or not judged.
func judgeCommitted(t *testing.T, r Run, x Expectation, listed bool) string {
	t.Helper()
	if !r.Registered() {
		t.Errorf("records gate/combine.sh %q, which the registry does not name", r.SHA256)
		return "not judged"
	}
	dir := runDir(r.Gid)
	fsys := os.DirFS(dir)
	want := files{readFile(t, filepath.Join(dir, "verdict.json")), readFile(t, filepath.Join(dir, "verdict.md"))}
	res, err := judge.Judge(fsys, r.Version, judge.Options{})
	if err != nil {
		t.Errorf("Judge under %s: %v", r.Version.Name, err)
		return "differing"
	}
	d := want.diff(files{res.JSON, res.MD})
	switch {
	case !listed && d != "":
		t.Errorf("under %s: %s", r.Version.Name, d)
		return "differing"
	case !listed:
		checkJudged(t, r, res, want)
		return "identical"
	case d == "":
		t.Errorf("expected.txt: %s %s %s is stale: the run judges identical under %s as it stands", r.Gid, x.Kind,
			x.Arg, r.Version.Name)
		return "differing"
	}
	switch x.Kind {
	case EmptyDirs:
		if gone := goneKeys(t, want.json, res.JSON); !slices.Equal(gone, x.Dirs()) {
			t.Errorf("the GONE rule names %q, and expected.txt %q:\n%s", gone, x.Dirs(), d)
			return "differing"
		}
		again, err := judge.Judge(fsys, r.Version, judge.Options{EmptyDirs: x.Dirs()})
		if err != nil {
			t.Errorf("Judge with %v made again: %v", x.Dirs(), err)
			return "differing"
		}
		if d := want.diff(files{again.JSON, again.MD}); d != "" {
			t.Errorf("Judge with %v made again: %s", x.Dirs(), d)
			return "differing"
		}
		checkJudged(t, r, again, want)
		return "known"
	case Trap:
		args, err := judge.TrapArgsOf(want.json)
		if err != nil {
			t.Error(err)
			return "differing"
		}
		proven, others := false, false
		for _, f := range judge.Forms {
			js, md, err := judge.RerenderTrap(fsys, f, args)
			d := "an error: " + fmt.Sprint(err)
			if err == nil {
				d = want.diff(files{js, md})
			}
			switch {
			case f == judge.Form(x.Arg) && d != "":
				t.Errorf("re-rendered as %s: %s", f, d)
			case f == judge.Form(x.Arg):
				proven = true
			case d == "":
				t.Errorf("form %s gives both files too, so expected.txt's %s is not the one form that does", f, x.Arg)
				others = true
			}
		}
		if !proven || others {
			return "differing"
		}
		return "re-rendered"
	}
	t.Errorf("expected.txt: kind %q has no proof here", x.Kind)
	return "differing"
}

// checkJudged checks what a judge's run prints and renders: Render over the committed verdict.json, the stdout and
// the exit.
func checkJudged(t *testing.T, r Run, res *judge.Result, want files) {
	t.Helper()
	back, err := jsonx.Parse(want.json)
	if err != nil {
		t.Fatalf("verdict.json does not parse: %v", err)
	}
	if md, err := judge.Render(r.Version, back); err != nil || !bytes.Equal(md, want.md) {
		t.Errorf("Render over the committed verdict.json: %v %s", err, firstLineDiff(want.md, md))
	}
	name := "gate/results/" + r.Gid
	out := res.Stdout(name)
	tail := "written          " + name + "/verdict.json, " + name + "/verdict.md\n" + res.Verdict + "\n"
	if !bytes.HasSuffix(out, []byte(tail)) || !bytes.HasPrefix(out, []byte("laug · gate "+r.Gid+", server ")) {
		t.Errorf("stdout:\n%s", out)
	}
	switch {
	case res.Verdict == "ADVANCE" && res.Exit() == 0, res.Verdict == "BLOCK" && res.Exit() == 1:
	default:
		t.Errorf("verdict %q, exit %d", res.Verdict, res.Exit())
	}
}

// goneKeys is the GONE rule over a run's committed verdict.json a and a rejudge's b (Gone): each
// item key of a, in order, for which a's R2 says only that a file of <key>/ is missing, and b's only that there is
// no <key>/ directory.
func goneKeys(t *testing.T, a, b []byte) []string {
	t.Helper()
	keys, ok := Gone(a, b)
	if !ok {
		t.Fatal("GONE stops on an error")
	}
	return keys
}

// TestDeterminism judges each committed run 3 times: the files, the stdout and the exit are the same bytes each
// time, and so are the expected file's proofs, a judging with empty directories made again and each trap form's
// re-render. Nothing in the judge reads a clock, a map's order or the environment.
func TestDeterminism(t *testing.T) {
	expected, err := ReadExpected(os.DirFS("testdata"), "expected.txt")
	if err != nil {
		t.Fatal(err)
	}
	judged := 0
	for _, r := range committed(t) {
		dir := os.DirFS(filepath.Join(results, r.Gid))
		x := expected[r.Gid]
		var runs []func() (files, string)
		for _, opts := range []judge.Options{{}, {EmptyDirs: x.Dirs()}} {
			runs = append(runs, func() (files, string) {
				res, err := judge.Judge(dir, r.Version, opts)
				if err != nil {
					return files{}, err.Error()
				}
				return files{res.JSON, res.MD}, string(res.Stdout("d")) + fmt.Sprint(res.Exit())
			})
		}
		if x.Kind == Trap {
			for _, f := range judge.Forms {
				runs = append(runs, func() (files, string) {
					args, err := judge.TrapArgsOf(readFile(t, filepath.Join(results, r.Gid, "verdict.json")))
					if err != nil {
						return files{}, err.Error()
					}
					js, md, err := judge.RerenderTrap(dir, f, args)
					return files{js, md}, fmt.Sprint(err)
				})
			}
		}
		judged++
		for i, run := range runs {
			first, firstOut := run()
			for range 2 {
				again, out := run()
				if first.diff(again) != "" || out != firstOut {
					t.Errorf("%s: judging %d differs the second time", r.Gid, i)
				}
			}
		}
	}
	if want := len(census(t).Here); judged != want {
		t.Errorf("judged %d runs, want %d", judged, want)
	}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// firstLineDiff names the first line where want and got differ.
func firstLineDiff(want, got []byte) string {
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := range min(len(w), len(g)) {
		if w[i] != g[i] {
			return "line " + strconv.Itoa(i+1) + ":\n  want: " + w[i] + "\n  got:  " + g[i]
		}
	}
	return "want " + strconv.Itoa(len(w)) + " lines, got " + strconv.Itoa(len(g))
}
