package wiregen

import "mcp-kicad/internal/sexp"

// dividerGen wires resistor dividers. A voltage_divider cluster anchors on the
// top resistor and its satellite is the bottom resistor: the two inner pins
// share the TAP net, giving a straight connecting segment when collinear (an L
// otherwise). A feedback_divider anchors on the IC; each divider resistor is
// wired to the IC across the feedback net. Both cases reduce to "wire each
// satellite to the anchor over the signal net they share".
type dividerGen struct{}

func (dividerGen) Handles(kind string) bool {
	return kind == "voltage_divider" || kind == "feedback_divider"
}

func (dividerGen) TryWire(gc *genCtx) (wires, juncs []*sexp.Node, pairs []Pair, ok bool) {
	return wireSharedPairs(gc)
}
