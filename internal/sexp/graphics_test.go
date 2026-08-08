package sexp_test

import (
	"fmt"
	"math"
	"testing"

	"mcp-kicad/internal/sexp"
)

// opampLibDef is an LM2904-shaped dual op-amp: unit 1 draws the classic
// triangle reaching 5.08 mm above and below a pin row that spans only
// 2.54 mm, unit 3 is the pin-only power unit that draws nothing.
const opampLibDef = `
	(symbol "Amplifier_Operational:LM2904"
		(symbol "LM2904_1_1"
			(polyline
				(pts (xy -5.08 5.08) (xy 5.08 0) (xy -5.08 -5.08) (xy -5.08 5.08))
				(stroke (width 0.254) (type default))
				(fill (type background)))
			(pin output line (at 7.62 0 180) (length 2.54) (name "" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
			(pin input line (at -7.62 -2.54 0) (length 2.54) (name "-" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27)))))
			(pin input line (at -7.62 2.54 0) (length 2.54) (name "+" (effects (font (size 1.27 1.27)))) (number "3" (effects (font (size 1.27 1.27))))))
		(symbol "LM2904_3_1"
			(pin power_in line (at -2.54 -7.62 90) (length 3.81) (name "V-" (effects (font (size 1.27 1.27)))) (number "4" (effects (font (size 1.27 1.27)))))
			(pin power_in line (at -2.54 7.62 270) (length 3.81) (name "V+" (effects (font (size 1.27 1.27)))) (number "8" (effects (font (size 1.27 1.27)))))))`

// resistorLibDef is the real Device:R geometry: a 2.032 x 5.08 mm rectangle
// with 1.27 mm pin lines, so the drawn body sits well inside the pin span.
const resistorLibDef = `
	(symbol "Device:R"
		(symbol "R_0_1"
			(rectangle (start -1.016 -2.54) (end 1.016 2.54)
				(stroke (width 0.254) (type default))
				(fill (type none))))
		(symbol "R_1_1"
			(pin passive line (at 0 3.81 270) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
			(pin passive line (at 0 -3.81 90) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27)))))))`

// asymLibDef draws a rectangle that is neither square nor centred on the
// symbol origin, so every rotation maps it to a distinguishable box.
const asymLibDef = `
	(symbol "Test:Asym"
		(symbol "Asym_0_1"
			(rectangle (start 0 0) (end 6 2)
				(stroke (width 0.254) (type default))
				(fill (type none))))
		(symbol "Asym_1_1"
			(pin passive line (at -2.54 0 0) (length 2.54) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))))`

