package rejudge

// S2, signature, in Go: two runs of the same case have equal signatures. Signature reads a run's, as laug gate
// signature prints it, and Compare and CompareLine compare two, as laug gate compare does. Its judge field, the judge
// version's name, was combine_sha256, the sha256, until the gate judged in Go.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
)

// A SignatureError is a run whose signature cannot be read, exit 2. Its text names the run and the file or the
// check that failed, or for a jq error, the run and jq's own message.
type SignatureError struct{ msg string }

func (e *SignatureError) Error() string { return e.msg }

// Signature is S2's signature of the run in dir: what laug gate signature prints for it.
// verdict.json and timeline.jsonl must be regular files that parse as JSON streams; then SIG reads verdict.json's
// one object and timeline.jsonl's lines:
//
//   - verdict, class, rerun, class_if_quiet, stopped_at, fake, server_tag and incumbent as verdict.json holds them,
//     and refused as whether it is not null;
//   - judge (Judge), the judge version's name;
//   - set_sha256, load/steady-state.yaml's sha256 in inputs, null with no comparison;
//   - failed_rules, the rules whose pass is not true, sorted; [] for a trap verdict, whose rules are null;
//   - items, in plan order, each with key, kind, arm, expect, observed, class, exit, stopped_by and block;
//   - comparison, its mode and each row's name, judged and result; null with none;
//   - marks, the timeline's steps that are no event, in order, and events, the rest, sorted and each once.
//
// An error is a *SignatureError with the reason, root being the repository root the path is shown
// against.
func Signature(dir, root string) (sig jsonx.Value, err error) {
	c := Checkout{Root: root}
	shown := c.Shown(dir)
	var v, t []jsonx.Value
	for _, f := range []struct {
		name string
		vs   *[]jsonx.Value
	}{{"verdict.json", &v}, {"timeline.jsonl", &t}} {
		p := filepath.Join(dir, f.name)
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return jsonx.Value{}, &SignatureError{"no " + f.name + " in " + shown}
		}
		data, err := os.ReadFile(p)
		if err == nil {
			*f.vs, err = jsonx.ParseStream(data)
		}
		if err != nil {
			return jsonx.Value{}, &SignatureError{f.name + " in " + shown + " does not parse"}
		}
	}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(jqError)
			if !ok {
				panic(r)
			}
			sig, err = jsonx.Value{}, &SignatureError{"the signature of " + shown + " failed: " + e.msg}
		}
	}()
	if why := sigCheck(v, t); why != "" {
		return jsonx.Value{}, &SignatureError{shown + ": " + why}
	}
	return sigOf(v[0], t), nil
}

// sigCheck is SIG's guards, in order: the err text of the first that fails, or "".
//
//	if ($v | length) != 1 or ($v[0] | type) != "object" then ...
//	elif ($v[0].verdict != "ADVANCE" and $v[0].verdict != "BLOCK") or ($v[0].items | type) != "array" then ...
//	elif $v[0].rules != null and ($v[0].rules | type) != "object" then ...
//	elif $v[0].rules == null and $v[0].stopped_at == null then ...
//	elif any($v[0].items[]; type != "object") then ...
//	elif any($t[]; type != "object" or (.step | type) != "string") then ...
//	elif ($t | length) == 0 or $t[0].step != "start" then ...
func sigCheck(v, t []jsonx.Value) string {
	if len(v) != 1 || v[0].Kind() != jsonx.KindObject {
		return "verdict.json is not one JSON object"
	}
	o, _ := v[0].Obj()
	get := func(k string) jsonx.Value { m, _ := o.Get(k); return m }
	verdict := get("verdict")
	if !jsonx.Equal(verdict, jsonx.Str("ADVANCE")) && !jsonx.Equal(verdict, jsonx.Str("BLOCK")) ||
		get("items").Kind() != jsonx.KindArray {
		return "verdict.json holds no gate verdict: no verdict ADVANCE or BLOCK, or no items"
	}
	rules := get("rules")
	if !rules.IsNull() && rules.Kind() != jsonx.KindObject {
		return "verdict.json: rules is neither null nor an object"
	}
	if rules.IsNull() && get("stopped_at").IsNull() {
		return "verdict.json: rules is null, and stopped_at is null: only a trap verdict has no rules"
	}
	items, _ := get("items").Arr()
	for _, it := range items {
		if it.Kind() != jsonx.KindObject {
			return "verdict.json: an item is not an object"
		}
	}
	for _, line := range t {
		if line.Kind() != jsonx.KindObject || index(line, "step").Kind() != jsonx.KindString {
			return "timeline.jsonl: a line names no step"
		}
	}
	if len(t) == 0 || !jsonx.Equal(index(t[0], "step"), jsonx.Str("start")) {
		return "timeline.jsonl does not begin with the mark start"
	}
	return ""
}

