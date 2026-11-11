package check

import (
	"errors"
	"fmt"
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

// The directory contract. A directory of readmeDirs has a README.md. It says what the directory is, why it exists and
// how to use and extend it, and points to its evidence. Numbers and citations live in docs/CAPABILITIES.md,
// docs/EVIDENCE.md and the records, and the README points there in one line.
// make test-go holds each README to these rules, each named by the kind of problem it reports:
//
//   missing  the directory has a README.md.
//   heading  its headings are these five, in this order, and no other: # <the directory, its path from the
//            checkout's root>, ## Why, ## Use, ## Extend, ## Evidence.
//   image    each image (a markdown image or an <img> tag, outside code and HTML comments) has its file in git
//            ls-files and an alt that is not empty; an <img> has a width and a height, and for an SVG they are its
//            viewBox's.
//   number   no number in prose. A number is a digit run that starts a word, not after a letter, a digit or an _.
//            So sha256, x86_64 and R6 hold none, and v1.17 holds one, 17. Prose is the page less its code (fenced
//            blocks and code spans), its link targets and web addresses, and its HTML comments. It also leaves out
//            the images (a markdown image or an <img>, alt and all) and each image's caption, the italic paragraph
//            under it, since a caption restates its figure. Two names are not numbers: a runbook's, as in runbook 4,
//            and the loopback address 127.0.0.1, where laug listens.
//   section  no transcript section letter: a word of one or two capitals, digits and at most one lower-case letter
//            (A9e, G3, R11g, D1). The rule reads everything but code, link targets and web addresses, so an image's
//            alt, a caption and an HTML comment count too. A record is cited by its key, or its file and line,
//            never by its section. R1 to R7 pass as the judge's rules (docs/DESIGN.md, Release gate). Boot 2's
//            transcript uses the same ids for its sections, so the rule does not catch a cite of those seven.
//
// The page rules of docs_test.go hold the README too, through the check TestPages runs: make targets and laug verbs
// that exist, "cell" not "cluster", no dead path. A directory joins the contract by one line of
// readmeDirs. TestReadmePlants reads the plants under check/testdata/readme-<kind>[-<case>]/ through the same
// function. Each fails with its kind and no other; readme-clean, which holds the forms the rules let through, fails
// with none.

// readmeDirs are the directories whose README.md keeps the contract, a directory a line.
var readmeDirs = []string{
	"gate",
	"internal/orchestrator",
	"_obs",
	"cells",
	"cells/rootfs",
	"internal/health",
	"dashboards",
	"internal/pool",
}

var (
	readmeHeads = []string{"## Why", "## Use", "## Extend", "## Evidence"}
	headRE      = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)
	commentRE   = regexp.MustCompile(`(?s)<!--.*?-->`)
	mdImageRE   = regexp.MustCompile(`!\[([^\]]*)\]\(\s*<?([^)\s>]*)[^)]*\)`)
	imgTagRE    = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	imgAttrRE   = regexp.MustCompile(`(?is)\b(src|alt|width|height)\s*=\s*"([^"]*)"`)
	targetRE    = regexp.MustCompile(`\]\([^)]*\)`)
	refTargetRE = regexp.MustCompile(`(?m)^ {0,3}\[[^\]]+\]:(.*)$`)
	attrValueRE = regexp.MustCompile(`(?is)\b(?:src|href|srcset)\s*=\s*"[^"]*"`)
	numberRE    = regexp.MustCompile(`\b[0-9]+`)
	notNumberRE = regexp.MustCompile(`(?i)\brunbook [0-9]+\b|\b127\.0\.0\.1\b`)
	sectionRE   = regexp.MustCompile(`\b[A-Z]{1,2}[0-9]+[a-z]?\b`)
	judgeRuleRE = regexp.MustCompile(`^R[1-7]$`)
	viewBoxRE   = regexp.MustCompile(`viewBox="\s*[-0-9.]+[ ,]+[-0-9.]+[ ,]+([0-9.]+)[ ,]+([0-9.]+)\s*"`)
	digitsRE    = regexp.MustCompile(`^[0-9]+$`)
)

// blankAt puts a space for each byte of s[i:j] but a newline, so offsets and line numbers hold.
func blankAt(s string, i, j int) string {
	b := []byte(s)
	for k := i; k < j; k++ {
		if b[k] != '\n' {
			b[k] = ' '
		}
	}
	return string(b)
}

// blank blanks each match of re in s; with group > 0, only that group of each match.
func blank(s string, re *regexp.Regexp, group int) string {
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		if i, j := m[2*group], m[2*group+1]; i >= 0 {
			s = blankAt(s, i, j)
		}
	}
	return s
}

// lineAt is the line, from 1, that offset off of s falls on.
func lineAt(s string, off int) int {
	return strings.Count(s[:off], "\n") + 1
}

