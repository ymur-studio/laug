package follow

// A gate directory as follow reads it (docs/DESIGN.md section 9): gate.json, plan.json, thresholds.json, clock.json,
// harness.json, verdict.json, timeline.jsonl and poll.jsonl, and per item lease.json, exit.json, the run JSON (a load
// item's run, a fault item's raun), the load verdict JSON and, for a fault item, item.log's four mark lines. A file is
// read again only when its mtime or size changed. A JSON file is taken only when it parses, and a JSON Lines file only
// up to its last whole line: a half-written file keeps the last version that parsed, and is counted as skipped, once
// for each version (its mtime and size) that does not parse, as is each whole line that does not parse. item.log is
// read from where the last pass stopped, whole lines only. Nothing here is computed: values are copied as the files
// hold them.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type obj = map[string]any

type stamp struct {
	mod  time.Time
	size int64
}

// step is one whole line of timeline.jsonl: lease, leased and ended carry an item key after the step's name.
type step struct {
	name, item string
	wall       float64
}

// rule is one of verdict.json's rules, in the file's order.
type rule struct {
	id, name string
	pass     bool
}

type item struct {
	key, runID                string
	lease, exit, run, verdict obj
	logOff                    int64
	logMarks                  map[string]float64
}

type gateDir struct {
	id, dir                                         string
	stamps                                          map[string]stamp
	gate, plan, thresholds, clock, harness, verdict obj
	rules                                           []rule
	timeline                                        []step
	polls                                           []obj
	items                                           map[string]*item
	skipped                                         int              // new in the last refresh
	bad                                             map[string]stamp // the version of a file last counted as skipped
	badLines                                        map[string]int   // a JSON Lines file's whole lines that did not parse
}

func newGateDir(id, dir string) *gateDir {
	return &gateDir{id: id, dir: dir, stamps: map[string]stamp{}, items: map[string]*item{}, bad: map[string]stamp{},
		badLines: map[string]int{}}
}

// skip counts a version of rel that did not parse, once: the next pass finds it unchanged and counts it no more.
func (g *gateDir) skip(rel string, st stamp) {
	if g.bad[rel] != st {
		g.bad[rel] = st
		g.skipped++
	}
}

// skipLines counts a JSON Lines file's whole lines that did not parse, each once: n is how many it holds now.
func (g *gateDir) skipLines(rel string, n int) {
	if n > g.badLines[rel] {
		g.skipped += n - g.badLines[rel]
	}
	g.badLines[rel] = n
}

// wholeLines splits b at newlines and drops what follows the last one: a line still being written.
func wholeLines(b []byte) [][]byte {
	var out [][]byte
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return out
		}
		if line := bytes.TrimSpace(b[:i]); len(line) > 0 {
			out = append(out, line)
		}
		b = b[i+1:]
	}
}

// changed stats rel and says whether it must be read: it exists and its stamp moved since the last version that
// parsed. gone is true when it does not exist.
func (g *gateDir) changed(rel string) (fresh, gone bool, st stamp) {
	fi, err := os.Stat(filepath.Join(g.dir, rel))
	if err != nil || !fi.Mode().IsRegular() {
		delete(g.stamps, rel)
		delete(g.bad, rel)
		delete(g.badLines, rel)
		return false, true, stamp{}
	}
	st = stamp{fi.ModTime(), fi.Size()}
	return g.stamps[rel] != st, false, st
}

// readJSON keeps in *dst the last version of rel that parsed as one JSON object.
func (g *gateDir) readJSON(rel string, dst *obj) {
	fresh, gone, st := g.changed(rel)
	if gone {
		*dst = nil
		return
	}
	if !fresh {
		return
	}
	b, err := os.ReadFile(filepath.Join(g.dir, rel))
	var m obj
	if err != nil || json.Unmarshal(b, &m) != nil || m == nil {
		g.skip(rel, st)
		return
	}
	*dst, g.stamps[rel] = m, st
}

var itemStep = regexp.MustCompile(`^(lease|leased|ended) (.+)$`)

