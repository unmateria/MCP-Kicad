// Package weld upgrades label-pair connections into real wires.
//
// Humans read wires, not tag pairs. A schematic where a connection is drawn as
// two same-name net labels two centimetres apart is electrically correct and
// visually useless, so this pass walks every net that is currently held
// together only by its labels and, wherever a clean orthogonal corridor exists
// between two of its wire-connected islands, draws the wire and drops the now
// redundant labels (one is kept as documentation of the net name).
//
// The pass never gambles. A candidate route is committed only when the
// geometric quality gate (internal/place2/gate) reports no NEW violation
// against the pre-weld baseline; otherwise every node it added is removed and
// the next candidate is tried. Power rails are never welded — the per-pin
// power-symbol policy owns them.
//
// Determinism: nets, points, components, component pairs, point pairs and
// route candidates are all visited in sorted order, so identical input always
// produces identical output and a second call is a no-op.
package weld

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// eps is the coordinate tolerance (mm), consistent with sexp's 2-decimal
// rounding and gate's own eps.
const eps = 0.01

// Beauty filters. A candidate that fails any of them is rejected before the
// (much more expensive) gate validation even runs.
const (
	maxDetourRatio = 1.5  // route length / manhattan distance
	minSegLen      = 2.54 // mm — no stubby segments, except a lone straight run
)

// How far a weld may reach. What makes a long wire hard to follow is not its
// length in millimetres but its length RELATIVE to the drawing: 65 mm across
// a full A4 is an ordinary inter-block run, while the same wire on a
// thumbnail-sized circuit dominates it. A fixed 50.8 mm ceiling therefore
// refused perfectly clean L-shaped runs between blocks — the pull-up to its
// MCU pin — and left a pair of tags where a person would have drawn a line.
//
// The reach is a fraction of the content's diagonal, floored so that small
// schematics still get the old allowance. Every other guarantee is unchanged:
// the route must be straight/L/Z, near-direct (maxDetourRatio), clear of
// bodies, and accepted by the gate.
// Half the diagonal is the line: beyond that a wire dominates the drawing and
// a tag pair genuinely reads better; below it, both ends are still in one
// eyeful and a person would draw the wire — which is what a review of the
// I2C pull-ups said about a 65 mm run this pass was refusing.
const (
	weldReachFraction = 0.5
	minWeldReach      = 50.8 // mm — 20 grid cells
)

// Route classes, in preference order.
const (
	classStraight = iota
	classL
	classZ
)

// Result reports what one Weld pass changed.
type Result struct {
	Welded        int
	LabelsRemoved int
	Notes         []string
}

// String renders the one-line tool summary.
func (r Result) String() string {
	if r.Welded == 0 && r.LabelsRemoved == 0 {
		return "weld: 0 label pair(s) upgraded to wires"
	}
	return fmt.Sprintf("weld: %d label pair(s) upgraded to wires, %d label(s) removed",
		r.Welded, r.LabelsRemoved)
}

// Weld upgrades label-pair connections into real wires wherever a clean
// orthogonal corridor exists. Mutates sch. Humans read wires, not tag pairs.
func Weld(sch *sexp.Schematic) Result {
	var res Result
	if sch == nil {
		return res
	}
	maxLen := contentReach(sch)
	for _, name := range weldableNetNames(sch) {
		welded, removed, notes := weldNet(sch, name, maxLen)
		res.Welded += welded
		res.LabelsRemoved += removed
		res.Notes = append(res.Notes, notes...)
	}
	return res
}

// pt is a 2-decimal-rounded schematic coordinate, the same key space used by
// sexp.TracePointNets.
type pt = [2]float64

// segment is one axis-aligned wire segment.
type segment struct{ ax, ay, bx, by float64 }

// component is one island of a net: the set of points joined by wires alone,
// ignoring labels and the implicit union of same-rail power symbols.
type component struct {
	key   pt   // lexicographically smallest member — stable identity
	ports []pt // points a new wire may attach to (labels, pins, free wire ends)
}

// route is a candidate orthogonal path between two points.
type route struct {
	class  int
	pts    []pt
	length float64
}

// weldableNetNames returns the nets eligible for welding, sorted by name.
// Power rails are excluded: one power symbol per pin is the project's policy
// and drawing rail wires would fight it.
func weldableNetNames(sch *sexp.Schematic) []string {
	var names []string
	seen := make(map[string]bool)
	for _, net := range sexp.TraceNets(sch) { // TraceNets already sorts by name
		if net.Dangling || net.Name == "" || seen[net.Name] || isRail(net) {
			continue
		}
		seen[net.Name] = true
		names = append(names, net.Name)
	}
	return names
}

