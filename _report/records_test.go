package report

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/rejudge"
)

// root is the checkout, from this package's directory; results its gate/results.
var (
	root    = ".."
	results = filepath.Join(root, "gate", "results")
)

// lastLab is the newest gate run at this commit, all of whose lab runs (32 vCPU) the pinned counts cover: runs that
// boot 4 adds come after it, and the dev host's runs at 24 vCPU, which leave the tree, are not among them.
const lastLab = "g20261019-005744"

// committed loads the records once for the tests that read every committed run: every run judged again, in process,
// so the judge is this tree's own and its hold is met (NotHeld empty).
var committed = sync.OnceValues(func() (*Records, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return Load(abs, results, rejudge.Checkout{Root: abs})
})

func records(t *testing.T) *Records {
	t.Helper()
	rs, err := committed()
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// lab is a run of the lab host at this commit: 32 vCPU, at or before lastLab.
func lab(r Run) bool { return r.Host.VCPU.String() == "32" && r.ID <= lastLab }

// TestRecordsReadEveryRun reads every committed run and holds each field the records keep to the same file read
// another way, with encoding/json, and each file the run holds to os.Stat: no run has a problem, every BLOCK has a
// deciding rule and no ADVANCE has one, the rejudge's counts line equals internal/rejudge's for the same directory,
// over the lab runs here the counts are their gates rows', and over every lab run's gates row the counts are the ones
// jq gave at this commit.
func TestRecordsReadEveryRun(t *testing.T) {
	rs := records(t)
	answers, err := rejudge.RejudgeAll(results, rejudge.Checkout{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != len(rs.Runs) {
		t.Fatalf("%d runs, internal/rejudge %d", len(rs.Runs), len(answers))
	}
	if got, want := rs.Counts.String(), rejudge.Count(answers).String(); got != want || rs.NotRun != "" {
		t.Errorf("counts %q (not run %q), internal/rejudge's %q", got, rs.NotRun, want)
	}
	tally := map[string]int{}
	epochs := map[int64]bool{}
	for i, r := range rs.Runs {
		t.Run(r.ID, func(t *testing.T) { checkRun(t, r, answers[i]) })
		if lab(r) {
			addTally(tally, r.Verdict, r.Class, r.Comparison, r.Judge.Name, r.Rules, r.Host.Type, r.Driver, r.Poll,
				r.Harness, r.ItemDirs)
			epochs[r.Epoch] = true
		}
	}
	// The same counts from the gates rows: of every lab run, pinned below, and of the lab runs here, which must be the
	// records' own.
	disk, err := RowsOf(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	all, here := map[string]int{}, map[string]int{}
	allBoots, hereBoots := map[string]bool{}, map[string]bool{}
	for id, g := range disk.Gates {
		if gateHost(g).VCPU.String() != "32" || id > lastLab {
			continue
		}
		tallies := []map[string]int{all}
		boots := []map[string]bool{allBoots}
		if _, ok := rs.Run(id); ok {
			tallies, boots = append(tallies, here), append(boots, hereBoots)
		}
		for i, m := range tallies {
			addRowTally(m, g)
			boots[i][rowBoot(g)] = true
		}
	}
	if got, want := tallyLines(tally), tallyLines(here); !slices.Equal(got, want) {
		t.Errorf("the lab runs here:\n%s\ntheir gates rows\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(epochs) != len(hereBoots) {
		t.Errorf("the lab runs here: %d boot epochs, their gates rows %d boots", len(epochs), len(hereBoots))
	}
	if len(allBoots) != 11 {
		t.Errorf("the lab runs' gates rows: %d boots, want 11", len(allBoots))
	}
	// jq over gate/results at this commit, the 137 runs at 32 vCPU, as their gates rows hold them.
	want := []string{
		"class control: 11", "class harness: 4", "class invalid: 4", "class none: 98", "class refused: 2",
		"class regression: 18",
		"driver not recorded: 28", "driver run.sh: 61", "driver step.sh: 48",
		"harness.json false: 2", "harness.json true: 135",
		"item directory false: 2", "item directory true: 135",
		"judge v1: 52", "judge v2-r7off: 1", "judge v2: 84",
		"mode judging: 53", "mode none: 52", "mode recording: 32",
		"poll.jsonl false: 6", "poll.jsonl true: 131",
		"rules R6: 50", "rules R7: 85", "rules null: 2",
		"runs at 32 vCPU: 137",
		"type m8i.8xlarge: 83", "type not recorded: 52", "type other: 2",
		"verdict ADVANCE: 98", "verdict BLOCK: 39",
	}
	if got := tallyLines(all); !slices.Equal(got, want) {
		t.Errorf("the lab runs' gates rows:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// addTally counts one lab run's fields as TestRecordsReadEveryRun's lines name them.
func addTally(tally map[string]int, verdict, class string, c *Comparison, judgeName string, rules []Rule, hostType,
	driver string, poll, harness, itemDirs bool) {
	add := func(k string, v any) { tally[fmt.Sprintf("%s %v", k, v)]++ }
	add("verdict", verdict)
	add("class", cmp.Or(class, "none"))
	mode := "none"
	if c != nil {
		mode = c.Mode
	}
	add("mode", mode)
	add("judge", judgeName)
	if rules == nil {
		add("rules", "null")
	} else {
		add("rules", rules[len(rules)-1].ID)
	}
	add("type", cmp.Or(hostType, "not recorded"))
	add("driver", cmp.Or(driver, "not recorded"))
	add("poll.jsonl", poll)
	add("harness.json", harness)
	add("item directory", itemDirs)
	add("runs", "at 32 vCPU")
}

// addRowTally counts a gates row as addTally counts its run.
func addRowTally(tally map[string]int, g jsonx.Value) {
	var c *Comparison
	if m := str(at(g, "comparison", "mode")); m != "" {
		c = &Comparison{Mode: m}
	}
	var rules []Rule
	if o, ok := at(g, "rules").Obj(); ok {
		for _, k := range o.Keys() {
			rules = append(rules, Rule{ID: k})
		}
	}
	b := func(k string) bool { v, _ := at(g, "files", k).Bool(); return v }
	addTally(tally, str(at(g, "verdict")), str(at(g, "class")), c, gateJudge(g).Name, rules, str(at(g, "host", "type")),
		str(at(g, "driver")), b("poll"), b("harness"), b("item_dirs"))
}

// tallyLines are a tally's lines, sorted.
func tallyLines(tally map[string]int) []string {
	var out []string
	for k, n := range tally {
		out = append(out, fmt.Sprintf("%s: %d", k, n))
	}
	slices.Sort(out)
	return out
}

// checkRun holds one run's record to its files read with encoding/json and os.Stat, and to its rejudge answer a.
func checkRun(t *testing.T, r Run, a rejudge.Answer) {
	t.Helper()
	if len(r.Problems) > 0 {
		t.Errorf("problems: %q", r.Problems)
	}
	dir := filepath.Join(results, r.ID)
	g, v := decode(t, filepath.Join(dir, "gate.json")), decode(t, filepath.Join(dir, "verdict.json"))
	text := func(m map[string]any, keys ...string) string {
		var x any = m
		for _, k := range keys {
			o, _ := x.(map[string]any)
			x = o[k]
		}
		switch x := x.(type) {
		case string:
			return x
		case json.Number:
			n, err := jsonx.Literal(x.String()) // jq's reading of the literal, verdict.md's text
			if err != nil {
				t.Errorf("%s: %v", x, err)
			}
			return n.String()
		case bool:
			return fmt.Sprint(x)
		}
		return ""
	}
	for _, c := range []struct{ name, got, want string }{
		{"server_tag", r.ServerTag, text(g, "server_tag")},
		{"plan", r.Plan, text(g, "plan")},
		{"driver", r.Driver, text(g, "driver")},
		{"workflow_id", r.WorkflowID, text(g, "workflow_id")},
		{"tree_dirty", r.TreeDirty, text(g, "tree_dirty")},
		{"host.type", r.Host.Type, text(g, "host", "type")},
		{"host.vcpu", r.Host.VCPU.String(), text(g, "host", "vcpu")},
		{"host.mem_gib", r.Host.MemGiB.String(), text(g, "host", "mem_gib")},
		{"host.mem_available_gib", r.Host.MemAvailGiB.String(), text(g, "host", "mem_available_gib")},
		{"verdict", r.Verdict, text(v, "verdict")},
		{"class", r.Class, text(v, "class")},
		{"incumbent", r.Incumbent, text(v, "incumbent")},
		{"refused", r.Refused, text(v, "refused")},
		{"stopped_at", r.StoppedAt, text(v, "stopped_at")},
		{"host_quiet", fmt.Sprint(r.HostQuiet), cmp.Or(text(v, "host_quiet"), "false")},
		{"t0.wall", r.T0.Wall.String(), text(v, "t0", "wall")},
		{"t0.mono", r.T0.Mono.String(), text(v, "t0", "mono")},
		{"t_post.wall", r.TPost.Wall.String(), text(v, "t_post", "wall")},
		{"elapsed_s.wall", r.Elapsed.String(), text(v, "elapsed_s", "wall")},
		{"reasons", fmt.Sprint(r.Reasons), fmt.Sprint(v["reasons"])},
		{"rejudge", string(r.Rejudge) + " " + r.RejudgeWhy, string(a.Outcome) + " " + a.Why},
		{"poll.jsonl", fmt.Sprint(r.Poll), fmt.Sprint(exists(filepath.Join(dir, "poll.jsonl")))},
		{"harness.json", fmt.Sprint(r.Harness), fmt.Sprint(exists(filepath.Join(dir, "harness.json")))},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, the file %q", c.name, c.got, c.want)
		}
	}
	if fmt.Sprint(r.Fake) != cmp.Or(text(g, "fake"), "false") {
		t.Errorf("fake: %v, the file %q", r.Fake, text(g, "fake"))
	}
	rules, _ := v["rules"].(map[string]any)
	if (rules == nil) != (r.Rules == nil) || len(rules) != len(r.Rules) {
		t.Errorf("rules: %d, the file %d", len(r.Rules), len(rules))
	}
	for i, rule := range r.Rules {
		m, _ := rules[rule.ID].(map[string]any)
		if rule.ID != fmt.Sprintf("R%d", i+1) || m == nil || m["pass"] != rule.Pass || m["name"] != rule.Name ||
			fmt.Sprint(m["reasons"]) != fmt.Sprint(rule.Reasons) {
			t.Errorf("rule %d: %+v, the file %v", i, rule, m)
		}
	}
	comparison, _ := v["comparison"].(map[string]any)
	switch {
	case (comparison == nil) != (r.Comparison == nil):
		t.Errorf("comparison: %v, the file %v", r.Comparison != nil, comparison != nil)
	case comparison != nil:
		rows, _ := comparison["rows"].([]any)
		if r.Comparison.Mode != text(comparison, "mode") || len(rows) != len(r.Comparison.Rows) {
			t.Errorf("comparison: %s with %d rows, the file %v with %d", r.Comparison.Mode, len(r.Comparison.Rows),
				comparison["mode"], len(rows))
			break
		}
		for i, row := range r.Comparison.Rows {
			m, _ := rows[i].(map[string]any)
			got := strings.Join([]string{row.Name, row.Scale, row.Release.String(), row.Incumbent.String(),
				row.D.String(), row.Tolerance.String(), fmt.Sprint(row.Judged), row.Result}, "|")
			want := strings.Join([]string{text(m, "name"), text(m, "scale"), text(m, "release"), text(m, "incumbent"),
				text(m, "d"), text(m, "tolerance"), fmt.Sprint(m["judged"]), text(m, "result")}, "|")
			if got != want {
				t.Errorf("comparison row %d: %s, the file %s", i, got, want)
			}
		}
	}
	entries, _ := os.ReadDir(dir)
	item := slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.IsDir() && e.Name()[0] != '.' })
	if r.ItemDirs != item {
		t.Errorf("item directory: %v, the run %v", r.ItemDirs, item)
	}
	if d := r.Deciding(); (d != "") != (r.Verdict == "BLOCK") {
		t.Errorf("verdict %s, deciding %q", r.Verdict, d)
	}
	if r.Judge.Name == "" || r.JudgeWhy != "" {
		t.Errorf("judge %q: %s", r.Judge.Name, r.JudgeWhy)
	}
}

func decode(t *testing.T, file string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func exists(file string) bool {
	fi, err := os.Stat(file)
	return err == nil && fi.Mode().IsRegular()
}

// TestReadRunShapes reads one lab run of each shape the records (Run) tell apart, each from gate/results or its copy
// in internal/rejudge's testdata/lab (runDir): a trap verdict, a refused gate, a judge before the arm with no driver
// recorded, R7 deciding, and a control branch's own judge.
func TestReadRunShapes(t *testing.T) {
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	linkRuns(t, dir, "g20261015-223429", "g20261017-014142", "g20261017-171604", "g20261018-080445",
		"g20261018-092540")
	rs, err := Load(abs, dir, rejudge.Checkout{Root: abs})
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) Run {
		r, ok := rs.Run(id)
		if !ok {
			t.Fatalf("no run %s", id)
		}
		return *r
	}
	// A trap verdict: the workflow cancelled 60 s into the wave (cells/results/2026-10-17-gate6-durability.txt:28).
	r := get("g20261017-014142")
	if r.Rules != nil || r.StoppedAt != "wave" || r.Deciding() != "stopped at wave" || r.Comparison != nil ||
		r.Judge.Name != "v1" || r.Rejudge != rejudge.Rerendered {
		t.Errorf("the trap: %+v", r)
	}
	// A refused gate: another host type declared (cells/results/2026-10-17-lab-arm-calibration.txt:538).
	r = get("g20261017-171604")
	want := "refused: this host's declared type is not m8i.8xlarge, the type load/steady-state.yaml was measured on: " +
		"its numbers judge no other host type (docs/DESIGN.md section 7.1)"
	if r.Deciding() != want || r.Host.Type != "other" || r.Poll || r.Harness || r.ItemDirs || len(r.Rules) != 7 {
		t.Errorf("the refusal: %q, %+v", r.Deciding(), r)
	}
	// A judge before the arm, R1 to R6 only, and a gate.json with no driver
	// (cells/results/2026-10-15-lab-gate-5.txt:30).
	r = get("g20261015-223429")
	if len(r.Rules) != 6 || r.Deciding() != "R4" || r.Driver != "" || r.Host.Type != "" ||
		r.Host.VCPU.String() != "32" || r.Judge.Name != "v1" || r.Comparison != nil || r.Class != "regression" {
		t.Errorf("before the arm: %q, %+v", r.Deciding(), r)
	}
	// The A/B gate that BLOCKed on R7 alone (cells/results/2026-10-18-lab-arm-validation.txt:420).
	r = get("g20261018-092540")
	if r.Deciding() != "R7" || r.ServerTag != "1.32.1" || r.Incumbent != "1.31.0" || r.Driver != "run.sh" ||
		r.Judge.Name != "v2" || r.Elapsed.String() != "587.455" || r.T0.Wall.String() != "1792315540.579" ||
		r.Comparison == nil || r.Comparison.Mode != "judging" || len(r.Comparison.Rows) != 12 {
		t.Fatalf("the A/B: %+v", r)
	}
	for _, c := range []struct {
		row                int
		name, d, tol, what string
	}{
		{1, "persistence_latency_p99_ms", "0.401", "0.35", "WORSE"},
		{2, "activity_schedule_to_start_over_10ms", "", "0.0072", "not produced"},
		{11, "workflows_completed", "-3", "", "recorded"},
	} {
		row := r.Comparison.Rows[c.row]
		if row.Name != c.name || row.D.String() != c.d || row.Tolerance.String() != c.tol || row.Result != c.what {
			t.Errorf("the A/B's row %d: %+v", c.row, row)
		}
	}
	// arm-always-pass: its branch's judge, which the registry names (validation.txt:412).
	if r = get("g20261018-080445"); r.Judge.Name != "v2-r7off" || r.Deciding() != "R1" || r.Poll {
		t.Errorf("arm-always-pass: %q, %+v", r.Deciding(), r)
	}
}

// TestReadRunProblems: a run whose files cannot be read is read as far as it can be, each file's problem named.
func TestReadRunProblems(t *testing.T) {
	fsys := fstest.MapFS{
		"g1/gate.json":    {Data: []byte(`{"server_tag":"1.31.0","host":{"vcpu":32}}`)},
		"g1/verdict.json": {Data: []byte(`[1]`)},
		"g2/verdict.json": {Data: []byte(`{"verdict":"ADVANCE"} {}`)},
		"g3/gate.json":    {Data: []byte(`{"t0":`)},
	}
	for _, c := range []struct{ id, problems string }{
		{"g1", `["verdict.json: not a JSON object"]`},
		{"g2", `["gate.json: file does not exist" "verdict.json: byte 22: more than one JSON value"]`},
		{"g3", `["gate.json: byte 6: unfinished JSON term" "verdict.json: file does not exist"]`},
	} {
		r := ReadRun(fsys, c.id)
		if got := fmt.Sprintf("%q", r.Problems); got != c.problems {
			t.Errorf("%s: %s, want %s", c.id, got, c.problems)
		}
	}
	if r := ReadRun(fsys, "g1"); r.ServerTag != "1.31.0" || r.Host.VCPU.String() != "32" || r.Verdict != "" ||
		r.Epoch != 0 || r.Deciding() != "" {
		t.Errorf("g1 read as far as it can be: %+v", r)
	}
}

// TestRejudgeHold: when the checkout's hold fails, Load judges no run again and says why; the records are written
// all the same. With it held, every run is judged. Two lab runs, through links in a temporary results directory.
func TestRejudgeHold(t *testing.T) {
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	linkRuns(t, dir, "g20261017-014142", "g20261018-092540")
	why := "laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD"
	rs, err := Load(abs, dir, rejudge.Checkout{Root: abs, NotHeld: why})
	if err != nil {
		t.Fatal(err)
	}
	if rs.NotRun != why || rs.Counts != nil || len(rs.Runs) != 2 || rs.Runs[0].Rejudge != "" ||
		rs.Runs[1].Rejudge != "" || rs.Runs[1].Verdict != "BLOCK" {
		t.Errorf("not held: %q %v %+v", rs.NotRun, rs.Counts, rs.Runs)
	}
	rs, err = Load(abs, dir, rejudge.Checkout{Root: abs})
	if err != nil {
		t.Fatal(err)
	}
	const counts = "2 runs: 1 identical, 0 known, 1 re-rendered, 0 unexplained, 0 not judged"
	if got := rs.Counts.String(); rs.NotRun != "" || got != counts {
		t.Errorf("held: %q %q", rs.NotRun, got)
	}
	if _, err := Load(abs, filepath.Join(dir, "none"), rejudge.Checkout{Root: abs}); !errors.Is(err,
		rejudge.ErrNoResults) {
		t.Errorf("no results directory: %v", err)
	}
	if _, err := Load(abs, t.TempDir(), rejudge.Checkout{Root: abs}); !errors.Is(err, rejudge.ErrNoRun) {
		t.Errorf("no run: %v", err)
	}
}

// setCite are the lines c names: of its transcript where it is here, read once into lines, else of an archived one
// its quotes rows.
func setCite(lines map[string][]string, arch *archive, c Cite) ([]string, error) {
	if _, err := os.Stat(filepath.Join(root, c.File)); err == nil || !arch.has(c.File) {
		if lines[c.File] == nil {
			b, err := os.ReadFile(filepath.Join(root, c.File))
			if err != nil {
				return nil, err
			}
			lines[c.File] = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		}
		if c.From < 1 || c.To < c.From || c.To > len(lines[c.File]) {
			return nil, fmt.Errorf("%s:%d-%d: past its %d lines", c.File, c.From, c.To, len(lines[c.File]))
		}
		return lines[c.File][c.From-1 : c.To], nil
	}
	var out []string
	for n := c.From; n <= c.To; n++ {
		key := c.File + ":" + strconv.Itoa(n)
		text, ok := arch.quotes[key]
		if !ok {
			return nil, fmt.Errorf("%s is archived, and docs/tables/quotes.jsonl has no row %s", c.File, key)
		}
		out = append(out, text)
	}
	return out, nil
}

// TestSets holds the committed sets to the committed rows: every id in gate/sets/ and _obs/aa/ has a row in
// docs/tables/gates.jsonl, each list's count is its header's (ParseSet), each id in gate/sets is on a line its
// citation names (the transcript's, or once it is archived, its quotes rows), and each set's gates are what its
// transcript says they are, as their gates and compare rows hold them.
func TestSets(t *testing.T) {
	rs := records(t)
	rows, err := RowsOf(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]Set{}
	for _, s := range rs.Sets {
		names = append(names, s.Path)
		byName[s.Kind+" "+s.Label] = s
	}
	want := []string{
		"gate/sets/calibration-arm-3item-m8i.8xlarge.txt", "gate/sets/control-arm-always-pass.txt",
		"gate/sets/control-arm-known-slow.txt", "gate/sets/control-gate5-always-advance.txt",
		"gate/sets/control-gate5-known-bad.txt", "gate/sets/control-persistence-maxqps-50.txt",
		"gate/sets/validation-arm-3item-m8i.8xlarge.txt", "gate/sets/warm-up-m8i.8xlarge.txt",
		"_obs/aa/arm-3item-m8i.8xlarge.txt", "_obs/aa/plan-2item-m8i.8xlarge.txt",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("sets %q, want %q", names, want)
	}
	lines := map[string][]string{}
	arch, err := readArchive(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{} // a gate/sets id's set, which holds it alone
	for _, s := range rs.Sets {
		for i, id := range s.IDs {
			g, ok := rows.Gates[id]
			if !ok {
				t.Errorf("%s: %s has no gates row", s.Path, id)
				continue
			}
			if gateHost(g).VCPU.String() != "32" {
				t.Errorf("%s: %s is not a lab host's run: a set names none of the dev host's, which leave the tree",
					s.Path, id)
			}
			if s.Kind == KindAA {
				continue
			}
			if other, ok := in[id]; ok {
				t.Errorf("%s: %s is in %s too", s.Path, id, other)
			}
			in[id] = s.Path
			c := s.Cites[i]
			cited, err := setCite(lines, arch, c)
			if err != nil {
				t.Errorf("%s: %v", s.Path, err)
				continue
			}
			if !slices.ContainsFunc(cited, func(l string) bool { return strings.Contains(l, id) }) {
				t.Errorf("%s: %s is not on %s:%d-%d", s.Path, id, c.File, c.From, c.To)
			}
		}
	}

	type expect struct {
		set, verdict, class, mode, judge string
	}
	for _, e := range []expect{
		{"calibration arm-3item-m8i.8xlarge", "ADVANCE", "", "recording", "v2"},
		{"validation arm-3item-m8i.8xlarge", "ADVANCE", "", "judging", "v2"},
		{"control arm-known-slow", "BLOCK", "regression", "", ""},
		{"control gate5-known-bad", "BLOCK", "regression", "", ""},
		{"control gate5-always-advance", "BLOCK", "harness", "", ""},
		{"control persistence-maxqps-50", "BLOCK", "refused", "", ""},
		{"control arm-always-pass", "BLOCK", "harness", "judging", "v2-r7off"},
	} {
		s := byName[e.set]
		drivers := map[string]int{}
		for _, id := range s.IDs {
			g, ok := rows.Gates[id]
			if !ok {
				continue // TestSets has said so
			}
			mode := ""
			if e.mode != "" {
				mode = str(at(g, "comparison", "mode"))
			}
			judge := ""
			if e.judge != "" {
				judge = gateJudge(g).Name
			}
			verdict, class, tag, plan := str(at(g, "verdict")), str(at(g, "class")), str(at(g, "server_tag")),
				str(at(g, "plan"))
			if verdict != e.verdict || class != e.class || mode != e.mode || judge != e.judge || tag != "1.31.0" ||
				plan != "gate/plan.yaml" {
				t.Errorf("%s: %s is %s %s %s %s on %s, %s", s.Path, id, verdict, class, mode, judge, tag, plan)
			}
			if s.Kind == KindCalibration || s.Kind == KindValidation {
				drivers[str(at(g, "driver"))]++
				incumbent, quiet, host := str(at(g, "incumbent")), at(g, "host_quiet").Kind() == jsonx.KindTrue,
					gateHost(g).Type
				if incumbent != "1.31.0" || !quiet || host != "m8i.8xlarge" || !strings.HasSuffix(s.Label, "-"+host) {
					t.Errorf("%s: %s: incumbent %s, host_quiet %v, host %s", s.Path, id, incumbent, quiet, host)
				}
			}
		}
		if s.Kind != KindControl && (len(drivers) != 2 || drivers["run.sh"] != drivers["step.sh"]) {
			t.Errorf("%s: drivers %v, the header's half each", s.Path, drivers)
		}
	}

	// R7 in the 30: it passes in each, and every judged d sits at most 0.67 of its tolerance
	// (cells/results/2026-10-18-lab-arm-validation.txt:907-910).
	most := 0.0
	for _, id := range byName["validation arm-3item-m8i.8xlarge"].IDs {
		g, ok := rows.Gates[id]
		if !ok {
			continue
		}
		if rules, _ := at(g, "rules").Obj(); rules == nil || rules.Len() != 7 || r7Failed(g) {
			t.Errorf("validation %s: rules %s", id, jsonx.AppendCompact(nil, at(g, "rules")))
		}
		for _, row := range rows.Compare[id] {
			d, tol := num(at(row, "d")), num(at(row, "tolerance"))
			if judged, _ := at(row, "judged").Bool(); judged && d.OK {
				most = math.Max(most, math.Abs(d.F)/tol.F)
			}
		}
	}
	if Num(most) != "0.674" {
		t.Errorf("the 30's largest |d| over T: %s", Num(most))
	}

	// The stack's three-item A/A list is the calibration session's 20 and the 10 of the validation's Part 1, in
	// order (its header).
	aa := byName["aa arm-3item-m8i.8xlarge"].IDs
	if got := append(slices.Clone(byName["calibration arm-3item-m8i.8xlarge"].IDs),
		byName["validation arm-3item-m8i.8xlarge"].IDs[:10]...); !slices.Equal(aa, got) {
		t.Errorf("_obs/aa/arm-3item-m8i.8xlarge.txt: %q, want the 20 calibration gates and the first 10 validation gates: %q",
			aa, got)
	}
	// The warm-up gates: the stack's w0, which ADVANCEd, boot 4's W1 and W2, which BLOCKed class invalid, and boot 6's
	// W1, which ADVANCEd after cell work (cells/results/2026-10-18-lab-obs.txt:490;
	// cells/results/2026-10-24-boot4.txt:58-59; cells/results/2026-10-27-boot6.txt:400).
	warm := byName["warm-up m8i.8xlarge"].IDs
	if !slices.Equal(warm, []string{"g20261018-225322", "g20261024-085719", "g20261024-090728", "g20261027-054034"}) {
		t.Errorf("the warm-up gates: %q", warm)
	}
	for i, id := range warm {
		g, ok := rows.Gates[id]
		if !ok {
			continue // TestSets has said so
		}
		verdict, class := "BLOCK", "invalid"
		if i == 0 || i == 3 {
			verdict, class = "ADVANCE", ""
		}
		if v, c, tag, plan := str(at(g, "verdict")), str(at(g, "class")), str(at(g, "server_tag")),
			str(at(g, "plan")); v != verdict || c != class || tag != "1.31.0" || plan != "gate/plan.yaml" {
			t.Errorf("warm-up %s is %s %s on %s, %s", id, v, c, tag, plan)
		}
	}
	if got := rs.SetsOf("g20261017-174403"); len(got) != 2 || got[0].Kind != KindCalibration || got[1].Kind != KindAA {
		t.Errorf("SetsOf c1: %+v", got)
	}
}

func readLines(t *testing.T, file string) []string {
	t.Helper()
	fh, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var out []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestParseSet: what a set file must be, and what it refuses.
func TestParseSet(t *testing.T) {
	const cite = "# cells/results/x.txt:3-4, two\n"
	good, err := ParseSet("gate/sets/control-x.txt", []byte("# a header\n# 2 gates.\n"+cite+
		"g20261018-080445\n\ng20261018-080919\n"))
	if err != nil || good.Kind != KindControl || good.Label != "x" || good.Count != 2 ||
		!slices.Equal(good.IDs, []string{"g20261018-080445", "g20261018-080919"}) ||
		good.Cites[1] != (Cite{"cells/results/x.txt", 3, 4}) {
		t.Errorf("a good set: %+v, %v", good, err)
	}
	if s, err := ParseSet("_obs/aa/w-t.txt", []byte("# 1 gate\ng20261018-080445\n")); err != nil ||
		s.Kind != KindAA || s.Cites[0] != (Cite{}) {
		t.Errorf("an A/A list needs no citation: %+v, %v", s, err)
	}
	for _, c := range []struct{ path, text, why string }{
		{"gate/sets/control-x.txt", "# 1 gate\r\n" + cite + "g20261018-080445\n", "LF lines"},
		{"gate/sets/control-x.txt", "# 1 gate\n" + cite + "g20261018-080445", "LF lines"},
		{"gate/sets/other-x.txt", "# 1 gate\n" + cite + "g20261018-080445\n", "kind one of"},
		{"gate/sets/control-.txt", "# 1 gate\n" + cite + "g20261018-080445\n", "kind one of"},
		{"gate/other/control-x.txt", "# 1 gate\n" + cite + "g20261018-080445\n", "under gate/sets or _obs/aa"},
		{"gate/sets/control-x.txt", "# 1 gate\n" + cite + " g20261018-080445\n", "neither a comment"},
		{"gate/sets/control-x.txt", "# 1 gate\n" + cite + "g2026101-080445\n", "neither a comment"},
		{"gate/sets/control-x.txt", "# 2 gates\n" + cite + "g20261018-080445\ng20261018-080445\n", "twice"},
		{"gate/sets/control-x.txt", "# 1 gate\n# 1 gate\n" + cite + "g20261018-080445\n", "a second count line"},
		{"gate/sets/control-x.txt", "# one gate\n" + cite + "g20261018-080445\n", "no count line"},
		{"gate/sets/control-x.txt", "# 2 gates\n" + cite + "g20261018-080445\n", "1 ids, and its count line says 2"},
		{"gate/sets/control-x.txt", "# 1 gate\ng20261018-080445\n", "no line"},
		{"gate/sets/control-x.txt", "# 1 gate\n# cells/results/x.txt:4-3\ng20261018-080445\n", "the cited lines, 4 to 3, are no range"},
		{"gate/sets/control-x.txt", "# 1 gate\n# cells/results/x.txt:0\ng20261018-080445\n", "the cited lines, 0 to 0, are no range"},
	} {
		if _, err := ParseSet(c.path, []byte(c.text)); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s %q: %v, want %q", c.path, c.text, err, c.why)
		}
	}
	if sets, err := ReadSets(fstest.MapFS{}); err != nil || sets != nil {
		t.Errorf("no set directory: %v, %v", sets, err)
	}
}

// TestEpochs groups the lab runs by boot and holds the groups to the transcripts: each boot's first gate is its
// first run that was not refused, and the two A/A gates of 55 that BLOCKed on a max are each the first of theirs.
func TestEpochs(t *testing.T) {
	t.Run("the gates rows", rowEpochs)
	if archived(t) {
		return
	}
	rs := records(t)
	byRun := map[string]Epoch{}
	for _, e := range rs.Epochs {
		for _, id := range e.Runs {
			byRun[id] = e
		}
	}
	spread, labEpochs, devRuns, devEpochs := 0.0, map[int64]bool{}, 0, map[int64]bool{}
	for _, e := range rs.Epochs {
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, id := range e.Runs {
			r, _ := rs.Run(id)
			off := r.T0.Wall.F - r.T0.Mono.F
			lo, hi = math.Min(lo, off), math.Max(hi, off)
			switch {
			case lab(*r):
				labEpochs[e.Start] = true
			case r.Host.VCPU.String() == "24":
				devRuns++
				devEpochs[e.Start] = true
			}
		}
		if !labEpochs[e.Start] {
			continue
		}
		// One boot's offsets sit well inside one second, so flooring cuts none of them in two.
		if lo-math.Floor(lo) < 0.05 || hi-math.Floor(lo) > 0.95 {
			t.Errorf("epoch %d: offsets %.3f to %.3f lie near a second's edge", e.Start, lo, hi)
		}
		spread = math.Max(spread, hi-lo)
	}
	t.Logf("the lab runs: %d epochs, offsets within each %.4f s apart at most", len(labEpochs), spread)
	if len(labEpochs) != 11 || Num(spread) != "0.01" {
		t.Errorf("the lab runs: %d epochs, offsets within each %s s apart at most", len(labEpochs), Num(spread))
	}
	// The dev host's clock: each run its own epoch (EpochOf).
	if len(devEpochs) != devRuns {
		t.Errorf("the dev host: %d epochs over %d runs", len(devEpochs), devRuns)
	}
	same := func(a, b string) {
		if byRun[a].Start == 0 || byRun[a].Start != byRun[b].Start {
			t.Errorf("%s and %s are not in one epoch", a, b)
		}
	}
	same("g20261017-171604", "g20261017-225512") // calibration: one boot for all three launches (calibration.txt:5-6)
	same("g20261018-155946", "g20261018-202807") // validation Part 2: two launches on one boot (validation.txt:25-26)
	// The three first gates since a boot: the calibration session's, the validation's Part 1's and its Part 2 first
	// launch's (validation.txt:833-835); the calibration session's after its refusal.
	for _, id := range []string{"g20261017-171614", "g20261018-062701", "g20261018-155946"} {
		if byRun[id].First != id {
			t.Errorf("%s: its epoch's first gate is %q", id, byRun[id].First)
		}
	}
	// Two A/A gates that BLOCKed, each a box's first gate since its start (cells/results/2026-10-24-boot4.txt:680-683).
	for _, id := range []string{"g20261017-171614", "g20261018-155946"} {
		if r, ok := rs.Run(id); !ok || r.Class != "invalid" || r.Deciding() != "R4" {
			t.Errorf("%s: %+v", id, r)
		}
	}
	// The stack's session: its first committed gate, w0, was the box's second since its start, the first not fetched
	// (cells/results/2026-10-18-lab-obs.txt:66-68, :87).
	if e := byRun["g20261018-225322"]; e.First != "g20261018-225322" || len(e.Runs) != 10 {
		t.Errorf("the stack's epoch: %+v", e)
	}
	if got := Epochs([]Run{{ID: "a", Epoch: 5, Refused: "x"}, {ID: "b"}, {ID: "c", Epoch: 2}}); fmt.Sprint(got) !=
		"[{2 [c] c} {5 [a] }]" {
		t.Errorf("Epochs: %v", got)
	}
}

// rowEpochs is TestEpochs on the rows, the whole of it once runs are archived: the epochs from the gates rows' boots
// (rowBoot), which -tables cut from the same t0 (EpochOf), held to the same transcripts' lines. The offsets within a
// boot need each run's t0, which its row keeps as a date alone; the full tree checks them.
func rowEpochs(t *testing.T) {
	disk, err := RowsOf(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	var runs []Run
	for id, g := range disk.Gates {
		r := Run{ID: id, Refused: str(at(g, "refused"))}
		if b := rowBoot(g); b != "" {
			start, err := time.Parse("20060102-150405", b[1:])
			if err != nil {
				t.Fatalf("%s: boot %q: %v", id, b, err)
			}
			r.Epoch = start.Unix()
		}
		r.Host.VCPU = num(at(g, "host", "vcpu"))
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b Run) int { return strings.Compare(a.ID, b.ID) })
	byRun := map[string]Epoch{}
	labEpochs := map[int64]bool{}
	for _, e := range Epochs(runs) {
		for _, id := range e.Runs {
			byRun[id] = e
			if r := runs[slices.IndexFunc(runs, func(r Run) bool { return r.ID == id })]; lab(r) {
				labEpochs[e.Start] = true
			}
		}
	}
	if len(labEpochs) != 11 {
		t.Errorf("the lab runs' gates rows: %d epochs, want 11", len(labEpochs))
	}
	for _, p := range [][2]string{{"g20261017-171604", "g20261017-225512"}, {"g20261018-155946", "g20261018-202807"}} {
		if byRun[p[0]].Start == 0 || byRun[p[0]].Start != byRun[p[1]].Start {
			t.Errorf("%s and %s are not in one epoch", p[0], p[1])
		}
	}
	for _, id := range []string{"g20261017-171614", "g20261018-062701", "g20261018-155946"} {
		if byRun[id].First != id {
			t.Errorf("%s: its epoch's first gate is %q", id, byRun[id].First)
		}
	}
	for _, id := range []string{"g20261017-171614", "g20261018-155946"} {
		if g := disk.Gates[id]; str(at(g, "class")) != "invalid" || gateDeciding(g) != "R4" {
			t.Errorf("%s: class %s, deciding %q", id, at(g, "class"), gateDeciding(g))
		}
	}
	if e := byRun["g20261018-225322"]; e.First != "g20261018-225322" || len(e.Runs) != 10 {
		t.Errorf("the stack's epoch: %+v", e)
	}
}

// TestBands draws the A/A lists' bands from compare rows: the three-item list's 12 compared readings over its 30
// gates, each range as recorded (jq over the 30 gates' verdict.json), and the two-item list's none, its plan naming
// no incumbent. The bands Load draws are the ones docs/tables/compare.jsonl's rows give alone, with no run read, and
// a run that leaves gate/results leaves its band as it was while its rows stay.
func TestBands(t *testing.T) {
	rs := records(t)
	disk, err := RowsOf(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	var aa []Set
	for _, s := range rs.Sets {
		if s.Kind == KindAA {
			aa = append(aa, s)
		}
	}
	for i, s := range aa {
		if got := BandOf(s, disk); i >= len(rs.Bands) || !reflect.DeepEqual(got, rs.Bands[i]) {
			t.Errorf("%s: the band of the committed rows alone is not the one Load draws", s.Path)
		}
	}
	if len(aa) > 0 && len(aa[0].IDs) > 0 {
		gone := Rows{Gates: map[string]jsonx.Value{}, Compare: map[string][]jsonx.Value{}}
		for id, g := range disk.Gates {
			if id != aa[0].IDs[0] {
				gone.Gates[id] = g
			}
		}
		for id, c := range disk.Compare {
			if id != aa[0].IDs[0] {
				gone.Compare[id] = c
			}
		}
		if err := gone.add(os.DirFS(root)); err != nil || !reflect.DeepEqual(BandOf(aa[0], gone), rs.Bands[0]) {
			t.Errorf("a run gone, its rows kept: the band changed (%v)", err)
		}
	}
	if len(rs.Bands) != 2 || rs.Bands[0].List != "_obs/aa/arm-3item-m8i.8xlarge.txt" || len(rs.Bands[0].Rows) != 12 ||
		rs.Bands[1].List != "_obs/aa/plan-2item-m8i.8xlarge.txt" || rs.Bands[1].Rows != nil {
		t.Fatalf("bands: %+v", rs.Bands)
	}
	got := map[string]string{}
	for _, b := range rs.Bands[0].Rows {
		if b.N != 30 {
			t.Errorf("%s: n %d", b.Name, b.N)
		}
		got[b.Name] = fmt.Sprintf("%s %s %s %s", b.Scale, b.Min, b.Max, Num(b.Mean))
	}
	for name, want := range map[string]string{
		"workflow_task_schedule_to_start_p99_ms": "ms -0.709 0.495 -0.012",
		"persistence_latency_p99_ms":             "ms -0.181 0.163 -0.019",
		"activity_schedule_to_start_over_10ms":   "share -0.003104 0.003701 0",
		"service_error_rate":                     "share 0 0 0",
		"workflows_completed":                    "count -69 71 7.267",
	} {
		if got[name] != want {
			t.Errorf("%s: %s, want %s", name, got[name], want)
		}
	}
	if rs.Bands[0].Rows[0].Name != "workflow_task_schedule_to_start_p99_ms" {
		t.Errorf("rows not in the gates' order: %s first", rs.Bands[0].Rows[0].Name)
	}
}
