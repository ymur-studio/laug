package judge

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// The repository's gate/ directory and its committed runs, from this package's directory; devRuns is the copy of the
// 7 dev host runs the tests name (internal/rejudge/testdata/runs/README.md), labRuns the copy of the lab host runs
// they name that are not kept in gate/results (internal/rejudge/testdata/lab/README.md).
var (
	gateDir = filepath.Join("..", "..", "gate")
	results = filepath.Join(gateDir, "results")
	devRuns = filepath.Join("..", "rejudge", "testdata", "runs")
	labRuns = filepath.Join("..", "rejudge", "testdata", "lab")
)

// runDir is the directory of the run gid a test names: its copy under devRuns for a dev host run or under labRuns
// for a lab host run kept as a fixture, else gate/results'.
func runDir(gid string) string {
	for _, d := range []string{devRuns, labRuns} {
		if fi, err := os.Stat(filepath.Join(d, gid)); err == nil && fi.IsDir() {
			return filepath.Join(d, gid)
		}
	}
	return filepath.Join(results, gid)
}

// shellTools is bash and jq 1.7.x, which TestRerenderTrap runs gate/lib.sh's trap lines with. Without them that part
// logs why and does not run; LAUG_TEST_SHELL=require turns that into a failure. jq --version prints
// jq-1.7 or jq-1.7.<n>.
type shellTools struct{ bash, jq string }

var findShell = sync.OnceValues(func() (shellTools, string) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		return shellTools{}, "no bash on PATH"
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		return shellTools{}, "no jq on PATH"
	}
	out, err := exec.Command(jq, "--version").Output()
	if err != nil {
		return shellTools{}, "jq --version failed: " + err.Error()
	}
	if v := strings.TrimSpace(string(out)); !regexp.MustCompile(`^jq-1\.7(\.|$)`).MatchString(v) {
		return shellTools{}, "jq is " + v + ", not 1.7.x"
	}
	return shellTools{bash: bash, jq: jq}, ""
})

// maybeShell is the shell tools, or the zero value, with the reason logged, when they are not here.
func maybeShell(t *testing.T) shellTools {
	t.Helper()
	tools, why := findShell()
	if why == "" {
		return tools
	}
	if os.Getenv("LAUG_TEST_SHELL") == "require" {
		t.Fatalf("LAUG_TEST_SHELL=require: %s", why)
	}
	t.Logf("not checked against the shell: %s", why)
	return shellTools{}
}

