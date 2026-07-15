package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// Fixture pattern shared with internal/place2/gate tests: a 2-pin Device:R
// with pins at local (-2.54,0)="1" and (2.54,0)="2".
const sheetFitLibSymbols = `
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_1_1"
				(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			)
		)
	)`

func sheetFitResistor(ref string, cx, cy float64) string {
	f := func(v float64) string {
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
	}
	return `
	(symbol (lib_id "Device:R") (at ` + f(cx) + ` ` + f(cy) + ` 0) (unit 1) (in_bom yes) (on_board yes) (uuid "` + ref + `-uuid")
		(property "Reference" "` + ref + `" (at ` + f(cx) + ` ` + f(cy-3.81) + ` 0) (effects (font (size 1.27 1.27))))
		(property "Value" "10k" (at ` + f(cx) + ` ` + f(cy+3.81) + ` 0) (effects (font (size 1.27 1.27))))
	)`
}

func sheetFitWrap(body string) string {
	return `(kicad_sch
	(version 20231120)
	(generator "test")
	(paper "A4")
	(uuid "00000000-0000-4000-8000-000000000000")` + body + `
)`
}

// connectivitySignature renders TraceNets output into a canonical string so
// before/after comparisons ignore net ordering.
func connectivitySignature(sch *sexp.Schematic) string {
	var lines []string
	for _, n := range sexp.TraceNets(sch) {
		pins := make([]string, 0, len(n.Pins))
		for _, p := range n.Pins {
			pins = append(pins, p.String())
		}
		sort.Strings(pins)
		lines = append(lines, n.Name+"="+strings.Join(pins, ","))
	}
	sort.Strings(lines)
	return strings.Join(lines, ";")
}

func TestFitToSheetNegativeAndFarCoords(t *testing.T) {
	// R1 sits far in negative space, R2 far beyond the A4 right edge; a wire
	// and a label pair tie R1.2 to R2.1 (fits A3 but not A4, so the fit both
	// translates AND upsizes the paper).
	body := sheetFitLibSymbols +
		sheetFitResistor("R1", -60.96, -30.48) +
		sheetFitResistor("R2", 299.72, 101.6) + `
	(wire (pts (xy -55.88 -30.48) (xy -50.8 -30.48)) (stroke (width 0) (type default)) (uuid "w1"))
	(label "N1" (at -55.88 -30.48 0) (effects (font (size 1.27 1.27))))
	(label "N1" (at 297.18 101.6 0) (effects (font (size 1.27 1.27))))
	(junction (at -50.8 -30.48) (diameter 0) (color 0 0 0 0))
	(no_connect (at -66.04 -30.48) (uuid "nc1"))`
	sch, err := sexp.ParseSchematic(sheetFitWrap(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	beforeConn := connectivitySignature(sch)
	symsBefore := sexp.ReadSymbols(sch)
	var r1x, r1y float64
	for _, s := range symsBefore {
		if s.Reference == "R1" {
			r1x, r1y = s.X, s.Y
		}
	}

	note := fitToSheet(sch)
	if note == "" {
		t.Fatal("expected fitToSheet to translate off-sheet content, got empty note")
	}

	// (1) bbox inside usable area of the (possibly resized) paper.
	minX, minY, maxX, maxY, ok := sexp.ContentBBox(sch)
	if !ok {
		t.Fatal("no content after fit")
	}
	paper := sch.PaperSize()
	p, known := paperSizes[paper]
	if !known {
		t.Fatalf("unknown paper %q after fit", paper)
	}
	const eps = 0.01
	if minX < sheetMarginMM-eps || minY < sheetMarginMM-eps ||
		maxX > p.w-sheetMarginMM+eps || maxY > p.h-sheetMarginMM+eps {
		t.Errorf("bbox (%.2f,%.2f)-(%.2f,%.2f) outside %s usable area", minX, minY, maxX, maxY, paper)
	}

	// (2) delta is a multiple of 2.54 mm on both axes.
	var r1xa, r1ya float64
	for _, s := range sexp.ReadSymbols(sch) {
		if s.Reference == "R1" {
			r1xa, r1ya = s.X, s.Y
		}
	}
	dx, dy := r1xa-r1x, r1ya-r1y
	if rem := math.Mod(math.Abs(dx)+eps/2, sheetGridStep); rem > eps {
		t.Errorf("dx=%.4f not a multiple of %.2f (rem %.4f)", dx, sheetGridStep, rem)
	}
	if rem := math.Mod(math.Abs(dy)+eps/2, sheetGridStep); rem > eps {
		t.Errorf("dy=%.4f not a multiple of %.2f (rem %.4f)", dy, sheetGridStep, rem)
	}
	if dx == 0 && dy == 0 {
		t.Error("expected a nonzero translation")
	}

	// (3) connectivity is untouched.
	if afterConn := connectivitySignature(sch); afterConn != beforeConn {
		t.Errorf("connectivity changed:\n before %s\n after  %s", beforeConn, afterConn)
	}
}

func TestFitToSheetUpgradesToA3(t *testing.T) {
	// Two resistors 320 mm apart: wider than A4's usable 271.6 mm but inside
	// A3's 394.6 mm.
	body := sheetFitLibSymbols +
		sheetFitResistor("R1", 0, 50.8) +
		sheetFitResistor("R2", 320.04, 50.8)
	sch, err := sexp.ParseSchematic(sheetFitWrap(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	before := connectivitySignature(sch)
	fitToSheet(sch)
	if got := sch.PaperSize(); got != "A3" {
		t.Fatalf("paper = %q, want A3", got)
	}
	minX, minY, maxX, maxY, _ := sexp.ContentBBox(sch)
	p := paperSizes["A3"]
	const eps = 0.01
	if minX < sheetMarginMM-eps || minY < sheetMarginMM-eps ||
		maxX > p.w-sheetMarginMM+eps || maxY > p.h-sheetMarginMM+eps {
		t.Errorf("bbox (%.2f,%.2f)-(%.2f,%.2f) outside A3 usable area", minX, minY, maxX, maxY)
	}
	if after := connectivitySignature(sch); after != before {
		t.Errorf("connectivity changed on A3 upgrade")
	}
}

func TestFitToSheetNoOpWhenInside(t *testing.T) {
	body := sheetFitLibSymbols +
		sheetFitResistor("R1", 50.8, 50.8) +
		sheetFitResistor("R2", 101.6, 50.8)
	sch, err := sexp.ParseSchematic(sheetFitWrap(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	serialBefore := sch.Serialize()
	if note := fitToSheet(sch); note != "" {
		t.Fatalf("expected no-op, got note %q", note)
	}
	if sch.Serialize() != serialBefore {
		t.Error("schematic mutated by a no-op fit")
	}
}
