package report

// tables.go builds the summary tables of the gate runs, docs/tables/<name>.jsonl, which report -tables writes:
// gates (a row a run), gate-items (a row an item of a run), gate-readings (a row a reading of an item's verdict),
// compare (a row an R7 compared reading) and rejudge (a row a run judged again by one judge). Each row holds what
// the records hold, in the text they hold it in (jsonx), plus the few columns derived here: deciding and
// deciding_reasons from the rules, check from a raun's reason, and the sha256 of each file read. A field a record
// does not hold is null, and so is a text column with nothing in it: absent and null are not told apart (the
// earlier runs' verdict.json items have no arm, their gate.json host no type, their gate.json no driver, and
// verdict.json no incumbent or comparison). Nothing else is added: no clock, host or path but the records' own, so two builds are the
// same bytes (TestTablesDeterministic). The judge's name, a host phrase and a filter of the deciding readings are
// a reader's to apply, never stored.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/tables"
)

// TablesDir is the summary tables' directory under DocsDir.
const TablesDir = "tables"

// gateRecords is where a gate run's record lies from the checkout's root, as a row's record column names it,
// whichever copy of gate/results the rows were read from.
const gateRecords = "gate/results/"

// A Table is one summary table as -tables writes it: its name, its files, its count of rows.
type Table struct {
	Name  string
	Files []tables.File
	Rows  int
}

// GateTables builds the five gate tables from rs, read by Load from results with every run judged again, and from
// results itself, a gate/results or a copy: gates, gate-items, gate-readings, compare and rejudge, in that order.
// judgeSHA256 is the sources' sha256 of the judge that judged them again (judge.Self), which keys the rejudge rows.
// It refuses: records whose rejudge did not run, a judge identity that is not 64 hex, a raun BLOCK whose reason no
// faults/run.sh check names, and a table that tables.Write refuses (two rows of one key).
func GateTables(rs *Records, results fs.FS, judgeSHA256 string) ([]Table, error) {
	if rs.NotRun != "" {
		return nil, fmt.Errorf("the rejudge did not run (%s), so the rejudge rows cannot be built", rs.NotRun)
	}
	if len(judgeSHA256) != 64 || strings.Trim(judgeSHA256, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("judge identity %q is not a sha256 of 64 lowercase hex", judgeSHA256)
	}
	rows := map[string][]jsonx.Value{}
	for _, r := range rs.Runs {
		g := readGateRun(results, r.ID)
		rows["gates"] = append(rows["gates"], gatesRow(g, r))
		items, readings, err := itemRows(results, g)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.ID, err)
		}
		rows["gate-items"] = append(rows["gate-items"], items...)
		rows["gate-readings"] = append(rows["gate-readings"], readings...)
		rows["compare"] = append(rows["compare"], compareRows(g)...)
		o := row(r.ID + "/" + judgeSHA256[:12])
		o.Set("gate", jsonx.Str(r.ID))
		o.Set("judge_sha256", jsonx.Str(judgeSHA256))
		o.Set("outcome", jsonx.Str(string(r.Rejudge)))
		o.Set("why", text(r.RejudgeWhy))
		rows["rejudge"] = append(rows["rejudge"], jsonx.Obj(o))
	}
	var out []Table
	for _, name := range GateTableNames {
		files, err := tables.Write(name, rows[name])
		if err != nil {
			return nil, err
		}
		out = append(out, Table{Name: name, Files: files, Rows: len(rows[name])})
	}
	return out, nil
}

// GateTableNames are GateTables' tables, in its order.
var GateTableNames = []string{"gates", "gate-items", "gate-readings", "compare", "rejudge"}

// A gateRun is the files of one run the rows are read from: each JSON file as jsonx reads it, null when it is not
// there or does not parse, and the sha256 of its bytes, "" when it is not there.
type gateRun struct {
	id                           string
	fsys                         fs.FS
	gate, verdict, plan, th, thi jsonx.Value
	gateSum, verdictSum          string
}

func readGateRun(results fs.FS, id string) gateRun {
	g := gateRun{id: id, fsys: results}
	g.gate, g.gateSum = readSum(results, id+"/gate.json")
	g.verdict, g.verdictSum = readSum(results, id+"/verdict.json")
	g.plan, _ = readSum(results, id+"/plan.json")
	g.th, _ = readSum(results, id+"/thresholds.json")
	g.thi, _ = readSum(results, id+"/thresholds-incumbent.json")
	return g
}

