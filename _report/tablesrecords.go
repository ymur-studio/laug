package report

// tablesrecords.go builds the summary tables of the records that are not gate runs, from a checkout's root: rauns (a
// row a raun or candidate JSON in faults/results), loads (a row a load in load/results, and a row a candidate's load
// copy in faults/results/load), load-readings (a row a reading of a load's verdict), readings-<batch> (a row a reading
// of a batch's cells/results/<batch>/readings.json), records (a row a record of any kind, the census) and quotes (a
// row a line of a record not kept whole that a page cites by line). Three hand inputs under docs/tables/src steer
// them: readings.txt names the readings kept whole, kept.txt the records kept whole, hosts.tsv the host each family
// of rauns and loads ran on, each checked here. As tables.go's: the records' text, null for absent, no clock, host or
// path but the records' own.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/tables"
)

// SrcDir is the tables' hand inputs' directory under TablesDir.
const SrcDir = "src"

// The hand inputs, under docs/tables/src.
const (
	srcReadings = "readings.txt"
	srcKept     = "kept.txt"
	srcHosts    = "hosts.tsv"
)

// resultDirs are the directories laug writes its records into, from the checkout's root, in the order the records
// table's kinds are read.
var resultDirs = []string{"gate/results", "faults/results", "load/results", "cells/results"}

// A resultFile is one committed file under resultDirs: its path from the checkout's root, its size and its sha256.
type resultFile struct {
	path string
	size int
	sum  string
}

// An entry is one record: its path as the records table keys it, its kind, its files sorted by path.
type entry struct {
	path, kind string
	files      []resultFile
}

// Record kinds, as the records table names them.
const (
	kindGate       = "gate"
	kindRaun       = "raun"
	kindCandidate  = "candidate"
	kindLoad       = "load"
	kindTranscript = "transcript"
	kindRunDir     = "run directory"
	kindReadings   = "readings"
)

// An archive is what the summary tables on disk say of records that are not here: those the records table marks
// kept false, which leave the tree for the private archive once their rows are built. A build reads none of their
// files; it reads their rows. The quotes rows hold the lines of an archived record that pages cite; the rauns and
// loads rows' ids are the runs hosts.tsv's globs match. With no table on disk, nothing is archived.
type archive struct {
	records []string          // the records table's keys of records kept false
	quotes  map[string]string // the quotes rows' text, by key <path>:<line>
	ids     []string          // the rauns and loads rows' ids, as checkHosts lists a build's
}

// readArchive reads the records, quotes, rauns and loads tables of checkout's docs/tables.
func readArchive(checkout fs.FS) (*archive, error) {
	a := &archive{quotes: map[string]string{}}
	read := func(name string) ([]tables.Row, error) {
		rows, err := tables.Read(checkout, DocsDir+"/"+TablesDir, name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return rows, err
	}
	records, err := read("records")
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if k, _ := at(r.Value, "kept").Bool(); !k {
			a.records = append(a.records, r.Key)
		}
	}
	quotes, err := read("quotes")
	if err != nil {
		return nil, err
	}
	for _, r := range quotes {
		a.quotes[r.Key], _ = at(r.Value, "text").Str()
	}
	for _, name := range []string{"rauns", "loads"} {
		rows, err := read(name)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			a.ids = append(a.ids, name+" "+r.Key)
		}
	}
	return a, nil
}

