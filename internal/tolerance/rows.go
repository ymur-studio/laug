package tolerance

// rows.go is laug gate tolerance -tables: the gates read from the summary tables, docs/tables, in place of each run's
// directory. gatherRows builds from the rows what gather() builds from the files, the members RULE reads and no
// others, so that RULE and SHOW run on it unchanged:
//
//	id, a, b     the gate id; plan.json's item of arm incumbent and its pair, the gates row's arm_pair (or PAIR's)
//	verdict      null when the gates row has no verdict_sha256, else stopped_at, host_quiet, host_noise, server_tag,
//	             incumbent, rules, laug_commit and host.type from the gates row (all 158 gates record one host type
//	             in verdict.json and gate.json), and comparison: the row's, its rows the compare rows' reading and
//	             result in pos order
//	driver       the row's driver // "run.sh"
//	th, thi      null when the row's th_not_judged (thi_not_judged) is, else a set marking those names not_judged,
//	             all nj reads of a thresholds file
//	slow         each of known_slow with "/known-slow.json", as find prints it
//	va, vb       null unless the item's gate-items row has verdict_file true, else {readings}: the item's
//	             gate-readings rows, name and value, in pos order
//
// One difference the tables hold: gate-readings keeps, after an item's readings, the metrics only its verdict's rows
// hold (iterations_failed, omes_exit, workflows_not_completed), so va and vb carry those too. RULE indexes readings by
// the set's names, the compare rows', the thresholds' and workflows_completed, and a set that named one of the three
// would read a number where the directories read none. load/steady-state.yaml names none of them.

import (
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/tables"
)

// TablesDir is where -tables reads the summary tables, from the checkout's root.
const TablesDir = "docs/tables"

// TablesEvidence is the -evidence that -tables gives when none is given: the gates table it read.
const TablesEvidence = TablesDir + "/gates" + tables.Ext

// Rows are the four tables -tables reads, each row by its key.
type Rows struct {
	gates, items, readings, compare map[string]jsonx.Value
	under                           map[string][]jsonx.Value // gate-readings and compare rows by parent key
}

// ReadRows reads gates, gate-items, gate-readings and compare from fsys's docs/tables.
func ReadRows(fsys fs.FS) (*Rows, error) {
	r := &Rows{under: map[string][]jsonx.Value{}}
	for _, t := range []struct {
		name   string
		to     *map[string]jsonx.Value
		parent string // the member that names a row's parent, for under; "" for none
	}{
		{"gates", &r.gates, ""},
		{"gate-items", &r.items, ""},
		{"gate-readings", &r.readings, "item"},
		{"compare", &r.compare, "gate"},
	} {
		rows, err := tables.Read(fsys, TablesDir, t.name)
		if err != nil {
			return nil, err
		}
		*t.to = make(map[string]jsonx.Value, len(rows))
		for _, row := range rows {
			(*t.to)[row.Key] = row.Value
			if t.parent == "" {
				continue
			}
			parent := text(field(row.Value, "gate"))
			if t.parent == "item" {
				parent = "item:" + parent + "/" + text(field(row.Value, "item"))
			}
			r.under[parent] = append(r.under[parent], row.Value)
		}
	}
	for _, rs := range r.under {
		slices.SortStableFunc(rs, func(a, b jsonx.Value) int {
			x, _ := field(a, "pos").Num()
			y, _ := field(b, "pos").Num()
			return jsonx.CompareNumbers(x, y)
		})
	}
	return r, nil
}

// text is v's string, "" for any other value.
func text(v jsonx.Value) string {
	s, _ := v.Str()
	return s
}

// CheckRows is Check over the tables: the same checks in the same order, each gate a row of the gates table in
// place of a directory.
func CheckRows(rows *Rows, r Request) error {
	if err := checkKnobs(r); err != nil {
		return err
	}
	for _, g := range r.IDs {
		if _, ok := rows.gates[g]; !ok {
			return &UsageError{Why: "no gate run " + g + " in " + TablesEvidence}
		}
	}
	return nil
}

