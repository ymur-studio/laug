package check

import (
	"bytes"
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

// The paths the records cite. A kept record names the tree as it stood when laug wrote it: a load's yaml, a script, a
// page, a command's directory. TestCitedPaths reads every record of the public set under the four results directories
// and fails on each path they cite that the tree no longer holds, so a move or a delete that breaks a citation is seen
// in make test-go before it lands. The four results READMEs are read as records too; their cites tables name each page
// at the path the tree has now (TestResultsReadmes). A path cited under one of the results directories themselves is
// TestResultsReadmes' (the outside tables), not this test's.
//
// A cite is a token of path segments with a / between them whose first segment is a top-level directory of the tree,
// one of citeTops (the top directories the tree had when the records were written, so a directory that leaves the
// tree whole still has its cites read) or one of docs_test.go's pathTops (laug's top directories as the records name
// them, with health, orchestrator and pool, the three that moved under internal/: a name there keeps its cites read
// once the directory moves), not inside a longer word and not after a / (an absolute path), a : (a host), a $ (a
// variable), a . (a parent), a - (a hyphenated word), an @ or a ~. The token, less a leading ./ and trailing
// punctuation, passes when the tree holds it as a file or a directory; when it is a name prefix (ending in - or _) or
// a wildcard follows it, and a tracked path starts with it; when git ignores it (cells/run/, gate/.lock: a run's
// state); or when it names a file next to the record. A token with a tree extension and another first segment (a
// server or SDK source file in a log line, a path inside a cell, a file of omes) is not a tree path and is left alone.
//
// docs/RECORDS.md's last section, allowHead, holds the allow table: one row a cited path the tree no longer has, with
// why. The test fails on a cited path that is neither in the tree nor in the table, and on a row whose path the tree
// holds or no record cites (the row is stale). TestKept's parse of the page stops at that heading.
//
// moves is the move table: a path the records cite that moved, where it went and the date. A cited old path passes
// when the tree holds its new path. The test fails on a row whose new path git ls-files lacks, whose old path the tree
// holds again, or whose old path no record cites. TestPages reads the table for the kept results READMEs. A
// results README's cites table names each page where the tree has it now (TestResultsReadmes).

const allowHead = "## Cited paths the tree no longer has"

// A move is one row of the move table: the path a record cites, the path it moved to, and the date of the move.
type move struct {
	old, new, date string
}

var moves = []move{
	{"obs/versions.txt", "_obs/versions.txt", "2026-11-04"},
}

// movedTo is where the move table says p went, and whether it has a row.
func movedTo(p string) (string, bool) {
	for _, m := range moves {
		if m.old == p {
			return m.new, true
		}
	}
	return "", false
}

var pathTokRE = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+`)

// A cite is one path token of a record: the path, whether a wildcard follows it, and the record's line.
type cite struct {
	path string
	glob bool
	line int
}

// pathCites is each tree path token of text, a record's body, whose first segment is in tops, with its line.
func pathCites(text string, tops map[string]bool) []cite {
	var out []cite
	line, last := 1, 0
	for _, ix := range pathTokRE.FindAllStringIndex(text, -1) {
		line += strings.Count(text[last:ix[0]], "\n")
		last = ix[0]
		if ix[0] > 0 {
			if p := text[ix[0]-1]; isWordByte(p) || strings.IndexByte("/:$.-@~", p) >= 0 {
				continue
			}
		}
		tok := strings.TrimPrefix(text[ix[0]:ix[1]], "./")
		tok = strings.TrimRight(tok, ".,")
		top, _, ok := strings.Cut(tok, "/")
		if !ok || !tops[top] || inResults(tok) {
			continue
		}
		glob := ix[1] < len(text) && strings.IndexByte("*?[{", text[ix[1]]) >= 0
		out = append(out, cite{tok, glob, line})
	}
	return out
}

// citeTops are the top directories the tree had when the records were written, so a directory that leaves the tree
// whole still has its cites read.
var citeTops = []string{"cells", "check", "cmd", "dashboards", "docs", "faults", "gate", "internal", "load", "obs"}

// tops is the set of first segments pathCites reads: citeTops, every top-level directory the site tracks a file in,
// and pathTops.
func (s *site) tops() map[string]bool {
	out := map[string]bool{}
	for d := range s.dirs {
		if !strings.Contains(d, "/") {
			out[d] = true
		}
	}
	for _, d := range slices.Concat(citeTops, pathTops) {
		out[d] = true
	}
	return out
}

// holdsCite says whether the site holds c, cited by record: a tracked file or directory, a prefix of one when c is a
// name prefix or a wildcard follows it, a path git ignores, or a file next to the record.
func (s *site) holdsCite(record string, c cite) bool {
	p := c.path
	if s.known(p) {
		return true
	}
	if c.glob || strings.HasSuffix(p, "-") || strings.HasSuffix(p, "_") {
		for f := range s.tracked {
			if strings.HasPrefix(f, p) {
				return true
			}
		}
	}
	if s.isIgnored(p) || s.isIgnored(p+"/") {
		return true
	}
	for _, d := range []string{path.Dir(record), strings.TrimSuffix(record, path.Ext(record))} {
		if q := path.Join(d, p); q != p && s.known(q) {
			return true
		}
	}
	return false
}

// allowTable reads the table under allowHead in docs/RECORDS.md: each row's path, its code span dropped, and why. A
// problem is the heading missing, a later "## " heading, a row without two cells or a row without a why.
func allowTable(body string) (map[string]string, []string) {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	rows, at := section(lines, allowHead)
	if at == 0 {
		return nil, []string{fmt.Sprintf("%s: no %q heading", recordsPage, allowHead)}
	}
	var bad []string
	for i := at; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			bad = append(bad, fmt.Sprintf("%s:%d: a section after %q, which is the page's last", recordsPage, i+1, allowHead))
		}
	}
	out := map[string]string{}
	for i, row := range rows {
		if i < 2 {
			continue
		}
		c := cells(row)
		if len(c) != 2 {
			bad = append(bad, fmt.Sprintf("%s: %q: a row of the table under %q has 2 cells, path and why",
				recordsPage, row, allowHead))
			continue
		}
		p, why := strings.Trim(c[0], "`"), c[1]
		if p == "" || why == "" {
			bad = append(bad, fmt.Sprintf("%s: %q: a row of the table under %q names a path and says why",
				recordsPage, row, allowHead))
			continue
		}
		out[p] = why
	}
	return out, bad
}

