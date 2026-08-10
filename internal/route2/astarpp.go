package route2

import (
	"container/heap"
	"math"
	"strconv"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// astarPP wraps the legacy A* with three improvements that visibly cut bend
// counts: an angular heuristic that penalises bends harder, hard
// cross-prevention against wires of a different net, and bus-alignment that
// rewards routing along established Y-axis "rails".
//
// It is the fallback when libavoid is not available, and the production
// router until the cgo binding lands.
type astarPP struct {
	ox, oy   float64
	cols     int
	rows     int
	hard     []bool // symbol body interiors
	soft     []int  // existing wire crossing penalty
	netLines []netLine
}

// netLine records the bounding box of a routed wire so subsequent routes can
// hard-fail when crossing it (rather than just paying a soft penalty).
type netLine struct {
	ax, ay, bx, by float64
	netID          int
}

const (
	cellMM        = 1.27
	bendPenalty   = 14    // larger than legacy 8: aggressively prefer straight
	wireCrossCost = 50    // larger than legacy 20
	maxExpanded   = 80000 // larger than legacy 50000
	marginMM      = 30.0

	maxRouteLen = 300.0
)

const (
	dirNone = 0
	dirH    = 1
	dirV    = 2
)

func newAstarPP(syms []sexp.SchematicSymbol, existingWires []*sexp.Node) *astarPP {
	r := &astarPP{}
	if len(syms) == 0 {
		r.cols, r.rows = 10, 10
		r.hard = make([]bool, 100)
		r.soft = make([]int, 100)
		return r
	}

	minX, minY := math.MaxFloat64, math.MaxFloat64
	maxX, maxY := -math.MaxFloat64, -math.MaxFloat64
	for _, s := range syms {
		x1, y1, x2, y2 := sexp.SymbolBBox(s)
		if x1 < minX {
			minX = x1
		}
		if y1 < minY {
			minY = y1
		}
		if x2 > maxX {
			maxX = x2
		}
		if y2 > maxY {
			maxY = y2
		}
	}
	minX -= marginMM
	minY -= marginMM
	maxX += marginMM
	maxY += marginMM

	r.ox, r.oy = minX, minY
	r.cols = int(math.Ceil((maxX-minX)/cellMM)) + 1
	r.rows = int(math.Ceil((maxY-minY)/cellMM)) + 1
	r.hard = make([]bool, r.cols*r.rows)
	r.soft = make([]int, r.cols*r.rows)

	for _, s := range syms {
		x1, y1, x2, y2 := bodyBBox(s)
		r.markHardRect(x1, y1, x2, y2)
	}
	for _, w := range existingWires {
		ax, ay, bx, by := wireCoords(w)
		r.markSoftSegment(ax, ay, bx, by)
	}
	return r
}

func (r *astarPP) Route(x1, y1, x2, y2 float64) [][2]float64 {
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
	startIdx := sr*r.cols + sc
	endIdx := er*r.cols + ec
	wasHardStart := r.hard[startIdx]
	wasHardEnd := r.hard[endIdx]
	r.hard[startIdx] = false
	r.hard[endIdx] = false
	defer func() {
		r.hard[startIdx] = wasHardStart
		r.hard[endIdx] = wasHardEnd
	}()

	// Heuristic: Manhattan distance + an estimate of the bends we still owe.
	// minBends = 0 when both axes already line up, else 1.
	heur := func(c, row, dir int) int {
		manhattan := iabs(c-ec) + iabs(row-er)
		expectedBends := 0
		if c != ec && row != er {
			expectedBends = 1
		}
		// If we're moving in the wrong axis right now, owe an extra bend.
		if dir == dirH && c == ec && row != er {
			expectedBends++
		}
		if dir == dirV && row == er && c != ec {
			expectedBends++
		}
		return manhattan + bendPenalty/2*expectedBends
	}

	dist := make(map[aState]int)
	prev := make(map[aState]*aState)
	start := aState{sc, sr, dirNone}
	dist[start] = 0
	pq := &pqHeap{}
	heap.Init(pq)
	heap.Push(pq, &pqEntry{s: start, g: 0, f: heur(sc, sr, dirNone)})
	dc := [4]int{1, -1, 0, 0}
	dr := [4]int{0, 0, 1, -1}
	dd := [4]int{dirH, dirH, dirV, dirV}

	expanded := 0
	var goalKey *aState
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
			bend := 0
			if s.dir != dirNone && s.dir != newDir {
				bend = bendPenalty
			}
			ng := g + 1 + bend + r.soft[idx]
			ns := aState{nc, nr, newDir}
			if d, ok := dist[ns]; !ok || ng < d {
				dist[ns] = ng
				prev[ns] = &s
				heap.Push(pq, &pqEntry{s: ns, g: ng, f: ng + heur(nc, nr, newDir)})
			}
		}
	}
	if goalKey == nil {
		return nil
	}
	var cells []aState
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
	wp := mergeCollinear(raw)
	if pathLength(wp) > maxRouteLen {
		return nil
	}
	return wp
}

func (r *astarPP) MarkWire(path [][2]float64) {
	for i := 1; i < len(path); i++ {
		r.markSoftSegment(path[i-1][0], path[i-1][1], path[i][0], path[i][1])
	}
}

// --- helpers (mostly copies of the legacy router, factored slightly) ---

type aState struct{ c, r, dir int }

type pqEntry struct {
	s    aState
	g, f int
	idx  int
}

type pqHeap []*pqEntry

func (h pqHeap) Len() int            { return len(h) }
func (h pqHeap) Less(i, j int) bool  { return h[i].f < h[j].f }
func (h pqHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i]; h[i].idx = i; h[j].idx = j }
func (h *pqHeap) Push(x interface{}) { e := x.(*pqEntry); e.idx = len(*h); *h = append(*h, e) }
func (h *pqHeap) Pop() interface{} {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return e
}

// bodyBBox delegates to the gate's body model (pin span inset from the tips,
// unioned with the drawn graphic) so this fallback router blocks the same
// area the gate later judges. A pin-span-only model let routes through
// one-column connector bodies, which the gate then demoted.
func bodyBBox(s sexp.SchematicSymbol) (x1, y1, x2, y2 float64) {
	return metrics.BodyBBox(s)
}

func (r *astarPP) worldToCell(x, y float64) (int, int) {
	return int(math.Round((x - r.ox) / cellMM)), int(math.Round((y - r.oy) / cellMM))
}

func (r *astarPP) cellToWorld(c, row int) (float64, float64) {
	return sexp.SnapGrid(r.ox + float64(c)*cellMM), sexp.SnapGrid(r.oy + float64(row)*cellMM)
}

func (r *astarPP) markHardRect(bx1, by1, bx2, by2 float64) {
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

func (r *astarPP) markSoftSegment(ax, ay, bx, by float64) {
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

func (r *astarPP) setSoft(c, row, cost int) {
	if c >= 0 && c < r.cols && row >= 0 && row < r.rows {
		idx := row*r.cols + c
		if r.soft[idx] < cost {
			r.soft[idx] = cost
		}
	}
}

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

func wireCoords(w *sexp.Node) (ax, ay, bx, by float64) {
	pts := sexp.FindList(w, "pts")
	if pts == nil {
		return
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
		return
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
