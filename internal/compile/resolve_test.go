package compile

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fakes. Geometry is invented but shaped exactly like real KiCad symbols:
// capacitor pins sit off the grid relative to their own origin (±3.81 mm),
// IC pin tips stick out of the body, and stacked pin names repeat.
// ---------------------------------------------------------------------------

const (
	libC       = "Device:C"
	libIC      = "Test:IC"
	libCrystal = "Device:Crystal"
	libMCU     = "MCU_Microchip_ATmega:ATmega328-A"
)

type fakePin struct {
	num, name string
	dx, dy    float64
}

type fakePart struct {
	pins           []fakePin
	x1, y1, x2, y2 float64 // body
}

type fakeGeom struct{ parts map[string]fakePart }

func (f fakeGeom) part(libID string) (fakePart, error) {
	p, ok := f.parts[libID]
	if !ok {
		return fakePart{}, fmt.Errorf("unknown symbol %q", libID)
	}
	return p, nil
}

// PinOffset resolves by pin number first, then by pin name; a name shared by
// several stacked pins resolves to the lowest pin number, like the real
// geometry layer.
func (f fakeGeom) PinOffset(libID, pin string) (float64, float64, error) {
	p, err := f.part(libID)
	if err != nil {
		return 0, 0, err
	}
	for _, q := range p.pins {
		if q.num == pin {
			return q.dx, q.dy, nil
		}
	}
	best := -1
	for i, q := range p.pins {
		if q.name != pin {
			continue
		}
		if best < 0 || pinNumLess(q.num, p.pins[best].num) {
			best = i
		}
	}
	if best >= 0 {
		return p.pins[best].dx, p.pins[best].dy, nil
	}
	return 0, 0, fmt.Errorf("symbol %q has no pin %q", libID, pin)
}

func (f fakeGeom) Pins(libID string) ([]string, error) {
	p, err := f.part(libID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(p.pins))
	for i, q := range p.pins {
		out[i] = q.num
	}
	return out, nil
}

func (f fakeGeom) Body(libID string) (float64, float64, float64, float64, error) {
	p, err := f.part(libID)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return p.x1, p.y1, p.x2, p.y2, nil
}

func pinNumLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}

type fakeTpl struct{ ext map[string][4]float64 }

func (f fakeTpl) Extent(name string) (float64, float64, float64, float64, error) {
	e, ok := f.ext[name]
	if !ok {
		return 0, 0, 0, 0, fmt.Errorf("unknown template %q", name)
	}
	return e[0], e[1], e[2], e[3], nil
}

func newFakes() (fakeGeom, fakeTpl) {
	sg := fakeGeom{parts: map[string]fakePart{
		// Capacitor: pins 3.81 mm above and below the origin — off grid with
		// respect to the origin, on grid with respect to each other (7.62 mm).
		libC: {
			pins: []fakePin{{"1", "1", 0, -3.81}, {"2", "2", 0, 3.81}},
			x1:   -1.27, y1: -2.54, x2: 1.27, y2: 2.54,
		},
		// Crystal: horizontal, pins ±3.81 mm from the origin.
		libCrystal: {
			pins: []fakePin{{"1", "1", -3.81, 0}, {"2", "2", 3.81, 0}},
			x1:   -2.54, y1: -2.54, x2: 2.54, y2: 2.54,
		},
		// IC: origin at the body's top-left corner, pin tips 2.54 mm outside
		// the body on both sides, pin 5 stacked on the name of pin 1.
		libIC: {
			pins: []fakePin{
				{"1", "VCC", -2.54, 2.54},
				{"2", "GND", -2.54, 5.08},
				{"3", "SDA", 27.94, 2.54},
				{"4", "SCL", 27.94, 5.08},
				{"5", "VCC", -2.54, 7.62},
			},
			x1: 0, y1: 0, x2: 25.4, y2: 50.8,
		},
		libMCU: {
			pins: []fakePin{
				{"3", "GND", -2.54, 2.54},
				{"4", "VCC", -2.54, 5.08},
				{"7", "XTAL1", -2.54, 25.4},
				{"8", "XTAL2", -2.54, 27.94},
				{"18", "AVCC", -2.54, 12.7},
				{"27", "SDA", 33.02, 5.08},
				{"28", "SCL", 33.02, 7.62},
			},
			x1: 0, y1: 0, x2: 30.48, y2: 60.96,
		},
	}}
	tg := fakeTpl{ext: map[string][4]float64{
		"tplA":                     {0, 0, 30.0, 18.0},
		"tplB":                     {-5.0, -3.0, 10.0, 12.0},
		"voltage_regulator_linear": {-7.62, -5.08, 45.72, 40.64},
		"i2c_pullups":              {0, 0, 20.32, 25.4},
	}}
	return sg, tg
}

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-6 }

