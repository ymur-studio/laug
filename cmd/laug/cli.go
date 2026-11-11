package main

// The exit codes, flag parsing, -json and the build's commit: small helpers written here rather than imported, so
// cmd/laug's own files import no other module.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The exit codes every command keeps. A command that uses another code names it in its doc.go paragraph.
const (
	exitDone     = 0 // done, or ADVANCE
	exitFailed   = 1 // failed, or the negative answer the command exists to give: BLOCK
	exitUsage    = 2 // usage, or refused before acting
	exitShortRun = 3 // a run shorter than the window its thresholds trust (load/verdict.sh's header)
)

// flags is one command's flag set. Go's flag package writes nothing: parse sends asked-for help to stdout and a
// mistake to stderr. self is the word its usage line starts with, the App's.
type flags struct {
	*flag.FlagSet
	cmd, synopsis, self string
}

func (a *App) newFlags(cmd, synopsis string) *flags {
	fs := flag.NewFlagSet("laug "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return &flags{fs, cmd, synopsis, a.self()}
}

// helpWidth is the widest a line of -h may be, in columns: under 80, as every help line laug prints.
const helpWidth = 79

// flagIndent is where a flag's text starts under its name: the column Go's flag package reached with a tab.
const flagIndent = "        "

// usage writes the synopsis and the flags, what -h prints, every line under 80 columns: each flag's name and the word
// for its value, as Go's flag package prints them, then its text wrapped beneath at flagIndent, ending with its
// default when that is not the type's zero value (shownDefault).
func (f *flags) usage(w io.Writer) {
	f.synopsisLines(w)
	f.VisitAll(func(fl *flag.Flag) {
		name, text := flag.UnquoteUsage(fl)
		head := "  -" + fl.Name
		if name != "" {
			head += " " + name
		}
		if def, ok := shownDefault(fl); ok {
			text += " (default " + def + ")"
		}
		fmt.Fprintln(w, head)
		for _, l := range wrap(text, flagIndent, flagIndent, helpWidth) {
			fmt.Fprintln(w, l)
		}
	})
}

// synopsisIndent is where a synopsis that goes on to another line goes on: under the word after "usage:".
const synopsisIndent = "       "

// synopsisLines writes "usage: <self> <cmd> <synopsis>", wrapped under 80 columns. A bracketed group stays on one
// line when it fits on one; one too wide breaks between its inner groups (pieces).
func (f *flags) synopsisLines(w io.Writer) {
	var words []string
	for _, g := range bracketGroups(f.synopsis) {
		words = append(words, pieces(g)...)
	}
	for _, l := range wrapWords(words, "usage: "+f.self+" "+f.cmd+" ", synopsisIndent, helpWidth) {
		fmt.Fprintln(w, strings.TrimRight(l, " "))
	}
}

// pieces is a synopsis group as the parts a wrap may break between. A group that fits on a line under synopsisIndent
// is one part. A bracketed group too wide is its inner groups, each split the same way, with its brackets on the first
// and the last: "[-a | -b [-c <x>] [-d <y>]]" is "[-a", "|", "-b", "[-c <x>]" and "[-d <y>]]". Anything else too wide
// is its words.
func pieces(g string) []string {
	if utf8.RuneCountInString(synopsisIndent+g) <= helpWidth {
		return []string{g}
	}
	inner, ok := bracketed(g)
	if parts := bracketGroups(inner); ok && len(parts) > 1 {
		var ps []string
		for _, p := range parts {
			ps = append(ps, pieces(p)...)
		}
		ps[0] = "[" + ps[0]
		ps[len(ps)-1] += "]"
		return ps
	}
	return strings.Fields(g)
}

// bracketed is what g holds inside its brackets when g is one bracket group: "[-a [-b]]" holds "-a [-b]", and
// "[-a] [-b]" is two groups, so false.
func bracketed(g string) (string, bool) {
	if !strings.HasPrefix(g, "[") {
		return "", false
	}
	depth := 0
	for i, r := range g {
		switch r {
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return g[1:i], i == len(g)-1
			}
		}
	}
	return "", false
}

// bracketGroups splits a synopsis at its spaces outside brackets: "-a [-b <c>] [-d]" is "-a", "[-b <c>]" and "[-d]".
func bracketGroups(s string) []string {
	var groups []string
	depth, start := 0, -1
	for i, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case r == ' ' && depth == 0:
			if start >= 0 {
				groups, start = append(groups, s[start:i]), -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		groups = append(groups, s[start:])
	}
	return groups
}

// wrap breaks text at its spaces into lines of width columns at most, the first starting with first and the rest
// with rest, the words one space apart. A word wider than a line has one to itself.
func wrap(text, first, rest string, width int) []string {
	return wrapWords(strings.Fields(text), first, rest, width)
}

