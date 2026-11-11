package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/rejudge"
	"github.com/ymur-studio/laug/internal/tables"
)

// tablesFixture is the nine runs cut to what the tables read (testdata/tables/README.md).
const tablesFixture = "testdata/tables"

// tablesJudge is a made-up judge identity the fixture's rejudge rows are keyed by.
var tablesJudge = strings.Repeat("ab", 32)

// fixtureFS is the fixture as a MapFS, plus a planted run, g20990101-000000: g20261015-065429's files with fake and
// rerun true and a poll.jsonl, since no committed run is fake or a rerun.
func fixtureFS(t *testing.T) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS(tablesFixture), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "README.md" {
			return err
		}
		data, err := os.ReadFile(filepath.Join(tablesFixture, filepath.FromSlash(p)))
		m[p] = &fstest.MapFile{Data: data}
		if rest, ok := strings.CutPrefix(p, "g20261015-065429/"); ok && err == nil {
			if rest == "verdict.json" {
				data = bytes.Replace(data, []byte(`"rerun": false`), []byte(`"rerun": true`), 1)
				data = bytes.Replace(data, []byte(`"fake": false`), []byte(`"fake": true`), 1)
			}
			rest = strings.ReplaceAll(rest, "g20261015-065429-", "g20990101-000000-")
			m["g20990101-000000/"+rest] = &fstest.MapFile{Data: data}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m["g20990101-000000/poll.jsonl"] = &fstest.MapFile{Data: []byte("{}\n")}
	return m
}

// fixtureRecords is the records of fsys as Load reads them, each run's rejudge "identical", which no run's files
// here could give (no verdict.md): GateTables takes the rejudge as Load gives it.
func fixtureRecords(t *testing.T, fsys fs.FS) *Records {
	t.Helper()
	listed, err := rejudge.Runs(fsys)
	if err != nil {
		t.Fatal(err)
	}
	rs := &Records{}
	for _, l := range listed {
		r := ReadRun(fsys, l.Gid)
		r.Recorded, r.Rejudge = l.Recorded, rejudge.Identical
		rs.Runs = append(rs.Runs, r)
	}
	return rs
}

// tableRows is each table's rows by key, from GateTables' files read back through tables.Parse.
func tableRows(t *testing.T, ts []Table) map[string]map[string]*jsonx.Object {
	t.Helper()
	out := map[string]map[string]*jsonx.Object{}
	for _, tb := range ts {
		out[tb.Name] = map[string]*jsonx.Object{}
		for _, f := range tb.Files {
			rows, err := tables.Parse(f.Data)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			for _, r := range rows {
				o, _ := r.Value.Obj()
				out[tb.Name][r.Key] = o
			}
		}
		if len(out[tb.Name]) != tb.Rows {
			t.Errorf("%s: %d rows read back, Rows says %d", tb.Name, len(out[tb.Name]), tb.Rows)
		}
	}
	return out
}

// cell is row's member down keys, compact, "<absent>" when a key on the way is not there.
func cell(o *jsonx.Object, keys ...string) string {
	v := jsonx.Obj(o)
	for _, k := range keys {
		ob, ok := v.Obj()
		if !ok {
			return "<absent>"
		}
		if v, ok = ob.Get(k); !ok {
			return "<absent>"
		}
	}
	return v.String()
}

// fileSum is the sha256 of the fixture file p, as a JSON string.
func fileSum(t *testing.T, fsys fs.FS, p string) string {
	t.Helper()
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return `"` + hex.EncodeToString(s[:]) + `"`
}

