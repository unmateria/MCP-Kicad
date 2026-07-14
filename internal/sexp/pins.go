package sexp

import (
	"math"
	"strconv"
	"strings"
)

// PinInfo holds a pin's identity and its position in schematic coordinates.
//
// Direction is the OUTGOING wire direction in screen coordinates (Y-down),
// measured CCW from +X visually:
//
//	0   = east  (wire goes right)
//	90  = north (wire goes up,    screen-Y decreases)
//	180 = west  (wire goes left)
//	270 = south (wire goes down,  screen-Y increases)
//
// Because KiCad pin angles describe the direction TOWARDS the body, Direction
// is computed as 180° opposite of the pin's local angle, plus the symbol
// rotation. Use it for power-symbol orientation, "exit-segment" wire stubs,
// and direction-aware A* tie-breaking.
type PinInfo struct {
	Number     string
	Name       string
	X, Y       float64
	Direction  float64
	Electrical string // KiCad pin electrical type: input, output, bidirectional,
	// tri_state, passive, free, unspecified, power_in, power_out,
	// open_collector, open_emitter, no_connect.
}

// DirDelta returns the (dx, dy) offset that goes one step in the pin's
// outgoing direction in SCREEN coordinates (Y-down).
func (p PinInfo) DirDelta() (dx, dy float64) {
	switch int(math.Round(p.Direction)) % 360 {
	case 0:
		return 1, 0
	case 90:
		return 0, -1
	case 180:
		return -1, 0
	case 270:
		return 0, 1
	}
	return 0, 0
}

// SchematicSymbol is a placed symbol with all its pin positions resolved.
type SchematicSymbol struct {
	Reference string
	LibID     string
	Value     string
	X, Y      float64
	Rotation  float64
	Unit      int // unit number for multi-unit ICs (1-based)
	Pins      []PinInfo
}

// ReadSymbols returns all placed symbols in the schematic with their pins
// resolved to absolute schematic coordinates.
func ReadSymbols(sch *Schematic) []SchematicSymbol {
	ls := sch.LibSymbols()
	var result []SchematicSymbol
	for _, inst := range sch.root.Children {
		if inst.Head() != "symbol" {
			continue
		}
		// Direct children with lib_id are instances; children inside lib_symbols are definitions.
		if FindList(inst, "lib_id") == nil {
			continue
		}
		ss := resolveSymbol(inst, ls)
		if ss != nil {
			result = append(result, *ss)
		}
	}
	return result
}

// FindPinPosition finds the schematic coordinates of a specific pin on a
// placed symbol.
//
// refPin formats:
//   - "REF.pin"       e.g. "R1.1" or "BT1.+"  (any unit — for single-unit components)
//   - "REF.unit.pin"  e.g. "U1.1.+" or "U1.2.-" (unit-qualified — required for multi-unit ICs)
//
// Returns the coordinates and true if found.
func FindPinPosition(sch *Schematic, refPin string) (x, y float64, ok bool) {
	if p, ok := FindPin(sch, refPin); ok {
		return p.X, p.Y, true
	}
	return 0, 0, false
}

// FindPin returns the full PinInfo (position + outgoing direction) for a pin
// reference. Use this when direction matters — e.g., for orienting power
// symbols or computing exit-segment vectors. See FindPinPosition for callers
// that only need coordinates.
func FindPin(sch *Schematic, refPin string) (PinInfo, bool) {
	ref, unit, pin := splitRefPinUnit(refPin)
	for _, sym := range ReadSymbols(sch) {
		if sym.Reference != ref {
			continue
		}
		if unit != 0 && sym.Unit != unit {
			continue
		}
		for _, p := range sym.Pins {
			if p.Number == pin || p.Name == pin {
				return p, true
			}
		}
	}
	return PinInfo{}, false
}

