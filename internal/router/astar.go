// Package router provides an A* orthogonal grid router for KiCad schematics.
// It finds obstacle-avoiding wire paths at 1.27 mm grid resolution.
package router

import (
	"container/heap"
	"math"
	"strconv"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

const (
	cellMM        = 1.27  // grid resolution in mm
	bendPenalty   = 8     // extra cost for changing direction
	wireCrossCost = 20    // extra cost per cell that overlaps an existing wire
	maxExpanded   = 50000 // A* node budget
	marginMM      = 30.0  // grid margin around world bounding box
	// MaxRouteLen is the maximum wire length (mm) before falling back to labels.
	// Schematics commonly span 200+ mm, so keep this generous.
	MaxRouteLen = 300.0
)

const (
	dirNone = 0
	dirH    = 1
	dirV    = 2
)

// Router holds the obstacle grid and can route multiple paths.
// After routing a segment call MarkWire to treat the result as a soft obstacle.
type Router struct {
	ox, oy  float64
	cols    int
	rows    int
	hard    []bool // true → not traversable (symbol body interior)
	soft    []int  // extra traversal cost (existing wire crossing)
	bendPen int    // 0 = use the package default
}

// NewRouter builds the obstacle grid from placed symbols and existing wires.
//
// Hard obstacles are the symbol body interiors (inset from pin positions so
// that pin-tip cells are always outside the blocked area and the A* can start
// and end at any pin without being immediately stuck).
//
// Soft obstacles are existing wire segments (+wireCrossCost to traverse).
func NewRouter(syms []sexp.SchematicSymbol, existingWires []*sexp.Node) *Router {
	if len(syms) == 0 {
		r := &Router{ox: 0, oy: 0, cols: 10, rows: 10}
		r.hard = make([]bool, 100)
		r.soft = make([]int, 100)
		return r
	}

	// World bounding box from the padded SymbolBBox (pin tips + margin).
	// Used only to size the grid — not for hard obstacles.
	minX, minY := math.MaxFloat64, math.MaxFloat64
	maxX, maxY := -math.MaxFloat64, -math.MaxFloat64
	for _, sym := range syms {
		bx1, by1, bx2, by2 := sexp.SymbolBBox(sym)
		if bx1 < minX {
			minX = bx1
		}
		if by1 < minY {
			minY = by1
		}
		if bx2 > maxX {
			maxX = bx2
		}
		if by2 > maxY {
			maxY = by2
		}
	}
	minX -= marginMM
	minY -= marginMM
	maxX += marginMM
	maxY += marginMM

	cols := int(math.Ceil((maxX-minX)/cellMM)) + 1
	rows := int(math.Ceil((maxY-minY)/cellMM)) + 1

	r := &Router{
		ox:   minX,
		oy:   minY,
		cols: cols,
		rows: rows,
		hard: make([]bool, cols*rows),
		soft: make([]int, cols*rows),
	}

	// Mark hard obstacles using the same body model the quality gate judges
	// with (metrics.BodyBBox): the pin-span box inset from the tips, unioned
	// with the symbol's drawn graphic (clamped off pin tips). Pin-tip cells
	// stay outside the hard area so the A* can expand from any pin endpoint.
	// Anything narrower — the pin span alone — is blind to bodies the pins do
	// not enclose: a one-column connector has a degenerate pin span and its
	// drawn rectangle used to be entirely routable, so the A* wired through
	// it and the gate demoted the net afterwards.
	for _, sym := range syms {
		bx1, by1, bx2, by2 := metrics.BodyBBox(sym)
		r.markHardRect(bx1, by1, bx2, by2)
	}

	for _, w := range existingWires {
		ax, ay, bx, by := nodeWireCoords(w)
		r.markSoftSegment(ax, ay, bx, by)
	}

	return r
}

// BendPenalty overrides the cost of changing direction for this router.
//
// Left at zero it means bendPenalty, the default. Raising it buys straighter
// wires with longer detours, and which of those a given sheet prefers is not
// something one constant can answer: swept across the reference corpus, 64
// gave the fewest corners (167 → 154) and the most text collisions in two of
// the four settings tried. So the value is a knob the compiler's own search
// turns while measuring, not a number chosen once here.
func (r *Router) BendPenalty(cost int) { r.bendPen = cost }

func (r *Router) bendPenalty() int {
	if r.bendPen > 0 {
		return r.bendPen
	}
	return bendPenalty
}

// astate is a grid position plus the direction we arrived from.
type astate struct {
	c, r, dir int
}

// pqEntry is one entry in the A* priority queue.
type pqEntry struct {
	s       astate
	g, f    int
	heapIdx int
}

// astarPQ is a min-heap of *pqEntry by f.
type astarPQ []*pqEntry

func (pq astarPQ) Len() int           { return len(pq) }
func (pq astarPQ) Less(i, j int) bool { return pq[i].f < pq[j].f }
func (pq astarPQ) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].heapIdx = i
	pq[j].heapIdx = j
}
func (pq *astarPQ) Push(x any) {
	e := x.(*pqEntry)
	e.heapIdx = len(*pq)
	*pq = append(*pq, e)
}
func (pq *astarPQ) Pop() any {
	old := *pq
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	return e
}