// isRail reports whether a net is held by power symbols.
func isRail(net sexp.Net) bool {
	for _, p := range net.Pins {
		if isPowerLibID(p.LibID) {
			return true
		}
	}
	return false
}

func isPowerLibID(libID string) bool {
	return strings.HasPrefix(libID, "power:") || libID == "Device:PWR_FLAG"
}

// weldNet welds one net until it is a single island or no candidate survives,
// then prunes its now-redundant labels.
func weldNet(sch *sexp.Schematic, netName string, maxLen float64) (welded, removed int, notes []string) {
	comps := componentsOf(sch, netName)
	if len(comps) < 2 {
		return 0, 0, nil // already one island — nothing this pass owns
	}
	for len(comps) > 1 {
		note, ok := weldOnce(sch, netName, comps, maxLen)
		if !ok {
			break
		}
		welded++
		notes = append(notes, note)
		comps = componentsOf(sch, netName)
	}
	if len(comps) == 1 {
		if n := pruneLabels(sch, netName); n > 0 {
			removed = n
			notes = append(notes, fmt.Sprintf("%s: dropped %d redundant label(s)", netName, n))
		}
	}
	return welded, removed, notes
}

// weldOnce tries every component pair (closest first) and commits the first
// candidate route the gate accepts.
func weldOnce(sch *sexp.Schematic, netName string, comps []component, maxLen float64) (string, bool) {
	baseline := len(gate.Check(sch))
	bodies := bodyBoxes(sch)
	segs := wireSegments(sch)
	netOf := sexp.TracePointNets(sch)
	junctions := junctionPoints(sch)

	for _, cp := range orderedComponentPairs(comps) {
		for _, pp := range orderedPointPairs(comps[cp.i], comps[cp.j]) {
			for _, r := range routes(pp.a, pp.b) {
				if !acceptable(r, maxLen) || !clearOfBodies(r, bodies) {
					continue
				}
				if !commit(sch, r, netName, netOf, segs, junctions, baseline) {
					continue
				}
				return fmt.Sprintf("%s: welded (%.2f, %.2f) -> (%.2f, %.2f) as %s, %.2f mm",
					netName, pp.a[0], pp.a[1], pp.b[0], pp.b[1], className(r.class), r.length), true
			}
		}
	}
	return "", false
}

// commit writes the route's wires (plus any junction where an endpoint lands
// mid-wire on the same net) and keeps them only if the gate stays as clean as
// it was. On rejection every added node is removed again.
func commit(sch *sexp.Schematic, r route, netName string, netOf map[pt]string,
	segs []segment, junctions map[pt]bool, baseline int) bool {

	var added []*sexp.Node
	for i := 0; i+1 < len(r.pts); i++ {
		w := sexp.NewWire(r.pts[i][0], r.pts[i][1], r.pts[i+1][0], r.pts[i+1][1])
		sch.AddWire(w)
		added = append(added, w)
	}
	for _, end := range []pt{r.pts[0], r.pts[len(r.pts)-1]} {
		if junctions[end] || !landsMidWire(end, netName, netOf, segs) {
			continue
		}
		j := sexp.NewJunction(end[0], end[1])
		sch.AddJunction(j)
		added = append(added, j)
	}
	if len(gate.Check(sch)) > baseline {
		removeNodes(sch, added)
		return false
	}
	return true
}

// landsMidWire reports whether p sits strictly inside an existing wire of the
// same net — the one case where the new attachment needs a junction dot.
func landsMidWire(p pt, netName string, netOf map[pt]string, segs []segment) bool {
	for _, s := range segs {
		a, b := roundPt(s.ax, s.ay), roundPt(s.bx, s.by)
		if netOf[a] != netName || p == a || p == b {
			continue
		}
		if pointOnSegment(p, s) {
			return true
		}
	}
	return false
}

// --- component extraction -------------------------------------------------