// mkrun makes a run directory from files: a path ending in / is a directory, content starting "-> " a symlink to
// the rest, and any other content a file's bytes.
func mkrun(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for _, p := range slices.Sorted(maps.Keys(files)) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		content := files[p]
		var err error
		switch target, link := strings.CutPrefix(content, "-> "); {
		case strings.HasSuffix(p, "/"):
			err = os.MkdirAll(full, 0o755)
		case link:
			err = os.Symlink(target, full)
		default:
			err = os.WriteFile(full, []byte(content), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestGather reads made runs, one rule a case: absent and unreadable files, the item directories and their order,
// the gid, the empty directories and the stream's own limits. Each case names the members it checks, in `jq -c`'s
// form, and every other top-level member must be null. With jq 1.7.x present, jq must read each case's gathered text
// as Gather does, or fail where Gather gives a StreamError.
func TestGather(t *testing.T) {
	const gate = `{"gate_id":"g1"}`
	item := func(name string) map[string]string {
		return map[string]string{
			name + "/lease.json":           `{"cell":"` + name + `"}`,
			name + "/exit.json":            `{"exit":0}`,
			name + "/g1-" + name + ".json": `{"own":"` + name + `"}`,
			name + "/load.exit":            "0\n",
			name + "/.lease.json":          "not json",
			name + "/item.log":             "a log",
			name + "/known-slow.json":      "not json",
		}
	}
	join := func(ms ...map[string]string) map[string]string {
		out := map[string]string{}
		for _, m := range ms {
			maps.Copy(out, m)
		}
		return out
	}
	deep := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	const s1 = `{"lease":{"cell":"s1"},"exit":{"exit":0},"run":{"own":"s1"},"verdict":null}`
	cases := []struct {
		name   string
		files  map[string]string
		v1     bool
		empty  []string
		mode0  string // a file made unreadable
		want   map[string]string
		gid    string
		stream bool   // a *StreamError
		err    string // another error, by its text
	}{
		{name: "absent", files: map[string]string{}, want: map[string]string{"dirs": `{}`}},
		{name: "empty", files: map[string]string{"gate.json": "", "poll.jsonl": ""},
			want: map[string]string{"gate": `"unreadable"`, "polls": `[]`, "dirs": `{}`}},
		{name: "white space only", files: map[string]string{"clock.json": " \n\t\n", "poll.jsonl": "\n\n"},
			want: map[string]string{"clock": `"unreadable"`, "polls": `[]`, "dirs": `{}`}},
		{name: "two values", files: map[string]string{"clock.json": `{"a":1} {"a":2}`,
			"poll.jsonl": "{\"t\":1}\n{\"t\":2}\n"},
			want: map[string]string{"clock": `"unreadable"`, "polls": `[{"t":1},{"t":2}]`, "dirs": `{}`}},
		{name: "a parse error", files: map[string]string{"plan.json": `{"a":`, "poll.jsonl": "{\"t\":1}\nnot json\n"},
			want: map[string]string{"plan": `"unreadable"`, "polls": `"unreadable"`, "dirs": `{}`}},
		{name: "one value keeps its text and key order",
			files: map[string]string{"thresholds.json": "\xef\xbb\xbf{ \"b\": 8.460, \"a\": 1e-05, \"b\": [1E2] }\n",
				"harness.json": "\"a\xffb\""},
			want: map[string]string{"thresholds": `{"b":[1E+2],"a":0.00001}`, "harness": "\"a\ufffdb\"",
				"dirs": `{}`}},
		{name: "a directory where a file goes", files: map[string]string{"thresholds.json/": ""}, stream: true},
		{name: "a directory where poll.jsonl goes", files: map[string]string{"poll.jsonl/": ""}, stream: true},
		{name: "a file without read permission", files: map[string]string{"harness.json": "{}"},
			mode0: "harness.json", stream: true},
		{name: "symlinks are followed",
			files: map[string]string{"harness.json": "-> other.json", "other.json": `{"h":1}`,
				"real/lease.json": `{"l":1}`, "lnk": "-> real"},
			want: map[string]string{"harness": `{"h":1}`,
				"dirs": `{"lnk":{"lease":{"l":1},"exit":null,"run":null,"verdict":null},` +
					`"real":{"lease":{"l":1},"exit":null,"run":null,"verdict":null}}`}},
		{name: "a dangling symlink is absent",
			files: map[string]string{"gate.json": gate, "clock.json": "-> nowhere", "s1/lease.json": "-> nowhere",
				"gone": "-> nowhere"},
			gid: "g1",
			want: map[string]string{"gate": gate,
				"dirs": `{"s1":{"lease":null,"exit":null,"run":null,"verdict":null}}`}},
		{name: "dot names and files are not item directories",
			files: map[string]string{".steps/": "", ".hidden/lease.json": "{}", ".state.json": "{}", "file": "{}",
				"verdict.json": "{}"},
			want: map[string]string{"dirs": `{}`}},
		{name: "item directories in the glob's byte order",
			files: map[string]string{"a/": "", "a-b/": "", "a.b/": "", "B/": "", "a b/": "", "ab/": "", "a!/": ""},
			want: map[string]string{"dirs": `{` +
				`"B":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"a b":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"a!":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"a-b":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"a.b":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"a":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"ab":{"lease":null,"exit":null,"run":null,"verdict":null}}`}},
		{name: "a gid reads the run and verdict files, and nothing else",
			files: join(map[string]string{"gate.json": gate, "timeline.jsonl": "x", "cleanup.json": "x",
				"verdict.md": "x", "gate.log": "x", "s1/g1-s1.verdict.json": `{"v":1}`}, item("s1")),
			gid: "g1",
			want: map[string]string{"gate": gate,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":{"own":"s1"},"verdict":{"v":1}}}`}},
		{name: "a gid that is not a name reads neither",
			files: join(map[string]string{"gate.json": `{"gate_id":"../g1"}`}, item("s1")),
			want: map[string]string{"gate": `{"gate_id":"../g1"}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "a gid loses trailing newlines",
			files: join(map[string]string{"gate.json": `{"gate_id":"g1\n\n"}`}, item("s1")),
			gid:   "g1",
			want:  map[string]string{"gate": `{"gate_id":"g1\n\n"}`, "dirs": `{"s1":` + s1 + `}`}},
		{name: "a gid loses NUL bytes",
			files: join(map[string]string{"gate.json": `{"gate_id":"g\u00001"}`}, item("s1")),
			gid:   "g1",
			want:  map[string]string{"gate": `{"gate_id":"g\u00001"}`, "dirs": `{"s1":` + s1 + `}`}},
		{name: "gate_id true is the gid true",
			files: join(map[string]string{"gate.json": `{"gate_id":true}`, "s1/true-s1.json": `{"t":1}`}, item("s1")),
			gid:   "true",
			want: map[string]string{"gate": `{"gate_id":true}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":{"t":1},"verdict":null}}`}},
		{name: "a number's text is the gid",
			files: join(map[string]string{"gate.json": `{"gate_id":1.50}`, "s1/1.50-s1.json": `{"n":1}`}, item("s1")),
			gid:   "1.50",
			want: map[string]string{"gate": `{"gate_id":1.50}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":{"n":1},"verdict":null}}`}},
		{name: "a number in exponent form is no gid",
			files: join(map[string]string{"gate.json": `{"gate_id":1e5}`, "s1/1E+5-s1.json": `{"n":1}`}, item("s1")),
			want: map[string]string{"gate": `{"gate_id":1E+5}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "gate_id false is no gid",
			files: join(map[string]string{"gate.json": `{"gate_id":false}`, "s1/false-s1.json": `{}`}, item("s1")),
			want: map[string]string{"gate": `{"gate_id":false}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "gate_id null is no gid",
			files: join(map[string]string{"gate.json": `{"gate_id":null}`, "s1/null-s1.json": `{}`}, item("s1")),
			want: map[string]string{"gate": `{"gate_id":null}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "gate_id an array is no gid",
			files: join(map[string]string{"gate.json": `{"gate_id":["g1"]}`}, item("s1")),
			want: map[string]string{"gate": `{"gate_id":["g1"]}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "no gate_id is no gid",
			files: join(map[string]string{"gate.json": `{"id":"g1"}`}, item("s1")),
			want: map[string]string{"gate": `{"id":"g1"}`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "gate.json not an object is no gid",
			files: join(map[string]string{"gate.json": `["g1"]`}, item("s1")),
			want: map[string]string{"gate": `["g1"]`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "gate.json unreadable is no gid",
			files: join(map[string]string{"gate.json": gate + gate}, item("s1")),
			want: map[string]string{"gate": `"unreadable"`,
				"dirs": `{"s1":{"lease":{"cell":"s1"},"exit":{"exit":0},"run":null,"verdict":null}}`}},
		{name: "an item name loses trailing newlines",
			files: join(map[string]string{"gate.json": gate}, map[string]string{"s1\n/g1-s1.json": `{"nl":1}`}),
			gid:   "g1",
			want: map[string]string{"gate": gate,
				"dirs": `{"s1":{"lease":null,"exit":null,"run":{"nl":1},"verdict":null}}`}},
		{name: "two names with one key: the first place, the last value",
			files: join(map[string]string{"gate.json": gate, "s1\n/lease.json": `{"nl":1}`, "s0/": ""}, item("s1")),
			gid:   "g1",
			want: map[string]string{"gate": gate,
				"dirs": `{"s0":{"lease":null,"exit":null,"run":null,"verdict":null},"s1":` + s1 + `}`}},
		{name: "an item name that is not UTF-8", files: map[string]string{"s\xff/": ""},
			err: `entry "s\xff": its name is not UTF-8`},
		{name: "v1 does not read thresholds-incumbent.json", v1: true,
			files: map[string]string{"thresholds-incumbent.json": `{"i":1}`, "thresholds.json": `{"t":1}`},
			want:  map[string]string{"thresholds": `{"t":1}`, "dirs": `{}`}},
		{name: "v2 does", files: map[string]string{"thresholds-incumbent.json": `{"i":1}`},
			want: map[string]string{"thresholds_incumbent": `{"i":1}`, "dirs": `{}`}},
		{name: "an empty directory in memory takes its place",
			files: join(map[string]string{"gate.json": gate, "pl/": "", "t/": ""}, item("s1")),
			empty: []string{"steady"}, gid: "g1",
			want: map[string]string{"gate": gate, "dirs": `{` +
				`"pl":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"s1":` + s1 + `,` +
				`"steady":{"lease":null,"exit":null,"run":null,"verdict":null},` +
				`"t":{"lease":null,"exit":null,"run":null,"verdict":null}}`}},
		{name: "an empty directory the run holds", files: map[string]string{"steady/": ""},
			empty: []string{"steady"}, err: `empty directory "steady": the run holds that name already`},
		{name: "an empty directory by a file's name", files: map[string]string{"steady": "-> nowhere"},
			empty: []string{"steady"}, err: `empty directory "steady": the run holds that name already`},
		{name: "an empty directory explain would not make", empty: []string{"../x"},
			err: `empty directory "../x": not a name laug gate rejudge makes again`},
		{name: "nesting jq reads in the gathered object", files: map[string]string{"gate.json": deep(254),
			"s1/lease.json": deep(250)},
			want: map[string]string{"gate": deep(254),
				"dirs": `{"s1":{"lease":` + deep(250) + `,"exit":null,"run":null,"verdict":null}}`}},
		{name: "nesting jq reads alone but not in the gathered object", files: map[string]string{"gate.json": deep(255)},
			stream: true},
		{name: "nesting past the limit in an item directory", files: map[string]string{"s1/exit.json": deep(251)},
			stream: true},
		{name: "nesting jq does not read alone", files: map[string]string{"gate.json": deep(257)},
			want: map[string]string{"gate": `"unreadable"`, "dirs": `{}`}},
	}
	jq := maybeJQ(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			mkrun(t, dir, c.files)
			if c.mode0 != "" {
				if os.Geteuid() == 0 {
					t.Skip("root reads a file without read permission")
				}
				if err := os.Chmod(filepath.Join(dir, c.mode0), 0); err != nil {
					t.Fatal(err)
				}
			}
			v := mustNamed(t, "v2")
			if c.v1 {
				v = mustNamed(t, "v1")
			}
			opts := Options{Arm: v.Arm, EmptyDirs: c.empty}
			in, err := Gather(os.DirFS(dir), opts)
			var se *StreamError
			switch {
			case c.err != "":
				if err == nil || !strings.HasPrefix(err.Error(), c.err) {
					t.Fatalf("Gather: %v, want an error starting %q", err, c.err)
				}
			case c.stream:
				if !errors.As(err, &se) {
					t.Fatalf("Gather: %v, want a StreamError", err)
				}
			case err != nil:
				t.Fatalf("Gather: %v", err)
			default:
				checkMembers(t, in.Value, v.Arm, c.want)
				if in.Gid != c.gid {
					t.Errorf("Gid = %q, want %q", in.Gid, c.gid)
				}
			}
			if jq == "" || c.err != "" {
				return
			}
			jqReads(t, jq, dir, opts, err)
		})
	}
}

func mustNamed(t *testing.T, name string) Version {
	t.Helper()
	v, ok := Named(name)
	if !ok {
		t.Fatalf("no version %s", name)
	}
	return v
}

// checkMembers holds the gathered object to its keys, in gather()'s order, and each member to want, or to null.
func checkMembers(t *testing.T, v jsonx.Value, arm bool, want map[string]string) {
	t.Helper()
	keys := []string{"gate", "clock", "plan", "thresholds", "thresholds_incumbent", "harness", "polls", "dirs"}
	if !arm {
		keys = slices.Delete(keys, 4, 5)
	}
	o, ok := v.Obj()
	if !ok || !slices.Equal(o.Keys(), keys) {
		t.Fatalf("gathered %s, want an object with the keys %v", v, keys)
	}
	for _, k := range keys {
		w, ok := want[k]
		if !ok {
			w = "null"
		}
		if m, _ := o.Get(k); m.String() != w {
			t.Errorf("%s = %s\nwant %s", k, m, w)
		}
	}
	for k := range want {
		if !slices.Contains(keys, k) {
			t.Errorf("want names %s, which the object does not hold", k)
		}
	}
}

// jqReads holds jq's read of the text Gather gathers from dir to Gather's own: jq -c . prints what Gather reads, or
// fails where Gather gives a StreamError.
func jqReads(t *testing.T, jq, dir string, opts Options, gerr error) {
	t.Helper()
	got, _, err := gatherText(os.DirFS(dir), opts)
	if err != nil {
		t.Fatalf("gatherText: %v", err)
	}
	cmd := exec.Command(jq, "-c", ".")
	cmd.Stdin = bytes.NewReader(got)
	out, err := cmd.Output()
	var se *StreamError
	switch {
	case errors.As(gerr, &se):
		if err == nil {
			t.Errorf("Gather gives a StreamError, and jq reads the text: %s", out)
		}
	case err != nil:
		t.Errorf("jq cannot read the text Gather reads: %v", err)
	default:
		in, gerr := Gather(os.DirFS(dir), opts)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if got := jsonx.MarshalCompact(in.Value); !bytes.Equal(got, out) {
			t.Errorf("jq reads the text as\n%s\nGather as\n%s", out, got)
		}
	}
}

// TestGatherNoRunDir: a run directory that is not there, or is a file, is ErrNoRunDir, refused before Gather
// reads a file.
func TestGatherNoRunDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "file")} {
		if _, err := Gather(os.DirFS(p), Options{Arm: true}); !errors.Is(err, ErrNoRunDir) {
			t.Errorf("Gather(%s): %v, want ErrNoRunDir", p, err)
		}
	}
}

// recordedVersion is the version a committed run's gate.json records: by its gate/combine.sh sha256, or with none by
// the version its internal/judge entry names, as a run bin/laug judge judged records it (internal/rejudge's
// ReadRecorded reads the two the same way).
func recordedVersion(t *testing.T, run string) Version {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir(run), "gate.json"))
	if err != nil {
		t.Fatal(err)
	}
	sha := RecordedSHA256(data)
	if sha == "" {
		name := entryVersion(data)
		v, ok := Named(name)
		if !ok {
			t.Fatalf("%s: gate.json records no gate/combine.sh sha256, and internal/judge %q, no version here", run,
				name)
		}
		return v
	}
	v, ok := Lookup(sha)
	if !ok {
		t.Fatalf("%s: gate.json records gate/combine.sh %q, no version here", run, sha)
	}
	return v
}

// entryVersion is the version string of the first internal/judge entry in gate.json's inputs, "" for none.
func entryVersion(gateJSON []byte) string {
	v, err := jsonx.Parse(gateJSON)
	if err != nil {
		return ""
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
		if p, _ := eo.Get("path"); !jsonx.Equal(p, jsonx.Str("internal/judge")) {
			continue
		}
		ver, _ := eo.Get("version")
		s, _ := ver.Str()
		return s
	}
	return ""
}

// watchFS records what is done to an fs.FS. It implements fs.StatFS and fs.ReadDirFS, so Open is only ever a read.
type watchFS struct {
	fsys fs.FS
	mu   sync.Mutex
	open []string
	stat []string
	list []string
}

func (w *watchFS) Open(name string) (fs.File, error) {
	w.mu.Lock()
	w.open = append(w.open, name)
	w.mu.Unlock()
	return w.fsys.Open(name)
}

func (w *watchFS) Stat(name string) (fs.FileInfo, error) {
	w.mu.Lock()
	w.stat = append(w.stat, name)
	w.mu.Unlock()
	return fs.Stat(w.fsys, name)
}

func (w *watchFS) ReadDir(name string) ([]fs.DirEntry, error) {
	w.mu.Lock()
	w.list = append(w.list, name)
	w.mu.Unlock()
	return fs.ReadDir(w.fsys, name)
}

// TestGatherReadsOnly gathers each committed run, and a made run that holds every file the judge never reads,
// through a watchFS, and fails on any read of a file gather() does not read: gate.json, clock.json, plan.json,
// thresholds.json, thresholds-incumbent.json (Arm only), harness.json, poll.jsonl, and in each item directory
// lease.json, exit.json, <gid>-<key>.json and <gid>-<key>.verdict.json. It lists only the run directory, and stats
// only it, its entries and those files. The list of what may be read is made here from the directory, not by Gather.
func TestGatherReadsOnly(t *testing.T) {
	made := t.TempDir()
	mkrun(t, made, map[string]string{
		"gate.json": `{"gate_id":"g1"}`, "clock.json": "{}", "plan.json": "{}", "thresholds.json": "{}",
		"thresholds-incumbent.json": "{}", "harness.json": "{}", "poll.jsonl": "{}\n",
		"timeline.jsonl": "{}\n", "cleanup.json": "{}", "verdict.json": "{}", "verdict.md": "x", "gate.log": "x",
		".state.json": "{}", ".steps/1": "x", ".harness": "x", "omes-build.log": "x",
		"s1/lease.json": "{}", "s1/exit.json": "{}", "s1/g1-s1.json": "{}", "s1/g1-s1.verdict.json": "{}",
		"s1/load.exit": "0", "s1/.lease.json": "{}", "s1/item.log": "x", "s1/known-slow.json": "{}",
		"s1/g1-pl.json": "{}", "s1/sub/lease.json": "{}",
		"pl/lease.json": "{}", "pl/exit.json": "{}", "pl/g1-pl.json": "{}",
	})
	type run struct {
		name, dir string
		v         Version
		empty     []string
	}
	var runs []run
	for _, name := range []string{"v1", "v2"} {
		runs = append(runs, run{"made, " + name, made, mustNamed(t, name), nil})
	}
	runs = append(runs, run{"made, with an empty steady", made, mustNamed(t, "v2"), []string{"steady"}})
	for _, name := range committedRuns(t) {
		runs = append(runs, run{name, filepath.Join(results, name), recordedVersion(t, name), nil})
	}
	for _, r := range runs {
		w := &watchFS{fsys: os.DirFS(r.dir)}
		in, err := Gather(w, Options{Arm: r.v.Arm, EmptyDirs: r.empty})
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		may := map[string]bool{"gate.json": true, "clock.json": true, "plan.json": true, "thresholds.json": true,
			"harness.json": true, "poll.jsonl": true}
		if r.v.Arm {
			may["thresholds-incumbent.json"] = true
		}
		var gid string
		if data, err := os.ReadFile(filepath.Join(r.dir, "gate.json")); err == nil {
			g, _ := jsonx.Parse(data)
			o, _ := g.Obj()
			if id, ok := o.Get("gate_id"); ok {
				gid, _ = id.Str()
			}
		}
		if gid == "" || in.Gid != gid {
			t.Fatalf("%s: Gid %q, gate.json's gate_id %q", r.name, in.Gid, gid)
		}
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			t.Fatal(err)
		}
		statable := map[string]bool{".": true}
		for _, name := range r.empty {
			statable[name] = true
		}
		items := 0
		for _, e := range entries {
			statable[e.Name()] = true
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			items++
			for _, f := range []string{"lease.json", "exit.json", gid + "-" + e.Name() + ".json",
				gid + "-" + e.Name() + ".verdict.json"} {
				may[e.Name()+"/"+f] = true
			}
		}
		for f := range may {
			statable[f] = true
		}
		for _, name := range w.open {
			if !may[name] {
				t.Errorf("%s: opened %s, which gather() never reads", r.name, name)
			}
		}
		for _, name := range w.stat {
			if !statable[name] {
				t.Errorf("%s: stat %s, which gather() never looks at", r.name, name)
			}
		}
		if !slices.Equal(w.list, []string{"."}) {
			t.Errorf("%s: listed %v, want only the run directory", r.name, w.list)
		}
		if len(w.open) == 0 {
			t.Errorf("%s: read nothing", r.name)
		}
		o, _ := in.Value.Obj()
		dirs, _ := o.Get("dirs")
		if d, _ := dirs.Obj(); d.Len() != items+len(r.empty) {
			t.Errorf("%s: %d item directories gathered, the run holds %d and %d empty", r.name, d.Len(), items,
				len(r.empty))
		}
	}
	t.Logf("%d runs gathered, each reading only gather()'s files", len(runs))
}

// TestEveryCommittedRunGathers is the done-when of the reader and the registry: every run in gate/results records a
// gate/combine.sh sha256 that resolves to a version, 52 v1, 84 v2 and 1 v2-r7off, or an internal/judge entry that
// names one, 21 v3, as the gates rows count them, and gathers under it with its own name as gid; the runs here count
// by version as their rows do.
func TestEveryCommittedRunGathers(t *testing.T) {
	count := map[string]int{}
	for _, run := range committedRuns(t) {
		v := recordedVersion(t, run)
		in, err := Gather(os.DirFS(filepath.Join(results, run)), Options{Arm: v.Arm})
		if err != nil {
			t.Errorf("%s: %v", run, err)
			continue
		}
		if in.Gid != run {
			t.Errorf("%s: Gid %q", run, in.Gid)
		}
		count[v.Name]++
	}
	cs := census(t)
	all, here := map[string]int{}, map[string]int{}
	for _, id := range cs.IDs {
		v := rowVersion(t, cs.Rows[id]).Name
		all[v]++
		if cs.IsHere(id) {
			here[v]++
		}
	}
	if want := map[string]int{"v1": 52, "v2": 84, "v2-r7off": 1, "v3": 21}; fmt.Sprint(all) != fmt.Sprint(want) {
		t.Errorf("the gates rows by version: %v, want %v", all, want)
	}
	if fmt.Sprint(count) != fmt.Sprint(here) {
		t.Errorf("runs by version: %v, their gates rows %v", count, here)
	}
	t.Logf("runs by version: %v", count)
}
