package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/place2/wiregen"
	"mcp-kicad/internal/router"
	"mcp-kicad/internal/sexp"
)

// connectNetlistInput defines the parameters for the connect_netlist tool.
type connectNetlistInput struct {
	SchematicPath string    `json:"schematic_path"         jsonschema:"Path to the .kicad_sch file"`
	Connections   []NetConn `json:"connections"            jsonschema:"List of nets to connect"`
	Strategy      string    `json:"strategy,omitempty"     jsonschema:"wire | label | auto (default: auto)"`
}

// NetConn describes one electrical net and the pins that belong to it.
type NetConn struct {
	Net  string   `json:"net"  jsonschema:"Net name, used as label text when the label strategy is chosen"`
	Pins []string `json:"pins" jsonschema:"Pin refs in REF.PIN format, e.g. [U1.VCC, R1.1, C1.+]"`
}

func (e *Env) handleConnectNetlist(_ context.Context, _ *mcp.CallToolRequest, input connectNetlistInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	if len(input.Connections) == 0 {
		return toolText("error: connections list is empty"), nil, nil
	}
	strategy := strings.ToLower(strings.TrimSpace(input.Strategy))
	if strategy == "" {
		strategy = "auto"
	}
	if strategy != "wire" && strategy != "label" && strategy != "auto" {
		return toolText(fmt.Sprintf("error: strategy must be 'wire', 'label', or 'auto' (got %q)", input.Strategy)), nil, nil
	}

	data, err := os.ReadFile(input.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}

	// Remember the user's original pin ordering — relayout uses it as a
	// signal-flow hint when picking DAG sources, instead of falling back to
	// schematic add-order which has no semantic meaning.
	rememberNetlistIntent(input.SchematicPath, input.Connections)

	var sb strings.Builder
	fmt.Fprintf(&sb, "connect_netlist: %d nets\n", len(input.Connections))

	// Phase 3: parametric cluster wiring. Before the A* router runs, wire the
	// pins of recognised functional clusters (decoupling, pull-ups, series
	// LEDs, dividers, crystal load caps) from closed-form geometry. Pins joined
	// here are removed from the router's work via compForNet; the geometric
	// gate still runs afterwards as the final safety net. Repositioning may move
	// satellite symbols, so the router obstacle grid is built AFTER this pass.
	//
	// NB: this runs only in connect_netlist, not in relayout. relayout shares
	// routeNets but its downstream rotation-optimizer (relayout_optimize.go)
	// strips every wire and re-routes independently via route2, which would
	// both discard the formula wires and compound any satellite moves — so
	// relayout passes a nil component map and behaves exactly as before.
	var compForNet map[string]map[string]int
	wiregenWires := 0
	if strategy != "label" {
		wNets, sNets := buildWiregenInputs(sch, input.Connections)
		clusters := cluster.Detect(sexp.ReadSymbols(sch), sNets)
		// allowMoves=false: a repositioned satellite would survive into the
		// downstream relayout and perturb its placement, so the pipeline only
		// wires clusters that are already adjacent.
		res := wiregen.ApplyOpts(sch, clusters, wNets, false)
		if !res.Empty() {
			compForNet = buildCompForNet(res.Pairs)
			wiregenWires = len(res.Wires)
			fmt.Fprintf(&sb, "  %s\n", res.ReportLine())
		}
	}

	// Build the router AFTER wiregen so its obstacle grid reflects moved
	// symbols and already-placed formula wires (soft obstacles).
	rt := router.NewRouter(sexp.ReadSymbols(sch), sch.Wires())

	totalWires, totalLabels, totalErrors := e.routeNets(sch, rt, input.Connections, strategy, compForNet, &sb)
	totalWires += wiregenWires

	// Geometric quality gate (Phase 1): demote any net whose wiring crosses
	// another net, crosses itself without a junction, cuts through a symbol
	// body, or overlaps another net's wire collinearly. Demoted nets keep
	// their exact connectivity via net labels instead of wires, which have
	// no geometry and therefore cannot violate anything.
	gateResult := gate.Enforce(sch)

	// ERC discipline: give every undriven power-input net a PWR_FLAG so KiCad
	// does not report "Input Power pin not driven" errors (Goal A). Emitted
	// after the gate so the flag stubs are never swept up by a demotion.
	e.ensurePowerFlags(sch)
	fmt.Fprintf(&sb, "\n%s\n", gateResult.String())

	if err := os.WriteFile(input.SchematicPath, []byte(sch.Serialize()), 0o644); err != nil {
		return toolText(fmt.Sprintf("error writing schematic: %v", err)), nil, nil
	}

	fmt.Fprintf(&sb, "\nDONE: %d wire segments, %d net labels placed, %d errors.\n", totalWires, totalLabels, totalErrors)
	if totalErrors > 0 {
		sb.WriteString("Errors: use connect_pins or add_label to handle failed connections manually.\n")
	}

	// Re-read symbols to get updated pin positions after routing.
	allSyms := sexp.ReadSymbols(sch)

	// Append pin positions for all non-power symbols.
	sb.WriteString("\nPin positions after routing:\n")
	for _, sym := range allSyms {
		if strings.HasPrefix(sym.LibID, "power:") || sym.LibID == "Device:PWR_FLAG" {
			continue
		}
		for _, pin := range sym.Pins {
			name := pin.Name
			if name == "" || name == "~" {
				name = pin.Number
			}
			fmt.Fprintf(&sb, "  %s.%s @ %.2f,%.2f\n", sym.Reference, name, pin.X, pin.Y)
		}
	}

	// Check for pins not assigned to any net in the connection table.
	// Also check existing wires/labels/no_connect markers in the schematic
	// so pins connected by previous calls (e.g. relayout auto-reconnect) are not falsely reported.
	assignedPins := make(map[string]bool)
	for _, conn := range input.Connections {
		for _, pin := range conn.Pins {
			assignedPins[pin] = true
		}
	}
	connSet := sexp.ConnectedPins(sch) // recognizes wires, labels AND power-symbol implicit nets
	ncSet := sexp.NoConnectPointSet(sch)
	// Multi-unit ICs (NE5532 etc.) appear in conn.Pins as "U1.1.+", "U1.3.V+",
	// so the lookup key must include the unit suffix when the symbol has units > 1.
	totalUnitsCache := make(map[string]int)
	totalUnitsFor := func(libID string) int {
		if n, ok := totalUnitsCache[libID]; ok {
			return n
		}
		n := countUnitsInLib(sch, libID)
		totalUnitsCache[libID] = n
		return n
	}
	var unassigned []string
	for _, sym := range allSyms {
		if strings.HasPrefix(sym.LibID, "power:") || sym.LibID == "Device:PWR_FLAG" {
			continue
		}
		multi := totalUnitsFor(sym.LibID) > 1
		for _, pin := range sym.Pins {
			name := pin.Name
			if name == "" || name == "~" {
				name = pin.Number
			}
			var ref string
			if multi {
				ref = fmt.Sprintf("%s.%d.%s", sym.Reference, sym.Unit, name)
			} else {
				ref = sym.Reference + "." + name
			}
			if assignedPins[ref] {
				continue
			}
			key := [2]float64{sexp.Round2(pin.X), sexp.Round2(pin.Y)}
			if connSet[key] || ncSet[key] {
				continue
			}
			unassigned = append(unassigned, ref)
		}
	}
	if len(unassigned) > 0 {
		fmt.Fprintf(&sb, "\nWARNING: %d pin(s) not assigned to any net — add no_connect for intentionally unused pins:\n", len(unassigned))
		for _, u := range unassigned {
			fmt.Fprintf(&sb, "  %s\n", u)
		}
	}

	// Suggest relayout when wires sprawl across a wide area — typical sign that
	// the LLM placed components manually without considering net topology.
	if span, n := wireSpan(sch); n >= 3 && span > 150.0 {
		fmt.Fprintf(&sb, "\nTIP: wires span %.0f mm across the page (%d segments). Call relayout to apply Sugiyama layout — it places connected symbols closer and shrinks total wire length.\n", span, n)
	}

	sb.WriteString("Tip: call validate_design (ERC) and add no_connect for unused pins.")
	return e.withInlinePNG(toolText(sb.String()), input.SchematicPath), nil, nil
}