func (g *gateDir) readTimeline() {
	rel := "timeline.jsonl"
	fresh, gone, st := g.changed(rel)
	if gone {
		g.timeline = nil
		return
	}
	if !fresh {
		return
	}
	b, err := os.ReadFile(filepath.Join(g.dir, rel))
	if err != nil {
		g.skip(rel, st)
		return
	}
	var out []step
	bad := 0
	defer func() { g.skipLines(rel, bad) }()
	for _, line := range wholeLines(b) {
		var l struct {
			Step string   `json:"step"`
			Wall *float64 `json:"wall"`
		}
		if json.Unmarshal(line, &l) != nil || l.Step == "" || l.Wall == nil {
			bad++
			continue
		}
		s := step{name: l.Step, wall: *l.Wall}
		if m := itemStep.FindStringSubmatch(l.Step); m != nil {
			s.name, s.item = m[1], m[2]
		}
		out = append(out, s)
	}
	g.timeline, g.stamps[rel] = out, st
}

func (g *gateDir) readPolls() {
	rel := "poll.jsonl"
	fresh, gone, st := g.changed(rel)
	if gone {
		g.polls = nil
		return
	}
	if !fresh {
		return
	}
	b, err := os.ReadFile(filepath.Join(g.dir, rel))
	if err != nil {
		g.skip(rel, st)
		return
	}
	var out []obj
	bad := 0
	defer func() { g.skipLines(rel, bad) }()
	for _, line := range wholeLines(b) {
		var m obj
		if json.Unmarshal(line, &m) != nil || m == nil {
			bad++
			continue
		}
		out = append(out, m)
	}
	g.polls, g.stamps[rel] = out, st
}

// orderedRules reads verdict.json's rules object in the file's order: Go's maps keep none.
func orderedRules(b []byte) []rule {
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil || top["rules"] == nil {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(top["rules"]))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out []rule
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return out
		}
		id, _ := t.(string)
		var r struct {
			Name string `json:"name"`
			Pass *bool  `json:"pass"`
		}
		if d.Decode(&r) != nil {
			return out
		}
		if id != "" && r.Pass != nil {
			out = append(out, rule{id, r.Name, *r.Pass})
		}
	}
	return out
}

func (g *gateDir) readVerdict() {
	rel := "verdict.json"
	fresh, gone, st := g.changed(rel)
	if gone {
		g.verdict, g.rules = nil, nil
		return
	}
	if !fresh {
		return
	}
	b, err := os.ReadFile(filepath.Join(g.dir, rel))
	var m obj
	if err != nil || json.Unmarshal(b, &m) != nil || m == nil {
		g.skip(rel, st)
		return
	}
	g.verdict, g.rules, g.stamps[rel] = m, orderedRules(b), st
}

