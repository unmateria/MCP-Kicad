package easyeda

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// convertSymbol turns an EasyEDA schematic document into a one-symbol
// .kicad_sym library.
func convertSymbol(c *Component, name string) ([]byte, []string, error) {
	f := frame{ox: c.DataStr.Head.X, oy: c.DataStr.Head.Y, flipY: true}

	var body strings.Builder // graphics, unit 0 (shared by all units)
	var pinsOut strings.Builder
	var notes []string
	skipped := map[string]int{}
	pinCount := 0

	for _, raw := range c.DataStr.Shape {
		r := parseRec(raw)
		switch r.kind {
		case "P":
			s, err := symbolPin(f, r)
			if err != nil {
				notes = append(notes, "pin skipped: "+err.Error())
				continue
			}
			pinsOut.WriteString(s)
			pinCount++
		case "R":
			body.WriteString(symbolRect(f, r))
		case "PL", "PG":
			body.WriteString(symbolPolyline(f, r, r.kind == "PG"))
		case "E":
			body.WriteString(symbolEllipse(f, r))
		case "PT", "A":
			body.WriteString(symbolPath(f, r))
		case "T":
			// Free text on a symbol is nearly always the part number or a
			// pin-group caption, both of which KiCad shows from properties.
			// Copying it would double the label.
			skipped[r.kind]++
		default:
			skipped[r.kind]++
		}
	}

	if pinCount == 0 {
		return nil, notes, fmt.Errorf("easyeda: %s has no pins", name)
	}
	if len(skipped) > 0 {
		notes = append(notes, "symbol graphics not translated: "+countSummary(skipped))
	}

	props := []property{
		{"Reference", refPrefix(c), 0, 5.08, false},
		{"Value", name, 0, -5.08, false},
		{"Footprint", "", 0, -7.62, true},
		{"Datasheet", c.Datasheet(), 0, -10.16, true},
		{"Description", c.Description, 0, -12.7, true},
	}
	if m := c.Manufacturer(); m != "" {
		props = append(props, property{"Manufacturer", m, 0, -15.24, true})
	}
	if c.LCSC.Number != "" {
		props = append(props, property{"LCSC", c.LCSC.Number, 0, -17.78, true})
	}

	var sb strings.Builder
	sb.WriteString("(kicad_symbol_lib\n\t(version 20241209)\n\t(generator \"mcp-kicad\")\n")
	fmt.Fprintf(&sb, "\t(symbol %s\n", quote(name))
	sb.WriteString("\t\t(exclude_from_sim no)\n\t\t(in_bom yes)\n\t\t(on_board yes)\n")
	for _, p := range props {
		sb.WriteString(p.render())
	}
	// KiCad splits a symbol into <name>_<unit>_<style>. Graphics go in style 1
	// of unit 0 so they show for every unit; pins go in unit 1.
	fmt.Fprintf(&sb, "\t\t(symbol %s\n%s\t\t)\n", quote(name+"_0_1"), body.String())
	fmt.Fprintf(&sb, "\t\t(symbol %s\n%s\t\t)\n", quote(name+"_1_1"), pinsOut.String())
	sb.WriteString("\t)\n)\n")
	return []byte(sb.String()), notes, nil
}

type property struct {
	name, value string
	x, y        float64
	hidden      bool
}

func (p property) render() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\t\t(property %s %s\n", quote(p.name), quote(p.value))
	fmt.Fprintf(&sb, "\t\t\t(at %s %s 0)\n", fmtNum(p.x), fmtNum(p.y))
	if p.hidden {
		sb.WriteString("\t\t\t(hide yes)\n")
	}
	sb.WriteString("\t\t\t(effects\n\t\t\t\t(font\n\t\t\t\t\t(size 1.27 1.27)\n\t\t\t\t)\n\t\t\t)\n\t\t)\n")
	return sb.String()
}

