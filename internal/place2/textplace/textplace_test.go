package textplace

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// --- fixtures -------------------------------------------------------------

// libSymbols holds minimal invented definitions: three two-pin passives
// (pins on the local X axis), a four-pin IC with a real body area, and a
// one-pin power symbol.
var libSymbols = `
	(lib_symbols
		(symbol "Device:R" (symbol "R_1_1" ` + pin2("1", -2.54, 0, 0) + pin2("2", 2.54, 0, 180) + `))
		(symbol "Device:C" (symbol "C_1_1" ` + pin2("1", -2.54, 0, 0) + pin2("2", 2.54, 0, 180) + `))
		(symbol "Device:L" (symbol "L_1_1" ` + pin2("1", -2.54, 0, 0) + pin2("2", 2.54, 0, 180) + `))
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

// field is one Reference/Value property of a fixture symbol.
type field struct {
	x, y, rot float64
	hidden    bool
}

// symbolAt renders a placed instance. refF/valF give the exact anchors so a
// test can start from a deliberately bad text layout.
func symbolAt(libID, ref, val string, x, y, rot float64, refF, valF field) string {
	prop := func(name, value string, fl field) string {
		effects := `(effects (font (size 1.27 1.27))`
		if fl.hidden {
			effects += ` (hide yes)`
		}
		effects += `)`
		return `(property "` + name + `" "` + value + `" (at ` + f(fl.x) + ` ` + f(fl.y) + ` ` + f(fl.rot) + `) ` + effects + `)`
	}
	return `
	(symbol (lib_id "` + libID + `") (at ` + f(x) + ` ` + f(y) + ` ` + f(rot) + `) (unit 1) (in_bom yes) (on_board yes) (uuid "` + ref + `-uuid")
		` + prop("Reference", ref, refF) + `
		` + prop("Value", val, valF) + `
	)`
}

// symbolDefault renders an instance with KiCad's default field anchors:
// Reference 3.81 mm above the body, Value 3.81 mm below, both horizontal.
func symbolDefault(libID, ref, val string, x, y, rot float64) string {
	return symbolAt(libID, ref, val, x, y, rot,
		field{x: x, y: y - 3.81}, field{x: x, y: y + 3.81})
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

// sixSymbolSchematic is a mixed board: horizontal and vertical passives, a
// rotated inductor, a four-pin IC, a power symbol with a hidden Reference,
// one wire, one no-connect and two net labels.
func sixSymbolSchematic() string {
	return wrapSch(libSymbols +
		symbolDefault("Device:R", "R1", "10k", 50, 50, 0) +
		symbolDefault("Device:R", "R2", "4k7", 50, 80, 90) +
		symbolDefault("Device:C", "C1", "100n", 80, 50, 0) +
		symbolDefault("Device:L", "L1", "33uH", 80, 80, 270) +
		symbolDefault("MCU:U", "U1", "MCU", 120, 65, 0) +
		symbolAt("power:GND", "#PWR01", "GND", 120, 100, 0,
			field{x: 120, y: 100, hidden: true}, field{x: 120, y: 97.46}) + `
	(wire (pts (xy 52.54 50) (xy 77.46 50)) (stroke (width 0) (type default)) (uuid "w1"))
	(no_connect (at 127.62 70.08) (uuid "nc1"))
	(label "VIN" (at 47.46 50 180) (effects (font (size 1.27 1.27))) (uuid "lbl-vin"))
	(label "VOUT" (at 112.38 59.92 180) (effects (font (size 1.27 1.27))) (uuid "lbl-vout"))`)
}

// --- assertion helpers ----------------------------------------------------

// textItem is one visible text rectangle with the name of whatever owns it.
type textItem struct {
	owner string
	b     box
}

func textItems(t *testing.T, sch *sexp.Schematic) []textItem {
	t.Helper()
	syms := sexp.ReadSymbols(sch)
	insts := instanceNodes(sch)
	if len(insts) != len(syms) {
		t.Fatalf("cannot pair %d instance nodes with %d resolved symbols", len(insts), len(syms))
	}
	var out []textItem
	for i, in := range insts {
		lines := visibleFields(in)
		if len(lines) == 0 {
			continue
		}
		b, ok := blockBounds(lines, syms[i].Rotation)
		if !ok {
			t.Fatalf("%s: text block has no usable (at ...)", syms[i].Reference)
		}
		out = append(out, textItem{owner: syms[i].Reference, b: b})
	}
	for _, c := range sch.Root().Children {
		if c.Head() != "label" {
			continue
		}
		x, y, ok := atXY(c)
		if !ok {
			continue
		}
		name := sexp.StringValue(c, 1)
		out = append(out, textItem{owner: "label:" + name, b: labelBox(name, x, y, atRot(c), labelJustifyRight(c))})
	}
	return out
}

// assertNoTextCollisions checks the global invariant: no two visible text
// rectangles overlap, and no text rectangle sits on a foreign symbol body.
func assertNoTextCollisions(t *testing.T, sch *sexp.Schematic) {
	t.Helper()
	items := textItems(t, sch)
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			if a := items[i].b.overlap(items[j].b); a > eps {
				t.Errorf("text of %s overlaps text of %s by %.2f mm^2", items[i].owner, items[j].owner, a)
			}
		}
	}
	for _, s := range sexp.ReadSymbols(sch) {
		x1, y1, x2, y2 := metrics.BodyBBox(s)
		body := box{x1, y1, x2, y2}.inflate()
		for _, it := range items {
			if it.owner == s.Reference {
				continue
			}
			if a := it.b.overlap(body); a > eps {
				t.Errorf("text of %s overlaps body of %s by %.2f mm^2", it.owner, s.Reference, a)
			}
		}
	}
}

// blockOf returns the current text-block rectangle of one reference.
func blockOf(t *testing.T, sch *sexp.Schematic, ref string) box {
	t.Helper()
	for _, it := range textItems(t, sch) {
		if it.owner == ref {
			return it.b
		}
	}
	t.Fatalf("no visible text block for %s", ref)
	return box{}
}

// propAnchor returns the (at x y rot) of one property of one symbol.
func propAnchor(t *testing.T, sch *sexp.Schematic, ref, propName string) (x, y, rot float64) {
	t.Helper()
	for _, in := range instanceNodes(sch) {
		if p := findProp(in, "Reference"); p == nil || propText(p) != ref {
			continue
		}
		p := findProp(in, propName)
		if p == nil {
			t.Fatalf("%s has no %s property", ref, propName)
		}
		px, py, ok := atXY(p)
		if !ok {
			t.Fatalf("%s.%s has no (at ...)", ref, propName)
		}
		return px, py, atRot(p)
	}
	t.Fatalf("symbol %s not found", ref)
	return 0, 0, 0
}

func labelNode(t *testing.T, sch *sexp.Schematic, name string) *sexp.Node {
	t.Helper()
	for _, c := range sch.Root().Children {
		if c.Head() == "label" && sexp.StringValue(c, 1) == name {
			return c
		}
	}
	t.Fatalf("label %q not found", name)
	return nil
}

// sceneOverlap scores a rectangle against the whole obstacle scene.
func sceneOverlap(sch *sexp.Schematic, b box) float64 {
	obs, _, _ := buildScene(sch, sexp.ReadSymbols(sch))
	return overlapSum(b, obs, -1)
}

// --- tests ----------------------------------------------------------------

// A 270°-rotated inductor prints "33uH" vertically straight across the VOUT
// label. The field must come back horizontal and clear of everything.
func TestRotatedFieldBecomesHorizontalAndClear(t *testing.T) {
	body := libSymbols +
		symbolAt("Device:L", "L1", "33uH", 100, 100, 270,
			field{x: 105, y: 100, rot: 270}, field{x: 95, y: 100, rot: 270}) + `
	(label "VOUT" (at 96 100 180) (effects (font (size 1.27 1.27))) (uuid "lbl-vout"))`
	sch := mustParse(t, wrapSch(body))

	before := blockOf(t, sch, "L1")
	if got := before.overlap(labelBox("VOUT", 96, 100, 180, false)); got <= eps {
		t.Fatalf("fixture is not colliding: overlap %.2f mm^2", got)
	}

	fields, _ := Autoplace(sch)
	if fields == 0 {
		t.Fatal("expected the rotated fields to be moved")
	}

	// L1 is rotated 270: the field angle must COMPENSATE the symbol rotation
	// (KiCad renders at field+symbol), so displayed = rot+270 ≡ 0 mod 180.
	for _, name := range []string{"Reference", "Value"} {
		if _, _, rot := propAnchor(t, sch, "L1", name); math.Mod(rot+270, 180) != 0 {
			t.Errorf("%s must DISPLAY horizontal after Autoplace, got field rot=%v on a 270 symbol", name, rot)
		}
	}
	if got := sceneOverlap(sch, blockOf(t, sch, "L1")); got > eps {
		t.Errorf("L1 text still overlaps the scene by %.2f mm^2", got)
	}
	assertNoTextCollisions(t, sch)
}

// Two neighbours whose default Reference/Value blocks are wider than the gap
// between them: the pass must pull them apart.
func TestAdjacentSymbolTextsSeparated(t *testing.T) {
	body := libSymbols +
		symbolDefault("Device:R", "R1", "100kOhm", 100, 100, 0) +
		symbolDefault("Device:R", "R2", "220uF250V", 108, 100, 0)
	sch := mustParse(t, wrapSch(body))

	if got := blockOf(t, sch, "R1").overlap(blockOf(t, sch, "R2")); got <= eps {
		t.Fatalf("fixture is not colliding: overlap %.2f mm^2", got)
	}

	if fields, _ := Autoplace(sch); fields == 0 {
		t.Fatal("expected fields to be moved")
	}
	if got := blockOf(t, sch, "R1").overlap(blockOf(t, sch, "R2")); got > eps {
		t.Errorf("R1 and R2 text blocks still overlap by %.2f mm^2", got)
	}
	assertNoTextCollisions(t, sch)
}

// A label reading rightwards from the left edge of an IC lands inside the
// body; it must flip to 180 while its anchor — the electrical point — stays.
func TestLabelFlipsKeepingAnchor(t *testing.T) {
	body := libSymbols +
		symbolDefault("MCU:U", "U1", "MCU", 100, 100, 0) + `
	(label "A" (at 94.92 100 0) (effects (font (size 1.27 1.27))) (uuid "lbl-a"))`
	sch := mustParse(t, wrapSch(body))

	_, flipped := Autoplace(sch)
	if flipped != 1 {
		t.Fatalf("expected exactly 1 label flipped, got %d", flipped)
	}
	x, y, ok := atXY(labelNode(t, sch, "A"))
	if !ok {
		t.Fatal("label lost its (at ...)")
	}
	if x != 94.92 || y != 100 {
		t.Errorf("label anchor moved to (%v, %v); it must stay at (94.92, 100)", x, y)
	}
	// Flipping a horizontal label means changing its JUSTIFICATION, not its
	// angle: KiCad draws (0, right) and (180, right) identically, and an
	// angle change on its own moves the text nowhere. Verified by exporting
	// SVG for all eight angle/justify pairs and measuring the drawn glyphs.
	if !labelJustifyRight(labelNode(t, sch, "A")) {
		t.Error("expected the label to end up right-justified so it reads away from the body")
	}
	assertNoTextCollisions(t, sch)
}

// A second pass over an already-placed schematic must not touch anything.
func TestAutoplaceIsIdempotent(t *testing.T) {
	sch := mustParse(t, sixSymbolSchematic())

	Autoplace(sch)
	after := sch.Serialize()

	fields, flipped := Autoplace(sch)
	if fields != 0 || flipped != 0 {
		t.Errorf("second pass moved %d fields and flipped %d labels; expected none", fields, flipped)
	}
	if sch.Serialize() != after {
		t.Error("second pass changed the serialized schematic")
	}
}

// Same input, same output — no map iteration may leak into the result.
func TestAutoplaceIsDeterministic(t *testing.T) {
	src := sixSymbolSchematic()
	a := mustParse(t, src)
	b := mustParse(t, src)

	fa, la := Autoplace(a)
	fb, lb := Autoplace(b)
	if fa != fb || la != lb {
		t.Errorf("counts differ between runs: (%d,%d) vs (%d,%d)", fa, la, fb, lb)
	}
	if a.Serialize() != b.Serialize() {
		t.Error("two runs on identical input produced different output")
	}
}

// Global invariant over a mixed board.
func TestGlobalTextInvariantOnMixedBoard(t *testing.T) {
	sch := mustParse(t, sixSymbolSchematic())
	Autoplace(sch)
	assertNoTextCollisions(t, sch)

	// The power symbol has a hidden Reference, so its block is the single
	// Value line and must have been handled without touching the hidden field.
	if _, _, rot := propAnchor(t, sch, "#PWR01", "Value"); rot != 0 {
		t.Errorf("power symbol Value must stay horizontal, got rot=%v", rot)
	}
	for _, ref := range []string{"C1", "L1", "R1", "R2", "U1", "#PWR01"} {
		if got := sceneOverlap(sch, blockOf(t, sch, ref)); got > eps {
			t.Errorf("%s text overlaps the scene by %.2f mm^2", ref, got)
		}
	}
}

// The extra reach exists for crowded sheets only: a symbol with clear paper
// around it must keep its text at the conventional near distance, or every
// schematic would drift its labels outward for no reason.
func TestFieldsStayNearWhenThereIsRoom(t *testing.T) {
	body := box{50, 50, 52, 60}
	cands := fieldCandidates(body, 6, 4)
	if len(cands) != 8*len(fieldReach) {
		t.Fatalf("expected %d candidates, got %d", 8*len(fieldReach), len(cands))
	}

	// Nothing in the scene: the very first (nearest, right-hand) candidate wins.
	got := bestCandidate(body, 6, 4, nil, nil)
	if got != cands[0] {
		t.Errorf("with an empty scene the nearest conventional spot must win, got %+v want %+v", got, cands[0])
	}

	// Block the whole near ring: the pass must then reach further out rather
	// than settle for the least-bad overlap.
	near := []box{{40, 40, 62, 70}}
	got = bestCandidate(body, 6, 4, near, nil)
	if got.overlap(near[0]) > eps {
		return // found clear paper further out, which is the point
	}
	t.Errorf("with the near ring blocked the block should back off to clear paper, got %+v", got)
}

// A row of passives must not label itself inconsistently — three references to
// the left of their capacitor and the fourth above it, because that one had
// room, is the signature of a machine. When one side is clear for the whole
// row, every member uses it.
func TestRowOfPassivesSharesOneSide(t *testing.T) {
	// Four capacitors in a row, 12.7 mm apart, nothing else on the sheet.
	body := libSymbols +
		symbolDefault("Device:C", "C1", "100n", 50, 50, 0) +
		symbolDefault("Device:C", "C2", "100n", 62.7, 50, 0) +
		symbolDefault("Device:C", "C3", "100n", 75.4, 50, 0) +
		symbolDefault("Device:C", "C4", "10u", 88.1, 50, 0)
	sch := mustParse(t, wrapSch(body))

	syms := sexp.ReadSymbols(sch)
	rows := passiveRows(syms)
	if len(rows) != 1 || len(rows[0]) != 4 {
		t.Fatalf("expected one row of 4, got %v", rows)
	}

	Autoplace(sch)

	// Every block must sit on the same side of its own symbol.
	type offset struct {
		ref string
		dx  float64
		dy  float64
	}
	var offsets []offset
	for _, s := range sexp.ReadSymbols(sch) {
		b := blockOf(t, sch, s.Reference)
		offsets = append(offsets, offset{
			ref: s.Reference,
			dx:  (b.x1+b.x2)/2 - s.X,
			dy:  (b.y1+b.y2)/2 - s.Y,
		})
	}
	for _, o := range offsets[1:] {
		if math.Abs(o.dx-offsets[0].dx) > eps || math.Abs(o.dy-offsets[0].dy) > eps {
			t.Errorf("%s text sits at offset (%.2f, %.2f) but %s at (%.2f, %.2f) — a row must agree",
				o.ref, o.dx, o.dy, offsets[0].ref, offsets[0].dx, offsets[0].dy)
		}
	}
	assertNoTextCollisions(t, sch)
}
