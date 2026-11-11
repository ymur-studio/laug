package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/tables"
)

// checkSchema holds every row of rows, by table, to its schema's columns, in order.
func checkSchema(t *testing.T, rows map[string]map[string]*jsonx.Object) {
	t.Helper()
	for name, byKey := range rows {
		s, ok := schemaOf(name)
		if !ok {
			t.Errorf("%s: no schema; README.md would not describe it", name)
			continue
		}
		var want []string
		for _, c := range s.columns {
			want = append(want, c.name)
		}
		for key, o := range byKey {
			if got := o.Keys(); !slices.Equal(got, want) {
				t.Errorf("%s %s: columns %q, want %q", name, key, got, want)
				break
			}
		}
	}
}

// builtOnce is every table the committed records give, built once.
var builtOnce = sync.OnceValues(func() ([]Table, error) {
	rs, err := committed()
	if err != nil {
		return nil, err
	}
	return Tables(rs, os.DirFS(root), judge.Self().SHA256)
})

// builtTables is every table the committed records give, built once for the tests that read them; a test must not
// change what it is given.
func builtTables(t *testing.T) []Table {
	t.Helper()
	ts, err := builtOnce()
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// TestTables: a build from the committed records is docs/tables, byte for byte: each table's files and the README,
// and no .jsonl file there that the build does not give. Each row holds its schema's columns in order. Once records
// are archived, the build gives the rows of the records here: laid beside docs/tables as -docs lays it (AddRows), it
// adds no row and changes none, and every table on disk reads and holds its schema.
func TestTables(t *testing.T) {
	ts := builtTables(t)
	if archived(t) {
		added, changes, err := AddRows(os.DirFS(root), ts)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range changes {
			t.Errorf("docs/tables/%s: the records here give it other bytes", c)
		}
		for _, a := range added {
			if len(a.Added) > 0 {
				t.Errorf("docs/tables/%s: the records here give %d rows it lacks, %q first: report -docs adds "+
					"them", a.Name+tables.Ext, len(a.Added), a.Added[0])
			}
		}
		got, err := os.ReadFile(filepath.Join(root, DocsDir, TablesDir, TablesReadme))
		if err != nil || !bytes.Equal(got, TablesReadmeData()) {
			t.Errorf("docs/tables/README.md is not what report -tables writes now (%v)", err)
		}
		checkSchema(t, tableRows(t, diskTables(t)))
		return
	}
	dir := filepath.Join(root, DocsDir, TablesDir)
	built := map[string]bool{TablesReadme: true}
	for _, tb := range ts {
		for _, f := range tb.Files {
			built[f.Name] = true
			got, err := os.ReadFile(filepath.Join(dir, f.Name))
			if err != nil {
				t.Errorf("%v: report -tables writes it", err)
				continue
			}
			if !bytes.Equal(got, f.Data) {
				hint := ""
				if tb.Name == "quotes" {
					hint = " (quotes scans the pages on disk, tracked or not: an untracked .md that cites a record " +
						"by line changes it)"
				}
				t.Errorf("docs/tables/%s is not what report -tables writes now, %s%s", f.Name,
					firstDiff(got, f.Data), hint)
			}
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, TablesReadme))
	if err != nil || !bytes.Equal(got, TablesReadmeData()) {
		t.Errorf("docs/tables/README.md is not what report -tables writes now (%v)", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.IsDir() && !built[e.Name()] {
			t.Errorf("docs/tables/%s: no table writes it now", e.Name())
		}
	}
	checkSchema(t, tableRows(t, ts))
}

// TestTablesReadmePlaceholders: every placeholder of docs/tables/README.md, such as <gate>, sits in a code span. A
// bare one reads as an HTML tag, and GitHub drops it, so "Key: <gate>/<key>." shows as "Key: /.".
func TestTablesReadmePlaceholders(t *testing.T) {
	for i, l := range strings.Split(string(TablesReadmeData()), "\n") {
		parts := strings.Split(l, "`")
		if len(parts)%2 == 0 {
			t.Errorf("README.md:%d: a backtick with no close: %s", i+1, l)
			continue
		}
		for j := 0; j < len(parts); j += 2 {
			if strings.Contains(parts[j], "<") {
				t.Errorf("README.md:%d: a < outside backticks, %q: %s", i+1, parts[j], l)
				break
			}
		}
	}
}

// TestTablesRecords holds the committed records' tables to what the plan names: every BLOCK of a raun or candidate
// has a check, the readings that decided a candidate's, and the deciding window of a raun's that names one; exactly
// the 27 records of the hybrid are kept; every raun and load has a row, and every committed readings.json a table.
func TestTablesRecords(t *testing.T) {
	ts := committedTables(t)
	rows := tableRows(t, ts)
	blocks := map[string]int{}
	for key, o := range rows["rauns"] {
		if cell(o, "result") != `"BLOCK"` {
			if cell(o, "check") != "null" {
				t.Errorf("rauns %s: an ADVANCE with check %s", key, cell(o, "check"))
			}
			continue
		}
		c := cell(o, "check")
		blocks[c]++
		switch c {
		case `"load verdict"`:
			if d := cell(o, "deciding"); d == "[]" || d == "null" {
				t.Errorf("rauns %s: a candidate BLOCK whose deciding is %s", key, d)
			}
		case `"steady"`, `"after window"`:
			w, _ := strconv.Unquote(cell(o, "deciding_window"))
			if r := cell(o, "windows", w); w == "" || r == "null" || r == "{}" {
				t.Errorf("rauns %s: check %s, deciding window %s, its readings %s", key, c,
					cell(o, "deciding_window"), r)
			}
		case `"landed"`, `"recovered"`:
			if w := cell(o, "deciding_window"); w != "null" {
				t.Errorf("rauns %s: check %s, decided by the signal, names deciding window %s", key, c, w)
			}
		case "null":
			t.Errorf("rauns %s: a BLOCK with no check", key)
		}
	}
	// 70 committed BLOCKs: 48 never steady, 7 did not land, 4 after window, 3 recovered (2 past the budget, 1 not
	// within it), 8 candidates. A runbook walk's rauns (raun-rb<N>-*) are ignored and have no row.
	want := map[string]int{`"steady"`: 48, `"landed"`: 7, `"after window"`: 4, `"recovered"`: 3, `"load verdict"`: 8}
	if !maps.Equal(blocks, want) {
		t.Errorf("BLOCKs by check %v, want %v", blocks, want)
	}

	var kept []string
	for key, o := range rows["records"] {
		if cell(o, "kept") == "true" {
			kept = append(kept, key)
		}
	}
	slices.Sort(kept)
	wantKept := []string{
		"cells/results/2026-10-23-boot1.txt", "cells/results/2026-10-23-boot2.txt",
		"cells/results/2026-10-24-batchE.txt", "cells/results/2026-10-24-boot4.txt",
		"cells/results/2026-10-27-boot6.txt", "cells/results/2026-10-29-boot9.txt",
		"cells/results/2026-11-02-frontend1.txt",
		"cells/results/2026-11-04-judge-shell-proof.txt",
	}
	for _, f := range []string{"errors-1", "errors-2", "errors-3", "errors-t010", "errors-t030", "errors-t050",
		"latency-1", "latency-2", "latency-3"} {
		wantKept = append(wantKept, "faults/results/raun-lab2-persistence-"+f+".json")
	}
	for _, n := range []string{"1", "2", "3"} {
		wantKept = append(wantKept, "faults/results/raun-lab6-kill-history-"+n+".json")
	}
	wantKept = append(wantKept, "gate/results/g20261018-075357/", "gate/results/g20261018-091444/",
		"gate/results/g20261018-092540/")
	for _, r := range []string{"1310", "1321"} {
		for _, n := range []string{"1", "2", "3"} {
			wantKept = append(wantKept, "load/results/lab1-v"+r+"-"+n+".*")
		}
	}
	slices.Sort(wantKept)
	if !slices.Equal(kept, wantKept) {
		t.Errorf("kept records %q, want %q", kept, wantKept)
	}

	kinds := map[string]int{}
	for _, o := range rows["records"] {
		kinds[cell(o, "kind")]++
	}
	if kinds[`"raun"`]+kinds[`"candidate"`] != len(rows["rauns"]) || kinds[`"load"`] != len(rows["loads"]) ||
		kinds[`"gate"`] != len(rows["gates"]) {
		t.Errorf("records by kind %v against %d rauns, %d loads, %d gates rows", kinds, len(rows["rauns"]),
			len(rows["loads"]), len(rows["gates"]))
	}
	batches := 0
	for _, tb := range ts {
		if strings.HasPrefix(tb.Name, readingsPrefix) {
			batches++
		}
	}
	if batches != kinds[`"readings"`] || batches == 0 {
		t.Errorf("%d readings tables, %d readings records", batches, kinds[`"readings"`])
	}
	// A quote never comes from a kept record.
	for key := range rows["quotes"] {
		p := key[:strings.LastIndexByte(key, ':')]
		for _, k := range kept {
			if p == k || (strings.HasSuffix(k, "/") && strings.HasPrefix(p, k)) ||
				(strings.HasSuffix(k, ".*") && strings.HasPrefix(p, strings.TrimSuffix(k, "*"))) {
				t.Errorf("quotes %s: a line of kept record %s", key, k)
			}
		}
	}
}

// git runs git in the checkout and gives its stdout.
func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// TestTablesReadCommitted holds the tables' reading of the tree to git's: the files walkResults lists are the
// committed files under resultDirs, but each directory's README.md (its mirror of .gitignore keeps a runbook walk's
// runs, logs and a gate's state out); and the pages the quotes table scans are the committed ones its rule names.
func TestTablesReadCommitted(t *testing.T) {
	files, err := walkResults(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.path)
	}
	var want []string
	for _, p := range strings.Split(strings.TrimSpace(gitOut(t, append([]string{"ls-files", "--"}, resultDirs...)...)), "\n") {
		if p != "" && !slices.ContainsFunc(resultDirs, func(d string) bool { return p == d+"/README.md" }) {
			want = append(want, p)
		}
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("walkResults lists %d files, git ls-files %d; first differing: %s", len(got), len(want),
			firstMissing(got, want))
	}

	pages, err := tablePages(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	probe := fstest.MapFS{}
	for _, p := range strings.Split(strings.TrimSpace(gitOut(t, "ls-files")), "\n") {
		probe[p] = &fstest.MapFile{}
	}
	if _, err := os.Stat(filepath.Join(root, DocsDir, TablesDir, SrcDir, srcHosts)); err == nil {
		probe[DocsDir+"/"+TablesDir+"/"+SrcDir+"/"+srcHosts] = &fstest.MapFile{}
	}
	if b, err := os.ReadFile(filepath.Join(root, privateList)); err == nil {
		probe[privateList] = &fstest.MapFile{Data: b} // the private set, which tablePages leaves out
	}
	for p := range probe {
		if strings.HasPrefix(p, DocsDir+"/"+TablesDir+"/") && path.Base(p) != srcHosts {
			delete(probe, p) // the tables this change adds, untracked until staged
		}
	}
	tracked, err := tablePages(probe)
	if err != nil {
		t.Fatal(err)
	}
	var untracked []string
	for _, p := range pages {
		if _, ok := probe[p]; !ok {
			untracked = append(untracked, p)
		}
	}
	if len(untracked) > 0 || len(pages)-len(untracked) != len(tracked) {
		t.Errorf("pages scanned %d, committed pages %d; not committed: %q", len(pages), len(tracked), untracked)
	}
}

// firstMissing names the first path in one list and not the other.
func firstMissing(a, b []string) string {
	in := func(l []string, s string) bool { _, ok := slices.BinarySearch(l, s); return ok }
	for _, s := range a {
		if !in(b, s) {
			return s + " (walked, not in git)"
		}
	}
	for _, s := range b {
		if !in(a, s) {
			return s + " (in git, not walked)"
		}
	}
	return "none"
}

// TestTablesHostPhrases: the four phrases hosts.tsv may give are docs/RECORDS.md's, word for word.
func TestTablesHostPhrases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root, "docs", "RECORDS.md"))
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(string(data)), " ")
	for _, h := range hostPhrases {
		if !strings.Contains(flat, `"`+h.phrase+`"`) {
			t.Errorf("docs/RECORDS.md does not give the phrase %q", h.phrase)
		}
		if !strings.Contains(h.phrase, h.token) {
			t.Errorf("phrase %q does not hold its token %q", h.phrase, h.token)
		}
	}
}

