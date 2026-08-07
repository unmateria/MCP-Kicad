package compile

import (
	"fmt"
	"math"
	"strings"
)

// gridEps is the tolerance for on-grid and box-interior comparisons, in mm.
// Well below any meaningful schematic geometry, well above float64 noise.
const gridEps = 1e-6

// Resolve turns a validated Design into absolute sheet geometry: every block
// placed by the arranger, every explicit symbol at its final position.
//
// The Design is assumed to have passed the parser's format validation; the
// errors returned here are geometric only — unknown symbols or pins, bad
// placement targets, and symbols overlapping inside a block.
//
// Layout.Blocks is returned in declaration order, so Layout.Blocks[i] always
// corresponds to d.Blocks[i]; the arrangement is expressed in the coordinates,
// not in the slice order.
func Resolve(d *Design, sg SymbolGeom, tg TemplateGeom) (*Layout, error) {
	if d == nil {
		return nil, fmt.Errorf("resolve: nil design")
	}
	locals := make([]*localBlock, len(d.Blocks))
	for i := range d.Blocks {
		lb, err := resolveBlock(&d.Blocks[i], sg, tg)
		if err != nil {
			return nil, err
		}
		locals[i] = lb
	}
	return &Layout{Blocks: arrangeBlocks(d, locals)}, nil
}

// PinPos returns the absolute position of one pin of an already placed symbol.
// The pin may be given by number or by name.
func PinPos(sg SymbolGeom, s PlacedSymbol, pin string) (x, y float64, err error) {
	dx, dy, err := offsetOf(sg, s.LibID, pin, s.Rot, s.Mirror)
	if err != nil {
		return 0, 0, err
	}
	return s.X + dx, s.Y + dy, nil
}

// BodyBox returns the absolute body bounding box of an already placed symbol,
// excluding pin length.
func BodyBox(sg SymbolGeom, s PlacedSymbol) (x1, y1, x2, y2 float64, err error) {
	bx1, by1, bx2, by2, err := sg.Body(s.LibID)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	r := transformRect(bx1, by1, bx2, by2, s.Rot, s.Mirror)
	return r.x1 + s.X, r.y1 + s.Y, r.x2 + s.X, r.y2 + s.Y, nil
}

// localBlock is one block resolved in its own frame: the block origin is
// (0, 0) and everything below is relative to it. The arranger then applies a
// single rigid translation per block.
type localBlock struct {
	name           string
	isTemplate     bool
	symbols        []PlacedSymbol
	x1, y1, x2, y2 float64
	// anchorX/anchorY is the grid reference: the first pin of the block's
	// anchor symbol. Its residual is what the final translation must absorb
	// so that every pin lands on the 2.54 mm grid.
	anchorX, anchorY float64
	hasAnchor        bool
}

func resolveBlock(b *Block, sg SymbolGeom, tg TemplateGeom) (*localBlock, error) {
	if b.Template != "" {
		x1, y1, x2, y2, err := tg.Extent(b.Template)
		if err != nil {
			return nil, fmt.Errorf("block %q: template %q: %w", b.Name, b.Template, err)
		}
		return &localBlock{name: b.Name, isTemplate: true, x1: x1, y1: y1, x2: x2, y2: y2}, nil
	}
	return resolveExplicitBlock(b, sg)
}

