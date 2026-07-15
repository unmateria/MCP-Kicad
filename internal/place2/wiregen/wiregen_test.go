package wiregen

import (
	"fmt"
	"strings"
	"testing"

	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/sexp"
)

// --- fixtures -----------------------------------------------------------

// deviceRLib is a two-pin Device:R (pins "1" at local (-2.54,0), "2" at
// (2.54,0)), same shape as gate_test.go / metrics_test.go.
const deviceRLib = `
	(symbol "Device:R"
		(symbol "R_1_1"
			(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
		)
	)`

// deviceCLib is a two-pin Device:C with pins "1" (local (0,2.54)) and "2"
// (local (0,-2.54)) — vertical, so pin 1 sits above the body center.
const deviceCLib = `
	(symbol "Device:C"
		(symbol "C_1_1"
			(pin passive line (at 0 2.54 270) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			(pin passive line (at 0 -2.54 90) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
		)
	)`

// testICLib is a four-pin IC whose pins span both axes wide enough that its
// BodyBBox (pin span inset by 2.54 mm) is a genuine rectangle — needed to test
// corridor blocking (two-pin passives collapse to a degenerate line).
// Pins: VP (-10.16,5.08), GNDP (-10.16,-5.08), A (10.16,5.08), B (10.16,-5.08).
const testICLib = `
	(symbol "Test:IC"
		(symbol "IC_1_1"
			(pin power_in line (at -10.16 5.08 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "VP" (effects (font (size 1.27 1.27)))))
			(pin power_in line (at -10.16 -5.08 0) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "GNDP" (effects (font (size 1.27 1.27)))))
			(pin bidirectional line (at 10.16 5.08 180) (length 2.54) (number "3" (effects (font (size 1.27 1.27)))) (name "A" (effects (font (size 1.27 1.27)))))
			(pin bidirectional line (at 10.16 -5.08 180) (length 2.54) (number "4" (effects (font (size 1.27 1.27)))) (name "B" (effects (font (size 1.27 1.27)))))
		)
	)`

func f(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return s
}

func placedSymbol(libID, ref string, cx, cy, rot float64) string {
	return `
	(symbol (lib_id "` + libID + `") (at ` + f(cx) + ` ` + f(cy) + ` ` + f(rot) + `) (unit 1) (in_bom yes) (on_board yes) (uuid "` + ref + `-uuid")
		(property "Reference" "` + ref + `" (at ` + f(cx) + ` ` + f(cy-6) + ` 0) (effects (font (size 1.27 1.27))))
		(property "Value" "x" (at ` + f(cx) + ` ` + f(cy+6) + ` 0) (effects (font (size 1.27 1.27))))
	)`
}

func wrap(libDefs, body string) string {
	return `(kicad_sch (version 20231120) (generator "test") (uuid "00000000-0000-4000-8000-000000000000")
		(lib_symbols` + libDefs + `)` + body + `
	)`
}

func mustParse(t *testing.T, content string) *sexp.Schematic {
	t.Helper()
	sch, err := sexp.ParseSchematic(content)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, content)
	}
	return sch
}

// pinAt resolves a pin ref to a NetInput pin with coordinates + direction.
func pinAt(t *testing.T, sch *sexp.Schematic, ref, net string) PinInput {
	t.Helper()
	owner := ref[:strings.Index(ref, ".")]
	info, ok := sexp.FindPin(sch, ref)
	if !ok {
		t.Fatalf("pin %s not found", ref)
	}
	var libID string
	for _, s := range sexp.ReadSymbols(sch) {
		if s.Reference == owner {
			libID = s.LibID
		}
	}
	return PinInput{Ref: ref, Owner: owner, Net: net, X: info.X, Y: info.Y, Dir: info.Direction, LibID: libID}
}

func netConnected(sch *sexp.Schematic, a, b string) bool {
	for _, n := range sexp.TraceNets(sch) {
		refs := map[string]bool{}
		for _, p := range n.Pins {
			refs[p.String()] = true
		}
		if refs[a] && refs[b] && !n.Dangling {
			return true
		}
	}
	return false
}

// --- twoPin: series/pullup straight run ---------------------------------