// TestTablesColumns builds the gate tables from the fixture's nine runs and a planted fake rerun, and holds each
// table's columns, in order, on every row, its count of rows against its source, and each column of chosen rows
// against the field it is read from: an A/B BLOCK at R7 with a WORSE compare row, an A/A ADVANCE, a run before the
// arm, a raun never steady, known-slow.json, a trap verdict, a refusal under internal/judge v3, a missing raun JSON,
// and the planted run. Numbers keep their recorded text.
func TestTablesColumns(t *testing.T) {
	fsys := fixtureFS(t)
	rs := fixtureRecords(t, fsys)
	ts, err := GateTables(rs, fsys, tablesJudge)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range ts {
		names = append(names, tb.Name)
	}
	if !slices.Equal(names, GateTableNames) {
		t.Fatalf("tables %q, want %q", names, GateTableNames)
	}
	rows := tableRows(t, ts)
	checkSchema(t, rows)

	// Rows against their sources: a gates and a rejudge row a run, a gate-items row an entry of items, a
	// gate-readings row a reading or row-only metric of each item verdict, a compare row a compared reading.
	items, readings, compared := 0, 0, 0
	for _, r := range rs.Runs {
		v, _ := readSum(fsys, r.ID+"/verdict.json")
		list, _ := at(v, "items").Arr()
		items += len(list)
		cr, _ := at(v, "comparison", "rows").Arr()
		compared += len(cr)
		for _, it := range list {
			key, _ := at(it, "key").Str()
			iv, _ := readSum(fsys, r.ID+"/"+key+"/"+r.ID+"-"+key+".verdict.json")
			names := map[string]bool{}
			if o, ok := at(iv, "readings").Obj(); ok {
				for _, k := range o.Keys() {
					names[k] = true
				}
			}
			ivRows, _ := at(iv, "rows").Arr()
			for _, row := range ivRows {
				m, _ := at(row, "metric").Str()
				names[m] = true
			}
			readings += len(names)
		}
	}
	for name, want := range map[string]int{"gates": 10, "rejudge": 10, "gate-items": items,
		"gate-readings": readings, "compare": compared} {
		if got := len(rows[name]); got != want {
			t.Errorf("%s: %d rows, want %d", name, got, want)
		}
	}
	if items != 21 || readings != 155 || compared != 48 {
		t.Errorf("the fixture's sources: %d items, %d readings, %d compared; the README's runs and the planted "+
			"one give 21, 155, 48",
			items, readings, compared)
	}

	checks := []struct {
		table, key string
		col        []string
		want       string
	}{
		// The A/B BLOCK: R7 decides, its reasons; the shell judge's sha256; the pair from plan.json.
		{"gates", "g20261018-092540", []string{"record"}, `"gate/results/g20261018-092540/"`},
		{"gates", "g20261018-092540", []string{"verdict_sha256"}, fileSum(t, fsys, "g20261018-092540/verdict.json")},
		{"gates", "g20261018-092540", []string{"gate_sha256"}, fileSum(t, fsys, "g20261018-092540/gate.json")},
		{"gates", "g20261018-092540", []string{"server_tag"}, `"1.32.1"`},
		{"gates", "g20261018-092540", []string{"incumbent"}, `"1.31.0"`},
		{"gates", "g20261018-092540", []string{"driver"}, `"run.sh"`},
		{"gates", "g20261018-092540", []string{"verdict"}, `"BLOCK"`},
		{"gates", "g20261018-092540", []string{"deciding"}, `"R7"`},
		{"gates", "g20261018-092540", []string{"deciding_reasons"}, `["steady: worse than the incumbent: ` +
			`persistence_latency_p99_ms 8.460 against 8.059 on incumbent, +0.401 past a tolerance of 0.35"]`},
		{"gates", "g20261018-092540", []string{"rules", "R7", "pass"}, `false`},
		{"gates", "g20261018-092540", []string{"combine_sha256"},
			`"33b4e89dc2ecef502e73ea88136a482fbf8694986789712fd5126e31f5faf6f0"`},
		{"gates", "g20261018-092540", []string{"judge_entry"}, `null`},
		{"gates", "g20261018-092540", []string{"host"},
			`{"type":"m8i.8xlarge","vcpu":32,"mem_gib":123,"mem_available_gib":121.7}`},
		{"gates", "g20261018-092540", []string{"t_post"}, `"2026-10-18"`},
		{"gates", "g20261018-092540", []string{"boot"}, `"b20261018-062046"`},
		{"gates", "g20261018-092540", []string{"comparison", "mode"}, `"judging"`},
		{"gates", "g20261018-092540", []string{"comparison", "gates"}, `20`},
		{"gates", "g20261018-092540", []string{"comparison", "rows"}, `<absent>`},
		{"gates", "g20261018-092540", []string{"arm_pair"}, `{"a":"incumbent","b":"steady"}`},
		{"gates", "g20261018-092540", []string{"th_not_judged"},
			`["activity_schedule_to_start_p99_ms","activity_schedule_to_start_over_10ms"]`},
		{"gates", "g20261018-092540", []string{"thi_not_judged"}, `[]`},
		{"gates", "g20261018-092540", []string{"files"}, `{"poll":false,"harness":true,"item_dirs":true}`},
		// The A/A ADVANCE: no rule decides.
		{"gates", "g20261018-075357", []string{"deciding"}, `null`},
		{"gates", "g20261018-075357", []string{"deciding_reasons"}, `[]`},
		{"gates", "g20261018-075357", []string{"incumbent"}, `"1.31.0"`},
		// Before the arm: no incumbent, no comparison, no driver, no thresholds-incumbent.json.
		{"gates", "g20261015-065429", []string{"incumbent"}, `null`},
		{"gates", "g20261015-065429", []string{"driver"}, `null`},
		{"gates", "g20261015-065429", []string{"comparison"}, `null`},
		{"gates", "g20261015-065429", []string{"arm_pair"}, `null`},
		{"gates", "g20261015-065429", []string{"thi_not_judged"}, `null`},
		{"gates", "g20261015-065429", []string{"fake"}, `false`},
		// The planted run: fake, a rerun, a poll.jsonl.
		{"gates", "g20990101-000000", []string{"fake"}, `true`},
		{"gates", "g20990101-000000", []string{"rerun"}, `true`},
		{"gates", "g20990101-000000", []string{"files", "poll"}, `true`},
		{"gate-items", "g20990101-000000/steady", []string{"verdict"}, `"ADVANCE"`},
		// known-slow.json in steady/.
		{"gates", "g20261017-212148", []string{"known_slow"}, `["steady"]`},
		{"gates", "g20261017-212148", []string{"deciding"}, `"R4"`},
		// The trap verdict: rules null, stopped at wave, no reasons of a rule.
		{"gates", "g20261017-014142", []string{"rules"}, `null`},
		{"gates", "g20261017-014142", []string{"stopped_at"}, `"wave"`},
		{"gates", "g20261017-014142", []string{"deciding"}, `"stopped"`},
		{"gates", "g20261017-014142", []string{"deciding_reasons"}, `[]`},
		// The refusal: refused decides, its why the reason; the judge an internal/judge entry and its sources.
		{"gates", "g20261024-100229", []string{"deciding"}, `"refused"`},
		{"gates", "g20261024-100229", []string{"deciding_reasons"}, `["lease incumbent: laugd refused it (curl ` +
			`exit 22): cell 35c802a7 not healthy before deadline: context deadline exceeded"]`},
		{"gates", "g20261024-100229", []string{"combine_sha256"}, `null`},
		{"gates", "g20261024-100229", []string{"judge_entry"}, `"v3"`},
		{"gates", "g20261024-104246", []string{"comparison"}, `null`},
		{"gates", "g20261024-104246", []string{"deciding"}, `"R2"`},

		// Items: a load item of arm incumbent, its verdict's fields; a raun ADVANCE and a raun never steady; a
		// raun JSON that is not there.
		{"gate-items", "g20261018-092540/incumbent", []string{"sha256"},
			fileSum(t, fsys, "g20261018-092540/incumbent/g20261018-092540-incumbent.verdict.json")},
		{"gate-items", "g20261018-092540/incumbent", []string{"arm"}, `"incumbent"`},
		{"gate-items", "g20261018-092540/incumbent", []string{"window_s"}, `302`},
		{"gate-items", "g20261018-092540/incumbent", []string{"fault"}, `null`},
		{"gate-items", "g20261018-092540/steady", []string{"failed"}, `["R7"]`},
		{"gate-items", "g20261018-092540/steady", []string{"thresholds_release"}, `"load/releases/1.32.yaml"`},
		{"gate-items", "g20261018-092540/pl", []string{"sha256"},
			fileSum(t, fsys, "g20261018-092540/pl/g20261018-092540-pl.json")},
		{"gate-items", "g20261018-092540/pl", []string{"verdict"}, `null`},
		{"gate-items", "g20261018-092540/pl", []string{"pool_busy"}, `{"before":0,"during":0,"drain":0,"after":0}`},
		{"gate-items", "g20261018-092540/pl", []string{"fault", "check"}, `""`},
		{"gate-items", "g20261018-092540/pl", []string{"fault", "time_to_recover_s"}, `"30"`},
		{"gate-items", "g20261018-092540/pl", []string{"fault", "iterations_failed", "during"}, `548`},
		{"gate-items", "g20261015-223429/pl", []string{"fault", "result"}, `"BLOCK"`},
		{"gate-items", "g20261015-223429/pl", []string{"fault", "check"}, `"steady"`},
		{"gate-items", "g20261015-230231/pl", []string{"sha256"}, `null`},
		{"gate-items", "g20261015-230231/pl", []string{"verdict_file"}, `false`},
		{"gate-items", "g20261015-230231/pl", []string{"fault"}, `null`},

		// Readings: a reading's value is .readings', its limit the row's, its no_data the release's thresholds;
		// a row-only metric kept; the incumbent's item read against thresholds-incumbent.json.
		{"gate-readings", "g20261018-092540/steady/persistence_latency_p99_ms", []string{"value"},
			`8.459853989313489`},
		{"gate-readings", "g20261018-092540/steady/persistence_latency_p99_ms", []string{"limit"}, `"23"`},
		{"gate-readings", "g20261018-092540/steady/persistence_latency_p99_ms", []string{"pos"}, `3`},
		{"gate-readings", "g20261018-092540/steady/persistence_latency_p99_ms", []string{"no_data"}, `"exceeds"`},
		{"gate-readings", "g20261018-092540/steady/activity_schedule_to_start_p99_ms", []string{"no_data"},
			`"not_judged"`},
		{"gate-readings", "g20261018-092540/steady/activity_schedule_to_start_p99_ms", []string{"value"}, `null`},
		{"gate-readings", "g20261018-092540/incumbent/activity_schedule_to_start_p99_ms", []string{"no_data"},
			`"exceeds"`},
		{"gate-readings", "g20261018-092540/steady/omes_exit", []string{"value"}, `0`},
		{"gate-readings", "g20261018-092540/steady/omes_exit", []string{"limit"}, `"0"`},
		{"gate-readings", "g20261018-092540/steady/omes_exit", []string{"pos"}, `14`},
		{"gate-readings", "g20261018-092540/steady/omes_exit", []string{"no_data"}, `null`},
		{"gate-readings", "g20261018-092540/steady/window_seconds", []string{"limit"}, `null`},

		// Compared: the WORSE row whole.
		{"compare", "g20261018-092540/persistence_latency_p99_ms", nil, `{"id":"g20261018-092540/` +
			`persistence_latency_p99_ms","gate":"g20261018-092540","reading":"persistence_latency_p99_ms","pos":1,` +
			`"scale":"ms","release_item":"steady","incumbent_item":"incumbent","release":8.459853989313489,` +
			`"incumbent":8.058667639455804,"d":0.401,"tolerance":0.35,"judged":true,"result":"WORSE"}`},

		// Rejudge: keyed by the judge's first 12 hex.
		{"rejudge", "g20261018-092540/" + tablesJudge[:12], nil, `{"id":"g20261018-092540/` + tablesJudge[:12] +
			`","gate":"g20261018-092540","judge_sha256":"` + tablesJudge + `","outcome":"identical","why":null}`},
	}
	for _, c := range checks {
		o, ok := rows[c.table][c.key]
		if !ok {
			t.Errorf("%s: no row %s", c.table, c.key)
			continue
		}
		if got := cell(o, c.col...); got != c.want {
			t.Errorf("%s %s %s: %s, want %s", c.table, c.key, strings.Join(c.col, "."), got, c.want)
		}
	}
	// Only the R7 run has a WORSE row.
	for key, o := range rows["compare"] {
		if cell(o, "result") == `"WORSE"` && !strings.HasPrefix(key, "g20261018-092540/") {
			t.Errorf("compare %s: WORSE outside the R7 run", key)
		}
	}
}

