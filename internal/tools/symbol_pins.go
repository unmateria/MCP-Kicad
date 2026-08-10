package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/sexp"
)

// SymbolPinsArgs selects one library symbol to inspect.
type SymbolPinsArgs struct {
	LibID  string `json:"lib_id" jsonschema:"Qualified symbol id, e.g. Timer:NE555P or 4xxx:4029"`
	Unit   int    `json:"unit,omitempty" jsonschema:"Unit number for multi-unit parts (default 1)"`
	Rot    int    `json:"rot,omitempty" jsonschema:"Show the pin directions as they would be with this rotation (0/90/180/270). Default 0."`
	Mirror bool   `json:"mirror,omitempty" jsonschema:"Show the pin directions as they would be with mirror:true, applied AFTER the rotation."`
}

// handleSymbolPins lists the pins a symbol really has.
//
// Without this the only way to learn a pin's name was to guess and read the
// compiler's rejection: a real session burned half its iterations cycling
// RESET → RST → giving up and using the number. The names in KiCad's libraries
// are frequently not the datasheet's (NE555P has THRES and ~{RST}, not THR and
// RESET), so guessing is not a reasonable expectation.
//
// The listing comes from the same libGeom probe the compiler uses, so whatever
// appears here is exactly what a `.design.json` source may write.
func (e *Env) handleSymbolPins(_ context.Context, _ *mcp.CallToolRequest, input SymbolPinsArgs) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)

	if strings.TrimSpace(input.LibID) == "" {
		return toolText("error: lib_id is required, e.g. \"Device:R\" or \"Timer:NE555P\""), nil, nil
	}
	if !strings.Contains(input.LibID, ":") {
		return toolText(fmt.Sprintf("error: lib_id must be qualified as Library:Symbol (got %q). Use check_component_existence to find the library.", input.LibID)), nil, nil
	}

	unit := input.Unit
	if unit < 1 {
		unit = 1
	}
	// Probe with the REQUESTED rotation and mirror rather than transforming
	// rot-0 geometry by hand: the instance goes through the same placement
	// machinery production uses, so what this lists is exactly where the pins
	// will be. A session designing a 7-segment fan-out trusted a pin-number
	// ordering, got the fan crossed, and had to deduce the real vertical order
	// from the rendered PNG — the geometric order below is that information.
	sym, err := e.probePlaced(input.LibID, unit, input.Rot, input.Mirror)
	if err != nil {
		return toolText(fmt.Sprintf("error: %v\n\nUse check_component_existence to confirm the symbol id.", err)), nil, nil
	}
	if len(sym.Pins) == 0 {
		return toolText(fmt.Sprintf("%s unit %d has no pins (try another unit — power pins often live on the last unit of a multi-unit part)", input.LibID, unit)), nil, nil
	}

	pins := append([]sexp.PinInfo(nil), sym.Pins...)
	// Geometric order, the one the drawing has: by side, then top→bottom for
	// vertical sides and left→right for horizontal ones. Wiring a row of pins
	// to a row of targets in this order is what keeps the wires parallel.
	sideRank := map[string]int{"left": 0, "right": 1, "up": 2, "down": 3}
	sideOf := func(p sexp.PinInfo) string { return pinSide(p.Direction) }
	sort.SliceStable(pins, func(i, j int) bool {
		si, sj := sideOf(pins[i]), sideOf(pins[j])
		if si != sj {
			return sideRank[si] < sideRank[sj]
		}
		if si == "left" || si == "right" {
			if pins[i].Y != pins[j].Y {
				return pins[i].Y < pins[j].Y // KiCad's Y grows downward: top first
			}
		} else if pins[i].X != pins[j].X {
			return pins[i].X < pins[j].X
		}
		return pinNumLess(pins[i].Number, pins[j].Number)
	})

	var sb strings.Builder
	orient := ""
	if input.Rot != 0 || input.Mirror {
		orient = fmt.Sprintf("  [as placed with rot %d", input.Rot)
		if input.Mirror {
			orient += " + mirror"
		}
		orient += "]"
	}
	fmt.Fprintf(&sb, "%s (unit %d) — %d pins, listed in DRAWING order (per side, top→bottom / left→right)%s\n\n",
		input.LibID, unit, len(pins), orient)
	fmt.Fprintf(&sb, "%-6s %-14s %-12s %-6s %s\n", "PIN", "NAME", "TYPE", "SIDE", "AT (mm, y grows down)")
	lastSide := ""
	for _, p := range pins {
		name := p.Name
		if name == "" || name == "~" {
			name = "-"
		}
		side := sideOf(p)
		if side != lastSide && lastSide != "" {
			sb.WriteString("\n")
		}
		lastSide = side
		mark := ""
		if strings.Contains(name, ".") {
			mark = "  ← dotted name: write the NUMBER"
		}
		fmt.Fprintf(&sb, "%-6s %-14s %-12s %-6s (%.2f, %.2f)%s\n", p.Number, name, p.Electrical,
			side, p.X-sym.X, p.Y-sym.Y, mark)
	}
	fmt.Fprintf(&sb, "\nIn a .design.json source write either form: \"%s.%s\" or \"%s.%s\".\n",
		"REF", pins[0].Number, "REF", displayPinName(pins[0]))
	sb.WriteString("Names with braces or tildes (~{RST}) are awkward to type — use the pin NUMBER for those.\n")
	sb.WriteString("If a pin NAME itself contains a dot (resistor packs call them R1.1), always use the NUMBER:\n")
	sb.WriteString("\"REF.R1.1\" collides with the multi-unit syntax REF.unit.pin and is rejected as ambiguous.\n")
	sb.WriteString("SIDE is the direction the pin points AWAY from the body: anchor the next part that way\n")
	sb.WriteString("(\"place\": {\"pin\": ..., \"dir\": <side>}) and the wire comes out straight.\n")
	sb.WriteString("To fan out one row of pins to another part in parallel wires, connect them in the\n")
	sb.WriteString("order listed here — pin-number order is often NOT the vertical order of the drawing.\n")

	return toolText(sb.String()), nil, nil
}

