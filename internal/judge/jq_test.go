package judge

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// The tests of the rules hold Go to their tables. findJQ, maybeJQ and jqRun run jq 1.7.x itself on a program a test
// writes (TestRegexMatchesJQ, TestClassTable, TestBuiltins, TestEpochMatchesJQ, TestGather); without jq that part
// logs why it did not run, and LAUG_TEST_SHELL=require turns that into a failure.

// findJQ is jq 1.7.x, jq --version printing jq-1.7 or jq-1.7.<n>, or why there is none.
var findJQ = sync.OnceValues(func() (string, string) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		return "", "no jq on PATH"
	}
	out, err := exec.Command(jq, "--version").Output()
	if err != nil {
		return "", "jq --version failed: " + err.Error()
	}
	if v := strings.TrimSpace(string(out)); !regexp.MustCompile(`^jq-1\.7(\.|$)`).MatchString(v) {
		return "", "jq is " + v + ", not 1.7.x"
	}
	return jq, ""
})

// maybeJQ is jq 1.7.x, or "" with the reason logged.
func maybeJQ(t *testing.T) string {
	t.Helper()
	jq, why := findJQ()
	if why == "" {
		return jq
	}
	if os.Getenv("LAUG_TEST_SHELL") == "require" {
		t.Fatalf("LAUG_TEST_SHELL=require: %s", why)
	}
	t.Logf("not checked against jq: %s", why)
	return ""
}

// jqRun runs jq with args and stdin under LC_ALL=C, the locale the judge assumes, and gives its stdout and whether
// it exited 0; on a failure, its stderr.
func jqRun(jq string, stdin []byte, args ...string) ([]byte, bool) {
	cmd := exec.Command(jq, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stderr.Bytes(), false
	}
	return stdout.Bytes(), true
}

// fixtures are the harness's gate runs (load/testdata/gate), gathered as v2 gathers them: base, a plan of a load
// and two fault items that ADVANCEs, and base-arm, a load item of arm incumbent paired with a load item and a fault
// item, in recording mode, that ADVANCEs too.
var fixtures sync.Map

func fixture(t *testing.T, name string) jsonx.Value {
	t.Helper()
	if v, ok := fixtures.Load(name); ok {
		return v.(jsonx.Value)
	}
	in, err := Gather(os.DirFS(filepath.Join("..", "..", "load", "testdata", "gate", name)), Options{Arm: true})
	if err != nil {
		t.Fatal(err)
	}
	fixtures.Store(name, in.Value)
	return in.Value
}

// judgingWave is base-arm's plan as wave_sig gives it.
const judgingWave = `["load:incumbent workflow_with_single_noop_activity 20/s 50 5m",` +
	`"load workflow_with_single_noop_activity 20/s 50 5m",` +
	`"fault:persistence-latency workflow_with_single_noop_activity"]`

// judgingPatches make base-arm's comparison judging: a tolerance on each compare row at or above its floor, the
// wave the plan runs, and the evidence the tolerances came from.
var judgingPatches = []string{
	"thresholds.compare.rows[0].tolerance=1", "thresholds.compare.rows[1].tolerance=1",
	"thresholds.compare.rows[2].tolerance=0.01", "thresholds.compare.rows[3].tolerance=0.01",
	"thresholds.compare.rows[4].tolerance=0.01", "thresholds.compare.wave=" + judgingWave,
	`thresholds.compare.evidence="cells/results/aa.txt"`, "thresholds.compare.gates=20",
}

// input is a fixture with patches applied: "path=json" sets the value at path, "-path" deletes it. A path is keys
// joined by dots, each maybe with [n] places: plan.items[1].expect. The base "judging" is base-arm with
// judgingPatches.
func input(t *testing.T, base string, patches ...string) jsonx.Value {
	t.Helper()
	if base == "judging" {
		base, patches = "base-arm", append(append([]string(nil), judgingPatches...), patches...)
	}
	v := fixture(t, base)
	for _, p := range patches {
		var err error
		if path, ok := strings.CutPrefix(p, "-"); ok {
			v, err = setPath(v, steps(t, path), jsonx.Value{}, true)
		} else {
			path, text, _ := strings.Cut(p, "=")
			nv, perr := jsonx.Parse([]byte(text))
			if perr != nil {
				t.Fatalf("patch %q: %v", p, perr)
			}
			v, err = setPath(v, steps(t, path), nv, false)
		}
		if err != nil {
			t.Fatalf("patch %q: %v", p, err)
		}
	}
	return v
}

// A step is one key or one place of a path.
type step struct {
	key   string
	place int // -1 for a key
}

