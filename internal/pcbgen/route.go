package pcbgen

import (
	"container/heap"
	"math"
	"sort"
)

// A two-layer grid router with negotiated congestion — PathFinder
// (McMurchie & Ebeling, "PathFinder: A Negotiation-Based Performance-Driven
// Router for FPGAs", FPGA 1995) on top of A* (Hart, Nilsson & Raphael, 1968).
// Written from the published algorithms; no code from any other router.
//
// Every net is routed every iteration while other nets' copper is only
// expensive, not forbidden. Cells that end up shared get dearer each round
// (present cost) and keep a memory of having been fought over (history
// cost), so nets negotiate who goes around. When nobody shares anything the
// routing is legal. What still conflicts after the last iteration is ripped
// out and reported unrouted: the router never hands back copper that breaks
// a clearance.
//
// Legality is geometric and conservative. A trace's centreline runs between
// grid cell centres; every point of it lies within m = grid·√2/2 of one, so
// keeping cell centres (clearance + both half widths + 2m) apart keeps the
// copper (clearance) apart. Pads and vias sit at exact positions and need
// only one m.

// RouteRules are the copper rules the router honours. DefaultRouteRules
// matches what KiCad 10 applies to a new board (measured with pcbnew:
// netclass clearance 0.2, track 0.2, via 0.6/0.3, copper-to-edge 0.5,
// hole-to-hole 0.25), with a slightly wider track.
type RouteRules struct {
	Track, Clearance, Via, Drill, Edge, HoleToHole float64
	Grid                                           float64
}

var DefaultRouteRules = RouteRules{
	Track: 0.25, Clearance: 0.2, Via: 0.6, Drill: 0.3, Edge: 0.5, HoleToHole: 0.25,
	// 1/8 of the 1.27 mm placement grid: a cell row runs exactly midway
	// between two DIP pins, which is where a hand-routed trace goes.
	Grid: 1.27 / 8,
}

// Segment is one straight piece of track.
type Segment struct {
	X1, Y1, X2, Y2 float64
	Layer          int // 0 = F.Cu, 1 = B.Cu
	Net            string
}

// Via is a plated hole joining the two layers.
type Via struct {
	X, Y float64
	Net  string
}

// RouteResult is what the router produced.
type RouteResult struct {
	Segments    []Segment
	Vias        []Via
	Connections int      // pad-to-pad connections to make
	Routed      int      // of which routed
	Unrouted    []string // nets with at least one connection left to the user
	Iterations  int
}

const (
	maxIterations = 40
	viaCost       = 3.0 // mm of track a layer change is worth
	wrongWayCost  = 1.6 // F.Cu prefers horizontal, B.Cu vertical
	diagonalCost  = 1.15
)

type rnode int32 // (layer*ny + y)*nx + x

type routePad struct {
	x, y   float64 // centre
	hw, hh float64 // half extents (axis aligned) or radius in hw when round
	round  bool
	front  bool
	back   bool
	net    int // -1: belongs to no net (mechanical, NPTH)
	drill  float64
	npth   bool
}

func (p routePad) dist(x, y float64) float64 {
	if p.round {
		return math.Max(0, math.Hypot(x-p.x, y-p.y)-p.hw)
	}
	dx := math.Max(math.Abs(x-p.x)-p.hw, 0)
	dy := math.Max(math.Abs(y-p.y)-p.hh, 0)
	return math.Hypot(dx, dy)
}

func (p routePad) onLayer(l int) bool { return (l == 0 && p.front) || (l == 1 && p.back) }

type conn struct {
	net  int
	path []rnode
	pads [2]int // pad indices at the ends, -1 where the path starts on the tree
}

type router struct {
	r       RouteRules
	outline Box
	nx, ny  int
	m       float64 // grid slack

	pads    []routePad
	netName []string
	netPads [][]int

	// static: -1 free, net id when only that net's pads are near, -2 blocked
	padOwner []int32 // per node, trace probe
	viaOK    []bool  // per cell (layer 0 index), via probe vs pads/edge

	// dynamic copper claims: how many foreign-copper discs cover a node,
	// for a trace probe and for a via probe; own[net] subtracts a net's own
	claimT, claimV []int32
	ownT, ownV     []map[rnode]int32
	hist           []float32
	pres           float64

	conns []*conn
	vias  [][]rnode // per net, via nodes (layer 0 index)

	// search scratch
	g     []float32
	from  []rnode
	stamp []uint32
	cur   uint32
}

