package tools

import (
	"mcp-kicad/internal/optimize"
	"mcp-kicad/internal/sexp"
)

// scoreSchematicLayout computes a CostBreakdown for the schematic in its
// current state. Used by the relayout flow to surface a quality score that
// can be tracked across iterations of the optimizer.
//
// The computation is read-only: it captures the symbols + wires already in
// the AST and feeds them to optimize.Cost.
func scoreSchematicLayout(sch *sexp.Schematic) optimize.CostBreakdown {
	syms := sexp.ReadSymbols(sch)
	var optSyms []sexp.SchematicSymbol
	for _, s := range syms {
		// Power symbols are tiny anchors with one pin; the cost function
		// already filters them via len(Pins) < 2 so we keep them in.
		optSyms = append(optSyms, s)
	}
	wireNodes := sch.Wires()
	wires := make([]optimize.Wire, 0, len(wireNodes))
	for _, w := range wireNodes {
		ax, ay, bx, by := wireEndpoints(w)
		wires = append(wires, optimize.Wire{X1: ax, Y1: ay, X2: bx, Y2: by})
	}
	return optimize.Cost(optimize.Layout{Symbols: optSyms, Wires: wires})
}
