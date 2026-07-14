package tools

import (
	"sort"

	"mcp-kicad/internal/place2"
	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/sexp"
)

// unitOffsetMM is the vertical spacing between units of a multi-unit IC when
// the relayout flow stacks them. It must match the unitOffsetY constant in
// schematic.go's relayout case so the synthetic per-unit positions agree
// with where MoveSymbolUnit actually puts each unit.
// 22.86 mm = 18 grid units. Tighter than the original 30mm so the power
// unit visually reads as part of the same IC, while keeping room for V+/V-
// pin labels and the bypass cap row above/below.
const unitOffsetMM = 22.86

// clusterApplyOnPositions pulls cluster satellites toward their anchor in the
// position map produced by layout.PlaceFlow. The map is keyed by REFERENCE
// (no unit suffix); for multi-unit ICs we augment it with synthetic
// "REF#unit" entries that match where MoveSymbolUnit will stack them so
// cluster anchors of the form "U1#3" resolve correctly.
//
// The function MUTATES `positions` in place — synthetic unit entries are
// added but downstream code that only looks up the bare ref keeps working.
func clusterApplyOnPositions(syms []sexp.SchematicSymbol, nets []sexp.Net, positions map[string][2]float64) {
	clusters := cluster.Detect(syms, nets)
	if len(clusters) == 0 {
		return
	}
	expandPositionsByUnit(syms, positions)
	place2.ApplyClusterPull(syms, place2.ConvertClusters(clusters), positions)
}

// expandPositionsByUnit adds "REF#unit" entries for multi-unit ICs alongside
// the existing bare-REF entry. Unit indices are stacked vertically by
// unitOffsetMM (matching schematic.go's relayout) so the synthetic position
// reflects where each unit will physically land.
func expandPositionsByUnit(syms []sexp.SchematicSymbol, positions map[string][2]float64) {
	unitsByRef := make(map[string][]int)
	for _, s := range syms {
		u := s.Unit
		if u <= 0 {
			u = 1
		}
		exists := false
		for _, e := range unitsByRef[s.Reference] {
			if e == u {
				exists = true
				break
			}
		}
		if !exists {
			unitsByRef[s.Reference] = append(unitsByRef[s.Reference], u)
		}
	}
	for ref, units := range unitsByRef {
		if len(units) <= 1 {
			continue
		}
		sort.Ints(units)
		base, ok := positions[ref]
		if !ok {
			continue
		}
		for i, u := range units {
			key := unitPosKey(ref, u)
			positions[key] = [2]float64{base[0], base[1] + float64(i)*unitOffsetMM}
		}
	}
}

// unitPosKey mirrors place2.symKey + cluster.anchorKey so all three layers
// agree on how an anchor encodes its unit.
func unitPosKey(ref string, unit int) string {
	if unit <= 1 {
		return ref
	}
	return ref + "#" + itoaInternal(unit)
}

func itoaInternal(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
