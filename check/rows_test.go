package check

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The rows a page may name in place of a record. Once a record leaves the tree for the archive, what a page says of
// it rests on the summary tables under docs/tables: its records row, the gates row of a gate id, and the quotes row of
// each line a page cites by line. A row is cited by its key, docs/tables/<table>.jsonl#<key>.

// backing is what a page's names resolve against: the tracked files and directories, and the rows of docs/tables.
type backing struct {
	tracked, dirs map[string]bool
	records       []string                   // the records table's keys: a path, a directory with its slash, a glob
	rows          map[string]map[string]bool // each table's keys, by the table's name
	ignored       func(p string) bool        // git check-ignore's answer: a runbook walk's runs stay on a host
}

var (
	// tableCiteRE is a row's cite: the table's name and the key, up to a space, a quote, a backtick or a bracket.
	tableCiteRE = regexp.MustCompile("docs/tables/([a-z0-9-]+)\\.jsonl#([^\\s`'\"()\\[\\]<>|]+)")
	// rowLineRE is a figure spec's row line, `row <table> <key>`.
	rowLineRE = regexp.MustCompile(`^row ([a-z0-9-]+) (\S+)$`)
	// byLineRE is a path's line or range of lines after it: `:12`, `:12-14`, and more ranges `, :20`.
	byLineRE   = regexp.MustCompile("^`?(?:, )?:([0-9]+)(?:-([0-9]+))?")
	moreLineRE = regexp.MustCompile("^`?,? (?:and )?:([0-9]+)(?:-([0-9]+))?")
	// quoteRunRE is a quotes cite of a run of lines, <path>:<from>-<to>: it names the quotes row of each line.
	quoteRunRE = regexp.MustCompile(`^(.+):([0-9]+)-([0-9]+)$`)
	gateWordRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(g[0-9]{8}-[0-9]{6})(?:[^0-9-]|-[^0-9]|$)`)
	// raunWordRE is a bare raun id, a set of them in braces or a glob of them, not a path's last part: what follows
	// it is checked apart, as a prefix (raun-rb3-<UTC time>) is no id.
	raunWordRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.-])(raun-[A-Za-z0-9{},*\[\]-]*[A-Za-z0-9}*\]])`)
)

