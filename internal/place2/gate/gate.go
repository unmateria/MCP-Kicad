// Package gate implements the geometric quality gate: a post-hoc pass over a
// parsed schematic that makes bad wire geometry impossible in the output.
//
// The router (internal/router, internal/route2) is not touched by this
// package — instead of trying to make routing perfect, the gate DETECTS any
// net whose wiring violates a geometric invariant (crosses another net,
// crosses itself without a junction, cuts through a symbol body, overlaps
// another net's wire collinearly, or runs over a foreign pin tip) and
// DEMOTES that one net: its wires and junctions are deleted and replaced
// with a net label at every pin, which has no geometry and therefore cannot
// violate anything. A demoted net stays electrically identical (same pins,
// same net, just drawn without wires).
//
// Guarantee: after gate.Enforce runs, the schematic has zero wire crossings
// between different nets, zero wires through symbol bodies, zero overlapping
// collinear segments of different nets, and no wire running over a pin it
// does not connect to. The loop terminates because every demotion strictly
// removes wire geometry; the worst case is an all-label schematic, which
// trivially has zero violations.
//
// What this package cannot decide is whether the connectivity it preserves
// is the connectivity the design asked for — a wire landing on a foreign pin
// is geometrically impeccable and electrically wrong. That question belongs
// to tools.VerifyNetlist, which compares the emitted netlist against the
// source that declared it.
package gate

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// eps is the coordinate tolerance (mm) used throughout this package —
// consistent with the 2-decimal rounding used across internal/sexp.
const eps = 0.01

// ViolationKind classifies one geometric defect found by Check.
type ViolationKind string

const (
	// CrossNetCrossing: two segments of DIFFERENT nets touch (crossing or
	// T-touch) at a point that is not a legitimate shared connection point.
	// In KiCad, physical wire contact means electrical connection, so this
	// is an electrical short drawn as a wire crossing, not merely ugly.
	CrossNetCrossing ViolationKind = "CROSS_NET_CROSSING"

	// SameNetNoJunction: two segments of the SAME net cross or T-touch at a
	// point with no junction marker — visually ambiguous (looks like it
	// might not be connected, or looks connected when a corner was meant).
	SameNetNoJunction ViolationKind = "SAME_NET_CROSSING_NO_JUNCTION"

	// WireThruSymbol: a wire segment passes through a symbol's body
	// interior (inset from pin tips — legitimate pin connections never
	// trigger this).
	WireThruSymbol ViolationKind = "WIRE_THRU_SYMBOL"

	// WireOverPin: a wire passes over a pin tip in mid-segment. Measured
	// against KiCad 10 (kicad-cli sch export netlist), this connects nothing
	// — connection needs a wire endpoint or a junction — so it is not a
	// short. It is a lie told to the reader: the eye reads that T as a
	// connection. It is also a trap, because adding a junction there, or
	// nudging either part, silently turns it into one.
	WireOverPin ViolationKind = "WIRE_OVER_PIN"

	// CollinearOverlap: two segments of DIFFERENT nets lie on the same line
	// and their intervals overlap — an electrical short drawn as a single
	// line. Same-net collinear overlaps are not a violation; Clean merges
	// them.
	CollinearOverlap ViolationKind = "COLLINEAR_OVERLAP"
)

// Violation is one geometric defect detected by Check.
type Violation struct {
	Kind   ViolationKind
	Net    string // net to blame (primary; the one Enforce will consider demoting)
	Net2   string // the OTHER net involved; "" for single-net violations (WireThruSymbol, SameNetNoJunction)
	Detail string
	X, Y   float64 // where, for the kinds that have a single point (0,0 otherwise)
}

// wireSeg is one wire segment with its net attribution resolved via
// sexp.TracePointNets.
type wireSeg struct {
	node           *sexp.Node
	ax, ay, bx, by float64
	net            string
}

