package sexp

import (
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// Schematic represents a parsed .kicad_sch file.
type Schematic struct {
	root *Node // top-level (kicad_sch ...) node
}

// ParseSchematic parses a .kicad_sch file content.
func ParseSchematic(content string) (*Schematic, error) {
	nodes, err := Parse(content)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("sexp: empty schematic file")
	}
	root := nodes[0]
	if root.Head() != "kicad_sch" {
		return nil, fmt.Errorf("sexp: expected kicad_sch, got %q", root.Head())
	}
	return &Schematic{root: root}, nil
}

// Serialize writes the schematic back to its S-expression text form.
func (s *Schematic) Serialize() string {
	return Write([]*Node{s.root})
}

// Root returns the raw AST root node for direct manipulation.
func (s *Schematic) Root() *Node { return s.root }

// ReplaceRoot swaps in a new root node, replacing the entire schematic
// content. Used to commit a variant computed on a re-parsed copy.
func (s *Schematic) ReplaceRoot(root *Node) {
	if root != nil {
		s.root = root
	}
}

// Version returns the KiCad schematic format version.
func (s *Schematic) Version() string {
	n := FindList(s.root, "version")
	if n == nil {
		return ""
	}
	return AtomValue(n, 1)
}

// Symbols returns all (symbol ...) instances in the schematic (not lib symbols).
func (s *Schematic) Symbols() []*Node {
	return FindAllLists(s.root, "symbol")
}

// LibSymbols returns the (lib_symbols ...) node, which holds embedded symbol definitions.
func (s *Schematic) LibSymbols() *Node {
	return FindList(s.root, "lib_symbols")
}

// Wires returns all (wire ...) nodes.
func (s *Schematic) Wires() []*Node {
	return FindAllLists(s.root, "wire")
}

// AddSymbol appends a symbol instance node to the schematic.
// symbolNode should be a (symbol ...) AST node.
func (s *Schematic) AddSymbol(symbolNode *Node) {
	s.root.Children = append(s.root.Children, symbolNode)
}

// AddLibSymbol appends a symbol definition into the (lib_symbols) block.
// Creates lib_symbols block if it does not exist.
func (s *Schematic) AddLibSymbol(defNode *Node) {
	ls := s.LibSymbols()
	if ls == nil {
		ls = List(Atom("lib_symbols"))
		s.root.Children = append(s.root.Children, ls)
	}
	ls.Children = append(ls.Children, defNode)
}

// HasLibSymbol reports whether a symbol with the given qualified ID is already
// present in lib_symbols (e.g. "Amplifier_Operational:NE5532").
func (s *Schematic) HasLibSymbol(qualifiedID string) bool {
	ls := s.LibSymbols()
	if ls == nil {
		return false
	}
	for _, child := range ls.Children {
		if child.Head() == "symbol" && StringValue(child, 1) == qualifiedID {
			return true
		}
	}
	return false
}

// FindLibDef returns the library symbol definition node for qualifiedID
// (e.g. "Device:R") from the embedded lib_symbols, or nil if not found.
func (s *Schematic) FindLibDef(qualifiedID string) *Node {
	ls := s.LibSymbols()
	if ls == nil {
		return nil
	}
	for _, child := range ls.Children {
		if child.Head() == "symbol" && StringValue(child, 1) == qualifiedID {
			return child
		}
	}
	return nil
}

// libPropAt extracts the local-frame (x, y, angle) of a named property from a
// library symbol definition. The (x, y) values are in KiCad local coordinates
// (Y-up); callers transform them to schematic coords with transformPin.
// Returns found=false when the property or its (at) node is absent.
func libPropAt(libDef *Node, propName string) (lx, ly, angle float64, found bool) {
	if libDef == nil {
		return 0, 0, 0, false
	}
	for _, child := range libDef.Children {
		if child.Head() != "property" || StringValue(child, 1) != propName {
			continue
		}
		atN := FindList(child, "at")
		if atN == nil {
			return 0, 0, 0, false
		}
		return parseF(AtomValue(atN, 1)),
			parseF(AtomValue(atN, 2)),
			parseF(AtomValue(atN, 3)),
			true
	}
	return 0, 0, 0, false
}

// HidePropertyText marks one property of a symbol instance node as hidden —
// used for text that must exist for KiCad (ERC bookkeeping like PWR_FLAG's
// value) but only adds noise on the printed schematic.
func HidePropertyText(symNode *Node, propName string) {
	for _, child := range symNode.Children {
		if child.Head() != "property" || StringValue(child, 1) != propName {
			continue
		}
		effects := FindList(child, "effects")
		if effects == nil {
			effects = List(Atom("effects"))
			child.Children = append(child.Children, effects)
		}
		if FindList(effects, "hide") == nil {
			effects.Children = append(effects.Children, List(Atom("hide"), Atom("yes")))
		}
		return
	}
}

// FixLabelPositions moves the visible Reference and Value properties of all
// placed instances of `reference` so they sit clearly outside the component's
// pin bounding box (2.54 mm clearance from the outermost pin, horizontal text).
//
// This must be called AFTER the symbol has been added to the schematic and
// its lib symbol is embedded (so ReadSymbols can resolve pin positions).
// Multi-unit ICs are handled: each placed unit is adjusted independently.
func FixLabelPositions(sch *Schematic, reference string) {
	const margin = 2.54

	// Resolve pin screen positions for each placed unit of this reference.
	type bounds struct {
		cx, cy     float64
		minY, maxY float64
		init       bool
	}
	byUnit := make(map[int]*bounds)
	for _, sym := range ReadSymbols(sch) {
		if sym.Reference != reference {
			continue
		}
		b, ok := byUnit[sym.Unit]
		if !ok {
			b = &bounds{cx: sym.X, cy: sym.Y}
			byUnit[sym.Unit] = b
		}
		for _, p := range sym.Pins {
			if !b.init {
				b.minY, b.maxY = p.Y, p.Y
				b.init = true
			} else {
				if p.Y < b.minY {
					b.minY = p.Y
				}
				if p.Y > b.maxY {
					b.maxY = p.Y
				}
			}
		}
	}
	if len(byUnit) == 0 {
		return
	}

	// Walk instance nodes and patch matching Reference/Value property positions.
	for _, symNode := range sch.root.Children {
		if symNode.Head() != "symbol" || FindList(symNode, "lib_id") == nil {
			continue
		}
		// Match reference.
		ref := ""
		for _, child := range symNode.Children {
			if child.Head() == "property" && StringValue(child, 1) == "Reference" {
				ref = StringValue(child, 2)
				break
			}
		}
		if ref != reference {
			continue
		}
		// Identify unit.
		unit := 1
		if un := FindList(symNode, "unit"); un != nil {
			if v, err := strconv.Atoi(AtomValue(un, 1)); err == nil {
				unit = v
			}
		}
		b, ok := byUnit[unit]
		if !ok || !b.init {
			continue
		}
		refY := b.minY - margin
		valY := b.maxY + margin

		for _, child := range symNode.Children {
			if child.Head() != "property" {
				continue
			}
			propName := StringValue(child, 1)
			if propName != "Reference" && propName != "Value" {
				continue
			}
			// Skip hidden properties (power symbol references, etc.).
			if eff := FindList(child, "effects"); eff != nil {
				if FindList(eff, "hide") != nil {
					continue
				}
			}
			atN := FindList(child, "at")
			if atN == nil || len(atN.Children) < 4 {
				continue
			}
			newY := refY
			if propName == "Value" {
				newY = valY
			}
			atN.Children[1] = Atom(fmt.Sprintf("%.6g", b.cx))
			atN.Children[2] = Atom(fmt.Sprintf("%.6g", newY))
			atN.Children[3] = Atom("0") // horizontal text
		}
	}
}

