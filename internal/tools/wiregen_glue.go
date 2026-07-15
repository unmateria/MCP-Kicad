package tools

import (
	"math"

	"strconv"
	"strings"

	"mcp-kicad/internal/place2/wiregen"
	"mcp-kicad/internal/sexp"
)

// buildWiregenInputs resolves the connection table into the two views the
// wiregen layer needs: the []wiregen.NetInput (pins with coordinates, keeping
// each pin's ORIGINAL connection-table string so consumed pairs report in the
// same form the router consumes) and the []sexp.Net that cluster.Detect
// expects. Pins that cannot be resolved are skipped.
func buildWiregenInputs(sch *sexp.Schematic, conns []NetConn) ([]wiregen.NetInput, []sexp.Net) {
	syms := sexp.ReadSymbols(sch)
	var wNets []wiregen.NetInput
	var sNets []sexp.Net
	for _, conn := range conns {
		wn := wiregen.NetInput{Name: conn.Net}
		sn := sexp.Net{Name: conn.Net}
		for _, ref := range conn.Pins {
			owner, info, libID, unit, ok := resolveConnPin(syms, ref)
			if !ok {
				continue
			}
			wn.Pins = append(wn.Pins, wiregen.PinInput{
				Ref: ref, Owner: owner, Net: conn.Net,
				X: info.X, Y: info.Y, Dir: info.Direction, LibID: libID,
			})
			sn.Pins = append(sn.Pins, sexp.PinRef{
				Reference: owner, PinNumber: info.Number, PinName: info.Name,
				Unit: unit, Electrical: info.Electrical, LibID: libID,
			})
		}
		if len(wn.Pins) > 0 {
			wNets = append(wNets, wn)
		}
		sn.Dangling = len(sn.Pins) < 2
		sNets = append(sNets, sn)
	}
	return wNets, sNets
}

// resolveConnPin parses a connection-table pin string ("U1.27", "U1.VCC",
// "U1.1.+") and locates the owning symbol + pin, returning the owner ref, the
// resolved PinInfo, the symbol lib_id, and its unit.
func resolveConnPin(syms []sexp.SchematicSymbol, ref string) (owner string, info sexp.PinInfo, libID string, unit int, ok bool) {
	parts := strings.SplitN(ref, ".", 3)
	owner = parts[0]
	var wantUnit int
	var pin string
	switch len(parts) {
	case 2:
		pin = parts[1]
	case 3:
		if u, err := strconv.Atoi(parts[1]); err == nil {
			wantUnit = u
			pin = parts[2]
		} else {
			pin = parts[1] + "." + parts[2]
		}
	default:
		return "", sexp.PinInfo{}, "", 0, false
	}
	for _, s := range syms {
		if s.Reference != owner {
			continue
		}
		if wantUnit != 0 && s.Unit != wantUnit {
			continue
		}
		for _, p := range s.Pins {
			if p.Number == pin || p.Name == pin {
				return owner, p, s.LibID, s.Unit, true
			}
		}
	}
	return "", sexp.PinInfo{}, "", 0, false
}

// buildCompForNet turns the wiregen pin-pairs into, per net, a map from
// connection-table pin string to a component id. Pins joined by wiregen wires
// share an id and are therefore treated as already-connected by the router's
// MST. Nets with no pairs are absent from the result.
func buildCompForNet(pairs []wiregen.Pair) map[string]map[string]int {
	// Per-net union-find over pin strings.
	parent := map[string]map[string]string{}
	var find func(net, x string) string
	find = func(net, x string) string {
		m := parent[net]
		p, ok := m[x]
		if !ok || p == x {
			m[x] = x
			return x
		}
		r := find(net, p)
		m[x] = r
		return r
	}
	for _, pr := range pairs {
		if parent[pr.Net] == nil {
			parent[pr.Net] = map[string]string{}
		}
		ra := find(pr.Net, pr.A)
		rb := find(pr.Net, pr.B)
		parent[pr.Net][ra] = rb
	}
	out := map[string]map[string]int{}
	for net, m := range parent {
		ids := map[string]int{}
		rootID := map[string]int{}
		next := 0
		for x := range m {
			r := find(net, x)
			id, ok := rootID[r]
			if !ok {
				id = next
				next++
				rootID[r] = id
			}
			ids[x] = id
		}
		out[net] = ids
	}
	return out
}

// mstSegments builds a minimum-spanning connection over pins, treating pins in
// the same pre-wired component (comp[ref]) as already joined so no redundant
// edge is emitted between them. Loose pins (absent from comp) are each their
// own component. Deterministic: pins are consumed in input order with
// stable tie-breaking.
func mstSegments(pins []pinPos, comp map[string]int) []routeSegment {
	if len(pins) < 2 {
		return nil
	}
	ids := make([]int, len(pins))
	next := 1 << 20
	for i, p := range pins {
		if comp != nil {
			if id, ok := comp[p.ref]; ok {
				ids[i] = id
				continue
			}
		}
		ids[i] = next
		next++
	}
	var connected, remaining []int
	firstID := ids[0]
	for i := range pins {
		if ids[i] == firstID {
			connected = append(connected, i)
		} else {
			remaining = append(remaining, i)
		}
	}
	var segs []routeSegment
	for len(remaining) > 0 {
		bestDist := math.MaxFloat64
		bestC, rIdx := 0, remaining[0]
		for _, ci := range connected {
			for _, r := range remaining {
				d := math.Abs(pins[ci].x-pins[r].x) + math.Abs(pins[ci].y-pins[r].y)
				if d < bestDist {
					bestDist, bestC, rIdx = d, ci, r
				}
			}
		}
		segs = append(segs, routeSegment{from: pins[bestC], to: pins[rIdx]})
		joinID := ids[rIdx]
		var stillRemaining []int
		for _, r := range remaining {
			if ids[r] == joinID {
				connected = append(connected, r)
			} else {
				stillRemaining = append(stillRemaining, r)
			}
		}
		remaining = stillRemaining
	}
	return segs
}

// compHasMultiple reports whether a per-net component map splits the given pin
// set into more than one component (i.e. the router still has work to do).
func compHasMultiple(pins []pinPos, comp map[string]int) bool {
	if comp == nil {
		return false
	}
	seen := map[int]bool{}
	loose := 0
	for _, p := range pins {
		if id, ok := comp[p.ref]; ok {
			seen[id] = true
		} else {
			loose++
		}
	}
	return len(seen)+loose > 1
}