func TestTwoPinStraightRun(t *testing.T) {
	// R1 and R2 collinear at y=50.8; SIG net joins R1.2 (15.24,50.8) and
	// R2.1 (25.4,50.8) — a clean horizontal segment. Centers are 1.27 grid
	// multiples so pins land on the connection grid and NewWire's snap is a
	// no-op (otherwise wire endpoints drift off the pins).
	body := placedSymbol("Device:R", "R1", 12.7, 50.8, 0) +
		placedSymbol("Device:R", "R2", 27.94, 50.8, 0)
	sch := mustParse(t, wrap(deviceRLib, body))

	nets := []NetInput{{Name: "SIG", Pins: []PinInput{
		pinAt(t, sch, "R1.2", "SIG"), pinAt(t, sch, "R2.1", "SIG"),
	}}}
	clusters := []cluster.Cluster{{Kind: "series_led", Anchor: "R1", Refs: []string{"R1", "R2"}}}

	res := Apply(sch, clusters, nets)
	if len(res.Wires) != 1 {
		t.Fatalf("expected 1 wire, got %d", len(res.Wires))
	}
	if len(res.Pairs) != 1 || res.Pairs[0].Net != "SIG" {
		t.Fatalf("expected 1 pair on SIG, got %+v", res.Pairs)
	}
	if v := gate.Check(sch); len(v) != 0 {
		t.Fatalf("gate found violations on clean formula wire: %+v", v)
	}
	if !netConnected(sch, "R1.2", "R2.1") {
		t.Fatalf("R1.2 and R2.1 not electrically connected after wiring")
	}
}

// --- twoPin: single-L when not collinear --------------------------------

func TestTwoPinSingleL(t *testing.T) {
	// R1 at y=50.8, R2 offset up at y=40.64 — close enough (<= maxSpan) that a
	// direct single-L is drawn rather than a reposition.
	body := placedSymbol("Device:R", "R1", 12.7, 50.8, 0) +
		placedSymbol("Device:R", "R2", 25.4, 40.64, 0)
	sch := mustParse(t, wrap(deviceRLib, body))
	nets := []NetInput{{Name: "SIG", Pins: []PinInput{
		pinAt(t, sch, "R1.2", "SIG"), pinAt(t, sch, "R2.1", "SIG"),
	}}}
	clusters := []cluster.Cluster{{Kind: "pullup", Anchor: "R1", Refs: []string{"R1", "R2"}}}

	res := Apply(sch, clusters, nets)
	if len(res.Wires) == 0 {
		t.Fatalf("expected an L (2 segments) or a straight run after align, got 0 wires")
	}
	if v := gate.Check(sch); len(v) != 0 {
		t.Fatalf("gate found violations: %+v", v)
	}
	if !netConnected(sch, "R1.2", "R2.1") {
		t.Fatalf("pins not connected")
	}
}

// --- blocked corridor declines ------------------------------------------

func TestBlockedCorridorDeclines(t *testing.T) {
	// R1.2 (15.24,50.8) would reach R2.1 (38.1,50.8) by a straight run, but a
	// Test:IC body sits astride y=50.8 between them (and astride the canonical
	// snug spots just east of R1.2), so neither the direct run nor a reposition
	// is clean — the generator must decline.
	body := placedSymbol("Device:R", "R1", 12.7, 50.8, 0) +
		placedSymbol("Device:R", "R2", 40.64, 50.8, 0) +
		placedSymbol("Test:IC", "U1", 25.4, 50.8, 0)
	sch := mustParse(t, wrap(deviceRLib+testICLib, body))
	nets := []NetInput{{Name: "SIG", Pins: []PinInput{
		pinAt(t, sch, "R1.2", "SIG"), pinAt(t, sch, "R2.1", "SIG"),
	}}}
	clusters := []cluster.Cluster{{Kind: "series_led", Anchor: "R1", Refs: []string{"R1", "R2"}}}

	res := Apply(sch, clusters, nets)
	if len(res.Wires) != 0 {
		t.Fatalf("expected the generator to decline (0 wires), got %d", len(res.Wires))
	}
	if len(res.Moves) != 0 {
		t.Fatalf("expected no reposition of a locked satellite, got %+v", res.Moves)
	}
}

// --- repositioning: satellite snapped onto the anchor row ---------------

