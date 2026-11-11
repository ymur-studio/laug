package main

// The edge a verb that wraps a script crosses, copied from cmd/laug/script.go: its -dry-run flag, and runScript, which
// hands _obs/obs.sh to internal/script from the checkout's root with a clean environment. prepare and start are
// runScript's two halves, for obs status -json, which keeps the script a child. cmd/laug's dryLine and its seams are
// left out: _obs/obs.sh reads none of CATALOG, CANDIDATES and LEASE_FILE, so its -dry-run line is script.DryRun's.

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"github.com/ymur-studio/laug/internal/script"
)

// dryRunFlag adds -dry-run to a script verb's flags.
func dryRunFlag(f *flags) *bool {
	return f.Bool("dry-run", false, "print the command it would run, the variables it sets first, and run nothing")
}

// runScript runs c for cmd, a verb as its lines name it ("obs up", "usage record"): from the checkout's
// root, with the environment scriptEnv builds from a.Environ, script.Env's and LAUG, after one stderr line for each
// stray copy of a knob c maps. With dry it prints script.DryRun's line on stdout and runs nothing. Otherwise it prints
// first, when not empty, on stdout, and runs c by how: script.Exec, which returns only when the exec failed (a recorder
// returns nil, and runScript 0), or script.Run, which returns the script's exit code as it was. Exit 2 outside a
// checkout; 1 when c names a name that is not a knob, there is no runner, or the script could not start.
func (a *App) runScript(cmd string, c script.Command, dry bool, how script.How, first string) int {
	c, env, code, ok := a.prepare(cmd, c, dry)
	if !ok {
		return code
	}
	if dry {
		fmt.Fprintln(a.Stdout, script.DryRun(c))
		return exitDone
	}
	return a.start(cmd, c, env, how, first, a.Stdout)
}

// prepare checks c for cmd and returns it rooted at the checkout, $HOME beside it for the -dry-run line, with the
// environment it runs with, after one stderr line for each stray. With ok false it printed why, and the verb returns
// code: 2 outside a checkout, 1 for a name that is not a knob, or, unless dry, no runner.
func (a *App) prepare(cmd string, c script.Command, dry bool) (_ script.Command, env []string, code int, ok bool) {
	if !a.inCheckout(cmd, c.Script) {
		return c, nil, exitUsage, false
	}
	c.Root = a.Root
	if home := a.getenv("HOME"); filepath.IsAbs(home) {
		c.Home = filepath.Clean(home)
	}
	if err := c.Check(); err != nil {
		fmt.Fprintf(a.Stderr, "laug · %s: %v\n", cmd, a.errShown(err))
		return c, nil, exitFailed, false
	}
	env, strays := a.scriptEnv(a.Environ, c)
	for _, s := range strays {
		fmt.Fprintf(a.Stderr, "laug · %s: %s\n", cmd, s)
	}
	if !dry && a.Runner == nil {
		fmt.Fprintf(a.Stderr, "laug · %s: no runner, so %s did not run\n", cmd, c.Script)
		return c, nil, exitFailed, false
	}
	return c, env, exitDone, true
}

// inCheckout reports whether laug runs in a checkout. Outside one it prints that cmd runs path, a script, so it needs
// one, and the verb exits 2.
func (a *App) inCheckout(cmd, path string) bool {
	if a.Root == "" {
		fmt.Fprintf(a.Stderr, "laug · %s: runs %s, so it needs a laug checkout: no directory above this one holds "+
			"go.mod and gate/run.sh\n", cmd, path)
	}
	return a.Root != ""
}

// start runs c, as prepare returned it, with env: first on laug's stdout when not empty, then the script, writing its
// own stdout to stdout. It returns the script's exit code, or 1 with one line when the script could not start.
func (a *App) start(cmd string, c script.Command, env []string, how script.How, first string, stdout io.Writer) int {
	if first != "" {
		fmt.Fprintln(a.Stdout, first)
	}
	var (
		code int
		err  error
	)
	switch how {
	case script.Exec:
		err = a.Runner.Exec(c, env)
	case script.Run:
		code, err = a.Runner.Run(c, env, stdout, a.Stderr)
	default:
		err = fmt.Errorf("how %d is neither script.Exec nor script.Run", how)
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · %s: could not run %s: %v\n", cmd, c.Script, a.errShown(err))
		return exitFailed
	}
	return code
}

// childExit is the exit of a verb that keeps its script a child: the script's code when the verb's paragraph names it
// (named) or a signal ended the script (128+n), else 2, after one stderr line that names the script's own code, so
// the verb exits only with codes doc.go names. A script under set -e stops with the code of the command that failed,
// which is that tool's own: load/verdict.sh with no Prometheus to read exited 7, curl's. what is what exit 2 means for
// the verb.
func (a *App) childExit(cmd, what, path string, code int, named ...int) int {
	if slices.Contains(named, code) || code > 128 && code <= 128+64 {
		return code
	}
	fmt.Fprintf(a.Stderr, "laug · %s: %s: %s exited %d, the code of a command it ran, which %s does not name; exit 2\n",
		cmd, what, path, code, cmd)
	return exitUsage
}
