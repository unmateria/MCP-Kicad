package pcbgen

import (
	"fmt"
	"testing"
)

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
	for i, a := range res.Segments {
		for _, b := range res.Segments[i+1:] {
			if a.Net == b.Net || a.Layer != b.Layer {
				continue
			}
			if d := segDist(a.X1, a.Y1, a.X2, a.Y2, b.X1, b.Y1, b.X2, b.Y2); d < (a.Width+b.Width)/2+r.Clearance-1e-6 {
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
				if d < s.Width/2+r.Clearance-1e-6 {
					t.Errorf("%s track %.3f mm from %s.%s", s.Net, d-s.Width/2, p.Comp.Ref, num)
				}
			}
		}
	}
}
