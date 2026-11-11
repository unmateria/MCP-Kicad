package pcbgen

import (
	"fmt"
	"math"
	"strconv"

	"mcp-kicad/internal/sexp"
)

// Box is an axis-aligned rectangle in millimetres.
type Box struct{ X1, Y1, X2, Y2 float64 }

func (b Box) W() float64 { return b.X2 - b.X1 }
func (b Box) H() float64 { return b.Y2 - b.Y1 }

func (b Box) shift(dx, dy float64) Box { return Box{b.X1 + dx, b.Y1 + dy, b.X2 + dx, b.Y2 + dy} }

func (b Box) overlaps(o Box, gap float64) bool {
	return b.X1 < o.X2+gap && o.X1 < b.X2+gap && b.Y1 < o.Y2+gap && o.Y1 < b.Y2+gap
}

func (b Box) union(o Box) Box {
	return Box{math.Min(b.X1, o.X1), math.Min(b.Y1, o.Y1), math.Max(b.X2, o.X2), math.Max(b.Y2, o.Y2)}
}

// Footprint is a library footprint ready to be instantiated: its text (so
// every instance is an independent parse) and the geometry placement needs.
type Footprint struct {
	LibID string
	text  string
	// Courtyard is the F.CrtYd outline's bounding box relative to the
	// footprint origin — the area KiCad's DRC forbids other parts to enter.
	// Footprints without one fall back to pads + graphics + 0.25 mm.
	Courtyard Box
	// Pads maps pad number to its position relative to the origin.
	Pads map[string][2]float64
	// PadShapes is every copper pad and hole, in file order, for the router.
	PadShapes []PadShape
	// refText is where the Reference prints on the silkscreen, nil when it
	// does not. It sits outside the courtyard on most footprints, so spacing
	// courtyards alone stacks each part's name onto its neighbour's body.
	refText *textAnchor
}

// PadShape is one pad as the router sees it, in the footprint's frame.
type PadShape struct {
	Number      string
	X, Y        float64
	W, H        float64
	Angle       float64 // the pad's own rotation inside the footprint
	Round       bool    // circle pad: W is the diameter
	Front, Back bool    // copper on F.Cu / B.Cu
	Drill       float64 // 0 for SMD
	NPTH        bool    // a bare hole, no copper
}

type textAnchor struct {
	x, y, angle float64
	w, h, thick float64 // font size and stroke thickness
}

// refBox approximates the silkscreen box of ref printed at the anchor. KiCad's
// stroke font advances slightly less than one font width per character; one
// full width per character plus the stroke keeps the estimate on the safe side.
func (t *textAnchor) refBox(ref string) Box {
	hw := (float64(len(ref))*t.w + t.thick) / 2
	hh := (t.h + t.thick) / 2
	if int(math.Round(t.angle))%180 != 0 {
		hw, hh = hh, hw
	}
	return Box{t.x - hw, t.y - hh, t.x + hw, t.y + hh}
}

// Keepout is the area a part claims on the board: its courtyard and, when it
// prints one, the box of its reference text.
func (f *Footprint) Keepout(ref string) Box {
	if f.refText == nil {
		return f.Courtyard
	}
	return f.Courtyard.union(f.refText.refBox(ref))
}

// LoadFootprint parses a .kicad_mod and measures it.
func LoadFootprint(libID, text string) (*Footprint, error) {
	root, err := parseFootprint(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", libID, err)
	}
	f := &Footprint{LibID: libID, text: text, Pads: map[string][2]float64{}}
	var crt, all Box
	haveCrt, haveAll := false, false
	add := func(b *Box, have *bool, x, y float64) {
		if !*have {
			*b, *have = Box{x, y, x, y}, true
			return
		}
		*b = b.union(Box{x, y, x, y})
	}
	for _, c := range root.Children {
		switch c.Head() {
		case "pad":
			x, y := xy(sexp.FindList(c, "at"))
			w, h := xy(sexp.FindList(c, "size"))
			if num := sexp.StringValue(c, 1); num != "" {
				if _, seen := f.Pads[num]; !seen {
					f.Pads[num] = [2]float64{x, y}
				}
			}
			f.PadShapes = append(f.PadShapes, padShape(c))
			// A pad's size is given in its own frame; the larger side on both
			// axes covers any pad rotation.
			r := math.Max(w, h) / 2
			add(&all, &haveAll, x-r, y-r)
			add(&all, &haveAll, x+r, y+r)
		case "property":
			if sexp.StringValue(c, 1) == "Reference" {
				f.refText = silkText(c)
			}
		case "fp_line", "fp_rect", "fp_poly", "fp_circle", "fp_arc":
			pts := shapePoints(c)
			onCrt := sexp.StringValue(sexp.FindList(c, "layer"), 1) == "F.CrtYd"
			for _, p := range pts {
				if onCrt {
					add(&crt, &haveCrt, p[0], p[1])
				}
				add(&all, &haveAll, p[0], p[1])
			}
		}
	}
	switch {
	case haveCrt:
		f.Courtyard = crt
	case haveAll:
		f.Courtyard = Box{all.X1 - 0.25, all.Y1 - 0.25, all.X2 + 0.25, all.Y2 + 0.25}
	default:
		return nil, fmt.Errorf("%s: footprint has neither pads nor graphics", libID)
	}
	return f, nil
}