// resolveExplicitBlock walks the anchor tree in declaration order. The first
// symbol sits at the block origin; each later symbol is positioned so that its
// own pin falls exactly Cells grid cells away, in direction Dir, from the
// target pin of an already placed symbol.
func resolveExplicitBlock(b *Block, sg SymbolGeom) (*localBlock, error) {
	lb := &localBlock{name: b.Name}
	index := make(map[string]int, len(b.Symbols))
	bodies := make([]rect, 0, len(b.Symbols))
	var bounds rect
	haveBounds := false
	grow := func(x, y float64) {
		if !haveBounds {
			bounds = rect{x, y, x, y}
			haveBounds = true
			return
		}
		bounds = bounds.include(x, y)
	}

	for i := range b.Symbols {
		s := &b.Symbols[i]
		rot := 0
		if s.Rot != nil {
			rot = *s.Rot
		}
		ps := PlacedSymbol{Ref: s.Ref, LibID: s.Lib, Value: s.Value, Rot: rot, Mirror: s.Mirror}

		if s.Place != nil {
			targetRef, targetPin := splitPlaceAt(s.Place.At)
			ti, ok := index[targetRef]
			if !ok {
				return nil, fmt.Errorf("block %q: symbol %s places at %q, but %s is not a symbol declared earlier in this block",
					b.Name, s.Ref, s.Place.At, targetRef)
			}
			target := lb.symbols[ti]
			tdx, tdy, err := offsetOf(sg, target.LibID, targetPin, target.Rot, target.Mirror)
			if err != nil {
				return nil, fmt.Errorf("block %q: symbol %s: target %q: %w", b.Name, s.Ref, s.Place.At, err)
			}
			vx, vy, err := dirVector(s.Place.Dir, s.Place.Cells)
			if err != nil {
				return nil, fmt.Errorf("block %q: symbol %s: %w", b.Name, s.Ref, err)
			}
			odx, ody, err := offsetOf(sg, s.Lib, s.Place.Pin, rot, s.Mirror)
			if err != nil {
				return nil, fmt.Errorf("block %q: symbol %s: own pin %q: %w", b.Name, s.Ref, s.Place.Pin, err)
			}
			// own pin = target pin + direction vector; origin = own pin - own offset.
			ps.X = target.X + tdx + vx - odx
			ps.Y = target.Y + tdy + vy - ody
		}

		lb.symbols = append(lb.symbols, ps)
		index[s.Ref] = i

		bx1, by1, bx2, by2, err := sg.Body(s.Lib)
		if err != nil {
			return nil, fmt.Errorf("block %q: symbol %s: body of %q: %w", b.Name, s.Ref, s.Lib, err)
		}
		body := transformRect(bx1, by1, bx2, by2, rot, s.Mirror).translate(ps.X, ps.Y)
		bodies = append(bodies, body)
		grow(body.x1, body.y1)
		grow(body.x2, body.y2)

		pins, err := sg.Pins(s.Lib)
		if err != nil {
			return nil, fmt.Errorf("block %q: symbol %s: pins of %q: %w", b.Name, s.Ref, s.Lib, err)
		}
		for j, pin := range pins {
			dx, dy, err := offsetOf(sg, s.Lib, pin, rot, s.Mirror)
			if err != nil {
				return nil, fmt.Errorf("block %q: symbol %s: pin %q: %w", b.Name, s.Ref, pin, err)
			}
			px, py := ps.X+dx, ps.Y+dy
			grow(px, py)
			if j == 0 && !lb.hasAnchor {
				lb.anchorX, lb.anchorY = px, py
				lb.hasAnchor = true
			}
		}
	}

	for i := 0; i < len(bodies); i++ {
		for j := i + 1; j < len(bodies); j++ {
			if bodies[i].overlaps(bodies[j]) {
				return nil, fmt.Errorf("block %q: symbols %s and %s overlap; move one of them with a different dir/cells",
					b.Name, b.Symbols[i].Ref, b.Symbols[j].Ref)
			}
		}
	}

	if haveBounds {
		lb.x1, lb.y1, lb.x2, lb.y2 = bounds.x1, bounds.y1, bounds.x2, bounds.y2
	}
	return lb, nil
}

