package compile

import "testing"

func wantBBox(t *testing.T, b PlacedBlock, x1, y1, x2, y2 float64) {
	t.Helper()
	if !near(b.X1, x1) || !near(b.Y1, y1) || !near(b.X2, x2) || !near(b.Y2, y2) {
		t.Errorf("block %s bbox = (%.4f, %.4f, %.4f, %.4f), want (%.4f, %.4f, %.4f, %.4f)",
			b.Name, b.X1, b.Y1, b.X2, b.Y2, x1, y1, x2, y2)
	}
}

// TestArrangeTwoRows walks the arranger with hand-computed numbers.
//
// Content starts at (25.4, 25.4) = 10 cells; the margin is 4 cells = 10.16 mm.
//
// Row 1, block "a" (template, extent 0,0..30,18): translation (25.4, 25.4) —
// already an integer number of cells — bbox (25.4, 25.4)-(55.4, 43.4).
// Cursor moves to 55.4 + 10.16 = 65.56.
//
// Row 1, block "b" (a lone capacitor, local bbox -1.27,-3.81..1.27,3.81):
// desired tx = 65.56 + 1.27 = 66.83; the anchor pin sits at local (0, -3.81),
// so tx snaps up to ceil(66.83/2.54)*2.54 = 68.58. Desired ty = 25.4 + 3.81 =
// 29.21, and 29.21 - 3.81 = 25.4 is already on grid, so ty stays. bbox
// (67.31, 25.4)-(69.85, 33.02). Row bottom = max(43.4, 33.02) = 43.4.
//
// Row 2 top = 43.4 + 10.16 = 53.56. Block "c" (template, extent -5,-3..10,12):
// desired (30.4, 56.56), snapped up to the cell grid → (30.48, 58.42), bbox
// (25.48, 55.42)-(40.48, 70.42).
func TestArrangeTwoRows(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{
		Blocks: []Block{
			{Name: "a", Template: "tplA"},
			{Name: "b", Symbols: []Symbol{{Ref: "C1", Lib: libC}}},
			{Name: "c", Template: "tplB"},
		},
		Arrange: [][]string{{"a", "b"}, {"c"}},
	}
	l := mustResolve(t, d, sg, tg)

	a := blockOf(t, l, "a")
	b := blockOf(t, l, "b")
	c := blockOf(t, l, "c")

	wantBBox(t, a, 25.4, 25.4, 55.4, 43.4)
	wantBBox(t, b, 67.31, 25.4, 69.85, 33.02)
	wantBBox(t, c, 25.48, 55.42, 40.48, 70.42)

	if !near(a.OriginX, 25.4) || !near(a.OriginY, 25.4) {
		t.Errorf("a origin = (%.4f, %.4f), want (25.4000, 25.4000)", a.OriginX, a.OriginY)
	}
	if !near(c.OriginX, 30.48) || !near(c.OriginY, 58.42) {
		t.Errorf("c origin = (%.4f, %.4f), want (30.4800, 58.4200)", c.OriginX, c.OriginY)
	}
	wantOrigin(t, l, "C1", 68.58, 29.21)

	const margin = BlockMarginCells * Cell
	if b.X1 < a.X2+margin-1e-6 {
		t.Errorf("horizontal margin: b.X1 = %.4f, want >= %.4f", b.X1, a.X2+margin)
	}
	if c.Y1 < a.Y2+margin-1e-6 {
		t.Errorf("vertical margin: c.Y1 = %.4f, want >= %.4f", c.Y1, a.Y2+margin)
	}
	assertOnGrid(t, l, sg)
}

// TestArrangeUnlistedBlocksOwnRows: blocks missing from arrange get one row
// each at the end, in declaration order.
//
// "a" occupies 25.4..43.4 in Y; row 2 top = 53.56, snapped up to 55.88;
// "b" ends at 73.88, row 3 top = 84.04, snapped up to 86.36.
func TestArrangeUnlistedBlocksOwnRows(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{
		Blocks: []Block{
			{Name: "a", Template: "tplA"},
			{Name: "b", Template: "tplA"},
			{Name: "c", Template: "tplA"},
		},
		Arrange: [][]string{{"a"}},
	}
	l := mustResolve(t, d, sg, tg)

	wantBBox(t, blockOf(t, l, "a"), 25.4, 25.4, 55.4, 43.4)
	wantBBox(t, blockOf(t, l, "b"), 25.4, 55.88, 55.4, 73.88)
	wantBBox(t, blockOf(t, l, "c"), 25.4, 86.36, 55.4, 104.36)
}

// TestArrangeIgnoresUnknownAndDuplicateNames: the parser owns format
// validation, so the arranger must not double-place or crash.
func TestArrangeIgnoresUnknownAndDuplicateNames(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{
		Blocks: []Block{
			{Name: "a", Template: "tplA"},
			{Name: "b", Template: "tplA"},
		},
		Arrange: [][]string{{"a", "ghost", "a"}, {"nope"}, {"b"}},
	}
	l := mustResolve(t, d, sg, tg)
	if len(l.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(l.Blocks))
	}
	// "a" alone in row 1, "b" in the next non-empty row.
	wantBBox(t, blockOf(t, l, "a"), 25.4, 25.4, 55.4, 43.4)
	wantBBox(t, blockOf(t, l, "b"), 25.4, 55.88, 55.4, 73.88)
}

// TestArrangeTemplateTranslationIsWholeCells: templates carry baked geometry,
// so their translation must be an exact number of cells even when their extent
// is not.
func TestArrangeTemplateTranslationIsWholeCells(t *testing.T) {
	sg, tg := newFakes()
	d := &Design{
		Blocks: []Block{
			{Name: "odd", Template: "tplA"},
			{Name: "gap", Symbols: []Symbol{{Ref: "C1", Lib: libC}}},
			{Name: "odd2", Template: "tplB"},
		},
		Arrange: [][]string{{"odd", "gap", "odd2"}},
	}
	l := mustResolve(t, d, sg, tg)
	for _, name := range []string{"odd", "odd2"} {
		b := blockOf(t, l, name)
		if !isCellMultiple(b.OriginX) || !isCellMultiple(b.OriginY) {
			t.Errorf("template block %s origin (%.6f, %.6f) is not a whole number of cells",
				name, b.OriginX, b.OriginY)
		}
	}
	assertOnGrid(t, l, sg)
}

// TestArrangeEmptyDesign: no blocks, no panic, empty layout.
func TestArrangeEmptyDesign(t *testing.T) {
	sg, tg := newFakes()
	l := mustResolve(t, &Design{}, sg, tg)
	if len(l.Blocks) != 0 {
		t.Fatalf("got %d blocks, want 0", len(l.Blocks))
	}
}
