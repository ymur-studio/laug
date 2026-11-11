package check

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The check of finished pages. check/docs.txt lists them, a page a line; the commit that finishes a page adds it to
// the list, and from then on make test-go holds it to these rules, each named by the kind of problem it reports:
//
//   make     in code, make's target is one of the Makefile's .PHONY targets; in prose, "make <x>" is one of them too
//            where x reads as a target name: a word with a hyphen, or the name of a laug command.
//   laug     in code, laug <command> [<verb>], or bin/laug, names a row of laug help, and for a command with verbs one
//            of the verbs laug <command> -h prints, from a laug this test builds from the checkout.
//   cluster  "cluster" only in Temporal's own terms (temporal operator cluster, cluster health, cluster metadata
//            and its methods, Multi-Cluster Replication), in a web address, or named as a word in quotes or a code
//            span. The word is cell, Temporal's own.
//   path     a relative link or image path, and a path in a code span under one of pathTops, cmd/laug or
//            internal/<one of laugPackages>, is in git ls-files: a file, a directory holding one, or a glob matching
//            one. A path git ignores, such as cells/run/, is a run's state and passes.
//   date     docs/LEARNING.md and docs/CAPABILITIES.md: no 20YY-MM- date once tracked paths and run ids are taken
//            out, so a dated result file and a run's day stand as evidence and nothing else does.
//   anchor   docs/LEARNING.md: the anchors stop-1 to stop-19, in order, once each.
//   stop     docs/LEARNING.md: each stop holds one **The idea**, one **Evidence** and one **Try it** line.
//
// A line of check/docs-private.txt may read "<page> like <listed page>": the private page keeps that page's own rules
// too, such as docs/CAPABILITIES.md's date.
//
// Code is a code span, or a line of a fenced block less its # comment. A run id is the name of an entry under
// gate/results, cells/results, load/results or faults/results, with or without its extension.

const (
	pageList     = "docs.txt"
	privatePages = "docs-private.txt"
	privateList  = "private.txt"
	records      = "docs/tables/records.jsonl"
	learning     = "docs/LEARNING.md"
	capabilities = "docs/CAPABILITIES.md"
	readme       = "README.md"
	stops        = 19
)

// site is what a page is checked against, read once from the checkout.
type site struct {
	targets  map[string]bool     // make's targets, the Makefile's .PHONY line
	commands map[string][]string // laug help's rows, each with the verbs its -h prints; none for a command of one verb
	tracked  map[string]bool     // git ls-files
	dirs     map[string]bool     // every directory that holds a tracked file
	runs     map[string]bool     // run ids: the entries under the four results directories
	mu       sync.Mutex
	ignored  map[string]bool   // git check-ignore's answer for an untracked path, kept
	private  []string          // check/private.txt's lines, when the file is here: a path, or a directory ending /
	like     map[string]string // a private page and the listed page whose own rules it keeps (check/docs-private.txt)
	pub      *site             // the public set's view: tracked, dirs and runs less what the public tree leaves out
}

// The public set. The public tree is this tree less two things: the paths check/private.txt names, and the result
// files of every record docs/tables/records.jsonl marks kept false (a file of a kept record stays, and so do the four
// results READMEs). check/private.txt is itself private, so in the public tree it is absent, nothing more is left
// out, and the public set is git ls-files. A public page is checked against the public set, so a page that names a
// private file, or a record that is not kept, fails here as it would in the public tree; a private page, one
// check/private.txt names, is checked against every tracked file. TestResultsReadmes and TestCitesHaveRows read the
// public set too, so each gives the same answer in both trees.

// isPrivate says whether p, a path from the checkout's root, is one check/private.txt names.
func (s *site) isPrivate(p string) bool {
	for _, l := range s.private {
		if p == l || strings.HasSuffix(l, "/") && strings.HasPrefix(p, l) {
			return true
		}
	}
	return false
}

// view is the site a page is checked against: the public set's for a public page, every tracked file's for a
// private one.
func (s *site) view(page string) *site {
	if s.pub == nil || s.isPrivate(page) {
		return s
	}
	return s.pub
}

// add puts f, a tracked path, in the site: its directories, and the run ids its path holds under a results directory.
func (s *site) add(f string) {
	s.tracked[f] = true
	for d := path.Dir(f); d != "."; d = path.Dir(d) {
		s.dirs[d] = true
	}
	for _, r := range []string{"gate/results/", "cells/results/", "load/results/", "faults/results/"} {
		rest, ok := strings.CutPrefix(f, r)
		if !ok {
			continue
		}
		for _, name := range strings.Split(rest, "/") {
			s.runs[name] = true
			for i := len(name) - 1; i > 0; i-- {
				if name[i] == '.' {
					s.runs[name[:i]] = true
				}
			}
		}
	}
}

// readPrivate reads check/private.txt, a path or a directory ending / a line, # comments and blank lines skipped;
// none when the file is not here (TestPrivateList holds each line to the tree).
func readPrivate() ([]string, error) {
	b, err := os.ReadFile(privateList)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, nil
}

