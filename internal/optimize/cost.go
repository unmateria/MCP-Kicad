// Package optimize scores schematic layouts and searches for the best one
// among a set of candidates. The goal is to replace single-shot heuristic
// layout (which produces "mediocre" results) with a measurable, optimizable
// process — define what "good" means quantitatively, then explore.
package optimize

import (
	"math"

	"mcp-kicad/internal/sexp"
)

// Layout is a candidate placement of components plus the wires that result
// from routing it. Score is computed by Cost().
type Layout struct {
	Symbols []sexp.SchematicSymbol // resolved positions, rotations, AND pin coords
	Wires   []Wire                 // routed wire segments (single-segment each)
}

// Wire is one straight segment of a routed net (after pin-direction-aware
// routing + collinear merging). The router output decomposes into these.
type Wire struct {
	X1, Y1, X2, Y2 float64
}

// CostBreakdown details the score components so we can report what made one
// layout worse than another. All fields are penalties (lower = better) — except
// AxisAlignBonus which carries a NEGATIVE weight (more is better, ergo it
// reduces the total).
type CostBreakdown struct {
	WireLength      float64 // sum of Manhattan lengths in mm — gentle pressure to be compact
	WireCrossings   int     // pairs of wires that cross at non-endpoint points
	WireBodyHits    int     // wire segments that pass through any symbol body
	BodyOverlaps    int     // pairs of symbols whose bodies overlap
	LabelHits       int     // symbol-vs-symbol-property overlaps (component label clashes)
	GridMisalign    int     // pin tips not on the 1.27 mm KiCad connection grid

	// Aesthetic metrics — weighted softer than electrical/correctness penalties.
	PinAxisMisalign float64 // 2-pin nets whose endpoints DON'T share an axis but could
	WireBodyClear   int     // wire segments running closer than 1.27mm to a symbol body without entering
	WhitespaceVar   float64 // variance of symbol density across a 4×4 grid (lower = even spread)
	AxisAlignBonus  int     // pairs of connected symbols sharing an X or Y (negative weight = bonus)
	SymmetryDev     float64 // average deviation from the cluster midline (clusters expect mirror layouts)

	Total float64 // weighted sum
}

// Cost weights — tuned so wire crossings and body collisions dominate, with
// length and grid-alignment as soft tie-breakers. Real engineers reject
// crossings and overlaps absolutely; long wires merely look ugly.
//
// WhitespaceVar was 10 originally; calibration on inv_amp showed a clean
// topology with one horizontal gap was scoring 0/100 because of this single
// term. Reduced to 2 — still rewards even spreads but doesn't dominate.
const (
	wWireLength      = 0.05
	wWireCrossing    = 200.0
	wWireBodyHit     = 500.0
	wBodyOverlap     = 5000.0 // symbols on top of each other = catastrophic
	wLabelHit        = 8.0    // approximate metric — reduced from 30 to avoid drowning the score
	wGridMisalign    = 100.0
	wPinAxisMisalign = 80.0
	wWireBodyClear   = 100.0  // soft pressure — KiCad routes are clean even when 1.27mm close
	wWhitespaceVar   = 2.0
	wAxisAlignBonus  = -25.0  // bonus: negative weight reduces total
	wSymmetryDev     = 20.0
)

// Score returns the weighted total cost. Lower is better.
func (b CostBreakdown) Score() float64 {
	return wWireLength*b.WireLength +
		wWireCrossing*float64(b.WireCrossings) +
		wWireBodyHit*float64(b.WireBodyHits) +
		wBodyOverlap*float64(b.BodyOverlaps) +
		wLabelHit*float64(b.LabelHits) +
		wGridMisalign*float64(b.GridMisalign) +
		wPinAxisMisalign*b.PinAxisMisalign +
		wWireBodyClear*float64(b.WireBodyClear) +
		wWhitespaceVar*b.WhitespaceVar +
		wAxisAlignBonus*float64(b.AxisAlignBonus) +
		wSymmetryDev*b.SymmetryDev
}