// componentsOf partitions a net's points into wire-connected islands. Labels
// and the implicit power-symbol union are deliberately NOT followed: they are
// exactly the connections this package exists to replace.
func componentsOf(sch *sexp.Schematic, netName string) []component {
	netOf := sexp.TracePointNets(sch)
	pts := netPoints(netOf, netName)
	if len(pts) == 0 {
		return nil
	}
	segs := wireSegments(sch)

	uf := newUnionFind()
	for _, p := range pts {
		uf.find(p)
	}
	for _, s := range segs {
		a, b := roundPt(s.ax, s.ay), roundPt(s.bx, s.by)
		if netOf[a] != netName {
			continue
		}
		uf.union(a, b)
		// A point sitting on a wire's interior is electrically on that wire.
		for _, p := range pts {
			if pointOnSegment(p, s) {
				uf.union(p, a)
			}
		}
	}

	groups := make(map[pt][]pt)
	var roots []pt
	seenRoot := make(map[pt]bool)
	for _, p := range pts { // pts is sorted, so group order and content are stable
		r := uf.find(p)
		if !seenRoot[r] {
			seenRoot[r] = true
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], p)
	}

	isPort := portPredicate(sch, segs)
	comps := make([]component, 0, len(roots))
	for _, r := range roots {
		members := groups[r]
		c := component{key: members[0]}
		for _, m := range members {
			if isPort(m) {
				c.ports = append(c.ports, m)
			}
		}
		if len(c.ports) == 0 {
			c.ports = members
		}
		comps = append(comps, c)
	}
	return comps
}

// netPoints returns every point attributed to netName, sorted.
func netPoints(netOf map[pt]string, netName string) []pt {
	pts := make([]pt, 0, len(netOf))
	for p, name := range netOf {
		if name == netName {
			pts = append(pts, p)
		}
	}
	sort.Slice(pts, func(i, j int) bool { return ptLess(pts[i], pts[j]) })
	return pts
}

// portPredicate builds the test for "a new wire may attach here": net label
// anchors, symbol pins, and wire endpoints with nothing else already attached.
func portPredicate(sch *sexp.Schematic, segs []segment) func(pt) bool {
	labels := make(map[pt]bool)
	for _, c := range sch.Root().Children {
		if c.Head() != "label" {
			continue
		}
		if x, y, ok := labelAt(c); ok {
			labels[roundPt(x, y)] = true
		}
	}
	pins := make(map[pt]bool)
	for _, sym := range sexp.ReadSymbols(sch) {
		for _, p := range sym.Pins {
			pins[roundPt(p.X, p.Y)] = true
		}
	}
	degree := make(map[pt]int)
	for _, s := range segs {
		degree[roundPt(s.ax, s.ay)]++
		degree[roundPt(s.bx, s.by)]++
	}
	return func(p pt) bool { return labels[p] || pins[p] || degree[p] == 1 }
}

// --- candidate ordering ---------------------------------------------------

type compPair struct {
	i, j int
	d    float64
	a, b pt
}

type pointPair struct {
	a, b pt
	d    float64
}

// orderedComponentPairs sorts pairs by their closest-port manhattan distance,
// ties broken by the coordinates of that closest pair.
func orderedComponentPairs(comps []component) []compPair {
	var out []compPair
	for i := 0; i < len(comps); i++ {
		for j := i + 1; j < len(comps); j++ {
			pairs := orderedPointPairs(comps[i], comps[j])
			if len(pairs) == 0 {
				continue
			}
			out = append(out, compPair{i: i, j: j, d: pairs[0].d, a: pairs[0].a, b: pairs[0].b})
		}
	}
	sort.SliceStable(out, func(x, y int) bool {
		if math.Abs(out[x].d-out[y].d) > eps {
			return out[x].d < out[y].d
		}
		if out[x].a != out[y].a {
			return ptLess(out[x].a, out[y].a)
		}
		return ptLess(out[x].b, out[y].b)
	})
	return out
}

// orderedPointPairs enumerates every port-to-port combination of two
// components, closest first.
func orderedPointPairs(a, b component) []pointPair {
	out := make([]pointPair, 0, len(a.ports)*len(b.ports))
	for _, pa := range a.ports {
		for _, pb := range b.ports {
			out = append(out, pointPair{a: pa, b: pb, d: manhattan(pa, pb)})
		}
	}
	sort.SliceStable(out, func(x, y int) bool {
		if math.Abs(out[x].d-out[y].d) > eps {
			return out[x].d < out[y].d
		}
		if out[x].a != out[y].a {
			return ptLess(out[x].a, out[y].a)
		}
		return ptLess(out[x].b, out[y].b)
	})
	return out
}

