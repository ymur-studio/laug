package rejudge

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
)

// root is the repository root, which Checkout shows paths against.
var root = func() string {
	r, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		panic(err)
	}
	return r
}()

// TestRejudgeLines holds S1's lines on every committed run: each run testdata/expected.txt does not name is
// identical; an empty-dirs line's run is known, by the directories it names; a trap line's run is re-rendered by
// its form. The counts are the rejudge rows' for the runs here: with every run here, 158 runs: 156 identical, 0
// known, 2 re-rendered, 0 unexplained, 0 not judged, exit 0 (TestEveryCommittedRun pins those in the rows).
func TestRejudgeLines(t *testing.T) {
	expected, err := ReadExpected(os.DirFS("testdata"), "expected.txt")
	if err != nil {
		t.Fatal(err)
	}
	answers, err := RejudgeAll(results, Checkout{Root: root})
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
	counts := Count(answers)
	c := census(t)
	if want := countRows(c.Here, outcomes(t, c)).String(); counts.String() != want {
		t.Errorf("counts: %s, want %s, as the rejudge rows count the runs here", counts, want)
	}
	if counts.Exit() != 0 {
		t.Errorf("exit %d", counts.Exit())
	}
}

// TestRejudgeMade judges made runs, copies of committed ones with one change each, and holds each answer to the
// table. A trap verdict no form re-renders is unexplained, and a run that names a shell judge not ported,
// sha256 2a9ff8b3f8f0 (notPorted in internal/judge), is not judged.
func TestRejudgeMade(t *testing.T) {
	const (
		v2run  = "g20261018-092540" // v2, judging: items incumbent, steady and pl
		v1run  = "g20261014-215905" // v1, a dev host run (runDir)
		t1run  = "g20261014-213037" // a T1 trap, a dev host run (runDir)
		t2run  = "g20261017-014142" // a T2 trap
		notIDs = "(outside the repository)/"
	)
	editJSON := func(t *testing.T, path string, f func(o *jsonx.Object)) {
		t.Helper()
		v, err := jsonx.Parse(readFile(t, path))
		if err != nil {
			t.Fatal(err)
		}
		o, _ := v.Obj()
		f(o)
		writeFile(t, path, jsonx.Marshal(v))
	}
	emptyItems := func(keys ...string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			for _, k := range keys {
				if err := os.RemoveAll(filepath.Join(dir, k)); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(dir, k), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeJudged(t, dir)
			for _, k := range keys {
				if err := os.Remove(filepath.Join(dir, k)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	trapShaped := func(t *testing.T, dir string) {
		editJSON(t, filepath.Join(dir, "verdict.json"), func(o *jsonx.Object) {
			o.Set("stopped_at", jsonx.Str("wave"))
			o.Set("rules", jsonx.Null())
			o.Set("items", jsonx.Arr(nil))
		})
	}
	for _, c := range []struct {
		name, base string
		edit       func(t *testing.T, dir string)
		notHeld    string
		want       Outcome
		why        string // Go's Why: whole, or for NotJudged its start
	}{
		{name: "v2 copy", base: v2run, want: Identical},
		{name: "v1 copy", base: v1run, want: Identical},
		{name: "no verdict.json", base: v2run, want: NotJudged,
			edit: func(t *testing.T, dir string) { must(t, os.Remove(filepath.Join(dir, "verdict.json"))) },
			why:  "no verdict.json in " + notIDs + "no verdict.json to compare with"},
		{name: "verdict.md a directory", base: v2run, want: NotJudged,
			edit: func(t *testing.T, dir string) {
				must(t, os.Remove(filepath.Join(dir, "verdict.md")))
				must(t, os.Mkdir(filepath.Join(dir, "verdict.md"), 0o755))
			},
			why: "no verdict.md in " + notIDs + "verdict.md a directory to compare with"},
		{name: "t0 a string", base: v2run, want: NotJudged,
			edit: func(t *testing.T, dir string) {
				editJSON(t, filepath.Join(dir, "clock.json"), func(o *jsonx.Object) { o.Set("t0", jsonx.Str("x")) })
			},
			why: "judge v2 writes no verdict for " + notIDs + "t0 a string: the judge failed"},
		{name: "an unnamed judge", base: v2run, want: NotJudged,
			edit: recordJudge(strings.Repeat("0", 64)),
			why:  "gate.json records gate/combine.sh sha256 000000000000, which internal/judge/versions.txt does not name"},
		{name: "no gate.json", base: v2run, want: NotJudged,
			edit: func(t *testing.T, dir string) { must(t, os.Remove(filepath.Join(dir, "gate.json"))) },
			why: "gate.json records no gate/combine.sh sha256 and no internal/judge version, so no judge version is " +
				"named"},
		// A run bin/laug judge judged records the internal/judge entry and no gate/combine.sh: judged under the
		// version it names, whatever sources judged it.
		{name: "a judge entry v3", base: v2run, edit: judgeEntry("v3", false, true), want: Identical},
		{name: "a judge entry v1", base: v1run, edit: judgeEntry("v1", false, true), want: Identical},
		{name: "a judge entry v3 beside a gate combine.sh v1", base: v1run, edit: judgeEntry("v3", true, true),
			want: Identical},
		{name: "a judge entry no version names", base: v2run, edit: judgeEntry("v9", false, false),
			want: NotJudged,
			why:  "gate.json records internal/judge version v9, which internal/judge/versions.txt does not name"},
		{name: "a judge entry version that is no name", base: v2run, edit: judgeEntry("v 3", false, false),
			want: NotJudged,
			why: "gate.json records no gate/combine.sh sha256 and no internal/judge version, so no judge version is " +
				"named"},
		{name: "a judge entry v3, not held", base: v2run, edit: judgeEntry("v3", false, true),
			notHeld: "internal/judge and internal/jsonx have changes not committed", want: NotJudged,
			why: "internal/judge and internal/jsonx have changes not committed"},
		{name: "a judge not ported", base: v2run, want: NotJudged,
			edit: recordJudge("2a9ff8b3f8f0352934d469d877d2a05fb396a3f0d23d2e89ca708563d019c26b"),
			why:  "gate.json records gate/combine.sh sha256 2a9ff8b3f8f0, which internal/judge/versions.txt does not name"},
		{name: "not held", base: v2run, notHeld: "laug was built from 1111111, and HEAD is 2222222", want: NotJudged,
			why: "laug was built from 1111111, and HEAD is 2222222"},
		{name: "not held, and an unnamed judge", base: v2run, notHeld: "held", want: NotJudged,
			edit: recordJudge(strings.Repeat("0", 64)),
			why:  "held; gate.json records gate/combine.sh sha256 000000000000, which"},
		{name: "verdict.md tampered", base: v2run, want: Unexplained,
			edit: func(t *testing.T, dir string) { appendFile(t, filepath.Join(dir, "verdict.md"), "tampered\n") }},
		{name: "verdict.json another field", base: v1run, want: Unexplained,
			edit: func(t *testing.T, dir string) {
				editJSON(t, filepath.Join(dir, "verdict.json"), func(o *jsonx.Object) {
					o.Set("laug_commit", jsonx.Str("0000000"))
				})
			}},
		{name: "an empty item directory", base: v2run, edit: emptyItems("pl"), want: Known,
			why: "an empty item directory (pl/), which git does not carry: a fresh clone judges it as no directory"},
		{name: "two empty item directories", base: v2run, edit: emptyItems("pl", "steady"), want: Known,
			why: "an empty item directory (steady/, pl/), which git does not carry: a fresh clone judges it as no " +
				"directory"},
		{name: "an empty item directory in v1", base: v1run, edit: emptyItems("steady"), want: Known,
			why: "an empty item directory (steady/), which git does not carry: a fresh clone judges it as no " +
				"directory"},
		{name: "an empty item directory, and another field", base: v2run, want: Unexplained,
			edit: func(t *testing.T, dir string) {
				emptyItems("pl")(t, dir)
				editJSON(t, filepath.Join(dir, "verdict.json"), func(o *jsonx.Object) {
					o.Set("laug_commit", jsonx.Str("0000000"))
				})
			}},
		{name: "an empty item directory, a file there now", base: v2run, want: Unexplained,
			edit: func(t *testing.T, dir string) {
				emptyItems("pl")(t, dir)
				writeFile(t, filepath.Join(dir, "pl"), []byte("x\n"))
			}},
		{name: "an empty item directory, a dangling link there now", base: v2run, want: Unexplained,
			edit: func(t *testing.T, dir string) {
				emptyItems("pl")(t, dir)
				must(t, os.Symlink("nowhere", filepath.Join(dir, "pl")))
			}},
		{name: "a T1 trap", base: t1run, want: Rerendered, why: "trap T1"},
		{name: "a T2 trap", base: t2run, want: Rerendered, why: "trap T2"},
		{name: "a T2 trap, verdict.md tampered", base: t2run, want: Unexplained,
			edit: func(t *testing.T, dir string) {
				data := bytes.Replace(readFile(t, filepath.Join(dir, "verdict.md")), []byte("Class: invalid."),
					[]byte("Class: invalid!"), 1)
				writeFile(t, filepath.Join(dir, "verdict.md"), data)
			}},
		{name: "a trap no form gives", base: v2run, want: Unexplained,
			edit: func(t *testing.T, dir string) {
				trapShaped(t, dir)
				g, _ := jsonx.Parse(readFile(t, filepath.Join(dir, "verdict.json")))
				o, _ := g.Obj()
				id, _ := o.Get("gate_id")
				gid, _ := id.Str()
				writeFile(t, filepath.Join(dir, "verdict.md"), []byte("## laug · verdict for raun 0000000\n\nGate run "+
					gid+" on server 1.32.1 stopped at step wave before gate/combine.sh judged it: stopped.\n\n"+
					"Class: invalid.\n\n**BLOCK**\n"))
			}},
		{name: "stopped_at set, verdict.md a judge's", base: v2run, edit: trapShaped, want: Unexplained},
		{name: "stopped_at an empty string", base: v2run, want: Unexplained,
			edit: func(t *testing.T, dir string) {
				editJSON(t, filepath.Join(dir, "verdict.json"), func(o *jsonx.Object) {
					o.Set("stopped_at", jsonx.Str(""))
				})
			}},
		{name: "verdict.json two values", base: v2run, want: Unexplained,
			edit: func(t *testing.T, dir string) { appendFile(t, filepath.Join(dir, "verdict.json"), "{}\n") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), c.name)
			copyTree(t, runDir(c.base), dir)
			if c.edit != nil {
				c.edit(t, dir)
			}
			a := Rejudge(dir, Checkout{Root: root, NotHeld: c.notHeld})
			if a.ID != c.name || a.Outcome != c.want || c.want == NotJudged && !strings.HasPrefix(a.Why, c.why) ||
				c.want != NotJudged && a.Why != c.why {
				t.Fatalf("%s: %q; want %s, %q", a.Outcome, a.Why, c.want, c.why)
			}
		})
	}
}

// TestRejudgeAll lists a made results directory: a run by a link, under the name it resolves to; a dot directory and
// a file skipped; and the two ways there is nothing to judge.
func TestRejudgeAll(t *testing.T) {
	top := t.TempDir()
	res := filepath.Join(top, "results")
	copyTree(t, filepath.Join(results, "g20261018-092540"), filepath.Join(res, "b"))
	copyTree(t, filepath.Join(results, "g20261018-092540"), filepath.Join(top, "store", "h"))
	copyTree(t, filepath.Join(results, "g20261018-092540"), filepath.Join(res, ".x"))
	must(t, os.Remove(filepath.Join(res, "b", "verdict.md")))
	must(t, os.Symlink(filepath.Join("..", "store", "h"), filepath.Join(res, "a")))
	writeFile(t, filepath.Join(res, "README"), []byte("not a run\n"))
	answers, err := RejudgeAll(res, Checkout{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, a := range answers {
		lines = append(lines, a.Line())
	}
	want := []string{"h  identical", "b  not judged: no verdict.md in (outside the repository)/b to compare with"}
	if !slices.Equal(lines, want) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if c := Count(answers); c.String() != "2 runs: 1 identical, 0 known, 0 re-rendered, 0 unexplained, 1 not judged" ||
		c.Exit() != 2 {
		t.Errorf("counts %s, exit %d", c, c.Exit())
	}
	must(t, os.MkdirAll(filepath.Join(top, "none"), 0o755))
	if _, err := RejudgeAll(filepath.Join(top, "none"), Checkout{Root: root}); !errors.Is(err, ErrNoRun) ||
		err.Error() != "no gate run in (outside the repository)/none" {
		t.Errorf("an empty results directory: %v", err)
	}
	if _, err := RejudgeAll(filepath.Join(top, "nowhere"), Checkout{Root: root}); !errors.Is(err, ErrNoResults) ||
		err.Error() != "no results directory at (outside the repository)/nowhere" {
		t.Errorf("no results directory: %v", err)
	}
	if got := (Checkout{Root: root}).Shown(filepath.Join(results, "g1", "..", "g2")); got != "gate/results/g2" {
		t.Errorf("Shown inside the repository: %s", got)
	}
	if got := (Checkout{}).Shown(results); got != "(outside the repository)/results" {
		t.Errorf("Shown with no root: %s", got)
	}
}

// TestCounts holds the counts line and its exit: 2 with a run not judged, else 1 with one unexplained, else 0.
func TestCounts(t *testing.T) {
	for _, c := range []struct {
		counts Counts
		line   string
		exit   int
	}{
		{Counts{Identical: 3, Known: 1, Rerendered: 1}, "5 runs: 3 identical, 1 known, 1 re-rendered, 0 unexplained, " +
			"0 not judged", 0},
		{Counts{Identical: 1, Unexplained: 1}, "2 runs: 1 identical, 0 known, 0 re-rendered, 1 unexplained, 0 not " +
			"judged", 1},
		{Counts{Unexplained: 1, NotJudged: 1}, "2 runs: 0 identical, 0 known, 0 re-rendered, 1 unexplained, 1 not " +
			"judged", 2},
	} {
		if c.counts.String() != c.line || c.counts.Exit() != c.exit {
			t.Errorf("%s, exit %d; want %s, exit %d", c.counts, c.counts.Exit(), c.line, c.exit)
		}
	}
}

// TestGone holds Gone, the GONE rule, on made pairs of verdicts: the keys it gives, or that it
// stops on an error, which explain() reads as unexplained.
func TestGone(t *testing.T) {
	r2 := func(reasons ...string) string {
		b, _ := jsonMarshalStrings(reasons)
		return `{"R2":{"reasons":` + b + `}}`
	}
	file := func(k string) string { return k + ": no JSON: " + k + "/g-" + k + ".json is missing" }
	exit := func(k string) string { return k + ": no exit record: " + k + "/exit.json is missing" }
	dir := func(k string) string { return k + ": no JSON: there is no " + k + "/ directory" }
	items := `"items":[{"key":"a"},{"key":"b"},{"key":5},{"key":"c"}]`
	for _, c := range []struct {
		name, a, b string
		keys       []string
		ok         bool
	}{
		{"one key", `{` + items + `,"rules":` + r2(file("a")) + `}`, `{"rules":` + r2(dir("a")) + `}`, []string{"a"}, true},
		{"by exit record, in items order", `{` + items + `,"rules":` + r2(exit("c"), file("a")) + `}`,
			`{"rules":` + r2(dir("a"), dir("c")) + `}`, []string{"a", "c"}, true},
		{"both in a", `{` + items + `,"rules":` + r2(file("a"), dir("a")) + `}`, `{"rules":` + r2(dir("a")) + `}`, nil, true},
		{"both in b", `{` + items + `,"rules":` + r2(file("a")) + `}`, `{"rules":` + r2(dir("a"), file("a")) + `}`, nil,
			true},
		{"not missing", `{` + items + `,"rules":` + r2("a: no JSON: a/x is short") + `}`, `{"rules":` + r2(dir("a")) + `}`,
			nil, true},
		{"a key twice", `{"items":[{"key":"a"},{"key":"a"}],"rules":` + r2(file("a")) + `}`, `{"rules":` + r2(dir("a")) + `}`,
			[]string{"a", "a"}, true},
		{"a key with a newline", `{"items":[{"key":"a\nb"}],"rules":` + r2(file("a\nb")) + `}`,
			`{"rules":` + r2(dir("a\nb")) + `}`, []string{"a\nb"}, true},
		{"no rules", `{` + items + `}`, `{"rules":` + r2(dir("a")) + `}`, nil, true},
		{"reasons not strings", `{` + items + `,"rules":{"R2":{"reasons":[5,null,"` + file("a") + `"]}}}`,
			`{"rules":` + r2(dir("a")) + `}`, []string{"a"}, true},
		{"reasons an object", `{` + items + `,"rules":{"R2":{"reasons":{"x":"` + file("a") + `"}}}}`,
			`{"rules":` + r2(dir("a")) + `}`, []string{"a"}, true},
		{"items a string", `{"items":"a","rules":` + r2(file("a")) + `}`, `{}`, nil, true},
		{"an empty file", ``, `{}`, nil, true},
		{"an item a number", `{"items":[5],"rules":` + r2(file("a")) + `}`, `{}`, nil, false},
		{"rules a string, with a key", `{` + items + `,"rules":"x"}`, `{}`, nil, false},
		{"rules a string, no key", `{"items":[],"rules":"x"}`, `{}`, nil, true},
		{"R2 a number", `{` + items + `,"rules":{"R2":5}}`, `{}`, nil, false},
		{"b's rules a string, a key a's rule names", `{` + items + `,"rules":` + r2(file("a")) + `}`,
			`{"rules":"x"}`, nil, false},
		{"b's rules a string, no key a's rule names", `{` + items + `,"rules":` + r2() + `}`, `{"rules":"x"}`, nil,
			true},
		{"a an array", `[1]`, `{}`, nil, false},
		{"a does not parse", `{"items":`, `{}`, nil, false},
		{"two values, the first read", `{` + items + `,"rules":` + r2(file("b")) + `} {"items":[]}`,
			`{"rules":` + r2(dir("b")) + `}`, []string{"b"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			keys, ok := Gone([]byte(c.a), []byte(c.b))
			if ok != c.ok || !slices.Equal(keys, c.keys) {
				t.Errorf("Gone: %q, %t; want %q, %t", keys, ok, c.keys, c.ok)
			}
		})
	}
}

// jsonMarshalStrings is a JSON array of strings as jq writes it.
func jsonMarshalStrings(ss []string) (string, error) {
	vs := make([]jsonx.Value, len(ss))
	for i, s := range ss {
		vs[i] = jsonx.Str(s)
	}
	return jsonx.Arr(vs).String(), nil
}

// recordJudge makes a run's gate.json record sha as its gate/combine.sh.
func recordJudge(sha string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		p := filepath.Join(dir, "gate.json")
		v, err := jsonx.Parse(readFile(t, p))
		if err != nil {
			t.Fatal(err)
		}
		o, _ := v.Obj()
		in, _ := o.Get("inputs")
		for _, e := range members(in) {
			eo, ok := e.Obj()
			if p, _ := eo.Get("path"); ok && jsonx.Equal(p, jsonx.Str("gate/combine.sh")) {
				eo.Set("sha256", jsonx.Str(sha))
			}
		}
		writeFile(t, p, jsonx.Marshal(v))
	}
}

// judgeEntry makes the run's gate.json record the internal/judge entry gate/lib.sh's judge_input records, version v
// and the sources' sha256 a made one, last in its inputs: in place of its gate/combine.sh input, or beside it with
// keep. With rejudge, the run's two files are then the Go judge's under the version it records (writeJudged).
func judgeEntry(v string, keep, rejudge bool) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		p := filepath.Join(dir, "gate.json")
		g, err := jsonx.Parse(readFile(t, p))
		if err != nil {
			t.Fatal(err)
		}
		o, _ := g.Obj()
		in, _ := o.Get("inputs")
		var list []jsonx.Value
		for _, e := range members(in) {
			if keep || !jsonx.Equal(index(e, "path"), jsonx.Str("gate/combine.sh")) {
				list = append(list, e)
			}
		}
		e, err := jsonx.Parse([]byte(`{"path":"internal/judge","sha256":"` + strings.Repeat("1", 64) +
			`","changed_vs_base":true,"version":` + jsonx.Str(v).String() + `,"go":"go1.0","tree_clean":true}`))
		if err != nil {
			t.Fatal(err)
		}
		o.Set("inputs", jsonx.Arr(append(list, e)))
		writeFile(t, p, jsonx.Marshal(g))
		if rejudge {
			writeJudged(t, dir)
		}
	}
}

// writeJudged writes the Go judge's verdict.json and verdict.md for the run in dir under the version it records.
func writeJudged(t *testing.T, dir string) {
	t.Helper()
	v, ok := ReadRecorded(readFile(t, filepath.Join(dir, "gate.json"))).Version()
	if !ok {
		t.Fatal("the run records no version")
	}
	res, err := judge.Judge(os.DirFS(dir), v, judge.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "verdict.json"), res.JSON)
	writeFile(t, filepath.Join(dir, "verdict.md"), res.MD)
}