// wireSpan returns the diagonal of the bounding box containing every wire
// endpoint, plus the wire count. Used to detect sprawled layouts that would
// benefit from relayout.
func wireSpan(sch *sexp.Schematic) (float64, int) {
	wires := sch.Wires()
	if len(wires) == 0 {
		return 0, 0
	}
	minX, minY := math.MaxFloat64, math.MaxFloat64
	maxX, maxY := -math.MaxFloat64, -math.MaxFloat64
	for _, w := range wires {
		ax, ay, bx, by := wireEndpoints(w)
		for _, p := range [4]float64{ax, bx} {
			if p < minX {
				minX = p
			}
			if p > maxX {
				maxX = p
			}
		}
		for _, p := range [4]float64{ay, by} {
			if p < minY {
				minY = p
			}
			if p > maxY {
				maxY = p
			}
		}
	}
	dx, dy := maxX-minX, maxY-minY
	return math.Sqrt(dx*dx + dy*dy), len(wires)
}

// routeNets routes conns into sch using the given router and strategy.
// Appends one status line per net to sb. Returns (totalWires, totalLabels, totalErrors).
// The Env receiver is needed because some strategies (e.g. auto power symbols
// for GND/VCC nets) call back into add_power_rail, which has to embed the
// power lib symbol — that requires access to the KiCad symbols path on Env.
// pinPos is a routing endpoint promoted to package scope so steiner / collinear
// helpers can share its layout. dir is the pin's outgoing direction in screen
// coords (0=E, 90=N, 180=W, 270=S; -1 when unknown).
type pinPos struct {
	ref  string
	x, y float64
	dir  float64
}