// readKept reads docs/tables/records.jsonl and returns the records it marks kept false and kept true, each its key:
// a path, a directory ending /, or a glob (load/results/<id>.*). None when the file is not here.
func readKept() (dropped, kept []string, err error) {
	b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(records)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for i, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var r struct {
			ID   string `json:"id"`
			Kept *bool  `json:"kept"`
		}
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %v", records, i+1, err)
		}
		if r.ID == "" || r.Kept == nil {
			return nil, nil, fmt.Errorf("%s:%d: no id or no kept", records, i+1)
		}
		if *r.Kept {
			kept = append(kept, r.ID)
		} else {
			dropped = append(dropped, r.ID)
		}
	}
	return dropped, kept, nil
}

// public says whether f, a tracked path, is in the public set: not private, and not a result file of a record kept
// false unless a kept record holds it too. The four results READMEs are public.
func (s *site) public(f string, dropped, kept []string) bool {
	if s.isPrivate(f) {
		return false
	}
	if !inResults(f) || path.Base(f) == "README.md" && slices.Contains(resultsDirs, path.Dir(f)) {
		return true
	}
	held := func(recs []string) bool {
		return slices.ContainsFunc(recs, func(r string) bool { return holds(r, f) })
	}
	return held(kept) || !held(dropped)
}

var (
	siteOnce sync.Once
	theSite  *site
	siteErr  error
)

// readSite reads the site once for every test of this file. laug is built into the first caller's t.TempDir() and
// read there; nothing needs it after.
func readSite(t *testing.T) *site {
	t.Helper()
	siteOnce.Do(func() { theSite, siteErr = newSite(t.TempDir()) })
	if siteErr != nil {
		t.Fatal(siteErr)
	}
	return theSite
}

func newSite(dir string) (*site, error) {
	s := &site{targets: map[string]bool{}, commands: map[string][]string{}, tracked: map[string]bool{},
		dirs: map[string]bool{}, runs: map[string]bool{}, ignored: map[string]bool{}}
	out, err := git("ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in the checkout: %v", err)
	}
	for _, f := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if f != "" {
			s.add(f)
		}
	}
	if s.private, err = readPrivate(); err != nil {
		return nil, err
	}
	dropped, kept, err := readKept()
	if err != nil {
		return nil, err
	}
	mk, err := os.ReadFile("../Makefile")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(mk), "\n") {
		if rest, ok := strings.CutPrefix(line, ".PHONY:"); ok {
			for _, f := range strings.Fields(rest) {
				s.targets[f] = true
			}
		}
	}
	if len(s.targets) == 0 {
		return nil, errors.New("the Makefile has no .PHONY line to read the targets from")
	}
	laug := filepath.Join(dir, "laug")
	build := exec.Command("go", "build", "-o", laug, "./cmd/laug")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go build ./cmd/laug: %v\n%s", err, out)
	}
	help, err := run(dir, laug, "help")
	if err != nil {
		return nil, err
	}
	// A row is a name, two spaces or more, then what it does: a command in the help, which lists the words too, each a
	// command or capitalised, as Omes is; a verb in a command's -h, whose flags start with a dash.
	row := regexp.MustCompile(`^  ([a-z][a-z0-9-]*)  +\S`)
	for _, line := range strings.Split(help, "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h, err := run(dir, laug, m[1], "-h")
		if err != nil {
			return nil, err
		}
		var verbs []string
		for _, l := range strings.Split(h, "\n") {
			if v := row.FindStringSubmatch(l); v != nil {
				verbs = append(verbs, v[1])
			}
		}
		s.commands[m[1]] = verbs
	}
	if len(s.commands) == 0 {
		return nil, fmt.Errorf("laug help printed no command:\n%s", help)
	}
	s.pub = &site{targets: s.targets, commands: s.commands, tracked: map[string]bool{}, dirs: map[string]bool{},
		runs: map[string]bool{}, ignored: map[string]bool{}, private: s.private}
	for f := range s.tracked {
		if s.public(f, dropped, kept) {
			s.pub.add(f)
		}
	}
	return s, nil
}

// git runs in the checkout with no GIT_* of the caller's, a hook's GIT_INDEX_FILE say.
func git(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = ".."
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	return cmd
}

