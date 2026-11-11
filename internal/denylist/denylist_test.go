package denylist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every list here is planted, with made-up words: zorblax, qx7, blue heron lake and the like.

// A rule is named by its line in the file, comments and blank lines counted; an empty line, one of white space and
// one whose first byte that is not white space is # are skipped; a trailing \r goes; a line that starts "re:" and
// nothing else is a regular expression.
func TestParse(t *testing.T) {
	list := "# planted\nzorblax\r\n\n   # indented\n \t \r\nre:qx7[0-9]\nblue heron lake\n re:qx7\nRE:qx7\n" +
		"#zorblax\ntrailing  "
	l, err := Parse([]byte(list))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range l.Rules {
		got = append(got, fmt.Sprintf("%d %v %q", r.Line, r.Re, r.word))
	}
	want := []string{`2 false "zorblax"`, `6 true ""`, `7 false "blue heron lake"`, `8 false " re:qx7"`,
		`9 false "re:qx7"`, `11 false "trailing  "`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rules %q, want %q", got, want)
	}
	if s := l.Summary(); s != "5 plain lines and 1 re: line" {
		t.Errorf("summary %q", s)
	}
	if s := (*List)(nil).Summary(); s != "0 plain lines and 0 re: lines" {
		t.Errorf("a nil list's summary %q", s)
	}
	if one, err := Parse([]byte("re:qx7")); err != nil || one.Summary() != "0 plain lines and 1 re: line" {
		t.Errorf("one re: line, no newline: %v, %v", one, err)
	}
	if none, err := Parse(nil); err != nil || len(none.Rules) != 0 {
		t.Errorf("no bytes: %v, %v", none, err)
	}
}

