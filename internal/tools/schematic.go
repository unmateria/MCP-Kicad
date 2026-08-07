package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/router"
	"mcp-kicad/internal/sexp"
)

// modifySchematicInput defines the parameters for the modify_schematic tool.
type modifySchematicInput struct {
	SchematicPath string           `json:"schematic_path"       jsonschema:"Path to the .kicad_sch file"`
	Action        string           `json:"action"               jsonschema:"create_schematic | add_symbol | add_power_rail | connect_pins | disconnect_pin | add_wire | no_connect | junction | add_label | batch"`
	LibID         string           `json:"lib_id,omitempty"     jsonschema:"[add_symbol] e.g. Device:R or power:GND"`
	Reference     string           `json:"reference,omitempty"  jsonschema:"[add_symbol] e.g. R1"`
	Value         string           `json:"value,omitempty"      jsonschema:"[add_symbol] component value, e.g. 100"`
	MountType     string           `json:"mount_type,omitempty" jsonschema:"[add_symbol] THT or SMD — defaults to THT when omitted"`
	Footprint     string           `json:"footprint,omitempty"  jsonschema:"[add_symbol] optional override; auto-assigned if empty"`
	Unit          int              `json:"unit,omitempty"       jsonschema:"[add_symbol] unit number for multi-unit ICs (1, 2, 3...). Default 1."`
	AutoPlace     bool             `json:"auto_place,omitempty" jsonschema:"[add_symbol] set true to let server compute position automatically (non-overlapping grid slot). Prefer explicit x/y."`
	Rotation      float64          `json:"rotation,omitempty"   jsonschema:"[add_symbol/add_label] CCW degrees: 0, 90, 180, 270 (default 0)"`
	X             float64          `json:"x,omitempty"          jsonschema:"[add_symbol/add_wire/no_connect/junction/add_label] X in mm"`
	Y             float64          `json:"y,omitempty"          jsonschema:"[add_symbol/add_wire/no_connect/junction/add_label] Y in mm"`
	X2            float64          `json:"x2,omitempty"         jsonschema:"[add_wire] end X in mm"`
	Y2            float64          `json:"y2,omitempty"         jsonschema:"[add_wire] end Y in mm"`
	From          string           `json:"from,omitempty"       jsonschema:"[connect_pins/no_connect] pin ref e.g. BT1.+ — auto-resolves coordinates"`
	To            string           `json:"to,omitempty"         jsonschema:"[connect_pins] destination pin, e.g. R1.1"`
	Via           [][]float64      `json:"via,omitempty"        jsonschema:"[connect_pins] optional waypoints [[x1,y1],[x2,y2],...] to route wires through intermediate points and avoid obstacles"`
	Name          string           `json:"name,omitempty"       jsonschema:"[add_label] net label name; two labels with the same name are connected"`
	Operations    []batchOperation `json:"operations,omitempty" jsonschema:"[batch] list of operations to execute in one file write. Each element uses the same fields as a normal call (action, lib_id, etc.) — schematic_path is inherited. Stops on first error."`
}

// batchOperation is a single step inside a batch call.
// Identical fields to modifySchematicInput except no nested Operations
// (the MCP SDK cannot generate JSON schemas for recursive types).
type batchOperation struct {
	Action    string      `json:"action"               jsonschema:"add_symbol | add_power_rail | connect_pins | disconnect_pin | add_wire | no_connect | junction | add_label"`
	LibID     string      `json:"lib_id,omitempty"`
	Reference string      `json:"reference,omitempty"`
	Value     string      `json:"value,omitempty"`
	MountType string      `json:"mount_type,omitempty"`
	Footprint string      `json:"footprint,omitempty"`
	Unit      int         `json:"unit,omitempty"`
	AutoPlace bool        `json:"auto_place,omitempty"`
	Rotation  float64     `json:"rotation,omitempty"`
	X         float64     `json:"x,omitempty"`
	Y         float64     `json:"y,omitempty"`
	X2        float64     `json:"x2,omitempty"`
	Y2        float64     `json:"y2,omitempty"`
	From      string      `json:"from,omitempty"`
	To        string      `json:"to,omitempty"`
	Via       [][]float64 `json:"via,omitempty"`
	Name      string      `json:"name,omitempty"`
}

