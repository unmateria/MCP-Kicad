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
}
