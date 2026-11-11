package judge

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// A trap verdict is what the gate writes when it stops before its judge runs: trap_verdict, in gate/run.sh and later
// gate/lib.sh, not a judge version. It writes a BLOCK of class invalid that names the step the gate stopped at, from
// gate.json, clock.json and plan.json and from shell variables. 4 committed runs hold one. Three forms of it wrote
// trap verdicts, read in the earlier history of gate/run.sh and gate/lib.sh, which this tree does not carry; each
// starts at the commit named:
//
//   - T1, from e4540a8: 25 keys. tree_dirty is `$g.tree_dirty // null`, so a false one reads null. The reason names
//     gate/run.sh.
//   - T2, from 7edb11d: tree_dirty is gate.json's when it is a boolean, else null. From 06ffb0b the reason names the
//     driver, gate/run.sh or gate/step.sh; before it, gate/run.sh, the same text as the driver run.sh gives.
//   - T3, from 0097a57 to now: 27 keys, incumbent after server_tag and comparison null after items.
//
// verdict.md's program is the same in all three. A re-rendered run is never a rejudged one: the step and the why of a
// trap are recorded nowhere but in the trap verdict itself for 2 of the 4.

// A Form is one of trap_verdict's three forms.
type Form string

const (
	T1 Form = "T1"
	T2 Form = "T2"
	T3 Form = "T3"
)

// Forms are the three, oldest first.
var Forms = []Form{T1, T2, T3}

// TrapArgs are what trap_verdict takes from the shell, not from the run's files: its jq's --arg and --argjson values.
type TrapArgs struct {
	GateID    string      // $gate_id, as --arg id
	ServerTag string      // $SERVER_TAG, as --arg tag
	Deadline  jsonx.Value // ${DEADLINE_S:-null}, as --argjson deadline
	Step      string      // $step, the step the gate stopped at
	Why       string      // $why: a signal, or "the workflow was cancelled"
	Driver    string      // T2 and T3's "gate/${driver:-run.sh}", as --arg drv; T1's text names gate/run.sh
}

// ErrTrap is a trap verdict that cannot be re-rendered: a committed verdict.json that does not read as a trap's, a
// form that does not exist or cannot have written the args, or run files on which the shell's trap fails, so that
// it writes no verdict.
var ErrTrap = errors.New("no trap verdict")

// trapReason is the one reason every form writes, but for the driver: T1's text names gate/run.sh where the others
// interpolate $drv.
//
//	"stopped: \($drv) stopped at step \($step) (\($why)) before gate/combine.sh judged; nothing was judged"
func trapReason(drv, step, why string) string {
	return "stopped: " + drv + " stopped at step " + step + " (" + why + ") before gate/combine.sh judged; " +
		"nothing was judged"
}

// TrapArgsOf reads a committed trap verdict.json for the args its trap was called with: gate_id, server_tag and
// deadline_s as they are, which trap_verdict took from the shell (its --arg and --argjson), the step from stopped_at,
// and the driver and the why from reasons[0], read as trapReason writes it with that step.
func TrapArgsOf(verdictJSON []byte) (TrapArgs, error) {
	bad := func(why string) (TrapArgs, error) { return TrapArgs{}, fmt.Errorf("%w: %s", ErrTrap, why) }
	v, err := jsonx.Parse(verdictJSON)
	if err != nil {
		return bad(err.Error())
	}
	o, ok := v.Obj()
	if !ok {
		return bad("verdict.json is not an object")
	}
	get := func(k string) string {
		m, _ := o.Get(k)
		s, ok := m.Str()
		if !ok {
			err = fmt.Errorf("%w: %s is not a string", ErrTrap, k)
		}
		return s
	}
	a := TrapArgs{GateID: get("gate_id"), ServerTag: get("server_tag"), Step: get("stopped_at")}
	if err != nil {
		return TrapArgs{}, err
	}
	a.Deadline, _ = o.Get("deadline_s")
	rs, _ := o.Get("reasons")
	list, _ := rs.Arr()
	if len(list) != 1 {
		return bad("reasons does not hold one reason")
	}
	r, _ := list[0].Str()
	const tail = ") before gate/combine.sh judged; nothing was judged"
	mid := " stopped at step " + a.Step + " ("
	rest, ok := strings.CutPrefix(r, "stopped: ")
	drv, rest, ok2 := strings.Cut(rest, mid)
	why, ok3 := strings.CutSuffix(rest, tail)
	if !ok || !ok2 || !ok3 {
		return bad(fmt.Sprintf("reasons[0] %q does not name step %q as a trap does", r, a.Step))
	}
	a.Driver, a.Why = drv, why
	return a, nil
}

