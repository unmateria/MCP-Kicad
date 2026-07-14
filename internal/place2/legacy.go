package place2

import (
	"math"
	"strings"

	"mcp-kicad/internal/sexp"
)

// legacyAutoPlace reproduces the column-fill heuristic from
// internal/tools.autoPlacePosition so AutoPlace stays bug-for-bug compatible
// while the new pipeline phases land. It is unexported and meant to be
// removed once Phase B+C take over.
func legacyAutoPlace(existing []sexp.SchematicSymbol) (float64, float64) {
	var nonPower []sexp.SchematicSymbol
	for _, sym := range existing {
		if !strings.HasPrefix(sym.LibID, "power:") && sym.LibID != "Device:PWR_FLAG" {
			nonPower = append(nonPower, sym)
		}
	}
	if len(nonPower) == 0 {
		return sexp.SnapGrid(50.8), sexp.SnapGrid(50.8)
	}
	maxX := nonPower[0].X
	for _, s := range nonPower[1:] {
		if s.X > maxX {
			maxX = s.X
		}
	}
	const colHalf = 25.0
	const maxPerCol = 4
	var colY []float64
	for _, s := range nonPower {
		if math.Abs(s.X-maxX) < colHalf {
			colY = append(colY, s.Y)
		}
	}
	if len(colY) < maxPerCol {
		maxY := colY[0]
		for _, y := range colY[1:] {
			if y > maxY {
				maxY = y
			}
		}
		return sexp.SnapGrid(maxX), sexp.SnapGrid(maxY + 30.0)
	}
	return sexp.SnapGrid(maxX + 50.0), sexp.SnapGrid(50.8)
}