// AddWire appends a wire node to the schematic.
func (s *Schematic) AddWire(wireNode *Node) {
	s.root.Children = append(s.root.Children, wireNode)
}

// DedupeWires removes duplicate wire nodes from the schematic. Two wires are
// considered duplicates when their endpoints coincide within 0.01 mm (in
// either order: (a→b) ≡ (b→a)). Returns the number of wires removed.
//
// Used after MST greedy routing where the same pin can emit multiple
// identical "exit stubs" for separate hops, leaving the file with visually
// stacked segments.
func (s *Schematic) DedupeWires() int {
	type endpointKey struct {
		ax, ay, bx, by int64
	}
	const scale = 100.0 // 0.01mm precision
	keyFor := func(w *Node) endpointKey {
		pts := FindList(w, "pts")
		if pts == nil {
			return endpointKey{}
		}
		var xs, ys [2]float64
		n := 0
		for _, xy := range pts.Children {
			if xy.Head() != "xy" || n >= 2 {
				continue
			}
			xs[n] = parseF(AtomValue(xy, 1))
			ys[n] = parseF(AtomValue(xy, 2))
			n++
		}
		ax, ay := int64(xs[0]*scale+0.5), int64(ys[0]*scale+0.5)
		bx, by := int64(xs[1]*scale+0.5), int64(ys[1]*scale+0.5)
		// Canonicalize: ensure (ax,ay) < (bx,by) lexicographically so reversed
		// segments produce the same key.
		if ax > bx || (ax == bx && ay > by) {
			ax, ay, bx, by = bx, by, ax, ay
		}
		return endpointKey{ax, ay, bx, by}
	}
	seen := make(map[endpointKey]bool)
	out := s.root.Children[:0]
	removed := 0
	for _, child := range s.root.Children {
		if child.Head() == "wire" {
			k := keyFor(child)
			if seen[k] {
				removed++
				continue
			}
			seen[k] = true
		}
		out = append(out, child)
	}
	s.root.Children = out
	return removed
}

// AddNoConnect appends a no_connect node to the schematic.
func (s *Schematic) AddNoConnect(n *Node) {
	s.root.Children = append(s.root.Children, n)
}

// AddJunction appends a junction node to the schematic.
func (s *Schematic) AddJunction(n *Node) {
	s.root.Children = append(s.root.Children, n)
}

// AddLabel appends a net label node to the schematic.
func (s *Schematic) AddLabel(n *Node) {
	s.root.Children = append(s.root.Children, n)
}

// RemoveWires removes all (wire ...) nodes. Returns the count removed.
func (s *Schematic) RemoveWires() int {
	return s.removeNodesByHead("wire")
}

// RemoveNoConnects removes all (no_connect ...) nodes. Returns count removed.
func (s *Schematic) RemoveNoConnects() int {
	return s.removeNodesByHead("no_connect")
}

// RemoveJunctions removes all (junction ...) nodes. Returns count removed.
func (s *Schematic) RemoveJunctions() int {
	return s.removeNodesByHead("junction")
}

// RemovedLabel holds the name and position of a removed net label.
type RemovedLabel struct {
	Name string
	X, Y float64
}

// RemoveLabels removes all (label ...) nodes and returns their names and positions
// so callers can re-add them after moving symbols.
func (s *Schematic) RemoveLabels() []RemovedLabel {
	var removed []RemovedLabel
	filtered := make([]*Node, 0, len(s.root.Children))
	for _, child := range s.root.Children {
		if child.Head() != "label" {
			filtered = append(filtered, child)
			continue
		}
		name := StringValue(child, 1)
		if name == "" {
			name = AtomValue(child, 1)
		}
		var x, y float64
		if atN := FindList(child, "at"); atN != nil && len(atN.Children) >= 3 {
			x = parseF(AtomValue(atN, 1))
			y = parseF(AtomValue(atN, 2))
		}
		removed = append(removed, RemovedLabel{Name: name, X: x, Y: y})
	}
	s.root.Children = filtered
	return removed
}

// RemovedPowerSymbol holds the reference, lib_id, value, and position of a removed power symbol.
type RemovedPowerSymbol struct {
	Reference string
	LibID     string
	Value     string
	X, Y      float64
}