// routeSegment is a from→to pair on the rectilinear plane; promoted to package
// scope so Steiner pre-pass can hand built segments back to routeNets.
type routeSegment struct {
	from, to pinPos
}

// routeNets accepts an optional compForNet (net name -> pin ref -> component
// id) produced by the Phase 3 wiregen pre-pass in handleConnectNetlist: pins
// sharing a component id are already joined by a formula wire, so the router
// skips them. It is nil for callers that do not run wiregen (e.g. relayout),
// in which case routing is identical to the pre-Phase-3 behaviour.
func (e *Env) routeNets(sch *sexp.Schematic, rt *router.Router, conns []NetConn, strategy string, compForNet map[string]map[string]int, sb *strings.Builder) (totalWires, totalLabels, totalErrors int) {
	for _, conn := range conns {
		if len(conn.Pins) < 2 {
			fmt.Fprintf(sb, "  %-20s SKIP  (need at least 2 pins, got %d)\n", conn.Net, len(conn.Pins))
			continue
		}

		positions := make([]pinPos, 0, len(conn.Pins))
		missingPin := ""
		for _, pin := range conn.Pins {
			info, ok := sexp.FindPin(sch, pin)
			if !ok {
				missingPin = pin
				break
			}
			positions = append(positions, pinPos{
				ref: pin,
				x:   sexp.SnapGrid(info.X),
				y:   sexp.SnapGrid(info.Y),
				dir: info.Direction,
			})
		}
		if missingPin != "" {
			fmt.Fprintf(sb, "  %-20s ERROR  pin %q not found\n", conn.Net, missingPin)
			totalErrors++
			continue
		}

		netWires, netLabels := 0, 0
		var netNotes []string
		labeledPoints := make(map[[2]float64]bool)

		// Power-rail policy: if the net name maps to a known power lib_id
		// (GND, VCC, +5V, ±12V…) AND strategy isn't forcing wires, place a
		// power symbol at EVERY pin instead of routing — never point-to-point.
		// This is how engineers draw power rails by hand: each pin carries its
		// own #PWR flag (GND below the pin, VCC/+5V above), and KiCad treats
		// every `power:GND` as electrically joined to every other `power:GND`.
		// The result is zero long "rail across the page" wires and nothing for
		// the geometric gate to demote. The emitter dedups pins that snap to a
		// shared coordinate, so coincident pins collapse to one symbol.
		if pwrLib := netNameToPowerLibID(conn.Net); pwrLib != "" && strategy != "wire" {
			em := e.NewPowerEmitter(sch)
			pwrPlaced := 0
			for _, p := range positions {
				msg, ok, dedup := em.Emit(pwrLib, p.ref)
				if ok && !dedup {
					pwrPlaced++
				}
				if !ok {
					netNotes = append(netNotes, msg)
				}
			}
			status := ""
			if len(netNotes) > 0 {
				status = "  NOTE: " + strings.Join(netNotes, "; ")
			}
			fmt.Fprintf(sb, "  %-20s %-15s %s  — %d %s symbol(s)%s\n",
				conn.Net, "[power]", strings.Join(conn.Pins, " · "), pwrPlaced, pwrLib, status)
			totalLabels += pwrPlaced
			continue
		}

		// Build segment list. For 2-pin nets a single segment suffices. For 3+ pins
		// (typical for power, ground, common buses) we greedily build a minimum
		// spanning tree on Manhattan distance — connect each remaining pin to its
		// closest already-connected pin. This produces ~30% less wire than naive
		// daisy-chain (positions[i]→positions[i+1]) and avoids long zig-zag routes
		// when the LLM passes pins in unhelpful order.
		var segments []routeSegment
		netComp := compForNet[conn.Net]
		switch {
		case compHasMultiple(positions, netComp):
			// Some pins of this net were already wired by the cluster layer;
			// connect only the remaining components (Steiner is skipped so we
			// never double-wire a pin the formula layer already handled).
			segments = mstSegments(positions, netComp)
		case netComp != nil:
			// Fully consumed by the cluster layer — nothing left to route.
			segments = nil
		case len(positions) == 2:
			segments = []routeSegment{{positions[0], positions[1]}}
		default:
			// Steiner pre-pass: when ≥3 pins of this net are colinear (share an
			// X or Y axis within 1.27 mm), emit ONE trunk segment + perpendicular
			// stubs for every pin instead of daisy-chaining MST edges. Cuts
			// total wirelength + bend count drastically on power and bus nets.
			if seg, ok := steinerSegmentsForNet(positions); ok {
				segments = seg
			} else {
				segments = mstSegments(positions, nil)
			}
		}

		for _, seg := range segments {
			from := seg.from
			to := seg.to

			usedLabel := false
			usedWire := false

			if strategy == "label" {
				usedLabel = true
			} else {
				path := routeWithExits(rt, from.x, from.y, from.dir, to.x, to.y, to.dir)
				if path != nil {
					wires := pathToWires(path)
					for _, w := range wires {
						sch.AddWire(w)
					}
					rt.MarkWire(path)
					addTJunctions(sch, path)
					netWires += len(wires)
					usedWire = true
				} else if strategy == "wire" {
					netNotes = append(netNotes, fmt.Sprintf("%s→%s A* failed", from.ref, to.ref))
					totalErrors++
					continue
				} else {
					usedLabel = true
				}
			}

			if usedLabel {
				fromKey := [2]float64{sexp.Round2(from.x), sexp.Round2(from.y)}
				toKey := [2]float64{sexp.Round2(to.x), sexp.Round2(to.y)}
				if !labeledPoints[fromKey] {
					sch.AddLabel(sexp.NewNetLabel(conn.Net, from.x, from.y, labelRotForDir(from.dir)))
					labeledPoints[fromKey] = true
					netLabels++
				}
				if !labeledPoints[toKey] {
					sch.AddLabel(sexp.NewNetLabel(conn.Net, to.x, to.y, labelRotForDir(to.dir)))
					labeledPoints[toKey] = true
					netLabels++
				}
			}
			_ = usedWire
		}

		// When all segments were routed with wires (no labels), place one net label
		// at the first pin so relayout can discover the net's name via TraceNets.
		// This must ALSO fire when the wiregen pre-pass consumed pairs of this net
		// (netComp != nil): a net fully wired by formula produces zero router
		// segments here (netWires==0), but without a label relayout cannot
		// rediscover the net and silently drops it, leaving its pins dangling
		// after the rewire (regression found on demo_voltage_regulator's
		// LED_NODE, wired entirely by the series_led generator).
		if netLabels == 0 && (netWires > 0 || netComp != nil) && conn.Net != "" {
			sch.AddLabel(sexp.NewNetLabel(conn.Net, positions[0].x, positions[0].y, labelRotForDir(positions[0].dir)))
			netLabels++
		}

		var parts []string
		if netWires > 0 {
			parts = append(parts, fmt.Sprintf("%d wire seg(s)", netWires))
		}
		if netLabels > 0 {
			parts = append(parts, fmt.Sprintf("%d label(s)", netLabels))
		}
		status := "OK"
		if len(parts) > 0 {
			status = strings.Join(parts, ", ")
		}
		if len(netNotes) > 0 {
			status += "  NOTE: " + strings.Join(netNotes, "; ")
		}
		pinChain := make([]string, len(conn.Pins))
		copy(pinChain, conn.Pins)
		fmt.Fprintf(sb, "  %-20s [%s]  %s  — %s\n",
			conn.Net,
			strategyUsed(netWires, netLabels),
			strings.Join(pinChain, " → "),
			status,
		)
		totalWires += netWires
		totalLabels += netLabels
	}
	// MST greedy routing can emit identical pin-exit stubs when two hops of
	// the same net leave from the same pin. Dedupe so the file doesn't carry
	// visually stacked segments.
	if dropped := sch.DedupeWires(); dropped > 0 {
		fmt.Fprintf(sb, "  (dedupe: removed %d duplicate wire segment(s))\n", dropped)
		totalWires -= dropped
		if totalWires < 0 {
			totalWires = 0
		}
	}
	return
}

