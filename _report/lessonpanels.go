package report

// lessonpanels.go draws the data a diagram's spec places as a panel: the eager A/B's readings on server 1.31.0 and
// 1.32.1 (eager-ab), what each of history's three piles left on the shards under the server's own fault injector
// (three-piles), and R7's tolerances (incumbent-arm); toxic-fuse's panel, boot 2's sampler at each toxicity, is in
// lessonfaults.go, and cancel-path's, boot 4's two cancels, in lessonworkflow.go. A panel reads committed files it
// names one by one, as the figures do, a summary table under docs/tables (lessonrows.go) or a file, and its numbers
// come from them, never from the spec.

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// A panelFunc reads a panel's files and returns them with the function that draws it into a rectangle.
type panelFunc func(fsys fs.FS, arg string) (sources []string, draw func(s *SVG, x, y, w, h float64), err error)

// lessonPanel is the panel a spec names.
func lessonPanel(name string) (panelFunc, bool) {
	switch name {
	case "eager":
		return eagerPanel, true
	case "piles":
		return pilesPanel, true
	case "tolerances":
		return tolerancesPanel, true
	case "toxicities":
		return toxicitiesPanel, true
	case "cancels":
		return cancelsPanel, true
	}
	return nil, false
}

// abGates are the incumbent arm's A/B gates, server 1.32.1 against 1.31.0 on the lab host: the calibration session's,
// in recording mode (docs/tables/records.jsonl#cells/results/2026-10-17-lab-arm-calibration.txt line 566), and the
// validation session's three, R7 judging
// (docs/tables/records.jsonl#cells/results/2026-10-18-lab-arm-validation.txt lines 418-420).
var abGates = []string{"g20261017-222254", "g20261018-090337", "g20261018-091444", "g20261018-092540"}

// bootLoads are boot 1's six loads on fresh cells, three on each release (cells/results/2026-10-23-boot1.txt, D), on
// the lab host.
var bootLoads = []string{"lab1-v1310-1", "lab1-v1310-2", "lab1-v1310-3", "lab1-v1321-1", "lab1-v1321-2",
	"lab1-v1321-3"}