// run runs the built laug in dir and returns what it printed on both streams.
func run(dir, laug string, args ...string) (string, error) {
	cmd := exec.Command(laug, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("laug %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String(), nil
}

// isIgnored is git check-ignore's answer for a path git does not track.
func (s *site) isIgnored(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.ignored[p]; ok {
		return v
	}
	s.ignored[p] = git("check-ignore", "-q", "--", p).Run() == nil
	return s.ignored[p]
}

// known says whether p, a path from the checkout's root, is a tracked file, a directory holding one, or a glob (a *
// or {a,b}) that matches one.
func (s *site) known(p string) bool {
	p = path.Clean(strings.TrimSuffix(p, "/"))
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	if s.tracked[p] || s.dirs[p] {
		return true
	}
	if !strings.ContainsAny(p, "*{") {
		return false
	}
	for _, g := range braces(p) {
		if _, err := path.Match(g, ""); err != nil {
			return false
		}
		for f := range s.tracked {
			if ok, _ := path.Match(g, f); ok {
				return true
			}
		}
		for d := range s.dirs {
			if ok, _ := path.Match(g, d); ok {
				return true
			}
		}
	}
	return false
}

// braces expands the first {a,b} in p and then the rest: a{1,2}b{x,y} is four paths.
func braces(p string) []string {
	i := strings.Index(p, "{")
	j := strings.Index(p, "}")
	if i < 0 || j < i {
		return []string{p}
	}
	var out []string
	for _, alt := range strings.Split(p[i+1:j], ",") {
		out = append(out, braces(p[:i]+alt+p[j+1:])...)
	}
	return out
}

// line is one line of a page: inside a fenced block of shell (no info string, or one whose first word is sh, bash,
// shell or console, as a runbook's "sh needs=cell" is), its code is the line less a # comment, and a block of another
// language has none; outside a block, its code is its code spans and its prose is the line with each span blanked.
type line struct {
	n     int
	text  string
	fence bool     // inside a fenced block; a fence's own lines are neither in nor out
	mark  bool     // a fence's own line
	code  []string // code spans, or the fenced line
	prose string
}

var (
	fenceRE = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	shells  = []string{"", "sh", "bash", "shell", "console"}
)

func scan(body string) []line {
	var out []line
	open, shell := "", false
	for i, text := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		l := line{n: i + 1, text: text}
		m := fenceRE.FindStringSubmatch(text)
		switch {
		case m != nil && open == "":
			open, l.mark = m[1], true
			lang, _, _ := strings.Cut(strings.TrimSpace(text[len(m[0]):]), " ")
			shell = slices.Contains(shells, lang)
		case m != nil && m[1][0] == open[0] && len(m[1]) >= len(open) && strings.TrimSpace(text[len(m[0]):]) == "":
			open, l.mark = "", true
		case open != "":
			l.fence = true
			if shell {
				l.code = []string{uncomment(text)}
			}
		default:
			l.code, l.prose = spans(text)
		}
		out = append(out, l)
	}
	return out
}

// uncomment cuts a fenced line at a # that starts it or follows a space.
func uncomment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
	}
	return s
}

// spans splits a line outside a fenced block into its code spans and its prose: a run of n backticks opens a span
// that the next run of exactly n closes; a run with no close is text.
func spans(s string) ([]string, string) {
	var code []string
	var prose strings.Builder
	run := func(i int) int {
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		return j
	}
	for i := 0; i < len(s); {
		if s[i] != '`' {
			prose.WriteByte(s[i])
			i++
			continue
		}
		j := run(i)
		n, end := j-i, -1
		for k := j; k < len(s); {
			if s[k] != '`' {
				k++
				continue
			}
			m := run(k)
			if m-k == n {
				end = k
				break
			}
			k = m
		}
		if end < 0 {
			prose.WriteString(s[i:j])
			i = j
			continue
		}
		code = append(code, strings.TrimSpace(s[j:end]))
		prose.WriteString(strings.Repeat(" ", end+n-i))
		i = end + n
	}
	return code, prose.String()
}

// problem is one place a page breaks a rule.
type problem struct {
	line int
	kind string
	what string
}

var (
	commandSep = regexp.MustCompile(`&&|\|\||[;&|()]|\$\(`)
	before     = regexp.MustCompile(`^(\$|sudo|time|exec|nohup|setsid|env|-.*|[A-Za-z_][A-Za-z0-9_]*=.*)$`)
	makeProse  = regexp.MustCompile(`\bmake ([a-z][a-z0-9]*(?:-[a-z0-9]+)*)\b`)
	word       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	webRE      = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)
	clusterOK  = regexp.MustCompile(`(?i)operator\s+cluster|cluster\s+health|cluster[\s_-]*metadata|` +
		`multi-cluster\s+replication|"cluster"|` + "\u201ccluster\u201d|`cluster`")
	clusterRE  = regexp.MustCompile(`(?i)cluster`)
	linkRE     = regexp.MustCompile(`\]\(\s*<?([^)\s>]*)`)
	refDefRE   = regexp.MustCompile(`^ {0,3}\[[^\]^][^\]]*\]:\s+<?([^>\s]+)`) // [^label]: is a footnote, not a link
	attrRE     = regexp.MustCompile(`\b(src|href|srcset)\s*=\s*"([^"]*)"`)
	schemeRE   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	pathWord   = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.*{},-]+)+/?$`)
	lineSuffix = regexp.MustCompile(`(?::[0-9]+(?:-[0-9]+)?)+$`)
	tokenRE    = regexp.MustCompile("[^\\s`()\\[\\]<>\"'|;]+")
	dateRE     = regexp.MustCompile(`20[0-9][0-9]-[01][0-9]-`)
	anchorRE   = regexp.MustCompile(`<a\s+(?:id|name)\s*=\s*"(stop-[0-9]+)"`)
	headingRE  = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.*?)[\s#]*$`)
	slugDrop   = regexp.MustCompile(`[^a-z0-9 _-]`)
	stopSlug   = regexp.MustCompile(`^stop-[0-9]+$`)
	stopLines  = []string{"The idea", "Evidence", "Try it"}
	stopLineRE = map[string]*regexp.Regexp{}
)

