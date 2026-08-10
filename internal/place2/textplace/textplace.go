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
	// Pin numbers are printed along the pin line, roughly half a grid step in
	// from the tip. Measured on a rendered NE5532: pin "2" of a pin at
	// (25.40, 33.02) draws across x 26.25..27.03, y 31.33..32.60.
	pinNumberInset = 1.27
	pinNumberSize  = 1.6
	minExtent      = 0.6  // minimum extent given to a degenerate obstacle box, mm
	eps            = 0.01 // coordinate/area tolerance, mm
)

// fieldReach lists the extra distances, beyond fieldMargin, a text block may
// back off to find clear paper — nearest first. Two grid cells is the limit:
// further out and the text stops reading as this symbol's own.
var fieldReach = [3]float64{0, 2.54, 5.08}

// Side indices into each distance's group of eight candidates, in the order
// fieldCandidates emits them. Only the four cardinal sides are named: a row
// shares a cardinal side, never a diagonal.
const (
	sideRight  = 0
	sideTop    = 1
	sideLeft   = 2
	sideBottom = 3
)

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

	obs, obsNames, labels := buildScene(sch, syms)

	side := rowSides(syms, obs, insts)
	for _, i := range symbolOrder(syms) {
		fieldsMoved += placeFields(insts[i], syms[i], &obs, foreignBodies(syms, i), side[i])
	}
	for _, i := range labelOrder(labels) {
		if flipLabel(&labels[i], obs, obsNames) {
			labelsFlipped++
		}
	}
	// Second chance for labels still colliding: slide along their own wire.
	// Every point of the wire is the same electrical node, so the anchor may
	// move to wherever a horizontal box fits — which is how the edge labels
	// of a fan-out escape the corner where flipping alone only offers
	// "vertical or overlapping".
	for _, i := range labelOrder(labels) {
		if slideLabel(sch, &labels[i], obs, obsNames) {
			labelsFlipped++
		}
	}
	return fieldsMoved, labelsFlipped
}

