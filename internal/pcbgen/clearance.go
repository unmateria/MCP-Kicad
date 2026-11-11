package pcbgen

import "math"

// The router's grid model is conservative, but it reasons about cell centres;
// the copper it hands back is segments with widths and pads with shapes.
// checkClearance measures that copper exactly, the way a DRC would, so a gap
// in the grid model shows up here instead of in KiCad's report.

// ptSeg is the distance from point p to segment ab.
func ptSeg(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	l := vx*vx + vy*vy
	t := 0.0
	if l > 0 {
		t = math.Max(0, math.Min(1, ((px-ax)*vx+(py-ay)*vy)/l))
	}
	return math.Hypot(px-(ax+t*vx), py-(ay+t*vy))
}

func segCross(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	o := func(px, py, qx, qy, rx, ry float64) float64 { return (qx-px)*(ry-py) - (qy-py)*(rx-px) }
	d1, d2 := o(cx, cy, dx, dy, ax, ay), o(cx, cy, dx, dy, bx, by)
	d3, d4 := o(ax, ay, bx, by, cx, cy), o(ax, ay, bx, by, dx, dy)
	return d1*d2 < 0 && d3*d4 < 0
}

// segDist is the distance between segments ab and cd.
func segDist(ax, ay, bx, by, cx, cy, dx, dy float64) float64 {
	if segCross(ax, ay, bx, by, cx, cy, dx, dy) {
		return 0
	}
	return math.Min(math.Min(ptSeg(ax, ay, cx, cy, dx, dy), ptSeg(bx, by, cx, cy, dx, dy)),
		math.Min(ptSeg(cx, cy, ax, ay, bx, by), ptSeg(dx, dy, ax, ay, bx, by)))
}

// padSegDist is the distance from a pad's copper to segment ab.
func (p routePad) padSegDist(ax, ay, bx, by float64) float64 {
	if p.round {
		return math.Max(0, ptSeg(p.x, p.y, ax, ay, bx, by)-p.hw)
	}
	x1, y1, x2, y2 := p.x-p.hw, p.y-p.hh, p.x+p.hw, p.y+p.hh
	inside := func(x, y float64) bool { return x >= x1 && x <= x2 && y >= y1 && y <= y2 }
	if inside(ax, ay) || inside(bx, by) {
		return 0
	}
	return math.Min(math.Min(segDist(ax, ay, bx, by, x1, y1, x2, y1), segDist(ax, ay, bx, by, x2, y1, x2, y2)),
		math.Min(segDist(ax, ay, bx, by, x2, y2, x1, y2), segDist(ax, ay, bx, by, x1, y2, x1, y1)))
}

// copper is one piece of a net's routing for the exact check: a segment, or
// a via as a zero-length segment on both layers.
type copper struct {
	ax, ay, bx, by float64
	half           float64 // half width, or via radius
	layers         [2]bool
	net            int
}

// checkClearance returns, per net, how many of its pieces of copper come
// closer than the clearance to another net's copper or pads. Bare holes keep
// the hole-to-hole distance as well.
func (rt *router) checkClearance(pieces []copper) map[int]int {
	bad := map[int]int{}
	c := rt.r.Clearance - 1e-6
	for i, a := range pieces {
		for _, b := range pieces[i+1:] {
			if a.net == b.net || !((a.layers[0] && b.layers[0]) || (a.layers[1] && b.layers[1])) {
				continue
			}
			if segDist(a.ax, a.ay, a.bx, a.by, b.ax, b.ay, b.bx, b.by) < a.half+b.half+c {
				bad[a.net]++
				bad[b.net]++
			}
		}
		for _, p := range rt.pads {
			if p.net == a.net && !p.npth {
				continue
			}
			if !((a.layers[0] && p.onLayer(0)) || (a.layers[1] && p.onLayer(1))) {
				continue
			}
			need := c
			if p.npth {
				need = math.Max(c, rt.r.HoleToHole)
			}
			if p.padSegDist(a.ax, a.ay, a.bx, a.by) < a.half+need {
				bad[a.net]++
			}
		}
	}
	return bad
}
