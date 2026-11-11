package check

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The kept records' page, docs/RECORDS.md. A section is a "## " heading; its first line of text names its records,
// each a code span under a results directory ({a,b} expanded): a record kept.txt names, or files of one, as a glob
// record's files are named; then its quotes, each a fenced block opened as "```quote <file>", a line "<n>: <the
// file's line n>" a line, byte for byte; then one line "<how>: `<command>`" that judges the record again or runs it
// again. TestKept holds the page to docs/tables/src/kept.txt and to the files. The page's last section, allowHead's
// table of cited paths the tree no longer has, is TestCitedPaths' (cited_paths_test.go) and is not read here.

const (
	recordsPage = "docs/RECORDS.md"
	keptList    = "docs/tables/src/kept.txt"
)

var (
	quoteOpen = regexp.MustCompile("^```quote (\\S+)$")
	quoteLine = regexp.MustCompile(`^([1-9][0-9]*): (.*)$`)
	spanRE    = regexp.MustCompile("`([^`]+)`")
	howRE     = regexp.MustCompile("^[A-Z][^`]*: `((?:bin/laug|make) [^`]+)`$")
)

// A keptSection is one section of the page: its heading's line, its records, its quotes and its commands.
type keptSection struct {
	at       int
	title    string
	records  []string
	quotes   []keptQuote
	commands []string
}

// A keptQuote is one quoted line: the page's line, the file and the file's line it names, and its text.
type keptQuote struct {
	at   int
	file string
	n    int
	text string
}

// parseRecords reads the page into its sections; a line it cannot place is an error.
func parseRecords(body string) ([]keptSection, []string) {
	var out []keptSection
	var bad []string
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if l == allowHead { // the page's last section, TestCitedPaths' table (cited_paths_test.go), not a record's
			break
		}
		if t, ok := strings.CutPrefix(l, "## "); ok {
			out = append(out, keptSection{at: i + 1, title: t})
			continue
		}
		if len(out) == 0 || l == "" {
			continue
		}
		s := &out[len(out)-1]
		if m := quoteOpen.FindStringSubmatch(l); m != nil {
			i++
			for ; i < len(lines) && lines[i] != "```"; i++ {
				q := quoteLine.FindStringSubmatch(lines[i])
				if q == nil {
					bad = append(bad, fmt.Sprintf("%s:%d: a quote is <n>: <the line>", recordsPage, i+1))
					continue
				}
				n, _ := strconv.Atoi(q[1])
				s.quotes = append(s.quotes, keptQuote{i + 1, m[1], n, q[2]})
			}
			continue
		}
		if strings.HasPrefix(l, "```") {
			bad = append(bad, fmt.Sprintf("%s:%d: a fenced block here is a quote, ```quote <file>", recordsPage, i+1))
			continue
		}
		if m := howRE.FindStringSubmatch(l); m != nil {
			s.commands = append(s.commands, m[1])
			continue
		}
		if s.records == nil {
			s.records = []string{}
			for _, m := range spanRE.FindAllStringSubmatch(l, -1) {
				if inResults(m[1]) {
					s.records = append(s.records, braces(m[1])...)
				}
			}
		}
	}
	return out, bad
}

// holds says whether record, a kept.txt path, holds file: the record itself, a file under a directory record, or a
// file a glob record (load/results/<id>.*) matches.
func holds(record, file string) bool {
	if strings.HasSuffix(record, "/") {
		return strings.HasPrefix(file, record)
	}
	ok, _ := path.Match(record, file)
	return ok || record == file
}

// readKeptList reads kept.txt's records, its # comments and blank lines skipped.
func readKeptList(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(keptList)))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		rec, _, _ := strings.Cut(l, "\t")
		out = append(out, rec)
	}
	return out
}

