// Package judge reads a gate run's directory and judges it, ADVANCE or BLOCK, under the judge version the run
// recorded, so that every committed run in gate/results judges again to its own verdict.json and verdict.md, byte
// for byte. Values are internal/jsonx's, which hold numbers, key order and strings as jq 1.7 does. The standard
// library only. The judge began as shell scripts under gate/, bash and jq, which this tree no longer holds. Each
// version here ports one, named by its sha256, and the Go judge matched them on every committed run
// (docs/CAPABILITIES.md, the judge row). The jq a comment quotes or names is v2's, unless it says v1's or v3's.
//
// The registry (registry.go) names each judge version by the sha256 that a run's gate.json records for its shell
// judge, with the features it adds to v1. The reader (input.go) gathers a run's files as that judge's gather() did.
// The rules (rules.go, items.go, arm.go and classes.go, on defs.go, epoch.go and match.go) are v2's JUDGE, and v1's,
// v2-r7off's and v3's with their features off or on: Evaluate gives the verdict object a version writes (verdict.go),
// or the error its jq stops on. Render (render.go) and Summary (summary.go) are RENDER and SUMMARY over verdict.json's
// bytes read back, and Judge (judge.go) runs the four in that order. RerenderTrap (trap.go) writes the gate's trap
// verdict, which no judge wrote, in each of its three forms. The identity (identity.go) is the sha256 of this
// package's and internal/jsonx's sources, their tests left out, which a gate run records. TestCorpusAnswers holds
// Judge to the shell's answers on a mutation corpus (corpus_test.go); TestV3IsV2Fixed holds v3 to v2 and its fixes.
package judge

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// versionsTxt holds one line per ported judge version, "<name> <sha256>", oldest first: the name, and the full
// sha256 of that version's shell judge, which gate.json's inputs record: it names the judge that judged a kept run.
// A line, once committed, never changes (TestRegistryMatchesArchive).
//
//go:embed versions.txt
var versionsTxt string

// A Version is one judge version: a shell judge that judged committed runs, or, as v3, the one new runs get.
type Version struct {
	Name   string // v1, v2, v2-r7off or v3
	SHA256 string // the sha256 of this version's shell judge, 64 lowercase hex
	Features
}

// Features are what a version adds to v1, sha256 284a759c5455. One implementation holds every version:
// with every feature off, each place v2 rewrote a v1 line gives v1's text.
type Features struct {
	// Arm is R7, the comparison of the release with the incumbent under the incumbent's thresholds, and what comes with
	// it: 3 class_table rows, R7 and the comparison ($arm7 and $thi_rel), the "incumbent: " prefix on the incumbent's
	// own reason ($own), R6 by the item's own release ($is_arm, $id and $r6), R7 in each item's reasons and record ($r7
	// and $reason), the check of thresholds-incumbent.json in $g6, R7 in $rules, incumbent and comparison in
	// verdict.json, the comparison block in verdict.md (RENDER's R7 block), and the line of gather() that reads
	// thresholds-incumbent.json.
	Arm bool
	// HostCheck is R6's host mirror of preflight: gate.json's host is the type, vcpu and memory the set
	// names.
	HostCheck bool
	// R7Off is a test branch's judge, sha256 d7278dabbb85: `{} as $r7_by_key`, so R7 still
	// compares and records but never fails an item.
	R7Off bool
	// ZeroFix is v3's fx: `round | if . == 0 then 0 else . end`, so a number that rounds to -0 prints as 0,
	// "0.000" and "0.0000 points", where v2 and v2-r7off print "0.0-0" and "0.00-0 points" (4 committed v2 runs
	// do). v1 has no fx.
	ZeroFix bool
	// ToleranceText is v3's wave reason, which ends "laug gate tolerance on A/A gates of this plan" where v2's
	// ends "make arm-tolerance on A/A gates of this plan".
	ToleranceText bool
}

// features names each version's: a version has Arm when its shell judge gathered thresholds-incumbent.json, HostCheck
// when it reported a set with no host type, R7Off when it bound R7's reasons to {}, ZeroFix when its fx turned a -0
// into 0, and ToleranceText when its wave reason named laug gate tolerance. v3 is v2 with the last two and nothing else
// (TestV3IsV2Fixed).
var features = map[string]Features{
	"v1":       {},
	"v2":       {Arm: true, HostCheck: true},
	"v2-r7off": {Arm: true, HostCheck: true, R7Off: true},
	"v3":       {Arm: true, HostCheck: true, ZeroFix: true, ToleranceText: true},
}

// Current is the version a new run gets, the one bin/laug judge -write judges it under (TestRegistryMatchesArchive
// holds it to a line of versions.txt).
const Current = "v3"

