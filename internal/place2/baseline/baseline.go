// Package baseline records the Phase A baseline metrics for the canonical
// demos. The numbers come from running each demo against the legacy
// PlaceFlow + A* router stack and serve as a regression floor: subsequent
// phases must MATCH OR IMPROVE every dimension.
//
// Update entries here only when a new pipeline phase has lowered the
// numbers — that act of dropping a baseline is the proof we shipped.
package baseline

// Baseline is the captured metric set for one demo project.
type Baseline struct {
	// Tolerance is the integer slack we allow on bend/crossing/wires_thru
	// counts before failing the test. Wire-length and area floats use
	// PercentTolerance.
	BendCount         int
	CrossingCount     int
	WireThruSymbol    int
	TotalWireLenMaxMM float64
	SymbolCountMin    int
	NetCountMin       int
}

// Phase A targets — soft ceilings captured from the legacy stack.
// Numbers come from `go run ./cmd/measure_layout` over each demo with the
// pre-redesign code path (PlaceFlow + A*), with comfortable headroom so
// minor router tweaks don't trip the test.
//
// The plan's gating targets (BendCount<12, WireThruSymbol=0, etc. on
// demo_invamp) are enforced by Targets, not Baselines.
var Baselines = map[string]Baseline{
	// Phase B (cluster + rules) ceilings, locked after the 2026-05-02 run.
	// Numbers leave a small headroom over observed values so minor router
	// retunings don't trip the test.
	"led_18650": {
		BendCount:         12, // post-B observed 7
		CrossingCount:     2,
		WireThruSymbol:    0,
		TotalWireLenMaxMM: 130, // post-B observed 80
		SymbolCountMin:    3,
		NetCountMin:       3,
	},
	"inv_amp": {
		BendCount:         12, // post-B observed 9
		CrossingCount:     2,
		WireThruSymbol:    0,
		TotalWireLenMaxMM: 100, // post-B observed 58
		SymbolCountMin:    5,
		NetCountMin:       4,
	},
	"demo_mcu_i2c": {
		BendCount:         60,
		CrossingCount:     8,
		WireThruSymbol:    2,
		TotalWireLenMaxMM: 1500,
		SymbolCountMin:    10,
		NetCountMin:       5,
	},
	"demo_voltage_regulator": {
		BendCount:         30,
		CrossingCount:     4,
		WireThruSymbol:    1,
		TotalWireLenMaxMM: 800,
		SymbolCountMin:    7,
		NetCountMin:       3,
	},
	"demo_buck_converter": {
		BendCount:         30,
		CrossingCount:     4,
		WireThruSymbol:    1,
		TotalWireLenMaxMM: 800,
		SymbolCountMin:    7,
		NetCountMin:       3,
	},
	"demo_full_board": {
		BendCount:         120,
		CrossingCount:     20,
		WireThruSymbol:    4,
		TotalWireLenMaxMM: 3000,
		SymbolCountMin:    16,
		NetCountMin:       7,
	},
}

// Targets is the plan-defined ceiling that the FINAL post-redesign system
// must hit on demo_invamp. Used by integration tests in Phase E.
var Targets = map[string]Baseline{
	"inv_amp": {
		BendCount:         12,
		CrossingCount:     0,
		WireThruSymbol:    0,
		TotalWireLenMaxMM: 250,
	},
}
