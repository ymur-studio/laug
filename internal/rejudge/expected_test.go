package rejudge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ymur-studio/laug/internal/judge"
)

// TestRuns lists gate/results: the runs of the gates table here, 158 with none archived, in byte order, each
// recording a registered judge, 52 v1, 84 v2 and 1 v2-r7off by gate/combine.sh's sha256 and 21 v3 by an
// internal/judge entry, as their rows count them; on a made results directory, a file that is not a run, a run with
// no gate.json, one whose gate.json does not parse, one that records a judge the registry does not name, and runs
// bin/laug judge judged, whose internal/judge entry names the version unless a gate/combine.sh sha256 does; and on
// disk, the links, dot names and names that are not UTF-8 that a shell's glob and -d meet (Runs).
func TestRuns(t *testing.T) {
	count := map[string]int{}
	runs := committed(t)
	for i, r := range runs {
		if i > 0 && runs[i-1].Gid >= r.Gid {
			t.Errorf("%s after %s", r.Gid, runs[i-1].Gid)
		}
		bySHA := r.SHA256 != "" && r.Version.SHA256 == r.SHA256
		byEntry := r.SHA256 == "" && r.Recorded.Entry == r.Version.Name
		if !r.Registered() || !bySHA && !byEntry {
			t.Errorf("%s: %q, %+v", r.Gid, r.SHA256, r.Version)
		}
		count[r.Version.Name]++
	}
	c := census(t)
	every := func(string) bool { return true }
	if got := versions(c, c.IDs, every); got != "map[v1:52 v2:84 v2-r7off:1 v3:21]" {
		t.Errorf("the gates rows by version: %s", got)
	}
	if got, want := fmt.Sprint(count), versions(c, c.Here, every); got != want {
		t.Errorf("runs by version: %s, want %s", got, want)
	}

	v2, _ := judge.Named("v2")
	gate := func(sha string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`{"inputs":[{"path":"gate/combine.sh","sha256":"` + sha + `"}]}`)}
	}
	made, err := Runs(fstest.MapFS{
		"README":           {Data: []byte("not a run")},
		"b/gate.json":      gate(v2.SHA256),
		"a/gate.json":      gate(strings.Repeat("0", 64)),
		"c/clock.json":     {Data: []byte("{}")},
		"a-b/gate.json":    {Data: []byte("{")},
		"d/gate.json/x":    {Data: []byte("a directory")},
		"e/gate.json":      {Data: []byte(`{"inputs":[]}`)},
		"b/verdict.json":   {Data: []byte("{}")},
		"b/steady/x.json":  {Data: []byte("{}")},
		"b/.lease.json":    {Data: []byte("{}")},
		"f/gate.json":      gate("2a9ff8b3f8f0352934d469d877d2a05fb396a3f0d23d2e89ca708563d019c26b"),
		"g/thresholds.yml": {Data: []byte("x")},
		"h/gate.json":      {Data: []byte(`{"inputs":[{"path":"internal/judge","version":"v3"}]}`)},
		"i/gate.json":      {Data: []byte(`{"inputs":[{"path":"internal/judge","version":"v9"}]}`)},
		"j/gate.json": {Data: []byte(`{"inputs":[{"path":"internal/judge","version":"v3"},` +
			`{"path":"gate/combine.sh","sha256":"` + v2.SHA256 + `"}]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range made {
		got = append(got, fmt.Sprintf("%s %s %v", r.Gid, r.Version.Name, r.Registered()))
	}
	want := "a  false|a-b  false|b v2 true|c  false|d  false|e  false|f  false|g  false|h v3 true|i  false|j v2 true"
	if strings.Join(got, "|") != want {
		t.Errorf("Runs:\n  got  %s\n  want %s", strings.Join(got, "|"), want)
	}

	// On disk: a link to a run directory is a run, under the link's name, as is a link to a dot directory; a dot
	// directory is not, nor a dangling link or a link to a file. With bash present, the shell's own loop lists the
	// same names.
	v1, _ := judge.Named("v1")
	top := t.TempDir()
	dir := filepath.Join(top, "results")
	write := func(rel string, f *fstest.MapFile) {
		p := filepath.Join(top, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		if err := os.Symlink(target, filepath.Join(top, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	write("store/h/gate.json", gate(v2.SHA256))
	write("results/a/gate.json", gate(v2.SHA256))
	write("results/.x/gate.json", gate(v2.SHA256))
	write("results/.y/gate.json", gate(v1.SHA256))
	write("results/README", &fstest.MapFile{Data: []byte("not a run")})
	link("../store/h", "results/h")
	link(".y", "results/i")
	link("nowhere", "results/j")
	link("README", "results/k")
	list := func() string {
		t.Helper()
		runs, err := Runs(os.DirFS(dir))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range runs {
			got = append(got, fmt.Sprintf("%s %s %v", r.Gid, r.Version.Name, r.Registered()))
		}
		return strings.Join(got, "|")
	}
	if got, want := list(), "a v2 true|h v2 true|i v1 true"; got != want {
		t.Errorf("Runs on disk:\n  got  %s\n  want %s", got, want)
	}
	if bash, err := exec.LookPath("bash"); err != nil {
		t.Logf("not checked against bash's glob: %v", err)
	} else {
		loop := `export LC_ALL=C; for p in "$1"/*; do [[ -d "$p" ]] || continue; printf '%s\n' "${p##*/}"; done`
		out, err := exec.Command(bash, "-c", loop, "bash", dir).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", " "); got != "a h i" {
			t.Errorf("bash's glob and -d list %q, and Runs a h i", got)
		}
	}
	// A name that is not UTF-8: a file is skipped, as -d skips it; a directory is an error, since an fs.FS cannot
	// open it and the shell would count it.
	odd := filepath.Join(dir, "r\xff")
	write("results/r\xff", &fstest.MapFile{Data: []byte("not a run")})
	if got := list(); got != "a v2 true|h v2 true|i v1 true" {
		t.Errorf("Runs with a file r\\xff: %s", got)
	}
	if err := os.Remove(odd); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Runs(os.DirFS(dir)); err == nil || !strings.Contains(err.Error(), "not UTF-8") {
		t.Errorf("Runs with a directory r\\xff: %v, want an error that says the name is not UTF-8", err)
	}
}

