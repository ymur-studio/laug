package report

// records.go is the records layer: every committed gate run as its gate.json and verdict.json record it, the
// judge version each names and its S1 answer through internal/rejudge, the counted sets under gate/sets/ and the A/A
// lists under _obs/aa/, the A/A bands those lists draw, and the boot epochs. It reads committed files only and writes
// nothing. Nothing is inferred: a field a run did not record is "" or not OK, and the documents say so. The tables and
// the figures read the rest when they draw it: verdict.json's items, timeline.jsonl, poll.jsonl, the thresholds and
// the item files.

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/rejudge"
)

// Records are what the tables and the documents are written from.
type Records struct {
	Runs []Run // every run in the results directory, by gate id, as internal/rejudge lists them
	// NotRun says why no run was judged again, "" when every one was: the checkout's hold failed (rejudge.Checkout's
	// NotHeld), and judged again each would read "not judged". The ledger then prints each run's recorded verdict, its
	// rejudge column empty, and the line "rejudge not run: <NotRun>".
	NotRun string
	Counts rejudge.Counts // the rejudge's counts, as laug gate rejudge -all's last line gives them; nil with NotRun
	Sets   []Set          // gate/sets/*.txt, then _obs/aa/*.txt, each by name
	Bands  []Band         // one per A/A list, in Sets' order, drawn from rows.Compare
	Epochs []Epoch        // by start
	byID   map[string]int
	// checkout is the checkout's root the records were read under, nil when Load named none: the documents' figures
	// are drawn from its committed files.
	checkout fs.FS
	// rows are the gates and compare rows the bands and the sets' counts are read from (Rows).
	rows Rows
}

// A Run is one gate run as its two files record it. A text is "" where the run recorded null or nothing.
type Run struct {
	ID string // the gate id, its directory's name

	// gate.json
	ServerTag  string
	Host       Host
	Plan       string
	Driver     string // run.sh or step.sh; "" where gate.json records none, which the tables show and never infer
	WorkflowID string
	Fake       bool
	TreeDirty  string // true or false, as recorded; "" for null
	SetSHA256  string // load/steady-state.yaml's sha256, the threshold set S2 names
	// Recorded is the judge gate.json records (rejudge.ReadRecorded). Judge is the registry's version for it, or
	// zero with JudgeWhy saying why the registry names none.
	Recorded rejudge.Recorded
	Judge    judge.Version
	JudgeWhy string

	// verdict.json
	Verdict, Class string
	Incumbent      string // the plan's incumbent, "" for a plan with none
	Refused        string // why preflight refused the gate, "" when it did not
	StoppedAt      string // the step a trap verdict names, "" for a judge's verdict
	Reasons        []string
	Rules          []Rule // in recorded order, R1 first; nil where rules is null, as in a trap verdict
	HostQuiet      bool
	T0, TPost      Clock
	Elapsed        Number      // elapsed_s.wall: t0 to the verdict handed on
	Comparison     *Comparison // nil where verdict.json has none: a judge before the arm, or a trap verdict

	// The files beside those two: poll.jsonl, harness.json and an item directory.
	Poll, Harness, ItemDirs bool

	Epoch int64 // its boot epoch (EpochOf); 0 when t0 is not two numbers
	// Rejudge is its S1 answer (rejudge.Rejudge) and RejudgeWhy that answer's why; "" when no run was judged again.
	Rejudge    rejudge.Outcome
	RejudgeWhy string
	Problems   []string // the files that could not be read or parsed; the run is shown as far as it was read
}

// Host is the host a run's gate.json records: Type "" where it records none, which hostLabel shows as "type not
// recorded" beside the vCPU, never inferred.
type Host struct {
	Type                      string
	VCPU, MemGiB, MemAvailGiB Number
}

// A Rule is one of R1 to R7 as verdict.json records it.
type Rule struct {
	ID      string // R1 to R7
	Name    string // harness, coverage, contract, outcome, live control, identity, incumbent
	Pass    bool
	Reasons []string
}

// A Clock is a stamp of gate/lib.sh's stamp(): the wall clock to the millisecond and the monotonic one, /proc/uptime's
// first field, seconds since the boot (gate/lib.sh, stamp; proc(5), /proc/uptime).
type Clock struct{ Wall, Mono Number }

// A Comparison is R7's: the release against the incumbent in one gate.
type Comparison struct {
	Mode string // judging or recording
	Rows []Row
}

// A Row is one compared reading. D is the release's reading less the incumbent's as R7 rounds it; a reading the
// release did not produce has no D.
type Row struct {
	Name, Scale                      string
	Release, Incumbent, D, Tolerance Number
	Judged                           bool
	Result                           string // ok, WORSE, recorded, not produced or not compared
}

