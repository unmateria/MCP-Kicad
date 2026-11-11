package pcbgen

import (
	"container/heap"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// A two-layer grid router with negotiated congestion — PathFinder
// (McMurchie & Ebeling, "PathFinder: A Negotiation-Based Performance-Driven
// Router for FPGAs", FPGA 1995) on top of A* (Hart, Nilsson & Raphael, 1968),
// followed by a rip-up-and-improve pass. Written from the published
// algorithms; no code from any other router.
//
// Negotiation: every net is routed every iteration while other nets' copper
// is only expensive, not forbidden. Cells that end up shared get dearer each
// round (present cost) and keep a memory of having been fought over (history
// cost), so nets negotiate who goes around. When nobody shares anything the
// routing is legal. What still conflicts after the last iteration is ripped
// out and reported unrouted: the router never hands back copper that breaks a
// clearance.
//
// Improvement: with a legal routing in hand, each net in turn is ripped up
// and routed again with everybody else's copper as a hard wall and a cost
// that charges more for vias and bends. The new route is kept only if it is
// cheaper, so the pass can only straighten the board, never break it.
//
// The search state carries the direction of travel, so a bend costs what it
// costs exactly — the difference between a track that turns once and one that
// zigzags to the same place. Acute turns (135° and reversals) are not allowed.
//
// Legality is geometric and conservative. A trace's centreline runs between
// grid cell centres; every point of it lies within m = grid·√2/2 of one, so
// keeping cell centres (clearance + both half widths + 2m) apart keeps the
// copper (clearance) apart. Pads and vias sit at exact positions and need
// only one m.

// RouteRules are the copper rules the router honours. DefaultRouteRules
// matches what KiCad 10 applies to a new board (measured with pcbnew:
// netclass clearance 0.2, track 0.2, via 0.6/0.3, copper-to-edge 0.5,
// hole-to-hole 0.25), with wider tracks.
type RouteRules struct {
	Track                                   float64 // signal track width
	PowerTrack                              float64 // supply and ground track width, where the pads allow it
	Clearance, Via, Drill, Edge, HoleToHole float64
	Grid                                    float64
	// GroundPlane pours the ground net over the whole back layer.
	GroundPlane bool
	// PlaneSolid joins the plane's pads solidly instead of through thermal
	// reliefs.
	PlaneSolid bool
	// NetWidths overrides the track width of named nets ("HEATER": 2.0), the
	// leading "/" of local net names optional. A requested width is never
	// narrowed: a net that does not fit at it is left unrouted and reported,
	// because a track too thin for its current is worse than no track.
	NetWidths map[string]float64
	// SingleSided routes a board made at home: every track on B.Cu, where
	// through-hole parts are soldered. A connection that cannot be made
	// there crosses over the top as a wire jumper — a straight F.Cu track
	// between two vias, which stand for the jumper's holes. Jumpers never
	// touch a pad and never branch, because a wire link cannot.
	SingleSided bool
}

// jumperCost is what a wire jumper costs in single-sided mode, in mm of
// track: worth a long detour on the copper side, since every jumper is two
// holes and a wire soldered by hand.
const jumperCost = 40.0

func (rt *router) viaCost(w weights) float64 {
	if rt.r.SingleSided {
		return jumperCost
	}
	return w.via
}

// planeGeometry is the ground pour's clearance to other copper, thermal gap
// and spoke width. They follow the board's own clearance and track width, so
// a pour on a home-milled board keeps the same gaps as its tracks.
func (r RouteRules) planeGeometry() (clearance, gap, spoke float64) {
	return math.Max(0.3, r.Clearance), math.Max(0.5, r.Clearance), math.Max(0.5, r.Track)
}

var DefaultRouteRules = RouteRules{
	Track: 0.25, PowerTrack: 0.5,
	Clearance: 0.2, Via: 0.6, Drill: 0.3, Edge: 0.5, HoleToHole: 0.25,
	// 1/8 of the 1.27 mm placement grid: a cell row runs exactly midway
	// between two DIP pins, which is where a hand-routed trace goes.
	Grid:        1.27 / 8,
	GroundPlane: true,
}

// powerPadMin is the smallest pad a power-width track may run into. A
// 0.5 mm track cannot enter a 0.8 mm-pitch QFP pad without crowding its
// neighbours; a net with any such pad routes at signal width.
const powerPadMin = 1.2

// Segment is one straight piece of track.
type Segment struct {
	X1, Y1, X2, Y2 float64
	Layer          int // 0 = F.Cu, 1 = B.Cu
	Width          float64
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
	Iterations  int      // negotiation rounds
	Improved    int      // nets the improvement pass made cheaper
	WideNets    []string // nets wider than signal, "NAME 0.5 mm"
	Narrowed    []string // supply nets that did not fit at power width and went at signal width
	PlaneNet    string   // poured on the back layer, "" for none
}

// weights price a route. Units are millimetres of straight track.
type weights struct {
	via, bend45, bend90, wrongWay, diag float64
}

var (
	negotiateW = weights{via: 3, bend45: 0.4, bend90: 1.0, wrongWay: 1.5, diag: 1.05}
	improveW   = weights{via: 8, bend45: 0.8, bend90: 2.0, wrongWay: 1.5, diag: 1.05}
)

const (
	maxIterations = 40
	improveRounds = 3
	noDir         = 8 // search state before the first step, and after a via
	nDirs         = 9
)

// dirs in circular order, so the turn between two is their index distance.
var dirs = [8][2]int{{1, 0}, {1, -1}, {0, -1}, {-1, -1}, {-1, 0}, {-1, 1}, {0, 1}, {1, 1}}

func turn(a, b int) int {
	d := a - b
	if d < 0 {
		d = -d
	}
	return min(d, 8-d)
}

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
	minDim float64
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
	cells   int
	m       float64 // grid slack

	pads    []routePad
	netName []string
	netPads [][]int
	netCls  []int     // index into widths
	widths  []float64 // distinct track widths in use; widths[0] is the signal width, the rest ascending
	asked   []bool    // per net: width requested by name, never to be narrowed

	// static, per track class: -1 free, net id when only that net's pads
	// are near, -2 blocked
	padOwner [][]int32
	viaOK    []bool // per cell (layer 0 index), via probe vs pads/edge

	// dynamic copper claims: how many copper discs cover a node, for a
	// track probe of each class and for a via probe; own subtracts a net's own
	claimT [][]int32
	claimV []int32
	ownT   [][]map[rnode]int32
	ownV   []map[rnode]int32
	hist   []float32
	// spoke marks the back-layer corridors where the ground plane's thermal
	// spokes leave its pads; other nets pay spokeCost to cross them. Nil
	// without a plane.
	spoke []bool
	plane int // the plane's net id, -1 for none
	pres  float64

	conns []*conn
	vias  [][]rnode // per net, via nodes (layer 0 index)

	// search scratch, per state = node*nDirs + dir
	g     []float32
	from  []int32
	stamp []uint32
	cur   uint32
}