// routes enumerates the candidate paths from a to b: the straight run when the
// points share an axis, otherwise the two Ls followed by the Zs with a single
// intermediate break in X or in Y at 1/2, 1/3 and 2/3 of the span. The result
// is ordered straight > L > Z, shortest first inside each class.
func routes(a, b pt) []route {
	ax, ay, bx, by := a[0], a[1], b[0], b[1]
	sameX, sameY := near(ax, bx), near(ay, by)
	if sameX && sameY {
		return nil
	}
	if sameX || sameY {
		return []route{mkRoute(classStraight, a, b)}
	}
	out := []route{
		mkRoute(classL, a, pt{bx, ay}, b),
		mkRoute(classL, a, pt{ax, by}, b),
	}
	for _, t := range []float64{0.5, 1.0 / 3.0, 2.0 / 3.0} {
		mx := sexp.SnapGrid(ax + (bx-ax)*t)
		out = append(out, mkRoute(classZ, a, pt{mx, ay}, pt{mx, by}, b))
		my := sexp.SnapGrid(ay + (by-ay)*t)
		out = append(out, mkRoute(classZ, a, pt{ax, my}, pt{bx, my}, b))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].class != out[j].class {
			return out[i].class < out[j].class
		}
		return out[i].length < out[j].length-eps
	})
	return out
}

func mkRoute(class int, pts ...pt) route {
	r := route{class: class, pts: pts}
	for i := 0; i+1 < len(pts); i++ {
		r.length += segLen(pts[i], pts[i+1])
	}
	return r
}

// acceptable applies the beauty filters: bounded length, bounded detour, no
// stubby segments (a lone straight run is exempt), every vertex on the KiCad
// connection grid and every segment axis-aligned.
func acceptable(r route, maxLen float64) bool {
	if len(r.pts) < 2 {
		return false
	}
	man := manhattan(r.pts[0], r.pts[len(r.pts)-1])
	if man <= eps || r.length > maxLen+eps || r.length > man*maxDetourRatio+eps {
		return false
	}
	single := len(r.pts) == 2
	for i := range r.pts {
		if !onGrid(r.pts[i]) {
			return false
		}
		if i+1 >= len(r.pts) {
			break
		}
		a, b := r.pts[i], r.pts[i+1]
		if !near(a[0], b[0]) && !near(a[1], b[1]) {
			return false // diagonal — this codebase only draws orthogonal wires
		}
		if !single && segLen(a, b) < minSegLen-eps {
			return false
		}
	}
	return true
}

// clearOfBodies rejects a route that cuts through any symbol body. Cheaper
// than the apply/gate/revert cycle, and catches the common blocker.
func clearOfBodies(r route, bodies [][4]float64) bool {
	for i := 0; i+1 < len(r.pts); i++ {
		a, b := r.pts[i], r.pts[i+1]
		for _, box := range bodies {
			if sexp.SegmentCrossesBox(a[0], a[1], b[0], b[1], box[0], box[1], box[2], box[3]) {
				return false
			}
		}
	}
	return true
}

// --- label pruning --------------------------------------------------------

// pruneLabels removes every label of netName except the leftmost one (ties on
// x broken by the smaller y), which stays as documentation of the net name.
// Only called once the net is a single wire-connected island, so connectivity
// is carried by the wires alone.
func pruneLabels(sch *sexp.Schematic, netName string) int {
	type entry struct {
		node *sexp.Node
		x, y float64
	}
	var found []entry
	for _, c := range sch.Root().Children {
		if c.Head() != "label" || labelName(c) != netName {
			continue
		}
		x, y, ok := labelAt(c)
		if !ok {
			continue
		}
		found = append(found, entry{node: c, x: x, y: y})
	}
	if len(found) < 2 {
		return 0
	}
	keep := 0
	for i := 1; i < len(found); i++ {
		if found[i].x < found[keep].x-eps ||
			(math.Abs(found[i].x-found[keep].x) <= eps && found[i].y < found[keep].y-eps) {
			keep = i
		}
	}
	doomed := make([]*sexp.Node, 0, len(found)-1)
	for i, e := range found {
		if i != keep {
			doomed = append(doomed, e.node)
		}
	}
	removeNodes(sch, doomed)
	return len(doomed)
}

// --- schematic helpers ----------------------------------------------------

func wireSegments(sch *sexp.Schematic) []segment {
	var segs []segment
	for _, w := range sch.Wires() {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		segs = append(segs, segment{ax: ax, ay: ay, bx: bx, by: by})
	}
	return segs
}

func bodyBoxes(sch *sexp.Schematic) [][4]float64 {
	var out [][4]float64
	for _, sym := range sexp.ReadSymbols(sch) {
		if isPowerLibID(sym.LibID) {
			continue
		}
		x1, y1, x2, y2 := metrics.BodyBBox(sym)
		out = append(out, [4]float64{x1, y1, x2, y2})
	}
	return out
}

