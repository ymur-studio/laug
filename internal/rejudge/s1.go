package rejudge

// S1, bytes, in Go: a run's own judge version, run again on the run as it stands, gives its verdict.json and
// verdict.md byte for byte. Rejudge judges one run, with the line and the exit laug gate rejudge gives it, and
// RejudgeAll every run in a results directory. Judge reads the run in place and writes nothing.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
)

// An Outcome is how a run judged again compares with the two files it holds.
type Outcome string

const (
	// Identical is both files byte for byte.
	Identical Outcome = "identical"
	// Known is a run that differs in a way whose proof holds here: an empty item directory, which git does not
	// carry, made again in memory, and then both files byte for byte (explain).
	Known Outcome = "known"
	// Rerendered is a run whose verdict.json is the gate's trap verdict, not a judge's: re-rendered by its form from
	// its own step and reason, it gives both files byte for byte. Never counted identical.
	Rerendered Outcome = "re-rendered"
	// Unexplained is a run that differs, and nothing here explains how.
	Unexplained Outcome = "unexplained"
	// NotJudged is a run the judge was not held to, or on which it wrote no verdict.
	NotJudged Outcome = "not judged"
)

// Outcomes are the five, in the order the counts line names them.
var Outcomes = []Outcome{Identical, Known, Rerendered, Unexplained, NotJudged}

// Files are a run's verdict.json and verdict.md.
type Files struct{ JSON, MD []byte }

// An Answer is one run judged again.
type Answer struct {
	// ID is the name its line starts with: the base name of the run's directory, symlinks resolved
	// (Resolve).
	ID      string
	Outcome Outcome
	// Why is, for Known, the difference named; for Rerendered, "trap <form>"; for NotJudged, why not; else "".
	Why string
	// Recorded is the judge the run's gate.json records (ReadRecorded).
	Recorded Recorded
	// Version judged the run: the recorded one when the registry names it, else judge.Current, whose verdict the
	// long form shows beside the reason the run is not judged. Zero when no judge ran.
	Version judge.Version
	// Committed is the run's own two files; Rejudged the judge's, nil when no judge ran or it wrote no verdict.
	Committed, Rejudged Files
}

// Line is the run's line, as laug gate rejudge -brief and -all print it:
// "<id>  identical", "<id>  differs: <why>", "<id>  differs: unexplained", "<id>  re-rendered: trap <form>" or
// "<id>  not judged: <why>".
func (a Answer) Line() string {
	switch a.Outcome {
	case Identical:
		return a.ID + "  identical"
	case Known:
		return a.ID + "  differs: " + a.Why
	case Rerendered:
		return a.ID + "  re-rendered: " + a.Why
	case NotJudged:
		return a.ID + "  not judged: " + a.Why
	}
	return a.ID + "  differs: unexplained"
}

// Exit is the run's exit, as laug gate rejudge gives it: 0 identical, 2 not judged, 1 for a run
// that differs, known, re-rendered or not.
func (a Answer) Exit() int {
	switch a.Outcome {
	case Identical:
		return 0
	case NotJudged:
		return 2
	}
	return 1
}

// A Checkout is where a rejudge runs.
type Checkout struct {
	// Root is the repository root, which a path in a line is shown against (Shown); "" outside a checkout.
	Root string
	// NotHeld says why the judge this binary embeds is not held to the one HEAD holds, "" when it is. When it is
	// set, every run is not judged: no judge but HEAD's judges a run.
	NotHeld string
}

// Shown is path as a line shows it: relative to the repository root when it is
// below it, else "(outside the repository)/<its base name>", after symlinks.
func (c Checkout) Shown(path string) string {
	p := Resolve(path)
	if c.Root != "" {
		if rel, ok := strings.CutPrefix(p, Resolve(c.Root)+string(filepath.Separator)); ok {
			return rel
		}
	}
	return "(outside the repository)/" + filepath.Base(p)
}