func init() {
	for _, name := range stopLines {
		stopLineRE[name] = regexp.MustCompile(`^\s*(?:[-*+]\s+)?\*\*` + name + `:?\*\*`)
	}
}

// trim takes the punctuation of the sentence around a word off it. A leading _ is emphasis, but not on _obs/, the
// stack's module, whose name starts with one.
func trim(w string) string {
	w = strings.TrimLeft(w, `"'*`)
	if !strings.HasPrefix(w, "_obs/") {
		w = strings.TrimLeft(w, "_")
	}
	return strings.TrimRight(w, `.,:;!?'"*_`)
}

// check returns every problem of page, its path from the checkout's root, holding body.
func (s *site) check(page, body string) []problem {
	var out []problem
	add := func(n int, kind, f string, a ...any) { out = append(out, problem{n, kind, fmt.Sprintf(f, a...)}) }
	dir := path.Dir(page)
	rules := page
	if p := s.like[page]; p != "" {
		rules = p
	}
	lines := scan(body)
	for i, l := range lines {
		for _, c := range l.code {
			s.checkCode(l.n, c, add)
		}
		for _, m := range makeProse.FindAllStringSubmatch(l.prose, -1) {
			_, command := s.commands[m[1]]
			if t := m[1]; (strings.Contains(t, "-") || command) && !s.targets[t] {
				add(l.n, "make", "make %s: no such make target (make help lists them)", t)
			}
		}
		if clusterOutside(lines, i) {
			add(l.n, "cluster", `"cluster" outside Temporal's own terms; the word is cell`)
		}
		if !l.fence && !l.mark {
			s.checkPaths(l, dir, keptReadme(page), add)
		}
		if (rules == learning || rules == capabilities) && !l.mark {
			if d := dateRE.FindString(s.unref(l.text, dir)); d != "" {
				add(l.n, "date", "a date, %s..., outside a tracked path or a run id", d)
			}
		}
	}
	if page == learning {
		s.checkStops(lines, add)
	}
	return out
}

// clusterOutside says whether line i holds "cluster" outside Temporal's terms, a web address and a word in quotes.
// The lines on either side are read with it, so a term a line break splits, "cluster" then "metadata", still counts.
func clusterOutside(lines []line, i int) bool {
	var prev, next string
	if i > 0 {
		prev = lines[i-1].text
	}
	if i+1 < len(lines) {
		next = lines[i+1].text
	}
	j := prev + " " + lines[i].text + " " + next
	lo, hi := len(prev)+1, len(prev)+1+len(lines[i].text)
	ok := append(clusterOK.FindAllStringIndex(j, -1), webRE.FindAllStringIndex(j, -1)...)
	for _, m := range clusterRE.FindAllStringIndex(j, -1) {
		if m[0] < lo || m[0] >= hi {
			continue
		}
		if !slices.ContainsFunc(ok, func(o []int) bool { return o[0] <= m[0] && m[1] <= o[1] }) {
			return true
		}
	}
	return false
}

// checkCode reads each command of a piece of code, past a prompt, a wrapper such as sudo and its flags, and any
// assignment: make's first word that is not a flag or an assignment is a target, and laug's (or bin/laug's) first word
// a command, its second a verb when the command has verbs. A word that is not a name, such as <verb>, is not read.
func (s *site) checkCode(n int, c string, add func(int, string, string, ...any)) {
	for _, part := range commandSep.Split(c, -1) {
		f := strings.Fields(part)
		for len(f) > 0 && before.MatchString(f[0]) {
			f = f[1:]
		}
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "make":
			i := 1
			for i < len(f) && before.MatchString(f[i]) {
				i++
			}
			if i < len(f) && word.MatchString(trim(f[i])) && !s.targets[trim(f[i])] {
				add(n, "make", "make %s: no such make target (make help lists them)", trim(f[i]))
			}
		case "laug", "bin/laug", "./bin/laug":
			cmd := trim(f[1])
			verbs, ok := s.commands[cmd]
			switch {
			case !word.MatchString(cmd):
			case !ok:
				add(n, "laug", "laug %s: no such command (laug help lists them)", cmd)
			case len(verbs) > 0 && len(f) > 2 && word.MatchString(trim(f[2])) && !slices.Contains(verbs, trim(f[2])):
				add(n, "laug", "laug %s %s: %s has no such verb (laug %s -h lists them)", cmd, trim(f[2]), cmd, cmd)
			}
		}
	}
}

// pathTops are the first directories of the code-span paths checkPaths reads: laug's own; _obs, the stack's module;
// obs, where the stack was, kept so a page that still names it fails; and the three that moved under internal/. Under
// internal/ and cmd/ it reads laug's packages only, laugPackages and cmd/laug, since the Go SDK's and the server's
// source paths start with internal/ and cmd/ too (internal/internal_utils.go, cmd/server); a path of another
// repository under one of these names its repository first, as temporalio/omes/docs/... does. TestLaugPackages holds
// laugPackages to the tree, so a new package joins it.
var (
	pathTops = []string{"cells", "check", "docs", "faults", "gate", "load", "obs", "_obs", "health", "orchestrator",
		"pool"}
	laugPackages = []string{"cellstat", "denylist", "follow", "gateopen", "health", "jsonx", "judge", "orchestrator",
		"pool", "poolapi", "rejudge", "report", "script", "tables", "tolerance"}
)