// eagerPanel is a table of the readings eager execution moves, by release: activity schedule-to-start p99 and
// workflow end-to-end p95 in boot 1's loads and in the A/B gates, and the gates' d. The loads are read from their
// loads rows, their host from src/hosts.tsv; the gates from their gates and compare rows.
func eagerPanel(fsys fs.FS, _ string) ([]string, func(s *SVG, x, y, w, h float64), error) {
	type side struct {
		act, e2e []float64
		none, n  int // loads or gates with no activity schedule-to-start, and all of them
	}
	ts, err := readFigTables(fsys, "loads", "gates", "compare")
	if err != nil {
		return nil, nil, err
	}
	loads, gates, compare := ts[0], ts[1], ts[2]
	load := map[string]*side{}
	var tags []string
	for _, id := range bootLoads {
		lv, err := loads.row(id)
		if err != nil {
			return nil, nil, err
		}
		p := loads.cite(id)
		tag := str(at(lv, "server_tag"))
		if tag == "" {
			return nil, nil, fmt.Errorf("%s: no server_tag", p)
		}
		if err := onLabHost(fsys, id); err != nil {
			return nil, nil, err
		}
		if load[tag] == nil {
			load[tag] = &side{}
			tags = append(tags, tag)
		}
		sd := load[tag]
		sd.n++
		e2e, err := need(lv, p, "readings", "workflow_end_to_end_p95_ms")
		if err != nil {
			return nil, nil, err
		}
		sd.e2e = append(sd.e2e, e2e)
		if a := num(at(lv, "readings", "activity_schedule_to_start_p99_ms")); a.OK {
			sd.act = append(sd.act, a.F)
		} else if at(lv, "readings", "activity_schedule_to_start_p99_ms").Kind() == jsonx.KindNull {
			sd.none++
		}
	}
	if len(tags) != 2 {
		return nil, nil, fmt.Errorf("boot 1's loads ran %d releases, want 2", len(tags))
	}
	slices.Sort(tags) // 1.31.0, then 1.32.1: the incumbent first
	inc, rel := tags[0], tags[1]
	var gIncAct, gIncE2E, gRelE2E, gD []float64
	gNone := 0
	for _, id := range abGates {
		r, err := rowsCompared(gates, compare, id)
		if err != nil {
			return nil, nil, err
		}
		if r.server != rel || r.incumbent != inc {
			return nil, nil, fmt.Errorf("%s: %s against %s, want %s against %s", r.path, r.server, r.incumbent, rel,
				inc)
		}
		if err := labGate(gates, id); err != nil {
			return nil, nil, err
		}
		for _, row := range r.rows {
			switch row.Name {
			case "activity_schedule_to_start_p99_ms":
				if !row.Incumbent.OK {
					return nil, nil, fmt.Errorf("%s: no incumbent %s", r.rowsPath, row.Name)
				}
				gIncAct = append(gIncAct, row.Incumbent.F)
				if !row.Release.OK {
					gNone++
				}
			case "workflow_end_to_end_p95_ms":
				if !row.Incumbent.OK || !row.Release.OK || !row.D.OK {
					return nil, nil, fmt.Errorf("%s: %s without both readings and d", r.rowsPath, row.Name)
				}
				gIncE2E, gRelE2E, gD = append(gIncE2E, row.Incumbent.F), append(gRelE2E, row.Release.F),
					append(gD, row.D.F)
			}
		}
	}
	if len(gIncAct) != len(abGates) || len(gD) != len(abGates) {
		return nil, nil, fmt.Errorf("the A/B gates hold %d activity rows and %d end-to-end rows, want %d each",
			len(gIncAct), len(gD), len(abGates))
	}
	sources := append(tableSources(ts...), hostsTSV)
	notProduced := func(none, n int) string {
		return fmt.Sprintf("none recorded, %d of %d", none, n)
	}
	ms := func(xs []float64) string { return span(xs) + " ms" }
	li, lr := load[inc], load[rel]
	rows := [][3]string{
		{"", "server " + inc, "server " + rel},
		{fmt.Sprintf("activity schedule-to-start p99, boot 1's %d loads", li.n+lr.n), ms(li.act),
			notProduced(lr.none, lr.n)},
		{fmt.Sprintf("the same, the arm's %d A/B gates", len(abGates)), ms(gIncAct), notProduced(gNone, len(abGates))},
		{"workflow end-to-end p95, boot 1's loads", ms(li.e2e), ms(lr.e2e)},
		{"the same, the A/B gates", ms(gIncE2E), ms(gRelE2E)},
		{"d, end-to-end p95, the A/B gates", "", ms(gD)},
	}
	if li.none > 0 || len(lr.act) > 0 {
		return nil, nil, fmt.Errorf("boot 1's %s loads recorded activity schedule-to-start in %d of %d, %s's in %d",
			inc, len(li.act), li.n, rel, len(lr.act))
	}
	draw := func(s *SVG, x, y, w, h float64) {
		c1, c2 := x+float64(w*0.5), x+float64(w*0.76)
		rowH := h / float64(len(rows))
		s.Group("the readings by release")
		for i, r := range rows {
			ty := y + float64(float64(i)*rowH) + rowH/2 + 4
			if i > 0 {
				s.Line(x, y+float64(float64(i)*rowH), x+w, y+float64(float64(i)*rowH), Style{Stroke: Silica})
			}
			head := i == 0
			for j, cx := range []float64{x + 4, c1, c2} {
				if r[j] != "" {
					s.Text(cx, ty, r[j], Font{Size: 11, Bold: head && j > 0, Mono: !head && j > 0})
				}
			}
		}
		s.EndGroup()
		for _, r := range rows {
			if textW(r[0], 11, false)+8 > c1-x || textW(r[1], 11, true)+8 > c2-c1 || textW(r[2], 11, true) > x+w-c2 {
				s.fail("the eager panel's row %q does not fit %v", r[0], w)
			}
		}
	}
	return sources, draw, nil
}

