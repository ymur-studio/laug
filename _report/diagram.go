package report

// diagram.go draws a diagram, a lesson's or a page's, from its spec, docs/img/src/<name>.txt: boxes, regions, arrows,
// links, paths and words placed by hand, through the SVG writer on the light ground, and data drawn by Go into the
// rectangles the spec names as panels (lessonpanels.go). A spec says where each fact comes from: its source lines name
// the committed files, its row lines the rows of docs/tables a fact rests on when its record is not here, and its
// comments the lines in them.
//
// A spec is a directive a line; a line starting with # is a comment and a blank line nothing. A line's fields are
// split at spaces: the first is the directive, a field key=value an option, a bare word of the directive's flags a
// flag, the rest its places. After a field "|", the text, each further "|" starting another line of it; a line that
// starts with "|", indented or not, continues the directive above it with more. A box's or a text's lines are drawn
// as lines; a title's, sub's, desc's or label's join with a space into one.
//
//	name <name>                                   first: the file's name less .txt
//	size <width> <height>                         second
//	title | <words>                               drawn at the top, and the figure's title
//	sub | <words>                                 drawn under it
//	desc | <words>                                how to read it; several lines join with a space
//	source <path>                                 a committed file the facts come from, one a line
//	row <table> <key>                             a row of docs/tables/<table>.jsonl a fact rests on, one a line
//	box <id> <x> <y> <w> <h> [fill=] [stroke=] [dash=] [mono] | <line> | ...
//	region <x> <y> <w> <h> [stroke=] [at=x,y] | <label>  a dashed outline, its label inside at the top or at x,y
//	arrow <from> <to> [colour=] [dash=] [at=x,y] [| <label>]
//	link <from> <to> [colour=] [dash=] [at=x,y] [| <label>]
//	path <x>,<y> <x>,<y> ... [colour=] [dash=] [head] [at=x,y] [| <label>]
//	text <x> <y> [size=] [colour=] [anchor=start|middle|end] [bold] [mono] | <line> | ...
//	panel <name> <x> <y> <w> <h> [arg=<word>]
//
// Colours are docs/BRAND.md's token names. A box's first line is bold when it has more than one, and mono sets the
// rest in Mono. An arrow or link joins two boxes at their facing sides, straight when they overlap across or down. A
// spec that breaks a rule is refused with its line: an unknown directive, option or flag; a box id twice or an
// arrow's box missing; a place outside the figure; a box's words wider or taller than the box. A spec is drawn only
// when every row it names is in its table; the table's files join its sources.

