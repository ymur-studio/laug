package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/rejudge"
)

// TestJudge drives laug judge: on committed runs under each way of naming the version, its default text, -json and -md;
// -write on a copy, whose stdout and files are Judge's; each refusal; -classify; and -identity in and outside
// a checkout. The default version is judge.Current, v3, which gives v2run its committed bytes (it holds no -0 d and no
// wave reason) and zeroRun its committed verdict.md with the -0 d as 0.000. v1run, a dev host run, is named by the path
// of its copy (runPath), as are zeroRun, trapRun and v3run, lab host runs kept as fixtures. A run that records another
// judge than the one judging gets a stderr line that names it, and -write refuses it, and a trap verdict (trapRun's),
// unless -overwrite-verdict is given.
func TestJudge(t *testing.T) {
	root := repoRoot(t)
	const v2run, v1run, zeroRun, trapRun = "g20261018-092540", "g20261014-215905", "g20261017-192202",
		"g20261017-014142"
	// notice is the stderr line for a run that records judge rec, judged under v3, the default.
	notice := func(rec string) string {
		return "laug · judge: judged under v3, the current judge; gate.json records " + rec + ", and -recorded " +
			"judges under " + rec + "\n"
	}
	judged := func(t *testing.T, gid, version string) *judge.Result {
		t.Helper()
		v, _ := judge.Named(version)
		res, err := judge.Judge(os.DirFS(filepath.Join(root, runPath(gid))), v, judge.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	committed := func(gid, name string) string {
		return readText(t, filepath.Join(root, runPath(gid), name))
	}
	compact := func(js string) string {
		v, err := jsonx.Parse([]byte(js))
		if err != nil {
			t.Fatal(err)
		}
		return string(jsonx.MarshalCompact(v))
	}
	res := judged(t, v2run, judge.Current)
	zeroMD := committed(zeroRun, "verdict.md")
	if !strings.Contains(zeroMD, " | 0.0-0 | ") {
		t.Fatalf("%s's verdict.md holds no 0.0-0", zeroRun)
	}
	// A run bin/laug judge judged in boot 4, the first of 21 that record v3, by its copy (runPath).
	const v3run = "g20261024-085719"
	if v, ok := rejudge.ReadRecorded([]byte(committed(v3run, "gate.json"))).Version(); !ok || v.Name != "v3" {
		t.Fatalf("%s records judge %q, not v3", v3run, v.Name)
	}
	for _, c := range []struct {
		name string
		args []string
		code int
		out  string
		err  string
	}{
		{"a v2 run", []string{v2run}, 1, string(res.Summary()) + "not written      judge v3 in Go; -write refuses: " +
			"gate.json records v2\nBLOCK\n", notice("v2")},
		{"a v3 run", []string{runPath(v3run)}, judged(t, v3run, "v3").Exit(), string(judged(t, v3run,
			"v3").Summary()) + "not written      judge v3 in Go; -write writes " + runPath(v3run) + "/verdict.json, " +
			runPath(v3run) + "/verdict.md\n" + judged(t, v3run, "v3").Verdict + "\n", ""},
		{"a trap verdict", []string{runPath(trapRun), "-recorded"}, 1, string(judged(t, trapRun, "v1").Summary()) +
			"not written      judge v1 in Go; -write refuses: verdict.json is a trap verdict\nBLOCK\n", ""},
		{"a d of -0", []string{runPath(zeroRun), "-md"}, 0, strings.Replace(zeroMD, " | 0.0-0 | ", " | 0.000 | ", 1),
			notice("v2")},
		{"a d of -0, -recorded", []string{runPath(zeroRun), "-md", "-recorded"}, 0, zeroMD, ""},
		{"a d of -0, -version v2", []string{runPath(zeroRun), "-md", "-version", "v2"}, 0, zeroMD, ""},
		{"-json", []string{v2run, "-json"}, 1, compact(committed(v2run, "verdict.json")), notice("v2")},
		{"-md", []string{"gate/results/" + v2run, "-md"}, 1, committed(v2run, "verdict.md"), notice("v2")},
		{"-recorded on v1", []string{runPath(v1run), "-recorded", "-md"}, 1, committed(v1run, "verdict.md"), ""},
		{"-version v1", []string{runPath(v1run), "-version", "v1", "-json"}, 1,
			compact(committed(v1run, "verdict.json")), ""},
		{"v2 on a v1 run", []string{runPath(v1run), "-json"}, 1, string(jsonx.MarshalCompact(mustParseJSON(t,
			judged(t, v1run, "v2").JSON))), notice("v1")},
		{"-version v2 on a v1 run", []string{runPath(v1run), "-version", "v2", "-json"}, 1,
			string(jsonx.MarshalCompact(mustParseJSON(t, judged(t, v1run, "v2").JSON))), "laug · judge: judged " +
				"under v2, as -version asks; gate.json records v1, and -recorded judges under v1\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runApp(t, &App{Root: root}, append([]string{"judge"}, c.args...)...)
			if code != c.code || out != c.out || errOut != c.err {
				t.Errorf("exit %d, want %d\n got:\n%s\nwant:\n%s\nstderr:\n%s\nwant:\n%s", code, c.code, out, c.out,
					errOut, c.err)
			}
		})
	}
	if committed(v1run, "verdict.json") == string(judged(t, v1run, "v2").JSON) {
		t.Error("v2 judges the v1 run as v1 did, so the case tells the versions apart in nothing")
	}

	t.Run("-write", func(t *testing.T) {
		// v3 on a v2 run, which -write refuses without -overwrite-verdict; v3 gives v2run its committed bytes.
		dir := filepath.Join(t.TempDir(), "run")
		copyRun(t, v2run, dir)
		code, out, errOut := runApp(t, &App{Root: root}, "judge", "-write", "-overwrite-verdict", dir)
		want := res.Stdout(dir)
		wantErr := notice("v2") + "laug · judge: -overwrite-verdict replaces 2 files in " + dir + ": verdict.json " +
			"(BLOCK) and verdict.md\n"
		if code != 1 || out != string(want) || errOut != wantErr {
			t.Errorf("exit %d\n got:\n%s\nwant:\n%s\nstderr:\n%s\nwant:\n%s", code, out, want, errOut, wantErr)
		}
		for _, f := range []string{"verdict.json", "verdict.md"} {
			fi, err := os.Stat(filepath.Join(dir, f))
			if err != nil || readText(t, filepath.Join(dir, f)) != committed(v2run, f) || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v, mode %v, or not the committed bytes", f, err, fi.Mode())
			}
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".verdict.*")); len(left) != 0 {
			t.Errorf("temp files left: %q", left)
		}
	})

	t.Run("-recorded, an internal/judge entry", func(t *testing.T) {
		// A run bin/laug judge -write judged records the internal/judge entry in place of gate/combine.sh (gate/lib.sh,
		// judge_input); -recorded takes its version, here v1 on a v1 run, which gives the run's own verdict.md.
		dir := filepath.Join(t.TempDir(), "e")
		copyRun(t, v1run, dir)
		writeFile(t, filepath.Join(dir, "gate.json"), strings.Replace(readText(t, filepath.Join(dir, "gate.json")),
			`"path": "gate/combine.sh",`, `"path": "internal/judge", "version": "v1",`, 1))
		code, out, errOut := runApp(t, &App{Root: root}, "judge", dir, "-recorded", "-md")
		if code != judged(t, v1run, "v1").Exit() || out != committed(v1run, "verdict.md") || errOut != "" {
			t.Errorf("exit %d\n%s\nstderr: %s", code, out, errOut)
		}
	})

	t.Run("-write keeps a recorded verdict", func(t *testing.T) {
		// A committed verdict that another judge wrote, or the gate's trap verdict, stays as it is: exit 2, the
		// refusal on stderr, nothing on stdout, the files as they were. -recorded writes the run's own judge's bytes.
		for _, c := range []struct {
			name, gid string
			args      []string
			err       string
		}{
			{"another judge", v2run, nil, "gate.json records v2, and this judge is v3; -recorded judges under v2; " +
				"-overwrite-verdict replaces the run's verdict"},
			{"another judge, by -version", v2run, []string{"-version", "v1"}, "gate.json records v2, and this judge " +
				"is v1; -recorded judges under v2; -overwrite-verdict replaces the run's verdict"},
			{"a trap verdict", trapRun, []string{"-recorded"}, "verdict.json is the trap verdict of a gate stopped " +
				"at step wave, which no judge gives back; -overwrite-verdict replaces the run's verdict"},
			{"a trap verdict, under v3", trapRun, nil, "verdict.json is the trap verdict of a gate stopped at step " +
				"wave, which no judge gives back; -overwrite-verdict replaces the run's verdict"},
		} {
			t.Run(c.name, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "run")
				copyRun(t, c.gid, dir)
				code, out, errOut := runApp(t, &App{Root: root}, append([]string{"judge", "-write", dir},
					c.args...)...)
				want := "laug · judge: " + dir + ": no verdict written: " + c.err + "\n"
				if code != 2 || out != "" || errOut != want {
					t.Errorf("exit %d\nstdout %q\nstderr:\n%s\nwant:\n%s", code, out, errOut, want)
				}
				for _, f := range []string{"verdict.json", "verdict.md"} {
					if readText(t, filepath.Join(dir, f)) != committed(c.gid, f) {
						t.Errorf("%s changed", f)
					}
				}
				if left, _ := filepath.Glob(filepath.Join(dir, ".verdict.*")); len(left) != 0 {
					t.Errorf("temp files left: %q", left)
				}
			})
		}
		dir := filepath.Join(t.TempDir(), "v1")
		copyRun(t, v1run, dir)
		code, _, errOut := runApp(t, &App{Root: root}, "judge", "-write", "-recorded", dir)
		if code != 1 || errOut != "" || readText(t, filepath.Join(dir, "verdict.md")) != committed(v1run, "verdict.md") {
			t.Errorf("-write -recorded on a v1 run: exit %d, stderr %q, or not its own verdict.md", code, errOut)
		}
		// A judge no version names, as make harness-test's rejudge cases record, is written over, with the line.
		odd := filepath.Join(t.TempDir(), "odd")
		copyRun(t, v2run, odd)
		writeFile(t, filepath.Join(odd, "gate.json"), strings.Replace(readText(t, filepath.Join(odd, "gate.json")),
			"33b4e89dc2ecef502e73ea88136a482fbf8694986789712fd5126e31f5faf6f0", strings.Repeat("0", 64), 1))
		code, out, errOut := runApp(t, &App{Root: root}, "judge", "-write", odd)
		wantErr := "laug · judge: judged under v3, the current judge; gate.json records gate/combine.sh sha256 " +
			strings.Repeat("0", 12) + ", which internal/judge/versions.txt does not name\n"
		if code != 1 || out != string(res.Stdout(odd)) || errOut != wantErr {
			t.Errorf("-write, a judge no version names: exit %d\n%s\nstderr %q, want %q", code, out, errOut, wantErr)
		}
	})

	t.Run("refused", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "bad")
		copyRun(t, v2run, dir)
		clock := readText(t, filepath.Join(dir, "clock.json"))
		writeFile(t, filepath.Join(dir, "clock.json"), strings.Replace(clock, `"t0": {`, `"t0": "x", "was": {`, 1))
		v9 := filepath.Join(t.TempDir(), "v9")
		copyRun(t, v2run, v9)
		writeFile(t, filepath.Join(v9, "gate.json"), strings.Replace(readText(t, filepath.Join(v9, "gate.json")),
			`"path": "gate/combine.sh",`, `"path": "internal/judge", "version": "v9",`, 1))
		odd := filepath.Join(t.TempDir(), "odd")
		copyRun(t, v2run, odd)
		gate := readText(t, filepath.Join(odd, "gate.json"))
		writeFile(t, filepath.Join(odd, "gate.json"), strings.Replace(gate,
			"33b4e89dc2ecef502e73ea88136a482fbf8694986789712fd5126e31f5faf6f0", strings.Repeat("0", 64), 1))
		for _, c := range []struct {
			name string
			args []string
			err  string // stderr's first line
		}{
			{"no such run", []string{"g1"}, "laug · judge: no results directory at gate/results/g1"},
			{"the judge stops", []string{"-write", "-version", "v2", dir}, "laug · judge: judge v2 on " + dir + ": the " +
				"judge failed, so no verdict is written: jq: error: Cannot index string with string \"wall\""},
			{"-overwrite-verdict alone", []string{v2run, "-overwrite-verdict"}, "laug · judge: -overwrite-verdict " +
				"goes with -write"},
			{"-recorded, a judge no version names", []string{"-recorded", odd}, "laug · judge: -recorded: " + odd +
				": its gate.json records gate/combine.sh sha256 000000000000, which no version names; -version names " +
				"one of v1, v2, v2-r7off, v3"},
			{"-recorded, an internal/judge entry no version names", []string{"-recorded", v9}, "laug · judge: " +
				"-recorded: " + v9 + ": its gate.json records internal/judge version v9, which no version names; " +
				"-version names one of v1, v2, v2-r7off, v3"},
			{"-version v9", []string{v2run, "-version", "v9"}, "laug · judge: -version v9: no such judge version; " +
				"the versions are v1, v2, v2-r7off, v3"},
			{"-recorded and -version", []string{v2run, "-recorded", "-version", "v1"}, "laug · judge: -recorded and " +
				"-version each name the judge version; give one"},
			{"-json and -md", []string{v2run, "-json", "-md"}, "laug · judge: -json and -md each print a file; give one"},
			{"two runs", []string{v2run, v1run}, `laug · judge: takes one gate run, got ["` + v2run + `" "` + v1run + `"]`},
			{"-classify and a run", []string{"-classify", "x", v2run}, "laug · judge: -classify takes a reason and " +
				"nothing else"},
			{"-classify -json", []string{"-classify", "x", "-json"}, "laug · judge: -classify takes a reason and " +
				"nothing else"},
			{"-identity and a run", []string{"-identity", v2run}, "laug · judge: -identity takes -json and nothing else"},
			{"-identity -md", []string{"-identity", "-md"}, "laug · judge: -identity takes -json and nothing else"},
			{"bare", nil, "laug · judge: names no gate run: give it a gate run's directory, or a gate id in " +
				"gate/results; or -classify <reason>, or -identity"},
		} {
			t.Run(c.name, func(t *testing.T) {
				code, out, errOut := runApp(t, &App{Root: root}, append([]string{"judge"}, c.args...)...)
				if code != 2 || out != "" || firstLine(errOut) != c.err {
					t.Errorf("exit %d\nstdout %q\nstderr:\n%s\nwant it to start %q", code, out, errOut, c.err)
				}
			})
		}
		if readText(t, filepath.Join(dir, "verdict.json")) != committed(v2run, "verdict.json") {
			t.Error("a judge that stops wrote verdict.json")
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".verdict.*")); len(left) != 0 {
			t.Errorf("temp files left: %q", left)
		}
	})

	t.Run("-classify", func(t *testing.T) {
		for reason, class := range map[string]string{"worse than the incumbent: x": "regression",
			"incumbent: x": "invalid", "": "unknown", "harness: make harness-test exited 2": "harness"} {
			code, out, errOut := runApp(t, &App{}, "judge", "-classify", reason)
			if code != 0 || out != class+"\n" || out != string(judge.Classify(reason)) || errOut != "" {
				t.Errorf("-classify %q: exit %d, %q, want %q\n%s", reason, code, out, class, errOut)
			}
		}
	})

	t.Run("-identity", func(t *testing.T) {
		saved := commit
		t.Cleanup(func() { commit = saved })
		commit = headSHA
		id := judge.Self()
		r := judgeCheckout(t)
		bin := filepath.Join(t.TempDir(), "bin")
		writeExec(t, filepath.Join(bin, "git"), gitStub)
		for _, c := range []struct {
			name, root string
			env        []string
			text, err  string
			clean      any
		}{
			{"in a checkout, clean", r, nil, "tree clean  yes: internal/judge and internal/jsonx hold this judge, " +
				"committed\n", "", true},
			{"in a checkout, changed", r, []string{"STUB_JUDGE_STATUS=?? internal/judge/x.go"}, "tree clean  no: " +
				"internal/judge and internal/jsonx have changes not committed: git status lists 1\n", "", false},
			{"outside a checkout", "", nil, "tree clean  not known\n", "laug · judge -identity: tree_clean is not " +
				"known: laug runs outside a checkout\n", nil},
			{"no git", r, []string{"PATH=/nowhere"}, "tree clean  not known\n", "laug · judge -identity: tree_clean " +
				"is not known: git status: no git on PATH, so whether internal/judge and internal/jsonx are HEAD's " +
				"is not known\n", nil},
		} {
			t.Run(c.name, func(t *testing.T) {
				env := append(c.env, "PATH="+bin)
				code, out, errOut := runApp(t, &App{Root: c.root, Environ: env}, "judge", "-identity")
				want := "laug · judge -identity: " + id.Version + ", sha256 " + id.SHA256 + ", " + id.Go + ", laug " +
					headSHA[:7] + "\n" + c.text
				if code != 0 || out != want || errOut != c.err {
					t.Errorf("exit %d\n got:\n%s\nwant:\n%s\nstderr %q, want %q", code, out, want, errOut, c.err)
				}
				code, out, _ = runApp(t, &App{Root: c.root, Environ: env}, "judge", "-identity", "-json")
				var doc map[string]any
				if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || strings.Count(out, "\n") != 1 {
					t.Fatalf("-json: exit %d, %v\n%s", code, err, out)
				}
				wantDoc := map[string]any{"version": id.Version, "sha256": id.SHA256, "go": id.Go, "commit": headSHA,
					"tree_clean": c.clean}
				if !jsonEqual(doc, wantDoc) || !strings.HasPrefix(out, `{"version":`) {
					t.Errorf("-json: %s", out)
				}
			})
		}
	})
}

func jsonEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func mustParseJSON(t *testing.T, data []byte) jsonx.Value {
	t.Helper()
	v, err := jsonx.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// repoRoot is this checkout's root, where gate/results holds the committed runs.
func repoRoot(t *testing.T) string {
	t.Helper()
	r, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// devRuns is the copy of the 7 dev host runs the tests name, from the checkout's root
// (internal/rejudge/testdata/runs/README.md); labRuns the copy of the lab host runs they name that are not kept in
// gate/results (internal/rejudge/testdata/lab/README.md).
const (
	devRuns = "internal/rejudge/testdata/runs"
	labRuns = "internal/rejudge/testdata/lab"
)

// runPath is the path from the checkout's root of the run gid a test names: its copy under devRuns for a dev host
// run or under labRuns for a lab host run kept as a fixture, else gate/results'.
func runPath(gid string) string {
	for _, d := range []string{devRuns, labRuns} {
		if fi, err := os.Stat(filepath.Join("..", "..", d, gid)); err == nil && fi.IsDir() {
			return d + "/" + gid
		}
	}
	return "gate/results/" + gid
}

// held makes judgeNotHeld answer why for the length of the test: "" holds the judge to HEAD's.
func held(t *testing.T, why string) {
	t.Helper()
	saved := judgeNotHeld
	t.Cleanup(func() { judgeNotHeld = saved })
	judgeNotHeld = func(_ context.Context, _ *App) string { return why }
}

// judgeCheckout is a checkout that holds the judge's sources as this one does, so that judge.SourcesIn reads there
// the sources this test binary embeds.
func judgeCheckout(t *testing.T) string {
	t.Helper()
	r := checkout(t)
	srcs, err := judge.SourcesIn(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		writeFile(t, filepath.Join(r, filepath.FromSlash(s.Path)), readText(t, filepath.Join(repoRoot(t),
			filepath.FromSlash(s.Path))))
	}
	return r
}

// copyRun copies the committed run gid to dst: its directories and regular files.
func copyRun(t *testing.T, gid, dst string) {
	t.Helper()
	src := filepath.Join(repoRoot(t), runPath(gid))
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	writeFile(t, path, readText(t, path)+text)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
