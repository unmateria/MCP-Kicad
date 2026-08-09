package tools

import (
	"math"
	"sort"

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
	horizontal := tr.Orientation == 'H'
	along := func(p pinPos) float64 {
		if horizontal {
			return p.x
		}
		return p.y
	}
	perp := func(p pinPos) float64 {
		if horizontal {
			return p.y
		}
		return p.x
	}
	// point builds a trunk endpoint. When a pin of this net sits exactly there
	// the endpoint IS that pin: the wire has to end on it, and naming it lets
	// the caller record that the wire joined it.
	point := func(at float64, pin *pinPos) pinPos {
		p := pinPos{ref: "*trunk", x: at, y: tr.Axis, dir: 0}
		if !horizontal {
			p = pinPos{ref: "*trunk", x: tr.Axis, y: at, dir: 90}
		}
		if pin != nil {
			p.ref, p.dir = pin.ref, pin.dir
		}
		return p
	}

	// The trunk is CUT at every pin sitting on it, rather than run end to end.
	//
	// A wire crossing a pin tip mid-segment is not a connection — that is
	// measured KiCad behaviour, and the geometric gate reports it as "wire runs
	// over pin X without connecting to it", then demotes the whole net to
	// labels. On the buck converter that turned VIN's four colinear pins into
	// four tags and one wire. Cutting the trunk puts two wire ENDS on the pin,
	// which is what EnsureJunctions needs to draw the solder dot, and what a
	// person drawing this by hand does.
	type cut struct {
		at  float64
		pin *pinPos
	}
	cuts := []cut{{at: tr.Min}, {at: tr.Max}}
	for i := range positions {
		p := &positions[i]
		if math.Abs(perp(*p)-tr.Axis) > 0.05 {
			continue // off the axis: it gets a stub, below
		}
		a := along(*p)
		switch {
		case math.Abs(a-tr.Min) < 0.05:
			cuts[0].pin = p
		case math.Abs(a-tr.Max) < 0.05:
			cuts[1].pin = p
		case a > tr.Min && a < tr.Max:
			cuts = append(cuts, cut{at: a, pin: p})
		}
	}
	sort.SliceStable(cuts, func(i, j int) bool { return cuts[i].at < cuts[j].at })

	out := make([]routeSegment, 0, len(cuts)+len(tr.Stubs))
	for i := 1; i < len(cuts); i++ {
		if cuts[i].at-cuts[i-1].at < 0.05 {
			continue
		}
		out = append(out, routeSegment{
			from: point(cuts[i-1].at, cuts[i-1].pin),
			to:   point(cuts[i].at, cuts[i].pin),
		})
	}
	for _, s := range tr.Stubs {
		var src pinPos
		found := false
		for _, p := range positions {
			if math.Abs(p.x-s[0][0]) < 0.05 && math.Abs(p.y-s[0][1]) < 0.05 {
				src, found = p, true
				break
			}
		}
		// A pin already on the axis has a zero-length stub and is now a trunk
		// endpoint; emitting it would be a wire from a point to itself.
		if !found || (math.Abs(s[0][0]-s[1][0]) < 0.05 && math.Abs(s[0][1]-s[1][1]) < 0.05) {
			continue
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