// Archived lists the records checkout's records table marks kept false that have no file in checkout: those that
// left the tree for the private archive. A build there gives only the rows of the records here, so report
// -tables, which writes the build whole, refuses to write while any is listed.
func Archived(checkout fs.FS) ([]string, error) {
	a, err := readArchive(checkout)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range a.records {
		if !present(checkout, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// present is whether record r, a path, a directory with its slash or a glob, has a file in checkout.
func present(checkout fs.FS, r string) bool {
	if strings.ContainsAny(r, "*?[") {
		m, _ := fs.Glob(checkout, r)
		return len(m) > 0
	}
	_, err := fs.Stat(checkout, strings.TrimSuffix(r, "/"))
	return err == nil
}

// has is whether path p, a file or a directory with its slash, is of an archived record: the record's path, a path
// under its directory, or a match of its glob.
func (a *archive) has(p string) bool {
	if a == nil {
		return false
	}
	for _, r := range a.records {
		if ok, _ := path.Match(r, p); ok || p == r || strings.HasSuffix(r, "/") && strings.HasPrefix(p, r) {
			return true
		}
	}
	return false
}

// gitIgnored says whether .gitignore keeps the file or directory p, under one of resultDirs, out of git: its lines
// that reach these directories, mirrored here so the tables read what a clone holds (TestTablesReadCommitted holds
// the two to git ls-files). A runbook walk's runs, raw logs and a gate's run state stay on a host and out of a row.
func gitIgnored(p string, dir bool) bool {
	base := path.Base(p)
	if !dir {
		for _, ext := range []string{".log", ".snap", ".mem", ".ext4", ".test"} {
			if strings.HasSuffix(base, ext) {
				return true
			}
		}
		if base == "coverage.out" || base == ".last-lease.json" || base == ".env" || strings.HasPrefix(base, ".lease.") {
			return true
		}
	}
	for _, pat := range []string{
		"load/results/*.verdict", "load/results/rb[0-9]-*", "faults/results/raun-rb[0-9]-*",
		"faults/results/load/raun-rb[0-9]-*", "gate/results/*/*/.lease.json", "gate/results/*/.steps",
		"gate/results/*/.state.json", "gate/results/*/*/.pid",
	} {
		if ok, _ := path.Match(pat, p); ok && (dir || !strings.HasSuffix(pat, "/.steps")) {
			return true
		}
	}
	return false
}

// walkResults lists the committed files under resultDirs in checkout, each directory's README.md left out, sorted by
// path in bytes; a directory that is not there lists none.
func walkResults(checkout fs.FS) ([]resultFile, error) {
	var out []resultFile
	for _, dir := range resultDirs {
		err := fs.WalkDir(checkout, dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == dir && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipDir
				}
				return err
			}
			if gitIgnored(p, d.IsDir()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || p == dir+"/README.md" {
				return nil
			}
			data, err := fs.ReadFile(checkout, p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			out = append(out, resultFile{p, len(data), hex.EncodeToString(sum[:])})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, func(a, b resultFile) int { return strings.Compare(a.path, b.path) })
	return out, nil
}

// loadFile is a load's file name in load/results: its id, then .json, .baseline.json or .verdict.json.
var loadFile = regexp.MustCompile(`^([^./]+)(\.baseline|\.verdict)?\.json$`)

// entries groups files into records: a gate run's directory, gate/results/<id>/; a raun or candidate JSON,
// faults/results/<name>.json; a candidate's load copy, faults/results/load/<name>.json; a load's files,
// load/results/<id>.*; a transcript, cells/results/<name>.txt; a batch's run directory, cells/results/<batch>/, and
// its readings.json as a record of its own. Each sorted by path. It refuses a file that is none of these.
func entries(checkout fs.FS, files []resultFile) ([]entry, error) {
	by := map[string]*entry{}
	add := func(p, kind string, f resultFile) {
		e := by[p]
		if e == nil {
			e = &entry{path: p, kind: kind}
			by[p] = e
		}
		e.files = append(e.files, f)
	}
	for _, f := range files {
		dir, rest, _ := strings.Cut(f.path, "/results/")
		parts := strings.Split(rest, "/")
		switch {
		case dir == "gate" && len(parts) > 1:
			add("gate/results/"+parts[0]+"/", kindGate, f)
		case dir == "faults" && len(parts) == 1 && strings.HasSuffix(rest, ".json"):
			v, err := readJSON(checkout, f.path)
			if err != nil {
				return nil, err
			}
			kind := kindRaun
			if _, ok := at(v, "candidate").Str(); ok {
				kind = kindCandidate
			}
			add(f.path, kind, f)
		case dir == "faults" && len(parts) == 2 && parts[0] == "load" && strings.HasSuffix(rest, ".json"):
			add(f.path, kindLoad, f)
		case dir == "load" && len(parts) == 1 && loadFile.MatchString(rest):
			add("load/results/"+loadFile.FindStringSubmatch(rest)[1]+".*", kindLoad, f)
		case dir == "cells" && len(parts) == 1 && strings.HasSuffix(rest, ".txt"):
			add(f.path, kindTranscript, f)
		case dir == "cells" && len(parts) > 1:
			add("cells/results/"+parts[0]+"/", kindRunDir, f)
			if len(parts) == 2 && parts[1] == "readings.json" {
				add(f.path, kindReadings, f)
			}
		default:
			return nil, fmt.Errorf("%s: not a record laug writes (a gate run's directory, a raun or candidate "+
				"JSON, a load's files, a transcript or a batch's directory)", f.path)
		}
	}
	out := make([]entry, 0, len(by))
	for _, e := range by {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b entry) int { return strings.Compare(a.path, b.path) })
	return out, nil
}

// listSum is the sha256 of files as sha256sum lists them, "<sha256>  <path>" a line, sorted by path in bytes: the
// sha256 of a record, one file or many.
func listSum(files []resultFile) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s  %s\n", f.sum, f.path)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RecordTableNames are RecordTables' tables but the readings ones, in its order; a readings-<batch> table a batch
// comes after load-readings.
var RecordTableNames = []string{"rauns", "loads", "load-readings", "records", "quotes"}

// readingsPrefix starts each batch's readings table's name.
const readingsPrefix = "readings-"

// ReadingsTable is the readings table of a batch: readings-<batch>, the batch in lowercase.
func ReadingsTable(batch string) string { return readingsPrefix + strings.ToLower(batch) }

// RecordTables builds the tables of checkout's records but the gate runs': rauns, loads, load-readings, a
// readings-<batch> a batch in name order, records and quotes. It refuses: a file under resultDirs that is no record
// laug writes, a raun BLOCK whose reason no faults/run.sh check names, a candidate BLOCK that no reading decided, a
// hand input's line that names nothing or does not resolve, a cite of a record that is not committed or a line past
// its end, and a table that tables.Write refuses.
func RecordTables(checkout fs.FS) ([]Table, error) {
	files, err := walkResults(checkout)
	if err != nil {
		return nil, err
	}
	es, err := entries(checkout, files)
	if err != nil {
		return nil, err
	}
	src := DocsDir + "/" + TablesDir + "/" + SrcDir + "/"
	arch, err := readArchive(checkout)
	if err != nil {
		return nil, err
	}
	kept, err := readKept(checkout, src+srcKept, es)
	if err != nil {
		return nil, err
	}
	rows := map[string][]jsonx.Value{}
	var batches []string
	whole, err := readWhole(checkout, src+srcReadings, es, arch)
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	for _, f := range files {
		sums[f.path] = f.sum
	}
	for _, e := range es {
		switch e.kind {
		case kindRaun, kindCandidate:
			r, err := raunRow(checkout, e)
			if err != nil {
				return nil, err
			}
			rows["rauns"] = append(rows["rauns"], r)
		case kindLoad:
			r, readings, err := loadRows(checkout, e)
			if err != nil {
				return nil, err
			}
			rows["loads"] = append(rows["loads"], r)
			rows["load-readings"] = append(rows["load-readings"], readings...)
		case kindReadings:
			batch := strings.TrimSuffix(strings.TrimPrefix(e.path, "cells/results/"), "/readings.json")
			rs, err := readingsRows(checkout, e.path, batch, whole[batch])
			if err != nil {
				return nil, err
			}
			name := ReadingsTable(batch)
			batches = append(batches, name)
			rows[name] = rs
		}
		o := row(e.path)
		o.Set("kind", jsonx.Str(e.kind))
		o.Set("files", jsonx.Num(count(len(e.files))))
		size := 0
		for _, f := range e.files {
			size += f.size
		}
		o.Set("bytes", jsonx.Num(count(size)))
		o.Set("sha256", jsonx.Str(listSum(e.files)))
		o.Set("kept", jsonx.Bool(kept[e.path]))
		rows["records"] = append(rows["records"], jsonx.Obj(o))
	}
	if err := checkHosts(checkout, src+srcHosts, rows["rauns"], rows["loads"], sums, arch); err != nil {
		return nil, err
	}
	keptFiles := map[string]bool{}
	for _, e := range es {
		if kept[e.path] {
			for _, f := range e.files {
				keptFiles[f.path] = true
			}
		}
	}
	if rows["quotes"], err = quoteRows(checkout, sums, keptFiles, arch); err != nil {
		return nil, err
	}
	names := append(slices.Clone(RecordTableNames[:3]), batches...)
	names = append(names, RecordTableNames[3:]...)
	var out []Table
	for _, name := range names {
		files, err := tables.Write(name, rows[name])
		if err != nil {
			return nil, err
		}
		out = append(out, Table{Name: name, Files: files, Rows: len(rows[name])})
	}
	return out, nil
}

// Tables builds every summary table: GateTables' five from rs, read by Load from checkout's gate/results with every
// run judged again, then RecordTables' from checkout, then FigureTables'.
func Tables(rs *Records, checkout fs.FS, judgeSHA256 string) ([]Table, error) {
	results, err := fs.Sub(checkout, "gate/results")
	if err != nil {
		return nil, err
	}
	gt, err := GateTables(rs, results, judgeSHA256)
	if err != nil {
		return nil, err
	}
	rt, err := RecordTables(checkout)
	if err != nil {
		return nil, err
	}
	ft, err := FigureTables(checkout)
	if err != nil {
		return nil, err
	}
	return append(append(gt, rt...), ft...), nil
}

// srcLines are the lines of a hand input that are not blank or a # comment, each with its line number; a file that
// is not there has none.
func srcLines(fsys fs.FS, p string) ([]numberedLine, error) {
	data, err := fs.ReadFile(fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []numberedLine
	for i, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, numberedLine{i + 1, l})
	}
	return out, nil
}

type numberedLine struct {
	n    int
	text string
}

// readKept reads src/kept.txt, a kept record a line: its path as the records table keys it, a TAB, and what it shows
// in one line. Each path must be a record of es, once.
func readKept(fsys fs.FS, p string, es []entry) (map[string]bool, error) {
	lines, err := srcLines(fsys, p)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, e := range es {
		known[e.path] = true
	}
	kept := map[string]bool{}
	for _, l := range lines {
		rec, what, ok := strings.Cut(l.text, "\t")
		switch {
		case !ok || strings.TrimSpace(what) == "":
			return nil, fmt.Errorf("%s:%d: a record's path, a TAB, then what it shows", p, l.n)
		case !known[rec]:
			return nil, fmt.Errorf("%s:%d: %s is no record here", p, l.n, rec)
		case kept[rec]:
			return nil, fmt.Errorf("%s:%d: %s is named twice", p, l.n, rec)
		}
		kept[rec] = true
	}
	return kept, nil
}

// readWhole reads src/readings.txt, a reading kept whole a line: its batch, a space, and its id in that batch's
// readings table (<step>/<name>, #2 on a repeat). Each must name a reading whose value is an object or an array, once:
// a scalar is always kept whole. A line of a batch whose readings.json is archived is passed over: its table on disk
// holds what it kept. By batch, the ids.
func readWhole(fsys fs.FS, p string, es []entry, arch *archive) (map[string]map[string]bool, error) {
	lines, err := srcLines(fsys, p)
	if err != nil {
		return nil, err
	}
	ids := map[string]map[string]string{} // batch, id: kind
	for _, e := range es {
		if e.kind != kindReadings {
			continue
		}
		batch := strings.TrimSuffix(strings.TrimPrefix(e.path, "cells/results/"), "/readings.json")
		list, err := readingsList(fsys, e.path)
		if err != nil {
			return nil, err
		}
		ids[batch] = map[string]string{}
		for _, r := range list {
			ids[batch][r.id] = r.value.Type()
		}
	}
	out := map[string]map[string]bool{}
	for _, l := range lines {
		batch, id, ok := strings.Cut(l.text, " ")
		kind, named := ids[batch][id]
		switch {
		case !ok:
			return nil, fmt.Errorf("%s:%d: a batch, a space, then a reading's id", p, l.n)
		case ids[batch] == nil && arch.has("cells/results/"+batch+"/readings.json"):
			continue
		case ids[batch] == nil:
			return nil, fmt.Errorf("%s:%d: no cells/results/%s/readings.json", p, l.n, batch)
		case !named:
			return nil, fmt.Errorf("%s:%d: %s has no reading %s", p, l.n, batch, id)
		case kind != "object" && kind != "array":
			return nil, fmt.Errorf("%s:%d: %s %s is a %s, always kept whole", p, l.n, batch, id, kind)
		case out[batch][id]:
			return nil, fmt.Errorf("%s:%d: %s %s is named twice", p, l.n, batch, id)
		}
		if out[batch] == nil {
			out[batch] = map[string]bool{}
		}
		out[batch][id] = true
	}
	return out, nil
}

// A batchReading is one reading of a readings.json: its id, its place from 1, and the reading.
type batchReading struct {
	id    string
	n     int
	o     jsonx.Value
	value jsonx.Value
}

// readingsList reads a batch's readings.json, an array of readings, each keyed <step>/<name>, then #2 and on when the
// key repeats.
func readingsList(fsys fs.FS, p string) ([]batchReading, error) {
	v, err := readJSON(fsys, p)
	if err != nil {
		return nil, err
	}
	list, ok := v.Arr()
	if !ok {
		return nil, fmt.Errorf("%s: a %s, not an array of readings", p, v.Type())
	}
	seen := map[string]int{}
	out := make([]batchReading, 0, len(list))
	for i, r := range list {
		step, ok1 := at(r, "step").Str()
		name, ok2 := at(r, "name").Str()
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%s: reading %d has no step or name", p, i+1)
		}
		id := step + "/" + name
		seen[id]++
		if seen[id] > 1 {
			id += "#" + strconv.Itoa(seen[id])
		}
		v, _ := r.Obj()
		value, _ := v.Get("value")
		out = append(out, batchReading{id, i + 1, r, value})
	}
	return out, nil
}

// readingsRows are the rows of a batch's readings table: a row a reading, its value as recorded when it is a scalar
// or whole names its id, else its bytes, leaves and sha256 as jq -c writes it; whole says which.
func readingsRows(fsys fs.FS, p, batch string, whole map[string]bool) ([]jsonx.Value, error) {
	list, err := readingsList(fsys, p)
	if err != nil {
		return nil, err
	}
	out := make([]jsonx.Value, 0, len(list))
	for _, r := range list {
		o := row(r.id)
		o.Set("batch", jsonx.Str(batch))
		o.Set("n", jsonx.Num(count(r.n)))
		for _, k := range []string{"boot", "step", "name", "unit", "how", "at", "error"} {
			if k == "at" {
				o.Set(k, dated(at(r.o, k))) // its date (timeofday.go)
				continue
			}
			o.Set(k, at(r.o, k))
		}
		kind := r.value.Type()
		o.Set("kind", jsonx.Str(kind))
		keep := whole[r.id] || (kind != "object" && kind != "array")
		o.Set("whole", jsonx.Bool(keep))
		if keep {
			o.Set("value", r.value)
		} else {
			text := jsonx.AppendCompact(nil, r.value)
			sum := sha256.Sum256(text)
			d := jsonx.NewObject()
			d.Set("bytes", jsonx.Num(count(len(text))))
			d.Set("leaves", jsonx.Num(count(leaves(r.value))))
			d.Set("sha256", jsonx.Str(hex.EncodeToString(sum[:])))
			o.Set("value", jsonx.Obj(d))
		}
		out = append(out, jsonx.Obj(o))
	}
	return out, nil
}

// leaves is the count of scalars in v: 1 for a scalar, the sum of its members' for an object or array.
func leaves(v jsonx.Value) int {
	if a, ok := v.Arr(); ok {
		n := 0
		for _, e := range a {
			n += leaves(e)
		}
		return n
	}
	if o, ok := v.Obj(); ok {
		n := 0
		for i := range o.Len() {
			_, e := o.Member(i)
			n += leaves(e)
		}
		return n
	}
	return 1
}

// raunRow is the rauns row of a raun or candidate JSON: keyed by its file name less .json, which a candidate's
// run_id is not (it repeats across candidates).
func raunRow(fsys fs.FS, e entry) (jsonx.Value, error) {
	v, err := readJSON(fsys, e.path)
	if err != nil {
		return jsonx.Null(), err
	}
	if v.Kind() != jsonx.KindObject {
		return jsonx.Null(), fmt.Errorf("%s: a %s, not a raun's object", e.path, v.Type())
	}
	o := row(strings.TrimSuffix(path.Base(e.path), ".json"))
	o.Set("record", jsonx.Str(e.path))
	o.Set("sha256", jsonx.Str(e.files[0].sum))
	o.Set("run_id", at(v, "run_id"))
	o.Set("kind", jsonx.Str(e.kind))
	for _, k := range []string{"fault", "server_tag", "backend", "scenario", "cell", "rate", "signal", "signal_max",
		"signal_peak", "signal_base"} {
		o.Set(k, at(v, k))
	}
	resultKey := "result"
	if e.kind == kindCandidate {
		resultKey = "verdict"
	}
	result, _ := at(v, resultKey).Str()
	reason, _ := at(v, "reason").Str()
	o.Set("result", at(v, resultKey))
	o.Set("reason", at(v, "reason"))
	var check string
	if e.kind == kindCandidate {
		ds, _ := at(v, "deciding").Arr()
		if result == "BLOCK" && len(ds) == 0 {
			return jsonx.Null(), fmt.Errorf("%s: a candidate BLOCK that no reading decided (deciding is empty)",
				e.path)
		}
		if result == "BLOCK" {
			check = "load verdict" // faults/candidate.sh's step 7: load/verdict.sh over the load's window
		}
	} else {
		var ok bool
		if check, ok = RaunCheck(result, reason); !ok {
			return jsonx.Null(), fmt.Errorf("%s: a raun BLOCK whose reason no faults/run.sh check names: %q",
				e.path, reason)
		}
	}
	o.Set("check", text(check))
	// Only steady and after window are decided by a window's readings; landed is decided by the signal alone (its
	// during window is never read when the fault did not land, faults/run.sh's during window), so it names none.
	o.Set("deciding_window", text(map[string]string{"steady": "before", "after window": "after"}[check]))
	// The raun's stamps, Unix times, as its inject's date and each other stamp in seconds after the inject
	// (timeofday.go); the windows' and rows' times as their dates.
	inject := at(v, "inject_at")
	o.Set("date", unixDate(inject))
	for _, k := range raunStamps {
		o.Set(k+"_s", secondsAfter(at(v, k), inject))
	}
	for _, k := range []string{"time_to_recover_s", "remove_wait_s", "history_shards_ready_s", "settle_s", "after_s"} {
		o.Set(k, at(v, k))
	}
	o.Set("windows", dated(pick(v, "before", "during", "drain", "after")))
	for _, k := range []string{"readings", "iterations_failed", "pool_busy", "mechanisms", "shards_acquired",
		"server_fi", "candidate", "key", "from", "value", "expect", "blocks_on", "deciding", "outcome",
		"outcome_reason", "prediction", "wait_s", "reread_s", "rows"} {
		switch k {
		case "mechanisms":
			o.Set(k, mechanismsAfter(at(v, k), inject))
		case "rows":
			o.Set(k, dated(at(v, k)))
		default:
			o.Set(k, at(v, k))
		}
	}
	if l := at(v, "load"); l.Kind() == jsonx.KindObject {
		o.Set("load", pick(l, "duration", "rate", "max_concurrent", "window_s"))
	} else {
		o.Set("load", jsonx.Null())
	}
	return jsonx.Obj(o), nil
}

// readLoadFile is a load's file p as one JSON value: null for an empty baseline (lab1-smoke60.baseline.json is one),
// an error for a file that cannot be read or does not parse, an empty one but a baseline among them.
func readLoadFile(fsys fs.FS, p string) (jsonx.Value, error) {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return jsonx.Null(), err
	}
	if len(bytes.TrimSpace(data)) == 0 && strings.HasSuffix(p, ".baseline.json") {
		return jsonx.Null(), nil
	}
	v, err := jsonx.Parse(data)
	if err != nil {
		return jsonx.Null(), fmt.Errorf("%s: %w", p, err)
	}
	return v, nil
}

