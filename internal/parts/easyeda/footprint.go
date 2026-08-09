package easyeda

import (
	"fmt"
	"math"
	"strings"
)

// kicadLayer maps an EasyEDA layer id onto a KiCad layer name.
//
// The ids are EasyEDA's fixed layer table. Ids above 20 are user layers whose
// meaning is per-document; anything not listed is dropped and counted, rather
// than guessed onto a copper layer where it would become a short.
var kicadLayer = map[string]string{
	"1":   "F.Cu",
	"2":   "B.Cu",
	"3":   "F.SilkS",
	"4":   "B.SilkS",
	"5":   "F.Paste",
	"6":   "B.Paste",
	"7":   "F.Mask",
	"8":   "B.Mask",
	"10":  "Edge.Cuts", // EasyEDA's BoardOutLine
	"11":  "Cmts.User", // Multi-layer marker in pads; a document layer elsewhere
	"12":  "F.Fab",
	"13":  "B.Fab",
	"100": "F.Mask",
	"101": "F.SilkS",
}

// convertFootprint turns an EasyEDA package document into a .kicad_mod.
func convertFootprint(c *Component, name string) ([]byte, []string, error) {
	pkg := c.PackageDetail.DataStr
	// A footprint keeps EasyEDA's Y direction: KiCad's board coordinates point
	// down too. Only the schematic needs flipping.
	f := frame{ox: pkg.Head.X, oy: pkg.Head.Y, flipY: false}

	var graphics strings.Builder
	var pads strings.Builder
	var notes []string
	skipped := map[string]int{}
	padCount := 0
	throughHole := false

	for _, raw := range pkg.Shape {
		r := parseRec(raw)
		switch r.kind {
		case "PAD":
			s, tht, err := footprintPad(f, r)
			if err != nil {
				notes = append(notes, "pad skipped: "+err.Error())
				continue
			}
			pads.WriteString(s)
			padCount++
			throughHole = throughHole || tht
		case "TRACK":
			graphics.WriteString(footprintTrack(f, r))
		case "CIRCLE":
			graphics.WriteString(footprintCircle(f, r))
		case "ARC":
			graphics.WriteString(footprintArc(f, r))
		case "SOLIDREGION":
			s, note := footprintRegion(f, r)
			graphics.WriteString(s)
			if note != "" {
				notes = append(notes, note)
			}
		case "HOLE":
			pads.WriteString(footprintHole(f, r))
		case "RECT":
			graphics.WriteString(footprintRect(f, r))
		default:
			// TEXT is KiCad's own Reference/Value here; SVGNODE is the 3D
			// model, which this converter does not translate.
			skipped[r.kind]++
		}
	}

	if padCount == 0 {
		return nil, notes, fmt.Errorf("the package has no pads")
	}
	if len(skipped) > 0 {
		notes = append(notes, "footprint graphics not translated: "+countSummary(skipped))
	}

	attr := "smd"
	if throughHole {
		attr = "through_hole"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "(footprint %s\n", quote(name))
	sb.WriteString("\t(version 20240108)\n\t(generator \"mcp-kicad\")\n\t(layer \"F.Cu\")\n")
	descr := strings.TrimSpace(c.Description)
	if descr == "" {
		descr = c.MPN()
	}
	fmt.Fprintf(&sb, "\t(descr %s)\n", quote(descr))
	fmt.Fprintf(&sb, "\t(tags %s)\n", quote(strings.TrimSpace(c.LCSC.Number+" "+c.MPN())))
	sb.WriteString(fpProperty("Reference", "REF**", "F.SilkS", false))
	sb.WriteString(fpProperty("Value", name, "F.Fab", false))
	if c.LCSC.Number != "" {
		sb.WriteString(fpProperty("LCSC", c.LCSC.Number, "F.Fab", true))
	}
	fmt.Fprintf(&sb, "\t(attr %s)\n", attr)
	sb.WriteString(graphics.String())
	sb.WriteString(pads.String())
	sb.WriteString(")\n")
	return []byte(sb.String()), notes, nil
}