func mustResolve(t *testing.T, d *Design, sg SymbolGeom, tg TemplateGeom) *Layout {
	t.Helper()
	l, err := Resolve(d, sg, tg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return l
}

func symbolOf(t *testing.T, l *Layout, ref string) PlacedSymbol {
	t.Helper()
	for _, b := range l.Blocks {
		for _, s := range b.Symbols {
			if s.Ref == ref {
				return s
			}
		}
	}
	t.Fatalf("symbol %s not found in layout", ref)
	return PlacedSymbol{}
}

func blockOf(t *testing.T, l *Layout, name string) PlacedBlock {
	t.Helper()
	for _, b := range l.Blocks {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("block %s not found in layout", name)
	return PlacedBlock{}
}

func wantOrigin(t *testing.T, l *Layout, ref string, x, y float64) {
	t.Helper()
	s := symbolOf(t, l, ref)
	if !near(s.X, x) || !near(s.Y, y) {
		t.Errorf("%s origin = (%.4f, %.4f), want (%.4f, %.4f)", ref, s.X, s.Y, x, y)
	}
}

func isCellMultiple(v float64) bool {
	r := v / Cell
	return math.Abs(r-math.Round(r)) <= 1e-6
}

// assertOnGrid walks every pin of every symbol of every block and checks the
// hard invariant: nothing may sit off the 2.54 mm grid. Template blocks carry
// no symbols, so their origin must itself be an exact multiple of Cell.
func assertOnGrid(t *testing.T, l *Layout, sg SymbolGeom) {
	t.Helper()
	for _, b := range l.Blocks {
		if len(b.Symbols) == 0 {
			if !isCellMultiple(b.OriginX) || !isCellMultiple(b.OriginY) {
				t.Errorf("template block %s origin off grid: (%.6f, %.6f)", b.Name, b.OriginX, b.OriginY)
			}
			continue
		}
		for _, s := range b.Symbols {
			pins, err := sg.Pins(s.LibID)
			if err != nil {
				t.Fatalf("Pins(%s): %v", s.LibID, err)
			}
			for _, pin := range pins {
				x, y, err := PinPos(sg, s, pin)
				if err != nil {
					t.Fatalf("PinPos(%s.%s): %v", s.Ref, pin, err)
				}
				if !isCellMultiple(x) || !isCellMultiple(y) {
					t.Errorf("block %s: pin %s.%s off grid: (%.6f, %.6f)", b.Name, s.Ref, pin, x, y)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestResolveAnchorChain checks a two-level anchor chain against positions
// computed by hand.
//
// Block frame: U1 at (0,0); U1.VCC resolves to pin 1 at (-2.54, 2.54).
// C1.1 sits 4 cells left of it → (-12.7, 2.54); C1's pin 1 offset is
// (0, -3.81) so C1's origin is (-12.7, 6.35).
// C2.1 sits 3 cells left of C1.1 → (-20.32, 2.54) → origin (-20.32, 6.35).
// Local bbox spans C2's body left edge (-21.59) to U1's right pins (27.94),
// and U1's body top (0) to its bottom (50.8).
//
// The block is not listed in arrange, so it gets its own row starting at
// (25.4, 25.4): tx = 25.4 + 21.59 = 46.99, then snapped up so the anchor pin
// U1.1 lands on the grid: ceil((46.99-2.54)/2.54)*2.54 + 2.54 = 48.26.
// ty = 25.4 - 0 = 25.4 already puts U1.1 at y = 27.94, on grid.
func TestResolveAnchorChain(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{Blocks: []Block{{Name: "mcu", Symbols: []Symbol{
		{Ref: "U1", Lib: libIC},
		{Ref: "C1", Lib: libC, Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 4}},
		{Ref: "C2", Lib: libC, Place: &Place{Pin: "1", At: "C1.1", Dir: "left", Cells: 3}},
	}}}}

	l := mustResolve(t, d, sg, tg)

	wantOrigin(t, l, "U1", 48.26, 25.4)
	wantOrigin(t, l, "C1", 35.56, 31.75)
	wantOrigin(t, l, "C2", 27.94, 31.75)

	b := blockOf(t, l, "mcu")
	if !near(b.X1, 26.67) || !near(b.Y1, 25.4) || !near(b.X2, 76.2) || !near(b.Y2, 76.2) {
		t.Errorf("block bbox = (%.4f, %.4f, %.4f, %.4f), want (26.6700, 25.4000, 76.2000, 76.2000)",
			b.X1, b.Y1, b.X2, b.Y2)
	}
	if !near(b.OriginX, 48.26) || !near(b.OriginY, 25.4) {
		t.Errorf("block origin = (%.4f, %.4f), want (48.2600, 25.4000)", b.OriginX, b.OriginY)
	}
	assertOnGrid(t, l, sg)
}

// TestResolveDirections checks the four placement directions: the placed pin
// must land exactly cells*2.54 mm away from the target pin, with Y growing
// downward.
func TestResolveDirections(t *testing.T) {
	sg, tg := newFakes()
	cases := []struct {
		at             string
		dir            string
		cells          int
		wantDX, wantDY float64
	}{
		{"U1.VCC", "left", 4, -10.16, 0},
		{"U1.SDA", "right", 2, 5.08, 0},
		{"U1.VCC", "up", 3, 0, -7.62},
		{"U1.VCC", "down", 3, 0, 7.62},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			d := &Design{Blocks: []Block{{Name: "b", Symbols: []Symbol{
				{Ref: "U1", Lib: libIC},
				{Ref: "C1", Lib: libC, Place: &Place{Pin: "1", At: c.at, Dir: c.dir, Cells: c.cells}},
			}}}}
			l := mustResolve(t, d, sg, tg)

			_, targetPin := splitPlaceAt(c.at)
			tx, ty, err := PinPos(sg, symbolOf(t, l, "U1"), targetPin)
			if err != nil {
				t.Fatal(err)
			}
			px, py, err := PinPos(sg, symbolOf(t, l, "C1"), "1")
			if err != nil {
				t.Fatal(err)
			}
			if !near(px-tx, c.wantDX) || !near(py-ty, c.wantDY) {
				t.Errorf("C1.1 - %s = (%.4f, %.4f), want (%.4f, %.4f)",
					c.at, px-tx, py-ty, c.wantDX, c.wantDY)
			}
			assertOnGrid(t, l, sg)
		})
	}
}

// TestResolveRotationTransform pins down the rotation convention replicated
// from internal/sexp/pins.go: KiCad rotation is counter-clockwise on screen,
// so an offset pointing up (negative Y) points left after 90°.
func TestResolveRotationTransform(t *testing.T) {
	cases := []struct {
		name         string
		dx, dy       float64
		rot          int
		mirror       bool
		wantX, wantY float64
	}{
		{"c pin1 rot0", 0, -3.81, 0, false, 0, -3.81},
		{"c pin1 rot90", 0, -3.81, 90, false, -3.81, 0},
		{"c pin1 rot180", 0, -3.81, 180, false, 0, 3.81},
		{"c pin1 rot270", 0, -3.81, 270, false, 3.81, 0},
		{"c pin2 rot90", 0, 3.81, 90, false, 3.81, 0},
		{"c pin2 rot270", 0, 3.81, 270, false, -3.81, 0},
		{"ic pin rot90", -2.54, 2.54, 90, false, 2.54, 2.54},
		{"ic pin rot180", -2.54, 2.54, 180, false, 2.54, -2.54},
		{"ic pin rot270", -2.54, 2.54, 270, false, -2.54, -2.54},
		{"ic pin mirror", -2.54, 2.54, 0, true, 2.54, 2.54},
		{"ic pin mirror rot90", -2.54, 2.54, 90, true, 2.54, -2.54},
	}
	for _, c := range cases {
		x, y := transformOffset(c.dx, c.dy, c.rot, c.mirror)
		if !near(x, c.wantX) || !near(y, c.wantY) {
			t.Errorf("%s: transformOffset(%g, %g, %d, %v) = (%.4f, %.4f), want (%.4f, %.4f)",
				c.name, c.dx, c.dy, c.rot, c.mirror, x, y, c.wantX, c.wantY)
		}
	}
}

// TestResolveRotatedSymbol places a capacitor rotated 90° and checks that the
// origin compensation uses the rotated pin offset.
func TestResolveRotatedSymbol(t *testing.T) {
	sg, tg := newFakes()
	rot := 90
	d := &Design{Blocks: []Block{{Name: "b", Symbols: []Symbol{
		{Ref: "U1", Lib: libIC},
		{Ref: "C1", Lib: libC, Rot: &rot, Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 4}},
	}}}}
	l := mustResolve(t, d, sg, tg)

	u1 := symbolOf(t, l, "U1")
	c1 := symbolOf(t, l, "C1")
	if c1.Rot != 90 {
		t.Errorf("C1 rot = %d, want 90", c1.Rot)
	}
	// C1.1 lands at U1.VCC + (-10.16, 0); at rot 90 pin 1 sits at (-3.81, 0)
	// from the origin, so the origin is 3.81 mm to the right of the pin.
	wantX := u1.X - 2.54 - 10.16 + 3.81
	wantY := u1.Y + 2.54
	if !near(c1.X, wantX) || !near(c1.Y, wantY) {
		t.Errorf("C1 origin = (%.4f, %.4f), want (%.4f, %.4f)", c1.X, c1.Y, wantX, wantY)
	}
	// Pin 2 of the rotated capacitor must still land on the grid.
	assertOnGrid(t, l, sg)
}

// TestResolveStackedPinName checks that a pin name shared by several pins is
// passed through to SymbolGeom, which resolves it to the lowest pin number.
func TestResolveStackedPinName(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{Blocks: []Block{{Name: "b", Symbols: []Symbol{
		{Ref: "U1", Lib: libIC},
		{Ref: "C1", Lib: libC, Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 4}},
	}}}}
	l := mustResolve(t, d, sg, tg)

	u1 := symbolOf(t, l, "U1")
	pos := func(pin string) (float64, float64) {
		t.Helper()
		x, y, err := PinPos(sg, u1, pin)
		if err != nil {
			t.Fatal(err)
		}
		return x, y
	}
	nameX, nameY := pos("VCC")
	lowX, lowY := pos("1")
	_, stackedY := pos("5")
	if !near(nameX, lowX) || !near(nameY, lowY) {
		t.Errorf("U1.VCC = (%.4f, %.4f), want pin 1 at (%.4f, %.4f)", nameX, nameY, lowX, lowY)
	}
	if near(nameY, stackedY) {
		t.Errorf("U1.VCC resolved to the stacked pin 5 (y=%.4f) instead of pin 1", stackedY)
	}

	px, _, err := PinPos(sg, symbolOf(t, l, "C1"), "1")
	if err != nil {
		t.Fatal(err)
	}
	if !near(px, nameX-10.16) {
		t.Errorf("C1.1 x = %.4f, want %.4f", px, nameX-10.16)
	}
}

// TestResolveIntraBlockOverlap: two capacitors hung off adjacent IC pins with
// the same dir/cells collide, and the error must name both refs and the block.
func TestResolveIntraBlockOverlap(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{Blocks: []Block{{Name: "mcu", Symbols: []Symbol{
		{Ref: "U1", Lib: libIC},
		{Ref: "C1", Lib: libC, Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 4}},
		{Ref: "C2", Lib: libC, Place: &Place{Pin: "1", At: "U1.GND", Dir: "left", Cells: 4}},
	}}}}

	_, err := Resolve(d, sg, tg)
	if err == nil {
		t.Fatal("expected an overlap error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"C1", "C2", "mcu"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// TestResolveTouchingBodiesAllowed: bodies that share an edge are not an
// overlap. C1's body spans y 3.81..8.89 and C2's spans 8.89..13.97.
func TestResolveTouchingBodiesAllowed(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{Blocks: []Block{{Name: "mcu", Symbols: []Symbol{
		{Ref: "U1", Lib: libIC},
		{Ref: "C1", Lib: libC, Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 4}},
		{Ref: "C2", Lib: libC, Place: &Place{Pin: "1", At: "U1.5", Dir: "left", Cells: 4}},
	}}}}

	l := mustResolve(t, d, sg, tg)
	c1 := symbolOf(t, l, "C1")
	c2 := symbolOf(t, l, "C2")
	if !near(c2.Y-c1.Y, 5.08) {
		t.Errorf("C2.Y - C1.Y = %.4f, want 5.0800 (bodies exactly touching)", c2.Y-c1.Y)
	}
	assertOnGrid(t, l, sg)
}

// TestResolveUnknownTarget: anchoring at a symbol that is not declared earlier
// in the same block is a geometric error.
func TestResolveUnknownTarget(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{Blocks: []Block{{Name: "b", Symbols: []Symbol{
		{Ref: "U1", Lib: libIC},
		{Ref: "C1", Lib: libC, Place: &Place{Pin: "1", At: "C2.1", Dir: "left", Cells: 4}},
		{Ref: "C2", Lib: libC, Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 8}},
	}}}}
	if _, err := Resolve(d, sg, tg); err == nil {
		t.Fatal("expected an error for a forward anchor reference, got nil")
	}
}

// TestResolveGridInvariant: mixed template and explicit blocks, rotated
// symbols, several rows — every pin of every symbol lands on the grid.
func TestResolveGridInvariant(t *testing.T) {
	sg, tg := newFakes()
	r90, r180, r270 := 90, 180, 270
	d := &Design{
		Blocks: []Block{
			{Name: "tpl", Template: "tplB"},
			{Name: "mixed", Symbols: []Symbol{
				{Ref: "U1", Lib: libIC},
				{Ref: "C1", Lib: libC, Rot: &r90, Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 4}},
				{Ref: "C2", Lib: libC, Rot: &r180, Place: &Place{Pin: "2", At: "U1.SCL", Dir: "right", Cells: 3}},
				{Ref: "C3", Lib: libC, Rot: &r270, Place: &Place{Pin: "1", At: "U1.5", Dir: "left", Cells: 7}},
				{Ref: "Y1", Lib: libCrystal, Place: &Place{Pin: "2", At: "U1.GND", Dir: "left", Cells: 12}},
			}},
			{Name: "solo", Symbols: []Symbol{{Ref: "C9", Lib: libC}}},
		},
		Arrange: [][]string{{"tpl", "mixed"}},
	}
	l := mustResolve(t, d, sg, tg)
	if len(l.Blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(l.Blocks))
	}
	assertOnGrid(t, l, sg)
}