func (rt *router) width(cls int) float64 { return rt.widths[cls] }

// Route routes every net between the placed parts inside the outline. A net
// widened automatically (supply and ground) that fails at its width is routed
// again at signal width; one widened by name is not.
func Route(parts []*Part, nets []Net, outline Box, rules RouteRules) RouteResult {
	narrow := map[string]bool{}
	for {
		rt, res := routeOnce(parts, nets, outline, rules, narrow)
		retry := false
		for _, n := range res.Unrouted {
			id := rt.netID(n)
			if id >= 0 && !rt.asked[id] && rt.netCls[id] != 0 && !narrow[n] {
				narrow[n], retry = true, true
			}
		}
		if !retry {
			for n := range narrow {
				res.Narrowed = append(res.Narrowed, n)
			}
			sort.Strings(res.Narrowed)
			return res
		}
	}
}

func (rt *router) netID(name string) int {
	for id, n := range rt.netName {
		if n == name {
			return id
		}
	}
	return -1
}

func routeOnce(parts []*Part, nets []Net, outline Box, rules RouteRules, narrow map[string]bool) (*router, RouteResult) {
	rt := newRouter(parts, nets, outline, rules, narrow)
	res := RouteResult{}
	order := rt.netOrder()
	for _, n := range order {
		res.Connections += max(0, len(rt.netPads[n])-1)
	}
	if res.Connections == 0 {
		return rt, res
	}
	rt.pres = 0.5
	for it := 1; it <= maxIterations; it++ {
		res.Iterations = it
		for _, n := range order {
			rt.ripUp(n)
			rt.routeNet(n, false, negotiateW)
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
	res.Improved = rt.improve(order)
	// Nets given up whole above may fit now that the rest is tidier.
	for _, n := range order {
		if rt.routedCount(n) == 0 && len(rt.netPads[n]) > 1 {
			rt.routeNet(n, true, improveW)
			if rt.netConflicts(n) > 0 {
				rt.ripUp(n)
			}
		}
	}
	rt.enforceClearance(order)
	return rt, rt.result(res)
}

func newRouter(parts []*Part, nets []Net, outline Box, rules RouteRules, narrow map[string]bool) *router {
	rt := &router{r: rules, outline: outline, m: rules.Grid * math.Sqrt2 / 2}
	rt.nx = int(math.Ceil(outline.W()/rules.Grid)) + 1
	rt.ny = int(math.Ceil(outline.H()/rules.Grid)) + 1
	rt.cells = rt.nx * rt.ny

	padNet := map[string]int{} // ref\x00pin -> net
	var netNodes [][]Node
	for _, n := range nets {
		if len(n.Nodes) == 0 {
			continue
		}
		id := len(rt.netName)
		rt.netName = append(rt.netName, n.Name)
		netNodes = append(netNodes, n.Nodes)
		for _, nd := range n.Nodes {
			padNet[nd.Ref+"\x00"+nd.Pin] = id
		}
	}
	rt.netPads = make([][]int, len(rt.netName))
	for _, p := range sortedParts(parts) {
		for _, ps := range p.FP.PadShapes {
			x, y := rotate(ps.X, ps.Y, p.Rot)
			rp := routePad{x: p.X + x, y: p.Y + y, front: ps.Front, back: ps.Back,
				drill: ps.Drill, npth: ps.NPTH, net: -1, minDim: math.Min(ps.W, ps.H)}
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

	// Each net's width: asked for by name, else power width for a supply
	// net whose pads can all take it, else signal width. One class per
	// distinct width, so class order never depends on map order.
	want := make([]float64, len(rt.netName))
	rt.asked = make([]bool, len(rt.netName))
	for id, name := range rt.netName {
		want[id] = rules.Track
		if w, ok := askedWidth(rules, name, netNodes[id]); ok {
			want[id], rt.asked[id] = w, true
		} else if isPowerNet(name, netNodes[id]) && rules.PowerTrack > rules.Track && !narrow[name] {
			wide := true
			for _, pi := range rt.netPads[id] {
				if rt.pads[pi].minDim < powerPadMin {
					wide = false
				}
			}
			if wide {
				want[id] = rules.PowerTrack
			}
		}
	}
	rt.widths = []float64{rules.Track}
	for _, w := range want {
		if !slices.Contains(rt.widths, w) {
			rt.widths = append(rt.widths, w)
		}
	}
	sort.Float64s(rt.widths[1:])
	rt.netCls = make([]int, len(rt.netName))
	for id, w := range want {
		rt.netCls[id] = slices.Index(rt.widths, w)
	}

	nodes := 2 * rt.cells
	rt.padOwner = make([][]int32, len(rt.widths))
	rt.claimT = make([][]int32, len(rt.widths))
	for k := range rt.widths {
		rt.padOwner[k] = make([]int32, nodes)
		rt.claimT[k] = make([]int32, nodes)
	}
	rt.viaOK = make([]bool, rt.cells)
	rt.claimV = make([]int32, nodes)
	rt.hist = make([]float32, nodes)
	rt.g = make([]float32, nodes*nDirs)
	rt.from = make([]int32, nodes*nDirs)
	rt.stamp = make([]uint32, nodes*nDirs)
	rt.ownT = make([][]map[rnode]int32, len(rt.netName))
	rt.ownV = make([]map[rnode]int32, len(rt.netName))
	for i := range rt.ownT {
		rt.ownT[i] = make([]map[rnode]int32, len(rt.widths))
		for k := range rt.widths {
			rt.ownT[i][k] = map[rnode]int32{}
		}
		rt.ownV[i] = map[rnode]int32{}
	}
	rt.vias = make([][]rnode, len(rt.netName))
	rt.buildStatic()
	rt.plane = -1
	if rules.GroundPlane {
		for id, name := range rt.netName {
			if name == rt.planeNet() {
				rt.plane = id
			}
		}
	}
	if rt.plane >= 0 {
		rt.buildSpokes()
	}
	return rt
}

// spokeCost is what a foreign track pays per step through a thermal spoke
// corridor: enough to go round when there is room, not a wall.
const spokeCost = 6.0

// buildSpokes marks, around every back-layer pad of the plane net, the four
// axis-aligned corridors its thermal relief spokes need: a track of another
// net crossing one starves the relief, and KiCad's DRC rejects a relief with
// fewer than two spokes. A corridor is as wide as a spoke plus clearance on
// both sides and reaches past the thermal gap into the pour.
func (rt *router) buildSpokes() {
	const reach = 1.0
	zoneClear, gap, spokeW := rt.r.planeGeometry()
	half := spokeW/2 + zoneClear + rt.r.Track/2
	rt.spoke = make([]bool, 2*rt.cells)
	for _, pi := range rt.netPads[rt.plane] {
		p := rt.pads[pi]
		if !p.back {
			continue
		}
		ext := math.Max(p.hw, p.hh) + gap + reach
		rt.eachCell(p.x, p.y, ext, func(x, y int, px, py float64) {
			dx, dy := math.Abs(px-p.x), math.Abs(py-p.y)
			if (dx <= half && dy <= p.hh+gap+reach) || (dy <= half && dx <= p.hw+gap+reach) {
				rt.spoke[rt.node(1, x, y)] = true
			}
		})
	}
}

// isPowerNet says whether a net carries supply current: ground, or anything
// a symbol marks as a power input or output.
func isPowerNet(name string, nodes []Node) bool {
	if isGroundNet(name) {
		return true
	}
	for _, nd := range nodes {
		if nd.Type == "power_in" || nd.Type == "power_out" {
			return true
		}
	}
	n := strings.ToUpper(strings.TrimPrefix(name, "/"))
	if strings.HasPrefix(n, "+") || strings.HasPrefix(n, "-") {
		return true
	}
	named := false
	for _, p := range []string{"VCC", "VDD", "VIN", "VBAT", "VBUS"} {
		named = named || strings.HasPrefix(n, p)
	}
	if !named {
		return false
	}
	// A supply-sounding name on a net that feeds a signal pin is a
	// measurement of the supply (VBAT_SENSE into an ADC), not the supply.
	for _, nd := range nodes {
		switch nd.Type {
		case "input", "output", "bidirectional", "tri_state":
			return false
		}
	}
	return true
}

// askedWidth is the width rules.NetWidths gives a net: by its name, with or
// without the leading "/", or by any of its pins as "REF.pin" — the only
// handle on a net KiCad named itself, like Net-(F1-Pad1).
func askedWidth(rules RouteRules, name string, nodes []Node) (float64, bool) {
	if w, ok := rules.NetWidths[name]; ok {
		return w, true
	}
	if w, ok := rules.NetWidths[strings.TrimPrefix(name, "/")]; ok {
		return w, true
	}
	for _, nd := range nodes {
		if w, ok := rules.NetWidths[nd.Ref+"."+nd.Pin]; ok {
			return w, true
		}
	}
	return 0, false
}

// buildStatic marks what no trace or via may approach: other nets' pads, bare
// holes and the board edge.
func (rt *router) buildStatic() {
	r := rt.r
	for y := 0; y < rt.ny; y++ {
		for x := 0; x < rt.nx; x++ {
			px, py := rt.pos(x, y)
			e := math.Min(math.Min(px-rt.outline.X1, rt.outline.X2-px), math.Min(py-rt.outline.Y1, rt.outline.Y2-py))
			c := y*rt.nx + x
			for k := range rt.widths {
				v := int32(-1)
				if e < r.Edge+rt.width(k)/2+rt.m {
					v = -2
				}
				rt.padOwner[k][c] = v
				rt.padOwner[k][rt.cells+c] = v
			}
			rt.viaOK[c] = e >= r.Edge+r.Via/2
		}
	}
	for _, p := range rt.pads {
		clear := r.Clearance
		if p.npth {
			clear = math.Max(clear, r.HoleToHole)
		}
		// A via may not come near ANY pad, own net included: its hole would
		// crowd the pad's hole or sit half on its copper.
		viaR := math.Max(clear+r.Via/2, p.drill/2+r.HoleToHole+r.Drill/2)
		for k := range rt.widths {
			traceR := clear + rt.width(k)/2 + rt.m
			reach := math.Max(traceR, viaR) + math.Max(p.hw, p.hh)
			rt.eachCell(p.x, p.y, reach, func(x, y int, px, py float64) {
				d := p.dist(px, py)
				c := y*rt.nx + x
				if k == 0 && d < viaR {
					rt.viaOK[c] = false
				}
				if d >= traceR {
					return
				}
				for l := 0; l < 2; l++ {
					if !p.onLayer(l) {
						continue
					}
					i := l*rt.cells + c
					switch o := rt.padOwner[k][i]; {
					case p.net < 0, l == 0 && r.SingleSided: // a jumper keeps off every pad
						rt.padOwner[k][i] = -2
					case o == -1:
						rt.padOwner[k][i] = int32(p.net)
					case o != int32(p.net):
						rt.padOwner[k][i] = -2
					}
				}
			})
		}
	}
}

func (rt *router) pos(x, y int) (float64, float64) {
	return rt.outline.X1 + float64(x)*rt.r.Grid, rt.outline.Y1 + float64(y)*rt.r.Grid
}

func (rt *router) node(l, x, y int) rnode { return rnode((l*rt.ny+y)*rt.nx + x) }

func (rt *router) split(n rnode) (l, x, y int) {
	c := int(n)
	l = c / rt.cells
	c %= rt.cells
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
// keeps other nets' track centres (of either width) and via centres out of.
func (rt *router) claim(net int, n rnode, isVia bool, d int32) {
	r := rt.r
	l, x, y := rt.split(n)
	cx, cy := rt.pos(x, y)
	half, slack := rt.width(rt.netCls[net])/2, rt.m
	if isVia {
		half, slack = r.Via/2, 0
	}
	rT := make([]float64, len(rt.widths))
	for k := range rt.widths {
		rT[k] = half + r.Clearance + rt.width(k)/2 + slack + rt.m
	}
	rV := half + r.Clearance + r.Via/2 + slack
	layers := []int{l}
	if isVia {
		layers = []int{0, 1}
	}
	reach := rV
	for _, v := range rT {
		reach = math.Max(reach, v)
	}
	own := rt.ownT[net]
	rt.eachCell(cx, cy, reach, func(xx, yy int, px, py float64) {
		dist := math.Hypot(px-cx, py-cy)
		for _, ll := range layers {
			i := rt.node(ll, xx, yy)
			for k := range rT {
				if dist < rT[k] {
					rt.claimT[k][i] += d
					own[k][i] += d
				}
			}
			if dist < rV {
				rt.claimV[i] += d
				rt.ownV[net][i] += d
			}
		}
	})
}

// others is how much foreign copper covers node i for a probe of net.
func (rt *router) othersT(net int, i rnode) int32 {
	k := rt.netCls[net]
	return rt.claimT[k][i] - rt.ownT[net][k][i]
}
func (rt *router) othersV(net int, i rnode) int32 { return rt.claimV[i] - rt.ownV[net][i] }

func (rt *router) open(net int, i rnode) bool {
	o := rt.padOwner[rt.netCls[net]][i]
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

func (rt *router) ripUp(net int) []*conn {
	var removed []*conn
	kept := rt.conns[:0]
	for _, c := range rt.conns {
		if c.net != net {
			kept = append(kept, c)
			continue
		}
		rt.claimPath(c, -1)
		removed = append(removed, c)
	}
	rt.conns = kept
	rt.vias[net] = nil
	return removed
}

// restore puts back connections removed by ripUp.
func (rt *router) restore(cs []*conn) {
	for _, c := range cs {
		rt.claimPath(c, +1)
		rt.conns = append(rt.conns, c)
		rt.noteVias(c)
	}
}

func (rt *router) noteVias(c *conn) {
	for k := 1; k < len(c.path); k++ {
		if rt.isLayerChange(c.path[k-1], c.path[k]) {
			_, x, y := rt.split(c.path[k])
			rt.vias[c.net] = append(rt.vias[c.net], rt.node(0, x, y))
		}
	}
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

func (rt *router) routedCount(net int) int {
	n := 0
	for _, c := range rt.conns {
		if c.net == net {
			n++
		}
	}
	return n
}

// routeNet grows the net as a tree: from everything already connected, to
// the nearest pad not yet reached. hard makes other nets' copper a wall
// instead of a price. Returns the connections made.
func (rt *router) routeNet(net int, hard bool, w weights) int {
	pads := rt.netPads[net]
	inTree := map[int]bool{pads[0]: true}
	var treeNodes []rnode
	made := 0
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
		for _, n := range treeNodes {
			// A wire jumper cannot branch halfway along.
			if l, _, _ := rt.split(n); l == 1 || !rt.r.SingleSided {
				sources = append(sources, n)
			}
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
		path := rt.search(net, sources, rt.padNodes(next, net), rt.pads[next], hard, w)
		inTree[next] = true
		if path == nil {
			continue
		}
		c := &conn{net: net, path: path, pads: [2]int{rt.padAt(net, path[0]), next}}
		rt.claimPath(c, +1)
		rt.noteVias(c)
		rt.conns = append(rt.conns, c)
		treeNodes = append(treeNodes, path...)
		made++
	}
	return made
}

// improve rips up each net in turn and routes it again against everybody
// else's copper as a wall, at a price that dislikes vias and bends more. The
// new route is kept only when it connects at least as much and costs less.
func (rt *router) improve(order []int) int {
	improved := map[int]bool{}
	for round := 0; round < improveRounds; round++ {
		changed := false
		for _, n := range order {
			before := rt.routedCount(n)
			if before == 0 {
				continue
			}
			oldCost := rt.netCost(n, improveW)
			old := rt.ripUp(n)
			made := rt.routeNet(n, true, improveW)
			if made >= before && rt.netConflicts(n) == 0 && rt.netCost(n, improveW) < oldCost-1e-6 {
				improved[n], changed = true, true
				continue
			}
			rt.ripUp(n)
			rt.restore(old)
		}
		if !changed {
			break
		}
	}
	return len(improved)
}

// netCost prices a net's routing with w, the same way the search does.
func (rt *router) netCost(net int, w weights) float64 {
	total := 0.0
	for _, c := range rt.conns {
		if c.net != net {
			continue
		}
		prev := noDir
		for k := 1; k < len(c.path); k++ {
			la, xa, ya := rt.split(c.path[k-1])
			lb, xb, yb := rt.split(c.path[k])
			if la != lb {
				total += rt.viaCost(w)
				prev = noDir
				continue
			}
			d := dirIndex(xb-xa, yb-ya)
			total += rt.stepCost(la, d, prev, w)
			prev = d
		}
	}
	return total
}

func dirIndex(dx, dy int) int {
	for i, d := range dirs {
		if d[0] == dx && d[1] == dy {
			return i
		}
	}
	return noDir
}

// stepCost is the price of one grid step in direction d on layer l, arriving
// from direction prev, before congestion. Negative means forbidden.
func (rt *router) stepCost(l, d, prev int, w weights) float64 {
	step := rt.r.Grid
	switch {
	case rt.r.SingleSided && l == 0 && prev != noDir && prev != d:
		return -1 // a wire jumper runs straight
	case d%2 == 1:
		step *= math.Sqrt2 * w.diag
	case rt.r.SingleSided:
		// One copper layer: no preferred direction.
	case (l == 0) != (dirs[d][1] == 0): // F.Cu vertical, B.Cu horizontal
		step *= w.wrongWay
	}
	if prev != noDir {
		switch turn(prev, d) {
		case 1:
			step += w.bend45
		case 2:
			step += w.bend90
		case 3, 4:
			return -1
		}
	}
	return step
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
	s int32 // state
	f float32
}
type pq []pqItem

func (q pq) Len() int { return len(q) }
func (q pq) Less(i, j int) bool {
	if q[i].f != q[j].f {
		return q[i].f < q[j].f
	}
	return q[i].s < q[j].s
}
func (q pq) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x any)   { *q = append(*q, x.(pqItem)) }
func (q *pq) Pop() any     { o := *q; it := o[len(o)-1]; *q = o[:len(o)-1]; return it }

// search is A* over (node, direction) from any source to any target node.
// Negotiated: foreign copper costs pres per claim and history. Hard: foreign
// copper is a wall.
func (rt *router) search(net int, sources, targets []rnode, goal routePad, hard bool, w weights) []rnode {
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
	h := func(n rnode) float32 {
		_, x, y := rt.split(n)
		px, py := rt.pos(x, y)
		dx, dy := math.Abs(px-goal.x), math.Abs(py-goal.y)
		d := math.Max(dx, dy) + (math.Sqrt2-1)*math.Min(dx, dy)
		return float32(math.Max(0, d-math.Max(goal.hw, goal.hh)-rt.r.Grid))
	}
	q := &pq{}
	push := func(s int32, g float32, from int32) {
		if rt.stamp[s] == rt.cur && rt.g[s] <= g {
			return
		}
		rt.stamp[s], rt.g[s], rt.from[s] = rt.cur, g, from
		heap.Push(q, pqItem{s, g + h(rnode(s/nDirs))})
	}
	for _, s := range sources {
		push(int32(s)*nDirs+noDir, 0, -1)
	}
	spokes := rt.spoke != nil && net != rt.plane
	congestion := func(m rnode, others int32) (float64, bool) {
		f := 1.0
		if spokes && rt.spoke[m] {
			f = spokeCost
		}
		if hard {
			return f, others == 0
		}
		return f * (1 + float64(rt.hist[m])) * (1 + rt.pres*float64(others)), true
	}
	for q.Len() > 0 {
		it := heap.Pop(q).(pqItem)
		s := it.s
		n, dir := rnode(s/nDirs), int(s%nDirs)
		if it.f > rt.g[s]+h(n)+1e-4 {
			continue // stale entry
		}
		if isTarget[n] {
			var path []rnode
			for k := s; k >= 0; k = rt.from[k] {
				node := rnode(k / nDirs)
				if len(path) == 0 || path[len(path)-1] != node {
					path = append(path, node)
				}
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return path
		}
		l, x, y := rt.split(n)
		for d, st := range dirs {
			xx, yy := x+st[0], y+st[1]
			if xx < 0 || yy < 0 || xx >= rt.nx || yy >= rt.ny {
				continue
			}
			m := rt.node(l, xx, yy)
			if !rt.open(net, m) {
				continue
			}
			base := rt.stepCost(l, d, dir, w)
			if base < 0 {
				continue
			}
			f, ok := congestion(m, rt.othersT(net, m))
			if !ok {
				continue
			}
			push(int32(m)*nDirs+int32(d), rt.g[s]+float32(base*f), s)
		}
		// Layer change: a via here.
		c := y*rt.nx + x
		other := rt.node(1-l, x, y)
		if rt.viaOK[c] && rt.open(net, other) && !rt.nearOwnVia(net, x, y) {
			cong := rt.othersV(net, rt.node(0, x, y)) + rt.othersV(net, rt.node(1, x, y))
			if f, ok := congestion(other, cong); ok {
				push(int32(other)*nDirs+noDir, rt.g[s]+float32(rt.viaCost(w)*f), s)
			}
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
		segs, vias := rt.geometry(c)
		res.Segments = append(res.Segments, segs...)
		res.Vias = append(res.Vias, vias...)
	}
	for _, n := range rt.netOrder() {
		want := len(rt.netPads[n]) - 1
		got := routedPerNet[n]
		res.Routed += got
		if got < want {
			res.Unrouted = append(res.Unrouted, rt.netName[n])
		}
		if rt.netCls[n] != 0 {
			res.WideNets = append(res.WideNets, fmt.Sprintf("%s %.2g mm", rt.netName[n], rt.width(rt.netCls[n])))
		}
	}
	sort.Strings(res.Unrouted)
	sort.Strings(res.WideNets)
	if rt.r.GroundPlane {
		res.PlaneNet = rt.planeNet()
	}
	return res
}

// geometry turns a connection's node path into merged straight segments and
// vias, with a stub from each end to its pad's centre: KiCad connects a track
// ending anywhere on a pad, but a track ending at the centre is what a hand
// draws. The stub is necked down to the pad's smaller side, so a wide supply
// track entering a small pad stays on its copper instead of spilling over it.
func (rt *router) geometry(c *conn) ([]Segment, []Via) {
	var segs []Segment
	var vias []Via
	name := rt.netName[c.net]
	width := rt.width(rt.netCls[c.net])
	pts := make([][3]float64, len(c.path)) // x, y, layer
	for k, n := range c.path {
		l, x, y := rt.split(n)
		px, py := rt.pos(x, y)
		pts[k] = [3]float64{px, py, float64(l)}
	}
	emit := func(a, b [3]float64, w float64) {
		if a[0] == b[0] && a[1] == b[1] {
			return
		}
		segs = append(segs, Segment{a[0], a[1], b[0], b[1], int(a[2]), w, name})
	}
	if c.pads[0] >= 0 {
		p := rt.pads[c.pads[0]]
		emit([3]float64{p.x, p.y, pts[0][2]}, pts[0], math.Min(width, p.minDim))
	}
	start := 0
	for k := 1; k < len(pts); k++ {
		if pts[k][2] != pts[k-1][2] {
			emit(pts[start], pts[k-1], width)
			vias = append(vias, Via{pts[k][0], pts[k][1], name})
			start = k
			continue
		}
		if k+1 < len(pts) && pts[k+1][2] == pts[k][2] && sameDir(pts[start], pts[k], pts[k+1]) {
			continue
		}
		emit(pts[start], pts[k], width)
		start = k
	}
	last := pts[len(pts)-1]
	p := rt.pads[c.pads[1]]
	emit(last, [3]float64{p.x, p.y, last[2]}, math.Min(width, p.minDim))
	return segs, vias
}

// enforceClearance measures the routing exactly (checkClearance) and rips
// out whole nets, worst first, until nothing breaks a clearance. Ripped nets
// get one more try against everybody else as a wall, kept only if exact.
func (rt *router) enforceClearance(order []int) {
	pieces := func() []copper {
		var out []copper
		for _, c := range rt.conns {
			segs, vias := rt.geometry(c)
			for _, sg := range segs {
				cp := copper{ax: sg.X1, ay: sg.Y1, bx: sg.X2, by: sg.Y2, half: sg.Width / 2, net: c.net}
				cp.layers[sg.Layer] = true
				out = append(out, cp)
			}
			for _, v := range vias {
				out = append(out, copper{ax: v.X, ay: v.Y, bx: v.X, by: v.Y, half: rt.r.Via / 2,
					layers: [2]bool{true, true}, net: c.net})
			}
		}
		return out
	}
	var ripped []int
	for {
		bad := rt.checkClearance(pieces())
		if len(bad) == 0 {
			break
		}
		worst, worstN := -1, 0
		for _, n := range order {
			if bad[n] > worstN {
				worst, worstN = n, bad[n]
			}
		}
		rt.ripUp(worst)
		ripped = append(ripped, worst)
	}
	for _, n := range ripped {
		rt.routeNet(n, true, improveW)
		if rt.netConflicts(n) > 0 || len(rt.checkClearance(pieces())) > 0 {
			rt.ripUp(n)
		}
	}
}

// planeNet is the ground net with the most pads, the one worth a pour.
func (rt *router) planeNet() string {
	best, bestN := "", 0
	for id, name := range rt.netName {
		if isGroundNet(name) && (len(rt.netPads[id]) > bestN || (len(rt.netPads[id]) == bestN && name < best)) {
			best, bestN = name, len(rt.netPads[id])
		}
	}
	return best
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
