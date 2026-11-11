package script

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// Value is one word of a script's line, a knob's value or an argument: its text, and whether the -dry-run line writes
// it in single quotes, as the Makefile's shq wrote a value it quoted. Bare text with anything but letters, digits and
// _./:@%+=,- in it is quoted anyway, so the line stays one a shell reads back as the same words.
type Value struct {
	Text  string
	Quote bool
}

// Var is one knob a verb sets for its script.
type Var struct {
	Name string
	Value
}

// Bare and Quoted are the two ways the Makefile wrote a value on a script's line: pasted as it was, after a check
// that it is one word, or through shq.
func Bare(name, value string) Var   { return Var{name, Value{value, false}} }
func Quoted(name, value string) Var { return Var{name, Value{value, true}} }

// Command is one script, as a verb hands it over.
type Command struct {
	Root string // the checkout: the -dry-run line writes it $ROOT
	// Home is $HOME: the -dry-run line writes a path under it, and outside the checkout, ~/ and the rest, so a line
	// pasted elsewhere names no home path. "" or / writes every path as it is.
	Home string
	// Script is the script, relative to the directory it runs in, as make ran it: load/run-omes.sh; with Tool, the
	// name of a program on PATH: go or temporal.
	Script string
	// Tool says Script is a program the PATH in the script's environment finds, as a recipe's shell found go and
	// temporal, and not a file in the checkout.
	Tool bool
	// Dir is the directory it runs in, relative to Root, as a recipe's "cd health &&" ran go test there; "" is Root.
	// The -dry-run line starts "cd <Dir> && ".
	Dir  string
	Args []Value
	// Set holds the knobs the old recipe set on the script's line, in its order. Pass holds the ones make passed on
	// unnamed from its command line, which make -n never printed: the -dry-run line writes them first, in the knob
	// table's order. Clear names the knobs the verb maps that this run leaves unset, so the script's own default
	// applies. A name appears once across the three, and each is a knob.
	Set   []Var
	Pass  []Var
	Clear []string
	// By names what sets a knob this verb maps when it is not the knob's Flag in the table, for the line a stray copy
	// gets: gate run takes SERVER_TAG as its argument, <tag>, where load takes it from -server-tag.
	By map[string]string
	// Stdin, when set, is what Run hands the script on its stdin, in place of laug's own. Exec cannot hand it over, so
	// it refuses a Command that sets it.
	Stdin io.Reader
	// Stop, when set, is the verb's context, and Run passes its end on: the script gets SIGTERM, so a signal sent to
	// laug alone reaches it as it reached a script laug execs; but not when a SIGINT ended it (Interrupted), Ctrl-C,
	// which a terminal sends the script as well. Exec has no use for it.
	Stop context.Context
}

// Signalled is the cause main gives a verb's context when a signal ends it: the signal.
type Signalled struct{ Signal os.Signal }

func (s Signalled) Error() string { return s.Signal.String() + " signal received" }

// Interrupted says whether a SIGINT ended ctx: Ctrl-C at a terminal, which reaches laug's children too.
func Interrupted(ctx context.Context) bool {
	var s Signalled
	return errors.As(context.Cause(ctx), &s) && s.Signal == os.Interrupt
}

// Check says what is wrong with c, or nil: no script or an absolute one, a tool whose name holds a slash, a Dir
// outside the checkout, a name that is not a knob, or a name twice.
func (c Command) Check() error {
	switch {
	case c.Script == "" || strings.HasPrefix(c.Script, "/"):
		return fmt.Errorf("script %q: a path relative to the checkout", c.Script)
	case c.Tool && strings.Contains(c.Script, "/"):
		return fmt.Errorf("tool %q: a program's name, which PATH finds", c.Script)
	case c.Dir != "" && !filepath.IsLocal(c.Dir):
		return fmt.Errorf("dir %q: a directory in the checkout, relative to it", c.Dir)
	}
	seen := map[string]bool{}
	names := slices.Clone(c.Clear)
	for _, v := range slices.Concat(c.Set, c.Pass) {
		names = append(names, v.Name)
	}
	for _, n := range names {
		if _, ok := Lookup(n); !ok {
			return fmt.Errorf("%s: not a knob: a verb sets only the names internal/script's table holds", n)
		}
		if seen[n] {
			return fmt.Errorf("%s: set twice", n)
		}
		seen[n] = true
	}
	return nil
}

// pass is c.Pass in the knob table's order.
func (c Command) pass() []Var {
	p := slices.Clone(c.Pass)
	slices.SortStableFunc(p, func(a, b Var) int { return order(a.Name) - order(b.Name) })
	return p
}

// DryRun is the line -dry-run prints: the assignments and the command, as make -n printed the old recipe's line, one
// space between words, with the knobs make passed unnamed in front, and "cd <Dir> && " first when it runs in Dir. An
// empty argument that is not quoted is written "", as a recipe wrote one. The checkout is written $ROOT, so the line
// reads the same in any checkout.
func DryRun(c Command) string {
	var words []string
	if c.Dir != "" {
		words = append(words, "cd", c.render(Value{Text: c.Dir}), "&&")
	}
	for _, v := range slices.Concat(c.pass(), c.Set) {
		words = append(words, v.Name+"="+c.render(v.Value))
	}
	words = append(words, c.render(Value{Text: c.Script}))
	for _, a := range c.Args {
		if a.Text == "" && !a.Quote {
			words = append(words, `""`)
			continue
		}
		words = append(words, c.render(a))
	}
	return strings.Join(words, " ")
}

var bareSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]*$`)

// render writes w as the -dry-run line does. A path under the checkout is written $ROOT and the rest; one under Home,
// ~/ and the rest, the ~/ outside any quotes, so a shell still reads it as $HOME.
func (c Command) render(w Value) string {
	t, root, home := w.Text, "", ""
	switch {
	case c.Root != "" && (t == c.Root || strings.HasPrefix(t, c.Root+"/")):
		t, root = t[len(c.Root):], "$ROOT"
	case c.Home != "" && c.Home != "/" && strings.HasPrefix(t, c.Home+"/"):
		t, home = t[len(c.Home)+1:], "~/"
	}
	if !w.Quote && bareSafe.MatchString(t) {
		return home + root + t
	}
	return home + "'" + root + strings.ReplaceAll(t, "'", `'\''`) + "'"
}

// argv is the script's argv: its path or name as make ran it, then its arguments.
func (c Command) argv() []string {
	argv := []string{c.Script}
	for _, a := range c.Args {
		argv = append(argv, a.Text)
	}
	return argv
}

// dir is the directory c runs in: Root, or Dir under it.
func (c Command) dir() string {
	return filepath.Join(c.Root, c.Dir)
}

// path is what c runs: the script, relative to the directory it runs in; or a tool's file, found in env's PATH.
func (c Command) path(env []string) (string, error) {
	if !c.Tool {
		return c.Script, nil
	}
	return lookPath(c.Script, env)
}

// lookPath finds name in the PATH env holds, its first entry's, as a shell does: the first directory that holds an
// executable file by that name. An empty or relative directory in PATH is skipped, so the checkout is never searched.
func lookPath(name string, env []string) (string, error) {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
			break
		}
	}
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s on PATH", name)
}

// How says how a verb runs its script. Exec replaces laug with it, for a verb with nothing to do after it: the
// process, its signals and its exit code are the script's, as under make. Run keeps laug as its parent, for a verb that
// acts after it (raun, -json): the script writes to laug's stdout and stderr, and its exit code comes back as it was.
type How int

const (
	_ How = iota
	Exec
	Run
)

// Runner starts a script. Real is the one main uses; a test records instead.
type Runner interface {
	// Exec replaces the process with c's script, run from c.Root, or c.Dir in it, with env. It returns only when it
	// could not.
	Exec(c Command, env []string) error
	// Run runs c's script from c.Root, or c.Dir in it, with env, its stdin laug's, and waits for it, passing c.Stop's
	// end on. code is its exit code, or 128+n when signal n ended it, as a shell reports one; err is set only when it
	// could not start.
	Run(c Command, env []string, stdout, stderr io.Writer) (code int, err error)
}

// Real runs scripts for real.
var Real Runner = real{}

type real struct{}

// Exec moves to the checkout, or Dir in it, and execs the script by its relative path, as make's shell did, so $0 is
// that path; a tool it execs by the file env's PATH finds, $0 its name. Every signal handler laug set goes back to its
// default at exec (execve(2)).
func (real) Exec(c Command, env []string) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.Stdin != nil {
		return errors.New("Stdin is set: an exec keeps laug's stdin, so a script that reads another runs with Run")
	}
	path, err := c.path(env)
	if err != nil {
		return err
	}
	if err := os.Chdir(c.dir()); err != nil {
		return err
	}
	return syscall.Exec(path, c.argv(), env)
}

// Run waits for the script whatever signal laug gets: Ctrl-C reaches the script from the terminal as it reaches laug,
// and the script stops on its own. A signal sent to laug alone is passed on as SIGTERM only when c.Stop is set. The
// script reads c.Stdin when it is set, else laug's stdin.
func (real) Run(c Command, env []string, stdout, stderr io.Writer) (int, error) {
	if err := c.Check(); err != nil {
		return -1, err
	}
	path, err := c.path(env)
	if err != nil {
		return -1, err
	}
	var stdin io.Reader = os.Stdin
	if c.Stdin != nil {
		stdin = c.Stdin
	}
	cmd := &exec.Cmd{Path: path, Args: c.argv(), Dir: c.dir(), Env: append([]string{}, env...),
		Stdin: stdin, Stdout: stdout, Stderr: stderr}
	if err := cmd.Start(); err != nil {
		return ExitCode(err)
	}
	if c.Stop != nil {
		waited := make(chan struct{})
		defer close(waited)
		go func() {
			select {
			case <-c.Stop.Done():
				if !Interrupted(c.Stop) {
					_ = cmd.Process.Signal(syscall.SIGTERM) // an error is a script that has ended
				}
			case <-waited:
			}
		}()
	}
	return ExitCode(cmd.Wait())
}

// ExitCode turns what exec.Cmd's Run or Wait returned into a shell's exit code: 0, the process's own, or 128+n when
// signal n ended it. err is returned only when the process did not run.
func ExitCode(err error) (int, error) {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	default:
		return -1, err
	}
}