// keptProblems is every way the page's sections part from kept, the records kept.txt names, and from the files that
// read returns, by path from the checkout's root.
func keptProblems(sections []keptSection, kept []string, read func(string) (string, error)) []string {
	var out []string
	where := map[string]string{}
	for _, s := range sections {
		if len(s.records) == 0 {
			out = append(out, fmt.Sprintf("%s:%d: %q names no record on its first line", recordsPage, s.at, s.title))
		}
		var mine []string // the kept records the section's paths name
		for _, p := range s.records {
			n := 0
			for _, r := range kept {
				if !holds(r, p) || slices.Contains(mine, r) {
					continue
				}
				n++
				if was, dup := where[r]; dup {
					out = append(out, fmt.Sprintf("%s:%d: %s has a section already, %q", recordsPage, s.at, r, was))
				}
				where[r] = s.title
				mine = append(mine, r)
			}
			if n == 0 && !slices.ContainsFunc(mine, func(r string) bool { return holds(r, p) }) {
				out = append(out, fmt.Sprintf("%s:%d: %s is in no record %s names", recordsPage, s.at, p, keptList))
			}
		}
		if len(s.quotes) == 0 {
			out = append(out, fmt.Sprintf("%s:%d: %q quotes 0 lines, not one or more", recordsPage, s.at, s.title))
		}
		if len(s.commands) != 1 {
			out = append(out, fmt.Sprintf("%s:%d: %q gives %d commands, not one line \"<how>: `<command>`\"",
				recordsPage, s.at, s.title, len(s.commands)))
		}
		for _, q := range s.quotes {
			if !slices.ContainsFunc(mine, func(r string) bool { return holds(r, q.file) }) {
				out = append(out, fmt.Sprintf("%s:%d: %s is in no record of its section", recordsPage, q.at, q.file))
				continue
			}
			body, err := read(q.file)
			if err != nil {
				out = append(out, fmt.Sprintf("%s:%d: %v", recordsPage, q.at, err))
				continue
			}
			lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
			switch {
			case q.n > len(lines):
				out = append(out, fmt.Sprintf("%s:%d: %s:%d is past its %d lines", recordsPage, q.at, q.file, q.n,
					len(lines)))
			case lines[q.n-1] != q.text:
				out = append(out, fmt.Sprintf("%s:%d: %s:%d reads %q, not the quote %q", recordsPage, q.at, q.file,
					q.n, lines[q.n-1], q.text))
			}
		}
	}
	for _, r := range kept {
		if _, ok := where[r]; !ok {
			out = append(out, fmt.Sprintf("%s: %s, in %s, has no section", recordsPage, r, keptList))
		}
	}
	return out
}

// Every record kept.txt names has one section of docs/RECORDS.md, and every line the page quotes is its file's line,
// byte for byte.
func TestKept(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(recordsPage)))
	if err != nil {
		t.Fatal(err)
	}
	sections, bad := parseRecords(string(b))
	for _, p := range bad {
		t.Error(p)
	}
	if len(sections) == 0 {
		t.Fatalf("%s: no section", recordsPage)
	}
	read := func(p string) (string, error) {
		b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(p)))
		return string(b), err
	}
	for _, p := range keptProblems(sections, readKeptList(t), read) {
		t.Error(p)
	}
}

// keptProblems finds a changed quote, a line past the file, a quote outside its section's records, a kept record
// with no section, a section with no command or no quote, and passes a page that holds to its files. The page and
// files are made up.
func TestKeptPlants(t *testing.T) {
	files := map[string]string{
		"cells/results/2099-01-01-a.txt":           "one\ntwo\nthree\nfour\nfive\nsix\n",
		"gate/results/g20990101-000000/verdict.md": "## v\n\n**ADVANCE**\n",
	}
	read := func(p string) (string, error) {
		s, ok := files[p]
		if !ok {
			return "", fmt.Errorf("%s: no such file", p)
		}
		return s, nil
	}
	quote := func(file string, ns ...int) string {
		s := "```quote " + file + "\n"
		for _, n := range ns {
			s += fmt.Sprintf("%d: %s\n", n, strings.Split(files[file], "\n")[n-1])
		}
		return s + "```\n"
	}
	good := "# p\n\n## 1. A\n\n`cells/results/2099-01-01-a.txt`: it shows.\n\n" +
		quote("cells/results/2099-01-01-a.txt", 1, 2, 3, 4, 5) + "\nRun it again: `bin/laug cell up`\n"
	kept := []string{"cells/results/2099-01-01-a.txt"}
	for _, c := range []struct {
		name string
		page string
		kept []string
		want string
	}{
		{"a page that holds", good, kept, ""},
		{"a changed quote", strings.Replace(good, "3: three", "3: thre", 1), kept, "not the quote"},
		{"a line past the file", strings.Replace(good, "5: five", "9: five", 1), kept, "past its 6 lines"},
		{"a kept record with no section", good, append(kept, "gate/results/g20990101-000000/"), "has no section"},
		{"no command", strings.Replace(good, "Run it again: `bin/laug cell up`", "", 1), kept, "gives 0 commands"},
		{"no quote", strings.Replace(good, quote("cells/results/2099-01-01-a.txt", 1, 2, 3, 4, 5), "", 1), kept,
			"quotes 0 lines"},
		{"a glob record by its files", strings.Replace(good, "`cells/results/2099-01-01-a.txt`",
			"`cells/results/2099-01-01-a{,.verdict}.txt`", 1), []string{"cells/results/2099-01-01-a*"}, ""},
		{"a path no record holds", strings.Replace(good, "`cells/results/2099-01-01-a.txt`",
			"`cells/results/2099-01-01-a.txt` and `cells/results/2099-01-01-b.txt`", 1), kept, "in no record"},
		{"a quote outside its records", good + quote("gate/results/g20990101-000000/verdict.md", 3), kept,
			"in no record of its section"},
	} {
		sections, bad := parseRecords(c.page)
		got := strings.Join(append(bad, keptProblems(sections, c.kept, read)...), "\n")
		switch {
		case c.want == "" && got != "":
			t.Errorf("%s: %s", c.name, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("%s: got %q, want a problem with %q", c.name, got, c.want)
		}
	}
}
