// Package layout implements automatic symbol placement algorithms for KiCad schematics.
package layout

import (
	"math"
	"sort"

	"mcp-kicad/internal/sexp"
)

// Place computes a Sugiyama-style left-to-right layered position for each symbol.
//
// Algorithm:
//  1. Build undirected adjacency graph from shared nets (power symbols excluded).
//  2. BFS layer assignment starting from the symbol with fewest connections.
//  3. Barycenter-heuristic ordering within each layer to minimise edge crossings.
//  4. Assign snapped (x, y) coordinates: 50 mm between layers, 30 mm within layer.
//
// The symbols slice should contain only non-power component instances.
// Multi-unit ICs appear once per unit; all units of the same reference are grouped
// together in the same layer column.
// Returns a map of reference → (x, y) in mm, snapped to the 1.27 mm KiCad grid.
func Place(symbols []sexp.SchematicSymbol, nets []sexp.Net) map[string][2]float64 {
	if len(symbols) == 0 {
		return nil
	}

	// De-duplicate references (multi-unit ICs appear once per unit in ReadSymbols).
	refs := deduplicateRefs(symbols)
	refSet := make(map[string]bool, len(refs))
	for _, r := range refs {
		refSet[r] = true
	}

	// Build undirected adjacency list: edge A–B when A and B share a net.
	adj := buildAdjacency(refs, refSet, nets)

	// BFS layer assignment.
	layer := bfsLayers(refs, adj)

	// Barycenter ordering within each layer.
	byLayer := groupByLayer(refs, layer)
	for l := range byLayer {
		sortByBarycenter(byLayer[l], layer, adj)
	}

	// Handle multi-unit ICs: units of the same reference share the same layer slot.
	// The positions below are per-reference; the caller places all units near that
	// anchor point (the server already handles multi-unit placement in add_symbol).
	return assignCoordinates(byLayer)
}

// deduplicateRefs returns unique reference designators preserving first-seen order.
func deduplicateRefs(symbols []sexp.SchematicSymbol) []string {
	seen := make(map[string]bool, len(symbols))
	var refs []string
	for _, sym := range symbols {
		if !seen[sym.Reference] {
			seen[sym.Reference] = true
			refs = append(refs, sym.Reference)
		}
	}
	return refs
}

// buildAdjacency returns an undirected adjacency list over refs.
// Two refs are adjacent when they share at least one net pin each.
func buildAdjacency(refs []string, refSet map[string]bool, nets []sexp.Net) map[string]map[string]bool {
	adj := make(map[string]map[string]bool, len(refs))
	for _, ref := range refs {
		adj[ref] = make(map[string]bool)
	}
	for _, net := range nets {
		// Collect distinct refs in this net that are in our symbol set.
		var present []string
		seen := make(map[string]bool)
		for _, pin := range net.Pins {
			if refSet[pin.Reference] && !seen[pin.Reference] {
				present = append(present, pin.Reference)
				seen[pin.Reference] = true
			}
		}
		for i := 0; i < len(present); i++ {
			for j := i + 1; j < len(present); j++ {
				adj[present[i]][present[j]] = true
				adj[present[j]][present[i]] = true
			}
		}
	}
	return adj
}

// bfsLayers assigns a layer index to each ref via BFS from the least-connected node.
// Isolated nodes (no connections) are placed in sequential layers after connected ones.
func bfsLayers(refs []string, adj map[string]map[string]bool) map[string]int {
	layer := make(map[string]int, len(refs))
	for _, ref := range refs {
		layer[ref] = -1
	}

	start := leastConnected(refs, adj)
	layer[start] = 0
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for neighbor := range adj[cur] {
			if layer[neighbor] == -1 {
				layer[neighbor] = layer[cur] + 1
				queue = append(queue, neighbor)
			}
		}
	}

	// BFS only visits the connected component of start. Assign remaining refs
	// (other components or isolated symbols) to layers after the main component.
	nextL := maxInt(layer) + 1
	for _, ref := range refs {
		if layer[ref] == -1 {
			layer[ref] = nextL
			nextL++
		}
	}
	return layer
}

// groupByLayer returns refs grouped by their layer index.
func groupByLayer(refs []string, layer map[string]int) map[int][]string {
	byLayer := make(map[int][]string)
	for _, ref := range refs {
		l := layer[ref]
		byLayer[l] = append(byLayer[l], ref)
	}
	return byLayer
}

// sortByBarycenter orders the refs in a single layer using the barycenter heuristic:
// each ref is scored by the average layer index of its neighbours.
func sortByBarycenter(group []string, layer map[string]int, adj map[string]map[string]bool) {
	sort.SliceStable(group, func(i, j int) bool {
		bi := barycenter(group[i], layer, adj)
		bj := barycenter(group[j], layer, adj)
		if math.Abs(bi-bj) > 1e-9 {
			return bi < bj
		}
		return group[i] < group[j] // tie-break: lexicographic
	})
}

// assignCoordinates maps each ref to a snapped (x, y) position.
// Layout constants (in mm):
//
//	originX / originY — top-left anchor
//	spacingX — horizontal gap between layers
//	spacingY — vertical gap between symbols within a layer
const (
	originX  = 50.8
	originY  = 50.8
	spacingX = 50.0
	spacingY = 30.0
)

func assignCoordinates(byLayer map[int][]string) map[string][2]float64 {
	result := make(map[string][2]float64)
	maxL := maxIntInMap(byLayer)
	for l := 0; l <= maxL; l++ {
		for i, ref := range byLayer[l] {
			x := sexp.SnapGrid(originX + float64(l)*spacingX)
			y := sexp.SnapGrid(originY + float64(i)*spacingY)
			result[ref] = [2]float64{x, y}
		}
	}
	return result
}

// --- helpers ---

func leastConnected(refs []string, adj map[string]map[string]bool) string {
	best := refs[0]
	min := len(adj[refs[0]])
	for _, ref := range refs[1:] {
		if n := len(adj[ref]); n < min {
			min = n
			best = ref
		}
	}
	return best
}

func barycenter(ref string, layer map[string]int, adj map[string]map[string]bool) float64 {
	neighbors := adj[ref]
	if len(neighbors) == 0 {
		return float64(layer[ref])
	}
	sum := 0.0
	for n := range neighbors {
		sum += float64(layer[n])
	}
	return sum / float64(len(neighbors))
}

func maxInt(m map[string]int) int {
	max := 0
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

func maxIntInMap(m map[int][]string) int {
	max := 0
	for k := range m {
		if k > max {
			max = k
		}
	}
	return max
}