// RunRows is Run over the tables, for a request CheckRows passed: the gathered text from the rows, then RULE and
// SHOW, the same lines and exits. It reads nothing but rows.
func RunRows(rows *Rows, r Request, set []byte, stdout, stderr io.Writer) int {
	return run(gatherRows(rows, r.IDs, r.Pair), "", r, set, stdout, stderr)
}

// gatherRows is gather over the tables: one array, a gate an object, by id in byte order.
func gatherRows(rows *Rows, ids []string, pair string) []byte {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	var gates []jsonx.Value
	for _, g := range sorted {
		row := rows.gates[g]
		var a, b string
		if pair != "" {
			a, b, _ = strings.Cut(pair, ":")
		} else {
			a, b = text(path(row, "arm_pair", "a")), text(path(row, "arm_pair", "b"))
		}
		if a+b != "" && !(itemKey.MatchString(a) && itemKey.MatchString(b)) {
			a, b = "", ""
		}
		var slow []jsonx.Value
		for _, s := range eachOpt(field(row, "known_slow")) {
			slow = append(slow, str(text(s)+"/known-slow.json"))
		}
		gates = append(gates, object("id", str(g), "a", str(a), "b", str(b), "verdict", rowVerdict(rows, g, row),
			"driver", alt(field(row, "driver"), str("run.sh")), "th", notJudged(field(row, "th_not_judged")),
			"thi", notJudged(field(row, "thi_not_judged")), "slow", jsonx.Arr(append([]jsonx.Value{}, slow...)),
			"va", itemReadings(rows, g, a), "vb", itemReadings(rows, g, b)))
	}
	return append(jsonx.MarshalCompact(jsonx.Arr(gates)), '\n')
}

// rowVerdict is the members of verdict.json RULE reads, from gate g's row; null when it has no verdict.json.
func rowVerdict(rows *Rows, g string, row jsonx.Value) jsonx.Value {
	if field(row, "verdict_sha256").IsNull() {
		return null
	}
	c := field(row, "comparison")
	if c.Kind() == jsonx.KindObject {
		var compared []jsonx.Value
		for _, r := range rows.under[g] {
			compared = append(compared, object("name", field(r, "reading"), "result", field(r, "result")))
		}
		c = object("wave", field(c, "wave"), "rows", jsonx.Arr(append([]jsonx.Value{}, compared...)))
	}
	return object("stopped_at", field(row, "stopped_at"), "host_quiet", field(row, "host_quiet"),
		"host_noise", field(row, "host_noise"), "server_tag", field(row, "server_tag"),
		"incumbent", field(row, "incumbent"), "rules", field(row, "rules"), "comparison", c,
		"host", object("type", path(row, "host", "type")), "laug_commit", field(row, "laug_commit"))
}

// notJudged is a thresholds file as nj reads it, from a row's list of the names it marks not_judged; null for null.
func notJudged(names jsonx.Value) jsonx.Value {
	if names.IsNull() {
		return null
	}
	var ts []jsonx.Value
	for _, n := range eachOpt(names) {
		ts = append(ts, object("name", n, "no_data", str("not_judged")))
	}
	return object("thresholds", jsonx.Arr(append([]jsonx.Value{}, ts...)))
}

// itemReadings is va or vb: item key of gate g as {readings}, its gate-readings rows in pos order; null when key is
// "" or the item's verdict JSON is not there as one object.
func itemReadings(rows *Rows, g, key string) jsonx.Value {
	if key == "" {
		return null
	}
	if v, _ := field(rows.items[g+"/"+key], "verdict_file").Bool(); !v {
		return null
	}
	o := jsonx.NewObject()
	for _, r := range rows.under["item:"+g+"/"+key] {
		o.Set(text(field(r, "reading")), field(r, "value"))
	}
	return object("readings", jsonx.Obj(o))
}