// Score100 normalizes Total into a 0-100 quality scale where higher is better.
// Calibration: a clean hand-edited demo schematic should land at ≥80,
// PlaceFlow legacy output ~30-50, broken layouts <30.
//
// Mapping is exponential because cost is mostly dominated by a few hard
// penalties (crossings, body hits) — we want the reportable score to react
// strongly when those drop to zero.
func (b CostBreakdown) Score100() int {
	// Reference scale: penalty 0 → 100, penalty 50 → 80, penalty 200 → 50,
	// penalty 1000 → ~20, penalty 5000 (one body overlap) → ~3.
	t := b.Total
	if t < 0 {
		t = 0 // AxisAlignBonus could push slightly negative; clamp.
	}
	score := 100.0 - 30.0*math.Log1p(t/100.0)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return int(math.Round(score))
}

// Cost evaluates a candidate Layout against all the quality criteria and
// returns the breakdown. Symbol bodies are approximated by `symbolBodyRect`
// (the inner box bounded by pin tips, inset by pinLen so pin cells stay
// outside). Wire-body hits use strict-interior crossing, so a wire that
// terminates on a pin doesn't count as "through the body".
func Cost(l Layout) CostBreakdown {
	b := CostBreakdown{}

	// 1. Wire length — Manhattan, since all routed wires are axis-aligned.
	for _, w := range l.Wires {
		b.WireLength += math.Abs(w.X2-w.X1) + math.Abs(w.Y2-w.Y1)
	}

	// 2. Wire-wire crossings (strict interior, axis-aligned).
	for i := 0; i < len(l.Wires); i++ {
		for j := i + 1; j < len(l.Wires); j++ {
			if wiresCross(l.Wires[i], l.Wires[j]) {
				b.WireCrossings++
			}
		}
	}

	// 3. Wire-body intersections.
	for _, sym := range l.Symbols {
		x1, y1, x2, y2 := symbolBodyRect(sym)
		if x1 == x2 || y1 == y2 {
			continue // degenerate (1-pin power symbols, or symmetric body)
		}
		for _, w := range l.Wires {
			if segmentEntersBox(w.X1, w.Y1, w.X2, w.Y2, x1, y1, x2, y2) {
				b.WireBodyHits++
			}
		}
	}

	// 4. Body-body overlaps (catastrophic — components stacked).
	for i := 0; i < len(l.Symbols); i++ {
		ix1, iy1, ix2, iy2 := symbolBodyRect(l.Symbols[i])
		for j := i + 1; j < len(l.Symbols); j++ {
			jx1, jy1, jx2, jy2 := symbolBodyRect(l.Symbols[j])
			if rectsOverlap(ix1, iy1, ix2, iy2, jx1, jy1, jx2, jy2) {
				b.BodyOverlaps++
			}
		}
	}

	// 5. Label hits — symbols whose property anchors fall inside another
	// symbol's body. Approximation: a property at (px, py) is treated as a
	// 6 mm × 2.5 mm rectangle to model the rendered text bounding box.
	for i, si := range l.Symbols {
		for _, prop := range symbolPropertyPositions(si) {
			lx1 := prop.X - 3
			ly1 := prop.Y - 1.27
			lx2 := prop.X + 3
			ly2 := prop.Y + 1.27
			for j, sj := range l.Symbols {
				if i == j {
					continue
				}
				bx1, by1, bx2, by2 := symbolBodyRect(sj)
				if rectsOverlap(lx1, ly1, lx2, ly2, bx1, by1, bx2, by2) {
					b.LabelHits++
				}
			}
		}
	}

	// 6. Grid misalignment — pin tips that aren't on the 1.27 mm grid.
	for _, sym := range l.Symbols {
		for _, p := range sym.Pins {
			if !onGrid(p.X) || !onGrid(p.Y) {
				b.GridMisalign++
			}
		}
	}

	// 7. Aesthetic metrics — only soft pressure but they break ties between
	// equally-valid topologies in favour of "looks like a human did it."
	b.PinAxisMisalign = pinAxisMisalignment(l)
	b.WireBodyClear = wireBodyClearanceHits(l)
	b.WhitespaceVar = whitespaceVariance(l)
	b.AxisAlignBonus = axisAlignedConnectedSymbols(l)
	b.SymmetryDev = symmetryDeviation(l)

	b.Total = b.Score()
	return b
}