// transformOffset rotates and mirrors an offset expressed in schematic
// coordinates (Y grows downward) at rotation 0 without mirror, as
// SymbolGeom.PinOffset and SymbolGeom.Body return them.
//
// This convention MUST stay identical to internal/sexp/pins.go:transformPin,
// which is the code that will read back the generated schematic. That function
// works from the library's Y-up local frame:
//
//	schX = cx + lx*cos(r) - ly*sin(r)
//	schY = cy - lx*sin(r) - ly*cos(r)
//
// At rotation 0 that gives (schX-cx, schY-cy) = (lx, -ly), so our rotation-0
// schematic offset is (dx0, dy0) = (lx, -ly), i.e. lx = dx0 and ly = -dy0.
// Substituting:
//
//	dx = dx0*cos(r) + dy0*sin(r)
//	dy = -dx0*sin(r) + dy0*cos(r)
//
// which is exactly a counter-clockwise visual rotation in a Y-down frame, the
// way KiCad displays symbol rotation.
//
// Mirror is applied first, in the rotation-0 frame, and flips the X axis —
// KiCad's `(mirror y)`, the horizontal flip. sexp/pins.go does not handle
// mirror yet; when it grows support it must match this order (mirror, then
// rotate) and this axis.
func transformOffset(dx, dy float64, rot int, mirror bool) (float64, float64) {
	if mirror {
		dx = -dx
	}
	c, s := rotCosSin(rot)
	return dx*c + dy*s, -dx*s + dy*c
}

// rotCosSin returns cos/sin of a symbol rotation, exact for the four legal
// KiCad orientations so no float noise leaks into pin coordinates.
func rotCosSin(rot int) (cos, sin float64) {
	r := ((rot % 360) + 360) % 360
	switch r {
	case 0:
		return 1, 0
	case 90:
		return 0, 1
	case 180:
		return -1, 0
	case 270:
		return 0, -1
	}
	rad := float64(r) * math.Pi / 180
	return math.Cos(rad), math.Sin(rad)
}

// dirVector converts a placement direction into an offset vector. Y grows
// downward, so "up" is negative Y.
func dirVector(dir string, cells int) (dx, dy float64, err error) {
	d := float64(cells) * Cell
	switch dir {
	case "left":
		return -d, 0, nil
	case "right":
		return d, 0, nil
	case "up":
		return 0, -d, nil
	case "down":
		return 0, d, nil
	}
	return 0, 0, fmt.Errorf("unknown placement direction %q", dir)
}

func offsetOf(sg SymbolGeom, libID, pin string, rot int, mirror bool) (dx, dy float64, err error) {
	ox, oy, err := sg.PinOffset(libID, pin)
	if err != nil {
		return 0, 0, err
	}
	x, y := transformOffset(ox, oy, rot, mirror)
	return x, y, nil
}

// splitPlaceAt splits a Place.At target such as "U1.VCC" into ("U1", "VCC").
// References never contain a dot, so the first one separates them.
func splitPlaceAt(s string) (ref, pin string) {
	if i := strings.Index(s, "."); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// rect is an axis-aligned box in mm.
type rect struct{ x1, y1, x2, y2 float64 }

// transformRect rotates and mirrors a rotation-0 box. Rotations are multiples
// of 90°, so transforming two opposite corners and re-normalising is exact.
func transformRect(x1, y1, x2, y2 float64, rot int, mirror bool) rect {
	ax, ay := transformOffset(x1, y1, rot, mirror)
	bx, by := transformOffset(x2, y2, rot, mirror)
	return rect{math.Min(ax, bx), math.Min(ay, by), math.Max(ax, bx), math.Max(ay, by)}
}

func (r rect) translate(dx, dy float64) rect {
	return rect{r.x1 + dx, r.y1 + dy, r.x2 + dx, r.y2 + dy}
}

func (r rect) include(x, y float64) rect {
	return rect{math.Min(r.x1, x), math.Min(r.y1, y), math.Max(r.x2, x), math.Max(r.y2, y)}
}

// overlaps reports whether the interiors intersect. Boxes that merely share an
// edge do not overlap.
func (r rect) overlaps(o rect) bool {
	return r.x1 < o.x2-gridEps && o.x1 < r.x2-gridEps &&
		r.y1 < o.y2-gridEps && o.y1 < r.y2-gridEps
}
