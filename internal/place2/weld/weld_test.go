package weld

import (
	"fmt"
	"strings"
	"testing"

	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/sexp"
)

// --- fixtures -------------------------------------------------------------

// libSymbols holds minimal invented definitions: a two-pin passive (pins on
// the local X axis), a four-pin IC with a real body area, and a one-pin power
// symbol. All coordinates in the fixtures are multiples of 1.27 mm so nothing
// moves when sexp.NewWire snaps to the connection grid.
var libSymbols = `
	(lib_symbols
		(symbol "Device:R" (symbol "R_1_1" ` + pin2("1", -2.54, 0, 0) + pin2("2", 2.54, 0, 180) + `))
		(symbol "MCU:U" (symbol "U_1_1" ` +
	pin2("1", -7.62, 5.08, 0) + pin2("2", -7.62, -5.08, 0) +
	pin2("3", 7.62, 5.08, 180) + pin2("4", 7.62, -5.08, 180) + `))
		(symbol "power:GND" (symbol "GND_1_1" ` + pin2("1", 0, 0, 90) + `))
	)`

func pin2(number string, x, y, angle float64) string {
	return `(pin passive line (at ` + f(x) + ` ` + f(y) + ` ` + f(angle) + `) (length 2.54)` +
		` (number "` + number + `" (effects (font (size 1.27 1.27))))` +
		` (name "~" (effects (font (size 1.27 1.27)))))`
}

func f(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return s
}

func symbolAt(libID, ref, val string, x, y, rot float64) string {
	return `
	(symbol (lib_id "` + libID + `") (at ` + f(x) + ` ` + f(y) + ` ` + f(rot) + `) (unit 1)
		(in_bom yes) (on_board yes) (uuid "sym-` + ref + `")
		(property "Reference" "` + ref + `" (at ` + f(x) + ` ` + f(y-3.81) + ` 0) (effects (font (size 1.27 1.27))))
		(property "Value" "` + val + `" (at ` + f(x) + ` ` + f(y+3.81) + ` 0) (effects (font (size 1.27 1.27))))
	)`
}

func labelAtXY(name string, x, y float64) string {
	return `
	(label "` + name + `" (at ` + f(x) + ` ` + f(y) + ` 0) (effects (font (size 1.27 1.27)))
		(uuid "lbl-` + name + "-" + f(x) + "-" + f(y) + `"))`
}

func wireXY(x1, y1, x2, y2 float64) string {
	return `
	(wire (pts (xy ` + f(x1) + ` ` + f(y1) + `) (xy ` + f(x2) + ` ` + f(y2) + `))
		(stroke (width 0) (type default)) (uuid "w-` + f(x1) + "-" + f(y1) + "-" + f(x2) + "-" + f(y2) + `"))`
}

func wrapSch(body string) string {
	return `(kicad_sch
	(version 20231120)
	(generator "test")
	(uuid "00000000-0000-4000-8000-000000000000")` + body + `
)`
}

func mustParse(t *testing.T, content string) *sexp.Schematic {
	t.Helper()
	sch, err := sexp.ParseSchematic(content)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, content)
	}
	return sch
}

// --- assertion helpers ----------------------------------------------------

func labelPositions(sch *sexp.Schematic, name string) []pt {
	var out []pt
	for _, c := range sch.Root().Children {
		if c.Head() != "label" || labelName(c) != name {
			continue
		}
		if x, y, ok := labelAt(c); ok {
			out = append(out, roundPt(x, y))
		}
	}
	return out
}

func hasWire(sch *sexp.Schematic, ax, ay, bx, by float64) bool {
	for _, s := range wireSegments(sch) {
		if (near(s.ax, ax) && near(s.ay, ay) && near(s.bx, bx) && near(s.by, by)) ||
			(near(s.ax, bx) && near(s.ay, by) && near(s.bx, ax) && near(s.by, ay)) {
			return true
		}
	}
	return false
}

