package sexp

import (
	"math"
	"sort"
	"strconv"
)

// EnsureJunctions adds the solder dots KiCad's own schematics carry, and
// reports how many it added.
//
// This is a DRAWING convention, not a connectivity fix: the wires are already
// joined by their coincident endpoints, and kicad-cli's netlist agrees. What
// the dots settle is what a HUMAN reads. A wire that runs straight through a
// pin with no dot is visually identical to a wire crossing a pin — and by
// KiCad's own rule a crossing is NOT a connection. Two experienced readers
// looked at a schematic this compiler produced and concluded that almost
// nothing in it was connected. They were wrong about the netlist and right
// about the drawing.
//
// The rule below is measured, not recalled. Across the 115 schematics shipped
// with KiCad 10:
//
//	3 or more wire ends at a point        1313 with a dot,   0 without
//	2 collinear wire ends ON a pin         489 with a dot,   0 without
//	2 wire ends turning a corner on a pin  164 with a dot,   6 without
//	a lone wire end at a pin              7263 without a dot
//
// So: a dot wherever three or more wire ends meet, and wherever two or more
// meet on a pin. A plain corner in free space gets nothing.
//
// Junctions merge nets in KiCad, so this must never fire where wires of two
// different nets touch. It cannot: the geometric gate has already guaranteed
// that no wire touches anything belonging to another net, and VerifyNetlist
// re-reads the finished file afterwards and would fail if it had.
func EnsureJunctions(sch *Schematic) int {
	wires := sch.Wires()
	if len(wires) == 0 {
		return 0
	}

	ends := map[gridPoint]int{}
	for _, w := range wires {
		a, b, ok := wireEnds(w)
		if !ok {
			continue
		}
		ends[snapPoint(a)]++
		ends[snapPoint(b)]++
	}

	pins := map[gridPoint]bool{}
	for _, sym := range ReadSymbols(sch) {
		for _, p := range sym.Pins {
			pins[snapPoint([2]float64{p.X, p.Y})] = true
		}
	}

	existing := map[gridPoint]bool{}
	for _, j := range FindAllLists(sch.Root(), "junction") {
		if at := FindList(j, "at"); at != nil {
			existing[snapPoint([2]float64{atofSexp(AtomValue(at, 1)), atofSexp(AtomValue(at, 2))})] = true
		}
	}

	// Sorted, because the order nodes are appended in is part of the output
	// and ranging a map would reshuffle it on every run.
	needed := make([]gridPoint, 0, len(ends))
	for pt, n := range ends {
		if existing[pt] {
			continue
		}
		if n >= 3 || (n >= 2 && pins[pt]) {
			needed = append(needed, pt)
		}
	}
	sort.Slice(needed, func(i, j int) bool {
		if needed[i].y != needed[j].y {
			return needed[i].y < needed[j].y
		}
		return needed[i].x < needed[j].x
	})

	for _, pt := range needed {
		sch.AddJunction(NewJunction(float64(pt.x)/100, float64(pt.y)/100))
	}
	return len(needed)
}

// gridPoint is a coordinate rounded to 0.01 mm, which is finer than any grid
// this compiler places on and coarse enough that two ends meant to coincide
// always do.
type gridPoint struct{ x, y int64 }

func snapPoint(p [2]float64) gridPoint {
	return gridPoint{int64(math.Round(p[0] * 100)), int64(math.Round(p[1] * 100))}
}

// wireEnds returns a wire node's two endpoints.
func wireEnds(w *Node) (a, b [2]float64, ok bool) {
	pts := FindList(w, "pts")
	if pts == nil {
		return a, b, false
	}
	var got [][2]float64
	for _, c := range pts.Children {
		if c.Head() == "xy" {
			got = append(got, [2]float64{atofSexp(AtomValue(c, 1)), atofSexp(AtomValue(c, 2))})
		}
	}
	if len(got) < 2 {
		return a, b, false
	}
	return got[0], got[len(got)-1], true
}

// atofSexp parses a coordinate atom, treating anything unparseable as zero —
// the same tolerance the rest of this package applies to malformed geometry.
func atofSexp(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