// netNameToPowerLibID maps common net names to their canonical KiCad power
// symbol lib_ids. Returns "" when the net name isn't a recognized rail —
// callers should then fall back to wire/label routing.
//
// Comparison is case-insensitive and ignores leading/trailing whitespace.
// VBAT, VIN, VOUT, etc. intentionally return "" because they're not rails
// in the KiCad power lib (they're application-specific signals).
func netNameToPowerLibID(name string) string {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "GND", "GND1", "GND2", "GNDA", "GNDD", "GNDPWR", "GNDREF", "GNDS":
		return "power:GND"
	case "EARTH":
		return "power:Earth"
	case "VCC":
		return "power:VCC"
	case "VDD":
		return "power:VDD"
	case "VEE":
		return "power:VEE"
	case "VSS":
		return "power:VSS"
	case "+5V":
		return "power:+5V"
	case "+3V3", "+3.3V":
		return "power:+3V3"
	case "+12V":
		return "power:+12V"
	case "-12V":
		return "power:-12V"
	case "+15V":
		return "power:+15V"
	case "-15V":
		return "power:-15V"
	case "+9V":
		return "power:+9V"
	case "-9V":
		return "power:-9V"
	case "+24V":
		return "power:+24V"
	case "VPP":
		return "power:+12V" // common alias for op-amp positive rail
	case "VMM":
		return "power:-12V" // common alias for op-amp negative rail
	}
	return ""
}

