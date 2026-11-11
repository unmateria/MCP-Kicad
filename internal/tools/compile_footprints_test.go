package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/sexp"
	"mcp-kicad/internal/symlib"
)

// Three entries of the default table named footprints KiCad 10 does not ship.
// A default that does not exist is caught at compile time and left empty, but
// it should never have been in the table.
func TestDefaultFootprintsExist(t *testing.T) {
	e := tidyEnv(t)
	for _, fp := range parts.DefaultFootprintRefs() {
		lib, name, _ := strings.Cut(fp, ":")
		if _, err := os.Stat(filepath.Join(e.KicadFootprints, lib+".pretty", name+".kicad_mod")); err != nil {
			t.Errorf("default footprint %s is not installed", fp)
		}
	}
}

func footprintsOf(t *testing.T, e *Env, d *compile.Design) (map[string]string, string, error) {
	t.Helper()
	sch, _, _, _, err := e.buildSchematic(d, buildOpts{})
	if err != nil {
		t.Fatal(err)
	}
	note, err := e.assignFootprints(sch, d)
	got := map[string]string{}
	for _, n := range sch.Root().Children {
		if n.Head() == "symbol" && sexp.AtomValue(sexp.FindList(n, "on_board"), 1) == "yes" {
			got[symlib.Property(n, "Reference")] = symlib.Property(n, "Footprint")
		}
	}
	return got, note, err
}

func TestAssignFootprints(t *testing.T) {
	e := tidyEnv(t)

	d := loadDesign(t, "led_18650.design.json")
	got, note, err := footprintsOf(t, e, d)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"BT1": "Battery:BatteryHolder_Keystone_1042_1x18650",
		"R1":  "Resistor_THT:R_Axial_DIN0207_L6.3mm_D2.5mm_P7.62mm_Horizontal",
		"D1":  "LED_THT:LED_D5.0mm",
	}
	for ref, fp := range want {
		if got[ref] != fp {
			t.Errorf("THT %s: footprint %q, want %q\n%s", ref, got[ref], fp, note)
		}
	}

	d.Mount = "SMD"
	d.Blocks[0].Symbols[1].Footprint = "Resistor_SMD:R_1206_3216Metric"
	got, note, err = footprintsOf(t, e, d)
	if err != nil {
		t.Fatal(err)
	}
	if got["R1"] != "Resistor_SMD:R_1206_3216Metric" {
		t.Errorf("explicit footprint ignored: R1 = %q\n%s", got["R1"], note)
	}
	if got["D1"] != "LED_SMD:LED_0603_1608Metric" {
		t.Errorf("SMD default ignored: D1 = %q\n%s", got["D1"], note)
	}

	// An explicit footprint that does not exist, or lacks a pad for one of
	// the symbol's pins, is an authoring error rather than a silent gap.
	for _, bad := range []string{"Resistor_SMD:R_DOES_NOT_EXIST", "TestPoint:TestPoint_Pad_1.0x1.0mm"} {
		d.Blocks[0].Symbols[1].Footprint = bad
		if _, _, err := footprintsOf(t, e, d); err == nil {
			t.Errorf("explicit footprint %s accepted", bad)
		}
	}
}

// The library's own pairing wins over the default table: an ATmega328-P is a
// DIP-28 because its symbol says so, not because of anything we chose.
func TestAssignFootprintsPrefersLibrary(t *testing.T) {
	e := tidyEnv(t)
	d := loadDesign(t, "ne555_astable.design.json")
	got, note, err := footprintsOf(t, e, d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "library   Package_DIP:DIP-8_W7.62mm") {
		t.Errorf("NE555P should take the library's DIP-8:\n%s", note)
	}
	for ref, fp := range got {
		if fp == "" {
			t.Errorf("%s left without a footprint\n%s", ref, note)
		}
	}
}