// untarget blanks what a reader does not read as text: src, href and srcset values, web addresses, link and image
// targets, and reference targets.
func untarget(s string) string {
	s = blank(s, attrValueRE, 0)
	s = blank(s, webRE, 0)
	s = blank(s, targetRE, 0)
	return blank(s, refTargetRE, 1)
}

// figure is one image of a page: where it starts and ends in the page's prose, and its attributes.
type figure struct {
	at, end                 int
	tag                     bool // an <img>, not a markdown image
	src, alt, width, height string
}

// readme returns every problem of dir's README.md, dir a path from the checkout's root.
func (s *site) readme(dir string) []problem {
	page := path.Join(dir, "README.md")
	b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(page)))
	if errors.Is(err, fs.ErrNotExist) {
		return []problem{{1, "missing", page + " is not here; every directory of readmeDirs has one"}}
	}
	if err != nil {
		return []problem{{1, "read", err.Error()}}
	}
	body := string(b)
	v := s.view(page)
	out := v.check(page, body)
	add := func(n int, kind, f string, a ...any) { out = append(out, problem{n, kind, fmt.Sprintf(f, a...)}) }
	lines := scan(body)

	want := append([]string{"# " + dir}, readmeHeads...)
	var got []string
	var at []int
	for _, l := range lines {
		if m := headRE.FindStringSubmatch(l.text); m != nil && !l.fence && !l.mark {
			got, at = append(got, m[1]+" "+m[2]), append(at, l.n)
		}
	}
	if !slices.Equal(got, want) {
		n := len(lines)
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				n = at[i]
				break
			}
		}
		add(n, "heading", "the headings are %q; want %q, in that order, and no other", got, want)
	}

	// prose is the page less its code, line for line: a fenced line is empty and a code span is blanked.
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.prose
	}
	prose := strings.Join(texts, "\n")
	shown := blank(prose, commentRE, 0)

	var figs []figure
	for _, m := range mdImageRE.FindAllStringSubmatchIndex(shown, -1) {
		figs = append(figs, figure{at: m[0], end: m[1], alt: shown[m[2]:m[3]], src: shown[m[4]:m[5]]})
	}
	for _, m := range imgTagRE.FindAllStringIndex(shown, -1) {
		f := figure{at: m[0], end: m[1], tag: true}
		for _, a := range imgAttrRE.FindAllStringSubmatch(shown[m[0]:m[1]], -1) {
			switch strings.ToLower(a[1]) {
			case "src":
				f.src = a[2]
			case "alt":
				f.alt = a[2]
			case "width":
				f.width = a[2]
			case "height":
				f.height = a[2]
			}
		}
		figs = append(figs, f)
	}
	slices.SortFunc(figs, func(a, b figure) int { return a.at - b.at })
	for _, f := range figs {
		v.checkFigure(lineAt(shown, f.at), dir, f, add)
	}

	// The number rule reads the prose less its link targets, its HTML comments, its images and each image's caption.
	// The images go first: untarget blanks a markdown image's target, and mdImageRE no longer matches what is left.
	// pl stays as it is: two images over one caption both find that paragraph, and neither reaches the next one.
	plain := untarget(blank(blank(shown, imgTagRE, 0), mdImageRE, 0))
	pl := strings.Split(plain, "\n")
	captioned := make([]bool, len(pl))
	for _, f := range figs {
		i, j := caption(pl, lines, lineAt(shown, f.end))
		for k := i; k < j; k++ {
			captioned[k] = true
		}
	}
	for i, l := range strings.Split(blank(plain, notNumberRE, 0), "\n") {
		if ns := numberRE.FindAllString(l, -1); ns != nil && !captioned[i] {
			add(i+1, "number", "%s in prose; a number lives in docs/CAPABILITIES.md or a record, and the README "+
				"points there", strings.Join(ns, ", "))
		}
	}

	targetless := untarget(prose)
	for _, m := range sectionRE.FindAllStringIndex(targetless, -1) {
		if w := targetless[m[0]:m[1]]; !judgeRuleRE.MatchString(w) {
			add(lineAt(targetless, m[0]), "section", "%s reads as a transcript's section letter; cite the record by "+
				"its key, or its file and line", w)
		}
	}
	return out
}

// caption returns the lines [i, j), from 0, of the caption under an image whose last line is n, from 1. The caption is
// the paragraph right after the image, past blank lines but not past a fence, when it is one run of italics; with
// none, i equals j.
func caption(pl []string, lines []line, n int) (int, int) {
	i := n // the line after the image's last, from 0
	for i < len(pl) && !lines[i].fence && !lines[i].mark && strings.TrimSpace(pl[i]) == "" {
		i++
	}
	j := i
	for j < len(pl) && !lines[j].fence && !lines[j].mark && strings.TrimSpace(pl[j]) != "" {
		j++
	}
	if i < j && italic(strings.TrimSpace(pl[i]), strings.TrimSpace(pl[j-1])) {
		return i, j
	}
	return i, i
}

