package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/place2/templates"
	"mcp-kicad/internal/sexp"
)

// --- list_templates ---

type listTemplatesInput struct{}

func (e *Env) handleListTemplates(_ context.Context, _ *mcp.CallToolRequest, _ listTemplatesInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	ts, err := templates.List()
	if err != nil {
		return toolText(fmt.Sprintf("error listing templates: %v", err)), nil, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d templates available:\n\n", len(ts))
	for _, t := range ts {
		fmt.Fprintf(&sb, "• %s — %s\n", t.Name, t.Description)
		if len(t.ExternalPins) > 0 {
			sb.WriteString("    external pins to wire:\n")
			for _, p := range t.ExternalPins {
				fmt.Fprintf(&sb, "      %-8s (%s) — %s\n", p.Label, p.From, p.Describe)
			}
		}
	}
	return toolText(sb.String()), nil, nil
}

// --- apply_template ---

type applyTemplateInput struct {
	SchematicPath string            `json:"schematic_path"   jsonschema:"Path to the .kicad_sch file"`
	Template      string            `json:"template"         jsonschema:"Template name (see list_templates), e.g. opamp_noninverting"`
	AnchorX       float64           `json:"anchor_x"         jsonschema:"Top-left X (mm) where the template is stamped"`
	AnchorY       float64           `json:"anchor_y"         jsonschema:"Top-left Y (mm) where the template is stamped"`
	PinMap        map[string]string `json:"pin_map,omitempty" jsonschema:"role-qualified pin (e.g. R_SDA.1) → external net name"`
	RefMap        map[string]string `json:"ref_map,omitempty" jsonschema:"role → desired reference designator (else auto-allocated)"`
}

func (e *Env) handleApplyTemplate(_ context.Context, _ *mcp.CallToolRequest, in applyTemplateInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if in.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	if in.Template == "" {
		return toolText("error: template is required (see list_templates)"), nil, nil
	}
	tpl, err := templates.Get(in.Template)
	if err != nil {
		return toolText(fmt.Sprintf("error: template %q not found: %v", in.Template, err)), nil, nil
	}
	data, err := os.ReadFile(in.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}
	stampRes, err := templates.Stamp(sch, tpl, templates.StampOptions{
		Anchor:    [2]float64{in.AnchorX, in.AnchorY},
		RefMap:    in.RefMap,
		PinMap:    in.PinMap,
		EmbedFunc: func(libID string) error { return e.embedLibSymbol(sch, libID) },
	})
	if err != nil {
		return toolText(fmt.Sprintf("error stamping: %v", err)), nil, nil
	}
	if err := os.WriteFile(in.SchematicPath, []byte(sch.Serialize()), 0o644); err != nil {
		return toolText(fmt.Sprintf("error writing schematic: %v", err)), nil, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "applied template %q at (%.2f,%.2f): placed %d symbols (%s), %d external labels\n",
		tpl.Name, in.AnchorX, in.AnchorY, len(stampRes.PlacedRefs), strings.Join(stampRes.PlacedRefs, ", "), stampRes.LabelsAdded)
	for _, n := range stampRes.Notes {
		fmt.Fprintf(&sb, "  NOTE: %s\n", n)
	}
	return toolText(sb.String()), nil, nil
}

// --- layout_metrics ---

type layoutMetricsInput struct {
	SchematicPath string `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
}

func (e *Env) handleLayoutMetrics(_ context.Context, _ *mcp.CallToolRequest, in layoutMetricsInput) (res *mcp.CallToolResult, _ any, _ error) {
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
	m := metrics.Compute(sch)
	return toolText(m.String()), nil, nil
}
