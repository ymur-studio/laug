package report

// rows.go holds the summary tables' rows the records layer counts from: a gates row a run and the compare rows of
// each, keyed as docs/tables keys them. The bands (BandOf), the validation lists' bounds and the pages' counts read
// these rows, not the run directories, so a count stays when its runs leave gate/results and only their rows remain.

import (
	"cmp"
	"errors"
	"io/fs"
	"slices"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/tables"
)

// Rows are gates and compare rows, as the tables hold them.
type Rows struct {
	Gates   map[string]jsonx.Value   // a gates row by gate id
	Compare map[string][]jsonx.Value // a gate's compare rows, by gate id, in pos order
}

// runRows are the rows of runs, built as -tables builds them from results.
func runRows(results fs.FS, runs []Run) Rows {
	rows := Rows{Gates: map[string]jsonx.Value{}, Compare: map[string][]jsonx.Value{}}
	for _, r := range runs {
		g := readGateRun(results, r.ID)
		rows.Gates[r.ID] = gatesRow(g, r)
		if c := compareRows(g); len(c) > 0 {
			rows.Compare[r.ID] = c
		}
	}
	return rows
}

// add adds the rows of checkout's docs/tables/gates.jsonl and compare.jsonl whose gate has no row here: the runs that
// left gate/results. A table that is not there adds none; one that does not read as a table is an error.
func (rows Rows) add(checkout fs.FS) error {
	dir := DocsDir + "/" + TablesDir
	gates, err := tables.Read(checkout, dir, "gates")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	here := map[string]bool{}
	for g := range rows.Gates {
		here[g] = true
	}
	for _, r := range gates {
		if !here[r.Key] {
			rows.Gates[r.Key] = r.Value
		}
	}
	compare, err := tables.Read(checkout, dir, "compare")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	added := map[string][]jsonx.Value{}
	for _, r := range compare {
		if g := str(at(r.Value, "gate")); !here[g] {
			added[g] = append(added[g], r.Value)
		}
	}
	for g, rs := range added {
		slices.SortStableFunc(rs, func(a, b jsonx.Value) int { return cmp.Compare(rowPos(a), rowPos(b)) })
		rows.Compare[g] = rs
	}
	return nil
}

// rowPos is a row's pos, -1 when it has none.
func rowPos(v jsonx.Value) float64 {
	if n := num(at(v, "pos")); n.OK {
		return n.F
	}
	return -1
}

// RowsOf are the gates and compare rows of checkout's docs/tables alone, as a page that cites them reads them.
func RowsOf(checkout fs.FS) (Rows, error) {
	rows := Rows{Gates: map[string]jsonx.Value{}, Compare: map[string][]jsonx.Value{}}
	return rows, rows.add(checkout)
}