// loadRows are the loads row of a load, and its load-readings rows: a load in load/results, keyed by its run id, its
// files <id>.json, <id>.baseline.json and <id>.verdict.json; or a candidate's load copy in faults/results/load, keyed
// by its path less .json, its one file a run JSON. A file that cannot be read or does not parse is refused; an empty
// baseline reads as null (readLoadFile). The readings are the verdict's (verdictReadings), with each baseline file's
// value beside.
func loadRows(fsys fs.FS, e entry) (jsonx.Value, []jsonx.Value, error) {
	var id string
	var run, verdict, baseline jsonx.Value = jsonx.Null(), jsonx.Null(), jsonx.Null()
	var runSum, verdictSum, baselineSum string
	var names []string
	for _, f := range e.files {
		names = append(names, path.Base(f.path))
		v, err := readLoadFile(fsys, f.path)
		if err != nil {
			return jsonx.Null(), nil, err
		}
		switch {
		case strings.HasSuffix(f.path, ".verdict.json"):
			verdict, verdictSum = v, f.sum
		case strings.HasSuffix(f.path, ".baseline.json"):
			baseline, baselineSum = v, f.sum
		default:
			run, runSum = v, f.sum
		}
	}
	if strings.HasPrefix(e.path, "load/results/") {
		id = strings.TrimSuffix(strings.TrimPrefix(e.path, "load/results/"), ".*")
	} else {
		id = strings.TrimSuffix(e.path, ".json")
	}
	if run.Kind() != jsonx.KindObject {
		return jsonx.Null(), nil, fmt.Errorf("%s: no run JSON, or one that is not an object", e.path)
	}
	o := row(id)
	o.Set("record", jsonx.Str(e.path))
	o.Set("files", texts(names))
	o.Set("run_sha256", text(runSum))
	o.Set("verdict_sha256", text(verdictSum))
	o.Set("baseline_sha256", text(baselineSum))
	for _, k := range []string{"run_id", "server_tag", "server_version", "backend", "scenario", "rate", "duration",
		"max_concurrent", "iterations", "start", "end", "omes_commit", "omes_exit", "iterations_failed"} {
		if k == "start" || k == "end" {
			o.Set(k, dated(at(run, k))) // its date (timeofday.go)
			continue
		}
		o.Set(k, at(run, k))
	}
	for _, k := range []string{"verdict", "exit", "deciding", "readings", "window_s", "short_window",
		"thresholds_release"} {
		o.Set(k, at(verdict, k))
	}
	var readings []jsonx.Value
	if verdict.Kind() == jsonx.KindObject {
		for pos, r := range verdictReadings(verdict) {
			ro := row(id + "/" + r.name)
			ro.Set("load", jsonx.Str(id))
			ro.Set("reading", jsonx.Str(r.name))
			r.set(ro, pos)
			ro.Set("baseline", at(baseline, r.name))
			readings = append(readings, jsonx.Obj(ro))
		}
	}
	return jsonx.Obj(o), readings, nil
}

