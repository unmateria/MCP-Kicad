package sexp

import "strings"

// graphicBBox returns the bounding box, in absolute schematic coordinates, of
// the drawn graphic primitives of a lib symbol definition for one placed
// instance.
//
// Only the sub-units that are actually rendered for that instance contribute:
// sub-unit 0 (geometry shared by every unit) plus the instance's own unit —
// the same filter extractPins applies to pins. unit == 0 takes everything.
//
// Primitive coordinates are stored in the symbol's local Y-up frame, exactly
// like pin positions, so they go through transformPin: symbol rotation and the
// Y flip are applied identically to pins and to graphics.
//
// ok is false when the definition draws nothing (a pin-only unit, e.g. the
// power unit of a multi-unit IC).
func graphicBBox(def *Node, cx, cy, rot float64, unit int) (x1, y1, x2, y2 float64, ok bool) {
	partName := StringValue(def, 1)
	if idx := strings.LastIndex(partName, ":"); idx >= 0 {
		partName = partName[idx+1:]
	}

	add := func(lx, ly float64) {
		sx, sy := transformPin(lx, ly, cx, cy, rot)
		if !ok {
			x1, y1, x2, y2 = sx, sy, sx, sy
			ok = true
			return
		}
		if sx < x1 {
			x1 = sx
		}
		if sy < y1 {
			y1 = sy
		}
		if sx > x2 {
			x2 = sx
		}
		if sy > y2 {
			y2 = sy
		}
	}

	// Primitives declared directly on the top-level symbol node.
	for _, child := range def.Children {
		addPrimitivePoints(child, add)
	}

	// Primitives inside sub-unit nodes, filtered by unit index.
	for _, child := range def.Children {
		if child.Head() != "symbol" {
			continue
		}
		subName := StringValue(child, 1)
		if subName == "" {
			subName = AtomValue(child, 1)
		}
		u := subUnitIndex(partName, subName)
		if unit != 0 && u != 0 && u != unit {
			continue
		}
		for _, prim := range child.Children {
			addPrimitivePoints(prim, add)
		}
	}

	if !ok {
		return 0, 0, 0, 0, false
	}
	return round2(x1), round2(y1), round2(x2), round2(y2), true
}

// addPrimitivePoints feeds add every local-frame point whose bounding box
// covers the primitive. Arcs are approximated by their three defining points
// and circles by the corners of their enclosing square — both conservative
// under the 0/90/180/270 rotations KiCad allows. Text is deliberately ignored:
// field text is placed by textplace, not part of the drawn body.
func addPrimitivePoints(n *Node, add func(lx, ly float64)) {
	switch n.Head() {
	case "rectangle":
		s, e := FindList(n, "start"), FindList(n, "end")
		if s == nil || e == nil {
			return
		}
		sx, sy := parseF(AtomValue(s, 1)), parseF(AtomValue(s, 2))
		ex, ey := parseF(AtomValue(e, 1)), parseF(AtomValue(e, 2))
		add(sx, sy)
		add(ex, sy)
		add(ex, ey)
		add(sx, ey)
	case "polyline", "bezier":
		pts := FindList(n, "pts")
		if pts == nil {
			return
		}
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			add(parseF(AtomValue(xy, 1)), parseF(AtomValue(xy, 2)))
		}
	case "circle":
		c, r := FindList(n, "center"), FindList(n, "radius")
		if c == nil || r == nil {
			return
		}
		ccx, ccy := parseF(AtomValue(c, 1)), parseF(AtomValue(c, 2))
		rad := parseF(AtomValue(r, 1))
		add(ccx-rad, ccy-rad)
		add(ccx+rad, ccy-rad)
		add(ccx+rad, ccy+rad)
		add(ccx-rad, ccy+rad)
	case "arc":
		for _, name := range [...]string{"start", "mid", "end"} {
			p := FindList(n, name)
			if p == nil {
				continue
			}
			add(parseF(AtomValue(p, 1)), parseF(AtomValue(p, 2)))
		}
	}
}