// readSum is the file p as one JSON value and the sha256 of its bytes: null and "" when it cannot be read, null and
// its sha256 when it does not parse.
func readSum(fsys fs.FS, p string) (jsonx.Value, string) {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return jsonx.Null(), ""
	}
	sum := sha256.Sum256(data)
	v, err := jsonx.Parse(data)
	if err != nil {
		return jsonx.Null(), hex.EncodeToString(sum[:])
	}
	return v, hex.EncodeToString(sum[:])
}

// row is a new row of key id, its first member.
func row(id string) *jsonx.Object {
	o := jsonx.NewObject()
	o.Set("id", jsonx.Str(id))
	return o
}

// text is s, or null for "".
func text(s string) jsonx.Value {
	if s == "" {
		return jsonx.Null()
	}
	return jsonx.Str(s)
}

// pick is an object of v's members under keys, in that order, each null where v has none.
func pick(v jsonx.Value, keys ...string) jsonx.Value {
	o := jsonx.NewObject()
	for _, k := range keys {
		o.Set(k, at(v, k))
	}
	return jsonx.Obj(o)
}

// texts is ss as an array of strings, [] for none.
func texts(ss []string) jsonx.Value {
	vs := make([]jsonx.Value, len(ss))
	for i, s := range ss {
		vs[i] = jsonx.Str(s)
	}
	return jsonx.Arr(vs)
}

// gatesRow is the gates row of run r: gate.json's, verdict.json's and plan.json's fields, the judge gate.json records
// (rejudge.ReadRecorded, as Load read it), the rule that decided it and that rule's reasons, the thresholds' readings
// marked not_judged, the item directories that hold known-slow.json, and which of poll.jsonl, harness.json and an
// item directory the run holds. The compared readings are compare's rows, the items gate-items'.
func gatesRow(g gateRun, r Run) jsonx.Value {
	v := g.verdict
	o := row(r.ID)
	o.Set("record", jsonx.Str(gateRecords+r.ID+"/"))
	o.Set("verdict_sha256", text(g.verdictSum))
	o.Set("gate_sha256", text(g.gateSum))
	for _, k := range []string{"server_tag", "incumbent"} {
		o.Set(k, at(v, k))
	}
	for _, k := range []string{"plan", "driver"} {
		o.Set(k, at(g.gate, k))
	}
	for _, k := range []string{"fake", "rerun", "tree_dirty", "laug_commit"} {
		o.Set(k, at(v, k))
	}
	o.Set("combine_sha256", text(r.Recorded.SHA256))
	o.Set("judge_entry", text(r.Recorded.Entry))
	o.Set("judge_sources", text(r.Recorded.Sources))
	o.Set("host", pick(at(g.gate, "host"), "type", "vcpu", "mem_gib", "mem_available_gib"))
	// t0 and t_post hold their dates, not their times of day (timeofday.go); elapsed_s is the span between them.
	o.Set("t0", unixDate(at(v, "t0", "wall")))
	o.Set("t_post", unixDate(at(v, "t_post", "wall")))
	for _, k := range []string{"elapsed_s", "items_in_plan", "deadline_s", "verdict", "class", "class_if_quiet",
		"refused", "stopped_at"} {
		o.Set(k, at(v, k))
	}
	// deciding is the rule's id, or refused or stopped: the refusal and the wave are in refused, stopped_at and
	// deciding_reasons already, so a filter on the word finds them. Null when nothing decided (an ADVANCE).
	deciding := r.Deciding()
	switch {
	case r.Refused != "":
		o.Set("deciding", jsonx.Str("refused"))
	case r.StoppedAt != "":
		o.Set("deciding", jsonx.Str("stopped"))
	default:
		o.Set("deciding", text(deciding))
	}
	reasons := jsonx.Arr([]jsonx.Value{})
	switch {
	case r.Refused != "":
		reasons = texts([]string{r.Refused})
	case r.StoppedAt == "" && deciding != "":
		if rs := at(v, "rules", deciding, "reasons"); rs.Kind() == jsonx.KindArray {
			reasons = rs
		}
	}
	o.Set("deciding_reasons", reasons)
	for _, k := range []string{"rules", "reasons", "host_quiet", "host_noise"} {
		o.Set(k, at(v, k))
	}
	if c := at(v, "comparison"); c.Kind() == jsonx.KindObject {
		o.Set("comparison", pick(c, "mode", "wave", "wave_measured", "measured_with", "evidence", "gates"))
	} else {
		o.Set("comparison", jsonx.Null())
	}
	o.Set("arm_pair", armPair(g.plan))
	o.Set("th_not_judged", notJudged(g.th))
	o.Set("thi_not_judged", notJudged(g.thi))
	o.Set("known_slow", texts(knownSlow(g.fsys, r.ID)))
	files := jsonx.NewObject()
	files.Set("poll", jsonx.Bool(r.Poll))
	files.Set("harness", jsonx.Bool(r.Harness))
	files.Set("item_dirs", jsonx.Bool(r.ItemDirs))
	o.Set("files", jsonx.Obj(files))
	o.Set("boot", bootID(r.Epoch))
	return jsonx.Obj(o)
}