// RemovePowerSymbols removes all placed power symbol instances (lib_id starting with "power:"
// or equal to "Device:PWR_FLAG") and returns their info for re-placement.
func (s *Schematic) RemovePowerSymbols() []RemovedPowerSymbol {
	var removed []RemovedPowerSymbol
	filtered := make([]*Node, 0, len(s.root.Children))
	for _, child := range s.root.Children {
		if child.Head() != "symbol" || FindList(child, "lib_id") == nil {
			filtered = append(filtered, child)
			continue
		}
		libID := StringValue(FindList(child, "lib_id"), 1)
		if !strings.HasPrefix(libID, "power:") && libID != "Device:PWR_FLAG" {
			filtered = append(filtered, child)
			continue
		}
		ref, val := "", ""
		for _, c := range child.Children {
			if c.Head() != "property" {
				continue
			}
			switch StringValue(c, 1) {
			case "Reference":
				ref = StringValue(c, 2)
			case "Value":
				val = StringValue(c, 2)
			}
		}
		var x, y float64
		if atN := FindList(child, "at"); atN != nil && len(atN.Children) >= 3 {
			x = parseF(AtomValue(atN, 1))
			y = parseF(AtomValue(atN, 2))
		}
		removed = append(removed, RemovedPowerSymbol{Reference: ref, LibID: libID, Value: val, X: x, Y: y})
	}
	s.root.Children = filtered
	return removed
}

// DisconnectPin removes all wire segments and no_connect markers whose endpoint
// coincides with (x, y) (rounded to 2 decimal places). Returns the number of
// nodes removed. Use after FindPinPosition to target a specific pin.
func (s *Schematic) DisconnectPin(x, y float64) int {
	target := [2]float64{round2(x), round2(y)}
	filtered := make([]*Node, 0, len(s.root.Children))
	removed := 0
	for _, child := range s.root.Children {
		switch child.Head() {
		case "wire":
			pts := FindList(child, "pts")
			if pts != nil {
				for _, xy := range pts.Children {
					if xy.Head() != "xy" {
						continue
					}
					cx := round2(parseF(AtomValue(xy, 1)))
					cy := round2(parseF(AtomValue(xy, 2)))
					if [2]float64{cx, cy} == target {
						removed++
						goto nextChild
					}
				}
			}
		case "no_connect":
			atN := FindList(child, "at")
			if atN != nil {
				cx := round2(parseF(AtomValue(atN, 1)))
				cy := round2(parseF(AtomValue(atN, 2)))
				if [2]float64{cx, cy} == target {
					removed++
					goto nextChild
				}
			}
		}
		filtered = append(filtered, child)
	nextChild:
	}
	s.root.Children = filtered
	return removed
}

// removeNodesByHead filters out all direct children whose head equals name.
func (s *Schematic) removeNodesByHead(name string) int {
	filtered := make([]*Node, 0, len(s.root.Children))
	removed := 0
	for _, child := range s.root.Children {
		if child.Head() == name {
			removed++
			continue
		}
		filtered = append(filtered, child)
	}
	s.root.Children = filtered
	return removed
}

// MoveSymbol updates the (at x y rotation) node of every placed instance whose
// Reference property equals reference. The rotation is preserved.
// Returns the number of symbol instances updated.
func (s *Schematic) MoveSymbol(reference string, x, y float64) int {
	x, y = snapGrid(x), snapGrid(y)
	updated := 0
	for _, sym := range s.root.Children {
		if sym.Head() != "symbol" || FindList(sym, "lib_id") == nil {
			continue
		}
		ref := ""
		for _, child := range sym.Children {
			if child.Head() == "property" && StringValue(child, 1) == "Reference" {
				ref = StringValue(child, 2)
				break
			}
		}
		if ref != reference {
			continue
		}
		atN := FindList(sym, "at")
		if atN == nil || len(atN.Children) < 3 {
			continue
		}
		oldX, _ := strconv.ParseFloat(AtomValue(atN, 1), 64)
		oldY, _ := strconv.ParseFloat(AtomValue(atN, 2), 64)
		dx, dy := x-oldX, y-oldY
		atN.Children[1] = Atom(fmt.Sprintf("%.6g", x))
		atN.Children[2] = Atom(fmt.Sprintf("%.6g", y))
		movePropertyPositions(sym, dx, dy)
		updated++
	}
	return updated
}

// SetSymbolRotation updates the rotation field of every placed instance whose
// Reference matches `reference`, AND rotates each property's (at) position
// around the symbol anchor so labels (Reference, Value, Footprint…) follow
// the symbol's new orientation. Returns the number of symbols updated.
//
// Used after PlaceFlow to auto-rotate horizontal-flowing symmetric components
// (resistors, capacitors, inductors) so their pins align with their neighbours
// instead of forcing the router to bend wires by 90° at every connection.
func (s *Schematic) SetSymbolRotation(reference string, newRot float64) int {
	updated := 0
	for _, sym := range s.root.Children {
		if sym.Head() != "symbol" || FindList(sym, "lib_id") == nil {
			continue
		}
		ref := ""
		for _, child := range sym.Children {
			if child.Head() == "property" && StringValue(child, 1) == "Reference" {
				ref = StringValue(child, 2)
				break
			}
		}
		if ref != reference {
			continue
		}
		atN := FindList(sym, "at")
		if atN == nil || len(atN.Children) < 4 {
			continue
		}
		oldRot, _ := strconv.ParseFloat(AtomValue(atN, 3), 64)
		if oldRot == newRot {
			continue
		}
		atN.Children[3] = Atom(fmt.Sprintf("%.6g", newRot))
		cx, _ := strconv.ParseFloat(AtomValue(atN, 1), 64)
		cy, _ := strconv.ParseFloat(AtomValue(atN, 2), 64)
		rotateProperties(sym, cx, cy, oldRot, newRot)
		updated++
	}
	return updated
}

// rotateProperties rotates each property's (at) anchor around (cx, cy) by
// (newRot - oldRot) degrees so the visible label stays in the same relative
// position to the symbol body after rotation.
func rotateProperties(sym *Node, cx, cy, oldRot, newRot float64) {
	delta := newRot - oldRot
	rad := delta * math.Pi / 180
	c, s := math.Cos(rad), math.Sin(rad)
	for _, child := range sym.Children {
		if child.Head() != "property" {
			continue
		}
		propAt := FindList(child, "at")
		if propAt == nil || len(propAt.Children) < 3 {
			continue
		}
		px, _ := strconv.ParseFloat(AtomValue(propAt, 1), 64)
		py, _ := strconv.ParseFloat(AtomValue(propAt, 2), 64)
		ox, oy := px-cx, py-cy
		nox := ox*c - oy*s
		noy := ox*s + oy*c
		propAt.Children[1] = Atom(fmt.Sprintf("%.6g", cx+nox))
		propAt.Children[2] = Atom(fmt.Sprintf("%.6g", cy+noy))
	}
}