// readsPath says whether checkPaths reads p, a code-span path: under one of pathTops, under cmd/laug, or under
// internal/ and one of laugPackages.
func readsPath(p string) bool {
	top, rest, _ := strings.Cut(p, "/")
	second, _, _ := strings.Cut(rest, "/")
	switch top {
	case "internal":
		return slices.Contains(laugPackages, second)
	case "cmd":
		return second == "laug"
	}
	return slices.Contains(pathTops, top)
}

// keptReadme says whether page is one of the four results READMEs, beside the records kept as laug wrote them.
func keptReadme(page string) bool {
	return path.Base(page) == "README.md" && slices.Contains(resultsDirs, path.Dir(page))
}

// checkPaths reads a line outside a fenced block for links, image paths and code-span paths. On a kept results
// README, a path of cited_paths_test.go's move table passes when the tree holds its new path: the page names
// paths as its records cite them, so a path it names that moved stays as it was.
func (s *site) checkPaths(l line, dir string, kept bool, add func(int, string, string, ...any)) {
	known := func(p string) bool {
		if s.known(p) {
			return true
		}
		to, ok := movedTo(path.Clean(strings.TrimSuffix(p, "/")))
		return kept && ok && s.known(to)
	}
	var targets []string
	for _, m := range linkRE.FindAllStringSubmatch(l.prose, -1) {
		targets = append(targets, m[1])
	}
	if m := refDefRE.FindStringSubmatch(l.prose); m != nil {
		targets = append(targets, m[1])
	}
	for _, m := range attrRE.FindAllStringSubmatch(l.prose, -1) {
		if m[1] != "srcset" {
			targets = append(targets, m[2])
			continue
		}
		for _, c := range strings.Split(m[2], ",") {
			if f := strings.Fields(c); len(f) > 0 {
				targets = append(targets, f[0])
			}
		}
	}
	for _, t := range targets {
		if p, ok := linkPath(dir, t); ok && p != "." && !known(p) {
			add(l.n, "path", "the link %s: %s is not in git ls-files", t, p)
		}
	}
	for _, c := range l.code {
		for _, f := range strings.Fields(c) {
			p := lineSuffix.ReplaceAllString(trim(f), "")
			if !pathWord.MatchString(p) || strings.Contains(p, "...") || !readsPath(p) {
				continue
			}
			if !known(p) && !s.isIgnored(p) {
				add(l.n, "path", "%s is not in git ls-files", p)
			}
		}
	}
}

// linkPath is the path from the checkout's root that link target t names on a page in dir, less its fragment and
// query; ok is false for an empty target, a fragment alone and a web address.
func linkPath(dir, t string) (p string, ok bool) {
	if t == "" || strings.HasPrefix(t, "#") || schemeRE.MatchString(t) {
		return "", false
	}
	p = t
	if i := strings.IndexAny(p, "#?"); i >= 0 {
		p = p[:i]
	}
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	if strings.HasPrefix(p, "/") {
		return strings.TrimLeft(p, "/"), true
	}
	return path.Join(dir, p), true
}

// unref blanks each word of text that names a tracked path, from the root or from dir, or a run id.
func (s *site) unref(text, dir string) string {
	return tokenRE.ReplaceAllStringFunc(text, func(tok string) string {
		w := trim(tok)
		if i := strings.Index(w, "#"); i >= 0 {
			w = w[:i]
		}
		w = lineSuffix.ReplaceAllString(w, "")
		if w != "" && (s.runs[w] || s.known(w) || s.known(path.Join(dir, w))) {
			return strings.Repeat(" ", len(tok))
		}
		return tok
	})
}

// cells splits a table row on each | that no backslash escapes.
func cells(row string) []string {
	r := strings.TrimSpace(row)
	r = strings.TrimPrefix(r, "|")
	if strings.HasSuffix(r, "|") && !strings.HasSuffix(r, `\|`) {
		r = r[:len(r)-1]
	}
	var out []string
	var b strings.Builder
	for i := 0; i < len(r); i++ {
		switch {
		case r[i] == '\\' && i+1 < len(r) && r[i+1] == '|':
			b.WriteString(`\|`)
			i++
		case r[i] == '|':
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(r[i])
		}
	}
	return append(out, strings.TrimSpace(b.String()))
}

