package tools

import (
	"os"
	"path/filepath"
	"testing"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/sexp"
)

// The lab fixture places R1, R2 and R3 in a row with their pin-1 tips on one
// horizontal line (y=25.40) at x = 27.94, 43.18 and 58.42. Its source
// declares SIG={R1.1,R3.1}, OTHER={R2.1} and GND={R1.2,R2.2,R3.2}, so any
// wire drawn straight from R1.1 to R3.1 runs exactly over R2.1 — the geometry
// these tests are about.
func loadLab(t *testing.T) (*sexp.Schematic, *compile.Design) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "lab_pinrow.kicad_sch"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	d, err := compile.ParseDesignFile(filepath.Join("testdata", "lab_pinrow.design.json"))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	return sch, d
}

// rewire replaces all wiring with the GND stubs plus one caller-supplied
// segment for SIG.
func rewire(sch *sexp.Schematic, sig [4]float64) {
	sch.RemoveWires()
	for _, w := range [][4]float64{
		{27.94, 33.02, 27.94, 35.56}, // R1.2 -> #PWR01
		{43.18, 33.02, 43.18, 35.56}, // R2.2 -> #PWR02
		{58.42, 33.02, 58.42, 35.56}, // R3.2 -> #PWR03
		{27.94, 33.02, 25.40, 33.02}, // R1.2 -> #FLG01
		sig,
	} {
		sch.AddWire(sexp.NewWire(w[0], w[1], w[2], w[3]))
	}
}

func TestVerifyNetlistAcceptsTheCompiledFixture(t *testing.T) {
	sch, d := loadLab(t)
	if defects := VerifyNetlist(sch, d); len(defects) != 0 {
		t.Fatalf("compiled fixture should verify clean, got %v", defects)
	}
}

// A wire crossing a pin tip mid-segment connects nothing in KiCad — verified
// against kicad-cli sch export netlist, which keeps R2.1 alone on /OTHER
// while /SIG holds R1.1 and R3.1. The netlist is therefore correct and this
// check must stay silent: the defect is that a reader's eye sees a junction
// there, which is the geometric gate's business, not the netlist's.
func TestVerifyNetlistWireOverForeignPinIsNotAShort(t *testing.T) {
	sch, d := loadLab(t)
	rewire(sch, [4]float64{27.94, 25.40, 58.42, 25.40}) // straight over R2.1

	if defects := VerifyNetlist(sch, d); len(defects) != 0 {
		t.Fatalf("crossing a pin tip is not an electrical defect, got %v", defects)
	}
}

// A wire ENDING on a foreign pin does connect — kicad-cli reports R1.1 and
// R2.1 together on /OTHER with R3.1 left unconnected. Geometry cannot catch
// this (the two nets are now one consistent net, and ERC stays quiet between
// passive pins), which is precisely why the netlist post-condition exists.
func TestVerifyNetlistCatchesWireEndingOnForeignPin(t *testing.T) {
	sch, d := loadLab(t)
	rewire(sch, [4]float64{27.94, 25.40, 43.18, 25.40}) // ends exactly on R2.1

	defects := VerifyNetlist(sch, d)
	if len(defects) == 0 {
		t.Fatal("a wire landing on R2.1 shorts SIG to OTHER and strands R3.1, but nothing was reported")
	}

	var split, merged bool
	for _, def := range defects {
		switch def.Kind {
		case DefectSplit:
			split = true
			if def.Net != "SIG" {
				t.Errorf("SPLIT should blame SIG, blamed %s", def.Net)
			}
		case DefectMerged:
			merged = true
		}
	}
	if !split {
		t.Errorf("R3.1 was stranded but no SPLIT reported: %v", defects)
	}
	if !merged {
		t.Errorf("SIG and OTHER share one net but no MERGED reported: %v", defects)
	}
}

// Determinism is non-negotiable in this pipeline: the same schematic must
// produce byte-identical reports run to run, so the defect list may never be
// built by ranging over a map.
func TestVerifyNetlistIsDeterministic(t *testing.T) {
	sch, d := loadLab(t)
	rewire(sch, [4]float64{27.94, 25.40, 43.18, 25.40})

	first := VerifyNetlist(sch, d)
	for i := 0; i < 20; i++ {
		again := VerifyNetlist(sch, d)
		if len(again) != len(first) {
			t.Fatalf("run %d returned %d defects, first run %d", i, len(again), len(first))
		}
		for j := range again {
			if again[j] != first[j] {
				t.Fatalf("run %d defect %d = %v, first run %v", i, j, again[j], first[j])
			}
		}
	}
}

// A +3V3/GND pair sitting flush on the two pins of one connector is the
// symbol's own pitch, not something the author can space out — the warning
// must not fire for it. demo_mcu_i2c's J1 is the case that used to trip this.
func TestFlushPowerPairsIgnoresOneComponentsOwnPins(t *testing.T) {
	e := tidyEnv(t)
	d := loadDesign(t, "demo_mcu_i2c.design.json")
	sch, _, _, _, err := e.buildSchematic(d, buildOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if flush := flushPowerPairs(sch); len(flush) != 0 {
		t.Errorf("flush warning fired for pins of one connector: %v", flush)
	}
}
