package pcbgen

import (
	"math"
	"sort"
	"strings"
)

// Part is one footprint to place. Seed is where its symbol sits on the
// schematic; X, Y, Rot is the footprint origin and orientation Place chose.
type Part struct {
	Comp         Component
	FP           *Footprint
	SeedX, SeedY float64
	X, Y         float64
	Rot          int // 0, 90, 180 or 270, counter-clockwise as KiCad counts it
}

// rotate turns a footprint-local point the way KiCad does. Measured with
// KiCad 10's pcbnew on demos/pic_programmer, at 90° and 180°: a pad at local
// (0, 2.54) on a footprint at 90° lands 2.54 mm to the RIGHT of the origin.
func rotate(x, y float64, rot int) (float64, float64) {
	switch rot {
	case 90:
		return y, -x
	case 180:
		return -x, -y
	case 270:
		return -y, x
	}
	return x, y
}

func (b Box) rotated(rot int) Box {
	x1, y1 := rotate(b.X1, b.Y1, rot)
	x2, y2 := rotate(b.X2, b.Y2, rot)
	return Box{math.Min(x1, x2), math.Min(y1, y2), math.Max(x1, x2), math.Max(y1, y2)}
}

func (b Box) center() (float64, float64) { return (b.X1 + b.X2) / 2, (b.Y1 + b.Y2) / 2 }

func (p *Part) localKeepout() Box { return p.FP.Keepout(p.Comp.Ref).rotated(p.Rot) }
func (p *Part) keepout() Box      { return p.localKeepout().shift(p.X, p.Y) }

// pad returns the board position of a pad.
func (p *Part) pad(num string) (float64, float64, bool) {
	l, ok := p.FP.Pads[num]
	if !ok {
		return 0, 0, false
	}
	x, y := rotate(l[0], l[1], p.Rot)
	return p.X + x, p.Y + y, true
}

const (
	placeGrid = 1.27 // footprint origins land on a 50 mil grid
	// courtyardGap is the free space kept between neighbouring courtyards.
	// DRC only forbids overlap; the gap is for the hand that routes it.
	courtyardGap = 0.5
	// boardMargin is the distance from the outermost courtyard to the edge.
	boardMargin = 2.0
)

var rotations = []int{0, 90, 180, 270}

// placeScales are the schematic-to-board scale factors tried. The schematic
// already groups each IC with its satellites, so its layout is a good seed;
// what it does not know is how big the footprints are.
var placeScales = []float64{0.25, 0.35, 0.5, 0.75, 1.0, 1.25, 1.5}

// PlaceResult reports the chosen placement.
type PlaceResult struct {
	Scale   float64
	HPWL    float64 // half-perimeter wirelength summed over nets, mm
	Outline Box     // the board edge
	Decaps  int     // decoupling capacitors placed at their IC's power pin
	Edge    int     // connectors placed on the board edge
}

// placer holds what every pass needs: which nets touch which part, which
// capacitor decouples which pin, and who sits on the edge.
type placer struct {
	parts  []*Part
	nets   []Net
	byRef  map[string]*Part
	netsOf map[*Part][]int // indices into nets
	decaps map[*Part]decap
	edge   map[*Part]bool
	cx, cy float64 // centroid of the seeds
	sx, sy float64 // spread of the seeds
}

// decap is a decoupling capacitor's assignment: the IC pin it serves and
// which of its own pads is on that pin's net.
type decap struct {
	ic           *Part
	icPin, myPad string
}

// Place positions every part and returns the board outline. Deterministic:
// same parts in, same placement out.
//
// Three rules on top of "seed from the schematic, never overlap", all taken
// from salitronic/eda-agent's placer (Apache-2.0; ideas reimplemented, no code
// copied): every part tries the four rotations and keeps the one with the
// shortest wiring; a decoupling capacitor goes right against its IC's power
// pin; connectors go on the board edge facing the side they sit on in the
// schematic.
func Place(parts []*Part, nets []Net) PlaceResult {
	if len(parts) == 0 {
		return PlaceResult{}
	}
	pl := newPlacer(parts, nets)

	type snapshot struct {
		x, y float64
		rot  int
	}
	best := PlaceResult{HPWL: math.Inf(1)}
	bestCost := math.Inf(1)
	var bestPos []snapshot
	for _, s := range placeScales {
		pl.run(s)
		h := HPWL(parts, nets)
		b := extent(parts)
		// HPWL first; area breaks near-ties (1 mm of wire per 100 mm² of board).
		if cost := h + b.W()*b.H()/100; cost < bestCost {
			bestCost = cost
			best.Scale, best.HPWL = s, h
			bestPos = bestPos[:0]
			for _, p := range parts {
				bestPos = append(bestPos, snapshot{p.X, p.Y, p.Rot})
			}
		}
	}
	for i, p := range parts {
		p.X, p.Y, p.Rot = bestPos[i].x, bestPos[i].y, bestPos[i].rot
	}
	best.Decaps, best.Edge = len(pl.decaps), len(pl.edge)

	// Move the drawing so the board's top-left corner sits at a round
	// coordinate inside KiCad's page.
	b := extent(parts)
	const originX, originY = 100.0, 50.0
	dx := snap(originX+boardMargin-b.X1, placeGrid)
	dy := snap(originY+boardMargin-b.Y1, placeGrid)
	for _, p := range parts {
		p.X += dx
		p.Y += dy
	}
	b = extent(parts)
	best.Outline = Box{
		math.Floor(b.X1 - boardMargin), math.Floor(b.Y1 - boardMargin),
		math.Ceil(b.X2 + boardMargin), math.Ceil(b.Y2 + boardMargin),
	}
	return best
}

