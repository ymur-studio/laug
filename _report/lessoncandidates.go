package report

// lessoncandidates.go draws the figure boot 3's pack holds
// (docs/tables/records.jsonl#cells/results/2026-10-23-boot3.txt): laug candidate's 14 counted runs (stop 13), each one
// dynamic config key changed on a leased compose cell and a load judged after it, as a row of its outcome, the rows
// that decided its verdict and each judged reading against its limit. It reads each run's rauns row, the row of its
// candidate JSON under faults/results, by name, never a glob; its host from src/hosts.tsv; and boot 3's readings, which
// name a step a run, so the batch's own count of its runs.

import (
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

const (
	candidateGen = "_report/lessoncandidates.go"
	// boot3Readings is the readings table of boot 3's batch, cells/results/2026-10-23-boot3/readings.json.
	boot3Readings = "readings-2026-10-23-boot3"
	// warmupStep is the warm-up's C step in boot 3's readings: it ran, and it is not counted.
	warmupStep = "C-benign-lab3-warmup"
)

// candidateRuns are boot 3's counted runs of laug candidate, by candidate in the batch's order: each candidate's three
// on server 1.31.0, then its run on 1.32.1 where it had one
// (docs/tables/records.jsonl#cells/results/2026-10-23-boot3.txt, C1 to C4). The warm-up, lab3-warmup, is not counted
// (C0).
var candidateRuns = []string{
	"benign-lab3-1", "benign-lab3-2", "benign-lab3-3", "benign-lab3-v1321",
	"read-partitions-lab3-1", "read-partitions-lab3-2", "read-partitions-lab3-3",
	"frontend-starved-lab3-1", "frontend-starved-lab3-2", "frontend-starved-lab3-3",
	"persistence-starved-lab3-1", "persistence-starved-lab3-2", "persistence-starved-lab3-3",
	"persistence-starved-lab3-v1321",
}

// b3Row is the rauns row of boot 3's run label: its candidate JSON's name less .json.
func b3Row(label string) string { return "candidate-" + label }

// candidateCols are the rows a candidate's verdict judges against a limit, in the order its rows record them: the
// reading, the lines of its column's head, and its name in a hover title.
var candidateCols = []struct {
	key   string
	head  []string
	label string
}{
	{"workflow_task_schedule_to_start_p99_ms", []string{"workflow", "task s2s", "p99, ms"},
		"workflow task schedule-to-start p99, ms"},
	{"activity_schedule_to_start_p99_ms", []string{"activity", "s2s", "p99, ms"}, "activity schedule-to-start p99, ms"},
	{"workflow_end_to_end_p95_ms", []string{"workflow", "end-to-end", "p95, ms"}, "workflow end-to-end p95, ms"},
	{"persistence_latency_p99_ms", []string{"persistence", "p99, ms"}, "persistence p99, ms"},
	{"service_error_rate", []string{"service", "error rate"}, "service error rate"},
	{"frontend_request_p99_ms", []string{"frontend", "p99, ms"}, "frontend p99, polls left out, ms"},
	{"workflows_completed", []string{"workflows", "completed"}, "workflows completed"},
	{"omes_exit", []string{"Omes", "exit"}, "Omes exit"},
	{"workflows_not_completed", []string{"workflows", "not", "completed"}, "workflows not completed"},
}

// candCell is one judged row of a run: its value as the row records it, its limit and result as recorded, and
// whether it decided the verdict.
type candCell struct {
	value          Number // not OK when the row has none
	limit, result  string
	floor          bool    // the limit is a floor, ">= n"; else a max
	bound          float64 // the limit's number
	decided, named bool    // in .deciding; and in the candidate's blocks_on too
}

// candRun is one run of laug candidate as its JSON records it.
type candRun struct {
	file, label, name, runID, server, backend, outcome, verdict, expect, key, from, to string
	blocksOn                                                                           []string
	duration, rate, inFlight                                                           string
	wait, reread, window                                                               float64
	cells                                                                              []candCell // by candidateCols
}

// readCandidate reads boot 3's run label from its rauns row: what it changed and expected, its outcome and verdict
// (the row's result), and each row its verdict judged against a limit, which must be candidateCols' rows in their
// order. The rows that decided must be the rows that block, in the same order.
func readCandidate(rauns figTable, label string) (candRun, error) {
	v, err := rauns.row(b3Row(label))
	if err != nil {
		return candRun{}, err
	}
	p := rauns.cite(b3Row(label))
	r := candRun{file: p, label: label, name: str(at(v, "candidate")), runID: str(at(v, "run_id")),
		server: str(at(v, "server_tag")), backend: str(at(v, "backend")), outcome: str(at(v, "outcome")),
		verdict: str(at(v, "result")), expect: str(at(v, "expect")), key: str(at(v, "key")),
		blocksOn: strs(at(v, "blocks_on")), duration: str(at(v, "load", "duration")), rate: str(at(v, "load", "rate")),
		inFlight: str(at(v, "load", "max_concurrent"))}
	from, to := num(at(v, "from")), num(at(v, "value"))
	r.from, r.to = from.String(), to.String()
	if r.name+"-"+r.runID != label || r.server == "" || r.backend == "" || r.outcome == "" || r.expect == "" ||
		r.key == "" ||
		!from.OK || !to.OK || r.duration == "" || r.rate == "" || r.inFlight == "" {
		return candRun{}, fmt.Errorf("%s: not candidate %s's run with its server, outcome, expect, change and load",
			p, label)
	}
	if r.verdict != "ADVANCE" && r.verdict != "BLOCK" {
		return candRun{}, fmt.Errorf("%s: the verdict %q is neither ADVANCE nor BLOCK", p, r.verdict)
	}
	if r.wait, err = need(v, p, "wait_s"); err != nil {
		return candRun{}, err
	}
	if r.reread, err = need(v, p, "reread_s"); err != nil {
		return candRun{}, err
	}
	if r.window, err = need(v, p, "load", "window_s"); err != nil {
		return candRun{}, err
	}
	rows, _ := at(v, "rows").Arr()
	var judged, blocking []string
	for _, row := range rows {
		lim := str(at(row, "limit"))
		if lim == "null" || lim == "" {
			continue // no threshold yet, or not judged apart from another row
		}
		m := str(at(row, "metric"))
		judged = append(judged, m)
		blocks, ok := at(row, "blocks").Bool()
		if !ok {
			return candRun{}, fmt.Errorf("%s: the row %s has no blocks", p, m)
		}
		c := candCell{value: num(at(row, "value")), limit: lim, result: str(at(row, "result")), decided: blocks,
			named: blocks && slices.Contains(r.blocksOn, m)}
		if blocks {
			blocking = append(blocking, m)
		}
		var n string
		n, c.floor = strings.CutPrefix(lim, ">= ")
		if c.bound, err = strconv.ParseFloat(n, 64); err != nil || c.bound < 0 {
			return candRun{}, fmt.Errorf("%s: the row %s's limit %q is no max or floor", p, m, lim)
		}
		if !c.value.OK && (blocks || !strings.HasPrefix(c.result, "not produced")) {
			return candRun{}, fmt.Errorf("%s: the row %s has no value, and its result is %q", p, m, c.result)
		}
		r.cells = append(r.cells, c)
	}
	var want []string
	for _, c := range candidateCols {
		want = append(want, c.key)
	}
	if !slices.Equal(judged, want) {
		return candRun{}, fmt.Errorf("%s: the rows judged against a limit are %v, the figure's columns %v", p, judged,
			want)
	}
	if deciding := strs(at(v, "deciding")); !slices.Equal(deciding, blocking) ||
		(r.verdict == "BLOCK") != (len(blocking) > 0) {
		return candRun{}, fmt.Errorf("%s: verdict %s decided by %v, against the rows that block, %v", p, r.verdict,
			deciding, blocking)
	}
	return r, nil
}

// readBoot3 checks each counted run ran on the lab host, by src/hosts.tsv, and that boot 3's readings name a C step
// for each run, each counted run and the warm-up, warmupStep, and no other: the batch's own list of its runs. That a
// counted run was counted, a verdict judged, is readCandidate's check of its rauns row. It returns the readings
// table's files.
func readBoot3(fsys fs.FS) ([]string, error) {
	readings, err := readFigTable(fsys, boot3Readings)
	if err != nil {
		return nil, err
	}
	for _, label := range candidateRuns {
		if err := onLabHost(fsys, b3Row(label)); err != nil {
			return nil, err
		}
	}
	var steps []string
	for _, r := range readings.rows {
		if st := str(at(r.Value, "step")); strings.HasPrefix(st, "C-") && !slices.Contains(steps, st) {
			steps = append(steps, st)
		}
	}
	var extra []string
	for _, st := range steps {
		if !slices.Contains(candidateRuns, strings.TrimPrefix(st, "C-")) {
			extra = append(extra, st)
		}
	}
	if len(steps)-len(extra) != len(candidateRuns) || len(extra) != 1 || extra[0] != warmupStep {
		return nil, fmt.Errorf("%s: C steps %v, want a C step for each of the %d counted runs and %s",
			readings.files[0], steps, len(candidateRuns), warmupStep)
	}
	return readings.files, nil
}

// candGroup is one candidate and its runs, in candidateRuns' order.
type candGroup struct {
	head string
	runs []candRun
}

// The candidates figure's layout: the run column, the heads of the columns, then a line a candidate and a row a run.
const (
	candLabelW  = 140.0
	candHeadY   = 102.0 // the baseline of each head's last line
	candBodyTop = 128.0
	candGroupH  = 24.0
	candRowH    = 30.0
)

// candidates draws boot 3's counted runs of laug candidate as a table a run, grouped by candidate: each run's outcome
// and verdict, then each row its verdict judged, its reading over a meter against its limit, the rows that decided
// tinted.
func candidates(fsys fs.FS) (drawn, error) {
	var runs []candRun
	var groups []candGroup
	rauns, err := readFigTable(fsys, "rauns")
	if err != nil {
		return drawn{}, err
	}
	for _, label := range candidateRuns {
		r, err := readCandidate(rauns, label)
		if err != nil {
			return drawn{}, err
		}
		runs = append(runs, r)
		head := r.name + ": " + r.key + " " + r.from + " to " + r.to + ", expect " + r.expect
		if len(r.blocksOn) > 0 {
			head += " on " + strings.Join(r.blocksOn, " or ")
		}
		switch i := len(groups) - 1; {
		case i >= 0 && groups[i].runs[0].name == r.name:
			if groups[i].head != head {
				return drawn{}, fmt.Errorf("%s: %q, against %q in %s", r.file, head, groups[i].head,
					groups[i].runs[0].file)
			}
			groups[i].runs = append(groups[i].runs, r)
		case slices.ContainsFunc(groups, func(g candGroup) bool { return g.runs[0].name == r.name }):
			return drawn{}, fmt.Errorf("%s: candidate %s's runs are not together", r.file, r.name)
		default:
			groups = append(groups, candGroup{head, []candRun{r}})
		}
	}
	b3, err := readBoot3(fsys)
	if err != nil {
		return drawn{}, err
	}
	sources := append(append(tableSources(rauns), b3...), hostsTSV)
	first := runs[0]
	var servers []string
	var windows []float64
	pass, advance := 0, 0
	for _, r := range runs {
		if r.duration != first.duration || r.rate != first.rate || r.inFlight != first.inFlight ||
			r.backend != first.backend || r.wait != first.wait || r.reread != first.reread {
			return drawn{}, fmt.Errorf("%s: a %s cell, a wait of %s s, re-read %s s, a load of %s at %s/s, %s in "+
				"flight, against %s's %s, %s s, %s s, %s at %s/s, %s", r.file, r.backend, Num(r.wait), Num(r.reread),
				r.duration, r.rate, r.inFlight, first.file, first.backend, Num(first.wait), Num(first.reread),
				first.duration, first.rate, first.inFlight)
		}
		if !slices.Contains(servers, r.server) {
			servers = append(servers, r.server)
		}
		if r.outcome == "PASS" {
			pass++
		}
		if r.verdict == "ADVANCE" {
			advance++
		}
		windows = append(windows, r.window)
	}
	var notProduced, floors, zeros []string
	for _, r := range runs {
		for j, c := range r.cells {
			np := candidateCols[j].label + ", \"" + c.result + "\""
			if !c.value.OK && !slices.Contains(notProduced, np) {
				notProduced = append(notProduced, np)
			}
		}
	}
	for j, c := range first.cells {
		switch {
		case c.floor:
			floors = append(floors, candidateCols[j].label)
		case c.bound == 0:
			zeros = append(zeros, candidateCols[j].label)
		}
	}
	waits := Num(first.wait) + " s"
	if n := first.wait / first.reread; n == float64(int(n)) && n > 1 {
		waits += fmt.Sprintf(", %d re-read periods of %s s", int(n), Num(first.reread))
	}

	title := fmt.Sprintf("%d dynamic config changes in %d runs: %d PASS, %d ADVANCE and %d BLOCK", len(groups),
		len(runs), pass, advance, len(runs)-advance)
	sub := fmt.Sprintf("One key changed, then a %s load at %s workflows/s; servers %s; the lab host, an m8i.8xlarge",
		first.duration, first.rate, strings.Join(servers, " and "))
	desc := "A row a run of laug candidate, grouped by candidate in the batch's order, each group's line naming the " +
		"key it changes, from and to, and the verdict it expects. Each run leases a " + first.backend + " cell, " +
		"changes the key in the cell's dynamic config, waits " + waits + ", runs a " + first.duration + " load at " +
		first.rate + " workflows/s with " + first.inFlight + " in flight and restores the key; the verdict judges " +
		"the load's window, " + span(windows) + " s. At the left, each " +
		"run's id and server, then its outcome and verdict: PASS when the verdict is the candidate's expect and, " +
		"for a BLOCK, a row its blocks_on names is among the rows that decided it. Across, each row the verdict " +
		"judges against a limit, s2s standing for schedule-to-start, the limit under it as the rows record it. " +
		"In each cell the reading as its row records it, over the load's window; under it a meter from 0 to twice " +
		"the limit, the reading its dot, the limit its tick at the middle, and a triangle at the right end for a " +
		"reading past twice the limit. A tinted cell is a row that decided the verdict; outlined, a row the " +
		"candidate's blocks_on names too."
	if len(floors) > 0 {
		desc += " A floor's dot left of its tick is short of it: " + strings.Join(floors, ", ") + "."
	}
	if len(zeros) > 0 {
		desc += " A limit of 0 has no meter: " + strings.Join(zeros, ", ") + "."
	}
	if len(notProduced) > 0 {
		desc += " A cell that reads not produced is a row with no value, which decided nothing; its result: " +
			strings.Join(notProduced, "; ") + "."
	}
	desc += " The batch's warm-up run is not among them (boot 3's readings, the step " + warmupStep + ")."

	colX0 := marginL + candLabelW
	colW := (marginR - colX0) / float64(len(candidateCols))
	height := candBodyTop + float64(float64(len(groups))*candGroupH) + float64(float64(len(runs))*candRowH) + 70
	s := lessonFigure("candidates", title, sub, desc, sources, height)

	// The heads: each column's reading, bottom-aligned, and its limit in ember under it.
	s.Text(marginL, candHeadY, "run, server", Font{Size: 9})
	s.Text(marginL, candHeadY+14, "outcome, verdict", Font{Size: 9})
	for j, c := range candidateCols {
		x := colX0 + float64(float64(j)*colW) + 4
		for i, l := range c.head {
			s.Text(x, candHeadY-float64(float64(len(c.head)-1-i)*11), l, Font{Size: 9})
			if textW(l, 9, false) > colW-6 {
				s.fail("the head %q is wider than its column", l)
			}
		}
		lim := runs[0].cells[j]
		for _, r := range runs {
			if r.cells[j].limit != lim.limit {
				return drawn{}, fmt.Errorf("%s: %s's limit %q, against %q in %s", r.file, c.key, r.cells[j].limit,
					lim.limit, runs[0].file)
			}
		}
		word := "max " + lim.limit
		if lim.floor {
			word = lim.limit
		}
		s.Text(x, candHeadY+14, word, Font{Size: 9, Mono: true, Colour: Ember})
	}
	s.Line(marginL, candBodyTop-4, marginR, candBodyTop-4, Style{Stroke: Basalt})

	y := candBodyTop
	for _, g := range groups {
		s.Line(marginL, y, marginR, y, Style{Stroke: Silica})
		s.Text(marginL, y+16, g.head, Font{Size: 11, Bold: true})
		if textW(g.head, 11, true) > marginR-marginL {
			s.fail("the line %q is wider than the figure", g.head)
		}
		y += candGroupH
		for _, r := range g.runs {
			s.Group(r.label + ": " + r.outcome + ", " + r.verdict)
			drawCandRow(s, r, y, colX0, colW)
			s.EndGroup()
			y += candRowH
		}
	}

	ly := y + 26
	tint := func(named bool) func(s *SVG, x, y float64) {
		return func(s *SVG, x, y float64) {
			st := Style{Fill: Ember, FillOpacity: 0.15}
			if named {
				st.Stroke, st.Width = Ember, 1.5
			}
			s.Rect(x, y-6, 18, 12, st)
		}
	}
	lessonLegend(s, marginL, ly, []legendItem{
		{"a row that decided the verdict", tint(false)}, {"and one its blocks_on names", tint(true)},
		{"the limit", func(s *SVG, x, y float64) { s.Line(x+9, y-6, x+9, y+6, Style{Stroke: Ember, Width: 1.5}) }},
	})
	lessonLegend(s, marginL, ly+22, []legendItem{
		{"the reading, 0 to twice the limit", func(s *SVG, x, y float64) {
			s.Rect(x, y-1.5, 18, 3, Style{Fill: Silica})
			s.Circle(x+6, y, 3, Style{Fill: Water, Stroke: Deep, Width: 0.5})
		}},
		{"past twice the limit", func(s *SVG, x, y float64) {
			s.Polygon([]Point{{x + 10, y - 4}, {x + 16, y}, {x + 10, y + 4}}, Style{Fill: Ember})
		}},
	})
	s.Text(marginR, ly+22, "s2s: schedule-to-start", Font{Size: 11, Anchor: AnchorEnd})
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// drawCandRow draws run r's row from y: its id, server, outcome and verdict, then a cell a judged row.
func drawCandRow(s *SVG, r candRun, y, colX0, colW float64) {
	s.Text(marginL, y+13, r.runID, Font{Size: 10, Mono: true})
	s.Text(marginL+72, y+13, r.server, Font{Size: 10, Mono: true})
	s.Text(marginL, y+26, r.outcome, Font{Size: 10, Bold: true})
	word := Moss
	if r.verdict != "ADVANCE" {
		word = Ember
	}
	s.Text(marginL+72, y+26, r.verdict, Font{Size: 10, Bold: true, Colour: word})
	if textW(r.runID, 10, true) > 66 || textW(r.outcome, 10, true) > 66 ||
		marginL+72+max(textW(r.server, 10, true), textW(r.verdict, 10, true)) > colX0-4 {
		s.fail("the run %s's words are wider than the run column", r.label)
	}
	for j, c := range r.cells {
		x := colX0 + float64(float64(j)*colW)
		v := c.value.String()
		if !c.value.OK {
			v = "not produced"
		}
		title := fmt.Sprintf("%s, %s: %s against %s, %s", r.label, candidateCols[j].label, v, c.limit, c.result)
		st := Style{Fill: Steam, Title: title}
		if c.decided {
			st = Style{Fill: Ember, FillOpacity: 0.15, Title: title + "; it decided the verdict"}
			if c.named {
				st.Stroke, st.Width = Ember, 1.5
			}
		}
		s.Rect(x+2, y+2, colW-4, candRowH-4, st)
		f := Font{Size: 9, Mono: true}
		if !c.value.OK {
			f = Font{Size: 9}
		}
		s.Text(x+4, y+13, v, f)
		if textW(v, 9, c.value.OK) > colW-7 {
			s.fail("the reading %q is wider than its column", v)
		}
		if !c.value.OK || c.bound == 0 {
			continue // no reading, or a limit of 0: no meter
		}
		left, w := x+6, colW-12
		mid := left + w/2
		s.Rect(left, y+20, w, 3, Style{Fill: Silica})
		s.Line(mid, y+16, mid, y+27, Style{Stroke: Ember, Width: 1.5})
		fill := Water
		if c.decided {
			fill = Ember
		}
		if ratio := c.value.F / c.bound; ratio > 2 {
			s.Polygon([]Point{{left + w - 6, y + 17.5}, {left + w, y + 21.5}, {left + w - 6, y + 25.5}},
				Style{Fill: fill, Title: fmt.Sprintf("%s, %s times the limit", v, Num(ratio))})
		} else {
			s.Circle(left+float64(ratio/2*w), y+21.5, 3, Style{Fill: fill, Stroke: Deep, Width: 0.5,
				Title: fmt.Sprintf("%s against %s", v, c.limit)})
		}
	}
}
