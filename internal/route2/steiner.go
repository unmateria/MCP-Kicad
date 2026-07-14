package route2

import (
	"math"
	"sort"
)

// Trunk is the rectilinear Steiner-tree result for a single net: a single
// trunk segment (Axis is the shared X or Y) plus orthogonal stubs from each
// off-trunk pin onto it.
type Trunk struct {
	Orientation byte    // 'H' (horizontal trunk) or 'V' (vertical trunk)
	Axis        float64 // Y for H, X for V
	Min, Max    float64 // span of the trunk along the OTHER axis
	Stubs       [][2][2]float64
}

// BuildSteinerTrunk picks the best rectilinear Steiner trunk for `pins`. It
// chooses between H and V trunks by minimum total wirelength (trunk span
// plus all stub lengths) and snaps the trunk axis to the median pin
// coordinate (a known optimum for the rectilinear Steiner case).
//
// Returns ok=false when fewer than two pins are supplied.
func BuildSteinerTrunk(pins []Pin) (Trunk, bool) {
	if len(pins) < 2 {
		return Trunk{}, false
	}
	hCost, hAxis, hMin, hMax := evalTrunk(pins, 'H')
	vCost, vAxis, vMin, vMax := evalTrunk(pins, 'V')
	if hCost <= vCost {
		return Trunk{
			Orientation: 'H',
			Axis:        hAxis,
			Min:         hMin,
			Max:         hMax,
			Stubs:       buildStubs(pins, 'H', hAxis),
		}, true
	}
	return Trunk{
		Orientation: 'V',
		Axis:        vAxis,
		Min:         vMin,
		Max:         vMax,
		Stubs:       buildStubs(pins, 'V', vAxis),
	}, true
}

// evalTrunk returns (totalCost, trunkAxis, trunkMin, trunkMax) for placing
// the trunk along the given orientation through the median axis coord.
func evalTrunk(pins []Pin, orient byte) (cost, axis, min, max float64) {
	axisVals := make([]float64, len(pins))
	otherMin, otherMax := math.MaxFloat64, -math.MaxFloat64
	for i, p := range pins {
		var ax, ot float64
		if orient == 'H' {
			ax, ot = p.Y, p.X
		} else {
			ax, ot = p.X, p.Y
		}
		axisVals[i] = ax
		if ot < otherMin {
			otherMin = ot
		}
		if ot > otherMax {
			otherMax = ot
		}
	}
	sort.Float64s(axisVals)
	axis = axisVals[len(axisVals)/2]
	min, max = otherMin, otherMax
	cost = max - min
	for _, a := range axisVals {
		cost += math.Abs(a - axis)
	}
	return
}

func buildStubs(pins []Pin, orient byte, axis float64) [][2][2]float64 {
	stubs := make([][2][2]float64, 0, len(pins))
	for _, p := range pins {
		if orient == 'H' {
			if math.Abs(p.Y-axis) < 1e-6 {
				continue
			}
			stubs = append(stubs, [2][2]float64{{p.X, p.Y}, {p.X, axis}})
		} else {
			if math.Abs(p.X-axis) < 1e-6 {
				continue
			}
			stubs = append(stubs, [2][2]float64{{p.X, p.Y}, {axis, p.Y}})
		}
	}
	return stubs
}

// TrunkSegments returns the trunk + stubs as a flat list of point pairs ready
// to convert into wires. Each segment is [from, to].
func (t Trunk) TrunkSegments() [][2][2]float64 {
	out := make([][2][2]float64, 0, len(t.Stubs)+1)
	if t.Orientation == 'H' {
		out = append(out, [2][2]float64{{t.Min, t.Axis}, {t.Max, t.Axis}})
	} else {
		out = append(out, [2][2]float64{{t.Axis, t.Min}, {t.Axis, t.Max}})
	}
	out = append(out, t.Stubs...)
	return out
}

// JunctionPoints returns the trunk-stub intersection points where a KiCad
// junction marker is required.
func (t Trunk) JunctionPoints() [][2]float64 {
	pts := make([][2]float64, 0, len(t.Stubs))
	for _, s := range t.Stubs {
		var pt [2]float64
		if t.Orientation == 'H' {
			pt = [2]float64{s[1][0], t.Axis}
		} else {
			pt = [2]float64{t.Axis, s[1][1]}
		}
		pts = append(pts, pt)
	}
	return pts
}
