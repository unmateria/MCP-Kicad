package optimize

import (
	"testing"

	"mcp-kicad/internal/sexp"
)

func mkSym(ref string, x, y float64, pins ...[2]float64) sexp.SchematicSymbol {
	pinInfos := make([]sexp.PinInfo, len(pins))
	for i, p := range pins {
		pinInfos[i] = sexp.PinInfo{Number: "1", X: p[0], Y: p[1]}
	}
	return sexp.SchematicSymbol{Reference: ref, X: x, Y: y, Pins: pinInfos}
}

// TestPinAxisAlignment: two resistors at same Y → 0 penalty; offset by 2.54mm → > 0.
func TestPinAxisAlignment(t *testing.T) {
	r1 := mkSym("R1", 0, 0, [2]float64{0, 0})
	r2 := mkSym("R2", 10, 0, [2]float64{10, 0})
	aligned := Layout{
		Symbols: []sexp.SchematicSymbol{r1, r2},
		Wires:   []Wire{{X1: 0, Y1: 0, X2: 10, Y2: 0}},
	}
	cost := Cost(aligned)
	if cost.PinAxisMisalign > 0.001 {
		t.Errorf("aligned 2-pin net should score 0 PinAxisMisalign, got %f", cost.PinAxisMisalign)
	}

	r3 := mkSym("R3", 10, 2.54, [2]float64{10, 2.54})
	misaligned := Layout{
		Symbols: []sexp.SchematicSymbol{r1, r3},
		Wires:   []Wire{{X1: 0, Y1: 0, X2: 10, Y2: 2.54}},
	}
	cost = Cost(misaligned)
	if cost.PinAxisMisalign <= 0 {
		t.Errorf("misaligned 2-pin net should score > 0 PinAxisMisalign, got %f", cost.PinAxisMisalign)
	}
}

// TestScore100Bounds verifies Score100 returns a value in [0, 100].
func TestScore100Bounds(t *testing.T) {
	cases := []CostBreakdown{
		{Total: 0},
		{Total: 100},
		{Total: 5000},
		{Total: 100000},
		{Total: -50}, // shouldn't normally happen but be defensive
	}
	for _, c := range cases {
		s := c.Score100()
		if s < 0 || s > 100 {
			t.Errorf("Score100(%v) = %d out of [0,100]", c, s)
		}
	}
}

// TestScore100Calibration: clean layout (no penalties) → 100; very bad → low.
func TestScore100Calibration(t *testing.T) {
	clean := CostBreakdown{Total: 0}
	if clean.Score100() != 100 {
		t.Errorf("clean layout should score 100, got %d", clean.Score100())
	}
	bad := CostBreakdown{Total: 10000}
	if bad.Score100() > 35 {
		t.Errorf("very bad layout should score < 35, got %d", bad.Score100())
	}
}

// TestAxisAlignBonus rewards connected symbols sharing a coordinate.
func TestAxisAlignBonus(t *testing.T) {
	r1 := mkSym("R1", 0, 0, [2]float64{0, 0})
	r2 := mkSym("R2", 10, 0, [2]float64{10, 0})
	cost := Cost(Layout{
		Symbols: []sexp.SchematicSymbol{r1, r2},
		Wires:   []Wire{{X1: 0, Y1: 0, X2: 10, Y2: 0}},
	})
	if cost.AxisAlignBonus < 1 {
		t.Errorf("connected aligned symbols should grant bonus ≥ 1, got %d", cost.AxisAlignBonus)
	}
}
