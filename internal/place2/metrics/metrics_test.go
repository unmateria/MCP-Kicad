package metrics

import (
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// minimal schematic — a single resistor, no wires.
const minSchematic = `(kicad_sch
	(version 20231120)
	(generator "test")
	(uuid "00000000-0000-4000-8000-000000000000")
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_1_1"
				(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			)
		)
	)
	(symbol (lib_id "Device:R") (at 50.8 50.8 0) (unit 1) (in_bom yes) (on_board yes) (uuid "00000000-0000-4000-8000-000000000001")
		(property "Reference" "R1" (at 50.8 47 0) (effects (font (size 1.27 1.27))))
		(property "Value" "10k" (at 50.8 54.6 0) (effects (font (size 1.27 1.27))))
	)
)`

func TestComputeNoWires(t *testing.T) {
	sch, err := sexp.ParseSchematic(minSchematic)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := Compute(sch)
	if m.SymbolCount != 1 {
		t.Errorf("SymbolCount = %d, want 1", m.SymbolCount)
	}
	if m.WireCount != 0 {
		t.Errorf("WireCount = %d, want 0", m.WireCount)
	}
	if m.BendCount != 0 {
		t.Errorf("BendCount = %d, want 0", m.BendCount)
	}
	if m.CrossingCount != 0 {
		t.Errorf("CrossingCount = %d, want 0", m.CrossingCount)
	}
	if m.WireThruSymbol != 0 {
		t.Errorf("WireThruSymbol = %d, want 0", m.WireThruSymbol)
	}
	if m.BboxArea <= 0 {
		t.Errorf("BboxArea = %.2f, want >0", m.BboxArea)
	}
}

// Schematic with a resistor and an L-shaped wire (bend at one corner).
const lWireSchematic = `(kicad_sch
	(version 20231120)
	(generator "test")
	(uuid "00000000-0000-4000-8000-000000000000")
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_1_1"
				(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			)
		)
	)
	(symbol (lib_id "Device:R") (at 50.8 50.8 0) (unit 1) (in_bom yes) (on_board yes) (uuid "00000000-0000-4000-8000-000000000001")
		(property "Reference" "R1" (at 50.8 47 0) (effects (font (size 1.27 1.27))))
		(property "Value" "10k" (at 50.8 54.6 0) (effects (font (size 1.27 1.27))))
	)
	(wire (pts (xy 60 50.8) (xy 80 50.8)) (stroke (width 0) (type default)) (uuid "00000000-0000-4000-8000-000000000010"))
	(wire (pts (xy 80 50.8) (xy 80 70)) (stroke (width 0) (type default)) (uuid "00000000-0000-4000-8000-000000000011"))
)`

func TestComputeLBend(t *testing.T) {
	sch, err := sexp.ParseSchematic(lWireSchematic)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := Compute(sch)
	if m.WireCount != 2 {
		t.Errorf("WireCount = %d, want 2", m.WireCount)
	}
	if m.BendCount != 1 {
		t.Errorf("BendCount = %d, want 1 (one L-corner at 80,50.8)", m.BendCount)
	}
	if m.TotalWireLen <= 0 {
		t.Errorf("TotalWireLen = %.2f, want >0", m.TotalWireLen)
	}
}

func TestStringFormat(t *testing.T) {
	sch, err := sexp.ParseSchematic(minSchematic)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := Compute(sch).String()
	for _, want := range []string{"symbols:", "bends:", "crossings:", "wires_thru_symbol:"} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics.String missing %q\n%s", want, out)
		}
	}
}
