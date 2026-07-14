package tools

import (
	"math"

	"mcp-kicad/internal/route2"
)

// steinerSegmentsForNet returns Steiner trunk + stub segments for a net when
// at least three of its pins are colinear (share an X or Y axis within
// 1.27 mm). Returns ok=false otherwise so the caller falls back to MST.
func steinerSegmentsForNet(positions []pinPos) ([]routeSegment, bool) {
	if len(positions) < 3 {
		return nil, false
	}
	pins := make([]route2.Pin, len(positions))
	for i, p := range positions {
		pins[i] = route2.Pin{Ref: p.ref, X: p.x, Y: p.y, Dir: p.dir}
	}
	medX := medianF(coordsX(pins))
	medY := medianF(coordsY(pins))
	const tol = 1.27
	hits := func(axis float64, isY bool) int {
		n := 0
		for _, p := range pins {
			v := p.X
			if isY {
				v = p.Y
			}
			if math.Abs(v-axis) <= tol {
				n++
			}
		}
		return n
	}
	// Conservative gate: only emit a Steiner trunk when ≥4 of the net's pins
	// are colinear AND that's at least 75% of the net. Smaller nets benefit
	// less and the trunk often crosses other geometry the MST would have
	// avoided. Tune up once route2 owns full obstacle awareness.
	hH, hV := hits(medY, true), hits(medX, false)
	best := hH
	if hV > best {
		best = hV
	}
	if best < 4 || float64(best)/float64(len(pins)) < 0.75 {
		return nil, false
	}
	tr, built := route2.BuildSteinerTrunk(pins)
	if !built {
		return nil, false
	}
	out := make([]routeSegment, 0, len(tr.Stubs)+1)
	if tr.Orientation == 'H' {
		out = append(out, routeSegment{
			from: pinPos{ref: "*trunk", x: tr.Min, y: tr.Axis, dir: 0},
			to:   pinPos{ref: "*trunk", x: tr.Max, y: tr.Axis, dir: 0},
		})
	} else {
		out = append(out, routeSegment{
			from: pinPos{ref: "*trunk", x: tr.Axis, y: tr.Min, dir: 90},
			to:   pinPos{ref: "*trunk", x: tr.Axis, y: tr.Max, dir: 90},
		})
	}
	for _, s := range tr.Stubs {
		var src pinPos
		for _, p := range positions {
			if math.Abs(p.x-s[0][0]) < 0.05 && math.Abs(p.y-s[0][1]) < 0.05 {
				src = p
				break
			}
		}
		dst := pinPos{ref: "*trunk", x: s[1][0], y: s[1][1], dir: src.dir}
		out = append(out, routeSegment{from: src, to: dst})
	}
	return out, true
}

func coordsX(pins []route2.Pin) []float64 {
	xs := make([]float64, len(pins))
	for i, p := range pins {
		xs[i] = p.X
	}
	return xs
}
func coordsY(pins []route2.Pin) []float64 {
	ys := make([]float64, len(pins))
	for i, p := range pins {
		ys[i] = p.Y
	}
	return ys
}
func medianF(xs []float64) float64 {
	c := append([]float64(nil), xs...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j-1] > c[j]; j-- {
			c[j-1], c[j] = c[j], c[j-1]
		}
	}
	return c[len(c)/2]
}