// strategyUsed returns a short tag for the result line.
func strategyUsed(wires, labels int) string {
	if wires > 0 && labels == 0 {
		return "wire"
	}
	if labels > 0 && wires == 0 {
		return "label"
	}
	if wires > 0 && labels > 0 {
		return "mixed"
	}
	return "none"
}

// routeWithExits routes from (x1,y1) to (x2,y2) but forces the first 2.54 mm
// to extend in the source pin's outgoing direction and the last 2.54 mm to
// approach the target pin from its outgoing direction (so wires meet pins
// head-on rather than sliding sideways across the symbol body).
//
// If either pin direction is unknown, falls back to plain rt.Route.
// If A* between the exit and entry stubs fails, falls back to plain routing
// rather than failing the segment outright.
func routeWithExits(rt *router.Router, x1, y1, dir1, x2, y2, dir2 float64) [][2]float64 {
	const stubLen = 2.54
	exitX, exitY, hasExit := offsetByDir(x1, y1, dir1, stubLen)
	entryX, entryY, hasEntry := offsetByDir(x2, y2, dir2, stubLen)

	if !hasExit && !hasEntry {
		return rt.Route(x1, y1, x2, y2)
	}

	startX, startY := x1, y1
	endX, endY := x2, y2
	var pre, post [][2]float64
	if hasExit {
		pre = [][2]float64{{x1, y1}, {exitX, exitY}}
		startX, startY = exitX, exitY
	}
	if hasEntry {
		post = [][2]float64{{entryX, entryY}, {x2, y2}}
		endX, endY = entryX, entryY
	}

	mid := rt.Route(startX, startY, endX, endY)
	if mid == nil {
		// A* with stubs failed — try plain routing (stubs were premature).
		return rt.Route(x1, y1, x2, y2)
	}

	// Stitch: pre + mid + post, dropping duplicates at the seams.
	var combined [][2]float64
	if len(pre) > 0 {
		combined = append(combined, pre[0])
		// pre[1] equals mid[0]; skip the duplicate.
		combined = append(combined, mid...)
	} else {
		combined = append(combined, mid...)
	}
	if len(post) > 0 {
		// combined ends at endX,endY which equals post[0]; skip the duplicate.
		combined = append(combined, post[1])
	}
	return mergeCollinear(combined)
}

