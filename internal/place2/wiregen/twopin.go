package wiregen

import (
	"math"
	"strings"

	"mcp-kicad/internal/sexp"
)

// twoPinGen wires a two-terminal satellite (pull-up resistor, series LED
// resistor, non-power bypass cap) to its anchor across the single SIGNAL net
// they share. Geometry is a straight run when the two pins already share an
// axis, otherwise a single L; when neither is clean the satellite is nudged
// onto the anchor pin's row/column so a straight wire opens up.
type twoPinGen struct{}

func (twoPinGen) Handles(kind string) bool {
	switch kind {
	case "pullup", "series_led", "bypass_nonpower":
		return true
	}
	return false
}

func (twoPinGen) TryWire(gc *genCtx) (wires, juncs []*sexp.Node, pairs []Pair, ok bool) {
	return wireSharedPairs(gc)
}

// wireSharedPairs wires every satellite of the cluster to the anchor over the
// signal net they share. Shared by twoPinGen and dividerGen.
func wireSharedPairs(gc *genCtx) (wires, juncs []*sexp.Node, pairs []Pair, ok bool) {
	anchor := gc.anchorRef()
	anchorPins := gc.pinsOf(anchor)
	for _, sat := range gc.satellites() {
		aPin, sPin := bestSharedPair(anchorPins, gc.pinsOf(sat))
		if aPin == nil {
			continue
		}
		w, done := gc.wireOne(sat, aPin, sPin)
		if !done {
			continue
		}
		gc.state.commit(w)
		gc.state.locked[sat] = true
		wires = append(wires, w...)
		pairs = append(pairs, Pair{Net: aPin.Net, A: aPin.Ref, B: sPin.Ref})
	}
	return wires, juncs, pairs, len(wires) > 0
}

// bestSharedPair picks the anchor pin + satellite pin that lie on a common net,
// preferring a non-power (signal) net and, within that, the closest pair.
func bestSharedPair(anchorPins, satPins []*PinInput) (a, s *PinInput) {
	bestRank := 99
	bestDist := math.MaxFloat64
	for _, ap := range anchorPins {
		for _, sp := range satPins {
			if ap.Net == "" || ap.Net != sp.Net {
				continue
			}
			rank := 0
			if isRailNet(ap.Net) {
				rank = 1
			}
			d := math.Abs(ap.X-sp.X) + math.Abs(ap.Y-sp.Y)
			if rank < bestRank || (rank == bestRank && d < bestDist-eps) {
				bestRank, bestDist = rank, d
				a, s = ap, sp
			}
		}
	}
	// Only wire signal nets by formula; power rails are the power placer's job.
	if a != nil && isRailNet(a.Net) {
		return nil, nil
	}
	return a, s
}

// alignAndConnect snugs the satellite to its canonical spot: sPin placed a
// short gap away from aPin along aPin's outgoing direction, giving a straight
// stub in that direction. This is the "grab the part and tuck it next to the
// anchor pin" move a human makes. It only commits when the resulting wire is
// provably clean (segClear ignoring the satellite's OLD body) and the
// destination bbox is clear; otherwise it declines without moving anything.
func (gc *genCtx) alignAndConnect(sat string, aPin, sPin *PinInput) ([]*sexp.Node, bool) {
	st := gc.state
	if st.locked[sat] {
		return nil, false
	}
	for _, dir := range cardinalsFrom(aPin.Dir) {
		dx, dy := dirDelta(dir)
		for _, gap := range []float64{5.08, 7.62} {
			tx := aPin.X + dx*gap
			ty := aPin.Y + dy*gap
			// Prospective straight stub aPin -> target, ignoring the satellite's
			// current (pre-move) body since it is about to relocate.
			if !st.segClearExcept(sat, aPin.X, aPin.Y, tx, ty) {
				continue
			}
			mdx := tx - sPin.X
			mdy := ty - sPin.Y
			b := st.bodies[sat]
			if !st.destClear(sat, b[0]+mdx, b[1]+mdy, b[2]+mdx, b[3]+mdy) {
				continue
			}
			if !gc.moveSatellite(sat, sPin.Ref, tx, ty) {
				continue
			}
			return []*sexp.Node{sexp.NewWire(aPin.X, aPin.Y, sPin.X, sPin.Y)}, true
		}
	}
	return nil, false
}

// cardinalsFrom returns the outgoing direction(s) to try: the single cardinal
// matching dir, or all four when dir is unknown/diagonal.
func cardinalsFrom(dir float64) []float64 {
	switch int(math.Round(dir)) % 360 {
	case 0:
		return []float64{0}
	case 90:
		return []float64{90}
	case 180:
		return []float64{180}
	case 270:
		return []float64{270}
	}
	return []float64{0, 90, 180, 270}
}

func dirDelta(dir float64) (dx, dy float64) {
	switch int(math.Round(dir)) % 360 {
	case 0:
		return 1, 0
	case 90:
		return 0, -1
	case 180:
		return -1, 0
	case 270:
		return 0, 1
	}
	return 0, 0
}

// isRailNet reports whether a net name is a power rail (handled by the power
// placer, not by formula wiring).
func isRailNet(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	switch u {
	case "GND", "GNDA", "GNDD", "VCC", "VDD", "VEE", "VSS", "VBUS", "AVCC", "AVDD", "EARTH", "0V":
		return true
	}
	return strings.HasPrefix(u, "+") || strings.HasPrefix(u, "-")
}
