package rules

import (
	"math"
	"strings"

	"mcp-kicad/internal/sexp"
)

// ApplySignalFlow nudges symbols so input/output nets land at the canvas
// margins. A net is "input" when its name matches inputNetPattern (IN, VIN,
// SIG_IN, MIC, …); "output" when it matches outputNetPattern (OUT, VOUT,
// SIG_OUT, AUDIO_OUT, …). Components on input nets are moved to the leftmost
// column of the current bbox; components on output nets to the rightmost.
//
// The function returns the number of symbol references moved. Positions are
// snapped to the 1.27 mm grid before being written back.
func ApplySignalFlow(
	syms []sexp.SchematicSymbol,
	nets []sexp.Net,
	positions map[string][2]float64,
) int {
	if len(positions) == 0 {
		return 0
	}
	// Compute bbox of current layout (non-power symbols only).
	minX, maxX := math.MaxFloat64, -math.MaxFloat64
	for _, s := range syms {
		if strings.HasPrefix(s.LibID, "power:") {
			continue
		}
		if pos, ok := positions[keyFor(s.Reference, s.Unit)]; ok {
			if pos[0] < minX {
				minX = pos[0]
			}
			if pos[0] > maxX {
				maxX = pos[0]
			}
		}
	}
	if minX == math.MaxFloat64 {
		return 0
	}
	const margin = 5.08

	moved := 0
	powerRefs := make(map[string]bool)
	for _, s := range syms {
		if strings.HasPrefix(s.LibID, "power:") {
			powerRefs[s.Reference] = true
		}
	}

	for _, n := range nets {
		var targetX float64
		switch {
		case isInputNetName(n.Name):
			targetX = sexp.SnapGrid(minX - margin)
		case isOutputNetName(n.Name):
			targetX = sexp.SnapGrid(maxX + margin)
		default:
			continue
		}
		for _, p := range n.Pins {
			if powerRefs[p.Reference] {
				continue
			}
			key := keyFor(p.Reference, p.Unit)
			cur, ok := positions[key]
			if !ok {
				continue
			}
			// Only push connectors / discrete refs, not multi-pin ICs (those
			// stay where they are; their boundary pin is handled by routing).
			s := findSym(syms, p.Reference)
			if s != nil && isMultiPinIC(s.LibID) {
				continue
			}
			positions[key] = [2]float64{targetX, cur[1]}
			moved++
		}
	}
	return moved
}

// ApplyRotations sets a rotation for each symmetric 2-pin component (R/C/L)
// based on where its connected non-self neighbours sit. Horizontal layout →
// 90°; vertical layout → 0°. Neighbours' positions come from `positions`.
//
// Op-amps, BJTs, FETs, diodes, and headers are left at their current
// rotation — the LLM picks orientation explicitly when polarity matters.
//
// Returns a per-key rotation map; absent entries mean "leave rotation
// unchanged".
func ApplyRotations(
	syms []sexp.SchematicSymbol,
	nets []sexp.Net,
	positions map[string][2]float64,
) map[string]float64 {
	out := make(map[string]float64)
	for _, sym := range syms {
		if !isSymmetric2Pin(sym.LibID) {
			continue
		}
		key := keyFor(sym.Reference, sym.Unit)
		myPos, ok := positions[key]
		if !ok {
			continue
		}
		neighbours := neighbourPositions(sym.Reference, nets, positions, syms)
		desired := inferRotation(neighbours, myPos)
		if desired < 0 {
			continue
		}
		out[key] = desired
	}
	return out
}

// neighbourPositions collects the layout positions of every component (other
// than `ref` itself) that shares a non-power net with `ref`.
func neighbourPositions(
	ref string,
	nets []sexp.Net,
	positions map[string][2]float64,
	syms []sexp.SchematicSymbol,
) [][2]float64 {
	powerRefs := make(map[string]bool)
	for _, s := range syms {
		if strings.HasPrefix(s.LibID, "power:") {
			powerRefs[s.Reference] = true
		}
	}
	seen := map[string]bool{ref: true}
	var out [][2]float64
	for _, n := range nets {
		on := false
		for _, p := range n.Pins {
			if p.Reference == ref {
				on = true
				break
			}
		}
		if !on {
			continue
		}
		for _, p := range n.Pins {
			if seen[p.Reference] || powerRefs[p.Reference] {
				continue
			}
			seen[p.Reference] = true
			s := findSym(syms, p.Reference)
			if s == nil {
				continue
			}
			if pos, ok := positions[keyFor(p.Reference, s.Unit)]; ok {
				out = append(out, pos)
			}
		}
	}
	return out
}

// inferRotation picks 0° or 90° based on which axis dominates. Returns -1
// when the geometry isn't decisive.
func inferRotation(neighbours [][2]float64, myPos [2]float64) float64 {
	if len(neighbours) == 0 {
		return -1
	}
	var dx, dy float64
	for _, np := range neighbours {
		dx += math.Abs(np[0] - myPos[0])
		dy += math.Abs(np[1] - myPos[1])
	}
	const ratio = 1.5
	if dx > dy*ratio {
		return 90
	}
	if dy > dx*ratio {
		return 0
	}
	return -1
}

func isSymmetric2Pin(libID string) bool {
	switch libID {
	case "Device:R", "Device:R_Small", "Device:R_US",
		"Device:C", "Device:C_Small",
		"Device:L", "Device:L_Small",
		"Device:R_Variable", "Device:Ferrite_Bead":
		return true
	}
	return false
}

func isMultiPinIC(libID string) bool {
	if strings.HasPrefix(libID, "MCU_") ||
		strings.HasPrefix(libID, "Amplifier_Operational:") ||
		strings.HasPrefix(libID, "Regulator_Linear:") ||
		strings.HasPrefix(libID, "Regulator_Switching:") ||
		strings.HasPrefix(libID, "Interface_") ||
		strings.HasPrefix(libID, "Logic_") ||
		strings.HasPrefix(libID, "Memory_") {
		return true
	}
	return false
}

func findSym(syms []sexp.SchematicSymbol, ref string) *sexp.SchematicSymbol {
	for i, s := range syms {
		if s.Reference == ref {
			return &syms[i]
		}
	}
	return nil
}

// isInputNetName matches IN, VIN, INPUT, MIC, SIG.*_IN, *_IN.
func isInputNetName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "IN", "VIN", "INPUT", "MIC", "AUDIO_IN", "SIG_IN":
		return true
	}
	if strings.HasSuffix(n, "_IN") {
		return true
	}
	return false
}

// isOutputNetName matches OUT, VOUT, OUTPUT, *_OUT, AUDIO_OUT.
func isOutputNetName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "OUT", "VOUT", "OUTPUT", "SIG_OUT", "AUDIO_OUT":
		return true
	}
	if strings.HasSuffix(n, "_OUT") {
		return true
	}
	return false
}