// RouteAvoiding is Route with extra cells blocked for the duration of this
// call: the pin tips of every net OTHER than the one being routed.
//
// Touching a pin tip is a connection in KiCad, so a route that crosses or
// ends on a foreign pin silently merges two nets. The result is geometrically
// impeccable — one consistent net, no crossing, nothing for the quality gate
// to object to — and electrically wrong. Keeping the A* out of those cells is
// the only place the distinction can still be made, because afterwards the
// two nets are indistinguishable from one.
//
// Cells are restored on return, so the grid is unchanged for the next net.
func (r *Router) RouteAvoiding(x1, y1, x2, y2 float64, avoid [][2]float64) [][2]float64 {
	saved := make(map[int]bool, len(avoid))
	for _, p := range avoid {
		c, row := r.worldToCell(p[0], p[1])
		if c < 0 || c >= r.cols || row < 0 || row >= r.rows {
			continue
		}
		idx := row*r.cols + c
		if _, seen := saved[idx]; !seen {
			saved[idx] = r.hard[idx]
		}
		r.hard[idx] = true
	}
	defer func() {
		for idx, was := range saved {
			r.hard[idx] = was
		}
	}()
	return r.Route(x1, y1, x2, y2)
}

// Route finds an orthogonal path from (x1,y1) to (x2,y2).
// Returns nil when no path is found or the route exceeds MaxRouteLen mm.
// The returned slice is a minimal set of waypoints (collinear points merged).
func (r *Router) Route(x1, y1, x2, y2 float64) [][2]float64 {
	sc, sr := r.worldToCell(x1, y1)
	ec, er := r.worldToCell(x2, y2)

	sc = clamp(sc, 0, r.cols-1)
	sr = clamp(sr, 0, r.rows-1)
	ec = clamp(ec, 0, r.cols-1)
	er = clamp(er, 0, r.rows-1)

	if sc == ec && sr == er {
		wx, wy := r.cellToWorld(sc, sr)
		return [][2]float64{{wx, wy}}
	}

	// Safety: pin cells must be traversable even if a bbox extends over them.
	// (metrics.BodyBBox already ensures this for well-formed symbols, but
	// power symbols or custom footprints may still land inside a hard cell.)
	startIdx := sr*r.cols + sc
	endIdx := er*r.cols + ec
	origHardStart := r.hard[startIdx]
	origHardEnd := r.hard[endIdx]
	r.hard[startIdx] = false
	r.hard[endIdx] = false
	defer func() {
		r.hard[startIdx] = origHardStart
		r.hard[endIdx] = origHardEnd
	}()

	heur := func(c, row int) int {
		return iabs(c-ec) + iabs(row-er)
	}

	dist := make(map[astate]int)
	prev := make(map[astate]*astate)

	start := astate{sc, sr, dirNone}
	dist[start] = 0

	pq := &astarPQ{}
	heap.Init(pq)
	heap.Push(pq, &pqEntry{s: start, g: 0, f: heur(sc, sr)})

	// Cardinal directions: right, left, down, up
	dc := [4]int{1, -1, 0, 0}
	dr := [4]int{0, 0, 1, -1}
	dd := [4]int{dirH, dirH, dirV, dirV}

	expanded := 0
	var goalKey *astate

	for pq.Len() > 0 && expanded < maxExpanded {
		cur := heap.Pop(pq).(*pqEntry)
		s := cur.s
		g := cur.g

		if s.c == ec && s.r == er {
			k := s
			goalKey = &k
			break
		}
		if d, ok := dist[s]; ok && g > d {
			continue
		}
		expanded++

		for i := 0; i < 4; i++ {
			nc, nr := s.c+dc[i], s.r+dr[i]
			if nc < 0 || nc >= r.cols || nr < 0 || nr >= r.rows {
				continue
			}
			idx := nr*r.cols + nc
			if r.hard[idx] {
				continue
			}
			newDir := dd[i]
			bendCost := 0
			if s.dir != dirNone && s.dir != newDir {
				bendCost = r.bendPenalty()
			}
			ng := g + 1 + bendCost + r.soft[idx]
			ns := astate{nc, nr, newDir}
			if d, ok := dist[ns]; !ok || ng < d {
				dist[ns] = ng
				prev[ns] = &s
				heap.Push(pq, &pqEntry{s: ns, g: ng, f: ng + heur(nc, nr)})
			}
		}
	}

	if goalKey == nil {
		return nil
	}

	// Reconstruct path.
	var cells []astate
	k := goalKey
	for k != nil {
		cells = append(cells, *k)
		p := prev[*k]
		k = p
	}
	for i, j := 0, len(cells)-1; i < j; i, j = i+1, j-1 {
		cells[i], cells[j] = cells[j], cells[i]
	}

	raw := make([][2]float64, len(cells))
	for i, s := range cells {
		wx, wy := r.cellToWorld(s.c, s.r)
		raw[i] = [2]float64{wx, wy}
	}
	waypoints := mergeCollinear(raw)

	if pathLength(waypoints) > MaxRouteLen {
		return nil
	}
	return waypoints
}

