package report

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// b2Files is a checkout of boot 2's raun label alone: its JSON and its step file, with edit applied to the step file.
func b2Files(t *testing.T, label string, edit func(step string) string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	for _, p := range []string{b2JSON(label), b2Step(label)} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if p == b2Step(label) {
			data = []byte(edit(string(data)))
		}
		fsys[p] = &fstest.MapFile{Data: data}
	}
	return fsys
}

// TestReadB2: a raun reads its hold and budget from its step file, and is refused when the step file is another
// raun's, or names a hold its JSON's remove does not keep.
func TestReadB2(t *testing.T) {
	needRecords(t, b2JSON("throttle-frontend-1"))
	same := func(s string) string { return s }
	r, err := readB2(b2Files(t, "throttle-frontend-1", same), "throttle-frontend-1")
	if err != nil {
		t.Fatal(err)
	}
	if r.hold != 120 || r.budget != 180 || r.catalog != "" || !r.landed.OK || r.landed.F != 35 || r.remove.F != 121 ||
		r.wait != 1 || r.done[1].F != 181 || r.secs[1].F != 85 {
		t.Errorf("read %+v", r)
	}
	for _, c := range []struct {
		edit func(string) string
		want string
	}{
		{func(s string) string { return strings.Replace(s, "hold 120s", "hold 110s", 1) }, "hold of 110 s"},
		{func(s string) string {
			return strings.Replace(s, "-run-id lab2-throttle-frontend-1", "-run-id lab2-x", 1)
		},
			"the first line"},
		{func(s string) string { return strings.Replace(s, "budget 180s", "budget s", 1) }, "want one line"},
	} {
		if _, err := readB2(b2Files(t, "throttle-frontend-1", c.edit), "throttle-frontend-1"); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
	// A raun from a catalog copy names it, and its inject is what the batch log read back from that copy.
	fsys := b2Files(t, "persistence-errors-t030", same)
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(boot2Log)))
	if err != nil {
		t.Fatal(err)
	}
	fsys[boot2Log] = &fstest.MapFile{Data: data}
	r, err = readB2(fsys, "persistence-errors-t030")
	if err != nil {
		t.Fatal(err)
	}
	in, err := injectOf(fsys, string(data), boot2Log, r)
	if r.catalog != "<out>/catalog-pe-0.3.yaml" || err != nil || in != "toxic add reset_peer timeout=0 toxicity=0.3" {
		t.Errorf("catalog %q, inject %q, %v", r.catalog, in, err)
	}
}

// TestCatalogField reads a key of one row only, past comments and the other rows.
func TestCatalogField(t *testing.T) {
	text := "defaults:\n  inject: none\nfaults:\n  a:\n    # inject: in a comment\n    inject: one\n  b:\n" +
		"    budget_s: 60\n"
	for _, c := range []struct{ fault, key, want string }{
		{"a", "inject", "one"}, {"b", "budget_s", "60"}, {"b", "inject", ""}, {"c", "inject", ""},
	} {
		got, err := catalogField(text, c.fault, c.key)
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("%s %s: %q, %v, want %q", c.fault, c.key, got, err, c.want)
		}
	}
}