// Resolve is `realpath -m`: path made absolute and clean, every symlink in the part of it that exists resolved.
func Resolve(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	rest := ""
	for p := abs; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return abs
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// Rejudge judges the run in dir again, in this order:
//
//   - verdict.json and verdict.md must be regular files, or the run is not judged: there is nothing to compare with.
//     One that cannot be read is not judged either.
//   - The conditions: the checkout's NotHeld, and a judge version the registry names for what gate.json records
//     (Recorded.Version): its gate/combine.sh sha256, or the internal/judge version bin/laug judge records. When one
//     fails, the run is not judged; the judge still runs, under Current for a judge the registry does not name, so
//     the long form can show what it gives.
//   - Judge under that version, in memory. A judge that writes no verdict (judge.Judge's error) is not judged.
//   - Both files byte for byte: identical. Else the difference is explained or not (explain).
func Rejudge(dir string, c Checkout) Answer {
	a := Answer{ID: filepath.Base(Resolve(dir))}
	notJudged := func(why string) Answer {
		a.Outcome, a.Why = NotJudged, why
		return a
	}
	for _, f := range []string{"verdict.json", "verdict.md"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || !fi.Mode().IsRegular() {
			return notJudged("no " + f + " in " + c.Shown(dir) + " to compare with")
		}
	}
	read := func(name string, b *[]byte) bool {
		var err error
		if *b, err = os.ReadFile(filepath.Join(dir, name)); err != nil {
			var pe *fs.PathError
			if errors.As(err, &pe) {
				err = pe.Err // the path is shown, not the absolute one
			}
			a.Why = name + " in " + c.Shown(dir) + " cannot be read: " + err.Error()
		}
		return err == nil
	}
	if !read("verdict.json", &a.Committed.JSON) || !read("verdict.md", &a.Committed.MD) {
		return notJudged(a.Why)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "gate.json")); err == nil {
		a.Recorded = ReadRecorded(data)
	}
	var conds []string
	if c.NotHeld != "" {
		conds = append(conds, c.NotHeld)
	}
	v, registered := a.Recorded.Version()
	if !registered {
		conds = append(conds, a.Recorded.Why())
	}
	if !registered {
		v, _ = judge.Named(judge.Current)
	}
	a.Version = v
	fsys := os.DirFS(dir)
	res, err := judge.Judge(fsys, v, judge.Options{})
	if err == nil {
		a.Rejudged = Files{res.JSON, res.MD}
	}
	switch {
	case len(conds) > 0:
		return notJudged(strings.Join(conds, "; "))
	case err != nil:
		return notJudged("judge " + v.Name + " writes no verdict for " + c.Shown(dir) + ": " + err.Error())
	case a.Committed.equal(a.Rejudged):
		a.Outcome = Identical
		return a
	}
	a.Outcome, a.Why = explain(dir, v, a.Committed, a.Rejudged)
	return a
}

func (f Files) equal(g Files) bool { return bytes.Equal(f.JSON, g.JSON) && bytes.Equal(f.MD, g.MD) }

// A Recorded is the judge a run's gate.json records, read from its bytes (ReadRecorded):
// the gate/combine.sh sha256 a run the shell judged records (judge.RecordedSHA256), and with none, the internal/judge
// entry a run bin/laug judge judged records, from preflight's bin/laug judge -identity -json (gate/lib.sh,
// judge_input).
type Recorded struct {
	// SHA256 is the recorded gate/combine.sh sha256, 64 lowercase hex, or "" for none.
	SHA256 string
	// Entry is, with no SHA256, the internal/judge entry's version, a name of letters, digits, ., _ and -, or ""
	// for none.
	Entry string
	// Sources is, with Entry, that entry's sha256, the identity of the judge sources that judged the run (as
	// judge.Self gives it), when it is 64 lowercase hex; "" for none.
	Sources string
}

// ReadRecorded reads the judge a gate.json text records. Each field is what
//
//	jq -r '[.inputs[]? | objects | select(.path == "<path>") | .<field> | strings][0] // empty' gate.json
//
// keeps, as judge.RecordedSHA256 reads the shell judge's sha256: the version only when no such sha256 is recorded,
// and only when it is one name, ^[A-Za-z0-9][A-Za-z0-9._-]*$ (isName).
func ReadRecorded(gateJSON []byte) Recorded {
	r := Recorded{SHA256: judge.RecordedSHA256(gateJSON)}
	if r.SHA256 != "" {
		return r
	}
	if v := recordedField(gateJSON, "internal/judge", "version"); isName(v) {
		r.Entry = v
		if s := recordedField(gateJSON, "internal/judge", "sha256"); isHex64(s) {
			r.Sources = s
		}
	}
	return r
}