// checkStops holds docs/LEARNING.md to its anchors stop-1 to stop-19, then each stop to its three lines. A stop runs
// from its anchor to the next one; with the anchors wrong, the stops are not read.
func (s *site) checkStops(lines []line, add func(int, string, string, ...any)) {
	type anchor struct {
		name string
		n    int
	}
	var got []anchor
	for _, l := range lines {
		if l.fence || l.mark {
			continue
		}
		for _, m := range anchorRE.FindAllStringSubmatch(l.prose, -1) {
			got = append(got, anchor{m[1], l.n})
		}
		if m := headingRE.FindStringSubmatch(l.prose); m != nil {
			slug := strings.ReplaceAll(slugDrop.ReplaceAllString(strings.ToLower(m[1]), ""), " ", "-")
			if stopSlug.MatchString(slug) {
				got = append(got, anchor{slug, l.n})
			}
		}
	}
	var names []string
	for _, a := range got {
		names = append(names, a.name)
	}
	var want []string
	for i := 1; i <= stops; i++ {
		want = append(want, fmt.Sprintf("stop-%d", i))
	}
	if !slices.Equal(names, want) {
		n := 1
		for i := range got {
			if i >= len(want) || got[i].name != want[i] {
				n = got[i].n
				break
			}
		}
		add(n, "anchor", "the anchors are %q; want stop-1 to stop-%d, in order, once each", names, stops)
		return
	}
	for i, a := range got {
		end := len(lines)
		if i+1 < len(got) {
			end = got[i+1].n - 1
		}
		for _, name := range stopLines {
			count := 0
			for _, l := range lines[a.n-1 : end] {
				if !l.fence && !l.mark && stopLineRE[name].MatchString(l.prose) {
					count++
				}
			}
			if count != 1 {
				add(a.n, "stop", "%s holds %d **%s** lines; it needs one", a.name, count, name)
			}
		}
	}
}

// listed reads check/docs.txt, the public pages, and check/docs-private.txt, the private ones, when it is here: a page
// a line, its path as git ls-files prints it; # comments and blank lines skipped. A page of docs.txt is public and a
// page of docs-private.txt private, by check/private.txt.
func listed(t *testing.T, s *site) []string {
	t.Helper()
	var out []string
	for _, list := range []string{pageList, privatePages} {
		b, err := os.ReadFile(list)
		if list == privatePages && errors.Is(err, fs.ErrNotExist) {
			t.Logf("no check/%s here: the public pages alone", privatePages)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			like := ""
			if p, q, ok := strings.Cut(l, " like "); ok && list == privatePages {
				l, like = p, q
			}
			switch {
			case like != "" && like != learning && like != capabilities:
				t.Errorf("check/%s:%d: like %s: a page with rules of its own is %s or %s", list, i+1, like, learning,
					capabilities)
			case strings.ContainsAny(l, " \t"):
				t.Errorf("check/%s:%d: %q is not one path", list, i+1, l)
			case !s.tracked[l]:
				t.Errorf("check/%s:%d: %s is not in git ls-files", list, i+1, l)
			case slices.Contains(out, l):
				t.Errorf("check/%s:%d: %s is listed twice", list, i+1, l)
			case list == pageList && s.isPrivate(l):
				t.Errorf("check/%s:%d: %s is private (check/%s); a private page goes in check/%s", list, i+1, l,
					privateList, privatePages)
			case list == privatePages && !s.isPrivate(l):
				t.Errorf("check/%s:%d: %s is not private (check/%s); a public page goes in check/%s", list, i+1, l,
					privateList, pageList)
			default:
				out = append(out, l)
				if like != "" {
					if s.like == nil {
						s.like = map[string]string{}
					}
					s.like[l] = like
				}
			}
		}
	}
	return out
}

// Every page check/docs.txt and check/docs-private.txt list keeps every rule, a public page against the public set.
func TestPages(t *testing.T) {
	s := readSite(t)
	pages := listed(t, s)
	t.Logf("%d finished pages listed in check/%s", len(pages), pageList)
	for _, p := range pages {
		t.Run(p, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(p)))
			if err != nil {
				t.Fatal(err)
			}
			for _, pr := range s.view(p).check(p, string(b)) {
				t.Errorf("%s:%d: %s: %s", p, pr.line, pr.kind, pr.what)
			}
		})
	}
}

// TestPrivateList: each line of check/private.txt, when it is here, names a tracked path, the file itself among them.
// The public set is built without them (site.public). In the public tree the file is absent and the test has nothing
// to read.
func TestPrivateList(t *testing.T) {
	s := readSite(t)
	if s.private == nil {
		t.Skipf("no check/%s here: the public set is git ls-files", privateList)
	}
	for _, l := range s.private {
		hit := s.tracked[l]
		for f := range s.tracked {
			if hit {
				break
			}
			hit = strings.HasSuffix(l, "/") && strings.HasPrefix(f, l)
		}
		if !hit {
			t.Errorf("check/%s: %s names no path in git ls-files", privateList, l)
		}
	}
	if !s.isPrivate("check/" + privateList) {
		t.Errorf("check/%s does not name itself", privateList)
	}
	t.Logf("%d tracked, %d in the public set", len(s.tracked), len(s.pub.tracked))
}