// Route routes every net between the placed parts inside the outline.
func Route(parts []*Part, nets []Net, outline Box, rules RouteRules) RouteResult {
	rt := newRouter(parts, nets, outline, rules)
	res := RouteResult{}
	order := rt.netOrder()
	for _, n := range order {
		res.Connections += max(0, len(rt.netPads[n])-1)
	}
	if res.Connections == 0 {
		return res
	}
	rt.pres = 0.5
	for it := 1; it <= maxIterations; it++ {
		res.Iterations = it
		for _, n := range order {
			rt.ripUp(n)
			rt.routeNet(n)
		}
		if rt.overuse(true) == 0 {
			break
		}
		rt.pres *= 1.7
	}
	// Whatever still fights is given up whole-net, worst first, until the
	// copper left is legal.
	for rt.overuse(false) > 0 {
		worst, worstN := -1, 0
		for _, n := range order {
			if c := rt.netConflicts(n); c > worstN {
				worst, worstN = n, c
			}
		}
		if worst < 0 {
			break
		}
		rt.ripUp(worst)
	}
	return rt.result(res)
}

func newRouter(parts []*Part, nets []Net, outline Box, rules RouteRules) *router {
	rt := &router{r: rules, outline: outline, m: rules.Grid * math.Sqrt2 / 2}
	rt.nx = int(math.Ceil(outline.W()/rules.Grid)) + 1
	rt.ny = int(math.Ceil(outline.H()/rules.Grid)) + 1

	netID := map[string]int{}
	byRef := map[string]*Part{}
	for _, p := range parts {
		byRef[p.Comp.Ref] = p
	}
	padNet := map[string]int{} // ref\x00pin -> net
	for _, n := range nets {
		if len(n.Nodes) == 0 {
			continue
		}
		id := len(rt.netName)
		netID[n.Name] = id
		rt.netName = append(rt.netName, n.Name)
		for _, nd := range n.Nodes {
			padNet[nd.Ref+"\x00"+nd.Pin] = id
		}
	}
	rt.netPads = make([][]int, len(rt.netName))
	for _, p := range sortedParts(parts) {
		for _, ps := range p.FP.PadShapes {
			x, y := rotate(ps.X, ps.Y, p.Rot)
			rp := routePad{x: p.X + x, y: p.Y + y, front: ps.Front, back: ps.Back,
				drill: ps.Drill, npth: ps.NPTH, net: -1}
			angle := math.Mod(ps.Angle+float64(p.Rot), 360)
			switch {
			case ps.Round:
				rp.round, rp.hw = true, ps.W/2
			case math.Mod(angle, 90) != 0:
				// Off-axis pad: its circumscribed circle.
				rp.round, rp.hw = true, math.Hypot(ps.W, ps.H)/2
			case math.Mod(angle, 180) != 0:
				rp.hw, rp.hh = ps.H/2, ps.W/2
			default:
				rp.hw, rp.hh = ps.W/2, ps.H/2
			}
			if ps.NPTH {
				rp.round, rp.hw, rp.front, rp.back = true, math.Max(ps.Drill, ps.W)/2, true, true
			}
			if id, ok := padNet[p.Comp.Ref+"\x00"+ps.Number]; ok && ps.Number != "" && !ps.NPTH {
				rp.net = id
				rt.netPads[id] = append(rt.netPads[id], len(rt.pads))
			}
			rt.pads = append(rt.pads, rp)
		}
	}

	cells := rt.nx * rt.ny
	rt.padOwner = make([]int32, 2*cells)
	rt.viaOK = make([]bool, cells)
	rt.claimT = make([]int32, 2*cells)
	rt.claimV = make([]int32, 2*cells)
	rt.hist = make([]float32, 2*cells)
	rt.g = make([]float32, 2*cells)
	rt.from = make([]rnode, 2*cells)
	rt.stamp = make([]uint32, 2*cells)
	rt.ownT = make([]map[rnode]int32, len(rt.netName))
	rt.ownV = make([]map[rnode]int32, len(rt.netName))
	for i := range rt.ownT {
		rt.ownT[i], rt.ownV[i] = map[rnode]int32{}, map[rnode]int32{}
	}
	rt.vias = make([][]rnode, len(rt.netName))
	rt.buildStatic()
	return rt
}

