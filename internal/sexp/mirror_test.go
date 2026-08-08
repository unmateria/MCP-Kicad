package sexp_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// mirroredInstance renders a placed symbol carrying KiCad's (mirror axis).
func mirroredInstance(libID, ref string, x, y, rot float64, axis string) string {
	base := instance(libID, ref, x, y, rot, 1, "01")
	if axis == "" {
		return base
	}
	// Insert the mirror node right after (at …), where KiCad writes it.
	marker := fmt.Sprintf("(at %g %g %g)", x, y, rot)
	return strings.Replace(base, marker, marker+" (mirror "+axis+")", 1)
}

func pinAt(t *testing.T, sym sexp.SchematicSymbol, number string) sexp.PinInfo {
	t.Helper()
	for _, p := range sym.Pins {
		if p.Number == number {
			return p
		}
	}
	t.Fatalf("%s has no pin %s", sym.Reference, number)
	return sexp.PinInfo{}
}

// KiCad applies (mirror y) to the FINISHED placement, after the rotation.
// Both rows below were measured with kicad-cli sch export netlist on a real
// Device:R, labelling each pin tip by position so the netlist named whichever
// pin landed there:
//
//	rot 0  — library pins lie on the Y axis, so flipping X changes nothing
//	rot 90 — the pins now lie on the X axis, so they swap ends
//
// Mirroring in the symbol's own frame BEFORE rotating predicts the opposite
// in both rows, and would go unnoticed until someone rotated a mirrored part.
func TestReadSymbolsAppliesMirrorAfterRotation(t *testing.T) {
	const cx, cy = 100.0, 100.0

	cases := []struct {
		name         string
		rot          float64
		axis         string
		pin1, pin2   [2]float64
		wantMirrored string
	}{
		{"rot0 plain", 0, "", [2]float64{cx, cy - 3.81}, [2]float64{cx, cy + 3.81}, ""},
		{"rot0 mirror y", 0, "y", [2]float64{cx, cy - 3.81}, [2]float64{cx, cy + 3.81}, "y"},
		{"rot90 plain", 90, "", [2]float64{cx - 3.81, cy}, [2]float64{cx + 3.81, cy}, ""},
		{"rot90 mirror y", 90, "y", [2]float64{cx + 3.81, cy}, [2]float64{cx - 3.81, cy}, "y"},
		{"rot0 mirror x", 0, "x", [2]float64{cx, cy + 3.81}, [2]float64{cx, cy - 3.81}, "x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sch := buildSch(t, resistorLibDef, mirroredInstance("Device:R", "R1", cx, cy, c.rot, c.axis))
			sym := findSym(t, sch, "R1")

			if sym.Mirror != c.wantMirrored {
				t.Errorf("Mirror = %q, want %q", sym.Mirror, c.wantMirrored)
			}
			for _, want := range []struct {
				number string
				at     [2]float64
			}{{"1", c.pin1}, {"2", c.pin2}} {
				p := pinAt(t, sym, want.number)
				if math.Abs(p.X-want.at[0]) > 0.001 || math.Abs(p.Y-want.at[1]) > 0.001 {
					t.Errorf("pin %s at (%.2f, %.2f), want (%.2f, %.2f)",
						want.number, p.X, p.Y, want.at[0], want.at[1])
				}
			}
		})
	}
}

// The drawn body must follow the pins, or every consumer that reasons about
// clearance — the router's obstacles, the gate's wire-through-symbol check,
// the text placer — would work against a body that is no longer there.
func TestMirrorReflectsGraphicBox(t *testing.T) {
	const cx, cy = 100.0, 100.0

	// An asymmetric body makes the reflection observable; Device:R's own
	// rectangle is centred and would look identical either way.
	plain := findSym(t, buildSch(t, asymLibDef, mirroredInstance("Test:Asym", "U1", cx, cy, 0, "")), "U1")
	flipped := findSym(t, buildSch(t, asymLibDef, mirroredInstance("Test:Asym", "U1", cx, cy, 0, "y")), "U1")

	if !plain.HasGraphic || !flipped.HasGraphic {
		t.Fatal("fixture must draw a body")
	}
	wantX1, wantX2 := 2*cx-plain.GraphicX2, 2*cx-plain.GraphicX1
	if math.Abs(flipped.GraphicX1-wantX1) > 0.001 || math.Abs(flipped.GraphicX2-wantX2) > 0.001 {
		t.Errorf("mirrored body X = [%.2f, %.2f], want [%.2f, %.2f]",
			flipped.GraphicX1, flipped.GraphicX2, wantX1, wantX2)
	}
	if math.Abs(flipped.GraphicY1-plain.GraphicY1) > 0.001 || math.Abs(flipped.GraphicY2-plain.GraphicY2) > 0.001 {
		t.Errorf("(mirror y) must not move the body in Y: [%.2f, %.2f] vs [%.2f, %.2f]",
			flipped.GraphicY1, flipped.GraphicY2, plain.GraphicY1, plain.GraphicY2)
	}
}

// SetSymbolMirror is the writing half: it must produce exactly what
// ReadSymbols reads back, and clear cleanly so it can be applied twice.
func TestSetSymbolMirrorRoundTrips(t *testing.T) {
	const cx, cy = 100.0, 100.0
	sch := buildSch(t, resistorLibDef, mirroredInstance("Device:R", "R1", cx, cy, 90, ""))

	sexp.SetSymbolMirror(sch.Symbols()[0], "y")
	sym := findSym(t, sch, "R1")
	if sym.Mirror != "y" {
		t.Fatalf("Mirror = %q after SetSymbolMirror, want \"y\"", sym.Mirror)
	}
	if p := pinAt(t, sym, "1"); math.Abs(p.X-(cx+3.81)) > 0.001 {
		t.Errorf("pin 1 at x=%.2f, want %.2f", p.X, cx+3.81)
	}

	// Applying it again must not stack a second node or shift anything.
	sexp.SetSymbolMirror(sch.Symbols()[0], "y")
	if got := strings.Count(sch.Serialize(), "(mirror y)"); got != 1 {
		t.Errorf("(mirror y) appears %d times, want 1", got)
	}

	sexp.SetSymbolMirror(sch.Symbols()[0], "")
	sym = findSym(t, sch, "R1")
	if sym.Mirror != "" {
		t.Errorf("Mirror = %q after clearing, want empty", sym.Mirror)
	}
	if p := pinAt(t, sym, "1"); math.Abs(p.X-(cx-3.81)) > 0.001 {
		t.Errorf("pin 1 back at x=%.2f, want %.2f", p.X, cx-3.81)
	}
}
