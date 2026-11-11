package report

// figtables.go builds the figure tables, docs/tables/fig-<figure>.jsonl, which report -tables writes beside the
// others: the points a lesson's figure draws that no row of another table holds, cut from the record that holds
// them, each row with from, that record and, for a transcript, its line. Where a figure parses a record, its table
// holds the lines or values it parses, as recorded (lineRows, jsonLineRows, valueRow), and the figure parses them as
// it parsed the record; -tables reads the record whole first, with every check the figure makes. A figure table is
// built while its records are in the checkout; a checkout without them builds none, and -docs keeps the rows on disk
// (AddRows), as it keeps a readings table's.

import (
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

// A figureTable is one figure table: its name and how its rows are cut from the checkout. ok is false when its
// records are not in the checkout, so no table is built.
type figureTable struct {
	name  string
	build func(checkout fs.FS) (rows []jsonx.Value, ok bool, err error)
}

// figureTables are the figure tables, in FigureTables' order.
var figureTables = []figureTable{
	{adoptedTable, adoptedTableRows},
	{"fig-bucket-edges", bucketEdgeRows},
	{cancelTable, cancelRows},
	{timerPack.table, clockStepRows},
	{killHistoryTable, killHistoryRows},
	{microvmPack.table, restoreTimingRows},
	{stalenessTable, stalenessRows},
	{stuckTable, func(c fs.FS) ([]jsonx.Value, bool, error) { return b2Rows(c, stuckRauns, false) }},
	{throttleTable, func(c fs.FS) ([]jsonx.Value, bool, error) { return b2Rows(c, throttleRauns, false) }},
	{toxicTable, func(c fs.FS) ([]jsonx.Value, bool, error) { return b2Rows(c, toxicRauns, true) }},
}

// FigureTables builds the figure tables whose records are in checkout, in figureTables' order. It refuses a record
// that does not hold its points as its figure reads them, and a table that tables.Write refuses.
func FigureTables(checkout fs.FS) ([]Table, error) {
	var out []Table
	for _, f := range figureTables {
		rows, ok, err := f.build(checkout)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
		if !ok {
			continue
		}
		files, err := tables.Write(f.name, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, Table{Name: f.name, Files: files, Rows: len(rows)})
	}
	return out, nil
}

// edgesLine is the transcript line that lists the le strings of server 1.31.0's latency histograms, as the lab
// host's Prometheus listed them on the incumbent's cell of the arm pilot's first gate: a list a histogram the gate
// reads, wt, act, pers, fe and e2e, each joined by |.
const (
	edgesRecord = "cells/results/2026-10-17-arm-pilot.txt"
	edgesLine   = 530
)

// edgesHistograms are the histograms edgesLine lists, in its order; the rows are act's, and the others must match.
var edgesHistograms = []string{"wt", "act", "pers", "fe", "e2e"}

// bucketEdgeRows are fig-bucket-edges' rows: an le string a row, keyed by its place from 00, the list ending in
// +Inf. The line must list the five histograms, each the same le strings, each a number past the one before, then
// +Inf.
func bucketEdgeRows(checkout fs.FS) ([]jsonx.Value, bool, error) {
	data, err := fs.ReadFile(checkout, edgesRecord)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	from := edgesRecord + ":" + strconv.Itoa(edgesLine)
	lines := strings.Split(string(data), "\n")
	if len(lines) < edgesLine {
		return nil, false, fmt.Errorf("%s: past its %d lines", from, len(lines))
	}
	_, list, ok := strings.Cut(lines[edgesLine-1], "le on incumbent's cell")
	if ok {
		_, list, ok = strings.Cut(list, "s: ")
	}
	parts := strings.Split(list, "|")
	if !ok || len(parts) != len(edgesHistograms) {
		return nil, false, fmt.Errorf("%s: not the le strings of the %d histograms", from, len(edgesHistograms))
	}
	var act string
	for i, p := range parts {
		name, les, _ := strings.Cut(p, " ")
		if name != edgesHistograms[i] {
			return nil, false, fmt.Errorf("%s: histogram %d is %q, not %s", from, i+1, name, edgesHistograms[i])
		}
		if name == "act" {
			act = les
		}
	}
	for _, p := range parts {
		if _, les, _ := strings.Cut(p, " "); les != act {
			return nil, false, fmt.Errorf("%s: %s's le strings are not act's", from, strings.SplitN(p, " ", 2)[0])
		}
	}
	vals := strings.Split(act, ",")
	var rows []jsonx.Value
	last := 0.0
	for i, v := range vals {
		inf := v == "+Inf"
		if inf != (i == len(vals)-1) {
			return nil, false, fmt.Errorf("%s: %q at %d; +Inf is the last le, and only it", from, v, i+1)
		}
		if !inf {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || !(f > last) {
				return nil, false, fmt.Errorf("%s: %q is not an edge over the one before it", from, v)
			}
			last = f
		}
		o := row(fmt.Sprintf("%02d", i))
		o.Set("le", jsonx.Str(v))
		o.Set("from", jsonx.Str(from))
		rows = append(rows, jsonx.Obj(o))
	}
	if len(vals) < 3 {
		return nil, false, fmt.Errorf("%s: %d le strings, want the edges, then +Inf", from, len(vals))
	}
	return rows, true, nil
}

// killHistoryRows are fig-kill-history's rows: boot 6's three kill-history rauns' failed starts a second, a row a
// second that failed one, keyed <run_id>/<its place, from 000>, s and n as the bins file records them. Each bins
// file must hold every failure of its raun's Omes log, at the remove_at its raun JSON records, and from the restart
// on, the raun's drain and after windows' count (readBins). A checkout with none of the bins files builds none; one
// with some is an error.
func killHistoryRows(checkout fs.FS) ([]jsonx.Value, bool, error) {
	there := 0
	for _, p := range killHistoryBins {
		if _, err := fs.Stat(checkout, p); err == nil {
			there++
		}
	}
	if there == 0 {
		return nil, false, nil
	}
	var rows []jsonx.Value
	for i, p := range killHistoryRauns {
		r, err := readKH(checkout, p)
		if err != nil {
			return nil, false, err
		}
		if err := readBins(checkout, killHistoryBins[i], &r); err != nil {
			return nil, false, err
		}
		v, err := readJSON(checkout, killHistoryBins[i])
		if err != nil {
			return nil, false, err
		}
		a, _ := at(v, "by_second_from_remove_at").Arr()
		if err := fits(killHistoryBins[i], len(a)-1, 3); err != nil {
			return nil, false, err
		}
		for j, e := range a {
			o := row(fmt.Sprintf("%s/%03d", r.id, j))
			o.Set("run", jsonx.Str(r.id))
			o.Set("pos", jsonx.Num(count(j)))
			o.Set("s", at(e, "s"))
			o.Set("n", at(e, "n"))
			o.Set("from", jsonx.Str(killHistoryBins[i]))
			rows = append(rows, jsonx.Obj(o))
		}
	}
	return rows, true, nil
}

// lineRows are the lines of the text record p that keep keeps, line n from 1: a row each, keyed <prefix>/<n, four
// digits>, with value, the line as recorded, a string, and from, p:<n>. A figure joins them (figTable.text) and reads
// them as it read the record, so keep must keep every line the figure's reader matches, and no line it would match
// twice. A record with no line kept, or past 9,999 lines (fits), is an error.
func lineRows(checkout fs.FS, p, prefix string, keep func(n int, line string) bool) ([]jsonx.Value, error) {
	text, err := readText(checkout, p)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(text, "\n")
	if err := fits(p, len(strings.Split(strings.TrimSuffix(text, "\n"), "\n")), 4); err != nil {
		return nil, err
	}
	var rows []jsonx.Value
	for i, l := range lines {
		if !keep(i+1, l) {
			continue
		}
		o := row(fmt.Sprintf("%s/%04d", prefix, i+1))
		o.Set("value", jsonx.Str(l))
		o.Set("from", jsonx.Str(p+":"+strconv.Itoa(i+1)))
		rows = append(rows, jsonx.Obj(o))
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: no line a figure reads", p)
	}
	return rows, nil
}

// matching keeps a line any of res matches, and the first line when first.
func matching(first bool, res ...*regexp.Regexp) func(n int, line string) bool {
	return func(n int, l string) bool {
		return first && n == 1 || slices.ContainsFunc(res, func(re *regexp.Regexp) bool { return re.MatchString(l) })
	}
}

// allOrNone is whether every record of ps is in checkout. Presence is judged on the records that leave the public
// tree, those src/kept.txt does not name, as killHistoryRows judges it on its bins files; when kept.txt names them
// all, on all of ps. None of those is false, so no table is built; some of them, or all of them with a kept record
// missing, is an error naming the first missing.
func allOrNone(checkout fs.FS, ps []string) (bool, error) {
	kept, err := keptPaths(checkout)
	if err != nil {
		return false, err
	}
	var leave []string
	for _, p := range ps {
		if !kept(p) {
			leave = append(leave, p)
		}
	}
	if len(leave) == 0 {
		leave = ps
	}
	missing := func(ps []string) []string {
		var out []string
		for _, p := range ps {
			if _, err := fs.Stat(checkout, p); err != nil {
				out = append(out, p)
			}
		}
		return out
	}
	switch m := missing(leave); len(m) {
	case 0:
	case len(leave):
		return false, nil
	default:
		return false, fmt.Errorf("%s: missing, while %d other records of the table are there", m[0], len(leave)-len(m))
	}
	if m := missing(ps); len(m) > 0 {
		return false, fmt.Errorf("%s: missing, a kept record, while the table's other records are there", m[0])
	}
	return true, nil
}

// keptPaths is whether a file is part of a record src/kept.txt names: the record's path itself, a file under a
// directory record, or a file a glob record matches. A checkout without kept.txt keeps none.
func keptPaths(checkout fs.FS) (func(p string) bool, error) {
	lines, err := srcLines(checkout, DocsDir+"/"+TablesDir+"/"+SrcDir+"/"+srcKept)
	if err != nil {
		return nil, err
	}
	var recs []string
	for _, l := range lines {
		rec, _, _ := strings.Cut(l.text, "\t")
		recs = append(recs, rec)
	}
	return func(p string) bool {
		for _, r := range recs {
			if ok, _ := path.Match(r, p); ok || p == r || strings.HasSuffix(r, "/") && strings.HasPrefix(p, r) {
				return true
			}
		}
		return false
	}, nil
}

// fits refuses a record whose last line or place, n, takes more than digits digits: its rows' keys hold n at that
// fixed width, and the table sorts keys in bytes, so a wider n would sort out of place and join in the wrong order.
func fits(p string, n, digits int) error {
	if len(strconv.Itoa(n)) > digits {
		return fmt.Errorf("%s: %d lines or places, past the %d digits of its rows' keys", p, n, digits)
	}
	return nil
}

// valueRow is a row keyed key of v, as recorded, and from, the record it was cut from: the shape of every row of a
// figure table cut from lines (lineRows), JSON Lines (jsonLineRows) or a JSON file's values.
func valueRow(key string, v jsonx.Value, from string) jsonx.Value {
	o := row(key)
	o.Set("value", v)
	o.Set("from", jsonx.Str(from))
	return jsonx.Obj(o)
}

// b2Rows are the rows a figure of boot 2 reads of its rauns, labels, besides their rauns rows: each raun's step file
// lines that hold its command and its fault line, keyed R-<label>.txt/<line>, and, when its JSON records a self-test,
// a row <run_id>/self_test of whether it passed. Each raun is read whole from its files first (readB2), so a row is cut
// only from a raun its figure can read. With samplers, the toxicities panel's rows too: the batch log's lines of the
// catalog copies, keyed batch-log.txt/<line>, which must read back the inject of each raun that ran from a copy, and
// each sampler read in a raun's hold, keyed pe-<label>.jsonl/<line>, with the members of the line the panel reads. A
// checkout with none of the rauns' files builds none.
func b2Rows(checkout fs.FS, labels []string, samplers bool) ([]jsonx.Value, bool, error) {
	var ps []string
	for _, l := range labels {
		ps = append(ps, b2JSON(l), b2Step(l))
		if samplers {
			ps = append(ps, b2Sampler(l))
		}
	}
	if samplers {
		ps = append(ps, boot2Log)
	}
	if ok, err := allOrNone(checkout, ps); !ok {
		return nil, false, err
	}
	var rows []jsonx.Value
	var log string
	if samplers {
		var err error
		if log, err = readText(checkout, boot2Log); err != nil {
			return nil, false, err
		}
		lr, err := lineRows(checkout, boot2Log, b2LogRows, func(_ int, l string) bool {
			return strings.Contains(l, "] catalog copy ")
		})
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, lr...)
	}
	for _, l := range labels {
		r, err := readB2(checkout, l)
		if err != nil {
			return nil, false, err
		}
		sr, err := lineRows(checkout, b2Step(l), b2StepRows(l), matching(true, stepFault))
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, sr...)
		if r.selfTest.Kind() == jsonx.KindObject {
			rows = append(rows, valueRow(r.id+"/self_test/passed", at(r.selfTest, "passed"), b2JSON(l)))
		}
		if !samplers {
			continue
		}
		if _, err := injectOf(checkout, log, boot2Log, r); err != nil {
			return nil, false, err
		}
		p := b2Sampler(l)
		vs, err := readJSONLines(checkout, p)
		if err != nil {
			return nil, false, err
		}
		if n, _ := countLines(checkout, p); n != len(vs) {
			return nil, false, fmt.Errorf("%s: %d lines, %d reads; want a read a line", p, n, len(vs))
		}
		if err := fits(p, len(vs), 4); err != nil {
			return nil, false, err
		}
		_, kept, err := samplesOf(vs, p, r)
		if err != nil {
			return nil, false, err
		}
		for _, i := range kept {
			v, err := toxicRow(vs[i], r)
			if err != nil {
				return nil, false, fmt.Errorf("%s:%d: %w", p, i+1, err)
			}
			rows = append(rows, valueRow(fmt.Sprintf("%s/%04d", b2SamplerRows(l), i+1), v, p+":"+strconv.Itoa(i+1)))
		}
	}
	return rows, true, nil
}