// slideLabel moves a still-colliding label along the wire its anchor sits on,
// looking for the nearest spot where a HORIZONTAL box is completely clear.
// Only wires with an ENDPOINT exactly at the anchor are followed — that point
// identity is what guarantees the wire belongs to the label's own net. The
// wire is split at the new anchor so the netlist tracer sees an endpoint
// there (KiCad connects a label to the wire under it; our union-find joins
// by point identity).
func slideLabel(sch *sexp.Schematic, l *labelRef, obs []box, names []string) bool {
	colliding := overlapSumForLabel(labelBox(l.name, l.x, l.y, l.rot, l.justifyRight), l, obs, names) > eps
	vertical := normalizeAngle(l.rot) == 90 || normalizeAngle(l.rot) == 270
	if !colliding && !vertical {
		return false
	}
	const step = 1.27
	for _, w := range sch.Wires() {
		pts := sexp.FindList(w, "pts")
		if pts == nil {
			continue
		}
		var xy [][2]float64
		for _, c := range pts.Children {
			if c.Head() == "xy" {
				xy = append(xy, [2]float64{parseF(sexp.AtomValue(c, 1)), parseF(sexp.AtomValue(c, 2))})
			}
		}
		if len(xy) != 2 {
			continue
		}
		var from, to [2]float64
		switch {
		case math.Abs(xy[0][0]-l.x) < eps && math.Abs(xy[0][1]-l.y) < eps:
			from, to = xy[0], xy[1]
		case math.Abs(xy[1][0]-l.x) < eps && math.Abs(xy[1][1]-l.y) < eps:
			from, to = xy[1], xy[0]
		default:
			continue
		}
		length := math.Hypot(to[0]-from[0], to[1]-from[1])
		if length < 2*step {
			continue // nowhere to slide
		}
		ux, uy := (to[0]-from[0])/length, (to[1]-from[1])/length
		// Interior points only, nearest first: the text stays as close to the
		// pin it names as a clear spot allows. The far endpoint is excluded —
		// it is usually another pin or a junction.
		for d := step; d <= length-step+eps; d += step {
			nx, ny := sexp.SnapGrid(from[0]+ux*d), sexp.SnapGrid(from[1]+uy*d)
			for _, right := range [2]bool{false, true} {
				b := labelBox(l.name, nx, ny, 0, right)
				if overlapSumForLabel(b, l, obs, names) > eps {
					continue
				}
				atN := sexp.FindList(l.node, "at")
				if atN == nil || len(atN.Children) < 3 {
					return false
				}
				atN.Children[1] = sexp.Atom(fmtCoord(nx))
				atN.Children[2] = sexp.Atom(fmtCoord(ny))
				for len(atN.Children) < 4 {
					atN.Children = append(atN.Children, sexp.Atom("0"))
				}
				atN.Children[3] = sexp.Atom("0")
				setLabelJustify(l.node, right)
				sexp.SplitWiresAt(sch, nx, ny)
				l.x, l.y, l.rot, l.justifyRight = nx, ny, 0, right
				obs[l.obsIdx] = b
				return true
			}
		}
	}
	return false
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
// overlapSumForLabel is overlapSum minus the label's own net wire: a label
// lying along the wire it names is the convention, not a collision — the same
// exemption Collisions applies when counting. Scoring that overlap here was
// what made every on-wire label prefer the vertical orientation: horizontal
// runs ALONG the wire (big self-overlap), vertical only touches it.
func overlapSumForLabel(b box, l *labelRef, obs []box, names []string) float64 {
	total := 0.0
	for i, o := range obs {
		// obs grows as fields are placed; names does not. Anything past the
		// original scene is field text, which is never the label's own wire.
		if i == l.obsIdx || (i < len(names) && names[i] == "wire:"+l.name) {
			continue
		}
		total += b.overlap(o)
	}
	return total
}

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

// labelBox estimates where KiCad actually DRAWS a net label.
//
// The angle alone does not decide which way the text reads — the horizontal
// justification does. Measured by exporting SVG for all eight combinations
// and reading back the stroked-text extents:
//
//	                 justify left        justify right
//	angle 0 / 180    text runs +x        text runs −x
//	angle 90 / 270   text runs −y (up)   text runs +y (down)
//
// So the angle only picks the axis; 0 and 180 draw identically, as do 90 and
// 270. Believing the angle flipped the text is what let labels sit on top of
// pin numbers even after the pass thought it had moved them away.
//
// Perpendicular placement follows "justify … bottom": the text's baseline is
// the anchor, so a horizontal label occupies the line ABOVE its anchor.
func labelBox(name string, x, y, rot float64, justifyRight bool) box {
	w, h := textWidth(name), lineHeight
	if a := normalizeAngle(rot); a == 90 || a == 270 {
		if justifyRight {
			return box{x - h, y, x, y + w}
		}
		return box{x - h, y - w, x, y}
	}
	if justifyRight {
		return box{x - w, y - h, x, y}
	}
	return box{x, y - h, x + w, y}
}

// labelRef is one (label ...) node together with the index of its box in the
// obstacle slice, so the label can be scored against everything but itself.
type labelRef struct {
	node         *sexp.Node
	name         string
	x, y         float64
	rot          float64
	justifyRight bool // horizontal justification: what actually aims the text
	obsIdx       int
}

// labelJustifyRight reports whether a label node carries (justify right …).
func labelJustifyRight(n *sexp.Node) bool {
	effects := sexp.FindList(n, "effects")
	if effects == nil {
		return false
	}
	j := sexp.FindList(effects, "justify")
	if j == nil {
		return false
	}
	for i := 1; i < len(j.Children); i++ {
		if sexp.AtomValue(j, i) == "right" {
			return true
		}
	}
	return false
}

// setLabelJustify rewrites a label's horizontal justification, keeping the
// vertical part ("bottom") that KiCad writes alongside it.
func setLabelJustify(n *sexp.Node, right bool) {
	effects := sexp.FindList(n, "effects")
	if effects == nil {
		return
	}
	horizontal := "left"
	if right {
		horizontal = "right"
	}
	for i, c := range effects.Children {
		if c.Head() != "justify" {
			continue
		}
		vertical := ""
		for k := 1; k < len(c.Children); k++ {
			if v := sexp.AtomValue(c, k); v == "top" || v == "bottom" {
				vertical = v
			}
		}
		if vertical == "" {
			effects.Children[i] = sexp.List(sexp.Atom("justify"), sexp.Atom(horizontal))
			return
		}
		effects.Children[i] = sexp.List(sexp.Atom("justify"), sexp.Atom(horizontal), sexp.Atom(vertical))
		return
	}
	effects.Children = append(effects.Children,
		sexp.List(sexp.Atom("justify"), sexp.Atom(horizontal), sexp.Atom("bottom")))
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

			// KiCad prints the pin NUMBER along the pin line, between the tip
			// and the body. It is text like any other and was missing from
			// this scene entirely, which is how net labels ended up written
			// straight across it.
			dx, dy := p.DirDelta()
			add(centeredBox(p.X-dx*pinNumberInset, p.Y-dy*pinNumberInset, pinNumberSize, pinNumberSize),
				"pin number "+s.Reference+"."+p.Number)
		}
	}
	// Wires carry their net name so a label can tell its OWN wire from a
	// stranger's: sitting on the wire it names is where KiCad puts a label,
	// while sitting on someone else's is a genuine misread.
	netOf := sexp.TracePointNets(sch)
	for _, w := range sch.Wires() {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		name := "wire"
		if n := netOf[[2]float64{sexp.Round2(ax), sexp.Round2(ay)}]; n != "" {
			name = "wire:" + n
		}
		add(box{
			math.Min(ax, bx) - wireThick/2, math.Min(ay, by) - wireThick/2,
			math.Max(ax, bx) + wireThick/2, math.Max(ay, by) + wireThick/2,
		}, name)
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
			right := labelJustifyRight(c)
			labels = append(labels, labelRef{node: c, name: name, x: x, y: y, rot: rot, justifyRight: right, obsIdx: len(obs)})
			add(labelBox(name, x, y, rot, right), "label "+name)
		}
	}
	return obs, names, labels
}

