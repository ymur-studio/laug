package report

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func drawLessons(t *testing.T) []File {
	t.Helper()
	files, err := lessonFiles(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestLessonsDeterministic: two draws are the same bytes.
func TestLessonsDeterministic(t *testing.T) {
	a, b := drawLessons(t), drawLessons(t)
	for i := range a {
		if a[i].Path != b[i].Path || !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("%s: two draws differ", a[i].Path)
		}
	}
}

// TestLessonFigures reads each figure back, the lessons' and the pages': well-formed, labelled by its name, on the
// light ground in the eight colours, each source in its desc a committed file of the checkout (git ls-files), never a
// glob, and nothing publicScan refuses. Every lesson is named once, by stop; every page's figure once, by page,
// then name, its page a tracked file and its name no lesson's; and every spec under docs/img/src is a lesson's or a
// page's figure's.
func TestLessonFigures(t *testing.T) {
	tracked := trackedFiles(t)
	ok := map[string]bool{"none": true}
	for _, c := range Colours() {
		ok[c.Hex()] = true
	}
	scan := publicScan(t)
	files := drawLessons(t)
	if len(files) != len(lessons)+len(pageFigures)+1 {
		t.Fatalf("%d files, want a figure each and the index", len(files))
	}
	check := func(name string, f File) {
		nodes := parse(t, f.Data)
		if r := nodes[0]; r.name != "svg" || attr(r, "aria-labelledby") != name+"-title "+name+"-desc" ||
			attr(r, "width") != Num(lessonW) {
			t.Errorf("%s: the root reads %v", name, r.attrs)
		}
		desc := strings.Join(strings.Fields(nodes[2].text), " ")
		if nodes[2].name != "desc" || !strings.Contains(desc, "Sources: ") {
			t.Errorf("%s: no desc naming its sources", name)
			return
		}
		srcs := strings.TrimSuffix(strings.SplitN(desc, "Sources: ", 2)[1], ".")
		for _, p := range strings.Split(srcs, ", ") {
			if !tracked[p] {
				t.Errorf("%s: the source %s is not a tracked file", name, p)
			}
		}
		if !bytes.Contains(f.Data, []byte(`fill="`+Steam.Hex()+`"/>`)) {
			t.Errorf("%s: no steam ground", name)
		}
		for _, m := range regexp.MustCompile(`#[0-9A-Fa-f]{3,8}`).FindAll(f.Data, -1) {
			if !ok[string(m)] {
				t.Errorf("%s: a colour of no token: %s", name, m)
			}
		}
		for _, hit := range scan(f.Data) {
			t.Errorf("%s: %s", name, hit)
		}
	}
	for i, l := range lessons {
		check(l.name, files[i])
		if i > 0 && (lessons[i-1].stop > l.stop || lessons[i-1].stop == l.stop && lessons[i-1].name >= l.name) {
			t.Errorf("%s comes before %s: the lessons are by stop, then name", lessons[i-1].name, l.name)
		}
		if l.stop < 1 || l.stop > 19 || (l.kind == kindData) == (l.draw == nil) {
			t.Errorf("%s: stop %d, kind %s", l.name, l.stop, l.kind)
		}
	}
	for i, p := range pageFigures {
		check(p.name, files[len(lessons)+i])
		if i > 0 && (pageFigures[i-1].page > p.page || pageFigures[i-1].page == p.page &&
			pageFigures[i-1].name >= p.name) {
			t.Errorf("%s comes before %s: the pages' figures are by page, then name", pageFigures[i-1].name, p.name)
		}
		if !tracked[p.page] {
			t.Errorf("%s: its page, %s, is not a tracked file", p.name, p.page)
		}
		if slices.ContainsFunc(lessons, func(l lesson) bool { return l.name == p.name }) {
			t.Errorf("%s: a lesson's name too", p.name)
		}
	}
	specs, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(specDir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range specs {
		name := strings.TrimSuffix(e.Name(), ".txt")
		if !slices.ContainsFunc(lessons, func(l lesson) bool { return l.name == name && l.kind == kindDiagram }) &&
			!slices.ContainsFunc(pageFigures, func(p pageFigure) bool { return p.name == name }) {
			t.Errorf("%s/%s: no lesson or page's figure draws it", specDir, e.Name())
		}
	}
}

// trackedFiles is git ls-files at the checkout's root.
func trackedFiles(t *testing.T) map[string]bool {
	t.Helper()
	out, err := gitOutput(root, "ls-files", "-z")
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	m := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		m[p] = true
	}
	return m
}

// TestLessonIndex: the index names each lesson's figure once, by stop, then file name, each with its stop, kind and
// generator, then each page's figure once, by page, then file name, with its page, kind and generator; and its lines
// but the tables' rows keep to 120 columns.
func TestLessonIndex(t *testing.T) {
	files := drawLessons(t)
	idx := string(files[len(files)-1].Data)
	var rows []string
	for _, l := range strings.Split(idx, "\n") {
		if strings.HasPrefix(l, "| [") {
			rows = append(rows, l)
			continue
		}
		if len([]rune(l)) > 120 {
			t.Errorf("a line of %d columns outside the table", len([]rune(l)))
		}
	}
	type want struct {
		file   string
		stop   int
		prefix string
	}
	var wants []want
	for _, l := range lessons {
		l = lessonOf(l)
		wants = append(wants, want{l.name + ".svg", l.stop, fmt.Sprintf("| [%s.svg](%s.svg) | %d | %s | %s | ",
			l.name, l.name, l.stop, l.kind, l.gen)})
	}
	slices.SortStableFunc(wants, func(a, b want) int {
		if a.stop != b.stop {
			return a.stop - b.stop
		}
		return strings.Compare(a.file, b.file)
	})
	pages := slices.Clone(pageFigures)
	slices.SortFunc(pages, func(a, b pageFigure) int {
		return cmp.Or(strings.Compare(a.page, b.page), strings.Compare(a.name, b.name))
	})
	for _, p := range pages {
		l := p.lesson()
		wants = append(wants, want{p.name + ".svg", 0, fmt.Sprintf("| [%s.svg](%s.svg) | %s | %s | %s | ",
			p.name, p.name, p.page, l.kind, l.gen)})
	}
	if len(rows) != len(wants) {
		t.Fatalf("%d rows, want %d", len(rows), len(wants))
	}
	for i, w := range wants {
		if !strings.HasPrefix(rows[i], w.prefix) {
			t.Errorf("row %d: %q, want it to start %q", i+1, rows[i], w.prefix)
		}
	}
}

// TestFigureIndex holds docs/img to its index and its pages. Every file directly in docs/img but the index is a row of
// docs/img/README.md, and every row is a file. A page's figure is shown by its page, and every other file is a
// lesson's, named by docs/LEARNING.md as img/<file>. A page shows its figure with an <img> or a markdown image whose
// src, read from the page's directory, is the figure's file. An image in a ``` fence, in a one-backtick code span on
// one line or in an HTML comment shows nothing, and a link only lists the file.
func TestFigureIndex(t *testing.T) {
	dir := filepath.Join(root, DocsDir, figuresDir)
	idx, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(root, DocsDir, Learning))
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\| \[([^\]]+)\]\(`).FindAllSubmatch(idx, -1) {
		named[string(m[1])] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	hidden := regexp.MustCompile("(?ms)^ *```.*?^ *```[^\\n]*$|<!--.*?-->|`[^`\\n]*`")
	image := regexp.MustCompile(`<img\s[^>]*?\bsrc="([^"]*)"|!\[[^\]]*\]\(([^)\s]*)`)
	shows := func(pg, file string) bool {
		b, err := os.ReadFile(filepath.Join(root, pg))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range image.FindAllSubmatch(hidden.ReplaceAll(b, nil), -1) {
			if path.Join(path.Dir(pg), string(m[1])+string(m[2])) == file {
				return true
			}
		}
		return false
	}
	files := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || e.Name() == "README.md" {
			continue
		}
		files[e.Name()] = true
		if !named[e.Name()] {
			t.Errorf("docs/img/%s: no row of docs/img/README.md names it", e.Name())
		}
		if i := slices.IndexFunc(pageFigures, func(p pageFigure) bool { return p.name+".svg" == e.Name() }); i >= 0 {
			pg, file := pageFigures[i].page, path.Join(DocsDir, figuresDir, e.Name())
			if !shows(pg, file) {
				t.Errorf("docs/img/%s: %s does not show it: no image there has the src %s", e.Name(), pg,
					strings.Repeat("../", strings.Count(pg, "/"))+file)
			}
			continue
		}
		if !bytes.Contains(page, []byte("img/"+e.Name())) {
			t.Errorf("docs/img/%s: docs/LEARNING.md does not show it", e.Name())
		}
	}
	for n := range named {
		if !files[n] {
			t.Errorf("docs/img/README.md names %s, which is not a file there", n)
		}
	}
}

