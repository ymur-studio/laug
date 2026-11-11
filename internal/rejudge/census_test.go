package rejudge

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/tables"
)

// census is the committed runs as the summary tables count them (tables.GateCensus): the gates table's 158 rows, and
// the runs of those whose directories are here. With every run here a test judges all 158; once runs are archived it
// judges the runs that stayed, and takes the counts it holds them to from their rows.
func census(t *testing.T) *tables.Census {
	t.Helper()
	c, err := tables.GateCensus(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.IDs) != 158 {
		t.Fatalf("docs/tables/gates.jsonl holds %d rows, want 158", len(c.IDs))
	}
	return c
}

// member is member name of a row.
func member(row jsonx.Value, name string) jsonx.Value {
	o, ok := row.Obj()
	if !ok {
		return jsonx.Null()
	}
	v, _ := o.Get(name)
	return v
}

// rowVersion is the version a gates row records, as Runs reads gate.json: by its combine_sha256, or with none by its
// judge_entry; "" for none registered.
func rowVersion(row jsonx.Value) string {
	if sha, ok := member(row, "combine_sha256").Str(); ok {
		v, _ := judge.Lookup(sha)
		return v.Name
	}
	name, _ := member(row, "judge_entry").Str()
	v, _ := judge.Named(name)
	return v.Name
}

// outcomes is each gate's outcome in the rejudge rows of this judge, the one whose sources' sha256 judge.Self names,
// by gate id: every gates row has one.
func outcomes(t *testing.T, c *tables.Census) map[string]Outcome {
	t.Helper()
	rows, err := tables.Read(os.DirFS(filepath.Join("..", "..")), tables.Dir, "rejudge")
	if err != nil {
		t.Fatal(err)
	}
	self := judge.Self().SHA256
	out := map[string]Outcome{}
	for _, r := range rows {
		if sha, _ := member(r.Value, "judge_sha256").Str(); sha == self {
			gate, _ := member(r.Value, "gate").Str()
			o, _ := member(r.Value, "outcome").Str()
			out[gate] = Outcome(o)
		}
	}
	for _, id := range c.IDs {
		if _, ok := out[id]; !ok {
			t.Fatalf("docs/tables/rejudge.jsonl: no row of %s for the judge with sources' sha256 %s", id, self)
		}
	}
	return out
}

// countRows is the outcomes of ids, as Count counts answers.
func countRows(ids []string, by map[string]Outcome) Counts {
	c := Counts{}
	for _, id := range ids {
		c[by[id]]++
	}
	return c
}

// versions is how many of ids record each version, by the gates rows, those keep passes, as fmt prints a map.
func versions(c *tables.Census, ids []string, keep func(id string) bool) string {
	n := map[string]int{}
	for _, id := range ids {
		if keep(id) {
			n[rowVersion(c.Rows[id])]++
		}
	}
	return fmt.Sprint(n)
}
