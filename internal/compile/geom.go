package compile

// Cell is the schematic grid pitch in mm. Every resolved pin position is an
// integer multiple of Cell by construction.
const Cell = 2.54

// BlockMargin is the minimum gap between block bounding boxes, in cells.
const BlockMarginCells = 4

// SymbolGeom answers geometry questions about library symbols. Implemented
// over internal/sexp by the integration layer so the resolver stays pure and
// testable with fakes.
type SymbolGeom interface {
	// PinOffset returns the position of a pin of one unit relative to the
	// symbol origin at rotation 0 without mirror, in schematic mm (Y grows
	// downward). The pin may be given by number or by name; a name matching
	// several stacked pins resolves to the lowest pin number. unit <= 1 is
	// unit 1.
	PinOffset(libID string, unit int, pin string) (dx, dy float64, err error)
	// Pins returns all pin numbers of one unit, sorted numerically where
	// possible. Used for no_connect "unused" expansion and overlap checks.
	Pins(libID string, unit int) ([]string, error)
	// Body returns the unit's body bounding box (excluding pin length)
	// relative to origin at rotation 0 without mirror.
	Body(libID string, unit int) (x1, y1, x2, y2 float64, err error)
}

// TemplateGeom exposes the footprint of a baked template so blocks can be
// arranged without instantiating them. Implemented over place2/templates.
type TemplateGeom interface {
	// Extent returns the template bounding box including baked wires and
	// labels, relative to the template origin.
	Extent(name string) (x1, y1, x2, y2 float64, err error)
}

// PlacedSymbol is one symbol unit with its absolute sheet position resolved.
type PlacedSymbol struct {
	Ref    string
	LibID  string
	Value  string
	Unit   int     // always >= 1
	X, Y   float64 // symbol origin, absolute mm
	Rot    int
	Mirror bool
}

// PlacedBlock is one block with its origin and content resolved. For
// template blocks Symbols is empty; the integration layer stamps the
// template at (OriginX, OriginY).
type PlacedBlock struct {
	Name             string
	OriginX, OriginY float64
	Symbols          []PlacedSymbol
	// BBox of everything the block will occupy, absolute mm.
	X1, Y1, X2, Y2 float64
}

// Layout is the resolver output: every block arranged, every explicit
// symbol at its final absolute position, no overlaps.
type Layout struct {
	Blocks []PlacedBlock
}
