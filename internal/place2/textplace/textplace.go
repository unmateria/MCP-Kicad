// Package textplace is the final text-autoplacement pass of the schematic
// pipeline. Bodies and wires are already guaranteed clean by
// internal/place2/gate; what still collides in generated schematics is TEXT:
// Reference/Value fields that rotate with their symbol and print vertically
// across a neighbouring net label, an oversized power-symbol value landing on
// a capacitor's value, a net label running into a reference designator.
//
// Autoplace models every visible string as a rectangle, collects every
// obstacle a string can hit (symbol bodies, wires, pin tips, net labels,
// no-connect markers), then re-anchors each Reference/Value block to the
// lowest-overlap position around its own body — always horizontal — and flips
// colliding net labels around their anchor, which is electrical and must not
// move.
//
// The pass is deterministic: symbols are visited in Reference/unit order,
// labels in name/position order, and no map is ever iterated.
package textplace

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

const (
	lineHeight  = 1.9  // rendered height of one text line, mm
	charWidth   = 1.2  // rendered width of one rune, mm
	textPadding = 0.8  // horizontal padding added to every text run, mm
	lineGap     = 0.3  // vertical gap between the Reference and Value lines, mm
	fieldMargin = 1.27 // clearance between a text block and its own body, mm
	wireThick   = 0.6  // collision thickness of a wire segment, mm
	ncSize      = 1.5  // collision box side of a no_connect marker, mm
	pinSize     = 1.0  // collision box side of a pin tip, mm
	minExtent   = 0.6  // minimum extent given to a degenerate obstacle box, mm
	eps         = 0.01 // coordinate/area tolerance, mm
)

// fieldReach lists the extra distances, beyond fieldMargin, a text block may
// back off to find clear paper — nearest first. Two grid cells is the limit:
// further out and the text stops reading as this symbol's own.
var fieldReach = [3]float64{0, 2.54, 5.08}

// Autoplace repositions every visible Reference/Value field and flips
// colliding net labels so no text overlaps bodies, wires, labels or other
// text. Mutates sch in place. Returns the number of fields moved + labels
// flipped.
func Autoplace(sch *sexp.Schematic) (fieldsMoved, labelsFlipped int) {
	syms := sexp.ReadSymbols(sch)
	insts := instanceNodes(sch)
	if len(insts) != len(syms) {
		// instanceNodes mirrors ReadSymbols' filter, so a mismatch means the
		// file is shaped in a way we cannot pair safely: change nothing.
		return 0, 0
	}

	obs, _, labels := buildScene(sch, syms)

	for _, i := range symbolOrder(syms) {
		fieldsMoved += placeFields(insts[i], syms[i], &obs)
	}
	for _, i := range labelOrder(labels) {
		if flipLabel(&labels[i], obs) {
			labelsFlipped++
		}
	}
	return fieldsMoved, labelsFlipped
}

// box is an axis-aligned rectangle in schematic coordinates (Y down).
type box struct {
	x1, y1, x2, y2 float64
}

