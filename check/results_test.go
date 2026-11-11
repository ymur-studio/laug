package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The results READMEs. Each of the four results directories holds a README.md with two tables this test holds. Both
// are read from the public set (check/docs_test.go): a README says what the public tree holds, so it is the same file
// in both trees, and a record that is not kept is a row of docs/tables, not an entry.
//
//   cites    "## Which page cites each entry": one row per entry of the directory (a file, or a directory as name/),
//            its README.md aside, with the public .md files outside the four results directories that cite it, and
//            the count of every other public file outside them that does. A file cites an entry when its text holds
//            a path to it, from the root or, on a page under the same top directory, from results/; a glob or {a,b}
//            in such a path that matches it, with at least 4 characters before the first wildcard, or a name prefix
//            of 8 characters or more ending in - or _, as code that joins a name to it holds; such a glob or {a,b}
//            alone, on a page under the same top directory; or its name alone, with or without its extensions, when
//            the name holds a digit and 8 characters or more (a name a / follows names a directory).
//   outside  "## Recorded citations outside the tree": one row per result file of the directory whose bytes name a
//            file kept outside the public tree, with the count and the names. A name is a path under a results
//            directory that the public set does not hold, or a gate id with no run under gate/results there. The
//            names of the four tables together are the set: every result file of the directory whose bytes hold one
//            of them has its row.
//
// On a mismatch the test prints the table it wants, whole.

var resultsDirs = []string{"gate/results", "cells/results", "load/results", "faults/results"}

const (
	citesHead   = "## Which page cites each entry"
	outsideHead = "## Recorded citations outside the tree"
)

var (
	resultPathRE = regexp.MustCompile(`(?:(?:gate|cells|load|faults)/)?results/[A-Za-z0-9_.*?{},/\[\]-]*`)
	nameTokenRE  = regexp.MustCompile(`[A-Za-z0-9_.-]+`)
	patternRE    = regexp.MustCompile(`[A-Za-z0-9_.*?{},\[\]-]+`)
	gateIDRE     = regexp.MustCompile(`^g[0-9]{8}-[0-9]{6}$`)
	digitRE      = regexp.MustCompile(`[0-9]`)
)