// pinAxisMisalignment counts wires whose two endpoints are both pin tips and
// could-but-don't share an axis. A 2-pin connection that runs as a single
// straight line scores 0; an L-shape adds the orthogonal offset (in mm) to the
// penalty. The optimizer pushes connected symbols to share rows/columns.
//
// The heuristic uses pin tips of the placed symbols as a proxy for "intended
// connection endpoints" — wires that don't terminate on a pin (mid-net hops)
// are skipped.
func pinAxisMisalignment(l Layout) float64 {
	const eps = 0.05
	pinSet := make(map[[2]float64]struct{}, len(l.Symbols)*4)
	for _, sym := range l.Symbols {
		for _, p := range sym.Pins {
			pinSet[[2]float64{round01(p.X), round01(p.Y)}] = struct{}{}
		}
	}
	pen := 0.0
	for _, w := range l.Wires {
		_, a := pinSet[[2]float64{round01(w.X1), round01(w.Y1)}]
		_, b := pinSet[[2]float64{round01(w.X2), round01(w.Y2)}]
		if !a || !b {
			continue
		}
		dx := math.Abs(w.X1 - w.X2)
		dy := math.Abs(w.Y1 - w.Y2)
		if dx < eps || dy < eps {
			continue // already axis-aligned — no penalty
		}
		// Penalty proportional to the smaller offset (cost of misaligning).
		if dx < dy {
			pen += dx
		} else {
			pen += dy
		}
	}
	return pen
}

// wireBodyClearanceHits counts wire segments running within 1.27 mm of a
// symbol body without piercing it. KiCad convention is to keep ≥1 grid step
// of clearance so labels/values don't collide with traces.
func wireBodyClearanceHits(l Layout) int {
	const clearance = 1.27
	hits := 0
	for _, sym := range l.Symbols {
		x1, y1, x2, y2 := symbolBodyRect(sym)
		if x1 == x2 || y1 == y2 {
			continue
		}
		ex1 := x1 - clearance
		ey1 := y1 - clearance
		ex2 := x2 + clearance
		ey2 := y2 + clearance
		for _, w := range l.Wires {
			// Already counted by WireBodyHits if it actually pierces.
			if segmentEntersBox(w.X1, w.Y1, w.X2, w.Y2, x1, y1, x2, y2) {
				continue
			}
			if segmentEntersBox(w.X1, w.Y1, w.X2, w.Y2, ex1, ey1, ex2, ey2) {
				hits++
			}
		}
	}
	return hits
}

// whitespaceVariance returns the variance of symbol-anchor density across a
// 4×4 grid laid over the layout's bounding box. Even spread → low variance.
// Returns 0 when the layout has fewer than 4 symbols (variance is meaningless).
func whitespaceVariance(l Layout) float64 {
	if len(l.Symbols) < 4 {
		return 0
	}
	const cells = 4
	minX, minY := math.MaxFloat64, math.MaxFloat64
	maxX, maxY := -math.MaxFloat64, -math.MaxFloat64
	for _, s := range l.Symbols {
		if s.X < minX {
			minX = s.X
		}
		if s.X > maxX {
			maxX = s.X
		}
		if s.Y < minY {
			minY = s.Y
		}
		if s.Y > maxY {
			maxY = s.Y
		}
	}
	w, h := maxX-minX, maxY-minY
	if w < 1 || h < 1 {
		return 0
	}
	var counts [cells * cells]int
	for _, s := range l.Symbols {
		cx := int((s.X - minX) / w * float64(cells))
		cy := int((s.Y - minY) / h * float64(cells))
		if cx >= cells {
			cx = cells - 1
		}
		if cy >= cells {
			cy = cells - 1
		}
		counts[cy*cells+cx]++
	}
	mean := float64(len(l.Symbols)) / float64(cells*cells)
	v := 0.0
	for _, c := range counts {
		d := float64(c) - mean
		v += d * d
	}
	return v / float64(cells*cells)
}

