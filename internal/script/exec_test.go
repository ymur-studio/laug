package script

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the process TestExec execs a script from: with LAUG_SCRIPT_EXEC set to a checkout, it builds
// show.sh's environment from its own and calls Real.Exec, which returns only on failure.
func TestMain(m *testing.M) {
	if root := os.Getenv("LAUG_SCRIPT_EXEC"); root != "" {
		c := Command{Root: root, Script: "bin/show.sh", Args: []Value{{Text: "7"}, {Text: "a b"}},
			Set: []Var{Bare("RUN_ID", "r1")}, Clear: []string{"SERVER_TAG"}}
		if os.Getenv("LAUG_SCRIPT_TOOL") != "" {
			c.Script, c.Tool, c.Dir = "showtool", true, "bin"
		}
		env, _ := Env(os.Environ(), c)
		err := Real.Exec(c, env)
		fmt.Fprintf(os.Stderr, "exec returned: %v\n", err)
		os.Exit(99)
	}
	os.Exit(m.Run())
}

// show is a script that prints $0, its arguments, its working directory and its environment, then exits with its
// first argument.
const show = `#!/bin/sh
printf 'arg0 %s\n' "$0"
for a in "$@"; do printf 'arg %s\n' "$a"; done
printf 'pwd %s\n' "$(pwd -P)"
env
[ "$1" = signal ] && kill -TERM $$
exit "$1"
`

// checkout makes a directory with bin/show.sh in it and returns it, symlinks resolved.
func checkout(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "show.sh"), []byte(show), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// shown checks what show.sh printed: $0 as make ran it, the arguments, the checkout as the working directory, the
// names it should see and none it should not.
func shown(t *testing.T, out, root string, args, has, hasnt []string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	want := append([]string{"arg0 bin/show.sh"}, args...)
	want = append(want, "pwd "+root)
	if len(lines) < len(want) || !slices.Equal(lines[:len(want)], want) {
		t.Errorf("show.sh printed:\n%s\nwant it to start:\n%s", out, strings.Join(want, "\n"))
	}
	for _, kv := range has {
		if !slices.Contains(lines, kv) {
			t.Errorf("the script's environment lacks %s:\n%s", kv, out)
		}
	}
	for _, k := range hasnt {
		for _, l := range lines {
			if strings.HasPrefix(l, k+"=") {
				t.Errorf("the script's environment holds %s", l)
			}
		}
	}
}

// TestRun: Run runs the script from the checkout with the environment Env built, its stdout and stderr the ones
// given, and its exit code as it was: 0, 3, or 128+15 for a TERM.
func TestRun(t *testing.T) {
	root := checkout(t)
	parent := []string{"PATH=" + os.Getenv("PATH"), "SERVER_TAG=1.32.1", "RUN_ID=stray", "FAKE_X=1", "USER=someone"}
	for _, c := range []struct {
		arg  string
		code int
	}{{"0", 0}, {"3", 3}, {"signal", 143}} {
		t.Run(c.arg, func(t *testing.T) {
			cmd := Command{Root: root, Script: "bin/show.sh", Args: []Value{{Text: c.arg}, {Text: "a b"}},
				Set: []Var{Bare("RUN_ID", "r1")}, Clear: []string{"SERVER_TAG"}}
			env, _ := Env(parent, cmd)
			var stdout, stderr bytes.Buffer
			code, err := Real.Run(cmd, env, &stdout, &stderr)
			if err != nil || code != c.code {
				t.Fatalf("Run: %d, %v; want %d\nstderr:\n%s", code, err, c.code, stderr.String())
			}
			shown(t, stdout.String(), root, []string{"arg " + c.arg, "arg a b"}, []string{"RUN_ID=r1", "FAKE_X=1"},
				[]string{"SERVER_TAG", "USER"})
		})
	}
	if _, err := Real.Run(Command{Root: root, Script: "bin/none.sh"}, nil, nil, nil); err == nil {
		t.Error("Run of a script that is not there: no error")
	}
	if _, err := Real.Run(Command{Root: root, Script: "bin/show.sh", Clear: []string{"NOT_A_KNOB"}}, nil, nil,
		nil); err == nil || !strings.Contains(err.Error(), "NOT_A_KNOB: not a knob") {
		t.Errorf("Run with a name that is not a knob: %v", err)
	}
}

