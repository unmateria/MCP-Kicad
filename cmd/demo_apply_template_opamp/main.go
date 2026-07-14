// demo_apply_template_opamp builds a tiny schematic and stamps the
// opamp_noninverting template into it via the MCP handler. The template only
// places symbols (no wiring), so ERC is expected to report unconnected-pin
// violations here — this demo verifies validate_design surfaces them, not
// that ERC passes cleanly.
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
	// The stamped template has no wires (every pin floating), so ERC is
	// expected to report violations here — this demo only exercises the
	// apply_template stamping step, not a fully wired circuit. Report the
	// violation count instead of pretending ERC passed.
	ercOut := textOf(r)
	if n := countViolationLines(ercOut); n > 0 {
		fmt.Printf("ERC found %d violation(s) — see above (expected: template stamps floating pins, no wires)\n", n)
	} else {
		fmt.Println("ERC: OK")
	}
	fmt.Println("OK — written:", sch)
}

// countViolationLines counts violation entries in the formatted ERC output
// (each violation line is indented and starts with a "[CATEGORY]" tag, e.g.
// "  [FIXABLE] Pin not connected → ..."). Returns 0 for "ERC: OK".
func countViolationLines(ercOut string) int {
	n := 0
	for _, line := range strings.Split(ercOut, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			n++
		}
	}
	return n
}

func must(name string, r *mcp.CallToolResult, err error) {
	fmt.Printf("\n=== %s ===\n%s\n", name, textOf(r))
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
}