// toxicRow is a sampler's read v of raun r as fig-toxic-fuse keeps it: its at, a time of day, as at_s, its seconds
// after the raun's inject (sampleAt), in at's place (timeofday.go).
func toxicRow(v jsonx.Value, r b2Raun) (jsonx.Value, error) {
	t, err := sampleAt(v, r)
	if err != nil {
		return jsonx.Null(), err
	}
	o, _ := v.Obj()
	n := jsonx.NewObject()
	for i := range o.Len() {
		k, x := o.Member(i)
		if k == "at" {
			n.Set("at_s", jsonx.Float(t))
			continue
		}
		n.Set(k, x)
	}
	return jsonx.Obj(n), nil
}

// countLines is how many lines the text file p holds, a final newline ending the last.
func countLines(checkout fs.FS, p string) (int, error) {
	text, err := readText(checkout, p)
	if err != nil {
		return 0, err
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1, nil
}

// restoreTimingRows are fig-restore-timing's rows: the lines of boot 9's step files that restore-timing reads, keyed
// <step>.txt/<line>: each cold boot's times, snapshot and exit; R1's reading of the snapshot and each restore's
// head and times; each copy's command, stamps and exit. The figure reads the lines as it read the files, so -tables
// reads the files first, each checked against readings.json as the figure checks the lines against the readings table.
// A checkout with none of the step files builds none.
func restoreTimingRows(checkout fs.FS) ([]jsonx.Value, bool, error) {
	steps := append(append(slices.Clone(coldSteps), restoreStep), copySteps...)
	var ps []string
	for _, st := range steps {
		ps = append(ps, microvmPack.step(st))
	}
	if ok, err := allOrNone(checkout, append(ps, microvmPack.readings())); !ok {
		return nil, false, err
	}
	tx, look := fileTexts(checkout), fileReadings(checkout, microvmPack.readings())
	for i, st := range coldSteps {
		if _, err := readCold(tx, look, st, i+1); err != nil {
			return nil, false, err
		}
	}
	if _, _, err := readRestores(tx, look); err != nil {
		return nil, false, err
	}
	if _, err := readCopies(tx, look); err != nil {
		return nil, false, err
	}
	cold := matching(false, coldCopy, coldSSH, coldDockerd, coldUp, warmChecks, coldFroze, coldPaused, coldCreate,
		coldSnap, stepExit0)
	restores := matching(false, restoreCache, restoreFrozen, restoreHead, restoreDisk, restoreLoad, restoreSSH,
		restoreBehnd, restoreSet, restoreThaw, restoreAfter, restoreCall, warmChecks, stepExit0)
	copies := matching(true, copyStamps, stepExit0)
	var rows []jsonx.Value
	for i, st := range steps {
		keep := copies
		switch {
		case slices.Contains(coldSteps, st):
			keep = cold
		case st == restoreStep:
			keep = restores
		}
		lr, err := lineRows(checkout, ps[i], st+".txt", keep)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, lr...)
	}
	return rows, true, nil
}

