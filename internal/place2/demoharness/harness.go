// Package demoharness centralises the boilerplate shared by all canonical
// demo binaries (cmd/demo_*). Each demo declares a Spec with its symbols,
// nets, and power rails and the harness drives them through the existing
// MCP handlers, then prints layout-quality metrics + the schematic path.
package demoharness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
	"mcp-kicad/internal/tools"
)

// Symbol describes one component to add via add_symbol(auto_place=true).
type Symbol struct {
	LibID     string
	Reference string
	Value     string
	Unit      int // 0 = single-unit
	MountType string
}

// PowerRail describes one add_power_rail call applied AFTER connect_netlist.
type PowerRail struct {
	LibID string // e.g. power:GND, power:+5V
	From  string // pin reference, e.g. "U1.1.+", "U1.GND"
}

// Spec defines a canonical demo: project name + components + connections.
type Spec struct {
	Project     string // unique directory name under projects/
	Description string
	Symbols     []Symbol
	Nets        []tools.NetConn // signal-flow ordered (first pin = upstream source)
	PowerRails  []PowerRail
	Strategy    string // routing strategy passed to connect_netlist; default "auto"
}

// Run executes the demo end-to-end and writes (1) the .kicad_sch in
// projects/<project>/ (2) a printout of layout metrics on stdout.
//
// On any handler error the function calls os.Exit(1).
func (s Spec) Run() {
	cfg := config.Load("config.ini")
	env := &tools.Env{
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		LibsRoot:        cfg.LibsRoot,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
	}

	dir, _ := filepath.Abs(filepath.Join("projects", s.Project))
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o755)
	schPath := filepath.Join(dir, s.Project+".kicad_sch")

	ctx := context.Background()

	fmt.Printf("==== %s ====\n", s.Project)
	if s.Description != "" {
		fmt.Println(s.Description)
	}

	step := func(name string, r *mcp.CallToolResult, err error) {
		if err != nil {
			fmt.Printf("  %s: ERROR %v\n", name, err)
			os.Exit(1)
		}
		out := textOf(r)
		first := strings.SplitN(out, "\n", 2)[0]
		if len(first) > 100 {
			first = first[:100] + "..."
		}
		fmt.Printf("  %s: %s\n", name, first)
	}

	r, _, err := env.HandleCreateSchematicForTest(ctx, nil, tools.CreateSchematicArgs{SchematicPath: schPath})
	step("create_schematic", r, err)

	for _, sym := range s.Symbols {
		mt := sym.MountType
		if mt == "" {
			mt = "THT"
		}
		r, _, err := env.HandleAddSymbolForTest(ctx, nil, tools.AddSymbolArgs{
			SchematicPath: schPath, LibID: sym.LibID, Reference: sym.Reference, Value: sym.Value,
			MountType: mt, AutoPlace: true, Unit: sym.Unit,
		})
		step("add_symbol "+sym.Reference, r, err)
	}

	strat := s.Strategy
	if strat == "" {
		strat = "auto"
	}
	r, _, err = env.HandleConnectNetlistForTest(ctx, nil, tools.ConnectNetlistArgs{
		SchematicPath: schPath, Connections: s.Nets, Strategy: strat,
	})
	step("connect_netlist", r, err)

	for _, pr := range s.PowerRails {
		r, _, err := env.HandleAddPowerRailForTest(ctx, nil, tools.AddPowerRailArgs{
			SchematicPath: schPath, LibID: pr.LibID, From: pr.From,
		})
		step("add_power_rail "+pr.LibID+"→"+pr.From, r, err)
	}

	r, _, err = env.HandleRelayoutForTest(ctx, nil, tools.RelayoutArgs{SchematicPath: schPath})
	step("relayout", r, err)

	r, _, err = env.HandleConnectivityForTest(ctx, nil, tools.ConnectivityArgs{SchematicPath: schPath})
	step("connectivity_summary", r, err)

	// Always read + parse for metrics, even if the file wasn't written by an
	// optional later step.
	data, err := os.ReadFile(schPath)
	if err != nil {
		fmt.Println("read:", err)
		os.Exit(1)
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		fmt.Println("parse:", err)
		os.Exit(1)
	}
	m := metrics.Compute(sch)

	fmt.Println("---- metrics ----")
	fmt.Print(m.String())
	fmt.Println("schematic:", schPath)

	// Best-effort PDF export. Failure is non-fatal — most demos are useful
	// without rendering, especially in CI containers without kicad-cli.
	if cfg.KicadCLI != "" {
		pdfOut := filepath.Join(cfg.OutputDir, s.Project+".pdf")
		if out, err := exec.Command(cfg.KicadCLI, "sch", "export", "pdf",
			"--output", pdfOut, "--exclude-drawing-sheet", "--no-background-color",
			schPath).CombinedOutput(); err != nil {
			fmt.Printf("pdf export skipped: %v %s\n", err, string(out))
		} else {
			fmt.Println("pdf:", pdfOut)
		}
	}
}

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