func newPlacer(parts []*Part, nets []Net) *placer {
	pl := &placer{parts: parts, nets: nets, byRef: map[string]*Part{},
		netsOf: map[*Part][]int{}, edge: map[*Part]bool{}}
	for _, p := range parts {
		pl.byRef[p.Comp.Ref] = p
		pl.cx += p.SeedX
		pl.cy += p.SeedY
	}
	pl.cx /= float64(len(parts))
	pl.cy /= float64(len(parts))
	for _, p := range parts {
		pl.sx = math.Max(pl.sx, math.Abs(p.SeedX-pl.cx))
		pl.sy = math.Max(pl.sy, math.Abs(p.SeedY-pl.cy))
	}
	for i, n := range nets {
		seen := map[*Part]bool{}
		for _, nd := range n.Nodes {
			if p := pl.byRef[nd.Ref]; p != nil && !seen[p] {
				seen[p] = true
				pl.netsOf[p] = append(pl.netsOf[p], i)
			}
		}
	}
	for _, p := range parts {
		if isEdgeConnector(p.Comp.Ref) && len(parts) > 1 {
			pl.edge[p] = true
		}
	}
	pl.decaps = findDecaps(parts, nets, pl.byRef, pl.edge)
	return pl
}

// run places everything at one seed scale: the core (largest first, each
// decoupling capacitor straight after its IC), then the rotation pass, then
// the connectors around the core.
func (pl *placer) run(scale float64) {
	var core []*Part
	for _, p := range pl.parts {
		p.Rot = 0
		if !pl.edge[p] {
			if _, isDecap := pl.decaps[p]; !isDecap {
				core = append(core, p)
			}
		}
	}
	sort.SliceStable(core, func(i, j int) bool {
		ai := core[i].FP.Courtyard.W() * core[i].FP.Courtyard.H()
		aj := core[j].FP.Courtyard.W() * core[j].FP.Courtyard.H()
		if ai != aj {
			return ai > aj
		}
		return refLess(core[i].Comp.Ref, core[j].Comp.Ref)
	})
	served := map[*Part][]*Part{} // IC -> its capacitors, in reference order
	for _, p := range sortedParts(pl.parts) {
		if d, ok := pl.decaps[p]; ok {
			served[d.ic] = append(served[d.ic], p)
		}
	}

	var placed []*Part
	for _, p := range core {
		lk := p.localKeepout()
		lcx, lcy := lk.center()
		wantX := (p.SeedX-pl.cx)*scale - lcx
		wantY := (p.SeedY-pl.cy)*scale - lcy
		p.X, p.Y = nearestFree(lk, wantX, wantY, boxes(placed))
		placed = append(placed, p)
		for _, c := range served[p] {
			pl.placeDecap(c, placed)
			placed = append(placed, c)
		}
	}
	all := pl.placeConnectors(placed)
	pl.rotatePass(all, served)
}

