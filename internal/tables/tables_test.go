package tables

import (
	"bytes"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// row is a row of key id and one more member, n, given as the JSON text it was recorded in.
func row(t *testing.T, id, n string) jsonx.Value {
	t.Helper()
	v, err := jsonx.Parse([]byte(`{"id":` + strconv.Quote(id) + `,"n":` + n + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestTablesWrite holds the layout: rows sorted by key in bytes, one compact line each, LF, a final newline, numbers
// in the text they were recorded in; an empty table one empty file; Read gives back the same keys and lines.
func TestTablesWrite(t *testing.T) {
	files, err := Write("gates", []jsonx.Value{row(t, "b", "1.50"), row(t, "B", "6.4e-05"), row(t, "a", `[1, 2]`)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"B","n":0.000064}` + "\n" + `{"id":"a","n":[1,2]}` + "\n" + `{"id":"b","n":1.50}` + "\n"
	if len(files) != 1 || files[0].Name != "gates.jsonl" || string(files[0].Data) != want || files[0].Rows != 3 {
		t.Fatalf("got %+v, want gates.jsonl:\n%s", files, want)
	}
	rows, err := Read(fstest.MapFS{"t/gates.jsonl": {Data: files[0].Data}}, "t", "gates")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	if strings.Join(keys, " ") != "B a b" || string(rows[2].Line) != `{"id":"b","n":1.50}` {
		t.Errorf("read back %q, last line %s", keys, rows[2].Line)
	}
	empty, err := Write("rejudge", nil)
	if err != nil || len(empty) != 1 || empty[0].Name != "rejudge.jsonl" || len(empty[0].Data) != 0 {
		t.Errorf("no rows: %+v, %v", empty, err)
	}
	if rows, err := Read(fstest.MapFS{"rejudge.jsonl": {}}, ".", "rejudge"); err != nil || len(rows) != 0 {
		t.Errorf("an empty file: %d rows, %v", len(rows), err)
	}
	if _, err := Read(fstest.MapFS{}, ".", "rejudge"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no file: %v, want fs.ErrNotExist", err)
	}
}

// TestTablesRoll holds the 1 MiB roll: the row that would pass Limit starts <name>-2.jsonl, every file stays at or
// under it, OnDisk lists them in number order (-10 after -2), and Read takes them back as one table in key order.
func TestTablesRoll(t *testing.T) {
	pad := `"` + strings.Repeat("x", 100_000) + `"`
	var rows []jsonx.Value
	for i := range 25 {
		rows = append(rows, row(t, "k"+strconv.Itoa(100+i), pad))
	}
	files, err := Write("gates", rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[1].Name != "gates-2.jsonl" || files[2].Name != "gates-3.jsonl" {
		t.Fatalf("files %d, want 3: gates.jsonl, gates-2.jsonl, gates-3.jsonl", len(files))
	}
	fsys, total := fstest.MapFS{}, 0
	for _, f := range files {
		if len(f.Data) > Limit {
			t.Errorf("%s is %d bytes, past %d", f.Name, len(f.Data), Limit)
		}
		total += f.Rows
		fsys["docs/tables/"+f.Name] = &fstest.MapFile{Data: f.Data}
	}
	if files[0].Rows != 10 || total != 25 {
		t.Errorf("rows %d in the first file, %d in all; want 10 and 25", files[0].Rows, total)
	}
	fsys["docs/tables/gate-items.jsonl"] = &fstest.MapFile{}
	fsys["docs/tables/gates-02.jsonl"] = &fstest.MapFile{}
	got, err := Read(fsys, "docs/tables", "gates")
	if err != nil || len(got) != 25 || got[10].Key != "k110" {
		t.Errorf("read %d rows, %v", len(got), err)
	}
	fsys["docs/tables/gates-10.jsonl"] = &fstest.MapFile{}
	names, err := OnDisk(fsys, "docs/tables", "gates")
	if err != nil || strings.Join(names, " ") != "gates.jsonl gates-2.jsonl gates-3.jsonl gates-10.jsonl" {
		t.Errorf("OnDisk %q, %v", names, err)
	}
	if _, err := Read(fsys, "docs/tables", "gates"); err == nil || !strings.Contains(err.Error(), "gates-4.jsonl is "+
		"missing") {
		t.Errorf("a gap in the files: %v", err)
	}
	if names, err := OnDisk(fsys, "nowhere", "gates"); err != nil || names != nil {
		t.Errorf("a missing dir: %q, %v", names, err)
	}
}

// TestTablesRefuse plants what a table must not hold, each refused and named: Write's duplicate key, a row past the
// size limit, a row that is not an object led by a string id, a bad name; Read's lines out of order or repeated, a
// CR, a missing final LF, an empty line, a line not in the compact form, a file past Limit, and keys out of order
// across two files.
func TestTablesRefuse(t *testing.T) {
	big := `"` + strings.Repeat("x", Limit) + `"`
	for _, c := range []struct {
		name string
		rows []string
		want string
	}{
		{"gates", []string{`{"id":"a"}`, `{"id":"b"}`, `{"id":"a","n":2}`}, "gates: two rows of key a"},
		{"gates", []string{`{"id":"a","n":` + big + `}`}, "gates: row a is 1048594 bytes, past the 1048576"},
		{"gates", []string{`[1]`}, "gates: row 1: a array, not an object"},
		{"gates", []string{`{"key":"a","id":"a"}`}, `row 1: its first member is "key"; a row starts with id`},
		{"gates", []string{`{"id":3}`}, "row 1: its id is a number, not a string"},
		{"gates", []string{`{"id":""}`}, "row 1: its id is empty"},
		{"gates", []string{`{}`}, "row 1: an empty object, with no id"},
		{"Gates", nil, `table name "Gates"`},
		{"../x", nil, `table name "../x"`},
		{"gates-2", nil, `table name "gates-2" ends in - and digits`},
	} {
		var vs []jsonx.Value
		for _, r := range c.rows {
			v, err := jsonx.Parse([]byte(r))
			if err != nil {
				t.Fatal(err)
			}
			vs = append(vs, v)
		}
		files, err := Write(c.name, vs)
		if err == nil || !strings.Contains(err.Error(), c.want) || files != nil {
			t.Errorf("Write %s %.40q: %v, want %q and no file", c.name, c.rows, err, c.want)
		}
	}
	for _, c := range []struct{ data, want string }{
		{`{"id":"b"}` + "\n" + `{"id":"a"}` + "\n", "line 2: key a sorts before b, the line above"},
		{`{"id":"a"}` + "\n" + `{"id":"a"}` + "\n", "line 2: a second row of key a"},
		{`{"id":"a"}` + "\r\n", "line 1: holds a CR"},
		{`{"id":"a"}`, "the last line does not end in LF"},
		{`{"id":"a"}` + "\n\n" + `{"id":"b"}` + "\n", "line 2: empty"},
		{`{"id": "a"}` + "\n", "line 1: row a is not in the compact form"},
		{`{"id":"a","n":1.0e1}` + "\n", "line 1: row a is not in the compact form"},
		{`{"id":"a"} {"id":"b"}` + "\n", "line 1: byte 11: more than one JSON value"},
		{`{"n":1}` + "\n", `line 1: its first member is "n"`},
		{string(bytes.Repeat([]byte{'\n'}, Limit+1)), "1048577 bytes, past the 1048576"},
	} {
		_, err := Read(fstest.MapFS{"t/gates.jsonl": {Data: []byte(c.data)}}, "t", "gates")
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "t/gates.jsonl: ") {
			t.Errorf("Read %.40q: %v, want t/gates.jsonl: ...%s", c.data, err, c.want)
		}
	}
	two := fstest.MapFS{"gates.jsonl": {Data: []byte(`{"id":"b"}` + "\n")},
		"gates-2.jsonl": {Data: []byte(`{"id":"a"}` + "\n")}}
	if _, err := Read(two, ".", "gates"); err == nil || err.Error() != "gates-2.jsonl: line 1: key a does not "+
		"sort after b, the last key of the file before" {
		t.Errorf("keys out of order across files: %v", err)
	}
}
