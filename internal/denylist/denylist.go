// Package denylist reads a deny list, private words kept out of the tree, by the one rule its three readers keep:
// check/denylist.sh's pass over a tree, bin/obs pack's word check (_obs/obs.sh, deny_check, which reads
// the list through check/denylist.sh) and laug orchestrator record's leak check (internal/orchestrator, through this
// package). Each takes the list's path from LAUG_DENYLIST or a flag; no tracked file names a line of it.
//
// The rule. The list is read a line at a time, and a line is named by its own number in the file, comments and blank
// lines counted: "denylist line N". A trailing \r is dropped, so a CRLF list reads as an LF one. A list that starts
// with a byte-order mark is refused at line 1, since the mark would hide that line's re: or #. A line that is empty,
// holds only white space, or whose first byte that is not white space is #, is skipped. A line that starts "re:" is
// the rest of it as a regular expression; any other line is a fixed string, matched as a substring. Both match with
// ASCII letters folded, a to z as A to Z, and every other byte as written. Text is matched a line at a time and byte
// by byte, as grep reads it under LC_ALL=C: . and a class take one byte, and ^ and $ are a line's ends.
//
// A re: line is held to the subset that grep -E, Go's regexp and Python's re read alike, so that the list means one
// thing to every reader. The bytes are printable ASCII, space to ~, 256 of them at most. An atom is one of:
//
//   - a byte that is none of . [ ( ) * + ? { | ^ $ \ (so ] and } stand for themselves);
//   - . for any byte;
//   - \ before one of . [ ] ( ) { } * + ? | ^ $ \, for that byte; no other escape (no \b, \d, \w, \1);
//   - a class [...] or [^...]: bytes other than \ [ and ], none of them doubled & ~ or |; a range of two digits, two
//     lower case letters or two upper case letters, in order; a - first or last; never empty; never starting with :,
//     since GNU grep refuses [:ab:] as a misspelt [[:ab:]];
//   - a group (...) of one or more branches, never (?...), inside 31 others at most;
//   - an anchor, ^ or $, which takes no quantifier.
//
// A branch is one or more atoms, each with at most one quantifier after it: ? * + {m} {m,} or {m,n}, m and n
// written with no leading 0, m <= n <= 255. A quantifier at a branch's start, or after another (a lazy or possessive
// form), is refused. Branches are joined by |, and none is empty. The counts nested around any atom multiply to 1000
// or less, Go's own limit on a count. A re: line outside the subset, one Go's regexp does not compile, or one that
// matches the empty string, which every line holds, is refused: Parse returns a LineError naming its line, never its
// text, and no engine's message, which quotes the expression.
//
// The standard library only (cmd/laug's TestStdlibOnly).
package denylist

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// The reasons a line is refused, word for word as check/denylist.sh prints them.
const (
	WhySubset = "a re: line outside the subset grep -E, Go and Python read alike, or one that does not compile"
	WhyEmpty  = "a re: line that matches the empty string"
	WhyBOM    = "a byte-order mark"
)

// LineError is a line of the list that no reader takes. Its text names the line, never what it holds.
type LineError struct {
	Line int
	Why  string
}

func (e *LineError) Error() string { return fmt.Sprintf("denylist line %d: %s", e.Line, e.Why) }

// Rule is one line of the list that is not skipped.
type Rule struct {
	Line int  // the file's own line number, from 1
	Re   bool // a re: line; else a plain one
	word string
	re   *regexp.Regexp
}

// List is the rules of a list, in the file's order.
type List struct {
	Rules []Rule
}