// shapePoints returns points bounding a graphic item. Circles contribute their
// bounding square; arcs their three defining points, which can under-cover a
// bulge by a fraction of a millimetre — courtyards are drawn with lines and
// rectangles in every KiCad library footprint measured, so this never matters
// for placement.
func shapePoints(c *sexp.Node) [][2]float64 {
	if c.Head() == "fp_circle" {
		cx, cy := xy(sexp.FindList(c, "center"))
		ex, ey := xy(sexp.FindList(c, "end"))
		r := math.Hypot(ex-cx, ey-cy)
		return [][2]float64{{cx - r, cy - r}, {cx + r, cy + r}}
	}
	var pts [][2]float64
	for _, k := range []string{"start", "mid", "end"} {
		if l := sexp.FindList(c, k); l != nil {
			x, y := xy(l)
			pts = append(pts, [2]float64{x, y})
		}
	}
	if pl := sexp.FindList(c, "pts"); pl != nil {
		for _, p := range sexp.FindAllLists(pl, "xy") {
			x, y := xy(p)
			pts = append(pts, [2]float64{x, y})
		}
	}
	return pts
}

// Instantiate returns the board footprint node for one part at (x, y),
// turned rot degrees. What changes against the library file was measured on the
// boards KiCad 10 ships (demos/pic_programmer):
//   - the head names the library: (footprint "Lib:Name" …)
//   - version/generator lines go; (uuid) and (at) follow (layer)
//   - Reference and Value carry the part's
//   - (path) (sheetname) (sheetfile) link it to its schematic symbol
//   - each pad carries its net BY NAME — KiCad 10 has no net table
//   - a rotated footprint adds its angle to every pad's and text's own angle;
//     positions stay in the footprint's frame
func (f *Footprint) Instantiate(c Component, x, y float64, rot int, pads map[string]PadNet) (*sexp.Node, error) {
	root, err := parseFootprint(f.text)
	if err != nil {
		return nil, err
	}
	root.Children[1] = sexp.Str(f.LibID)

	var kept []*sexp.Node
	for _, ch := range root.Children {
		switch ch.Head() {
		case "version", "generator", "generator_version", "uuid", "at", "path", "sheetname", "sheetfile":
			continue
		}
		kept = append(kept, ch)
	}
	root.Children = kept

	insertAfter(root, "layer",
		sexp.List(sexp.Atom("uuid"), sexp.Str(sexp.NewUUID())),
		atNode(x, y, rot),
	)
	sheetName := c.SheetName
	if sheetName == "" {
		sheetName = "/"
	}
	link := []*sexp.Node{
		sexp.List(sexp.Atom("path"), sexp.Str(c.Path)),
		sexp.List(sexp.Atom("sheetname"), sexp.Str(sheetName)),
		sexp.List(sexp.Atom("sheetfile"), sexp.Str(c.SheetFile)),
	}
	insertAfterLast(root, "property", link...)

	for _, ch := range root.Children {
		switch ch.Head() {
		case "property", "fp_text", "pad":
			if rot != 0 {
				addAngle(sexp.FindList(ch, "at"), rot)
			}
		}
		switch ch.Head() {
		case "property":
			switch sexp.StringValue(ch, 1) {
			case "Reference":
				ch.Children[2] = sexp.Str(c.Ref)
			case "Value":
				ch.Children[2] = sexp.Str(c.Value)
			}
		case "pad":
			pn, ok := pads[c.Ref+"\x00"+sexp.StringValue(ch, 1)]
			if !ok {
				continue
			}
			extra := []*sexp.Node{sexp.List(sexp.Atom("net"), sexp.Str(pn.Net))}
			if pn.Function != "" {
				extra = append(extra, sexp.List(sexp.Atom("pinfunction"), sexp.Str(pn.Function)))
			}
			if pn.Type != "" {
				extra = append(extra, sexp.List(sexp.Atom("pintype"), sexp.Str(pn.Type)))
			}
			ch.Children = append(ch.Children, extra...)
		}
	}
	return root, nil
}

func parseFootprint(text string) (*sexp.Node, error) {
	nodes, err := sexp.Parse(text)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 || (nodes[0].Head() != "footprint" && nodes[0].Head() != "module") || len(nodes[0].Children) < 2 {
		return nil, fmt.Errorf("not a footprint")
	}
	nodes[0].Children[0] = sexp.Atom("footprint")
	return nodes[0], nil
}