// TestResolveDeterministic: same input, byte-identical output.
func TestResolveDeterministic(t *testing.T) {
	sg, tg := newFakes()
	build := func() *Design {
		return demoFullBoardDesign()
	}
	a := mustResolve(t, build(), sg, tg)
	b := mustResolve(t, build(), sg, tg)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Resolve is not deterministic:\n%#v\n%#v", a, b)
	}
}

// demoFullBoardDesign mirrors docs/compiler/demo_full_board.design.json. It is
// built in Go rather than parsed so this package's geometry tests never depend
// on the JSON parser.
func demoFullBoardDesign() *Design {
	return &Design{
		Version: 1,
		Project: "demo_full_board",
		Sheet:   "auto",
		Blocks: []Block{
			{
				Name:     "power",
				Template: "voltage_regulator_linear",
				Refs:     map[string]string{"REG": "U2", "C_IN_BYP": "C4"},
				Connect:  map[string]string{"VIN": "VIN", "VOUT": "+5V"},
			},
			{Name: "mcu", Symbols: []Symbol{
				{Ref: "U1", Lib: libMCU, Value: "ATmega328"},
				{Ref: "C1", Lib: libC, Value: "100n", Place: &Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 4}},
				{Ref: "C2", Lib: libC, Value: "100n", Place: &Place{Pin: "1", At: "U1.AVCC", Dir: "left", Cells: 4}},
				{Ref: "C3", Lib: libC, Value: "10u", Place: &Place{Pin: "1", At: "C1.1", Dir: "left", Cells: 3}},
				{Ref: "Y1", Lib: libCrystal, Value: "16MHz", Place: &Place{Pin: "1", At: "U1.7", Dir: "left", Cells: 5}},
				{Ref: "C8", Lib: libC, Value: "22p", Place: &Place{Pin: "1", At: "Y1.1", Dir: "down", Cells: 2}},
				{Ref: "C9", Lib: libC, Value: "22p", Place: &Place{Pin: "1", At: "Y1.2", Dir: "down", Cells: 2}},
			}},
			{
				Name:     "i2c",
				Template: "i2c_pullups",
				Refs:     map[string]string{"R_SDA": "R1", "R_SCL": "R2"},
				Connect:  map[string]string{"SDA": "SDA", "SCL": "SCL", "VCC": "+5V"},
			},
		},
		Arrange: [][]string{{"power"}, {"mcu", "i2c"}},
		Nets: map[string][]string{
			"+5V": {"U1.VCC", "U1.AVCC", "C1.1", "C2.1", "C3.1"},
			"GND": {"U1.GND", "C1.2", "C2.2", "C3.2", "C8.2", "C9.2"},
		},
		PowerNets: map[string]string{"+5V": "power:+5V", "GND": "power:GND"},
	}
}

