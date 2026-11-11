// Package report draws laug's figures and renders the committed documents report -docs writes (cmd/report, run by
// make docs; evidence.go). It is its own module, imported by nothing in the main module.
// This file is its SVG writer, which every figure goes through. A figure is the same bytes on every run and every
// host (TestSVGDeterministic, TestWriterGolden):
//
//   - Marks are written in the order they are drawn, each element on a line of its own, each attribute in a fixed
//     order. A caller sorts its data first, runs by gate id, and never ranges over a map into a figure.
//   - A line wraps before column 121: between attributes, between two numbers of a points or stroke-dasharray list,
//     and at a space in a figure's title or desc. No other attribute value is broken: XML reads a newline in a value
//     as one space and keeps the indent after it (XML 1.0, section 3.3.3; seen with Python 3.12's xml.etree), so a
//     broken 'JetBrains Mono' would name no family. More space between two numbers of a list reads the same
//     (SVG 1.1, sections 9.7.1 and 11.4). A mark's text and hover title stay on their line: SVG 1.1 drops a newline
//     in a text (SVG 1.1, section 10.15). TestValuesWhole holds the values.
//   - Every coordinate and computed value is written by Num: 3 decimals, trailing zeros cut, -0 as 0. A coordinate
//     that is NaN or infinite is refused. A product is written float64(x*y), so that no compiler fuses it into an add
//     with one rounding (the Go spec, Floating-point operators), as Go's arm64 compiler does and its amd64 compiler
//     does not. internal/judge's TestNoFusableExpressions holds this package to it.
//   - Colours are the eight of docs/BRAND.md's Colour table, and nothing else (TestColoursFromBrand). A mark with no
//     colour is drawn with none, never with SVG's default black.
//   - Fonts are named, never embedded: Sans on the root, Mono on numbers (docs/BRAND.md, Type). No style element, no
//     font file, no image, no link.
//   - Each figure is labelled: a title and a desc tied to the root by aria-labelledby, the desc ending in the
//     committed files the figure was drawn from, sorted, each a repository-relative path and never a glob. Each axis
//     has a label.
//   - No generation time, host or path but its sources: this file imports no clock, file system or random source
//     (TestWriterImports). Lines end in LF, the last one too.
//
// Figures for documents use Light, a solid steam ground, which GitHub's dark theme shows as a card (inference: as any
// image with an opaque ground); Dark is kept for a figure on a dark ground (docs/BRAND.md: dark is the project's
// default voice, light is for documents).
//
// The standard library only (cmd/laug's TestStdlibOnly).
package report

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Num is a computed value as every figure and page writes it: 3 decimals, then trailing zeros and a trailing point
// cut, and -0, or anything that rounds to it, as 0. strconv rounds the float's exact binary value, so 1.0005 is 1 and
// 0.0005 is 0.001 (TestFloats). NaN and the infinities come back as strconv writes them; the writer refuses them as
// coordinates. A recorded number is shown in its recorded text instead, not through Num.
func Num(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return s
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

// Colour is one of docs/BRAND.md's eight colour tokens. None, the zero Colour, is no colour; any other value is
// refused.
type Colour uint8

// The eight tokens, in docs/BRAND.md's order, each with its use in a figure.
const (
	None   Colour = iota
	Basalt        // the dark ground; ink on light
	Deep          // text on light; the A/A band's fill
	Water         // the accent: series lines and dots
	Silica        // grid and rules on light; text on dark
	Steam         // the light ground; ink on dark
	Warm          // marks and windows
	Moss          // ADVANCE
	Ember         // BLOCK; limits and thresholds
)

var tokens = [...]struct{ name, hex string }{
	{"", "none"},
	{"basalt", "#10181C"},
	{"deep", "#163A44"},
	{"water", "#5FB3BF"},
	{"silica", "#CFE3E4"},
	{"steam", "#EEF3F2"},
	{"warm", "#D99A3A"},
	{"moss", "#5B9B6A"},
	{"ember", "#C44B32"},
}

// Name is the token's name in docs/BRAND.md, empty for None.
func (c Colour) Name() string {
	if int(c) >= len(tokens) {
		return ""
	}
	return tokens[c].name
}

// Hex is the token's value as docs/BRAND.md writes it, "none" for None and empty for a value that is no token.
func (c Colour) Hex() string {
	if int(c) >= len(tokens) {
		return ""
	}
	return tokens[c].hex
}

// Colours are the eight tokens, in docs/BRAND.md's order.
func Colours() []Colour { return []Colour{Basalt, Deep, Water, Silica, Steam, Warm, Moss, Ember} }

// The fonts, named and never embedded. Sans is the root's family. Mono is tokens.css's --laug-font-mono, its double
// quotes written single so that it sits in an attribute.
const (
	Sans = "Inter, system-ui, sans-serif"
	Mono = "ui-monospace, 'JetBrains Mono', Menlo, Consolas, monospace"
)

// Theme is a figure's ground and the colours its axes, words and grid take.
type Theme struct {
	Ground, Ink, Text, Grid Colour
}

// Light is for documents: steam ground, basalt axes, deep text, silica grid. Dark is for a dark ground.
var (
	Light = Theme{Ground: Steam, Ink: Basalt, Text: Deep, Grid: Silica}
	Dark  = Theme{Ground: Basalt, Ink: Silica, Text: Steam, Grid: Deep}
)

// Figure is what a figure is and what it was drawn from.
type Figure struct {
	Name    string   // lower case letters and digits in hyphenated words: the file's name and the ids' prefix
	Title   string   // what the figure shows, one line
	Desc    string   // how to read it: its axes, its lines, what each colour stands beside
	Sources []string // the committed files it was drawn from, repository-relative; written sorted, once each
	Width   float64  // in user units, the viewBox's too
	Height  float64
	Theme   Theme
}

// Style is how a mark is painted. None for Fill or Stroke is no paint; a mark with neither is refused.
type Style struct {
	Fill        Colour
	FillOpacity float64   // over 0 and under 1, or 0 for opaque
	Stroke      Colour    // its width 1 unless Width says otherwise
	Width       float64   // the stroke's width
	Dash        []float64 // the stroke's dashes and gaps
	Title       string    // a hover label, written as the mark's title element
}

// Anchor is where a text sits against its x.
type Anchor uint8

// The anchors: AnchorStart, the zero Anchor, puts the text's start at x.
const (
	AnchorStart Anchor = iota
	AnchorMiddle
	AnchorEnd
)

// Font is how a text is set. The zero Font is 12 units of Sans in the theme's text colour, starting at x.
type Font struct {
	Colour Colour
	Size   float64
	Anchor Anchor
	Mono   bool    // numbers: the Mono family
	Bold   bool    // weight 600
	Rotate float64 // degrees about the text's x and y; -90 for a y axis's label
}

// Point is a point in user units.
type Point struct{ X, Y float64 }

// SVG is one figure being drawn. The first error it meets is kept, and Bytes returns it in place of the figure.
type SVG struct {
	fig     Figure
	title   string // escaped
	desc    string // escaped, the sources at its end
	body    bytes.Buffer
	depth   int
	err     error
	sources []string
}

var figureName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// New starts a figure: the root's labels from f, then the theme's ground over the whole viewBox.
func New(f Figure) *SVG {
	s := &SVG{fig: f}
	if !figureName.MatchString(f.Name) {
		s.fail("the name %q is not lower case letters and digits in hyphenated words", f.Name)
	}
	if f.Title == "" || f.Desc == "" {
		s.fail("a figure needs a title and a desc")
	}
	s.title = s.text(f.Title)
	s.sources = slices.Compact(slices.Sorted(slices.Values(f.Sources)))
	if len(s.sources) == 0 {
		s.fail("no sources: a figure names the committed files it was drawn from")
	}
	for _, p := range s.sources {
		s.source(p)
	}
	s.desc = s.text(f.Desc + " Sources: " + strings.Join(s.sources, ", ") + ".")
	if !(f.Width > 0 && f.Height > 0) || math.IsInf(f.Width, 0) || math.IsInf(f.Height, 0) {
		s.fail("a size of %v by %v", f.Width, f.Height)
	}
	for _, c := range []Colour{f.Theme.Ground, f.Theme.Ink, f.Theme.Text, f.Theme.Grid} {
		if c == None {
			s.fail("a theme with no colour for its ground, ink, text or grid")
			break
		}
	}
	s.Rect(0, 0, f.Width, f.Height, Style{Fill: f.Theme.Ground})
	return s
}

// source refuses a source that is not a repository-relative path to one file.
func (s *SVG) source(p string) {
	switch {
	case strings.ContainsAny(p, "*?[]{}"):
		s.fail("the source %q is a glob: a figure names each file it reads", p)
	case p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.ContainsAny(p, `\:`) ||
		strings.ContainsFunc(p, unicode.IsSpace) || path.Clean(p) != p || p == "." || p == ".." ||
		strings.HasPrefix(p, "../"):
		s.fail("the source %q is not a repository-relative path", p)
	}
}

func (s *SVG) fail(format string, a ...any) {
	if s.err == nil {
		s.err = fmt.Errorf("report: figure %q: %s", s.fig.Name, fmt.Sprintf(format, a...))
	}
}

// num writes a coordinate, refusing NaN and the infinities.
func (s *SVG) num(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		s.fail("a coordinate is %v", v)
		return "0"
	}
	return Num(v)
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

// text escapes v for XML, refusing what a figure cannot hold: text that is not UTF-8, a carriage return (lines end in
// LF), a newline (the writer wraps lines), a control character but the tab, and the two noncharacters XML 1.0 leaves
// out.
func (s *SVG) text(v string) string {
	if !utf8.ValidString(v) {
		s.fail("the text %q is not UTF-8", v)
		return ""
	}
	for _, r := range v {
		switch {
		case r == '\r':
			s.fail("the text %q holds a carriage return: a figure's lines end in LF", v)
		case r == '\n':
			s.fail("the text %q is more than one line", v)
		case r < 0x20 && r != '\t', r == 0xfffe, r == 0xffff:
			s.fail("the text %q holds the character %U", v, r)
		}
	}
	return escaper.Replace(v)
}

// colour writes c, refusing a value that is no token.
func (s *SVG) colour(c Colour) string {
	if c.Hex() == "" {
		s.fail("colour %d is none of docs/BRAND.md's eight", c)
		return "none"
	}
	return c.Hex()
}

// paint is st as attributes: fill always, so that nothing takes SVG's default black, then the rest when set.
func (s *SVG) paint(st Style) []string {
	if st.Fill == None && st.Stroke == None {
		s.fail("a mark with neither fill nor stroke draws nothing")
	}
	a := []string{"fill", s.colour(st.Fill)}
	if st.FillOpacity != 0 {
		if !(st.FillOpacity > 0 && st.FillOpacity < 1) || st.Fill == None {
			s.fail("a fill opacity of %v, on a fill of %q", st.FillOpacity, st.Fill.Hex())
		}
		a = append(a, "fill-opacity", s.num(st.FillOpacity))
	}
	if st.Stroke == None {
		if st.Width != 0 || len(st.Dash) > 0 {
			s.fail("a stroke width or dash with no stroke")
		}
		return a
	}
	a = append(a, "stroke", s.colour(st.Stroke))
	if st.Width != 0 {
		if !(st.Width > 0) {
			s.fail("a stroke width of %v", st.Width)
		}
		a = append(a, "stroke-width", s.num(st.Width))
	}
	if len(st.Dash) > 0 {
		d := make([]string, len(st.Dash))
		for i, v := range st.Dash {
			if !(v >= 0) {
				s.fail("a dash of %v", v)
			}
			d[i] = s.num(v)
		}
		a = append(a, "stroke-dasharray", strings.Join(d, " "))
	}
	return a
}

// element writes one element, <name k="v" ...> then end, wrapped as wrap does.
func (s *SVG) element(name string, attrs []string, end string) {
	w := words(name, attrs)
	w[len(w)-1] += end
	wrap(&s.body, strings.Repeat("  ", s.depth), w)
}

// words is an element's start, <name k="v" ..., as wrap's words: each attribute one word, but a points or
// stroke-dasharray list, whose numbers are words of their own, so that a line breaks only between two of them.
func words(name string, attrs []string) []string {
	w := []string{"<" + name}
	for i := 0; i+1 < len(attrs); i += 2 {
		k, v := attrs[i], attrs[i+1]
		if k != "points" && k != "stroke-dasharray" {
			w = append(w, k+`="`+v+`"`)
			continue
		}
		f := strings.Split(v, " ")
		f[0] = k + `="` + f[0]
		f[len(f)-1] += `"`
		w = append(w, f...)
	}
	return w
}

// wrap writes words joined by single spaces after indent, then LF. A word that would pass column 120 starts a new
// line, indented 4 more; a word longer than a line stays whole.
func wrap(b *bytes.Buffer, indent string, words []string) {
	col := 0
	for i, w := range words {
		n := utf8.RuneCountInString(w)
		switch {
		case i == 0:
			b.WriteString(indent + w)
			col = len(indent) + n
		case col+1+n > 120:
			b.WriteString("\n" + indent + "    " + w)
			col = len(indent) + 4 + n
		default:
			b.WriteString(" " + w)
			col += 1 + n
		}
	}
	b.WriteString("\n")
}

func (s *SVG) mark(name string, geometry []string, st Style) {
	end := "/>"
	if st.Title != "" {
		end = "><title>" + s.text(st.Title) + "</title></" + name + ">"
	}
	s.element(name, append(geometry, s.paint(st)...), end)
}

// Rect draws a rectangle from its top left corner.
func (s *SVG) Rect(x, y, w, h float64, st Style) {
	if !(w >= 0 && h >= 0) {
		s.fail("a rectangle of %v by %v", w, h)
	}
	s.mark("rect", []string{"x", s.num(x), "y", s.num(y), "width", s.num(w), "height", s.num(h)}, st)
}

// Line draws a straight line.
func (s *SVG) Line(x1, y1, x2, y2 float64, st Style) {
	s.mark("line", []string{"x1", s.num(x1), "y1", s.num(y1), "x2", s.num(x2), "y2", s.num(y2)}, st)
}

// Circle draws a circle about its centre.
func (s *SVG) Circle(cx, cy, r float64, st Style) {
	if !(r > 0) {
		s.fail("a circle of radius %v", r)
	}
	s.mark("circle", []string{"cx", s.num(cx), "cy", s.num(cy), "r", s.num(r)}, st)
}

// Polyline draws an open line through two or more points, in their order.
func (s *SVG) Polyline(pts []Point, st Style) { s.mark("polyline", s.points(pts, 2), st) }

// Polygon draws a closed shape through three or more points, in their order: a band, an arrow's head.
func (s *SVG) Polygon(pts []Point, st Style) { s.mark("polygon", s.points(pts, 3), st) }

func (s *SVG) points(pts []Point, least int) []string {
	if len(pts) < least {
		s.fail("%d points, and the mark needs %d", len(pts), least)
	}
	p := make([]string, len(pts))
	for i, pt := range pts {
		p[i] = s.num(pt.X) + "," + s.num(pt.Y)
	}
	return []string{"points", strings.Join(p, " ")}
}

// Text writes one line of text with its baseline at y.
func (s *SVG) Text(x, y float64, v string, f Font) {
	if v == "" {
		s.fail("an empty text labels nothing")
	}
	size := f.Size
	if size == 0 {
		size = 12
	}
	if !(size > 0) {
		s.fail("a font size of %v", f.Size)
	}
	c := f.Colour
	if c == None {
		c = s.fig.Theme.Text
	}
	a := []string{"x", s.num(x), "y", s.num(y), "font-size", s.num(size), "fill", s.colour(c)}
	switch f.Anchor {
	case AnchorStart:
	case AnchorMiddle:
		a = append(a, "text-anchor", "middle")
	case AnchorEnd:
		a = append(a, "text-anchor", "end")
	default:
		s.fail("anchor %d is none of start, middle and end", f.Anchor)
	}
	if f.Mono {
		a = append(a, "font-family", Mono)
	}
	if f.Bold {
		a = append(a, "font-weight", "600")
	}
	if f.Rotate != 0 {
		a = append(a, "transform", "rotate("+s.num(f.Rotate)+" "+s.num(x)+" "+s.num(y)+")")
	}
	s.element("text", a, ">"+s.text(v)+"</text>")
}

// Group opens a group of marks, with label as its accessible name when there is one. EndGroup closes it.
func (s *SVG) Group(label string) {
	var a []string
	if label != "" {
		a = []string{"aria-label", s.text(label)}
	}
	s.element("g", a, ">")
	s.depth++
}

// EndGroup closes the group Group opened last.
func (s *SVG) EndGroup() {
	if s.depth == 0 {
		s.fail("EndGroup with no group open")
		return
	}
	s.depth--
	s.body.WriteString(strings.Repeat("  ", s.depth) + "</g>\n")
}

// Box draws a rectangle with label centred in it, in the theme's text colour: a diagram's step.
func (s *SVG) Box(x, y, w, h float64, label string, st Style) {
	s.Rect(x, y, w, h, st)
	s.Text(x+w/2, y+h/2+4, label, Font{Anchor: AnchorMiddle})
}

// Arrow draws a line from (x1, y1) to (x2, y2) in c, its head a filled triangle 8 units long and 8 wide at the end.
// The head is a polygon, not a marker, so a page that holds several figures holds no ids but their labels'.
func (s *SVG) Arrow(x1, y1, x2, y2 float64, c Colour) {
	dx, dy := x2-x1, y2-y1
	n := math.Sqrt(float64(dx*dx) + float64(dy*dy))
	if !(n > 8) {
		s.fail("an arrow %v long: its head alone is 8", n)
		return
	}
	ux, uy := dx/n, dy/n
	bx, by := x2-float64(8*ux), y2-float64(8*uy)
	s.Line(x1, y1, bx, by, Style{Stroke: c, Width: 1.5})
	s.Polygon([]Point{{x2, y2}, {bx - float64(4*uy), by + float64(4*ux)}, {bx + float64(4*uy), by - float64(4*ux)}},
		Style{Fill: c})
}

// Scale maps a domain, D0 to D1, onto user units, R0 to R1, linearly. A domain of one value maps to the range's
// middle. y grows downward, so a y scale's R0 is the larger.
type Scale struct{ D0, D1, R0, R1 float64 }

// At is v's place in user units.
func (sc Scale) At(v float64) float64 {
	if sc.D1 == sc.D0 {
		return sc.R0 + (sc.R1-sc.R0)/2
	}
	return sc.R0 + float64((v-sc.D0)/(sc.D1-sc.D0)*(sc.R1-sc.R0))
}

// Ticks are the round values, 1, 2 or 5 times a power of ten apart, from lo to hi inclusive, about n of them (nice);
// each is k times the step, never a running sum. The step is 0.001 at least, as Num writes 3 decimals. lo equal to hi
// is one tick; NaN, an infinity, hi under lo or n under 1 is none.
func Ticks(lo, hi float64, n int) []float64 {
	if math.IsNaN(lo) || math.IsNaN(hi) || math.IsInf(lo, 0) || math.IsInf(hi, 0) || hi < lo || n < 1 {
		return nil
	}
	if hi == lo {
		return []float64{lo}
	}
	step := max(nice((hi-lo)/float64(n)), 0.001)
	var t []float64
	for k := math.Ceil(lo/step - 1e-9); k <= math.Floor(hi/step+1e-9); k++ {
		t = append(t, float64(k*step))
	}
	return t
}

// nice is the step for raw: raw's power of ten times 1, 2, 5 or 10, whichever is nearest raw on a log scale, the cuts
// at the square roots of 2, 10 and 50. The power comes from math.Pow10 and comparisons, not from a logarithm, so no
// step rests on a last bit that one architecture's math library may round otherwise (inference).
func nice(raw float64) float64 {
	e := 0
	for raw >= math.Pow10(e+1) {
		e++
	}
	for raw < math.Pow10(e) {
		e--
	}
	p := math.Pow10(e)
	switch r := raw / p; {
	case r >= math.Sqrt(50):
		return math.Pow10(e + 1)
	case r >= math.Sqrt(10):
		return float64(5 * p)
	case r >= math.Sqrt(2):
		return float64(2 * p)
	}
	return p
}

// AxisX draws a horizontal axis at y across sc's range: the rule and a 4-unit tick in the theme's ink, each tick's
// value under it in Mono, and label centred under those.
func (s *SVG) AxisX(sc Scale, y float64, ticks []float64, label string) {
	if label == "" {
		s.fail("an x axis with no label")
	}
	ink := Style{Stroke: s.fig.Theme.Ink}
	s.Line(sc.R0, y, sc.R1, y, ink)
	for _, v := range ticks {
		x := sc.At(v)
		s.Line(x, y, x, y+4, ink)
		s.Text(x, y+16, Num(v), Font{Size: 11, Anchor: AnchorMiddle, Mono: true})
	}
	s.Text(sc.R0+(sc.R1-sc.R0)/2, y+32, label, Font{Anchor: AnchorMiddle})
}

// AxisY draws a vertical axis at x across sc's range: the rule and a 4-unit tick in the theme's ink, each tick's
// value to its left in Mono, and label turned a quarter left of those, 7 units a character of the longest value
// further out.
func (s *SVG) AxisY(sc Scale, x float64, ticks []float64, label string) {
	if label == "" {
		s.fail("a y axis with no label")
	}
	ink := Style{Stroke: s.fig.Theme.Ink}
	s.Line(x, sc.R0, x, sc.R1, ink)
	longest := 0
	for _, v := range ticks {
		y := sc.At(v)
		s.Line(x-4, y, x, y, ink)
		s.Text(x-6, y+4, Num(v), Font{Size: 11, Anchor: AnchorEnd, Mono: true})
		longest = max(longest, len(Num(v)))
	}
	s.Text(x-float64(14+7*longest), sc.R0+(sc.R1-sc.R0)/2, label, Font{Anchor: AnchorMiddle, Rotate: -90})
}

// GridY draws a rule across x0 to x1 at each tick, in the theme's grid colour: drawn first, under the marks.
func (s *SVG) GridY(sc Scale, x0, x1 float64, ticks []float64) {
	for _, v := range ticks {
		y := sc.At(v)
		s.Line(x0, y, x1, y, Style{Stroke: s.fig.Theme.Grid})
	}
}

// Bytes is the figure: the root with its labels, the marks in the order drawn, LF after each line, the last too. It
// returns the first error the figure met instead, or one for a group left open.
func (s *SVG) Bytes() ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.depth != 0 {
		return nil, fmt.Errorf("report: figure %q: %d groups not ended", s.fig.Name, s.depth)
	}
	if s.body.Len() == 0 {
		return nil, errors.New("report: a figure not made by New")
	}
	w, h, id := Num(s.fig.Width), Num(s.fig.Height), s.fig.Name
	root := words("svg", []string{"xmlns", "http://www.w3.org/2000/svg", "width", w, "height", h, "viewBox",
		"0 0 " + w + " " + h, "role", "img", "aria-labelledby", id + "-title " + id + "-desc", "font-family", Sans})
	root[len(root)-1] += ">"
	var b bytes.Buffer
	wrap(&b, "", root)
	wrap(&b, "", strings.Split(`<title id="`+id+`-title">`+s.title+"</title>", " "))
	wrap(&b, "", strings.Split(`<desc id="`+id+`-desc">`+s.desc+"</desc>", " "))
	b.Write(s.body.Bytes())
	b.WriteString("</svg>\n")
	return b.Bytes(), nil
}
