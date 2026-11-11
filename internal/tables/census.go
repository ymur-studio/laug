package tables

// census.go is the gate runs as the summary tables count them: every row of the gates table, and of those the runs
// whose directories are here, under gate/results. A run that is not here was archived: the records table marks its
// record kept false. A test that reads every committed run takes its count from the rows and judges the runs here;
// with every run here, it judges them all.

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Dir is where the summary tables are, from the checkout's root.
const Dir = "docs/tables"

// GateResults is where the gate runs are, from the checkout's root.
const GateResults = "gate/results"

// A Census is the gates table's rows and which of their runs are here.
type Census struct {
	Rows map[string]jsonx.Value // each gates row, by its id
	IDs  []string               // every gates row's id, in byte order
	Here []string               // the ids whose directory is under gate/results, in byte order
}

// All is whether every run the gates table names is here.
func (c *Census) All() bool { return len(c.Here) == len(c.IDs) }

// IsHere is whether run id's directory is here.
func (c *Census) IsHere(id string) bool {
	_, ok := slices.BinarySearch(c.Here, id)
	return ok
}

// Count is how many rows of ids pass keep, ids nil for every row.
func (c *Census) Count(ids []string, keep func(row jsonx.Value) bool) int {
	if ids == nil {
		ids = c.IDs
	}
	n := 0
	for _, id := range ids {
		if keep(c.Rows[id]) {
			n++
		}
	}
	return n
}

// GateCensus reads the gates and records tables of fsys's docs/tables and lists its gate/results. It refuses a
// directory there that is no gates row, a gates row the records table does not name, and a run the records table
// marks kept that is not here. It is a helper the census tests of internal/judge, internal/rejudge and
// _report share; no command calls it.
func GateCensus(fsys fs.FS) (*Census, error) {
	gates, err := Read(fsys, Dir, "gates")
	if err != nil {
		return nil, err
	}
	records, err := Read(fsys, Dir, "records")
	if err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	named := map[string]bool{}
	for _, r := range records {
		named[r.Key] = true
		o, _ := r.Value.Obj()
		if k, ok := o.Get("kept"); ok {
			kept[r.Key], _ = k.Bool()
		}
	}
	c := &Census{Rows: make(map[string]jsonx.Value, len(gates))}
	for _, r := range gates {
		c.Rows[r.Key] = r.Value
		c.IDs = append(c.IDs, r.Key)
	}
	ents, err := fs.ReadDir(fsys, GateResults)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, ok := c.Rows[e.Name()]; !ok {
			return nil, fmt.Errorf("%s/%s: no row of %s/gates%s", GateResults, e.Name(), Dir, Ext)
		}
		c.Here = append(c.Here, e.Name())
	}
	slices.Sort(c.Here)
	for _, id := range c.IDs {
		record := GateResults + "/" + id + "/"
		if !named[record] {
			return nil, fmt.Errorf("%s/gates%s#%s: no row of %s/records%s names %s", Dir, Ext, id, Dir, Ext, record)
		}
		if kept[record] && !c.IsHere(id) {
			return nil, fmt.Errorf("%s: kept, and not here", path.Clean(record))
		}
	}
	return c, nil
}
