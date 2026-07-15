package wiregen

import "mcp-kicad/internal/sexp"

// crystalGen wires a crystal's two load capacitors to the crystal body. Each
// load cap shares exactly one XTAL net with the crystal (XTAL1 with pin 1,
// XTAL2 with pin 2), so the connection is the crystal pin -> cap pin straight
// stub (or a single L), with the cap nudged onto the crystal pin's axis when
// blocked. The crystal-to-MCU leg of each XTAL net is left to the router: the
// cap+crystal pair is already joined, so the router only adds the final hop.
//
// The generator declines a cap whose stub cannot be drawn cleanly, matching the
// design brief's "only when the geometry matches, otherwise decline".
type crystalGen struct{}

func (crystalGen) Handles(kind string) bool { return kind == "crystal" }

func (crystalGen) TryWire(gc *genCtx) (wires, juncs []*sexp.Node, pairs []Pair, ok bool) {
	return wireSharedPairs(gc)
}
