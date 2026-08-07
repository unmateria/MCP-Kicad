package tools

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/sexp"
)

// withInlinePNG attaches an inline PNG preview of schPath to the given result.
// If rendering fails for any reason (kicad-cli unavailable, schematic missing,
// SVG conversion failure), returns res unchanged — never breaks the caller.
//
// Applied only to high-value mutating tools where the visual feedback justifies
// the ~600ms-2s kicad-cli latency. Atomic edits (add_wire, junction, etc.) skip
// this helper to keep latency low.
func (e *Env) withInlinePNG(res *mcp.CallToolResult, schPath string) *mcp.CallToolResult {
	if res == nil || schPath == "" || e.KicadCLI == "" {
		return res
	}
	pngBytes, err := RenderSchematicPNG(schPath, e.KicadCLI, e.OutputDir)
	if err != nil || len(pngBytes) == 0 {
		return res
	}
	prepended := []mcp.Content{&mcp.ImageContent{Data: pngBytes, MIMEType: "image/png"}}
	prepended = append(prepended, res.Content...)
	return &mcp.CallToolResult{
		Content:           prepended,
		IsError:           res.IsError,
		StructuredContent: res.StructuredContent,
	}
}

// execOne reads, parses, applies one operation, and writes back the schematic.
// Shared by every per-action handler. Returns a tool-text result, never a Go error.
func (e *Env) execOne(schPath string, op modifySchematicInput) (*mcp.CallToolResult, any, error) {
	if schPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	if op.Action == "create_schematic" {
		return e.handleCreateSchematic(schPath)
	}
	data, err := os.ReadFile(schPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v — call create_schematic first", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}
	op.SchematicPath = schPath
	msg, ok := e.applyOp(sch, op, false)
	if !ok {
		return toolText(msg), nil, nil
	}
	if err := os.WriteFile(schPath, []byte(sch.Serialize()), 0o644); err != nil {
		return toolText(fmt.Sprintf("error writing schematic: %v", err)), nil, nil
	}
	return toolText(msg), nil, nil
}

// --- create_schematic ---

type createSchematicInput struct {
	SchematicPath string `json:"schematic_path" jsonschema:"Path to the .kicad_sch file to create"`
}

func (e *Env) handleCreateSchematicTool(_ context.Context, _ *mcp.CallToolRequest, in createSchematicInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	r, _, err := e.handleCreateSchematic(in.SchematicPath)
	return r, nil, err
}

// --- add_symbol ---

type addSymbolInput struct {
	SchematicPath string  `json:"schematic_path"          jsonschema:"Path to the .kicad_sch file"`
	LibID         string  `json:"lib_id"                  jsonschema:"e.g. Device:R, Amplifier_Operational:NE5532, power:GND"`
	Reference     string  `json:"reference"               jsonschema:"e.g. R1, U1 (multi-unit ICs share the reference across units)"`
	Value         string  `json:"value,omitempty"         jsonschema:"e.g. 100, 10k, 100n; ignored for power symbols"`
	MountType     string  `json:"mount_type,omitempty"    jsonschema:"THT or SMD — defaults to THT"`
	Footprint     string  `json:"footprint,omitempty"     jsonschema:"override; auto-assigned from mount_type if empty"`
	Unit          int     `json:"unit,omitempty"          jsonschema:"unit number for multi-unit ICs (default 1); place each unit separately"`
	AutoPlace     bool    `json:"auto_place,omitempty"    jsonschema:"true → server assigns a free grid position"`
	Rotation      float64 `json:"rotation,omitempty"      jsonschema:"CCW degrees: 0, 90, 180, 270"`
	X             float64 `json:"x,omitempty"             jsonschema:"X in mm; required if auto_place is false"`
	Y             float64 `json:"y,omitempty"             jsonschema:"Y in mm; required if auto_place is false"`
}

func (e *Env) handleAddSymbol(_ context.Context, _ *mcp.CallToolRequest, in addSymbolInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	r, _, err := e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "add_symbol", LibID: in.LibID, Reference: in.Reference, Value: in.Value,
		MountType: in.MountType, Footprint: in.Footprint, Unit: in.Unit,
		AutoPlace: in.AutoPlace, Rotation: in.Rotation, X: in.X, Y: in.Y,
	})
	if in.AutoPlace {
		r = e.withInlinePNG(r, in.SchematicPath)
	}
	return r, nil, err
}

// --- add_power_rail ---

type addPowerRailInput struct {
	SchematicPath string  `json:"schematic_path"      jsonschema:"Path to the .kicad_sch file"`
	LibID         string  `json:"lib_id"              jsonschema:"power: symbol, e.g. power:GND, power:VCC, power:+5V, power:-12V"`
	From          string  `json:"from"                jsonschema:"target pin in REF.pin form (e.g. U1.VCC, U1.1.+ for multi-unit)"`
	Rotation      float64 `json:"rotation,omitempty"  jsonschema:"CCW degrees: 0, 90, 180, 270"`
}

