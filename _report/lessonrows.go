package report

// lessonrows.go is how a lesson's data figure reads the summary tables under docs/tables: a table whole, by
// tables.Read, its rows by key, and the files it read, which the figure's desc names as its sources in place of the
// records the rows were cut from. A figure reads the tables the checkout holds, as the pages' counts do (RowsOf), so it
// is drawn the same when its records leave gate/results, faults/results and load/results and only their rows remain.
// A row a figure needs that its table lacks is an error naming the table and the key.

import (
	"cmp"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/tables"
)

// tablesPath is the summary tables' directory from the checkout's root.
const tablesPath = DocsDir + "/" + TablesDir

// A figTable is one summary table as a figure reads it: its rows in key order, by key, and its files from the
// checkout's root, as the figure names them.
type figTable struct {
	name  string
	files []string
	rows  []tables.Row
	byKey map[string]jsonx.Value
}

// readFigTable reads table name from fsys's docs/tables. A table with no file is an error that names its first file.
func readFigTable(fsys fs.FS, name string) (figTable, error) {
	rows, err := tables.Read(fsys, tablesPath, name)
	if err != nil {
		return figTable{}, err
	}
	files, err := tables.OnDisk(fsys, tablesPath, name)
	if err != nil {
		return figTable{}, err
	}
	t := figTable{name: name, rows: rows, byKey: make(map[string]jsonx.Value, len(rows))}
	for _, f := range files {
		t.files = append(t.files, tablesPath+"/"+f)
	}
	for _, r := range rows {
		t.byKey[r.Key] = r.Value
	}
	return t, nil
}

// cite is a row's cite, docs/tables/<name>.jsonl#<key>, as an error names it.
func (t figTable) cite(key string) string { return tablesPath + "/" + t.name + tables.Ext + "#" + key }

// row is the row of key, an error naming it when the table has none.
func (t figTable) row(key string) (jsonx.Value, error) {
	v, ok := t.byKey[key]
	if !ok {
		return jsonx.Null(), fmt.Errorf("%s: no such row", t.cite(key))
	}
	return v, nil
}

// under is the rows whose key starts <parent>/, in their pos column's order: a gate's compare rows, an item's readings.
func (t figTable) under(parent string) []jsonx.Value {
	var out []jsonx.Value
	for _, r := range t.rows {
		if strings.HasPrefix(r.Key, parent+"/") {
			out = append(out, r.Value)
		}
	}
	slices.SortStableFunc(out, func(a, b jsonx.Value) int { return cmp.Compare(rowPos(a), rowPos(b)) })
	return out
}

// rowsCompared is what gate id's gates row and compare rows hold of its comparison, as the run's verdict.json records
// it: the release, the incumbent, the verdict, the mode and R7's rows in their order. A gate with no comparison, or
// a mode and no compare rows, is an error.
func rowsCompared(gates, compare figTable, id string) (verdictCmp, error) {
	g, err := gates.row(id)
	if err != nil {
		return verdictCmp{}, err
	}
	c := verdictCmp{path: gates.cite(id), rowsPath: compare.cite(id) + "/", server: str(at(g, "server_tag")), incumbent: str(at(g, "incumbent")),
		verdict: str(at(g, "verdict")), mode: str(at(g, "comparison", "mode"))}
	if c.mode == "" {
		return verdictCmp{}, fmt.Errorf("%s: no comparison", c.path)
	}
	rows := compare.under(id)
	if len(rows) == 0 {
		return verdictCmp{}, fmt.Errorf("%s: no rows, and the gates row's comparison is %s", c.rowsPath, c.mode)
	}
	for _, row := range rows {
		judged, _ := at(row, "judged").Bool()
		c.rows = append(c.rows, Row{Name: str(at(row, "reading")), Scale: str(at(row, "scale")),
			Release: num(at(row, "release")), Incumbent: num(at(row, "incumbent")), D: num(at(row, "d")),
			Tolerance: num(at(row, "tolerance")), Judged: judged, Result: str(at(row, "result"))})
	}
	return c, nil
}

// tableSources are the files of ts, as a figure names them in its sources.
func tableSources(ts ...figTable) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.files...)
	}
	return out
}

// readFigTables reads each table of names, in that order.
func readFigTables(fsys fs.FS, names ...string) ([]figTable, error) {
	var out []figTable
	for _, n := range names {
		t, err := readFigTable(fsys, n)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// values are the value column of the rows under prefix, in their keys' order, which is their record's: a figure
// table's lines or values of one record (valueRow).
func (t figTable) values(prefix string) []jsonx.Value {
	var out []jsonx.Value
	for _, r := range t.under(prefix) {
		out = append(out, at(r, "value"))
	}
	return out
}

// text is the rows under prefix as their record's text: each row's value, a line of the record a figure table kept
// (lineRows), joined in their keys' order, which is the record's. A figure reads it as it read the record. A prefix
// with no rows is an error naming it.
func (t figTable) text(prefix string) (string, error) {
	vs := t.values(prefix)
	if len(vs) == 0 {
		return "", fmt.Errorf("%s: no rows", t.cite(prefix+"/"))
	}
	lines := make([]string, len(vs))
	for i, v := range vs {
		lines[i] = str(v)
	}
	return strings.Join(lines, "\n"), nil
}

// reading is the value of the one row of readings table t named name, which must be kept whole, and the row's cite.
func (t figTable) reading(name string) (jsonx.Value, string, error) {
	var found []tables.Row
	for _, r := range t.rows {
		if str(at(r.Value, "name")) == name {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		return jsonx.Null(), "", fmt.Errorf("%s: %d rows of reading %s, want 1", t.files[0], len(found), name)
	}
	c := t.cite(found[0].Key)
	if whole, _ := at(found[0].Value, "whole").Bool(); !whole {
		return jsonx.Null(), "", fmt.Errorf("%s: not kept whole; src/readings.txt does not name it", c)
	}
	return at(found[0].Value, "value"), c, nil
}

// labHostWords are a figure's words for the lab host, docs/RECORDS.md's phrase less its size.
const labHostWords = "the lab host, an m8i.8xlarge"

// batchOnLabHost refuses a batch, by its transcript, unless src/hosts.tsv has a line that cites it and every line
// that cites it names the lab host: a batch whose figure draws no raun or load of its own is placed by the lines of
// its runs, which report -tables checks against the transcript line each cites.
func batchOnLabHost(fsys fs.FS, transcript string) error {
	lines, err := srcLines(fsys, hostsTSV)
	if err != nil {
		return err
	}
	n := 0
	for _, l := range lines {
		f := strings.Split(l.text, "\t")
		if len(f) != 3 || !strings.HasPrefix(f[2], transcript+":") {
			continue
		}
		if f[1] != labHostPhrase {
			return fmt.Errorf("%s:%d: %s ran on %s, not %s", hostsTSV, l.n, transcript, f[1], labHostPhrase)
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("%s: no line cites %s", hostsTSV, transcript)
	}
	return nil
}
