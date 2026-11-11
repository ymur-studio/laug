// Package tables reads and writes laug's summary tables, docs/tables/<name>.jsonl: JSON Lines, one row a line, each
// row an object whose first member, "id", is its key. Rows are sorted by key in bytes and keys are unique; lines end
// in LF, the last one too; no header. A row is written as `jq -c` writes it (jsonx.AppendCompact), so a number keeps
// the text it was recorded in and two writes of the same rows are the same bytes. A file stays at or under Limit:
// the row that would pass it starts <name>-2.jsonl, then <name>-3.jsonl, and a reader reads them in that order as one
// table. The standard library and internal/jsonx only.
package tables

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Limit is the most bytes one file of a table holds, 1 MiB.
const Limit = 1 << 20

// Ext is a table file's extension.
const Ext = ".jsonl"

// A Row is one row of a table: its key, its line as written, without the LF, and the line read back.
type Row struct {
	Key   string
	Line  []byte
	Value jsonx.Value
}

// A File is one file of a table: its name, <name>.jsonl or <name>-<n>.jsonl, its bytes and its count of rows.
type File struct {
	Name string
	Data []byte
	Rows int
}

// FileName is the name of a table's n-th file, n from 1: <name>.jsonl, then <name>-2.jsonl and on.
func FileName(name string, n int) string {
	if n <= 1 {
		return name + Ext
	}
	return name + "-" + strconv.Itoa(n) + Ext
}

// Write lays out the rows of table name as its files. Each row is an object whose first member is "id", a string
// that is not empty; the rows go out sorted by it in bytes, one compact line each. A table of no rows is one empty
// file. Write refuses, writing nothing: a name that is not one, a row that is not such an object, two rows of one
// key, and a row whose line alone passes Limit.
func Write(name string, rows []jsonx.Value) ([]File, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for i, v := range rows {
		key, err := keyOf(v)
		if err != nil {
			return nil, fmt.Errorf("%s: row %d: %w", name, i+1, err)
		}
		line := jsonx.AppendCompact(nil, v)
		if len(line)+1 > Limit {
			return nil, fmt.Errorf("%s: row %s is %d bytes, past the %d a file holds", name, key, len(line)+1, Limit)
		}
		out = append(out, Row{Key: key, Line: line, Value: v})
	}
	slices.SortStableFunc(out, func(a, b Row) int { return strings.Compare(a.Key, b.Key) })
	for i := 1; i < len(out); i++ {
		if out[i].Key == out[i-1].Key {
			return nil, fmt.Errorf("%s: two rows of key %s", name, out[i].Key)
		}
	}
	files := []File{{Name: FileName(name, 1)}}
	for _, r := range out {
		f := &files[len(files)-1]
		if len(f.Data)+len(r.Line)+1 > Limit {
			files = append(files, File{Name: FileName(name, len(files)+1)})
			f = &files[len(files)-1]
		}
		f.Data = append(append(f.Data, r.Line...), '\n')
		f.Rows++
	}
	return files, nil
}

// OnDisk lists the files of table name in dir of fsys, in the order a reader reads them: <name>.jsonl, then
// <name>-2.jsonl and on, each number as the writer names it. A missing dir lists none.
func OnDisk(fsys fs.FS, dir, name string) ([]string, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	ents, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type numbered struct {
		n    int
		name string
	}
	var got []numbered
	for _, e := range ents {
		n, ok := fileNumber(name, e.Name())
		if ok {
			got = append(got, numbered{n, e.Name()})
		}
	}
	slices.SortFunc(got, func(a, b numbered) int { return a.n - b.n })
	names := make([]string, len(got))
	for i, g := range got {
		names[i] = g.name
	}
	return names, nil
}