// Version is the registry's version for what the run records: by its sha256 (judge.Lookup) or by its entry's name
// (judge.Named); ok is false when it names none.
func (r Recorded) Version() (v judge.Version, ok bool) {
	switch {
	case r.SHA256 != "":
		return judge.Lookup(r.SHA256)
	case r.Entry != "":
		return judge.Named(r.Entry)
	}
	return judge.Version{}, false
}

// Why says why the registry names no version for what the run records, in a clause; "" when it names one.
func (r Recorded) Why() string {
	if _, ok := r.Version(); ok {
		return ""
	}
	switch {
	case r.SHA256 != "":
		return "gate.json records gate/combine.sh sha256 " + r.SHA256[:12] + ", which internal/judge/versions.txt " +
			"does not name"
	case r.Entry != "":
		return "gate.json records internal/judge version " + r.Entry + ", which internal/judge/versions.txt does " +
			"not name"
	}
	return "gate.json records no gate/combine.sh sha256 and no internal/judge version, so no judge version is named"
}

// isHex64 says s is 64 lowercase hex digits, a sha256 as sha256sum prints it.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// recordedField is what jq -r '[.inputs[]? | objects | select(.path == path) | .<field> | strings][0] // empty'
// prints for a gate.json text, as bash's $(...) keeps it: each value of the stream prints the field of its first
// input object with that path whose field is a string; null prints nothing; any other value that is not an object,
// or a text jq cannot parse, is jq's error and gives "". NUL bytes and the newlines at the end are dropped.
func recordedField(gateJSON []byte, path, field string) string {
	vs, err := jsonx.ParseStream(gateJSON)
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, v := range vs {
		if v.IsNull() {
			continue
		}
		o, ok := v.Obj()
		if !ok {
			return ""
		}
		in, _ := o.Get("inputs")
		for _, e := range members(in) {
			eo, ok := e.Obj()
			if !ok {
				continue
			}
			if p, _ := eo.Get("path"); !jsonx.Equal(p, jsonx.Str(path)) {
				continue
			}
			if f, _ := eo.Get(field); f.Kind() == jsonx.KindString {
				s, _ := f.Str()
				out.WriteString(s + "\n") // [...][0] // empty: the first string, raw under -r
				break
			}
		}
	}
	return strings.TrimRight(strings.ReplaceAll(out.String(), "\x00", ""), "\n")
}

// explain names how a run whose rejudge differs differs. A stopped run is not explained by a line of its
// verdict.md, but re-rendered.
//
//   - A stopped run, whose verdict.json holds a stopped_at (stoppedAt): its verdict.json is the gate's trap verdict.
//     It is re-rendered by each of the trap's three forms from its own step and reason (judge.TrapArgsOf,
//     judge.RerenderTrap); the forms that give both files byte for byte are named, "trap T2", and with none it is
//     unexplained, even when its verdict.md names the step and ends in BLOCK.
//   - Else the GONE rule (Gone) names the items the run judged as an empty directory and the rejudge as none. Each
//     must be a name the run does not hold; judged again with those directories made in memory, the run must give
//     both files byte for byte. Then it is known, as "an empty item directory (<key>/), which git does not carry: a
//     fresh clone judges it as no directory".
//   - Anything else is unexplained.
func explain(dir string, v judge.Version, committed, rejudged Files) (Outcome, string) {
	if stoppedAt(committed.JSON) != "" {
		args, err := judge.TrapArgsOf(committed.JSON)
		if err != nil {
			return Unexplained, ""
		}
		var forms []string
		for _, f := range judge.Forms {
			js, md, err := judge.RerenderTrap(os.DirFS(dir), f, args)
			if err == nil && committed.equal(Files{js, md}) {
				forms = append(forms, string(f))
			}
		}
		if len(forms) == 0 {
			return Unexplained, ""
		}
		return Rerendered, "trap " + strings.Join(forms, " or ")
	}
	keys, ok := Gone(committed.JSON, rejudged.JSON)
	// $(jq -r ...) then `while read`: the keys' lines, less the trailing newlines $(...) drops.
	text := strings.TrimRight(strings.Join(keys, "\n"), "\n")
	if !ok || text == "" {
		return Unexplained, ""
	}
	lines := strings.Split(text, "\n")
	res, err := judge.Judge(os.DirFS(dir), v, judge.Options{EmptyDirs: lines})
	if err != nil || !committed.equal(Files{res.JSON, res.MD}) {
		return Unexplained, "" // a key that is no name, one the run holds, one twice, or the files still differ
	}
	for i, k := range lines {
		lines[i] = k + "/"
	}
	return Known, "an empty item directory (" + strings.Join(lines, ", ") + "), which git does not carry: a fresh " +
		"clone judges it as no directory"
}