// PowerSymbolPinAngle returns the local-frame pin angle of a power symbol's
// single pin (e.g. 90 for power:VCC, 270 for power:GND). Used to compute the
// rotation needed to make the power symbol face an arbitrary target pin.
// Returns 270 (GND-like default) when no pin is found.
func PowerSymbolPinAngle(libSymDef *Node) float64 {
	for _, child := range libSymDef.Children {
		if child.Head() != "symbol" {
			continue
		}
		for _, c := range child.Children {
			if c.Head() != "pin" {
				continue
			}
			atN := FindList(c, "at")
			if atN != nil && len(atN.Children) >= 4 {
				return parseF(AtomValue(atN, 3))
			}
			return 0
		}
	}
	return 270
}

// splitRefPinUnit parses pin references in two formats:
//   - "REF.pin"       → ref=REF, unit=0 (any), pin=pin
//   - "REF.unit.pin"  → ref=REF, unit=N, pin=pin  (when middle part is numeric)
func splitRefPinUnit(s string) (ref string, unit int, pin string) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) == 3 {
		if u, err := strconv.Atoi(parts[1]); err == nil {
			return parts[0], u, parts[2]
		}
	}
	r, p := splitRefPin(s)
	return r, 0, p
}

// resolveSymbol computes the SchematicSymbol for one placed symbol instance.
func resolveSymbol(inst, libSymbols *Node) *SchematicSymbol {
	atN := FindList(inst, "at")
	if atN == nil {
		return nil
	}
	cx := parseF(AtomValue(atN, 1))
	cy := parseF(AtomValue(atN, 2))
	rot := parseF(AtomValue(atN, 3))

	libIDN := FindList(inst, "lib_id")
	libID := StringValue(libIDN, 1)

	// Read the unit number from (unit N).
	unit := 1
	if unitNode := FindList(inst, "unit"); unitNode != nil {
		if v, err := strconv.Atoi(AtomValue(unitNode, 1)); err == nil && v > 0 {
			unit = v
		}
	}

	ref, val := "", ""
	for _, child := range inst.Children {
		if child.Head() == "property" {
			switch StringValue(child, 1) {
			case "Reference":
				ref = StringValue(child, 2)
			case "Value":
				val = StringValue(child, 2)
			}
		}
	}

	ss := &SchematicSymbol{Reference: ref, LibID: libID, Value: val, X: cx, Y: cy, Rotation: rot, Unit: unit}

	if libSymbols == nil {
		return ss
	}
	for _, def := range libSymbols.Children {
		if def.Head() == "symbol" && StringValue(def, 1) == libID {
			ss.Pins = extractPins(def, cx, cy, rot, unit)
			return ss
		}
	}
	return ss
}

// extractPins collects pins for the given unit from a lib symbol definition
// and transforms them to absolute schematic coordinates.
//
// KiCad multi-unit symbols store pins inside sub-unit nodes named "PART_N_S"
// where N is the unit index (1, 2, …) and S is the body style. Sub-unit 0
// contains shared geometry (e.g. power pins shared by all units). When unit > 0
// only pins belonging to that unit or the shared unit 0 are returned.
// unit == 0 returns all pins (used internally for single-unit symbols).
func extractPins(def *Node, cx, cy, rot float64, unit int) []PinInfo {
	// Unqualified part name, e.g. "NE5532" from "Amplifier_Operational:NE5532".
	partName := StringValue(def, 1)
	if idx := strings.LastIndex(partName, ":"); idx >= 0 {
		partName = partName[idx+1:]
	}

	toSchematic := func(pinNode *Node) PinInfo {
		atN := FindList(pinNode, "at")
		lx := parseF(AtomValue(atN, 1))
		ly := parseF(AtomValue(atN, 2))
		// pin angle (3rd at component) is the direction towards the body in
		// the symbol's Y-up local frame; outgoing direction is opposite (+180°).
		// Symbol rotation is then added in screen frame (CCW visual).
		pa := parseF(AtomValue(atN, 3))
		dir := math.Mod(pa+180+rot, 360)
		if dir < 0 {
			dir += 360
		}
		sx, sy := transformPin(lx, ly, cx, cy, rot)
		numN := FindList(pinNode, "number")
		nameN := FindList(pinNode, "name")
		// Electrical type is the first atom child after `pin` (e.g. "passive",
		// "power_in", "output"). Default empty when malformed.
		elec := AtomValue(pinNode, 1)
		return PinInfo{
			Number:     StringValue(numN, 1),
			Name:       StringValue(nameN, 1),
			X:          round2(sx),
			Y:          round2(sy),
			Direction:  dir,
			Electrical: elec,
		}
	}

	var pins []PinInfo

	// Pins declared directly inside the top-level symbol node
	// (occurs in simple single-unit symbols).
	for _, child := range def.Children {
		if child.Head() == "pin" {
			pins = append(pins, toSchematic(child))
		}
	}

	// Pins inside sub-unit nodes, filtered by unit index.
	for _, child := range def.Children {
		if child.Head() != "symbol" {
			continue
		}
		subName := StringValue(child, 1)
		if subName == "" {
			subName = AtomValue(child, 1)
		}
		u := subUnitIndex(partName, subName)
		// Include sub-unit 0 (shared body, e.g. power pins) always.
		// Include the requested unit. If unit == 0, include all.
		if unit != 0 && u != 0 && u != unit {
			continue
		}
		for _, pinNode := range child.Children {
			if pinNode.Head() == "pin" {
				pins = append(pins, toSchematic(pinNode))
			}
		}
	}
	return pins
}