// MoveSymbolUnit updates the (at x y) of one specific unit of a multi-unit IC.
// If unit == 0 it behaves like MoveSymbol (moves all units). Rotation preserved.
// Returns the number of symbol instances updated.
func (s *Schematic) MoveSymbolUnit(reference string, unit int, x, y float64) int {
	x, y = snapGrid(x), snapGrid(y)
	updated := 0
	for _, sym := range s.root.Children {
		if sym.Head() != "symbol" || FindList(sym, "lib_id") == nil {
			continue
		}
		ref := ""
		for _, child := range sym.Children {
			if child.Head() == "property" && StringValue(child, 1) == "Reference" {
				ref = StringValue(child, 2)
				break
			}
		}
		if ref != reference {
			continue
		}
		if unit != 0 {
			unitN := 1
			if unitNode := FindList(sym, "unit"); unitNode != nil {
				if v, err := strconv.Atoi(AtomValue(unitNode, 1)); err == nil {
					unitN = v
				}
			}
			if unitN != unit {
				continue
			}
		}
		atN := FindList(sym, "at")
		if atN == nil || len(atN.Children) < 3 {
			continue
		}
		oldX, _ := strconv.ParseFloat(AtomValue(atN, 1), 64)
		oldY, _ := strconv.ParseFloat(AtomValue(atN, 2), 64)
		dx, dy := x-oldX, y-oldY
		atN.Children[1] = Atom(fmt.Sprintf("%.6g", x))
		atN.Children[2] = Atom(fmt.Sprintf("%.6g", y))
		movePropertyPositions(sym, dx, dy)
		updated++
	}
	return updated
}

// movePropertyPositions shifts all property (at ...) positions by (dx, dy).
func movePropertyPositions(sym *Node, dx, dy float64) {
	for _, child := range sym.Children {
		if child.Head() != "property" {
			continue
		}
		propAt := FindList(child, "at")
		if propAt == nil || len(propAt.Children) < 3 {
			continue
		}
		px, _ := strconv.ParseFloat(AtomValue(propAt, 1), 64)
		py, _ := strconv.ParseFloat(AtomValue(propAt, 2), 64)
		propAt.Children[1] = Atom(fmt.Sprintf("%.6g", px+dx))
		propAt.Children[2] = Atom(fmt.Sprintf("%.6g", py+dy))
	}
}

// NewUUID returns a random UUID v4 string. Exported for use by other packages.
func NewUUID() string { return newUUID() }

// snapGrid rounds v to the nearest 1.27mm (50 mil) KiCad connection grid point.
// All symbol, wire, and label coordinates are snapped automatically so that pin
// endpoints land on the connection grid and wires connect reliably.
func snapGrid(v float64) float64 {
	const grid = 1.27
	return math.Round(v/grid) * grid
}

// SnapGrid is the exported version of snapGrid for use by other packages.
func SnapGrid(v float64) float64 { return snapGrid(v) }

// WireEndpointSet returns a set of all wire endpoint positions in the schematic,
// rounded to 2 decimal places. Use as a fast membership test.
func WireEndpointSet(sch *Schematic) map[[2]float64]bool {
	set := make(map[[2]float64]bool)
	for _, wire := range sch.Wires() {
		pts := FindList(wire, "pts")
		if pts == nil {
			continue
		}
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			x := round2(parseF(AtomValue(xy, 1)))
			y := round2(parseF(AtomValue(xy, 2)))
			set[[2]float64{x, y}] = true
		}
	}
	// Also include label positions (net labels connect pins too).
	for _, child := range sch.root.Children {
		if child.Head() != "label" {
			continue
		}
		atN := FindList(child, "at")
		if atN == nil {
			continue
		}
		x := round2(parseF(AtomValue(atN, 1)))
		y := round2(parseF(AtomValue(atN, 2)))
		set[[2]float64{x, y}] = true
	}
	return set
}

// ConnectedPins returns a set of all pin positions that participate in a net
// with at least two pins (per TraceNets). Use this instead of WireEndpointSet
// when you need to know whether a pin is electrically connected — it accounts
// for wires, labels, AND power-symbol implicit nets, which WireEndpointSet
// alone misses.
//
// Performance: ReadSymbols is O(n) and was previously called once per pin
// inside a triple-nested loop, costing O(p²·n) on big schematics. We cache
// it and index by reference for O(1) lookup so the whole function is O(p+n).
func ConnectedPins(sch *Schematic) map[[2]float64]bool {
	set := make(map[[2]float64]bool)
	allSyms := ReadSymbols(sch)
	byRef := make(map[string][]SchematicSymbol, len(allSyms))
	for _, s := range allSyms {
		byRef[s.Reference] = append(byRef[s.Reference], s)
	}
	for _, net := range TraceNets(sch) {
		if len(net.Pins) < 2 {
			continue
		}
		for _, ref := range net.Pins {
			for _, sym := range byRef[ref.Reference] {
				if ref.Unit != 0 && sym.Unit != ref.Unit {
					continue
				}
				for _, p := range sym.Pins {
					if p.Number == ref.PinNumber || p.Name == ref.PinName {
						set[[2]float64{round2(p.X), round2(p.Y)}] = true
					}
				}
			}
		}
	}
	return set
}

// WireEndpoints returns all wire endpoint and label positions as a slice
// (rounded to 2 decimal places). Use when distances to other points matter;
// use WireEndpointSet for fast membership tests.
func WireEndpoints(sch *Schematic) [][2]float64 {
	var pts [][2]float64
	for _, wire := range sch.Wires() {
		ptsList := FindList(wire, "pts")
		if ptsList == nil {
			continue
		}
		for _, xy := range ptsList.Children {
			if xy.Head() != "xy" {
				continue
			}
			pts = append(pts, [2]float64{
				round2(parseF(AtomValue(xy, 1))),
				round2(parseF(AtomValue(xy, 2))),
			})
		}
	}
	for _, child := range sch.root.Children {
		if child.Head() != "label" {
			continue
		}
		atN := FindList(child, "at")
		if atN == nil {
			continue
		}
		pts = append(pts, [2]float64{
			round2(parseF(AtomValue(atN, 1))),
			round2(parseF(AtomValue(atN, 2))),
		})
	}
	return pts
}