// labelRotForDir returns the label rotation that makes the text read away
// from the symbol body — i.e., aligned with the pin's outgoing wire
// direction. Pin direction is in screen-space CCW degrees from +X.
//
//   pin direction east  (0)   → label rotation 0   (text reads right)
//   pin direction north (90)  → label rotation 90  (text reads up)
//   pin direction west  (180) → label rotation 180 (text reads left)
//   pin direction south (270) → label rotation 270 (text reads down)
func labelRotForDir(dir float64) float64 {
	switch int(math.Round(dir)) % 360 {
	case 0:
		return 0
	case 90:
		return 90
	case 180:
		return 180
	case 270:
		return 270
	}
	return 0
}

// offsetByDir returns (x,y) offset from (x,y) by `dist` in the direction `dir`
// (degrees CCW from +X visually, screen Y-down). hasDir is false when dir is
// not one of the four cardinal directions or when the offset cell would land
// inside a hard obstacle (caller falls back to direct routing).
func offsetByDir(x, y, dir, dist float64) (nx, ny float64, ok bool) {
	switch int(math.Round(dir)) % 360 {
	case 0:
		return x + dist, y, true
	case 90:
		return x, y - dist, true // screen north = -Y
	case 180:
		return x - dist, y, true
	case 270:
		return x, y + dist, true
	}
	return x, y, false
}

// mergeCollinear removes interior waypoints that are collinear with their
// neighbours (so consecutive wires on the same axis merge into one).
func mergeCollinear(pts [][2]float64) [][2]float64 {
	if len(pts) <= 2 {
		return pts
	}
	out := []([2]float64){pts[0]}
	for i := 1; i < len(pts)-1; i++ {
		prev := out[len(out)-1]
		cur := pts[i]
		next := pts[i+1]
		sameH := math.Abs(prev[1]-cur[1]) < 0.001 && math.Abs(cur[1]-next[1]) < 0.001
		sameV := math.Abs(prev[0]-cur[0]) < 0.001 && math.Abs(cur[0]-next[0]) < 0.001
		if sameH || sameV {
			continue
		}
		out = append(out, cur)
	}
	return append(out, pts[len(pts)-1])
}

// pathToWires converts a sequence of waypoints to wire nodes.
func pathToWires(path [][2]float64) []*sexp.Node {
	if len(path) < 2 {
		return nil
	}
	wires := make([]*sexp.Node, 0, len(path)-1)
	for i := 1; i < len(path); i++ {
		wires = append(wires, sexp.NewWire(path[i-1][0], path[i-1][1], path[i][0], path[i][1]))
	}
	return wires
}