// placeDecap puts a capacitor against the side of its IC nearest the power
// pin it serves, in whichever rotation brings its own pad closest to that pin.
func (pl *placer) placeDecap(c *Part, placed []*Part) {
	d := pl.decaps[c]
	px, py, ok := d.ic.pad(d.icPin)
	if !ok {
		return
	}
	k := d.ic.keepout()
	bestD := math.Inf(1)
	var bx, by float64
	var brot int
	for _, rot := range rotations {
		c.Rot = rot
		lk := c.localKeepout()
		lcx, lcy := lk.center()
		// Centre just outside the IC's nearest side, level with the pin.
		tx, ty := px, py
		switch nearestSide(k, px, py) {
		case sideLeft:
			tx = k.X1 - courtyardGap - lk.W()/2
		case sideRight:
			tx = k.X2 + courtyardGap + lk.W()/2
		case sideTop:
			ty = k.Y1 - courtyardGap - lk.H()/2
		case sideBottom:
			ty = k.Y2 + courtyardGap + lk.H()/2
		}
		c.X, c.Y = nearestFree(lk, tx-lcx, ty-lcy, boxes(placed))
		mx, my, _ := c.pad(d.myPad)
		if dist := math.Hypot(mx-px, my-py); dist < bestD-1e-9 {
			bestD, bx, by, brot = dist, c.X, c.Y, rot
		}
	}
	c.X, c.Y, c.Rot = bx, by, brot
}

// rotatePass tries the four rotations on every core part in place and keeps
// one only when it shortens the wiring and still fits. A part turns together
// with the capacitors that decouple it, as one rigid group about the part's
// centre: turning the IC alone would carry its power pin away from them. It
// runs last, with everything placed, so it measures real positions;
// connectors keep the orientation their edge chose. Two sweeps: the first
// turn of a part changes what is best for its neighbours.
func (pl *placer) rotatePass(placed []*Part, served map[*Part][]*Part) {
	for sweep := 0; sweep < 2; sweep++ {
		for _, p := range placed {
			if _, isDecap := pl.decaps[p]; isDecap || pl.edge[p] {
				continue
			}
			group := append([]*Part{p}, served[p]...)
			inGroup := map[*Part]bool{}
			for _, m := range group {
				inGroup[m] = true
			}
			var others []Box
			for _, o := range placed {
				if !inGroup[o] {
					others = append(others, o.keepout())
				}
			}
			type pose struct {
				x, y float64
				rot  int
			}
			save := func() []pose {
				out := make([]pose, len(group))
				for k, m := range group {
					out[k] = pose{m.X, m.Y, m.Rot}
				}
				return out
			}
			restore := func(ps []pose) {
				for k, m := range group {
					m.X, m.Y, m.Rot = ps[k].x, ps[k].y, ps[k].rot
				}
			}
			cost := func() float64 {
				total := 0.0
				for _, m := range group {
					total += pl.localHPWL(m)
				}
				return total
			}
			orig := save()
			best, bestCost := orig, cost()
			pcx, pcy := p.keepout().center()
			for _, d := range rotations[1:] {
				restore(orig)
				ok := true
				var mine []Box
				for _, m := range group {
					mcx, mcy := m.keepout().center()
					rx, ry := rotate(mcx-pcx, mcy-pcy, d)
					m.Rot = (m.Rot + d) % 360
					lcx, lcy := m.localKeepout().center()
					m.X, m.Y = snap(pcx+rx-lcx, placeGrid), snap(pcy+ry-lcy, placeGrid)
					k := m.keepout()
					if !fits(k, others) || !fits(k, mine) {
						ok = false
						break
					}
					mine = append(mine, k)
				}
				if ok {
					if c := cost(); c < bestCost-1e-9 {
						best, bestCost = save(), c
					}
				}
			}
			restore(best)
		}
	}
}

// placeConnectors puts each connector just outside the placed core, on the
// side its symbol sits on in the schematic, with its long side along the
// edge, sliding along that edge to the free spot nearest its seed.
// Returns every placed part, connectors included.
func (pl *placer) placeConnectors(placed []*Part) []*Part {
	var conns []*Part
	for _, p := range sortedParts(pl.parts) {
		if pl.edge[p] {
			conns = append(conns, p)
		}
	}
	if len(conns) == 0 {
		return placed
	}
	var core Box
	if len(placed) > 0 {
		core = extent(placed)
	}
	all := append([]*Part{}, placed...)
	ccx, ccy := core.center()
	for _, p := range conns {
		side := pl.seedSide(p)
		bestCost := math.Inf(1)
		var bx, by float64
		var brot int
		for _, rot := range rotations {
			p.Rot = rot
			lk := p.localKeepout()
			alongEdge := lk.H() >= lk.W() // long side vertical
			if (side == sideTop || side == sideBottom) == alongEdge && math.Abs(lk.H()-lk.W()) > 0.01 {
				continue // long side would point into the board
			}
			lcx, lcy := lk.center()
			var x, y float64
			switch side {
			case sideLeft:
				x, y = core.X1-courtyardGap-lk.X2, ccy+(p.SeedY-pl.cy)/pl.spreadY()*core.H()/2-lcy
			case sideRight:
				x, y = core.X2+courtyardGap-lk.X1, ccy+(p.SeedY-pl.cy)/pl.spreadY()*core.H()/2-lcy
			case sideTop:
				x, y = ccx+(p.SeedX-pl.cx)/pl.spreadX()*core.W()/2-lcx, core.Y1-courtyardGap-lk.Y2
			case sideBottom:
				x, y = ccx+(p.SeedX-pl.cx)/pl.spreadX()*core.W()/2-lcx, core.Y2+courtyardGap-lk.Y1
			}
			p.X, p.Y = slideFree(lk, x, y, side == sideLeft || side == sideRight, boxes(all))
			if cost := pl.localHPWL(p); cost < bestCost-1e-9 {
				bestCost, bx, by, brot = cost, p.X, p.Y, rot
			}
		}
		p.X, p.Y, p.Rot = bx, by, brot
		all = append(all, p)
	}
	return all
}

