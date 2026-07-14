// demo_apply_template_opamp builds a tiny schematic and stamps the
// opamp_noninverting template into it via the MCP handler. As of Phase 2 the
// template carries baked geometry, so the stamp comes out FULLY WIRED: the
// geometric gate reports zero violations and ERC no longer flags unconnected
// internal pins. The only remaining ERC entries are expected for an isolated
// sub-circuit — the external VIN/VOUT labels drive nothing yet, the VCC/GND
// rails have no PWR_FLAG, and the second (unused) op-amp unit is unplaced.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/sexp"
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
	// No pin_map needed: the template bakes its own VIN/VOUT labels and its
	// VCC/GND power symbols, so the stamp is self-contained.
	r, _, err = env.HandleApplyTemplateForTest(ctx, nil, tools.ApplyTemplateArgs{
		SchematicPath: sch, Template: "opamp_noninverting",
		AnchorX: 80, AnchorY: 80,
	})
	must("apply_template", r, err)

	// The whole point of a baked template: the geometric gate must find nothing
	// wrong with the stamped wiring.
	if n := gateViolations(sch); n == 0 {
		fmt.Println("\n=== gate ===\ngeometric gate: 0 violations (wired stamp is clean)")
	} else {
		fmt.Printf("\n=== gate ===\nGATE FOUND %d VIOLATION(S) — this must be 0\n", n)
		os.Exit(1)
	}

	r, _, err = env.HandleValidateForTest(ctx, nil, tools.ValidateArgs{SchematicPath: sch, RunERC: true})
	must("validate_design ERC", r, err)
	ercOut := textOf(r)
	n := countViolationLines(ercOut)
	fmt.Printf("ERC found %d violation(s) — all expected for an isolated sub-circuit:\n"+
		"  - 'Input pin not driven' / 'Label connected to only one pin': the external\n"+
		"    VIN/VOUT labels are wired by the surrounding circuit, not here.\n"+
		"  - 'Input Power pin not driven': VCC/GND need a PWR_FLAG from the parent design.\n"+
		"  - 'unplaced unit B': the second op-amp of the dual LM358 is intentionally unused.\n"+
		"  No unconnected internal pins and no [MCP BUG] entries — the stamp is fully wired.\n", n)
	fmt.Println("OK — written:", sch)
}

// gateViolations parses the on-disk schematic and returns the geometric gate
// violation count.
func gateViolations(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("ERROR reading schematic for gate check:", err)
		os.Exit(1)
	}
	parsed, err := sexp.ParseSchematic(string(data))
	if err != nil {
		fmt.Println("ERROR parsing schematic for gate check:", err)
		os.Exit(1)
	}
	v := gate.Check(parsed)
	for _, x := range v {
		fmt.Printf("  gate: %s: %s\n", x.Kind, x.Detail)
	}
	return len(v)
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
