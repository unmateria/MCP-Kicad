package sexp

import (
	"fmt"
	"strconv"
)

// ContentBBox returns the bounding box of everything drawn on the schematic:
// symbol bodies (via SymbolBBox, which includes pins plus a 2.54 mm pad),
// wire endpoints, labels, junctions and no_connect markers. ok is false when
// the schematic has no content.
func ContentBBox(sch *Schematic) (minX, minY, maxX, maxY float64, ok bool) {
	minX, minY = 1e18, 1e18
	maxX, maxY = -1e18, -1e18
	grow := func(x, y float64) {
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
		ok = true
	}
	for _, sym := range ReadSymbols(sch) {
		x1, y1, x2, y2 := SymbolBBox(sym)
		grow(x1, y1)
		grow(x2, y2)
	}
	for _, child := range sch.root.Children {
		switch child.Head() {
		case "wire", "bus":
			pts := FindList(child, "pts")
			if pts == nil {
				continue
			}
			for _, xy := range pts.Children {
				if xy.Head() != "xy" {
					continue
				}
				grow(parseF(AtomValue(xy, 1)), parseF(AtomValue(xy, 2)))
			}
		case "label", "global_label", "hierarchical_label", "junction", "no_connect", "text":
			atN := FindList(child, "at")
			if atN == nil {
				continue
			}
			grow(parseF(AtomValue(atN, 1)), parseF(AtomValue(atN, 2)))
		}
	}
	return minX, minY, maxX, maxY, ok
}

// TranslateContent rigidly shifts every placed element of the schematic by
// (dx, dy): symbol instances (with their property label positions), wires,
// buses, labels, junctions, no_connects and free text. Relative geometry is
// untouched, so connectivity and every geometric invariant are preserved.
// Library definitions (lib_symbols) hold local coordinates and are not moved.
func TranslateContent(sch *Schematic, dx, dy float64) {
	if dx == 0 && dy == 0 {
		return
	}
	shiftAt := func(n *Node) {
		atN := FindList(n, "at")
		if atN == nil || len(atN.Children) < 3 {
			return
		}
		x, _ := strconv.ParseFloat(AtomValue(atN, 1), 64)
		y, _ := strconv.ParseFloat(AtomValue(atN, 2), 64)
		atN.Children[1] = Atom(fmt.Sprintf("%.6g", x+dx))
		atN.Children[2] = Atom(fmt.Sprintf("%.6g", y+dy))
	}
	for _, child := range sch.root.Children {
		switch child.Head() {
		case "symbol":
			if FindList(child, "lib_id") == nil {
				continue // lib_symbols entry, local coords
			}
			shiftAt(child)
			movePropertyPositions(child, dx, dy)
		case "wire", "bus":
			pts := FindList(child, "pts")
			if pts == nil {
				continue
			}
			for _, xy := range pts.Children {
				if xy.Head() != "xy" || len(xy.Children) < 3 {
					continue
				}
				// Children[0] is the "xy" head atom; coordinates are [1], [2].
				x, _ := strconv.ParseFloat(AtomValue(xy, 1), 64)
				y, _ := strconv.ParseFloat(AtomValue(xy, 2), 64)
				xy.Children[1] = Atom(fmt.Sprintf("%.6g", x+dx))
				xy.Children[2] = Atom(fmt.Sprintf("%.6g", y+dy))
			}
		case "label", "global_label", "hierarchical_label", "junction", "no_connect", "text":
			shiftAt(child)
		}
	}
}

// PaperSize returns the schematic's paper name (e.g. "A4"). Empty when the
// file carries no (paper ...) node.
func (s *Schematic) PaperSize() string {
	p := FindList(s.root, "paper")
	if p == nil {
		return ""
	}
	return StringValue(p, 1)
}

// SetPaper sets (or inserts) the (paper "SIZE") node.
func (s *Schematic) SetPaper(size string) {
	if p := FindList(s.root, "paper"); p != nil {
		if len(p.Children) >= 2 {
			p.Children[1] = Str(size)
		} else {
			p.Children = append(p.Children, Str(size))
		}
		return
	}
	// Insert after the uuid node (matching KiCad's usual header order), or
	// append when no uuid exists.
	node := List(Atom("paper"), Str(size))
	for i, child := range s.root.Children {
		if child.Head() == "uuid" {
			s.root.Children = append(s.root.Children[:i+1],
				append([]*Node{node}, s.root.Children[i+1:]...)...)
			return
		}
	}
	s.root.Children = append(s.root.Children, node)
}
