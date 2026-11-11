package report

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var write = flag.Bool("write", false, "rewrite the goldens under testdata")

// sample draws every mark the writer has once, on invented values: no run's.
func sample(theme Theme, sources []string) *SVG {
	s := New(Figure{
		Name:    "writer-test",
		Title:   "The writer's test figure: every mark once",
		Desc:    "Invented values, not a run's: five dots against a band and a limit, then two boxes and an arrow.",
		Sources: sources,
		Width:   480,
		Height:  340,
		Theme:   theme,
	})
	x := Scale{D0: 0, D1: 4, R0: 80, R1: 450}
	y := Scale{D0: -1, D1: 1, R0: 220, R1: 40}
	ticks := Ticks(-1, 1, 4)
	s.GridY(y, x.R0, x.R1, ticks)
	s.AxisY(y, x.R0, ticks, "d, ms")
	s.AxisX(x, y.R0, Ticks(0, 4, 4), "run")
	s.Group("the A/A band, d from -0.25 to 0.25")
	s.Polygon([]Point{{x.R0, y.At(0.25)}, {x.R1, y.At(0.25)}, {x.R1, y.At(-0.25)}, {x.R0, y.At(-0.25)}},
		Style{Fill: Deep, FillOpacity: 0.25})
	s.EndGroup()
	s.Line(x.R0, y.At(0.75), x.R1, y.At(0.75), Style{Stroke: Ember, Width: 1.5, Dash: []float64{4, 3}})
	s.Text(x.R1, y.At(0.75)-4, "limit 0.75", Font{Colour: Ember, Size: 11, Anchor: AnchorEnd, Mono: true})
	// The band's label, centred at 7-character coordinates: its line reaches column 120 inside Mono's family, which the
	// writer moves whole to the next line (TestValuesWhole).
	s.Text(x.At(2.125), y.At(0.2875), "band 0.25", Font{Size: 11, Anchor: AnchorMiddle, Mono: true})
	d := []float64{0.1, -0.2, 1 / 3.0, 0.9, -0.0001}
	pts := make([]Point, len(d))
	for i, v := range d {
		pts[i] = Point{x.At(float64(i)), y.At(v)}
	}
	s.Polyline(pts, Style{Stroke: Water, Width: 2})
	long := make([]Point, 37)
	for i := range long {
		long[i] = Point{x.At(float64(i) / 9), y.At(float64(i%7-3) / 8)}
	}
	s.Polyline(long, Style{Stroke: Warm, Dash: []float64{2, 2}, Title: "a series of 37 points, wrapped"})
	s.Group("the runs")
	for i, v := range d {
		c, word := Moss, "ADVANCE"
		if v > 0.75 {
			c, word = Ember, "BLOCK"
		}
		s.Circle(pts[i].X, pts[i].Y, 4, Style{Fill: c, Stroke: Basalt, Title: fmt.Sprintf("run %d: d %s, %s", i,
			Num(v), word)})
		if word == "BLOCK" {
			s.Text(pts[i].X+8, pts[i].Y+4, word, Font{Colour: Ember, Size: 11, Bold: true})
		}
	}
	s.EndGroup()
	s.Box(80, 280, 120, 36, "lease & load", Style{Fill: Silica, Stroke: Deep})
	s.Box(330, 280, 120, 36, "verdict", Style{Fill: Warm})
	s.Arrow(200, 298, 330, 298, Deep)
	return s
}

func render(t *testing.T, s *SVG) []byte {
	t.Helper()
	b, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var sources = []string{"_report/svg_test.go", "docs/BRAND.md"}

// node is one element of a figure as encoding/xml reads it: its name, its attributes as written, and the text directly
// in it.
type node struct {
	name  string
	attrs []xml.Attr
	text  string
}

// parse reads a figure with encoding/xml, failing the test on anything that is not well-formed, and returns its
// elements in document order.
func parse(t *testing.T, b []byte) []node {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(b))
	var nodes []node
	var open []int
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nodes
		}
		if err != nil {
			t.Fatalf("not well-formed: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			open = append(open, len(nodes))
			nodes = append(nodes, node{name: tok.Name.Local, attrs: tok.Attr})
		case xml.EndElement:
			open = open[:len(open)-1]
		case xml.CharData:
			if len(open) > 0 {
				nodes[open[len(open)-1]].text += string(tok)
			}
		}
	}
}

