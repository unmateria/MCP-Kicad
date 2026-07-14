package sexp

import "testing"

const minimalSchWith2DupWires = `(kicad_sch
	(version 20231120)
	(generator "test")
	(uuid "00000000-0000-0000-0000-000000000001")
	(paper "A4")
	(wire (pts (xy 10 10) (xy 20 10)) (stroke (width 0)) (uuid "w1"))
	(wire (pts (xy 10 10) (xy 20 10)) (stroke (width 0)) (uuid "w2"))
	(wire (pts (xy 20 10) (xy 10 10)) (stroke (width 0)) (uuid "w3"))
	(wire (pts (xy 30 30) (xy 40 30)) (stroke (width 0)) (uuid "w4"))
	(sheet_instances (path "/" (page "1")))
	(symbol_instances)
)`

// TestDedupeWires removes exact duplicates and reverse duplicates.
func TestDedupeWires(t *testing.T) {
	sch, err := ParseSchematic(minimalSchWith2DupWires)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(sch.Wires()); got != 4 {
		t.Fatalf("expected 4 initial wires, got %d", got)
	}
	removed := sch.DedupeWires()
	// w1, w2, w3 all share endpoints {(10,10),(20,10)} → 2 removed.
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}
	if got := len(sch.Wires()); got != 2 {
		t.Errorf("expected 2 wires after dedupe, got %d", got)
	}
}