// A Number is a number a run recorded, as jq 1.7 reads the file back (jsonx): the text verdict.md prints, so a d
// written 6.4e-05 reads 0.000064 (internal/judge/verdict.go:14-20). OK is false for null, an absent key or a value of
// another kind, and String is then "".
type Number struct {
	jsonx.Number
	OK bool
}

func (n Number) String() string {
	if !n.OK {
		return ""
	}
	return n.Number.String()
}

// Deciding is what the ledger's deciding rule column says for the run: "refused: <why>" for a refused gate, "stopped
// at <step>" for a trap verdict, for which no rule decides, else the first rule in recorded order that failed, its id,
// and "" when none did. Every committed BLOCK has one of the three (TestRecordsReadEveryRun).
func (r Run) Deciding() string {
	switch {
	case r.Refused != "":
		return "refused: " + r.Refused
	case r.StoppedAt != "":
		return "stopped at " + r.StoppedAt
	}
	for _, rule := range r.Rules {
		if !rule.Pass {
			return rule.ID
		}
	}
	return ""
}

// Load reads the records: the runs in results, gate/results or a copy of it, through internal/rejudge, each judged
// again under c's hold, and the sets under root, the checkout ("" reads none). The bands are drawn from the runs'
// gates and compare rows, and when results is root's own gate/results, from the rows docs/tables holds for runs no
// longer there too (Rows). An error is a results directory that is not there or holds no run, a set that does not
// parse, or a table under docs/tables that does not read; a run's file that cannot be read is that run's Problems.
func Load(root, results string, c rejudge.Checkout) (*Records, error) {
	dir := rejudge.Resolve(results)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w at %s", rejudge.ErrNoResults, c.Shown(dir))
	}
	fsys := os.DirFS(dir)
	listed, err := rejudge.Runs(fsys)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Shown(dir), err)
	}
	if len(listed) == 0 {
		return nil, fmt.Errorf("%w in %s", rejudge.ErrNoRun, c.Shown(dir))
	}
	rs := &Records{NotRun: c.NotHeld, byID: map[string]int{}}
	var answers []rejudge.Answer
	for i, l := range listed {
		r := ReadRun(fsys, l.Gid)
		r.Recorded = l.Recorded
		if l.Registered() {
			r.Judge = l.Version
		} else {
			r.JudgeWhy = l.Recorded.Why()
		}
		if c.NotHeld == "" {
			a := rejudge.Rejudge(filepath.Join(dir, l.Gid), c)
			r.Rejudge, r.RejudgeWhy = a.Outcome, a.Why
			answers = append(answers, a)
		}
		rs.Runs = append(rs.Runs, r)
		rs.byID[r.ID] = i
	}
	if c.NotHeld == "" {
		rs.Counts = rejudge.Count(answers)
	}
	rs.rows = runRows(fsys, rs.Runs)
	if root != "" {
		rs.checkout = os.DirFS(root)
		if rs.Sets, err = ReadSets(rs.checkout); err != nil {
			return nil, err
		}
		if dir == rejudge.Resolve(filepath.Join(root, "gate", "results")) {
			if err := rs.rows.add(rs.checkout); err != nil {
				return nil, err
			}
		}
	}
	for _, s := range rs.Sets {
		if s.Kind == KindAA {
			rs.Bands = append(rs.Bands, BandOf(s, rs.rows))
		}
	}
	rs.Epochs = Epochs(rs.Runs)
	return rs, nil
}

// Run is the run with gate id id.
func (rs *Records) Run(id string) (*Run, bool) {
	i, ok := rs.byID[id]
	if !ok {
		return nil, false
	}
	return &rs.Runs[i], true
}

// SetsOf is every set that names the run, in Sets' order.
func (rs *Records) SetsOf(id string) []Set {
	var out []Set
	for _, s := range rs.Sets {
		if slices.Contains(s.IDs, id) {
			out = append(out, s)
		}
	}
	return out
}