// clockStepRows are fig-clock-step-timers' rows: the lines of batch E's files that clock-step-timers reads, keyed
// <file>/<line>: each timer's JSON line and exit, each step's and set's date command and exit, and the transcript's
// line of the step's size. -tables reads the files first, as the figure reads the lines. A checkout with none of the
// files builds none.
func clockStepRows(checkout fs.FS) ([]jsonx.Value, bool, error) {
	timers := append([]string{timerControl, timerStep}, timerSets...)
	clocks := []string{"T2-step", "T2-set", "T3-step", "T3-set"}
	ps := []string{timerPack.transcript}
	for _, st := range append(slices.Clone(timers), clocks...) {
		ps = append(ps, timerPack.step(st))
	}
	if ok, err := allOrNone(checkout, ps); !ok {
		return nil, false, err
	}
	if _, err := readClockRun(fileTexts(checkout), timerPack.dir+"steps"); err != nil {
		return nil, false, err
	}
	var rows []jsonx.Value
	for _, p := range ps {
		keep := matching(false, clockCmd, stepExit0)
		switch {
		case p == timerPack.transcript:
			keep = matching(false, stepSize)
		case slices.Contains(timers, strings.TrimSuffix(path.Base(p), ".txt")):
			keep = matching(false, timerJSON, stepExit0)
		}
		lr, err := lineRows(checkout, p, path.Base(p), keep)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, lr...)
	}
	return rows, true, nil
}