// fileNumber is the number of file in table name's files: 1 for <name>.jsonl, n for <name>-<n>.jsonl, n from 2 with
// no leading zero.
func fileNumber(name, file string) (int, bool) {
	if file == name+Ext {
		return 1, true
	}
	mid, ok := strings.CutPrefix(file, name+"-")
	if !ok {
		return 0, false
	}
	digits, ok := strings.CutSuffix(mid, Ext)
	if !ok || digits == "" || digits[0] == '0' || strings.Trim(digits, "0123456789") != "" || len(digits) > 6 {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil && n >= 2
}

// Read reads table name from dir of fsys, its files in OnDisk's order, as one list of rows. It refuses what Write
// would not have written: files numbered with a gap, a file past Limit, one that does not end in LF, an empty line or
// a CR, a line that is not one object with a string "id" first, a line not in the compact form Write gives it, and
// keys out of byte order or repeated, across files too. A table with no file reads as no rows and fs.ErrNotExist.
func Read(fsys fs.FS, dir, name string) ([]Row, error) {
	files, err := OnDisk(fsys, dir, name)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: %w", path.Join(dir, FileName(name, 1)), fs.ErrNotExist)
	}
	var rows []Row
	for i, f := range files {
		p := path.Join(dir, f)
		if want := FileName(name, i+1); f != want {
			return nil, fmt.Errorf("%s: %s is missing, so the table's files have a gap", p, want)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		got, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if len(rows) > 0 && len(got) > 0 && got[0].Key <= rows[len(rows)-1].Key {
			return nil, fmt.Errorf("%s: line 1: key %s does not sort after %s, the last key of the file before",
				p, got[0].Key, rows[len(rows)-1].Key)
		}
		rows = append(rows, got...)
	}
	return rows, nil
}

// Parse reads the bytes of one table file, held as Read holds each.
func Parse(data []byte) ([]Row, error) {
	if len(data) > Limit {
		return nil, fmt.Errorf("%d bytes, past the %d a file holds", len(data), Limit)
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, errors.New("the last line does not end in LF")
	}
	var rows []Row
	for n, line := range bytes.Split(data[:len(data)-1], []byte("\n")) {
		at := fmt.Sprintf("line %d", n+1)
		if len(line) == 0 {
			return nil, fmt.Errorf("%s: empty", at)
		}
		if bytes.IndexByte(line, '\r') >= 0 {
			return nil, fmt.Errorf("%s: holds a CR; lines end in LF alone", at)
		}
		v, err := jsonx.Parse(line)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		key, err := keyOf(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		if !bytes.Equal(jsonx.AppendCompact(nil, v), line) {
			return nil, fmt.Errorf("%s: row %s is not in the compact form jq -c writes", at, key)
		}
		if len(rows) > 0 {
			switch last := rows[len(rows)-1].Key; {
			case key == last:
				return nil, fmt.Errorf("%s: a second row of key %s", at, key)
			case key < last:
				return nil, fmt.Errorf("%s: key %s sorts before %s, the line above", at, key, last)
			}
		}
		rows = append(rows, Row{Key: key, Line: line, Value: v})
	}
	return rows, nil
}

// keyOf is v's key: the string of its first member, "id".
func keyOf(v jsonx.Value) (string, error) {
	o, ok := v.Obj()
	if !ok {
		return "", fmt.Errorf("a %s, not an object", v.Type())
	}
	if o.Len() == 0 {
		return "", errors.New("an empty object, with no id")
	}
	k, id := o.Member(0)
	s, ok := id.Str()
	switch {
	case k != "id":
		return "", fmt.Errorf("its first member is %q; a row starts with id", k)
	case !ok:
		return "", fmt.Errorf("its id is a %s, not a string", id.Type())
	case s == "":
		return "", errors.New("its id is empty")
	}
	return s, nil
}

// checkName refuses a table name that cannot name a file in docs/tables: empty, holding a byte that is not a
// lowercase letter, a digit, - or _, starting with -, or ending in - and digits, which would read as another table's
// second file (gates-2 as the second file of gates).
func checkName(name string) error {
	if name == "" || name[0] == '-' || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return fmt.Errorf("table name %q: lowercase letters, digits, - and _ only, not first a -", name)
	}
	if i := strings.LastIndexByte(name, '-'); i >= 0 && strings.Trim(name[i+1:], "0123456789") == "" {
		return fmt.Errorf("table name %q ends in - and digits, the name of another table's file", name)
	}
	return nil
}
