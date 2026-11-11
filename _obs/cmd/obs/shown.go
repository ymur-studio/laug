package main

// What the verbs share with laug's own, copied from cmd/laug: the checks on a value (daily.go), how a refusal and a
// mistake are printed (daily.go), the gate runs in -results (gate.go), how a path is shown (gate.go), and a word, a
// field and a JSON document as a line holds them (lease.go, pool.go). Each keeps its cmd/laug text.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// gateResultsDefault is GATE_RESULTS's default, as cmd/laug/constants.go holds it.
const gateResultsDefault = "gate/results"

// wordRE is cmd/laug/daily.go's: the shape of a word pasted bare onto a script's line. Here it checks a run id
// (checkWords) and a gate id (gateRun).
var wordRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// checkWords returns an error naming the first flag, of pairs given as flag then value, whose value is not one word.
func checkWords(pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		if !wordRE.MatchString(pairs[i+1]) {
			return fmt.Errorf("%s %s: not one word of letters, digits, ., _ and -, from a letter or digit", pairs[i],
				shellWord(pairs[i+1]))
		}
	}
	return nil
}

// usageError prints why on one stderr line, its paths as textShown writes them, then f's synopsis and the line that
// prints its flags, and returns 2.
func (a *App) usageError(f *flags, format string, v ...any) int {
	fmt.Fprintf(a.Stderr, "laug · %s: %s\n", f.cmd, a.textShown(fmt.Sprintf(format, v...)))
	f.mistake(a.Stderr)
	return exitUsage
}

// refused prints why cmd refuses, one stderr line, its paths as textShown writes them, and returns 2.
func (a *App) refused(cmd string, err error) int {
	fmt.Fprintf(a.Stderr, "laug · %s: %s\n", cmd, a.errShown(err))
	return exitUsage
}

// given reports whether the flag called name was set on the command line, as make told a variable given from its
// default by its origin.
func given(f *flags, name string) bool {
	set := false
	f.Visit(func(fl *flag.Flag) { set = set || fl.Name == name })
	return set
}

// gateIDRE is a gate run's id as gate/run.sh makes it: g<UTC date>-<UTC time>, and -<n> for a second run in that
// second.
var gateIDRE = regexp.MustCompile(`^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)

// checkGateID refuses a gate id that is not one, as the Makefile's CHECK_GATE_ID did before obs-find and obs-backfill.
func checkGateID(id string) error {
	if !gateIDRE.MatchString(id) {
		return fmt.Errorf("%s: not a gate id, g<UTC date>-<UTC time>, as gate run names one", shellWord(id))
	}
	return nil
}

// runRef is a gate run as a verb takes it: an id, one word, read in -results; or, holding a slash, the path to a
// run's directory.
type runRef struct {
	arg  string // as given
	path string // the run's directory: -results/<id>, or the path given
}

// gateRun reads a gate run argument, or an error that names it.
func gateRun(results, arg string) (runRef, error) {
	if strings.Contains(arg, "/") {
		return runRef{arg, arg}, nil
	}
	if !wordRE.MatchString(arg) {
		return runRef{}, fmt.Errorf("%s: neither a gate id in %s, one word of letters, digits, ., _ and -, nor the "+
			"path to a run's directory", shellWord(arg), results)
	}
	return runRef{arg, filepath.Join(results, arg)}, nil
}

// resultsFlag adds -results to a verb's flags.
func resultsFlag(f *flags) *string {
	return f.String("results", gateResultsDefault, "the `dir` that holds the gate runs; a relative path starts at the "+
		"checkout's root")
}

// latestRun is the newest gate run in results, by id; "" when there is none.
func (a *App) latestRun(results string) string {
	ents, err := os.ReadDir(a.abs(results))
	if err != nil {
		return ""
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() && gateIDRE.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return ""
	}
	slices.SortFunc(ids, compareGateIDs)
	return ids[len(ids)-1]
}

// compareGateIDs orders gate ids by time, then a second run in the same second after the first.
func compareGateIDs(x, y string) int {
	xs, xn, _ := strings.Cut(x[1:], "-")
	ys, yn, _ := strings.Cut(y[1:], "-")
	xt, xi, _ := strings.Cut(xn, "-")
	yt, yi, _ := strings.Cut(yn, "-")
	if c := strings.Compare(xs+xt, ys+yt); c != 0 {
		return c
	}
	n := func(s string) int { i, _ := strconv.Atoi(s); return i }
	return n(xi) - n(yi)
}

// abs is path as a verb opens it: under the checkout's root when relative, as the scripts, run from there, read it;
// outside a checkout, as given.
func (a *App) abs(path string) string {
	if filepath.IsAbs(path) || a.Root == "" {
		return path
	}
	return filepath.Join(a.Root, path)
}

// pathShown is an absolute path as a line prints it, naming no home path: relative to the checkout's root when below
// it, ~/ and the rest when below $HOME, else as it is. A relative path is left as given.
func (a *App) pathShown(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	path = filepath.Clean(path)
	if a.Root != "" {
		if rel, ok := strings.CutPrefix(path, filepath.Clean(a.Root)+string(filepath.Separator)); ok {
			return rel
		}
	}
	if home := a.getenv("HOME"); filepath.IsAbs(home) && filepath.Clean(home) != "/" {
		if rel, ok := strings.CutPrefix(path, filepath.Clean(home)+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return path
}

// wordShown is pathShown as one shell word: quoted when it needs it, with a leading ~/ left out of the quotes, so a
// shell still reads it as $HOME.
func (a *App) wordShown(path string) string {
	s := a.pathShown(path)
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		return "~/" + shellWord(rest)
	}
	return shellWord(s)
}

// textShown is s, a line a verb prints, with each absolute path in it written as pathShown writes one: relative to the
// checkout's root when below it, ~/ and the rest when below $HOME. A path counts where it starts s or follows a space,
// a quote, =, :, ( or [, as an error's text names its paths; one in single quotes gets its ~/ outside them, as
// wordShown writes one, so the word still pastes.
func (a *App) textShown(s string) string {
	if a.Root != "" && filepath.IsAbs(a.Root) {
		s = prefixShown(s, filepath.Clean(a.Root)+string(filepath.Separator), "", "'")
	}
	if home := a.getenv("HOME"); filepath.IsAbs(home) && filepath.Clean(home) != "/" {
		s = prefixShown(s, filepath.Clean(home)+string(filepath.Separator), "~/", "~/'")
	}
	return s
}

// prefixShown is s with each prefix that starts a path written as with, or as quoted after a single quote.
func prefixShown(s, prefix, with, quoted string) string {
	re := regexp.MustCompile(`(^|[\s"'=:(\[,])` + regexp.QuoteMeta(prefix))
	return re.ReplaceAllStringFunc(s, func(m string) string {
		lead := m[:len(m)-len(prefix)]
		if lead == "'" {
			return quoted
		}
		return lead + with
	})
}

// errShown is err's text as textShown writes it, for a line that prints an error: an error from the file system or
// from exec names its path whole.
func (a *App) errShown(err error) string {
	return a.textShown(err.Error())
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// shellWord quotes s for a shell when it holds anything but letters, digits and _./:@%+=,-.
func shellWord(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeCompact writes doc, a JSON document as a script printed it, the way -json prints one: on one line, nothing
// else.
func writeCompact(w io.Writer, doc []byte) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, doc); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// plain is a field as one word on one line: "-" when empty, quoted when it holds a space, a tab or a control
// character.
func plain(s string) string {
	switch {
	case s == "":
		return "-"
	case strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return strconv.Quote(s)
	}
	return s
}