// stopPage is a docs/LEARNING.md that keeps every rule; edit, when given, changes stop n's lines first. Its Evidence
// lines name dated result files, a run's directory, a glob and run ids, as the real page's do, each a kept record.
func stopPage(edit func(n int, stop []string) []string) string {
	evidence := []string{
		"`cells/results/2026-10-23-boot1.txt`:82-114, on the lab host, an m8i.8xlarge",
		"[boot 1](../cells/results/2026-10-23-boot1.txt), run 2026-10-23-boot1, on the lab host, an m8i.8xlarge",
		"`faults/results/raun-lab{2,6}-*-1.json` and `gate/results/g20261018-092540/verdict.md`, on the lab host",
		"`cells/results/2026-10-24-boot4.txt`:677-679 and `gate/results/g20261018-092540/`; the gate g20261018-092540",
	}
	var b strings.Builder
	b.WriteString("# Temporal as it behaves\n\nThe readings come from laug's release gate, on one lab host.\n")
	for n := 1; n <= stops; n++ {
		stop := []string{
			"",
			fmt.Sprintf(`<a id="stop-%d"></a>`, n),
			fmt.Sprintf("## %d. What the server does", n),
			"",
			"What Temporal does, with its version; `temporal operator cluster health` answers first.",
			"",
			"**The idea**: one sentence.",
			"",
			"**Evidence**: " + evidence[n%len(evidence)] + ".",
			"",
			"**Try it**: `make test-go`, or `bin/laug gate rejudge g20261018-092540`.",
		}
		if edit != nil {
			stop = edit(n, stop)
		}
		b.WriteString(strings.Join(stop, "\n") + "\n")
	}
	return b.String()
}

// Pages that keep every rule hold no problem: LEARNING with dated result paths in its Evidence lines, CAPABILITIES with
// a run's day as evidence, a README with make and laug in a fence, and a page of the forms the rules let through.
func TestPageClean(t *testing.T) {
	s := readSite(t)
	readmeBody := "laug keeps cells warm.\n\n```sh\nmake build && bin/laug deps   # every tool checked\n" +
		"bin/laug pool serve & sleep 2   # make gate ran this once\nsudo make fc-image\n```\n"
	for _, c := range []struct{ page, body string }{
		{learning, stopPage(nil)},
		{capabilities, "# Capabilities\n\n| Gate | State | Evidence |\n| --- | --- | --- |\n" +
			"| 2 | green on g20261018-092540 | `cells/results/2026-10-23-boot1.txt`:364-368 |\n" +
			"| 4 | a \\| in a cell | `faults/results/raun-lab6-kill-history-1.json` |\n"},
		{readme, readmeBody},
		{"docs/plant.md", strings.Join([]string{
			"`make build`, `make -s test-go`, `make fc-restore RESTORES=3` and `laug · make build: bin/laug`.",
			"Make sure the page reads well, and make the change in one commit. make test-go runs it.",
			"`bin/laug gate run 1.31.0 -fake`, `laug gate open -results <dir>`, `laug <command> -h`, `laug lease -h`.",
			"`laug raun persistence-latency` and `laug gate <verb>`; `go build -o bin/laug ./cmd/laug`.",
			"`temporal operator cluster health`, the cluster metadata, `GetCurrentClusterMetadata`; cluster health.",
			"`docker compose -p laug ps` lists a cell's containers; `laug` and `make` alone are words.",
			"No Multi-Cluster Replication. A cell, never \"cluster\"; a label `cluster` elsewhere.",
			"See https://docs.temporal.io/clusters for Temporal's word. A term split by a line break, cluster",
			"metadata, still counts, and so does `temporal operator",
			"cluster health`.",
			"[SECURITY](../SECURITY.md), [the listeners](../SECURITY.md#listeners), [docs](../docs/),",
			"[mark](brand/laug-mark-16.svg).",
			"![the mark](brand/favicon.svg) <img src=\"brand/laug-wordmark.svg\" alt=\"laug\">",
			"<source srcset=\"brand/laug-wordmark-dark.svg 1x, brand/laug-wordmark.svg 2x\">",
			"[ref]: ../README.md",
			"[^s1]: go.temporal.io/sdk v1.49.0, internal/workflow.go:753-763; a footnote, not a link.",
			"`cells/run/pool.pid`, `load/results/<id>.json`, `gate/results/g.../verdict.md`, `internal/judge`.",
			"`gate/lib.sh:144`, `gate/results/g20261018-092540/`, `faults/results/raun-lab{2,6}-*-1.json`.",
			"`internal/pool/pool.go`:636, `cmd/laug/doc.go`; the SDK's `internal/internal_utils.go`:146-147, the " +
				"server's `cmd/server/main.go`.",
			"A date in a page that is not LEARNING or CAPABILITIES: 2026-10-23.",
			"```",
			"[not a link](no-such-page.md) `cells/results/2099-01-01-none.txt` inside a fence",
			"feat(pool): make reaper interval a flag",
			"```",
			"```html",
			"make gate",
			"```",
			"```sh needs=cell exit=1",
			"bin/laug pool serve -backend compose & sleep 2   # a runbook's block",
			"```",
			"```text",
			"laug · gate run: ... make gate ...",
			"```",
		}, "\n") + "\n"},
	} {
		for _, pr := range s.check(c.page, c.body) {
			t.Errorf("%s: a clean page has a problem at line %d: %s: %s", c.page, pr.line, pr.kind, pr.what)
		}
	}
}

