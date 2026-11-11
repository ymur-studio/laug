package main

// The test helpers the moved tests call, copied from cmd/laug's tests: the recorder (runner_test.go), the env matrix
// (daily_test.go), the built binary (binary_test.go), and a buffer, a checkout and a run (pool_test.go, main_test.go,
// gate_test.go, dryrun_test.go). Each keeps its cmd/laug text but for the roles, which this binary runs none of.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// TestMain removes the binary obsBinary built.
func TestMain(m *testing.M) {
	code := m.Run()
	if built != "" {
		os.RemoveAll(built)
	}
	os.Exit(code)
}

var (
	builtOnce sync.Once
	built     string
	buildErr  error
)

// obsBinary builds this package once into a temp dir, as make build builds bin/obs, and returns the dir.
func obsBinary(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		dir, err := os.MkdirTemp("", "obs-binary-")
		if err != nil {
			buildErr = err
			return
		}
		built = dir
		out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "obs"), ".").CombinedOutput()
		if err != nil {
			buildErr = errors.New("go build: " + err.Error() + "\n" + string(out))
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return built
}

// recorder is a Runner that records what it was handed, how, and what it read on stdin, and runs nothing. run, when
// set, stands in for a script Run starts: it writes what the script would and returns its exit code. execErr is what
// Exec returns.
type recorder struct {
	calls   []script.Command
	envs    [][]string
	hows    []script.How
	stdins  []string
	run     func(c script.Command, env []string, stdout, stderr io.Writer) int
	execErr error
	root    string
}

func (r *recorder) record(c script.Command, env []string, how script.How) {
	in := ""
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		in = string(b)
	}
	r.calls, r.envs, r.hows, r.stdins = append(r.calls, c), append(r.envs, env), append(r.hows, how),
		append(r.stdins, in)
}

func (r *recorder) Exec(c script.Command, env []string) error {
	r.record(c, env, script.Exec)
	return r.execErr
}

func (r *recorder) Run(c script.Command, env []string, stdout, stderr io.Writer) (int, error) {
	r.record(c, env, script.Run)
	if r.run != nil {
		return r.run(c, env, stdout, stderr), nil
	}
	return 0, nil
}

// strayEnviron is the caller's environment in every row of the env matrix: a stray FORCE, which the stack's verbs map,
// the login names, a name of Temporal's and a seam. No knob may reach a script from it, a verb names on stderr each
// stray it maps, and the seam passes through. cmd/laug's holds the daily path's knobs too.
var strayEnviron = []string{"FORCE=1", "USER=someone", "LOGNAME=someone", "TEMPORAL_ADDRESS=127.0.0.1:1",
	"FAKE_SEAM=1"}

// envCase is one row of a verb's env matrix.
type envCase struct {
	name  string
	args  []string // after bin/obs
	files map[string]string
	more  []string // the caller's environment beyond strayEnviron
	code  int
	// What the runner was handed first: how, the script and its arguments, and every knob in its environment,
	// NAME=value, in any order, $ROOT standing for the checkout in both. how 0 wants nothing handed over.
	how    script.How
	script string
	argv   []string
	knobs  []string
	strays []string // the knobs stderr names as strays
	out    string   // a line stdout holds, when not empty
	errOut string   // a line stderr holds, when not empty
}

// matrixApp is a row's App: a checkout holding the row's files; HOME a temp dir; strayEnviron; a recorder.
func matrixApp(t *testing.T, c envCase) (*App, *recorder) {
	t.Helper()
	root := checkout(t)
	for p, content := range c.files {
		writeFile(t, filepath.Join(root, p), content)
	}
	environ := append([]string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}, strayEnviron...)
	environ = append(environ, c.more...)
	rec := &recorder{root: root}
	return &App{Root: root, Environ: environ, Runner: rec}, rec
}

// strayLine is the line a stray gets, from a verb of one word or two ("obs up", "usage record").
var strayLine = regexp.MustCompile(`(?m)^laug · [a-z]+(?: [a-z-]+)?: ([A-Z][A-Z0-9_]*)=.* in the environment is ` +
	`not passed on; `)

// checkRow holds one row's run to the row.
func checkRow(t *testing.T, c envCase, rec *recorder, code int, out, errOut string) {
	t.Helper()
	if code != c.code {
		t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
	}
	if c.out != "" && !slices.Contains(strings.Split(out, "\n"), c.out) {
		t.Errorf("stdout lacks the line %q:\n%s", c.out, out)
	}
	if c.errOut != "" && !slices.Contains(strings.Split(errOut, "\n"), c.errOut) {
		t.Errorf("stderr lacks the line %q:\n%s", c.errOut, errOut)
	}
	var strays []string
	for _, m := range strayLine.FindAllStringSubmatch(errOut, -1) {
		strays = append(strays, m[1])
	}
	if !slices.Equal(strays, c.strays) {
		t.Errorf("strays named %q, want %q\nstderr:\n%s", strays, c.strays, errOut)
	}
	if c.how == 0 {
		if len(rec.calls) != 0 {
			t.Errorf("handed over %s, want nothing", rec.calls[0].Script)
		}
		return
	}
	if len(rec.calls) == 0 {
		t.Fatalf("nothing handed over, want %s\nstderr:\n%s", c.script, errOut)
	}
	got := rec.calls[0]
	var argv []string
	for _, v := range got.Args {
		argv = append(argv, v.Text)
	}
	root := strings.NewReplacer("$ROOT", got.Root)
	wantArgv := slices.Clone(c.argv)
	for i, w := range wantArgv {
		wantArgv[i] = root.Replace(w)
	}
	if rec.hows[0] != c.how || got.Script != c.script || !slices.Equal(argv, wantArgv) {
		t.Errorf("handed over %v %s %q, want %v %s %q", rec.hows[0], got.Script, argv, c.how, c.script, wantArgv)
	}
	var knobs []string
	for _, kv := range rec.envs[0] {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := script.Lookup(name); ok {
			knobs = append(knobs, kv)
		}
		if slices.Contains([]string{"USER", "LOGNAME"}, name) || kv == "TEMPORAL_ADDRESS=127.0.0.1:1" {
			t.Errorf("the script's environment holds %s", kv)
		}
	}
	want := slices.Clone(c.knobs)
	for i, k := range want {
		want[i] = root.Replace(k)
	}
	slices.Sort(knobs)
	slices.Sort(want)
	if !slices.Equal(knobs, want) {
		t.Errorf("the script's knobs:\n got %q\nwant %q", knobs, want)
	}
	if !slices.Contains(rec.envs[0], "FAKE_SEAM=1") {
		t.Errorf("the seam FAKE_SEAM did not pass through: %q", rec.envs[0])
	}
}

// syncBuffer is a buffer a verb in another goroutine writes while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// checkout is a temp dir that findRoot takes for a laug checkout.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "gate/run.sh"} {
		writeFile(t, filepath.Join(dir, f), "")
	}
	return dir
}

// waitUntil polls ok every 5 ms for 3 s at most.
func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s within 3 s", what)
		}
	}
}

// runArgs runs bin/obs with args on an App of two buffers and an empty environment.
func runArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), &App{Stdout: &stdout, Stderr: &stderr}, args)
	return code, stdout.String(), stderr.String()
}

// runApp runs bin/obs with args on a, its two streams made buffers.
func runApp(t *testing.T, a *App, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a.Stdout, a.Stderr = &stdout, &stderr
	code := run(context.Background(), a, args)
	return code, stdout.String(), stderr.String()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeExec writes an executable file.
func writeExec(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