// keys is the plan's item keys in its order; before plan.json, gate.json's wiring's.
func (g *gateDir) keys() []string {
	var out []string
	if its, ok := g.plan["items"].([]any); ok {
		for _, x := range its {
			if k := str(obj0(x), "key"); k != "" {
				out = append(out, k)
			}
		}
		return out
	}
	for _, w := range arr(g.gate, "wiring") {
		if k := str(obj0(w), "key"); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// planItem is the plan's entry for key.
func (g *gateDir) planItem(key string) obj {
	for _, x := range arr(g.plan, "items") {
		if str(obj0(x), "key") == key {
			return obj0(x)
		}
	}
	return nil
}

// wiring is gate.json's wiring entry for key.
func (g *gateDir) wiring(key string) obj {
	for _, x := range arr(g.gate, "wiring") {
		if str(obj0(x), "key") == key {
			return obj0(x)
		}
	}
	return nil
}

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// refresh reads what changed since the last pass and returns the files and lines it skipped.
func (g *gateDir) refresh() int {
	g.skipped = 0
	g.readJSON("gate.json", &g.gate)
	g.readJSON("plan.json", &g.plan)
	g.readJSON("thresholds.json", &g.thresholds)
	g.readJSON("clock.json", &g.clock)
	g.readJSON("harness.json", &g.harness)
	g.readVerdict()
	g.readTimeline()
	g.readPolls()
	for _, k := range g.keys() {
		if !keyRe.MatchString(k) {
			continue // a key is a directory name; one that is not a plain name is not read
		}
		it := g.items[k]
		if it == nil {
			it = &item{key: k, logMarks: map[string]float64{}}
			g.items[k] = it
		}
		it.runID = str(g.wiring(k), "run_id")
		if it.runID == "" {
			it.runID = g.id + "-" + k
		}
		g.readJSON(k+"/lease.json", &it.lease)
		g.readJSON(k+"/exit.json", &it.exit)
		g.readJSON(k+"/"+it.runID+".json", &it.run)
		g.readJSON(k+"/"+it.runID+".verdict.json", &it.verdict)
		if str(g.planItem(k), "kind") == "fault" {
			g.readLog(it)
		}
	}
	return g.skipped
}

// The four mark lines of a raun's item.log: faults/lib.sh's log(), "[RUN_ID] HH:MM:SSZ <message>", with the messages
// faults/run.sh writes after inject, at landing, after the removal and at recovery (TestItemLogLineForms pins both).
var (
	logLine  = regexp.MustCompile(`^\[([^\]]+)\] ([0-2][0-9]):([0-5][0-9]):([0-5][0-9])Z (.*)$`)
	logMarks = []struct {
		mark string
		re   *regexp.Regexp
	}{
		{"inject", regexp.MustCompile(`^injected$`)},
		{"landed", regexp.MustCompile(`^landed after [0-9]+ s: `)},
		{"remove", regexp.MustCompile(`^removed( |:|$)`)},
		{"recovered", regexp.MustCompile(`^recovered [0-9]+ s after removal`)},
	}
)

// t0 is the gate's start on the wall clock: clock.json's t0, else the first timeline line.
func (g *gateDir) t0() (float64, bool) {
	if v, ok := num(obj0(g.clock["t0"]), "wall"); ok {
		return v, true
	}
	if len(g.timeline) > 0 {
		return g.timeline[0].wall, true
	}
	return 0, false
}

// markTime puts a log's HH:MM:SS on t0's UTC day, or the next one when it reads more than 12 h before t0: a gate
// lasts minutes, so such a time crossed 00:00 UTC.
func markTime(t0 float64, h, m, s int) float64 {
	day := time.Unix(int64(t0), 0).UTC().Truncate(24 * time.Hour)
	v := float64(day.Unix() + int64(h*3600+m*60+s))
	if v < t0-12*3600 {
		v += 24 * 3600
	}
	return v
}

// readLog reads item.log's new whole lines for the item's four marks, 1 s resolution. A line of another run id, or
// any other line, is passed over; no text of the log is kept.
func (g *gateDir) readLog(it *item) {
	path := filepath.Join(g.dir, it.key, "item.log")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if fi.Size() < it.logOff {
		it.logOff, it.logMarks = 0, map[string]float64{} // a new file
	}
	t0, ok := g.t0()
	if !ok || fi.Size() == it.logOff {
		return
	}
	if _, err := f.Seek(it.logOff, io.SeekStart); err != nil {
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return
	}
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		return
	}
	for _, line := range strings.Split(string(b[:end]), "\n") {
		m := logLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil || m[1] != it.runID {
			continue
		}
		for _, lm := range logMarks {
			if _, seen := it.logMarks[lm.mark]; !seen && lm.re.MatchString(m[5]) {
				h, _ := strconv.Atoi(m[2])
				mi, _ := strconv.Atoi(m[3])
				s, _ := strconv.Atoi(m[4])
				it.logMarks[lm.mark] = markTime(t0, h, mi, s)
			}
		}
	}
	it.logOff += int64(end + 1)
}

// The readers of a decoded JSON value: a key that is missing, null or of another type is absent.
func obj0(v any) obj { m, _ := v.(map[string]any); return m }

func arr(m obj, k string) []any { a, _ := m[k].([]any); return a }

func str(m obj, k string) string { s, _ := m[k].(string); return s }

func num(m obj, k string) (float64, bool) {
	switch v := m[k].(type) {
	case float64:
		return v, true
	case string: // the raun JSON writes its stamps as strings of Unix seconds
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil && v != ""
	}
	return 0, false
}

func boolStr(m obj, k string) string {
	if b, ok := m[k].(bool); ok {
		return strconv.FormatBool(b)
	}
	return ""
}