// bootID names the host boot a run ran in from the run's own t0, so that no other run changes it: b and its boot
// epoch (EpochOf, t0's wall clock less its monotonic clock, floored to the second) in UTC as a gate id writes its
// time, b<date>-<time>. Runs of one boot share it, boots sort by it, and it is an id, not a stamp: t0 itself holds
// its date (timeofday.go). Null when t0 is not two numbers.
func bootID(epoch int64) jsonx.Value {
	if epoch == 0 {
		return jsonx.Null()
	}
	return jsonx.Str("b" + unixUTC(float64(epoch)).Format("20060102-150405"))
}

// unixUTC is the second a wall clock reading falls in.
func unixUTC(sec float64) time.Time {
	s := int64(sec)
	if float64(s) > sec {
		s--
	}
	return time.Unix(s, 0).UTC()
}

// armPair is the A/A or A/B pair plan.json names: a, the key of its first item of arm incumbent, and b, that item's
// pair, as tolerance's gather reads them (internal/tolerance/gather.go:47-50); null when no item has arm incumbent.
func armPair(plan jsonx.Value) jsonx.Value {
	items, _ := at(plan, "items").Arr()
	for _, it := range items {
		if s, _ := at(it, "arm").Str(); s == "incumbent" {
			o := jsonx.NewObject()
			o.Set("a", at(it, "key"))
			o.Set("b", at(it, "pair"))
			return jsonx.Obj(o)
		}
	}
	return jsonx.Null()
}

// notJudged is the names a thresholds file marks no_data "not_judged", in its order, each name by its first entry
// as tolerance's nj reads it (internal/tolerance/rule.go:306-316); null when the file is not there or holds no list.
func notJudged(th jsonx.Value) jsonx.Value {
	list, ok := at(th, "thresholds").Arr()
	if !ok {
		return jsonx.Null()
	}
	var names []string
	seen := map[string]bool{}
	for _, t := range list {
		n, ok := at(t, "name").Str()
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		if nd, _ := at(t, "no_data").Str(); nd == "not_judged" {
			names = append(names, n)
		}
	}
	return texts(names)
}

// noData is the no_data a thresholds file gives the reading name, by its first entry; null for none.
func noData(th jsonx.Value, name string) jsonx.Value {
	list, _ := at(th, "thresholds").Arr()
	for _, t := range list {
		if n, _ := at(t, "name").Str(); n == name {
			return at(t, "no_data")
		}
	}
	return jsonx.Null()
}