// NoConnectPointSet returns a set of all no_connect marker positions,
// rounded to 2 decimal places.
func NoConnectPointSet(sch *Schematic) map[[2]float64]bool {
	set := make(map[[2]float64]bool)
	for _, child := range sch.root.Children {
		if child.Head() != "no_connect" {
			continue
		}
		atN := FindList(child, "at")
		if atN == nil {
			continue
		}
		x := round2(parseF(AtomValue(atN, 1)))
		y := round2(parseF(AtomValue(atN, 2)))
		set[[2]float64{x, y}] = true
	}
	return set
}

// NewSymbolInstance creates a symbol instance node compatible with KiCad 9.
//
// libID is the full library identifier, e.g. "Device:R".
// reference is the reference designator, e.g. "R1".
// value is the component value shown on the schematic, e.g. "100" for a resistor.
// footprint is the footprint reference. May be "".
// x, y are the position coordinates in mm (auto-snapped to 1.27mm grid).
// rotation is CCW degrees (0, 90, 180, 270).
// unit selects which unit of a multi-unit IC to place (1-based); <= 0 defaults to 1.
// pinNumbers lists the symbol's pin numbers for KiCad 9 pin UUID entries; nil to omit.
// schUUID is the root schematic UUID for the (instances ...) block; "" uses "/" as path.
// inBom and onBoard control the in_bom/on_board attributes; false for power symbols.
// libDef is the embedded library definition node (from Schematic.FindLibDef); when
// non-nil its property positions are used instead of the hardcoded fallback offsets,
// so Reference and Value labels land where KiCad itself would place them.
func NewSymbolInstance(libID, reference, value, footprint string, x, y, rotation float64, unit int, pinNumbers []string, schUUID string, inBom, onBoard bool, libDef *Node) *Node {
	if unit <= 0 {
		unit = 1
	}
	x, y = snapGrid(x), snapGrid(y)
	// visibleProp: shown on schematic canvas (Reference, Value only).
	// When libDef is available, use the library's own property coordinates
	// (transformed to schematic frame) so labels match KiCad's placement.
	// Fallback offsets (defaultOx, defaultOy) are used when no lib data exists.
	visibleProp := func(name, val string, defaultOx, defaultOy float64) *Node {
		var px, py, propAngle float64
		if lx, ly, la, ok := libPropAt(libDef, name); ok {
			px, py = transformPin(lx, ly, x, y, rotation)
			propAngle = la // keep lib text angle; position already rotated via transformPin
		} else {
			px, py = transformPin(defaultOx, defaultOy, x, y, rotation)
		}
		return List(
			Atom("property"),
			Str(name),
			Str(val),
			List(Atom("at"), Atom(fmt.Sprintf("%.6g", px)), Atom(fmt.Sprintf("%.6g", py)), Atom(fmt.Sprintf("%.6g", propAngle))),
			List(Atom("effects"), List(Atom("font"), List(Atom("size"), Atom("1.27"), Atom("1.27")))),
		)
	}
	// hiddenProp: hidden from canvas (Footprint, Datasheet, Description).
	hiddenProp := func(name, val string) *Node {
		return List(
			Atom("property"),
			Str(name),
			Str(val),
			List(Atom("at"), Atom(fmt.Sprintf("%.6g", x)), Atom(fmt.Sprintf("%.6g", y)), Atom("0")),
			List(Atom("effects"),
				List(Atom("font"), List(Atom("size"), Atom("1.27"), Atom("1.27"))),
				List(Atom("hide"), Atom("yes")),
			),
		)
	}
	if value == "" {
		value = "~"
	}

	boolAtom := func(b bool) *Node {
		if b {
			return Atom("yes")
		}
		return Atom("no")
	}

	// Power/utility symbols have their reference hidden (KiCad convention: #PWR0N not shown).
	// Regular components: reference above (local +Y), value below (local -Y), both
	// rotation-aware. Power symbols: only value shown, placed 2.54 mm above anchor
	// (local +Y) matching KiCad's standard VCC/GND placement.
	var refProp *Node
	var valProp *Node
	if !inBom && !onBoard {
		refProp = hiddenProp("Reference", reference)
		valProp = visibleProp("Value", value, 0, 2.54)
	} else {
		refProp = visibleProp("Reference", reference, 0, 3.81)
		valProp = visibleProp("Value", value, 0, -3.81)
	}

	children := []*Node{
		Atom("symbol"),
		List(Atom("lib_id"), Str(libID)),
		List(Atom("at"), Atom(fmt.Sprintf("%.6g", x)), Atom(fmt.Sprintf("%.6g", y)), Atom(fmt.Sprintf("%.6g", rotation))),
		List(Atom("unit"), Atom(strconv.Itoa(unit))),
		// (mirror …) is inserted by SetSymbolMirror when asked for; KiCad
		// omits the node entirely on unmirrored symbols.
		List(Atom("exclude_from_sim"), Atom("no")),
		List(Atom("in_bom"), boolAtom(inBom)),
		List(Atom("on_board"), boolAtom(onBoard)),
		List(Atom("dnp"), Atom("no")),
		List(Atom("uuid"), Str(newUUID())),
		refProp,
		valProp,
		hiddenProp("Footprint", footprint),
		hiddenProp("Datasheet", ""),
		hiddenProp("Description", ""),
	}

	// KiCad 9 requires a (pin "N" (uuid "...")) entry for each pin.
	for _, num := range pinNumbers {
		children = append(children, List(Atom("pin"), Str(num), List(Atom("uuid"), Str(newUUID()))))
	}

	// KiCad 9 requires an (instances ...) block for reference tracking.
	path := "/"
	if schUUID != "" {
		path = "/" + schUUID
	}
	children = append(children, List(
		Atom("instances"),
		List(
			Atom("project"),
			Str(""),
			List(
				Atom("path"),
				Str(path),
				List(Atom("reference"), Str(reference)),
				List(Atom("unit"), Atom(strconv.Itoa(unit))),
			),
		),
	))

	return &Node{Children: children}
}

