package tools

import (
	"strings"
	"testing"
)

// A decoupling farm's aligned pins must share ONE rail wire and ONE symbol
// instead of a symbol per pin — the per-pin policy drew a power entry plus
// four capacitors with not a single wire, and two readers independently
// called that zone unintelligible. The rail must survive every check: gate
// clean, netlist verified.
func TestPowerRailOnDecouplingFarm(t *testing.T) {
	e := tidyEnv(t)
	d := loadDesign(t, "demo_mcu_i2c.design.json")
	sch, report, defects, _, err := e.buildSchematic(d, buildOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(defects) != 0 {
		t.Fatalf("netlist defects with rails: %v", defects)
	}
	if !strings.Contains(report, "rail:") {
		t.Fatalf("no rail was emitted for the cap farm; report:\n%s", report)
	}
	_ = sch
}

func TestDetectPowerRailsGroupsAlignedPins(t *testing.T) {
	pins := []pinPos{
		{ref: "C1.1", x: 10, y: 50, dir: 90},
		{ref: "C2.1", x: 20, y: 50, dir: 90},
		{ref: "C3.1", x: 30, y: 50, dir: 90},
		{ref: "J1.1", x: 100, y: 80, dir: 0},  // wrong direction: loose
		{ref: "C9.1", x: 200, y: 50, dir: 90}, // too far: loose
	}
	groups, loose := detectPowerRails(pins)
	if len(groups) != 1 || len(groups[0].pins) != 3 {
		t.Fatalf("groups = %+v", groups)
	}
	if len(loose) != 2 {
		t.Fatalf("loose = %+v", loose)
	}
	if got := groups[0].trunkY(); got != 50-railStub {
		t.Errorf("trunkY = %v, want %v", got, 50-railStub)
	}
}

// Two pins are not a rail: the per-pin policy stays for them.
func TestDetectPowerRailsNeedsThree(t *testing.T) {
	pins := []pinPos{
		{ref: "C1.1", x: 10, y: 50, dir: 90},
		{ref: "C2.1", x: 20, y: 50, dir: 90},
	}
	groups, loose := detectPowerRails(pins)
	if len(groups) != 0 || len(loose) != 2 {
		t.Fatalf("groups=%+v loose=%+v", groups, loose)
	}
}
