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
	LibID string `json:"lib_id" jsonschema:"Qualified symbol id, e.g. Timer:NE555P or 4xxx:4029"`
	Unit  int    `json:"unit,omitempty" jsonschema:"Unit number for multi-unit parts (default 1)"`
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

	sg, err := e.newLibGeom()
	if err != nil {
		return toolText(fmt.Sprintf("error: %v", err)), nil, nil
	}
	unit := input.Unit
	if unit < 1 {
		unit = 1
	}
	sym, err := sg.instance(input.LibID, unit)
	if err != nil {
		return toolText(fmt.Sprintf("error: %v\n\nUse check_component_existence to confirm the symbol id.", err)), nil, nil
	}
	if len(sym.Pins) == 0 {
		return toolText(fmt.Sprintf("%s unit %d has no pins (try another unit — power pins often live on the last unit of a multi-unit part)", input.LibID, unit)), nil, nil
	}

	pins := append([]sexp.PinInfo(nil), sym.Pins...)
	sort.SliceStable(pins, func(i, j int) bool { return pinNumLess(pins[i].Number, pins[j].Number) })

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (unit %d) — %d pins\n\n", input.LibID, unit, len(pins))
	fmt.Fprintf(&sb, "%-6s %-14s %-12s %s\n", "PIN", "NAME", "TYPE", "SIDE")
	for _, p := range pins {
		name := p.Name
		if name == "" || name == "~" {
			name = "-"
		}
		fmt.Fprintf(&sb, "%-6s %-14s %-12s %s\n", p.Number, name, p.Electrical, pinSide(p.Direction))
	}
	fmt.Fprintf(&sb, "\nIn a .design.json source write either form: \"%s.%s\" or \"%s.%s\".\n",
		sym.Reference[:0]+"REF", pins[0].Number, "REF", displayPinName(pins[0]))
	sb.WriteString("Names with braces or tildes (~{RST}) are awkward to type — use the pin NUMBER for those.\n")
	sb.WriteString("SIDE is the direction the pin points AWAY from the body: anchor the next part that way\n")
	sb.WriteString("(\"place\": {\"pin\": ..., \"dir\": <side>}) and the wire comes out straight.\n")

	return toolText(sb.String()), nil, nil
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
