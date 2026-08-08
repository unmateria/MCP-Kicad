package gate

import (
	"fmt"
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// deviceRLibSymbols is the shared (lib_symbols ...) block for a two-pin
// Device:R resistor, pins at local (-2.54,0)="1" and (2.54,0)="2". Same
// fixture pattern as internal/place2/metrics/metrics_test.go.
const deviceRLibSymbols = `
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_1_1"
				(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			)
		)
	)`

func resistor(ref string, cx, cy, rot float64) string {
	return `
	(symbol (lib_id "Device:R") (at ` + f(cx) + ` ` + f(cy) + ` ` + f(rot) + `) (unit 1) (in_bom yes) (on_board yes) (uuid "` + ref + `-uuid")
		(property "Reference" "` + ref + `" (at ` + f(cx) + ` ` + f(cy-3.81) + ` 0) (effects (font (size 1.27 1.27))))
		(property "Value" "10k" (at ` + f(cx) + ` ` + f(cy+3.81) + ` 0) (effects (font (size 1.27 1.27))))
	)`
}

func f(v float64) string {
	// minimal float formatter avoiding scientific notation for our small fixture values
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return s
}

func wrapSch(body string) string {
	return `(kicad_sch
	(version 20231120)
	(generator "test")
	(uuid "00000000-0000-4000-8000-000000000000")` + body + `
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

// --- Test 1: different-net crossing ---

func TestEnforceDemotesCrossingNet(t *testing.T) {
	// R1(0,50.8) -- wireA --- R2(40,50.8): horizontal net at y=50.8, x in [2.54,37.46]
	// R3 -- wireB --- R4 (rot90): vertical net at x=20.32, y in [40.64,60.96]
	// wireA and wireB cross at (20.32, 50.8), strictly interior to both — different nets.
	// All symbol coordinates are exact multiples of 1.27mm (the KiCad
	// connection grid, see sexp.snapGrid) so that gate.demoteNet's
	// NewNetLabel placement — which snaps to that same grid — lands exactly
	// back on the real pin position, whichever of the two nets gets demoted.
	body := deviceRLibSymbols +
		resistor("R1", 0, 50.8, 0) +
		resistor("R2", 40.64, 50.8, 0) +
		resistor("R3", 20.32, 38.1, 90) +
		resistor("R4", 20.32, 63.5, 90) +
		`
	(wire (pts (xy 2.54 50.8) (xy 38.1 50.8)) (stroke (width 0) (type default)) (uuid "wireA"))
	(wire (pts (xy 20.32 40.64) (xy 20.32 60.96)) (stroke (width 0) (type default)) (uuid "wireB"))`
	sch := mustParse(t, wrapSch(body))

	violations := Check(sch)
	foundCross := false
	for _, v := range violations {
		if v.Kind == CrossNetCrossing {
			foundCross = true
		}
	}
	if !foundCross {
		t.Fatalf("expected a CROSS_NET_CROSSING violation, got %+v", violations)
	}

	result := Enforce(sch)
	if len(result.Demoted) != 1 {
		t.Fatalf("expected exactly 1 net demoted, got %d: %+v", len(result.Demoted), result.Demoted)
	}
	if result.Violations != 0 {
		t.Fatalf("expected 0 violations after Enforce, got %d", result.Violations)
	}
	if remaining := Check(sch); len(remaining) != 0 {
		t.Fatalf("Check after Enforce should be empty, got %+v", remaining)
	}

	// Both original nets must still be fully connected (2 pins each, not
	// dangling) — regardless of which one Enforce chose to demote. R4 is
	// rotated 90°, which (per sexp's pin-position transform) maps its local
	// pin "1" to the +y side and pin "2" to the -y side, so the wire drawn
	// here actually lands on R4's pin "2".
	nets := sexp.TraceNets(sch)
	found := map[string]bool{"R1.2-R2.1": false, "R3.1-R4.2": false}
	for _, n := range nets {
		refs := map[string]bool{}
		for _, p := range n.Pins {
			refs[p.String()] = true
		}
		if refs["R1.2"] && refs["R2.1"] && len(n.Pins) == 2 && !n.Dangling {
			found["R1.2-R2.1"] = true
		}
		if refs["R3.1"] && refs["R4.2"] && len(n.Pins) == 2 && !n.Dangling {
			found["R3.1-R4.2"] = true
		}
	}
	for name, ok := range found {
		if !ok {
			t.Errorf("net %s not found intact after Enforce; nets=%+v", name, nets)
		}
	}
}

// --- Test 2: wire through symbol body, and pin-tip false-positive guard ---

func TestWireThruSymbolDemotedNoFalsePositive(t *testing.T) {
	// R1(50.8,50.8): body box collapses to a zero-width line at x=50.8,
	// y in [48.26,53.34] (see metrics.BodyBBox). Its own pins get two short
	// stub wires that terminate exactly at the pin tips (must NOT be
	// flagged). R2 sits far away (x=202.54) so its own body never overlaps
	// R1's stubs; the wire from R2's pin back to the origin cuts straight
	// through R1's body at y=49 (must be flagged and demoted) while itself
	// stopping exactly at R2's pin tip (no false positive on R2's body).
	body := deviceRLibSymbols +
		resistor("R1", 50.8, 50.8, 0) +
		resistor("R2", 202.54, 49, 0) +
		`
	(wire (pts (xy 20 50.8) (xy 48.26 50.8)) (stroke (width 0) (type default)) (uuid "stub1"))
	(wire (pts (xy 53.34 50.8) (xy 80 50.8)) (stroke (width 0) (type default)) (uuid "stub2"))
	(wire (pts (xy 0 49) (xy 200 49)) (stroke (width 0) (type default)) (uuid "offend"))`
	sch := mustParse(t, wrapSch(body))

	violations := Check(sch)
	var thru []Violation
	for _, v := range violations {
		if v.Kind == WireThruSymbol {
			thru = append(thru, v)
		}
	}
	if len(thru) == 0 {
		t.Fatalf("expected a WIRE_THRU_SYMBOL violation, got %+v", violations)
	}
	for _, v := range thru {
		if v.Net != "Net-(R2.1)" {
			t.Errorf("expected WIRE_THRU_SYMBOL to implicate R2's net, got %q", v.Net)
		}
	}

	result := Enforce(sch)
	if len(result.Demoted) != 1 {
		t.Fatalf("expected exactly 1 net demoted, got %d: %+v", len(result.Demoted), result.Demoted)
	}
	if result.Violations != 0 {
		t.Fatalf("expected 0 violations after Enforce, got %d", result.Violations)
	}

	// The two pin-tip stubs must survive as real wires — no false-positive demotion.
	remainingWires := len(sch.Wires())
	if remainingWires != 2 {
		t.Errorf("expected 2 wires to survive (the pin-tip stubs), got %d", remainingWires)
	}
}

// --- Test 3: same-net duplicate/overlapping collinear wires are merged, not demoted ---

func TestCleanMergesSameNetDuplicates(t *testing.T) {
	body := deviceRLibSymbols +
		resistor("R1", 0, 50.8, 0) +
		resistor("R2", 40, 50.8, 0) +
		`
	(wire (pts (xy 2.54 50.8) (xy 37.46 50.8)) (stroke (width 0) (type default)) (uuid "full"))
	(wire (pts (xy 2.54 50.8) (xy 20 50.8)) (stroke (width 0) (type default)) (uuid "partialDup"))`
	sch := mustParse(t, wrapSch(body))

	if got := len(sch.Wires()); got != 2 {
		t.Fatalf("expected 2 wires before Clean, got %d", got)
	}
	Clean(sch)
	if got := len(sch.Wires()); got != 1 {
		t.Errorf("expected 1 merged wire after Clean, got %d", got)
	}

	result := Enforce(sch)
	if len(result.Demoted) != 0 {
		t.Errorf("expected 0 demotions for a same-net duplicate overlap, got %d: %+v", len(result.Demoted), result.Demoted)
	}
}

// --- Test 3b: merging must not swallow a pin sitting inside the overlap ---
//
// Regression test for a real bug found while validating this package against
// demo project schematics: two same-net overlapping wires sharing a start
// point (a common daisy-chain pattern — e.g. one #PWR-style stub and one
// longer stub from the same source pin) must NOT be blindly merged into one
// long wire when the SHORTER wire's own far endpoint sits on a real
// component pin that lies strictly inside the longer wire's span. Naively
// merging would drop that pin's coordinate as a registered wire endpoint,
// silently disconnecting it (sexp.TraceNets only unions a wire's own two
// declared endpoints, so a pin that stops being anyone's endpoint becomes an
// isolated singleton net).
func TestCleanDoesNotOrphanPinInsideOverlap(t *testing.T) {
	body := deviceRLibSymbols +
		resistor("Ra", 0, 50.8, 0) +
		resistor("Rb", 40.64, 50.8, 0) +
		resistor("Rc", 81.28, 50.8, 0) +
		`
	(wire (pts (xy 2.54 50.8) (xy 38.1 50.8)) (stroke (width 0) (type default)) (uuid "short"))
	(wire (pts (xy 2.54 50.8) (xy 78.74 50.8)) (stroke (width 0) (type default)) (uuid "long"))`
	sch := mustParse(t, wrapSch(body))

	before := sexp.TraceNets(sch)
	var beforeNet sexp.Net
	for _, n := range before {
		if len(n.Pins) == 3 {
			beforeNet = n
		}
	}
	if len(beforeNet.Pins) != 3 {
		t.Fatalf("expected one 3-pin net before Clean; nets=%+v", before)
	}

	Clean(sch)

	after := sexp.TraceNets(sch)
	found := false
	for _, n := range after {
		refs := map[string]bool{}
		for _, p := range n.Pins {
			refs[p.String()] = true
		}
		if refs["Ra.2"] && refs["Rb.1"] && refs["Rc.1"] && len(n.Pins) == 3 && !n.Dangling {
			found = true
		}
	}
	if !found {
		t.Fatalf("Rb.1 got orphaned by Clean's merge; nets after=%+v", after)
	}
}

// --- Test 4: clean two-net L-shaped schematic — Enforce must be a no-op ---

func TestEnforceNoopOnCleanSchematic(t *testing.T) {
	body := deviceRLibSymbols +
		resistor("R1", 0, 0, 0) +
		resistor("R2", 40, 20, 0) +
		resistor("R3", 200, 0, 0) +
		resistor("R4", 240, 20, 0) +
		`
	(wire (pts (xy 2.54 0) (xy 37.46 0)) (stroke (width 0) (type default)) (uuid "a1"))
	(wire (pts (xy 37.46 0) (xy 37.46 20)) (stroke (width 0) (type default)) (uuid "a2"))
	(wire (pts (xy 202.54 0) (xy 237.46 0)) (stroke (width 0) (type default)) (uuid "b1"))
	(wire (pts (xy 237.46 0) (xy 237.46 20)) (stroke (width 0) (type default)) (uuid "b2"))`
	sch := mustParse(t, wrapSch(body))

	if got := len(sch.Wires()); got != 4 {
		t.Fatalf("expected 4 wires, got %d", got)
	}
	if v := Check(sch); len(v) != 0 {
		t.Fatalf("expected 0 violations on a clean L-shaped schematic, got %+v", v)
	}

	result := Enforce(sch)
	if len(result.Demoted) != 0 {
		t.Fatalf("expected Enforce to be a no-op, got %d demotions: %+v", len(result.Demoted), result.Demoted)
	}
	if got := len(sch.Wires()); got != 4 {
		t.Errorf("Enforce must not remove wires from a clean schematic, wire count changed to %d", got)
	}
}

// --- Test 5: wire running over a foreign pin tip ---

// Three vertical resistors side by side with their pin-2 tips on one row at
// y=48.26 (rot 90 puts pin 2 on top), and a wire spanning that row from R1.2
// to R3.2 — so it runs exactly over R2.2 without stopping there.
//
// KiCad 10 connects nothing in that geometry (measured with kicad-cli sch
// export netlist: the crossed pin stays on its own net), which is exactly why
// it has to be caught here. The drawing tells the reader R2 is connected to
// the row when it is not, and one junction dot away it becomes a real short.
func pinRowBody(extra string) string {
	return deviceRLibSymbols +
		resistor("R1", 0, 50.8, 90) +
		resistor("R2", 15.24, 50.8, 90) +
		resistor("R3", 30.48, 50.8, 90) +
		extra
}

func TestCheckFlagsWireOverForeignPin(t *testing.T) {
	sch := mustParse(t, wrapSch(pinRowBody(`
	(wire (pts (xy 0 48.26) (xy 30.48 48.26)) (stroke (width 0) (type default)) (uuid "w1"))`)))

	var over []Violation
	for _, v := range Check(sch) {
		if v.Kind == WireOverPin {
			over = append(over, v)
		}
	}
	if len(over) != 1 {
		t.Fatalf("expected exactly 1 WIRE_OVER_PIN (R2.2), got %d: %+v", len(over), Check(sch))
	}
	if !strings.Contains(over[0].Detail, "R2.2") {
		t.Errorf("violation should name the pin it runs over, got %q", over[0].Detail)
	}
}

// Enforce must clear it by demoting the WIRE's net. Demoting the pin's net
// would leave the offending wire in place and the violation standing.
func TestEnforceClearsWireOverForeignPin(t *testing.T) {
	sch := mustParse(t, wrapSch(pinRowBody(`
	(wire (pts (xy 0 48.26) (xy 30.48 48.26)) (stroke (width 0) (type default)) (uuid "w1"))`)))

	result := Enforce(sch)
	if len(result.Demoted) == 0 {
		t.Fatal("expected the wire's net to be demoted")
	}
	if result.Violations != 0 {
		t.Errorf("Enforce left %d violation(s) standing", result.Violations)
	}
	for _, v := range Check(sch) {
		if v.Kind == WireOverPin {
			t.Errorf("WIRE_OVER_PIN survived Enforce: %s", v.Detail)
		}
	}
}

// A wire ENDING on a pin is how every connection is drawn — never a
// violation, or the gate would demote every net in every schematic.
func TestCheckIgnoresWireEndingOnPin(t *testing.T) {
	sch := mustParse(t, wrapSch(pinRowBody(`
	(wire (pts (xy 0 48.26) (xy 15.24 48.26)) (stroke (width 0) (type default)) (uuid "w1"))`)))

	for _, v := range Check(sch) {
		if v.Kind == WireOverPin {
			t.Errorf("a wire ending on R2.2 is a normal connection, got: %s", v.Detail)
		}
	}
}

// With a junction on the pin, KiCad DOES connect — so the drawing no longer
// lies and this is not a geometric defect. Whether that connection belongs
// in the netlist is answered by tools.VerifyNetlist against the source.
func TestCheckIgnoresWireOverPinWithJunction(t *testing.T) {
	sch := mustParse(t, wrapSch(pinRowBody(`
	(wire (pts (xy 0 48.26) (xy 30.48 48.26)) (stroke (width 0) (type default)) (uuid "w1"))
	(junction (at 15.24 48.26) (diameter 0) (color 0 0 0 0) (uuid "j1"))`)))

	for _, v := range Check(sch) {
		if v.Kind == WireOverPin {
			t.Errorf("a junction makes it a real connection, not a lie: %s", v.Detail)
		}
	}
}
