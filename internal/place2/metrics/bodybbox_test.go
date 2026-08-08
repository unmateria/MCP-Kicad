package metrics

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"mcp-kicad/internal/sexp"
)

// legacyBodyBBox is the pin-only body model as it stood before graphic
// extents were read. Tests compare against it to prove the new model never
// shrinks a body and leaves definition-less symbols untouched.
func legacyBodyBBox(sym sexp.SchematicSymbol) (x1, y1, x2, y2 float64) {
	const pinLen = 2.54
	const defaultHalf = 5.08
	if len(sym.Pins) == 0 {
		return sym.X - defaultHalf, sym.Y - defaultHalf,
			sym.X + defaultHalf, sym.Y + defaultHalf
	}
	x1, y1 = sym.Pins[0].X, sym.Pins[0].Y
	x2, y2 = x1, y1
	for _, p := range sym.Pins[1:] {
		x1, y1 = math.Min(x1, p.X), math.Min(y1, p.Y)
		x2, y2 = math.Max(x2, p.X), math.Max(y2, p.Y)
	}
	x1, y1, x2, y2 = x1+pinLen, y1+pinLen, x2-pinLen, y2-pinLen
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	return
}

const opampSch = `(kicad_sch (version 20231120) (generator "test")
	(uuid "00000000-0000-4000-8000-000000000000")
	(lib_symbols
		(symbol "Amplifier_Operational:LM2904"
			(symbol "LM2904_1_1"
				(polyline
					(pts (xy -5.08 5.08) (xy 5.08 0) (xy -5.08 -5.08) (xy -5.08 5.08))
					(stroke (width 0.254) (type default))
					(fill (type background)))
				(pin output line (at 7.62 0 180) (length 2.54) (name "" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
				(pin input line (at -7.62 -2.54 0) (length 2.54) (name "-" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27)))))
				(pin input line (at -7.62 2.54 0) (length 2.54) (name "+" (effects (font (size 1.27 1.27)))) (number "3" (effects (font (size 1.27 1.27))))))))
	(symbol (lib_id "Amplifier_Operational:LM2904") (at 100 100 0) (unit 1) (in_bom yes) (on_board yes)
		(uuid "00000000-0000-4000-8000-000000000001")
		(property "Reference" "U1" (at 100 96 0) (effects (font (size 1.27 1.27))))
		(property "Value" "LM2904" (at 100 104 0) (effects (font (size 1.27 1.27)))))
	(symbol (lib_id "Missing:Part") (at 200 200 0) (unit 1) (in_bom yes) (on_board yes)
		(uuid "00000000-0000-4000-8000-000000000002")
		(property "Reference" "U2" (at 200 196 0) (effects (font (size 1.27 1.27))))
		(property "Value" "?" (at 200 204 0) (effects (font (size 1.27 1.27))))))`

const resistorSch = `(kicad_sch (version 20231120) (generator "test")
	(uuid "00000000-0000-4000-8000-000000000000")
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_0_1"
				(rectangle (start -1.016 -2.54) (end 1.016 2.54)
					(stroke (width 0.254) (type default))
					(fill (type none))))
			(symbol "R_1_1"
				(pin passive line (at 0 3.81 270) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 0 -3.81 90) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27))))))))
	(symbol (lib_id "Device:R") (at 100 100 0) (unit 1) (in_bom yes) (on_board yes)
		(uuid "00000000-0000-4000-8000-000000000001")
		(property "Reference" "R1" (at 100 96 0) (effects (font (size 1.27 1.27))))
		(property "Value" "10k" (at 100 104 0) (effects (font (size 1.27 1.27))))))`