func steps(t *testing.T, path string) []step {
	t.Helper()
	var out []step
	for _, seg := range strings.Split(path, ".") {
		key, rest, _ := strings.Cut(seg, "[")
		out = append(out, step{key: key, place: -1})
		for rest != "" {
			n, tail, ok := strings.Cut(rest, "]")
			i, err := strconv.Atoi(n)
			if !ok || err != nil {
				t.Fatalf("path %q: bad place in %q", path, seg)
			}
			out = append(out, step{place: i})
			rest = strings.TrimPrefix(tail, "[")
		}
	}
	return out
}

// setPath is v with the value at path set to nv, or deleted, every object and array on the way copied. A key the
// object lacks is added last.
func setPath(v jsonx.Value, path []step, nv jsonx.Value, del bool) (jsonx.Value, error) {
	if len(path) == 0 {
		return nv, nil
	}
	s := path[0]
	if s.place >= 0 {
		a, ok := v.Arr()
		if !ok || s.place >= len(a) {
			return v, errors.New("no place " + strconv.Itoa(s.place))
		}
		b := append([]jsonx.Value(nil), a...)
		if len(path) == 1 && del {
			return jsonx.Arr(append(b[:s.place], b[s.place+1:]...)), nil
		}
		e, err := setPath(b[s.place], path[1:], nv, del)
		b[s.place] = e
		return jsonx.Arr(b), err
	}
	o, ok := v.Obj()
	if !ok {
		return v, errors.New("no object at " + s.key)
	}
	out := jsonx.NewObject()
	found := false
	for i := range o.Len() {
		k, m := o.Member(i)
		if k == s.key {
			found = true
			if len(path) == 1 && del {
				continue
			}
			var err error
			if m, err = setPath(m, path[1:], nv, del); err != nil {
				return v, err
			}
		}
		out.Set(k, m)
	}
	if !found {
		if del || len(path) > 1 {
			return v, errors.New("no key " + s.key)
		}
		out.Set(s.key, nv)
	}
	return jsonx.Obj(out), nil
}

// judgeV2 is judgeAs under v2.
func judgeV2(t *testing.T, in jsonx.Value) (jsonx.Value, error) {
	t.Helper()
	return judgeAs(t, mustNamed(t, "v2"), in)
}

// judgeAs evaluates in under version v, and fails the test when Evaluate's error is not a jq error. It gives the
// verdict and the error.
func judgeAs(t *testing.T, v Version, in jsonx.Value) (jsonx.Value, error) {
	t.Helper()
	got, err := Evaluate(v, in)
	if err != nil {
		var e *jqError
		if !errors.As(err, &e) {
			t.Fatalf("Evaluate: not a jq error: %v", err)
		}
	}
	return got, err
}

// firstLineDiff names the first line where want and got differ.
func firstLineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range min(len(w), len(g)) {
		if w[i] != g[i] {
			return "line " + strconv.Itoa(i+1) + ":\n  jq: " + w[i] + "\n  go: " + g[i]
		}
	}
	return "jq has " + strconv.Itoa(len(w)) + " lines, Go " + strconv.Itoa(len(g))
}

// at is the value at a dotted path of v, as input's patches name one, or null when there is none.
func at(t *testing.T, v jsonx.Value, path string) jsonx.Value {
	t.Helper()
	if path == "" {
		return v
	}
	for _, s := range steps(t, path) {
		if s.place >= 0 {
			a, _ := v.Arr()
			if s.place >= len(a) {
				return null
			}
			v = a[s.place]
			continue
		}
		o, ok := v.Obj()
		if !ok {
			return null
		}
		v, _ = o.Get(s.key)
	}
	return v
}

// A ruleCase is one row of a rule table: patches on a fixture, then a line the verdict's reasons must hold or must
// not, and values at verdict paths, each by its tojson.
type ruleCase struct {
	name   string
	base   string
	patch  []string
	reason string            // a line of .reasons, "R2 pl: ...", that must be there
	absent string            // a line that must not be there
	paths  map[string]string // verdict path: tojson
}

// runRules runs a table of rule cases.
func runRules(t *testing.T, cases []ruleCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := c.base
			if base == "" {
				base = "base"
			}
			v, err := judgeV2(t, input(t, base, c.patch...))
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			reasons := stringsOf(at(t, v, "reasons"))
			if c.reason != "" && !slices.Contains(reasons, c.reason) {
				t.Errorf("no reason %q in\n  %s", c.reason, strings.Join(reasons, "\n  "))
			}
			if c.absent != "" && slices.Contains(reasons, c.absent) {
				t.Errorf("reason %q is there", c.absent)
			}
			for p, want := range c.paths {
				if got := tojson(at(t, v, p)); got != want {
					t.Errorf("%s: got %s, want %s", p, got, want)
				}
			}
		})
	}
}