func TestRepositionAlign(t *testing.T) {
	// R1.2 (15.24,50.8) faces east. R2 sits far east (R2.1 at 58.42,50.8) and a
	// Test:IC wall at x~35 blocks the straight run between them. Since R1.2's
	// outgoing side (just east of it) is clear, the generator snugs R2 next to
	// R1.2 and draws a short straight stub — the canonical reposition.
	body := placedSymbol("Device:R", "R1", 12.7, 50.8, 0) +
		placedSymbol("Device:R", "R2", 60.96, 50.8, 0) +
		placedSymbol("Test:IC", "U1", 35.56, 50.8, 0)
	sch := mustParse(t, wrap(deviceRLib+testICLib, body))
	nets := []NetInput{{Name: "SIG", Pins: []PinInput{
		pinAt(t, sch, "R1.2", "SIG"), pinAt(t, sch, "R2.1", "SIG"),
	}}}
	clusters := []cluster.Cluster{{Kind: "pullup", Anchor: "R1", Refs: []string{"R1", "R2"}}}

	res := Apply(sch, clusters, nets)
	if len(res.Moves) != 1 || res.Moves[0].Ref != "R2" {
		t.Fatalf("expected R2 repositioned once, got %+v", res.Moves)
	}
	if v := gate.Check(sch); len(v) != 0 {
		t.Fatalf("gate violations after reposition: %+v", v)
	}
	if !netConnected(sch, "R1.2", "R2.1") {
		t.Fatalf("pins not connected after reposition")
	}
	// R2 must have moved west, next to R1.
	p, _ := sexp.FindPin(sch, "R2.1")
	if p.X > 30 {
		t.Fatalf("expected R2.1 snugged west near R1, got x=%.2f", p.X)
	}
}

// --- repositioning destination clash declines ---------------------------

func TestRepositionClashDeclines(t *testing.T) {
	// Same as TestRepositionAlign but a second Test:IC (U2) occupies the
	// canonical snug spots just east of R1.2, so every reposition destination
	// clashes and the direct run is walled by U1 — the generator declines.
	body := placedSymbol("Device:R", "R1", 12.7, 50.8, 0) +
		placedSymbol("Device:R", "R2", 60.96, 50.8, 0) +
		placedSymbol("Test:IC", "U1", 35.56, 50.8, 0) +
		placedSymbol("Test:IC", "U2", 22.86, 50.8, 0)
	sch := mustParse(t, wrap(deviceRLib+testICLib, body))
	nets := []NetInput{{Name: "SIG", Pins: []PinInput{
		pinAt(t, sch, "R1.2", "SIG"), pinAt(t, sch, "R2.1", "SIG"),
	}}}
	clusters := []cluster.Cluster{{Kind: "pullup", Anchor: "R1", Refs: []string{"R1", "R2"}}}

	res := Apply(sch, clusters, nets)
	if len(res.Wires) != 0 {
		t.Fatalf("expected decline (0 wires) when both L and reposition are blocked, got %d wires; moves=%+v", len(res.Wires), res.Moves)
	}
}

// --- decoupling: cap wired to IC power pin ------------------------------

func TestDecouplingWiresCap(t *testing.T) {
	// Test:IC VP pin at (40.64,45.72). C1 placed so C1.1 sits directly below VP
	// at (40.64,40.64) → clean vertical stub. Cap center (40.64,43.18) is within
	// 15 mm of the IC center.
	body := placedSymbol("Test:IC", "U1", 50.8, 50.8, 0) +
		placedSymbol("Device:C", "C1", 40.64, 43.18, 0)
	sch := mustParse(t, wrap(testICLib+deviceCLib, body))

	nets := []NetInput{{Name: "VP", Pins: []PinInput{
		pinAt(t, sch, "U1.VP", "VP"), pinAt(t, sch, "C1.1", "VP"),
	}}}
	clusters := []cluster.Cluster{{Kind: "decoupling", Anchor: "U1", Refs: []string{"U1", "C1"}}}

	res := Apply(sch, clusters, nets)
	if len(res.Wires) == 0 {
		t.Fatalf("expected decoupling wire, got 0")
	}
	if v := gate.Check(sch); len(v) != 0 {
		t.Fatalf("gate violations on decoupling wire: %+v", v)
	}
	if !netConnected(sch, "U1.VP", "C1.1") {
		t.Fatalf("cap not connected to IC power pin")
	}
}

// --- decoupling declines a far cap --------------------------------------

func TestDecouplingFarCapDeclines(t *testing.T) {
	// C1 sits >15 mm from the IC anchor center — not a bypass cap for formula
	// wiring; the generator declines and leaves it to the router.
	body := placedSymbol("Test:IC", "U1", 50.8, 50.8, 0) +
		placedSymbol("Device:C", "C1", 40.64, 90.17, 0)
	sch := mustParse(t, wrap(testICLib+deviceCLib, body))
	nets := []NetInput{{Name: "VP", Pins: []PinInput{
		pinAt(t, sch, "U1.VP", "VP"), pinAt(t, sch, "C1.1", "VP"),
	}}}
	clusters := []cluster.Cluster{{Kind: "decoupling", Anchor: "U1", Refs: []string{"U1", "C1"}}}

	res := Apply(sch, clusters, nets)
	if len(res.Wires) != 0 {
		t.Fatalf("expected decline for far cap, got %d wires", len(res.Wires))
	}
}