// Check inspects sch and returns every geometric invariant violation found.
// It does not mutate the schematic.
func Check(sch *sexp.Schematic) []Violation {
	netOf := sexp.TracePointNets(sch)
	segs := collectWireSegs(sch, netOf)
	syms := sexp.ReadSymbols(sch)
	junctions := junctionPositions(sch)

	var violations []Violation

	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			a, b := segs[i], segs[j]
			dirA := metrics.SegDir(a.ax, a.ay, a.bx, a.by)
			dirB := metrics.SegDir(b.ax, b.ay, b.bx, b.by)
			if dirA == -1 || dirB == -1 {
				continue // diagonal/degenerate segment — not produced by this codebase's router
			}

			if dirA == dirB {
				if !collinearOverlap(a, b, dirA) {
					continue
				}
				if a.net != b.net {
					violations = append(violations, Violation{
						Kind: CollinearOverlap, Net: a.net, Net2: b.net,
						Detail: fmt.Sprintf("%s and %s wires overlap collinearly", a.net, b.net),
					})
				}
				// Same-net collinear overlaps are cleaned by merging, not a violation.
				continue
			}

			px, py, touch := contactPoint(a, b, dirA)
			if !touch {
				continue
			}
			if a.net != b.net {
				violations = append(violations, Violation{
					Kind: CrossNetCrossing, Net: a.net, Net2: b.net,
					Detail: fmt.Sprintf("%s crosses %s at (%.2f, %.2f)", a.net, b.net, px, py),
				})
				continue
			}
			// Same net: an ordinary two-segment continuation (both segments
			// end exactly at the contact point) needs no junction. Anything
			// else — a segment passing through the other's interior — is a
			// 3-way-or-more meet that needs a junction marker.
			if isEndpoint(a, px, py) && isEndpoint(b, px, py) {
				continue
			}
			if !junctions[round2pt(px, py)] {
				violations = append(violations, Violation{
					Kind: SameNetNoJunction, Net: a.net,
					Detail: fmt.Sprintf("%s crosses itself at (%.2f, %.2f) without a junction", a.net, px, py),
					X:      px, Y: py,
				})
			}
		}
	}

	for _, s := range segs {
		for _, sym := range syms {
			if strings.HasPrefix(sym.LibID, "power:") || sym.LibID == "Device:PWR_FLAG" {
				continue
			}
			// A wire that starts or ends on one of THIS symbol's pins is its
			// connection, not an intrusion. Skipping it matters for parts whose
			// drawn outline encloses its own pin tips — a connector's rectangle
			// extends past its pins, so every wire leaving it technically
			// "passes through the body". That made the gate demote every net
			// touching a connector: on a dual supply it took out GND, +12V and
			// −12V at once, and since a rail's only wires are its power-symbol
			// stubs, twenty symbols were stranded and the sheet came out
			// labelled instead of drawn.
			if touchesOwnPin(sym, s) {
				continue
			}
			x1, y1, x2, y2 := metrics.BodyBBox(sym)
			if sexp.SegmentCrossesBox(s.ax, s.ay, s.bx, s.by, x1, y1, x2, y2) {
				violations = append(violations, Violation{
					Kind: WireThruSymbol, Net: s.net,
					Detail: fmt.Sprintf("%s wire passes through %s body", s.net, sym.Reference),
				})
				break
			}
		}
	}

	for _, s := range segs {
		for _, sym := range syms {
			for _, pin := range sym.Pins {
				if !pinInSegmentInterior(s, pin.X, pin.Y) {
					continue
				}
				if junctions[round2pt(pin.X, pin.Y)] {
					// A junction here DOES connect in KiCad. Whether that is
					// the intended netlist is not a geometric question —
					// tools.VerifyNetlist answers it against the source.
					continue
				}
				pinNet := netOf[round2pt(pin.X, pin.Y)]
				violations = append(violations, Violation{
					Kind: WireOverPin, Net: s.net, Net2: pinNet,
					Detail: fmt.Sprintf("%s wire runs over pin %s.%s (%s) without connecting to it",
						s.net, sym.Reference, pin.Number, pinNetLabel(pinNet)),
					X: pin.X, Y: pin.Y,
				})
			}
		}
	}

	sortViolationsForDisplay(violations)
	return violations
}

// pinInSegmentInterior reports whether a pin tip lies ON a wire segment but
// strictly between its endpoints. Endpoints are excluded because a wire
// ending on a pin is the normal way to connect one.
func pinInSegmentInterior(s wireSeg, px, py float64) bool {
	switch metrics.SegDir(s.ax, s.ay, s.bx, s.by) {
	case 0: // horizontal
		if math.Abs(py-s.ay) > eps {
			return false
		}
		return strictlyBetween(px, s.ax, s.bx)
	case 1: // vertical
		if math.Abs(px-s.ax) > eps {
			return false
		}
		return strictlyBetween(py, s.ay, s.by)
	}
	return false
}

func strictlyBetween(v, a, b float64) bool {
	lo, hi := math.Min(a, b), math.Max(a, b)
	return v > lo+eps && v < hi-eps
}