func junctionPoints(sch *sexp.Schematic) map[pt]bool {
	set := make(map[pt]bool)
	for _, c := range sch.Root().Children {
		if c.Head() != "junction" {
			continue
		}
		atN := sexp.FindList(c, "at")
		if atN == nil {
			continue
		}
		set[roundPt(parseF(sexp.AtomValue(atN, 1)), parseF(sexp.AtomValue(atN, 2)))] = true
	}
	return set
}

func labelName(n *sexp.Node) string {
	if v := sexp.StringValue(n, 1); v != "" {
		return v
	}
	return sexp.AtomValue(n, 1)
}

func labelAt(n *sexp.Node) (x, y float64, ok bool) {
	atN := sexp.FindList(n, "at")
	if atN == nil || len(atN.Children) < 3 {
		return 0, 0, false
	}
	return parseF(sexp.AtomValue(atN, 1)), parseF(sexp.AtomValue(atN, 2)), true
}

// removeNodes drops the given nodes from the schematic root by identity.
func removeNodes(sch *sexp.Schematic, nodes []*sexp.Node) {
	if len(nodes) == 0 {
		return
	}
	doomed := make(map[*sexp.Node]bool, len(nodes))
	for _, n := range nodes {
		doomed[n] = true
	}
	kids := sch.Root().Children
	kept := kids[:0]
	for _, c := range kids {
		if doomed[c] {
			continue
		}
		kept = append(kept, c)
	}
	sch.Root().Children = kept
}

// --- geometry helpers -----------------------------------------------------

func near(a, b float64) bool { return math.Abs(a-b) < eps }

func roundPt(x, y float64) pt { return pt{sexp.Round2(x), sexp.Round2(y)} }

func ptLess(a, b pt) bool {
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	return a[1] < b[1]
}

func manhattan(a, b pt) float64 {
	return math.Abs(a[0]-b[0]) + math.Abs(a[1]-b[1])
}

func segLen(a, b pt) float64 {
	return math.Hypot(b[0]-a[0], b[1]-a[1])
}

// onGrid guards against sexp.NewWire's automatic 1.27 mm snap silently pulling
// a wire end off the pin or label it was meant to touch.
func onGrid(p pt) bool {
	return near(p[0], sexp.SnapGrid(p[0])) && near(p[1], sexp.SnapGrid(p[1]))
}

func pointOnSegment(p pt, s segment) bool {
	if near(s.ay, s.by) {
		lo, hi := math.Min(s.ax, s.bx), math.Max(s.ax, s.bx)
		return near(p[1], s.ay) && p[0] >= lo-eps && p[0] <= hi+eps
	}
	if near(s.ax, s.bx) {
		lo, hi := math.Min(s.ay, s.by), math.Max(s.ay, s.by)
		return near(p[0], s.ax) && p[1] >= lo-eps && p[1] <= hi+eps
	}
	return false
}

func className(class int) string {
	switch class {
	case classStraight:
		return "straight"
	case classL:
		return "L"
	default:
		return "Z"
	}
}

func parseF(s string) float64 {
	var v float64
	_, _ = fmt.Sscanf(s, "%f", &v)
	return v
}

// --- union-find over 2D points --------------------------------------------

type unionFind struct{ parent map[pt]pt }

func newUnionFind() *unionFind { return &unionFind{parent: make(map[pt]pt)} }

func (u *unionFind) find(p pt) pt {
	root, ok := u.parent[p]
	if !ok {
		u.parent[p] = p
		return p
	}
	if root == p {
		return p
	}
	r := u.find(root)
	u.parent[p] = r
	return r
}

func (u *unionFind) union(a, b pt) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}

// contentReach is how far a weld may run on this particular sheet: a fraction
// of the drawing's own diagonal, never less than minWeldReach. Scaling with
// the content is what makes the rule mean "a wire must not dominate the
// drawing" rather than "a wire must be under 50 mm".
func contentReach(sch *sexp.Schematic) float64 {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	note := func(x, y float64) {
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
	}
	for _, s := range sexp.ReadSymbols(sch) {
		for _, p := range s.Pins {
			note(p.X, p.Y)
		}
	}
	for _, seg := range wireSegments(sch) {
		note(seg.ax, seg.ay)
		note(seg.bx, seg.by)
	}
	if math.IsInf(minX, 1) {
		return minWeldReach
	}
	return math.Max(minWeldReach, math.Hypot(maxX-minX, maxY-minY)*weldReachFraction)
}