// RerenderTrap is what trap_verdict writes for the run in fsys under form f, called with args: verdict.json and
// verdict.md, byte for byte. It reads gate.json, clock.json and plan.json as the trap does, after the trap has
// stamped clock.json's t_post, which a committed run's clock.json holds already. Nothing is written.
//
// The trap reads each file with `jq -c .`: a file that is absent, cannot be read or does not parse is null. One
// that holds no value gives "", which --argjson refuses, so the trap fails; plan.json alone is checked for that, and
// is null. One that holds two values fails --argjson too. Then gate.json, clock.json or plan.json that is not an
// object, null or false cannot be indexed: jq's error. Each failure is an ErrTrap.
func RerenderTrap(fsys fs.FS, f Form, args TrapArgs) (verdictJSON, verdictMD []byte, err error) {
	switch f {
	case T1:
		if args.Driver != "gate/run.sh" {
			return nil, nil, fmt.Errorf("%w: T1's reason names gate/run.sh, not %q", ErrTrap, args.Driver)
		}
	case T2, T3:
	default:
		return nil, nil, fmt.Errorf("%w: no form %q", ErrTrap, f)
	}
	g, err := trapLoad(fsys, "gate.json", false)
	if err != nil {
		return nil, nil, err
	}
	c, err := trapLoad(fsys, "clock.json", false)
	if err != nil {
		return nil, nil, err
	}
	pl, err := trapLoad(fsys, "plan.json", true)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*jqError)
			if !ok {
				panic(r)
			}
			verdictJSON, verdictMD, err = nil, nil, fmt.Errorf("%w: %v", ErrTrap, e)
		}
	}()
	verdictJSON = jsonx.Marshal(trapVerdict(f, args, g, c, pl))
	return verdictJSON, trapMD(readBack(verdictJSON)), nil
}

// trapLoad is `x="$(jq -c . "$dir/<name>" 2>/dev/null)" || x=null` and what --argjson then makes of x: the one
// value, or null when jq fails. No value is "" and an error, unless emptyIsNull, as plan.json's `[[ -n "$pl" ]] ||
// pl=null` makes it; two values are an error.
func trapLoad(fsys fs.FS, name string, emptyIsNull bool) (jsonx.Value, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return null, nil
	}
	vs, err := jsonx.ParseStream(data)
	switch {
	case err != nil:
		return null, nil // jq prints what it read before the error, then exits 2: x=null
	case len(vs) == 0 && emptyIsNull:
		return null, nil
	case len(vs) != 1:
		return null, fmt.Errorf("%w: %s holds %d JSON values, which --argjson refuses", ErrTrap, name, len(vs))
	}
	return vs[0], nil
}

