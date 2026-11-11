package report

// lessons.go is the lessons' figures: the figures the stops of docs/LEARNING.md show, each drawn on the light ground
// from committed files it names one by one, never a glob, so that runs added to gate/results change no figure. A data
// figure is drawn by Go from the summary tables under docs/tables, rows it names by key (lessonrows.go): the gates,
// rauns and readings rows, and a figure table, fig-<figure>.jsonl, of the points no other row holds (figtables.go); by
// lessondata.go, by lessonfaults.go of boot 2's pack, by lessoncandidates.go of boot 3's, by lessonworkflow.go of boot
// 4's, or by lessonmicrovm.go of boot 9's and batch E's. A diagram is drawn from its spec, docs/img/src/<name>.txt
// (diagram.go), with data drawn into it where the spec names a panel (lessonpanels.go). laug report -docs writes each
// into docs/img, with the index, README.md. A page's figure, one a directory's README or the root README shows and no
// stop does, is a diagram drawn the same way (pageFigures); the index lists it after the stops.

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// A lesson is one of the lessons' figures.
type lesson struct {
	name string // the file's name less .svg
	stop int    // the stop of docs/LEARNING.md that shows it
	kind string // data, drawn by Go from the summary tables; or diagram, drawn from its spec
	gen  string // the code that draws it, as the index names it
	draw func(fsys fs.FS) (drawn, error)
}

// drawn is a lesson's figure and what the index says of it.
type drawn struct {
	svg     []byte
	sources []string // every committed file it read, as its desc names them
}

const (
	kindData    = "data"
	kindDiagram = "diagram"
	dataGen     = "_report/lessondata.go"
	diagramGen  = "_report/diagram.go"
	// specDir is where the diagrams' specs are, from the checkout's root.
	specDir = "docs/img/src"
)

// lessons are the figures whose data is committed, by stop, then name.
var lessons = []lesson{
	{"healthcheck-path", 1, kindDiagram, "", nil},
	{"replay-compared", 2, kindDiagram, "", nil},
	{"replay-swapped", 2, kindDiagram, "", nil},
	{"warm-check", 3, kindDiagram, "", nil},
	{"kill-history", 4, kindData, dataGen, killHistory},
	{"startup-race", 5, kindDiagram, "", nil},
	{"throttle-landing", 6, kindData, faultGen, throttleLanding},
	{"bucket-edges", 7, kindData, dataGen, bucketEdges},
	{"fc-network", 8, kindDiagram, "", nil},
	{"restore-timing", 8, kindData, microvmGen, restoreTiming},
	{"clock-step-timers", 9, kindData, microvmGen, clockStepTimers},
	{"three-piles", 10, kindDiagram, "", nil},
	{"toxic-fuse", 11, kindDiagram, "", nil},
	{"eager-ab", 12, kindDiagram, "", nil},
	{"candidates", 13, kindData, candidateGen, candidates},
	{"neighbours", 13, kindData, dataGen, neighbours},
	{"stuck-shard", 13, kindData, faultGen, stuckShard},
	{"arm-band", 14, kindData, dataGen, armBand},
	{"incumbent-arm", 14, kindDiagram, "", nil},
	{"orchestrator", 15, kindDiagram, "", nil},
	{"cancel-path", 16, kindDiagram, "", nil},
	{"adopted", 17, kindData, workflowGen, adopted},
	{"gate-layers", 18, kindDiagram, "", nil},
	{"gate-workflow", 18, kindDiagram, "", nil},
	{"obs-stack", 19, kindDiagram, "", nil},
	{"staleness", 19, kindData, workflowGen, staleness},
}

// A pageFigure is a figure a page shows and no stop does: a diagram, drawn from its spec as a lesson's is.
type pageFigure struct {
	name string // the file's name less .svg
	page string // the page that shows it, from the checkout's root
}

// pageFigures are the pages' figures, by page, then name.
var pageFigures = []pageFigure{
	{"map", "README.md"},
	{"cell", "cells/README.md"},
	{"rootfs", "cells/rootfs/README.md"},
}

// lesson is f as a lesson with no stop, so it draws as a diagram lesson does.
func (f pageFigure) lesson() lesson { return lessonOf(lesson{name: f.name, kind: kindDiagram}) }

// lessonOf is the lesson with its draw: a diagram's is its spec's.
func lessonOf(l lesson) lesson {
	if l.kind == kindDiagram {
		spec := specDir + "/" + l.name + ".txt"
		l.gen = spec + ", " + diagramGen
		l.draw = func(fsys fs.FS) (drawn, error) { return diagram(fsys, spec) }
	}
	return l
}