// pileRauns are the server fault injector's rauns at 1% on UpdateWorkflowExecution, and the control, boot 6's on the
// lab host, an m8i.8xlarge (cells/results/2026-10-27-boot6.txt, AUD), by the pile each error falls in; all 27
// ADVANCEd.
var pileRauns = map[string][]string{
	"did-not-happen": {"wf-update-resource-exhausted-1", "wf-update-resource-exhausted-2",
		"wf-update-resource-exhausted-3"},
	"owner-lost": {"wf-update-ownership-lost-1", "wf-update-ownership-lost-2", "wf-update-ownership-lost-3"},
	"cannot-know": {"wf-update-timeout-1", "wf-update-timeout-2", "wf-update-timeout-3", "wf-update-deadline-1",
		"wf-update-deadline-2", "wf-update-deadline-3", "wf-update-execute-timeout-1", "wf-update-execute-timeout-2",
		"wf-update-execute-timeout-3"},
	"unavailable": {"wf-update-unavailable-1", "wf-update-unavailable-2", "wf-update-unavailable-3"},
	"control":     {"history-recreate-1", "history-recreate-2", "history-recreate-3"},
}

// pilesPanel is what one pile's rauns left while the fault held, from each raun's own readings in its rauns row: shard
// contexts closed, UpdateShard calls and failed client starts, each over the during window; the host from
// src/hosts.tsv.
func pilesPanel(fsys fs.FS, arg string) ([]string, func(s *SVG, x, y, w, h float64), error) {
	ids, ok := pileRauns[arg]
	if !ok {
		return nil, nil, fmt.Errorf("arg=%q is none of the piles", arg)
	}
	rauns, err := readFigTable(fsys, "rauns")
	if err != nil {
		return nil, nil, err
	}
	var closed, calls, failed []float64
	for _, id := range ids {
		v, err := rauns.row("raun-lab6-" + id)
		if err != nil {
			return nil, nil, err
		}
		p := rauns.cite("raun-lab6-" + id)
		if err := onLabHost(fsys, "raun-lab6-"+id); err != nil {
			return nil, nil, err
		}
		if f := str(at(v, "fault")); f+"-"+id[len(id)-1:] != id {
			return nil, nil, fmt.Errorf("%s: fault %q", p, f)
		}
		for _, k := range []struct {
			to   *[]float64
			keys []string
		}{
			{&closed, []string{"readings", "during", "shard_contexts_closed"}},
			{&calls, []string{"readings", "during", "update_shard"}},
			{&failed, []string{"iterations_failed", "during"}},
		} {
			f, err := need(v, p, k.keys...)
			if err != nil {
				return nil, nil, err
			}
			*k.to = append(*k.to, f)
		}
	}
	sources := append(tableSources(rauns), hostsTSV)
	lines := []string{
		strconv.Itoa(len(ids)) + " rauns, while the fault held:",
		"shard contexts closed: " + wholeSpan(closed),
		"UpdateShard calls: " + wholeSpan(calls),
		"failed client starts: " + wholeSpan(failed),
	}
	if arg == "control" {
		lines[0] = strconv.Itoa(len(ids)) + " rauns, two restarts, no fault:"
	}
	draw := func(s *SVG, x, y, w, h float64) {
		s.Group(strings.Join(lines, " "))
		for i, l := range lines {
			f := Font{Size: 11}
			if i == 0 {
				f.Bold = true
			}
			s.Text(x+4, y+14+float64(float64(i)*15), l, f)
			if textW(l, 11, i == 0)+8 > w || float64(float64(len(lines))*15)+4 > h {
				s.fail("the panel's line %q does not fit %v by %v", l, w, h)
			}
		}
		s.EndGroup()
	}
	return sources, draw, nil
}