func insertAfter(parent *sexp.Node, head string, nodes ...*sexp.Node) {
	at := 2
	for i, c := range parent.Children {
		if c.Head() == head {
			at = i + 1
			break
		}
	}
	spliceAt(parent, at, nodes)
}

func insertAfterLast(parent *sexp.Node, head string, nodes ...*sexp.Node) {
	at := 2
	for i, c := range parent.Children {
		if c.Head() == head {
			at = i + 1
		}
	}
	spliceAt(parent, at, nodes)
}

func spliceAt(parent *sexp.Node, at int, nodes []*sexp.Node) {
	if at > len(parent.Children) {
		at = len(parent.Children)
	}
	out := make([]*sexp.Node, 0, len(parent.Children)+len(nodes))
	out = append(out, parent.Children[:at]...)
	out = append(out, nodes...)
	out = append(out, parent.Children[at:]...)
	parent.Children = out
}

func xy(l *sexp.Node) (float64, float64) {
	if l == nil || len(l.Children) < 3 {
		return 0, 0
	}
	x, _ := strconv.ParseFloat(l.Children[1].Value, 64)
	y, _ := strconv.ParseFloat(l.Children[2].Value, 64)
	return x, y
}

func num(v float64) *sexp.Node {
	return sexp.Atom(strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64))
}

// silkText reads a text item's anchor and font, or nil when it is hidden or
// not on the front silkscreen.
func silkText(c *sexp.Node) *textAnchor {
	if sexp.StringValue(sexp.FindList(c, "layer"), 1) != "F.SilkS" {
		return nil
	}
	if h := sexp.FindList(c, "hide"); h != nil && sexp.AtomValue(h, 1) != "no" {
		return nil
	}
	t := &textAnchor{w: 1, h: 1, thick: 0.15}
	if at := sexp.FindList(c, "at"); at != nil {
		t.x, t.y = xy(at)
		if len(at.Children) > 3 {
			t.angle, _ = strconv.ParseFloat(at.Children[3].Value, 64)
		}
	}
	if eff := sexp.FindList(c, "effects"); eff != nil {
		if h := sexp.FindList(eff, "hide"); h != nil && sexp.AtomValue(h, 1) != "no" {
			return nil
		}
		if font := sexp.FindList(eff, "font"); font != nil {
			if sz := sexp.FindList(font, "size"); sz != nil {
				t.h, t.w = xy(sz)
			}
			if th := sexp.FindList(font, "thickness"); th != nil && len(th.Children) > 1 {
				t.thick, _ = strconv.ParseFloat(th.Children[1].Value, 64)
			}
		}
	}
	return t
}

func atNode(x, y float64, rot int) *sexp.Node {
	if rot == 0 {
		return sexp.List(sexp.Atom("at"), num(x), num(y))
	}
	return sexp.List(sexp.Atom("at"), num(x), num(y), sexp.Atom(strconv.Itoa(rot)))
}

// addAngle turns an (at x y [angle]) node by rot degrees.
func addAngle(at *sexp.Node, rot int) {
	if at == nil || len(at.Children) < 3 {
		return
	}
	a := 0.0
	if len(at.Children) > 3 {
		a, _ = strconv.ParseFloat(at.Children[3].Value, 64)
	}
	a = math.Mod(a+float64(rot), 360)
	if len(at.Children) > 3 {
		at.Children[3] = num(a)
	} else {
		at.Children = append(at.Children, num(a))
	}
}

func padShape(c *sexp.Node) PadShape {
	ps := PadShape{Number: sexp.StringValue(c, 1)}
	ps.X, ps.Y = xy(sexp.FindList(c, "at"))
	if at := sexp.FindList(c, "at"); at != nil && len(at.Children) > 3 {
		ps.Angle, _ = strconv.ParseFloat(at.Children[3].Value, 64)
	}
	ps.W, ps.H = xy(sexp.FindList(c, "size"))
	if len(c.Children) > 3 {
		ps.NPTH = c.Children[2].Value == "np_thru_hole"
		ps.Round = c.Children[3].Value == "circle"
	}
	if d := sexp.FindList(c, "drill"); d != nil {
		for _, ch := range d.Children[1:] {
			if v, err := strconv.ParseFloat(ch.Value, 64); err == nil {
				ps.Drill = math.Max(ps.Drill, v) // oval drills list two sizes
			}
		}
	}
	if l := sexp.FindList(c, "layers"); l != nil {
		for i := 1; i < len(l.Children); i++ {
			switch sexp.StringValue(l, i) {
			case "*.Cu", "F&B.Cu":
				ps.Front, ps.Back = true, true
			case "F.Cu":
				ps.Front = true
			case "B.Cu":
				ps.Back = true
			}
		}
	}
	return ps
}