// lessonFiles draws every lesson from fsys, the checkout's root: <name>.svg each, in the lessons' order, then each
// page's figure in pageFigures' order, then README.md, the index, a row each in the same order. The first figure that
// cannot be drawn is the error.
func lessonFiles(fsys fs.FS) ([]File, error) {
	var files []File
	draw := func(l lesson, shownBy string) (string, error) {
		d, err := l.draw(fsys)
		if err != nil {
			return "", fmt.Errorf("report: figure %s: %w", l.name, err)
		}
		files = append(files, File{l.name + ".svg", d.svg})
		in := strings.Join(slices.Compact(slices.Sorted(slices.Values(d.sources))), ", ")
		return indexRow(l.name+".svg", shownBy, l.kind, l.gen, in), nil
	}
	var rows, pageRows []string
	for _, l := range lessons {
		row, err := draw(lessonOf(l), strconv.Itoa(l.stop))
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	for _, f := range pageFigures {
		row, err := draw(f.lesson(), f.page)
		if err != nil {
			return nil, err
		}
		pageRows = append(pageRows, row)
	}
	return append(files, File{"README.md", lessonIndex(rows, pageRows)}), nil
}

// indexRow is a figure's row of the index; shownBy is its stop, or the page that shows it.
func indexRow(file, shownBy, kind, gen, sources string) string {
	return fmt.Sprintf("| [%s](%s) | %s | %s | %s | %s |", file, file, shownBy, kind, mdCell(gen), mdCell(sources))
}

// lessonIndex is the figures' index: what each is drawn from and by, and the stop or the page that shows it.
func lessonIndex(rows, pageRows []string) []byte {
	return []byte(`# Figures

The figures of docs/LEARNING.md's stops, a row each, by stop, then the figures other pages show, a row each, by page.
The renderer, _report, draws each SVG from the committed files its row names, on the light ground in docs/BRAND.md's
colors: a data figure by Go from the summary tables under docs/tables, which
` + "`go -C _report run ./cmd/report -tables`" + ` cuts from the records; a diagram from its spec under docs/img/src,
with data drawn into it where the spec names a panel. Each SVG is labeled: its title and desc say what it shows and
how to read it, and the desc ends in every file it read, one by one. Two renders are the same bytes, and none reads a
row or a file that runs added later would change: a figure reads the rows it names, by key or by a record's prefix,
and ` + "`make docs`" + ` adds rows but refuses to change one.
` + "`make docs`" + ` writes them and this page; a test in ` + "`make test-go`" + ` fails a committed copy that differs.

| Figure | Stop | Kind | Generator | Sources |
| --- | --- | --- | --- | --- |
` + strings.Join(rows, "\n") + `

## The pages' figures

Each is a diagram a page shows and no stop does, drawn as the stops' diagrams are.

| Figure | Page | Kind | Generator | Sources |
| --- | --- | --- | --- | --- |
` + strings.Join(pageRows, "\n") + "\n")
}

// The lessons' figures are this wide; the title is drawn at the top, its subtitle under it.
const (
	lessonW = 800.0
	marginL = 24.0
	marginR = lessonW - 24
)

// lessonFigure starts a lesson's figure on the light ground and draws its title and subtitle at the top. A title that
// would not fit the width, at 9 units a character, is refused.
func lessonFigure(name, title, sub, desc string, sources []string, height float64) *SVG {
	s := New(Figure{Name: name, Title: title, Desc: desc, Sources: sources, Width: lessonW, Height: height,
		Theme: Light})
	if w := textW(title, 14, true); w > marginR-marginL {
		s.fail("the title %q is %v wide, over the figure's %v", title, w, marginR-marginL)
	}
	if w := textW(sub, 12, false); w > marginR-marginL {
		s.fail("the subtitle %q is %v wide, over the figure's %v", sub, w, marginR-marginL)
	}
	s.Text(marginL, 30, title, Font{Size: 14, Bold: true})
	if sub != "" {
		s.Text(marginL, 50, sub, Font{Size: 12})
	}
	return s
}

// textW is about how wide a line of text is set, in user units: 0.56 of its size a character, 0.6 in bold or Mono
// (inference: Inter's and the monospace faces' average advance; the figures leave room past it).
func textW(v string, size float64, wide bool) float64 {
	k := 0.56
	if wide {
		k = 0.6
	}
	return float64(float64(utf8.RuneCountInString(v)) * float64(size*k))
}

// readJSON is the committed file p of fsys as one JSON value, named in the error.
func readJSON(fsys fs.FS, p string) (jsonx.Value, error) {
	data, err := fs.ReadFile(fsys, p)
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	if err != nil {
		return jsonx.Null(), fmt.Errorf("%s: %w", p, err)
	}
	v, err := jsonx.Parse(data)
	if err != nil {
		return jsonx.Null(), fmt.Errorf("%s: %w", p, err)
	}
	return v, nil
}

// need is v's number at keys, an error naming the file and the keys when it is not one: a figure draws no value it
// had to guess. A stamp written as a string, as a raun writes inject_at, reads as its number.
func need(v jsonx.Value, file string, keys ...string) (float64, error) {
	n := stamp(at(v, keys...))
	if !n.OK {
		return 0, fmt.Errorf("%s: %s is not a number", file, strings.Join(keys, "."))
	}
	return n.F, nil
}

// span is the range of xs as a figure writes it, by Num: "a" when all are a, else "a to b".
func span(xs []float64) string {
	if len(xs) == 0 {
		return "none"
	}
	lo, hi := slices.Min(xs), slices.Max(xs)
	if Num(lo) == Num(hi) {
		return Num(lo)
	}
	return Num(lo) + " to " + Num(hi)
}

// spanTo is span with each end to prec decimals, as a title rounds what its figure shows in full.
func spanTo(xs []float64, prec int) string {
	if len(xs) == 0 {
		return "none"
	}
	lo := strconv.FormatFloat(slices.Min(xs), 'f', prec, 64)
	hi := strconv.FormatFloat(slices.Max(xs), 'f', prec, 64)
	if lo == hi {
		return lo
	}
	return lo + " to " + hi
}

// whole is v to the nearest whole number, as a figure writes a count read through increase(), which extrapolates.
func whole(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }

// failedStarts is a count of failed starts in words, whole: "1 failed start", "0 failed starts", "545 failed starts".
func failedStarts(v float64) string {
	if n := whole(v); n != "1" {
		return n + " failed starts"
	}
	return "1 failed start"
}

// wholeSpan is span for counts: each end whole.
func wholeSpan(xs []float64) string {
	if len(xs) == 0 {
		return "none"
	}
	lo, hi := whole(slices.Min(xs)), whole(slices.Max(xs))
	if lo == hi {
		return lo
	}
	return lo + " to " + hi
}