// probePlaced instantiates a symbol at the origin with the given rotation and
// mirror in a scratch schematic and reads it back, so the pin coordinates are
// the placed ones — the identical code path a real placement takes.
func (e *Env) probePlaced(libID string, unit, rot int, mirror bool) (sexp.SchematicSymbol, error) {
	sch, err := newEmptySchematic()
	if err != nil {
		return sexp.SchematicSymbol{}, err
	}
	if err := e.embedLibSymbol(sch, libID); err != nil {
		return sexp.SchematicSymbol{}, fmt.Errorf("symbol %s: %w", libID, err)
	}
	pinNums := extractPinNumbers(sch, libID, unit)
	libDef := sch.FindLibDef(libID)
	inst := sexp.NewSymbolInstance(libID, "XPROBE1", "", "",
		0, 0, float64(rot), unit, pinNums, sch.UUID(), false, false, libDef)
	if mirror {
		sexp.SetSymbolMirror(inst, "y")
	}
	sch.AddSymbol(inst)
	for _, sym := range sexp.ReadSymbols(sch) {
		if sym.Reference == "XPROBE1" {
			return sym, nil
		}
	}
	return sexp.SchematicSymbol{}, fmt.Errorf("geometry probe for %s unit %d produced no readable instance", libID, unit)
}

// displayPinName is the name a source would use, falling back to the number.
func displayPinName(p sexp.PinInfo) string {
	if p.Name == "" || p.Name == "~" {
		return p.Number
	}
	return p.Name
}

// pinSide turns a pin's outgoing angle into the word a source uses for `dir`.
func pinSide(deg float64) string {
	switch int(deg+360) % 360 {
	case 0:
		return "right"
	case 90:
		return "up"
	case 180:
		return "left"
	case 270:
		return "down"
	}
	return fmt.Sprintf("%.0f°", deg)
}

// pinNumLess orders pin numbers numerically when both are numeric, so 2 comes
// before 10, and lexically otherwise (BGA-style A1, B2…).
func pinNumLess(a, b string) bool {
	ai, aerr := atoiSafe(a)
	bi, berr := atoiSafe(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}

func atoiSafe(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