// TestResolveDemoFullBoard runs the resolver over the real demo source
// (rebuilt in Go) and checks the structural guarantees rather than exact mm:
// declaration-ordered output, arrange rows honoured, margins respected, every
// anchor exactly where the source asked, everything on grid.
func TestResolveDemoFullBoard(t *testing.T) {
	sg, tg := newFakes()
	d := demoFullBoardDesign()
	l := mustResolve(t, d, sg, tg)

	if len(l.Blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(l.Blocks))
	}
	for i, want := range []string{"power", "mcu", "i2c"} {
		if l.Blocks[i].Name != want {
			t.Errorf("block %d is %q, want %q (declaration order)", i, l.Blocks[i].Name, want)
		}
	}

	power := blockOf(t, l, "power")
	mcu := blockOf(t, l, "mcu")
	i2c := blockOf(t, l, "i2c")

	const margin = BlockMarginCells * Cell
	if mcu.Y1 < power.Y2+margin-1e-6 {
		t.Errorf("row gap: mcu.Y1 = %.4f, want >= %.4f", mcu.Y1, power.Y2+margin)
	}
	if i2c.X1 < mcu.X2+margin-1e-6 {
		t.Errorf("column gap: i2c.X1 = %.4f, want >= %.4f", i2c.X1, mcu.X2+margin)
	}
	if !near(mcu.Y1, i2c.Y1) && math.Abs(mcu.Y1-i2c.Y1) > Cell {
		t.Errorf("row tops not aligned: mcu.Y1 = %.4f, i2c.Y1 = %.4f", mcu.Y1, i2c.Y1)
	}
	if len(power.Symbols) != 0 || len(i2c.Symbols) != 0 {
		t.Error("template blocks must carry no explicit symbols")
	}
	if len(mcu.Symbols) != 7 {
		t.Errorf("mcu has %d symbols, want 7", len(mcu.Symbols))
	}

	// Every anchor relationship declared in the source, verified in mm.
	u1 := symbolOf(t, l, "U1")
	checkAnchor(t, sg, u1, "VCC", symbolOf(t, l, "C1"), "1", -4*Cell, 0)
	checkAnchor(t, sg, u1, "AVCC", symbolOf(t, l, "C2"), "1", -4*Cell, 0)
	checkAnchor(t, sg, symbolOf(t, l, "C1"), "1", symbolOf(t, l, "C3"), "1", -3*Cell, 0)
	checkAnchor(t, sg, u1, "7", symbolOf(t, l, "Y1"), "1", -5*Cell, 0)
	checkAnchor(t, sg, symbolOf(t, l, "Y1"), "1", symbolOf(t, l, "C8"), "1", 0, 2*Cell)
	checkAnchor(t, sg, symbolOf(t, l, "Y1"), "2", symbolOf(t, l, "C9"), "1", 0, 2*Cell)

	assertOnGrid(t, l, sg)
}

