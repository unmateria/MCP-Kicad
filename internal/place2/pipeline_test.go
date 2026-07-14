package place2

import (
	"testing"

	"mcp-kicad/internal/place2/cluster"
)

func TestApplyClusterPullDecoupling(t *testing.T) {
	// Synthetic placement: anchor IC at (100, 100), cap C1 far away at (300, 300).
	// After pull, C1 should sit within a few cm of U1.
	positions := map[string][2]float64{
		"U1": {100, 100},
		"C1": {300, 300},
	}
	clusters := []Cluster{
		{Kind: "decoupling", Anchor: "U1", Refs: []string{"U1", "C1"}},
	}
	moved := ApplyClusterPull(nil, clusters, positions)
	if moved != 1 {
		t.Fatalf("ApplyClusterPull moved %d, want 1", moved)
	}
	c1 := positions["C1"]
	dx := c1[0] - 100
	dy := c1[1] - 100
	dist := dx*dx + dy*dy
	if dist > 50*50 {
		t.Errorf("C1 distance² = %.1f, want < 2500 (sqrt = 50 mm)", dist)
	}
}

func TestConvertClustersIdentity(t *testing.T) {
	in := []cluster.Cluster{{Kind: "decoupling", Anchor: "U1", Refs: []string{"U1", "C1"}}}
	out := ConvertClusters(in)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Kind != "decoupling" || out[0].Anchor != "U1" {
		t.Errorf("ConvertClusters returned %+v", out[0])
	}
}