// inResults says whether p, a path from the checkout's root, is under one of the four results directories.
func inResults(p string) bool {
	for _, d := range resultsDirs {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// entries is each results directory's entries, a directory as name/, README.md left out, sorted.
func entries(tracked map[string]bool) map[string][]string {
	out := map[string][]string{}
	for _, d := range resultsDirs {
		seen := map[string]bool{}
		for f := range tracked {
			rest, ok := strings.CutPrefix(f, d+"/")
			if !ok || rest == "README.md" {
				continue
			}
			name, _, sub := strings.Cut(rest, "/")
			if sub {
				name += "/"
			}
			if !seen[name] {
				seen[name] = true
				out[d] = append(out[d], name)
			}
		}
		slices.Sort(out[d])
	}
	return out
}

// bareNames maps a name a page may cite alone to the entries it names: an entry's name less its trailing / and,
// one at a time, its extensions, when the name holds a digit and 8 characters or more.
func bareNames(ents map[string][]string) map[string][]string {
	out := map[string][]string{}
	for d, es := range ents {
		for _, e := range es {
			n := strings.TrimSuffix(e, "/")
			for {
				if len(n) >= 8 && digitRE.MatchString(n) {
					out[n] = append(out[n], d+"/"+e)
				}
				i := strings.LastIndex(n, ".")
				if i <= 0 {
					break
				}
				n = n[:i]
			}
		}
	}
	return out
}

// cited is the set of entries, as dir/entry, that text, the body of the tracked file page, cites.
func cited(page, text string, ents map[string][]string, bare map[string][]string) map[string]bool {
	out := map[string]bool{}
	top, _, _ := strings.Cut(page, "/")
	for _, ix := range resultPathRE.FindAllStringIndex(text, -1) {
		tok := text[ix[0]:ix[1]]
		var prev byte
		if ix[0] > 0 {
			prev = text[ix[0]-1]
		}
		if isWordByte(prev) {
			continue
		}
		if strings.HasPrefix(tok, "results/") {
			if prev == '/' || prev == '.' || !slices.Contains(resultsDirs, top+"/results") {
				continue
			}
			tok = top + "/" + tok
		}
		parts := strings.SplitN(tok, "/", 4)
		d := parts[0] + "/" + parts[1]
		if len(parts) < 3 {
			continue
		}
		match(d, strings.TrimRight(parts[2], ".,"), ents[d], out)
	}
	if d := top + "/results"; slices.Contains(resultsDirs, d) {
		for _, ix := range patternRE.FindAllStringIndex(text, -1) {
			tok := strings.TrimRight(text[ix[0]:ix[1]], ".,")
			if ix[0] > 0 && text[ix[0]-1] == '/' || !strings.ContainsAny(tok, "*?{") {
				continue
			}
			match(d, tok, ents[d], out)
		}
	}
	for _, ix := range nameTokenRE.FindAllStringIndex(text, -1) {
		dirOnly := ix[1] < len(text) && text[ix[1]] == '/'
		for _, k := range bare[strings.TrimRight(text[ix[0]:ix[1]], ".")] {
			if !dirOnly || strings.HasSuffix(k, "/") {
				out[k] = true
			}
		}
	}
	return out
}

// match marks each entry of directory d that name, one path segment, names: an entry's name, a glob or a {a,b} with
// at least 4 characters before its first wildcard, or a name prefix of 8 characters or more ending in - or _.
func match(d, name string, ents []string, out map[string]bool) {
	for _, g := range braces(name) {
		if g == "" {
			continue
		}
		if (strings.HasSuffix(g, "-") || strings.HasSuffix(g, "_")) && len(g) >= 8 {
			g += "*"
		}
		i := strings.IndexAny(g, "*?[")
		if i >= 0 && i < 4 {
			continue
		}
		for _, e := range ents {
			n := strings.TrimSuffix(e, "/")
			if ok, _ := path.Match(g, n); ok && i >= 0 || n == g {
				out[d+"/"+e] = true
			}
		}
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

// citesTable is the cites table rows dir's README wants.
func citesTable(dir string, ents []string, by map[string][]string) []string {
	rows := []string{"| Entry | Pages | Other files |", "| --- | --- | ---: |"}
	for _, e := range ents {
		var pages []string
		other := 0
		for _, f := range by[dir+"/"+e] {
			if strings.HasSuffix(f, ".md") {
				pages = append(pages, "`"+f+"`")
			} else {
				other++
			}
		}
		p := "none"
		if len(pages) > 0 {
			p = strings.Join(pages, ", ")
		}
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %d |", e, p, other))
	}
	return rows
}

// section is the table under heading head in the README's lines, and the number of the heading's line, 0 if none.
func section(lines []string, head string) (rows []string, at int) {
	for i, l := range lines {
		if l != head {
			continue
		}
		at = i + 1
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(lines[j], "|") && !strings.HasPrefix(lines[j], "## ") {
			j++
		}
		for ; j < len(lines) && strings.HasPrefix(lines[j], "|"); j++ {
			rows = append(rows, lines[j])
		}
		return rows, at
	}
	return nil, 0
}

// diffRows names the first row where got and want part, and prints want whole.
func diffRows(t *testing.T, readme, what string, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	g, w := "(none)", "(none)"
	if i < len(got) {
		g = got[i]
	}
	if i < len(want) {
		w = want[i]
	}
	t.Errorf("%s: the %s table parts from what the tree says at its row %d:\n got: %s\nwant: %s\nthe table it wants:\n%s",
		readme, what, i+1, g, w, strings.Join(want, "\n"))
}

// Each results README's two tables say what the public set says.
func TestResultsReadmes(t *testing.T) {
	s := readSite(t).pub
	ents := entries(s.tracked)
	bare := bareNames(ents)
	by := map[string][]string{}
	var files []string
	for f := range s.tracked {
		if !inResults(f) {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		for k := range cited(f, string(b), ents, bare) {
			by[k] = append(by[k], f)
		}
	}
	type readme struct {
		dir     string
		lines   []string
		outside []string
		at      int
	}
	var readmes []readme
	names := map[string]bool{}
	for _, d := range resultsDirs {
		p := d + "/README.md"
		if !s.tracked[p] {
			t.Errorf("%s is not in git ls-files", p)
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		r := readme{dir: d, lines: strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")}
		got, at := section(r.lines, citesHead)
		if at == 0 {
			t.Errorf("%s: no %q heading", p, citesHead)
		}
		diffRows(t, p, "cites", got, citesTable(d, ents[d], by))
		r.outside, r.at = section(r.lines, outsideHead)
		if r.at == 0 {
			t.Errorf("%s: no %q heading", p, outsideHead)
		}
		for i, row := range r.outside {
			c := cells(row)
			if i < 2 {
				continue
			}
			if len(c) != 3 {
				t.Errorf("%s: %q: an outside row has 3 cells", p, row)
				continue
			}
			for _, n := range strings.Split(c[2], ", ") {
				names[n] = true
			}
		}
		readmes = append(readmes, r)
	}
	for n := range names {
		switch {
		case gateIDRE.MatchString(n):
			if s.dirs["gate/results/"+n] {
				t.Errorf("%s is a gate run kept under gate/results, not one kept outside the tree", n)
			}
		case inResults(n):
			if s.known(n) {
				t.Errorf("%s is in the public set, not kept outside the tree", n)
			}
		default:
			t.Errorf("%q is neither a gate id nor a path under a results directory", n)
		}
	}
	sorted := slices.Sorted(maps.Keys(names))
	for _, r := range readmes {
		want := []string{"| Result file | Count | Files outside the tree |", "| --- | ---: | --- |"}
		var rfiles []string
		for f := range s.tracked {
			if strings.HasPrefix(f, r.dir+"/") && f != r.dir+"/README.md" {
				rfiles = append(rfiles, f)
			}
		}
		slices.Sort(rfiles)
		for _, f := range rfiles {
			b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(f)))
			if err != nil {
				t.Fatal(err)
			}
			var hit []string
			for _, n := range sorted {
				if bytes.Contains(b, []byte(n)) {
					hit = append(hit, n)
				}
			}
			if len(hit) > 0 {
				want = append(want, fmt.Sprintf("| `%s` | %d | %s |", strings.TrimPrefix(f, r.dir+"/"), len(hit),
					strings.Join(hit, ", ")))
			}
		}
		diffRows(t, r.dir+"/README.md", "outside", r.outside, want)
	}
}

// cited reads paths, globs, braces, name prefixes and bare names, and leaves broad globs and other words alone; and
// unbacked names what a page cites that is neither here nor a row, an archived id with no row among them. The entries
// and rows are made up, so this file cites none of the tree's.
func TestCited(t *testing.T) {
	ents := map[string][]string{
		"cells/results":  {"2099-01-01-pilot.txt", "2099-01-01-pilot/", "2099-01-02-boot.txt"},
		"faults/results": {"zzload/", "raun-zz9-oom-1.json", "raun-zz9-oom-2.json", "raun-zzq1.json"},
		"gate/results":   {"g20990101-000001/", "g20990101-000002/"},
		"load/results":   {"boot-z.json", "v9999-1.json"},
	}
	bare := bareNames(ents)
	for _, c := range []struct {
		page, text string
		want       []string
	}{
		{"docs/a.md", "`cells/results/2099-01-01-pilot.txt`:530", []string{"cells/results/2099-01-01-pilot.txt"}},
		{"docs/a.md", "cells/results/2099-01-01-pilot/extra/x.json", []string{"cells/results/2099-01-01-pilot/"}},
		{"docs/a.md", "run 2099-01-01-pilot.", []string{"cells/results/2099-01-01-pilot.txt",
			"cells/results/2099-01-01-pilot/"}},
		{"docs/a.md", "`faults/results/raun-zz9-oom-*.json`", []string{"faults/results/raun-zz9-oom-1.json",
			"faults/results/raun-zz9-oom-2.json"}},
		{"docs/a.md", `"faults/results/raun-zz9-oom-" + label`, []string{"faults/results/raun-zz9-oom-1.json",
			"faults/results/raun-zz9-oom-2.json"}},
		{"docs/a.md", "faults/results/raun-zz9-oom-{2,9}.json", []string{"faults/results/raun-zz9-oom-2.json"}},
		{"docs/a.md", "faults/results/*.json, gate/results/g*/verdict.json and \"faults/results/raun-\"", nil},
		{"docs/a.md", "faults/results/zzload/candidate-x.json", []string{"faults/results/zzload/"}},
		{"docs/a.md", "the gate g20990101-000001, and g20990101-000002.html", []string{"gate/results/g20990101-000001/"}},
		{"load/README.md", "[run](results/boot-z.json)", []string{"load/results/boot-z.json"}},
		{"docs/a.md", "results/boot-z.json and boot-z.json alone", nil},
		{"docs/a.md", "v9999-1.json alone", []string{"load/results/v9999-1.json"}},
		{"docs/a.md", "testcells/results/boot-z.json", nil},
		{"faults/README.md", "`raun-zzq*` and raun-zz9-oom-{1,3}.json", []string{"faults/results/raun-zz9-oom-1.json",
			"faults/results/raun-zzq1.json"}},
		{"docs/a.md", "`raun-zzq*`, alone on a page under docs", nil},
	} {
		got := slices.Sorted(maps.Keys(cited(c.page, c.text, ents, bare)))
		if !slices.Equal(got, c.want) {
			t.Errorf("cited(%s, %q) = %q, want %q", c.page, c.text, got, c.want)
		}
	}

	// What a page names must be here or have a row (TestCitesHaveRows): a kept record, or an archived one by its
	// records row, each line cited by line by its quotes row, a gate id by its gates row, a row cite by its key.
	b := backing{
		tracked: map[string]bool{"cells/results/2099-01-02-boot.txt": true, "gate/results/g20990101-000001/gate.json": true},
		dirs:    map[string]bool{"cells/results": true, "gate/results": true, "gate/results/g20990101-000001": true},
		records: []string{"cells/results/2099-01-01-pilot.txt", "cells/results/2099-01-01-pilot/",
			"load/results/v9999-1.*", "gate/results/g20990101-000002/"},
		rows: map[string]map[string]bool{
			"gates":   {"g20990101-000001": true, "g20990101-000002": true},
			"quotes":  {"cells/results/2099-01-01-pilot.txt:530": true, "cells/results/2099-01-01-pilot.txt:531": true},
			"compare": {"g20990101-000002/persistence_latency_p99_ms": true},
			"rauns":   {"raun-zz9-oom-1": true, "raun-zz9-oom-2": true},
		},
		ignored: func(p string) bool { return strings.HasPrefix(p, "faults/results/raun-rb") },
	}
	for _, c := range []struct {
		page, text string
		want       []string
	}{
		{"docs/a.md", "`cells/results/2099-01-02-boot.txt`:7, kept and here", nil},
		{"docs/a.md", "`cells/results/2099-01-01-pilot.txt`:530-531, archived, both lines quoted", nil},
		{"docs/a.md", "`cells/results/2099-01-01-pilot.txt`:530, :532", []string{
			"cells/results/2099-01-01-pilot.txt:532: archived, and no quotes row"}},
		{"docs/a.md", "cells/results/2099-01-01-pilot/extra/x.json and load/results/v9999-1.verdict.json", nil},
		{"docs/a.md", "the gates g20990101-000001 and g20990101-000002", nil},
		{"docs/a.md", "the gate g20990101-000003, archived with no row", []string{
			"g20990101-000003: no run in gate/results and no gates row"}},
		{"docs/a.md", "`faults/results/raun-zz9-oom-1.json`, archived with no row", []string{
			"faults/results/raun-zz9-oom-1.json: no kept record and no records row"}},
		{"docs/a.md", "`docs/tables/compare.jsonl#g20990101-000002/persistence_latency_p99_ms` and " +
			"docs/tables/compare.jsonl#g20990101-000002/", nil},
		{"docs/a.md", "docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:530.", nil},
		{"docs/a.md", "`docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:530-531`, a run of lines", nil},
		{"docs/a.md", "docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:530-532", []string{
			"docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:530-532: no such row"}},
		{"docs/a.md", "`docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:530`, :531, and more", nil},
		{"docs/a.md", "`docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:530`, :532", []string{
			"docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:532: no such row"}},
		{"docs/a.md", "docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:540", []string{
			"docs/tables/quotes.jsonl#cells/results/2099-01-01-pilot.txt:540: no such row"}},
		{"docs/a.md", "docs/tables/gates.jsonl#g20990101-000009", []string{
			"docs/tables/gates.jsonl#g20990101-000009: no such row"}},
		{"docs/img/src/x.txt", "row quotes cells/results/2099-01-01-pilot.txt:531\nrow gates g20990101-000004", []string{
			"row gates g20990101-000004: no such row"}},
		{"docs/a.md", "docs/tables/rauns.jsonl#raun-zz9-<n>, `cells/results/<file>`, gate/results/g.../verdict.md", nil},
		{"docs/a.md", "the rauns raun-zz9-oom-1, raun-zz9-oom-{1,2} and raun-zz9-oom-[12], each with its row", nil},
		{"docs/a.md", "raun-zz9-<n>, raun-zz9-..., RUN_ID=raun-rb6 and `raun-rb`: placeholders and a walk's", nil},
		{"docs/a.md", "raun-zz9-oom-{1,2,3} and raun-zz8-* archived, raun-zz8-kill-1 with no row", []string{
			"raun-zz9-oom-{1,2,3}: no raun row", "raun-zz8-*: no raun row", "raun-zz8-kill-1: no raun row"}},
	} {
		if got := b.unbacked(c.page, c.text); !slices.Equal(got, c.want) {
			t.Errorf("unbacked(%s, %q) = %q, want %q", c.page, c.text, got, c.want)
		}
	}
}

// TestWalkFilesIgnored: a runbook walk names its runs rb<N>-<UTC time> (docs/runbooks/2-lease-and-load.md), and they
// land in tracked directories: a load's files in load/results, a raun's in faults/results and its load's copy in
// faults/results/load. .gitignore keeps each out of git status, so a walk leaves the tree clean. A committed run keeps
// its name and stays tracked: no tracked file has a walk's name, and a run named another way is not ignored.
func TestWalkFilesIgnored(t *testing.T) {
	walk := []string{"load/results/rb[0-9]-*", "faults/results/raun-rb[0-9]-*", "faults/results/load/raun-rb[0-9]-*"}
	for _, c := range []struct {
		path    string
		ignored bool
	}{
		{"load/results/rb2-20261027-120000.json", true},
		{"load/results/rb2-20261027-120000.verdict.json", true},
		{"load/results/rb2-20261027-120000.baseline.json", true},
		{"faults/results/raun-rb3-20261027-120000.json", true},
		{"faults/results/load/raun-rb3-20261027-120000.json", true},
		{"faults/results/raun-rb6-20261027-120000.json", true},
		{"load/results/20261027-120000.json", false},
		{"load/results/lab0-1.json", false},
		{"faults/results/raun-20261027-120000.json", false},
		{"faults/results/raun-lab0-persistence-latency-1.json", false},
		{"faults/results/load/raun-20261027-120000.json", false},
	} {
		if _, err := os.Stat(filepath.Join("..", filepath.FromSlash(c.path))); err == nil {
			t.Fatalf("%s is on disk; the test names paths that are not", c.path)
		}
		err := git("check-ignore", "-q", "--no-index", "--", c.path).Run()
		if got := err == nil; got != c.ignored {
			t.Errorf("git check-ignore %s: ignored %v, want %v (%v)", c.path, got, c.ignored, err)
		}
	}
	out, err := git("ls-files", "-z", "--", "load/results", "faults/results").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		for _, w := range walk {
			if ok, _ := path.Match(w, p); ok {
				t.Errorf("%s is tracked, and .gitignore's %s names it a walk's file", p, w)
			}
		}
	}
}

// TestPinnedFigures: two figures in script comments that named no file now name one, and hold to it.
// load/lost-work.sh's failed iterations are the lab rauns' iterations_failed, and gate/run.sh's dev host clock ratio is
// boot 1's record of it (cells/results/2026-10-23-boot1.txt:69).
func TestPinnedFigures(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	m := regexp.MustCompile(`rauns failed (\d+), (\d+) and (\d+) iterations during the fault and 0 in the after\n# ` +
		`window, by lw_buckets below \(iterations_failed in faults/results/raun-lab2-persistence-latency-\{1,2,3\}` +
		`\.json\)`).FindStringSubmatch(read("load/lost-work.sh"))
	if m == nil {
		t.Fatal("load/lost-work.sh: no failed iterations that name faults/results/raun-lab2-persistence-latency-*")
	}
	for i := 1; i <= 3; i++ {
		var r struct {
			Failed struct {
				During, After *int
			} `json:"iterations_failed"`
		}
		p := fmt.Sprintf("faults/results/raun-lab2-persistence-latency-%d.json", i)
		if err := json.Unmarshal([]byte(read(p)), &r); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if r.Failed.During == nil || fmt.Sprint(*r.Failed.During) != m[i] || r.Failed.After == nil || *r.Failed.After != 0 {
			t.Errorf("%s: iterations_failed %+v; load/lost-work.sh says %s during and 0 after", p, r.Failed, m[i])
		}
	}
	const clock = "dev host's wall clock ran 1.045 to 1.048 times it, by laug host clock, " +
		"cells/results/2026-10-23-boot1.txt:69)"
	if !strings.Contains(read("gate/run.sh"), clock) {
		t.Errorf("gate/run.sh: no %q", clock)
	}
	if l := strings.Split(read("cells/results/2026-10-23-boot1.txt"), "\n"); len(l) < 69 ||
		!strings.Contains(l[68], "wall over monotonic 1.048, then 1.045 (dev host") {
		t.Error("cells/results/2026-10-23-boot1.txt:69 does not record the dev host's 1.048, then 1.045")
	}
}
