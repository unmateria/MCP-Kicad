package tools

import (
	"fmt"
	"strconv"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/place2/templates"
	"mcp-kicad/internal/sexp"
)

// newEmptySchematic returns a fresh in-memory schematic with the same minimal
// skeleton create_schematic writes to disk.
func newEmptySchematic() (*sexp.Schematic, error) {
	content := fmt.Sprintf(`(kicad_sch (version 20231120) (generator "eeschema") (generator_version "9.0") (paper "A4")
  (uuid "%s")
  (lib_symbols)
  (sheet_instances
    (path "/" (page "1"))))
`, sexp.NewUUID())
	return sexp.ParseSchematic(content)
}

// libGeom implements compile.SymbolGeom by instantiating each library symbol
// once, at the origin with rotation 0, inside a scratch schematic and reading
// the pin geometry back through sexp.ReadSymbols — the exact machinery used
// when the symbol is placed for real, so the resolver's view can never drift
// from production placement.
type libGeom struct {
	env     *Env
	scratch *sexp.Schematic
	cache   map[string]sexp.SchematicSymbol
	n       int
}

func (e *Env) newLibGeom() (*libGeom, error) {
	sch, err := newEmptySchematic()
	if err != nil {
		return nil, err
	}
	return &libGeom{env: e, scratch: sch, cache: map[string]sexp.SchematicSymbol{}}, nil
}

func (g *libGeom) instance(libID string, unit int) (sexp.SchematicSymbol, error) {
	if unit < 1 {
		unit = 1
	}
	key := fmt.Sprintf("%s#%d", libID, unit)
	if sym, ok := g.cache[key]; ok {
		return sym, nil
	}
	if err := g.env.embedLibSymbol(g.scratch, libID); err != nil {
		return sexp.SchematicSymbol{}, fmt.Errorf("symbol %s: %w", libID, err)
	}
	g.n++
	ref := fmt.Sprintf("XGEOM%d", g.n)
	pinNums := extractPinNumbers(g.scratch, libID, unit)
	libDef := g.scratch.FindLibDef(libID)
	g.scratch.AddSymbol(sexp.NewSymbolInstance(libID, ref, "", "",
		0, 0, 0, unit, pinNums, g.scratch.UUID(), false, false, libDef))
	for _, sym := range sexp.ReadSymbols(g.scratch) {
		if sym.Reference == ref {
			g.cache[key] = sym
			return sym, nil
		}
	}
	return sexp.SchematicSymbol{}, fmt.Errorf("geometry probe for %s unit %d produced no readable instance", libID, unit)
}

func (g *libGeom) PinOffset(libID string, unit int, pin string) (float64, float64, error) {
	sym, err := g.instance(libID, unit)
	if err != nil {
		return 0, 0, err
	}
	best := -1
	for i, p := range sym.Pins {
		if p.Number != pin && p.Name != pin {
			continue
		}
		if best == -1 || lessPinNumber(p.Number, sym.Pins[best].Number) {
			best = i
		}
	}
	if best == -1 {
		return 0, 0, fmt.Errorf("symbol %s has no pin %q", libID, pin)
	}
	return sym.Pins[best].X, sym.Pins[best].Y, nil
}

func (g *libGeom) Pins(libID string, unit int) ([]string, error) {
	sym, err := g.instance(libID, unit)
	if err != nil {
		return nil, err
	}
	nums := make([]string, 0, len(sym.Pins))
	for _, p := range sym.Pins {
		nums = append(nums, p.Number)
	}
	sortPinNumbers(nums)
	return nums, nil
}

func (g *libGeom) Body(libID string, unit int) (float64, float64, float64, float64, error) {
	sym, err := g.instance(libID, unit)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	x1, y1, x2, y2 := metrics.BodyBBox(sym)
	return x1, y1, x2, y2, nil
}

// lessPinNumber orders pin numbers numerically when both parse as integers,
// lexicographically otherwise (BGA-style "A1" pins).
func lessPinNumber(a, b string) bool {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	if errA == nil && errB == nil {
		return na < nb
	}
	return a < b
}

func sortPinNumbers(nums []string) {
	for i := 1; i < len(nums); i++ {
		for j := i; j > 0 && lessPinNumber(nums[j], nums[j-1]); j-- {
			nums[j], nums[j-1] = nums[j-1], nums[j]
		}
	}
}

// tmplGeom implements compile.TemplateGeom over the baked template library.
type tmplGeom struct{}

func (tmplGeom) Extent(name string) (float64, float64, float64, float64, error) {
	tpl, err := templates.Get(name)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	// Component origins are padded to approximate their bodies; wires and
	// junctions are exact; labels get a pad for their text extent.
	const pad = 2 * compile.Cell
	var x1, y1, x2, y2 float64
	first := true
	add := func(x, y, p float64) {
		if first {
			x1, y1, x2, y2 = x-p, y-p, x+p, y+p
			first = false
			return
		}
		if x-p < x1 {
			x1 = x - p
		}
		if y-p < y1 {
			y1 = y - p
		}
		if x+p > x2 {
			x2 = x + p
		}
		if y+p > y2 {
			y2 = y + p
		}
	}
	for _, c := range tpl.Components {
		add(c.RelX, c.RelY, pad)
	}
	for _, w := range tpl.Wires {
		add(w.X1, w.Y1, 0)
		add(w.X2, w.Y2, 0)
	}
	for _, j := range tpl.Junctions {
		add(j.X, j.Y, 0)
	}
	for _, l := range tpl.Labels {
		add(l.X, l.Y, pad)
	}
	if first {
		return 0, 0, 0, 0, fmt.Errorf("template %s has no geometry", name)
	}
	return x1, y1, x2, y2, nil
}