// ReadRun reads the run in results/id: its gate.json and verdict.json, read as jq reads them (jsonx), and which of
// poll.jsonl, harness.json and an item directory it holds. The judge and the rejudge are Load's.
func ReadRun(results fs.FS, id string) Run {
	r := Run{ID: id}
	g, v := r.read(results, "gate.json"), r.read(results, "verdict.json")

	r.ServerTag, r.Plan = str(at(g, "server_tag")), str(at(g, "plan"))
	r.Host = Host{Type: str(at(g, "host", "type")), VCPU: num(at(g, "host", "vcpu")),
		MemGiB: num(at(g, "host", "mem_gib")), MemAvailGiB: num(at(g, "host", "mem_available_gib"))}
	r.Driver, r.WorkflowID = str(at(g, "driver")), str(at(g, "workflow_id"))
	r.Fake, _ = at(g, "fake").Bool()
	if d := at(g, "tree_dirty"); d.Kind() == jsonx.KindFalse || d.Kind() == jsonx.KindTrue {
		r.TreeDirty = d.String()
	}
	in, _ := at(g, "inputs").Arr()
	for _, e := range in {
		if str(at(e, "path")) == "load/steady-state.yaml" && at(e, "sha256").Kind() == jsonx.KindString {
			r.SetSHA256 = str(at(e, "sha256"))
			break
		}
	}

	r.Verdict, r.Class, r.Incumbent = str(at(v, "verdict")), str(at(v, "class")), str(at(v, "incumbent"))
	r.Refused, r.StoppedAt, r.Reasons = str(at(v, "refused")), str(at(v, "stopped_at")), strs(at(v, "reasons"))
	if rules, ok := at(v, "rules").Obj(); ok {
		r.Rules = []Rule{}
		for i := range rules.Len() {
			id, rule := rules.Member(i)
			pass, _ := at(rule, "pass").Bool()
			r.Rules = append(r.Rules, Rule{ID: id, Name: str(at(rule, "name")), Pass: pass,
				Reasons: strs(at(rule, "reasons"))})
		}
	}
	r.HostQuiet, _ = at(v, "host_quiet").Bool()
	r.T0 = Clock{num(at(v, "t0", "wall")), num(at(v, "t0", "mono"))}
	r.TPost = Clock{num(at(v, "t_post", "wall")), num(at(v, "t_post", "mono"))}
	r.Elapsed = num(at(v, "elapsed_s", "wall"))
	if c := at(v, "comparison"); c.Kind() == jsonx.KindObject {
		r.Comparison = &Comparison{Mode: str(at(c, "mode"))}
		rows, _ := at(c, "rows").Arr()
		for _, row := range rows {
			judged, _ := at(row, "judged").Bool()
			r.Comparison.Rows = append(r.Comparison.Rows, Row{Name: str(at(row, "name")),
				Scale: str(at(row, "scale")), Release: num(at(row, "release")), Incumbent: num(at(row, "incumbent")),
				D: num(at(row, "d")), Tolerance: num(at(row, "tolerance")), Judged: judged,
				Result: str(at(row, "result"))})
		}
	}

	r.Poll, r.Harness = isFile(results, id+"/poll.jsonl"), isFile(results, id+"/harness.json")
	entries, _ := fs.ReadDir(results, id)
	r.ItemDirs = slices.ContainsFunc(entries, func(e fs.DirEntry) bool {
		return e.IsDir() && !strings.HasPrefix(e.Name(), ".")
	})
	r.Epoch = EpochOf(r.T0)
	return r
}

// read parses the run's file name as one JSON value, as internal/judge's load does; a file that cannot be read, or
// is not one object, is a problem, and reads as null.
func (r *Run) read(results fs.FS, name string) jsonx.Value {
	data, err := fs.ReadFile(results, r.ID+"/"+name)
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err // the run and file are named here, not the absolute path
	}
	var v jsonx.Value
	if err == nil {
		v, err = jsonx.Parse(data)
	}
	if err == nil && v.Kind() != jsonx.KindObject {
		err = errors.New("not a JSON object")
	}
	if err != nil {
		r.Problems = append(r.Problems, name+": "+err.Error())
		return jsonx.Null()
	}
	return v
}

// at is v's member down the keys, null where a value on the way is no object or has no such key.
func at(v jsonx.Value, keys ...string) jsonx.Value {
	for _, k := range keys {
		o, ok := v.Obj()
		if !ok {
			return jsonx.Null()
		}
		v, _ = o.Get(k)
	}
	return v
}

// str is v when it is a string, else "".
func str(v jsonx.Value) string {
	s, _ := v.Str()
	return s
}

// strs is v's string elements when it is an array.
func strs(v jsonx.Value) []string {
	a, _ := v.Arr()
	var out []string
	for _, e := range a {
		if s, ok := e.Str(); ok {
			out = append(out, s)
		}
	}
	return out
}

func num(v jsonx.Value) Number {
	n, ok := v.Num()
	return Number{n, ok}
}

func isFile(fsys fs.FS, name string) bool {
	fi, err := fs.Stat(fsys, name)
	return err == nil && fi.Mode().IsRegular()
}

