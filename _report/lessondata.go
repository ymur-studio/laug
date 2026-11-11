package report

// lessondata.go draws the lessons' data figures from the summary tables' rows (lessonrows.go): kill-history's windows
// (stop 4), the bucket edges and the pilot's windows against them (stop 7), one to four steady loads at once (stop
// 13), and persistence p99 in the arm's A/A gates and its slow controls (stop 14). Each names the rows it reads by
// key, from a list here or a committed list under gate/sets, never a glob, and the files of the tables it read.

import (
	"fmt"
	"io/fs"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// killHistoryRauns are boot 6's three rauns of kill-history alone, on cold leases (cells/results/2026-10-27-boot6.txt,
// A4). The figure reads their rauns rows; -tables reads the files with killHistoryBins to cut fig-kill-history.
var killHistoryRauns = []string{
	"faults/results/raun-lab6-kill-history-1.json",
	"faults/results/raun-lab6-kill-history-2.json",
	"faults/results/raun-lab6-kill-history-3.json",
}

// killHistoryBins are the same rauns' failed starts a second, in the same order: every failed iteration of each raun's
// Omes log, less its remove_at, floored to the second (the transcript's A4-<n>-bins).
var killHistoryBins = []string{
	"cells/results/2026-10-27-boot6/extra/a4-kill-history-1-bins.json",
	"cells/results/2026-10-27-boot6/extra/a4-kill-history-2-bins.json",
	"cells/results/2026-10-27-boot6/extra/a4-kill-history-3-bins.json",
}

// killHistoryTable holds the same rauns' failed starts a second, cut from killHistoryBins by -tables (figtables.go).
const killHistoryTable = "fig-kill-history"

// khBin is one second of a raun's failed starts: s, seconds from the restart, floored; n, how many failed in it.
type khBin struct{ s, n float64 }

// khRaun is one raun's stamps, in seconds from its inject, and its failed starts by window. The during window runs
// from the signal landing to the hold's end, held, which is the remove less remove_wait_s (faults/run.sh), and the
// before window ends before the inject at a time the raun does not record, so it is counted and not drawn.
type khRaun struct {
	id, result, server                                    string
	rate, hold                                            float64
	before, landed, held, remove, ready, recovered, after float64
	endAt                                                 float64
	failed                                                [4]float64 // before, during, drain, after
	removeAt                                              float64    // remove_at, as the raun wrote it
	bins                                                  []khBin    // its failed starts a second, from the restart
	last                                                  float64    // the last second from the restart that failed one
}

// readKH reads a kill-history raun's JSON, p, as khOf reads it.
func readKH(fsys fs.FS, p string) (khRaun, error) {
	v, err := readJSON(fsys, p)
	if err != nil {
		return khRaun{}, err
	}
	return khOf(v, p, "before")
}

// khOf reads a kill-history raun from v, its JSON or its rauns row, named p in an error: its stamps, rate, failed
// starts by window and its before window's length, which v holds under before, the raun's key, or windows.before, the
// row's.
func khOf(v jsonx.Value, p string, before ...string) (khRaun, error) {
	var err error
	r := khRaun{id: str(at(v, "run_id")), result: str(at(v, "result")), server: str(at(v, "server_tag"))}
	if str(at(v, "fault")) != "kill-history" || r.id == "" || r.result == "" || r.server == "" {
		return khRaun{}, fmt.Errorf("%s: not a kill-history raun with its run_id, result and server_tag", p)
	}
	var inject, landed, remove, wait, ready, recovered, after, end float64
	// A raun JSON holds its stamps as Unix times; a rauns row as seconds after the inject, its inject 0
	// (timeofday.go).
	stamp := func(k string) string { return k }
	injectKeys := []string{"inject_at"}
	if isRaunRow(v) {
		stamp = func(k string) string { return k + "_s" }
		injectKeys = nil
	}
	for _, f := range []struct {
		to   *float64
		keys []string
	}{
		{&inject, injectKeys}, {&landed, []string{stamp("landed_at")}}, {&remove, []string{stamp("remove_at")}},
		{&wait, []string{"remove_wait_s"}},
		{&ready, []string{"history_shards_ready_s"}}, {&recovered, []string{stamp("recovered_at")}},
		{&after, []string{stamp("after_start")}}, {&end, []string{stamp("after_end")}}, {&r.rate, []string{"rate"}},
		{&r.before, append(before, "window_seconds")},
	} {
		if f.keys == nil {
			continue
		}
		if *f.to, err = need(v, p, f.keys...); err != nil {
			return khRaun{}, err
		}
	}
	for i, w := range windowNames {
		if r.failed[i], err = need(v, p, "iterations_failed", w); err != nil {
			return khRaun{}, err
		}
	}
	r.landed, r.remove, r.recovered, r.held = landed-inject, remove-inject, recovered-inject, remove-wait-inject
	r.ready, r.after, r.endAt, r.hold = r.remove+ready, after-inject, end-inject, remove-inject
	r.removeAt = remove
	return r, nil
}

// readBins adds to r its failed starts a second from p, refused unless p names r's run and remove_at, every failure in
// the log has its second, and those from the restart on are r's drain and after windows' count.
func readBins(fsys fs.FS, p string, r *khRaun) error {
	v, err := readJSON(fsys, p)
	if err != nil {
		return err
	}
	if id := str(at(v, "run_id")); id != r.id {
		return fmt.Errorf("%s: run_id %q, not %s", p, id, r.id)
	}
	var remove, inLog, unplaced float64
	for _, f := range []struct {
		to  *float64
		key string
	}{{&remove, "remove_at"}, {&inLog, "failed_in_log"}, {&unplaced, "unplaced"}} {
		if *f.to, err = need(v, p, f.key); err != nil {
			return err
		}
	}
	if remove != r.removeAt || unplaced != 0 {
		return fmt.Errorf("%s: remove_at %s and %s unplaced, not %s and 0", p, Num(remove), Num(unplaced), Num(r.removeAt))
	}
	a, _ := at(v, "by_second_from_remove_at").Arr()
	var bins []khBin
	for _, e := range a {
		var b khBin
		if b.s, err = need(e, p, "s"); err != nil {
			return err
		}
		if b.n, err = need(e, p, "n"); err != nil {
			return err
		}
		bins = append(bins, b)
	}
	return r.addBins(p, bins, inLog)
}

// addBins gives r its failed starts a second, bins, read from p: each a count in its own later whole second, all of
// them inLog, and those from the restart on r's drain and after windows' count, which is not 0.
func (r *khRaun) addBins(p string, bins []khBin, inLog float64) error {
	var all, from float64
	r.last = math.Inf(-1)
	for i, b := range bins {
		if b.n <= 0 || b.s != math.Floor(b.s) || (i > 0 && b.s <= r.bins[i-1].s) {
			return fmt.Errorf("%s: bin %d, %s failed at %s s, is not a count in its own later second", p, i, Num(b.n),
				Num(b.s))
		}
		r.bins = append(r.bins, b)
		all += b.n
		if b.s >= 0 {
			from += b.n
			r.last = b.s
		}
	}
	if all != inLog || from != r.failed[2]+r.failed[3] || from == 0 {
		return fmt.Errorf("%s: %s failed a second in all and %s from the restart on, not %s and the windows' %s", p,
			Num(all), Num(from), Num(inLog), Num(r.failed[2]+r.failed[3]))
	}
	return nil
}

// killHistory draws the three lab rauns of kill-history as lanes of their windows against seconds from the kill, each
// with its failed starts a second under it: each raun's rauns row, its host from src/hosts.tsv, and its
// fig-kill-history rows.
func killHistory(fsys fs.FS) (drawn, error) {
	ts, err := readFigTables(fsys, "rauns", killHistoryTable)
	if err != nil {
		return drawn{}, err
	}
	rauns, binRows := ts[0], ts[1]
	var rs []khRaun
	for _, p := range killHistoryRauns {
		id := strings.TrimSuffix(path.Base(p), ".json")
		v, err := rauns.row(id)
		if err != nil {
			return drawn{}, err
		}
		r, err := khOf(v, rauns.cite(id), "windows", "before")
		if err != nil {
			return drawn{}, err
		}
		if err := onLabHost(fsys, id); err != nil {
			return drawn{}, err
		}
		var bins []khBin
		all := 0.0
		for _, b := range binRows.under(r.id) {
			var k khBin
			if k.s, err = need(b, binRows.cite(r.id+"/"), "s"); err != nil {
				return drawn{}, err
			}
			if k.n, err = need(b, binRows.cite(r.id+"/"), "n"); err != nil {
				return drawn{}, err
			}
			bins, all = append(bins, k), all+k.n
		}
		if err := r.addBins(binRows.cite(r.id+"/"), bins, all); err != nil {
			return drawn{}, err
		}
		rs = append(rs, r)
	}
	col := func(f func(r khRaun) float64) []float64 {
		var xs []float64
		for _, r := range rs {
			xs = append(xs, f(r))
		}
		return xs
	}
	during := col(func(r khRaun) float64 { return r.failed[1] })
	drain := col(func(r khRaun) float64 { return r.failed[2] })
	sources := append(tableSources(ts...), hostsTSV)
	maxN := 0.0
	for _, r := range rs {
		for _, b := range r.bins {
			maxN = math.Max(maxN, b.n)
		}
	}
	title := "kill-history: " + wholeSpan(during) + " failed starts in the outage, " + wholeSpan(drain) +
		" after the restart"
	sub := fmt.Sprintf("%d rauns at %s workflows/s on server %s, the lab host, an m8i.8xlarge; history held down "+
		"%s s", len(rs), span(col(func(r khRaun) float64 { return r.rate })), rs[0].server,
		span(col(func(r khRaun) float64 { return r.hold })))
	desc := "A lane a raun of faults/catalog.yaml's kill-history; across, seconds from the kill, the raun's inject_at. " +
		"Each lane's bars are the raun's windows, left to right, each holding its failed client starts: during, from " +
		"the signal landing to the hold's end; drain, from the restart to the after window; after. The " +
		"dashed line is the restart, remove_at; the first tick the signal landing, the diamond history's own " +
		"\"initial shards ready\", the second tick the signal recovered. Shards back " +
		span(col(func(r khRaun) float64 { return r.ready - r.remove })) + " s after the restart, the signal " +
		span(col(func(r khRaun) float64 { return r.recovered - r.remove })) + " s after it, the drain " +
		span(col(func(r khRaun) float64 { return r.after - r.remove })) + " s long. The " +
		span(col(func(r khRaun) float64 { return r.before })) + " s before window, read " +
		"before the kill, failed " + wholeSpan(col(func(r khRaun) float64 { return r.failed[0] })) + ". Under each " +
		"lane, its failed starts a second: every failed iteration in the raun's Omes log, by its second from the " +
		"restart, floored, a bar a second, the tallest " + whole(maxN) + ". From the restart on, " +
		wholeSpan(col(func(r khRaun) float64 { return r.failed[2] + r.failed[3] })) + " failed, the last " +
		span(col(func(r khRaun) float64 { return r.last })) + " s after it."

	const top, laneH, barH, stripH, axisGap = 96.0, 88.0, 20.0, 18.0, 16.0
	lo, hi := -15.0, float64(30*math.Ceil(slices.Max(col(func(r khRaun) float64 { return r.endAt }))/30))
	x := Scale{lo, hi, 236, marginR}
	bottom := top + float64(float64(len(rs))*laneH)
	height := bottom + axisGap + 40 + 56
	s := lessonFigure("kill-history", title, sub, desc, sources, height)
	var ticks []float64
	for k := 0; float64(30*k) <= hi; k++ {
		ticks = append(ticks, float64(30*k))
	}
	for _, t := range ticks {
		s.Line(x.At(t), top-14, x.At(t), bottom, Style{Stroke: Silica})
	}
	s.Text(x.At(0), top-20, "killed", Font{Size: 11, Anchor: AnchorMiddle})
	s.Text(x.At(rs[0].remove), top-20, "restarted", Font{Size: 11, Anchor: AnchorMiddle})
	for i, r := range rs {
		y := top + float64(float64(i)*laneH) + 10
		s.Group(r.id + ": " + r.result)
		s.Text(marginL, y+14, r.id, Font{Size: 11, Mono: true})
		word := Moss
		if r.result != "ADVANCE" {
			word = Ember
		}
		s.Text(marginL, y+30, r.result, Font{Size: 11, Bold: true, Colour: word})
		bars := []struct {
			from, to float64
			fill     Colour
			n        float64
			name     string
		}{
			{r.landed, r.held, Warm, r.failed[1], "during"},
			{r.remove, r.after, Water, r.failed[2], "drain"},
			{r.after, r.endAt, Silica, r.failed[3], "after"},
		}
		for _, b := range bars {
			w := x.At(b.to) - x.At(b.from)
			s.Rect(x.At(b.from), y, w, barH, Style{Fill: b.fill, Stroke: Deep, Width: 0.5,
				Title: fmt.Sprintf("%s, %s to %s s: %s", b.name, Num(b.from), Num(b.to), failedStarts(b.n))})
			if lbl := whole(b.n); textW(lbl, 11, true)+6 < w {
				s.Text(x.At(b.from)+w/2, y+14, lbl, Font{Size: 11, Anchor: AnchorMiddle, Mono: true})
			}
		}
		s.Line(x.At(0), y-6, x.At(0), y+barH+6, Style{Stroke: Basalt, Width: 1.5})
		s.Line(x.At(r.remove), y-8, x.At(r.remove), y+barH+4+stripH, Style{Stroke: Basalt, Width: 1.5,
			Dash: []float64{4, 3}})
		for _, t := range []float64{r.landed, r.recovered} {
			s.Line(x.At(t), y-8, x.At(t), y-1, Style{Stroke: Basalt, Width: 2})
		}
		cx := x.At(r.ready)
		s.Polygon([]Point{{cx, y - 10}, {cx + 4, y - 5}, {cx, y}, {cx - 4, y - 5}}, Style{Fill: Deep,
			Title: "initial shards ready " + Num(r.ready-r.remove) + " s after the restart"})
		base := y + barH + 4 + stripH
		s.Line(x.At(lo), base, x.At(r.endAt), base, Style{Stroke: Silica})
		for _, b := range r.bins {
			h := float64(stripH*b.n) / maxN
			s.Rect(x.At(r.remove+b.s), base-h, x.At(r.remove+b.s+1)-x.At(r.remove+b.s), h, Style{Fill: Deep,
				Title: fmt.Sprintf("%+g s from the restart: %s", b.s, failedStarts(b.n))})
		}
		caption := fmt.Sprintf("landed +%s s; after the restart: shards ready %s s, signal recovered %s s, last "+
			"failed start %s s; drain %s s", Num(r.landed), Num(r.ready-r.remove), Num(r.recovered-r.remove),
			Num(r.last), Num(r.after-r.remove))
		s.Text(marginL, base+15, caption, Font{Size: 10})
		if marginL+textW(caption, 10, false) > marginR {
			s.fail("the lane's caption %q does not fit", caption)
		}
		s.EndGroup()
	}
	s.AxisX(x, bottom+axisGap, ticks, "seconds after history was killed")
	legend := []legendItem{
		{"after", swatch(Silica)}, {"during: held down", swatch(Warm)},
		{"drain: restart to after window", swatch(Water)}, {"restart", stroke(Basalt, []float64{4, 3}, 1.5)},
		{"shards ready", func(s *SVG, x, y float64) {
			s.Polygon([]Point{{x + 9, y - 5}, {x + 13, y}, {x + 9, y + 5}, {x + 5, y}}, Style{Fill: Deep})
		}},
	}
	lessonLegend(s, marginL, height-18, legend)
	s.Text(marginL, top-20, "In each bar: failed client starts;", Font{Size: 11})
	s.Text(marginL, top-6, "under it, the same a second at a time", Font{Size: 11})
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// legendItem is one entry of a figure's legend: a mark drawn at its place, then its words.
type legendItem struct {
	label string
	mark  func(s *SVG, x, y float64)
}

// swatch is a legend mark: a filled bar in c.
func swatch(c Colour) func(s *SVG, x, y float64) {
	return func(s *SVG, x, y float64) { s.Rect(x, y-5, 18, 10, Style{Fill: c}) }
}

// stroke is a legend mark: a line in c, dashed as dash says, width wide.
func stroke(c Colour, dash []float64, width float64) func(s *SVG, x, y float64) {
	return func(s *SVG, x, y float64) { s.Line(x, y, x+18, y, Style{Stroke: c, Dash: dash, Width: width}) }
}

// triangle is an upward triangle about (x, y), r from its centre to a corner.
func triangle(x, y, r float64) []Point {
	return []Point{{x, y - r}, {x + float64(r*0.866), y + r/2}, {x - float64(r*0.866), y + r/2}}
}

// lessonLegend draws items in one row from x at y on a lesson's figure.
func lessonLegend(s *SVG, x, y float64, items []legendItem) {
	for _, it := range items {
		it.mark(s, x, y-4)
		s.Text(x+24, y, it.label, Font{Size: 11})
		x += 24 + textW(it.label, 11, false) + 18
	}
}

// pilotGates are the arm's pilot, six A/A gates of incumbent, steady and pl on server 1.31.0 on the lab host
// (docs/tables/records.jsonl#cells/results/2026-10-17-arm-pilot.txt lines 287-292); their incumbent and steady loads
// are its 12 load windows.
var pilotGates = []string{"g20261017-060633", "g20261017-061717", "g20261017-062801", "g20261017-063843",
	"g20261017-064925", "g20261017-070006"}

// edgesTable holds the edges of server 1.31.0's latency histograms, cut by -tables from the transcript line that
// lists them (figtables.go).
const edgesTable = "fig-bucket-edges"

// pilotWindow is one load window of the pilot: an item's readings as its verdict recorded them.
type pilotWindow struct {
	gate, key string
	read      map[string]float64
}

// pilotReadings are the readings bucketEdges draws, each with its edge and its words.
var pilotReadings = []struct{ name, label string }{
	{"workflow_task_schedule_to_start_p99_ms", "workflow task schedule-to-start p99"},
	{"activity_schedule_to_start_p99_ms", "activity schedule-to-start p99"},
	{"persistence_latency_p99_ms", "persistence p99"},
	{"frontend_request_p99_ms", "frontend p99, polls left out"},
	{"workflow_end_to_end_p95_ms", "workflow end-to-end p95"},
}

// logScale maps a positive value onto user units on a log10 scale from d0 to d1.
func logScale(v, d0, d1, r0, r1 float64) float64 {
	return r0 + float64((math.Log10(v)-math.Log10(d0))/(math.Log10(d1)-math.Log10(d0))*(r1-r0))
}

// edgeLabel is an edge in seconds as the ladder writes it: in ms under 1 s, else in s.
func edgeLabel(sec float64, first bool) string {
	if sec < 1 {
		l := Num(float64(sec * 1000))
		if first {
			l += " ms"
		}
		return l
	}
	l := Num(sec)
	if first {
		l += " s"
	}
	return l
}

// bucketEdges draws the edge ladder of server 1.31.0's latency histograms, the pilot's five readings in its 12 load
// windows against the edges, and the activity schedule-to-start p99 of each window against its share past 10 ms.
func bucketEdges(fsys fs.FS) (drawn, error) {
	ts, err := readFigTables(fsys, edgesTable, "gates", "gate-readings")
	if err != nil {
		return drawn{}, err
	}
	edgeRows, gates, readings := ts[0], ts[1], ts[2]
	var vals []string
	for _, r := range edgeRows.rows {
		vals = append(vals, str(at(r.Value, "le")))
	}
	var edges []float64
	inf := false
	for _, v := range vals {
		if v == "+Inf" {
			inf = true
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || !(f > 0) || len(edges) > 0 && f <= edges[len(edges)-1] {
			return drawn{}, fmt.Errorf("%s: %q is not an edge over the one before it", edgeRows.files[0], v)
		}
		edges = append(edges, f)
	}
	if len(edges) < 2 || !inf {
		return drawn{}, fmt.Errorf("%s: want the edges in order, then +Inf", edgeRows.files[0])
	}
	var ws []pilotWindow
	server := ""
	for _, g := range pilotGates {
		gv, err := gates.row(g)
		if err != nil {
			return drawn{}, err
		}
		t := str(at(gv, "server_tag"))
		if t == "" || server != "" && t != server {
			return drawn{}, fmt.Errorf("%s: server_tag %q, against %q in the gates before it", gates.cite(g), t, server)
		}
		if err := labGate(gates, g); err != nil {
			return drawn{}, err
		}
		server = t
		for _, k := range []string{"incumbent", "steady"} {
			read, _, f := rowReadings(readings, g+"/"+k)
			for _, r := range append(pilotReadings[:len(pilotReadings):len(pilotReadings)],
				struct{ name, label string }{"activity_schedule_to_start_over_10ms", ""}) {
				if _, ok := read[r.name]; !ok {
					return drawn{}, fmt.Errorf("%s: no %s", f, r.name)
				}
			}
			ws = append(ws, pilotWindow{g, k, read})
		}
	}
	sources := tableSources(ts...)

	title := fmt.Sprintf("Server %s's latency buckets: %d edges, and %d pilot windows against them", server,
		len(edges), len(ws))
	sub := fmt.Sprintf("The edges every latency histogram has, then %d load windows of the arm's pilot on the lab "+
		"host, an m8i.8xlarge", len(ws))
	desc := fmt.Sprintf("Top: the %d edges of server %s's latency histograms in seconds, as the lab host's Prometheus "+
		"listed them for the five the gate reads, on a log scale, then +Inf. Middle: the pilot's five p-readings, a "+
		"row each, a dot a load window, across in ms with the edges from 5 to 50 ms drawn through them. Bottom: "+
		"activity schedule-to-start p99 against its share past 10 ms, a dot a window: at or under a 1%% share the "+
		"p99 reads at the 10 ms edge, past it the p99 is placed inside the 10 to 20 ms bucket.", len(edges), server)

	const ladderTop, rowsTop, rowH, scatterTop, scatterH = 92.0, 178.0, 26.0, 380.0, 200.0
	height := scatterTop + scatterH + 64
	s := lessonFigure("bucket-edges", title, sub, desc, sources, height)

	// The ladder.
	lx := func(v float64) float64 { return logScale(v, edges[0], edges[len(edges)-1], 60, marginR-64) }
	s.Text(marginL, ladderTop-14, fmt.Sprintf("%d edges, in seconds, then +Inf", len(edges)), Font{Size: 12,
		Bold: true})
	s.Line(lx(edges[0]), ladderTop+10, lx(edges[len(edges)-1])+22, ladderTop+10, Style{Stroke: Basalt})
	s.Group("the edges")
	for i, e := range edges {
		s.Line(lx(e), ladderTop, lx(e), ladderTop+20, Style{Stroke: Deep, Width: 1.5,
			Title: "le=\"" + vals[i] + "\""})
		s.Text(lx(e), ladderTop+34, edgeLabel(e, i == 0 || e == 1), Font{Size: 10, Anchor: AnchorMiddle, Mono: true})
	}
	s.EndGroup()
	s.Text(lx(edges[len(edges)-1])+28, ladderTop+14, "+Inf", Font{Size: 10, Mono: true})

	// The five readings against the edges from 5 to 50 ms.
	zx := Scale{4, 54, 250, 690}
	s.Text(marginL, rowsTop-14, "Each window's readings, ms, against the edges", Font{Size: 12, Bold: true})
	rowsBottom := rowsTop + float64(float64(len(pilotReadings))*rowH)
	for _, e := range edges {
		if ms := float64(e * 1000); ms >= zx.D0 && ms <= zx.D1 {
			s.Line(zx.At(ms), rowsTop-4, zx.At(ms), rowsBottom, Style{Stroke: Deep, Width: 1.5})
			s.Text(zx.At(ms), rowsBottom+14, Num(ms), Font{Size: 10, Anchor: AnchorMiddle, Mono: true})
		}
	}
	s.Text(zx.R1, rowsBottom+28, "edges, ms", Font{Size: 10, Anchor: AnchorEnd})
	for i, r := range pilotReadings {
		y := rowsTop + float64(float64(i)*rowH) + rowH/2
		var xs []float64
		s.Group(r.label)
		s.Text(marginL, y+4, r.label, Font{Size: 11})
		for _, w := range ws {
			v := w.read[r.name]
			xs = append(xs, v)
			s.Circle(zx.At(v), y, 3.5, Style{Fill: Water, Stroke: Deep, Width: 0.5, FillOpacity: 0.8,
				Title: w.gate + " " + w.key + ": " + Num(v) + " ms"})
		}
		s.Text(marginR, y+4, spanTo(xs, 1)+" ms", Font{Size: 10, Mono: true, Anchor: AnchorEnd})
		s.EndGroup()
	}

	// Activity schedule-to-start p99 against its share past 10 ms.
	var shares, p99s []float64
	for _, w := range ws {
		shares = append(shares, float64(w.read["activity_schedule_to_start_over_10ms"]*100))
		p99s = append(p99s, w.read["activity_schedule_to_start_p99_ms"])
	}
	sx := Scale{math.Floor(float64(slices.Min(shares)*10)) / 10, math.Ceil(float64(slices.Max(shares)*10)) / 10, 290,
		marginR - 20}
	sy := Scale{math.Floor(slices.Min(p99s)) - 0.5, math.Ceil(slices.Max(p99s)) + 0.5, scatterTop + scatterH,
		scatterTop + 20}
	s.Text(marginL, scatterTop+4, "Activity schedule-to-start, a dot a window", Font{Size: 12, Bold: true})
	yt, xt := Ticks(sy.D0, sy.D1, 4), Ticks(sx.D0, sx.D1, 6)
	s.GridY(sy, sx.R0, sx.R1, yt)
	s.Line(sx.R0, sy.At(10), sx.R1, sy.At(10), Style{Stroke: Deep, Width: 1.5})
	s.Text(sx.R1, sy.At(10)-5, "the 10 ms edge", Font{Size: 10, Anchor: AnchorEnd})
	if sx.D0 < 1 && sx.D1 > 1 {
		s.Line(sx.At(1), sy.R1, sx.At(1), sy.R0, Style{Stroke: Deep, Dash: []float64{4, 3}})
		s.Text(sx.At(1)+5, sy.R1+10, "1%, the p99's own share", Font{Size: 10})
	}
	s.Group("the windows")
	for i, w := range ws {
		s.Circle(sx.At(shares[i]), sy.At(p99s[i]), 4, Style{Fill: Water, Stroke: Deep, Width: 0.5,
			Title: fmt.Sprintf("%s %s: %s%% past 10 ms, p99 %s ms", w.gate, w.key, Num(shares[i]), Num(p99s[i]))})
	}
	s.EndGroup()
	s.AxisY(sy, sx.R0, yt, "p99, ms")
	s.AxisX(sx, sy.R0, xt, "share of activities past 10 ms, %")
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// noiseGates are boot 4's steady-load waves of gate/plans/noise.yaml on the lab host, N loads at once, by N, for N
// from 1 to 4 (cells/results/2026-10-24-boot4.txt:605-628, N1-1 to N4-1). They replace the waves of
// docs/tables/records.jsonl#cells/results/2026-10-15-lab-gates-4-5-noise.txt (boot4.txt:667-672).
var noiseGates = [][]string{
	{"g20261024-104246"},
	{"g20261024-105215"},
	{"g20261024-110203"},
	{"g20261024-111210"},
}

// neighbourReadings are the four readings the neighbours figure draws, a panel each.
var neighbourReadings = []struct{ name, label string }{
	{"workflow_task_schedule_to_start_p99_ms", "workflow task schedule-to-start p99, ms"},
	{"workflow_end_to_end_p95_ms", "workflow end-to-end p95, ms"},
	{"persistence_latency_p99_ms", "persistence p99, ms"},
	{"frontend_request_p99_ms", "frontend p99, polls left out, ms"},
}

// neighbours draws each item's readings in the noise waves, one to four steady loads at once on one host, against the
// max each item was judged by: each gate's gates row, each item's gate-items row and its gate-readings rows.
func neighbours(fsys fs.FS) (drawn, error) {
	type itemRead struct {
		gate, key string
		n         int
		read      map[string]float64
		limit     map[string]string
		verdict   string
	}
	var items []itemRead
	ts, err := readFigTables(fsys, "gates", "gate-items", "gate-readings")
	if err != nil {
		return drawn{}, err
	}
	gates, gateItems, readings := ts[0], ts[1], ts[2]
	server, plan := "", ""
	for i, ids := range noiseGates {
		n := i + 1
		for _, g := range ids {
			gv, err := gates.row(g)
			if err != nil {
				return drawn{}, err
			}
			t, pl := str(at(gv, "server_tag")), str(at(gv, "plan"))
			if t == "" || pl == "" || server != "" && (t != server || pl != plan) {
				return drawn{}, fmt.Errorf("%s: server_tag %q and plan %q, against %q and %q before it", gates.cite(g),
					t, pl, server, plan)
			}
			if err := labGate(gates, g); err != nil {
				return drawn{}, err
			}
			server, plan = t, pl
			for k := 1; k <= n; k++ {
				key := "s" + strconv.Itoa(k)
				iv, err := gateItems.row(g + "/" + key)
				if err != nil {
					return drawn{}, err
				}
				read, limit, p := rowReadings(readings, g+"/"+key)
				it := itemRead{g, key, n, read, limit, str(at(iv, "verdict"))}
				for _, r := range neighbourReadings {
					if _, ok := read[r.name]; !ok {
						return drawn{}, fmt.Errorf("%s: no %s", p, r.name)
					}
					if _, err := strconv.ParseFloat(limit[r.name], 64); err != nil {
						return drawn{}, fmt.Errorf("%s: %s's limit %q is not a number", p, r.name, limit[r.name])
					}
				}
				items = append(items, it)
			}
		}
	}
	sources := tableSources(ts...)
	advance := 0
	for _, it := range items {
		if it.verdict == "ADVANCE" {
			advance++
		}
	}
	var fe1, fe4 []float64
	for _, it := range items {
		switch it.n {
		case 1:
			fe1 = append(fe1, it.read["frontend_request_p99_ms"])
		case len(noiseGates):
			fe4 = append(fe4, it.read["frontend_request_p99_ms"])
		}
	}
	title := fmt.Sprintf("1 to %d steady loads at once: frontend p99 %s ms alone, %s ms at %d", len(noiseGates),
		spanTo(fe1, 1), spanTo(fe4, 1), len(noiseGates))
	sub := fmt.Sprintf("A dot an item, %d in %d noise waves, against its max: %d ADVANCE; the lab host, an m8i.8xlarge",
		len(items), gateCount(noiseGates), advance)
	desc := "Four panels, a reading each. Across, how many steady loads ran at once on the host, each on a cell of " +
		"its own, on server " + server + " (" + plan + "); up, the reading in ms, a dot an item; the dashed line " +
		"is the max the item was judged by, as its verdict records it."

	const top, panelW, panelH, gapX, gapY = 92.0, 300.0, 150.0, 76.0, 74.0
	height := top + float64(2*(panelH+gapY)) + 8
	s := lessonFigure("neighbours", title, sub, desc, sources, height)
	for p, r := range neighbourReadings {
		x0 := marginL + 60 + float64(float64(p%2)*(panelW+gapX))
		y0 := top + float64(float64(p/2)*(panelH+gapY))
		lim, _ := strconv.ParseFloat(items[0].limit[r.name], 64)
		for _, it := range items {
			if l, _ := strconv.ParseFloat(it.limit[r.name], 64); l != lim {
				return drawn{}, fmt.Errorf("%s/%s: %s's limit %s, against %s", it.gate, it.key, r.name,
					it.limit[r.name], items[0].limit[r.name])
			}
		}
		x := Scale{0.5, float64(len(noiseGates)) + 0.5, x0, x0 + panelW}
		y := Scale{0, float64(lim * 1.15), y0 + 20 + panelH, y0 + 20}
		yt := Ticks(0, y.D1, 4)
		s.Text(x0-60, y0+8, r.label, Font{Size: 12, Bold: true})
		s.Group(r.label)
		s.GridY(y, x.R0, x.R1, yt)
		s.Line(x.R0, y.At(lim), x.R1, y.At(lim), Style{Stroke: Ember, Width: 1.5, Dash: []float64{4, 3}})
		s.Text(x.R1, y.At(lim)-5, "max "+Num(lim), Font{Size: 10, Anchor: AnchorEnd, Colour: Ember, Mono: true})
		for n := 1; n <= len(noiseGates); n++ {
			var at []float64
			for _, it := range items {
				if it.n == n {
					at = append(at, it.read[r.name])
				}
			}
			for j, v := range at {
				dx := float64((float64(j) - float64(len(at)-1)/2) * 4)
				s.Circle(x.At(float64(n))+dx, y.At(v), 3, Style{Fill: Water, Stroke: Deep, Width: 0.5,
					Title: fmt.Sprintf("%d at once: %s ms", n, Num(v))})
			}
			s.Text(x.At(float64(n)), y.At(slices.Max(at))-8, spanTo(at, 1), Font{Size: 9, Anchor: AnchorMiddle,
				Mono: true})
		}
		s.EndGroup()
		s.AxisY(y, x.R0, yt, "ms")
		var xt []float64
		for n := 1; n <= len(noiseGates); n++ {
			xt = append(xt, float64(n))
		}
		s.AxisX(x, y.R0, xt, "loads at once")
	}
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// rowReadings are an item's readings as its gate-readings rows hold them, item <gate>/<key>: the value of each that
// is a number, as the item's verdict JSON records it, and each one's limit as recorded; and the rows' cite.
func rowReadings(readings figTable, item string) (map[string]float64, map[string]string, string) {
	read, limit := map[string]float64{}, map[string]string{}
	for _, r := range readings.under(item) {
		name := str(at(r, "reading"))
		if n := num(at(r, "value")); n.OK {
			read[name] = n.F
		}
		limit[name] = str(at(r, "limit"))
	}
	return read, limit, readings.cite(item + "/")
}

// labGate refuses gate id when its gates row does not place it on the lab host, an m8i.8xlarge: by the host type
// gate.json records, or, in the runs from before it recorded one, by its 32 vCPU, which no other host of
// docs/RECORDS.md's four has.
func labGate(gates figTable, id string) error {
	g, err := gates.row(id)
	if err != nil {
		return err
	}
	t, vcpu := at(g, "host", "type"), num(at(g, "host", "vcpu"))
	if s, ok := t.Str(); ok && s == "m8i.8xlarge" || t.Kind() == jsonx.KindNull && vcpu.OK && vcpu.F == 32 {
		return nil
	}
	return fmt.Errorf("%s: host %s, not the lab host, an m8i.8xlarge", gates.cite(id), jsonx.AppendCompact(nil,
		at(g, "host")))
}

func gateCount(g [][]string) int {
	n := 0
	for _, gs := range g {
		n += len(gs)
	}
	return n
}

// The arm's sets: the 20 A/A gates its tolerances were set from, the 30 that tested them, and its slow control.
const (
	armCalibration = "gate/sets/calibration-arm-3item-m8i.8xlarge.txt"
	armValidation  = "gate/sets/validation-arm-3item-m8i.8xlarge.txt"
	armSlow        = "gate/sets/control-arm-known-slow.txt"
	armSet         = "load/steady-state.yaml"
	armReading     = "persistence_latency_p99_ms"
)

// armGate is one gate's persistence p99 row: the release's reading, the incumbent's, d and its tolerance.
type armGate struct {
	id, set, result     string
	server, inc         string // the release and the incumbent, as verdict.json records them
	release, incumbent  float64
	d, tolerance        float64
	tolerated, slowGate bool
}

// armBand draws persistence p99 in the arm's 50 A/A gates and its slow control's judged gates: the release cell's
// reading against the stored max, and d, the release's reading less the incumbent's, against R7's tolerance. Each
// gate's comparison is read from its gates row and compare rows, the gates named by the sets.
func armBand(fsys fs.FS) (drawn, error) {
	var gs []armGate
	var sources []string
	gates, err := readFigTable(fsys, "gates")
	if err != nil {
		return drawn{}, err
	}
	compare, err := readFigTable(fsys, "compare")
	if err != nil {
		return drawn{}, err
	}
	for _, sp := range []string{armCalibration, armValidation, armSlow} {
		data, err := fs.ReadFile(fsys, sp)
		if err != nil {
			return drawn{}, err
		}
		set, err := ParseSet(sp, data)
		if err != nil {
			return drawn{}, err
		}
		sources = append(sources, sp)
		for _, id := range set.IDs {
			c, err := rowsCompared(gates, compare, id)
			if err != nil {
				return drawn{}, err
			}
			slow := sp == armSlow
			if slow && c.mode != "judging" {
				continue // the 1 ms toxic's four, run before any tolerance: recording mode, so no d was judged
			}
			i := slices.IndexFunc(c.rows, func(row Row) bool { return row.Name == armReading })
			if i < 0 || !c.rows[i].Release.OK || !c.rows[i].Incumbent.OK || !c.rows[i].D.OK {
				return drawn{}, fmt.Errorf("%s: no %s row with both readings and d", c.rowsPath, armReading)
			}
			row := c.rows[i]
			gs = append(gs, armGate{id: id, set: sp, result: row.Result, server: c.server, inc: c.incumbent,
				release: row.Release.F, incumbent: row.Incumbent.F, d: row.D.F, tolerance: row.Tolerance.F,
				tolerated: row.Tolerance.OK, slowGate: slow})
		}
	}
	setYAML, err := fs.ReadFile(fsys, armSet)
	if err != nil {
		return drawn{}, err
	}
	max, tol, err := armLimits(setYAML)
	if err != nil {
		return drawn{}, fmt.Errorf("%s: %w", armSet, err)
	}
	sources = append(append(sources, armSet), tableSources(gates, compare)...)
	var aaRel, aaD, slowRel, slowD []float64
	nAA, aa := 0, ""
	for _, g := range gs {
		if g.slowGate {
			slowRel, slowD = append(slowRel, g.release), append(slowD, g.d)
			continue
		}
		if g.server != g.inc || aa != "" && g.server != aa {
			return drawn{}, fmt.Errorf("%s: %s against %s in an A/A list", g.id, g.server, g.inc)
		}
		nAA, aa = nAA+1, g.server
		aaRel, aaD = append(aaRel, g.release), append(aaD, g.d)
	}
	if len(slowD) == 0 || nAA == 0 {
		return drawn{}, fmt.Errorf("want A/A gates and judged slow gates: %d and %d", nAA, len(slowD))
	}
	title := fmt.Sprintf("The slow control: persistence p99 under its %s ms max, d over its %s ms tolerance",
		Num(max), Num(tol))
	sub := fmt.Sprintf("Its %d judged gates read %s ms, d %s ms; %d A/A gates beside; the lab host, an m8i.8xlarge",
		len(slowD), spanTo(slowRel, 1), spanTo(slowD, 2), nAA)
	desc := fmt.Sprintf("Two panels over the same gates, A/A first, in their sets' order, then the slow control's. "+
		"Top: the release cell's persistence p99, ms, against the stored max, %s ms. Bottom: d, the release's "+
		"reading less the incumbent's in the same gate, ms, against R7's tolerance, %s ms. A circle an A/A gate, "+
		"both arms server %s: d %s. A triangle a gate of the control, which delays every packet into the "+
		"release cell's Postgres: R7 read it WORSE in each. The 1 ms toxic's four gates of that control ran in "+
		"recording mode and are left out.", Num(max), Num(tol), aa, span(aaD))

	const top, panelH, gap = 104.0, 150.0, 84.0
	height := top + float64(2*panelH+gap) + 70
	s := lessonFigure("arm-band", title, sub, desc, sources, height)
	x := Scale{-0.5, float64(len(gs)) - 0.5, 110, marginR - 10}
	panels := []struct {
		label string
		y     Scale
		line  float64
		lineL string
		val   func(g armGate) float64
	}{
		{"the release cell's reading, ms", Scale{0, float64(max * 1.12), top + panelH, top}, max,
			"max " + Num(max) + " ms", func(g armGate) float64 { return g.release }},
		{"d, the release's reading less the incumbent's, ms",
			Scale{math.Min(-0.5, math.Floor(float64(slices.Min(aaD)*2))/2), math.Ceil(float64(slices.Max(slowD)*2)) / 2,
				top + float64(2*panelH+gap), top + panelH + gap}, tol, "tolerance " + Num(tol) + " ms",
			func(g armGate) float64 { return g.d }},
	}
	for _, p := range panels {
		yt := Ticks(p.y.D0, p.y.D1, 4)
		s.Text(marginL, p.y.R1-12, p.label, Font{Size: 12, Bold: true})
		s.Group(p.label)
		s.GridY(p.y, x.R0, x.R1, yt)
		if p.y.D0 < 0 {
			s.Line(x.R0, p.y.At(0), x.R1, p.y.At(0), Style{Stroke: Deep, Width: 0.5})
		}
		s.Line(x.R0, p.y.At(p.line), x.R1, p.y.At(p.line), Style{Stroke: Ember, Width: 1.5, Dash: []float64{4, 3}})
		s.Text(x.R1, p.y.At(p.line)-5, p.lineL, Font{Size: 10, Anchor: AnchorEnd, Colour: Ember, Mono: true})
		for i, g := range gs {
			cx, cy := x.At(float64(i)), p.y.At(p.val(g))
			t := fmt.Sprintf("%s: release %s ms, incumbent %s ms, d %s ms, %s", g.id, Num(g.release),
				Num(g.incumbent), Num(g.d), g.result)
			if g.slowGate {
				s.Polygon(triangle(cx, cy, 5), Style{Fill: Ember, Title: t})
			} else {
				s.Circle(cx, cy, 3, Style{Fill: Water, Stroke: Deep, Width: 0.5, Title: t})
			}
		}
		s.EndGroup()
		s.AxisY(p.y, x.R0, yt, "ms")
	}
	split := x.At(float64(nAA) - 0.5)
	yb := panels[1].y.R0
	s.Line(split, top-4, split, yb, Style{Stroke: Deep, Dash: []float64{2, 3}})
	s.Text(x.At(float64(nAA-1)/2), yb+18, fmt.Sprintf("%d A/A gates", nAA), Font{Size: 11, Anchor: AnchorMiddle})
	s.Text(x.At(float64(nAA)+float64(len(slowD)-1)/2), yb+18, "slow control", Font{Size: 11, Anchor: AnchorMiddle})
	lessonLegend(s, marginL, height-18, []legendItem{
		{"A/A gate, both arms " + aa, func(s *SVG, x, y float64) {
			s.Circle(x+9, y, 3, Style{Fill: Water, Stroke: Deep, Width: 0.5})
		}},
		{"slow control, WORSE", func(s *SVG, x, y float64) { s.Polygon(triangle(x+9, y, 5), Style{Fill: Ember}) }},
		{"max and tolerance", stroke(Ember, []float64{4, 3}, 1.5)},
	})
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// armLimits reads persistence p99's max and its R7 tolerance from load/steady-state.yaml: the max on the line after
// its name in thresholds, the tolerance on its compare row. A shape other than those lines is an error, not a guess.
func armLimits(yaml []byte) (max, tol float64, err error) {
	lines := strings.Split(string(yaml), "\n")
	maxOK, tolOK := false, false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "- name: "+armReading && i+2 < len(lines) {
			for _, next := range lines[i+1 : i+3] {
				if v, ok := strings.CutPrefix(strings.TrimSpace(next), "max: "); ok {
					f := strings.Fields(v)
					if len(f) > 0 {
						if max, err = strconv.ParseFloat(f[0], 64); err == nil {
							maxOK = true
						}
					}
				}
			}
		}
		if strings.HasPrefix(t, "- {name: "+armReading+",") {
			_, after, ok := strings.Cut(t, "tolerance: ")
			if v, _, ok2 := strings.Cut(after, "}"); ok && ok2 {
				if tol, err = strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					tolOK = true
				}
			}
		}
	}
	if !maxOK || !tolOK {
		return 0, 0, fmt.Errorf("no max and tolerance for %s", armReading)
	}
	return max, tol, nil
}

// verdictCmp is what a gate run's verdict.json records of its comparison, as its gates and compare rows hold it
// (rowsCompared).
// path cites its gates row; rowsPath its compare rows, docs/tables/compare.jsonl#<id>/.
type verdictCmp struct {
	path, rowsPath, server, incumbent, verdict, mode string
	rows                                             []Row
}