// current is Current's Version. A Current that versions.txt lacks is a build fault, as a malformed line is.
var current = func() Version {
	v, ok := Named(Current)
	if !ok {
		panic("internal/judge: Current " + Current + " is not in versions.txt")
	}
	return v
}()

// notPorted is the one shell judge with no version here, sha256 2a9ff8b3f8f0, which no committed run
// records. A run that names it is not judged.
var notPorted = []string{"2a9ff8b3f8f0352934d469d877d2a05fb396a3f0d23d2e89ca708563d019c26b"}

// versions is versionsTxt read once. A malformed line is a build fault, which TestRegistryMatchesArchive catches
// before any binary carries it, so it panics.
var versions = mustParseVersions(versionsTxt)

func mustParseVersions(text string) []Version {
	vs, err := parseVersions(text)
	if err != nil {
		panic("internal/judge/versions.txt: " + err.Error())
	}
	return vs
}

// parseVersions reads versions.txt: each line "<name> <sha256>", one space between, no blank line, every name and
// sha256 once, and an entry in features for every name.
func parseVersions(text string) ([]Version, error) {
	body, ok := strings.CutSuffix(text, "\n")
	if !ok || body == "" {
		return nil, fmt.Errorf("want lines ending in a newline, got %q", text)
	}
	var vs []Version
	names, shas := map[string]bool{}, map[string]bool{}
	for i, line := range strings.Split(body, "\n") {
		name, sha, ok := strings.Cut(line, " ")
		if !ok || name == "" || strings.ContainsAny(name, " \t") || !isSHA256(sha) {
			return nil, fmt.Errorf("line %d: want \"<name> <64 lowercase hex>\", got %q", i+1, line)
		}
		if names[name] || shas[sha] {
			return nil, fmt.Errorf("line %d: %s or its sha256 is on an earlier line", i+1, name)
		}
		f, ok := features[name]
		if !ok {
			return nil, fmt.Errorf("line %d: %s has no entry in registry.go's features table", i+1, name)
		}
		names[name], shas[sha] = true, true
		vs = append(vs, Version{Name: name, SHA256: sha, Features: f})
	}
	return vs, nil
}

// Versions is every ported version, in versions.txt's order.
func Versions() []Version { return append([]Version(nil), versions...) }

// Lookup is the version whose shell judge has the sha256 sha. A sha256 no version has, such as a shell judge
// that was not ported, is not found.
func Lookup(sha string) (Version, bool) {
	for _, v := range versions {
		if v.SHA256 == sha {
			return v, true
		}
	}
	return Version{}, false
}

// Named is the version called name.
func Named(name string) (Version, bool) {
	for _, v := range versions {
		if v.Name == name {
			return v, true
		}
	}
	return Version{}, false
}

// RecordedSHA256 is the gate/combine.sh sha256 that a run's gate.json records, read from the file's bytes as
// this jq program reads it:
//
//	jq -r '[.inputs[]? | objects | select(.path == "gate/combine.sh") | .sha256 | strings][0] // empty' gate.json
//
// kept only when it matches ^[0-9a-f]{64}$. jq reads the file as a stream: each value prints its first such string,
// a value with none prints nothing, and null prints nothing. A value that is not an object or null is jq's error
// ("Cannot index array with "inputs"", seen on jq-1.7), and any error, or a file jq cannot parse, gives "". The shell
// keeps what jq printed less its NUL bytes and trailing newlines, so two values that each print one give two lines,
// and no sha256.
func RecordedSHA256(gateJSON []byte) string {
	vs, err := jsonx.ParseStream(gateJSON)
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, v := range vs {
		if v.IsNull() {
			continue // null.inputs is null, and null[]? gives nothing
		}
		o, ok := v.Obj()
		if !ok {
			return "" // jq: Cannot index <type> with "inputs", exit 5
		}
		in, _ := o.Get("inputs")
		for _, e := range members(in) { // .inputs[]?: an array's elements, an object's values, else nothing
			eo, ok := e.Obj()
			if !ok {
				continue // objects
			}
			if p, _ := eo.Get("path"); !jsonx.Equal(p, jsonx.Str("gate/combine.sh")) {
				continue
			}
			sha, _ := eo.Get("sha256")
			if s, ok := sha.Str(); ok {
				out.WriteString(s + "\n") // [...][0] // empty: the first string, raw under -r
				break
			}
		}
	}
	s := strings.TrimRight(strings.ReplaceAll(out.String(), "\x00", ""), "\n")
	if !isSHA256(s) {
		return ""
	}
	return s
}

// members is v's elements when v is an array and its values when v is an object, else none, as .[]? walks them.
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

// isSHA256 says s is 64 lowercase hex digits.
func isSHA256(s string) bool {
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
