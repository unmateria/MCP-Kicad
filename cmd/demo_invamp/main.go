// demo_invamp builds the inverting op-amp circuit through the MCP handlers
// directly so we can see the layout result of the intent-persistence fix
// without needing to reconnect the live MCP after each rebuild.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		LibsRoot:        cfg.LibsRoot,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
	}

	dir, _ := filepath.Abs("projects/inv_amp_v2")
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o755)
	schPath := filepath.Join(dir, "inv_amp.kicad_sch")
	ctx := context.Background()

	report := func(name, out string, err error) {
		fmt.Printf("\n=== %s ===\n", name)
		if err != nil {
			fmt.Println("ERROR:", err)
			os.Exit(1)
		}
		if len(out) > 1000 {
			out = out[:1000] + "..."
		}
		fmt.Println(out)
	}

	r, _, err := env.HandleCreateSchematicForTest(ctx, nil, tools.CreateSchematicArgs{SchematicPath: schPath})
	report("create_schematic", textOf(r), err)

	add := func(libID, ref, val string, unit int) {
		r, _, err := env.HandleAddSymbolForTest(ctx, nil, tools.AddSymbolArgs{
			SchematicPath: schPath, LibID: libID, Reference: ref, Value: val,
			MountType: "THT", AutoPlace: true, Unit: unit,
		})
		report("add_symbol "+ref, textOf(r), err)
	}
	add("Amplifier_Operational:NE5532", "U1", "NE5532", 1)
	add("Amplifier_Operational:NE5532", "U1", "NE5532", 3)
	add("Device:R", "R1", "10k", 0)
	add("Device:R", "R2", "100k", 0)
	add("Device:C", "C1", "100n", 0)
	add("Device:C", "C2", "100n", 0)

	r, _, err = env.HandleConnectNetlistForTest(ctx, nil, tools.ConnectNetlistArgs{
		SchematicPath: schPath,
		// IMPORTANT: pin order encodes signal flow. R1 first → R1 is the input
		// driving INV_NODE. U1.1 first in VOUT → op-amp output drives R2.
		Connections: []tools.NetConn{
			{Net: "INV_NODE", Pins: []string{"R1.2", "U1.1.-", "R2.1"}},
			{Net: "VOUT", Pins: []string{"U1.1.1", "R2.2"}},
			{Net: "GND", Pins: []string{"U1.1.+", "C1.2", "C2.1"}},
			{Net: "+12V", Pins: []string{"U1.3.V+", "C1.1"}},
			{Net: "-12V", Pins: []string{"U1.3.V-", "C2.2"}},
		},
		Strategy: "auto",
	})
	report("connect_netlist", textOf(r), err)

	r, _, err = env.HandleRelayoutForTest(ctx, nil, tools.RelayoutArgs{SchematicPath: schPath})
	report("relayout", textOf(r), err)

	r, _, err = env.HandleConnectivityForTest(ctx, nil, tools.ConnectivityArgs{SchematicPath: schPath})
	report("get_connectivity_summary", textOf(r), err)

	r, _, err = env.HandleValidateForTest(ctx, nil, tools.ValidateArgs{SchematicPath: schPath})
	report("validate_design ERC", textOf(r), err)

	pdfOut := filepath.Join(cfg.OutputDir, "inv_amp_v2.pdf")
	_, _ = exec.Command(cfg.KicadCLI, "sch", "export", "pdf",
		"--output", pdfOut, "--exclude-drawing-sheet", "--no-background-color",
		schPath).CombinedOutput()
	fmt.Println("PDF:", pdfOut)
}
