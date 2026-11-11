package report

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/tables"
)

// docsTables are the tables as report -docs writes them now: the build from the committed records laid beside
// docs/tables (AddRows), with no row that would change.
func docsTables(t *testing.T) []Table {
	t.Helper()
	added, changes, err := AddRows(os.DirFS(root), builtTables(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		t.Errorf("docs/tables/%s: the records give it other bytes; report -docs refuses it", c)
	}
	var ts []Table
	for _, a := range added {
		ts = append(ts, a.Table)
	}
	return ts
}

// TestDocs holds the committed documents to the renderer: docs/EVIDENCE.md and each figure under docs/img are the
// bytes report -docs writes now from gate/results, every run judged again in process by this tree's own judge,
// EVIDENCE.md drawn from the tables -docs writes; each table under docs/tables is what -docs writes, no row added
// or changed (a row on disk the build does not give is kept, as -docs keeps it); and docs/img holds no SVG that no
// figure writes. make docs writes them again.
func TestDocs(t *testing.T) {
	ts := docsTables(t)
	files, err := Docs(records(t), ts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(root, DocsDir, filepath.FromSlash(f.Path))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%v: make docs writes it", err)
			continue
		}
		if !bytes.Equal(got, f.Data) {
			t.Errorf("docs/%s is not what report -docs writes now (make docs writes it again), %s", f.Path,
				firstDiff(got, f.Data))
		}
	}
	for _, tb := range ts {
		for _, f := range tb.Files {
			got, err := os.ReadFile(filepath.Join(root, DocsDir, TablesDir, f.Name))
			if err != nil || !bytes.Equal(got, f.Data) {
				t.Errorf("docs/%s/%s is not what report -docs writes now (%v)", TablesDir, f.Name, err)
			}
		}
	}
	stale, err := Stale(os.DirFS(root), files)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range stale {
		t.Errorf("%s: no figure writes it now; git rm it, or add the figure back", p)
	}
}

// firstDiff names the first line where a committed copy and a render differ.
func firstDiff(committed, rendered []byte) string {
	c, r := strings.Split(string(committed), "\n"), strings.Split(string(rendered), "\n")
	for i := range min(len(c), len(r)) {
		if c[i] != r[i] {
			return "first at line " + strconv.Itoa(i+1) + ":\ncommitted " + c[i] + "\nrendered  " + r[i]
		}
	}
	return "the committed copy " + strconv.Itoa(len(c)) + " lines, the render " + strconv.Itoa(len(r))
}

