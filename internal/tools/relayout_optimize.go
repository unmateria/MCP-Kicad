package tools

import (
	"fmt"
	"os"
	"strings"

	"mcp-kicad/internal/optimize"
	"mcp-kicad/internal/route2"
	"mcp-kicad/internal/sexp"
)

// optPwrLink mirrors the (LibID, Target) pair captured pre-relayout so the
// optimizer can re-place ALL original power-symbol attachments after
// applying winner rotations.
type optPwrLink struct {
	LibID  string
	Target string
}

// optimizeRotations probes alternative rotations for symmetric 2-pin
// components on top of the schematic state already produced by the
// deterministic relayout flow. For each candidate it applies rotations to a
// reparsed copy of the schematic, re-routes the supplied auto-connections
// with route2 (which is crossing-aware), scores the result, and picks the
// best.
//
// Returns the best layout's serialized bytes plus its CostBreakdown. When no
// candidate beats the base by `minImprovementPct` percent, returns the base
// bytes unchanged.
//
// The function is conservative: budget is small (≤ 8 candidates) and runs
// AFTER the main relayout has committed correctness. Worst case the score
// improves marginally; nothing is mutated permanently if the search returns
// the base.
func (e *Env) optimizeRotations(baseBytes []byte, autoConns []NetConn, pwrLinks []optPwrLink) (winnerBytes []byte, breakdown optimize.CostBreakdown, tried int, ok bool) {
	const minImprovementPct = 5.0
	// P4: bumped from 8 → 64 so wider variator chains can actually exhaust
	// their search space. The rotation+swap product still fits well under this
	// for typical demos (≤32 candidates).
	const budget = 64

	// Identify the symmetric 2-pin refs from the base schematic.
	baseSch, err := sexp.ParseSchematic(string(baseBytes))
	if err != nil {
		return baseBytes, optimize.CostBreakdown{}, 0, false
	}
	baseSyms := sexp.ReadSymbols(baseSch)

	// Refs that have ANY #PWR target attached (per pwrLinks) are locked from
	// rotation: changing their orientation would invalidate the stored target
	// position and mis-route the rail symbol to a coord that may coincide
	// with another net (causing an electrical short).
	pwrLockedRefs := make(map[string]bool)
	for _, l := range pwrLinks {
		ref := strings.SplitN(l.Target, ".", 2)[0]
		pwrLockedRefs[ref] = true
	}

	var symmetricRefs []string
	currentRot := make(map[string]float64)
	for _, sym := range baseSyms {
		if !isSymmetric2Pin(sym.LibID) {
			continue
		}
		if pwrLockedRefs[sym.Reference] {
			continue
		}
		symmetricRefs = append(symmetricRefs, sym.Reference)
		currentRot[sym.Reference] = sym.Rotation
	}
	if len(symmetricRefs) == 0 {
		return baseBytes, optimize.CostBreakdown{}, 0, false
	}

	// Build base candidate from the current state.
	base := optimize.Candidate{
		Positions:   make(map[string][2]float64),
		Rotations:   make(map[string]float64),
		Annotations: make(map[string]string),
	}
	for _, sym := range baseSyms {
		key := sym.Reference
		if sym.Unit > 1 {
			key = sym.Reference + "#" + itoaInternal(sym.Unit)
		}
		base.Positions[key] = [2]float64{sym.X, sym.Y}
		base.Rotations[key] = sym.Rotation
	}

	// Materialize: reparse + apply rotations + re-route + score.
	materialize := func(c optimize.Candidate) optimize.Layout {
		sch, err := sexp.ParseSchematic(string(baseBytes))
		if err != nil {
			return optimize.Layout{}
		}
		// Apply rotation overrides.
		for ref, rot := range c.Rotations {
			if cur, ok := currentRot[ref]; ok && cur == rot {
				continue
			}
			sch.SetSymbolRotation(ref, rot)
		}
		// Strip non-power-rail wires, keep labels (so power symbols still
		// connect via implicit nets).
		sch.RemoveWires()
		sch.RemoveJunctions()

		// Re-route via route2.
		syms := sexp.ReadSymbols(sch)
		rt := route2.New(syms, nil)
		// Convert NetConn to (from,to) pairs and route via the legacy router
		// we already have. Since route2.New satisfies route2.Router which has
		// the same Route(...) signature as the legacy router, we can use it
		// directly through routeNets if we adapt — but routeNets takes
		// *router.Router. To keep this minimal, build wires inline.
		wires := routeNetsInMemory(syms, rt, autoConns)
		layoutSyms := sexp.ReadSymbols(sch)
		var optSyms []sexp.SchematicSymbol
		for _, s := range layoutSyms {
			if strings.HasPrefix(s.LibID, "power:") {
				continue
			}
			optSyms = append(optSyms, s)
		}
		return optimize.Layout{Symbols: optSyms, Wires: wires}
	}

	// Build variator: rotation options 0/90 for each symmetric ref.
	options := make(map[string][]float64, len(symmetricRefs))
	for _, ref := range symmetricRefs {
		cur := currentRot[ref]
		alt := 90.0
		if cur == 90 {
			alt = 0
		}
		options[ref] = []float64{cur, alt}
	}
	// P4: keep up to 6 refs in the rotation product (2^6 = 64). Past that the
	// chain variator (rotation → swap) takes over.
	scopedRefs := symmetricRefs
	if len(scopedRefs) > 6 {
		scopedRefs = scopedRefs[:6]
	}
	rotV := optimize.NewRotationVariator(scopedRefs, options)
	swapV := optimize.NewSwapVariator(scopedRefs, base.Positions)
	v := optimize.NewChainVariator(rotV, swapV)
	results, total := optimize.SearchTopK(base, v, materialize, budget, 1)
	tried = total

	if len(results) == 0 {
		return baseBytes, optimize.CostBreakdown{}, tried, false
	}
	best := results[0]
	baseCost := optimize.Cost(materialize(base))
	if best.Cost.Total >= baseCost.Total*(1.0-minImprovementPct/100.0) {
		// No meaningful improvement.
		return baseBytes, baseCost, tried, false
	}

	// Apply winner: reparse base, apply rotations, re-route, serialize.
	winner, err := sexp.ParseSchematic(string(baseBytes))
	if err != nil {
		return baseBytes, baseCost, tried, false
	}
	for ref, rot := range best.Candidate.Rotations {
		if cur, ok := currentRot[ref]; ok && cur == rot {
			continue
		}
		winner.SetSymbolRotation(ref, rot)
	}
	// Re-route in the winning schematic so the wire layout reflects the
	// rotation choice.
	winner.RemoveWires()
	winner.RemoveJunctions()
	winner.RemovePowerSymbols()
	wsyms := sexp.ReadSymbols(winner)
	wrt := route2.New(wsyms, nil)
	wireNodes := routeNetsInMemoryNodes(wsyms, wrt, autoConns)
	for _, w := range wireNodes {
		winner.AddWire(w)
	}
	winner.DedupeWires()
	// Re-place ONLY the original #PWR symbols that were lost in the
	// RemovePowerSymbols call above. Power-anchored refs are locked from
	// rotation, so these re-placements always go to the same pin coord they
	// had pre-optimizer — safe.
	pwrPlaced := make(map[[3]string]bool)
	addedPwr := 0
	for _, t := range pwrLinks {
		pin, ok := sexp.FindPin(winner, t.Target)
		if !ok {
			continue
		}
		key := [3]string{
			t.LibID,
			fmt.Sprintf("%.1f", pin.X),
			fmt.Sprintf("%.1f", pin.Y),
		}
		if pwrPlaced[key] {
			continue
		}
		pwrPlaced[key] = true
		_, ok = e.applyOp(winner, modifySchematicInput{
			Action: "add_power_rail",
			LibID:  t.LibID,
			From:   t.Target,
		}, true)
		if ok {
			addedPwr++
		}
	}
	winner.DedupeWires()
	fmt.Fprintf(os.Stderr, "[optimizer] re-placed %d #PWR symbols across %d targets\n", addedPwr, len(pwrLinks))
	return []byte(winner.Serialize()), best.Cost, tried, true
}