func (e *Env) handleAddPowerRail(_ context.Context, _ *mcp.CallToolRequest, in addPowerRailInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	r, _, err := e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "add_power_rail", LibID: in.LibID, From: in.From, Rotation: in.Rotation,
	})
	r = e.withInlinePNG(r, in.SchematicPath)
	return r, nil, err
}

// --- connect_pins ---

type connectPinsInput struct {
	SchematicPath string      `json:"schematic_path"  jsonschema:"Path to the .kicad_sch file"`
	From          string      `json:"from"            jsonschema:"source pin (REF.pin or REF.unit.pin)"`
	To            string      `json:"to"              jsonschema:"destination pin (REF.pin or REF.unit.pin)"`
	Via           [][]float64 `json:"via,omitempty"   jsonschema:"optional [[x,y],...] waypoints to route around obstacles"`
}

func (e *Env) handleConnectPinsTool(_ context.Context, _ *mcp.CallToolRequest, in connectPinsInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "connect_pins", From: in.From, To: in.To, Via: in.Via,
	})
}

// --- disconnect_pin ---

type disconnectPinInput struct {
	SchematicPath string `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
	From          string `json:"from"           jsonschema:"pin to disconnect (e.g. R1.1) — removes wires/no_connect markers touching it"`
}

func (e *Env) handleDisconnectPin(_ context.Context, _ *mcp.CallToolRequest, in disconnectPinInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "disconnect_pin", From: in.From,
	})
}

// --- add_wire ---

type addWireInput struct {
	SchematicPath string  `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
	X             float64 `json:"x"              jsonschema:"start X in mm"`
	Y             float64 `json:"y"              jsonschema:"start Y in mm"`
	X2            float64 `json:"x2"             jsonschema:"end X in mm"`
	Y2            float64 `json:"y2"             jsonschema:"end Y in mm"`
}

func (e *Env) handleAddWire(_ context.Context, _ *mcp.CallToolRequest, in addWireInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "add_wire", X: in.X, Y: in.Y, X2: in.X2, Y2: in.Y2,
	})
}

// --- no_connect ---

type noConnectInput struct {
	SchematicPath string  `json:"schematic_path"   jsonschema:"Path to the .kicad_sch file"`
	From          string  `json:"from,omitempty"   jsonschema:"pin to mark unconnected (REF.pin) — auto-resolves coords"`
	X             float64 `json:"x,omitempty"      jsonschema:"X in mm (only if from is empty)"`
	Y             float64 `json:"y,omitempty"      jsonschema:"Y in mm (only if from is empty)"`
}

func (e *Env) handleNoConnectTool(_ context.Context, _ *mcp.CallToolRequest, in noConnectInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "no_connect", From: in.From, X: in.X, Y: in.Y,
	})
}

// --- junction ---

type junctionInput struct {
	SchematicPath string  `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
	X             float64 `json:"x"              jsonschema:"X in mm at the wire intersection"`
	Y             float64 `json:"y"              jsonschema:"Y in mm at the wire intersection"`
}

func (e *Env) handleJunctionTool(_ context.Context, _ *mcp.CallToolRequest, in junctionInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "junction", X: in.X, Y: in.Y,
	})
}

// --- add_label ---

type addLabelInput struct {
	SchematicPath string  `json:"schematic_path"     jsonschema:"Path to the .kicad_sch file"`
	Name          string  `json:"name"               jsonschema:"net label; two labels with the same name are electrically connected"`
	X             float64 `json:"x"                  jsonschema:"X in mm"`
	Y             float64 `json:"y"                  jsonschema:"Y in mm"`
	Rotation      float64 `json:"rotation,omitempty" jsonschema:"label angle: 0=right, 90=up, 180=left, 270=down"`
}

func (e *Env) handleAddLabelTool(_ context.Context, _ *mcp.CallToolRequest, in addLabelInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return e.execOne(in.SchematicPath, modifySchematicInput{
		Action: "add_label", Name: in.Name, X: in.X, Y: in.Y, Rotation: in.Rotation,
	})
}

// --- batch_schematic ---
// Reuses the existing batchOperation type and the batch path inside handleModifySchematic.

type batchSchematicInput struct {
	SchematicPath string           `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
	Operations    []batchOperation `json:"operations"     jsonschema:"List of operations executed atomically in one file write. Stops on first error."`
}

func (e *Env) handleBatchSchematic(ctx context.Context, req *mcp.CallToolRequest, in batchSchematicInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	r, _, err := e.handleModifySchematic(ctx, req, modifySchematicInput{
		SchematicPath: in.SchematicPath,
		Action:        "batch",
		Operations:    in.Operations,
	})
	r = e.withInlinePNG(r, in.SchematicPath)
	return r, nil, err
}
