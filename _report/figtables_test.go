package report

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// TestFigureTables: the figure tables cut from the committed records are the ones docs/tables holds (TestTables holds
// their bytes), a checkout without their records builds none, and each refuses a record that does not hold its points
// as its figure reads them.
func TestFigureTables(t *testing.T) {
	ts, err := FigureTables(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fig-adopted 333", "fig-bucket-edges 20", "fig-cancel-path 306", "fig-clock-step-timers 19",
		"fig-kill-history 154", "fig-restore-timing 171", "fig-staleness 5", "fig-stuck-shard 9",
		"fig-throttle-landing 6", "fig-toxic-fuse 63"}
	var got, disk []string
	for _, tb := range ts {
		got = append(got, fmt.Sprintf("%s %d", tb.Name, tb.Rows))
	}
	for _, tb := range diskTables(t) {
		if strings.HasPrefix(tb.Name, "fig-") {
			disk = append(disk, fmt.Sprintf("%s %d", tb.Name, tb.Rows))
		}
	}
	if !slices.Equal(disk, want) {
		t.Errorf("docs/tables: tables and rows %q, want %q", disk, want)
	}
	if archived(t) {
		// The records here give the rows of none: TestTables holds what they give to docs/tables.
		t.Logf("records archived: the build gives %q", got)
		needRecords(t, edgesRecord, killHistoryBins[0], b2JSON(throttleRauns[0]))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tables and rows %q, want %q", got, want)
	}
	if none, err := FigureTables(fstest.MapFS{"README.md": {}}); err != nil || len(none) != 0 {
		t.Errorf("a checkout without the records: %d tables, %v", len(none), err)
	}
	line := func(s string) string { return strings.Split(s, "\n")[edgesLine-1] }
	for _, c := range []struct {
		name string
		edit func(string) string
		want string
	}{
		{"a histogram's list unlike act's", func(s string) string {
			l := line(s)
			return strings.Replace(s, l, strings.Replace(l, "|e2e 0.001,", "|e2e 0.003,", 1), 1)
		}, "e2e's le strings are not act's"},
		{"no +Inf", func(s string) string {
			l := line(s)
			return strings.Replace(s, l, strings.ReplaceAll(l, ",1000,+Inf", ",1000"), 1)
		}, `"1000" at 19; +Inf is the last le`},
		{"an edge out of order", func(s string) string {
			l := line(s)
			return strings.Replace(s, l, strings.ReplaceAll(l, "0.002,0.005", "0.005,0.002"), 1)
		}, `"0.002" is not an edge over the one before it`},
		{"another line", func(s string) string {
			l := line(s)
			return strings.Replace(s, l, "", 1)
		}, "not the le strings of the 5 histograms"},
	} {
		_, err := FigureTables(editFS(t, edgesRecord, c.edit))
		if err == nil || !strings.Contains(err.Error(), "fig-bucket-edges: "+edgesRecord+":530: ") ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	bins := fstest.MapFS{}
	for _, p := range append(slices.Clone(killHistoryRauns), killHistoryBins[0]) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		bins[p] = &fstest.MapFile{Data: data}
	}
	if _, _, err := killHistoryRows(bins); err == nil || !strings.Contains(err.Error(), killHistoryBins[1]) {
		t.Errorf("one bins file of three: %v, want it to name %s", err, killHistoryBins[1])
	}
	if _, _, err := b2Rows(bins, throttleRauns, false); err != nil {
		t.Errorf("none of boot 2's rauns: %v, want no table and no error", err)
	}
	some := fstest.MapFS{}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(b2JSON(throttleRauns[0]))))
	if err != nil {
		t.Fatal(err)
	}
	some[b2JSON(throttleRauns[0])] = &fstest.MapFile{Data: data}
	if _, ok, err := b2Rows(some, throttleRauns, false); ok || err == nil ||
		!strings.Contains(err.Error(), b2Step(throttleRauns[0])+": missing") {
		t.Errorf("one file of boot 2's rauns: %v, want it to name %s", err, b2Step(throttleRauns[0]))
	}
}

// hybridFS is the checkout as the public tree will hold it: every file under a results directory that src/kept.txt
// does not name is gone.
type hybridFS struct {
	fs.FS
	kept func(string) bool
}

func (h hybridFS) Open(name string) (fs.File, error) {
	for _, d := range []string{"gate/results/", "faults/results/", "load/results/", "cells/results/"} {
		if strings.HasPrefix(name, d) && !strings.HasSuffix(name, "README.md") && !h.kept(name) {
			if fi, err := fs.Stat(h.FS, name); err == nil && !fi.IsDir() {
				return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
			}
		}
	}
	return h.FS.Open(name)
}

// TestAllOrNone: a table judges presence on the records that leave the public tree, so a checkout with only the
// kept records builds none, and one with a leaving record but not a kept one is an error.
func TestAllOrNone(t *testing.T) {
	kept := DocsDir + "/" + TablesDir + "/" + SrcDir + "/" + srcKept
	fsys := fstest.MapFS{kept: {Data: []byte("# kept\nr/a.json\tA.\nr/d/\tD.\n")}}
	ps := []string{"r/a.json", "r/d/x.txt", "r/b.txt", "r/c.txt"}
	put := func(ps ...string) fstest.MapFS {
		m := maps.Clone(fsys)
		for _, p := range ps {
			m[p] = &fstest.MapFile{}
		}
		return m
	}
	for _, c := range []struct {
		have []string
		ok   bool
		err  string
	}{
		{nil, false, ""},
		{[]string{"r/a.json", "r/d/x.txt"}, false, ""},
		{[]string{"r/a.json", "r/d/x.txt", "r/b.txt", "r/c.txt"}, true, ""},
		{[]string{"r/a.json", "r/b.txt"}, false, "r/c.txt: missing, while 1 other"},
		{[]string{"r/d/x.txt", "r/b.txt", "r/c.txt"}, false, "r/a.json: missing, a kept record"},
	} {
		ok, err := allOrNone(put(c.have...), ps)
		if ok != c.ok || (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("with %q: %v, %v; want %v, %q", c.have, ok, err, c.ok, c.err)
		}
	}
	root := os.DirFS(root)
	k, err := keptPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := FigureTables(hybridFS{root, k})
	if err != nil {
		t.Fatalf("the kept records alone: %v, want no table and no error", err)
	}
	for _, tb := range ts {
		t.Errorf("the kept records alone built %s", tb.Name)
	}
}

// TestFits: a record whose lines or places outgrow its rows' fixed-width keys is refused, since the keys would sort
// out of place.
func TestFits(t *testing.T) {
	if err := fits("r", 9999, 4); err != nil {
		t.Errorf("9999 in four digits: %v", err)
	}
	if err := fits("r", 10000, 4); err == nil || !strings.Contains(err.Error(), "past the 4 digits") {
		t.Errorf("10000 in four digits: %v, want refused", err)
	}
	long := fstest.MapFS{"r.txt": {Data: []byte(strings.Repeat("x\n", 9999))}}
	if _, err := lineRows(long, "r.txt", "r", matching(true)); err != nil {
		t.Errorf("9999 lines: %v", err)
	}
	long["r.txt"] = &fstest.MapFile{Data: []byte(strings.Repeat("x\n", 10000))}
	if _, err := lineRows(long, "r.txt", "r", matching(true)); err == nil {
		t.Error("10000 lines: no error, want refused")
	}
}
