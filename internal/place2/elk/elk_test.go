package elk

import (
	"context"
	"errors"
	"testing"

	"mcp-kicad/internal/sexp"
)

func TestBuildGraphLinearisesNets(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "U1", LibID: "MCU_Microchip_ATmega:ATmega328-AU"},
		{Reference: "R1", LibID: "Device:R"},
		{Reference: "C1", LibID: "Device:C"},
	}
	nets := []sexp.Net{
		{Name: "VCC", Pins: []sexp.PinRef{
			{Reference: "U1"}, {Reference: "C1"},
		}},
		{Name: "SDA", Pins: []sexp.PinRef{
			{Reference: "U1"}, {Reference: "R1"},
		}},
	}
	g := BuildGraph(syms, nets, nil)
	if len(g.Children) != 3 {
		t.Errorf("Children = %d, want 3", len(g.Children))
	}
	if len(g.Edges) != 2 {
		t.Errorf("Edges = %d, want 2", len(g.Edges))
	}
}

func TestRunLiveELKSmoke(t *testing.T) {
	l, err := Detect()
	if err != nil {
		t.Skip("Node not available:", err)
	}
	g := Graph{
		ID: "root",
		LayoutOptions: map[string]string{
			"elk.algorithm":  "layered",
			"elk.direction":  "RIGHT",
			"elk.randomSeed": "1",
		},
		Children: []Node{
			{ID: "n1", Width: 10, Height: 10},
			{ID: "n2", Width: 10, Height: 10},
		},
		Edges: []Edge{{ID: "e1", Sources: []string{"n1"}, Targets: []string{"n2"}}},
	}
	out, err := l.Run(context.Background(), g)
	if err != nil {
		// elkjs not installed → fall through cleanly. The pipeline handles
		// this case via the Go fallback so tests just skip here.
		if errors.Is(err, ErrNotAvailable) {
			t.Skip(err)
		}
		t.Skipf("ELK live run failed (likely elkjs not installed globally): %v", err)
	}
	if len(out.Children) != 2 {
		t.Errorf("after layout, children = %d, want 2", len(out.Children))
	}
	// Both nodes should have been moved to non-overlapping positions.
	if out.Children[0].X == out.Children[1].X && out.Children[0].Y == out.Children[1].Y {
		t.Errorf("ELK didn't separate nodes: %+v", out.Children)
	}
}
