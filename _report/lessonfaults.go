package report

// lessonfaults.go draws the figures boot 2's pack holds (cells/results/2026-10-23-boot2.txt): throttle-frontend's edit
// landing and lifting (stop 6) and one stuck shard (stop 13), each a lane a raun, and the panel under toxic-fuse's
// diagram (stop 11), what a sampler read beside each persistence-errors raun at each toxicity. Each reads its rauns'
// rows, one by one, never a glob, and its figure table, fig-throttle-landing, fig-stuck-shard or fig-toxic-fuse: the
// lines of each raun's step file that record the hold and the budget it ran with, which report -tables cuts from
// the step files after reading each raun whole from its files (readB2); the panel's table holds the batch log's lines
// of its catalog copies and the sampler's reads in each hold too. Each raun's host is src/hosts.tsv's.

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Boot 2's pack, and its batch log, which records what each catalog copy a raun ran from reads.
const (
	boot2Pack    = "cells/results/2026-10-23-boot2/"
	boot2Log     = boot2Pack + "batch-log.txt"
	faultCatalog = "faults/catalog.yaml"
	faultGen     = "_report/lessonfaults.go"
)

// Boot 2's figure tables, cut by -tables (figtables.go).
const (
	throttleTable = "fig-throttle-landing"
	stuckTable    = "fig-stuck-shard"
	toxicTable    = "fig-toxic-fuse"
)

// throttleRauns are R8's three rauns of throttle-frontend; stuckRauns are R6's three of stuck-shard and R7's
// stuck-shard-norestore, its self-test; toxicRauns are R3's three of persistence-errors at the committed toxicity and
// R4's three from catalog copies, each with the sampler beside it (cells/results/2026-10-23-boot2.txt, R3 to R8).
var (
	throttleRauns = []string{"throttle-frontend-1", "throttle-frontend-2", "throttle-frontend-3"}
	stuckRauns    = []string{"stuck-shard-1", "stuck-shard-2", "stuck-shard-3", "stuck-shard-norestore-1"}
	toxicRauns    = []string{"persistence-errors-1", "persistence-errors-2", "persistence-errors-3",
		"persistence-errors-t010", "persistence-errors-t030", "persistence-errors-t050"}
)

// The files of boot 2's raun label: its JSON, its step file and its sampler's file.
func b2JSON(label string) string    { return "faults/results/raun-lab2-" + label + ".json" }
func b2Step(label string) string    { return boot2Pack + "steps/R-" + label + ".txt" }
func b2Sampler(label string) string { return boot2Pack + "extra/pe-" + label + ".jsonl" }

// The prefixes of the rows a figure table holds of boot 2's raun label: its step file's lines, its sampler's reads.
func b2StepRows(label string) string    { return "R-" + label + ".txt" }
func b2SamplerRows(label string) string { return "pe-" + label + ".jsonl" }

// b2LogRows is the prefix of the rows of the batch log's lines.
const b2LogRows = "batch-log.txt"

// maybe is a value a raun may not have recorded: OK when it did.
type maybe struct {
	F  float64
	OK bool
}

// b2Raun is one of boot 2's rauns as its JSON and its step file record it. Every stamp is in seconds from the inject.
type b2Raun struct {
	label, id, fault, result, reason, server, cell  string
	catalog                                         string  // the catalog copy it ran from; "" is faults/catalog.yaml
	rate, hold, budget                              float64 // workflows a second; the hold and budget, in seconds
	inject                                          float64 // Unix seconds from a raun JSON, 0 from a rauns row
	wait                                            float64 // remove_wait_s: the remove less the hold's end
	landed, remove, recovered, afterStart, afterEnd maybe
	failed, done, secs                              [4]maybe // by window, before to after
	failedTotal                                     maybe
	v                                               jsonx.Value
	win                                             []string    // where v holds its windows: windows in a row
	selfTest                                        jsonx.Value // the self-test, an object when it ran one
	files                                           []string    // the JSON, then the step file, or their rows
}

// path is the keys of a window's member key in r.v.
func (r b2Raun) path(window, key string) []string { return append(slices.Clone(r.win), window, key) }

var (
	// stepCmd is a step file's first line, the command: a catalog copy's path in CATALOG, the fault, the label.
	stepCmd = regexp.MustCompile(`^\$ (?:env CATALOG=(\S+) )?bin/laug raun (\S+) -run-id lab2-(\S+)$`)
	// stepFault is the raun's own line of what it ran: the fault, the server, the hold and the budget.
	stepFault = regexp.MustCompile(`\] [0-9:]+Z fault (\S+) on cell \S+, server (\S+): .*; steady [0-9]+s ` +
		`hold ([0-9]+)s budget ([0-9]+)s after [0-9]+s;`)
)

// readText is the committed file p of fsys, named in the error.
func readText(fsys fs.FS, p string) (string, error) {
	data, err := fs.ReadFile(fsys, p)
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	return string(data), nil
}

// readB2 reads boot 2's raun label from its files: its JSON and its step file (b2Of), and its self-test.
func readB2(fsys fs.FS, label string) (b2Raun, error) {
	p, sp := b2JSON(label), b2Step(label)
	v, err := readJSON(fsys, p)
	if err != nil {
		return b2Raun{}, err
	}
	text, err := readText(fsys, sp)
	if err != nil {
		return b2Raun{}, err
	}
	r, err := b2Of(label, v, p, nil, text, sp)
	r.selfTest = at(v, "self_test")
	return r, err
}