// buildStatic marks what no trace or via may approach: other nets' pads, bare
// holes and the board edge.
func (rt *router) buildStatic() {
	r := rt.r
	traceEdge := r.Edge + r.Track/2 + rt.m
	viaEdge := r.Edge + r.Via/2
	for y := 0; y < rt.ny; y++ {
		for x := 0; x < rt.nx; x++ {
			px, py := rt.pos(x, y)
			e := math.Min(math.Min(px-rt.outline.X1, rt.outline.X2-px), math.Min(py-rt.outline.Y1, rt.outline.Y2-py))
			c := y*rt.nx + x
			for l := 0; l < 2; l++ {
				rt.padOwner[l*rt.nx*rt.ny+c] = -1
				if e < traceEdge {
					rt.padOwner[l*rt.nx*rt.ny+c] = -2
				}
			}
			rt.viaOK[c] = e >= viaEdge
		}
	}
	for _, p := range rt.pads {
		clear := r.Clearance
		if p.npth {
			clear = math.Max(clear, r.HoleToHole)
		}
		traceR := clear + r.Track/2 + rt.m
		// A via may not come near ANY pad, own net included: its hole
		// would crowd the pad's hole or sit half on its copper.
		viaR := math.Max(clear+r.Via/2, p.drill/2+r.HoleToHole+r.Drill/2)
		reach := math.Max(traceR, viaR) + math.Max(p.hw, p.hh)
		rt.eachCell(p.x, p.y, reach, func(x, y int, px, py float64) {
			d := p.dist(px, py)
			c := y*rt.nx + x
			if d < viaR {
				rt.viaOK[c] = false
			}
			if d >= traceR {
				return
			}
			for l := 0; l < 2; l++ {
				if !p.onLayer(l) {
					continue
				}
				i := l*rt.nx*rt.ny + c
				switch o := rt.padOwner[i]; {
				case p.net < 0:
					rt.padOwner[i] = -2
				case o == -1:
					rt.padOwner[i] = int32(p.net)
				case o != int32(p.net):
					rt.padOwner[i] = -2
				}
			}
		})
	}
}

func (rt *router) pos(x, y int) (float64, float64) {
	return rt.outline.X1 + float64(x)*rt.r.Grid, rt.outline.Y1 + float64(y)*rt.r.Grid
}

func (rt *router) node(l, x, y int) rnode { return rnode((l*rt.ny+y)*rt.nx + x) }

func (rt *router) split(n rnode) (l, x, y int) {
	c := int(n)
	l = c / (rt.nx * rt.ny)
	c %= rt.nx * rt.ny
	return l, c % rt.nx, c / rt.nx
}

// eachCell visits the cells whose centre lies within reach of (cx, cy).
func (rt *router) eachCell(cx, cy, reach float64, f func(x, y int, px, py float64)) {
	g := rt.r.Grid
	x0 := max(0, int(math.Floor((cx-reach-rt.outline.X1)/g)))
	x1 := min(rt.nx-1, int(math.Ceil((cx+reach-rt.outline.X1)/g)))
	y0 := max(0, int(math.Floor((cy-reach-rt.outline.Y1)/g)))
	y1 := min(rt.ny-1, int(math.Ceil((cy+reach-rt.outline.Y1)/g)))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			px, py := rt.pos(x, y)
			f(x, y, px, py)
		}
	}
}

// claim adds (d=+1) or removes (d=-1) the discs a net's copper at node n
// keeps other nets' trace and via centres out of.
func (rt *router) claim(net int, n rnode, isVia bool, d int32) {
	r := rt.r
	l, x, y := rt.split(n)
	cx, cy := rt.pos(x, y)
	half, slack := r.Track/2, rt.m
	if isVia {
		half, slack = r.Via/2, 0
	}
	rT := half + r.Clearance + r.Track/2 + slack + rt.m
	rV := half + r.Clearance + r.Via/2 + slack
	layers := []int{l}
	if isVia {
		layers = []int{0, 1}
	}
	rt.eachCell(cx, cy, math.Max(rT, rV), func(xx, yy int, px, py float64) {
		dist := math.Hypot(px-cx, py-cy)
		for _, ll := range layers {
			i := rt.node(ll, xx, yy)
			if dist < rT {
				rt.claimT[i] += d
				rt.ownT[net][i] += d
			}
			if dist < rV {
				rt.claimV[i] += d
				rt.ownV[net][i] += d
			}
		}
	})
}