// MarkWire marks the cells along a routed path as soft obstacles so that
// subsequent routes avoid them (with a cost penalty) rather than being blocked.
func (r *Router) MarkWire(path [][2]float64) {
	for i := 1; i < len(path); i++ {
		r.markSoftSegment(path[i-1][0], path[i-1][1], path[i][0], path[i][1])
	}
}

// --- grid helpers ---

func (r *Router) worldToCell(x, y float64) (int, int) {
	c := int(math.Round((x - r.ox) / cellMM))
	row := int(math.Round((y - r.oy) / cellMM))
	return c, row
}

func (r *Router) cellToWorld(c, row int) (float64, float64) {
	x := r.ox + float64(c)*cellMM
	y := r.oy + float64(row)*cellMM
	return sexp.SnapGrid(x), sexp.SnapGrid(y)
}

func (r *Router) markHardRect(bx1, by1, bx2, by2 float64) {
	c1, r1 := r.worldToCell(bx1, by1)
	c2, r2 := r.worldToCell(bx2, by2)
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	for row := r1; row <= r2; row++ {
		for col := c1; col <= c2; col++ {
			if col >= 0 && col < r.cols && row >= 0 && row < r.rows {
				r.hard[row*r.cols+col] = true
			}
		}
	}
}

func (r *Router) markSoftSegment(ax, ay, bx, by float64) {
	d := math.Sqrt((bx-ax)*(bx-ax) + (by-ay)*(by-ay))
	if d < 0.001 {
		c, row := r.worldToCell(ax, ay)
		r.setSoft(c, row, wireCrossCost)
		return
	}
	steps := int(math.Ceil(d/cellMM)) + 1
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		c, row := r.worldToCell(ax+t*(bx-ax), ay+t*(by-ay))
		r.setSoft(c, row, wireCrossCost)
	}
}

func (r *Router) setSoft(c, row, cost int) {
	if c >= 0 && c < r.cols && row >= 0 && row < r.rows {
		idx := row*r.cols + c
		if r.soft[idx] < cost {
			r.soft[idx] = cost
		}
	}
}

// --- path utilities ---

func mergeCollinear(pts [][2]float64) [][2]float64 {
	if len(pts) <= 2 {
		return pts
	}
	out := [][2]float64{pts[0]}
	for i := 1; i < len(pts)-1; i++ {
		prev := out[len(out)-1]
		cur := pts[i]
		next := pts[i+1]
		sameH := math.Abs(prev[1]-cur[1]) < 0.001 && math.Abs(cur[1]-next[1]) < 0.001
		sameV := math.Abs(prev[0]-cur[0]) < 0.001 && math.Abs(cur[0]-next[0]) < 0.001
		if !sameH && !sameV {
			out = append(out, cur)
		}
	}
	return append(out, pts[len(pts)-1])
}

func pathLength(pts [][2]float64) float64 {
	total := 0.0
	for i := 1; i < len(pts); i++ {
		dx := pts[i][0] - pts[i-1][0]
		dy := pts[i][1] - pts[i-1][1]
		total += math.Sqrt(dx*dx + dy*dy)
	}
	return total
}

// nodeWireCoords extracts (ax,ay,bx,by) from a (wire (pts (xy ..) (xy ..)) ..) node.
func nodeWireCoords(w *sexp.Node) (float64, float64, float64, float64) {
	pts := sexp.FindList(w, "pts")
	if pts == nil {
		return 0, 0, 0, 0
	}
	var xs, ys [2]float64
	n := 0
	for _, xy := range pts.Children {
		if xy.Head() != "xy" || n >= 2 {
			continue
		}
		xs[n], _ = strconv.ParseFloat(sexp.AtomValue(xy, 1), 64)
		ys[n], _ = strconv.ParseFloat(sexp.AtomValue(xy, 2), 64)
		n++
	}
	if n < 2 {
		return 0, 0, 0, 0
	}
	return xs[0], ys[0], xs[1], ys[1]
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