// tolerancesPanel lists R7's compared readings and their tolerances, as load/steady-state.yaml's compare rows hold
// them: one line a row, its name, its tolerance and its scale.
func tolerancesPanel(fsys fs.FS, _ string) ([]string, func(s *SVG, x, y, w, h float64), error) {
	data, err := fs.ReadFile(fsys, armSet)
	if err != nil {
		return nil, nil, err
	}
	var names, tols []string
	for _, l := range strings.Split(string(data), "\n") {
		t, ok := strings.CutPrefix(strings.TrimSpace(l), "- {name: ")
		if !ok || !strings.Contains(t, "tolerance: ") {
			continue
		}
		name, rest, _ := strings.Cut(t, ",")
		_, scale, _ := strings.Cut(rest, "scale: ")
		scale, _, _ = strings.Cut(scale, ",")
		_, tol, _ := strings.Cut(rest, "tolerance: ")
		tol = strings.TrimSuffix(strings.TrimSpace(tol), "}")
		if _, err := strconv.ParseFloat(tol, 64); err != nil {
			return nil, nil, fmt.Errorf("%s: %s's tolerance %q is not a number", armSet, name, tol)
		}
		if scale == "ms" {
			tol += " ms"
		}
		label, ok := readingLabels[name]
		if !ok {
			label = name
		}
		names, tols = append(names, label), append(tols, tol)
	}
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("%s: no compare row with a tolerance", armSet)
	}
	draw := func(s *SVG, x, y, w, h float64) {
		s.Group("R7's tolerances")
		s.Text(x+4, y+14, "R7's tolerances, ms or a share", Font{Size: 11, Bold: true})
		for i := range names {
			ly := y + 32 + float64(float64(i)*15)
			s.Text(x+4, ly, names[i], Font{Size: 11})
			s.Text(x+w-4, ly, tols[i], Font{Size: 11, Mono: true, Anchor: AnchorEnd})
			if textW(names[i], 11, false)+textW(tols[i], 11, true)+16 > w || float64(float64(len(names)+1)*15)+8 > h {
				s.fail("the tolerances panel's row %q does not fit %v by %v", names[i], w, h)
			}
		}
		s.EndGroup()
	}
	return []string{armSet}, draw, nil
}

// readingLabels are the compared readings as a panel names them, shorter than their names in the records.
var readingLabels = map[string]string{
	"workflow_task_schedule_to_start_p99_ms": "workflow task s2s p99",
	"persistence_latency_p99_ms":             "persistence p99",
	"activity_schedule_to_start_over_10ms":   "activity s2s past 10 ms",
	"frontend_request_over_10ms":             "frontend past 10 ms",
	"workflow_end_to_end_over_50ms":          "end-to-end past 50 ms",
}

// hostsTSV is the hand input that names the host each family of rauns and loads ran on (checkHosts).
const hostsTSV = DocsDir + "/" + TablesDir + "/" + SrcDir + "/" + srcHosts

// labHostPhrase is docs/RECORDS.md's phrase for the lab host, the first of hostPhrases, as src/hosts.tsv writes it.
var labHostPhrase = hostPhrases[0].phrase

// onLabHost refuses run id, a rauns or loads row's id, unless the first line of src/hosts.tsv whose glob matches it
// names the lab host: a raun's or a load's JSON does not record its host, and report -tables checks each line
// against the transcript line it cites.
func onLabHost(fsys fs.FS, id string) error {
	lines, err := srcLines(fsys, hostsTSV)
	if err != nil {
		return err
	}
	for _, l := range lines {
		f := strings.Split(l.text, "\t")
		if len(f) != 3 {
			continue
		}
		if ok, _ := path.Match(f[0], path.Base(id)); !ok {
			continue
		}
		if f[1] != labHostPhrase {
			return fmt.Errorf("%s:%d: %s ran on %s, not %s", hostsTSV, l.n, id, f[1], labHostPhrase)
		}
		return nil
	}
	return fmt.Errorf("%s: no line names the host of %s", hostsTSV, id)
}
