package tools

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/sexp"
)

// modifyPCBLayoutInput defines the parameters for the modify_pcb_layout tool.
type modifyPCBLayoutInput struct {
	PCBPath   string  `json:"pcb_path"   jsonschema:"Path to the .kicad_pcb file"`
	Action    string  `json:"action"     jsonschema:"Action: move_footprint or define_edge_cuts"`
	Reference string  `json:"reference"  jsonschema:"Reference designator for move_footprint (e.g. R1)"`
	X         float64 `json:"x"          jsonschema:"X position in mm (or left edge for edge_cuts)"`
	Y         float64 `json:"y"          jsonschema:"Y position in mm (or top edge for edge_cuts)"`
	Angle     float64 `json:"angle"      jsonschema:"Rotation angle in degrees for move_footprint"`
	Width     float64 `json:"width"      jsonschema:"Board width in mm for define_edge_cuts"`
	Height    float64 `json:"height"     jsonschema:"Board height in mm for define_edge_cuts"`
}

func (e *Env) handleModifyPCBLayout(_ context.Context, _ *mcp.CallToolRequest, input modifyPCBLayoutInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.PCBPath == "" {
		return toolText("error: pcb_path is required"), nil, nil
	}

	data, err := os.ReadFile(input.PCBPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading PCB file: %v", err)), nil, nil
	}

	pcb, err := sexp.ParsePCB(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing PCB file: %v", err)), nil, nil
	}

	switch input.Action {
	case "move_footprint":
		if input.Reference == "" {
			return toolText("error: reference is required for move_footprint"), nil, nil
		}
		if err := pcb.MoveFootprint(input.Reference, input.X, input.Y, input.Angle); err != nil {
			return toolText(fmt.Sprintf("error: %v", err)), nil, nil
		}

	case "define_edge_cuts":
		if input.Width <= 0 || input.Height <= 0 {
			return toolText("error: width and height must be positive for define_edge_cuts"), nil, nil
		}
		for _, line := range sexp.NewEdgeCutsRect(input.X, input.Y, input.Width, input.Height) {
			pcb.AddGrLine(line)
		}

	default:
		return toolText(fmt.Sprintf("error: unknown action %q (use move_footprint or define_edge_cuts)", input.Action)), nil, nil
	}

	if err := os.WriteFile(input.PCBPath, []byte(pcb.Serialize()), 0o644); err != nil {
		return toolText(fmt.Sprintf("error writing PCB file: %v", err)), nil, nil
	}

	return toolText(fmt.Sprintf("PCB updated: %s (action=%s)", input.PCBPath, input.Action)), nil, nil
}

// RegisterPCBTools registers PCB editing tools on the server.
func RegisterPCBTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "modify_pcb_layout",
		Description: "Modify a KiCad PCB file (.kicad_pcb). " +
			"Action 'move_footprint': requires reference, x, y, angle. " +
			"Action 'define_edge_cuts': requires x, y (top-left corner), width, height.",
	}, WrapTool(env.Log, "modify_pcb_layout", env.handleModifyPCBLayout))
	mcp.AddTool(s, &mcp.Tool{
		Name: "compile_pcb",
		Description: "Build an unrouted board (.kicad_pcb) from a schematic made by compile_schematic: every footprint placed without overlaps inside an edge outline, pads on the nets KiCad reads from the schematic, linked to their symbols so KiCad's Update PCB from Schematic keeps working. " +
			"Checked by KiCad itself (DRC with --schematic-parity); returns the report and a preview. Every board part needs a footprint — compile_schematic's report lists any that are MISSING. " +
			"Never overwrites an existing board unless overwrite is set.",
	}, WrapTool(env.Log, "compile_pcb", env.handleCompilePCB))
	mcp.AddTool(s, &mcp.Tool{
		Name: "export_fabrication",
		Description: "Write the files to have a board made and assembled, next to the .kicad_pcb in <name>_fab/: Gerbers, Excellon drill files and map, pick-and-place CSV, BOM CSV, and <name>_gerbers.zip ready to upload to a board house. " +
			"Refuses unless KiCad's DRC finds no error and the board matches its schematic; refuses unrouted boards unless allow_unrouted is set.",
	}, WrapTool(env.Log, "export_fabrication", env.handleExportFabrication))
}