func (e *Env) handleModifySchematic(_ context.Context, _ *mcp.CallToolRequest, input modifySchematicInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	if input.Action == "create_schematic" {
		return e.handleCreateSchematic(input.SchematicPath)
	}

	if input.Action == "batch" {
		if len(input.Operations) == 0 {
			return toolText("error: batch requires a non-empty operations list"), nil, nil
		}
		data, err := os.ReadFile(input.SchematicPath)
		if err != nil {
			return toolText(fmt.Sprintf("error reading schematic: %v — use action 'create_schematic' first", err)), nil, nil
		}
		sch, err := sexp.ParseSchematic(string(data))
		if err != nil {
			return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
		}
		var results []string
		stopped := false
		for i, bop := range input.Operations {
			op := modifySchematicInput{
				SchematicPath: input.SchematicPath,
				Action:        bop.Action,
				LibID:         bop.LibID,
				Reference:     bop.Reference,
				Value:         bop.Value,
				MountType:     bop.MountType,
				Footprint:     bop.Footprint,
				Unit:          bop.Unit,
				AutoPlace:     bop.AutoPlace,
				Rotation:      bop.Rotation,
				X:             bop.X,
				Y:             bop.Y,
				X2:            bop.X2,
				Y2:            bop.Y2,
				From:          bop.From,
				To:            bop.To,
				Via:           bop.Via,
				Name:          bop.Name,
			}
			msg, ok := e.applyOp(sch, op, true)
			results = append(results, fmt.Sprintf("[%d/%d %s] %s", i+1, len(input.Operations), op.Action, msg))
			if !ok {
				stopped = true
				break
			}
		}
		if err := os.WriteFile(input.SchematicPath, []byte(sch.Serialize()), 0o644); err != nil {
			return toolText(fmt.Sprintf("error writing schematic: %v", err)), nil, nil
		}
		out := strings.Join(results, "\n\n")
		if stopped {
			out += "\n\nBATCH STOPPED — fix the error above and retry remaining operations."
		} else {
			out += fmt.Sprintf("\n\nbatch complete: %d operations applied in one write.", len(input.Operations))
		}
		return toolText(out), nil, nil
	}

	data, err := os.ReadFile(input.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v — use action 'create_schematic' first", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}

	msg, ok := e.applyOp(sch, input, false)
	if !ok {
		// Error or gate — return message without writing.
		return toolText(msg), nil, nil
	}
	if err := os.WriteFile(input.SchematicPath, []byte(sch.Serialize()), 0o644); err != nil {
		return toolText(fmt.Sprintf("error writing schematic: %v", err)), nil, nil
	}
	return toolText(msg), nil, nil
}

// applyOp executes one schematic mutation on an already-loaded AST without file I/O.
// Returns (message, true) on success, (errorMsg, false) on failure.
// inBatch suppresses the interactive placement-mode gate.
func (e *Env) applyOp(sch *sexp.Schematic, op modifySchematicInput, inBatch bool) (string, bool) {
	switch op.Action {
	case "add_symbol":
		if op.LibID == "" || op.Reference == "" {
			return "error: lib_id and reference are required for add_symbol", false
		}
		isPower := strings.HasPrefix(op.LibID, "power:") ||
			op.LibID == "Device:PWR_FLAG" ||
			strings.HasPrefix(op.LibID, "Simulation_SPICE:")
		var fp string
		inBom, onBoard := true, true
		if isPower {
			inBom, onBoard = false, false
		} else {
			mt := strings.ToUpper(op.MountType)
			if mt == "" {
				mt = "THT" // default — most LLM-generated designs are through-hole
			}
			if mt != "THT" && mt != "SMD" {
				return "error: mount_type must be 'THT' or 'SMD' (got " + op.MountType + ")", false
			}
			fp = op.Footprint
			if fp == "" {
				fp = parts.SuggestFootprint(op.LibID, mt)
			}
		}
		unitN := op.Unit
		if unitN <= 0 {
			unitN = 1
		}

		existing := sexp.ReadSymbols(sch)
		// Placement mode gate — suppressed in batch (caller already chose a mode).
		if !inBatch && !isPower && !op.AutoPlace && op.X == 0 && op.Y == 0 &&
			countNonPowerSymbols(existing) == 0 {
			return "error: provide x/y for manual placement OR set auto_place=true (server picks a free grid position)", false
		}

		placeX, placeY := op.X, op.Y
		var autoNote string
		if op.AutoPlace {
			placeX, placeY = autoPlacePosition(existing)
			// Auto-resolve overlaps with ALL existing symbols (including power) so
			// consecutive auto-placed power symbols don't stack on the same position.
			const autoOverlap = 5.0
			for hasSymbolNear(existing, sexp.SnapGrid(placeX), sexp.SnapGrid(placeY), autoOverlap) {
				placeX += 10.0
			}
			autoNote = fmt.Sprintf("auto_place: positioned at (%.2f, %.2f)\n", placeX, placeY)
			if isPower {
				autoNote += "NOTE: power symbol auto-placed — move it onto the target pin after reading symbol positions.\n"
			}
		}

		snappedX := sexp.SnapGrid(placeX)
		snappedY := sexp.SnapGrid(placeY)
		var snapNote string
		const eps = 0.001
		if math.Abs(snappedX-placeX) > eps || math.Abs(snappedY-placeY) > eps {
			snapNote = fmt.Sprintf("NOTE: coordinates snapped from (%.3f,%.3f) to (%.3f,%.3f) — use snapped coords for wires.\n",
				placeX, placeY, snappedX, snappedY)
		}

		const overlapThreshold = 5.0
		var overlapNote string
		for _, sym := range existing {
			dx := sym.X - snappedX
			dy := sym.Y - snappedY
			if math.Sqrt(dx*dx+dy*dy) < overlapThreshold {
				sugX, sugY := suggestFreePos(existing, snappedX, snappedY, overlapThreshold)
				overlapNote = fmt.Sprintf("WARNING: %s is only %.1fmm away — symbols will overlap. Suggested free position: x=%.2f, y=%.2f\n",
					sym.Reference, math.Sqrt(dx*dx+dy*dy), sugX, sugY)
				break
			}
		}

		if err := e.embedLibSymbol(sch, op.LibID); err != nil {
			return fmt.Sprintf("error: could not load symbol %q from KiCad library: %v\n"+
				"Check lib_id is correct (use check_component_existence to search).", op.LibID, err), false
		}
		pinNums := extractPinNumbers(sch, op.LibID, unitN)
		libDef := sch.FindLibDef(op.LibID)
		sch.AddSymbol(sexp.NewSymbolInstance(op.LibID, op.Reference, op.Value, fp,
			placeX, placeY, op.Rotation, unitN, pinNums, sch.UUID(), inBom, onBoard, libDef))
		if inBom && onBoard {
			sexp.FixLabelPositions(sch, op.Reference)
		}
		return autoNote + snapNote + overlapNote + symbolAddedMsg(sch, op.Reference, unitN), true

	case "connect_pins":
		if op.From == "" || op.To == "" {
			return "error: from and to are required for connect_pins (e.g. from=BT1.+ to=R1.1)", false
		}
		wires, routeNote, err := connectPins(sch, op.From, op.To, op.Via)
		if err != nil {
			return fmt.Sprintf("error: %v", err), false
		}
		for _, w := range wires {
			sch.AddWire(w)
		}
		msg := fmt.Sprintf("connected %s → %s (%d wire segment(s))", op.From, op.To, len(wires))
		if routeNote != "" {
			msg += "\n" + routeNote
		}
		return msg, true

	case "add_wire":
		sch.AddWire(sexp.NewWire(op.X, op.Y, op.X2, op.Y2))
		return "wire added", true

	case "disconnect_pin":
		if op.From == "" {
			return "error: from is required for disconnect_pin (e.g. from=R1.1)", false
		}
		px, py, ok := sexp.FindPinPosition(sch, op.From)
		if !ok {
			return fmt.Sprintf("error: pin %q not found (use read_schematic to list pins)", op.From), false
		}
		n := sch.DisconnectPin(px, py)
		if n == 0 {
			return fmt.Sprintf("disconnect_pin %s: no wires or no_connect markers found at (%.2f,%.2f) — pin may already be disconnected", op.From, px, py), true
		}
		return fmt.Sprintf("disconnect_pin %s: removed %d wire/no_connect element(s) at (%.2f,%.2f)", op.From, n, px, py), true

	case "no_connect":
		nx, ny := op.X, op.Y
		if op.From != "" {
			px, py, ok := sexp.FindPinPosition(sch, op.From)
			if !ok {
				return fmt.Sprintf("error: pin %q not found (use read_schematic to list pins)", op.From), false
			}
			nx, ny = px, py
		}
		sch.AddNoConnect(sexp.NewNoConnect(nx, ny))
		return fmt.Sprintf("no_connect added at %.2f,%.2f", nx, ny), true

	case "junction":
		if op.X == 0 && op.Y == 0 {
			return "error: x and y are required for junction", false
		}
		sch.AddJunction(sexp.NewJunction(op.X, op.Y))
		return fmt.Sprintf("junction added at %.2f,%.2f", op.X, op.Y), true

	case "add_label":
		if op.Name == "" {
			return "error: name is required for add_label", false
		}
		sch.AddLabel(sexp.NewNetLabel(op.Name, op.X, op.Y, op.Rotation))
		return fmt.Sprintf("label %q added at %.2f,%.2f", op.Name, op.X, op.Y), true

	case "add_power_rail":
		if op.LibID == "" {
			return "error: lib_id is required for add_power_rail (e.g. power:GND, power:VCC)", false
		}
		if op.From == "" {
			return "error: pin is required for add_power_rail (e.g. from=U1.VCC)", false
		}
		em := e.NewPowerEmitter(sch)
		msg, ok, _ := em.Emit(op.LibID, op.From)
		return "add_power_rail: " + msg, ok

	default:
		return fmt.Sprintf("unknown action %q — valid actions: create_schematic, add_symbol, add_power_rail, connect_pins, disconnect_pin, add_wire, no_connect, junction, add_label, batch", op.Action), false
	}
}

// connectPins resolves pin positions and returns wire nodes connecting from→via…→to.
// It uses smart L-routing: tries both elbow orientations and picks the one that
// avoids cutting through symbol bounding boxes. Returns a note if routing was
// adjusted or if collisions remain after best-effort avoidance.
func connectPins(sch *sexp.Schematic, from, to string, via [][]float64) ([]*sexp.Node, string, error) {
	x1, y1, ok1 := sexp.FindPinPosition(sch, from)
	if !ok1 {
		return nil, "", fmt.Errorf("pin %q not found — check reference and pin name/number (use read_schematic to list pins)", from)
	}
	x2, y2, ok2 := sexp.FindPinPosition(sch, to)
	if !ok2 {
		return nil, "", fmt.Errorf("pin %q not found — check reference and pin name/number (use read_schematic to list pins)", to)
	}

	// Snap all routing endpoints to the 1.27mm grid. Pin positions come from round2
	// (2 decimal places) which may differ slightly from snapGrid. Snapping here
	// ensures the orthogonality check in smartLWire uses grid-aligned values,
	// preventing diagonal segments when two "equal" coords snap to different grid lines.
	x1, y1 = sexp.SnapGrid(x1), sexp.SnapGrid(y1)
	x2, y2 = sexp.SnapGrid(x2), sexp.SnapGrid(y2)

	syms := sexp.ReadSymbols(sch)
	fromRef := strings.SplitN(from, ".", 2)[0]
	toRef := strings.SplitN(to, ".", 2)[0]

	// Build ordered point list: from → via… → to.
	points := [][2]float64{{x1, y1}}
	for _, v := range via {
		if len(v) >= 2 {
			points = append(points, [2]float64{sexp.SnapGrid(v[0]), sexp.SnapGrid(v[1])})
		}
	}
	points = append(points, [2]float64{x2, y2})

	var wires []*sexp.Node
	var notes []string
	for i := 0; i < len(points)-1; i++ {
		// Per-segment skip: only skip source at the first segment, destination at the last.
		seg := map[string]bool{}
		if i == 0 {
			seg[fromRef] = true
		}
		if i == len(points)-2 {
			seg[toRef] = true
		}
		segs, note := smartLWire(points[i][0], points[i][1], points[i+1][0], points[i+1][1], syms, seg)
		wires = append(wires, segs...)
		if note != "" {
			notes = append(notes, note)
		}
	}

	// Check new wire segments against existing wires for crossings.
	existingWires := sch.Wires()
	var crossCount int
	for _, newW := range wires {
		ax, ay, bx, by := wireCoords(newW)
		for _, exW := range existingWires {
			cx, cy, dx, dy := wireCoords(exW)
			if orthoCross(ax, ay, bx, by, cx, cy, dx, dy) {
				crossCount++
			}
		}
	}
	noteStr := strings.Join(notes, "; ")
	if crossCount > 0 {
		crossing := fmt.Sprintf(
			"⚠ CROSSING: %d wire-wire crossing(s) detected."+
				" For long-distance connections, use add_label instead:"+
				" place a label with the same name at each endpoint — no wire needed.",
			crossCount,
		)
		if noteStr != "" {
			noteStr += "\n" + crossing
		} else {
			noteStr = crossing
		}
	}
	return wires, noteStr, nil
}

// smartLWire generates 1 or 2 wire segments, choosing the L-shape orientation
// that avoids symbol bounding boxes. Returns a note if collision-free routing
// was not possible.
func smartLWire(x1, y1, x2, y2 float64, syms []sexp.SchematicSymbol, skipRefs map[string]bool) ([]*sexp.Node, string) {
	const eps = 0.01
	if math.Abs(x1-x2) < eps || math.Abs(y1-y2) < eps {
		return []*sexp.Node{sexp.NewWire(x1, y1, x2, y2)}, ""
	}

	// Score each L-shape by how many symbol boxes the segments cross.
	scoreA := 0 // H-then-V: elbow at (x2, y1)
	if wireHitsSymbol(x1, y1, x2, y1, syms, skipRefs) {
		scoreA++
	}
	if wireHitsSymbol(x2, y1, x2, y2, syms, skipRefs) {
		scoreA++
	}
	scoreB := 0 // V-then-H: elbow at (x1, y2)
	if wireHitsSymbol(x1, y1, x1, y2, syms, skipRefs) {
		scoreB++
	}
	if wireHitsSymbol(x1, y2, x2, y2, syms, skipRefs) {
		scoreB++
	}

	var note string
	if scoreA > 0 && scoreB > 0 {
		// Both L-orientations are blocked — try A* grid routing first.
		rt := router.NewRouter(syms, nil)
		if path := rt.Route(x1, y1, x2, y2); path != nil {
			return pathToWires(path), "routed via A* (avoided obstacle)"
		}
		// A* also failed — suggest escape via points around blocking symbols.
		var escapes []string
		for _, sym := range syms {
			if skipRefs[sym.Reference] {
				continue
			}
			bx1, by1, bx2, by2 := sexp.SymbolBBox(sym)
			margin := 2.54
			candidates := [][2]float64{
				{bx1 - margin, y1},
				{bx2 + margin, y1},
				{x1, by1 - margin},
				{x1, by2 + margin},
			}
			for _, c := range candidates {
				if !wireHitsSymbol(x1, y1, c[0], c[1], syms, skipRefs) {
					escapes = append(escapes, fmt.Sprintf("via=[%.2f,%.2f]", c[0], c[1]))
					break
				}
			}
		}
		if len(escapes) > 0 {
			note = "WARNING: both routes cross a symbol — retry with " + strings.Join(escapes, " or ")
		} else {
			note = fmt.Sprintf("WARNING: wire (%.1f,%.1f)→(%.1f,%.1f) may cross a symbol body — use via waypoints to route around it", x1, y1, x2, y2)
		}
	}

	if scoreB < scoreA {
		// Option B is cleaner: vertical-then-horizontal.
		return []*sexp.Node{
			sexp.NewWire(x1, y1, x1, y2),
			sexp.NewWire(x1, y2, x2, y2),
		}, note
	}
	// Default: horizontal-then-vertical.
	return []*sexp.Node{
		sexp.NewWire(x1, y1, x2, y1),
		sexp.NewWire(x2, y1, x2, y2),
	}, note
}

// wireHitsSymbol returns true if the segment from (ax,ay)→(bx,by) crosses
// the interior of any symbol bounding box not in skipRefs.
func wireHitsSymbol(ax, ay, bx, by float64, syms []sexp.SchematicSymbol, skipRefs map[string]bool) bool {
	for _, sym := range syms {
		if skipRefs[sym.Reference] {
			continue
		}
		rx1, ry1, rx2, ry2 := sexp.SymbolBBox(sym)
		if sexp.SegmentCrossesBox(ax, ay, bx, by, rx1, ry1, rx2, ry2) {
			return true
		}
	}
	return false
}

// wireCoords extracts (x1,y1,x2,y2) from a (wire (pts (xy ...) (xy ...)) ...) node.
func wireCoords(w *sexp.Node) (float64, float64, float64, float64) {
	pts := sexp.FindList(w, "pts")
	if pts == nil {
		return 0, 0, 0, 0
	}
	var coords [2][2]float64
	n := 0
	for _, xy := range pts.Children {
		if xy.Head() != "xy" || n >= 2 {
			continue
		}
		coords[n][0], _ = strconv.ParseFloat(sexp.AtomValue(xy, 1), 64)
		coords[n][1], _ = strconv.ParseFloat(sexp.AtomValue(xy, 2), 64)
		n++
	}
	if n < 2 {
		return 0, 0, 0, 0
	}
	return coords[0][0], coords[0][1], coords[1][0], coords[1][1]
}

// orthoCross returns true when two axis-aligned wire segments cross in their
// strict interiors (not just share an endpoint or overlap).
func orthoCross(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	const eps = 0.01
	// A-B horizontal, C-D vertical
	if math.Abs(ay-by) < eps && math.Abs(cx-dx) < eps {
		hY, vX := ay, cx
		hLo, hHi := math.Min(ax, bx), math.Max(ax, bx)
		vLo, vHi := math.Min(cy, dy), math.Max(cy, dy)
		return vX > hLo && vX < hHi && hY > vLo && hY < vHi
	}
	// A-B vertical, C-D horizontal
	if math.Abs(ax-bx) < eps && math.Abs(cy-dy) < eps {
		vX, hY := ax, cy
		vLo, vHi := math.Min(ay, by), math.Max(ay, by)
		hLo, hHi := math.Min(cx, dx), math.Max(cx, dx)
		return vX > hLo && vX < hHi && hY > vLo && hY < vHi
	}
	return false // parallel or degenerate
}

// suggestFreePos searches for a free position near (x, y) with at least minDist
// separation from all existing symbols, stepping 10mm to the right.
func suggestFreePos(existing []sexp.SchematicSymbol, x, y, minDist float64) (float64, float64) {
	for i := 1; i <= 30; i++ {
		cx := sexp.SnapGrid(x + float64(i)*10.0)
		if !hasSymbolNear(existing, cx, y, minDist) {
			return cx, y
		}
	}
	return sexp.SnapGrid(x + 30.0), y
}

// hasSymbolNear returns true if any symbol is within dist mm of (x, y).
func hasSymbolNear(syms []sexp.SchematicSymbol, x, y, dist float64) bool {
	for _, sym := range syms {
		dx := sym.X - x
		dy := sym.Y - y
		if math.Sqrt(dx*dx+dy*dy) < dist {
			return true
		}
	}
	return false
}

// countNonPowerSymbols returns how many placed symbols are not power/utility symbols.
func countNonPowerSymbols(syms []sexp.SchematicSymbol) int {
	n := 0
	for _, sym := range syms {
		if !strings.HasPrefix(sym.LibID, "power:") && sym.LibID != "Device:PWR_FLAG" {
			n++
		}
	}
	return n
}

// missingUnits returns, for every multi-unit IC reference in the schematic,
// the list of unit numbers that have NOT been placed yet. Returns an empty
// map when every multi-unit IC is fully populated.
func missingUnits(sch *sexp.Schematic, symbols []sexp.SchematicSymbol) map[string][]int {
	type ref struct {
		LibID string
		Units map[int]bool
	}
	byRef := make(map[string]*ref)
	for _, sym := range symbols {
		if strings.HasPrefix(sym.LibID, "power:") || sym.LibID == "Device:PWR_FLAG" {
			continue
		}
		r, ok := byRef[sym.Reference]
		if !ok {
			r = &ref{LibID: sym.LibID, Units: make(map[int]bool)}
			byRef[sym.Reference] = r
		}
		u := sym.Unit
		if u <= 0 {
			u = 1
		}
		r.Units[u] = true
	}
	out := make(map[string][]int)
	for refName, r := range byRef {
		total := countUnitsInLib(sch, r.LibID)
		if total <= 1 {
			continue
		}
		var missing []int
		for u := 1; u <= total; u++ {
			if !r.Units[u] {
				missing = append(missing, u)
			}
		}
		if len(missing) > 0 {
			out[refName] = missing
		}
	}
	return out
}

// parseFloat is a small wrapper used by the AST readers to read
// raw atom values without pulling strconv into every call site.
func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// nearestEndpoint returns the closest point in pts to (x, y) within maxDist mm.
// Result is [x, y, distance]. Returns ok=false if no point is within maxDist
// or if the closest point is exactly at (x, y) (which means the pin IS connected).
func nearestEndpoint(x, y float64, pts [][2]float64, maxDist float64) ([3]float64, bool) {
	best := [3]float64{}
	bestDist := math.MaxFloat64
	for _, pt := range pts {
		dx, dy := pt[0]-x, pt[1]-y
		d := math.Sqrt(dx*dx + dy*dy)
		if d < bestDist {
			bestDist = d
			best = [3]float64{pt[0], pt[1], d}
		}
	}
	if bestDist >= maxDist || bestDist < 0.01 {
		return [3]float64{}, false
	}
	return best, true
}

// autoPlacePosition computes the next grid position for auto_place mode.
// Fills columns of up to 4 symbols (30 mm apart), then starts a new column 50 mm right.
func autoPlacePosition(existing []sexp.SchematicSymbol) (float64, float64) {
	var nonPower []sexp.SchematicSymbol
	for _, sym := range existing {
		if !strings.HasPrefix(sym.LibID, "power:") && sym.LibID != "Device:PWR_FLAG" {
			nonPower = append(nonPower, sym)
		}
	}
	if len(nonPower) == 0 {
		return sexp.SnapGrid(50.8), sexp.SnapGrid(50.8)
	}
	// Find rightmost column X.
	maxX := nonPower[0].X
	for _, s := range nonPower[1:] {
		if s.X > maxX {
			maxX = s.X
		}
	}
	// Collect Y values in that column (within ±25 mm).
	const colHalf = 25.0
	const maxPerCol = 4
	var colY []float64
	for _, s := range nonPower {
		if math.Abs(s.X-maxX) < colHalf {
			colY = append(colY, s.Y)
		}
	}
	if len(colY) < maxPerCol {
		// Still room — place below last in column.
		maxY := colY[0]
		for _, y := range colY[1:] {
			if y > maxY {
				maxY = y
			}
		}
		return sexp.SnapGrid(maxX), sexp.SnapGrid(maxY + 30.0)
	}
	// Start a new column.
	return sexp.SnapGrid(maxX + 50.0), sexp.SnapGrid(50.8)
}

// symbolAddedMsg returns a compact pin-position summary for the just-added symbol.
// If the symbol has multiple units, informs the LLM to place remaining units.
func symbolAddedMsg(sch *sexp.Schematic, ref string, unitN int) string {
	// Find the placed instance matching ref AND unit.
	var found *sexp.SchematicSymbol
	all := sexp.ReadSymbols(sch)
	for i := range all {
		if all[i].Reference == ref && all[i].Unit == unitN {
			found = &all[i]
			break
		}
	}
	if found == nil {
		return fmt.Sprintf("added %s", ref)
	}

	totalUnits := countUnitsInLib(sch, found.LibID)
	var sb strings.Builder
	if totalUnits > 1 {
		fmt.Fprintf(&sb, "added %s (%s) unit %d/%d\n", ref, found.LibID, unitN, totalUnits)
		if unitN < totalUnits {
			fmt.Fprintf(&sb, "NOTE: place unit(s) %d", unitN+1)
			for u := unitN + 2; u <= totalUnits; u++ {
				fmt.Fprintf(&sb, ", %d", u)
			}
			fmt.Fprintf(&sb, " separately with the same reference %s\n", ref)
		}
	} else {
		fmt.Fprintf(&sb, "added %s (%s)\n", ref, found.LibID)
	}
	if len(found.Pins) == 0 {
		sb.WriteString("pins: none resolved (power symbol or no lib definition)")
		return sb.String()
	}
	fmt.Fprintf(&sb, "pins (%d — all UNCONNECTED, connect or no_connect each):\n", len(found.Pins))
	hasAnode, hasCathode := false, false
	for _, p := range found.Pins {
		// When pin name is "~" (unnamed, e.g. both terminals of R/C/L), use the
		// pin number instead so the LLM can distinguish pin 1 from pin 2.
		pinLabel := p.Name
		if pinLabel == "~" {
			pinLabel = p.Number
		}
		if p.Name == "A" {
			hasAnode = true
		}
		if p.Name == "K" {
			hasCathode = true
		}
		var pinRef string
		if totalUnits > 1 {
			pinRef = fmt.Sprintf("%s.%d.%s", ref, unitN, pinLabel)
		} else {
			pinRef = ref + "." + pinLabel
		}
		fmt.Fprintf(&sb, "  pin %-4s (%-10s): %.2f,%.2f  → use from=%s\n",
			p.Number, p.Name, p.X, p.Y, pinRef)
	}
	if hasAnode && hasCathode {
		sb.WriteString("NOTE: rotation=0 places Anode (A) on the RIGHT, Cathode (K) on the LEFT.\n")
		sb.WriteString("For standard left-to-right circuit flow (signal → R → A → K → GND),\n")
		sb.WriteString("use rotation=180 to put Anode on the left.\n")
	}
	return sb.String()
}

// handleCreateSchematic creates a minimal valid .kicad_sch at the given path.
func (e *Env) handleCreateSchematic(schPath string) (*mcp.CallToolResult, any, error) {
	absPath, err := filepath.Abs(schPath)
	if err != nil {
		return toolText(fmt.Sprintf("error resolving path: %v", err)), nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return toolText(fmt.Sprintf("error creating directories: %v", err)), nil, nil
	}
	// KiCad 9 root-sheet sheet_instances always uses path "/" (just a slash).
	// Symbol instances use "/{schematic-uuid}" — that is handled by NewSymbolInstance.
	schUUID := sexp.NewUUID()
	content := fmt.Sprintf(`(kicad_sch (version 20231120) (generator "eeschema") (generator_version "9.0") (paper "A4")
  (uuid "%s")
  (lib_symbols)
  (sheet_instances
    (path "/" (page "1"))))
`, schUUID)
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return toolText(fmt.Sprintf("error writing schematic: %v", err)), nil, nil
	}
	return toolText(fmt.Sprintf("created (fresh): %s", absPath)), nil, nil
}