// addTJunctions checks whether any new waypoint in path lands on an existing
// wire segment (T-intersection) and inserts a junction node if so.
func addTJunctions(sch *sexp.Schematic, path [][2]float64) {
	existing := sch.Wires()
	// Check interior waypoints (not start/end — those are pin locations).
	for i := 1; i < len(path)-1; i++ {
		px, py := path[i][0], path[i][1]
		for _, w := range existing {
			ax, ay, bx, by := wireEndpoints(w)
			if pointOnSegment(px, py, ax, ay, bx, by) {
				sch.AddJunction(sexp.NewJunction(px, py))
				break
			}
		}
	}
}

// wireEndpoints returns (ax,ay,bx,by) for a wire node.
func wireEndpoints(w *sexp.Node) (float64, float64, float64, float64) {
	pts := sexp.FindList(w, "pts")
	if pts == nil {
		return 0, 0, 0, 0
	}
	var xs, ys [2]float64
	n := 0
	for _, xy := range pts.Children {
		if xy.Head() != "xy" || n >= 2 {
			continue
		}
		xs[n] = parseAtomF(sexp.AtomValue(xy, 1))
		ys[n] = parseAtomF(sexp.AtomValue(xy, 2))
		n++
	}
	if n < 2 {
		return 0, 0, 0, 0
	}
	return xs[0], ys[0], xs[1], ys[1]
}

// pointOnSegment returns true when (px,py) lies strictly between (ax,ay) and (bx,by)
// on an axis-aligned segment (not at either endpoint).
func pointOnSegment(px, py, ax, ay, bx, by float64) bool {
	const eps = 0.01
	// Horizontal segment.
	if math.Abs(ay-by) < eps && math.Abs(py-ay) < eps {
		lo, hi := math.Min(ax, bx), math.Max(ax, bx)
		return px > lo+eps && px < hi-eps
	}
	// Vertical segment.
	if math.Abs(ax-bx) < eps && math.Abs(px-ax) < eps {
		lo, hi := math.Min(ay, by), math.Max(ay, by)
		return py > lo+eps && py < hi-eps
	}
	return false
}

func parseAtomF(s string) float64 {
	v := 0.0
	fmt.Sscanf(s, "%f", &v)
	return v
}

// RegisterNetlistTools registers the connect_netlist tool on the MCP server.
func RegisterNetlistTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "connect_netlist",
		Description: "Route a complete netlist in one call — replaces many individual connect_pins calls.\n\n" +
			"Each connection item has two fields:\n" +
			"  {\"net\": \"VCC\", \"pins\": [\"U1.VCC\", \"R1.1\", \"C1.+\"]}\n" +
			"  net  — net name (used as label text)\n" +
			"  pins — pin refs in REF.PIN format (at least 2)\n\n" +
			"The server routes wires between pins using an A* grid router that avoids symbol bodies.\n" +
			"Returns full pin positions after routing — no need to call read_schematic separately.\n\n" +
			"Strategy options:\n" +
			"  auto  (default) — A* routing; falls back to net labels when routing fails or route > 150 mm\n" +
			"  wire  — A* only; warns about failures but does not fall back to labels\n" +
			"  label — skip routing entirely; place net labels at every pin (fastest, always clean)\n\n" +
			"Typical workflow:\n" +
			"  1. create_schematic\n" +
			"  2. batch: add all symbols (auto_place: true)\n" +
			"  3. connect_netlist with full connection table, strategy: auto\n" +
			"  4. relayout  ← Sugiyama places symbols based on connections\n" +
			"  5. add_power_rail for power pins (uses pin positions from connect_netlist output)\n" +
			"  6. validate_design (ERC)\n\n" +
			"Pin ref format: REF.pin  e.g. R1.1, U1.VCC, C1.+\n" +
			"Multi-unit: REF.unit.pin  e.g. U1.1.+\n\n" +
			"Junctions are auto-inserted at T-intersections.\n" +
			"One net label per net is always placed (even with wire strategy) so relayout can discover topology.",
	}, WrapTool(env.Log, "connect_netlist", env.handleConnectNetlist))
}