// copyTree copies the directory src to dst, which must not exist: directories, regular files and links.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, data, 0o644))
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(text)
	must(t, errors.Join(err, f.Close()))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestFirstDiff names the first line two files differ at, a line one lacks, and a last line with no newline.
func TestFirstDiff(t *testing.T) {
	for _, c := range []struct {
		a, b      string
		n         int
		want, got string
	}{
		{"a\nb\n", "a\nb\n", 0, "", ""},
		{"a\nb\n", "a\nc\n", 2, "b", "c"},
		{"a\n", "a\nb\n", 2, "(the file ends)", "b"},
		{"a\n", "a", 1, "a", "a (no newline at the end)"},
		{"a\n", "a\n\n", 2, "(the file ends)", ""},
		{"x\n", "x\n(the file ends)\n", 2, "(the file ends)", "(the file ends)"},
		{"", "a\n", 1, "(the file ends)", "a"},
	} {
		n, want, got := FirstDiff([]byte(c.a), []byte(c.b))
		if n != c.n || want != c.want || got != c.got {
			t.Errorf("FirstDiff(%q, %q) = %d, %q, %q; want %d, %q, %q", c.a, c.b, n, want, got, c.n, c.want, c.got)
		}
	}
}

// TestRejudgeUnreadable: a verdict.md that cannot be read is not judged, and the line shows no absolute path.
func TestRejudgeUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 0")
	}
	dir := filepath.Join(t.TempDir(), "r")
	copyTree(t, filepath.Join(results, "g20261018-092540"), dir)
	must(t, os.Chmod(filepath.Join(dir, "verdict.md"), 0))
	a := Rejudge(dir, Checkout{Root: root})
	if want := "r  not judged: verdict.md in (outside the repository)/r cannot be read: permission denied"; a.Line() != want {
		t.Errorf("got  %s\nwant %s", a.Line(), want)
	}
}