// knownSlow is the run's item directories that hold an entry named known-slow.json, sorted by bytes, as
// internal/tolerance's slow (gather.go) finds them, less the "/known-slow.json" it prints.
func knownSlow(fsys fs.FS, id string) []string {
	entries, _ := fs.ReadDir(fsys, id)
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := fs.Stat(fsys, id+"/"+e.Name()+"/known-slow.json"); err == nil {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

// itemRows are the gate-items rows of run g, a row an entry of verdict.json's items in its order, and the
// gate-readings rows of each item whose verdict file is there.
//
// An item's file is <key>/<gate>-<key>.json, the raun JSON, for an item of kind fault, and
// <key>/<gate>-<key>.verdict.json, the load's verdict, for any other: sha256 is that file's, null when it is not
// there, and verdict_file says it is there and is one object. verdict, window_s, short_window, deciding and
// thresholds_release are the load verdict's; fault is the raun's result, reason, the faults/run.sh check its reason
// starts with, time to recover and failed iterations.
func itemRows(results fs.FS, g gateRun) (items, readings []jsonx.Value, err error) {
	list, _ := at(g.verdict, "items").Arr()
	for _, it := range list {
		key, _ := at(it, "key").Str()
		kind, _ := at(it, "kind").Str()
		file := key + "/" + g.id + "-" + key + ".verdict.json"
		if kind == "fault" {
			file = key + "/" + g.id + "-" + key + ".json"
		}
		f, sum := readSum(results, g.id+"/"+file)
		o := row(g.id + "/" + key)
		o.Set("gate", jsonx.Str(g.id))
		o.Set("key", at(it, "key"))
		o.Set("record", jsonx.Str(gateRecords+g.id+"/"+key+"/"))
		o.Set("sha256", text(sum))
		for _, k := range []string{"kind", "arm", "expect", "observed", "class", "reason", "failed", "run_id",
			"cell", "exit", "stopped_by", "block", "pool_busy"} {
			o.Set(k, at(it, k))
		}
		load := jsonx.Null()
		if kind != "fault" {
			load = f
		}
		for _, k := range []string{"verdict", "window_s", "short_window", "deciding", "thresholds_release"} {
			o.Set(k, at(load, k))
		}
		o.Set("verdict_file", jsonx.Bool(f.Kind() == jsonx.KindObject))
		o.Set("fault", jsonx.Null())
		if kind == "fault" && f.Kind() == jsonx.KindObject {
			result, _ := at(f, "result").Str()
			reason, _ := at(f, "reason").Str()
			check, ok := RaunCheck(result, reason)
			if !ok {
				return nil, nil, fmt.Errorf("item %s: a raun BLOCK whose reason no faults/run.sh check names: %q",
					key, reason)
			}
			fo := jsonx.NewObject()
			fo.Set("result", at(f, "result"))
			fo.Set("reason", at(f, "reason"))
			fo.Set("check", jsonx.Str(check))
			fo.Set("time_to_recover_s", at(f, "time_to_recover_s"))
			fo.Set("iterations_failed", at(f, "iterations_failed"))
			o.Set("fault", jsonx.Obj(fo))
		}
		items = append(items, jsonx.Obj(o))
		if load.Kind() == jsonx.KindObject {
			th := g.th
			if arm, _ := at(it, "arm").Str(); arm == "incumbent" {
				th = g.thi
			}
			readings = append(readings, readingRows(g.id, key, load, th)...)
		}
	}
	return items, readings, nil
}

// readingRows are the gate-readings rows of one item's verdict v, each of verdictReadings in its order; no_data is
// th's for it, th being thresholds-incumbent.json for the item of arm incumbent and thresholds.json for any other.
func readingRows(gate, key string, v, th jsonx.Value) []jsonx.Value {
	list := verdictReadings(v)
	out := make([]jsonx.Value, 0, len(list))
	for pos, r := range list {
		o := row(gate + "/" + key + "/" + r.name)
		o.Set("gate", jsonx.Str(gate))
		o.Set("item", jsonx.Str(key))
		o.Set("reading", jsonx.Str(r.name))
		r.set(o, pos)
		o.Set("no_data", noData(th, r.name))
		out = append(out, jsonx.Obj(o))
	}
	return out
}

// A verdictReading is one reading of a load's verdict as the readings tables hold it: its name, its value, its row
// in .rows (null with none) and whether .deciding names it.
type verdictReading struct {
	name     string
	value    jsonx.Value
	row      jsonx.Value
	deciding bool
}

// set adds the reading's pos, value, limit, result, blocks and deciding to o, in that order.
func (r verdictReading) set(o *jsonx.Object, pos int) {
	o.Set("pos", jsonx.Num(count(pos)))
	o.Set("value", r.value)
	o.Set("limit", at(r.row, "limit"))
	o.Set("result", dated(at(r.row, "result"))) // "started before <a time>" holds its date (timeofday.go)
	o.Set("blocks", at(r.row, "blocks"))
	o.Set("deciding", jsonx.Bool(r.deciding))
}

// verdictReadings are the readings of a load's verdict v (load/verdict.sh's, an item's or a load's): each reading of
// .readings in its order, then each metric of .rows that .readings lacks (iterations_failed, omes_exit and
// workflows_not_completed), in .rows' order. value is .readings' when it holds the reading, as the neighbours figure
// reads it (lessondata.go, itemReadings), else the row's; row is the first row of its metric.
func verdictReadings(v jsonx.Value) []verdictReading {
	var list []verdictReading
	index := map[string]int{}
	if o, ok := at(v, "readings").Obj(); ok {
		for i := range o.Len() {
			k, val := o.Member(i)
			index[k] = len(list)
			list = append(list, verdictReading{name: k, value: val})
		}
	}
	rows, _ := at(v, "rows").Arr()
	for _, r := range rows {
		m, ok := at(r, "metric").Str()
		if !ok {
			continue
		}
		if i, ok := index[m]; ok {
			if list[i].row.IsNull() {
				list[i].row = r
			}
			continue
		}
		index[m] = len(list)
		list = append(list, verdictReading{name: m, value: at(r, "value"), row: r})
	}
	ds, _ := at(v, "deciding").Arr()
	for _, d := range ds {
		if s, ok := d.Str(); ok {
			if i, ok := index[s]; ok {
				list[i].deciding = true
			}
		}
	}
	return list
}

// compareRows are the compare rows of run g: a row each of verdict.json's comparison.rows, in its order, with the
// comparison's release and incumbent items.
func compareRows(g gateRun) []jsonx.Value {
	c := at(g.verdict, "comparison")
	rows, _ := at(c, "rows").Arr()
	out := make([]jsonx.Value, 0, len(rows))
	for pos, r := range rows {
		name, _ := at(r, "name").Str()
		o := row(g.id + "/" + name)
		o.Set("gate", jsonx.Str(g.id))
		o.Set("reading", at(r, "name"))
		o.Set("pos", jsonx.Num(count(pos)))
		o.Set("scale", at(r, "scale"))
		o.Set("release_item", at(c, "release"))
		o.Set("incumbent_item", at(c, "incumbent"))
		for _, k := range []string{"release", "incumbent", "d", "tolerance", "judged", "result"} {
			o.Set(k, at(r, k))
		}
		out = append(out, jsonx.Obj(o))
	}
	return out
}

// count is the whole number n as a literal, the text jq writes for it.
func count(n int) jsonx.Number {
	lit, err := jsonx.Literal(fmt.Sprint(n))
	if err != nil {
		panic(err) // a whole number is always a literal
	}
	return lit
}

// raunChecks are faults/run.sh's BLOCK reasons, each by the words it starts with, and the check that wrote it: a
// window or step of faults/run.sh, or load/lost-work.sh's lw_raun_judge, whose reasons run.sh appends after its own.
var raunChecks = []struct{ prefix, check string }{
	{"never steady: ", "steady"},                    // the before window
	{"no signal reading before inject: ", "signal"}, // the signal's reading before inject
	{"inject failed: ", "inject"},                   // inject
	{"did not land: ", "landed"},                    // the during window
	{"remove failed: ", "remove"},                   // remove
	{"did not recover within ", "recovered"},        // recovery, not within budget_s + 60 s
	{"recovered in ", "recovered"},                  // recovery, past the budget
	{"after window not clean: ", "after window"},    // the after window
	{"sweep failed: ", "lost work"},                 // lw_raun_judge, the sweep
	{"sweep found no workflows ", "lost work"},      // lw_raun_judge, the sweep
	{"sweep: ", "lost work"},                        // lw_raun_judge, the sweep
	{"after window completed ", "lost work"},        // lw_raun_judge, the floor
}

// failedIterations is the words lw_raun_judge's reasons hold after their count of failed iterations.
const failedIterations = " failed iteration(s) "

// RaunCheck is the check that decided a raun: for a BLOCK, the faults/run.sh check whose reason its reason starts
// with, and ok false when none does; for any other result, "" and ok.
func RaunCheck(result, reason string) (string, bool) {
	if result != "BLOCK" {
		return "", true
	}
	for _, c := range raunChecks {
		if strings.HasPrefix(reason, c.prefix) {
			return c.check, true
		}
	}
	if n := strings.TrimLeft(reason, "0123456789"); n != reason && strings.HasPrefix(n, failedIterations) {
		return "lost work", true
	}
	return "", false
}