// normalize replaces every uuid with a positional placeholder so two runs can
// be compared byte-for-byte despite sexp.NewWire minting random UUIDs.
func normalize(t *testing.T, serialized string) string {
	t.Helper()
	nodes, err := sexp.Parse(serialized)
	if err != nil {
		t.Fatalf("normalize parse: %v", err)
	}
	n := 0
	var walk func(*sexp.Node)
	walk = func(node *sexp.Node) {
		if node.Head() == "uuid" && len(node.Children) > 1 {
			n++
			node.Children[1] = sexp.Str(fmt.Sprintf("uuid-%d", n))
		}
		for _, c := range node.Children {
			walk(c)
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return sexp.Write(nodes)
}

// --- schematics under test ------------------------------------------------

// cleanPairSch: R1.2 and R2.1 are joined only by two "SDA" labels 15.24 mm
// apart on the same horizontal line, with nothing in between.
func cleanPairSch() string {
	return wrapSch(libSymbols +
		symbolAt("Device:R", "R1", "10k", 50.8, 50.8, 0) +
		symbolAt("Device:R", "R2", "10k", 71.12, 50.8, 0) +
		labelAtXY("SDA", 53.34, 50.8) +
		labelAtXY("SDA", 68.58, 50.8))
}

// bodyDetourSch: the two "NET1" points are diagonal to each other and a
// four-pin IC blocks the first L; the second L clears it.
func bodyDetourSch() string {
	return wrapSch(libSymbols +
		symbolAt("Device:R", "R1", "10k", 48.26, 50.8, 0) +
		symbolAt("Device:R", "R2", "10k", 78.74, 63.5, 0) +
		symbolAt("MCU:U", "U1", "MCU", 63.5, 50.8, 0) +
		labelAtXY("NET1", 50.8, 50.8) +
		labelAtXY("NET1", 76.2, 63.5))
}

// fullyBlockedSch: the two "NET1" points share a row, so the straight run is
// the only candidate — and the IC sits right on it.
func fullyBlockedSch() string {
	return wrapSch(libSymbols +
		symbolAt("Device:R", "R1", "10k", 48.26, 50.8, 0) +
		symbolAt("Device:R", "R2", "10k", 78.74, 50.8, 0) +
		symbolAt("MCU:U", "U1", "MCU", 63.5, 50.8, 0) +
		labelAtXY("NET1", 50.8, 50.8) +
		labelAtXY("NET1", 76.2, 50.8))
}

// crossingSch: the only corridor between the two "SIG" points is cut by a
// live wire of another net.
func crossingSch() string {
	return wrapSch(libSymbols +
		symbolAt("Device:R", "R1", "10k", 48.26, 50.8, 0) +
		symbolAt("Device:R", "R2", "10k", 78.74, 50.8, 0) +
		symbolAt("Device:R", "R3", "10k", 63.5, 40.64, 90) +
		symbolAt("Device:R", "R4", "10k", 63.5, 60.96, 90) +
		labelAtXY("SIG", 50.8, 50.8) +
		labelAtXY("SIG", 76.2, 50.8) +
		wireXY(63.5, 43.18, 63.5, 58.42))
}

// threeIslandSch: one net split into three islands stacked vertically.
func threeIslandSch() string {
	return wrapSch(libSymbols +
		symbolAt("Device:R", "R1", "10k", 48.26, 40.64, 0) +
		symbolAt("Device:R", "R2", "10k", 48.26, 50.8, 0) +
		symbolAt("Device:R", "R3", "10k", 48.26, 60.96, 0) +
		labelAtXY("BUS", 50.8, 40.64) +
		labelAtXY("BUS", 50.8, 50.8) +
		labelAtXY("BUS", 50.8, 60.96))
}

// railSch: two power:GND symbols hold one rail with a perfectly clean
// vertical corridor between them. Geometry alone would weld it.
func railSch() string {
	return wrapSch(libSymbols +
		symbolAt("Device:R", "R1", "10k", 50.8, 40.64, 90) +
		symbolAt("Device:R", "R2", "10k", 50.8, 60.96, 90) +
		symbolAt("power:GND", "#PWR01", "GND", 50.8, 43.18, 0) +
		symbolAt("power:GND", "#PWR02", "GND", 50.8, 58.42, 0))
}

// --- tests ----------------------------------------------------------------

// The headline case: two tags 15 mm apart become one straight wire and a
// single surviving label.
func TestCleanPairBecomesStraightWire(t *testing.T) {
	sch := mustParse(t, cleanPairSch())

	res := Weld(sch)
	if res.Welded != 1 {
		t.Fatalf("Welded = %d, want 1 (notes: %v)", res.Welded, res.Notes)
	}
	if res.LabelsRemoved != 1 {
		t.Errorf("LabelsRemoved = %d, want 1", res.LabelsRemoved)
	}
	if got := wireSegments(sch); len(got) != 1 {
		t.Fatalf("wire count = %d, want 1: %+v", len(got), got)
	}
	if !hasWire(sch, 53.34, 50.8, 68.58, 50.8) {
		t.Errorf("expected the straight wire (53.34,50.8)-(68.58,50.8), got %+v", wireSegments(sch))
	}
	labels := labelPositions(sch, "SDA")
	if len(labels) != 1 {
		t.Fatalf("SDA labels = %d, want 1: %v", len(labels), labels)
	}
	if labels[0] != (pt{53.34, 50.8}) {
		t.Errorf("surviving label at %v, want the leftmost (53.34, 50.8)", labels[0])
	}
	if v := gate.Check(sch); len(v) != 0 {
		t.Errorf("gate found %d violation(s) after welding: %+v", len(v), v)
	}
}

// A symbol body on the direct path must be routed around, not through.
func TestSymbolBodyIsRoutedAround(t *testing.T) {
	sch := mustParse(t, bodyDetourSch())

	res := Weld(sch)
	if res.Welded != 1 {
		t.Fatalf("Welded = %d, want 1 (notes: %v)", res.Welded, res.Notes)
	}
	if got := wireSegments(sch); len(got) != 2 {
		t.Fatalf("wire count = %d, want 2 (an L): %+v", len(got), got)
	}
	// The clear L is the one turning at (50.8, 63.5); the other corner cuts U1.
	if !hasWire(sch, 50.8, 50.8, 50.8, 63.5) || !hasWire(sch, 50.8, 63.5, 76.2, 63.5) {
		t.Errorf("expected the L through (50.8, 63.5), got %+v", wireSegments(sch))
	}
	for _, v := range gate.Check(sch) {
		if v.Kind == gate.WireThruSymbol {
			t.Errorf("welded wire cuts a symbol body: %s", v.Detail)
		}
	}
	if n := len(labelPositions(sch, "NET1")); n != 1 {
		t.Errorf("NET1 labels = %d, want 1", n)
	}
}

// When the body covers the only possible corridor, nothing is welded and the
// labels stay exactly as they were.
func TestFullyBlockedPairIsLeftAlone(t *testing.T) {
	sch := mustParse(t, fullyBlockedSch())
	before := sch.Serialize()

	res := Weld(sch)
	if res.Welded != 0 || res.LabelsRemoved != 0 {
		t.Fatalf("Result = %+v, want no changes", res)
	}
	if got := wireSegments(sch); len(got) != 0 {
		t.Errorf("wire count = %d, want 0: %+v", len(got), got)
	}
	if n := len(labelPositions(sch, "NET1")); n != 2 {
		t.Errorf("NET1 labels = %d, want the original 2", n)
	}
	if sch.Serialize() != before {
		t.Error("a blocked net must leave the schematic untouched")
	}
}

// A candidate that would short two nets is rejected by the gate and rolled
// back completely.
func TestCrossNetCandidateIsRejected(t *testing.T) {
	sch := mustParse(t, crossingSch())
	if v := gate.Check(sch); len(v) != 0 {
		t.Fatalf("fixture is not clean to begin with: %+v", v)
	}
	before := sch.Serialize()

	res := Weld(sch)
	if res.Welded != 0 {
		t.Fatalf("Welded = %d, want 0 (notes: %v)", res.Welded, res.Notes)
	}
	if got := wireSegments(sch); len(got) != 1 {
		t.Errorf("wire count = %d, want the 1 pre-existing wire: %+v", len(got), got)
	}
	if n := len(labelPositions(sch, "SIG")); n != 2 {
		t.Errorf("SIG labels = %d, want the original 2", n)
	}
	if sch.Serialize() != before {
		t.Error("a rejected candidate must be reverted byte-for-byte")
	}
}

// Three islands collapse to one, greedily closing the shortest gap first.
func TestThreeIslandsCollapseToOne(t *testing.T) {
	sch := mustParse(t, threeIslandSch())

	res := Weld(sch)
	if res.Welded != 2 {
		t.Fatalf("Welded = %d, want 2 (notes: %v)", res.Welded, res.Notes)
	}
	if res.LabelsRemoved != 2 {
		t.Errorf("LabelsRemoved = %d, want 2", res.LabelsRemoved)
	}
	if !hasWire(sch, 50.8, 40.64, 50.8, 50.8) || !hasWire(sch, 50.8, 50.8, 50.8, 60.96) {
		t.Errorf("expected both vertical welds, got %+v", wireSegments(sch))
	}
	labels := labelPositions(sch, "BUS")
	if len(labels) != 1 {
		t.Fatalf("BUS labels = %d, want 1: %v", len(labels), labels)
	}
	if labels[0] != (pt{50.8, 40.64}) {
		t.Errorf("surviving label at %v, want the top-left (50.8, 40.64)", labels[0])
	}
	if comps := componentsOf(sch, "BUS"); len(comps) != 1 {
		t.Errorf("BUS still has %d islands, want 1", len(comps))
	}
	if v := gate.Check(sch); len(v) != 0 {
		t.Errorf("gate found %d violation(s) after welding: %+v", len(v), v)
	}
}

// Power rails are owned by the per-pin power-symbol policy: never welded,
// even when the corridor is spotless.
func TestPowerRailIsNeverWelded(t *testing.T) {
	sch := mustParse(t, railSch())
	before := sch.Serialize()

	// Sanity: the rail really is split in two wire-islands with a clear path.
	if comps := componentsOf(sch, "GND"); len(comps) != 2 {
		t.Fatalf("fixture GND has %d islands, want 2", len(comps))
	}
	r := routes(pt{50.8, 43.18}, pt{50.8, 58.42})
	if len(r) != 1 || !acceptable(r[0], minWeldReach) || !clearOfBodies(r[0], bodyBoxes(sch)) {
		t.Fatalf("fixture corridor is not clean, so the test would prove nothing")
	}

	res := Weld(sch)
	if res.Welded != 0 || res.LabelsRemoved != 0 {
		t.Fatalf("Result = %+v, want no changes on a power rail", res)
	}
	if sch.Serialize() != before {
		t.Error("power rail schematic was modified")
	}
}

// A second pass has nothing left to do.
func TestWeldIsIdempotent(t *testing.T) {
	sch := mustParse(t, threeIslandSch())

	if first := Weld(sch); first.Welded == 0 {
		t.Fatal("first pass welded nothing; the fixture is wrong")
	}
	after := sch.Serialize()

	second := Weld(sch)
	if second.Welded != 0 || second.LabelsRemoved != 0 || second.Notes != nil {
		t.Errorf("second pass returned %+v, want the zero Result", second)
	}
	if sch.Serialize() != after {
		t.Error("second pass changed the serialized schematic")
	}
}

// Same input, same output — no map iteration may leak into the result.
func TestWeldIsDeterministic(t *testing.T) {
	for _, src := range []string{cleanPairSch(), bodyDetourSch(), threeIslandSch(), crossingSch()} {
		a := mustParse(t, src)
		b := mustParse(t, src)

		ra, rb := Weld(a), Weld(b)
		if ra.Welded != rb.Welded || ra.LabelsRemoved != rb.LabelsRemoved {
			t.Errorf("counts differ between runs: %+v vs %+v", ra, rb)
		}
		if strings.Join(ra.Notes, "|") != strings.Join(rb.Notes, "|") {
			t.Errorf("notes differ between runs:\n%v\n%v", ra.Notes, rb.Notes)
		}
		if normalize(t, a.Serialize()) != normalize(t, b.Serialize()) {
			t.Error("two runs on identical input produced different schematics")
		}
	}
}

// The beauty filters reject candidates before the gate is even consulted.
func TestBeautyFilters(t *testing.T) {
	long := routes(pt{0, 0}, pt{63.5, 0})
	if len(long) != 1 || acceptable(long[0], minWeldReach) {
		t.Errorf("a %0.2f mm straight run must be rejected as too long", 63.5)
	}
	// A 1.27 mm jog can only be drawn with a stubby segment, so every
	// multi-segment candidate must be rejected.
	for _, r := range routes(pt{0, 0}, pt{25.4, 1.27}) {
		if acceptable(r, minWeldReach) {
			t.Errorf("accepted a route with a 1.27 mm jog: %v", r.pts)
		}
	}
	found := false
	for _, r := range routes(pt{0, 0}, pt{25.4, 5.08}) {
		if !acceptable(r, minWeldReach) {
			continue
		}
		found = true
		for i := 0; i+1 < len(r.pts); i++ {
			if l := segLen(r.pts[i], r.pts[i+1]); l < minSegLen-eps {
				t.Errorf("accepted route keeps a %.2f mm segment", l)
			}
		}
	}
	if !found {
		t.Error("expected at least one acceptable route for a 25.4 x 5.08 mm offset")
	}
	single := routes(pt{0, 0}, pt{1.27, 0})
	if len(single) != 1 || !acceptable(single[0], minWeldReach) {
		t.Error("a lone short straight run must be exempt from the min-segment filter")
	}
}

// String stays a single line in both states.
func TestResultString(t *testing.T) {
	for _, r := range []Result{{}, {Welded: 3, LabelsRemoved: 4}} {
		s := r.String()
		if strings.Contains(s, "\n") || !strings.HasPrefix(s, "weld: ") {
			t.Errorf("String() = %q, want a single line prefixed with \"weld: \"", s)
		}
	}
}