// EpochOf is a run's boot epoch: t0's wall clock less its monotonic clock, the wall clock at the host's boot, floored
// to the second. Two runs of one boot share it: on the lab host the difference moves 0.01 s at most within a boot,
// and no boot's sits within 0.05 s of a whole second (TestEpochs). A host whose monotonic clock falls behind its wall
// clock between runs gives each run its own, as each of the dev host's runs at 24 vCPU has. 0 when t0 is not two
// numbers.
func EpochOf(t0 Clock) int64 {
	if !t0.Wall.OK || !t0.Mono.OK {
		return 0
	}
	return int64(math.Floor(t0.Wall.F - t0.Mono.F))
}

// An Epoch is the runs of one boot epoch.
type Epoch struct {
	Start int64    // EpochOf, seconds since 1970 UTC
	Runs  []string // gate ids, in order
	// First is the epoch's first run that was not refused: a refused gate stopped at preflight and ran no load, and the
	// transcripts do not count it as a box's first gate
	// (docs/tables/records.jsonl#cells/results/2026-10-17-lab-arm-calibration.txt lines 59-61, the refusal
	// g20261017-171604 before c1). It is the first committed one: a gate the box ran that was not fetched is not a run
	// here (docs/tables/records.jsonl#cells/results/2026-10-18-lab-obs.txt lines 66-68, 87), so this is "the first
	// committed gate of the epoch". "" when every run was refused.
	First string
}

// Epochs groups runs by boot epoch: the epochs by start, each epoch's runs in the order given, Load's by gate id. A
// run with no epoch is in none.
func Epochs(runs []Run) []Epoch {
	index := map[int64]int{}
	var out []Epoch
	for _, r := range runs {
		if r.Epoch == 0 {
			continue
		}
		i, ok := index[r.Epoch]
		if !ok {
			i = len(out)
			index[r.Epoch] = i
			out = append(out, Epoch{Start: r.Epoch})
		}
		out[i].Runs = append(out[i].Runs, r.ID)
		if out[i].First == "" && r.Refused == "" {
			out[i].First = r.ID
		}
	}
	slices.SortFunc(out, func(a, b Epoch) int { return cmp.Compare(a.Start, b.Start) })
	return out
}

// The kinds of set. A file under gate/sets is named <kind>-<label>.txt; a list under _obs/aa is an A/A list.
const (
	KindCalibration = "calibration" // the gates a tolerance was set from
	KindValidation  = "validation"  // the gates that tested it, R7 judging
	KindControl     = "control"     // a control branch's gates; the label is the branch
	KindWarmUp      = "warm-up"     // a gate run first to warm the box, counted toward no set
	KindAA          = "aa"          // an A/A list of _obs/aa, which the stack draws its band from
)

var setKinds = []string{KindCalibration, KindValidation, KindControl, KindWarmUp}

// A Set is a committed list of gate ids, gate/sets/<kind>-<label>.txt or _obs/aa/<label>.txt, in _obs/aa's format: one
// gate id a line, a line starting with # a comment. One comment line states the count, "# <n> gates" or "# 1 gate".
// In gate/sets every id comes after a line "# <file>:<from>[-<to>], ..." naming the result file lines that record it.
type Set struct {
	Path  string   // repository-relative
	Kind  string   // one of the five kinds
	Label string   // the name less its kind: "arm-known-slow", "arm-3item-m8i.8xlarge"
	Count int      // the count its header states, which is len(IDs)
	IDs   []string // in the file's order
	Cites []Cite   // where each id is recorded, beside IDs; zero in an A/A list
}

// A Cite is the lines of a committed result file that record an id: From to To, from 1.
type Cite struct {
	File     string
	From, To int
}

var (
	gateID    = regexp.MustCompile(`^g[0-9]{8}-[0-9]{6}$`)
	countLine = regexp.MustCompile(`^# ([0-9]+) gates?([ ,.]|$)`)
	citeLine  = regexp.MustCompile(`^# ((?:cells|faults|load|gate)/results/[\w./-]+):([0-9]+)(?:-([0-9]+))?(,|$)`)
)

// ReadSets reads gate/sets/*.txt and _obs/aa/*.txt from fsys, a checkout's root, each directory by name. A directory
// that is not there holds no set.
func ReadSets(fsys fs.FS) ([]Set, error) {
	var sets []Set
	for _, dir := range []string{"gate/sets", "_obs/aa"} {
		entries, err := fs.ReadDir(fsys, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".txt") || !e.Type().IsRegular() {
				continue
			}
			p := path.Join(dir, e.Name())
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				return nil, err
			}
			s, err := ParseSet(p, data)
			if err != nil {
				return nil, err
			}
			sets = append(sets, s)
		}
	}
	return sets, nil
}