func symbolByRef(t *testing.T, src, ref string) sexp.SchematicSymbol {
	t.Helper()
	sch, err := sexp.ParseSchematic(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, s := range sexp.ReadSymbols(sch) {
		if s.Reference == ref {
			return s
		}
	}
	t.Fatalf("symbol %s not found", ref)
	return sexp.SchematicSymbol{}
}

// TestBodyBBoxCoversOpampTriangle is the regression this whole model exists
// for: the pin-only box collapses an op-amp unit to a horizontal line, so a
// feedback loop could run straight across the drawn triangle.
func TestBodyBBoxCoversOpampTriangle(t *testing.T) {
	sym := symbolByRef(t, opampSch, "U1")

	lx1, ly1, lx2, ly2 := legacyBodyBBox(sym)
	if ly2-ly1 > 0.001 {
		t.Fatalf("fixture broken: legacy box is not degenerate in y: [%.2f,%.2f]", ly1, ly2)
	}

	x1, y1, x2, y2 := BodyBBox(sym)
	if y1 > 94.92+0.001 || y2 < 105.08-0.001 {
		t.Errorf("body y range [%.2f,%.2f] does not cover the triangle [94.92,105.08]", y1, y2)
	}
	if x1 > lx1 || x2 < lx2 || y1 > ly1 || y2 < ly2 {
		t.Errorf("body (%.2f,%.2f)-(%.2f,%.2f) shrank vs legacy (%.2f,%.2f)-(%.2f,%.2f)",
			x1, y1, x2, y2, lx1, ly1, lx2, ly2)
	}

	// A horizontal wire 3.81 mm above the pin row used to be "legal" and now
	// correctly reads as cutting the triangle.
	if !sexp.SegmentCrossesBox(90, 96.19, 110, 96.19, x1, y1, x2, y2) {
		t.Error("a wire across the triangle is still not detected")
	}
}

// TestBodyBBoxResistorBarelyGrows checks the common case: the union is the
// drawn rectangle, which stays comfortably inside the pin span.
func TestBodyBBoxResistorBarelyGrows(t *testing.T) {
	sym := symbolByRef(t, resistorSch, "R1")
	lx1, ly1, lx2, ly2 := legacyBodyBBox(sym)
	x1, y1, x2, y2 := BodyBBox(sym)

	// x is unchanged: the rectangle is narrower than the legacy inset.
	if math.Abs(x1-lx1) > 0.001 || math.Abs(x2-lx2) > 0.001 {
		t.Errorf("x range changed: legacy [%.2f,%.2f] → new [%.2f,%.2f]", lx1, lx2, x1, x2)
	}
	// y grows by exactly the difference between the assumed 2.54 mm pin length
	// and Device:R's real 1.27 mm one — 1.27 mm per side, no more.
	if math.Abs((ly1-y1)-1.27) > 0.001 || math.Abs((y2-ly2)-1.27) > 0.001 {
		t.Errorf("y grew by (%.3f, %.3f), want (1.27, 1.27)", ly1-y1, y2-ly2)
	}
	for _, p := range sym.Pins {
		if p.Y > y1 && p.Y < y2 {
			t.Errorf("pin %s at y=%.2f ended up inside the body", p.Number, p.Y)
		}
	}
}

// TestBodyBBoxWithoutDefinitionIsUnchanged pins the fallback path: no embedded
// definition means no pins and no graphics, and the box must be identical to
// what the old model produced.
func TestBodyBBoxWithoutDefinitionIsUnchanged(t *testing.T) {
	sym := symbolByRef(t, opampSch, "U2")
	if sym.HasGraphic {
		t.Fatal("HasGraphic = true for a symbol with no embedded definition")
	}
	x1, y1, x2, y2 := BodyBBox(sym)
	lx1, ly1, lx2, ly2 := legacyBodyBBox(sym)
	if x1 != lx1 || y1 != ly1 || x2 != lx2 || y2 != ly2 {
		t.Errorf("body (%v,%v)-(%v,%v), want legacy (%v,%v)-(%v,%v)",
			x1, y1, x2, y2, lx1, ly1, lx2, ly2)
	}
}

func inside(px, py, x1, y1, x2, y2 float64) bool {
	return px > x1 && px < x2 && py > y1 && py < y2
}

// TestBodyBBoxNeverSwallowsAPin is the invariant every consumer relies on: a
// pin tip strictly inside a body makes every wire attached to it look like it
// cuts through the symbol. Reading real graphics must not swallow any pin the
// pin-only model kept outside, and must never shrink a body either. Checked
// across all compiled reference schematics.
func TestBodyBBoxNeverSwallowsAPin(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "projects", "compiled_*", "*.kicad_sch"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no compiled reference schematics on disk")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		sch, err := sexp.ParseSchematic(string(data))
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, sym := range sexp.ReadSymbols(sch) {
			x1, y1, x2, y2 := BodyBBox(sym)
			lx1, ly1, lx2, ly2 := legacyBodyBBox(sym)
			if x1 > lx1 || y1 > ly1 || x2 < lx2 || y2 < ly2 {
				t.Errorf("%s: %s body (%.2f,%.2f)-(%.2f,%.2f) shrank below legacy (%.2f,%.2f)-(%.2f,%.2f)",
					filepath.Base(f), sym.Reference, x1, y1, x2, y2, lx1, ly1, lx2, ly2)
			}
			for _, p := range sym.Pins {
				if inside(p.X, p.Y, x1, y1, x2, y2) && !inside(p.X, p.Y, lx1, ly1, lx2, ly2) {
					t.Errorf("%s: %s pin %s (%.2f,%.2f) newly swallowed by body (%.2f,%.2f)-(%.2f,%.2f)",
						filepath.Base(f), sym.Reference, p.Number, p.X, p.Y, x1, y1, x2, y2)
				}
			}
		}
	}
}