// extractPinNumbers returns pin numbers for a given unit of libID by looking
// up the embedded lib symbol in the schematic's lib_symbols block.
func extractPinNumbers(sch *sexp.Schematic, libID string, unit int) []string {
	if def := libSymbolDef(sch, libID); def != nil {
		return sexp.ExtractPinNumbers(def, unit)
	}
	return nil
}

// libSymbolDef returns the embedded lib symbol definition node for libID,
// or nil if not present in the schematic's lib_symbols block.
func libSymbolDef(sch *sexp.Schematic, libID string) *sexp.Node {
	ls := sch.LibSymbols()
	if ls == nil {
		return nil
	}
	for _, child := range ls.Children {
		if child.Head() == "symbol" && sexp.StringValue(child, 1) == libID {
			return child
		}
	}
	return nil
}

// countUnitsInLib returns how many units libID has, by looking at the embedded
// lib symbol definition. Returns 1 if not found or single-unit.
func countUnitsInLib(sch *sexp.Schematic, libID string) int {
	ls := sch.LibSymbols()
	if ls == nil {
		return 1
	}
	for _, child := range ls.Children {
		if child.Head() == "symbol" && sexp.StringValue(child, 1) == libID {
			return sexp.CountUnits(child)
		}
	}
	return 1
}