// hostPhrases are the four phrases docs/RECORDS.md lets a tracked file give a test machine in, each with the words a
// transcript's line that says it holds.
var hostPhrases = []struct{ phrase, token string }{
	{"the lab host, an m8i.8xlarge (32 vCPU, 128 GiB)", "m8i.8xlarge"},
	{"the dev host, a Linux VM with 24 vCPU and 47 GiB running Docker", "the dev host"},
	{"the metal host, an m8id.metal-48xl (192 vCPU, 768 GiB)", "m8id.metal-48xl"},
	{"the first lab host, an m8i.2xlarge (8 vCPU, 32 GiB)", "m8i.2xlarge"},
}

// checkHosts checks src/hosts.tsv, a family of runs a line: a glob of run ids (path.Match, against a rauns or loads
// row's id, a load copy's by its file name), a TAB, one of hostPhrases, a TAB, and the transcript:line or :line-line
// that says it. The glob matches a row, no run falls under two lines, the cite is a committed result whose lines are
// there, and they hold the phrase's machine. A run no line names has no host here. The runs are the build's and those
// of the rauns and loads rows on disk, so a glob of archived runs still matches; an archived transcript's lines are its
// quotes rows (citedLines).
func checkHosts(fsys fs.FS, p string, rauns, loads []jsonx.Value, sums map[string]string, arch *archive) error {
	lines, err := srcLines(fsys, p)
	if err != nil {
		return err
	}
	var ids []string
	for _, r := range rauns {
		id, _ := at(r, "id").Str()
		ids = append(ids, "rauns "+id)
	}
	for _, r := range loads {
		id, _ := at(r, "id").Str()
		ids = append(ids, "loads "+id)
	}
	if arch != nil {
		ids = append(ids, arch.ids...)
		slices.Sort(ids)
		ids = slices.Compact(ids)
	}
	owner := map[string]int{}
	for _, l := range lines {
		f := strings.Split(l.text, "\t")
		if len(f) != 3 {
			return fmt.Errorf("%s:%d: a glob of run ids, a TAB, the host's phrase, a TAB, the transcript:line", p, l.n)
		}
		token := ""
		for _, h := range hostPhrases {
			if f[1] == h.phrase {
				token = h.token
			}
		}
		if token == "" {
			return fmt.Errorf("%s:%d: %q is not one of docs/RECORDS.md's four host phrases", p, l.n, f[1])
		}
		matched := 0
		for _, id := range ids {
			ok, err := path.Match(f[0], path.Base(strings.SplitN(id, " ", 2)[1]))
			if err != nil {
				return fmt.Errorf("%s:%d: glob %s: %w", p, l.n, f[0], err)
			}
			if !ok {
				continue
			}
			if was, dup := owner[id]; dup {
				return fmt.Errorf("%s:%d: %s falls under line %d too", p, l.n, id, was)
			}
			owner[id] = l.n
			matched++
		}
		if matched == 0 {
			return fmt.Errorf("%s:%d: glob %s matches no raun or load", p, l.n, f[0])
		}
		cites := findCites(f[2])
		if len(cites) != 1 || cites[0].at != 0 {
			return fmt.Errorf("%s:%d: %q is not one transcript:line", p, l.n, f[2])
		}
		got, err := citedLines(fsys, sums, cites[0], arch)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", p, l.n, err)
		}
		if !strings.Contains(strings.Join(got, " "), token) {
			return fmt.Errorf("%s:%d: %s:%d-%d does not say %s", p, l.n, cites[0].path, cites[0].from, cites[0].to,
				token)
		}
	}
	return nil
}