// Every path a kept record cites is in the tree, in the move table at a path the tree holds, or in docs/RECORDS.md's
// allow table; every row of either table is a cited path the tree no longer has.
func TestCitedPaths(t *testing.T) {
	s := readSite(t).pub
	tops := s.tops()
	var records []string
	for f := range s.tracked {
		if inResults(f) {
			records = append(records, f)
		}
	}
	slices.Sort(records)
	type hit struct {
		record string
		line   int
	}
	gone, moved := map[string][]hit{}, map[string][]hit{}
	for _, f := range records {
		b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		seen := map[string]bool{}
		for _, c := range pathCites(string(b), tops) {
			if to, ok := movedTo(c.path); ok {
				moved[c.path] = append(moved[c.path], hit{f, c.line})
				if s.known(to) {
					continue
				}
			}
			if seen[c.path] || s.holdsCite(f, c) {
				continue
			}
			seen[c.path] = true
			gone[c.path] = append(gone[c.path], hit{f, c.line})
		}
	}
	for _, m := range moves {
		switch {
		case !s.known(m.new):
			t.Errorf("%s: the move table says it went to %s on %s, and git ls-files lacks %s", m.old, m.new, m.date, m.new)
		case s.known(m.old):
			t.Errorf("%s: the move table says it went to %s on %s, and the tree holds it: the row is stale",
				m.old, m.new, m.date)
		case len(moved[m.old]) == 0:
			t.Errorf("%s: the move table says it went to %s on %s, and no record cites it: the row is stale",
				m.old, m.new, m.date)
		}
	}
	body, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(recordsPage)))
	if err != nil {
		t.Fatal(err)
	}
	allow, bad := allowTable(string(body))
	for _, p := range bad {
		t.Error(p)
	}
	for _, p := range slices.Sorted(maps.Keys(gone)) {
		if _, ok := allow[p]; ok {
			continue
		}
		h := gone[p]
		t.Errorf("%s: %d records cite it, %s:%d first, and it is in neither git ls-files nor the table under %q in %s",
			p, len(h), h[0].record, h[0].line, allowHead, recordsPage)
	}
	for _, p := range slices.Sorted(maps.Keys(allow)) {
		switch {
		case s.known(p):
			t.Errorf("%s: the table under %q in %s allows it, and the tree holds it: the row is stale",
				p, allowHead, recordsPage)
		case len(gone[p]) == 0:
			t.Errorf("%s: the table under %q in %s allows it, and no record cites it: the row is stale",
				p, allowHead, recordsPage)
		}
	}
	t.Logf("%d records, %d cited paths gone and allowed, %d moved", len(records), len(allow), len(moves))
}

