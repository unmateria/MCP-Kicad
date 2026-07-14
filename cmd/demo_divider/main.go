// demo_divider rebuilds the LED + 2R divider through the MCP handlers
// directly (so we can compare layouts before/after PlaceFlow + exit segments).
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

	dir, _ := filepath.Abs("projects/divider_v2")
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o755)
	schPath := filepath.Join(dir, "divider.kicad_sch")
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

	add := func(libID, ref, val string) {
		r, _, err := env.HandleAddSymbolForTest(ctx, nil, tools.AddSymbolArgs{
			SchematicPath: schPath, LibID: libID, Reference: ref, Value: val,
			MountType: "THT", AutoPlace: true,
		})
		report("add_symbol "+ref, textOf(r), err)
	}
	add("Device:Battery_Cell", "BT1", "18650")
	add("Device:R", "R1", "1k")
	add("Device:R", "R2", "1k")
	add("Device:LED", "D1", "LED_RED")

	r, _, err = env.HandleConnectNetlistForTest(ctx, nil, tools.ConnectNetlistArgs{
		SchematicPath: schPath,
		Connections: []tools.NetConn{
			{Net: "VBAT", Pins: []string{"BT1.+", "R1.1"}},
			{Net: "MID", Pins: []string{"R1.2", "R2.1", "D1.A"}},
			{Net: "GND", Pins: []string{"R2.2", "D1.K", "BT1.-"}},
		},
		Strategy: "auto",
	})
	report("connect_netlist", textOf(r), err)

	r, _, err = env.HandleRelayoutForTest(ctx, nil, tools.RelayoutArgs{SchematicPath: schPath})
	report("relayout (PlaceFlow)", textOf(r), err)

	r, _, err = env.HandleConnectivityForTest(ctx, nil, tools.ConnectivityArgs{SchematicPath: schPath})
	report("get_connectivity_summary", textOf(r), err)

	r, _, err = env.HandleValidateForTest(ctx, nil, tools.ValidateArgs{SchematicPath: schPath})
	report("validate_design ERC", textOf(r), err)

	// Render PDF via kicad-cli directly (no headless browser needed).
	pdfOut := filepath.Join(cfg.OutputDir, "divider_v2.pdf")
	out, err := exec.Command(cfg.KicadCLI, "sch", "export", "pdf",
		"--output", pdfOut, "--exclude-drawing-sheet", "--no-background-color",
		schPath).CombinedOutput()
	fmt.Printf("\nkicad-cli pdf: %s\n", out)
	if err != nil {
		fmt.Println("kicad-cli error:", err)
	}
	fmt.Println("PDF:", pdfOut)

	// Use the export handler to also produce a PNG preview via Edge headless,
	// extracted from the inline image content.
	r, _, err = env.HandleExportForTest(ctx, nil, tools.ExportArgs{
		SchematicPath: schPath, Format: "svg",
	})
	if err != nil {
		fmt.Println("export error:", err)
		return
	}
	for _, c := range r.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			pngPath := filepath.Join(cfg.OutputDir, "divider_v2.png")
			if werr := os.WriteFile(pngPath, img.Data, 0o644); werr == nil {
				fmt.Println("PNG preview:", pngPath, "(", len(img.Data), "bytes)")
			}
		}
	}
}
