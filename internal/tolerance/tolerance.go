// Package tolerance computes the incumbent arm's tolerances from A/A gates (docs/DESIGN.md, 7.1): its usage checks
// (Check), the gather of each gate's files, RULE and SHOW (Run), over a results directory's gates and the threshold set
// given as JSON. laug gate tolerance resolves the set with load/thresholds.sh and yq. It began as a shell script under
// gate/, "the script" here, whose jq the comments name. Until it was deleted, tests ran the two on the same gates:
// stdout and the exit matched byte for byte, and stderr too, but for laug's ~/ paths and the lines Run words itself.
//
// Values are jsonx's, as jq 1.7 holds them, and every step follows the script's jq: the files read as its load()
// reads them, null for a bad one (gather.go); its jq builtins and errors (jq.go); RULE (rule.go), whose object is
// written as jq -c writes it and read back, as the script's $(...) and <<< hand it to SHOW (show.go). The arithmetic
// rounds every product with an explicit float64() (jq.go, times), so that no compiler fuses it into an add.
//
// The standard library and internal/jsonx only (cmd/laug's TestStdlibOnly).
package tolerance

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// Request is one run: the gates and the three knobs, laug gate tolerance's -results, -pair and -evidence.
type Request struct {
	IDs      []string // the gate ids, as the script splits its argument
	Results  string   // GATE_RESULTS as given, for the usage line and find's; "" is gate/results, the script's default
	Pair     string   // PAIR: <a>:<b>, two plain items read in each gate instead of the arm's pair
	Evidence string   // EVIDENCE: the file in cells/results/ the compare: block cites
}

// results is GATE_RESULTS as the script resolves it.
func (r Request) results() string {
	if r.Results == "" {
		return "gate/results"
	}
	return r.Results
}

// usageLine is the script's usage, which every usage error starts with, on stderr.
const usageLine = `laug · gate tolerance <gate id>... [-pair <a>:<b>] [-evidence cells/results/<file>]`

// A UsageError is a request the script refuses before it reads a gate, exit 2. Its text is the script's line.
type UsageError struct{ Why string }

func (e *UsageError) Error() string { return usageLine + ": " + e.Why }

var (
	gateID   = regexp.MustCompile(`^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)
	pairRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}:[a-z0-9][a-z0-9-]{0,31}$`)
	evidence = regexp.MustCompile(`^(cells/results/[A-Za-z0-9._-]+|docs/tables/gates\.jsonl)$`)
)

// Check is the script's usage checks, in its order, over dir, the results directory as laug opens it: a
// gate, each a gate id, none twice, PAIR two item keys that differ, EVIDENCE a file name in cells/results/ or the
// gates table, docs/tables/gates.jsonl, and a directory in dir for each gate, after symlinks. The first that fails is
// a *UsageError.
func Check(dir string, r Request) error {
	if err := checkKnobs(r); err != nil {
		return err
	}
	for _, g := range r.IDs {
		if fi, err := os.Stat(filepath.Join(dir, g)); err != nil || !fi.IsDir() {
			return &UsageError{Why: "no gate run " + g + " in -results, " + r.results()}
		}
	}
	return nil
}

// checkKnobs is Check's checks before the directories: a gate, each a gate id, none twice, PAIR and EVIDENCE.
func checkKnobs(r Request) error {
	usage := func(why string) error { return &UsageError{Why: why} }
	if len(r.IDs) == 0 {
		return usage("the gate ids name no gate")
	}
	for _, g := range r.IDs {
		if !gateID.MatchString(g) {
			return usage("'" + g + "' is not a gate id, g<date>-<time>")
		}
	}
	sorted := slices.Clone(r.IDs)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) < len(r.IDs) {
		return usage("the gate ids name a gate twice")
	}
	if r.Pair != "" && !pairRE.MatchString(r.Pair) {
		return usage("-pair " + r.Pair + " is not <a>:<b>, two item keys")
	}
	if a, b, _ := strings.Cut(r.Pair, ":"); r.Pair != "" && a == b {
		return usage("-pair " + r.Pair + " names one item twice")
	}
	if r.Evidence != "" && !evidence.MatchString(r.Evidence) {
		return usage("-evidence " + r.Evidence + " is not a file in cells/results/ or " + TablesEvidence)
	}
	return nil
}

// The script's own lines when its jq does not run.
const (
	ruleDidNotRun = "laug · gate tolerance: the rule did not run"
	NoBase        = "laug · gate tolerance: load/thresholds.sh does not resolve the base"
)

