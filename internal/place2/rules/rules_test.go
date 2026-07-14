package rules

import (
	"testing"

	"mcp-kicad/internal/sexp"
)

func TestApplyPowerRailsAboveAndBelow(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "#PWR1", LibID: "power:+5V", Unit: 1, Pins: []sexp.PinInfo{{X: 100, Y: 50}}},
		{Reference: "#PWR2", LibID: "power:GND", Unit: 1, Pins: []sexp.PinInfo{{X: 100, Y: 70}}},
	}
	pos := map[string][2]float64{
		"#PWR1": {100, 50},
		"#PWR2": {100, 70},
	}
	moved := ApplyPowerRails(syms, pos)
	if moved != 2 {
		t.Fatalf("ApplyPowerRails moved %d, want 2", moved)
	}
	// +5V should be moved ABOVE its pin (smaller Y).
	if pos["#PWR1"][1] >= 50 {
		t.Errorf("+5V Y=%.2f, want < 50", pos["#PWR1"][1])
	}
	// GND should be moved BELOW its pin (larger Y).
	if pos["#PWR2"][1] <= 70 {
		t.Errorf("GND Y=%.2f, want > 70", pos["#PWR2"][1])
	}
}

func TestApplySignalFlowPushesIO(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "R1", LibID: "Device:R", Unit: 1},
		{Reference: "R2", LibID: "Device:R", Unit: 1},
		{Reference: "R3", LibID: "Device:R", Unit: 1},
	}
	pos := map[string][2]float64{
		"R1": {100, 50},
		"R2": {150, 50},
		"R3": {200, 50},
	}
	nets := []sexp.Net{
		{Name: "VIN", Pins: []sexp.PinRef{{Reference: "R1"}}},
		{Name: "VOUT", Pins: []sexp.PinRef{{Reference: "R3"}}},
	}
	moved := ApplySignalFlow(syms, nets, pos)
	if moved != 2 {
		t.Fatalf("ApplySignalFlow moved %d, want 2", moved)
	}
	if pos["R1"][0] >= pos["R2"][0] {
		t.Errorf("R1.X=%.2f should be < R2.X=%.2f", pos["R1"][0], pos["R2"][0])
	}
	if pos["R3"][0] <= pos["R2"][0] {
		t.Errorf("R3.X=%.2f should be > R2.X=%.2f", pos["R3"][0], pos["R2"][0])
	}
}

func TestApplyRotationsHorizontalNeighbours(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "R1", LibID: "Device:R", Unit: 1},
		{Reference: "R2", LibID: "Device:R", Unit: 1},
	}
	pos := map[string][2]float64{
		"R1": {50, 100},
		"R2": {200, 100},
	}
	nets := []sexp.Net{
		{Name: "TAP", Pins: []sexp.PinRef{
			{Reference: "R1"},
			{Reference: "R2"},
		}},
	}
	rot := ApplyRotations(syms, nets, pos)
	if rot["R1"] != 90 {
		t.Errorf("R1 rotation = %.0f, want 90 (neighbours horizontally)", rot["R1"])
	}
}