type side int

const (
	sideLeft side = iota
	sideRight
	sideTop
	sideBottom
)

func nearestSide(k Box, x, y float64) side {
	s, d := sideLeft, x-k.X1
	if v := k.X2 - x; v < d {
		s, d = sideRight, v
	}
	if v := y - k.Y1; v < d {
		s, d = sideTop, v
	}
	if v := k.Y2 - y; v < d {
		s = sideBottom
	}
	return s
}

// seedSide is the board edge a connector belongs on: the direction it lies in
// from the centre of the schematic, each axis normalised by the drawing's
// spread so a wide sheet does not send everything left or right.
func (pl *placer) seedSide(p *Part) side {
	dx := (p.SeedX - pl.cx) / pl.spreadX()
	dy := (p.SeedY - pl.cy) / pl.spreadY()
	if math.Abs(dx) >= math.Abs(dy) {
		if dx < 0 {
			return sideLeft
		}
		return sideRight
	}
	if dy < 0 {
		return sideTop
	}
	return sideBottom
}

func (pl *placer) spreadX() float64 { return math.Max(pl.sx, 1) }
func (pl *placer) spreadY() float64 { return math.Max(pl.sy, 1) }

// localHPWL is the wirelength of the nets touching p.
func (pl *placer) localHPWL(p *Part) float64 {
	total := 0.0
	for _, i := range pl.netsOf[p] {
		total += netHPWL(pl.nets[i], pl.byRef)
	}
	return total
}

// isEdgeConnector says whether a reference names something that belongs on
// the board edge: connectors and battery holders.
func isEdgeConnector(ref string) bool {
	prefix, _ := splitRef(ref)
	switch prefix {
	case "J", "P", "CN", "X", "USB", "BT":
		return true
	}
	return false
}

// findDecaps pairs each decoupling capacitor with an IC power pin. A
// decoupling capacitor is a two-pad C between a ground net and a net that
// feeds a power_in pin of a part with three or more pads. Several capacitors
// on one rail spread over that rail's pins, nearest seed first, before any
// pin gets a second one.
func findDecaps(parts []*Part, nets []Net, byRef map[string]*Part, edge map[*Part]bool) map[*Part]decap {
	netOf := map[string]string{} // ref\x00pin -> net
	for _, n := range nets {
		for _, nd := range n.Nodes {
			netOf[nd.Ref+"\x00"+nd.Pin] = n.Name
		}
	}
	type powerPin struct {
		ic  *Part
		pin string
	}
	railPins := map[string][]powerPin{}
	for _, n := range nets {
		if isGroundNet(n.Name) {
			continue
		}
		for _, nd := range n.Nodes {
			ic := byRef[nd.Ref]
			if nd.Type == "power_in" && ic != nil && len(ic.FP.Pads) >= 3 && !edge[ic] {
				railPins[n.Name] = append(railPins[n.Name], powerPin{ic, nd.Pin})
			}
		}
	}
	out := map[*Part]decap{}
	uses := map[powerPin]int{}
	for _, c := range sortedParts(parts) {
		if prefix, _ := splitRef(c.Comp.Ref); prefix != "C" || len(c.FP.Pads) != 2 {
			continue
		}
		var pads []string
		for num := range c.FP.Pads {
			pads = append(pads, num)
		}
		sort.Strings(pads)
		n0, n1 := netOf[c.Comp.Ref+"\x00"+pads[0]], netOf[c.Comp.Ref+"\x00"+pads[1]]
		myPad, rail := "", ""
		switch {
		case isGroundNet(n0) && !isGroundNet(n1):
			myPad, rail = pads[1], n1
		case isGroundNet(n1) && !isGroundNet(n0):
			myPad, rail = pads[0], n0
		default:
			continue
		}
		pins := railPins[rail]
		if len(pins) == 0 {
			continue
		}
		best, bestKey := -1, [2]float64{math.Inf(1), math.Inf(1)}
		for i, pp := range pins {
			key := [2]float64{float64(uses[pp]), math.Hypot(pp.ic.SeedX-c.SeedX, pp.ic.SeedY-c.SeedY)}
			if key[0] < bestKey[0] || (key[0] == bestKey[0] && key[1] < bestKey[1]) {
				best, bestKey = i, key
			}
		}
		pp := pins[best]
		uses[pp]++
		out[c] = decap{ic: pp.ic, icPin: pp.pin, myPad: myPad}
	}
	return out
}