// others is how much foreign copper covers node i for a probe of net.
func (rt *router) othersT(net int, i rnode) int32 { return rt.claimT[i] - rt.ownT[net][i] }
func (rt *router) othersV(net int, i rnode) int32 { return rt.claimV[i] - rt.ownV[net][i] }

func (rt *router) open(net int, i rnode) bool {
	o := rt.padOwner[i]
	return o == -1 || o == int32(net)
}

// netOrder routes short nets first: they have the fewest ways around.
func (rt *router) netOrder() []int {
	type ns struct {
		id   int
		span float64
	}
	var list []ns
	for id, pads := range rt.netPads {
		if len(pads) < 2 {
			continue
		}
		b := Box{rt.pads[pads[0]].x, rt.pads[pads[0]].y, rt.pads[pads[0]].x, rt.pads[pads[0]].y}
		for _, pi := range pads[1:] {
			b = b.union(Box{rt.pads[pi].x, rt.pads[pi].y, rt.pads[pi].x, rt.pads[pi].y})
		}
		list = append(list, ns{id, b.W() + b.H()})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].span != list[j].span {
			return list[i].span < list[j].span
		}
		return rt.netName[list[i].id] < rt.netName[list[j].id]
	})
	out := make([]int, len(list))
	for i, n := range list {
		out[i] = n.id
	}
	return out
}

func (rt *router) ripUp(net int) {
	kept := rt.conns[:0]
	for _, c := range rt.conns {
		if c.net != net {
			kept = append(kept, c)
			continue
		}
		rt.claimPath(c, -1)
	}
	rt.conns = kept
	rt.vias[net] = nil
}

func (rt *router) claimPath(c *conn, d int32) {
	for k, n := range c.path {
		rt.claim(c.net, n, false, d)
		if k > 0 && rt.isLayerChange(c.path[k-1], n) {
			rt.claim(c.net, n, true, d)
		}
	}
}

func (rt *router) isLayerChange(a, b rnode) bool {
	la, xa, ya := rt.split(a)
	lb, xb, yb := rt.split(b)
	return la != lb && xa == xb && ya == yb
}

// routeNet grows the net as a tree: from everything already connected, to
// the nearest pad not yet reached.
func (rt *router) routeNet(net int) {
	pads := rt.netPads[net]
	inTree := map[int]bool{pads[0]: true}
	var treeNodes []rnode
	for len(inTree) < len(pads) {
		// Nearest unreached pad to any reached one (Prim on pad centres).
		next, nextD := -1, math.Inf(1)
		for _, pi := range pads {
			if inTree[pi] {
				continue
			}
			for ti := range inTree {
				d := math.Abs(rt.pads[pi].x-rt.pads[ti].x) + math.Abs(rt.pads[pi].y-rt.pads[ti].y)
				if d < nextD || (d == nextD && pi < next) {
					next, nextD = pi, d
				}
			}
		}
		var sources []rnode
		for ti := range inTree {
			sources = append(sources, rt.padNodes(ti, net)...)
		}
		sources = append(sources, treeNodes...)
		sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
		path := rt.search(net, sources, rt.padNodes(next, net), rt.pads[next])
		inTree[next] = true
		if path == nil {
			continue
		}
		c := &conn{net: net, path: path, pads: [2]int{rt.padAt(net, path[0]), next}}
		rt.claimPath(c, +1)
		for k := 1; k < len(path); k++ {
			if rt.isLayerChange(path[k-1], path[k]) {
				_, x, y := rt.split(path[k])
				rt.vias[net] = append(rt.vias[net], rt.node(0, x, y))
			}
		}
		rt.conns = append(rt.conns, c)
		treeNodes = append(treeNodes, path...)
	}
}

// padNodes are the grid nodes inside a pad's copper, on its layers, that the
// net may use.
func (rt *router) padNodes(pi, net int) []rnode {
	p := rt.pads[pi]
	var out []rnode
	rt.eachCell(p.x, p.y, math.Max(p.hw, p.hh), func(x, y int, px, py float64) {
		if p.dist(px, py) > 0 {
			return
		}
		for l := 0; l < 2; l++ {
			if i := rt.node(l, x, y); p.onLayer(l) && rt.open(net, i) {
				out = append(out, i)
			}
		}
	})
	if len(out) == 0 {
		// A pad smaller than a cell: its nearest node.
		x := int(math.Round((p.x - rt.outline.X1) / rt.r.Grid))
		y := int(math.Round((p.y - rt.outline.Y1) / rt.r.Grid))
		if x >= 0 && y >= 0 && x < rt.nx && y < rt.ny {
			for l := 0; l < 2; l++ {
				if i := rt.node(l, x, y); p.onLayer(l) && rt.open(net, i) {
					out = append(out, i)
				}
			}
		}
	}
	return out
}

