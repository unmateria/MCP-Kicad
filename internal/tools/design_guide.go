package tools

import (
	"context"
	_ "embed"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The guide is served as three parts, in the order an author actually needs
// them: a complete worked example, then the normative syntax, then the design
// criteria.
//
// The syntax used to live only in docs/compiler/FORMAT.md, which a client with
// no filesystem cannot read — so the first real user session had to reverse
// engineer the grammar from rejection messages ("version" missing, "symbols"
// not "components", nets a map not an array). Shipping the spec inside the
// binary is the fix: whoever can call the tool can read the format.
//
//go:embed design_example.md
var designExampleMD string

//go:embed design_format.md
var designFormatMD string

//go:embed design_guide.md
var designGuideMD string

// DesignGuideArgs is the (empty) MCP input for design_guide.
type DesignGuideArgs struct{}

func (e *Env) handleDesignGuide(_ context.Context, _ *mcp.CallToolRequest, _ DesignGuideArgs) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	return toolText(strings.Join([]string{
		designExampleMD,
		designFormatMD,
		"\n---\n",
		designGuideMD,
	}, "\n")), nil, nil
}
