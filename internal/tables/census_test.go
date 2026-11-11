package tables

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// TestGateCensus counts the gates rows and the runs here, and refuses a run with no row, a row the records table does
// not name, and a kept run that is not here.
func TestGateCensus(t *testing.T) {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := func() fstest.MapFS {
		return fstest.MapFS{
			Dir + "/gates.jsonl": f(`{"id":"g1","combine_sha256":null}` + "\n" + `{"id":"g2","combine_sha256":"a"}` + "\n" +
				`{"id":"g3","combine_sha256":"b"}` + "\n"),
			Dir + "/records.jsonl": f(`{"id":"gate/results/g1/","kept":true}` + "\n" +
				`{"id":"gate/results/g2/","kept":false}` + "\n" + `{"id":"gate/results/g3/","kept":false}` + "\n"),
			GateResults + "/README.md":    f("not a run\n"),
			GateResults + "/g1/gate.json": f("{}"),
			GateResults + "/g3/gate.json": f("{}"),
		}
	}
	c, err := GateCensus(fsys())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.IDs, " ") != "g1 g2 g3" || strings.Join(c.Here, " ") != "g1 g3" || c.All() || !c.IsHere("g3") ||
		c.IsHere("g2") {
		t.Errorf("rows %q, here %q", c.IDs, c.Here)
	}
	none := func(ids []string) int {
		return c.Count(ids, func(row jsonx.Value) bool {
			o, _ := row.Obj()
			v, _ := o.Get("combine_sha256")
			return v.IsNull()
		})
	}
	if none(nil) != 1 || none(c.Here) != 1 || none([]string{"g2", "g3"}) != 0 {
		t.Errorf("counts %d, %d", none(nil), none(c.Here))
	}
	for _, x := range []struct {
		edit func(fstest.MapFS)
		want string
	}{
		{func(m fstest.MapFS) { m[GateResults+"/g9/gate.json"] = f("{}") },
			"gate/results/g9: no row of docs/tables/gates.jsonl"},
		{func(m fstest.MapFS) { m[Dir+"/records.jsonl"] = f(`{"id":"gate/results/g1/","kept":true}` + "\n") },
			"docs/tables/gates.jsonl#g2: no row of docs/tables/records.jsonl names gate/results/g2/"},
		{func(m fstest.MapFS) { delete(m, GateResults+"/g1/gate.json") }, "gate/results/g1: kept, and not here"},
	} {
		m := fsys()
		x.edit(m)
		if _, err := GateCensus(m); err == nil || err.Error() != x.want {
			t.Errorf("%v, want %q", err, x.want)
		}
	}
}
