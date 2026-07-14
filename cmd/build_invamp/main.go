// build_invamp constructs the inverting-amplifier reference schematic via the
// MCP tool handlers (no Claude Desktop, no MCP transport). Used to validate
// the placement+routing pipeline end-to-end after pillar fixes.
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

type sym struct {
	libID, ref, value string
	unit              int
}

func main() {
	cfg := config.Load("config.ini")
	env := &tools.Env{
		LibsRoot:        cfg.LibsRoot,
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		AnthropicKey:    cfg.Anthropic,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
		Log:             tools.NewSessionLogger(cfg.OutputDir),
	}

	schDir, _ := filepath.Abs("projects/test_pillar4")
	_ = os.RemoveAll(schDir)
	_ = os.MkdirAll(schDir, 0o755)
	schPath := filepath.Join(schDir, "inv_amp.kicad_sch")

	ctx := context.Background()
	req := &mcp.CallToolRequest{}

	report := func(name string, r *mcp.CallToolResult, err error) {
		fmt.Printf("\n=== %s ===\n", name)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(textOf(r))
	}

	r, _, err := env.HandleCreateSchematicForTest(ctx, req, tools.CreateSchematicArgs{SchematicPath: schPath})
	report("create", r, err)

	syms := []sym{
		{"Amplifier_Operational:NE5532", "U1", "NE5532", 1},
		{"Amplifier_Operational:NE5532", "U1", "NE5532", 3},
		{"Device:R", "R1", "1k", 1},
		{"Device:R", "R2", "10k", 1},
		{"Device:R", "R3", "10k", 1},
		{"Device:C", "C1", "100n", 1},
		{"Device:C", "C2", "100n", 1},
		{"Connector:Conn_01x02_Pin", "J1", "IN", 1},
		{"Connector:Conn_01x02_Pin", "J2", "OUT", 1},
	}
	for _, s := range syms {
		r, _, err := env.HandleAddSymbolForTest(ctx, req, tools.AddSymbolArgs{
			SchematicPath: schPath, LibID: s.libID, Reference: s.ref, Value: s.value, Unit: s.unit, AutoPlace: true,
		})
		if err != nil {
			fmt.Printf("add_symbol %s ERROR: %v\n", s.ref, err)
			os.Exit(1)
		}
		_ = r
	}
	fmt.Println("\n=== batch add_symbol ===\nadded 9 symbols")

	conns := []tools.NetConn{
		{Net: "IN", Pins: []string{"J1.Pin_1", "R1.1"}},
		{Net: "INV", Pins: []string{"R1.2", "R2.1", "U1.1.-"}},
		{Net: "OUT", Pins: []string{"U1.1.1", "R2.2", "J2.Pin_1"}},
		{Net: "BIAS", Pins: []string{"U1.1.+", "R3.1"}},
		{Net: "GND", Pins: []string{"J1.Pin_2", "J2.Pin_2", "R3.2", "C1.2", "C2.1"}},
		{Net: "+12V", Pins: []string{"U1.3.V+", "C1.1"}},
		{Net: "-12V", Pins: []string{"U1.3.V-", "C2.2"}},
	}
	r, _, err = env.HandleConnectNetlistForTest(ctx, req, tools.ConnectNetlistArgs{
		SchematicPath: schPath, Connections: conns, Strategy: "auto",
	})
	report("connect_netlist", r, err)

	r, _, err = env.HandleRelayoutForTest(ctx, req, tools.RelayoutArgs{SchematicPath: schPath})
	report("relayout", r, err)

	r, _, err = env.HandleValidateForTest(ctx, req, tools.ValidateArgs{SchematicPath: schPath})
	report("validate ERC", r, err)

	fmt.Printf("\nSchematic at: %s\n", schPath)
}