// waitOn is a script that says when it is ready, then waits for a file named go in its directory, 10 s at most: TERM
// ends it with 9, its own code; go, with 0.
const waitOn = `#!/bin/sh
trap 'echo term; exit 9' TERM
: > ready
i=0
while [ ! -e go ]; do
  sleep 0.05; i=$((i + 1)); [ "$i" -lt 200 ] || exit 3
done
echo ended
`

// TestRunStop: when a Command's Stop ends, Run sends the script SIGTERM, and returns the script's own exit: when a
// SIGTERM ended it, or a cancel with no signal; but not when a SIGINT ended it, which a terminal sends the script too,
// and the script ends on its own.
func TestRunStop(t *testing.T) {
	for _, c := range []struct {
		name  string
		cause error
		code  int
		out   string
	}{
		{"SIGTERM", Signalled{syscall.SIGTERM}, 9, "term\n"},
		{"a cancel", nil, 9, "term\n"},
		{"SIGINT", Signalled{os.Interrupt}, 0, "ended\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := checkout(t)
			if err := os.WriteFile(filepath.Join(root, "bin", "wait.sh"), []byte(waitOn), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			var stdout bytes.Buffer
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() {
				code, err := Real.Run(Command{Root: root, Script: "bin/wait.sh", Stop: ctx},
					[]string{"PATH=" + os.Getenv("PATH")}, &stdout, nil)
				done <- result{code, err}
			}()
			for i := 0; ; i++ {
				if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
					break
				} else if i == 200 {
					t.Fatal("the script never said it was ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel(c.cause)
			if Interrupted(ctx) != (c.code == 0) {
				t.Errorf("Interrupted: %v for the cause %v", Interrupted(ctx), c.cause)
			}
			time.Sleep(200 * time.Millisecond)
			if err := os.WriteFile(filepath.Join(root, "go"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			r := <-done
			if r.err != nil || r.code != c.code || stdout.String() != c.out {
				t.Errorf("Run: %d, %v, stdout %q; want %d and %q", r.code, r.err, stdout.String(), c.code, c.out)
			}
		})
	}
}

// TestRunStdin: a Command's Stdin is what the script reads, and Exec refuses a Command that sets one, since an exec
// keeps laug's stdin.
func TestRunStdin(t *testing.T) {
	root := checkout(t)
	if err := os.WriteFile(filepath.Join(root, "bin", "cat.sh"), []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := Command{Root: root, Script: "bin/cat.sh", Stdin: strings.NewReader("set: 1\n")}
	var stdout bytes.Buffer
	if code, err := Real.Run(cmd, []string{"PATH=" + os.Getenv("PATH")}, &stdout, nil); err != nil || code != 0 ||
		stdout.String() != "set: 1\n" {
		t.Errorf("Run: %d, %v, stdout %q; want 0 and the reader's text", code, err, stdout.String())
	}
	if err := Real.Exec(cmd, nil); err == nil || !strings.Contains(err.Error(), "Stdin is set") {
		t.Errorf("Exec with Stdin set: %v", err)
	}
}

// TestExec: a process that calls Exec becomes the script: its output, its exit code, and an environment with no stray
// SERVER_TAG and none of the caller's other names.
func TestExec(t *testing.T) {
	root := checkout(t)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LAUG_SCRIPT_EXEC=" + root, "SERVER_TAG=1.32.1", "RUN_ID=stray",
		"XDG_RUNTIME_DIR=/run/x"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code, err := ExitCode(cmd.Run())
	if err != nil || code != 7 {
		t.Fatalf("exit %d, %v; want 7, the script's\nstderr:\n%s", code, err, stderr.String())
	}
	shown(t, stdout.String(), root, []string{"arg 7", "arg a b"}, []string{"RUN_ID=r1", "XDG_RUNTIME_DIR=/run/x"},
		[]string{"SERVER_TAG", "LAUG_SCRIPT_EXEC"})
}

// TestExecTool: a process that calls Exec on a tool becomes the program the PATH in its environment finds, run in Dir.
// The program is a script, so its $0 is the file the kernel ran, as when a shell finds it on PATH.
func TestExecTool(t *testing.T) {
	root := checkout(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "showtool"), []byte(show), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "LAUG_SCRIPT_EXEC=" + root, "LAUG_SCRIPT_TOOL=1"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code, err := ExitCode(cmd.Run())
	if err != nil || code != 7 {
		t.Fatalf("exit %d, %v; want 7, the program's\nstderr:\n%s", code, err, stderr.String())
	}
	lines := strings.Split(stdout.String(), "\n")
	if want := []string{"arg0 " + filepath.Join(bin, "showtool"), "arg 7", "arg a b", "pwd " + filepath.Join(root,
		"bin")}; len(lines) < 4 ||
		!slices.Equal(lines[:4], want) {
		t.Errorf("the program printed:\n%s\nwant it to start %q", stdout.String(), want)
	}
}

// TestRunTool: Run finds a tool in the PATH of the environment it is handed, not laug's, skipping a relative
// directory, and runs it in Dir; with none there it does not start.
func TestRunTool(t *testing.T) {
	root := checkout(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "showtool"), []byte(show), 0o755); err != nil {
		t.Fatal(err)
	}
	// A showtool in the checkout, which a relative PATH entry would find first.
	if err := os.WriteFile(filepath.Join(root, "bin", "showtool"), []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := Command{Root: root, Script: "showtool", Tool: true, Dir: "bin", Args: []Value{{Text: "3"}}}
	env := []string{"PATH=.:bin:" + bin + ":/usr/bin:/bin", "PATH=/nowhere"}
	var stdout bytes.Buffer
	code, err := Real.Run(cmd, env, &stdout, nil)
	if err != nil || code != 3 {
		t.Fatalf("Run: %d, %v; want 3, the program's", code, err)
	}
	if lines := strings.Split(stdout.String(), "\n"); len(lines) < 3 || lines[0] != "arg0 "+filepath.Join(bin, "showtool") ||
		lines[2] != "pwd "+filepath.Join(root, "bin") {
		t.Errorf("the program printed:\n%s", stdout.String())
	}
	if _, err := Real.Run(cmd, []string{"PATH=/nowhere"}, nil, nil); err == nil || err.Error() != "no showtool on PATH" {
		t.Errorf("Run with no showtool on PATH: %v", err)
	}
	if err := Real.Exec(cmd, []string{"PATH=/nowhere"}); err == nil || err.Error() != "no showtool on PATH" {
		t.Errorf("Exec with no showtool on PATH: %v", err)
	}
}

// TestDryRun: the line make -n printed: the knobs make passed unnamed first in the table's order, then the recipe's in
// its own, quoted as it quoted them, then the script and its arguments; the checkout written $ROOT, and the rest of
// $HOME ~/ with the quotes after it.
func TestDryRun(t *testing.T) {
	root := "/work/laug"
	for _, c := range []struct {
		name string
		cmd  Command
		want string
	}{
		{"load", Command{Root: root, Script: "load/run-omes.sh",
			Args: []Value{{Text: "workflow_with_single_noop_activity"}},
			Set: []Var{Quoted("OMES_DIR", ".upstream/omes"), Quoted("CELL_ADDRESS", "127.0.0.1:7233"),
				Quoted("PROM_URL", "http://127.0.0.1:9090"), Bare("PROJECT", "laug"), Bare("NAMESPACE", "default"),
				Bare("RUN_ID", "20261022-120000"), Bare("SERVER_TAG", "")},
			Pass: []Var{Bare("RATE", "50"), Bare("DURATION", "5m")}},
			"DURATION=5m RATE=50 OMES_DIR='.upstream/omes' CELL_ADDRESS='127.0.0.1:7233' " +
				"PROM_URL='http://127.0.0.1:9090' PROJECT=laug NAMESPACE=default RUN_ID=20261022-120000 SERVER_TAG= " +
				"load/run-omes.sh workflow_with_single_noop_activity"},
		{"an empty quoted value", Command{Root: root, Script: "load/verdict.sh", Args: []Value{{Text: "r1"}},
			Set: []Var{Quoted("PROM_URL", ""), Quoted("FALLBACK_PROM_URL", "http://127.0.0.1:9090")}},
			"PROM_URL='' FALLBACK_PROM_URL='http://127.0.0.1:9090' load/verdict.sh r1"},
		{"the checkout, a quote and a blank", Command{Root: root, Script: "faults/candidate.sh",
			Args: []Value{{Text: "benign"}, {Text: root + "/.last-lease.json"}, {Text: root}, {Text: "it's"},
				{Text: "a b"}, {Text: "1.31.0", Quote: true}, {Text: root + "x"}}},
			"faults/candidate.sh benign $ROOT/.last-lease.json $ROOT 'it'\\''s' 'a b' '1.31.0' /work/laugx"},
		{"a tool in a dir, an empty argument", Command{Root: root, Script: "go", Tool: true, Dir: "health",
			Args: []Value{{Text: "run"}, {Text: "./cmd/healthcheck"}, {Text: "-metrics"}, {Text: ""}, {Text: "-x"},
				{Text: "", Quote: true}}, Set: []Var{Quoted("REPLAY_HISTORY", root+"/h.json")}},
			`cd health && REPLAY_HISTORY='$ROOT/h.json' go run ./cmd/healthcheck -metrics "" -x ''`},
		{"a tool", Command{Root: root, Script: "temporal", Tool: true, Args: []Value{{Text: "workflow"},
			{Text: "gate-g1", Quote: true}}}, "temporal workflow 'gate-g1'"},
		{"home, outside the checkout", Command{Root: root, Home: "/work", Script: "gate/run.sh",
			Set: []Var{Quoted("GATE_RESULTS", "/work/tmp/laug-fake-gate-1/results"), Bare("OMES_DIR", "/work/o"),
				Quoted("PLAN", root+"/gate/plan.yaml")}, Args: []Value{{Text: "/work/a b"}, {Text: "/workx"}}},
			"GATE_RESULTS=~/'tmp/laug-fake-gate-1/results' OMES_DIR=~/o PLAN='$ROOT/gate/plan.yaml' gate/run.sh " +
				"~/'a b' /workx"},
		{"home /", Command{Root: root, Home: "/", Script: "gate/run.sh", Args: []Value{{Text: "/tmp/x"}}},
			"gate/run.sh /tmp/x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := DryRun(c.cmd); got != c.want {
				t.Errorf("\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		cmd  Command
		want string
	}{
		{Command{Script: "load/run-omes.sh", Set: []Var{Bare("RUN_ID", "x")}, Pass: []Var{Bare("RATE", "1")},
			Clear: []string{"SERVER_TAG"}}, ""},
		{Command{}, `script "": a path relative to the checkout`},
		{Command{Script: "/abs.sh"}, `script "/abs.sh": a path relative to the checkout`},
		{Command{Script: "x.sh", Set: []Var{Bare("GATE_LOCK", "x")}}, "GATE_LOCK: not a knob"},
		{Command{Script: "x.sh", Set: []Var{Bare("RUN_ID", "x")}, Clear: []string{"RUN_ID"}}, "RUN_ID: set twice"},
		{Command{Script: "go", Tool: true, Dir: "health"}, ""},
		{Command{Script: "bin/go", Tool: true}, `tool "bin/go": a program's name, which PATH finds`},
		{Command{Script: "go", Tool: true, Dir: "../x"}, `dir "../x": a directory in the checkout`},
		{Command{Script: "go", Tool: true, Dir: "/x"}, `dir "/x": a directory in the checkout`},
	} {
		err := c.cmd.Check()
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), c.want)) {
			t.Errorf("%+v: %v, want %q", c.cmd, err, c.want)
		}
	}
}