// A cite is a by-line cite of a record: its path, its first and last line, and where it starts in the text. self
// marks one that is a row's key, inside a row cite, docs/tables/quotes.jsonl#<path>:<line>: it names the quotes row,
// not the record's line.
type cite struct {
	path     string
	from, to int
	at       int
	self     bool
}

// citePath is a record's path; citeLines a line or a range after its colon; citeMore a further range of the same path,
// as pages write them: `cells/results/x.txt`:11-12, :89-101, or (`cells/results/x.txt:369`, :399). A path with a
// brace or a placeholder in it is not a cite of one record and is left alone.
var (
	citePath  = regexp.MustCompile("(?:gate|cells|faults|load)/results/[A-Za-z0-9_.+/-]*[A-Za-z0-9_]")
	citeFirst = regexp.MustCompile("^`?(?:, )?:([0-9]+)(?:-([0-9]+))?")
	citeMore  = regexp.MustCompile("^`?,? (?:and )?:([0-9]+)(?:-([0-9]+))?")
)

// rowCite is a row cite, docs/tables/<table>.jsonl#<key>, up to the first space, backtick or quote.
var rowCite = regexp.MustCompile("docs/tables/[a-z0-9-]+\\.jsonl#[^\\s`'\"]+")

// findCites are the by-line cites of records in s, in order, each inside a row cite marked self.
func findCites(s string) []cite {
	var out []cite
	rows := rowCite.FindAllStringIndex(s, -1)
	for _, loc := range citePath.FindAllStringIndex(s, -1) {
		p, rest := s[loc[0]:loc[1]], s[loc[1]:]
		self := slices.ContainsFunc(rows, func(r []int) bool { return loc[0] >= r[0] && loc[0] < r[1] })
		m := citeFirst.FindStringSubmatchIndex(rest)
		for m != nil {
			from, _ := strconv.Atoi(rest[m[2]:m[3]])
			to := from
			if m[4] >= 0 {
				to, _ = strconv.Atoi(rest[m[4]:m[5]])
			}
			out = append(out, cite{p, from, to, loc[0], self})
			rest = rest[m[1]:]
			m = citeMore.FindStringSubmatchIndex(rest)
		}
	}
	return out
}

