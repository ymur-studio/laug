package tolerance

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// results is the committed runs.
func results() string { return filepath.Join(root, "gate", "results") }

// labRuns is internal/rejudge's copy of the lab host runs the tests name that are not kept in gate/results
// (internal/rejudge/testdata/lab/README.md).
func labRuns() string { return filepath.Join("..", "rejudge", "testdata", "lab") }

// copyGate copies the committed gate id, as internal/rejudge's testdata/lab holds it, into dir/id: the files
// laug gate tolerance reads and the two items' directories.
func copyGate(t *testing.T, id, dir string) string {
	t.Helper()
	src, dst := filepath.Join(labRuns(), id), filepath.Join(dir, id)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// madeGate is one gate made from a committed one, under an id of its own: name says what it holds.
type madeGate struct {
	name string
	edit func(t *testing.T, g string) // g: the gate's directory
}

// base is the committed gate each made gate copies (internal/rejudge/testdata/lab holds it): an A/A gate whose
// incumbent and steady items read numbers.
const base = "g20261017-174403"

var madeGates = []madeGate{
	{"as committed", func(*testing.T, string) {}},
	{"verdict.json a directory", func(t *testing.T, g string) {
		remove(t, g+"/verdict.json")
		write(t, g+"/verdict.json/x", "1")
	}},
	{"verdict.json empty", func(t *testing.T, g string) { write(t, g+"/verdict.json", "") }},
	{"a comparison without a wave", func(t *testing.T, g string) {
		b, err := os.ReadFile(g + "/verdict.json")
		if err != nil {
			t.Fatal(err)
		}
		v, err := jsonx.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		o, _ := v.Obj()
		c, _ := o.Get("comparison")
		co, _ := c.Obj()
		co.Set("wave", jsonx.Null())
		write(t, g+"/verdict.json", string(jsonx.Marshal(v)))
	}},
	{"verdict.json two values", func(t *testing.T, g string) { write(t, g+"/verdict.json", "{} {}") }},
	{"verdict.json a parse error", func(t *testing.T, g string) { write(t, g+"/verdict.json", "{") }},
	{"verdict.json a dangling symlink", func(t *testing.T, g string) {
		remove(t, g+"/verdict.json")
		symlink(t, "nowhere", g+"/verdict.json")
	}},
	{"thresholds.json behind a mark", func(t *testing.T, g string) {
		write(t, g+"/thresholds.json", "\xef\xbb\xbf{\"thresholds\":[]}")
	}},
	{"gate.json empty", func(t *testing.T, g string) { write(t, g+"/gate.json", "") }},
	{"gate.json absent", func(t *testing.T, g string) { remove(t, g+"/gate.json") }},
	{"gate.json a directory", func(t *testing.T, g string) {
		remove(t, g+"/gate.json")
		write(t, g+"/gate.json/x", "1")
	}},
	{"gate.json a value, then garbage", func(t *testing.T, g string) {
		write(t, g+"/gate.json", `{"driver":"x"} garbage`)
	}},
	{"gate.json a value, then an open one", func(t *testing.T, g string) { write(t, g+"/gate.json", `{"driver":"x"}{`) }},
	{"gate.json a number, then a value", func(t *testing.T, g string) {
		write(t, g+"/gate.json", `5 {"driver":"step.sh"}`)
	}},
	{"gate.json a value, then a number", func(t *testing.T, g string) {
		write(t, g+"/gate.json", `{"driver":"step.sh"} 5`)
	}},
	{"gate.json two values", func(t *testing.T, g string) { write(t, g+"/gate.json", `{"driver":"a"}{"driver":"b"}`) }},
	{"gate.json an array", func(t *testing.T, g string) { write(t, g+"/gate.json", `[1]`) }},
	{"gate.json a driver of every kind", func(t *testing.T, g string) {
		write(t, g+"/gate.json", `{"driver":{"a":[1,2.50]}}`)
	}},
	{"gate.json behind a mark", func(t *testing.T, g string) {
		write(t, g+"/gate.json", "\xef\xbb\xbf{\"driver\":\"m\"}")
	}},
	{"gate.json a mark later", func(t *testing.T, g string) {
		write(t, g+"/gate.json", "{\"driver\":\"m\"} \xef\xbb\xbf{}")
	}},
	{"gate.json a bracket too many", func(t *testing.T, g string) { write(t, g+"/gate.json", `{"driver":"x"}}`) }},
	{"gate.json a token and a NUL", func(t *testing.T, g string) {
		write(t, g+"/gate.json", "1\x00x {\"driver\":\"n\"}")
	}},
	{"plan.json two values", func(t *testing.T, g string) {
		b, _ := os.ReadFile(g + "/plan.json")
		write(t, g+"/plan.json", string(b)+string(b))
	}},
	{"plan.json a key that is a number", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[{"key":100,"arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json a key that is true", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[{"key":true,"arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json a key that is a list", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[{"key":["a"],"arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json a key with a NUL", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[{"key":"incum\u0000bent","arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json a key in capitals", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[{"key":"Incumbent","arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json a key of 33", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[{"key":"`+strings.Repeat("a", 33)+`","arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json a key and no pair", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[{"key":"incumbent","arm":"incumbent"}]}`)
	}},
	{"plan.json an item that is a number", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `{"items":[5,{"key":"incumbent","arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json items a number", func(t *testing.T, g string) { write(t, g+"/plan.json", `{"items":5}`) }},
	{"plan.json a number, then a plan", func(t *testing.T, g string) {
		write(t, g+"/plan.json", `5 {"items":[{"key":"incumbent","arm":"incumbent","pair":"steady"}]}`)
	}},
	{"plan.json absent", func(t *testing.T, g string) { remove(t, g+"/plan.json") }},
	{"known-slow of every kind", func(t *testing.T, g string) {
		write(t, g+"/steady/known-slow.json", "{}")
		write(t, g+"/.hidden/known-slow.json", "{}")
		write(t, g+"/a-b/known-slow.json", "{}")
		write(t, g+"/zz/known-slow.json/x", "1")
		write(t, g+"/other/x", "1")
		symlink(t, "nowhere", g+"/other/known-slow.json")
		symlink(t, "steady", g+"/linked")
		write(t, g+"/x\xffy/known-slow.json", "{}")
	}},
	{"directories find cannot read", func(t *testing.T, g string) {
		// Each empty, so that TempDir's cleanup removes it: rmdir needs no permission on the directory itself. The
		// second name holds each kind of byte find quotes.
		for _, d := range []struct {
			name string
			mode os.FileMode
		}{{"locked", 0}, {"it's a\\b \xff\t\x7f\x1b?", 0o111}, {"w", 0o222}} {
			if err := os.Mkdir(g+"/"+d.name, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(g+"/"+d.name, d.mode); err != nil {
				t.Fatal(err)
			}
		}
	}},
}

// makeGates makes each of madeGates under dir, ids g20261101-000000 and on, and returns their ids; and one more, a
// symlink to a copy of base that holds known-slow.json, which find does not walk into.
func makeGates(t *testing.T, dir string) []string {
	t.Helper()
	var ids []string
	for i, m := range madeGates {
		id := "g20261101-0000" + itoa2(i)
		tmp := t.TempDir()
		src := copyGate(t, base, tmp)
		m.edit(t, src)
		if err := os.Rename(src, filepath.Join(dir, id)); err != nil {
			t.Fatal(err)
		}
		fixIDs(t, filepath.Join(dir, id), id)
		ids = append(ids, id)
	}
	real := filepath.Join(dir, ".real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "g20261101-010000"
	g := copyGate(t, base, real)
	write(t, g+"/steady/known-slow.json", "{}")
	if err := os.Rename(g, filepath.Join(real, id)); err != nil {
		t.Fatal(err)
	}
	fixIDs(t, filepath.Join(real, id), id)
	symlink(t, filepath.Join(".real", id), filepath.Join(dir, id))
	return append(ids, id)
}

func itoa2(i int) string { return string([]byte{'0' + byte(i/10), '0' + byte(i%10)}) }

// fixIDs renames the item verdict files of base to the made gate's id, as a gate names them.
func fixIDs(t *testing.T, g, id string) {
	t.Helper()
	for _, k := range []string{"incumbent", "steady"} {
		from := filepath.Join(g, k, base+"-"+k+".verdict.json")
		if _, err := os.Lstat(from); err == nil {
			if err := os.Rename(from, filepath.Join(g, k, id+"-"+k+".verdict.json")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestGather gathers every committed run at once, with no PAIR and with s1:s2, and a set of made gates, with no PAIR
// and with incumbent:steady. The made gates hold bad files, a driver or a plan item of every kind, known-slow.json of
// every kind, directories find cannot read, and a gate directory that is a symlink. The committed runs' text parses.
// Over the made gates, find prints one stderr line for each of the 3 directories it cannot read, unless the test runs
// as root.
func TestGather(t *testing.T) {
	entries, err := os.ReadDir(results())
	if err != nil {
		t.Fatal(err)
	}
	var committed []string
	for _, e := range entries {
		committed = append(committed, e.Name())
	}
	made := t.TempDir()
	ids := makeGates(t, made)
	cases := []struct {
		name, dir, pair string
		ids             []string
	}{
		{"every committed run", results(), "", committed},
		{"every committed run, s1:s2", results(), "s1:s2", committed},
		{"made gates", made, "", ids},
		{"made gates, incumbent:steady", made, "incumbent:steady", ids},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := gather(c.dir, c.dir, c.ids, c.pair)
			if _, err := jsonx.Parse(got); err != nil && c.dir == results() {
				t.Errorf("the committed runs gather to text that does not parse: %v", err)
			}
			if c.dir == made && os.Geteuid() != 0 && strings.Count(found, "\n") != 3 {
				t.Errorf("find printed %d lines for the 3 directories it cannot read, want 3: %q", strings.Count(found,
					"\n"), found)
			}
		})
	}
	t.Logf("%d committed runs, %d made gates", len(committed), len(ids))
}

// TestStream holds stream, the values jq runs before a parse error, to jq -c . on the same bytes.
func TestStream(t *testing.T) {
	cases := []struct {
		data string
		want []string // compact values
		ok   bool
	}{
		{"", nil, true},
		{"  \n", nil, true},
		{"1", []string{"1"}, true},
		{"1 2", []string{"1", "2"}, true},
		{`{"a":1}{"b":2}`, []string{`{"a":1}`, `{"b":2}`}, true},
		{`{"driver":"x"} garbage`, []string{`{"driver":"x"}`}, false},
		{`{}{`, []string{`{}`}, false},
		{`[1]]`, []string{`[1]`}, false},
		{`1x 2`, nil, false},
		{"\xef\xbb\xbf1 2", []string{"1", "2"}, true},
		{"1 \xef\xbb\xbf2", []string{"1"}, false},
		{`"a" "b`, []string{`"a"`}, false},
		{`"a\"" 1`, []string{`"a\""`, "1"}, true},
		{`["]"] 2 ,`, []string{`["]"]`, "2"}, false},
		{`true false nul`, []string{"true", "false"}, false},
		{"1\x00x 2", []string{"1", "2"}, true},
		{strings.Repeat("[", 257) + strings.Repeat("]", 257), nil, false},
		{"1 " + strings.Repeat("[", 256) + strings.Repeat("]", 256) + " 3", nil, true},
		{`{"a":1,}`, nil, false},
		{`: 1`, nil, false},
	}
	jq := maybeJQ(t)
	for _, c := range cases {
		vs, ok := stream([]byte(c.data))
		var got []string
		for _, v := range vs {
			got = append(got, v.String())
		}
		if c.want == nil && strings.HasPrefix(c.data, "1 [") { // the deep array: two values and a third
			c.want = []string{"1", strings.Repeat("[", 256) + strings.Repeat("]", 256), "3"}
		}
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") || ok != c.ok {
			t.Errorf("%q: Go %q %t, the table %q %t", c.data, got, ok, c.want, c.ok)
		}
		if jq == "" {
			continue
		}
		out, _, code := jqRun(t, jq, []byte(c.data), "-c", ".")
		var want []string
		if s := strings.TrimSuffix(string(out), "\n"); s != "" {
			want = strings.Split(s, "\n")
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") || ok != (code == 0) {
			t.Errorf("%q: Go %q %t, jq %q, exit %d", c.data, got, ok, want, code)
		}
	}
}