// routeNetsInMemory routes autoConns and returns optimize.Wire segments —
// used by Materialize to score candidates without persisting to disk.
func routeNetsInMemory(syms []sexp.SchematicSymbol, rt route2.Router, conns []NetConn) []optimize.Wire {
	pinByRef := make(map[string][]sexp.PinInfo, len(syms))
	for _, s := range syms {
		pinByRef[s.Reference] = append(pinByRef[s.Reference], s.Pins...)
	}
	resolve := func(refPin string) (x, y float64, ok bool) {
		parts := strings.SplitN(refPin, ".", 3)
		if len(parts) < 2 {
			return 0, 0, false
		}
		ref := parts[0]
		pin := parts[1]
		// Skip the unit prefix when present (e.g. U1.1.+ → ref="U1", pin="+").
		if len(parts) == 3 {
			pin = parts[2]
		}
		for _, p := range pinByRef[ref] {
			if p.Number == pin || p.Name == pin {
				return p.X, p.Y, true
			}
		}
		return 0, 0, false
	}
	var wires []optimize.Wire
	for _, conn := range conns {
		if len(conn.Pins) < 2 {
			continue
		}
		var pts [][2]float64
		for _, ref := range conn.Pins {
			x, y, ok := resolve(ref)
			if !ok {
				continue
			}
			pts = append(pts, [2]float64{sexp.SnapGrid(x), sexp.SnapGrid(y)})
		}
		if len(pts) < 2 {
			continue
		}
		// Daisy-chain routing — same shape as routeNets uses for 2-pin nets.
		for i := 1; i < len(pts); i++ {
			path := rt.Route(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1])
			if path == nil {
				continue
			}
			rt.MarkWire(path)
			for j := 1; j < len(path); j++ {
				wires = append(wires, optimize.Wire{
					X1: path[j-1][0], Y1: path[j-1][1],
					X2: path[j][0], Y2: path[j][1],
				})
			}
		}
	}
	return wires
}