// readBacking reads the tracked files and docs/tables from the checkout at root.
func readBacking(t *testing.T, s *site, root string) backing {
	t.Helper()
	b := backing{tracked: s.tracked, dirs: s.dirs, rows: map[string]map[string]bool{}, ignored: s.isIgnored}
	ents, err := os.ReadDir(filepath.Join(root, "docs", "tables"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok {
			continue
		}
		if i := strings.LastIndex(name, "-"); i > 0 && strings.Trim(name[i+1:], "0123456789") == "" {
			name = name[:i] // a rolled file, <name>-2.jsonl
		}
		if b.rows[name] == nil {
			b.rows[name] = map[string]bool{}
		}
		f, err := os.Open(filepath.Join(root, "docs", "tables", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var r struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.ID == "" {
				t.Fatalf("docs/tables/%s: a line with no id: %v", e.Name(), err)
			}
			b.rows[name][r.ID] = true
			if name == "records" {
				b.records = append(b.records, r.ID)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

// pathBacked says whether p, a path from the root that may hold a glob or a {a,b}, names a tracked file or directory
// or a record of the records table: the record itself, a path under its directory, a match of its glob, or a
// directory that holds records.
func (b backing) pathBacked(p string) bool {
	p = strings.TrimSuffix(p, "/")
	for _, g := range braces(p) {
		wild := strings.ContainsAny(g, "*?[")
		if !wild && (b.tracked[g] || b.dirs[g]) {
			return true
		}
		for _, r := range b.records {
			rr := strings.TrimSuffix(r, "/")
			switch {
			case g == rr, strings.HasSuffix(r, "/") && strings.HasPrefix(g, r), !wild && strings.HasPrefix(r, g+"/"):
				return true // the record, a path under its directory, or a directory that holds records
			}
			if ok, _ := path.Match(r, g); ok && !wild {
				return true
			}
			if wild {
				// A glob names a record when it matches the record's path, or one of the names a load's glob stands for.
				for _, k := range []string{rr, strings.TrimSuffix(rr, "*") + "json"} {
					if ok, _ := path.Match(g, k); ok {
						return true
					}
				}
			}
		}
		if wild {
			for f := range b.tracked {
				if ok, _ := path.Match(g, f); ok {
					return true
				}
			}
		}
	}
	return false
}

// unbacked is what page names that has no kept record here and no row: a result path that is not tracked and is no
// record of the records table; a line it cites by line of a record that is not tracked, with no quotes row; a gate id
// that is not under gate/results and has no gates row; a bare raun id, set or glob with no rauns row; a row cite,
// docs/tables/<table>.jsonl#<key> or a spec's `row <table> <key>`, whose table has no such key (a key ending in / names
// the keys under it, and a quotes key <path>:<a>-<b> the row of each line a to b). A path, id or key that holds
// a placeholder, <...> or ..., is left alone, as is a path that is no more than a prefix ending in - or _ and a path or
// raun id .gitignore keeps out of git, a runbook walk's.
func (b backing) unbacked(page, text string) []string {
	var out []string
	top, _, _ := strings.Cut(page, "/")
	keyOK := func(table, key string) bool {
		key = strings.TrimRight(key, ".,;:")
		if strings.HasSuffix(key, "/") {
			for k := range b.rows[table] {
				if strings.HasPrefix(k, key) {
					return true
				}
			}
			return false
		}
		if m := quoteRunRE.FindStringSubmatch(key); m != nil && table == "quotes" {
			from, _ := strconv.Atoi(m[2])
			to, _ := strconv.Atoi(m[3])
			for n := from; n <= to && to-from < 1000; n++ {
				if !b.rows[table][m[1]+":"+strconv.Itoa(n)] {
					return false
				}
			}
			return from <= to && to-from < 1000
		}
		return b.rows[table][key]
	}
	for _, line := range strings.Split(text, "\n") {
		if m := rowLineRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil && strings.HasPrefix(page,
			"docs/img/src/") {
			if !strings.Contains(m[2], "<") && !keyOK(m[1], m[2]) {
				out = append(out, "row "+m[1]+" "+m[2]+": no such row")
			}
			continue // a row line cites its row, never a record's line
		}
		cites := tableCiteRE.FindAllStringSubmatchIndex(line, -1)
		inCite := func(i int) bool {
			for _, c := range cites {
				if i >= c[0] && i < c[1] {
					return true
				}
			}
			return false
		}
		for _, c := range cites {
			table, key := line[c[2]:c[3]], line[c[4]:c[5]]
			if c[1] < len(line) && line[c[1]] == '<' {
				continue // a placeholder: docs/tables/rauns.jsonl#raun-lab6-<row>-<n>
			}
			if !keyOK(table, key) {
				out = append(out, "docs/tables/"+table+".jsonl#"+strings.TrimRight(key, ".,;:")+": no such row")
			}
			if table != "quotes" {
				continue
			}
			// More lines of the same record after a quotes cite, `, :20` or `, :20-21`, name their rows too.
			file := strings.TrimRight(key, ".,;:")
			if i := strings.LastIndex(file, ":"); i > 0 {
				file = file[:i]
			}
			rest := line[c[1]:]
			for m := moreLineRE.FindStringSubmatchIndex(rest); m != nil; m = moreLineRE.FindStringSubmatchIndex(rest) {
				run := file + ":" + rest[m[2]:m[3]]
				if m[4] >= 0 {
					run += "-" + rest[m[4]:m[5]]
				}
				if !keyOK(table, run) {
					out = append(out, "docs/tables/quotes.jsonl#"+run+": no such row")
				}
				rest = rest[m[1]:]
			}
		}
		for _, ix := range resultPathRE.FindAllStringIndex(line, -1) {
			if inCite(ix[0]) {
				continue // the key of a row cite, which the row holds
			}
			tok := strings.TrimRight(line[ix[0]:ix[1]], ".,")
			if ix[0] > 0 && (isWordByte(line[ix[0]-1]) || line[ix[0]-1] == '<') {
				continue
			}
			if strings.HasPrefix(tok, "results/") {
				if ix[0] > 0 && (line[ix[0]-1] == '/' || line[ix[0]-1] == '.') ||
					!slices.Contains(resultsDirs, top+"/results") {
					continue
				}
				tok = top + "/" + tok
			}
			name := strings.TrimSuffix(tok, "/")
			if i := strings.Index(name, "results/"); i < 0 || len(name) <= i+len("results/") {
				continue
			}
			if ix[1] < len(line) && line[ix[1]] == '<' || strings.HasSuffix(name, "-") ||
				strings.HasSuffix(name, "_") || strings.Contains(name, "...") {
				continue // a placeholder, or a prefix code joins a name to
			}
			if !b.pathBacked(name) && b.ignored != nil && b.ignored(name) {
				continue // a runbook walk's run, which .gitignore keeps on the host
			}
			if !b.pathBacked(name) {
				out = append(out, name+": no kept record and no records row")
				continue
			}
			if b.tracked[name] || strings.ContainsAny(name, "*?[{") {
				continue
			}
			rest := line[ix[1]:]
			m := byLineRE.FindStringSubmatchIndex(rest)
			for m != nil {
				from, _ := strconv.Atoi(rest[m[2]:m[3]])
				to := from
				if m[4] >= 0 {
					to, _ = strconv.Atoi(rest[m[4]:m[5]])
				}
				for n := from; n <= to && n-from < 1000; n++ {
					if key := name + ":" + strconv.Itoa(n); !b.rows["quotes"][key] {
						out = append(out, key+": archived, and no quotes row")
					}
				}
				rest = rest[m[1]:]
				m = moreLineRE.FindStringSubmatchIndex(rest)
			}
		}
		for _, m := range gateWordRE.FindAllStringSubmatchIndex(line, -1) {
			id := line[m[2]:m[3]]
			if inCite(m[2]) || b.dirs["gate/results/"+id] || b.rows["gates"][id] {
				continue
			}
			out = append(out, id+": no run in gate/results and no gates row")
		}
		for _, m := range raunWordRE.FindAllStringSubmatchIndex(line, -1) {
			if rest := line[m[3]:]; inCite(m[2]) || strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, "<") ||
				strings.HasPrefix(rest, "..") {
				continue // a row cite's key, or a prefix a name is made from
			}
			id := line[m[2]:m[3]]
			if b.ignored != nil &&
				(b.ignored("faults/results/"+id+"-0.json") || b.ignored("faults/results/"+id+"0-0.json")) {
				continue // a runbook walk's run id or its prefix: .gitignore keeps the walk's rauns on the host
			}
			if !b.raunBacked(id) {
				out = append(out, id+": no raun row")
			}
		}
	}
	return out
}

// raunBacked says whether id, a bare raun id, a {a,b} set or a glob, names rauns rows: each id of a set a row, a
// glob at least one. A raun's JSON is under faults/results whether it is here or archived, and its rauns row stays.
// Load ids have no shape of their own a page's prose can be told by, so they are not checked here.
func (b backing) raunBacked(id string) bool {
	for _, g := range braces(id) {
		if !strings.ContainsAny(g, "*?[") {
			if !b.rows["rauns"][g] {
				return false
			}
			continue
		}
		found := false
		for k := range b.rows["rauns"] {
			if ok, _ := path.Match(g, k); ok {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// rowPages are the pages whose names must resolve: every .md of s outside the results directories, docs/tables,
// testdata directories and names that start with a dot, less the generated pages (docs/EVIDENCE.md,
// docs/img/README.md); each figure's spec; each gate set; and
// docs/tables/src/hosts.tsv. The pages report -tables reads for by-line cites (_report's tablePages), so
// every quotes row a page needs is one -tables builds. TestCitesHaveRows gives it the public set, so a private page
// is not read.
func rowPages(s *site) []string {
	skip := map[string]bool{"docs/EVIDENCE.md": true, "docs/img/README.md": true}
	var out []string
	for f := range s.tracked {
		dir := path.Dir(f)
		hidden := false
		for _, seg := range strings.Split(f, "/") {
			if strings.HasPrefix(seg, ".") || seg == "testdata" {
				hidden = true
			}
		}
		switch {
		case hidden, skip[f], inResults(f), strings.HasPrefix(f, "docs/tables/") && f != "docs/tables/src/hosts.tsv",
			strings.HasPrefix(f, "bin/"), strings.HasPrefix(f, "report/"):
		case strings.HasSuffix(f, ".md"), dir == "docs/img/src" && strings.HasSuffix(f, ".txt"),
			dir == "gate/sets" && strings.HasSuffix(f, ".txt"), f == "docs/tables/src/hosts.tsv":
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

// TestCitesHaveRows: every record, gate id or bare raun id a public page names is a kept record or has a row, every
// line it cites of a record that is not here has a quotes row, and every row it cites by key is there. A bare load id
// is not checked: it has no shape prose can be told by.
func TestCitesHaveRows(t *testing.T) {
	s := readSite(t).pub
	b := readBacking(t, s, "..")
	pages := rowPages(s)
	for _, p := range pages {
		data, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range b.unbacked(p, string(data)) {
			t.Errorf("%s: %s", p, u)
		}
	}
	t.Logf("%d pages", len(pages))
}