// adoptedTableRows are fig-adopted's rows of boot 4's workflow gates, each gate read whole from its files first
// (readAdopted): its gate.json's driver and workflow_id, keyed <gate>/gate.json; each timeline and poll line, as
// recorded, <gate>/timeline.jsonl/<line> and <gate>/poll.jsonl/<line>; and for a KILL, the next worker's step file's
// replay and Started Worker lines, <gate>/<step file>/<line>, and each activity's attempt, <gate>/<attempts
// file>/<place, from 001>. Its verdict is its gates row's and the KILL's reading boot 4's readings table's. A checkout
// with none of the files builds none.
func adoptedTableRows(checkout fs.FS) ([]jsonx.Value, bool, error) {
	var ps []string
	for _, g := range adoptedGates {
		dir := "gate/results/" + g.gate + "/"
		ps = append(ps, dir+"gate.json", dir+"timeline.jsonl", dir+"verdict.json", dir+"poll.jsonl")
		if g.kill != "" {
			ps = append(ps, g.worker, g.attempts)
		}
	}
	if ok, err := allOrNone(checkout, append(ps, boot4Readings)); !ok {
		return nil, false, err
	}
	var rows []jsonx.Value
	for _, g := range adoptedGates {
		if _, err := readAdopted(checkout, g); err != nil {
			return nil, false, err
		}
		dir := "gate/results/" + g.gate + "/"
		gv, err := readJSON(checkout, dir+"gate.json")
		if err != nil {
			return nil, false, err
		}
		for _, k := range []string{"driver", "workflow_id"} {
			rows = append(rows, valueRow(adoptedRows(g.gate, "gate.json/"+k), at(gv, k), dir+"gate.json"))
		}
		for _, f := range []string{"timeline.jsonl", "poll.jsonl"} {
			lr, err := jsonLineRows(checkout, dir+f, adoptedRows(g.gate, f))
			if err != nil {
				return nil, false, err
			}
			rows = append(rows, lr...)
		}
		if g.kill == "" {
			continue
		}
		wr, err := lineRows(checkout, g.worker, adoptedRows(g.gate, path.Base(g.worker)), matching(false,
			workerStarted, workerReplayed))
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, wr...)
		v, err := readJSON(checkout, g.attempts)
		if err != nil {
			return nil, false, err
		}
		all, _ := v.Arr()
		if err := fits(g.attempts, len(all), 3); err != nil {
			return nil, false, err
		}
		for i, a := range all {
			rows = append(rows, valueRow(fmt.Sprintf("%s/%03d", adoptedRows(g.gate, path.Base(g.attempts)), i+1), a,
				g.attempts))
		}
	}
	return rows, true, nil
}