// Run is the script after its set, for a request Check passed: it gathers the gates in dir, the results
// directory as laug opens it, runs RULE with set, the JSON text the set resolved to, and prints what the script
// printed. It reads files and writes none. It returns the exit:
//
//   - 0: the table on stdout, SHOW's lines.
//   - 2: the rule did not run, with why, then the script's line; or gates refused, a line a gate, on stderr.
//   - 5: SHOW stopped on a jq error, after the lines before it, as the script's last jq exited.
//
// Where the script's find cannot read a directory, Run prints find's line first, as find words it (gather). Where
// the script's jq fails at run time, Run prints the line jq printed, "jq: error (at <stdin>:N): <msg>". Where
// jq cannot read its input, Run says why in a line of its own in place of jq's: a set that is not one JSON value,
// which jq refuses at --argjson, or gathered text that does not parse, which happens when a file is there and cannot
// be read, gate.json is empty or holds two values, or a value nests past jq's depth limit.
func Run(dir string, r Request, set []byte, stdout, stderr io.Writer) int {
	text, found := gather(dir, r.results(), r.IDs, r.Pair)
	return run(text, found, r, set, stdout, stderr)
}

// run is Run from the gathered text and find's lines, from the directories (Run) or the tables (RunRows).
func run(text []byte, found string, r Request, set []byte, stdout, stderr io.Writer) int {
	sv, err := jsonx.Parse(set)
	if err != nil {
		fmt.Fprintf(stderr, "laug · gate tolerance: the set is not one JSON value, which jq's --argjson needs: %v\n",
			err)
		fmt.Fprintln(stderr, ruleDidNotRun)
		return 2
	}
	// find's lines come first: jq runs RULE only once it has read the whole gathered text. The set is checked before
	// them, as jq refuses a bad --argjson before it reads a byte.
	io.WriteString(stderr, found)
	gates, err := jsonx.Parse(text)
	if err != nil {
		fmt.Fprintf(stderr, "laug · gate tolerance: the gates' files, as gather() prints them, are not one JSON "+
			"text, which jq reads: %v\n", err)
		fmt.Fprintln(stderr, ruleDidNotRun)
		return 2
	}
	var out jsonx.Value
	if e := evaluate(func() { out = rule(gates, sv, r.Pair) }); e != nil {
		fmt.Fprintf(stderr, "jq: error (at <stdin>:%d): %s\n", jqLine(text, len(text)-2), e.msg)
		fmt.Fprintln(stderr, ruleDidNotRun)
		return 2
	}
	// out="$(jq -c ...)", then <<<"$out": jq -c's one line, read again, so every number is a literal of its text.
	line := bytes.TrimSuffix(jsonx.MarshalCompact(out), []byte("\n"))
	back, err := jsonx.Parse(line)
	if err != nil {
		panic(fmt.Sprintf("tolerance: RULE's own output does not read back: %v", err))
	}
	o, _ := back.Obj()
	refused, _ := o.Get("refused")
	if lines, _ := refused.Arr(); len(lines) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "laug · gate tolerance: %d gate(s) refused; nothing computed\n", len(lines))
		for _, l := range lines {
			s, _ := l.Str()
			b.WriteString("  " + s + "\n")
		}
		io.WriteString(stderr, b.String())
		return 2
	}
	lines, e := show(back, r.Pair, r.Evidence)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	io.WriteString(stdout, b.String())
	if e != nil {
		input := append(line, '\n')
		fmt.Fprintf(stderr, "jq: error (at <stdin>:%d): %s\n", jqLine(input, len(line)-1), e.msg)
		return 5
	}
	return 0
}

// jqLine is the N of jq's "(at <stdin>:N)" for the value whose last byte is at last in input. jq 1.7 reads its input
// as fgets does, at most 4095 bytes at a time and never past a newline, counts the reads that end in one, and runs
// the program once a read completes the value (seen on jq-1.7: a value of 4094 or 4096 bytes and its newline give 1,
// one of 4095 bytes gives 0, its newline not yet read).
func jqLine(input []byte, last int) int {
	n := 0
	for start := 0; start < len(input); {
		end := min(start+4095, len(input))
		if i := bytes.IndexByte(input[start:end], '\n'); i >= 0 {
			end = start + i + 1
			n++
		}
		if last < end {
			return n
		}
		start = end
	}
	return n
}