// placeFields re-anchors one instance's visible Reference/Value block and
// appends the resulting rectangle to obs. Returns how many property nodes were
// actually rewritten (0 when the block was already good, which is what makes
// the pass idempotent).
func placeFields(inst *sexp.Node, sym sexp.SchematicSymbol, obs *[]box, foreign []box, forcedSide int) int {
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
	body := box{x1, y1, x2, y2}.inflate()
	best := bestCandidate(body, w, h, *obs, foreign)
	if forcedSide >= 0 {
		if c, ok := clearCandidateOnSide(body, w, h, *obs, forcedSide); ok {
			best = c
		}
	}
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
//
// A position is only considered when the text still reads as belonging to its
// own symbol: clear of every foreign body, and closer to its own than to any
// other. Without that rule, backing away from a crowded neighbour parks
// "C2 100n" midway between two capacitors, where it is clean by area and
// useless to a reader, who cannot tell which part it labels. Ownership beats
// tidiness; only if no candidate qualifies does the lowest-overlap one win.
func bestCandidate(body box, w, h float64, obs []box, foreign []box) box {
	cands := fieldCandidates(body, w, h)

	best, bestScore := box{}, 0.0
	found := false
	fallback, fallbackScore := cands[0], overlapSum(cands[0], obs, -1)

	for _, c := range cands {
		s := overlapSum(c, obs, -1)
		if s < fallbackScore-eps {
			fallback, fallbackScore = c, s
		}
		if !ownsText(c, body, foreign) {
			continue
		}
		if !found || s < bestScore-eps {
			best, bestScore, found = c, s, true
		}
	}
	if found {
		return best
	}
	return fallback
}

// ownsText reports whether a text block at c unambiguously belongs to the
// symbol whose body is `body`: it touches no foreign body, and its centre is
// nearer to its own body than to any other.
func ownsText(c, body box, foreign []box) bool {
	cx, cy := (c.x1+c.x2)/2, (c.y1+c.y2)/2
	own := boxDistance(body, cx, cy)
	for _, f := range foreign {
		if c.overlap(f) > eps {
			return false
		}
		if boxDistance(f, cx, cy) < own-eps {
			return false
		}
	}
	return true
}

// boxDistance is the distance from a point to a rectangle (0 when inside).
func boxDistance(b box, px, py float64) float64 {
	dx := math.Max(math.Max(b.x1-px, px-b.x2), 0)
	dy := math.Max(math.Max(b.y1-py, py-b.y2), 0)
	return math.Hypot(dx, dy)
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
// A label has exactly four distinguishable orientations, not eight: the angle
// picks the axis and the justification picks the direction along it, so
// (0, right) and (180, right) draw the same text in the same place. Rotating
// without setting the justification — which is what this pass used to do —
// moves nothing at all.
var labelOrientations = [4]struct {
	rot   float64
	right bool
}{
	{0, false},  // reads rightwards
	{0, true},   // reads leftwards
	{90, false}, // reads upwards
	{90, true},  // reads downwards
}

func flipLabel(l *labelRef, obs []box, names []string) bool {
	current := overlapSumForLabel(labelBox(l.name, l.x, l.y, l.rot, l.justifyRight), l, obs, names)
	if current <= eps {
		// Clean, but vertical: take a horizontal orientation at the same
		// anchor if one is JUST as clean. A vertical label that cleared is
		// still the thing a reviewer picks out as machine-drawn.
		if a := normalizeAngle(l.rot); a != 90 && a != 270 {
			return false
		}
		for _, o := range labelOrientations {
			if o.rot != 0 {
				continue
			}
			if overlapSumForLabel(labelBox(l.name, l.x, l.y, o.rot, o.right), l, obs, names) <= eps {
				return applyLabelOrientation(l, obs, o.rot, o.right)
			}
		}
		return false
	}
	// Horizontal text is preferred, not merely tied: a vertical label reads
	// worse and is the one thing a reviewer said "gives away the machine" on
	// an otherwise clean sheet. So the vertical orientations only win when no
	// horizontal one clears the overlap completely — least-overlap alone used
	// to turn the edge labels of a fan-out sideways while a clean horizontal
	// spot existed.
	score := func(rot float64, right bool) float64 {
		if rot == l.rot && right == l.justifyRight {
			return current
		}
		return overlapSumForLabel(labelBox(l.name, l.x, l.y, rot, right), l, obs, names)
	}
	bestRot, bestRight, bestScore := l.rot, l.justifyRight, current
	pick := func(rot float64, right bool, s float64) {
		if s < bestScore-eps {
			bestScore, bestRot, bestRight = s, rot, right
		}
	}
	for _, o := range labelOrientations {
		if o.rot != 0 {
			continue
		}
		pick(o.rot, o.right, score(o.rot, o.right))
	}
	if bestScore > eps {
		// No horizontal clears. A vertical may take over, but only by earning
		// it: at least halving the overlap. A near-tie stays horizontal —
		// equal ugliness reads better lying down.
		for _, o := range labelOrientations {
			if o.rot == 0 {
				continue
			}
			if s := score(o.rot, o.right); s <= eps || s < bestScore/2-eps {
				pick(o.rot, o.right, s)
			}
		}
	}
	if bestRot == l.rot && bestRight == l.justifyRight {
		return false
	}
	return applyLabelOrientation(l, obs, bestRot, bestRight)
}

// applyLabelOrientation rewrites a label node's angle and justification and
// refreshes its obstacle box.
func applyLabelOrientation(l *labelRef, obs []box, rot float64, right bool) bool {
	atN := sexp.FindList(l.node, "at")
	if atN == nil || len(atN.Children) < 3 {
		return false
	}
	for len(atN.Children) < 4 {
		atN.Children = append(atN.Children, sexp.Atom("0"))
	}
	atN.Children[3] = sexp.Atom(fmtCoord(rot))
	setLabelJustify(l.node, right)
	l.rot, l.justifyRight = rot, right
	obs[l.obsIdx] = labelBox(l.name, l.x, l.y, rot, right)
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

// foreignBodies returns the body rectangles of every symbol except the one at
// index skip — what a text block must not be mistaken for belonging to.
// Power symbols are included: a reference parked on a GND symbol reads as
// that symbol's own label just as badly.
func foreignBodies(syms []sexp.SchematicSymbol, skip int) []box {
	out := make([]box, 0, len(syms)-1)
	for i, s := range syms {
		if i == skip {
			continue
		}
		x1, y1, x2, y2 := metrics.BodyBBox(s)
		out = append(out, box{x1, y1, x2, y2}.inflate())
	}
	return out
}

// --- rows of passives -----------------------------------------------------

// A decoupling farm is read as a row, and a row whose labels alternate sides
// reads as machine output: three references to the left of their capacitor and
// the fourth above it, because that one happened to have room. Humans put them
// all on the same side.
//
// rowSides returns, per symbol index, the side every member of its row should
// use, or -1 for "choose freely". A shared side is only imposed when it is
// clear for EVERY member — consistency is not worth buying with an overlap —
// so a row hemmed in on all sides falls back to per-symbol choice.
func rowSides(syms []sexp.SchematicSymbol, obs []box, insts []*sexp.Node) map[int]int {
	out := make(map[int]int, len(syms))
	for i := range syms {
		out[i] = -1
	}
	for _, row := range passiveRows(syms) {
		for _, s := range [4]int{sideRight, sideTop, sideLeft, sideBottom} {
			if !sideClearForRow(row, s, syms, obs, insts) {
				continue
			}
			for _, i := range row {
				out[i] = s
			}
			break
		}
	}
	return out
}

// passiveRows groups two-pin symbols that stand side by side on one horizontal
// band. Two pins keeps it to passives — an IC and a crystal at the same height
// are not a row anyone reads as one — and the gap limit keeps separate clusters
// apart. Rows are returned in symbol order, each sorted left to right.
func passiveRows(syms []sexp.SchematicSymbol) [][]int {
	const maxGap = 15.0 // mm — a hair over the 5-cell farm spacing

	var idx []int
	for i, s := range syms {
		if len(s.Pins) == 2 {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		if syms[idx[a]].Y != syms[idx[b]].Y {
			return syms[idx[a]].Y < syms[idx[b]].Y
		}
		return syms[idx[a]].X < syms[idx[b]].X
	})

	var rows [][]int
	var cur []int
	for _, i := range idx {
		if len(cur) == 0 {
			cur = []int{i}
			continue
		}
		prev := syms[cur[len(cur)-1]]
		s := syms[i]
		if math.Abs(s.Y-prev.Y) < eps && s.X-prev.X > 0 && s.X-prev.X <= maxGap {
			cur = append(cur, i)
			continue
		}
		if len(cur) >= 3 {
			rows = append(rows, cur)
		}
		cur = []int{i}
	}
	if len(cur) >= 3 {
		rows = append(rows, cur)
	}
	return rows
}

// sideClearForRow reports whether every member of the row can put its text on
// the given side without overlapping anything.
func sideClearForRow(row []int, side int, syms []sexp.SchematicSymbol, obs []box, insts []*sexp.Node) bool {
	placed := append([]box(nil), obs...)
	for _, i := range row {
		lines := visibleFields(insts[i])
		if len(lines) == 0 {
			continue
		}
		w, h := blockExtent(lines)
		x1, y1, x2, y2 := metrics.BodyBBox(syms[i])
		c, ok := clearCandidateOnSide(box{x1, y1, x2, y2}.inflate(), w, h, placed, side)
		if !ok {
			return false
		}
		placed = append(placed, c)
	}
	return true
}

// clearCandidateOnSide returns the nearest position on one side of the body
// that overlaps nothing, trying each of fieldReach's distances in turn.
func clearCandidateOnSide(body box, w, h float64, obs []box, side int) (box, bool) {
	cands := fieldCandidates(body, w, h)
	for step := range fieldReach {
		c := cands[step*8+side]
		if overlapSum(c, obs, -1) <= eps {
			return c, true
		}
	}
	return box{}, false
}

// blockExtent is the width and height a field block will occupy.
func blockExtent(lines []*sexp.Node) (w, h float64) {
	for _, p := range lines {
		if tw := textWidth(propText(p)); tw > w {
			w = tw
		}
	}
	return w, float64(len(lines))*lineHeight + float64(len(lines)-1)*lineGap
}
