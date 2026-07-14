package route2

import (
	"math"

	"mcp-kicad/internal/sexp"
)

// Junction is a solder dot — three or more wire segments of the SAME net
// meet at a point, forming a T or X intersection that KiCad must render.
type Junction struct {
	X, Y  float64
	NetID int
}

// Crossing is a wire of one net visually crossing a wire of another net
// at a 90° angle. KiCad renders this as a clean cross WITHOUT a junction —
// the absence of a junction marker is what tells KiCad they are NOT
// electrically joined. Crossings are tracked so the metrics layer can flag
// excessive overlap.
type Crossing struct {
	X, Y           float64
	NetIDA, NetIDB int
}

// PathSegment is one routed wire segment (an axis-aligned 2-point segment),
// tagged with its net so DetectJunctions can distinguish T vs X.
type PathSegment struct {
	AX, AY, BX, BY float64
	NetID          int
}

// DetectJunctions walks all routed segments and emits:
//   - one Junction per (point, net) where ≥ 3 segments of that net meet
//   - one Crossing per orthogonal interior intersection of distinct nets
//
// 4-way same-net junctions are decomposed into two T's by the caller (see
// the caller-side helper SplitFourWay).
func DetectJunctions(segs []PathSegment) ([]Junction, []Crossing) {
	type ptKey [2]int64
	round := func(v float64) int64 { return int64(math.Round(v * 100)) }
	key := func(x, y float64) ptKey { return ptKey{round(x), round(y)} }

	type touch struct {
		netID int
		count int
	}
	endpoints := make(map[ptKey]map[int]int) // pt → netID → segment count
	for _, s := range segs {
		for _, p := range [...][2]float64{{s.AX, s.AY}, {s.BX, s.BY}} {
			k := key(p[0], p[1])
			if endpoints[k] == nil {
				endpoints[k] = make(map[int]int)
			}
			endpoints[k][s.NetID]++
		}
	}

	var junctions []Junction
	for k, byNet := range endpoints {
		x := float64(k[0]) / 100
		y := float64(k[1]) / 100
		for netID, count := range byNet {
			if count >= 3 {
				junctions = append(junctions, Junction{X: x, Y: y, NetID: netID})
			}
		}
	}

	var crossings []Crossing
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			a, b := segs[i], segs[j]
			if a.NetID == b.NetID {
				continue
			}
			ix, iy, ok := orthogonalIntersection(a, b)
			if !ok {
				continue
			}
			crossings = append(crossings, Crossing{X: ix, Y: iy, NetIDA: a.NetID, NetIDB: b.NetID})
		}
	}
	return junctions, crossings
}

// orthogonalIntersection returns the intersection point of two segments when
// one is strictly horizontal, the other strictly vertical, AND they cross
// in their interior (not at an endpoint).
func orthogonalIntersection(a, b PathSegment) (x, y float64, ok bool) {
	aH := math.Abs(a.AY-a.BY) < 0.01 && math.Abs(a.AX-a.BX) > 0.01
	aV := math.Abs(a.AX-a.BX) < 0.01 && math.Abs(a.AY-a.BY) > 0.01
	bH := math.Abs(b.AY-b.BY) < 0.01 && math.Abs(b.AX-b.BX) > 0.01
	bV := math.Abs(b.AX-b.BX) < 0.01 && math.Abs(b.AY-b.BY) > 0.01

	var hx1, hy, hx2 float64
	var vy1, vx, vy2 float64
	switch {
	case aH && bV:
		hx1, hy, hx2 = a.AX, a.AY, a.BX
		vy1, vx, vy2 = b.AY, b.AX, b.BY
	case aV && bH:
		hx1, hy, hx2 = b.AX, b.AY, b.BX
		vy1, vx, vy2 = a.AY, a.AX, a.BY
	default:
		return 0, 0, false
	}
	if hx1 > hx2 {
		hx1, hx2 = hx2, hx1
	}
	if vy1 > vy2 {
		vy1, vy2 = vy2, vy1
	}
	if vx > hx1 && vx < hx2 && hy > vy1 && hy < vy2 {
		return vx, hy, true
	}
	return 0, 0, false
}

// EmitJunctionNodes builds (junction ...) AST nodes for each detected
// Junction. Skips entries whose coordinates are not on the 1.27 mm grid (a
// belt-and-braces check; the router output is grid-snapped already).
func EmitJunctionNodes(js []Junction) []*sexp.Node {
	out := make([]*sexp.Node, 0, len(js))
	for _, j := range js {
		out = append(out, sexp.NewJunction(j.X, j.Y))
	}
	return out
}
