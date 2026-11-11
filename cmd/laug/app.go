package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// App is what every verb gets: the checkout it runs in, the two streams it writes, the environment it reads, what
// runs its scripts and the clock. main fills it from the process; a test fills it with a temp dir, buffers, a list, a
// recorder and a fixed time, so no test reads or sets the process environment or runs a script it did not mean to.
type App struct {
	// Root is the laug checkout: the first directory up from the working directory that holds go.mod and
	// gate/run.sh. Empty outside one; a verb that needs it says so.
	Root string
	// Environ is the environment as os.Environ gives it, one KEY=value a string. laug reads TERM and NO_COLOR, pool
	// serve hands all of it to its backend, and a script gets what internal/script's Env keeps of it.
	Environ []string
	Stdout  io.Writer
	Stderr  io.Writer
	// Runner runs a verb's script (runScript): script.Real in main. nil runs none, and the verb says so.
	Runner script.Runner
	// Now is the clock for a default a verb stamps, as a run id is: time.Now in main. nil is time.Now.
	Now func() time.Time
	// Roles runs the Temporal SDK's roles in laug's own process (runRole): sdkRoles in main. nil runs none, and the
	// verb says so.
	Roles Roles
	// Self is the word every line laug prints for a person to type starts with: laug when PATH finds a laug, else
	// bin/laug, the one make build builds (selfWord). It never comes from argv, which can hold an absolute path, a home
	// path often. A script or role laug runs gets it as LAUG (selfVar). Empty is laug.
	Self string
}

// selfVar is the variable laug sets to Self for every script and role it runs, so a script's lines to type start
// with the word laug's own do. A script reads it as ${LAUG:-bin/laug}: one run by hand gets bin/laug.
const selfVar = "LAUG"

// self is the word a line to type starts with: Self, or laug when it is empty.
func (a *App) self() string {
	if a.Self == "" {
		return "laug"
	}
	return a.Self
}

// selfWord is Self as newApp sets it: laug when lookPath, exec.LookPath in main, finds a laug on PATH, else bin/laug.
// Which laug PATH finds is not checked: a line names the command, not the file.
func selfWord(lookPath func(string) (string, error)) string {
	if _, err := lookPath("laug"); err == nil {
		return "laug"
	}
	return "bin/laug"
}

// scriptEnv is script.Env's environment for c, built from environ, with LAUG set to the word a line to type starts
// with. Env drops a LAUG the caller's environment holds, as it drops every name it does not pass on.
func (a *App) scriptEnv(environ []string, c script.Command) (env []string, strays []script.Stray) {
	env, strays = script.Env(environ, c)
	return append(env, selfVar+"="+a.self()), strays
}

// Roles runs one of the six Temporal SDK roles in laug's own process, by name: healthcheck and timerprobe
// (internal/health), worker, gate, probe and record (internal/orchestrator). cmd is the command and verb it runs for,
// health check say, which names the role's lines on stderr, "laug · <cmd>: ", as any laug error is named. args are its
// own flags, environ the environment it reads in place of the process's, and stdout and stderr laug's. It returns the
// role's exit code.
type Roles interface {
	Role(ctx context.Context, name, cmd string, args, environ []string, stdout, stderr io.Writer) int
}

// roleMain is one role's entry point: health.CheckMain, orchestrator.WorkerMain and the rest.
type roleMain func(ctx context.Context, cmd string, args, environ []string, stdout, stderr io.Writer) int

// sdkRoles runs a role from healthRoles (health.go) or orchRoles (orchestrator.go), the two files of this package that
// import the packages that link the SDK.
type sdkRoles struct{}

func (sdkRoles) Role(ctx context.Context, name, cmd string, args, environ []string, stdout, stderr io.Writer) int {
	if m, ok := healthRoles[name]; ok {
		return m(ctx, cmd, args, environ, stdout, stderr)
	}
	if m, ok := orchRoles[name]; ok {
		return m(ctx, cmd, args, environ, stdout, stderr)
	}
	fmt.Fprintf(stderr, "laug · %s: no SDK role %q\n", cmd, name)
	return exitFailed
}

// newApp is the App main runs: the checkout above the working directory, the process's environment and streams, the
// real runner and clock, the SDK's roles, and the word a line to type starts with.
func newApp() *App {
	wd, _ := os.Getwd()
	return &App{Root: findRoot(wd), Environ: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr, Runner: script.Real,
		Now: time.Now, Roles: sdkRoles{}, Self: selfWord(exec.LookPath)}
}

// findRoot returns the first of dir and its parents that holds go.mod and gate/run.sh, or "" when none does or
// dir is not absolute.
func findRoot(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	for {
		if isFile(filepath.Join(dir, "go.mod")) && isFile(filepath.Join(dir, "gate", "run.sh")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// executable says whether the checkout holds an executable file at rel, a link followed: a binary make built.
func (a *App) executable(rel string) bool {
	fi, err := os.Stat(filepath.Join(a.Root, rel))
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// getenv returns key's value in Environ, or "" when it is absent. The first entry wins, as with os.Getenv: an execve
// with K=first then K=second, and os.Getenv returned first (seen, go1.26.0 on the dev host).
func (a *App) getenv(key string) string {
	for _, kv := range a.Environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}
