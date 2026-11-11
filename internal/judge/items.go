package judge

import (
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// itemsOf is $items: one record per planned item, in plan order, its reasons by rule kept under _r for
// per() and dropped from verdict.json.
func (j *judging) itemsOf() []jsonx.Value {
	items := make([]jsonx.Value, 0, len(j.planned))
	for _, p := range j.planned { // $planned[] | . as $p
		items = append(items, j.item(p))
	}
	return items
}

// item is one planned item's record, the plan item p an object whose key is a string.
func (j *judging) item(p jsonx.Value) jsonx.Value {
	keyV := field(p, "key") // .key as $k
	k, _ := keyV.Str()
	d := alt(index(j.dirs, keyV), null)              // ($dirs[$k] // null) as $d
	runID := tostring(alt(j.gid, str(""))) + "-" + k // "\($gid // "")-\($k)" as $run_id
	runPath := k + "/" + runID + ".json"             // "\($k)/\($run_id).json" as $run_path
	vjPath := k + "/" + runID + ".verdict.json"      // "\($k)/\($run_id).verdict.json" as $vj_path
	run := obj(field(d, "run"))                      // ($d.run | obj) as $run
	vj := obj(field(d, "verdict"))                   // ($d.verdict | obj) as $vj
	ex := obj(field(d, "exit"))                      // ($d.exit | obj) as $ex
	lease := obj(field(d, "lease"))                  // ($d.lease | obj) as $lease
	exit := num(field(ex, "exit"))                   // ($ex.exit | num) as $exit
	stopped := alt(field(ex, "stopped_by"), null)    // ($ex.stopped_by // null) as $stopped
	isLoad := is(field(p, "kind"), "load")           // ($p.kind == "load") as $is_load
	judged, observed := run, null                    // $judged
	if isLoad {
		judged = vj
		observed = field(vj, "verdict") // $observed
	} else {
		observed = field(run, "result")
	}
	ended := j.endedOf(ex)                                                   // $ended
	r2 := j.r2Of(k, d, ex, run, vj, stopped, ended, isLoad, runPath, vjPath) // $r2
	script := "faults/run.sh"                                                // $script
	if isLoad {
		script = "load/verdict.sh"
	}
	r3 := r3Of(exit, observed, vj, isLoad, script, vjPath) // $r3
	own := ownOf(run, vj, isLoad)                          // $own
	if j.f.Arm && is(field(p, "arm"), "incumbent") && !is(own, "") {
		own = plus(str("incumbent: "), own) // the incumbent's own failure: $own; v1 has no such prefix
	}
	ownClass := j.reasonClass(own) // ($own | reason_class) as $own_class
	expect := field(p, "expect")
	r4 := r4Of(observed, expect, ownClass) // $r4
	ser := null                            // ($run.during.service_error_rate? // null) as $ser
	if v, ok := indexOpt(field(run, "during"), str("service_error_rate")); ok {
		ser = alt(v, null)
	}
	r5 := j.r5Of(ser, isLoad) // $r5
	// v1 holds every item to SERVER_TAG and thresholds.json, as v2 holds an item not of arm incumbent.
	isArm := j.f.Arm && is(field(p, "arm"), "incumbent")        // ($p.arm == "incumbent") as $is_arm
	id := j.idOf(isArm)                                         // $id
	r6 := r6Of(id, isArm, isLoad, run, judged, runPath, vjPath) // $r6
	// v1 has no R7: its reason chain follows its R6.
	var r7 []string
	if j.f.Arm {
		r7 = stringsOf(alt(index(j.r7ByKey, keyV), jsonx.Arr(nil))) // ($r7_by_key[$k] // []) as $r7
	}
	// The one reason, $reason. v1's chain has no R7, and an empty r7 changes nothing.
	reason := reasonOf(r2, r3, r5, r6, r7, run, own, ownClass, observed, expect)

	class := null
	if !is(reason, "") {
		class = str(j.reasonClass(reason))
	}
	var failed []string
	for _, r := range []struct {
		name    string
		reasons []string
	}{{"R2", r2}, {"R3", r3}, {"R4", r4}, {"R5", r5}, {"R6", r6}, {"R7", r7}} {
		if len(r.reasons) > 0 {
			failed = append(failed, r.name)
		}
	}
	cell, poolBusy, evidence := field(run, "cell"), alt(field(run, "pool_busy"), null), []string{runPath}
	if isLoad {
		cell, poolBusy, evidence = null, null, []string{runPath, vjPath}
	}
	r4own := make([]jsonx.Value, len(r4)) // [$r4[] | . + (if $own != "" then ": " + $own else "" end)]
	for i, r := range r4 {
		tail := str("")
		if !is(own, "") {
			tail = plus(str(": "), own) // a non-string $own cannot be added: jq's error
		}
		r4own[i] = plus(str(r), tail)
	}
	// The record. v1's has no arm, and no R7 in _r.
	rec := []any{"key", keyV, "kind", field(p, "kind")}
	if j.f.Arm {
		rec = append(rec, "arm", alt(field(p, "arm"), null))
	}
	rec = append(rec, "expect", expect,
		"observed", observed, "class", class, "reason", reason, "failed", strs(failed),
		"run_id", str(runID), "cell", alt(alt(field(lease, "id"), cell), null),
		"block", alt(field(lease, "block"), null), "exit", exit, "stopped_by", stopped, "pool_busy", poolBusy,
		"evidence", strs(evidence))
	rs := []any{"R2", strs(r2), "R3", strs(r3), "R4", jsonx.Arr(r4own), "R5", strs(r5), "R6", strs(r6)}
	if j.f.Arm {
		rs = append(rs, "R7", strs(r7))
	}
	return object(append(rec, "_r", object(rs...))...)
}

// endedOf is $ended: exit.json's end.wall less clock.json's t0.wall, when both are numbers. An end, or a t0
// once end.wall is a number, that is not an object or null cannot be indexed: jq's error.
func (j *judging) endedOf(ex jsonx.Value) jsonx.Value {
	if typeIs(path(ex, "end", "wall"), "number") && typeIs(path(j.clock, "t0", "wall"), "number") {
		return minus(path(ex, "end", "wall"), path(j.clock, "t0", "wall"))
	}
	return null
}

// r2Of is $r2, R2 per item: the deadline, no end time, no directory, no exit record, no run JSON, no
// verdict JSON, a partial JSON.
func (j *judging) r2Of(k string, d, ex, run, vj, stopped, ended jsonx.Value, isLoad bool,
	runPath, vjPath string) []string {
	var r2 []string
	deadline := field(j.plan, "deadline_s")
	switch {
	case !stopped.IsNull():
		r2 = append(r2, "deadline: the watchdog stopped it ("+tostring(stopped)+")")
	case !ended.IsNull() && typeIs(deadline, "number") && more(ended, deadline):
		r2 = append(r2, "deadline: it ended "+tostring(r3(ended))+" s after the gate start, past the deadline at "+
			tostring(deadline)+" s")
	}
	if !d.IsNull() && !ex.IsNull() && stopped.IsNull() && ended.IsNull() {
		why := k + "/exit.json has no end.wall"
		if !typeIs(path(j.clock, "t0", "wall"), "number") {
			why = "clock.json has no t0.wall"
			if j.clock.IsNull() {
				why = missing("clock.json", field(j.in, "clock"))
			}
		}
		r2 = append(r2, "no end time: cannot tell whether "+k+" ended before the deadline: "+why)
	}
	if d.IsNull() {
		r2 = append(r2, "no JSON: there is no "+k+"/ directory")
	}
	if !d.IsNull() && ex.IsNull() {
		r2 = append(r2, "no exit record: "+missing(k+"/exit.json", field(d, "exit")))
	}
	if !d.IsNull() && run.IsNull() {
		r2 = append(r2, "no JSON: "+missing(runPath, field(d, "run")))
	}
	if !d.IsNull() && isLoad && vj.IsNull() {
		r2 = append(r2, "no JSON: "+missing(vjPath, field(d, "verdict")))
	}
	if partial(run) {
		r2 = append(r2, "partial JSON: "+runPath)
	}
	return r2
}

// partial is `($run.partial // false) == true`.
func partial(run jsonx.Value) bool {
	return eq(alt(field(run, "partial"), jsonx.Bool(false)), jsonx.Bool(true))
}

// r3Of is $r3, R3 contract: the script's exit code, with the result it gave, and a load verdict's own
// exit and short window.
func r3Of(exit, observed, vj jsonx.Value, isLoad bool, script, vjPath string) []string {
	if exit.IsNull() {
		return []string{"contract: no exit code was recorded"}
	}
	var r3 []string
	switch {
	case eq(exit, two) && isLoad:
		r3 = append(r3, "contract: "+script+" exited 2: no reading")
	case eq(exit, three) && isLoad:
		r3 = append(r3, "contract: "+script+" exited 3: the window is shorter than the thresholds trust")
	case eq(exit, two):
		r3 = append(r3, "contract: "+script+" exited 2: a refusal or a harness failure")
	case !eq(exit, zero) && !eq(exit, one):
		r3 = append(r3, "contract: "+script+" exited "+tostring(exit))
	}
	if eq(exit, zero) && !is(observed, "ADVANCE") || eq(exit, one) && !is(observed, "BLOCK") {
		what := "result"
		if isLoad {
			what = "verdict"
		}
		r3 = append(r3, "contract: "+script+" exited "+tostring(exit)+" with "+what+" "+
			tostring(alt(observed, str("null"))))
	}
	if isLoad && !vj.IsNull() && !eq(field(vj, "exit"), exit) {
		r3 = append(r3, "contract: "+vjPath+" says exit "+tojson(field(vj, "exit"))+", and the process exited "+
			tostring(exit))
	}
	if isLoad && !vj.IsNull() && !field(vj, "short_window").IsNull() {
		r3 = append(r3, "contract: "+script+" read a window under "+tostring(field(vj, "short_window")))
	}
	return r3
}

// ownOf is $own, the item's own reason as it wrote it: a raun's reason, or a load verdict's deciding
// rows. Every row of a load verdict is read first, so one that is not an object or null is jq's error.
func ownOf(run, vj jsonx.Value, isLoad bool) jsonx.Value {
	if !isLoad {
		return alt(field(run, "reason"), str(""))
	}
	var dec []jsonx.Value // [$vj.rows[]? | select(.blocks == true)] as $dec
	for _, r := range eachOpt(field(vj, "rows")) {
		if eq(field(r, "blocks"), jsonx.Bool(true)) {
			dec = append(dec, r)
		}
	}
	switch {
	case !is(field(vj, "verdict"), "BLOCK"):
		return str("")
	case len(dec) > 0 && is(field(dec[0], "metric"), "workflows_not_completed") &&
		test(str(tostring(field(dec[0], "result"))), sweepFailed):
		return str("sweep failed: " + tostring(field(dec[0], "result")))
	case len(dec) == 0:
		return str("load verdict BLOCK: no row decided")
	}
	rows := make([]string, len(dec))
	for i, r := range dec {
		rows[i] = tostring(field(r, "metric")) + " " + tostring(alt(field(r, "value"), str("null"))) + " " +
			tostring(field(r, "result"))
	}
	return str("load verdict BLOCK: " + strings.Join(rows, ", "))
}

// r4Of is $r4, R4 outcome: what the item observed is what the plan expects, and an expected BLOCK is a
// regression the item saw.
func r4Of(observed, expect jsonx.Value, ownClass string) []string {
	switch {
	case eq(observed, expect) && (!is(expect, "BLOCK") || ownClass == "regression"):
		return nil
	case is(observed, "BLOCK") && is(expect, "BLOCK"):
		return []string{"expected BLOCK from a regression, observed BLOCK of class " + ownClass}
	}
	return []string{"expected " + tostring(expect) + ", observed " + tostring(alt(observed, str("nothing")))}
}

// r5Of is $r5, R5 per fault item: its during window's service_error_rate is above the set's max.
func (j *judging) r5Of(ser jsonx.Value, isLoad bool) []string {
	switch {
	case isLoad || !typeIs(j.ctlMax, "number"):
		return nil
	case !typeIs(ser, "number"):
		return []string{"control: service_error_rate read " + tostring(alt(ser, str("null"))) +
			" in the during window, not above its max " + tostring(j.ctlMax)}
	case more(ser, j.ctlMax):
		return nil
	}
	return []string{"control: service_error_rate " + tostring(ser) + " in the during window, not above its max " +
		tostring(j.ctlMax)}
}

// An itemID is $id: the release an item is held to, and its set.
type itemID struct {
	tag  jsonx.Value
	of   string
	rel  release
	file string
}

func (j *judging) idOf(isArm bool) itemID {
	if isArm {
		return itemID{j.inc, "the plan names incumbent", j.thiRel, "thresholds-incumbent.json"}
	}
	return itemID{j.tag, "the gate SERVER_TAG is", j.thRel, "thresholds.json"}
}

// r6Of is $r6, R6 per item: the release the cell said is the item's, and the item recorded the set its
// release names.
func r6Of(id itemID, isArm, isLoad bool, run, judged jsonx.Value, runPath, vjPath string) []string {
	var r6 []string
	switch {
	case !typeIs(id.tag, "string"):
		if isArm {
			r6 = append(r6, "identity: the item says arm incumbent, and the plan names no incumbent")
		}
	case isLoad:
		if v := field(run, "server_version"); !eq(v, id.tag) {
			r6 = append(r6, "identity: the cell said server_version "+tostring(alt(v, str("null")))+", and "+id.of+
				" "+tostring(id.tag))
		}
	default:
		if v := field(run, "server_tag"); !eq(v, id.tag) {
			r6 = append(r6, "identity: server_tag "+tostring(alt(v, str("null")))+", and "+id.of+" "+tostring(id.tag))
		}
	}
	switch {
	case !id.rel.ok:
	case !typeIs(judged, "object") || !hasKey(judged, "thresholds_release"):
		p := runPath
		if isLoad {
			p = vjPath
		}
		r6 = append(r6, "identity: "+p+" records no thresholds_release")
	case !eq(field(judged, "thresholds_release"), id.rel.v):
		r6 = append(r6, "identity: thresholds_release "+tostring(alt(field(judged, "thresholds_release"), str("null")))+
			", and "+id.file+" names "+tostring(alt(id.rel.v, str("null"))))
	}
	return r6
}

// reasonOf is $reason, the item's one reason, the first of: a deadline; any other R2 reason, with its own
// after "; " when the JSON is partial; its own BLOCK when that is not an expected regression; R3; an ADVANCE where
// a BLOCK was expected; R5; R6; R7. A non-string own reason goes through as it is, or cannot be added: jq's error.
func reasonOf(r2, r3, r5, r6, r7 []string, run, own jsonx.Value, ownClass string, observed,
	expect jsonx.Value) jsonx.Value {
	switch {
	case startswith(str(first(r2)), "deadline: "): // ($r2[0] // "") | startswith("deadline: ")
		return str(r2[0])
	case len(r2) > 0:
		tail := str("")
		if partial(run) && !is(own, "") {
			tail = plus(str("; "), own)
		}
		return plus(str(r2[0]), tail)
	case is(observed, "BLOCK") && (!is(expect, "BLOCK") || ownClass != "regression"):
		if is(own, "") {
			return str("BLOCKed with no reason recorded")
		}
		return own
	case len(r3) > 0:
		return str(r3[0])
	case is(observed, "ADVANCE") && is(expect, "BLOCK"):
		return str("control: expected BLOCK, observed ADVANCE")
	case len(r5) > 0:
		return str(r5[0])
	case len(r6) > 0:
		return str(r6[0])
	case len(r7) > 0:
		return str(r7[0])
	}
	return str("")
}

// first is `$r2[0] // ""` for a list of reasons, which holds no empty one.
func first(rs []string) string {
	if len(rs) == 0 {
		return ""
	}
	return rs[0]
}

// stringsOf is an array of strings as Go strings: R7's reasons, which JUDGE made strings.
func stringsOf(v jsonx.Value) []string {
	var out []string
	for _, e := range each(v) {
		out = append(out, tostring(e))
	}
	return out
}
