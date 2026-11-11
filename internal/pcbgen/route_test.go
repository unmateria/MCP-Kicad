package pcbgen

import (
	"fmt"
	"math"
	"testing"
)

// segDist is the exact distance between two segments.
func segDist(ax, ay, bx, by, cx, cy, dx, dy float64) float64 {
	if segCross(ax, ay, bx, by, cx, cy, dx, dy) {
		return 0
	}
	return math.Min(math.Min(ptSeg(ax, ay, cx, cy, dx, dy), ptSeg(bx, by, cx, cy, dx, dy)),
		math.Min(ptSeg(cx, cy, ax, ay, bx, by), ptSeg(dx, dy, ax, ay, bx, by)))
}

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

// A row of resistors whose nets all cross over each other: every pair of
// tracks of different nets, and every track against every foreign pad, must
// keep the clearance — measured exactly, not on the router's own grid.
func TestRouteKeepsClearance(t *testing.T) {
	fp := mustFP(t, "Lib:R", testR)
	var parts []*Part
	for i := 0; i < 6; i++ {
		parts = append(parts, &Part{Comp: Component{Ref: fmt.Sprintf("R%d", i+1)}, FP: fp,
			X: 10 + float64(i%2)*12.7, Y: 10 + float64(i/2)*3.81})
	}
	// R1.1-R6.2, R2.1-R5.2, R3.1-R4.2…: every net crosses the others.
	var nets []Net
	for i := 0; i < 6; i++ {
		nets = append(nets, Net{Name: fmt.Sprintf("N%d", i), Nodes: []Node{
			{Ref: fmt.Sprintf("R%d", i+1), Pin: "1"}, {Ref: fmt.Sprintf("R%d", 6-i), Pin: "2"}}})
	}
	outline := extent(parts)
	outline = Box{outline.X1 - 6, outline.Y1 - 6, outline.X2 + 6, outline.Y2 + 6}
	r := DefaultRouteRules
	res := Route(parts, nets, outline, r)
	if res.Routed != res.Connections {
		t.Errorf("routed %d of %d; unrouted %v", res.Routed, res.Connections, res.Unrouted)
	}
	need := r.Track + r.Clearance - 1e-6
	for i, a := range res.Segments {
		for _, b := range res.Segments[i+1:] {
			if a.Net == b.Net || a.Layer != b.Layer {
				continue
			}
			if d := segDist(a.X1, a.Y1, a.X2, a.Y2, b.X1, b.Y1, b.X2, b.Y2); d < need {
				t.Errorf("%s and %s tracks %.3f mm apart on layer %d", a.Net, b.Net, d, a.Layer)
			}
		}
	}
	padNet := map[string]string{}
	for _, n := range nets {
		for _, nd := range n.Nodes {
			padNet[nd.Ref+"."+nd.Pin] = n.Name
		}
	}
	for _, s := range res.Segments {
		for _, p := range parts {
			for num, pos := range p.FP.Pads {
				if padNet[p.Comp.Ref+"."+num] == s.Net {
					continue
				}
				// testR pads are 1.6 mm circles.
				d := ptSeg(p.X+pos[0], p.Y+pos[1], s.X1, s.Y1, s.X2, s.Y2) - 0.8
				if d < r.Track/2+r.Clearance-1e-6 {
					t.Errorf("%s track %.3f mm from %s.%s", s.Net, d-r.Track/2, p.Comp.Ref, num)
				}
			}
		}
	}
}