// wrapWords is wrap of words already split.
func wrapWords(ws []string, first, rest string, width int) []string {
	var lines []string
	line, words := first, 0
	for _, word := range ws {
		if words > 0 && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width {
			lines, line, words = append(lines, line), rest, 0
		}
		if words > 0 {
			line += " "
		}
		line, words = line+word, words+1
	}
	return append(lines, line)
}

// shownDefault is a flag's default as -h writes it, and false when it is the zero value of its type, which Go's flag
// package leaves out too. A duration is in its short form, 30m for 30m0s and 2h for 2h0m0s, as the synopses write
// one. A string is bare when it is one word, as the synopses write it, and quoted when it holds a space or a quote.
func shownDefault(fl *flag.Flag) (string, bool) {
	typ := reflect.TypeOf(fl.Value)
	zero := reflect.Zero(typ)
	if typ.Kind() == reflect.Pointer {
		zero = reflect.New(typ.Elem())
	}
	if z, ok := zero.Interface().(flag.Value); ok && z.String() == fl.DefValue {
		return "", false
	}
	if g, ok := fl.Value.(flag.Getter); ok {
		switch g.Get().(type) { // the type alone: the value may be one parsed before -h
		case time.Duration:
			if d, err := time.ParseDuration(fl.DefValue); err == nil {
				return shortDuration(d), true
			}
		case string:
			if fl.DefValue == "" || strings.ContainsAny(fl.DefValue, " \t\"'`") {
				return strconv.Quote(fl.DefValue), true
			}
		}
	}
	return fl.DefValue, true
}

// shortDuration is d as Go writes it, its zero minutes and seconds left off: 30m, 2h, 1h30m, 90s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// mistake writes what follows a mistake's line, and the choices when it has some: the synopsis and the line that
// prints the flags. The flags themselves are -h's, so a mistake fits on one screen.
func (f *flags) mistake(w io.Writer) {
	f.synopsisLines(w)
	fmt.Fprintf(w, "%q prints its flags.\n", f.self+" "+f.cmd+" -h")
}

// undefined is how Go's flag package starts the error for a flag it does not hold, the name after the dash (seen:
// go1.26.0, flag.go:1115).
const undefined = "flag provided but not defined: -"

// Go's flag package's other errors, which why writes in laug's words (seen: go1.26.0, flag.go): a value its flag's
// type does not parse, with what was typed, the flag and why; the same for a boolean, given as -name=value; a flag
// last on the line with no value after it; and a word that starts with a dash and is no flag's shape.
var (
	badValue  = regexp.MustCompile(`^invalid value "(.*)" for flag -([^ ]+): (.*)$`)
	badSwitch = regexp.MustCompile(`^invalid boolean value "(.*)" for -([^ ]+): (.*)$`)
	noValue   = regexp.MustCompile(`^flag needs an argument: -(.+)$`)
	badSyntax = regexp.MustCompile(`^bad flag syntax: (.*)$`)
)

// why is a parse error in laug's words, the flag and what was typed first: "-ttl 5x: a duration, 30m say". A flag it
// does not hold gets the nearest names. An error of another shape is Go's, as it came.
func (f *flags) why(err error) string {
	s := err.Error()
	if name, ok := strings.CutPrefix(s, undefined); ok {
		var names []string
		f.VisitAll(func(fl *flag.Flag) { names = append(names, fl.Name) })
		return "no flag -" + name + didYouMean("-", name, names)
	}
	if m := badValue.FindStringSubmatch(s); m != nil {
		if fl := f.Lookup(m[2]); fl != nil {
			return "-" + fl.Name + " " + shellWord(m[1]) + ": " + takes(fl, m[3] == "value out of range")
		}
	}
	if m := badSwitch.FindStringSubmatch(s); m != nil {
		return "-" + m[2] + "=" + shellWord(m[1]) + ": true or false, and -" + m[2] + " alone is true"
	}
	if m := noValue.FindStringSubmatch(s); m != nil {
		if fl := f.Lookup(m[1]); fl != nil {
			return "-" + fl.Name + " needs a value after it: " + takes(fl, false)
		}
	}
	if m := badSyntax.FindStringSubmatch(s); m != nil {
		return shellWord(m[1]) + ": not a flag; a flag is -name, -name value or -name=value"
	}
	return s
}

// takes is what fl's value must be, with an example, its default when it has one: a duration, 30m say; a whole
// number; a number; else the word its -h names the value by, after "an" when it starts with a, e, i or o (an address,
// an id), else "a" (a file, a url).
func takes(fl *flag.Flag, tooLarge bool) string {
	example, has := shownDefault(fl)
	say := func(kind, otherwise string) string {
		if !has {
			example = otherwise
		}
		return kind + ", " + example + " say"
	}
	if g, ok := fl.Value.(flag.Getter); ok {
		switch g.Get().(type) {
		case time.Duration:
			return say("a duration", "30s")
		case int, int64, uint, uint64:
			if tooLarge {
				return "a whole number, and that one is too large"
			}
			return say("a whole number", "3")
		case float64:
			if tooLarge {
				return "a number, and that one is too large"
			}
			return say("a number", "0.5")
		}
	}
	name, _ := flag.UnquoteUsage(fl)
	if name != "" && strings.ContainsAny(name[:1], "aeio") {
		return "an " + name
	}
	return "a " + name
}