// citedLines are the lines c names of its record, which must be a committed result with those lines, or an archived
// one whose quotes rows on disk hold each of them.
func citedLines(fsys fs.FS, sums map[string]string, c cite, arch *archive) ([]string, error) {
	if c.from < 1 || c.to < c.from {
		return nil, fmt.Errorf("%s:%d-%d is not a range of lines", c.path, c.from, c.to)
	}
	if _, ok := sums[c.path]; !ok && arch.has(c.path) {
		var out []string
		for n := c.from; n <= c.to; n++ {
			key := c.path + ":" + strconv.Itoa(n)
			text, ok := arch.quotes[key]
			if !ok {
				return nil, fmt.Errorf("%s is archived, and docs/tables/quotes.jsonl has no row %s", c.path, key)
			}
			out = append(out, text)
		}
		return out, nil
	}
	if _, ok := sums[c.path]; !ok {
		return nil, fmt.Errorf("%s is not a result file under %s", c.path, strings.Join(resultDirs, ", "))
	}
	data, err := fs.ReadFile(fsys, c.path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if c.to > len(lines) {
		return nil, fmt.Errorf("%s:%d: past its %d lines", c.path, c.to, len(lines))
	}
	return lines[c.from-1 : c.to], nil
}

// A page is a file the quotes table scans for by-line cites: its path from the checkout's root.
//
// tablePages are the pages: every .md file of the checkout outside resultDirs, docs/tables, testdata directories,
// bin/, report/, cells/run/, orchestrator/run/ and any name that starts with a dot, less the pages that are not
// public (a path or a directory check/private.txt names, when that file is here, and any *.local.md, a checkout's own
// notes) or are generated (docs/EVIDENCE.md, docs/img/README.md);
// each figure's spec, docs/img/src/*.txt; each gate set, gate/sets/*.txt, whose comments cite the transcripts that
// ran it; and the hand TSV, docs/tables/src/hosts.tsv. Sorted.
func tablePages(checkout fs.FS) ([]string, error) {
	skipDir := map[string]bool{"bin": true, "report": true, "cells/run": true, "orchestrator/run": true,
		DocsDir + "/" + TablesDir: true}
	for _, d := range resultDirs {
		skipDir[d] = true
	}
	skipFile := map[string]bool{DocsDir + "/" + Evidence: true, DocsDir + "/img/README.md": true}
	private, err := privateSet(checkout)
	if err != nil {
		return nil, err
	}
	var out []string
	err = fs.WalkDir(checkout, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || (d.IsDir() && (skipDir[p] || d.Name() == "testdata")) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || skipFile[p] || strings.HasSuffix(p, ".local.md") || private(p) {
			return nil
		}
		dir := path.Dir(p)
		switch {
		case strings.HasSuffix(p, ".md"),
			dir == specDir && strings.HasSuffix(p, ".txt"),
			dir == "gate/sets" && strings.HasSuffix(p, ".txt"):
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	hosts := DocsDir + "/" + TablesDir + "/" + SrcDir + "/" + srcHosts
	if _, err := fs.Stat(checkout, hosts); err == nil {
		out = append(out, hosts)
	}
	slices.Sort(out)
	return out, nil
}

// privateList is the private set's list, a path or a directory ending / a line, # lines and blank lines skipped. The
// list is itself private: a tree without it has no private set, and its pages are all public.
const privateList = "check/private.txt"

// privateSet reads privateList from the checkout, when it is there, and says whether a path is in the set.
func privateSet(checkout fs.FS) (func(string) bool, error) {
	data, err := fs.ReadFile(checkout, privateList)
	if errors.Is(err, fs.ErrNotExist) {
		return func(string) bool { return false }, nil
	}
	if err != nil {
		return nil, err
	}
	var set []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			set = append(set, l)
		}
	}
	return func(p string) bool {
		return slices.ContainsFunc(set, func(l string) bool {
			return p == l || strings.HasSuffix(l, "/") && strings.HasPrefix(p, l)
		})
	}, nil
}

// quoteRows are the quotes table's rows: a row a line of a committed result that is not a file of a kept record and
// that a page cites by line, keyed <path>:<line>, its text the line as recorded: of an archived record, its quotes row
// on disk. A cite that names a quotes row rather than a line, inside a row cite or on a spec's `row quotes <key>`
// line, gives its row from the record while the record is here, and nothing once the records table archives it: a
// quote never cites itself, and the row stays on disk as -docs keeps it. A record neither here nor archived fails.
func quoteRows(fsys fs.FS, sums map[string]string, kept map[string]bool, arch *archive) ([]jsonx.Value, error) {
	pages, err := tablePages(fsys)
	if err != nil {
		return nil, err
	}
	quoted := map[string]bool{}
	var out []jsonx.Value
	for _, pg := range pages {
		data, err := fs.ReadFile(fsys, pg)
		if err != nil {
			return nil, err
		}
		for n, line := range bytes.Split(data, []byte("\n")) {
			rowLine := path.Dir(pg) == specDir && bytes.HasPrefix(bytes.TrimSpace(line), []byte("row quotes "))
			for _, c := range findCites(string(line)) {
				if _, here := sums[c.path]; (c.self || rowLine) && !here && arch.has(c.path) {
					continue
				}
				got, err := citedLines(fsys, sums, c, arch)
				if err != nil {
					return nil, fmt.Errorf("%s:%d cites %w", pg, n+1, err)
				}
				if kept[c.path] {
					continue
				}
				for i, t := range got {
					key := c.path + ":" + strconv.Itoa(c.from+i)
					if quoted[key] {
						continue
					}
					quoted[key] = true
					o := row(key)
					o.Set("text", jsonx.Str(t))
					out = append(out, jsonx.Obj(o))
				}
			}
		}
	}
	return out, nil
}