// embedLibSymbol embeds the KiCad global symbol definition into lib_symbols.
// If the symbol uses (extends "Parent"), the parent is embedded first.
func (e *Env) embedLibSymbol(sch *sexp.Schematic, libID string) error {
	if e.KicadSymbols == "" {
		return fmt.Errorf("KicadSymbols path not configured")
	}
	if sch.HasLibSymbol(libID) {
		return nil // already embedded
	}
	ps := strings.SplitN(libID, ":", 2)
	if len(ps) != 2 {
		return fmt.Errorf("invalid lib_id: %q", libID)
	}
	libName, partName := ps[0], ps[1]
	symFile := filepath.Join(e.KicadSymbols, libName+".kicad_sym")
	// ExtractSymbolDefWithParents returns [grandparent, parent, ..., symbol]
	// so embedding in order is safe.
	defs, err := sexp.ExtractSymbolDefWithParents(symFile, libName, partName)
	if err != nil {
		return err
	}
	for _, def := range defs {
		qualID := sexp.StringValue(def, 1)
		if !sch.HasLibSymbol(qualID) {
			sch.AddLibSymbol(def)
		}
	}
	return nil
}

// --- read_schematic tool ---

type readSchematicInput struct {
	SchematicPath string `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
}

func (e *Env) handleReadSchematic(_ context.Context, _ *mcp.CallToolRequest, input readSchematicInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	absPath, err := filepath.Abs(input.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error: %v", err)), nil, nil
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}

	symbols := sexp.ReadSymbols(sch)
	if len(symbols) == 0 {
		return toolText("schematic is empty — use add_symbol to place components"), nil, nil
	}

	connSet := sexp.ConnectedPins(sch)
	ncSet := sexp.NoConnectPointSet(sch)

	pinStatus := func(x, y float64) string {
		key := [2]float64{math.Round(x*100) / 100, math.Round(y*100) / 100}
		if connSet[key] {
			return "[connected]"
		}
		if ncSet[key] {
			return "[no_connect]"
		}
		return "[UNCONNECTED]"
	}

	totalPins, connectedPins, noConnPins, unconnPins := 0, 0, 0, 0
	for _, sym := range symbols {
		for _, p := range sym.Pins {
			totalPins++
			switch pinStatus(p.X, p.Y) {
			case "[connected]":
				connectedPins++
			case "[no_connect]":
				noConnPins++
			default:
				unconnPins++
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(asciiLayout(symbols))
	fmt.Fprintf(&sb, "\nConnectivity: %d/%d pins connected, %d no_connect, %d UNCONNECTED\n",
		connectedPins, totalPins, noConnPins, unconnPins)
	if unconnPins > 0 {
		sb.WriteString("ACTION REQUIRED: connect or no_connect all UNCONNECTED pins before export.\n")
	}
	sb.WriteString("\nSymbols (ref  lib_id  value  @ x,y):\n")
	for _, sym := range symbols {
		totalUnits := countUnitsInLib(sch, sym.LibID)
		fmt.Fprintf(&sb, "  %-6s %-30s %-10s @ %.1f,%.1f\n", sym.Reference, sym.LibID, sym.Value, sym.X, sym.Y)
		for _, p := range sym.Pins {
			status := pinStatus(p.X, p.Y)
			pinLabel := p.Name
			if pinLabel == "~" {
				pinLabel = p.Number
			}
			var pinRef string
			if totalUnits > 1 && sym.Unit > 0 {
				pinRef = fmt.Sprintf("%s.%d.%s", sym.Reference, sym.Unit, pinLabel)
			} else {
				pinRef = sym.Reference + "." + pinLabel
			}
			fmt.Fprintf(&sb, "         pin %-4s (%-6s): %.2f,%.2f  %-3s  %s  [%s]\n",
				p.Number, p.Name, p.X, p.Y, directionName(p.Direction), status, pinRef)
		}
	}
	if missing := missingUnits(sch, symbols); len(missing) > 0 {
		sb.WriteString("\n⚠ MULTI-UNIT IC: missing units must be placed (same reference, different unit number):\n")
		for ref, units := range missing {
			parts := make([]string, len(units))
			for i, u := range units {
				parts[i] = strconv.Itoa(u)
			}
			fmt.Fprintf(&sb, "  %s: missing unit(s) %s — call add_symbol with reference=%s, unit=%s\n",
				ref, strings.Join(parts, ", "), ref, parts[0])
		}
	}

	sb.WriteString("\nTo connect: connect_pins from=REF.pinRef to=REF.pinRef")
	sb.WriteString("\n  Single-unit: from=R1.1  Multi-unit IC: from=U1.1.+ (REF.unit.pin)")
	sb.WriteString("\nTo mark unused pins: no_connect from=REF.pinRef")
	return toolText(strings.TrimRight(sb.String(), "\n")), nil, nil
}

func directionName(d float64) string {
	switch int(math.Round(d)) % 360 {
	case 0:
		return "→E"
	case 90:
		return "→N"
	case 180:
		return "→W"
	case 270:
		return "→S"
	}
	return "→?"
}

// asciiLayout renders a compact ASCII grid showing where symbols are placed.
// Each cell represents ~10 mm. Grid is auto-sized to content (max 16×10 cells).
func asciiLayout(symbols []sexp.SchematicSymbol) string {
	if len(symbols) == 0 {
		return ""
	}

	const cellMM = 10.0
	const maxCols = 16
	const maxRows = 10

	// Find bounding box.
	minX, minY := symbols[0].X, symbols[0].Y
	maxX, maxY := minX, minY
	for _, s := range symbols {
		if s.X < minX {
			minX = s.X
		}
		if s.Y < minY {
			minY = s.Y
		}
		if s.X > maxX {
			maxX = s.X
		}
		if s.Y > maxY {
			maxY = s.Y
		}
	}

	// Grid dimensions (at least 1 cell, capped).
	cols := int((maxX-minX)/cellMM) + 2
	rows := int((maxY-minY)/cellMM) + 2
	if cols > maxCols {
		cols = maxCols
	}
	if rows > maxRows {
		rows = maxRows
	}

	// Place symbols in grid cells (trim label to 4 chars).
	type cell struct{ label string }
	grid := make([][]cell, rows)
	for i := range grid {
		grid[i] = make([]cell, cols)
	}
	for _, s := range symbols {
		col := int((s.X - minX) / cellMM)
		row := int((s.Y - minY) / cellMM)
		if col >= cols {
			col = cols - 1
		}
		if row >= rows {
			row = rows - 1
		}
		lbl := s.Reference
		if len(lbl) > 4 {
			lbl = lbl[:4]
		}
		grid[row][col].label = lbl
	}

	// Render.
	var sb strings.Builder
	sb.WriteString("Layout (each cell ≈ 10mm):\n")
	// Column header.
	sb.WriteString("     ")
	for c := 0; c < cols; c++ {
		x := minX + float64(c)*cellMM
		sb.WriteString(fmt.Sprintf("%-6.0f", x))
	}
	sb.WriteString("\n")
	for r := 0; r < rows; r++ {
		y := minY + float64(r)*cellMM
		sb.WriteString(fmt.Sprintf("%4.0f ", y))
		for c := 0; c < cols; c++ {
			lbl := grid[r][c].label
			if lbl == "" {
				sb.WriteString("·     ")
			} else {
				sb.WriteString(fmt.Sprintf("%-6s", lbl))
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// handleGetConnectivitySummary returns a full pin connectivity audit plus net list.
func (e *Env) handleGetConnectivitySummary(_ context.Context, _ *mcp.CallToolRequest, input readSchematicInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	absPath, err := filepath.Abs(input.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error: %v", err)), nil, nil
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}

	symbols := sexp.ReadSymbols(sch)
	if len(symbols) == 0 {
		return toolText("schematic is empty"), nil, nil
	}

	connSet := sexp.ConnectedPins(sch) // includes wires, labels, AND power-symbol implicit nets
	ncSet := sexp.NoConnectPointSet(sch)
	endpoints := sexp.WireEndpoints(sch)

	type nearMiss struct {
		pin      string
		px, py   float64
		ex, ey   float64
		distance float64
	}

	totalPins, connectedPins, noConnPins := 0, 0, 0
	var unconnected []string
	var nearMisses []nearMiss
	for _, sym := range symbols {
		for _, p := range sym.Pins {
			key := [2]float64{math.Round(p.X*100) / 100, math.Round(p.Y*100) / 100}
			totalPins++
			if connSet[key] {
				connectedPins++
				continue
			}
			if ncSet[key] {
				noConnPins++
				continue
			}
			ref := sexp.PinRef{
				Reference: sym.Reference,
				PinNumber: p.Number,
				PinName:   p.Name,
			}.String()
			unconnected = append(unconnected, ref)
			if nm, ok := nearestEndpoint(p.X, p.Y, endpoints, 2.54); ok {
				nearMisses = append(nearMisses, nearMiss{
					pin: ref, px: p.X, py: p.Y,
					ex: nm[0], ey: nm[1],
					distance: nm[2],
				})
			}
		}
	}

	nets := sexp.TraceNets(sch)
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Dangling != nets[j].Dangling {
			return nets[i].Dangling // dangling first (require attention)
		}
		return nets[i].Name < nets[j].Name
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Connectivity summary: %d total | %d connected | %d no_connect | %d UNCONNECTED\n",
		totalPins, connectedPins, noConnPins, len(unconnected))

	if len(unconnected) > 0 {
		sb.WriteString("\nUNCONNECTED pins (need connect_pins or no_connect):\n")
		for _, u := range unconnected {
			fmt.Fprintf(&sb, "  %s\n", u)
		}
	}

	if len(nearMisses) > 0 {
		fmt.Fprintf(&sb, "\n⚠ NEAR-MISS (%d): pin is within 2.54 mm of a wire/label endpoint but not exactly on it.\n", len(nearMisses))
		sb.WriteString("Likely an LLM coordinate mistake. Fix with disconnect_pin (the wire endpoint) then connect_pins to the actual pin position.\n")
		for _, nm := range nearMisses {
			fmt.Fprintf(&sb, "  %s @ (%.2f, %.2f) — endpoint at (%.2f, %.2f), %.2f mm away\n",
				nm.pin, nm.px, nm.py, nm.ex, nm.ey, nm.distance)
		}
	}

	fmt.Fprintf(&sb, "\nNets (%d):\n", len(nets))
	for _, net := range nets {
		marker := "  "
		if net.Dangling {
			marker = "⚠ "
		}
		pinStrs := make([]string, len(net.Pins))
		for i, p := range net.Pins {
			pinStrs[i] = p.String()
		}
		fmt.Fprintf(&sb, "%s%-22s %d pin(s): %s\n",
			marker, net.Name, len(net.Pins), strings.Join(pinStrs, ", "))
	}

	if missing := missingUnits(sch, symbols); len(missing) > 0 {
		sb.WriteString("\n⚠ MULTI-UNIT IC: missing units (same reference, different unit number):\n")
		for ref, units := range missing {
			parts := make([]string, len(units))
			for i, u := range units {
				parts[i] = strconv.Itoa(u)
			}
			fmt.Fprintf(&sb, "  %s: missing unit(s) %s\n", ref, strings.Join(parts, ", "))
		}
	}

	// Electrical sanity checks across nets.
	shorts, conflicts := electricalIssues(nets)
	if len(shorts) > 0 {
		sb.WriteString("\n❌ POWER SHORT(S) — GND and a positive/negative supply share a net:\n")
		for _, msg := range shorts {
			fmt.Fprintf(&sb, "  %s\n", msg)
		}
	}
	if len(conflicts) > 0 {
		sb.WriteString("\n❌ DRIVER CONFLICT(S) — multiple output pins on the same net:\n")
		for _, msg := range conflicts {
			fmt.Fprintf(&sb, "  %s\n", msg)
		}
	}

	if len(unconnected) == 0 && totalPins > 0 && len(shorts) == 0 && len(conflicts) == 0 {
		sb.WriteString("\n✓ All pins connected or marked no_connect; no shorts or driver conflicts. Ready for ERC/export.")
	}
	return toolText(strings.TrimRight(sb.String(), "\n")), nil, nil
}

// electricalIssues scans the netlist for two common error classes that ERC
// also catches but only after the schematic is exportable:
//
//   - power short: a GND-style supply (power:GND, power:GNDA, ...) connected
//     to any positive/negative supply (power:VCC, power:+5V, power:-12V) on
//     the same net.
//   - driver conflict: two or more pins of electrical type "output" or
//     "power_out" sharing a net.
//
// Returns human-readable descriptions for each issue.
func electricalIssues(nets []sexp.Net) (shorts, conflicts []string) {
	for _, net := range nets {
		var grounds, supplies []string
		var outputs []string
		for _, p := range net.Pins {
			if strings.HasPrefix(p.LibID, "power:") {
				partName := strings.TrimPrefix(p.LibID, "power:")
				if isGroundSymbol(partName) {
					grounds = append(grounds, p.Reference+" ("+partName+")")
				} else {
					supplies = append(supplies, p.Reference+" ("+partName+")")
				}
			}
			if p.Electrical == "output" || p.Electrical == "power_out" {
				outputs = append(outputs, p.String())
			}
		}
		if len(grounds) > 0 && len(supplies) > 0 {
			shorts = append(shorts, fmt.Sprintf("net %q: GND %v shorted to supply %v",
				net.Name, grounds, supplies))
		}
		if len(outputs) > 1 {
			conflicts = append(conflicts, fmt.Sprintf("net %q: %d driver pins → %v",
				net.Name, len(outputs), outputs))
		}
	}
	return shorts, conflicts
}

// isGroundSymbol returns true for power-symbol part names that represent
// ground (return path), so the short detector can flag GND↔supply collisions.
func isGroundSymbol(partName string) bool {
	up := strings.ToUpper(partName)
	switch up {
	case "GND", "GND1", "GND2", "GND3", "GNDA", "GNDD", "GNDPWR", "GNDREF", "GNDS", "EARTH", "0V":
		return true
	}
	return false
}

// RegisterSchematicTools registers schematic editing tools on the server.
//
// Each editing action is its own MCP tool with a focused input schema and a
// short description. The LLM picks an action by tool name, not by an "action"
// switch field — this avoids hauling around input fields it doesn't need and
// keeps each schema small enough to internalize.
//
// Workflow tips live in kicad_workflow_help; the per-tool descriptions stay
// short on purpose so the LLM doesn't skim past them.
func RegisterSchematicTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_schematic",
		Description: "Create an empty .kicad_sch file at schematic_path. Always the first call.",
	}, WrapTool(env.Log, "create_schematic", env.handleCreateSchematicTool))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_symbol",
		Description: "Place a component on the schematic. Returns pin positions for the placed instance — use those coords with connect_pins, never compute pin coords manually. For multi-unit ICs (NE5532, LM358…) call once per unit with the same reference.",
	}, WrapTool(env.Log, "add_symbol", env.handleAddSymbol))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_power_rail",
		Description: "Place a power symbol (power:GND, power:VCC, power:+5V…) directly on a target pin in one call. Auto-generates the #PWR reference; no wire needed.",
	}, WrapTool(env.Log, "add_power_rail", env.handleAddPowerRail))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "connect_pins",
		Description: "Draw an L-shaped wire between two pins (REF.pin → REF.pin). Smart routing avoids crossing other symbol bodies. Use add_label or connect_netlist instead for distant pins.",
	}, WrapTool(env.Log, "connect_pins", env.handleConnectPinsTool))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "disconnect_pin",
		Description: "Remove all wires and no_connect markers touching a pin endpoint. Use to undo a bad connection before re-routing.",
	}, WrapTool(env.Log, "disconnect_pin", env.handleDisconnectPin))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_wire",
		Description: "Manual wire from (x,y) to (x2,y2). Prefer connect_pins when both endpoints are pin positions.",
	}, WrapTool(env.Log, "add_wire", env.handleAddWire))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "no_connect",
		Description: "Mark a pin as intentionally unconnected so ERC stops complaining. Provide either from=REF.pin or explicit x,y.",
	}, WrapTool(env.Log, "no_connect", env.handleNoConnectTool))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "junction",
		Description: "Solder dot at a T-intersection of three or more wires. Required for KiCad to treat the T as connected.",
	}, WrapTool(env.Log, "junction", env.handleJunctionTool))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_label",
		Description: "Net label. Two labels with the same name are electrically connected — use instead of long wires for distant pins.",
	}, WrapTool(env.Log, "add_label", env.handleAddLabelTool))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "batch_schematic",
		Description: "Run multiple actions atomically in one file write. Stops on the first error. Use for adding many symbols at once or scripted setups.",
	}, WrapTool(env.Log, "batch_schematic", env.handleBatchSchematic))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_schematic",
		Description: "List placed symbols, their pin positions, and connectivity status. Use before connect_pins so you know exact pin coordinates.",
	}, WrapTool(env.Log, "read_schematic", env.handleReadSchematic))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_connectivity_summary",
		Description: "Connectivity audit: total pins, connected, unconnected, no_connect, near-misses, multi-unit gaps, full net list. Faster than ERC.",
	}, WrapTool(env.Log, "get_connectivity_summary", env.handleGetConnectivitySummary))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "kicad_workflow_help",
		Description: "Read once at the start of a session. Returns the recommended workflow: write a .design.json source → compile_schematic → inspect PNG/ERC → iterate on the source.",
	}, WrapTool(env.Log, "kicad_workflow_help", env.handleWorkflowHelp))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "cluster_components",
		Description: "Read-only — list functional clusters detected in the current schematic (decoupling caps near IC, I²C pull-ups, LC filters, crystal+load caps, voltage dividers, op-amp feedback, header neighbours). Useful to understand which symbols the compiler wires from closed-form geometry.",
	}, WrapTool(env.Log, "cluster_components", env.handleClusterComponents))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_templates",
		Description: "List the canonical sub-circuit templates available (op-amp non-inverting, voltage divider, MCU minimal, LM7805 regulator, I²C pull-ups). Each entry includes the external pins that the surrounding circuit must wire.",
	}, WrapTool(env.Log, "list_templates", env.handleListTemplates))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "layout_metrics",
		Description: "Read-only — compute layout-quality metrics (bend count, crossings, wires-through-symbol, total wire length, density). Use to track regression vs baseline after editing a design source.",
	}, WrapTool(env.Log, "layout_metrics", env.handleLayoutMetrics))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "apply_template",
		Description: "Stamp a canonical sub-circuit template (see list_templates) into the schematic at (anchor_x, anchor_y). Pin_map ties role-qualified template pins to external net names so the surrounding circuit picks them up by label. Auto-allocates references unless ref_map is supplied.",
	}, WrapTool(env.Log, "apply_template", env.handleApplyTemplate))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_design_context",
		Description: "Returns full spatial + connectivity + direction context for a schematic in one call. Includes all component positions, all pin directions (→N/S/E/W), full netlist, and geometric problems (wrong-direction wires, crossings, near-misses). Use this before routing to understand the complete picture without multiple round trips.",
	}, WrapTool(env.Log, "get_design_context", env.handleGetDesignContext))
}

// handleWorkflowHelp returns the recommended end-to-end workflow as plain text.
// Centralizing the workflow advice keeps each per-tool description focused on
// what the tool does rather than how to chain them.
func (e *Env) handleWorkflowHelp(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	return toolText(`KiCad MCP — recommended workflow

1. Write a .design.json source (blocks, pin-anchored placement, nets). The format
   is documented in docs/compiler/FORMAT.md; docs/compiler/*.design.json are the
   canonical worked examples.
2. compile_schematic(design_path) — one call produces the .kicad_sch with wiring,
   power symbols, no_connects, ERC and a rendered preview.
3. Inspect the returned PNG and the ERC section of the report.
4. Iterate on the SOURCE and recompile. The .kicad_sch is a build artifact —
   never hand-edit it, your changes are lost on the next compile.

The per-action tools below (create_schematic, add_symbol, connect_pins, …) are
the low-level surface. Use them to inspect or patch an existing schematic that
has no design source, not to author a new one.

Common LLM mistakes:
• Editing the generated .kicad_sch instead of the .design.json source.
• Computing pin coords by hand. Always use the coords printed by add_symbol /
  read_schematic; the resistor pin tip is at body_y ± 3.81 mm, not body_y.
• Forgetting multi-unit ICs. NE5532 has units A,B,C — one entry per unit.
• Skipping no_connect for unused IC pins — every unconnected pin is an ERC error.`), nil, nil
}
