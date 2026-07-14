package layout

import (
	"sort"
	"strings"

	"mcp-kicad/internal/sexp"
)

// IntentNet is the user's original (pre-tracing) pin ordering for one net,
// passed in from connect_netlist via relayout. PlaceFlow uses this when
// available to pick the DAG source faithfully — net.Pins from TraceNets has
// already been shuffled into schematic-add-order, which loses the LLM's
// signal-flow intent.
type IntentNet struct {
	Name string
	// Pins are reference designators (NOT REF.pin) in the user's original
	// upstream→downstream order.
	Refs []string
}

// PlaceFlow lays symbols out left-to-right by following directed signal flow,
// where each net's FIRST pin is treated as the upstream source and the rest
// as downstream sinks. Cycles are broken by ignoring "ground-like" return
// nets (GND, GNDA, EARTH…), which lets a typical battery → resistor → load
// loop topology layer cleanly without producing a useless flat layer of
// "everything connected to the battery".
//
// Layer assignment: longest-path-from-source on the cycle-broken DAG.
// Within a layer, components stack vertically; barycenter-of-neighbours sets
// the order so connected pairs land near each other.
//
// When the resulting graph has multiple disconnected components (e.g. a
// stray decoupling cap not yet wired), the unconnected refs are appended to
// the rightmost layer.
//
// `intent` (optional) supersedes net.Pins ordering when present, so callers
// that know the original LLM-supplied ordering should pass it through.
func PlaceFlow(symbols []sexp.SchematicSymbol, nets []sexp.Net, intent []IntentNet) map[string][2]float64 {
	if len(symbols) == 0 {
		return nil
	}

	refs := deduplicateRefs(symbols)
	refSet := make(map[string]bool, len(refs))
	for _, r := range refs {
		refSet[r] = true
	}

	// Index intent by net name for O(1) lookup.
	intentByName := make(map[string][]string, len(intent))
	for _, in := range intent {
		intentByName[strings.ToUpper(strings.TrimSpace(in.Name))] = in.Refs
	}

	// Build directed adjacency: first pin of each non-ground net → subsequent pins.
	out := make(map[string]map[string]bool, len(refs)) // out[a] = set of b where a→b
	inEdges := make(map[string]map[string]bool, len(refs))
	for _, ref := range refs {
		out[ref] = make(map[string]bool)
		inEdges[ref] = make(map[string]bool)
	}
	for _, net := range nets {
		if isReturnNet(net) {
			continue
		}
		// Prefer the user's original ordering when we have it; this is the
		// only reliable signal-flow hint. TraceNets reorders by ReadSymbols
		// iteration, which has no semantic meaning.
		var present []string
		seen := make(map[string]bool)
		if hint, ok := intentByName[strings.ToUpper(strings.TrimSpace(net.Name))]; ok {
			for _, ref := range hint {
				if refSet[ref] && !seen[ref] {
					present = append(present, ref)
					seen[ref] = true
				}
			}
		} else {
			for _, pin := range net.Pins {
				if refSet[pin.Reference] && !seen[pin.Reference] {
					present = append(present, pin.Reference)
					seen[pin.Reference] = true
				}
			}
		}
		if len(present) < 2 {
			continue
		}
		src := present[0]
		for _, dst := range present[1:] {
			if dst == src {
				continue
			}
			out[src][dst] = true
			inEdges[dst][src] = true
		}
	}

	// Sources = nodes with no incoming edges. If none (everything cycles),
	// fall back to the symbol with the lowest reference designator.
	var sources []string
	for _, ref := range refs {
		if len(inEdges[ref]) == 0 {
			sources = append(sources, ref)
		}
	}
	if len(sources) == 0 {
		sources = []string{refs[0]}
	}

	// Longest-path layer from any source. Iterative relaxation; safe against
	// any residual cycle because the GND-net filter normally breaks them.
	layer := make(map[string]int, len(refs))
	for _, ref := range refs {
		layer[ref] = 0
	}
	for _, src := range sources {
		layer[src] = 0
	}
	// Relax up to len(refs) times — enough for any DAG.
	for iter := 0; iter < len(refs); iter++ {
		changed := false
		for _, ref := range refs {
			for next := range out[ref] {
				if layer[next] < layer[ref]+1 {
					layer[next] = layer[ref] + 1
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	// Group by layer.
	byLayer := make(map[int][]string)
	for _, ref := range refs {
		byLayer[layer[ref]] = append(byLayer[layer[ref]], ref)
	}

	// Within-layer ordering: barycenter of upstream neighbours for stability,
	// fall back to lexicographic.
	for l := range byLayer {
		group := byLayer[l]
		sort.SliceStable(group, func(i, j int) bool {
			bi := flowBarycenter(group[i], inEdges, layer)
			bj := flowBarycenter(group[j], inEdges, layer)
			if bi != bj {
				return bi < bj
			}
			return group[i] < group[j]
		})
	}

	// Assign coordinates with the same spacing constants as Sugiyama Place().
	result := make(map[string][2]float64)
	maxL := 0
	for l := range byLayer {
		if l > maxL {
			maxL = l
		}
	}
	for l := 0; l <= maxL; l++ {
		for i, ref := range byLayer[l] {
			x := sexp.SnapGrid(originX + float64(l)*spacingX)
			y := sexp.SnapGrid(originY + float64(i)*spacingY)
			result[ref] = [2]float64{x, y}
		}
	}
	return result
}

// isReturnNet returns true for nets whose name suggests current return (GND,
// GNDA, GNDD, EARTH, 0V…). These are skipped during DAG construction so a
// battery+R+R+LED loop becomes a tree (BT1 → R1 → {R2, D1}) instead of a
// 4-cycle that confuses topological layering.
func isReturnNet(net sexp.Net) bool {
	name := strings.ToUpper(strings.TrimSpace(net.Name))
	switch name {
	case "GND", "GND1", "GND2", "GNDA", "GNDD", "GNDPWR", "GNDREF", "GNDS", "EARTH", "0V":
		return true
	}
	return false
}

// flowBarycenter is the average layer of incoming neighbours; used to keep
// related components vertically near each other within their layer.
func flowBarycenter(ref string, inEdges map[string]map[string]bool, layer map[string]int) int {
	if len(inEdges[ref]) == 0 {
		return layer[ref] * 1000 // isolated → keep in original order
	}
	sum, n := 0, 0
	for src := range inEdges[ref] {
		sum += layer[src]
		n++
	}
	return sum * 1000 / n
}
