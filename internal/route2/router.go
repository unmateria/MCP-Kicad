// Package route2 is the next-generation routing layer that will replace
// internal/router. It exposes a single Router interface with two
// implementations:
//
//   - libavoid (build tag `cgo libavoid`) — orthogonal visibility-graph
//     router with hyperedge support; the production target.
//   - astarpp (always available) — improved A* with angular heuristic,
//     cross-prevention, and bus alignment; the fallback.
//
// During Phase D only the astarpp implementation is wired in. The libavoid
// path is stubbed so the public Router interface can stabilise before the
// vendored C++ source is added.
package route2

import (
	"mcp-kicad/internal/sexp"
)

// Router routes orthogonal wires between pin endpoints.
type Router interface {
	// Route returns waypoints from (x1,y1) to (x2,y2) or nil when blocked.
	// The caller is responsible for snapping to grid (the engine guarantees
	// grid-aligned waypoints).
	Route(x1, y1, x2, y2 float64) [][2]float64

	// MarkWire marks a routed path as a soft obstacle so subsequent
	// routes prefer to avoid it.
	MarkWire(path [][2]float64)
}

// New returns the best available Router implementation. The fallback is
// always the astarpp router; libavoid can be enabled by building with the
// `libavoid` tag once the cgo bindings land.
func New(syms []sexp.SchematicSymbol, existingWires []*sexp.Node) Router {
	return newAstarPP(syms, existingWires)
}