// ExtractPinNumbers returns pin numbers for a specific unit from a lib symbol node.
// unit selects which unit's pins to include (1-based). Unit 0 sub-units (body geometry
// shared by all units) are always included if they contain pins. Pass unit=0 to get
// all pins regardless of unit.
func ExtractPinNumbers(libSymDef *Node, unit int) []string {
	partName := StringValue(libSymDef, 1)
	// strip lib prefix if qualified (e.g. "Device:R" → "R")
	if idx := strings.LastIndex(partName, ":"); idx >= 0 {
		partName = partName[idx+1:]
	}

	seen := make(map[string]bool)
	var nums []string
	for _, child := range libSymDef.Children {
		if child.Head() != "symbol" {
			continue
		}
		subName := StringValue(child, 1)
		if subName == "" {
			subName = AtomValue(child, 1)
		}
		// Determine which unit this sub-unit belongs to.
		subUnit := subUnitIndex(partName, subName)
		// Include unit 0 (common body) always, and the requested unit.
		if unit != 0 && subUnit != 0 && subUnit != unit {
			continue
		}
		for _, c := range child.Children {
			if c.Head() != "pin" {
				continue
			}
			numNode := FindList(c, "number")
			if numNode == nil || len(numNode.Children) < 2 {
				continue
			}
			// KiCad stores pin numbers as quoted strings ("1", "2", "VCC"...).
			// StringValue strips the quotes; fall back to the raw atom value if a
			// non-standard symbol declares the number unquoted.
			num := StringValue(numNode, 1)
			if num == "" {
				num = AtomValue(numNode, 1)
			}
			if num != "" && !seen[num] {
				seen[num] = true
				nums = append(nums, num)
			}
		}
	}
	return nums
}