// TestSpecRefusals: a spec that breaks a rule is refused, with its line.
func TestSpecRefusals(t *testing.T) {
	head := "name x\nsize 800 300\ntitle | T\ndesc | D\nsource README.md\n"
	for _, c := range []struct{ spec, want string }{
		{"size 800 300\n", "name comes first"},
		{"name y\n", "the file's name"},
		{"name x\nsize 640 300\n", "size is 800"},
		{head + "frame 1 2\n", `no directive "frame"`},
		{head + "box a 10 10 100 40 fill=red | A\n", "none of docs/BRAND.md's eight"},
		{head + "box a 10 10 100 40 colour=deep | A\n", `takes no option "colour=deep"`},
		{head + "box a 10 10 100 40 | A\nbox a 200 10 100 40 | B\n", "a second box a"},
		{head + "box a 10 10 100 40 | A\narrow a b\n", "a box is missing"},
		{head + "box a 10 10 100 40 | A\nbox b 50 20 100 40 | B\narrow a b\n", "overlap"},
		{head + "box a 750 10 100 40 | A\n", "not inside the figure"},
		{head + "box a 10 10 60 40 | a line far too long for it\n", "wider than its box"},
		{head + "box a 10 10 100 20 | one | two\n", "do not fit"},
		{head + "path 10,10 20,20 | where\n", "needs at=x,y"},
		{head + "path 10,10\n", "two points or more"},
		{head + "text 790 20 | words past the edge\n", "runs outside the figure"},
		{head + "panel nothing 10 10 100 100\n", `no panel "nothing"`},
		{head + "row quotes\n", "row takes 2 places, got 1"},
		{head + "row quotes a:1 | words\n", "row: 1 lines of text"},
		{head + "box a 10 10 100 40 dash=0 | A\n", "lengths over 0"},
		{head + "box a 10 10 100 40 |\n", "an empty line"},
		{"name x\nsize 800 300\ndesc | D\nsource README.md\n", "a title and a desc"},
		{"name x\nsize 800 300\ntitle | T\ndesc | D\n", "no source"},
	} {
		fsys := fstest.MapFS{"x.txt": {Data: []byte(c.spec)}}
		_, err := readSpec(fsys, "x.txt")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.spec, err, c.want)
		}
	}
	// A spec that keeps every rule draws.
	ok := head + "box a 24 70 200 50 | A | two\nbox b 400 70 200 50 fill=deep | B\narrow a b colour=ember dash=4,3 | " +
		"joins\nregion 16 60 640 80 | a region\npath 100,200 300,200 300,250 head colour=water at=120,190 | a path\n" +
		"text 24 280 bold | words\n"
	d, err := diagram(fstest.MapFS{"x.txt": {Data: []byte(ok)}}, "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(d.svg, []byte(">joins</text>")) || !slices.Equal(d.sources, []string{"x.txt", "README.md"}) {
		t.Errorf("the spec drew %d bytes from %q", len(d.svg), d.sources)
	}
}