// TestEvidence reads the ledger the committed runs give: a row a run, in gate id order, each with the host as
// recorded, the judge version gate.json names, the verdict and the deciding rule; the counts by verdict; this
// judge's identity; the runs the rejudge counts and the gate records a clone holds, those docs/tables/src/kept.txt
// names; and the rejudge's counts line last, as laug gate rejudge -all ends. Two renders are the same bytes, and
// nothing in it names a time, a host or a path outside the checkout.
func TestEvidence(t *testing.T) {
	rs := records(t)
	ts := builtTables(t)
	files, err := Docs(rs, ts)
	if err != nil || len(files) != 2+len(lessons)+len(pageFigures) || files[0].Path != Evidence {
		t.Fatalf("Docs: %v, %d files", err, len(files))
	}
	again, err := Docs(rs, ts)
	if err != nil || !bytes.Equal(again[0].Data, files[0].Data) {
		t.Fatalf("two renders differ: %v", err)
	}
	page := string(files[0].Data)
	var rows []string
	advance, block := 0, 0
	for _, r := range rs.Runs {
		v := r.Judge.Name
		if v == "" {
			v = "not named"
		}
		rows = append(rows, "| "+r.ID+" | "+mdCell(hostLabel(r.Host))+" | "+v+" | "+r.Verdict+" | "+
			mdCell(r.Deciding())+" |")
		switch r.Verdict {
		case "ADVANCE":
			advance++
		case "BLOCK":
			block++
		}
	}
	var got []string
	for _, l := range strings.Split(page, "\n") {
		if strings.HasPrefix(l, "| g") {
			got = append(got, l)
		}
	}
	if !slices.Equal(got, rows) {
		t.Errorf("%d rows, want %d, one a run in gate id order", len(got), len(rows))
	}
	id := judge.Self()
	judged := 0
	for _, n := range rs.Counts {
		judged += n
	}
	keptTxt, err := os.ReadFile(filepath.Join(root, DocsDir, TablesDir, SrcDir, srcKept))
	if err != nil {
		t.Fatal(err)
	}
	kept := 0
	for _, l := range strings.Split(string(keptTxt), "\n") {
		if strings.HasPrefix(l, "gate/results/") {
			kept++
		}
	}
	for _, want := range []string{
		"\n" + plural(len(rs.Runs), "gate run", "gate runs") + ": " + strconv.Itoa(advance) + " ADVANCE, " +
			strconv.Itoa(block) + " BLOCK.\n",
		"version " + id.Version + " for a new run, sources'\nsha256 " + id.SHA256 + ". The sources are " +
			"internal/judge/versions.txt\nand the .go files in internal/judge and internal/jsonx that are not tests.",
		" picks its judge by the version. The " + strconv.Itoa(judged) + " are the lab tree's, kept outside\nthis " +
			"one; a clone holds the " + plural(kept, "run", "runs") + " kept in `gate/results/` " +
			"(`gate/results/README.md`).\n\n```text\n",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if last := "laug · rejudge, " + rs.Counts.String() + "\n```\n"; !strings.HasSuffix(page, last) {
		t.Errorf("the page does not end in %q", last)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page, "\r") || strings.Contains(page, abs) {
		t.Errorf("a CR, or the checkout's absolute path")
	}
	for i, l := range strings.Split(page, "\n") {
		if !strings.HasPrefix(l, "|") && len([]rune(l)) > 120 {
			t.Errorf("line %d is %d columns: only a table row passes 120", i+1, len([]rune(l)))
		}
	}

	notRun := &Records{Runs: rs.Runs, NotRun: "laug was built from abc1234, and HEAD is def5678"}
	if _, err := Docs(notRun, ts); err == nil || !strings.Contains(err.Error(), "the rejudge did not run: laug was "+
		"built from abc1234") {
		t.Errorf("Docs with the rejudge not run: %v", err)
	}
}

// TestMdCell: recorded text in a table cell stays text: a pipe, a backtick, emphasis, a link and a tag are escaped,
// a newline is a space, and plain words pass as they are.
func TestMdCell(t *testing.T) {
	for in, want := range map[string]string{
		"refused: lease steady: laugd refused it (curl exit 22)": "refused: lease steady: laugd refused it (curl exit 22)",
		"a | b":          `a \| b`,
		"`x` *y* [z](w)": "\\`x\\` \\*y\\* \\[z\\](w)",
		"<b>\nc\\d":      `\<b> c\\d`,
		"R2_steady":      "R2_steady",
	} {
		if got := mdCell(in); got != want {
			t.Errorf("mdCell(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDocFigures: Docs writes EVIDENCE.md, then each lesson's figure as img/<name>.svg in the lessons' order, then each
// page's figure in pageFigures' order, then img/README.md; from records whose checkout holds no docs/LEARNING.md, or
// that name no checkout, EVIDENCE.md alone. Stale names each SVG under docs/img that Docs does not write, and nothing
// else there.
func TestDocFigures(t *testing.T) {
	rs := records(t)
	ts := builtTables(t)
	files, err := Docs(rs, ts)
	if err != nil || len(files) != 2+len(lessons)+len(pageFigures) || files[0].Path != Evidence ||
		files[len(files)-1].Path != "img/README.md" {
		t.Fatalf("Docs: %v, %d files", err, len(files))
	}
	var names []string
	for _, l := range lessons {
		names = append(names, l.name)
	}
	for _, p := range pageFigures {
		names = append(names, p.name)
	}
	for i, name := range names {
		if f := files[1+i]; f.Path != "img/"+name+".svg" || !bytes.HasPrefix(f.Data, []byte("<svg ")) {
			t.Errorf("file %d is %s, want img/%s.svg", 1+i, f.Path, name)
		}
	}
	for _, c := range []fs.FS{fstest.MapFS{"docs/EVIDENCE.md": {}}, nil} {
		bare := *rs
		bare.checkout = c
		got, err := Docs(&bare, ts)
		if err != nil || len(got) != 1 || got[0].Path != Evidence {
			t.Errorf("Docs with no docs/LEARNING.md: %v, %d files", err, len(got))
		}
	}
	fsys := fstest.MapFS{
		"docs/img/kill-history.svg": {Data: files[1].Data},
		"docs/img/retired.svg":      {Data: []byte("<svg/>\n")},
		"docs/img/README.md":        {Data: []byte("# Figures\n")},
		"docs/img/warm-check.png":   {Data: []byte("png")},
	}
	if stale, err := Stale(fsys, files); err != nil || !slices.Equal(stale, []string{"docs/img/retired.svg"}) {
		t.Errorf("Stale: %q, %v", stale, err)
	}
	if stale, err := Stale(fstest.MapFS{"docs/EVIDENCE.md": {}}, files); err != nil || stale != nil {
		t.Errorf("Stale with no docs/img: %q, %v", stale, err)
	}
}

// TestAddRows lays a build beside the tables on disk as -docs does: a row the disk lacks is added, a row on disk the
// build does not give is kept, a row of the same bytes left, and a row of other bytes named as a change; a table
// with no file on disk has every row added, and one on disk that does not read as a table is an error.
func TestAddRows(t *testing.T) {
	write := func(name string, lines ...string) Table {
		var rows []jsonx.Value
		for _, l := range lines {
			v, err := jsonx.Parse([]byte(l))
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, v)
		}
		files, err := tables.Write(name, rows)
		if err != nil {
			t.Fatal(err)
		}
		return Table{Name: name, Files: files, Rows: len(rows)}
	}
	dir := DocsDir + "/" + TablesDir + "/"
	disk := fstest.MapFS{
		dir + "gates.jsonl": {Data: []byte(`{"id":"a","v":1}` + "\n" + `{"id":"c","v":3}` + "\n")},
	}
	b := []Table{write("gates", `{"id":"a","v":1}`, `{"id":"b","v":2}`), write("rejudge", `{"id":"x"}`)}
	got, changes, err := AddRows(disk, b)
	if err != nil || changes != nil || len(got) != 2 {
		t.Fatalf("AddRows: %v, %v, %d tables", err, changes, len(got))
	}
	if g := got[0]; g.Name != "gates" || g.Rows != 3 || !slices.Equal(g.Added, []string{"b"}) ||
		!slices.Equal(g.Kept, []string{"c"}) || string(g.Files[0].Data) != `{"id":"a","v":1}`+"\n"+
		`{"id":"b","v":2}`+"\n"+`{"id":"c","v":3}`+"\n" {
		t.Errorf("gates: %+v", g)
	}
	if r := got[1]; r.Rows != 1 || !slices.Equal(r.Added, []string{"x"}) || r.Kept != nil {
		t.Errorf("rejudge, no file on disk: %+v", r)
	}
	b[0] = write("gates", `{"id":"a","v":9}`, `{"id":"c","v":3}`)
	if _, changes, err := AddRows(disk, b); err != nil || len(changes) != 1 ||
		changes[0].String() != "gates.jsonl row a" {
		t.Errorf("a changed row: %v, %v", changes, err)
	}
	disk[dir+"gates.jsonl"] = &fstest.MapFile{Data: []byte(`{"id":"a"}`)}
	if _, _, err := AddRows(disk, b); err == nil || !strings.Contains(err.Error(), "does not end in LF") {
		t.Errorf("a table on disk that does not read: %v", err)
	}
}

// TestEvidenceRefuses: EVIDENCE.md is drawn from rows, so tables with no gates row, or no rejudge row of this judge,
// write none.
func TestEvidenceRefuses(t *testing.T) {
	ts := builtTables(t)
	id := judge.Self()
	var noRejudge, noGates []Table
	for _, tb := range ts {
		if tb.Name != "rejudge" {
			noRejudge = append(noRejudge, tb)
		}
		if tb.Name != "gates" {
			noGates = append(noGates, tb)
		}
	}
	if _, err := evidence(noGates, id); err == nil || !strings.Contains(err.Error(), "no gates row") {
		t.Errorf("no gates table: %v", err)
	}
	if _, err := evidence(noRejudge, id); err == nil || !strings.Contains(err.Error(), "no rejudge row") {
		t.Errorf("no rejudge table: %v", err)
	}
	other := id
	other.SHA256 = strings.Repeat("0", 64)
	if _, err := evidence(ts, other); err == nil || !strings.Contains(err.Error(), "no rejudge row") {
		t.Errorf("rejudge rows of another judge only: %v", err)
	}
}