import (
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// specOp is one directive of a spec, as read.
type specOp struct {
	line   int
	verb   string
	places []string
	opt    map[string]string
	flags  []string
	text   []string
}

// specBox is a box's rectangle.
type specBox struct{ x, y, w, h float64 }

// spec is a diagram's spec, read and checked.
type spec struct {
	file, name, title, sub string
	desc, sources          []string
	rows                   []specOp // the row lines, in order
	w, h                   float64
	boxes                  map[string]specBox // looked up by id; never ranged
	ops                    []specOp
}

var (
	specID    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	specPoint = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?,-?[0-9]+(\.[0-9]+)?$`)
	// specVerbs are each directive's places, options and flags; -1 places is two or more points.
	specVerbs = map[string]struct {
		places int
		opts   []string
		flags  []string
		text   int // 0 none, 1 one line, 2 one or more lines, 3 none or one line; for 1 and 3 the lines join
	}{
		"name":   {1, nil, nil, 0},
		"size":   {2, nil, nil, 0},
		"title":  {0, nil, nil, 1},
		"sub":    {0, nil, nil, 1},
		"desc":   {0, nil, nil, 1},
		"source": {1, nil, nil, 0},
		"row":    {2, nil, nil, 0},
		"box":    {5, []string{"fill", "stroke", "dash"}, []string{"mono"}, 2},
		"region": {4, []string{"stroke", "at"}, nil, 1},
		"arrow":  {2, []string{"colour", "dash", "at"}, nil, 3},
		"link":   {2, []string{"colour", "dash", "at"}, nil, 3},
		"path":   {-1, []string{"colour", "dash", "at"}, []string{"head"}, 3},
		"text":   {2, []string{"size", "colour", "anchor"}, []string{"bold", "mono"}, 2},
		"panel":  {5, []string{"arg"}, nil, 0},
	}
)

// readSpec reads and checks the spec at p, a path under fsys.
func readSpec(fsys fs.FS, p string) (*spec, error) {
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	sp := &spec{file: p, boxes: map[string]specBox{}}
	bad := func(n int, format string, a ...any) error {
		return fmt.Errorf("%s:%d: %s", p, n, fmt.Sprintf(format, a...))
	}
	// A directive is its line and the lines of text that continue it.
	type directive struct {
		n          int
		head, tail string
		hasText    bool
		more       []string
	}
	var ds []directive
	for n, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		n++
		if strings.ContainsRune(line, '\r') || strings.ContainsRune(line, '\t') {
			return nil, bad(n, "a carriage return or a tab")
		}
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(t, "|"); ok {
			if len(ds) == 0 {
				return nil, bad(n, "a line of text with no directive before it")
			}
			ds[len(ds)-1].more = append(ds[len(ds)-1].more, rest)
			continue
		}
		head, tail, hasText := strings.Cut(line, "|")
		ds = append(ds, directive{n, head, tail, hasText, nil})
	}
	for _, d := range ds {
		n, head, tail, hasText := d.n, d.head, d.tail, d.hasText
		f := strings.Fields(head)
		if len(f) == 0 {
			return nil, bad(n, "text with no directive")
		}
		op := specOp{line: n, verb: f[0], opt: map[string]string{}}
		rule, ok := specVerbs[op.verb]
		if !ok {
			return nil, bad(n, "no directive %q", op.verb)
		}
		for _, w := range f[1:] {
			if k, v, ok := strings.Cut(w, "="); ok {
				if !slices.Contains(rule.opts, k) || v == "" {
					return nil, bad(n, "%s takes no option %q", op.verb, w)
				}
				op.opt[k] = v
				continue
			}
			if slices.Contains(rule.flags, w) {
				op.flags = append(op.flags, w)
				continue
			}
			op.places = append(op.places, w)
		}
		var segs []string
		if hasText {
			segs = append(segs, strings.Split(tail, "|")...)
		}
		for _, m := range d.more {
			segs = append(segs, strings.Split(m, "|")...)
		}
		for _, t := range segs {
			t = strings.TrimSpace(t)
			if t == "" {
				return nil, bad(n, "an empty line of text")
			}
			op.text = append(op.text, t)
		}
		if (rule.text == 1 || rule.text == 3) && len(op.text) > 1 {
			op.text = []string{strings.Join(op.text, " ")} // one line of words, written over several
		}
		switch {
		case rule.places >= 0 && len(op.places) != rule.places:
			return nil, bad(n, "%s takes %d places, got %d", op.verb, rule.places, len(op.places))
		case rule.places < 0 && len(op.places) < 2:
			return nil, bad(n, "%s takes two points or more", op.verb)
		case rule.text == 0 && len(op.text) > 0, rule.text == 1 && len(op.text) != 1,
			rule.text == 2 && len(op.text) == 0:
			return nil, bad(n, "%s: %d lines of text", op.verb, len(op.text))
		}
		switch {
		case op.verb == "name":
			if sp.name != "" || len(sp.ops) > 0 || op.places[0] != strings.TrimSuffix(path.Base(p), ".txt") {
				return nil, bad(n, "name comes first, once, and is the file's name")
			}
			sp.name = op.places[0]
			continue
		case sp.name == "":
			return nil, bad(n, "name comes first")
		case op.verb == "size":
			if sp.w != 0 {
				return nil, bad(n, "a second size")
			}
			w, err1 := strconv.ParseFloat(op.places[0], 64)
			h, err2 := strconv.ParseFloat(op.places[1], 64)
			if err1 != nil || err2 != nil || w != lessonW || !(h > 0 && h <= 2000) {
				return nil, bad(n, "size is %v by a height from 1 to 2000", lessonW)
			}
			sp.w, sp.h = w, h
			continue
		case sp.w == 0:
			return nil, bad(n, "size comes second")
		case op.verb == "title":
			sp.title = op.text[0]
			continue
		case op.verb == "sub":
			sp.sub = op.text[0]
			continue
		case op.verb == "desc":
			sp.desc = append(sp.desc, op.text[0])
			continue
		case op.verb == "source":
			sp.sources = append(sp.sources, op.places[0])
			continue
		case op.verb == "row":
			sp.rows = append(sp.rows, op)
			continue
		}
		if err := sp.check(op); err != nil {
			return nil, bad(n, "%v", err)
		}
		sp.ops = append(sp.ops, op)
	}
	switch {
	case sp.title == "" || len(sp.desc) == 0:
		return nil, fmt.Errorf("%s: a title and a desc", p)
	case len(sp.sources) == 0 && len(sp.rows) == 0:
		return nil, fmt.Errorf("%s: no source", p)
	}
	for _, op := range sp.ops {
		if op.verb == "arrow" || op.verb == "link" {
			a, aok := sp.boxes[op.places[0]]
			b, bok := sp.boxes[op.places[1]]
			if !aok || !bok {
				return nil, bad(op.line, "%s joins %s and %s, and a box is missing", op.verb, op.places[0],
					op.places[1])
			}
			if _, _, _, _, ok := joinBoxes(a, b); !ok {
				return nil, bad(op.line, "%s and %s overlap", op.places[0], op.places[1])
			}
		}
	}
	return sp, nil
}

// num reads place i of op as a number.
func (op specOp) num(i int) (float64, error) {
	v, err := strconv.ParseFloat(op.places[i], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q is not a number", op.places[i])
	}
	return v, nil
}

// nums reads places from..from+3 as a rectangle.
func (op specOp) rect(from int) (specBox, error) {
	var v [4]float64
	for i := range v {
		f, err := op.num(from + i)
		if err != nil {
			return specBox{}, err
		}
		v[i] = f
	}
	return specBox{v[0], v[1], v[2], v[3]}, nil
}

// colourNamed is the token a spec names, None for a name that is none of the eight.
func colourNamed(name string) Colour {
	for _, c := range Colours() {
		if c.Name() == name {
			return c
		}
	}
	return None
}

// colourOpt is op's option k as a token, def when unset.
func (op specOp) colour(k string, def Colour) (Colour, error) {
	v, ok := op.opt[k]
	if !ok {
		return def, nil
	}
	if c := colourNamed(v); c != None {
		return c, nil
	}
	return None, fmt.Errorf("%s=%s is none of docs/BRAND.md's eight colours", k, v)
}

// dash is op's dash option, "a,b", nil when unset.
func (op specOp) dash() ([]float64, error) {
	v, ok := op.opt["dash"]
	if !ok {
		return nil, nil
	}
	var d []float64
	for _, f := range strings.Split(v, ",") {
		x, err := strconv.ParseFloat(f, 64)
		if err != nil || !(x > 0) {
			return nil, fmt.Errorf("dash=%s is not lengths over 0", v)
		}
		d = append(d, x)
	}
	return d, nil
}

// point reads "x,y".
func point(s string) (Point, error) {
	if !specPoint.MatchString(s) {
		return Point{}, fmt.Errorf("%q is not a point x,y", s)
	}
	xs, ys, _ := strings.Cut(s, ",")
	x, _ := strconv.ParseFloat(xs, 64)
	y, _ := strconv.ParseFloat(ys, 64)
	return Point{x, y}, nil
}

func (sp *spec) inside(x, y float64) bool { return x >= 0 && y >= 0 && x <= sp.w && y <= sp.h }

// boxLine is how a box's line i of n is set.
func boxLine(op specOp, i, n int) Font {
	return Font{Size: 12, Anchor: AnchorMiddle, Bold: i == 0 && n > 1, Mono: i > 0 && slices.Contains(op.flags, "mono")}
}

// check refuses op when it breaks a rule, and records a box.
func (sp *spec) check(op specOp) error {
	for _, k := range []string{"fill", "stroke", "colour"} {
		if _, err := op.colour(k, None); err != nil {
			return err
		}
	}
	if _, err := op.dash(); err != nil {
		return err
	}
	if a, ok := op.opt["at"]; ok {
		p, err := point(a)
		if err != nil {
			return err
		}
		if !sp.inside(p.X, p.Y) {
			return fmt.Errorf("at=%s is outside the figure", a)
		}
	}
	switch op.verb {
	case "box", "region", "panel":
		from := 1
		if op.verb == "region" {
			from = 0
		}
		r, err := op.rect(from)
		if err != nil {
			return err
		}
		if !(r.w > 0 && r.h > 0) || !sp.inside(r.x, r.y) || !sp.inside(r.x+r.w, r.y+r.h) {
			return fmt.Errorf("the rectangle %v is not inside the figure", r)
		}
		switch op.verb {
		case "box":
			id := op.places[0]
			if !specID.MatchString(id) {
				return fmt.Errorf("the box id %q is not lower case words with hyphens", id)
			}
			if _, ok := sp.boxes[id]; ok {
				return fmt.Errorf("a second box %s", id)
			}
			sp.boxes[id] = r
			if float64(float64(len(op.text))*15)+6 > r.h {
				return fmt.Errorf("%d lines do not fit a box %v high", len(op.text), r.h)
			}
			for i, l := range op.text {
				f := boxLine(op, i, len(op.text))
				if textW(l, f.Size, f.Bold || f.Mono)+10 > r.w {
					return fmt.Errorf("%q is wider than its box, %v", l, r.w)
				}
			}
		case "region":
			if textW(op.text[0], 11, true)+16 > r.w {
				return fmt.Errorf("the label %q is wider than its region", op.text[0])
			}
		case "panel":
			if _, ok := lessonPanel(op.places[0]); !ok {
				return fmt.Errorf("no panel %q", op.places[0])
			}
		}
	case "path":
		for _, pt := range op.places {
			p, err := point(pt)
			if err != nil {
				return err
			}
			if !sp.inside(p.X, p.Y) {
				return fmt.Errorf("the point %s is outside the figure", pt)
			}
		}
		if len(op.text) > 0 && op.opt["at"] == "" {
			return fmt.Errorf("a path's label needs at=x,y")
		}
	case "text":
		x, err := op.num(0)
		if err != nil {
			return err
		}
		y, err := op.num(1)
		if err != nil {
			return err
		}
		f, err := op.font()
		if err != nil {
			return err
		}
		for i, l := range op.text {
			w := textW(l, f.Size, f.Bold || f.Mono)
			x0 := x
			switch f.Anchor {
			case AnchorMiddle:
				x0 = x - w/2
			case AnchorEnd:
				x0 = x - w
			}
			ly := y + float64(float64(i)*float64(f.Size+3))
			if !sp.inside(x0, ly-f.Size) || !sp.inside(x0+w, ly+3) {
				return fmt.Errorf("the line %q runs outside the figure", l)
			}
		}
	}
	return nil
}

// font is a text directive's font.
func (op specOp) font() (Font, error) {
	f := Font{Size: 11, Bold: slices.Contains(op.flags, "bold"), Mono: slices.Contains(op.flags, "mono")}
	if v, ok := op.opt["size"]; ok {
		s, err := strconv.ParseFloat(v, 64)
		if err != nil || !(s >= 8 && s <= 16) {
			return Font{}, fmt.Errorf("size=%s is not 8 to 16", v)
		}
		f.Size = s
	}
	c, err := op.colour("colour", None)
	if err != nil {
		return Font{}, err
	}
	f.Colour = c
	switch op.opt["anchor"] {
	case "", "start":
	case "middle":
		f.Anchor = AnchorMiddle
	case "end":
		f.Anchor = AnchorEnd
	default:
		return Font{}, fmt.Errorf("anchor=%s is none of start, middle and end", op.opt["anchor"])
	}
	return f, nil
}

// textOn is the text colour on a fill: steam on the dark tokens and ember, basalt on the mid ones, deep on the light.
func textOn(fill Colour) Colour {
	switch fill {
	case Deep, Basalt, Ember:
		return Steam
	case Moss, Water, Warm:
		return Basalt
	}
	return Deep
}

// joinBoxes is where a line from a to b leaves a and meets b: down or up from the middle of their shared columns when
// they share 8 or more, across from the middle of their shared rows when they share those, else between their
// centres, cut at each box's edge. ok is false when the boxes overlap.
func joinBoxes(a, b specBox) (x1, y1, x2, y2 float64, ok bool) {
	ox0, ox1 := max(a.x, b.x), min(a.x+a.w, b.x+b.w)
	oy0, oy1 := max(a.y, b.y), min(a.y+a.h, b.y+b.h)
	if ox1 > ox0 && oy1 > oy0 {
		return 0, 0, 0, 0, false
	}
	switch {
	case ox1-ox0 >= 8:
		x := (ox0 + ox1) / 2
		if b.y >= a.y+a.h {
			return x, a.y + a.h, x, b.y, true
		}
		return x, a.y, x, b.y + b.h, true
	case oy1-oy0 >= 8:
		y := (oy0 + oy1) / 2
		if b.x >= a.x+a.w {
			return a.x + a.w, y, b.x, y, true
		}
		return a.x, y, b.x + b.w, y, true
	}
	ax, ay, bx, by := a.x+a.w/2, a.y+a.h/2, b.x+b.w/2, b.y+b.h/2
	dx, dy := bx-ax, by-ay
	cut := func(w, h float64) float64 { // the share of the centre line inside a box of w by h about its start
		t := math.Inf(1)
		if dx != 0 {
			t = math.Min(t, w/2/math.Abs(dx))
		}
		if dy != 0 {
			t = math.Min(t, h/2/math.Abs(dy))
		}
		return t
	}
	ta, tb := cut(a.w, a.h), cut(b.w, b.h)
	return ax + float64(dx*ta), ay + float64(dy*ta), bx - float64(dx*tb), by - float64(dy*tb), true
}

// arrowHead draws a line from (x1, y1) to (x2, y2), dashed when dash says so, its head a filled triangle 8 long and 8
// wide at the end when head is true.
func arrowHead(s *SVG, x1, y1, x2, y2 float64, c Colour, dash []float64, head bool) {
	if !head {
		s.Line(x1, y1, x2, y2, Style{Stroke: c, Width: 1.5, Dash: dash})
		return
	}
	dx, dy := x2-x1, y2-y1
	n := math.Sqrt(float64(dx*dx) + float64(dy*dy))
	if !(n > 8) {
		s.fail("an arrow %v long: its head alone is 8", n)
		return
	}
	ux, uy := dx/n, dy/n
	bx, by := x2-float64(8*ux), y2-float64(8*uy)
	s.Line(x1, y1, bx, by, Style{Stroke: c, Width: 1.5, Dash: dash})
	s.Polygon([]Point{{x2, y2}, {bx - float64(4*uy), by + float64(4*ux)}, {bx + float64(4*uy), by - float64(4*ux)}},
		Style{Fill: c})
}

// diagram draws the spec at p from fsys, the checkout's root.
func diagram(fsys fs.FS, p string) (drawn, error) {
	sp, err := readSpec(fsys, p)
	if err != nil {
		return drawn{}, err
	}
	sources := append([]string{p}, sp.sources...)
	read := map[string]figTable{} // by name; looked up, never ranged
	for _, op := range sp.rows {
		name, key := op.places[0], op.places[1]
		t, ok := read[name]
		if !ok {
			if t, err = readFigTable(fsys, name); err != nil {
				return drawn{}, fmt.Errorf("%s:%d: row %s: %w", p, op.line, name, err)
			}
			read[name] = t
			sources = append(sources, t.files...)
		}
		if _, err := t.row(key); err != nil {
			return drawn{}, fmt.Errorf("%s:%d: %w", p, op.line, err)
		}
	}
	type ready struct {
		draw func(s *SVG, x, y, w, h float64)
	}
	drawers := map[int]ready{} // by the op's line; looked up, never ranged
	for _, op := range sp.ops {
		if op.verb != "panel" {
			continue
		}
		f, _ := lessonPanel(op.places[0])
		srcs, draw, err := f(fsys, op.opt["arg"])
		if err != nil {
			return drawn{}, fmt.Errorf("%s:%d: panel %s: %w", p, op.line, op.places[0], err)
		}
		sources = append(sources, srcs...)
		drawers[op.line] = ready{draw}
	}
	s := lessonFigure(sp.name, sp.title, sp.sub, strings.Join(sp.desc, " "), sources, sp.h)
	for _, op := range sp.ops {
		dash, _ := op.dash()
		switch op.verb {
		case "box":
			r := sp.boxes[op.places[0]]
			fill, _ := op.colour("fill", Silica)
			line, _ := op.colour("stroke", Deep)
			s.Group(strings.Join(op.text, ", "))
			s.Rect(r.x, r.y, r.w, r.h, Style{Fill: fill, Stroke: line, Dash: dash})
			n := len(op.text)
			y0 := r.y + r.h/2 - float64(float64(n-1)*15)/2 + 4
			for i, l := range op.text {
				f := boxLine(op, i, n)
				f.Colour = textOn(fill)
				s.Text(r.x+r.w/2, y0+float64(float64(i)*15), l, f)
			}
			s.EndGroup()
		case "region":
			r, _ := op.rect(0)
			c, _ := op.colour("stroke", Deep)
			s.Rect(r.x, r.y, r.w, r.h, Style{Stroke: c, Dash: []float64{6, 4}})
			lx, ly := r.x+8, r.y+16
			if a := op.opt["at"]; a != "" {
				pt, _ := point(a)
				lx, ly = pt.X, pt.Y
			}
			s.Text(lx, ly, op.text[0], Font{Size: 11, Bold: true, Colour: c})
		case "arrow", "link":
			x1, y1, x2, y2, _ := joinBoxes(sp.boxes[op.places[0]], sp.boxes[op.places[1]])
			c, _ := op.colour("colour", Deep)
			arrowHead(s, x1, y1, x2, y2, c, dash, op.verb == "arrow")
			if len(op.text) > 0 {
				lx, ly, a := (x1+x2)/2, (y1+y2)/2, AnchorMiddle
				switch {
				case op.opt["at"] != "":
					pt, _ := point(op.opt["at"])
					lx, ly, a = pt.X, pt.Y, AnchorStart
				case y1 == y2:
					ly -= 6
				default:
					lx, ly, a = lx+6, ly+4, AnchorStart
				}
				s.Text(lx, ly, op.text[0], Font{Size: 11, Anchor: a, Colour: labelOn(c)})
			}
		case "path":
			c, _ := op.colour("colour", Deep)
			var pts []Point
			for _, pt := range op.places {
				q, _ := point(pt)
				pts = append(pts, q)
			}
			head := slices.Contains(op.flags, "head")
			if len(pts) > 2 {
				s.Polyline(pts[:len(pts)-1], Style{Stroke: c, Width: 1.5, Dash: dash})
			}
			a, b := pts[len(pts)-2], pts[len(pts)-1]
			arrowHead(s, a.X, a.Y, b.X, b.Y, c, dash, head)
			if len(op.text) > 0 {
				pt, _ := point(op.opt["at"])
				s.Text(pt.X, pt.Y, op.text[0], Font{Size: 11, Colour: labelOn(c)})
			}
		case "text":
			x, _ := op.num(0)
			y, _ := op.num(1)
			f, _ := op.font()
			for i, l := range op.text {
				s.Text(x, y+float64(float64(i)*float64(f.Size+3)), l, f)
			}
		case "panel":
			r, _ := op.rect(1)
			drawers[op.line].draw(s, r.x, r.y, r.w, r.h)
		}
	}
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// labelOn is the colour of a line's label on the light ground: ember beside an ember line, else deep, since water and
// warm read too faint as text on steam.
func labelOn(c Colour) Colour {
	if c == Ember {
		return Ember
	}
	return Deep
}