// ParseSet reads the set at the repository-relative path p from its bytes. It refuses a file that is not LF lines
// ending in one, a name of no kind, a line that is neither a comment nor a gate id, an id twice, a count line missing,
// twice or unequal to the ids, and in gate/sets an id with no citation before it.
func ParseSet(p string, data []byte) (Set, error) {
	s := Set{Path: p}
	bad := func(format string, a ...any) (Set, error) {
		return Set{}, fmt.Errorf("%s: %s", p, fmt.Sprintf(format, a...))
	}
	name := strings.TrimSuffix(path.Base(p), ".txt")
	switch path.Dir(p) {
	case "_obs/aa":
		s.Kind, s.Label = KindAA, name
	case "gate/sets":
		for _, k := range setKinds {
			if l, ok := strings.CutPrefix(name, k+"-"); ok && l != "" {
				s.Kind, s.Label = k, l
			}
		}
		if s.Kind == "" {
			return bad("the name is not <kind>-<label>.txt, kind one of %s", strings.Join(setKinds, ", "))
		}
	default:
		return bad("a set is under gate/sets or _obs/aa")
	}
	body, ok := bytes.CutSuffix(data, []byte("\n"))
	if !ok || bytes.ContainsRune(data, '\r') {
		return bad("want LF lines, the last one ended")
	}
	counted, cite := false, Cite{}
	for i, line := range strings.Split(string(body), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			if m := countLine.FindStringSubmatch(line); m != nil {
				if counted {
					return bad("line %d: a second count line", i+1)
				}
				counted = true
				s.Count, _ = strconv.Atoi(m[1])
			}
			if m := citeLine.FindStringSubmatch(line); m != nil {
				from, _ := strconv.Atoi(m[2])
				to := from
				if m[3] != "" {
					to, _ = strconv.Atoi(m[3])
				}
				if from < 1 || to < from {
					return bad("line %d: the cited lines, %d to %d, are no range from line 1", i+1, from, to)
				}
				cite = Cite{File: m[1], From: from, To: to}
			}
		case line == "":
		case !gateID.MatchString(line):
			return bad("line %d: neither a comment nor a gate id: %q", i+1, line)
		case slices.Contains(s.IDs, line):
			return bad("line %d: %s twice", i+1, line)
		case s.Kind != KindAA && cite.File == "":
			return bad("line %d: %s has no line \"# <file>:<lines>, ...\" before it", i+1, line)
		default:
			s.IDs = append(s.IDs, line)
			s.Cites = append(s.Cites, cite)
		}
	}
	switch {
	case !counted:
		return bad("no count line, \"# <n> gates\"")
	case s.Count != len(s.IDs):
		return bad("%d ids, and its count line says %d", len(s.IDs), s.Count)
	}
	return s, nil
}

// A Band is an A/A list's spread of each compared reading's d over the list's gates, least and most as recorded: the
// noise band. No document draws Records.Bands now (lessondata's armBand reads the records itself); records_test holds
// it to BandOf over the committed compare rows. A list whose gates compared nothing, as one whose plan names no
// incumbent, has no rows.
type Band struct {
	List string // the list's path
	Rows []BandRow
}

// A BandRow is one compared reading's d over the gates of a list that recorded it as a number.
type BandRow struct {
	Name, Scale string
	N           int
	Min, Max    Number  // as recorded
	Mean        float64 // the sum in the list's order, over N
}

// BandOf is s's Band over the compare rows of the gates it names, its rows in the order the list's gates first record
// them, each gate's in the order its verdict.json holds them (pos).
func BandOf(s Set, rows Rows) Band {
	b := Band{List: s.Path}
	sum := map[string]float64{}
	for _, id := range s.IDs {
		for _, row := range rows.Compare[id] {
			name := str(at(row, "reading"))
			i := slices.IndexFunc(b.Rows, func(br BandRow) bool { return br.Name == name })
			if i < 0 {
				i = len(b.Rows)
				b.Rows = append(b.Rows, BandRow{Name: name, Scale: str(at(row, "scale"))})
			}
			d := num(at(row, "d"))
			if !d.OK {
				continue
			}
			br := &b.Rows[i]
			if br.N == 0 || d.F < br.Min.F {
				br.Min = d
			}
			if br.N == 0 || d.F > br.Max.F {
				br.Max = d
			}
			br.N++
			sum[name] += d.F
		}
	}
	for i := range b.Rows {
		if b.Rows[i].N > 0 {
			b.Rows[i].Mean = sum[b.Rows[i].Name] / float64(b.Rows[i].N)
		}
	}
	return b
}