// xmlReads is an attribute value as XML reads it: each newline, tab and carriage return one space, and the spaces
// after it kept (XML 1.0, section 3.3.3, attribute-value normalization). encoding/xml leaves the value as written.
var xmlReads = strings.NewReplacer("\n", " ", "\t", " ", "\r", " ").Replace

// attr is the value of n's attribute k as XML reads it, empty when n has none.
func attr(n node, k string) string {
	for _, a := range n.attrs {
		if a.Name.Local == k {
			return xmlReads(a.Value)
		}
	}
	return ""
}

func TestFloats(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{-0.0004, "0"},
		{1e-9, "0"},
		{1, "1"},
		{100, "100"},
		{1234567, "1234567"},
		{-2.5, "-2.5"},
		{1.5, "1.5"},
		{1.25, "1.25"},
		{12.3456789, "12.346"},
		{-12.3456789, "-12.346"},
		{2.0004999, "2"},
		{0.1 + 0.2, "0.3"},
		{1 / 3.0, "0.333"},
		{1.0005, "1"},     // the float is 1.000499...: strconv rounds the exact value
		{0.0005, "0.001"}, // and this one is 0.000500...01
		{1e21, "1000000000000000000000"},
		{math.NaN(), "NaN"},
		{math.Inf(1), "+Inf"},
		{math.Inf(-1), "-Inf"},
	} {
		if got := Num(c.v); got != c.want {
			t.Errorf("Num(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// TestSVGDeterministic renders the sample twice, its sources given in two orders and once twice over: equal bytes,
// LF endings, a final newline, no line past 120 columns, and every number at 3 decimals or fewer.
func TestSVGDeterministic(t *testing.T) {
	for _, theme := range []Theme{Light, Dark} {
		a := render(t, sample(theme, sources))
		r := slices.Clone(sources)
		slices.Reverse(r)
		b := render(t, sample(theme, append(r, r...)))
		if !bytes.Equal(a, b) {
			t.Fatalf("two renders differ:\n%s\n%s", a, b)
		}
		if bytes.Contains(a, []byte("\r")) || !bytes.HasSuffix(a, []byte("</svg>\n")) {
			t.Errorf("not LF endings with a final newline:\n%q", a)
		}
		wrapped := 0
		for i, line := range strings.Split(string(a), "\n") {
			if n := utf8.RuneCountInString(line); n > 120 {
				t.Errorf("line %d is %d columns: %s", i+1, n, line)
			}
			if strings.HasPrefix(strings.TrimLeft(line, " "), "<") == false && line != "" {
				wrapped++
			}
		}
		if wrapped < 3 {
			t.Errorf("%d lines wrapped; the root, the desc and the long series wrap", wrapped)
		}
		if m := regexp.MustCompile(`[0-9]\.[0-9]{4}|[0-9]\.[0-9]*0"|-0[" ,)]`).Find(a); m != nil {
			t.Errorf("a number past 3 decimals, with a trailing zero or as -0: %q", m)
		}
	}
}

// TestWriterGolden holds the sample to testdata, light and dark; go test -run TestWriterGolden -args -write rewrites
// them.
func TestWriterGolden(t *testing.T) {
	for name, theme := range map[string]Theme{"light": Light, "dark": Dark} {
		got := render(t, sample(theme, sources))
		file := filepath.Join("testdata", "writer-"+name+".svg")
		if *write {
			if err := os.WriteFile(file, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%v (go test -run TestWriterGolden -args -write writes it)", err)
		}
		if !bytes.Equal(got, want) {
			g, w := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
			for i := range min(len(g), len(w)) {
				if g[i] != w[i] {
					t.Fatalf("%s differs at line %d:\ngot  %s\nwant %s", file, i+1, g[i], w[i])
				}
			}
			t.Fatalf("%s: %d lines, the render %d", file, len(w), len(g))
		}
	}
}

// TestColoursFromBrand ties the eight tokens to docs/BRAND.md's Colour table and docs/brand/tokens.css, by name, value
// and order, as cmd/laug's TestVerdictColoursMatchBrand ties the terminal's.
func TestColoursFromBrand(t *testing.T) {
	var want []string
	for _, c := range Colours() {
		want = append(want, c.Name()+" "+c.Hex())
	}
	for file, re := range map[string]string{
		"../docs/BRAND.md":         "(?m)^\\| ([a-z]+) \\| `(#[0-9A-Fa-f]{6})` \\|",
		"../docs/brand/tokens.css": `(?m)^ *--laug-([a-z]+): *(#[0-9A-Fa-f]{6});`,
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range regexp.MustCompile(re).FindAllSubmatch(b, -1) {
			got = append(got, string(m[1])+" "+strings.ToUpper(string(m[2])))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s holds %q; the writer %q", file, got, want)
		}
	}
	if None.Hex() != "none" || Colour(len(tokens)).Hex() != "" {
		t.Errorf("None is %q and a value past the eight %q", None.Hex(), Colour(len(tokens)).Hex())
	}
}

// TestFontsNamed holds the families to tokens.css and the sample to fonts named, never embedded or fetched.
func TestFontsNamed(t *testing.T) {
	css, err := os.ReadFile("../docs/brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	family := func(name string) string {
		m := regexp.MustCompile(`--laug-font-` + name + `: *([^;]*);`).FindSubmatch(css)
		if m == nil {
			t.Fatalf("tokens.css has no --laug-font-%s", name)
		}
		return strings.ReplaceAll(string(m[1]), `"`, "'")
	}
	if mono := family("mono"); Mono != mono {
		t.Errorf("Mono is %q, tokens.css %q", Mono, mono)
	}
	// Sans keeps tokens.css's first and last families and system-ui, in its order.
	sans := strings.Split(family("sans"), ", ")
	at := -1
	for _, f := range strings.Split(Sans, ", ") {
		i := slices.Index(sans, f)
		if i <= at {
			t.Errorf("Sans's %q is not in tokens.css's %q after the families before it", f, sans)
		}
		at = i
	}
	out := render(t, sample(Light, sources))
	nodes := parse(t, out)
	if f := attr(nodes[0], "font-family"); nodes[0].name != "svg" || f != Sans {
		t.Errorf("the root's family reads %q, want Sans, %q", f, Sans)
	}
	mono := 0
	for _, n := range nodes[1:] {
		if f := attr(n, "font-family"); f != "" {
			if n.name != "text" || f != Mono {
				t.Errorf("a %s's family reads %q, want Mono, %q", n.name, f, Mono)
			}
			mono++
		}
	}
	if mono == 0 {
		t.Errorf("no number names Mono:\n%s", out)
	}
	for _, no := range []string{"<style", "@font-face", "url(", "href", "data:", "<image", "<font", "<link"} {
		if bytes.Contains(out, []byte(no)) {
			t.Errorf("the sample holds %q", no)
		}
	}
}

// TestOnlyBrandColours: every paint in the sample is one of the eight or none, and every hex in it is one of the
// eight.
func TestOnlyBrandColours(t *testing.T) {
	ok := map[string]bool{"none": true}
	for _, c := range Colours() {
		ok[c.Hex()] = true
	}
	for _, theme := range []Theme{Light, Dark} {
		out := render(t, sample(theme, sources))
		paints := regexp.MustCompile(`(fill|stroke)="([^"]*)"`).FindAllSubmatch(out, -1)
		if len(paints) < 20 {
			t.Fatalf("%d paints in the sample", len(paints))
		}
		for _, m := range paints {
			if !ok[string(m[2])] {
				t.Errorf("%s", m[0])
			}
		}
		for _, m := range regexp.MustCompile(`#[0-9A-Fa-f]{3,8}`).FindAll(out, -1) {
			if !ok[string(m)] {
				t.Errorf("a colour of no token: %s", m)
			}
		}
		if !bytes.Contains(out, []byte(`<rect x="0" y="0" width="480" height="340" fill="`+theme.Ground.Hex()+`"/>`)) {
			t.Errorf("no solid ground in %s first", theme.Ground.Name())
		}
	}
}

// TestLabelled: the root names its title and desc, the desc ends in the sources sorted and once each, and each axis
// and the band carry their labels. Each element is its name, its id, its aria-label and its words: a title or desc
// wraps at a space, so its words are compared, not its spaces.
func TestLabelled(t *testing.T) {
	nodes := parse(t, render(t, sample(Light, append(sources, sources[0]))))
	if r := nodes[0]; r.name != "svg" || attr(r, "role") != "img" ||
		attr(r, "aria-labelledby") != "writer-test-title writer-test-desc" {
		t.Errorf("the root reads %v, want role img labelled by writer-test-title and writer-test-desc", r.attrs)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, strings.Join([]string{n.name, attr(n, "id"), attr(n, "aria-label"),
			strings.Join(strings.Fields(n.text), " ")}, "|"))
	}
	for _, want := range []string{
		"title|writer-test-title||The writer's test figure: every mark once",
		"desc|writer-test-desc||Invented values, not a run's: five dots against a band and a limit, then two boxes and " +
			"an arrow. Sources: _report/svg_test.go, docs/BRAND.md.",
		"text|||d, ms", "text|||run", "g||the A/A band, d from -0.25 to 0.25|", "title|||run 3: d 0.9, BLOCK",
		"text|||lease & load",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q among the elements:\n%s", want, strings.Join(got, "\n"))
		}
	}
}

// TestValuesWhole: every attribute of the sample, light and dark, reads in XML as the writer joined it. XML reads a
// newline in a value as a space and keeps the indent after it, so a value broken across lines reads with the indent's
// spaces in it: 'JetBrains Mono' would name no family. Only a points or stroke-dasharray list breaks, a number or a
// pair of them a word, so more space between two reads the same. The sample holds one list broken across lines, and
// the band's label, centred at 7-character coordinates, whose line reaches column 120 inside Mono's family.
func TestValuesWhole(t *testing.T) {
	broken := regexp.MustCompile(`\n *`)
	number := regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?(,-?[0-9]+(\.[0-9]+)?)?$`)
	for _, theme := range []Theme{Light, Dark} {
		lists, labels := 0, 0
		for _, n := range parse(t, render(t, sample(theme, sources))) {
			for _, a := range n.attrs {
				k, read, joined := a.Name.Local, xmlReads(a.Value), broken.ReplaceAllString(a.Value, " ")
				switch {
				case k == "points" || k == "stroke-dasharray":
					for _, f := range strings.Fields(read) {
						if !number.MatchString(f) {
							t.Errorf("<%s %s>: %q is not a number or a pair", n.name, k, f)
						}
					}
					if read != joined {
						lists++
					}
				case read != joined:
					t.Errorf("<%s %s>: XML reads %q; the writer joined %q", n.name, k, read, joined)
				}
			}
			if x, y := attr(n, "x"), attr(n, "y"); n.name == "text" && attr(n, "text-anchor") == "middle" &&
				attr(n, "font-family") == Mono && len(x) == 7 && len(y) == 7 {
				labels++
			}
		}
		if lists == 0 || labels == 0 {
			t.Errorf("%d lists broken across lines and %d centred Mono texts at 7-character coordinates; the "+
				"sample has one of each", lists, labels)
		}
	}
}

// TestRefusals: each figure here breaks one rule, and Bytes returns an error that names it instead of the figure.
func TestRefusals(t *testing.T) {
	fig := func() Figure {
		return Figure{Name: "f", Title: "t", Desc: "d", Sources: []string{"a/b.json"}, Width: 10, Height: 10,
			Theme: Light}
	}
	with := func(edit func(*Figure)) func() *SVG {
		return func() *SVG { f := fig(); edit(&f); return New(f) }
	}
	draw := func(d func(*SVG)) func() *SVG { return func() *SVG { s := New(fig()); d(s); return s } }
	ink := Style{Stroke: Basalt}
	for _, c := range []struct {
		name string
		make func() *SVG
		want string
	}{
		{"a name with capitals", with(func(f *Figure) { f.Name = "Writer Test" }), "hyphenated words"},
		{"no title", with(func(f *Figure) { f.Title = "" }), "a title and a desc"},
		{"a title of two lines", with(func(f *Figure) { f.Title = "a\nb" }), "more than one line"},
		{"a desc of two lines", with(func(f *Figure) { f.Desc = "a\nb" }), "more than one line"},
		{"a directory", with(func(f *Figure) { f.Sources = []string{"."} }), "repository-relative"},
		{"no desc", with(func(f *Figure) { f.Desc = "" }), "a title and a desc"},
		{"no sources", with(func(f *Figure) { f.Sources = nil }), "no sources"},
		{"an absolute source", with(func(f *Figure) { f.Sources = []string{"/srv/x.json"} }), "repository-relative"},
		{"a source above the tree", with(func(f *Figure) { f.Sources = []string{"../x.json"} }), "repository-relative"},
		{"an unclean source", with(func(f *Figure) { f.Sources = []string{"a/./b.json"} }), "repository-relative"},
		{"a source with a line", with(func(f *Figure) { f.Sources = []string{"a.txt:12"} }), "repository-relative"},
		{"a glob", with(func(f *Figure) { f.Sources = []string{"gate/results/*/verdict.json"} }), "a glob"},
		{"no width", with(func(f *Figure) { f.Width = 0 }), "a size of"},
		{"an infinite height", with(func(f *Figure) { f.Height = math.Inf(1) }), "a size of"},
		{"a theme with no grid", with(func(f *Figure) { f.Theme.Grid = None }), "a theme with no colour"},
		{"a NaN", draw(func(s *SVG) { s.Rect(math.NaN(), 0, 1, 1, ink) }), "a coordinate is NaN"},
		{"an infinity", draw(func(s *SVG) { s.Circle(0, math.Inf(-1), 1, ink) }), "a coordinate is -Inf"},
		{"a negative width", draw(func(s *SVG) { s.Rect(0, 0, -1, 1, ink) }), "a rectangle of -1 by 1"},
		{"a zero radius", draw(func(s *SVG) { s.Circle(0, 0, 0, ink) }), "a circle of radius 0"},
		{"a carriage return", draw(func(s *SVG) { s.Text(0, 0, "a\r\n", Font{}) }), "carriage return"},
		{"not UTF-8", draw(func(s *SVG) { s.Text(0, 0, "a\xff", Font{}) }), "not UTF-8"},
		{"a bell", draw(func(s *SVG) { s.Text(0, 0, "a\a", Font{}) }), "U+0007"},
		{"an empty text", draw(func(s *SVG) { s.Text(0, 0, "", Font{}) }), "an empty text"},
		{"no anchor", draw(func(s *SVG) { s.Text(0, 0, "a", Font{Anchor: 3}) }), "anchor 3"},
		{"a negative size", draw(func(s *SVG) { s.Text(0, 0, "a", Font{Size: -1}) }), "a font size of -1"},
		{"no token", draw(func(s *SVG) { s.Line(0, 0, 1, 1, Style{Stroke: 42}) }), "colour 42"},
		{"no paint", draw(func(s *SVG) { s.Line(0, 0, 1, 1, Style{}) }), "draws nothing"},
		{"an opacity of 1.5", draw(func(s *SVG) { s.Rect(0, 0, 1, 1, Style{Fill: Deep, FillOpacity: 1.5}) }),
			"a fill opacity of 1.5"},
		{"an opacity with no fill", draw(func(s *SVG) { s.Rect(0, 0, 1, 1, Style{Stroke: Deep, FillOpacity: 0.5}) }),
			"a fill opacity of 0.5"},
		{"a width with no stroke", draw(func(s *SVG) { s.Rect(0, 0, 1, 1, Style{Fill: Deep, Width: 2}) }),
			"no stroke"},
		{"a negative stroke", draw(func(s *SVG) { s.Line(0, 0, 1, 1, Style{Stroke: Deep, Width: -2}) }),
			"a stroke width of -2"},
		{"a negative dash", draw(func(s *SVG) { s.Line(0, 0, 1, 1, Style{Stroke: Deep, Dash: []float64{-1}}) }),
			"a dash of -1"},
		{"one point", draw(func(s *SVG) { s.Polyline([]Point{{0, 0}}, ink) }), "1 points, and the mark needs 2"},
		{"two corners", draw(func(s *SVG) { s.Polygon([]Point{{0, 0}, {1, 1}}, ink) }), "2 points, and the mark needs 3"},
		{"a short arrow", draw(func(s *SVG) { s.Arrow(0, 0, 3, 4, Deep) }), "an arrow 5 long"},
		{"an x axis with no label", draw(func(s *SVG) { s.AxisX(Scale{0, 1, 0, 10}, 5, nil, "") }), "no label"},
		{"a y axis with no label", draw(func(s *SVG) { s.AxisY(Scale{0, 1, 10, 0}, 5, nil, "") }), "no label"},
		{"an end with no group", draw(func(s *SVG) { s.EndGroup() }), "no group open"},
		{"a group left open", draw(func(s *SVG) { s.Group("a") }), "1 groups not ended"},
		{"no New", func() *SVG { return &SVG{} }, "not made by New"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, err := c.make().Bytes()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v and %d bytes, want an error with %q", err, len(b), c.want)
			}
		})
	}
	// The first error stands: a later one does not replace it.
	s := New(fig())
	s.Rect(math.NaN(), 0, 1, 1, ink)
	s.Text(0, 0, "", Font{})
	if _, err := s.Bytes(); err == nil || !strings.Contains(err.Error(), "NaN") {
		t.Errorf("got %v, want the first error, the NaN", err)
	}
}

func TestTicks(t *testing.T) {
	for _, c := range []struct {
		lo, hi float64
		n      int
		want   string
	}{
		{0, 10, 5, "0 2 4 6 8 10"},
		{-1, 1, 4, "-1 -0.5 0 0.5 1"},
		{0.1, 0.95, 4, "0.2 0.4 0.6 0.8"},
		{0, 0.3, 3, "0 0.1 0.2 0.3"},
		{-3, 7, 5, "-2 0 2 4 6"},
		{0, 1000, 4, "0 200 400 600 800 1000"},
		{0, 1000, 2, "0 500 1000"},
		{0, 3, 1, "0 2"},
		{17.5, 50.7, 4, "20 30 40 50"},
		{0, 0.0001, 5, "0"},
		{3, 3, 4, "3"},
		{1, 0, 4, ""},
		{0, 1, 0, ""},
		{math.NaN(), 1, 4, ""},
		{0, math.Inf(1), 4, ""},
	} {
		var got []string
		for _, v := range Ticks(c.lo, c.hi, c.n) {
			got = append(got, Num(v))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("Ticks(%v, %v, %d) = %q, want %q", c.lo, c.hi, c.n, got, c.want)
		}
	}
}

func TestScale(t *testing.T) {
	y := Scale{D0: -1, D1: 1, R0: 220, R1: 40}
	for v, want := range map[float64]string{-1: "220", 1: "40", 0: "130", 0.75: "62.5", 1 / 3.0: "100"} {
		if got := Num(y.At(v)); got != want {
			t.Errorf("At(%v) = %s, want %s", v, got, want)
		}
	}
	if got := (Scale{D0: 2, D1: 2, R0: 0, R1: 10}).At(7); got != 5 {
		t.Errorf("a domain of one value maps to %v, want the middle, 5", got)
	}
}

// TestWriterImports holds svg.go to no clock, file system, network, random source or host: a figure cannot hold the
// time it was drawn or where.
func TestWriterImports(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "svg.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		switch p {
		case "time", "os", "os/user", "os/exec", "io/fs", "path/filepath", "net", "math/rand", "math/rand/v2",
			"crypto/rand", "runtime", "syscall", "hash/maphash":
			t.Errorf("svg.go imports %s", p)
		}
	}
}

// TestNoHostOrPath scans the sample, light and dark, for the tree's deny and text shapes and for this host's
// paths and name (publicScan).
func TestNoHostOrPath(t *testing.T) {
	scan := publicScan(t)
	for _, theme := range []Theme{Light, Dark} {
		for _, hit := range scan(render(t, sample(theme, sources))) {
			t.Error(hit)
		}
	}
}

// publicScan reads the shapes the tree refuses, check/deny-shapes.txt and, where the tree holds it, the shapes
// check/text-shapes.txt turns on, with their excepts, and this checkout's path, the temporary directory, the home
// directory and the host's name, read here at run time. check/text-shapes.txt is private: the public tree scans with
// deny-shapes.txt's shapes alone. The three paths count on path boundaries (holdsPath), so HOME=/root does not match
// cells/rootfs/; the name counts anywhere. The function it returns names each line of a file that holds one, never
// what matched.
func publicScan(t *testing.T) func(data []byte) []string {
	t.Helper()
	type shape struct {
		id string
		re *regexp.Regexp
	}
	var shapes []shape
	var excepts []*regexp.Regexp
	least := 12 // check/deny-shapes.txt's shapes; 20 or more with check/text-shapes.txt's
	for _, file := range []string{"../check/deny-shapes.txt", "../check/text-shapes.txt"} {
		fh, err := os.Open(file)
		if errors.Is(err, fs.ErrNotExist) && strings.HasSuffix(file, "text-shapes.txt") {
			t.Logf("no check/text-shapes.txt in this tree: check/deny-shapes.txt's %d shapes alone", len(shapes))
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(file, "text-shapes.txt") {
			least = 20
		}
		sc := bufio.NewScanner(fh)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			id, expr, except := fmt.Sprintf("deny:%d", n), line, false
			if e, ok := strings.CutPrefix(line, "except "); ok && strings.HasSuffix(file, "deny-shapes.txt") {
				expr, except = e, true
			}
			if strings.HasSuffix(file, "text-shapes.txt") {
				f := strings.Split(line, "\t")
				switch {
				case len(f) == 2 && f[0] == "except":
					expr, except = f[1], true
				case len(f) == 2:
					id, expr = f[0], f[1]
				case len(f) == 3 && f[1] == "off":
					continue
				default:
					t.Fatalf("%s:%d: not a shape line", file, n)
				}
			}
			if except {
				excepts = append(excepts, regexp.MustCompilePOSIX("^("+expr+")$"))
				continue
			}
			re, err := regexp.CompilePOSIX(expr)
			if err != nil {
				t.Fatalf("%s:%d: %v", file, n, err)
			}
			shapes = append(shapes, shape{id, re})
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if len(shapes) < least {
		t.Fatalf("%d shapes read, under %d", len(shapes), least)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{root, filepath.Clean(os.TempDir())}
	if home, err := os.UserHomeDir(); err == nil && len(filepath.Clean(home)) > 1 {
		paths = append(paths, filepath.Clean(home))
	}
	var names []string
	if host, err := os.Hostname(); err == nil && len(host) >= 4 {
		names = append(names, host)
	}
	return func(data []byte) []string {
		var hits []string
		for i, line := range strings.Split(string(data), "\n") {
			for _, sh := range shapes {
				for _, m := range sh.re.FindAllString(line, -1) {
					if !slices.ContainsFunc(excepts, func(e *regexp.Regexp) bool { return e.MatchString(m) }) {
						hits = append(hits, fmt.Sprintf("line %d matches the shape %s", i+1, sh.id))
					}
				}
			}
			for _, p := range paths {
				if holdsPath(line, p) {
					hits = append(hits, fmt.Sprintf("line %d holds a path or name of this host", i+1))
				}
			}
			for _, n := range names {
				if holdsName(line, n) {
					hits = append(hits, fmt.Sprintf("line %d holds a path or name of this host", i+1))
				}
			}
		}
		return hits
	}
}

// holdsPath reports whether line holds the path p on path boundaries: no file name byte just before it, and none
// just after it, where a slash, a quote, a space or the line's end may stand. So a home of /root counts in
// "HOME=/root" and "cd /root", and not in "cells/rootfs/" or "a/root".
func holdsPath(line, p string) bool {
	for i := 0; i+len(p) <= len(line); {
		j := strings.Index(line[i:], p)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(p)
		if (j == 0 || !nameByte(line[j-1])) && (end == len(line) || !nameByte(line[end])) {
			return true
		}
		i = j + 1
	}
	return false
}

// holdsName reports whether line holds the name n as a word: no letter, digit or underscore just before it, and
// none just after it, where any other byte or the line's end may stand. So a host named lab counts in "lab:" and
// "on lab", and not in "label" or "a_lab".
func holdsName(line, n string) bool {
	for i := 0; i+len(n) <= len(line); {
		j := strings.Index(line[i:], n)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(n)
		if (j == 0 || !wordByte(line[j-1])) && (end == len(line) || !wordByte(line[end])) {
			return true
		}
		i = j + 1
	}
	return false
}

// wordByte reports whether c can stand in a word beside a name: a letter, a digit or '_'.
func wordByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
}

// nameByte reports whether c can stand in a file name beside a path: a letter, a digit, '.', '_' or '-'.
func nameByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '_' || c == '-'
}

// TestHoldsPath holds holdsPath to its boundaries: a home of /root is not in cells/rootfs/, and a real home path is
// found wherever it stands as a path. Its cases split such paths in two, so the tree's shapes pass this file.
func TestHoldsPath(t *testing.T) {
	for _, c := range []struct {
		line, p string
		want    bool
	}{
		{"source cells/rootfs/README.md", "/root", false},
		{"as cells/rootfs/snapshot.sh printed them", "/root", false},
		{"a/ro" + "ot/b", "/root", false},
		{"/rootfs", "/root", false},
		{"/root", "/root", true},
		{"HOME=/root", "/root", true},
		{"cd /ro" + "ot/x", "/root", true},
		{`href="/root"`, "/root", true},
		{"/ho" + "me/someone/x", "/ho" + "me/someone", true},
		{"file:///ho" + "me/someone/x", "/ho" + "me/someone", true},
		{"/ho" + "me/someone2/x", "/ho" + "me/someone", false},
		{"/ho" + "me/someone.d", "/ho" + "me/someone", false},
		{"", "/root", false},
	} {
		if got := holdsPath(c.line, c.p); got != c.want {
			t.Errorf("holdsPath(%q, %q) = %v, want %v", c.line, c.p, got, c.want)
		}
	}
}

// TestHoldsName holds holdsName to word boundaries: a name inside a longer word is not found, the bare name is, and
// so is the name at a line's start or end or beside punctuation.
func TestHoldsName(t *testing.T) {
	for _, c := range []struct {
		line, n string
		want    bool
	}{
		{"labhost", "labhost", true},
		{"labhost ran it", "labhost", true},
		{"it ran on labhost", "labhost", true},
		{"on labhost.", "labhost", true},
		{"host=labhost-2", "labhost", true},
		{`"labhost"`, "labhost", true},
		{"mylabhosts", "labhost", false},
		{"labhosts", "labhost", false},
		{"mylabhost", "labhost", false},
		{"labhost2", "labhost", false},
		{"a_labhost", "labhost", false},
		{"labhosts and labhost", "labhost", true},
		{"", "labhost", false},
	} {
		if got := holdsName(c.line, c.n); got != c.want {
			t.Errorf("holdsName(%q, %q) = %v, want %v", c.line, c.n, got, c.want)
		}
	}
}
