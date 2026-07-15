package tools

import (
	"fmt"
	"math"

	"mcp-kicad/internal/sexp"
)

// Sheet-normalization constants. KiCad landscape sheet sizes in mm and the
// clearance kept from every paper edge. The title block occupies roughly the
// bottom-right 180×60 mm of the sheet; content is nudged off it when that is
// possible without violating the margins.
const (
	sheetMarginMM = 12.7
	sheetGridStep = 2.54
	titleBlockWMM = 180.0
	titleBlockHMM = 60.0
	sheetFitEpsMM = 0.01
)

type paperDef struct{ w, h float64 }

var paperSizes = map[string]paperDef{
	"A4": {297, 210},
	"A3": {420, 297},
}

// fitToSheet rigidly translates the whole schematic content by one
// grid-aligned delta so it sits inside the paper's usable area (≥ 12.7 mm
// from all four edges). When the content cannot fit on A4 the paper is
// upgraded to A3; when it cannot fit even there, content is aligned to the
// top-left margin and the overflow is reported. After fitting, if the content
// bbox overlaps the title-block corner and shifting up or left can avoid it
// within the margins, that shift is added.
//
// Pure translation: relative geometry, connectivity and all gate invariants
// are untouched. Returns a human-readable note ("" when nothing was done).
func fitToSheet(sch *sexp.Schematic) string {
	minX, minY, maxX, maxY, ok := sexp.ContentBBox(sch)
	if !ok {
		return ""
	}
	paper := sch.PaperSize()
	if _, known := paperSizes[paper]; !known {
		paper = "A4"
	}
	p := paperSizes[paper]
	width, height := maxX-minX, maxY-minY

	note := ""
	overflow := false
	if width > p.w-2*sheetMarginMM || height > p.h-2*sheetMarginMM {
		if paper == "A4" {
			// A3 is strictly larger, so upgrade even when the content will
			// still overflow — it clips less than staying on A4.
			sch.SetPaper("A3")
			paper, p = "A3", paperSizes["A3"]
			note = "sheet: content exceeds A4 usable area — paper changed to A3. "
		}
		if width > p.w-2*sheetMarginMM || height > p.h-2*sheetMarginMM {
			overflow = true
			note += fmt.Sprintf("sheet: content (%.0f×%.0f mm) does not fit %s usable area even after resize — aligned to top-left margin. ",
				width, height, paper)
		}
	}

	ceilStep := func(v float64) float64 {
		return math.Ceil((v-sheetFitEpsMM)/sheetGridStep) * sheetGridStep
	}

	var dx, dy float64
	if overflow {
		// Best effort: pin the top-left of the content at the margin corner.
		dx = ceilStep(sheetMarginMM - minX)
		dy = ceilStep(sheetMarginMM - minY)
	} else {
		if minX < sheetMarginMM-sheetFitEpsMM {
			dx = ceilStep(sheetMarginMM - minX)
		} else if maxX > p.w-sheetMarginMM+sheetFitEpsMM {
			dx = -ceilStep(maxX - (p.w - sheetMarginMM))
		}
		if minY < sheetMarginMM-sheetFitEpsMM {
			dy = ceilStep(sheetMarginMM - minY)
		} else if maxY > p.h-sheetMarginMM+sheetFitEpsMM {
			dy = -ceilStep(maxY - (p.h - sheetMarginMM))
		}

		// Title-block avoidance (best effort): if the fitted bbox pokes into
		// the bottom-right title-block rect, shift up or left — whichever
		// needs the smaller move — provided the opposite margin still holds.
		bMinX, bMinY := minX+dx, minY+dy
		bMaxX, bMaxY := maxX+dx, maxY+dy
		tbX, tbY := p.w-titleBlockWMM, p.h-titleBlockHMM
		if bMaxX > tbX+sheetFitEpsMM && bMaxY > tbY+sheetFitEpsMM {
			upNeed := ceilStep(bMaxY - tbY)
			leftNeed := ceilStep(bMaxX - tbX)
			upOK := bMinY-upNeed >= sheetMarginMM-sheetFitEpsMM
			leftOK := bMinX-leftNeed >= sheetMarginMM-sheetFitEpsMM
			switch {
			case upOK && (!leftOK || upNeed <= leftNeed):
				dy -= upNeed
			case leftOK:
				dx -= leftNeed
			default:
				note += "sheet: content overlaps the title-block corner and cannot be shifted clear. "
			}
		}
	}

	if dx == 0 && dy == 0 {
		if note == "" {
			return ""
		}
		return note
	}
	sexp.TranslateContent(sch, dx, dy)
	return note + fmt.Sprintf("sheet: content translated by (%.2f, %.2f) mm to fit %s with %.1f mm margins.",
		dx, dy, paper, sheetMarginMM)
}
