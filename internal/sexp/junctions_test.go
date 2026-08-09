package sexp

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// junctionPoints lists where a schematic carries solder dots.
func junctionPoints(t *testing.T, sch *Schematic) []gridPoint {
	t.Helper()
	var out []gridPoint
	for _, j := range FindAllLists(sch.Root(), "junction") {
		at := FindList(j, "at")
		if at == nil {
			continue
		}
		out = append(out, snapPoint([2]float64{atofSexp(AtomValue(at, 1)), atofSexp(AtomValue(at, 2))}))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].y != out[j].y {
			return out[i].y < out[j].y
		}
		return out[i].x < out[j].x
	})
	return out
}

// dropJunctions removes every solder dot, leaving the wires untouched.
func dropJunctions(sch *Schematic) {
	root := sch.Root()
	kept := root.Children[:0]
	for _, c := range root.Children {
		if c.Head() == "junction" {
			continue
		}
		kept = append(kept, c)
	}
	root.Children = kept
}

// TestEnsureJunctions_ReproducesAHumansChoices is the reference measurement
// for this whole pass.
//
// The fixture is the demo buck converter as a human redrew it by hand, dots
// and all, after two readers on the KiCad forum looked at the compiler's own
// output and said almost nothing in it appeared to be connected. Strip that
// schematic's junctions and EnsureJunctions must put them back in exactly the
// same places — not more, not fewer. If it puts a dot somewhere a person did
// not, the drawing claims a connection nobody made.
func TestEnsureJunctions_ReproducesAHumansChoices(t *testing.T) {
	path := filepath.Join("testdata", "human_buck_converter.kicad_sch")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("reference schematic not available: %v", err)
	}
	sch, err := ParseSchematic(string(data))
	if err != nil {
		t.Fatal(err)
	}
	want := junctionPoints(t, sch)
	if len(want) == 0 {
		t.Fatal("the reference carries no junctions; it cannot serve as one")
	}

	dropJunctions(sch)
	if got := len(junctionPoints(t, sch)); got != 0 {
		t.Fatalf("stripping failed, %d junctions left", got)
	}

	added := EnsureJunctions(sch)
	got := junctionPoints(t, sch)

	if added != len(want) {
		t.Errorf("added %d dots, the human drew %d", added, len(want))
	}
	if len(got) != len(want) {
		t.Fatalf("got %d dots, want %d\n got: %v\nwant: %v", len(got), len(want), fmtPts(got), fmtPts(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dot %d at %s, the human put one at %s", i, fmtPt(got[i]), fmtPt(want[i]))
		}
	}
}

// A dot must never appear where a lone wire ends on a pin — that is every
// ordinary connection in a schematic, and dotting them all would be noise.
func TestEnsureJunctions_LeavesOrdinaryConnectionsAlone(t *testing.T) {
	sch := mustSchematic(t, `(kicad_sch (version 20231120) (generator "test") (paper "A4")
  (uuid "00000000-0000-0000-0000-000000000001")
  (lib_symbols)
  (wire (pts (xy 100 100) (xy 120 100)) (stroke (width 0) (type default)) (uuid "w1"))
  (wire (pts (xy 120 100) (xy 120 120)) (stroke (width 0) (type default)) (uuid "w2"))
  (sheet_instances (path "/" (page "1"))))`)

	if added := EnsureJunctions(sch); added != 0 {
		t.Errorf("a plain corner in free space needs no dot, added %d", added)
	}
}

// Three wire ends meeting is the unambiguous case: KiCad's own schematics
// carry a dot at 1313 such points and at none without one.
func TestEnsureJunctions_ThreeEndsAlwaysGetADot(t *testing.T) {
	sch := mustSchematic(t, `(kicad_sch (version 20231120) (generator "test") (paper "A4")
  (uuid "00000000-0000-0000-0000-000000000002")
  (lib_symbols)
  (wire (pts (xy 100 100) (xy 120 100)) (stroke (width 0) (type default)) (uuid "w1"))
  (wire (pts (xy 120 100) (xy 140 100)) (stroke (width 0) (type default)) (uuid "w2"))
  (wire (pts (xy 120 100) (xy 120 120)) (stroke (width 0) (type default)) (uuid "w3"))
  (sheet_instances (path "/" (page "1"))))`)

	if added := EnsureJunctions(sch); added != 1 {
		t.Fatalf("added %d dots, want exactly 1", added)
	}
	pts := junctionPoints(t, sch)
	if len(pts) != 1 || pts[0] != (gridPoint{12000, 10000}) {
		t.Errorf("dot at %v, want (120,100)", fmtPts(pts))
	}
}

// Running the pass twice must not double the dots.
func TestEnsureJunctions_Idempotent(t *testing.T) {
	sch := mustSchematic(t, `(kicad_sch (version 20231120) (generator "test") (paper "A4")
  (uuid "00000000-0000-0000-0000-000000000003")
  (lib_symbols)
  (wire (pts (xy 100 100) (xy 120 100)) (stroke (width 0) (type default)) (uuid "w1"))
  (wire (pts (xy 120 100) (xy 140 100)) (stroke (width 0) (type default)) (uuid "w2"))
  (wire (pts (xy 120 100) (xy 120 120)) (stroke (width 0) (type default)) (uuid "w3"))
  (sheet_instances (path "/" (page "1"))))`)

	first := EnsureJunctions(sch)
	second := EnsureJunctions(sch)
	if first != 1 || second != 0 {
		t.Errorf("first pass added %d, second added %d; want 1 then 0", first, second)
	}
}

func mustSchematic(t *testing.T, src string) *Schematic {
	t.Helper()
	sch, err := ParseSchematic(src)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func fmtPt(p gridPoint) string {
	return "(" + trimF(float64(p.x)/100) + "," + trimF(float64(p.y)/100) + ")"
}

func fmtPts(ps []gridPoint) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += " "
		}
		out += fmtPt(p)
	}
	return out
}

func trimF(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