// axisAlignedConnectedSymbols counts pairs of symbols that share an X or Y
// coordinate (within 0.5 mm) AND are connected by a wire endpoint chain. The
// caller must NEGATE the weight — more aligned pairs is better.
func axisAlignedConnectedSymbols(l Layout) int {
	const eps = 0.5
	wireMap := make(map[[2]float64][][2]float64)
	for _, w := range l.Wires {
		a := [2]float64{round01(w.X1), round01(w.Y1)}
		b := [2]float64{round01(w.X2), round01(w.Y2)}
		wireMap[a] = append(wireMap[a], b)
		wireMap[b] = append(wireMap[b], a)
	}
	pinOwner := make(map[[2]float64]int)
	for i, sym := range l.Symbols {
		for _, p := range sym.Pins {
			pinOwner[[2]float64{round01(p.X), round01(p.Y)}] = i
		}
	}
	type pair struct{ a, b int }
	connected := make(map[pair]bool)
	for src := range wireMap {
		ownerA, hasA := pinOwner[src]
		if !hasA {
			continue
		}
		// BFS within wire graph until we reach another pin.
		seen := map[[2]float64]bool{src: true}
		queue := [][2]float64{src}
		for len(queue) > 0 {
			pt := queue[0]
			queue = queue[1:]
			for _, nb := range wireMap[pt] {
				if seen[nb] {
					continue
				}
				seen[nb] = true
				if ownerB, hasB := pinOwner[nb]; hasB && ownerB != ownerA {
					a, b := ownerA, ownerB
					if a > b {
						a, b = b, a
					}
					connected[pair{a, b}] = true
					continue
				}
				queue = append(queue, nb)
			}
		}
	}
	bonus := 0
	for p := range connected {
		sa, sb := l.Symbols[p.a], l.Symbols[p.b]
		if math.Abs(sa.X-sb.X) < eps || math.Abs(sa.Y-sb.Y) < eps {
			bonus++
		}
	}
	return bonus
}