// jsonLineRows are the lines of the JSON Lines record p, a row each keyed <prefix>/<its line, four digits>, with
// value, the line's value as recorded, and from, p:<line>. Each line must hold one value.
func jsonLineRows(checkout fs.FS, p, prefix string) ([]jsonx.Value, error) {
	vs, err := readJSONLines(checkout, p)
	if err != nil {
		return nil, err
	}
	if n, _ := countLines(checkout, p); n != len(vs) {
		return nil, fmt.Errorf("%s: %d lines, %d values; want a value a line", p, n, len(vs))
	}
	if err := fits(p, len(vs), 4); err != nil {
		return nil, err
	}
	var rows []jsonx.Value
	for i, v := range vs {
		rows = append(rows, valueRow(fmt.Sprintf("%s/%04d", prefix, i+1), v, p+":"+strconv.Itoa(i+1)))
	}
	return rows, nil
}

// stalenessRows are fig-staleness's rows: the lines of boot 4's F12 step file that the fake cell tests logged and
// staleness reads, keyed F12.txt/<line>, each time of day as its seconds (staleSeconds), the step file read first as
// the figure reads them. A checkout without the
// step file builds none.
func stalenessRows(checkout fs.FS) ([]jsonx.Value, bool, error) {
	if ok, err := allOrNone(checkout, []string{obsTestSteps}); !ok {
		return nil, false, err
	}
	if _, err := readStale(checkout); err != nil {
		return nil, false, err
	}
	text, err := readText(checkout, obsTestSteps)
	if err != nil {
		return nil, false, err
	}
	seconds, err := staleSeconds(strings.Split(text, "\n"))
	if err != nil {
		return nil, false, err
	}
	keep := matching(false, staleOut, staleBack, staleControl, stalePass)
	rows, err := lineRows(checkout, obsTestSteps, path.Base(obsTestSteps), func(n int, _ string) bool {
		return keep(n, seconds[n-1])
	})
	for _, r := range rows { // each line as staleSeconds gives it, its time of day as seconds
		o, _ := r.Obj()
		n, _ := strconv.Atoi(path.Base(str(at(r, "id"))))
		o.Set("value", jsonx.Str(seconds[n-1]))
	}
	return rows, err == nil, err
}