// TestTablesRefuseRecords holds GateTables' refusals: records whose rejudge did not run, a judge identity that is
// not 64 hex, and a raun BLOCK whose reason no faults/run.sh check names; and RaunCheck's map of each check's
// reason.
func TestTablesRefuseRecords(t *testing.T) {
	fsys := fixtureFS(t)
	rs := fixtureRecords(t, fsys)
	if _, err := GateTables(rs, fsys, "abc"); err == nil || !strings.Contains(err.Error(), "not a sha256") {
		t.Errorf("a short judge identity: %v", err)
	}
	notRun := *rs
	notRun.NotRun = "the checkout's judge is not HEAD's"
	if _, err := GateTables(&notRun, fsys, tablesJudge); err == nil || !strings.Contains(err.Error(), "the rejudge "+
		"did not run (the checkout's judge is not HEAD's)") {
		t.Errorf("rejudge not run: %v", err)
	}
	p := "g20261015-223429/pl/g20261015-223429-pl.json"
	fsys[p] = &fstest.MapFile{Data: bytes.Replace(fsys[p].Data, []byte(`"never steady: `), []byte(`"odd: `), 1)}
	if _, err := GateTables(rs, fsys, tablesJudge); err == nil || !strings.HasPrefix(err.Error(), "g20261015-223429: "+
		"item pl: a raun BLOCK whose reason no faults/run.sh check names: \"odd: ") {
		t.Errorf("an unknown BLOCK reason: %v", err)
	}
	for _, c := range []struct{ result, reason, check string }{
		{"ADVANCE", "", ""},
		{"ADVANCE", "anything", ""},
		{"BLOCK", "never steady: x=1", "steady"},
		{"BLOCK", "no signal reading before inject: custom read null", "signal"},
		{"BLOCK", "inject failed: 'x' exited 1", "inject"},
		{"BLOCK", "did not land: signal custom stayed at 0 over 60s", "landed"},
		{"BLOCK", "remove failed: x", "remove"},
		{"BLOCK", "did not recover within 120s: signal 3", "recovered"},
		{"BLOCK", "recovered in 75s, budget 60s", "recovered"},
		{"BLOCK", "after window not clean: x=1", "after window"},
		{"BLOCK", "3 failed iteration(s) in the after window", "lost work"},
		{"BLOCK", "sweep failed: x", "lost work"},
		{"BLOCK", "sweep found no workflows started before t", "lost work"},
		{"BLOCK", "sweep: 2 running among workflows started before t", "lost work"},
		{"BLOCK", "after window completed 10 workflows, floor 20 (1/s x (30 s - 10))", "lost work"},
	} {
		if got, ok := RaunCheck(c.result, c.reason); got != c.check || !ok {
			t.Errorf("RaunCheck(%s, %q) = %q, %v; want %q", c.result, c.reason, got, ok, c.check)
		}
	}
	for _, reason := range []string{"", "failed iteration(s) with no count", "odd"} {
		if got, ok := RaunCheck("BLOCK", reason); ok {
			t.Errorf("RaunCheck(BLOCK, %q) = %q, ok; want refused", reason, got)
		}
	}
}

