package follow

// The gate's records as series (docs/DESIGN.md section 9): one renderer, live on GET /metrics and later with
// -openmetrics for bin/obs backfill. Every value is copied from the gate's files. The only arithmetic is the
// difference of two times read (a step's length), which step is newest, the deadline (t0 plus the plan's deadline_s,
// as the watchdog reads it) and counting the projects of a poll. No reading, mean, band, rule or pass is computed here.
//
// Each series knows when it appears in the gate's own time: a time it holds appears at that time; the files of an
// item at its ended line; verdict.json's at the combine line; harness.json at the wave line. Live, everything that
// is there is shown; -openmetrics shows each series from its time on, at every timeline line and every 60 s between,
// in whole seconds: promtool check metrics reads timestamps as integers (seen with promtool 2.53.0: a
// fractional one is "expected integer as timestamp"), and two samples of one series in one second would be two at one
// timestamp. A poll's series are at that poll's own time only.
//
// No label or value holds "/": the plan label is a base name, a path is never read into a label, and a series whose
// label would hold one is dropped and counted. So is a series with one label name twice, which the store refuses with
// the whole scrape (seen with Prometheus 2.53.0: label name "fake" is not unique: invalid sample).

import (
	"bytes"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// series is one sample's name, labels in order, value, the gate time it appears (NaN: not yet) and, for a poll, its
// own time.
type series struct {
	name   string
	labels [][2]string
	value  float64
	at     float64
	ts     float64
}

// families is every family follow serves, in the order it serves them, with its HELP and TYPE.
var families = []struct{ name, typ, help string }{
	{"laug_gate_info", "gauge", "One gate as its gate.json, plan.json and thresholds.json name it; the value is 1."},
	{"laug_gate_clock_seconds", "gauge", "The gate's clock marks t0 and t_post from clock.json, Unix seconds."},
	{"laug_gate_deadline_seconds", "gauge", "t0 plus the plan's deadline_s, when the watchdog stops what is left."},
	{"laug_gate_step_seconds", "gauge", "When each timeline line was written, Unix seconds."},
	{"laug_gate_step_active", "gauge", "1 for the newest timeline line, 0 for the others."},
	{"laug_gate_step_duration_seconds", "gauge", "The next timeline line's time less this one's."},
	{"laug_gate_harness", "gauge", "harness.json: its exit, its seconds, and the passed and failed counts of its tally."},
	{"laug_gate_poll", "gauge", "One poll of the wave: laugd's busy cells, and the compose projects up that the gate leased and that it did not. A null in the poll is no sample."},
	{"laug_gate_poll_pressure", "gauge", "One poll's host pressure, avg10 in percent, as the gate recorded it."},
	{"laug_threshold", "gauge", "Each reading's max in the gate's thresholds.json."},
	{"laug_threshold_not_judged", "gauge", "1 for a reading the gate's thresholds.json does not judge on this release."},
	{"laug_threshold_tolerance", "gauge", "A compare row's tolerance in thresholds.json; no sample while it is null."},
	{"laug_item_info", "gauge", "One item of the gate as plan.json and its lease.json name it; the value is 1."},
	{"laug_item_rate", "gauge", "The item's rate in plan.json, workflows a second."},
	{"laug_item_window_seconds", "gauge", "An item's window edges as its run or raun JSON holds them, Unix seconds."},
	{"laug_item_reading", "gauge", "Each reading of an item's verdict JSON (the whole window) or raun JSON (per window); a null is no sample."},
	{"laug_item_failed_iterations", "gauge", "Failed iterations as the run or raun JSON counts them."},
	{"laug_item_sweep", "gauge", "Workflows the item's sweep found, by status."},
	{"laug_item_softasserts", "gauge", "Soft assertions the raun counted, by window and role."},
	{"laug_fault_mark_seconds", "gauge", "A fault's inject, landed, remove and recovered times: item.log's lines while it runs, then the raun JSON's stamps."},
	{"laug_fault_signal", "gauge", "The raun's signal max and peak, and its catalog row's recovery budget in seconds."},
	{"laug_item_exit", "gauge", "The item's exit code as exit.json holds it."},
	{"laug_gate_verdict", "gauge", "The gate's verdict as verdict.json holds it, once gate combine wrote it; the value is 1."},
	{"laug_gate_rule", "gauge", "Each rule of verdict.json, 1 pass and 0 fail, in the file's order."},
	{"laug_gate_elapsed_seconds", "gauge", "verdict.json's elapsed_s on the wall clock."},
	{"laug_compare_row", "gauge", "verdict.json's comparison rows, one sample a number."},
	{"laug_aa", "gauge", "1 for a gate in a committed A-A list, the gates an unchanged release ran."},
	{"laug_lease_info", "gauge", "Each cell follow wrote a federation target for, with its labels; the value is 1."},
	{"laug_follow_polls_total", "counter", "Polls since follow started."},
	{"laug_follow_laugd_errors_total", "counter", "Reads of laugd's status that failed."},
	{"laug_follow_targets", "gauge", "Target files written by the last poll."},
	{"laug_follow_gate_dir_found", "gauge", "1 for each gate whose directory follow follows."},
	{"laug_follow_stale_targets", "gauge", "Target files past their lease's expiry at the last poll."},
	{"laug_follow_skipped_files_total", "counter", "Files and whole lines skipped because they did not parse, each counted once: a file by its mtime and size, a half-written one among them, and a line of the timeline or the polls by its place. A line still being written is not counted."},
	{"laug_follow_dropped_series", "gauge", "Series the last poll left out: a label would hold a slash or a control character, or a label name is there twice."},
}

var nan = math.NaN()

// lineAt is the wall clock of the first timeline line of that name and item, or NaN.
func (g *gateDir) lineAt(name, item string) float64 {
	for _, s := range g.timeline {
		if s.name == name && s.item == item {
			return s.wall
		}
	}
	return nan
}

// firstAt is the first of the named lines there is; failing all, the newest line's time: a file that is there before
// its line is shown at once live, and at the end of a gate that has no such line.
func (g *gateDir) firstAt(names ...string) float64 {
	for _, n := range names {
		if v := g.lineAt(n, ""); !math.IsNaN(v) {
			return v
		}
	}
	if len(g.timeline) > 0 {
		return g.timeline[len(g.timeline)-1].wall
	}
	return nan
}

// itemAt is the item's ended line, or the newest line's time.
func (g *gateDir) itemAt(key string) float64 {
	if v := g.lineAt("ended", key); !math.IsNaN(v) {
		return v
	}
	return g.firstAt()
}

// hostType is thresholds.json's host.type, else gate.json's, else vcpu-<n> from gate.json's host.vcpu.
func hostType(m obj) string {
	h := obj0(m["host"])
	if t := str(h, "type"); t != "" {
		return t
	}
	if v, ok := num(h, "vcpu"); ok {
		return "vcpu-" + strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// arms names the incumbent arm's two items: the plan's item with arm incumbent, and the item its pair names.
func (g *gateDir) arms() map[string]string {
	out := map[string]string{}
	for _, x := range arr(g.plan, "items") {
		it := obj0(x)
		if str(it, "arm") == "incumbent" {
			out[str(it, "key")] = "incumbent"
			if p := str(it, "pair"); p != "" {
				out[p] = "release"
			}
		}
	}
	return out
}

var harnessTally = regexp.MustCompile(`([0-9]+) passed, ([0-9]+) failed`)

// numbers gives an object's number fields in key order; strings, nulls and the rest are left out.
func numbers(m obj, skip ...string) [][2]any {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out [][2]any
next:
	for _, k := range keys {
		for _, s := range skip {
			if k == s {
				continue next
			}
		}
		if v, ok := m[k].(float64); ok {
			out = append(out, [2]any{k, v})
		}
	}
	return out
}

// fakeLabel gives a fake gate's series the label fake="true", once: laug_gate_info has its own fake label, true or
// false, and a label name twice is a sample the store refuses with the whole scrape.
func fakeLabel(ls [][2]string, fake bool) [][2]string {
	if !fake {
		return ls
	}
	for _, kv := range ls {
		if kv[0] == "fake" {
			return ls
		}
	}
	return append(ls, [2]string{"fake", "true"})
}

// series gives every record of the gate. aa is each gate id's A/A lists; budget is each fault's catalog budget_s.
func (g *gateDir) series(aa map[string][]string, budget map[string]float64) []series {
	var out []series
	fake := boolStr(g.gate, "fake") == "true"
	add := func(name string, at, v float64, labels ...[2]string) {
		ls := fakeLabel(append([][2]string{{"gate_id", g.id}}, nonEmpty(labels)...), fake)
		out = append(out, series{name: name, labels: ls, value: v, at: at})
	}
	L := func(k, v string) [2]string { return [2]string{k, v} }
	first := g.firstAt("start")
	if len(g.timeline) > 0 {
		first = g.timeline[0].wall
	}

	if g.gate != nil {
		incumbent := str(g.plan, "incumbent")
		if incumbent == "" {
			incumbent = str(g.gate, "incumbent")
		}
		add("laug_gate_info", first, 1, L("server_tag", str(g.gate, "server_tag")), L("incumbent", incumbent),
			L("plan", filepath.Base(str(g.gate, "plan"))), L("driver", str(g.gate, "driver")), L("fake", boolStr(g.gate, "fake")),
			L("host_type", hostType(g.gate)), L("laug_commit", str(g.gate, "laug_commit")), L("tree_dirty", boolStr(g.gate, "tree_dirty")))
	}
	t0, hasT0 := num(obj0(g.clock["t0"]), "wall")
	for _, m := range []string{"t0", "t_post"} {
		if v, ok := num(obj0(g.clock[m]), "wall"); ok {
			add("laug_gate_clock_seconds", v, v, L("mark", m))
		}
	}
	if d, ok := num(g.plan, "deadline_s"); ok && hasT0 {
		add("laug_gate_deadline_seconds", first, t0+d)
	}
	for i, s := range g.timeline {
		add("laug_gate_step_seconds", s.wall, s.wall, L("step", s.name), L("item", s.item))
		if i+1 < len(g.timeline) {
			next := g.timeline[i+1].wall
			add("laug_gate_step_duration_seconds", next, next-s.wall, L("step", s.name), L("item", s.item))
		}
	}
	if g.harness != nil {
		at := g.firstAt("wave", "combine")
		for _, f := range []string{"exit", "seconds"} {
			if v, ok := num(g.harness, f); ok {
				add("laug_gate_harness", at, v, L("field", f))
			}
		}
		if m := harnessTally.FindStringSubmatch(str(g.harness, "tally")); m != nil {
			p, _ := strconv.ParseFloat(m[1], 64)
			f, _ := strconv.ParseFloat(m[2], 64)
			add("laug_gate_harness", at, p, L("field", "passed"))
			add("laug_gate_harness", at, f, L("field", "failed"))
		}
	}
	leased := map[string]bool{}
	for _, it := range g.items {
		if p := str(it.lease, "project"); p != "" {
			leased[p] = true
		}
	}
	for _, p := range g.polls {
		w, ok := num(p, "wall")
		if !ok {
			continue
		}
		poll := func(name string, v float64, labels ...[2]string) {
			add(name, w, v, labels...)
			out[len(out)-1].ts = w
		}
		if v, ok := num(p, "busy"); ok {
			poll("laug_gate_poll", v, L("field", "busy"))
		}
		if ps, ok := p["projects"].([]any); ok {
			var mine, other float64
			for _, x := range ps {
				if s, _ := x.(string); leased[s] {
					mine++
				} else {
					other++
				}
			}
			poll("laug_gate_poll", mine, L("field", "projects_leased"))
			poll("laug_gate_poll", other, L("field", "projects_other"))
		}
		pr := obj0(p["pressure"])
		for _, r := range []string{"cpu", "memory", "io"} {
			for _, k := range []string{"some", "full"} {
				if v, ok := num(obj0(pr[r]), k); ok {
					poll("laug_gate_poll_pressure", v, L("resource", r), L("kind", k))
				}
			}
		}
	}
	if g.thresholds != nil {
		release := str(obj0(g.thresholds["resolved"]), "release")
		ht := hostType(g.thresholds)
		if ht == "" {
			ht = hostType(g.gate)
		}
		for _, x := range arr(g.thresholds, "thresholds") {
			t := obj0(x)
			n := str(t, "name")
			if v, ok := t["max"].(float64); ok && n != "" {
				add("laug_threshold", first, v, L("reading", n), L("release", release), L("host_type", ht))
			}
			if str(t, "no_data") == "not_judged" && n != "" {
				add("laug_threshold_not_judged", first, 1, L("reading", n), L("release", release))
			}
		}
		for _, x := range arr(obj0(g.thresholds["compare"]), "rows") {
			r := obj0(x)
			if v, ok := r["tolerance"].(float64); ok && str(r, "name") != "" {
				add("laug_threshold_tolerance", first, v, L("reading", str(r, "name")), L("scale", str(r, "scale")))
			}
		}
	}
	arms := g.arms()
	for _, k := range g.keys() {
		it := g.items[k]
		if it == nil {
			continue
		}
		pi := g.planItem(k)
		kind := str(pi, "kind")
		at := g.itemAt(k)
		// item_info carries the lease's cell, so it appears with the lease: at the leased line, or at once when
		// lease.json is there before its line; an item that never leased appears at the done line.
		infoAt := g.lineAt("leased", k)
		switch {
		case it.lease == nil:
			infoAt = g.lineAt("done", "")
		case math.IsNaN(infoAt):
			infoAt = g.firstAt()
		}
		block := ""
		if v, ok := num(it.lease, "block"); ok {
			block = strconv.FormatFloat(v, 'f', -1, 64)
		}
		add("laug_item_info", infoAt, 1, L("item", k), L("kind", kind), L("arm", arms[k]), L("scenario", str(pi, "scenario")),
			L("fault", str(pi, "fault")), L("cell", str(it.lease, "id")), L("block", block), L("server_tag", str(it.lease, "server_tag")),
			L("run_id", it.runID))
		if v, ok := pi["rate"].(float64); ok {
			add("laug_item_rate", first, v, L("item", k))
		}
		if v, ok := num(it.exit, "exit"); ok {
			add("laug_item_exit", at, v, L("item", k), L("stopped_by", str(it.exit, "stopped_by")))
		}
		if kind == "load" {
			for _, e := range []string{"start", "end"} {
				if ts, err := parseTime(str(it.run, e)); err == nil {
					add("laug_item_window_seconds", at, ts, L("item", k), L("window", "whole"), L("edge", e))
				}
			}
			for _, kv := range numbers(obj0(it.verdict["readings"])) {
				add("laug_item_reading", at, kv[1].(float64), L("item", k), L("window", "whole"), L("reading", kv[0].(string)))
			}
			if v, ok := it.run["iterations_failed"].(float64); ok {
				add("laug_item_failed_iterations", at, v, L("item", k), L("window", "whole"))
			}
			for _, kv := range numbers(obj0(it.verdict["sweep"])) {
				add("laug_item_sweep", at, kv[1].(float64), L("item", k), L("status", kv[0].(string)))
			}
			continue
		}
		if kind != "fault" {
			continue
		}
		r := it.run // the raun JSON
		for _, w := range []struct{ window, edge, key string }{{"during", "start", "landed_at"}, {"drain", "start", "remove_at"},
			{"drain", "end", "after_start"}, {"after", "start", "after_start"}, {"after", "end", "after_end"}} {
			if v, ok := num(r, w.key); ok {
				add("laug_item_window_seconds", at, v, L("item", k), L("window", w.window), L("edge", w.edge))
			}
		}
		for _, w := range []string{"before", "during", "drain", "after"} {
			for _, kv := range numbers(obj0(r[w])) {
				add("laug_item_reading", at, kv[1].(float64), L("item", k), L("window", w), L("reading", kv[0].(string)))
			}
		}
		for _, kv := range numbers(obj0(r["iterations_failed"])) {
			add("laug_item_failed_iterations", at, kv[1].(float64), L("item", k), L("window", kv[0].(string)))
		}
		for _, kv := range numbers(obj0(r["sweep"])) {
			add("laug_item_sweep", at, kv[1].(float64), L("item", k), L("status", kv[0].(string)))
		}
		sa := obj0(r["softasserts"])
		for _, w := range []string{"before", "during", "drain", "after"} {
			for _, kv := range numbers(obj0(sa[w])) {
				add("laug_item_softasserts", at, kv[1].(float64), L("item", k), L("window", w), L("role", kv[0].(string)))
			}
		}
		// The marks: the raun JSON's stamps once it has them, item.log's lines before.
		for _, m := range []struct{ mark, key string }{{"inject", "inject_at"}, {"landed", "landed_at"}, {"remove", "remove_at"}, {"recovered", "recovered_at"}} {
			if v, ok := num(r, m.key); ok {
				add("laug_fault_mark_seconds", at, v, L("item", k), L("mark", m.mark))
			} else if v, ok := it.logMarks[m.mark]; ok && r == nil {
				add("laug_fault_mark_seconds", v, v, L("item", k), L("mark", m.mark))
			}
		}
		if r != nil {
			for _, f := range []struct{ field, key string }{{"max", "signal_max"}, {"peak", "signal_peak"}} {
				if v, ok := r[f.key].(float64); ok {
					add("laug_fault_signal", at, v, L("item", k), L("field", f.field))
				}
			}
		}
		fault := str(r, "fault")
		if fault == "" {
			fault = str(pi, "fault")
		}
		if b, ok := budget[fault]; ok {
			add("laug_fault_signal", first, b, L("item", k), L("field", "budget"))
		}
	}
	if g.verdict != nil {
		at := g.firstAt("combine")
		add("laug_gate_verdict", at, 1, L("verdict", str(g.verdict, "verdict")), L("class", str(g.verdict, "class")),
			L("rerun", boolStr(g.verdict, "rerun")), L("host_quiet", boolStr(g.verdict, "host_quiet")))
		for _, r := range g.rules {
			v := 0.0
			if r.pass {
				v = 1
			}
			add("laug_gate_rule", at, v, L("rule", r.id), L("name", r.name))
		}
		if v, ok := num(obj0(g.verdict["elapsed_s"]), "wall"); ok {
			add("laug_gate_elapsed_seconds", at, v)
		}
		for _, x := range arr(obj0(g.verdict["comparison"]), "rows") {
			row := obj0(x)
			for _, f := range []string{"release", "incumbent", "d", "tolerance"} {
				if v, ok := row[f].(float64); ok {
					add("laug_compare_row", at, v, L("reading", str(row, "name")), L("field", f), L("result", str(row, "result")),
						L("judged", boolStr(row, "judged")))
				}
			}
		}
	}
	for _, list := range aa[g.id] {
		add("laug_aa", first, 1, L("list", list))
	}
	return out
}

// stepActive is laug_gate_step_active at time t: 1 for the newest line at or before t, 0 for the lines before it.
func (g *gateDir) stepActive(t float64) []series {
	n := -1
	for i, s := range g.timeline {
		if s.wall <= t {
			n = i
		}
	}
	var out []series
	for i := 0; i <= n; i++ {
		s := g.timeline[i]
		v := 0.0
		if i == n {
			v = 1
		}
		ls := fakeLabel(nonEmpty([][2]string{{"gate_id", g.id}, {"step", s.name}, {"item", s.item}}), boolStr(g.gate, "fake") == "true")
		out = append(out, series{name: "laug_gate_step_active", labels: ls, value: v, at: s.wall})
	}
	return out
}

func labelText(ls [][2]string) string {
	if len(ls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range ls {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[0] + `="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[1]) + `"`)
	}
	b.WriteByte('}')
	return b.String()
}

// clean says whether a series may be served: no label value holds a slash or a control character, and no label name
// is there twice.
func clean(ls [][2]string) bool {
	for i, kv := range ls {
		if strings.ContainsAny(kv[1], "/\n\r\t") {
			return false
		}
		for _, before := range ls[:i] {
			if before[0] == kv[0] {
				return false
			}
		}
	}
	return true
}

func fmtValue(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// point is one sample of a series in the OpenMetrics output: whole seconds.
type point struct {
	ts int64
	v  float64
}

// grid is the times -openmetrics samples a gate at: each timeline line, rounded up to the second so that the line is
// in it, and every 60 s from the first line to the last.
func (g *gateDir) grid() []int64 {
	if len(g.timeline) == 0 {
		return nil
	}
	seen := map[int64]bool{}
	var out []int64
	addT := func(t int64) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	first, last := int64(math.Ceil(g.timeline[0].wall)), int64(math.Ceil(g.timeline[len(g.timeline)-1].wall))
	for _, s := range g.timeline {
		addT(int64(math.Ceil(s.wall)))
	}
	for t := first + 60; t < last; t += 60 {
		addT(t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// openMetrics renders the gate's records with the gate's own times: OpenMetrics 1.0 text, every family a gauge, ended
// by # EOF, each series' samples in time order (the OpenMetrics spec; promtool tsdb create-blocks-from
// openmetrics, Prometheus 2.53.0). dropped counts the series clean kept out, each once.
func openMetrics(g *gateDir, aa map[string][]string, budget map[string]float64) (out []byte, dropped int) {
	type acc struct {
		labels string
		pts    []point
	}
	byFam := map[string][]*acc{}
	index := map[string]*acc{}
	left := map[string]bool{}
	put := func(s series, ts int64) {
		if !clean(s.labels) {
			if k := s.name + labelText(s.labels); !left[k] {
				left[k] = true
				dropped++
			}
			return
		}
		key := s.name + labelText(s.labels)
		a := index[key]
		if a == nil {
			a = &acc{labels: labelText(s.labels)}
			index[key] = a
			byFam[s.name] = append(byFam[s.name], a)
		}
		if n := len(a.pts); n > 0 && a.pts[n-1].ts >= ts {
			return // one sample a second: the first a series has in that second
		}
		a.pts = append(a.pts, point{ts, s.value})
	}
	all := g.series(aa, budget)
	for _, s := range all {
		if s.ts != 0 {
			put(s, int64(math.Ceil(s.ts)))
		}
	}
	for _, t := range g.grid() {
		for _, s := range all {
			if s.ts == 0 && !math.IsNaN(s.at) && s.at <= float64(t) {
				put(s, t)
			}
		}
		for _, s := range g.stepActive(float64(t)) {
			put(s, t)
		}
	}
	var b bytes.Buffer
	for _, f := range families {
		as := byFam[f.name]
		if len(as) == 0 {
			continue
		}
		b.WriteString("# TYPE " + f.name + " " + f.typ + "\n# HELP " + f.name + " " + f.help + "\n")
		for _, a := range as {
			for _, p := range a.pts {
				b.WriteString(f.name + a.labels + " " + fmtValue(p.v) + " " + strconv.FormatInt(p.ts, 10) + "\n")
			}
		}
	}
	b.WriteString("# EOF\n")
	return b.Bytes(), dropped
}