func isGroundNet(name string) bool {
	n := strings.ToUpper(strings.TrimPrefix(name, "/"))
	return strings.Contains(n, "GND") || strings.HasPrefix(n, "VSS") || n == "0V"
}

// nearestFree searches square rings of grid cells around the wanted origin
// and returns the free origin closest to it. The search stops one ring after
// the first hit, since a diagonal cell of ring r can be farther than an axial
// cell of ring r+1.
func nearestFree(lk Box, wantX, wantY float64, placed []Box) (float64, float64) {
	gx, gy := math.Round(wantX/placeGrid), math.Round(wantY/placeGrid)
	bestD := math.Inf(1)
	var bx, by float64
	foundAt := -1
	for r := 0; r < 2000; r++ {
		if foundAt >= 0 && r > foundAt+1 {
			break
		}
		for i := -r; i <= r; i++ {
			for j := -r; j <= r; j++ {
				if max(abs(i), abs(j)) != r {
					continue
				}
				x, y := (gx+float64(i))*placeGrid, (gy+float64(j))*placeGrid
				d := math.Hypot(x-wantX, y-wantY)
				if d < bestD && fits(lk.shift(x, y), placed) {
					bestD, bx, by = d, x, y
					if foundAt < 0 {
						foundAt = r
					}
				}
			}
		}
	}
	return bx, by
}

// slideFree moves along one axis only (vertical when vertical is set) to the
// free grid position nearest the wanted one: a connector stays on its edge.
func slideFree(lk Box, wantX, wantY float64, vertical bool, placed []Box) (float64, float64) {
	gx, gy := snap(wantX, placeGrid), snap(wantY, placeGrid)
	for k := 0; k < 4000; k++ {
		for _, s := range []float64{1, -1} {
			off := float64((k+1)/2) * placeGrid * s
			if k == 0 {
				off = 0
			}
			x, y := gx, gy
			if vertical {
				y += off
			} else {
				x += off
			}
			if fits(lk.shift(x, y), placed) {
				return x, y
			}
		}
	}
	return gx, gy
}

func fits(b Box, placed []Box) bool {
	for _, o := range placed {
		if b.overlaps(o, courtyardGap) {
			return false
		}
	}
	return true
}

func boxes(parts []*Part) []Box {
	out := make([]Box, len(parts))
	for i, p := range parts {
		out[i] = p.keepout()
	}
	return out
}

func sortedParts(parts []*Part) []*Part {
	out := append([]*Part{}, parts...)
	sort.SliceStable(out, func(i, j int) bool { return refLess(out[i].Comp.Ref, out[j].Comp.Ref) })
	return out
}

// HPWL is the half-perimeter wirelength: for each net, half the perimeter of
// the box around its pads. The standard placement cost, and a lower bound on
// the copper the net will need.
func HPWL(parts []*Part, nets []Net) float64 {
	byRef := map[string]*Part{}
	for _, p := range parts {
		byRef[p.Comp.Ref] = p
	}
	total := 0.0
	for _, n := range nets {
		total += netHPWL(n, byRef)
	}
	return total
}

func netHPWL(n Net, byRef map[string]*Part) float64 {
	var b Box
	count := 0
	for _, nd := range n.Nodes {
		p := byRef[nd.Ref]
		if p == nil {
			continue
		}
		x, y, ok := p.pad(nd.Pin)
		if !ok {
			continue
		}
		if count == 0 {
			b = Box{x, y, x, y}
		} else {
			b = b.union(Box{x, y, x, y})
		}
		count++
	}
	if count < 2 {
		return 0
	}
	return b.W() + b.H()
}

func extent(parts []*Part) Box {
	b := parts[0].keepout()
	for _, p := range parts[1:] {
		b = b.union(p.keepout())
	}
	return b
}

func snap(v, g float64) float64 { return math.Round(v/g) * g }

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