// refPrefix is the reference designator letter EasyEDA suggests ("U?" → "U").
func refPrefix(c *Component) string {
	pre := c.DataStr.Head.Param("pre")
	pre = strings.TrimSuffix(strings.TrimSpace(pre), "?")
	if pre == "" {
		return "U"
	}
	return pre
}

// symbolPin renders one EasyEDA pin record.
//
// The pin's connection point is the record's own x/y; its direction comes from
// the STEM PATH, not from the record's rotation field. EasyEDA writes rotation
// 180 for a pin whose stem runs to the right, so trusting that field mirrors
// every symbol — the path says what is actually drawn.
func symbolPin(f frame, r rec) (string, error) {
	number := strings.TrimSpace(r.at(3))
	if number == "" {
		return "", fmt.Errorf("pin with no number")
	}
	ex, ey := r.num(4), r.num(5)

	dx, dy, ok := pinStem(ex, ey, r.sub(2, 0))
	if !ok {
		return "", fmt.Errorf("pin %s has no stem path", number)
	}
	// KiCad's angle is the direction from the connection point towards the
	// body, and its Y axis is flipped relative to EasyEDA's.
	angle, err := snapAngle(dx, -dy)
	if err != nil {
		return "", fmt.Errorf("pin %s: %w", number, err)
	}
	length := f.length(math.Hypot(dx, dy))
	if length == 0 {
		length = 2.54
	}

	name := strings.TrimSpace(r.sub(3, 4))
	if name == "" {
		name = "~"
	}
	p := f.pt(ex, ey)

	// EasyEDA's electrical-type field is 0 ("unspecified") on virtually every
	// LCSC-contributed symbol. Translating that to `passive` would be quieter
	// and would be a claim the source never made; `unspecified` is what it
	// says, and the importer warns that ERC will ask about it.
	elec := electricalType(r.at(2))
	shape := "line"
	if r.sub(5, 0) == "1" { // inverted dot
		shape = "inverted"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "\t\t\t(pin %s %s\n", elec, shape)
	fmt.Fprintf(&sb, "\t\t\t\t(at %s %s %d)\n", fmtNum(p.X), fmtNum(p.Y), angle)
	fmt.Fprintf(&sb, "\t\t\t\t(length %s)\n", fmtNum(length))
	fmt.Fprintf(&sb, "\t\t\t\t(name %s\n\t\t\t\t\t(effects\n\t\t\t\t\t\t(font\n\t\t\t\t\t\t\t(size 1.27 1.27)\n\t\t\t\t\t\t)\n\t\t\t\t\t)\n\t\t\t\t)\n", quote(name))
	fmt.Fprintf(&sb, "\t\t\t\t(number %s\n\t\t\t\t\t(effects\n\t\t\t\t\t\t(font\n\t\t\t\t\t\t\t(size 1.27 1.27)\n\t\t\t\t\t\t)\n\t\t\t\t\t)\n\t\t\t\t)\n", quote(number))
	sb.WriteString("\t\t\t)\n")
	return sb.String(), nil
}

// pinStem reads the pin's stem path and returns the direction it runs in, in
// EasyEDA units.
func pinStem(x, y float64, path string) (dx, dy float64, ok bool) {
	segs := parsePath(path)
	for _, s := range segs {
		if s.cmd != 'L' || len(s.points) < 2 {
			continue
		}
		return s.points[1].X - s.points[0].X, s.points[1].Y - s.points[0].Y, true
	}
	return 0, 0, false
}

// electricalType maps EasyEDA's pin kinds onto KiCad's.
func electricalType(v string) string {
	switch strings.TrimSpace(v) {
	case "1":
		return "input"
	case "2":
		return "output"
	case "3":
		return "bidirectional"
	case "4":
		return "power_in"
	default:
		return "unspecified"
	}
}