// sigFields are the signature's fields, in its order: SIG's object.
var sigFields = []string{"verdict", "class", "rerun", "class_if_quiet", "stopped_at", "refused", "fake", "server_tag",
	"incumbent", "judge", "set_sha256", "failed_rules", "items", "comparison", "marks", "events"}

// sigOf is SIG's object for verdict.json's one object v and the timeline's lines t, both past sigCheck. jq builds
// it field by field in this order, so of two fields that raise an error, the first one's is the error.
func sigOf(v jsonx.Value, t []jsonx.Value) jsonx.Value {
	get := func(k string) jsonx.Value { return index(v, k) }
	sig := jsonx.NewObject()
	for _, k := range []string{"verdict", "class", "rerun", "class_if_quiet", "stopped_at"} {
		sig.Set(k, get(k))
	}
	sig.Set("refused", jsonx.Bool(!get("refused").IsNull()))
	for _, k := range []string{"fake", "server_tag", "incumbent"} {
		sig.Set(k, get(k))
	}
	sig.Set("judge", Judge(get("inputs")))
	// set_sha256: (if $v.comparison == null then null else ([$v.inputs[]? | objects
	//   | select(.path == "load/steady-state.yaml") | .sha256][0] // null) end)
	if get("comparison").IsNull() {
		sig.Set("set_sha256", jsonx.Null())
	} else {
		sig.Set("set_sha256", inputField(get("inputs"), "load/steady-state.yaml", "sha256"))
	}
	// failed_rules: (if $v.rules == null then [] else [$v.rules | to_entries[] | select(.value.pass != true)
	//   | .key] | sort end)
	failed := []jsonx.Value{}
	if rules := get("rules"); !rules.IsNull() {
		ro, _ := rules.Obj()
		for i := range ro.Len() {
			k, val := ro.Member(i)
			if !jsonx.Equal(index(val, "pass"), jsonx.Bool(true)) {
				failed = append(failed, jsonx.Str(k))
			}
		}
		jsonx.Sort(failed)
	}
	sig.Set("failed_rules", jsonx.Arr(failed))
	// items: [$v.items[] | {key, kind, arm, expect, observed, class, exit, stopped_by, block}]
	list, _ := get("items").Arr()
	items := []jsonx.Value{}
	for _, it := range list {
		items = append(items, pick(it, "key", "kind", "arm", "expect", "observed", "class", "exit", "stopped_by",
			"block"))
	}
	sig.Set("items", jsonx.Arr(items))
	// comparison: (if $v.comparison == null then null else {mode: $v.comparison.mode, rows: [$v.comparison.rows[]?
	//   | {name, judged, result}]} end)
	if c := get("comparison"); c.IsNull() {
		sig.Set("comparison", jsonx.Null())
	} else {
		cmp := jsonx.NewObject()
		cmp.Set("mode", index(c, "mode"))
		rows := []jsonx.Value{}
		for _, r := range members(index(c, "rows")) {
			rows = append(rows, pick(r, "name", "judged", "result"))
		}
		cmp.Set("rows", jsonx.Arr(rows))
		sig.Set("comparison", jsonx.Obj(cmp))
	}
	// marks: [$steps[] | select(event | not)], events: ([$steps[] | select(event)] | unique)
	marks, events := []jsonx.Value{}, []jsonx.Value{}
	for _, line := range t {
		step := index(line, "step")
		s, _ := step.Str()
		if event(s) {
			events = append(events, step)
		} else {
			marks = append(marks, step)
		}
	}
	sig.Set("marks", jsonx.Arr(marks))
	sig.Set("events", jsonx.Arr(unique(events)))
	return jsonx.Obj(sig)
}