// buildSch assembles a schematic from lib definitions and placed instances.
func buildSch(t *testing.T, libDefs string, instances string) *sexp.Schematic {
	t.Helper()
	src := `(kicad_sch (version 20231120) (generator "test")
		(uuid "00000000-0000-4000-8000-000000000000")
		(lib_symbols` + libDefs + `)
		` + instances + `)`
	sch, err := sexp.ParseSchematic(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return sch
}

func instance(libID, ref string, x, y, rot float64, unit int, uuidSuffix string) string {
	return fmt.Sprintf(`(symbol (lib_id "%s") (at %g %g %g) (unit %d) (in_bom yes) (on_board yes)
		(uuid "00000000-0000-4000-8000-0000000000%s")
		(property "Reference" "%s" (at %g %g 0) (effects (font (size 1.27 1.27))))
		(property "Value" "V" (at %g %g 0) (effects (font (size 1.27 1.27)))))`,
		libID, x, y, rot, unit, uuidSuffix, ref, x, y-3.81, x, y+3.81)
}

func findSym(t *testing.T, sch *sexp.Schematic, ref string) sexp.SchematicSymbol {
	t.Helper()
	for _, s := range sexp.ReadSymbols(sch) {
		if s.Reference == ref {
			return s
		}
	}
	t.Fatalf("symbol %s not found", ref)
	return sexp.SchematicSymbol{}
}

func wantBox(t *testing.T, what string, gx1, gy1, gx2, gy2, x1, y1, x2, y2 float64) {
	t.Helper()
	const eps = 0.001
	if math.Abs(gx1-x1) > eps || math.Abs(gy1-y1) > eps ||
		math.Abs(gx2-x2) > eps || math.Abs(gy2-y2) > eps {
		t.Errorf("%s = (%.3f,%.3f)-(%.3f,%.3f), want (%.3f,%.3f)-(%.3f,%.3f)",
			what, gx1, gy1, gx2, gy2, x1, y1, x2, y2)
	}
}

// TestGraphicBBoxOpampTriangle is the case the pin-only body model cannot see:
// the op-amp triangle reaches 5.08 mm above and below a pin row 2.54 mm tall.
func TestGraphicBBoxOpampTriangle(t *testing.T) {
	sch := buildSch(t, opampLibDef,
		instance("Amplifier_Operational:LM2904", "U1", 100, 100, 0, 1, "01"))
	sym := findSym(t, sch, "U1")

	if !sym.HasGraphic {
		t.Fatal("HasGraphic = false, want true for an embedded op-amp unit")
	}
	wantBox(t, "graphic bbox",
		sym.GraphicX1, sym.GraphicY1, sym.GraphicX2, sym.GraphicY2,
		94.92, 94.92, 105.08, 105.08)

	// The vertical reach must exceed the pin row on both sides.
	minPinY, maxPinY := math.MaxFloat64, -math.MaxFloat64
	for _, p := range sym.Pins {
		minPinY = math.Min(minPinY, p.Y)
		maxPinY = math.Max(maxPinY, p.Y)
	}
	if sym.GraphicY1 >= minPinY || sym.GraphicY2 <= maxPinY {
		t.Errorf("triangle y range [%.2f,%.2f] does not enclose pin row [%.2f,%.2f]",
			sym.GraphicY1, sym.GraphicY2, minPinY, maxPinY)
	}
}

// TestGraphicBBoxPinOnlyUnit covers a unit that carries pins but draws
// nothing — the power unit of a multi-unit IC.
func TestGraphicBBoxPinOnlyUnit(t *testing.T) {
	sch := buildSch(t, opampLibDef,
		instance("Amplifier_Operational:LM2904", "U1", 100, 100, 0, 3, "03"))
	sym := findSym(t, sch, "U1")
	if sym.HasGraphic {
		t.Errorf("HasGraphic = true for pin-only unit 3; box = (%.2f,%.2f)-(%.2f,%.2f)",
			sym.GraphicX1, sym.GraphicY1, sym.GraphicX2, sym.GraphicY2)
	}
}

// TestGraphicBBoxNoEmbeddedDef pins the fallback: a placed symbol whose lib
// definition is absent reports no graphic at all.
func TestGraphicBBoxNoEmbeddedDef(t *testing.T) {
	sch := buildSch(t, "", instance("Amplifier_Operational:LM2904", "U1", 100, 100, 0, 1, "01"))
	sym := findSym(t, sch, "U1")
	if sym.HasGraphic {
		t.Error("HasGraphic = true without an embedded definition")
	}
	if len(sym.Pins) != 0 {
		t.Errorf("Pins = %d without an embedded definition, want 0", len(sym.Pins))
	}
}

// TestGraphicBBoxRotation checks that graphics go through the same
// library→schematic transform as pins: rotation plus the Y flip.
func TestGraphicBBoxRotation(t *testing.T) {
	cases := []struct {
		rot            float64
		x1, y1, x2, y2 float64
		suffix         string
		pinX, pinY     float64
		description    string
	}{
		// rot 0:   schX = cx+lx, schY = cy-ly
		{0, 100, 98, 106, 100, "10", 97.46, 100, "east"},
		// rot 90:  schX = cx-ly, schY = cy-lx
		{90, 98, 94, 100, 100, "11", 100, 102.54, "north"},
		// rot 180: schX = cx-lx, schY = cy+ly
		{180, 94, 100, 100, 102, "12", 102.54, 100, "west"},
		// rot 270: schX = cx+ly, schY = cy+lx
		{270, 100, 100, 102, 106, "13", 100, 97.46, "south"},
	}
	for _, c := range cases {
		t.Run(c.description, func(t *testing.T) {
			sch := buildSch(t, asymLibDef, instance("Test:Asym", "X1", 100, 100, c.rot, 1, c.suffix))
			sym := findSym(t, sch, "X1")
			if !sym.HasGraphic {
				t.Fatal("HasGraphic = false")
			}
			wantBox(t, fmt.Sprintf("graphic bbox at rot %g", c.rot),
				sym.GraphicX1, sym.GraphicY1, sym.GraphicX2, sym.GraphicY2,
				c.x1, c.y1, c.x2, c.y2)
			// The pin must land where the same transform puts it.
			if len(sym.Pins) != 1 {
				t.Fatalf("Pins = %d, want 1", len(sym.Pins))
			}
			if math.Abs(sym.Pins[0].X-c.pinX) > 0.001 || math.Abs(sym.Pins[0].Y-c.pinY) > 0.001 {
				t.Errorf("pin at (%.2f,%.2f), want (%.2f,%.2f)", sym.Pins[0].X, sym.Pins[0].Y, c.pinX, c.pinY)
			}
		})
	}
}

// TestGraphicBBoxResistor confirms the common case stays boring: a Device:R
// rectangle is fully inside the pin span, so it adds no surprises.
func TestGraphicBBoxResistor(t *testing.T) {
	sch := buildSch(t, resistorLibDef, instance("Device:R", "R1", 100, 100, 0, 1, "20"))
	sym := findSym(t, sch, "R1")
	if !sym.HasGraphic {
		t.Fatal("HasGraphic = false for Device:R")
	}
	wantBox(t, "graphic bbox",
		sym.GraphicX1, sym.GraphicY1, sym.GraphicX2, sym.GraphicY2,
		98.98, 97.46, 101.02, 102.54)
	for _, p := range sym.Pins {
		if p.Y > sym.GraphicY1 && p.Y < sym.GraphicY2 {
			t.Errorf("pin %s at y=%.2f is inside the drawn rectangle", p.Number, p.Y)
		}
	}
}
