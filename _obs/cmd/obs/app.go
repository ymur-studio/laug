package main

// The App, copied from cmd/laug/app.go without what only laug's own verbs use: the clock and the Temporal SDK's
// roles. Self is fixed here, not looked up: another program called obs may be on PATH.

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ymur-studio/laug/internal/script"
)

// App is what every verb gets: the checkout it runs in, the two streams it writes, the environment it reads and what
// runs its scripts. main fills it from the process; a test fills it with a temp dir, buffers, a list and a recorder,
// so no test reads or sets the process environment or runs a script it did not mean to.
type App struct {
	// Root is the laug checkout: the first directory up from the working directory that holds go.mod and
	// gate/run.sh. Empty outside one; a verb that needs it says so.
	Root string
	// Environ is the environment as os.Environ gives it, one KEY=value a string. A script gets what internal/script's
	// Env keeps of it.
	Environ []string
	Stdout  io.Writer
	Stderr  io.Writer
	// Runner runs a verb's script (runScript): script.Real in main. nil runs none, and the verb says so.
	Runner script.Runner
	// Self is the word every line this binary prints for a person to type starts with: bin/obs, the one make build
	// builds, as newApp sets it. It never comes from argv, which can hold an absolute path, a home path often. A
	// script it runs gets it as OBS (selfVar). Empty is bin/obs.
	Self string
}

// obsBin is the binary make build builds from this module, and the word a line to type starts with. _obs/obs.sh runs
// it for follow's answers and copies it to run the stack's cellstat and follow.
const obsBin = "bin/obs"

// selfVar is the variable this binary sets to Self for the script it runs, so the script's lines to type start with
// the word its own do. _obs/obs.sh reads it as ${OBS:-bin/obs}: one run by hand gets bin/obs. Its lines that name a
// laug verb read LAUG, ${LAUG:-bin/laug}, which internal/script's Env drops, so they name bin/laug.
const selfVar = "OBS"

// self is the word a line to type starts with: Self, or bin/obs when it is empty.
func (a *App) self() string {
	if a.Self == "" {
		return obsBin
	}
	return a.Self
}

// scriptEnv is script.Env's environment for c, built from environ, with OBS set to the word a line to type starts
// with. Env drops an OBS the caller's environment holds, as it drops every name it does not pass on.
func (a *App) scriptEnv(environ []string, c script.Command) (env []string, strays []script.Stray) {
	env, strays = script.Env(environ, c)
	return append(env, selfVar+"="+a.self()), strays
}

// newApp is the App main runs: the checkout above the working directory, the process's environment and streams, and
// the real runner.
func newApp() *App {
	wd, _ := os.Getwd()
	return &App{Root: findRoot(wd), Environ: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr, Runner: script.Real,
		Self: obsBin}
}

// findRoot returns the first of dir and its parents that holds go.mod and gate/run.sh, or "" when none does or
// dir is not absolute. _obs/go.mod alone does not make _obs a checkout: gate/run.sh is the root's.
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

// lookPath finds an executable called name on the App's PATH, or returns "". The process's own PATH is never read, so
// a test's stub is the only docker a test can run.
func (a *App) lookPath(name string) string {
	for _, dir := range filepath.SplitList(a.getenv("PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