// parse parses args with the flags on either side of the arguments, since Go's flag package stops at the first
// argument (the flag package's docs), so "gate run 1.31.0 -fake" sets -fake. A "--" in a flag's place ends
// the flags. With ok false the command returns code: 0 after -h, its usage on stdout; 2 after a bad flag, the error
// in laug's words (why), and the synopsis on stderr.
func (f *flags) parse(args []string, stdout, stderr io.Writer) (pos []string, code int, ok bool) {
	for {
		err := f.Parse(args)
		if errors.Is(err, flag.ErrHelp) {
			if summary, found := summaryOf(f.cmd); found {
				fmt.Fprintf(stdout, "laug · %s: %s\n", f.cmd, summary)
			} else {
				fmt.Fprintf(stdout, "laug · %s\n", f.cmd)
			}
			f.usage(stdout)
			return nil, exitDone, false
		}
		if err != nil {
			fmt.Fprintf(stderr, "laug · %s: %s\n", f.cmd, f.why(err))
			f.mistake(stderr)
			return nil, exitUsage, false
		}
		rest := f.Args()
		if f.ended(args[:len(args)-len(rest)]) {
			return append(pos, rest...), exitDone, true
		}
		if len(rest) == 0 {
			return pos, exitDone, true
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
}

// ended reports whether Parse, having consumed these args, stopped at a "--" and not at an argument. It walks them
// as Parse did: a flag, then its value unless it is boolean or carries "=". A "--" that is a flag's value is not an
// end.
func (f *flags) ended(consumed []string) bool {
	for i := 0; i < len(consumed); i++ {
		a := consumed[i]
		if a == "--" {
			return true
		}
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if fl := f.Lookup(name); fl != nil {
			if b, isBool := fl.Value.(interface{ IsBoolFlag() bool }); isBool && b.IsBoolFlag() {
				continue
			}
		}
		i++
	}
	return false
}

// didYouMean is "; did you mean <name>?" for the names nearest typed, each with prefix before it, a dash for a flag,
// or "" when none is near.
func didYouMean(prefix, typed string, names []string) string {
	near := nearest(typed, names)
	if len(near) == 0 {
		return ""
	}
	for i := range near {
		near[i] = prefix + near[i]
	}
	if len(near) == 1 {
		return "; did you mean " + near[0] + "?"
	}
	return "; did you mean " + strings.Join(near[:len(near)-1], ", ") + " or " + near[len(near)-1] + "?"
}

// nearest returns the names fewest edits from typed, in their order: 2 edits at most for a word of 6 letters or more,
// 1 for a shorter one, so that a short word is not near everything, and none for one letter. An edit puts in, takes
// out or changes one letter, or swaps two side by side.
func nearest(typed string, names []string) []string {
	best := 2
	if n := utf8.RuneCountInString(typed); n < 6 {
		best = min(1, n-1)
	}
	var near []string
	for _, n := range names {
		switch d := edits(typed, n); {
		case n == typed || d > best:
		case d < best:
			best, near = d, []string{n}
		default:
			near = append(near, n)
		}
	}
	return near
}

// edits is the number of edits from a to b, as nearest counts them: the optimal string alignment distance.
func edits(a, b string) int {
	s, t := []rune(a), []rune(b)
	d := make([][]int, len(s)+1)
	for i := range d {
		d[i] = make([]int, len(t)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(s); i++ {
		for j := 1; j <= len(t); j++ {
			change := 1
			if s[i-1] == t[j-1] {
				change = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+change)
			if i > 1 && j > 1 && s[i-1] == t[j-2] && s[i-2] == t[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(s)][len(t)]
}

// writeJSON writes v as what -json prints: one JSON document on one line, and nothing else.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// commit is HEAD's commit, set by make build through -ldflags "-X main.commit=<sha>", with -dirty when git status
// printed anything at build time. A build without the flag leaves it empty, and buildCommit falls back.
var commit string

// readBuildInfo is debug.ReadBuildInfo. Tests replace it.
var readBuildInfo = debug.ReadBuildInfo

// buildCommit returns the commit this binary was built from: make build's stamp; else the VCS stamp a plain go build
// or go install writes inside a git checkout, with -dirty when it records a modified tree; else dev. A go test binary
// records no revision (seen: go version -m, go1.26.0).
func buildCommit() string {
	if commit != "" {
		return commit
	}
	info, ok := readBuildInfo()
	if !ok {
		return "dev"
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return "dev"
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return rev
}