// routeNetsInMemoryNodes is the AST-emitting variant of routeNetsInMemory —
// used when applying the winning candidate to a real schematic.
func routeNetsInMemoryNodes(syms []sexp.SchematicSymbol, rt route2.Router, conns []NetConn) []*sexp.Node {
	pinByRef := make(map[string][]sexp.PinInfo, len(syms))
	for _, s := range syms {
		pinByRef[s.Reference] = append(pinByRef[s.Reference], s.Pins...)
	}
	resolve := func(refPin string) (x, y float64, ok bool) {
		parts := strings.SplitN(refPin, ".", 3)
		if len(parts) < 2 {
			return 0, 0, false
		}
		ref := parts[0]
		pin := parts[1]
		if len(parts) == 3 {
			pin = parts[2]
		}
		for _, p := range pinByRef[ref] {
			if p.Number == pin || p.Name == pin {
				return p.X, p.Y, true
			}
		}
		return 0, 0, false
	}
	var nodes []*sexp.Node
	for _, conn := range conns {
		if len(conn.Pins) < 2 {
			continue
		}
		var pts [][2]float64
		for _, ref := range conn.Pins {
			x, y, ok := resolve(ref)
			if !ok {
				continue
			}
			pts = append(pts, [2]float64{sexp.SnapGrid(x), sexp.SnapGrid(y)})
		}
		if len(pts) < 2 {
			continue
		}
		for i := 1; i < len(pts); i++ {
			path := rt.Route(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1])
			if path == nil {
				continue
			}
			rt.MarkWire(path)
			for j := 1; j < len(path); j++ {
				nodes = append(nodes, sexp.NewWire(path[j-1][0], path[j-1][1], path[j][0], path[j][1]))
			}
		}
	}
	return nodes
}