func fpProperty(name, value, layer string, hidden bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\t(property %s %s\n\t\t(at 0 0 0)\n\t\t(layer %s)\n", quote(name), quote(value), quote(layer))
	if hidden {
		sb.WriteString("\t\t(hide yes)\n")
	}
	sb.WriteString("\t\t(effects\n\t\t\t(font\n\t\t\t\t(size 1 1)\n\t\t\t\t(thickness 0.15)\n\t\t\t)\n\t\t)\n\t)\n")
	return sb.String()
}

// footprintPad renders one EasyEDA pad.
//
//	PAD ~ shape ~ x ~ y ~ width ~ height ~ layer ~ net ~ number ~ holeRadius ~
//	      points ~ rotation ~ id ~ holeLength ~ holePoint ~ plated ~ …
func footprintPad(f frame, r rec) (string, bool, error) {
	number := strings.TrimSpace(r.at(8))
	if number == "" {
		number = strings.TrimSpace(r.at(7))
	}
	if number == "" {
		return "", false, fmt.Errorf("pad with no number")
	}
	p := f.pt(r.num(2), r.num(3))
	w, h := f.length(r.num(4)), f.length(r.num(5))
	holeR := f.length(r.num(9))
	rot := r.num(11)
	layerID := strings.TrimSpace(r.at(6))

	// EasyEDA marks a through-hole pad by putting it on layer 11 (multi-layer);
	// a drill radius alone is not enough, because a plated SMD pad in a
	// castellated package also carries one.
	tht := layerID == "11" || holeR > 0
	padType, layers := "smd", `"F.Cu" "F.Paste" "F.Mask"`
	switch {
	case tht && strings.EqualFold(strings.TrimSpace(r.at(15)), "N"):
		padType, layers = "np_thru_hole", `"*.Cu" "*.Mask"`
	case tht:
		padType, layers = "thru_hole", `"*.Cu" "*.Mask"`
	case layerID == "2":
		layers = `"B.Cu" "B.Paste" "B.Mask"`
	}

	shape, extra := "rect", ""
	switch strings.ToUpper(strings.TrimSpace(r.at(1))) {
	case "ELLIPSE":
		shape = "circle"
		if math.Abs(w-h) > 1e-6 {
			shape = "oval"
		}
	case "OVAL":
		shape = "oval"
	case "RECT":
		shape = "rect"
	case "POLYGON":
		shape = "custom"
		pts := f.points(r.at(10))
		if len(pts) < 3 {
			shape = "rect"
			break
		}
		extra = customPadPrimitive(pts, p)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "\t(pad %s %s %s\n", quote(number), padType, shape)
	fmt.Fprintf(&sb, "\t\t(at %s %s", fmtNum(p.X), fmtNum(p.Y))
	if rot != 0 {
		fmt.Fprintf(&sb, " %s", fmtNum(rot))
	}
	sb.WriteString(")\n")
	fmt.Fprintf(&sb, "\t\t(size %s %s)\n", fmtNum(math.Abs(w)), fmtNum(math.Abs(h)))
	if holeR > 0 {
		if hl := f.length(r.num(13)); hl > 0 && math.Abs(hl-2*holeR) > 1e-6 {
			fmt.Fprintf(&sb, "\t\t(drill oval %s %s)\n", fmtNum(2*holeR), fmtNum(hl))
		} else {
			fmt.Fprintf(&sb, "\t\t(drill %s)\n", fmtNum(2*holeR))
		}
	}
	fmt.Fprintf(&sb, "\t\t(layers %s)\n", layers)
	if extra != "" {
		sb.WriteString(extra)
	}
	sb.WriteString("\t)\n")
	return sb.String(), tht, nil
}

// customPadPrimitive renders a polygon pad's outline, in coordinates relative
// to the pad's own anchor as KiCad expects.
func customPadPrimitive(pts []pt, anchor pt) string {
	var sb strings.Builder
	sb.WriteString("\t\t(options\n\t\t\t(clearance outline)\n\t\t\t(anchor rect)\n\t\t)\n")
	sb.WriteString("\t\t(primitives\n\t\t\t(gr_poly\n\t\t\t\t(pts\n")
	for _, q := range pts {
		fmt.Fprintf(&sb, "\t\t\t\t\t(xy %s %s)\n", fmtNum(q.X-anchor.X), fmtNum(q.Y-anchor.Y))
	}
	sb.WriteString("\t\t\t\t)\n\t\t\t\t(width 0)\n\t\t\t\t(fill yes)\n\t\t\t)\n\t\t)\n")
	return sb.String()
}

// footprintTrack renders a silkscreen or fabrication line run.
func footprintTrack(f frame, r rec) string {
	layer, ok := kicadLayer[strings.TrimSpace(r.at(2))]
	if !ok {
		return ""
	}
	width := f.length(r.num(1))
	if width <= 0 {
		width = 0.12
	}
	pts := f.points(r.at(4))
	var sb strings.Builder
	for i := 0; i+1 < len(pts); i++ {
		fmt.Fprintf(&sb, "\t(fp_line\n\t\t(start %s %s)\n\t\t(end %s %s)\n\t\t(stroke\n\t\t\t(width %s)\n\t\t\t(type solid)\n\t\t)\n\t\t(layer %s)\n\t)\n",
			fmtNum(pts[i].X), fmtNum(pts[i].Y), fmtNum(pts[i+1].X), fmtNum(pts[i+1].Y),
			fmtNum(width), quote(layer))
	}
	return sb.String()
}

// footprintCircle renders a circle: CIRCLE ~ cx ~ cy ~ r ~ width ~ layer ~ …
func footprintCircle(f frame, r rec) string {
	layer, ok := kicadLayer[strings.TrimSpace(r.at(5))]
	if !ok {
		return ""
	}
	c := f.pt(r.num(1), r.num(2))
	radius := f.length(r.num(3))
	if radius <= 0 {
		return ""
	}
	width := f.length(r.num(4))
	if width <= 0 {
		width = 0.12
	}
	return fmt.Sprintf("\t(fp_circle\n\t\t(center %s %s)\n\t\t(end %s %s)\n\t\t(stroke\n\t\t\t(width %s)\n\t\t\t(type solid)\n\t\t)\n\t\t(fill no)\n\t\t(layer %s)\n\t)\n",
		fmtNum(c.X), fmtNum(c.Y), fmtNum(c.X+radius), fmtNum(c.Y),
		fmtNum(width), quote(layer))
}

// footprintArc renders an arc: ARC ~ width ~ layer ~ net ~ path ~ …
//
// KiCad wants three points; SVG gives two plus flags. The pin-1 marker on most
// LCSC packages is exactly one of these, so dropping arcs would lose the only
// thing telling you which way round the part goes.
func footprintArc(f frame, r rec) string {
	layer, ok := kicadLayer[strings.TrimSpace(r.at(2))]
	if !ok {
		return ""
	}
	width := f.length(r.num(1))
	if width <= 0 {
		width = 0.12
	}
	var sb strings.Builder
	for _, s := range parsePath(r.at(4)) {
		if s.cmd != 'A' {
			continue
		}
		mid, ok := arcMidpoint(s.arc)
		if !ok {
			continue
		}
		a := f.pt(s.arc.start.X, s.arc.start.Y)
		m := f.pt(mid.X, mid.Y)
		e := f.pt(s.arc.end.X, s.arc.end.Y)
		// A full circle arrives as an arc whose ends coincide. KiCad refuses
		// that; the two-point circle it accepts says the same thing.
		if math.Hypot(a.X-e.X, a.Y-e.Y) < 1e-6 {
			cx, cy := (a.X+m.X)/2, (a.Y+m.Y)/2
			fmt.Fprintf(&sb, "\t(fp_circle\n\t\t(center %s %s)\n\t\t(end %s %s)\n\t\t(stroke\n\t\t\t(width %s)\n\t\t\t(type solid)\n\t\t)\n\t\t(fill no)\n\t\t(layer %s)\n\t)\n",
				fmtNum(cx), fmtNum(cy), fmtNum(m.X), fmtNum(m.Y), fmtNum(width), quote(layer))
			continue
		}
		fmt.Fprintf(&sb, "\t(fp_arc\n\t\t(start %s %s)\n\t\t(mid %s %s)\n\t\t(end %s %s)\n\t\t(stroke\n\t\t\t(width %s)\n\t\t\t(type solid)\n\t\t)\n\t\t(layer %s)\n\t)\n",
			fmtNum(a.X), fmtNum(a.Y), fmtNum(m.X), fmtNum(m.Y), fmtNum(e.X), fmtNum(e.Y),
			fmtNum(width), quote(layer))
	}
	return sb.String()
}

// footprintRegion renders a filled region: SOLIDREGION ~ layer ~ net ~ path ~ type ~ …
//
// A "cutout" region carves a hole out of another one, which KiCad's polygon
// has no way to express. Those are dropped and reported rather than filled in
// solid, which would cover the very opening they exist to leave.
func footprintRegion(f frame, r rec) (string, string) {
	layer, ok := kicadLayer[strings.TrimSpace(r.at(1))]
	if !ok {
		return "", ""
	}
	if strings.EqualFold(strings.TrimSpace(r.at(4)), "cutout") {
		return "", ""
	}
	var pts []pt
	arcs := false
	for _, s := range parsePath(r.at(3)) {
		switch s.cmd {
		case 'M':
			pts = append(pts, f.pt(s.points[0].X, s.points[0].Y))
		case 'L', 'Z':
			if len(s.points) == 2 {
				pts = append(pts, f.pt(s.points[1].X, s.points[1].Y))
			}
		case 'A':
			arcs = true
		}
	}
	if arcs {
		return "", "a filled region with curved edges was left out of the footprint"
	}
	if len(pts) < 3 {
		return "", ""
	}
	var sb strings.Builder
	sb.WriteString("\t(fp_poly\n\t\t(pts\n")
	for _, p := range pts {
		fmt.Fprintf(&sb, "\t\t\t(xy %s %s)\n", fmtNum(p.X), fmtNum(p.Y))
	}
	fmt.Fprintf(&sb, "\t\t)\n\t\t(stroke\n\t\t\t(width 0)\n\t\t\t(type solid)\n\t\t)\n\t\t(fill yes)\n\t\t(layer %s)\n\t)\n", quote(layer))
	return sb.String(), ""
}

// footprintHole renders a mounting hole: HOLE ~ cx ~ cy ~ radius ~ id ~ locked.
// KiCad has no standalone hole, so it becomes an unnumbered, unplated pad —
// which is exactly what fplib counts as a mechanical pad.
func footprintHole(f frame, r rec) string {
	c := f.pt(r.num(1), r.num(2))
	d := 2 * f.length(r.num(3))
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf("\t(pad \"\" np_thru_hole circle\n\t\t(at %s %s)\n\t\t(size %s %s)\n\t\t(drill %s)\n\t\t(layers \"F&B.Cu\" \"*.Mask\")\n\t)\n",
		fmtNum(c.X), fmtNum(c.Y), fmtNum(d), fmtNum(d), fmtNum(d))
}

// footprintRect renders a rectangle: RECT ~ x ~ y ~ w ~ h ~ layer ~ …
func footprintRect(f frame, r rec) string {
	layer, ok := kicadLayer[strings.TrimSpace(r.at(5))]
	if !ok {
		return ""
	}
	a := f.pt(r.num(1), r.num(2))
	b := f.pt(r.num(1)+r.num(3), r.num(2)+r.num(4))
	return fmt.Sprintf("\t(fp_rect\n\t\t(start %s %s)\n\t\t(end %s %s)\n\t\t(stroke\n\t\t\t(width 0.12)\n\t\t\t(type solid)\n\t\t)\n\t\t(fill no)\n\t\t(layer %s)\n\t)\n",
		fmtNum(a.X), fmtNum(a.Y), fmtNum(b.X), fmtNum(b.Y), quote(layer))
}