// TestLaugPackages: every directory directly under internal/ that git tracks a file in is on laugPackages, so the
// path rule reads a page's paths into a new package from its first commit.
func TestLaugPackages(t *testing.T) {
	s := readSite(t)
	n := 0
	for d := range s.dirs {
		pkg, ok := strings.CutPrefix(d, "internal/")
		if !ok || strings.Contains(pkg, "/") {
			continue
		}
		n++
		if !slices.Contains(laugPackages, pkg) {
			t.Errorf("%s is a package of the tree and not on laugPackages", d)
		}
	}
	if n == 0 {
		t.Error("no directory under internal/ in git ls-files")
	}
}

// One planted line of each kind fails the check with that kind and no other.
func TestPagePlants(t *testing.T) {
	s := readSite(t)
	// A private page that check/docs-private.txt would list as like CAPABILITIES.
	const likePlant = "docs/like-plant.md"
	if s.like == nil {
		s.like = map[string]string{}
	}
	s.like[likePlant] = capabilities
	defer delete(s.like, likePlant)
	plant := func(n int, line string) func(int, []string) []string {
		return func(m int, stop []string) []string {
			if m == n {
				return append(stop, "", line)
			}
			return stop
		}
	}
	date := strings.Join([]string{"2026", "10", "23"}, "-")
	for _, c := range []struct{ name, kind, page, body string }{
		{"a retired target in a code span", "make", "docs/plant.md", "Run `make pool-test` first.\n"},
		{"a retired target in a fence", "make", "docs/plant.md", "```sh\nmake build && make gate\n```\n"},
		{"a retired target after sudo", "make", "docs/plant.md", "```\n$ sudo -E make fc-pool\n```\n"},
		{"a retired target in prose, hyphenated", "make", "docs/plant.md", "Run make cell-up first.\n"},
		{"a retired target in prose, a laug command's name", "make", "docs/plant.md", "Run make gate first.\n"},
		{"no such command", "laug", "docs/plant.md", "Run `bin/laug frob` first.\n"},
		{"no such verb", "laug", "docs/plant.md", "Run `laug gate frob 1.31.0`.\n"},
		{"no such verb in a fence", "laug", "docs/plant.md", "```sh\nsudo bin/laug pool frob -backend x\n```\n"},
		{"no such verb in a marked fence", "laug", "docs/plant.md", "```sh needs=lab\nbin/laug gate frob\n```\n"},
		{"a retired target in a marked fence", "make", "docs/plant.md", "```bash exit=2\nmake gate\n```\n"},
		{"cluster for a cell", "cluster", "docs/plant.md", "Each cluster holds one server.\n"},
		{"cluster beside a term", "cluster", "docs/plant.md", "A cluster, and cluster metadata.\n"},
		{"cluster in a fence's comment", "cluster", "docs/plant.md", "```sh\nbin/laug cell up  # a cluster\n```\n"},
		{"a dead link", "path", "docs/plant.md", "[the page](no-such-page.md)\n"},
		{"a link out of the checkout", "path", "docs/plant.md", "[up](../../elsewhere.md)\n"},
		{"a dead image", "path", "docs/plant.md", "![a figure](img/no-such-figure.png)\n"},
		{"a dead img src", "path", "docs/plant.md", "<img src=\"brand/no-such-mark.svg\" alt=\"laug\">\n"},
		{"a dead reference", "path", "docs/plant.md", "[ref]: ../NO-SUCH.md\n"},
		{"a dead code-span path", "path", "docs/plant.md", "See `cells/results/2099-01-01-none.txt`:12.\n"},
		{"a glob matching nothing", "path", "docs/plant.md", "See `faults/results/raun-nosuch-*.json`.\n"},
		{"a dead path in a laug package", "path", "docs/plant.md", "See `internal/pool/NOPE.go`:12.\n"},
		{"a dead path under cmd/laug", "path", "docs/plant.md", "See `cmd/laug/nope.go`.\n"},
		{"a date in LEARNING", "date", learning, stopPage(plant(5, "Boot 1 ran on "+date+"."))},
		{"a date in a LEARNING code span", "date", learning, stopPage(plant(9, "On `"+date+"`, boot 1."))},
		{"a date in a page like CAPABILITIES", "date", likePlant, "| 0 | green on " + date +
			" | `cells/results/2026-10-23-boot1.txt` |\n"},
		{"a stop's anchor wrong", "anchor", learning, stopPage(func(n int, stop []string) []string {
			if n == stops {
				stop[1] = `<a id="stop-20"></a>`
			}
			return stop
		})},
		{"an anchor twice", "anchor", learning, stopPage(plant(3, `<a name="stop-3"></a>`))},
		{"a stop without Try it", "stop", learning, stopPage(func(n int, stop []string) []string {
			if n == 4 {
				return stop[:len(stop)-1]
			}
			return stop
		})},
		{"a stop with two Evidence lines", "stop", learning, stopPage(plant(11, "**Evidence**: a second line."))},
		{"a date in CAPABILITIES", "date", capabilities, "Boot 4 ran on " + date + ".\n"},
	} {
		got := s.check(c.page, c.body)
		if len(got) == 0 {
			t.Errorf("%s: no problem; want %s", c.name, c.kind)
		}
		for _, pr := range got {
			if pr.kind != c.kind {
				t.Errorf("%s: line %d: %s: %s; want only %s", c.name, pr.line, pr.kind, pr.what, c.kind)
			}
		}
	}
}
