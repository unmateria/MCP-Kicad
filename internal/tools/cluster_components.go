package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/place2/cluster"
	_ "mcp-kicad/internal/place2/cluster/canonical" // register extra detectors
	"mcp-kicad/internal/sexp"
)

type clusterComponentsInput struct {
	SchematicPath string `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
}

// handleClusterComponents reads a schematic, runs the cluster detector, and
// returns a human-readable list. Read-only — never mutates the file. Useful
// for the LLM to introspect WHY the layout will group certain symbols
// together, and to debug rules that aren't firing as expected.
func (e *Env) handleClusterComponents(_ context.Context, _ *mcp.CallToolRequest, in clusterComponentsInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if in.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	data, err := os.ReadFile(in.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}
	syms := sexp.ReadSymbols(sch)
	nets := sexp.TraceNets(sch)
	clusters := cluster.Detect(syms, nets)

	var sb strings.Builder
	if len(clusters) == 0 {
		sb.WriteString("no clusters detected.\n")
		sb.WriteString("\nrules that would have applied:\n")
		sb.WriteString("  decoupling     — IC + capacitor with both pins on Vcc/GND\n")
		sb.WriteString("  pullup         — resistor between Vcc and a signal pin on an IC\n")
		sb.WriteString("  lc_filter      — inductor on a switch node + cap to GND\n")
		sb.WriteString("  crystal        — Y/X*tal + 2 load caps\n")
		sb.WriteString("  voltage_divider— two Rs between Vcc and GND sharing a tap\n")
		sb.WriteString("  opamp_feedback — op-amp + Rs forming the feedback loop\n")
		sb.WriteString("  header         — connector + every component on its nets\n")
		return toolText(sb.String()), nil, nil
	}
	fmt.Fprintf(&sb, "%d cluster(s) detected:\n\n", len(clusters))
	for i, c := range clusters {
		fmt.Fprintf(&sb, "%d. %s — anchor %s\n", i+1, c.Kind, c.Anchor)
		fmt.Fprintf(&sb, "   members: %s\n", strings.Join(c.Refs, ", "))
	}
	return toolText(sb.String()), nil, nil
}