func (b box) overlap(o box) float64 {
	w := math.Min(b.x2, o.x2) - math.Max(b.x1, o.x1)
	h := math.Min(b.y2, o.y2) - math.Max(b.y1, o.y1)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

func (b box) contains(x, y float64) bool {
	return x >= b.x1-eps && x <= b.x2+eps && y >= b.y1-eps && y <= b.y2+eps
}

func (b box) union(o box) box {
	return box{
		math.Min(b.x1, o.x1), math.Min(b.y1, o.y1),
		math.Max(b.x2, o.x2), math.Max(b.y2, o.y2),
	}
}

// inflate gives a degenerate box a minimum extent. metrics.BodyBBox collapses
// a two-pin component to a zero-width line, which area-based scoring would
// read as free space.
func (b box) inflate() box {
	if b.x2-b.x1 < minExtent {
		c := (b.x1 + b.x2) / 2
		b.x1, b.x2 = c-minExtent/2, c+minExtent/2
	}
	if b.y2-b.y1 < minExtent {
		c := (b.y1 + b.y2) / 2
		b.y1, b.y2 = c-minExtent/2, c+minExtent/2
	}
	return b
}

func centeredBox(cx, cy, w, h float64) box {
	return box{cx - w/2, cy - h/2, cx + w/2, cy + h/2}
}

// overlapSum totals the area b shares with every obstacle, ignoring the
// obstacle at index skip (pass -1 to skip none). A text item that is itself
// registered as an obstacle must not score against its own box.
func overlapSum(b box, obs []box, skip int) float64 {
	total := 0.0
	for i, o := range obs {
		if i == skip {
			continue
		}
		total += b.overlap(o)
	}
	return total
}

func textWidth(s string) float64 {
	return charWidth*float64(len([]rune(s))) + textPadding
}

// labelBox estimates the rendered extent of a net label. The text reads away
// from the anchor along the label's rotation (0 → +x, 180 → −x, 90 → up,
// 270 → down) and is centred on the anchor in the perpendicular axis.
func labelBox(name string, x, y, rot float64) box {
	w, h := textWidth(name), lineHeight
	switch normalizeAngle(rot) {
	case 90:
		return box{x - h/2, y - w, x + h/2, y}
	case 180:
		return box{x - w, y - h/2, x, y + h/2}
	case 270:
		return box{x - h/2, y, x + h/2, y + w}
	default:
		return box{x, y - h/2, x + w, y + h/2}
	}
}

// labelRef is one (label ...) node together with the index of its box in the
// obstacle slice, so the label can be scored against everything but itself.
type labelRef struct {
	node   *sexp.Node
	name   string
	x, y   float64
	rot    float64
	obsIdx int
}

// buildScene collects every obstacle a piece of text must avoid. Symbol
// Reference/Value blocks are NOT included here: they are appended one by one
// as they are placed, so each block also avoids the blocks placed before it.
// The names slice runs parallel to obs and says what each rectangle IS, so a
// residual overlap can be reported as "over body U1" rather than a number.
// Autoplace itself only scores areas and ignores it.
func buildScene(sch *sexp.Schematic, syms []sexp.SchematicSymbol) ([]box, []string, []labelRef) {
	var obs []box
	var names []string
	add := func(b box, name string) {
		obs = append(obs, b)
		names = append(names, name)
	}

	for _, s := range syms {
		x1, y1, x2, y2 := metrics.BodyBBox(s)
		add(box{x1, y1, x2, y2}.inflate(), "body "+s.Reference)
		for _, p := range s.Pins {
			add(centeredBox(p.X, p.Y, pinSize, pinSize), "pin "+s.Reference+"."+p.Number)
		}
	}
	for _, w := range sch.Wires() {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		add(box{
			math.Min(ax, bx) - wireThick/2, math.Min(ay, by) - wireThick/2,
			math.Max(ax, bx) + wireThick/2, math.Max(ay, by) + wireThick/2,
		}, "wire")
	}
	var labels []labelRef
	for _, c := range sch.Root().Children {
		switch c.Head() {
		case "no_connect":
			if x, y, ok := atXY(c); ok {
				add(centeredBox(x, y, ncSize, ncSize), "no-connect")
			}
		case "label":
			x, y, ok := atXY(c)
			if !ok {
				continue
			}
			name := sexp.StringValue(c, 1)
			if name == "" {
				name = sexp.AtomValue(c, 1)
			}
			rot := normalizeAngle(atRot(c))
			labels = append(labels, labelRef{node: c, name: name, x: x, y: y, rot: rot, obsIdx: len(obs)})
			add(labelBox(name, x, y, rot), "label "+name)
		}
	}
	return obs, names, labels
}

// placeFields re-anchors one instance's visible Reference/Value block and
// appends the resulting rectangle to obs. Returns how many property nodes were
// actually rewritten (0 when the block was already good, which is what makes
// the pass idempotent).
func placeFields(inst *sexp.Node, sym sexp.SchematicSymbol, obs *[]box) int {
	lines := visibleFields(inst)
	if len(lines) == 0 {
		return 0
	}

	// KiCad renders a field at (field angle + symbol rotation): writing 0 on a
	// 90/270 symbol prints the text vertically. The compensating field angle
	// keeps the DISPLAYED text horizontal.
	compRot := 0.0
	if a := normalizeAngle(sym.Rotation); a == 90 || a == 270 {
		compRot = 90
	}

	// A block that already displays horizontally, centre-justified and hitting
	// nothing stays exactly where it is.
	if cur, ok := blockBounds(lines, sym.Rotation); ok && blockNormalized(lines, compRot) &&
		overlapSum(cur, *obs, -1) <= eps {
		*obs = append(*obs, cur)
		return 0
	}

	w := 0.0
	for _, p := range lines {
		if tw := textWidth(propText(p)); tw > w {
			w = tw
		}
	}
	h := float64(len(lines))*lineHeight + float64(len(lines)-1)*lineGap

	x1, y1, x2, y2 := metrics.BodyBBox(sym)
	best := bestCandidate(box{x1, y1, x2, y2}.inflate(), w, h, *obs)
	cx := sexp.Round2((best.x1 + best.x2) / 2)
	cy := sexp.Round2((best.y1 + best.y2) / 2)

	moved := 0
	top := cy - h/2 + lineHeight/2
	for i, p := range lines {
		if patchProp(p, cx, top+float64(i)*(lineHeight+lineGap), compRot) {
			moved++
		}
	}
	*obs = append(*obs, centeredBox(cx, cy, w, h))
	return moved
}

// bestCandidate picks the block position with the least obstacle overlap.
// Candidates are generated in preference order, and only a strictly better
// score displaces the incumbent, so ties resolve to the preferred side.
func bestCandidate(body box, w, h float64, obs []box) box {
	cands := fieldCandidates(body, w, h)
	best := cands[0]
	bestScore := overlapSum(best, obs, -1)
	for _, c := range cands[1:] {
		if s := overlapSum(c, obs, -1); s < bestScore-eps {
			best, bestScore = c, s
		}
	}
	return best
}

// fieldCandidates returns the block positions around a body: eight
// directions — right, top, left, bottom, then the four diagonals — at each of
// fieldReach's distances. For a vertical passive the first candidate puts the
// block to its right and for a horizontal one above it, which is the KiCad
// convention, and the caller only moves off a candidate for a strictly better
// score, so the conventional spot wins every tie.
//
// Distance is a candidate axis because direction alone is not enough: in a
// decoupling farm every one of the eight near positions lands on a
// neighbouring capacitor, and the pass could only pick the least bad overlap.
// One or two cells further out there is usually clear paper, which is what a
// person drawing this would use — while staying close enough that the text
// still reads as belonging to its own symbol.
func fieldCandidates(body box, w, h float64) []box {
	midX := (body.x1 + body.x2) / 2
	midY := (body.y1 + body.y2) / 2

	out := make([]box, 0, 8*len(fieldReach))
	for _, extra := range fieldReach {
		left := body.x1 - fieldMargin - extra - w/2
		right := body.x2 + fieldMargin + extra + w/2
		top := body.y1 - fieldMargin - extra - h/2
		bottom := body.y2 + fieldMargin + extra + h/2

		for _, c := range [8][2]float64{
			{right, midY}, {midX, top}, {left, midY}, {midX, bottom},
			{right, top}, {right, bottom}, {left, top}, {left, bottom},
		} {
			out = append(out, centeredBox(c[0], c[1], w, h))
		}
	}
	return out
}

// flipLabel rotates a colliding label around its anchor. The anchor is the
// electrical connection point and never moves; only the reading direction
// changes. Returns true when the node was rewritten.
func flipLabel(l *labelRef, obs []box) bool {
	bestRot := l.rot
	bestScore := overlapSum(labelBox(l.name, l.x, l.y, l.rot), obs, l.obsIdx)
	if bestScore <= eps {
		return false
	}
	for _, r := range [4]float64{0, 180, 90, 270} {
		if r == l.rot {
			continue
		}
		if s := overlapSum(labelBox(l.name, l.x, l.y, r), obs, l.obsIdx); s < bestScore-eps {
			bestScore, bestRot = s, r
		}
	}
	if bestRot == l.rot {
		return false
	}
	atN := sexp.FindList(l.node, "at")
	if atN == nil || len(atN.Children) < 3 {
		return false
	}
	for len(atN.Children) < 4 {
		atN.Children = append(atN.Children, sexp.Atom("0"))
	}
	atN.Children[3] = sexp.Atom(fmtCoord(bestRot))
	l.rot = bestRot
	obs[l.obsIdx] = labelBox(l.name, l.x, l.y, bestRot)
	return true
}

// visibleFields returns the property nodes forming the text block, top line
// first. Hidden properties are skipped, so a power symbol (Reference hidden)
// yields a single-line block holding only its Value.
func visibleFields(inst *sexp.Node) []*sexp.Node {
	var lines []*sexp.Node
	for _, name := range [2]string{"Reference", "Value"} {
		if p := findProp(inst, name); p != nil && !propHidden(p) {
			lines = append(lines, p)
		}
	}
	return lines
}

// blockBounds returns the rectangle the block currently occupies, honouring
// each line's DISPLAYED rotation — field angle plus symbol rotation, which is
// what KiCad draws (a vertical line swaps width and height). ok is false when
// a line has no usable (at ...).
func blockBounds(lines []*sexp.Node, symRot float64) (box, bool) {
	var b box
	for i, p := range lines {
		atN := sexp.FindList(p, "at")
		if atN == nil || len(atN.Children) < 3 {
			return box{}, false
		}
		w, h := textWidth(propText(p)), lineHeight
		if a := normalizeAngle(atRot(p) + symRot); a == 90 || a == 270 {
			w, h = h, w
		}
		lb := centeredBox(parseF(sexp.AtomValue(atN, 1)), parseF(sexp.AtomValue(atN, 2)), w, h)
		if i == 0 {
			b = lb
		} else {
			b = b.union(lb)
		}
	}
	return b, true
}

// blockNormalized reports whether every line already carries the compensating
// angle that displays horizontally and is centre-justified, i.e. whether the
// geometry model used here matches what KiCad will draw. Anything else is
// re-placed even when it happens to collide with nothing.
func blockNormalized(lines []*sexp.Node, compRot float64) bool {
	for _, p := range lines {
		if normalizeAngle(atRot(p)) != normalizeAngle(compRot) || findJustify(p) != nil {
			return false
		}
	}
	return true
}

// patchProp writes a field's anchor as (at x y rot) and drops any justify so
// the text is centred on it. Reports whether the node actually changed.
func patchProp(prop *sexp.Node, x, y, rot float64) bool {
	atN := sexp.FindList(prop, "at")
	if atN == nil || len(atN.Children) < 3 {
		return false
	}
	changed := false
	for len(atN.Children) < 4 {
		atN.Children = append(atN.Children, sexp.Atom("0"))
		changed = true
	}
	if math.Abs(parseF(sexp.AtomValue(atN, 1))-x) > eps {
		atN.Children[1] = sexp.Atom(fmtCoord(x))
		changed = true
	}
	if math.Abs(parseF(sexp.AtomValue(atN, 2))-y) > eps {
		atN.Children[2] = sexp.Atom(fmtCoord(y))
		changed = true
	}
	if normalizeAngle(parseF(sexp.AtomValue(atN, 3))) != normalizeAngle(rot) {
		atN.Children[3] = sexp.Atom(fmtCoord(rot))
		changed = true
	}
	if stripJustify(prop) {
		changed = true
	}
	return changed
}

func findProp(inst *sexp.Node, name string) *sexp.Node {
	for _, c := range inst.Children {
		if c.Head() == "property" && sexp.StringValue(c, 1) == name {
			return c
		}
	}
	return nil
}

func propText(prop *sexp.Node) string { return sexp.StringValue(prop, 2) }

// propHidden reports whether a property is hidden, accepting both the
// (hide yes) list form and the bare `hide` atom of older files.
func propHidden(prop *sexp.Node) bool {
	eff := sexp.FindList(prop, "effects")
	if eff == nil {
		return false
	}
	if h := sexp.FindList(eff, "hide"); h != nil {
		return sexp.AtomValue(h, 1) != "no"
	}
	for _, c := range eff.Children {
		if !c.IsList() && c.Value == "hide" {
			return true
		}
	}
	return false
}

func findJustify(prop *sexp.Node) *sexp.Node {
	eff := sexp.FindList(prop, "effects")
	if eff == nil {
		return nil
	}
	return sexp.FindList(eff, "justify")
}

func stripJustify(prop *sexp.Node) bool {
	eff := sexp.FindList(prop, "effects")
	if eff == nil {
		return false
	}
	kept := eff.Children[:0]
	removed := false
	for _, c := range eff.Children {
		if c.IsList() && c.Head() == "justify" {
			removed = true
			continue
		}
		kept = append(kept, c)
	}
	eff.Children = kept
	return removed
}

// instanceNodes returns the placed symbol instances in file order using
// exactly the filter sexp.ReadSymbols applies, so index i here matches index i
// of ReadSymbols' output.
func instanceNodes(sch *sexp.Schematic) []*sexp.Node {
	var out []*sexp.Node
	for _, n := range sch.Root().Children {
		if n.Head() != "symbol" {
			continue
		}
		if sexp.FindList(n, "lib_id") == nil || sexp.FindList(n, "at") == nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func symbolOrder(syms []sexp.SchematicSymbol) []int {
	idx := make([]int, len(syms))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := syms[idx[a]], syms[idx[b]]
		switch {
		case x.Reference != y.Reference:
			return x.Reference < y.Reference
		case x.Unit != y.Unit:
			return x.Unit < y.Unit
		case x.X != y.X:
			return x.X < y.X
		default:
			return x.Y < y.Y
		}
	})
	return idx
}

func labelOrder(labels []labelRef) []int {
	idx := make([]int, len(labels))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := labels[idx[a]], labels[idx[b]]
		switch {
		case x.name != y.name:
			return x.name < y.name
		case x.x != y.x:
			return x.x < y.x
		default:
			return x.y < y.y
		}
	})
	return idx
}

func atXY(n *sexp.Node) (x, y float64, ok bool) {
	atN := sexp.FindList(n, "at")
	if atN == nil || len(atN.Children) < 3 {
		return 0, 0, false
	}
	return parseF(sexp.AtomValue(atN, 1)), parseF(sexp.AtomValue(atN, 2)), true
}

func atRot(n *sexp.Node) float64 {
	atN := sexp.FindList(n, "at")
	if atN == nil {
		return 0
	}
	return parseF(sexp.AtomValue(atN, 3))
}

// normalizeAngle snaps an angle to the nearest of 0/90/180/270.
func normalizeAngle(a float64) float64 {
	q := math.Mod(math.Round(a/90)*90, 360)
	if q < 0 {
		q += 360
	}
	return q
}

func parseF(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func fmtCoord(v float64) string { return fmt.Sprintf("%.6g", v) }