// trapVerdict is the trap's verdict object in form f, in its key order:
//
//	($g // {}) as $g | ($c // {}) as $c | ($pl // {}) as $pl
//	| {gate_id: $id, verdict: "BLOCK", class: "invalid", rerun: false, class_if_quiet: null,
//	   refused: ($g.refused // null), fake: ($g.fake == true), server_tag: $tag,
//	   incumbent: ($pl.incumbent // null) [T3], laug_commit: ($g.laug_commit // null), tree_dirty: [by form],
//	   base: ($g.base // null), inputs: ($g.inputs // null), deadline_s: $deadline,
//	   items_in_plan: ($pl.items_in_plan // null), items_limit: ($g.items_limit // null),
//	   t0: ($c.t0 // null), t_post: ($c.t_post // null),
//	   elapsed_s: (if ($c.t0.wall | type) == "number" and ($c.t_post.wall | type) == "number"
//	               then {wall: ((($c.t_post.wall - $c.t0.wall) * 1000 | round) / 1000),
//	                     mono: ((($c.t_post.mono - $c.t0.mono) * 1000 | round) / 1000)} else null end),
//	   host: ($g.host // null), rules: null, reasons: [the reason], items: [], comparison: null [T3],
//	   host_quiet: false, host_noise: ["the gate stopped before its verdict"], stopped_at: $step}
//
// elapsed_s checks the walls only, so a mono that is not a number is jq's error. Every index above can raise one.
func trapVerdict(f Form, a TrapArgs, g, c, pl jsonx.Value) jsonx.Value {
	g, c, pl = alt(g, object()), alt(c, object()), alt(pl, object())
	tree := field(g, "tree_dirty") // T2, T3: if ($g.tree_dirty | type) == "boolean" then $g.tree_dirty else null end
	if f == T1 {
		tree = alt(tree, null) // T1: $g.tree_dirty // null
	} else if !typeIs(tree, "boolean") {
		tree = null
	}
	elapsed := null
	if typeIs(path(c, "t0", "wall"), "number") && typeIs(path(c, "t_post", "wall"), "number") {
		elapsed = object(
			"wall", r3(minus(path(c, "t_post", "wall"), path(c, "t0", "wall"))),
			"mono", r3(minus(path(c, "t_post", "mono"), path(c, "t0", "mono"))),
		)
	}
	drv := a.Driver // T1 names gate/run.sh, which RerenderTrap holds Driver to
	out := []any{
		"gate_id", str(a.GateID), "verdict", str("BLOCK"), "class", str("invalid"), "rerun", jsonx.Bool(false),
		"class_if_quiet", null, "refused", alt(field(g, "refused"), null),
		"fake", jsonx.Bool(eq(field(g, "fake"), jsonx.Bool(true))), "server_tag", str(a.ServerTag),
	}
	if f == T3 {
		out = append(out, "incumbent", alt(field(pl, "incumbent"), null))
	}
	out = append(out, "laug_commit", alt(field(g, "laug_commit"), null), "tree_dirty", tree,
		"base", alt(field(g, "base"), null), "inputs", alt(field(g, "inputs"), null), "deadline_s", a.Deadline,
		"items_in_plan", alt(field(pl, "items_in_plan"), null), "items_limit", alt(field(g, "items_limit"), null),
		"t0", alt(field(c, "t0"), null), "t_post", alt(field(c, "t_post"), null), "elapsed_s", elapsed,
		"host", alt(field(g, "host"), null), "rules", null, "reasons", strs([]string{trapReason(drv, a.Step, a.Why)}),
		"items", jsonx.Arr(nil))
	if f == T3 {
		out = append(out, "comparison", null)
	}
	return object(append(out, "host_quiet", jsonx.Bool(false),
		"host_noise", strs([]string{"the gate stopped before its verdict"}), "stopped_at", str(a.Step))...)
}

// trapMD is the trap's verdict.md, `jq -r` over the verdict.json it wrote, the same program in every form:
//
//	"## laug · verdict for raun \(.laug_commit // "unknown")\(if .tree_dirty == true then " (tree dirty)" else "" end)
//	 \(if .fake then " (GATE_FAKE=1: fixtures, no cell ran)" else "" end)", "",
//	"Gate run \(.gate_id) on server \(.server_tag) stopped at step \(.stopped_at) before gate/combine.sh judged it:
//	 \(.reasons[0]).", "",
//	"Class: \(.class).", "", "**BLOCK**"
//
// (each quoted string is one line; it is cut here to fit). v is what the trap wrote, read back.
func trapMD(v jsonx.Value) []byte {
	var w lines
	h := "## laug · verdict for raun " + tostring(alt(field(v, "laug_commit"), str("unknown")))
	if eq(field(v, "tree_dirty"), jsonx.Bool(true)) {
		h += " (tree dirty)"
	}
	if truthy(field(v, "fake")) {
		h += " (GATE_FAKE=1: fixtures, no cell ran)"
	}
	w.line(h)
	w.line("")
	w.line("Gate run " + tostring(field(v, "gate_id")) + " on server " + tostring(field(v, "server_tag")) +
		" stopped at step " + tostring(field(v, "stopped_at")) + " before gate/combine.sh judged it: " +
		tostring(index(field(v, "reasons"), zero)) + ".")
	w.line("")
	w.line("Class: " + tostring(field(v, "class")) + ".")
	w.line("")
	w.line("**BLOCK**")
	return w.b
}
