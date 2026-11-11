package judge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// baseArm is the harness's run with an incumbent arm, in recording mode.
var baseArm = filepath.Join("..", "..", "load", "testdata", "gate", "base-arm")

// fixtureCopy is base-arm copied to a new directory, each file named in patches parsed, patched as input patches a
// gathered run, and written back in jq's default form.
func fixtureCopy(t *testing.T, patches map[string][]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(baseArm)); err != nil {
		t.Fatal(err)
	}
	for name, ps := range patches {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		v, err := jsonx.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			path, text, _ := bytes.Cut([]byte(p), []byte("="))
			nv, err := jsonx.Parse(text)
			if err != nil {
				t.Fatal(err)
			}
			if v, err = setPath(v, steps(t, string(path)), nv, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), jsonx.Marshal(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestJudgeReadsBack holds Judge's order: JUDGE writes verdict.json, and RENDER reads that file back (readBack),
// so verdict.md prints each number as a literal of its JSON text. A count's d of 1e16 is
// computed, so verdict.json holds 1e+16, jq's dtoa form; read back it is the literal 1E+16, decNumber's form, and
// verdict.md prints +1E+16. Rendering the object JUDGE built, not the bytes, would print +1e+16. No committed run
// has such a d.
func TestJudgeReadsBack(t *testing.T) {
	dir := fixtureCopy(t, map[string][]string{
		"steady/g20261014-140000-steady.verdict.json":       {"readings.workflows_completed=10000000000000000"},
		"incumbent/g20261014-140000-incumbent.verdict.json": {"readings.workflows_completed=0"},
	})
	v2, _ := Named("v2")
	res, err := Judge(os.DirFS(dir), v2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(res.JSON, []byte(`"d": 1e+16,`)) {
		t.Errorf("verdict.json has no d of 1e+16:\n%s", res.JSON)
	}
	row := "\n| workflows_completed | 10000000000000000 | 0 | +1E+16 |  | recorded |\n"
	if !bytes.Contains(res.MD, []byte(row)) {
		t.Errorf("verdict.md has no line %q:\n%s", row[1:len(row)-1], res.MD)
	}
	if res.Verdict != "ADVANCE" || res.Exit() != 0 {
		t.Errorf("verdict %s, exit %d", res.Verdict, res.Exit())
	}
}

// TestJudgeFails holds Judge's errors, each laug judge's exit 2 with no file written: no directory; a jq error in
// JUDGE, an exit record whose end is a string (endedOf); and one in RENDER on a verdict JUDGE wrote, a gate.json
// host that is a string in a judging comparison whose set names no host, which g6 reports without indexing it, so
// it reaches verdict.json and fails at RENDER's `.host.type` (comparison).
func TestJudgeFails(t *testing.T) {
	v2, _ := Named("v2")
	if _, err := Judge(os.DirFS(filepath.Join(t.TempDir(), "none")), v2, Options{}); !errors.Is(err, ErrNoRunDir) {
		t.Errorf("no directory: %v, want ErrNoRunDir", err)
	}
	for _, c := range []struct {
		step    string
		patches map[string][]string
	}{
		{"judge", map[string][]string{"steady/exit.json": {`end="x"`}}},
		{"render", map[string][]string{"gate.json": {`host="x"`}, "thresholds.json": {
			"compare.rows[0].tolerance=1", "compare.rows[1].tolerance=1", "compare.rows[2].tolerance=0.01",
			"compare.rows[3].tolerance=0.01", "compare.rows[4].tolerance=0.01", "compare.wave=" + judgingWave,
			`compare.evidence="cells/results/aa.txt"`, "compare.gates=20", "resolved.host=null"}}},
	} {
		dir := fixtureCopy(t, c.patches)
		_, err := Judge(os.DirFS(dir), v2, Options{})
		var f *Failure
		var e *jqError
		if !errors.As(err, &f) || f.Step != c.step || !errors.As(err, &e) {
			t.Errorf("%s: %v, want a %s Failure of a jq error", c.step, err, c.step)
		}
	}
}

// TestStdout holds Stdout's last two lines, the files' line and the verdict, with the directory as given.
func TestStdout(t *testing.T) {
	r := &Result{Verdict: "BLOCK", summary: []byte("laug · gate g, server s: BLOCK, class invalid\n")}
	want := "laug · gate g, server s: BLOCK, class invalid\n" +
		"written          runs/g1//verdict.json, runs/g1//verdict.md\nBLOCK\n"
	if got := string(r.Stdout("runs/g1/")); got != want {
		t.Errorf("Stdout = %q, want %q", got, want)
	}
	if r.Exit() != 1 {
		t.Errorf("Exit = %d, want 1", r.Exit())
	}
}
