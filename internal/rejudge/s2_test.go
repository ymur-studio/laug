package rejudge

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// TestSignature holds Signature to its table, on made runs that reach each branch of SIG: the signature, or the
// start of its error.
func TestSignature(t *testing.T) {
	for _, c := range sigCases() {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "run")
			copyTree(t, runDir(c.base), dir)
			c.edit(t, dir)
			sig, err := Signature(dir, root)
			switch {
			case c.err == "" && err != nil:
				t.Fatalf("Signature: %v", err)
			case c.err != "" && (err == nil || !strings.HasPrefix(err.Error(), c.err)):
				t.Fatalf("Signature: %v, want an error that starts %q", err, c.err)
			case c.check != nil:
				c.check(t, sig)
			}
		})
	}
}

func parse(data []byte) (jsonx.Value, bool) {
	v, err := jsonx.Parse(data)
	return v, err == nil
}

// A sigCase is a committed run with one edit, and what Signature makes of it.
type sigCase struct {
	name, base string
	edit       func(t *testing.T, dir string)
	err        string                              // the start of Signature's error; "" for none
	check      func(t *testing.T, sig jsonx.Value) // more on the signature, when not nil
}

// sigCases reach each branch of SIG and each field's reading, on a v2 run with a comparison, a v1 run and a trap.
func sigCases() []sigCase {
	const (
		v2run = "g20261018-092540"
		v1run = "g20261014-215905" // a dev host run (runDir)
		trap  = "g20261017-014142"
	)
	verdict := func(f func(o *jsonx.Object)) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) { editObject(t, filepath.Join(dir, "verdict.json"), f) }
	}
	text := func(name, body string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, name), []byte(body)) }
	}
	remove := func(name string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) { must(t, os.Remove(filepath.Join(dir, name))) }
	}
	set := func(k, v string) func(o *jsonx.Object) {
		return func(o *jsonx.Object) { o.Set(k, mustParse(v)) }
	}
	field := func(k, want string) func(t *testing.T, sig jsonx.Value) {
		return func(t *testing.T, sig jsonx.Value) {
			t.Helper()
			if got := index(sig, k).String(); got != want {
				t.Errorf("%s: %s, want %s", k, got, want)
			}
		}
	}
	inputs := func(in string) func(t *testing.T, dir string) { return verdict(set("inputs", in)) }
	v1sha := "284a759c54557c18bcbdfc257414199c9c94bfb573e3234e2265ecf18ab5366c"
	v2sha := "33b4e89dc2ecef502e73ea88136a482fbf8694986789712fd5126e31f5faf6f0"
	tl := func(steps ...string) string {
		var b strings.Builder
		for _, s := range steps {
			b.WriteString(jsonx.Str(s).String() + "\n")
		}
		return b.String()
	}
	timeline := func(steps ...string) func(t *testing.T, dir string) {
		var b strings.Builder
		for _, line := range strings.Split(strings.TrimSuffix(tl(steps...), "\n"), "\n") {
			b.WriteString(`{"step":` + line + `,"wall":1.0}` + "\n")
		}
		return text("timeline.jsonl", b.String())
	}
	return []sigCase{
		{name: "v2", base: v2run, edit: func(*testing.T, string) {}, check: field("judge", `"v2"`)},
		{name: "v1", base: v1run, edit: func(*testing.T, string) {}, check: field("judge", `"v1"`)},
		{name: "a trap", base: trap, edit: func(*testing.T, string) {}, check: field("failed_rules", `[]`)},
		{name: "events and marks", base: v2run, check: func(t *testing.T, sig jsonx.Value) {
			field("events", `["done","done\n","ended a","laugd stopped\n","watchdog TERM"]`)(t, sig)
			field("marks", `["start","done\n\n","laugd stopped\n\n","x\nleased a","leased","done\r","lease a"]`)(t, sig)
		}, edit: timeline("start", "done\n", "done\n\n", "laugd stopped\n", "laugd stopped\n\n", "x\nleased a",
			"leased", "watchdog TERM", "watchdog TERM", "done", "done\r", "ended a", "lease a")},
		{name: "two gate/combine.sh entries", base: v2run, check: field("judge", `"v1"`),
			edit: inputs(`[5, {"path":"gate/combine.sh","sha256":"` + v1sha + `"},
				{"path":"gate/combine.sh","sha256":"` + v2sha + `"}]`)},
		{name: "an unnamed judge", base: v2run, check: field("judge", `"sha256:`+strings.Repeat("0", 64)+`"`),
			edit: inputs(`[{"path":"gate/combine.sh","sha256":"` + strings.Repeat("0", 64) + `"}]`)},
		{name: "a sha256 that is no sha256", base: v2run, check: field("judge", `"sha256:x"`),
			edit: inputs(`[{"path":"gate/combine.sh","sha256":"x"}]`)},
		{name: "a sha256 a number", base: v2run, check: field("judge", `1.50`),
			edit: inputs(`[{"path":"gate/combine.sh","sha256":1.50}]`)},
		{name: "a sha256 false, an internal/judge entry", base: v2run, check: field("judge", `"v3"`),
			edit: inputs(`[{"path":"gate/combine.sh","sha256":false},{"path":"internal/judge","version":"v3"}]`)},
		{name: "an internal/judge entry", base: v2run, check: field("judge", `"v3"`),
			edit: inputs(`{"a":{"path":"internal/judge","version":"v3","sha256":"x"}}`)},
		{name: "an internal/judge entry with no version", base: v2run, check: field("judge", `null`),
			edit: inputs(`[{"path":"internal/judge"},{"path":"internal/judge","version":"v3"}]`)},
		{name: "no inputs", base: v2run, check: field("judge", `null`), edit: inputs(`"x"`)},
		{name: "no set with no comparison", base: v2run, check: field("set_sha256", `null`),
			edit: verdict(set("comparison", `null`))},
		{name: "a comparison with no rows", base: v2run, check: field("comparison", `{"mode":null,"rows":[]}`),
			edit: verdict(set("comparison", `{}`))},
		{name: "rows an object, a row null", base: v2run,
			check: field("comparison", `{"mode":"m","rows":[{"name":null,"judged":null,"result":null},`+
				`{"name":"n","judged":1.0,"result":null}]}`),
			edit: verdict(set("comparison", `{"mode":"m","rows":{"a":null,"b":{"name":"n","judged":1.0}}}`))},
		{name: "refused false is refused", base: v2run, check: field("refused", `true`),
			edit: verdict(set("refused", `false`))},
		{name: "rules, sorted, pass true alone passes", base: v2run,
			check: field("failed_rules", `["R1","R10","R2","R3","R4"]`),
			edit: verdict(set("rules", `{"R4":{"pass":"true"},"R10":{},"R2":null,"R1":{"pass":1},"R3":{"pass":false},`+
				`"R5":{"pass":true}}`))},
		{name: "items, literals as written", base: v2run,
			check: field("items", `[{"key":null,"kind":null,"arm":null,"expect":null,"observed":null,"class":null,`+
				`"exit":1E+2,"stopped_by":null,"block":0.10}]`),
			edit: verdict(set("items", `[{"exit":1e2,"block":0.10,"x":1}]`))},
		{name: "no timeline.jsonl", base: v2run, edit: remove("timeline.jsonl"), err: "no timeline.jsonl in "},
		{name: "timeline.jsonl a directory", base: v2run, err: "no timeline.jsonl in ",
			edit: func(t *testing.T, dir string) {
				must(t, os.Remove(filepath.Join(dir, "timeline.jsonl")))
				must(t, os.Mkdir(filepath.Join(dir, "timeline.jsonl"), 0o755))
			}},
		{name: "an empty timeline", base: v2run, edit: text("timeline.jsonl", ""),
			err: "(outside the repository)/run: timeline.jsonl does not begin with the mark start"},
		{name: "a timeline that does not parse", base: v2run, edit: text("timeline.jsonl", `{"step":"start"}`+"\n{\n"),
			err: "timeline.jsonl in (outside the repository)/run does not parse"},
		{name: "a timeline from lock", base: v2run, edit: timeline("lock", "start"),
			err: "(outside the repository)/run: timeline.jsonl does not begin with the mark start"},
		{name: "a line with no step", base: v2run, edit: text("timeline.jsonl", `{"step":"start"}`+"\n"+`{"step":5}`),
			err: "(outside the repository)/run: timeline.jsonl: a line names no step"},
		{name: "a line a number", base: v2run, edit: text("timeline.jsonl", `{"step":"start"} 5`),
			err: "(outside the repository)/run: timeline.jsonl: a line names no step"},
		{name: "no verdict.json", base: v2run, edit: remove("verdict.json"), err: "no verdict.json in "},
		{name: "verdict.json two values", base: v2run, edit: text("verdict.json", "{} {}"),
			err: "(outside the repository)/run: verdict.json is not one JSON object"},
		{name: "verdict.json empty", base: v2run, edit: text("verdict.json", ""),
			err: "(outside the repository)/run: verdict.json is not one JSON object"},
		{name: "verdict.json an array", base: v2run, edit: text("verdict.json", "[]"),
			err: "(outside the repository)/run: verdict.json is not one JSON object"},
		{name: "verdict.json does not parse", base: v2run, edit: text("verdict.json", "{"),
			err: "verdict.json in (outside the repository)/run does not parse"},
		{name: "verdict MAYBE", base: v2run, edit: verdict(set("verdict", `"MAYBE"`)),
			err: "(outside the repository)/run: verdict.json holds no gate verdict"},
		{name: "items an object", base: v2run, edit: verdict(set("items", `{}`)),
			err: "(outside the repository)/run: verdict.json holds no gate verdict"},
		{name: "rules a string", base: v2run, edit: verdict(set("rules", `"x"`)),
			err: "(outside the repository)/run: verdict.json: rules is neither null nor an object"},
		{name: "rules null, not stopped", base: v2run, edit: verdict(set("rules", `null`)),
			err: "(outside the repository)/run: verdict.json: rules is null, and stopped_at is null"},
		{name: "rules false", base: v2run, edit: verdict(set("rules", `false`)),
			err: "(outside the repository)/run: verdict.json: rules is neither null nor an object"},
		{name: "an item a string", base: v2run, edit: verdict(set("items", `[{}, "x"]`)),
			err: "(outside the repository)/run: verdict.json: an item is not an object"},
		{name: "a rule a string", base: v2run, edit: verdict(set("rules", `{"R1":"x"}`)),
			err: "the signature of (outside the repository)/run failed: Cannot index string with string \"pass\""},
		{name: "comparison true", base: v2run, edit: verdict(set("comparison", `true`)),
			err: "the signature of (outside the repository)/run failed: Cannot index boolean with string \"mode\""},
		{name: "a row a number", base: v2run, edit: verdict(set("comparison", `{"rows":[5]}`)),
			err: "the signature of (outside the repository)/run failed: Cannot index number with string \"name\""},
		{name: "a rule an array and comparison a string", base: v2run,
			edit: verdict(func(o *jsonx.Object) { set("rules", `{"R1":[]}`)(o); set("comparison", `"x"`)(o) }),
			err:  "the signature of (outside the repository)/run failed: Cannot index array with string \"pass\""},
	}
}

// editObject parses the JSON object at path, edits it and writes it back in jq's form.
func editObject(t *testing.T, path string, f func(o *jsonx.Object)) {
	t.Helper()
	v, err := jsonx.Parse(readFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	o, _ := v.Obj()
	f(o)
	writeFile(t, path, jsonx.Marshal(v))
}

func mustParse(s string) jsonx.Value {
	v, err := jsonx.Parse([]byte(s))
	if err != nil {
		panic(err)
	}
	return v
}

// TestSignatureFields: the signature's fields are sigFields, in its order.
func TestSignatureFields(t *testing.T) {
	sig, err := Signature(filepath.Join(results, "g20261018-092540"), root)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := sig.Obj()
	if got := o.Keys(); !slices.Equal(got, sigFields) {
		t.Errorf("fields %q, want %q", got, sigFields)
	}
	if _, err := Signature(filepath.Join(t.TempDir(), "none"), root); err == nil ||
		err.Error() != "no verdict.json in (outside the repository)/none" {
		t.Errorf("no run: %v", err)
	}
}