// TestDiagrams: a spec's row lines name rows of docs/tables. A spec whose rows are all there draws, and its table's
// file joins its sources once; a row planted missing, or its table, refuses the spec with the line and the row's cite.
// Every committed spec's rows are there: TestLessonFigures draws each.
func TestDiagrams(t *testing.T) {
	head := "name x\nsize 800 300\ntitle | T\ndesc | D\n"
	table := []byte(`{"id":"cells/results/a.txt:1","text":"one"}` + "\n" +
		`{"id":"cells/results/a.txt:2","text":"two"}` + "\n")
	fsys := func(spec string) fstest.MapFS {
		return fstest.MapFS{"x.txt": {Data: []byte(spec)}, "docs/tables/quotes.jsonl": {Data: table}}
	}
	ok := head + "row quotes cells/results/a.txt:1\nrow quotes cells/results/a.txt:2\ntext 24 280 | words\n"
	d, err := diagram(fsys(ok), "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.sources, []string{"x.txt", "docs/tables/quotes.jsonl"}) {
		t.Errorf("sources %q, want the spec and the table's file", d.sources)
	}
	for _, c := range []struct {
		fsys fstest.MapFS
		want string
	}{
		{fsys(head + "row quotes cells/results/a.txt:1\nrow quotes cells/results/a.txt:3\n"),
			"x.txt:6: docs/tables/quotes.jsonl#cells/results/a.txt:3: no such row"},
		{fstest.MapFS{"x.txt": {Data: []byte(ok)}}, "x.txt:5: row quotes: "},
		{fsys(head + "row gates g1\n"), "x.txt:5: row gates: "},
	} {
		if _, err := diagram(c.fsys, "x.txt"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("drawn with a row missing: %v, want %q", err, c.want)
		}
	}
}

