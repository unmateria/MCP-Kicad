package pcbgen

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// A trimmed kicadsexpr netlist with the shape `kicad-cli sch export netlist`
// writes (measured on KiCad 10.0.2).
const testNetlist = `(export (version "E")
	(components
		(comp (ref "R1") (value "10k") (footprint "Lib:R")
			(property (name "Sheetname") (value ""))
			(property (name "Sheetfile") (value "demo.kicad_sch"))
			(sheetpath (names "/") (tstamps "/"))
			(tstamps "aaaaaaaa-0000-0000-0000-000000000001"))
		(comp (ref "U1") (value "NE5532") (footprint "Lib:DIP8")
			(property (name "Sheetfile") (value "demo.kicad_sch"))
			(sheetpath (names "/") (tstamps "/"))
			(tstamps "bbbbbbbb-0000-0000-0000-000000000001" "bbbbbbbb-0000-0000-0000-000000000002")))
	(nets
		(net (code "1") (name "/OUT") (class "Default")
			(node (ref "R1") (pin "1") (pintype "passive"))
			(node (ref "U1") (pin "1") (pinfunction "OUT") (pintype "output")))
		(net (code "2") (name "GND") (class "Default")
			(node (ref "R1") (pin "2") (pintype "passive")))))`

const testR = `(footprint "R"
	(version 20241229)
	(generator "pcbnew")
	(layer "F.Cu")
	(property "Reference" "REF**" (at 0 -2 0) (layer "F.SilkS"))
	(property "Value" "R" (at 0 2 0) (layer "F.Fab"))
	(attr through_hole)
	(fp_rect (start -1.5 -1) (end 9.1 1) (stroke (width 0.05) (type solid)) (layer "F.CrtYd"))
	(pad "1" thru_hole circle (at 0 0) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "2" thru_hole oval (at 7.62 0) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
)`