// TestTablesDeterministic builds the fixture's tables twice through Load, every run judged again, and holds the
// same bytes, and the same as a build from fs.FS reads of the same files (but rejudge, which Load alone runs).
func TestTablesDeterministic(t *testing.T) {
	build := func(dir string) []Table {
		t.Helper()
		rs, err := Load("", dir, rejudge.Checkout{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		ts, err := GateTables(rs, os.DirFS(dir), tablesJudge)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	a, b := build(tablesFixture), build(tablesFixture)
	if len(a) != len(b) {
		t.Fatalf("%d tables, then %d", len(a), len(b))
	}
	for i := range a {
		if len(a[i].Files) != len(b[i].Files) {
			t.Fatalf("%s: %d files, then %d", a[i].Name, len(a[i].Files), len(b[i].Files))
		}
		for j := range a[i].Files {
			if a[i].Files[j].Name != b[i].Files[j].Name || !bytes.Equal(a[i].Files[j].Data, b[i].Files[j].Data) {
				t.Errorf("%s: two builds differ", a[i].Files[j].Name)
			}
		}
		if len(a[i].Files[0].Data) == 0 {
			t.Errorf("%s: no rows", a[i].Name)
		}
	}
	// Every run judged again: none here has a verdict.md, so each is not judged, and says why.
	rows := tableRows(t, a)
	for key, o := range rows["rejudge"] {
		if cell(o, "outcome") != `"not judged"` || !strings.Contains(cell(o, "why"), "no verdict.md in") {
			t.Errorf("rejudge %s: %s, %s", key, cell(o, "outcome"), cell(o, "why"))
		}
	}
	// The same rows, but rejudge, from fs.FS reads of the same bytes: Load's view and the MapFS's agree.
	fsys := fixtureFS(t)
	delete(fsys, "g20990101-000000/poll.jsonl")
	for p := range fsys {
		if strings.HasPrefix(p, "g20990101-000000/") {
			delete(fsys, p)
		}
	}
	m, err := GateTables(fixtureRecords(t, fsys), fsys, tablesJudge)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if a[i].Name == "rejudge" {
			continue
		}
		if !bytes.Equal(a[i].Files[0].Data, m[i].Files[0].Data) {
			t.Errorf("%s: Load's build and the MapFS's differ", a[i].Name)
		}
	}
}