// TestParseExpected reads the expected file's grammar: a line per run, its gid, kind and argument; comments and blank
// lines; and each way a line is refused.
func TestParseExpected(t *testing.T) {
	good := "# the runs that are not judged identical as they stand\n\n" +
		"g20261014-212013  empty-dirs  steady\n" +
		"g20261014-212526  empty-dirs  steady,pl\n" +
		"g20261014-213037  trap        T1\n" +
		"g20261017-014632\ttrap\tT2\n"
	ex, err := ParseExpected([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(ex) != 4 || ex["g20261014-213037"] != (Expectation{"g20261014-213037", Trap, "T1"}) ||
		strings.Join(ex["g20261014-212526"].Dirs(), " ") != "steady pl" || ex["g20261014-213037"].Dirs() != nil {
		t.Errorf("ParseExpected: %+v", ex)
	}
	if ex, err := ParseExpected(nil); err != nil || len(ex) != 0 {
		t.Errorf("an empty file: %v, %v", ex, err)
	}
	for _, bad := range []string{
		"g1 trap",
		"g1 trap T1 more",
		"-g1 trap T1",
		"g1 trap T4",
		"g1 known x",
		"g1 empty-dirs steady,",
		"g1 empty-dirs .steady",
		"g2 trap T1\ng1 trap T1",
		"g1 trap T1\ng1 trap T2",
		" # a comment after a blank is a line",
	} {
		if _, err := ParseExpected([]byte(bad)); err == nil {
			t.Errorf("ParseExpected(%q): no error", bad)
		}
	}
	if _, err := ReadExpected(fstest.MapFS{"x": {Data: []byte("g1 trap T1\r\n")}}, "x"); err == nil {
		t.Error("a carriage return: no error")
	}
	if ex, err := ReadExpected(fstest.MapFS{}, "x"); err != nil || len(ex) != 0 {
		t.Errorf("no file: %v, %v", ex, err)
	}
}