// pick is jq's {a, b, ...} on v: each name and v's member, null when v lacks it; an error unless v is an object or
// null.
func pick(v jsonx.Value, names ...string) jsonx.Value {
	o := jsonx.NewObject()
	for _, n := range names {
		o.Set(n, index(v, n))
	}
	return jsonx.Obj(o)
}

// event is SIG's `def event: test("^(leased |ended |watchdog |laugd stopped$|done$)")` on a step. Oniguruma's $
// also matches before a final newline, and only there (seen on jq-1.7: "done\n" is an event, "done\n\n" and
// "done\r" are not).
func event(s string) bool {
	for _, p := range []string{"leased ", "ended ", "watchdog "} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	switch s {
	case "laugd stopped", "laugd stopped\n", "done", "done\n":
		return true
	}
	return false
}

// unique is jq's: sorted in its order, each value once.
func unique(vs []jsonx.Value) []jsonx.Value {
	jsonx.Sort(vs)
	out := []jsonx.Value{}
	for i, v := range vs {
		if i == 0 || jsonx.Compare(vs[i-1], v) != 0 {
			out = append(out, v)
		}
	}
	return out
}

// inputField is ([$inputs[]? | objects | select(.path == path) | .<field>][0] // null): the field of the first
// object in inputs whose path is path, null when there is none or it is null or false.
func inputField(inputs jsonx.Value, path, field string) jsonx.Value {
	for _, e := range members(inputs) {
		if e.Kind() != jsonx.KindObject || !jsonx.Equal(index(e, "path"), jsonx.Str(path)) {
			continue
		}
		f := index(e, field)
		if b, ok := f.Bool(); f.IsNull() || ok && !b {
			return jsonx.Null()
		}
		return f
	}
	return jsonx.Null()
}

// Judge is S2's judge field for a run's inputs, where S2 had combine_sha256,
// the gate/combine.sh sha256 they record, before the gate judged in Go. A sha256 that internal/judge/versions.txt
// names gives that version's name, so a run judged by the shell's copy of a version and one judged by the Go judge
// under it compare equal; another string gives "sha256:<it>", so two judges the registry does not name never compare
// equal to a named one. With no gate/combine.sh sha256, an internal/judge entry's version, which a run judged by
// `laug judge` records, gives the field; null for neither. A recorded value that is not a string stays as it is.
func Judge(inputs jsonx.Value) jsonx.Value {
	c := inputField(inputs, "gate/combine.sh", "sha256")
	if s, ok := c.Str(); ok {
		if v, ok := judge.Lookup(s); ok {
			return jsonx.Str(v.Name)
		}
		return jsonx.Str("sha256:" + s)
	}
	if !c.IsNull() {
		return c
	}
	return inputField(inputs, "internal/judge", "version")
}

// Compare is S2 of two signatures: one line for each of a's fields whose value b does
// not equal, in a's order, "<field>: A <a's value> B <b's value>", each value as tojson writes it.
func Compare(a, b jsonx.Value) []string {
	ao, _ := a.Obj()
	var lines []string
	for i := range ao.Len() {
		k, av := ao.Member(i)
		bv := jsonx.Null()
		if bo, ok := b.Obj(); ok {
			bv, _ = bo.Get(k)
		}
		if !jsonx.Equal(av, bv) {
			lines = append(lines, k+": A "+av.String()+" B "+bv.String())
		}
	}
	return lines
}

// CompareLine is S2's first line for runs named a and b, given the lines Compare gave and the signature's field
// count: "laug · S2, A <a>, B <b>: equal", or "... : <n> of <fields> fields differ".
func CompareLine(a, b string, diffs []string, fields int) string {
	head := "laug · S2, A " + a + ", B " + b + ": "
	if len(diffs) == 0 {
		return head + "equal"
	}
	return head + strconv.Itoa(len(diffs)) + " of " + strconv.Itoa(fields) + " fields differ"
}