// TestJoinBoxes: boxes that share columns join down the middle of them, boxes that share rows across, and others
// between their centres, cut at each edge.
func TestJoinBoxes(t *testing.T) {
	a := specBox{0, 0, 100, 40}
	for _, c := range []struct {
		b    specBox
		want [4]float64
	}{
		{specBox{50, 100, 100, 40}, [4]float64{75, 40, 75, 100}},
		{specBox{50, -100, 100, 40}, [4]float64{75, 0, 75, -60}},
		{specBox{200, 20, 100, 40}, [4]float64{100, 30, 200, 30}},
		{specBox{-200, 10, 100, 40}, [4]float64{0, 25, -100, 25}},
		{specBox{200, 200, 100, 40}, [4]float64{70, 40, 230, 200}},
	} {
		x1, y1, x2, y2, ok := joinBoxes(a, c.b)
		if !ok || [4]float64{x1, y1, x2, y2} != c.want {
			t.Errorf("%v to %v: %v %v %v %v, want %v", a, c.b, x1, y1, x2, y2, c.want)
		}
	}
	if _, _, _, _, ok := joinBoxes(a, specBox{50, 20, 100, 40}); ok {
		t.Error("overlapping boxes joined")
	}
}

// TestLessonsNameTheirFiles: a figure's sources are named one by one: a lesson drawn from a checkout that lacks one
// of its files fails and names it.
func TestLessonsNameTheirFiles(t *testing.T) {
	full := os.DirFS(root)
	all := slices.Clone(lessons)
	for _, p := range pageFigures {
		all = append(all, p.lesson())
	}
	for _, l := range all {
		l = lessonOf(l)
		d, err := l.draw(full)
		if err != nil {
			t.Fatal(err)
		}
		fsys := fstest.MapFS{}
		for _, p := range d.sources {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil {
				t.Fatal(err)
			}
			fsys[p] = &fstest.MapFile{Data: data}
		}
		again, err := l.draw(fsys)
		if err != nil || !bytes.Equal(again.svg, d.svg) {
			t.Errorf("%s: drawn from its sources alone: %v", l.name, err)
			continue
		}
		missing := d.sources[0]
		delete(fsys, missing)
		if _, err := l.draw(fsys); err == nil || !strings.Contains(err.Error(), path.Base(missing)) {
			t.Errorf("%s: drawn without %s: %v", l.name, missing, err)
		}
	}
}

// gitOutput runs git in dir and returns its standard output.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// TestSpecShortForms: the forms the redrawn specs brought, which no earlier spec used, draw as written: a path of two
// points and no head is a line alone, however short; a one-line box draws from 21 px tall, its line at the middle,
// and a box of 20 is refused; size=10 sets a tick label at 10.
func TestSpecShortForms(t *testing.T) {
	head := "name x\nsize 800 300\ntitle | T\ndesc | D\nsource README.md\n"
	ok := head + "path 286,226 286,230\nbox a 24 70 40 21 | 10\nbox b 100 70 40 28 | 24\ntext 283 242 size=10 | 0\n"
	d, err := diagram(fstest.MapFS{"x.txt": {Data: []byte(ok)}}, "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<line x1="286" y1="226" x2="286" y2="230" `,
		`<text x="44" y="84.5" font-size="12" `, // 70 + 21/2 + 4
		`<text x="120" y="88" font-size="12" `,  // 70 + 28/2 + 4
		`<text x="283" y="242" font-size="10" `,
	} {
		if !bytes.Contains(d.svg, []byte(want)) {
			t.Errorf("the spec drew no %s", want)
		}
	}
	if bytes.Contains(d.svg, []byte("<polygon")) {
		t.Error("a path with no head drew a head")
	}
	if _, err := readSpec(fstest.MapFS{"x.txt": {Data: []byte(head + "box a 24 70 40 20 | 10\n")}}, "x.txt"); err == nil ||
		!strings.Contains(err.Error(), "do not fit") {
		t.Errorf("a one-line box 20 high: %v, want %q", err, "do not fit")
	}
}
