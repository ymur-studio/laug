package judge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
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

// committedRuns is every run directory under gate/results, by name: each a row of the gates table.
func committedRuns(t *testing.T) []string {
	t.Helper()
	c := census(t)
	if len(c.Here) == 0 {
		t.Fatal("gate/results holds no run of the gates table")
	}
	return c.Here
}

// hereOf is the ids of ids that are here, in their order.
func hereOf(c *tables.Census, ids []string) []string {
	var out []string
	for _, id := range ids {
		if c.IsHere(id) {
			out = append(out, id)
		}
	}
	return out
}

// rowMember is member name of a gates row.
func rowMember(row jsonx.Value, name string) jsonx.Value {
	o, ok := row.Obj()
	if !ok {
		return jsonx.Null()
	}
	v, _ := o.Get(name)
	return v
}

// rowVersion is the version a gates row records, as recordedVersion reads gate.json: by its combine_sha256, or with
// none by its judge_entry.
func rowVersion(t *testing.T, row jsonx.Value) Version {
	t.Helper()
	if sha, ok := rowMember(row, "combine_sha256").Str(); ok {
		v, ok := Lookup(sha)
		if !ok {
			t.Fatalf("%s: combine_sha256 %q, no version here", rowMember(row, "id"), sha)
		}
		return v
	}
	name, _ := rowMember(row, "judge_entry").Str()
	v, ok := Named(name)
	if !ok {
		t.Fatalf("%s: no combine_sha256, and judge_entry %q, no version here", rowMember(row, "id"), name)
	}
	return v
}