// stoppedAt is what explain reads first: each value of verdict.json that is an object
// with a stopped_at that is not null gives that stopped_at as tostring does, one line each, less the trailing
// newlines $(...) drops; "" when none does or the file does not parse.
//
//	jq -r 'if type == "object" and .stopped_at != null then .stopped_at | tostring else empty end' verdict.json
func stoppedAt(verdictJSON []byte) string {
	vs, err := jsonx.ParseStream(verdictJSON)
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, v := range vs {
		o, ok := v.Obj()
		if !ok {
			continue
		}
		if st, _ := o.Get("stopped_at"); !st.IsNull() {
			out.WriteString(tostring(st) + "\n")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// tostring is jq's: a string as it is, anything else as its JSON text.
func tostring(v jsonx.Value) string {
	if s, ok := v.Str(); ok {
		return s
	}
	return v.String()
}

// Gone is the GONE rule over a run's verdict.json a and its rejudge's b, each read as jq's
// --slurpfile reads it, its first value: each item key of a, in order, for which a's R2 names a file of <key>/
// missing and not the directory, and b's names the directory missing and not a file. ok is false where jq stops:
// a file that does not parse, or an index of something that is not an object or null.
//
//	def dir_gone($k): . == "\($k): no JSON: there is no \($k)/ directory";
//	def file_gone($k): (startswith("\($k): no JSON: \($k)/") or startswith("\($k): no exit record: \($k)/"))
//	  and endswith(" is missing");
//	def r2($v): [$v.rules.R2.reasons[]? | strings];
//	def only_dir($v; $k): any(r2($v)[]; dir_gone($k)) and (any(r2($v)[]; file_gone($k)) | not);
//	def only_file($v; $k): any(r2($v)[]; file_gone($k)) and (any(r2($v)[]; dir_gone($k)) | not);
//	$a[0] as $a | $b[0] as $b | $a.items[]?.key | strings | select(only_file($a; .) and only_dir($b; .))
func Gone(a, b []byte) (keys []string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, isJQ := r.(jqError); !isJQ {
				panic(r)
			}
			keys, ok = nil, false
		}
	}()
	first := func(data []byte) jsonx.Value {
		vs, err := jsonx.ParseStream(data)
		if err != nil {
			panic(jqError{err.Error()})
		}
		if len(vs) == 0 {
			return jsonx.Null()
		}
		return vs[0]
	}
	va, vb := first(a), first(b)
	r2 := func(v jsonx.Value) []string {
		var out []string
		for _, r := range members(index(index(index(v, "rules"), "R2"), "reasons")) {
			if s, ok := r.Str(); ok {
				out = append(out, s)
			}
		}
		return out
	}
	dirGone := func(k string) func(string) bool {
		return func(s string) bool { return s == k+": no JSON: there is no "+k+"/ directory" }
	}
	fileGone := func(k string) func(string) bool {
		return func(s string) bool {
			return (strings.HasPrefix(s, k+": no JSON: "+k+"/") || strings.HasPrefix(s, k+": no exit record: "+k+"/")) &&
				strings.HasSuffix(s, " is missing")
		}
	}
	only := func(v jsonx.Value, yes, no func(string) bool) bool {
		return anyOf(r2(v), yes) && !anyOf(r2(v), no)
	}
	for _, it := range members(index(va, "items")) {
		k, isStr := index(it, "key").Str()
		if isStr && only(va, fileGone(k), dirGone(k)) && only(vb, dirGone(k), fileGone(k)) {
			keys = append(keys, k)
		}
	}
	return keys, true
}

func anyOf(ss []string, f func(string) bool) bool {
	for _, s := range ss {
		if f(s) {
			return true
		}
	}
	return false
}

// A jqError is an error jq raises, which stops its program: Gone and Signature recover it.
type jqError struct{ msg string }

// index is jq's .name on v: null gives null, an object its member or null, anything else an error.
func index(v jsonx.Value, name string) jsonx.Value {
	if v.IsNull() {
		return v
	}
	o, ok := v.Obj()
	if !ok {
		panic(jqError{fmt.Sprintf("Cannot index %s with string %q", v.Type(), name)})
	}
	m, _ := o.Get(name)
	return m
}

// members is .[]? on v: an array's elements, an object's values, else nothing.
func members(v jsonx.Value) []jsonx.Value {
	if a, ok := v.Arr(); ok {
		return a
	}
	o, ok := v.Obj()
	if !ok {
		return nil
	}
	vs := make([]jsonx.Value, o.Len())
	for i := range vs {
		_, vs[i] = o.Member(i)
	}
	return vs
}

// RejudgeAll judges again every run in results, as laug gate rejudge -all does: each entry that is
// a directory after symlinks, dot names skipped, by name in byte order (Runs), one Answer each. An error is a
// results directory that is not there, one that holds no run, or an entry Runs cannot list.
func RejudgeAll(results string, c Checkout) ([]Answer, error) {
	r := Resolve(results)
	if fi, err := os.Stat(r); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w at %s", ErrNoResults, c.Shown(r))
	}
	runs, err := Runs(os.DirFS(r))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Shown(r), err)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoRun, c.Shown(r))
	}
	answers := make([]Answer, len(runs))
	for i, run := range runs {
		answers[i] = Rejudge(filepath.Join(r, run.Gid), c)
	}
	return answers, nil
}

