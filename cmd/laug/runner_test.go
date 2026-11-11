package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// recorder is a Runner, and the App's Roles, that records what it was handed, how, and what it read on stdin, and
// runs nothing. run, when set, stands in for a script Run starts: it writes what the script would and returns its exit
// code. execErr is what Exec returns. A role is recorded as a Command named for it, its flags as arguments, Root the
// checkout's, how inProcess, and the command it ran for in cmds; role, when set, stands in for it as run does for a
// script.
type recorder struct {
	calls   []script.Command
	envs    [][]string
	hows    []script.How
	stdins  []string
	cmds    []string
	run     func(c script.Command, env []string, stdout, stderr io.Writer) int
	execErr error
	root    string
	role    func(name string, args, env []string, stdout, stderr io.Writer) int
}

// inProcess is how a role runs: in laug's own process, through the App's Roles.
const inProcess script.How = 100

func (r *recorder) Role(_ context.Context, name, cmd string, args, env []string, stdout, stderr io.Writer) int {
	r.cmds = append(r.cmds, cmd)
	c := script.Command{Root: r.root, Script: name}
	for _, a := range args {
		c.Args = append(c.Args, script.Value{Text: a})
	}
	r.record(c, env, inProcess)
	if r.role != nil {
		return r.role(name, args, env, stdout, stderr)
	}
	return 0
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

// tideFlow is a stand-in script verb: -server-tag sets SERVER_TAG on the script's line, -rate passes RATE unnamed,
// -wait runs the script as a child; bin/show.sh gets the exit code to end with as its argument.
func tideFlow(_ context.Context, a *App, args []string) int {
	f := a.newFlags("tide flow", "<exit> [-server-tag <x.y.z>] [-rate <n>] [-wait] [-dry-run]")
	tag := f.String("server-tag", "", "the server `tag`")
	rate := f.String("rate", "", "the `rate`")
	wait := f.Bool("wait", false, "run the script as a child")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	c := script.Command{Script: "bin/show.sh", Args: []script.Value{{Text: pos[0]}}, Set: []script.Var{
		script.Bare("RUN_ID", "r1")}}
	if *tag != "" {
		c.Set = append(c.Set, script.Bare("SERVER_TAG", *tag))
	} else {
		c.Clear = append(c.Clear, "SERVER_TAG")
	}
	if *rate != "" {
		c.Pass = append(c.Pass, script.Bare("RATE", *rate))
	}
	how := script.Exec
	if *wait {
		how = script.Run
	}
	return a.runScript("tide flow", c, *dry, how, "")
}

// showScript prints its environment and exits with its argument.
const showScript = "#!/bin/sh\nenv\nexit \"$1\"\n"

// TestRunScript drives a stand-in script verb through run: a stray SERVER_TAG in laug's environment never reaches the
// script, which gets the one its flag sets, or none; nor does a stray LAUG, which laug sets to the App's word;
// -dry-run prints the line and runs nothing; the script's exit comes back as it was; outside a checkout the verb
// refuses.
func TestRunScript(t *testing.T) {
	saved := commands
	t.Cleanup(func() { commands = saved })
	tide := command{"tide", groupDaily, "run a stand-in", []verb{{"flow", classAct, tideFlow, "flow a stand-in"}}}
	commands = append(slices.Clone(saved), tide)
	checkVerbs(t, tide)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "show.sh"), []byte(showScript), 0o755); err != nil {
		t.Fatal(err)
	}
	environ := []string{"PATH=" + os.Getenv("PATH"), "SERVER_TAG=1.32.1", "RUN_ID=stray", "RATE=9", "FAKE_X=1",
		"LAUG=/elsewhere/bin/laug"}
	stray := "laug · tide flow: SERVER_TAG=1.32.1 in the environment is not passed on; -server-tag sets it\n"
	for _, c := range []struct {
		name      string
		args      []string
		code      int
		has       []string // lines the script's environment holds
		hasnt     []string // names it must not hold
		stderr    string
		dryStdout string // with -dry-run: the one stdout line
	}{
		{"no tag", []string{"3", "-wait"}, 3, []string{"RUN_ID=r1", "FAKE_X=1", "LAUG=laug"}, []string{"SERVER_TAG", "RATE"},
			"laug · tide flow: RUN_ID=stray in the environment is not passed on; -run-id sets it\n" + stray, ""},
		{"a tag and a rate", []string{"0", "-wait", "-server-tag", "1.31.0", "-rate", "9"}, 0,
			[]string{"SERVER_TAG=1.31.0", "RATE=9", "RUN_ID=r1"}, nil,
			"laug · tide flow: RUN_ID=stray in the environment is not passed on; -run-id sets it\n" + stray, ""},
		{"-dry-run", []string{"0", "-server-tag", "1.31.0", "-rate", "9", "-dry-run"}, 0, nil, nil,
			"laug · tide flow: RUN_ID=stray in the environment is not passed on; -run-id sets it\n" + stray,
			"RATE=9 RUN_ID=r1 SERVER_TAG=1.31.0 bin/show.sh 0\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &recorder{}
			a := &App{Root: root, Environ: environ, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Runner: rec}
			if !slices.Contains(c.args, "-dry-run") {
				a.Runner = script.Real
			}
			code := run(context.Background(), a, append([]string{"tide", "flow"}, c.args...))
			out, errOut := a.Stdout.(*bytes.Buffer).String(), a.Stderr.(*bytes.Buffer).String()
			if code != c.code || errOut != c.stderr {
				t.Fatalf("exit %d, want %d\nstderr:\n%s\nwant:\n%s", code, c.code, errOut, c.stderr)
			}
			if c.dryStdout != "" {
				if out != c.dryStdout || len(rec.calls) != 0 {
					t.Errorf("-dry-run: stdout %q, want %q; %d scripts run", out, c.dryStdout, len(rec.calls))
				}
				return
			}
			lines := strings.Split(out, "\n")
			for _, kv := range c.has {
				if !slices.Contains(lines, kv) {
					t.Errorf("the script's environment lacks %s:\n%s", kv, out)
				}
			}
			for _, l := range lines {
				for _, k := range c.hasnt {
					if strings.HasPrefix(l, k+"=") {
						t.Errorf("the script's environment holds %s", l)
					}
				}
				if strings.HasPrefix(l, "SERVER_TAG=1.32.1") {
					t.Errorf("the stray SERVER_TAG reached the script: %s", l)
				}
			}
		})
	}

	t.Run("exec", func(t *testing.T) {
		rec := &recorder{}
		var stdout, stderr bytes.Buffer
		a := &App{Root: root, Environ: environ, Stdout: &stdout, Stderr: &stderr, Runner: rec, Self: "bin/laug"}
		if code := run(context.Background(), a, []string{"tide", "flow", "0"}); code != 0 || len(rec.calls) != 1 {
			t.Fatalf("exit %d, %d calls\n%s", code, len(rec.calls), stderr.String())
		}
		if got := rec.calls[0]; got.Root != root || got.Script != "bin/show.sh" {
			t.Errorf("handed %+v", got)
		}
		want := []string{"PATH=" + os.Getenv("PATH"), "FAKE_X=1", "RUN_ID=r1", "LAUG=bin/laug"}
		if !slices.Equal(rec.envs[0], want) {
			t.Errorf("env %q, want %q", rec.envs[0], want)
		}
	})
	t.Run("outside a checkout", func(t *testing.T) {
		rec := &recorder{}
		var stdout, stderr bytes.Buffer
		a := &App{Environ: environ, Stdout: &stdout, Stderr: &stderr, Runner: rec}
		code := run(context.Background(), a, []string{"tide", "flow", "0"})
		if code != 2 || len(rec.calls) != 0 || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(),
			"laug · tide flow: runs bin/show.sh, so it needs a laug checkout: ") {
			t.Errorf("exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", code, len(rec.calls), stdout.String(), stderr.String())
		}
	})
	t.Run("no runner", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		a := &App{Root: root, Stdout: &stdout, Stderr: &stderr}
		if code := run(context.Background(), a, []string{"tide", "flow", "0"}); code != 1 ||
			stderr.String() != "laug · tide flow: no runner, so bin/show.sh did not run\n" {
			t.Errorf("exit %d\nstderr:\n%s", code, stderr.String())
		}
	})
}