// pathCites reads tree paths and leaves other tokens alone; holdsCite and allowTable resolve them. The paths are made
// up, so this file cites none of the tree's.
func TestPathCitesPlants(t *testing.T) {
	tops := map[string]bool{"load": true, "gate": true, "docs": true, "cmd": true, "pool": true, "cells": true}
	for _, c := range []struct {
		text string
		want []cite
	}{
		{"the set load/zz-state.yaml, then gate/lib.sh:144.", []cite{{"load/zz-state.yaml", false, 1},
			{"gate/lib.sh", false, 1}}},
		{"see ./docs/zz.md and ../load/zz.yaml and /opt/x/load/zz.yaml", []cite{{"docs/zz.md", false, 1}}},
		{"host:load/zz.yaml $OBS/load/zz.yaml a-load/zz.yaml x.load/zz.yaml", nil},
		{"pool/README.md moved; cmd/gate too\ngate/results/g20990101-000001/verdict.json is a record's", []cite{
			{"pool/README.md", false, 1}, {"cmd/gate", false, 1}}},
		{"docs/tables/zz-<batch>.jsonl and load/releases/1.3{1,2}.yaml", []cite{{"docs/tables/zz-", false, 1},
			{"load/releases/1.3", true, 1}}},
		{"common/util.go:12 and harness/worker.go are not the tree's; cells/run/zz/laug.yaml is a run's", []cite{
			{"cells/run/zz/laug.yaml", false, 1}}},
	} {
		if got := pathCites(c.text, tops); !slices.Equal(got, c.want) {
			t.Errorf("pathCites(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	s := &site{tracked: map[string]bool{"load/zz-state.yaml": true, "docs/tables/zz-2099.jsonl": true,
		"cells/results/2099-01-01-zz/steps/A.txt": true}, dirs: map[string]bool{"load": true, "docs": true,
		"docs/tables": true, "cells": true, "cells/results": true, "cells/results/2099-01-01-zz": true,
		"cells/results/2099-01-01-zz/steps": true}, ignored: map[string]bool{"cells/run/zz/laug.yaml": true}}
	for _, c := range []struct {
		record string
		cite   cite
		want   bool
	}{
		{"cells/results/2099-01-01-zz.txt", cite{"load/zz-state.yaml", false, 1}, true},
		{"cells/results/2099-01-01-zz.txt", cite{"load/zz-gone.yaml", false, 1}, false},
		{"cells/results/2099-01-01-zz.txt", cite{"docs/tables", false, 1}, true},
		{"cells/results/2099-01-01-zz.txt", cite{"docs/tables/zz-", false, 1}, true},
		{"cells/results/2099-01-01-zz.txt", cite{"docs/tables/zz", true, 1}, true},
		{"cells/results/2099-01-01-zz.txt", cite{"docs/tables/yy-", false, 1}, false},
		{"cells/results/2099-01-01-zz.txt", cite{"cells/run/zz/laug.yaml", false, 1}, true},
		{"cells/results/2099-01-01-zz.txt", cite{"steps/A.txt", false, 1}, true},
		{"cells/results/2099-01-01-zz/steps/B.txt", cite{"steps/A.txt", false, 1}, false},
	} {
		if got := s.holdsCite(c.record, c.cite); got != c.want {
			t.Errorf("holdsCite(%s, %+v) = %v, want %v", c.record, c.cite, got, c.want)
		}
	}
	page := "# p\n\n## 1. A\n\ntext\n\n" + allowHead + "\n\nOne sentence.\n\n| path | why |\n| --- | --- |\n" +
		"| load/zz-gone.yaml | a why |\n| `cmd/zz` | another |\n"
	allow, bad := allowTable(page)
	if len(bad) != 0 || len(allow) != 2 || allow["load/zz-gone.yaml"] != "a why" || allow["cmd/zz"] != "another" {
		t.Errorf("allowTable: %v %q", bad, allow)
	}
	for _, c := range []struct {
		name, page, want string
	}{
		{"no heading", "# p\n\n## 1. A\n\ntext\n", "no \"" + allowHead + "\" heading"},
		{"a section after it", page + "\n## 2. B\n\ntext\n", "the page's last"},
		{"a row of one cell", page + "| load/zz-x.yaml |\n", "has 2 cells"},
		{"a row with no why", page + "| load/zz-x.yaml | |\n", "names a path and says why"},
	} {
		_, bad := allowTable(c.page)
		if got := strings.Join(bad, "\n"); !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want a problem with %q", c.name, got, c.want)
		}
	}
}