// TestTablesFindCites reads the by-line cites as pages write them, one inside a row cite marked self.
func TestTablesFindCites(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"`cells/results/a.txt`:11-12, :89-101", []string{"cells/results/a.txt 11 12", "cells/results/a.txt 89 101"}},
		{"(`cells/results/a.txt:369`, :399).", []string{"cells/results/a.txt 369 369", "cells/results/a.txt 399 399"}},
		{"`cells/results/a.txt`, :207", []string{"cells/results/a.txt 207 207"}},
		{"`cells/results/a.txt`:83, :90 and :95", []string{"cells/results/a.txt 83 83", "cells/results/a.txt 90 90",
			"cells/results/a.txt 95 95"}},
		{"gate/results/g1/verdict.md:16 then faults/results/x.json:3", []string{"gate/results/g1/verdict.md 16 16",
			"faults/results/x.json 3 3"}},
		{"`cells/results/b/a4-health-{1,2,3}.txt:4`", nil},
		{"`cells/results/a.txt` names it", nil},
		{"cells/results/a.txt: 12:00 UTC", nil},
		{"docs/tables/quotes.jsonl#cells/results/a.txt:490, then `cells/results/a.txt`:12", []string{
			"cells/results/a.txt 490 490 self", "cells/results/a.txt 12 12"}},
	} {
		var got []string
		for _, ci := range findCites(c.text) {
			g := ci.path + " " + itoa(ci.from) + " " + itoa(ci.to)
			if ci.self {
				g += " self"
			}
			got = append(got, g)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("findCites(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// recordsFixture is a checkout of the smallest records: a raun BLOCK never steady, a candidate BLOCK, a load with
// its verdict and baseline, a candidate's load copy, a batch with a readings.json and its transcript, an ignored
// walk's raun and a log, the three hand inputs and a page that cites a line.
func recordsFixture() fstest.MapFS {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	src := DocsDir + "/" + TablesDir + "/" + SrcDir + "/"
	return fstest.MapFS{
		"faults/results/README.md":   f("# not a record\n"),
		"faults/results/raun-x.json": f(`{"run_id":"x","fault":"f","result":"BLOCK","reason":"never steady: a=1","before":{"a":1}}`),
		"faults/results/candidate-c-1.json": f(`{"candidate":"c","run_id":"lab3-1","verdict":"BLOCK",` +
			`"deciding":["service_error_rate"],"load":{"duration":"5m","rate":"20","max_concurrent":"50","window_s":301}}`),
		"faults/results/load/candidate-c-1.json":       f(`{"run_id":"candidate-c-1","rate":"20"}`),
		"faults/results/raun-rb2-20990101-000000.json": f(`{"result":"BLOCK","reason":"odd"}`),
		"faults/results/raun-y.json": f(`{"run_id":"y","result":"BLOCK","reason":"did not land: signal s stayed ` +
			`at 0 over 120s","signal_peak":null,"during":{}}`),
		"faults/results/raun-x.load.log": f("a log\n"),
		"load/results/l-1.json":          f(`{"run_id":"l-1","server_tag":"1.31.0","omes_exit":0}`),
		"load/results/l-1.verdict.json": f(`{"run_id":"l-1","verdict":"BLOCK","deciding":["p99"],` +
			`"readings":{"p99":12.50},"rows":[{"metric":"p99","value":12.5,"limit":"10","result":"over","blocks":true},` +
			`{"metric":"omes_exit","value":0,"limit":"0","result":"ok","blocks":false}]}`),
		"load/results/l-1.baseline.json": f(`{"p99":12.50}`),
		"cells/results/b.txt":            f("laug · b\nHost: the lab host, an m8i.8xlarge (32 vCPU, 128 GiB).\nline 3\n"),
		"cells/results/b/readings.json": f(`[{"boot":1,"step":"S1","name":"t","value":3.10,"unit":"s"},` +
			`{"boot":1,"step":"S1","name":"t","value":{"a":[1,2]},"unit":"s"},` +
			`{"boot":1,"step":"S2","name":"o","value":{"b":1},"unit":"","error":"x"}]`),
		"cells/results/b/steps/S1.txt": f("one\ntwo\nthree\n"),
		src + srcKept:                  f("# kept\nload/results/l-1.*\tA load that BLOCKed.\n"),
		src + srcReadings:              f("b S2/o\n"),
		src + srcHosts:                 f("l-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/b.txt:2\n"),
		"docs/page.md":                 f("See `cells/results/b/steps/S1.txt`:2-3 and `load/results/l-1.json:1`.\n"),
		"docs/.hidden/x.md":            f("`cells/results/b.txt:99`\n"),
		"gate/sets/s.txt":              f("# cells/results/b.txt:3, the run\n"),
		"internal/x/testdata/y.md":     f("`cells/results/b.txt:99`\n"),
	}
}

// TestTablesArchived builds the fixture's record tables with records archived: the records table on disk marks
// cells/results/z.txt and the batch z kept false, and neither is here. A page's by-line cite of z.txt takes its
// lines from the quotes rows on disk, and fails on a line no row holds; a row cite or a spec's row line of z.txt
// builds no row, since a quote never cites itself; readings.txt's line of batch z is passed over; hosts.tsv's glob
// matches a raun of the rauns table on disk and its cite reads a quotes row. A cite of a record no table names still
// fails.
func TestTablesArchived(t *testing.T) {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	dir := DocsDir + "/" + TablesDir + "/"
	src := dir + SrcDir + "/"
	fsys := recordsFixture()
	fsys[dir+"records.jsonl"] = f(`{"id":"cells/results/z.txt","kept":false}` + "\n" +
		`{"id":"cells/results/z/readings.json","kept":false}` + "\n")
	fsys[dir+"quotes.jsonl"] = f(`{"id":"cells/results/z.txt:4","text":"four"}` + "\n" +
		`{"id":"cells/results/z.txt:5","text":"the lab host, an m8i.8xlarge"}` + "\n" +
		`{"id":"cells/results/z.txt:9","text":"nine"}` + "\n")
	fsys[dir+"rauns.jsonl"] = f(`{"id":"raun-zz-1"}` + "\n")
	fsys[src+srcReadings] = f("b S2/o\nz S1/x\n")
	fsys[src+srcHosts] = f("l-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/b.txt:2\n" +
		"raun-zz-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/z.txt:5\n")
	fsys["docs/z.md"] = f("`cells/results/z.txt`:4-5, and docs/tables/quotes.jsonl#cells/results/z.txt:9.\n")
	fsys[specDir+"/z.txt"] = f("row quotes cells/results/z.txt:9\n")
	ts, err := RecordTables(fsys)
	if err != nil {
		t.Fatal(err)
	}
	rows := tableRows(t, ts)
	var got []string
	for k, o := range rows["quotes"] {
		got = append(got, k+" "+cell(o, "text"))
	}
	slices.Sort(got)
	want := []string{"cells/results/b.txt:2 \"Host: the lab host, an m8i.8xlarge (32 vCPU, 128 GiB).\"",
		"cells/results/b.txt:3 \"line 3\"", "cells/results/b/steps/S1.txt:2 \"two\"",
		"cells/results/b/steps/S1.txt:3 \"three\"", "cells/results/z.txt:4 \"four\"",
		"cells/results/z.txt:5 \"the lab host, an m8i.8xlarge\""}
	if !slices.Equal(got, want) {
		t.Errorf("quotes %q, want %q", got, want)
	}
	fsys["docs/z.md"] = f("`cells/results/z.txt`:6\n")
	if _, err := RecordTables(fsys); err == nil || !strings.Contains(err.Error(),
		"cells/results/z.txt is archived, and docs/tables/quotes.jsonl has no row cells/results/z.txt:6") {
		t.Errorf("a line no quotes row holds: %v", err)
	}
	fsys["docs/z.md"] = f("`cells/results/y.txt`:1\n")
	if _, err := RecordTables(fsys); err == nil ||
		!strings.Contains(err.Error(), "cells/results/y.txt is not a result file") {
		t.Errorf("a record no table names: %v", err)
	}
	delete(fsys, "docs/z.md")
	fsys[src+srcReadings] = f("b S2/o\ny S1/x\n")
	if _, err := RecordTables(fsys); err == nil || !strings.Contains(err.Error(), "no cells/results/y/readings.json") {
		t.Errorf("readings.txt names a batch no table names: %v", err)
	}
}

// TestTablesRecordsColumns builds the fixture's tables and holds their rows and columns.
func TestTablesRecordsColumns(t *testing.T) {
	ts, err := RecordTables(recordsFixture())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range ts {
		names = append(names, tb.Name)
	}
	if want := []string{"rauns", "loads", "load-readings", "readings-b", "records", "quotes"}; !slices.Equal(names, want) {
		t.Fatalf("tables %q, want %q", names, want)
	}
	rows := tableRows(t, ts)
	checkSchema(t, rows)
	for _, c := range []struct {
		table, key string
		col        []string
		want       string
	}{
		{"rauns", "raun-x", []string{"check"}, `"steady"`},
		{"rauns", "raun-x", []string{"deciding_window"}, `"before"`},
		{"rauns", "raun-x", []string{"kind"}, `"raun"`},
		{"rauns", "raun-y", []string{"check"}, `"landed"`},
		{"rauns", "raun-y", []string{"deciding_window"}, `null`},
		{"rauns", "raun-x", []string{"windows"}, `{"before":{"a":1},"during":null,"drain":null,"after":null}`},
		{"rauns", "candidate-c-1", []string{"run_id"}, `"lab3-1"`},
		{"rauns", "candidate-c-1", []string{"kind"}, `"candidate"`},
		{"rauns", "candidate-c-1", []string{"check"}, `"load verdict"`},
		{"rauns", "candidate-c-1", []string{"load"}, `{"duration":"5m","rate":"20","max_concurrent":"50","window_s":301}`},
		{"loads", "l-1", []string{"record"}, `"load/results/l-1.*"`},
		{"loads", "l-1", []string{"files"}, `["l-1.baseline.json","l-1.json","l-1.verdict.json"]`},
		{"loads", "l-1", []string{"verdict"}, `"BLOCK"`},
		{"loads", "l-1", []string{"deciding"}, `["p99"]`},
		{"loads", "faults/results/load/candidate-c-1", []string{"verdict_sha256"}, `null`},
		{"loads", "faults/results/load/candidate-c-1", []string{"rate"}, `"20"`},
		{"load-readings", "l-1/p99", []string{"value"}, `12.50`},
		{"load-readings", "l-1/p99", []string{"blocks"}, `true`},
		{"load-readings", "l-1/p99", []string{"deciding"}, `true`},
		{"load-readings", "l-1/p99", []string{"baseline"}, `12.50`},
		{"load-readings", "l-1/omes_exit", []string{"pos"}, `1`},
		{"load-readings", "l-1/omes_exit", []string{"baseline"}, `null`},
		{"readings-b", "S1/t", []string{"value"}, `3.10`},
		{"readings-b", "S1/t#2", []string{"whole"}, `false`},
		{"readings-b", "S1/t#2", []string{"value", "leaves"}, `2`},
		{"readings-b", "S1/t#2", []string{"value", "bytes"}, `11`},
		{"readings-b", "S2/o", []string{"whole"}, `true`},
		{"readings-b", "S2/o", []string{"value"}, `{"b":1}`},
		{"readings-b", "S2/o", []string{"error"}, `"x"`},
		{"readings-b", "S1/t", []string{"error"}, `null`},
		{"records", "load/results/l-1.*", []string{"kept"}, `true`},
		{"records", "load/results/l-1.*", []string{"files"}, `3`},
		{"records", "cells/results/b/", []string{"kind"}, `"run directory"`},
		{"records", "cells/results/b/", []string{"files"}, `2`},
		{"records", "cells/results/b/readings.json", []string{"kind"}, `"readings"`},
		{"records", "cells/results/b.txt", []string{"kept"}, `false`},
		{"quotes", "cells/results/b/steps/S1.txt:2", []string{"text"}, `"two"`},
		{"quotes", "cells/results/b/steps/S1.txt:3", []string{"text"}, `"three"`},
		{"quotes", "cells/results/b.txt:2", []string{"text"}, `"Host: the lab host, an m8i.8xlarge (32 vCPU, 128 GiB)."`},
		{"quotes", "cells/results/b.txt:3", []string{"text"}, `"line 3"`},
	} {
		o, ok := rows[c.table][c.key]
		if !ok {
			t.Errorf("%s: no row %s", c.table, c.key)
			continue
		}
		if got := cell(o, c.col...); got != c.want {
			t.Errorf("%s %s %s: %s, want %s", c.table, c.key, strings.Join(c.col, "."), got, c.want)
		}
	}
	if n := len(rows["quotes"]); n != 4 {
		t.Errorf("%d quotes, want 4: S1.txt:2-3, hosts.tsv's b.txt:2 and the gate set's b.txt:3; the kept load's "+
			"line is not quoted", n)
	}
	if _, ok := rows["rauns"]["raun-rb2-20990101-000000"]; ok {
		t.Errorf("a runbook walk's raun, ignored by git, has a row")
	}
	sum := listSum([]resultFile{{"a", 1, "x"}, {"b", 2, "y"}})
	if want := sha256Hex("x  a\ny  b\n"); sum != want {
		t.Errorf("listSum = %s, want sha256sum's list %s", sum, want)
	}
	again, _ := RecordTables(recordsFixture())
	for i := range ts {
		if !bytes.Equal(ts[i].Files[0].Data, again[i].Files[0].Data) {
			t.Errorf("%s: two builds differ", ts[i].Name)
		}
	}
}

// TestTablesRecordsRefuse plants each thing RecordTables refuses and holds the error that names it.
func TestTablesRecordsRefuse(t *testing.T) {
	src := DocsDir + "/" + TablesDir + "/" + SrcDir + "/"
	for _, c := range []struct {
		path, data, want string
	}{
		{"faults/results/raun-x.json", `{"result":"BLOCK","reason":"odd"}`, "a raun BLOCK whose reason no faults/run.sh check names"},
		{"faults/results/candidate-c-1.json", `{"candidate":"c","verdict":"BLOCK","deciding":[]}`, "a candidate BLOCK that no reading decided"},
		{"faults/results/notes.txt", "x", "faults/results/notes.txt: not a record laug writes"},
		{src + srcKept, "load/results/l-2.*\tgone\n", "kept.txt:1: load/results/l-2.* is no record here"},
		{src + srcKept, "load/results/l-1.*\n", "kept.txt:1: a record's path, a TAB, then what it shows"},
		{src + srcReadings, "b S1/t\n", "readings.txt:1: b S1/t is a number, always kept whole"},
		{src + srcReadings, "b S9/x\n", "readings.txt:1: b has no reading S9/x"},
		{src + srcReadings, "z S1/t\n", "readings.txt:1: no cells/results/z/readings.json"},
		{src + srcHosts, "l-*\tthe lab host\tcells/results/b.txt:2\n", "hosts.tsv:1: \"the lab host\" is not one of docs/RECORDS.md's four host phrases"},
		{src + srcHosts, "q-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/b.txt:2\n", "glob q-* matches no raun or load"},
		{src + srcHosts, "l-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/b.txt:3\n", "cells/results/b.txt:3-3 does not say m8i.8xlarge"},
		{src + srcHosts, "l-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/b.txt:9\n", "cells/results/b.txt:9: past its 3 lines"},
		{src + srcHosts, "l-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/b.txt:2\n" +
			"l-1\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\tcells/results/b.txt:2\n", "hosts.tsv:2: loads l-1 falls under line 1 too"},
		{"docs/page.md", "`cells/results/b/steps/S9.txt:1`\n", "docs/page.md:1 cites cells/results/b/steps/S9.txt is not a result file under"},
		{"gate/sets/s.txt", "# cells/results/b.txt:4\n", "gate/sets/s.txt:1 cites cells/results/b.txt:4: past its 3 lines"},
		{"load/results/l-1.verdict.json", `{"run_id":`, "load/results/l-1.verdict.json: "},
		{"load/results/l-1.verdict.json", "", "load/results/l-1.verdict.json: "},
		{"docs/page.md", "`cells/results/b/steps/S1.txt`:4\n", "docs/page.md:1 cites cells/results/b/steps/S1.txt:4: past its 3 lines"},
	} {
		fsys := recordsFixture()
		fsys[c.path] = &fstest.MapFile{Data: []byte(c.data)}
		if _, err := RecordTables(fsys); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %q: %v, want an error naming %q", c.path, c.data, err, c.want)
		}
	}
	// No hand input at all: no kept record, no value whole but scalars, no host checked.
	fsys := recordsFixture()
	for p := range fsys {
		if strings.HasPrefix(p, src) {
			delete(fsys, p)
		}
	}
	if _, err := RecordTables(fsys); err != nil {
		t.Errorf("without hand inputs: %v", err)
	}
	// An empty baseline, as lab1-smoke60's, reads as null.
	fsys = recordsFixture()
	fsys["load/results/l-1.baseline.json"] = &fstest.MapFile{}
	ts, err := RecordTables(fsys)
	if err != nil {
		t.Fatalf("an empty baseline: %v", err)
	}
	if got := cell(tableRows(t, ts)["load-readings"]["l-1/p99"], "baseline"); got != "null" {
		t.Errorf("an empty baseline: l-1/p99's baseline %s, want null", got)
	}
	if _, err := tables.Write("readings-B", nil); err == nil {
		t.Errorf("tables.Write takes an uppercase name, so ReadingsTable need not lowercase")
	}
	if got := ReadingsTable("2026-10-24-batchE"); got != "readings-2026-10-24-batche" {
		t.Errorf("ReadingsTable = %s", got)
	}
}

// sha256Hex is the sha256 of s in hex.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestTablePagesPrivate: the quotes table scans no page of the private set check/private.txt names, a path or a
// directory, and no *.local.md; without the list every page is scanned.
func TestTablePagesPrivate(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md": {}, "PLAN.md": {}, "notes.local.md": {}, "docs/a.md": {}, "docs/inner/b.md": {},
		"gate/sets/s.txt": {},
	}
	got, err := tablePages(fsys)
	if want := []string{"PLAN.md", "README.md", "docs/a.md", "docs/inner/b.md", "gate/sets/s.txt"}; err != nil ||
		!slices.Equal(got, want) {
		t.Errorf("no list: %q, %v; want %q", got, err, want)
	}
	fsys[privateList] = &fstest.MapFile{Data: []byte("# the private set\nPLAN.md\n\ndocs/inner/\n")}
	got, err = tablePages(fsys)
	if want := []string{"README.md", "docs/a.md", "gate/sets/s.txt"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("with the list: %q, %v; want %q", got, err, want)
	}
}