// The two ways RejudgeAll finds nothing to judge, each exit 2 in laug gate rejudge -all.
var (
	ErrNoResults = errors.New("no results directory")
	ErrNoRun     = errors.New("no gate run")
)

// Counts are answers counted by outcome.
type Counts map[Outcome]int

// Count counts answers by outcome.
func Count(answers []Answer) Counts {
	c := Counts{}
	for _, a := range answers {
		c[a.Outcome]++
	}
	return c
}

// String is the counts as the counts line gives them: "156 runs: 154 identical, 0 known, 2 re-rendered,
// 0 unexplained, 0 not judged".
func (c Counts) String() string {
	n := 0
	parts := make([]string, len(Outcomes))
	for i, o := range Outcomes {
		n += c[o]
		parts[i] = strconv.Itoa(c[o]) + " " + string(o)
	}
	return strconv.Itoa(n) + " runs: " + strings.Join(parts, ", ")
}

// Exit is laug gate rejudge -all's exit for these counts: 2 when a run is not judged, else 1 when one is
// unexplained, else 0.
func (c Counts) Exit() int {
	switch {
	case c[NotJudged] > 0:
		return 2
	case c[Unexplained] > 0:
		return 1
	}
	return 0
}

// FirstDiff names the first line where committed and rejudged differ: its number and each side's line, without its
// newline, "(the file ends)" for a side that has no such line, and "(no newline at the end)" after a last line that
// lacks one; n is 0 when the two are equal.
func FirstDiff(committed, rejudged []byte) (n int, want, got string) {
	w, g := strings.SplitAfter(string(committed), "\n"), strings.SplitAfter(string(rejudged), "\n")
	at := func(ls []string, i int) string {
		if i < len(ls) {
			return ls[i]
		}
		return ""
	}
	show := func(l string) string {
		if l == "" {
			return "(the file ends)"
		}
		if t, ok := strings.CutSuffix(l, "\n"); ok {
			return t
		}
		return l + " (no newline at the end)"
	}
	for i := range max(len(w), len(g)) {
		if wl, gl := at(w, i), at(g, i); wl != gl {
			return i + 1, show(wl), show(gl)
		}
	}
	return 0, "", ""
}