func pinNetLabel(net string) string {
	if net == "" {
		return "unconnected"
	}
	return "net " + net
}

func collectWireSegs(sch *sexp.Schematic, netOf map[[2]float64]string) []wireSeg {
	var segs []wireSeg
	for _, w := range sch.Wires() {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		segs = append(segs, wireSeg{
			node: w, ax: ax, ay: ay, bx: bx, by: by,
			net: netOf[round2pt(ax, ay)],
		})
	}
	return segs
}

// collinearOverlap reports whether two same-direction segments lie on the
// same line and their intervals genuinely overlap (more than a boundary
// touch — a shared endpoint alone is not an overlap).
func collinearOverlap(a, b wireSeg, dir int) bool {
	if dir == 0 {
		if math.Abs(a.ay-b.ay) > eps {
			return false
		}
		return intervalsOverlap(a.ax, a.bx, b.ax, b.bx)
	}
	if math.Abs(a.ax-b.ax) > eps {
		return false
	}
	return intervalsOverlap(a.ay, a.by, b.ay, b.by)
}

func intervalsOverlap(a1, a2, b1, b2 float64) bool {
	loA, hiA := math.Min(a1, a2), math.Max(a1, a2)
	loB, hiB := math.Min(b1, b2), math.Max(b1, b2)
	lo := math.Max(loA, loB)
	hi := math.Min(hiA, hiB)
	return hi > lo+eps
}

// contactPoint finds where a horizontal and a vertical segment touch,
// inclusive of their endpoints (so T-touches are detected, not just strict
// interior crossings — a wire ending on top of another wire's interior is a
// real electrical connection in KiCad, junction dot or not).
func contactPoint(a, b wireSeg, dirA int) (px, py float64, touch bool) {
	var hx1, hy, hx2, vx, vy1, vy2 float64
	if dirA == 0 {
		hx1, hy, hx2 = a.ax, a.ay, a.bx
		vx, vy1, vy2 = b.ax, b.ay, b.by
	} else {
		vx, vy1, vy2 = a.ax, a.ay, a.by
		hx1, hy, hx2 = b.ax, b.ay, b.bx
	}
	if hx1 > hx2 {
		hx1, hx2 = hx2, hx1
	}
	if vy1 > vy2 {
		vy1, vy2 = vy2, vy1
	}
	if vx >= hx1-eps && vx <= hx2+eps && hy >= vy1-eps && hy <= vy2+eps {
		return vx, hy, true
	}
	return 0, 0, false
}

func isEndpoint(s wireSeg, px, py float64) bool {
	return (math.Abs(s.ax-px) < eps && math.Abs(s.ay-py) < eps) ||
		(math.Abs(s.bx-px) < eps && math.Abs(s.by-py) < eps)
}

func junctionPositions(sch *sexp.Schematic) map[[2]float64]bool {
	set := make(map[[2]float64]bool)
	for _, c := range sch.Root().Children {
		if c.Head() != "junction" {
			continue
		}
		atN := sexp.FindList(c, "at")
		if atN == nil {
			continue
		}
		x, _ := strconv.ParseFloat(sexp.AtomValue(atN, 1), 64)
		y, _ := strconv.ParseFloat(sexp.AtomValue(atN, 2), 64)
		set[round2pt(x, y)] = true
	}
	return set
}

func round2pt(x, y float64) [2]float64 {
	return [2]float64{sexp.Round2(x), sexp.Round2(y)}
}

// sortViolationsForDisplay orders violations deterministically for logging —
// not required for correctness, only for stable test/CLI output.
func sortViolationsForDisplay(v []Violation) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].Kind != v[j].Kind {
			return v[i].Kind < v[j].Kind
		}
		if v[i].Net != v[j].Net {
			return v[i].Net < v[j].Net
		}
		return v[i].Net2 < v[j].Net2
	})
}

// touchesOwnPin reports whether a wire segment ends on a pin of the given
// symbol — the difference between a wire connecting to a part and a wire
// driven straight through it.
func touchesOwnPin(sym sexp.SchematicSymbol, s wireSeg) bool {
	for _, p := range sym.Pins {
		if (math.Abs(p.X-s.ax) < eps && math.Abs(p.Y-s.ay) < eps) ||
			(math.Abs(p.X-s.bx) < eps && math.Abs(p.Y-s.by) < eps) {
			return true
		}
	}
	return false
}