func mustFP(t *testing.T, id, text string) *Footprint {
	t.Helper()
	fp, err := LoadFootprint(id, text)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestParseNetlist(t *testing.T) {
	nl, err := ParseNetlist(testNetlist)
	if err != nil {
		t.Fatal(err)
	}
	if len(nl.Components) != 2 || len(nl.Nets) != 2 {
		t.Fatalf("got %d components, %d nets", len(nl.Components), len(nl.Nets))
	}
	u := nl.Components[1]
	if u.Path != "/bbbbbbbb-0000-0000-0000-000000000001" || len(u.UUIDs) != 2 || u.SheetFile != "demo.kicad_sch" {
		t.Errorf("U1 link wrong: %+v", u)
	}
	pn := nl.PadNets()["U1\x00"+"1"]
	if pn.Net != "/OUT" || pn.Function != "OUT" || pn.Type != "output" {
		t.Errorf("U1.1 = %+v", pn)
	}
}

func TestLoadFootprintCourtyard(t *testing.T) {
	fp := mustFP(t, "Lib:R", testR)
	if fp.Courtyard != (Box{-1.5, -1, 9.1, 1}) {
		t.Errorf("courtyard %+v", fp.Courtyard)
	}
	if fp.Pads["2"] != [2]float64{7.62, 0} {
		t.Errorf("pad 2 at %v", fp.Pads["2"])
	}
}

// What KiCad 10 needs on a board footprint, as measured on its own demos.
func TestInstantiate(t *testing.T) {
	nl, _ := ParseNetlist(testNetlist)
	fp := mustFP(t, "Lib:R", testR)
	node, err := fp.Instantiate(nl.Components[0], 110.49, 50.8, 0, nl.PadNets())
	if err != nil {
		t.Fatal(err)
	}
	out := sexp.WriteNode(node)
	for _, want := range []string{
		`(footprint "Lib:R"`,
		`(at 110.49 50.8)`,
		`(property "Reference" "R1"`,
		`(property "Value" "10k"`,
		`(path "/aaaaaaaa-0000-0000-0000-000000000001")`,
		`(sheetfile "demo.kicad_sch")`,
		`(net "/OUT")`,
		`(net "GND")`,
	} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Contains(out, "(version") || strings.Contains(out, "REF**") {
		t.Errorf("library leftovers in\n%s", out)
	}
}

// Many parts seeded on top of each other must come out disjoint, inside the
// outline, and the same every run.
func TestPlaceLegalAndDeterministic(t *testing.T) {
	run := func() ([]*Part, PlaceResult) {
		fp := mustFP(t, "Lib:R", testR)
		var parts []*Part
		var nets []Net
		for i := 0; i < 24; i++ {
			ref := fmt.Sprintf("R%d", i+1)
			parts = append(parts, &Part{Comp: Component{Ref: ref}, FP: fp, SeedX: float64(i % 3), SeedY: float64(i % 2)})
			if i > 0 {
				nets = append(nets, Net{Name: ref, Nodes: []Node{{Ref: fmt.Sprintf("R%d", i), Pin: "2"}, {Ref: ref, Pin: "1"}}})
			}
		}
		return parts, Place(parts, nets)
	}
	a, ra := run()
	b, rb := run()
	for i := range a {
		if a[i].X != b[i].X || a[i].Y != b[i].Y {
			t.Fatalf("placement not deterministic: %s at %v,%v then %v,%v", a[i].Comp.Ref, a[i].X, a[i].Y, b[i].X, b[i].Y)
		}
		for j := i + 1; j < len(a); j++ {
			if a[i].keepout().overlaps(a[j].keepout(), 0) {
				t.Errorf("%s overlaps %s", a[i].Comp.Ref, a[j].Comp.Ref)
			}
		}
		c := a[i].keepout()
		o := ra.Outline
		if c.X1 < o.X1+boardMargin-0.01 || c.Y1 < o.Y1+boardMargin-0.01 || c.X2 > o.X2-boardMargin+0.01 || c.Y2 > o.Y2-boardMargin+0.01 {
			t.Errorf("%s courtyard %+v too close to outline %+v", a[i].Comp.Ref, c, o)
		}
	}
	if ra != rb {
		t.Errorf("results differ: %+v vs %+v", ra, rb)
	}
}

const testDIP8 = `(footprint "DIP8"
	(layer "F.Cu")
	(property "Reference" "REF**" (at 3.81 -2.33 0) (layer "F.SilkS"))
	(fp_rect (start -1.1 -1.55) (end 8.7 9.15) (stroke (width 0.05) (type solid)) (layer "F.CrtYd"))
	(pad "1" thru_hole rect (at 0 0) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "2" thru_hole oval (at 0 2.54) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "3" thru_hole oval (at 0 5.08) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "4" thru_hole oval (at 0 7.62) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "5" thru_hole oval (at 7.62 7.62) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "6" thru_hole oval (at 7.62 5.08) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "7" thru_hole oval (at 7.62 2.54) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
	(pad "8" thru_hole oval (at 7.62 0) (size 1.6 1.6) (drill 0.8) (layers "*.Cu" "*.Mask"))
)`

const testHeader2 = `(footprint "H2"
	(layer "F.Cu")
	(property "Reference" "REF**" (at 0 -2.33 0) (layer "F.SilkS"))
	(fp_rect (start -1.8 -1.8) (end 1.8 4.35) (stroke (width 0.05) (type solid)) (layer "F.CrtYd"))
	(pad "1" thru_hole rect (at 0 0) (size 1.7 1.7) (drill 1) (layers "*.Cu" "*.Mask"))
	(pad "2" thru_hole oval (at 0 2.54) (size 1.7 1.7) (drill 1) (layers "*.Cu" "*.Mask"))
)`

// The decoupling capacitor ends up against its IC, its own VCC pad close to
// the IC's VCC pin, even when the schematic drew it far away; the input
// connector, drawn left of everything, ends up on the left edge.
func TestPlaceDecapAndEdge(t *testing.T) {
	ic := &Part{Comp: Component{Ref: "U1"}, FP: mustFP(t, "Lib:DIP8", testDIP8)}
	c := &Part{Comp: Component{Ref: "C1"}, FP: mustFP(t, "Lib:R", testR), SeedX: 60, SeedY: 40}
	r := &Part{Comp: Component{Ref: "R1"}, FP: mustFP(t, "Lib:R", testR), SeedX: 20}
	j := &Part{Comp: Component{Ref: "J1"}, FP: mustFP(t, "Lib:H2", testHeader2), SeedX: -40}
	nets := []Net{
		{Name: "VCC", Nodes: []Node{{Ref: "U1", Pin: "8", Type: "power_in"}, {Ref: "C1", Pin: "1"}, {Ref: "J1", Pin: "1"}}},
		{Name: "GND", Nodes: []Node{{Ref: "U1", Pin: "4", Type: "power_in"}, {Ref: "C1", Pin: "2"}, {Ref: "J1", Pin: "2"}}},
		{Name: "OUT", Nodes: []Node{{Ref: "U1", Pin: "3"}, {Ref: "R1", Pin: "1"}}},
	}
	parts := []*Part{ic, c, r, j}
	res := Place(parts, nets)
	if res.Decaps != 1 || res.Edge != 1 {
		t.Fatalf("decaps %d, edge %d", res.Decaps, res.Edge)
	}
	vx, vy, _ := ic.pad("8")
	cx, cy, _ := c.pad("1")
	if d := math.Hypot(vx-cx, vy-cy); d > 6 {
		t.Errorf("C1's VCC pad is %.1f mm from U1.8", d)
	}
	for _, p := range parts {
		if p != j && p.keepout().X1 < j.keepout().X2 {
			t.Errorf("%s reaches left of the connector J1", p.Comp.Ref)
		}
	}
	if k := j.localKeepout(); k.H() < k.W() {
		t.Errorf("J1 lies across the left edge instead of along it (rot %d)", j.Rot)
	}
}
