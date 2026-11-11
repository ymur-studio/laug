package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/_report"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/rejudge"
	"github.com/ymur-studio/laug/internal/tables"
)

// reportRuns are two kept lab runs of the committed results, the A/A gate's ADVANCE and the A/B gate's BLOCK; the
// tests copy them into a temporary checkout, never the dev host's runs, which leave the tree.
var reportRuns = []string{"g20261018-075357", "g20261018-092540"}

// repoRoot is the checkout this package is in, three directories up: _report/cmd/report.
func repoRoot(t *testing.T) string {
	t.Helper()
	r, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// devRuns is the copy of the dev host runs the tests name, from the checkout's root
// (internal/rejudge/testdata/runs/README.md); labRuns the copy of the lab host runs they name that are not kept in
// gate/results (internal/rejudge/testdata/lab/README.md).
const (
	devRuns = "internal/rejudge/testdata/runs"
	labRuns = "internal/rejudge/testdata/lab"
)

// runPath is where the committed run gid is, from the checkout's root: its copy under devRuns or labRuns when there
// is one, else gate/results.
func runPath(t *testing.T, gid string) string {
	for _, d := range []string{devRuns, labRuns} {
		if fi, err := os.Stat(filepath.Join(repoRoot(t), d, gid)); err == nil && fi.IsDir() {
			return d + "/" + gid
		}
	}
	return "gate/results/" + gid
}

// judgeCheckout is a temporary checkout that holds the judge's sources as this one does, so that judge.SourcesIn reads
// there the sources this test binary embeds.
func judgeCheckout(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	srcs, err := judge.SourcesIn(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		writeFile(t, filepath.Join(r, filepath.FromSlash(s.Path)), readText(t, filepath.Join(repoRoot(t),
			filepath.FromSlash(s.Path))))
	}
	return r
}

// copyRun copies the committed run gid to dst: its directories and regular files.
func copyRun(t *testing.T, gid, dst string) {
	t.Helper()
	src := filepath.Join(repoRoot(t), filepath.FromSlash(runPath(t, gid)))
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// firstLine is s up to its first newline.
func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// runReportArgs runs the command in root with args, the home directory a temporary one.
func runReportArgs(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a := &app{root: root, home: t.TempDir(), stdout: &stdout, stderr: &stderr}
	code := a.run(context.Background(), args)
	return code, stdout.String(), stderr.String()
}

// TestReport drives the command's own flags: -h on stdout, its summary first and its synopsis; bare, it names neither
// -docs nor -tables, so its usage, exit 2, nothing written; -docs with -tables the same; a positional argument is
// refused before either check; -check alone needs -tables; a flag it lacks is a mistake. The two write paths are
// TestReportDocs and TestReportTables.
func TestReport(t *testing.T) {
	root := t.TempDir()
	code, out, errOut := runReportArgs(t, root, "-h")
	if code != 0 || errOut != "" || !strings.HasPrefix(out, "laug · report: write the committed documents or tables "+
		"from the records\nusage: go -C _report run ./cmd/report -docs | -tables [-check]\n") ||
		!strings.Contains(out, "  -docs\n") || !strings.Contains(out, "  -tables\n") ||
		!strings.Contains(out, "  -check\n") {
		t.Errorf("-h: exit %d\n%s%s", code, out, errOut)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "laug · report: writes docs/ with -docs or docs/tables/ with -tables; name one"},
		{[]string{"-docs", "-tables"}, "laug · report: -docs writes docs/ and -tables writes docs/tables/; name one"},
		{[]string{"x"}, `laug · report: takes no arguments, got ["x"]`},
		{[]string{"-check"}, "laug · report: -check checks the tables -tables writes, so it needs -tables"},
		{[]string{"-out", "x"}, "laug · report: flag provided but not defined: -out"},
	} {
		code, out, errOut := runReportArgs(t, root, c.args...)
		if code != 2 || out != "" || firstLine(errOut) != c.want {
			t.Errorf("%q: exit %d, want 2\nstdout:\n%sstderr:\n%s", c.args, code, out, errOut)
		}
		if ents, _ := os.ReadDir(root); len(ents) != 0 {
			t.Errorf("%q wrote %d entries", c.args, len(ents))
		}
	}
	if code, out, errOut := runReportArgs(t, "", "-docs"); code != 2 || out != "" ||
		!strings.Contains(errOut, "needs a laug checkout") {
		t.Errorf("outside a checkout: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
}

// TestFindRoot: from _report, and from this package's directory under it, the checkout is the directory that holds
// go.mod and gate/run.sh, this tree's root, and not _report, which holds a go.mod and no gate/run.sh.
func TestFindRoot(t *testing.T) {
	want := repoRoot(t)
	for _, dir := range []string{".", "..", "../.."} {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := findRoot(abs); got != want {
			t.Errorf("findRoot(%s) = %q, want %q", abs, got, want)
		}
	}
	if got := findRoot(t.TempDir()); got != "" {
		t.Errorf("findRoot of a temporary directory = %q, want none", got)
	}
	if got := findRoot("relative"); got != "" {
		t.Errorf("findRoot of a relative path = %q, want none", got)
	}
}

// TestShown: an absolute path below the checkout prints relative to it, one below the home directory as ~/ and the
// rest, and any other as it is.
func TestShown(t *testing.T) {
	a := &app{root: "/srv/laug", home: "/srv/people/someone"}
	for _, c := range []struct{ in, want string }{
		{"reading /srv/laug/docs/tables: x", "reading docs/tables: x"},
		{"open /srv/people/someone/x.json: no such file", "open ~/x.json: no such file"},
		{"/opt/x stays", "/opt/x stays"},
		{"a 'quoted /srv/laug/path'", "a 'quoted path'"},
	} {
		if got := a.shown(c.in); got != c.want {
			t.Errorf("shown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// docsRendered is what -docs writes in root from its gate/results: the tables (report.AddRows over a build) and the
// documents report.Docs renders from them.
func docsRendered(t *testing.T, root string) ([]report.RowsAdded, []report.File) {
	t.Helper()
	rs, err := report.Load(root, filepath.Join(root, "gate", "results"), rejudge.Checkout{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	built, err := report.Tables(rs, os.DirFS(root), judge.Self().SHA256)
	if err != nil {
		t.Fatal(err)
	}
	ts, changes, err := report.AddRows(os.DirFS(root), built)
	if err != nil || changes != nil {
		t.Fatalf("AddRows: %v, %v", changes, err)
	}
	var merged []report.Table
	for _, a := range ts {
		merged = append(merged, a.Table)
	}
	files, err := report.Docs(rs, merged)
	if err != nil {
		t.Fatal(err)
	}
	return ts, files
}

// TestReportDocs drives -docs in a checkout that holds this binary's judge sources and two committed runs:
// docs/EVIDENCE.md and the tables written, the bytes report.Docs renders from the rows -docs writes, then left as they
// were; an SVG in docs/img that no figure writes named, exit 1, and kept; -docs with -tables a usage error; and a
// judge source that is not this binary's refused, exit 2, with nothing written.
func TestReportDocs(t *testing.T) {
	root := judgeCheckout(t)
	res := filepath.Join(root, "gate", "results")
	for _, id := range reportRuns {
		copyRun(t, id, filepath.Join(res, id)) // copies, not links: the records table walks files
	}
	evidence := filepath.Join(root, "docs", report.Evidence)
	code, out, errOut := runReportArgs(t, root, "-docs")
	first := "laug · report: docs/EVIDENCE.md, 0 figures and 10 tables from the 2 gate runs in gate/results; " +
		"Ctrl-C stops it\n" +
		"laug · rejudge, 2 runs: 2 identical, 0 known, 0 re-rendered, 0 unexplained, 0 not judged\n"
	if code != 0 || errOut != "" || !strings.HasPrefix(out, first) ||
		!strings.Contains(out, "laug · report: docs/tables/gates.jsonl: 2 rows; written: 2 rows added "+
			"(g20261018-075357, g20261018-092540)\n") ||
		!strings.Contains(out, "laug · report: docs/tables/README.md: written\n") ||
		!strings.HasSuffix(out, "laug · report: docs/EVIDENCE.md: written\n"+
			"laug · report: 12 files of 12 written into docs/\n") {
		t.Fatalf("exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	ts, files := docsRendered(t, root)
	if got := readText(t, evidence); got != string(files[0].Data) || !strings.Contains(got, "| g20261018-092540 | "+
		"m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |\n") {
		t.Errorf("docs/EVIDENCE.md is not what report.Docs renders:\n%s", got)
	}
	for _, a := range ts {
		if a.Added != nil || a.Kept != nil {
			t.Errorf("%s: after -docs, %q to add and %q kept", a.Name, a.Added, a.Kept)
		}
		if got := readText(t, filepath.Join(root, "docs", "tables", a.Files[0].Name)); got != string(a.Files[0].Data) {
			t.Errorf("docs/tables/%s is not what -docs writes", a.Files[0].Name)
		}
	}

	code, out, errOut = runReportArgs(t, root, "-docs")
	if code != 0 || errOut != "" || !strings.Contains(out, "laug · report: docs/tables/gates.jsonl: 2 rows; the same "+
		"bytes, left as it was\n") || !strings.HasSuffix(out, "laug · report: docs/EVIDENCE.md: the same bytes, left "+
		"as it was\nlaug · report: 0 files of 12 written into docs/\n") {
		t.Errorf("again: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}

	retired := filepath.Join(root, "docs", "img", "retired.svg")
	writeFile(t, retired, "<svg/>\n")
	code, out, errOut = runReportArgs(t, root, "-docs")
	if code != 1 || errOut != "laug · report: docs/img/retired.svg: no figure writes it now; git rm it, or add the "+
		"figure back\n" || readText(t, retired) != "<svg/>\n" {
		t.Errorf("a retired figure: exit %d, want 1\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	if err := os.Remove(retired); err != nil {
		t.Fatal(err)
	}

	code, out, errOut = runReportArgs(t, root, "-docs", "-tables")
	if code != 2 || out != "" || firstLine(errOut) != "laug · report: -docs writes docs/ and -tables writes "+
		"docs/tables/; name one" {
		t.Errorf("-docs -tables: exit %d, want 2\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}

	if err := os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	versions := filepath.Join(root, "internal", "judge", "versions.txt")
	writeFile(t, versions, readText(t, versions)+"\n")
	code, out, errOut = runReportArgs(t, root, "-docs")
	if code != 2 || out != "" || !strings.HasPrefix(errOut, "laug · report: -docs: the judge this binary holds "+
		"(sources' sha256 ") || !strings.HasSuffix(errOut, "): run it from the checkout, go -C _report run "+
		"./cmd/report\n") {
		t.Errorf("a judge not this binary's: exit %d, want 2\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	if _, err := os.Stat(evidence); err == nil {
		t.Errorf("a refused -docs wrote docs/EVIDENCE.md")
	}
}

// TestReportDocsAddsRows drives -docs over tables it wrote, in a temporary checkout: a run added to gate/results adds
// its rows, and every byte of every row before stays as it was, in every table; a run that leaves gate/results keeps
// its rows and its line in docs/EVIDENCE.md; and a row on disk the records give other bytes is named and refused,
// exit 1, with nothing written.
func TestReportDocsAddsRows(t *testing.T) {
	root := judgeCheckout(t)
	res := filepath.Join(root, "gate", "results")
	copyRun(t, reportRuns[0], filepath.Join(res, reportRuns[0]))
	if code, out, errOut := runReportArgs(t, root, "-docs"); code != 0 || errOut != "" {
		t.Fatalf("one run: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	dir := filepath.Join(root, "docs", "tables")
	before := dirFiles(t, dir)
	evidence := filepath.Join(root, "docs", report.Evidence)
	was := readText(t, evidence)

	copyRun(t, reportRuns[1], filepath.Join(res, reportRuns[1]))
	code, out, errOut := runReportArgs(t, root, "-docs")
	if code != 0 || errOut != "" || !strings.Contains(out, "laug · report: docs/tables/gates.jsonl: 2 rows; written: "+
		"1 row added (g20261018-092540)\n") || !strings.Contains(out, "laug · report: docs/tables/compare.jsonl: ") {
		t.Fatalf("a run added: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	after := dirFiles(t, dir)
	grew := 0
	for name, old := range before {
		now, ok := after[name]
		if !ok {
			t.Errorf("docs/tables/%s went", name)
			continue
		}
		if name == report.TablesReadme {
			if now != old {
				t.Errorf("docs/tables/README.md changed")
			}
			continue
		}
		lines := strings.SplitAfter(now, "\n")
		for _, l := range strings.SplitAfter(old, "\n") {
			if l != "" && !slices.Contains(lines, l) {
				t.Errorf("docs/tables/%s: a row is not as it was: %s", name, l)
			}
		}
		for _, l := range lines {
			if l != "" && !strings.Contains(old, l) {
				grew++
				if !strings.Contains(l, reportRuns[1]) {
					t.Errorf("docs/tables/%s: a row added that is not of %s: %s", name, reportRuns[1], l)
				}
			}
		}
	}
	if grew == 0 {
		t.Errorf("no row added")
	}
	now := readText(t, evidence)
	if !strings.Contains(now, "| "+reportRuns[1]+" | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |\n") {
		t.Errorf("docs/EVIDENCE.md lacks the added run:\n%s", now)
	}
	for _, l := range strings.Split(was, "\n") {
		if strings.HasPrefix(l, "| g") && !strings.Contains(now, l+"\n") {
			t.Errorf("docs/EVIDENCE.md lost a row: %s", l)
		}
	}

	if err := os.RemoveAll(filepath.Join(res, reportRuns[1])); err != nil {
		t.Fatal(err)
	}
	gates := readText(t, filepath.Join(dir, "gates.jsonl"))
	code, out, errOut = runReportArgs(t, root, "-docs")
	if code != 0 || errOut != "" || !strings.Contains(out, "laug · report: docs/tables/gates.jsonl: 2 rows; the same "+
		"bytes, left as it was; 1 row kept ("+reportRuns[1]+") that the records no longer give\n") ||
		readText(t, filepath.Join(dir, "gates.jsonl")) != gates || readText(t, evidence) != now {
		t.Errorf("a run gone: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}

	changed := strings.Replace(gates, `"verdict":"ADVANCE"`, `"verdict":"BLOCK"`, 1)
	writeFile(t, filepath.Join(dir, "gates.jsonl"), changed)
	if err := os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runReportArgs(t, root, "-docs")
	if code != 1 || out != "" || errOut != "laug · report: docs/tables/gates.jsonl row "+reportRuns[0]+": the "+
		"records give it other bytes; -docs adds rows and never changes one\n"+
		"laug · report: -docs refused: 1 row would change, nothing written; go -C _report run ./cmd/report -tables "+
		"rewrites the tables whole\n" {
		t.Errorf("a changed row: exit %d, want 1\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	if readText(t, filepath.Join(dir, "gates.jsonl")) != changed {
		t.Errorf("a refused -docs wrote docs/tables/gates.jsonl")
	}
	if _, err := os.Stat(evidence); err == nil {
		t.Errorf("a refused -docs wrote docs/EVIDENCE.md")
	}
}

// dirFiles reads every file under dir, by its slash path there.
func dirFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestReportTables drives -tables in a checkout that holds this binary's judge sources and two committed runs: the
// tables written under docs/tables/, the bytes report.Tables builds, one line a table; again, each left as it was;
// -check after a row is changed, a stale rolled file and a file no table writes planted names the row and both files,
// exits 1 and writes nothing; -tables then puts all three right; a record kept false with no file refused; and each
// usage error, exit 2.
func TestReportTables(t *testing.T) {
	root := judgeCheckout(t)
	res := filepath.Join(root, "gate", "results")
	if err := os.MkdirAll(res, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range reportRuns {
		copyRun(t, id, filepath.Join(res, id)) // copies, not links: the records table walks files
	}
	rs, err := report.Load(root, res, rejudge.Checkout{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := report.Tables(rs, os.DirFS(root), judge.Self().SHA256)
	if err != nil {
		t.Fatal(err)
	}
	lines := func(what func(report.Table) string) string {
		var b strings.Builder
		for _, tb := range ts {
			fmt.Fprintf(&b, "laug · report: docs/tables/%s: %s, %s; %s\n", tb.Files[0].Name, plural(tb.Rows, "row"),
				plural(len(tb.Files[0].Data), "byte"), what(tb))
		}
		return b.String()
	}
	says := func(s string) func(report.Table) string { return func(report.Table) string { return s } }
	// added is the written line of a table new on disk: its rows added, up to 3 keys named.
	added := func(tb report.Table) string {
		rows, err := tables.Parse(tb.Files[0].Data)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			return "written: no rows, a new file"
		}
		var keys []string
		for _, r := range rows[:min(3, len(rows))] {
			keys = append(keys, r.Key)
		}
		more := ""
		if len(rows) > 3 {
			more = ", ..."
		}
		return "written: " + plural(len(rows), "row") + " added (" + strings.Join(keys, ", ") + more + ")"
	}
	first := "laug · report: 10 tables from the committed records (2 gate runs in gate/results), "
	counts := "laug · rejudge, 2 runs: 2 identical, 0 known, 0 re-rendered, 0 unexplained, 0 not judged\n"
	code, out, errOut := runReportArgs(t, root, "-tables")
	want := first + "into docs/tables/; Ctrl-C stops it\n" + counts + lines(added) +
		"laug · report: docs/tables/README.md: written\n" +
		"laug · report: 11 of 11 files (10 tables and the README) written into docs/tables/\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("exit %d\nstdout:\n%s\nwant:\n%sstderr:\n%s", code, out, want, errOut)
	}
	dir := filepath.Join(root, "docs", "tables")
	for _, tb := range ts {
		if got := readText(t, filepath.Join(dir, tb.Files[0].Name)); got != string(tb.Files[0].Data) {
			t.Errorf("docs/tables/%s is not what report.Tables builds", tb.Files[0].Name)
		}
	}
	if !strings.Contains(readText(t, filepath.Join(dir, "compare.jsonl")), `{"id":"g20261018-092540/`+
		`persistence_latency_p99_ms","gate":"g20261018-092540","reading":"persistence_latency_p99_ms","pos":1,`) {
		t.Errorf("compare.jsonl lacks the WORSE row of g20261018-092540")
	}

	code, out, errOut = runReportArgs(t, root, "-tables")
	if want := first + "into docs/tables/; Ctrl-C stops it\n" + counts + lines(says("the same bytes, left as it was")) +
		"laug · report: docs/tables/README.md: the same bytes, left as it was\n" +
		"laug · report: 0 of 11 files (10 tables and the README) written into docs/tables/\n"; code != 0 ||
		out != want || errOut != "" {
		t.Errorf("again: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	code, out, errOut = runReportArgs(t, root, "-tables", "-check")
	if want := first + "checked against docs/tables/, nothing written; Ctrl-C stops it\n" + counts +
		lines(says("the same bytes")) + "laug · report: docs/tables/README.md: the same bytes\n" +
		"laug · report: 0 of 11 files (10 tables and the README) differ from a build\n"; code != 0 ||
		out != want || errOut != "" {
		t.Errorf("-check, the same: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}

	gates := filepath.Join(dir, "gates.jsonl")
	was := readText(t, gates)
	changed := strings.Replace(was, `"verdict":"BLOCK"`, `"verdict":"ADVANCE"`, 1)
	writeFile(t, gates, changed)
	stale := filepath.Join(dir, "rejudge-2.jsonl")
	writeFile(t, stale, "")
	stray := filepath.Join(dir, "stray.jsonl")
	writeFile(t, stray, "")
	code, out, errOut = runReportArgs(t, root, "-tables", "-check")
	if code != 1 || errOut != "" || !strings.Contains(out, "gates.jsonl: 2 rows, "+
		plural(len(ts[0].Files[0].Data), "byte")+"; differs: 1 row changed (g20261018-092540)\n") ||
		!strings.Contains(out, "rejudge.jsonl: 2 rows, "+plural(len(ts[4].Files[0].Data), "byte")+
			"; differs: the same rows, laid out in other files; rejudge-2.jsonl would go\n") ||
		!strings.Contains(out, "laug · report: docs/tables/stray.jsonl: no table writes it; would go\n") ||
		!strings.HasSuffix(out, "laug · report: 3 of 12 files (10 tables, the README and 1 file no table writes) "+
			"differ from a build; go -C _report run ./cmd/report -tables writes them\n") {
		t.Errorf("-check, a changed row and a stale file: exit %d, want 1\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	if readText(t, gates) != changed {
		t.Errorf("-check wrote docs/tables/gates.jsonl")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("-check removed rejudge-2.jsonl: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("-check removed stray.jsonl: %v", err)
	}
	code, out, errOut = runReportArgs(t, root, "-tables")
	if code != 0 || errOut != "" || !strings.HasSuffix(out, "laug · report: 3 of 12 files (10 tables, the README "+
		"and 1 file no table writes) written into docs/tables/\n") ||
		!strings.Contains(out, "laug · report: docs/tables/stray.jsonl: no table writes it; removed\n") ||
		!strings.Contains(out, "gates.jsonl: 2 rows, "+plural(len(ts[0].Files[0].Data), "byte")+
			"; written: 1 row changed (g20261018-092540)\n") ||
		!strings.Contains(out, "rejudge.jsonl: 2 rows, "+plural(len(ts[4].Files[0].Data), "byte")+
			"; written: the same rows, laid out in other files; rejudge-2.jsonl removed\n") ||
		readText(t, gates) != was {
		t.Errorf("-tables after -check: exit %d\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("-tables left rejudge-2.jsonl, a file the build no longer gives")
	}
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("-tables left stray.jsonl, a file no table writes")
	}

	// A record kept false whose file is not here has left for the archive: -tables, which writes the build whole,
	// would drop its rows, so it refuses and writes nothing; -check still names what differs.
	records := filepath.Join(dir, "records.jsonl")
	was = readText(t, records)
	gone := `{"id":"gate/results/g20990101-000001/","kept":false}` + "\n"
	writeFile(t, records, was+gone)
	code, out, errOut = runReportArgs(t, root, "-tables")
	if want := "laug · report: -tables: the records table marks 1 record kept false with no file here, " +
		"gate/results/g20990101-000001/ first: a build would drop their rows; nothing written (-tables -check " +
		"names what differs)\n"; code != 2 || out != "" ||
		errOut != want || readText(t, records) != was+gone {
		t.Errorf("-tables, an archived record: exit %d, want 2\nstdout:\n%sstderr:\n%s", code, out, errOut)
	}
	if code, _, _ = runReportArgs(t, root, "-tables", "-check"); code != 1 {
		t.Errorf("-tables -check, an archived record: exit %d, want 1", code)
	}
	writeFile(t, records, was)
}