// TestReadSamples keeps the reads from the inject to the hold's end, and refuses one there that did not answer, or a
// read of another cell.
func TestReadSamples(t *testing.T) {
	r := b2Raun{cell: "c1", inject: 1000, hold: 30}
	line := func(at, cell, errs string) string {
		return `{"at":"` + at + `","cell":"` + cell + `","redials":[{"m":{"service_name":"history"},"v":2}],` +
			`"redial_failures":[],"errors_per_s":` + errs + `,"completions_per_s":[{"m":{},"v":20}],` +
			`"pg_connections":40}` + "\n"
	}
	// 1000 s after the epoch is 00:16:40 UTC.
	ok := line("1970-01-01T00:16:25Z", "c1", "null") + line("1970-01-01T00:16:40Z", "c1", `[{"m":{},"v":0}]`) +
		line("1970-01-01T00:16:55Z", "c1", `[{"m":{},"v":1.5}]`) + line("1970-01-01T00:17:10Z", "c1", "null")
	ss, err := readSamples(fstest.MapFS{"s.jsonl": {Data: []byte(ok)}}, "s.jsonl", r)
	if err != nil || len(ss) != 2 || ss[0].t != 0 || ss[1].t != 15 || ss[1].errors != 1.5 {
		t.Fatalf("%+v, %v", ss, err)
	}
	if p, role, n, seen := peakRole(ss, func(s samplerRead) []roleVal { return s.redials }); p != 2 ||
		role != "history" || n != 1 || !seen {
		t.Errorf("redials peak %v by %q of %d roles, seen %v", p, role, n, seen)
	}
	if _, _, _, seen := peakRole(ss, func(s samplerRead) []roleVal { return s.failures }); seen {
		t.Error("a peak where no read held the series")
	}
	for _, c := range []struct{ data, want string }{
		{line("1970-01-01T00:16:40Z", "c1", "null") + line("1970-01-01T00:16:55Z", "c1", "null"),
			"did not answer"},
		{line("1970-01-01T00:16:40Z", "c2", `[{"m":{},"v":0}]`), `of cell "c2"`},
		{line("1970-01-01T00:16:40Z", "c1", `[{"m":{},"v":0}]`), "1 reads in the hold"},
	} {
		if _, err := readSamples(fstest.MapFS{"s.jsonl": {Data: []byte(c.data)}}, "s.jsonl", r); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
}

// swapFS is a checkout with the file at path read as data.
type swapFS struct {
	fs.FS
	path string
	data []byte
}

func (f swapFS) Open(name string) (fs.File, error) {
	if name == f.path {
		return fstest.MapFS{name: {Data: f.data}}.Open(name)
	}
	return f.FS.Open(name)
}

// TestBoot2Rows: each of boot 2's figures reads each raun from its rauns row and its figure table's rows as it read
// the raun's files, and, since it prints its words for the host, refuses a raun src/hosts.tsv places on another.
func TestBoot2Rows(t *testing.T) {
	needRecords(t, b2JSON(stuckRauns[0]))
	ts, err := readFigTables(os.DirFS(root), "rauns", stuckTable, toxicTable)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		table  figTable
		labels []string
	}{{ts[1], stuckRauns}, {ts[2], toxicRauns}} {
		rs, err := rowsB2(os.DirFS(root), ts[0], c.table, c.labels)
		if err != nil {
			t.Fatal(err)
		}
		for i, label := range c.labels {
			f, err := readB2(os.DirFS(root), label)
			if err != nil {
				t.Fatal(err)
			}
			r := rs[i]
			if !jsonx.Equal(at(r.selfTest, "passed"), at(f.selfTest, "passed")) {
				t.Errorf("%s: self-test passed %v from rows, %v from files", label, at(r.selfTest, "passed"),
					at(f.selfTest, "passed"))
			}
			// v is the JSON each was read from and win where in it the windows are, files the files or rows,
			// and selfTest whole only from the files: the rest is the raun, which must be the same.
			r.v, f.v, r.win, f.win, r.files, f.files = jsonx.Value{}, jsonx.Value{}, nil, nil, nil, nil
			r.selfTest, f.selfTest = jsonx.Value{}, jsonx.Value{}
			// A row holds each stamp as its seconds after the inject (timeofday.go), so its inject is 0; a stamp
			// it lacks reads as no number from either, whatever F it carries.
			r.inject, f.inject = 0, 0
			for _, m := range []*maybe{&r.landed, &r.remove, &r.recovered, &r.afterStart, &r.afterEnd,
				&f.landed, &f.remove, &f.recovered, &f.afterStart, &f.afterEnd} {
				if !m.OK {
					m.F = 0
				}
			}
			if !reflect.DeepEqual(r, f) {
				t.Errorf("%s: rows read %+v, files %+v", label, r, f)
			}
		}
	}
	line := "raun-lab2-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)"
	fsys := editFS(t, hostsTSV, func(s string) string {
		return strings.Replace(s, line, "raun-lab2-*\tthe dev host, a Linux VM with 24 vCPU and 47 GiB running Docker", 1)
	})
	_, _, panelErr := toxicitiesPanel(fsys, "")
	_, throttleErr := throttleLanding(fsys)
	_, stuckErr := stuckShard(fsys)
	for name, err := range map[string]error{"toxicities": panelErr, "throttle-landing": throttleErr,
		"stuck-shard": stuckErr} {
		if err == nil || !strings.Contains(err.Error(), "not the lab host") {
			t.Errorf("%s with raun-lab2-* on the dev host: %v", name, err)
		}
	}
}
