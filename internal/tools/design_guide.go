package tools

import (
	"context"
	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed design_guide.md
var designGuideMD string

// DesignGuideArgs is the (empty) MCP input for design_guide.
type DesignGuideArgs struct{}

func (e *Env) handleDesignGuide(_ context.Context, _ *mcp.CallToolRequest, _ DesignGuideArgs) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return toolText(designGuideMD), nil, nil
}