// italic says whether a paragraph whose first line is first and last line is last is one run of emphasis.
func italic(first, last string) bool {
	for _, c := range []string{"*", "_"} {
		if strings.HasPrefix(first, c) && !strings.HasPrefix(first, c+c) && strings.HasSuffix(last, c) {
			return true
		}
	}
	return false
}

// checkFigure holds one image of the README in dir, at line n: its alt, its file, and an <img>'s width and height.
func (s *site) checkFigure(n int, dir string, f figure, add func(int, string, string, ...any)) {
	if strings.TrimSpace(f.alt) == "" {
		add(n, "image", "the image %q has no alt text; say what the figure shows", f.src)
	}
	p, ok := linkPath(dir, f.src)
	if !ok || !s.known(p) {
		add(n, "image", "the image %q: its file is not in git ls-files", f.src)
		return
	}
	if !f.tag {
		return
	}
	if !digitsRE.MatchString(f.width) || !digitsRE.MatchString(f.height) {
		add(n, "image", "the <img> of %s has width %q and height %q; it needs both, in pixels", p, f.width, f.height)
		return
	}
	if path.Ext(p) != ".svg" {
		return
	}
	svg, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(p)))
	if err != nil {
		add(n, "image", "%v", err)
		return
	}
	m := viewBoxRE.FindSubmatch(svg)
	if m == nil {
		add(n, "image", "%s has no viewBox to take the <img>'s width and height from", p)
		return
	}
	w, _ := strconv.ParseFloat(string(m[1]), 64)
	h, _ := strconv.ParseFloat(string(m[2]), 64)
	if iw, _ := strconv.ParseFloat(f.width, 64); iw != w {
		add(n, "image", "the <img> of %s is %s wide; its viewBox is %s", p, f.width, m[1])
	}
	if ih, _ := strconv.ParseFloat(f.height, 64); ih != h {
		add(n, "image", "the <img> of %s is %s high; its viewBox is %s", p, f.height, m[2])
	}
}

// Every directory of readmeDirs keeps the directory contract.
func TestReadmes(t *testing.T) {
	s := readSite(t)
	for _, d := range readmeDirs {
		t.Run(d, func(t *testing.T) {
			if n := len(slices.DeleteFunc(slices.Clone(readmeDirs), func(e string) bool { return e != d })); n > 1 {
				t.Errorf("%s is on readmeDirs %d times", d, n)
			}
			for _, pr := range s.readme(d) {
				t.Errorf("%s/README.md:%d: %s: %s", d, pr.line, pr.kind, pr.what)
			}
		})
	}
}

// readmeKinds are the kinds the plants must show: the contract's, and two of docs_test.go's, read through readme.
var readmeKinds = []string{"missing", "heading", "image", "number", "section", "path", "laug"}

// Each plant, check/testdata/readme-<kind>[-<case>]/README.md, fails with its kind and no other, and readme-clean with
// none. The plant for missing is readme-missing, a directory that is not there. A dead image is also a dead path, so
// readme-image-file fails with path too. Every kind of readmeKinds has a plant.
func TestReadmePlants(t *testing.T) {
	s := readSite(t)
	const absent = "readme-missing"
	plants, err := filepath.Glob(filepath.Join("testdata", "readme-*"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plants, filepath.Join("testdata", absent)) {
		t.Fatalf("check/testdata/%s is here; it is the plant for missing, so it stays absent", absent)
	}
	also := map[string]string{"readme-image-file": "path"}
	shown := map[string]bool{}
	for _, p := range append(plants, filepath.Join("testdata", absent)) {
		name := filepath.Base(p)
		kind, _, _ := strings.Cut(strings.TrimPrefix(name, "readme-"), "-")
		got := s.readme("check/testdata/" + name)
		if kind == "clean" {
			for _, pr := range got {
				t.Errorf("%s: a clean README has a problem at line %d: %s: %s", name, pr.line, pr.kind, pr.what)
			}
			continue
		}
		if !slices.Contains(readmeKinds, kind) {
			t.Errorf("%s: %s is not a kind of readmeKinds", name, kind)
			continue
		}
		hit := false
		for _, pr := range got {
			switch pr.kind {
			case kind:
				hit = true
				t.Logf("%s: line %d: %s: %s", name, pr.line, pr.kind, pr.what)
			case also[name]:
				t.Logf("%s: line %d: %s: %s", name, pr.line, pr.kind, pr.what)
			default:
				t.Errorf("%s: line %d: %s: %s; want only %s", name, pr.line, pr.kind, pr.what, kind)
			}
		}
		if !hit {
			t.Errorf("%s: no %s problem", name, kind)
		}
		shown[kind] = true
	}
	for _, k := range readmeKinds {
		if !shown[k] {
			t.Errorf("no plant under check/testdata/ shows %s", k)
		}
	}
}