// rowsB2 reads boot 2's rauns of labels, in order, from their rauns rows and the step file lines figure table steps
// holds (b2Of), each with the self-test row steps holds of it, if any, and each placed on the lab host by
// src/hosts.tsv.
func rowsB2(fsys fs.FS, rauns, steps figTable, labels []string) ([]b2Raun, error) {
	var rs []b2Raun
	for _, label := range labels {
		id := "raun-lab2-" + label
		v, err := rauns.row(id)
		if err != nil {
			return nil, err
		}
		text, err := steps.text(b2StepRows(label))
		if err != nil {
			return nil, err
		}
		r, err := b2Of(label, v, rauns.cite(id), []string{"windows"}, text, steps.cite(b2StepRows(label)+"/"))
		if err != nil {
			return nil, err
		}
		if passed, ok := steps.byKey[id+"/self_test/passed"]; ok {
			o := jsonx.NewObject()
			o.Set("passed", at(passed, "value"))
			r.selfTest = jsonx.Obj(o)
		}
		if err := onLabHost(fsys, id); err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, nil
}

// b2Of reads boot 2's raun label from v, its JSON or its rauns row, named p, whose windows sit under win, then from
// text, its step file's lines, named sp, the step's command and its fault line. The hold the step file names must be
// v's remove less remove_wait_s, when the raun removed the fault.
func b2Of(label string, v jsonx.Value, p string, win []string, text, sp string) (b2Raun, error) {
	var err error
	r := b2Raun{label: label, id: str(at(v, "run_id")), fault: str(at(v, "fault")), result: str(at(v, "result")),
		reason: str(at(v, "reason")), server: str(at(v, "server_tag")), cell: str(at(v, "cell")), v: v, win: win,
		files: []string{p, sp}}
	if r.id != "raun-lab2-"+label || r.fault == "" || r.result == "" || r.server == "" || r.cell == "" {
		return b2Raun{}, fmt.Errorf("%s: not raun-lab2-%s with its fault, result, server_tag and cell", p, label)
	}
	row := isRaunRow(v)
	if row { // a rauns row holds its stamps as seconds after the inject (timeofday.go)
		r.inject = 0
		if str(at(v, "date")) == "" {
			return b2Raun{}, fmt.Errorf("%s: date is not a date", p)
		}
	} else if r.inject, err = need(v, p, "inject_at"); err != nil {
		return b2Raun{}, err
	}
	if r.rate, err = need(v, p, "rate"); err != nil {
		return b2Raun{}, err
	}
	rel := func(k string) maybe {
		if row {
			k += "_s"
		}
		n := stamp(at(v, k))
		return maybe{n.F - r.inject, n.OK}
	}
	r.landed, r.remove, r.recovered = rel("landed_at"), rel("remove_at"), rel("recovered_at")
	r.afterStart, r.afterEnd = rel("after_start"), rel("after_end")
	r.wait = num(at(v, "remove_wait_s")).F
	for i, w := range windowNames {
		f, d, s := num(at(v, "iterations_failed", w)), num(at(v, r.path(w, "workflows_completed")...)),
			num(at(v, r.path(w, "window_seconds")...))
		r.failed[i], r.done[i], r.secs[i] = maybe{f.F, f.OK}, maybe{d.F, d.OK}, maybe{s.F, s.OK}
	}
	t := num(at(v, "iterations_failed", "total"))
	r.failedTotal = maybe{t.F, t.OK}

	lines := strings.Split(text, "\n")
	m := stepCmd.FindStringSubmatch(lines[0])
	if m == nil || m[2] != r.fault || m[3] != label {
		return b2Raun{}, fmt.Errorf("%s: the first line is not bin/laug raun %s -run-id lab2-%s", sp, r.fault, label)
	}
	r.catalog = m[1]
	var found [][]string
	for _, l := range lines {
		if f := stepFault.FindStringSubmatch(l); f != nil {
			found = append(found, f)
		}
	}
	if len(found) != 1 || found[0][1] != r.fault || found[0][2] != r.server {
		return b2Raun{}, fmt.Errorf("%s: want one line of fault %s on server %s, with its hold and budget", sp,
			r.fault, r.server)
	}
	r.hold, _ = strconv.ParseFloat(found[0][3], 64)
	r.budget, _ = strconv.ParseFloat(found[0][4], 64)
	if r.remove.OK && r.remove.F-r.wait != r.hold {
		return b2Raun{}, fmt.Errorf("%s: removed %s s after the inject, %s s after the hold's end, against %s's hold "+
			"of %s s", p, Num(r.remove.F), Num(r.wait), sp, Num(r.hold))
	}
	return r, nil
}

// catalogField is the value of key in fault's row of faults/catalog.yaml, as its line holds it after "key: ". A row is
// its name at an indent of two under faults:, and its lines are those under it, up to the next row.
func catalogField(text, fault, key string) (string, error) {
	in, row := false, ""
	for _, l := range strings.Split(text, "\n") {
		if l == "faults:" {
			in = true
			continue
		}
		t := strings.TrimSpace(l)
		if !in || t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(t, ":") {
			row = strings.TrimSuffix(t, ":")
			continue
		}
		if v, ok := strings.CutPrefix(t, key+": "); ok && row == fault {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: no %s in the row of %s", faultCatalog, key, fault)
}

// injectOf is the inject a raun ran: its row's in faults/catalog.yaml, or what the batch log's lines, text, named
// log, read back from the catalog copy it ran from.
func injectOf(fsys fs.FS, text, log string, r b2Raun) (string, error) {
	if r.catalog == "" {
		text, err := readText(fsys, faultCatalog)
		if err != nil {
			return "", err
		}
		return catalogField(text, r.fault, "inject")
	}
	var found []string
	for _, l := range strings.Split(text, "\n") {
		if _, rest, ok := strings.Cut(l, "] catalog copy "+r.catalog+": "); ok {
			if _, in, ok := strings.Cut(rest, "yq reads: "); ok {
				found = append(found, in)
			}
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("%s: %d lines read back the catalog copy %s, want 1", log, len(found), r.catalog)
	}
	return found[0], nil
}

var toxicityWord = regexp.MustCompile(`(?:^| )toxicity=([0-9.]+)(?: |$)`)

// laneMark is a stamp on a raun's lane, each kind drawn its own way.
type laneMark struct {
	at    float64
	kind  int
	title string
}

// The marks: the inject, a solid line; the remove, a dashed one; a tick above the bar; a diamond above it, the
// shard's "Acquired shard"; the budget, ember; the cleanup, a fine dashed line.
const (
	markInject = iota
	markRemove
	markTick
	markDiamond
	markBudget
	markCleanup
)

// laneBar is a window on a raun's lane: from and to in seconds from the inject, its words in the bar when they fit, and
// its hover title. An open bar's end is not recorded: it is drawn faint, its edge dashed.
type laneBar struct {
	from, to     float64
	fill         Colour
	label, title string
	open         bool
}

// lane is one raun on a lane figure: its run id and verdict at the left, its windows, its marks, and a line of words
// under the bar.
type lane struct {
	id, result, note, words string
	bars                    []laneBar
	marks                   []laneMark
}

const laneTop, laneH, laneBarH = 96.0, 64.0, 20.0

// laneX is the lanes' scale: from 15 s before the inject to the 30 s past the last stamp, from the label column to
// the right margin.
func laneX(last float64) (Scale, []float64) {
	hi := float64(30 * math.Ceil(last/30))
	var ticks []float64
	for k := 0; float64(30*k) <= hi; k++ {
		ticks = append(ticks, float64(30*k))
	}
	return Scale{-15, hi, 236, marginR}, ticks
}

// drawLanes draws lanes from laneTop on x and returns the y under the last.
func drawLanes(s *SVG, x Scale, ticks []float64, lanes []lane) float64 {
	bottom := laneTop + float64(float64(len(lanes))*laneH)
	for _, t := range ticks {
		s.Line(x.At(t), laneTop-14, x.At(t), bottom, Style{Stroke: Silica})
	}
	for i, l := range lanes {
		y := laneTop + float64(float64(i)*laneH) + 10
		s.Group(l.id + ": " + l.result)
		s.Text(marginL, y+14, l.id, Font{Size: 10, Mono: true})
		if textW(l.id, 10, true) > x.R0-marginL-8 {
			s.fail("the run id %q is wider than the label column", l.id)
		}
		word := Moss
		if l.result != "ADVANCE" {
			word = Ember
		}
		s.Text(marginL, y+30, l.result, Font{Size: 11, Bold: true, Colour: word})
		if l.note != "" {
			s.Text(marginL, y+44, l.note, Font{Size: 10})
		}
		for _, b := range l.bars {
			w := x.At(b.to) - x.At(b.from)
			st := Style{Fill: b.fill, Stroke: Deep, Width: 0.5, Title: b.title}
			if b.open {
				st.FillOpacity, st.Dash = 0.45, []float64{3, 2}
			}
			s.Rect(x.At(b.from), y, w, laneBarH, st)
			if textW(b.label, 11, true)+6 < w {
				s.Text(x.At(b.from)+w/2, y+14, b.label, Font{Size: 11, Anchor: AnchorMiddle, Mono: true})
			}
		}
		for _, m := range l.marks {
			mx := x.At(m.at)
			switch m.kind {
			case markInject:
				s.Line(mx, y-6, mx, y+laneBarH+6, Style{Stroke: Basalt, Width: 1.5, Title: m.title})
			case markRemove:
				s.Line(mx, y-8, mx, y+laneBarH+6, Style{Stroke: Basalt, Width: 1.5, Dash: []float64{4, 3},
					Title: m.title})
			case markTick:
				s.Line(mx, y-8, mx, y-1, Style{Stroke: Basalt, Width: 2, Title: m.title})
			case markDiamond:
				s.Polygon([]Point{{mx, y - 10}, {mx + 4, y - 5}, {mx, y}, {mx - 4, y - 5}}, Style{Fill: Deep,
					Title: m.title})
			case markBudget:
				s.Line(mx, y-8, mx, y+laneBarH+6, Style{Stroke: Ember, Width: 1.5, Dash: []float64{4, 3},
					Title: m.title})
			case markCleanup:
				s.Line(mx, y-8, mx, y+laneBarH+6, Style{Stroke: Deep, Width: 1.5, Dash: []float64{1.5, 2},
					Title: m.title})
			}
		}
		s.Text(x.R0, y+laneBarH+17, l.words, Font{Size: 10})
		if textW(l.words, 10, false) > marginR-x.R0 {
			s.fail("the words under %s, %q, are wider than the lane", l.id, l.words)
		}
		s.EndGroup()
	}
	return bottom
}

// sameServer is the server every raun of rs ran, an error when they differ.
func sameServer(rs []b2Raun) (string, error) {
	for _, r := range rs[1:] {
		if r.server != rs[0].server {
			return "", fmt.Errorf("%s ran server %s, %s server %s", r.id, r.server, rs[0].id, rs[0].server)
		}
	}
	return rs[0].server, nil
}

// col is f of each raun of rs.
func col(rs []b2Raun, f func(r b2Raun) float64) []float64 {
	var xs []float64
	for _, r := range rs {
		xs = append(xs, f(r))
	}
	return xs
}

// throttleLanding draws R8's three rauns of throttle-frontend as lanes of their windows against seconds from the edit:
// when the limit landed, when it lifted after the restore, and the workflows each window completed.
func throttleLanding(fsys fs.FS) (drawn, error) {
	ts, err := readFigTables(fsys, "rauns", throttleTable)
	if err != nil {
		return drawn{}, err
	}
	rs, err := rowsB2(fsys, ts[0], ts[1], throttleRauns)
	if err != nil {
		return drawn{}, err
	}
	for _, r := range rs {
		ok := r.fault == "throttle-frontend" && r.landed.OK && r.remove.OK && r.recovered.OK && r.afterStart.OK &&
			r.afterEnd.OK
		for i := range windowNames {
			ok = ok && r.done[i].OK && r.secs[i].OK
		}
		if !ok {
			return drawn{}, fmt.Errorf("%s: not a throttle-frontend raun with every stamp and window", r.files[0])
		}
	}
	server, err := sameServer(rs)
	if err != nil {
		return drawn{}, err
	}
	cat, err := readText(fsys, faultCatalog)
	if err != nil {
		return drawn{}, err
	}
	inject, err := catalogField(cat, "throttle-frontend", "inject")
	if err != nil {
		return drawn{}, err
	}
	edit, ok := strings.CutPrefix(inject, "dynconfig ")
	if !ok {
		return drawn{}, fmt.Errorf("%s: throttle-frontend's inject %q is no dynconfig edit", faultCatalog, inject)
	}
	host := labHostWords
	sources := append(tableSources(ts...), faultCatalog, hostsTSV)
	var limit []float64 // the signal's max, as each raun records it
	for _, r := range rs {
		m, err := need(r.v, r.files[0], "signal_max")
		if err != nil {
			return drawn{}, err
		}
		limit = append(limit, m)
	}

	landed := col(rs, func(r b2Raun) float64 { return r.landed.F })
	back := col(rs, func(r b2Raun) float64 { return r.recovered.F - r.remove.F })
	title := "throttle-frontend: landed " + span(landed) + " s after the edit, back " + span(back) +
		" s after the restore"
	sub := fmt.Sprintf("%d rauns at %s workflows/s on server %s, %s; %s, held %s s", len(rs),
		span(col(rs, func(r b2Raun) float64 { return r.rate })), server, host, edit,
		span(col(rs, func(r b2Raun) float64 { return r.hold })))
	desc := "A lane a raun of faults/catalog.yaml's throttle-frontend on a compose cell, which sets " + edit + " in " +
		"the leased cell's copy of its dynamic config, then restores the copy; across, seconds from the edit, the " +
		"raun's inject_at. The first tick is the signal landing, ResourceExhausted errors a second over 30 s past " +
		span(limit) + "; the first dashed line is the restore, remove_at; the second tick the signal back under it; " +
		"the second dashed line the raun's budget, " + span(col(rs, func(r b2Raun) float64 { return r.budget })) +
		" s after the restore. Each bar is a window holding the workflows it completed in its seconds, left to " +
		"right: during, from the landing to the hold's end; drain, from the restore to the after window; after. The " +
		"before windows completed " + span(col(rs, func(r b2Raun) float64 { return r.done[0].F })) + " in " +
		span(col(rs, func(r b2Raun) float64 { return r.secs[0].F })) + " s; failed client starts over every " +
		"window: " + wholeSpan(col(rs, func(r b2Raun) float64 { return r.failedTotal.F })) + "."

	x, ticks := laneX(slices.Max(col(rs, func(r b2Raun) float64 {
		return math.Max(r.afterEnd.F, r.remove.F+r.budget)
	})))
	var lanes []lane
	for k, r := range rs {
		bar := func(i int, from, to float64, fill Colour) laneBar {
			return laneBar{from, to, fill, whole(r.done[i].F) + " in " + Num(r.secs[i].F) + " s",
				fmt.Sprintf("%s, %s to %s s: %s workflows completed in %s s", windowNames[i], Num(from), Num(to),
					whole(r.done[i].F), Num(r.secs[i].F)), false}
		}
		lanes = append(lanes, lane{id: r.id, result: r.result,
			bars: []laneBar{bar(1, r.landed.F, r.remove.F-r.wait, Warm), bar(2, r.remove.F, r.afterStart.F, Water),
				bar(3, r.afterStart.F, r.afterEnd.F, Silica)},
			marks: []laneMark{{0, markInject, "the edit, inject_at"},
				{r.landed.F, markTick, "landed +" + Num(r.landed.F) + " s"},
				{r.remove.F, markRemove, "the restore, remove_at, +" + Num(r.remove.F) + " s"},
				{r.recovered.F, markTick, "back under " + Num(limit[k]) + " " + Num(r.recovered.F-r.remove.F) +
					" s after the restore"},
				{r.remove.F + r.budget, markBudget, "the budget, " + Num(r.budget) + " s after the restore"}},
			words: fmt.Sprintf("landed +%s s; restored +%s s, back %s s after it, against a %s s budget",
				Num(r.landed.F), Num(r.remove.F), Num(r.recovered.F-r.remove.F), Num(r.budget))})
	}
	height := laneTop + float64(float64(len(lanes))*laneH) + 16 + 40 + 56
	s := lessonFigure("throttle-landing", title, sub, desc, sources, height)
	s.Text(marginL, laneTop-20, "In each bar: workflows completed", Font{Size: 11})
	s.Text(x.At(0), laneTop-20, "edit", Font{Size: 11, Anchor: AnchorMiddle})
	s.Text(x.At(rs[0].remove.F), laneTop-20, "restore", Font{Size: 11, Anchor: AnchorMiddle})
	bottom := drawLanes(s, x, ticks, lanes)
	s.AxisX(x, bottom+16, ticks, "seconds after the edit")
	lessonLegend(s, marginL, height-18, []legendItem{
		{"during: landed to hold's end", swatch(Warm)}, {"drain: restore to after window", swatch(Water)},
		{"after", swatch(Silica)}, {"restore", stroke(Basalt, []float64{4, 3}, 1.5)},
		{"budget", stroke(Ember, []float64{4, 3}, 1.5)},
	})
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// shardFacts are what a shard raun records of its shard: the shard, its range_id before and after the bump, its
// refused acquires in the hold, its "Acquired shard" after the remove, and the cleanup's restore.
type shardFacts struct {
	shard, before, bumped, refused string
	acquired, cleanup              maybe
	selfTest                       string // "passed" or "failed" for the self-test; "" for the others
}

// readShard reads r's shard facts from its mechanisms, shards_acquired and self-test.
func readShard(r b2Raun) (shardFacts, error) {
	p := r.files[0]
	ms, _ := at(r.v, "mechanisms").Arr()
	var f shardFacts
	for _, m := range ms {
		switch str(at(m, "step")) {
		case "inject":
			sh, b, a := num(at(m, "shard")), num(at(m, "range_id_before")), num(at(m, "range_id_bumped"))
			if !sh.OK || !b.OK || !a.OK {
				return shardFacts{}, fmt.Errorf("%s: an inject mechanism without its shard and range ids", p)
			}
			f.shard, f.before, f.bumped = whole(sh.F), whole(b.F), whole(a.F)
		case "cleanup":
			t := num(at(m, "at")) // a raun JSON's Unix time; a rauns row's at_s, after the inject
			if !t.OK {
				t = num(at(m, "at_s"))
			}
			if t.OK && str(at(m, "mechanism")) == "pg restore" {
				f.cleanup = maybe{t.F - r.inject, true}
			}
		}
	}
	if f.shard == "" {
		return shardFacts{}, fmt.Errorf("%s: no inject mechanism", p)
	}
	n := num(at(r.v, "shards_acquired", "held_evidence", f.shard, "Couldn't acquire shard"))
	if !n.OK {
		return shardFacts{}, fmt.Errorf("%s: no refused acquires recorded for shard %s", p, f.shard)
	}
	f.refused = whole(n.F)
	if a := num(at(r.v, "shards_acquired", "acquired_s", f.shard)); a.OK {
		f.acquired = maybe{a.F, true}
	}
	if st := r.selfTest; st.Kind() == jsonx.KindObject {
		passed, ok := at(st, "passed").Bool()
		if !ok {
			return shardFacts{}, fmt.Errorf("%s: a self_test without passed", p)
		}
		f.selfTest = map[bool]string{true: "passed", false: "failed"}[passed]
	}
	return f, nil
}

// stuckReadings are the readings the stuck shard's table compares, before the bump and while it held, each as the
// table names it.
var stuckReadings = []struct{ key, label string }{
	{"workflow_end_to_end_p95_ms", "workflow end-to-end p95, ms"},
	{"workflow_task_schedule_to_start_p99_ms", "workflow task schedule-to-start p99, ms"},
	{"activity_schedule_to_start_p99_ms", "activity schedule-to-start p99, ms"},
	{"persistence_latency_p99_ms", "persistence p99, ms"},
	{"frontend_request_p99_ms", "frontend p99, polls left out, ms"},
}

// stuckShard draws R6's three rauns of stuck-shard and R7's self-test as lanes of their windows against seconds from
// the bump, then the readings of the before and during windows beside their failed starts.
func stuckShard(fsys fs.FS) (drawn, error) {
	ts, err := readFigTables(fsys, "rauns", stuckTable)
	if err != nil {
		return drawn{}, err
	}
	rs, err := rowsB2(fsys, ts[0], ts[1], stuckRauns)
	if err != nil {
		return drawn{}, err
	}
	var facts []shardFacts
	var restored []b2Raun
	var failedHeld, back []float64 // the restored rauns' failed starts in the hold, and seconds to "Acquired shard"
	for _, r := range rs {
		f, err := readShard(r)
		if err != nil {
			return drawn{}, err
		}
		ok := r.landed.OK && r.remove.OK && r.failed[0].OK && r.failed[1].OK && r.failed[2].OK &&
			r.done[0].OK && r.secs[0].OK && r.done[1].OK && r.secs[1].OK
		switch r.fault {
		case "stuck-shard":
			ok = ok && f.acquired.OK && r.afterStart.OK && r.afterEnd.OK && r.failed[3].OK && f.selfTest == ""
			restored = append(restored, r)
			failedHeld, back = append(failedHeld, r.failed[1].F), append(back, f.acquired.F)
		case "stuck-shard-norestore":
			ok = ok && !f.acquired.OK && f.cleanup.OK && f.selfTest != ""
		default:
			ok = false
		}
		if !ok {
			return drawn{}, fmt.Errorf("%s: not a stuck-shard raun, or its self-test, with every stamp it needs",
				r.files[0])
		}
		facts = append(facts, f)
	}
	if len(restored) == 0 {
		return drawn{}, errors.New("no stuck-shard raun with its restore")
	}
	server, err := sameServer(rs)
	if err != nil {
		return drawn{}, err
	}
	host := labHostWords
	sources := append(tableSources(ts...), hostsTSV)

	held := col(restored, func(r b2Raun) float64 { return r.hold })
	title := "One stuck shard: " + wholeSpan(failedHeld) + " failed starts in the hold, back " + span(back) +
		" s after the restore"
	sub := fmt.Sprintf("%d rauns and a self-test at %s workflows/s on server %s, %s", len(restored),
		span(col(rs, func(r b2Raun) float64 { return r.rate })), server, host)
	budget := span(col(rs, func(r b2Raun) float64 { return r.budget }))
	desc := "A lane a raun of faults/catalog.yaml's stuck-shard on a compose cell, which moves one shard's range_id " +
		"column in the cell's Postgres past the range id the shard holds and puts it back at removal, held " +
		span(held) + " s; the last lane is stuck-shard-norestore, whose removal does nothing. Across, seconds from " +
		"the bump, the raun's inject_at. The first tick is the signal landing, the shard's closes counted since " +
		"just before the bump; the first dashed line is the removal, remove_at; the diamond the shard's \"Acquired " +
		"shard\" after it; the second dashed line the raun's budget, " + budget + " s after the removal; the fine " +
		"dashed line the self-test's cleanup restore. Each bar is a window holding its failed client starts, left to " +
		"right: during, from the landing to the hold's end; drain, from the removal to the after window; after. The " +
		"self-test's drain has no recorded end and is drawn to the cleanup, faint. Below, the readings of the " +
		"before and during windows of all " + strconv.Itoa(len(rs)) + " rauns, the range over them, " +
		"completions a second computed from each window's count and seconds."

	last := 0.0
	for i, r := range rs {
		last = math.Max(last, math.Max(r.afterEnd.F, math.Max(r.remove.F+r.budget, facts[i].cleanup.F)))
	}
	x, ticks := laneX(last)
	var lanes []lane
	for i, r := range rs {
		f := facts[i]
		bar := func(w int, from, to float64, fill Colour) laneBar {
			return laneBar{from, to, fill, whole(r.failed[w].F), fmt.Sprintf("%s, %s to %s s: %s",
				windowNames[w], Num(from), Num(to), failedStarts(r.failed[w].F)), false}
		}
		l := lane{id: r.id, result: r.result,
			bars: []laneBar{bar(1, r.landed.F, r.remove.F-r.wait, Warm)},
			marks: []laneMark{{0, markInject, "the bump, inject_at: shard " + f.shard + "'s range_id " + f.before +
				" to " + f.bumped}, {r.landed.F, markTick, "landed +" + Num(r.landed.F) + " s"}}}
		if f.selfTest == "" {
			l.bars = append(l.bars, bar(2, r.remove.F, r.afterStart.F, Water), bar(3, r.afterStart.F, r.afterEnd.F,
				Silica))
			l.marks = append(l.marks, laneMark{r.remove.F, markRemove, "the restore, remove_at, +" + Num(r.remove.F) +
				" s"}, laneMark{r.remove.F + f.acquired.F, markDiamond, "\"Acquired shard\" " + Num(f.acquired.F) +
				" s after the restore"})
			l.words = fmt.Sprintf("shard %s: range_id %s to %s, restored +%s s; %s refused acquires; back %s s after",
				f.shard, f.before, f.bumped, Num(r.remove.F), f.refused, Num(f.acquired.F))
		} else {
			d := bar(2, r.remove.F, f.cleanup.F, Water)
			d.open, d.title = true, fmt.Sprintf("drain, from the removal at %s s, its end not recorded, drawn to the "+
				"cleanup's restore at %s s: %s", Num(r.remove.F), Num(f.cleanup.F), failedStarts(r.failed[2].F))
			l.bars = append(l.bars, d)
			l.marks = append(l.marks, laneMark{r.remove.F, markRemove, "the removal, remove_at, +" + Num(r.remove.F) +
				" s: nothing done"}, laneMark{f.cleanup.F, markCleanup, "the cleanup's restore, +" + Num(f.cleanup.F) +
				" s"})
			reason, _, _ := strings.Cut(r.reason, ":")
			l.note = "self-test " + f.selfTest
			l.words = fmt.Sprintf("shard %s: range_id %s to %s, no restore; %s refused acquires; %s", f.shard, f.before,
				f.bumped, f.refused, reason)
		}
		l.marks = append(l.marks, laneMark{r.remove.F + r.budget, markBudget, "the budget, " + Num(r.budget) +
			" s after the removal"})
		lanes = append(lanes, l)
	}

	// The table: each reading's range over the rauns, before and during.
	type row struct{ label, before, during string }
	var rows []row
	for _, rd := range stuckReadings {
		var b, d []float64
		for _, r := range rs {
			for i, to := range []*[]float64{&b, &d} {
				v, err := need(r.v, r.files[0], r.path(windowNames[i], rd.key)...)
				if err != nil {
					return drawn{}, err
				}
				*to = append(*to, v)
			}
		}
		rows = append(rows, row{rd.label, spanTo(b, 1), spanTo(d, 1)})
	}
	var eb, ed []float64
	for _, r := range rs {
		for i, to := range []*[]float64{&eb, &ed} {
			v, err := need(r.v, r.files[0], r.path(windowNames[i], "service_error_rate")...)
			if err != nil {
				return drawn{}, err
			}
			*to = append(*to, float64(v*100))
		}
	}
	rows = append(rows, row{"service errors, % of requests", span(eb), span(ed)},
		row{"workflows completed a second", spanTo(col(rs, func(r b2Raun) float64 { return r.done[0].F / r.secs[0].F }),
			2), spanTo(col(rs, func(r b2Raun) float64 { return r.done[1].F / r.secs[1].F }), 2)},
		row{"failed client starts", wholeSpan(col(rs, func(r b2Raun) float64 { return r.failed[0].F })),
			wholeSpan(col(rs, func(r b2Raun) float64 { return r.failed[1].F }))})

	const rowH, colB, colD = 18.0, 420.0, 600.0
	lanesBottom := laneTop + float64(float64(len(lanes))*laneH)
	tableTop := lanesBottom + 16 + 40 + 34
	height := tableTop + float64(float64(len(rows)+1)*rowH) + 50
	s := lessonFigure("stuck-shard", title, sub, desc, sources, height)
	s.Text(marginL, laneTop-20, "In each bar: failed client starts", Font{Size: 11})
	s.Text(x.At(0), laneTop-20, "bump", Font{Size: 11, Anchor: AnchorMiddle})
	s.Text(x.At(rs[0].remove.F), laneTop-20, "removal", Font{Size: 11, Anchor: AnchorMiddle})
	bottom := drawLanes(s, x, ticks, lanes)
	s.AxisX(x, bottom+16, ticks, "seconds after the bump")
	s.Text(marginL, tableTop-12, fmt.Sprintf("The %d rauns' readings, before the bump and while the shard was held",
		len(rs)), Font{Size: 12, Bold: true})
	s.Group("the readings before and during")
	s.Text(colB, tableTop+12, "before window", Font{Size: 11, Bold: true})
	s.Text(colD, tableTop+12, "during window", Font{Size: 11, Bold: true})
	for i, r := range rows {
		ry := tableTop + float64(float64(i+1)*rowH)
		s.Line(marginL, ry, marginR, ry, Style{Stroke: Silica})
		s.Text(marginL+4, ry+13, r.label, Font{Size: 11})
		s.Text(colB, ry+13, r.before, Font{Size: 11, Mono: true})
		s.Text(colD, ry+13, r.during, Font{Size: 11, Mono: true})
		if textW(r.label, 11, false)+8 > colB-marginL || textW(r.before, 11, true)+8 > colD-colB ||
			textW(r.during, 11, true) > marginR-colD {
			s.fail("the table's row %q does not fit", r.label)
		}
	}
	s.EndGroup()
	lessonLegend(s, marginL, height-18, []legendItem{
		{"during: landed to hold's end", swatch(Warm)}, {"drain: removal to after", swatch(Water)},
		{"after", swatch(Silica)},
		{"removal", stroke(Basalt, []float64{4, 3}, 1.5)}, {"budget", stroke(Ember, []float64{4, 3}, 1.5)},
		{"acquired", func(s *SVG, x, y float64) {
			s.Polygon([]Point{{x + 9, y - 5}, {x + 13, y}, {x + 9, y + 5}, {x + 5, y}}, Style{Fill: Deep})
		}},
	})
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// samplerRead is one of the sampler's reads, t seconds from its raun's inject.
type samplerRead struct {
	t, errors, completions, conns float64
	redials, failures             []roleVal // in the file's order
}

// roleVal is a value of one role's series.
type roleVal struct {
	role string
	v    float64
}

// readSamples reads the sampler's file p beside raun r: its reads from the inject to the hold's end (samplesOf).
func readSamples(fsys fs.FS, p string, r b2Raun) ([]samplerRead, error) {
	vs, err := readJSONLines(fsys, p)
	if err != nil {
		return nil, err
	}
	out, _, err := samplesOf(vs, p, r)
	return out, err
}

// sampleAt is a sampler's read v in seconds after raun r's inject: its at, a time, less the inject in the file;
// its at_s in a fig-toxic-fuse row, which -tables wrote as those seconds (toxicRow).
func sampleAt(v jsonx.Value, r b2Raun) (float64, error) {
	if n := num(at(v, "at_s")); n.OK {
		return n.F - r.inject, nil
	}
	when, err := time.Parse(time.RFC3339, str(at(v, "at")))
	if err != nil {
		return 0, err
	}
	return float64(when.Unix()) - r.inject, nil
}

// samplesOf reads the sampler's reads vs beside raun r, a line each of the file or a row each of its figure table,
// named p: those from the inject to the hold's end, and their places in vs. Each read must be of r's cell, and each
// there must answer every query.
func samplesOf(vs []jsonx.Value, p string, r b2Raun) ([]samplerRead, []int, error) {
	one := func(v jsonx.Value, k string) (float64, bool) {
		a, ok := at(v, k).Arr()
		if !ok || len(a) != 1 {
			return 0, false
		}
		n := num(at(a[0], "v"))
		return n.F, n.OK
	}
	roles := func(v jsonx.Value, k string) ([]roleVal, bool) {
		a, ok := at(v, k).Arr()
		if !ok {
			return nil, false
		}
		var out []roleVal
		for _, e := range a {
			n := num(at(e, "v"))
			role := str(at(e, "m", "service_name"))
			if !n.OK || role == "" {
				return nil, false
			}
			out = append(out, roleVal{role, n.F})
		}
		return out, true
	}
	var out []samplerRead
	var kept []int
	for i, v := range vs {
		t, err := sampleAt(v, r)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: read %d: %w", p, i+1, err)
		}
		if c := str(at(v, "cell")); c != r.cell {
			return nil, nil, fmt.Errorf("%s: read %d is of cell %q, the raun's is %s", p, i+1, c, r.cell)
		}
		if t < 0 || t >= r.hold {
			continue
		}
		var s samplerRead
		var ok1, ok2, ok3, ok4 bool
		s.t = t
		s.errors, ok1 = one(v, "errors_per_s")
		s.completions, ok2 = one(v, "completions_per_s")
		s.redials, ok3 = roles(v, "redials")
		s.failures, ok4 = roles(v, "redial_failures")
		c := num(at(v, "pg_connections"))
		s.conns = c.F
		if !ok1 || !ok2 || !ok3 || !ok4 || !c.OK {
			return nil, nil, fmt.Errorf("%s: read %d, %s s after the inject, did not answer every query", p, i+1,
				Num(t))
		}
		out, kept = append(out, s), append(kept, i)
	}
	if len(out) < 2 {
		return nil, nil, fmt.Errorf("%s: %d reads in the hold, want 2 or more", p, len(out))
	}
	return out, kept, nil
}

// peakRole is the highest value of the series of every read and the first role that reached it; seen is false when
// no read held any series, and roles counts the roles that answered.
func peakRole(ss []samplerRead, f func(s samplerRead) []roleVal) (peak float64, role string, roles int, seen bool) {
	var names []string
	for _, s := range ss {
		for _, rv := range f(s) {
			if !slices.Contains(names, rv.role) {
				names = append(names, rv.role)
			}
			if !seen || rv.v > peak {
				peak, role, seen = rv.v, rv.role, true
			}
		}
	}
	return peak, role, len(names), seen
}

// toxicitiesPanel is a table, a column a persistence-errors raun of boot 2 by its toxicity: its verdict and when it
// landed, what the sampler beside it read in the hold, each the range over the hold's reads, and its failed starts.
// Each raun is read from its rauns row and fig-toxic-fuse's rows: its step file's lines, the sampler's reads in its
// hold, and the batch log's lines of the catalog copies.
func toxicitiesPanel(fsys fs.FS, _ string) ([]string, func(s *SVG, x, y, w, h float64), error) {
	ts, err := readFigTables(fsys, "rauns", toxicTable)
	if err != nil {
		return nil, nil, err
	}
	toxic := ts[1]
	rs, err := rowsB2(fsys, ts[0], toxic, toxicRauns)
	if err != nil {
		return nil, nil, err
	}
	log, err := toxic.text(b2LogRows)
	if err != nil {
		return nil, nil, err
	}
	type column struct {
		toxicity, end string
		cells         []string
		result        string
	}
	var cols []column
	var holds, reads, gaps []float64
	for _, r := range rs {
		if r.fault != "persistence-errors" || !r.failedTotal.OK {
			return nil, nil, fmt.Errorf("%s: not a persistence-errors raun with its failed starts", r.files[0])
		}
		in, err := injectOf(fsys, log, toxic.cite(b2LogRows+"/"), r)
		if err != nil {
			return nil, nil, err
		}
		m := toxicityWord.FindStringSubmatch(in)
		if m == nil {
			return nil, nil, fmt.Errorf("%s: the inject %q names no toxicity", r.files[0], in)
		}
		sp := b2SamplerRows(r.label)
		ss, _, err := samplesOf(toxic.values(sp), toxic.cite(sp+"/"), r)
		if err != nil {
			return nil, nil, err
		}
		landed := "not in " + Num(r.hold) + " s"
		if r.landed.OK {
			landed = "+" + Num(r.landed.F) + " s"
		}
		get := func(f func(s samplerRead) float64) []float64 {
			var xs []float64
			for _, s := range ss {
				xs = append(xs, f(s))
			}
			return xs
		}
		peak := func(f func(s samplerRead) []roleVal) (string, string) {
			p, role, n, seen := peakRole(ss, f)
			switch {
			case !seen:
				return "no series", ""
			case p == 0:
				return "0", fmt.Sprintf("%d roles at 0", n)
			}
			return Num(p), role
		}
		rp, rr := peak(func(s samplerRead) []roleVal { return s.redials })
		fp, fr := peak(func(s samplerRead) []roleVal { return s.failures })
		cols = append(cols, column{toxicity: m[1], end: strings.TrimPrefix(r.label, "persistence-errors"),
			result: r.result, cells: []string{r.result, landed,
				span(get(func(s samplerRead) float64 { return s.errors })),
				span(get(func(s samplerRead) float64 { return s.completions })), rp, rr, fp, fr,
				span(get(func(s samplerRead) float64 { return s.conns })), whole(r.failedTotal.F)}})
		holds, reads = append(holds, r.hold), append(reads, float64(len(ss)))
		for i := 1; i < len(ss); i++ {
			gaps = append(gaps, ss[i].t-ss[i-1].t)
		}
	}
	host := labHostWords
	sources := append(tableSources(ts...), faultCatalog, hostsTSV)
	labels := []string{"verdict", "landed, from the inject", "persistence errors a second",
		"workflows completed a second", "refresh attempts in 1 m, peak", "the role at that peak",
		"refresh failures in 1 m, peak", "the role at that peak", "connections to the database",
		"failed client starts"}
	heading := fmt.Sprintf("Boot 2 on %s: the sampler in each %s s hold, %s reads a raun, %s s apart", host,
		span(holds), wholeSpan(reads), wholeSpan(gaps))
	draw := func(s *SVG, x, y, w, h float64) {
		const labelW, rowH, headH = 190.0, 16.0, 60.0
		colW := (w - labelW) / float64(len(cols))
		s.Group("what the sampler read, by toxicity")
		s.Text(x+4, y+14, heading, Font{Size: 11, Bold: true})
		s.Text(x+4, y+38, "toxicity", Font{Size: 11, Bold: true})
		s.Text(x+4, y+52, "raun-lab2-persistence-errors", Font{Size: 10, Mono: true})
		for j, c := range cols {
			cx := x + labelW + float64(float64(j)*colW)
			s.Text(cx, y+38, c.toxicity, Font{Size: 11, Bold: true, Mono: true})
			s.Text(cx, y+52, c.end, Font{Size: 10, Mono: true})
		}
		for i, l := range labels {
			ry := y + headH + float64(float64(i)*rowH)
			s.Line(x, ry-4, x+w, ry-4, Style{Stroke: Silica})
			s.Text(x+4, ry+8, l, Font{Size: 11})
			if textW(l, 11, false)+8 > labelW {
				s.fail("the toxicities panel's label %q is wider than its column", l)
			}
			for j, c := range cols {
				if c.cells[i] == "" {
					continue // a role where no read held the series
				}
				f := Font{Size: 10, Mono: true}
				if i == 0 {
					f = Font{Size: 10, Bold: true, Colour: Moss}
					if c.result != "ADVANCE" {
						f.Colour = Ember
					}
				}
				s.Text(x+labelW+float64(float64(j)*colW), ry+8, c.cells[i], f)
				if textW(c.cells[i], 10, true)+8 > colW {
					s.fail("the toxicities panel's cell %q is wider than its column", c.cells[i])
				}
			}
		}
		s.EndGroup()
		if textW(heading, 11, true)+8 > w || headH+float64(float64(len(labels))*rowH) > h {
			s.fail("the toxicities panel does not fit %v by %v", w, h)
		}
	}
	return sources, draw, nil
}
