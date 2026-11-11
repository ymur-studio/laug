package judge

import (
	"github.com/ymur-studio/laug/internal/jsonx"
)

// Summary is the terminal summary: SUMMARY run on v as `jq -r "$SUMMARY"` runs it on verdict.json once written, v being
// that file read back. It has no DEFS. The first line names the gate, the server and the verdict; then come the
// refusal, one line a rule, and the host, each label padded to 17 characters. Stdout then adds two lines: the
// "written" line, padded the same, which names the two files, and the verdict alone.
//
// An error is one jq raises. The shell judge ran SUMMARY after both files were in place, so it would exit 5 with
// the files written. No verdict Evaluate builds raises one: what SUMMARY indexes or joins, the rules and host_noise,
// Evaluate makes itself, and everything else it only interpolates.
func Summary(v jsonx.Value) (out []byte, err error) {
	defer catch(&err)
	var w lines
	w.summary(v)
	return w.b, nil
}

func (w *lines) summary(v jsonx.Value) {
	// The first line: the gate, the server, the verdict, its class, a rerun and a fake gate.
	s := "laug · gate " + tostring(alt(field(v, "gate_id"), str("unknown"))) + ", server " +
		tostring(alt(field(v, "server_tag"), str("unknown"))) + ": " + tostring(field(v, "verdict"))
	if class := field(v, "class"); !class.IsNull() {
		s += ", class " + tostring(class)
	}
	if truthy(field(v, "rerun")) {
		s += " (rerun: the host was not quiet)"
	}
	if eq(field(v, "fake"), jsonx.Bool(true)) {
		s += " (GATE_FAKE=1: fixtures, no cell ran)"
	}
	w.line(s)

	// The refusal, when there is one.
	if refused := field(v, "refused"); !refused.IsNull() {
		w.line("refused          " + tostring(refused))
	}

	// Each rule, "R<n> <name>" and 17 spaces cut to 17 code points, then pass or its reasons.
	//
	//	"\("\(.key) \(.value.name)                 " | .[:17])\(if .value.pass then "pass" else (...) end)"
	for _, e := range toEntries(field(v, "rules")) {
		label := tostring(e.key) + " " + tostring(field(e.value, "name")) + "                 "
		result := "pass"
		if !truthy(field(e.value, "pass")) {
			result = join(field(e.value, "reasons"), "; ")
		}
		w.line(tostring(index(str(label), object("start", null, "end", constant("17")))) + result)
	}

	// The host.
	q := "yes"
	if !truthy(field(v, "host_quiet")) {
		q = "no: " + join(field(v, "host_noise"), "; ")
	}
	w.line("host quiet       " + q)
}

// Classify is what `laug judge -classify <reason>` prints: reason_class of the reason, and a
// newline. jq takes the reason by --arg, so bad UTF-8 in it is read as Str reads it; the shell's `${2-}` makes a
// missing reason "", which is class unknown.
func Classify(reason string) []byte {
	return []byte(ReasonClass(reason) + "\n")
}