// subUnitIndex returns the unit index N from a sub-unit name "PARTNAME_N_S".
// Returns 0 for the shared-body unit or if parsing fails.
func subUnitIndex(partName, subName string) int {
	if !strings.HasPrefix(subName, partName+"_") {
		return -1 // unrelated
	}
	rest := subName[len(partName)+1:] // "N_S"
	parts := strings.SplitN(rest, "_", 2)
	if len(parts) < 1 {
		return 0
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return n
}

// CountUnits returns how many distinct units a lib symbol definition has.
// Single-unit components return 1. Multi-unit ICs (e.g. NE5532 dual op-amp) return 2+.
func CountUnits(libSymDef *Node) int {
	partName := StringValue(libSymDef, 1)
	if idx := strings.LastIndex(partName, ":"); idx >= 0 {
		partName = partName[idx+1:]
	}
	max := 0
	for _, child := range libSymDef.Children {
		if child.Head() != "symbol" {
			continue
		}
		subName := StringValue(child, 1)
		if subName == "" {
			subName = AtomValue(child, 1)
		}
		n := subUnitIndex(partName, subName)
		if n > max {
			max = n
		}
	}
	if max <= 0 {
		return 1
	}
	return max
}

// UUID returns the top-level schematic UUID (unquoted), or "" if not present.
func (s *Schematic) UUID() string {
	n := FindList(s.root, "uuid")
	if n == nil || len(n.Children) < 2 {
		return ""
	}
	if v := StringValue(n, 1); v != "" {
		return v
	}
	return AtomValue(n, 1)
}

// newUUID returns a random UUID v4 string.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ExtractSymbolDef reads a .kicad_sym file, finds the named symbol definition,
// and returns a deep-cloned node with names rewritten for embedding in a
// schematic's lib_symbols block (e.g. "R" → "Device:R").
// If the symbol uses (extends "Parent"), the extends value is qualified too
// (e.g. "LM2904" → "Amplifier_Operational:LM2904").
// Call ExtractSymbolDefWithParents when you also need the parent chain embedded.
func ExtractSymbolDef(symFilePath, libName, partName string) (*Node, error) {
	defs, err := parseSymbolLib(symFilePath)
	if err != nil {
		return nil, err
	}
	for _, child := range defs {
		if child.Head() == "symbol" && StringValue(child, 1) == partName {
			return qualifySymbolDef(child, libName), nil
		}
	}
	return nil, fmt.Errorf("sexp: symbol %q not found in %s", partName, symFilePath)
}

// ExtractSymbolDefWithParents returns a single self-contained symbol definition
// for embedding in a .kicad_sch lib_symbols block.
//
// If the symbol uses (extends "Parent"), the full ancestor chain is resolved
// recursively (R_POT → R_Potentiometer → R_Variable → …), merging pin geometry
// from each ancestor. The returned node has no extends reference and can be
// loaded by kicad-cli without any parent present in the library.
func ExtractSymbolDefWithParents(symFilePath, libName, partName string) ([]*Node, error) {
	defs, err := parseSymbolLib(symFilePath)
	if err != nil {
		return nil, err
	}

	index := make(map[string]*Node, len(defs))
	for _, d := range defs {
		if d.Head() == "symbol" {
			index[StringValue(d, 1)] = d
		}
	}

	raw, err := resolveRawChain(index, partName)
	if err != nil {
		return nil, fmt.Errorf("sexp: %w (in %s)", err, symFilePath)
	}
	return []*Node{qualifySymbolDef(raw, libName)}, nil
}

// FlattenLibSymbol resolves an (extends …) chain within one library's symbol
// set and returns a self-contained symbol whose name stays UNQUALIFIED.
//
// This is the .kicad_sym counterpart of ExtractSymbolDefWithParents, which
// qualifies names as "Lib:Part" because that is what a schematic's lib_symbols
// block wants. A library FILE wants the bare name, and copying a symbol into
// one without flattening loses every pin: 53.8% of KiCad's official symbols
// carry their geometry only in an ancestor.
//
// libSymbols is the child list of a (kicad_symbol_lib …) node.
func FlattenLibSymbol(libSymbols []*Node, partName string) (*Node, error) {
	index := make(map[string]*Node, len(libSymbols))
	for _, d := range libSymbols {
		if d.Head() == "symbol" {
			index[StringValue(d, 1)] = d
		}
	}
	raw, err := resolveRawChain(index, partName)
	if err != nil {
		return nil, fmt.Errorf("sexp: %w", err)
	}
	clone := deepClone(raw)
	// A flattened symbol carries its ancestors' geometry, so the reference
	// would now be a dangling pointer to a parent that is not coming along.
	filtered := clone.Children[:0]
	for _, c := range clone.Children {
		if c.Head() == "extends" {
			continue
		}
		filtered = append(filtered, c)
	}
	clone.Children = filtered
	return clone, nil
}

// resolveRawChain recursively resolves an (extends ...) chain, returning a raw
// (unqualified) node with all ancestor pin geometry merged in.
// All names stay unqualified so sub-unit renaming uses correct prefix lengths.
func resolveRawChain(index map[string]*Node, partName string) (*Node, error) {
	node, ok := index[partName]
	if !ok {
		return nil, fmt.Errorf("symbol %q not found", partName)
	}
	parentRawName := symbolExtendsName(node)
	if parentRawName == "" {
		return node, nil // leaf — return as-is
	}
	resolvedParent, err := resolveRawChain(index, parentRawName)
	if err != nil {
		return nil, err
	}
	return flattenRawSymbol(node, resolvedParent), nil
}

// flattenRawSymbol merges child against fully-resolved parent, both in raw
// (unqualified) form. The returned node keeps the child's unqualified name.
// Parent's sub-unit names are renamed from "ParentName_N_M" to "ChildName_N_M"
// so that extractPins can locate them by the child's unqualified prefix.
func flattenRawSymbol(child, parent *Node) *Node {
	partName := StringValue(child, 1)
	parentName := StringValue(parent, 1)

	merged := &Node{Children: []*Node{Atom("symbol"), Str(partName)}}

	// Parent's non-property metadata (pin_names, pin_numbers, exclude_from_sim…),
	// its properties, and its renamed sub-unit nodes (the pin definitions).
	// KiCad's own LIB_SYMBOL::Flatten() copies the parent whole and then applies
	// the derived symbol's field overrides, so a flattened derived symbol INHERITS
	// every parent property the child does not itself redefine (e.g. ki_locked).
	// Dropping those makes the embedded copy differ from what KiCad regenerates
	// and triggers a spurious lib_symbol_mismatch ERC warning.
	var parentMeta []*Node
	var parentProps []*Node
	var parentSubSymbols []*Node
	for _, c := range parent.Children[2:] { // skip "symbol" atom and unqualified name
		switch c.Head() {
		case "embedded_fonts", "extends":
			continue
		case "property":
			parentProps = append(parentProps, deepClone(c))
		case "symbol":
			sub := deepClone(c)
			if sub.Head() == "symbol" && len(sub.Children) > 1 {
				oldName := StringValue(sub, 1)
				if oldName == "" {
					oldName = AtomValue(sub, 1)
				}
				if strings.HasPrefix(oldName, parentName+"_") {
					// Rename e.g. "R_Potentiometer_1_1" → "R_POT_1_1"
					sub.Children[1] = Str(partName + oldName[len(parentName):])
				}
			}
			parentSubSymbols = append(parentSubSymbols, sub)
		default:
			parentMeta = append(parentMeta, deepClone(c))
		}
	}

	// Child's own properties (by name, override parent) and any non-structural
	// child metadata.
	childProps := map[string]*Node{}
	var childPropOrder []string
	var childMeta []*Node
	for _, c := range child.Children[2:] {
		switch c.Head() {
		case "extends", "embedded_fonts", "symbol":
			continue
		case "property":
			name := StringValue(c, 1)
			if _, seen := childProps[name]; !seen {
				childPropOrder = append(childPropOrder, name)
			}
			childProps[name] = deepClone(c)
		default:
			childMeta = append(childMeta, deepClone(c))
		}
	}

	// Merge properties: parent's, overridden in place by the child's; then any
	// property the child adds that the parent lacked.
	var mergedProps []*Node
	usedChild := map[string]bool{}
	for _, pp := range parentProps {
		name := StringValue(pp, 1)
		if cp, ok := childProps[name]; ok {
			mergedProps = append(mergedProps, cp)
			usedChild[name] = true
		} else {
			mergedProps = append(mergedProps, pp)
		}
	}
	for _, name := range childPropOrder {
		if !usedChild[name] {
			mergedProps = append(mergedProps, childProps[name])
		}
	}

	// Order: parent metadata, merged properties, child metadata, parent sub-units.
	merged.Children = append(merged.Children, parentMeta...)
	merged.Children = append(merged.Children, mergedProps...)
	merged.Children = append(merged.Children, childMeta...)
	merged.Children = append(merged.Children, parentSubSymbols...)
	return merged
}

// qualifySymbolDef deep-clones a raw symbol node (as read from a .kicad_sym)
// and qualifies its name and any extends reference with libName.
// Strips .kicad_sym-specific attributes (embedded_fonts) that are invalid
// inside a .kicad_sch lib_symbols block.
func qualifySymbolDef(n *Node, libName string) *Node {
	partName := StringValue(n, 1)
	qualified := libName + ":" + partName
	clone := deepClone(n)
	// Rename top-level symbol name.
	if clone.Head() == "symbol" && len(clone.Children) > 1 {
		if StringValue(clone, 1) == partName {
			clone.Children[1] = Str(qualified)
		}
	}
	// Qualify the extends reference; strip .kicad_sym-only attributes.
	filtered := clone.Children[:0]
	for _, child := range clone.Children {
		switch child.Head() {
		case "embedded_fonts":
			// .kicad_sym-only attribute, not valid in lib_symbols.
			continue
		case "extends":
			if len(child.Children) > 1 {
				parentName := StringValue(child, 1)
				child.Children[1] = Str(libName + ":" + parentName)
			}
		}
		filtered = append(filtered, child)
	}
	clone.Children = filtered
	return clone
}

// symbolExtendsName returns the unqualified parent name from (extends "Name"),
// or "" if the symbol does not extend anything.
func symbolExtendsName(n *Node) string {
	for _, child := range n.Children {
		if child.Head() == "extends" {
			return StringValue(child, 1)
		}
	}
	return ""
}

// parseSymbolLib reads a .kicad_sym file and returns its top-level child nodes.
func parseSymbolLib(symFilePath string) ([]*Node, error) {
	data, err := os.ReadFile(symFilePath)
	if err != nil {
		return nil, fmt.Errorf("sexp: cannot read %s: %w", symFilePath, err)
	}
	nodes, err := Parse(string(data))
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 || nodes[0].Head() != "kicad_symbol_lib" {
		return nil, fmt.Errorf("sexp: not a kicad_symbol_lib file: %s", symFilePath)
	}
	return nodes[0].Children, nil
}

// deepClone returns a full recursive copy of n.
func deepClone(n *Node) *Node {
	clone := &Node{Value: n.Value, IsString: n.IsString}
	if n.Children == nil {
		return clone
	}
	clone.Children = make([]*Node, len(n.Children))
	for i, child := range n.Children {
		clone.Children[i] = deepClone(child)
	}
	return clone
}

// deepCloneRenameSymbol is kept for backward compatibility.
// Prefer qualifySymbolDef for new code.
func deepCloneRenameSymbol(n *Node, oldPrefix, newPrefix string) *Node {
	clone := deepClone(n)
	if clone.Head() == "symbol" && len(clone.Children) > 1 {
		if StringValue(clone, 1) == oldPrefix {
			clone.Children[1] = Str(newPrefix)
		}
	}
	return clone
}

// NewWire creates a wire node connecting two points (auto-snapped to 1.27mm grid).
func NewWire(x1, y1, x2, y2 float64) *Node {
	x1, y1 = snapGrid(x1), snapGrid(y1)
	x2, y2 = snapGrid(x2), snapGrid(y2)
	return List(
		Atom("wire"),
		List(
			Atom("pts"),
			List(Atom("xy"), Atom(fmt.Sprintf("%.6g", x1)), Atom(fmt.Sprintf("%.6g", y1))),
			List(Atom("xy"), Atom(fmt.Sprintf("%.6g", x2)), Atom(fmt.Sprintf("%.6g", y2))),
		),
		List(Atom("stroke"), List(Atom("width"), Atom("0")), List(Atom("type"), Atom("default"))),
		List(Atom("uuid"), Str(newUUID())),
	)
}

// NewNoConnect creates a no_connect marker at (x, y) (auto-snapped to 1.27mm grid).
// Place this at any pin endpoint that is intentionally left unconnected to
// suppress ERC "pin not connected" errors.
func NewNoConnect(x, y float64) *Node {
	x, y = snapGrid(x), snapGrid(y)
	return List(
		Atom("no_connect"),
		List(Atom("at"), Atom(fmt.Sprintf("%.6g", x)), Atom(fmt.Sprintf("%.6g", y))),
		List(Atom("uuid"), Str(newUUID())),
	)
}

// NewJunction creates a junction dot at (x, y) (auto-snapped to 1.27mm grid).
// Required wherever three or more wires meet in a T-intersection; without it
// KiCad does not treat the crossing as an electrical connection.
func NewJunction(x, y float64) *Node {
	x, y = snapGrid(x), snapGrid(y)
	return List(
		Atom("junction"),
		List(Atom("at"), Atom(fmt.Sprintf("%.6g", x)), Atom(fmt.Sprintf("%.6g", y))),
		List(Atom("diameter"), Atom("0")),
		List(Atom("color"), Atom("0"), Atom("0"), Atom("0"), Atom("0")),
		List(Atom("uuid"), Str(newUUID())),
	)
}

// NewNetLabel creates a net label at (x, y, angleDeg) (auto-snapped to 1.27mm grid).
// Two labels with the same name form an electrical connection without a wire.
// angle: 0 = label to the right, 90 = label upward, 180 = left, 270 = down.
func NewNetLabel(name string, x, y, angleDeg float64) *Node {
	x, y = snapGrid(x), snapGrid(y)
	return List(
		Atom("label"),
		Str(name),
		List(Atom("at"), Atom(fmt.Sprintf("%.6g", x)), Atom(fmt.Sprintf("%.6g", y)), Atom(fmt.Sprintf("%.6g", angleDeg))),
		List(Atom("fields_autoplaced"), Atom("yes")),
		List(Atom("effects"),
			List(Atom("font"), List(Atom("size"), Atom("1.27"), Atom("1.27"))),
			List(Atom("justify"), Atom("left"), Atom("bottom")),
		),
		List(Atom("uuid"), Str(newUUID())),
		List(Atom("property"),
			Str("Intersheet References"),
			Str(""),
			List(Atom("at"), Atom("0"), Atom("0"), Atom("0")),
			List(Atom("effects"), List(Atom("font"), List(Atom("size"), Atom("1.27"), Atom("1.27"))), List(Atom("hide"), Atom("yes"))),
		),
	)
}

// SetSymbolMirror stamps KiCad's (mirror x|y) onto a symbol instance and
// reflects its property anchors to match, since those carry absolute
// coordinates that no longer follow the body once it flips.
//
// The node goes right after (at …), which is where KiCad writes it, and an
// axis of "" removes any mirror already present. Reflecting the geometry
// itself is ReadSymbols' job — see applyMirror in pins.go, and the measured
// convention in internal/compile.transformOffset.
func SetSymbolMirror(inst *Node, axis string) {
	if inst == nil {
		return
	}

	atN := FindList(inst, "at")
	if atN == nil {
		return
	}
	cx, cy := parseF(AtomValue(atN, 1)), parseF(AtomValue(atN, 2))

	// Drop any existing mirror node first, so this is idempotent and "" clears.
	kept := inst.Children[:0]
	for _, c := range inst.Children {
		if c.Head() == "mirror" {
			continue
		}
		kept = append(kept, c)
	}
	inst.Children = kept

	if axis != "x" && axis != "y" {
		return
	}

	for _, c := range inst.Children {
		if c.Head() != "property" {
			continue
		}
		pAt := FindList(c, "at")
		if pAt == nil || len(pAt.Children) < 3 {
			continue
		}
		if axis == "y" {
			pAt.Children[1] = Atom(fmt.Sprintf("%.6g", round2(2*cx-parseF(AtomValue(pAt, 1)))))
		} else {
			pAt.Children[2] = Atom(fmt.Sprintf("%.6g", round2(2*cy-parseF(AtomValue(pAt, 2)))))
		}
	}

	// Insert directly after (at …) to match KiCad's own node order.
	mirrorNode := List(Atom("mirror"), Atom(axis))
	for i, c := range inst.Children {
		if c.Head() == "at" {
			rest := append([]*Node{mirrorNode}, inst.Children[i+1:]...)
			inst.Children = append(inst.Children[:i+1], rest...)
			return
		}
	}
}

// RemoveLabelsNamed removes every (label "name") node and returns them, so a
// caller can put them back if dropping them breaks connectivity.
func (s *Schematic) RemoveLabelsNamed(name string) []*Node {
	var removed []*Node
	kept := s.root.Children[:0]
	for _, c := range s.root.Children {
		if c.Head() == "label" && StringValue(c, 1) == name {
			removed = append(removed, c)
			continue
		}
		kept = append(kept, c)
	}
	s.root.Children = kept
	return removed
}