// transformPin converts a pin's local (Y-up) coordinates to schematic (Y-down)
// coordinates, applying the symbol's rotation (degrees CCW in KiCad display).
//
// Formula (derived from KiCad coordinate conventions):
//
//	r = rotation in radians
//	schX = cx + lx*cos(r) - ly*sin(r)
//	schY = cy - lx*sin(r) - ly*cos(r)
func transformPin(lx, ly, cx, cy, rot float64) (float64, float64) {
	r := rot * math.Pi / 180
	c, s := math.Cos(r), math.Sin(r)
	return cx + lx*c - ly*s, cy - lx*s - ly*c
}

func parseF(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// Round2 is the exported version of round2 for use by other packages.
func Round2(v float64) float64 { return round2(v) }

// splitRefPin splits "BT1.+" into ("BT1", "+").
func splitRefPin(s string) (ref, pin string) {
	for i, c := range s {
		if c == '.' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// SymbolBBox returns an approximate bounding box for a placed symbol.
// Uses pin positions plus a small pad if pins are available,
// otherwise falls back to a fixed default centred on (X, Y).
func SymbolBBox(sym SchematicSymbol) (x1, y1, x2, y2 float64) {
	const pad = 2.54
	const defaultHalf = 7.62
	if len(sym.Pins) == 0 {
		return sym.X - defaultHalf, sym.Y - defaultHalf,
			sym.X + defaultHalf, sym.Y + defaultHalf
	}
	x1, y1 = sym.Pins[0].X, sym.Pins[0].Y
	x2, y2 = x1, y1
	for _, p := range sym.Pins[1:] {
		if p.X < x1 {
			x1 = p.X
		}
		if p.Y < y1 {
			y1 = p.Y
		}
		if p.X > x2 {
			x2 = p.X
		}
		if p.Y > y2 {
			y2 = p.Y
		}
	}
	return x1 - pad, y1 - pad, x2 + pad, y2 + pad
}

// SegmentCrossesBox returns true when the axis-aligned segment from (ax,ay)
// to (bx,by) passes through the strict interior of the rectangle
// [rx1,rx2] × [ry1,ry2]. Suitable for detecting whether a wire would cut
// through the body of a symbol.
func SegmentCrossesBox(ax, ay, bx, by, rx1, ry1, rx2, ry2 float64) bool {
	if ax > bx {
		ax, bx = bx, ax
	}
	if ay > by {
		ay, by = by, ay
	}
	if rx1 > rx2 {
		rx1, rx2 = rx2, rx1
	}
	if ry1 > ry2 {
		ry1, ry2 = ry2, ry1
	}
	return ax < rx2 && bx > rx1 && ay < ry2 && by > ry1
}
