package report

// lessonworkflow.go draws the figures boot 4's pack holds (cells/results/2026-10-24-boot4.txt) of workflow Gate and of
// the stack: a worker KILLed mid-wave and the one that took the wave on (adopted, stop 17), the store reading a gauge
// that left the cell with and without track_timestamps_staleness (staleness, stop 19), and the panel under
// cancel-path's diagram (stop 16), boot 4's two cancels from the cancel request's event. Each reads rows it names one
// by one, never a glob: the gates rows of boot 4's workflow gates, which place the batch on the lab host, the batch's
// readings table, and its figure table, fig-adopted, fig-staleness or fig-cancel-path, which report -tables cuts
// from the files after reading them as the figure reads the rows: a gate's gate.json, timeline and poll lines, the
// next worker's step file and the activities' attempts; make obs-test's own lines; the events the batch kept of each
// cancel, its timeline, and the transcript's heading of each cancel's step.

import (
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Boot 4's pack: its transcript, which names each step, the batch's readings, and its readings table.
const (
	boot4Transcript    = "cells/results/2026-10-24-boot4.txt"
	boot4Pack          = "cells/results/2026-10-24-boot4/"
	boot4Readings      = boot4Pack + "readings.json"
	boot4ReadingsTable = "readings-2026-10-24-boot4"
	workflowGen        = "_report/lessonworkflow.go"
)

// Boot 4's figure tables, cut by -tables (figtables.go).
const (
	adoptedTable   = "fig-adopted"
	stalenessTable = "fig-staleness"
	cancelTable    = "fig-cancel-path"
)

// boot4Tables reads table, a figure table of boot 4, then the gates table, and places the batch on the lab host by
// the gates rows of its workflow gates (labGate), which record the host: the tables, and the figure's words for the
// host. This is a proxy. staleness (F12) and cancel-path (F5, F7) are other steps of the batch, which no gates row
// records, and src/hosts.tsv has no line citing the transcript, whose line 3 names the host; the batch ran on one host,
// so its gates' host is theirs.
func boot4Tables(fsys fs.FS, table string) ([]figTable, string, error) {
	ts, err := readFigTables(fsys, table, "gates")
	if err != nil {
		return nil, "", err
	}
	for _, g := range adoptedGates {
		if err := labGate(ts[1], g.gate); err != nil {
			return nil, "", err
		}
	}
	return ts, labHostWords, nil
}

// readJSONLines is the committed JSON Lines file p, a value a line, named in the error.
func readJSONLines(fsys fs.FS, p string) ([]jsonx.Value, error) {
	text, err := readText(fsys, p)
	if err != nil {
		return nil, err
	}
	vs, err := jsonx.ParseStream([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return vs, nil
}

// tmark is a line of a gate's timeline.jsonl: its step and its wall clock, in Unix seconds.
type tmark struct {
	step string
	wall float64
}

// readMarks reads the timeline.jsonl at p (marksOf).
func readMarks(fsys fs.FS, p string) ([]tmark, error) {
	vs, err := readJSONLines(fsys, p)
	if err != nil {
		return nil, err
	}
	return marksOf(vs, p)
}

// marksOf reads a timeline's lines vs, or its rows, named p: each its step and its wall.
func marksOf(vs []jsonx.Value, p string) ([]tmark, error) {
	var ms []tmark
	for i, v := range vs {
		step, w := str(at(v, "step")), num(at(v, "wall"))
		if step == "" || !w.OK {
			return nil, fmt.Errorf("%s: line %d has no step and wall", p, i+1)
		}
		ms = append(ms, tmark{step, w.F})
	}
	return ms, nil
}

// markOf is the wall of the one mark of ms named step, an error naming p when there is none or more than one.
func markOf(ms []tmark, p, step string) (float64, error) {
	var found []float64
	for _, m := range ms {
		if m.step == step {
			found = append(found, m.wall)
		}
	}
	if len(found) != 1 {
		return 0, fmt.Errorf("%s: %d %q marks, want 1", p, len(found), step)
	}
	return found[0], nil
}

// endedMarks are the marks of ms that end an item, "ended <key>", in order.
func endedMarks(ms []tmark) []tmark {
	var out []tmark
	for _, m := range ms {
		if strings.HasPrefix(m.step, "ended ") {
			out = append(out, m)
		}
	}
	return out
}

// readingIn is the value of the one reading named name in a batch's readings.json at p.
func readingIn(fsys fs.FS, p, name string) (jsonx.Value, error) {
	v, err := readJSON(fsys, p)
	if err != nil {
		return jsonx.Null(), err
	}
	all, _ := v.Arr()
	var found []jsonx.Value
	for _, r := range all {
		if str(at(r, "name")) == name {
			found = append(found, at(r, "value"))
		}
	}
	if len(found) != 1 {
		return jsonx.Null(), fmt.Errorf("%s: %d readings named %s, want 1", p, len(found), name)
	}
	return found[0], nil
}

// unixOf is t in Unix seconds.
func unixOf(t time.Time) float64 { return float64(t.Unix()) + float64(t.Nanosecond())/1e9 }

var (
	// workerStarted is the SDK's line as a worker starts polling, its time in RFC 3339.
	workerStarted = regexp.MustCompile(`^time=(\S+) level=INFO msg="Started Worker" `)
	// workerReplayed is laug's line as a worker replays the open gates on its queue, before it polls.
	workerReplayed = regexp.MustCompile(`^laug · worker \S+: replayed ([0-9]+) open gate\(s\) on \S+ on this build: (.*)$`)
)

// workerOf reads a worker's step file's text, named p: its replay line, which must name gate alone, and when it
// started polling, in Unix seconds.
func workerOf(text, p, gate string) (replay string, started float64, err error) {
	var starts []string
	var replays [][]string
	for _, l := range strings.Split(text, "\n") {
		if m := workerStarted.FindStringSubmatch(l); m != nil {
			starts = append(starts, m[1])
		}
		if m := workerReplayed.FindStringSubmatch(l); m != nil {
			replays = append(replays, m)
		}
	}
	if len(replays) != 1 || replays[0][1] != "1" || replays[0][2] != "gate-"+gate {
		return "", 0, fmt.Errorf("%s: want one line replaying 1 open gate, gate-%s", p, gate)
	}
	if len(starts) != 1 {
		return "", 0, fmt.Errorf("%s: %d Started Worker lines, want 1", p, len(starts))
	}
	t, err := time.Parse(time.RFC3339Nano, starts[0])
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", p, err)
	}
	return replays[0][0], unixOf(t), nil
}

// readAttempts reads the attempts the batch kept of a gate's activities, the file p (attemptsOf).
func readAttempts(fsys fs.FS, p string) (over []string, attempt, ones int, err error) {
	v, err := readJSON(fsys, p)
	if err != nil {
		return nil, 0, 0, err
	}
	all, ok := v.Arr()
	if !ok {
		return nil, 0, 0, fmt.Errorf("%s: no activity", p)
	}
	return attemptsOf(all, p)
}

// attemptsOf reads the attempts of a gate's activities (temporal workflow show, each ActivityTaskStarted's attempt),
// all, its file's entries or their rows, named p: the activities on an attempt over 1, in the file's order, that
// attempt, which they must share, and how many ran on attempt 1.
func attemptsOf(all []jsonx.Value, p string) (over []string, attempt, ones int, err error) {
	if len(all) == 0 {
		return nil, 0, 0, fmt.Errorf("%s: no activity", p)
	}
	for i, a := range all {
		name, n := str(at(a, "activity")), num(at(a, "attempt"))
		if name == "" || !n.OK || n.F < 1 || n.F != math.Trunc(n.F) {
			return nil, 0, 0, fmt.Errorf("%s: entry %d is no activity with its attempt", p, i+1)
		}
		switch k := int(n.F); {
		case k == 1:
			ones++
		case attempt == 0 || k == attempt:
			attempt = k
			over = append(over, name)
		default:
			return nil, 0, 0, fmt.Errorf("%s: activities on attempts %d and %d", p, attempt, k)
		}
	}
	return over, attempt, ones, nil
}

// adoptedGate is one of boot 4's gates through laug gate run -workflow on cells (cells/results/2026-10-24-boot4.txt,
// G): its step and id and, for a gate whose worker was KILLed, the reading that holds the KILL, the activities'
// attempts and the next worker's step file.
type adoptedGate struct {
	step, gate, kill, attempts, worker string
}

// adoptedGates are G2, a workflow gate with no KILL, then G4 and G5, the same gate with the worker's process group
// KILLed 60 s and 300 s into the wave and another worker started 30 s after (cells/results/2026-10-24-boot4.txt, G2,
// G4 and G5).
var adoptedGates = []adoptedGate{
	{"G2", "g20261024-092853", "", "", ""},
	{"G4", "g20261024-095106", "kill_G4", boot4Pack + "extra/g4-attempts.json", boot4Pack + "steps/G4-worker.txt"},
	{"G5", "g20261024-122949", "kill_G5", boot4Pack + "extra/g5-attempts.json", boot4Pack + "steps/G5-worker.txt"},
}

// adoptedRun is one of adoptedGates as its files record it. Every stamp is in Unix seconds.
type adoptedRun struct {
	adoptedGate
	verdict, server            string
	start, wave, waveEnd, post float64
	ended                      []tmark
	polls                      []float64 // poll.jsonl's lines
	killed, started            float64   // the KILL and the next worker's Started Worker; 0 with no KILL
	replay                     string
	over                       []string
	attempt, ones              int
	files                      []string
}

// adoptedIn is what one of adoptedGates' records hold of it, each part with its name in an error: from its gate.json,
// its driver and workflow id; from its verdict, its gate id, verdict and server_tag; its timeline's marks and its poll
// lines; for a KILL, the reading, the next worker's step file and the activities' attempts.
type adoptedIn struct {
	driver, workflowID, gateID, verdict, server string
	gateName, verdictName                       string
	marks                                       []tmark
	marksName                                   string
	polls                                       []jsonx.Value
	pollsName                                   string
	kill                                        jsonx.Value
	killName                                    string
	worker, workerName                          string
	attempts                                    []jsonx.Value
	attemptsName                                string
	files                                       []string
}

// readAdopted reads gate g from its files (adoptedOf): its gate.json, timeline, verdict and poll lines under
// gate/results, and for a KILL, the reading in boot 4's readings.json, the next worker's step file and the attempts.
func readAdopted(fsys fs.FS, g adoptedGate) (adoptedRun, error) {
	dir := "gate/results/" + g.gate + "/"
	gj, tl, vj, pj := dir+"gate.json", dir+"timeline.jsonl", dir+"verdict.json", dir+"poll.jsonl"
	in := adoptedIn{gateName: gj, verdictName: vj, marksName: tl, pollsName: pj, files: []string{gj, tl, vj, pj}}
	gv, err := readJSON(fsys, gj)
	if err != nil {
		return adoptedRun{adoptedGate: g}, err
	}
	in.driver, in.workflowID = str(at(gv, "driver")), str(at(gv, "workflow_id"))
	if in.marks, err = readMarks(fsys, tl); err != nil {
		return adoptedRun{adoptedGate: g}, err
	}
	vv, err := readJSON(fsys, vj)
	if err != nil {
		return adoptedRun{adoptedGate: g}, err
	}
	in.gateID, in.verdict, in.server = str(at(vv, "gate_id")), str(at(vv, "verdict")), str(at(vv, "server_tag"))
	if in.polls, err = readJSONLines(fsys, pj); err != nil {
		return adoptedRun{adoptedGate: g}, err
	}
	if g.kill != "" {
		in.files = append(in.files, boot4Readings, g.worker, g.attempts)
		in.killName, in.workerName, in.attemptsName = boot4Readings, g.worker, g.attempts
		if in.kill, err = readingIn(fsys, boot4Readings, g.kill); err != nil {
			return adoptedRun{adoptedGate: g}, err
		}
		if in.worker, err = readText(fsys, g.worker); err != nil {
			return adoptedRun{adoptedGate: g}, err
		}
		v, err := readJSON(fsys, g.attempts)
		if err != nil {
			return adoptedRun{adoptedGate: g}, err
		}
		in.attempts, _ = v.Arr()
	}
	return adoptedOf(g, in)
}

// adoptedOf reads gate g from what its records hold, in: its gate.json must name workflow Gate's driver, its verdict
// the gate, and for a KILL, the reading's wave and replay line must be the timeline's and the step file's.
func adoptedOf(g adoptedGate, in adoptedIn) (adoptedRun, error) {
	r := adoptedRun{adoptedGate: g, files: in.files}
	var err error
	if in.driver != "step.sh" || in.workflowID != "gate-"+g.gate {
		return r, fmt.Errorf("%s: not workflow gate-%s through step.sh", in.gateName, g.gate)
	}
	ms, tl := in.marks, in.marksName
	for _, m := range []struct {
		to   *float64
		step string
	}{{&r.start, "start"}, {&r.wave, "wave"}, {&r.waveEnd, "wave end"}, {&r.post, "post"}} {
		if *m.to, err = markOf(ms, tl, m.step); err != nil {
			return r, err
		}
	}
	if r.ended = endedMarks(ms); len(r.ended) == 0 {
		return r, fmt.Errorf("%s: no item ended", tl)
	}
	r.verdict, r.server = in.verdict, in.server
	if in.gateID != g.gate || r.verdict == "" || r.server == "" {
		return r, fmt.Errorf("%s: not gate %s's verdict with its server_tag", in.verdictName, g.gate)
	}
	for i, p := range in.polls {
		w := num(at(p, "wall"))
		if !w.OK {
			return r, fmt.Errorf("%s: line %d has no wall", in.pollsName, i+1)
		}
		r.polls = append(r.polls, w.F)
	}
	if len(r.polls) < 2 {
		return r, fmt.Errorf("%s: %d lines, want 2 or more", in.pollsName, len(r.polls))
	}
	if g.kill == "" {
		return r, nil
	}
	kv, kn := in.kill, in.killName
	waveAt, err := need(kv, kn, "wave_at")
	if err != nil {
		return r, err
	}
	if math.Abs(waveAt-r.wave) > 0.0005 {
		return r, fmt.Errorf("%s: %s's wave_at %s is not %s's wave mark %s", kn, g.kill, Num(waveAt), tl,
			Num(r.wave))
	}
	if r.killed, err = need(kv, kn, "killed_at"); err != nil {
		return r, err
	}
	if r.replay, r.started, err = workerOf(in.worker, in.workerName, g.gate); err != nil {
		return r, err
	}
	if str(at(kv, "replay_line")) != r.replay {
		return r, fmt.Errorf("%s: %s's replay_line is not %s's", kn, g.kill, in.workerName)
	}
	if !(r.wave < r.killed && r.killed < r.started && r.started < r.waveEnd) {
		return r, fmt.Errorf("%s: the KILL and %s's Started Worker are not in that order inside the wave", g.kill,
			in.workerName)
	}
	if r.over, r.attempt, r.ones, err = attemptsOf(in.attempts, in.attemptsName); err != nil {
		return r, err
	}
	return r, nil
}

// The prefixes of the rows fig-adopted holds of gate g's files: its gate.json, timeline and poll lines, and for a
// KILL, the next worker's step file's lines and the activities' attempts.
func adoptedRows(g, file string) string { return g + "/" + file }

// rowsAdopted reads gate g from its gates row, the rows of fig-adopted and the reading of the KILL in boot 4's
// readings table (adoptedOf).
func rowsAdopted(gates, fig, readings figTable, g adoptedGate) (adoptedRun, error) {
	gv, err := gates.row(g.gate)
	if err != nil {
		return adoptedRun{adoptedGate: g}, err
	}
	gj := adoptedRows(g.gate, "gate.json")
	var driver, workflowID jsonx.Value
	for _, f := range []struct {
		key string
		to  *jsonx.Value
	}{{"driver", &driver}, {"workflow_id", &workflowID}} {
		v, err := fig.row(gj + "/" + f.key)
		if err != nil {
			return adoptedRun{adoptedGate: g}, err
		}
		*f.to = at(v, "value")
	}
	tl, pj := adoptedRows(g.gate, "timeline.jsonl"), adoptedRows(g.gate, "poll.jsonl")
	in := adoptedIn{driver: str(driver), workflowID: str(workflowID), gateName: fig.cite(gj + "/"),
		gateID: str(at(gv, "id")), verdict: str(at(gv, "verdict")), server: str(at(gv, "server_tag")),
		verdictName: gates.cite(g.gate), marksName: fig.cite(tl + "/"), polls: fig.values(pj),
		pollsName: fig.cite(pj + "/")}
	if in.marks, err = marksOf(fig.values(tl), in.marksName); err != nil {
		return adoptedRun{adoptedGate: g}, err
	}
	if g.kill != "" {
		if in.kill, in.killName, err = readings.reading(g.kill); err != nil {
			return adoptedRun{adoptedGate: g}, err
		}
		wp := adoptedRows(g.gate, path.Base(g.worker))
		if in.worker, err = fig.text(wp); err != nil {
			return adoptedRun{adoptedGate: g}, err
		}
		in.workerName = fig.cite(wp + "/")
		ap := adoptedRows(g.gate, path.Base(g.attempts))
		in.attempts, in.attemptsName = fig.values(ap), fig.cite(ap+"/")
	}
	return adoptedOf(g, in)
}

// andJoin is words as a list: "a", "a and b", "a, b and c".
func andJoin(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// maxGap is the largest difference between neighbours of xs.
func maxGap(xs []float64) float64 {
	g := 0.0
	for i := 1; i < len(xs); i++ {
		g = math.Max(g, xs[i]-xs[i-1])
	}
	return g
}

// legendW is how wide lessonLegend sets items, the last one's gap included.
func legendW(items []legendItem) float64 {
	w := 0.0
	for _, it := range items {
		w += 24 + textW(it.label, 11, false) + 18
	}
	return w
}

// diamond draws the diamond that marks a moment above a bar, its point at y.
func diamond(s *SVG, x, y float64, title string) {
	s.Polygon([]Point{{x, y - 10}, {x + 4, y - 5}, {x, y}, {x - 4, y - 5}}, Style{Fill: Deep, Title: title})
}

// adopted draws G2, G4 and G5 as lanes against seconds from the wave: the wave and its items' ends, the gate's poll
// lines, and the worker beside it, KILLed in G4 and G5 and followed by another that replayed the gate.
func adopted(fsys fs.FS) (drawn, error) {
	ts, host, err := boot4Tables(fsys, adoptedTable)
	if err != nil {
		return drawn{}, err
	}
	readings, err := readFigTable(fsys, boot4ReadingsTable)
	if err != nil {
		return drawn{}, err
	}
	var runs []adoptedRun
	for _, g := range adoptedGates {
		r, err := rowsAdopted(ts[1], ts[0], readings, g)
		if err != nil {
			return drawn{}, err
		}
		runs = append(runs, r)
	}
	sources := tableSources(append(ts, readings)...)
	var kills, still, killed []string
	var advance, attempt int
	var last float64
	for _, r := range runs {
		if r.server != runs[0].server {
			return drawn{}, fmt.Errorf("%s ran server %s, %s server %s", r.gate, r.server, runs[0].gate, runs[0].server)
		}
		last = math.Max(last, r.post-r.wave)
		if r.kill == "" {
			still = append(still, r.step)
			continue
		}
		if attempt == 0 {
			attempt = r.attempt
		}
		if !slices.Contains(r.over, "wave") || r.attempt != attempt {
			return drawn{}, fmt.Errorf("%s: the wave is not among the activities on attempt %d", r.attempts, attempt)
		}
		kills, killed = append(kills, spanTo([]float64{r.killed - r.wave}, 1)), append(killed, r.step)
		if r.verdict == "ADVANCE" {
			advance++
		}
	}
	if len(kills) == 0 || len(still) == 0 {
		return drawn{}, fmt.Errorf("want gates with a KILL and without one, have %d and %d", len(kills), len(still))
	}
	nItems := len(runs[0].ended)
	for _, r := range runs {
		if len(r.ended) != nItems {
			return drawn{}, fmt.Errorf("%s ended %d items, %s %d", r.gate, len(r.ended), runs[0].gate, nItems)
		}
	}
	title := fmt.Sprintf("A worker KILLed %s s into the wave: the wave on attempt %d, %d of %d ADVANCE",
		andJoin(kills), attempt, advance, len(kills))
	sub := fmt.Sprintf("Boot 4's workflow gates on server %s, %s: %s with no KILL, %s with one", runs[0].server,
		host, andJoin(still), andJoin(killed))
	desc := "A lane a gate of boot 4 through laug gate run -workflow, " + strconv.Itoa(nItems) + " items on cells; " +
		"across, seconds from the gate's wave mark (timeline.jsonl). The upper bar is the wave, from its mark to its " +
		"wave end mark; the diamonds above it are the items' ended marks, the ticks under it the lines of the " +
		"gate's poll.jsonl. The row under it is this checkout's worker. " + andJoin(still) + " ran with one " +
		"throughout. In " + andJoin(killed) + " the batch sent KILL to the worker's process group, the solid line " +
		"(readings.json's killed_at), and started another; the worker row's middle segment, no worker, runs up to " +
		"the next one's Started Worker line, after it replayed the open gate (its step file). The words give the " +
		"activities temporal workflow show recorded on an attempt over 1 (each gate's attempts file in the pack's " +
		"extra/), the largest gap " +
		"between poll lines, and handed on, the post mark less the start."

	const rowH, workerH, laneStep = 16.0, 10.0, 98.0
	x, ticks := laneX(last)
	axisY := laneTop + float64(float64(len(runs))*laneStep)
	height := axisY + 40 + 34
	s := lessonFigure("adopted", title, sub, desc, sources, height)
	s.Text(x.At(0), laneTop-20, "wave", Font{Size: 11, Anchor: AnchorMiddle})
	s.Text(marginL, laneTop-20, "Above each wave: its items' ends", Font{Size: 11})
	for i, r := range runs {
		y := laneTop + float64(float64(i)*laneStep) + 10
		at := func(t float64) float64 { return x.At(t - r.wave) }
		wy := y + rowH + 9
		for _, t := range ticks {
			s.Line(x.At(t), y-12, x.At(t), wy+workerH+3, Style{Stroke: Silica})
		}
		s.Group(r.step + " " + r.gate + ": " + r.verdict)
		s.Text(marginL, y+10, r.step+"  "+r.gate, Font{Size: 10, Mono: true})
		word := Moss
		if r.verdict != "ADVANCE" {
			word = Ember
		}
		s.Text(marginL, y+26, r.verdict, Font{Size: 11, Bold: true, Colour: word})

		waveW := at(r.waveEnd) - at(r.wave)
		label := "wave"
		if r.kill != "" {
			label = fmt.Sprintf("wave, attempt %d", r.attempt)
		}
		s.Rect(at(r.wave), y, waveW, rowH, Style{Fill: Water, Stroke: Deep, Width: 0.5,
			Title: fmt.Sprintf("the wave, its mark to wave end: %s s", Num(r.waveEnd-r.wave))})
		if textW(label, 11, false)+8 < waveW {
			s.Text(at(r.wave)+4, y+12, label, Font{Size: 11})
		}
		for _, e := range r.ended {
			diamond(s, at(e.wall), y-1, fmt.Sprintf("%s +%s s", e.step, Num(e.wall-r.wave)))
		}
		s.Group(fmt.Sprintf("poll.jsonl's %d lines", len(r.polls)))
		for _, p := range r.polls {
			if px := at(p); px >= x.R0 && px <= x.R1 {
				s.Line(px, y+rowH+1, px, y+rowH+5, Style{Stroke: Deep})
			}
		}
		s.EndGroup()

		if r.kill == "" {
			s.Rect(x.R0, wy, at(r.post)-x.R0, workerH, Style{Fill: Silica, Stroke: Deep, Width: 0.5,
				Title: "one worker throughout"})
		} else {
			s.Rect(x.R0, wy, at(r.killed)-x.R0, workerH, Style{Fill: Silica, Stroke: Deep, Width: 0.5,
				Title: fmt.Sprintf("the first worker, to the KILL at +%s s", Num(r.killed-r.wave))})
			s.Rect(at(r.killed), wy, at(r.started)-at(r.killed), workerH, Style{Fill: Warm, Stroke: Deep, Width: 0.5,
				Title: fmt.Sprintf("no worker: %s s", Num(r.started-r.killed))})
			s.Rect(at(r.started), wy, at(r.post)-at(r.started), workerH, Style{Fill: Silica, Stroke: Deep,
				Width: 0.5, Title: fmt.Sprintf("the next worker, Started Worker at +%s s after it replayed gate-%s",
					Num(r.started-r.wave), r.gate)})
			s.Line(at(r.killed), y-4, at(r.killed), wy+workerH+4, Style{Stroke: Basalt, Width: 1.5,
				Title: fmt.Sprintf("KILL to the worker's process group, +%s s", Num(r.killed-r.wave))})
		}

		var words []string
		gap := fmt.Sprintf("poll lines at most %s s apart", Num(maxGap(r.polls)))
		wave := fmt.Sprintf("the wave %s s, handed on %s s after the start", Num(r.waveEnd-r.wave),
			Num(r.post-r.start))
		if r.kill == "" {
			words = []string{"no KILL, one worker throughout; " + wave, gap}
		} else {
			words = []string{
				fmt.Sprintf("KILL +%s s; the next worker replayed gate-%s, Started Worker %s s after the KILL",
					Num(r.killed-r.wave), r.gate, Num(r.started-r.killed)),
				fmt.Sprintf("attempt %d: %s; attempt 1: the %d others", r.attempt, strings.Join(r.over, ", "), r.ones),
				wave + "; " + gap,
			}
		}
		for j, w := range words {
			s.Text(marginL, wy+workerH+16+float64(float64(j)*13), w, Font{Size: 10})
			if textW(w, 10, false) > marginR-marginL {
				s.fail("the words under %s, %q, are wider than the figure", r.step, w)
			}
		}
		s.EndGroup()
	}
	s.AxisX(x, axisY, ticks, "seconds after the wave mark")
	items := []legendItem{
		{"the wave", swatch(Water)},
		{"an item ended", func(s *SVG, x, y float64) { diamond(s, x+9, y+5, "") }},
		{"a poll line", stroke(Deep, nil, 1)},
		{"a worker polling", swatch(Silica)}, {"no worker", swatch(Warm)}, {"KILL", stroke(Basalt, nil, 1.5)},
	}
	lessonLegend(s, marginL, height-18, items)
	if legendW(items) > marginR-marginL {
		s.fail("the legend is wider than the figure")
	}
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// The fake cell tests make obs-test runs (_obs/follow/fakecell_test.go) and the lines they log, as boot 4's F12
// kept them (cells/results/2026-10-24-boot4/steps/F12.txt).
const (
	obsTestSteps   = boot4Pack + "steps/F12.txt"
	obsStoreConfig = "_obs/prometheus.yml"
	fakeCellTest   = "_obs/follow/fakecell_test.go"
	stalenessOn    = "track_timestamps_staleness: true"
	stalenessOff   = "track_timestamps_staleness: false"
)

// logClock and logSecs are a time of day and a duration as the fake cell tests log them, to 0.1 s; logAt is a time
// as fig-staleness keeps it, its seconds after the first time of day the lines give (staleSeconds).
const (
	logClock = `([0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9])`
	logSecs  = `([0-9]+\.[0-9])`
	logAt    = `\+` + logSecs + ` s`
)

// staleClock is a time of day in a fake cell test's line, as F12 logged it.
var staleClock = regexp.MustCompile(`at ` + logClock + ` UTC`)

// staleSeconds is lines, the fake cell tests' lines of F12's step file, with each time of day, "at <hh:mm:ss.s> UTC",
// as "at +<s> s", its seconds after the first time of day the lines give, to 0.1 s: fig-staleness keeps no time of
// day (timeofday.go), and the figure reads the seconds.
func staleSeconds(lines []string) ([]string, error) {
	out := slices.Clone(lines)
	var zero time.Time
	var bad error
	for i, l := range lines {
		out[i] = staleClock.ReplaceAllStringFunc(l, func(m string) string {
			t, err := time.Parse("15:04:05.0", staleClock.FindStringSubmatch(m)[1])
			if err != nil {
				bad = err
				return m
			}
			if zero.IsZero() {
				zero = t
			}
			if t.Before(zero) {
				bad = fmt.Errorf("%s is before the first time of day, %s", m, zero.Format("15:04:05.0"))
			}
			return "at +" + strconv.FormatFloat(t.Sub(zero).Seconds(), 'f', 1, 64) + " s"
		})
	}
	return out, bad
}

var (
	staleOut = regexp.MustCompile(`^  staleness: numshards_gauge left the exposition at ` + logAt + `; ` +
		`kill-history read 512 missing on the cell at \+` + logSecs + ` s, in the store at \+` + logSecs + ` s, ` +
		logSecs + ` s after the cell; the store read 512 at each of its ([0-9]+) reads to \+` + logSecs + ` s$`)
	staleBack = regexp.MustCompile(`^  numshards_gauge back at ` + logAt + `: kill-history read 0 on the cell ` +
		`at \+` + logSecs + ` s, in the store at \+` + logSecs + ` s, ` + logSecs + ` s after the cell$`)
	staleControl = regexp.MustCompile(`^  negative control: numshards_gauge left the exposition at ` + logAt +
		`; kill-history read 512 missing on the cell at \+` + logSecs + ` s; the store read 0, recovered, at ` +
		`each of its ([0-9]+) reads to \+` + logSecs + ` s, and first read 512 at \+` + logSecs + ` s, when the 5 m ` +
		`lookback let the last sample go$`)
	stalePass = regexp.MustCompile(`^--- PASS: (TestFakeCellInTheStore|TestFakeCellStalenessControl) ` +
		`\(([0-9]+\.[0-9]+)s\)$`)
)

// staleRun is what the fake cell tests logged, each value in the text its line gives it: with the stack's config, the
// gauge out at out and back at back, each row's first reading after either, the store's lag behind the cell, and its
// reads of 512; in the control, the cell's 512, the store's reads of 0 and its first 512. Every time is in seconds
// from the gauge leaving the exposition, or coming back for the Back fields, but out, back and ctlOut, which are
// seconds after the first time of day the lines gave (staleSeconds). backAt is computed: back less out.
type staleRun struct {
	out, back, ctlOut                         string
	cellOut, storeOut, lagOut, reads, readsTo string
	cellBack, storeBack, lagBack              string
	ctlCell, ctlReads, ctlTo, ctlFlip         string
	inStore, control                          string // each test's seconds, as its PASS line gives them
	backAt                                    float64
}

// secsOf is a time the tests logged, in seconds.
func secsOf(v string) float64 {
	n, _ := strconv.ParseFloat(v, 64)
	return n
}

// oneMatch is the submatches of the one line of lines re matches, an error naming p when there is none or more.
func oneMatch(lines []string, p string, re *regexp.Regexp, what string) ([]string, error) {
	var found [][]string
	for _, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%s: %d lines of %s, want 1", p, len(found), what)
	}
	return found[0], nil
}

// readStale reads the fake cell tests' lines from boot 4's F12 step file, each time of day as its seconds
// (staleSeconds, staleOf).
func readStale(fsys fs.FS) (staleRun, error) {
	text, err := readText(fsys, obsTestSteps)
	if err != nil {
		return staleRun{}, err
	}
	lines, err := staleSeconds(strings.Split(text, "\n"))
	if err != nil {
		return staleRun{}, fmt.Errorf("%s: %w", obsTestSteps, err)
	}
	return staleOf(strings.Join(lines, "\n"), obsTestSteps)
}

// staleOf reads the fake cell tests' lines from text, F12's step file or the lines fig-staleness kept of it, named
// p.
func staleOf(text, p string) (staleRun, error) {
	lines := strings.Split(text, "\n")
	out, err := oneMatch(lines, p, staleOut, "the gauge leaving, stack's config")
	if err != nil {
		return staleRun{}, err
	}
	back, err := oneMatch(lines, p, staleBack, "the gauge back")
	if err != nil {
		return staleRun{}, err
	}
	ctl, err := oneMatch(lines, p, staleControl, "the negative control")
	if err != nil {
		return staleRun{}, err
	}
	var r staleRun
	pass := map[string]string{}
	for _, l := range lines {
		if m := stalePass.FindStringSubmatch(l); m != nil {
			if _, twice := pass[m[1]]; twice {
				return staleRun{}, fmt.Errorf("%s: two PASS lines of %s", p, m[1])
			}
			pass[m[1]] = m[2]
		}
	}
	r.inStore, r.control = pass["TestFakeCellInTheStore"], pass["TestFakeCellStalenessControl"]
	if r.inStore == "" || r.control == "" {
		return staleRun{}, fmt.Errorf("%s: want a PASS line of TestFakeCellInTheStore and TestFakeCellStalenessControl",
			p)
	}
	r.out, r.cellOut, r.storeOut, r.lagOut, r.reads, r.readsTo = out[1], out[2], out[3], out[4], out[5], out[6]
	r.back, r.cellBack, r.storeBack, r.lagBack = back[1], back[2], back[3], back[4]
	r.ctlOut, r.ctlCell, r.ctlReads, r.ctlTo, r.ctlFlip = ctl[1], ctl[2], ctl[3], ctl[4], ctl[5]
	if !(secsOf(r.back) > secsOf(r.out)) {
		return staleRun{}, fmt.Errorf("%s: the gauge left at +%s s and came back at +%s s", p, r.out, r.back)
	}
	r.backAt = secsOf(r.back) - secsOf(r.out)
	f := secsOf
	if !(f(r.cellOut) < f(r.storeOut) && f(r.storeOut) <= f(r.readsTo) && f(r.readsTo) < r.backAt &&
		f(r.cellBack) < f(r.storeBack) && f(r.ctlCell) < f(r.ctlTo) && f(r.ctlTo) < f(r.ctlFlip)) {
		return staleRun{}, fmt.Errorf("%s: the tests' times are out of their order", p)
	}
	return r, nil
}

// staleness draws make obs-test's fake cell in boot 4: kill-history's signal on the cell's Prometheus and in the store,
// against seconds from numshards_gauge leaving the exposition, with the stack's config and in the negative control.
func staleness(fsys fs.FS) (drawn, error) {
	ts, host, err := boot4Tables(fsys, stalenessTable)
	if err != nil {
		return drawn{}, err
	}
	steps := path.Base(obsTestSteps)
	text, err := ts[0].text(steps)
	if err != nil {
		return drawn{}, err
	}
	r, err := staleOf(text, ts[0].cite(steps+"/"))
	if err != nil {
		return drawn{}, err
	}
	conf, err := readText(fsys, obsStoreConfig)
	if err != nil {
		return drawn{}, err
	}
	if strings.Count(conf, stalenessOn) != 1 || strings.Contains(conf, stalenessOff) {
		return drawn{}, fmt.Errorf("%s: want %q once", obsStoreConfig, stalenessOn)
	}
	test, err := readText(fsys, fakeCellTest)
	if err != nil {
		return drawn{}, err
	}
	if !strings.Contains(test, `strings.Replace(conf, "`+stalenessOn+`", "`+stalenessOff+`", 1)`) {
		return drawn{}, fmt.Errorf("%s: the negative control does not set %q", fakeCellTest, stalenessOff)
	}
	cat, err := readText(fsys, faultCatalog)
	if err != nil {
		return drawn{}, err
	}
	signal, err := catalogField(cat, "kill-history", "signal_query")
	if err != nil {
		return drawn{}, err
	}
	sources := append(tableSources(ts...), obsStoreConfig, fakeCellTest, faultCatalog)

	f := secsOf
	title := fmt.Sprintf("The store read the gauge gone %s s after the cell; without staleness tracking, %s s",
		r.lagOut, Num(f(r.ctlFlip)-f(r.ctlCell)))
	sub := "make obs-test's fake cell on " + host + ": kill-history's signal on the cell and in the store"
	desc := "Two lanes, each a fake cell test of make obs-test in boot 4: a canned exposition serves numshards_gauge " +
		"512 to a Prometheus standing in for the cell's, which laug's store federates; then numshards_gauge " +
		"leaves the exposition, the solid line. Across, seconds from that moment. Each row is kill-history's " +
		"signal, faults/catalog.yaml's " + signal + ", read on the cell's Prometheus and in the store, its " +
		"segments named by their reading: 0 missing while the gauge serves 512; 512 missing once no sample of it " +
		"is left. The top lane is " +
		"TestFakeCellInTheStore, the store's config the stack's, " + stalenessOn + " (" + obsStoreConfig + "); the " +
		"gauge came back at the dashed line. The bottom lane is TestFakeCellStalenessControl, the same config with " +
		stalenessOff + " (" + fakeCellTest + "): the store kept the gauge's last sample and read 0, drawn faint from " +
		"the cell's 512 on, until the diamond, its first read of 512. Each time is the tests' own, to 0.1 s."

	const rowH, laneStep, labelR = 18.0, 116.0, 228.0
	hi := float64(30 * math.Ceil(math.Max(f(r.ctlFlip), r.backAt+f(r.storeBack))/30))
	var ticks []float64
	for k := 0; float64(30*k) <= hi; k++ {
		ticks = append(ticks, float64(30*k))
	}
	x := Scale{0, hi, 236, marginR}
	top := laneTop - 6
	axisY := top + float64(2*laneStep)
	height := axisY + 40 + 34
	s := lessonFigure("staleness", title, sub, desc, sources, height)
	type bar struct {
		from, to float64
		fill     Colour
		faint    bool
		label    string
	}
	row := func(y float64, name string, bars []bar) {
		s.Text(labelR, y+13, name, Font{Size: 11, Anchor: AnchorEnd})
		for _, b := range bars {
			w := x.At(b.to) - x.At(b.from)
			st := Style{Fill: b.fill, Stroke: Deep, Width: 0.5, Title: fmt.Sprintf("%s, +%s to +%s s", b.label,
				Num(b.from), Num(b.to))}
			if b.faint {
				st.FillOpacity, st.Dash = 0.45, []float64{3, 2}
			}
			s.Rect(x.At(b.from), y, w, rowH, st)
			if textW(b.label, 11, false)+8 < w {
				s.Text(x.At(b.from)+4, y+13, b.label, Font{Size: 11})
			}
		}
	}
	lane := func(i int, head string, words [2]string) float64 {
		y := top + float64(float64(i)*laneStep)
		s.Text(marginL, y+12, head, Font{Size: 11, Bold: true})
		if textW(head, 11, true) > marginR-marginL {
			s.fail("the lane's heading %q is wider than the figure", head)
		}
		for _, t := range ticks {
			s.Line(x.At(t), y+16, x.At(t), y+float64(2*rowH)+34, Style{Stroke: Silica})
		}
		for j, w := range words {
			s.Text(marginL, y+84+float64(float64(j)*13), w, Font{Size: 10})
			if textW(w, 10, false) > marginR-marginL {
				s.fail("the words %q are wider than the figure", w)
			}
		}
		return y + 22
	}
	const gone, none = "512 missing", "0 missing"
	end := r.backAt + f(r.storeBack)
	y := lane(0, "TestFakeCellInTheStore, "+r.inStore+" s: the stack's config, "+stalenessOn, [2]string{
		fmt.Sprintf("512 missing on the cell at +%s s, in the store at +%s s, %s s after; the store read 512 at each "+
			"of its %s reads to +%s s", r.cellOut, r.storeOut, r.lagOut, r.reads, r.readsTo),
		fmt.Sprintf("the gauge back at +%s s: 0 on the cell %s s after it, in the store %s s after it, %s s "+
			"after the cell", Num(r.backAt), r.cellBack, r.storeBack, r.lagBack),
	})
	s.Group("the stack's config")
	row(y, "the cell's Prometheus", []bar{{0, f(r.cellOut), Silica, false, none},
		{f(r.cellOut), r.backAt + f(r.cellBack), Warm, false, gone},
		{r.backAt + f(r.cellBack), end, Silica, false, none}})
	row(y+rowH+8, "the store", []bar{{0, f(r.storeOut), Silica, false, none},
		{f(r.storeOut), end, Warm, false, gone}})
	s.Line(x.At(0), y-6, x.At(0), y+float64(2*rowH)+14, Style{Stroke: Basalt, Width: 1.5,
		Title: "numshards_gauge left the exposition, +0 s"})
	s.Line(x.At(r.backAt), y-6, x.At(r.backAt), y+float64(2*rowH)+14, Style{Stroke: Basalt, Width: 1.5,
		Dash: []float64{4, 3}, Title: "numshards_gauge back, +" + Num(r.backAt) + " s"})
	s.EndGroup()

	y = lane(1, "TestFakeCellStalenessControl, "+r.control+" s: the negative control, "+stalenessOff, [2]string{
		fmt.Sprintf("512 missing on the cell at +%s s; the store read 0, recovered, at each of its %s reads to +%s s",
			r.ctlCell, r.ctlReads, r.ctlTo),
		fmt.Sprintf("and first read 512 at +%s s, when the 5 m lookback let the last sample go", r.ctlFlip),
	})
	s.Group("the negative control")
	row(y, "the cell's Prometheus", []bar{{0, f(r.ctlCell), Silica, false, none},
		{f(r.ctlCell), f(r.ctlFlip), Warm, false, gone}})
	row(y+rowH+8, "the store", []bar{{0, f(r.ctlCell), Silica, false, none},
		{f(r.ctlCell), f(r.ctlFlip), Silica, true, "0, the last sample kept"}})
	s.Line(x.At(0), y-6, x.At(0), y+float64(2*rowH)+14, Style{Stroke: Basalt, Width: 1.5,
		Title: "numshards_gauge left the exposition again, " + r.ctlOut + " s after it first left"})
	diamond(s, x.At(f(r.ctlFlip)), y+rowH+8, "the store's first read of 512, +"+r.ctlFlip+" s")
	s.EndGroup()

	s.AxisX(x, axisY, ticks, "seconds after numshards_gauge left the exposition")
	items := []legendItem{
		{none, swatch(Silica)}, {gone, swatch(Warm)},
		{"0 from the last sample", func(s *SVG, x, y float64) {
			s.Rect(x, y-5, 18, 10, Style{Fill: Silica, FillOpacity: 0.45, Stroke: Deep, Width: 0.5,
				Dash: []float64{3, 2}})
		}},
		{"gauge left", stroke(Basalt, nil, 1.5)}, {"gauge back", stroke(Basalt, []float64{4, 3}, 1.5)},
		{"first 512", func(s *SVG, x, y float64) { diamond(s, x+9, y+5, "") }},
	}
	lessonLegend(s, marginL, height-18, items)
	if legendW(items) > marginR-marginL {
		s.fail("the legend is wider than the figure")
	}
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// boot4Cancels are boot 4's two cancels, each of a fake workflow gate at its wave mark plus 30 s through laug gate
// cancel: F5 on this tree, F7 in a scratch commit (cells/results/2026-10-24-boot4.txt, F5 and F7).
var boot4Cancels = []struct{ step, gate string }{
	{"F5", "g20261024-102139"},
	{"F7", "g20261024-102311"},
}

// cancelHeading is the transcript's heading of a cancel's step, which names its WaitForCancellation.
var cancelHeading = regexp.MustCompile(`(?m)^-- (F[0-9]+): gate cancel, WaitForCancellation (true|false)\b`)

// cancelModes is each cancel's WaitForCancellation by its step, as text, the transcript or the lines of it
// fig-cancel-path kept, named p, gives it in the step's heading; a step with two headings is an error.
func cancelModes(text, p string) (map[string]string, error) {
	modes := map[string]string{}
	for _, m := range cancelHeading.FindAllStringSubmatch(text, -1) {
		if _, twice := modes[m[1]]; twice {
			return nil, fmt.Errorf("%s: two headings of %s", p, m[1])
		}
		modes[m[1]] = m[2]
	}
	return modes, nil
}

// cancelRun is one cancel as the events the batch kept (types and times) and its gate's timeline record it. Every
// stamp is in seconds from the cancel request's event.
type cancelRun struct {
	step, gate, mode string
	sinceWave        float64      // the request less the wave mark
	actReq, actDone  []float64    // ACTIVITY_TASK_CANCEL_REQUESTED, ACTIVITY_TASK_CANCELED
	stops            [][2]float64 // each activity scheduled after the request, to its ACTIVITY_TASK_COMPLETED
	stopFirst        bool         // the first was scheduled before any ACTIVITY_TASK_CANCELED
	term, closed     float64      // the timeline's watchdog TERM; WORKFLOW_EXECUTION_COMPLETED
	ended            []float64    // the timeline's ended <key> marks
	files            []string
}

// cancelEvent is one of the events the batch kept of a cancel: its id, type and time, as recorded.
type cancelEvent struct{ id, typ, time string }

// readCancel reads one cancel from its files (cancelOf): the events the batch kept, and the gate's timeline.
func readCancel(fsys fs.FS, step, gate, mode string) (cancelRun, error) {
	ev, tl := cancelEvents(step), cancelTimeline(step, gate)
	v, err := readJSON(fsys, ev)
	if err != nil {
		return cancelRun{step: step, gate: gate, mode: mode}, err
	}
	all, _ := v.Arr()
	var es []cancelEvent
	for _, e := range all {
		es = append(es, cancelEvent{str(at(e, "id")), str(at(e, "type")), str(at(e, "time"))})
	}
	ms, err := readMarks(fsys, tl)
	if err != nil {
		return cancelRun{step: step, gate: gate, mode: mode}, err
	}
	return cancelOf(step, gate, mode, es, ev, ms, tl, []string{ev, tl})
}

// The files the batch kept of the cancel of step: its events, and its gate's timeline.
func cancelEvents(step string) string {
	return boot4Pack + "extra/" + strings.ToLower(step) + "-events.json"
}
func cancelTimeline(step, gate string) string {
	return boot4Pack + "extra/" + strings.ToLower(step) + "-" + gate + "-timeline.jsonl"
}

// cancelOf reads one cancel: its events, es, named ev, which must run 1 to n with one cancel request and close with
// the workflow's completion, and the activities scheduled after the request one at a time; and its timeline's marks,
// ms, named tl.
func cancelOf(step, gate, mode string, es0 []cancelEvent, ev string, ms []tmark, tl string, files []string) (
	cancelRun, error) {
	r := cancelRun{step: step, gate: gate, mode: mode, files: files}
	type event struct {
		typ string
		t   float64
	}
	var es []event
	req := -1
	for i, e := range es0 {
		typ, id := strings.TrimPrefix(e.typ, "EVENT_TYPE_"), e.id
		t, err := time.Parse(time.RFC3339Nano, e.time)
		if err != nil || id != strconv.Itoa(i+1) || typ == "" {
			return r, fmt.Errorf("%s: entry %d is not event %d with its type and time", ev, i+1, i+1)
		}
		if typ == "WORKFLOW_EXECUTION_CANCEL_REQUESTED" {
			if req >= 0 {
				return r, fmt.Errorf("%s: two cancel requests", ev)
			}
			req = i
		}
		es = append(es, event{typ, unixOf(t)})
	}
	if req < 0 || es[len(es)-1].typ != "WORKFLOW_EXECUTION_COMPLETED" {
		return r, fmt.Errorf("%s: want a cancel request, and the workflow's completion last", ev)
	}
	t0 := es[req].t
	var sched, done []float64
	for _, e := range es[req+1:] {
		switch e.typ {
		case "ACTIVITY_TASK_CANCEL_REQUESTED":
			r.actReq = append(r.actReq, e.t-t0)
		case "ACTIVITY_TASK_CANCELED":
			r.actDone = append(r.actDone, e.t-t0)
		case "ACTIVITY_TASK_SCHEDULED":
			if len(sched) == 0 {
				r.stopFirst = len(r.actDone) == 0
			}
			sched = append(sched, e.t-t0)
		case "ACTIVITY_TASK_COMPLETED":
			done = append(done, e.t-t0)
		}
	}
	r.closed = es[len(es)-1].t - t0
	if len(r.actReq) == 0 || len(r.actReq) != len(r.actDone) {
		return r, fmt.Errorf("%s: %d activities asked to cancel, %d canceled", ev, len(r.actReq), len(r.actDone))
	}
	if len(sched) == 0 || len(sched) != len(done) {
		return r, fmt.Errorf("%s: %d activities scheduled after the request, %d completed", ev, len(sched), len(done))
	}
	for k := range sched {
		if done[k] < sched[k] || k+1 < len(sched) && sched[k+1] < done[k] {
			return r, fmt.Errorf("%s: the activities scheduled after the request did not run one at a time", ev)
		}
		r.stops = append(r.stops, [2]float64{sched[k], done[k]})
	}
	wave, err := markOf(ms, tl, "wave")
	if err != nil {
		return r, err
	}
	term, err := markOf(ms, tl, "watchdog TERM")
	if err != nil {
		return r, err
	}
	r.sinceWave, r.term = t0-wave, term-t0
	for _, m := range endedMarks(ms) {
		r.ended = append(r.ended, m.wall-t0)
	}
	if len(r.ended) == 0 {
		return r, fmt.Errorf("%s: no item ended", tl)
	}
	return r, nil
}

// cancelsPanel is boot 4's two cancels as lanes against seconds from the cancel request's event: the pending
// activities from their cancel requests to their cancellations, the activities scheduled after the request, the
// timeline's watchdog TERM and items' ends, and the workflow's completion.
func cancelsPanel(fsys fs.FS, _ string) ([]string, func(s *SVG, x, y, w, h float64), error) {
	ts, host, err := boot4Tables(fsys, cancelTable)
	if err != nil {
		return nil, nil, err
	}
	fig := ts[0]
	tp := path.Base(boot4Transcript)
	text, err := fig.text(tp)
	if err != nil {
		return nil, nil, err
	}
	modes, err := cancelModes(text, fig.cite(tp+"/"))
	if err != nil {
		return nil, nil, err
	}
	var runs []cancelRun
	for _, c := range boot4Cancels {
		mode, ok := modes[c.step]
		if !ok {
			return nil, nil, fmt.Errorf("%s: no heading of %s naming its WaitForCancellation", fig.cite(tp+"/"),
				c.step)
		}
		ev, tl := path.Base(cancelEvents(c.step)), path.Base(cancelTimeline(c.step, c.gate))
		var es []cancelEvent
		for _, e := range fig.values(ev) {
			es = append(es, cancelEvent{str(at(e, "id")), str(at(e, "type")), str(at(e, "time"))})
		}
		ms, err := marksOf(fig.values(tl), fig.cite(tl+"/"))
		if err != nil {
			return nil, nil, err
		}
		r, err := cancelOf(c.step, c.gate, mode, es, fig.cite(ev+"/"), ms, fig.cite(tl+"/"), nil)
		if err != nil {
			return nil, nil, err
		}
		runs = append(runs, r)
	}
	sources := tableSources(ts...)
	var since, last []float64
	for _, r := range runs {
		since, last = append(since, r.sinceWave), append(last, r.closed)
	}
	heading := fmt.Sprintf("Boot 4 on %s: a fake gate canceled %s s into its wave, in each mode", host,
		spanTo(since, 2))
	const labelW, laneStep, rowH = 170.0, 82.0, 12.0
	draw := func(s *SVG, x, y, w, h float64) {
		hi := float64(0.2 * math.Ceil(slices.Max(last)/0.2))
		xs := Scale{0, hi, x + labelW, x + w}
		ticks := Ticks(0, hi, int(math.Round(hi/0.2)))
		s.Group("boot 4's two cancels")
		s.Text(x+4, y+14, heading, Font{Size: 11, Bold: true})
		lanesTop := y + 40
		for i, r := range runs {
			ly := lanesTop + float64(float64(i)*laneStep)
			for _, t := range ticks {
				s.Line(xs.At(t), ly-12, xs.At(t), ly+float64(2*rowH)+8, Style{Stroke: Silica})
			}
			s.Group(r.step + ": WaitForCancellation " + r.mode)
			s.Text(x+4, ly+9, r.step+"  "+r.gate, Font{Size: 10, Mono: true})
			s.Text(x+4, ly+23, "WaitForCancellation "+r.mode, Font{Size: 10, Bold: true})
			from, to := slices.Min(r.actReq), slices.Max(r.actDone)
			s.Rect(xs.At(from), ly, xs.At(to)-xs.At(from), rowH, Style{Fill: Water, Stroke: Deep, Width: 0.5,
				Title: fmt.Sprintf("the %d pending activities: cancel requested +%s s, canceled +%s s", len(r.actReq),
					span(r.actReq), span(r.actDone))})
			for _, t := range r.actDone {
				s.Line(xs.At(t), ly, xs.At(t), ly+rowH, Style{Stroke: Deep, Width: 1.5})
			}
			for k, st := range r.stops {
				s.Rect(xs.At(st[0]), ly+rowH+4, xs.At(st[1])-xs.At(st[0]), rowH, Style{Fill: Warm, Stroke: Deep,
					Width: 0.5, Title: fmt.Sprintf("activity %d of %d scheduled after the request: +%s to +%s s", k+1,
						len(r.stops), Num(st[0]), Num(st[1]))})
			}
			for _, t := range r.ended {
				diamond(s, xs.At(t), ly-1, "an item ended, +"+Num(t)+" s")
			}
			s.Line(xs.At(r.term), ly-4, xs.At(r.term), ly+float64(2*rowH)+8, Style{Stroke: Basalt, Width: 1.5,
				Dash: []float64{4, 3}, Title: "the timeline's watchdog TERM, +" + Num(r.term) + " s"})
			s.Line(xs.At(r.closed), ly-4, xs.At(r.closed), ly+float64(2*rowH)+8, Style{Stroke: Basalt, Width: 2.5,
				Title: "WORKFLOW_EXECUTION_COMPLETED, +" + Num(r.closed) + " s"})
			order := "after"
			if r.stopFirst {
				order = "before"
			}
			words := [2]string{
				fmt.Sprintf("the next activity scheduled +%s s, %s the %d pending ones were canceled, +%s s",
					Num(r.stops[0][0]), order, len(r.actDone), span(r.actDone)),
				fmt.Sprintf("watchdog TERM +%s s; items ended +%s s; the workflow closed +%s s", Num(r.term),
					span(r.ended), Num(r.closed)),
			}
			for j, wd := range words {
				s.Text(x+4, ly+float64(2*rowH)+22+float64(float64(j)*13), wd, Font{Size: 10})
				if textW(wd, 10, false)+8 > w {
					s.fail("the cancels panel's words %q do not fit %v", wd, w)
				}
			}
			s.EndGroup()
		}
		ay := lanesTop + float64(float64(len(runs))*laneStep) - 10
		s.AxisX(xs, ay, ticks, "seconds after the workflow's cancel request")
		items := []legendItem{
			{"canceled activities", swatch(Water)},
			{"scheduled after the request", swatch(Warm)},
			{"watchdog TERM", stroke(Basalt, []float64{4, 3}, 1.5)},
			{"an item ended", func(s *SVG, x, y float64) { diamond(s, x+9, y+5, "") }},
			{"closed", stroke(Basalt, nil, 2.5)},
		}
		lessonLegend(s, x+4, ay+56, items)
		s.EndGroup()
		if textW(heading, 11, true)+8 > w || ay+60-y > h || legendW(items)+4 > w {
			s.fail("the cancels panel does not fit %v by %v", w, h)
		}
	}
	return sources, draw, nil
}