// symmetryDeviation measures how far the layout deviates from a vertical
// axis of symmetry. For each symbol on the left of the centroid, it looks for
// a partner on the right at a similar Y; the residual offset accumulates.
// Topologies without natural symmetry score near zero (no partners found).
func symmetryDeviation(l Layout) float64 {
	if len(l.Symbols) < 4 {
		return 0
	}
	cx := 0.0
	for _, s := range l.Symbols {
		cx += s.X
	}
	cx /= float64(len(l.Symbols))
	dev := 0.0
	matched := 0
	for i, si := range l.Symbols {
		if si.X >= cx-0.5 {
			continue
		}
		// Look for the closest "mirror" candidate at Y ≈ si.Y.
		bestDist := math.MaxFloat64
		bestJ := -1
		for j, sj := range l.Symbols {
			if j == i || sj.X <= cx+0.5 {
				continue
			}
			if math.Abs(sj.Y-si.Y) > 5 {
				continue
			}
			d := math.Abs((cx - si.X) - (sj.X - cx))
			if d < bestDist {
				bestDist = d
				bestJ = j
			}
		}
		if bestJ >= 0 {
			dev += bestDist
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return dev / float64(matched)
}

func round01(v float64) float64 {
	return math.Round(v*100) / 100
}

// symbolBodyRect returns the inner bounding box of a symbol's drawn body —
// inset from the outermost pin tips by the standard 2.54 mm pin length so
// pin endpoints stay outside the rectangle and don't accidentally count as
// body-hits. Falls back to a zero-area rect for power symbols (1 pin).
func symbolBodyRect(sym sexp.SchematicSymbol) (x1, y1, x2, y2 float64) {
	if len(sym.Pins) < 2 {
		return sym.X, sym.Y, sym.X, sym.Y
	}
	x1, y1 = sym.Pins[0].X, sym.Pins[0].Y
	x2, y2 = x1, y1
	for _, p := range sym.Pins[1:] {
		if p.X < x1 {
			x1 = p.X
		}
		if p.Y < y1 {
			y1 = p.Y
		}
		if p.X > x2 {
			x2 = p.X
		}
		if p.Y > y2 {
			y2 = p.Y
		}
	}
	const pinLen = 2.54
	x1 += pinLen
	y1 += pinLen
	x2 -= pinLen
	y2 -= pinLen
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	return x1, y1, x2, y2
}

// PropPos is a synthetic position used by the cost function — it represents
// where a label/property would be rendered on the schematic.
type PropPos struct{ X, Y float64 }

// symbolPropertyPositions returns the rendered position for each visible
// property (Reference + Value, typically). Without parsing all KiCad font
// metrics, we approximate with the symbol's anchor offset by ±3.81 mm in
// the symbol's principal axis — this is where Reference and Value sit by
// default in `NewSymbolInstance`.
func symbolPropertyPositions(sym sexp.SchematicSymbol) []PropPos {
	rad := sym.Rotation * math.Pi / 180
	c, s := math.Cos(rad), math.Sin(rad)
	const offset = 3.81
	rotate := func(ox, oy float64) PropPos {
		return PropPos{
			X: sym.X + ox*c - oy*s,
			Y: sym.Y - ox*s - oy*c,
		}
	}
	return []PropPos{
		rotate(0, offset),  // Reference
		rotate(0, -offset), // Value
	}
}

// wiresCross returns true when two axis-aligned segments intersect strictly
// inside both (touching at a shared endpoint doesn't count).
func wiresCross(a, b Wire) bool {
	const eps = 0.01
	aHoriz := math.Abs(a.Y1-a.Y2) < eps
	bHoriz := math.Abs(b.Y1-b.Y2) < eps
	if aHoriz == bHoriz {
		return false // parallel segments don't cross
	}
	hr, vr := a, b
	if !aHoriz {
		hr, vr = b, a
	}
	hY := hr.Y1
	vX := vr.X1
	hLo, hHi := math.Min(hr.X1, hr.X2), math.Max(hr.X1, hr.X2)
	vLo, vHi := math.Min(vr.Y1, vr.Y2), math.Max(vr.Y1, vr.Y2)
	return vX > hLo+eps && vX < hHi-eps && hY > vLo+eps && hY < vHi-eps
}

// segmentEntersBox returns true when an axis-aligned segment passes through
// the strict interior of a rectangle (touching the edge doesn't count).
func segmentEntersBox(ax, ay, bx, by, rx1, ry1, rx2, ry2 float64) bool {
	const eps = 0.01
	if ax > bx {
		ax, bx = bx, ax
	}
	if ay > by {
		ay, by = by, ay
	}
	return ax < rx2-eps && bx > rx1+eps && ay < ry2-eps && by > ry1+eps
}

// rectsOverlap returns true when two axis-aligned rectangles share interior.
func rectsOverlap(ax1, ay1, ax2, ay2, bx1, by1, bx2, by2 float64) bool {
	const eps = 0.01
	return ax1 < bx2-eps && ax2 > bx1+eps && ay1 < by2-eps && ay2 > by1+eps
}

// onGrid returns true when v is within 0.01 mm of an integer multiple of
// 1.27 mm (the KiCad standard connection grid).
func onGrid(v float64) bool {
	r := v / 1.27
	return math.Abs(r-math.Round(r)) < 0.01
}