// A plain line matches its case variants, ASCII letters folded and other bytes as written, and its . [ ] * as written;
// a re: line with a boundary hits and misses as written, a line of the text at a time, and its . takes any byte but a
// newline, a 0 byte too.
func TestMatch(t *testing.T) {
	l, err := Parse([]byte("zorblax\nre:(^|[^a-z0-9])qx7([^a-z0-9]|$)\nre:^vex[0-9]{2,3}$\n\xc3\xa9tang\nre:a.b\n" +
		"Brol.Vix[2]*\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		text string
		want []int
	}{
		{"zorblax", []int{1}},
		{"see ZorBLAX here", []int{1}},
		{"ZORBLAX", []int{1}},
		{"zorbla x", nil},
		{"qx7 first", []int{2}},
		{"last, QX7", []int{2}},
		{"(qx7)", []int{2}},
		{"aqx7 and qx70 and qx7b", nil},
		{"vex12", []int{3}},
		{"vex123", []int{3}},
		{"vex1", nil},
		{"vex1234", nil},
		{"a vex12", nil},
		{"one line\nvex12\nanother", []int{3}},
		{"vex12\r", nil},
		{"zq\nx7", nil},
		{"q\nqx7", []int{2}},
		{"\xc3\xa9TANG", []int{4}},
		{"\xc3\x89tang", nil},
		{"a\xffb", []int{5}},
		{"a\xc3\xa9b", nil},
		{"aXb", []int{5}},
		{"a\x00b", []int{5}},
		{"a\nb", nil},
		{"see brol.VIX[2]*", []int{6}},
		{"BROLxVIX2", nil},
		{"", nil},
	} {
		var got []int
		for _, r := range l.Rules {
			if r.Match(c.text) {
				got = append(got, r.Line)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%q: lines %v, want %v", c.text, got, c.want)
		}
	}
}

// Spans are byte ranges of the text, line by line, whatever bytes come before.
func TestSpans(t *testing.T) {
	l, err := Parse([]byte("zorblax\nre:qx7+\n"))
	if err != nil {
		t.Fatal(err)
	}
	text := "\xc3\xa9 ZORBLAX qx77\nzorblax qx7"
	if got := fmt.Sprint(l.Rules[0].Spans(text)); got != "[[3 10] [16 23]]" {
		t.Errorf("plain spans %s", got)
	}
	if got := fmt.Sprint(l.Rules[1].Spans(text)); got != "[[11 15] [24 27]]" {
		t.Errorf("re: spans %s", got)
	}
}

// subsetCase is a pattern of a re: line, less its re:, and why every reader refuses it, "" when each takes it.
type subsetCase struct{ p, why string }

// subsetTable is testdata/subset.txt's table, then the subset's limits: 256 bytes and groups 32 deep. 800 nested
// groups once crashed the bash reader.
func subsetTable(t *testing.T) []subsetCase {
	t.Helper()
	b, err := os.ReadFile("testdata/subset.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []subsetCase
	for i, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		verdict, p, _ := strings.Cut(line, " ")
		why, ok := map[string]string{"take": "", "subset": WhySubset, "empty": WhyEmpty}[verdict]
		if strings.HasPrefix(p, `"`) {
			p, err = strconv.Unquote(p)
		}
		if !ok || err != nil {
			t.Fatalf("testdata/subset.txt:%d: %q: no verdict and pattern", i+1, line)
		}
		out = append(out, subsetCase{p, why})
	}
	nest := func(n int) string { return strings.Repeat("(", n) + "q" + strings.Repeat(")", n) }
	return append(out, subsetCase{nest(maxDepth), ""}, subsetCase{nest(maxDepth + 1), WhySubset},
		subsetCase{nest(800), WhySubset}, subsetCase{strings.Repeat("q", maxBytes), ""},
		subsetCase{strings.Repeat("q", maxBytes+1), WhySubset})
}

// The subset takes every construct it names and refuses the rest, each with its reason: a construct one engine reads
// otherwise, one that does not compile, one past a limit, and one that matches the empty string.
func TestSubset(t *testing.T) {
	cases := subsetTable(t)
	if len(cases) < 100 {
		t.Fatalf("%d cases in testdata/subset.txt and the limits", len(cases))
	}
	for _, c := range cases {
		if _, why := compile(c.p); why != c.why {
			t.Errorf("%q: %q, want %q", c.p, why, c.why)
		}
	}
}

// A refused line stops Parse with its line number, the file's own, and its reason; the error holds neither the line's
// text nor an engine's message.
func TestRefusedLine(t *testing.T) {
	_, err := Parse([]byte("# planted\nzorblax\r\n\nre:(qx7\r\nre:x*\n"))
	var le *LineError
	if !errors.As(err, &le) || le.Line != 4 || le.Why != WhySubset {
		t.Fatalf("%v, want line 4: %s", err, WhySubset)
	}
	got := err.Error()
	if got != "denylist line 4: "+WhySubset || strings.Contains(got, "qx7") || strings.Contains(got, "`") {
		t.Errorf("%q", got)
	}
	if _, err := Parse([]byte("zorblax\nre:x*\n")); err == nil || err.Error() != "denylist line 2: "+WhyEmpty {
		t.Errorf("%v", err)
	}
	// A byte-order mark would make line 1 a plain line with 3 bytes in front, so its re: or # would not count.
	for _, list := range []string{"\xef\xbb\xbfre:qx7[0-9]\nzorblax\n", "\xef\xbb\xbf# planted\n"} {
		if _, err := Parse([]byte(list)); err == nil || err.Error() != "denylist line 1: "+WhyBOM {
			t.Errorf("%q: %v", list, err)
		}
	}
}

// A list that is not there is no list and no error; one that is there is read.
func TestRead(t *testing.T) {
	dir := t.TempDir()
	if l, err := Read(filepath.Join(dir, "absent.txt")); l != nil || err != nil {
		t.Fatalf("absent: %v, %v", l, err)
	}
	p := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(p, []byte("zorblax\nre:qx7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := Read(p); err != nil || l.Summary() != "1 plain line and 1 re: line" {
		t.Fatalf("%v, %v", l, err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("a directory read as a list")
	}
	gone := filepath.Join(dir, "gone.txt")
	if err := os.Symlink(filepath.Join(dir, "nothing.txt"), gone); err != nil {
		t.Fatal(err)
	}
	if l, err := Read(gone); l != nil || err == nil || err.Error() != gone+" is not a file" {
		t.Fatalf("a link to nothing: %v, %v", l, err)
	}
}