// padAt returns the pad of net whose copper contains node n, or -1.
func (rt *router) padAt(net int, n rnode) int {
	l, x, y := rt.split(n)
	px, py := rt.pos(x, y)
	for _, pi := range rt.netPads[net] {
		p := rt.pads[pi]
		if p.onLayer(l) && p.dist(px, py) <= rt.r.Grid {
			return pi
		}
	}
	return -1
}

type pqItem struct {
	n rnode
	f float32
}
type pq []pqItem

func (q pq) Len() int { return len(q) }
func (q pq) Less(i, j int) bool {
	if q[i].f != q[j].f {
		return q[i].f < q[j].f
	}
	return q[i].n < q[j].n
}
func (q pq) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x any)   { *q = append(*q, x.(pqItem)) }
func (q *pq) Pop() any     { o := *q; it := o[len(o)-1]; *q = o[:len(o)-1]; return it }

var steps = [8][3]int{ // dx, dy, diagonal
	{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0},
	{1, 1, 1}, {1, -1, 1}, {-1, 1, 1}, {-1, -1, 1},
}

// search is A* from any source to any target node, costs negotiated.
func (rt *router) search(net int, sources, targets []rnode, goal routePad) []rnode {
	if len(sources) == 0 || len(targets) == 0 {
		return nil
	}
	rt.cur++
	if rt.cur == 0 { // wrapped: clear stamps
		for i := range rt.stamp {
			rt.stamp[i] = 0
		}
		rt.cur = 1
	}
	isTarget := map[rnode]bool{}
	for _, t := range targets {
		isTarget[t] = true
	}
	g := rt.r.Grid
	h := func(n rnode) float32 {
		_, x, y := rt.split(n)
		px, py := rt.pos(x, y)
		dx, dy := math.Abs(px-goal.x), math.Abs(py-goal.y)
		d := math.Max(dx, dy) + (math.Sqrt2-1)*math.Min(dx, dy)
		return float32(math.Max(0, d-math.Max(goal.hw, goal.hh)-g))
	}
	q := &pq{}
	for _, s := range sources {
		if rt.stamp[s] == rt.cur {
			continue
		}
		rt.stamp[s], rt.g[s], rt.from[s] = rt.cur, 0, -1
		heap.Push(q, pqItem{s, h(s)})
	}
	for q.Len() > 0 {
		it := heap.Pop(q).(pqItem)
		n := it.n
		if it.f > rt.g[n]+h(n)+1e-4 {
			continue // stale entry
		}
		if isTarget[n] {
			var path []rnode
			for k := n; k >= 0; k = rt.from[k] {
				path = append(path, k)
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return path
		}
		l, x, y := rt.split(n)
		relax := func(m rnode, cost float64) {
			ng := rt.g[n] + float32(cost)
			if rt.stamp[m] == rt.cur && rt.g[m] <= ng {
				return
			}
			rt.stamp[m], rt.g[m], rt.from[m] = rt.cur, ng, n
			heap.Push(q, pqItem{m, ng + h(m)})
		}
		for _, s := range steps {
			xx, yy := x+s[0], y+s[1]
			if xx < 0 || yy < 0 || xx >= rt.nx || yy >= rt.ny {
				continue
			}
			m := rt.node(l, xx, yy)
			if !rt.open(net, m) {
				continue
			}
			step := g
			switch {
			case s[2] == 1:
				step *= math.Sqrt2 * diagonalCost
			case (l == 0) != (s[1] == 0): // F.Cu moving vertically, B.Cu horizontally
				step *= wrongWayCost
			}
			cost := step * (1 + float64(rt.hist[m])) * (1 + rt.pres*float64(rt.othersT(net, m)))
			relax(m, cost)
		}
		// Layer change: a via here.
		c := y*rt.nx + x
		other := rt.node(1-l, x, y)
		if rt.viaOK[c] && rt.open(net, other) && !rt.nearOwnVia(net, x, y) {
			cong := float64(rt.othersV(net, rt.node(0, x, y)) + rt.othersV(net, rt.node(1, x, y)))
			relax(other, viaCost*(1+float64(rt.hist[other]))*(1+rt.pres*cong))
		}
	}
	return nil
}

// nearOwnVia keeps two holes of the same net the hole-to-hole distance apart;
// foreign vias are kept away by the claims.
func (rt *router) nearOwnVia(net, x, y int) bool {
	px, py := rt.pos(x, y)
	min := rt.r.Drill + rt.r.HoleToHole
	for _, v := range rt.vias[net] {
		_, vx, vy := rt.split(v)
		qx, qy := rt.pos(vx, vy)
		if d := math.Hypot(px-qx, py-qy); d > 0 && d < min {
			return true
		}
	}
	return false
}

// overuse counts nodes where a net's copper sits inside another net's
// clearance; with grow set it also raises the history cost there.
func (rt *router) overuse(grow bool) int {
	total := 0
	for _, c := range rt.conns {
		for k, n := range c.path {
			bad := rt.othersT(c.net, n) > 0
			if k > 0 && rt.isLayerChange(c.path[k-1], n) {
				bad = bad || rt.othersV(c.net, n) > 0
			}
			if bad {
				total++
				if grow {
					rt.hist[n] += 1
				}
			}
		}
	}
	return total
}

func (rt *router) netConflicts(net int) int {
	total := 0
	for _, c := range rt.conns {
		if c.net != net {
			continue
		}
		for k, n := range c.path {
			if rt.othersT(c.net, n) > 0 || (k > 0 && rt.isLayerChange(c.path[k-1], n) && rt.othersV(c.net, n) > 0) {
				total++
			}
		}
	}
	return total
}

// result turns the node paths into merged straight segments and vias, with a
// stub from each end to its pad's centre: KiCad connects a track ending
// anywhere on a pad, but a track ending at the centre is what a hand draws.
func (rt *router) result(res RouteResult) RouteResult {
	routedPerNet := map[int]int{}
	for _, c := range rt.conns {
		routedPerNet[c.net]++
		name := rt.netName[c.net]
		pts := make([][3]float64, len(c.path)) // x, y, layer
		for k, n := range c.path {
			l, x, y := rt.split(n)
			px, py := rt.pos(x, y)
			pts[k] = [3]float64{px, py, float64(l)}
		}
		emit := func(a, b [3]float64) {
			if a[0] == b[0] && a[1] == b[1] {
				return
			}
			res.Segments = append(res.Segments, Segment{a[0], a[1], b[0], b[1], int(a[2]), name})
		}
		if c.pads[0] >= 0 {
			p := rt.pads[c.pads[0]]
			emit([3]float64{p.x, p.y, pts[0][2]}, pts[0])
		}
		start := 0
		for k := 1; k < len(pts); k++ {
			if pts[k][2] != pts[k-1][2] {
				emit(pts[start], pts[k-1])
				res.Vias = append(res.Vias, Via{pts[k][0], pts[k][1], name})
				start = k
				continue
			}
			if k+1 < len(pts) && pts[k+1][2] == pts[k][2] &&
				sameDir(pts[start], pts[k], pts[k+1]) {
				continue
			}
			emit(pts[start], pts[k])
			start = k
		}
		last := pts[len(pts)-1]
		p := rt.pads[c.pads[1]]
		emit(last, [3]float64{p.x, p.y, last[2]})
	}
	for _, n := range rt.netOrder() {
		want := len(rt.netPads[n]) - 1
		got := routedPerNet[n]
		res.Routed += got
		if got < want {
			res.Unrouted = append(res.Unrouted, rt.netName[n])
		}
	}
	sort.Strings(res.Unrouted)
	return res
}

// sameDir reports whether b→c continues the direction of a→b.
func sameDir(a, b, c [3]float64) bool {
	dx1, dy1 := sign(b[0]-a[0]), sign(b[1]-a[1])
	dx2, dy2 := sign(c[0]-b[0]), sign(c[1]-b[1])
	return dx1 == dx2 && dy1 == dy2
}

func sign(v float64) int {
	switch {
	case v > 1e-9:
		return 1
	case v < -1e-9:
		return -1
	}
	return 0
}
