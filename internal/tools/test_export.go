package tools

// This file exposes thin wrappers around per-action handlers for use by the
// cmd/verify_e2e end-to-end driver. They are NOT part of the MCP surface.

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type CreateSchematicArgs struct{ SchematicPath string }
type AddSymbolArgs struct {
	SchematicPath string
	LibID         string
	Reference     string
	Value         string
	MountType     string
	Footprint     string
	Unit          int
	AutoPlace     bool
	Rotation      float64
	X, Y          float64
}
type ConnectNetlistArgs struct {
	SchematicPath string
	Connections   []NetConn
	Strategy      string
}
type AddPowerRailArgs struct {
	SchematicPath string
	LibID         string
	From          string
	Rotation      float64
}
type RelayoutArgs struct{ SchematicPath string }
type ExportArgs struct {
	SchematicPath string
	Format        string
}
type ConnectivityArgs struct{ SchematicPath string }
type ValidateArgs struct {
	SchematicPath string
	RunERC        bool // accepted for caller convenience; ERC always runs when SchematicPath is non-empty
}

func (e *Env) HandleCreateSchematicForTest(ctx context.Context, req *mcp.CallToolRequest, a CreateSchematicArgs) (*mcp.CallToolResult, any, error) {
	return e.handleCreateSchematicTool(ctx, req, createSchematicInput{SchematicPath: a.SchematicPath})
}

func (e *Env) HandleAddSymbolForTest(ctx context.Context, req *mcp.CallToolRequest, a AddSymbolArgs) (*mcp.CallToolResult, any, error) {
	return e.handleAddSymbol(ctx, req, addSymbolInput{
		SchematicPath: a.SchematicPath, LibID: a.LibID, Reference: a.Reference, Value: a.Value,
		MountType: a.MountType, Footprint: a.Footprint, Unit: a.Unit,
		AutoPlace: a.AutoPlace, Rotation: a.Rotation, X: a.X, Y: a.Y,
	})
}

func (e *Env) HandleConnectNetlistForTest(ctx context.Context, req *mcp.CallToolRequest, a ConnectNetlistArgs) (*mcp.CallToolResult, any, error) {
	return e.handleConnectNetlist(ctx, req, connectNetlistInput{
		SchematicPath: a.SchematicPath, Connections: a.Connections, Strategy: a.Strategy,
	})
}

func (e *Env) HandleRelayoutForTest(ctx context.Context, req *mcp.CallToolRequest, a RelayoutArgs) (*mcp.CallToolResult, any, error) {
	return e.handleRelayoutTool(ctx, req, relayoutInput{SchematicPath: a.SchematicPath})
}

func (e *Env) HandleExportForTest(ctx context.Context, req *mcp.CallToolRequest, a ExportArgs) (*mcp.CallToolResult, any, error) {
	return e.handleExportSchematicImage(ctx, req, exportSchematicImageInput{
		SchematicPath: a.SchematicPath, Format: a.Format,
	})
}

func (e *Env) HandleAddPowerRailForTest(ctx context.Context, req *mcp.CallToolRequest, a AddPowerRailArgs) (*mcp.CallToolResult, any, error) {
	return e.handleAddPowerRail(ctx, req, addPowerRailInput{
		SchematicPath: a.SchematicPath, LibID: a.LibID, From: a.From, Rotation: a.Rotation,
	})
}

func (e *Env) HandleConnectivityForTest(ctx context.Context, req *mcp.CallToolRequest, a ConnectivityArgs) (*mcp.CallToolResult, any, error) {
	return e.handleGetConnectivitySummary(ctx, req, readSchematicInput{SchematicPath: a.SchematicPath})
}

func (e *Env) HandleValidateForTest(ctx context.Context, req *mcp.CallToolRequest, a ValidateArgs) (*mcp.CallToolResult, any, error) {
	return e.handleValidateDesign(ctx, req, validateDesignInput{SchematicPath: a.SchematicPath})
}

type ApplyTemplateArgs struct {
	SchematicPath string
	Template      string
	AnchorX       float64
	AnchorY       float64
	PinMap        map[string]string
	RefMap        map[string]string
}

func (e *Env) HandleApplyTemplateForTest(ctx context.Context, req *mcp.CallToolRequest, a ApplyTemplateArgs) (*mcp.CallToolResult, any, error) {
	return e.handleApplyTemplate(ctx, req, applyTemplateInput{
		SchematicPath: a.SchematicPath, Template: a.Template,
		AnchorX: a.AnchorX, AnchorY: a.AnchorY, PinMap: a.PinMap, RefMap: a.RefMap,
	})
}