// TestReadRecorded reads the judge gate.json texts record: the table's and the 158 committed runs', 21 of them with an
// internal/judge entry.
func TestReadRecorded(t *testing.T) {
	const v1 = "284a759c54557c18bcbdfc257414199c9c94bfb573e3234e2265ecf18ab5366c"
	v3, _ := judge.Named("v3")
	src := strings.Repeat("ab", 32)
	entry := func(version string) string {
		return `{"path":"internal/judge","sha256":"` + src + `","version":` + version + `}`
	}
	combine := `{"path":"gate/combine.sh","sha256":"` + v1 + `"}`
	for _, c := range []struct {
		name, gate string
		want       Recorded
		named      string // the version Recorded.Version gives, "" for none
	}{
		{"an entry", `{"inputs":[` + entry(`"v3"`) + `]}`, Recorded{Entry: "v3", Sources: src}, "v3"},
		{"gate/combine.sh first", `{"inputs":[` + entry(`"v3"`) + `,` + combine + `]}`, Recorded{SHA256: v1}, "v1"},
		{"a gate/combine.sh sha256 that is none, then an entry", `{"inputs":[{"path":"gate/combine.sh","sha256":"x"},` +
			entry(`"v1"`) + `]}`, Recorded{Entry: "v1", Sources: src}, "v1"},
		{"a version no line names", `{"inputs":[` + entry(`"v9"`) + `]}`, Recorded{Entry: "v9", Sources: src}, ""},
		{"a version that is no name", `{"inputs":[` + entry(`"v 3"`) + `]}`, Recorded{}, ""},
		{"a version that starts with a dot", `{"inputs":[` + entry(`".v3"`) + `]}`, Recorded{}, ""},
		{"a version a number", `{"inputs":[` + entry(`3`) + `]}`, Recorded{}, ""},
		{"the first string of two entries", `{"inputs":[` + entry(`null`) + `,` + entry(`"v2"`) + `]}`,
			Recorded{Entry: "v2", Sources: src}, "v2"},
		{"a trailing newline", `{"inputs":[` + entry(`"v3\n\n"`) + `]}`, Recorded{Entry: "v3", Sources: src}, "v3"},
		{"a NUL byte", `{"inputs":[` + entry(`"v\u00003"`) + `]}`, Recorded{Entry: "v3", Sources: src}, "v3"},
		{"inputs an object", `{"inputs":{"a":` + entry(`"v3"`) + `}}`, Recorded{Entry: "v3", Sources: src}, "v3"},
		{"null, then an entry", `null {"inputs":[` + entry(`"v3"`) + `]}`, Recorded{Entry: "v3", Sources: src}, "v3"},
		{"two values with one each", `{"inputs":[` + entry(`"v3"`) + `]} {"inputs":[` + entry(`"v3"`) + `]}`,
			Recorded{}, ""},
		{"an entry, then an array", `{"inputs":[` + entry(`"v3"`) + `]} []`, Recorded{}, ""},
		{"a parse error", `{"inputs":[` + entry(`"v3"`) + `]} {`, Recorded{}, ""},
		{"sources that are no sha256", `{"inputs":[{"path":"internal/judge","sha256":"x","version":"v3"}]}`,
			Recorded{Entry: "v3"}, "v3"},
		{"neither", `{"inputs":[]}`, Recorded{}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := ReadRecorded([]byte(c.gate))
			v, ok := r.Version()
			if r != c.want || v.Name != c.named || ok != (c.named != "") {
				t.Errorf("%+v, version %q %t; want %+v, %q", r, v.Name, ok, c.want, c.named)
			}
			if ok != (r.Why() == "") {
				t.Errorf("Why %q with a version %t", r.Why(), ok)
			}
		})
	}
	if v3.SHA256 == "" {
		t.Fatal("no v3 in the registry")
	}
	entries := 0
	for _, r := range committed(t) {
		if r.Recorded.Entry != "" {
			entries++
		}
	}
	c := census(t)
	byEntry := func(row jsonx.Value) bool { return member(row, "combine_sha256").IsNull() }
	if all := c.Count(nil, byEntry); all != 21 {
		t.Errorf("the gates table records %d runs with an internal/judge entry, want 21", all)
	}
	if want := c.Count(c.Here, byEntry); entries != want {
		t.Errorf("%d committed runs record an internal/judge entry, want %d", entries, want)
	}
}