func checkAnchor(t *testing.T, sg SymbolGeom, target PlacedSymbol, targetPin string, own PlacedSymbol, ownPin string, wantDX, wantDY float64) {
	t.Helper()
	tx, ty, err := PinPos(sg, target, targetPin)
	if err != nil {
		t.Fatal(err)
	}
	ox, oy, err := PinPos(sg, own, ownPin)
	if err != nil {
		t.Fatal(err)
	}
	if !near(ox-tx, wantDX) || !near(oy-ty, wantDY) {
		t.Errorf("%s.%s - %s.%s = (%.4f, %.4f), want (%.4f, %.4f)",
			own.Ref, ownPin, target.Ref, targetPin, ox-tx, oy-ty, wantDX, wantDY)
	}
}

// TestResolveBodyBox sanity-checks the exported body helper against a rotation.
func TestResolveBodyBox(t *testing.T) {
	sg, _ := newFakes()
	s := PlacedSymbol{Ref: "C1", LibID: libC, X: 100, Y: 50, Rot: 90}
	x1, y1, x2, y2, err := BodyBox(sg, s)
	if err != nil {
		t.Fatal(err)
	}
	// (-1.27,-2.54)-(1.27,2.54) rotated 90° becomes (-2.54,-1.27)-(2.54,1.27).
	if !near(x1, 97.46) || !near(y1, 48.73) || !near(x2, 102.54) || !near(y2, 51.27) {
		t.Errorf("BodyBox = (%.4f, %.4f, %.4f, %.4f), want (97.4600, 48.7300, 102.5400, 51.2700)", x1, y1, x2, y2)
	}
}