// cancelRows are fig-cancel-path's rows of boot 4's two cancels, each read whole from its files first (readCancel):
// the transcript's headings of the cancels' steps, keyed 2026-10-24-boot4.txt/<line>; each event the batch kept,
// <events file>/<place, from 001>, as recorded; and each timeline line, as recorded, <timeline file>/<line>. A
// checkout with none of the files that leave the public tree builds none (allOrNone).
func cancelRows(checkout fs.FS) ([]jsonx.Value, bool, error) {
	ps := []string{boot4Transcript}
	for _, c := range boot4Cancels {
		ps = append(ps, cancelEvents(c.step), cancelTimeline(c.step, c.gate))
	}
	if ok, err := allOrNone(checkout, ps); !ok {
		return nil, false, err
	}
	text, err := readText(checkout, boot4Transcript)
	if err != nil {
		return nil, false, err
	}
	modes, err := cancelModes(text, boot4Transcript)
	if err != nil {
		return nil, false, err
	}
	rows, err := lineRows(checkout, boot4Transcript, path.Base(boot4Transcript), matching(false, cancelHeading))
	if err != nil {
		return nil, false, err
	}
	for _, c := range boot4Cancels {
		mode, ok := modes[c.step]
		if !ok {
			return nil, false, fmt.Errorf("%s: no heading of %s naming its WaitForCancellation", boot4Transcript,
				c.step)
		}
		if _, err := readCancel(checkout, c.step, c.gate, mode); err != nil {
			return nil, false, err
		}
		ev := cancelEvents(c.step)
		v, err := readJSON(checkout, ev)
		if err != nil {
			return nil, false, err
		}
		all, _ := v.Arr()
		if err := fits(ev, len(all), 3); err != nil {
			return nil, false, err
		}
		for i, e := range all {
			rows = append(rows, valueRow(fmt.Sprintf("%s/%03d", path.Base(ev), i+1), e, ev))
		}
		tl := cancelTimeline(c.step, c.gate)
		lr, err := jsonLineRows(checkout, tl, path.Base(tl))
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, lr...)
	}
	return rows, true, nil
}