// Read reads the list at path. A file that is not there is no error: Read returns nil, and each reader says so. A
// link to nothing is an error, as a directory is: the list is meant to be there.
func Read(path string) (*List, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, lerr := os.Lstat(path); lerr == nil {
			return nil, fmt.Errorf("%s is not a file", path)
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads a list's bytes by the rule; the first line it refuses stops it.
func Parse(b []byte) (*List, error) {
	if bytes.HasPrefix(b, []byte("\xef\xbb\xbf")) {
		return nil, &LineError{Line: 1, Why: WhyBOM}
	}
	l := &List{}
	lines := bytes.Split(b, []byte("\n"))
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	for i, raw := range lines {
		s := strings.TrimSuffix(string(raw), "\r")
		if t := strings.TrimLeft(s, " \t\v\f\r"); t == "" || t[0] == '#' {
			continue
		}
		r := Rule{Line: i + 1}
		if p, ok := strings.CutPrefix(s, "re:"); ok {
			re, why := compile(p)
			if why != "" {
				return nil, &LineError{Line: i + 1, Why: why}
			}
			r.Re, r.re = true, re
		} else {
			r.word = lowerASCII(s)
		}
		l.Rules = append(l.Rules, r)
	}
	return l, nil
}

// Count is the list's plain lines and its re: lines; a nil list has none.
func (l *List) Count() (plain, re int) {
	if l == nil {
		return 0, 0
	}
	for _, r := range l.Rules {
		if r.Re {
			re++
		} else {
			plain++
		}
	}
	return plain, re
}

// Summary is the list's count as each reader's summary line puts it: "3 plain lines and 1 re: line".
func (l *List) Summary() string {
	p, r := l.Count()
	return plural(p, "plain line") + " and " + plural(r, "re: line")
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

// Match says whether a line of s holds a match of r.
func (r Rule) Match(s string) bool { return len(r.find(s, 1)) > 0 }

// Spans are the byte ranges of s that r matches: a plain line's every place, a re: line's matches line by line,
// leftmost-longest and apart.
func (r Rule) Spans(s string) [][2]int { return r.find(s, -1) }

func (r Rule) find(s string, n int) [][2]int {
	var out [][2]int
	if !r.Re {
		low := lowerASCII(s)
		for i := 0; n < 0 || len(out) < n; {
			j := strings.Index(low[i:], r.word)
			if j < 0 {
				break
			}
			out = append(out, [2]int{i + j, i + j + len(r.word)})
			i += j + 1
		}
		return out
	}
	start := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		for _, m := range findBytes(r.re, strings.TrimSuffix(line, "\n"), n) {
			out = append(out, [2]int{start + m[0], start + m[1]})
			if n > 0 && len(out) >= n {
				return out
			}
		}
		start += len(line)
	}
	return out
}

// findBytes runs re over text byte by byte: a byte over 127 is read as one character, U+0080 to U+00FF, so . and a
// class take one byte, as grep does under LC_ALL=C. re holds ASCII alone, and no ASCII letter folds with a character
// in that range, so (?i) folds ASCII letters and nothing else here. The indexes are text's.
func findBytes(re *regexp.Regexp, text string, n int) [][]int {
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return re.FindAllStringIndex(text, n)
	}
	var b strings.Builder
	at := make([]int, 0, 2*len(text)+1)
	for i := 0; i < len(text); i++ {
		w := b.Len()
		b.WriteRune(rune(text[i]))
		for ; w < b.Len(); w++ {
			at = append(at, i)
		}
	}
	at = append(at, len(text))
	ms := re.FindAllStringIndex(b.String(), n)
	for _, m := range ms {
		m[0], m[1] = at[m[0]], at[m[1]]
	}
	return ms
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// compile holds p, a re: line less its re:, to the subset, then compiles it case-insensitive and leftmost-longest;
// why is "" when it takes it.
func compile(p string) (re *regexp.Regexp, why string) {
	if p == "" {
		return nil, WhyEmpty
	}
	if !inSubset(p) {
		return nil, WhySubset
	}
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		return nil, WhySubset // never err's text, which quotes p
	}
	if re.MatchString("") {
		return nil, WhyEmpty
	}
	re.Longest()
	return re, ""
}

// subset reads a pattern by the package's grammar, at i. check/denylist.sh's re_subset is the same reader in bash,
// check for check, in the same order.
type subset struct {
	p     string
	i     int
	depth int
}

// maxProduct is the most the counts nested around an atom may multiply to; maxCount the most one count may be;
// maxBytes the longest pattern and maxDepth the deepest nest of groups, which keep the bash reader's recursion short
// (800 nested groups crashed it).
const (
	maxProduct = 1000
	maxCount   = 255
	maxBytes   = 256
	maxDepth   = 32
)

var countRE = regexp.MustCompile(`^\{(0|[1-9][0-9]{0,2})(,(0|[1-9][0-9]{0,2})?)?\}`)

func inSubset(p string) bool {
	if len(p) > maxBytes {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < ' ' || p[i] > '~' {
			return false
		}
	}
	x := &subset{p: p}
	_, ok := x.alternation()
	return ok && x.i == len(p)
}

func (x *subset) peek(k int) byte {
	if x.i+k < len(x.p) {
		return x.p[x.i+k]
	}
	return 0
}

// alternation reads branches joined by |, up to a ) or the end; n is the largest product of counts inside.
func (x *subset) alternation() (n int, ok bool) {
	n = 1
	for {
		m, ok := x.branch()
		if !ok {
			return 0, false
		}
		n = max(n, m)
		if x.peek(0) != '|' {
			return n, true
		}
		x.i++
	}
}

// branch reads one or more atoms, each with its quantifier, up to a |, a ) or the end.
func (x *subset) branch() (n int, ok bool) {
	n = 1
	pieces := 0
	for x.i < len(x.p) && x.p[x.i] != '|' && x.p[x.i] != ')' {
		inner, anchor, ok := x.atom()
		if !ok {
			return 0, false
		}
		count, quantified, ok := x.quantifier()
		if !ok || quantified && anchor {
			return 0, false
		}
		if quantified {
			inner *= count
		}
		if inner > maxProduct {
			return 0, false
		}
		n = max(n, inner)
		pieces++
	}
	return n, pieces > 0
}

// atom reads one atom; n is the product of counts inside a group, 1 for any other atom.
func (x *subset) atom() (n int, anchor, ok bool) {
	switch c := x.p[x.i]; {
	case c == '(':
		x.i++
		if x.depth++; x.depth > maxDepth || x.peek(0) == '?' {
			return 0, false, false
		}
		n, ok := x.alternation()
		if !ok || x.peek(0) != ')' {
			return 0, false, false
		}
		x.i++
		x.depth--
		return n, false, true
	case c == '[':
		return 1, false, x.class()
	case c == '\\':
		if e := x.peek(1); e == 0 || !strings.ContainsRune(`.[]()*+?{}|^$\`, rune(e)) {
			return 0, false, false
		}
		x.i += 2
		return 1, false, true
	case c == '^' || c == '$':
		x.i++
		return 1, true, true
	case strings.IndexByte("*+?{", c) >= 0:
		return 0, false, false
	}
	x.i++
	return 1, false, true
}

// quantifier reads the quantifier after an atom, if one is there; count is its largest count, 1 for ? * and +.
func (x *subset) quantifier() (count int, quantified, ok bool) {
	switch c := x.peek(0); {
	case c == '*' || c == '+' || c == '?':
		x.i++
		count = 1
	case c == '{':
		m := countRE.FindStringSubmatch(x.p[x.i:])
		if m == nil {
			return 0, false, false
		}
		lo, _ := strconv.Atoi(m[1])
		count = lo
		if m[3] != "" {
			hi, _ := strconv.Atoi(m[3])
			if hi < lo {
				return 0, false, false
			}
			count = hi
		}
		if lo > maxCount || count > maxCount {
			return 0, false, false
		}
		x.i += len(m[0])
	default:
		return 1, false, true
	}
	if c := x.peek(0); c != 0 && strings.IndexByte("*+?{", c) >= 0 {
		return 0, false, false
	}
	return count, true, true
}

// class reads a bracket class, its [ first.
func (x *subset) class() bool {
	x.i++
	if x.peek(0) == '^' {
		x.i++
	}
	if x.peek(0) == ':' {
		return false
	}
	start := x.i
	for x.i < len(x.p) {
		c, next := x.p[x.i], x.peek(1)
		switch {
		case c == ']':
			if x.i == start {
				return false
			}
			x.i++
			return true
		case c == '\\' || c == '[':
			return false
		case c == '-':
			if x.i != start && next != ']' {
				return false
			}
			x.i++
		case next == '-' && x.peek(2) != 0 && x.peek(2) != ']':
			if hi := x.peek(2); !sameKind(c, hi) || c > hi {
				return false
			}
			x.i += 3
		case (c == '&' || c == '~' || c == '|') && next == c:
			return false
		default:
			x.i++
		}
	}
	return false
}

// sameKind says whether a and b are two digits, two lower case letters or two upper case letters.
func sameKind(a, b byte) bool {
	in := func(c, lo, hi byte) bool { return lo <= c && c <= hi }
	return in(a, '0', '9') && in(b, '0', '9') || in(a, 'a', 'z') && in(b, 'a', 'z') || in(a, 'A', 'Z') && in(b, 'A', 'Z')
}
