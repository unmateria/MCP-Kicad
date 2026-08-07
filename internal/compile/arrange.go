package compile

import "math"

// contentBaseCells is where the arranged content starts, in grid cells from
// the sheet origin. Fitting the finished content to the chosen sheet (margins,
// title block, A4→A3 upgrade) is a later stage; this only needs to be a sane
// on-grid starting corner.
const contentBaseCells = 10

// arrangeBlocks gives every locally resolved block its final rigid
// translation: rows top→bottom, blocks left→right inside a row, at least
// BlockMarginCells cells of clearance between bounding boxes horizontally
// within a row and vertically between rows. Block tops in a row are aligned,
// up to the sub-cell nudge that the grid invariant may impose.
//
// The returned slice is indexed by block declaration order.
func arrangeBlocks(d *Design, locals []*localBlock) []PlacedBlock {
	placed := make([]PlacedBlock, len(locals))
	rowTop := float64(contentBaseCells * Cell)
	for _, row := range blockRows(d) {
		cursorX := float64(contentBaseCells * Cell)
		rowBottom := rowTop
		for _, idx := range row {
			lb := locals[idx]
			tx, ty := snapTranslation(lb, cursorX-lb.x1, rowTop-lb.y1)
			pb := lb.at(tx, ty)
			placed[idx] = pb
			cursorX = pb.X2 + BlockMarginCells*Cell
			if pb.Y2 > rowBottom {
				rowBottom = pb.Y2
			}
		}
		rowTop = rowBottom + BlockMarginCells*Cell
	}
	return placed
}

// blockRows turns d.Arrange into rows of block indices. Names that do not
// match a block, and blocks listed twice, are ignored — the parser validates
// the source. Every block not mentioned in d.Arrange gets its own row at the
// end, in declaration order.
func blockRows(d *Design) [][]int {
	byName := make(map[string]int, len(d.Blocks))
	for i, b := range d.Blocks {
		if _, dup := byName[b.Name]; !dup {
			byName[b.Name] = i
		}
	}
	used := make([]bool, len(d.Blocks))
	var rows [][]int
	for _, names := range d.Arrange {
		var row []int
		for _, n := range names {
			i, ok := byName[n]
			if !ok || used[i] {
				continue
			}
			used[i] = true
			row = append(row, i)
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	for i := range d.Blocks {
		if !used[i] {
			rows = append(rows, []int{i})
		}
	}
	return rows
}

// snapTranslation adjusts a desired translation so that every pin of the block
// lands on an exact multiple of Cell.
//
// Inside an explicit block all pins are already separated from each other by
// multiples of Cell (anchors are expressed in cells, and library pins sit on
// the grid relative to their own symbol origin), so aligning a single
// reference pin aligns them all. The anchor's own origin may well be off-grid
// — a capacitor has its pins at ±3.81 mm — and the translation absorbs that
// residual. Template blocks carry baked, already aligned geometry, so their
// translation is simply an integer number of cells.
//
// The correction always rounds up, never back, so the arranger's minimum
// margins can never be eaten by the snap.
func snapTranslation(lb *localBlock, tx, ty float64) (float64, float64) {
	if lb.isTemplate || !lb.hasAnchor {
		return ceilToCell(tx), ceilToCell(ty)
	}
	return ceilToCell(tx+lb.anchorX) - lb.anchorX, ceilToCell(ty+lb.anchorY) - lb.anchorY
}

func ceilToCell(v float64) float64 {
	return math.Ceil(v/Cell-gridEps) * Cell
}

// at applies a rigid translation to a locally resolved block.
func (lb *localBlock) at(tx, ty float64) PlacedBlock {
	pb := PlacedBlock{
		Name:    lb.name,
		OriginX: tx,
		OriginY: ty,
		X1:      lb.x1 + tx,
		Y1:      lb.y1 + ty,
		X2:      lb.x2 + tx,
		Y2:      lb.y2 + ty,
	}
	if len(lb.symbols) > 0 {
		pb.Symbols = make([]PlacedSymbol, len(lb.symbols))
		for i, s := range lb.symbols {
			s.X += tx
			s.Y += ty
			pb.Symbols[i] = s
		}
	}
	return pb
}
