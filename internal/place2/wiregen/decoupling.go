package wiregen

import (
	"math"

	"mcp-kicad/internal/sexp"
)

// maxDecoupleDist is the center-to-center adjacency precondition (mm) from the
// design brief: a cap further than this from its IC anchor is not treated as a
// bypass cap for formula wiring.
const maxDecoupleDist = 15.0

// decouplingGen wires a bypass cap to the nearest IC pin on each rail the two
// share. Each rail side is an independent straight-or-L segment; the cap is
// only eligible when it sits within maxDecoupleDist of the IC anchor. When the
// current geometry is blocked and the cap carries no wires yet, it is nudged
// onto the IC power pin's row/column so a straight stub opens up.
type decouplingGen struct{}

func (decouplingGen) Handles(kind string) bool {
	return kind == "decoupling"
}

func (decouplingGen) TryWire(gc *genCtx) (wires, juncs []*sexp.Node, pairs []Pair, ok bool) {
	anchor := gc.anchorRef()
	anchorPins := gc.pinsOf(anchor)
	anchorCenter, hasCenter := gc.state.center[anchor]
	for _, cap := range gc.satellites() {
		if hasCenter {
			if c, okc := gc.state.center[cap]; okc {
				d := math.Hypot(c[0]-anchorCenter[0], c[1]-anchorCenter[1])
				if d > maxDecoupleDist {
					continue
				}
			}
		}
		for _, cp := range gc.pinsOf(cap) {
			ap := nearestAnchorPinOnNet(anchorPins, cp)
			if ap == nil {
				continue
			}
			w, done := gc.wireOne(cap, ap, cp)
			if !done {
				continue
			}
			gc.state.commit(w)
			gc.state.locked[cap] = true
			wires = append(wires, w...)
			pairs = append(pairs, Pair{Net: ap.Net, A: ap.Ref, B: cp.Ref})
		}
	}
	return wires, juncs, pairs, len(wires) > 0
}

// nearestAnchorPinOnNet returns the anchor pin sharing capPin's net that is
// closest to capPin, or nil when the anchor has no pin on that net.
func nearestAnchorPinOnNet(anchorPins []*PinInput, capPin *PinInput) *PinInput {
	var best *PinInput
	bestDist := math.MaxFloat64
	for _, ap := range anchorPins {
		if ap.Net == "" || ap.Net != capPin.Net {
			continue
		}
		d := math.Abs(ap.X-capPin.X) + math.Abs(ap.Y-capPin.Y)
		if d < bestDist-eps {
			bestDist = d
			best = ap
		}
	}
	return best
}
