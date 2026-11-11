package report

// docstables.go is how report -docs writes the summary tables: it adds rows and never changes or removes one.
// A build from the committed records (Tables) is laid beside the tables on disk under docs/tables/: a row the build
// gives that the disk lacks is added; a row on disk the build does not give, a record that left gate/results or a
// rejudge row of an earlier judge, is kept as it is; a row the build gives with other bytes than the disk's is
// refused, by table and key, and nothing is written. report -tables is the one that rewrites a table whole.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/tables"
)

// A RowsAdded is one table as -docs writes it: the rows on disk and the rows of the build the disk lacks, with the
// keys of each added row and of each row on disk the build does not give, kept, both in byte order.
type RowsAdded struct {
	Table
	Added, Kept []string
}

// A RowChange is a row the build gives with other bytes than the row of its key on disk: -docs refuses it.
type RowChange struct {
	Table, Key string
}

func (c RowChange) String() string { return c.Table + tables.Ext + " row " + c.Key }

// AddRows lays each table of built beside its files under checkout's docs/tables/: each table as -docs writes it, in
// built's order, and the rows that would change, which -docs refuses. A table with no file on disk has every row
// added. An error is a table on disk that does not read as one (tables.Read), or one the rows together cannot be
// written as (tables.Write).
func AddRows(checkout fs.FS, built []Table) ([]RowsAdded, []RowChange, error) {
	dir := DocsDir + "/" + TablesDir
	var out []RowsAdded
	var changes []RowChange
	for _, t := range built {
		disk, err := tables.Read(checkout, dir, t.Name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		was := make(map[string][]byte, len(disk))
		for _, r := range disk {
			was[r.Key] = r.Line
		}
		now := map[string]bool{}
		a := RowsAdded{}
		var rows []jsonx.Value
		for _, r := range disk {
			rows = append(rows, r.Value)
		}
		for _, f := range t.Files {
			got, err := tables.Parse(f.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("%s as built: %w", f.Name, err)
			}
			for _, r := range got {
				now[r.Key] = true
				line, ok := was[r.Key]
				switch {
				case !ok:
					a.Added = append(a.Added, r.Key)
					rows = append(rows, r.Value)
				case !bytes.Equal(line, r.Line):
					changes = append(changes, RowChange{t.Name, r.Key})
				}
			}
		}
		for _, r := range disk {
			if !now[r.Key] {
				a.Kept = append(a.Kept, r.Key)
			}
		}
		files, err := tables.Write(t.Name, rows)
		if err != nil {
			return nil, nil, err
		}
		a.Table = Table{Name: t.Name, Files: files, Rows: len(rows)}
		out = append(out, a)
	}
	return out, changes, nil
}

// tableValues is the rows of table name in ts, in key order, or false when ts holds no such table.
func tableValues(ts []Table, name string) ([]jsonx.Value, bool, error) {
	for _, t := range ts {
		if t.Name != name {
			continue
		}
		var out []jsonx.Value
		for _, f := range t.Files {
			rows, err := tables.Parse(f.Data)
			if err != nil {
				return nil, true, fmt.Errorf("%s: %w", f.Name, err)
			}
			for _, r := range rows {
				out = append(out, r.Value)
			}
		}
		return out, true, nil
	}
	return nil, false, nil
}
