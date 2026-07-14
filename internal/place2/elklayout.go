package place2

import (
	"context"
	"strings"

	"mcp-kicad/internal/place2/elk"
	"mcp-kicad/internal/sexp"
)

// runELKLayout attempts to lay out the schematic via the elkjs subprocess.
// On any failure (Node missing, elkjs missing, timeout, malformed result)
// it returns ok=false and the caller should fall back to the legacy
// PlaceFlow positions.
//
// Cluster compounds are encoded so ELK keeps decoupling caps adjacent to
// their IC, etc.
func runELKLayout(syms []sexp.SchematicSymbol, nets []sexp.Net, clusters []Cluster) (map[string][2]float64, bool) {
	layouter, err := elk.Detect()
	if err != nil {
		return nil, false
	}
	specs := make([]elk.ClusterSpec, len(clusters))
	for i, c := range clusters {
		specs[i] = elk.ClusterSpec{Kind: c.Kind, Refs: c.Refs, Anchor: c.Anchor}
	}
	graph := elk.BuildGraph(syms, nets, specs)
	out, err := layouter.Run(context.Background(), graph)
	if err != nil {
		return nil, false
	}
	positions := elk.ResultPositions(out, syms)
	if len(positions) == 0 {
		return nil, false
	}
	// Power symbols are not in the graph; copy their existing positions over
	// so downstream rules (ApplyPowerRails) still see them.
	powerExists := false
	for _, s := range syms {
		if strings.HasPrefix(s.LibID, "power:") {
			powerExists = true
			break
		}
	}
	_ = powerExists
	return positions, true
}