func symbolRect(f frame, r rec) string {
	x, y := r.num(1), r.num(2)
	w, h := r.num(5), r.num(6)
	a := f.pt(x, y)
	b := f.pt(x+w, y+h)
	return fmt.Sprintf("\t\t\t(rectangle\n\t\t\t\t(start %s %s)\n\t\t\t\t(end %s %s)\n%s\t\t\t)\n",
		fmtNum(a.X), fmtNum(a.Y), fmtNum(b.X), fmtNum(b.Y), strokeAndFill(3))
}

func symbolPolyline(f frame, r rec, closed bool) string {
	pts := f.points(r.at(1))
	if len(pts) < 2 {
		return ""
	}
	if closed && (pts[0] != pts[len(pts)-1]) {
		pts = append(pts, pts[0])
	}
	return polyNode(pts, 3)
}

func symbolEllipse(f frame, r rec) string {
	c := f.pt(r.num(1), r.num(2))
	rx, ry := f.length(r.num(3)), f.length(r.num(4))
	// KiCad symbols have circles, not ellipses. An LCSC ellipse is round in
	// every case seen; when it is not, the mean radius keeps it in place
	// rather than dropping the graphic.
	radius := (math.Abs(rx) + math.Abs(ry)) / 2
	if radius == 0 {
		return ""
	}
	return fmt.Sprintf("\t\t\t(circle\n\t\t\t\t(center %s %s)\n\t\t\t\t(radius %s)\n%s\t\t\t)\n",
		fmtNum(c.X), fmtNum(c.Y), fmtNum(radius), strokeAndFill(3))
}

// symbolPath renders a path graphic as one or more polylines and arcs.
func symbolPath(f frame, r rec) string {
	var sb strings.Builder
	var run []pt
	flush := func() {
		if len(run) >= 2 {
			sb.WriteString(polyNode(run, 3))
		}
		run = nil
	}
	for _, s := range parsePath(r.at(1)) {
		switch s.cmd {
		case 'M':
			flush()
			run = []pt{f.pt(s.points[0].X, s.points[0].Y)}
		case 'L', 'Z':
			if len(s.points) == 2 {
				if len(run) == 0 {
					run = append(run, f.pt(s.points[0].X, s.points[0].Y))
				}
				run = append(run, f.pt(s.points[1].X, s.points[1].Y))
			}
		case 'A':
			flush()
			if mid, ok := arcMidpoint(s.arc); ok {
				a := f.pt(s.arc.start.X, s.arc.start.Y)
				m := f.pt(mid.X, mid.Y)
				e := f.pt(s.arc.end.X, s.arc.end.Y)
				fmt.Fprintf(&sb, "\t\t\t(arc\n\t\t\t\t(start %s %s)\n\t\t\t\t(mid %s %s)\n\t\t\t\t(end %s %s)\n%s\t\t\t)\n",
					fmtNum(a.X), fmtNum(a.Y), fmtNum(m.X), fmtNum(m.Y), fmtNum(e.X), fmtNum(e.Y), strokeAndFill(3))
			}
		}
	}
	flush()
	return sb.String()
}

func polyNode(pts []pt, indent int) string {
	tab := strings.Repeat("\t", indent)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s(polyline\n%s\t(pts\n", tab, tab)
	for _, p := range pts {
		fmt.Fprintf(&sb, "%s\t\t(xy %s %s)\n", tab, fmtNum(p.X), fmtNum(p.Y))
	}
	fmt.Fprintf(&sb, "%s\t)\n%s%s)\n", tab, strokeAndFill(indent+1), tab)
	return sb.String()
}

func strokeAndFill(indent int) string {
	tab := strings.Repeat("\t", indent)
	return fmt.Sprintf("%s(stroke\n%s\t(width 0)\n%s\t(type default)\n%s)\n%s(fill\n%s\t(type none)\n%s)\n",
		tab, tab, tab, tab, tab, tab, tab)
}

// quote renders a KiCad string literal.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}

// countSummary turns a tally into "SVGNODE×2, TEXT×5", sorted so the note
// reads the same on every run.
func countSummary(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s×%d", k, counts[k]))
	}
	return strings.Join(out, ", ")
}
