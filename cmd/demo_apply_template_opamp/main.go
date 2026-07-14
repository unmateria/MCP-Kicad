// demo_apply_template_opamp builds a tiny schematic and stamps the
// opamp_noninverting template into it via the MCP handler. ERC must pass.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

func textOf(r *mcp.CallToolResult) string {
	if r == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

func main() {
	cfg := config.Load("config.ini")
	env := &tools.Env{
		LibsRoot:        cfg.LibsRoot,
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
	}
	dir, _ := filepath.Abs("projects/demo_apply_template_opamp")
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o755)
	sch := filepath.Join(dir, "stamp.kicad_sch")
	ctx := context.Background()

	r, _, err := env.HandleCreateSchematicForTest(ctx, nil, tools.CreateSchematicArgs{SchematicPath: sch})
	must("create_schematic", r, err)
	r, _, err = env.HandleApplyTemplateForTest(ctx, nil, tools.ApplyTemplateArgs{
		SchematicPath: sch, Template: "opamp_noninverting",
		AnchorX: 80, AnchorY: 80,
		PinMap: map[string]string{"U.+": "VIN", "U.-": "FB", "U.OUT": "VOUT"},
	})
	must("apply_template", r, err)
	r, _, err = env.HandleValidateForTest(ctx, nil, tools.ValidateArgs{SchematicPath: sch, RunERC: true})
	must("validate_design ERC", r, err)
	fmt.Println("OK — written:", sch)
}

func must(name string, r *mcp.CallToolResult, err error) {
	fmt.Printf("\n=== %s ===\n%s\n", name, textOf(r))
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
}
