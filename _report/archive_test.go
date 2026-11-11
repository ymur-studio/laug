package report

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ymur-studio/laug/internal/tables"
)

// labRuns is internal/rejudge's copy of the lab host runs the tests name that are not kept in gate/results
// (internal/rejudge/testdata/lab/README.md).
var labRuns = filepath.Join(root, "internal", "rejudge", "testdata", "lab")

// runDir is the directory of the gate run id a test names: its copy under labRuns when there is one, else
// gate/results'.
func runDir(id string) string {
	if fi, err := os.Stat(filepath.Join(labRuns, id)); err == nil && fi.IsDir() {
		return filepath.Join(labRuns, id)
	}
	return filepath.Join(results, id)
}

// linkRuns links each run of ids into dir, from runDir.
func linkRuns(t *testing.T, dir string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		src, err := filepath.Abs(runDir(id))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(src, filepath.Join(dir, id)); err != nil {
			t.Fatal(err)
		}
	}
}

// publicCheckout is a temporary checkout as it is published: a link to each top-level entry of this one, but for the
// four directories of resultDirs, whose results directories hold a link to each file of a kept record
// (docs/tables/src/kept.txt, by the records table) and to their README.md alone; gate/results holds no run, for the
// caller to link its own. Its path.
func publicCheckout(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	top := t.TempDir()
	link := func(src, dst string) {
		if err := os.Symlink(src, dst); err != nil {
			t.Fatal(err)
		}
	}
	parents := map[string]bool{}
	for _, d := range resultDirs {
		parents[strings.Split(d, "/")[0]] = true
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !parents[e.Name()] {
			link(filepath.Join(abs, e.Name()), filepath.Join(top, e.Name()))
			continue
		}
		subs, err := os.ReadDir(filepath.Join(abs, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, sub := range subs {
			if sub.Name() == "results" {
				continue
			}
			if err := os.MkdirAll(filepath.Join(top, e.Name()), 0o755); err != nil {
				t.Fatal(err)
			}
			link(filepath.Join(abs, e.Name(), sub.Name()), filepath.Join(top, e.Name(), sub.Name()))
		}
	}
	rows, err := tables.Read(os.DirFS(abs), DocsDir+"/"+TablesDir, "records")
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, d := range resultDirs {
		if err := os.MkdirAll(filepath.Join(top, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
		keep = append(keep, d+"/README.md")
	}
	for _, r := range rows {
		if k, _ := at(r.Value, "kept").Bool(); !k || strings.HasPrefix(r.Key, "gate/results/") {
			continue
		}
		if strings.HasSuffix(r.Key, "/") {
			t.Fatalf("records %s: a kept directory; publicCheckout links files", r.Key)
		}
		m, err := fs.Glob(os.DirFS(abs), r.Key)
		if err != nil || len(m) == 0 {
			t.Fatalf("records %s: kept, and no file here (%v)", r.Key, err)
		}
		keep = append(keep, m...)
	}
	for _, p := range keep {
		link(filepath.Join(abs, filepath.FromSlash(p)), filepath.Join(top, filepath.FromSlash(p)))
	}
	return top
}

// archivedOnce is the records the records table on disk marks kept false whose files are not here: those that left
// the tree for the private archive.
var archivedOnce = sync.OnceValues(func() ([]string, error) { return Archived(os.DirFS(root)) })

// archived is whether any record has left the tree: the tests that build the tables from the records then hold the
// build to the tables on disk where it gives a row, and read the rest from disk.
func archived(t *testing.T) bool {
	t.Helper()
	a, err := archivedOnce()
	if err != nil {
		t.Fatal(err)
	}
	return len(a) > 0
}

// needRecords skips t when a file of ps, paths from the checkout's root, is not here and is of an archived record:
// a test that reads such a file holds a reader the private rebuild runs (report -tables on the archive), and
// the rows that reader built are what the pages read. A path that is not here and of no archived record fails.
func needRecords(t *testing.T, ps ...string) {
	t.Helper()
	a, err := readArchive(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if !a.has(p) {
			t.Fatalf("%s: not here, and no records row kept false names it", p)
		}
		t.Skipf("%s is archived (docs/tables/records.jsonl, kept false): its rows hold what this reader read", p)
	}
}

// diskTables are the tables under docs/tables as files, each read by tables.Read: what the pages read.
func diskTables(t *testing.T) []Table {
	t.Helper()
	dir := filepath.Join(root, DocsDir, TablesDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), tables.Ext)
		if !ok || e.IsDir() {
			continue
		}
		if i := strings.LastIndex(name, "-"); i > 0 && strings.Trim(name[i+1:], "0123456789") == "" {
			name = name[:i] // a rolled file, <name>-2.jsonl
		}
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	var out []Table
	for _, name := range names {
		rows, err := tables.Read(os.DirFS(root), DocsDir+"/"+TablesDir, name)
		if err != nil {
			t.Fatal(err)
		}
		files, err := tables.OnDisk(os.DirFS(root), DocsDir+"/"+TablesDir, name)
		if err != nil {
			t.Fatal(err)
		}
		tb := Table{Name: name, Rows: len(rows)}
		for _, f := range files {
			data, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				t.Fatal(err)
			}
			tb.Files = append(tb.Files, tables.File{Name: f, Data: data})
		}
		out = append(out, tb)
	}
	return out
}

// committedTables are the tables the tests read for what the records hold: the build from the records while every
// record is here (TestTables holds it to docs/tables byte for byte), else docs/tables itself.
func committedTables(t *testing.T) []Table {
	t.Helper()
	if archived(t) {
		return diskTables(t)
	}
	return builtTables(t)
}
